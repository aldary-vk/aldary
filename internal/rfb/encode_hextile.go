package rfb

import "bytes"

// Hextile 编码：将矩形拆分为 16x16 瓦片，每瓦片用背景/前景/子矩形进行压缩。
// 对应 UltraVNC vncencodehext.cpp，逐逻辑保持一致。

const (
	hextileRaw                 = 1 << 0
	hextileBackgroundSpecified = 1 << 1
	hextileForegroundSpecified = 1 << 2
	hextileAnySubrects         = 1 << 3
	hextileSubrectsColoured    = 1 << 4
)

func hextilePackXY(x, y int) byte { return byte((x << 4) | y) }
func hextilePackWH(w, h int) byte { return byte(((w - 1) << 4) | (h - 1)) }

func (e *Encoder) encodeHextile(fb Framebuffer, rect Rect) []byte {
	rect = clampRect(rect, fb.Width(), fb.Height())
	bpp := e.PF.BytesPerPixel()
	be := e.PF.BigEndian != 0

	var out bytes.Buffer
	validBg := false
	validFg := false
	var bg, fg uint32

	for y := rect.Y; y < rect.Y+rect.H; y += 16 {
		for x := rect.X; x < rect.X+rect.W; x += 16 {
			w, h := 16, 16
			if rect.X+rect.W-x < 16 {
				w = rect.X + rect.W - x
			}
			if rect.Y+rect.H-y < 16 {
				h = rect.Y + rect.H - y
			}

			tile := translateRect(fb, Rect{X: x, Y: y, W: w, H: h}, e.PF)
			tileVals := make([]uint32, len(tile))
			copy(tileVals, tile)

			mono, solid, _, _, newBg, newFg := testColoursFull(tileVals)

			subencoding := byte(0)
			var body bytes.Buffer

			if !validBg || newBg != bg {
				validBg = true
				bg = newBg
				subencoding |= hextileBackgroundSpecified
				_ = writeNativePixel(&body, bg, bpp, be)
			}

			if !solid {
				subencoding |= hextileAnySubrects

				if mono {
					if !validFg || newFg != fg {
						validFg = true
						fg = newFg
						subencoding |= hextileForegroundSpecified
						_ = writeNativePixel(&body, fg, bpp, be)
					}
				} else {
					validFg = false
					subencoding |= hextileSubrectsColoured
				}

				encoded := hextileSubrects(tileVals, w, h, bg, fg, mono, bpp, be)
				if len(encoded) == 0 {
					// 子矩形编码体积超过原始像素，回退为 Raw 瓦片。
					body.Reset()
					subencoding = hextileRaw
					writeRawPixels(&body, tile, e.PF)
				} else {
					body.Write(encoded)
				}
			}

			out.WriteByte(subencoding)
			out.Write(body.Bytes())
		}
	}
	return assemble(rect, EncodingHextile, out.Bytes())
}

// testColoursFull 是 vncencodehext.cpp 中 testColours##bpp 的逐行移植，
// 判断瓦片是单色(solid)、双色(mono)或多色，并给出背景/前景色。
func testColoursFull(vals []uint32) (mono, solid bool, n1, n2 int, bg, fg uint32) {
	mono = true
	solid = true
	var colour1, colour2 uint32
	n1, n2 = 0, 0

	for _, v := range vals {
		if n1 == 0 {
			colour1 = v
		}
		if v == colour1 {
			n1++
			continue
		}
		if n2 == 0 {
			solid = false
			colour2 = v
		}
		if v == colour2 {
			n2++
			continue
		}
		mono = false
		break
	}

	if n1 > n2 {
		bg = colour1
		fg = colour2
	} else {
		bg = colour2
		fg = colour1
	}
	return mono, solid, n1, n2, bg, fg
}

// hextileSubrects 对应 subrectEncode##bpp：编码子矩形序列。
// 返回 [numsubs 字节][子矩形...]；若体积超过原始瓦片像素则返回 nil。
func hextileSubrects(vals []uint32, w, h int, bg, fg uint32, mono bool, bpp int, be bool) []byte {
	var buf bytes.Buffer
	buf.WriteByte(0) // numsubs 占位
	curLen := 1
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

			newLen := curLen + 2
			if !mono {
				newLen += bpp
			}
			if newLen > w*h*bpp {
				return nil
			}

			numsubs++
			if !mono {
				_ = writeNativePixel(&buf, cl, bpp, be)
			}
			buf.WriteByte(hextilePackXY(thex, they))
			buf.WriteByte(hextilePackWH(thew, theh))
			curLen = newLen

			for jj := they; jj < they+theh; jj++ {
				row := vals[jj*w : (jj+1)*w]
				for ii := thex; ii < thex+thew; ii++ {
					row[ii] = bg
				}
			}
		}
	}

	b := buf.Bytes()
	b[0] = byte(numsubs)
	return b
}