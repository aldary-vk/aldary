package rfb

import "encoding/binary"

// CopyRect 编码：仅包含源矩形左上角坐标 (rfbCopyRect)，无像素数据。
// 对应 rfbproto.h 中的 rfbCopyRect 结构。
func (e *Encoder) encodeCopyRect(rect Rect) []byte {
	body := make([]byte, 4)
	binary.BigEndian.PutUint16(body[0:2], uint16(rect.X))
	binary.BigEndian.PutUint16(body[2:4], uint16(rect.Y))
	return assemble(rect, EncodingCopyRect, body)
}