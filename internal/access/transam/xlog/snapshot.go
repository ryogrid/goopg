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
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/nodes"
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
	// ReplIdent is pg_class.relreplident ('d', 'f', 'i', 'n'), which the
	// Relation message carries (logicalrep_write_rel).
	ReplIdent byte
}

// ColumnDef mirrors `catalog.Column` but lives in the snapshot
// (no Stats). Future columns (replica identity flag, attoptions)
// land here.
type ColumnDef struct {
	Name    string
	Type    catalog.Type
	NotNull bool
	Ordinal int
	// Typmod is pg_attribute.atttypmod (-1 for none), which the Relation
	// message carries per column (logicalrep_write_attrs).
	Typmod int32
	// ReplIdent marks a column of the table's replica identity — the
	// Relation message's LOGICALREP_IS_REPLICA_IDENTITY flag
	// (identityColumns).
	ReplIdent bool
}

// CatalogSnapshot is the slot-creation-time view of all user
// tables. Lookup is keyed by RelOid because the WAL records
// carry the relation as a `storage.RelFileNode` triple — the
// classifier already extracts that, and `RelOid` is the
// stable identifier across schema renames.
type CatalogSnapshot struct {
	// mu guards rels: Current replaces a relation's definition when the
	// live catalog's shape moved on (M0149-0042b).
	mu   sync.RWMutex
	rels map[uint32]*RelationDef
	// live / dbOid are the catalog the snapshot was built from, which
	// Current consults for schema changes made after the build. nil
	// live (tests, no catalog) keeps the snapshot frozen.
	live  *catalog.InMemory
	dbOid []uint32
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
	out := &CatalogSnapshot{rels: make(map[uint32]*RelationDef, len(tables)), live: c, dbOid: dbOid}
	out.RegOut = regOut
	for _, t := range tables {
		out.rels[t.OID] = relationDefFor(c, t, dbOid...)
	}
	return out
}

// relationDefFor builds t's RelationDef: the columns in order with their
// types and typmods, and the replica identity flags.
func relationDefFor(c *catalog.InMemory, t *catalog.Table, dbOid ...uint32) *RelationDef {
	def := &RelationDef{
		Schema:    t.Schema,
		Name:      t.Name,
		OID:       t.OID,
		Columns:   make([]ColumnDef, len(t.Columns)),
		ReplIdent: catalog.ReplIdentOrDefault(t.ReplicaIdentity)[0],
	}
	ident := identityColumns(c, t, def.ReplIdent, dbOid...)
	for i, col := range t.Columns {
		def.Columns[i] = ColumnDef{
			Name:      col.Name,
			Type:      col.Type,
			NotNull:   col.NotNull,
			Ordinal:   col.Ordinal,
			Typmod:    nodes.ColumnTypmod(col.Type.Name, col.Type.Args),
			ReplIdent: ident == nil || ident[col.Name],
		}
	}
	return def
}

// identityColumns is the column set logicalrep_write_attrs flags as replica
// identity (RelationGetIdentityKeyBitmap): every column under FULL (nil
// means all), the primary key's columns under DEFAULT, the
// indisreplident index's columns under USING INDEX, and none under NOTHING
// or when the index the mode names does not exist.
func identityColumns(c *catalog.InMemory, t *catalog.Table, replIdent byte, dbOid ...uint32) map[string]bool {
	if replIdent == 'f' {
		return nil
	}
	out := map[string]bool{}
	if replIdent != 'd' && replIdent != 'i' {
		return out
	}
	for _, idx := range c.IndexesOnTable(t, dbOid...) {
		// rd_replidindex (RelationGetIndexList) never names a deferrable
		// index. Sibling of executor.hasReplicaIdentity.
		if idx.IsImmediate() && ((replIdent == 'd' && idx.Primary) || (replIdent == 'i' && idx.IsReplicaIdentity)) {
			for _, col := range idx.Columns {
				out[col] = true
			}
			break
		}
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
	s.mu.RLock()
	def, ok := s.rels[rel.RelOid]
	s.mu.RUnlock()
	return def, ok
}

// Current is Lookup plus pgoutput's relation-invalidation path
// (rel_sync_cache_relation_cb clears schema_sent, so the next change
// re-sends the Relation message): when the live catalog's definition of a
// relation in the snapshot no longer matches the captured one — a column
// added, dropped, renamed or retyped, the table renamed, the replica
// identity changed — the snapshot takes the live definition and changed
// reports it, so the caller re-sends the Relation message. A relation
// created after the build is resolved from the live catalog too (a FOR
// ALL TABLES publication publishes it, and the walsender used to skip its
// changes until the subscriber reconnected).
//
// PG decodes each change against the catalog as of that change (a
// historic snapshot); goopg uses the catalog as of decoding, so a change
// decoded after a later DDL already carries the new shape (ledgered).
// M0149-0042b.
func (s *CatalogSnapshot) Current(rel storage.RelFileNode) (def *RelationDef, changed, ok bool) {
	def, ok = s.Lookup(rel)
	// System catalogs are never published (relationIsPublishable), so
	// skip the live lookup for them: an ALTER TABLE writes several
	// catalog rows per statement.
	if s.live == nil || rel.RelOid < catalog.FirstNormalObjectId {
		return def, false, ok
	}
	t, found := s.live.LookupTableByOID(rel.RelOid, s.dbOid...)
	if !found {
		return def, false, ok
	}
	if ok && relationDefMatches(def, t) {
		return def, false, true
	}
	fresh := relationDefFor(s.live, t, s.dbOid...)
	s.mu.Lock()
	s.rels[rel.RelOid] = fresh
	s.mu.Unlock()
	return fresh, true, true
}

// relationDefMatches is the cheap per-change check that def still
// describes t: the names, the replica identity and every column's name,
// type, array-ness and NOT NULL. A typmod-only change (varchar(10) →
// varchar(20)) is not detected.
func relationDefMatches(def *RelationDef, t *catalog.Table) bool {
	if def.Name != t.Name || def.Schema != t.Schema || len(def.Columns) != len(t.Columns) ||
		def.ReplIdent != catalog.ReplIdentOrDefault(t.ReplicaIdentity)[0] {
		return false
	}
	for i, col := range t.Columns {
		dc := def.Columns[i]
		if dc.Name != col.Name || dc.NotNull != col.NotNull || dc.Type.IsArray != col.Type.IsArray ||
			!strings.EqualFold(dc.Type.Name, col.Type.Name) {
			return false
		}
	}
	return true
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
