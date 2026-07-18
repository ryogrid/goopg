package executor

// B4.2 (docs/design/wal-pg-identical-stream/02d §3/§3b): ALTER DATABASE/ROLE
// SET/RESET/RESET ALL journal as real pg_db_role_setting heap rows (SHARED,
// global/2964), replacing the bespoke RecordKindAlterDatabaseSetConfig(73)/
// ResetConfig(74)/ResetAllConfig(75) and the pg_authid-side AlterRoleSetConfig
// (76)/ResetConfig(77)/ResetAllConfig(78). One heap row per (setdatabase,
// setrole) pair carries the whole setconfig text[] ("name=value" GUC list);
// goopg holds that list in its dbRoleSettings/roleSettings registries, so any
// SET/RESET re-syncs the single affected row (stamp old + write current) —
// collapsing all six mutation kinds into one row-rewrite (the B3.6
// pg_ts_config_map pattern).
//
// Like every non-boot-critical shared catalog in goopg (see
// bootstrapSharedCatalogPlaceholders), pg_db_role_setting's index (2965) is NOT
// materialized in global/ — PG reads the catalog by seq scan at login — so this
// writer maintains the heap only, no index, no base/5 mirror. Column layout:
// postgres/src/include/catalog/pg_db_role_setting.h.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const pgDbRoleSettingRelOID = 2964

// PGDbRoleSettingColumnsPG18 mirrors FormData_pg_db_role_setting (3 columns).
// Exported for the initdb reload.
func PGDbRoleSettingColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "setdatabase", Type: catalog.Type{Name: "oid"}},
		{Name: "setrole", Type: catalog.Type{Name: "oid"}},
		{Name: "setconfig", Type: catalog.Type{Name: "text", IsArray: true}},
	}
}

func pgDbRoleSettingRel() storage.RelFileNode {
	// DBOid 0 → global/ (shared catalog); the B4.1a WAL encoder stamps the
	// block-ref locator with spcOid=1664/dbOid=0 for the standby.
	return storage.RelFileNode{DBOid: 0, RelOid: pgDbRoleSettingRelOID, Fork: storage.MainFork}
}

// SyncDbRoleSettingRow re-syncs the single pg_db_role_setting row keyed by
// (setDatabase, setRole): it stamps any existing live row for that key deleted,
// then — if entries is non-empty — writes the current setconfig. RESET ALL (or
// removing the last entry) passes an empty slice, leaving only the xmax stamp
// (row gone). Exported for the server-layer ALTER DATABASE/ROLE SET handlers.
func SyncDbRoleSettingRow(ctx *Context, setDatabase, setRole uint32, entries []string) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampDbRoleSettingRow(ctx, setDatabase, setRole)
	if len(entries) == 0 {
		return nil
	}
	row := Row{
		NewIntDatum(int64(setDatabase)),
		NewIntDatum(int64(setRole)),
		NewStringDatum(formatTextArray(entries)),
	}
	if _, err := writeHeapRowCanonical(ctx, pgDbRoleSettingRel(), PGDbRoleSettingColumnsPG18(), row); err != nil {
		return err
	}
	return nil
}

// stampDbRoleSettingRow marks every live pg_db_role_setting row for the
// (setDatabase, setRole) key deleted. setdatabase/setrole are the two leading
// fixed-width non-null columns, so the heap payload packs them at [0:4]/[4:8].
// The caller has materialized the writer XID.
func stampDbRoleSettingRow(ctx *Context, setDatabase, setRole uint32) {
	stampCatalogRows(ctx, pgDbRoleSettingRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 8 &&
			binary.LittleEndian.Uint32(data[0:4]) == setDatabase &&
			binary.LittleEndian.Uint32(data[4:8]) == setRole
	})
}
