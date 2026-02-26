package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/queue"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/store"
)

// ════════════════════════════════════════════════════════════════
//  Job Dispatcher — Priority Router
//
//  Responsibilities:
//    1. Read new jobs from the store (status=pending)
//    2. Detect priority
//    3. Route to correct queue tier (HIGH / MEDIUM / LOW)
//    4. Apply per-client rate limits (via token in metadata)
//    5. Check dependencies (job.Metadata["depends_on"])
//    6. Non-blocking, fast, stateless (horizontally scalable)
//
//  The dispatcher runs as a background goroutine. When a job is
//  submitted via API, the handler calls dispatcher.Dispatch(job)
//  directly for minimum latency (push model). The background loop
//  is a safety net that catches any missed pending jobs.
// ════════════════════════════════════════════════════════════════

// DispatcherConfig tuning knobs.
type DispatcherConfig struct {
	PollInterval    time.Duration // How often to scan for pending jobs
	MaxBatchSize    int           // Max jobs to dispatch per poll cycle
	RateLimitPerSec int           // Max dispatches per client per second (0 = no limit)
}

// DefaultConfig returns production-ready defaults.
func DefaultConfig() DispatcherConfig {
	return DispatcherConfig{
		PollInterval:    500 * time.Millisecond,
		MaxBatchSize:    200,
		RateLimitPerSec: 0,
	}
}

// Dispatcher routes jobs from the store into the priority queue.
type Dispatcher struct {
	store  store.JobStore
	queue  *queue.PriorityQueue
	config DispatcherConfig
	logger *slog.Logger

	// Rate limiter: client_id → last dispatch timestamps (sliding window)
	rateMu     sync.RWMutex
	rateTokens map[string]*tokenBucket

	// Metrics
	dispatched atomic.Int64
	rejected   atomic.Int64
	depBlocked atomic.Int64

	// Lifecycle
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// tokenBucket implements a simple token-bucket rate limiter per client.
type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex
}

func newTokenBucket(ratePerSec int) *tokenBucket {
	return &tokenBucket{
		tokens:     float64(ratePerSec),
		maxTokens:  float64(ratePerSec),
		refillRate: float64(ratePerSec),
		lastRefill: time.Now(),
	}
}

func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastRefill = now

	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// ──────────────────────────────────────────────
// Constructor
// ──────────────────────────────────────────────

// New creates a new Dispatcher.
func New(s store.JobStore, q *queue.PriorityQueue, cfg DispatcherConfig, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		store:      s,
		queue:      q,
		config:     cfg,
		logger:     logger,
		rateTokens: make(map[string]*tokenBucket),
	}
}

// ──────────────────────────────────────────────
// Direct dispatch (push model — called by handlers)
// ──────────────────────────────────────────────

// Dispatch validates and routes a single job into the priority queue.
// This is the primary path: handler → Dispatch → queue.
// Non-blocking. Returns error only on validation/capacity failure.
func (d *Dispatcher) Dispatch(job *models.Job) error {
	// 1. Validate priority
	if !job.Priority.IsValid() {
		d.rejected.Add(1)
		return fmt.Errorf("invalid priority: %d", job.Priority)
	}

	// 2. Client rate limit check
	if d.config.RateLimitPerSec > 0 {
		clientID := job.CreatedBy
		if clientID == "" {
			clientID = "_anonymous"
		}
		if !d.allowClient(clientID) {
			d.rejected.Add(1)
			return fmt.Errorf("rate limit exceeded for client: %s", clientID)
		}
	}

	// 3. Dependency check
	if depID, ok := job.Metadata["depends_on"]; ok && depID != "" {
		depJob, err := d.store.Get(depID)
		if err != nil {
			d.depBlocked.Add(1)
			return fmt.Errorf("dependency job not found: %s", depID)
		}
		if depJob.Status != models.StatusCompleted {
			d.depBlocked.Add(1)
			return fmt.Errorf("dependency job %s is not completed (status: %s)", depID, depJob.Status)
		}
	}

	// 4. Enqueue (RAM-aware — may return error if cap exceeded)
	if err := d.queue.Enqueue(job); err != nil {
		d.rejected.Add(1)
		return fmt.Errorf("queue rejected: %w", err)
	}

	// 5. Update job status to queued
	if err := d.store.UpdateStatus(job.ID, models.StatusQueued); err != nil {
		d.logger.Warn("Failed to update job status to queued",
			slog.String("job_id", job.ID),
			slog.String("error", err.Error()),
		)
	}

	d.dispatched.Add(1)
	d.logger.Debug("Job dispatched",
		slog.String("job_id", job.ID),
		slog.Int("priority", int(job.Priority)),
		slog.String("tier", string(job.Priority.Tier())),
	)

	return nil
}

// ──────────────────────────────────────────────
// Background poll loop (safety net)
// ──────────────────────────────────────────────

// Start begins the background polling loop.
func (d *Dispatcher) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	d.wg.Add(1)

	go func() {
		defer d.wg.Done()

		ticker := time.NewTicker(d.config.PollInterval)
		defer ticker.Stop()

		d.logger.Info("Dispatcher started",
			slog.Duration("poll_interval", d.config.PollInterval),
			slog.Int("max_batch_size", d.config.MaxBatchSize),
			slog.Int("rate_limit_per_sec", d.config.RateLimitPerSec),
		)

		for {
			select {
			case <-ctx.Done():
				d.logger.Info("Dispatcher stopping...")
				return
			case <-ticker.C:
				d.pollPendingJobs()
			}
		}
	}()
}

// Stop gracefully shuts down the dispatcher.
func (d *Dispatcher) Stop() {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	d.logger.Info("Dispatcher stopped",
		slog.Int64("total_dispatched", d.dispatched.Load()),
		slog.Int64("total_rejected", d.rejected.Load()),
	)
}

// pollPendingJobs scans for pending jobs and dispatches them.
func (d *Dispatcher) pollPendingJobs() {
	jobs, _, err := d.store.List(store.ListFilter{
		Status:   models.StatusPending,
		PageSize: d.config.MaxBatchSize,
		SortBy:   "priority",
		SortDir:  "asc", // lower number = higher priority first
	})
	if err != nil {
		d.logger.Error("Failed to poll pending jobs", slog.String("error", err.Error()))
		return
	}

	for _, job := range jobs {
		if err := d.Dispatch(job); err != nil {
			d.logger.Warn("Dispatch failed during poll",
				slog.String("job_id", job.ID),
				slog.String("error", err.Error()),
			)
		}
	}
}

// ──────────────────────────────────────────────
// Rate limiter helpers
// ──────────────────────────────────────────────

func (d *Dispatcher) allowClient(clientID string) bool {
	d.rateMu.RLock()
	bucket, ok := d.rateTokens[clientID]
	d.rateMu.RUnlock()

	if !ok {
		d.rateMu.Lock()
		bucket, ok = d.rateTokens[clientID]
		if !ok {
			bucket = newTokenBucket(d.config.RateLimitPerSec)
			d.rateTokens[clientID] = bucket
		}
		d.rateMu.Unlock()
	}

	return bucket.allow()
}

// ──────────────────────────────────────────────
// Metrics
// ──────────────────────────────────────────────

// DispatcherStats holds dispatcher metrics.
type DispatcherStats struct {
	TotalDispatched int64       `json:"total_dispatched"`
	TotalRejected   int64       `json:"total_rejected"`
	DepBlocked      int64       `json:"dependency_blocked"`
	QueueStats      queue.Stats `json:"queue_stats"`
}

// Stats returns current dispatcher metrics.
func (d *Dispatcher) Stats() DispatcherStats {
	return DispatcherStats{
		TotalDispatched: d.dispatched.Load(),
		TotalRejected:   d.rejected.Load(),
		DepBlocked:      d.depBlocked.Load(),
		QueueStats:      d.queue.Stats(),
	}
}
