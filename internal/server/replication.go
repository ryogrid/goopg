// Replication-command dispatcher for connections opened with
// `replication=true` in the StartupMessage. Handles the three
// commands a v0 walreceiver needs before it can stream:
//
//   - IDENTIFY_SYSTEM                       — returns cluster identity
//   - CREATE_REPLICATION_SLOT name PHYSICAL — durable slot for retention
//   - DROP_REPLICATION_SLOT name            — release a slot
//
// START_REPLICATION + the walsender goroutine ship in the next loop
// per the implementation plan in
// docs/design/0005-0001-streaming-replication-architecture.md.
//
// Replication commands ride on top of the regular MsgQuery framing,
// just like upstream PostgreSQL — `runPostStartupLoop` peels off the
// command before the normal SQL dispatcher gets a look at it. When
// the input doesn't match a known replication verb we return
// (false, nil) so the regular handler can take it; this keeps utility
// commands like `SHOW server_version` working for diagnostics on a
// replication connection.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
	"github.com/goopg/goopg/internal/wal"
)

// handleReplicationCommand inspects a Query payload and dispatches
// recognised replication verbs. Returns (handled=true, err) when the
// command was a replication verb (regardless of success/failure),
// (false, nil) when the dispatcher should let the regular SQL path
// take it. Errors are write errors on the wire (the connection is
// likely dead); SQLSTATE-level command failures are reported via
// ErrorResponse and still return (true, nil).
func (s *Server) handleReplicationCommand(ctx context.Context, r *protocol.FrameReader, w *protocol.FrameWriter, payload []byte, appName string) (bool, error) {
	q, err := extractCString(payload)
	if err != nil {
		return false, nil // let the regular handler emit the error
	}
	trimmed := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(q), ";"))
	upper := strings.ToUpper(trimmed)
	switch {
	case upper == "IDENTIFY_SYSTEM":
		return true, s.replyIdentifySystem(w)
	case strings.HasPrefix(upper, "CREATE_REPLICATION_SLOT "):
		return true, s.replyCreateReplicationSlot(w, trimmed[len("CREATE_REPLICATION_SLOT "):])
	case strings.HasPrefix(upper, "DROP_REPLICATION_SLOT "):
		return true, s.replyDropReplicationSlot(w, trimmed[len("DROP_REPLICATION_SLOT "):])
	case strings.HasPrefix(upper, "READ_REPLICATION_SLOT "):
		return true, s.replyReadReplicationSlot(w, trimmed[len("READ_REPLICATION_SLOT "):])
	case strings.HasPrefix(upper, "START_REPLICATION"):
		return true, s.replyStartReplication(ctx, r, w, trimmed, appName)
	case strings.HasPrefix(upper, "TIMELINE_HISTORY "):
		return true, s.replyTimelineHistory(w, trimmed[len("TIMELINE_HISTORY "):])
	case upper == "BASE_BACKUP" || strings.HasPrefix(upper, "BASE_BACKUP "):
		args := ""
		if len(trimmed) > len("BASE_BACKUP") {
			args = strings.TrimSpace(trimmed[len("BASE_BACKUP"):])
		}
		return true, s.replyBaseBackup(ctx, w, args)
	}
	return false, nil
}

// replyTimelineHistory serves the upstream TIMELINE_HISTORY <tli> command.
// Argument is a single decimal TLI; the server replies with a 1-row,
// 2-column result-set (filename text, content bytea) holding the raw
// `<tli>.history` bytes from `pg_wal/`. A missing file (e.g. TLI=1
// on a freshly initialised primary) returns an empty content row —
// upstream's walreceiver treats that as "no history yet" rather than
// an error. Mirrors `postgres/src/backend/replication/walsender.c`'s
// SendTimeLineHistory branch.
func (s *Server) replyTimelineHistory(w *protocol.FrameWriter, args string) error {
	tok := strings.Fields(strings.TrimSpace(args))
	if len(tok) == 0 {
		return s.writeQueryError(w, sqlstate.SyntaxError,
			"TIMELINE_HISTORY requires a timeline ID")
	}
	tli64, err := strconv.ParseUint(tok[0], 10, 32)
	if err != nil {
		return s.writeQueryError(w, sqlstate.SyntaxError,
			fmt.Sprintf("TIMELINE_HISTORY: invalid TLI %q", tok[0]))
	}
	tli := uint32(tli64)
	walDir := s.cfg.WALDirPath
	if walDir == "" {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			"TIMELINE_HISTORY requires Config.WALDirPath to be set")
	}

	name := wal.TimelineHistoryFileName(tli)
	path := filepath.Join(walDir, name)
	content, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return s.writeQueryError(w, sqlstate.InternalError, err.Error())
		}
		// Upstream parity: TLI without a history file (typically TLI=1)
		// returns an empty content blob rather than an error.
		content = nil
	}

	if err := w.WriteRowDescription([]protocol.FieldDescription{
		{Name: "filename", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "content", TypeOID: oidBytea, TypeSize: -1, TypeModifier: -1, Format: 0},
	}); err != nil {
		return err
	}
	row := [][]byte{[]byte(name), content}
	if err := w.WriteDataRow(row); err != nil {
		return err
	}
	if err := w.WriteCommandComplete("TIMELINE_HISTORY"); err != nil {
		return err
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// replyIdentifySystem emits the four-column / one-row reply upstream's
// `walsender.c IdentifySystem()` produces. Column names and types
// follow upstream so a libpq client (and the future goopg-side
// walreceiver) parse it transparently:
//
//	systemid  : text   — pg_control identifier
//	timeline  : int4   — current timeline; v0 is single-timeline
//	xlogpos   : text   — current write LSN as `X/X` hex pair
//	dbname    : text   — empty for physical replication
func (s *Server) replyIdentifySystem(w *protocol.FrameWriter) error {
	systemID := s.cfg.SystemID
	if systemID == "" {
		// Mirror upstream's 64-bit decimal format. The real value
		// lives in pg_control's `system_identifier` field; until the
		// pg_control file lands in initdb, a fixed placeholder keeps
		// the wire shape predictable.
		systemID = "0"
	}
	var lsn uint64
	if s.cfg.WAL != nil {
		lsn = s.cfg.WAL.WrittenLSN()
	}
	if err := w.WriteRowDescription([]protocol.FieldDescription{
		{Name: "systemid", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "timeline", TypeOID: oidInt4, TypeSize: 4, TypeModifier: -1, Format: 0},
		{Name: "xlogpos", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "dbname", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
	}); err != nil {
		return err
	}
	row := [][]byte{
		[]byte(systemID),
		[]byte("1"), // timeline; v0 is single-timeline
		[]byte(formatLSN(lsn)),
		nil, // dbname is NULL for physical replication
	}
	if err := w.WriteDataRow(row); err != nil {
		return err
	}
	if err := w.WriteCommandComplete("IDENTIFY_SYSTEM"); err != nil {
		return err
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// replyCreateReplicationSlot parses the upstream CREATE_REPLICATION_SLOT
// grammar and creates a slot. The v0 subset acknowledges:
//
//	CREATE_REPLICATION_SLOT slot_name [TEMPORARY]
//	  { PHYSICAL | LOGICAL output_plugin }
//	  [ ( option [value] [, ...] ) ]
//
// Legacy positional trailing keywords (EXPORT_SNAPSHOT, NOEXPORT_SNAPSHOT,
// USE_SNAPSHOT, TWO_PHASE, RESERVE_WAL) are still accepted for
// backwards-compatibility. The parenthesised options list is the
// PG14+ shape libpqwalreceiver uses: SNAPSHOT 'export'|'use'|'nothing',
// TWO_PHASE [bool], RESERVE_WAL [bool], FAILOVER [bool]. All known
// options are no-ops in v0 because goopg does not yet ship a snapshot
// exporter; snapshot_name is always returned NULL.
func (s *Server) replyCreateReplicationSlot(w *protocol.FrameWriter, args string) error {
	if s.cfg.Slots == nil {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			"replication slots are not configured on this server")
	}
	// Split off any trailing parenthesised options list before
	// whitespace-tokenising the rest. The options block is parsed
	// separately because its values can be single-quoted strings
	// containing whitespace.
	prefix, optsBlock, hasOpts, err := splitReplicationSlotOptionsBlock(args)
	if err != nil {
		return s.writeQueryError(w, sqlstate.SyntaxError, err.Error())
	}
	tokens := strings.Fields(prefix)
	if len(tokens) < 2 {
		return s.writeQueryError(w, sqlstate.SyntaxError,
			"CREATE_REPLICATION_SLOT requires a slot name and a kind")
	}
	name := unquoteIdent(tokens[0])

	idx := 1
	if strings.EqualFold(tokens[idx], "TEMPORARY") {
		idx++
		if idx >= len(tokens) {
			return s.writeQueryError(w, sqlstate.SyntaxError,
				"CREATE_REPLICATION_SLOT TEMPORARY requires PHYSICAL or LOGICAL")
		}
	}
	kind := strings.ToUpper(tokens[idx])
	idx++

	var slotKind wal.SlotKind
	var plugin string
	switch kind {
	case "PHYSICAL":
		slotKind = wal.SlotPhysical
		// Tolerate trailing RESERVE_WAL (no-op).
		for idx < len(tokens) {
			if !strings.EqualFold(tokens[idx], "RESERVE_WAL") {
				return s.writeQueryError(w, sqlstate.SyntaxError,
					fmt.Sprintf("unexpected token %q after PHYSICAL", tokens[idx]))
			}
			idx++
		}
	case "LOGICAL":
		slotKind = wal.SlotLogical
		if idx >= len(tokens) {
			return s.writeQueryError(w, sqlstate.SyntaxError,
				"CREATE_REPLICATION_SLOT LOGICAL requires an output plugin name")
		}
		plugin = unquoteIdent(tokens[idx])
		idx++
		if !strings.EqualFold(plugin, "pgoutput") {
			return s.writeQueryError(w, sqlstate.FeatureNotSupported,
				fmt.Sprintf("logical output plugin %q is not supported (pgoutput only)", plugin))
		}
		// Legacy positional trailing keywords (pre-PG14 grammar):
		// EXPORT_SNAPSHOT | NOEXPORT_SNAPSHOT | USE_SNAPSHOT | TWO_PHASE.
		for idx < len(tokens) {
			opt := strings.ToUpper(tokens[idx])
			switch opt {
			case "EXPORT_SNAPSHOT", "NOEXPORT_SNAPSHOT", "USE_SNAPSHOT", "TWO_PHASE":
				idx++
			default:
				return s.writeQueryError(w, sqlstate.SyntaxError,
					fmt.Sprintf("unexpected token %q after LOGICAL pgoutput", tokens[idx]))
			}
		}
	default:
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			fmt.Sprintf("CREATE_REPLICATION_SLOT %s is not supported (PHYSICAL or LOGICAL pgoutput)", kind))
	}

	if hasOpts {
		if err := parseReplicationSlotOptions(optsBlock, slotKind); err != nil {
			return s.writeQueryError(w, sqlstate.SyntaxError, err.Error())
		}
	}

	// Slot RestartLSN is the LSN at which the consumer will resume
	// reading WAL — the *first byte of the next record*, not the
	// last byte of the current one. WrittenLSN() returns the last
	// appended byte's LSN, so the next record begins at +1. Without
	// the +1, NewRecordIterator's `pos = startLSN-1` would land on
	// the last byte of the previous record and the very first
	// readOneAt() would decode garbage (e.g., rmid=240 from random
	// payload bytes). Same off-by-one M0094-0005 fixed for
	// startStandbyReplayer / startWalreceiver.
	var startLSN uint64
	if s.cfg.WAL != nil {
		startLSN = s.cfg.WAL.WrittenLSN() + 1
	}
	slot, err := s.cfg.Slots.Create(name, slotKind, startLSN)
	if err != nil {
		return s.writeQueryError(w, replicationSlotErrCode(err), err.Error())
	}
	// Upstream replies with (slot_name, consistent_point, snapshot_name,
	// output_plugin). For PHYSICAL, snapshot_name and output_plugin
	// are NULL. For LOGICAL, snapshot_name is NULL in v0 and
	// output_plugin echoes the requested plugin.
	if err := w.WriteRowDescription([]protocol.FieldDescription{
		{Name: "slot_name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "consistent_point", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "snapshot_name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "output_plugin", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
	}); err != nil {
		return err
	}
	var pluginCol []byte
	if slotKind == wal.SlotLogical {
		pluginCol = []byte(plugin)
	}
	row := [][]byte{
		[]byte(slot.Name),
		[]byte(formatLSN(slot.RestartLSN)),
		nil,
		pluginCol,
	}
	if err := w.WriteDataRow(row); err != nil {
		return err
	}
	if err := w.WriteCommandComplete("CREATE_REPLICATION_SLOT"); err != nil {
		return err
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// replyDropReplicationSlot removes a slot. Argument shape:
// `name [WAIT]`. The optional `WAIT` keyword (block until the slot is
// inactive) is accepted but the v0 implementation refuses to drop an
// active slot rather than blocking — that's adequate for a synchronous
// test harness and avoids tying up a server goroutine indefinitely.
func (s *Server) replyDropReplicationSlot(w *protocol.FrameWriter, args string) error {
	if s.cfg.Slots == nil {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			"replication slots are not configured on this server")
	}
	tokens := strings.Fields(args)
	if len(tokens) == 0 {
		return s.writeQueryError(w, sqlstate.SyntaxError,
			"DROP_REPLICATION_SLOT requires a slot name")
	}
	name := unquoteIdent(tokens[0])
	if err := s.cfg.Slots.Drop(name); err != nil {
		return s.writeQueryError(w, replicationSlotErrCode(err), err.Error())
	}
	if err := w.WriteCommandComplete("DROP_REPLICATION_SLOT"); err != nil {
		return err
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// replyReadReplicationSlot serves the upstream READ_REPLICATION_SLOT
// command (PG 15+, walsender.c:ReadReplicationSlot). pg_receivewal issues
// it before START_REPLICATION to learn a physical slot's restart position
// and timeline so it can resume streaming from the right LSN. Argument
// shape is just the slot name.
//
// The result is a single row of three text/int8 columns
// (slot_type, restart_lsn, restart_tli):
//   - slot absent or invalidated  -> all three NULL (upstream returns
//     all-NULL when the slot is not in use);
//   - physical slot               -> ('physical', 'X/X', tli), with
//     restart_lsn/restart_tli NULL when the slot has no reserved LSN;
//   - logical slot                -> feature_not_supported, mirroring
//     upstream's "cannot use READ_REPLICATION_SLOT with a logical
//     replication slot".
func (s *Server) replyReadReplicationSlot(w *protocol.FrameWriter, args string) error {
	if s.cfg.Slots == nil {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			"replication slots are not configured on this server")
	}
	tokens := strings.Fields(args)
	if len(tokens) == 0 {
		return s.writeQueryError(w, sqlstate.SyntaxError,
			"READ_REPLICATION_SLOT requires a slot name")
	}
	name := unquoteIdent(tokens[0])

	if err := w.WriteRowDescription([]protocol.FieldDescription{
		{Name: "slot_type", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "restart_lsn", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		// TimeLineID is unsigned, so int8 is used (upstream INT8OID).
		{Name: "restart_tli", TypeOID: oidInt8, TypeSize: 8, TypeModifier: -1, Format: 0},
	}); err != nil {
		return err
	}

	row := [][]byte{nil, nil, nil} // default: slot absent -> all NULL
	slot, err := s.cfg.Slots.Get(name)
	if err == nil && !slot.Invalidated {
		if slot.Kind == wal.SlotLogical {
			return s.writeQueryError(w, sqlstate.FeatureNotSupported,
				"cannot use READ_REPLICATION_SLOT with a logical replication slot")
		}
		row[0] = []byte("physical")
		if slot.RestartLSN != 0 {
			row[1] = []byte(formatLSN(slot.RestartLSN))
			// goopg operates on a single timeline (see 0005-0001);
			// any reserved position is on timeline 1.
			row[2] = []byte("1")
		}
	}
	if err := w.WriteDataRow(row); err != nil {
		return err
	}
	if err := w.WriteCommandComplete("READ_REPLICATION_SLOT"); err != nil {
		return err
	}
	return w.WriteReadyForQuery(protocol.TxStatusIdle)
}

// replyStartReplication validates the START_REPLICATION command,
// flips the connection into CopyBoth streaming mode, and runs the
// walsender loop until the WAL writer is gone, the context is
// cancelled, or the client closes its side. Argument shape:
//
//	START_REPLICATION [SLOT name] PHYSICAL <X/X> [TIMELINE n]
//
// v0 supports physical only; the TIMELINE clause is accepted but a
// non-1 value is rejected with feature_not_supported (single-timeline
// operation is the documented v0 scope, see 0005-0001).
//
// `SLOT name`: when present, the named slot is acquired (made active)
// and ConfirmedFlushLSN is advanced as standby status updates
// arrive. When absent the walsender streams without slot-backed WAL
// retention — fine for one-shot test traffic, dangerous in
// production. Upstream behaves identically.
func (s *Server) replyStartReplication(ctx context.Context, r *protocol.FrameReader, w *protocol.FrameWriter, raw string, appName string) error {
	if s.cfg.WAL == nil {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			"START_REPLICATION requires a configured WAL writer")
	}
	args, err := parseStartReplicationArgs(raw)
	if err != nil {
		return s.writeQueryError(w, sqlstate.SyntaxError, err.Error())
	}
	if args.Timeline != 0 && args.Timeline != 1 {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			fmt.Sprintf("START_REPLICATION TIMELINE %d not supported (v0 is single-timeline)", args.Timeline))
	}
	if args.SlotName != "" {
		if s.cfg.Slots == nil {
			return s.writeQueryError(w, sqlstate.FeatureNotSupported,
				"replication slots are not configured on this server")
		}
		if err := s.cfg.Slots.SetActive(args.SlotName, true); err != nil {
			return s.writeQueryError(w, replicationSlotErrCode(err), err.Error())
		}
		defer func() { _ = s.cfg.Slots.SetActive(args.SlotName, false) }()
	}

	// Hand the connection over to CopyBoth streaming mode.
	if err := w.WriteCopyBothResponse(0, nil); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// LOGICAL mode runs a separate path that drives the M0008
	// pipeline (`wal.SlotDecoder` + `wal.PgOutput`) and ships
	// pgoutput messages through `'w'` CopyData frames. See
	// docs/design/0008-0004-apply-worker-and-tablesync.md.
	if args.Mode == "LOGICAL" {
		return s.runLogicalWalsender(ctx, r, w, args, appName)
	}

	walDir := s.cfg.WALDirPath
	if walDir == "" {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			"START_REPLICATION requires Config.WALDirPath to be set")
	}
	segSize := s.cfg.WALSegmentSize
	if segSize <= 0 {
		segSize = wal.DefaultSegmentSize
	}

	it, err := wal.NewRecordIterator(s.cfg.WAL, walDir, segSize, args.StartLSN)
	if err != nil {
		// The CopyBoth stream is already open; report the failure
		// and end the conversation rather than rolling back.
		return s.writeStreamingError(w, sqlstate.InternalError, err.Error())
	}
	defer it.Close()

	// Register this walsender into the process-wide observability
	// registry so the pg_stat_replication virtual view can render
	// a live row. Unregister on exit (whether clean or error). The
	// disconnect log fires on every exit path (clean stream end,
	// keepalive write failure, ctx cancel) so an operator scraping
	// for replication churn has a single event name to gate alerts
	// on (`event=walsender_disconnect`).
	var senderHandle *wal.Sender
	if s.cfg.WalSenders != nil {
		senderHandle = s.cfg.WalSenders.Register(wal.SenderState{
			SlotName:        args.SlotName,
			ClientAddr:      walsenderClientAddr(r),
			SentLSN:         args.StartLSN,
			ApplicationName: appName,
		})
		defer s.cfg.WalSenders.Unregister(senderHandle)
	}
	// M0102-0005: forget the standby's last reported progress when the
	// connection drops so a disconnected standby no longer counts toward
	// the FIRST/ANY quorum. Empty appName is harmless (ForgetStandby
	// removes the "" key, which never matches any rule entry).
	if s.cfg.SyncRep != nil && appName != "" {
		defer s.cfg.SyncRep.ForgetStandby(appName)
	}
	defer func() {
		if s.cfg.Logger == nil {
			return
		}
		s.cfg.Logger.Info("walsender disconnect",
			"event", wal.EventWalsenderDisconnect,
			"slot", args.SlotName,
			"start_lsn", args.StartLSN,
			"primary_written_lsn", s.cfg.WAL.WrittenLSN(),
		)
	}()

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	// handleStandbyCopyData also dispatches into SyncRep when configured;
	// pass appName so the dispatcher can update the per-standby progress
	// row keyed by application_name.
	syncRep := s.cfg.SyncRep

	// Receive-side goroutine: consume CopyData / CopyDone frames from
	// the standby and apply standby-status updates to the slot. The
	// goroutine ends naturally when the connection closes (ReadFrame
	// returns an error) or the streaming context is cancelled.
	// clientSentCopyDone tracks whether the client gracefully ended
	// the stream (e.g. pg_basebackup sending CopyDone); the send loop
	// responds with its own CopyDone+CommandComplete+ReadyForQuery.
	// For continuous streaming (PG standby), the client never sends
	// CopyDone, so no response is sent and the stream stays open.
	clientSentCopyDone := false
	receiveDone := make(chan struct{})
	go func() {
		defer close(receiveDone)
		for {
			f, err := r.ReadFrame()
			if err != nil {
				return
			}
			switch f.Type {
			case protocol.MsgCopyData:
				_ = s.handleStandbyCopyData(args.SlotName, f.Payload, senderHandle, syncRep, appName)
			case protocol.MsgCopyDone:
				clientSentCopyDone = true
				streamCancel()
				return
			case protocol.MsgTerminate:
				streamCancel()
				return
			default:
				// Unknown frame mid-stream — drop it; libpq's
				// walreceiver doesn't send anything else.
			}
		}
	}()

	// Send-side loop: forward contiguous raw WAL chunks as 'w'
	// WAL-data frames, emit periodic keepalives so the standby can advance its
	// progress reporting even when the primary is idle.
	const keepaliveInterval = 10 * time.Second
	keepaliveTimer := time.NewTimer(keepaliveInterval)
	defer keepaliveTimer.Stop()

	chunkCh := make(chan wal.RawChunk, 1)
	recErrCh := make(chan error, 1)
	go func() {
		for {
			chunk, err := it.NextRaw(streamCtx, wal.XLOGBlockSize)
			if err != nil {
				recErrCh <- err
				return
			}
			select {
			case chunkCh <- chunk:
			case <-streamCtx.Done():
				recErrCh <- streamCtx.Err()
				return
			}
		}
	}()

	for {
		select {
		case <-streamCtx.Done():
			<-receiveDone
			if clientSentCopyDone {
				_ = w.WriteCopyDone()
				_ = w.WriteCommandComplete("START_REPLICATION")
				_ = w.WriteReadyForQuery(protocol.TxStatusIdle)
			}
			return nil
		case <-receiveDone:
			if clientSentCopyDone {
				_ = w.WriteCopyDone()
				_ = w.WriteCommandComplete("START_REPLICATION")
				_ = w.WriteReadyForQuery(protocol.TxStatusIdle)
			}
			return nil
		case chunk := <-chunkCh:
			frame := protocol.EncodeWALData(chunk.StartLSN, chunk.EndLSN, time.Now().UTC(), chunk.Payload)
			if err := w.WriteCopyData(frame); err != nil {
				streamCancel()
				return err
			}
			if err := w.Flush(); err != nil {
				streamCancel()
				return err
			}
			if senderHandle != nil {
				senderHandle.SetSentLSN(chunk.EndLSN)
			}
			// Reset the keepalive timer — fresh WAL implies the
			// standby has just heard from us.
			if !keepaliveTimer.Stop() {
				select {
				case <-keepaliveTimer.C:
				default:
				}
			}
			keepaliveTimer.Reset(keepaliveInterval)
		case <-keepaliveTimer.C:
			frame := protocol.EncodeKeepalive(s.cfg.WAL.WrittenLSN(), time.Now().UTC(), false)
			if err := w.WriteCopyData(frame); err != nil {
				streamCancel()
				return err
			}
			if err := w.Flush(); err != nil {
				streamCancel()
				return err
			}
			keepaliveTimer.Reset(keepaliveInterval)
		case err := <-recErrCh:
			if errors.Is(err, context.Canceled) || errors.Is(err, wal.ErrClosed) {
				<-receiveDone
				if clientSentCopyDone {
					_ = w.WriteCopyDone()
					_ = w.WriteCommandComplete("START_REPLICATION")
					_ = w.WriteReadyForQuery(protocol.TxStatusIdle)
				}
				return nil
			}
			streamCancel()
			// Do NOT block on <-receiveDone here. The receiver goroutine is
			// blocked inside r.ReadFrame() — a blocking network read that
			// cannot be interrupted by context cancellation. Waiting for it
			// while the WAL iterator has already failed creates a livelock:
			// the receiver waits for the client to send CopyDone, the client
			// waits for the server to send keepalives or data, and the server
			// (this goroutine) never sends them because it exited the select.
			// Instead, return immediately and let the deferred connection close
			// (when handleConn exits) unblock the receiver via an EOF read.
			return s.writeStreamingError(w, sqlstate.InternalError, err.Error())
		}
	}
}

// handleStandbyCopyData decodes a client-side CopyData payload and
// applies the relevant slot advance. Errors are swallowed (logged at
// debug elsewhere) — a malformed status frame should not kill the
// stream. When a senderHandle is supplied the standby-reported LSNs
// are also pushed into the observability registry so
// pg_stat_replication renders write_lsn / flush_lsn / replay_lsn.
func (s *Server) handleStandbyCopyData(slotName string, payload []byte, senderHandle *wal.Sender, syncRep *wal.SyncRep, appName string) error {
	parsed, kind, err := protocol.DecodeReplicationMessage(payload)
	if err != nil {
		return err
	}
	if kind == protocol.ReplMsgStandbyStatus {
		st := parsed.(*protocol.StandbyStatusUpdate)
		if slotName != "" && s.cfg.Slots != nil {
			_ = s.cfg.Slots.AdvanceConfirmedFlushLSN(slotName, st.FlushLSN)
		}
		if senderHandle != nil {
			senderHandle.ApplyStandbyStatus(st.WriteLSN, st.FlushLSN, st.ApplyLSN)
		}
		// M0102-0005: feed the SyncRep wait primitive so any commit
		// blocking on this standby gets released as soon as the
		// acknowledged LSN reaches the commit target.
		if syncRep != nil && appName != "" {
			syncRep.UpdateStandbyProgress(appName, st.WriteLSN, st.FlushLSN, st.ApplyLSN)
		}
	}
	return nil
}

// walsenderClientAddr returns a best-effort `host:port` string for
// the standby connection backing this walsender. The frame reader
// doesn't expose its underlying net.Conn, so v0 reports "" and the
// pg_stat_replication.client_addr column ends up blank. A future
// loop can plumb the connection's RemoteAddr through Server.handleConn
// to fill this in.
func walsenderClientAddr(_ *protocol.FrameReader) string {
	return ""
}

// writeStreamingError emits an ErrorResponse on a CopyBoth-mode
// connection. Upstream's walsender does the same: error mid-stream
// goes via a regular error frame, and the standby treats it as a
// terminal disconnect.
func (s *Server) writeStreamingError(w *protocol.FrameWriter, code sqlstate.Code, msg string) error {
	return s.writeQueryError(w, code, msg)
}

// startReplicationArgs is the parsed form of the SQL command body.
type startReplicationArgs struct {
	SlotName string
	StartLSN uint64
	Timeline int
	// Mode is "PHYSICAL" or "LOGICAL". Empty defaults to
	// "PHYSICAL" — older clients omit the keyword and assume
	// physical mode.
	Mode string
	// Options carries the parsed `(key 'value' [, ...])` block
	// after the LSN in LOGICAL mode. Keys are lower-cased.
	// pgoutput consumes `proto_version` and `publication_names`.
	Options map[string]string
}

// parseStartReplicationArgs handles upstream's grammar:
//
//	START_REPLICATION [SLOT name] PHYSICAL X/X [TIMELINE n]
//	START_REPLICATION SLOT name LOGICAL X/X [( "key" 'value' [, ...] )]
//
// Tokens are case-insensitive for keywords. LSNs are upstream's
// `X/X` hex pair. The LOGICAL option block follows libpq's
// quoted-pair shape verbatim.
func parseStartReplicationArgs(raw string) (startReplicationArgs, error) {
	// Strip a trailing semicolon if present (libpq sends none,
	// but psql copy/paste adds one).
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ";")

	// Split off the optional `(...)` option block first so the
	// keyword/LSN fields-scan stays simple.
	var optionsRaw string
	if i := strings.Index(raw, "("); i >= 0 {
		j := strings.LastIndex(raw, ")")
		if j <= i {
			return startReplicationArgs{}, errors.New("START_REPLICATION: unmatched '(' in options")
		}
		optionsRaw = raw[i+1 : j]
		raw = strings.TrimSpace(raw[:i])
	}

	tokens := strings.Fields(raw)
	if len(tokens) == 0 || !strings.EqualFold(tokens[0], "START_REPLICATION") {
		return startReplicationArgs{}, errors.New("START_REPLICATION: unexpected verb")
	}
	tokens = tokens[1:]
	var out startReplicationArgs
	if len(tokens) >= 2 && strings.EqualFold(tokens[0], "SLOT") {
		out.SlotName = unquoteIdent(tokens[1])
		tokens = tokens[2:]
	}
	if len(tokens) == 0 {
		return startReplicationArgs{}, errors.New("START_REPLICATION: missing PHYSICAL/LOGICAL keyword")
	}
	modeKeywordPresent := true
	switch {
	case strings.EqualFold(tokens[0], "PHYSICAL"):
		out.Mode = "PHYSICAL"
	case strings.EqualFold(tokens[0], "LOGICAL"):
		out.Mode = "LOGICAL"
		if out.SlotName == "" {
			return startReplicationArgs{}, errors.New("START_REPLICATION LOGICAL requires SLOT name")
		}
	case isLSNToken(tokens[0]):
		out.Mode = "PHYSICAL"
		modeKeywordPresent = false
	default:
		return startReplicationArgs{}, fmt.Errorf("START_REPLICATION: unknown mode %q", tokens[0])
	}
	if modeKeywordPresent {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return startReplicationArgs{}, errors.New("START_REPLICATION: missing start LSN")
	}
	lsn, err := parseLSN(tokens[0])
	if err != nil {
		return startReplicationArgs{}, err
	}
	out.StartLSN = lsn
	tokens = tokens[1:]
	if len(tokens) >= 2 && strings.EqualFold(tokens[0], "TIMELINE") {
		tl, err := strconv.Atoi(tokens[1])
		if err != nil {
			return startReplicationArgs{}, fmt.Errorf("START_REPLICATION: invalid TIMELINE %q", tokens[1])
		}
		out.Timeline = tl
	}
	if optionsRaw != "" {
		opts, err := parseStartReplicationOptions(optionsRaw)
		if err != nil {
			return startReplicationArgs{}, err
		}
		out.Options = opts
	}
	return out, nil
}

func isLSNToken(token string) bool {
	_, err := parseLSN(token)
	return err == nil
}

// parseStartReplicationOptions parses libpq's pgoutput-style
// option block: `"key" 'value' [, "key" 'value' ...]`. Keys
// are lower-cased; values are unquoted single-quoted strings.
func parseStartReplicationOptions(raw string) (map[string]string, error) {
	out := map[string]string{}
	parts := splitStartReplicationOptionList(raw)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// "key" 'value' — find the value as the part after the
		// last whitespace, since key may be quoted with embedded
		// underscores.
		spaceIdx := strings.LastIndexAny(p, " \t")
		if spaceIdx <= 0 {
			return nil, fmt.Errorf("START_REPLICATION: malformed option %q", p)
		}
		key := strings.TrimSpace(p[:spaceIdx])
		val := strings.TrimSpace(p[spaceIdx+1:])
		key = strings.Trim(key, "\"")
		val = strings.Trim(val, "'")
		out[strings.ToLower(key)] = val
	}
	return out, nil
}

// splitStartReplicationOptionList splits on commas that are not
// inside single-quoted strings.
func splitStartReplicationOptionList(raw string) []string {
	var out []string
	inQuote := false
	start := 0
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\'':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				out = append(out, raw[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, raw[start:])
	return out
}

// splitReplicationSlotOptionsBlock locates an optional trailing
// parenthesised options list in a CREATE_REPLICATION_SLOT args string.
// Returns the prefix (everything before the `(`), the content between
// the parens (excluding the parens themselves), and whether an options
// list was present. The opening paren must appear outside any
// single-quoted string and only after the slot-kind keywords (the
// prefix tokens never contain `(` or `'` so the scan can be linear).
// An unbalanced or mid-string paren returns an error.
func splitReplicationSlotOptionsBlock(args string) (prefix, opts string, has bool, err error) {
	depth := 0
	inQuote := false
	openIdx := -1
	for i := 0; i < len(args); i++ {
		c := args[i]
		switch c {
		case '\'':
			if !inQuote {
				inQuote = true
			} else if i+1 < len(args) && args[i+1] == '\'' {
				// SQL doubled single-quote escape inside a string.
				i++
			} else {
				inQuote = false
			}
		case '(':
			if inQuote {
				continue
			}
			if depth == 0 {
				openIdx = i
			}
			depth++
		case ')':
			if inQuote {
				continue
			}
			if depth == 0 {
				return "", "", false, fmt.Errorf("CREATE_REPLICATION_SLOT: unmatched ')' in options")
			}
			depth--
			if depth == 0 {
				tail := strings.TrimSpace(args[i+1:])
				if tail != "" {
					return "", "", false, fmt.Errorf("CREATE_REPLICATION_SLOT: unexpected trailing tokens %q after options", tail)
				}
				return strings.TrimSpace(args[:openIdx]), args[openIdx+1 : i], true, nil
			}
		}
	}
	if depth != 0 {
		return "", "", false, fmt.Errorf("CREATE_REPLICATION_SLOT: options list missing closing ')'")
	}
	if inQuote {
		return "", "", false, fmt.Errorf("CREATE_REPLICATION_SLOT: unterminated single-quoted string")
	}
	return args, "", false, nil
}

// parseReplicationSlotOptions parses the comma-separated option list
// from the parenthesised form of CREATE_REPLICATION_SLOT (PG14+ shape):
//
//	(option [value] [, ...])
//
// Each option is a single keyword followed optionally by a value. The
// value can be a single-quoted string, an unquoted identifier
// (typically `true`/`false`), or an integer. Recognised options:
//
//	SNAPSHOT { 'export' | 'use' | 'nothing' } — snapshot disposition.
//	  goopg has no snapshot exporter yet, so all three values are
//	  no-ops and snapshot_name is returned NULL regardless.
//	TWO_PHASE [ boolean ]   — two-phase commit decoding (no-op).
//	RESERVE_WAL [ boolean ] — PHYSICAL only (no-op).
//	FAILOVER [ boolean ]    — PG17+ failover slots (no-op).
//
// Unknown options return a syntax error so future probe rungs surface
// loudly. RESERVE_WAL is rejected on LOGICAL slots and FAILOVER on
// PHYSICAL — both match upstream's parse_create_replication_slot.
func parseReplicationSlotOptions(raw string, kind wal.SlotKind) error {
	parts := splitStartReplicationOptionList(raw)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Split into name + optional value. The name is a single
		// keyword (no whitespace, no quotes); the value, if present,
		// follows after whitespace.
		name := p
		val := ""
		if i := strings.IndexAny(p, " \t"); i >= 0 {
			name = strings.TrimSpace(p[:i])
			val = strings.TrimSpace(p[i+1:])
		}
		key := strings.ToUpper(name)
		switch key {
		case "SNAPSHOT":
			if val == "" {
				return fmt.Errorf("CREATE_REPLICATION_SLOT: SNAPSHOT requires a value")
			}
			v := strings.ToLower(strings.Trim(val, "'"))
			switch v {
			case "export", "use", "nothing":
				// no-op
			default:
				return fmt.Errorf("CREATE_REPLICATION_SLOT: SNAPSHOT %q not recognised (export|use|nothing)", v)
			}
		case "TWO_PHASE":
			// boolean or no value (presence implies true) — no-op.
		case "RESERVE_WAL":
			if kind == wal.SlotLogical {
				return fmt.Errorf("CREATE_REPLICATION_SLOT: RESERVE_WAL is not valid for LOGICAL slots")
			}
		case "FAILOVER":
			if kind == wal.SlotPhysical {
				return fmt.Errorf("CREATE_REPLICATION_SLOT: FAILOVER is not valid for PHYSICAL slots")
			}
		default:
			return fmt.Errorf("CREATE_REPLICATION_SLOT: unrecognised option %q", name)
		}
	}
	return nil
}

// parseLSN parses upstream's `X/X` hex notation back into a uint64.
// Empty / "0/0" → 0 (start of WAL).
func parseLSN(s string) (uint64, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid LSN %q (want X/X)", s)
	}
	hi, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: %w", s, err)
	}
	lo, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: %w", s, err)
	}
	return (hi << 32) | lo, nil
}

// formatLSN renders an LSN in upstream's `X/X` notation. PostgreSQL's
// pg_lsn is an unsigned 64-bit byte position split into two 32-bit
// halves, hex-formatted. Walprotocol payloads carry the value as a
// big-endian uint64; this is just for the human-facing wire columns.
func formatLSN(lsn uint64) string {
	return fmt.Sprintf("%X/%X", uint32(lsn>>32), uint32(lsn))
}

// unquoteIdent strips wrapping double quotes if present; otherwise
// returns the input verbatim. Matches the way replication-protocol
// clients send slot names ("primary" vs primary).
func unquoteIdent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// replicationSlotErrCode maps wal.ErrSlot* sentinels to upstream-
// aligned SQLSTATEs the wire ErrorResponse should advertise.
func replicationSlotErrCode(err error) sqlstate.Code {
	switch err {
	case wal.ErrSlotExists:
		return sqlstate.DuplicateObject
	case wal.ErrSlotNotFound:
		return sqlstate.UndefinedObject
	case wal.ErrSlotInUse:
		return sqlstate.ObjectInUse
	case wal.ErrSlotInvalidName:
		return sqlstate.InvalidParameterValue
	}
	return sqlstate.InternalError
}
