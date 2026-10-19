# Xbox 下载优选服务

这是一个面向家庭局域网的 Xbox 与 Microsoft Store 下载网络优选服务。服务端运行在 Linux 或 macOS 主机（包括 NAS）上；Xbox、Windows Xbox App 和 Microsoft Store 是局域网内的下载客户端。项目独立实现参考项目的 DNS 选路和 HTTP 重定向行为，本仓库没有内置其程序代码、图片或 IP 表副本。

项目为 MVP，尚未完成 Xbox 或 Microsoft Store 实机验收。节点测速仅描述测试资源的表现，不保证某个游戏或商店资源实际下载速度，也不承诺一定提速。

## 工作方式

DNS 可把受控下载域名解析到用户为域名组指定的 CDN IP。需要展示或改写的 Xbox 下载请求可以先经过本服务的 HTTP 重定向入口，之后大文件由下载客户端直接连接 CDN。Microsoft Store 使用独立域名组。HTTPS 入口只做 SNI 原样转发，不安装 CA、不解密 TLS，也不改写 HTTPS 域名。

服务会尝试从配置中指定的公开在线文本源读取候选 IP；默认引用 [参考项目的在线 IP 列表](https://github.com/skydevil88/XboxDownload/tree/master/IP)，本仓库没有附带其列表副本。下载成功后可用缓存，更新失败会保留旧缓存；无缓存时回退到当前域名的 DNS 结果并显示警告。可以在 WebUI 配置自己的 HTTPS 文本源，也可以导入 IP 列表。首次使用没有缓存时需要联网；候选源的可用性和更新由其提供方决定。项目不调用参考项目作者的商店 API，不做遥测，也不上传下载链接或测速数据。

候选节点测速需要由服务端访问测试 URL，并按用户提供的 IP 发起测试。`cn1` 默认使用 `http://assets1.xboxlive.cn/Z/routing/extraextralarge.txt`，`cn2` 默认使用 `http://assets2.xboxlive.cn/Z/routing/extraextralarge.txt`，`global` 默认使用 `http://assets1.xboxlive.com/Z/routing/extraextralarge.txt`。`store` 没有默认测试 URL，需要在配置中填写真实可下载的测试资源 URL；未填写时会明确提示无法测速。Store 也可以不测速，直接指定 IP。

吞吐测试结果只代表该测试 URL、该 IP 和当时网络条件下的测试表现，不能作为游戏实时下载速度或实际 CDN 命中情况的保证。

## 离线下载

离线下载与 Xbox 本地回传是可选功能，默认关闭。只需 302 到 Xbox 中国 CDN 的用户可保持关闭，不会打开下载库或启动下载队列。安装引导会询问，也可在 WebUI 服务设置启用，或执行 `xdp init --data-dir ./data --offline=true`。

启用后，把监听日志中的 Xbox HTTP 链接加入离线下载，暂停 Xbox 下载并等待 NAS 下载完成，再启动加速服务、让 Xbox 继续下载。命中完整资源时直接由 NAS 返回文件，支持 HEAD 和 Range；未命中继续 CDN 路径。回传模式会把受支持 Xbox 域名指向 NAS，最终 CDN 域名的未命中请求由 NAS 转发，避免循环。关闭该功能后恢复原有 DNS/302 路由，已有任务和文件保留。

后台任务也可下载其他公网 HTTP(S) URL，供 WebUI/CLI 取回；首版向 Xbox 回传仅支持 Xbox HTTP 资源，HTTPS 仍使用原站证书透传，不解密或本地回传。它不登录账号、抓取商店目录或安装内容。

最多两个任务同时下载，其余任务排队。任务支持暂停、继续、取消和删除；任务元数据持久化，服务重启后原先运行/排队的任务会暂停，等待用户恢复。支持 HTTP Range 与 If-Range 续传；源站不支持 Range、缺少可验证标识或返回完整的新资源时，从头下载。下载时保留 256 MiB 空间预算；已知大小会预先检查，未知大小会在下载中持续检查，空间不足时暂停。容量显示包含下载目录所在文件系统的总量、已用、可用空间和本工具文件大小总计。

默认保存目录是 `data/downloads`，原生部署可通过 `serve --download-dir /path/to/large-disk` 将文件放到 NAS 大容量磁盘。Signed URL 过期后不能续签，请获取新 URL 并创建新任务。WebUI 优先使用浏览器流式保存能力；浏览器不支持时会退回 Blob 下载，并限制在 256 MiB，较大文件请直接从 NAS 目录访问或用 CLI 流式取回。完整行为及命令见 [离线下载说明](docs/offline-downloads.md)。

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

在项目根目录运行：

```sh
./setup.sh
```

脚本引导选择本机安装或 Docker Compose、安装目录、服务端 LAN IPv4、管理端口，以及是否启用离线下载和 Xbox 本地回传。该功能默认关闭；已有配置在此项留空时保持原值。默认安装到 `~/.local/share/xdp`，完成后直接显示 **WebUI 地址、管理 token 和启动命令**。已有配置与 token 会保留；更新生成的二进制、启动脚本或 Compose 文件时会先确认。脚本不会修改系统 DNS 或自动提权。

本机模式有 Go 时优先从当前源码编译，避免误用旧的本地构建产物；需要 Go 1.24 或更新版本。没有 Go 时使用项目根目录已有的 `xdp`。生成的 `start.sh` 用于前台运行 Web/API；如启用 53/80/443 时提示权限不足，先用 Ctrl+C 停止前台进程，再按脚本给出的 `sudo` 命令重启，之后在 WebUI 启用加速服务。Compose 模式需要 Docker 与 `docker compose`，使用本地 Dockerfile 构建，主要面向 Linux NAS；生成文件引用当前仓库的绝对路径，后续重建镜像需要保留仓库。

也可直接传入参数：

```sh
./setup.sh --mode native --lan-ip 192.168.1.10
./setup.sh --mode compose --lan-ip 192.168.1.10 --install-dir ./deployment
# 非交互启用或关闭离线下载与 Xbox 本地回传
./setup.sh --mode compose --lan-ip 192.168.1.10 --offline yes
./setup.sh --mode native --lan-ip 192.168.1.10 --offline no
# Compose 初始化后立即启动 Web/API（新安装的加速服务仍保持关闭）
./setup.sh --mode compose --lan-ip 192.168.1.10 --install-dir ./deployment --yes --start
./setup.sh --help
```

新安装默认只运行管理界面；已有安装沿用 `autoStart` 设置。登录后建议按以下顺序完成配置：

1. 在“测速”选择下载域名组、所在省份和运营商，点击“按地区自动测速”。默认从匹配项中抽样最多 16 个节点，可调整至 8–128 个；搜索框也会参与筛选。没有地区匹配时会提示调整条件，未标注地区的节点可手选。
2. 比较结果，点击“应用最快节点”或手动应用其他节点。只推荐成功且有有效吞吐的结果；应用节点会停止加速服务。
3. 检查网络设置，点击醒目状态提示中的“启动加速服务”。确认显示“加速服务已开启”后，再将 Xbox / Windows 的 DNS 设置为服务端 LAN IP。

搜索支持空格分隔的多个关键词，例如 `广东 联通` 可以匹配 `广东深圳 联通`，关键词顺序不限。省份和运营商信息来自候选源标注，不使用定位权限，也不保证同省节点一定最快。自动测速会显示流量预算，不会自动应用节点或启用 DNS/HTTP(S)。Microsoft Store 需先填写真实下载 URL。

## 手动原生安装

要求 Go 1.24 或更新版本（开发验证使用 Go 1.26.1）。本项目使用 Go 标准库构建，静态 Web 文件嵌入可执行文件；支持 Linux/macOS 的 amd64 与 arm64。

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

Linux 上可使用 host 网络，让服务直接绑定 NAS 的 LAN 接口。Dockerfile 在本地构建镜像，没有假定存在公开镜像仓库。

```sh
LAN_IP=192.168.1.10 docker compose build
LAN_IP=192.168.1.10 docker compose run --rm xdp init --data-dir /data
LAN_IP=192.168.1.10 docker compose up -d
docker compose logs -f xdp
```

编辑 `LAN_IP` 为 NAS 的 LAN 地址。Compose 将宿主网络端口交给容器使用，不配置端口映射；确保所需端口已空闲。容器只添加绑定低端口所需的 `NET_BIND_SERVICE` capability，不使用 `privileged`。数据目录挂载到 `/data`。Docker host 网络主要面向 Linux NAS；macOS 上 Docker Desktop 的 host 网络行为不同，建议原生运行。

默认离线下载目录位于 `./data/downloads`。如需使用 NAS 的大容量卷，可设置 `DOWNLOAD_PATH=/volume1/xdp-downloads`；Compose 会挂载到 `/downloads`，服务通过 `--download-dir /downloads` 使用它。

Compose 服务启动 Web/API 后可通过 Web UI 启动 DNS/HTTP(S)；要让容器启动时一并开启这些服务，可在 `compose.yaml` 的命令中增加 `--start`。

## CLI 操作

管理命令可通过 `--data-dir ./data`、`--url http://127.0.0.1:8080` 和 `--token <token>` 指定本地数据目录、服务 URL 与管理令牌。令牌也可以放在 `data/token` 中供本机命令读取。

```sh
./xdp status --data-dir ./data
./xdp config --data-dir ./data                         # 查看配置
./xdp config --data-dir ./data --file ./my-config.json # 校验并更新配置
./xdp sources --data-dir ./data --group cn1 --refresh
./xdp import --data-dir ./data --group cn1 --file ./ips.txt
./xdp speedtest --data-dir ./data --group cn1 --ips '<从列表选出的IP>,<另一个IP>' --wait
./xdp apply --data-dir ./data --group cn1 --ip '<选择的IP>'
./xdp logs --data-dir ./data --after 0
./xdp download --data-dir ./data --source 'https://example.com/file.zip'
./xdp downloads --data-dir ./data
./xdp download-save --data-dir ./data --id '<任务ID>' --output ./file.zip
./xdp doctor --data-dir ./data
./xdp stop --data-dir ./data
```

`speedtest` 可加 `--test-url URL` 覆盖该次测试资源。`apply` 保存指定 IP 并停止下载服务；应用后需再次执行 `start`。`doctor` 用于离线检查配置与监听端口，不会更改系统设置。

文本导入格式为每行一个公网 IPv4，后面可带地区/运营商说明。每轮最多 128 个候选，默认每节点最多 5 秒、10 MiB，并发 2；时间预算到达时，已有足够数据的样本按实际接收字节计算速度。首包延迟包含连接和取得响应头的时间；并发测速会共享同一条带宽，最终候选可用并发 1 复测。结果中的 `mbps` 是十进制 Mbit/s，WebUI 换算为 MiB/s。

例如在不占用系统低端口的情况下做本机检查：

```sh
./xdp serve --data-dir ./test-data --lan-ip 127.0.0.1 \
  --web 127.0.0.1:18088 --dns 127.0.0.1:11053 \
  --http 127.0.0.1:18089 --tls '' --start
# 另开终端：
dig @127.0.0.1 -p 11053 assets1.xboxlive.com A
curl -I -H 'Host: assets1.xboxlive.com' \
  'http://127.0.0.1:18089/example/file?example=value'
```

应返回本机 A 记录与保留路径/查询参数的 HTTP 302。这验证协议行为，不代表实际游戏资源存在或下载已提速。

API 请求使用 Bearer token。例如读取状态：

```sh
curl -H "Authorization: Bearer $XDP_TOKEN" \
  http://127.0.0.1:8080/api/status
```

## 构建与检查

```sh
make build  # 本机可执行文件 ./xdp
make test   # Go 测试
make test-ui # 节点筛选 / 推荐逻辑测试（需要 Node.js 18+）
make test-setup # 安装脚本 mock 测试（无需真实安装或 Docker）
make vet    # go vet
make dist   # Linux/macOS amd64/arm64 交叉构建到 dist/
```

普通分支 push 会在 GitHub Actions 中构建验证，不打包或上传产物。推送符合 `v主版本.次版本.修订版本` 格式的 tag（例如 `v0.1.0`）会为 Linux 和 macOS 分别构建 amd64、arm64 版本；四个平台全部构建成功后，Actions 会自动创建或更新同名 GitHub Release，并附上四个 `xdp-<tag>-<系统>-<架构>.tar.gz` 压缩包及对应的 `.sha256` 校验文件。arm64 也称 aarch64。tag 构建仍会将各目标作为 artifact 保留，可在仓库的 Actions 页面下载。重新运行同一 tag 的工作流会更新该 Release 中的对应资产。

```sh
git tag v0.1.0
git push origin v0.1.0
```

## 手工验收

从 [手工验收清单](docs/manual-test.md) 开始。该清单区分本机检查、同网段客户端检查和 Xbox/Store 实机检查；本仓库目前尚未报告实机验收完成。

已执行的自动化检查及运行限制见 [MVP 验证记录](docs/validation.md)。

## 配置和安全

配置由 `data/config.json` 保存，管理令牌由 `data/token` 单独保存。请限制数据目录权限，不要公开暴露管理 Web/API。HTTP 管理入口本身在局域网中未加密，应仅在可信网络使用。

项目只面向受控家庭 LAN 使用，不提供公网 VPS 或 Windows 服务端支持。停止服务后，下载客户端应恢复使用自动 DNS。遇到端口冲突时，先检查实际监听者并调整网络或端口；本服务不会代替用户修改宿主机网络设置。
