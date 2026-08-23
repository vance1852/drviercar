// Package auth implements login, session lifecycle and role resolution.
package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/audit"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/repository"
)

// Service owns identity and session behaviour.
type Service struct {
	store    repository.Store
	clock    clock.Clock
	recorder *audit.Recorder
	ttl      time.Duration
}

// Config configures the auth service.
type Config struct {
	SessionTTL time.Duration
}

// NewService builds the auth service.
func NewService(store repository.Store, source clock.Clock, recorder *audit.Recorder, config Config) *Service {
	if source == nil {
		source = clock.System{}
	}
	if recorder == nil {
		recorder = audit.NewRecorder(source)
	}
	ttl := config.SessionTTL
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	return &Service{store: store, clock: source, recorder: recorder, ttl: ttl}
}

// RegisterInput describes a new operator.
type RegisterInput struct {
	Username    string
	DisplayName string
	Password    string
	Role        domain.Role
}

// Register creates an operator with a hashed credential.
func (s *Service) Register(ctx context.Context, input RegisterInput) (*domain.Operator, error) {
	username := strings.TrimSpace(input.Username)
	if username == "" {
		return nil, apperr.Invalidf("operator_username_required", "登录名不能为空")
	}
	if len(input.Password) < 8 {
		return nil, apperr.Invalidf("operator_password_too_short", "口令长度至少 8 位")
	}
	if !input.Role.Valid() {
		return nil, apperr.Invalidf("operator_role_invalid", "未知的操作员角色 %q", string(input.Role))
	}
	salt, err := domain.NewSalt()
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	operator := &domain.Operator{
		Username:     username,
		DisplayName:  strings.TrimSpace(input.DisplayName),
		Role:         input.Role,
		Salt:         salt,
		PasswordHash: domain.HashPassword(salt, input.Password),
		Active:       true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	var created *domain.Operator
	err = s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		id, createErr := tx.Operators.Create(ctx, operator)
		if createErr != nil {
			return createErr
		}
		operator.ID = id
		if auditErr := s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: id,
			ObjectType: "operator",
			ObjectID:   id,
			Action:     "operator.register",
			Detail:     audit.Detail("role", string(operator.Role), "username", operator.Username),
		}); auditErr != nil {
			return auditErr
		}
		created = operator.Clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// Credentials carries a login attempt.
type Credentials struct {
	Username string
	Password string
}

// LoginResult carries the issued bearer token.
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	Operator  *domain.Operator
}

// Login verifies the credentials and issues a server side session.
func (s *Service) Login(ctx context.Context, credentials Credentials) (*LoginResult, error) {
	operator, err := s.store.Repos().Operators.ByUsername(ctx, strings.TrimSpace(credentials.Username))
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return nil, apperr.Wrap(apperr.ErrUnauthenticated, apperr.KindUnauthorized,
				"login_rejected", "登录名或口令不正确")
		}
		return nil, err
	}
	if !operator.Active {
		return nil, apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"operator_disabled", "该账号已被停用")
	}
	if !operator.VerifyPassword(credentials.Password) {
		return nil, apperr.Wrap(apperr.ErrUnauthenticated, apperr.KindUnauthorized,
			"login_rejected", "登录名或口令不正确")
	}
	token, err := domain.NewSessionToken()
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	session := &domain.Session{
		TokenHash:  domain.HashSessionToken(token),
		OperatorID: operator.ID,
		Role:       operator.Role,
		IssuedAt:   now,
		ExpiresAt:  now.Add(s.ttl),
		LastSeenAt: now,
	}
	err = s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		id, createErr := tx.Sessions.Create(ctx, session)
		if createErr != nil {
			return createErr
		}
		session.ID = id
		return s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: operator.ID,
			ObjectType: "session",
			ObjectID:   id,
			Action:     "session.login",
			Detail:     audit.Detail("role", string(operator.Role)),
		})
	})
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, ExpiresAt: session.ExpiresAt, Operator: operator}, nil
}

// Authenticate resolves the principal behind a bearer token.
func (s *Service) Authenticate(ctx context.Context, token string) (domain.Principal, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return domain.Principal{}, apperr.Wrap(apperr.ErrUnauthenticated, apperr.KindUnauthorized,
			"session_token_missing", "缺少会话令牌")
	}
	session, err := s.store.Repos().Sessions.ByTokenHash(ctx, domain.HashSessionToken(trimmed))
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return domain.Principal{}, apperr.Wrap(apperr.ErrUnauthenticated, apperr.KindUnauthorized,
				"session_unknown", "会话不存在，请重新登录")
		}
		return domain.Principal{}, err
	}
	now := s.clock.Now()
	if err := session.EnsureUsable(now); err != nil {
		return domain.Principal{}, err
	}
	operator, err := s.store.Repos().Operators.ByID(ctx, session.OperatorID)
	if err != nil {
		return domain.Principal{}, err
	}
	if !operator.Active {
		return domain.Principal{}, apperr.Wrap(apperr.ErrPermissionDenied, apperr.KindForbidden,
			"operator_disabled", "该账号已被停用")
	}
	if err := s.store.Repos().Sessions.TouchLastSeen(ctx, session.ID, now); err != nil {
		return domain.Principal{}, err
	}
	return domain.Principal{
		OperatorID: operator.ID,
		Username:   operator.Username,
		Role:       operator.Role,
		SessionID:  session.ID,
		ExpiresAt:  session.ExpiresAt,
	}, nil
}

// Logout revokes the session behind the supplied token.
func (s *Service) Logout(ctx context.Context, token string) error {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return apperr.Invalidf("session_token_missing", "缺少会话令牌")
	}
	hash := domain.HashSessionToken(trimmed)
	return s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		session, err := tx.Sessions.ByTokenHash(ctx, hash)
		if err != nil {
			return err
		}
		if err := tx.Sessions.Revoke(ctx, hash, s.clock.Now()); err != nil {
			return err
		}
		return s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: session.OperatorID,
			ObjectType: "session",
			ObjectID:   session.ID,
			Action:     "session.logout",
		})
	})
}

// RevokeOperatorSessions revokes every active session of one operator.
func (s *Service) RevokeOperatorSessions(ctx context.Context, actor domain.Principal, operatorID int64) (int, error) {
	if err := actor.RequireRole(domain.RoleFleetAdmin); err != nil {
		return 0, err
	}
	var revoked int
	err := s.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		count, revokeErr := tx.Sessions.RevokeAllForOperator(ctx, operatorID, s.clock.Now())
		if revokeErr != nil {
			return revokeErr
		}
		revoked = count
		return s.recorder.Record(ctx, tx, audit.Entry{
			OperatorID: actor.OperatorID,
			ObjectType: "operator",
			ObjectID:   operatorID,
			Action:     "session.revoke_all",
			Detail:     audit.Detail("revoked", count),
		})
	})
	if err != nil {
		return 0, err
	}
	return revoked, nil
}

// PurgeExpiredSessions deletes sessions whose validity window has elapsed.
func (s *Service) PurgeExpiredSessions(ctx context.Context) (int, error) {
	return s.store.Repos().Sessions.DeleteExpired(ctx, s.clock.Now())
}
