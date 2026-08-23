package worker_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vance1852/drviercar/internal/repository"
	"github.com/vance1852/drviercar/internal/testsupport"
)

// TestShutdownCancelsRunningJob starts the background dispatcher, lets a job
// enter its handler and then shuts the dispatcher down, checking that the job is
// told to stop and is not recorded as a success.
func TestShutdownCancelsRunningJob(t *testing.T) {
	harness, err := testsupport.New(t.TempDir())
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	defer func() { _ = harness.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu          sync.Mutex
		observed    error
		handlerRuns int
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	harness.Dispatcher.Register("mileage-rollup", func(jobCtx context.Context, _ *repository.Job) error {
		mu.Lock()
		handlerRuns++
		first := handlerRuns == 1
		mu.Unlock()
		if !first {
			return nil
		}
		close(entered)
		<-release
		jobErr := jobCtx.Err()
		mu.Lock()
		observed = jobErr
		mu.Unlock()
		if jobErr != nil {
			return jobErr
		}
		return nil
	})

	jobID, err := harness.Dispatcher.Enqueue(ctx, "mileage-rollup", `{"campaign_id":41}`, 3, testsupport.Anchor)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	harness.Dispatcher.Start(ctx)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		harness.Dispatcher.Stop()
		t.Fatal("the dispatcher never started the queued job")
	}

	cancel()
	close(release)
	harness.Dispatcher.Stop()

	mu.Lock()
	seen := observed
	runs := handlerRuns
	mu.Unlock()
	if runs != 1 {
		t.Fatalf("the job handler must run exactly once, ran %d times", runs)
	}
	if !errors.Is(seen, context.Canceled) {
		t.Fatalf("a shutting down dispatcher must cancel the job it is running, handler saw %v", seen)
	}

	job, err := harness.Store.Repos().Jobs.ByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("read job after shutdown: %v", err)
	}
	if job.Status == repository.JobSucceeded {
		t.Fatalf("a job interrupted by shutdown must not be recorded as succeeded, status=%s", job.Status)
	}
	if strings.EqualFold(job.Status, repository.JobDead) {
		t.Fatalf("a job interrupted by shutdown must stay recoverable, status=%s", job.Status)
	}
	metrics := harness.Dispatcher.Metrics()
	if metrics.Succeeded != 0 {
		t.Fatalf("no job may be counted as succeeded after the shutdown, metrics=%+v", metrics)
	}
	succeeded, err := harness.Store.Repos().Jobs.CountByStatus(context.Background(), repository.JobSucceeded)
	if err != nil {
		t.Fatalf("count succeeded jobs: %v", err)
	}
	if succeeded != 0 {
		t.Fatalf("the queue must not hold a succeeded job after the shutdown, got %d", succeeded)
	}
}
