package rfb

import (
	"context"
	"image/color"
	"net"
	"testing"
	"time"

	vnc "github.com/bhmj/vnc2video"
)

// 本文件使用 vnc2video 库 (github.com/bhmj/vnc2video) 作为真实的第三方客户端，
// 通过完整 TCP 通路连接本地 UltraVNC 兼容服务端，验证 Tight 编码输出能被
// 标准客户端逐像素正确解码。vnc2video 的 Tight 解码实现与其对照的
// UltraVNC vncviewer/ClientConnectionTight.cpp 逐行一致，因此这里是用独立
// 客户端库来验证服务端编码器兼容性的端到端测试。

// dialTightServer 在随机端口启动服务端并建立真实 TCP 连接。
func dialTightServer(t *testing.T, fb *MemoryFramebuffer) net.Conn {
	t.Helper()
	srv := &Server{
		Password:      "", // 无密码 -> SecTypeNone
		Name:          "tight-vnc2video",
		CompressLevel: 6,
		Framebuffer:   fb,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go srv.Serve(ln)

	nc, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	return nc
}

// connectTightClient 用 vnc2video 客户端完成握手，并仅声明 Tight 编码。
func connectTightClient(t *testing.T, nc net.Conn) *vnc.ClientConn {
	t.Helper()
	serverCh := make(chan vnc.ServerMessage, 8)
	errCh := make(chan error, 8)

	cfg := &vnc.ClientConfig{
		SecurityHandlers: []vnc.SecurityHandler{&vnc.ClientAuthNone{}},
		DrawCursor:       false,
		PixelFormat:      vnc.PixelFormat32bit,
		ClientMessageCh:  make(chan vnc.ClientMessage, 8),
		ServerMessageCh:  serverCh,
		Messages:         vnc.DefaultServerMessages,
		Encodings:        []vnc.Encoding{&vnc.TightEncoding{}, &vnc.RawEncoding{}},
		ErrorCh:          errCh,
	}

	cc, err := vnc.Connect(context.Background(), nc, cfg)
	if err != nil {
		t.Fatalf("vnc2video connect: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })

	// 握手期间 DefaultClientMessageHandler 会按 map 随机顺序声明编码，无法保证
	// Tight 排在最前；这里显式只声明 Tight，确保服务端后续选用 Tight。
	if err := cc.SetEncodings([]vnc.EncodingType{vnc.EncTight}); err != nil {
		t.Fatalf("set encodings: %v", err)
	}
	req := vnc.FramebufferUpdateRequest{Inc: 0, X: 0, Y: 0, Width: cc.Width(), Height: cc.Height()}
	if err := req.Write(cc); err != nil {
		t.Fatalf("framebuffer update request: %v", err)
	}

	// 等待一个 Tight 编码的 FramebufferUpdate (握手期间的初始请求可能已产生一帧
	// 任意编码，或为 Raw 或为 Tight，读取并忽略，直到确认收到 Tight 帧)。
	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-errCh:
			t.Fatalf("client error: %v", err)
		case msg := <-serverCh:
			if msg.Type() != vnc.FramebufferUpdateMsgType {
				continue
			}
			fu := msg.(*vnc.FramebufferUpdate)
			for _, r := range fu.Rects {
				if r.EncType == vnc.EncTight {
					return cc
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for Tight FramebufferUpdate")
		}
	}
}

// assertCanvasMatches 将客户端解码出的画布与源帧缓冲逐像素比较 (8bit RGB，无损)。
func assertCanvasMatches(t *testing.T, cc *vnc.ClientConn, fb *MemoryFramebuffer, name string) {
	t.Helper()
	for y := 0; y < fb.Height(); y++ {
		for x := 0; x < fb.Width(); x++ {
			got := color.RGBAModel.Convert(cc.Canvas.At(x, y)).(color.RGBA)
			exp := fb.GetPixel(x, y)
			if uint32(got.R) != exp.R || uint32(got.G) != exp.G || uint32(got.B) != exp.B {
				t.Fatalf("%s: pixel (%d,%d) = (%d,%d,%d) != (%d,%d,%d)",
					name, x, y, got.R, got.G, got.B, exp.R, exp.G, exp.B)
			}
		}
	}
}

func TestTightVnc2VideoEndToEnd(t *testing.T) {
	t.Run("solid", func(t *testing.T) {
		fb := NewMemoryFramebuffer(32, 24)
		for y := 0; y < 24; y++ {
			for x := 0; x < 32; x++ {
				fb.SetPixel(x, y, pixel{20, 120, 240})
			}
		}
		nc := dialTightServer(t, fb)
		cc := connectTightClient(t, nc)
		assertCanvasMatches(t, cc, fb, "Tight solid")
	})

	t.Run("mono", func(t *testing.T) {
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
		nc := dialTightServer(t, fb)
		cc := connectTightClient(t, nc)
		assertCanvasMatches(t, cc, fb, "Tight mono")
	})

	t.Run("indexed", func(t *testing.T) {
		fb := NewMemoryFramebuffer(40, 20)
		colors := []pixel{
			{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 0}, {255, 0, 255},
		}
		for y := 0; y < 20; y++ {
			for x := 0; x < 40; x++ {
				fb.SetPixel(x, y, colors[(x/8)%len(colors)])
			}
		}
		nc := dialTightServer(t, fb)
		cc := connectTightClient(t, nc)
		assertCanvasMatches(t, cc, fb, "Tight indexed")
	})

	t.Run("fullcolor", func(t *testing.T) {
		fb := NewMemoryFramebuffer(64, 64)
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				fb.SetPixel(x, y, pixel{uint32(x * 4), uint32(y * 4), uint32((x + y) * 2)})
			}
		}
		nc := dialTightServer(t, fb)
		cc := connectTightClient(t, nc)
		assertCanvasMatches(t, cc, fb, "Tight fullcolor")
	})
}