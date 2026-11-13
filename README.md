# ClipSync Pro (Windows Local Edition)

> **专为开发者、AI 重度用户与隐私敏感人群打造的 Windows 11 原生现代剪贴板历史、AI 提示词（Prompt）模板库与本地密码保险箱。**

![Platform](https://img.shields.io/badge/Platform-Windows%2011%20%2F%2010%20(x64)-0078D4?logo=windows)
![Storage](https://img.shields.io/badge/Storage-SQLite%20(100%25%20Local)-003B57?logo=sqlite)
![Security](https://img.shields.io/badge/Encryption-AES--256--GCM%20%2B%20HMAC--SHA256-2EA043)
![UI](https://img.shields.io/badge/UI-WinUI%203%20Fluent%20Acrylic-512BD4?logo=dotnet)

**ClipSync Pro (Windows Local Edition)** 采用 **WinUI 3 原生桌面前端 + Go 本地守护进程 + 嵌入式 SQLite 引擎** 架构，无需安装任何外部数据库，无需配置环境变量，解压即用，将高频剪贴板流转、AI 提示词（Prompt）复用与敏感凭据/2FA 管理浓缩在一次 `Alt + V` 呼出之中。

---

## 核心特性与三大安全生产力支柱

### 1. 剪贴板内容 100% 保存在本机，绝对的隐私与安全

剪贴板往往承载着日常工作中最核心的代码片段、未公开文档、API Token 与私人对话。**ClipSync Pro 纯本地单机版从架构底层杜绝一切外发风险**：

- **100% 本机闭环存储**：零云端上传、零外网同步请求、零第三方服务器依赖。所有剪贴板记录、图片数据与提示词模板均保存在用户本机目录 `%LOCALAPPDATA%\ClipSync Pro\clipsync.db`（嵌入式 SQLite 本地数据库，启用 WAL 高并发模式）。
- **彻底告别云剪贴板泄露隐患**：不经过任何公网中转服务，后台守护进程仅监听本机回环地址（默认 `127.0.0.1:37618`）供本地 WinUI 界面调用。
- **纯净发行版红线保障**：GitHub Release 发行版与源码仓库**仅打包纯净客户端二进制程序**，在构建与发布全链路执行严格的数据与符号清零审计，**绝不打包任何本地剪贴板历史数据、数据库文件（`.db`/`.sqlite`）、密钥文件（`.key`）或调试符号文件（`.pdb`）**。首次启动时才会在用户本机自动初始化全新的空白数据库。

### 2. 高频 AI 提示词（Prompt）随身生产力中枢

针对开发者与 AI 重度用户在 ChatGPT、Claude、Cursor、Antigravity、Windsurf 及各类 IDE 中每天需要高频编写、调优和复用大量 AI 提示词（Prompt）的工作流深度定制：

- **结构化 Prompt 模板库**：内置独立的「💡 提示词库」工作台，支持新建、编辑、删除提示词模板，支持 `{{变量名}}` 动态占位符填充，并可为每条模板配置标题、正文、**分类标签（Category）**、**快捷标识（Shortcut）**以及在编辑弹窗中勾选**「设为星标收藏提示词」**。
- **关键词秒级检索与分类过滤**：支持按分类标签一键筛选或通过关键词实时检索，迅速从数百条 System Prompt、代码审查指令、重构模板或翻译指令中定位目标。
- **智能频次排序与星标置顶**：自动记录每条 Prompt 的调用次数（`usage_count`），并在新建/编辑弹窗中支持勾选「设为星标收藏提示词」，按「星标收藏优先 -> 使用频次降序 -> 最近更新」智能排序，越常用的提示词越靠前。
- **双击/一键自动切回窗口粘贴注入**：选中提示词后双击或点击调用（含 `{{变量}}` 时先弹出参数填写框），客户端通过受保护剪贴板通道自动将焦点切回先前的聊天窗口或 IDE 编辑器，并通过原生 `SendInput`（`Ctrl + V`）直接将组装后的长篇 Prompt 模板模拟粘贴到位，彻底告别在笔记软件与对话框之间反复来回复制的繁琐操作。

### 3. 本地加密账户、密码与 2FA 动态验证码保险箱

无需为了临时保存开发测试账号、服务器密码或二步验证密钥而打开笨重的外部密码管理器，ClipSync Pro 内置高强度本地加密保险箱：

- **全要素凭据与 2FA (TOTP) 管理**：支持将任意剪贴板记录一键标记（「设为密码」/「设为账户」）或在编辑弹窗中结构化保存**所属平台（Platform）**、**账号（Account）**、**密码（Password）**、**备注（Remark）**以及 **2FA 密钥（Base32 Secret）**。
- **内置 TOTP 6 位动态验证码实时展示与安全复制**：配置 2FA 密钥后，在敏感记录「查看」详情弹窗中实时计算并展示当前 6 位 2FA (TOTP) 动态验证码与 30 秒周期倒计时，支持一键安全复制动态码（通过守护进程受保护通道写入系统剪贴板，绝不会被当成普通明文记录重复捕获入库）；双击敏感卡片时亦可按字段选择直接粘贴账号、密码、所属平台、2FA 密钥或备注。
- **AES-256-GCM 认证加密 + HMAC-SHA256 密态检索**：
  - 首次启动时，系统在 `%LOCALAPPDATA%\ClipSync Pro\master.key` 自动排他生成 32 字节密码学安全随机主密钥（严格限制文件权限）。
  - 所有敏感记录的正文与凭据载荷均使用 **AES-256-GCM**（配合 PBKDF2-HMAC-SHA256 密钥派生）认证加密后再写入 SQLite，数据库落盘仅存密文，不落明文。
  - 针对加密后的敏感记录，采用基于主密钥派生的 **HMAC-SHA256 盲索引（Blind Index）** 技术构建关键词哈希表，既能在本地数据库中隐藏明文，又能实现毫秒级的关键词（含多词空格组合）密态搜索。
  - 界面默认对敏感条目进行掩码脱敏展示，仅在用户主动查看或复制/粘贴时于内存态瞬时解密。

---

## 工程与交互体验亮点

- **零外部依赖、解压即用**：
  内置纯 Go 实现的 SQLite 存储引擎（`modernc.org/sqlite`）与本地主密钥自动托管机制。**无需安装 MySQL**，无需配置任何环境变量或启动外部数据库服务，下载 Release 压缩包解压后双击 `ClipSyncPro.WinUI.exe` 即可开箱使用。
- **Windows 11 Fluent 亚克力现代交互**：
  - **`Alt + V` 全局热键**：随时随地一键呼出或隐藏窗口，呼出时自动记忆当前活动目标窗口句柄。
  - **屏幕右上角智能停靠**：固定于主工作区右上角黄金视野，不遮挡核心编码与阅读区域。
  - **双击自动切回并粘贴（`SendInput`）**：双击任意文本、图片、敏感凭据字段或 AI 提示词，自动通过受保护通道写回系统剪贴板、恢复原目标窗口焦点并触发 `Ctrl + V` 粘贴。
  - **图文混排与图片备注**：自动捕获剪贴板截图与复制图片（标准化为 PNG 二进制存储，列表采用轻量摘要惰性加载，滚动丝滑不卡顿），支持为记录编辑内容或备注以便通过关键词检索。
  - **四大核心视图与精准筛选**：提供 **「全部」「⭐ 收藏夹」「💡 提示词库」「🔒 账户密码」** 四大核心视图切换，支持关键词实时搜索、按日期（`yyyyMMdd`）精确筛选及开机自启。

---

## 快速开始

### 一、普通用户：下载 Release 解压即用（推荐）

1. 前往本仓库的 **[Releases](../../releases)** 页面，下载最新版本的 **`ClipSyncPro-Windows-Local-x64.zip`**。
2. 将压缩包解压到任意本地目录（例如 `D:\Tools\ClipSyncPro`）。
3. 双击运行 **`ClipSyncPro.WinUI.exe`**：
   - 程序会自动在后台拉起同目录下的本地守护进程 `ClipSyncPro-Agent.exe`。
   - 自动在 `%LOCALAPPDATA%\ClipSync Pro\` 下初始化空白数据库 `clipsync.db` 与加密主密钥 `master.key`。
4. 按下 **`Alt + V`** 呼出或隐藏主面板，复制任意文本或截图即可立即在列表中看到记录；双击任意条目即可自动粘贴到当前活动窗口。

---

### 二、开发者指南：从源码测试与编译打包

#### 环境要求

- **操作系统**：Windows 10 / Windows 11 (x64)
- **Go 工具链**：Go 1.25+
- **.NET SDK**：.NET 8.0+ SDK（包含 Windows App SDK / WinUI 3 构建工作负载）

#### 1. 运行全量单元测试

项目核心存储层（SQLite CRUD、密态索引检索、Prompt 模板管理）、应用服务层、本地 Agent API 与密钥管理均具备完整的自动化测试覆盖：

```powershell
go test -v ./...
```

#### 2. 编译本地守护进程与原生客户端

在仓库根目录下按顺序执行以下命令，即可构建出完整的独立发行版目录 `dist\ClipSyncPro.WinUI-x64-latest`：

```powershell
# 1. 编译 Go 本地守护进程 (Agent) 与 Win32 轻量入口（去除本地路径与符号表）
go build -trimpath -ldflags="-s -w" -o "dist\ClipSyncPro-Agent.exe" .\cmd\clipsync-agent
go build -trimpath -ldflags="-s -w" -o "dist\ClipSyncPro-Windows.exe" .\cmd\clipsync-windows

# 2. 发布自包含 (Self-Contained) WinUI 3 桌面客户端
dotnet publish "windows\ClipSyncPro.WinUI\ClipSyncPro.WinUI.csproj" `
  -c Release `
  -p:Platform=x64 `
  -r win-x64 `
  --self-contained true `
  -o "dist\ClipSyncPro.WinUI-x64-latest" `
  --nologo

# 3. 将最新编译的 Agent 放入 WinUI 发布目录并清理调试符号文件 (.pdb)
Copy-Item "dist\ClipSyncPro-Agent.exe" "dist\ClipSyncPro.WinUI-x64-latest\ClipSyncPro-Agent.exe" -Force
Remove-Item "dist\ClipSyncPro.WinUI-x64-latest\*.pdb" -Force -ErrorAction SilentlyContinue
```

构建完成后，直接运行 `dist\ClipSyncPro.WinUI-x64-latest\ClipSyncPro.WinUI.exe` 即可启动。

---

## 本地数据存储与高级配置说明

默认情况下，ClipSync Pro **零配置开箱即用**，所有运行数据均存放在当前 Windows 用户的本地应用数据目录中，与程序安装目录完全分离（升级客户端只需覆盖解压程序目录，数据不会丢失）：

| 数据项 | 默认本机路径 | 说明 |
| :--- | :--- | :--- |
| **SQLite 数据库** | `%LOCALAPPDATA%\ClipSync Pro\clipsync.db` | 保存所有剪贴板历史、PNG 图片 BLOB、AI 提示词模板及加密后的敏感记录 |
| **本地加密主密钥** | `%LOCALAPPDATA%\ClipSync Pro\master.key` | 自动生成的 32 字节（64 位 Hex）随机密钥，用于 AES-256-GCM 与 HMAC-SHA256 |
| **运行与崩溃日志** | `%LOCALAPPDATA%\ClipSync Pro\agent.log` / `crash.log` | 本地守护进程与客户端诊断日志 |

如需自定义路径、密钥或端口（例如便携式测试环境），可通过以下可选环境变量覆盖默认行为：

| 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `CLIPSYNC_DATA_DIR` | `%LOCALAPPDATA%\ClipSync Pro` | 自定义本地数据库、主密钥与日志的存储根目录 |
| `CLIPSYNC_SQLITE_PATH` | `%CLIPSYNC_DATA_DIR%\clipsync.db` | 显式指定 SQLite 数据库文件完整路径 |
| `CLIPSYNC_SECRET` | 自动读取/生成 `master.key` | 显式指定自定义加密主密钥（优先于 `master.key`） |
| `CLIPSYNC_AGENT_ADDR` | `127.0.0.1:37618` | 本地守护进程监听地址（同时兼容 `CLIPSYNC_UI_ADDR` 与客户端 `CLIPSYNC_UI_URL`） |
| `CLIPSYNC_LOG_DIR` | `%CLIPSYNC_DATA_DIR%` | 显式覆盖守护进程 `agent.log` 输出目录 |

---

## 项目目录结构

```text
ClipSync Pro/
├── cmd/
│   ├── clipsync-agent/       # 本地守护进程入口（SQLite 存储 + 本地密钥初始化 + Loopback HTTP API）
│   └── clipsync-windows/     # 轻量级原生 Win32 备选入口
├── docs/                     # 架构设计、本地 Agent API 契约与 SQLite Schema 文档
├── internal/
│   ├── agent/                # 剪贴板监听、受保护剪贴板写回、本地 REST/SSE API 处理
│   ├── app/                  # 核心应用服务层（捕获去重、敏感凭据加解密、收藏与删除编排）
│   ├── autostart/            # Windows 当前用户开机自启注册表管理
│   ├── domain/               # 剪贴板记录、敏感凭据、2FA 与 AI 提示词领域模型
│   ├── nativeui/             # 轻量级原生 Win32 界面实现
│   ├── security/             # 本地主密钥自动托管、AES-256-GCM 加解密与 HMAC 密态盲索引
│   ├── singleinstance/       # Windows 命名互斥体单实例锁
│   ├── storage/              # 纯本地嵌入式 SQLite 存储引擎实现与完整测试套件
│   └── win32/                # Windows 原生剪贴板文本/PNG/DIB 捕获、焦点恢复、全局热键与 SendInput
└── windows/
    └── ClipSyncPro.WinUI/    # Windows 11 WinUI 3 桌面客户端（全局热键、智能停靠、提示词库、密码与 2FA 保险箱）
```

## 隐私与安全承诺

1. **无遥测、无埋点、无联网追踪**：客户端不包含任何统计 SDK 或后台外联行为。
2. **代码与数据严格隔离**：项目 `.gitignore` 与打包流程强制拦截一切 `.db`、`.sqlite`、`.key`、`.log`、`.pdb` 文件，确保任何开发者或打包环境中的个人剪贴板数据与本机路径符号绝不会混入代码仓库或发行安装包。
