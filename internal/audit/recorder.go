// Package audit turns business outcomes into durable audit events. Audit rows
// are written through the same transaction as the business change so that a
// rolled back operation never leaves an audit trail behind.
package audit

import (
	"context"
	"fmt"

	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/domain"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/repository"
)

// Recorder appends audit events.
type Recorder struct {
	clock clock.Clock
}

// NewRecorder builds a recorder using the supplied clock.
func NewRecorder(source clock.Clock) *Recorder {
	if source == nil {
		source = clock.System{}
	}
	return &Recorder{clock: source}
}

// Entry describes one audited action.
type Entry struct {
	OperatorID int64
	ObjectType string
	ObjectID   int64
	Action     string
	Result     domain.AuditResult
	Detail     map[string]string
}

// Record appends a success entry through the supplied registry.
func (r *Recorder) Record(ctx context.Context, repos *repository.Registry, entry Entry) error {
	if entry.Result == "" {
		entry.Result = domain.AuditSuccess
	}
	event := &domain.AuditEvent{
		RequestID:  logging.RequestIDFromValues(ctx),
		OperatorID: entry.OperatorID,
		ObjectType: entry.ObjectType,
		ObjectID:   entry.ObjectID,
		Action:     entry.Action,
		Result:     entry.Result,
		Detail:     entry.Detail,
		CreatedAt:  r.clock.Now(),
	}
	if _, err := repos.Audit.Append(ctx, event); err != nil {
		return err
	}
	return nil
}

// RecordFailure appends a failure entry describing why an action was rejected.
func (r *Recorder) RecordFailure(
	ctx context.Context,
	repos *repository.Registry,
	entry Entry,
	cause error,
) error {
	if entry.Detail == nil {
		entry.Detail = map[string]string{}
	}
	if cause != nil {
		entry.Detail["error"] = cause.Error()
	}
	entry.Result = domain.AuditFailure
	return r.Record(ctx, repos, entry)
}

// Detail is a small helper that builds a detail map from alternating keys and
// values so call sites stay readable.
func Detail(pairs ...any) map[string]string {
	detail := map[string]string{}
	for index := 0; index+1 < len(pairs); index += 2 {
		key, ok := pairs[index].(string)
		if !ok {
			continue
		}
		detail[key] = fmt.Sprintf("%v", pairs[index+1])
	}
	return detail
}
