package rfb

import (
	"net"
)

// Listener 开始监听并接受客户端连接，为每个连接启动一个 goroutine 处理会话。
// 对应 UltraVNC winvnc 的 vncServer::SockConnect / ClientThread 调度。
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve 接受连接并为每个客户端启动会话。
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			c := newClient(conn, s)
			_ = c.Serve()
		}()
	}
}