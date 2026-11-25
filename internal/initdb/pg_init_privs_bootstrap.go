package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// bootstrapPgInitPrivs writes the 229 pg_init_privs rows a PostgreSQL 18.3
// initdb records in setup_privileges() (postgres/src/bin/initdb/initdb.c) to
// base/{1,5}/3394 and its index 3395 pg_init_privs_o_c_o_index on (objoid,
// classoid, objsubid). Without them a PG node's pg_dump sees every built-in
// ACL as a user change — the LEFT JOIN on pg_init_privs yields NULL and
// pg_dump emits GRANT/REVOKE for, e.g., schema public. The rows are
// catalog.PGInitdbInitPrivs, the same table goopg's own pg_init_privs view
// serves. M0149-0001g slice 22.
func bootstrapPgInitPrivs(dataDir string) error {
	cols := []catalog.Column{
		{Name: "objoid", Type: catalog.Type{Name: "oid"}},
		{Name: "classoid", Type: catalog.Type{Name: "oid"}},
		{Name: "objsubid", Type: catalog.Type{Name: "int4"}},
		{Name: "privtype", Type: catalog.Type{Name: "char"}},
		{Name: "initprivs", Type: catalog.Type{Name: "aclitem[]"}},
	}
	src := catalog.PGInitdbInitPrivs
	rows := make([]executor.Row, len(src))
	keys := make([]pgDescriptionRow, len(src))
	for i, r := range src {
		what := fmt.Sprintf("pg_init_privs (%d,%d,%d)", r.ObjOID, r.ClassOID, r.ObjSubID)
		rows[i] = executor.Row{executor.NewIntDatum(int64(r.ObjOID)), executor.NewIntDatum(int64(r.ClassOID)),
			executor.NewIntDatum(int64(r.ObjSubID)), executor.NewStringDatum(string(r.PrivType)),
			initdbACLDatum(r.InitPrivs, what)}
		keys[i] = pgDescriptionRow{ObjOID: r.ObjOID, ClassOID: r.ClassOID, ObjSubID: r.ObjSubID}
	}
	tids, err := writeMultiPageHeapRows(dataDir, "3394", cols, rows)
	if err != nil {
		return fmt.Errorf("pg_init_privs heap: %w", err)
	}
	return writeDescriptionIndex(dataDir, []string{"base/1", "base/5"}, "3395", keys, tids, 3)
}
