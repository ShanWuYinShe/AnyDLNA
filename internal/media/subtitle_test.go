package media

import (
	"os"
	"path/filepath"
	"testing"
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
