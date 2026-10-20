package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lover-instance/backend-go/internal/report"
)

// TestExport_WritesTheReportBesideTheFingerprint checks the happy path: a file
// is produced whose byte count is what the response said, named after the
// fingerprint rather than the conversation.
//
// The name matters as much as the contents. An exports directory is a place
// strings live for a long time — in a screenshot, in a folder listing, in a
// backup — and a wxid there would outlast the report itself.
func TestExport_WritesTheReportBesideTheFingerprint(t *testing.T) {
	s := testStore(t)
	dir := t.TempDir()
	engine := New(Options{
		Store: s, NLPURL: fakeNLP(t, healthyNLP).URL, Version: "test", Log: io.Discard, ExportsDir: dir,
	})

	const talker = "wxid_secret_talker"
	seedText(t, s, "k1", talker, testBaseMs, true, "你好")
	seedText(t, s, "k2", talker, testBaseMs+1000, false, "今天也不错")

	recorder := do(t, engine, http.MethodPost, "/api/export", `{"talker":"`+talker+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST /api/export = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	var response exportResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode export body %q: %v", recorder.Body, err)
	}
	if response.Bytes <= 0 {
		t.Fatalf("bytes = %d, want a written document", response.Bytes)
	}

	info, err := os.Stat(response.Path)
	if err != nil {
		t.Fatalf("stat exported report %q: %v", response.Path, err)
	}
	if int(info.Size()) != response.Bytes {
		t.Errorf("file size = %d, want the %d bytes the response reported", info.Size(), response.Bytes)
	}

	name := filepath.Base(response.Path)
	if strings.Contains(name, talker) {
		t.Errorf("file name = %q, want no raw talker: an export outlives the request", name)
	}
	if !strings.HasPrefix(name, report.TalkerHash(talker)) {
		t.Errorf("file name = %q, want it to start with the fingerprint %q", name, report.TalkerHash(talker))
	}

	document, err := os.ReadFile(response.Path)
	if err != nil {
		t.Fatalf("read exported report: %v", err)
	}
	if !strings.Contains(string(document), "<!DOCTYPE html>") {
		t.Error("exported file is not an HTML document, so the path is not usable as a report")
	}
	// The report is checked once more on disk: the endpoint promises a
	// self-contained file, and the file is what people will actually open.
	if refs := report.ExternalRefs(document); len(refs) != 0 {
		t.Errorf("exported report reaches the network in %d place(s): %v", len(refs), refs)
	}
}

// TestExport_RefusesToWriteAReportThatWouldReachTheNetwork is the guard.
//
// The check runs before the file exists: a half-written report on disk is
// something a person can open, believe, and send, whereas a refused request is
// just a request. The seam makes the guard fire without needing a view that
// happens to contain a URL.
func TestExport_RefusesToWriteAReportThatWouldReachTheNetwork(t *testing.T) {
	original := externalRefs
	externalRefs = func([]byte) []string { return []string{"https://example.test  <<https://"} }
	t.Cleanup(func() { externalRefs = original })

	s := testStore(t)
	dir := t.TempDir()
	engine := New(Options{
		Store: s, NLPURL: fakeNLP(t, healthyNLP).URL, Version: "test", Log: io.Discard, ExportsDir: dir,
	})
	seedText(t, s, "k1", "wxid_talker", testBaseMs, true, "你好")

	recorder := do(t, engine, http.MethodPost, "/api/export", `{"talker":"wxid_talker"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/export = %d, want 500: %s", recorder.Code, recorder.Body)
	}
	envelope := decodeErrorBody(t, recorder.Body.Bytes())
	if envelope.Code != "external_reference" {
		t.Errorf("code = %q, want %q", envelope.Code, "external_reference")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the export directory: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("export directory holds %v, want nothing: a refused report must not leave a file, not even a .tmp", names)
	}
}
