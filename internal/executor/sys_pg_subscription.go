package executor

// B4.4 (docs/design/wal-pg-identical-stream/02d §3 B4): CREATE/DROP/ALTER
// SUBSCRIPTION OWNER journal a real pg_subscription heap row (SHARED,
// global/6100), replacing the bespoke RecordKindCreateSubscription(53)/
// DropSubscription(54)/AlterSubscriptionOwner(55). This closes the pub/sub
// group (pg_publication converted in B3.3). goopg's PubSub registry tracks a
// SUBSET of PG's 18 pg_subscription columns (name/oid/conninfo/owner/
// publications/enabled/slotname/dbid); the other 10 are written with their PG
// defaults so a real standby's pg_subscription is well-formed.
//
// Like every non-boot-critical shared catalog in goopg (see
// bootstrapSharedCatalogPlaceholders), pg_subscription's indexes (6114/6115)
// are NOT materialized in global/, so PG reads it by seq scan and a heap
// INSERT/xmax-stamp alone is faithful — no runtime index maintenance. The
// registry is goopg's query truth (a restart reloads it from this heap), so
// each CREATE/ALTER re-syncs the single row (stamp by oid + write current) and
// DROP stamps by the oid captured before the registry removal. Column layout:
// postgres/src/include/catalog/pg_subscription.h (PG18).

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const pgSubscriptionRelOID = 6100

// PGSubscriptionColumnsPG18 mirrors FormData_pg_subscription (18 columns, PG18
// order: 13 fixed-width then 5 varlen). Exported for the initdb reload.
func PGSubscriptionColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "subdbid", Type: catalog.Type{Name: "oid"}},
		{Name: "subskiplsn", Type: catalog.Type{Name: "pg_lsn"}},
		{Name: "subname", Type: catalog.Type{Name: "name"}},
		{Name: "subowner", Type: catalog.Type{Name: "oid"}},
		{Name: "subenabled", Type: catalog.Type{Name: "bool"}},
		{Name: "subbinary", Type: catalog.Type{Name: "bool"}},
		{Name: "substream", Type: catalog.Type{Name: "char"}},
		{Name: "subtwophasestate", Type: catalog.Type{Name: "char"}},
		{Name: "subdisableonerr", Type: catalog.Type{Name: "bool"}},
		{Name: "subpasswordrequired", Type: catalog.Type{Name: "bool"}},
		{Name: "subrunasowner", Type: catalog.Type{Name: "bool"}},
		{Name: "subfailover", Type: catalog.Type{Name: "bool"}},
		{Name: "subconninfo", Type: catalog.Type{Name: "text"}},
		{Name: "subslotname", Type: catalog.Type{Name: "name"}},
		{Name: "subsynccommit", Type: catalog.Type{Name: "text"}},
		{Name: "subpublications", Type: catalog.Type{Name: "text", IsArray: true}},
		{Name: "suborigin", Type: catalog.Type{Name: "text"}},
	}
}

func buildPGSubscriptionRow(sub *catalog.Subscription) Row {
	owner := sub.Owner
	if owner == 0 {
		owner = 10 // BOOTSTRAP_SUPERUSERID
	}
	slot := NullDatum
	if sub.SlotName != "" {
		slot = NewStringDatum(sub.SlotName)
	}
	return Row{
		NewIntDatum(int64(sub.OID)),   // oid
		NewIntDatum(int64(sub.DBOid)), // subdbid
		NewIntDatum(0),                // subskiplsn — pg_lsn 0/0 (no skip)
		NewStringDatum(sub.Name),      // subname
		NewIntDatum(int64(owner)),     // subowner
		NewBoolDatum(sub.Enabled),     // subenabled
		NewBoolDatum(false),           // subbinary
		NewStringDatum("f"),           // substream — LOGICALREP_STREAM_OFF
		NewStringDatum("d"),           // subtwophasestate — LOGICALREP_TWOPHASE_STATE_DISABLED
		NewBoolDatum(false),           // subdisableonerr
		NewBoolDatum(true),            // subpasswordrequired
		NewBoolDatum(false),           // subrunasowner
		NewBoolDatum(false),           // subfailover
		NewStringDatum(sub.Conninfo),  // subconninfo
		slot,                          // subslotname (NULL when unset)
		NewStringDatum("off"),         // subsynccommit
		NewStringDatum(formatTextArray(sub.Publications)), // subpublications text[]
		NewStringDatum("any"),                             // suborigin — LOGICALREP_ORIGIN_ANY
	}
}

func pgSubscriptionRel() storage.RelFileNode {
	// DBOid 0 → global/ (shared catalog); the B4.1a WAL encoder stamps the
	// block-ref locator with spcOid=1664/dbOid=0 for the standby.
	return storage.RelFileNode{DBOid: 0, RelOid: pgSubscriptionRelOID, Fork: storage.MainFork}
}

// syncSubscriptionRow re-syncs the single pg_subscription row for oid: it
// stamps any live row for that oid, then — if sub is non-nil — writes its
// current state. DROP passes sub=nil (oid captured before the registry drop);
// CREATE/ALTER OWNER pass the looked-up subscription.
func syncSubscriptionRow(ctx *Context, oid uint32, sub *catalog.Subscription) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampSubscriptionRow(ctx, oid)
	if sub == nil {
		return nil
	}
	if _, err := writeHeapRowCanonical(ctx, pgSubscriptionRel(), PGSubscriptionColumnsPG18(), buildPGSubscriptionRow(sub)); err != nil {
		return err
	}
	return nil
}

// stampSubscriptionRow marks every live pg_subscription row for oid (column 0)
// deleted. The caller has materialized the writer XID.
func stampSubscriptionRow(ctx *Context, oid uint32) {
	stampCatalogRows(ctx, pgSubscriptionRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 4 && binary.LittleEndian.Uint32(data[0:4]) == oid
	})
}
