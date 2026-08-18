// Standby-side walreceiver: connects to a primary, opens a
// replication-mode session, issues START_REPLICATION, and persists
// each received WAL byte stream into the local WAL writer.
//
// The walreceiver is the standby's mirror of the primary's walsender.
// It speaks the same v3 wire protocol via internal/protocol's
// FrameReader/Writer — no third-party libpq dependency. WAL records
// arriving as `'w'` CopyData payloads are unwrapped and written into
// the local writer. PostgreSQL physical replication forwards raw WAL
// stream bytes, including page headers and record framing, so those
// chunks must be preserved verbatim. goopg's native walsender still
// forwards decoded record payloads, so the standby re-encodes those
// through the normal Append path.
//
// Status updates on the back-channel: every
// `wal_receiver_status_interval` (default 10s, mirroring upstream)
// the receiver emits an `'r'` standby-status CopyData with its
// current ApplyLSN. The primary uses these to advance the
// corresponding slot's `confirmed_flush_lsn`.
//
// This file also carries the standby-side launcher (StartWalReceiver — the
// reconnect-with-backoff loop upstream keeps in walreceiver.c's
// WalReceiverMain) and the `primary_conninfo` parsing that upstream splits
// into libpqwalreceiver.c's libpqrcv_connect.
//
// See docs/design/0005-0001-streaming-replication-architecture.md.
package replication

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// WalReceiverConfig parameterises a single walreceiver session.
type WalReceiverConfig struct {
	// PrimaryAddr is `host:port` of the upstream primary's listener.
	PrimaryAddr string

	// User identifies the role on the primary. Replication
	// connections still authenticate; v0 ships trust by default
	// so any non-empty user works.
	User string

	// SlotName, when non-empty, names the physical replication
	// slot to acquire on the primary. The slot must already
	// exist (a future loop adds CREATE_REPLICATION_SLOT-on-startup
	// for the convenience case).
	SlotName string

	// StartLSN is the starting position for the stream. 0 means
	// "from the beginning" (matches upstream's `0/0` shorthand).
	StartLSN uint64

	// WAL is the local WAL writer where received records land.
	WAL *xlog.Writer

	// StatusInterval throttles standby-status updates. Zero falls
	// back to 10s, matching upstream's default
	// `wal_receiver_status_interval`.
	StatusInterval time.Duration

	// DialTimeout caps how long Connect blocks on the TCP dial +
	// startup handshake. Zero falls back to 10s.
	DialTimeout time.Duration

	// Receivers, when set, is the process-wide observability
	// registry the receiver registers itself into so
	// pg_stat_wal_receiver renders a row. nil makes registration a
	// no-op (suitable for unit tests that don't care about
	// observability).
	Receivers *xlog.Receivers

	// Conninfo is the original conninfo string the start-up code
	// parsed before constructing PrimaryAddr. Stored verbatim in
	// pg_stat_wal_receiver.conninfo so operators can see what was
	// configured. Empty falls back to PrimaryAddr.
	Conninfo string

	// ApplicationName, when non-empty, is sent as the
	// `application_name` startup parameter so the primary can
	// identify this standby for `synchronous_standby_names`
	// matching (M0102-0005). Empty disables sync-replication
	// participation — the standby still streams, just async.
	ApplicationName string

	// ApplyLSNFunc, when non-nil, is consulted by sendStatus to
	// report the standby's apply LSN. Distinct from the
	// receive/append LSN: apply lags when a replay backlog exists.
	// nil falls back to reporting applyLSN (received-LSN) for all
	// three fields, matching the v0 (sync-replication-disabled)
	// behaviour.
	ApplyLSNFunc func() uint64

	// SSLMode is the libpq sslmode requested in primary_conninfo.
	// Empty is treated as libpq's "prefer" default. goopg has no TLS
	// implementation, so "disable"/"allow"/"prefer" all connect in
	// plaintext (matching what "prefer" would negotiate down to
	// against any server that doesn't speak SSL); "require",
	// "verify-ca", and "verify-full" are rejected by DialWalReceiver
	// instead of silently downgrading to plaintext, since that would
	// defeat the operator's explicit encryption requirement.
	SSLMode string
}

// WalReceiver is the standby-side replication client. Construct via
// `DialWalReceiver` (which performs the TCP dial + startup
// handshake), then call `Run(ctx)` to drain WAL until the context is
// cancelled or the connection breaks.
type WalReceiver struct {
	cfg    WalReceiverConfig
	conn   net.Conn
	r      *libpq.FrameReader
	w      *libpq.FrameWriter
	mu     sync.Mutex
	closed bool

	// applyLSN tracks the last record successfully Append'd to the
	// local writer. Re-read by the status-update goroutine via the
	// writer's own WrittenLSN(), but we cache for a tiny perf win
	// and to make the test surface predictable.
	applyLSN uint64

	// monHandle is the observability registry handle this receiver
	// publishes progress through. nil when cfg.Receivers was unset
	// (in which case all the SetReceivedLSN / MarkMessageReceived
	// calls become no-ops via the nil-check helpers below).
	monHandle *xlog.Receiver
}

// checkSSLMode rejects sslmode values that promise encryption goopg
// cannot deliver. goopg has no TLS implementation, so a replication
// connection is always plaintext; "disable"/"allow"/"prefer" (and
// unset, which libpq defaults to "prefer") accept that transparently,
// matching what those modes would negotiate down to against any real
// server that doesn't speak SSL. "require"/"verify-ca"/"verify-full"
// are refused instead of silently connecting in plaintext, since that
// would defeat an operator's explicit encryption requirement.
func checkSSLMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "disable", "allow", "prefer":
		return nil
	case "require", "verify-ca", "verify-full":
		return fmt.Errorf("walreceiver: sslmode=%s requires TLS, which goopg does not yet implement (use disable, allow, or prefer)", mode)
	default:
		return fmt.Errorf("walreceiver: invalid sslmode value %q", mode)
	}
}

// DialWalReceiver opens the TCP connection, performs the v3 startup
// handshake with `replication=true`, optionally consumes the auth /
// parameter status / ReadyForQuery handshake, then issues
// `START_REPLICATION` and confirms the `CopyBothResponse`. The
// returned receiver is ready for `Run`.
func DialWalReceiver(ctx context.Context, cfg WalReceiverConfig) (*WalReceiver, error) {
	if cfg.PrimaryAddr == "" {
		return nil, errors.New("walreceiver: PrimaryAddr is required")
	}
	if cfg.WAL == nil {
		return nil, errors.New("walreceiver: WAL is required")
	}
	if cfg.User == "" {
		cfg.User = "postgres"
	}
	if cfg.StatusInterval <= 0 {
		cfg.StatusInterval = 10 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if err := checkSSLMode(cfg.SSLMode); err != nil {
		return nil, err
	}

	dialCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(dialCtx, "tcp", cfg.PrimaryAddr)
	if err != nil {
		return nil, fmt.Errorf("walreceiver: dial %s: %w", cfg.PrimaryAddr, err)
	}
	rec := &WalReceiver{
		cfg:  cfg,
		conn: conn,
		r:    libpq.NewFrameReader(conn),
		w:    libpq.NewFrameWriter(conn),
	}
	if err := rec.handshake(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rec.startStreaming(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	rec.applyLSN = cfg.StartLSN
	if cfg.Receivers != nil {
		conninfo := cfg.Conninfo
		if conninfo == "" {
			conninfo = cfg.PrimaryAddr
		}
		rec.monHandle = cfg.Receivers.Register(xlog.ReceiverState{
			Status:          "streaming",
			PID:             uint32(os.Getpid()),
			ReceiveStartLSN: cfg.StartLSN,
			ReceivedLSN:     cfg.StartLSN,
			SenderHost:      cfg.PrimaryAddr,
			SlotName:        cfg.SlotName,
			Conninfo:        conninfo,
		})
	}
	return rec, nil
}

// handshake performs the v3 startup with replication=true and drains
// frames until ReadyForQuery. The primary's side is the existing
// goopg server; trust auth means we just see AuthenticationOk +
// ParameterStatus block + BackendKeyData + ReadyForQuery.
func (r *WalReceiver) handshake() error {
	params := map[string]string{
		"user":        r.cfg.User,
		"replication": "true",
	}
	if r.cfg.ApplicationName != "" {
		params["application_name"] = r.cfg.ApplicationName
	}
	if err := r.w.WriteStartupMessage(params); err != nil {
		return fmt.Errorf("walreceiver: write startup: %w", err)
	}
	for {
		f, err := r.r.ReadFrame()
		if err != nil {
			return fmt.Errorf("walreceiver: handshake read: %w", err)
		}
		switch f.Type {
		case libpq.MsgErrorResponse:
			return fmt.Errorf("walreceiver: server rejected startup: %s",
				summariseErrorResponse(f.Payload))
		case libpq.MsgReadyForQuery:
			return nil
		}
	}
}

// startStreaming sends START_REPLICATION and waits for the CopyBoth
// handoff. Any ErrorResponse mid-handshake is reported to the caller
// for human triage.
func (r *WalReceiver) startStreaming() error {
	cmd := buildStartReplicationCommand(r.cfg.SlotName, r.cfg.StartLSN)
	if err := r.w.WriteQuery(cmd); err != nil {
		return fmt.Errorf("walreceiver: write START_REPLICATION: %w", err)
	}
	if err := r.w.Flush(); err != nil {
		return fmt.Errorf("walreceiver: flush START_REPLICATION: %w", err)
	}
	for {
		f, err := r.r.ReadFrame()
		if err != nil {
			return fmt.Errorf("walreceiver: start-stream read: %w", err)
		}
		switch f.Type {
		case libpq.MsgCopyBothResponse:
			return nil
		case libpq.MsgErrorResponse:
			return fmt.Errorf("walreceiver: START_REPLICATION rejected: %s",
				summariseErrorResponse(f.Payload))
		}
		// Other frames pre-CopyBoth (NoticeResponse, etc.) are
		// ignored.
	}
}

// Run drains WAL records from the connection until ctx is cancelled
// or the link breaks. Each WAL-data frame is unwrapped and Append'd
// to the local writer; periodic standby-status updates flow back to
// the primary every `StatusInterval`. Returns the first error it
// can't recover from; nil when ctx is cancelled cleanly.
func (r *WalReceiver) Run(ctx context.Context) error {
	statusTicker := time.NewTicker(r.cfg.StatusInterval)
	defer statusTicker.Stop()

	frames := make(chan libpq.Frame, 4)
	errCh := make(chan error, 1)
	go func() {
		defer close(frames)
		for {
			f, err := r.r.ReadFrame()
			if err != nil {
				errCh <- err
				return
			}
			// FrameReader's payload buffer is reused; copy.
			payload := make([]byte, len(f.Payload))
			copy(payload, f.Payload)
			select {
			case frames <- libpq.Frame{Type: f.Type, Payload: payload}:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = r.Close()
			return nil
		case err := <-errCh:
			_ = r.Close()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case f, ok := <-frames:
			if !ok {
				return nil
			}
			if err := r.handleFrame(f); err != nil {
				_ = r.Close()
				return err
			}
		case <-statusTicker.C:
			if err := r.sendStatus(); err != nil {
				_ = r.Close()
				return err
			}
		}
	}
}

// handleFrame dispatches one server frame. WAL-data is appended into
// the local writer; keepalives with replyRequested=true trigger an
// immediate status update; CopyDone closes the stream.
func (r *WalReceiver) handleFrame(f libpq.Frame) error {
	switch f.Type {
	case libpq.MsgCopyData:
		return r.handleCopyData(f.Payload)
	case libpq.MsgCopyDone:
		return io.EOF // caller treats EOF as clean stream end
	case libpq.MsgErrorResponse:
		return fmt.Errorf("walreceiver: server error mid-stream: %s",
			summariseErrorResponse(f.Payload))
	}
	return nil // unknown frame: ignore (forward-compatibility)
}

func (r *WalReceiver) handleCopyData(payload []byte) error {
	parsed, kind, err := libpq.DecodeReplicationMessage(payload)
	if err != nil {
		return fmt.Errorf("walreceiver: decode replication message: %w", err)
	}
	switch kind {
	case libpq.ReplMsgWALData:
		m := parsed.(*libpq.WALDataMessage)
		if len(m.WALBytes) == 0 {
			// Empty advance: just track endLSN.
			r.mu.Lock()
			if m.EndLSN > r.applyLSN {
				r.applyLSN = m.EndLSN
			}
			r.mu.Unlock()
			r.publishProgress(m.EndLSN)
			return nil
		}
		appendVerbatim := m.EndLSN > m.StartLSN && uint64(len(m.WALBytes)) == m.EndLSN-m.StartLSN
		var end uint64
		if appendVerbatim {
			expectedStart := r.cfg.WAL.WrittenLSN()
			payload := m.WALBytes
			if m.StartLSN != 0 && m.StartLSN != expectedStart {
				slog.Info("walreceiver WALData start mismatch",
					"start_lsn", m.StartLSN,
					"end_lsn", m.EndLSN,
					"expected_start_lsn", expectedStart,
					"bytes", len(m.WALBytes))
				switch {
				case m.StartLSN < expectedStart:
					trim := expectedStart - m.StartLSN
					if trim >= uint64(len(payload)) {
						end = r.cfg.WAL.WrittenLSN()
						break
					}
					payload = payload[int(trim):]
				case m.StartLSN > expectedStart:
					return fmt.Errorf("walreceiver: raw WAL gap: start_lsn=%d expected_start_lsn=%d end_lsn=%d", m.StartLSN, expectedStart, m.EndLSN)
				}
			}
			if end == 0 && len(payload) > 0 {
				_, end, err = r.cfg.WAL.AppendRaw(payload)
			} else if end == 0 {
				end = r.cfg.WAL.WrittenLSN()
			}
		} else {
			_, end, err = r.cfg.WAL.Append(m.WALBytes)
		}
		if err != nil {
			return fmt.Errorf("walreceiver: append local WAL: %w", err)
		}
		r.mu.Lock()
		if end > r.applyLSN {
			r.applyLSN = end
		}
		r.mu.Unlock()
		r.publishProgress(end)
	case libpq.ReplMsgKeepalive:
		ka := parsed.(*libpq.KeepaliveMessage)
		r.publishProgress(0)
		if ka.ReplyRequested {
			return r.sendStatus()
		}
	}
	return nil
}

// publishProgress pushes the latest received LSN + a "message just
// arrived" timestamp into the observability registry. Both updates
// are no-ops when monHandle is nil. lsn==0 means "no LSN to report"
// (e.g. a keepalive with no advance) and only the timestamp moves.
func (r *WalReceiver) publishProgress(lsn uint64) {
	if r.monHandle == nil {
		return
	}
	if lsn != 0 {
		r.monHandle.SetReceivedLSN(lsn)
	}
	r.monHandle.MarkMessageReceived(time.Now())
}

// sendStatus emits an 'r' standby-status CopyData payload reporting
// the current write / flush / apply LSNs. v0's standby still has no
// separate write/flush staging — write_lsn and flush_lsn both report
// the received-and-appended position — but apply_lsn is read from
// ApplyLSNFunc when wired so the primary's SyncRep wait at
// `synchronous_commit=remote_apply` sees real replay progress
// instead of treating "received" as "applied". M0102-0005.
func (r *WalReceiver) sendStatus() error {
	r.mu.Lock()
	received := r.applyLSN
	r.mu.Unlock()
	apply := received
	if r.cfg.ApplyLSNFunc != nil {
		if v := r.cfg.ApplyLSNFunc(); v > 0 {
			apply = v
		}
	}
	frame := libpq.EncodeStandbyStatusUpdate(received, received, apply, time.Now().UTC(), false)
	if err := r.w.WriteCopyData(frame); err != nil {
		return err
	}
	return r.w.Flush()
}

// ApplyLSN returns the last LSN successfully appended to the local
// WAL. Useful for tests that need to observe the receiver's
// progress.
func (r *WalReceiver) ApplyLSN() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applyLSN
}

// Close terminates the underlying connection. Safe to call multiple
// times; subsequent Run invocations on the same receiver will
// short-circuit.
func (r *WalReceiver) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	if r.cfg.Receivers != nil && r.monHandle != nil {
		r.cfg.Receivers.Unregister(r.monHandle)
	}
	return r.conn.Close()
}

// buildStartReplicationCommand renders the wire-level command
// string. Mirrors upstream's libpq walreceiver shape so a goopg
// primary or upstream PostgreSQL primary parse it identically.
func buildStartReplicationCommand(slotName string, startLSN uint64) string {
	if slotName != "" {
		return fmt.Sprintf("START_REPLICATION SLOT %s PHYSICAL %s",
			slotName, formatLSN(startLSN))
	}
	return fmt.Sprintf("START_REPLICATION PHYSICAL %s", formatLSN(startLSN))
}

// WalReceiverLauncherConfig is the narrow slice of the standby runtime the
// launcher needs. It exists so this package does not have to import
// internal/initdb just to reach two fields — the same "carry a narrow Config"
// rule the package doc states for the walsender Handler.
type WalReceiverLauncherConfig struct {
	// WAL is the local WAL writer received records land in, and whose
	// WrittenLSN seeds each (re)connect's StartLSN.
	WAL *xlog.Writer

	// Receivers is the process-wide observability registry backing
	// pg_stat_wal_receiver. nil disables registration.
	Receivers *xlog.Receivers

	// Registry supplies primary_conninfo, primary_slot_name and
	// wal_receiver_status_interval. Upstream's WalReceiverMain reads the
	// same GUCs directly. nil falls back to the built-in defaults, which
	// means an empty conninfo and therefore no connection attempt.
	Registry *misc.Registry

	// Logger receives the dial/connect/disconnect lifecycle events. nil
	// means slog.Default().
	Logger *slog.Logger

	// ApplyLSNFunc is forwarded to WalReceiverConfig.ApplyLSNFunc so
	// standby status updates report real replay progress.
	ApplyLSNFunc func() uint64
}

// StartWalReceiver dials the primary identified by `primary_conninfo`
// and runs a `WalReceiver` in a goroutine, reconnecting with
// exponential backoff on transient failures. Returns once the
// goroutine is launched; the supplied `done` channel closes when
// the goroutine exits (after the parent context is cancelled).
// Upstream's equivalent is walreceiver.c's WalReceiverMain retry loop.
//
// `primary_conninfo` is parsed as a libpq-style key=value bag; v0
// honours `host` + `port` (anything else is ignored). Empty conninfo
// is logged and the function returns without spawning — useful for
// integration tests that exercise the standby-mode boot path
// without an actual primary.
func StartWalReceiver(ctx context.Context, done chan struct{}, cfg WalReceiverLauncherConfig) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	registry := cfg.Registry
	conninfo := ""
	slotName := ""
	statusInterval := 10 * time.Second
	if registry != nil {
		if v, ok := registry.Get("primary_conninfo"); ok {
			conninfo = v.Display()
		}
		if v, ok := registry.Get("primary_slot_name"); ok {
			slotName = v.Display()
		}
		if v, ok := registry.Get("wal_receiver_status_interval"); ok {
			if secs, err := strconv.Atoi(v.Display()); err == nil && secs > 0 {
				statusInterval = time.Duration(secs) * time.Second
			}
		}
	}
	addr, appName, user, sslmode := parsePrimaryConninfoFull(conninfo)
	if addr == "" {
		logger.Info("standby mode: primary_conninfo empty or missing host:port; walreceiver not started")
		close(done)
		return
	}
	if user == "" {
		user = "postgres"
	}
	logger.Info("standby mode: starting walreceiver",
		"primary", addr, "slot", slotName, "status_interval", statusInterval)
	go func() {
		defer close(done)
		const baseBackoff = 500 * time.Millisecond
		const maxBackoff = 30 * time.Second
		backoff := baseBackoff
		for {
			if ctx.Err() != nil {
				return
			}
			// StartLSN is the LSN of the next record's first byte —
			// i.e., WrittenLSN+1 (one past the last byte already in
			// our local WAL). Sending WrittenLSN itself would make the
			// primary's iterator anchor inside the last-applied record
			// and stream garbage.
			rec, err := DialWalReceiver(ctx, WalReceiverConfig{
				PrimaryAddr:     addr,
				User:            user,
				SlotName:        slotName,
				StartLSN:        cfg.WAL.WrittenLSN() + 1,
				WAL:             cfg.WAL,
				StatusInterval:  statusInterval,
				DialTimeout:     10 * time.Second,
				Receivers:       cfg.Receivers,
				Conninfo:        conninfo,
				ApplicationName: appName,
				ApplyLSNFunc:    cfg.ApplyLSNFunc,
				SSLMode:         sslmode,
			})
			if err != nil {
				logger.Warn("walreceiver dial failed; will retry",
					"event", xlog.EventWalreceiverDialFailed,
					"primary", addr, "err", err, "backoff", backoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < maxBackoff {
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
				continue
			}
			backoff = baseBackoff
			logger.Info("walreceiver connected",
				"event", xlog.EventWalreceiverConnected,
				"primary", addr, "slot", slotName,
				"start_lsn", cfg.WAL.WrittenLSN()+1)
			runErr := rec.Run(ctx)
			lastApplied := rec.ApplyLSN()
			_ = rec.Close()
			if ctx.Err() != nil {
				return
			}
			if runErr != nil {
				logger.Warn("walreceiver disconnect; will reconnect",
					"event", xlog.EventWalreceiverDisconnect,
					"primary", addr, "apply_lsn", lastApplied, "err", runErr)
			} else {
				logger.Info("walreceiver disconnect (clean); will reconnect",
					"event", xlog.EventWalreceiverDisconnect,
					"primary", addr, "apply_lsn", lastApplied)
			}
		}
	}()
}

// parsePrimaryConninfo extracts the host:port from a libpq-style
// `key=value [key=value ...]` conninfo string. Defaults port to 5432
// when host is given without one. Returns "" when no host is
// provided. v0 honours host + port + sslmode; password follows in a
// later loop once the replication connection speaks an auth
// challenge (today it's trust-only, so a password has nowhere to go).
func parsePrimaryConninfo(conninfo string) string {
	addr, _, _, _ := parsePrimaryConninfoFull(conninfo)
	return addr
}

// parsePrimaryConninfoFull extracts host:port, application_name, user
// override, and sslmode (if any) from a libpq-style `key=value
// [key=value ...]` conninfo string. host:port defaults port to 5432;
// missing host yields empty addr. application_name is forwarded to
// the primary in the startup parameters so SyncRep can match the
// standby against synchronous_standby_names. M0102-0005. sslmode
// defaults to libpq's "prefer" when unset; DialWalReceiver rejects
// require/verify-ca/verify-full since goopg has no TLS implementation
// (falling back to plaintext there would silently defeat the
// operator's explicit encryption requirement).
func parsePrimaryConninfoFull(conninfo string) (addr, appName, user, sslmode string) {
	conninfo = strings.TrimSpace(conninfo)
	if conninfo == "" {
		return "", "", "", ""
	}
	host := ""
	port := "5432"
	for _, tok := range strings.Fields(conninfo) {
		eq := strings.IndexByte(tok, '=')
		if eq < 0 {
			continue
		}
		k := strings.ToLower(tok[:eq])
		v := tok[eq+1:]
		switch k {
		case "host":
			host = v
		case "port":
			port = v
		case "application_name":
			appName = v
		case "user":
			user = v
		case "sslmode":
			sslmode = v
		}
	}
	if host == "" {
		return "", appName, user, sslmode
	}
	return host + ":" + port, appName, user, sslmode
}
