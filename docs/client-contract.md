# ClipSync Pro Client Contract (Windows Local Edition)

This document defines the contract between the Windows WinUI 3 desktop client (`windows/ClipSyncPro.WinUI`) and the local Go daemon (`cmd/clipsync-agent`).

## Runtime Boundary

```text
WinUI 3 Desktop Client <-> http://127.0.0.1:37618/api/* <-> Go Local Agent <-> Embedded SQLite (clipsync.db)
```

### Environment Variables

| Variable | Default | Description |
| --- | --- | --- |
| `CLIPSYNC_DATA_DIR` | `%LOCALAPPDATA%\ClipSync Pro` | Root directory for `clipsync.db`, `master.key`, `agent.log`, and `crash.log` |
| `CLIPSYNC_SQLITE_PATH` | `%CLIPSYNC_DATA_DIR%\clipsync.db` | Explicit SQLite database file path |
| `CLIPSYNC_SECRET` | Auto-loaded from `%CLIPSYNC_DATA_DIR%\master.key` | Custom master secret override for AES-256-GCM and HMAC blind index |
| `CLIPSYNC_AGENT_ADDR` | `127.0.0.1:37618` | Local daemon listen address (also compatible with `CLIPSYNC_UI_ADDR` and `CLIPSYNC_UI_URL`) |

## ClipboardRecord Schema

```json
{
  "id": "rec_20260601150000_...",
  "user_id": "local",
  "content_type": "text",
  "content_text": "plain text when not sensitive",
  "content_cipher": null,
  "content_hash": "sha256 hex",
  "is_sensitive": false,
  "sensitive_type": "",
  "account_cipher": null,
  "password_cipher": null,
  "remark_cipher": null,
  "two_factor_cipher": null,
  "search_index": [],
  "source_device_id": "windows-local",
  "source_platform": "windows",
  "target_platform": "",
  "client_created_at": "2026-06-01T15:00:00Z",
  "updated_at": "2026-06-01T15:00:00Z",
  "deleted_at": null,
  "is_favorite": false,
  "favorite_at": null,
  "sync_status": "local"
}
```

## PromptTemplate Schema

```json
{
  "id": "prm_...",
  "title": "Code Review Expert",
  "category": "研发",
  "template_content": "Please review the following {{language}} code:\n{{code}}",
  "shortcut": "/cr",
  "usage_count": 12,
  "is_favorite": true,
  "created_at": "2026-06-01T15:00:00Z",
  "updated_at": "2026-06-01T15:00:00Z"
}
```

## Encryption Envelope & Reveal Payload

Sensitive fields use AES-256-GCM with PBKDF2-HMAC-SHA256 key derivation:

```json
{
  "algorithm": "AES-256-GCM",
  "kdf": "PBKDF2-HMAC-SHA256",
  "iterations": 120000,
  "salt": "base64",
  "nonce": "base64",
  "ciphertext": "base64"
}
```

When revealed via `POST /api/records/{id}/reveal`, the local agent returns:

```json
{
  "record_id": "rec_...",
  "content": "original clipboard content",
  "platform": "GitHub",
  "account": "user@example.com",
  "password": "secret-password",
  "two_factor": "JBSWY3DPEHPK3PXP",
  "remark": "production account",
  "expires": "2026-06-01T15:30:00+08:00"
}
```

The WinUI client computes the 6-digit TOTP verification code and 30-second countdown in the detail modal when `two_factor` is configured, and uses `POST /api/clipboard/write` for guarded copying so temporary TOTP codes or edited records are not re-captured as plaintext clipboard history.
