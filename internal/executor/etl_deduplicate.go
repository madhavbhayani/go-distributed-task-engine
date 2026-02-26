package executor

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// ════════════════════════════════════════════════════════════════
//  ETL Deduplicate Executor
//
//  Detects and removes duplicate records from a dataset.
//  Features:
//    • Exact matching    — hash-based key deduplication
//    • Fuzzy matching    — Levenshtein distance similarity
//    • Keep strategy     — first / last / most_complete
//    • Dry-run mode      — flag duplicates without removing
//    • Chunk processing  — progress updates per chunk
// ════════════════════════════════════════════════════════════════

func (e *Engine) executeETLDeduplicate(ctx context.Context, job *models.Job) (any, error) {
	params, err := toETLDeduplicateParams(job.Params)
	if err != nil {
		return nil, fmt.Errorf("invalid deduplicate params: %w", err)
	}

	ds, err := e.datasetStore.Get(params.DatasetID)
	if err != nil {
		return nil, err
	}

	workDS := ds
	if params.CreateCopy {
		outID := params.OutputID
		if outID == "" {
			outID = params.DatasetID + "_deduped"
		}
		workDS = ds.Clone(outID)
	}

	startTime := time.Now()
	totalRecords := int64(len(workDS.Records))

	var duplicatesFound int64
	var kept []dataset.Record
	var dupeIndices []int           // original 0-based indices of duplicate rows
	var groupInfos []dedupGroupInfo // group details for report

	switch params.Strategy {
	case "exact":
		kept, dupeIndices, duplicatesFound, groupInfos = exactDedup(ctx, workDS.Records, params.MatchColumns, params.KeepStrategy)
	case "fuzzy":
		threshold := params.FuzzyThreshold
		if threshold <= 0 {
			threshold = 0.85
		}
		kept, dupeIndices, duplicatesFound, groupInfos = fuzzyDedup(ctx, workDS.Records, params.MatchColumns, params.KeepStrategy, threshold)
	default:
		return nil, fmt.Errorf("unsupported dedup strategy: %s", params.Strategy)
	}

	// Build DedupReport
	dedupReport := &models.DedupReport{
		Strategy:        params.Strategy,
		MatchColumns:    params.MatchColumns,
		KeepStrategy:    params.KeepStrategy,
		TotalGroups:     int64(len(groupInfos)),
		TotalDuplicates: duplicatesFound,
		TotalKept:       int64(len(kept)),
	}
	// Include all group summaries for the frontend
	for i := 0; i < len(groupInfos); i++ {
		gi := groupInfos[i]
		// Sample up to 3 dropped rows
		droppedSample := make([]map[string]string, 0, 3)
		for j, idx := range gi.droppedIndices {
			if j >= 3 {
				break
			}
			row := make(map[string]string)
			for k, v := range workDS.Records[idx] {
				row[k] = v
			}
			row["_row_number"] = fmt.Sprintf("%d", idx+1)
			droppedSample = append(droppedSample, row)
		}
		dedupReport.Groups = append(dedupReport.Groups, models.DedupGroupSummary{
			MatchKey:    gi.matchKey,
			GroupSize:   gi.groupSize,
			KeptIndex:   gi.keptIndex + 1, // 1-based
			DroppedRows: droppedSample,
		})
	}

	processed := int64(len(kept))
	removed := duplicatesFound

	// Dry run: store a dataset of the duplicate rows + preview for the UI
	if params.DryRun {
		// Build the duplicates dataset
		dupeCols := append([]string{"_row_number"}, ds.Columns...)
		dupeRecords := make([]dataset.Record, 0, len(dupeIndices))
		for _, idx := range dupeIndices {
			rec := make(dataset.Record, len(ds.Columns)+1)
			rec["_row_number"] = fmt.Sprintf("%d", idx+1) // 1-based
			for k, v := range workDS.Records[idx] {
				rec[k] = v
			}
			dupeRecords = append(dupeRecords, rec)
		}

		dupesDatasetID := params.DatasetID + "_duplicates"
		dupesDS := &dataset.Dataset{
			ID:      dupesDatasetID,
			Columns: dupeCols,
			Records: dupeRecords,
		}
		e.datasetStore.Put(dupesDS)

		// Build a preview (first 100 rows) for the frontend dialog
		previewLimit := 100
		if len(dupeRecords) < previewLimit {
			previewLimit = len(dupeRecords)
		}
		previewRows := make([]map[string]string, previewLimit)
		for i := 0; i < previewLimit; i++ {
			previewRows[i] = map[string]string(dupeRecords[i])
		}

		_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
			TotalItems:     totalRecords,
			ProcessedItems: totalRecords,
			Percentage:     100,
			CurrentStep:    "completed (dry-run)",
			Message:        fmt.Sprintf("Dry run: found %d duplicates in %d records", duplicatesFound, totalRecords),
		})

		e.logger.Info("ETL deduplicate dry-run completed",
			slog.String("job_id", job.ID),
			slog.String("dataset_id", params.DatasetID),
			slog.Int64("duplicates_found", duplicatesFound),
			slog.String("duplicates_dataset", dupesDatasetID),
		)

		return &models.ETLJobResult{
			DatasetID:           params.DatasetID,
			TotalRecords:        totalRecords,
			Processed:           totalRecords,
			DuplicatesFound:     duplicatesFound,
			DryRun:              true,
			DuplicateRows:       previewRows,
			DuplicatesDatasetID: dupesDatasetID,
			DedupReport:         dedupReport,
			Duration:            time.Since(startTime).Round(time.Millisecond).String(),
		}, nil
	}

	// Apply dedup
	workDS.Records = kept
	e.datasetStore.Put(workDS)

	_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
		TotalItems:     totalRecords,
		ProcessedItems: processed,
		SkippedItems:   removed,
		Percentage:     100,
		CurrentStep:    "completed",
		Message:        fmt.Sprintf("Deduplicated '%s': %d kept, %d removed", workDS.ID, processed, removed),
	})

	e.logger.Info("ETL deduplicate completed",
		slog.String("job_id", job.ID),
		slog.String("dataset_id", workDS.ID),
		slog.Int64("kept", processed),
		slog.Int64("duplicates_removed", removed),
		slog.Duration("elapsed", time.Since(startTime)),
	)

	result := &models.ETLJobResult{
		DatasetID:       workDS.ID,
		TotalRecords:    totalRecords,
		Processed:       processed,
		Skipped:         removed,
		DuplicatesFound: duplicatesFound,
		DedupReport:     dedupReport,
		Duration:        time.Since(startTime).Round(time.Millisecond).String(),
	}
	e.reanalyzeDataset(workDS, result)
	return result, nil
}

// dedupGroupInfo holds info about a dedup group for reporting.
type dedupGroupInfo struct {
	matchKey       string
	groupSize      int
	keptIndex      int // 0-based
	droppedIndices []int
}

// ──────────────────────────────────────────────
// Exact deduplication (hash-key based)
// ──────────────────────────────────────────────

func exactDedup(ctx context.Context, records []dataset.Record, matchCols []string, keepStrategy string) ([]dataset.Record, []int, int64, []dedupGroupInfo) {
	type group struct {
		indices []int
	}

	seen := make(map[string]*group, len(records))
	order := make([]string, 0, len(records))

	for i, rec := range records {
		select {
		case <-ctx.Done():
			return records, nil, 0, nil // cancelled
		default:
		}

		key := buildMatchKey(rec, matchCols)
		if g, exists := seen[key]; exists {
			g.indices = append(g.indices, i)
		} else {
			seen[key] = &group{indices: []int{i}}
			order = append(order, key)
		}
	}

	var duplicates int64
	kept := make([]dataset.Record, 0, len(order))
	var dupeIndices []int
	var groupInfos []dedupGroupInfo

	for _, key := range order {
		g := seen[key]
		if len(g.indices) > 1 {
			winner := pickWinner(records, g.indices, keepStrategy)
			var dropped []int
			for _, idx := range g.indices {
				if idx != winner {
					dupeIndices = append(dupeIndices, idx)
					dropped = append(dropped, idx)
				}
			}
			duplicates += int64(len(g.indices) - 1)
			kept = append(kept, records[winner])
			groupInfos = append(groupInfos, dedupGroupInfo{
				matchKey:       key,
				groupSize:      len(g.indices),
				keptIndex:      winner,
				droppedIndices: dropped,
			})
		} else {
			kept = append(kept, records[g.indices[0]])
		}
	}

	sort.Ints(dupeIndices)
	return kept, dupeIndices, duplicates, groupInfos
}

// ──────────────────────────────────────────────
// Fuzzy deduplication (Levenshtein similarity)
// ──────────────────────────────────────────────

func fuzzyDedup(ctx context.Context, records []dataset.Record, matchCols []string, keepStrategy string, threshold float64) ([]dataset.Record, []int, int64, []dedupGroupInfo) {
	n := len(records)
	eliminated := make([]bool, n)
	var duplicates int64
	// Track groups: winner → dropped indices
	groupMap := make(map[int][]int)

	// Sort by match key for locality
	indices := make([]int, n)
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(a, b int) bool {
		return buildMatchKey(records[indices[a]], matchCols) < buildMatchKey(records[indices[b]], matchCols)
	})

	// Compare each record against following records within a sliding window
	windowSize := 50 // compare against next N records (performance bound)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return records, nil, duplicates, nil
		default:
		}

		idx := indices[i]
		if eliminated[idx] {
			continue
		}

		keyA := buildMatchKey(records[idx], matchCols)

		limit := i + windowSize
		if limit > n {
			limit = n
		}
		for j := i + 1; j < limit; j++ {
			jdx := indices[j]
			if eliminated[jdx] {
				continue
			}

			keyB := buildMatchKey(records[jdx], matchCols)
			sim := similarity(keyA, keyB)

			if sim >= threshold {
				// These are duplicates — eliminate the loser
				winner := pickWinner(records, []int{idx, jdx}, keepStrategy)
				loser := idx
				if winner == idx {
					loser = jdx
				}
				eliminated[loser] = true
				duplicates++
				groupMap[winner] = append(groupMap[winner], loser)
			}
		}
	}

	kept := make([]dataset.Record, 0, n-int(duplicates))
	var dupeIndices []int
	for i, rec := range records {
		if eliminated[i] {
			dupeIndices = append(dupeIndices, i)
		} else {
			kept = append(kept, rec)
		}
	}

	// Build group infos from groupMap
	var groupInfos []dedupGroupInfo
	for winner, dropped := range groupMap {
		groupInfos = append(groupInfos, dedupGroupInfo{
			matchKey:       buildMatchKey(records[winner], matchCols),
			groupSize:      1 + len(dropped),
			keptIndex:      winner,
			droppedIndices: dropped,
		})
	}

	return kept, dupeIndices, duplicates, groupInfos
}

// ──────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────

func buildMatchKey(rec dataset.Record, cols []string) string {
	if len(cols) == 0 {
		// If no columns specified, use all columns from the record
		cols = make([]string, 0, len(rec))
		for k := range rec {
			cols = append(cols, k)
		}
		sort.Strings(cols) // deterministic order
	}
	parts := make([]string, len(cols))
	for i, col := range cols {
		v := strings.ToLower(strings.TrimSpace(rec[col]))
		// Treat null sentinels as empty for matching purposes
		if cleanNullSentinels[v] {
			v = ""
		}
		// Collapse whitespace for better matching
		v = reDupWhitespace.ReplaceAllString(v, " ")
		parts[i] = v
	}
	return strings.Join(parts, "|")
}

func pickWinner(records []dataset.Record, indices []int, strategy string) int {
	switch strategy {
	case "last":
		return indices[len(indices)-1]
	case "most_complete":
		bestIdx := indices[0]
		bestCount := countNonEmpty(records[indices[0]])
		for _, idx := range indices[1:] {
			c := countNonEmpty(records[idx])
			if c > bestCount {
				bestCount = c
				bestIdx = idx
			}
		}
		return bestIdx
	default: // "first"
		return indices[0]
	}
}

func countNonEmpty(rec dataset.Record) int {
	count := 0
	for _, v := range rec {
		if strings.TrimSpace(v) != "" {
			count++
		}
	}
	return count
}

// similarity computes normalized Levenshtein similarity (0.0–1.0).
func similarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 1.0
	}
	dist := levenshtein(a, b)
	return 1.0 - float64(dist)/float64(maxLen)
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Optimize: single row DP
	prev := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(
				prev[j]+1,
				curr[j-1]+1,
				prev[j-1]+cost,
			)
		}
		prev = curr
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
