package rfb

import (
	"fmt"
	"log"
	"net"
	"strings"
)

// Server 是 UltraVNC 兼容的服务端，对应 winvnc 的 vncServer 主体：
// 监听 TCP、握手认证、并为每个客户端维护消息循环。
type Server struct {
	Password      string       // VncAuth 密码 (空则使用 None 安全类型)
	Name          string       // 桌面名称
	CompressLevel int          // zlib 压缩级别 (0..9)
	Framebuffer   Framebuffer  // 桌面像素源
	InputHandler  InputHandler // 输入事件回调 (可为 nil)
}

// Client 表示一个已连接的客户端会话。
type Client struct {
	conn net.Conn
	r    *wireReader
	w    *wireWriter

	server    *Server
	pf        PixelFormat
	enc       *Encoder
	encodings []uint32
}

// NewClient 创建客户端会话 (握手尚未发生)。
func newClient(conn net.Conn, srv *Server) *Client {
	return &Client{
		conn:   conn,
		r:      newWireReader(conn),
		w:      newWireWriter(conn),
		server: srv,
		pf:     DefaultPixelFormat(),
		enc:    NewEncoder(DefaultPixelFormat(), srv.CompressLevel),
	}
}

// Serve 处理单个客户端：握手、认证、初始化、消息循环。
func (c *Client) Serve() error {
	defer c.conn.Close()
	if err := c.handshake(); err != nil {
		return err
	}
	return c.messageLoop()
}

// handshake 对应 RFB 3.8 的协议版本交换、安全协商、VncAuth 认证与初始化。
func (c *Client) handshake() error {
	// 1. 服务端版本
	if _, err := c.w.w.WriteString(fmt.Sprintf(ProtocolVersionFormat, ProtocolMajorVersion, ProtocolMinorVersion)); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}

	// 2. 客户端版本 (固定 12 字节 "RFB xxx.yyy\n")
	verBuf := make([]byte, 12)
	if err := c.r.ReadFull(verBuf); err != nil {
		return err
	}
	if err := parseClientVersion(string(verBuf)); err != nil {
		return err
	}
	c.logf("version negotiated: server=%q client=%q", fmt.Sprintf(ProtocolVersionFormat, ProtocolMajorVersion, ProtocolMinorVersion), strings.TrimSpace(string(verBuf)))

	// 3. 安全类型协商
	var secTypes []byte
	if c.server.Password == "" {
		secTypes = []byte{SecTypeNone}
	} else {
		secTypes = []byte{SecTypeVncAuth}
	}
	if err := c.w.WriteU8(uint8(len(secTypes))); err != nil {
		return err
	}
	if _, err := c.w.w.Write(secTypes); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}

	secType, err := c.r.ReadU8()
	if err != nil {
		return err
	}
	c.logf("offered security types=%v, client selected=%d", secTypes, secType)

	switch secType {
	case SecTypeNone:
		if c.server.Password != "" {
			return fmt.Errorf("client requested None auth but password is set")
		}
	case SecTypeVncAuth:
		if err := c.vncAuth(); err != nil {
			return err
		}
	default:
		// 客户端选择了不支持的安全类型
		_ = c.w.WriteU32(VncAuthFailed)
		_ = c.w.Flush()
		return fmt.Errorf("unsupported security type %d", secType)
	}

	// 认证成功后，服务端统一返回 4 字节结果 (rfbVncAuthOK)，与 UltraVNC 的
	// vncclient.cpp Authenticate 流程一致：即使 rfbNoAuth 也会发送结果码。
	if err := c.w.WriteU32(VncAuthOK); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}
	c.logf("authentication OK (security=%d), sent result=%d", secType, VncAuthOK)

	// 4. ClientInit (1 字节 shared-flag)
	shared, err := c.r.ReadU8()
	if err != nil {
		return err
	}
	c.logf("ClientInit shared-flag=%d", shared)

	// 5. ServerInit
	return c.serverInit()
}

// vncAuth 执行 VNC 挑战-响应认证，对应 vncauth.c 的服务端流程。
func (c *Client) vncAuth() error {
	challenge, err := vncRandomBytes(challengeSize)
	if err != nil {
		return err
	}
	if _, err := c.w.w.Write(challenge); err != nil {
		return err
	}
	if err := c.w.Flush(); err != nil {
		return err
	}

	response := make([]byte, challengeSize)
	if err := c.r.ReadFull(response); err != nil {
		return err
	}

	if !verifyChallenge(challenge, response, c.server.Password) {
		_ = c.w.WriteU32(VncAuthFailed)
		_ = c.w.Flush()
		return fmt.Errorf("authentication failed")
	}
	// 注意：认证结果码 (rfbVncAuthOK) 统一由 handshake() 在认证成功后发送一次，
	// 与 UltraVNC vncclient.cpp 的 AuthenticateClient 仅在流程结束时
	// SendExactQueue 一个 CARD32 保持一致，避免重复发送导致后续字节流错位。
	return nil
}

// serverInit 发送帧缓冲尺寸、像素格式与桌面名称。
func (c *Client) serverInit() error {
	fb := c.server.Framebuffer
	if err := c.w.WriteU16(uint16(fb.Width())); err != nil {
		return err
	}
	if err := c.w.WriteU16(uint16(fb.Height())); err != nil {
		return err
	}
	var pfbuf [16]byte
	c.pf.WriteTo(pfbuf[:])
	if _, err := c.w.w.Write(pfbuf[:]); err != nil {
		return err
	}
	name := c.server.Name
	if name == "" {
		name = "UltraVNC Go"
	}
	if err := c.w.WriteU32(uint32(len(name))); err != nil {
		return err
	}
	if _, err := c.w.w.WriteString(name); err != nil {
		return err
	}
	c.logf("serverInit: %dx%d, pixelFormat={%s}, name=%q", fb.Width(), fb.Height(), c.pf.String(), name)
	return c.w.Flush()
}

// logf 以连接地址为前缀输出日志，便于多客户端场景下区分会话。
func (c *Client) logf(format string, args ...interface{}) {
	log.Printf("[client %s] "+format, append([]interface{}{c.conn.RemoteAddr()}, args...)...)
}

// encodingName 返回编码类型的可读名称，用于日志展示当前传输模式。
func encodingName(e uint32) string {
	switch e {
	case EncodingRaw:
		return "Raw"
	case EncodingCopyRect:
		return "CopyRect"
	case EncodingRRE:
		return "RRE"
	case EncodingCoRRE:
		return "CoRRE"
	case EncodingHextile:
		return "Hextile"
	case EncodingZlib:
		return "Zlib"
	case EncodingZRLE:
		return "ZRLE"
	case EncodingTight:
		return "Tight"
	default:
		return fmt.Sprintf("0x%X", e)
	}
}

func parseClientVersion(s string) error {
	// 接受 "RFB 003.003" .. "RFB 003.008"
	if len(s) < 12 || !strings.HasPrefix(s, "RFB ") {
		return fmt.Errorf("bad client version %q", s)
	}
	var major, minor int
	if _, err := fmt.Sscanf(s, "RFB %03d.%03d\n", &major, &minor); err != nil {
		return fmt.Errorf("bad client version %q", strings.TrimSpace(s))
	}
	if major != 3 {
		return fmt.Errorf("unsupported RFB major %d", major)
	}
	return nil
}

// messageLoop 处理客户端消息，对应 vncServer 的 ClientThread / DoClient 循环。
func (c *Client) messageLoop() error {
	for {
		msgType, err := c.r.ReadU8()
		if err != nil {
			return err
		}
		switch msgType {
		case MsgSetPixelFormat:
			err = c.handleSetPixelFormat()
		case MsgSetEncodings:
			err = c.handleSetEncodings()
		case MsgFramebufferUpdateRequest:
			err = c.handleFramebufferUpdateRequest()
		case MsgKeyEvent:
			err = c.handleKeyEvent()
		case MsgPointerEvent:
			err = c.handlePointerEvent()
		case MsgClientCutText:
			err = c.handleClientCutText()
		default:
			err = fmt.Errorf("unknown client message type %d", msgType)
		}
		if err != nil {
			return err
		}
	}
}

func (c *Client) handleSetPixelFormat() error {
	if err := c.r.skip(3); err != nil {
		return err
	}
	pf, err := ReadPixelFormat(c.r)
	if err != nil {
		return err
	}
	c.pf = pf
	c.enc = NewEncoder(pf, c.server.CompressLevel)
	c.logf("SetPixelFormat: {%s}", pf.String())
	return nil
}

func (c *Client) handleSetEncodings() error {
	if err := c.r.skip(1); err != nil {
		return err
	}
	n, err := c.r.ReadU16()
	if err != nil {
		return err
	}
	c.encodings = c.encodings[:0]
	for i := 0; i < int(n); i++ {
		e, err := c.r.ReadU32()
		if err != nil {
			return err
		}
		c.encodings = append(c.encodings, e)
		c.applyTightPseudoEncoding(e)
	}
	names := make([]string, len(c.encodings))
	for i, e := range c.encodings {
		names[i] = encodingName(e)
	}
	c.logf("SetEncodings (n=%d): %v", n, names)
	return nil
}

// applyTightPseudoEncoding 解析 Tight 专用伪编码并更新编码器协商参数，
// 对应 UltraVNC vncClient::HandleSetEncodingsMsg 中的 Tight 参数段。
func (c *Client) applyTightPseudoEncoding(e uint32) {
	if applied, desc := setTightPseudoEncoding(c.enc, e); applied {
		c.logf("Tight pseudo-encoding: %s", desc)
	}
}

// setTightPseudoEncoding 将单个 Tight 伪编码应用到 Encoder，返回是否命中及描述。
// 关键约定 (与 TightVNC/UltraVNC 一致)：
//   - CompressLevel 0..9        -> 设置 Tight zlib 压缩级别 (映射到 m_conf 4 档)
//   - QualityLevel 0..9         -> 声明支持 JPEG 并设置质量 (quality = level*10 + 5)
//   - FineQualityLevel 0..100   -> 声明支持 JPEG 并精确设置质量 0..100
//   - Subsamp1X..Subsamp16X     -> JPEG 色度子采样 (仅记录，Go 标准库固定 4:2:0)
func setTightPseudoEncoding(enc *Encoder, e uint32) (bool, string) {
	switch {
	case e >= EncodingCompressLevel0 && e <= EncodingCompressLevel9:
		enc.CompressLevel = int(e - EncodingCompressLevel0)
		return true, fmt.Sprintf("compress level = %d", enc.CompressLevel)
	case e >= EncodingQualityLevel0 && e <= EncodingQualityLevel9:
		level := int(e - EncodingQualityLevel0)
		enc.tightQuality = level*10 + 5
		return true, fmt.Sprintf("JPEG quality level = %d (=> %d)", level, enc.tightQuality)
	case e >= EncodingFineQualityLevel0 && e <= EncodingFineQualityLevel100:
		enc.tightQuality = int(e - EncodingFineQualityLevel0)
		return true, fmt.Sprintf("JPEG fine quality = %d", enc.tightQuality)
	case e >= EncodingSubsamp1X && e <= EncodingSubsamp16X:
		enc.tightSubsampling = int(e - EncodingSubsamp1X)
		return true, fmt.Sprintf("JPEG subsampling = %d", enc.tightSubsampling)
	}
	return false, ""
}

func (c *Client) handleFramebufferUpdateRequest() error {
	if _, err := c.r.ReadU8(); err != nil { // incremental (忽略：静态桌面每次全量)
		return err
	}
	x, err := c.r.ReadU16()
	if err != nil {
		return err
	}
	y, err := c.r.ReadU16()
	if err != nil {
		return err
	}
	w, err := c.r.ReadU16()
	if err != nil {
		return err
	}
	h, err := c.r.ReadU16()
	if err != nil {
		return err
	}

	rect := Rect{X: int(x), Y: int(y), W: int(w), H: int(h)}
	encoding := c.chooseEncoding()
	c.logf("FramebufferUpdateRequest rect=(%d,%d %dx%d) -> encoding=%s(%d), pixelFormat={%s}",
		x, y, w, h, encodingName(encoding), encoding, c.pf.String())
	rects := c.enc.EncodeRect(c.server.Framebuffer, rect, encoding)
	return c.sendFramebufferUpdate(rects, encoding)
}

func (c *Client) handleKeyEvent() error {
	down, err := c.r.ReadU8()
	if err != nil {
		return err
	}
	if err := c.r.skip(2); err != nil {
		return err
	}
	keysym, err := c.r.ReadU32()
	if err != nil {
		return err
	}
	if c.server.InputHandler != nil {
		c.server.InputHandler.KeyEvent(keysym, down != 0)
	}
	return nil
}

func (c *Client) handlePointerEvent() error {
	mask, err := c.r.ReadU8()
	if err != nil {
		return err
	}
	x, err := c.r.ReadU16()
	if err != nil {
		return err
	}
	y, err := c.r.ReadU16()
	if err != nil {
		return err
	}
	if c.server.InputHandler != nil {
		c.server.InputHandler.PointerEvent(mask, x, y)
	}
	return nil
}

func (c *Client) handleClientCutText() error {
	if err := c.r.skip(3); err != nil {
		return err
	}
	n, err := c.r.ReadU32()
	if err != nil {
		return err
	}
	buf := make([]byte, n)
	return c.r.ReadFull(buf)
}

// chooseEncoding 从客户端声明的编码列表中选出第一个受支持的编码。
func (c *Client) chooseEncoding() uint32 {
	for _, enc := range c.encodings {
		switch enc {
		case EncodingRaw, EncodingRRE, EncodingCoRRE, EncodingHextile,
			EncodingZlib, EncodingZRLE, EncodingTight:
			return enc
		}
	}
	return EncodingRaw
}

// sendFramebufferUpdate 发送一条 FramebufferUpdate 消息 (type 0)。
func (c *Client) sendFramebufferUpdate(rects [][]byte, encoding uint32) error {
	if len(rects) == 0 {
		return nil
	}
	var total int
	for _, r := range rects {
		total += len(r)
	}
	if err := c.w.WriteU8(MsgFramebufferUpdate); err != nil {
		return err
	}
	if err := c.w.WriteU8(0); err != nil { // padding
		return err
	}
	if err := c.w.WriteU16(uint16(len(rects))); err != nil {
		return err
	}
	for _, r := range rects {
		if _, err := c.w.w.Write(r); err != nil {
			return err
		}
	}
	c.logf("FramebufferUpdate sent: encoding=%s(%d), numRects=%d, bytes=%d",
		encodingName(encoding), encoding, len(rects), total)
	return c.w.Flush()
}