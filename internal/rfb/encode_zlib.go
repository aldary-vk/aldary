package rfb

import (
	"bytes"
	"encoding/binary"
)

// 本文件实现 Zlib 编码，对应 UltraVNC vncEncodeZlib.cpp / common/UltraVncZ.cpp。
//
// 编码格式为 [矩形头][4 字节压缩长度][zlib 压缩数据]，其中 zlib 流在连接生命周期内
// 持久复用，每个矩形边界以 Z_SYNC_FLUSH 刷新。与 UltraVNC 一致：
//   - 当原始数据小于 minSize(25) 字节时，回退为 Raw 编码
//   - 大矩形按 maxLines = maxSize(w*h)/w 拆分为多个子矩形分别编码
func (e *Encoder) encodeZlib(fb Framebuffer, rect Rect) [][]byte {
	rect = clampRect(rect, fb.Width(), fb.Height())
	if rect.W <= 0 || rect.H <= 0 {
		return nil
	}

	// ultraVncZ->maxSize(size) = max(size, 8192)
	maxSize := rect.W * rect.H
	if maxSize < 8192 {
		maxSize = 8192
	}
	maxLines := maxSize / rect.W
	if maxLines < 1 {
		maxLines = 1
	}

	out := make([][]byte, 0, 4)
	linesRemaining := rect.H
	top := rect.Y
	for linesRemaining > 0 {
		linesToComp := maxLines
		if linesToComp > linesRemaining {
			linesToComp = linesRemaining
		}
		sub := Rect{X: rect.X, Y: top, W: rect.W, H: linesToComp}
		out = append(out, e.encodeZlibOne(fb, sub))
		linesRemaining -= linesToComp
		top += linesToComp
	}
	return out
}

func (e *Encoder) encodeZlibOne(fb Framebuffer, rect Rect) []byte {
	rawDataSize := rect.W * rect.H * e.PF.BytesPerPixel()
	// minSize() == 25，小于该值直接按 Raw 编码 (与 UltraVNC 一致)。
	if rawDataSize < 25 {
		return e.encodeRaw(fb, rect)
	}

	vals := translateRect(fb, rect, e.PF)
	var raw bytes.Buffer
	writeRawPixels(&raw, vals, e.PF)

	compressed := e.zlibStream().compress(raw.Bytes())

	body := make([]byte, 4+len(compressed))
	binary.BigEndian.PutUint32(body[0:4], uint32(len(compressed)))
	copy(body[4:], compressed)
	return assemble(rect, EncodingZlib, body)
}