package models

import "time"

// ──────────────────────────────────────────────
// Job Status Lifecycle
// ──────────────────────────────────────────────

// JobStatus represents the current state of a job in its lifecycle.
//
// Lifecycle:  pending → queued → running → completed
//                                       → failed (→ retry → queued)
//                          ↕ cancelled
type JobStatus string

const (
	StatusPending   JobStatus = "pending"   // Accepted, awaiting queue
	StatusQueued    JobStatus = "queued"    // In priority queue, waiting for worker
	StatusRunning   JobStatus = "running"   // Actively being processed
	StatusCompleted JobStatus = "completed" // Finished successfully
	StatusFailed    JobStatus = "failed"    // Execution failed
	StatusCancelled JobStatus = "cancelled" // Cancelled by client
)

// ──────────────────────────────────────────────
// Job Types
// ──────────────────────────────────────────────

// JobType categorizes the work to be performed.
type JobType string

const (
	// ETL / Data migration job types
	JobTypeETLImport      JobType = "etl.import"
	JobTypeETLClean       JobType = "etl.clean"
	JobTypeETLNormalize   JobType = "etl.normalize"
	JobTypeETLDeduplicate JobType = "etl.deduplicate"
	JobTypeETLPipeline    JobType = "etl.pipeline"
)

// ──────────────────────────────────────────────
// Progress Tracking
// ──────────────────────────────────────────────

// Progress holds real-time progress information for a running job.
type Progress struct {
	TotalItems     int64   `json:"total_items"`
	ProcessedItems int64   `json:"processed_items"`
	FailedItems    int64   `json:"failed_items"`
	SkippedItems   int64   `json:"skipped_items"`
	Percentage     float64 `json:"percentage"`
	CurrentStep    string  `json:"current_step,omitempty"`
	Message        string  `json:"message,omitempty"`
}

// ──────────────────────────────────────────────
// Core Job Entity
// ──────────────────────────────────────────────

// Job is the central work unit in the distributed processing engine.
// It carries its type, priority, parameters, lifecycle timestamps,
// and progress information.
type Job struct {
	// Identity
	ID   string  `json:"id"`
	Type JobType `json:"type"`
	Name string  `json:"name,omitempty"` // Optional human-readable name

	// Scheduling
	Priority Priority  `json:"priority"`
	Status   JobStatus `json:"status"`

	// Progress
	Progress Progress `json:"progress"`

	// Lifecycle timestamps
	CreatedAt   time.Time  `json:"created_at"`
	QueuedAt    *time.Time `json:"queued_at,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// Task parameters (type-specific, serialized as JSON)
	Params any `json:"params"`

	// Result data (populated on completion)
	Result any `json:"result,omitempty"`

	// Error information
	Error      string `json:"error,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	StackTrace string `json:"stack_trace,omitempty"`

	// Retry policy
	RetryCount int `json:"retry_count"`
	MaxRetries int `json:"max_retries"`

	// Webhook callback (optional — notify on completion/failure)
	CallbackURL string `json:"callback_url,omitempty"`

	// Metadata & tagging
	Tags     []string          `json:"tags,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`

	// Ownership
	CreatedBy string `json:"created_by,omitempty"`
	WorkerID  string `json:"worker_id,omitempty"`
}

// Duration returns the processing time if the job has started.
func (j *Job) Duration() *time.Duration {
	if j.StartedAt == nil {
		return nil
	}
	end := time.Now()
	if j.CompletedAt != nil {
		end = *j.CompletedAt
	}
	d := end.Sub(*j.StartedAt)
	return &d
}
