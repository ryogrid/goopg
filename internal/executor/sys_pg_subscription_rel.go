package executor

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pgSubscriptionRelRelOID is pg_subscription_rel's OID (a per-database
// catalog, base/<db>/6102).
const pgSubscriptionRelRelOID = 6102

// PGSubscriptionRelColumnsPG18 mirrors FormData_pg_subscription_rel
// (srsubid, srrelid, srsubstate, srsublsn). Exported for the initdb reload.
func PGSubscriptionRelColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "srsubid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "srrelid", Type: catalog.Type{Name: "oid"}, Ordinal: 1},
		{Name: "srsubstate", Type: catalog.Type{Name: "char"}, Ordinal: 2},
		{Name: "srsublsn", Type: catalog.Type{Name: "pg_lsn"}, Ordinal: 3},
	}
}

// JournalSubscriptionRel writes one pg_subscription_rel change to the
// heap in ctx's transaction: the rel's previous row is stamped dead, and
// unless deleted the new state is written (AddSubscriptionRel /
// UpdateSubscriptionRelState / RemoveSubscriptionRel). sr.RelOID == 0 with
// deleted removes every row of the subscription (DROP SUBSCRIPTION).
// Before this the tablesync state lived only in memory, so after a
// subscriber restart pg_subscription_rel was empty and the apply worker
// skipped every change (should_apply_changes_for_rel found no READY rel).
// M0149-0061m.
func JournalSubscriptionRel(ctx *Context, sr catalog.SubscriptionRel, deleted bool) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	rel := storage.RelFileNode{DBOid: tableCatalogHeapDBOid(ctx), RelOid: pgSubscriptionRelRelOID, Fork: storage.MainFork}
	stampCatalogRows(ctx, rel, ctx.Tx.XID, func(data []byte) bool {
		if len(data) < 8 || binary.LittleEndian.Uint32(data[0:4]) != sr.SubID {
			return false
		}
		return sr.RelOID == 0 || binary.LittleEndian.Uint32(data[4:8]) == sr.RelOID
	})
	if deleted {
		return nil
	}
	row := Row{
		NewIntDatum(int64(sr.SubID)),
		NewIntDatum(int64(sr.RelOID)),
		NewStringDatum(sr.State),
		NewIntDatum(int64(sr.LSN)),
	}
	_, err := writeHeapRowCanonical(ctx, rel, PGSubscriptionRelColumnsPG18(), row)
	return err
}
