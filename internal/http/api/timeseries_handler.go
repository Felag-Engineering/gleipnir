package api

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
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
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"1d":  24 * time.Hour,
}

// defaultBucketForWindow is the bucket granularity used when the client omits
// the bucket query param. The 24h window uses 5-minute buckets (288 points)
// so the chart scrolls smoothly; wider windows use coarser buckets.
var defaultBucketForWindow = map[string]string{
	"24h": "5m",
	"7d":  "6h",
	"30d": "1d",
}

// Get handles GET /api/v1/stats/timeseries?window=24h&bucket=5m.
//
// It fetches rows from the DB (grouped by configurable time bucket, status, and
// model), then distributes them into UTC-aligned buckets for the requested
// window. Empty buckets are always emitted so the frontend has a consistent
// x-axis.
func (h *TimeSeriesHandler) Get(w http.ResponseWriter, r *http.Request) {
	windowParam := r.URL.Query().Get("window")
	if windowParam == "" {
		windowParam = "24h"
	}
	bucketParam := r.URL.Query().Get("bucket")
	if bucketParam == "" {
		bucketParam = defaultBucketForWindow[windowParam]
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
			"supported values: 5m, 15m, 1h, 6h, 1d")
		return
	}

	// end tracks now exactly so the right edge of the chart advances
	// continuously. start is aligned to a step boundary so the empty-bucket
	// synthesis in Go produces keys that match the SQL strftime output.
	now := time.Now().UTC()
	end := now
	start := end.Add(-window).Truncate(step)

	rows, err := h.store.GetRunTimeSeries(r.Context(), db.GetRunTimeSeriesParams{
		BucketSeconds: strconv.FormatInt(int64(step/time.Second), 10),
		Since:         start.Format(time.RFC3339Nano),
	})
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to fetch time series", err.Error())
		return
	}

	// Build the ordered list of clock-aligned bucket timestamps covering the window.
	buckets := buildBuckets(start, end, step, rows)
	httputil.WriteJSON(w, http.StatusOK, TimeSeriesResponse{Buckets: buckets})
}

// buildBuckets assembles clock-aligned TimeSeriesBuckets for the window.
// start is aligned to a step boundary; end is not necessarily aligned (it is
// now). bucketCount uses ceil so the trailing partial bucket containing recent
// runs is always emitted.
// DB rows are matched to buckets by comparing their SQL-generated bucket string
// (e.g. "2026-04-01T14:05:00Z") against each bucket's timestamp. Model IDs in
// the DB rows are converted to display names before being placed in CostByModel.
func buildBuckets(start, end time.Time, step time.Duration, rows []db.GetRunTimeSeriesRow) []TimeSeriesBucket {
	bucketCount := int(math.Ceil(float64(end.Sub(start)) / float64(step)))
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
		// Format to match strftime('%Y-%m-%dT%H:%M:%SZ', ..., 'unixepoch') output.
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
