# shareto

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go)](https://go.dev/)
[![Platform](https://img.shields.io/badge/Platform-Linux-FCC624?logo=linux&logoColor=black)](https://kernel.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

`shareto` 面向 Linux 个人服务器和 MacBook 用户，提供带有下载次数、有效期和密码保护的文件分享功能（v3.0）。

它通过内核级系统调用与契约驱动架构，提供比传统网盘、Alist 或网盘挂载工具更安全、更可控的大文件外链分发能力：支持阅后即焚（单次或多次下载额度）、子网绑定、并发限流、防爬虫误扣配额、防篡改熔断，且**全面拥抱 Go 1.22+ 原生标准库路由，零第三方重型 Web 框架**。

---

## 🌟 核心特性 (Key Features)

- 🔒 **全生命周期文件防篡改熔断**：
  - 采用系统调用级 `O_RDONLY | O_NOFOLLOW | O_CLOEXEC` 打开文件，禁止跨目录符号链接穿透。
  - 记录 `Dev`、`Inode`、`Mtime`、`Ctime` 与 `FileSize` 五重底层指纹；在向客户端传输数据的**每一次读取前后**均进行完整性核验。
  - **熔断机制**：传输中途文件若被修改（哪怕恢复了修改时间），立即丢弃已读数据并强行中断连接，杜绝脏数据输出；未开始的请求统一返回 `HTTP 410 Gone`，并自动清理失效分享。
  - 基于系统调用 `pread`（`io.SectionReader`）实现 100% 协程安全的并发 HTTP Range 断点续传与多线程分片下载。
- 🛡️ **防爬虫误扣配额**：
  - 自动识别并拦截主流社交预览爬虫（微信、Telegram、Twitter/X、Slack、Discord、Facebook 等），防止聊天软件自动抓取卡片信息时偷跑一次性直链配额。
- ⚡ **两阶段会话与原子配额控制**：
  - **阶段一 (Landing/Direct)**：无消耗访问静态页面，或通过 `GET ?raw=1` 触发无密码直链跳转。`HEAD` 仅查询元数据。
  - **阶段二 (Claim)**：输入密码并校验 HMAC-SHA256 CSRF 令牌，原子扣减 1 次额度，签发 2 小时绑定的 `SessionID` 并 `303` 重定向。
  - **阶段三 (Download)**：客户端绑定网段校验（IPv4 `/24`，IPv6 `/64`），杜绝会话直链跨公网非法扩散。
- 🚦 **全维度抗拒绝服务 (DoS) 与防爆破防御**：
  - **连接池准入限制**：底层 TCP 限制全局连接上限（默认 256）及单 IP 上限（默认 32），超时 5 秒未发请求直接断开，防御 Slowloris 慢速连接耗尽端口。
  - **Argon2id 资源隔离**：严格校验密码哈希算法参数，每次使用 64 MiB 内存，全局并发验证上限为 2（内存预算 128 MiB），超出返回 `503`，彻底免疫 CPU/内存 DoS 炸机。
  - **并发预留防爆破**：固定容量 65536 条目的并发安全 LRU 限频器，正在验证中的请求即时占用失败配额，杜绝攻击者利用毫秒级高并发绕过 5 次锁定的规则。
  - **防慢速读取 (Slow-Read)**：15 秒滑动窗口最低速率检测（低于 16KB/s 强制掐线释放连接槽位）。
  - **原子落盘保证**：创建、领取与撤销均等待状态文件同步、原子重命名及目录落盘；重启服务后仍在 2 小时有效期内的未下载完会话自动保留。
- 🔌 **内核级安全的进程间通信 (IPC)**：
  - CLI 与后台守护进程通过 Unix Domain Socket 通信（权限 `0600`）。
  - Linux 使用 `SO_PEERCRED`，macOS 使用 `LOCAL_PEERCRED`。客户端和后台进程均检查对端用户身份。
- 📦 **生产级 Systemd 用户服务**：
  - 内置内核沙箱安全指令（`ProtectSystem=strict`, `ProtectHome=read-only`, `NoNewPrivileges=true`）。
  - CLI 自动检测 Systemd 用户 linger 状态，给出操作建议。

---

## 🏗️ 交互工作流 (Workflow)

```mermaid
sequenceDiagram
    autonumber
    actor U as 访客客户端 / 浏览器
    participant S as shareto HTTP Server
    participant M as Store Manager (内存/状态)
    participant F as 磁盘文件 (pread)

    Note over U,S: 阶段一：访问落地页
    U->>S: GET /s/{token}
    S->>M: 校验 Token 是否有效
    S-->>U: 返回静态 HTML 落地页 (含 CSRF Nonce)

    Note over U,S: 阶段二：校验密码并获取会话
    U->>S: POST /s/{token}/claim (CSRF + 密码)
    S->>S: 校验 CSRF 签名与子网
    S->>S: 申请全局验证名额并预留尝试次数
    S->>S: Argon2id 恒定时间密码比对
    S->>M: 原子扣减 1 次 RemainingDownloads, 同步落盘签发 SessionID
    S-->>U: 303 See Other (/d/{token}/{session}/{filename})

    Note over U,F: 阶段三：多线程并发流下载
    U->>S: GET /d/{token}/{session}/{filename} (带 Range)
    S->>S: 校验 Session 有效期及客户端子网归一化匹配
    S->>M: 申请全局/分享/会话 3 级并发槽位
    S->>F: 每次读取前后核对 Inode/Ctime/Mtime/Size 并 pread 读取
    F-->>U: 206 Partial Content (受滑动窗口最低速率监控)
    Note over S: 请求结束，释放并发槽位
```

---

## 🚀 快速上手 (Quick Start)

### Docker 部署

需要 Docker 和 Docker Compose 插件（v2 或更新版本）。在源码目录准备配置和待分享目录，然后启动：

```bash
cp packaging/docker/config.example.json config.docker.json
mkdir -p shared
docker compose up -d --build --wait
docker compose exec shareto shareto /files/report.pdf
```

将待分享文件放入 `shared`，例如 `shared/report.pdf`。
默认通过当前电脑的 `http://127.0.0.1:38080` 访问。
向其他电脑分享时，需要设置宿主机发布地址，并在配置中填写接收者能够访问的网址。
支持 amd64 和 arm64，分享记录保存在 Docker 数据卷中。
完整安装、端口配置、更新和撤销分享命令见 [Docker 使用说明](docs/DOCKER.md)。

### MacBook 安装

下载 `shareto_<version>_darwin_universal.pkg`，使用 macOS 安装器打开。安装包同时支持 Apple Silicon 和 Intel，要求 macOS 13 或以后版本。

安装完成后在终端执行：

```bash
shareto init
shareto service install
shareto /path/to/archive.zip
```

后台服务使用当前登录用户的 launchd LaunchAgent，在用户登录期间运行。
也提供可安装到用户目录的 `.tar.gz` 文件，具体命令、文件位置和卸载方式见 [MacBook 使用说明](docs/MACOS.md)。

### Linux 安装

### 1. 通过 APT 软件源安装 (Debian / Ubuntu 推荐)

我们提供了基于 GitHub Pages 维护的官方 APT 仓库，支持 `amd64` 与 `arm64`：

#### 一键安装
```bash
curl -fsSL https://huadaox.github.io/shareto/install.sh | sudo bash
```

#### 手动添加软件源
```bash
# 1. 导入 GPG 公钥
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://huadaox.github.io/shareto/key.gpg | sudo gpg --dearmor --yes -o /etc/apt/keyrings/shareto.gpg
sudo chmod a+r /etc/apt/keyrings/shareto.gpg

# 2. 添加 APT 软件源
echo "deb [signed-by=/etc/apt/keyrings/shareto.gpg] https://huadaox.github.io/shareto stable main" | sudo tee /etc/apt/sources.list.d/shareto.list > /dev/null

# 3. 安装 shareto
sudo apt update && sudo apt install -y shareto
```

### 2. 通过 GitHub Releases 下载安装包

前往 [GitHub Releases](https://github.com/huadaox/shareto/releases) 页面下载对应架构的包：

- **Debian / Ubuntu** (`.deb`):
  ```bash
  sudo dpkg -i shareto_<version>_amd64.deb
  ```
- **RHEL / CentOS / Rocky / Fedora** (`.rpm`):
  ```bash
  sudo rpm -ivh shareto-<version>-1.x86_64.rpm
  ```

### 3. 源码编译

环境要求：Linux，Go 1.22+。

```bash
git clone https://github.com/huadaox/shareto.git
cd shareto

# 编译单一二进制文件 (CGO_ENABLED=0)
make build
```
编译产物位于 `dist/shareto`。

### 4. 本地打包与构建 (.deb / .rpm / .tar.gz)

本项目内置 `nfpm` 打包工作流，一条命令自动编译并构建覆盖全 Linux 发行版与主流架构的安装包：

```bash
# 生成当前架构的 deb/rpm/tarball
make package

# 或一键编译生成全架构包 (amd64, arm64)
make package-all
```

本地安装说明：
- **Debian / Ubuntu** (`.deb`)：
  ```bash
  sudo dpkg -i dist/shareto_3.0.0_amd64.deb
  ```
- **RHEL / CentOS / Rocky / Fedora** (`.rpm`)：
  ```bash
  sudo rpm -Uvh dist/shareto-3.0.0-1.x86_64.rpm
  ```
- **通用便携包** (`.tar.gz`，适用于 Arch、Alpine 及任意 Linux 发行版)：
  ```bash
  tar -xzf dist/shareto_3.0.0_linux_amd64.tar.gz
  sudo cp shareto/shareto /usr/local/bin/
  ```

安装后启用 Systemd 用户后台守护进程：
```bash
# 启用并立即启动守护进程
systemctl --user enable --now shareto.service

# 开启持久化运行 (保证 SSH 断开后服务不中断)
sudo loginctl enable-linger $USER
```

---

## 💻 CLI 命令与使用示例

### 1. 运行交互式配置向导 (`shareto init`)

首次安装后，**强烈建议先运行 `shareto init`**。向导会自动探测本机公网 IPv6/局域网 IP 与端口可用性，并引导配置 HTTPS 证书：

```bash
shareto init
```
流程示例：
```text
1. Listen address and port (e.g. [::]:38080) [[::]:38080]: [::]:38080
   ✓ Port check passed for [::]:38080

2. Enable native HTTPS / TLS encryption? (y/N) [y]: y
   Certificate fullchain path (.crt/.pem): ~/.config/ssl/share.example.com.crt
   Private key path (.key/.pem): ~/.config/ssl/share.example.com.key
   ✓ Valid certificate loaded! CommonName: share.example.com, Expires: 2027-01-20

3. Public Base URL [https://share.example.com:38080]: https://share.example.com:38080

[✓] Configuration successfully saved to ~/.config/shareto/config.json
[✓] shareto.service restarted successfully!
```

### 2. 创建文件分享 (默认命令)

```bash
# 快速分享文件（默认有效期 24h，最大下载次数 10）
shareto /path/to/archive.iso

# 自定义有效期 (-t 支持 30m, 12h, 7d 等格式)
shareto -t 3d -m 5 /path/to/backup.tar.gz

# 启用密码保护（终端交互式无回显输入）
shareto -p /path/to/confidential.zip

# 管道输入密码或环境变量免交互
echo "my_secret_pass" | shareto --password-stdin /path/to/data.bin
# 或
SHARETO_PASSWORD="my_password" shareto -p /path/to/data.bin

# 临时覆盖基础 URL（适用于临时使用特定外网域名或 IP）
shareto --base-url https://share.example.com:38080 /path/to/file.iso
```

终端将输出公共访问链接：
```text
File successfully shared!
  Token:      m3l2Z4s8X91bC...
  Public URL: https://share.example.com/s/m3l2Z4s8X91bC...
  Direct URL: https://share.example.com/s/m3l2Z4s8X91bC...?raw=1
  Expires:    2026-10-23T23:30:00Z
  Max Claims: 5
```

> **终端直链下载模式**：若分享未设置密码，使用 `curl -L -OJ "<Public_URL>?raw=1"` 可直接重定向至流传输分片下载。

### 3. 查看当前分享列表

```bash
shareto list
```
输出示例：
```text
TOKEN       FILE             SIZE       REMAINING  STREAMS  AUTH      EXPIRES
m3l2Z4s8..  archive.iso      4.28 GB    3/10       2        None      2026-10-23T23:30:00Z
a90Kf21x..  backup.tar.gz    512.00 MB  1/5        0        Password  2026-10-24T12:00:00Z
```

### 4. 吊销指定分享

支持传入 Token 全称、前 6 位以上前缀、或原始物理文件路径：
```bash
shareto revoke m3l2Z4s8
# 或
shareto revoke /path/to/archive.iso
```

### 5. 查看守护进程状态

```bash
shareto status
```

---

## 🔒 文件变更与异常处理机制 (FAQ)

当分享出去的文件在本地被修改或丢失时，`shareto` 会触发内核级安全熔断：

| 异常情况 | 访客侧行为 | 服务端与系统行为 |
| :--- | :--- | :--- |
| **未开始下载时：文件被修改**（内容改变、大小改变或通过 touch 伪造时间） | 访问落地页或点击下载时，立即收到 **`HTTP 410 Gone`**（提示文件已被修改或失效） | 底层核验 Inode、Mtime、不可伪造的 Ctime 及大小指纹不匹配，触发熔断，自动注销该分享并落盘 |
| **下载传输中途：文件被修改** | 浏览器或下载工具**立即发生连接中断**（报错传输失败） | `verifiedReaderAt` 在每次 `ReadAt` 时实时核验，一旦发现文件被篡改，**立刻丢弃全部已读数据并掐断 TCP 连接**，绝不流出被篡改的半截损坏文件 |
| **文件被本地删除 (rm)** | 访问立即返回 **`HTTP 410 Gone`**（守护进程运行时）或 **`HTTP 404`**（重启服务后） | Linux 下常驻 fd 释放，内核彻底回收磁盘空间，系统自动注销该条失效记录 |

---

## ⚙️ 配置文件与公网 HTTPS 说明

首次启动时，程序会在 `$XDG_CONFIG_HOME/shareto/config.json`（默认为 `~/.config/shareto/config.json`）自动生成默认配置：

```json
{
  "listen_addr": "127.0.0.1:38080",
  "public_base_url": "http://127.0.0.1:38080",
  "tls": {
    "enabled": false,
    "cert_file": "",
    "key_file": ""
  },
  "trusted_proxies": [
    "127.0.0.1/32",
    "::1/128"
  ],
  "max_global_streams": 64,
  "max_streams_per_token": 16,
  "max_streams_per_session": 8,
  "max_active_shares": 100,
  "min_transfer_rate_kbps": 16,
  "max_connections": 256,
  "max_connections_per_ip": 32,
  "max_concurrent_auth": 2
}
```

| 字段 | 默认值 | 作用说明 |
| :--- | :--- | :--- |
| `listen_addr` | `127.0.0.1:38080` | HTTP/HTTPS 监听地址（若直连外网需改为 `[::]:38080` 或 `0.0.0.0:38080`） |
| `public_base_url` | `http://127.0.0.1:38080` | 生成公共分享链接时使用的前缀（配置公网域名/HTTPS 时修改此处） |
| `tls.enabled` | `false` | 是否开启内置 TLS 加密（直连模式下推荐开启） |
| `tls.cert_file` | `""` | 证书路径（支持绝对路径或 `~/...` 用户目录路径） |
| `tls.key_file` | `""` | 私钥路径（支持绝对路径或 `~/...` 用户目录路径） |
| `trusted_proxies` | `["127.0.0.1/32", "::1/128"]` | 可信反向代理 CIDR 列表（仅受信任代理来源允许解析 `X-Forwarded-For`） |
| `max_global_streams` | `64` | 全局允许的最大活跃下载并发连接流 |
| `max_streams_per_token` | `16` | 单个分享链接允许的最大下载并发连接流 |
| `max_streams_per_session` | `8` | 单个会话允许的最大并发连接流（多线程分片下载） |
| `max_connections` | `256` | 全部 HTTP 连接数量上限，包括读取请求体和空闲连接 |
| `max_connections_per_ip` | `32` | 单个来源 IPv4 地址或 IPv6 /64 网段的连接上限；可信代理使用全部连接上限 |
| `max_concurrent_auth` | `2` | 全部分享的密码验证并发上限，范围 1 至 16，每个名额使用 64 MiB Argon2id 工作内存 |
| `min_transfer_rate_kbps`| `16` | 最低传输速率阈值（KB/s），低于该速率超 15s 强制掐断 |

> **配置约束**：连接配置需满足 `1 <= max_connections_per_ip <= max_connections <= 65536`。已有配置缺少新增字段时自动套用安全默认值。

### 域名与 HTTPS 证书配置实战

#### 模式 A：原生内置 HTTPS 直连（推荐公网直连或动态 IPv6）

若你的机器拥有公网 IPv6 或公网 IP，直接使用内置 TLS 即可无需安装额外 Web 服务器：

1. **申请证书**：家庭宽带 80/443 被封时，推荐使用 `acme.sh` 通过 DNS-01 API 验证免费申请 Let's Encrypt 证书并安装到本地（例如 `~/.config/ssl/`）。
2. **修改配置**：
   ```json
   {
     "listen_addr": "[::]:38080",
     "public_base_url": "https://share.example.com:38080",
     "tls": {
       "enabled": true,
       "cert_file": "~/.config/ssl/share.example.com.crt",
       "key_file": "~/.config/ssl/share.example.com.key"
     }
   }
   ```
3. **重启守护进程**：`systemctl --user restart shareto.service`。

> 💡 **提示**：程序原生支持 `~` 自动展开为当前用户目录（如 `/home/user`）。

#### 模式 B：Nginx / Caddy 反向代理模式

若服务器已有 Nginx / Caddy 集中处理证书与 443 端口：
1. `shareto` 配置保持 `listen_addr: "127.0.0.1:38080"`，`tls.enabled: false`，`public_base_url: "https://share.example.com"`。
2. Nginx 配置核心指令：**必须关闭缓冲以支持大文件直通流传输**：
   ```nginx
   location / {
       proxy_pass http://127.0.0.1:38080;
       proxy_buffering off;
       proxy_read_timeout 3600s;
       proxy_set_header Host $host;
       proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
       proxy_set_header X-Forwarded-Proto https;
   }
   ```

---

## 🧪 自动化测试与工程验证

本项目包含严苛的端到端集成测试套件，全面开启 `-race` 数据竞争检测：

```bash
make test
```

测试覆盖清单：
1. **防爬虫误扣测试**：模拟多种社交平台爬虫 User-Agent 连续抓取 20 次，验证配额零误扣。
2. **并发原子扣减测试**：设置额度为 1，50 个并发 Goroutine 争抢 Claim，验证**恰好仅有 1 个请求**成功并签发 Session。
3. **多线程 Range 并发安全测试**：8 协程分片下载大文件并核验 SHA-256 哈希，验证并发 `pread` 零竞争。
4. **文件篡改熔断测试**：模拟向底层文件静默追加 1 字节或更改 Ctime，验证后续请求立即熔断返回 `410 Gone` 并清理分享；传输中途修改即刻掐断连接。
5. **设备文件拦截**：拦截 `/dev/zero` 等特殊字符设备或空文件。
6. **IPv6 /64 子网限频测试**：模拟同 `/64` 网段不同后缀的连续爆破，验证第 6 次精准触发 `429 Too Many Requests`。
7. **连接数与并发鉴权安全测试**：验证连接池上限限制、未授权访问拦截与 Argon2id 内存工作预算限制。

---

## 📄 License

MIT License. See [LICENSE](LICENSE) for details.
