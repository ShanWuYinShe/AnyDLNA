// Command faketv 启动一台假电视，用于无实体电视时试用 AnyDLNA 投屏：
//
//	go run ./cmd/faketv [-o 录制文件]
//
// 启动后：打开 AnyDLNA 应用 → 搜索设备 → 投到 FakeTV；
// 收到 Play 即弹 ffplay 窗口实时播放（有画面有声音，延迟数秒）。
// -o 指定录制文件则同步保存收到的流（调试用）。Ctrl-C 退出。
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"AnyDLNA/internal/faketv"
)

func main() {
	record := flag.String("o", "", "录制文件路径（收到的流同步写入，调试用）")
	flag.Parse()

	tv, err := faketv.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "启动假电视失败:", err)
		os.Exit(1)
	}
	defer tv.Close()
	if err := tv.EnablePlayer(); err != nil {
		fmt.Fprintln(os.Stderr, "注意:", err, "——只做拉流审计，不弹窗。")
	} else {
		fmt.Println("实时播放窗口已启用（收到 Play 即弹 ffplay）。")
	}
	if *record != "" {
		tv.SetRecordPath(*record)
		fmt.Println("录制文件:", *record)
	}

	fmt.Println("假电视已上线，名称 FakeTV，描述地址:", tv.DescriptionURL())
	fmt.Println("用法：打开 AnyDLNA 应用 → 搜索设备 → 投到 FakeTV。Ctrl-C 退出。")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			fmt.Println("\n退出。")
			return
		case <-tick.C:
			n, packets, errs := tv.Stats()
			fmt.Printf("[%s] 状态=%s 地址=%.60s 字节=%d 包=%d 错误=%d\n",
				time.Now().Format("15:04:05"), tv.State(), tv.URI(), n, packets, errs)
		}
	}
}
