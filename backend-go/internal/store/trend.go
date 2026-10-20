package store

import (
	"context"
	"fmt"
	"math"
	"time"
)

// trendWindowDays is the width of the rolling window in calendar days,
// inclusive of the day being estimated.
const trendWindowDays = 7

// trendMinSamples is how many scored messages a point needs before it may be
// drawn. One or two messages are noise wearing a trend's clothes.
const trendMinSamples = 3

// trendDayLayout is the day key both the query and this file format with.
const trendDayLayout = "2006-01-02"

// TrendPoint is one day of a rolling sentiment estimate.
type TrendPoint struct {
	Day    string  // "YYYY-MM-DD" in UTC+8
	Mean   float64 // mean polarity of every scored message in the window
	StdDev float64 // population standard deviation of those same values (divide by n, not n-1)
	Count  int     // number of messages in the window
}

// Drawable reports whether this point has enough samples to be drawn.
// A mean over one or two messages is noise dressed as a trend.
func (p TrendPoint) Drawable() bool {
	return p.Count >= trendMinSamples
}

// trendDay is one calendar day's aggregate of scored polarities, summed rather
// than averaged so that overlapping windows can be combined by addition.
type trendDay struct {
	day    string
	moment time.Time
	count  int
	sum    float64
	sumSq  float64
}

// Trend returns the rolling 7-day sentiment estimate, oldest first.
//
// The window is a span of calendar days, not a span of rows: for each day that
// has data, it covers the messages on the days from six days before through the
// day itself. Only days that actually have messages take part. The days in
// between are not zero-filled, because a zero is a claim ("neutral that day")
// and the truth is that nobody said anything — padding with zeros would drag
// the mean toward neutral and draw a mood dip that never happened. Days with no
// data produce no point at all; breaking the line there is the renderer's job.
//
// Days are bucketed in UTC+8, the same fixed offset RebuildSentimentMetrics
// uses. Bucketing in UTC would slide every boundary eight hours and misfile
// late-night messages under the previous day.
//
// Aggregation happens in two passes — per-day sums from SQL, then the window in
// Go — because the window is defined over the days that exist rather than over
// a date range, which a SQL frame clause cannot express without first
// materialising the missing days we deliberately do not want.
func (s *Store) Trend(ctx context.Context, talker, modelVersion string) ([]TrendPoint, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT strftime('%Y-%m-%d', m.ts / 1000, 'unixepoch', '+8 hours') AS day,
       COUNT(s.polarity), SUM(s.polarity), SUM(s.polarity * s.polarity)
  FROM sentiment s
  JOIN messages m ON m.id = s.msg_id
 WHERE m.talker = ? AND s.model_version = ?
   AND s.polarity IS NOT NULL
 GROUP BY day
 ORDER BY day ASC
`, talker, modelVersion)
	if err != nil {
		return nil, fmt.Errorf("read daily sentiment sums: %w", err)
	}
	defer rows.Close()

	days := make([]trendDay, 0, 64)
	for rows.Next() {
		var day trendDay
		if err := rows.Scan(&day.day, &day.count, &day.sum, &day.sumSq); err != nil {
			return nil, fmt.Errorf("scan daily sentiment sum: %w", err)
		}
		moment, err := time.ParseInLocation(trendDayLayout, day.day, cst)
		if err != nil {
			return nil, fmt.Errorf("parse sentiment day %q: %w", day.day, err)
		}
		day.moment = moment
		days = append(days, day)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate daily sentiment sums: %w", err)
	}

	points := make([]TrendPoint, 0, len(days))
	for i := range days {
		windowStart := days[i].moment.AddDate(0, 0, -(trendWindowDays - 1))
		count := 0
		sum, sumSq := 0.0, 0.0
		// Walk backwards over the days that have data and stop at the window
		// edge. A day with no messages contributes nothing rather than a zero.
		for j := i; j >= 0 && !days[j].moment.Before(windowStart); j-- {
			count += days[j].count
			sum += days[j].sum
			sumSq += days[j].sumSq
		}
		mean := sum / float64(count)
		// Population variance as E[x²] − E[x]². For a run of identical values
		// rounding can push this a hair below zero, and sqrt(-1e-17) is NaN,
		// which would poison every comparison downstream. Clamp before the root.
		variance := sumSq/float64(count) - mean*mean
		if variance < 0 {
			variance = 0
		}
		points = append(points, TrendPoint{
			Day:    days[i].day,
			Mean:   mean,
			StdDev: math.Sqrt(variance),
			Count:  count,
		})
	}
	return points, nil
}
