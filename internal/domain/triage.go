package domain

import (
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// TicketStatus is the lifecycle state of a shadow-mode triage ticket.
type TicketStatus string

const (
	TicketOpen          TicketStatus = "open"
	TicketInvestigating TicketStatus = "investigating"
	TicketDisposed      TicketStatus = "disposed"
	TicketArchived      TicketStatus = "archived"
)

var ticketTransitions = map[TicketStatus][]TicketStatus{
	TicketOpen:          {TicketInvestigating, TicketDisposed},
	TicketInvestigating: {TicketDisposed},
	TicketDisposed:      {TicketArchived},
	TicketArchived:      {},
}

// Valid reports whether the ticket status is part of the state machine.
func (s TicketStatus) Valid() bool {
	_, ok := ticketTransitions[s]
	return ok
}

// Pending reports whether the ticket still blocks downstream closure.
func (s TicketStatus) Pending() bool {
	return s == TicketOpen || s == TicketInvestigating
}

// CanTransitionTo reports whether the state machine allows the move.
func (s TicketStatus) CanTransitionTo(next TicketStatus) bool {
	for _, allowed := range ticketTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Disposition is the recorded conclusion of a triage ticket.
type Disposition string

const (
	DispositionNone         Disposition = ""
	DispositionSoftwareBug  Disposition = "software_bug"
	DispositionSensorFault  Disposition = "sensor_fault"
	DispositionEnvironment  Disposition = "environment"
	DispositionFalseTrigger Disposition = "false_trigger"
)

// Valid reports whether the disposition is a terminal conclusion.
func (d Disposition) Valid() bool {
	switch d {
	case DispositionSoftwareBug, DispositionSensorFault, DispositionEnvironment, DispositionFalseTrigger:
		return true
	default:
		return false
	}
}

// RequiresDatasetHold reports whether frames must stay out of released datasets.
func (d Disposition) RequiresDatasetHold() bool {
	return d == DispositionSensorFault || d == DispositionFalseTrigger
}

// TriageTicket tracks the investigation of one capture batch replay.
type TriageTicket struct {
	ID           int64
	BatchID      int64
	DriveID      int64
	Status       TicketStatus
	Disposition  Disposition
	Severity     int
	AssigneeID   int64
	OpenedAt     time.Time
	DeadlineAt   time.Time
	DisposedAt   *time.Time
	Conclusion   string
	Version      int64
}

// Validate enforces the ticket invariants.
func (t *TriageTicket) Validate() error {
	if t.BatchID <= 0 {
		return apperr.Invalidf("ticket_batch_required", "处置单必须关联采集批次")
	}
	if !t.Status.Valid() {
		return apperr.Invalidf("ticket_status_invalid", "未知的处置单状态 %q", string(t.Status))
	}
	if t.Severity < 1 || t.Severity > 5 {
		return apperr.Invalidf("ticket_severity_invalid", "处置单严重度必须在 1~5 之间")
	}
	if !t.DeadlineAt.After(t.OpenedAt) {
		return apperr.Invalidf("ticket_deadline_invalid", "处置期限必须晚于开单时间")
	}
	return nil
}

// EnsureTransition validates a requested lifecycle move.
func (t *TriageTicket) EnsureTransition(next TicketStatus) error {
	if !next.Valid() {
		return apperr.Invalidf("ticket_status_invalid", "未知的目标处置单状态 %q", string(next))
	}
	if !t.Status.CanTransitionTo(next) {
		return apperr.Wrap(apperr.ErrIllegalTransition, apperr.KindPrecondition,
			"ticket_transition_illegal", "处置单不能从 %s 变更为 %s", string(t.Status), string(next))
	}
	return nil
}

// Overdue reports whether the deadline has elapsed without a disposition.
func (t *TriageTicket) Overdue(now time.Time) bool {
	return t.Status.Pending() && !now.Before(t.DeadlineAt)
}

// EnsureDisposable validates the conclusion payload before closing the ticket.
func (t *TriageTicket) EnsureDisposable(disposition Disposition, conclusion string) error {
	if !disposition.Valid() {
		return apperr.Invalidf("ticket_disposition_invalid", "未知的处置结论 %q", string(disposition))
	}
	if strings.TrimSpace(conclusion) == "" {
		return apperr.Invalidf("ticket_conclusion_required", "处置单必须填写结论说明")
	}
	if err := t.EnsureTransition(TicketDisposed); err != nil {
		return err
	}
	return nil
}

// TriageDeadline derives the disposition deadline of a ticket from its severity.
// High severity replays must be dispositioned within the same operations shift.
func TriageDeadline(openedAt time.Time, severity int) time.Time {
	switch {
	case severity >= 5:
		return openedAt.Add(4 * time.Hour)
	case severity == 4:
		return openedAt.Add(12 * time.Hour)
	case severity == 3:
		return openedAt.Add(24 * time.Hour)
	default:
		return openedAt.Add(72 * time.Hour)
	}
}

// Clone returns an independent copy of the ticket.
func (t *TriageTicket) Clone() *TriageTicket {
	if t == nil {
		return nil
	}
	copied := *t
	if t.DisposedAt != nil {
		disposed := *t.DisposedAt
		copied.DisposedAt = &disposed
	}
	return &copied
}

// CloneTickets copies a slice of tickets element by element.
func CloneTickets(items []*TriageTicket) []*TriageTicket {
	if items == nil {
		return nil
	}
	copied := make([]*TriageTicket, 0, len(items))
	for _, item := range items {
		copied = append(copied, item.Clone())
	}
	return copied
}
