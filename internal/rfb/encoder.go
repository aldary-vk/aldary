package rfb

import (
	"bytes"
	"encoding/binary"
)

// Encoder 负责把一个更新矩形按客户端协商的像素格式与编码类型，
// 编码为一段或多段完整的 RFB 更新矩形数据 (12 字节头 + 编码负载)。
// 其职责对应 UltraVNC 的 vncEncoder 体系：Translate() 负责像素格式转换，
// 各子类 (Raw/RRE/CoRRE/Hextile/Zlib/ZRLE) 负责具体编码。
type Encoder struct {
	PF            PixelFormat // 客户端协商的像素格式
	CompressLevel int         // zlib 压缩级别 (0..9)

	// Tight 专用伪编码协商结果 (来自客户端 SetEncodings)：
	//   tightQuality       JPEG 质量 0..100；<0 表示客户端未声明支持 JPEG (保持无损)
	//   tightSubsampling   JPEG 色度子采样 (0..5，对应 Subsamp1X..Subsamp16X)，仅记录
	//
	// 与 UltraVNC vncClient 的 m_qualityLevel / TightVNC 的 tightQualityLevel 对应。
	tightQuality      int
	tightSubsampling  int

	// 各编码类型使用独立的、每连接持久的 zlib 流 (Z_SYNC_FLUSH 逐块输出)，
	// 与 UltraVNC 中 ZlibEncoder 的 ultraVncZ、ZRLEEncoder 的 zos 一一对应。
	zlibEnc *zlibStream // Zlib 编码专用
	zrleEnc *zlibStream // ZRLE 编码专用

	// Tight 编码使用 4 条独立、每连接持久的 zlib 流 (索引 0..3)，
	// 与 vncEncodeTight 的 ultraVncZTight[4] 一一对应。
	tightEnc [4]*zlibStream
}

func (e *Encoder) zlibStream() *zlibStream {
	if e.zlibEnc == nil {
		e.zlibEnc = newZlibStream(e.CompressLevel)
	}
	return e.zlibEnc
}

func (e *Encoder) zrleStream() *zlibStream {
	if e.zrleEnc == nil {
		e.zrleEnc = newZlibStream(e.CompressLevel)
	}
	return e.zrleEnc
}

// captureWriter 捕获 zlib 流每次 Flush 后新产生的压缩字节。
// zlib.Writer 的压缩状态在内部持久化，这里仅持续接收输出并每次取走增量。
type captureWriter struct {
	buf []byte
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.buf = append(c.buf, p...)
	return len(p), nil
}

// NewEncoder 创建一个编码器。level 为 zlib 压缩级别，超范围时钳制到 0..9。
func NewEncoder(pf PixelFormat, level int) *Encoder {
	if level < 0 {
		level = 6
	}
	if level > 9 {
		level = 9
	}
	return &Encoder{PF: pf, CompressLevel: level, tightQuality: -1, tightSubsampling: -1}
}

// tightJpegQuality 返回客户端声明的 JPEG 质量 0..100；负数表示未声明 (禁用 JPEG)。
func (e *Encoder) jpegEnabled() bool { return e.tightQuality >= 0 }

// translateRect 将 framebuffer 的矩形区域转换为客户端像素格式下的 native 值切片。
// 对应 vncEncoder::Translate() 的职责。
func translateRect(fb Framebuffer, rect Rect, pf PixelFormat) []uint32 {
	rect = clampRect(rect, fb.Width(), fb.Height())
	pix := fb.ReadRect(rect)
	out := make([]uint32, len(pix))
	for i, p := range pix {
		out[i] = p.toNative(pf)
	}
	return out
}

// rectHeader 生成 12 字节的 rfbFramebufferUpdateRectHeader (大端)。
func rectHeader(rect Rect, encoding uint32) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:2], uint16(rect.X))
	binary.BigEndian.PutUint16(b[2:4], uint16(rect.Y))
	binary.BigEndian.PutUint16(b[4:6], uint16(rect.W))
	binary.BigEndian.PutUint16(b[6:8], uint16(rect.H))
	binary.BigEndian.PutUint32(b[8:12], encoding)
	return b
}

// assemble 将 12 字节头 + 负载粘合成完整更新矩形。
func assemble(rect Rect, encoding uint32, body []byte) []byte {
	out := make([]byte, 0, 12+len(body))
	out = append(out, rectHeader(rect, encoding)...)
	out = append(out, body...)
	return out
}

// writeRawPixels 按客户端像素格式把 native 值列表写为原生像素字节 (对应 PUT_PIXEL)。
func writeRawPixels(w *bytes.Buffer, vals []uint32, pf PixelFormat) {
	size := nativeSize(pf)
	be := pf.BigEndian != 0
	for _, v := range vals {
		_ = writeNativePixel(w, v, size, be)
	}
}

// EncodeRect 按 encoding 将 rect 编码为一段或多段完整更新矩形。
// 若请求的编码不被支持，则回退为 Raw。
func (e *Encoder) EncodeRect(fb Framebuffer, rect Rect, encoding uint32) [][]byte {
	rect = clampRect(rect, fb.Width(), fb.Height())
	if rect.W <= 0 || rect.H <= 0 {
		return nil
	}

	switch encoding {
	case EncodingRaw:
		return [][]byte{e.encodeRaw(fb, rect)}
	case EncodingCopyRect:
		return [][]byte{e.encodeCopyRect(rect)}
	case EncodingRRE:
		return [][]byte{e.encodeRRE(fb, rect)}
	case EncodingCoRRE:
		return e.encodeCoRRE(fb, rect)
	case EncodingHextile:
		return [][]byte{e.encodeHextile(fb, rect)}
	case EncodingZlib:
		return e.encodeZlib(fb, rect)
	case EncodingZRLE:
		return [][]byte{e.encodeZRLE(fb, rect)}
	case EncodingTight:
		return e.encodeTight(fb, rect)
	default:
		return [][]byte{e.encodeRaw(fb, rect)}
	}
}

// EncodeRectRaw 无条件使用 Raw 编码 (用于 RRE/CoRRE 回退)。
func (e *Encoder) encodeRaw(fb Framebuffer, rect Rect) []byte {
	rect = clampRect(rect, fb.Width(), fb.Height())
	vals := translateRect(fb, rect, e.PF)
	var buf bytes.Buffer
	writeRawPixels(&buf, vals, e.PF)
	return assemble(rect, EncodingRaw, buf.Bytes())
}