package executor

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/madhavbhayani/go-distributed-task-engine/internal/dataset"
	"github.com/madhavbhayani/go-distributed-task-engine/internal/models"
)

// ════════════════════════════════════════════════════════════════
//  ETL Normalize Executor — dual mode
//
//  Mode A:  Value-level transforms (per-cell operations)
//           min_max_scale, z_score, email_normalize, phone_format,
//           date_format, enum_map, url_normalize, to_lowercase,
//           to_uppercase, trim, currency_format, unit_convert
//
//  Mode B:  Database normalization (1NF / 2NF / 3NF)
//           Decomposes a flat table into related lookup tables.
//
//  Both modes can run together — value-level first, then DB decomposition.
// ════════════════════════════════════════════════════════════════

func (e *Engine) executeETLNormalize(ctx context.Context, job *models.Job) (any, error) {
	params, err := toETLNormalizeParams(job.Params)
	if err != nil {
		return nil, fmt.Errorf("invalid normalize params: %w", err)
	}

	ds, err := e.datasetStore.Get(params.DatasetID)
	if err != nil {
		return nil, err
	}

	startTime := time.Now()
	totalRecords := int64(len(ds.Records))

	// Work on a copy so we never mutate the original.
	baseID := params.OutputID
	if baseID == "" {
		baseID = params.DatasetID + "_normalized"
	}
	workDS := ds.Clone(baseID)

	_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
		TotalItems:  totalRecords,
		CurrentStep: "analyzing",
		Message:     "Analyzing data for normalization…",
	})

	report := &models.NormalizeReport{}

	// ═══════════════════════════════════════════════════════════
	//  Phase 1 — Value-level transforms
	// ═══════════════════════════════════════════════════════════
	if len(params.Rules) > 0 {
		vlReport := e.applyValueLevelTransforms(ctx, job, workDS, params.Rules)
		report.ValueLevel = vlReport
	}

	// ═══════════════════════════════════════════════════════════
	//  Phase 2 — Database normalization (1NF / 2NF / 3NF)
	// ═══════════════════════════════════════════════════════════
	var lookupDatasets []*dataset.Dataset

	if params.NormalForm >= 1 {
		report.NormalForm = params.NormalForm
		report.PrimaryKey = params.PrimaryKeyColumn

		// ── 1NF — split multi-valued cells ─────────────────
		e.apply1NF(ctx, job, workDS, report)

		// ── 2NF — extract categorical lookup tables ────────
		if params.NormalForm >= 2 && len(params.CategoricalColumns) > 0 {
			lookupDatasets = e.apply2NF(ctx, job, workDS, params, report, baseID)
		}

		// ── 3NF — extract transitive dependencies ──────────
		if params.NormalForm >= 3 {
			extra := e.apply3NF(ctx, job, workDS, params, report, baseID)
			lookupDatasets = append(lookupDatasets, extra...)
		}

		// Build a nice description
		switch params.NormalForm {
		case 1:
			report.Description = "First Normal Form (1NF): ensured all cells contain atomic values."
		case 2:
			report.Description = fmt.Sprintf("Second Normal Form (2NF): extracted %d lookup table(s) from categorical columns.",
				len(lookupDatasets))
		case 3:
			report.Description = fmt.Sprintf("Third Normal Form (3NF): decomposed into %d total table(s) including transitive dependency extraction.",
				1+len(lookupDatasets))
		}

		// Add the main table to the report
		mainTable := models.DecomposedTable{
			DatasetID:   workDS.ID,
			Name:        "Main Table",
			Columns:     workDS.Columns,
			RecordCount: len(workDS.Records),
			Description: "Primary table" + func() string {
				if len(lookupDatasets) > 0 {
					return " with foreign keys replacing extracted columns"
				}
				return ""
			}(),
			IsMain: true,
		}
		// Prepend main table
		report.Tables = append([]models.DecomposedTable{mainTable}, report.Tables...)
	}

	// ── Store all datasets ─────────────────────────────
	e.datasetStore.Put(workDS)
	for _, ld := range lookupDatasets {
		e.datasetStore.Put(ld)
	}

	processed := int64(len(workDS.Records))

	_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
		TotalItems:     totalRecords,
		ProcessedItems: processed,
		Percentage:     100,
		CurrentStep:    "completed",
		Message:        "Normalization completed",
	})

	e.logger.Info("ETL normalize completed",
		slog.String("job_id", job.ID),
		slog.String("dataset_id", workDS.ID),
		slog.Int("rules", len(params.Rules)),
		slog.Int("normal_form", params.NormalForm),
		slog.Duration("elapsed", time.Since(startTime)),
	)

	result := &models.ETLJobResult{
		DatasetID:       workDS.ID,
		TotalRecords:    totalRecords,
		Processed:       processed,
		Duration:        time.Since(startTime).Round(time.Millisecond).String(),
		NormalizeReport: report,
	}
	e.reanalyzeDataset(workDS, result)
	return result, nil
}

// ════════════════════════════════════════════════════════════════
//  Value-level Transforms
// ════════════════════════════════════════════════════════════════

func (e *Engine) applyValueLevelTransforms(ctx context.Context, job *models.Job, ds *dataset.Dataset, rules []models.NormalizationRule) *models.ValueLevelReport {
	_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
		CurrentStep: "value_transforms",
		Message:     fmt.Sprintf("Applying %d value-level normalization rules…", len(rules)),
	})

	// ── Pass 1: compute column statistics needed by z_score / min_max_scale ──
	type colStats struct {
		min, max, mean, stddev float64
		computed               bool
	}
	stats := map[string]*colStats{}

	for _, rule := range rules {
		if rule.Operation == "min_max_scale" || rule.Operation == "z_score" {
			if _, exists := stats[rule.Column]; !exists {
				s := &colStats{}
				var vals []float64
				for _, rec := range ds.Records {
					v := stripNumericSymbols(rec[rule.Column])
					if f, err := strconv.ParseFloat(v, 64); err == nil {
						vals = append(vals, f)
					}
				}
				if len(vals) > 0 {
					mn, mx := vals[0], vals[0]
					sum := 0.0
					for _, v := range vals {
						sum += v
						if v < mn {
							mn = v
						}
						if v > mx {
							mx = v
						}
					}
					s.mean = sum / float64(len(vals))
					s.min = mn
					s.max = mx
					// standard deviation
					sumSqDiff := 0.0
					for _, v := range vals {
						d := v - s.mean
						sumSqDiff += d * d
					}
					s.stddev = math.Sqrt(sumSqDiff / float64(len(vals)))
					s.computed = true
				}
				stats[rule.Column] = s
			}
		}
	}

	// ── Pass 1b: compute enum mappings for enum_map(auto) ──
	enumMaps := map[string]map[string]string{} // col → (originalLower → canonical)
	for _, rule := range rules {
		if rule.Operation == "enum_map" && rule.Params["auto"] == "true" {
			if _, exists := enumMaps[rule.Column]; !exists {
				groups := map[string]map[string]int{} // lowercase → (original → count)
				for _, rec := range ds.Records {
					v := rec[rule.Column]
					if v == "" {
						continue
					}
					low := strings.ToLower(strings.TrimSpace(v))
					if groups[low] == nil {
						groups[low] = map[string]int{}
					}
					groups[low][v]++
				}
				mapping := map[string]string{}
				for _, variants := range groups {
					// Pick the form with the highest count as canonical
					best := ""
					bestN := 0
					for form, n := range variants {
						if n > bestN || (n == bestN && form < best) {
							best = form
							bestN = n
						}
					}
					for form := range variants {
						if form != best {
							mapping[form] = best
						}
					}
				}
				enumMaps[rule.Column] = mapping
			}
		}
	}

	// ── Pass 2: apply transforms ──
	var ops []models.NormOpSummary
	var totalCells int64

	for _, rule := range rules {
		summary := models.NormOpSummary{
			Operation: rule.Operation,
			Column:    rule.Column,
		}
		var affected int64
		var sampleBefore, sampleAfter []string
		const maxSamples = 5
		captureSample := func(before, after string) {
			if before != after && len(sampleBefore) < maxSamples {
				sampleBefore = append(sampleBefore, before)
				sampleAfter = append(sampleAfter, after)
			}
		}

		for i := range ds.Records {
			rec := ds.Records[i]
			col := rule.Column
			old := rec[col]
			newVal := old

			switch rule.Operation {
			case "min_max_scale":
				s := stats[col]
				if s != nil && s.computed && s.max != s.min {
					v := stripNumericSymbols(old)
					if f, err := strconv.ParseFloat(v, 64); err == nil {
						scaled := (f - s.min) / (s.max - s.min)
						newVal = strconv.FormatFloat(scaled, 'f', 6, 64)
					}
				}

			case "z_score":
				s := stats[col]
				if s != nil && s.computed && s.stddev > 0 {
					v := stripNumericSymbols(old)
					if f, err := strconv.ParseFloat(v, 64); err == nil {
						z := (f - s.mean) / s.stddev
						newVal = strconv.FormatFloat(z, 'f', 6, 64)
					}
				}

			case "email_normalize":
				newVal = strings.ToLower(strings.TrimSpace(old))

			case "phone_format":
				newVal = normalizePhone(old)

			case "date_format":
				targetFmt := rule.Params["target_format"]
				if targetFmt == "" {
					targetFmt = "2006-01-02"
				}
				newVal = normalizeDate(old, targetFmt)

			case "enum_map":
				if rule.Params["auto"] == "true" {
					if m, ok := enumMaps[col]; ok {
						if canonical, found := m[old]; found {
							newVal = canonical
						}
					}
				} else {
					// Manual mapping from params
					if mapped, ok := rule.Params[old]; ok {
						newVal = mapped
					}
				}

			case "url_normalize":
				newVal = normalizeURL(old)

			case "to_lowercase":
				newVal = strings.ToLower(old)

			case "to_uppercase":
				newVal = strings.ToUpper(old)

			case "trim":
				newVal = strings.TrimSpace(old)

			case "currency_format":
				newVal = normalizeCurrency(old)

			case "unit_convert":
				newVal = normalizeUnit(old, rule.Params)
			}

			if newVal != old {
				ds.Records[i][col] = newVal
				affected++
				captureSample(old, newVal)
			}
		}

		summary.CellsAffected = affected
		summary.SampleBefore = sampleBefore
		summary.SampleAfter = sampleAfter
		totalCells += affected
		ops = append(ops, summary)
	}

	return &models.ValueLevelReport{
		TotalCellsModified: totalCells,
		Operations:         ops,
	}
}

// ════════════════════════════════════════════════════════════════
//  Value-level transform helpers
// ════════════════════════════════════════════════════════════════

var reNonDigit = regexp.MustCompile(`[^\d.+-]`)

// stripNumericSymbols removes $ , € £ etc from a value for numeric parsing.
func stripNumericSymbols(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, "€", "")
	s = strings.ReplaceAll(s, "£", "")
	s = strings.ReplaceAll(s, "¥", "")
	return s
}

// normalizePhone keeps only digits and leading +.
func normalizePhone(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if r == '+' && i == 0 {
			b.WriteRune(r)
		} else if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	result := b.String()
	if result == "" {
		return s // return original if nothing left
	}
	return result
}

// normalizeDate tries common date formats and reformats to target.
var dateFormats = []string{
	"2006-01-02", "01/02/2006", "02/01/2006", "2006/01/02",
	"Jan 2, 2006", "January 2, 2006", "2 Jan 2006",
	"02-01-2006", "01-02-2006",
	"2006-01-02T15:04:05Z07:00", // ISO 8601
	"2006-01-02 15:04:05",
	"Mon, 02 Jan 2006", "02 January 2006",
}

func normalizeDate(s, targetFmt string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	for _, fmt := range dateFormats {
		if t, err := time.Parse(fmt, s); err == nil {
			return t.Format(targetFmt)
		}
	}
	return s // return original if no format matched
}

// normalizeURL lowercases scheme+host and strips trailing slash.
func normalizeURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	result := u.String()
	result = strings.TrimRight(result, "/")
	return result
}

// normalizeCurrency strips currency symbols and formats as plain decimal.
func normalizeCurrency(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	cleaned := stripNumericSymbols(s)
	if f, err := strconv.ParseFloat(cleaned, 64); err == nil {
		return strconv.FormatFloat(f, 'f', 2, 64)
	}
	return s
}

// normalizeUnit does basic unit conversion via params (from_unit, to_unit).
func normalizeUnit(s string, params map[string]string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	fromUnit := strings.ToLower(params["from_unit"])
	toUnit := strings.ToLower(params["to_unit"])
	cleaned := stripNumericSymbols(s)
	f, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return s
	}

	// Common conversions
	var result float64
	converted := true
	switch {
	case fromUnit == "miles" && toUnit == "km":
		result = f * 1.60934
	case fromUnit == "km" && toUnit == "miles":
		result = f / 1.60934
	case fromUnit == "lbs" && toUnit == "kg":
		result = f * 0.453592
	case fromUnit == "kg" && toUnit == "lbs":
		result = f / 0.453592
	case fromUnit == "fahrenheit" && toUnit == "celsius":
		result = (f - 32) * 5 / 9
	case fromUnit == "celsius" && toUnit == "fahrenheit":
		result = f*9/5 + 32
	case fromUnit == "inches" && toUnit == "cm":
		result = f * 2.54
	case fromUnit == "cm" && toUnit == "inches":
		result = f / 2.54
	case fromUnit == "gallons" && toUnit == "liters":
		result = f * 3.78541
	case fromUnit == "liters" && toUnit == "gallons":
		result = f / 3.78541
	default:
		converted = false
	}

	if converted {
		return strconv.FormatFloat(result, 'f', 4, 64)
	}
	return s
}

// ════════════════════════════════════════════════════════════════
//  1NF — Atomic Values
// ════════════════════════════════════════════════════════════════

func (e *Engine) apply1NF(ctx context.Context, job *models.Job, ds *dataset.Dataset, report *models.NormalizeReport) {
	// Detect columns with multi-valued cells (semicolon / pipe separated).
	type splitInfo struct {
		col          string
		cellsSplit   int
		sampleBefore string
		sampleAfter  string
	}

	var splits []splitInfo
	separators := []string{";", "|"} // Not comma — too ambiguous in CSV context

	for _, col := range ds.Columns {
		info := splitInfo{col: col}
		for _, rec := range ds.Records {
			val := rec[col]
			for _, sep := range separators {
				if strings.Contains(val, sep) {
					parts := strings.Split(val, sep)
					allEmpty := true
					for _, p := range parts {
						if strings.TrimSpace(p) != "" {
							allEmpty = false
							break
						}
					}
					if !allEmpty && len(parts) >= 2 {
						info.cellsSplit++
						if info.sampleBefore == "" {
							info.sampleBefore = val
							trimmed := make([]string, 0, len(parts))
							for _, p := range parts {
								t := strings.TrimSpace(p)
								if t != "" {
									trimmed = append(trimmed, t)
								}
							}
							info.sampleAfter = strings.Join(trimmed, ", ")
						}
					}
					break // first matching separator wins
				}
			}
		}
		// Only include if ≥5% of cells are multi-valued
		threshold := len(ds.Records) * 5 / 100
		if threshold < 1 {
			threshold = 1
		}
		if info.cellsSplit >= threshold {
			splits = append(splits, info)
		}
	}

	if len(splits) == 0 {
		return // Nothing to split — data is already 1NF
	}

	// For each multi-valued column, expand rows
	rowsBefore := len(ds.Records)
	for _, sp := range splits {
		var newRecords []dataset.Record
		for _, rec := range ds.Records {
			val := rec[sp.col]
			// Find which separator is used
			var sep string
			for _, s := range separators {
				if strings.Contains(val, s) {
					sep = s
					break
				}
			}
			if sep == "" {
				newRecords = append(newRecords, rec)
				continue
			}
			parts := strings.Split(val, sep)
			trimmed := make([]string, 0, len(parts))
			for _, p := range parts {
				t := strings.TrimSpace(p)
				if t != "" {
					trimmed = append(trimmed, t)
				}
			}
			if len(trimmed) <= 1 {
				newRecords = append(newRecords, rec)
				continue
			}
			// Create one row per atomic value
			for _, atom := range trimmed {
				newRec := make(dataset.Record, len(rec))
				for k, v := range rec {
					newRec[k] = v
				}
				newRec[sp.col] = atom
				newRecords = append(newRecords, newRec)
			}
		}
		ds.Records = newRecords
	}
	rowsAfter := len(ds.Records)

	for _, sp := range splits {
		report.MultiValueSplits = append(report.MultiValueSplits, models.MultiValueSplit{
			Column:       sp.col,
			CellsSplit:   sp.cellsSplit,
			RowsBefore:   rowsBefore,
			RowsAfter:    rowsAfter,
			SampleBefore: sp.sampleBefore,
			SampleAfter:  sp.sampleAfter,
		})
	}
}

// ════════════════════════════════════════════════════════════════
//  2NF — Extract Categorical Lookups
//
//  For each categorical column, we:
//    1. Build a lookup table of unique values → integer ID
//    2. Replace the categorical value in the main table with the ID
//    3. Store the lookup as a separate Dataset
// ════════════════════════════════════════════════════════════════

func (e *Engine) apply2NF(ctx context.Context, job *models.Job, ds *dataset.Dataset, params *models.ETLNormalizeParams, report *models.NormalizeReport, baseID string) []*dataset.Dataset {
	var lookups []*dataset.Dataset

	for _, catCol := range params.CategoricalColumns {
		// Verify column exists
		found := false
		for _, c := range ds.Columns {
			if c == catCol {
				found = true
				break
			}
		}
		if !found {
			continue
		}

		// Find dependent columns: columns whose value is always the same
		// for a given value of catCol (functional dependency: catCol → depCol).
		depCols := findDependentColumns(ds, catCol, params.PrimaryKeyColumn)

		// Build unique value list for the categorical column + dependents
		lookupCols := []string{catCol + "_id", catCol}
		lookupCols = append(lookupCols, depCols...)

		uniqueVals := make(map[string]int)            // catVal → id
		uniqueRows := make(map[string]dataset.Record) // catVal → full row
		nextID := 1

		for _, rec := range ds.Records {
			catVal := rec[catCol]
			if _, exists := uniqueVals[catVal]; !exists {
				uniqueVals[catVal] = nextID
				row := dataset.Record{
					catCol + "_id": strconv.Itoa(nextID),
					catCol:         catVal,
				}
				for _, dep := range depCols {
					row[dep] = rec[dep]
				}
				uniqueRows[catVal] = row
				nextID++
			}
		}

		// Create lookup Dataset
		lookupID := baseID + "_" + sanitizeID(catCol)
		type kv struct {
			val string
			id  int
		}
		sorted := make([]kv, 0, len(uniqueVals))
		for v, id := range uniqueVals {
			sorted = append(sorted, kv{v, id})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].id < sorted[j].id })

		lookupRecords := make([]dataset.Record, 0, len(sorted))
		for _, s := range sorted {
			lookupRecords = append(lookupRecords, uniqueRows[s.val])
		}

		lookupDS := &dataset.Dataset{
			ID:      lookupID,
			Columns: lookupCols,
			Records: lookupRecords,
		}
		lookups = append(lookups, lookupDS)

		// Replace catCol in main table with catCol_id (FK)
		fkCol := catCol + "_id"
		for i, rec := range ds.Records {
			catVal := rec[catCol]
			ds.Records[i][fkCol] = strconv.Itoa(uniqueVals[catVal])
			delete(ds.Records[i], catCol)
			for _, dep := range depCols {
				delete(ds.Records[i], dep)
			}
		}

		// Update main table columns
		newCols := []string{}
		replaced := false
		for _, c := range ds.Columns {
			if c == catCol {
				if !replaced {
					newCols = append(newCols, fkCol)
					replaced = true
				}
				continue
			}
			isDep := false
			for _, dep := range depCols {
				if c == dep {
					isDep = true
					break
				}
			}
			if isDep {
				continue
			}
			newCols = append(newCols, c)
		}
		ds.Columns = newCols

		// Add to report
		report.Tables = append(report.Tables, models.DecomposedTable{
			DatasetID:   lookupID,
			Name:        catCol + " (lookup)",
			Columns:     lookupCols,
			RecordCount: len(lookupRecords),
			Description: fmt.Sprintf("Lookup table for '%s' — %d unique values", catCol, len(uniqueVals)),
		})
		report.Relationships = append(report.Relationships, models.TableRelationship{
			FromTable:  baseID,
			FromColumn: fkCol,
			ToTable:    lookupID,
			ToColumn:   fkCol,
			Type:       "many-to-one",
		})

		_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
			CurrentStep: "normalizing",
			Message:     fmt.Sprintf("Extracted lookup table for '%s' (%d unique values)", catCol, len(uniqueVals)),
		})
	}

	return lookups
}

// ════════════════════════════════════════════════════════════════
//  3NF — Extract Transitive Dependencies
//
//  After 2NF, scan remaining non-key columns for transitive
//  dependencies (A → B where A is not the PK). If found,
//  extract into a separate table.
// ════════════════════════════════════════════════════════════════

func (e *Engine) apply3NF(ctx context.Context, job *models.Job, ds *dataset.Dataset, params *models.ETLNormalizeParams, report *models.NormalizeReport, baseID string) []*dataset.Dataset {
	var extra []*dataset.Dataset
	pk := params.PrimaryKeyColumn

	// Get non-key, non-FK columns that remain
	nonKeyCols := []string{}
	for _, c := range ds.Columns {
		if c == pk || strings.HasSuffix(c, "_id") {
			continue
		}
		nonKeyCols = append(nonKeyCols, c)
	}

	if len(nonKeyCols) < 2 {
		return nil
	}

	// For each pair (A, B) where A → B, check if A functionally determines B
	// and A is not the PK.
	type transitiveDep struct {
		determinant string
		dependents  []string
	}

	found := map[string]*transitiveDep{}
	alreadyExtracted := map[string]bool{}

	for _, colA := range nonKeyCols {
		if alreadyExtracted[colA] {
			continue
		}
		deps := []string{}
		for _, colB := range nonKeyCols {
			if colB == colA || alreadyExtracted[colB] {
				continue
			}
			if isFunctionalDependency(ds, colA, colB) {
				deps = append(deps, colB)
			}
		}
		if len(deps) > 0 {
			// Only extract if colA has reasonable cardinality (not unique per row)
			uniqueA := countUnique(ds, colA)
			if uniqueA < len(ds.Records)*80/100 {
				found[colA] = &transitiveDep{determinant: colA, dependents: deps}
				for _, d := range deps {
					alreadyExtracted[d] = true
				}
				alreadyExtracted[colA] = true
			}
		}
	}

	for determinant, td := range found {
		lookupCols := []string{determinant + "_id", determinant}
		lookupCols = append(lookupCols, td.dependents...)

		uniqueVals := map[string]int{}
		uniqueRows := map[string]dataset.Record{}
		nextID := 1

		for _, rec := range ds.Records {
			detVal := rec[determinant]
			if _, exists := uniqueVals[detVal]; !exists {
				uniqueVals[detVal] = nextID
				row := dataset.Record{
					determinant + "_id": strconv.Itoa(nextID),
					determinant:         detVal,
				}
				for _, dep := range td.dependents {
					row[dep] = rec[dep]
				}
				uniqueRows[detVal] = row
				nextID++
			}
		}

		lookupID := baseID + "_" + sanitizeID(determinant)
		type kv struct {
			val string
			id  int
		}
		sorted := make([]kv, 0, len(uniqueVals))
		for v, id := range uniqueVals {
			sorted = append(sorted, kv{v, id})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].id < sorted[j].id })

		lookupRecords := make([]dataset.Record, 0, len(sorted))
		for _, s := range sorted {
			lookupRecords = append(lookupRecords, uniqueRows[s.val])
		}

		lookupDS := &dataset.Dataset{
			ID:      lookupID,
			Columns: lookupCols,
			Records: lookupRecords,
		}
		extra = append(extra, lookupDS)

		// Replace in main table
		fkCol := determinant + "_id"
		for i, rec := range ds.Records {
			detVal := rec[determinant]
			ds.Records[i][fkCol] = strconv.Itoa(uniqueVals[detVal])
			delete(ds.Records[i], determinant)
			for _, dep := range td.dependents {
				delete(ds.Records[i], dep)
			}
		}

		// Update columns
		newCols := []string{}
		replaced := false
		for _, c := range ds.Columns {
			if c == determinant {
				if !replaced {
					newCols = append(newCols, fkCol)
					replaced = true
				}
				continue
			}
			isDep := false
			for _, dep := range td.dependents {
				if c == dep {
					isDep = true
					break
				}
			}
			if isDep {
				continue
			}
			newCols = append(newCols, c)
		}
		ds.Columns = newCols

		report.Tables = append(report.Tables, models.DecomposedTable{
			DatasetID:   lookupID,
			Name:        determinant + " (3NF lookup)",
			Columns:     lookupCols,
			RecordCount: len(lookupRecords),
			Description: fmt.Sprintf("Transitive dependency: '%s' determines %v", determinant, td.dependents),
		})
		report.Relationships = append(report.Relationships, models.TableRelationship{
			FromTable:  baseID,
			FromColumn: fkCol,
			ToTable:    lookupID,
			ToColumn:   fkCol,
			Type:       "many-to-one",
		})

		_ = e.jobStore.UpdateProgress(job.ID, models.Progress{
			CurrentStep: "normalizing",
			Message:     fmt.Sprintf("3NF: extracted '%s' → %v into lookup table", determinant, td.dependents),
		})
	}

	return extra
}

// ════════════════════════════════════════════════════════════════
//  Helpers
// ════════════════════════════════════════════════════════════════

// findDependentColumns finds columns that are functionally dependent
// on the given categorical column (catCol → depCol), excluding the PK.
func findDependentColumns(ds *dataset.Dataset, catCol, pkCol string) []string {
	var deps []string
	for _, col := range ds.Columns {
		if col == catCol || col == pkCol {
			continue
		}
		if isFunctionalDependency(ds, catCol, col) {
			deps = append(deps, col)
		}
	}
	return deps
}

// isFunctionalDependency checks if A → B (every distinct value of A
// maps to exactly one distinct value of B).
func isFunctionalDependency(ds *dataset.Dataset, colA, colB string) bool {
	mapping := map[string]string{}
	for _, rec := range ds.Records {
		a := rec[colA]
		b := rec[colB]
		if a == "" {
			continue
		}
		if prev, exists := mapping[a]; exists {
			if prev != b {
				return false
			}
		} else {
			mapping[a] = b
		}
	}
	return len(mapping) > 0
}

// countUnique counts distinct non-empty values in a column.
func countUnique(ds *dataset.Dataset, col string) int {
	seen := map[string]bool{}
	for _, rec := range ds.Records {
		v := rec[col]
		if v != "" {
			seen[v] = true
		}
	}
	return len(seen)
}

// sanitizeID makes a string safe for use in dataset IDs.
func sanitizeID(s string) string {
	r := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ".", "_")
	return strings.ToLower(r.Replace(s))
}
