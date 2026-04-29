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

	"os"
	"path/filepath"

	"github.com/goopg/goopg/internal/auth"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
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

	// Policy decides whether to admit a connection based on the
	// (conn-type, remote, database, user) tuple from the StartupMessage.
	// nil means auth.DefaultPolicy() — loopback-only trust. See
	// docs/design/0003-authentication.md.
	Policy auth.Policy

	// UserStore answers password/md5/scram credential lookups during
	// the auth exchange. nil means "no users configured", which makes
	// every method other than trust/reject fail with
	// ErrMethodUnsupported. The default policy is trust-only, so a nil
	// UserStore is fine for the common loopback-development setup.
	UserStore auth.UserStore

	// Registry is the GUC registry. nil means
	// config.BuildDefaultRegistry() — the seeded set of variables we
	// already advertise. See docs/design/0004-configuration-and-guc.md.
	Registry *config.Registry

	// Catalog / Pool / TxnMgr are the storage handles the executor
	// needs for table-form COPY (and, in future loops, every other
	// table-touching statement). All three must be non-nil to enable
	// the parser→planner→executor path; if any is nil, the wire
	// layer falls back to the v0 string-matching COPY shape so
	// existing deployments without storage configured keep working.
	Catalog catalog.Catalog
	Pool    *storage.Pool
	TxnMgr  *mvcc.Manager

	// Checkpointer, when set, is invoked by the SQL `CHECKPOINT`
	// verb. Production wiring points at *wal.Checkpointer; tests can
	// inject a fake. nil makes CHECKPOINT fail with
	// feature_not_supported, matching the v0 in-process default
	// where the server runs without a WAL writer.
	Checkpointer executor.Checkpointer

	// Promote, when set, is invoked by the control-plane PROMOTE
	// command. The handler drains pending WAL replay, removes
	// `<DataDir>/standby.signal`, and switches the runtime out of
	// standby mode. Wired by `cmd/goopg start` only when the data
	// directory had standby.signal at boot. nil makes PROMOTE
	// reply with "promote not configured" — protecting an
	// already-primary process from a stray `goopg promote`.
	Promote func() error

	// Slots, when set, exposes the replication-slot registry to
	// the wire-layer replication-command handler (IDENTIFY_SYSTEM,
	// CREATE_REPLICATION_SLOT, DROP_REPLICATION_SLOT). nil disables
	// replication entirely — replication-mode connections still
	// authenticate but every replication command returns
	// feature_not_supported. See
	// docs/design/0005-0001-streaming-replication-architecture.md.
	Slots *wal.Slots

	// WAL exposes the WAL writer's WrittenLSN() so IDENTIFY_SYSTEM
	// can report a current xlogpos. nil → IDENTIFY_SYSTEM reports
	// xlogpos=0/0 (acceptable for tests that don't care about the
	// value).
	WAL *wal.Writer

	// WALDirPath is the on-disk directory the WAL writer is using
	// (typically `<DataDir>/pg_wal`). Required for the walsender
	// path so RecordIterator can open segments. Empty disables the
	// walsender — START_REPLICATION returns feature_not_supported.
	WALDirPath string

	// WALSegmentSize matches the SegmentSize the WAL writer was
	// constructed with. Zero falls back to wal.DefaultSegmentSize.
	WALSegmentSize int64

	// SystemID is the cluster's pg_control identifier reported by
	// IDENTIFY_SYSTEM. Empty makes IDENTIFY_SYSTEM emit a fixed
	// placeholder. Production wiring derives this from
	// `<DataDir>/global/pg_control` once that file exists.
	SystemID string

	// DataDir, when set, controls where the server writes its
	// `postmaster.pid` file and binds its operator-facing control
	// socket. Empty disables both — useful for in-process tests
	// that don't want a sticky pidfile in /tmp.
	DataDir string
}

// hasStorage reports whether all three storage handles are configured.
func (c *Config) hasStorage() bool {
	return c.Catalog != nil && c.Pool != nil && c.TxnMgr != nil
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
	if c.Policy == nil {
		c.Policy = auth.DefaultPolicy()
	}
	if c.Registry == nil {
		c.Registry = config.BuildDefaultRegistry()
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

	// controlListener is the operator-facing command socket; nil
	// when DataDir is unset (in-process tests).
	controlListener *control.Listener
	controlPath     string
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

	// Operator control plane: pidfile + Unix-domain command socket.
	// Optional — disabled when DataDir is empty (e.g. tests).
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	if s.cfg.DataDir != "" {
		if err := s.startControlPlane(runCtx, runCancel, ln); err != nil {
			return err
		}
		defer s.stopControlPlane()
	}

	// One goroutine watches ctx.Done() and closes the listener so Accept
	// unblocks. We also stamp accept deadlines in the loop as a backstop
	// for platforms where Close doesn't unblock pending Accept (e.g. some
	// kernels/golang versions historically had quirks here).
	go func() {
		<-runCtx.Done()
		s.closeOnce.Do(func() { _ = ln.Close() })
	}()

	acceptErr := s.acceptLoop(runCtx, ln)

	// Wait for in-flight connections to drain.
	s.connWG.Wait()

	s.cfg.Logger.Info("goopg listener stopped")
	if errors.Is(runCtx.Err(), context.Canceled) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return acceptErr
}

// startControlPlane writes the pidfile and binds the Unix-domain
// command socket. STOP cancels runCancel so the accept loop drains.
// RELOAD is a v0 no-op — the GUC system can already absorb a new
// postgresql.conf via SET, but a true restart-the-listener reload
// is deferred.
func (s *Server) startControlPlane(runCtx context.Context, runCancel context.CancelFunc, ln net.Listener) error {
	dir := s.cfg.DataDir
	socketPath := filepath.Join(dir, control.SocketName)
	clog := control.PIDFile{
		PID:        os.Getpid(),
		DataDir:    dir,
		StartedAt:  time.Now(),
		ListenAddr: ln.Addr().String(),
		SocketPath: socketPath,
	}
	if err := control.WritePIDFile(dir, clog); err != nil {
		return fmt.Errorf("write pidfile: %w", err)
	}
	cl, err := control.NewListener(socketPath)
	if err != nil {
		_ = control.RemovePIDFile(dir)
		return fmt.Errorf("control listener: %w", err)
	}
	cl.OnStop = func() error {
		s.cfg.Logger.Info("control: stop requested")
		runCancel()
		return nil
	}
	cl.OnReload = func() error {
		s.cfg.Logger.Info("control: reload requested (v0 no-op)")
		return nil
	}
	cl.OnCheckpoint = func() error {
		if s.cfg.Checkpointer == nil {
			s.cfg.Logger.Info("control: checkpoint requested but no checkpointer configured")
			return errors.New("checkpoint not configured (server has no WAL writer)")
		}
		s.cfg.Logger.Info("control: checkpoint requested")
		return s.cfg.Checkpointer.CheckpointNow()
	}
	if s.cfg.Promote != nil {
		cl.OnPromote = func() error {
			s.cfg.Logger.Info("control: promote requested")
			return s.cfg.Promote()
		}
	}
	s.controlListener = cl
	s.controlPath = socketPath
	go func() {
		if err := cl.Serve(); err != nil {
			s.cfg.Logger.Debug("control listener serve returned", "err", err)
		}
	}()
	_ = runCtx // referenced for future cancellation hooks
	return nil
}

func (s *Server) stopControlPlane() {
	if s.controlListener != nil {
		_ = s.controlListener.Close()
		s.controlListener = nil
	}
	if s.cfg.DataDir != "" {
		_ = control.RemovePIDFile(s.cfg.DataDir)
	}
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
	// PostgreSQL streaming replication: the standby's libpq client
	// passes `replication=true` (or `database`, `1`) in the
	// StartupMessage parameter bag to request a replication-mode
	// connection. We tag the per-conn ctx so runPostStartupLoop can
	// route IDENTIFY_SYSTEM / START_REPLICATION etc. through the
	// walsender path instead of the regular SQL dispatcher. See
	// docs/design/0005-0001-streaming-replication-architecture.md.
	isReplication := isReplicationStartupParam(params["replication"])
	if isReplication {
		logger = logger.With("replication", true)
	}
	logger = logger.With("user", user, "application_name", app)

	if !s.checkAuth(raw, r, w, params, logger) {
		return
	}
	logger.Info("connection established")

	sess := config.NewSessionRegistry(s.cfg.Registry)
	// Echo StartupMessage values for variables clients commonly send.
	// Failures are not fatal — clients send a wide variety of values
	// and we want to keep the connection going.
	if app != "" {
		_ = sess.Set("application_name", app, false)
	}
	if user != "" {
		// session_authorization is FlagReport; use a layer-write so the
		// effective value matches.
		_ = sess.Set("session_authorization", user, false)
	}

	// Now wire the per-session ParameterStatus emitter so subsequent
	// SET application_name = 'X' (etc.) auto-emits.
	sess.SetReportableHook(func(name, value string) {
		if err := w.WriteParameterStatus(name, value); err != nil {
			logger.Debug("ParameterStatus write failed", "name", name, "err", err)
			return
		}
		_ = w.Flush()
	})

	if err := s.sendStartupReply(w, sess, pid); err != nil {
		logger.Info("startup reply failed", "err", err)
		return
	}

	s.runPostStartupLoop(connCtx, r, w, sess, logger, isReplication)
}

// isReplicationStartupParam interprets the StartupMessage `replication`
// parameter the way upstream PostgreSQL does: case-insensitive `true`
// or `1` enables physical replication mode; `database` enables logical
// replication (deferred in v0; treated as physical for now); empty /
// `false` / `0` / unrecognised values mean "not a replication
// connection". Mirrors postgres/src/backend/replication/walsender.c
// (`got_STOPPING`, `EnableReplicationOriginCmd`).
func isReplicationStartupParam(v string) bool {
	switch v {
	case "":
		return false
	case "0", "false", "FALSE", "False":
		return false
	}
	return true
}

// checkAuth runs the configured Policy and the corresponding wire
// exchange (Trust → AuthenticationOk; Password / MD5 → AuthRequest +
// PasswordMessage round-trip; Reject / unsupported method → FATAL
// ErrorResponse). It returns true iff the connection should proceed to
// the parameter-status block.
func (s *Server) checkAuth(raw net.Conn, r *protocol.FrameReader, w *protocol.FrameWriter, params map[string]string, logger *slog.Logger) bool {
	req := auth.Request{
		ConnType: connTypeFor(raw),
		Remote:   remoteIP(raw),
		Database: params["database"],
		User:     params["user"],
	}
	if req.Database == "" {
		// PostgreSQL convention: when no database is provided in the
		// StartupMessage, the user name is used as the database name.
		req.Database = req.User
	}
	decision := s.cfg.Policy.Match(req)
	err := auth.Exchange(decision, r, w, s.cfg.UserStore, req.User)
	switch {
	case err == nil:
		return true
	case isAuthRejected(err) || isInvalidPassword(err) || isUserUnknown(err):
		logger.Info("connection rejected",
			"err", err,
			"method", decision.Method.String(),
		)
		s.writeFatal(w, sqlstate.InvalidAuthorizationSpecification, err.Error())
		return false
	case isAuthExchangeFailure(err) || errors.Is(err, auth.ErrUnexpectedFrame):
		// Wire-level failure or client misbehaved during the exchange.
		// The connection is already in a bad state — log and close
		// without trying to emit FATAL, matching how upstream's
		// auth.c quits on EOF during a password packet read.
		logger.Info("auth exchange failed", "err", err)
		return false
	default:
		// ErrMethodUnsupported and any future errors land here.
		logger.Info("auth method not supported",
			"err", err,
			"method", decision.Method.String(),
		)
		s.writeFatal(w, sqlstate.FeatureNotSupported, err.Error())
		return false
	}
}

func isAuthRejected(err error) bool {
	var rej auth.ErrRejected
	return errors.As(err, &rej)
}

func isInvalidPassword(err error) bool {
	var bad auth.ErrInvalidPassword
	return errors.As(err, &bad)
}

func isUserUnknown(err error) bool {
	var unk auth.ErrUserUnknown
	return errors.As(err, &unk)
}

func isAuthExchangeFailure(err error) bool {
	var x auth.ErrAuthExchange
	return errors.As(err, &x)
}

// connTypeFor reports the auth.ConnType implied by the listener that
// accepted raw. v0 only listens on TCP; ConnLocal arrives once we add a
// Unix-socket listener (deferred). TLS tagging (ConnHostSSL) lands when
// TLS does.
func connTypeFor(raw net.Conn) auth.ConnType {
	if _, ok := raw.LocalAddr().(*net.UnixAddr); ok {
		return auth.ConnLocal
	}
	return auth.ConnHost
}

// remoteIP returns the client's IP, or nil for connections without one.
func remoteIP(raw net.Conn) net.IP {
	if tcp, ok := raw.RemoteAddr().(*net.TCPAddr); ok {
		return tcp.IP
	}
	return nil
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

// sendStartupReply emits the post-auth portion of the startup sequence:
// the ParameterStatus block (driven by the GUC registry), BackendKeyData,
// and ReadyForQuery. AuthenticationOk has already been written by
// checkAuth at this point.
func (s *Server) sendStartupReply(w *protocol.FrameWriter, sess *config.SessionRegistry, pid uint32) error {
	for _, kv := range sess.ReportableVariables() {
		if err := w.WriteParameterStatus(kv.Name, kv.Value); err != nil {
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

// runPostStartupLoop handles every frame after ReadyForQuery. v0 routes
// simple Query messages into handleQuery; Terminate closes the connection
// cleanly; anything else is an "unsupported" ErrorResponse followed by
// another ReadyForQuery so the client can keep going.
func (s *Server) runPostStartupLoop(ctx context.Context, r *protocol.FrameReader, w *protocol.FrameWriter, sess *config.SessionRegistry, logger *slog.Logger, isReplication bool) {
	extended := newExtendedState()
	var copyIn *copyInState
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
		if copyIn != nil {
			if f.Type == protocol.MsgTerminate {
				return
			}
			done, err := s.handleCopyInFrame(w, copyIn, f)
			if err != nil {
				return
			}
			if done {
				copyIn = nil
			}
			if err := w.Flush(); err != nil {
				return
			}
			continue
		}
		if extended.syncRequired && f.Type != protocol.MsgSync && f.Type != protocol.MsgTerminate {
			continue
		}
		switch f.Type {
		case protocol.MsgTerminate:
			return
		case protocol.MsgQuery:
			// Replication-mode connections route MsgQuery through the
			// replication-command dispatcher instead of the regular
			// SQL path. The dispatcher recognises IDENTIFY_SYSTEM /
			// CREATE_REPLICATION_SLOT / DROP_REPLICATION_SLOT and the
			// (deferred) START_REPLICATION verbs; everything else
			// falls back to the normal handler so utility commands
			// like SHOW still work for diagnostics.
			if isReplication {
				handled, err := s.handleReplicationCommand(ctx, r, w, f.Payload)
				if err != nil {
					logger.Debug("replication command write error", "err", err)
					return
				}
				if handled {
					break
				}
			}
			nextCopyIn, err := s.handleQueryOrCopy(w, sess, f.Payload)
			if err != nil {
				logger.Debug("handleQueryOrCopy write error", "err", err)
				return
			}
			copyIn = nextCopyIn
		case protocol.MsgParse:
			em := s.handleParseFrame(extended, f.Payload)
			if em != nil {
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
				break
			}
			if err := w.WriteParseComplete(); err != nil {
				return
			}
		case protocol.MsgBind:
			em := s.handleBindFrame(extended, f.Payload)
			if em != nil {
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
				break
			}
			if err := w.WriteBindComplete(); err != nil {
				return
			}
		case protocol.MsgDescribe:
			em, err := s.handleDescribeFrame(extended, f.Payload, w)
			if err != nil {
				return
			}
			if em != nil {
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
			}
		case protocol.MsgExecute:
			em, err := s.handleExecuteFrame(extended, f.Payload, w, sess)
			if err != nil {
				return
			}
			if em != nil {
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
			}
		case protocol.MsgClose:
			em := s.handleCloseFrame(extended, f.Payload)
			if em != nil {
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
				break
			}
			if err := w.WriteCloseComplete(); err != nil {
				return
			}
		case protocol.MsgSync:
			extended.syncRequired = false
			if err := w.WriteReadyForQuery(protocol.TxStatusIdle); err != nil {
				return
			}
		case protocol.MsgFlush:
			// Flush itself carries no payload and no response frame.
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
