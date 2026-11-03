package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestScanExportHandler_NotFound(t *testing.T) {
	s := testStore(t)
	emptyDir := t.TempDir()
	r := New(Options{Store: s, ExportsDir: emptyDir})

	req, _ := http.NewRequest(http.MethodPost, "/api/scan-export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var res map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res["found"] != false {
		t.Errorf("expected found: false in empty dir, got %v", res["found"])
	}
}

func TestScanExportHandler_FoundAndImport(t *testing.T) {
	s := testStore(t)
	tempDir := t.TempDir()
	exportPath := filepath.Join(tempDir, "wechat_messages.json")
	sampleJSON := `[
		{"ts":1704121620000,"sender":"对方","sender_wxid":"wxid_scan","chat_room":"wxid_scan","conversation_id":"wxid_scan","content":"hi","msg_type":1,"is_self":false,"account_id":"me","thread_id":"th1","source_offset":"0","conversation_type":"direct","is_group_chat":false}
	]`
	if err := os.WriteFile(exportPath, []byte(sampleJSON), 0o600); err != nil {
		t.Fatalf("write sample export: %v", err)
	}

	r := New(Options{Store: s, ExportsDir: tempDir})

	// 1. Scan only
	req, _ := http.NewRequest(http.MethodPost, "/api/scan-export", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var res map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res["found"] != true {
		t.Fatalf("expected found: true, got %v", res["found"])
	}

	// 2. Scan and import
	req2, _ := http.NewRequest(http.MethodPost, "/api/scan-export?import=true", nil)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
	var res2 map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &res2)
	if res2["ok"] != true {
		t.Errorf("expected ok: true, got %v", res2["ok"])
	}
}
