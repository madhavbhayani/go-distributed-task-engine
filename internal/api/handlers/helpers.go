package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/api/middleware"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
	"github.com/google/uuid"
)

// ════════════════════════════════════════════════════════════════
//  Shared handler utilities
// ════════════════════════════════════════════════════════════════

// respondJSON writes a standard JSON response.
func respondJSON(w http.ResponseWriter, status int, data *models.APIResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// success builds a successful APIResponse.
func success(requestID, message string, data any) *models.APIResponse {
	return &models.APIResponse{
		Success:   true,
		Message:   message,
		Data:      data,
		RequestID: requestID,
		Timestamp: time.Now().UTC(),
	}
}

// apiError builds an error APIResponse.
func apiError(requestID, code, message string, details map[string]string) *models.APIResponse {
	return &models.APIResponse{
		Success: false,
		Error: &models.APIError{
			Code:    code,
			Message: message,
			Details: details,
		},
		RequestID: requestID,
		Timestamp: time.Now().UTC(),
	}
}

// decodeJSON decodes a JSON request body into dst.
func decodeJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

// getRequestID extracts the request ID from context (set by middleware).
func getRequestID(r *http.Request) string {
	if id, ok := r.Context().Value(middleware.RequestIDKey).(string); ok {
		return id
	}
	return "unknown"
}

// buildJob constructs a new Job from the common request wrapper.
func buildJob(req models.CreateJobRequest, jobType models.JobType, params any) *models.Job {
	p := models.Priority(3)
	if req.Priority.IsValid() {
		p = req.Priority
	}
	maxRetries := req.MaxRetries
	if maxRetries == 0 {
		maxRetries = 3
	}
	return &models.Job{
		ID:          uuid.New().String(),
		Type:        jobType,
		Name:        req.Name,
		Priority:    p,
		Status:      models.StatusPending,
		Params:      params,
		MaxRetries:  maxRetries,
		CallbackURL: req.CallbackURL,
		Tags:        req.Tags,
		Metadata:    req.Metadata,
		CreatedAt:   time.Now().UTC(),
	}
}

// buildCreateResponse builds a CreateJobResponse summary from a Job.
func buildCreateResponse(job *models.Job) models.CreateJobResponse {
	return models.CreateJobResponse{
		JobID:     job.ID,
		Type:      job.Type,
		Status:    job.Status,
		Priority:  job.Priority,
		CreatedAt: job.CreatedAt,
	}
}
