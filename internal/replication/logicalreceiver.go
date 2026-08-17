// Subscriber-side logical-replication receiver: connects to a
// publisher, opens a replication-mode session, issues
// `START_REPLICATION SLOT name LOGICAL ...`, and feeds the pgoutput
// byte stream into a local *executor.ApplyWorker.
//
// Mirrors the structure of WalReceiver (the physical-replication
// client) — both speak the same v3 wire protocol via
// internal/protocol's FrameReader/Writer with no third-party libpq
// dependency. The logical variant differs in three places:
//
//   - START_REPLICATION uses the LOGICAL keyword and the
//     `("proto_version" '1', "publication_names" 'p1,p2')` option
//     block.
//   - Each `'w'` (WAL data) CopyData payload carries one pgoutput
//     message, not a chunk of raw WAL. The receiver routes those
//     payloads through `wal.DecodeMessage` → `ApplyWorker.ApplyMessage`.
//   - The standby-status `'r'` reply reports the `confirmed_flush_lsn`
//     the apply worker has applied, advancing the slot on the
//     publisher.
//
// See docs/design/0008-0004-apply-worker-and-tablesync.md and
// docs/design/0103-0002-apply-worker-reconnect.md (Run's reconnect
// loop and bounded backoff).

package replication

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// Default reconnect parameters. Picked to match upstream's
// `wal_retrieve_retry_interval` (PG hard-codes a 10 s wait); the
// exponential variant gives a quicker first reconnect for transient
// blips while still capping at a humane upper bound.
const (
	defaultInitialBackoff = 1 * time.Second
	defaultMaxBackoff     = 30 * time.Second
)

// LogicalReceiverConfig parameterises a single subscriber session.
type LogicalReceiverConfig struct {
	// PrimaryAddr is `host:port` of the publisher's listener.
	PrimaryAddr string

	// User identifies the role on the publisher. Replication
	// connections still authenticate; v0 ships trust by default.
	User string

	// ApplicationName, when non-empty, is sent as the
	// `application_name` startup parameter so the publisher's
	// pg_stat_replication row and any matching
	// synchronous_standby_names rule see this apply worker under
	// its subscription-configured name. Empty means "no
	// application_name parameter", matching pre-M0103-0005 behaviour.
	ApplicationName string

	// SlotName names the logical replication slot to acquire.
	// The slot must already exist on the publisher (M0008 /
	// 0008-0001 loop 1's `Slots.CreateLogical`).
	SlotName string

	// Publications is the list of publication names the
	// subscriber is interested in. Empty means "all visible
	// publications" — matches upstream's
	// `publication_names`-as-empty fallback.
	Publications []string

	// StartLSN is the resume position. 0 maps to the slot's
	// current `confirmed_flush_lsn`.
	StartLSN uint64

	// ProtoVersion is the pgoutput protocol version. v0 only
	// implements v1; setting anything else is reserved for a
	// future loop.
	ProtoVersion int

	// Apply is the local apply worker that consumes decoded
	// pgoutput messages. Required.
	Apply *executor.ApplyWorker

	// StatusInterval throttles standby-status updates. Zero
	// falls back to 10s.
	StatusInterval time.Duration

	// DialTimeout caps the TCP dial + startup handshake. Zero
	// falls back to 10s.
	DialTimeout time.Duration

	// InitialBackoff is the wait after the first reconnect-eligible
	// failure; each subsequent transient failure doubles it up to
	// MaxBackoff. Zero falls back to 1s.
	InitialBackoff time.Duration

	// MaxBackoff caps the per-retry sleep regardless of how many
	// consecutive failures occur. Zero falls back to 30s.
	MaxBackoff time.Duration

	// Dialer is an injectable hook used by the reconnect loop on
	// every (re)connect. Defaults to a net.Dialer bound by
	// DialTimeout. Test code overrides this to avoid binding real
	// TCP ports.
	Dialer func(ctx context.Context) (net.Conn, error)
}

// LogicalReceiver is the subscriber-side replication client.
//
// One LogicalReceiver instance corresponds to one logical slot. Its
// Run method drives a reconnect loop: each iteration dials the
// publisher, performs the v3 handshake, issues
// `START_REPLICATION SLOT … LOGICAL <applyLSN>`, and streams
// pgoutput messages into the apply worker until the link breaks.
// On a transient disconnect the loop sleeps with bounded
// exponential backoff and reconnects, resuming from the apply
// worker's last successfully committed LSN so no row is replayed
// twice.
type LogicalReceiver struct {
	cfg LogicalReceiverConfig

	// applyLSN is updated atomically by the apply pipeline on
	// every Commit and read by the reconnect loop to set the
	// next START_REPLICATION resume point and by the standby-
	// status frame to advance the publisher slot's
	// confirmed_flush_lsn.
	applyLSN atomic.Uint64

	// mu guards the per-iteration connection state and the
	// terminal-close flag.
	mu     sync.Mutex
	conn   net.Conn
	r      *libpq.FrameReader
	w      *libpq.FrameWriter
	closed bool // permanent close via Close()
}

// errLogicalReceiverClosed is returned (and treated as permanent)
// when a runOnce attempt races with Close().
var errLogicalReceiverClosed = errors.New("logicalreceiver: closed")

// NewLogicalReceiver builds a receiver without performing any
// network I/O. The first dial happens inside Run. Use this entry
// when the caller wants the reconnect loop to own the entire
// connection lifecycle (the M0103 apply-worker launcher path).
func NewLogicalReceiver(cfg LogicalReceiverConfig) *LogicalReceiver {
	if cfg.ProtoVersion == 0 {
		cfg.ProtoVersion = 1
	}
	if cfg.StatusInterval == 0 {
		cfg.StatusInterval = 10 * time.Second
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.InitialBackoff == 0 {
		cfg.InitialBackoff = defaultInitialBackoff
	}
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = defaultMaxBackoff
	}
	rec := &LogicalReceiver{cfg: cfg}
	if cfg.StartLSN > 0 {
		rec.applyLSN.Store(cfg.StartLSN)
	}
	return rec
}

// DialLogicalReceiver builds a receiver and performs the initial
// dial+handshake+START_REPLICATION synchronously. Retained for
// callers that want to surface the very first dial error to the
// operator before launching the long-lived Run loop. Run still
// owns subsequent reconnects.
func DialLogicalReceiver(ctx context.Context, cfg LogicalReceiverConfig) (*LogicalReceiver, error) {
	if cfg.PrimaryAddr == "" && cfg.Dialer == nil {
		return nil, errors.New("logicalreceiver: PrimaryAddr or Dialer is required")
	}
	if cfg.SlotName == "" {
		return nil, errors.New("logicalreceiver: SlotName is required")
	}
	if cfg.Apply == nil {
		return nil, errors.New("logicalreceiver: Apply is required")
	}
	rec := NewLogicalReceiver(cfg)
	if err := rec.dial(ctx); err != nil {
		return nil, err
	}
	return rec, nil
}

// dial opens a fresh connection and walks through the v3 startup
// and START_REPLICATION handshakes. The resulting r.conn / r.r /
// r.w are owned by the current iteration of Run.
func (r *LogicalReceiver) dial(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errLogicalReceiverClosed
	}
	r.mu.Unlock()

	var (
		conn net.Conn
		err  error
	)
	if r.cfg.Dialer != nil {
		conn, err = r.cfg.Dialer(ctx)
	} else {
		dialer := net.Dialer{Timeout: r.cfg.DialTimeout}
		conn, err = dialer.DialContext(ctx, "tcp", r.cfg.PrimaryAddr)
	}
	if err != nil {
		return fmt.Errorf("logicalreceiver: dial %s: %w", r.cfg.PrimaryAddr, err)
	}

	fr := libpq.NewFrameReader(conn)
	fw := libpq.NewFrameWriter(conn)

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = conn.Close()
		return errLogicalReceiverClosed
	}
	r.conn, r.r, r.w = conn, fr, fw
	r.mu.Unlock()

	if err := r.handshake(); err != nil {
		r.closeConn()
		return err
	}
	if err := r.startStreaming(); err != nil {
		r.closeConn()
		return err
	}
	return nil
}

// handshake performs the v3 startup with `replication=database`
// and drains frames until ReadyForQuery. The publisher's side
// is the existing goopg server.
func (r *LogicalReceiver) handshake() error {
	params := map[string]string{
		"user":        r.cfg.User,
		"replication": "database",
	}
	if r.cfg.ApplicationName != "" {
		params["application_name"] = r.cfg.ApplicationName
	}
	if err := r.w.WriteStartupMessage(params); err != nil {
		return fmt.Errorf("logicalreceiver: write startup: %w", err)
	}
	if err := r.w.Flush(); err != nil {
		return fmt.Errorf("logicalreceiver: flush startup: %w", err)
	}
	for {
		f, err := r.r.ReadFrame()
		if err != nil {
			return fmt.Errorf("logicalreceiver: handshake read: %w", err)
		}
		switch f.Type {
		case libpq.MsgErrorResponse:
			return fmt.Errorf("logicalreceiver: server rejected startup: %s",
				summariseErrorResponse(f.Payload))
		case libpq.MsgReadyForQuery:
			return nil
		}
	}
}

// startStreaming sends START_REPLICATION SLOT … LOGICAL with the
// proto-version + publication-names options block and waits for
// CopyBoth. The start LSN is the apply worker's last commit
// (atomic) — falling back to the configured StartLSN on the very
// first iteration before any commit has been applied.
func (r *LogicalReceiver) startStreaming() error {
	start := r.applyLSN.Load()
	if start == 0 {
		start = r.cfg.StartLSN
	}
	cmd := buildStartLogicalReplicationCommand(r.cfg.SlotName, start, r.cfg.ProtoVersion, r.cfg.Publications)
	if err := r.w.WriteQuery(cmd); err != nil {
		return fmt.Errorf("logicalreceiver: write START_REPLICATION: %w", err)
	}
	if err := r.w.Flush(); err != nil {
		return fmt.Errorf("logicalreceiver: flush START_REPLICATION: %w", err)
	}
	for {
		f, err := r.r.ReadFrame()
		if err != nil {
			return fmt.Errorf("logicalreceiver: start-stream read: %w", err)
		}
		switch f.Type {
		case libpq.MsgCopyBothResponse:
			return nil
		case libpq.MsgErrorResponse:
			return fmt.Errorf("logicalreceiver: START_REPLICATION rejected: %s",
				summariseErrorResponse(f.Payload))
		}
	}
}

// Run drives the receive loop in a reconnect-aware outer loop.
// Each iteration dials the publisher, drives the frame loop until
// the link breaks, then either retries with bounded exponential
// backoff (on a transient error) or returns (on a permanent error
// or ctx cancellation).
//
// On every iteration boundary the apply worker is force-rolled-
// back to discard any partial transaction so the next iteration
// starts from a clean slate; the apply worker itself persists
// across iterations and its applyLSN drives the resume position.
func (r *LogicalReceiver) Run(ctx context.Context) error {
	backoff := r.cfg.InitialBackoff

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Reuse a pre-dialed connection (the DialLogicalReceiver
		// path) for the first iteration only. All subsequent
		// iterations dial via runOnce.
		r.mu.Lock()
		preDialed := r.conn != nil && !r.closed
		r.mu.Unlock()

		var iterErr error
		if preDialed {
			iterErr = r.streamFrames(ctx)
		} else {
			iterErr = r.runOnce(ctx)
		}

		// Per-iteration teardown: close socket + roll back any
		// half-applied transaction. The apply worker survives.
		r.closeConn()
		r.cfg.Apply.SafeRollback()

		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(iterErr, errLogicalReceiverClosed) {
			return nil
		}
		if iterErr == nil || errors.Is(iterErr, io.EOF) {
			// Clean EOF: publisher sent CopyDone or socket
			// closed cleanly. Reset backoff and reconnect
			// immediately — the publisher likely just
			// rotated; the slot still has WAL queued.
			backoff = r.cfg.InitialBackoff
			continue
		}
		if isPermanent(iterErr) {
			return iterErr
		}

		// Transient — sleep with ±20% jitter, then retry.
		jitter := time.Duration(0)
		if j := int64(backoff / 5); j > 0 {
			jitter = time.Duration(rand.Int63n(j))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff + jitter):
		}
		backoff = nextBackoff(backoff, r.cfg.MaxBackoff)
	}
}

// runOnce performs one connect → handshake → stream cycle. Errors
// returned by runOnce are classified by isPermanent in Run.
func (r *LogicalReceiver) runOnce(ctx context.Context) error {
	if err := r.dial(ctx); err != nil {
		return err
	}
	return r.streamFrames(ctx)
}

// streamFrames is the inner receive loop: read frames from r.r
// until ctx is cancelled or the link breaks. Pulled out of Run so
// the pre-dialed (DialLogicalReceiver) and reconnect-loop paths
// share the same frame-handling logic.
func (r *LogicalReceiver) streamFrames(ctx context.Context) error {
	// Snapshot the per-iteration reader under the mutex so the
	// reader goroutine doesn't race with closeConn() / Close()
	// which both nil out r.r and r.w. The reader still observes
	// the right peer because closing the conn breaks ReadFrame
	// with an error, terminating the goroutine cleanly.
	r.mu.Lock()
	fr := r.r
	r.mu.Unlock()
	if fr == nil {
		return errLogicalReceiverClosed
	}

	statusTicker := time.NewTicker(r.cfg.StatusInterval)
	defer statusTicker.Stop()

	frames := make(chan libpq.Frame, 4)
	errCh := make(chan error, 1)
	readerCtx, cancelReader := context.WithCancel(ctx)
	defer cancelReader()

	go func() {
		defer close(frames)
		for {
			f, err := fr.ReadFrame()
			if err != nil {
				select {
				case errCh <- err:
				case <-readerCtx.Done():
				}
				return
			}
			payload := make([]byte, len(f.Payload))
			copy(payload, f.Payload)
			select {
			case frames <- libpq.Frame{Type: f.Type, Payload: payload}:
			case <-readerCtx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return err
		case f, ok := <-frames:
			if !ok {
				return io.EOF
			}
			if err := r.handleFrame(f); err != nil {
				if errors.Is(err, io.EOF) {
					return io.EOF
				}
				return err
			}
		case <-statusTicker.C:
			if err := r.sendStatus(); err != nil {
				return err
			}
		}
	}
}

func (r *LogicalReceiver) handleFrame(f libpq.Frame) error {
	switch f.Type {
	case libpq.MsgCopyData:
		return r.handleCopyData(f.Payload)
	case libpq.MsgCopyDone:
		return io.EOF
	case libpq.MsgErrorResponse:
		return fmt.Errorf("logicalreceiver: server error mid-stream: %s",
			summariseErrorResponse(f.Payload))
	}
	return nil
}

func (r *LogicalReceiver) handleCopyData(payload []byte) error {
	parsed, kind, err := libpq.DecodeReplicationMessage(payload)
	if err != nil {
		return fmt.Errorf("logicalreceiver: decode replication message: %w", err)
	}
	switch kind {
	case libpq.ReplMsgWALData:
		m := parsed.(*libpq.WALDataMessage)
		if len(m.WALBytes) == 0 {
			return nil
		}
		// Each 'w' payload carries one pgoutput message in
		// logical mode. Hand the bytes to the decoder, then
		// dispatch through the apply worker.
		decoded, err := xlog.DecodeMessage(m.WALBytes)
		if err != nil {
			return fmt.Errorf("logicalreceiver: pgoutput decode: %w", err)
		}
		commitLSN, err := r.cfg.Apply.ApplyMessage(decoded)
		if err != nil {
			r.cfg.Apply.SafeRollback()
			return fmt.Errorf("logicalreceiver: apply pgoutput kind=%q: %w",
				decoded.Kind, err)
		}
		// Advance applyLSN monotonically on every commit so the
		// next standby-status frame moves the slot's
		// confirmed_flush_lsn forward and a reconnect resumes
		// at exactly the right byte.
		advanced := false
		if commitLSN > 0 {
			for {
				cur := r.applyLSN.Load()
				if commitLSN <= cur {
					break
				}
				if r.applyLSN.CompareAndSwap(cur, commitLSN) {
					advanced = true
					break
				}
			}
		}
		// Eagerly push a standby-status frame on commit so the
		// publisher's pg_stat_replication.{flush_lsn,replay_lsn}
		// reflects the freshly applied LSN within one RTT. Without
		// this the apply confirmation only reaches the publisher on
		// the next StatusInterval tick (default 10 s), which makes
		// any sync-rep-shaped invariant (M0103-0007 rung 26) slow
		// to converge. Send-error is swallowed: the next ticker tick
		// retries, and the receiver's reconnect loop will surface
		// hard link failures via the read side.
		if advanced {
			_ = r.sendStatus()
		}
	case libpq.ReplMsgKeepalive:
		ka := parsed.(*libpq.KeepaliveMessage)
		if ka.ReplyRequested {
			return r.sendStatus()
		}
	}
	return nil
}

func (r *LogicalReceiver) sendStatus() error {
	apply := r.applyLSN.Load()
	// Report flush_lsn = applyLSN so the publisher's SyncRep wait
	// queue (M0102-0005 primitive, wired in M0103-0005) can
	// release waiters once the subscriber confirms apply.
	frame := libpq.EncodeStandbyStatusUpdate(apply, apply, apply, time.Now().UTC(), false)
	r.mu.Lock()
	w := r.w
	r.mu.Unlock()
	if w == nil {
		return errLogicalReceiverClosed
	}
	if err := w.WriteCopyData(frame); err != nil {
		return err
	}
	return w.Flush()
}

// ApplyLSN returns the last commit LSN the apply worker has
// committed locally. Useful for tests + observability.
func (r *LogicalReceiver) ApplyLSN() uint64 {
	return r.applyLSN.Load()
}

// closeConn closes the active connection (if any) and clears the
// per-iteration r/w handles. Safe to call from any goroutine and
// idempotent within a single iteration. The terminal `closed`
// flag is left untouched so the reconnect loop can decide whether
// to redial.
func (r *LogicalReceiver) closeConn() {
	r.mu.Lock()
	conn := r.conn
	r.conn = nil
	r.r = nil
	r.w = nil
	r.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// Close terminates the receiver permanently. After Close returns,
// any in-flight Run loop will observe ctx cancellation or
// errLogicalReceiverClosed on its next dial attempt and exit.
// Idempotent.
func (r *LogicalReceiver) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	conn := r.conn
	r.conn = nil
	r.r = nil
	r.w = nil
	r.mu.Unlock()
	r.cfg.Apply.SafeRollback()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// nextBackoff doubles the current delay up to the cap.
func nextBackoff(cur, max time.Duration) time.Duration {
	n := cur * 2
	if n > max || n <= 0 {
		return max
	}
	return n
}

// isPermanent classifies a runOnce error: permanent errors abort
// the reconnect loop, transient errors trigger a backoff retry.
//
// Permanent conditions: server-side rejection of START_REPLICATION
// (e.g. slot doesn't exist), apply-pipeline errors that wouldn't
// be helped by a fresh connection (decode failures, schema
// divergence), and the terminal-close sentinel.
//
// Everything else — TCP resets, dial timeouts, mid-stream io.EOF
// before CopyDone — is treated as transient.
func isPermanent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errLogicalReceiverClosed) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Caller observes ctx.Err() directly; treat as
		// "stop the loop" here so we never spin.
		return true
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "START_REPLICATION rejected"):
		return true
	case strings.Contains(msg, "server rejected startup"):
		return true
	case strings.Contains(msg, "pgoutput decode"):
		return true
	case strings.Contains(msg, "apply pgoutput"):
		return true
	case strings.Contains(msg, "decode replication message"):
		return true
	}
	return false
}

// buildStartLogicalReplicationCommand renders the wire-level
// START_REPLICATION command for logical mode. Mirrors upstream
// libpq's pgoutput-bound walreceiver shape so an upstream
// PostgreSQL primary parses it identically.
func buildStartLogicalReplicationCommand(slotName string, startLSN uint64, protoVersion int, pubs []string) string {
	opts := []string{
		fmt.Sprintf("\"proto_version\" '%d'", protoVersion),
	}
	if len(pubs) > 0 {
		opts = append(opts,
			fmt.Sprintf("\"publication_names\" '%s'", strings.Join(pubs, ",")))
	}
	return fmt.Sprintf("START_REPLICATION SLOT %s LOGICAL %s (%s)",
		slotName, formatLSN(startLSN), strings.Join(opts, ", "))
}
