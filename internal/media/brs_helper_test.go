package media

import "bytes"

// newBRS 把字节切片包成可在 ServeContent 中按 Range 读取的 ReaderAt。
func newBRS(b []byte) *bytes.Reader { return bytes.NewReader(b) }
