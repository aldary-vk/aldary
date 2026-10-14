package rfb

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"testing"
)

// 本文件对 RRE / CoRRE / Zlib 编码做独立解码 round-trip 验证，
// 以证明服务端编码输出可被任意符合 RFC 6143 的客户端正确还原。
// (Raw / Hextile / ZRLE 已通过 vncdotool 真实客户端逐像素验证通过，
//  故此处仅覆盖无法用 vncdotool 解码的 CoRRE 与 Zlib，RRE 作为对照。)

// testFramebuffer 构造一个确定性的测试帧缓冲：纯色背景 + 若干纯色矩形，
// 以充分触发 RRE/CoRRE 的背景色与子矩形编码逻辑。
func testFramebuffer() *MemoryFramebuffer {
	fb := NewMemoryFramebuffer(41, 33)
	bg := pixel{200, 200, 200}
	for y := 0; y < 33; y++ {
		for x := 0; x < 41; x++ {
			fb.SetPixel(x, y, bg)
		}
	}
	fill := func(x0, y0, w, h int, p pixel) {
		for y := y0; y < y0+h; y++ {
			for x := x0; x < x0+w; x++ {
				fb.SetPixel(x, y, p)
			}
		}
	}
	fill(2, 2, 9, 7, pixel{255, 0, 0})
	fill(12, 5, 9, 11, pixel{0, 255, 0})
	fill(5, 20, 11, 9, pixel{0, 0, 255})
	return fb
}

// expectPixels 校验解码结果与帧缓冲逐像素一致。
func expectPixels(t *testing.T, fb *MemoryFramebuffer, got []pixel, w, h int, name string) {
	t.Helper()
	if w*h != 41*33 || len(got) != 41*33 {
		t.Fatalf("%s: wrong dimensions %dx%d", name, w, h)
	}
	for i := 0; i < len(got); i++ {
		x, y := i%w, i/w
		if got[i] != fb.GetPixel(x, y) {
			t.Fatalf("%s: pixel (%d,%d) = %v != %v", name, x, y, got[i], fb.GetPixel(x, y))
		}
	}
}

// readRectHeader 解析 12 字节更新矩形头，返回几何、编码与负载。
func readRectHeader(t *testing.T, data []byte) (x, y, w, h int, enc uint32, body []byte) {
	t.Helper()
	if len(data) < 12 {
		t.Fatalf("rect data too short: %d", len(data))
	}
	x = int(binary.BigEndian.Uint16(data[0:2]))
	y = int(binary.BigEndian.Uint16(data[2:4]))
	w = int(binary.BigEndian.Uint16(data[4:6]))
	h = int(binary.BigEndian.Uint16(data[6:8]))
	enc = binary.BigEndian.Uint32(data[8:12])
	return x, y, w, h, enc, data[12:]
}

// decodeRRECoRRE 解码一个 RRE (corre=false) 或 CoRRE (corre=true) 矩形负载。
func decodeRRECoRRE(t *testing.T, body []byte, w, h int, pf PixelFormat, corre bool) []pixel {
	t.Helper()
	bpp := pf.BytesPerPixel()
	be := pf.BigEndian != 0
	if len(body) < 4+bpp {
		t.Fatalf("RRE body too short: %d", len(body))
	}
	n := binary.BigEndian.Uint32(body[0:4])
	pos := 4 + bpp
	bg := bytesToNativePixel(body, 4, bpp, be)

	out := make([]pixel, w*h)
	rgb := rgbFromPF(bg, pf)
	for i := range out {
		out[i] = rgb
	}

	geo := 8
	if corre {
		geo = 4
	}
	for i := uint32(0); i < n; i++ {
		if pos+bpp+geo > len(body) {
			t.Fatalf("RRE subrect %d out of bounds: pos=%d len=%d", i, pos, len(body))
		}
		cl := bytesToNativePixel(body, pos, bpp, be)
		pos += bpp
		var sx, sy, sw, sh int
		if corre {
			sx, sy, sw, sh = int(body[pos]), int(body[pos+1]), int(body[pos+2]), int(body[pos+3])
			pos += 4
		} else {
			sx = int(binary.BigEndian.Uint16(body[pos : pos+2]))
			sy = int(binary.BigEndian.Uint16(body[pos+2 : pos+4]))
			sw = int(binary.BigEndian.Uint16(body[pos+4 : pos+6]))
			sh = int(binary.BigEndian.Uint16(body[pos+6 : pos+8]))
			pos += 8
		}
		c := rgbFromPF(cl, pf)
		for jj := sy; jj < sy+sh; jj++ {
			for ii := sx; ii < sx+sw; ii++ {
				if jj < h && ii < w {
					out[jj*w+ii] = c
				}
			}
		}
	}
	return out
}

func TestEncodeRRERoundTrip(t *testing.T) {
	fb := testFramebuffer()
	e := NewEncoder(DefaultPixelFormat(), 6)
	rects := e.EncodeRect(fb, Rect{0, 0, 41, 33}, EncodingRRE)
	if len(rects) != 1 {
		t.Fatalf("expected 1 rect, got %d", len(rects))
	}
	x, y, w, h, enc, body := readRectHeader(t, rects[0])
	if enc != EncodingRRE || x != 0 || y != 0 {
		t.Fatalf("bad header: enc=%d x=%d y=%d", enc, x, y)
	}
	got := decodeRRECoRRE(t, body, w, h, DefaultPixelFormat(), false)
	expectPixels(t, fb, got, w, h, "RRE")
}

func TestEncodeCoRRERoundTrip(t *testing.T) {
	fb := testFramebuffer()
	e := NewEncoder(DefaultPixelFormat(), 6)
	rects := e.EncodeRect(fb, Rect{0, 0, 41, 33}, EncodingCoRRE)

	decoded := make([]pixel, 41*33)
	covered := make([]bool, 41*33)
	for _, r := range rects {
		x, y, w, h, enc, body := readRectHeader(t, r)
		if enc != EncodingCoRRE {
			t.Fatalf("expected CoRRE encoding, got %d", enc)
		}
		sub := decodeRRECoRRE(t, body, w, h, DefaultPixelFormat(), true)
		for j := 0; j < h; j++ {
			for i := 0; i < w; i++ {
				idx := (y+j)*41 + (x + i)
				decoded[idx] = sub[j*w+i]
				covered[idx] = true
			}
		}
	}
	for i, c := range covered {
		if !c {
			t.Fatalf("pixel %d not covered by CoRRE rects", i)
		}
	}
	expectPixels(t, fb, decoded, 41, 33, "CoRRE")
}

func TestEncodeZlibRoundTrip(t *testing.T) {
	fb := testFramebuffer()
	e := NewEncoder(DefaultPixelFormat(), 6)
	rects := e.EncodeRect(fb, Rect{0, 0, 41, 33}, EncodingZlib)

	pf := DefaultPixelFormat()
	bpp := pf.BytesPerPixel()
	be := pf.BigEndian != 0

	// 服务端在一条持久 zlib 流上按矩形顺序 Z_SYNC_FLUSH，各块压缩数据拼接后
	// 仍是同一条 deflate 流的连续片段，客户端用一条 inflate 流顺序解压。
	var stream bytes.Buffer
	for _, r := range rects {
		_, _, _, _, _, body := readRectHeader(t, r)
		if len(body) < 4 {
			t.Fatal("zlib body too short")
		}
		clen := int(binary.BigEndian.Uint32(body[0:4]))
		if 4+clen > len(body) {
			t.Fatalf("zlib length %d exceeds body %d", clen, len(body))
		}
		stream.Write(body[4 : 4+clen])
	}

	zr, err := zlib.NewReader(&stream)
	if err != nil {
		t.Fatal(err)
	}

	decoded := make([]pixel, 41*33)
	covered := make([]bool, 41*33)
	for _, r := range rects {
		x, y, w, h, _, _ := readRectHeader(t, r)
		rawLen := w * h * bpp
		raw := make([]byte, rawLen)
		if _, err := io.ReadFull(zr, raw); err != nil {
			t.Fatalf("decompress rect (%d,%d %dx%d): %v", x, y, w, h, err)
		}
		for j := 0; j < h; j++ {
			for i := 0; i < w; i++ {
				v := bytesToNativePixel(raw, (j*w+i)*bpp, bpp, be)
				idx := (y+j)*41 + (x + i)
				decoded[idx] = rgbFromPF(v, pf)
				covered[idx] = true
			}
		}
	}
	for i, c := range covered {
		if !c {
			t.Fatalf("pixel %d not covered by zlib rects", i)
		}
	}
	expectPixels(t, fb, decoded, 41, 33, "Zlib")
}