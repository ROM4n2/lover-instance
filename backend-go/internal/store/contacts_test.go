package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestContacts_SaveAndGet(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	dsn, err := DSN(dbPath)
	if err != nil {
		t.Fatalf("DSN error: %v", err)
	}
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	contact := Contact{
		WxID:     "wxid_alice",
		Nickname: "Alice",
		Remark:   "My Alice",
		IsSelf:   false,
	}
	if err := s.SaveContact(ctx, contact); err != nil {
		t.Fatalf("SaveContact error: %v", err)
	}

	got, found, err := s.GetContact(ctx, "wxid_alice")
	if err != nil {
		t.Fatalf("GetContact error: %v", err)
	}
	if !found {
		t.Fatalf("expected contact to be found")
	}
	if got.Nickname != "Alice" || got.Remark != "My Alice" {
		t.Errorf("unexpected contact: %+v", got)
	}
}
