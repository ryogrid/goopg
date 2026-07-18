package executor

// B3.7 (docs/design/wal-pg-identical-stream/02d §3b): CREATE/DROP ACCESS
// METHOD journal as real pg_am heap rows, replacing the bespoke
// RecordKindCreateAccessMethod(70)/DropAccessMethod(71). FormData_pg_am
// (postgres/src/include/catalog/pg_am.h) is four scalars — oid, amname,
// amhandler (a pg_proc OID, already resolved in the registry), amtype (the
// 'i'/'t' char). Narrow surface, like the sequence OWNED-BY pg_depend
// writer (B1.3b): NO index maintenance — goopg bootstraps neither
// pg_am_oid_index (2652) nor pg_am_name_index (2651) (they are absent
// entirely, not empty placeholders), so a runtime index insert has nowhere
// to land; the startup reload seq-scans the pg_am heap instead. Adding those
// two indexes (populated with the 7 built-in AM rows, the B2.1a pattern) is
// a separate standby-completeness task, ledgered.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const pgAmRelOID = 2601

// PGAccessMethodColumnsPG18 mirrors FormData_pg_am (4 columns). Exported for
// the initdb reload.
func PGAccessMethodColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "amname", Type: catalog.Type{Name: "name"}},
		{Name: "amhandler", Type: catalog.Type{Name: "regproc"}},
		{Name: "amtype", Type: catalog.Type{Name: "char"}},
	}
}

func buildPGAccessMethodRow(am *catalog.AccessMethod) Row {
	amtype := am.AMType
	if amtype == "" {
		amtype = "i"
	}
	return Row{
		NewIntDatum(int64(am.OID)),
		NewStringDatum(am.Name),
		NewIntDatum(int64(am.HandlerOID)),
		NewStringDatum(amtype),
	}
}

func pgAmRel() storage.RelFileNode {
	return storage.RelFileNode{DBOid: catalog.DefaultDBOid, RelOid: pgAmRelOID, Fork: storage.MainFork}
}

// writeAccessMethodCatalogRow journals CREATE ACCESS METHOD: a pg_am heap
// INSERT preceded by an xmax stamp of any prior row version for the same OID
// (defensive — CREATE ACCESS METHOD has no OR REPLACE form, so this is
// normally a plain INSERT).
func writeAccessMethodCatalogRow(ctx *Context, am *catalog.AccessMethod) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampAccessMethodRow(ctx, am.OID)
	if _, err := writeHeapRowCanonical(ctx, pgAmRel(), PGAccessMethodColumnsPG18(), buildPGAccessMethodRow(am)); err != nil {
		return err
	}
	mirrorAccessMethodCatalogFiles(ctx)
	return nil
}

// deleteAccessMethodCatalogRow stamps xmax on the access method's row (DROP).
func deleteAccessMethodCatalogRow(ctx *Context, amOID uint32) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return
	}
	stampAccessMethodRow(ctx, amOID)
	mirrorAccessMethodCatalogFiles(ctx)
}

// stampAccessMethodRow marks every live pg_am row for amOID (oid = column 0)
// deleted. The caller has materialized the writer XID.
func stampAccessMethodRow(ctx *Context, amOID uint32) {
	stampCatalogRows(ctx, pgAmRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == amOID
	})
}

// mirrorAccessMethodCatalogFiles propagates the pg_am heap to base/5 (reload
// reads it there). No index files exist to mirror.
func mirrorAccessMethodCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgAmRelOID)
}
