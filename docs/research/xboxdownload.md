# XboxDownload 源码研究

参考仓库：[skydevil88/XboxDownload](https://github.com/skydevil88/XboxDownload)。研究固定于提交 [814ccf3f0d97028abf45440962942261254177a7](https://github.com/skydevil88/XboxDownload/tree/814ccf3f0d97028abf45440962942261254177a7)，核对日期为 2026-10-18。

以下为静态源码分析；没有运行原程序或验证当前真实下载链路。源码通过 HTTP 归档读取，未向本仓库引入上游代码、资源或 IP 表。

## 主要发现

### 现有产品形态

当前上游使用 .NET 10 与 Avalonia 12，已有 Linux/macOS 发布脚本。入口直接启动桌面生命周期，网络 listener 依赖 ServiceViewModel 及全局 App.Settings。新项目的主要变化是提供适合 NAS 的独立服务、CLI 和 WebUI，而不是单纯移植 Windows 程序。

来源：[项目配置](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/XboxDownload.csproj)、[程序入口](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Program.cs)、[服务 ViewModel](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/ViewModels/ServiceViewModel.cs)。

### 核心加速路径

DNS 将指定下载域名映射到选定的 CDN IP，或先映射到本机 HTTP 服务。HTTP 服务对部分 Xbox 域名返回 302，保留路径和查询参数，并将全球域名换为中国大陆下载域名。后续大文件通常由下载客户端直接向 CDN 获取。Microsoft Store 域名独立成组，主要使用 CDN IP 映射，没有同样的 Xbox 国内域名重写。

因此，上游的主要路径不是全流量反向代理。直连 CDN 的下载速率与失败情况无法仅凭该应用的 HTTP 日志完整得知；其 Range 处理也不能直接作为完整反代实现的依据。

来源：[域名组](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/DnsMappingGenerator.cs#L16)、[DNS 映射](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/DnsConnectionListener.cs#L432)、[HTTP 重定向](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/TcpConnectionListener.cs#L417)。

### HTTPS 与 IPv6

上游 HTTPS listener 使用自建 CA 终止 TLS，可选 SNI 代理服务于其他站点；不能据此推断 Xbox 和 Store 下载必须解密 HTTPS。DNS 对部分 IPv4 映射的 AAAA 查询返回空答复，避免 IPv6 绕过所选节点。MVP 的 HTTPS 与 IPv6 行为需要单独设计及实机验证。

来源：[TLS 处理](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/TcpConnectionListener.cs#L687)、[默认 SNI 规则](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Models/LocalProxy/LocalProxyBuilder.cs)、[AAAA 行为](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/DnsConnectionListener.cs#L935)。

### 测速与节点取得

上游维护不同平台的 IP 文本表。HTTP 测速强制连接指定 IP，同时保留原域名；HTTPS 使用原域名进行 TLS 校验，Range 下载用于衡量吞吐。自动 Akamai 优选会先筛候选，再进行小文件并发竞速。这些测试不能保证每个实际游戏资源都在获选节点命中缓存。

来源：[IP 表](https://github.com/skydevil88/XboxDownload/tree/814ccf3f0d97028abf45440962942261254177a7/IP)、[优选流程](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/SpeedTestService.cs#L17)、[指定 IP 测速](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Helpers/Network/HttpClientHelper.cs#L164)。

### 外部依赖与复用边界

Microsoft Store 测速 URL 的取得依赖作者后端；部分测速结果及发现的游戏下载链接也会向作者后端提交。新项目不能把这些依赖和数据发送行为当作本地优选的必然组成部分。

所检查归档及源码中未发现 LICENSE、COPYING、NOTICE 或明确开源许可声明；这只是检查结果，不代表已取得复用授权。当前研究用于理解行为，是否复用源代码、IP 表及素材需另外确认。

来源：[后端请求](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/UpdateService.cs#L924)、[测速上报](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/ViewModels/SpeedTestViewModel.cs#L884)、[下载链接上报](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/TcpConnectionListener.cs#L620)、[仓库文件](https://github.com/skydevil88/XboxDownload/tree/814ccf3f0d97028abf45440962942261254177a7)。

## 后续必须验证的事实

- 当前 Xbox 与 Microsoft Store 实际使用的域名、HTTP/HTTPS 比例及接入限制。
- HTTP 重定向后的 Range、签名 URL 和资源可用性。
- 冷门资源在不同 CDN 上的缓存命中与失败行为。
- 家庭实际网络下的优选收益，以及 NAS 端口和网卡吞吐约束。

这些是后续实验事项，不应把上游存在相关代码等同于新项目已达到兼容性要求。

## 用户截图的进一步核对

用户提供两张 v3 截图，分别展示测速列表及服务配置。以下对应所检查源码版本 3.0.0.65；不能据此断言截图来自同一个具体构建。

### 为什么有 HTTP(S) 勾选和流量数字

HTTP(S) 开关启动 TCP 80 与 443 的 listener；HTTP handler 包含 Xbox 重定向、本地文件回传和连接检测等路径，可选 HTTPS SNI 代理是另一类能力。勾选该开关并不决定客户端所有游戏下载字节是否经过应用。

底部 Traffic 每秒读取所选网卡 GetIPStatistics().BytesSent / BytesReceived 的差分。它包含该网卡其他应用的流量，不是代理专属或 Xbox 专属统计。

来源：[服务启动](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/ViewModels/ServiceViewModel.cs#L443)、[监听端口](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/TcpConnectionListener.cs#L333)、[流量计数](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/ViewModels/ServiceViewModel.cs#L873)。

### 截图配置对应的数据路径

假设简体中文、DNS 与 HTTP 开启、无额外 Akamai 全局覆盖，L 为本机 LAN IP，G/C/S 为指定 CDN IP：

| 配置 | DNS 与后续行为 |
| --- | --- |
| Xbox com 留空 | com 下载域名指向 L；HTTP 重定向到对应 cn 下载域名 |
| Xbox com 指定 G，不显示链接 | com 下载域名直接指向 G |
| Xbox com 指定 G，显示链接 | 1 号下载域名指向 L，302 到 2 号下载域名后直连 G |
| cn1 指定 C，不显示链接 | cn1 组直接指向 C |
| cn1 指定 C，显示链接 | assets1/d1 指向 L，302 到 assets2.cn 后直连 C |
| Store 指定 S | Store 三个域名直接指向 S，不受 Xbox 显示链接开关影响 |

来源：[配置读取及映射](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/DnsConnectionListener.cs#L432)、[映射生成条件](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/DnsConnectionListener.cs#L935)、[重定向目标](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/TcpConnectionListener.cs#L447)。

### 预设候选来源的真实体验

首次打开测速页选中的是占位项，用户选择具体组后加载该组 IP 表。缓存缺失时联网取得候选列表，存在缓存则可读缓存；更新条件使用 LastAccessTimeUtc，因此不能称其为严格每日更新机制。列表内容由项目维护，用户无需自行收集 IP。

读取流程为选择组 → 从在线来源下载或读缓存 → 按地区/运营商筛选或搜索 → 用户勾选测速 → 手动使用指定 IP。测速结束不默认等于自动应用最快节点。

进一步检查的 Linux-x64 v3.0.0.65 发布 ZIP 只包含主程序、启动脚本、README，没有外置 IP 表。结合源码资源声明和加载逻辑，可以确认首次加载无缓存的候选表依赖网络；未反编译发布二进制。

来源：[初始选择](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/ViewModels/SpeedTestViewModel.cs#L58)、[列表加载](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/ViewModels/SpeedTestViewModel.cs#L137)、[来源及首项定义](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Models/SpeedTest/SpeedTestDataBuilder.cs#L26)、[手动应用](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/ViewModels/SpeedTestViewModel.cs#L435)、[发布版本](https://github.com/skydevil88/XboxDownload/releases/tag/v3.0.0.65)。

### 核心代码是否实质存在

核心源码包含 socket bind/accept、DNS 报文解析与构造、HTTP 请求解析、302 响应、指定 IP 的 HTTP 测速与本地文件 Range 响应。检查的核心目录未见将这些逻辑转交私有二进制的路径，也未见对应实现只有 NotImplementedException 占位。属性和命令中的 partial 声明配合 CommunityToolkit source generator，不能据此认定实现未公开。

该结论仅针对可审查的本地核心实现；没有构建验证、真实设备测试或发布二进制与源码的一致性验证。部分商店 API 后端实现未在本仓库发现。

来源：[DNS 实际处理](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/DnsConnectionListener.cs#L690)、[HTTP 读取](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/Services/TcpConnectionListener.cs#L1092)、[声明的包依赖](https://github.com/skydevil88/XboxDownload/blob/814ccf3f0d97028abf45440962942261254177a7/XboxDownload/XboxDownload.csproj#L19)。
