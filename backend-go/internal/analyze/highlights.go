package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"lover-instance/backend-go/internal/store"
)

// cstZone is the East-8 timezone fixed for highlights timeline and daily calculations.
var cstZone = time.FixedZone("CST", 8*3600)

// HighlightsAnalyzer extracts relationship milestones: journey start, streak, peak days, and late nights.
type HighlightsAnalyzer struct{}

func (h *HighlightsAnalyzer) ID() string             { return "highlights" }
func (h *HighlightsAnalyzer) Name() string           { return "关系高光与里程碑挖掘" }
func (h *HighlightsAnalyzer) Dependencies() []string { return []string{"behavior"} }

func (h *HighlightsAnalyzer) Run(ctx context.Context, s *store.Store, opts Options, onProgress func(done, total int)) error {
	if opts.Talker == "" {
		return fmt.Errorf("analyze: talker is required for highlights analysis")
	}

	minTs, maxTs, totalCount, err := s.FindOrigin(ctx, opts.Talker)
	if err != nil {
		return fmt.Errorf("analyze highlights: %w", err)
	}
	if totalCount == 0 {
		return nil
	}

	var events []store.HighlightEvent

	// 1. Origin milestone
	startT := time.UnixMilli(minTs).In(cstZone)
	endT := time.UnixMilli(maxTs).In(cstZone)
	startDate := startT.Format("2006-01-02")
	endDate := endT.Format("2006-01-02")

	startDay, _ := time.ParseInLocation("2006-01-02", startDate, cstZone)
	endDay, _ := time.ParseInLocation("2006-01-02", endDate, cstZone)
	journeyDays := int(endDay.Sub(startDay).Hours()/24) + 1

	originExtra, _ := json.Marshal(map[string]any{
		"start_date":   startDate,
		"end_date":     endDate,
		"journey_days": journeyDays,
		"total_msgs":   totalCount,
	})

	events = append(events, store.HighlightEvent{
		Talker:      opts.Talker,
		Kind:        "origin",
		EventDate:   startDate,
		Title:       "启程之日：初次相遇",
		Description: fmt.Sprintf("从 %s 留下第一条记录起，携手走过 %d 天，累计互动 %d 条消息", startDate, journeyDays, totalCount),
		MetricValue: float64(journeyDays),
		ExtraJSON:   string(originExtra),
	})

	// 2. Longest Streak milestone
	days, err := s.FindDistinctDays(ctx, opts.Talker)
	if err != nil {
		return fmt.Errorf("analyze highlights streak: %w", err)
	}

	if len(days) > 0 {
		maxStreak := 1
		maxStart := days[0]
		maxEnd := days[0]

		curStreak := 1
		curStart := days[0]

		for i := 1; i < len(days); i++ {
			prev, _ := time.ParseInLocation("2006-01-02", days[i-1], cstZone)
			curr, _ := time.ParseInLocation("2006-01-02", days[i], cstZone)

			if curr.Sub(prev) == 24*time.Hour {
				curStreak++
				if curStreak > maxStreak {
					maxStreak = curStreak
					maxStart = curStart
					maxEnd = days[i]
				}
			} else {
				curStreak = 1
				curStart = days[i]
			}
		}

		streakExtra, _ := json.Marshal(map[string]any{
			"max_streak": maxStreak,
			"start_date": maxStart,
			"end_date":   maxEnd,
		})

		events = append(events, store.HighlightEvent{
			Talker:      opts.Talker,
			Kind:        "streak",
			EventDate:   fmt.Sprintf("%s~%s", maxStart, maxEnd),
			Title:       fmt.Sprintf("心动连击：连续聊天 %d 天", maxStreak),
			Description: fmt.Sprintf("最长连续互发消息 %d 天（%s 至 %s），每一天都不曾缺席", maxStreak, maxStart, maxEnd),
			MetricValue: float64(maxStreak),
			ExtraJSON:   string(streakExtra),
		})
	}

	// 3. Peak Days
	peaks, err := s.FindPeakDays(ctx, opts.Talker, 3)
	if err != nil {
		return fmt.Errorf("analyze highlights peak days: %w", err)
	}

	for _, p := range peaks {
		peakExtra, _ := json.Marshal(map[string]any{
			"total_msgs":   p.TotalMsgs,
			"self_msgs":    p.SelfMsgs,
			"partner_msgs": p.PartnerMsgs,
		})
		events = append(events, store.HighlightEvent{
			Talker:      opts.Talker,
			Kind:        "peak_day",
			EventDate:   p.Day,
			Title:       fmt.Sprintf("倾心长谈：单日 %d 条", p.TotalMsgs),
			Description: fmt.Sprintf("%s 迎来长谈峰值，单日留下 %d 条消息（我 %d 条，TA %d 条）", p.Day, p.TotalMsgs, p.SelfMsgs, p.PartnerMsgs),
			MetricValue: float64(p.TotalMsgs),
			ExtraJSON:   string(peakExtra),
		})
	}

	// 4. Late Night Talks
	nights, err := s.FindLateNights(ctx, opts.Talker, 3)
	if err != nil {
		return fmt.Errorf("analyze highlights late nights: %w", err)
	}

	for _, n := range nights {
		nightExtra, _ := json.Marshal(map[string]any{
			"late_msgs": n.LateMsgs,
		})
		events = append(events, store.HighlightEvent{
			Talker:      opts.Talker,
			Kind:        "late_night",
			EventDate:   n.Day,
			Title:       fmt.Sprintf("深夜畅聊：凌晨 %d 条", n.LateMsgs),
			Description: fmt.Sprintf("%s 凌晨时分（00:00–05:00），在夜深人静时留下了 %d 条深夜对话", n.Day, n.LateMsgs),
			MetricValue: float64(n.LateMsgs),
			ExtraJSON:   string(nightExtra),
		})
	}

	if err := s.SaveHighlights(ctx, opts.Talker, events); err != nil {
		return fmt.Errorf("analyze highlights save: %w", err)
	}

	if onProgress != nil {
		onProgress(len(events), len(events))
	}

	return nil
}
