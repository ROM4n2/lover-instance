package main

// The model menu: three states, one download path, one removal that needs --yes.
//
// Every test here is offline. The registry is injected, the cache root is a
// temporary directory and the download seam is a function that fails the test if
// it is reached — which is how "listing downloads nothing" is asserted rather
// than assumed.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	configBytes  = `{"id2label": {"0": "Negative", "1": "Positive"}}`
	weightsBytes = "onnx-weights-bytes"
	pinnedSHA    = "5e0e07161a093d7134f0a3293ae6c3c6e9bae2d73c6bfbba65ae26e3a512f466"
)

func digestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// cannedRegistry is the table the Python module reports, with three shapes: a
// model that is installed and verified, one that was never fetched, and one whose
// file on disk is not the bytes that were pinned.
func cannedRegistry(cacheRoot string) registry {
	files := map[string]string{
		"config.json":          digestOf(configBytes),
		"model_quantized.onnx": digestOf(weightsBytes),
	}
	return registry{
		CacheRoot: cacheRoot,
		Models: []modelEntry{
			{Version: "installed-v1", Name: "installed", Revision: "main", Files: files},
			{Version: "absent-v1", Name: "absent", Revision: "main", Files: files},
			{Version: "mismatch-v1", Name: "mismatch", Revision: "main", Files: files},
		},
	}
}

// seedCache writes the three shapes into cacheRoot: the installed model has the
// right bytes, the mismatched one has the wrong ones, the absent one has nothing.
func seedCache(t *testing.T, reg registry) {
	t.Helper()
	for _, name := range []string{"installed", "mismatch"} {
		dir := filepath.Join(reg.CacheRoot, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for file := range reg.Models[0].Files {
			body := configBytes
			if file != "config.json" {
				body = weightsBytes
			}
			if name == "mismatch" && file != "config.json" {
				body = "bytes that were never reviewed"
			}
			if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600); err != nil {
				t.Fatalf("write %s/%s: %v", name, file, err)
			}
		}
	}
}

// stubRegistry hands the model commands a fixed table.
func stubRegistry(t *testing.T, reg registry) {
	t.Helper()
	restore := fetchRegistry
	fetchRegistry = func(context.Context, string, string) (registry, error) { return reg, nil }
	t.Cleanup(func() { fetchRegistry = restore })
}

// stubDownload fails the test if a download is attempted.
func stubDownload(t *testing.T) {
	t.Helper()
	restore := ensureModel
	ensureModel = func(context.Context, string, string, string) (ensureReport, error) {
		t.Error("a download was attempted; the model menu must not install anything on its own")
		return ensureReport{}, fmt.Errorf("no download expected")
	}
	t.Cleanup(func() { ensureModel = restore })
}

func TestModelListShowsTheThreeStatesAndDownloadsNothing(t *testing.T) {
	newRepo(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	seedCache(t, reg)
	stubRegistry(t, reg)
	stubDownload(t)

	got := invoke(t, context.Background(), "model", "list")

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	for _, want := range []string{
		"installed-v1", "installed",
		"absent-v1", "not installed",
		"mismatch-v1", "sha mismatch",
		"files match their pin",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, got.stdout)
		}
	}
	// The mismatched model's detail names the file and both digests, so the fix
	// is visible without re-hashing anything by hand.
	if !strings.Contains(got.stdout, "model_quantized.onnx: expected") {
		t.Errorf("the mismatch detail does not name the file and the digests: %q", got.stdout)
	}
}

func TestModelVerifyReportsOnlyTheProblems(t *testing.T) {
	newRepo(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	seedCache(t, reg)
	stubRegistry(t, reg)
	stubDownload(t)

	got := invoke(t, context.Background(), "model", "verify")

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if strings.Contains(got.stdout, "absent-v1") {
		t.Errorf("verify lists a model that was never installed: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "mismatch-v1") {
		t.Errorf("verify does not list the model whose digest is wrong: %q", got.stdout)
	}
}

func TestModelVerifyIsQuietWhenEverythingMatches(t *testing.T) {
	newRepo(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	// Only the installed model exists on disk; the others are absent, and an
	// absent model is not a problem for verify — nothing claims to be ready.
	seedCache(t, reg)
	if err := os.RemoveAll(filepath.Join(reg.CacheRoot, "mismatch")); err != nil {
		t.Fatal(err)
	}
	stubRegistry(t, reg)

	got := invoke(t, context.Background(), "model", "verify")

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d", got.code, exitOK)
	}
	if !strings.Contains(got.stdout, "every installed model matches") {
		t.Errorf("stdout does not report a clean cache: %q", got.stdout)
	}
}

// TestModelAddReportsThePathAndTheDigests is the download path: it says which
// files it fetched and the digest each one now has.
func TestModelAddReportsThePathAndTheDigests(t *testing.T) {
	newRepo(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	stubRegistry(t, reg)

	restore := ensureModel
	ensureModel = func(_ context.Context, _, _, version string) (ensureReport, error) {
		return ensureReport{
			Version: version,
			Path:    filepath.Join(reg.CacheRoot, "installed"),
			Fetched: []string{"model_quantized.onnx", "config.json"},
			Files: []ensuredFile{
				{Name: "config.json", SHA256: digestOf(configBytes)},
				{Name: "model_quantized.onnx", SHA256: digestOf(weightsBytes)},
			},
		}, nil
	}
	t.Cleanup(func() { ensureModel = restore })

	got := invoke(t, context.Background(), "model", "add", "installed-v1")

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	for _, want := range []string{
		"downloaded model_quantized.onnx, config.json",
		filepath.Join(reg.CacheRoot, "installed"),
		digestOf(weightsBytes),
		"--sentiment-version installed-v1",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, got.stdout)
		}
	}
}

func TestModelAddRefusesAnUnknownVersion(t *testing.T) {
	newRepo(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	stubRegistry(t, reg)
	stubDownload(t)

	got := invoke(t, context.Background(), "model", "add", "not-a-model")

	if got.code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "installed-v1") {
		t.Errorf("the refusal does not name the configured versions: %q", got.stderr)
	}
}

// TestModelRemoveRequiresYes is the guard on the one destructive path: without
// --yes it prints what would go and deletes nothing.
func TestModelRemoveRequiresYes(t *testing.T) {
	newRepo(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	seedCache(t, reg)
	stubRegistry(t, reg)
	dir := filepath.Join(reg.CacheRoot, "installed")

	refused := invoke(t, context.Background(), "model", "rm", "installed-v1")
	if refused.code != exitUsage {
		t.Fatalf("exit code without --yes = %d, want %d; stderr:\n%s", refused.code, exitUsage, refused.stderr)
	}
	if !strings.Contains(refused.stdout, "would remove") {
		t.Errorf("the refusal does not say what would be removed: %q", refused.stdout)
	}
	if !strings.Contains(refused.stdout, dir) {
		t.Errorf("the refusal does not name the directory: %q", refused.stdout)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the cache was removed without --yes: %v", err)
	}

	removed := invoke(t, context.Background(), "model", "rm", "installed-v1", "--yes")
	if removed.code != exitOK {
		t.Fatalf("exit code with --yes = %d, want %d; stderr:\n%s", removed.code, exitOK, removed.stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the cache survived --yes: %v", err)
	}
}

func TestModelRemoveOfSomethingAbsentIsNotAnError(t *testing.T) {
	newRepo(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	stubRegistry(t, reg)

	got := invoke(t, context.Background(), "model", "rm", "absent-v1", "--yes")

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d", got.code, exitOK)
	}
	if !strings.Contains(got.stdout, "nothing to remove") {
		t.Errorf("stdout does not say the model was not installed: %q", got.stdout)
	}
}

func TestModelRemoveRefusesAPathOutsideTheCache(t *testing.T) {
	newRepo(t)
	stubRegistryModules(t)
	reg := cannedRegistry(t.TempDir())
	reg.Models[0].Name = ".."
	stubRegistry(t, reg)

	got := invoke(t, context.Background(), "model", "rm", "installed-v1", "--yes")

	if got.code == exitOK {
		t.Fatalf("exit code = %d, want a refusal: %q", got.code, got.stdout)
	}
	if !strings.Contains(got.stderr, "refusing to remove") {
		t.Errorf("stderr does not explain the refusal: %q", got.stderr)
	}
}

// modelCacheUnderLocalAppData is the cache root as the real registry reports it:
// under LOCALAPPDATA, beside the database, which is what makes "nothing
// installed" the ordinary state of a machine that has not asked for a model.
func modelCacheUnderLocalAppData(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	return filepath.Join(root, "lover-instance", "models")
}

// singleModelRegistry is a table with one version pinning the two files every
// test here writes.
func singleModelRegistry(cacheRoot string) registry {
	return registry{
		CacheRoot: cacheRoot,
		Models: []modelEntry{{
			Version:  "erlangshen-110m-v1",
			Name:     "erlangshen",
			Revision: "main",
			Files: map[string]string{
				"config.json":          digestOf(configBytes),
				"model_quantized.onnx": digestOf(weightsBytes),
			},
		}},
	}
}

// listOneModel runs `lover model list` against one model in the given cache and
// returns what it printed.
func listOneModel(t *testing.T, cacheRoot string) invocation {
	t.Helper()
	newRepo(t)
	stubRegistryModules(t)
	stubRegistry(t, singleModelRegistry(cacheRoot))
	stubDownload(t)
	return invoke(t, context.Background(), "model", "list")
}

// TestModelDetailSaysNothingWasInstalled is the wording that was wrong: an empty
// directory is how a machine that never asked for a model looks, not how an
// interrupted download looks. The old line blamed an add that never happened.
func TestModelDetailSaysNothingWasInstalled(t *testing.T) {
	cache := modelCacheUnderLocalAppData(t)

	got := listOneModel(t, cache)

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stdout, "nothing installed") {
		t.Errorf("stdout does not say nothing is installed: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "has never been fetched") {
		t.Errorf("stdout does not say the version was never fetched: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "lover model add erlangshen-110m-v1") {
		t.Errorf("stdout does not name the command that installs it: %q", got.stdout)
	}
	// The attribution that was wrong, stated as what it must never say again:
	// no files on disk is not evidence of an interrupted download.
	if strings.Contains(got.stdout, "interrupted") {
		t.Errorf("stdout blames an interrupted add on an empty cache: %q", got.stdout)
	}
}

// TestModelDetailSaysAnAddDidNotFinish is the case the interrupted wording was
// actually about: part of the model is on disk and part of it is not.
func TestModelDetailSaysAnAddDidNotFinish(t *testing.T) {
	cache := modelCacheUnderLocalAppData(t)
	dir := filepath.Join(cache, "erlangshen")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// One of the two pinned files, with the right bytes: the download stopped
	// after the first one.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(configBytes), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	got := listOneModel(t, cache)

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stdout, "1 of 2 files present") {
		t.Errorf("stdout does not say how far the install got: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "did not finish") {
		t.Errorf("stdout does not say the last add did not finish: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "lover model add erlangshen-110m-v1") {
		t.Errorf("stdout does not name the command that finishes it: %q", got.stdout)
	}
	if strings.Contains(got.stdout, "nothing installed") {
		t.Errorf("stdout reports a half-present model as one that was never fetched: %q", got.stdout)
	}
}

// TestModelDetailSaysTheBytesDoNotMatchThePin is the third state: every file is
// there and at least one is not the bytes that were reviewed. Nothing was
// interrupted here, and the fix is to look or to fetch again, in that order.
func TestModelDetailSaysTheBytesDoNotMatchThePin(t *testing.T) {
	cache := modelCacheUnderLocalAppData(t)
	dir := filepath.Join(cache, "erlangshen")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for name, body := range map[string]string{
		"config.json":          configBytes,
		"model_quantized.onnx": "bytes that were never reviewed",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	got := listOneModel(t, cache)

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stdout, "sha mismatch") {
		t.Errorf("stdout does not report the mismatch: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "not the bytes that were pinned") {
		t.Errorf("stdout does not say the files are there but wrong: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "lover model verify") {
		t.Errorf("stdout does not point at verify: %q", got.stdout)
	}
	if !strings.Contains(got.stdout, "lover model add erlangshen-110m-v1") {
		t.Errorf("stdout does not say how to fetch them again: %q", got.stdout)
	}
	if strings.Contains(got.stdout, "interrupted") {
		t.Errorf("stdout blames an interrupted add for a wrong digest: %q", got.stdout)
	}
}

// TestModelListHashesWhatIsOnDisk pins the state decision itself: a file whose
// name is right and whose bytes are wrong is a mismatch, not an installation.
func TestModelListHashesWhatIsOnDisk(t *testing.T) {
	reg := cannedRegistry(t.TempDir())
	seedCache(t, reg)

	byVersion := map[string]modelStatus{}
	for _, status := range modelStatuses(reg, reg.CacheRoot) {
		byVersion[status.entry.Version] = status
	}
	if got := byVersion["installed-v1"].state(); got != "installed" {
		t.Errorf("installed-v1 state = %q, want installed", got)
	}
	if got := byVersion["absent-v1"].state(); got != "not installed" {
		t.Errorf("absent-v1 state = %q, want not installed", got)
	}
	if got := byVersion["mismatch-v1"].state(); got != "sha mismatch" {
		t.Errorf("mismatch-v1 state = %q, want sha mismatch", got)
	}
	if byVersion["mismatch-v1"].installed() {
		t.Error("a mismatched model reports itself as installed")
	}
}

// TestShortDigestKeepsAMessageReadable is trivial but it is what a human reads.
func TestShortDigestKeepsAMessageReadable(t *testing.T) {
	if got := shortDigest(pinnedSHA); len(got) >= len(pinnedSHA) {
		t.Errorf("shortDigest(%q) = %q, want a shorter form", pinnedSHA, got)
	}
}
