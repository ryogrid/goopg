// Replication observability virtual views: `pg_stat_replication`
// (one row per active walsender) and `pg_stat_wal_receiver` (zero
// or one row, the standby's walreceiver if any).
//
// Column order and naming mirror upstream PG 18.x. Fields v0 doesn't
// track yet (sync_state, write_lag, flush_lag, replay_lag,
// reply_time on the sender; latest_end_lsn vs received_tli on the
// receiver) are emitted as either NULL-equivalent empty strings or
// "0", documented inline. The seam exists so a future loop can fill
// them without changing the catalog wiring.
//
// See docs/design/0005-0003-replication-observability.md.

package initdb

import (
	"fmt"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// registerStatReplicationView installs `pg_catalog.pg_stat_replication`
// backed by the process-wide *wal.Senders registry. The optional
// writer pointer surfaces M0010-0002's in-memory ring counters
// (`send_buffer_hits` / `send_buffer_misses` /
// `send_buffer_bytes_resident`) — these are cluster-wide rather
// than per-sender, so every row reports the same value. The
// counters live alongside the per-sender LSN fields here so an
// operator's `\watch pg_stat_replication` shows ring health in
// the same query.
func registerStatReplicationView(cat *catalog.InMemory, senders *wal.Senders, writer *wal.Writer, syncRep *wal.SyncRep) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_stat_replication",
		Columns: []catalog.Column{
			{Name: "pid", Type: catalog.Type{Name: "text"}},
			{Name: "usesysid", Type: catalog.Type{Name: "text"}},
			{Name: "usename", Type: catalog.Type{Name: "text"}},
			{Name: "application_name", Type: catalog.Type{Name: "text"}},
			{Name: "client_addr", Type: catalog.Type{Name: "text"}},
			{Name: "client_hostname", Type: catalog.Type{Name: "text"}},
			{Name: "client_port", Type: catalog.Type{Name: "text"}},
			{Name: "backend_start", Type: catalog.Type{Name: "text"}},
			{Name: "backend_xmin", Type: catalog.Type{Name: "text"}},
			{Name: "state", Type: catalog.Type{Name: "text"}},
			{Name: "sent_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "write_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "flush_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "replay_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "write_lag", Type: catalog.Type{Name: "text"}},
			{Name: "flush_lag", Type: catalog.Type{Name: "text"}},
			{Name: "replay_lag", Type: catalog.Type{Name: "text"}},
			{Name: "sync_priority", Type: catalog.Type{Name: "text"}},
			{Name: "sync_state", Type: catalog.Type{Name: "text"}},
			{Name: "reply_time", Type: catalog.Type{Name: "text"}},
			{Name: "slot_name", Type: catalog.Type{Name: "text"}},
			// M0010-0002 / M0010-0003: cluster-wide ring counters.
			// Per-sender breakdown isn't useful — every sender
			// reads from the same ring — so the same values
			// appear on every row for consistency with the
			// upstream-aligned per-row shape.
			{Name: "send_buffer_hits", Type: catalog.Type{Name: "text"}},
			{Name: "send_buffer_misses", Type: catalog.Type{Name: "text"}},
			{Name: "send_buffer_bytes_resident", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		if senders == nil {
			return nil
		}
		var hits, misses uint64
		var resident int64
		if writer != nil {
			if ring := writer.MemRing(); ring != nil {
				hits = ring.Hits()
				misses = ring.Misses()
				resident = ring.BytesResident()
			}
		}
		hitsStr := fmt.Sprintf("%d", hits)
		missesStr := fmt.Sprintf("%d", misses)
		residentStr := fmt.Sprintf("%d", resident)
		snap := senders.Snapshot()
		out := make([][]string, 0, len(snap))
		for _, s := range snap {
			out = append(out, []string{
				fmt.Sprintf("%d", s.PID),
				"",                // usesysid: roles aren't oid-stable in v0
				"",                // usename
				s.ApplicationName, // application_name
				s.ClientAddr,      // client_addr
				"",                // client_hostname
				"",                // client_port (carried inside ClientAddr in v0)
				formatTime(s.BackendStart),
				"",      // backend_xmin: hot-standby feedback not wired in v0
				s.State, // state
				formatLSN(s.SentLSN),
				formatLSN(s.WriteLSN),
				formatLSN(s.FlushLSN),
				formatLSN(s.ReplayLSN),
				"00:00:00",                               // write_lag: lag intervals not wired in v0
				"00:00:00",                               // flush_lag
				"00:00:00",                               // replay_lag
				syncPriority(syncRep, s.ApplicationName), // sync_priority
				syncState(syncRep, s.ApplicationName),    // sync_state
				"",                                       // reply_time: not tracked per-message in v0
				s.SlotName,                               // slot_name
				hitsStr,
				missesStr,
				residentStr,
			})
		}
		return out
	}
	return cat.RegisterVirtualTable(tbl)
}

// syncState returns the pg_stat_replication.sync_state string for appName.
// Returns "async" when syncRep is nil (no synchronous replication configured).
func syncState(syncRep *wal.SyncRep, appName string) string {
	if syncRep == nil {
		return "async"
	}
	return syncRep.SyncStateFor(appName)
}

// syncPriority returns the pg_stat_replication.sync_priority integer string
// for appName. Returns "0" when syncRep is nil.
func syncPriority(syncRep *wal.SyncRep, appName string) string {
	if syncRep == nil {
		return "0"
	}
	return fmt.Sprintf("%d", syncRep.SyncPriorityFor(appName))
}

// registerStatWalReceiverView installs `pg_catalog.pg_stat_wal_receiver`
// backed by the process-wide *wal.Receivers registry. The view is
// empty when no walreceiver is registered (the standard "primary
// node" case).
func registerStatWalReceiverView(cat *catalog.InMemory, receivers *wal.Receivers) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_stat_wal_receiver",
		Columns: []catalog.Column{
			{Name: "pid", Type: catalog.Type{Name: "text"}},
			{Name: "status", Type: catalog.Type{Name: "text"}},
			{Name: "receive_start_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "receive_start_tli", Type: catalog.Type{Name: "text"}},
			{Name: "written_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "flushed_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "received_tli", Type: catalog.Type{Name: "text"}},
			{Name: "last_msg_send_time", Type: catalog.Type{Name: "text"}},
			{Name: "last_msg_receipt_time", Type: catalog.Type{Name: "text"}},
			{Name: "latest_end_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "latest_end_time", Type: catalog.Type{Name: "text"}},
			{Name: "slot_name", Type: catalog.Type{Name: "text"}},
			{Name: "sender_host", Type: catalog.Type{Name: "text"}},
			{Name: "sender_port", Type: catalog.Type{Name: "text"}},
			{Name: "conninfo", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		if receivers == nil {
			return nil
		}
		st, ok := receivers.Snapshot()
		if !ok {
			return nil
		}
		// receive_start_tli / received_tli: v0 is single-timeline,
		// always 1.
		// written_lsn / flushed_lsn: v0's writer treats Append +
		// FlushUpTo separately but the receiver only Appends; the
		// flush comes via the local writer's normal cadence. We
		// expose ReceivedLSN as the value of both for symmetry.
		// latest_end_* mirror the most-recent-message fields.
		return [][]string{{
			fmt.Sprintf("%d", st.PID),
			st.Status,
			formatLSN(st.ReceiveStartLSN),
			"1",
			formatLSN(st.ReceivedLSN),
			formatLSN(st.ReceivedLSN),
			"1",
			formatTime(st.LastMsgSendTime),
			formatTime(st.LastMsgReceiptTime),
			formatLSN(st.ReceivedLSN),
			formatTime(st.LastMsgReceiptTime),
			st.SlotName,
			st.SenderHost,
			"",
			st.Conninfo,
		}}
	}
	return cat.RegisterVirtualTable(tbl)
}

// registerStatSubscriptionView installs `pg_catalog.pg_stat_subscription`
// backed by the process-wide *wal.Subscribers registry. Column
// shape mirrors upstream PG 18.x. Worker rows are emitted in
// (subname, worker_type, relid, pid) order — the same stable
// ordering Senders / Receivers use — so an operator's repeated
// SELECT against a quiet subscription returns identical bytes.
//
// See docs/design/0008-0005-logical-replication-observability.md.
func registerStatSubscriptionView(cat *catalog.InMemory, subs *wal.Subscribers) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_stat_subscription",
		Columns: []catalog.Column{
			{Name: "subid", Type: catalog.Type{Name: "text"}},
			{Name: "subname", Type: catalog.Type{Name: "text"}},
			{Name: "worker_type", Type: catalog.Type{Name: "text"}},
			{Name: "pid", Type: catalog.Type{Name: "text"}},
			{Name: "leader_pid", Type: catalog.Type{Name: "text"}},
			{Name: "relid", Type: catalog.Type{Name: "text"}},
			{Name: "received_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "last_msg_send_time", Type: catalog.Type{Name: "text"}},
			{Name: "last_msg_receipt_time", Type: catalog.Type{Name: "text"}},
			{Name: "latest_end_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "latest_end_time", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		if subs == nil {
			return nil
		}
		snap := subs.Snapshot()
		out := make([][]string, 0, len(snap))
		for _, s := range snap {
			leaderPID := ""
			if s.LeaderPID != 0 {
				leaderPID = fmt.Sprintf("%d", s.LeaderPID)
			}
			relID := ""
			if s.RelOID != 0 {
				relID = fmt.Sprintf("%d", s.RelOID)
			}
			out = append(out, []string{
				fmt.Sprintf("%d", s.SubID),
				s.SubName,
				string(s.WorkerType),
				fmt.Sprintf("%d", s.PID),
				leaderPID,
				relID,
				formatLSN(s.ReceivedLSN),
				formatTime(s.LastMsgSendTime),
				formatTime(s.LastMsgReceiptTime),
				formatLSN(s.LatestEndLSN),
				formatTime(s.LatestEndTime),
			})
		}
		return out
	}
	return cat.RegisterVirtualTable(tbl)
}

// registerReplicationSlotsView installs `pg_catalog.pg_replication_slots`
// backed by the process-wide *wal.Slots registry. Renders both
// physical and logical slots with the upstream PG 18.x column shape;
// columns goopg doesn't track yet (`temporary`, `xmin`, `safe_wal_size`,
// `two_phase`, `inactive_since`, `failover`, `synced`,
// `conflict_reason`) emit empty strings. See
// docs/design/0008-0001-logical-decoding-pipeline.md.
func registerReplicationSlotsView(cat *catalog.InMemory, slots *wal.Slots) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_replication_slots",
		Columns: []catalog.Column{
			{Name: "slot_name", Type: catalog.Type{Name: "text"}},
			{Name: "plugin", Type: catalog.Type{Name: "text"}},
			{Name: "slot_type", Type: catalog.Type{Name: "text"}},
			{Name: "datoid", Type: catalog.Type{Name: "text"}},
			{Name: "database", Type: catalog.Type{Name: "text"}},
			{Name: "temporary", Type: catalog.Type{Name: "text"}},
			{Name: "active", Type: catalog.Type{Name: "text"}},
			{Name: "active_pid", Type: catalog.Type{Name: "text"}},
			{Name: "xmin", Type: catalog.Type{Name: "text"}},
			{Name: "catalog_xmin", Type: catalog.Type{Name: "text"}},
			{Name: "restart_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "confirmed_flush_lsn", Type: catalog.Type{Name: "text"}},
			{Name: "wal_status", Type: catalog.Type{Name: "text"}},
			{Name: "safe_wal_size", Type: catalog.Type{Name: "text"}},
			{Name: "two_phase", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		if slots == nil {
			return nil
		}
		all := slots.List()
		out := make([][]string, 0, len(all))
		for _, sl := range all {
			out = append(out, []string{
				sl.Name,
				sl.Plugin,
				string(sl.Kind),
				"", // datoid: no oid mapping for db names yet
				sl.Database,
				"f", // temporary: temp slots deferred
				boolText(sl.Active),
				"",  // active_pid: not yet tracked per-slot
				"0", // xmin: physical-slot xmin tracking deferred
				formatXmin(sl.CatalogXmin),
				formatLSN(sl.RestartLSN),
				formatLSN(sl.ConfirmedFlushLSN),
				slotWalStatus(sl),
				"",  // safe_wal_size: not yet computed
				"f", // two_phase: 2PC decoding out of scope
			})
		}
		return out
	}
	return cat.RegisterVirtualTable(tbl)
}

// registerPublicationViews installs `pg_catalog.pg_publication`,
// `pg_catalog.pg_publication_rel`, and `pg_catalog.pg_publication_tables`
// backed by the *catalog.PubSub registry. Column shapes mirror
// upstream PG 18.x. Columns goopg doesn't yet track
// (`pubowner`, `pubviaroot`, `prqual`, `prattrs`) emit empty
// strings / `f`. See
// docs/design/0008-0003-publication-subscription-ddl.md.
func registerPublicationViews(cat *catalog.InMemory, ps *catalog.PubSub) error {
	pgPub := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_publication",
		Columns: []catalog.Column{
			{Name: "oid", Type: catalog.Type{Name: "text"}},
			{Name: "pubname", Type: catalog.Type{Name: "text"}},
			{Name: "pubowner", Type: catalog.Type{Name: "text"}},
			// Boolean flags: declared bool so the analyzer types bare
			// column refs as boolean. psql's `\d` publication probe issues
			// `WHERE p.puballtables AND pg_relation_is_publishable(...)`,
			// which fails with "operator AND requires boolean operands"
			// if these are typed text. VirtualRows emits "t"/"f" (boolText),
			// which TypedVirtualCell decodes to BooleanConst. M0097-0023.
			{Name: "puballtables", Type: catalog.Type{Name: "bool"}},
			{Name: "pubinsert", Type: catalog.Type{Name: "bool"}},
			{Name: "pubupdate", Type: catalog.Type{Name: "bool"}},
			{Name: "pubdelete", Type: catalog.Type{Name: "bool"}},
			{Name: "pubtruncate", Type: catalog.Type{Name: "bool"}},
			{Name: "pubviaroot", Type: catalog.Type{Name: "bool"}},
			// pubgencols (PG18): generated-column publish mode. 'n'(none) if
			// generated column data is not published, 's'(stored) if it is.
			// pg_dump's getPublications selects this column. goopg does not
			// publish generated columns, so 'n' is correct. See pg_publication.h.
			{Name: "pubgencols", Type: catalog.Type{Name: "char"}},
		},
		Virtual: true,
	}
	pgPub.VirtualRows = func() [][]string {
		if ps == nil {
			return nil
		}
		out := make([][]string, 0)
		for _, pub := range ps.Publications() {
			out = append(out, []string{
				fmt.Sprintf("%d", pub.OID),
				pub.Name,
				fmt.Sprintf("%d", pub.Owner), // pubowner
				boolText(pub.AllTables),
				boolText(pub.PublishInsert),
				boolText(pub.PublishUpdate),
				boolText(pub.PublishDelete),
				"f", // pubtruncate: M0008-out-of-scope
				"f", // pubviaroot: out of scope
				"n", // pubgencols: generated cols not published
			})
		}
		return out
	}
	if err := cat.RegisterVirtualTable(pgPub); err != nil {
		return err
	}

	pgPubRel := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_publication_rel",
		Columns: []catalog.Column{
			{Name: "oid", Type: catalog.Type{Name: "oid"}},
			{Name: "prpubid", Type: catalog.Type{Name: "oid"}},
			{Name: "prrelid", Type: catalog.Type{Name: "oid"}},
			{Name: "prqual", Type: catalog.Type{Name: "text"}},
			{Name: "prattrs", Type: catalog.Type{Name: "int2vector"}},
		},
		Virtual: true,
	}
	pgPubRel.VirtualRows = func() [][]string {
		if ps == nil {
			return nil
		}
		out := make([][]string, 0)
		for _, pub := range ps.Publications() {
			if pub.AllTables {
				continue
			}
			for _, qname := range pub.Tables {
				relOID := lookupRelOID(cat, qname)
				out = append(out, []string{
					"", // oid of the pg_publication_rel row itself
					fmt.Sprintf("%d", pub.OID),
					relOID,
					"", // prqual: row filter — out of scope
					"", // prattrs: column list — out of scope
				})
			}
		}
		return out
	}
	if err := cat.RegisterVirtualTable(pgPubRel); err != nil {
		return err
	}

	pgPubTables := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_publication_tables",
		Columns: []catalog.Column{
			{Name: "pubname", Type: catalog.Type{Name: "text"}},
			{Name: "schemaname", Type: catalog.Type{Name: "text"}},
			{Name: "tablename", Type: catalog.Type{Name: "text"}},
			{Name: "attnames", Type: catalog.Type{Name: "text"}},
			{Name: "rowfilter", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	pgPubTables.VirtualRows = func() [][]string {
		if ps == nil {
			return nil
		}
		out := make([][]string, 0)
		for _, pub := range ps.Publications() {
			if pub.AllTables {
				for _, t := range cat.AllTables() {
					out = append(out, pubTableRow(pub.Name, t.Schema, t.Name))
				}
				continue
			}
			for _, qname := range pub.Tables {
				schema, name := splitQualifiedName(qname)
				out = append(out, pubTableRow(pub.Name, schema, name))
			}
		}
		return out
	}
	return cat.RegisterVirtualTable(pgPubTables)
}

// registerSubscriptionViews installs `pg_catalog.pg_subscription`
// and `pg_catalog.pg_subscription_rel` backed by *catalog.PubSub.
// `pg_subscription_rel` emits zero rows in this loop — tablesync
// state lives in the apply worker (0008-0004).
func registerSubscriptionViews(cat *catalog.InMemory, ps *catalog.PubSub) error {
	pgSub := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_subscription",
		Columns: []catalog.Column{
			{Name: "oid", Type: catalog.Type{Name: "text"}},
			{Name: "subdbid", Type: catalog.Type{Name: "text"}},
			{Name: "subname", Type: catalog.Type{Name: "text"}},
			{Name: "subowner", Type: catalog.Type{Name: "text"}},
			{Name: "subenabled", Type: catalog.Type{Name: "text"}},
			{Name: "subbinary", Type: catalog.Type{Name: "text"}},
			{Name: "substream", Type: catalog.Type{Name: "text"}},
			{Name: "subtwophasestate", Type: catalog.Type{Name: "text"}},
			{Name: "subdisableonerr", Type: catalog.Type{Name: "text"}},
			{Name: "subconninfo", Type: catalog.Type{Name: "text"}},
			{Name: "subslotname", Type: catalog.Type{Name: "text"}},
			{Name: "subsynccommit", Type: catalog.Type{Name: "text"}},
			{Name: "subpublications", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	pgSub.VirtualRows = func() [][]string {
		if ps == nil {
			return nil
		}
		out := make([][]string, 0)
		for _, sub := range ps.Subscriptions() {
			out = append(out, []string{
				fmt.Sprintf("%d", sub.OID),
				"", // subdbid
				sub.Name,
				"", // subowner
				boolText(sub.Enabled),
				"f", // subbinary
				"f", // substream
				"d", // subtwophasestate disabled
				"f", // subdisableonerr
				sub.Conninfo,
				sub.SlotName,
				"local", // subsynccommit
				formatStringList(sub.Publications),
			})
		}
		return out
	}
	if err := cat.RegisterVirtualTable(pgSub); err != nil {
		return err
	}

	pgSubRel := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_subscription_rel",
		Columns: []catalog.Column{
			{Name: "srsubid", Type: catalog.Type{Name: "text"}},
			{Name: "srrelid", Type: catalog.Type{Name: "text"}},
			{Name: "srsubstate", Type: catalog.Type{Name: "text"}},
			{Name: "srsublsn", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	pgSubRel.VirtualRows = func() [][]string {
		if ps == nil {
			return nil
		}
		all := ps.AllSubscriptionRels()
		out := make([][]string, 0, len(all))
		for _, sr := range all {
			lsn := ""
			if sr.LSN != 0 {
				lsn = formatLSN(sr.LSN)
			}
			out = append(out, []string{
				fmt.Sprintf("%d", sr.SubID),
				fmt.Sprintf("%d", sr.RelOID),
				sr.State,
				lsn,
			})
		}
		return out
	}
	if err := cat.RegisterVirtualTable(pgSubRel); err != nil {
		return err
	}

	// pg_publication_namespace — namespace-level publication filtering (PG15+).
	// Stub: always empty; goopg does not support schema-level publications.
	pgPubNS := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_publication_namespace",
		Columns: []catalog.Column{
			{Name: "oid", Type: catalog.Type{Name: "oid"}},
			{Name: "pnpubid", Type: catalog.Type{Name: "oid"}},
			{Name: "pnnspid", Type: catalog.Type{Name: "oid"}},
		},
		Virtual: true,
	}
	pgPubNS.VirtualRows = func() [][]string { return nil }
	return cat.RegisterVirtualTable(pgPubNS)
}

// lookupRelOID resolves a qualified table name ("schema.name" or
// just "name") to its OID via the catalog's LookupTable. Empty
// when the table isn't registered — keeps `pg_publication_rel`
// well-formed even for stale publication entries.
func lookupRelOID(cat *catalog.InMemory, qname string) string {
	schema, name := splitQualifiedName(qname)
	t, ok := cat.LookupTable(parser.ObjectName{Schema: schema, Name: name})
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d", t.OID)
}

// splitQualifiedName splits "schema.name" into ("schema", "name");
// a single bare name returns ("", name).
func splitQualifiedName(qname string) (string, string) {
	for i := 0; i < len(qname); i++ {
		if qname[i] == '.' {
			return qname[:i], qname[i+1:]
		}
	}
	return "", qname
}

func pubTableRow(pubname, schema, table string) []string {
	if schema == "" {
		schema = "public"
	}
	return []string{pubname, schema, table, "", ""}
}

func formatStringList(xs []string) string {
	if len(xs) == 0 {
		return "{}"
	}
	out := "{"
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out + "}"
}

// slotWalStatus mirrors upstream's pg_replication_slots.wal_status:
// `reserved` for live slots, `lost` for invalidated ones. The
// `extended` / `unreserved` states upstream uses for the in-between
// lag tier aren't surfaced yet — v0's retention path either keeps
// the slot live or invalidates it outright.
func slotWalStatus(s wal.Slot) string {
	if s.Invalidated {
		return "lost"
	}
	return "reserved"
}

func formatXmin(xid uint64) string {
	if xid == 0 {
		return ""
	}
	return fmt.Sprintf("%d", xid)
}

func boolText(b bool) string {
	if b {
		return "t"
	}
	return "f"
}

// formatLSN renders a uint64 byte position in PostgreSQL's
// `XXXXXXXX/XXXXXXXX` hex form. Mirrors upstream's
// `pg_lsn_out` / LSN-formatting macros so an operator's existing
// `\watch pg_stat_replication` muscle memory transfers verbatim.
// LSN 0 renders as "0/0" (the upstream "no LSN known" sentinel).
func formatLSN(lsn uint64) string {
	hi := uint32(lsn >> 32)
	lo := uint32(lsn)
	return fmt.Sprintf("%X/%X", hi, lo)
}

// formatTime renders an absolute instant in upstream's
// `YYYY-MM-DD HH:MM:SS.mmm-TZ` form. Zero time renders as "" so the
// view doesn't surface placeholder timestamps for unset fields.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05.000-07")
}
