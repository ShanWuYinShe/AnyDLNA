package main

import (
	"AnyDLNA/internal/browser"
	"AnyDLNA/internal/dlna"
	"AnyDLNA/internal/media"
	"context"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"sync"
	"time"
)

// 本文件是应用层核心：App 结构、Wails 生命周期与锁纪律。
// 其余绑定方法按职责拆分：设备发现见 app_devices.go，投屏与播放控制见
// app_cast.go，设置与登录浏览器见 app_settings.go，摄像头/屏幕采集见 app_camera.go。

// logf 输出应用诊断日志。
//
// 刻意使用标准库而非 Wails runtime：runtime.Log* 在传入的上下文不是
// Wails 生命周期上下文时会直接 log.Fatalf 终止进程。诊断信息不该有这种
// 后果——例如在测试或任何非 Wails 环境下调用时，应用不应因此退出。
// 生命周期钩子（startup/shutdown）里拿到的 ctx 是真实上下文，
// 那两处仍用 runtime 日志以便进入 Wails 的应用日志。
func (a *App) logf(format string, args ...any) { media.Diagf(format, args...) }

// App 是绑定给前端的应用层。
//
// 锁纪律（避免再次出现「点了没反应」的死锁）：
//   - a.mu 只保护内存状态的读写，临界区内绝不做网络、进程或等待用户的操作；
//   - a.castMu 串行化投屏相关操作（投屏/停止/跳转），耗时 I/O 在持有
//     castMu 但不持有 a.mu 的情况下执行；
//   - a.searchMu 串行化主动搜索，避免定时搜索与手动搜索同时占用网络。
type App struct {
	ctx           context.Context
	streamSrv     *media.StreamServer
	watcherCancel context.CancelFunc
	browserMgr    *browser.Manager

	castMu   sync.Mutex // 串行化投屏操作，长耗时步骤不持有 a.mu
	searchMu sync.Mutex // 串行化主动搜索，长耗时步骤不持有 a.mu

	mu         sync.Mutex
	cfg        media.Config   // 在线视频代理与 Cookies 配置
	devices    []*dlna.Device // 已发现的渲染设备（搜索结果 + 被动监听累积）
	deviceMiss map[string]int // UDN → 连续未应答搜索的轮数（离线判定用）
	renderer   *dlna.Renderer // 当前投屏目标
	sessionID  string         // 当前流会话 ID
	castFile   string
	castMode   string
}

// controlTimeout 是播放控制与音量操作的单次超时；pollTimeout 是进度轮询的
// 单次超时（轮询频率 1 秒，超时必须明显短于间隔才不会堆积）。
const (
	controlTimeout = 8 * time.Second
	pollTimeout    = 5 * time.Second
)

// NewApp 创建应用实例。
func NewApp() *App {
	return &App{cfg: media.DefaultConfig(), browserMgr: browser.NewManager(""), deviceMiss: map[string]int{}}
}

// startup 在应用启动时创建流服务、加载配置并开启设备广播常驻监听。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.mu.Lock()
	a.cfg = media.LoadConfig()
	a.mu.Unlock()

	// 文件日志先行：GUI 的 stdout 用户看不见，之后所有 logf/标准 log
	// 都落到数据目录的日志文件，出问题直接看文件。
	if path, err := media.InitDiagLog(); err != nil {
		runtime.LogWarningf(ctx, "诊断日志初始化失败（仅影响落盘）: %v", err)
	} else {
		runtime.LogInfof(ctx, "诊断日志: %s", path)
	}

	// 启动自检：记录外部工具的实际解析结果。GUI 应用不继承 shell 的 PATH，
	// 工具「装了却找不到」是最容易误判的一类问题，这里留下可核对的依据。
	a.logf("外部工具: %s", media.ToolStatus())

	// 清理上次异常退出残留的上游缓存临时文件（正常路径在投屏结束时删除）。
	media.CleanUpstreamTemps()

	srv, err := media.NewStreamServer()
	if err != nil {
		runtime.LogErrorf(ctx, "启动流服务失败: %v", err)
		return
	}
	a.streamSrv = srv

	watchCtx, cancel := context.WithCancel(context.Background())
	a.watcherCancel = cancel

	// 被动监听：设备主动广播 SSDP alive 时立刻出现。
	if err := dlna.WatchRenderers(watchCtx, func(dev *dlna.Device) {
		a.mergeDevices([]*dlna.Device{dev}, true)
	}); err != nil {
		runtime.LogWarningf(ctx, "设备广播监听未启动（不影响定时与手动搜索）: %v", err)
	}

	// 定时主动搜索：不少电视（含实测的目标设备）平时不广播、只应答搜索，
	// 仅靠被动监听无法在设备开机后自动发现，因此需要周期性主动搜索兜底。
	go a.watchDevices(watchCtx)

	// 尽力恢复上次投屏的控制（按持久化的设备描述地址重新拉取，网络 I/O 不阻塞启动）。
	go a.restoreCast()
}

// shutdown 在应用退出时清理流服务、监听、浏览器与转码进程。
func (a *App) shutdown(ctx context.Context) {
	if a.watcherCancel != nil {
		a.watcherCancel()
	}
	if a.browserMgr != nil {
		a.browserMgr.Close()
	}
	if a.streamSrv != nil {
		a.streamSrv.Close()
	}
}
