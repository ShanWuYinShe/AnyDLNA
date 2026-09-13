package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Go 原生本地文件探测：替代 ffprobe，省 51MB 打包体积与一次子进程调用。
//
// 覆盖 MP4/MOV 与 MKV/WebM（本地文件的绝对主流）；其他容器（AVI/TS 等）
// 返回无编码信息，PlanForLocal 会将其判为转码——结果保守但正确，
// 不会把播不了的文件直出给电视。
//
// 只读 moov/Segment 头部索引区，不碰 mdat 媒体数据，探测即返回。

// mp4ContainerName 模仿 ffprobe 的 format_name，保持计划逻辑与测试一致。
const mp4ContainerName = "mov,mp4,m4a,3gp,3g2,mj2"

// mkvContainerName 模仿 ffprobe 的 format_name。
const mkvContainerName = "matroska,webm"

// maxMoovSize 是 moov 盒子的读取上限。超长视频的索引也就几十 MB，
// 超限说明文件异常，回退保守 verdict。
const maxMoovSize = 64 << 20

// goProbeFile 按魔数嗅探容器并解析，返回 Info；无法识别时返回保守 verdict。
func goProbeFile(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	info := &Info{
		Path:      path,
		Title:     strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		SizeBytes: st.Size(),
		// 默认可流式；MP4/MOV 实测后修正（与原 ffprobe 路径语义一致）。
		FastStart: true,
	}

	head := make([]byte, 12)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, fmt.Errorf("文件过小或不可读: %w", err)
	}
	// EBML 头魔数：MKV/WebM。
	if head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3 {
		info.Container = mkvContainerName
		if err := parseMKV(f, info); err != nil {
			return nil, err
		}
		return info, nil
	}
	// ftyp 盒子（size + "ftyp"）：MP4/MOV。
	if string(head[4:8]) == "ftyp" {
		info.Container = mp4ContainerName
		if err := parseMP4(f, info); err != nil {
			return nil, err
		}
		if fast, ferr := IsFastStart(path); ferr == nil {
			info.FastStart = fast
		} else {
			info.FastStart = false
		}
		return info, nil
	}
	// 未知容器：无编码信息，调用方按转码处理（安全回退）。
	return info, nil
}

// ---------- MP4 ----------

// mp4Box 是读出的一个盒子（含载荷）。
type mp4Box struct {
	typ     string
	payload []byte
}

// readMP4Boxes 顺序读出 r 中限定长度内的顶层盒子。
func readMP4Boxes(r io.Reader, limit int64) ([]mp4Box, error) {
	var out []mp4Box
	lr := io.LimitReader(r, limit)
	for {
		hdr := make([]byte, 8)
		if _, err := io.ReadFull(lr, hdr); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, nil
			}
			return nil, err
		}
		size := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		if size == 1 {
			ext := make([]byte, 8)
			if _, err := io.ReadFull(lr, ext); err != nil {
				return nil, err
			}
			size = int64(binary.BigEndian.Uint64(ext))
		} else if size == 0 {
			// size 0 表示延伸到末尾：载荷未知，不支持。
			return nil, fmt.Errorf("不支持的盒子 %s（size=0）", typ)
		}
		if size < 8 {
			return nil, fmt.Errorf("非法盒子 %s 长度 %d", typ, size)
		}
		payload := make([]byte, size-8)
		if _, err := io.ReadFull(lr, payload); err != nil {
			return nil, fmt.Errorf("盒子 %s 载荷不完整: %w", typ, err)
		}
		out = append(out, mp4Box{typ, payload})
	}
}

// childBoxes 解析容器盒子载荷内的子盒子。
func childBoxes(payload []byte) ([]mp4Box, error) {
	return readMP4Boxes(bytes.NewReader(payload), int64(len(payload)))
}

// parseMP4 解析 moov 索引，填充编码、尺寸与时长。
func parseMP4(f *os.File, info *Info) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// 顶层找 moov（通常紧跟 ftyp；mdat 可能在前，跳过它）。
	for {
		hdr := make([]byte, 8)
		if _, err := io.ReadFull(f, hdr); err != nil {
			return fmt.Errorf("未找到 moov: %w", err)
		}
		size := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		if size == 1 {
			ext := make([]byte, 8)
			if _, err := io.ReadFull(f, ext); err != nil {
				return err
			}
			size = int64(binary.BigEndian.Uint64(ext))
		}
		if size < 8 {
			return fmt.Errorf("非法顶层盒子 %s", typ)
		}
		if typ == "moov" {
			if size-8 > maxMoovSize {
				return fmt.Errorf("moov 过大（%d 字节）", size)
			}
			moov := make([]byte, size-8)
			if _, err := io.ReadFull(f, moov); err != nil {
				return fmt.Errorf("moov 不完整: %w", err)
			}
			return parseMoov(moov, info)
		}
		if _, err := f.Seek(size-8, io.SeekCurrent); err != nil {
			return err
		}
	}
}

// parseMoov 解析 moov：mvhd 时长 + 各 trak 的编码。
func parseMoov(moov []byte, info *Info) error {
	boxes, err := childBoxes(moov)
	if err != nil {
		return err
	}
	for _, b := range boxes {
		switch b.typ {
		case "mvhd":
			info.DurationSec = parseMVHD(b.payload)
		case "trak":
			parseTrak(b.payload, info)
		}
	}
	return nil
}

// parseMVHD 解析时长（秒）。
func parseMVHD(p []byte) float64 {
	if len(p) < 4 {
		return 0
	}
	if p[0] == 1 {
		if len(p) < 28 {
			return 0
		}
		ts := binary.BigEndian.Uint32(p[20:24])
		dur := binary.BigEndian.Uint64(p[24:32])
		if ts == 0 {
			return 0
		}
		return float64(dur) / float64(ts)
	}
	if len(p) < 20 {
		return 0
	}
	ts := binary.BigEndian.Uint32(p[12:16])
	dur := binary.BigEndian.Uint32(p[16:20])
	if ts == 0 {
		return 0
	}
	return float64(dur) / float64(ts)
}

// parseTrak 解析一个轨道：第一个视频轨与第一个音频轨胜出（与原逻辑一致）。
func parseTrak(trak []byte, info *Info) {
	boxes, err := childBoxes(trak)
	if err != nil {
		return
	}
	var mdia []byte
	for _, b := range boxes {
		if b.typ == "mdia" {
			mdia = b.payload
		}
	}
	if mdia == nil {
		return
	}
	boxes, err = childBoxes(mdia)
	if err != nil {
		return
	}
	var minf []byte
	var handler string
	for _, b := range boxes {
		if b.typ == "hdlr" && len(b.payload) >= 12 {
			handler = string(b.payload[8:12])
		}
		if b.typ == "minf" {
			minf = b.payload
		}
	}
	if minf == nil {
		return
	}
	boxes, err = childBoxes(minf)
	if err != nil {
		return
	}
	for _, b := range boxes {
		if b.typ != "stbl" {
			continue
		}
		stbl, err := childBoxes(b.payload)
		if err != nil {
			return
		}
		for _, s := range stbl {
			if s.typ != "stsd" || len(s.payload) < 8 {
				continue
			}
			entries, err := childBoxes(s.payload[8:])
			if err != nil || len(entries) == 0 {
				continue
			}
			e := entries[0]
			switch handler {
			case "vide":
				if info.VideoCodec != "" {
					continue
				}
				codec, w, h, pix := parseVideoEntry(e.typ, e.payload)
				info.VideoCodec, info.Width, info.Height, info.PixFmt = codec, w, h, pix
			case "soun":
				if info.AudioCodec != "" {
					continue
				}
				info.AudioCodec = parseAudioEntry(e.typ, e.payload)
			}
		}
	}
}

// parseVideoEntry 解析视频 sample entry，返回编码名（ffprobe 风格）、宽高、像素格式。
func parseVideoEntry(fourcc string, p []byte) (codec string, w, h int, pix string) {
	switch fourcc {
	case "avc1", "avc3":
		codec = "h264"
	case "hev1", "hvc1":
		codec = "hevc"
	case "vp08":
		codec = "vp8"
	case "vp09":
		codec = "vp9"
	case "av01":
		codec = "av1"
	case "mp4v":
		codec = "mpeg4"
	default:
		codec = strings.ToLower(fourcc)
	}
	if len(p) >= 28 {
		w, h = int(binary.BigEndian.Uint16(p[24:26])), int(binary.BigEndian.Uint16(p[26:28]))
	}
	// avcC[1] 是 profile_idc：110/122/244 为 10-bit（Hi10P 等动漫常见），
	// 电视普遍解不了，必须走转码。这里直接给出 10-bit 像素格式标记，
	// 下游现有的高比特深度判断无需改动。找不到 avcC 时按 8-bit 处理
	// （ffprobe 同场景也会给出 yuv420p，不静默留空）。
	if fourcc == "avc1" || fourcc == "avc3" {
		pix = "yuv420p"
		if avcc := findChild(p, "avcC", func(b []byte) bool {
			return len(b) >= 2 && b[0] == 1
		}); len(avcc) >= 2 {
			switch avcc[1] {
			case 110, 122, 244:
				pix = "yuv420p10le"
			}
		}
	}
	return codec, w, h, pix
}

// parseAudioEntry 解析音频 sample entry，返回 ffprobe 风格编码名。
func parseAudioEntry(fourcc string, p []byte) string {
	switch fourcc {
	case "mp4a":
		return parseESDSObjectType(p)
	case "ac-3":
		return "ac3"
	case "ec-3":
		return "eac3"
	case "alac":
		return "alac"
	case "Opus":
		return "opus"
	case "fLaC":
		return "flac"
	case ".mp3":
		return "mp3"
	case "mp4v":
		return "mpeg4"
	default:
		return strings.ToLower(fourcc)
	}
}

// parseESDSObjectType 从 esds 盒子读对象类型：0x40 系为 AAC，0x69/0x6B 实为 MP3。
func parseESDSObjectType(entryPayload []byte) string {
	esds := findChild(entryPayload, "esds", func(b []byte) bool {
		return len(b) >= 5 && b[4] == 0x03
	})
	if len(esds) < 8 {
		return "aac"
	}
	p := esds[4:] // 跳过 version/flags
	if len(p) < 1 || p[0] != 0x03 {
		return "aac"
	}
	p, ok := skipDescriptor(p[1:])
	if !ok || len(p) < 3 {
		return "aac"
	}
	p = p[3:] // ES_ID(2) + flags(1)
	if len(p) < 1 || p[0] != 0x04 {
		return "aac"
	}
	p, ok = skipDescriptor(p[1:])
	if !ok || len(p) < 1 {
		return "aac"
	}
	switch p[0] {
	case 0x40, 0x66, 0x67, 0x68:
		return "aac"
	case 0x69, 0x6B:
		return "mp3"
	default:
		return "mp4a"
	}
}

// skipDescriptor 跳过 ES 描述符的长度字段（7bit 续接编码），返回剩余部分。
func skipDescriptor(p []byte) ([]byte, bool) {
	for i := 0; i < 4 && i < len(p); i++ {
		if p[i]&0x80 == 0 {
			return p[i+1:], true
		}
	}
	return nil, false
}

// findChild 在 sample entry 载荷中找指定 fourcc 的子盒子，返回其载荷。
//
// 不能从头按盒子链走：entry 载荷开头是定长头（视频 78 字节、音频 24 字节起，
// 全零保留字段会被误读成 size=0 的盒子）。逐字节扫描 fourcc 并用 valid
// 校验内容（avcC 版本号、esds 描述符头），压缩机名字符串里偶然出现的
// 同名字节会被校验挡掉。
func findChild(payload []byte, want string, valid func([]byte) bool) []byte {
	for i := 0; i+8 <= len(payload); i++ {
		if string(payload[i+4:i+8]) != want {
			continue
		}
		size := int(binary.BigEndian.Uint32(payload[i : i+4]))
		if size < 8 || i+size > len(payload) {
			continue
		}
		if body := payload[i+8 : i+size]; valid(body) {
			return body
		}
	}
	return nil
}

// ---------- MKV ----------

// Matroska 元素 ID（仅解析需要 ones）。
const (
	ebmlSegment        = 0x18538067
	ebmlInfo           = 0x1549A966
	ebmlTimestampScale = 0x2AD7B1
	ebmlDuration       = 0x4489
	ebmlTracks         = 0x1654AE6B
	ebmlTrackEntry     = 0xAE
	ebmlTrackNumber    = 0xD7
	ebmlTrackType      = 0x83
	ebmlCodecID        = 0x86
	ebmlCodecPrivate   = 0x63A2
	ebmlVideo          = 0xE0
	ebmlPixelWidth     = 0xB0
	ebmlPixelHeight    = 0xBA
)

// mkvReader 是带上限的顺序读取器。
type mkvReader struct {
	f    *os.File
	left int64 // 剩余可读字节（元素载荷边界）；-1 表示直到文件尾
}

// readEBMLID 读元素 ID（长度位计入值）。
func (r *mkvReader) readEBMLID() (uint64, error) {
	b := make([]byte, 1)
	if _, err := io.ReadFull(r.f, b); err != nil {
		return 0, err
	}
	n := 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		n++
		if n > 4 {
			return 0, fmt.Errorf("非法 EBML ID")
		}
	}
	id := uint64(b[0])
	for i := 1; i < n; i++ {
		if _, err := io.ReadFull(r.f, b); err != nil {
			return 0, err
		}
		id = id<<8 | uint64(b[0])
	}
	r.left -= int64(n)
	return id, nil
}

// readEBMLSize 读数据长度（全 1 表示未知长度）。
func (r *mkvReader) readEBMLSize() (int64, error) {
	b := make([]byte, 1)
	if _, err := io.ReadFull(r.f, b); err != nil {
		return 0, err
	}
	n := 1
	mask := byte(0x80)
	for ; b[0]&mask == 0; mask >>= 1 {
		n++
		if n > 8 {
			return 0, fmt.Errorf("非法 EBML 长度")
		}
	}
	size := int64(b[0] & (^mask))
	allOne := b[0] == 0xFF
	for i := 1; i < n; i++ {
		if _, err := io.ReadFull(r.f, b); err != nil {
			return 0, err
		}
		size = size<<8 | int64(b[0])
		allOne = allOne && b[0] == 0xFF
	}
	r.left -= int64(n)
	if allOne {
		return -1, nil
	}
	return size, nil
}

func (r *mkvReader) bytes(n int64) ([]byte, error) {
	if n < 0 || (r.left >= 0 && n > r.left) {
		return nil, fmt.Errorf("元素越界")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.f, buf); err != nil {
		return nil, err
	}
	r.left -= n
	return buf, nil
}

func (r *mkvReader) uint(n int64) (uint64, error) {
	b, err := r.bytes(n)
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

func (r *mkvReader) skip(n int64) error {
	if n < 0 {
		return fmt.Errorf("未知长度无法跳过")
	}
	if _, err := r.f.Seek(n, io.SeekCurrent); err != nil {
		return err
	}
	r.left -= n
	return nil
}

// subReader 切出载荷子读取器（size<0 时读到父边界）。
func (r *mkvReader) subReader(size int64) *mkvReader {
	left := size
	if size < 0 {
		left = r.left
	}
	return &mkvReader{f: r.f, left: left}
}

// consume 把子读取器读剩的字节计入父读取器（子用 Seek 跳过时父边界需同步）。
func (r *mkvReader) consume(child *mkvReader, size int64) {
	if size < 0 {
		return
	}
	r.left -= size - child.left
}

// parseMKV 解析 Segment 的 Info 与 Tracks。
func parseMKV(f *os.File, info *Info) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	r := &mkvReader{f: f, left: st.Size()}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	for r.left != 0 {
		id, err := r.readEBMLID()
		if err != nil {
			return err
		}
		size, err := r.readEBMLSize()
		if err != nil {
			return err
		}
		if id == ebmlSegment {
			sub := r.subReader(size)
			if err := parseSegment(sub, info); err != nil {
				return err
			}
			r.consume(sub, size)
			// Segment 之后只有 Cluster（媒体数据），可停。
			return nil
		}
		// EBML 头等顶层元素直接跳过。
		if size < 0 {
			return fmt.Errorf("顶层未知长度")
		}
		if err := r.skip(size); err != nil {
			return err
		}
	}
	return fmt.Errorf("未找到 Segment")
}

// parseSegment 解析 Info（时长）与 Tracks（编码）。
func parseSegment(r *mkvReader, info *Info) error {
	timeScale := uint64(1000000)
	var duration float64
	var haveDuration bool
	for r.left != 0 {
		id, err := r.readEBMLID()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		size, err := r.readEBMLSize()
		if err != nil {
			return err
		}
		switch id {
		case ebmlInfo:
			sub := r.subReader(size)
			ts, dur, hasDur, err := parseInfo(sub)
			if err != nil {
				return err
			}
			if ts != 0 {
				timeScale = ts
			}
			duration, haveDuration = dur, hasDur
			r.consume(sub, size)
		case ebmlTracks:
			sub := r.subReader(size)
			if err := parseTracks(sub, info); err != nil {
				return err
			}
			r.consume(sub, size)
			// Tracks 之后是媒体数据；时长若已知可直接返回。
			if haveDuration {
				info.DurationSec = duration * float64(timeScale) / 1e9
				return nil
			}
		default:
			// Cluster 等媒体数据：Info/Tracks 已读完就停，否则跳过。
			if info.VideoCodec != "" && haveDuration {
				return nil
			}
			if size < 0 {
				return nil
			}
			if err := r.skip(size); err != nil {
				return err
			}
		}
	}
	if haveDuration {
		info.DurationSec = duration * float64(timeScale) / 1e9
	}
	return nil
}

// parseInfo 解析时间戳精度与时长。
func parseInfo(r *mkvReader) (timeScale uint64, duration float64, haveDuration bool, err error) {
	timeScale = 1000000
	for r.left != 0 {
		id, err := r.readEBMLID()
		if err != nil {
			return 0, 0, false, err
		}
		size, err := r.readEBMLSize()
		if err != nil {
			return 0, 0, false, err
		}
		switch id {
		case ebmlTimestampScale:
			if v, err := r.uint(size); err == nil {
				timeScale = v
			} else {
				return 0, 0, false, err
			}
		case ebmlDuration:
			b, err := r.bytes(size)
			if err != nil {
				return 0, 0, false, err
			}
			switch size {
			case 4:
				duration = float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
			case 8:
				duration = math.Float64frombits(binary.BigEndian.Uint64(b))
			default:
				return 0, 0, false, fmt.Errorf("非法时长长度 %d", size)
			}
			haveDuration = true
		default:
			if err := r.skip(size); err != nil {
				return 0, 0, false, err
			}
		}
	}
	return timeScale, duration, haveDuration, nil
}

// parseTracks 解析轨道：第一个视频轨与第一个音频轨胜出。
func parseTracks(r *mkvReader, info *Info) error {
	for r.left != 0 {
		id, err := r.readEBMLID()
		if err != nil {
			return err
		}
		size, err := r.readEBMLSize()
		if err != nil {
			return err
		}
		if id != ebmlTrackEntry {
			if err := r.skip(size); err != nil {
				return err
			}
			continue
		}
		sub := r.subReader(size)
		codec, w, h, pix, trackType, err := parseTrackEntry(sub)
		r.consume(sub, size)
		if err != nil {
			return err
		}
		switch trackType {
		case 1:
			if info.VideoCodec == "" {
				info.VideoCodec, info.Width, info.Height, info.PixFmt = codec, w, h, pix
			}
		case 2:
			if info.AudioCodec == "" {
				info.AudioCodec = codec
			}
		}
	}
	return nil
}

// parseTrackEntry 解析单个轨道条目。
func parseTrackEntry(r *mkvReader) (codec string, w, h int, pix string, trackType uint64, err error) {
	var codecID string
	var priv []byte
	for r.left != 0 {
		id, err := r.readEBMLID()
		if err != nil {
			return "", 0, 0, "", 0, err
		}
		size, err := r.readEBMLSize()
		if err != nil {
			return "", 0, 0, "", 0, err
		}
		switch id {
		case ebmlTrackType:
			if v, err := r.uint(size); err == nil {
				trackType = v
			} else {
				return "", 0, 0, "", 0, err
			}
		case ebmlCodecID:
			if b, err := r.bytes(size); err == nil {
				codecID = string(b)
			} else {
				return "", 0, 0, "", 0, err
			}
		case ebmlCodecPrivate:
			if b, err := r.bytes(size); err == nil {
				priv = b
			} else {
				return "", 0, 0, "", 0, err
			}
		case ebmlVideo:
			sub := r.subReader(size)
			ww, hh, err := parseVideo(sub)
			r.consume(sub, size)
			if err != nil {
				return "", 0, 0, "", 0, err
			}
			w, h = ww, hh
		default:
			if err := r.skip(size); err != nil {
				return "", 0, 0, "", 0, err
			}
		}
	}
	return mkvCodecName(codecID, priv), w, h, mkvPixFmt(codecID, priv), trackType, nil
}

// parseVideo 解析视频宽高。
func parseVideo(r *mkvReader) (int, int, error) {
	var w, h int
	for r.left != 0 {
		id, err := r.readEBMLID()
		if err != nil {
			return 0, 0, err
		}
		size, err := r.readEBMLSize()
		if err != nil {
			return 0, 0, err
		}
		switch id {
		case ebmlPixelWidth:
			if v, err := r.uint(size); err == nil {
				w = int(v)
			} else {
				return 0, 0, err
			}
		case ebmlPixelHeight:
			if v, err := r.uint(size); err == nil {
				h = int(v)
			} else {
				return 0, 0, err
			}
		default:
			if err := r.skip(size); err != nil {
				return 0, 0, err
			}
		}
	}
	return w, h, nil
}

// mkvCodecName 把 Matroska CodecID 映射为 ffprobe 风格编码名。
func mkvCodecName(codecID string, priv []byte) string {
	switch codecID {
	case "V_MPEG4/ISO/AVC":
		return "h264"
	case "V_MPEGH/ISO/HEVC":
		return "hevc"
	case "V_VP8":
		return "vp8"
	case "V_VP9":
		return "vp9"
	case "V_AV1":
		return "av1"
	case "V_MPEG4/ISO/ASP":
		return "mpeg4"
	case "V_THEORA":
		return "theora"
	case "A_AAC":
		return "aac"
	case "A_MPEG/L3":
		return "mp3"
	case "A_AC3":
		return "ac3"
	case "A_EAC3":
		return "eac3"
	case "A_OPUS":
		return "opus"
	case "A_FLAC":
		return "flac"
	case "A_VORBIS":
		return "vorbis"
	case "A_DTS":
		return "dts"
	default:
		// 未知按最后一段小写处理（如 A_TRUEHD → truehd）。
		if i := strings.LastIndex(codecID, "/"); i >= 0 {
			return strings.ToLower(codecID[i+1:])
		}
		return strings.ToLower(codecID)
	}
}

// mkvPixFmt 从 CodecPrivate 判断 H.264 高比特深度（avcC profile），
// 其余按 8-bit 处理（HEVC/VP9 本来就要转码，深度无意义）。
func mkvPixFmt(codecID string, priv []byte) string {
	if codecID == "V_MPEG4/ISO/AVC" && len(priv) >= 2 {
		switch priv[1] {
		case 110, 122, 244:
			return "yuv420p10le"
		}
		return "yuv420p"
	}
	return ""
}
