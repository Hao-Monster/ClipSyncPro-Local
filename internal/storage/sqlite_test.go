package storage

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clipsyncpro/internal/domain"
	"clipsyncpro/internal/security"
)

func TestOpenSQLiteFromEnvAndPragmas(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("CLIPSYNC_DATA_DIR", tempDir)
	t.Setenv("CLIPSYNC_SQLITE_PATH", "")

	ctx := context.Background()
	store, err := OpenSQLiteFromEnv(ctx)
	if err != nil {
		t.Fatalf("OpenSQLiteFromEnv() error = %v", err)
	}
	defer store.Close()

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	expectedPath := filepath.Join(tempDir, "clipsync.db")
	if store.Path() != expectedPath {
		t.Fatalf("store.Path() = %q, want %q", store.Path(), expectedPath)
	}

	var journalMode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := store.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}

	var foreignKeys int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestSQLiteStoreRecordsSummariesFavoriteAndSoftDelete(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "nested", "clipsync.db")
	store, err := OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	defer store.Close()

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)

	// 1. Plain text record
	textRec, err := domain.NewClipboardRecord("local", "dev-win", domain.PlatformWindows, "Hello SQLite 剪贴板", baseTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRecord(ctx, textRec); err != nil {
		t.Fatalf("UpsertRecord(text): %v", err)
	}

	gotText, err := store.GetRecord(ctx, textRec.ID)
	if err != nil {
		t.Fatalf("GetRecord(text): %v", err)
	}
	if gotText.ContentText != "Hello SQLite 剪贴板" || len([]rune(gotText.ContentText)) != 16 {
		t.Fatalf("unexpected text record: %+v", gotText)
	}

	// 2. Image record with large blob + caption
	imgBytes := bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, 512)
	imgRec, err := domain.NewClipboardImageRecord("local", "dev-win", domain.PlatformWindows, "image/png", imgBytes, baseTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	imgRec.ContentText = "架构图截图"
	if err := store.UpsertRecord(ctx, imgRec); err != nil {
		t.Fatalf("UpsertRecord(image): %v", err)
	}

	gotImg, err := store.GetRecord(ctx, imgRec.ID)
	if err != nil {
		t.Fatalf("GetRecord(image): %v", err)
	}
	if gotImg.ImageMIMEType != "image/png" || !bytes.Equal(gotImg.ImageData, imgBytes) || gotImg.ContentText != "架构图截图" {
		t.Fatalf("GetRecord(image) did not preserve binary blob or caption")
	}

	// Verify ListRecordSummaries omits image_blob & image_mime but preserves ContentText/CharCount/summary
	summaries, err := store.ListRecordSummaries(ctx, Filter{UserID: "local", Limit: 10})
	if err != nil {
		t.Fatalf("ListRecordSummaries: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries len = %d, want 2", len(summaries))
	}
	var imgSummary domain.ClipboardRecord
	for _, s := range summaries {
		if s.ID == imgRec.ID {
			imgSummary = s
		}
	}
	if len(imgSummary.ImageData) != 0 || imgSummary.ImageMIMEType != "" {
		t.Fatalf("ListRecordSummaries leaked image_blob (%d bytes) or mime (%q)", len(imgSummary.ImageData), imgSummary.ImageMIMEType)
	}
	if imgSummary.ContentText != "架构图截图" || domain.RedactedSummary(imgSummary) != "图片 · 架构图截图" {
		t.Fatalf("ListRecordSummaries lost caption/summary: %+v", imgSummary)
	}

	// 3. SetFavorite & FavoriteOnly / ExcludeFavorite
	if err := store.SetFavorite(ctx, textRec.ID, true); err != nil {
		t.Fatalf("SetFavorite(true): %v", err)
	}
	favList, err := store.ListRecordSummaries(ctx, Filter{UserID: "local", FavoriteOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(favList) != 1 || favList[0].ID != textRec.ID || !favList[0].IsFavorite || favList[0].FavoriteAt == nil {
		t.Fatalf("FavoriteOnly list = %+v", favList)
	}

	unfavList, err := store.ListRecordSummaries(ctx, Filter{UserID: "local", ExcludeFavorite: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(unfavList) != 1 || unfavList[0].ID != imgRec.ID {
		t.Fatalf("ExcludeFavorite list = %+v", unfavList)
	}

	if err := store.SetFavorite(ctx, "non-existent-id", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetFavorite(missing) err = %v, want ErrNotFound", err)
	}

	// 4. DeleteRecords sets deleted_at (soft delete)
	if err := store.DeleteRecords(ctx, []string{textRec.ID}); err != nil {
		t.Fatalf("DeleteRecords: %v", err)
	}
	if _, err := store.GetRecord(ctx, textRec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRecord after soft delete err = %v, want ErrNotFound", err)
	}
	activeRecords, err := store.ListRecords(ctx, Filter{UserID: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeRecords) != 1 || activeRecords[0].ID != imgRec.ID {
		t.Fatalf("active records after soft delete = %+v", activeRecords)
	}
	allWithDeleted, err := store.ListRecords(ctx, Filter{UserID: "local", IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(allWithDeleted) != 2 {
		t.Fatalf("IncludeDeleted count = %d, want 2", len(allWithDeleted))
	}
	var deletedRec *domain.ClipboardRecord
	for i := range allWithDeleted {
		if allWithDeleted[i].ID == textRec.ID {
			deletedRec = &allWithDeleted[i]
		}
	}
	if deletedRec == nil || deletedRec.DeletedAt == nil {
		t.Fatalf("expected soft-deleted record to have non-nil DeletedAt, got %+v", deletedRec)
	}
}

func TestSQLiteStoreSensitiveSearchDateRangeAndPagination(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "clipsync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	day1 := time.Date(2026, 6, 1, 14, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 6, 2, 9, 30, 0, 0, time.Local)

	// Create 3 plain text records on day1 and 1 sensitive record on day2
	for i, text := range []string{"alpha keyword item", "beta keyword item", "gamma other item"} {
		rec, err := domain.NewClipboardRecord("local", "dev", domain.PlatformWindows, text, day1.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertRecord(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	sensRec, err := domain.NewClipboardRecord("local", "dev", domain.PlatformWindows, "GitHub prod-admin@example.com", day2)
	if err != nil {
		t.Fatal(err)
	}
	sensRec.IsSensitive = true
	sensRec.SensitiveType = domain.SensitiveAccount
	sensRec.TargetPlatform = "GitHub"
	sensRec.AccountCipher, _ = security.EncryptString(secret, "prod-admin@example.com")
	sensRec.PasswordCipher, _ = security.EncryptString(secret, "S3cretP@ss!")
	sensRec.TwoFactorCipher, _ = security.EncryptString(secret, "JBSWY3DPEHPK3PXP")
	sensRec.RemarkCipher, _ = security.EncryptString(secret, "核心生产库")
	sensRec.ContentCipher, _ = security.EncryptString(secret, "S3cretP@ss!")
	sensRec.SearchIndex = security.BuildSearchIndex(secret, "GitHub", "prod-admin@example.com", "核心生产库")
	if err := store.UpsertRecord(ctx, sensRec); err != nil {
		t.Fatal(err)
	}

	// SensitiveOnly + KeywordHashes search
	kwHashes := security.BuildQueryIndex(secret, "prod-admin")
	sensHits, err := store.ListRecordSummaries(ctx, Filter{
		UserID:        "local",
		SensitiveOnly: true,
		Keyword:       "prod-admin",
		KeywordHashes: kwHashes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sensHits) != 1 || sensHits[0].ID != sensRec.ID || !sensHits[0].IsSensitive {
		t.Fatalf("expected 1 sensitive hit, got %+v", sensHits)
	}

	// Plain text keyword search
	kwHits, err := store.ListRecordSummaries(ctx, Filter{
		UserID:  "local",
		Keyword: "keyword",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kwHits) != 2 {
		t.Fatalf("expected 2 plain keyword hits, got %d", len(kwHits))
	}

	// Date range filter (only day1)
	start, end, err := domain.ParseFilterDate("20260601")
	if err != nil {
		t.Fatal(err)
	}
	day1Records, err := store.ListRecordSummaries(ctx, Filter{
		UserID:    "local",
		DateStart: &start,
		DateEnd:   &end,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(day1Records) != 3 {
		t.Fatalf("expected 3 records on 20260601, got %d", len(day1Records))
	}

	// Limit / Offset pagination
	page1, err := store.ListRecordSummaries(ctx, Filter{UserID: "local", Limit: 2, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	page2, err := store.ListRecordSummaries(ctx, Filter{UserID: "local", Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || len(page2) != 2 || page1[0].ID == page2[0].ID {
		t.Fatalf("unexpected pagination results: page1=%d page2=%d", len(page1), len(page2))
	}
}

func TestSQLiteStorePromptsCRUDAndOrdering(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "clipsync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Seed prompts should exist automatically
	initial, err := store.ListPrompts(ctx, "", "")
	if err != nil {
		t.Fatalf("ListPrompts initial: %v", err)
	}
	if len(initial) == 0 {
		t.Fatal("expected seeded default prompts")
	}

	p1 := &domain.PromptTemplate{
		Title:           "Go 单元测试生成",
		Category:        "代码开发",
		TemplateContent: "请为以下 Go 函数编写表驱动测试：\n{clip}",
		Shortcut:        "/gotest",
		IsFavorite:      false,
	}
	if err := store.SavePrompt(ctx, p1); err != nil {
		t.Fatalf("SavePrompt(p1): %v", err)
	}
	if p1.ID == "" || len(p1.Variables) != 1 || p1.Variables[0] != "clip" {
		t.Fatalf("SavePrompt did not populate ID or Variables: %+v", p1)
	}

	p2 := &domain.PromptTemplate{
		Title:           "SQL 慢查询诊断",
		Category:        "代码开发",
		TemplateContent: "分析以下 SQL 执行计划：{sql_text}",
		Shortcut:        "/sqlplan",
		IsFavorite:      true,
	}
	if err := store.SavePrompt(ctx, p2); err != nil {
		t.Fatalf("SavePrompt(p2): %v", err)
	}

	// Increment usage on p1 twice
	if err := store.IncrementPromptUsage(ctx, p1.ID); err != nil {
		t.Fatalf("IncrementPromptUsage: %v", err)
	}
	if err := store.IncrementPromptUsage(ctx, p1.ID); err != nil {
		t.Fatalf("IncrementPromptUsage 2: %v", err)
	}

	// Updating p1 should preserve UsageCount
	p1.Title = "Go 单元测试生成 Pro"
	if err := store.SavePrompt(ctx, p1); err != nil {
		t.Fatalf("SavePrompt update: %v", err)
	}
	gotP1, err := store.GetPrompt(ctx, p1.ID)
	if err != nil {
		t.Fatalf("GetPrompt(p1): %v", err)
	}
	if gotP1.UsageCount != 2 || gotP1.Title != "Go 单元测试生成 Pro" {
		t.Fatalf("updated prompt = %+v", gotP1)
	}

	// Filter by category and keyword, check ordering (is_favorite DESC, usage_count DESC, updated_at DESC)
	devPrompts, err := store.ListPrompts(ctx, "代码开发", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(devPrompts) < 2 {
		t.Fatalf("expected at least 2 dev prompts, got %d", len(devPrompts))
	}
	// All favorite prompts in devPrompts must appear before non-favorite prompts
	seenNonFav := false
	for _, p := range devPrompts {
		if !p.IsFavorite {
			seenNonFav = true
		} else if seenNonFav {
			t.Fatalf("favorite prompt %q appeared after non-favorite prompt", p.Title)
		}
	}

	kwPrompts, err := store.ListPrompts(ctx, "代码开发", "gotest")
	if err != nil {
		t.Fatal(err)
	}
	if len(kwPrompts) != 1 || kwPrompts[0].ID != p1.ID {
		t.Fatalf("keyword filter prompts = %+v", kwPrompts)
	}

	// DeletePrompt
	if err := store.DeletePrompt(ctx, p1.ID); err != nil {
		t.Fatalf("DeletePrompt: %v", err)
	}
	if _, err := store.GetPrompt(ctx, p1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after DeletePrompt, got %v", err)
	}
	if err := store.IncrementPromptUsage(ctx, p1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on IncrementPromptUsage for deleted prompt, got %v", err)
	}
}

func TestSQLiteStoreSyncQueueContract(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "clipsync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id1, err := store.Enqueue(ctx, SyncQueueItem{RecordID: "rec-1", Action: QueueCreate})
	if err != nil {
		t.Fatalf("Enqueue create: %v", err)
	}
	id2, err := store.Enqueue(ctx, SyncQueueItem{RecordID: "rec-1", Action: QueueUpdate})
	if err != nil {
		t.Fatalf("Enqueue update: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("expected Enqueue for same RecordID to coalesce id %d vs %d", id1, id2)
	}

	pending, err := store.ListPendingQueue(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingQueue: %v", err)
	}
	if len(pending) != 1 || pending[0].Action != QueueUpdate {
		t.Fatalf("unexpected pending queue: %+v", pending)
	}

	if err := store.MarkQueueRetry(ctx, id1, "temporary error"); err != nil {
		t.Fatalf("MarkQueueRetry: %v", err)
	}
	dueNow, err := store.ListPendingQueue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dueNow) != 0 {
		t.Fatalf("expected backed-off item not to be immediately due, got %+v", dueNow)
	}

	if err := store.MarkQueueDone(ctx, id1); err != nil {
		t.Fatalf("MarkQueueDone: %v", err)
	}
}

func TestSQLiteLikeWildcardEscapingAndSubSecondTimestamps(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "clipsync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// 1. Test sub-second timestamp ordering (.000000000Z vs .500000000Z) and range filtering
	tWhole := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	tHalf := time.Date(2026, 6, 15, 12, 0, 0, 500_000_000, time.UTC)

	recWhole, err := domain.NewClipboardRecord("local", "dev", domain.PlatformWindows, "plain record alpha", tWhole)
	if err != nil {
		t.Fatal(err)
	}
	recHalf, err := domain.NewClipboardRecord("local", "dev", domain.PlatformWindows, "100% discount on user_id C:\\temp\\file", tHalf)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRecord(ctx, recWhole); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRecord(ctx, recHalf); err != nil {
		t.Fatal(err)
	}

	ordered, err := store.ListRecordSummaries(ctx, Filter{UserID: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 2 || ordered[0].ID != recHalf.ID || ordered[1].ID != recWhole.ID {
		t.Fatalf("expected .500000000Z record before .000000000Z record, got %+v", ordered)
	}

	rangeStart := time.Date(2026, 6, 15, 12, 0, 0, 100_000_000, time.UTC)
	rangeEnd := time.Date(2026, 6, 15, 12, 0, 0, 900_000_000, time.UTC)
	inRange, err := store.ListRecordSummaries(ctx, Filter{
		UserID:    "local",
		DateStart: &rangeStart,
		DateEnd:   &rangeEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inRange) != 1 || inRange[0].ID != recHalf.ID {
		t.Fatalf("expected only .500000000Z record in sub-second range, got %+v", inRange)
	}

	// 2. Test LIKE wildcard escaping (%, _, \) in ListRecordSummaries
	for _, kw := range []string{"%", "_", `\`} {
		hits, err := store.ListRecordSummaries(ctx, Filter{UserID: "local", Keyword: kw})
		if err != nil {
			t.Fatalf("ListRecordSummaries(keyword=%q): %v", kw, err)
		}
		if len(hits) != 1 || hits[0].ID != recHalf.ID {
			t.Fatalf("keyword %q should only match recHalf, got %d hits: %+v", kw, len(hits), hits)
		}
	}

	// 3. Test LIKE wildcard escaping (%, _, \) in ListPrompts
	specialPrompt := &domain.PromptTemplate{
		Title:           "Printf 100% 格式化_模板",
		Category:        "测试分类",
		TemplateContent: `使用 %s 和 user_id 以及路径 C:\work\demo 输出`,
		Shortcut:        "/fmt_100%",
	}
	plainPrompt := &domain.PromptTemplate{
		Title:           "普通润色模板",
		Category:        "测试分类",
		TemplateContent: "请润色这段普通文本",
		Shortcut:        "/polish",
	}
	if err := store.SavePrompt(ctx, specialPrompt); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePrompt(ctx, plainPrompt); err != nil {
		t.Fatal(err)
	}

	for _, kw := range []string{"%", "_", `\`} {
		pHits, err := store.ListPrompts(ctx, "测试分类", kw)
		if err != nil {
			t.Fatalf("ListPrompts(keyword=%q): %v", kw, err)
		}
		if len(pHits) != 1 || pHits[0].ID != specialPrompt.ID {
			t.Fatalf("prompt keyword %q should only match specialPrompt, got %d hits", kw, len(pHits))
		}
	}

	// 4. Test UpsertRecord preserves client_created_at and defaults favorite_at when IsFavorite=true
	favNoTime, err := domain.NewClipboardRecord("local", "dev", domain.PlatformWindows, "fav without explicit time", tWhole)
	if err != nil {
		t.Fatal(err)
	}
	favNoTime.IsFavorite = true
	favNoTime.FavoriteAt = nil
	if err := store.UpsertRecord(ctx, favNoTime); err != nil {
		t.Fatal(err)
	}
	gotFav, err := store.GetRecord(ctx, favNoTime.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotFav.IsFavorite || gotFav.FavoriteAt == nil {
		t.Fatalf("expected UpsertRecord to default FavoriteAt when IsFavorite=true, got %+v", gotFav)
	}

	laterTime := tWhole.Add(10 * time.Minute)
	favNoTime.ClientCreatedAt = laterTime
	favNoTime.UpdatedAt = laterTime
	if err := store.UpsertRecord(ctx, favNoTime); err != nil {
		t.Fatal(err)
	}
	gotFavAfter, err := store.GetRecord(ctx, favNoTime.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gotFavAfter.ClientCreatedAt.Equal(gotFav.ClientCreatedAt) {
		t.Fatalf("expected UpsertRecord ON CONFLICT to preserve ClientCreatedAt %v, got %v", gotFav.ClientCreatedAt, gotFavAfter.ClientCreatedAt)
	}
}

