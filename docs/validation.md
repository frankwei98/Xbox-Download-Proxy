# MVP 验证记录

日期：2026-10-18。验证环境：macOS arm64、Go 1.26.1。

## 已通过

- `go test -race ./...`：DNS UDP/TCP 本地映射与转发、空 AAAA 答复、HTTP 重定向及签名查询参数保留、未知域名拒绝、启动失败回滚、TLS ClientHello 解析和加密通道、候选源缓存/导入、测速请求 Host/Range/吞吐单位/取消、配置持久化及管理接口生命周期。
- TLS 通道测试实际建立 TLS 会话，核对原站证书未被替换，HTTP 下载内容和 SNI 保持不变。测试使用本地 fixture，不连接真实游戏 CDN。
- `go vet ./...`。
- `node --check internal/app/web/app.js`。
- 使用已有 Playwright / headless Chromium 对本地高端口实例执行浏览器交互：错误及正确 token 登录、配置读取与保存、候选导入、恶意 HTML 按文本显示、手动应用 IP、商店缺少测试 URL 提示、服务启停、运行中配置更新拒绝、HTTP 跳转日志及分类过滤、桌面和 390px 窄屏布局。未发现页面 JavaScript 异常。浏览器 fixture 中的候选 IP 仅用于界面测试，不作为有效游戏 CDN 推荐。
- CLI 实际读取本地测试实例的状态及日志。
- `make build dist`：本机 `xdp`，以及 Linux amd64/arm64、macOS amd64/arm64 四个可执行文件。Linux 产物为静态链接 ELF，macOS 产物为对应架构 Mach-O。交叉编译不等于已在每个平台运行。
- `docker compose config --quiet`。

## 离线下载扩展验证

同日新增并通过：

- 完整 `go test -race ./...`，包括后台两任务并发与排队、暂停/取消、带 ETag 的 Range/If-Range 续传、重启保留进度、源站忽略 Range 时从头下载、下载内容逐字节比较。
- 已知大小的空间预检与未知大小在接收途中触发空间预算暂停；使用注入的容量 fixture 测试边界，另用本机 statfs 核对真实空间统计。未填满真实磁盘。
- 下载库独占文件锁、异常退出后任务恢复、非法文件名和非公网目标拒绝、非法 Content-Range 与私网重定向拒绝。
- 管理 API 的文件鉴权、HTTP Range 取回、已完成任务删除；CLI 流式保存及拒绝覆盖已有文件。
- 在真实本机服务上运行 CLI 的列表/空间、创建、暂停、继续、取消、删除及流式取回命令；取回文件 SHA-256 与 fixture 一致。任务失败路径使用无监听的本机上游 DNS，不访问公网。
- 使用 Python Playwright 与已有 headless Chromium 检查真实磁盘空间显示、进度、文件名按文本显示、任务创建/失败/取消/删除、刷新后的持久状态、认证文件取回并核对 SHA-256，以及桌面/390px 布局，无页面 JavaScript 异常。浏览器测试的完成/部分文件为预置 fixture；实际网络下载及续传由 Go 本机 HTTP fixture 覆盖，没有连接真实游戏下载 CDN。
- 更新后的 Linux/macOS amd64/arm64 可执行文件交叉构建、`go vet`、JavaScript 语法检查和 Compose 配置检查。

执行网络测试时需要测试进程能够绑定本机临时端口；受限执行环境最初禁止 socket bind，放开本地测试进程后测试通过。开发环境 Go 缓存使用 `/tmp/xdp-go-cache`，未改动宿主网络配置。

## 安装与节点优选改进验证（2026-10-19）

- `go test ./...`、`go vet ./...` 和 JavaScript 语法检查通过；管理 API 测试确认新安装的加速服务保持关闭、节点筛选模块可正常加载。
- `make test-ui` 覆盖多关键词搜索、地区与运营商组合筛选、缺少地区标注、无匹配项、去重与抽样上限，以及排除失败/无效测速结果的推荐逻辑。
- `bash -n`、ShellCheck 和 `make test-setup` 通过。安装测试使用 mock Go / Docker，覆盖 native / Compose、重复运行保留 token、覆盖确认、特殊路径、端口/IP 校验和构建失败。
- 在临时目录实际执行 native 安装与重复安装，确认 token 在终端展示，配置和 token 保留，默认关闭加速服务，生成的启动脚本通过语法检查。
- 通过真实 Docker Compose CLI 解析生成的配置，核对绝对构建路径、host 网络、带空格/引号/美元符号的挂载路径与默认启动参数；镜像构建和容器运行仍未实测。
- Playwright / headless Chromium 对本地高端口实例验证多关键词搜索、地区无匹配提示、自动选择 16 个节点、最快有效结果推荐、应用后保持关闭、手动启停、管理连接中断与恢复，以及 375px 布局，无页面 JavaScript 异常。候选和测速响应使用 fixture，不访问真实游戏下载 CDN。

## 可选本地回传验证（2026-10-19）

- `go test -race ./...`：实际 HTTP fixture 下载后按原始资源匹配，CDN 别名与版本路径签名变化、重启后的查找、文件大小变化拒绝；完整文件回传、HEAD、正向与后缀 Range、416、并发分段读取和本地计数。
- 本机 DNS/HTTP fixture 验证回传模式的 Xbox 域名映射、空 AAAA、未命中时 HTTP 302，以及最终 CDN 别名通过选定 IP 转发并保持签名查询和 Range；非局域网及未知域名拒绝。
- 默认关闭不创建下载库，管理 API 禁止创建任务；开关切换保留任务和文件。CLI 初始化显式 true/false 生效，未指定保留现值。cn2 空测速 URL 自动补默认，已有自定义 URL 保留。
- 安装脚本 mock 测试覆盖 native / Compose 的 `--offline yes|no` 转发，缺省不覆盖配置；`bash -n`、JavaScript 语法检查、地区筛选测试、`go vet` 与构建通过。
- Playwright / headless Chromium 连接本地高端口实例，验证默认关闭、WebUI 启用与服务未启动提示、实际 HTTP Range 内容与 57 字节计数、日志“加入离线下载”预填、关闭后文件保留和 375px 布局；无页面 JavaScript 异常。完成文件为预置 fixture，未连接真实游戏下载 CDN。
- 在临时目录实际执行 native 安装的开启、保留选择、关闭、再次保留选择，确认 `offlineEnabled`、token 和 `autoStart`，尚未运行时不创建下载库。

## Debian / Alpine 安装兼容验证（2026-10-19）

- 安装入口、启动脚本和安装测试改用 POSIX `/bin/sh`，通过语法检查、ShellCheck、`make test-setup`；不再依赖 Bash 或假定 `/usr/bin` 中没有 Go。完整 Go 测试、`go vet` 和节点筛选测试通过。
- Debian 12 amd64、Debian 13 arm64、Alpine 3.22 amd64、Alpine 3.24 arm64 临时容器中运行安装测试及真实预编译安装：显式安装证书但不安装 Go、配置/token 保留、管理 API 鉴权、默认关闭加速服务、启动标准低端口，以及运行中替换二进制。安装路径包含空格、引号、美元符号和 `%`。
- Alpine 3.23 arm64 的新环境通过 `--install-deps` 自动安装证书及 Go 1.25.10，从源码构建、初始化并启动服务。仓库根目录存在不可在 Linux 执行的 macOS 二进制时，会安装 Go 并重新构建。`readelf` 确认产物没有动态解释器。
- Alpine 3.22 / 3.23 / 3.24 使用真实 OpenRC 工具和容器内运行状态 fixture，验证注册、开机启动链接、服务启动/停止/重启、运行中升级及管理 API；日志目录/文件限制为 0700/0600。fixture 不代表完整启动过 Alpine VM 或 LXC。
- Debian 12 / 13 使用真实 systemd 工具验证 unit 语法、特殊路径转义和离线启用。未运行 init 的容器中，`--start` 会在注册服务之前明确拒绝；没有把 `systemctl` 已安装等同于 systemd 正在运行。
- 新增 Installer CI，覆盖 Debian 12/13、Alpine 3.22/3.23，包含 Alpine 源码安装路径。CI 配置已写入仓库，尚未在远端执行。

## 尚未验证

- 尚未进行真实 VM/LXC 的完整开机自启、systemd 在线服务启停及 NAS 安装验证。上面的安装验证使用临时 Docker 容器，未修改宿主机 DNS、防火墙或系统服务；不等于已验收项目 Dockerfile/Compose 的正式部署流程。
- 未进行 Xbox 主机、Windows Xbox App 或 Microsoft Store 的实际下载、断点续传及优选收益测试；由用户按 [手工验收清单](manual-test.md) 自行验证。
- 候选 IP 列表和默认测速资源的大陆网络可达性及每个实际游戏的 CDN 缓存命中，不属于本地 fixture 测试可证明的事实。

cn2 使用公开 Xbox 测试资源 URL；store 不依赖作者商店 API 获取测试资源，首次测速需要用户提供真实可下载 URL。Microsoft Store 域名的 DNS 指定 IP 功能不依赖该 URL。
