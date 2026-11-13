# Detailed Design (Windows Local Edition)

## 1. Record & Prompt Storage Model

- **`clipboard_record`**: Stores plaintext only for non-sensitive records (`content_text`). Image records store normalized PNG bytes in `image_blob` (`image_mime = 'image/png'`), keeping list queries lightweight by serving images lazily via `GET /api/records/{id}/content`.
- **Sensitive Credentials**: Once marked as sensitive (`account` or `password`), `content_text` is cleared and all credential attributes (`content_cipher`, `account_cipher`, `password_cipher`, `remark_cipher`, `two_factor_cipher`) plus `target_platform` are persisted in encrypted form.
- **`prompt_template`**: Stores reusable AI Prompt templates with `title`, `category`, `template_content` (supporting `{{variable}}` placeholders), `shortcut`, `usage_count`, and `is_favorite`. Queries order by `is_favorite DESC, usage_count DESC, updated_at DESC, id DESC`.

## 2. Local Master Key & Encryption Envelope

- On startup, `security.ResolveOrCreateSecret()` checks `CLIPSYNC_SECRET` first; if unset, it loads or atomically creates (`O_WRONLY|O_CREATE|O_EXCL`, mode `0600`) a 32-byte random key (64 hex chars) at `%LOCALAPPDATA%\ClipSync Pro\master.key`.
- Existing corrupted or blank `master.key` files return an explicit error rather than silently overwriting the key and orphaning encrypted records.
- Each encrypted field is wrapped in a JSON envelope (`AES-256-GCM`, `PBKDF2-HMAC-SHA256`, 120,000 iterations, random 16-byte salt, 12-byte GCM nonce). Decryption strictly validates iteration bounds and nonce length before invoking `gcm.Open`.

## 3. Keyed HMAC-SHA256 Blind Index

- To enable fast keyword search over encrypted sensitive records without exposing plaintext in SQLite, `security.BuildSearchIndex()` normalizes text into lowercase rune n-grams and whole tokens, hashing each with HMAC-SHA256 derived from the master secret.
- Multi-word search queries (`security.BuildQueryIndex()`) split on whitespace and compute token hashes per word so multi-term queries match across platform, account, and remark fields.

## 4. Embedded SQLite Concurrency & Reliability

- `storage.OpenSQLite()` injects connection-level PRAGMAs via the driver DSN (`_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)`).
- `LIKE` queries escape `%`, `_`, and `\` using `ESCAPE '\'` and apply Unicode case-insensitive filtering for prompt templates.

## 5. Windows Native Integration & Guarded Clipboard Writes

- Uses Win32 APIs (`GetClipboardSequenceNumber`, `OpenClipboard`, `GetClipboardData`, `SetClipboardData`, `RegisterHotKey`, `SendInput`) in `internal/win32`.
- All agent-initiated clipboard writes (record copy/paste, prompt template injection, and 2FA TOTP copy via `POST /api/clipboard/write`) execute inside `clipboardWriteGuard`, recording the resulting Windows clipboard sequence number so the background polling loop suppresses self-capture.
- Double-clicking or invoking a prompt template writes the assembled prompt via `POST /api/clipboard/write` with `paste: true` and `target_hwnd`, automatically restoring focus to the target chat/IDE window and simulating `Ctrl + V` via `SendInput`.
