package agent

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"clipsyncpro/internal/app"
	"clipsyncpro/internal/domain"
	"clipsyncpro/internal/httpapi"
	"clipsyncpro/internal/storage"
	"clipsyncpro/internal/syncer"
	"clipsyncpro/internal/win32"
)

type Config struct {
	UIAddr        string
	ServerURL     string
	Secret        string
	UserID        string
	DeviceID      string
	PollInterval  time.Duration
	OnRecord      func(domain.ClipboardRecord)
	OnRemoteEvent func(domain.SyncEvent)
}

type Agent struct {
	cfg                Config
	store              storage.Store
	service            *app.Service
	sync               *syncer.Client
	reveals            revealCache
	events             *httpapi.Hub
	clipboardWrites    *clipboardWriteGuard
	writeClipboardText func(string) error
	pasteText          func(string) error
	focusWindow        func(uintptr) error
	storageHealthy     atomic.Bool
}

type schemaEnsurer interface {
	EnsureSchema(context.Context) error
}

func New(store storage.Store, cfg Config) *Agent {
	if cfg.UIAddr == "" {
		cfg.UIAddr = "127.0.0.1:37618"
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID = "windows-default"
	}
	if cfg.UserID == "" {
		cfg.UserID = "local"
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	service := app.NewService(store, app.Config{
		Secret:    cfg.Secret,
		UserID:    cfg.UserID,
		DeviceID:  cfg.DeviceID,
		Platform:  domain.PlatformWindows,
		LocalOnly: cfg.ServerURL == "",
	})
	var syncClient *syncer.Client
	if cfg.ServerURL != "" {
		syncClient = syncer.NewClient(cfg.ServerURL)
		syncClient.DeviceID = cfg.DeviceID
	}
	a := &Agent{
		cfg:                cfg,
		store:              store,
		service:            service,
		sync:               syncClient,
		reveals:            newRevealCache(),
		events:             httpapi.NewHub(),
		clipboardWrites:    newClipboardWriteGuard(win32.ClipboardSequenceNumber),
		writeClipboardText: win32.WriteClipboardText,
		pasteText:          win32.PasteText,
		focusWindow:        win32.FocusWindow,
	}
	a.storageHealthy.Store(true)
	return a
}

func (a *Agent) Run(ctx context.Context) error {
	a.StartBackground(ctx)
	go a.runHotkey(ctx)
	log.Printf("ClipSync Pro debug UI listening on http://%s", a.cfg.UIAddr)
	return httpapi.ListenAndServe(ctx, a.cfg.UIAddr, a)
}

func (a *Agent) RunBridge(ctx context.Context) error {
	a.StartBackground(ctx)
	log.Printf("ClipSync Pro local UI bridge listening on http://%s", a.cfg.UIAddr)
	return httpapi.ListenAndServe(ctx, a.cfg.UIAddr, a)
}

func (a *Agent) StartBackground(ctx context.Context) {
	go a.runStorageHealthLoop(ctx)
	go a.runClipboardLoop(ctx)
	if a.sync != nil {
		go a.runSyncLoop(ctx)
		go a.runEventLoop(ctx)
	}
}

func (a *Agent) Service() *app.Service {
	return a.service
}

func (a *Agent) runStorageHealthLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.probeStorageAndHeal(ctx)
		}
	}
}

func (a *Agent) probeStorageAndHeal(ctx context.Context) (bool, storage.MySQLServiceStatus, error) {
	pingCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	err := a.store.Ping(pingCtx)
	cancel()
	if err != nil {
		if a.storageHealthy.Swap(false) {
			log.Printf("local storage became unavailable: %v; entering self-healing loop", err)
		}
		var svcStatus storage.MySQLServiceStatus
		if _, isMySQL := a.store.(*storage.MySQLStore); isMySQL {
			svcStatus = storage.HealLocalMySQLFromEnv(ctx)
		}
		return false, svcStatus, err
	}
	if !a.storageHealthy.Swap(true) {
		if ensurer, ok := a.store.(schemaEnsurer); ok {
			schemaCtx, schemaCancel := context.WithTimeout(ctx, 5*time.Second)
			if schemaErr := ensurer.EnsureSchema(schemaCtx); schemaErr != nil {
				log.Printf("ensure schema after storage recovery failed: %v", schemaErr)
			}
			schemaCancel()
		}
		log.Printf("local storage recovered")
		a.broadcastLocalEvent(domain.SyncEvent{
			EventType: domain.EventStorageRecovered,
			Occurred:  time.Now().UTC(),
			DeviceID:  a.cfg.DeviceID,
		})
	}
	return true, storage.MySQLServiceStatus{}, nil
}

func (a *Agent) runClipboardLoop(ctx context.Context) {
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()
	lastSeq := win32.ClipboardSequenceNumber()
	var retryNotBefore time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot, suppressed, err := a.clipboardWrites.read(win32.ReadClipboard)
			if err != nil {
				continue
			}
			if snapshot.Sequence == lastSeq {
				continue
			}
			if suppressed || (!snapshot.HasText && !snapshot.HasImage) {
				lastSeq = snapshot.Sequence
				retryNotBefore = time.Time{}
				continue
			}
			if !retryNotBefore.IsZero() && time.Now().Before(retryNotBefore) {
				continue
			}
			rec, err := a.captureSnapshot(ctx, snapshot)
			if err != nil {
				pingCtx, cancel := context.WithTimeout(ctx, time.Second)
				pingErr := a.store.Ping(pingCtx)
				cancel()
				if pingErr != nil {
					if a.storageHealthy.Swap(false) {
						log.Printf("clipboard capture deferred while storage unavailable: %v", err)
					}
					retryNotBefore = time.Now().Add(2 * time.Second)
					continue
				}
				lastSeq = snapshot.Sequence
				retryNotBefore = time.Time{}
				log.Printf("clipboard capture failed: %v", err)
				continue
			}
			lastSeq = snapshot.Sequence
			retryNotBefore = time.Time{}
			if a.cfg.OnRecord != nil {
				a.cfg.OnRecord(rec)
			}
			a.broadcastLocalEvent(domain.SyncEvent{
				EventType: domain.EventRecordCreated,
				RecordID:  rec.ID,
				Occurred:  time.Now().UTC(),
				DeviceID:  a.cfg.DeviceID,
			})
		}
	}
}

func (a *Agent) captureSnapshot(ctx context.Context, snapshot win32.ClipboardSnapshot) (domain.ClipboardRecord, error) {
	if snapshot.HasImage {
		return a.service.CaptureImage(ctx, snapshot.ImageMIMEType, snapshot.ImageData)
	}
	return a.service.CaptureText(ctx, snapshot.Text)
}

func (a *Agent) runSyncLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !a.storageHealthy.Load() {
				continue
			}
			if err := a.sync.FlushQueue(ctx, a.store, 50, syncer.DefaultQueueBatchBytes); err != nil {
				pingCtx, cancel := context.WithTimeout(ctx, time.Second)
				pingErr := a.store.Ping(pingCtx)
				cancel()
				if pingErr != nil {
					a.storageHealthy.Store(false)
					continue
				}
				log.Printf("sync flush failed: %v", err)
			}
		}
	}
}

func (a *Agent) runEventLoop(ctx context.Context) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		err := a.sync.Subscribe(ctx, a.cfg.DeviceID, func(event domain.SyncEvent) error {
			switch event.EventType {
			case domain.EventRecordCreated, domain.EventRecordUpdated:
				var record domain.ClipboardRecord
				if event.Record != nil {
					record = *event.Record
				} else if event.RecordID != "" {
					var err error
					record, err = a.sync.GetRecord(ctx, event.RecordID)
					if err != nil {
						return err
					}
				}
				if record.ID != "" {
					if err := a.store.UpsertRecord(ctx, record); err != nil {
						return err
					}
					event.RecordID = record.ID
					event.Record = nil
				}
			case domain.EventRecordDeleted:
				if event.RecordID != "" {
					if err := a.store.DeleteRecords(ctx, []string{event.RecordID}); err != nil {
						return err
					}
				}
			}
			if a.cfg.OnRemoteEvent != nil {
				a.cfg.OnRemoteEvent(event)
			}
			a.broadcastLocalEvent(event)
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		log.Printf("sync event stream disconnected: %v", err)
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (a *Agent) broadcastLocalEvent(event domain.SyncEvent) {
	if a.events != nil {
		a.events.Broadcast(event)
	}
}

func (a *Agent) runHotkey(ctx context.Context) {
	err := win32.RegisterAltV(ctx, func() {
		if err := win32.OpenURL("http://" + a.cfg.UIAddr); err != nil {
			log.Printf("open UI failed: %v", err)
		}
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("Alt+V hotkey unavailable: %v", err)
	}
}

var _ http.Handler = (*Agent)(nil)
