package api

import (
	"net/http"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/httputil"
)

// TimeSeriesHandler serves GET /api/v1/stats/timeseries.
type TimeSeriesHandler struct {
	store *db.Store
}

// NewTimeSeriesHandler creates a TimeSeriesHandler backed by the given store.
func NewTimeSeriesHandler(store *db.Store) *TimeSeriesHandler {
	return &TimeSeriesHandler{store: store}
}

// TimeSeriesBucket holds the aggregated metrics for one time bucket.
type TimeSeriesBucket struct {
	Timestamp          string           `json:"timestamp"`
	Completed          int64            `json:"completed"`
	Failed             int64            `json:"failed"`
	WaitingForApproval int64            `json:"waiting_for_approval"`
	WaitingForFeedback int64            `json:"waiting_for_feedback"`
	CostByModel        map[string]int64 `json:"cost_by_model"`
}

// TimeSeriesResponse wraps the bucket slice returned by GET /api/v1/stats/timeseries.
type TimeSeriesResponse struct {
	Buckets []TimeSeriesBucket `json:"buckets"`
}

// windowDuration maps the supported window query param values to durations.
var windowDuration = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// bucketDuration maps the supported bucket query param values to durations.
var bucketDuration = map[string]time.Duration{
	"1h": time.Hour,
	"6h": 6 * time.Hour,
	"1d": 24 * time.Hour,
}

// Get handles GET /api/v1/stats/timeseries?window=24h&bucket=1h.
//
// It fetches raw hourly rows from the DB (grouped by status and model), then
// distributes them into clock-aligned buckets for the requested window. Empty
// buckets are always emitted so the frontend has a consistent x-axis.
func (h *TimeSeriesHandler) Get(w http.ResponseWriter, r *http.Request) {
	windowParam := r.URL.Query().Get("window")
	if windowParam == "" {
		windowParam = "24h"
	}
	bucketParam := r.URL.Query().Get("bucket")
	if bucketParam == "" {
		bucketParam = "1h"
	}

	window, ok := windowDuration[windowParam]
	if !ok {
		httputil.WriteError(w, http.StatusBadRequest, "unsupported window parameter",
			"supported values: 24h, 7d, 30d")
		return
	}
	step, ok := bucketDuration[bucketParam]
	if !ok {
		httputil.WriteError(w, http.StatusBadRequest, "unsupported bucket parameter",
			"supported values: 1h, 6h, 1d")
		return
	}

	// end is the next clock-aligned boundary after now. This ensures the current
	// partial bucket is always the last one in the response.
	now := time.Now().UTC()
	end := now.Truncate(step).Add(step)
	start := end.Add(-window)
	sinceStr := start.Format(time.RFC3339Nano)

	rows, err := h.store.GetRunTimeSeries(r.Context(), sinceStr)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to fetch time series", err.Error())
		return
	}

	// Build the ordered list of clock-aligned bucket timestamps covering the window.
	buckets := buildBuckets(start, end, step, rows)
	httputil.WriteJSON(w, http.StatusOK, TimeSeriesResponse{Buckets: buckets})
}

// buildBuckets assembles clock-aligned TimeSeriesBuckets for the window.
// start and end are already aligned to step boundaries (start = end - window).
// DB rows are matched to buckets by comparing their SQL-generated bucket string
// (e.g. "2026-04-01T14:00:00Z") against each bucket's timestamp. Model IDs in
// the DB rows are converted to display names before being placed in CostByModel.
func buildBuckets(start, end time.Time, step time.Duration, rows []db.GetRunTimeSeriesRow) []TimeSeriesBucket {
	window := end.Sub(start)
	bucketCount := int(window / step)
	if bucketCount < 1 {
		bucketCount = 1
	}

	// Pre-index rows by their bucket string for O(1) lookup.
	// The Bucket field from sqlc is interface{} because SQLite's strftime returns
	// a text value; we convert to string via rowBucketString.
	rowsByBucket := make(map[string][]db.GetRunTimeSeriesRow, len(rows))
	for _, row := range rows {
		key := rowBucketString(row.Bucket)
		rowsByBucket[key] = append(rowsByBucket[key], row)
	}

	buckets := make([]TimeSeriesBucket, 0, bucketCount)
	for i := 0; i < bucketCount; i++ {
		t := start.Add(time.Duration(i) * step)
		// Format to match strftime('%Y-%m-%dT%H:00:00Z', ...) output.
		bucketTS := t.UTC().Format("2006-01-02T15:04:05Z")

		b := TimeSeriesBucket{
			Timestamp:   t.UTC().Format(time.RFC3339),
			CostByModel: make(map[string]int64),
		}

		for _, row := range rowsByBucket[bucketTS] {
			switch row.Status {
			case "complete":
				b.Completed += row.RunCount
			case "failed":
				b.Failed += row.RunCount
			case "waiting_for_approval":
				b.WaitingForApproval += row.RunCount
			case "waiting_for_feedback":
				b.WaitingForFeedback += row.RunCount
			}
			if row.TotalTokens > 0 {
				displayName := GetModelDisplayName(row.Model)
				b.CostByModel[displayName] += row.TotalTokens
			}
		}

		buckets = append(buckets, b)
	}

	return buckets
}

// rowBucketString converts the interface{} Bucket field from sqlc into a plain
// string. SQLite's strftime returns TEXT, so the value is always a string at
// runtime. This function handles the interface{} wrapper that sqlc emits for
// computed columns whose type it cannot infer statically.
func rowBucketString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
