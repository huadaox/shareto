# Windows 安装与运行指南 (Windows Guide)

`shareto` 是专为大文件安全、极速直链分发设计的跨平台工具。Windows 版本完整支持本地 CLI 分享、常驻后台守护进程、Task Scheduler 自启托管与 TLS 安全直链传输。

---

## 1. 快速安装

### 方式一：使用 PowerShell 一键安装（推荐）

1. 下载最新发行包 `shareto_<version>_windows_amd64.zip`（若为 ARM 设备请下载 `arm64.zip`）。
2. 解压 ZIP 压缩包至任意目录。
3. 打开 PowerShell（普通用户权限即可，无需管理员身份），进入解压目录并运行：

```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
.\install.ps1
```

`install.ps1` 会自动完成以下操作：
- 将 `shareto.exe` 部署至 `%LOCALAPPDATA%\Programs\shareto\`；
- 将安装路径写入当前用户的 `PATH` 环境变量；
- 注册用户自启服务（`HKCU\...\Run` 与静默启动脚本），实现开机登录自动在后台静默托管守护进程；
- 立即启动后台守护进程。

安装完成后，重新打开一个新的 PowerShell 或 Windows Terminal 终端窗口即可全局使用 `shareto` 命令。

---

### 方式二：手动安装到用户目录

1. 将 `shareto.exe` 放置于你喜好的目录，例如 `C:\Users\<YourUser>\bin\`。
2. 将该目录添加到当前用户的系统环境变量 `Path` 中。
3. 打开命令行终端，执行服务安装：

```powershell
shareto version
shareto init
shareto service install
```

---

## 2. 快速上手

### 2.1 交互式配置向导 (`init`)

初次使用推荐执行交互式向导配置监听地址与对外直链访问域名：

```powershell
shareto init
```

向导将自动探测本机 IPv6 与局域网 IPv4 地址，配置监听端口（默认 `[::]:38080`），并支持输入公网域名（如 `https://share.yourdomain.com`）与 TLS 证书路径。

### 2.2 快速分发文件

```powershell
# 1. 极简分享（默认 24 小时过期，限 10 次有效下载）
shareto D:\LargeFiles\game_update.zip

# 2. 自定义有效期与下载限额
shareto -t 2h -m 3 "C:\Users\fei\Desktop\design_assets.tar"

# 3. 启用密码保护（自动生成安全提取码或手动输入）
shareto -p D:\Backup\database_snapshot.sql

# 4. 指定下载直链对外 Public Base URL
shareto --base-url https://dl.example.com "D:\Videos\demo.mp4"
```

终端将输出落地页链接、下载直链与安全口令：
```text
✔ File pinned successfully: demo.mp4 (4.2 GB)
🔗 Public Link:     https://dl.example.com/s/7J_sD2...
⚡ Direct Download: https://dl.example.com/s/7J_sD2...?raw=1
🔑 Password:        Ab3#k9Pz
⏱ Expires In:      24h0m0s (at 2026-10-27 15:30)
📥 Download Quota:  10 sessions remaining
```

### 2.3 管理分享中的文件

```powershell
# 列出当前所有活动的分享条目
shareto list

# 查看后台守护进程状态与活动连接
shareto status

# 撤销指定分享直链（支持传入 Token 或原文件路径）
shareto revoke 7J_sD2...
```

---

## 3. 后台服务管理 (`shareto service`)

Windows 版本的后台服务在当前登录用户会话下独立托管，无需系统管理员权限，也不会弹黑框影响前台使用：

```powershell
# 查看后台任务加载状态
shareto service status

# 启动 / 停止 / 重启守护进程
shareto service start
shareto service stop
shareto service restart

# 卸载开机自启任务（保留配置文件与现有分享状态）
shareto service uninstall
```

> **提示**：如果需要前台实时观察服务器访问与分发日志，可直接运行：
> ```powershell
> shareto daemon
> ```

---

## 4. 文件存储位置与安全模型

| 模块 | Windows 默认路径 | 说明 |
| :--- | :--- | :--- |
| **配置文件** | `%APPDATA%\shareto\config.json` | 端口、TLS 证书、限频与并发流控制 |
| **持久化状态** | `%APPDATA%\shareto\state.json` | 活动分享条目、剩余额度与会话快照 |
| **进程间 IPC** | `%LOCALAPPDATA%\shareto\ipc.sock` | AF_UNIX 域套接字（受 NTFS 用户 DACL 保护） |
| **托管自启任务** | Windows 任务计划程序 `\shareto` | 用户开机登录静默自启 |

### 安全与完整性保证 (Security Guarantees)
1. **防静默篡改与文件替换**：Windows 版本通过调用底层 Win32 API `GetFileInformationByHandle`，捕获文件打开时刻的 NTFS `VolumeSerialNumber`（卷序列号）与 64 位 `FileIndex`（MFT 记录索引号，等同于 Unix Inode），结合创建时间戳与修改时间戳进行连续快照核验。若文件在分发过程中被恶意替换、追加或重命名，服务会立即中断连接并熔断直链。
2. **防符号链接/Junction 越界**：解析 Windows 符号链接与 NTFS 目录连接点（Reparse Points），严禁非普通文件与空文件分发。
3. **内存快照防竞争**：支持并发多线程分块下载（HTTP 206 Range），采用 `io.SectionReader` 实现并发零互斥高效 `ReadAt`。
4. **抗爆破与慢速连接防护**：Argon2id 密码学恒定时间校验、65536 槽位 LRU IP 限频、15s 滑动窗口传输速率监控。

---

## 5. 源码构建 (Build from Source)

在安装有 Go 1.22+ 环境的 Windows 或 Linux 终端中：

```bash
# 构建 Windows x86-64 二进制
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/shareto.exe ./cmd/shareto

# 或在仓库根目录下使用 Makefile
make windows
```
