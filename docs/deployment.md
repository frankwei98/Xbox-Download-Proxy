# 部署与安装参考

第一次使用请先按 [README](../README.md) 完成安装和客户端配置。本页供需要指定参数、开机启动或手动部署的用户查阅。下文的 `./setup.sh` 等相对路径命令均在项目根目录执行，IP 和安装路径请按实际情况替换。

## 端口与网络要求

默认监听端口如下：

| 端口 | 协议 | 用途 |
| --- | --- | --- |
| 53 | UDP、TCP | DNS |
| 80 | TCP | Xbox HTTP 重定向、可选的本地回传 |
| 443 | TCP | 可选的 HTTPS SNI 转发 |
| 8080 | TCP | Web UI 与管理 API |

NAS 上已有 DNS、Web 或其他服务占用端口时，需要自行腾出标准端口，或给容器分配独立网络 IP，并将监听地址绑定到该 IP。MVP 不集成 AdGuard Home、Pi-hole 等现有 DNS 服务。**Xbox 和 Windows 普通 DNS 设置不能指定自定义端口；改成高端口仅适合本机测试，不能直接作为客户端部署方案。** HTTP 重定向同样需要客户端能访问标准 80 端口；HTTPS 转发需要标准 443 端口。服务不会杀死占用端口的进程，也不会更改宿主机 DNS、hosts 或防火墙。客户端应将 DNS 设置为运行服务的 LAN IP；停止服务后，手动将客户端 DNS 恢复为自动获取。辅助 DNS 会绕过本服务的节点选择，不建议与本服务地址混填。

IPv6 对受控域名可能返回空 AAAA 答案，以避免绕过 IPv4 节点选择。客户端已有的 IPv6 DNS 缓存或加密 DNS 仍可能绕过服务，请按你的网络环境验证。主机休眠会让服务暂时不可用；macOS 可在服务运行期间用 `caffeinate` 防止睡眠。


## 引导安装（推荐）

在支持的 Linux 主机或 Apple Silicon Mac 上可从 GitHub 下载安装脚本和源码并启动引导（需要 `curl`、`tar`，以及 `sha256sum` 或 `shasum` 用于校验发布版）：

```sh
curl -fsSL https://raw.githubusercontent.com/frankwei98/Xbox-Download-Proxy/main/install.sh | sh
```

远程安装器会将源码保留在 `~/.local/share/xdp-sources/`，供 setup helper 和后续 Compose 构建使用。交互运行时会询问安装模式；无交互运行时默认选 `native`，此时必须通过 `--lan-ip` 提供服务端 LAN IPv4 地址。远程 `native` 模式会从 GitHub Releases 选择最新稳定版中与 Linux amd64/arm64 或 macOS arm64 匹配的压缩包，下载对应的 `.sha256` 文件，并用 `sha256sum` 或 `shasum` 校验。它不需要 Go，也不会在下载失败、没有匹配资产或校验失败时回退到源码构建。显式传入 `--binary PATH` 时直接使用指定文件，不自动下载。Compose 模式仍从保留的源码在本机构建镜像，需要 Docker 和 `docker compose`；它不下载发布版二进制。安装器不会自动安装 Linux 运行依赖，需时可显式传入 `--install-deps`，详见下文。可用 `sh -s --` 向脚本传参，例如：

```sh
curl -fsSL https://raw.githubusercontent.com/frankwei98/Xbox-Download-Proxy/main/install.sh | sh -s -- --mode native --lan-ip 192.168.1.10
```

已下载仓库后，也可在项目根目录运行本地安装脚本：

```sh
sh ./setup.sh
```

本地 `setup.sh` 引导选择本机安装或 Docker Compose、安装目录、服务端 LAN IPv4、管理端口，以及是否启用离线下载和 Xbox 本地回传。该功能默认关闭；已有配置在此项留空时保持原值。默认安装到 `~/.local/share/xdp`，完成后直接显示 **WebUI 地址、管理 token 和启动命令**。已有配置与 token 会保留；更新生成的二进制、启动脚本或 Compose 文件时会先确认。安装器在替换二进制或启动前，使用临时配置运行实际的 `xdp doctor` 检查 DNS UDP/TCP、HTTP、TLS 和配置的 Web 端口；端口冲突或管理端口不可用时会停止，不会终止占用端口的其他进程。Linux 有 `sudo`/`doas` 时会提权检查端口；关闭自启且没有提权工具的普通用户安装，仅低端口绑定权限不足时会警告并继续，但这些端口是否空闲仍需在有权限时确认。更新前需先停止正在运行的实例。脚本不会修改系统 DNS，也不会自动安装软件（`--install-deps` 除外）。远程一行安装与本地 `setup.sh` 的 native 二进制获取方式不同：远程安装默认下载并校验发布版，本地脚本始终使用当前仓库源码或已有二进制。

`setup.sh` 和生成的 `start.sh` 使用 POSIX `/bin/sh`，无需安装 Bash。**本地 `sh ./setup.sh`** 的 Linux 原生模式有 Go 1.24 或更新版本时会从当前源码以 `CGO_ENABLED=0` 编译静态二进制；没有 Go 时使用项目根目录已有的 `xdp`。也可用 `--binary PATH` 指定预编译的 Linux amd64/arm64 二进制；指定后始终直接使用该文件，不会因本机安装了 Go 而重新编译。远程一行安装的 `native` 默认使用经 SHA256 校验的 GitHub Release 二进制，即使系统已安装 Go 也不会编译当前源码。128/256 MiB 容器建议使用远程发布版或提供预编译二进制，避免编译时内存不足。

默认不会安装系统软件。Linux 原生安装可显式加 `--install-deps` 安装运行依赖；脚本不会自动提权，因此必须由 root 执行。Alpine 使用 `apk add --no-cache ca-certificates`；只有需要编译且没有 Go 时才同时安装 `go`。Debian 使用 `apt-get update` 和 `apt-get install -y --no-install-recommends ca-certificates`；只有需要编译且没有 Go 时才同时安装 `golang-go`。部分 Debian 仓库的 Go 版本低于 1.24，脚本会说明版本不满足；此时可改用 `--binary PATH`，或自行提供 Go 1.24 及以上版本。示例：

```sh
sh ./setup.sh --mode native --install-dir /opt/xdp --install-deps --lan-ip 192.168.1.10
sh ./setup.sh --mode native --install-dir /opt/xdp --binary ./dist/xdp-linux-amd64 --lan-ip 192.168.1.10
```

Linux 原生安装默认会询问 `Enable boot autostart and start Web/API now? [Y/n]`，交互和非交互默认均为是。选择是会调用生成的 `install-service.sh --manager auto --start` 注册并启动服务；非 root 执行时使用 `sudo` 或 `doas` 提权注册服务；端口预检有这些工具时也会使用，与自启选择无关。可用 `--autostart no` 跳过注册和启动。该脚本也可手动运行，并可指定 `--manager systemd` 或 `--manager openrc`；`--yes` 跳过覆盖确认。安装引导的 `--start` 只适用于 Compose；原生安装请使用 `--autostart yes|no`。新安装的 DNS/302 加速服务仍默认关闭，需登录 WebUI 后手动启用，已有安装沿用 `autoStart` 设置。systemd 日志可用 `journalctl -u xdp` 查看，OpenRC 日志位于安装目录的 `logs/xdp.log`，日志目录和文件权限分别为 0700、0600。

macOS 原生安装不支持开机自启动，默认不启用；显式指定 `--autostart yes` 会报错。安装后按终端提示在前台启动 Web/API。

没有实际 init 的最小容器应使用 `start.sh` 在前台运行，容器里存在 `systemctl` 命令不代表 systemd 可用。推荐在 LXC 中使用独立 LAN 固定 IP，并在网络/防火墙放行 UDP/TCP 53、TCP 80、443 和 8080；无需 privileged、nesting、TUN 或 NET_ADMIN，只需允许绑定低端口。项目不会自动修改宿主机防火墙。日常运行建议从 128 MiB 内存起步，256 MiB 更宽裕；这是运行预算，编译所需内存更高。

生成的 `start.sh` 用于前台运行 Web/API；如启用 53/80/443 时提示权限不足，先用 Ctrl+C 停止前台进程，再以有低端口绑定权限的方式重启，之后在 WebUI 启用加速服务。Compose 模式需要 Docker 与 `docker compose`，使用本地 Dockerfile 构建，主要面向 Linux NAS；生成文件引用当前仓库的绝对路径，后续重建镜像需要保留仓库。`--autostart yes` 会生成 `restart: unless-stopped` 并执行 `docker compose up -d`；`--autostart no` 会生成 `restart: no`。Compose 安装前会在 host 网络中检查端口。更新已有安装的文件仍需 `--yes` 或交互确认。

也可直接传入参数：

```sh
sh ./setup.sh --mode native --lan-ip 192.168.1.10
sh ./setup.sh --mode compose --lan-ip 192.168.1.10 --install-dir ./deployment
# 非交互启用或关闭离线下载与 Xbox 本地回传
sh ./setup.sh --mode compose --lan-ip 192.168.1.10 --offline yes
sh ./setup.sh --mode native --lan-ip 192.168.1.10 --offline no
# Linux 原生：默认启用开机自启动并立即启动 Web/API；可显式关闭
sh ./setup.sh --mode native --lan-ip 192.168.1.10 --autostart no
# Compose：启用开机自启动并立即启动 Web/API（默认值）
sh ./setup.sh --mode compose --lan-ip 192.168.1.10 --install-dir ./deployment --yes --start
sh ./setup.sh --help
```

新安装启动后默认只运行管理界面；已有安装沿用 `autoStart` 设置。登录后建议按以下顺序完成配置：

1. 在“测速”选择下载域名组、所在省份和运营商，点击“按地区自动测速”。默认从匹配项中抽样最多 16 个节点，可调整至 8–128 个；搜索框也会参与筛选。没有地区匹配时会提示调整条件，未标注地区的节点可手选。
2. 比较结果，点击“应用最快节点”或手动应用其他节点。只推荐成功且有有效吞吐的结果；应用节点会停止加速服务。
3. 检查网络设置，点击醒目状态提示中的“启动加速服务”。确认显示“加速服务已开启”后，再将 Xbox / Windows 的 DNS 设置为服务端 LAN IP。

搜索支持空格分隔的多个关键词，例如 `广东 联通` 可以匹配 `广东深圳 联通`，关键词顺序不限。省份和运营商信息来自候选源标注，不使用定位权限，也不保证同省节点一定最快。自动测速会显示流量预算，不会自动应用节点或启用 DNS/HTTP(S)。Microsoft Store 需先填写真实下载 URL。


## 使用预编译二进制安装

这适合未安装 Go 或内存较小的服务端：

1. 在服务端运行 `uname -s` 和 `uname -m`，确认系统和 CPU 架构。
2. 从[项目 Releases](https://github.com/frankwei98/Xbox-Download-Proxy/releases)选择已有版本。Linux 的 `x86_64` 对应 `linux-amd64`，`aarch64` / `arm64` 对应 `linux-arm64`；Apple Silicon Mac 对应 `darwin-arm64`。
3. 下载对应的 `xdp-<版本>-<系统>-<架构>.tar.gz`，解压得到 `xdp`，将它放到服务端。版本包也附带 `.sha256` 校验文件。
4. 用 `--binary` 指定该文件，启动安装引导。下面的 `/path/to/xdp` 必须替换成服务端上解压文件的实际路径：

```sh
curl -fsSL https://raw.githubusercontent.com/frankwei98/Xbox-Download-Proxy/main/install.sh | sh -s -- --mode native --binary /path/to/xdp --lan-ip 192.168.1.10
```

如果已经下载源码，也可以运行 `sh ./setup.sh --mode native --binary /path/to/xdp --lan-ip 192.168.1.10`。指定 `--binary` 后不会再编译源码；完成后仍需执行终端显示的启动命令。使用 `install.sh` 远程安装时也可指定此选项，以跳过 Release 下载和校验。远程自动下载只会使用当前最新稳定 Release 的匹配资产，不保证该 Release 已包含 `main` 分支的最新功能。

## 手动原生安装

要求 Go 1.24 或更新版本（开发验证使用 Go 1.26.1）。本项目使用 Go 标准库构建，静态 Web 文件嵌入可执行文件；支持 Linux amd64/arm64 和 macOS arm64。

```sh
# 在项目根目录执行
go build -o xdp ./cmd/xdp
./xdp init --data-dir ./data
```

初始化会生成 `data/config.json` 和随机管理令牌 `data/token`（权限为仅当前用户可读写），并直接在终端显示令牌。请妥善保管该令牌。启动 Web UI/API：

```sh
./xdp serve --data-dir ./data --lan-ip 192.168.1.10
```

首次运行可在终端查看服务显示的令牌，然后访问 `http://192.168.1.10:8080`。Web UI 将令牌保存在当前浏览器标签页的 `sessionStorage`，管理 API 使用 `Authorization: Bearer <token>`。Web/API 默认启动时不打开 DNS、HTTP、HTTPS 服务；用户在 Web UI 启动服务，或在启动时传入 `--start`：

```sh
sudo ./xdp serve --data-dir ./data --lan-ip 192.168.1.10 --start
# 或在 Web/API 已运行后：
./xdp start --data-dir ./data --url http://127.0.0.1:8080 --token '<token>'
```

Linux/macOS 绑定 53、80、443 等低端口通常需要相应权限。只在明确需要监听这些端口时提升权限；也可以将监听端口配置为可用的高端口。`serve` 可覆盖监听地址与上游 DNS，例如：

```sh
sudo ./xdp serve --data-dir ./data --lan-ip 192.168.1.10 \
  --web :8080 --dns :53 --http :80 --tls :443 \
  --upstream 114.114.114.114:53 --start
```

`--tls ""` 可关闭 HTTPS SNI 转发。`xdp init` 只需执行一次。配置文件会以原子方式写入。服务启动失败时会关闭已创建的监听器，不保留部分启动状态。


## Docker Compose（Linux NAS）

本节命令使用仓库自带的 `compose.yaml`，在项目根目录执行。如果通过安装引导生成了部署文件，请使用引导显示的 `docker compose -f ...` 命令，其数据目录和构建路径以生成文件为准。

Linux 上可使用 host 网络，让服务直接绑定 NAS 的 LAN 接口。Dockerfile 在本地构建镜像，没有假定存在公开镜像仓库。

```sh
LAN_IP=192.168.1.10 docker compose build
LAN_IP=192.168.1.10 docker compose run --rm xdp init --data-dir /data
LAN_IP=192.168.1.10 docker compose up -d
docker compose logs -f xdp
```

编辑 `LAN_IP` 为 NAS 的 LAN 地址。Compose 将宿主网络端口交给容器使用，不配置端口映射；确保所需端口已空闲。容器只添加绑定低端口所需的 `NET_BIND_SERVICE` capability，不使用 `privileged`。数据目录挂载到 `/data`。Docker host 网络主要面向 Linux NAS；macOS 上 Docker Desktop 的 host 网络行为不同，建议原生运行。

默认离线下载目录位于 `./data/downloads`。如需使用 NAS 的大容量卷，可设置 `DOWNLOAD_PATH=/volume1/xdp-downloads`；Compose 会挂载到 `/downloads`，服务通过 `--download-dir /downloads` 使用它。

Compose 服务启动 Web/API 后可通过 Web UI 启动 DNS/HTTP(S)；要让容器启动时一并开启这些服务，可在 `compose.yaml` 的命令中增加 `--start`。安装器的 `--start` 仅用于 Compose，控制立即执行 `docker compose up -d`；`--autostart yes` 设置 `restart: unless-stopped` 并立即启动，`--autostart no` 设置 `restart: no`，而 `--start` 即使与 `--autostart no` 同用也会立即启动。


## 配置和安全

配置由 `data/config.json` 保存，管理令牌由 `data/token` 单独保存。请限制数据目录权限，不要公开暴露管理 Web/API。HTTP 管理入口本身在局域网中未加密，应仅在可信网络使用。

项目只面向受控家庭 LAN 使用，不提供公网 VPS 或 Windows 服务端支持。停止服务后，下载客户端应恢复使用自动 DNS。遇到端口冲突时，先检查实际监听者并调整网络或端口；本服务不会代替用户修改宿主机网络设置。
