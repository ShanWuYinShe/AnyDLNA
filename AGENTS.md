# AnyDLNA

Go + Wails v2 桌面应用：把在线视频（yt-dlp 解析 YouTube/B 站等）与本地任意格式视频投到局域网 DLNA 电视/盒子。

通用约束以全局 `~/AGENTS.md` 为准，本文件只做项目特有补充。

## 关键事实

- `go test` 依赖系统 PATH 上的外部二进制：`ffmpeg`、`yt-dlp`、`qjs`（brew 安装；查找链与 PATH 补齐原理见 README.md）。
- 构建发布走 `scripts/build-app.sh`；对外部工具做 pin 版本打包走 `scripts/bundle-tools.sh`。
- `frontend/` 是纯原生 JS（无 package.json、无构建链），不要为它引入打包工具。
- `cmd/faketv` 是模拟 DLNA 电视，供 `internal/media` 的 faketv E2E 与手动试用；`negotiation_itest_test.go` 用的是真实设备（`ANYDLNA_TV_HOST`）。
- 功能说明与依赖查找链详见 README.md。
