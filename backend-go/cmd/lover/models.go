package main

// `lover model`: the explicit model menu.
//
// Design decision D2 is that a model is something a person asks for, not
// something a first run downloads: the dictionary baseline scores without a
// single byte off the network, and a 100 MB weight is an upgrade with a reason
// behind it. So this command is a menu first and a download second, and the
// listing half never touches the network at all — `list` hashes what is already
// on disk and stops there.
//
// The version table has one source of truth: nlp-service/app/model_registry.py.
// It is read by running `python -m app.model_cli registry`, which imports that
// module and prints its MODELS table as JSON. Nothing here holds a second copy:
// adding a model to the registry is the whole act of adding it to this menu, and
// a copy in Go would drift the first time a pin changed.
//
// The three states are the answer to "can I score with this?", and they are
// deliberately not a boolean. "Installed" means every pinned file is present with
// the digest the registry demands; "not installed" means at least one is absent;
// "mismatch" means a file is there and is not the bytes that were reviewed —
// usually a truncated download, and the one state that must never be read as
// ready.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"lover-instance/backend-go/internal/app"
)

// registry is the version table as app/model_cli.py reports it.
type registry struct {
	CacheRoot string       `json:"cache_root"`
	Models    []modelEntry `json:"models"`
}

// modelEntry is one configured model: its version name, the directory its
// weights live in, and the digest every file must have.
type modelEntry struct {
	Version  string            `json:"version"`
	Name     string            `json:"name"`
	Revision string            `json:"revision"`
	Files    map[string]string `json:"files"`
}

// fetchRegistry is the seam a test replaces, so a listing can be checked without
// a Python interpreter on the machine running the test.
var fetchRegistry = defaultFetchRegistry

// defaultFetchRegistry asks the service's own module for the table.
func defaultFetchRegistry(ctx context.Context, python, serviceDir string) (registry, error) {
	cmd := exec.CommandContext(ctx, python, "-m", "app.model_cli", "registry")
	cmd.Dir = serviceDir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return registry{}, fmt.Errorf("could not read the model registry with %s (%v): %s", python, err, strings.TrimSpace(stderr.String()))
	}
	var reg registry
	if err := json.Unmarshal(out, &reg); err != nil {
		return registry{}, fmt.Errorf("the model registry output is not JSON (%v)", err)
	}
	if len(reg.Models) == 0 {
		return registry{}, fmt.Errorf("the model registry is empty, which means no model is configured; check nlp-service/app/model_registry.py")
	}
	return reg, nil
}

// fileStatus is one pinned file as it stands on disk right now.
type fileStatus struct {
	name     string
	expected string
	present  bool
	actual   string
}

// modelStatus is one model's state, computed by hashing its files.
type modelStatus struct {
	entry modelEntry
	dir   string
	files []fileStatus
}

// modelStatuses hashes every configured model's files under cacheRoot. It is the
// only place a model's state is decided, and it makes no request: the digest of a
// file on disk is the answer, and "the name is there" is not.
func modelStatuses(reg registry, cacheRoot string) []modelStatus {
	statuses := make([]modelStatus, 0, len(reg.Models))
	for _, entry := range reg.Models {
		dir := filepath.Join(cacheRoot, entry.Name)
		status := modelStatus{entry: entry, dir: dir}
		for _, name := range sortedNames(entry.Files) {
			file := fileStatus{name: name, expected: entry.Files[name]}
			if actual, err := sha256File(filepath.Join(dir, name)); err == nil {
				file.present = true
				file.actual = actual
			}
			status.files = append(status.files, file)
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// state is the three-way answer. A model with some files present and some absent
// is not installed — it cannot score — and the detail column says how far an
// interrupted add got.
func (m modelStatus) state() string {
	present, mismatched := 0, 0
	for _, file := range m.files {
		switch {
		case !file.present:
		case file.actual != file.expected:
			mismatched++
		default:
			present++
		}
	}
	switch {
	case mismatched > 0:
		return "sha mismatch"
	case present == 0:
		return "not installed"
	case present < len(m.files):
		return "not installed"
	default:
		return "installed"
	}
}

// detail is the second column: how many files match, or which one does not.
func (m modelStatus) detail() string {
	present, mismatched := 0, 0
	var bad []string
	for _, file := range m.files {
		switch {
		case !file.present:
		case file.actual != file.expected:
			mismatched++
			bad = append(bad, fmt.Sprintf("%s: expected %s, got %s", file.name, shortDigest(file.expected), shortDigest(file.actual)))
		default:
			present++
		}
	}
	switch {
	case mismatched > 0:
		return " (" + strings.Join(bad, "; ") + ")"
	case present < len(m.files):
		return fmt.Sprintf(" (%d of %d files present, so an earlier add was interrupted; re-run `lover model add %s`)", present, len(m.files), m.entry.Version)
	default:
		return fmt.Sprintf(" (%d/%d files match their pin)", present, len(m.files))
	}
}

// installed reports whether the model can be scored with right now.
func (m modelStatus) installed() bool { return m.state() == "installed" }

// anyFilePresent reports whether anything at all is in this model's directory.
func (m modelStatus) anyFilePresent() bool {
	for _, file := range m.files {
		if file.present {
			return true
		}
	}
	return false
}

// shortDigest is a digest cut to a length a human can compare at a glance.
func shortDigest(digest string) string {
	if len(digest) > 12 {
		return digest[:12] + "…"
	}
	return digest
}

// sha256File is the digest of a file on disk, or an error if it is not there.
func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// sortedNames keeps a file listing stable: the registry's map has no order, and a
// menu that reshuffles itself between two runs looks broken.
func sortedNames(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ensureReport is what app/model_cli.py reports after a download.
type ensureReport struct {
	Version string        `json:"version"`
	Path    string        `json:"path"`
	Fetched []string      `json:"fetched"`
	Files   []ensuredFile `json:"files"`
}

type ensuredFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// ensureModel is the seam a test replaces with a fake download.
var ensureModel = defaultEnsureModel

// defaultEnsureModel runs the service's own ensure path, which downloads only
// what is missing or mismatched, verifies every byte by sha256 and promotes it
// from `<file>.part`.
func defaultEnsureModel(ctx context.Context, python, serviceDir, version string) (ensureReport, error) {
	cmd := exec.CommandContext(ctx, python, "-m", "app.model_cli", "ensure", version)
	cmd.Dir = serviceDir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return ensureReport{}, fmt.Errorf("could not install %s with %s (%v): %s", version, python, err, strings.TrimSpace(stderr.String()))
	}
	var report ensureReport
	if err := json.Unmarshal(out, &report); err != nil {
		return ensureReport{}, fmt.Errorf("the install output is not JSON (%v)", err)
	}
	return report, nil
}

// modelOptions is the parsed `lover model` command line.
type modelOptions struct {
	python     string
	yes        bool
	serviceDir string
}

func parseModelFlags(args []string, stderr io.Writer) (modelOptions, string, []string, error) {
	cfg := modelOptions{python: pythonCommand()}
	fs := flag.NewFlagSet("lover model", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: lover model <list|add|verify|rm> [version] [options]

The model menu. A model is an explicit upgrade, never something a run installs
by itself: the dictionary baseline scores without downloading anything.

  list    show every configured version and whether it is installed; no network
  add     download and verify one version, saying which files it fetched
  verify  re-hash the installed weights and list what does not match its pin
  rm      remove one version's cache directory; needs --yes

The version table is nlp-service/app/model_registry.py, read through the
service's own module: this command holds no copy of it.

Flags:
`)
		fs.PrintDefaults()
	}
	fs.StringVar(&cfg.python, "python", cfg.python, "interpreter used to read the registry and to download")
	fs.BoolVar(&cfg.yes, "yes", false, "confirm a removal without being asked")
	// Go's flag package stops at the first positional argument, and a person
	// types `lover model rm erlangshen --yes` — the flag after the version. A
	// flag set can be parsed more than once and accumulates what it has seen, so
	// stepping over each positional in turn collects the flags that follow it.
	// Without this, --yes would silently do nothing in the one command that
	// needs it.
	var positional []string
	remaining := args
	for {
		if err := fs.Parse(remaining); err != nil {
			return cfg, "", nil, flagParseError{err: err}
		}
		left := fs.Args()
		if len(left) == 0 {
			break
		}
		positional = append(positional, left[0])
		remaining = left[1:]
	}
	if len(positional) == 0 {
		return cfg, "", nil, app.Usagef("a subcommand is required: list, add <version>, verify, or rm <version>")
	}
	return cfg, positional[0], positional[1:], nil
}

// runModel dispatches the model subcommands. Every one of them reads the registry
// first, so an unknown version is refused with the menu named rather than
// guessed at.
func runModel(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, sub, rest, err := parseModelFlags(args, stderr)
	if err != nil {
		return classifyParseError(err, stderr)
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		return exitEnv
	}
	cfg.serviceDir = filepath.Join(root, "nlp-service")

	if err := probeRegistryModules(ctx, cfg.python, cfg.serviceDir); err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		return exitEnv
	}
	reg, err := fetchRegistry(ctx, cfg.python, cfg.serviceDir)
	if err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		return exitRun
	}

	switch sub {
	case "list":
		return modelList(reg, stdout)
	case "verify":
		return modelVerify(reg, stdout)
	case "add":
		return modelAdd(ctx, cfg, reg, rest, stdout, stderr)
	case "rm":
		return modelRemove(cfg, reg, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "lover: unknown model subcommand %q; the choices are list, add <version>, verify and rm <version>\n\n", sub)
		fmt.Fprint(stderr, modelUsage)
		return exitUsage
	}
}

// modelUsage is the usage block a bad subcommand prints.
const modelUsage = `Usage: lover model <list|add|verify|rm> [version] [options]

  list    show every configured version and whether it is installed; no network
  add     download and verify one version, saying which files it fetched
  verify  re-hash the installed weights and list what does not match its pin
  rm      remove one version's cache directory; needs --yes
`

// modelList prints the three states. It is the reason this command exists as a
// menu: what is installed is a fact about this machine, and finding it out must
// not cost a download.
func modelList(reg registry, stdout io.Writer) int {
	fmt.Fprintf(stdout, "model cache: %s\n", reg.CacheRoot)
	for _, status := range modelStatuses(reg, reg.CacheRoot) {
		fmt.Fprintf(stdout, "  %-28s %s%s\n", status.entry.Version, status.state(), status.detail())
	}
	fmt.Fprintf(stdout, "the version table is nlp-service/app/model_registry.py; listing it downloads nothing — `lover model add <version>` is what installs\n")
	return exitOK
}

// modelVerify re-hashes what is installed and reports only the problems. It is
// the same computation as list with a different question: not "what is here" but
// "is what is here still the bytes that were reviewed".
//
// A model with nothing on disk is not a problem here: there is nothing to verify,
// and `list` is the command that answers "what is installed". Reporting an absent
// model as a failed verification would make a clean machine look broken.
func modelVerify(reg registry, stdout io.Writer) int {
	problems := 0
	for _, status := range modelStatuses(reg, reg.CacheRoot) {
		if status.installed() || !status.anyFilePresent() {
			continue
		}
		problems++
		fmt.Fprintf(stdout, "%s: %s%s\n", status.entry.Version, status.state(), status.detail())
	}
	if problems == 0 {
		fmt.Fprintf(stdout, "every installed model matches its pinned digests\n")
	}
	return exitOK
}

// modelAdd is the one path that downloads, and it says what it did: which files
// it fetched, and the digest each one now has.
func modelAdd(ctx context.Context, cfg modelOptions, reg registry, rest []string, stdout, stderr io.Writer) int {
	if len(rest) != 1 {
		fmt.Fprintf(stderr, "lover: `lover model add` takes exactly one version\n\n%s", modelUsage)
		return exitUsage
	}
	version := rest[0]
	entry, ok := findEntry(reg, version)
	if !ok {
		fmt.Fprintf(stderr, "lover: %q is not a configured model version; the choices are %s\n", version, strings.Join(versionNames(reg), ", "))
		return exitUsage
	}
	fmt.Fprintf(stderr, "lover: fetching %s (%s, revision %s) into %s\n", version, entry.Name, entry.Revision, reg.CacheRoot)
	report, err := ensureModel(ctx, cfg.python, cfg.serviceDir, version)
	if err != nil {
		fmt.Fprintf(stderr, "lover: %v\n", err)
		return exitRun
	}
	if len(report.Fetched) == 0 {
		fmt.Fprintf(stdout, "%s: already installed, nothing downloaded\n", version)
	} else {
		fmt.Fprintf(stdout, "%s: downloaded %s\n", version, strings.Join(report.Fetched, ", "))
	}
	for _, file := range report.Files {
		fmt.Fprintf(stdout, "  %-24s sha256 %s\n", file.Name, file.SHA256)
	}
	fmt.Fprintf(stdout, "%s: installed at %s; score with it by passing --sentiment-version %s\n", version, report.Path, version)
	return exitOK
}

// modelRemove deletes a cache directory, and only after --yes. The refusal prints
// what would have gone, because a removal that has to be confirmed is a removal
// whose size the person is entitled to know first.
func modelRemove(cfg modelOptions, reg registry, rest []string, stdout, stderr io.Writer) int {
	if len(rest) != 1 {
		fmt.Fprintf(stderr, "lover: `lover model rm` takes exactly one version\n\n%s", modelUsage)
		return exitUsage
	}
	version := rest[0]
	entry, ok := findEntry(reg, version)
	if !ok {
		fmt.Fprintf(stderr, "lover: %q is not a configured model version; the choices are %s\n", version, strings.Join(versionNames(reg), ", "))
		return exitUsage
	}
	dir := filepath.Join(reg.CacheRoot, entry.Name)
	if !underRoot(reg.CacheRoot, dir) {
		fmt.Fprintf(stderr, "lover: refusing to remove %s: it is not inside the model cache %s\n", dir, reg.CacheRoot)
		return exitRun
	}
	status := modelStatuses(registry{CacheRoot: reg.CacheRoot, Models: []modelEntry{entry}}, reg.CacheRoot)[0]
	present := 0
	for _, file := range status.files {
		if file.present {
			present++
		}
	}
	if present == 0 {
		fmt.Fprintf(stdout, "%s: not installed, nothing to remove\n", version)
		return exitOK
	}
	if !cfg.yes {
		fmt.Fprintf(stdout, "would remove %s (%d of %d pinned files present)\n", dir, present, len(status.files))
		fmt.Fprintf(stdout, "re-run with --yes to delete it: `lover model rm %s --yes`\n", version)
		return exitUsage
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(stderr, "lover: could not remove %s: %v\n", dir, err)
		return exitRun
	}
	fmt.Fprintf(stdout, "%s: removed %s\n", version, dir)
	return exitOK
}

// underRoot is the guard that keeps a registry entry with a strange name from
// turning `rm` into a delete of something else entirely.
func underRoot(root, dir string) bool {
	cleanRoot := filepath.Clean(root)
	cleanDir := filepath.Clean(dir)
	if cleanDir == cleanRoot {
		return false
	}
	return strings.HasPrefix(cleanDir, cleanRoot+string(filepath.Separator))
}

func findEntry(reg registry, version string) (modelEntry, bool) {
	for _, entry := range reg.Models {
		if entry.Version == version {
			return entry, true
		}
	}
	return modelEntry{}, false
}

func versionNames(reg registry) []string {
	names := make([]string, 0, len(reg.Models))
	for _, entry := range reg.Models {
		names = append(names, entry.Version)
	}
	return names
}
