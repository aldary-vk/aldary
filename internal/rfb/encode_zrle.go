package rfb

import (
	"bytes"
	"encoding/binary"
)

// 本文件实现 ZRLE 编码，对应 UltraVNC rfb/zrleEncode.h (源自 RealVNC) 的
// zrleEncode8/15/16/24A/24B/32 系列函数。矩形按 64x64 瓦片切分，每个瓦片
// 选择 solid/raw/RLE/packed-palette 之一编码，全部瓦片数据经一条持久的 zlib
// 流 (Z_SYNC_FLUSH) 压缩后以 [4 字节长度][数据] 输出。

const (
	zrleTileW  = 64
	zrleTileH  = 64
	zrlePalMax = 127
)

// zrleBitsPerPackedPixel 对应 zrleEncode.h 的 bitsPerPackedPixel[]。
var zrleBitsPerPackedPixel = [16]int{0, 1, 2, 2, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4}

func (e *Encoder) encodeZRLE(fb Framebuffer, rect Rect) []byte {
	rect = clampRect(rect, fb.Width(), fb.Height())
	if rect.W <= 0 || rect.H <= 0 {
		return nil
	}

	var tileData bytes.Buffer
	for ty := rect.Y; ty < rect.Y+rect.H; ty += zrleTileH {
		th := zrleTileH
		if th > rect.Y+rect.H-ty {
			th = rect.Y + rect.H - ty
		}
		for tx := rect.X; tx < rect.X+rect.W; tx += zrleTileW {
			tw := zrleTileW
			if tw > rect.X+rect.W-tx {
				tw = rect.X + rect.W - tx
			}
			vals := translateRect(fb, Rect{X: tx, Y: ty, W: tw, H: th}, e.PF)
			e.zrleEncodeTile(&tileData, vals, tw, th)
		}
	}

	compressed := e.zrleStream().compress(tileData.Bytes())
	body := make([]byte, 4+len(compressed))
	binary.BigEndian.PutUint32(body[0:4], uint32(len(compressed)))
	copy(body[4:], compressed)
	return assemble(rect, EncodingZRLE, body)
}

// zrlePalette 对应 zrleEncode.h 的 PaletteHelper：哈希表反向索引调色板。
type zrlePalette struct {
	palette [zrlePalMax]uint32
	index   [4096 + zrlePalMax]uint8
	key     [4096 + zrlePalMax]uint32
	size    int
}

func newZRLEPalette() *zrlePalette {
	p := &zrlePalette{}
	for i := range p.index {
		p.index[i] = 255
	}
	return p
}

func (p *zrlePalette) hash(pix uint32) int {
	return int((pix ^ (pix >> 17)) & 4095)
}

func (p *zrlePalette) insert(pix uint32) {
	if p.size < zrlePalMax {
		i := p.hash(pix)
		for p.index[i] != 255 && p.key[i] != pix {
			i++
		}
		if p.index[i] != 255 {
			return
		}
		p.index[i] = uint8(p.size)
		p.key[i] = pix
		p.palette[p.size] = pix
	}
	p.size++
}

func (p *zrlePalette) lookup(pix uint32) int {
	i := p.hash(pix)
	for p.index[i] != 255 && p.key[i] != pix {
		i++
	}
	if p.index[i] != 255 {
		return int(p.index[i])
	}
	return -1
}

// zrlePixelBytes 返回 WRITE_PIXEL 输出一个像素的字节数 (BPPOUT/8)。
func zrlePixelBytes(pf PixelFormat) int {
	switch pf.BitsPerPixel {
	case 8:
		return 1
	case 16:
		return 2
	}
	fitsLS3 := uint32(pf.RedMax)<<pf.RedShift < 1<<24 &&
		uint32(pf.GreenMax)<<pf.GreenShift < 1<<24 &&
		uint32(pf.BlueMax)<<pf.BlueShift < 1<<24
	fitsMS3 := pf.RedShift > 7 && pf.GreenShift > 7 && pf.BlueShift > 7
	if fitsLS3 || fitsMS3 {
		return 3
	}
	return 4
}

// zrleWritePixel 对应 writeOpaque8/16/32/24A/24B：按客户端像素格式写入单个像素。
func zrleWritePixel(b *bytes.Buffer, v uint32, pf PixelFormat) {
	switch pf.BitsPerPixel {
	case 8:
		b.WriteByte(byte(v))
		return
	case 16:
		if pf.BigEndian != 0 {
			b.WriteByte(byte(v >> 8))
			b.WriteByte(byte(v))
		} else {
			b.WriteByte(byte(v))
			b.WriteByte(byte(v >> 8))
		}
		return
	}

	fitsLS3 := uint32(pf.RedMax)<<pf.RedShift < 1<<24 &&
		uint32(pf.GreenMax)<<pf.GreenShift < 1<<24 &&
		uint32(pf.BlueMax)<<pf.BlueShift < 1<<24
	fitsMS3 := pf.RedShift > 7 && pf.GreenShift > 7 && pf.BlueShift > 7

	if (fitsLS3 && pf.BigEndian == 0) || (fitsMS3 && pf.BigEndian != 0) {
		// writeOpaque24A：前三个 wire 字节
		if pf.BigEndian != 0 {
			b.WriteByte(byte(v >> 24))
			b.WriteByte(byte(v >> 16))
			b.WriteByte(byte(v >> 8))
		} else {
			b.WriteByte(byte(v))
			b.WriteByte(byte(v >> 8))
			b.WriteByte(byte(v >> 16))
		}
		return
	}
	if (fitsLS3 && pf.BigEndian != 0) || (fitsMS3 && pf.BigEndian == 0) {
		// writeOpaque24B：后三个 wire 字节
		if pf.BigEndian != 0 {
			b.WriteByte(byte(v >> 16))
			b.WriteByte(byte(v >> 8))
			b.WriteByte(byte(v))
		} else {
			b.WriteByte(byte(v >> 8))
			b.WriteByte(byte(v >> 16))
			b.WriteByte(byte(v >> 24))
		}
		return
	}
	// 完整 32bpp
	if pf.BigEndian != 0 {
		b.WriteByte(byte(v >> 24))
		b.WriteByte(byte(v >> 16))
		b.WriteByte(byte(v >> 8))
		b.WriteByte(byte(v))
	} else {
		b.WriteByte(byte(v))
		b.WriteByte(byte(v >> 8))
		b.WriteByte(byte(v >> 16))
		b.WriteByte(byte(v >> 24))
	}
}

// zrleEncodeTile 对应 ZRLE_ENCODE_TILE：编码单个 64x64 (或边缘更小) 瓦片。
func (e *Encoder) zrleEncodeTile(b *bytes.Buffer, data []uint32, w, h int) {
	pf := e.PF
	bppOut := zrlePixelBytes(pf)

	ph := newZRLEPalette()
	runs := 0
	singlePixels := 0

	n := w * h
	d := make([]uint32, n+1)
	copy(d, data)
	if n > 0 {
		d[n] = ^d[n-1] // 哨兵：末端之后放置一个不同的值，使扫描循环自然终止
	}

	ptr := 0
	for ptr < n {
		pix := d[ptr]
		ptr++
		if d[ptr] != pix {
			singlePixels++
		} else {
			for d[ptr] == pix {
				ptr++
			}
			runs++
		}
		ph.insert(pix)
	}

	// 纯色瓦片特例
	if ph.size == 1 {
		b.WriteByte(1)
		zrleWritePixel(b, ph.palette[0], pf)
		return
	}

	// 估算各编码体积，选最小者
	useRle := false
	usePalette := false

	estimatedBytes := w * h * bppOut
	plainRleBytes := (bppOut + 1) * (runs + singlePixels)
	if plainRleBytes < estimatedBytes {
		useRle = true
		estimatedBytes = plainRleBytes
	}

	if ph.size < 128 {
		paletteRleBytes := bppOut*ph.size + 2*runs + singlePixels
		if paletteRleBytes < estimatedBytes {
			useRle = true
			usePalette = true
			estimatedBytes = paletteRleBytes
		}
		if ph.size < 17 {
			packedBytes := bppOut*ph.size + w*h*zrleBitsPerPackedPixel[ph.size-1]/8
			if packedBytes < estimatedBytes {
				useRle = false
				usePalette = true
				estimatedBytes = packedBytes
			}
		}
	}

	if !usePalette {
		ph.size = 0
	}

	sub := byte(ph.size)
	if useRle {
		sub |= 128
	}
	b.WriteByte(sub)

	for i := 0; i < ph.size; i++ {
		zrleWritePixel(b, ph.palette[i], pf)
	}

	if useRle {
		ptr := 0
		for ptr < n {
			runStart := ptr
			pix := data[ptr]
			ptr++
			for ptr < n && data[ptr] == pix {
				ptr++
			}
			length := ptr - runStart
			if length <= 2 && usePalette {
				index := ph.lookup(pix)
				if length == 2 {
					b.WriteByte(byte(index))
				}
				b.WriteByte(byte(index))
				continue
			}
			if usePalette {
				index := ph.lookup(pix)
				b.WriteByte(byte(index) | 128)
			} else {
				zrleWritePixel(b, pix, pf)
			}
			length--
			for length >= 255 {
				b.WriteByte(255)
				length -= 255
			}
			b.WriteByte(byte(length))
		}
		return
	}

	if usePalette {
		// packed pixels
		bppp := zrleBitsPerPackedPixel[ph.size-1]
		ptr := 0
		for i := 0; i < h; i++ {
			nbits := 0
			var by byte
			eol := ptr + w
			for ptr < eol {
				pix := data[ptr]
				ptr++
				index := byte(ph.lookup(pix))
				by = (by << bppp) | index
				nbits += bppp
				if nbits >= 8 {
					b.WriteByte(by)
					nbits = 0
				}
			}
			if nbits > 0 {
				by <<= 8 - nbits
				b.WriteByte(by)
			}
		}
		return
	}

	// raw：逐像素输出 (对应 CPIXEL 分支的 WRITE_PIXEL 循环)
	for _, pix := range data {
		zrleWritePixel(b, pix, pf)
	}
}