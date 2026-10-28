# Docker 安装与使用

需要 Docker Engine 和 Docker Compose 插件（v2 或更新版本）。MacBook 可以通过 Colima 在终端运行 Docker，安装步骤见下文“MacBook 终端安装”。
镜像支持 Linux amd64 和 arm64；Apple Silicon 电脑使用 arm64 镜像。

## 构建并启动

下载源码并进入项目目录，准备配置和待分享目录：

```bash
git clone https://github.com/huadaox/shareto.git
cd shareto
cp packaging/docker/config.example.json config.docker.json
mkdir -p shared
```

MacBook 使用 Colima 时，在此源码目录完成下文的终端安装并启动 Colima，再执行容器启动命令。

将需要分享的文件放到 `shared` 目录。例如，将 `report.pdf` 放到 `shared/report.pdf`。

编辑 `config.docker.json` 中的 `public_base_url`，填写接收者能够访问的完整地址：

- 在当前电脑测试：`http://127.0.0.1:38080`。
- 在局域网内使用：填写运行 Docker 的电脑的局域网地址和端口。
- 在服务器上使用：填写服务器地址或已经配置好的 HTTPS 域名。

`listen_addr` 保持 `0.0.0.0:38080`，用于接收进入容器的连接。

启动容器并等待健康检查通过：

```bash
docker compose up -d --build --wait
docker compose ps
```

默认只允许当前电脑通过 `127.0.0.1:38080` 访问。
需要让局域网或其他电脑连接时，在项目目录创建 `.env` 文件，写入：

```dotenv
SHARETO_BIND_ADDRESS=0.0.0.0
SHARETO_PORT=38080
```

然后重新执行 `docker compose up -d --wait`。服务器和路由器也需要允许对应端口的入站连接。
填写 `public_base_url` 决定生成链接时使用哪个地址，发布端口决定哪些电脑可以建立连接。

## 分享与管理文件

宿主机的 `shared` 目录在容器中对应 `/files`。例如：

```bash
docker compose exec shareto shareto /files/report.pdf
docker compose exec shareto shareto -t 2h -m 3 /files/report.pdf
docker compose exec shareto shareto -p /files/report.pdf
docker compose exec shareto shareto list
docker compose exec shareto shareto status
docker compose exec shareto shareto revoke TOKEN
```

将最后一条命令中的 `TOKEN` 替换为创建分享时打印的完整 Token。
撤销分享会使该分享的链接失效，原文件会保留。
当前版本的 `list` 只显示文件信息和缩短的 Token，创建时请保存打印出的完整链接和 Token。

这些命令需要通过 `docker compose exec` 在正在运行的容器内执行，文件路径也使用容器内的 `/files/...`。
后台服务由 Docker 启动，管理容器使用以下命令：

```bash
docker compose logs --tail 100 shareto
docker compose restart shareto
docker compose stop
docker compose up -d --wait
docker compose down
```

修改 `config.docker.json` 后执行 `docker compose restart shareto`。
配置通过只读文件挂载，容器内无需执行 `shareto init` 或 `shareto service install`。

## 文件与数据保存位置

| 内容 | 保存位置 |
| --- | --- |
| 配置 | 宿主机的 `config.docker.json`，容器内为 `/config/shareto/config.json` |
| 待分享文件 | 宿主机的 `shared`，容器内为 `/files`，只读挂载 |
| 分享记录与下载会话 | Compose 管理的 `shareto-state` 数据卷，容器内为 `/data/shareto/state.json` |
| 运行日志 | `docker compose logs shareto` |

`docker compose down` 会删除容器并保留数据卷，重新启动后会加载仍然有效的分享和下载会话。
`docker compose down --volumes` 会同时删除分享记录所在的数据卷，已有分享会丢失。
重新部署时保持项目目录、Compose 项目名称及待分享目录一致，才能继续使用原来的数据卷和文件路径。
修改或替换已经分享的原文件会触发文件完整性检查，原分享可能失效。

容器使用 UID/GID `65532:65532`。在 Linux 上，待分享目录需要允许这个用户遍历，文件需要允许读取。
遇到权限错误时检查目录和文件的读取权限。
容器根目录、配置文件和待分享目录均为只读，分享记录数据卷和临时 IPC 目录允许写入。

可以在 `.env` 中指定已有目录或其他端口：

```dotenv
SHARETO_FILES_DIR=/absolute/path/to/files
SHARETO_CONFIG_FILE=/absolute/path/to/config.docker.json
SHARETO_BIND_ADDRESS=127.0.0.1
SHARETO_PORT=38081
```

文件和目录需要事先存在。修改外部端口时，同步修改 `public_base_url` 中的端口；容器内部仍使用 `38080`。

## HTTPS 与反向代理

公网部署可以由 Caddy、Nginx 等反向代理提供 HTTPS，将请求转发到发布的 HTTP 端口。
此时 `public_base_url` 填写接收者访问的 HTTPS 地址，`tls.enabled` 保持 `false`。
如果代理和 Docker 在同一台服务器上，宿主机发布地址可以保持 `127.0.0.1`。

示例配置的 `trusted_proxies` 为空，不信任客户端自行提供的转发头。
需要通过代理识别访问者地址时，将实际连接到 shareto 的代理地址或专用网段加入 `trusted_proxies`，并在代理中覆盖客户端传入的 `X-Forwarded-For`。

## MacBook 终端安装

进入已经下载的 shareto 源码目录，通过 Homebrew 安装运行环境与命令行工具：

```bash
brew install colima docker docker-compose docker-buildx
mkdir -p "$HOME/.docker/cli-plugins"
ln -sfn "$(brew --prefix)/opt/docker-compose/bin/docker-compose" "$HOME/.docker/cli-plugins/docker-compose"
ln -sfn "$(brew --prefix)/opt/docker-buildx/bin/docker-buildx" "$HOME/.docker/cli-plugins/docker-buildx"
colima start --cpus 4 --memory 4 --disk 20 --mount "$PWD" --ssh-config=false
docker version
docker compose version
```

Colima 创建并管理 Linux 虚拟机，启动后 Docker 命令会连接到 Colima。
上面的配置提供 4 个 CPU、4 GiB 内存和 20 GiB 数据磁盘，并将当前源码目录挂载到虚拟机。
宿主机源码目录中的配置和 `shared` 文件由虚拟机读取，分享记录写入虚拟机内的 Docker 数据卷。
使用项目目录以外的待分享目录时，需要将对应目录也加入 Colima 的挂载配置。

环境启动后，按照前文执行 `docker compose up -d --build --wait`，再使用分享命令。
日常使用可以执行：

```bash
colima status
colima stop
colima start
```

`colima stop` 会停止虚拟机和其中的容器，重新启动时会沿用已经保存的配置和磁盘。

构建时需要联网下载基础镜像和 Go 依赖。需要代理的网络环境，可以按照 [Docker 构建代理说明](https://docs.docker.com/engine/cli/proxy/) 配置 Docker CLI 的代理；代理地址需要能从 Colima 虚拟机中访问。

## 版本更新与镜像发布

从源码更新并重新构建：

```bash
git pull
docker compose up -d --build --wait
```

仓库增加了 Docker 工作流：每次提交和 PR 都在 amd64、arm64 两种 Linux 机器上构建并运行真实容器测试。
维护者发布 `v` 开头的版本标签后，测试通过才向 GitHub Container Registry 发布两种架构的镜像。
手动运行工作流时发布 `manual` 标签。
首次发布后，维护者需要将对应的 GitHub Packages 镜像设为公开，才能让其他用户直接拉取。

已有公开镜像时，可以在 `.env` 中设置 `SHARETO_IMAGE=ghcr.io/huadaox/shareto:版本号`，然后执行：

```bash
docker compose pull
docker compose up -d --no-build --wait
```

版本号应替换为仓库实际已经发布的镜像版本。

## 运行容器测试

测试需要 Docker、Docker Compose v2 和 Python 3：

```bash
python3 packaging/docker/test.py
```

测试会创建独立的 Compose 项目、随机端口和测试文件，验证健康检查、只读挂载、下载内容、Range 下载、密码、次数限制、撤销，以及容器重启和重建后的数据保留。
测试完成后删除该测试项目的容器和数据卷，日志保存于 `work/docker-test-*/container.log`。

2026 年 10 月 27 日，已在 Apple Silicon Mac（macOS 26.5.1）上使用 Colima 0.10.3、Docker Engine 29.5.2 和 Compose 5.6.1 完成上述完整容器测试。
