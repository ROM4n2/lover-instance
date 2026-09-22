// Command probe prints one JSON object with WeChat-exe detection,
// wechat_version, sidecar state, HTTP reachability and talker_count — the M0
// data-spike evidence probe (plan §Task 3, sidecar 6-state machine).
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"lover-instance/backend-go/internal/probe"
)

const (
	defaultSidecarURL = "http://127.0.0.1:5030"
	// Plan §Task 3: read timeout 30s (connect stays capped at 3s inside ProbeHTTP).
	probeReadTimeout = 30 * time.Second
)

type probeReport struct {
	WeChatExe     string         `json:"wechat_exe"`
	WeChatVersion string         `json:"wechat_version"`
	SidecarState  probe.State    `json:"sidecar_state"`
	HTTPOK        bool           `json:"http_ok"`
	TalkerCount   int            `json:"talker_count"`
	RawBody       string         `json:"raw_body"`
	Advice        string         `json:"advice"`
	Evidence      map[string]any `json:"evidence"`
}

func main() {
	if err := json.NewEncoder(os.Stdout).Encode(buildReport()); err != nil {
		fmt.Fprintln(os.Stderr, "probe: encode report:", err)
		os.Exit(1)
	}
}

func buildReport() probeReport {
	baseURL := os.Getenv("SIDECAR_URL")
	if baseURL == "" {
		baseURL = defaultSidecarURL
	}
	evidence := map[string]any{"base_url": baseURL}
	exePath, version := detectWeChat(evidence)
	addPortEvidence(baseURL, evidence)

	res, probeErr := probe.ProbeHTTP(baseURL, probeReadTimeout)
	if res == nil {
		res = &probe.ProbeResult{
			State:  probe.StateProcessAbsent,
			Advice: probe.StateProcessAbsent.Hint(),
			Error:  "ProbeHTTP violated its contract and returned a nil result",
		}
	}
	if probeErr != nil {
		evidence["probe_error"] = probeErr.Error()
	}
	return probeReport{
		WeChatExe:     exePath,
		WeChatVersion: version,
		SidecarState:  res.State,
		HTTPOK:        probeErr == nil,
		TalkerCount:   res.TalkerCount,
		RawBody:       res.RawBody,
		Advice:        composeAdvice(res.Advice, exePath, evidence),
		Evidence:      evidence,
	}
}

// detectWeChat locates the first existing exe candidate and reads its version;
// failures land in evidence instead of disappearing.
func detectWeChat(evidence map[string]any) (string, string) {
	candidates := probe.FindWeChatPaths()
	exePath := firstExistingExe(candidates)
	if exePath == "" {
		evidence["searched_candidates"] = candidates
		return "", ""
	}
	version, err := probe.WeChatVersionExe(exePath)
	if err != nil {
		evidence["version_error"] = err.Error()
		return exePath, ""
	}
	return exePath, version
}

func firstExistingExe(candidates []string) string {
	for _, candidate := range candidates {
		if path, ok := existingExe(candidate); ok {
			return path
		}
	}
	return ""
}

func existingExe(candidate string) (string, bool) {
	if !strings.HasSuffix(strings.ToLower(candidate), ".exe") {
		return "", false // data-directory candidate (WeChat Files), not an exe
	}
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return "", false
	}
	return candidate, true
}

// addPortEvidence records the TCP reachability probe for the report evidence.
func addPortEvidence(baseURL string, evidence map[string]any) {
	addr, err := portAddr(baseURL)
	if err != nil {
		evidence["port_error"] = err.Error()
		return
	}
	evidence["port_addr"] = addr
	evidence["port_open"] = probe.CheckPortOpen(addr)
}

func portAddr(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url %q: %w", baseURL, err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("base url %q has no host", baseURL)
	}
	if u.Port() != "" {
		return u.Host, nil
	}
	schemeDefault := "80"
	if u.Scheme == "https" {
		schemeDefault = "443"
	}
	return net.JoinHostPort(u.Hostname(), schemeDefault), nil
}

func composeAdvice(stateAdvice, exePath string, evidence map[string]any) string {
	advice := stateAdvice
	if exePath == "" {
		advice = "未在候选路径找到微信 exe（候选见 evidence.searched_candidates）；" + advice
	}
	if _, failed := evidence["version_error"]; failed {
		advice += "；微信版本读取失败，详见 evidence.version_error"
	}
	return advice
}
