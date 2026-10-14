package rfb

// Framebuffer 表示服务端桌面的像素源 (对应 UltraVNC 的 vncDesktop / vncbuffer)。
// 像素内部统一按 8-8-8 RGB 真彩存储，编码时转换为客户端请求的格式。
type Framebuffer interface {
	Width() int
	Height() int
	// ReadRect 返回 (x,y) 处 w*h 的区域，行优先 (row-major)。
	ReadRect(r Rect) []pixel
}

// InputHandler 接收来自客户端的指针与键盘事件 (对应 vncDesktop 的输入投递)。
type InputHandler interface {
	PointerEvent(buttonMask uint8, x, y uint16)
	KeyEvent(keysym uint32, down bool)
}

// Rect 表示一个矩形区域。
type Rect struct {
	X, Y          int
	W, H          int
}

func (r Rect) area() int { return r.W * r.H }

// clampRect 将矩形裁剪到帧缓冲范围内。
func clampRect(r Rect, fw, fh int) Rect {
	if r.X < 0 {
		r.W += r.X
		r.X = 0
	}
	if r.Y < 0 {
		r.H += r.Y
		r.Y = 0
	}
	if r.X+r.W > fw {
		r.W = fw - r.X
	}
	if r.Y+r.H > fh {
		r.H = fh - r.Y
	}
	if r.W < 0 {
		r.W = 0
	}
	if r.H < 0 {
		r.H = 0
	}
	return r
}