package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/service/auth"
	"github.com/vance1852/drviercar/internal/testsupport"
)

func newHarness(t *testing.T) *testsupport.Harness {
	t.Helper()
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	t.Cleanup(func() { _ = harness.Close() })
	return harness
}

func TestLoginIssuesUsableSessionAndHidesCredentials(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	if _, err := harness.Auth.Register(ctx, auth.RegisterInput{
		Username: "lin-wei", DisplayName: "Lin Wei",
		Password: "shadow-mode-1", Role: domain.RoleSafetyOperator,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	result, err := harness.Auth.Login(ctx, auth.Credentials{Username: "lin-wei", Password: "shadow-mode-1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if result.Token == "" {
		t.Fatal("login must issue a bearer token")
	}
	stored, err := harness.Store.Repos().Sessions.ByTokenHash(ctx, domain.HashSessionToken(result.Token))
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored.TokenHash == result.Token {
		t.Fatal("the raw token must never be persisted")
	}
	principal, err := harness.Auth.Authenticate(ctx, result.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if principal.Role != domain.RoleSafetyOperator || principal.Username != "lin-wei" {
		t.Fatalf("unexpected principal %+v", principal)
	}
}

func TestLoginRejectsWrongCredentialsWithoutLeakingWhichPartFailed(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	if _, err := harness.Auth.Register(ctx, auth.RegisterInput{
		Username: "zhao-min", Password: "night-drive-9", Role: domain.RoleSafetyOperator,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, wrongPassword := harness.Auth.Login(ctx, auth.Credentials{Username: "zhao-min", Password: "wrong-secret"})
	_, unknownUser := harness.Auth.Login(ctx, auth.Credentials{Username: "nobody", Password: "night-drive-9"})
	if !errors.Is(wrongPassword, apperr.ErrUnauthenticated) || !errors.Is(unknownUser, apperr.ErrUnauthenticated) {
		t.Fatalf("both attempts must be unauthenticated: %v / %v", wrongPassword, unknownUser)
	}
	if apperr.CodeOf(wrongPassword) != apperr.CodeOf(unknownUser) {
		t.Fatalf("the failure code must not distinguish the cases: %s / %s",
			apperr.CodeOf(wrongPassword), apperr.CodeOf(unknownUser))
	}
	if _, err := harness.Auth.Register(ctx, auth.RegisterInput{
		Username: "zhao-min", Password: "another-secret", Role: domain.RoleSafetyOperator,
	}); !errors.Is(err, apperr.ErrAlreadyExists) {
		t.Fatalf("a duplicate username must conflict, got %v", err)
	}
	if _, err := harness.Auth.Register(ctx, auth.RegisterInput{
		Username: "short", Password: "tiny", Role: domain.RoleSafetyOperator,
	}); !errors.Is(err, apperr.ErrInvalidArgument) {
		t.Fatalf("a short password must be rejected, got %v", err)
	}
}

func TestLogoutRevokesTheSessionImmediately(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	if _, err := harness.Auth.Register(ctx, auth.RegisterInput{
		Username: "revoke-me", Password: "revoke-secret-1", Role: domain.RoleFleetAdmin,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	login, err := harness.Auth.Login(ctx, auth.Credentials{Username: "revoke-me", Password: "revoke-secret-1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := harness.Auth.Authenticate(ctx, login.Token); err != nil {
		t.Fatalf("authenticate before logout: %v", err)
	}
	if err := harness.Auth.Logout(ctx, login.Token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	_, err = harness.Auth.Authenticate(ctx, login.Token)
	if !errors.Is(err, apperr.ErrUnauthenticated) {
		t.Fatalf("a revoked session must not authenticate, got %v", err)
	}
	if apperr.CodeOf(err) != "session_revoked" {
		t.Fatalf("unexpected error code %s", apperr.CodeOf(err))
	}
	if err := harness.Auth.Logout(ctx, login.Token); err == nil {
		t.Fatal("logging out twice must be refused")
	}
}

func TestExpiredSessionsAreRejectedAndPurged(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	if _, err := harness.Auth.Register(ctx, auth.RegisterInput{
		Username: "expire-me", Password: "expire-secret-1", Role: domain.RoleSafetyOperator,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	login, err := harness.Auth.Login(ctx, auth.Credentials{Username: "expire-me", Password: "expire-secret-1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	harness.Clock.Advance(3 * time.Hour)
	_, err = harness.Auth.Authenticate(ctx, login.Token)
	if !errors.Is(err, apperr.ErrSessionExpired) {
		t.Fatalf("an expired session must be refused, got %v", err)
	}
	if apperr.KindOf(err) != apperr.KindUnauthorized {
		t.Fatalf("expired sessions map to unauthorized, got %v", apperr.KindOf(err))
	}
	purged, err := harness.Auth.PurgeExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if purged != 1 {
		t.Fatalf("one expired session expected, got %d", purged)
	}
	if _, err := harness.Auth.Authenticate(ctx, login.Token); apperr.CodeOf(err) != "session_unknown" {
		t.Fatalf("a purged session must be unknown, got %s", apperr.CodeOf(err))
	}
}

func TestAdministratorCanRevokeEverySessionOfAnOperator(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	actors, err := harness.SeedActors(ctx)
	if err != nil {
		t.Fatalf("seed actors: %v", err)
	}
	second, err := harness.Auth.Login(ctx, auth.Credentials{Username: "safety-lin", Password: "driver-secret-1"})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	revoked, err := harness.Auth.RevokeOperatorSessions(ctx, actors.Admin, actors.Operator.OperatorID)
	if err != nil {
		t.Fatalf("revoke sessions: %v", err)
	}
	if revoked != 2 {
		t.Fatalf("both sessions of the operator must be revoked, got %d", revoked)
	}
	if _, err := harness.Auth.Authenticate(ctx, actors.OperatorToken); err == nil {
		t.Fatal("the first session must stop working")
	}
	if _, err := harness.Auth.Authenticate(ctx, second.Token); err == nil {
		t.Fatal("the second session must stop working")
	}
	if _, err := harness.Auth.Authenticate(ctx, actors.AdminToken); err != nil {
		t.Fatalf("the administrator session must stay valid: %v", err)
	}
	if _, err := harness.Auth.RevokeOperatorSessions(ctx, actors.Operator, actors.Admin.OperatorID); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("a safety operator must not revoke sessions, got %v", err)
	}
}

func TestDisabledOperatorCannotAuthenticate(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	operator, err := harness.Auth.Register(ctx, auth.RegisterInput{
		Username: "disable-me", Password: "disable-secret-1", Role: domain.RoleSafetyOperator,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	login, err := harness.Auth.Login(ctx, auth.Credentials{Username: "disable-me", Password: "disable-secret-1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := harness.Store.Repos().Operators.SetActive(ctx, operator.ID, false); err != nil {
		t.Fatalf("disable operator: %v", err)
	}
	if _, err := harness.Auth.Authenticate(ctx, login.Token); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("a disabled operator must not authenticate, got %v", err)
	}
	if _, err := harness.Auth.Login(ctx, auth.Credentials{
		Username: "disable-me", Password: "disable-secret-1",
	}); !errors.Is(err, apperr.ErrPermissionDenied) {
		t.Fatalf("a disabled operator must not log in, got %v", err)
	}
}

func TestSessionAuditTrailRecordsLoginAndLogout(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	if _, err := harness.Auth.Register(ctx, auth.RegisterInput{
		Username: "audited", Password: "audited-secret-1", Role: domain.RoleFleetAdmin,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	login, err := harness.Auth.Login(ctx, auth.Credentials{Username: "audited", Password: "audited-secret-1"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	principal, err := harness.Auth.Authenticate(ctx, login.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if err := harness.Auth.Logout(ctx, login.Token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	events, err := harness.Store.Repos().Audit.ByObject(ctx, "session", principal.SessionID)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("login and logout must both be audited, got %d", len(events))
	}
	if events[0].Action != "session.login" || events[1].Action != "session.logout" {
		t.Fatalf("unexpected audit actions %s / %s", events[0].Action, events[1].Action)
	}
	for _, event := range events {
		for key, value := range event.Detail {
			if key == "password" || value == "audited-secret-1" {
				t.Fatalf("the audit trail must not contain credentials: %s=%s", key, value)
			}
		}
	}
}

func TestEmptyTokenIsRejected(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	if _, err := harness.Auth.Authenticate(ctx, "   "); apperr.CodeOf(err) != "session_token_missing" {
		t.Fatalf("an empty token must be refused, got %v", err)
	}
	if err := harness.Auth.Logout(ctx, ""); apperr.CodeOf(err) != "session_token_missing" {
		t.Fatalf("logging out without a token must be refused, got %v", err)
	}
}
