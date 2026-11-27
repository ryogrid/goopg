// Publisher-side LOGICAL walsender: drives the M0008 logical-decoding
// pipeline (`wal.SlotDecoder` + `wal.PgOutput`) on top of the existing
// CopyBoth streaming session and ships each emitted pgoutput message
// to the standby in a `'w'` CopyData frame.
//
// The standby (`logicalreceiver.go`, this package) consumes the
// stream and applies each event through `*executor.ApplyWorker`.
// See docs/design/0008-0004-apply-worker-and-tablesync.md.

package replication

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// runLogicalWalsender is invoked by replyStartReplication after the
// CopyBoth handshake when the client requested `... LOGICAL`. It
// builds a SlotDecoder over the slot, plumbs its OutputPlugin into
// a PgOutput writing through `walsenderPgoutputAdapter`, and runs
// until the standby disconnects or ctx is cancelled.
func (s *Handler) runLogicalWalsender(ctx context.Context, r *libpq.FrameReader, w *libpq.FrameWriter, args startReplicationArgs, appName, dbName string, pid int32) error {
	if s.cfg.WAL == nil {
		return s.writeStreamingError(w, errcodes.FeatureNotSupported,
			"START_REPLICATION LOGICAL requires a configured WAL writer")
	}
	if s.cfg.Catalog == nil {
		return s.writeStreamingError(w, errcodes.FeatureNotSupported,
			"START_REPLICATION LOGICAL requires a configured Catalog")
	}
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		return s.writeStreamingError(w, errcodes.FeatureNotSupported,
			"START_REPLICATION LOGICAL requires *catalog.InMemory backing")
	}
	// M0119-0006 (deferral row 1354 claim 2): every catalog lookup on this
	// path — the reg* renderer, the slot-creation snapshot scope, and the
	// publication filter — must resolve against the connection's database.
	// walsenderCatalogDBOidVar translates the physical oid through
	// catalog.NamespaceDBOid; empty/unknown dbName resolves to DefaultDBOid
	// via NamespaceDBOid(0)→1 (the downstream resolveDBOid empty-slice
	// fallback at catalog.go:3694-3699 is no longer what carries it).
	dbOidVar := walsenderCatalogDBOidVar(im, dbName)
	walDir := s.cfg.WALDirPath
	if walDir == "" {
		return s.writeStreamingError(w, errcodes.FeatureNotSupported,
			"START_REPLICATION LOGICAL requires Config.WALDirPath to be set")
	}
	segSize := s.cfg.WALSegmentSize
	if segSize <= 0 {
		segSize = xlog.WALSegmentSize()
	}

	// M0103-0005: forget the subscriber's last reported progress when the
	// connection drops so a disconnected subscriber no longer counts toward
	// the FIRST/ANY quorum. Mirrors the physical-walsender path in
	// replyStartReplication. Empty appName is harmless (ForgetStandby
	// removes the "" key, which never matches any rule entry).
	if s.cfg.SyncRep != nil && appName != "" {
		defer s.cfg.SyncRep.ForgetStandby(appName)
	}

	// Snapshot the catalog at session start so the pgoutput
	// stream resolves relations against a stable shape; mirrors
	// what the apply worker expects.
	// Bind a real reg* name renderer so TEXT-mode pgoutput emits a reg*
	// column's NAME (regclassout/regprocout/..., proto.c:848), not the numeric
	// OID that belongs to BINARY mode's typsend. The publisher side never SETs
	// search_path (postgres/src/backend/replication/ has zero search_path writes
	// on the publisher side), so the session inherits the default `"$user",
	// public` — for the walsender the visible set is effectively
	// {pg_catalog, public} (pg_catalog is always searched implicitly; public is
	// in the default path). RegOutRendererVisible schema-qualifies an off-path
	// regclass/regproc/regprocedure/regcollation (other_schema.tbl) and leaves
	// public/pg_catalog bare, matching regclassout's RelationIsVisible arm
	// (regproc.c:973-981). The $user-schema-visible edge is a documented
	// approximation (the walsender binds no session user). M0119-0006 (84th
	// slice, deferral row 1354 claim 1).
	snap := xlog.BuildCatalogSnapshot(im, executor.RegOutRendererVisible(im,
		func(schema string) bool { return schema == "" || schema == "pg_catalog" || schema == "public" },
		dbOidVar...), dbOidVar...)

	// Adapter wraps each pgoutput message in a `'w'` CopyData
	// frame so it lands on the wire in the shape the subscriber's
	// LogicalReceiver consumes.
	// sentPtr starts at the slot's confirmed_flush_lsn
	// (StartLogicalReplication: "sentPtr = MyReplicationSlot->data.confirmed_flush"),
	// so a keepalive on a quiet publisher already reports where the
	// subscriber has confirmed up to. M0149-0040b.
	startSent := args.StartLSN
	if s.cfg.Slots != nil {
		if sl, err := s.cfg.Slots.Get(args.SlotName); err == nil && sl.ConfirmedFlushLSN > startSent {
			startSent = sl.ConfirmedFlushLSN
		}
	}
	adapter := &walsenderPgoutputAdapter{
		w:       w,
		sentPtr: startSent,
	}
	plugin := xlog.NewPgOutput(snap, adapter)

	// Apply the publication-membership filter from the slot's
	// requested publication_names option (parsed by
	// parseStartReplicationArgs). Empty list / missing PubSub
	// registry leaves the filter nil — emit every change in the
	// snapshot, matching the v0 pre-filter behaviour. See
	// docs/design/0008-0003-publication-subscription-ddl.md.
	if pubNames := splitPublicationNames(args.Options["publication_names"]); len(pubNames) > 0 && s.cfg.PubSub != nil {
		plugin.SetFilter(newLivePublicationFilter(s.cfg.PubSub, pubNames, dbOidVar...).withCatalog(im))
	}

	// pgoutput's "messages" option (parse_output_parameters,
	// defGetBoolean): logical decoding messages reach the subscriber only
	// when it asked for them.
	if v, ok := args.Options["messages"]; ok {
		on, perr := parsePgoutputBool(v)
		if perr != nil {
			return s.writeStreamingError(w, errcodes.SyntaxError, perr.Error())
		}
		plugin.SetMessages(on)
	}

	dec, err := xlog.NewSlotDecoderWithSnapshot(s.cfg.Slots, args.SlotName, s.cfg.WAL, walDir, segSize, plugin, xlog.SlotSnapshot{Catalog: snap})
	if err != nil {
		return s.writeStreamingError(w, errcodes.InternalError, err.Error())
	}
	defer dec.Close()
	// Logical decoding's positions (M0149-0040b): a pgoutput message
	// goes out at the LSN of the record being decoded — goopg decodes a
	// transaction at its commit record, so its Begin, changes and Commit
	// all carry that record's end (txn->end_lsn for the Commit) — and
	// sentPtr follows the end of each record read, which keepalives
	// report (WalSndKeepalive).
	dec.SetRecordHooks(func(rec xlog.Record) { adapter.setWriteLocation(rec.EndLSN) },
		func(rec xlog.Record) { adapter.advanceSent(rec.EndLSN) })

	// The walsender's pg_stat_replication row (WalSndCtl->walsnds):
	// sent_lsn follows sentPtr, write/flush/replay the subscriber's 'r'
	// replies (ProcessStandbyReplyMessage). M0149-0040b.
	var senderHandle *xlog.Sender
	if s.cfg.WalSenders != nil {
		// pg_stat_replication.pid is the walsender's backend pid
		// (pg_stat_get_wal_senders: walsnd->pid); 024 waits for it to
		// change after an apply-worker restart. M0149-0052a.
		senderHandle = s.cfg.WalSenders.Register(xlog.SenderState{
			PID:             uint32(pid),
			SlotName:        args.SlotName,
			ClientAddr:      walsenderClientAddr(r),
			SentLSN:         startSent,
			ApplicationName: appName,
		})
		defer s.cfg.WalSenders.Unregister(senderHandle)
		adapter.sender = senderHandle
	}
	// logicalmsg_decode skips a message emitted from another database.
	dec.SetDatabase(resolveConnDBOid(im, dbName))
	// start_decoding_at = max(client start point, confirmed_flush_lsn):
	// what the subscriber already has is not sent again, and the slot moves
	// only on the subscriber's feedback (handleStandbyCopyData), so what it
	// has not confirmed is sent again after a reconnect. M0149-0057c.
	dec.SetFeedbackOnly(true)
	dec.SetSkipThrough(startSent)
	dec.SetWriteLocationHook(adapter.setWriteLocation)

	// Receive-side goroutine: parse standby-status frames so the
	// slot's confirmed_flush_lsn advances. The decoder's commit
	// path also advances it on each commit; the standby-status
	// path mirrors what walreceiver/PHYSICAL slots do so the
	// subscriber ack drives retention.
	//
	// M0103-0005: handleStandbyCopyData also dispatches the parsed
	// progress into SyncRep when configured; the appName carried
	// through from the START_REPLICATION handshake keys each
	// per-subscriber row in the wait registry, so a publisher
	// COMMIT blocking on synchronous_standby_names = '<appName>'
	// releases as soon as the subscriber reports apply ≥ commit LSN.
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	receiveDone := make(chan struct{})
	// clientCopyDone: the subscriber ended the stream itself (CopyDone) —
	// a table-sync worker that caught up, an apply worker restarting. The
	// walsender then answers with its own CopyDone and CommandComplete
	// "COPY 0" (ProcessRepliesIfAny / StartLogicalReplication), and the
	// connection returns to command mode; walrcv_endstreaming waits for
	// both. M0149-0042a.
	var clientCopyDone atomic.Bool
	go func() {
		defer close(receiveDone)
		for {
			f, err := r.ReadFrame()
			if err != nil {
				return
			}
			switch f.Type {
			case libpq.MsgCopyData:
				_ = s.handleStandbyCopyDataCapped(args.SlotName, f.Payload, senderHandle, s.cfg.SyncRep, appName, dec.RestartCapFor)
				adapter.noteReply(f.Payload)
			case libpq.MsgCopyDone:
				clientCopyDone.Store(true)
				adapter.stopSending()
				streamCancel()
				return
			case libpq.MsgTerminate:
				streamCancel()
				return
			}
		}
	}()

	runErr := make(chan error, 1)
	runErrDone := make(chan struct{})
	go func() {
		err := dec.Run(streamCtx)
		close(runErrDone)
		runErr <- err
	}()

	// Periodic keepalive emission. Without it, PG's apply worker
	// terminates the connection after `wal_receiver_timeout` (default
	// 60 s) when no pgoutput frame has crossed the wire — which is the
	// common case during a quiet workload. The physical walsender path
	// in replyStartReplication runs the same 10 s cadence; the LOGICAL
	// path needs symmetric behaviour. The keepalive's walEnd advertises
	// the adapter's last-emitted synthetic LSN so the subscriber's
	// progress reporting matches the byte-stream LSNs it has seen.
	//
	// WalSndWaitForWal also sends a keepalive as soon as the subscriber's
	// reported write and flush positions are behind sentPtr (and no ping
	// is outstanding), so an idle subscriber learns the WAL end and
	// confirms it without waiting for the interval. keepaliveCheck is
	// that check's cadence. M0149-0040b.
	const keepaliveInterval = 10 * time.Second
	const keepaliveCheck = 200 * time.Millisecond
	keepaliveDone := make(chan struct{})
	go func() {
		defer close(keepaliveDone)
		ticker := time.NewTicker(keepaliveCheck)
		defer ticker.Stop()
		last := time.Now()
		for {
			select {
			case <-streamCtx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				if now.Sub(last) < keepaliveInterval && !adapter.subscriberBehind() {
					continue
				}
				if err := adapter.WriteKeepalive(now.UTC()); err != nil {
					streamCancel()
					return
				}
				last = now
			}
		}
	}()

	// endStreaming answers the subscriber's CopyDone once the decoder and
	// keepalive goroutines have stopped writing.
	endStreaming := func() error {
		<-runErrDone
		<-keepaliveDone
		return adapter.endStreaming()
	}

	select {
	case <-streamCtx.Done():
		// Session-level teardown (apply-worker stop during ALTER
		// SUBSCRIPTION, connection ctx cancel). The reader is
		// parked in ReadFrame on a LIVE socket — the subscriber
		// side stays up through a worker restart — so awaiting
		// receiveDone here deadlocked the session forever: the
		// slot's ACTIVE mark persisted (deferred SetActive(false)
		// never ran), the relaunched worker lost its relaunch to
		// 55006, and the server's clean STOP hung behind the
		// session. The physical replyStartReplication loop never
		// joins the reader on this arm; the reader goroutine
		// exits on its own when the client finally disconnects,
		// and the buffered channels keep the decoder/keepalive
		// goroutines leak-free.
		slog.Debug("logical walsender: stream ctx done",
			"slot", args.SlotName, "ctxErr", streamCtx.Err())
		streamCancel()
		if clientCopyDone.Load() {
			return endStreaming()
		}
		return nil
	case <-receiveDone:
		// The client vanished (FIN/RST surfaced as a ReadFrame
		// error, or a clean CopyDone/Terminate). The reader
		// goroutine exits on any of those and closes receiveDone,
		// but on a plain disconnect nothing else wakes this
		// select: the decoder idles silently on a quiet publisher
		// and the 10 s keepalive's write vanishes into the socket
		// buffer. Without this arm the session lived for minutes
		// (observed forever), keeping the slot's ACTIVE mark —
		// and an eager apply-worker restart (ALTER SUBSCRIPTION
		// ... refresh) then lost its relaunch to
		// 55006 "replication slot is active" until the 10 s
		// launcher poll. The physical replyStartReplication
		// select already carries the symmetric arm.
		streamCancel()
		if clientCopyDone.Load() {
			return endStreaming()
		}
		<-runErr
		<-keepaliveDone
		return nil
	case err := <-runErr:
		var cle *ColumnListConflictError
		if errors.As(err, &cle) {
			// pgoutput's ERROR: logged as the walsender's ERROR and sent
			// to the subscriber as 0A000. M0149-0060.
			slog.Error(cle.Error(), "slot", args.SlotName)
			streamCancel()
			<-keepaliveDone
			return s.writeStreamingError(w, errcodes.FeatureNotSupported, cle.Error())
		}
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, xlog.ErrClosed) {
			slog.Info("logical walsender: decoder run failed",
				"slot", args.SlotName, "err", err)
		}
		streamCancel()
		<-keepaliveDone
		if errors.Is(err, context.Canceled) || errors.Is(err, xlog.ErrClosed) {
			if clientCopyDone.Load() {
				return adapter.endStreaming()
			}
			return nil
		}
		// A real decode failure must reach the client (PG sends an
		// ErrorResponse and drops the stream). Await NOTHING on the
		// reader side first: the subscriber keeps the socket open
		// through worker restarts, so the reader parks in ReadFrame
		// forever and joining receiveDone here deadlocked the
		// session (slot ACTIVE + hung STOP, see the ctx-done arm).
		if err != nil {
			return s.writeStreamingError(w, errcodes.InternalError, err.Error())
		}
		return nil
	}
}

// walsenderPgoutputAdapter implements io.Writer by wrapping each
// Write call in a `'w'` CopyData frame and shipping it through the
// FrameWriter. Each PgOutput.Begin/Commit/Change call performs
// exactly one Write so each Write maps to one pgoutput message
// — that's what makes the wire framing work.
//
// Each frame carries real WAL positions, as PG's do: the LSN of the
// record being decoded, and keepalives the end of the last record read.
// A subscriber's replies therefore name WAL positions the publisher can
// compare with pg_current_wal_lsn() (wait_for_catchup), and they advance
// the slot's confirmed_flush_lsn to real record boundaries. Before
// M0149-0040b the frames were numbered by a byte counter started at
// START_REPLICATION's position.
type walsenderPgoutputAdapter struct {
	w  *libpq.FrameWriter
	mu sync.Mutex
	// writeLoc is logical decoding's ctx->write_location: the LSN the
	// next message goes out at (WalSndPrepareWrite sends it as both
	// dataStart and walEnd). sentPtr is the end of the last WAL record
	// read, which keepalives report. M0149-0040b.
	writeLoc uint64
	sentPtr  uint64
	// replyWrite / replyFlush are the subscriber's last 'r' positions;
	// pingPending is WalSndKeepalive's waiting_for_ping_response.
	replyWrite, replyFlush uint64
	pingPending            bool
	sender                 *xlog.Sender
	// doneSending is WalSndCtl's streamingDoneSending: once the
	// subscriber's CopyDone is read, no further 'w' or 'k' frame goes
	// out, so the walsender's CopyDone directly follows the stream the
	// subscriber saw. M0149-0042a.
	doneSending bool
}

// stopSending marks the stream finished: later Write and WriteKeepalive
// calls send nothing.
func (a *walsenderPgoutputAdapter) stopSending() {
	a.mu.Lock()
	a.doneSending = true
	a.mu.Unlock()
}

// setWriteLocation sets the LSN the next pgoutput messages carry.
func (a *walsenderPgoutputAdapter) setWriteLocation(lsn uint64) {
	a.mu.Lock()
	a.writeLoc = lsn
	a.mu.Unlock()
}

// advanceSent raises sentPtr to lsn (the end of a record just read).
func (a *walsenderPgoutputAdapter) advanceSent(lsn uint64) {
	a.mu.Lock()
	if lsn > a.sentPtr {
		a.sentPtr = lsn
	}
	sent := a.sentPtr
	a.mu.Unlock()
	if a.sender != nil {
		a.sender.SetSentLSN(sent)
	}
}

// noteReply records a standby status update's write and flush
// positions (ProcessStandbyReplyMessage); any reply answers a ping.
func (a *walsenderPgoutputAdapter) noteReply(payload []byte) {
	parsed, kind, err := libpq.DecodeReplicationMessage(payload)
	if err != nil || kind != libpq.ReplMsgStandbyStatus {
		return
	}
	st := parsed.(*libpq.StandbyStatusUpdate)
	a.mu.Lock()
	a.replyWrite, a.replyFlush = st.WriteLSN, st.FlushLSN
	a.pingPending = false
	a.mu.Unlock()
}

// subscriberBehind is WalSndWaitForWal's keepalive condition: the
// subscriber's reported write and flush positions are both behind
// sentPtr and no keepalive is awaiting its reply.
func (a *walsenderPgoutputAdapter) subscriberBehind() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.pingPending && a.replyWrite < a.sentPtr && a.replyFlush < a.sentPtr
}

func (a *walsenderPgoutputAdapter) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(p) == 0 || a.doneSending {
		// An empty payload carries nothing; drop it. (review/260831-2 CP-4)
		// After the subscriber's CopyDone nothing more is streamed.
		return len(p), nil
	}
	frame := libpq.EncodeWALData(a.writeLoc, a.writeLoc, time.Now().UTC(), p)
	if err := a.w.WriteCopyData(frame); err != nil {
		return 0, fmt.Errorf("walsender pgoutput write: %w", err)
	}
	if err := a.w.Flush(); err != nil {
		return 0, fmt.Errorf("walsender pgoutput flush: %w", err)
	}
	return len(p), nil
}

// endStreaming is the walsender's half of ending COPY BOTH after the
// subscriber's CopyDone: CopyDone, CommandComplete "COPY 0"
// (StartLogicalReplication's EndCommand), a second CommandComplete
// "START_REPLICATION" (exec_replication_command: "dupe, but necessary per
// libpqrcv_endstreaming", whose PQendcopy consumes the first) and
// ReadyForQuery, so the connection takes commands again.
func (a *walsenderPgoutputAdapter) endStreaming() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.w.WriteCopyDone(); err != nil {
		return err
	}
	if err := a.w.WriteCommandComplete("COPY 0"); err != nil {
		return err
	}
	if err := a.w.WriteCommandComplete("START_REPLICATION"); err != nil {
		return err
	}
	return a.w.ReadyForQuery()
}

// WriteKeepalive emits a primary-keepalive (`'k'`) frame through the
// same FrameWriter the pgoutput Write path uses, sharing the adapter's
// mutex so the bytes never interleave with an in-flight `'w'` frame.
// walEnd is sentPtr, the end of the last WAL record read
// (WalSndKeepalive): an idle subscriber confirms up to it.
func (a *walsenderPgoutputAdapter) WriteKeepalive(sendTime time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.doneSending {
		return nil
	}
	a.pingPending = true
	frame := libpq.EncodeKeepalive(a.sentPtr, sendTime, false)
	if err := a.w.WriteCopyData(frame); err != nil {
		return fmt.Errorf("walsender pgoutput keepalive write: %w", err)
	}
	if err := a.w.Flush(); err != nil {
		return fmt.Errorf("walsender pgoutput keepalive flush: %w", err)
	}
	return nil
}

// guard the unused-import warning in some build configurations.
var _ = storage.MainFork

// publicationFilter implements wal.RelationFilter by checking
// each (relation, change kind) pair against the union of the
// slot's subscribed publications. A relation passes if at least
// one of its subscribed publications either covers ALL TABLES
// or names the relation explicitly *and* the corresponding
// publish-flag for the change kind is set.
//
// Mirrors upstream's per-publication membership rules from
// postgres/src/backend/replication/pgoutput/pgoutput.c
// `relation_is_publishable`.
type publicationFilter struct {
	allTablesAllowed allowFlags
	byTable          map[string]allowFlags
	// colLists is the union of the column lists the subscribed
	// publications give a table; wholeTable marks a table one of them
	// publishes without a list (as FOR ALL TABLES does every table).
	colLists   map[string][]string
	wholeTable map[string]bool
	allTables  bool
	// bySchema holds the FOR TABLES IN SCHEMA members (lower-cased schema
	// name): a relation in one of them, or a partition whose ancestor is,
	// is published whole. M0149-0053a.
	bySchema map[string]allowFlags
	// viaRootTables / viaRootSchemas are the members of the
	// publish_via_partition_root publications. M0149-0057d.
	viaRootTables  map[string]bool
	viaRootSchemas map[string]bool
	// viaRootAll: a FOR ALL TABLES publication publishes via the root.
	viaRootAll bool
	// pubs are the subscribed publications, for the per-publication row
	// filter rules (pgoutput_row_filter_init). M0149-0059c.
	pubs []*catalog.Publication
	// cat / dbOid evaluate the row filters (nil cat: filters are not
	// applied).
	cat   catalog.Catalog
	dbOid uint32
}

// RowFilter implements xlog.RowFilterFilter (pgoutput_row_filter_init):
// over the subscribed publications that publish rel (the publish-as
// relation) and a change of kind, the filters are OR'ed; one such
// publication without a filter for rel — or one that is FOR ALL TABLES or
// publishes rel's schema — means no filter.
func (f *publicationFilter) RowFilter(rel *xlog.RelationDef, kind xlog.ChangeKind) (xlog.RowFilterEval, bool) {
	if rel == nil || f.cat == nil || len(f.pubs) == 0 {
		return nil, false
	}
	q := relQualifiedName(rel)
	var quals []string
	for _, pub := range f.pubs {
		flags := allowFlags{insert: pub.PublishInsert, update: pub.PublishUpdate, delete: pub.PublishDelete}
		if !flags.allowsKind(kind) || !publicationCovers(pub, rel, q) {
			continue
		}
		qual, ok := catalog.PublicationRowFilter(pub, rel.Schema, rel.Name)
		if !ok {
			return nil, false
		}
		quals = append(quals, qual)
	}
	if len(quals) == 0 {
		return nil, false
	}
	cols := make([]executor.RowFilterColumn, len(rel.Columns))
	for i, c := range rel.Columns {
		cols[i] = executor.RowFilterColumn{Name: c.Name, Type: c.Type}
	}
	cat, dbOid, relOID := f.cat, f.dbOid, rel.OID
	return func(values []*string) (bool, error) {
		return executor.EvalPublicationRowFilter(cat, dbOid, relOID, cols, quals, values)
	}, true
}

// CheckColumnLists implements xlog.ColumnListChecker
// (pgoutput_column_list_init): every subscribed publication publishing rel
// must give it the same columns — its column list, or, without one (or as
// FOR ALL TABLES / TABLES IN SCHEMA), every published column.
func (f *publicationFilter) CheckColumnLists(rel *xlog.RelationDef) error {
	if rel == nil || len(f.pubs) < 2 {
		return nil
	}
	q := relQualifiedName(rel)
	first := true
	var want string
	for _, pub := range f.pubs {
		if !publicationCovers(pub, rel, q) {
			continue
		}
		var cols []string
		list, has := pub.Columns[q]
		if !has && rel.Schema == "" {
			list, has = pub.Columns["public."+rel.Name]
		}
		bySchema := false
		for _, sch := range pub.Schemas {
			if strings.EqualFold(sch, rel.Schema) || (rel.Schema == "" && strings.EqualFold(sch, "public")) {
				bySchema = true
			}
		}
		if has && !pub.AllTables && !bySchema {
			cols = list
		} else {
			stored := pub.GenColsType() == 's'
			for _, c := range rel.Columns {
				if c.Generated == 0 || (c.Generated == 's' && stored) {
					cols = append(cols, c.Name)
				}
			}
		}
		key := strings.Join(cols, "\x00")
		if first {
			want, first = key, false
		} else if key != want {
			schema := rel.Schema
			if schema == "" {
				schema = "public"
			}
			return &ColumnListConflictError{Schema: schema, Name: rel.Name}
		}
	}
	return nil
}

// CheckColumnLists implements xlog.ColumnListChecker.
func (f *livePublicationFilter) CheckColumnLists(rel *xlog.RelationDef) error {
	return f.load().CheckColumnLists(rel)
}

// publicationCovers reports whether pub publishes rel (qualified name q):
// FOR ALL TABLES, a FOR TABLE member — rel itself or a partition ancestor —
// or a FOR TABLES IN SCHEMA member covering rel's or an ancestor's schema.
func publicationCovers(pub *catalog.Publication, rel *xlog.RelationDef, q string) bool {
	if pub.AllTables {
		return true
	}
	for _, t := range pub.Tables {
		if t == q {
			return true
		}
		for _, anc := range rel.AncestorNames {
			if t == anc {
				return true
			}
		}
	}
	for _, sch := range pub.Schemas {
		for _, rs := range append([]string{rel.Schema}, rel.AncestorSchemas...) {
			if strings.EqualFold(sch, rs) || (rs == "" && strings.EqualFold(sch, "public")) {
				return true
			}
		}
	}
	return false
}

// PublishAsAncestor is GetTopMostAncestorInPublication over the
// publish_via_partition_root publications: the topmost ancestor of rel that
// is one of their members (by name, or by its schema).
func (f *publicationFilter) PublishAsAncestor(rel *xlog.RelationDef) (uint32, bool) {
	if rel == nil || len(rel.AncestorOIDs) == 0 {
		return 0, false
	}
	if f.viaRootAll {
		return rel.AncestorOIDs[len(rel.AncestorOIDs)-1], true
	}
	if len(f.viaRootTables) == 0 && len(f.viaRootSchemas) == 0 {
		return 0, false
	}
	for i := len(rel.AncestorOIDs) - 1; i >= 0; i-- {
		name := ""
		if i < len(rel.AncestorNames) {
			name = rel.AncestorNames[i]
		}
		sch := ""
		if i < len(rel.AncestorSchemas) {
			sch = strings.ToLower(rel.AncestorSchemas[i])
		}
		if f.viaRootTables[name] || f.viaRootSchemas[sch] {
			return rel.AncestorOIDs[i], true
		}
	}
	return 0, false
}

// schemaFlags is the union of the schema publications covering rel — its
// own schema and its partition ancestors' (pgoutput get_rel_sync_entry:
// GetSchemaPublications of the relation's namespace, then of each ancestor's).
func (f *publicationFilter) schemaFlags(rel *xlog.RelationDef) (allowFlags, bool) {
	if len(f.bySchema) == 0 || rel == nil {
		return allowFlags{}, false
	}
	var out allowFlags
	found := false
	for _, sch := range append([]string{rel.Schema}, rel.AncestorSchemas...) {
		if fl, ok := f.bySchema[strings.ToLower(sch)]; ok {
			out = unionFlags(out, fl)
			found = true
		}
	}
	return out, found
}

// allowFlags is the bitset of publish-* flags that have been
// observed across the union of subscribed publications. Any one
// publication granting an action is enough.
type allowFlags struct {
	insert, update, delete, truncate bool
	// gencolsStored: a publication covering the relation has
	// publish_generated_columns = stored.
	gencolsStored bool
}

func (f allowFlags) allowsKind(k xlog.ChangeKind) bool {
	switch k {
	case xlog.ChangeInsert:
		return f.insert
	case xlog.ChangeUpdate:
		return f.update
	case xlog.ChangeDelete:
		return f.delete
	case xlog.ChangeTruncate:
		return f.truncate
	}
	return false
}

func (f *publicationFilter) Allows(rel *xlog.RelationDef, kind xlog.ChangeKind) bool {
	if rel == nil {
		return false
	}
	if f.allTablesAllowed.allowsKind(kind) {
		return true
	}
	qname := relQualifiedName(rel)
	if flags, ok := f.byTable[qname]; ok && flags.allowsKind(kind) {
		return true
	}
	if flags, ok := f.schemaFlags(rel); ok && flags.allowsKind(kind) {
		return true
	}
	// A partition of a published partitioned table (publish_via_partition_root
	// = false: published as itself). M0149-0054b.
	for _, anc := range rel.AncestorNames {
		if flags, ok := f.byTable[anc]; ok && flags.allowsKind(kind) {
			return true
		}
	}
	return false
}

// ColumnList implements xlog.ColumnListFilter: the union of the subscribed
// publications' column lists for rel, unless one of them publishes the
// whole table. PG requires the lists to be identical (pgoutput_column_list_init
// "cannot use different column lists"); goopg takes their union.
func (f *publicationFilter) ColumnList(rel *xlog.RelationDef) ([]string, bool) {
	if rel == nil || f.allTables {
		return nil, false
	}
	q := relQualifiedName(rel)
	cols, ok := f.colLists[q]
	if !ok || f.wholeTable[q] {
		return nil, false
	}
	// A schema publication publishes the table whole.
	if _, bySchema := f.schemaFlags(rel); bySchema {
		return nil, false
	}
	return cols, true
}

// PublishesStoredGenerated implements xlog.GenColsFilter: a publication
// that covers rel (FOR ALL TABLES or by name) has publish_generated_columns
// = stored. PG requires every publication of a table to agree (pgoutput's
// "cannot use different values of publish_generated_columns"); goopg
// takes their union.
func (f *publicationFilter) PublishesStoredGenerated(rel *xlog.RelationDef) bool {
	if rel == nil {
		return false
	}
	if f.allTablesAllowed.gencolsStored {
		return true
	}
	if f.byTable[relQualifiedName(rel)].gencolsStored {
		return true
	}
	fl, _ := f.schemaFlags(rel)
	return fl.gencolsStored
}

// walsenderCatalogDBOidVar resolves the connection's database name to the
// namespace dbOid the walsender's catalog lookups (snapshot + publication
// filter) must key off. M0119-0006 (deferral row 1354 claim 2): every catalog
// lookup on this path must resolve against the connection's database — upstream
// acquires a logical slot only when the slot's database matches MyDatabaseId
// (slot.c:1760; walsender.c:1447-1518). The physical oid resolveConnDBOid
// returns must be translated through catalog.NamespaceDBOid like every other
// catalog path: goopg persists "postgres"-routed tables/publications under
// DefaultDBOid (1), so a raw PostgresDBOid (5) reads an empty namespace and
// drops every DML (M-NIGHTLY AI-20260815-011722-003, regression of 92d99a25).
// NamespaceDBOid maps 0 and 5 → 1 and passes genuine CREATE DATABASE oids
// through unchanged (catalog.go:23539-23544).
func walsenderCatalogDBOidVar(im *catalog.InMemory, dbName string) []uint32 {
	return []uint32{catalog.NamespaceDBOid(resolveConnDBOid(im, dbName))}
}

// databaseOidResolver is implemented by catalogs that can resolve a database
// name to its real, physical pg_database.oid (catalog.InMemory). Structural
// copy of internal/postmaster/dispatch.go's interface — declaring it locally
// costs three lines and keeps this package independent of postmaster.
type databaseOidResolver interface {
	ResolveDatabaseOid(name string) (uint32, bool)
}

// parsePgoutputBool is defGetBoolean for an output-plugin option value:
// true/false, on/off, yes/no, 1/0 (case-insensitive).
func parsePgoutputBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "on", "yes", "1":
		return true, nil
	case "false", "off", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("messages requires a Boolean value")
}

// resolveConnDBOid resolves a connection's database name to its physical
// pg_database.oid, returning 0 (SearchPathCatalog.effectiveDBOid's fallback)
// when the catalog cannot answer. Copy of internal/postmaster/dispatch.go's
// helper; M0122-0007 slice 4c.
func resolveConnDBOid(cat catalog.Catalog, dbName string) uint32 {
	if dbName == "" {
		return 0
	}
	if dr, ok := cat.(databaseOidResolver); ok {
		if oid, ok := dr.ResolveDatabaseOid(dbName); ok {
			return oid
		}
	}
	return 0
}

// buildPublicationFilter materialises the membership rules from
// the named publications. Unknown publication names are silently
// skipped — upstream rejects them at CREATE SUBSCRIPTION time
// rather than at slot start; v0 follows the lenient path so
// startup doesn't fail when a publication has been dropped.
func buildPublicationFilter(ps *catalog.PubSub, names []string, dbOid ...uint32) *publicationFilter {
	return buildPublicationFilterMissing(ps, names, nil, dbOid...)
}

// buildPublicationFilterMissing is buildPublicationFilter that reports each
// name with no publication to missing (when non-nil).
func buildPublicationFilterMissing(ps *catalog.PubSub, names []string, missing func(name string), dbOid ...uint32) *publicationFilter {
	out := &publicationFilter{
		byTable: map[string]allowFlags{},
	}
	for _, name := range names {
		// The PubSub registry is keyed per-database (pubMapKey(dbOid, name),
		// catalog/pubsub.go); an empty dbOid resolves to DefaultDBOid inside
		// LookupPublication via resolveDBOid. M0119-0006 (deferral row 1354
		// claim 2): a non-default dbOid must find publications registered under
		// that database, or the empty filter silently drops every change.
		pub, ok := ps.LookupPublication(name, dbOid...)
		if !ok {
			if missing != nil {
				missing(name)
			}
			continue
		}
		out.pubs = append(out.pubs, pub)
		flags := allowFlags{
			insert:   pub.PublishInsert,
			update:   pub.PublishUpdate,
			delete:   pub.PublishDelete,
			truncate: pub.PublishTruncate,

			gencolsStored: pub.GenColsType() == 's',
		}
		if pub.AllTables {
			out.allTablesAllowed = unionFlags(out.allTablesAllowed, flags)
			out.allTables = true
			if pub.PublishViaRoot {
				// FOR ALL TABLES publishes every partitioned root, so a
				// partition's change goes out as its topmost ancestor's.
				// M0149-0061b.
				out.viaRootAll = true
			}
			continue
		}
		if pub.PublishViaRoot {
			for _, q := range pub.Tables {
				if out.viaRootTables == nil {
					out.viaRootTables = map[string]bool{}
				}
				out.viaRootTables[q] = true
			}
			for _, sch := range pub.Schemas {
				if out.viaRootSchemas == nil {
					out.viaRootSchemas = map[string]bool{}
				}
				out.viaRootSchemas[strings.ToLower(sch)] = true
			}
		}
		for _, sch := range pub.Schemas {
			if out.bySchema == nil {
				out.bySchema = map[string]allowFlags{}
			}
			k := strings.ToLower(sch)
			out.bySchema[k] = unionFlags(out.bySchema[k], flags)
		}
		for _, qname := range pub.Tables {
			out.byTable[qname] = unionFlags(out.byTable[qname], flags)
			if cols, ok := pub.Columns[qname]; ok {
				if out.colLists == nil {
					out.colLists = map[string][]string{}
				}
				out.colLists[qname] = append(out.colLists[qname], cols...)
			} else {
				if out.wholeTable == nil {
					out.wholeTable = map[string]bool{}
				}
				out.wholeTable[qname] = true
			}
		}
	}
	return out
}

// livePublicationFilter re-resolves the stream's publication_names whenever
// the publication registry changes (create, drop, rename, ALTER PUBLICATION
// … ADD/SET/DROP TABLE, options): pgoutput's publication_invalidation_cb
// marks its publications invalid and get_rel_sync_entry reloads them BY
// NAME, so a publication renamed away stops feeding the stream and one
// renamed into a subscribed name starts. The filter is consulted from the
// single decoder goroutine. M0149-0043b.
//
// The publications are loaded lazily, on the first change consulted after
// the filter is made or the registry changes — get_rel_sync_entry's
// `if (!publications_valid) LoadPublications(...)` — and each requested
// name with no publication logs LoadPublications' WARNING "skipped loading
// publication". 024_add_drop_pub.pl waits for that warning after SET
// PUBLICATION names a publication that does not exist yet. M0149-0052a.
type livePublicationFilter struct {
	ps     *catalog.PubSub
	names  []string
	dbOid  []uint32
	gen    uint64
	loaded bool
	cur    *publicationFilter
	// cat evaluates the publications' row filters (M0149-0059c).
	cat catalog.Catalog
}

// withCatalog sets the catalog the row filters are evaluated against.
func (f *livePublicationFilter) withCatalog(cat catalog.Catalog) *livePublicationFilter {
	f.cat = cat
	return f
}

// RowFilter implements xlog.RowFilterFilter.
func (f *livePublicationFilter) RowFilter(rel *xlog.RelationDef, kind xlog.ChangeKind) (xlog.RowFilterEval, bool) {
	return f.load().RowFilter(rel, kind)
}

func newLivePublicationFilter(ps *catalog.PubSub, names []string, dbOid ...uint32) *livePublicationFilter {
	return &livePublicationFilter{ps: ps, names: names, dbOid: dbOid}
}

// load (re)builds the filter when it has not been loaded yet or the
// registry generation moved.
func (f *livePublicationFilter) load() *publicationFilter {
	if g := f.ps.PublicationGeneration(); !f.loaded || g != f.gen {
		f.gen, f.loaded = g, true
		f.cur = buildPublicationFilterMissing(f.ps, f.names, func(name string) {
			slog.Warn(fmt.Sprintf("skipped loading publication \"%s\"", name),
				"detail", "The publication does not exist at this point in the WAL.",
				"hint", "Create the publication if it does not exist.")
		}, f.dbOid...)
		f.cur.cat = f.cat
		if len(f.dbOid) > 0 {
			f.cur.dbOid = f.dbOid[0]
		}
	}
	return f.cur
}

// PublishAsAncestor implements xlog.ViaRootFilter.
func (f *livePublicationFilter) PublishAsAncestor(rel *xlog.RelationDef) (uint32, bool) {
	return f.load().PublishAsAncestor(rel)
}

// Allows implements xlog.RelationFilter.
func (f *livePublicationFilter) Allows(rel *xlog.RelationDef, kind xlog.ChangeKind) bool {
	return f.load().Allows(rel, kind)
}

// ColumnList implements xlog.ColumnListFilter.
func (f *livePublicationFilter) ColumnList(rel *xlog.RelationDef) ([]string, bool) {
	return f.load().ColumnList(rel)
}

// PublishesStoredGenerated implements xlog.GenColsFilter.
func (f *livePublicationFilter) PublishesStoredGenerated(rel *xlog.RelationDef) bool {
	return f.load().PublishesStoredGenerated(rel)
}

// unionFlags ORs the publish-flag bitsets — a relation gets the
// most-permissive treatment across every publication that
// covers it. Mirrors upstream's "any publication grants the
// action" rule.
func unionFlags(a, b allowFlags) allowFlags {
	return allowFlags{
		insert:   a.insert || b.insert,
		update:   a.update || b.update,
		delete:   a.delete || b.delete,
		truncate: a.truncate || b.truncate,

		gencolsStored: a.gencolsStored || b.gencolsStored,
	}
}

// relQualifiedName renders a wal.RelationDef the same way
// catalog.PubSub stores publication-table names ("schema.name")
// so the lookup is byte-equal.
func relQualifiedName(rel *xlog.RelationDef) string {
	if rel.Schema == "" {
		return rel.Name
	}
	return rel.Schema + "." + rel.Name
}

// splitPublicationNames parses the `publication_names` option
// value libpq's logical-replication client embeds as the raw
// argument to START_REPLICATION. The shape mirrors upstream
// PG's `SplitIdentifierString(rawstring, ',', ...)` semantics
// (postgres/src/backend/utils/adt/varlena.c): each comma-
// separated entry may be a double-quoted identifier (with
// doubled `""` collapsing to a single `"` inside) or an
// unquoted identifier (lowercased to match `downcase_truncate_identifier`).
// libpqwalreceiver always emits quoted names (one per
// publication), so without unquoting here the lookup keys
// don't match what `execCreatePublication` stored. Whitespace
// around each entry is tolerated. Returns nil on syntax error
// so START_REPLICATION proceeds with an empty filter (the
// caller logs and proceeds as for "no publications matched"
// rather than tearing the connection down — upstream rejects
// at SUBSCRIPTION DDL time, well before the wire-level slot
// is started). See 0103-0016.
func splitPublicationNames(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	r := []rune(raw)
	i := 0
	skipSpace := func() {
		for i < len(r) && unicodeIsSpace(r[i]) {
			i++
		}
	}
	for {
		skipSpace()
		if i >= len(r) {
			return out
		}
		// Lenient: empty entries between commas (e.g. `p1,,p2`)
		// drop silently. Upstream's SplitIdentifierString treats
		// these as a syntax error, but the existing goopg
		// behaviour was permissive and the wire libpq actually
		// emits never produces them — so the lenient path keeps
		// the legacy contract intact without weakening the
		// quoted-identifier handling that 0103-0016 adds.
		if r[i] == ',' {
			i++
			continue
		}
		var name strings.Builder
		if r[i] == '"' {
			i++ // consume opening quote
			for {
				if i >= len(r) {
					return nil // unterminated quoted identifier
				}
				if r[i] == '"' {
					if i+1 < len(r) && r[i+1] == '"' {
						name.WriteRune('"')
						i += 2
						continue
					}
					i++ // consume closing quote
					break
				}
				name.WriteRune(r[i])
				i++
			}
		} else {
			start := i
			for i < len(r) && r[i] != ',' && !unicodeIsSpace(r[i]) {
				i++
			}
			if i == start {
				return nil // empty unquoted identifier (shouldn't reach)
			}
			name.WriteString(strings.ToLower(string(r[start:i])))
		}
		if s := name.String(); s != "" {
			out = append(out, s)
		}
		skipSpace()
		if i >= len(r) {
			return out
		}
		if r[i] != ',' {
			return nil // syntax error: junk after identifier
		}
		i++ // consume comma
	}
}

// unicodeIsSpace mirrors PG's scanner_isspace — only ASCII
// whitespace characters that the SQL scanner treats as token
// separators (space, tab, newline, carriage-return, form-feed,
// vertical-tab). Avoids unicode.IsSpace's broader definition so
// the split behaviour matches upstream byte-for-byte.
func unicodeIsSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}
