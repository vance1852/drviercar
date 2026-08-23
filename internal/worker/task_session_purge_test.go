package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestSessionPurgeKeepsLiveSessions runs the session maintenance job in the middle
// of a shift and checks that operators who are still inside their session validity
// window stay logged in, while sessions whose window elapsed are reclaimed.
func TestSessionPurgeKeepsLiveSessions(t *testing.T) {
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	defer func() { _ = harness.Close() }()
	ctx := context.Background()

	actors, err := harness.SeedActors(ctx)
	if err != nil {
		t.Fatalf("seed actors: %v", err)
	}

	harness.Clock.Advance(30 * time.Minute)
	if err := harness.Maintenance.PurgeSessions(ctx, nil); err != nil {
		t.Fatalf("session maintenance during the shift: %v", err)
	}

	onDuty := map[string]string{
		"fleet administrator":  actors.AdminToken,
		"safety operator lin":  actors.OperatorToken,
		"safety operator zhao": actors.SecondToken,
	}
	for role, token := range onDuty {
		principal, authErr := harness.Auth.Authenticate(ctx, token)
		if authErr != nil {
			t.Fatalf("the %s must stay logged in after session maintenance: %v", role, authErr)
		}
		if principal.OperatorID == 0 {
			t.Fatalf("the %s must resolve to a real operator, got %+v", role, principal)
		}
	}

	remaining, err := harness.Store.Repos().Sessions.ByTokenHash(ctx,
		domain.HashSessionToken(actors.OperatorToken))
	if err != nil {
		t.Fatalf("the live session row must still exist: %v", err)
	}
	if remaining.Revoked() {
		t.Fatal("session maintenance must not revoke a live session")
	}

	harness.Clock.Advance(3 * time.Hour)
	if err := harness.Maintenance.PurgeSessions(ctx, nil); err != nil {
		t.Fatalf("session maintenance after the shift: %v", err)
	}
	for role, token := range onDuty {
		if _, authErr := harness.Auth.Authenticate(ctx, token); authErr == nil {
			t.Fatalf("the %s session must be gone once its validity window elapsed", role)
		} else if apperr.CodeOf(authErr) != "session_unknown" {
			t.Fatalf("an elapsed session must be reported as unknown for the %s, got %s",
				role, apperr.CodeOf(authErr))
		}
	}
}
