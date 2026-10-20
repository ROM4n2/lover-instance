package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// BehaviorModelVersion labels the rows RebuildBehaviorMetrics writes. Pure
// behavioral measures carry their own version so they can coexist with a
// sentiment model's rows for the same day, which is why daily_stats is keyed
// on (talker, day, model_version) rather than (talker, day).
const BehaviorModelVersion = "behavior-v1"

// cst is the fixed UTC+8 zone every day and hour bucket is computed in.
//
// The machine's own zone is not consulted: a report must read the same in
// August and in January, and the conversation happened in Beijing time
// regardless of where the report is later opened. Bucketing in UTC would shift
// every day boundary by eight hours and misclassify late-night messages as
// evening ones.
var cst = time.FixedZone("CST", 8*60*60)

// MonthOf names the CST month a timestamp falls in, as the YYYY-MM period the
// monthly word-freq rows use.
//
// It lives here, next to cst, so there is one definition of the boundary: the
// monthly word counts and the daily and hourly buckets must agree about which
// month a late-night message belongs to, and a second `time.FixedZone` or a
// second format string elsewhere would be a second chance to disagree.
func MonthOf(tsMs int64) string {
	return time.UnixMilli(tsMs).In(cst).Format("2006-01")
}

// conversationGapMs is how long a silence has to be before the next message is
// treated as opening a new conversation rather than continuing one.
const conversationGapMs int64 = 5 * 60 * 1000

// fastReplyMs is the "instant reply" threshold: one minute.
const fastReplyMs int64 = 60 * 1000

// lateNightHour is the first hour counted as late night, in CST.
const lateNightHour = 23

// DailyRow is one day's behavioral totals. AvgPolarity belongs to a sentiment
// model version and is left at zero here; see BehaviorMetrics for why the two
// live in different rows.
type DailyRow struct {
	Day          string
	SelfMsgs     int
	PartnerMsgs  int
	SelfChars    int
	PartnerChars int
	FirstIsSelf  bool
	LastIsSelf   bool
}

// HeatCell is one square of the 24x7 grid.
type HeatCell struct {
	Weekday      int
	Hour         int
	SelfCount    int
	PartnerCount int
}

// ConversationRow is one run of messages separated by less than the gap
// threshold.
type ConversationRow struct {
	StartMs   int64
	EndMs     int64
	Initiator string
	MsgCount  int
}

// ReplyStats summarizes how quickly each side answered the other.
type ReplyStats struct {
	MedianMs    int64
	P90Ms       int64
	FastRatio   float64
	SampleCount int
}

// TypeCount is one message type's frequency.
type TypeCount struct {
	MsgType int
	Count   int
}

// BehaviorMetrics is everything the behavioral pass derives from the message
// stream.
type BehaviorMetrics struct {
	Daily         []DailyRow
	Heatmap       []HeatCell
	Conversations []ConversationRow
	Reply         ReplyStats
	Types         []TypeCount

	// LateNightRatio is the share of messages sent at or after 23:00 CST.
	LateNightRatio float64
	// InitiatorRatio is the share of conversations this user opened.
	InitiatorRatio float64
	// TurnBalanceCharRatio is self-typed characters per partner-typed
	// character. Above one means this user writes more.
	TurnBalanceCharRatio float64
	// TurnBalanceMsgRatio is self-sent messages per partner-sent message, the
	// other half of the turn balance the design asks for. The two can point in
	// opposite directions — many short messages against few long ones — which
	// is exactly why both are reported rather than one standing in for the
	// other.
	TurnBalanceMsgRatio float64
	// AvgSelfLen and AvgPartnerLen are mean characters per message.
	AvgSelfLen    float64
	AvgPartnerLen float64
	// RangeStartMs and RangeEndMs are the first and last message timestamps,
	// which is what the report states as the period it covers.
	RangeStartMs int64
	RangeEndMs   int64
}

// SelfSpeaker and PartnerSpeaker are the two values recorded in
// conversations.initiator, so a reader does not have to guess what an empty
// string meant.
const (
	SelfSpeaker    = "self"
	PartnerSpeaker = "partner"
)

// RebuildBehaviorMetrics recomputes every behavioral aggregate for one
// conversation, replacing whatever was there.
//
// Rebuild rather than append: the tables are derived from messages, and
// re-running an import or a rebuild must not double the counts. Everything
// happens in one transaction so a report never reads a half-rebuilt day.
func (s *Store) RebuildBehaviorMetrics(ctx context.Context, talker string) error {
	facts, err := s.messageFacts(ctx, talker)
	if err != nil {
		return err
	}
	metrics := foldBehavior(facts)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin behavior rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, statement := range []string{
		`DELETE FROM daily_stats WHERE talker = ? AND model_version = ?`,
		`DELETE FROM activity_heatmap WHERE talker = ?`,
		`DELETE FROM conversations WHERE talker = ?`,
	} {
		args := []any{talker}
		if statement == `DELETE FROM daily_stats WHERE talker = ? AND model_version = ?` {
			args = append(args, BehaviorModelVersion)
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return fmt.Errorf("clear previous behavior metrics: %w", err)
		}
	}

	if err := insertDailyRows(ctx, tx, talker, metrics.Daily); err != nil {
		return err
	}
	if err := insertHeatCells(ctx, tx, talker, metrics.Heatmap); err != nil {
		return err
	}
	if err := insertConversations(ctx, tx, talker, metrics.Conversations); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit behavior rebuild: %w", err)
	}
	return nil
}

// BehaviorMetrics derives the behavioral aggregates for one conversation.
//
// It recomputes from the message stream rather than reading back the tables
// RebuildBehaviorMetrics writes. That is deliberate: several of these figures
// — the late-night share, the reply percentiles, the message-type histogram —
// have no column in the design's schema, and reading some values from the
// tables while computing others from the messages would leave two derivations
// of the same day that can drift apart. The persisted tables still exist, are
// still filled, and are what a dashboard will read in M3; the M1 report uses
// this one path so there is exactly one answer.
func (s *Store) BehaviorMetrics(ctx context.Context, talker string) (BehaviorMetrics, error) {
	facts, err := s.messageFacts(ctx, talker)
	if err != nil {
		return BehaviorMetrics{}, err
	}
	return foldBehavior(facts), nil
}

// messageFacts reads the ordered message stream. Content itself is not
// selected: only its length is needed, and the bodies have no business
// travelling further than the scoring step.
func (s *Store) messageFacts(ctx context.Context, talker string) ([]messageFact, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT ts, is_self, msg_type, LENGTH(COALESCE(content, ''))
  FROM messages
 WHERE talker = ?
 ORDER BY ts ASC, id ASC
`, talker)
	if err != nil {
		return nil, fmt.Errorf("read messages for behavior metrics: %w", err)
	}
	defer rows.Close()

	var facts []messageFact
	for rows.Next() {
		var fact messageFact
		var isSelf int
		if err := rows.Scan(&fact.ts, &isSelf, &fact.msgType, &fact.charCount); err != nil {
			return nil, fmt.Errorf("scan message fact: %w", err)
		}
		fact.isSelf = isSelf == 1
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message facts: %w", err)
	}
	return facts, nil
}

type messageFact struct {
	ts        int64
	isSelf    bool
	msgType   int
	charCount int
}

// foldBehavior turns the ordered stream into every behavioral aggregate in one
// pass. Doing it here rather than in SQL keeps the day-boundary arithmetic and
// the conversation splitting next to each other, and those are exactly the two
// parts that need a test at the edge.
func foldBehavior(facts []messageFact) BehaviorMetrics {
	metrics := BehaviorMetrics{}
	if len(facts) == 0 {
		return metrics
	}

	// The grid is always all 168 squares, so a report can plot a stable axis
	// without asking whether an hour was omitted for lack of traffic.
	cells := make([]HeatCell, 168)
	for index := range cells {
		cells[index] = HeatCell{Weekday: index / 24, Hour: index % 24}
	}

	byDay := make(map[string]*DailyRow)
	typeCounts := make(map[int]int)
	var latencies []int64
	var selfChars, partnerChars, selfMsgs, partnerMsgs, lateNight int

	metrics.Conversations = make([]ConversationRow, 0, 16)
	var previous *messageFact
	var lastEnd int64

	for i := range facts {
		fact := facts[i]
		at := time.UnixMilli(fact.ts).In(cst)

		day := at.Format("2006-01-02")
		row, seen := byDay[day]
		if !seen {
			row = &DailyRow{Day: day}
			byDay[day] = row
		}
		if fact.isSelf {
			row.SelfMsgs++
			row.SelfChars += fact.charCount
			selfMsgs++
			selfChars += fact.charCount
		} else {
			row.PartnerMsgs++
			row.PartnerChars += fact.charCount
			partnerMsgs++
			partnerChars += fact.charCount
		}
		// The stream is ordered by timestamp, so the first message seen for a
		// day is that day's first, and the last one seen wins the "last"
		// attribution.
		if row.SelfMsgs+row.PartnerMsgs == 1 {
			row.FirstIsSelf = fact.isSelf
		}
		row.LastIsSelf = fact.isSelf

		cell := &cells[weekdayIndex(at)*24+at.Hour()]
		if fact.isSelf {
			cell.SelfCount++
		} else {
			cell.PartnerCount++
		}

		if at.Hour() >= lateNightHour {
			lateNight++
		}
		typeCounts[fact.msgType]++

		if previous == nil || fact.ts-lastEnd >= conversationGapMs {
			metrics.Conversations = append(metrics.Conversations, ConversationRow{
				StartMs:   fact.ts,
				EndMs:     fact.ts,
				Initiator: speaker(fact.isSelf),
				MsgCount:  1,
			})
		} else {
			open := &metrics.Conversations[len(metrics.Conversations)-1]
			open.EndMs = fact.ts
			open.MsgCount++
		}
		lastEnd = fact.ts

		// A reply is where the speaker alternates. The silence beforehand is
		// deliberately not capped: the median and the instant-reply share are
		// robust to the long tail, and P90 exists to report that tail rather
		// than to have it trimmed away.
		if previous != nil && previous.isSelf != fact.isSelf {
			latencies = append(latencies, fact.ts-previous.ts)
		}
		previous = &facts[i]
	}

	metrics.Heatmap = cells
	metrics.Daily = make([]DailyRow, 0, len(byDay))
	for _, row := range byDay {
		metrics.Daily = append(metrics.Daily, *row)
	}
	sort.Slice(metrics.Daily, func(i, j int) bool { return metrics.Daily[i].Day < metrics.Daily[j].Day })

	metrics.Types = make([]TypeCount, 0, len(typeCounts))
	for msgType, count := range typeCounts {
		metrics.Types = append(metrics.Types, TypeCount{MsgType: msgType, Count: count})
	}
	sort.Slice(metrics.Types, func(i, j int) bool { return metrics.Types[i].MsgType < metrics.Types[j].MsgType })

	metrics.Reply = summarizeLatencies(latencies)
	if total := selfMsgs + partnerMsgs; total > 0 {
		metrics.LateNightRatio = float64(lateNight) / float64(total)
	}
	if len(metrics.Conversations) > 0 {
		initiated := 0
		for _, conversation := range metrics.Conversations {
			if conversation.Initiator == SelfSpeaker {
				initiated++
			}
		}
		metrics.InitiatorRatio = float64(initiated) / float64(len(metrics.Conversations))
	}
	if partnerChars > 0 {
		metrics.TurnBalanceCharRatio = float64(selfChars) / float64(partnerChars)
	}
	if partnerMsgs > 0 {
		metrics.TurnBalanceMsgRatio = float64(selfMsgs) / float64(partnerMsgs)
	}
	if selfMsgs > 0 {
		metrics.AvgSelfLen = float64(selfChars) / float64(selfMsgs)
	}
	if partnerMsgs > 0 {
		metrics.AvgPartnerLen = float64(partnerChars) / float64(partnerMsgs)
	}
	metrics.RangeStartMs = facts[0].ts
	metrics.RangeEndMs = facts[len(facts)-1].ts
	return metrics
}

// weekdayIndex numbers days from Monday, which is how a Chinese 24x7 grid is
// read. Go's time.Weekday starts at Sunday.
func weekdayIndex(at time.Time) int {
	return (int(at.Weekday()) + 6) % 7
}

func speaker(isSelf bool) string {
	if isSelf {
		return SelfSpeaker
	}
	return PartnerSpeaker
}

// summarizeLatencies returns the median, the 90th percentile and the share of
// replies inside the instant threshold.
func summarizeLatencies(latencies []int64) ReplyStats {
	if len(latencies) == 0 {
		return ReplyStats{}
	}
	sorted := append([]int64(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	fast := 0
	for _, latency := range sorted {
		if latency <= fastReplyMs {
			fast++
		}
	}
	return ReplyStats{
		MedianMs:    percentile(sorted, 0.50),
		P90Ms:       percentile(sorted, 0.90),
		FastRatio:   float64(fast) / float64(len(sorted)),
		SampleCount: len(sorted),
	}
}

// percentile picks the value at q using the nearest-rank method, which needs no
// interpolation and so cannot invent a reply time that never occurred.
func percentile(sorted []int64, q float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(q * float64(len(sorted)))
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func insertDailyRows(ctx context.Context, tx *sql.Tx, talker string, rows []DailyRow) error {
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO daily_stats
  (talker, day, model_version, self_msgs, partner_msgs, self_chars, partner_chars,
   first_is_self, last_is_self, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
`)
	if err != nil {
		return fmt.Errorf("prepare daily_stats insert: %w", err)
	}
	defer statement.Close()
	for _, row := range rows {
		_, err := statement.ExecContext(ctx, talker, row.Day, BehaviorModelVersion,
			row.SelfMsgs, row.PartnerMsgs, row.SelfChars, row.PartnerChars,
			boolToInt(row.FirstIsSelf), boolToInt(row.LastIsSelf))
		if err != nil {
			return fmt.Errorf("insert daily_stats row %q: %w", row.Day, err)
		}
	}
	return nil
}

func insertHeatCells(ctx context.Context, tx *sql.Tx, talker string, cells []HeatCell) error {
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO activity_heatmap (talker, weekday, hour, self_count, partner_count)
VALUES (?, ?, ?, ?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare activity_heatmap insert: %w", err)
	}
	defer statement.Close()
	for _, cell := range cells {
		if _, err := statement.ExecContext(ctx, talker, cell.Weekday, cell.Hour, cell.SelfCount, cell.PartnerCount); err != nil {
			return fmt.Errorf("insert activity_heatmap cell %d/%d: %w", cell.Weekday, cell.Hour, err)
		}
	}
	return nil
}

func insertConversations(ctx context.Context, tx *sql.Tx, talker string, conversations []ConversationRow) error {
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO conversations (talker, start_ts, end_ts, initiator, msg_count)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (talker, start_ts) DO UPDATE SET
  end_ts = excluded.end_ts, initiator = excluded.initiator, msg_count = excluded.msg_count
`)
	if err != nil {
		return fmt.Errorf("prepare conversations insert: %w", err)
	}
	defer statement.Close()
	for _, conversation := range conversations {
		if _, err := statement.ExecContext(ctx, talker, conversation.StartMs, conversation.EndMs, conversation.Initiator, conversation.MsgCount); err != nil {
			return fmt.Errorf("insert conversation at %d: %w", conversation.StartMs, err)
		}
	}
	return nil
}
