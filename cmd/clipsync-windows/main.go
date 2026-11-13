package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"clipsyncpro/internal/agent"
	"clipsyncpro/internal/autostart"
	"clipsyncpro/internal/domain"
	"clipsyncpro/internal/nativeui"
	"clipsyncpro/internal/security"
	"clipsyncpro/internal/singleinstance"
	"clipsyncpro/internal/storage"
)

func main() {
	lock, alreadyRunning, err := singleinstance.Acquire("Global\\ClipSyncProWindowsClient")
	if err != nil {
		log.Fatalf("single instance lock failed: %v", err)
	}
	defer lock.Release()
	if err := autostart.EnsureEnabled("ClipSyncPro"); err != nil {
		log.Printf("enable startup failed: %v", err)
	}
	if alreadyRunning {
		singleinstance.ActivateExistingWindow("ClipSyncProNativeWindow", "ClipSync Pro")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.OpenSQLiteFromEnv(ctx)
	if err != nil {
		log.Fatalf("open local SQLite failed: %v", err)
	}
	defer store.Close()

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
		log.Fatalf("resolve local secret failed: %v", err)
	}

	refreshUI := make(chan struct{}, 1)
	notifyUI := func() {
		select {
		case refreshUI <- struct{}{}:
		default:
		}
	}
	background := agent.New(store, agent.Config{
		UIAddr:       firstEnv("CLIPSYNC_AGENT_ADDR", "CLIPSYNC_UI_ADDR"),
		ServerURL:    serverURL,
		Secret:       secret,
		UserID:       firstEnv("CLIPSYNC_USER_ID"),
		DeviceID:     deviceID,
		PollInterval: 250 * time.Millisecond,
		OnRecord:     func(_ domain.ClipboardRecord) { notifyUI() },
		OnRemoteEvent: func(_ domain.SyncEvent) {
			notifyUI()
		},
	})
	background.StartBackground(ctx)
	if err := nativeui.Run(ctx, background.Service(), refreshUI); err != nil && ctx.Err() == nil {
		log.Fatal(err)
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
