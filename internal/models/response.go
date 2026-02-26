package models

import "time"

// ════════════════════════════════════════════════════════════════
//  API Response Envelopes
//  Consistent response structure for all endpoints.
// ════════════════════════════════════════════════════════════════

// APIResponse is the standard envelope for all API responses.
type APIResponse struct {
	Success   bool      `json:"success"`
	Message   string    `json:"message,omitempty"`
	Data      any       `json:"data,omitempty"`
	Error     *APIError `json:"error,omitempty"`
	RequestID string    `json:"request_id"`
	Timestamp time.Time `json:"timestamp"`
}

// APIError contains structured error information.
type APIError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

// ──────────────────────────────────────────────
// Job Submission Request / Response
// ──────────────────────────────────────────────

// CreateJobRequest is the common wrapper for submitting any job.
type CreateJobRequest struct {
	// Optional human-readable name for the job
	Name string `json:"name,omitempty"`

	// Priority (1-10). Default: 3
	Priority Priority `json:"priority"`

	// Max retry attempts on failure. Default: 3
	MaxRetries int `json:"max_retries"`

	// Optional callback URL — engine will POST status on completion/failure
	CallbackURL string `json:"callback_url,omitempty"`

	// Tags for filtering/grouping
	Tags []string `json:"tags,omitempty"`

	// Arbitrary key-value metadata
	Metadata map[string]string `json:"metadata,omitempty"`

	// Task-specific parameters (type varies per endpoint)
	Params any `json:"params" validate:"required"`
}

// CreateJobResponse contains the newly created job summary.
type CreateJobResponse struct {
	JobID     string    `json:"job_id"`
	Type      JobType   `json:"type"`
	Status    JobStatus `json:"status"`
	Priority  Priority  `json:"priority"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// ──────────────────────────────────────────────
// Job Query / Listing
// ──────────────────────────────────────────────

// ListJobsQuery represents query parameters for listing jobs.
type ListJobsQuery struct {
	Status   JobStatus `json:"status,omitempty"`
	Type     JobType   `json:"type,omitempty"`
	Priority Priority  `json:"priority,omitempty"`
	Tag      string    `json:"tag,omitempty"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
	SortBy   string    `json:"sort_by"`  // "created_at" | "priority" | "status"
	SortDir  string    `json:"sort_dir"` // "asc" | "desc"
}

// PaginatedResponse wraps a list with pagination metadata.
type PaginatedResponse struct {
	Items      any `json:"items"`
	TotalCount int `json:"total_count"`
	Page       int `json:"page"`
	PageSize   int `json:"page_size"`
	TotalPages int `json:"total_pages"`
}

// ──────────────────────────────────────────────
// Health Check
// ──────────────────────────────────────────────

// HealthResponse contains system health information.
type HealthResponse struct {
	Status    string                 `json:"status"` // "healthy" | "degraded" | "unhealthy"
	Version   string                 `json:"version"`
	Uptime    string                 `json:"uptime"`
	Timestamp time.Time              `json:"timestamp"`
	Checks    map[string]HealthCheck `json:"checks"`
}

// HealthCheck represents a single subsystem health check.
type HealthCheck struct {
	Status  string `json:"status"` // "up" | "down"
	Message string `json:"message,omitempty"`
	Latency string `json:"latency,omitempty"`
}
