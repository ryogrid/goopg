package initdb

import (
	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/catalog"
)

// numericPIDOrNull returns pid unchanged when it is a non-empty decimal
// integer (regular client backends use monotonic numeric pids), or "" (NULL)
// otherwise. Background-worker pids such as "cp-0" are non-numeric and would
// fail an int4 decode, so they surface as NULL in the int4 pid column.
func numericPIDOrNull(pid string) string {
	if pid == "" {
		return ""
	}
	for _, r := range pid {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return pid
}

// registerPgStatActivityView registers the pg_catalog.pg_stat_activity
// virtual view (M0022 Stage A).
func registerPgStatActivityView(cat *catalog.InMemory, reg *activity.Registry) error {
	tbl := &catalog.Table{
		Schema:  "pg_catalog",
		Name:    "pg_stat_activity",
		Virtual: true,
		Columns: []catalog.Column{
			{Name: "datid", Type: catalog.Type{Name: "text"}},
			{Name: "datname", Type: catalog.Type{Name: "text"}},
			// pid / leader_pid are int4 in PostgreSQL. Typing them as int4 (not
			// text) lets pg_locks JOIN pg_stat_activity USING (pid) match — the
			// pg_locks.pid column is int4, and a text↔int4 join silently yields
			// zero rows. Non-numeric internal pids (background workers such as
			// "cp-0") are emitted as NULL so the int4 decode never fails.
			// M0100-0006b.
			{Name: "pid", Type: catalog.Type{Name: "int4"}},
			{Name: "leader_pid", Type: catalog.Type{Name: "int4"}},
			{Name: "usesysid", Type: catalog.Type{Name: "text"}},
			{Name: "usename", Type: catalog.Type{Name: "text"}},
			{Name: "application_name", Type: catalog.Type{Name: "text"}},
			{Name: "client_addr", Type: catalog.Type{Name: "text"}},
			{Name: "client_hostname", Type: catalog.Type{Name: "text"}},
			{Name: "client_port", Type: catalog.Type{Name: "text"}},
			{Name: "backend_start", Type: catalog.Type{Name: "text"}},
			{Name: "xact_start", Type: catalog.Type{Name: "text"}},
			{Name: "query_start", Type: catalog.Type{Name: "text"}},
			{Name: "state_change", Type: catalog.Type{Name: "text"}},
			{Name: "wait_event_type", Type: catalog.Type{Name: "text"}},
			{Name: "wait_event", Type: catalog.Type{Name: "text"}},
			{Name: "state", Type: catalog.Type{Name: "text"}},
			{Name: "backend_xid", Type: catalog.Type{Name: "text"}},
			{Name: "backend_xmin", Type: catalog.Type{Name: "text"}},
			{Name: "query", Type: catalog.Type{Name: "text"}},
			{Name: "backend_type", Type: catalog.Type{Name: "text"}},
		},
	}
	tbl.VirtualRows = func() [][]string {
		if reg == nil {
			return nil
		}
		snap := reg.Snapshot()
		rows := make([][]string, 0, len(snap))
		for _, b := range snap {
			rows = append(rows, []string{
				b.DatID,
				b.DatName,
				numericPIDOrNull(b.PID),
				"", // leader_pid: always NULL
				b.UserSysID,
				b.UserName,
				b.ApplicationName,
				b.ClientAddr,
				"", // client_hostname: always NULL
				b.ClientPort,
				b.BackendStart,
				b.XactStart,
				b.QueryStart,
				b.StateChange,
				"", // wait_event_type: NULL in Stage A
				"", // wait_event: NULL in Stage A
				b.State,
				b.BackendXID,
				b.BackendXMin,
				b.Query,
				b.BackendType,
			})
		}
		return rows
	}
	return cat.RegisterVirtualTable(tbl)
}
