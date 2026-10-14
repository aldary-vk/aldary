package rfb

import (
	"bytes"
	"compress/zlib"
	"image/jpeg"
	"io"
	"testing"
)

// 本文件对 Tight 编码做独立解码 round-trip 验证。
// Tight 是压缩率最高的通用编码之一，其解码涉及 4 条独立的持久 zlib 流、
// 纯色/单色/调色板/真彩子编码、紧凑长度编码与调色板过滤器，
// 这里对照 UltraVNC vncviewer/ClientConnectionTight.cpp 的解码逻辑逐行复刻，
// 以证明服务端编码输出可被标准客户端正确还原。

// tightTestRect 记录一个 Tight 子矩形在解压前解析出的元信息。
type tightTestRect struct {
	x, y, w, h int
	streamID   int
	dataSize   int
	filter     int
	numColors  int
	palette    []pixel
	raw        []byte // dataSize < TIGHT_MIN_TO_COMPRESS 时内联的原始数据
	compressed bool
}

// readTightCompactLen 对应 ReadCompactLen：解析 1..3 字节紧凑长度。
func readTightCompactLen(b []byte) (int, int) {
	if len(b) < 1 {
		return -1, 0
	}
	n := int(b[0] & 0x7F)
	used := 1
	if b[0]&0x80 != 0 {
		if len(b) < 2 {
			return -1, 0
		}
		n |= int(b[1]&0x7F) << 7
		used = 2
		if b[1]&0x80 != 0 {
			if len(b) < 3 {
				return -1, 0
			}
			n |= int(b[2]) << 14
			used = 3
		}
	}
	return n, used
}

// readTightPixel 解析一个 Tight 像素：use24 (depth24 且 8bit 通道) 为 3 字节 RGB，
// 否则按客户端像素格式读原始字节，返回 native 值。
func readTightPixel(t *testing.T, body []byte, pos *int, pf PixelFormat, use24 bool) uint32 {
	t.Helper()
	if use24 {
		if *pos+3 > len(body) {
			t.Fatal("short tight pixel (24bpp)")
		}
		r := uint32(body[*pos])
		g := uint32(body[*pos+1])
		b := uint32(body[*pos+2])
		*pos += 3
		return (r << pf.RedShift) | (g << pf.GreenShift) | (b << pf.BlueShift)
	}
	bpp := pf.BytesPerPixel()
	if *pos+bpp > len(body) {
		t.Fatal("short tight pixel")
	}
	v := bytesToNativePixel(body, *pos, bpp, pf.BigEndian != 0)
	*pos += bpp
	return v
}

// fillRectPixels 将纯色写入输出帧缓冲对应区域。
func fillRectPixels(out []pixel, fbW, x, y, w, h int, c pixel) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			out[(y+j)*fbW+(x+i)] = c
		}
	}
}

// unfilterGradient 是 filterGradient 的严格逆运算（解码方向），
// 与 UltraVNC 客户端 decodeGradData 逻辑一致：pred = clamp(above + left - upperLeft, 0, 255)，
// dst = src + pred。
func unfilterGradient(dst, src []byte, w, h, bpp int) {
	prevRow := make([]byte, w*bpp+bpp)
	thisRow := make([]byte, w*bpp+bpp)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base := x * bpp
			for c := 0; c < bpp; c++ {
				pred := int(prevRow[base+bpp+c]) + int(thisRow[base+c]) - int(prevRow[base+c])
				if pred < 0 {
					pred = 0
				} else if pred > 255 {
					pred = 255
				}
				thisRow[base+bpp+c] = byte(int(src[y*w*bpp+base+c]) + pred)
			}
		}
		copy(dst[y*w*bpp:], thisRow[bpp:])
		prevRow, thisRow = thisRow, prevRow
	}
}

// applyTightFilter 对照 ClientConnectionTight 的过滤器，把解压后的原始字节还原为像素。
func applyTightFilter(data []byte, info tightTestRect, pf PixelFormat, use24 bool, bpp int) []pixel {
	w, h := info.w, info.h
	out := make([]pixel, w*h)
	switch info.filter {
	case rfbTightFilterCopy:
		for i := 0; i < w*h; i++ {
			var v uint32
			if use24 {
				r := uint32(data[i*3])
				g := uint32(data[i*3+1])
				b := uint32(data[i*3+2])
				v = (r << pf.RedShift) | (g << pf.GreenShift) | (b << pf.BlueShift)
			} else {
				v = bytesToNativePixel(data, i*bpp, bpp, pf.BigEndian != 0)
			}
			out[i] = rgbFromPF(v, pf)
		}
	case rfbTightFilterPalette:
		if info.numColors == 2 {
			// 1bit/像素位图，MSB 优先。
			rowBytes := (w + 7) / 8
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					bit := (data[y*rowBytes+x/8] >> uint(7-x%8)) & 1
					out[y*w+x] = info.palette[bit]
				}
			}
		} else {
			for i := 0; i < w*h; i++ {
				out[i] = info.palette[int(data[i])]
			}
		}
	case rfbTightFilterGradient:
		gbpp := bpp
		if use24 {
			gbpp = 3
		}
		buf := make([]byte, w*h*gbpp)
		unfilterGradient(buf, data, w, h, gbpp)
		for i := 0; i < w*h; i++ {
			var v uint32
			if use24 {
				r := uint32(buf[i*3])
				g := uint32(buf[i*3+1])
				b := uint32(buf[i*3+2])
				v = (r << pf.RedShift) | (g << pf.GreenShift) | (b << pf.BlueShift)
			} else {
				v = bytesToNativePixel(buf, i*gbpp, gbpp, pf.BigEndian != 0)
			}
			out[i] = rgbFromPF(v, pf)
		}
	default:
		// 本实现不产生其他过滤器。
		for i := range out {
			out[i] = pixel{}
		}
	}
	return out
}

// decodeTightRects 解码整段 Tight 更新矩形流，返回按 (x,y)->pixel 拼接的全帧。
func decodeTightRects(t *testing.T, rects [][]byte, pf PixelFormat, fbW, fbH int) []pixel {
	t.Helper()
	bpp := pf.BytesPerPixel()
	use24 := pf.Depth == 24 && pf.RedMax == 0xFF && pf.GreenMax == 0xFF && pf.BlueMax == 0xFF

	out := make([]pixel, fbW*fbH)
	infos := make([]tightTestRect, 0, len(rects))
	streamBytes := [4]*bytes.Buffer{
		{},
		{},
		{},
		{},
	}

	for _, r := range rects {
		x, y, w, h, enc, body := readRectHeader(t, r)
		if enc != EncodingTight {
			t.Fatalf("expected Tight encoding %d, got %d", EncodingTight, enc)
		}
		if len(body) < 1 {
			t.Fatal("empty Tight body")
		}
		compCtl := body[0]
		pos := 1
		sub := compCtl >> 4

		if sub == rfbTightFill {
			pv := readTightPixel(t, body, &pos, pf, use24)
			fillRectPixels(out, fbW, x, y, w, h, rgbFromPF(pv, pf))
			continue
		}
		if sub == rfbTightJpeg {
			compressedLen, n := readTightCompactLen(body[pos:])
			if n <= 0 {
				t.Fatal("bad Tight JPEG compact length")
			}
			pos += n
			if pos+compressedLen > len(body) {
				t.Fatalf("Tight JPEG len %d exceeds body %d", compressedLen, len(body)-pos)
			}
			img, err := jpeg.Decode(bytes.NewReader(body[pos : pos+compressedLen]))
			if err != nil {
				t.Fatalf("jpeg decode: %v", err)
			}
			for j := 0; j < h; j++ {
				for i := 0; i < w; i++ {
					r, g, b, _ := img.At(i, j).RGBA()
					out[(y+j)*fbW+(x+i)] = pixel{r >> 8, g >> 8, b >> 8}
				}
			}
			continue
		}

		streamID := int(sub & 0x03)
		filter := rfbTightFilterCopy
		if sub&rfbTightExplicitFilter != 0 {
			filter = int(body[pos])
			pos++
		}

		var numColors int
		var palette []pixel
		if filter == rfbTightFilterPalette {
			numColors = int(body[pos]) + 1
			pos++
			palette = make([]pixel, numColors)
			for i := 0; i < numColors; i++ {
				palette[i] = rgbFromPF(readTightPixel(t, body, &pos, pf, use24), pf)
			}
		}

		bitsPixel := 0
		switch filter {
		case rfbTightFilterCopy, rfbTightFilterGradient:
			if use24 {
				bitsPixel = 24
			} else {
				bitsPixel = bpp * 8
			}
		case rfbTightFilterPalette:
			if numColors == 2 {
				bitsPixel = 1
			} else {
				bitsPixel = 8
			}
		default:
			t.Fatalf("unexpected Tight filter %d", filter)
		}

		rowSize := (w*bitsPixel + 7) / 8
		dataSize := h * rowSize
		info := tightTestRect{x: x, y: y, w: w, h: h, streamID: streamID, dataSize: dataSize, filter: filter, numColors: numColors, palette: palette}

		if dataSize < tightMinToCompress {
			info.raw = body[pos : pos+dataSize]
		} else {
			compressedLen, n := readTightCompactLen(body[pos:])
			if n <= 0 {
				t.Fatal("bad Tight compact length")
			}
			pos += n
			if pos+compressedLen > len(body) {
				t.Fatalf("Tight compressed len %d exceeds body %d", compressedLen, len(body)-pos)
			}
			streamBytes[streamID].Write(body[pos : pos+compressedLen])
			info.compressed = true
		}
		infos = append(infos, info)
	}

	// 4 条独立的持久 inflate 流，按流号分别串联解压。
	readers := [4]io.Reader{nil, nil, nil, nil}
	for i := 0; i < 4; i++ {
		if streamBytes[i].Len() > 0 {
			zr, err := zlib.NewReader(streamBytes[i])
			if err != nil {
				t.Fatalf("stream %d: %v", i, err)
			}
			readers[i] = zr
		}
	}

	for _, info := range infos {
		decompressed := info.raw
		if info.compressed {
			decompressed = make([]byte, info.dataSize)
			if _, err := io.ReadFull(readers[info.streamID], decompressed); err != nil {
				t.Fatalf("stream %d decompress: %v", info.streamID, err)
			}
		}
		px := applyTightFilter(decompressed, info, pf, use24, bpp)
		for j := 0; j < info.h; j++ {
			for i := 0; i < info.w; i++ {
				out[(info.y+j)*fbW+(info.x+i)] = px[j*info.w+i]
			}
		}
	}
	return out
}

// tightRoundTrip 通用校验：编码整帧后解码并与源帧缓冲逐像素比较。
func tightRoundTrip(t *testing.T, fb *MemoryFramebuffer, pf PixelFormat, level int, name string) {
	t.Helper()
	e := NewEncoder(pf, level)
	rects := e.EncodeRect(fb, Rect{0, 0, fb.Width(), fb.Height()}, EncodingTight)
	if len(rects) == 0 {
		t.Fatalf("%s: no rects produced", name)
	}
	got := decodeTightRects(t, rects, pf, fb.Width(), fb.Height())
	for y := 0; y < fb.Height(); y++ {
		for x := 0; x < fb.Width(); x++ {
			if got[y*fb.Width()+x] != fb.GetPixel(x, y) {
				t.Fatalf("%s: pixel (%d,%d) = %v != %v", name, x, y, got[y*fb.Width()+x], fb.GetPixel(x, y))
			}
		}
	}
}

func TestTightSolidRect(t *testing.T) {
	fb := NewMemoryFramebuffer(32, 24)
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			fb.SetPixel(x, y, pixel{20, 120, 240})
		}
	}
	tightRoundTrip(t, fb, DefaultPixelFormat(), 6, "Tight solid")
}

func TestTightMonoRect(t *testing.T) {
	fb := NewMemoryFramebuffer(40, 20)
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := pixel{0, 0, 0}
			if x >= 20 {
				c = pixel{255, 255, 255}
			}
			fb.SetPixel(x, y, c)
		}
	}
	tightRoundTrip(t, fb, DefaultPixelFormat(), 6, "Tight mono")
}

func TestTightIndexedRect(t *testing.T) {
	fb := NewMemoryFramebuffer(40, 20)
	colors := []pixel{
		{255, 0, 0},
		{0, 255, 0},
		{0, 0, 255},
		{255, 255, 0},
		{255, 0, 255},
	}
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			fb.SetPixel(x, y, colors[(x/8)%len(colors)])
		}
	}
	tightRoundTrip(t, fb, DefaultPixelFormat(), 6, "Tight indexed")
}

func TestTightFullColorRect(t *testing.T) {
	// 大量唯一颜色，触发调色板溢出 -> 真彩 (raw zlib) 子编码。
	fb := NewMemoryFramebuffer(64, 64)
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			fb.SetPixel(x, y, pixel{uint32(x * 4), uint32(y * 4), uint32((x + y) * 2)})
		}
	}
	tightRoundTrip(t, fb, DefaultPixelFormat(), 6, "Tight fullcolor")
}

func TestTightFullColorGradient(t *testing.T) {
	// 伪随机高频内容，梯度预测 pred = left+above-upperLeft 会频繁越出 [0,255]，
	// 用于验证编码端与解码端的 clamp 严格互逆、无损还原。
	fb := NewMemoryFramebuffer(64, 48)
	state := uint32(0x12345678)
	next := func() uint32 {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		return state
	}
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			v := next()
			fb.SetPixel(x, y, pixel{v & 0xFF, (v >> 8) & 0xFF, (v >> 16) & 0xFF})
		}
	}
	tightRoundTrip(t, fb, DefaultPixelFormat(), 6, "Tight fullcolor gradient")
}

func TestTightMono16bpp(t *testing.T) {
	// 16bpp 客户端像素格式 (RGB565，小端) 下的单色矩形。
	pf := PixelFormat{BitsPerPixel: 16, Depth: 16, BigEndian: 0, TrueColour: 1,
		RedMax: 31, GreenMax: 63, BlueMax: 31, RedShift: 11, GreenShift: 5, BlueShift: 0}
	fb := NewMemoryFramebuffer(33, 17)
	for y := 0; y < 17; y++ {
		for x := 0; x < 33; x++ {
			c := pixel{255, 0, 0}
			if x < 16 {
				c = pixel{0, 0, 255}
			}
			fb.SetPixel(x, y, c)
		}
	}
	tightRoundTrip(t, fb, pf, 6, "Tight mono 16bpp")
}

func TestTightSplitLargeRect(t *testing.T) {
	// 宽度超过 maxRectWidth(2048)，验证矩形拆分与多子矩形解码。
	fb := NewPatternFramebuffer(2100, 40)
	tightRoundTrip(t, fb, DefaultPixelFormat(), 6, "Tight split")
}

func TestTightPseudoEncoding(t *testing.T) {
	e := NewEncoder(DefaultPixelFormat(), 6)
	if e.tightQuality != -1 {
		t.Fatalf("default tightQuality should be -1, got %d", e.tightQuality)
	}

	// QualityLevel 8 -> 质量 85
	if ok, _ := setTightPseudoEncoding(e, EncodingQualityLevel0+8); !ok || e.tightQuality != 85 {
		t.Fatalf("QualityLevel8 => (ok=%v, quality=%d), want (true, 85)", ok, e.tightQuality)
	}
	// CompressLevel 2 -> 覆盖 zlib 压缩级别
	if ok, _ := setTightPseudoEncoding(e, EncodingCompressLevel0+2); !ok || e.CompressLevel != 2 {
		t.Fatalf("CompressLevel2 => (ok=%v, level=%d), want (true, 2)", ok, e.CompressLevel)
	}
	// FineQualityLevel 80 -> 精确质量 80 (覆盖之前的 QualityLevel)
	if ok, _ := setTightPseudoEncoding(e, EncodingFineQualityLevel0+80); !ok || e.tightQuality != 80 {
		t.Fatalf("FineQualityLevel80 => (ok=%v, quality=%d), want (true, 80)", ok, e.tightQuality)
	}
	// Subsamp2X -> 子采样索引 2
	if ok, _ := setTightPseudoEncoding(e, EncodingSubsamp2X); !ok || e.tightSubsampling != 2 {
		t.Fatalf("Subsamp2X => (ok=%v, subsampling=%d), want (true, 2)", ok, e.tightSubsampling)
	}
	// 非 Tight 伪编码不应命中
	if ok, _ := setTightPseudoEncoding(e, EncodingLastRect); ok {
		t.Fatal("EncodingLastRect should not be parsed as Tight pseudo-encoding")
	}
	// 边界：QualityLevel0 -> 质量 5
	if ok, _ := setTightPseudoEncoding(e, EncodingQualityLevel0); !ok || e.tightQuality != 5 {
		t.Fatalf("QualityLevel0 => (ok=%v, quality=%d), want (true, 5)", ok, e.tightQuality)
	}
}

func TestTightJpegRect(t *testing.T) {
	// 平滑渐变触发真彩子编码 (大量唯一颜色)，且空间上平滑以控制 JPEG 有损误差。
	fb := NewMemoryFramebuffer(128, 96)
	for y := 0; y < 96; y++ {
		for x := 0; x < 128; x++ {
			fb.SetPixel(x, y, pixel{
				uint32(x * 255 / 127),
				uint32(y * 255 / 95),
				uint32((x + y) * 255 / 222),
			})
		}
	}
	pf := DefaultPixelFormat()
	e := NewEncoder(pf, 6)
	e.tightQuality = 90 // 模拟客户端 QualityLevel 9 -> 95，这里用质量 90

	rects := e.EncodeRect(fb, Rect{0, 0, 128, 96}, EncodingTight)
	if len(rects) == 0 {
		t.Fatal("no rects produced")
	}
	// 面积小于 maxRectSize 且宽度小于 maxRectWidth，应得到单一子矩形。
	if len(rects) != 1 {
		t.Fatalf("expected 1 sub-rect, got %d", len(rects))
	}
	// 13 字节处为 compression-control byte，其高 4 位应为 rfbTightJpeg。
	if got := uint8(rects[0][12]) >> 4; got != rfbTightJpeg {
		t.Fatalf("expected JPEG subencoding 0x%x, got 0x%x", rfbTightJpeg, got)
	}

	got := decodeTightRects(t, rects, pf, 128, 96)
	// JPEG 有损：逐通道误差应在容差内 (quality 90 平滑渐变下抖动很小)。
	for y := 0; y < 96; y++ {
		for x := 0; x < 128; x++ {
			want := fb.GetPixel(x, y)
			g := got[y*128+x]
			if diff(want.R, g.R) > 40 || diff(want.G, g.G) > 40 || diff(want.B, g.B) > 40 {
				t.Fatalf("JPEG pixel (%d,%d) = %v too far from %v", x, y, g, want)
			}
		}
	}
}

// diff 返回两个无符号整数的绝对差。
func diff(a, b uint32) uint32 {
	if a >= b {
		return a - b
	}
	return b - a
}

// TestTightJpegDisabledByDefault 验证未声明 JPEG 时保持无损 gradient+raw zlib 子编码。
func TestTightJpegDisabledByDefault(t *testing.T) {
	fb := NewMemoryFramebuffer(128, 96)
	for y := 0; y < 96; y++ {
		for x := 0; x < 128; x++ {
			fb.SetPixel(x, y, pixel{
				uint32(x*2) & 0xff,
				uint32(y*3) & 0xff,
				uint32((x+y)*5) & 0xff,
			})
		}
	}
	pf := DefaultPixelFormat()
	e := NewEncoder(pf, 6) // tightQuality 默认 -1，应禁用 JPEG

	rects := e.EncodeRect(fb, Rect{0, 0, 128, 96}, EncodingTight)
	if len(rects) != 1 {
		t.Fatalf("expected 1 sub-rect, got %d", len(rects))
	}
	if got := uint8(rects[0][12]) >> 4; got == rfbTightJpeg {
		t.Fatalf("JPEG should be disabled by default, but got subencoding 0x%x", got)
	}
	// 无损路径 round-trip 精确相等。
	got := decodeTightRects(t, rects, pf, 128, 96)
	for y := 0; y < 96; y++ {
		for x := 0; x < 128; x++ {
			if got[y*128+x] != fb.GetPixel(x, y) {
				t.Fatalf("pixel (%d,%d) = %v != %v", x, y, got[y*128+x], fb.GetPixel(x, y))
			}
		}
	}
}