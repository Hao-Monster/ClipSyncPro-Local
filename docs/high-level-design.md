# High-Level Design (Windows Local Edition)

## Modules

| Module | Responsibility |
| --- | --- |
| `cmd/clipsync-agent` | Windows background daemon entrypoint: local key resolution, SQLite initialization, and loopback HTTP server |
| `cmd/clipsync-windows` | Lightweight native Win32 standalone desktop entrypoint |
| `internal/domain` | Domain models for clipboard records, encrypted envelopes, sensitive credentials, 2FA, and AI prompt templates |
| `internal/security` | Local `master.key` lifecycle, AES-256-GCM encryption/decryption, and HMAC-SHA256 blind search index |
| `internal/storage` | Embedded SQLite storage engine (`SQLiteStore`) and in-memory test store |
| `internal/app` | Core application service: capture, deduplication, credential encryption, reveal, favorite, and deletion |
| `internal/agent` | Clipboard sequence listener, guarded clipboard writer, local REST API, and SSE event broadcaster |
| `internal/win32` | Native Windows clipboard text/PNG/DIB capture, window focus restoration, hotkey registration, and `SendInput` |
| `internal/autostart` | Per-user `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` startup registration |
| `internal/singleinstance` | Named mutex single-instance guard for Windows executables |
| `windows/ClipSyncPro.WinUI` | Windows 11 WinUI 3 Fluent Acrylic desktop client |

## Core User Flows

### 1. Clipboard Capture Flow
1. Windows clipboard sequence number changes.
2. `internal/agent` checks `clipboardWriteGuard`; if not self-originated, reads Unicode text or PNG/DIB image via `internal/win32`.
3. `internal/app` deduplicates or creates a `ClipboardRecord` with `sync_status = "local"` and persists it to `%LOCALAPPDATA%\ClipSync Pro\clipsync.db`.
4. Agent broadcasts `record_created` / `record_updated` over `GET /api/events` (SSE) to refresh the WinUI 3 client immediately.

### 2. WinUI 3 Views & Actions
The WinUI 3 client (`MainPage.xaml`) provides four primary views:
- **全部 (`All`)**: Non-favorite clipboard history with keyword search and exact date (`yyyyMMdd`) filtering.
- **⭐ 收藏夹 (`Favorites`)**: Starred clipboard records pinned for quick reuse.
- **💡 提示词库 (`Prompts`)**: Structured AI Prompt library with category tabs, keyword search, star-favorite checkbox in the edit dialog, `{{variable}}` fill modal, and double-click/one-click focus restore + `SendInput` (`Ctrl + V`) paste injection.
- **🔒 账户密码 (`Vault`)**: Encrypted sensitive credentials supporting masked cards, field selection on double-click paste, and a detail modal (`查看`) that computes live 6-digit 2FA (TOTP) codes with a 30-second countdown and guarded one-click copy.
