package monitor

import (
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"
)

// ════════════════════════════════════════════════════════════════
//  RAM Monitor
//
//  Tracks Go runtime memory usage and enforces the 500 MB cap.
//  Exposes real-time stats via Stats() for the health endpoint
//  and queue back-pressure decisions.
// ════════════════════════════════════════════════════════════════

// RAMStats holds current memory metrics.
type RAMStats struct {
	// Go runtime stats
	AllocBytes      uint64 `json:"alloc_bytes"`       // Current heap allocation
	TotalAllocBytes uint64 `json:"total_alloc_bytes"` // Cumulative allocation
	SysBytes        uint64 `json:"sys_bytes"`         // Total memory from OS
	HeapInUseBytes  uint64 `json:"heap_in_use_bytes"`
	HeapObjects     uint64 `json:"heap_objects"`
	NumGC           uint32 `json:"num_gc"`
	GoroutineCount  int    `json:"goroutine_count"`

	// Budget tracking
	CapBytes      int64   `json:"cap_bytes"`      // Hard cap
	UsagePct      float64 `json:"usage_pct"`      // Alloc / Cap * 100
	UnderCap      bool    `json:"under_cap"`      // true = safe
	HeadroomBytes int64   `json:"headroom_bytes"` // Cap - Alloc

	// Human-readable
	AllocMB    float64 `json:"alloc_mb"`
	SysMB      float64 `json:"sys_mb"`
	CapMB      float64 `json:"cap_mb"`
	HeadroomMB float64 `json:"headroom_mb"`
}

// RAMMonitor periodically samples memory and provides stats.
type RAMMonitor struct {
	capBytes int64
	logger   *slog.Logger
	stop     chan struct{}

	// Cached stats (updated periodically, read lock-free)
	cached atomic.Value // *RAMStats
}

// NewRAMMonitor creates a memory monitor with the given cap in MB.
func NewRAMMonitor(capMB int64, logger *slog.Logger) *RAMMonitor {
	if capMB <= 0 {
		capMB = 500
	}

	m := &RAMMonitor{
		capBytes: capMB * 1024 * 1024,
		logger:   logger,
		stop:     make(chan struct{}),
	}

	// Initial sample
	m.sample()

	logger.Info("RAM monitor initialized",
		slog.Int64("cap_mb", capMB),
		slog.Int64("cap_bytes", m.capBytes),
	)

	return m
}

// Start begins periodic memory sampling.
func (m *RAMMonitor) Start(interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				m.sample()
			}
		}
	}()
}

// Stop halts the monitoring goroutine.
func (m *RAMMonitor) Stop() {
	close(m.stop)
}

// Stats returns the latest cached memory stats. Lock-free.
func (m *RAMMonitor) Stats() RAMStats {
	if v := m.cached.Load(); v != nil {
		return *v.(*RAMStats)
	}
	return RAMStats{}
}

// UnderCap returns true if current allocation is below the RAM cap.
func (m *RAMMonitor) UnderCap() bool {
	return m.Stats().UnderCap
}

// HeadroomBytes returns how many bytes remain before hitting the cap.
func (m *RAMMonitor) HeadroomBytes() int64 {
	return m.Stats().HeadroomBytes
}

// sample reads runtime.MemStats and caches the result.
func (m *RAMMonitor) sample() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	alloc := int64(ms.Alloc)
	headroom := m.capBytes - alloc
	pct := float64(alloc) / float64(m.capBytes) * 100

	stats := &RAMStats{
		AllocBytes:      ms.Alloc,
		TotalAllocBytes: ms.TotalAlloc,
		SysBytes:        ms.Sys,
		HeapInUseBytes:  ms.HeapInuse,
		HeapObjects:     ms.HeapObjects,
		NumGC:           ms.NumGC,
		GoroutineCount:  runtime.NumGoroutine(),

		CapBytes:      m.capBytes,
		UsagePct:      pct,
		UnderCap:      alloc < m.capBytes,
		HeadroomBytes: headroom,

		AllocMB:    float64(ms.Alloc) / (1024 * 1024),
		SysMB:      float64(ms.Sys) / (1024 * 1024),
		CapMB:      float64(m.capBytes) / (1024 * 1024),
		HeadroomMB: float64(headroom) / (1024 * 1024),
	}

	m.cached.Store(stats)

	// Warn if approaching cap
	if pct > 90 {
		m.logger.Warn("RAM usage CRITICAL",
			slog.Float64("usage_pct", pct),
			slog.Float64("alloc_mb", stats.AllocMB),
			slog.Float64("cap_mb", stats.CapMB),
		)
	} else if pct > 75 {
		m.logger.Warn("RAM usage HIGH",
			slog.Float64("usage_pct", pct),
			slog.Float64("alloc_mb", stats.AllocMB),
		)
	}
}
