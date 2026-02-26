package queue

import (
	"container/heap"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// ════════════════════════════════════════════════════════════════
//  Priority Queue System
//
//  Three logical queues:
//    HIGH   (priorities 1,2,3)   — processed first
//    MEDIUM (priorities 4,5,6,7) — processed when HIGH is empty
//    LOW    (priorities 8,9,10)  — processed when MEDIUM is empty
//
//  Within each queue, jobs are sorted by:
//    1. Priority value (ascending — lower number = higher priority)
//    2. CreatedAt timestamp (ascending — FIFO for equal priority)
//
//  RAM tracking:
//    Every enqueue/dequeue updates consumingRAM.
//    If total system RAM exceeds the cap, Enqueue returns an error
//    (back-pressure to the dispatcher).
// ════════════════════════════════════════════════════════════════

// ──────────────────────────────────────────────
// Queue Item (min-heap element)
// ──────────────────────────────────────────────

// item wraps a Job for heap ordering.
type item struct {
	job       *models.Job
	priority  models.Priority
	createdAt time.Time
	sizeBytes int64 // estimated RAM footprint of this item
	index     int   // managed by heap.Interface
}

// ──────────────────────────────────────────────
// Min-Heap implementation (container/heap)
// ──────────────────────────────────────────────

type jobHeap []*item

func (h jobHeap) Len() int { return len(h) }

func (h jobHeap) Less(i, j int) bool {
	// Lower priority number = higher urgency (1 is top)
	if h[i].priority != h[j].priority {
		return h[i].priority < h[j].priority
	}
	// FIFO for equal priority
	return h[i].createdAt.Before(h[j].createdAt)
}

func (h jobHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *jobHeap) Push(x any) {
	n := len(*h)
	it := x.(*item)
	it.index = n
	*h = append(*h, it)
}

func (h *jobHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil // avoid memory leak
	it.index = -1
	*h = old[:n-1]
	return it
}

// ──────────────────────────────────────────────
// Single Tier Queue
// ──────────────────────────────────────────────

// tierQueue is a thread-safe priority min-heap for one tier.
type tierQueue struct {
	mu   sync.Mutex
	heap jobHeap
	tier models.QueueTier
}

func newTierQueue(tier models.QueueTier) *tierQueue {
	tq := &tierQueue{tier: tier}
	heap.Init(&tq.heap)
	return tq
}

func (tq *tierQueue) push(it *item) {
	tq.mu.Lock()
	heap.Push(&tq.heap, it)
	tq.mu.Unlock()
}

func (tq *tierQueue) pop() *item {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	if tq.heap.Len() == 0 {
		return nil
	}
	return heap.Pop(&tq.heap).(*item)
}

func (tq *tierQueue) peek() *item {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	if tq.heap.Len() == 0 {
		return nil
	}
	return tq.heap[0]
}

func (tq *tierQueue) len() int {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	return tq.heap.Len()
}

// ──────────────────────────────────────────────
// Priority Queue (public API)
// ──────────────────────────────────────────────

// Stats exposes queue metrics.
type Stats struct {
	HighCount    int     `json:"high_count"`
	MediumCount  int     `json:"medium_count"`
	LowCount     int     `json:"low_count"`
	TotalCount   int     `json:"total_count"`
	ConsumingRAM int64   `json:"consuming_ram_bytes"`
	RAMCapBytes  int64   `json:"ram_cap_bytes"`
	RAMUsagePct  float64 `json:"ram_usage_pct"`
}

// PriorityQueue holds three tier queues and enforces a RAM cap.
type PriorityQueue struct {
	high   *tierQueue
	medium *tierQueue
	low    *tierQueue

	// RAM tracking (atomic for lock-free reads)
	consumingRAM atomic.Int64
	ramCapBytes  int64 // max RAM budget for the queue system

	// Notification channel — dispatcher reads from here
	notify chan struct{}

	logger *slog.Logger
}

// NewPriorityQueue creates the three-tier priority queue.
// ramCapMB is the maximum MB the queue system is allowed to consume.
func NewPriorityQueue(ramCapMB int64, logger *slog.Logger) *PriorityQueue {
	if ramCapMB <= 0 {
		ramCapMB = 400 // default 400 MB for queues (leaves headroom for app)
	}

	pq := &PriorityQueue{
		high:        newTierQueue(models.QueueTierHigh),
		medium:      newTierQueue(models.QueueTierMedium),
		low:         newTierQueue(models.QueueTierLow),
		ramCapBytes: ramCapMB * 1024 * 1024,
		notify:      make(chan struct{}, 4096), // buffered so enqueue never blocks
		logger:      logger,
	}

	logger.Info("Priority queue initialized",
		slog.Int64("ram_cap_mb", ramCapMB),
		slog.Int64("ram_cap_bytes", pq.ramCapBytes),
	)

	return pq
}

// Enqueue adds a job to the correct tier queue.
// Returns error if the RAM cap would be exceeded (back-pressure).
func (pq *PriorityQueue) Enqueue(job *models.Job) error {
	size := estimateJobSize(job)

	// RAM cap check
	current := pq.consumingRAM.Load()
	if current+size > pq.ramCapBytes {
		return fmt.Errorf(
			"queue RAM cap exceeded: current=%d bytes, item=%d bytes, cap=%d bytes",
			current, size, pq.ramCapBytes,
		)
	}

	it := &item{
		job:       job,
		priority:  job.Priority,
		createdAt: job.CreatedAt,
		sizeBytes: size,
	}

	// Route to correct tier
	tier := job.Priority.Tier()
	switch tier {
	case models.QueueTierHigh:
		pq.high.push(it)
	case models.QueueTierMedium:
		pq.medium.push(it)
	default:
		pq.low.push(it)
	}

	pq.consumingRAM.Add(size)

	pq.logger.Debug("Job enqueued",
		slog.String("job_id", job.ID),
		slog.Int("priority", int(job.Priority)),
		slog.String("tier", string(tier)),
		slog.Int64("item_bytes", size),
		slog.Int64("total_ram", pq.consumingRAM.Load()),
	)

	// Non-blocking notify
	select {
	case pq.notify <- struct{}{}:
	default:
	}

	return nil
}

// Dequeue returns the next highest-priority job.
// Drains HIGH first, then MEDIUM, then LOW.
// Returns nil if all queues are empty.
func (pq *PriorityQueue) Dequeue() *models.Job {
	// Try HIGH first
	if it := pq.high.pop(); it != nil {
		pq.consumingRAM.Add(-it.sizeBytes)
		return it.job
	}
	// Then MEDIUM
	if it := pq.medium.pop(); it != nil {
		pq.consumingRAM.Add(-it.sizeBytes)
		return it.job
	}
	// Then LOW
	if it := pq.low.pop(); it != nil {
		pq.consumingRAM.Add(-it.sizeBytes)
		return it.job
	}
	return nil
}

// Notify returns the channel that signals new items are available.
func (pq *PriorityQueue) Notify() <-chan struct{} {
	return pq.notify
}

// Stats returns current queue metrics.
func (pq *PriorityQueue) Stats() Stats {
	hc := pq.high.len()
	mc := pq.medium.len()
	lc := pq.low.len()
	ram := pq.consumingRAM.Load()
	pct := 0.0
	if pq.ramCapBytes > 0 {
		pct = float64(ram) / float64(pq.ramCapBytes) * 100
	}
	return Stats{
		HighCount:    hc,
		MediumCount:  mc,
		LowCount:     lc,
		TotalCount:   hc + mc + lc,
		ConsumingRAM: ram,
		RAMCapBytes:  pq.ramCapBytes,
		RAMUsagePct:  pct,
	}
}

// Len returns total items across all tiers.
func (pq *PriorityQueue) Len() int {
	return pq.high.len() + pq.medium.len() + pq.low.len()
}

// ──────────────────────────────────────────────
// RAM estimation
// ──────────────────────────────────────────────

// estimateJobSize returns an approximate byte size of a job in memory.
// This is a conservative estimate used for RAM cap enforcement.
func estimateJobSize(job *models.Job) int64 {
	// Base struct size
	base := int64(unsafe.Sizeof(*job))

	// String fields
	base += int64(len(job.ID))
	base += int64(len(job.Name))
	base += int64(len(job.Error))
	base += int64(len(job.ErrorCode))
	base += int64(len(job.StackTrace))
	base += int64(len(job.CallbackURL))
	base += int64(len(job.CreatedBy))
	base += int64(len(job.WorkerID))
	base += int64(len(job.Type))

	// Tags
	for _, t := range job.Tags {
		base += int64(len(t)) + 16 // string header + data
	}

	// Metadata
	for k, v := range job.Metadata {
		base += int64(len(k)) + int64(len(v)) + 48 // map entry overhead
	}

	// Params — rough estimate (JSON-serializable blob)
	base += 512 // conservative estimate for params interface

	// heap item overhead + pointer
	base += int64(unsafe.Sizeof(item{})) + 8

	// Minimum floor
	if base < 1024 {
		base = 1024
	}

	return base
}
