package media

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 应用诊断文件日志：GUI 程序的 stdout 用户看不见，关键链路的耗时与报错
// 必须落盘，出问题直接看文件，不用复现猜测。
//
// 位置：DataDir()/logs/anydlna.log（与 config.json 同一数据目录）；
// 轮转：超过 2MB 则改名为 .1（只留一份旧文件），都是投屏诊断用的近期日志。
// 未初始化（单测等）时 Diagf 退化为标准 log，保证任何环境都不丢信息。
const (
	diagLogName    = "anydlna.log"
	diagLogMaxSize = 2 << 20
)

var diagMu sync.Mutex
var diagFile *os.File

// DiagLogPath 返回诊断日志路径（初始化失败时返回空串与错误）。
func DiagLogPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", diagLogName), nil
}

// InitDiagLog 初始化文件日志：后续 Diagf 与标准 log 全部写入文件。
// 重复调用返回已初始化的路径。失败不阻塞启动（调用方记一笔即可）。
func InitDiagLog() (string, error) {
	return initDiagLogTo("")
}

// initDiagLogTo 供单测指定路径；空串表示用默认位置。
func initDiagLogTo(path string) (string, error) {
	diagMu.Lock()
	defer diagMu.Unlock()
	if diagFile != nil {
		return diagFile.Name(), nil
	}
	if path == "" {
		var err error
		path, err = DiagLogPath()
		if err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	rotateDiagLog(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	diagFile = f
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	return path, nil
}

// rotateDiagLog 在超限时把旧日志挪为 .1；调用方须持有 diagMu。
func rotateDiagLog(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < diagLogMaxSize {
		return
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}

// Diagf 写一条诊断日志（未初始化时走标准 log）。
func Diagf(format string, args ...any) {
	diagMu.Lock()
	f := diagFile
	diagMu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if f == nil {
		log.Printf("%s", msg)
		return
	}
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05.000"), msg)
}
