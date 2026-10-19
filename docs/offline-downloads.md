# 离线下载

离线下载与 Xbox 本地回传是可选功能，默认关闭（`offlineEnabled: false`）。不需要时保留原有 DNS 优选与 HTTP 302 加速，不打开下载库、不运行下载队列。`setup.sh` 会询问，也可传入 `--offline yes|no`。已有安装未指定选项时保留当前选择。

可在 WebUI 的服务设置勾选“启用离线下载与 Xbox 本地回传”；保存前先停止加速服务。CLI 可执行 `xdp init --data-dir ./data --offline=true`，或首次运行 `serve --offline=true`。CLI `init` 修改的是磁盘配置，已有运行进程需重启才能读取它；WebUI 保存会立即切换下载库。关闭时暂停任务并保留文件，重新开启后不会自动恢复下载。

该功能把用户提供的 HTTP(S) URL 下载到 NAS，完成后可向 Xbox 回传受支持的 HTTP 资源，也可从 WebUI/CLI 取回到另一台设备。不登录 Xbox 或 Microsoft 账号、抓取商店目录或安装内容。

服务运行 Web/API 即可使用下载队列；不需要启动 DNS、HTTP 重定向或 HTTPS 转发 listener。默认文件目录为 `data/downloads`。启动时可指定 NAS 上空间更大的目录：

```sh
./xdp serve --data-dir ./data --offline=true --download-dir /volume1/xdp-downloads \
  --lan-ip 192.168.1.10
```

关闭浏览器不影响下载。`xdp stop` 只停止 DNS/HTTP/TLS 加速服务，离线任务仍会继续；停止 `serve` 进程才会暂停整个队列。

任务元数据保存在下载目录的 `tasks.json`，文件位于该目录下的 `任务 ID/文件名`。未完成文件也保留在这里，以任务状态判断是否下载完成。更换 `--download-dir` 相当于切换下载库；同一个目录仅允许一个服务实例使用。重启服务后，原先运行和排队的任务会恢复为暂停状态，等待用户继续；服务不会在重启后自行恢复下载。

## Xbox 从 NAS 下载

1. 开启“显示下载链接”并启动加速服务，将 Xbox DNS 指向 NAS 的 LAN IP，开始下载游戏以捕获链接。
2. 暂停 Xbox 下载，在日志中点击“加入离线下载”，提交任务并等待完成。一个游戏可能包含多个资源文件，需要捕获并下载所有所需资源。
3. 确认已启用离线下载，且加速服务正在运行。下载页显示每个完整、受支持的任务是否可回传。
4. 在 Xbox 上继续下载。NAS 在原 HTTP 请求上返回 `200/206`，无需管理 token；下载页和日志显示命中次数与实际发送字节。计数只统计本次加速服务运行期间的本地响应，不代表游戏安装进度。

回传只接受局域网的 `GET/HEAD`，支持原版 Xbox HTTP CDN 域名。按完整 URL 路径匹配 CDN 别名，不按文件名兜底；带 Content ID 和版本 GUID 的 Xbox 资源路径可忽略查询签名，其他路径保留完整查询参数。任务保存 `originalURL` 与实际下载 `url`，避免 `.com → .cn` 改写导致资源无法匹配。旧任务缺少 `originalURL` 时使用原 `url`。

仅提供状态为已完成、普通文件且实际大小与记录一致的内容；文件缺失、大小变化、未完成或已删除时视为未命中。文件打开后删除任务不会改变已打开连接的内容。没有额外验证官方内容哈希；同长度的外部文件改动无法仅凭大小发现，不应手动替换下载库文件。

启用后，受支持 Xbox 域名的 A 记录指向 NAS，并返回空 AAAA，避免 IPv6 绕过本地回传。未命中仍可 HTTP 302 到 CDN 别名；最终别名已指向 NAS，其未命中请求通过选定 CDN IP 或独立上游 DNS 转发，不使用本机 DNS，不循环。管理 token 与 Cookie 不转发给 CDN。此模式的未命中流量也经过 NAS，需考虑 NAS 的带宽。

HTTPS 保持加密字节透传，不支持本地文件命中。因此开启该功能需保留 TLS listener（实际部署为 443），避免 DNS 指向 NAS 后 HTTPS 失去通道。关闭功能后恢复原来的域名优选路由；客户端旧 DNS 缓存可能暂时残留，需要等待过期或重新连接网络。Microsoft Store 和普通公网 URL 的文件首版仅可取回，不能自动回传。

## CDN 与 HTTPS

下载源是 Xbox 或 Store 下载域名时，服务通过已选 CDN IP 连接，同时保留目标 URL 的 Host，并在 HTTPS 请求中保留 TLS SNI 和原站证书校验。HTTP Xbox 全球下载链接在开启国内优先且没有指定全球 IP 时，沿用 `.com → .cn` 规则，保留路径和查询参数；HTTPS 链接保持原域名。其他公网 HTTP(S) 地址也可下载。只接受 80/443 端口，不接受本机或私网目标，包括重定向后的目标。

Signed URL 可能有时效。源站 URL 过期后，服务无法替用户续签；需要获取新的 URL 并创建一个新任务。

## 队列、续传与空间

最多两个任务同时下载，其他任务在队列中等待。任务支持暂停、继续、取消和删除。删除会永久删除任务元数据和对应下载文件。

续传使用 HTTP Range 与 If-Range。若源站不支持 Range、没有提供可用于续传的 ETag/Last-Modified，或通过 If-Range 表示资源已变化，任务会从头下载。非法 Content-Range 或矛盾的资源标识会显示错误，避免把不同版本的内容拼在一起。网络断线或服务重启后，用户可以检查任务状态并决定是否继续。取消会保留已下载的文件以便检查，但不能继续；删除会释放文件空间。

下载目录所在文件系统至少保留 256 MiB 可用空间。已知资源大小会在开始下载前检查；未知大小会在接收数据时持续检查。剩余空间将触及保留值时，任务会失败或暂停，需要释放磁盘空间后由用户恢复。存储信息显示文件系统实际总量、已用空间、可用空间，以及本工具下载文件占用量。

## CLI

创建下载任务并列出任务：

```sh
./xdp download --data-dir ./data --source 'https://example.test/file.zip' --name '我的文件'
./xdp downloads --data-dir ./data
```

控制指定任务：

```sh
./xdp download-pause --data-dir ./data --id TASK_ID
./xdp download-resume --data-dir ./data --id TASK_ID
./xdp download-cancel --data-dir ./data --id TASK_ID
./xdp download-delete --data-dir ./data --id TASK_ID
```

`download-delete` 会不可恢复地删除任务及其文件。将已完成的文件流式取回到运行 CLI 的设备：

```sh
./xdp download-save --data-dir ./data --id TASK_ID --output ./file.zip
```

取回中断后可以重新执行命令，但会从头传输文件。若输出路径已存在，命令会拒绝覆盖。

所有 CLI 管理命令还可用 `--url` 与 `--token` 覆盖管理 API 地址和令牌。`--download-dir` 是 `serve` 的启动参数；其他管理命令通过 Web/API 操作任务。

## WebUI 与 API

WebUI 提供任务创建、队列状态、暂停、继续、取消、删除和取回操作。大文件取回优先使用浏览器 File System Access 流式写入能力。浏览器不支持时使用 Blob 下载，该 fallback 限制为 256 MiB；需要取回更大的文件时，建议直接访问 NAS 下载目录，或在本机使用 `download-save`。

使用 Bearer token 读取任务和存储信息：

```sh
curl -H "Authorization: Bearer $XDP_TOKEN" \
  http://127.0.0.1:8080/api/downloads
```

响应包含 `tasks`、`storage` 与 `delivery`。每个任务的 `localReady` 表示完整文件具备回传条件；`delivery` 包含 `enabled`、加速服务 `running`、`hits` 与 `bytes`。功能关闭时列表为空、空间为 `null`，已有数据保留；创建、修改和取回接口返回 409。管理 API 始终需要 token。下载目录实际挂载位置应按部署方式确认。Docker Compose 默认将 `./data/downloads` 挂载到容器的 `/downloads`，可通过 `DOWNLOAD_PATH` 指向 NAS 的大容量目录：

```sh
DOWNLOAD_PATH=/volume1/xdp-downloads LAN_IP=192.168.1.10 docker compose up -d
```

其他管理接口：

| 方法与路径 | 用途 |
| --- | --- |
| `POST /api/downloads` | JSON `{ "url": "https://example.com/file", "name": "file.bin" }` 创建任务，`name` 可省略 |
| `POST /api/downloads/{id}/pause` | 暂停并保留部分文件 |
| `POST /api/downloads/{id}/resume` | 继续暂停或失败的任务 |
| `POST /api/downloads/{id}/cancel` | 取消并保留文件 |
| `DELETE /api/downloads/{id}` | 永久删除任务及文件；运行中返回 409 |
| `GET /api/downloads/{id}/file` | 认证取回已完成文件，支持 Range |

`totalBytes: -1` 表示源站未声明总大小；`speedBPS` 是字节/秒。`storage.availableBytes` 是系统报告的调用者可用空间，WebUI 的“可供下载”显示扣除 `reserveBytes` 后的值。`downloadBytes` 是本工具现有任务文件的逻辑大小总计，包括部分文件；文件系统“已用”还包含其他程序和系统文件。
