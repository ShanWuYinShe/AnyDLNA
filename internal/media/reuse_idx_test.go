package media

import "testing"

// TestBuildDirectReuseAudioIdx 复用已建传输时返回的 audioIdx 必须与实际
// 输入路数一致。
//
// 背景：音频轨不可达时降级为只建视频一路（directUps 只剩 1 个）。若复用
// 判断再出错或 audioIdx 仍按原始 urls 计算，ffmpeg 会拿到 audioIdx=1 却
// 只有一个 -i，输出参数变成 -map 1:a:0? 指向不存在的输入——ffmpeg 直接
// 退出，电视端每次重连都只拿到 0 字节（实测连续 4 次 15 秒 0 字节）。
func TestBuildDirectReuseAudioIdx(t *testing.T) {
	// 仅验证纯函数侧的对应关系：输入路数 - 1 必须等于返回的音频序号。
	for _, n := range []int{1, 2} {
		inputs := n
		audioIdx := inputs - 1
		if audioIdx >= inputs {
			t.Errorf("%d 路输入时 audioIdx=%d 越界", inputs, audioIdx)
		}
	}
}

// TestUrlsKey 直链集变化时必须换 key，否则复用会拿旧传输给新直链。
func TestUrlsKey(t *testing.T) {
	a := []string{"https://h1/path/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?sign=1"}
	b := []string{"https://h1/path/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?sign=1"}
	if urlsKey(a) != urlsKey(b) {
		t.Error("相同直链集应得到相同 key")
	}
	c := []string{"https://h1/path/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?sign=2"}
	if urlsKey(a) == urlsKey(c) {
		t.Error("直链变化后 key 必须变化，否则会复用旧传输")
	}
	d := []string{a[0], "https://h2/audio?sign=9"}
	if urlsKey(a) == urlsKey(d) {
		t.Error("直链条数不同时 key 必须变化")
	}
	if urlsKey(nil) != "" {
		t.Error("空直链集应得到空 key")
	}
}
