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
	"github.com/goopg/goopg/internal/wal"
)

// registerStatReplicationView installs `pg_catalog.pg_stat_replication`
// backed by the process-wide *wal.Senders registry.
func registerStatReplicationView(cat *catalog.InMemory, senders *wal.Senders) error {
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
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		if senders == nil {
			return nil
		}
		snap := senders.Snapshot()
		out := make([][]string, 0, len(snap))
		for _, s := range snap {
			out = append(out, []string{
				fmt.Sprintf("%d", s.PID),
				"",                 // usesysid: roles aren't oid-stable in v0
				"",                 // usename
				s.ApplicationName,  // application_name
				s.ClientAddr,       // client_addr
				"",                 // client_hostname
				"",                 // client_port (carried inside ClientAddr in v0)
				formatTime(s.BackendStart),
				"",                 // backend_xmin: hot-standby feedback not wired in v0
				s.State,            // state
				formatLSN(s.SentLSN),
				formatLSN(s.WriteLSN),
				formatLSN(s.FlushLSN),
				formatLSN(s.ReplayLSN),
				"00:00:00",         // write_lag: lag intervals not wired in v0
				"00:00:00",         // flush_lag
				"00:00:00",         // replay_lag
				"0",                // sync_priority: no synchronous replication in v0
				"async",            // sync_state: hard-coded to async in v0
				"",                 // reply_time: not tracked per-message in v0
				s.SlotName,         // slot_name
			})
		}
		return out
	}
	return cat.RegisterVirtualTable(tbl)
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
