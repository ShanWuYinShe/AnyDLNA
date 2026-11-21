package media

import (
	"golang.org/x/text/encoding/simplifiedchinese"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"
)

func TestFindExternalSubtitle(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "my_movie.mp4")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. 无字幕时返回空串
	if got := FindExternalSubtitle(videoPath); got != "" {
		t.Errorf("无字幕时应返回空串，实际得到 %q", got)
	}

	// 2. 只有 en.srt 时返回 en.srt
	enSub := filepath.Join(dir, "my_movie.en.srt")
	if err := os.WriteFile(enSub, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindExternalSubtitle(videoPath); got != enSub {
		t.Errorf("期望匹配 %q，实际得到 %q", enSub, got)
	}

	// 3. 同时存在 chs.srt 时，优先返回中文字幕
	chsSub := filepath.Join(dir, "my_movie.chs.srt")
	if err := os.WriteFile(chsSub, []byte("1\n00:00:01,000 --> 00:00:02,000\n你好\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindExternalSubtitle(videoPath); got != chsSub {
		t.Errorf("优先匹配中文字幕，期望 %q，实际得到 %q", chsSub, got)
	}

	// 4. 测试子目录 subs/
	subDir := filepath.Join(dir, "subs_movie")
	if err := os.MkdirAll(filepath.Join(subDir, "subs"), 0o755); err != nil {
		t.Fatal(err)
	}
	subVideo := filepath.Join(subDir, "other.mkv")
	if err := os.WriteFile(subVideo, []byte("fake-video"), 0o644); err != nil {
		t.Fatal(err)
	}
	subInDir := filepath.Join(subDir, "subs", "other.srt")
	if err := os.WriteFile(subInDir, []byte("1\n00:00:01,000 --> 00:00:02,000\nSubs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindExternalSubtitle(subVideo); got != subInDir {
		t.Errorf("期望匹配子目录字幕 %q，实际得到 %q", subInDir, got)
	}
}

func TestExtractEmbeddedSubtitle(t *testing.T) {
	ctx := t.Context()
	// 1. 空路径应返回错误
	if _, err := ExtractEmbeddedSubtitle(ctx, ""); err == nil {
		t.Error("空路径应返回错误")
	}

	// 2. 不存在的文件应返回错误
	if _, err := ExtractEmbeddedSubtitle(ctx, "/path/to/non_existent.mkv"); err == nil {
		t.Error("不存在的文件应返回错误")
	}

	// 3. 无字幕流的空文件/伪文件应返回错误
	tmp := filepath.Join(t.TempDir(), "dummy.mp4")
	_ = os.WriteFile(tmp, []byte("not-a-valid-video"), 0o644)
	if _, err := ExtractEmbeddedSubtitle(ctx, tmp); err == nil {
		t.Error("无字幕流的文件应返回错误")
	}
}

// TestNormalizeSubtitleEncoding 验证 UTF-8 BOM 清洗与 GBK/GB18030 自动转码。
func TestNormalizeSubtitleEncoding(t *testing.T) {
	// 1. 验证带 UTF-8 BOM 字符清洗
	bomData := []byte("\xef\xbb\xbf1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	cleaned := NormalizeSubtitleEncoding(bomData)
	if string(cleaned) != "1\n00:00:01,000 --> 00:00:02,000\nHello\n" {
		t.Errorf("BOM 清洗不符合预期: %q", string(cleaned))
	}

	// 2. 验证 GBK 中文字符转码为 UTF-8
	encoder := simplifiedchinese.GB18030.NewEncoder()
	gbkBytes, err := encoder.Bytes([]byte("中文字幕测试"))
	if err != nil {
		t.Fatal(err)
	}
	if utf8.Valid(gbkBytes) {
		t.Fatal("GBK 字节不应是合法的 UTF-8")
	}

	utf8Result := NormalizeSubtitleEncoding(gbkBytes)
	if !utf8.Valid(utf8Result) {
		t.Errorf("转码后应为有效 UTF-8")
	}
	if string(utf8Result) != "中文字幕测试" {
		t.Errorf("GBK 转码文本不符: %s; 期望 %s", string(utf8Result), "中文字幕测试")
	}
}

// TestConvertASSToSRT 验证 ASS / SSA 特效字幕转为标准 SRT 文本。
func TestConvertASSToSRT(t *testing.T) {
	assContent := `[Script Info]
Title: Sample
ScriptType: v4.00+

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:01:20.12,0:01:23.45,Default,,0,0,0,,{\pos(100,200)}第一行\N第二行
Dialogue: 0,0:01:24.00,0:01:26.50,Default,,0,0,0,,{\c&H0000FF&}第二句普通字幕
`
	srtBytes := ConvertASSToSRT([]byte(assContent))
	if len(srtBytes) == 0 {
		t.Fatal("ASS 转 SRT 失败：输出为空")
	}

	expected := "1\n00:01:20,120 --> 00:01:23,450\n第一行\n第二行\n\n2\n00:01:24,000 --> 00:01:26,500\n第二句普通字幕\n\n"
	if string(srtBytes) != expected {
		t.Errorf("转换结果不符:\nGot:\n%s\nExpected:\n%s", string(srtBytes), expected)
	}
}

// TestFindBestSubtitleMapFromOutput 验证 ffmpeg 流元数据解析中文字幕轨。
func TestFindBestSubtitleMapFromOutput(t *testing.T) {
	// 场景 1：多字幕且第二路为中文 (chi)
	output1 := `
Input #0, matroska,webm, from 'test.mkv':
  Duration: 01:23:45.67, start: 0.000000, bitrate: 5432 kb/s
  Stream #0:0(eng): Video: h264 (High)
  Stream #0:1(eng): Audio: aac (LC)
  Stream #0:2(eng): Subtitle: subrip
    Metadata:
      title           : English
  Stream #0:3(chi): Subtitle: subrip
    Metadata:
      title           : Chinese
`
	if got := findBestSubtitleMapFromOutput(output1); got != "0:s:1" {
		t.Errorf("场景1 应匹配中文字幕 0:s:1，实际得到 %q", got)
	}

	// 场景 2：语言为 und，但标题包含「简体中文」
	output2 := `
Input #0, matroska,webm, from 'test.mkv':
  Stream #0:2(und): Subtitle: ass
    Metadata:
      title           : English (Forced)
  Stream #0:3(und): Subtitle: ass
    Metadata:
      title           : 简体中文字幕
`
	if got := findBestSubtitleMapFromOutput(output2); got != "0:s:1" {
		t.Errorf("场景2 应匹配标题中文 0:s:1，实际得到 %q", got)
	}

	// 场景 3：无中文字幕，回退到首条字幕
	output3 := `
Input #0, matroska,webm, from 'test.mkv':
  Stream #0:2(eng): Subtitle: subrip
  Stream #0:3(fre): Subtitle: subrip
`
	if got := findBestSubtitleMapFromOutput(output3); got != "0:s:0" {
		t.Errorf("场景3 无中文时应回退 0:s:0，实际得到 %q", got)
	}

	// 场景 4：无任何字幕流
	output4 := `
Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'test.mp4':
  Stream #0:0: Video: h264
  Stream #0:1: Audio: aac
`
	if got := findBestSubtitleMapFromOutput(output4); got != "" {
		t.Errorf("场景4 无字幕流应返回空串，实际得到 %q", got)
	}
}
