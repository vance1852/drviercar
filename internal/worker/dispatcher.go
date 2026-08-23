// Package worker runs the background job queue that keeps sessions, idempotency
// keys and overdue triage tickets under control.
package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/clock"
	"github.com/vance1852/drviercar/internal/logging"
	"github.com/vance1852/drviercar/internal/repository"
)

// ErrPermanent marks a failure that must not be retried.
var ErrPermanent = errors.New("permanent job failure")

// Handler processes one job.
type Handler func(ctx context.Context, job *repository.Job) error

// Metrics is a snapshot of dispatcher counters.
type Metrics struct {
	Processed int
	Succeeded int
	Retried   int
	Dead      int
}

// Config configures the dispatcher loop.
type Config struct {
	Interval    time.Duration
	BatchSize   int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

// Dispatcher claims due jobs and runs the registered handlers.
type Dispatcher struct {
	store    repository.Store
	clock    clock.Clock
	logger   *logging.Logger
	config   Config
	handlers map[string]Handler

	mu      sync.Mutex
	metrics Metrics

	wg   sync.WaitGroup
	done chan struct{}
	once sync.Once
}

// NewDispatcher builds a dispatcher.
func NewDispatcher(
	store repository.Store,
	source clock.Clock,
	logger *logging.Logger,
	config Config,
) *Dispatcher {
	if source == nil {
		source = clock.System{}
	}
	if logger == nil {
		logger = logging.New(nil, logging.LevelInfo)
	}
	if config.Interval <= 0 {
		config.Interval = 500 * time.Millisecond
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 5
	}
	if config.BaseBackoff <= 0 {
		config.BaseBackoff = time.Second
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = 5 * time.Minute
	}
	return &Dispatcher{
		store:    store,
		clock:    source,
		logger:   logger,
		config:   config,
		handlers: map[string]Handler{},
		done:     make(chan struct{}),
	}
}

// Register binds a handler to a job kind.
func (d *Dispatcher) Register(kind string, handler Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[kind] = handler
}

// Enqueue schedules a job.
func (d *Dispatcher) Enqueue(
	ctx context.Context,
	kind, payload string,
	maxAttempts int,
	runAt time.Time,
) (int64, error) {
	if runAt.IsZero() {
		runAt = d.clock.Now()
	}
	job := &repository.Job{
		Kind:        kind,
		Payload:     payload,
		Status:      repository.JobQueued,
		MaxAttempts: maxAttempts,
		NextRunAt:   runAt,
	}
	var id int64
	err := d.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		created, enqueueErr := tx.Jobs.Enqueue(ctx, job)
		if enqueueErr != nil {
			return enqueueErr
		}
		id = created
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// Metrics returns a copy of the dispatcher counters.
func (d *Dispatcher) Metrics() Metrics {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.metrics
}

func (d *Dispatcher) handlerFor(kind string) (Handler, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	handler, ok := d.handlers[kind]
	return handler, ok
}

func (d *Dispatcher) countProcessed(succeeded, retried, dead int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.metrics.Processed += succeeded + retried + dead
	d.metrics.Succeeded += succeeded
	d.metrics.Retried += retried
	d.metrics.Dead += dead
}

// RunOnce claims and runs at most one batch of due jobs. It returns the number
// of jobs that were processed.
//
// ctx carries the stop signal so that a handler in flight when the process is
// asked to shut down observes the cancellation and can abort. The bookkeeping
// that records the outcome always runs on a detached context, so the outcome of
// a claimed job is committed even while the process is winding down.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var claimed []*repository.Job
	err := d.store.WithTx(ctx, func(ctx context.Context, tx *repository.Registry) error {
		jobs, claimErr := tx.Jobs.ClaimDue(ctx, d.clock.Now(), d.config.BatchSize)
		if claimErr != nil {
			return claimErr
		}
		claimed = jobs
		return nil
	})
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, job := range claimed {
		if err := d.runJob(ctx, job); err != nil {
			d.logger.Error(ctx, "job bookkeeping failed", map[string]any{
				"job_id": job.ID,
				"error":  err.Error(),
			})
			continue
		}
		processed++
	}
	return processed, nil
}

func (d *Dispatcher) runJob(ctx context.Context, job *repository.Job) error {
	// bookCtx drops the stop signal so that the outcome of an already claimed
	// job is always committed, even when the process is shutting down. The job
	// is already in the running state: leaving it unbooked would strand it, so
	// the bookkeeping must not fail together with the stop signal.
	bookCtx := context.WithoutCancel(ctx)
	bookkeep := func(fn func(tx *repository.Registry) error) error {
		return d.store.WithTx(bookCtx, func(ctx context.Context, tx *repository.Registry) error {
			return fn(tx)
		})
	}
	handler, ok := d.handlerFor(job.Kind)
	if !ok {
		d.countProcessed(0, 0, 1)
		return bookkeep(func(tx *repository.Registry) error {
			return tx.Jobs.MarkDead(bookCtx, job.ID, "no handler registered for kind "+job.Kind)
		})
	}
	runErr := handler(ctx, job)
	// If the stop signal arrived while the handler was running, the job did not
	// really finish: it must be returned to the queue so the next process can
	// claim it again. This takes precedence over both success and failure so a
	// partially done job is never recorded as succeeded.
	if ctx.Err() != nil {
		d.countProcessed(0, 0, 0)
		cause := runErr
		if cause == nil {
			cause = ctx.Err()
		}
		d.logger.Warn(ctx, "job interrupted by shutdown", map[string]any{
			"job_id":   job.ID,
			"kind":     job.Kind,
			"attempts": job.Attempts,
			"error":    cause.Error(),
		})
		return bookkeep(func(tx *repository.Registry) error {
			return tx.Jobs.MarkInterrupted(bookCtx, job.ID, d.clock.Now(),
				"interrupted by shutdown: "+cause.Error())
		})
	}
	if runErr == nil {
		d.countProcessed(1, 0, 0)
		return bookkeep(func(tx *repository.Registry) error {
			return tx.Jobs.MarkSucceeded(bookCtx, job.ID)
		})
	}
	permanent := errors.Is(runErr, ErrPermanent) || job.Attempts >= job.MaxAttempts
	if permanent {
		d.countProcessed(0, 0, 1)
		d.logger.Warn(ctx, "job failed permanently", map[string]any{
			"job_id":   job.ID,
			"kind":     job.Kind,
			"attempts": job.Attempts,
			"error":    runErr.Error(),
		})
		return bookkeep(func(tx *repository.Registry) error {
			return tx.Jobs.MarkDead(bookCtx, job.ID, runErr.Error())
		})
	}
	delay := Backoff(job.Attempts, d.config.BaseBackoff, d.config.MaxBackoff)
	d.countProcessed(0, 1, 0)
	d.logger.Info(ctx, "job scheduled for retry", map[string]any{
		"job_id":   job.ID,
		"kind":     job.Kind,
		"attempts": job.Attempts,
		"delay_ms": delay.Milliseconds(),
	})
	return bookkeep(func(tx *repository.Registry) error {
		return tx.Jobs.MarkRetry(bookCtx, job.ID, d.clock.Now().Add(delay), runErr.Error())
	})
}

// Backoff computes the exponential retry delay of an attempt.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for index := 1; index < attempt; index++ {
		delay *= 2
		if delay >= max {
			return max
		}
	}
	if delay > max {
		return max
	}
	return delay
}

// Start runs the dispatcher loop until ctx is cancelled. Before the loop starts
// it reclaims any job that a previous process left in the running state, which
// is how a job stranded by an abrupt restart becomes claimable again.
func (d *Dispatcher) Start(ctx context.Context) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if reclaimed, err := d.store.Repos().Jobs.ReclaimRunning(ctx, d.clock.Now()); err != nil {
			d.logger.Error(ctx, "job reclaim failed", map[string]any{"error": err.Error()})
		} else if reclaimed > 0 {
			d.logger.Info(ctx, "reclaimed jobs left running by a previous process",
				map[string]any{"count": reclaimed})
		}
		ticker := time.NewTicker(d.config.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.done:
				return
			case <-ticker.C:
				// ctx carries the stop signal so a handler in flight when the
				// process is asked to shut down observes the cancellation. The
				// bookkeeping inside RunOnce detaches the cancellation so the
				// outcome of a claimed job is still committed.
				if _, err := d.RunOnce(ctx); err != nil {
					if errors.Is(err, context.Canceled) || apperr.KindOf(err) == apperr.KindCancelled {
						return
					}
					d.logger.Error(ctx, "dispatcher run failed", map[string]any{"error": err.Error()})
				}
			}
		}
	}()
}

// Stop signals the loop and waits for it to finish.
func (d *Dispatcher) Stop() {
	d.once.Do(func() { close(d.done) })
	d.wg.Wait()
}
