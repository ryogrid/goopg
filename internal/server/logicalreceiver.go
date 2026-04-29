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
// See docs/design/0008-0004-apply-worker-and-tablesync.md.

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/wal"
)

// LogicalReceiverConfig parameterises a single subscriber session.
type LogicalReceiverConfig struct {
	// PrimaryAddr is `host:port` of the publisher's listener.
	PrimaryAddr string

	// User identifies the role on the publisher. Replication
	// connections still authenticate; v0 ships trust by default.
	User string

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
}

// LogicalReceiver is the subscriber-side replication client.
type LogicalReceiver struct {
	cfg    LogicalReceiverConfig
	conn   net.Conn
	r      *protocol.FrameReader
	w      *protocol.FrameWriter
	mu     sync.Mutex
	closed bool

	// applyLSN is the highest commit LSN the apply worker has
	// successfully committed. Reported back to the publisher in
	// standby-status frames so the slot's `confirmed_flush_lsn`
	// advances.
	applyLSN uint64
}

// DialLogicalReceiver opens the TCP connection, performs the v3
// startup with `replication=database`, then issues
// `START_REPLICATION SLOT name LOGICAL …`. The returned receiver
// is ready for `Run`.
func DialLogicalReceiver(ctx context.Context, cfg LogicalReceiverConfig) (*LogicalReceiver, error) {
	if cfg.PrimaryAddr == "" {
		return nil, errors.New("logicalreceiver: PrimaryAddr is required")
	}
	if cfg.SlotName == "" {
		return nil, errors.New("logicalreceiver: SlotName is required")
	}
	if cfg.Apply == nil {
		return nil, errors.New("logicalreceiver: Apply is required")
	}
	if cfg.ProtoVersion == 0 {
		cfg.ProtoVersion = 1
	}
	if cfg.StatusInterval == 0 {
		cfg.StatusInterval = 10 * time.Second
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}

	dialer := net.Dialer{Timeout: cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.PrimaryAddr)
	if err != nil {
		return nil, fmt.Errorf("logicalreceiver: dial %s: %w", cfg.PrimaryAddr, err)
	}
	rec := &LogicalReceiver{
		cfg:  cfg,
		conn: conn,
		r:    protocol.NewFrameReader(conn),
		w:    protocol.NewFrameWriter(conn),
	}
	if err := rec.handshake(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rec.startStreaming(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return rec, nil
}

// handshake performs the v3 startup with `replication=database`
// and drains frames until ReadyForQuery. The publisher's side
// is the existing goopg server.
func (r *LogicalReceiver) handshake() error {
	params := map[string]string{
		"user":        r.cfg.User,
		"replication": "database",
	}
	if err := r.w.WriteStartupMessage(params); err != nil {
		return fmt.Errorf("logicalreceiver: write startup: %w", err)
	}
	for {
		f, err := r.r.ReadFrame()
		if err != nil {
			return fmt.Errorf("logicalreceiver: handshake read: %w", err)
		}
		switch f.Type {
		case protocol.MsgErrorResponse:
			return fmt.Errorf("logicalreceiver: server rejected startup: %s",
				summariseErrorResponse(f.Payload))
		case protocol.MsgReadyForQuery:
			return nil
		}
	}
}

// startStreaming sends START_REPLICATION SLOT … LOGICAL with the
// proto-version + publication-names options block and waits for
// CopyBoth.
func (r *LogicalReceiver) startStreaming() error {
	cmd := buildStartLogicalReplicationCommand(r.cfg.SlotName, r.cfg.StartLSN, r.cfg.ProtoVersion, r.cfg.Publications)
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
		case protocol.MsgCopyBothResponse:
			return nil
		case protocol.MsgErrorResponse:
			return fmt.Errorf("logicalreceiver: START_REPLICATION rejected: %s",
				summariseErrorResponse(f.Payload))
		}
	}
}

// Run drives the receive loop until ctx is cancelled or the
// link breaks.
func (r *LogicalReceiver) Run(ctx context.Context) error {
	statusTicker := time.NewTicker(r.cfg.StatusInterval)
	defer statusTicker.Stop()

	frames := make(chan protocol.Frame, 4)
	errCh := make(chan error, 1)
	go func() {
		defer close(frames)
		for {
			f, err := r.r.ReadFrame()
			if err != nil {
				errCh <- err
				return
			}
			payload := make([]byte, len(f.Payload))
			copy(payload, f.Payload)
			select {
			case frames <- protocol.Frame{Type: f.Type, Payload: payload}:
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
				if errors.Is(err, io.EOF) {
					return nil
				}
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

func (r *LogicalReceiver) handleFrame(f protocol.Frame) error {
	switch f.Type {
	case protocol.MsgCopyData:
		return r.handleCopyData(f.Payload)
	case protocol.MsgCopyDone:
		return io.EOF
	case protocol.MsgErrorResponse:
		return fmt.Errorf("logicalreceiver: server error mid-stream: %s",
			summariseErrorResponse(f.Payload))
	}
	return nil
}

func (r *LogicalReceiver) handleCopyData(payload []byte) error {
	parsed, kind, err := protocol.DecodeReplicationMessage(payload)
	if err != nil {
		return fmt.Errorf("logicalreceiver: decode replication message: %w", err)
	}
	switch kind {
	case protocol.ReplMsgWALData:
		m := parsed.(*protocol.WALDataMessage)
		if len(m.WALBytes) == 0 {
			return nil
		}
		// Each 'w' payload carries one pgoutput message in
		// logical mode. Hand the bytes to the decoder, then
		// dispatch through the apply worker.
		decoded, err := wal.DecodeMessage(m.WALBytes)
		if err != nil {
			return fmt.Errorf("logicalreceiver: pgoutput decode: %w", err)
		}
		commitLSN, err := r.cfg.Apply.ApplyMessage(decoded)
		if err != nil {
			r.cfg.Apply.SafeRollback()
			return fmt.Errorf("logicalreceiver: apply pgoutput kind=%q: %w",
				decoded.Kind, err)
		}
		// Advance applyLSN on every commit so the next
		// standby-status frame moves the slot's
		// confirmed_flush_lsn forward.
		if commitLSN > 0 {
			r.mu.Lock()
			if commitLSN > r.applyLSN {
				r.applyLSN = commitLSN
			}
			r.mu.Unlock()
		}
	case protocol.ReplMsgKeepalive:
		ka := parsed.(*protocol.KeepaliveMessage)
		if ka.ReplyRequested {
			return r.sendStatus()
		}
	}
	return nil
}

func (r *LogicalReceiver) sendStatus() error {
	r.mu.Lock()
	apply := r.applyLSN
	r.mu.Unlock()
	frame := protocol.EncodeStandbyStatusUpdate(apply, apply, apply, time.Now().UTC(), false)
	if err := r.w.WriteCopyData(frame); err != nil {
		return err
	}
	return r.w.Flush()
}

// ApplyLSN returns the last commit LSN the apply worker has
// committed locally. Useful for tests + observability.
func (r *LogicalReceiver) ApplyLSN() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applyLSN
}

// Close terminates the connection. Idempotent.
func (r *LogicalReceiver) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	r.cfg.Apply.SafeRollback()
	return r.conn.Close()
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
