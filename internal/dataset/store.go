package dataset

import (
	"fmt"
	"sort"
	"sync"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/analyzer"
)

// ════════════════════════════════════════════════════════════════
//  In-Memory Dataset Store
//
//  Holds imported CSV data so that subsequent ETL steps
//  (clean, normalize, deduplicate) can operate on it.
//  Each dataset is identified by a string ID.
//
//  Thread-safe for concurrent read/write from multiple workers.
// ════════════════════════════════════════════════════════════════

// Record is a single row: column name → string value.
type Record map[string]string

// Dataset holds tabular data in memory.
type Dataset struct {
	ID       string                    `json:"id"`
	Columns  []string                  `json:"columns"` // ordered column names
	Records  []Record                  `json:"records"`
	Analysis *analyzer.DatasetAnalysis `json:"analysis,omitempty"`
}

// Clone creates a deep copy of the dataset with a new ID.
func (d *Dataset) Clone(newID string) *Dataset {
	cols := make([]string, len(d.Columns))
	copy(cols, d.Columns)

	recs := make([]Record, len(d.Records))
	for i, r := range d.Records {
		nr := make(Record, len(r))
		for k, v := range r {
			nr[k] = v
		}
		recs[i] = nr
	}

	return &Dataset{
		ID:       newID,
		Columns:  cols,
		Records:  recs,
		Analysis: d.Analysis,
	}
}

// Store manages named datasets.
type Store struct {
	mu       sync.RWMutex
	datasets map[string]*Dataset
}

// NewStore creates an empty dataset store.
func NewStore() *Store {
	return &Store{
		datasets: make(map[string]*Dataset),
	}
}

// Put saves (or overwrites) a dataset.
func (s *Store) Put(ds *Dataset) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.datasets[ds.ID] = ds
}

// Get retrieves a dataset by ID.
func (s *Store) Get(id string) (*Dataset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ds, ok := s.datasets[id]
	if !ok {
		return nil, fmt.Errorf("dataset not found: %s", id)
	}
	return ds, nil
}

// Delete removes a dataset.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.datasets, id)
}

// List returns all dataset IDs and their record counts.
func (s *Store) List() []DatasetInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	infos := make([]DatasetInfo, 0, len(s.datasets))
	for _, ds := range s.datasets {
		infos = append(infos, DatasetInfo{
			ID:          ds.ID,
			Columns:     ds.Columns,
			RecordCount: len(ds.Records),
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ID < infos[j].ID
	})
	return infos
}

// DatasetInfo is a summary of a stored dataset.
type DatasetInfo struct {
	ID          string   `json:"id"`
	Columns     []string `json:"columns"`
	RecordCount int      `json:"record_count"`
}
