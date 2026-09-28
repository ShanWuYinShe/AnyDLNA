// 摄像头常驻采集分发器：一路采集进程 + 定长环形缓冲 + 多消费者游标读取。
//
// 为什么需要它：摄像头是独占设备，且电视端（DLNA 直播流）会周期性发起
// 并存连接——先探测后播放、播放中重连都属常态。若每次连接都启动一份采集
// 进程，并存连接会抢占设备，表现为电视端周期性卡顿加载。hub 模式下采集
// 进程常驻一份，电视端连接/断开/重连只是消费者增减，完全不触碰采集。
//
// 关键语义（对 MPEG-TS 流绝不能丢中间数据——电视解码器遇到连续性断裂会
// 强制重新缓冲，表现为周期性加载转圈）：
//   - 采集 pump 独立读取 stdout 写入环形缓冲，永不被慢消费者反压阻塞
//     （avfoundation 是实时源，ffmpeg 在下游受阻时自行丢输入帧，画面帧率
//     短暂下降但流数据连续）；
//   - 消费者用绝对位置游标读取；落后超过缓冲容量（约 20 秒）说明已无法
//     跟上实时，返回 cameraErrLagged 让电视端断开重连——重连后最多等一个
//     GOP（2 秒）即可恢复，远好于喂一段不连续的坏流导致解码失败。
package media

import (
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// cameraRingBytes 是环形缓冲容量。按最高 4Mbps 码率约可容纳 16 秒，
// 覆盖 Wi-Fi 抖动与电视端短暂停止读取的常见区间。
const cameraRingBytes = 8 << 20

// cameraErrLagged 是消费者追不上实时（游标被环形缓冲覆盖）时，
// read 返回该错误；消费者应结束本次连接，电视端会自动重连恢复。
var cameraErrLagged = errors.New("摄像头消费者落后于实时流")

// cameraOutputArgs 摄像头输出的编码与封装参数：完整转码 + 直播短 GOP。
// 刻意不用 -tune zerolatency：它禁用 lookahead 让 x264 逐帧即时输出，
// IDR 突发会直落电视端，实测部分电视解码器在每个 IDR 处都会顿挫
// （表现为约每 2 秒一次卡顿）。默认 lookahead（约 40 帧）会把输出平滑掉，
// 代价是约 +1 秒延迟——对「电视上监看摄像头」场景，平滑远比低延迟重要。
// 刻意不用 -muxrate（CBR 恒定码率填充）：CBR 是 UDP 组播/IPTV 的做法，
// 对 HTTP 单播拉流会用无意义的 null 包把码率硬填到设定值（约为真实数据
// 的 5 倍），电视端播放缓冲被填充垃圾迅速塞满后判停断开——实测表现为
// 电视端周期性（约 25 秒）主动断开重连。HTTP 单播由 TCP 自带流控，
// VBR 真实码率即可；全部编码参数必须在 containerArgs（以输出文件名
// 结尾）之前给出。
func cameraOutputArgs(plan Plan) []string {
	args := codecArgs(plan, 0)
	args = append(args, "-g", "60", "-keyint_min", "60")
	return append(args, containerArgs(plan.Container)...)
}

// cameraHub 管理一路常驻采集进程与环形缓冲。
type cameraHub struct {
	src    *CameraSource
	plan   Plan
	stderr *limitBuffer

	mu     sync.Mutex
	cond   *sync.Cond // head 前进或 closed 变化时广播
	ring   []byte     // 定长环形缓冲
	head   int64      // 累计写入字节数（绝对位置，单调递增）
	closed bool
	cmd    *exec.Cmd
	done   chan struct{} // 采集进程退出后关闭
}

// newCameraHub 创建分发器（不启动）；调用 start 启动采集进程。
func newCameraHub(src *CameraSource, plan Plan) *cameraHub {
	h := &cameraHub{src: src, plan: plan.orTranscode(), stderr: new(limitBuffer), ring: make([]byte, cameraRingBytes)}
	h.cond = sync.NewCond(&h.mu)
	return h
}

// start 启动常驻采集进程并开始填充环形缓冲。
func (h *cameraHub) start() error {
	args := append(cameraInputArgs(h.src), cameraOutputArgs(h.plan)...)
	Diagf("摄像头采集命令: ffmpeg %s", strings.Join(args, " "))
	cmd := toolCmd("ffmpeg", args...)
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

// pump 把采集进程的 stdout 持续写入环形缓冲（只保留尾部），直到流结束。
// 写入永不阻塞：落后超过容量的消费者由其游标读取时感知 lagged。
func (h *cameraHub) pump(stdout io.ReadCloser) {
	buf := make([]byte, 32*1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			h.write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// write 把一块数据追加进环形缓冲并唤醒等待中的消费者。
// 单块不会超过缓冲容量（pump 每次最多 32KB），copy 必然完整写入。
func (h *cameraHub) write(p []byte) {
	h.mu.Lock()
	start := int(h.head % int64(len(h.ring)))
	n1 := copy(h.ring[start:], p)
	copy(h.ring[:len(p)-n1], p[n1:])
	h.head += int64(len(p))
	h.cond.Broadcast()
	h.mu.Unlock()
}

// read 从 pos（绝对位置）读取数据到 p。返回本次读取字节数与新位置。
//   - pos+len(p) <= head：正常读取（环形分段拷贝）；
//   - pos < head-cap：游标已被覆盖（落后太多），返回 cameraErrLagged；
//   - pos == head：无新数据，阻塞等待（或 closed/采集退出）。
func (h *cameraHub) read(pos int64, p []byte) (int, int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for {
		if h.closed {
			return 0, pos, io.ErrClosedPipe
		}
		oldest := h.head - int64(len(h.ring))
		if oldest < 0 {
			oldest = 0
		}
		if pos < oldest {
			return 0, pos, cameraErrLagged
		}
		if pos < h.head {
			start := int(pos % int64(len(h.ring)))
			avail := h.head - pos
			if int64(len(p)) > avail {
				p = p[:avail]
			}
			n := copy(p, h.ring[start:])
			if n < len(p) {
				n += copy(p[n:], h.ring[:len(p)-n])
			}
			return n, pos + int64(n), nil
		}
		// 无新数据：等写入或关闭。
		h.cond.Wait()
	}
}

// stop 终止常驻采集进程并唤醒全部等待中的消费者。
func (h *cameraHub) stop() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	h.cond.Broadcast()
	h.mu.Unlock()
	killProcGroup(h.cmd)
	if h.done != nil {
		<-h.done
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

// streamTo 从当前实时尾部开始持续读取环形缓冲并写入 w，直到电视端断开
// （Write 错误）、hub 关闭或落后太多（cameraErrLagged，电视端会自动重连，
// 重连后从新的实时尾部开始，最多等一个 GOP 即可恢复画面）。
func (h *cameraHub) streamTo(w io.Writer) error {
	h.mu.Lock()
	pos := h.head
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return io.ErrClosedPipe
	}

	buf := make([]byte, 64*1024)
	for {
		n, npos, err := h.read(pos, buf)
		if err != nil {
			return err
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return err
		}
		pos = npos
	}
}
