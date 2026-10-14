// Package rfb 是 UltraVNC 服务端核心协议 (RFB 3.3/3.8) 的 Go 语言复刻。
//
// 该文件中的常量、结构体与消息定义均对照 UltraVNC 源码 rfb/rfbproto.h
// (SPDX GPL-3.0-or-later, Copyright UltraVNC Team / AT&T / RealVNC / Const Kaplinsky)。
// 网络字节序为大端 (BigEndian)，与原始 C 结构体逐字节一致。
package rfb

// 协议版本
const (
	ProtocolMajorVersion = 3
	ProtocolMinorVersion = 8
	// UltraVNC 使用 "RFB 003.008\n" (3.8) 作为服务端版本。
	ProtocolVersionFormat = "RFB %03d.%03d\n"
	ProtocolVersionMsgLen = 12
)

// 安全类型 (Security Types) —— 对照 rfbproto.h
const (
	SecTypeInvalid = 0
	SecTypeNone    = 1
	SecTypeVncAuth = 2

	// UltraVNC 扩展安全类型 (0x70-0x76 段属 UltraVNC)
	SecTypeUltraVNC              = 17   // rfbUltraVNC
	SecTypeUltraVNC_SCPrompt     = 0x68 // rfbUltraVNC_SCPrompt
	SecTypeUltraVNC_SessionSel   = 0x69 // rfbUltraVNC_SessionSelect
	SecTypeUltraVNC_MsLogonIAuth = 0x70
	SecTypeUltraVNC_MsLogonII    = 0x71 // rfbUltraVNC_MsLogonIIAuth
	SecTypeUltraVNC_SecurePlugin = 0x72
	SecTypeUltraVNC_SecurePluginNew = 0x73
	SecTypeUltraVNC_ExtraMsg     = 0x74 // rfbClientInitExtraMsgSupport
	SecTypeUltraVNC_ExtraMsgNew  = 0x75
	SecTypeUltraVNC_MsLogonIII   = 0x76
)

// VNC 认证结果 (rfbVncAuthOK / Failed / TooMany ...)
const (
	VncAuthOK        uint32 = 0
	VncAuthFailed    uint32 = 1
	VncAuthTooMany   uint32 = 2
	VncAuthFailedEx  uint32 = 3
	VncAuthContinue  uint32 = 0xFFFFFFFF
)

// 服务端 -> 客户端 消息类型
const (
	MsgFramebufferUpdate   = 0
	MsgSetColourMapEntries = 1
	MsgBell                = 2
	MsgServerCutText       = 3
	MsgResizeFrameBuffer   = 4
	MsgServerState         = 0xAD
)

// 客户端 -> 服务端 消息类型
const (
	MsgSetPixelFormat           = 0
	MsgFixColourMapEntries      = 1
	MsgSetEncodings             = 2
	MsgFramebufferUpdateRequest = 3
	MsgKeyEvent                 = 4
	MsgPointerEvent             = 5
	MsgClientCutText            = 6
	MsgFileTransfer             = 7
	MsgSetScale                 = 8
	MsgSetServerInput           = 9
	MsgSetSW                    = 10
	MsgTextChat                 = 11
	MsgKeepAlive                = 13
	MsgSetDesktopSize           = 251
)

// 编码类型 (Encoding Types) —— 对照 rfbproto.h
const (
	EncodingRaw            uint32 = 0
	EncodingCopyRect       uint32 = 1
	EncodingRRE            uint32 = 2
	EncodingCoRRE          uint32 = 4
	EncodingHextile        uint32 = 5
	EncodingZlib           uint32 = 6
	EncodingTight          uint32 = 7
	EncodingZlibHex        uint32 = 8
	EncodingUltra          uint32 = 9  // UltraVNC 专用 (LZO / 回退 zlib)
	EncodingUltra2         uint32 = 10 // UltraVNC 专用
	EncodingZRLE           uint32 = 16

	// 伪编码 / 扩展 (Pseudo-encodings)
	EncodingCache              uint32 = 0xFFFF0000
	EncodingCacheEnable        uint32 = 0xFFFF0001
	EncodingCacheZip           uint32 = 0xFFFF0007
	EncodingQueueZip           uint32 = 0xFFFF0008
	EncodingUltraZip           uint32 = 0xFFFF0009
	EncodingServerState        uint32 = 0xFFFF8000
	EncodingEnableKeepAlive    uint32 = 0xFFFF8001
	EncodingFTProtocolVersion  uint32 = 0xFFFF8002
	EncodingPseudoSession      uint32 = 0xFFFF8003
	EncodingEnableIdleTime     uint32 = 0xFFFF8004
	EncodingMonitorInfo        uint32 = 0xFFFF8005
	EncodingUnicodeTextChat    uint32 = 0xFFFF8006

	// Tight 专用伪编码
	EncodingFineQualityLevel0   uint32 = 0xFFFFFE00
	EncodingFineQualityLevel100 uint32 = 0xFFFFFE64
	EncodingSubsamp1X           uint32 = 0xFFFFFD00
	EncodingSubsamp4X           uint32 = 0xFFFFFD01
	EncodingSubsamp2X           uint32 = 0xFFFFFD02
	EncodingSubsampGray         uint32 = 0xFFFFFD03
	EncodingSubsamp8X           uint32 = 0xFFFFFD04
	EncodingSubsamp16X          uint32 = 0xFFFFFD05
	EncodingCompressLevel0      uint32 = 0xFFFFFF00
	EncodingCompressLevel9      uint32 = 0xFFFFFF09
	EncodingXCursor             uint32 = 0xFFFFFF10
	EncodingRichCursor          uint32 = 0xFFFFFF11
	EncodingPointerPos          uint32 = 0xFFFFFF18
	EncodingLastRect            uint32 = 0xFFFFFF20
	EncodingNewFBSize           uint32 = 0xFFFFFF21
	EncodingQualityLevel0       uint32 = 0xFFFFFFE0
	EncodingQualityLevel9       uint32 = 0xFFFFFFE9
	EncodingExtendedClipboard   uint32 = 0xC0A1E5CE
)

// 客户端初始化标志位 (rfbClientInitMsgFlags)
const (
	ClientInitShared           = 0x01
	ClientInitExtraMsgSupport  = 0x02
	ClientInitExtraMsgUnicode  = 0x04
)

// 指针事件按钮掩码 (rfbPointerEventMsg)
const (
	Button1Mask  = 1
	Button2Mask  = 2
	Button3Mask  = 4
	Button4Mask  = 8
	Button5Mask  = 16
	Button6Mask  = 32
	Button7Mask  = 64
	Button8Mask  = 128
	WheelUpMask  = Button4Mask
	WheelDownMask = Button5Mask
	WheelLeftMask = Button6Mask
	WheelRightMask = Button7Mask
)

// ZRLE / Hextile 参数
const (
	ZRLETileWidth  = 64
	ZRLETileHeight = 64
)