# AnyDLNA

Go + Wails v2 桌面应用：把在线视频（yt-dlp 解析 YouTube/B 站等）与本地任意格式视频投到局域网 DLNA 电视/盒子。

通用约束以全局 `~/AGENTS.md` 为准，本文件只做项目特有补充。

## 关键事实

- 默认测试套件无需外部工具（依赖二进制的用例缺失时自动 skip）；仅集成测试需要系统 PATH 上的 `ffmpeg`、`yt-dlp`、`qjs`，缺失时先 `brew install ffmpeg yt-dlp quickjs`（查找链与 PATH 补齐原理见 README.md）。日常入口 `./scripts/test.sh`（`RACE=1` 启用竞态检测）。
- 构建发布走 `scripts/build-app.sh`；对外部工具做 pin 版本打包走 `scripts/bundle-tools.sh`（下载物经 sha256 pin 校验，升级版本需同步更新哈希）。
- `frontend/` 是纯原生 JS（无 package.json、无构建链），不要为它引入打包工具；`frontend/wailsjs/runtime/package.json` 是 Wails 生成文件，属此约定的例外，勿手改、勿据此认为存在前端构建链。
- `cmd/faketv` 是模拟 DLNA 电视，供 `internal/media` 的 faketv E2E 与手动试用；`negotiation_itest_test.go` 用的是真实设备（`ANYDLNA_TV_HOST`）。
- 功能说明与依赖查找链详见 README.md。
