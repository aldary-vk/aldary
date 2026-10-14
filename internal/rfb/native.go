package rfb

import (
	"encoding/binary"
	"io"
)

// 本文件提供“客户端像素格式”下的原生像素读写与转换工具，
// 对应 UltraVNC vncEncoder::Translate() 的职责：将本地帧缓冲 (32bpp BGR)
// 转换为客户端请求的像素格式，供各编码器使用。

// nativeSize 返回客户端格式下一个像素占用的字节数。
func nativeSize(pf PixelFormat) int {
	return int(pf.BitsPerPixel) / 8
}

// bytesToNativePixel 将原生缓冲区中 offset 处的一个像素解析为 uint32。
func bytesToNativePixel(buf []byte, off, size int, bigEndian bool) uint32 {
	switch size {
	case 1:
		return uint32(buf[off])
	case 2:
		if bigEndian {
			return uint32(binary.BigEndian.Uint16(buf[off : off+2]))
		}
		return uint32(binary.LittleEndian.Uint16(buf[off : off+2]))
	case 4:
		if bigEndian {
			return binary.BigEndian.Uint32(buf[off : off+4])
		}
		return binary.LittleEndian.Uint32(buf[off : off+4])
	default:
		return 0
	}
}

// writeNativePixel 按客户端像素格式写入一个原生像素值。
func writeNativePixel(w io.Writer, v uint32, size int, bigEndian bool) error {
	var buf [4]byte
	switch size {
	case 1:
		buf[0] = byte(v & 0xff)
		_, err := w.Write(buf[:1])
		return err
	case 2:
		if bigEndian {
			binary.BigEndian.PutUint16(buf[:2], uint16(v))
		} else {
			binary.LittleEndian.PutUint16(buf[:2], uint16(v))
		}
		_, err := w.Write(buf[:2])
		return err
	case 4:
		if bigEndian {
			binary.BigEndian.PutUint32(buf[:4], v)
		} else {
			binary.LittleEndian.PutUint32(buf[:4], v)
		}
		_, err := w.Write(buf[:4])
		return err
	default:
		_, err := w.Write(buf[:size])
		return err
	}
}

// writeCPixel 写入 Tight / ZRLE 使用的 CPIXEL 格式：
// 8bpp -> 1 字节；16bpp -> 2 字节 (客户端字节序)；
// 24/32bpp -> 3 字节，大端为 R,G,B，小端为 B,G,R (与 RFC 6143 CPIXEL 一致)。
func writeCPixel(w io.Writer, v uint32, pf PixelFormat) error {
	switch pf.BitsPerPixel {
	case 8:
		return writeNativePixel(w, v, 1, pf.BigEndian != 0)
	case 16:
		return writeNativePixel(w, v, 2, pf.BigEndian != 0)
	default:
		p := rgbFromPF(v, pf)
		if pf.BigEndian != 0 {
			return writeBytes(w, byte(p.R), byte(p.G), byte(p.B))
		}
		return writeBytes(w, byte(p.B), byte(p.G), byte(p.R))
	}
}

// cpixelSize 返回 CPIXEL 的字节数。
func cpixelSize(pf PixelFormat) int {
	switch pf.BitsPerPixel {
	case 8:
		return 1
	case 16:
		return 2
	default:
		return 3
	}
}

func writeBytes(w io.Writer, b ...byte) error {
	_, err := w.Write(b)
	return err
}