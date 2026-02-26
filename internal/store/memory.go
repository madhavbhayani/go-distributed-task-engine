package store

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
	"github.com/google/uuid"
)

// ════════════════════════════════════════════════════════════════
//  In-Memory Job Store
//  Thread-safe, priority-aware job storage.
//  Will be replaced with Redis/PostgreSQL in production phase.
// ════════════════════════════════════════════════════════════════

// JobStore defines the interface for job persistence.
type JobStore interface {
	Create(job *models.Job) error
	Get(id string) (*models.Job, error)
	Update(job *models.Job) error
	Delete(id string) error
	List(filter ListFilter) ([]*models.Job, int, error)
	UpdateStatus(id string, status models.JobStatus) error
	UpdateProgress(id string, progress models.Progress) error
}

// ListFilter contains parameters for filtering and paginating jobs.
type ListFilter struct {
	Status   models.JobStatus
	Type     models.JobType
	Priority models.Priority
	Tag      string
	Page     int
	PageSize int
	SortBy   string // "created_at" | "priority" | "status"
	SortDir  string // "asc" | "desc"
}

// ──────────────────────────────────────────────
// MemoryStore implementation
// ──────────────────────────────────────────────

// MemoryStore is a thread-safe in-memory implementation of JobStore.
type MemoryStore struct {
	mu   sync.RWMutex
	jobs map[string]*models.Job
}

// NewMemoryStore creates a new in-memory job store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		jobs: make(map[string]*models.Job),
	}
}

// Create adds a new job to the store with a generated UUID.
func (s *MemoryStore) Create(job *models.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job.ID == "" {
		job.ID = uuid.New().String()
	}
	job.CreatedAt = time.Now().UTC()
	job.Status = models.StatusPending

	if job.Priority == 0 {
		job.Priority = models.PriorityDefault
	}

	s.jobs[job.ID] = job
	return nil
}

// Get retrieves a job by ID.
func (s *MemoryStore) Get(id string) (*models.Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, ok := s.jobs[id]
	if !ok {
		return nil, fmt.Errorf("job not found: %s", id)
	}
	return job, nil
}

// Update replaces a job in the store.
func (s *MemoryStore) Update(job *models.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[job.ID]; !ok {
		return fmt.Errorf("job not found: %s", job.ID)
	}
	s.jobs[job.ID] = job
	return nil
}

// Delete removes a job from the store.
func (s *MemoryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("job not found: %s", id)
	}
	delete(s.jobs, id)
	return nil
}

// UpdateStatus changes a job's status and sets lifecycle timestamps.
func (s *MemoryStore) UpdateStatus(id string, status models.JobStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	now := time.Now().UTC()
	job.Status = status

	switch status {
	case models.StatusQueued:
		job.QueuedAt = &now
	case models.StatusRunning:
		job.StartedAt = &now
	case models.StatusCompleted, models.StatusFailed, models.StatusCancelled:
		job.CompletedAt = &now
	}

	return nil
}

// UpdateProgress updates a job's progress data.
func (s *MemoryStore) UpdateProgress(id string, progress models.Progress) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	job.Progress = progress
	return nil
}

// List returns a filtered, sorted, paginated list of jobs.
func (s *MemoryStore) List(filter ListFilter) ([]*models.Job, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Apply filters
	var filtered []*models.Job
	for _, job := range s.jobs {
		if filter.Status != "" && job.Status != filter.Status {
			continue
		}
		if filter.Type != "" && job.Type != filter.Type {
			continue
		}
		if filter.Priority != 0 && job.Priority != filter.Priority {
			continue
		}
		if filter.Tag != "" && !containsTag(job.Tags, filter.Tag) {
			continue
		}
		filtered = append(filtered, job)
	}

	totalCount := len(filtered)

	// Sort
	sortBy := filter.SortBy
	if sortBy == "" {
		sortBy = "created_at"
	}
	sortDir := filter.SortDir
	if sortDir == "" {
		sortDir = "desc"
	}
	sort.Slice(filtered, func(i, j int) bool {
		var less bool
		switch sortBy {
		case "priority":
			less = filtered[i].Priority > filtered[j].Priority
		case "status":
			less = filtered[i].Status < filtered[j].Status
		default: // created_at
			less = filtered[i].CreatedAt.After(filtered[j].CreatedAt)
		}
		if sortDir == "asc" {
			return !less
		}
		return less
	})

	// Paginate
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	start := (page - 1) * pageSize
	if start >= len(filtered) {
		return []*models.Job{}, totalCount, nil
	}

	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}

	return filtered[start:end], totalCount, nil
}

func containsTag(tags []string, target string) bool {
	for _, t := range tags {
		if t == target {
			return true
		}
	}
	return false
}
