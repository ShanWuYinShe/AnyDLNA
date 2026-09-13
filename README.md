# AnyDLNA

基于 Go + [Wails v2](https://wails.io) 的 DLNA 投屏应用：把**在线视频（YouTube、Bilibili 等）与本地任意格式视频**投到局域网内的电视 / 盒子上播放。

## 功能

- **设备发现**：SSDP 组播搜索局域网内的 DLNA MediaRenderer（电视、盒子），自动解析设备描述。
- **在线视频投屏**：粘贴视频页面/流地址，一次 `yt-dlp` 调用同时拿到元数据与直链；下载、并发、定位、代理全由 Go 原生传输层完成（并发 Range 拉取 + 稀疏缓存 + 本机 Range 服务，http/https 与 socks5 代理都支持），`ffmpeg` 只做合并/remux，经本机中转给电视——电视端不需要支持该网站，也不受 DASH 分离流限制。支持 YouTube、Bilibili 等数千个站点与 m3u8/mp4 直链（部分站点 CDN 拒绝非浏览器客户端，自动改由 yt-dlp 下载经管道中转；直播 m3u8 由 `ffmpeg` 直连）。
- **优先免转码**：投屏前先查询设备声明支持的格式，能直出就直出、能换封装就不转码，画质无损且几乎不占 CPU；源编码确实不被支持时才转码。详见「播放性能」。
- **本地视频**：`ffprobe` 探测编码；设备声明支持该格式且索引前置的 MP4 原文件直出（带 Range，支持拖动进度）；其他 H.264 内容换封装；HEVC/AV1/10-bit 等设备无法解码的格式才实时转码。
- **播放控制**：播放 / 暂停 / 停止、进度跳转（在线模式跳转时复用同一缓存、按需优先拉取跳转位置，无需重新解析；转码模式跳转时转码进程从新位置重启）、音量调节（RenderingControl）。
- **流服务**：应用内置 HTTP 服务监听局域网可达地址，电视端通过 `SetAVTransportURI` 拉取本机流。
- **设置页**：代理（跟随系统 / 手动 / 直连）与站点登录状态（读取本机浏览器 / 应用登录浏览器 / 不使用）集中管理，见下文。

## 环境依赖

- Go ≥ 1.25、[Wails CLI v2](https://wails.io/docs/gettingstarted/installation)（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）
- 发布版 `.app` 自带 pin 好版本的 `yt-dlp` / `ffmpeg` / `ffprobe` / `qjs`（见 `scripts/bundle-tools.sh`），用户侧零安装；用 `./scripts/build-app.sh` 一键构建自带版。
- 二次开发与跑单测仍需本机工具（macOS：`brew install ffmpeg yt-dlp quickjs`），因为 `go test` 直接调系统里的二进制。在线视频解析建议定期升级 `yt-dlp`（`brew upgrade yt-dlp`）以跟进各站点变化。
- 若使用「用应用登录浏览器」，需要本机安装 Chrome / Edge / Brave 等 Chromium 系浏览器之一（应用会自动检测；可用 `ANYDLNA_BROWSER_PATH` 指定可执行文件路径）。

### 为什么需要 yt-dlp，以及它如何被找到

在线视频解析依赖 yt-dlp，而它**不是可以用 Go 平替的依赖**：yt-dlp 的价值在于内置 1700 多个站点解析器（官方支持列表当前共 1731 条），并持续跟进各站点的反爬变化（YouTube 的签名解密、PO token、SABR 等）。Go 生态中的同类库覆盖面小得多（例如 `kkdai/youtube` 仅支持 YouTube，`iawia002/lux` 支持约 46 个站点），且需要自行跟进同样频繁的站点变更；[`lrstanley/go-ytdlp`](https://github.com/lrstanley/go-ytdlp) 则只是 yt-dlp 的 CLI 绑定，仍然需要该二进制。因此这里把 yt-dlp 当作外部解析引擎使用。

工具的查找方式（`yt-dlp` / `ffmpeg` / `ffprobe` / `qjs` 一致）：

1. 环境变量显式指定：`ANYDLNA_YTDLP_PATH` / `ANYDLNA_FFMPEG_PATH` / `ANYDLNA_FFPROBE_PATH`；
2. 应用自带的 `Contents/Resources/tools`（发布版；版本构建时 pin 好，行为确定，不随用户环境漂移）；
3. 系统 `PATH`；
4. 各平台常见安装目录——macOS 覆盖 Homebrew（`/opt/homebrew/bin`、`/usr/local/bin`）、MacPorts 与用户级目录，Linux 覆盖 `/usr/local/bin`、`/snap/bin`、Flatpak 与 `~/.local/bin`。

另外 yt-dlp 解 YouTube JS challenge 需要 JS 运行时：自带 `qjs`（2.6MB）并显式启用（`deno` 仍优先，有则行为不变），干净机器也能解。

第 3 步是必需的，而非锦上添花：**从 Finder / Dock 启动的 macOS 应用不继承 shell 的 PATH**（`launchctl` 默认也未设置），进程实际只有 `/usr/bin:/bin:/usr/sbin:/sbin`。只查 `PATH` 会让 Homebrew 安装的工具一律「未找到」——尽管它们在终端里完全可用。

同样地，应用会把补齐后的 `PATH` 传给子进程：**yt-dlp 合并分离的音视频流时会自行调用 ffmpeg**，若子进程沿用精简 PATH，即使应用找到了 yt-dlp，它也会因找不到 ffmpeg 而失败。

> 若提示「未找到某工具」，提示信息会列出已搜索的目录，并给出对应环境变量。请先确认不是路径问题，再考虑安装。

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

点击右上角「⚙ 设置」进入设置页。所有设置只作用于**在线视频的解析与拉流**；设备发现与投屏流始终走局域网直连，不受影响。设置持久化保存在系统用户配置目录下的 `AnyDLNA/config.json`（macOS：`~/Library/Application Support/AnyDLNA/`）。诊断日志在同目录的 `logs/anydlna.log`（解析耗时、直链条数、上游长度、投屏/跳转成败都在里面，出问题直接看它）。

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

## 播放性能

播放流畅度取决于**是否重编码视频**——这是整条链路唯一的性能瓶颈。应用会先询问设备支持什么，再在三种方式中选择最省的一种，界面上的提示会写明当前用的是哪种：

| 方式 | 触发条件 | 实测速度 | 画质 |
| --- | --- | --- | --- |
| **原文件直出**（direct） | 设备声明支持源文件格式，视频 H.264 + 音频 AAC（MP4 还须 faststart） | 无开销 | 无损，支持拖动进度 |
| **换封装**（remux） | 视频 H.264（8-bit），音频任意 | 约 **18–29 倍**实时 | **无损** |
| **实时转码**（transcode） | 视频为 HEVC/AV1/VP9/10-bit 等 | 1080p 约 2 倍、4K 约 1.4 倍 | 有损 |

### 与设备协商格式

DLNA 设备可通过标准的 `ConnectionManager` 服务声明自己能播放哪些格式。应用在投屏前会调用它的 `GetProtocolInfo` 取得 Sink 列表，据此决定：

- 设备**声明支持**源文件格式 → 直接投原文件（零处理，且支持拖动进度）；
- 设备只支持 **MP4** 而不支持 MPEG-TS → 换封装为碎片化 MP4 而非 TS；
- 设备**未提供**该服务或查询失败 → 回退到通用策略（H.264 换封装为 MPEG-TS），投屏不受影响。

选中设备后，其声明支持的格式会显示在设备面板下方，便于确认协商依据。

需要注意该列表的**可信度差异**：

- 规范实现会带上 `DLNA.ORG_PN` profile（如 `AVC_MP4_MP_HD_1080i_AAC`）与 `DLNA.ORG_OP` 参数，信息具体；
- 部分设备只给 MIME 通配（如 `http-get:*:video/mp4:*`），甚至同时声称支持 RMVB/DivX 等一长串格式。

这类列表只能作为**容器级**依据——它不包含编码信息。所以应用仍会检查视频编码：设备声称支持 MKV，不代表它能解 MKV 里的 HEVC。实际决策是「设备声明的容器能力」与「源文件真实编码」的结合：容器这层听设备的，编码这层仍按 H.264 / 8-bit 这一通用基线判断。

### 另外三个关键设计

- **优先挑选 H.264/AAC 源**。yt-dlp 默认会选 AV1/VP9 等压缩率更高的编码，但设备普遍无法解码，结果是每次都得完整转码。应用改用格式选择器优先取 `avc1` + `mp4a`，因此 B 站、YouTube 等站点通常都能走免转码路径（逐级回退，任何站点仍能选出可用格式）。
- **MP4 需要索引前置（faststart）才能边下边播**。若索引在文件末尾，播放器必须下载完整个文件才起播——实测这类文件投出去后设备长时间黑屏不出画面。应用会检查索引位置，未前置时改用换封装以立即起播。这不是设备协商能覆盖的问题：容器与编码都兼容，仅索引顺序不同。
- **10-bit H.264（Hi10P）仍会转码**。这类动漫常见格式虽名为 H.264，但绝大多数设备解不了，直通会导致黑屏，因此应用会探测像素格式并强制转码。

> 若播放仍不流畅，通常是网络带宽而非解码：4K 源本身码率可达 9 Mbps 以上，免转码虽不耗 CPU，但仍需把这些数据传到设备。这种情况可在设置页改用较低清晰度，或检查设备的 Wi-Fi 信号。

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
internal/dlna/           # SSDP 发现、设备描述解析、AVTransport/RenderingControl SOAP 控制、
                         # ConnectionManager 格式协商（GetProtocolInfo 解析与 MIME 归一化）
internal/media/          # 外部工具定位（含 GUI PATH 兼容）、ffprobe 探测（含 MP4 faststart 检测）、
                         # 输出方式决策（直出/换封装/转码）、yt-dlp 在线源解析、
                         # ffmpeg 实时处理、局域网 HTTP 流服务、
                         # 配置持久化、代理探测（分平台）、Netscape Cookies 文件读写
internal/browser/        # 纯 Go 的浏览器自动化（CDP）：启动独立 profile 的浏览器并读回 Cookies
internal/netutil/        # 本机局域网地址探测
frontend/src/            # 原生 HTML/JS/CSS 界面（投屏主页 + 设置页）
```

### 并发约定

`app.go` 中的两把锁职责严格区分，修改时务必遵守：

- `a.mu` 只保护内存状态，**临界区内绝不做网络、进程或等待用户的操作**；
- `app.castMu` 串行化投屏相关操作，耗时的 I/O（设备能力查询、yt-dlp 解析、设备 SOAP 调用、转码进程回收）在持有 `castMu`、但不持有 `a.mu` 的情况下执行。

违反该约定会导致 `a.mu` 被长时间占用，进而让**所有前端 IPC 挂起**（表现为「点了投屏没反应」），`app_test.go` 中的回归测试会捕获这类问题。

诊断日志请用 `a.logf`（标准库）而非 `runtime.Log*`：Wails 的 runtime 日志在上下文不是生命周期上下文时会 `log.Fatalf` 直接终止进程，诊断信息不该有这种后果。

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
# 设备格式协商集成测试（需局域网内有可访问的 DLNA 设备）：
ANYDLNA_TV_HOST=192.168.1.100 go test ./internal/dlna/ -run TestRealDeviceProtocolInfoIntegration -v
ANYDLNA_TV_HOST=192.168.1.100 go test . -run TestNegotiationEndToEnd -v
```
