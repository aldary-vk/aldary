package rfb

import (
	"encoding/binary"
	"fmt"
)

// PixelFormat 对应 rfbproto.h 中的 rfbPixelFormat (16 字节)。
type PixelFormat struct {
	BitsPerPixel uint8
	Depth        uint8
	BigEndian    uint8
	TrueColour   uint8
	RedMax       uint16
	GreenMax     uint16
	BlueMax      uint16
	RedShift     uint8
	GreenShift   uint8
	BlueShift    uint8
}

// DefaultPixelFormat 返回 UltraVNC 服务端默认 32 位真彩格式 (BGRX，小端)。
// 与 winvnc 使用 GetLocalFormat() 得到的 32bpp / depth 24 / BGR 布局一致。
func DefaultPixelFormat() PixelFormat {
	return PixelFormat{
		BitsPerPixel: 32,
		Depth:        24,
		BigEndian:    0,
		TrueColour:   1,
		RedMax:       255,
		GreenMax:     255,
		BlueMax:      255,
		RedShift:     16,
		GreenShift:   8,
		BlueShift:    0,
	}
}

// BytesPerPixel 返回一个像素占用的字节数。
func (pf PixelFormat) BytesPerPixel() int {
	return int(pf.BitsPerPixel) / 8
}

// ReadPixelFormat 从网络中读取一个 rfbPixelFormat (16 字节，大端)。
func ReadPixelFormat(r *wireReader) (PixelFormat, error) {
	var pf PixelFormat
	var err error
	if pf.BitsPerPixel, err = r.ReadU8(); err != nil {
		return pf, err
	}
	if pf.Depth, err = r.ReadU8(); err != nil {
		return pf, err
	}
	if pf.BigEndian, err = r.ReadU8(); err != nil {
		return pf, err
	}
	if pf.TrueColour, err = r.ReadU8(); err != nil {
		return pf, err
	}
	if pf.RedMax, err = r.ReadU16(); err != nil {
		return pf, err
	}
	if pf.GreenMax, err = r.ReadU16(); err != nil {
		return pf, err
	}
	if pf.BlueMax, err = r.ReadU16(); err != nil {
		return pf, err
	}
	if pf.RedShift, err = r.ReadU8(); err != nil {
		return pf, err
	}
	if pf.GreenShift, err = r.ReadU8(); err != nil {
		return pf, err
	}
	if pf.BlueShift, err = r.ReadU8(); err != nil {
		return pf, err
	}
	if err = r.skip(3); err != nil { // pad1 + pad2
		return pf, err
	}
	return pf, nil
}

// WriteTo 将 PixelFormat 写入 buffer (大端，含 3 字节 padding)。
func (pf PixelFormat) WriteTo(buf []byte) {
	buf[0] = pf.BitsPerPixel
	buf[1] = pf.Depth
	buf[2] = pf.BigEndian
	buf[3] = pf.TrueColour
	binary.BigEndian.PutUint16(buf[4:6], pf.RedMax)
	binary.BigEndian.PutUint16(buf[6:8], pf.GreenMax)
	binary.BigEndian.PutUint16(buf[8:10], pf.BlueMax)
	buf[10] = pf.RedShift
	buf[11] = pf.GreenShift
	buf[12] = pf.BlueShift
	buf[13] = 0 // pad1
	buf[14] = 0 // pad2 (hi)
	buf[15] = 0 // pad2 (lo)
}

// pixel 表示一个像素，按 RGB 三个通道独立存储 (0..255)。
// 帧缓冲内部统一使用 8-8-8 真彩以便编码器处理。
type pixel struct {
	R, G, B uint32
}

// rgbFromNative 从本地帧缓冲像素值解析 RGB。native 为 PF32 格式下的 raw 像素。
func rgbFromPF(val uint32, pf PixelFormat) pixel {
	var r, g, b uint32
	if pf.TrueColour == 0 {
		// 调色板格式：索引 -> 灰度/调色板 (简单映射为 8 位灰度)
		v := val & 0xff
		return pixel{v, v, v}
	}
	r = (val >> pf.RedShift) & uint32(pf.RedMax)
	g = (val >> pf.GreenShift) & uint32(pf.GreenMax)
	b = (val >> pf.BlueShift) & uint32(pf.BlueMax)
	// 归一化到 0..255
	if pf.RedMax != 255 {
		r = (r*255 + uint32(pf.RedMax)/2) / uint32(pf.RedMax)
	}
	if pf.GreenMax != 255 {
		g = (g*255 + uint32(pf.GreenMax)/2) / uint32(pf.GreenMax)
	}
	if pf.BlueMax != 255 {
		b = (b*255 + uint32(pf.BlueMax)/2) / uint32(pf.BlueMax)
	}
	return pixel{r, g, b}
}

// toNative 将 RGB 像素转换为目标 PixelFormat 下的 native 值。
func (p pixel) toNative(pf PixelFormat) uint32 {
	r := p.R & 0xff
	g := p.G & 0xff
	b := p.B & 0xff
	if pf.TrueColour == 0 {
		return r
	}
	// 与 tableinittctemplate.cpp 一致：含四舍五入 (inMax=255)。
	rm := (r*uint32(pf.RedMax) + 127) / 255
	gm := (g*uint32(pf.GreenMax) + 127) / 255
	bm := (b*uint32(pf.BlueMax) + 127) / 255
	return (rm << pf.RedShift) | (gm << pf.GreenShift) | (bm << pf.BlueShift)
}

func (pf PixelFormat) String() string {
	return fmt.Sprintf("bpp=%d depth=%d bigEndian=%d trueColor=%d rgbMax=%d/%d/%d shift=%d/%d/%d",
		pf.BitsPerPixel, pf.Depth, pf.BigEndian, pf.TrueColour,
		pf.RedMax, pf.GreenMax, pf.BlueMax, pf.RedShift, pf.GreenShift, pf.BlueShift)
}