package rfb

import (
	"bytes"
	"encoding/binary"
)

// 本文件实现 RRE (Rise-and-Run-length) 与 CoRRE (Compact RRE) 编码，
// 对应 UltraVNC vncencoderre.cpp 与 vncencodecorre.cpp。
//
// 编码结构为 [背景像素][子矩形...]，每个子矩形为 [像素][几何]。
// RRE 的几何信息为 4 个 U16；CoRRE 的几何信息为 4 个 U8 (因此矩形必须 ≤255x255)。

func (e *Encoder) encodeRRE(fb Framebuffer, rect Rect) []byte {
	rect = clampRect(rect, fb.Width(), fb.Height())
	vals := translateRect(fb, rect, e.PF)
	body, n, ok := rreSubrects(vals, rect.W, rect.H, e.PF, false)
	if !ok {
		return e.encodeRaw(fb, rect)
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(n))
	return assemble(rect, EncodingRRE, append(hdr, body...))
}

// encodeCoRRE 将大矩形拆分为 ≤m_maxWidth×m_maxHeight 的小矩形分别编码。
// 与 UltraVNC 初始状态一致，m_maxWidth=m_maxHeight=24。
func (e *Encoder) encodeCoRRE(fb Framebuffer, rect Rect) [][]byte {
	rect = clampRect(rect, fb.Width(), fb.Height())
	const maxW, maxH = 24, 24
	out := make([][]byte, 0, 8)
	for y := rect.Y; y < rect.Y+rect.H; y += maxH {
		subH := maxH
		if y+subH > rect.Y+rect.H {
			subH = rect.Y + rect.H - y
		}
		for x := rect.X; x < rect.X+rect.W; x += maxW {
			subW := maxW
			if x+subW > rect.X+rect.W {
				subW = rect.X + rect.W - x
			}
			sub := Rect{X: x, Y: y, W: subW, H: subH}
			out = append(out, e.encodeCoRRESmall(fb, sub))
		}
	}
	return out
}

// encodeCoRRESmall 编码一个 ≤255x255 的矩形。
func (e *Encoder) encodeCoRRESmall(fb Framebuffer, rect Rect) []byte {
	rect = clampRect(rect, fb.Width(), fb.Height())
	vals := translateRect(fb, rect, e.PF)
	body, n, ok := rreSubrects(vals, rect.W, rect.H, e.PF, true)
	if !ok {
		return e.encodeRaw(fb, rect)
	}
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, uint32(n))
	return assemble(rect, EncodingCoRRE, append(hdr, body...))
}

// rreSubrects 实现 subrectEncode## 核心算法，与 vncencoderre.cpp / vncencodecorre.cpp 一致：
// 逐像素找背景色以外的单色最大水平或垂直子矩形，编码后在原缓冲区标记为背景色。
// 返回负载 (背景像素 + 子矩形序列) 与子矩形数量；若编码后体积超过原始像素体积则返回 ok=false。
func rreSubrects(vals []uint32, w, h int, pf PixelFormat, corre bool) ([]byte, int, bool) {
	bpp := pf.BytesPerPixel()
	if len(vals) != w*h || w <= 0 || h <= 0 {
		return nil, 0, false
	}

	bg := getBgColour(vals, bpp)

	var buf bytes.Buffer
	be := pf.BigEndian != 0
	_ = writeNativePixel(&buf, bg, bpp, be)
	curLen := bpp

	rectSize := 8
	if corre {
		rectSize = 4
	}

	numsubs := 0
	for y := 0; y < h; y++ {
		line := vals[y*w : (y+1)*w]
		for x := 0; x < w; x++ {
			if line[x] == bg {
				continue
			}
			cl := line[x]
			hy := y - 1
			hyflag := true
			var j int
			hx, vx, vy := 0, 0, 0
			for j = y; j < h; j++ {
				seg := vals[j*w : (j+1)*w]
				if seg[x] != cl {
					break
				}
				i := x
				for i < w && seg[i] == cl {
					i++
				}
				i--
				if j == y {
					vx = i
					hx = i
				}
				if i < vx {
					vx = i
				}
				if hyflag && i >= hx {
					hy++
				} else {
					hyflag = false
				}
			}
			vy = j - 1

			hw := hx - x + 1
			hh := hy - y + 1
			vw := vx - x + 1
			vh := vy - y + 1

			thew, theh := hw, hh
			if vw*vh > hw*hh {
				thew, theh = vw, vh
			}
			thex, they := x, y

			newLen := curLen + bpp + rectSize
			if newLen > w*h*bpp {
				return nil, 0, false
			}

			numsubs++
			_ = writeNativePixel(&buf, cl, bpp, be)
			if corre {
				buf.WriteByte(byte(thex))
				buf.WriteByte(byte(they))
				buf.WriteByte(byte(thew))
				buf.WriteByte(byte(theh))
			} else {
				var g [8]byte
				binary.BigEndian.PutUint16(g[0:2], uint16(thex))
				binary.BigEndian.PutUint16(g[2:4], uint16(they))
				binary.BigEndian.PutUint16(g[4:6], uint16(thew))
				binary.BigEndian.PutUint16(g[6:8], uint16(theh))
				buf.Write(g[:8])
			}
			curLen = newLen

			// 将已编码的子矩形标记为背景色，避免重复处理。
			for jj := they; jj < they+theh; jj++ {
				row := vals[jj*w : (jj+1)*w]
				for ii := thex; ii < thex+thew; ii++ {
					row[ii] = bg
				}
			}
		}
	}
	return buf.Bytes(), numsubs, true
}

// getBgColour 返回背景色：非 8bpp 时直接取第一个像素 (与 vncencoderre.cpp 一致)；
// 8bpp 时统计出现次数最多的字节值。
func getBgColour(vals []uint32, bpp int) uint32 {
	if bpp != 1 {
		if len(vals) == 0 {
			return 0
		}
		return vals[0]
	}
	var counts [256]int
	maxCount := 0
	var maxClr uint32
	for _, v := range vals {
		b := byte(v & 0xff)
		counts[b]++
		if counts[b] > maxCount {
			maxCount = counts[b]
			maxClr = uint32(b)
		}
	}
	return maxClr
}