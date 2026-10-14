package rfb

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// TestVncAuthEndToEnd 验证带密码 (VncAuth) 场景的完整握手字节流：
// 服务端在认证成功后必须只发送一个 4 字节结果码 (rfbVncAuthOK)，紧接着是
// 正确的帧缓冲尺寸而非 0x0。历史上曾因结果码重复发送导致客户端把第二个
// 结果码读成 width=0/height=0，从而引发 UltraVNC「create DIB Section failed」。
func TestVncAuthEndToEnd(t *testing.T) {
	const pw = "xtgl"
	fb := testFramebuffer() // 41x33
	srv := &Server{
		Password:      pw,
		Name:          "auth-e2e",
		CompressLevel: 6,
		Framebuffer:   fb,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// 1/2. 版本交换
	ver := make([]byte, 12)
	if _, err := io.ReadFull(conn, ver); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatal(err)
	}

	// 3. 安全类型列表
	var cnt [1]byte
	if _, err := io.ReadFull(conn, cnt[:]); err != nil {
		t.Fatal(err)
	}
	types := make([]byte, int(cnt[0]))
	if _, err := io.ReadFull(conn, types); err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 || types[0] != SecTypeVncAuth {
		t.Fatalf("unexpected security types %v", types)
	}

	// 4. 选择 VncAuth
	if _, err := conn.Write([]byte{SecTypeVncAuth}); err != nil {
		t.Fatal(err)
	}

	// 5. 读取 16 字节 challenge
	challenge := make([]byte, 16)
	if _, err := io.ReadFull(conn, challenge); err != nil {
		t.Fatal(err)
	}

	// 6. 用密码加密 challenge 作为响应
	response := make([]byte, 16)
	copy(response, challenge)
	vncEncryptBytes(response, pw)
	if _, err := conn.Write(response); err != nil {
		t.Fatal(err)
	}

	// 7. 只读取一个 4 字节结果码
	var authResult [4]byte
	if _, err := io.ReadFull(conn, authResult[:]); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(authResult[:]) != VncAuthOK {
		t.Fatalf("unexpected auth result %d", binary.BigEndian.Uint32(authResult[:]))
	}

	// 8. ClientInit shared-flag
	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}

	// 9. ServerInit: 分辨率必须为 41x33 (若结果码被重复发送则此处会读到 0x0)
	head := make([]byte, 24)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatal(err)
	}
	if fbW := int(binary.BigEndian.Uint16(head[0:2])); fbW != 41 {
		t.Fatalf("unexpected framebuffer width %d", fbW)
	}
	if fbH := int(binary.BigEndian.Uint16(head[2:4])); fbH != 33 {
		t.Fatalf("unexpected framebuffer height %d", fbH)
	}
}

// TestTightEndToEnd 通过真实 TCP 连接走完完整 RFB 握手、认证协商、编码协商与
// 帧缓冲更新请求，验证服务端在客户端声明支持 Tight 时会选用 Tight 编码，
// 且网络字节流与服务端 EncodeRect 的确定性输出逐字节一致。
// 这与 encode_tight_test.go 中对照 ClientConnectionTight.cpp 的往返解码测试
// 互为补充：后者证明编码可被标准客户端还原，前者证明完整协议路径走通。
func TestTightEndToEnd(t *testing.T) {
	fb := testFramebuffer() // 41x33 纯色背景 + 色块
	srv := &Server{
		Password:      "", // 无密码 -> SecTypeNone
		Name:          "tight-e2e",
		CompressLevel: 6,
		Framebuffer:   fb,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go srv.Serve(ln)

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	// 1. 服务端版本
	ver := make([]byte, 12)
	if _, err := io.ReadFull(conn, ver); err != nil {
		t.Fatal(err)
	}
	if string(ver) != "RFB 003.008\n" {
		t.Fatalf("unexpected server version %q", ver)
	}

	// 2. 客户端版本
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatal(err)
	}

	// 3. 安全类型列表
	var cnt [1]byte
	if _, err := io.ReadFull(conn, cnt[:]); err != nil {
		t.Fatal(err)
	}
	types := make([]byte, int(cnt[0]))
	if _, err := io.ReadFull(conn, types); err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 || types[0] != SecTypeNone {
		t.Fatalf("unexpected security types %v", types)
	}

	// 4/5. 选择 None + shared-flag
	if _, err := conn.Write([]byte{SecTypeNone, 0}); err != nil {
		t.Fatal(err)
	}

	// 5.5 认证成功结果码 (rfbVncAuthOK)，与 UltraVNC 一致 (即使 None 也发送)。
	var authResult [4]byte
	if _, err := io.ReadFull(conn, authResult[:]); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(authResult[:]) != VncAuthOK {
		t.Fatalf("unexpected auth result %d", binary.BigEndian.Uint32(authResult[:]))
	}

	// 6. ServerInit (2+2+16+4 字节头 + 桌面名)
	head := make([]byte, 24)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatal(err)
	}
	fbW := int(binary.BigEndian.Uint16(head[0:2]))
	fbH := int(binary.BigEndian.Uint16(head[2:4]))
	nameLen := binary.BigEndian.Uint32(head[20:24])
	name := make([]byte, nameLen)
	if _, err := io.ReadFull(conn, name); err != nil {
		t.Fatal(err)
	}
	if fbW != 41 || fbH != 33 {
		t.Fatalf("unexpected framebuffer %dx%d", fbW, fbH)
	}

	// 7. SetEncodings: 仅声明 Tight
	var se bytes.Buffer
	se.WriteByte(MsgSetEncodings)
	se.WriteByte(0) // padding
	var u16 [2]byte
	binary.BigEndian.PutUint16(u16[:], 1)
	se.Write(u16[:])
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], EncodingTight)
	se.Write(u32[:])
	if _, err := conn.Write(se.Bytes()); err != nil {
		t.Fatal(err)
	}

	// 8. FramebufferUpdateRequest (全量 41x33)
	var fu bytes.Buffer
	fu.WriteByte(MsgFramebufferUpdateRequest)
	fu.WriteByte(0) // incremental
	binary.BigEndian.PutUint16(u16[:], 0)
	fu.Write(u16[:]) // x
	fu.Write(u16[:]) // y
	binary.BigEndian.PutUint16(u16[:], 41)
	fu.Write(u16[:]) // w
	binary.BigEndian.PutUint16(u16[:], 33)
	fu.Write(u16[:]) // h
	if _, err := conn.Write(fu.Bytes()); err != nil {
		t.Fatal(err)
	}

	// 计算期望的输出：与服务端相同编码器、相同配置的确定性输出。
	enc := NewEncoder(DefaultPixelFormat(), 6)
	rects := enc.EncodeRect(fb, Rect{0, 0, 41, 33}, EncodingTight)
	var exp bytes.Buffer
	exp.WriteByte(MsgFramebufferUpdate)
	exp.WriteByte(0) // padding
	binary.BigEndian.PutUint16(u16[:], uint16(len(rects)))
	exp.Write(u16[:])
	for _, r := range rects {
		exp.Write(r)
	}

	got := make([]byte, exp.Len())
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, exp.Bytes()) {
		t.Fatalf("wire bytes mismatch: got %d bytes, want %d", len(got), exp.Len())
	}
}