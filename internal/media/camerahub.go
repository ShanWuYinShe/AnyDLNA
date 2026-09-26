// 摄像头常驻采集分发器：一路采集进程 + 多消费者实时分发。
//
// 为什么需要它：摄像头是独占设备，且电视端（DLNA 直播流）会周期性发起
// 并存连接——先探测后播放、播放中重连都属常态。若每次连接都启动一份采集
// 进程，并存连接会抢占设备，表现为电视端周期性卡顿加载。hub 模式下采集
// 进程常驻一份，电视端连接/断开/重连只是消费者增减，完全不触碰采集。
//
// 消费者语义（直播）：从加入时刻的实时尾部开始读；消费过慢时整块丢弃，
// 保证始终追得上实时——配合短 GOP（2 秒），丢帧后最多 2 秒即可重新出画面。
package media

import (
	"io"
	"os/exec"
	"sync"
)

// cameraHub 管理一路常驻采集进程与其消费者集合。
type cameraHub struct {
	src    *CameraSource
	plan   Plan
	stderr *limitBuffer

	mu     sync.Mutex
	subs   map[chan []byte]struct{}
	cmd    *exec.Cmd
	done   chan struct{} // 采集进程退出后关闭
	closed bool          // Stop 已调用：新订阅直接拿到关闭的 channel
}

// cameraOutputArgs 摄像头输出的编码参数：完整转码 + 直播短 GOP。
// libx264 默认 250 帧一个 I 帧（30fps 下约 8.3 秒），电视端缓冲重同步/
// 中途加入都要等 I 帧，表现为周期性卡顿加载；收紧到 2 秒，并启用
// zerolatency 关闭 B 帧与编码器内部缓冲，降低采集端延迟。
func cameraOutputArgs(plan Plan) []string {
	args := outputArgs(plan, 0)
	return append(args, "-g", "60", "-keyint_min", "60", "-tune", "zerolatency")
}

// newCameraHub 创建分发器（不启动）；调用 start 启动采集进程。
func newCameraHub(src *CameraSource, plan Plan) *cameraHub {
	return &cameraHub{src: src, plan: plan.orTranscode(), stderr: new(limitBuffer), subs: map[chan []byte]struct{}{}}
}

// start 启动常驻采集进程并开始分发。
func (h *cameraHub) start() error {
	cmd := toolCmd("ffmpeg", append(cameraInputArgs(h.src), cameraOutputArgs(h.plan)...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = h.stderr
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	h.cmd = cmd
	h.done = make(chan struct{})
	go func() {
		_ = h.cmd.Wait()
		close(h.done)
	}()
	go h.pump(stdout)
	return nil
}

// pump 把采集进程的 stdout 持续分发给全部消费者，直到流结束。
func (h *cameraHub) pump(stdout io.ReadCloser) {
	defer func() {
		h.mu.Lock()
		for ch := range h.subs {
			close(ch)
		}
		h.subs = map[chan []byte]struct{}{}
		h.mu.Unlock()
	}()
	buf := make([]byte, 16*1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			h.dispatch(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// dispatch 向每个消费者投递一块数据（拷贝后投递，慢消费者整块丢弃——
// 直播语义：宁可跳帧也不能让慢消费者拖住采集与快消费者）。
func (h *cameraHub) dispatch(p []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- append([]byte(nil), p...):
		default:
		}
	}
}

// subscribe 注册一个消费者。返回的 channel 在流结束或注销时关闭；
// 注销函数幂等，可在任意 goroutine 调用（如电视端断开时的清理）。
func (h *cameraHub) subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 256) // 约 4MB：调度抖动时的缓冲余量
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	once := sync.Once{}
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subs[ch]; ok {
				delete(h.subs, ch)
				close(ch)
			}
			h.mu.Unlock()
		})
	}
}

// finished 报告采集进程是否已退出（设备拔出、采集错误或 Stop）。
func (h *cameraHub) finished() bool {
	if h == nil {
		return true
	}
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// stop 终止常驻采集进程并清理全部消费者。
func (h *cameraHub) stop() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	killProcGroup(h.cmd)
	if h.done != nil {
		<-h.done
	}
}
