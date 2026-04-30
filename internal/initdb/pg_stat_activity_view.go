package initdb

import (
	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/catalog"
)

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
			{Name: "pid", Type: catalog.Type{Name: "text"}},
			{Name: "leader_pid", Type: catalog.Type{Name: "text"}},
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
				b.PID,
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
