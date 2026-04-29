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
	"fmt"
	"strings"

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
func (s *Server) handleReplicationCommand(w *protocol.FrameWriter, payload []byte) (bool, error) {
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
	}
	return false, nil
}

// replyIdentifySystem emits the four-column / one-row reply upstream's
// `walsender.c IdentifySystem()` produces. Column names and types
// follow upstream so a libpq client (and the future goopg-side
// walreceiver) parse it transparently:
//
//   systemid  : text   — pg_control identifier
//   timeline  : int4   — current timeline; v0 is single-timeline
//   xlogpos   : text   — current write LSN as `X/X` hex pair
//   dbname    : text   — empty for physical replication
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

// replyCreateReplicationSlot parses `name PHYSICAL [...]` (the v0
// subset) and creates a slot. Other variants (LOGICAL, EXPORT_SNAPSHOT,
// RESERVE_WAL options) return feature_not_supported.
func (s *Server) replyCreateReplicationSlot(w *protocol.FrameWriter, args string) error {
	if s.cfg.Slots == nil {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			"replication slots are not configured on this server")
	}
	tokens := strings.Fields(args)
	if len(tokens) < 2 {
		return s.writeQueryError(w, sqlstate.SyntaxError,
			"CREATE_REPLICATION_SLOT requires a slot name and a kind")
	}
	name := unquoteIdent(tokens[0])
	kind := strings.ToUpper(tokens[1])
	if kind != "PHYSICAL" {
		return s.writeQueryError(w, sqlstate.FeatureNotSupported,
			fmt.Sprintf("CREATE_REPLICATION_SLOT %s is not supported in v0 (PHYSICAL only)", kind))
	}
	var startLSN uint64
	if s.cfg.WAL != nil {
		startLSN = s.cfg.WAL.WrittenLSN()
	}
	slot, err := s.cfg.Slots.Create(name, wal.SlotPhysical, startLSN)
	if err != nil {
		return s.writeQueryError(w, replicationSlotErrCode(err), err.Error())
	}
	// Upstream replies with (slot_name, consistent_point, snapshot_name,
	// output_plugin). For PHYSICAL, snapshot_name and output_plugin
	// are NULL; consistent_point is the LSN at which the slot was
	// reserved.
	if err := w.WriteRowDescription([]protocol.FieldDescription{
		{Name: "slot_name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "consistent_point", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "snapshot_name", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
		{Name: "output_plugin", TypeOID: oidText, TypeSize: -1, TypeModifier: -1, Format: 0},
	}); err != nil {
		return err
	}
	row := [][]byte{
		[]byte(slot.Name),
		[]byte(formatLSN(slot.RestartLSN)),
		nil,
		nil,
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
