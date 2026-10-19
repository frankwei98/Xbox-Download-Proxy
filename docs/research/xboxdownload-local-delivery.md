# XboxDownload 离线下载与本地回传评估

核对日期：2026-10-19。参考源码固定于 skydevil88/XboxDownload 的提交 [844a13638158e0be19c7fb3bca873212c27e7c42](https://github.com/skydevil88/XboxDownload/tree/844a13638158e0be19c7fb3bca873212c27e7c42)；本项目对照提交为 [81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb](https://github.com/frankwei98/Xbox-Download-Proxy/tree/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb)。这是源码静态分析，没有进行 Xbox、NAS 或参考程序的实机下载验证。本文建议不代表已经实现。

## 结论

参考项目的“PC 下载回传”符合用户想要的“提前下载完整资源，再通过局域网供 Xbox 获取”的核心目标。但它把下载交给外部工具，本地命中时由监听服务直接发送文件，返回 `200/206`；不是把命中请求 `302` 到另一台本机文件服务器。`302` 是未命中后部分 CDN 切换路径的机制。[使用说明](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/README.md#L71-L83)、[HTTP 命中分支](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/TcpConnectionListener.cs#L438-L458)、[文件响应](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/TcpConnectionListener.cs#L998-L1029)。

本项目已经有后台下载、完成状态检查和支持 Range 的管理文件接口，缺少的是把这些能力接到 Xbox 的原始资源请求上。应补齐这个闭环，保留已有下载器；不需要把参考项目的整个桌面应用搬过来。[任务模型](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/offline/manager.go#L33-L46)、[完成文件检查](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/offline/manager.go#L348-L367)、[现有文件接口](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/app/downloads.go#L94-L109)、[当前 HTTP 处理](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/engine/runtime.go#L150-L179)。

## 参考项目的工作方式

1. 开启“显示下载链接”，让 Xbox 开始下载，监听得到资源 URL；暂停 Xbox 下载，将 URL 交给 PC 下载工具。README 明确描述的是这个流程，不能据此认为参考程序有一个完整的内置任务下载器。[操作步骤](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/README.md#L71-L81)。
2. 把“本地上传”目录指向下载目录，开启本地上传并启动监听。界面启用本地上传时，会同时启用“显示下载链接”和 HTTP 服务；该回调没有强制开启 DNS，因此 DNS 配置和请求能否到达监听器仍然是独立条件。[设置联动](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/ViewModels/ServiceViewModel.cs#L618-L639)。
3. Xbox 再次请求资源时，HTTP 监听器提取 Host 和路径，去掉查询参数，优先查询本地文件；命中则直接写回同一连接，未命中才进入后续 CDN 重定向等处理。[请求分支](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/TcpConnectionListener.cs#L417-L458)。

### 文件映射和更新

目录默认是程序所在目录下的 `Upload`，可以自定义；启用监听前会清理目录末尾分隔符并保存设置。[初始化](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/ViewModels/ServiceViewModel.cs#L50-L57)、[保存目录配置](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/ViewModels/ServiceViewModel.cs#L394-L420)。

映射没有预建数据库或扫描索引：每个请求先检查“上传目录 + 请求相对路径”，再检查“上传目录 + 文件名”。前一个候选检查规范化后的路径仍位于目录前缀内。查找函数只接收路径，没有 Host、任务 ID 或 Content ID 参数。因此可以保持原目录层次，也可以把保留原文件名的文件直接放到目录根部；不同域名、不同路径下的同名文件可能命中同一个文件。[完整查找函数](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/TcpConnectionListener.cs#L951-L969)。

由每次请求执行 `File.Exists` 可推断，手动新增或移除文件不需要重新生成索引；但同一函数没有下载完成状态、预期长度或哈希检查，只能证明“文件存在”，不能证明它是目标资源的完整版本。文件在检查后打不开时返回 `500`，不会自动转入 CDN 未命中分支。上线实现不应照搬这种就绪判断。[候选选择](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/TcpConnectionListener.cs#L951-L989)。

### DNS 和优选 IP 会影响是否命中

本地上传通过“显示下载链接”间接影响路由。Xbox 三组 DNS 映射传入这个开关；HTTP 和该开关都开启时，通常把名称不含 `2` 的域名指向本机，含 `2` 的域名仍使用选定 IP。简体中文且未指定全球节点 IP 的特殊分支可把全球组都指向本机。它不是无条件把所有游戏下载流量送进本地文件服务。[三组映射](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/DnsConnectionListener.cs#L474-L476)、[映射条件](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/DnsConnectionListener.cs#L935-L977)。

对本项目的启示是：本地回传必须明确覆盖哪些域名与协议，确保这些请求到达服务；只加一个文件查找函数，仍可能因为 DNS 直接返回优选 CDN IP 而被绕开。本项目目前也按 `ShowLinks`、域名是否含 `2` 和优选 IP 分流，需一起评估。[当前域名路由](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/domain/rules.go#L35-L55)。

### 文件响应和 HTTPS 的边界

参考实现会声明 `Accept-Ranges: bytes`，正向单段范围请求返回 `206`，范围越界返回 `416`，并从文件指定偏移流式发送。值得复用的是“支持分段取完整文件”的目标，而非手写 HTTP 细节：Range 正则没有忽略大小写选项，不支持 `bytes=-N` 后缀形式，也没有完整多段语义；请求解析接受 `HEAD`，文件发送函数仍然进入写文件体循环，没有按方法省略响应体。[Range 与响应头](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/TcpConnectionListener.cs#L998-L1043)、[方法与 Range 正则](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/TcpConnectionListener.cs#L1397-L1413)。

HTTPS 分支使用 `SslStream` 和本地证书终止 TLS，握手成功后也会进入同样的本地文件匹配；Windows 分支生成自签证书，其他平台使用本地 CA 签发的证书。这证明参考实现具备解密后的处理分支，不证明 Xbox 能信任这张证书或所有 HTTPS 游戏下载都可用。[证书生成](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/TcpConnectionListener.cs#L279-L318)、[TLS 终止与命中](https://github.com/skydevil88/XboxDownload/blob/844a13638158e0be19c7fb3bca873212c27e7c42/XboxDownload/Services/TcpConnectionListener.cs#L694-L720)。

## 本项目的具体差距

| 能力 | 当前情况 | 需要补齐 |
| --- | --- | --- |
| 提前下载到 NAS | 已有任务和来源 URL | 保留原始资源身份，关联可回传状态 |
| 提供已完成文件 | 管理 API 能提供文件 | 增加 Xbox 能访问的受限局域网交付入口 |
| 原始资源请求命中 | HTTP 只做 CDN 重定向 | 在 CDN 回退前查询完成文件映射 |
| DNS 接入 | 部分域名可能直达优选 IP | 开启回传时保证支持的请求先到本机 |
| HTTPS 资源路径 | SNI 原样隧道看不到路径 | 首版明确限制为 HTTP；HTTPS 原样转发 |

依据：[任务字段](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/offline/manager.go#L33-L46)、[管理 API 的 Bearer token 保护](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/app/http.go#L152-L166)、[HTTP 重定向](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/engine/runtime.go#L150-L179)、[DNS 分流](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/domain/rules.go#L35-L55)、[TLS ClientHello 原样读取与转发](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/engine/tls.go#L11-L13)。

有两个实现细节应保留：`OpenCompleted` 已要求任务完成、文件为普通文件且大小与记录一致；管理文件接口已经使用 Go 的 `http.ServeContent`，不能说本项目完全没有 Range 支持，准确说法是没有把这套交付能力接入 Xbox 原始请求。[完成文件检查](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/offline/manager.go#L348-L367)、[ServeContent](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/app/downloads.go#L94-L109)。

另外，添加任务前可能把全球域名改写成国内 CDN 域名，再保存到 `Task.URL`。如果直接用当前任务 URL 与 Xbox 的原始 URL 做精确字符串匹配，会漏掉这类资源。需要保存原始 URL 和实际下载 URL，或用经过验证的域名别名规则建立资源身份。[添加前改写](https://github.com/frankwei98/Xbox-Download-Proxy/blob/81a38fd907c8d03332b1a0bb4a77bd2d9c2cb9fb/internal/app/downloads.go#L55-L65)。

## 建议的首版范围

以下是设计建议，不是上游已经具备的能力：

- 保留 NAS 后台下载器，任务完成并检查文件后登记为可回传。UI 区分“下载中”“已下载，尚未启用回传”“可回传”，记录实际 Xbox 命中与已发送字节。
- 以原始资源路径及明确的域名别名组建立索引，同时保留来源 URL、长度和已有 ETag/Last-Modified；不要全局使用文件名兜底。查询参数仅在确认是临时授权参数时才排除，不泛化为所有 URL 的通用规则。
- 首版仅覆盖已支持且能解读的 HTTP 原始请求，命中就在当前监听连接返回文件，复用 `ServeContent` 的 `HEAD`、`Range`、`206/416` 处理。目录文件缺失或大小变化时撤销可回传状态，继续现有 CDN 路径；传输已开始后的 I/O 故障只能终止当前响应并允许客户端重试。
- 若坚持使用 `302`，额外提供 Xbox 可达的 NAS 局域网文件地址，目标不能指向 `localhost` 或需要管理 token 的 API；Docker 需使用宿主机可达地址，防止 DNS 与重定向循环。它是部署方案选择，不是复刻上游所必需的协议步骤。
- 回传文件入口只提供已登记完成文件、只允许局域网 `GET/HEAD`，不把管理 token 放进 Xbox 请求或重定向 URL。外部下载文件可作为下一步的显式导入功能，导入时绑定原始资源并检查完整性，不直接开放任意目录。
- HTTPS 保持现有加密隧道并明确显示不支持本地命中。不要因参考源码有 TLS 解密分支，就承诺 Xbox 信任自签证书或所有 HTTPS 下载都能回传。

首版应重点验证：真实 Xbox 的请求 URL 与 Range、CDN 别名变化、缺失或变更文件回退、更新版本不误命中、Docker/NAS 局域网可达性，以及一台 Xbox 的实际下载完成。源码可证明机制存在，不能替代这些兼容性验收。
