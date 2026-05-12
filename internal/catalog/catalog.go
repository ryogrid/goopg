// Package catalog is goopg's in-memory schema catalog.
//
// Scope and growth path are documented in
// docs/design/0011-planner.md. v0 keeps tables in a Go map; the
// system-catalog persistence (`pg_class`, `pg_attribute`) lands in
// a follow-up alongside the on-disk catalog work.
package catalog

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// Errors returned by database registration helpers (M0054-0001).
var (
	ErrDatabaseExists   = errors.New("database already exists")
	ErrDatabaseNotFound = errors.New("database does not exist")
)

// Type is the textual type tag plus an optional typmod argument list.
// v0 keeps types as strings so the planner doesn't need a real type
// system; the executor casts based on Type.Name until the type system
// lands.
type Type struct {
	Name string
	Args []int64
}

// Column is one column of a table.
type Column struct {
	Name    string
	Type    Type
	NotNull bool
	Ordinal int // 0-based heap-tuple position
	// GeneratedExpr holds the raw SQL expression for a GENERATED ALWAYS AS … STORED
	// column. Empty for ordinary columns. M0096-0008.
	GeneratedExpr string
	// GeneratedAlways is true when the column uses GENERATED ALWAYS AS semantics.
	GeneratedAlways bool
}

// Table is one relation in the catalog.
type Table struct {
	Schema  string
	Name    string
	Columns []Column
	OID     uint32

	// Virtual marks tables that don't live on the heap. The planner
	// short-circuits SeqScan into a materialised Values node by
	// calling VirtualRows() at plan time. v0 uses this for the
	// minimal pg_catalog views (pg_class, etc.) so external tools
	// like pgbench that probe for table metadata get a useful answer
	// without us bootstrapping a full system catalog.
	Virtual     bool
	VirtualRows func() [][]string

	// View, when non-nil, marks this table as a SQL view. The
	// stored value is the parser AST of the view's defining
	// SELECT; planScanRangeVar substitutes the planned inner
	// node for any reference to this name. ViewColumnAliases is
	// the optional explicit column-name list from
	// `CREATE VIEW name (col_list) AS …`. Required for
	// HammerDB TPC-H Q15. v0 doesn't enforce read-only —
	// INSERT/UPDATE/DELETE against a view will surface as a
	// planner error because the substituted plan isn't a heap
	// scan.
	View               *parser.SelectStmt
	ViewColumnAliases  []string

	// Stats holds the most recent ANALYZE output for this
	// table. nil before ANALYZE has run; the planner treats nil
	// as "no statistics yet" and falls back to the legacy
	// rules-only join order. Mirrors upstream's pg_class
	// reltuples / relpages plus per-column pg_statistic data.
	Stats *TableStats

	// SmallDimension flags a table whose row count is known to
	// be ≤ a tiny constant — the canonical TPC-H examples are
	// `region` (5 rows) and `nation` (25 rows). The planner uses
	// the flag as a cardinality fallback when ANALYZE-derived
	// stats are absent: a hash join with a SmallDimension side
	// pins the small side as the build side regardless of the
	// other side's estimated rows. See M0054-0010 / design doc
	// `docs/design/0054-0005-hash-join-small-side-build.md`.
	SmallDimension bool

	// RelFrozenXID is the minimum XID still present in the heap as an
	// unfrozen xmin. VACUUM FREEZE advances this toward the current XID.
	// When currentXID − RelFrozenXID exceeds autovacuum_freeze_max_age,
	// autovacuum triggers an anti-wraparound vacuum. Zero means no freeze
	// pass has run yet on this table. Mirrors pg_class.relfrozenxid.
	RelFrozenXID storage.TransactionID

	// CheckConstraints holds the raw SQL expressions for table-level and
	// column-level CHECK constraints. M0097-0014.
	CheckConstraints []string

	// IsMatView marks this table as a materialized view. The underlying
	// SELECT query is stored in View; data is materialized in the heap
	// (unlike regular views). M0097-0013.
	IsMatView bool
	// IsPopulated tracks whether REFRESH MATERIALIZED VIEW has been run
	// (false for WITH NO DATA, true after first REFRESH). M0097-0013.
	IsPopulated bool

	// ForeignKeys holds FK constraints declared on this table (inline
	// REFERENCES or ALTER TABLE ADD FOREIGN KEY). M0096-0011.
	ForeignKeys []ForeignKey

	// Triggers holds row- and statement-level triggers defined on this
	// table via CREATE TRIGGER. M0096-0012.
	Triggers []Trigger

	// ── Partition support (M0096-0007) ────────────────────────────────────
	//
	// PartitionKey holds the column names when this is a partitioned table
	// (has a PARTITION BY clause). nil for regular tables.
	PartitionKey []string
	// PartitionMethod is "LIST", "RANGE", or "HASH" when PartitionKey != nil.
	PartitionMethod string
	// PartitionParentOID is the OID of the parent partitioned table if this
	// table is a partition child. Zero for root / non-partition tables.
	PartitionParentOID uint32
	// PartitionBounds holds the partition range/list bounds as strings for
	// routing and display. For LIST: InValues strings; for RANGE: one
	// PartitionBound with From/To strings.
	PartitionBounds []PartitionBound
}

// TriggerTiming mirrors parser.TriggerTiming to avoid importing the
// parser package in contexts that only need the catalog. M0096-0012.
type TriggerTiming int

const (
	TriggerBefore    TriggerTiming = 1
	TriggerAfter     TriggerTiming = 2
	TriggerInsteadOf TriggerTiming = 3
)

// Trigger describes one row-level or statement-level trigger on a table.
// M0096-0012.
type Trigger struct {
	Name       string
	TableOID   uint32
	Timing     TriggerTiming
	Events     []string // "insert", "update", "delete"
	ForEachRow bool
	FuncName   string // function/procedure name (unschemed)
	FuncSchema string
}

// ForeignKey describes one referential integrity constraint stored on a
// child table. M0096-0011.
type ForeignKey struct {
	Columns           []string // columns in THIS table
	RefTable          string   // referenced table name (unschemed)
	RefColumns        []string // referenced columns (empty = use parent PK)
	OnDelete          parser.FKAction
	OnUpdate          parser.FKAction
	Deferrable        bool
	InitiallyDeferred bool
}

// PartitionBound describes the bounds for a single partition child.
// For LIST partitioning, InValues contains the literal string values.
// For RANGE partitioning, From and To contain the bound strings ("MINVALUE", "MAXVALUE", or a literal).
// For HASH partitioning, Modulus and Remainder specify the hash bucket. M0096-0007; HASH M0097-0015.
type PartitionBound struct {
	InValues  []string // LIST: values in this partition
	From      string   // RANGE: lower bound
	To        string   // RANGE: upper bound
	Modulus   int64    // HASH: modulus
	Remainder int64    // HASH: remainder (partition index)
	IsHash    bool     // true for HASH partitions
}

// TableStats captures the pg_class-shaped table-level stats
// plus per-column NDistinct, MCV lists, and equi-depth
// histograms. See
// docs/design/0006-0001-sampling-and-mcv-histograms.md.
type TableStats struct {
	RowCount int64         `json:"row_count,omitempty"`
	Pages    int           `json:"pages,omitempty"`
	AvgWidth float64       `json:"avg_width,omitempty"`
	Columns  []ColumnStats `json:"columns,omitempty"`
}

// ColumnStats is the per-column pg_statistic-shaped subset v0
// collects. NDistinct mirrors upstream's stadistinct: the
// number of distinct non-NULL values seen during ANALYZE.
// NullFrac is the fraction of NULL rows.
//
// MCV holds the most-common-values list (upstream's
// STATISTIC_KIND_MCV slot), sorted by Frequency desc. Histogram
// holds equi-depth bucket boundaries over the non-MCV portion
// (upstream's STATISTIC_KIND_HISTOGRAM slot); len(Histogram) is
// bucketCount+1 when populated, zero when the column type has
// no total order or fewer than two non-MCV values were sampled.
//
// MCV.Value and Histogram entries are the canonical
// `Datum.Format()` rendering of the stored value. See
// docs/design/0006-0001-sampling-and-mcv-histograms.md for the
// rationale (catalog must not depend on the executor's Datum
// type) and the planner-side parsing contract.
type ColumnStats struct {
	NDistinct int64      `json:"ndistinct,omitempty"`
	NullFrac  float64    `json:"null_frac,omitempty"`
	MCV       []MCVEntry `json:"mcv,omitempty"`
	Histogram []string   `json:"histogram,omitempty"`
}

// MCVEntry is one entry in a per-column MCV list. Frequency is
// the sample frequency (0..1). Mirrors a single (stavalues,
// stanumbers) pair in upstream's pg_statistic MCV slot.
type MCVEntry struct {
	Value     string  `json:"value"`
	Frequency float64 `json:"frequency"`
}

// Index is one index relation in the catalog.
type Index struct {
	Schema  string
	Name    string
	Table   *Table
	Columns []string
	Unique  bool
	Method  string
	Primary bool
	OID     uint32
}

// QualifiedName renders the table's name in the canonical
// `schema.name` form when schema-qualified, otherwise just `name`.
func (t *Table) QualifiedName() string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// QualifiedName renders the index name in canonical form.
func (i *Index) QualifiedName() string {
	if i.Schema == "" {
		return i.Name
	}
	return i.Schema + "." + i.Name
}

// Catalog is the lookup interface the planner uses.
type Catalog interface {
	LookupTable(name parser.ObjectName) (*Table, bool)
	LookupColumn(table *Table, name string) (*Column, bool)
	LookupIndex(name parser.ObjectName) (*Index, bool)
	CreateTable(name parser.ObjectName, cols []Column) (*Table, error)
	CreateIndex(name parser.ObjectName, table *Table, cols []string, unique bool, method string, primary bool) (*Index, error)
	CreateView(name parser.ObjectName, cols []Column, aliases []string, query *parser.SelectStmt, orReplace bool) (*Table, error)
	DropView(name parser.ObjectName, ifExists bool) error
	SetTableStats(table *Table, stats *TableStats)
	AddColumn(table *Table, col Column) (*Column, error)
	DropTable(name parser.ObjectName) error
	DropIndex(name parser.ObjectName) error
	IndexesOnTable(table *Table) []*Index
	HasPrimaryKey(table *Table) bool
	RelFileNode(table *Table) storage.RelFileNode
	IndexRelFileNode(index *Index) storage.RelFileNode
	// Routines returns the user-defined-routine registry. M0015
	// Stage A step 3: the executor's CREATE FUNCTION / DROP
	// FUNCTION operators call into this to mutate `pg_proc`-shaped
	// state. Implementations may return a process-local registry
	// (current InMemory behaviour) or a future on-disk-backed one.
	Routines() *Routines
}

// InMemory is the v0 implementation: a sync.RWMutex-guarded map.
//
// OIDs are assigned sequentially starting at FirstUserOID. The DBOid
// field on the produced RelFileNode is fixed at DefaultDBOid for v0
// — the multi-database layer arrives with milestone 7.
type InMemory struct {
	mu      sync.RWMutex
	tables  map[string]*Table
	indexes map[string]*Index
	byTable map[uint32]map[string]*Index
	nextOID uint32
	dbOid   uint32
	// routines holds user-defined routines (M0015 Stage A step 2).
	// Separate registry — not part of the table/index map space —
	// so existing CRUD on those types stays untouched. Accessed
	// via `(*InMemory).Routines()`.
	routines *Routines
	// databases is the set of database names the cluster knows
	// about (M0054-0001). Populated by `CreateDatabase` and
	// drained by `DropDatabase`. At startup the catalog seeds
	// `"postgres"` (the conventional bootstrap DB); the recovery
	// driver in `internal/initdb` re-applies WAL-logged
	// CREATE/DROP DATABASE events on top. v0 still routes every
	// relation through DefaultDBOid — the registry exists so
	// `pg_database` returns truthful rows and so connections to
	// recovered databases succeed after a crash, NOT for
	// per-database storage isolation (that lands later).
	databases map[string]bool

	// partitionChildren maps parent table OID → slice of child OIDs
	// for partitioned-table support (M0096-0007).
	partitionChildren map[uint32][]uint32

	// inheritanceChildren maps parent table OID → slice of child OIDs
	// for table inheritance support (M0096-0009).
	inheritanceChildren map[uint32][]uint32

	// enumTypes holds user-defined enum types. M0097-0017.
	enumTypes map[string]*EnumType
	// domains holds user-defined domain types. M0097-0017.
	domains map[string]*Domain
}

// EnumType holds one user-defined enum type. M0097-0017.
type EnumType struct {
	Name   string
	OID    uint32
	Values []string // ordered labels; position = sortorder (0-based)
}

// Domain holds one user-defined domain type. M0097-0017.
type Domain struct {
	Name    string
	OID     uint32
	Base    Type // resolved base type
	NotNull bool
}

// Fixed OIDs for the three core system catalog heap tables.
// Values match upstream's pg_class.h / pg_attribute.h / pg_type.h
// so tools that query OID columns by numeric value (e.g. ODBC metadata
// probes) see the expected numbers.
const (
	TypeRelationId      uint32 = 1247 // pg_type
	AttributeRelationId uint32 = 1249 // pg_attribute
	RelationRelationId  uint32 = 1259 // pg_class
)

// FirstUserOID is the first OID handed out for user-created tables.
// 16384 is upstream's `FirstNormalObjectId` — anything below is
// reserved for system catalogs.
const FirstUserOID uint32 = 16384

// IsSystemRelation reports whether oid belongs to the reserved system-
// catalog OID range (anything below FirstUserOID). Used by the executor
// and storage bootstrap to gate behaviour that only makes sense for
// system relations (e.g. skipping WAL for catalog seeding writes).
func IsSystemRelation(oid uint32) bool {
	return oid < FirstUserOID
}

// DefaultDBOid is the v0 default database OID. Real multi-database
// support (CREATE DATABASE) lives in milestone 7; until then every
// catalog entry lives in this database.
const DefaultDBOid uint32 = 1

// NewInMemory returns a catalog seeded with the v0 pg_catalog
// virtual views.
func NewInMemory() *InMemory {
	c := &InMemory{
		tables:              make(map[string]*Table),
		indexes:             make(map[string]*Index),
		byTable:             make(map[uint32]map[string]*Index),
		nextOID:             FirstUserOID,
		dbOid:               DefaultDBOid,
		routines:            NewRoutines(),
		databases:           map[string]bool{"postgres": true},
		partitionChildren:   make(map[uint32][]uint32),
		inheritanceChildren: make(map[uint32][]uint32),
		enumTypes:           make(map[string]*EnumType),
		domains:             make(map[string]*Domain),
	}
	c.registerSystemTables()
	return c
}

// RegisterInheritanceChild registers childOID as a child of parentOID for
// table inheritance. Called when CREATE TABLE c INHERITS (p) executes.
// M0096-0009.
func (c *InMemory) RegisterInheritanceChild(parentOID, childOID uint32) {
	c.mu.Lock()
	c.inheritanceChildren[parentOID] = append(c.inheritanceChildren[parentOID], childOID)
	c.mu.Unlock()
}

// InheritanceChildren returns the direct inheritance children of parentOID.
// Returns nil if the table has no inheritance children. M0096-0009.
func (c *InMemory) InheritanceChildren(parentOID uint32) []*Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	children := c.inheritanceChildren[parentOID]
	if len(children) == 0 {
		return nil
	}
	out := make([]*Table, 0, len(children))
	for _, oid := range children {
		for _, t := range c.tables {
			if t.OID == oid {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// PartitionChildren returns the OIDs of partition children for a partitioned table.
// Returns nil if the table has no partitions registered.  M0096-0007.
func (c *InMemory) PartitionChildren(parentOID uint32) []*Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	children := c.partitionChildren[parentOID]
	if len(children) == 0 {
		return nil
	}
	out := make([]*Table, 0, len(children))
	for _, oid := range children {
		for _, t := range c.tables {
			if t.OID == oid {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// RegisterPartitionChild registers tbl (OID childOID) as a partition of parentOID.
// M0096-0007.
func (c *InMemory) RegisterPartitionChild(parentOID, childOID uint32) {
	c.mu.Lock()
	c.partitionChildren[parentOID] = append(c.partitionChildren[parentOID], childOID)
	c.mu.Unlock()
}

// FindPartitionForValue finds the partition child that matches a given key value
// string for a LIST-partitioned table. Returns nil if no partition matches.
// M0096-0007.
func (c *InMemory) FindPartitionForValue(parentOID uint32, keyValue string) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, childOID := range c.partitionChildren[parentOID] {
		for _, t := range c.tables {
			if t.OID != childOID {
				continue
			}
			for _, pb := range t.PartitionBounds {
				for _, v := range pb.InValues {
					if v == keyValue {
						return t
					}
				}
			}
		}
	}
	return nil
}

// FindRangePartitionForValue finds the RANGE partition child that contains keyValue.
// M0096-0007.
func (c *InMemory) FindRangePartitionForValue(parentOID uint32, keyValue int64) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, childOID := range c.partitionChildren[parentOID] {
		for _, t := range c.tables {
			if t.OID != childOID {
				continue
			}
			for _, pb := range t.PartitionBounds {
				if pb.From == "" && pb.To == "" {
					continue
				}
				var from, to int64 = -1<<62, 1<<62
				if pb.From != "" && pb.From != "MINVALUE" {
					fmt.Sscanf(pb.From, "%d", &from)
				}
				if pb.To != "" && pb.To != "MAXVALUE" {
					fmt.Sscanf(pb.To, "%d", &to)
				}
				if keyValue >= from && keyValue < to {
					return t
				}
			}
		}
	}
	return nil
}

// FindHashPartitionForValue finds the HASH partition child that owns the given
// key value's hash bucket. Uses a simple FNV-inspired hash. M0097-0015.
func (c *InMemory) FindHashPartitionForValue(parentOID uint32, keyValue string) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Compute a simple hash of the key value.
	h := uint64(14695981039346656037)
	for _, b := range []byte(keyValue) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	for _, childOID := range c.partitionChildren[parentOID] {
		for _, t := range c.tables {
			if t.OID != childOID {
				continue
			}
			for _, pb := range t.PartitionBounds {
				if pb.IsHash && pb.Modulus > 0 {
					if int64(h%uint64(pb.Modulus)) == pb.Remainder {
						return t
					}
				}
			}
		}
	}
	return nil
}

// HasDatabase reports whether the given database name is registered
// in the catalog. Used by the connection startup path to validate
// the requested database parameter.
func (c *InMemory) HasDatabase(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.databases[name]
}

// CreateDatabase registers a new database name (M0054-0001). Returns
// `ErrDatabaseExists` when the name is already known. Callers are
// expected to write a `wal.RecordKindCreateDatabase` record on the
// success path so the registration survives a crash. Bootstrap and
// recovery paths use `RegisterDatabaseDuringRecovery` instead, which
// is idempotent.
func (c *InMemory) CreateDatabase(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.databases[name] {
		return ErrDatabaseExists
	}
	c.databases[name] = true
	return nil
}

// DropDatabase removes a database from the catalog (M0054-0001).
// Returns `ErrDatabaseNotFound` when the name is not registered.
func (c *InMemory) DropDatabase(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.databases[name] {
		return ErrDatabaseNotFound
	}
	delete(c.databases, name)
	return nil
}

// RegisterDatabaseDuringRecovery is the idempotent version of
// CreateDatabase used by the WAL-replay driver. Re-applying a record
// that has already taken effect (e.g. because a SaveCatalog snapshot
// captured it) is a no-op rather than an error.
func (c *InMemory) RegisterDatabaseDuringRecovery(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.databases[name] = true
}

// UnregisterDatabaseDuringRecovery is the idempotent counterpart to
// `RegisterDatabaseDuringRecovery` — used for replaying
// `RecordKindDropDatabase`.
func (c *InMemory) UnregisterDatabaseDuringRecovery(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.databases, name)
}

// RegisterIndexDuringRecovery is the idempotent version of
// CreateIndex used by the WAL-replay driver. Differs from
// CreateIndex in three ways: (a) the OID comes from the WAL
// record rather than `nextOID++`, so the recovered catalog
// entry maps to the same on-disk relfile that physical replay
// just restored; (b) re-applying a record whose Index already
// exists in the catalog (e.g. because a JSON snapshot captured
// it) is a no-op rather than an error; (c) `nextOID` is
// advanced past the recovered OID so subsequent allocations
// don't collide.
//
// Used by `internal/initdb.replayIndexDDLRecords` to restore
// the in-memory index registry after a crash that bypassed
// SaveCatalog. (M0079-0001.)
func (c *InMemory) RegisterIndexDuringRecovery(
	schema string,
	name string,
	tableOID uint32,
	cols []string,
	unique bool,
	method string,
	primary bool,
	oid uint32,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tbl, ok := c.tableByOID(tableOID)
	if !ok {
		// Owning table not yet recovered — caller must run
		// `loadUserTablesFromHeap` (or equivalent) before
		// replaying CREATE INDEX records.
		return
	}
	k := key(parser.ObjectName{Schema: schema, Name: name})
	if existing, dup := c.indexes[k]; dup {
		// JSON snapshot or earlier WAL pass already registered
		// this index. Idempotent no-op.
		_ = existing
		c.advanceNextOIDLocked(oid)
		return
	}
	idx := &Index{
		Schema:  schema,
		Name:    name,
		Table:   tbl,
		Columns: append([]string(nil), cols...),
		Unique:  unique,
		Method:  strings.ToLower(method),
		Primary: primary,
		OID:     oid,
	}
	c.indexes[k] = idx
	if c.byTable[tbl.OID] == nil {
		c.byTable[tbl.OID] = map[string]*Index{}
	}
	c.byTable[tbl.OID][k] = idx
	c.advanceNextOIDLocked(oid)
}

// UnregisterIndexDuringRecovery is the idempotent counterpart
// to `RegisterIndexDuringRecovery` — used for replaying
// `RecordKindDropIndex`. (M0079-0001.)
func (c *InMemory) UnregisterIndexDuringRecovery(schema, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(parser.ObjectName{Schema: schema, Name: name})
	idx, ok := c.indexes[k]
	if !ok {
		return
	}
	delete(c.indexes, k)
	if idx.Table != nil {
		if perTable := c.byTable[idx.Table.OID]; perTable != nil {
			delete(perTable, k)
			if len(perTable) == 0 {
				delete(c.byTable, idx.Table.OID)
			}
		}
	}
}

// tableByOID returns the *Table whose OID matches; caller must
// hold c.mu (read or write). Used by the recovery hooks to map
// a WAL-encoded table OID back to the in-memory entry without
// re-walking c.tables on every call site.
func (c *InMemory) tableByOID(oid uint32) (*Table, bool) {
	for _, t := range c.tables {
		if t.OID == oid {
			return t, true
		}
	}
	return nil, false
}

// advanceNextOIDLocked nudges nextOID past `oid` so subsequent
// allocations don't collide with the recovered identifier.
// Caller must hold c.mu.
func (c *InMemory) advanceNextOIDLocked(oid uint32) {
	if oid >= c.nextOID {
		c.nextOID = oid + 1
	}
}

// ListDatabases returns the registered database names in
// deterministic (lexicographic) order. Backs the `pg_database`
// virtual table.
func (c *InMemory) ListDatabases() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.databases))
	for n := range c.databases {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Routines returns the user-defined routine registry. Stage A
// step 2: catalog-side bookkeeping for CREATE FUNCTION / DROP
// FUNCTION; the analyzer / executor wiring lands in subsequent
// slices. Pointer is stable for the catalog's lifetime.
func (c *InMemory) Routines() *Routines { return c.routines }

// registerSystemTables installs the minimal pg_catalog v0 needs:
// pg_class with one row per user table. The OID column is text-typed
// because regclass casts are no-ops in v0 — pgbench's
// `oid=$1::pg_catalog.regclass` ends up comparing the bound text
// parameter (the table name) against pg_class.oid, so storing the
// relname there makes the equality match.
func (c *InMemory) registerSystemTables() {
	pgClass := &Table{
		Schema: "pg_catalog",
		Name:   "pg_class",
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "relname", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "relkind", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "relnamespace", Type: Type{Name: "text"}, Ordinal: 3},
			// Additional columns required by vacuumdb catalog query (M0095-0004).
			{Name: "relpersistence", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "reltoastrelid", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "relpages", Type: Type{Name: "text"}, Ordinal: 6},
			// relispopulated: true for tables/views, reflects IsPopulated for matviews.
			// M0097-0013.
			{Name: "relispopulated", Type: Type{Name: "bool"}, Ordinal: 7},
		},
		OID:     1259, // upstream's RelationRelationId
		Virtual: true,
	}
	pgClass.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		keys := make([]string, 0, len(c.tables))
		for k := range c.tables {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([][]string, 0, len(c.tables))
		for _, k := range keys {
			t := c.tables[k]
			if t.Virtual {
				// Don't list ourselves in our own view — keeps the
				// regclass probe shape predictable for pgbench.
				continue
			}
			relkind := "r"
			if t.View != nil && !t.IsMatView {
				relkind = "v"
			} else if t.IsMatView {
				relkind = "m"
			}
			populated := "t"
			if t.IsMatView && !t.IsPopulated {
				populated = "f"
			}
			out = append(out, []string{
				t.Name,
				t.Name,
				relkind,
				"2200",    // relnamespace: OID of public namespace (matches pg_namespace.oid)
				"p",       // relpersistence: permanent
				"0",       // reltoastrelid: no TOAST table
				"0",       // relpages: estimated page count (0 = unknown)
				populated, // relispopulated
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_class"] = pgClass

	// pg_namespace — required by vacuumdb's table-discovery catalog query
	// (M0095-0004). vacuumdb sends:
	//   SELECT c.relname, ns.nspname
	//   FROM pg_class c JOIN pg_namespace ns ON c.relnamespace = ns.oid ...
	// Returns the standard system namespaces. The oid values match upstream
	// PostgreSQL's well-known OIDs so client tools can join correctly.
	pgNamespace := &Table{
		Schema: "pg_catalog",
		Name:   "pg_namespace",
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "nspname", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "nspowner", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "nspacl", Type: Type{Name: "text"}, Ordinal: 3},
		},
		OID:     2615, // upstream's NamespaceRelationId
		Virtual: true,
	}
	pgNamespace.VirtualRows = func() [][]string {
		return [][]string{
			{"11", "pg_catalog", "10", ""},
			{"2200", "public", "10", ""},
			{"99", "information_schema", "10", ""},
		}
	}
	c.tables["pg_catalog.pg_namespace"] = pgNamespace

	// pg_indexes view. HammerDB's checkschema step queries
	// `select tablename, indexname from pg_indexes where
	// tablename = '$table'` to verify each TPC-H table has at
	// least one index after CreateIndexes runs. Mirrors the
	// upstream view's first three columns; tablespace and
	// indexdef are populated as empty / placeholder strings
	// since v0 doesn't track them.
	pgIndexes := &Table{
		Schema: "pg_catalog",
		Name:   "pg_indexes",
		Columns: []Column{
			{Name: "schemaname", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "tablename", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "indexname", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "tablespace", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "indexdef", Type: Type{Name: "text"}, Ordinal: 4},
		},
		OID:     2604, // upstream's pg_indexes OID
		Virtual: true,
	}
	pgIndexes.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		// Sort for deterministic output across calls.
		tableKeys := make([]string, 0, len(c.tables))
		for k, t := range c.tables {
			if t.Virtual {
				continue
			}
			tableKeys = append(tableKeys, k)
		}
		sort.Strings(tableKeys)
		var out [][]string
		for _, tk := range tableKeys {
			t := c.tables[tk]
			idxs := c.byTable[t.OID]
			idxKeys := make([]string, 0, len(idxs))
			for ik := range idxs {
				idxKeys = append(idxKeys, ik)
			}
			sort.Strings(idxKeys)
			for _, ik := range idxKeys {
				idx := idxs[ik]
				schema := idx.Schema
				if schema == "" {
					schema = "public"
				}
				out = append(out, []string{
					schema,
					t.Name,
					idx.Name,
					"", // tablespace
					"",
				})
			}
		}
		return out
	}
	c.tables["pg_catalog.pg_indexes"] = pgIndexes

	// pg_database — HammerDB probes
	// `SELECT 1 FROM pg_database WHERE datname = '<db>'` to
	// decide whether to issue CREATE DATABASE. v0 doesn't track
	// multiple databases (single dbOid), so the seeded row is
	// the conventional `postgres` superuser DB. A query for any
	// other name filters to zero rows → HammerDB takes the
	// CREATE-DATABASE branch which the dispatch.go no-op
	// compatibility tag absorbs.
	pgDatabase := &Table{
		Schema: "pg_catalog",
		Name:   "pg_database",
		Columns: []Column{
			{Name: "datname", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "datdba", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "encoding", Type: Type{Name: "text"}, Ordinal: 2},
			// Additional columns for vacuumdb --all (M0095-0004).
			{Name: "datallowconn", Type: Type{Name: "bool"}, Ordinal: 3},
			{Name: "datconnlimit", Type: Type{Name: "int4"}, Ordinal: 4},
		},
		OID:     1262, // upstream's DatabaseRelationId
		Virtual: true,
	}
	pgDatabase.VirtualRows = func() [][]string {
		// M0054-0001: enumerate the live database registry instead
		// of hard-coding a single `postgres` row. CREATE DATABASE
		// adds entries; the recovery driver replays them.
		names := c.ListDatabases()
		out := make([][]string, 0, len(names))
		for _, n := range names {
			out = append(out, []string{
				n,
				"10",   // datdba: OID of owner (10 = postgres superuser)
				"6",    // encoding: 6 = UTF8
				"true", // datallowconn: allow connections
				"0",    // datconnlimit: 0 = default (vacuumdb filters datconnlimit <> -2)
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_database"] = pgDatabase

	// pg_roles — HammerDB probes
	// `SELECT 1 FROM pg_roles WHERE rolname = '<user>'` before
	// CREATE USER. v0's auth layer doesn't expose role state
	// through the catalog, so the seeded row is the conventional
	// `postgres` superuser. Other names filter to zero, and
	// the dispatch.go CREATE USER no-op handles the follow-up.
	pgRoles := &Table{
		Schema: "pg_catalog",
		Name:   "pg_roles",
		Columns: []Column{
			{Name: "rolname", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "rolsuper", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "rolcanlogin", Type: Type{Name: "text"}, Ordinal: 2},
		},
		OID:     1260, // upstream's AuthIdRelationId
		Virtual: true,
	}
	pgRoles.VirtualRows = func() [][]string {
		return [][]string{{"postgres", "t", "t"}}
	}
	c.tables["pg_catalog.pg_roles"] = pgRoles

	// pg_tables — HammerDB probes
	// `SELECT 1 FROM pg_tables WHERE schemaname = 'public'` to
	// decide whether the target DB is empty before
	// CreateTables and `SELECT EXISTS (... WHERE tablename =
	// '<t>')` during checkschema. Walks user (non-virtual)
	// tables in deterministic key order.
	pgTables := &Table{
		Schema: "pg_catalog",
		Name:   "pg_tables",
		Columns: []Column{
			{Name: "schemaname", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "tablename", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "tableowner", Type: Type{Name: "text"}, Ordinal: 2},
		},
		OID:     1259101, // synthetic — upstream's pg_tables is a view, no fixed OID
		Virtual: true,
	}
	pgTables.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		keys := make([]string, 0, len(c.tables))
		for k := range c.tables {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out [][]string
		for _, k := range keys {
			t := c.tables[k]
			if t.Virtual {
				continue
			}
			schema := t.Schema
			if schema == "" {
				schema = "public"
			}
			out = append(out, []string{schema, t.Name, "postgres"})
		}
		return out
	}
	c.tables["pg_catalog.pg_tables"] = pgTables

	// pg_settings — isolation specs use
	// `SELECT setting FROM pg_settings WHERE name = 'default_transaction_isolation'`
	// to detect the effective isolation level. Returns GUC-style rows.
	// M0096-0006: minimal stub returning only the entries isolation specs query.
	pgSettings := &Table{
		Schema: "pg_catalog",
		Name:   "pg_settings",
		Columns: []Column{
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "setting", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "unit", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "category", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "short_desc", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "extra_desc", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "context", Type: Type{Name: "text"}, Ordinal: 6},
			{Name: "vartype", Type: Type{Name: "text"}, Ordinal: 7},
			{Name: "source", Type: Type{Name: "text"}, Ordinal: 8},
			{Name: "min_val", Type: Type{Name: "text"}, Ordinal: 9},
			{Name: "max_val", Type: Type{Name: "text"}, Ordinal: 10},
			{Name: "enumvals", Type: Type{Name: "text"}, Ordinal: 11},
			{Name: "boot_val", Type: Type{Name: "text"}, Ordinal: 12},
			{Name: "reset_val", Type: Type{Name: "text"}, Ordinal: 13},
			{Name: "sourcefile", Type: Type{Name: "text"}, Ordinal: 14},
			{Name: "sourceline", Type: Type{Name: "int4"}, Ordinal: 15},
			{Name: "pending_restart", Type: Type{Name: "bool"}, Ordinal: 16},
		},
		OID:     1259200, // synthetic
		Virtual: true,
	}
	pgSettings.VirtualRows = func() [][]string {
		// Minimal rows for isolation-spec chkiso step.
		return [][]string{
			{"default_transaction_isolation", "read committed", "", "Client Connection Defaults / Statement Behavior",
				"Sets the transaction isolation level of each new transaction.", "",
				"user", "enum", "default", "", "", "{\"serializable\",\"repeatable read\",\"read committed\",\"read uncommitted\"}",
				"read committed", "read committed", "", "", "f"},
			{"enable_seqscan", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of sequential-scan plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
		}
	}
	c.tables["pg_catalog.pg_settings"] = pgSettings

	// pg_locks — advisory_lock.sql queries this to verify lock state.
	// v0 returns empty rows (no lock tracking infrastructure). M0097-0010.
	pgLocks := &Table{
		Schema:  "pg_catalog",
		Name:    "pg_locks",
		Virtual: true,
		Columns: []Column{
			{Name: "locktype", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "database", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "relation", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "page", Type: Type{Name: "int4"}, Ordinal: 3},
			{Name: "tuple", Type: Type{Name: "int2"}, Ordinal: 4},
			{Name: "virtualxid", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "transactionid", Type: Type{Name: "xid"}, Ordinal: 6},
			{Name: "classid", Type: Type{Name: "oid"}, Ordinal: 7},
			{Name: "objid", Type: Type{Name: "oid"}, Ordinal: 8},
			{Name: "objsubid", Type: Type{Name: "int2"}, Ordinal: 9},
			{Name: "virtualtransaction", Type: Type{Name: "text"}, Ordinal: 10},
			{Name: "pid", Type: Type{Name: "int4"}, Ordinal: 11},
			{Name: "mode", Type: Type{Name: "text"}, Ordinal: 12},
			{Name: "granted", Type: Type{Name: "bool"}, Ordinal: 13},
			{Name: "fastpath", Type: Type{Name: "bool"}, Ordinal: 14},
			{Name: "waitstart", Type: Type{Name: "timestamptz"}, Ordinal: 15},
		},
	}
	pgLocks.VirtualRows = func() [][]string { return nil } // always empty in v0
	c.tables["pg_catalog.pg_locks"] = pgLocks

	// pg_enum — one row per enum label. M0097-0017.
	pgEnum := &Table{
		Schema: "pg_catalog",
		Name:   "pg_enum",
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "enumtypid", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "enumsortorder", Type: Type{Name: "numeric"}, Ordinal: 2},
			{Name: "enumlabel", Type: Type{Name: "text"}, Ordinal: 3},
		},
		OID:     2417, // upstream's EnumRelationId
		Virtual: true,
	}
	pgEnum.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		var rows [][]string
		names := make([]string, 0, len(c.enumTypes))
		for n := range c.enumTypes {
			names = append(names, n)
		}
		sort.Strings(names)
		oid := 20000
		for _, name := range names {
			et := c.enumTypes[name]
			for i, label := range et.Values {
				rows = append(rows, []string{
					fmt.Sprintf("%d", oid),
					et.Name,
					fmt.Sprintf("%d", i+1),
					label,
				})
				oid++
			}
		}
		return rows
	}
	c.tables["pg_catalog.pg_enum"] = pgEnum

	// pg_type — minimal rows for user-defined types (enums + domains). M0097-0017.
	pgType := &Table{
		Schema: "pg_catalog",
		Name:   "pg_type",
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "typname", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "typnamespace", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "typlen", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "typtype", Type: Type{Name: "text"}, Ordinal: 4},
		},
		OID:     1247, // upstream's TypeRelationId
		Virtual: true,
	}
	pgType.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		var rows [][]string
		for _, et := range c.enumTypes {
			rows = append(rows, []string{
				fmt.Sprintf("%d", et.OID),
				et.Name,
				"2200",
				"-1",
				"e",
			})
		}
		for _, d := range c.domains {
			rows = append(rows, []string{
				fmt.Sprintf("%d", d.OID),
				d.Name,
				"2200",
				"-1",
				"d",
			})
		}
		return rows
	}
	c.tables["pg_catalog.pg_type"] = pgType
}

// TryRegisterUserTable installs a user table recovered from the pg_class/
// pg_attribute heap scan during startup (M0030-0003). Unlike CreateTable,
// it preserves the original OID from the heap row and is idempotent:
// if a table with the same qualified name already exists (e.g. loaded from
// the JSON snapshot), the call is a no-op and returns nil. nextOID is
// advanced past tbl.OID to prevent future allocations from colliding with
// existing heap-stored relations.
func (c *InMemory) TryRegisterUserTable(tbl *Table) error {
	if tbl == nil {
		return fmt.Errorf("TryRegisterUserTable: nil table")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(parser.ObjectName{Schema: tbl.Schema, Name: tbl.Name})
	if _, exists := c.tables[k]; exists {
		return nil // already registered — idempotent
	}
	for i := range tbl.Columns {
		tbl.Columns[i].Ordinal = i
	}
	c.tables[k] = tbl
	if tbl.OID >= c.nextOID {
		c.nextOID = tbl.OID + 1
	}
	return nil
}

// RegisterRealTable installs a heap-backed system catalog table.
// Used at startup to register pg_type and pg_attribute after their
// relfiles are confirmed present on disk. The table must have
// Virtual=false and a pre-assigned OID below FirstUserOID
// (i.e. IsSystemRelation(t.OID) must be true).
//
// If an entry with the same qualified name already exists and has
// the same OID, the call is a no-op (idempotent). This handles
// the case where Restore() loaded the table from a JSON snapshot
// before loadSystemCatalogsIfPresent ran.
//
// System catalog tables are excluded from Snapshot() so they are
// never persisted to JSON — they are always re-registered at
// startup from their heap relfiles.
func (c *InMemory) RegisterRealTable(t *Table) error {
	if t == nil {
		return fmt.Errorf("RegisterRealTable: nil table")
	}
	if t.Virtual {
		return fmt.Errorf("RegisterRealTable: table %q must not be virtual", t.QualifiedName())
	}
	if !IsSystemRelation(t.OID) {
		return fmt.Errorf("RegisterRealTable: OID %d is above FirstUserOID; use CreateTable instead", t.OID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(parser.ObjectName{Schema: t.Schema, Name: t.Name})
	if existing, ok := c.tables[k]; ok {
		if existing.OID == t.OID {
			return nil // already registered — idempotent
		}
		return fmt.Errorf("RegisterRealTable: %q already exists with OID %d (want %d)", k, existing.OID, t.OID)
	}
	for i := range t.Columns {
		t.Columns[i].Ordinal = i
	}
	c.tables[k] = t
	return nil
}

// RegisterVirtualTable installs a virtual table built by an
// out-of-tree caller. Used by the runtime to wire stats views
// (`pg_stat_checkpointer`, etc.) whose row data lives outside
// the catalog. The supplied table must have Virtual=true and a
// non-nil VirtualRows; otherwise an error is returned.
//
// Like the seeded `pg_catalog.pg_class`, virtual tables are NOT
// part of the persisted catalog snapshot — the caller is
// expected to re-register on every startup.
func (c *InMemory) RegisterVirtualTable(t *Table) error {
	if t == nil {
		return fmt.Errorf("RegisterVirtualTable: nil table")
	}
	if !t.Virtual || t.VirtualRows == nil {
		return fmt.Errorf("RegisterVirtualTable: %s is not a virtual table with a row provider", t.QualifiedName())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(parser.ObjectName{Schema: t.Schema, Name: t.Name})
	if _, exists := c.tables[k]; exists {
		return fmt.Errorf("relation %q already exists", k)
	}
	if t.OID == 0 {
		t.OID = c.nextOID
		c.nextOID++
	}
	for i := range t.Columns {
		t.Columns[i].Ordinal = i
	}
	c.tables[k] = t
	return nil
}

// key builds the map key for a parser.ObjectName. Matching follows
// upstream's lower-cased convention: unquoted identifiers were
// already lower-cased by the lexer; quoted identifiers preserve
// case but are still stored under their literal form.
func key(name parser.ObjectName) string {
	if name.Schema == "" {
		return name.Name
	}
	return name.Schema + "." + name.Name
}

// LookupTable returns the table with the given name, or false when
// the name doesn't resolve. Unqualified lookups fall back to the
// `pg_catalog` schema so HammerDB's `SELECT FROM pg_indexes ...`
// and `SELECT FROM pg_class ...` shapes resolve without an
// explicit schema qualifier — mirrors upstream's implicit
// `pg_catalog` entry on the search_path.
func (c *InMemory) LookupTable(name parser.ObjectName) (*Table, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if t, ok := c.tables[key(name)]; ok {
		return t, true
	}
	if name.Schema == "" {
		if t, ok := c.tables[key(parser.ObjectName{Schema: "pg_catalog", Name: name.Name})]; ok {
			return t, true
		}
	}
	return nil, false
}

// LookupColumn returns the column with the given name on a resolved
// table. Comparison is case-insensitive; quoted identifiers are
// matched literally.
func (c *InMemory) LookupColumn(table *Table, name string) (*Column, bool) {
	for i := range table.Columns {
		if strings.EqualFold(table.Columns[i].Name, name) {
			return &table.Columns[i], true
		}
	}
	return nil, false
}

// LookupIndex returns the index with the given name, or false when
// unresolved.
func (c *InMemory) LookupIndex(name parser.ObjectName) (*Index, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	idx, ok := c.indexes[key(name)]
	return idx, ok
}

// CreateTable installs a new table in the catalog. Returns an error
// when a table with the same name already exists.
func (c *InMemory) CreateTable(name parser.ObjectName, cols []Column) (*Table, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(name)
	if _, exists := c.tables[k]; exists {
		return nil, fmt.Errorf("relation %q already exists", k)
	}
	for i := range cols {
		cols[i].Ordinal = i
	}
	t := &Table{
		Schema:  name.Schema,
		Name:    name.Name,
		Columns: append([]Column(nil), cols...),
		OID:     c.nextOID,
	}
	c.nextOID++
	c.tables[k] = t
	return t, nil
}

// CreateIndex installs a new index in the catalog. Returns an error
// when an index with the same name already exists.
func (c *InMemory) CreateIndex(name parser.ObjectName, table *Table, cols []string, unique bool, method string, primary bool) (*Index, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if table == nil {
		return nil, fmt.Errorf("table is nil")
	}
	k := key(name)
	if _, exists := c.indexes[k]; exists {
		return nil, fmt.Errorf("relation %q already exists", k)
	}
	idx := &Index{
		Schema:  name.Schema,
		Name:    name.Name,
		Table:   table,
		Columns: append([]string(nil), cols...),
		Unique:  unique,
		Method:  strings.ToLower(method),
		Primary: primary,
		OID:     c.nextOID,
	}
	c.nextOID++
	c.indexes[k] = idx
	if c.byTable[table.OID] == nil {
		c.byTable[table.OID] = map[string]*Index{}
	}
	c.byTable[table.OID][k] = idx
	return idx, nil
}

// AddColumn appends one column to an existing table definition.
func (c *InMemory) AddColumn(table *Table, col Column) (*Column, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if table == nil {
		return nil, fmt.Errorf("table is nil")
	}
	k := key(parser.ObjectName{Schema: table.Schema, Name: table.Name})
	t, ok := c.tables[k]
	if !ok {
		return nil, fmt.Errorf("relation %q does not exist", table.QualifiedName())
	}
	for i := range t.Columns {
		if strings.EqualFold(t.Columns[i].Name, col.Name) {
			return nil, fmt.Errorf("column %q already exists", col.Name)
		}
	}
	col.Ordinal = len(t.Columns)
	t.Columns = append(t.Columns, col)
	return &t.Columns[len(t.Columns)-1], nil
}

// DropTable removes a table from the catalog. Returns an error when
// the name doesn't resolve.
// CreateView installs a view in the catalog. The view is
// registered as a Virtual Table whose `View` field carries the
// parser SELECT for later expansion at planning time. cols
// derive from the explicit alias list when present, otherwise
// from the inner SELECT's target list (caller's
// responsibility — passes already-typed []Column). orReplace
// drops an existing same-name view first; CREATE VIEW (without
// REPLACE) over an existing object is an error per upstream.
func (c *InMemory) CreateView(name parser.ObjectName, cols []Column, aliases []string, query *parser.SelectStmt, orReplace bool) (*Table, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(name)
	if existing, ok := c.tables[k]; ok {
		if !orReplace {
			return nil, fmt.Errorf("relation %q already exists", k)
		}
		if existing.View == nil {
			return nil, fmt.Errorf("%q is not a view", k)
		}
		delete(c.tables, k)
	}
	for i := range cols {
		cols[i].Ordinal = i
	}
	t := &Table{
		Schema:            name.Schema,
		Name:              name.Name,
		Columns:           append([]Column(nil), cols...),
		OID:               c.nextOID,
		Virtual:           true,
		View:              query,
		ViewColumnAliases: append([]string(nil), aliases...),
	}
	c.nextOID++
	c.tables[k] = t
	return t, nil
}

// SetTableStats publishes the latest ANALYZE result on the
// given table. Caller must own the Table pointer (i.e. it
// came from LookupTable / CreateTable on this catalog).
// Concurrency: the catalog lock guards table-map mutations
// only; stats are pointer-replaced atomically so concurrent
// readers never see a torn struct.
func (c *InMemory) SetTableStats(table *Table, stats *TableStats) {
	if table == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	table.Stats = stats
}

// DropView removes a view from the catalog. Errors when the
// name resolves to a non-view relation; respects ifExists.
func (c *InMemory) DropView(name parser.ObjectName, ifExists bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(name)
	t, ok := c.tables[k]
	if !ok {
		if ifExists {
			return nil
		}
		return fmt.Errorf("view %q does not exist", k)
	}
	if t.View == nil {
		return fmt.Errorf("%q is not a view", k)
	}
	delete(c.tables, k)
	return nil
}

func (c *InMemory) DropTable(name parser.ObjectName) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(name)
	tbl, exists := c.tables[k]
	if !exists {
		return fmt.Errorf("relation %q does not exist", k)
	}
	if idxs, ok := c.byTable[tbl.OID]; ok {
		for idxKey := range idxs {
			delete(c.indexes, idxKey)
		}
		delete(c.byTable, tbl.OID)
	}
	delete(c.tables, k)
	return nil
}

// DropIndex removes an index from the catalog.
func (c *InMemory) DropIndex(name parser.ObjectName) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(name)
	idx, ok := c.indexes[k]
	if !ok {
		return fmt.Errorf("index %q does not exist", k)
	}
	delete(c.indexes, k)
	if idxs, ok := c.byTable[idx.Table.OID]; ok {
		delete(idxs, k)
		if len(idxs) == 0 {
			delete(c.byTable, idx.Table.OID)
		}
	}
	return nil
}

// IndexesOnTable returns indexes whose base relation is table.
func (c *InMemory) IndexesOnTable(table *Table) []*Index {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if table == nil {
		return nil
	}
	idxs := c.byTable[table.OID]
	out := make([]*Index, 0, len(idxs))
	for _, idx := range idxs {
		out = append(out, idx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QualifiedName() < out[j].QualifiedName() })
	return out
}

// HasPrimaryKey reports whether table has a primary-key index.
func (c *InMemory) HasPrimaryKey(table *Table) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if table == nil {
		return false
	}
	idxs := c.byTable[table.OID]
	for _, idx := range idxs {
		if idx.Primary {
			return true
		}
	}
	return false
}

// RelFileNode returns the storage manager identity for a table.
func (c *InMemory) RelFileNode(table *Table) storage.RelFileNode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return storage.RelFileNode{DBOid: c.dbOid, RelOid: table.OID, Fork: storage.MainFork}
}

// IndexRelFileNode returns the storage manager identity for an index.
func (c *InMemory) IndexRelFileNode(index *Index) storage.RelFileNode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return storage.RelFileNode{DBOid: c.dbOid, RelOid: index.OID, Fork: storage.MainFork}
}

// AllTables returns deep copies of every non-virtual user table
// in the catalog, in OID order. Used by the M0008 logical-decoding
// snapshot builder to freeze the schema as it stood at slot
// creation time so plugins can interpret tuple bytes against a
// stable shape. See
// docs/design/0008-0001-logical-decoding-pipeline.md.
func (c *InMemory) AllTables() []*Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Table, 0, len(c.tables))
	for _, t := range c.tables {
		if t.Virtual {
			continue
		}
		cp := *t
		cp.Columns = append([]Column(nil), t.Columns...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// FKRef pairs a child table with one of its FK constraints that
// references a given parent table. M0096-0011.
type FKRef struct {
	Child *Table
	FK    ForeignKey
}

// FindFKsReferencingTable returns all FKRef entries where FK.RefTable
// matches the given table name (case-insensitive). Used by the executor
// to find FK constraints that need enforcement when a parent row is
// deleted or updated. M0096-0011.
func (c *InMemory) FindFKsReferencingTable(tableName string) []FKRef {
	c.mu.RLock()
	defer c.mu.RUnlock()
	name := strings.ToLower(tableName)
	var out []FKRef
	for _, t := range c.tables {
		if t.Virtual {
			continue
		}
		for _, fk := range t.ForeignKeys {
			if strings.ToLower(fk.RefTable) == name {
				out = append(out, FKRef{Child: t, FK: fk})
			}
		}
	}
	return out
}

// ── Enum type methods ────────────────────────────────────────────────────────

// RegisterEnum creates a new enum type. Returns an error if the name already
// exists. M0097-0017.
func (c *InMemory) RegisterEnum(name string, values []string) (*EnumType, error) {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.enumTypes[k]; exists {
		return nil, fmt.Errorf("type %q already exists", name)
	}
	et := &EnumType{
		Name:   k,
		OID:    c.nextOID,
		Values: append([]string(nil), values...),
	}
	c.nextOID++
	c.enumTypes[k] = et
	return et, nil
}

// LookupEnum finds an enum type by name (case-insensitive). M0097-0017.
func (c *InMemory) LookupEnum(name string) (*EnumType, bool) {
	k := strings.ToLower(name)
	c.mu.RLock()
	defer c.mu.RUnlock()
	et, ok := c.enumTypes[k]
	return et, ok
}

// AddEnumValue appends a new label to an existing enum. before/after are
// reference labels (empty = append at end). Returns an error if label already
// exists unless ifNotExists is true, in which case it is a no-op. M0097-0017.
func (c *InMemory) AddEnumValue(name, value string, ifNotExists bool, before, after string) error {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	et, ok := c.enumTypes[k]
	if !ok {
		return fmt.Errorf("type %q does not exist", name)
	}
	// Check for duplicate.
	for _, v := range et.Values {
		if strings.EqualFold(v, value) {
			if ifNotExists {
				return nil
			}
			return fmt.Errorf("enum label %q already exists", value)
		}
	}
	switch {
	case before != "":
		for i, v := range et.Values {
			if strings.EqualFold(v, before) {
				newVals := make([]string, 0, len(et.Values)+1)
				newVals = append(newVals, et.Values[:i]...)
				newVals = append(newVals, value)
				newVals = append(newVals, et.Values[i:]...)
				et.Values = newVals
				return nil
			}
		}
		return fmt.Errorf("enum label %q not found", before)
	case after != "":
		for i, v := range et.Values {
			if strings.EqualFold(v, after) {
				newVals := make([]string, 0, len(et.Values)+1)
				newVals = append(newVals, et.Values[:i+1]...)
				newVals = append(newVals, value)
				newVals = append(newVals, et.Values[i+1:]...)
				et.Values = newVals
				return nil
			}
		}
		return fmt.Errorf("enum label %q not found", after)
	default:
		et.Values = append(et.Values, value)
	}
	return nil
}

// DropEnum removes an enum type. cascade=true is accepted (stub — does not
// remove dependent columns). Returns an error if not found. M0097-0017.
func (c *InMemory) DropEnum(name string, cascade bool) error {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.enumTypes[k]; !ok {
		return fmt.Errorf("type %q does not exist", name)
	}
	delete(c.enumTypes, k)
	return nil
}

// ── Domain type methods ──────────────────────────────────────────────────────

// RegisterDomain creates a new domain type. Returns an error if name already
// exists. M0097-0017.
func (c *InMemory) RegisterDomain(name string, base Type, notNull bool) (*Domain, error) {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.domains[k]; exists {
		return nil, fmt.Errorf("type %q already exists", name)
	}
	d := &Domain{
		Name:    k,
		OID:     c.nextOID,
		Base:    base,
		NotNull: notNull,
	}
	c.nextOID++
	c.domains[k] = d
	return d, nil
}

// LookupDomain finds a domain by name (case-insensitive). M0097-0017.
func (c *InMemory) LookupDomain(name string) (*Domain, bool) {
	k := strings.ToLower(name)
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.domains[k]
	return d, ok
}

// DropDomain removes a domain. cascade=true is accepted (stub). Returns an
// error if not found and ifExists is false. M0097-0017.
func (c *InMemory) DropDomain(name string, ifExists bool, cascade bool) error {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.domains[k]; !ok {
		if ifExists {
			return nil
		}
		return fmt.Errorf("type %q does not exist", name)
	}
	delete(c.domains, k)
	return nil
}

// ResolveColumnType resolves a column type name through the domain and enum
// registries to determine the effective storage type. For enums → "text"; for
// domains → recursively resolves the base type. Returns the input unchanged if
// no match is found. M0097-0017.
func (c *InMemory) ResolveColumnType(typeName string) string {
	k := strings.ToLower(typeName)
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Check domain.
	if d, ok := c.domains[k]; ok {
		baseName := strings.ToLower(d.Base.Name)
		// Recurse (without lock reacquire — use direct map lookup).
		return c.resolveColumnTypeLocked(baseName)
	}
	// Check enum.
	if _, ok := c.enumTypes[k]; ok {
		return "text"
	}
	return typeName
}

// resolveColumnTypeLocked is the lock-free recursive helper for ResolveColumnType.
func (c *InMemory) resolveColumnTypeLocked(typeName string) string {
	k := strings.ToLower(typeName)
	if d, ok := c.domains[k]; ok {
		return c.resolveColumnTypeLocked(strings.ToLower(d.Base.Name))
	}
	if _, ok := c.enumTypes[k]; ok {
		return "text"
	}
	return typeName
}
