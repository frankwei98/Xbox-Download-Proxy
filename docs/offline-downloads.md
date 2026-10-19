# 离线下载

离线下载把用户提供的 HTTP(S) URL 下载到加速服务端的磁盘，方便从 NAS 目录直接访问，或从 WebUI/CLI 取回到另一台设备。它是独立 URL 下载功能，不登录 Xbox 或 Microsoft 账号、抓取商店目录或安装内容。

服务运行 Web/API 即可使用下载队列；不需要启动 DNS、HTTP 重定向或 HTTPS 转发 listener。默认文件目录为 `data/downloads`。启动时可指定 NAS 上空间更大的目录：

```sh
./xdp serve --data-dir ./data --download-dir /volume1/xdp-downloads \
  --lan-ip 192.168.1.10
```

关闭浏览器不影响下载。`xdp stop` 只停止 DNS/HTTP/TLS 加速服务，离线任务仍会继续；停止 `serve` 进程才会暂停整个队列。

任务元数据保存在下载目录的 `tasks.json`，文件位于该目录下的 `任务 ID/文件名`。未完成文件也保留在这里，以任务状态判断是否下载完成。更换 `--download-dir` 相当于切换下载库；同一个目录仅允许一个服务实例使用。重启服务后，原先运行和排队的任务会恢复为暂停状态，等待用户继续；服务不会在重启后自行恢复下载。

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

响应包含 `tasks` 与 `storage`。下载目录实际挂载位置应按部署方式确认。Docker Compose 默认将 `./data/downloads` 挂载到容器的 `/downloads`，可通过 `DOWNLOAD_PATH` 指向 NAS 的大容量目录：

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
