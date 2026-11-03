// Package ingest moves a chatlog-keeper export into the store: it normalizes
// the export through the adapter, applies the rescan window, and advances the
// watermark. It is the only place that decides what "nothing new" means, and
// it is deliberately stubborn about treating that as success.
package ingest

import (
	"context"
	"fmt"
	"io"
	"sort"

	"lover-instance/backend-go/internal/model"
	"lover-instance/backend-go/internal/source/chatlogkeeper"
	"lover-instance/backend-go/internal/store"
)

// RescanWindowMs is how far behind the watermark an import re-reads.
//
// Messages can land out of order — the exporter walks the database, and the
// client writes asynchronously — so a strict "newer than the last ts" filter
// would step over anything that arrived late with an older timestamp. Sixty
// seconds of overlap costs a few duplicate-key checks and closes that hole.
// Deduplication is the unique key's job, not the window's.
const RescanWindowMs int64 = 60_000

// Options describes one import.
type Options struct {
	// Source names the adapter's data source, e.g. "chatlog-keeper". It is
	// part of the message identity, so it is recorded rather than implied.
	Source string
	// Talker selects one conversation. Empty means "every conversation the
	// export contains": the CLI analyzes one conversation at a time, but the
	// import itself has no reason to discard the others, and the caller
	// cannot always name the conversation in advance.
	Talker string
	// Reader yields the export.
	Reader io.Reader
}

// Report accounts for one import.
type Report struct {
	// Parsed counts rows read from the export, across every conversation.
	Parsed int
	// Inserted, Skipped and Updated sum to the number of rows actually
	// submitted, which is Parsed minus anything the rescan window excluded.
	Inserted int
	Skipped  int
	Updated  int
	// Talkers lists the conversations this import touched, sorted.
	Talkers []string
	// WindowStartMs is the earliest timestamp the rescan window admitted, or
	// zero when no window applied (a first import reads everything).
	WindowStartMs int64
}

// Run imports an export.
//
// A zero row count is never reported as success. Two independent guards cover
// the two ways that happens, and they are kept separate because they fail for
// different reasons: an export with nothing in it is a broken input, whereas
// an export whose rows all collapse onto one identity is a broken key — and
// the second one is the failure mode this project exists to prevent.
func Run(ctx context.Context, s *store.Store, opts Options) (Report, error) {
	if opts.Source == "" {
		return Report{}, fmt.Errorf("ingest: no source given; the source is part of the message identity")
	}
	parsed, err := chatlogkeeper.ParseMessages(opts.Reader)
	if err != nil {
		return Report{}, fmt.Errorf("ingest: parse export: %w", err)
	}
	if len(parsed) == 0 {
		return Report{}, fmt.Errorf("ingest: the export contains no messages; refusing to report an empty import as success")
	}

	byTalker := groupByTalker(parsed)
	talkers := make([]string, 0, len(byTalker))
	for talker := range byTalker {
		talkers = append(talkers, talker)
	}
	sort.Strings(talkers)

	if opts.Talker != "" {
		if _, present := byTalker[opts.Talker]; !present {
			return Report{}, fmt.Errorf("ingest: talker %q does not appear in this export (%d conversation(s) present); run with --list-talkers to see the available values", opts.Talker, len(byTalker))
		}
		talkers = []string{opts.Talker}
	}

	report := Report{Parsed: len(parsed)}
	for _, talker := range talkers {
		sub, err := importTalker(ctx, s, opts.Source, talker, byTalker[talker])
		if err != nil {
			return Report{}, err
		}
		report.Inserted += sub.Inserted
		report.Skipped += sub.Skipped
		report.Updated += sub.Updated
		if sub.WindowStartMs > 0 && (report.WindowStartMs == 0 || sub.WindowStartMs < report.WindowStartMs) {
			report.WindowStartMs = sub.WindowStartMs
		}
	}
	report.Talkers = talkers
	return report, nil
}

// importTalker applies the rescan window to one conversation and writes it.
func importTalker(ctx context.Context, s *store.Store, source, talker string, rows []model.CanonicalMessage) (Report, error) {
	baseline, err := s.CountMessages(ctx, talker)
	if err != nil {
		return Report{}, fmt.Errorf("ingest: %w", err)
	}

	windowStart, hasWindow, err := rescanStart(ctx, s, source, talker)
	if err != nil {
		return Report{}, err
	}

	selected := rows
	if hasWindow {
		selected = make([]model.CanonicalMessage, 0, len(rows))
		for _, row := range rows {
			// `>=` rather than `>`: a message landing exactly on the window
			// edge must be re-checked, not skipped. Dropping it would lose a
			// message on the boundary of every single rescan.
			if row.TimestampMs >= windowStart {
				selected = append(selected, row)
			}
		}
	}

	if len(selected) == 0 {
		if baseline == 0 {
			return Report{}, fmt.Errorf("ingest: no messages for a previously-seen talker fall inside the rescan window (watermark %d ms) yet the stored table is empty; the watermark and the messages disagree", windowStart)
		}
		return Report{WindowStartMs: windowStart}, nil
	}

	result, err := s.UpsertMessages(ctx, source, selected)
	if err != nil {
		return Report{}, fmt.Errorf("ingest: store messages: %w", err)
	}
	if result.Inserted+result.Updated == 0 && baseline == 0 {
		return Report{}, fmt.Errorf("ingest: %d message(s) for this conversation were submitted but none could be stored, and the stored table was empty — message identities are collapsing onto each other; refusing to report this import as success", len(selected))
	}

	newest := selected[0]
	for _, row := range selected[1:] {
		if row.TimestampMs > newest.TimestampMs {
			newest = row
		}
	}
	if err := s.SaveIngestState(ctx, source, talker, newest.TimestampMs, newest.UniqueKey); err != nil {
		return Report{}, fmt.Errorf("ingest: %w", err)
	}

	var displayName string
	for _, row := range rows {
		if row.Extra != nil {
			if cr, ok := row.Extra["chat_room"].(string); ok && cr != "" && cr != talker {
				displayName = cr
				break
			}
			if !row.IsSelf {
				if s, ok := row.Extra["sender"].(string); ok && s != "" && s != talker {
					displayName = s
					break
				}
			}
		}
	}
	if displayName != "" {
		_ = s.SaveContact(ctx, store.Contact{
			WxID:     talker,
			Nickname: displayName,
			IsSelf:   false,
		})
	}

	return Report{
		Inserted:      result.Inserted,
		Skipped:       result.Skipped,
		Updated:       result.Updated,
		WindowStartMs: windowStart,
	}, nil
}

// rescanStart computes where this import should start reading.
func rescanStart(ctx context.Context, s *store.Store, source, talker string) (int64, bool, error) {
	lastTs, ok, err := s.IngestState(ctx, source, talker)
	if err != nil {
		return 0, false, fmt.Errorf("ingest: %w", err)
	}
	if !ok {
		return 0, false, nil
	}
	return lastTs - RescanWindowMs, true, nil
}

// groupByTalker buckets the parsed rows by conversation, keeping export order
// inside each bucket so the newest row is found by a simple scan.
func groupByTalker(rows []model.CanonicalMessage) map[string][]model.CanonicalMessage {
	grouped := make(map[string][]model.CanonicalMessage)
	for _, row := range rows {
		grouped[row.Talker] = append(grouped[row.Talker], row)
	}
	return grouped
}
