package narrative

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// CachedProvider is a Provider that remembers what it was told, keyed by digest.
//
// It is a distinct interface from Provider rather than a wrapper around it: the
// cache needs to know the digest before it can decide whether to call the inner
// provider, and the inner Provider only ever sees the prompt. Rather than push
// the digest into every Provider implementation, Generate here takes it
// explicitly. There is exactly one caching shape in this package; do not add a
// second.
type CachedProvider interface {
	Generate(ctx context.Context, digest, prompt string) (string, error)
}

// CachePath is where the narration for digest lives.
func CachePath(dir, digest string) string {
	return filepath.Join(dir, digest+".md")
}

// Cached wraps inner with an on-disk cache rooted at dir.
//
// A non-empty cache file is returned directly and inner is never called. A miss
// calls inner and writes the result only on success, so a failed generation
// never leaves a poisoned entry behind.
func Cached(inner Provider, dir string) CachedProvider {
	return &cachedProvider{inner: inner, dir: dir}
}

type cachedProvider struct {
	inner Provider
	dir   string
}

// Generate returns the cached narration when present, otherwise asks inner and
// stores a copy.
func (c *cachedProvider) Generate(ctx context.Context, digest, prompt string) (string, error) {
	path := CachePath(c.dir, digest)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return string(data), nil
	}
	text, err := c.inner.Generate(ctx, prompt)
	if err != nil {
		return "", err
	}
	if err := writeAtomically(path, []byte(text)); err != nil {
		return "", err
	}
	return text, nil
}

// writeAtomically writes next to the final name and renames it into place, the
// same shape the report and export writers use: a crash mid-write must not
// leave a truncated file that a later run would treat as a finished narration.
func writeAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("narrative: create cache directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("narrative: write cache: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("narrative: move cache into place: %w", err)
	}
	return nil
}
