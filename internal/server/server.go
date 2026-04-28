// Package server implements the goopg listener and per-connection lifecycle.
//
// v0 supports the protocol-3.0 startup handshake only: it sends
// AuthenticationOk, a fixed ParameterStatus block, BackendKeyData, and
// ReadyForQuery, then rejects every subsequent frontend message with an
// "unsupported" ErrorResponse. The simple Query path arrives in milestone 2.
//
// Design: docs/design/0002-wire-protocol.md.
package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// Config controls a single Server instance.
type Config struct {
	// Address is the listen address in "host:port" form. Default
	// "127.0.0.1:5432" if empty.
	Address string

	// ServerVersion is the value reported in the `server_version`
	// ParameterStatus message. Tracked in
	// docs/design/0001-architecture-overview.md §5.
	ServerVersion string

	// Logger receives connection-lifecycle events. nil means
	// slog.Default().
	Logger *slog.Logger

	// AcceptDeadline bounds Accept() so the goroutine can notice context
	// cancellation. Defaults to 250ms; tests can shrink it.
	AcceptDeadline time.Duration

	// HandshakeTimeout caps how long a client may take to complete the
	// startup handshake. Defaults to 30s.
	HandshakeTimeout time.Duration
}

func (c *Config) defaults() {
	if c.Address == "" {
		c.Address = "127.0.0.1:5432"
	}
	if c.ServerVersion == "" {
		c.ServerVersion = "18.3"
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.AcceptDeadline == 0 {
		c.AcceptDeadline = 250 * time.Millisecond
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = 30 * time.Second
	}
}

// Server is the goopg TCP listener.
//
// Lifecycle:
//
//	srv, err := server.New(cfg)
//	go func() { _ = srv.Run(ctx) }()
//	...
//	cancel(); srv.Wait() // or rely on Run to return
//
// Once Run returns, the Server cannot be restarted.
type Server struct {
	cfg Config

	listener net.Listener
	ready    chan struct{} // closed once listener is bound
	addr     atomic.Pointer[net.TCPAddr]

	connWG    sync.WaitGroup
	nextPID   atomic.Uint32
	closeOnce sync.Once
}

// New constructs a Server but does not start listening. Use Run to start.
func New(cfg Config) *Server {
	cfg.defaults()
	s := &Server{
		cfg:   cfg,
		ready: make(chan struct{}),
	}
	s.nextPID.Store(0)
	return s
}

// Addr returns the listen address (resolved port if the config used :0).
// Returns nil before Run has bound the listener; callers in tests should
// wait on Ready() first.
func (s *Server) Addr() *net.TCPAddr { return s.addr.Load() }

// Ready returns a channel that closes once the listener is accepting
// connections. Useful in tests to avoid a race between server startup and
// client dial.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Run binds the listener and serves connections until ctx is cancelled or
// a non-recoverable Accept error occurs. It returns nil on a clean shutdown
// (ctx cancelled), or the underlying error otherwise.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Address, err)
	}
	s.listener = ln
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		s.addr.Store(tcpAddr)
	}
	close(s.ready)
	s.cfg.Logger.Info("goopg listener bound", "address", ln.Addr().String())

	// One goroutine watches ctx.Done() and closes the listener so Accept
	// unblocks. We also stamp accept deadlines in the loop as a backstop
	// for platforms where Close doesn't unblock pending Accept (e.g. some
	// kernels/golang versions historically had quirks here).
	go func() {
		<-ctx.Done()
		s.closeOnce.Do(func() { _ = ln.Close() })
	}()

	acceptErr := s.acceptLoop(ctx, ln)

	// Wait for in-flight connections to drain.
	s.connWG.Wait()

	s.cfg.Logger.Info("goopg listener stopped")
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return acceptErr
}

func (s *Server) acceptLoop(ctx context.Context, ln net.Listener) error {
	tcpLn, hasDeadline := ln.(*net.TCPListener)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if hasDeadline {
			_ = tcpLn.SetDeadline(time.Now().Add(s.cfg.AcceptDeadline))
		}
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		s.connWG.Add(1)
		go func() {
			defer s.connWG.Done()
			s.serveConn(ctx, conn)
		}()
	}
}

func (s *Server) serveConn(ctx context.Context, raw net.Conn) {
	pid := s.nextPID.Add(1)
	logger := s.cfg.Logger.With(
		"remote", raw.RemoteAddr().String(),
		"pid", pid,
	)
	defer func() {
		_ = raw.Close()
		logger.Debug("connection closed")
	}()

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Cancel the connection context when the parent ctx is cancelled. Closing
	// the conn nudges the goroutine out of any blocking read.
	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-connCtx.Done():
			_ = raw.SetDeadline(time.Now().Add(50 * time.Millisecond))
		case <-stopWatcher:
		}
	}()

	// Bound the handshake.
	_ = raw.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))

	r := protocol.NewFrameReader(raw)
	w := protocol.NewFrameWriter(raw)

	params, err := s.handleStartup(r, w)
	if err != nil {
		logger.Info("startup failed", "err", err)
		return
	}
	// Clear the handshake deadline; idle-timeout policy is a separate concern.
	_ = raw.SetDeadline(time.Time{})

	user := params["user"]
	app := params["application_name"]
	logger = logger.With("user", user, "application_name", app)
	logger.Info("connection established")

	if err := s.sendStartupReply(w, params, pid); err != nil {
		logger.Info("startup reply failed", "err", err)
		return
	}

	s.runPostStartupLoop(connCtx, r, w, logger)
}

// handleStartup reads startup packets, transparently rejecting SSL/GSS
// requests with 'N' and looping back to read the actual startup packet. It
// returns the parsed parameter map for a regular protocol-3.0 session, or
// an error for everything else (cancel requests, unsupported protocol
// versions, malformed packets).
func (s *Server) handleStartup(r *protocol.FrameReader, w *protocol.FrameWriter) (map[string]string, error) {
	for range 3 {
		version, payload, err := r.ReadStartupPacket()
		if err != nil {
			return nil, err
		}
		switch version {
		case protocol.NegotiateSSLCode, protocol.NegotiateGSSCode:
			if err := w.WriteRaw([]byte{'N'}); err != nil {
				return nil, fmt.Errorf("reject SSL/GSS: %w", err)
			}
			if err := w.Flush(); err != nil {
				return nil, fmt.Errorf("flush SSL/GSS rejection: %w", err)
			}
			continue
		case protocol.CancelRequestCode:
			// v0 has no backends to cancel; close silently per protocol.
			return nil, errors.New("cancel request received (v0 ignores)")
		}
		if version != protocol.ProtocolVersion3_0 {
			major, minor := version>>16, version&0xFFFF
			s.writeFatal(w, sqlstate.FeatureNotSupported,
				fmt.Sprintf("unsupported frontend protocol %d.%d: server supports 3.0", major, minor))
			return nil, fmt.Errorf("unsupported protocol %d.%d", major, minor)
		}
		params, err := protocol.ParseStartupParameters(payload)
		if err != nil {
			s.writeFatal(w, sqlstate.ProtocolViolation, "invalid startup packet")
			return nil, err
		}
		return params, nil
	}
	return nil, errors.New("too many SSL/GSS negotiation attempts")
}

// sendStartupReply emits AuthenticationOk + ParameterStatus block +
// BackendKeyData + ReadyForQuery.
func (s *Server) sendStartupReply(w *protocol.FrameWriter, params map[string]string, pid uint32) error {
	if err := w.WriteAuthenticationOk(); err != nil {
		return err
	}
	for _, kv := range s.parameterStatusBlock(params) {
		if err := w.WriteParameterStatus(kv[0], kv[1]); err != nil {
			return err
		}
	}
	secret, err := newSecretKey()
	if err != nil {
		return err
	}
	if err := w.WriteBackendKeyData(pid, secret); err != nil {
		return err
	}
	if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
		return err
	}
	return w.Flush()
}

// parameterStatusBlock returns the v0 ParameterStatus list. The order is
// deliberate: clients see `server_version` first (the most-gated value) and
// then conventional encodings, matching the order PostgreSQL itself uses.
func (s *Server) parameterStatusBlock(params map[string]string) [][2]string {
	user := params["user"]
	app := params["application_name"]
	return [][2]string{
		{"server_version", s.cfg.ServerVersion},
		{"server_encoding", "UTF8"},
		{"client_encoding", "UTF8"},
		{"application_name", app},
		{"is_superuser", "off"},
		{"session_authorization", user},
		{"DateStyle", "ISO, MDY"},
		{"IntervalStyle", "postgres"},
		{"TimeZone", "UTC"},
		{"integer_datetimes", "on"},
		{"standard_conforming_strings", "on"},
		{"in_hot_standby", "off"},
		{"default_transaction_read_only", "off"},
	}
}

// runPostStartupLoop handles every frame after ReadyForQuery. v0 routes
// simple Query messages into handleQuery; Terminate closes the connection
// cleanly; anything else is an "unsupported" ErrorResponse followed by
// another ReadyForQuery so the client can keep going.
func (s *Server) runPostStartupLoop(ctx context.Context, r *protocol.FrameReader, w *protocol.FrameWriter, logger *slog.Logger) {
	for {
		if ctx.Err() != nil {
			s.writeFatal(w, sqlstate.AdminShutdown, "terminating connection due to administrator command")
			return
		}
		f, err := r.ReadFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				logger.Debug("read frame error", "err", err)
			}
			return
		}
		switch f.Type {
		case protocol.MsgTerminate:
			return
		case protocol.MsgQuery:
			if err := s.handleQuery(w, f.Payload); err != nil {
				logger.Debug("handleQuery write error", "err", err)
				return
			}
		default:
			err = w.WriteErrorResponse([]protocol.ErrorField{
				{Code: protocol.FieldSeverity, Value: "ERROR"},
				{Code: protocol.FieldSeverityNonLocal, Value: "ERROR"},
				{Code: protocol.FieldSQLState, Value: string(sqlstate.FeatureNotSupported)},
				{Code: protocol.FieldMessage, Value: fmt.Sprintf("message type %q not yet supported", f.Type)},
				{Code: protocol.FieldRoutine, Value: "server.runPostStartupLoop"},
			})
			if err != nil {
				return
			}
			if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
				return
			}
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

// writeFatal sends a single FATAL ErrorResponse and flushes. Errors are
// silently swallowed: by the time we're emitting FATAL, the connection is
// already going away.
func (s *Server) writeFatal(w *protocol.FrameWriter, code sqlstate.Code, msg string) {
	_ = w.WriteErrorResponse([]protocol.ErrorField{
		{Code: protocol.FieldSeverity, Value: "FATAL"},
		{Code: protocol.FieldSeverityNonLocal, Value: "FATAL"},
		{Code: protocol.FieldSQLState, Value: string(code)},
		{Code: protocol.FieldMessage, Value: msg},
	})
	_ = w.Flush()
}

func newSecretKey() (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("generate cancel-key: %w", err)
	}
	return binary.BigEndian.Uint32(b[:]), nil
}
