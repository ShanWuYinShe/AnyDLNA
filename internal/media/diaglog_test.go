package media

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiagLogWriteAndRotate 写入落盘、超限轮转、路径可用。
func TestDiagLogWriteAndRotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anydlna.log")
	got, err := initDiagLogTo(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("路径不对: %q", got)
	}
	// 用完即关并恢复，避免影响其他测试的标准 log 输出。
	defer func() {
		diagMu.Lock()
		if diagFile != nil {
			_ = diagFile.Close()
			diagFile = nil
		}
		diagMu.Unlock()
		log.SetOutput(os.Stderr)
	}()
	Diagf("投屏解析 url=%s", "https://example.com/v")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "投屏解析") {
		t.Fatalf("日志未落盘: %q", data)
	}

	// 造超限文件再初始化一次，应轮转为 .1。
	diagMu.Lock()
	_ = diagFile.Close()
	diagFile = nil
	diagMu.Unlock()
	big := make([]byte, diagLogMaxSize+1)
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := initDiagLogTo(path); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path + ".1"); err != nil || st.Size() != int64(len(big)) {
		t.Fatalf("轮转失败: %v", err)
	}
}

// TestDiagLogPath 数据目录下路径拼接正确。
func TestDiagLogPath(t *testing.T) {
	path, err := DiagLogPath()
	if err != nil {
		t.Skipf("无数据目录: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("logs", diagLogName)) {
		t.Fatalf("路径不对: %q", path)
	}
}
