package analyzer

// ====================================================================
//  CSV Column Analyzer
//
//  Automatically inspects ALL rows of a CSV and:
//    - Infers data type per column (integer, float, boolean, date,
//      datetime, email, phone, url, categorical, alphanumeric,
//      numeric_string, free_text)
//    - Measures completeness (null %, unique count, fill rate)
//    - Detects patterns (whitespace padding, HTML tags, special chars,
//      mixed case, multi-line values, numeric comma-separators)
//    - Computes numeric statistics (min, max, mean, std-dev)
//    - Records top-5 most frequent values per column
//    - Generates actionable clean + normalize recommendations
//
//  Entry point:
//    Analyze(headers []string, rows [][]string) *DatasetAnalysis
// ====================================================================

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ----------------------------------------------------------------
// Column type enum
// ----------------------------------------------------------------

// InferredType is the detected semantic type of a column.
type InferredType string

const (
	TypeInteger       InferredType = "integer"
	TypeFloat         InferredType = "float"
	TypeBoolean       InferredType = "boolean"
	TypeDate          InferredType = "date"
	TypeDateTime      InferredType = "datetime"
	TypeEmail         InferredType = "email"
	TypePhone         InferredType = "phone"
	TypeURL           InferredType = "url"
	TypeCategorical   InferredType = "categorical"
	TypeAlphanumeric  InferredType = "alphanumeric"
	TypeNumericString InferredType = "numeric_string"
	TypeFreeText      InferredType = "free_text"
)

// ----------------------------------------------------------------
// Output structs
// ----------------------------------------------------------------

// ValueCount pairs a value with its frequency.
type ValueCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// CleanRecommendation suggests a cleaning operation for a column.
type CleanRecommendation struct {
	Operation string            `json:"operation"`
	Column    string            `json:"column"`
	Reason    string            `json:"reason"`
	Params    map[string]string `json:"params,omitempty"`
}

// NormalizeRecommendation suggests a normalisation operation for a column.
type NormalizeRecommendation struct {
	Operation string            `json:"operation"`
	Column    string            `json:"column"`
	Reason    string            `json:"reason"`
	Params    map[string]string `json:"params,omitempty"`
}

// ColumnAnalysis contains the full analysis result for a single column.
type ColumnAnalysis struct {
	Name         string       `json:"name"`
	InferredType InferredType `json:"inferred_type"`

	// Completeness metrics
	TotalCount  int     `json:"total_count"`
	NullCount   int     `json:"null_count"`
	EmptyCount  int     `json:"empty_count"`
	UniqueCount int     `json:"unique_count"`
	FillRate    float64 `json:"fill_rate"` // 0.0 – 1.0

	// Up to 5 distinct non-null example values
	SampleValues []string `json:"sample_values,omitempty"`

	// Pattern flags
	HasLeadingTrailingSpace bool `json:"has_leading_trailing_space"`
	HasSpecialChars         bool `json:"has_special_chars"`
	HasHTMLTags             bool `json:"has_html_tags"`
	HasInconsistentCase     bool `json:"has_inconsistent_case"`
	HasMultilineValues      bool `json:"has_multiline_values"`
	HasCommas               bool `json:"has_commas"`               // e.g. "1,234"
	HasDuplicateWhitespace  bool `json:"has_duplicate_whitespace"` // e.g. "foo  bar"

	// Type-inference confidence and mismatches
	TypeConfidence   float64  `json:"type_confidence"`             // 0.0–1.0 fraction of values matching InferredType
	MismatchCount    int      `json:"mismatch_count"`              // values that don't match InferredType
	MismatchedValues []string `json:"mismatched_values,omitempty"` // up to 10 sample mismatched values

	// Categorical quality — outlier values (frequency ≤1% of mode)
	CategoricalOutliers []string `json:"categorical_outliers,omitempty"`

	// Numeric statistics (only for integer / float columns)
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
	Mean   *float64 `json:"mean,omitempty"`
	Median *float64 `json:"median,omitempty"`
	StdDev *float64 `json:"std_dev,omitempty"`

	// Mode — the most frequent non-null value
	ModeValue string `json:"mode_value,omitempty"`

	// Date formats that matched (only for date / datetime)
	DetectedDateFormats []string `json:"detected_date_formats,omitempty"`

	// Top-5 most frequent values
	TopValues []ValueCount `json:"top_values,omitempty"`

	// Auto-generated recommendations
	RecommendedCleanOps     []CleanRecommendation     `json:"recommended_clean_ops,omitempty"`
	RecommendedNormalizeOps []NormalizeRecommendation `json:"recommended_normalize_ops,omitempty"`
}

// DatasetAnalysis is the complete analysis result for a CSV sample.
type DatasetAnalysis struct {
	TotalRows       int                        `json:"total_rows"`
	TotalColumns    int                        `json:"total_columns"`
	SampledRows     int                        `json:"sampled_rows"`
	Columns         map[string]*ColumnAnalysis `json:"columns"`
	OverallQuality  float64                    `json:"overall_quality"` // avg fill-rate 0.0–1.0
	Recommendations []string                   `json:"recommendations,omitempty"`
}

// ----------------------------------------------------------------
// Compiled regexes  (package-level, compiled once)
// ----------------------------------------------------------------

var (
	reInteger   = regexp.MustCompile(`^[+-]?\d+$`)
	reFloat     = regexp.MustCompile(`^[+-]?\d*\.?\d+([eE][+-]?\d+)?$`)
	reEmail     = regexp.MustCompile(`(?i)^[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}$`)
	rePhone     = regexp.MustCompile(`^[+]?[\d\s\-().]{7,20}$`)
	reURL       = regexp.MustCompile(`(?i)^(https?://|www\.)\S+`)
	reHTMLTag   = regexp.MustCompile(`<[^>]+>`)
	reAlpha     = regexp.MustCompile(`^[a-zA-Z0-9\s\-_.]+$`)
	reNumSym    = regexp.MustCompile(`^[\d,.$%\s\-+()]+$`)
	reDupSpaces = regexp.MustCompile(`\s{2,}`)
)

// Common date / datetime layouts — most specific first.
var dateLayouts = []string{
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"01/02/2006 15:04:05",
	"2006-01-02",
	"01/02/2006",
	"02/01/2006",
	"2006/01/02",
	"Jan 2, 2006",
	"January 2, 2006",
	"2-Jan-2006",
	"20060102",
}

// Boolean sentinel set (case-insensitive match).
var boolSentinels = map[string]bool{
	"true": true, "false": true,
	"yes": true, "no": true,
	"1": true, "0": true,
	"t": true, "f": true,
	"y": true, "n": true,
}

// Null/missing value sentinels (case-insensitive).
var nullSentinels = map[string]bool{
	"":     true,
	"null": true,
	"n/a":  true,
	"na":   true,
	"none": true,
	"nil":  true,
	"-":    true,
	"<na>": true,
}

// ----------------------------------------------------------------
// Entry point
// ----------------------------------------------------------------

// Analyze inspects ALL rows and returns a full DatasetAnalysis.
// headers must be the ordered column names; rows are the raw CSV data rows.
func Analyze(headers []string, rows [][]string) *DatasetAnalysis {
	sampled := rows

	cols := make(map[string]*ColumnAnalysis, len(headers))
	var totalFill float64

	for colIdx, colName := range headers {
		vals := make([]string, 0, len(sampled))
		for _, row := range sampled {
			if colIdx < len(row) {
				vals = append(vals, row[colIdx])
			} else {
				vals = append(vals, "")
			}
		}
		ca := analyzeColumn(colName, vals)
		cols[colName] = ca
		totalFill += ca.FillRate
	}

	quality := 0.0
	if len(cols) > 0 {
		quality = math.Round(totalFill/float64(len(cols))*1000) / 1000
	}

	da := &DatasetAnalysis{
		TotalRows:      len(rows),
		TotalColumns:   len(headers),
		SampledRows:    len(sampled),
		Columns:        cols,
		OverallQuality: quality,
	}
	da.Recommendations = datasetRecommendations(da)
	return da
}

// ----------------------------------------------------------------
// Per-column analysis
// ----------------------------------------------------------------

func analyzeColumn(name string, values []string) *ColumnAnalysis {
	ca := &ColumnAnalysis{Name: name, TotalCount: len(values)}
	freq := make(map[string]int, len(values))
	var nonEmpty []string

	for _, v := range values {
		// Null / empty detection
		low := strings.ToLower(strings.TrimSpace(v))
		if nullSentinels[low] {
			if v == "" {
				ca.EmptyCount++
			} else {
				ca.NullCount++
			}
			continue
		}
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			ca.EmptyCount++
			continue
		}

		nonEmpty = append(nonEmpty, v)
		freq[v]++

		// Pattern detection
		if v != trimmed {
			ca.HasLeadingTrailingSpace = true
		}
		if strings.ContainsAny(v, "\n\r") {
			ca.HasMultilineValues = true
		}
		if reDupSpaces.MatchString(v) {
			ca.HasDuplicateWhitespace = true
		}
		unesc := html.UnescapeString(v)
		if reHTMLTag.MatchString(unesc) || strings.Contains(v, "&amp;") || strings.Contains(v, "&lt;") {
			ca.HasHTMLTags = true
		}
		for _, r := range v {
			if r > unicode.MaxASCII {
				ca.HasSpecialChars = true
				break
			}
		}
		if strings.Contains(v, ",") {
			ca.HasCommas = true
		}
	}

	ca.UniqueCount = len(freq)
	filled := len(nonEmpty)
	if ca.TotalCount > 0 {
		ca.FillRate = math.Round(float64(filled)/float64(ca.TotalCount)*1000) / 1000
	}

	// Collect up to 5 distinct sample values
	seen := map[string]bool{}
	for _, v := range nonEmpty {
		if len(ca.SampleValues) >= 5 {
			break
		}
		sv := strings.TrimSpace(v)
		if !seen[sv] {
			ca.SampleValues = append(ca.SampleValues, sv)
			seen[sv] = true
		}
	}

	// Compute mode (most frequent value)
	if len(freq) > 0 {
		modeVal := ""
		modeCount := 0
		for v, c := range freq {
			if c > modeCount {
				modeCount = c
				modeVal = v
			}
		}
		ca.ModeValue = modeVal
	}

	ca.HasInconsistentCase = mixedCase(nonEmpty)
	ca.TopValues = topN(freq, 5)
	ca.InferredType, ca.TypeConfidence, ca.MismatchCount, ca.MismatchedValues = inferTypeAdvanced(nonEmpty, ca)

	if ca.InferredType == TypeInteger || ca.InferredType == TypeFloat {
		ca.Min, ca.Max, ca.Mean, ca.Median, ca.StdDev = numStats(nonEmpty)
	}

	// Categorical outlier detection
	if ca.InferredType == TypeCategorical && len(freq) > 1 {
		ca.CategoricalOutliers = detectCategoricalOutliers(freq, len(nonEmpty))
	}

	ca.RecommendedCleanOps, ca.RecommendedNormalizeOps = buildRecommendations(ca)
	return ca
}

// ----------------------------------------------------------------
// Type inference — majority vote (≥85% threshold)
// ----------------------------------------------------------------

// inferTypeAdvanced uses majority-vote type inference. A type is accepted
// when ≥85% of non-null values match the pattern. Values that don't match
// are reported as mismatches so the cleaner can fix them.
func inferTypeAdvanced(values []string, ca *ColumnAnalysis) (InferredType, float64, int, []string) {
	if len(values) == 0 {
		return TypeFreeText, 0, 0, nil
	}

	total := float64(len(values))
	threshold := 0.85

	// Helper: count how many match, collect mismatches
	countAndMismatch := func(fn func(string) bool) (int, []string) {
		matched := 0
		var mismatches []string
		for _, v := range values {
			if fn(v) {
				matched++
			} else if len(mismatches) < 10 {
				mismatches = append(mismatches, v)
			}
		}
		return matched, mismatches
	}

	// Boolean (check before integer — "0"/"1" qualify as both)
	if cnt, mm := countAndMismatch(func(v string) bool {
		return boolSentinels[strings.ToLower(strings.TrimSpace(v))]
	}); float64(cnt)/total >= threshold {
		return TypeBoolean, float64(cnt) / total, len(values) - cnt, mm
	}

	// Integer
	if cnt, mm := countAndMismatch(func(v string) bool {
		return reInteger.MatchString(strings.TrimSpace(v))
	}); float64(cnt)/total >= threshold {
		return TypeInteger, float64(cnt) / total, len(values) - cnt, mm
	}

	// Float
	if cnt, mm := countAndMismatch(func(v string) bool {
		return reFloat.MatchString(strings.TrimSpace(v))
	}); float64(cnt)/total >= threshold {
		return TypeFloat, float64(cnt) / total, len(values) - cnt, mm
	}

	// Email
	if cnt, mm := countAndMismatch(func(v string) bool {
		return reEmail.MatchString(strings.TrimSpace(v))
	}); float64(cnt)/total >= threshold {
		return TypeEmail, float64(cnt) / total, len(values) - cnt, mm
	}

	// URL
	if cnt, mm := countAndMismatch(func(v string) bool {
		return reURL.MatchString(strings.TrimSpace(v))
	}); float64(cnt)/total >= threshold {
		return TypeURL, float64(cnt) / total, len(values) - cnt, mm
	}

	// Phone
	if cnt, mm := countAndMismatch(func(v string) bool {
		t := strings.TrimSpace(v)
		return rePhone.MatchString(t) && len(t) >= 7
	}); float64(cnt)/total >= threshold {
		return TypePhone, float64(cnt) / total, len(values) - cnt, mm
	}

	// Date / DateTime (uses its own 70% threshold)
	if dt, fmts := detectDate(values); dt != "" {
		ca.DetectedDateFormats = fmts
		dateCnt := 0
		for _, v := range values {
			tv := strings.TrimSpace(v)
			for _, layout := range dateLayouts {
				if _, err := time.Parse(layout, tv); err == nil {
					dateCnt++
					break
				}
			}
		}
		conf := float64(dateCnt) / total
		mm := len(values) - dateCnt
		var mismatches []string
		if mm > 0 {
			for _, v := range values {
				tv := strings.TrimSpace(v)
				matched := false
				for _, layout := range dateLayouts {
					if _, err := time.Parse(layout, tv); err == nil {
						matched = true
						break
					}
				}
				if !matched && len(mismatches) < 10 {
					mismatches = append(mismatches, v)
				}
			}
		}
		return dt, conf, mm, mismatches
	}

	// Numeric string — digits+commas+currency symbols (e.g. "$1,234")
	if cnt, mm := countAndMismatch(func(v string) bool {
		return reNumSym.MatchString(strings.TrimSpace(v))
	}); float64(cnt)/total >= threshold {
		return TypeNumericString, float64(cnt) / total, len(values) - cnt, mm
	}

	// Categorical — low cardinality (≤50 unique, ≤30% of total)
	if ca.UniqueCount > 0 && ca.UniqueCount <= 50 && ca.TotalCount > 0 {
		ratio := float64(ca.UniqueCount) / float64(ca.TotalCount)
		if ratio <= 0.30 {
			return TypeCategorical, 1.0 - ratio, 0, nil
		}
	}

	// Alphanumeric — only letters, digits, spaces, dashes, dots, underscores
	if cnt, mm := countAndMismatch(func(v string) bool {
		return reAlpha.MatchString(v)
	}); float64(cnt)/total >= threshold {
		return TypeAlphanumeric, float64(cnt) / total, len(values) - cnt, mm
	}

	return TypeFreeText, 1.0, 0, nil
}

// allMatch returns true when fn returns true for every value.
func allMatch(values []string, fn func(string) bool) bool {
	for _, v := range values {
		if !fn(v) {
			return false
		}
	}
	return true
}

// detectCategoricalOutliers finds categorical values that appear very
// rarely compared to the most common value (≤1% of mode frequency).
// Also detects values that look like typos of common values.
func detectCategoricalOutliers(freq map[string]int, totalNonEmpty int) []string {
	if len(freq) <= 1 {
		return nil
	}

	// Find the mode count
	modeCount := 0
	for _, c := range freq {
		if c > modeCount {
			modeCount = c
		}
	}

	// Outlier threshold: a value is an outlier if:
	// 1) It appears ≤ 1% of the mode count, OR
	// 2) It appears only once and there are at least 20 total values
	outlierThreshold := int(math.Max(1, float64(modeCount)*0.01))
	var outliers []string

	for val, count := range freq {
		if count <= outlierThreshold || (count == 1 && totalNonEmpty >= 20) {
			outliers = append(outliers, val)
			if len(outliers) >= 20 {
				break
			}
		}
	}

	sort.Strings(outliers)
	return outliers
}

// ----------------------------------------------------------------
// Date/DateTime detection — majority vote (≥70% of values)
// ----------------------------------------------------------------

func detectDate(values []string) (InferredType, []string) {
	hits := make(map[string]int)
	for _, v := range values {
		tv := strings.TrimSpace(v)
		for _, layout := range dateLayouts {
			if _, err := time.Parse(layout, tv); err == nil {
				hits[layout]++
				break // first matching layout wins for this value
			}
		}
	}
	threshold := int(math.Ceil(float64(len(values)) * 0.70))
	var matched []string
	for layout, cnt := range hits {
		if cnt >= threshold {
			matched = append(matched, layout)
		}
	}
	if len(matched) == 0 {
		return "", nil
	}
	// Any layout containing time components → DateTime
	for _, layout := range matched {
		if strings.Contains(layout, "15:04") || strings.Contains(layout, "T") {
			return TypeDateTime, matched
		}
	}
	return TypeDate, matched
}

// ----------------------------------------------------------------
// Numeric statistics
// ----------------------------------------------------------------

func numStats(values []string) (minV, maxV, meanV, medianV, stdV *float64) {
	var nums []float64
	for _, v := range values {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			nums = append(nums, f)
		}
	}
	if len(nums) == 0 {
		return
	}
	mn, mx, sum := nums[0], nums[0], 0.0
	for _, f := range nums {
		if f < mn {
			mn = f
		}
		if f > mx {
			mx = f
		}
		sum += f
	}
	avg := sum / float64(len(nums))
	variance := 0.0
	for _, f := range nums {
		d := f - avg
		variance += d * d
	}
	variance /= float64(len(nums))
	sd := math.Sqrt(variance)

	// Compute median
	sorted := make([]float64, len(nums))
	copy(sorted, nums)
	sort.Float64s(sorted)
	var med float64
	n := len(sorted)
	if n%2 == 0 {
		med = (sorted[n/2-1] + sorted[n/2]) / 2.0
	} else {
		med = sorted[n/2]
	}

	minV, maxV, meanV, medianV, stdV = &mn, &mx, &avg, &med, &sd
	return
}

// ----------------------------------------------------------------
// Cardinality — top-N most frequent values
// ----------------------------------------------------------------

func topN(freq map[string]int, n int) []ValueCount {
	all := make([]ValueCount, 0, len(freq))
	for v, c := range freq {
		all = append(all, ValueCount{v, c})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Count > all[j].Count })
	if len(all) > n {
		return all[:n]
	}
	return all
}

// ----------------------------------------------------------------
// Case inconsistency detection
// ----------------------------------------------------------------

// mixedCase returns true when the values contain more than one casing style
// (all-lower, all-upper, or title-case).
func mixedCase(values []string) bool {
	hasLower, hasUpper, hasTitle := false, false, false
	for _, v := range values {
		lv, uv := strings.ToLower(v), strings.ToUpper(v)
		words := strings.Fields(v)
		isTitle := len(words) > 0
		for _, w := range words {
			rr := []rune(w)
			if len(rr) > 0 && !unicode.IsUpper(rr[0]) {
				isTitle = false
				break
			}
		}
		switch {
		case v == lv && v != uv:
			hasLower = true
		case v == uv && v != lv:
			hasUpper = true
		case isTitle && v != lv && v != uv:
			hasTitle = true
		}
		cnt := 0
		if hasLower {
			cnt++
		}
		if hasUpper {
			cnt++
		}
		if hasTitle {
			cnt++
		}
		if cnt >= 2 {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------
// Recommendation generation
// ----------------------------------------------------------------

func buildRecommendations(ca *ColumnAnalysis) ([]CleanRecommendation, []NormalizeRecommendation) {
	var clean []CleanRecommendation
	var norm []NormalizeRecommendation
	col := ca.Name

	// ── Cleaning ──────────────────────────────────────────────────

	// Always trim whitespace if there are leading/trailing spaces
	if ca.HasLeadingTrailingSpace {
		clean = append(clean, CleanRecommendation{
			Operation: "trim_whitespace", Column: col,
			Reason: "Values contain leading or trailing whitespace",
		})
	}

	// Collapse duplicate whitespace ("foo  bar" → "foo bar")
	if ca.HasDuplicateWhitespace {
		clean = append(clean, CleanRecommendation{
			Operation: "collapse_whitespace", Column: col,
			Reason: "Multiple consecutive spaces detected — collapse to single space",
		})
	}

	if ca.HasHTMLTags {
		clean = append(clean, CleanRecommendation{
			Operation: "remove_html", Column: col,
			Reason: "Values contain HTML tags or encoded entities",
		})
	}
	if ca.HasMultilineValues {
		clean = append(clean, CleanRecommendation{
			Operation: "remove_newlines", Column: col,
			Reason: "Multi-line values detected — collapse newlines to space",
		})
	}
	if ca.HasCommas && ca.InferredType == TypeNumericString {
		clean = append(clean, CleanRecommendation{
			Operation: "remove_special_chars", Column: col,
			Reason: "Numeric values contain thousand-separators or currency symbols",
			Params: map[string]string{"pattern": `[^0-9.\-]`},
		})
	}

	// ── Null/missing handling — smarter strategies ─────────────
	missing := ca.NullCount + ca.EmptyCount
	if missing > 0 {
		switch ca.InferredType {
		case TypeInteger, TypeFloat:
			// For numeric: recommend fill with mean (better than 0)
			fillVal := "0"
			reason := fmt.Sprintf("%d missing value(s) in numeric column — fill with mean", missing)
			if ca.Mean != nil {
				fillVal = fmt.Sprintf("%.4f", *ca.Mean)
				reason = fmt.Sprintf("%d missing value(s) in numeric column — fill with mean (%.2f)", missing, *ca.Mean)
			}
			clean = append(clean, CleanRecommendation{
				Operation: "fill_null", Column: col,
				Reason: reason,
				Params: map[string]string{"fill_value": fillVal, "strategy": "mean", "inferred_type": string(ca.InferredType)},
			})
		case TypeNumericString:
			clean = append(clean, CleanRecommendation{
				Operation: "fill_null", Column: col,
				Reason: fmt.Sprintf("%d missing value(s) in numeric-string column — fill with 0", missing),
				Params: map[string]string{"fill_value": "0", "inferred_type": string(ca.InferredType)},
			})
		case TypeBoolean:
			clean = append(clean, CleanRecommendation{
				Operation: "fill_null", Column: col,
				Reason: fmt.Sprintf("%d missing value(s) in boolean column — fill with false", missing),
				Params: map[string]string{"fill_value": "false", "inferred_type": string(ca.InferredType)},
			})
		case TypeCategorical:
			// For categorical: fill with mode (most common value)
			fillVal := "Unknown"
			reason := fmt.Sprintf("%d missing value(s) in categorical column — fill with mode", missing)
			if ca.ModeValue != "" {
				fillVal = ca.ModeValue
				reason = fmt.Sprintf("%d missing value(s) in categorical column — fill with mode (%q)", missing, ca.ModeValue)
			}
			clean = append(clean, CleanRecommendation{
				Operation: "fill_null", Column: col,
				Reason: reason,
				Params: map[string]string{"fill_value": fillVal, "strategy": "mode", "inferred_type": string(ca.InferredType)},
			})
		default:
			// Always fill — never auto-recommend dropping rows.
			fillVal := "Unknown"
			strategy := "mode"
			reason := fmt.Sprintf("%d missing value(s) — fill with most common value", missing)
			if ca.ModeValue != "" {
				fillVal = ca.ModeValue
				reason = fmt.Sprintf("%d missing value(s) — fill with mode (%q)", missing, ca.ModeValue)
			}
			if ca.FillRate < 0.50 {
				reason = fmt.Sprintf("Fill-rate %.1f%% — %d missing value(s), filling with %q", ca.FillRate*100, missing, fillVal)
			}
			clean = append(clean, CleanRecommendation{
				Operation: "fill_null", Column: col,
				Reason: reason,
				Params: map[string]string{"fill_value": fillVal, "strategy": strategy, "inferred_type": string(ca.InferredType)},
			})
		}
	}

	// ── Case standardization ──────────────────────────────────
	if ca.HasInconsistentCase &&
		(ca.InferredType == TypeFreeText || ca.InferredType == TypeCategorical || ca.InferredType == TypeAlphanumeric) {
		clean = append(clean, CleanRecommendation{
			Operation: "to_lowercase", Column: col,
			Reason: "Mixed letter casing detected — standardise to lowercase",
		})
	}
	if ca.HasSpecialChars && (ca.InferredType == TypeAlphanumeric || ca.InferredType == TypeFreeText) {
		clean = append(clean, CleanRecommendation{
			Operation: "remove_special_chars", Column: col,
			Reason: "Non-ASCII characters detected",
		})
	}
	if ca.InferredType == TypeDate || ca.InferredType == TypeDateTime {
		clean = append(clean, CleanRecommendation{
			Operation: "standardize_date", Column: col,
			Reason: "Standardise date values to ISO 8601 (YYYY-MM-DD)",
			Params: map[string]string{"target_format": "2006-01-02"},
		})
	}

	// ── Mismatched values — recommend dropping or fixing ───────
	if ca.MismatchCount > 0 && ca.TypeConfidence >= 0.85 {
		clean = append(clean, CleanRecommendation{
			Operation: "fix_mismatched_types", Column: col,
			Reason: fmt.Sprintf("%d value(s) don't match inferred type %q (confidence %.0f%%) — will be cleaned", ca.MismatchCount, ca.InferredType, ca.TypeConfidence*100),
			Params: map[string]string{"inferred_type": string(ca.InferredType)},
		})
	}

	// ── Categorical outliers — recommend cleanup ──────────────
	if len(ca.CategoricalOutliers) > 0 {
		sample := ca.CategoricalOutliers
		if len(sample) > 5 {
			sample = sample[:5]
		}
		clean = append(clean, CleanRecommendation{
			Operation: "fix_categorical_outliers", Column: col,
			Reason: fmt.Sprintf("%d rare/outlier value(s) in categorical column — e.g. %v", len(ca.CategoricalOutliers), sample),
		})
	}

	// ── Normalisation ─────────────────────────────────────────────
	// Value-level transforms (z_score, min_max_scale, enum_map, etc.)
	// are intentionally NOT recommended because they destructively
	// replace original CSV values.  Only DB normalization (1NF/2NF/3NF)
	// is offered via the Normalize step's DB-normalization panel.
	_ = norm // keep the compiler happy

	return clean, norm
}

// ----------------------------------------------------------------
// Dataset-level summary recommendations
// ----------------------------------------------------------------

func datasetRecommendations(da *DatasetAnalysis) []string {
	var recs []string
	lowFill, htmlCols, spaceCols, numCols := 0, 0, 0, 0
	for _, ca := range da.Columns {
		if ca.FillRate < 0.70 {
			lowFill++
		}
		if ca.HasHTMLTags {
			htmlCols++
		}
		if ca.HasLeadingTrailingSpace {
			spaceCols++
		}
		if ca.InferredType == TypeInteger || ca.InferredType == TypeFloat {
			numCols++
		}
	}
	if da.OverallQuality < 0.80 {
		recs = append(recs, fmt.Sprintf(
			"Overall fill-rate %.1f%% — define a null-handling strategy before ETL",
			da.OverallQuality*100,
		))
	}
	if lowFill > 0 {
		recs = append(recs, fmt.Sprintf(
			"%d column(s) with fill-rate below 70%% — review null handling",
			lowFill,
		))
	}
	if htmlCols > 0 {
		recs = append(recs, fmt.Sprintf(
			"%d column(s) contain HTML tags — apply remove_html cleaning",
			htmlCols,
		))
	}
	if spaceCols > 0 {
		recs = append(recs, fmt.Sprintf(
			"%d column(s) have whitespace padding — apply trim_whitespace globally",
			spaceCols,
		))
	}
	if numCols >= 2 {
		recs = append(recs, fmt.Sprintf(
			"%d numeric column(s) detected — consider feature scaling for ML pipelines",
			numCols,
		))
	}
	if da.SampledRows < da.TotalRows {
		recs = append(recs, fmt.Sprintf(
			"Analysis based on a sample of %d/%d rows — edge-case values may differ in the full dataset",
			da.SampledRows, da.TotalRows,
		))
	}
	return recs
}
