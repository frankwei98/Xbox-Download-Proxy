# 开发与验证

返回 [README](../README.md)。以下命令在项目根目录执行；普通用户安装和运行服务请先看 README，无需完成本页的开发检查。

## 构建与检查

```sh
make build  # 本机可执行文件 ./xdp
make test   # Go 测试
make test-race # Go 竞态检查，禁用测试缓存
make test-cover # 竞态检查及语句覆盖率报告，写入 /tmp/xdp-coverage.out
make test-ui # 节点筛选 / 推荐逻辑测试（需要 Node.js 18+）
make test-setup # 安装脚本 mock 测试（无需真实安装或 Docker）
make vet    # go vet
make check  # 竞态测试、前端逻辑、安装脚本、go vet 与 JavaScript 语法检查
make dist   # Linux amd64/arm64、macOS arm64 交叉构建到 dist/
```

Pull request、推送到 `main` 和符合 `v主版本.次版本.修订版本` 格式的版本 tag（例如 `v0.1.0`）会在 GitHub Actions 中运行检查，避免同一分支提交因 push 和 pull request 事件重复运行。检查包括 Go 竞态与覆盖率测试、`go vet`、前端逻辑和安装脚本 mock 测试；macOS 测试仅覆盖 arm64，Linux 测试与安装验证仅覆盖 Alpine 3.23 arm64 和 Debian 13 amd64。测试与三个目标的构建并行执行，同一 PR 的新提交会取消旧检查。版本 tag 会打包 Linux amd64、Linux arm64 和 macOS arm64 三个发布目标；全部测试、安装验证和构建成功后，Actions 会自动创建或更新同名 GitHub Release，并附上三个 `xdp-<tag>-<系统>-<架构>.tar.gz` 压缩包及对应的 `.sha256` 校验文件。arm64 也称 aarch64。tag 构建仍会将各目标作为 artifact 保留，可在仓库的 Actions 页面下载。重新运行同一 tag 的工作流会更新该 Release 中的对应资产。

```sh
git tag v0.1.0
git push origin v0.1.0
```


## 手工验收

从 [手工验收清单](manual-test.md) 开始。该清单区分本机检查、同网段客户端检查和 Xbox/Store 实机检查；本仓库目前尚未报告实机验收完成。

已执行的自动化检查及运行限制见 [MVP 验证记录](validation.md)。
