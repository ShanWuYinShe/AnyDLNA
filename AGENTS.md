# AnyDLNA

Go + Wails v2 桌面应用：把在线视频（yt-dlp 解析 YouTube/B 站等）与本地任意格式视频投到局域网 DLNA 电视/盒子。

通用约束以全局 `~/AGENTS.md` 为准，本文件只做项目特有补充。

## 关键事实

- 默认测试套件无需外部工具（依赖二进制的用例缺失时自动 skip）；仅集成测试需要系统 PATH 上的 `ffmpeg`、`yt-dlp`、`qjs`，缺失时先 `brew install ffmpeg yt-dlp quickjs`（查找链与 PATH 补齐原理见 README.md）。完整套件 `./scripts/test.sh`（`RACE=1` 启用竞态检测）由 CI 门禁执行；本地只在调试单个用例时按 README「测试」直接跑单包命令。
- 构建发布走 `scripts/build-app.sh`；对外部工具做 pin 版本打包走 `scripts/bundle-tools.sh`（下载物经 sha256 pin 校验，升级版本需同步更新哈希）。
- `frontend/` 是纯原生 JS（无 package.json、无构建链），不要为它引入打包工具；`frontend/wailsjs/runtime/package.json` 是 Wails 生成文件，属此约定的例外，勿手改、勿据此认为存在前端构建链。
- `cmd/faketv` 是模拟 DLNA 电视，供 `internal/media` 的 faketv E2E 与手动试用；`negotiation_itest_test.go` 用的是真实设备（`ANYDLNA_TV_HOST`）。
- 功能说明与依赖查找链详见 README.md。

## CI 门禁与本地验证分工

重活（全量测试、构建、打包、E2E、镜像构建）一律交给 GitHub Actions，本地只跑秒级检查。
完整流程见全局 skill `github-ci-flow`：开发分支推送 → `gh pr create` → `gh pr checks --watch`
全绿 → `gh pr merge --rebase --delete-branch` → 本地 `git pull --ff-only` 并清理 worktree。

- 本地（提交前，秒级；本机实测均不超过 2 秒）：

  ```bash
  gofmt -l $(git ls-files '*.go')   # 应无输出
  go vet ./...
  go build ./...                    # 只验证本机平台可编译
  ```

- CI 覆盖（`.github/workflows/ci.yml`，单 job `ci`）：gofmt 检查 → `go vet ./...` →
  清空集成测试环境变量（清单与 `scripts/test.sh` 的 unset 同步）→ `go test -race ./...` →
  `GOOS=darwin/windows/linux go build ./...` 三平台交叉编译校验。全量测试与竞态检测以 CI 结果为准。
- CI 覆盖不到、必须本地/真机验证的：`wails build` 图形产物（需要各平台 WebView 工具链，本地按需
  `./scripts/build-app.sh`）；`ANYDLNA_*` 集成测试需要真实媒体样本、在线流（ffmpeg/yt-dlp）、浏览器
  Cookie、局域网组播或真实 DLNA 设备（命令见 README「测试」），CI 不跑。
- 发版/发布门禁不变：按 RELEASE.md 在本机手动完成 `wails build` → `scripts/bundle-tools.sh` →
  Developer ID 签名 → 公证 → staple；CI 不产出构建产物、不分发。
