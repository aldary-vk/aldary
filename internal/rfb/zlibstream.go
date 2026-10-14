package rfb

import (
	"compress/zlib"
	"io"
)

// zlibStream 包装一个每连接持久的 zlib 压缩流。
// RFB 的 Zlib / ZRLE 编码要求同一编码类型在整个连接生命周期内复用同一条
// zlib 流，并在每个矩形边界以 Z_SYNC_FLUSH 刷新，使客户端能用持久 inflate
// 流增量解压。这与 UltraVNC common/UltraVncZ.cpp 的 deflateInit2 + Z_SYNC_FLUSH
// 语义一致。Go 的 compress/zlib.Writer.Flush() 即执行 Z_SYNC_FLUSH。
type zlibStream struct {
	w   *zlib.Writer
	cap *captureWriter
	off int // cap.buf 中已被调用方取走的字节偏移
}

func newZlibStream(level int) *zlibStream {
	c := &captureWriter{}
	w, _ := zlib.NewWriterLevel(c, level)
	return &zlibStream{w: w, cap: c}
}

// compress 将 data 送入 zlib 流并 Z_SYNC_FLUSH，返回自上次调用以来新产生的压缩字节。
// 返回值是独立副本，后续对流的写操作不会影响已返回的数据。
func (z *zlibStream) compress(data []byte) []byte {
	_, _ = z.w.Write(data)
	_ = z.w.Flush()
	out := make([]byte, len(z.cap.buf)-z.off)
	copy(out, z.cap.buf[z.off:])
	z.off = len(z.cap.buf)
	return out
}

var _ io.Writer = (*captureWriter)(nil)