package narrative

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
)

// fakeProvider records how often it is called. boom makes any call an
// immediate, loud failure, which is how a cache hit asserts "zero requests".
type fakeProvider struct {
	calls atomic.Int32
	out   string
	err   error
	boom  bool
}

func (f *fakeProvider) Generate(context.Context, string) (string, error) {
	f.calls.Add(1)
	if f.boom {
		return "", errors.New("provider must not be called on a cache hit")
	}
	return f.out, f.err
}

// TestCached_HitMakesNoRequest proves a cache hit never touches the provider.
func TestCached_HitMakesNoRequest(t *testing.T) {
	dir := t.TempDir()
	const digest = "cached-digest"
	if err := os.WriteFile(CachePath(dir, digest), []byte("缓存内容"), 0o600); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	inner := &fakeProvider{boom: true}
	cache := Cached(inner, dir)

	got, err := cache.Generate(context.Background(), digest, "prompt")
	if err != nil {
		t.Fatalf("Generate() on a cache hit error: %v", err)
	}
	if got != "缓存内容" {
		t.Errorf("Generate() = %q, want %q", got, "缓存内容")
	}
	if n := inner.calls.Load(); n != 0 {
		t.Errorf("inner provider called %d times on a cache hit, want 0", n)
	}
}

// TestCached_MissCallsAndWrites proves a miss calls the provider, writes the
// file, and the second call hits with identical content.
func TestCached_MissCallsAndWrites(t *testing.T) {
	dir := t.TempDir()
	inner := &fakeProvider{out: "第一次生成"}
	cache := Cached(inner, dir)

	first, err := cache.Generate(context.Background(), "d1", "prompt")
	if err != nil {
		t.Fatalf("first Generate() error: %v", err)
	}
	if first != "第一次生成" {
		t.Errorf("first = %q, want %q", first, "第一次生成")
	}
	if n := inner.calls.Load(); n != 1 {
		t.Fatalf("provider called %d times after a miss, want 1", n)
	}
	if _, err := os.Stat(CachePath(dir, "d1")); err != nil {
		t.Errorf("cache file was not written on a miss: %v", err)
	}

	second, err := cache.Generate(context.Background(), "d1", "prompt")
	if err != nil {
		t.Fatalf("second Generate() error: %v", err)
	}
	if second != first {
		t.Errorf("second = %q, want the cached %q", second, first)
	}
	if n := inner.calls.Load(); n != 1 {
		t.Errorf("provider called %d times after two generates, want the second to hit the cache", n)
	}
}

// TestCached_EmptyFileIsNotAHit proves a zero-byte cache file is treated as a
// miss, not as an empty narration.
func TestCached_EmptyFileIsNotAHit(t *testing.T) {
	dir := t.TempDir()
	const digest = "empty-digest"
	if err := os.WriteFile(CachePath(dir, digest), nil, 0o600); err != nil {
		t.Fatalf("seed empty cache: %v", err)
	}

	inner := &fakeProvider{out: "生成结果"}
	cache := Cached(inner, dir)

	got, err := cache.Generate(context.Background(), digest, "prompt")
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if got != "生成结果" {
		t.Errorf("Generate() = %q, want a fresh call result", got)
	}
	if n := inner.calls.Load(); n != 1 {
		t.Errorf("provider called %d times; an empty cache file must force a fresh call", n)
	}
}
