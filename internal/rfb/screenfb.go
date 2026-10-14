package rfb

import (
	"fmt"
	"image"
	"log"
	"sync"
	"time"

	"github.com/kbinani/screenshot"
)

// ScreenFramebuffer 通过 X11 抓取真实桌面，替代测试彩条/图案帧缓冲。
// 内部用后台 goroutine 按固定间隔抓屏到内存缓存，ReadRect 从缓存读取，
// 实现近似实时的屏幕共享 (对应 UltraVNC 的 vncDesktop 桌面采集)。
type ScreenFramebuffer struct {
	display  int
	interval time.Duration

	mu      sync.RWMutex
	fb      *MemoryFramebuffer
	lastErr error

	stop chan struct{}
	done chan struct{}
}

// NewScreenFramebuffer 创建基于真实桌面的帧缓冲，interval 为抓屏间隔。
// 若当前环境无可用显示服务器，返回错误，调用方应回退到测试图案。
func NewScreenFramebuffer(interval time.Duration) (*ScreenFramebuffer, error) {
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	if screenshot.NumActiveDisplays() == 0 {
		return nil, fmt.Errorf("no active display for screen capture (is DISPLAY set?)")
	}
	bounds := screenshot.GetDisplayBounds(0)
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, fmt.Errorf("invalid display bounds for screen capture")
	}
	s := &ScreenFramebuffer{
		display:  0,
		interval: interval,
		fb:       NewMemoryFramebuffer(bounds.Dx(), bounds.Dy()),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	if err := s.capture(); err != nil {
		return nil, err
	}
	go s.loop()
	return s, nil
}

// capture 抓取一帧并替换像素缓存 (整体替换，保证读取始终看到完整帧)。
func (s *ScreenFramebuffer) capture() error {
	img, err := screenshot.CaptureDisplay(s.display)
	if err != nil {
		return err
	}
	fb := NewMemoryFramebuffer(img.Bounds().Dx(), img.Bounds().Dy())
	fillFbFromRGBA(fb, img)
	s.mu.Lock()
	s.fb = fb
	s.mu.Unlock()
	return nil
}

func (s *ScreenFramebuffer) loop() {
	defer close(s.done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			if err := s.capture(); err != nil {
				s.mu.Lock()
				s.lastErr = err
				s.mu.Unlock()
				log.Printf("screen capture failed: %v", err)
			}
		}
	}
}

// Close 停止后台抓屏 goroutine。
func (s *ScreenFramebuffer) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}

func (s *ScreenFramebuffer) Width() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fb.Width()
}

func (s *ScreenFramebuffer) Height() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fb.Height()
}

func (s *ScreenFramebuffer) ReadRect(r Rect) []pixel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fb.ReadRect(r)
}

// fillFbFromRGBA 将 *image.RGBA (RGBA 字节序, 每像素 4 字节) 转为帧缓冲
// 像素 (RGB 8-8-8)，按 stride 逐行拷贝，忽略 alpha 通道。
func fillFbFromRGBA(fb *MemoryFramebuffer, img *image.RGBA) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := img.Pix
	stride := img.Stride
	for y := 0; y < h; y++ {
		row := pix[y*stride : y*stride+w*4]
		for x := 0; x < w; x++ {
			o := x * 4
			fb.SetPixel(x, y, pixel{uint32(row[o]), uint32(row[o+1]), uint32(row[o+2])})
		}
	}
}