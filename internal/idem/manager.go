// Package idem implements idempotent request handling. A key is only valid for
// the same operator, HTTP method, path and request body; reusing it with a
// different payload is reported as a conflict instead of silently replaying an
// unrelated response.
package idem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/repository"
)

// Manager coordinates idempotency reservations.
type Manager struct {
	clock     clock.Clock
	retention time.Duration
}

// NewManager builds a manager with the supplied retention window.
func NewManager(source clock.Clock, retention time.Duration) *Manager {
	if source == nil {
		source = clock.System{}
	}
	if retention <= 0 {
		retention = 48 * time.Hour
	}
	return &Manager{clock: source, retention: retention}
}

// Request identifies one idempotent operation.
type Request struct {
	Key        string
	Method     string
	Path       string
	OperatorID int64
	Body       string
}

// Fingerprint derives the stable request hash.
func Fingerprint(body string) string {
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:])
}

// Outcome describes the result of a reservation attempt.
type Outcome struct {
	Replay       bool
	StoredBody   string
	Reserved     bool
}

// Begin reserves the key. When the same key and payload were already used the
// stored response is returned for replay; when the payload differs the call is
// rejected so that two different requests cannot share one key.
func (m *Manager) Begin(
	ctx context.Context,
	repos *repository.Registry,
	request Request,
) (Outcome, error) {
	key := strings.TrimSpace(request.Key)
	if key == "" {
		return Outcome{}, apperr.Invalidf("idempotency_key_required", "该操作必须提供幂等键")
	}
	if len(key) > 128 {
		return Outcome{}, apperr.Invalidf("idempotency_key_too_long", "幂等键长度不能超过 128")
	}
	record := repository.IdempotencyRecord{
		Key:         key,
		Method:      strings.ToUpper(strings.TrimSpace(request.Method)),
		Path:        request.Path,
		OperatorID:  request.OperatorID,
		RequestHash: Fingerprint(request.Body),
		CreatedAt:   m.clock.Now(),
	}
	existing, err := repos.Idempotency.Reserve(ctx, record)
	if err != nil {
		if errors.Is(err, apperr.ErrNotFound) {
			return Outcome{Reserved: true}, nil
		}
		return Outcome{}, err
	}
	if existing == nil {
		return Outcome{Reserved: true}, nil
	}
	if existing.RequestHash != record.RequestHash {
		return Outcome{}, apperr.Wrap(apperr.ErrIdempotencyReuse, apperr.KindConflict,
			"idempotency_key_conflict",
			"幂等键 %s 已用于内容不同的请求，请更换幂等键", key)
	}
	return Outcome{Replay: true, StoredBody: existing.ResponseBody}, nil
}

// Finish stores the response body so a retry can replay the same answer.
func (m *Manager) Finish(
	ctx context.Context,
	repos *repository.Registry,
	request Request,
	responseBody string,
) error {
	return repos.Idempotency.Complete(ctx,
		strings.TrimSpace(request.Key),
		strings.ToUpper(strings.TrimSpace(request.Method)),
		request.Path,
		request.OperatorID,
		responseBody)
}

// Prune removes keys older than the retention window.
func (m *Manager) Prune(ctx context.Context, repos *repository.Registry) (int, error) {
	return repos.Idempotency.DeleteOlderThan(ctx, m.clock.Now().Add(-m.retention))
}
