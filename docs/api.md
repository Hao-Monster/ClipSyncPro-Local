# Windows Local Agent API

Default loopback base URL:

```text
http://127.0.0.1:37618
```

Override via environment variable `CLIPSYNC_AGENT_ADDR` (compatible with `CLIPSYNC_UI_ADDR` / `CLIPSYNC_UI_URL`).

## Health & Status

```http
GET /api/status
```

Returns agent health, storage status (`ok` / `unavailable`), storage engine (`sqlite`), and `local_only` mode flag.

## Local Event Stream (SSE)

```http
GET /api/events
```

Server-Sent Events (`text/event-stream`) pushing real-time local UI refresh notifications (`record_created`, `record_updated`, `record_deleted`).

## Clipboard Records

### List Records

```http
GET /api/records?keyword=abc&date=20260601&sensitive=true&favorite=true&exclude_favorite=false&offset=0
```

Query parameters:

- `keyword`: optional contains search (supports plaintext `LIKE` with wildcard escaping and HMAC blind-index matching for sensitive records).
- `date`: optional exact date filter in `yyyyMMdd` format.
- `sensitive`: optional `true` to filter account/password records.
- `favorite`: optional `true` to filter starred records.
- `exclude_favorite`: optional `true` to exclude starred records.
- `offset`: optional pagination offset (page size is 10).

### Get Image Binary Content

```http
GET /api/records/{id}/content
```

Returns the normalized `image/png` binary payload of an image clipboard record.

### Reveal Sensitive Record

```http
POST /api/records/{id}/reveal
```

Decrypts a sensitive record (`content`, `platform`, `account`, `password`, `two_factor`, `remark`) and caches the revealed payload in agent memory for 30 minutes.

### Update Record or Credentials

```http
POST /api/records/{id}/update
```

Updates either a standard text record (`content`, `favorite`) or encrypted sensitive credentials (`platform`, `account`, `password`, `two_factor`, `remark`, `favorite`, `is_sensitive`).

### Mark Record as Sensitive

```http
POST /api/records/{id}/mark-password
POST /api/records/{id}/mark-account
POST /api/records/{id}/remark
```

Converts a plaintext record into an AES-256-GCM encrypted credential record or updates its encrypted remark.

### Toggle Favorite

```http
POST /api/records/{id}/favorite
```

Body: `{"is_favorite": true}`.

### Copy or Paste Record Field

```http
POST /api/records/{id}/copy
POST /api/records/{id}/paste
```

Body: `{"field": "content|platform|account|password|two_factor|remark", "target_hwnd": 12345}`. Writes the selected field to the Windows clipboard under `clipboardWriteGuard` protection and (for `/paste`) restores focus to `target_hwnd` before sending `Ctrl + V` via `SendInput`.

### Guarded Direct Clipboard Write & Paste

```http
POST /api/clipboard/write
```

Body: `{"content": "...", "paste": true, "target_hwnd": 12345}`. Writes arbitrary text (such as rendered AI Prompt templates or 2FA TOTP codes) to the system clipboard under `clipboardWriteGuard` suppression so the agent does not re-capture it as a duplicate/plaintext record, and optionally focuses `target_hwnd` and simulates `Ctrl + V` via `SendInput`.

### Capture Current Clipboard

```http
POST /api/clipboard/capture-current
```

Immediately reads and stores current Windows clipboard text or image content.

### Delete Record(s)

```http
DELETE /api/records/{id}
POST /api/records/batch-delete
```

Batch delete body: `{"ids": ["id1", "id2"], "skip_sensitive": true, "skip_favorite": true}`.

## AI Prompt Templates

```http
GET /api/prompts?category=&keyword=
POST /api/prompts
POST /api/prompts/{id}/use
DELETE /api/prompts/{id}
```

Manages structured AI Prompt templates (`title`, `category`, `template_content`, `shortcut`, `is_favorite`, `usage_count`), ordered by `is_favorite DESC, usage_count DESC, updated_at DESC`.
