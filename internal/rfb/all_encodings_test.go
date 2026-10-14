package rfb

import (
	"context"
	"fmt"
	"image/color"
	"net"
	"testing"
	"time"

	vnc "github.com/bhmj/vnc2video"
)

// waitFramebufferUpdate 消费服务端推送的下一条 FramebufferUpdate 消息，
// 忽略其它控制消息，直到收到一条更新或超时/出错。
func waitFramebufferUpdate(t *testing.T, errCh chan error, serverCh chan vnc.ServerMessage) error {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case err := <-errCh:
			return err
		case msg := <-serverCh:
			if msg != nil && msg.Type() == vnc.FramebufferUpdateMsgType {
				return nil
			}
		case <-deadline:
			return fmt.Errorf("timed out waiting for FramebufferUpdate")
		}
	}
}

// TestAllEncodingsVnc2VideoColors 用 vnc2video 真实客户端逐个编码解码服务端输出，
// 并与源帧缓冲逐像素比对颜色，确保除 Tight 外的 Raw/Hextile/ZRLE/Zlib/RRE/CoRRE
// 也都能被标准客户端正确还原 (用于排查"彩条信号"是否源于某个编码器的颜色错乱)。
func TestAllEncodingsVnc2VideoColors(t *testing.T) {
	cases := []struct {
		name string
		et   vnc.EncodingType
	}{
		{"Raw", vnc.EncRaw},
		{"Tight", vnc.EncTight},
		{"Hextile", vnc.EncHextile},
		{"ZRLE", vnc.EncZRLE},
		{"Zlib", vnc.EncZlib},
		{"RRE", vnc.EncRRE},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fb := NewBlockFramebuffer(96, 64)
			srv := &Server{
				Password:      "",
				Name:          "all-enc",
				CompressLevel: 6,
				Framebuffer:   fb,
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go srv.Serve(ln)

			nc, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer nc.Close()

			serverCh := make(chan vnc.ServerMessage, 16)
			errCh := make(chan error, 8)
			cfg := &vnc.ClientConfig{
				SecurityHandlers: []vnc.SecurityHandler{&vnc.ClientAuthNone{}},
				DrawCursor:       false,
				PixelFormat:      vnc.PixelFormat32bit,
				ClientMessageCh:  make(chan vnc.ClientMessage, 8),
				ServerMessageCh:  serverCh,
				Messages:         vnc.DefaultServerMessages,
				Encodings: []vnc.Encoding{
					&vnc.RawEncoding{}, &vnc.TightEncoding{}, &vnc.HextileEncoding{},
					&vnc.ZRLEEncoding{}, &vnc.CopyRectEncoding{}, &vnc.ZLibEncoding{},
					&vnc.RREEncoding{},
				},
				ErrorCh: errCh,
			}
			cc, err := vnc.Connect(context.Background(), nc, cfg)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer cc.Close()

			// vnc2video 的 Connect 握手会先自动发送一次 SetEncodings + 全量
			// FramebufferUpdateRequest。先消费掉这次握手更新，避免其后台应用
			// 与我们随后定向触发的更新竞争，导致读到半更新的画布。
			if err := waitFramebufferUpdate(t, errCh, serverCh); err != nil {
				t.Fatalf("handshake update: %v", err)
			}

			if err := cc.SetEncodings([]vnc.EncodingType{c.et}); err != nil {
				t.Fatalf("set encodings: %v", err)
			}
			req := vnc.FramebufferUpdateRequest{Inc: 0, X: 0, Y: 0, Width: cc.Width(), Height: cc.Height()}
			if err := req.Write(cc); err != nil {
				t.Fatalf("framebuffer update request: %v", err)
			}

			if err := waitFramebufferUpdate(t, errCh, serverCh); err != nil {
				t.Fatalf("target update: %v", err)
			}

			mismatch := 0
			for y := 0; y < fb.Height(); y++ {
				for x := 0; x < fb.Width(); x++ {
					got := color.RGBAModel.Convert(cc.Canvas.At(x, y)).(color.RGBA)
					exp := fb.GetPixel(x, y)
					if uint32(got.R) != exp.R || uint32(got.G) != exp.G || uint32(got.B) != exp.B {
						if mismatch < 3 {
							t.Logf("pixel (%d,%d) got=(%d,%d,%d) want=(%d,%d,%d)",
								x, y, got.R, got.G, got.B, exp.R, exp.G, exp.B)
						}
						mismatch++
					}
				}
			}
			if mismatch != 0 {
				t.Fatalf("encoding %s: %d/%d pixels mismatched", c.name, mismatch, fb.Width()*fb.Height())
			}
		})
	}
}
