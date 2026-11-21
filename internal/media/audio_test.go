package media

import "testing"

func TestFindBestAudioMapFromOutput(t *testing.T) {
	// 场景 1：多音轨且第二路为中文 (chi)
	output1 := `
Input #0, matroska,webm, from 'movie.mkv':
  Stream #0:0(eng): Video: h264
  Stream #0:1(eng): Audio: dts, 48000 Hz, 5.1
  Stream #0:2(chi): Audio: ac3, 48000 Hz, stereo
`
	if got := findBestAudioMapFromOutput(output1); got != 1 {
		t.Errorf("场景1 应匹配中文音轨 1，实际得到 %d", got)
	}

	// 场景 2：语言为 und，但标题包含「国语配音」
	output2 := `
Input #0, matroska,webm, from 'movie.mkv':
  Stream #0:1(und): Audio: aac
    Metadata:
      title           : English
  Stream #0:2(und): Audio: aac
    Metadata:
      title           : 国语普通话配音
`
	if got := findBestAudioMapFromOutput(output2); got != 1 {
		t.Errorf("场景2 应匹配标题中文音轨 1，实际得到 %d", got)
	}

	// 场景 3：无中文音轨，回退首路音频
	output3 := `
Input #0, matroska,webm, from 'movie.mkv':
  Stream #0:1(eng): Audio: dts
  Stream #0:2(jpn): Audio: aac
`
	if got := findBestAudioMapFromOutput(output3); got != 0 {
		t.Errorf("场景3 无中文应回退 0，实际得到 %d", got)
	}

	// 场景 4：无音频流
	output4 := `
Input #0, mov,mp4, from 'silent.mp4':
  Stream #0:0: Video: h264
`
	if got := findBestAudioMapFromOutput(output4); got != 0 {
		t.Errorf("场景4 无音频应返回 0，实际得到 %d", got)
	}
}
