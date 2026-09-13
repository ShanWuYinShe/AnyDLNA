package media

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 合成 MP4 ----------

// mp4box 拼一个盒子。
func mp4box(typ string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], body)
	return out
}

func u16(v int) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, uint16(v)); return b }
func u32(v int) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, uint32(v)); return b }

// visualEntry 拼视频 sample entry（定长头 78 字节 + 子盒子）。
func visualEntry(fourcc string, w, h int, kids ...[]byte) []byte {
	head := make([]byte, 78)
	binary.BigEndian.PutUint16(head[24:26], uint16(w))
	binary.BigEndian.PutUint16(head[26:28], uint16(h))
	return mp4box(fourcc, append([][]byte{head}, kids...)...)
}

// audioEntryV0 拼音频 sample entry（定长头 28 字节 + 子盒子，参考 ISO MP4 v0）。
func audioEntryV0(fourcc string, kids ...[]byte) []byte {
	head := make([]byte, 28)
	return mp4box(fourcc, append([][]byte{head}, kids...)...)
}

// esdsBox 拼指定对象类型的 esds。
func esdsBox(objType byte) []byte {
	body := []byte{0, 0, 0, 0, 0x03, 0x19, 0, 1, 0, 0x04, 0x11, objType}
	return mp4box("esds", body)
}

// avcCBox 拼指定 profile 的 avcC。
func avcCBox(profile byte) []byte {
	return mp4box("avcC", []byte{1, profile, 0, 0, 0xFF, 0xE1, 0, 4, 0, 0, 0, 1})
}

// mdiaBox 拼媒体盒子。
func mdiaBox(handler string, timescale, duration uint32, stsdKids ...[]byte) []byte {
	mdhd := mp4box("mdhd", append(append([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		u32(int(timescale))...), append(u32(int(duration)), 0x55, 0xC4, 0, 0)...))
	hdlr := mp4box("hdlr", append([]byte{0, 0, 0, 0, 0, 0, 0, 0}, handler...))
	stsd := mp4box("stsd", append([][]byte{{0, 0, 0, 0, 0, 0, 0, 1}}, stsdKids...)...)
	stbl := mp4box("stbl", stsd)
	minf := mp4box("minf", stbl)
	return mp4box("mdia", mdhd, hdlr, minf)
}

// synthMP4 拼最小测试文件：mvhd 15 秒 + H264(High)视频轨 + AAC 音频轨。
func synthMP4() []byte {
	ftyp := mp4box("ftyp", []byte("isom"))
	mvhd := mp4box("mvhd", append(append([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, u32(1000)...), append(u32(15000), 0, 1, 0, 0)...))
	video := mp4box("trak", mdiaBox("vide", 30000, 450000,
		visualEntry("avc1", 640, 360, avcCBox(100))))
	audio := mp4box("trak", mdiaBox("soun", 44100, 661500,
		audioEntryV0("mp4a", esdsBox(0x40))))
	moov := mp4box("moov", mvhd, video, audio)
	return append(ftyp, moov...)
}

// TestGoProbeSynthMP4 合成文件解析正确。
func TestGoProbeSynthMP4(t *testing.T) {
	info := &Info{}
	f, err := os.CreateTemp("", "probe-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(synthMP4()); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, err := goProbeFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	_ = info
	if got.VideoCodec != "h264" || got.Width != 640 || got.Height != 360 {
		t.Errorf("视频不对: %+v", got)
	}
	if got.AudioCodec != "aac" {
		t.Errorf("音频不对: %+v", got)
	}
	if got.DurationSec != 15 {
		t.Errorf("时长不对: %v", got.DurationSec)
	}
	if got.PixFmt != "yuv420p" {
		t.Errorf("像素格式不对: %q", got.PixFmt)
	}
	if got.Container == "" || got.Title == "" || got.SizeBytes == 0 {
		t.Errorf("基础字段缺失: %+v", got)
	}
}

// TestGoProbeHi10P High profile 应标 10-bit（走转码）。
func TestGoProbeHi10P(t *testing.T) {
	data := synthMP4()
	// 把 avcC profile 改成 110（High10）。
	if i := bytes.Index(data, []byte("avcC")); i < 0 {
		t.Fatal("合成文件缺 avcC")
	} else {
		data[i+5] = 110
	}
	f, _ := os.CreateTemp("", "probe-hi10-*.mp4")
	defer os.Remove(f.Name())
	f.Write(data)
	f.Close()
	got, err := goProbeFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got.PixFmt != "yuv420p10le" {
		t.Fatalf("应标 10-bit: %+v", got)
	}
	if plan := PlanForLocal(got, DeviceCapabilities{}); plan.Mode != OutputTranscode {
		t.Errorf("Hi10P 必须转码: %+v", plan)
	}
}

// TestGoProbeMP3InMP4 mp4a 套 MP3 对象类型应识别为 mp3（不能直通 TS）。
func TestGoProbeMP3InMP4(t *testing.T) {
	data := synthMP4()
	if i := bytes.Index(data, []byte("esds")); i < 0 {
		t.Fatal("合成文件缺 esds")
	} else {
		// esds 载荷内找 0x04 描述符后的对象类型字节改成 0x69。
		j := bytes.Index(data[i:], []byte{0x04})
		data[i+j+2] = 0x69
	}
	f, _ := os.CreateTemp("", "probe-mp3-*.mp4")
	defer os.Remove(f.Name())
	f.Write(data)
	f.Close()
	got, err := goProbeFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got.AudioCodec != "mp3" {
		t.Fatalf("应识别 mp3: %+v", got)
	}
}

// ---------- 合成 MKV ----------

// ebmlID 写元素 ID（含长度标记位）。
func ebmlID(v uint64) []byte {
	switch {
	case v < 0x80:
		return nil // 非法 ID，测试不应构造
	case v < 0x100:
		return []byte{byte(v)}
	case v < 0x4000:
		return nil
	case v < 0x8000:
		return []byte{byte(v >> 8), byte(v)}
	case v < 0x200000:
		return nil
	case v < 0x400000:
		return []byte{byte(v >> 16), byte(v >> 8), byte(v)}
	case v < 0x10000000:
		return nil
	case v < 0x20000000:
		return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	default:
		return nil
	}
}

// ebmlSize 写数据长度（1~8 字节 VINT）。
func ebmlSize(n int64) []byte {
	switch {
	case n < 0x7F:
		return []byte{0x80 | byte(n)}
	case n < 0x3FFF:
		return []byte{0x40 | byte(n>>8), byte(n)}
	default:
		return []byte{0x20 | byte(n>>24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

// ebml 拼元素。
func ebml(id uint64, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	out := append(ebmlID(id), ebmlSize(int64(len(body)))...)
	return append(out, body...)
}

func ebmlUint(id uint64, v uint64) []byte {
	var b [8]byte
	n := 8
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
		if v == 0 {
			n = i
			break
		}
	}
	if n == 8 {
		n = 7
	}
	return ebml(id, b[n:])
}

func ebmlFloat64(id uint64, v float64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, math.Float64bits(v))
	return ebml(id, b)
}

func ebmlString(id uint64, s string) []byte {
	return ebml(id, []byte(s))
}

// synthMKV 拼最小测试文件：15 秒 + H264(High10)视频 640x360 + AAC 音频。
func synthMKV() []byte {
	head := ebml(0x1A45DFA3, ebmlString(0x4282, "matroska"))
	avcC := []byte{1, 110, 0, 0}
	videoEntry := ebml(0xAE,
		ebmlUint(0xD7, 1),
		ebmlUint(0x83, 1),
		ebmlString(0x86, "V_MPEG4/ISO/AVC"),
		ebml(0x63A2, avcC),
		ebml(0xE0, ebmlUint(0xB0, 640), ebmlUint(0xBA, 360)),
	)
	audioEntry := ebml(0xAE,
		ebmlUint(0xD7, 2),
		ebmlUint(0x83, 2),
		ebmlString(0x86, "A_AAC"),
	)
	seg := ebml(0x18538067,
		// Duration 单位是 TimestampScale（此处 1ms）：15000 即 15 秒。
		ebml(0x1549A966, ebmlUint(0x2AD7B1, 1000000), ebmlFloat64(0x4489, 15000.0)),
		ebml(0x1654AE6B, videoEntry, audioEntry),
	)
	return append(head, seg...)
}

// TestGoProbeSynthMKV 合成 MKV 解析正确（含 Hi10P 标记）。
func TestGoProbeSynthMKV(t *testing.T) {
	f, _ := os.CreateTemp("", "probe-*.mkv")
	defer os.Remove(f.Name())
	f.Write(synthMKV())
	f.Close()
	got, err := goProbeFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got.Container != mkvContainerName {
		t.Errorf("容器不对: %q", got.Container)
	}
	if got.VideoCodec != "h264" || got.Width != 640 || got.Height != 360 {
		t.Errorf("视频不对: %+v", got)
	}
	if got.AudioCodec != "aac" {
		t.Errorf("音频不对: %+v", got)
	}
	if got.DurationSec != 15 {
		t.Errorf("时长不对: %v", got.DurationSec)
	}
	if got.PixFmt != "yuv420p10le" {
		t.Errorf("应标 10-bit: %+v", got)
	}
}

// TestGoProbeUnknown 未知容器返回保守 verdict（可转码，不报错）。
func TestGoProbeUnknown(t *testing.T) {
	f, _ := os.CreateTemp("", "probe-*.bin")
	defer os.Remove(f.Name())
	f.Write([]byte("0123456789ABCDEF0123456789ABCDEF"))
	f.Close()
	got, err := goProbeFile(f.Name())
	if err != nil {
		t.Fatalf("未知容器不应报错: %v", err)
	}
	if got.VideoCodec != "" {
		t.Errorf("未知容器不应有编码: %+v", got)
	}
	if plan := PlanForLocal(got, DeviceCapabilities{}); plan.Mode != OutputTranscode {
		t.Errorf("未知容器必须转码: %+v", plan)
	}
}

// TestGoProbeTruncated 截断文件报错不 panic。
func TestGoProbeTruncated(t *testing.T) {
	data := synthMP4()
	for _, n := range []int{10, 100, len(data) / 2} {
		f, _ := os.CreateTemp("", "probe-cut-*.mp4")
		f.Write(data[:n])
		f.Close()
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("截断文件 panic (n=%d): %v", n, r)
				}
			}()
			_, _ = goProbeFile(f.Name())
		}()
		os.Remove(f.Name())
	}
	mkv := synthMKV()
	for _, n := range []int{10, 100, len(mkv) / 2} {
		f, _ := os.CreateTemp("", "probe-cut-*.mkv")
		f.Write(mkv[:n])
		f.Close()
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("截断 MKV panic (n=%d): %v", n, r)
				}
			}()
			_, _ = goProbeFile(f.Name())
		}()
		os.Remove(f.Name())
	}
}

// TestGoProbeMatchesFFprobe 真实文件与 ffprobe 逐项一致（oracle 测试）。
func TestGoProbeMatchesFFprobe(t *testing.T) {
	if _, ok := ResolveTool("ffprobe"); !ok {
		t.Skip("无 ffprobe，跳过对照")
	}
	dir := t.TempDir()
	mp4 := filepath.Join(dir, "s.mp4")
	if out, err := runFFmpegProbeSample(mp4); err != nil {
		t.Skipf("生成样本失败: %v", err)
	} else {
		_ = out
	}
	check := func(path string) {
		t.Helper()
		got, err := goProbeFile(path)
		if err != nil {
			t.Fatalf("%s Go 失败: %v", path, err)
		}
		want, err := probeWithFFprobe(path)
		if err != nil {
			t.Fatalf("%s ffprobe 失败: %v", path, err)
		}
		if got.VideoCodec != want.VideoCodec || got.AudioCodec != want.AudioCodec ||
			got.Width != want.Width || got.Height != want.Height ||
			got.PixFmt != want.PixFmt || got.Container != want.Container ||
			abs(got.DurationSec-want.DurationSec) > 0.5 || got.SizeBytes != want.SizeBytes ||
			got.FastStart != want.FastStart {
			t.Errorf("不一致:\nGo=%+v\nffprobe=%+v", got, want)
		}
	}
	check(mp4)
	// 同一样本转 MKV 再对照。
	mkv := filepath.Join(dir, "s.mkv")
	if err := remuxToMKV(mp4, mkv); err != nil {
		t.Skipf("转 MKV 失败: %v", err)
	}
	check(mkv)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func isTestTool(name string) bool { _, ok := ResolveTool(name); return ok }

// runFFmpegProbeSample 生成 h264+aac 测试样本。
func runFFmpegProbeSample(path string) (string, error) {
	if !isTestTool("ffmpeg") {
		return "", os.ErrNotExist
	}
	cmd := exec.Command(toolLookupPath("ffmpeg"), "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=5:size=640x360:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=5",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v: %s", err, out)
	}
	return path, nil
}

// remuxToMKV 无损转封装供对照。
func remuxToMKV(src, dst string) error {
	cmd := exec.Command(toolLookupPath("ffmpeg"), "-hide_banner", "-loglevel", "error", "-y",
		"-i", src, "-c", "copy", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// probeWithFFprobe 是旧实现的测试内拷贝，作为对照基准（oracle）。
func probeWithFFprobe(path string) (*Info, error) {
	cmd := exec.Command(toolLookupPath("ffprobe"), "-v", "error",
		"-print_format", "json", "-show_format", "-show_streams", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var raw struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			PixFmt    string `json:"pix_fmt"`
		} `json:"streams"`
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
			Size       string `json:"size"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	info := &Info{
		Path:      path,
		Container: raw.Format.FormatName,
		Title:     strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		FastStart: true,
	}
	fmt.Sscanf(raw.Format.Duration, "%f", &info.DurationSec)
	fmt.Sscanf(raw.Format.Size, "%d", &info.SizeBytes)
	if strings.Contains(strings.ToLower(info.Container), "mp4") ||
		strings.Contains(strings.ToLower(info.Container), "mov") {
		if fast, err := IsFastStart(path); err == nil {
			info.FastStart = fast
		} else {
			info.FastStart = false
		}
	}
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if info.VideoCodec == "" {
				info.VideoCodec, info.Width, info.Height = s.CodecName, s.Width, s.Height
				info.PixFmt = s.PixFmt
			}
		case "audio":
			if info.AudioCodec == "" {
				info.AudioCodec = s.CodecName
			}
		}
	}
	return info, nil
}
