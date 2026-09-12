# AnyDLNA

基于 Go + [Wails v2](https://wails.io) 的 DLNA 投屏应用：把**在线视频（YouTube、Bilibili 等）与本地任意格式视频**投到局域网内的电视 / 盒子上播放。

## 功能

- **设备发现**：SSDP 组播搜索局域网内的 DLNA MediaRenderer（电视、盒子），自动解析设备描述。
- **在线视频投屏**：粘贴视频页面/流地址，`yt-dlp` 解析并拉取音视频流，`ffmpeg` 实时转码为 MPEG-TS 中转给电视——电视端不需要支持该网站，也不受 DASH 分离流、Referer/IP 校验限制。支持 YouTube、Bilibili 等数千个站点与 m3u8/mp4 直链。
- **本地视频**：`ffprobe` 探测编码；`H.264 + AAC` 的 MP4/MOV 原文件直出（带 Range，支持拖动进度）；其余格式（MKV、HEVC、AVI、FLV、RMVB……）自动实时转码。
- **播放控制**：播放 / 暂停 / 停止、进度跳转（转码/在线模式跳转时转码进程从新位置重启）、音量调节（RenderingControl）。
- **流服务**：应用内置 HTTP 服务监听局域网可达地址，电视端通过 `SetAVTransportURI` 拉取本机流。
- **设置页**：代理（跟随系统 / 手动 / 直连）与站点登录状态（读取本机浏览器 / 应用登录浏览器 / 不使用）集中管理，见下文。

## 环境依赖

- Go ≥ 1.25、[Wails CLI v2](https://wails.io/docs/gettingstarted/installation)（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）
- `ffmpeg` / `ffprobe`（macOS：`brew install ffmpeg`）。未安装时仅支持直出格式，其余格式会提示安装。
- `yt-dlp`（macOS：`brew install yt-dlp`）。在线视频投屏必需；建议定期升级（`brew upgrade yt-dlp`）以跟进各站点变化。
- 若使用「用应用登录浏览器」，需要本机安装 Chrome / Edge / Brave 等 Chromium 系浏览器之一（应用会自动检测；可用 `ANYDLNA_BROWSER_PATH` 指定可执行文件路径）。

### 跨平台说明

核心逻辑（DLNA 发现与控制、媒体探测转码、流服务、代理与 Cookies 管理）为**纯 Go 实现，无 cgo**，可在 macOS / Linux / Windows 上构建运行：

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...   # 交叉编译校验
```

系统代理读取按平台实现：macOS 用 `scutil --proxy`，Windows 读注册表 `Internet Settings`，Linux 读 GNOME `gsettings`；任何平台都同时支持 `HTTPS_PROXY` 等环境变量。

> 注：Wails 的图形界面在 macOS/Linux/Windows 各自需要对应平台的构建环境（WebKit / WebView2），这不属于 cgo 依赖。应用内置浏览器采用 CDP 驱动外部浏览器的方式，同样不引入 cgo。

## 运行与构建

```bash
wails dev     # 开发模式（热重载）
wails build   # 产出 build/bin/AnyDLNA.app（macOS）
```

## 使用

1. 打开应用后设备会**自动发现**：应用常驻监听电视的 DLNA 上线广播，电视上线即自动出现在列表（也可点「搜索设备」主动搜索）。需与电脑同一网络且电视开启 DLNA。
2. 选择视频来源：粘贴在线视频链接后点「解析」，或点「选择本地视频」。
3. 点击「投屏到选中设备」，底部出现播放控制条即可控制。

## 设置（代理与 Cookies）

点击右上角「⚙ 设置」进入设置页。所有设置只作用于**在线视频的解析与拉流**；设备发现与投屏流始终走局域网直连，不受影响。设置持久化保存在系统用户配置目录下的 `AnyDLNA/config.json`（macOS：`~/Library/Application Support/AnyDLNA/`）。

### 网络代理

| 模式 | 说明 |
| --- | --- |
| **跟随系统代理**（默认） | 交给 yt-dlp 读取系统代理与 `HTTPS_PROXY` 等环境变量。设置页会显示实际检测到的地址。规则分流（国内直连、国外走代理）的代理软件用这一项即可。 |
| **手动配置** | 显式填写代理地址，支持 `http://` 与 `socks5://`。 |
| **不使用代理** | 显式直连。注意：yt-dlp 默认会自行读取环境变量与系统代理，因此本模式会显式传空代理来确保真正直连。 |

### 站点登录状态（Cookies）

YouTube 等站点常要求人机验证（"Sign in to confirm you're not a bot"），需要提供已登录的 Cookies。

| 模式 | 说明 |
| --- | --- |
| **读取本机浏览器**（推荐） | 直接读取你日常浏览器中已登录的状态，**无需任何额外操作**。支持 `chrome` / `edge` / `firefox` / `safari` / `brave` / `chromium` / `opera` / `vivaldi` / `whale`。 |
| **用应用登录浏览器** | 打开一个**独立 profile** 的浏览器窗口供你登录，再把登录状态交给应用。适合日常浏览器不是 Chromium 系、或希望把登录态与应用隔离的场景。 |
| **不使用 Cookies** | 仅访问无需登录的公开内容。 |

关于两种登录方式的取舍，有几点基于实测的说明：

- **「读取本机浏览器」不做数据复制**，由 yt-dlp 直接读取浏览器自身的 Cookie 存储并解密，因此始终与你浏览器里的登录状态一致。首次在 macOS 上读取 Chrome 时系统会弹出钥匙串授权，请点「允许」。
- **「用应用登录浏览器」必须使用独立 profile（技术限制，非实现选择）**：Chrome 136 起，`--remote-debugging-port` 在默认数据目录下会被忽略，官方推荐自动化场景使用独立数据目录（见 [Chrome 官方说明](https://developer.chrome.com/blog/remote-debugging-port)）；此外同一 profile 不能被两个进程同时打开，共享 profile 将要求你先完全退出日常浏览器。因此这里刻意不共用日常 profile。
- **复制日常 profile 的做法不可行**：Chrome 的 App-Bound Encryption 把 Cookie 密钥绑定在原 profile 上，复制到别处后登录态无法解密（实测复制后仅能读到匿名 Cookie）。这也是「读取本机浏览器」由 yt-dlp 原地读取、而非应用先复制的原因。

> 注意：在线视频经本机实时转码，清晰度默认为 yt-dlp 所选最佳格式，转码码率上限 4 Mbps。

## 搜索不到设备？

应用有四层发现机制：常驻监听电视广播（自动发现）、每个网卡接口上组播 M-SEARCH（多轮、多种目标类型）、搜索与已发现设备合并展示、手动添加兜底。若仍然搜不到：

1. **手动添加（最直接）**：在设备面板输入电视 IP（电视设置里可查，如 `192.168.1.100`）点「手动添加」，应用会直接拉取其设备描述，完全绕过 SSDP。
2. 确认电视的 **DLNA / 多屏互动** 开关已打开——它与 AirPlay/Chromecast 是相互独立的服务，AirPlay 能用不代表 DLNA 开着。
3. 电视**息屏待机后很多型号会关闭 DLNA 服务**，请点亮电视后重试。
4. 检查路由器是否开启了 **AP 隔离 / 客户端隔离**（会阻断设备互访），可尝试关闭。

## 代码结构

```
main.go                  # Wails 入口
app.go                   # 绑定给前端的业务层（搜索/选择/解析/投屏/控制/轮询/设置）
internal/dlna/           # SSDP 发现、设备描述解析、AVTransport/RenderingControl SOAP 控制
internal/media/          # ffprobe 探测、yt-dlp 在线源解析、ffmpeg 实时转码、局域网 HTTP 流服务、
                         # 配置持久化、代理探测（分平台）、Netscape Cookies 文件读写
internal/browser/        # 纯 Go 的浏览器自动化（CDP）：启动独立 profile 的浏览器并读回 Cookies
internal/netutil/        # 本机局域网地址探测
frontend/src/            # 原生 HTML/JS/CSS 界面（投屏主页 + 设置页）
```

### 并发约定

`app.go` 中的两把锁职责严格区分，修改时务必遵守：

- `a.mu` 只保护内存状态，**临界区内绝不做网络、进程或等待用户的操作**；
- `app.castMu` 串行化投屏相关操作，耗时的 I/O（yt-dlp 解析、设备 SOAP 调用、转码进程回收）在持有 `castMu`、但不持有 `a.mu` 的情况下执行。

违反该约定会导致 `a.mu` 被长时间占用，进而让**所有前端 IPC 挂起**（表现为「点了投屏没反应」），`app_test.go` 中的回归测试会捕获这类问题。

## 测试

```bash
go test ./...
# 竞态检测（推荐在改并发相关代码后执行）：
go test -race ./...
# ffprobe 集成测试（需提供真实媒体文件）：
ANYDLNA_PROBE_SAMPLE=/path/to/video.mp4 go test ./internal/media/ -run TestProbeIntegration -v
# 在线流全链路集成测试（本地 HTTP → yt-dlp → ffmpeg → MPEG-TS，需安装 yt-dlp/ffmpeg）：
ANYDLNA_STREAM_ITEST=1 ANYDLNA_STREAM_SAMPLE_DIR=/目录 go test ./internal/media/ -run TestURLStreamIntegration -v
# 浏览器 Cookie 集成测试（会真实启动一个浏览器进程，使用临时 profile）：
ANYDLNA_BROWSER_ITEST=1 go test ./internal/browser/ -run TestManagerCookiesIntegration -v
```
