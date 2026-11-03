package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDemoHandler(t *testing.T) {
	s := testStore(t)
	r := New(Options{Store: s})

	req, _ := http.NewRequest(http.MethodPost, "/api/demo", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("json parse response: %v", err)
	}
	if res["ok"] != true {
		t.Errorf("expected ok: true, got %v", res["ok"])
	}
}

func TestHighlightsHandler(t *testing.T) {
	s := testStore(t)
	r := New(Options{Store: s})

	// Missing talker -> 400
	req, _ := http.NewRequest(http.MethodGet, "/api/highlights", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing talker, got %d", w.Code)
	}

	// First load demo so talker has highlights
	reqDemo, _ := http.NewRequest(http.MethodPost, "/api/demo", nil)
	wDemo := httptest.NewRecorder()
	r.ServeHTTP(wDemo, reqDemo)

	// Valid talker -> 200
	req2, _ := http.NewRequest(http.MethodGet, "/api/highlights?talker=wxid_peer_b", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "present") {
		t.Errorf("expected present in json response, got: %s", w2.Body.String())
	}

	// Valid fingerprint -> 200 and present: true
	reqFingerprint, _ := http.NewRequest(http.MethodGet, "/api/highlights?talker=2241483c34a72321", nil)
	wF := httptest.NewRecorder()
	r.ServeHTTP(wF, reqFingerprint)
	if wF.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for fingerprint, got %d", wF.Code)
	}
	if !strings.Contains(wF.Body.String(), `"present":true`) {
		t.Errorf("expected present: true for fingerprint, got: %s", wF.Body.String())
	}
}

func TestImportHandler_InvalidFile(t *testing.T) {
	s := testStore(t)
	r := New(Options{Store: s})

	// Post without multipart body -> 400
	req, _ := http.NewRequest(http.MethodPost, "/api/import", bytes.NewReader([]byte("not multipart")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-multipart, got %d", w.Code)
	}
}

func TestImportHandler_ValidUpload(t *testing.T) {
	s := testStore(t)
	r := New(Options{Store: s})

	sampleJSON := `[
		{"ts":1704121620000,"sender":"对方","sender_wxid":"wxid_t","chat_room":"wxid_t","conversation_id":"wxid_t","content":"hi","msg_type":1,"is_self":false,"account_id":"me","thread_id":"th1","source_offset":"0","conversation_type":"direct","is_group_chat":false}
	]`

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "chat.json")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.WriteString(part, sampleJSON); err != nil {
		t.Fatalf("write part: %v", err)
	}
	writer.Close()

	req, _ := http.NewRequest(http.MethodPost, "/api/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
}
