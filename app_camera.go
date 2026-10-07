package main

import (
	"AnyDLNA/internal/media"
	"AnyDLNA/internal/netutil"
	"context"
	"fmt"
	"time"
)

// ---------- 摄像头投屏 ----------

// ListCameras 列出本机可用的摄像头与麦克风（macOS avfoundation）。
// 首次调用会触发系统摄像头/麦克风权限弹窗，允许后才能采集。
func (a *App) ListCameras() ([]media.CameraDevice, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return media.ListCameras(ctx)
}

// CastCamera 把摄像头或屏幕画面（可选麦克风）经实时转码投到指定设备。
// screen 为真时采集屏幕而非摄像头（采集参数不同，见 media.cameraInputArgs）。
// 直播性质：不支持进度拖动；输出 720p30，完整转码 H.264/AAC。
// titleOverride 为本次投屏的临时显示名称（空串=按全局设置）。
func (a *App) CastCamera(udn, videoDevice, audioDevice string, screen bool, titleOverride string) (*CastStatus, error) {
	a.castMu.Lock()
	defer a.castMu.Unlock()

	dev, srv, err := a.castTarget(udn)
	if err != nil {
		return nil, err
	}
	if videoDevice == "" {
		return nil, fmt.Errorf("请先点「检测采集设备」并选择摄像头或屏幕")
	}
	if !media.HasFFmpeg() {
		return nil, fmt.Errorf("%w；摄像头采集需要它实时转码", media.MissingToolError("ffmpeg"))
	}

	caps := a.deviceCapabilities(dev)
	plan := media.PlanForCamera(caps)
	sourceName := "摄像头"
	if screen {
		sourceName = "屏幕"
	}
	title := a.effectiveCastTitle(sourceName, titleOverride)
	sessionID := srv.AddCamera(videoDevice, audioDevice, screen, title, plan)

	// 协商依据留痕：容器选择规则 = 设备声明支持 MPEG-TS 时优先 TS
	//（直播标准、封装开销最小、延迟最低），否则声明支持碎片化 MP4 时用它，
	// 都没有时仍回退 TS（绝大多数 DLNA 设备都能解）。
	a.logf("%s投屏 设备=%s %s 协商容器=%s（设备声明 %d 种视频格式）",
		sourceName, dev.FriendlyName, media.CameraLabel(videoDevice, audioDevice),
		plan.Container, len(caps.MIMEs))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ip, err := netutil.LANIP()
	if err != nil {
		srv.Remove(sessionID)
		return nil, fmt.Errorf("获取本机局域网地址失败: %w", err)
	}
	playURL := srv.URL(ip, sessionID, false)
	return a.startCast(dev, srv, sessionID, title, playURL, plan.OutputMIME(), string(plan.Mode), ctx)
}
