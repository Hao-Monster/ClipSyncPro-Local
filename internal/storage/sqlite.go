package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"clipsyncpro/internal/domain"
	"clipsyncpro/internal/security"
)

const sqliteTimeLayout = "2006-01-02T15:04:05.000000000Z"

const sqliteSchemaSQL = `
CREATE TABLE IF NOT EXISTS clipboard_record (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    content_type TEXT NOT NULL,
    content_text TEXT NULL,
    image_mime TEXT NULL,
    image_blob BLOB NULL,
    content_cipher TEXT NULL,
    content_hash TEXT NOT NULL,
    is_sensitive INTEGER NOT NULL DEFAULT 0,
    sensitive_type TEXT NULL,
    account_cipher TEXT NULL,
    password_cipher TEXT NULL,
    remark_cipher TEXT NULL,
    two_factor_cipher TEXT NULL,
    search_index TEXT NULL,
    source_device_id TEXT NOT NULL,
    source_platform TEXT NOT NULL,
    target_platform TEXT NULL,
    client_created_at TEXT NOT NULL,
    server_created_at TEXT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT NULL,
    is_favorite INTEGER NOT NULL DEFAULT 0,
    favorite_at TEXT NULL,
    sync_status TEXT NOT NULL DEFAULT 'pending'
);

CREATE INDEX IF NOT EXISTS idx_sqlite_user_created_at ON clipboard_record (user_id, client_created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sqlite_hash ON clipboard_record (content_hash);
CREATE INDEX IF NOT EXISTS idx_sqlite_sensitive ON clipboard_record (user_id, is_sensitive, sensitive_type);
CREATE INDEX IF NOT EXISTS idx_sqlite_favorite ON clipboard_record (user_id, is_favorite, favorite_at);
CREATE INDEX IF NOT EXISTS idx_sqlite_deleted_at ON clipboard_record (deleted_at);

CREATE TABLE IF NOT EXISTS prompt_template (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT '通用',
    template_content TEXT NOT NULL,
    variables_json TEXT NULL,
    shortcut TEXT NULL,
    usage_count INTEGER NOT NULL DEFAULT 0,
    is_favorite INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sqlite_prompts_category ON prompt_template (category);
CREATE INDEX IF NOT EXISTS idx_sqlite_prompts_order ON prompt_template (is_favorite DESC, usage_count DESC, updated_at DESC);

CREATE TABLE IF NOT EXISTS sync_queue_v2 (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    record_id TEXT NOT NULL UNIQUE,
    action TEXT NOT NULL,
    retry_count INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    last_error TEXT NULL,
    next_attempt_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sqlite_sync_queue_schedule ON sync_queue_v2 (status, retry_count, next_attempt_at, created_at);
`

type SQLiteStore struct {
	db   *sql.DB
	path string
}

// ResolveSQLitePath resolves the local SQLite database path.
// Priority: CLIPSYNC_SQLITE_PATH -> CLIPSYNC_DATA_DIR/clipsync.db -> %LOCALAPPDATA%\ClipSync Pro\clipsync.db.
func ResolveSQLitePath() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("CLIPSYNC_SQLITE_PATH")); custom != "" {
		return custom, nil
	}
	dir, err := security.DefaultDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "clipsync.db"), nil
}

func OpenSQLiteFromEnv(ctx context.Context) (*SQLiteStore, error) {
	dbPath, err := ResolveSQLitePath()
	if err != nil {
		return nil, err
	}
	return OpenSQLite(ctx, dbPath)
}

func OpenSQLite(ctx context.Context, dbPath string) (*SQLiteStore, error) {
	cleanPath := filepath.Clean(strings.TrimSpace(dbPath))
	if cleanPath == "" || cleanPath == "." {
		return nil, errors.New("sqlite database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return nil, err
	}
	dsn := cleanPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
PRAGMA synchronous=NORMAL;
`); err != nil {
		_ = db.Close()
		return nil, err
	}

	store := &SQLiteStore{
		db:   db,
		path: cleanPath,
	}
	if err := store.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Path() string {
	return s.path
}

func (s *SQLiteStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) EnsureSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, sqliteSchemaSQL); err != nil {
		return err
	}
	var promptCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM prompt_template").Scan(&promptCount); err == nil && promptCount == 0 {
		for _, p := range domain.DefaultPrompts() {
			cp := *p
			_ = s.SavePrompt(ctx, &cp)
		}
	}
	return nil
}

func (s *SQLiteStore) UpsertRecord(ctx context.Context, rec domain.ClipboardRecord) error {
	var err error
	rec, err = domain.ClipboardRecordForStorage(rec)
	if err != nil {
		return err
	}
	contentText := rec.ContentText
	var imageMIME, imageData any
	if rec.ContentType == domain.ContentImage {
		imageMIME = rec.ImageMIMEType
		imageData = rec.ImageData
	}
	contentCipher, err := jsonOrNull(rec.ContentCipher)
	if err != nil {
		return err
	}
	accountCipher, err := jsonOrNull(rec.AccountCipher)
	if err != nil {
		return err
	}
	passwordCipher, err := jsonOrNull(rec.PasswordCipher)
	if err != nil {
		return err
	}
	remarkCipher, err := jsonOrNull(rec.RemarkCipher)
	if err != nil {
		return err
	}
	twoFactorCipher, err := jsonOrNull(rec.TwoFactorCipher)
	if err != nil {
		return err
	}
	searchIndex, err := json.Marshal(rec.SearchIndex)
	if err != nil {
		return err
	}
	now := time.Now()
	if rec.ClientCreatedAt.IsZero() {
		rec.ClientCreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	if rec.IsFavorite && rec.FavoriteAt == nil {
		favAt := rec.UpdatedAt
		rec.FavoriteAt = &favAt
	}
	if rec.SyncStatus == "" {
		rec.SyncStatus = domain.SyncPending
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO clipboard_record (
    id, user_id, content_type, content_text, image_mime, image_blob, content_cipher, content_hash,
    is_sensitive, sensitive_type, account_cipher, password_cipher, remark_cipher, two_factor_cipher,
    search_index, source_device_id, source_platform, target_platform, client_created_at,
    server_created_at, updated_at, deleted_at, is_favorite, favorite_at, sync_status
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    user_id=excluded.user_id,
    content_type=excluded.content_type,
    content_text=excluded.content_text,
    image_mime=excluded.image_mime,
    image_blob=excluded.image_blob,
    content_cipher=excluded.content_cipher,
    content_hash=excluded.content_hash,
    is_sensitive=excluded.is_sensitive,
    sensitive_type=excluded.sensitive_type,
    account_cipher=excluded.account_cipher,
    password_cipher=excluded.password_cipher,
    remark_cipher=excluded.remark_cipher,
    two_factor_cipher=excluded.two_factor_cipher,
    search_index=excluded.search_index,
    source_device_id=excluded.source_device_id,
    source_platform=excluded.source_platform,
    target_platform=excluded.target_platform,
    client_created_at=clipboard_record.client_created_at,
    server_created_at=excluded.server_created_at,
    updated_at=excluded.updated_at,
    deleted_at=excluded.deleted_at,
    is_favorite=excluded.is_favorite,
    favorite_at=excluded.favorite_at,
    sync_status=excluded.sync_status`,
		rec.ID,
		rec.UserID,
		string(rec.ContentType),
		nullIfEmpty(contentText),
		imageMIME,
		imageData,
		contentCipher,
		rec.ContentHash,
		boolToInt(rec.IsSensitive),
		nullIfEmpty(string(rec.SensitiveType)),
		accountCipher,
		passwordCipher,
		remarkCipher,
		twoFactorCipher,
		string(searchIndex),
		rec.SourceDeviceID,
		string(rec.SourcePlatform),
		nullIfEmpty(rec.TargetPlatform),
		formatSQLiteTime(rec.ClientCreatedAt),
		formatSQLiteTimePtr(rec.ServerCreatedAt),
		formatSQLiteTime(rec.UpdatedAt),
		formatSQLiteTimePtr(rec.DeletedAt),
		boolToInt(rec.IsFavorite),
		formatSQLiteTimePtr(rec.FavoriteAt),
		string(rec.SyncStatus),
	)
	return err
}

func (s *SQLiteStore) GetRecord(ctx context.Context, id string) (domain.ClipboardRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user_id, content_type, content_text, image_mime, image_blob, content_cipher, content_hash,
       is_sensitive, sensitive_type, account_cipher, password_cipher, remark_cipher, two_factor_cipher,
       search_index, source_device_id, source_platform, target_platform, client_created_at,
       server_created_at, updated_at, deleted_at, is_favorite, favorite_at, sync_status
FROM clipboard_record
WHERE id = ? AND deleted_at IS NULL
LIMIT 1`, id)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanSQLiteRecord(rows)
	}
	return domain.ClipboardRecord{}, ErrNotFound
}

func (s *SQLiteStore) ListRecords(ctx context.Context, filter Filter) ([]domain.ClipboardRecord, error) {
	return s.listRecords(ctx, filter, false)
}

func (s *SQLiteStore) ListRecordSummaries(ctx context.Context, filter Filter) ([]domain.ClipboardRecord, error) {
	return s.listRecords(ctx, filter, true)
}

func (s *SQLiteStore) listRecords(ctx context.Context, filter Filter, summariesOnly bool) ([]domain.ClipboardRecord, error) {
	clauses := []string{"1=1"}
	args := make([]any, 0)
	if filter.UserID != "" {
		clauses = append(clauses, "user_id = ?")
		args = append(args, filter.UserID)
	}
	if !filter.IncludeDeleted {
		clauses = append(clauses, "deleted_at IS NULL")
	}
	if filter.SensitiveOnly {
		clauses = append(clauses, "is_sensitive = 1")
	}
	if filter.FavoriteOnly {
		clauses = append(clauses, "is_favorite = 1")
	}
	if filter.ExcludeFavorite {
		clauses = append(clauses, "is_favorite = 0")
	}
	if filter.DateStart != nil {
		clauses = append(clauses, "client_created_at >= ?")
		args = append(args, formatSQLiteTime(*filter.DateStart))
	}
	if filter.DateEnd != nil {
		clauses = append(clauses, "client_created_at <= ?")
		args = append(args, formatSQLiteTime(*filter.DateEnd))
	}
	if filter.Keyword != "" {
		if len(filter.KeywordHashes) > 0 {
			clauses = append(clauses, `((content_type <> 'image' AND LOWER(COALESCE(content_text, '')) LIKE ? ESCAPE '\') OR is_sensitive = 1)`)
		} else {
			clauses = append(clauses, `(content_type <> 'image' AND LOWER(COALESCE(content_text, '')) LIKE ? ESCAPE '\')`)
		}
		args = append(args, "%"+escapeSQLiteLike(strings.ToLower(filter.Keyword))+"%")
	}

	contentColumn := "content_text"
	imageMIMEColumn := "image_mime"
	imageDataColumn := "image_blob"
	if summariesOnly {
		contentColumn = "CASE WHEN content_type = 'image' AND content_text LIKE 'data:%' THEN NULL ELSE content_text END"
		imageMIMEColumn = "NULL"
		imageDataColumn = "NULL"
	}
	query := `
SELECT id, user_id, content_type, ` + contentColumn + ` AS content_text,
       ` + imageMIMEColumn + ` AS image_mime, ` + imageDataColumn + ` AS image_blob,
       content_cipher, content_hash,
       is_sensitive, sensitive_type, account_cipher, password_cipher, remark_cipher, two_factor_cipher,
       search_index, source_device_id, source_platform, target_platform, client_created_at,
       server_created_at, updated_at, deleted_at, is_favorite, favorite_at, sync_status
FROM clipboard_record
WHERE ` + strings.Join(clauses, " AND ") + `
ORDER BY client_created_at DESC, id DESC`
	if filter.Limit > 0 && filter.Keyword == "" {
		query += " LIMIT ? OFFSET ?"
		args = append(args, filter.Limit, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]domain.ClipboardRecord, 0)
	matches := 0
	for rows.Next() {
		rec, err := scanSQLiteRecord(rows)
		if err != nil {
			return nil, err
		}
		if RecordMatchesFilter(rec, filter) {
			if filter.Keyword != "" && matches < filter.Offset {
				matches++
				continue
			}
			matches++
			records = append(records, rec)
		}
		if filter.Limit > 0 && len(records) >= filter.Limit {
			break
		}
	}
	return records, rows.Err()
}

func (s *SQLiteStore) DeleteRecords(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	nowStr := formatSQLiteTime(time.Now())
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	args = append(args, nowStr, nowStr)
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE clipboard_record
SET deleted_at = ?, updated_at = ?
WHERE id IN (`+placeholders+`)`, args...)
	return err
}

func (s *SQLiteStore) SetFavorite(ctx context.Context, id string, isFavorite bool) error {
	now := time.Now()
	var favoriteAt any
	if isFavorite {
		favoriteAt = formatSQLiteTime(now)
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE clipboard_record
SET is_favorite = ?, favorite_at = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL`, boolToInt(isFavorite), favoriteAt, formatSQLiteTime(now), id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) Enqueue(ctx context.Context, item SyncQueueItem) (int64, error) {
	now := time.Now()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	nowStr := formatSQLiteTime(now)
	createdStr := formatSQLiteTime(item.CreatedAt)
	updatedStr := formatSQLiteTime(item.UpdatedAt)

	var id int64
	err := s.db.QueryRowContext(ctx, `
INSERT INTO sync_queue_v2 (
    record_id, action, retry_count, status, last_error, next_attempt_at, created_at, updated_at
) VALUES (?, ?, 0, 'pending', NULL, ?, ?, ?)
ON CONFLICT(record_id) DO UPDATE SET
    action=excluded.action,
    retry_count=0,
    status='pending',
    last_error=NULL,
    next_attempt_at=excluded.next_attempt_at,
    updated_at=excluded.updated_at
RETURNING id`,
		item.RecordID, string(item.Action), nowStr, createdStr, updatedStr).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *SQLiteStore) ListPendingQueue(ctx context.Context, limit int) ([]SyncQueueItem, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, record_id, action, retry_count, status, last_error, next_attempt_at, created_at, updated_at
FROM sync_queue_v2
WHERE status IN ('pending', 'failed')
  AND next_attempt_at <= ?
ORDER BY retry_count ASC, next_attempt_at ASC, created_at ASC
LIMIT ?`, formatSQLiteTime(time.Now()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]SyncQueueItem, 0)
	for rows.Next() {
		var item SyncQueueItem
		var lastError, nextAttemptStr, createdStr, updatedStr sql.NullString
		if err := rows.Scan(&item.ID, &item.RecordID, &item.Action, &item.RetryCount, &item.Status, &lastError, &nextAttemptStr, &createdStr, &updatedStr); err != nil {
			return nil, err
		}
		item.LastError = lastError.String
		item.NextAttemptAt = parseSQLiteTime(nextAttemptStr.String)
		item.CreatedAt = parseSQLiteTime(createdStr.String)
		item.UpdatedAt = parseSQLiteTime(updatedStr.String)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLiteStore) MarkQueueDone(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sync_queue_v2 WHERE id = ?", id)
	return err
}

func (s *SQLiteStore) MarkQueueRetry(ctx context.Context, id int64, errText string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var retryCount int
	err = tx.QueryRowContext(ctx, "SELECT retry_count FROM sync_queue_v2 WHERE id = ?", id).Scan(&retryCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	now := time.Now()
	retryCount++
	nextAttempt := now.Add(QueueRetryDelay(retryCount))
	if _, err := tx.ExecContext(ctx, `
UPDATE sync_queue_v2
SET status = 'failed', retry_count = ?, last_error = ?, next_attempt_at = ?, updated_at = ?
WHERE id = ?`,
		retryCount, truncateRunes(errText, 4000), formatSQLiteTime(nextAttempt), formatSQLiteTime(now), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) ListPrompts(ctx context.Context, category, keyword string) ([]*domain.PromptTemplate, error) {
	clauses := []string{"1=1"}
	args := make([]any, 0)
	cat := strings.TrimSpace(category)
	if cat != "" && cat != "全部" {
		clauses = append(clauses, "category = ?")
		args = append(args, cat)
	}
	kw := strings.TrimSpace(keyword)
	kwLower := strings.ToLower(kw)
	if kwLower != "" {
		clauses = append(clauses, `(LOWER(title) LIKE ? ESCAPE '\' OR LOWER(template_content) LIKE ? ESCAPE '\' OR LOWER(COALESCE(shortcut, '')) LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeSQLiteLike(kwLower) + "%"
		args = append(args, pattern, pattern, pattern)
	}
	query := `
SELECT id, title, category, template_content, variables_json, shortcut, usage_count, is_favorite, created_at, updated_at
FROM prompt_template
WHERE ` + strings.Join(clauses, " AND ") + `
ORDER BY is_favorite DESC, usage_count DESC, updated_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]*domain.PromptTemplate, 0)
	for rows.Next() {
		p, err := scanSQLitePrompt(rows)
		if err != nil {
			return nil, err
		}
		if kwLower != "" {
			if !strings.Contains(strings.ToLower(p.Title), kwLower) &&
				!strings.Contains(strings.ToLower(p.TemplateContent), kwLower) &&
				!strings.Contains(strings.ToLower(p.Shortcut), kwLower) {
				continue
			}
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (s *SQLiteStore) GetPrompt(ctx context.Context, id string) (*domain.PromptTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, title, category, template_content, variables_json, shortcut, usage_count, is_favorite, created_at, updated_at
FROM prompt_template WHERE id = ? LIMIT 1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanSQLitePrompt(rows)
	}
	return nil, ErrNotFound
}

func (s *SQLiteStore) SavePrompt(ctx context.Context, prompt *domain.PromptTemplate) error {
	if prompt.ID == "" {
		newP := domain.NewPromptTemplate(prompt.Title, prompt.Category, prompt.TemplateContent, prompt.Shortcut)
		newP.IsFavorite = prompt.IsFavorite
		*prompt = *newP
	} else {
		existing, err := s.GetPrompt(ctx, prompt.ID)
		switch {
		case err == nil:
			prompt.UsageCount = existing.UsageCount
			prompt.CreatedAt = existing.CreatedAt
		case !errors.Is(err, ErrNotFound):
			return err
		}
	}
	prompt.Variables = domain.ExtractVariables(prompt.TemplateContent)
	varJSON, _ := json.Marshal(prompt.Variables)
	now := time.Now()
	prompt.UpdatedAt = now
	if prompt.CreatedAt.IsZero() {
		prompt.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO prompt_template (
    id, title, category, template_content, variables_json, shortcut, usage_count, is_favorite, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    title=excluded.title,
    category=excluded.category,
    template_content=excluded.template_content,
    variables_json=excluded.variables_json,
    shortcut=excluded.shortcut,
    is_favorite=excluded.is_favorite,
    updated_at=excluded.updated_at`,
		prompt.ID,
		prompt.Title,
		prompt.Category,
		prompt.TemplateContent,
		string(varJSON),
		nullIfEmpty(prompt.Shortcut),
		prompt.UsageCount,
		boolToInt(prompt.IsFavorite),
		formatSQLiteTime(prompt.CreatedAt),
		formatSQLiteTime(prompt.UpdatedAt),
	)
	return err
}

func (s *SQLiteStore) DeletePrompt(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM prompt_template WHERE id = ?", id)
	return err
}

func (s *SQLiteStore) IncrementPromptUsage(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE prompt_template
SET usage_count = usage_count + 1, updated_at = ?
WHERE id = ?`, formatSQLiteTime(time.Now()), id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func scanSQLiteRecord(scanner rowScanner) (domain.ClipboardRecord, error) {
	var rec domain.ClipboardRecord
	var contentText, imageMIME, contentCipher, sensitiveType, accountCipher, passwordCipher, remarkCipher, twoFactorCipher, searchIndex, targetPlatform sql.NullString
	var clientCreatedStr, serverCreatedStr, updatedStr, deletedStr, favoriteStr sql.NullString
	var imageData []byte
	var isSensitiveInt, isFavoriteInt int
	if err := scanner.Scan(
		&rec.ID,
		&rec.UserID,
		&rec.ContentType,
		&contentText,
		&imageMIME,
		&imageData,
		&contentCipher,
		&rec.ContentHash,
		&isSensitiveInt,
		&sensitiveType,
		&accountCipher,
		&passwordCipher,
		&remarkCipher,
		&twoFactorCipher,
		&searchIndex,
		&rec.SourceDeviceID,
		&rec.SourcePlatform,
		&targetPlatform,
		&clientCreatedStr,
		&serverCreatedStr,
		&updatedStr,
		&deletedStr,
		&isFavoriteInt,
		&favoriteStr,
		&rec.SyncStatus,
	); err != nil {
		return domain.ClipboardRecord{}, err
	}
	rec.ContentText = contentText.String
	rec.ImageMIMEType = imageMIME.String
	rec.ImageData = imageData
	rec.IsSensitive = isSensitiveInt != 0
	rec.SensitiveType = domain.SensitiveType(sensitiveType.String)
	rec.TargetPlatform = targetPlatform.String
	rec.ClientCreatedAt = parseSQLiteTime(clientCreatedStr.String)
	rec.ServerCreatedAt = parseSQLiteTimePtr(serverCreatedStr)
	rec.UpdatedAt = parseSQLiteTime(updatedStr.String)
	rec.DeletedAt = parseSQLiteTimePtr(deletedStr)
	rec.IsFavorite = isFavoriteInt != 0
	rec.FavoriteAt = parseSQLiteTimePtr(favoriteStr)
	rec.ContentCipher = parseCipher(contentCipher)
	rec.AccountCipher = parseCipher(accountCipher)
	rec.PasswordCipher = parseCipher(passwordCipher)
	rec.RemarkCipher = parseCipher(remarkCipher)
	rec.TwoFactorCipher = parseCipher(twoFactorCipher)
	if searchIndex.Valid && searchIndex.String != "" {
		_ = json.Unmarshal([]byte(searchIndex.String), &rec.SearchIndex)
	}
	return rec, nil
}

func scanSQLitePrompt(scanner rowScanner) (*domain.PromptTemplate, error) {
	var p domain.PromptTemplate
	var variablesJSON, shortcut, createdStr, updatedStr sql.NullString
	var isFavoriteInt int
	if err := scanner.Scan(
		&p.ID,
		&p.Title,
		&p.Category,
		&p.TemplateContent,
		&variablesJSON,
		&shortcut,
		&p.UsageCount,
		&isFavoriteInt,
		&createdStr,
		&updatedStr,
	); err != nil {
		return nil, err
	}
	p.Shortcut = shortcut.String
	p.IsFavorite = isFavoriteInt != 0
	p.CreatedAt = parseSQLiteTime(createdStr.String)
	p.UpdatedAt = parseSQLiteTime(updatedStr.String)
	if variablesJSON.Valid && variablesJSON.String != "" {
		_ = json.Unmarshal([]byte(variablesJSON.String), &p.Variables)
	}
	if p.Variables == nil {
		p.Variables = domain.ExtractVariables(p.TemplateContent)
	}
	return &p, nil
}

func formatSQLiteTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(sqliteTimeLayout)
}

func formatSQLiteTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(sqliteTimeLayout)
}

func parseSQLiteTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{sqliteTimeLayout, time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.Local()
		}
	}
	return time.Time{}
}

func parseSQLiteTimePtr(raw sql.NullString) *time.Time {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil
	}
	t := parseSQLiteTime(raw.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func escapeSQLiteLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

