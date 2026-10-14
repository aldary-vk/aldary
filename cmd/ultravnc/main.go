// Command ultravnc 是 UltraVNC 兼容 RFB 服务端的命令行入口。
package main

import (
	"flag"
	"log"
	"time"

	"ultravnc-go/internal/rfb"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:5900", "监听地址")
	password := flag.String("password", "password", "VNC 认证密码 (空则不认证)")
	width := flag.Int("width", 640, "桌面宽度")
	height := flag.Int("height", 480, "桌面高度")
	blocks := flag.Bool("blocks", false, "使用色块测试图案 (便于触发 RRE/CoRRE 子矩形编码)")
	screen := flag.Bool("screen", true, "抓取真实桌面 (无显示环境自动回退到测试图案)")
	fps := flag.Int("fps", 5, "屏幕抓取刷新率 (帧/秒)")
	flag.Parse()

	var fb rfb.Framebuffer
	if *screen {
		interval := time.Second / time.Duration(max(*fps, 1))
		if s, err := rfb.NewScreenFramebuffer(interval); err == nil {
			fb = s
			log.Printf("screen capture enabled: %dx%d @ %d fps", s.Width(), s.Height(), *fps)
		} else {
			log.Printf("screen capture unavailable (%v), falling back to test pattern", err)
		}
	}
	if fb == nil {
		if *blocks {
			fb = rfb.NewBlockFramebuffer(*width, *height)
		} else {
			fb = rfb.NewPatternFramebuffer(*width, *height)
		}
	}

	srv := &rfb.Server{
		Password:      *password,
		Name:          "UltraVNC Go",
		CompressLevel: 6,
		Framebuffer:   fb,
	}

	log.Printf("listening on %s (password=%q, %dx%d)", *addr, *password, fb.Width(), fb.Height())
	if err := srv.ListenAndServe(*addr); err != nil {
		log.Fatal(err)
	}
}