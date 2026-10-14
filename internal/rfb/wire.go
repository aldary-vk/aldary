package rfb

import (
	"bufio"
	"encoding/binary"
	"io"
)

// wireReader 提供大端 (网络序) 的协议原语读取，与 rfbproto.h 的 CARD8/16/32 对应。
type wireReader struct {
	r *bufio.Reader
}

func newWireReader(r io.Reader) *wireReader {
	return &wireReader{r: bufio.NewReader(r)}
}

func (w *wireReader) ReadU8() (uint8, error) {
	b, err := w.r.ReadByte()
	return b, err
}

func (w *wireReader) ReadU16() (uint16, error) {
	var buf [2]byte
	if _, err := io.ReadFull(w.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(buf[:]), nil
}

func (w *wireReader) ReadU32() (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(w.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(buf[:]), nil
}

func (w *wireReader) ReadFull(p []byte) error {
	_, err := io.ReadFull(w.r, p)
	return err
}

func (w *wireReader) skip(n int) error {
	for i := 0; i < n; i++ {
		if _, err := w.r.ReadByte(); err != nil {
			return err
		}
	}
	return nil
}

// wireWriter 提供大端协议原语写入。
type wireWriter struct {
	w *bufio.Writer
}

func newWireWriter(w io.Writer) *wireWriter {
	return &wireWriter{w: bufio.NewWriter(w)}
}

func (w *wireWriter) WriteU8(v uint8) error {
	return w.w.WriteByte(v)
}

func (w *wireWriter) WriteU16(v uint16) error {
	var buf [2]byte
	binary.BigEndian.PutUint16(buf[:], v)
	_, err := w.w.Write(buf[:])
	return err
}

func (w *wireWriter) WriteU32(v uint32) error {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	_, err := w.w.Write(buf[:])
	return err
}

func (w *wireWriter) Write(p []byte) (int, error) {
	return w.w.Write(p)
}

func (w *wireWriter) Flush() error {
	return w.w.Flush()
}