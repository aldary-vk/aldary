package rfb

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
)

// 本文件实现 Tight 编码，对应 UltraVNC winvnc/vncEncodeTight.cpp。
//
// Tight 是 VNC 中压缩率最高的通用编码之一，核心思路：
//   1. 将大矩形按 maxRectSize(65536)/maxRectWidth(2048) 拆分为子矩形
//   2. 每个子矩形先统计颜色：纯色(1)/双色(2)/调色板(3..256)/真彩(0)
//   3. 双色用 1bit/像素位图，调色板用 1字节索引，真彩用原始像素
//   4. 位图/索引/真彩数据分别经过 4 条独立的持久 zlib 流 (索引 0..2) 压缩
//   5. 纯色直接写填充子编码，无压缩
//
// 与 UltraVNC 的 m_conf[4] 压缩配置表一一对应。

const (
	tightMinToCompress = 12

	rfbTightExplicitFilter = 0x04
	rfbTightFill           = 0x08
	rfbTightJpeg           = 0x09
	rfbTightNoZlib         = 0x0A
	rfbTightMaxSubencoding = 0x09

	rfbTightFilterCopy     = 0x00
	rfbTightFilterPalette  = 0x01
	rfbTightFilterGradient = 0x02

	// tightJPEGMinArea 是启用 JPEG 子编码的最小矩形面积。
	// 过小的矩形受益于有损 JPEG 的幅度有限，却要付出 JPEG 头部开销与有损代价，
	// 因此面积不足时回退到无损的 gradient+raw zlib 路径。
	tightJPEGMinArea = 64
)

// tightConf 对应 TIGHT_CONF 结构体。
type tightConf struct {
	maxRectSize, maxRectWidth             int
	monoMinRectSize                       int
	idxZlibLevel, monoZlibLevel, rawZlibLevel int
	idxMaxColorsDivisor                   int
	palMaxColorsWithJPEG                  int
}

// tightConfTable 对应 vncEncodeTight::m_conf 配置表。
var tightConfTable = [4]tightConf{
	{65536, 2048, 6, 0, 0, 0, 4, 24},
	{65536, 2048, 32, 1, 1, 1, 96, 24},
	{65536, 2048, 32, 3, 3, 2, 96, 96},
	{65536, 2048, 32, 7, 7, 5, 96, 256},
}

// tightTurboLevel 对应 EncodeRect 中的 m_turboCompressLevel 映射逻辑：
// 将通用压缩级别 (0..9) 映射到 m_conf 表的 4 档之一。
func (e *Encoder) tightTurboLevel() int {
	turbo := e.CompressLevel
	if turbo > 2 {
		turbo = 2
	}
	// CL 9 映射到内部 CL 3，主要为向后兼容。
	if e.CompressLevel == 9 {
		turbo = 3
	}
	return turbo
}

// tightStream 返回 Tight 编码第 i 条持久 zlib 流，按需创建。
func (e *Encoder) tightStream(i, level int) *zlibStream {
	if e.tightEnc[i] == nil {
		e.tightEnc[i] = newZlibStream(level)
	}
	return e.tightEnc[i]
}

// tightCtx 是一个子矩形编码过程中的状态载体，对应 vncEncodeTight 的成员字段。
type tightCtx struct {
	e    *Encoder
	pf   PixelFormat
	conf tightConf
	use24 bool

	paletteNumColors int
	paletteMaxColors int
	monoBackground   uint32
	monoForeground   uint32
	pal              tightPalette
}

func (e *Encoder) encodeTight(fb Framebuffer, rect Rect) [][]byte {
	rect = clampRect(rect, fb.Width(), fb.Height())
	if rect.W <= 0 || rect.H <= 0 {
		return nil
	}
	ctx := &tightCtx{
		e:    e,
		pf:   e.PF,
		conf: tightConfTable[e.tightTurboLevel()],
		// m_usePixelFormat24 对应 remoteformat.depth==24 && rgbMax 全 0xFF。
		use24: e.PF.Depth == 24 && e.PF.RedMax == 0xFF && e.PF.GreenMax == 0xFF && e.PF.BlueMax == 0xFF,
	}
	return ctx.encodeRectSimple(fb, rect)
}

// encodeRectSimple 对应 EncodeRectSimple：将过大的矩形拆分后逐个编码子矩形。
func (ctx *tightCtx) encodeRectSimple(fb Framebuffer, rect Rect) [][]byte {
	x, y, w, h := rect.X, rect.Y, rect.W, rect.H
	maxRectSize := ctx.conf.maxRectSize
	maxRectWidth := ctx.conf.maxRectWidth

	var out [][]byte
	if w > maxRectWidth || w*h > maxRectSize {
		subrectMaxWidth := w
		if w > maxRectWidth {
			subrectMaxWidth = maxRectWidth
		}
		subrectMaxHeight := maxRectSize / subrectMaxWidth
		for dy := 0; dy < h; dy += subrectMaxHeight {
			for dx := 0; dx < w; dx += maxRectWidth {
				rw := maxRectWidth
				if dx+rw > w {
					rw = w - dx
				}
				rh := subrectMaxHeight
				if dy+rh > h {
					rh = h - dy
				}
				out = append(out, ctx.encodeSubrect(fb, Rect{X: x + dx, Y: y + dy, W: rw, H: rh}))
			}
		}
	} else {
		out = append(out, ctx.encodeSubrect(fb, rect))
	}
	return out
}

// encodeSubrect 对应 EncodeSubrect：统计颜色并选择子编码。
func (ctx *tightCtx) encodeSubrect(fb Framebuffer, rect Rect) []byte {
	data := translateRect(fb, rect, ctx.pf)
	n := len(data)
	if n == 0 {
		return nil
	}
	w, h := rect.W, rect.H

	// 矩形头 (encoding = Tight)。
	var hdr bytes.Buffer
	hdr.Write(rectHeader(rect, EncodingTight))

	// m_paletteMaxColors = w*h / idxMaxColorsDivisor。
	ctx.paletteMaxColors = n / ctx.conf.idxMaxColorsDivisor
	if ctx.paletteMaxColors < 2 && n >= ctx.conf.monoMinRectSize {
		ctx.paletteMaxColors = 2
	}

	ctx.fillPalette(data)

	var body []byte
	ok := true
	switch ctx.paletteNumColors {
	case 0:
		// 真彩矩形：客户端声明支持 JPEG 且面积足够时用有损 JPEG 子编码，
		// 否则无损 gradient + raw zlib。
		if ctx.e.jpegEnabled() && w*h >= tightJPEGMinArea {
			if body = ctx.sendJpegRect(&hdr, fb, rect, w, h); body == nil {
				body, ok = ctx.sendFullColorRect(&hdr, data, w, h)
			}
		} else {
			body, ok = ctx.sendFullColorRect(&hdr, data, w, h)
		}
	case 1:
		body = ctx.sendSolidRect(&hdr, data)
	case 2:
		body, ok = ctx.sendMonoRect(&hdr, data, w, h)
	default:
		body, ok = ctx.sendIndexedRect(&hdr, data, w, h)
	}
	if !ok {
		// 对应 EncodeSubrect 中 encDataSize < 0 时回退 Raw。
		return ctx.e.encodeRaw(fb, rect)
	}

	out := make([]byte, 0, hdr.Len()+len(body))
	out = append(out, hdr.Bytes()...)
	out = append(out, body...)
	return out
}

// fillPalette 依据客户端 bpp 分发到对应的 FillPaletteN 实现。
func (ctx *tightCtx) fillPalette(data []uint32) {
	switch ctx.pf.BitsPerPixel {
	case 8:
		ctx.fillPalette8(data)
	case 16:
		ctx.fillPalette16or32(data, 16)
	default:
		ctx.fillPalette16or32(data, 32)
	}
}

// fillPalette8 对应 FillPalette8：仅识别纯色/双色，不构建多色调色板。
func (ctx *tightCtx) fillPalette8(data []uint32) {
	ctx.paletteNumColors = 0
	count := len(data)

	c0 := data[0]
	i := 1
	for i < count && data[i] == c0 {
		i++
	}
	if i == count {
		ctx.paletteNumColors = 1
		return
	}
	if ctx.paletteMaxColors < 2 {
		return
	}

	n0 := i
	c1 := data[i]
	n1 := 0
	i++
	for i < count {
		ci := data[i]
		if ci == c0 {
			n0++
		} else if ci == c1 {
			n1++
		} else {
			break
		}
		i++
	}
	if i == count {
		if n0 > n1 {
			ctx.monoBackground, ctx.monoForeground = c0, c1
		} else {
			ctx.monoBackground, ctx.monoForeground = c1, c0
		}
		ctx.paletteNumColors = 2
	}
}

// fillPalette16or32 对应 FillPalette16 / FillPalette32 宏展开：
// 识别纯色/双色，或按像素频次构建 sorted 调色板。
func (ctx *tightCtx) fillPalette16or32(data []uint32, bpp int) {
	count := len(data)

	c0 := data[0]
	i := 1
	for i < count && data[i] == c0 {
		i++
	}
	if i >= count {
		ctx.paletteNumColors = 1
		return
	}
	if ctx.paletteMaxColors < 2 {
		ctx.paletteNumColors = 0
		return
	}

	n0 := i
	c1 := data[i]
	n1 := 0
	i++
	var ci uint32
	for i < count {
		ci = data[i]
		if ci == c0 {
			n0++
		} else if ci == c1 {
			n1++
		} else {
			break
		}
		i++
	}
	if i >= count {
		if n0 > n1 {
			ctx.monoBackground, ctx.monoForeground = c0, c1
		} else {
			ctx.monoBackground, ctx.monoForeground = c1, c0
		}
		ctx.paletteNumColors = 2
		return
	}

	ctx.pal.reset()
	ctx.pal.insert(c0, n0, bpp, ctx.paletteMaxColors)
	ctx.pal.insert(c1, n1, bpp, ctx.paletteMaxColors)

	ni := 1
	i++
	for i < count {
		if data[i] == ci {
			ni++
		} else {
			if !ctx.pal.insert(ci, ni, bpp, ctx.paletteMaxColors) {
				ctx.paletteNumColors = 0
				return
			}
			ci = data[i]
			ni = 1
		}
		i++
	}
	ctx.pal.insert(ci, ni, bpp, ctx.paletteMaxColors)
	ctx.paletteNumColors = ctx.pal.numColors
}

// sendSolidRect 对应 SendSolidRect：直接输出单个像素的填充子编码。
func (ctx *tightCtx) sendSolidRect(hdr *bytes.Buffer, data []uint32) []byte {
	var body bytes.Buffer
	tightWritePixel(&body, data[0], ctx.pf, ctx.use24)
	hdr.WriteByte(byte(rfbTightFill << 4))
	return body.Bytes()
}

// sendMonoRect 对应 SendMonoRect：1bit/像素位图 + 2 色调色板，走流 1。
func (ctx *tightCtx) sendMonoRect(hdr *bytes.Buffer, data []uint32, w, h int) ([]byte, bool) {
	const streamID = 1
	dataLen := ((w + 7) / 8) * h

	zlibLevel := ctx.conf.monoZlibLevel
	if zlibLevel == 0 {
		hdr.WriteByte(byte((rfbTightNoZlib | rfbTightExplicitFilter) << 4))
	} else {
		hdr.WriteByte(byte((streamID | rfbTightExplicitFilter) << 4))
	}
	hdr.WriteByte(rfbTightFilterPalette)
	hdr.WriteByte(1) // 2 色 - 1

	var palette bytes.Buffer
	tightWritePixel(&palette, ctx.monoBackground, ctx.pf, ctx.use24)
	tightWritePixel(&palette, ctx.monoForeground, ctx.pf, ctx.use24)
	hdr.Write(palette.Bytes())

	var bitmap bytes.Buffer
	ctx.encodeMono(&bitmap, data, w, h)

	return ctx.compressData(hdr, streamID, dataLen, zlibLevel, bitmap.Bytes())
}

// sendIndexedRect 对应 SendIndexedRect：1字节索引 + 调色板，走流 2。
func (ctx *tightCtx) sendIndexedRect(hdr *bytes.Buffer, data []uint32, w, h int) ([]byte, bool) {
	const streamID = 2
	bpp := int(ctx.pf.BitsPerPixel)

	zlibLevel := ctx.conf.idxZlibLevel
	if zlibLevel == 0 {
		hdr.WriteByte(byte((rfbTightNoZlib | rfbTightExplicitFilter) << 4))
	} else {
		hdr.WriteByte(byte((streamID | rfbTightExplicitFilter) << 4))
	}
	hdr.WriteByte(rfbTightFilterPalette)
	hdr.WriteByte(byte(ctx.paletteNumColors - 1))

	var palette bytes.Buffer
	for i := 0; i < ctx.paletteNumColors; i++ {
		tightWritePixel(&palette, ctx.pal.entry[i].listNode.rgb, ctx.pf, ctx.use24)
	}
	hdr.Write(palette.Bytes())

	var idx bytes.Buffer
	ctx.encodeIndexed(&idx, data, bpp)

	return ctx.compressData(hdr, streamID, w*h, zlibLevel, idx.Bytes())
}

// sendFullColorRect 对应 SendFullColorRect：原始像素，走流 0。
// 真彩压缩路径先做梯度过滤预处理再送 zlib，对齐 UltraVNC FilterGradient。
func (ctx *tightCtx) sendFullColorRect(hdr *bytes.Buffer, data []uint32, w, h int) ([]byte, bool) {
	const streamID = 0

	zlibLevel := ctx.conf.rawZlibLevel
	if zlibLevel == 0 {
		hdr.WriteByte(byte(rfbTightNoZlib << 4))
	} else {
		hdr.WriteByte(byte((streamID | rfbTightExplicitFilter) << 4))
	}

	var raw bytes.Buffer
	for _, v := range data {
		tightWritePixel(&raw, v, ctx.pf, ctx.use24)
	}
	rawBytes := raw.Bytes()

	if zlibLevel == 0 {
		return ctx.compressData(hdr, streamID, len(rawBytes), zlibLevel, rawBytes)
	}

	bpp := nativeSize(ctx.pf)
	if ctx.use24 {
		bpp = 3
	}
	filtered := make([]byte, len(rawBytes))
	filterGradient(filtered, rawBytes, w, h, bpp)
	hdr.WriteByte(rfbTightFilterGradient)
	return ctx.compressData(hdr, streamID, len(filtered), zlibLevel, filtered)
}

// sendJpegRect 对应 UltraVNC 的 JPEG 子编码 (compctl 高 4 位 = rfbTightJpeg)：
// 真彩矩形用有损 JPEG 压缩，压缩率高于 gradient+raw zlib，但会引入有损误差。
// 仅在客户端通过 QualityLevel/FineQualityLevel 伪编码声明支持 JPEG 时调用。
//
// 编码输出结构：compctl(0x90) + 紧凑长度 + JPEG 字节流。
// JPEG 数据始终携带紧凑长度 (客户端无法从像素格式推断其长度)。
// 返回 nil 表示 JPEG 编码失败，由调用方回退到无损路径。
func (ctx *tightCtx) sendJpegRect(hdr *bytes.Buffer, fb Framebuffer, rect Rect, w, h int) []byte {
	pix := fb.ReadRect(rect)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			p := pix[row+x]
			img.SetRGBA(x, y, color.RGBA{uint8(p.R), uint8(p.G), uint8(p.B), 0xFF})
		}
	}

	quality := ctx.e.tightQuality
	if quality < 1 {
		quality = 1 // Go 标准库 jpeg 编码器要求质量 >= 1
	}
	if quality > 100 {
		quality = 100
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil
	}

	hdr.WriteByte(byte(rfbTightJpeg << 4))
	writeTightCompactLen(hdr, buf.Len())
	return buf.Bytes()
}

// compressData 对应 CompressData + SendCompressedData：
// 小于 12 字节不写紧凑长度直接原始输出；否则按 zlibLevel 决定是否压缩并写紧凑长度。
func (ctx *tightCtx) compressData(hdr *bytes.Buffer, streamID, dataLen, zlibLevel int, data []byte) ([]byte, bool) {
	if dataLen < tightMinToCompress {
		return data[:dataLen], true
	}
	if zlibLevel == 0 {
		writeTightCompactLen(hdr, dataLen)
		return data[:dataLen], true
	}

	compressed := ctx.e.tightStream(streamID, zlibLevel).compress(data[:dataLen])
	if len(compressed) == 0 {
		return nil, false
	}
	writeTightCompactLen(hdr, len(compressed))
	return compressed, true
}

// writeTightCompactLen 对应 SendCompressedData：写出 1..3 字节紧凑长度。
func writeTightCompactLen(b *bytes.Buffer, n int) {
	if n <= 0x7F {
		b.WriteByte(byte(n))
		return
	}
	b.WriteByte(byte(n&0x7F) | 0x80)
	if n <= 0x3FFF {
		b.WriteByte(byte((n >> 7) & 0x7F))
		return
	}
	b.WriteByte(byte((n>>7)&0x7F) | 0x80)
	b.WriteByte(byte(n >> 14))
}

// tightWritePixel 输出单个像素：use24 (depth24 且 rgbMax 全 0xFF) 写 3 字节 R,G,B，
// 否则按客户端像素格式写原始字节。对应 Pack24 / 原生像素输出。
func tightWritePixel(b *bytes.Buffer, v uint32, pf PixelFormat, use24 bool) {
	if use24 {
		b.WriteByte(byte((v >> pf.RedShift) & 0xFF))
		b.WriteByte(byte((v >> pf.GreenShift) & 0xFF))
		b.WriteByte(byte((v >> pf.BlueShift) & 0xFF))
		return
	}
	_ = writeNativePixel(b, v, nativeSize(pf), pf.BigEndian != 0)
}

// filterGradient 对 w×h、每像素 bpp 字节的缓冲做 Tight 梯度过滤（编码方向）。
// 与 UltraVNC vncEncodeTight::FilterGradient 及客户端 decodeGradData 严格互逆。
// 预测值 pred 由原始像素计算：pred = clamp(left + above - upperLeft, 0, 255)，
// 残差 dst = src - pred。解码端用重建像素做同样的 clamp 求和，等价于原始像素。
func filterGradient(dst, src []byte, w, h, bpp int) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base := y*w*bpp + x*bpp
			for c := 0; c < bpp; c++ {
				var left, above, upperLeft int
				if x > 0 {
					left = int(src[base+c-bpp])
				}
				if y > 0 {
					above = int(src[base+c-w*bpp])
					if x > 0 {
						upperLeft = int(src[base+c-w*bpp-bpp])
					}
				}
				pred := left + above - upperLeft
				if pred < 0 {
					pred = 0
				} else if pred > 255 {
					pred = 255
				}
				dst[base+c] = byte(int(src[base+c]) - pred)
			}
		}
	}
}

// encodeMono 对应 EncodeMonoRect8/16/32：背景=0、前景=1，MSB 优先的位图。
func (ctx *tightCtx) encodeMono(dst *bytes.Buffer, data []uint32, w, h int) {
	bg := ctx.monoBackground
	ptr := 0
	alignedWidth := w - w%8

	for y := 0; y < h; y++ {
		x := 0
		for x < alignedWidth {
			bgBits := 0
			for bgBits < 8 {
				// 对应 C++ 的 `if (*ptr++ != bg) break;`：无论是否命中背景色，
				// 指针都要先前进一位。
				p := data[ptr]
				ptr++
				if p != bg {
					break
				}
				bgBits++
			}
			if bgBits == 8 {
				dst.WriteByte(0)
				x += 8
				continue
			}
			mask := uint(0x80) >> bgBits
			value := mask
			bgBits++
			for bgBits < 8 {
				mask >>= 1
				if data[ptr] != bg {
					value |= mask
				}
				ptr++
				bgBits++
			}
			dst.WriteByte(byte(value))
			x += 8
		}

		mask := uint(0x80)
		value := uint(0)
		if x >= w {
			continue
		}
		for x < w {
			if data[ptr] != bg {
				value |= mask
			}
			mask >>= 1
			ptr++
			x++
		}
		dst.WriteByte(byte(value))
	}
}

// encodeIndexed 对应 EncodeIndexedRect16/32：将像素替换为调色板索引 (1字节/像素)。
func (ctx *tightCtx) encodeIndexed(dst *bytes.Buffer, data []uint32, bpp int) {
	count := len(data)
	rep := 0
	for count > 0 {
		rgb := data[len(data)-count]
		count--
		for count > 0 && data[len(data)-count] == rgb {
			rep++
			count--
		}
		pnode := ctx.pal.hash[ctx.pal.hashKey(rgb, bpp)]
		for pnode != nil {
			if pnode.rgb == rgb {
				dst.WriteByte(byte(pnode.idx))
				for rep > 0 {
					dst.WriteByte(byte(pnode.idx))
					rep--
				}
				break
			}
			pnode = pnode.next
		}
	}
}

// tightColorList 对应 COLOR_LIST。
type tightColorList struct {
	next *tightColorList
	idx  int
	rgb  uint32
}

// tightPaletteEntry 对应 PALETTE_ENTRY。
type tightPaletteEntry struct {
	listNode  *tightColorList
	numPixels int
}

// tightPalette 对应 PALETTE：按像素频次排序的调色板 + 哈希查找表。
type tightPalette struct {
	entry     [256]tightPaletteEntry
	hash      [256]*tightColorList
	list      [256]tightColorList
	numColors int
}

func (p *tightPalette) reset() {
	p.numColors = 0
	for i := range p.hash {
		p.hash[i] = nil
	}
}

func (p *tightPalette) hashKey(rgb uint32, bpp int) int {
	if bpp == 16 {
		return int(((rgb >> 8) + rgb) & 0xFF)
	}
	return int(((rgb >> 16) + (rgb >> 8)) & 0xFF)
}

// insert 对应 PaletteInsert：按 numPixels 递减插入/更新调色板条目。
func (p *tightPalette) insert(rgb uint32, numPixels, bpp, maxColors int) bool {
	hashKey := p.hashKey(rgb, bpp)
	pnode := p.hash[hashKey]
	var prev *tightColorList

	for pnode != nil {
		if pnode.rgb == rgb {
			newIdx := pnode.idx
			idx := newIdx
			count := p.entry[idx].numPixels + numPixels
			if newIdx != 0 && p.entry[newIdx-1].numPixels < count {
				for {
					p.entry[newIdx] = p.entry[newIdx-1]
					p.entry[newIdx].listNode.idx = newIdx
					newIdx--
					if newIdx == 0 || p.entry[newIdx-1].numPixels >= count {
						break
					}
				}
				p.entry[newIdx].listNode = pnode
				pnode.idx = newIdx
			}
			p.entry[newIdx].numPixels = count
			return true
		}
		prev = pnode
		pnode = pnode.next
	}

	if p.numColors == 256 || p.numColors == maxColors {
		p.numColors = 0
		return false
	}

	idx := p.numColors
	for idx > 0 && p.entry[idx-1].numPixels < numPixels {
		p.entry[idx] = p.entry[idx-1]
		p.entry[idx].listNode.idx = idx
		idx--
	}

	pnode = &p.list[p.numColors]
	if prev != nil {
		prev.next = pnode
	} else {
		p.hash[hashKey] = pnode
	}
	pnode.next = nil
	pnode.idx = idx
	pnode.rgb = rgb
	p.entry[idx].listNode = pnode
	p.entry[idx].numPixels = numPixels

	p.numColors++
	return true
}