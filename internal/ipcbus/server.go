package ipcbus

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/go-logr/logr"
)

// SidecarConn represents a connected sidecar over the UDS.
type SidecarConn struct {
	Channel string
	Mode    string
	conn    net.Conn
	mu      sync.Mutex
}

// Send writes a framed message to the sidecar connection.
func (sc *SidecarConn) Send(msg *Message) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return WriteMessage(sc.conn, msg)
}

// Server is a Unix Domain Socket server that accepts sidecar connections
// and routes messages via the Router.
type Server struct {
	socketPath string
	listener   net.Listener
	router     *Router
	logger     logr.Logger
	wg         sync.WaitGroup
}

// NewServer creates a Server that will listen on the given socket path.
func NewServer(socketPath string, router *Router, logger logr.Logger) *Server {
	return &Server{
		socketPath: socketPath,
		router:     router,
		logger:     logger,
	}
}

// Start removes any stale socket file, listens on the Unix socket, and
// accepts connections in a loop. It blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	// Remove stale socket file.
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on unix socket: %w", err)
	}
	s.listener = ln

	// Make socket world-accessible so sidecars in different containers can connect.
	if err := os.Chmod(s.socketPath, 0o777); err != nil {
		ln.Close()
		return fmt.Errorf("failed to chmod socket: %w", err)
	}

	s.logger.Info("UDS server started", "socket", s.socketPath)

	// Close listener when context is cancelled.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				s.wg.Wait()
				return nil
			default:
				s.logger.Error(err, "accept failed")
				continue
			}
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
}

// handleConn processes a single sidecar connection. The first message must
// be a TypeRegister; after successful registration an ACK is sent back and
// the connection enters a read loop forwarding messages to the router.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// First message must be registration.
	msg, err := ReadMessage(conn)
	if err != nil {
		s.logger.Error(err, "failed to read registration message")
		return
	}

	if msg.Type != TypeRegister {
		s.logger.Info("first message is not register, closing connection", "type", msg.Type)
		return
	}

	sc := &SidecarConn{
		Channel: msg.Channel,
		Mode:    string(msg.Type),
		conn:    conn,
	}

	s.router.Register(sc)
	defer s.router.Unregister(sc)

	// Send registration ACK.
	ack := NewAck(msg.ID)
	if err := sc.Send(ack); err != nil {
		s.logger.Error(err, "failed to send registration ACK", "channel", sc.Channel)
		return
	}

	// Read loop.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		inMsg, err := ReadMessage(conn)
		if err != nil {
			if err == io.EOF {
				s.logger.Info("sidecar disconnected", "channel", sc.Channel)
			} else {
				select {
				case <-ctx.Done():
					// Expected during shutdown.
				default:
					s.logger.Error(err, "read error from sidecar", "channel", sc.Channel)
				}
			}
			return
		}

		s.router.HandleInbound(ctx, sc, inMsg)
	}
}

// ConnectedCount returns the number of connected sidecars.
func (s *Server) ConnectedCount() int {
	return s.router.ConnectedCount()
}
