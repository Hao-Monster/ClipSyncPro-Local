-- ClipSync Pro (Windows Local Edition) SQLite Schema
-- Engine: modernc.org/sqlite (WAL mode, busy_timeout=5000, synchronous=NORMAL, foreign_keys=1)

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
    sync_status TEXT NOT NULL DEFAULT 'local'
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
