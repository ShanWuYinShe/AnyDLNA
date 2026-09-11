# AnyDLNA

基于 Go + [Wails v2](https://wails.io) 的 DLNA 投屏应用：把本地**任意格式**的视频投到局域网内的电视 / 盒子上播放。

## 功能

- **设备发现**：SSDP 组播搜索局域网内的 DLNA MediaRenderer（电视、盒子），自动解析设备描述。
- **任意视频**：`ffprobe` 探测编码；`H.264 + AAC` 的 MP4/MOV 原文件直出（带 Range，支持拖动进度）；其余格式（MKV、HEVC、AVI、FLV、RMVB……）由 `ffmpeg` 实时转码为 MPEG-TS 供电视端拉流。
- **播放控制**：播放 / 暂停 / 停止、进度跳转（转码模式跳转时 ffmpeg 从新位置重启）、音量调节（RenderingControl）。
- **流服务**：应用内置 HTTP 服务监听局域网可达地址，电视端通过 `SetAVTransportURI` 拉取本机流。

## 环境依赖

- Go ≥ 1.25、[Wails CLI v2](https://wails.io/docs/gettingstarted/installation)（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）
- `ffmpeg` / `ffprobe`（macOS：`brew install ffmpeg`）。未安装时仅支持直出格式，其余格式会提示安装。

## 运行与构建

```bash
wails dev     # 开发模式（热重载）
wails build   # 产出 build/bin/AnyDLNA.app（macOS）
```

## 使用

1. 打开应用，点击「搜索设备」选择电视（需与电脑同一网络且电视开启 DLNA）。
2. 点击「选择视频」选取任意视频文件。
3. 点击「投屏到选中设备」，底部出现播放控制条即可控制。

## 代码结构

```
main.go                  # Wails 入口
app.go                   # 绑定给前端的业务层（搜索/选择/投屏/控制/轮询）
internal/dlna/           # SSDP 发现、设备描述解析、AVTransport/RenderingControl SOAP 控制
internal/media/          # ffprobe 探测、ffmpeg 实时转码、局域网 HTTP 流服务
internal/netutil/        # 本机局域网地址探测
frontend/src/            # 原生 HTML/JS/CSS 界面
```

## 测试

```bash
go test ./...
# ffprobe 集成测试（需提供真实媒体文件）：
ANYDLNA_PROBE_SAMPLE=/path/to/video.mp4 go test ./internal/media/ -run TestProbeIntegration -v
```
