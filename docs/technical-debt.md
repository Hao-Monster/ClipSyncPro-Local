# Engineering Guardrails & Technical Debt Register

This register tracks known platform constraints and engineering safeguards for ClipSync Pro (Windows Local Edition).

| Priority | Area | Current Safeguard | Future Enhancement |
| --- | --- | --- | --- |
| Medium | Clipboard Image Formats | `internal/win32` normalizes PNG and 24/32-bit `BI_RGB`/`BI_BITFIELDS` DIB/DIBV5 clipboard bitmaps into PNG BLOBs and rejects unsupported/corrupt formats explicitly. | Expand decoder coverage for additional custom clipboard image formats with strict size limits. |
| Medium | WinUI 3 Self-Contained Publishing | `ClipSyncPro.WinUI.csproj` sets `PublishReadyToRun=False` and `PublishTrimmed=False` for compatibility with WinRT COM vtable initialization and reflection-based JSON serialization, and strips `.pdb` symbols from release archives. | Adopt source-generated `System.Text.Json` contexts if trimmed builds are required in future Windows App SDK releases. |
| Low | SQLite Schema Evolution | `SQLiteStore.EnsureSchema()` applies idempotent `CREATE TABLE IF NOT EXISTS` and column backfills on startup. | Introduce numbered schema version migrations if backward-incompatible table changes are needed. |
