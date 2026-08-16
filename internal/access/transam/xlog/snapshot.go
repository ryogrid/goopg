// Slot-creation-time HISTORIC snapshot for the M0008 logical
// decoder.
//
// Upstream's `SnapBuild` state machine tracks which transactions
// were committed when a slot started so the decoder can interpret
// historic catalog state and decide tuple visibility. The full
// machine is large (it tracks the slot's progress through
// initial / building / consistent states across multiple
// running-xact snapshots).
//
// v0's first slice is "snapshot built at slot creation stays
// consistent for the slot's lifetime" — schema changes during
// the slot's life are out of scope for this milestone (they
// surface as decoder errors when they bite). This file delivers:
//
//   - `CatalogSnapshot` — frozen, per-RelOid map of relation
//     metadata captured at a point in time. Immutable after
//     construction; safe for concurrent reads.
//   - `BuildCatalogSnapshot(c)` — captures the current catalog
//     state via `catalog.AllTables`.
//   - `SlotSnapshot{Catalog, MVCC}` — the bundle a slot
//     decoder hands a plugin. The MVCC snapshot is upstream's
//     `mvcc.Snapshot` value (already a frozen view).
//
// See docs/design/0008-0001-logical-decoding-pipeline.md.
package xlog

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
)

// RelationDef is the immutable per-relation snapshot the M0008
// plugin reads to interpret tuple bytes. v0 v0 carries just
// the columns and the OID triple; replica-identity, type-modifier
// columns, and the rest land alongside pgoutput in 0008-0002.
type RelationDef struct {
	Schema  string
	Name    string
	OID     uint32
	Columns []ColumnDef
}

// ColumnDef mirrors `catalog.Column` but lives in the snapshot
// (no Stats). Future columns (replica identity flag, attoptions)
// land here.
type ColumnDef struct {
	Name    string
	Type    catalog.Type
	NotNull bool
	Ordinal int
}

// CatalogSnapshot is the slot-creation-time view of all user
// tables. Lookup is keyed by RelOid because the WAL records
// carry the relation as a `storage.RelFileNode` triple — the
// classifier already extracts that, and `RelOid` is the
// stable identifier across schema renames.
type CatalogSnapshot struct {
	rels map[uint32]*RelationDef
	// RegOut renders a reg* column value's 4-byte OID as its NAME (PG's text-mode
	// pgoutput serializes a reg* value through its typoutput — regclassout etc. —
	// via OidOutputFunctionCall, proto.c:848; the numeric OID is BINARY mode's
	// typsend). nil (the default; no catalog at hand) keeps the numeric fallback.
	// M0119-0006 (deferral row 1353).
	RegOut func(typeName string, oid uint32) string
}

// BuildCatalogSnapshot captures the current catalog state. The
// returned snapshot is safe to share across goroutines and never
// changes after construction.
//
// regOut is nil (no renderer; numeric reg* fallback) or a real reg* name
// renderer. dbOid optionally scopes the snapshot to a specific database's
// namespace; empty keeps the DefaultDBOid (DB-1) scoping — byte-identical to
// today's default path. M0119-0006 (deferral row 1354 claim 2): a logical
// slot on a non-default database must freeze that database's schema, or
// PgOutput skips every change (snap.Lookup misses before any value renders).
func BuildCatalogSnapshot(c *catalog.InMemory, regOut func(string, uint32) string, dbOid ...uint32) *CatalogSnapshot {
	if c == nil {
		return &CatalogSnapshot{rels: map[uint32]*RelationDef{}}
	}
	tables := c.AllTables(dbOid...)
	out := &CatalogSnapshot{rels: make(map[uint32]*RelationDef, len(tables))}
	out.RegOut = regOut
	for _, t := range tables {
		def := &RelationDef{
			Schema:  t.Schema,
			Name:    t.Name,
			OID:     t.OID,
			Columns: make([]ColumnDef, len(t.Columns)),
		}
		for i, col := range t.Columns {
			def.Columns[i] = ColumnDef{
				Name:    col.Name,
				Type:    col.Type,
				NotNull: col.NotNull,
				Ordinal: col.Ordinal,
			}
		}
		out.rels[t.OID] = def
	}
	return out
}

// Lookup returns the relation definition for the given RelFileNode,
// or nil if the relation didn't exist when the snapshot was built.
// A logical decoder that sees a HeapInsert against an unknown
// relation should skip it — the relation was created after the
// slot started and is out of the snapshot's scope. Once schema-
// change handling lands the lookup will instead fail loudly.
func (s *CatalogSnapshot) Lookup(rel storage.RelFileNode) (*RelationDef, bool) {
	if s == nil {
		return nil, false
	}
	def, ok := s.rels[rel.RelOid]
	return def, ok
}

// Len reports the number of tables captured. Used by tests and
// observability — len 0 means the slot was started against a
// schemaless cluster.
func (s *CatalogSnapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.rels)
}

// SlotSnapshot bundles the two frozen views a logical-decoding
// plugin needs at slot start: schema (`Catalog`) plus xact
// visibility (`MVCC`). The `MVCC` field can be the zero value
// when a test doesn't care about visibility — the slot decoder
// loop captures a real snapshot at construction time.
type SlotSnapshot struct {
	Catalog *CatalogSnapshot
	MVCC    transam.Snapshot
}
