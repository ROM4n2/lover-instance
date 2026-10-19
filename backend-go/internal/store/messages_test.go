package store

import (
	"context"
	"testing"

	"lover-instance/backend-go/internal/model"
)

// message builds a canonical message whose only interesting field is the one
// the test is about. The UniqueKey is always supplied, mirroring the contract
// model.ParseRaw enforces.
func message(key, text string, isSelf bool) model.CanonicalMessage {
	return model.CanonicalMessage{
		UniqueKey:   key,
		TimestampMs: 1756225000000,
		Talker:      "wxid_talker",
		Sender:      "wxid_sender",
		IsSelf:      isSelf,
		Type:        1,
		Content:     text,
	}
}

// migratedStore opens a fresh database with the schema in place.
func migratedStore(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t, t.TempDir(), "data.db")
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	return s
}

func TestUpsertMessages_SecondRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	batch := []model.CanonicalMessage{
		message("k1", "hello", true),
		message("k2", "world", false),
		message("k3", "again", true),
	}

	first, err := s.UpsertMessages(ctx, "chatlog-keeper", batch)
	if err != nil {
		t.Fatalf("first UpsertMessages() error: %v", err)
	}
	if first.Inserted != 3 || first.Updated != 0 || first.Skipped != 0 {
		t.Errorf("first run = %+v, want {Inserted:3 Skipped:0 Updated:0}", first)
	}

	second, err := s.UpsertMessages(ctx, "chatlog-keeper", batch)
	if err != nil {
		t.Fatalf("second UpsertMessages() error: %v", err)
	}
	if second.Inserted != 0 || second.Updated != 0 || second.Skipped != 3 {
		t.Errorf("second run = %+v, want {Inserted:0 Skipped:3 Updated:0}", second)
	}

	count, err := s.CountMessages(ctx, "wxid_talker")
	if err != nil {
		t.Fatalf("CountMessages() error: %v", err)
	}
	if count != 3 {
		t.Errorf("stored messages = %d, want 3", count)
	}
}

// TestUpsertMessages_AccountsForEveryMessage is the no-silent-loss invariant:
// every submitted message must be inserted, refreshed, or recognized. A batch
// that quietly dropped a row would still look successful without this.
func TestUpsertMessages_AccountsForEveryMessage(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	batch := []model.CanonicalMessage{
		message("k1", "one", true),
		message("k2", "two", false),
		message("k3", "three", true),
		message("k4", "four", false),
	}
	if _, err := s.UpsertMessages(ctx, "chatlog-keeper", batch); err != nil {
		t.Fatalf("first UpsertMessages() error: %v", err)
	}

	// A second batch that mixes a repeat, a changed repeat, and a new row.
	mixed := []model.CanonicalMessage{
		message("k1", "one", true),          // unchanged repeat
		message("k2", "two revised", false), // same key, new body
		message("k5", "five", true),         // new
	}
	got, err := s.UpsertMessages(ctx, "chatlog-keeper", mixed)
	if err != nil {
		t.Fatalf("mixed UpsertMessages() error: %v", err)
	}
	if got.Inserted+got.Skipped+got.Updated != len(mixed) {
		t.Errorf("accounting %+v sums to %d, want %d", got, got.Inserted+got.Skipped+got.Updated, len(mixed))
	}
	if got.Inserted != 1 || got.Updated != 1 || got.Skipped != 1 {
		t.Errorf("mixed run = %+v, want {Inserted:1 Skipped:1 Updated:1}", got)
	}
}

// TestUpsertMessages_RefreshesAChangedBody covers revocation: WeChat rewrites a
// message's text, so a later import must carry the new text rather than keep
// serving the stale one.
func TestUpsertMessages_RefreshesAChangedBody(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	if _, err := s.UpsertMessages(ctx, "chatlog-keeper", []model.CanonicalMessage{message("k1", "the original text", false)}); err != nil {
		t.Fatalf("first UpsertMessages() error: %v", err)
	}

	got, err := s.UpsertMessages(ctx, "chatlog-keeper", []model.CanonicalMessage{message("k1", "the sender recalled a message", false)})
	if err != nil {
		t.Fatalf("second UpsertMessages() error: %v", err)
	}
	if got.Updated != 1 {
		t.Errorf("Updated = %d, want 1", got.Updated)
	}

	var content, hash string
	err = s.db.QueryRowContext(ctx, `SELECT content, content_sha1 FROM messages WHERE source_msg_id = ?`, "k1").Scan(&content, &hash)
	if err != nil {
		t.Fatalf("read back message: %v", err)
	}
	if content != "the sender recalled a message" {
		t.Errorf("stored content = %q, want the refreshed body", content)
	}
	if hash != ContentSHA1("the sender recalled a message") {
		t.Errorf("stored content_sha1 = %q, want the hash of the refreshed body", hash)
	}
}

// TestUpsertMessages_RejectsAKeylessMessage pins the contract UpsertMessages
// relies on: ParseRaw never emits an empty key. If a future adapter broke that,
// every such row would collide on the unique index and collapse into one —
// this is the assertion that makes that failure loud instead of silent.
func TestUpsertMessages_RejectsAKeylessMessage(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	keyless := message("", "no identity", false)

	if _, err := s.UpsertMessages(ctx, "chatlog-keeper", []model.CanonicalMessage{keyless}); err == nil {
		t.Fatal("UpsertMessages() = nil error for a message with an empty unique key, want an error")
	}
	count, err := s.CountMessages(ctx, "wxid_talker")
	if err != nil {
		t.Fatalf("CountMessages() error: %v", err)
	}
	if count != 0 {
		t.Errorf("stored messages = %d after a rejected batch, want 0 (the batch must roll back)", count)
	}
}

func TestUpsertMessages_EmptyBatchIsNotAnError(t *testing.T) {
	got, err := migratedStore(t).UpsertMessages(context.Background(), "chatlog-keeper", nil)
	if err != nil {
		t.Fatalf("UpsertMessages(nil) error: %v", err)
	}
	if got != (InsertResult{}) {
		t.Errorf("UpsertMessages(nil) = %+v, want the zero result", got)
	}
}

func TestContentSHA1_IgnoresSurroundingWhitespace(t *testing.T) {
	if ContentSHA1("a") != ContentSHA1("  a \n") {
		t.Error("ContentSHA1 differs for a padded and an unpadded body: a re-export would read as a content change")
	}
	if ContentSHA1("a") == ContentSHA1("b") {
		t.Error("ContentSHA1 collides for different bodies")
	}
}

func TestListTalkers_ReturnsEachConversationOnce(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	batch := []model.CanonicalMessage{
		message("k1", "hi", true),
		message("k2", "hi", false),
	}
	other := message("k3", "hi", true)
	other.Talker = "wxid_other"
	batch = append(batch, other)

	if _, err := s.UpsertMessages(ctx, "chatlog-keeper", batch); err != nil {
		t.Fatalf("UpsertMessages() error: %v", err)
	}
	talkers, err := s.ListTalkers(ctx)
	if err != nil {
		t.Fatalf("ListTalkers() error: %v", err)
	}
	if len(talkers) != 2 || talkers[0] != "wxid_other" || talkers[1] != "wxid_talker" {
		t.Errorf("ListTalkers() = %v, want [wxid_other wxid_talker] in sorted order", talkers)
	}
}

func TestCountMessages_IsScopedToTheTalker(t *testing.T) {
	ctx := context.Background()
	s := migratedStore(t)
	if _, err := s.UpsertMessages(ctx, "chatlog-keeper", []model.CanonicalMessage{message("k1", "hi", true)}); err != nil {
		t.Fatalf("UpsertMessages() error: %v", err)
	}
	got, err := s.CountMessages(ctx, "wxid_nobody")
	if err != nil {
		t.Fatalf("CountMessages() error: %v", err)
	}
	if got != 0 {
		t.Errorf("CountMessages(wxid_nobody) = %d, want 0", got)
	}
}
