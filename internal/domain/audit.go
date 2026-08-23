package domain

import (
	"sort"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// AuditResult is the outcome recorded for an audited action.
type AuditResult string

const (
	AuditSuccess AuditResult = "success"
	AuditFailure AuditResult = "failure"
)

// AuditEvent is one immutable operations audit record.
type AuditEvent struct {
	ID         int64
	RequestID  string
	OperatorID int64
	ObjectType string
	ObjectID   int64
	Action     string
	Result     AuditResult
	Detail     map[string]string
	CreatedAt  time.Time
}

// Validate enforces the audit invariants required by the storage layer.
func (a *AuditEvent) Validate() error {
	if strings.TrimSpace(a.ObjectType) == "" {
		return apperr.Invalidf("audit_object_type_required", "审计事件必须记录对象类型")
	}
	if strings.TrimSpace(a.Action) == "" {
		return apperr.Invalidf("audit_action_required", "审计事件必须记录动作")
	}
	if a.Result != AuditSuccess && a.Result != AuditFailure {
		return apperr.Invalidf("audit_result_invalid", "审计结果只能是 success 或 failure")
	}
	return nil
}

// DetailKeys returns the sorted detail keys, used for stable rendering.
func (a *AuditEvent) DetailKeys() []string {
	keys := make([]string, 0, len(a.Detail))
	for key := range a.Detail {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Clone returns an independent copy including the detail map so that callers
// cannot mutate a stored event through the returned value.
func (a *AuditEvent) Clone() *AuditEvent {
	if a == nil {
		return nil
	}
	copied := *a
	if a.Detail != nil {
		copied.Detail = make(map[string]string, len(a.Detail))
		for key, value := range a.Detail {
			copied.Detail[key] = value
		}
	}
	return &copied
}

// CloneAuditEvents copies a slice of audit events element by element.
func CloneAuditEvents(items []*AuditEvent) []*AuditEvent {
	if items == nil {
		return nil
	}
	copied := make([]*AuditEvent, 0, len(items))
	for _, item := range items {
		copied = append(copied, item.Clone())
	}
	return copied
}
