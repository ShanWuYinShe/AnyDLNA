package media

import "testing"

func TestNeedsTranscode(t *testing.T) {
	cases := []struct {
		name string
		info Info
		want bool
	}{
		{"MP4 H264 AAC 直出", Info{Container: "mov,mp4,m4a,3gp,3g2,mj2", VideoCodec: "h264", AudioCodec: "aac"}, false},
		{"MOV H264 AAC 直出", Info{Container: "mov", VideoCodec: "h264", AudioCodec: "aac"}, false},
		{"MKV 需转码", Info{Container: "matroska,webm", VideoCodec: "h264", AudioCodec: "aac"}, true},
		{"HEVC 需转码", Info{Container: "mov,mp4,m4a,3gp,3g2,mj2", VideoCodec: "hevc", AudioCodec: "aac"}, true},
		{"MP3 音频需转码", Info{Container: "mov,mp4,m4a,3gp,3g2,mj2", VideoCodec: "h264", AudioCodec: "mp3"}, true},
		{"AVI 需转码", Info{Container: "avi", VideoCodec: "mpeg4", AudioCodec: "mp3"}, true},
		{"无声视频需转码（保险）", Info{Container: "mov,mp4,m4a,3gp,3g2,mj2", VideoCodec: "h264", AudioCodec: ""}, true},
	}
	for _, c := range cases {
		if got := c.info.NeedsTranscode(); got != c.want {
			t.Errorf("%s: NeedsTranscode() = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

func TestMimeTypeFor(t *testing.T) {
	cases := map[string]string{
		"/a/movie.mp4": "video/mp4",
		"/a/movie.m4v": "video/mp4",
		"/a/movie.mov": "video/quicktime",
		"/a/movie.mkv": "video/x-matroska",
		"/a/movie.ts":  "video/mp2t",
		"/a/未知.xyz":    "application/octet-stream",
	}
	for path, want := range cases {
		if got := MimeTypeFor(path); got != want {
			t.Errorf("MimeTypeFor(%q) = %q, 期望 %q", path, got, want)
		}
	}
}
