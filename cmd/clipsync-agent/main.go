package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"clipsyncpro/internal/agent"
	"clipsyncpro/internal/httpapi"
	"clipsyncpro/internal/security"
	"clipsyncpro/internal/singleinstance"
	"clipsyncpro/internal/storage"
)

func main() {
	closeLog := configureLog()
	defer closeLog()

	lock, alreadyRunning, err := singleinstance.Acquire("Global\\ClipSyncProWindowsAgent")
	if err != nil {
		log.Fatalf("single instance lock failed: %v", err)
	}
	defer lock.Release()
	if alreadyRunning {
		log.Printf("ClipSync Pro agent is already running")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := buildAgentConfig()
	if err != nil {
		log.Fatalf("initialize local agent config failed: %v", err)
	}

	if err := runWithStorageRetry(ctx, cfg.UIAddr, cfg); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func buildAgentConfig() (agent.Config, error) {
	host, _ := os.Hostname()
	deviceID := firstEnv("CLIPSYNC_DEVICE_ID")
	if deviceID == "" {
		deviceID = "windows-" + host
	}
	serverURL := ""
	if strings.TrimSpace(os.Getenv("CLIPSYNC_ENABLE_REMOTE_SYNC")) == "1" {
		serverURL = firstEnv("CLIPSYNC_SERVER_URL")
		if serverURL == "" {
			serverURL = "http://127.0.0.1:37617"
		}
	}
	secret, err := security.ResolveLocalSecret()
	if err != nil {
		return agent.Config{}, err
	}
	uiAddr := normalizeListenAddr(firstEnv("CLIPSYNC_AGENT_ADDR", "CLIPSYNC_UI_ADDR"))
	if uiAddr == "" {
		uiAddr = "127.0.0.1:37618"
	}
	return agent.Config{
		UIAddr:       uiAddr,
		ServerURL:    serverURL,
		Secret:       secret,
		UserID:       firstEnv("CLIPSYNC_USER_ID"),
		DeviceID:     deviceID,
		PollInterval: 250 * time.Millisecond,
	}, nil
}

func normalizeListenAddr(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	for _, prefix := range []string{"http://", "https://"} {
		if strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			trimmed = trimmed[len(prefix):]
			break
		}
	}
	return strings.TrimSuffix(trimmed, "/")
}

func runWithStorageRetry(ctx context.Context, uiAddr string, cfg agent.Config) error {
	var (
		degraded     *http.Server
		degradedDone chan struct{}
		handler      = newDegradedHandler(cfg)
	)
	stopDegraded := func() {
		if degraded == nil {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := degraded.Shutdown(shutdownCtx); err != nil {
			_ = degraded.Close()
		}
		cancel()
		if degradedDone != nil {
			<-degradedDone
		}
		degraded = nil
		degradedDone = nil
	}

	for {
		if ctx.Err() != nil {
			stopDegraded()
			return nil
		}
		openCtx, openCancel := context.WithTimeout(ctx, 5*time.Second)
		store, err := storage.OpenSQLiteFromEnv(openCtx)
		openCancel()
		if err != nil {
			handler.update(err)
			log.Printf("open local SQLite failed: %v; retrying in 3s", err)
			if degraded == nil {
				server := &http.Server{
					Addr:              uiAddr,
					Handler:           handler,
					ReadHeaderTimeout: 5 * time.Second,
				}
				done := make(chan struct{})
				degraded = server
				degradedDone = done
				go func() {
					defer close(done)
					if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
						log.Printf("degraded local UI failed: %v", serveErr)
					}
				}()
			}
			select {
			case <-ctx.Done():
				stopDegraded()
				return nil
			case <-time.After(3 * time.Second):
				continue
			}
		}

		stopDegraded()
		background := agent.New(store, cfg)
		background.StartBackground(ctx)
		log.Printf("ClipSync Pro local UI bridge listening on http://%s (SQLite standalone)", uiAddr)
		runErr := httpapi.ListenAndServe(ctx, uiAddr, background)
		_ = store.Close()
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("local agent stopped: %v; retrying in 2s", runErr)
		time.Sleep(2 * time.Second)
	}
}

type degradedHandler struct {
	mu        sync.RWMutex
	deviceID  string
	userID    string
	serverURL string
	lastErr   string
}

func newDegradedHandler(cfg agent.Config) *degradedHandler {
	deviceID := cfg.DeviceID
	if deviceID == "" {
		deviceID = "windows-local"
	}
	userID := cfg.UserID
	if userID == "" {
		userID = "local"
	}
	return &degradedHandler{
		deviceID:  deviceID,
		userID:    userID,
		serverURL: cfg.ServerURL,
	}
}

func (h *degradedHandler) update(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil {
		h.lastErr = err.Error()
	} else {
		h.lastErr = ""
	}
}

func (h *degradedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.mu.RLock()
	deviceID := h.deviceID
	userID := h.userID
	serverURL := h.serverURL
	lastErr := h.lastErr
	h.mu.RUnlock()
	if deviceID == "" {
		deviceID = "windows-local"
	}
	if userID == "" {
		userID = "local"
	}

	if r.URL.Path == "/api/status" {
		resp := map[string]any{
			"status":         "degraded",
			"ui_api_version": "1",
			"device_id":      deviceID,
			"user_id":        userID,
			"server_url":     serverURL,
			"ui_addr":        r.Host,
			"storage_status": "unavailable",
			"storage_engine": "sqlite",
			"local_only":     serverURL == "",
		}
		if lastErr != "" {
			resp["storage_error"] = lastErr
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	resp := map[string]string{
		"error": "storage unavailable",
		"code":  "storage_unavailable",
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func configureLog() func() {
	logDir := strings.TrimSpace(os.Getenv("CLIPSYNC_LOG_DIR"))
	if logDir == "" {
		var err error
		logDir, err = security.DefaultDataDir()
		if err != nil {
			return func() {}
		}
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return func() {}
	}
	file, err := os.OpenFile(filepath.Join(logDir, "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}
	}
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(io.MultiWriter(os.Stderr, file))
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	return func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		_ = file.Close()
	}
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}
