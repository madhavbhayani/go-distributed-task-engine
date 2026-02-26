package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/executor"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/queue"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/store"
)

// ════════════════════════════════════════════════════════════════
//  Worker Pool System
//
//  Each worker:
//    1. Pulls a job from the priority queue
//    2. Locks the job (status → running, workerID set)
//    3. Executes via the Job Executor layer
//    4. Updates status (completed / failed)
//    5. Pushes result back to the store
//
//  Features:
//    • Concurrency control (configurable 5–10 workers)
//    • Per-job timeout support
//    • Graceful shutdown (drain in-flight jobs)
//    • Live scaling via API (Scale endpoint)
// ════════════════════════════════════════════════════════════════

const (
	MinWorkers     = 5
	MaxWorkers     = 10
	DefaultWorkers = 5
	DefaultTimeout = 5 * time.Minute // per-job timeout
)

// PoolConfig tunes the worker pool.
type PoolConfig struct {
	InitialWorkers int
	JobTimeout     time.Duration
}

// DefaultPoolConfig returns sensible defaults.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		InitialWorkers: DefaultWorkers,
		JobTimeout:     DefaultTimeout,
	}
}

// PoolStats exposes pool metrics.
type PoolStats struct {
	ActiveWorkers  int   `json:"active_workers"`
	DesiredWorkers int   `json:"desired_workers"`
	MinWorkers     int   `json:"min_workers"`
	MaxWorkers     int   `json:"max_workers"`
	JobsProcessed  int64 `json:"jobs_processed"`
	JobsFailed     int64 `json:"jobs_failed"`
	JobsTimedOut   int64 `json:"jobs_timed_out"`
	InFlight       int64 `json:"in_flight"`
}

// Pool manages a dynamic set of workers that consume from the priority queue.
type Pool struct {
	store    store.JobStore
	queue    *queue.PriorityQueue
	executor *executor.Engine
	config   PoolConfig
	logger   *slog.Logger

	// Dynamic worker management
	desiredWorkers int
	mu             sync.Mutex
	workers        map[int]context.CancelFunc // workerID → cancel
	nextWorkerID   int

	// Metrics
	processed atomic.Int64
	failed    atomic.Int64
	timedOut  atomic.Int64
	inFlight  atomic.Int64

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new worker pool.
func New(
	s store.JobStore,
	q *queue.PriorityQueue,
	exec *executor.Engine,
	cfg PoolConfig,
	logger *slog.Logger,
) *Pool {
	if cfg.InitialWorkers < MinWorkers {
		cfg.InitialWorkers = MinWorkers
	}
	if cfg.InitialWorkers > MaxWorkers {
		cfg.InitialWorkers = MaxWorkers
	}
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = DefaultTimeout
	}

	return &Pool{
		store:          s,
		queue:          q,
		executor:       exec,
		config:         cfg,
		logger:         logger,
		desiredWorkers: cfg.InitialWorkers,
		workers:        make(map[int]context.CancelFunc),
	}
}

// Start launches the initial set of workers.
func (p *Pool) Start(ctx context.Context) {
	p.ctx, p.cancel = context.WithCancel(ctx)

	p.mu.Lock()
	for i := 0; i < p.desiredWorkers; i++ {
		p.spawnWorkerLocked()
	}
	p.mu.Unlock()

	p.logger.Info("Worker pool started",
		slog.Int("workers", p.desiredWorkers),
		slog.Duration("job_timeout", p.config.JobTimeout),
	)
}

// Stop gracefully shuts down all workers and waits for in-flight jobs.
func (p *Pool) Stop() {
	p.cancel()
	p.wg.Wait()
	p.logger.Info("Worker pool stopped",
		slog.Int64("processed", p.processed.Load()),
		slog.Int64("failed", p.failed.Load()),
		slog.Int64("timed_out", p.timedOut.Load()),
	)
}

// Scale adjusts the number of workers. Returns the new count.
// Allowed range: [MinWorkers, MaxWorkers].
func (p *Pool) Scale(desired int) (int, error) {
	if desired < MinWorkers || desired > MaxWorkers {
		return 0, fmt.Errorf("worker count must be between %d and %d, got %d", MinWorkers, MaxWorkers, desired)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	current := len(p.workers)
	p.desiredWorkers = desired

	if desired > current {
		// Scale up — spawn more workers
		for i := 0; i < desired-current; i++ {
			p.spawnWorkerLocked()
		}
		p.logger.Info("Worker pool scaled up",
			slog.Int("from", current),
			slog.Int("to", desired),
		)
	} else if desired < current {
		// Scale down — cancel excess workers (they finish current job first)
		toRemove := current - desired
		removed := 0
		for id, cancelFn := range p.workers {
			if removed >= toRemove {
				break
			}
			cancelFn()
			delete(p.workers, id)
			removed++
		}
		p.logger.Info("Worker pool scaled down",
			slog.Int("from", current),
			slog.Int("to", desired),
			slog.Int("cancelled", removed),
		)
	}

	return desired, nil
}

// Stats returns current pool metrics.
func (p *Pool) Stats() PoolStats {
	p.mu.Lock()
	active := len(p.workers)
	desired := p.desiredWorkers
	p.mu.Unlock()

	return PoolStats{
		ActiveWorkers:  active,
		DesiredWorkers: desired,
		MinWorkers:     MinWorkers,
		MaxWorkers:     MaxWorkers,
		JobsProcessed:  p.processed.Load(),
		JobsFailed:     p.failed.Load(),
		JobsTimedOut:   p.timedOut.Load(),
		InFlight:       p.inFlight.Load(),
	}
}

// ──────────────────────────────────────────────
// Internal: worker goroutine
// ──────────────────────────────────────────────

// spawnWorkerLocked starts a new worker goroutine. Must be called with p.mu held.
func (p *Pool) spawnWorkerLocked() {
	p.nextWorkerID++
	id := p.nextWorkerID
	workerCtx, workerCancel := context.WithCancel(p.ctx)
	p.workers[id] = workerCancel

	p.wg.Add(1)
	go p.runWorker(workerCtx, id)
}

// runWorker is the main loop for a single worker.
func (p *Pool) runWorker(ctx context.Context, id int) {
	defer p.wg.Done()

	workerTag := fmt.Sprintf("worker-%d", id)
	p.logger.Debug("Worker started", slog.String("worker", workerTag))

	for {
		select {
		case <-ctx.Done():
			p.logger.Debug("Worker stopping", slog.String("worker", workerTag))
			return

		case <-p.queue.Notify():
			// Pull job from queue
			job := p.queue.Dequeue()
			if job == nil {
				continue
			}

			p.processJob(ctx, workerTag, job)

		}
	}
}

// processJob handles a single job: lock → execute → update.
func (p *Pool) processJob(ctx context.Context, workerTag string, job *models.Job) {
	// Check if job was cancelled while waiting
	freshJob, err := p.store.Get(job.ID)
	if err != nil || freshJob.Status == models.StatusCancelled {
		p.logger.Debug("Job skipped (cancelled or deleted)",
			slog.String("worker", workerTag),
			slog.String("job_id", job.ID),
		)
		return
	}

	// ── 1. Lock the job (status → running) ────────
	if err := p.store.UpdateStatus(job.ID, models.StatusRunning); err != nil {
		p.logger.Error("Failed to lock job",
			slog.String("worker", workerTag),
			slog.String("job_id", job.ID),
			slog.String("error", err.Error()),
		)
		return
	}

	p.inFlight.Add(1)
	defer p.inFlight.Add(-1)

	p.logger.Info("Job started",
		slog.String("worker", workerTag),
		slog.String("job_id", job.ID),
		slog.String("type", string(job.Type)),
		slog.Int("priority", int(job.Priority)),
	)

	// ── 2. Execute with timeout ───────────────────
	jobCtx, jobCancel := context.WithTimeout(ctx, p.config.JobTimeout)
	defer jobCancel()

	startTime := time.Now()
	result, execErr := p.executor.Execute(jobCtx, job)
	elapsed := time.Since(startTime)

	// ── 3. Update status + push result ────────────
	if execErr != nil {
		// Check if it was a timeout
		if jobCtx.Err() == context.DeadlineExceeded {
			p.timedOut.Add(1)
			job.Error = "job timed out after " + p.config.JobTimeout.String()
			job.ErrorCode = "TIMEOUT"
		} else {
			job.Error = execErr.Error()
			job.ErrorCode = "EXECUTION_FAILED"
		}

		job.Result = result // partial result may be useful
		if err := p.store.Update(job); err != nil {
			p.logger.Error("Failed to save job error", slog.String("job_id", job.ID))
		}

		// Retry logic
		if job.RetryCount < job.MaxRetries && job.ErrorCode != "TIMEOUT" {
			job.RetryCount++
			job.Error = ""
			job.ErrorCode = ""
			if err := p.store.UpdateStatus(job.ID, models.StatusPending); err == nil {
				p.logger.Info("Job retrying",
					slog.String("worker", workerTag),
					slog.String("job_id", job.ID),
					slog.Int("retry", job.RetryCount),
					slog.Int("max_retries", job.MaxRetries),
				)
				return // dispatcher will re-enqueue on next poll
			}
		}

		if err := p.store.UpdateStatus(job.ID, models.StatusFailed); err != nil {
			p.logger.Error("Failed to update job status to failed", slog.String("job_id", job.ID))
		}
		p.failed.Add(1)

		p.logger.Warn("Job failed",
			slog.String("worker", workerTag),
			slog.String("job_id", job.ID),
			slog.String("error_code", job.ErrorCode),
			slog.Duration("elapsed", elapsed),
		)
		return
	}

	// Success
	job.Result = result
	if err := p.store.Update(job); err != nil {
		p.logger.Error("Failed to save job result", slog.String("job_id", job.ID))
	}
	if err := p.store.UpdateStatus(job.ID, models.StatusCompleted); err != nil {
		p.logger.Error("Failed to update job status to completed", slog.String("job_id", job.ID))
	}
	p.processed.Add(1)

	p.logger.Info("Job completed",
		slog.String("worker", workerTag),
		slog.String("job_id", job.ID),
		slog.String("type", string(job.Type)),
		slog.Duration("elapsed", elapsed),
	)
}
