// Package probe implements the M0 WeChat/sidecar detection probe: the plan's
// 6-state sidecar state machine (进程不在 / 微信没跑 / 密钥取不到 / 版本不支持 /
// schema 不兼容 / 正常) with actionable per-state advice and raw-body evidence.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// Plan §Task 3: connect 3s / read 30s for the HTTP probe.
	connectTimeout     = 3 * time.Second
	defaultReadTimeout = 30 * time.Second
	portProbeTimeout   = 3 * time.Second
	// sessionPath is the sidecar endpoint the probe classifies against.
	sessionPath = "/api/v1/session"
	// maxProbeBody bounds how much of the response we read at all.
	maxProbeBody = 4 << 20 // 4MB
	// maxRawBody is the plan's evidence cap: raw_body truncated to <=4KB.
	maxRawBody = 4096
)

// State is one of the sidecar 6-state machine outcomes.
type State string

const (
	StateProcessAbsent      State = "process_absent"
	StateWeChatNotRunning   State = "wechat_not_running"
	StateKeyUnavailable     State = "key_unavailable"
	StateVersionUnsupported State = "version_unsupported"
	StateSchemaIncompatible State = "schema_incompatible"
	StateOK                 State = "ok"
)

// Hint returns the actionable guidance text for a state (plan: 每态有可执行 hint).
func (s State) Hint() string {
	switch s {
	case StateProcessAbsent:
		return "sidecar 进程未监听：请安装并启动 sidecar，确认其监听 http://127.0.0.1:5030（安装步骤与风险见 DISCLAIMER.md / ADR-0001）"
	case StateWeChatNotRunning:
		return "sidecar 在线但微信未运行：请启动微信并完成登录后重试"
	case StateKeyUnavailable:
		return "微信在跑但密钥取不到：微信 4.1.10.x+ 已把密钥移出明文内存，请核对 sidecar 与微信版本兼容清单，或回退到已验证版本"
	case StateVersionUnsupported:
		return "微信版本不受支持：对照已验证 sidecar 版本清单升级 sidecar 或固定微信版本，并关闭微信自动更新"
	case StateSchemaIncompatible:
		return "API schema 不兼容：拿 raw_body 取证，核对 sidecar 版本与适配器期望的响应结构后再更新适配器"
	case StateOK:
		return "sidecar 正常在线，session 探测通过"
	default:
		return fmt.Sprintf("未知状态 %q：携带 raw_body 取证并上报该状态", s)
	}
}

// ProbeResult classifies one sidecar probe. RawBody keeps the verbatim
// response (truncated to <=4KB) for schema/version forensics; Error keeps the
// transport failure text so nothing is silently swallowed.
type ProbeResult struct {
	State       State  `json:"state"`
	HTTPStatus  int    `json:"http_status"`
	TalkerCount int    `json:"talker_count"`
	RawBody     string `json:"raw_body"`
	Advice      string `json:"advice"`
	Error       string `json:"error,omitempty"`
}

// ProbeHTTP GETs baseURL+/api/v1/session and classifies the outcome onto the
// 6-state machine. A non-nil *ProbeResult is always returned — transport
// failures still carry State/Advice — and the error is returned alongside it,
// never swallowed. timeout<=0 defaults to 30s so the client is never
// zero-valued; connect is additionally capped at 3s (plan: connect 3s/read 30s).
func ProbeHTTP(baseURL string, timeout time.Duration) (*ProbeResult, error) {
	if timeout <= 0 {
		timeout = defaultReadTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionURL(baseURL), nil)
	if err != nil {
		return transportFailure(err)
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: connectTimeout}).DialContext,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return transportFailure(err)
	}
	defer resp.Body.Close()
	return classifyResponse(resp)
}

func sessionURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + sessionPath
}

// transportFailure classifies dial/timeout errors onto process_absent while
// keeping the wrapped error for the caller's evidence.
func transportFailure(err error) (*ProbeResult, error) {
	res := &ProbeResult{
		State:  StateProcessAbsent,
		Advice: StateProcessAbsent.Hint(),
		Error:  err.Error(),
	}
	if isTimeout(err) {
		res.Advice += "（本次连接超时：sidecar 无响应，进程可能卡死，重启 sidecar 后重试）"
	} else {
		res.Advice += "（本次连接失败：确认 sidecar 进程已启动且监听该地址端口）"
	}
	return res, fmt.Errorf("probe sidecar http: %w", err)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func classifyResponse(resp *http.Response) (*ProbeResult, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	if err != nil {
		res := &ProbeResult{
			State:      StateSchemaIncompatible,
			HTTPStatus: resp.StatusCode,
			Advice:     StateSchemaIncompatible.Hint(),
			Error:      err.Error(),
		}
		return res, fmt.Errorf("read probe body: %w", err)
	}
	state, count := classify(body)
	return &ProbeResult{
		State:       state,
		HTTPStatus:  resp.StatusCode,
		TalkerCount: count,
		RawBody:     truncateRawBody(string(body)),
		Advice:      state.Hint(),
	}, nil
}

// classify maps a sidecar response body onto the 6-state machine and extracts
// talker_count when the payload is recognizable.
func classify(body []byte) (State, int) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return StateSchemaIncompatible, 0
	}
	if state, failed := classifyFailure(payload); failed {
		return state, 0
	}
	if !hasRecognizedSessionKey(payload) {
		return StateSchemaIncompatible, 0
	}
	return StateOK, extractTalkerCount(payload)
}

// classifyFailure handles {"success":false,"error":...} payloads. A false
// success whose error text maps to no known failure mode falls back to
// schema_incompatible so unknown sidecar errors surface for forensics.
func classifyFailure(payload map[string]json.RawMessage) (State, bool) {
	raw, hasSuccess := payload["success"]
	if !hasSuccess {
		return "", false
	}
	var success bool
	if json.Unmarshal(raw, &success) != nil || success {
		return "", false
	}
	return classifyFailureMessage(failureMessage(payload)), true
}

func classifyFailureMessage(msg string) State {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "not running"), strings.Contains(lower, "未运行"), strings.Contains(lower, "没跑"):
		return StateWeChatNotRunning
	case strings.Contains(lower, "version"), strings.Contains(lower, "版本"):
		return StateVersionUnsupported
	case strings.Contains(lower, "key"), strings.Contains(lower, "密钥"), strings.Contains(lower, "decrypt"):
		return StateKeyUnavailable
	default:
		return StateSchemaIncompatible
	}
}

func failureMessage(payload map[string]json.RawMessage) string {
	for _, key := range []string{"error", "message", "msg"} {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text
		}
		return string(raw)
	}
	return ""
}

func hasRecognizedSessionKey(payload map[string]json.RawMessage) bool {
	for _, key := range []string{"success", "data", "session", "talker_count", "talkerCount", "talkers"} {
		if _, ok := payload[key]; ok {
			return true
		}
	}
	return false
}

func extractTalkerCount(payload map[string]json.RawMessage) int {
	if count, ok := intField(payload, "talker_count"); ok {
		return count
	}
	if count, ok := intField(payload, "talkerCount"); ok {
		return count
	}
	rawData, ok := payload["data"]
	if !ok {
		return 0
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(rawData, &data) != nil {
		return 0
	}
	return extractTalkerCount(data)
}

func intField(payload map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := payload[key]
	if !ok {
		return 0, false
	}
	var value int
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func truncateRawBody(body string) string {
	if len(body) <= maxRawBody {
		return body
	}
	return body[:maxRawBody]
}

// FindWeChatPaths returns the candidate WeChat exe/data paths to scan; the
// caller stats them, so the full search list stays inspectable in probe
// output. WeChat 4.x renamed the exe to Weixin.exe, hence dual candidates.
func FindWeChatPaths() []string {
	programFiles := envOr("ProgramFiles", `C:\Program Files`)
	programFilesX86 := envOr("ProgramFiles(x86)", `C:\Program Files (x86)`)
	userProfile := envOr("USERPROFILE", `C:\Users\default`)
	return []string{
		programFiles + `\Tencent\WeChat\WeChat.exe`,
		programFiles + `\Tencent\Weixin\Weixin.exe`,
		programFilesX86 + `\Tencent\WeChat\WeChat.exe`,
		programFilesX86 + `\Tencent\Weixin\Weixin.exe`,
		userProfile + `\AppData\Local\Tencent\WeChat\WeChat.exe`,
		userProfile + `\AppData\Local\Tencent\Weixin\Weixin.exe`,
		userProfile + `\Documents\WeChat Files`,
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// CheckPortOpen dials addr with the plan's 3s bound; false on any dial error.
func CheckPortOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, portProbeTimeout)
	if err != nil {
		return false
	}
	// The successful dial already answers the probe; close hygiene cannot flip it.
	_ = conn.Close()
	return true
}

// versionCmdTimeout bounds one PowerShell VersionInfo read (defect D1): a
// shell stalled on broken WMI/AV must not hang detectWeChat, which runs
// before ProbeHTTP — an unbounded hang would keep the CLI from ever
// emitting its single JSON line.
var versionCmdTimeout = 10 * time.Second

// runVersionCmd is the injectable command seam for WeChatVersionExe (plan:
// stub interface, replaced by PE-resource reads later if M0 needs it). The
// real implementation runs PowerShell under ctx so the caller's deadline
// (exec.CommandContext) kills a hung shell instead of blocking forever.
var runVersionCmd = func(ctx context.Context, script string) ([]byte, error) {
	return exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
}

// WeChatVersionExe reads the exe's FileVersion via PowerShell VersionInfo.
// Every failure mode (command error, deadline miss, empty output) returns an
// error; the read is bounded by versionCmdTimeout so a hung PowerShell can
// never stall the probe (defect D1).
func WeChatVersionExe(exePath string) (string, error) {
	if strings.TrimSpace(exePath) == "" {
		return "", fmt.Errorf("WeChatVersionExe: empty exe path")
	}
	quoted := strings.ReplaceAll(exePath, "'", "''")
	script := fmt.Sprintf("(Get-Item -LiteralPath '%s').VersionInfo.FileVersion", quoted)
	ctx, cancel := context.WithTimeout(context.Background(), versionCmdTimeout)
	defer cancel()
	out, err := runVersionCmd(ctx, script)
	if err != nil {
		return "", fmt.Errorf("read FileVersion of %s: %w", exePath, err)
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", fmt.Errorf("read FileVersion of %s: empty PowerShell output", exePath)
	}
	return version, nil
}
