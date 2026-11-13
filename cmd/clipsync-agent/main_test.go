package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clipsyncpro/internal/agent"
	"clipsyncpro/internal/storage"
)

func TestBuildAgentConfigDefaultsToLocalOnlyAndAutoSecret(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("CLIPSYNC_DATA_DIR", tempDir)
	t.Setenv("CLIPSYNC_SECRET", "")
	t.Setenv("CLIPSYNC_ENABLE_REMOTE_SYNC", "")
	t.Setenv("CLIPSYNC_SERVER_URL", "http://127.0.0.1:37617")

	cfg, err := buildAgentConfig()
	if err != nil {
		t.Fatalf("buildAgentConfig() error = %v", err)
	}
	if len(cfg.Secret) != 64 {
		t.Fatalf("cfg.Secret length = %d, want 64 hex chars", len(cfg.Secret))
	}
	if cfg.ServerURL != "" {
		t.Fatalf("cfg.ServerURL = %q, want empty string in local standalone mode", cfg.ServerURL)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "master.key")); err != nil {
		t.Fatalf("expected master.key to exist: %v", err)
	}

	// Explicit opt-in enables remote sync URL
	t.Setenv("CLIPSYNC_ENABLE_REMOTE_SYNC", "1")
	cfgSync, err := buildAgentConfig()
	if err != nil {
		t.Fatalf("buildAgentConfig() with remote sync error = %v", err)
	}
	if cfgSync.ServerURL != "http://127.0.0.1:37617" {
		t.Fatalf("cfgSync.ServerURL = %q, want http://127.0.0.1:37617", cfgSync.ServerURL)
	}
}

func TestLocalAgentStatusAndSQLiteEndToEndWithoutMySQL(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("CLIPSYNC_DATA_DIR", tempDir)
	t.Setenv("CLIPSYNC_SQLITE_PATH", "")
	t.Setenv("CLIPSYNC_SECRET", "")
	t.Setenv("CLIPSYNC_ENABLE_REMOTE_SYNC", "")

	cfg, err := buildAgentConfig()
	if err != nil {
		t.Fatalf("buildAgentConfig: %v", err)
	}

	ctx := context.Background()
	store, err := storage.OpenSQLiteFromEnv(ctx)
	if err != nil {
		t.Fatalf("OpenSQLiteFromEnv: %v", err)
	}
	defer store.Close()

	bg := agent.New(store, cfg)

	statusRec := httptest.NewRecorder()
	bg.ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", statusRec.Code)
	}

	var statusBody map[string]any
	if err := json.NewDecoder(statusRec.Body).Decode(&statusBody); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if statusBody["status"] != "ok" || statusBody["storage_status"] != "ok" {
		t.Fatalf("unexpected status body: %+v", statusBody)
	}
	if statusBody["storage_engine"] != "sqlite" {
		t.Fatalf("storage_engine = %v, want sqlite", statusBody["storage_engine"])
	}
	if statusBody["local_only"] != true {
		t.Fatalf("local_only = %v, want true", statusBody["local_only"])
	}

	// Verify sensitive encryption & reveal work out-of-the-box with auto-generated secret + SQLite
	rec, err := bg.Service().CaptureText(ctx, "MyS3cretP@ss")
	if err != nil {
		t.Fatalf("CaptureText: %v", err)
	}
	if _, err := bg.Service().MarkPassword(ctx, rec.ID, "admin@local", "本地保险箱"); err != nil {
		t.Fatalf("MarkPassword: %v", err)
	}
	revealed, err := bg.Service().Reveal(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if revealed.Password != "MyS3cretP@ss" || revealed.Account != "admin@local" {
		t.Fatalf("unexpected revealed credentials: %+v", revealed)
	}
}

func TestDegradedHandlerKeepsAgentReachable(t *testing.T) {
	handler := newDegradedHandler(agent.Config{DeviceID: "windows-test"})
	handler.update(errors.New("sqlite busy"))

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", status.Code, http.StatusOK)
	}
	body := status.Body.String()
	if !strings.Contains(body, `"storage_status":"unavailable"`) {
		t.Fatalf("status body = %s", body)
	}
	if !strings.Contains(body, `"storage_engine":"sqlite"`) {
		t.Fatalf("status body missing storage_engine sqlite: %s", body)
	}
	if !strings.Contains(body, `"local_only":true`) {
		t.Fatalf("status body missing local_only true: %s", body)
	}

	records := httptest.NewRecorder()
	handler.ServeHTTP(records, httptest.NewRequest(http.MethodGet, "/api/records", nil))
	if records.Code != http.StatusServiceUnavailable {
		t.Fatalf("records code = %d, want %d", records.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(records.Body.String(), `"code":"storage_unavailable"`) {
		t.Fatalf("records body = %s", records.Body.String())
	}
}

func TestBuildAgentConfigSupportsAgentAddrEnv(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("CLIPSYNC_DATA_DIR", tempDir)
	t.Setenv("CLIPSYNC_AGENT_ADDR", "127.0.0.1:39001")
	t.Setenv("CLIPSYNC_UI_ADDR", "127.0.0.1:39002")

	cfg, err := buildAgentConfig()
	if err != nil {
		t.Fatalf("buildAgentConfig: %v", err)
	}
	if cfg.UIAddr != "127.0.0.1:39001" {
		t.Fatalf("cfg.UIAddr = %q, want 127.0.0.1:39001", cfg.UIAddr)
	}

	t.Setenv("CLIPSYNC_AGENT_ADDR", "http://127.0.0.1:39003/")
	cfgURL, err := buildAgentConfig()
	if err != nil {
		t.Fatalf("buildAgentConfig with http prefix: %v", err)
	}
	if cfgURL.UIAddr != "127.0.0.1:39003" {
		t.Fatalf("cfgURL.UIAddr = %q, want 127.0.0.1:39003", cfgURL.UIAddr)
	}
}

func TestConfigureLogUsesDefaultDataDir(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("CLIPSYNC_DATA_DIR", tempDir)
	t.Setenv("CLIPSYNC_LOG_DIR", "")

	closeLog := configureLog()
	defer closeLog()

	logPath := filepath.Join(tempDir, "agent.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected agent.log in CLIPSYNC_DATA_DIR (%s): %v", logPath, err)
	}
}

func TestRunWithStorageRetryExitsOnCanceledContext(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("CLIPSYNC_DATA_DIR", tempDir)
	t.Setenv("CLIPSYNC_SQLITE_PATH", "")

	cfg, err := buildAgentConfig()
	if err != nil {
		t.Fatalf("buildAgentConfig: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := runWithStorageRetry(ctx, "127.0.0.1:0", cfg); err != nil {
		t.Fatalf("runWithStorageRetry with canceled ctx returned error: %v", err)
	}
}
