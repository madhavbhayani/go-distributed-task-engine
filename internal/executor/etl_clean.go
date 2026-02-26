package executor

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// ════════════════════════════════════════════════════════════════
//  ETL Clean Executor
//
//  Applies cleaning rules to an in-memory dataset.
//  Features:
//    • Chunk processing — processes records in batches
//    • Streaming progress updates
//    • Supports: trim_whitespace, to_lowercase, to_uppercase,
//      remove_html, regex_replace, fill_null, drop_null,
//      remove_special_chars, standardize_date, type_cast,
//      remove_newlines, collapse_whitespace,
//      fix_mismatched_types, fix_categorical_outliers
// ════════════════════════════════════════════════════════════════

// cleanNullSentinels — comprehensive null/missing value set. Case-insensitive.
var cleanNullSentinels = map[string]bool{
	"":          true,
	"null":      true,
	"n/a":       true,
	"na":        true,
	"none":      true,
	"nil":       true,
	"-":         true,
	"<na>":      true,
	"nan":       true,
	"missing":   true,
	"#n/a":      true,
	"#null!":    true,
	"#ref!":     true,
	"#value!":   true,
	"undefined": true,
	"inf":       true,
	"-inf":      true,
	"?":         true,
	"..":        true,
	"--":        true,
}

var reDupWhitespace = regexp.MustCompile(`\s{2,}`)

// isNullValue checks if a string represents a null/missing value.
func isNullValue(val string) bool {
	return cleanNullSentinels[strings.ToLower(strings.TrimSpace(val))]
}

// formatFillValueByType ensures a numeric fill value respects the column's
// inferred data type.  For integer columns the value is rounded to the nearest
// whole number; for floats it keeps up to 4 significant decimals.
func formatFillValueByType(raw float64, inferredType string) string {
	switch inferredType {
	case "integer":
		return strconv.FormatInt(int64(math.Round(raw)), 10)
	default: // float, numeric_string, or unknown
		s := fmt.Sprintf("%.4f", raw)
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		return s
	}
}

func (e *Engine) executeETLClean(ctx context.Context, job *models.Job) (any, error) {
	params, err := toETLCleanParams(job.Params)
	if err != nil {
		return nil, fmt.Errorf("invalid clean params: %w", err)
	}

	// ── Load dataset ──────────────────────────────
	ds, err := e.datasetStore.Get(params.DatasetID)
	if err != nil {
		return nil, err
	}

	// Create copy if requested
	workDS := ds
	if params.CreateCopy {
		outID := params.OutputID
		if outID == "" {
			outID = params.DatasetID + "_cleaned"
		}
		workDS = ds.Clone(outID)
	}

	startTime := time.Now()
	totalRecords := int64(len(workDS.Records))
	var processed, failed, skipped int64

	// ── Pre-processing: drop_empty_columns ────────
	// Remove columns from the dataset where every value is null/empty.
	var droppedCols []string
	for _, rule := range params.Rules {
		if rule.Operation == "drop_empty_columns" {
			threshold := 0.0 // default: drop columns with 0% fill
			if t := rule.Params["min_fill_rate"]; t != "" {
				if v, err := parseFloat(t); err == nil {
					threshold = v
				}
			}

			keepCols := make([]string, 0, len(workDS.Columns))
			for _, col := range workDS.Columns {
				filled := 0
				for _, rec := range workDS.Records {
					v := strings.TrimSpace(rec[col])
					low := strings.ToLower(v)
					if v != "" && low != "null" && low != "none" && low != "na" && low != "n/a" && low != "nan" {
						filled++
					}
				}
				fillRate := 0.0
				if len(workDS.Records) > 0 {
					fillRate = float64(filled) / float64(len(workDS.Records))
				}
				if fillRate > threshold {
					keepCols = append(keepCols, col)
				} else {
					droppedCols = append(droppedCols, col)
					// Remove column from every record
					for _, rec := range workDS.Records {
						delete(rec, col)
					}
				}
			}
			droppedCount := len(droppedCols)
			if droppedCount > 0 {
				e.logger.Info("Dropped empty columns",
					slog.String("dataset_id", workDS.ID),
					slog.Int("dropped", droppedCount),
					slog.Int("remaining", len(keepCols)),
				)
			}
			workDS.Columns = keepCols
			break // only process one drop_empty_columns rule
		}
	}

	// ── Pre-processing: compute column statistics for smart fill ──
	// For fill_null with strategy=mean/median/mode, pre-compute per-column values.
	type colFillStats struct {
		mean   float64
		median float64
		mode   string
	}
	fillStats := make(map[string]*colFillStats)

	for _, rule := range params.Rules {
		if rule.Operation == "fill_null" {
			strategy := rule.Params["strategy"]
			col := rule.Column
			if strategy == "mean" || strategy == "median" || strategy == "mode" || col != "" {
				cfs := &colFillStats{}
				// Gather column values, compute stats
				var numVals []float64
				freq := make(map[string]int)
				for _, rec := range workDS.Records {
					v := rec[col]
					if isNullValue(v) {
						continue
					}
					freq[v]++
					if f, err := parseFloat(v); err == nil {
						numVals = append(numVals, f)
					}
				}
				if len(numVals) > 0 {
					sum := 0.0
					for _, f := range numVals {
						sum += f
					}
					cfs.mean = sum / float64(len(numVals))
					sorted := make([]float64, len(numVals))
					copy(sorted, numVals)
					sort.Float64s(sorted)
					n := len(sorted)
					if n%2 == 0 {
						cfs.median = (sorted[n/2-1] + sorted[n/2]) / 2.0
					} else {
						cfs.median = sorted[n/2]
					}
				}
				// Mode: most frequent value
				modeVal, modeCount := "", 0
				for v, c := range freq {
					if c > modeCount {
						modeCount = c
						modeVal = v
					}
				}
				cfs.mode = modeVal
				fillStats[col] = cfs
			}
		}
	}

	// ── Pre-processing: compute per-column frequency maps for categorical outlier fixing ──
	// Also pre-compute fill stats for fix_mismatched_types columns (mode values)
	catFreq := make(map[string]map[string]int)
	for _, rule := range params.Rules {
		if rule.Operation == "fix_categorical_outliers" || rule.Operation == "fix_mismatched_types" {
			col := rule.Column
			if _, ok := catFreq[col]; !ok {
				freq := make(map[string]int)
				for _, rec := range workDS.Records {
					v := strings.TrimSpace(rec[col])
					if !isNullValue(v) {
						freq[strings.ToLower(v)]++
					}
				}
				catFreq[col] = freq
			}
			// Ensure fillStats exists for this column too (for mode fallback in fix_mismatched_types)
			if rule.Operation == "fix_mismatched_types" {
				if _, ok := fillStats[col]; !ok {
					cfs := &colFillStats{}
					var numVals []float64
					freq := make(map[string]int)
					for _, rec := range workDS.Records {
						v := rec[col]
						if isNullValue(v) {
							continue
						}
						freq[v]++
						if f, err := parseFloat(v); err == nil {
							numVals = append(numVals, f)
						}
					}
					if len(numVals) > 0 {
						sum := 0.0
						for _, f := range numVals {
							sum += f
						}
						cfs.mean = sum / float64(len(numVals))
						sorted := make([]float64, len(numVals))
						copy(sorted, numVals)
						sort.Float64s(sorted)
						n := len(sorted)
						if n%2 == 0 {
							cfs.median = (sorted[n/2-1] + sorted[n/2]) / 2.0
						} else {
							cfs.median = sorted[n/2]
						}
					}
					modeVal, modeCount := "", 0
					for v, c := range freq {
						if c > modeCount {
							modeCount = c
							modeVal = v
						}
					}
					cfs.mode = modeVal
					fillStats[col] = cfs
				}
			}
		}
	}

	// ── Chunk processing ──────────────────────────
	// Track every operation's effect for the detailed CleanReport.
	type opKey struct {
		operation string
		column    string
	}
	type opTracker struct {
		affected     int64
		sampleBefore []string // up to 5
		sampleAfter  []string // up to 5
	}
	opTrackers := make(map[opKey]*opTracker)
	getTracker := func(op, col string) *opTracker {
		k := opKey{op, col}
		if t, ok := opTrackers[k]; ok {
			return t
		}
		t := &opTracker{}
		opTrackers[k] = t
		return t
	}

	chunkSize := 1000
	for start := 0; start < len(workDS.Records); start += chunkSize {
		// Check cancellation
		select {
		case <-ctx.Done():
			return buildCleanResult(workDS.ID, totalRecords, processed, failed, skipped, startTime), ctx.Err()
		default:
		}

		end := start + chunkSize
		if end > len(workDS.Records) {
			end = len(workDS.Records)
		}
		chunk := workDS.Records[start:end]

		// Apply rules to each record in the chunk
		surviving := make([]dataset.Record, 0, len(chunk))
		for _, rec := range chunk {
			drop := false
			for _, rule := range params.Rules {
				if drop {
					break
				}

				cols := []string{rule.Column}
				if rule.Column == "" {
					cols = workDS.Columns // apply to all columns
				}

				for _, col := range cols {
					val, exists := rec[col]
					if !exists {
						continue
					}

					// Helper: record a change for the report
					trackChange := func(op, oldVal, newVal string) {
						if oldVal == newVal {
							return
						}
						t := getTracker(op, col)
						t.affected++
						if len(t.sampleBefore) < 5 {
							t.sampleBefore = append(t.sampleBefore, oldVal)
							t.sampleAfter = append(t.sampleAfter, newVal)
						}
					}

					switch rule.Operation {
					case "trim_whitespace":
						nv := strings.TrimSpace(val)
						trackChange("trim_whitespace", val, nv)
						rec[col] = nv

					case "to_lowercase":
						nv := strings.ToLower(val)
						trackChange("to_lowercase", val, nv)
						rec[col] = nv

					case "to_uppercase":
						nv := strings.ToUpper(val)
						trackChange("to_uppercase", val, nv)
						rec[col] = nv

					case "remove_html":
						nv := stripHTML(val)
						trackChange("remove_html", val, nv)
						rec[col] = nv

					case "remove_newlines":
						s := strings.ReplaceAll(val, "\r\n", " ")
						s = strings.ReplaceAll(s, "\n", " ")
						s = strings.ReplaceAll(s, "\r", " ")
						nv := strings.TrimSpace(s)
						trackChange("remove_newlines", val, nv)
						rec[col] = nv

					case "collapse_whitespace":
						nv := reDupWhitespace.ReplaceAllString(val, " ")
						trackChange("collapse_whitespace", val, nv)
						rec[col] = nv

					case "regex_replace":
						pattern := rule.Params["pattern"]
						replacement := rule.Params["replacement"]
						if pattern != "" {
							if re, err := regexp.Compile(pattern); err == nil {
								nv := re.ReplaceAllString(val, replacement)
								trackChange("regex_replace", val, nv)
								rec[col] = nv
							}
						}

					case "fill_null":
						// When global null_handling is "skip", never fill nulls
						if params.NullHandling == "skip" {
							continue
						}
						if isNullValue(val) {
							fillVal := rule.Params["fill_value"]
							strategy := rule.Params["strategy"]
							colType := rule.Params["inferred_type"] // e.g. "integer", "float", ...

							// Smart fill based on strategy
							if cfs, ok := fillStats[col]; ok {
								switch strategy {
								case "mean":
									fillVal = formatFillValueByType(cfs.mean, colType)
								case "median":
									fillVal = formatFillValueByType(cfs.median, colType)
								case "mode":
									if cfs.mode != "" {
										fillVal = cfs.mode
									}
								}
							}

							// Global null handling fallback
							if fillVal == "" {
								switch params.NullHandling {
								case "fill_mean":
									if cfs, ok := fillStats[col]; ok {
										fillVal = formatFillValueByType(cfs.mean, colType)
									} else {
										fillVal = "0"
									}
								case "fill_median":
									if cfs, ok := fillStats[col]; ok {
										fillVal = formatFillValueByType(cfs.median, colType)
									} else {
										fillVal = "0"
									}
								case "fill_default":
									// Use mode (most frequent) as the default fill
									if cfs, ok := fillStats[col]; ok && cfs.mode != "" {
										fillVal = cfs.mode
									} else {
										fillVal = "0"
									}
								case "fill_custom":
									if cv, ok := params.CustomFillValues[col]; ok && cv != "" {
										fillVal = cv
									} else if cfs, ok := fillStats[col]; ok && cfs.mode != "" {
										fillVal = cfs.mode
									} else {
										fillVal = "0"
									}
								case "skip":
									continue
								default:
									// Default: use mode
									if cfs, ok := fillStats[col]; ok && cfs.mode != "" {
										fillVal = cfs.mode
									} else {
										fillVal = "0"
									}
								}
							}
							trackChange("fill_null", val, fillVal)
							rec[col] = fillVal
						}

					case "drop_null":
						// ONLY drop rows when user EXPLICITLY chose "drop" strategy.
						// This is a destructive operation — never auto-apply.
						if isNullValue(val) {
							t := getTracker("drop_null", col)
							t.affected++
							if len(t.sampleBefore) < 5 {
								t.sampleBefore = append(t.sampleBefore, val)
								t.sampleAfter = append(t.sampleAfter, "(row dropped)")
							}
							drop = true
						}

					case "remove_special_chars":
						allowed := rule.Params["allow"]
						if allowed == "" {
							allowed = `a-zA-Z0-9\s._-`
						}
						re, err := regexp.Compile(`[^` + allowed + `]`)
						if err == nil {
							nv := re.ReplaceAllString(val, "")
							trackChange("remove_special_chars", val, nv)
							rec[col] = nv
						}

					case "standardize_date":
						nv := standardizeDate(val, rule.Params["format"])
						trackChange("standardize_date", val, nv)
						rec[col] = nv

					case "type_cast":
						nv := strings.TrimSpace(val)
						trackChange("type_cast", val, nv)
						rec[col] = nv

					case "fix_mismatched_types":
						inferredType := rule.Params["inferred_type"]
						trimVal := strings.TrimSpace(val)
						if isNullValue(trimVal) {
							continue
						}
						nv := trimVal
						switch inferredType {
						case "integer":
							if _, err := strconv.ParseInt(trimVal, 10, 64); err != nil {
								cleaned := extractNumeric(trimVal)
								if cleaned != "" {
									nv = cleaned
								} else {
									// Fill with mode (most frequent value) instead of emptying
									if cfs, ok := fillStats[col]; ok && cfs.mode != "" {
										nv = cfs.mode
									} else {
										nv = "0"
									}
								}
							}
						case "float":
							if _, err := strconv.ParseFloat(trimVal, 64); err != nil {
								cleaned := extractNumeric(trimVal)
								if cleaned != "" {
									nv = cleaned
								} else {
									// Fill with mode (most frequent value) instead of emptying
									if cfs, ok := fillStats[col]; ok && cfs.mode != "" {
										nv = cfs.mode
									} else {
										nv = "0"
									}
								}
							}
						case "boolean":
							low := strings.ToLower(trimVal)
							boolMap := map[string]string{
								"true": "true", "false": "false",
								"yes": "true", "no": "false",
								"1": "true", "0": "false",
								"t": "true", "f": "false",
								"y": "true", "n": "false",
							}
							if mapped, ok := boolMap[low]; ok {
								nv = mapped
							} else {
								// Fill with mode instead of emptying
								if cfs, ok := fillStats[col]; ok && cfs.mode != "" {
									nv = cfs.mode
								} else {
									nv = "false"
								}
							}
						case "email":
							if !strings.Contains(trimVal, "@") {
								// Fill with mode instead of emptying
								if cfs, ok := fillStats[col]; ok && cfs.mode != "" {
									nv = cfs.mode
								}
							}
						case "categorical":
							// For categorical: normalize text variants
							// "very high" / "veryhigh" / "very_high" → "very_high"
							nv = normalizeTextToUnderscore(trimVal)
						default:
							// For string/freetext types: normalize spacing variants to underscore
							normalized := normalizeTextToUnderscore(trimVal)
							// Check if this normalized form matches any frequent value in the column
							if freq, ok := catFreq[col]; ok {
								normLow := strings.ToLower(normalized)
								if _, exists := freq[normLow]; exists {
									nv = normalized
								} else {
									// Find the best matching canonical form
									bestVal, bestCnt := "", 0
									for candidate, cnt := range freq {
										if normalizeTextToUnderscore(candidate) == normLow && cnt > bestCnt {
											bestVal = candidate
											bestCnt = cnt
										}
									}
									if bestVal != "" {
										nv = normalizeTextToUnderscore(bestVal)
									} else {
										nv = normalized
									}
								}
							} else {
								nv = normalized
							}
						}
						trackChange("fix_mismatched_types", val, nv)
						rec[col] = nv

					case "fix_categorical_outliers":
						if isNullValue(val) {
							continue
						}
						trimVal := strings.TrimSpace(val)
						lowVal := strings.ToLower(trimVal)
						freq := catFreq[col]
						if freq == nil {
							continue
						}
						totalVals := 0
						for _, c := range freq {
							totalVals += c
						}
						threshold := int(math.Max(1, float64(totalVals)*0.01))
						if freq[lowVal] <= threshold {
							bestMatch := findClosestCategory(lowVal, freq, threshold)
							if bestMatch != "" && bestMatch != lowVal {
								trackChange("fix_categorical_outliers", val, bestMatch)
								rec[col] = bestMatch
							}
						}
					}
				}
			}

			if drop {
				skipped++
			} else {
				surviving = append(surviving, rec)
				processed++
			}
		}

		// Replace chunk in-place (for drop_null we need to track indices)
		copy(workDS.Records[start:], surviving)
		// If some were dropped, we need to adjust — handled after the loop

		// Progress update
		pct := float64(start+len(chunk)) / float64(totalRecords) * 100
		_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
			TotalItems:     totalRecords,
			ProcessedItems: processed + skipped,
			SkippedItems:   skipped,
			Percentage:     pct,
			CurrentStep:    "cleaning",
			Message:        fmt.Sprintf("Cleaned %d / %d records", processed+skipped, totalRecords),
		})
	}

	// Rebuild record list if any were dropped
	if skipped > 0 {
		kept := make([]dataset.Record, 0, processed)
		for _, rec := range workDS.Records {
			if rec != nil {
				kept = append(kept, rec)
			}
			if int64(len(kept)) >= processed {
				break
			}
		}
		workDS.Records = kept
	}

	// ── Build CleanReport ─────────────────────────
	report := &models.CleanReport{
		TotalRowsDropped: skipped,
		ColumnsDropped:   droppedCols,
	}
	var totalCellsMod int64
	for k, t := range opTrackers {
		totalCellsMod += t.affected
		report.Operations = append(report.Operations, models.CleanOpSummary{
			Operation:     k.operation,
			Column:        k.column,
			CellsAffected: t.affected,
			SampleBefore:  t.sampleBefore,
			SampleAfter:   t.sampleAfter,
		})
	}
	report.TotalCellsModified = totalCellsMod

	// ── Store result ──────────────────────────────
	e.datasetStore.Put(workDS)

	_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
		TotalItems:     totalRecords,
		ProcessedItems: processed,
		SkippedItems:   skipped,
		Percentage:     100,
		CurrentStep:    "completed",
		Message:        fmt.Sprintf("Cleaned dataset '%s': %d kept, %d dropped", workDS.ID, processed, skipped),
	})

	e.logger.Info("ETL clean completed",
		slog.String("job_id", job.ID),
		slog.String("dataset_id", workDS.ID),
		slog.Int64("processed", processed),
		slog.Int64("skipped", skipped),
		slog.Int64("cells_modified", totalCellsMod),
		slog.Duration("elapsed", time.Since(startTime)),
	)

	result := buildCleanResult(workDS.ID, totalRecords, processed, failed, skipped, startTime)
	result.CleanReport = report
	e.reanalyzeDataset(workDS, result)
	return result, nil
}

func buildCleanResult(dsID string, total, processed, failed, skipped int64, start time.Time) *models.ETLJobResult {
	return &models.ETLJobResult{
		DatasetID:    dsID,
		TotalRecords: total,
		Processed:    processed,
		Failed:       failed,
		Skipped:      skipped,
		Duration:     time.Since(start).Round(time.Millisecond).String(),
	}
}

// ──────────────────────────────────────────────
// Cleaning helpers
// ──────────────────────────────────────────────

var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	s = htmlTagRegex.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

func standardizeDate(val, targetFormat string) string {
	if val == "" {
		return val
	}
	if targetFormat == "" {
		targetFormat = "2006-01-02"
	}

	// Try common date formats
	formats := []string{
		"2006-01-02",
		"01/02/2006",
		"02-01-2006",
		"2006/01/02",
		"Jan 2, 2006",
		"January 2, 2006",
		"02 Jan 2006",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		time.RFC3339,
	}

	for _, f := range formats {
		if t, err := time.Parse(f, val); err == nil {
			return t.Format(targetFormat)
		}
	}

	return val // return as-is if unparseable
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// extractNumeric tries to extract a numeric value from a string with noise.
// e.g. "$1,234.56" → "1234.56", "12kg" → "12"
func extractNumeric(s string) string {
	s = strings.TrimSpace(s)
	// Strip common non-numeric characters
	cleaned := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '+' {
			return r
		}
		return -1
	}, s)
	if cleaned == "" || cleaned == "." || cleaned == "-" || cleaned == "+" {
		return ""
	}
	// Validate it's a parseable number
	if _, err := strconv.ParseFloat(cleaned, 64); err == nil {
		return cleaned
	}
	return ""
}

// findClosestCategory finds the most frequent category value that is
// similar to the given rare value (Levenshtein distance ≤ 2).
func findClosestCategory(lowVal string, freq map[string]int, threshold int) string {
	bestMatch := ""
	bestCount := 0

	for candidate, count := range freq {
		if count <= threshold {
			continue // skip other rare values
		}
		// Check string similarity — Levenshtein distance
		dist := levenshteinClean(lowVal, candidate)
		maxLen := len(lowVal)
		if len(candidate) > maxLen {
			maxLen = len(candidate)
		}
		if maxLen == 0 {
			continue
		}
		similarity := 1.0 - float64(dist)/float64(maxLen)
		if similarity >= 0.70 && count > bestCount {
			bestMatch = candidate
			bestCount = count
		}
	}
	return bestMatch
}

// levenshteinClean is a simple Levenshtein edit-distance implementation.
func levenshteinClean(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
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
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev = curr
	}
	return prev[lb]
}

// normalizeTextToUnderscore normalizes text variants to a consistent underscore form.
// "very high" / "veryhigh" / "very_high" / "very-high" / "VeryHigh" → "very_high"
// This handles camelCase, spaces, hyphens, underscores, and concatenated words.
var reNormSeparators = regexp.MustCompile(`[\s\-_]+`)
var reCamelCase = regexp.MustCompile(`([a-z])([A-Z])`)

func normalizeTextToUnderscore(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Split camelCase: "veryHigh" → "very High"
	s = reCamelCase.ReplaceAllString(s, "${1}_${2}")
	// Replace spaces, hyphens, multiple underscores with single underscore
	s = reNormSeparators.ReplaceAllString(s, "_")
	// Lowercase
	s = strings.ToLower(s)
	// Remove leading/trailing underscores
	s = strings.Trim(s, "_")
	return s
}
