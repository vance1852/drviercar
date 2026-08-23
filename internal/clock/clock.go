// Package clock centralises the platform time source and the business calendar
// rules used by shift windows, triage deadlines and settlement cut-off.
package clock

import (
	"sync"
	"time"
)

// OperationsZone is the fleet operations timezone. Every business day boundary
// and every shift window is evaluated in this zone regardless of server locale.
var OperationsZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// Clock is the injectable time source.
type Clock interface {
	Now() time.Time
}

// System reads the host wall clock.
type System struct{}

// Now returns the current time in the operations zone.
func (System) Now() time.Time { return time.Now().In(OperationsZone) }

// Fixed is a deterministic clock used by tests and by replay tooling.
type Fixed struct {
	mu  sync.Mutex
	now time.Time
}

// NewFixed builds a deterministic clock anchored at moment.
func NewFixed(moment time.Time) *Fixed {
	return &Fixed{now: moment.In(OperationsZone)}
}

// Now returns the frozen instant.
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the frozen instant forward.
func (f *Fixed) Advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	return f.now
}

// Set replaces the frozen instant.
func (f *Fixed) Set(moment time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = moment.In(OperationsZone)
}

// BusinessDay renders the operations business day of moment.
func BusinessDay(moment time.Time) string {
	return moment.In(OperationsZone).Format("2006-01-02")
}

// DayBounds returns the inclusive start and exclusive end of the business day
// that contains moment.
func DayBounds(moment time.Time) (time.Time, time.Time) {
	local := moment.In(OperationsZone)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, OperationsZone)
	return start, start.AddDate(0, 0, 1)
}

// WithinWindow reports whether moment falls inside [start, end).
func WithinWindow(moment, start, end time.Time) bool {
	if end.Before(start) || end.Equal(start) {
		return false
	}
	m := moment.In(OperationsZone)
	return !m.Before(start.In(OperationsZone)) && m.Before(end.In(OperationsZone))
}

// WindowsOverlap reports whether the two half-open windows intersect.
func WindowsOverlap(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

// Truncate normalises a timestamp to whole seconds so that persisted values and
// in-memory values compare equal after a round trip.
func Truncate(moment time.Time) time.Time {
	return moment.In(OperationsZone).Truncate(time.Second)
}
