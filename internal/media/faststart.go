package media

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// mp4Atom 是 MP4 文件中的一个顶层 box。
type mp4Atom struct {
	Type string
	Size uint64
}

// IsFastStart 判断 MP4/MOV 文件是否把索引（moov）放在媒体数据（mdat）之前。
//
// 这对流式播放是硬性要求：播放器需要先读到 moov 才能知道轨道布局与时长，
// 若 moov 在文件末尾，就必须下载完整个文件才能起播。实测这类文件投给电视后
// 电视会一直卡在 0 进度不出画面。
//
// 仅扫描顶层 box，读够所需信息即返回，不会读取整个文件。
func IsFastStart(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	moovPos, mdatPos := -1, -1
	index := 0
	for {
		atom, err := readMP4Atom(f)
		if err != nil {
			if err == io.EOF {
				break
			}
			return false, err
		}
		switch atom.Type {
		case "moov":
			if moovPos < 0 {
				moovPos = index
			}
		case "mdat":
			if mdatPos < 0 {
				mdatPos = index
			}
		}
		// 两个位置都已确定，无需继续扫描。
		if moovPos >= 0 && mdatPos >= 0 {
			break
		}
		// 跳过该 box 的剩余内容。
		if atom.Size < 8 {
			return false, fmt.Errorf("MP4 box %q 长度非法: %d", atom.Type, atom.Size)
		}
		if _, err := f.Seek(int64(atom.Size-8), io.SeekCurrent); err != nil {
			return false, err
		}
		index++
	}

	switch {
	case moovPos < 0:
		return false, fmt.Errorf("未找到 moov box，可能不是有效的 MP4 文件")
	case mdatPos < 0:
		// 没有 mdat（例如仅含索引的元数据文件）时无需考虑顺序。
		return true, nil
	default:
		return moovPos < mdatPos, nil
	}
}

// readMP4Atom 从当前位置读取一个顶层 box 的类型与总长度。
// 支持 32 位长度、64 位扩展长度（size==1）以及延伸到文件末尾（size==0）。
func readMP4Atom(f *os.File) (mp4Atom, error) {
	var header [8]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return mp4Atom{}, io.EOF
		}
		return mp4Atom{}, err
	}
	size := uint64(binary.BigEndian.Uint32(header[:4]))
	atomType := string(header[4:8])

	switch size {
	case 0:
		// 长度 0 表示该 box 延伸到文件末尾。
		pos, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return mp4Atom{}, err
		}
		end, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			return mp4Atom{}, err
		}
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return mp4Atom{}, err
		}
		size = uint64(end - pos + 8)
	case 1:
		// 长度 1 表示真实长度由紧随其后的 8 字节给出。
		var extended [8]byte
		if _, err := io.ReadFull(f, extended[:]); err != nil {
			return mp4Atom{}, err
		}
		size = binary.BigEndian.Uint64(extended[:])
	}
	return mp4Atom{Type: atomType, Size: size}, nil
}
