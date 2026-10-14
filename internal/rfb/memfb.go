package rfb

import "image"

// MemoryFramebuffer 是基于内存的 Framebuffer 实现，可用于测试与静态桌面。
type MemoryFramebuffer struct {
	w, h   int
	pixels []pixel
}

// NewMemoryFramebuffer 创建一个 w*h 的帧缓冲，初始为黑色。
func NewMemoryFramebuffer(w, h int) *MemoryFramebuffer {
	return &MemoryFramebuffer{w: w, h: h, pixels: make([]pixel, w*h)}
}

// NewMemoryFramebufferFrom 从 image.Image 创建帧缓冲 (逐像素转换为 RGB)。
func NewMemoryFramebufferFrom(img image.Image) *MemoryFramebuffer {
	b := img.Bounds()
	fb := NewMemoryFramebuffer(b.Dx(), b.Dy())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			fb.SetPixel(x, y, pixel{r >> 8, g >> 8, bl >> 8})
		}
	}
	return fb
}

func (m *MemoryFramebuffer) Width() int  { return m.w }
func (m *MemoryFramebuffer) Height() int { return m.h }

func (m *MemoryFramebuffer) SetPixel(x, y int, p pixel) {
	if x < 0 || y < 0 || x >= m.w || y >= m.h {
		return
	}
	m.pixels[y*m.w+x] = p
}

func (m *MemoryFramebuffer) GetPixel(x, y int) pixel {
	if x < 0 || y < 0 || x >= m.w || y >= m.h {
		return pixel{}
	}
	return m.pixels[y*m.w+x]
}

func (m *MemoryFramebuffer) ReadRect(r Rect) []pixel {
	r = clampRect(r, m.w, m.h)
	out := make([]pixel, r.W*r.H)
	idx := 0
	for y := r.Y; y < r.Y+r.H; y++ {
		row := m.pixels[y*m.w+r.X : y*m.w+r.X+r.W]
		copy(out[idx:idx+r.W], row)
		idx += r.W
	}
	return out
}

// PixelRef 返回指向内部像素切片的引用，供高效读写 (谨慎使用)。
func (m *MemoryFramebuffer) PixelRef() []pixel { return m.pixels }

// NewPatternFramebuffer 创建一个测试用的渐变 + 色块帧缓冲，用于演示与验证编码器。
func NewPatternFramebuffer(w, h int) *MemoryFramebuffer {
	fb := NewMemoryFramebuffer(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// 左右红/绿渐变 + 斜向蓝条纹，右侧留一块纯色区域。
			var r, g, b uint32
			r = uint32(255 * x / max(w-1, 1))
			g = uint32(255 * y / max(h-1, 1))
			b = uint32((x + y) % 256)
			if x > w*3/4 {
				b = 128
			}
			fb.SetPixel(x, y, pixel{r, g, b})
		}
	}
	return fb
}

// NewBlockFramebuffer 创建一个纯色背景 + 若干纯色矩形的帧缓冲，
// 便于触发 RRE/CoRRE 的子矩形编码分支，用于验证这些编码。
func NewBlockFramebuffer(w, h int) *MemoryFramebuffer {
	fb := NewMemoryFramebuffer(w, h)
	bg := pixel{180, 180, 180}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fb.SetPixel(x, y, bg)
		}
	}
	fill := func(x0, y0, bw, bh int, p pixel) {
		for y := y0; y < y0+bh && y < h; y++ {
			for x := x0; x < x0+bw && x < w; x++ {
				fb.SetPixel(x, y, p)
			}
		}
	}
	fill(20, 20, 120, 90, pixel{255, 0, 0})
	fill(180, 60, 200, 140, pixel{0, 255, 0})
	fill(60, 220, 150, 100, pixel{0, 0, 255})
	fill(350, 300, 180, 120, pixel{255, 255, 0})
	fill(420, 30, 90, 200, pixel{0, 255, 255})
	return fb
}