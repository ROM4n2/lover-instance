package probe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// healthyBody is the probe's expected sidecar /api/v1/session payload shape.
// M0 (Task 5) validates it against the real sidecar; the field names here are
// probe heuristics, never guesses promoted to canonical schema.
const healthyBody = `{"success":true,"data":{"talker_count":7}}`

func newSessionServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// closedListenAddr returns a host:port that nothing listens on: a listener is
// opened to claim a port, then closed, so a subsequent dial is refused.
func closedListenAddr(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.Listener.Addr().String()
	srv.Close()
	return addr
}

func TestProbeHTTP_HealthySessionClassifiesOK(t *testing.T) {
	srv := newSessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/session" {
			t.Errorf("path = %s, want /api/v1/session", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, healthyBody)
	})

	res, err := ProbeHTTP(srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("ProbeHTTP(healthy) error: %v", err)
	}
	if res == nil {
		t.Fatal("ProbeHTTP(healthy) returned nil result")
	}
	if res.State != StateOK {
		t.Errorf("State = %q, want %q", res.State, StateOK)
	}
	if res.HTTPStatus != http.StatusOK {
		t.Errorf("HTTPStatus = %d, want 200", res.HTTPStatus)
	}
	if res.TalkerCount != 7 {
		t.Errorf("TalkerCount = %d, want 7", res.TalkerCount)
	}
	if res.RawBody != healthyBody {
		t.Errorf("RawBody = %q, want raw response captured verbatim", res.RawBody)
	}
	if res.Advice == "" || res.Advice != res.State.Hint() {
		t.Errorf("Advice = %q, want the state hint %q", res.Advice, res.State.Hint())
	}
}

func TestProbeHTTP_ConnectionRefusedClassifiesProcessAbsent(t *testing.T) {
	baseURL := "http://" + closedListenAddr(t)

	start := time.Now()
	res, err := ProbeHTTP(baseURL, 2*time.Second)
	if err == nil {
		t.Fatal("ProbeHTTP(refused): want transport error surfaced, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ProbeHTTP(refused) took %v, want fast refusal", elapsed)
	}
	if res == nil {
		t.Fatal("ProbeHTTP(refused): want classified result alongside error, got nil")
	}
	if res.State != StateProcessAbsent {
		t.Errorf("State = %q, want %q", res.State, StateProcessAbsent)
	}
	if res.Error == "" {
		t.Error("ProbeResult.Error empty: transport error was swallowed")
	}
	if res.Advice == "" {
		t.Error("Advice empty: connection-refused outcome has no guidance")
	}
}

func TestProbeHTTP_TimeoutClassifiesProcessAbsent(t *testing.T) {
	srv := newSessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Hang without writing headers; return early if the client gives up.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})

	start := time.Now()
	res, err := ProbeHTTP(srv.URL, 150*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("ProbeHTTP(timeout): want transport error surfaced, got nil")
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("ProbeHTTP(timeout) took %v, want bounded by the 150ms client timeout", elapsed)
	}
	if res == nil {
		t.Fatal("ProbeHTTP(timeout): want classified result alongside error, got nil")
	}
	if res.State != StateProcessAbsent {
		t.Errorf("State = %q, want %q", res.State, StateProcessAbsent)
	}
	if !strings.Contains(res.Advice, "超时") {
		t.Errorf("Advice = %q, want timeout-specific guidance (超时)", res.Advice)
	}
}

func TestProbeHTTP_GarbageBodyClassifiesSchemaIncompatible(t *testing.T) {
	const garbage = "<<<this is not json>>>"
	srv := newSessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, garbage)
	})

	res, err := ProbeHTTP(srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("ProbeHTTP(garbage): classification should carry the outcome, got error: %v", err)
	}
	if res == nil {
		t.Fatal("ProbeHTTP(garbage): nil result")
	}
	if res.State != StateSchemaIncompatible {
		t.Errorf("State = %q, want %q", res.State, StateSchemaIncompatible)
	}
	if res.RawBody != garbage {
		t.Errorf("RawBody = %q, want garbage kept verbatim as evidence", res.RawBody)
	}
	if res.Advice == "" {
		t.Error("Advice empty: schema-incompatible outcome has no guidance")
	}
}

func TestProbeHTTP_RawBodyTruncatedTo4KB(t *testing.T) {
	body := `{"success":true,"data":{"talker_count":7},"pad":"` + strings.Repeat("x", 8000) + `"}`
	srv := newSessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	})

	res, err := ProbeHTTP(srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("ProbeHTTP(big body) error: %v", err)
	}
	if res.State != StateOK {
		t.Errorf("State = %q, want %q: truncation must not break classification", res.State, StateOK)
	}
	if len(res.RawBody) > 4096 {
		t.Errorf("len(RawBody) = %d, want <= 4096 (4KB cap)", len(res.RawBody))
	}
	if !strings.HasPrefix(res.RawBody, `{"success":true`) {
		t.Errorf("RawBody prefix = %q, want original body prefix", res.RawBody)
	}
	if res.TalkerCount != 7 {
		t.Errorf("TalkerCount = %d, want 7 parsed from the full body", res.TalkerCount)
	}
}

func TestProbeHTTP_SidecarErrorPayloadsClassifyToSemanticStates(t *testing.T) {
	cases := []struct {
		name string
		body string
		want State
	}{
		{"wechat not running", `{"success":false,"error":"WeChat is not running, please start WeChat"}`, StateWeChatNotRunning},
		{"key unavailable", `{"success":false,"error":"failed to get decryption key from memory"}`, StateKeyUnavailable},
		{"version unsupported", `{"success":false,"error":"unsupported WeChat version 9.9.9.9"}`, StateVersionUnsupported},
		{"unmapped failure falls back to schema incompatible", `{"success":false,"error":"totally unknown failure"}`, StateSchemaIncompatible},
		{"recognized session key without success flag", `{"session":"abc123"}`, StateOK},
		{"empty object is not a verifiable session", `{}`, StateSchemaIncompatible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSessionServer(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.body)
			})

			res, err := ProbeHTTP(srv.URL, 2*time.Second)
			if err != nil {
				t.Fatalf("ProbeHTTP error: %v", err)
			}
			if res.State != tc.want {
				t.Errorf("State = %q, want %q (raw_body=%q)", res.State, tc.want, res.RawBody)
			}
			if res.RawBody != tc.body {
				t.Errorf("RawBody = %q, want %q", res.RawBody, tc.body)
			}
		})
	}
}

func TestProbeHTTP_ZeroTimeoutDefaultsToBoundedClient(t *testing.T) {
	srv := newSessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, healthyBody)
	})

	start := time.Now()
	res, err := ProbeHTTP(srv.URL, 0)
	if err != nil {
		t.Fatalf("ProbeHTTP(timeout=0) error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("ProbeHTTP(timeout=0) took %v: a zero timeout must not disable the client deadline", elapsed)
	}
	if res == nil || res.State != StateOK {
		t.Fatalf("result = %+v, want State %q", res, StateOK)
	}
}

func TestFindWeChatPaths_ContainsCandidateSubstrings(t *testing.T) {
	paths := FindWeChatPaths()
	if len(paths) == 0 {
		t.Fatal("FindWeChatPaths() is empty: candidate scan list required")
	}
	joined := strings.Join(paths, "\n")
	for _, want := range []string{"WeChat.exe", "Weixin.exe", "Program Files (x86)", "WeChat Files"} {
		if !strings.Contains(joined, want) {
			t.Errorf("candidate paths missing %q:\n%s", want, joined)
		}
	}
}

// TestDriveWeChatPaths_ExpandsEveryGivenRoot pins the expansion against an
// explicitly supplied root list. The previous version of this test computed its
// expectation from fixedDriveRoots(), the very function under test, so a
// regression that returned only C:\ still satisfied it — the M0 target-box
// defect (WeChat at D:\Program Files\Tencent\Weixin\Weixin.exe reported as "not
// installed") could be reintroduced with a fully green suite.
func TestDriveWeChatPaths_ExpandsEveryGivenRoot(t *testing.T) {
	roots := []string{`C:\`, `D:\`, `Z:\`}
	joined := strings.Join(driveWeChatPathsFor(roots), "\n")
	for _, root := range roots {
		for _, want := range []string{
			filepath.Join(root, `Program Files\Tencent\Weixin\Weixin.exe`),
			filepath.Join(root, `Program Files\Tencent\WeChat\WeChat.exe`),
			filepath.Join(root, `xwechat_files`),
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("expansion for root %q is missing %q:\n%s", root, want, joined)
			}
		}
	}
}

// repoVolume returns the volume this repository sits on, e.g. "D:". It is an
// anchor independent of the code under test: on the M0 target box that volume
// is D:, where WeChat actually lives.
func repoVolume(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Skipf("os.Getwd: %v", err)
	}
	volume := filepath.VolumeName(cwd)
	if volume == "" {
		t.Skipf("working directory %q carries no volume name", cwd)
	}
	systemVolume := filepath.VolumeName(os.Getenv("SystemRoot"))
	if systemVolume != "" && strings.EqualFold(systemVolume, volume) {
		t.Skipf("repository is on the system drive %s, which the hard-coded defaults already cover; this anchor cannot discriminate here", volume)
	}
	return volume
}

// TestFindWeChatPaths_CoversTheDriveHostingThisRepo is the real guard for the
// cross-drive defect: it asserts a candidate exists on a drive that is known to
// be present for an independent reason (this repo lives there) rather than on
// whatever fixedDriveRoots happens to report.
func TestFindWeChatPaths_CoversTheDriveHostingThisRepo(t *testing.T) {
	volume := repoVolume(t)
	want := filepath.Join(volume+`\`, `Program Files\Tencent\Weixin\Weixin.exe`)
	if joined := strings.Join(FindWeChatPaths(), "\n"); !strings.Contains(joined, want) {
		t.Errorf("FindWeChatPaths() has no candidate on %s (want %q): the scan regressed to system-drive-only", volume, want)
	}
}

// TestFixedDriveRoots_IncludesRepoDrive applies the same independent anchor to
// the enumeration primitive.
func TestFixedDriveRoots_IncludesRepoDrive(t *testing.T) {
	volume := repoVolume(t)
	roots := fixedDriveRoots()
	if len(roots) == 0 {
		t.Fatal("fixedDriveRoots() is empty: drive enumeration found nothing")
	}
	for _, root := range roots {
		if strings.EqualFold(filepath.VolumeName(root), volume) {
			return
		}
	}
	t.Errorf("fixedDriveRoots() = %v, want it to contain %s", roots, volume)
}

// TestFixedDriveRoots_SkipsADriveThatBlocks guards the stall: a disconnected
// mapped network drive makes os.Stat block for the SMB redirector timeout, and
// fixedDriveRoots probes every letter A–Z before detectWeChat can return its
// report. The seam stands in for the wedged share.
func TestFixedDriveRoots_SkipsADriveThatBlocks(t *testing.T) {
	origStat, origTimeout := statRoot, driveStatTimeout
	t.Cleanup(func() { statRoot, driveStatTimeout = origStat, origTimeout })
	driveStatTimeout = 150 * time.Millisecond

	released := make(chan struct{})
	t.Cleanup(func() { close(released) })
	statRoot = func(path string) (os.FileInfo, error) {
		if strings.HasPrefix(path, "Z:") {
			// Stands in for a stat wedged inside the SMB redirector. It gives up
			// eventually rather than never, so an implementation that drops the
			// bound overshoots the assertion below instead of hanging the suite.
			select {
			case <-released:
			case <-time.After(10 * time.Second):
			}
			return nil, os.ErrNotExist
		}
		return os.Stat(path)
	}

	start := time.Now()
	roots := fixedDriveRoots()
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("fixedDriveRoots() took %v with one blocked drive, want it bounded near the %v drive deadline", elapsed, driveStatTimeout)
	}
	for _, root := range roots {
		if strings.HasPrefix(root, "Z:") {
			t.Errorf("fixedDriveRoots() = %v, want a drive that never answered to be dropped", roots)
		}
	}
}

// TestFixedDriveRoots_IncludesSystemDrive pins the enumeration primitive: an
// empty result would silently make FindWeChatPaths() C:-defaults-only again.
func TestFixedDriveRoots_IncludesSystemDrive(t *testing.T) {
	roots := fixedDriveRoots()
	if len(roots) == 0 {
		t.Fatal("fixedDriveRoots() is empty: drive enumeration found nothing")
	}
	want := strings.ToUpper(os.Getenv("SystemDrive"))
	if want == "" {
		want = "C:"
	}
	for _, root := range roots {
		if strings.HasPrefix(strings.ToUpper(root), want) {
			return
		}
	}
	t.Errorf("fixedDriveRoots() = %v, want it to contain the system drive %q", roots, want)
}

func TestCheckPortOpen_ClosedPortFalse(t *testing.T) {
	addr := closedListenAddr(t)
	if CheckPortOpen(addr) {
		t.Errorf("CheckPortOpen(%q) = true, want false for a closed port", addr)
	}
}

func TestCheckPortOpen_OpenPortTrue(t *testing.T) {
	srv := newSessionServer(t, func(w http.ResponseWriter, r *http.Request) {})
	addr := srv.Listener.Addr().String()
	if !CheckPortOpen(addr) {
		t.Errorf("CheckPortOpen(%q) = false, want true for a listening port", addr)
	}
}

func TestStateHints_AllSixStatesHaveDistinctActionableHints(t *testing.T) {
	states := []State{
		StateProcessAbsent,
		StateWeChatNotRunning,
		StateKeyUnavailable,
		StateVersionUnsupported,
		StateSchemaIncompatible,
		StateOK,
	}
	seen := make(map[string]State, len(states))
	for _, s := range states {
		hint := s.Hint()
		if len(hint) < 8 {
			t.Errorf("State %q hint = %q, want actionable text (>=8 chars)", s, hint)
		}
		if prev, dup := seen[hint]; dup {
			t.Errorf("State %q shares hint with %q: %q", s, prev, hint)
		}
		seen[hint] = s
	}
	if len(seen) != 6 {
		t.Errorf("distinct hints = %d, want 6", len(seen))
	}
}

func TestWeChatVersionExe_StubbedPowerShellReadsVersion(t *testing.T) {
	orig := runVersionCmd
	t.Cleanup(func() { runVersionCmd = orig })

	var gotScript string
	runVersionCmd = func(ctx context.Context, script string) ([]byte, error) {
		gotScript = script
		return []byte("9.9.9.9\r\n"), nil
	}

	version, err := WeChatVersionExe(`C:\Program Files\Tencent\WeChat\WeChat.exe`)
	if err != nil {
		t.Fatalf("WeChatVersionExe error: %v", err)
	}
	if version != "9.9.9.9" {
		t.Errorf("version = %q, want %q", version, "9.9.9.9")
	}
	if !strings.Contains(gotScript, `C:\Program Files\Tencent\WeChat\WeChat.exe`) {
		t.Errorf("script = %q, want the exe path in the VersionInfo query", gotScript)
	}
	if !strings.Contains(gotScript, "FileVersion") {
		t.Errorf("script = %q, want VersionInfo.FileVersion lookup", gotScript)
	}
}

func TestWeChatVersionExe_CommandFailureSurfacesError(t *testing.T) {
	orig := runVersionCmd
	t.Cleanup(func() { runVersionCmd = orig })

	runVersionCmd = func(ctx context.Context, script string) ([]byte, error) {
		return nil, fmt.Errorf("powershell not found")
	}

	version, err := WeChatVersionExe(`C:\missing\WeChat.exe`)
	if err == nil {
		t.Fatal("WeChatVersionExe: want error when the command fails, got nil (silent swallow)")
	}
	if version != "" {
		t.Errorf("version = %q, want empty on error", version)
	}
}

func TestWeChatVersionExe_EmptyOutputSurfacesError(t *testing.T) {
	orig := runVersionCmd
	t.Cleanup(func() { runVersionCmd = orig })

	runVersionCmd = func(ctx context.Context, script string) ([]byte, error) {
		return []byte("  \r\n"), nil
	}

	version, err := WeChatVersionExe(`C:\Program Files\Tencent\WeChat\WeChat.exe`)
	if err == nil {
		t.Fatal("WeChatVersionExe: want error on empty version output, got nil (silent swallow)")
	}
	if version != "" {
		t.Errorf("version = %q, want empty on error", version)
	}
}

// TestWeChatVersionExe_HungPowerShellBoundedByDeadline guards defect D1: a
// PowerShell stalled on broken WMI/AV must not hang detectWeChat — it runs
// before ProbeHTTP, so an unbounded hang would keep the CLI from ever
// emitting its single JSON line. The seam must receive a ctx carrying the
// package deadline; a stub that blocks far past it must surface a
// DeadlineExceeded error quickly (test bound 1s, never the 10s production
// wait).
func TestWeChatVersionExe_HungPowerShellBoundedByDeadline(t *testing.T) {
	orig := runVersionCmd
	origTimeout := versionCmdTimeout
	t.Cleanup(func() {
		runVersionCmd = orig
		versionCmdTimeout = origTimeout
	})
	versionCmdTimeout = 1 * time.Second // fast test bound; never wait out 10s here

	var gotDeadline time.Time
	var gotDeadlineOK bool
	runVersionCmd = func(ctx context.Context, script string) ([]byte, error) {
		gotDeadline, gotDeadlineOK = ctx.Deadline()
		select {
		case <-time.After(5 * time.Second): // hung shell: far longer than the 1s deadline
			return []byte("9.9.9.9"), nil
		case <-ctx.Done(): // what exec.CommandContext does: killed at the deadline
			return nil, ctx.Err()
		}
	}

	start := time.Now()
	version, err := WeChatVersionExe(`C:\Program Files\Tencent\WeChat\WeChat.exe`)
	elapsed := time.Since(start)

	if elapsed >= 3*time.Second {
		t.Errorf("WeChatVersionExe took %v, want it bounded by the %v deadline (hung PowerShell must not stall the probe)", elapsed, versionCmdTimeout)
	}
	if !gotDeadlineOK {
		t.Error("runVersionCmd received no ctx deadline: exec.CommandContext could never kill a hung PowerShell")
	} else {
		lower, upper := start.Add(versionCmdTimeout/2), start.Add(versionCmdTimeout+time.Second)
		if gotDeadline.Before(lower) || gotDeadline.After(upper) {
			t.Errorf("deadline %v outside [%v, %v]: want ~versionCmdTimeout (%v) measured from call start", gotDeadline, lower, upper, versionCmdTimeout)
		}
	}
	if err == nil {
		t.Errorf("WeChatVersionExe returned version %q, nil error after %v: a hung PowerShell must surface a deadline error", version, elapsed)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want wrapping context.DeadlineExceeded", err)
	}
	if version != "" {
		t.Errorf("version = %q, want empty when the version read misses its deadline", version)
	}
}
