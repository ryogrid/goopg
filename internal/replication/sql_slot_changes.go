package replication

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/storage"
)

// SQLSlotChanges is pg_logical_slot_get_binary_changes /
// pg_logical_slot_peek_binary_changes (logicalfuncs.c
// pg_logical_slot_get_changes_guts) for a pgoutput slot: it decodes the
// slot's WAL from its restart point to the end of WAL as of the call,
// through the pgoutput plugin with the given options (publication_names,
// messages; proto_version is accepted), and returns one row per message
// written — its LSN, its transaction's xid (0 for a non-transactional
// logical message) and its bytes. get (peek = false) then confirms the
// slot up to the end of what it read; peek leaves it. physDBOid is the
// connection's database (logical messages from another database are
// skipped), nsDBOid its catalog namespace.
func (s *Handler) SQLSlotChanges(slotName string, peek bool, options []string, physDBOid, nsDBOid uint32) ([]executor.LogicalSlotChange, error) {
	if s.cfg.WAL == nil || s.cfg.Slots == nil || s.cfg.WALDirPath == "" {
		return nil, &executor.ExecError{Code: "0A000", Message: "logical decoding requires a configured WAL writer and slot registry"}
	}
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		return nil, &executor.ExecError{Code: "0A000", Message: "logical decoding requires *catalog.InMemory backing"}
	}
	slot, err := s.cfg.Slots.Get(slotName)
	if err != nil {
		return nil, &executor.ExecError{Code: "42704", Message: fmt.Sprintf("replication slot \"%s\" does not exist", slotName)}
	}
	if slot.Kind != xlog.SlotLogical {
		return nil, &executor.ExecError{Code: "22023", Message: fmt.Sprintf("cannot use physical replication slot for logical decoding")}
	}
	if slot.Plugin != "" && slot.Plugin != "pgoutput" {
		return nil, &executor.ExecError{Code: "0A000", Message: fmt.Sprintf("logical decoding output plugin \"%s\" is not supported", slot.Plugin)}
	}
	if slot.Active {
		return nil, &executor.ExecError{Code: "55006", Message: fmt.Sprintf("replication slot \"%s\" is active for PID 0", slotName)}
	}
	if len(options)%2 != 0 {
		return nil, &executor.ExecError{Code: "22023", Message: "array must have even number of elements"}
	}
	opts := map[string]string{}
	for i := 0; i+1 < len(options); i += 2 {
		opts[strings.ToLower(options[i])] = options[i+1]
	}

	dbOidVar := []uint32{nsDBOid}
	snap := xlog.BuildCatalogSnapshot(im, executor.RegOutRendererVisible(im,
		func(schema string) bool { return schema == "" || schema == "pg_catalog" || schema == "public" },
		dbOidVar...), dbOidVar...)
	col := &sqlChangeCollector{}
	pg := xlog.NewPgOutput(snap, col)
	if pubNames := splitPublicationNames(opts["publication_names"]); len(pubNames) > 0 && s.cfg.PubSub != nil {
		pg.SetFilter(newLivePublicationFilter(s.cfg.PubSub, pubNames, dbOidVar...))
	}
	if v, ok := opts["messages"]; ok {
		on, perr := parsePgoutputBool(v)
		if perr != nil {
			return nil, &executor.ExecError{Code: "42601", Message: perr.Error()}
		}
		pg.SetMessages(on)
	}
	plugin := &sqlChangePlugin{pg: pg, col: col}
	dec := xlog.NewDecoder(plugin)
	dec.SetDatabase(physDBOid)

	segSize := s.cfg.WALSegmentSize
	if segSize <= 0 {
		segSize = xlog.WALSegmentSize()
	}
	// The slot's restart point is a 0-based end-of-record LSN; the
	// iterator takes the next record's 1-based start (NewSlotDecoder).
	start := slot.RestartLSN
	if start > 0 {
		start++
	}
	it, err := xlog.NewRecordIterator(s.cfg.WAL, s.cfg.WALDirPath, segSize, start)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	// Decode up to the end of WAL as of the call (GetFlushRecPtr in
	// pg_logical_slot_get_changes_guts).
	end := s.cfg.WAL.WrittenLSN()
	it.SetReadLimit(func() uint64 { return end })
	var lastEnd uint64
	for it.ConsumedThrough() < end {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		rec, nerr := it.Next(ctx)
		cancel()
		if nerr != nil {
			if errors.Is(nerr, context.DeadlineExceeded) && it.ConsumedThrough() >= end {
				break
			}
			if errors.Is(nerr, context.DeadlineExceeded) {
				continue
			}
			return nil, nerr
		}
		col.recordEnd = rec.EndLSN
		if err := xlog.Classify(dec, rec); err != nil {
			return nil, err
		}
		lastEnd = rec.EndLSN
	}
	col.flushPendingBegin(false)
	if !peek && lastEnd > 0 {
		if err := s.cfg.Slots.AdvanceConfirmedFlushLSN(slotName, lastEnd); err != nil {
			return nil, err
		}
	}
	return col.rows, nil
}

// sqlChangePlugin forwards to pgoutput and tells the collector the LSN
// and xid of the message about to be written (OutputPluginPrepareWrite's
// write_location and txn->xid).
type sqlChangePlugin struct {
	pg  *xlog.PgOutput
	col *sqlChangeCollector
}

func (p *sqlChangePlugin) Begin(xid storage.TransactionID, commitLSN uint64) error {
	p.col.xid, p.col.lsn = uint32(xid), commitLSN
	return p.pg.Begin(xid, commitLSN)
}

func (p *sqlChangePlugin) Change(c xlog.Change) error {
	p.col.lsn = c.LSN
	return p.pg.Change(c)
}

func (p *sqlChangePlugin) Commit(xid storage.TransactionID, commitLSN uint64) error {
	p.col.lsn = commitLSN
	return p.pg.Commit(xid, commitLSN)
}

func (p *sqlChangePlugin) LogicalMessage(xid storage.TransactionID, lsn uint64, transactional bool, prefix string, content []byte) error {
	saved := p.col.xid
	p.col.lsn = lsn
	if !transactional {
		p.col.xid = 0
	}
	err := p.pg.LogicalMessage(xid, lsn, transactional, prefix, content)
	p.col.xid = saved
	return err
}

// sqlChangeCollector turns each pgoutput Write into a result row. It also
// applies pgoutput's empty-transaction rule (pgoutput_send_begin is lazy,
// and pgoutput_commit_txn skips a transaction that sent nothing): a
// BEGIN is held until the transaction writes something else, and a
// BEGIN followed directly by COMMIT is dropped with it.
type sqlChangeCollector struct {
	rows      []executor.LogicalSlotChange
	lsn       uint64
	xid       uint32
	recordEnd uint64
	pending   *executor.LogicalSlotChange
}

func (c *sqlChangeCollector) Write(b []byte) (int, error) {
	row := executor.LogicalSlotChange{LSN: c.lsn, XID: c.xid, Data: append([]byte(nil), b...)}
	if len(b) > 0 {
		switch b[0] {
		case 'B':
			c.pending = &row
			return len(b), nil
		case 'C':
			if c.pending != nil {
				c.pending = nil
				return len(b), nil
			}
		default:
			c.flushPendingBegin(true)
		}
	}
	c.rows = append(c.rows, row)
	return len(b), nil
}

// flushPendingBegin emits a held BEGIN (emit) or drops it.
func (c *sqlChangeCollector) flushPendingBegin(emit bool) {
	if c.pending != nil && emit {
		c.rows = append(c.rows, *c.pending)
	}
	c.pending = nil
}
