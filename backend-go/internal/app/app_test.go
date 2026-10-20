package app

import "testing"

// TestDefaultOptions_ReadsEveryDefaultFromPaths pins the single source. A
// default that drifted into a literal here is exactly the "one rule, two
// implementations" defect this package exists to prevent: the next entry point
// would fill its own Options, and the two would disagree about where the
// database lives or which service to call.
func TestDefaultOptions_ReadsEveryDefaultFromPaths(t *testing.T) {
	opts := DefaultOptions()

	if opts.DBPath != DefaultDBPath() {
		t.Errorf("DBPath = %q, want DefaultDBPath() = %q", opts.DBPath, DefaultDBPath())
	}
	if opts.NLPURL != DefaultNLPURL() {
		t.Errorf("NLPURL = %q, want DefaultNLPURL() = %q", opts.NLPURL, DefaultNLPURL())
	}
	if opts.OllamaURL != DefaultOllamaURL() {
		t.Errorf("OllamaURL = %q, want DefaultOllamaURL() = %q", opts.OllamaURL, DefaultOllamaURL())
	}
}
