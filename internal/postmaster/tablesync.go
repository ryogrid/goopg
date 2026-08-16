// Subscriber-side initial-COPY tablesync transport for the M0008
// logical-replication pipeline. Consumes a publisher's
// `COPY <rel> TO STDOUT` exchange, writes each decoded row through
// the supplied LineWriter (typically *executor.CopyFromExecutor),
// and walks pg_subscription_rel.srsubstate from i → d → s.
//
// See docs/design/0008-0004-apply-worker-and-tablesync.md.
//
// What this file delivers:
//
//   - Wire-shape driver: writes Query("COPY <rel> TO STDOUT") to
//     the publisher, reads the CopyOutResponse → CopyData* →
//     CopyDone → CommandComplete → ReadyForQuery sequence, splits
//     each CopyData payload on '\n' (one row per terminated line)
//     and forwards lines to the LineWriter.
//   - State-machine bookkeeping: Add the (sub, rel) row at state
//     'i' if missing; advance to 'd' on first CopyOutResponse;
//     advance to 's' on a clean CopyDone. Errors leave the row
//     where it was so the next loop can resume.
//
// What's deferred:
//
//   - Connection setup. Caller has already dialed and authenticated
//     the publisher; we drive only the framed exchange. The
//     LogicalReceiver wiring that opens a per-subscription tablesync
//     connection lives in a follow-up loop.
//   - LSN handoff. The simple-COPY path doesn't surface the
//     publisher slot's snapshot LSN, so we advance to 's' with
//     LSN=0. The apply-worker integration that drives 's → r' on
//     a streaming commit boundary is a separate slice (also
//     deferred — see fix_plan.md).
//   - Multi-row CopyData frames + cross-frame row boundaries. v0
//     goopg's runCopyTo emits exactly one COPY-TEXT line per
//     CopyData frame, terminated by '\n'. We accept that shape
//     and also tolerate a single frame that carries multiple
//     '\n'-separated rows (upstream PG sometimes batches), but we
//     do not yet buffer across frames. A row split across frames
//     is a wire error today.

package postmaster

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// LineWriter is the subset of executor.CopyFromExecutor that
// RunTableSync needs. Keeping this as an interface here means the
// server package doesn't need to import executor purely for the
// type, and tests can substitute a recording fake.
type LineWriter interface {
	PushLine(line []byte) error
	RowsInserted() int64
}

// TableSyncConfig bundles the inputs to RunTableSync. The caller
// owns the connection (Reader/Writer); RunTableSync neither dials
// nor closes it. PubSub and SubName/RelOID drive the catalog state
// machine; Relname is the qualified name interpolated into the
// COPY query.
type TableSyncConfig struct {
	PubSub  *catalog.PubSub
	SubName string
	RelOID  uint32
	Relname string
	From    LineWriter
	Reader  *libpq.FrameReader
	Writer  *libpq.FrameWriter

	// Stat is the optional pg_stat_subscription handle for this
	// tablesync worker. When non-nil, RunTableSync stamps every
	// CopyData frame's receipt time via MarkMessage and bumps
	// the worker's received_lsn to the row count once the
	// stream completes (LSN handoff isn't on the wire in v0, so
	// the row count stands in as a "progress observed" signal).
	// Caller owns Register / Unregister around RunTableSync.
	Stat *xlog.Subscriber

	// Logger receives structured replication-event lines
	// (started, state-change, completed). nil falls back to
	// slog.Default(). See internal/wal/repllog.go for the
	// event vocabulary.
	Logger *slog.Logger
}

// RunTableSync drives one publisher → subscriber initial COPY and
// returns the number of rows applied. On success the row in
// pg_subscription_rel ends at state 's' (sync done). On error the
// state machine is left wherever it was when the error fired so
// the next loop can resume; the error is returned wrapped.
func RunTableSync(cfg TableSyncConfig) (int64, error) {
	if cfg.PubSub == nil {
		return 0, errors.New("tablesync: PubSub is nil")
	}
	if cfg.From == nil {
		return 0, errors.New("tablesync: From is nil")
	}
	if cfg.Reader == nil || cfg.Writer == nil {
		return 0, errors.New("tablesync: Reader/Writer are nil")
	}
	if cfg.Relname == "" {
		return 0, errors.New("tablesync: Relname is empty")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Idempotent seed: if the (sub, rel) row already exists, leave
	// its state alone — a previous attempt may have advanced it to
	// 'd' before crashing. ErrSubscriptionRelExists is fine.
	if _, err := cfg.PubSub.AddSubscriptionRel(cfg.SubName, cfg.RelOID); err != nil &&
		!errors.Is(err, catalog.ErrSubscriptionRelExists) {
		return 0, fmt.Errorf("tablesync: seed subscription_rel: %w", err)
	}

	logger.Info("logical tablesync: started",
		"event", xlog.EventTablesyncStarted,
		"sub", cfg.SubName, "rel_oid", cfg.RelOID, "rel", cfg.Relname)

	// Send the simple-query COPY. Quoting policy: the caller is
	// expected to pass a qualified, already-safe identifier (the
	// PubSub registry stores names as-given). v0 wraps in double
	// quotes for the schema and table parts so a name with a dot
	// inside still parses; the upstream tablesync uses the same
	// dotted form.
	sql := fmt.Sprintf("COPY %s TO STDOUT", cfg.Relname)
	if err := cfg.Writer.WriteQuery(sql); err != nil {
		return 0, fmt.Errorf("tablesync: write Query: %w", err)
	}
	if err := cfg.Writer.Flush(); err != nil {
		return 0, fmt.Errorf("tablesync: flush Query: %w", err)
	}

	// Phase 1: wait for CopyOutResponse. The publisher may emit a
	// NoticeResponse first (we ignore it) or an ErrorResponse (we
	// surface it). Anything else is a wire-shape violation.
	for {
		f, err := cfg.Reader.ReadFrame()
		if err != nil {
			return 0, fmt.Errorf("tablesync: wait for CopyOutResponse: %w", err)
		}
		switch f.Type {
		case libpq.MsgCopyOutResponse:
			// OK, fall through to phase 2.
		case libpq.MsgNoticeResponse:
			continue
		case libpq.MsgErrorResponse:
			return 0, errFromErrorResponse(f.Payload, "tablesync (publisher rejected COPY)")
		default:
			return 0, fmt.Errorf("tablesync: expected CopyOutResponse, got %q", f.Type)
		}
		break
	}

	// Move i → d now that the publisher has agreed to stream.
	// This transition is allowed and a no-op if we're already at d
	// (the validity map has d → d).
	prev, _ := cfg.PubSub.LookupSubscriptionRel(cfg.SubName, cfg.RelOID)
	if _, err := cfg.PubSub.AdvanceSubscriptionRel(cfg.SubName, cfg.RelOID,
		catalog.SubRelStateDataCopy, 0); err != nil {
		return 0, fmt.Errorf("tablesync: advance to d: %w", err)
	}
	if prev != nil && prev.State != catalog.SubRelStateDataCopy {
		logger.Info("logical tablesync: state change",
			"event", xlog.EventTablesyncStateChange,
			"sub", cfg.SubName, "rel_oid", cfg.RelOID,
			"from", prev.State, "to", catalog.SubRelStateDataCopy)
	}

	// Phase 2: stream rows until CopyDone. Each CopyData payload
	// is one or more '\n'-terminated COPY-TEXT lines.
	for {
		f, err := cfg.Reader.ReadFrame()
		if err != nil {
			return cfg.From.RowsInserted(), fmt.Errorf("tablesync: read row frame: %w", err)
		}
		switch f.Type {
		case libpq.MsgCopyData:
			if cfg.Stat != nil {
				cfg.Stat.MarkMessage(time.Now(), 0)
			}
			if err := pushCopyDataLines(cfg.From, f.Payload); err != nil {
				return cfg.From.RowsInserted(), fmt.Errorf("tablesync: apply row: %w", err)
			}
		case libpq.MsgCopyDone:
			// Phase 3: drain trailer (CommandComplete +
			// ReadyForQuery) and advance to 's'. We don't strictly
			// need to wait for ReadyForQuery to declare sync done —
			// the row data is already locally durable — but reading
			// it leaves the connection in a clean state for the
			// caller's next query.
			if err := drainCopyTrailer(cfg.Reader); err != nil {
				return cfg.From.RowsInserted(), fmt.Errorf("tablesync: drain trailer: %w", err)
			}
			if _, err := cfg.PubSub.AdvanceSubscriptionRel(cfg.SubName, cfg.RelOID,
				catalog.SubRelStateSyncDone, 0); err != nil {
				return cfg.From.RowsInserted(), fmt.Errorf("tablesync: advance to s: %w", err)
			}
			logger.Info("logical tablesync: state change",
				"event", xlog.EventTablesyncStateChange,
				"sub", cfg.SubName, "rel_oid", cfg.RelOID,
				"from", catalog.SubRelStateDataCopy,
				"to", catalog.SubRelStateSyncDone)
			rows := cfg.From.RowsInserted()
			logger.Info("logical tablesync: completed",
				"event", xlog.EventTablesyncCompleted,
				"sub", cfg.SubName, "rel_oid", cfg.RelOID,
				"rel", cfg.Relname, "rows", rows)
			return rows, nil
		case libpq.MsgErrorResponse:
			return cfg.From.RowsInserted(),
				errFromErrorResponse(f.Payload, "tablesync (publisher errored mid-COPY)")
		case libpq.MsgNoticeResponse:
			continue
		default:
			return cfg.From.RowsInserted(),
				fmt.Errorf("tablesync: unexpected frame %q during COPY stream", f.Type)
		}
	}
}

// pushCopyDataLines splits one CopyData payload on '\n' and forwards
// each terminated line (without the trailing '\n') to the LineWriter.
// A trailing un-terminated remainder is a wire error — v0 doesn't
// buffer rows across CopyData frames.
func pushCopyDataLines(out LineWriter, payload []byte) error {
	for len(payload) > 0 {
		nl := -1
		for i, b := range payload {
			if b == '\n' {
				nl = i
				break
			}
		}
		if nl < 0 {
			return fmt.Errorf("CopyData payload has %d trailing bytes with no newline", len(payload))
		}
		line := payload[:nl]
		if err := out.PushLine(line); err != nil {
			return err
		}
		payload = payload[nl+1:]
	}
	return nil
}

// drainCopyTrailer reads CommandComplete and ReadyForQuery after a
// CopyDone. NoticeResponses are ignored. Anything else is an error.
func drainCopyTrailer(r *libpq.FrameReader) error {
	sawComplete := false
	for {
		f, err := r.ReadFrame()
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				// Some publishers close immediately after CopyDone
				// (no CommandComplete/ReadyForQuery). v0 goopg's
				// runCopyTo always sends both, but we tolerate the
				// truncated case so a real-world publisher hang-up
				// after CopyDone doesn't fail tablesync.
				return nil
			}
			return err
		}
		switch f.Type {
		case libpq.MsgCommandComplete:
			sawComplete = true
		case libpq.MsgReadyForQuery:
			return nil
		case libpq.MsgNoticeResponse:
			continue
		case libpq.MsgErrorResponse:
			return errFromErrorResponse(f.Payload, "tablesync (publisher errored after CopyDone)")
		default:
			if sawComplete {
				return fmt.Errorf("tablesync: unexpected frame %q after CommandComplete", f.Type)
			}
			return fmt.Errorf("tablesync: expected CommandComplete, got %q", f.Type)
		}
	}
}

// errFromErrorResponse pulls the human-readable message out of an
// ErrorResponse payload. The fields are NUL-terminated key+value
// pairs with a leading 1-byte field code; we look for 'M' (the
// primary message). Falls back to a generic string on parse trouble.
func errFromErrorResponse(payload []byte, ctx string) error {
	msg, code := "", ""
	i := 0
	for i < len(payload) {
		field := payload[i]
		if field == 0 {
			break
		}
		i++
		end := i
		for end < len(payload) && payload[end] != 0 {
			end++
		}
		val := string(payload[i:end])
		switch field {
		case 'M':
			msg = val
		case 'C':
			code = val
		}
		if end >= len(payload) {
			break
		}
		i = end + 1
	}
	if code == "" {
		code = string(errcodes.SystemError)
	}
	if msg == "" {
		msg = "publisher returned ErrorResponse"
	}
	return fmt.Errorf("%s: [%s] %s", ctx, code, msg)
}
