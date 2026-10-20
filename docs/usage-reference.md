# 工作原理与命令行参考

首次使用请先阅读 [README](../README.md)。本页说明节点来源、测速行为和命令行操作；离线任务另见[离线下载说明](offline-downloads.md)。

## 工作方式

DNS 可把受控下载域名解析到用户为域名组指定的 CDN IP。需要展示或改写的 Xbox 下载请求可以先经过本服务的 HTTP 重定向入口，之后大文件由下载客户端直接连接 CDN。Microsoft Store 使用独立域名组。HTTPS 入口只做 SNI 原样转发，不安装 CA、不解密 TLS，也不改写 HTTPS 域名。

服务会尝试从配置中指定的公开在线文本源读取候选 IP；默认引用 [参考项目的在线 IP 列表](https://github.com/skydevil88/XboxDownload/tree/master/IP)，本仓库没有附带其列表副本。下载成功后可用缓存，更新失败会保留旧缓存；无缓存时回退到当前域名的 DNS 结果并显示警告。可以在 WebUI 配置自己的 HTTPS 文本源，也可以导入 IP 列表。首次使用没有缓存时需要联网；候选源的可用性和更新由其提供方决定。项目不调用参考项目作者的商店 API，不做遥测，也不上传下载链接或测速数据。

候选节点测速需要由服务端访问测试 URL，并按用户提供的 IP 发起测试。`cn1` 默认使用 `http://assets1.xboxlive.cn/Z/routing/extraextralarge.txt`，`cn2` 默认使用 `http://assets2.xboxlive.cn/Z/routing/extraextralarge.txt`，`global` 默认使用 `http://assets1.xboxlive.com/Z/routing/extraextralarge.txt`。`store` 没有默认测试 URL，需要在配置中填写真实可下载的测试资源 URL；未填写时会明确提示无法测速。Store 也可以不测速，直接指定 IP。

吞吐测试结果只代表该测试 URL、该 IP 和当时网络条件下的测试表现，不能作为游戏实时下载速度或实际 CDN 命中情况的保证。


## CLI 操作

下面的命令假设当前目录中有 `xdp` 程序，数据位于 `./data`。引导安装用户可进入安装目录（默认为 `~/.local/share/xdp`）执行；使用其他目录时，请同步修改程序路径和 `--data-dir`。管理操作需要 Web/API 正在运行。

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
