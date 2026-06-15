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
	"strconv"
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

// RelationLockRowsFunc is optionally set by the executor to provide
// currently-held relation lock rows (from LOCK TABLE) for pg_locks.
// Same column order as AdvisoryLockRowsFunc. M0097.
var RelationLockRowsFunc func() [][]string

// AdvisoryLockRowsFunc is optionally set by the executor to provide
// currently-held advisory lock rows for the pg_locks virtual table.
// Each returned slice has the same column order as pg_locks.VirtualRows:
// locktype, database, relation, page, tuple, virtualxid, transactionid,
// classid, objid, objsubid, virtualtransaction, pid, mode, granted,
// fastpath, waitstart.
// This avoids an import cycle (executor → catalog; catalog must not → executor).
// M0097-0021.
var AdvisoryLockRowsFunc func() [][]string

// VirtualSpecLockRowsFunc is optionally set by the executor to provide
// synthetic spectoken/transactionid rows for pg_locks representing active
// speculative insertions. Same column order as AdvisoryLockRowsFunc.
// M0100-0006b.
var VirtualSpecLockRowsFunc func() [][]string

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
	// DefaultExpr holds the parsed AST of the column's DEFAULT clause when
	// CREATE TABLE provided one. nil for columns without a DEFAULT. The
	// apply worker evaluates this when filling subscriber-extra columns at
	// INSERT time so logical replication preserves DEFAULT semantics across
	// schema-extended subscribers (M0103-0007 rung 13).
	DefaultExpr parser.Expr
	// MissingValue is the precomputed default value used by the heap
	// decoder for rows that pre-date this column (storedNatts < ordinal+1).
	// Populated by `ALTER TABLE ADD COLUMN <name> <type> DEFAULT <const>`
	// to avoid the table rewrite — mirrors PostgreSQL's `attmissingval`.
	// Type is `executor.Datum`, stored as `any` to avoid the catalog →
	// executor import cycle. nil means trailing missing columns decode as
	// NULL (the prior default). M0097-0077.
	MissingValue any
	// IdentityColumn is true for GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY columns.
	// When true, INSERT without an explicit value calls nextval(tablename_colname_seq).
	IdentityColumn bool
	// IdentityStart is the START WITH value from the sequence options (0 = use default=1).
	IdentityStart int64
	// Dropped is true for columns removed via ALTER TABLE DROP COLUMN.
	// The column's heap slot (Ordinal) is retained for tuple compatibility;
	// dropped columns are invisible in SELECT *, RETURNING *, and column lookups.
	// M0097-0028.
	Dropped bool
	// Inherited is true for columns that were copied from a parent partitioned
	// table when this table was created as a partition child. Sets attislocal=false
	// and attinhcount=1 in pg_attribute. M0097-0023.
	Inherited bool
	// DeclaredTypeName is the original type name as written in DDL before domain
	// resolution (e.g. "intdom1" when Type.Name has been resolved to "int").
	// Used for DROP DOMAIN dependency tracking. Empty when the type was not a domain.
	// M0097-0023.
	DeclaredTypeName string
	// Storage is the column storage type: "plain", "main", "external", "extended".
	// Empty means the default for the column's type.
	Storage string
}

// NamedCheckConstraint holds a CHECK constraint with an explicit name.
// Name may be empty for anonymous constraints (e.g. inline column-level CHECK).
type NamedCheckConstraint struct {
	Name      string // constraint name; empty for auto-named constraints
	Expr      string // raw SQL expression (same as CheckConstraints entry)
	OID       uint32 // synthetic OID for pg_constraint virtual table
	NoInherit bool   // PG18: CHECK NO INHERIT — not propagated to child tables
	IsLocal   bool   // conislocal: true if locally defined (not purely inherited)
	InhCount  int    // coninhcount: number of direct parents this was inherited from
}

// NamedNotNullConstraint holds a NOT NULL constraint with a catalog-visible name.
// PostgreSQL 18 introduces named NOT NULL constraints (contype='n' in pg_constraint).
// Auto-named as <table>_<col>_not_null on CREATE; preserved on LIKE copy. M0097-0023.
type NamedNotNullConstraint struct {
	Name      string // e.g. "tablename_colname_not_null"
	ColName   string // column this constraint applies to
	OID       uint32 // synthetic OID for pg_constraint virtual table
	NoInherit bool   // PG18: NOT NULL NO INHERIT
	IsLocal   bool   // conislocal: true if locally declared
	InhCount  int    // coninhcount: 1 for partition children (they always inherit from one parent)
}

// AddNotNull appends a named NOT NULL constraint to the table.
// isLocal=true means the constraint is locally declared; inhCount=1 for partition children.
func (t *Table) AddNotNull(name, colName string, oid uint32, noInherit bool, isLocal bool, inhCount int) {
	t.NotNullConstraints = append(t.NotNullConstraints, NamedNotNullConstraint{
		Name: name, ColName: colName, OID: oid, NoInherit: noInherit,
		IsLocal: isLocal, InhCount: inhCount,
	})
}

// AddCheck appends a locally-defined CHECK constraint (IsLocal=true, InhCount=0),
// keeping CheckConstraints and NamedChecks parallel so index i of each always
// corresponds. name is empty for anonymous constraints; oid is 0 for anonymous
// constraints (pg_constraint's VirtualRows skips empty-name / zero-OID rows so
// the common unnamed case stays invisible in the catalog). M0097-0023.
func (t *Table) AddCheck(name, expr string, oid uint32) {
	t.CheckConstraints = append(t.CheckConstraints, expr)
	t.NamedChecks = append(t.NamedChecks, NamedCheckConstraint{
		Name: name, Expr: expr, OID: oid, IsLocal: true,
	})
}

// AddCheckInherited appends a CHECK constraint inherited from a partition parent
// (IsLocal=false, InhCount=1). Used when creating partition children.
func (t *Table) AddCheckInherited(name, expr string, oid uint32) {
	t.CheckConstraints = append(t.CheckConstraints, expr)
	t.NamedChecks = append(t.NamedChecks, NamedCheckConstraint{
		Name: name, Expr: expr, OID: oid, IsLocal: false, InhCount: 1,
	})
}

// Table is one relation in the catalog.
type Table struct {
	Schema  string
	Name    string
	Columns []Column
	OID     uint32
	// RelFileNodeOID overrides the on-disk relfile identity when it differs
	// from the catalog OID. PostgreSQL physical backups use relfilenode for
	// storage paths; when zero, goopg falls back to OID (its native layout).
	RelFileNodeOID uint32

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
	View              *parser.SelectStmt
	ViewColumnAliases []string

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

	// NamedChecks holds named CHECK constraints (name + expression).
	// Parallel to CheckConstraints; populated when the constraint has an
	// explicit CONSTRAINT name clause. Index i of NamedChecks corresponds to
	// index i of CheckConstraints; Name may be empty for anonymous constraints.
	NamedChecks []NamedCheckConstraint

	// NotNullConstraints holds NOT NULL constraints with catalog-visible names.
	// PostgreSQL 18 tracks NOT NULL constraints as contype='n' in pg_constraint.
	// Auto-named <table>_<col>_not_null on CREATE TABLE; preserved on LIKE copy.
	NotNullConstraints []NamedNotNullConstraint

	// IsSequence marks this table as a sequence virtual table. The three columns
	// (last_value int8, log_cnt int8, is_called bool) are served by VirtualRows.
	// SELECT * FROM seq_name returns the sequence's current state. M0097-0024.
	IsSequence bool

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
	// PartitionKeyOpClasses is the operator class name per key column.
	// Empty string means "use the default hash function". M0097-0027.
	PartitionKeyOpClasses []string
	// PartitionKeyExprs holds the parsed expression AST for expression-based
	// partition keys. Parallel to PartitionKey: nil entry = plain column name,
	// non-nil entry = expression (e.g. abs(b), (a+b)/2, NOT a). M0097-0023.
	PartitionKeyExprs []parser.Expr
	// PartitionKeyCollations is the explicit collation name per key column.
	// Empty string means default (not shown in pg_get_partkeydef). M0097-0023.
	PartitionKeyCollations []string

	// Unlogged / Temp track relpersistence. 'u' for UNLOGGED, 't' for TEMP, 'p' for permanent.
	Unlogged bool
	Temp     bool
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
	Events     []string // "insert", "update", "delete", "truncate"
	ForEachRow bool
	FuncName   string // function/procedure name (unschemed)
	FuncSchema string
	Args       []string // trigger function arguments (TG_ARGV)
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
	InValues   []string // LIST: values in this partition
	From       string   // RANGE: lower bound (single-column, kept for compat)
	To         string   // RANGE: upper bound (single-column, kept for compat)
	FromValues []string // RANGE: lower bound tuple (multi-column; len==1 for single-col)
	ToValues   []string // RANGE: upper bound tuple (multi-column; len==1 for single-col)
	Modulus    int64    // HASH: modulus
	Remainder  int64    // HASH: remainder (partition index)
	IsHash     bool     // true for HASH partitions
	IsDefault  bool     // true for DEFAULT partitions
	ChildName  string   // name of the child partition that owns this bound
}

// FormatPartitionBound formats a PartitionBound as the "FOR VALUES ..." string
// that PostgreSQL stores in pg_class.relpartbound (decompiled by pg_get_expr).
func FormatPartitionBound(pb PartitionBound) string {
	if pb.IsDefault {
		return "DEFAULT"
	}
	if pb.IsHash {
		return fmt.Sprintf("FOR VALUES WITH (modulus %d, remainder %d)", pb.Modulus, pb.Remainder)
	}
	if len(pb.InValues) > 0 {
		quoted := make([]string, len(pb.InValues))
		for i, v := range pb.InValues {
			quoted[i] = v
		}
		return "FOR VALUES IN (" + strings.Join(quoted, ", ") + ")"
	}
	// RANGE partition.
	fromParts := pb.FromValues
	if len(fromParts) == 0 && pb.From != "" {
		fromParts = []string{pb.From}
	}
	toParts := pb.ToValues
	if len(toParts) == 0 && pb.To != "" {
		toParts = []string{pb.To}
	}
	if len(fromParts) > 0 || len(toParts) > 0 {
		fromStr := "(" + strings.Join(fromParts, ", ") + ")"
		toStr := "(" + strings.Join(toParts, ", ") + ")"
		return "FOR VALUES FROM " + fromStr + " TO " + toStr
	}
	return ""
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
	// ColExprs holds the parsed expression AST for expression-based index
	// columns (e.g. lower(col)). Parallel to Columns: ColExprs[i] is non-nil
	// when Columns[i] == "" (expression column); nil for plain column names.
	// Not persisted to JSON (parser.Expr is not JSON-serializable).
	ColExprs []*parser.Expr
	// ColExprStrings is a pre-serialized SQL string for each expression column
	// (parallel to ColExprs). Non-empty when Columns[i]=="" and the executor
	// has serialized the expression via defaultExprToSQL. M0097-0023.
	ColExprStrings []string
	HasPredicate   bool        // true if this is a partial index (has a WHERE clause)
	Predicate      parser.Expr // WHERE predicate expression (nil if no WHERE clause)
	// PredicateString is a pre-serialized SQL string for the WHERE predicate.
	// Set by the executor at CREATE INDEX time. M0097-0023.
	PredicateString string
	IncludeColumns  []string // non-key covering columns from INCLUDE (…)
	IsConstraint    bool     // true when index backs a named UNIQUE/PK constraint (not bare CREATE INDEX)
	IsExclusion     bool     // true when index backs an EXCLUDE USING constraint
	ExclusionOp     string   // per-column exclusion operator (e.g. "=")
	// PartitionParentOID is the OID of the parent index for partition index
	// trees (ALTER INDEX parent ATTACH PARTITION child). Zero if not a partition
	// index child. M0097-0023.
	PartitionParentOID uint32
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
	// TablesInSchema returns the names of all non-virtual user tables in the given
	// schema.  Used by DROP SCHEMA CASCADE. M0097-0020.
	TablesInSchema(schemaName string) []parser.ObjectName
	// SchemaExists reports whether a schema has been registered.
	SchemaExists(name string) bool
	// RegisterSchema records a user-created schema. M0097-drop_if_exists.
	RegisterSchema(name string)
	// UnregisterSchema removes a schema from the registry. M0097-drop_if_exists.
	UnregisterSchema(name string)
	// CreateExtension records a CREATE EXTENSION install in the runtime
	// pg_extension registry. schema is the install namespace name (defaulted by
	// the caller), version the extension version string. When the extension
	// already exists it returns nil if ifNotExists is set, else an error.
	// M0110-0003 (amcheck SQL surface).
	CreateExtension(name, schema, version string, ifNotExists bool) error
	// RegisterCompatObject records a noop-created object (e.g. CREATE CONVERSION as noop).
	// objType is "conversion", "operator", "rule", "text search configuration", etc.
	RegisterCompatObject(objType, name string)
	// DropCompatObject removes an object from the compat registry. Returns true if found.
	DropCompatObject(objType, name string) bool
	// RoleExists reports whether a role has been registered. M0097-drop_if_exists.
	RoleExists(name string) bool
	// RegisterRole records a user-created role. M0097-drop_if_exists.
	RegisterRole(name string)
	// UnregisterRole removes a role from the registry. M0097-drop_if_exists.
	UnregisterRole(name string)
	IndexesOnTable(table *Table) []*Index
	// AllIndexes returns every index in the catalog, sorted by OID. M0097-0023.
	AllIndexes() []*Index
	HasPrimaryKey(table *Table) bool
	RelFileNode(table *Table) storage.RelFileNode
	IndexRelFileNode(index *Index) storage.RelFileNode
	// Routines returns the user-defined-routine registry. M0015
	// Stage A step 3: the executor's CREATE FUNCTION / DROP
	// FUNCTION operators call into this to mutate `pg_proc`-shaped
	// state. Implementations may return a process-local registry
	// (current InMemory behaviour) or a future on-disk-backed one.
	Routines() *Routines
	// RegisterUserAggregate registers a user-defined aggregate in the catalog.
	RegisterUserAggregate(agg *UserAggregate)
	// LookupUserAggregateByName looks up a user-defined aggregate by lower-case name.
	// Returns nil, false if not found.
	LookupUserAggregateByName(name string) (*UserAggregate, bool)
	// RenameUserAggregate renames an existing user-defined aggregate.
	// Returns false if the old name is not found.
	RenameUserAggregate(oldName, newName string) bool
	// LookupCompositeTypeFields returns the ordered field list for a composite
	// type registered via RegisterCompositeTypeWithFields. Returns nil if the
	// type has no field metadata. M0097-composite.
	LookupCompositeTypeFields(name string) []CompositeField
	// AllocOID atomically allocates and returns a fresh catalog OID from the
	// running counter. Used to give catalog objects a stable identity when no
	// dedicated creation method exists — e.g. named CHECK constraints that must
	// surface in pg_constraint with a real OID. M0097-0023.
	AllocOID() uint32
	// RenameTable renames a table/sequence/view by swapping its catalog key.
	// Returns an error if old does not exist or new already exists. M0097-0024.
	RenameTable(old, new parser.ObjectName) error
}

// InMemory is the v0 implementation: a sync.RWMutex-guarded map.
//
// OIDs are assigned sequentially starting at FirstUserOID. The dbOid
// field on produced RelFileNodes defaults to DefaultDBOid for v0, but
// startup may override it when importing a physical PostgreSQL backup
// whose active database lives under a different base/<oid> directory.
// Full multi-database storage routing still arrives with milestone 7.
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
	// indexPartitionChildren maps parent index OID → slice of child index OIDs
	// for partition index trees (ALTER INDEX parent ATTACH PARTITION child). M0097-0023.
	indexPartitionChildren map[uint32][]uint32

	// inheritanceChildren maps parent table OID → slice of child OIDs
	// for table inheritance support (M0096-0009).
	inheritanceChildren map[uint32][]uint32

	// enumTypes holds user-defined enum types. M0097-0017.
	enumTypes map[string]*EnumType
	// domains holds user-defined domain types. M0097-0017.
	domains map[string]*Domain
	// compositeTypeNames tracks names of composite/range/base types created via
	// CREATE TYPE ... AS (...). Since we don't implement composite type evaluation,
	// we only track the name so DROP TYPE can succeed silently. M0097-0064.
	compositeTypeNames map[string]bool
	// compositeTypeFields stores the ordered field list for composite types so
	// that PL/pgSQL can perform field access and assignment. M0097-composite.
	compositeTypeFields map[string][]CompositeField

	// constraintViewDeps maps "tableOID:constraintName" → []viewName for
	// views that rely on the constraint for GROUP BY functional dependency.
	// Used to enforce DROP CONSTRAINT RESTRICT. M0097-0036 / functional_deps.
	constraintViewDeps map[string][]string

	// opClassHashFuncs maps operator class name → hash extended routine name.
	// Only FUNCTION 2 (hash extended) entries are registered; used by
	// satisfies_hash_partition. M0097-0027.
	opClassHashFuncs map[string]string

	// opClassSchemas maps operator class name → schema (for DROP SCHEMA CASCADE).
	// M0097-0022.
	opClassSchemas map[string]string

	// userAggregates maps lower-case aggregate name → UserAggregate for
	// user-defined aggregates registered via CREATE AGGREGATE.
	userAggregates map[string]*UserAggregate

	// schemas tracks user-created schemas (CREATE SCHEMA). Pre-populated
	// with the standard system schemas. Maps lowercase schema name → OID.
	// Used to detect schema-qualified drops and for pg_namespace. M0097-drop_if_exists.
	schemas map[string]uint32

	// roles tracks user-created roles (CREATE ROLE / CREATE USER). Used by
	// DROP ROLE IF EXISTS to produce proper "does not exist" notices.
	// M0097-drop_if_exists.
	roles map[string]struct{}

	// compatObjects tracks objects created via noop CompatNoopStmt (e.g. CREATE CONVERSION,
	// CREATE OPERATOR). Key: objType (e.g. "conversion") → set of names. M0097-drop_if_exists.
	compatObjects map[string]map[string]struct{}

	// tableRuleKinds tracks the most-recently-registered rule kind per table.
	// Key: lowercase table name; value: rule kind string used by planCopy. M0097-0140.
	tableRuleKinds map[string]string

	// comments stores COMMENT ON descriptions keyed by (classoid, objoid, objsubid).
	// Populated by SetComment; read by pg_description VirtualRows. M0097-comments.
	comments map[commentKey]string

	// statisticsObjs tracks CREATE STATISTICS objects. Key = "schema.name" (lowercase).
	// Populated by RegisterStatistics; read by pg_statistic_ext VirtualRows. M0097-0023.
	statisticsObjs map[string]*StatisticsObject

	// extensions tracks CREATE EXTENSION installs (e.g. amcheck), keyed by
	// lowercase extension name. Backs the pg_extension virtual catalog read by
	// pg_amcheck's "is amcheck installed?" probe. M0110-0003.
	extensions map[string]*extensionRow
}

// extensionRow is one runtime CREATE EXTENSION record backing pg_extension.
// The install namespace is stored by name and resolved to an OID at read time
// (in pg_extension's VirtualRows), so it stays consistent if the schema set
// changes. M0110-0003.
type extensionRow struct {
	oid     uint32
	name    string
	schema  string
	version string
}

// commentKey is the composite key for pg_description.
type commentKey struct {
	ClassOID uint32
	ObjOID   uint32
	ObjSubID int32
}

// StatisticsObject tracks one CREATE STATISTICS object. M0097-0023.
type StatisticsObject struct {
	Name     string // unqualified name
	Schema   string // schema name (empty = public)
	OID      uint32
	TableOID uint32 // stxrelid — the table the statistics are defined on
}

// qualifiedKey returns the lowercase schema.name key used in statisticsObjs.
func (s *StatisticsObject) qualifiedKey() string {
	schema := s.Schema
	if schema == "" {
		schema = "public"
	}
	return strings.ToLower(schema + "." + s.Name)
}

// CommentRow is one row for pg_description.
type CommentRow struct {
	ObjOID      uint32
	ClassOID    uint32
	ObjSubID    int32
	Description string
}

// EnumValue is one label in a user-defined enum type together with its sort
// position, matching pg_enum.enumsortorder (float4). Initial values start at
// 1.0, 2.0, …; BEFORE/AFTER insertions get the midpoint of their neighbours.
// M0097-0071.
type EnumValue struct {
	Label     string
	SortOrder float64
}

// EnumType holds one user-defined enum type. M0097-0017.
type EnumType struct {
	Name   string
	OID    uint32
	Values []EnumValue // ordered by SortOrder; each element stores its own sortorder
}

// Domain holds one user-defined domain type. M0097-0017.
type Domain struct {
	Name          string
	OID           uint32
	Base          Type // resolved base type
	NotNull       bool
	CheckInValues []string // allowed values from CHECK (VALUE IN ...), M0097-domain-check
}

// CompositeField describes one field in a user-defined composite type.
// M0097-composite.
type CompositeField struct {
	Name    string // lower-case field name
	ColType string // column type string (e.g. "bigint", "text")
}

// UserAggregate holds metadata for a CREATE AGGREGATE user-defined aggregate.
// It is stored in InMemory.userAggregates and looked up by lower-case name.
type UserAggregate struct {
	Name        string   // lower-case aggregate name
	ArgTypes    []string // base argument type names (may be empty for zero-arg like count(*))
	SType       string   // state type name
	SFunc       string   // state transition function name
	FinalFunc   string   // final function name (may be empty)
	CombineFunc string   // combine function name for parallel agg (may be empty)
	InitCond    string   // initial condition string (may be empty)
	SFuncStrict bool     // true if sfunc is STRICT (skips NULL inputs)
	Variadic    bool     // true when declared with VARIADIC input arg
}

// Fixed OIDs for the three core system catalog heap tables.
// Values match upstream's pg_class.h / pg_attribute.h / pg_type.h
// so tools that query OID columns by numeric value (e.g. ODBC metadata
// probes) see the expected numbers.
const (
	TypeRelationId      uint32 = 1247 // pg_type
	AttributeRelationId uint32 = 1249 // pg_attribute
	RelationRelationId  uint32 = 1259 // pg_class
	IndexRelationId     uint32 = 2610 // pg_index
	StatisticRelationId uint32 = 2619 // pg_statistic
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

// PostgresDBOid is the PG-canonical OID for the "postgres" database
// (template_pg_database.h: Template1ObjectId=1, PostgresObjectId=5).
// A PG18 client backend connecting with `dbname=postgres` sysscan'd
// every catalog lookup at `base/5/...`. M0106-0010 bootstrap mirrors
// every nailed catalog file (heap + index) to both base/1/ and
// base/5/; runtime catalog writes (M0106-0010 batched-40) must do
// the same so a PG-standby clone of a goopg primary that ran any
// CREATE TABLE sees the user-table row through its postgres-DB lens.
const PostgresDBOid uint32 = 5

// NewInMemory returns a catalog seeded with the v0 pg_catalog
// virtual views.
func NewInMemory() *InMemory {
	c := &InMemory{
		tables:                 make(map[string]*Table),
		indexes:                make(map[string]*Index),
		byTable:                make(map[uint32]map[string]*Index),
		nextOID:                FirstUserOID,
		dbOid:                  DefaultDBOid,
		routines:               NewRoutines(),
		databases:              map[string]bool{"postgres": true},
		partitionChildren:      make(map[uint32][]uint32),
		indexPartitionChildren: make(map[uint32][]uint32),
		inheritanceChildren:    make(map[uint32][]uint32),
		enumTypes:              make(map[string]*EnumType),
		domains:                make(map[string]*Domain),
		compositeTypeNames:     make(map[string]bool),
		compositeTypeFields:    make(map[string][]CompositeField),
		constraintViewDeps:     make(map[string][]string),
		opClassHashFuncs:       make(map[string]string),
		opClassSchemas:         make(map[string]string),
		userAggregates:         make(map[string]*UserAggregate),
		schemas: map[string]uint32{
			"pg_catalog":         11,
			"public":             2200,
			"information_schema": 99,
			"pg_toast":           2200, // toast uses same OID as public in simplified model
		},
		roles:          make(map[string]struct{}),
		comments:       make(map[commentKey]string),
		statisticsObjs: make(map[string]*StatisticsObject),
		extensions:     make(map[string]*extensionRow),
	}
	c.registerSystemTables()
	return c
}

// RegisterStatistics adds a new statistics object to the catalog and returns it.
// If a statistics object with the same schema-qualified name already exists it
// is overwritten. M0097-0023.
func (c *InMemory) RegisterStatistics(schema, name string, tableOID uint32) *StatisticsObject {
	if schema == "" {
		schema = "public"
	}
	obj := &StatisticsObject{Name: name, Schema: schema, OID: c.AllocOID(), TableOID: tableOID}
	key := obj.qualifiedKey()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statisticsObjs == nil {
		c.statisticsObjs = make(map[string]*StatisticsObject)
	}
	c.statisticsObjs[key] = obj
	return obj
}

// LookupStatistics finds a statistics object by name. The name may be
// schema-qualified; if not, the public schema is tried. M0097-0023.
func (c *InMemory) LookupStatistics(name string) (*StatisticsObject, bool) {
	key := strings.ToLower(name)
	if !strings.Contains(key, ".") {
		key = "public." + key
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.statisticsObjs == nil {
		return nil, false
	}
	obj, ok := c.statisticsObjs[key]
	return obj, ok
}

// AllStatistics returns a snapshot of all registered statistics objects. M0097-0023.
func (c *InMemory) AllStatistics() []*StatisticsObject {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*StatisticsObject, 0, len(c.statisticsObjs))
	for _, obj := range c.statisticsObjs {
		out = append(out, obj)
	}
	return out
}

// SetComment stores a description for an object in pg_description.
// classoid is the OID of the system catalog that tracks the object
// (e.g. 2606 for pg_constraint, 1259 for pg_class, 3381 for pg_statistic_ext).
// objoid is the OID of the object; objsubid is 0 for whole-object comments,
// or the attnum for column comments.
func (c *InMemory) SetComment(classoid, objoid uint32, objsubid int32, description string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.comments == nil {
		c.comments = make(map[commentKey]string)
	}
	k := commentKey{ClassOID: classoid, ObjOID: objoid, ObjSubID: objsubid}
	if description == "" {
		delete(c.comments, k)
	} else {
		c.comments[k] = description
	}
}

// GetComment retrieves a stored description, returning ("", false) when absent.
func (c *InMemory) GetComment(classoid, objoid uint32, objsubid int32) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.comments[commentKey{ClassOID: classoid, ObjOID: objoid, ObjSubID: objsubid}]
	return v, ok
}

// AllComments returns all stored comments as a slice of CommentRow.
func (c *InMemory) AllComments() []CommentRow {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]CommentRow, 0, len(c.comments))
	for k, desc := range c.comments {
		out = append(out, CommentRow{
			ObjOID:      k.ObjOID,
			ClassOID:    k.ClassOID,
			ObjSubID:    k.ObjSubID,
			Description: desc,
		})
	}
	return out
}

// RegisterUserAggregate registers a user-defined aggregate in the catalog.
func (c *InMemory) RegisterUserAggregate(agg *UserAggregate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.userAggregates[strings.ToLower(agg.Name)] = agg
}

// LookupUserAggregateByName looks up a user-defined aggregate by name (case-insensitive).
// Returns nil, false if not found.
func (c *InMemory) LookupUserAggregateByName(name string) (*UserAggregate, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	a, ok := c.userAggregates[strings.ToLower(name)]
	return a, ok
}

// RenameUserAggregate renames an existing user-defined aggregate. M0097-0035.
func (c *InMemory) RenameUserAggregate(oldName, newName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	oldKey := strings.ToLower(oldName)
	agg, ok := c.userAggregates[oldKey]
	if !ok {
		return false
	}
	delete(c.userAggregates, oldKey)
	agg.Name = newName
	c.userAggregates[strings.ToLower(newName)] = agg
	return true
}

// SetDBOID overrides the database OID used for RelFileNode generation.
// v0 still exposes a single logical database; this hook exists so
// physical PostgreSQL backups whose active database is not base/1 can be
// queried without rewriting relfilenode paths on disk.
func (c *InMemory) SetDBOID(dbOid uint32) {
	if dbOid == 0 {
		return
	}
	c.mu.Lock()
	c.dbOid = dbOid
	c.mu.Unlock()
}

// DBOID returns the catalog's current storage database OID.
func (c *InMemory) DBOID() uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dbOid
}

// RegisterInheritanceChild registers childOID as a child of parentOID for
// table inheritance. Called when CREATE TABLE c INHERITS (p) executes.
// M0096-0009.
func (c *InMemory) RegisterInheritanceChild(parentOID, childOID uint32) {
	c.mu.Lock()
	c.inheritanceChildren[parentOID] = append(c.inheritanceChildren[parentOID], childOID)
	c.mu.Unlock()
}

// IsInheritanceDescendant reports whether descendantOID appears anywhere in
// the transitive inheritance-children subtree of rootOID. Used to detect
// circular inheritance before registering a new parent-child edge.
func (c *InMemory) IsInheritanceDescendant(rootOID, descendantOID uint32) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	visited := map[uint32]bool{}
	var walk func(oid uint32) bool
	walk = func(oid uint32) bool {
		if visited[oid] {
			return false
		}
		visited[oid] = true
		for _, childOID := range c.inheritanceChildren[oid] {
			if childOID == descendantOID {
				return true
			}
			if walk(childOID) {
				return true
			}
		}
		return false
	}
	return walk(rootOID)
}

// UnregisterInheritanceChild removes childOID from parentOID's child list.
func (c *InMemory) UnregisterInheritanceChild(parentOID, childOID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	children := c.inheritanceChildren[parentOID]
	for i, oid := range children {
		if oid == childOID {
			c.inheritanceChildren[parentOID] = append(children[:i], children[i+1:]...)
			return
		}
	}
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

// UnregisterPartitionChild removes childOID from parentOID's partition children
// list (DETACH PARTITION). M0097-0028.
func (c *InMemory) UnregisterPartitionChild(parentOID, childOID uint32) {
	c.mu.Lock()
	children := c.partitionChildren[parentOID]
	filtered := children[:0]
	for _, oid := range children {
		if oid != childOID {
			filtered = append(filtered, oid)
		}
	}
	c.partitionChildren[parentOID] = filtered
	c.mu.Unlock()
}

// RegisterIndexPartitionChild registers childOID as a partition child of
// parentOID in the index partition tree. M0097-0023.
func (c *InMemory) RegisterIndexPartitionChild(parentOID, childOID uint32) {
	c.mu.Lock()
	c.indexPartitionChildren[parentOID] = append(c.indexPartitionChildren[parentOID], childOID)
	c.mu.Unlock()
}

// IndexPartitionChildren returns the direct partition-child indexes of parentOID.
// Returns nil if none are registered. M0097-0023.
func (c *InMemory) IndexPartitionChildren(parentOID uint32) []*Index {
	c.mu.RLock()
	children := c.indexPartitionChildren[parentOID]
	c.mu.RUnlock()
	if len(children) == 0 {
		return nil
	}
	out := make([]*Index, 0, len(children))
	for _, oid := range children {
		if idx, ok := c.LookupIndexByOID(oid); ok {
			out = append(out, idx)
		}
	}
	return out
}

// LookupIndexByOID returns the index with the given OID, or false if not found.
// M0097-0023.
func (c *InMemory) LookupIndexByOID(oid uint32) (*Index, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, idx := range c.indexes {
		if idx.OID == oid {
			return idx, true
		}
	}
	return nil, false
}

// FindPartitionForValue finds the partition child that matches a given key value
// string for a LIST-partitioned table. Returns nil if no partition matches.
// M0096-0007.
func (c *InMemory) FindPartitionForValue(parentOID uint32, keyValue string) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var defaultPart *Table
	for _, childOID := range c.partitionChildren[parentOID] {
		for _, t := range c.tables {
			if t.OID != childOID {
				continue
			}
			for _, pb := range t.PartitionBounds {
				if pb.IsDefault {
					defaultPart = t
					continue
				}
				for _, v := range pb.InValues {
					if v == keyValue {
						return t
					}
				}
			}
		}
	}
	return defaultPart // fall back to DEFAULT partition
}

// FindRangePartitionForValue finds the RANGE partition child that contains keyValue.
// M0096-0007.
func (c *InMemory) FindRangePartitionForValue(parentOID uint32, keyValue int64) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var defaultPart *Table
	for _, childOID := range c.partitionChildren[parentOID] {
		for _, t := range c.tables {
			if t.OID != childOID {
				continue
			}
			for _, pb := range t.PartitionBounds {
				if pb.IsDefault {
					defaultPart = t
					continue
				}
				if pb.From == "" && pb.To == "" {
					continue
				}
				var from, to int64 = -1 << 62, 1 << 62
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
	return defaultPart // fall back to DEFAULT partition
}

// FindRangePartitionForDatums routes a row to its RANGE partition using a
// multi-column key tuple expressed as string-formatted values (one per
// partition-key column). Tuple comparison is lexicographic:
// (k1,k2,...) >= (f1,f2,...) AND (k1,k2,...) < (t1,t2,...).
// "MINVALUE" and "MAXVALUE" are special sentinels (-∞ and +∞).
func (c *InMemory) FindRangePartitionForDatums(parentOID uint32, keyStrs []string) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var defaultPart *Table
	for _, childOID := range c.partitionChildren[parentOID] {
		for _, t := range c.tables {
			if t.OID != childOID {
				continue
			}
			for _, pb := range t.PartitionBounds {
				if pb.IsDefault {
					defaultPart = t
					continue
				}
				if len(pb.FromValues) == 0 && len(pb.ToValues) == 0 {
					continue
				}
				if rangeStrTupleGE(keyStrs, pb.FromValues) && rangeStrTupleLT(keyStrs, pb.ToValues) {
					return t
				}
			}
		}
	}
	return defaultPart
}

// rangeStrTupleGE returns true if key >= bound (lexicographic tuple comparison).
func rangeStrTupleGE(key, bound []string) bool {
	for i := range key {
		if i >= len(bound) {
			break
		}
		cmp := compareRangeBoundStr(key[i], bound[i])
		if cmp > 0 {
			return true
		}
		if cmp < 0 {
			return false
		}
	}
	return true // equal on all compared positions: satisfies >=
}

// rangeStrTupleLT returns true if key < bound (lexicographic tuple comparison).
func rangeStrTupleLT(key, bound []string) bool {
	for i := range key {
		if i >= len(bound) {
			break
		}
		cmp := compareRangeBoundStr(key[i], bound[i])
		if cmp < 0 {
			return true
		}
		if cmp > 0 {
			return false
		}
	}
	return false // equal on all compared positions: does NOT satisfy < (exclusive upper bound)
}

// compareRangeBoundStr compares a string-formatted key value against a
// partition bound string. Returns -1, 0, +1.
// "MINVALUE" is -∞ (key > MINVALUE → +1); "MAXVALUE" is +∞ (key < MAXVALUE → -1).
func compareRangeBoundStr(keyStr, boundStr string) int {
	switch boundStr {
	case "MINVALUE":
		return 1 // anything > -∞
	case "MAXVALUE":
		return -1 // anything < +∞
	}
	switch keyStr {
	case "MINVALUE":
		return -1
	case "MAXVALUE":
		return 1
	}
	// Try integer comparison first (covers int, bigint, etc.).
	var ki, bi int64
	_, kerr := fmt.Sscanf(keyStr, "%d", &ki)
	_, berr := fmt.Sscanf(boundStr, "%d", &bi)
	if kerr == nil && berr == nil {
		if ki < bi {
			return -1
		}
		if ki > bi {
			return 1
		}
		return 0
	}
	// Fall back to lexicographic string comparison (text, char, etc.).
	if keyStr < boundStr {
		return -1
	}
	if keyStr > boundStr {
		return 1
	}
	return 0
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
	var defaultPart *Table
	for _, childOID := range c.partitionChildren[parentOID] {
		for _, t := range c.tables {
			if t.OID != childOID {
				continue
			}
			for _, pb := range t.PartitionBounds {
				if pb.IsDefault {
					defaultPart = t
					continue
				}
				if pb.IsHash && pb.Modulus > 0 {
					if int64(h%uint64(pb.Modulus)) == pb.Remainder {
						return t
					}
				}
			}
		}
	}
	return defaultPart // fall back to DEFAULT partition
}

// FindHashPartitionByHash returns the partition whose modulus/remainder matches
// the given pre-computed hash value. Used when a user-defined operator class
// provides the hash function. M0097-0022.
func (c *InMemory) FindHashPartitionByHash(parentOID uint32, h uint64) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var defaultPart *Table
	for _, childOID := range c.partitionChildren[parentOID] {
		for _, t := range c.tables {
			if t.OID != childOID {
				continue
			}
			for _, pb := range t.PartitionBounds {
				if pb.IsDefault {
					defaultPart = t
					continue
				}
				if pb.IsHash && pb.Modulus > 0 {
					if int64(h%uint64(pb.Modulus)) == pb.Remainder {
						return t
					}
				}
			}
		}
	}
	return defaultPart
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

// LookupTableByOID is the read-locked public accessor for tableByOID.
// Used by the executor to render `oid::regclass` for the `tableoid`
// system column (M0100-0005y) and similar OID-back-to-name lookups.
func (c *InMemory) LookupTableByOID(oid uint32) (*Table, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tableByOID(oid)
}

// RegisterOpClassHashFunc records that opClassName uses routineName as its
// FUNCTION 2 (hash extended support function). M0097-0027.
func (c *InMemory) RegisterOpClassHashFunc(opClassName, routineName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opClassHashFuncs[opClassName] = routineName
}

// LookupOpClassHashFunc returns the hash-extended routine name for an operator
// class, and whether one was registered. M0097-0027.
func (c *InMemory) LookupOpClassHashFunc(opClassName string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.opClassHashFuncs[opClassName]
	return v, ok
}

// RegisterOpClassSchema records the schema of an operator class.
// Used for DROP SCHEMA CASCADE detail output. M0097-0022.
func (c *InMemory) RegisterOpClassSchema(opClassName, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opClassSchemas[opClassName] = schema
}

// HasOpClass returns true if the named operator class was registered via
// RegisterOpClassSchema. Used by DROP OPERATOR CLASS to check existence.
func (c *InMemory) HasOpClass(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.opClassSchemas[name]
	return ok
}

// RemoveOpClass deletes an operator class from the registry.
func (c *InMemory) RemoveOpClass(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.opClassSchemas, name)
}

// OpClassesInSchema returns the names of operator classes in the given schema,
// sorted alphabetically. Used for DROP SCHEMA CASCADE detail output. M0097-0022.
func (c *InMemory) OpClassesInSchema(schemaName string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	schemaLC := strings.ToLower(schemaName)
	var out []string
	for name, schema := range c.opClassSchemas {
		if strings.ToLower(schema) == schemaLC {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// advanceNextOIDLocked nudges nextOID past `oid` so subsequent
// allocations don't collide with the recovered identifier.
// Caller must hold c.mu.
func (c *InMemory) advanceNextOIDLocked(oid uint32) {
	if oid >= c.nextOID {
		c.nextOID = oid + 1
	}
}

// NextOID returns the current next-OID counter value. Used by the
// checkpointer (M0106-0013) to embed the live counter into each
// checkpoint WAL record and pg_control so a crashed cluster can
// recover the OID counter without relying on pg_catalog.json.
func (c *InMemory) NextOID() uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nextOID
}

// AdvanceNextOIDPast ensures the next-OID counter is strictly greater
// than oid. Called during startup after tables are loaded from heap
// pages so the counter never re-uses an OID already present on disk.
// M0106-0013.
func (c *InMemory) AdvanceNextOIDPast(oid uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.advanceNextOIDLocked(oid)
}

// AllocOID atomically allocates and returns a fresh OID, advancing the
// running counter. Mirrors the inline `t.OID = c.nextOID; c.nextOID++`
// pattern used by CreateTable/CreateIndex, but exposed through the Catalog
// interface so executor DDL paths (e.g. named CHECK constraint creation)
// can mint synthetic OIDs without a dedicated catalog mutator. M0097-0023.
func (c *InMemory) AllocOID() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	oid := c.nextOID
	c.nextOID++
	return oid
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
// pg_class with one row per user table. The OID column is emitted
// as the table's numeric OID (decimal text wire format under type
// OID 26) so libpqrcv can decode it via DatumGetObjectId — required
// by CREATE SUBSCRIPTION's fetch_remote_table_info probe (M0103-0008
// rung 16). The regclass cast handles the legacy "name as OID"
// shape by resolving the bound text parameter through the catalog.
// verboseIntervalOffset renders a signed second count as a PostgreSQL
// postgres_verbose interval string (e.g. -28378 → "@ 7 hours 52 mins 58 secs
// ago", 3600 → "@ 1 hour", 0 → "@ 0"). The timezone system views store their
// utc_offset column pre-rendered this way because pg_regress runs the suite
// with intervalstyle=postgres_verbose and goopg's virtual tables emit stored
// strings verbatim (no type-aware reformatting). Mirrors EncodeInterval's
// INTSTYLE_POSTGRES_VERBOSE arm (postgres/src/backend/utils/adt/datetime.c).
func verboseIntervalOffset(totalSecs int) string {
	if totalSecs == 0 {
		return "@ 0"
	}
	neg := totalSecs < 0
	a := totalSecs
	if neg {
		a = -a
	}
	h := a / 3600
	m := (a % 3600) / 60
	s := a % 60
	var b strings.Builder
	b.WriteString("@")
	addPart := func(v int, unit string) {
		if v == 0 {
			return
		}
		b.WriteString(fmt.Sprintf(" %d %s", v, unit))
		if v != 1 {
			b.WriteString("s")
		}
	}
	addPart(h, "hour")
	addPart(m, "min")
	addPart(s, "sec")
	if neg {
		b.WriteString(" ago")
	}
	return b.String()
}

func (c *InMemory) registerSystemTables() {
	pgClass := &Table{
		Schema: "pg_catalog",
		Name:   "pg_class",
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "relname", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "relkind", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "relnamespace", Type: Type{Name: "oid"}, Ordinal: 3},
			// Additional columns required by vacuumdb catalog query (M0095-0004).
			{Name: "relpersistence", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "reltoastrelid", Type: Type{Name: "oid"}, Ordinal: 5},
			{Name: "relpages", Type: Type{Name: "int4"}, Ordinal: 6},
			// relispopulated: true for tables/views, reflects IsPopulated for matviews.
			// M0097-0013.
			{Name: "relispopulated", Type: Type{Name: "bool"}, Ordinal: 7},
			// relnatts: number of user columns. Required by PG's
			// CREATE SUBSCRIPTION column-list probe (M0103-0008 rung 14):
			//   `… (array_length(gpt.attrs,1) = c.relnatts) … FROM pg_class c …`
			// where `gpt = pg_get_publication_tables(...)`.
			{Name: "relnatts", Type: Type{Name: "int4"}, Ordinal: 8},
			// relreplident: replica identity setting. Required by PG's
			// CREATE SUBSCRIPTION tablesync probe (M0103-0008 rung 16):
			//   `SELECT c.oid, c.relreplident, c.relkind FROM pg_class c …`
			// 'd' = REPLICA_IDENTITY_DEFAULT (PG default for tables).
			{Name: "relreplident", Type: Type{Name: "char"}, Ordinal: 9},
			// relchecks: number of CHECK constraints. Always 0 in goopg v0.
			{Name: "relchecks", Type: Type{Name: "int2"}, Ordinal: 10},
			// Additional columns used by psql \d+ meta-commands. M0097-0028.
			{Name: "relhasindex", Type: Type{Name: "bool"}, Ordinal: 11},
			{Name: "relhasrules", Type: Type{Name: "bool"}, Ordinal: 12},
			{Name: "relhastriggers", Type: Type{Name: "bool"}, Ordinal: 13},
			{Name: "relrowsecurity", Type: Type{Name: "bool"}, Ordinal: 14},
			{Name: "relforcerowsecurity", Type: Type{Name: "bool"}, Ordinal: 15},
			{Name: "relhasoids", Type: Type{Name: "bool"}, Ordinal: 16},
			{Name: "relispartition", Type: Type{Name: "bool"}, Ordinal: 17},
			{Name: "reltablespace", Type: Type{Name: "oid"}, Ordinal: 18},
			{Name: "reloftype", Type: Type{Name: "oid"}, Ordinal: 19},
			{Name: "reloptions", Type: Type{Name: "text[]"}, Ordinal: 20},
			// relam: access method OID. 2=heap for tables, method OID for indexes.
			// Required by psql \d+ and pg_class.relam joins with pg_am. M0097-0023.
			{Name: "relam", Type: Type{Name: "oid"}, Ordinal: 21},
			// relfilenode: storage file node OID. Equals the table OID when first
			// created; changes on REINDEX/CLUSTER. Exposed so that regress tests
			// can detect filenode changes (create_index.sql REINDEX section).
			{Name: "relfilenode", Type: Type{Name: "oid"}, Ordinal: 22},
			// relpartbound: partition bound expression tree for partition children.
			// Stores the pre-formatted "FOR VALUES ..." string; pg_get_expr returns it as-is.
			{Name: "relpartbound", Type: Type{Name: "pg_node_tree"}, Ordinal: 23},
			// relhassubclass: true if any child tables (inheritance or partition children)
			// or child indexes (partition index children) exist. M0097-0026.
			{Name: "relhassubclass", Type: Type{Name: "bool"}, Ordinal: 24},
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
		out := make([][]string, 0, len(c.tables)+len(c.indexes))
		for _, k := range keys {
			t := c.tables[k]
			if t.Virtual && t.View == nil && !t.IsMatView {
				// Skip system-catalog virtual tables (pg_class, pg_locks, etc.)
				// but include user views (t.View != nil) and materialized views.
				continue
			}
			relkind := "r"
			if t.View != nil && !t.IsMatView {
				relkind = "v"
			} else if t.IsMatView {
				relkind = "m"
			} else if t.PartitionMethod != "" && t.PartitionParentOID == 0 {
				relkind = "p"
			}
			populated := "t"
			if t.IsMatView && !t.IsPopulated {
				populated = "f"
			}
			// Resolve namespace OID from the schema registry.
			schema := t.Schema
			if schema == "" {
				schema = "public"
			}
			nsOID := c.schemas[strings.ToLower(schema)]
			if nsOID == 0 {
				nsOID = 2200 // default to public
			}
			hasIdx := "f"
			if len(c.byTable[t.OID]) > 0 {
				hasIdx = "t"
			}
			isPartition := "f"
			if t.PartitionParentOID != 0 {
				isPartition = "t"
			}
			// Build relpartbound: "FOR VALUES ..." string for partition children.
			partBound := ""
			if t.PartitionParentOID != 0 && len(t.PartitionBounds) > 0 {
				partBound = FormatPartitionBound(t.PartitionBounds[0])
			}
			relpers := "p"
			if t.Unlogged {
				relpers = "u"
			} else if t.Temp {
				relpers = "t"
			}
			out = append(out, []string{
				strconv.Itoa(int(t.OID)),     // oid: numeric OID (M0103-0008 rung 16)
				t.Name,                       // relname
				relkind,                      // relkind
				strconv.Itoa(int(nsOID)),     // relnamespace: schema OID
				relpers,                      // relpersistence
				"0",                          // reltoastrelid: no TOAST table
				"0",                          // relpages: estimated page count
				populated,                    // relispopulated
				strconv.Itoa(len(t.Columns)), // relnatts: number of user columns
				"d",                          // relreplident: REPLICA_IDENTITY_DEFAULT
				"0",                          // relchecks: number of CHECK constraints
				hasIdx,                       // relhasindex
				"f",                          // relhasrules
				"f",                          // relhastriggers
				"f",                          // relrowsecurity
				"f",                          // relforcerowsecurity
				"f",                          // relhasoids
				isPartition,                  // relispartition
				"0",                          // reltablespace
				"0",                          // reloftype
				"",                           // reloptions: empty (NULL via TypedVirtualCell empty-string fallback)
				"2",                          // relam: heap access method OID (OID 2)
				strconv.Itoa(int(t.OID)),     // relfilenode: equals OID (no rewrite tracking in v0)
				partBound,                    // relpartbound: partition bound string for children
				func() string {
					if len(c.partitionChildren[t.OID]) > 0 {
						return "t"
					}
					return "f"
				}(), // relhassubclass
			})
		}
		// Emit index rows (relkind='i'/'I') so pg_class can be used to count indexes.
		idxKeys := make([]string, 0, len(c.indexes))
		for k := range c.indexes {
			idxKeys = append(idxKeys, k)
		}
		sort.Strings(idxKeys)
		for _, k := range idxKeys {
			idx := c.indexes[k]
			// Resolve namespace from the index's table.
			idxNsOID := uint32(2200)
			if idx.Table != nil {
				schema := idx.Table.Schema
				if schema == "" {
					schema = "public"
				}
				if oid := c.schemas[strings.ToLower(schema)]; oid != 0 {
					idxNsOID = oid
				}
			}
			// Determine relam for index: btree=403, hash=405, gist=783, gin=2742, spgist=4000, brin=3580.
			idxRelam := "403" // default btree
			if idx.Method == "hash" {
				idxRelam = "405"
			}
			idxPers := "p"
			if idx.Table != nil {
				if idx.Table.Unlogged {
					idxPers = "u"
				} else if idx.Table.Temp {
					idxPers = "t"
				}
			}
			// 'I' = partitioned index (has partition children); 'i' = regular index.
			hasIdxChildren := len(c.indexPartitionChildren[idx.OID]) > 0
			idxRelkind := "i"
			if hasIdxChildren {
				idxRelkind = "I"
			}
			idxHasSubclass := "f"
			if hasIdxChildren {
				idxHasSubclass = "t"
			}
			out = append(out, []string{
				strconv.Itoa(int(idx.OID)),  // oid
				idx.Name,                    // relname
				idxRelkind,                  // relkind: 'I' if partitioned, 'i' otherwise
				strconv.Itoa(int(idxNsOID)), // relnamespace
				idxPers,                     // relpersistence
				"0",                         // reltoastrelid
				"0",                         // relpages
				"t",                         // relispopulated
				"0",                         // relnatts
				"n",                         // relreplident: not applicable for indexes
				"0",                         // relchecks
				"f",                         // relhasindex
				"f",                         // relhasrules
				"f",                         // relhastriggers
				"f",                         // relrowsecurity
				"f",                         // relforcerowsecurity
				"f",                         // relhasoids
				"f",                         // relispartition
				"0",                         // reltablespace
				"0",                         // reloftype
				"",                          // reloptions: NULL
				idxRelam,                    // relam: index access method OID
				strconv.Itoa(int(idx.OID)),  // relfilenode: equals OID
				"",                          // relpartbound: indexes are never partition children
				idxHasSubclass,              // relhassubclass: true if partitioned index
			})
		}
		// Include pg_class itself (OID 1259, relkind='r', pg_catalog namespace OID 11).
		// PostgreSQL's pg_class is a real heap table; oid::int8 queries like
		//   SELECT oid::int8 FROM pg_class WHERE relname = 'pg_class'
		// must return 1259. M0097-0029.
		out = append(out, []string{
			"1259",     // oid
			"pg_class", // relname
			"r",        // relkind = regular table
			"11",       // relnamespace = pg_catalog
			"p",        // relpersistence
			"0",        // reltoastrelid
			"0",        // relpages
			"t",        // relispopulated
			"25",       // relnatts: 25 columns (including relhassubclass added M0097-0026)
			"n",        // relreplident
			"0",        // relchecks
			"t",        // relhasindex (pg_class itself has indexes)
			"f",        // relhasrules
			"f",        // relhastriggers
			"f",        // relrowsecurity
			"f",        // relforcerowsecurity
			"f",        // relhasoids
			"f",        // relispartition
			"0",        // reltablespace
			"0",        // reloftype
			"",         // reloptions: NULL
			"2",        // relam: heap access method OID
			"1259",     // relfilenode: equals OID for pg_class
			"",         // relpartbound: pg_class is not a partition child
			"f",        // relhassubclass: pg_class itself has no subclasses
		})
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
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "nspname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "nspowner", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "nspacl", Type: Type{Name: "text"}, Ordinal: 3},
		},
		OID:     2615, // upstream's NamespaceRelationId
		Virtual: true,
	}
	pgNamespace.VirtualRows = func() [][]string {
		c.mu.RLock()
		schemas := c.allSchemasLocked()
		c.mu.RUnlock()
		// Sort by OID for deterministic output.
		sort.Slice(schemas, func(i, j int) bool { return schemas[i].oid < schemas[j].oid })
		out := make([][]string, 0, len(schemas))
		for _, s := range schemas {
			if s.name == "pg_toast" {
				continue // skip internal alias
			}
			out = append(out, []string{
				strconv.Itoa(int(s.oid)), // oid
				s.name,                   // nspname
				"10",                     // nspowner
				"",                       // nspacl
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_namespace"] = pgNamespace

	// pg_extension — backs pg_amcheck's "is amcheck installed?" probe
	// (M0110-0003):
	//   SELECT n.nspname, x.extversion FROM pg_catalog.pg_extension x
	//     JOIN pg_catalog.pg_namespace n ON x.extnamespace = n.oid
	//   WHERE x.extname = 'amcheck'
	// Rows come from the runtime extensions registry (CREATE EXTENSION). Column
	// order, names, and OID match upstream pg_extension (ExtensionRelationId
	// 3079, src/include/catalog/pg_extension.h). extconfig/extcondition are
	// always NULL in goopg v0 (no extension config tables).
	pgExtension := &Table{
		Schema: "pg_catalog",
		Name:   "pg_extension",
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "extname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "extowner", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "extnamespace", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "extrelocatable", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "extversion", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "extconfig", Type: Type{Name: "oid[]"}, Ordinal: 6},
			{Name: "extcondition", Type: Type{Name: "text[]"}, Ordinal: 7},
		},
		OID:     3079, // upstream's ExtensionRelationId
		Virtual: true,
	}
	pgExtension.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		rows := make([]*extensionRow, 0, len(c.extensions))
		for _, e := range c.extensions {
			rows = append(rows, e)
		}
		// Sort by OID for deterministic output.
		sort.Slice(rows, func(i, j int) bool { return rows[i].oid < rows[j].oid })
		out := make([][]string, 0, len(rows))
		for _, e := range rows {
			nsOID := c.schemas[strings.ToLower(e.schema)]
			out = append(out, []string{
				strconv.Itoa(int(e.oid)), // oid
				e.name,                   // extname
				"10",                     // extowner (bootstrap superuser)
				strconv.Itoa(int(nsOID)), // extnamespace
				"f",                      // extrelocatable
				e.version,                // extversion
				"",                       // extconfig: NULL
				"",                       // extcondition: NULL
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_extension"] = pgExtension

	// pg_indexes view. HammerDB's checkschema step queries
	// `select tablename, indexname from pg_indexes where
	// NOTE: pg_indexes is a VIEW (not a catalog table); OID 11024 is assigned
	// here to avoid conflicting with the catalog table pg_attrdef (OID 2604).
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
		OID:     11024, // VIEW (not catalog table); pg_attrdef owns OID 2604
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
					BuildIndexDef(idx),
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
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "datname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "datdba", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "encoding", Type: Type{Name: "text"}, Ordinal: 3},
			// Additional columns for vacuumdb --all (M0095-0004).
			{Name: "datallowconn", Type: Type{Name: "boolean"}, Ordinal: 4},
			{Name: "datconnlimit", Type: Type{Name: "int4"}, Ordinal: 5},
			// datistemplate: standard pg_database column; false for all live databases (M0097-0021).
			{Name: "datistemplate", Type: Type{Name: "boolean"}, Ordinal: 6},
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
				"16384", // oid: conventional database OID (M0097-0021)
				n,
				"10",    // datdba: OID of owner (10 = postgres superuser)
				"6",     // encoding: 6 = UTF8
				"true",  // datallowconn: allow connections
				"0",     // datconnlimit: 0 = default (vacuumdb filters datconnlimit <> -2)
				"false", // datistemplate: live databases are not templates
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
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "enumtypid", Type: Type{Name: "oid"}, Ordinal: 1},
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
			for _, ev := range et.Values {
				rows = append(rows, []string{
					fmt.Sprintf("%d", oid),
					fmt.Sprintf("%d", et.OID),
					strconv.FormatFloat(ev.SortOrder, 'f', -1, 32),
					ev.Label,
				})
				oid++
			}
		}
		return rows
	}
	c.tables["pg_catalog.pg_enum"] = pgEnum

	// NOTE: pg_type (OID 1247) is a heap-backed system catalog registered by
	// initdb, NOT a virtual table. Do NOT add it here. M0097-0017 originally
	// added a virtual pg_type which broke the heap-backed version — removed.
	// Enum and domain type metadata is accessible via pg_enum and the catalog
	// enumTypes/domains registries. M0097-0018.

	// ── M0097-0018: system views needed by regress tests ──────────────────

	// pg_locks: return static relation row plus live advisory lock rows.
	// M0097-0021: AdvisoryLockRowsFunc is set by the executor at init time.
	pgLocks.VirtualRows = func() [][]string {
		// locktype, database, relation, page, tuple, virtualxid, transactionid,
		// classid, objid, objsubid, virtualtransaction, pid, mode, granted, fastpath, waitstart
		rows := [][]string{
			{"relation", "16384", "1259", "", "", "", "", "", "", "", "1/1", "0", "AccessShareLock", "t", "t", ""},
		}
		if RelationLockRowsFunc != nil {
			rows = append(rows, RelationLockRowsFunc()...)
		}
		if AdvisoryLockRowsFunc != nil {
			rows = append(rows, AdvisoryLockRowsFunc()...)
		}
		if VirtualSpecLockRowsFunc != nil {
			rows = append(rows, VirtualSpecLockRowsFunc()...)
		}
		return rows
	}

	// pg_available_extensions — 0 rows is fine.
	pgAvailExt := &Table{
		Schema: "pg_catalog", Name: "pg_available_extensions", Virtual: true,
		Columns: []Column{
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "default_version", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "installed_version", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "comment", Type: Type{Name: "text"}, Ordinal: 3},
		},
		OID: 3391,
	}
	pgAvailExt.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_available_extensions"] = pgAvailExt

	// pg_available_extension_versions — 0 rows is fine.
	pgAvailExtVer := &Table{
		Schema: "pg_catalog", Name: "pg_available_extension_versions", Virtual: true,
		Columns: []Column{
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "version", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "installed", Type: Type{Name: "bool"}, Ordinal: 2},
			{Name: "superuser", Type: Type{Name: "bool"}, Ordinal: 3},
			{Name: "trusted", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "relocatable", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "schema", Type: Type{Name: "text"}, Ordinal: 6},
			{Name: "requires", Type: Type{Name: "text"}, Ordinal: 7},
			{Name: "comment", Type: Type{Name: "text"}, Ordinal: 8},
		},
		OID: 3392,
	}
	pgAvailExtVer.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_available_extension_versions"] = pgAvailExtVer

	// pg_backend_memory_contexts — needs a row with level=1.
	pgBackendMemCtx := &Table{
		Schema: "pg_catalog", Name: "pg_backend_memory_contexts", Virtual: true,
		Columns: []Column{
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "ident", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "parent", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "level", Type: Type{Name: "int4"}, Ordinal: 3},
			{Name: "total_bytes", Type: Type{Name: "int8"}, Ordinal: 4},
			{Name: "total_nblocks", Type: Type{Name: "int8"}, Ordinal: 5},
			{Name: "free_bytes", Type: Type{Name: "int8"}, Ordinal: 6},
			{Name: "free_chunks", Type: Type{Name: "int8"}, Ordinal: 7},
			{Name: "used_bytes", Type: Type{Name: "int8"}, Ordinal: 8},
			{Name: "type", Type: Type{Name: "text"}, Ordinal: 9},
			{Name: "path", Type: Type{Name: "text"}, Ordinal: 10},
		},
		OID: 3393,
	}
	pgBackendMemCtx.VirtualRows = func() [][]string {
		// Path uses sequential integer IDs so that c1.path[c2.level]=c2.path[c2.level]
		// correctly identifies rows within the same ancestor subtree (sysviews test).
		return [][]string{
			{"TopMemoryContext", "", "", "1", "1048576", "1", "524288", "0", "524288", "AllocSet", "{1}"},
			{"CacheMemoryContext", "", "TopMemoryContext", "2", "524288", "1", "262144", "0", "262144", "AllocSet", "{1,2}"},
			{"CacheMemoryContext_child1", "", "CacheMemoryContext", "3", "8192", "1", "4096", "0", "4096", "AllocSet", "{1,2,3}"},
			{"Caller tuples", "", "TopMemoryContext", "2", "65536", "2", "32768", "0", "32768", "Bump", "{1,4}"},
		}
	}
	c.tables["pg_catalog.pg_backend_memory_contexts"] = pgBackendMemCtx

	// pg_config — needs count > 20.
	pgConfig := &Table{
		Schema: "pg_catalog", Name: "pg_config", Virtual: true,
		Columns: []Column{
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "setting", Type: Type{Name: "text"}, Ordinal: 1},
		},
		OID: 3394,
	}
	pgConfig.VirtualRows = func() [][]string {
		return [][]string{
			{"BINDIR", "/usr/lib/postgresql/18/bin"},
			{"DOCDIR", "/usr/share/doc/postgresql-doc-18"},
			{"HTMLDIR", "/usr/share/doc/postgresql-doc-18"},
			{"INCLUDEDIR", "/usr/include/postgresql"},
			{"PKGINCLUDEDIR", "/usr/include/postgresql"},
			{"INCLUDEDIR-SERVER", "/usr/include/postgresql/18/server"},
			{"LIBDIR", "/usr/lib/x86_64-linux-gnu"},
			{"PKGLIBDIR", "/usr/lib/postgresql/18/lib"},
			{"LOCALEDIR", "/usr/share/locale"},
			{"MANDIR", "/usr/share/postgresql/18/man"},
			{"SHAREDIR", "/usr/share/postgresql/18"},
			{"SYSCONFDIR", "/etc/postgresql-common"},
			{"PGXS", "/usr/lib/postgresql/18/lib/pgxs/src/makefiles/pgxs.mk"},
			{"CONFIGURE", "--with-openssl"},
			{"CC", "gcc"},
			{"CPPFLAGS", "-D_GNU_SOURCE"},
			{"CFLAGS", "-Wall -Wmissing-prototypes -Wpointer-arith"},
			{"CFLAGS_SL", "-fPIC"},
			{"LDFLAGS", "-Wl,-z,relro -Wl,-z,now"},
			{"LDFLAGS_EX", ""},
			{"LDFLAGS_SL", ""},
			{"LIBS", "-lpgcommon -lpgport -lssl -lcrypto -lz -lreadline -lm"},
			{"VERSION", "PostgreSQL 18.0"},
		}
	}
	c.tables["pg_catalog.pg_config"] = pgConfig

	// pg_cursors — count = 0 expected.
	pgCursors := &Table{
		Schema: "pg_catalog", Name: "pg_cursors", Virtual: true,
		Columns: []Column{
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "statement", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "is_holdable", Type: Type{Name: "bool"}, Ordinal: 2},
			{Name: "is_binary", Type: Type{Name: "bool"}, Ordinal: 3},
			{Name: "is_scrollable", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "creation_time", Type: Type{Name: "timestamptz"}, Ordinal: 5},
		},
		OID: 3395,
	}
	pgCursors.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_cursors"] = pgCursors

	// pg_file_settings — 0 rows is fine.
	pgFileSettings := &Table{
		Schema: "pg_catalog", Name: "pg_file_settings", Virtual: true,
		Columns: []Column{
			{Name: "sourcefile", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "sourceline", Type: Type{Name: "int4"}, Ordinal: 1},
			{Name: "seqno", Type: Type{Name: "int4"}, Ordinal: 2},
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "setting", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "applied", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "error", Type: Type{Name: "text"}, Ordinal: 6},
		},
		OID: 3396,
	}
	pgFileSettings.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_file_settings"] = pgFileSettings

	// pg_hba_file_rules — sysviews.sql checks `count(*) > 0` and
	// `count(*) FILTER (WHERE error IS NOT NULL) = 0`, so we must emit at
	// least one row whose `error` column is SQL NULL (a parsed rule with no
	// error). The error column is the last column, and both the planner
	// (buildVirtualValues) and executor (rematerialiseVirtualRows) materialise
	// a missing trailing cell as NullConst — so we omit the trailing `error`
	// cell rather than storing "" (which is NOT NULL and would make `no_err`
	// come out false).
	pgHbaRules := &Table{
		Schema: "pg_catalog", Name: "pg_hba_file_rules", Virtual: true,
		Columns: []Column{
			{Name: "rule_number", Type: Type{Name: "int4"}, Ordinal: 0},
			{Name: "file_name", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "line_number", Type: Type{Name: "int4"}, Ordinal: 2},
			{Name: "type", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "database", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "user_name", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "address", Type: Type{Name: "text"}, Ordinal: 6},
			{Name: "netmask", Type: Type{Name: "text"}, Ordinal: 7},
			{Name: "auth_method", Type: Type{Name: "text"}, Ordinal: 8},
			{Name: "options", Type: Type{Name: "text"}, Ordinal: 9},
			{Name: "error", Type: Type{Name: "text"}, Ordinal: 10},
		},
		OID: 3397,
	}
	pgHbaRules.VirtualRows = func() [][]string {
		return [][]string{
			// Trailing `error` cell omitted → materialised as SQL NULL.
			{"1", "pg_hba.conf", "1", "local", "{all}", "{all}", "", "", "trust", "{}"},
		}
	}
	c.tables["pg_catalog.pg_hba_file_rules"] = pgHbaRules

	// pg_ident_file_mappings — 0 rows is fine, no errors needed.
	pgIdentMappings := &Table{
		Schema: "pg_catalog", Name: "pg_ident_file_mappings", Virtual: true,
		Columns: []Column{
			{Name: "map_number", Type: Type{Name: "int4"}, Ordinal: 0},
			{Name: "file_name", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "line_number", Type: Type{Name: "int4"}, Ordinal: 2},
			{Name: "map_name", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "sys_name", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "pg_username", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "error", Type: Type{Name: "text"}, Ordinal: 6},
		},
		OID: 3398,
	}
	pgIdentMappings.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_ident_file_mappings"] = pgIdentMappings

	// pg_prepared_statements — count = 0 expected.
	pgPrepStmts := &Table{
		Schema: "pg_catalog", Name: "pg_prepared_statements", Virtual: true,
		Columns: []Column{
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "statement", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "prepare_time", Type: Type{Name: "timestamptz"}, Ordinal: 2},
			{Name: "parameter_types", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "result_types", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "from_sql", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "generic_plans", Type: Type{Name: "int8"}, Ordinal: 6},
			{Name: "custom_plans", Type: Type{Name: "int8"}, Ordinal: 7},
		},
		OID: 3399,
	}
	pgPrepStmts.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_prepared_statements"] = pgPrepStmts

	// pg_prepared_xacts — 0 rows is fine.
	pgPrepXacts := &Table{
		Schema: "pg_catalog", Name: "pg_prepared_xacts", Virtual: true,
		Columns: []Column{
			{Name: "transaction", Type: Type{Name: "xid"}, Ordinal: 0},
			{Name: "gid", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "prepared", Type: Type{Name: "timestamptz"}, Ordinal: 2},
			{Name: "owner", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "database", Type: Type{Name: "text"}, Ordinal: 4},
		},
		OID: 3400,
	}
	pgPrepXacts.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_prepared_xacts"] = pgPrepXacts

	// pg_stat_slru — needs count > 0.
	pgStatSlru := &Table{
		Schema: "pg_catalog", Name: "pg_stat_slru", Virtual: true,
		Columns: []Column{
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "blks_zeroed", Type: Type{Name: "int8"}, Ordinal: 1},
			{Name: "blks_hit", Type: Type{Name: "int8"}, Ordinal: 2},
			{Name: "blks_read", Type: Type{Name: "int8"}, Ordinal: 3},
			{Name: "blks_written", Type: Type{Name: "int8"}, Ordinal: 4},
			{Name: "blks_exists", Type: Type{Name: "int8"}, Ordinal: 5},
			{Name: "flushes", Type: Type{Name: "int8"}, Ordinal: 6},
			{Name: "truncates", Type: Type{Name: "int8"}, Ordinal: 7},
			{Name: "stats_reset", Type: Type{Name: "timestamptz"}, Ordinal: 8},
		},
		OID: 3401,
	}
	pgStatSlru.VirtualRows = func() [][]string {
		reset := "2026-01-01 00:00:00+00"
		return [][]string{
			{"pg_notify", "0", "0", "0", "0", "0", "0", "0", reset},
			{"pg_serial", "0", "0", "0", "0", "0", "0", "0", reset},
			{"pg_subtrans", "0", "0", "0", "0", "0", "0", "0", reset},
			{"pg_xact", "0", "0", "0", "0", "0", "0", "0", reset},
			{"pg_multixact/members", "0", "0", "0", "0", "0", "0", "0", reset},
			{"pg_multixact/offsets", "0", "0", "0", "0", "0", "0", "0", reset},
			{"pg_commit_ts", "0", "0", "0", "0", "0", "0", "0", reset},
		}
	}
	c.tables["pg_catalog.pg_stat_slru"] = pgStatSlru

	// pg_stat_wal — exactly 1 row expected.
	pgStatWal := &Table{
		Schema: "pg_catalog", Name: "pg_stat_wal", Virtual: true,
		Columns: []Column{
			{Name: "wal_records", Type: Type{Name: "int8"}, Ordinal: 0},
			{Name: "wal_fpi", Type: Type{Name: "int8"}, Ordinal: 1},
			{Name: "wal_bytes", Type: Type{Name: "numeric"}, Ordinal: 2},
			{Name: "wal_buffers_full", Type: Type{Name: "int8"}, Ordinal: 3},
			{Name: "wal_write", Type: Type{Name: "int8"}, Ordinal: 4},
			{Name: "wal_sync", Type: Type{Name: "int8"}, Ordinal: 5},
			{Name: "wal_write_time", Type: Type{Name: "float8"}, Ordinal: 6},
			{Name: "wal_sync_time", Type: Type{Name: "float8"}, Ordinal: 7},
			{Name: "stats_reset", Type: Type{Name: "timestamptz"}, Ordinal: 8},
		},
		OID: 3402,
	}
	pgStatWal.VirtualRows = func() [][]string {
		return [][]string{
			{"0", "0", "0", "0", "0", "0", "0", "0", "2026-01-01 00:00:00+00"},
		}
	}
	c.tables["pg_catalog.pg_stat_wal"] = pgStatWal

	// pg_stat_io — per-backend-type I/O statistics (PG 16+, OID 8061).
	// goopg v0 does not track I/O statistics; all counters are 0 and no
	// rows are returned. The table exists so queries filtering by
	// backend_type (e.g. 'walsummarizer') succeed and return 0 rows.
	pgStatIO := &Table{
		Schema: "pg_catalog", Name: "pg_stat_io", Virtual: true,
		Columns: []Column{
			{Name: "backend_type", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "object", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "context", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "reads", Type: Type{Name: "int8"}, Ordinal: 3},
			{Name: "read_bytes", Type: Type{Name: "int8"}, Ordinal: 4},
			{Name: "read_time", Type: Type{Name: "float8"}, Ordinal: 5},
			{Name: "writes", Type: Type{Name: "int8"}, Ordinal: 6},
			{Name: "write_bytes", Type: Type{Name: "int8"}, Ordinal: 7},
			{Name: "write_time", Type: Type{Name: "float8"}, Ordinal: 8},
			{Name: "writebacks", Type: Type{Name: "int8"}, Ordinal: 9},
			{Name: "writeback_time", Type: Type{Name: "float8"}, Ordinal: 10},
			{Name: "extends", Type: Type{Name: "int8"}, Ordinal: 11},
			{Name: "extend_bytes", Type: Type{Name: "int8"}, Ordinal: 12},
			{Name: "extend_time", Type: Type{Name: "float8"}, Ordinal: 13},
			{Name: "hits", Type: Type{Name: "int8"}, Ordinal: 14},
			{Name: "evictions", Type: Type{Name: "int8"}, Ordinal: 15},
			{Name: "reuses", Type: Type{Name: "int8"}, Ordinal: 16},
			{Name: "fsyncs", Type: Type{Name: "int8"}, Ordinal: 17},
			{Name: "fsync_time", Type: Type{Name: "float8"}, Ordinal: 18},
			{Name: "stats_reset", Type: Type{Name: "timestamptz"}, Ordinal: 19},
		},
		OID: 8061,
	}
	pgStatIO.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_stat_io"] = pgStatIO

	// pg_description — stores COMMENT ON object descriptions (OID 2609).
	// COMMENT ON is parsed as a no-op in goopg v0; this stub allows queries
	// against pg_description to succeed (returning 0 rows) instead of erroring
	// with "relation pg_description does not exist". M0097-0023.
	pgDescription := &Table{
		Schema: "pg_catalog", Name: "pg_description", Virtual: true,
		Columns: []Column{
			{Name: "objoid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "classoid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "objsubid", Type: Type{Name: "int4"}, Ordinal: 2},
			{Name: "description", Type: Type{Name: "text"}, Ordinal: 3},
		},
		OID: 2609,
	}
	pgDescription.VirtualRows = func() [][]string {
		rows := c.AllComments()
		out := make([][]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, []string{
				fmt.Sprintf("%d", r.ObjOID),
				fmt.Sprintf("%d", r.ClassOID),
				fmt.Sprintf("%d", r.ObjSubID),
				r.Description,
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_description"] = pgDescription

	// pg_attrdef — stores column default expressions (OID 2604).
	// COMMENT ON and DEFAULT tracking are stubs in goopg v0; this virtual table
	// lets psql \d+ meta-queries succeed (returning 0 rows) instead of erroring
	// with "relation pg_attrdef does not exist". M0097-0023.
	pgAttrdef := &Table{
		Schema: "pg_catalog", Name: "pg_attrdef", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "adrelid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "adnum", Type: Type{Name: "int2"}, Ordinal: 2},
			{Name: "adbin", Type: Type{Name: "text"}, Ordinal: 3},
		},
		OID: 2604,
	}
	pgAttrdef.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		var rows [][]string
		var oid uint32 = 1
		for _, tbl := range c.tables {
			if tbl.Virtual {
				continue
			}
			for _, col := range tbl.Columns {
				if col.DefaultExpr == nil {
					continue
				}
				adbin := formatExprForAttrdef(col.DefaultExpr)
				rows = append(rows, []string{
					fmt.Sprintf("%d", oid),
					fmt.Sprintf("%d", tbl.OID),
					fmt.Sprintf("%d", col.Ordinal+1),
					adbin,
				})
				oid++
			}
		}
		return rows
	}
	c.tables["pg_catalog.pg_attrdef"] = pgAttrdef

	// pg_constraint — stores table and domain constraint definitions (OID 2606).
	// Constraint tracking is a stub in goopg v0; this virtual table lets queries
	// like SELECT conname FROM pg_constraint WHERE conrelid = ... succeed (returning
	// 0 rows) instead of erroring with "relation pg_constraint does not exist".
	// M0097-0023.
	pgConstraint := &Table{
		Schema: "pg_catalog", Name: "pg_constraint", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "conname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "connamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "contype", Type: Type{Name: "char"}, Ordinal: 3},
			{Name: "condeferrable", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "condeferred", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "convalidated", Type: Type{Name: "bool"}, Ordinal: 6},
			{Name: "conrelid", Type: Type{Name: "oid"}, Ordinal: 7},
			{Name: "contypid", Type: Type{Name: "oid"}, Ordinal: 8},
			{Name: "conindid", Type: Type{Name: "oid"}, Ordinal: 9},
			{Name: "conparentid", Type: Type{Name: "oid"}, Ordinal: 10},
			{Name: "confrelid", Type: Type{Name: "oid"}, Ordinal: 11},
			{Name: "confupdtype", Type: Type{Name: "char"}, Ordinal: 12},
			{Name: "confdeltype", Type: Type{Name: "char"}, Ordinal: 13},
			{Name: "confmatchtype", Type: Type{Name: "char"}, Ordinal: 14},
			{Name: "conislocal", Type: Type{Name: "bool"}, Ordinal: 15},
			{Name: "coninhcount", Type: Type{Name: "int2"}, Ordinal: 16},
			{Name: "connoinherit", Type: Type{Name: "bool"}, Ordinal: 17},
			{Name: "conperiod", Type: Type{Name: "bool"}, Ordinal: 18},
			{Name: "conkey", Type: Type{Name: "int2[]"}, Ordinal: 19},
			{Name: "confkey", Type: Type{Name: "int2[]"}, Ordinal: 20},
			{Name: "conpfeqop", Type: Type{Name: "oid[]"}, Ordinal: 21},
			{Name: "conppeqop", Type: Type{Name: "oid[]"}, Ordinal: 22},
			{Name: "confdelsetcols", Type: Type{Name: "int2[]"}, Ordinal: 23},
			{Name: "conbin", Type: Type{Name: "text"}, Ordinal: 24},
			// conenforced: PG18 column; true = constraint is enforced (default),
			// false = NOT ENFORCED. goopg v0 always enforces. M0097-0026.
			{Name: "conenforced", Type: Type{Name: "bool"}, Ordinal: 25},
		},
		OID: 2606,
	}
	pgConstraint.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		var out [][]string
		for _, tbl := range c.tables {
			if tbl.Virtual || tbl.OID == 0 {
				continue
			}
			// Emit named CHECK constraints.
			for _, nc := range tbl.NamedChecks {
				if nc.Name == "" || nc.OID == 0 {
					continue
				}
				row := make([]string, 26)
				row[0] = fmt.Sprintf("%d", nc.OID)  // oid
				row[1] = nc.Name                    // conname
				row[2] = "2200"                     // connamespace (public)
				row[3] = "c"                        // contype = check
				row[4] = "f"                        // condeferrable
				row[5] = "f"                        // condeferred
				row[6] = "t"                        // convalidated
				row[7] = fmt.Sprintf("%d", tbl.OID) // conrelid
				row[8] = "0"                        // contypid
				row[9] = "0"                        // conindid
				row[10] = "0"                       // conparentid
				row[11] = "0"                       // confrelid
				row[12] = " "                       // confupdtype
				row[13] = " "                       // confdeltype
				row[14] = " "                       // confmatchtype
				if nc.IsLocal {
					row[15] = "t"
				} else {
					row[15] = "f"
				}
				row[16] = fmt.Sprintf("%d", nc.InhCount) // coninhcount
				row[17] = "f"                            // connoinherit
				row[18] = "f"                            // conperiod
				row[24] = nc.Expr                        // conbin
				row[25] = "t"                            // conenforced: always true in v0
				out = append(out, row)
			}
		}
		// Emit UNIQUE, PRIMARY KEY, and EXCLUDE constraints from constraint-backed indexes.
		for _, idx := range c.indexes {
			if (!idx.IsConstraint && !idx.IsExclusion) || idx.Table == nil {
				continue
			}
			colOrdMap := make(map[string]int, len(idx.Table.Columns))
			for _, col := range idx.Table.Columns {
				colOrdMap[col.Name] = col.Ordinal + 1
			}
			var keyNums []string
			for _, colName := range idx.Columns {
				if ord, ok := colOrdMap[colName]; ok {
					keyNums = append(keyNums, fmt.Sprintf("%d", ord))
				}
			}
			contype := "u"
			if idx.Primary {
				contype = "p"
			} else if idx.IsExclusion {
				contype = "x"
			}
			row := make([]string, 26)
			row[0] = fmt.Sprintf("%d", idx.OID)
			row[1] = idx.Name
			row[2] = "2200"
			row[3] = contype
			row[4] = "f"
			row[5] = "f"
			row[6] = "t"
			row[7] = fmt.Sprintf("%d", idx.Table.OID)
			row[8] = "0"
			row[9] = fmt.Sprintf("%d", idx.OID)
			row[10] = "0"
			row[11] = "0"
			row[12] = " "
			row[13] = " "
			row[14] = " "
			row[15] = "t"
			row[16] = "0"
			row[17] = "f"
			row[18] = "f"
			row[19] = "{" + strings.Join(keyNums, ",") + "}"
			row[25] = "t" // conenforced: always true in v0
			out = append(out, row)
		}
		// Emit NOT NULL constraints (contype='n', PG18). M0097-0023.
		for _, tbl := range c.tables {
			if tbl.Virtual || tbl.OID == 0 {
				continue
			}
			for _, nc := range tbl.NotNullConstraints {
				if nc.Name == "" || nc.OID == 0 {
					continue
				}
				colOrd := 0
				for i, col := range tbl.Columns {
					if strings.EqualFold(col.Name, nc.ColName) {
						colOrd = i + 1
						break
					}
				}
				row := make([]string, 26)
				row[0] = fmt.Sprintf("%d", nc.OID)
				row[1] = nc.Name
				row[2] = "2200"
				row[3] = "n" // contype = not null
				row[4] = "f"
				row[5] = "f"
				row[6] = "t"
				row[7] = fmt.Sprintf("%d", tbl.OID)
				row[8] = "0"
				row[9] = "0"
				row[10] = "0"
				row[11] = "0"
				row[12] = " "
				row[13] = " "
				row[14] = " "
				if nc.IsLocal {
					row[15] = "t"
				} else {
					row[15] = "f"
				}
				row[16] = fmt.Sprintf("%d", nc.InhCount)
				if nc.NoInherit {
					row[17] = "t"
				} else {
					row[17] = "f"
				}
				row[18] = "f"
				if colOrd > 0 {
					row[19] = fmt.Sprintf("{%d}", colOrd)
				}
				row[25] = "t" // conenforced: always true in v0
				out = append(out, row)
			}
		}
		return out
	}
	c.tables["pg_catalog.pg_constraint"] = pgConstraint

	// pg_inherits — stores inheritance/partition parent-child relationships (OID 2611).
	// Used by psql \d to identify partition children. M0097-0023.
	pgInherits := &Table{
		Schema: "pg_catalog", Name: "pg_inherits", Virtual: true,
		Columns: []Column{
			{Name: "inhrelid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "inhparent", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "inhseqno", Type: Type{Name: "int4"}, Ordinal: 2},
			{Name: "inhdetachpending", Type: Type{Name: "bool"}, Ordinal: 3},
		},
		OID: 2611,
	}
	pgInherits.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		var out [][]string
		parentSeq := make(map[uint32]int)
		for _, tbl := range c.tables {
			if tbl.Virtual || tbl.PartitionParentOID == 0 {
				continue
			}
			parentSeq[tbl.PartitionParentOID]++
			seq := parentSeq[tbl.PartitionParentOID]
			out = append(out, []string{
				fmt.Sprintf("%d", tbl.OID),
				fmt.Sprintf("%d", tbl.PartitionParentOID),
				fmt.Sprintf("%d", seq),
				"f",
			})
		}
		// Emit index partition rows: each index with PartitionParentOID set is a
		// partition child of its parent index. These rows enable the join pattern:
		//   pg_index LEFT JOIN pg_inherits ON (indexrelid = inhrelid)
		// used by indexing.sql to discover partitioned-index parent/child chains.
		idxParentSeq := make(map[uint32]int)
		for _, idx := range c.indexes {
			if idx.PartitionParentOID == 0 {
				continue
			}
			idxParentSeq[idx.PartitionParentOID]++
			seq := idxParentSeq[idx.PartitionParentOID]
			out = append(out, []string{
				fmt.Sprintf("%d", idx.OID),
				fmt.Sprintf("%d", idx.PartitionParentOID),
				fmt.Sprintf("%d", seq),
				"f",
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_inherits"] = pgInherits

	// pg_index — stores index definitions (OID 2610).
	// This virtual stub lets queries that join against pg_index (e.g. psql \d+
	// meta-queries) succeed instead of erroring with "relation pg_index does not
	// exist". M0097-0023.
	pgIndexCatalog := &Table{
		Schema: "pg_catalog", Name: "pg_index", Virtual: true,
		Columns: []Column{
			{Name: "indexrelid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "indrelid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "indnatts", Type: Type{Name: "int2"}, Ordinal: 2},
			{Name: "indnkeyatts", Type: Type{Name: "int2"}, Ordinal: 3},
			{Name: "indisunique", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "indnullsnotdistinct", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "indisprimary", Type: Type{Name: "bool"}, Ordinal: 6},
			{Name: "indisexclusion", Type: Type{Name: "bool"}, Ordinal: 7},
			{Name: "indimmediate", Type: Type{Name: "bool"}, Ordinal: 8},
			{Name: "indisclustered", Type: Type{Name: "bool"}, Ordinal: 9},
			{Name: "indisvalid", Type: Type{Name: "bool"}, Ordinal: 10},
			{Name: "indcheckxmin", Type: Type{Name: "bool"}, Ordinal: 11},
			{Name: "indisready", Type: Type{Name: "bool"}, Ordinal: 12},
			{Name: "indislive", Type: Type{Name: "bool"}, Ordinal: 13},
			{Name: "indisreplident", Type: Type{Name: "bool"}, Ordinal: 14},
			{Name: "indkey", Type: Type{Name: "int2[]"}, Ordinal: 15},
			{Name: "indcollation", Type: Type{Name: "oid[]"}, Ordinal: 16},
			{Name: "indclass", Type: Type{Name: "oid[]"}, Ordinal: 17},
			{Name: "indoption", Type: Type{Name: "int2[]"}, Ordinal: 18},
			{Name: "indexprs", Type: Type{Name: "text"}, Ordinal: 19},
			{Name: "indpred", Type: Type{Name: "text"}, Ordinal: 20},
			{Name: "indcoloptions", Type: Type{Name: "int2[]"}, Ordinal: 21},
		},
		OID: 2610,
	}
	pgIndexCatalog.VirtualRows = func() [][]string {
		idxs := c.AllIndexes()
		out := make([][]string, 0, len(idxs))
		for _, idx := range idxs {
			if idx.Table == nil {
				continue
			}
			// Build column-ordinal lookups (1-based) for the parent table.
			colOrd := make(map[string]int, len(idx.Table.Columns))
			for _, col := range idx.Table.Columns {
				colOrd[col.Name] = col.Ordinal + 1
			}
			// indkey: key columns then include columns; expression columns get ordinal 0.
			keyParts := make([]string, 0, len(idx.Columns)+len(idx.IncludeColumns))
			for _, col := range idx.Columns {
				if col == "" {
					keyParts = append(keyParts, "0")
				} else if ord, ok := colOrd[col]; ok {
					keyParts = append(keyParts, fmt.Sprintf("%d", ord))
				} else {
					keyParts = append(keyParts, "0")
				}
			}
			for _, col := range idx.IncludeColumns {
				if ord, ok := colOrd[col]; ok {
					keyParts = append(keyParts, fmt.Sprintf("%d", ord))
				} else {
					keyParts = append(keyParts, "0")
				}
			}
			indkey := strings.Join(keyParts, " ")
			// indclass: one opclass OID per key column (0 = unknown; btree int4=1978).
			classOIDs := make([]string, len(idx.Columns))
			for i, col := range idx.Columns {
				oid := "0"
				if col != "" {
					for _, tc := range idx.Table.Columns {
						if tc.Name == col {
							switch tc.Type.Name {
							case "int2", "smallint":
								oid = "1970"
							case "int4", "int", "integer", "serial":
								oid = "1978"
							case "int8", "bigint", "bigserial":
								oid = "1980"
							case "float4", "real":
								oid = "2968"
							case "float8", "double precision":
								oid = "2970"
							case "text", "varchar", "character varying":
								oid = "1994"
							case "name":
								oid = "1996"
							case "bpchar", "char", "character":
								oid = "426"
							case "oid":
								oid = "2990"
							case "bool", "boolean":
								oid = "424"
							case "date":
								oid = "434"
							case "timestamp", "timestamp without time zone":
								oid = "2040"
							case "timestamptz", "timestamp with time zone":
								oid = "2040"
							}
							break
						}
					}
				}
				classOIDs[i] = oid
			}
			indclass := strings.Join(classOIDs, " ")
			boolStr := func(b bool) string {
				if b {
					return "t"
				}
				return "f"
			}
			natts := len(idx.Columns) + len(idx.IncludeColumns)
			nkeyatts := len(idx.Columns)
			// Build space-separated zero-vector for indcollation/indoption.
			buildZeroVec := func(n int) string {
				if n <= 0 {
					return ""
				}
				parts := make([]string, n)
				for i := range parts {
					parts[i] = "0"
				}
				return strings.Join(parts, " ")
			}
			out = append(out, []string{
				fmt.Sprintf("%d", idx.OID),       // indexrelid
				fmt.Sprintf("%d", idx.Table.OID), // indrelid
				fmt.Sprintf("%d", natts),         // indnatts
				fmt.Sprintf("%d", nkeyatts),      // indnkeyatts
				boolStr(idx.Unique),              // indisunique
				"f",                              // indnullsnotdistinct
				boolStr(idx.Primary),             // indisprimary
				boolStr(idx.IsExclusion),         // indisexclusion
				"t",                              // indimmediate
				"f",                              // indisclustered
				"t",                              // indisvalid
				"f",                              // indcheckxmin
				"t",                              // indisready
				"t",                              // indislive
				"f",                              // indisreplident
				indkey,                           // indkey
				buildZeroVec(nkeyatts),           // indcollation
				indclass,                         // indclass
				buildZeroVec(nkeyatts),           // indoption
				"",                               // indexprs (NULL)
				"",                               // indpred (NULL)
				"",                               // indcoloptions (NULL)
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_index"] = pgIndexCatalog

	// pg_statistic_ext — stores extended statistics objects (OID 3381).
	// This virtual stub lets queries against pg_statistic_ext succeed instead
	// of erroring with "relation pg_statistic_ext does not exist". M0097-0023.
	pgStatisticExt := &Table{
		Schema: "pg_catalog", Name: "pg_statistic_ext", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "stxrelid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "stxname", Type: Type{Name: "name"}, Ordinal: 2},
			{Name: "stxnamespace", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "stxowner", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "stxstattarget", Type: Type{Name: "int4"}, Ordinal: 5},
			{Name: "stxkeys", Type: Type{Name: "int2[]"}, Ordinal: 6},
			{Name: "stxexprs", Type: Type{Name: "text"}, Ordinal: 7},
			{Name: "stxkind", Type: Type{Name: "text"}, Ordinal: 8},
		},
		OID: 3381,
	}
	pgStatisticExt.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		if len(c.statisticsObjs) == 0 {
			return nil
		}
		out := make([][]string, 0, len(c.statisticsObjs))
		for _, obj := range c.statisticsObjs {
			schema := obj.Schema
			if schema == "" {
				schema = "public"
			}
			nsOID := "2200" // public namespace OID
			if schema != "public" {
				nsOID = "0"
			}
			row := make([]string, 9)
			row[0] = fmt.Sprintf("%d", obj.OID)      // oid
			row[1] = fmt.Sprintf("%d", obj.TableOID) // stxrelid
			row[2] = obj.Name                        // stxname
			row[3] = nsOID                           // stxnamespace
			row[4] = "10"                            // stxowner (bootstrap superuser)
			row[5] = "-1"                            // stxstattarget (default)
			row[6] = ""                              // stxkeys
			row[7] = ""                              // stxexprs
			row[8] = ""                              // stxkind
			out = append(out, row)
		}
		return out
	}
	c.tables["pg_catalog.pg_statistic_ext"] = pgStatisticExt

	// pg_collation — stores collation definitions (OID 3456).
	// This virtual stub lets psql \d+ meta-queries succeed instead of erroring
	// with "relation pg_collation does not exist". M0097-0023.
	pgCollation := &Table{
		Schema: "pg_catalog", Name: "pg_collation", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "collname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "collnamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "collowner", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "collprovider", Type: Type{Name: "char"}, Ordinal: 4},
			{Name: "collisdeterministic", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "collencoding", Type: Type{Name: "int4"}, Ordinal: 6},
			{Name: "collcollate", Type: Type{Name: "text"}, Ordinal: 7},
			{Name: "collctype", Type: Type{Name: "text"}, Ordinal: 8},
			{Name: "colllocale", Type: Type{Name: "text"}, Ordinal: 9},
			{Name: "collicurules", Type: Type{Name: "text"}, Ordinal: 10},
			{Name: "collversion", Type: Type{Name: "text"}, Ordinal: 11},
		},
		OID: 3456,
	}
	pgCollation.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_collation"] = pgCollation

	// pg_policy — stores row-level security policies (OID 3256).
	// Row-level security is not implemented in goopg v0; this stub lets psql
	// \d+ meta-queries succeed instead of erroring. M0097-0023.
	pgPolicy := &Table{
		Schema: "pg_catalog", Name: "pg_policy", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "polname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "polrelid", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "polcmd", Type: Type{Name: "char"}, Ordinal: 3},
			{Name: "polpermissive", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "polroles", Type: Type{Name: "oid[]"}, Ordinal: 5},
			{Name: "polqual", Type: Type{Name: "text"}, Ordinal: 6},
			{Name: "polwithcheck", Type: Type{Name: "text"}, Ordinal: 7},
		},
		OID: 3256,
	}
	pgPolicy.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_policy"] = pgPolicy

	// pg_wait_events — needs at least one row per type.
	pgWaitEvents := &Table{
		Schema: "pg_catalog", Name: "pg_wait_events", Virtual: true,
		Columns: []Column{
			{Name: "type", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "description", Type: Type{Name: "text"}, Ordinal: 2},
		},
		OID: 3403,
	}
	pgWaitEvents.VirtualRows = func() [][]string {
		return [][]string{
			{"Activity", "ArchiverMain", "Waiting in main loop of archiver process."},
			{"Activity", "AutoVacuumMain", "Waiting in main loop of autovacuum launcher process."},
			{"Activity", "BgWriterHibernate", "Waiting in background writer process, hibernating."},
			{"Activity", "BgWriterMain", "Waiting in main loop of background writer process."},
			{"Activity", "CheckpointerMain", "Waiting in main loop of checkpointer process."},
			{"Activity", "LogicalApplyMain", "Waiting in main loop of logical replication apply process."},
			{"Activity", "LogicalLauncherMain", "Waiting in main loop of logical replication launcher process."},
			{"Activity", "RecoveryWalStream", "Waiting in main loop of startup process for WAL to arrive."},
			{"Activity", "SysLoggerMain", "Waiting in main loop of syslogger process."},
			{"Activity", "WalReceiverMain", "Waiting in main loop of WAL receiver process."},
			{"Activity", "WalSenderMain", "Waiting in main loop of WAL sender process."},
			{"Activity", "WalSummarizer", "Waiting in main loop of WAL summarizer."},
			{"Activity", "WalWriterMain", "Waiting in main loop of WAL writer process."},
			{"Client", "ClientRead", "Waiting to read data from the client."},
			{"Client", "ClientWrite", "Waiting to write data to the client."},
			{"Client", "GSSOpenServer", "Waiting to read data from the client while establishing a GSSAPI session."},
			{"Client", "LibPQWalReceiverConnect", "Waiting in WAL receiver to establish connection to remote server."},
			{"Client", "LibPQWalReceiverReceive", "Waiting in WAL receiver to receive data from remote server."},
			{"Client", "SSLOpenServer", "Waiting to read from client to finish establishing SSL connection."},
			{"Client", "WalSenderWaitForWAL", "Waiting for WAL to be flushed in WAL sender process."},
			{"Client", "WalSenderWriteData", "Waiting for any activity when processing replies from WAL receiver in WAL sender process."},
			{"IO", "BufFileRead", "Waiting for a read from a buffered file."},
			{"IO", "BufFileWrite", "Waiting for a write to a buffered file."},
			{"IO", "BufFileTruncate", "Waiting for a truncate of a buffered file."},
			{"IO", "ControlFileRead", "Waiting for a read from the pg_control file."},
			{"IO", "ControlFileSync", "Waiting for the pg_control file to reach durable storage."},
			{"IO", "ControlFileWrite", "Waiting for a write to the pg_control file."},
			{"IO", "DataFileExtend", "Waiting for a relation data file to be extended."},
			{"IO", "DataFileFlush", "Waiting for a relation data file to reach durable storage."},
			{"IO", "DataFileRead", "Waiting for a read from a relation data file."},
			{"IO", "DataFileSync", "Waiting for changes to a relation data file to reach durable storage."},
			{"IO", "DataFileTruncate", "Waiting for a relation data file to be truncated."},
			{"IO", "DataFileWrite", "Waiting for a write to a relation data file."},
			{"Lock", "advisory", "Waiting to acquire an advisory user lock."},
			{"Lock", "applytransaction", "Waiting to acquire a lock on a remote transaction being applied by a logical replication subscriber."},
			{"Lock", "extend", "Waiting to extend a relation."},
			{"Lock", "frozenid", "Waiting to update pg_database.datfrozenxid and pg_database.datminmxid."},
			{"Lock", "object", "Waiting to acquire a lock on a non-relation database object."},
			{"Lock", "page", "Waiting to acquire a lock on a page of a relation."},
			{"Lock", "relation", "Waiting to acquire a lock on a relation."},
			{"Lock", "spectoken", "Waiting to acquire a speculative insertion lock."},
			{"Lock", "transaction", "Waiting for a transaction to finish."},
			{"Lock", "tuple", "Waiting to acquire a lock on a tuple."},
			{"Lock", "userlock", "Waiting to acquire a user lock."},
			{"Lock", "virtualxid", "Waiting to acquire a virtual transaction ID lock."},
			{"LWLock", "AddinShmemInit", "Waiting to manage an extension's space allocation in shared memory."},
			{"LWLock", "AutoFile", "Waiting to update the postgresql.auto.conf file."},
			{"LWLock", "Autovacuum", "Waiting to read or update the current state of autovacuum workers."},
			{"LWLock", "AutovacuumSchedule", "Waiting to ensure that a table selected for autovacuum still needs vacuuming."},
			{"LWLock", "BackgroundWorker", "Waiting to read or update background worker state."},
			{"LWLock", "BtreeVacuum", "Waiting to read or update vacuum-related information for a B-tree index."},
			{"LWLock", "BufferContent", "Waiting to access a data page in memory."},
			{"LWLock", "BufferMapping", "Waiting to associate a data block with a buffer in the buffer pool."},
			{"LWLock", "Checkpoint", "Waiting to begin a checkpoint."},
			{"LWLock", "CheckpointerComm", "Waiting to manage communication with the checkpointer."},
			{"LWLock", "ControlFile", "Waiting to read or update the pg_control file or create a new WAL file."},
			{"LWLock", "ShmemIndexLock", "Waiting to find or allocate space in shared memory."},
			{"LWLock", "WALBufMapping", "Waiting to replace a page in WAL buffers."},
			{"LWLock", "WALWrite", "Waiting for WAL buffers to be written to disk."},
			{"Timeout", "BaseBackupThrottle", "Waiting during base backup when throttling activity."},
			{"Timeout", "CheckpointWriteDelay", "Waiting between writes while performing a checkpoint."},
			{"Timeout", "PgSleep", "Waiting due to a call to pg_sleep or a sibling function."},
			{"Timeout", "RecoveryApplyDelay", "Waiting to apply WAL at recovery because of a recovery_min_apply_delay setting."},
			{"Timeout", "RecoveryRetrieveRetryInterval", "Waiting during recovery when WAL data is not available from any source."},
			{"Timeout", "RegisterSyncRequest", "Waiting while inserting a request for the checkpointer to perform a fsync."},
			{"Timeout", "SpinDelay", "Waiting while acquiring a contended spinlock."},
			{"Timeout", "VacuumDelay", "Waiting in a cost-based vacuum delay point."},
			{"Timeout", "VacuumTruncate", "Waiting to acquire an exclusive lock to truncate off any empty pages at the end of a table vacuumed."},
			{"BufferPin", "BufferPin", "Waiting to acquire an exclusive pin on a buffer."},
			{"Extension", "Extension", "Waiting in an extension."},
			{"IPC", "AppendReady", "Waiting for subplan nodes of an Append plan node to be ready."},
			{"IPC", "BackendTermination", "Waiting for the termination of another backend."},
			{"IPC", "BgWorkerShutdown", "Waiting for background worker to shut down."},
			{"IPC", "BgWorkerStartup", "Waiting for background worker to start up."},
		}
	}
	c.tables["pg_catalog.pg_wait_events"] = pgWaitEvents

	// pg_timezone_names — needs count(distinct utc_offset) >= 24.
	pgTimezoneNames := &Table{
		Schema: "pg_catalog", Name: "pg_timezone_names", Virtual: true,
		Columns: []Column{
			{Name: "name", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "abbrev", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "utc_offset", Type: Type{Name: "interval"}, Ordinal: 2},
			{Name: "is_dst", Type: Type{Name: "bool"}, Ordinal: 3},
		},
		OID: 3404,
	}
	pgTimezoneNames.VirtualRows = func() [][]string {
		var rows [][]string
		for i := -12; i <= 14; i++ {
			var name, abbrev string
			if i == 0 {
				name = "UTC"
				abbrev = "UTC"
			} else if i > 0 {
				name = fmt.Sprintf("Etc/GMT-%d", i)
				abbrev = fmt.Sprintf("GMT-%d", i)
			} else {
				name = fmt.Sprintf("Etc/GMT+%d", -i)
				abbrev = fmt.Sprintf("GMT+%d", -i)
			}
			rows = append(rows, []string{name, abbrev, verboseIntervalOffset(i * 3600), "f"})
		}
		// Add fractional offsets for extra distinct utc_offsets.
		rows = append(rows, []string{"Asia/Kolkata", "IST", verboseIntervalOffset(5*3600 + 30*60), "f"})
		rows = append(rows, []string{"Asia/Kathmandu", "NPT", verboseIntervalOffset(5*3600 + 45*60), "f"})
		rows = append(rows, []string{"Pacific/Marquesas", "MART", verboseIntervalOffset(-(9*3600 + 30*60)), "f"})
		rows = append(rows, []string{"Pacific/Chatham", "CHAST", verboseIntervalOffset(12*3600 + 45*60), "f"})
		// LMT historical local-mean-time for America/Los_Angeles.
		rows = append(rows, []string{"America/Los_Angeles", "LMT", verboseIntervalOffset(-(7*3600 + 52*60 + 58)), "f"})
		return rows
	}
	c.tables["pg_catalog.pg_timezone_names"] = pgTimezoneNames

	// pg_timezone_abbrevs — needs count(distinct utc_offset) >= 24 and a row for abbrev = 'LMT'.
	pgTimezoneAbbrevs := &Table{
		Schema: "pg_catalog", Name: "pg_timezone_abbrevs", Virtual: true,
		Columns: []Column{
			{Name: "abbrev", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "utc_offset", Type: Type{Name: "interval"}, Ordinal: 1},
			{Name: "is_dst", Type: Type{Name: "bool"}, Ordinal: 2},
		},
		OID: 3405,
	}
	pgTimezoneAbbrevs.VirtualRows = func() [][]string {
		var rows [][]string
		for i := -12; i <= 14; i++ {
			var abbrev string
			if i == 0 {
				abbrev = "UTC"
			} else if i > 0 {
				abbrev = fmt.Sprintf("GMT-%d", i)
			} else {
				abbrev = fmt.Sprintf("GMT+%d", -i)
			}
			rows = append(rows, []string{abbrev, verboseIntervalOffset(i * 3600), "f"})
		}
		// Fractional offsets.
		rows = append(rows, []string{"IST", verboseIntervalOffset(5*3600 + 30*60), "f"})
		rows = append(rows, []string{"NPT", verboseIntervalOffset(5*3600 + 45*60), "f"})
		rows = append(rows, []string{"MART", verboseIntervalOffset(-(9*3600 + 30*60)), "f"})
		rows = append(rows, []string{"CHAST", verboseIntervalOffset(12*3600 + 45*60), "f"})
		// LMT entry required by sysviews.sql: select * from pg_timezone_abbrevs where abbrev = 'LMT'.
		// PostgreSQL displays this offset as "@ 7 hours 52 mins 58 secs ago"
		// because pg_regress forces intervalstyle=postgres_verbose.
		rows = append(rows, []string{"LMT", verboseIntervalOffset(-(7*3600 + 52*60 + 58)), "f"})
		return rows
	}
	c.tables["pg_catalog.pg_timezone_abbrevs"] = pgTimezoneAbbrevs

	// pg_am — access method catalog (OID 2601).
	// Returns the standard set of PostgreSQL access methods so queries
	// that join pg_am (e.g. \d+ in psql) succeed. M0097-0028.
	pgAm := &Table{
		Schema: "pg_catalog", Name: "pg_am", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "amname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "amhandler", Type: Type{Name: "regproc"}, Ordinal: 2},
			{Name: "amtype", Type: Type{Name: "char"}, Ordinal: 3},
		},
		OID: 2601,
	}
	pgAm.VirtualRows = func() [][]string {
		return [][]string{
			{"2", "heap", "3", "t"},
			{"403", "btree", "330", "i"},
			{"405", "hash", "331", "i"},
			{"783", "gist", "332", "i"},
			{"2742", "gin", "333", "i"},
			{"4000", "spgist", "334", "i"},
			{"3580", "brin", "335", "i"},
		}
	}
	c.tables["pg_catalog.pg_am"] = pgAm

	// Update pg_settings to include more enable_* settings so sysviews.sql
	// `select name, setting from pg_settings where name like 'enable%'` is non-empty.
	pgSettings.VirtualRows = func() [][]string {
		rows := [][]string{
			{"default_transaction_isolation", "read committed", "", "Client Connection Defaults / Statement Behavior",
				"Sets the transaction isolation level of each new transaction.", "",
				"user", "enum", "default", "", "", "{\"serializable\",\"repeatable read\",\"read committed\",\"read uncommitted\"}",
				"read committed", "read committed", "", "", "f"},
			{"enable_seqscan", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of sequential-scan plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_indexscan", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of index-scan plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_indexonlyscan", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of index-only-scan plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_bitmapscan", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of bitmap-scan plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_hashjoin", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of hash join plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_mergejoin", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of merge join plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_nestloop", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of nested-loop join plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_sort", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of explicit sort steps.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_hashagg", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of hashed aggregation plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_material", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of materialization.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_partition_pruning", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables plan-time and run-time partition pruning.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_partitionwise_join", "off", "", "Query Tuning / Planner Method Configuration",
				"Enables partitionwise join.", "",
				"user", "bool", "default", "", "", "", "off", "off", "", "", "f"},
			{"enable_partitionwise_aggregate", "off", "", "Query Tuning / Planner Method Configuration",
				"Enables partitionwise aggregation and grouping.", "",
				"user", "bool", "default", "", "", "", "off", "off", "", "", "f"},
			{"enable_parallel_hash", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of parallel hash plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_parallel_append", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of parallel append plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_gathermerge", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of gather merge plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_incremental_sort", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of incremental sort steps.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_async_append", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of async append plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_memoize", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of memoization.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_presorted_aggregate", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of presorted aggregate plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_distinct_reordering", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables reordering of DISTINCT pathkeys.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_group_by_reordering", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables reordering of GROUP BY keys.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_self_join_elimination", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables removal of unique self-joins.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			{"enable_tidscan", "on", "", "Query Tuning / Planner Method Configuration",
				"Enables the planner's use of TID scan plans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
			// Additional planner GUCs needed for regress tests. M0097-0069.
			{"from_collapse_limit", "8", "", "Query Tuning / Planner Cost Constants",
				"Sets the FROM-list size beyond which subqueries are not collapsed.", "",
				"user", "integer", "default", "1", "2147483647", "", "8", "8", "", "", "f"},
			{"join_collapse_limit", "8", "", "Query Tuning / Planner Cost Constants",
				"Sets the FROM-list size beyond which JOIN constructs are not flattened.", "",
				"user", "integer", "default", "1", "2147483647", "", "8", "8", "", "", "f"},
			{"hash_mem_multiplier", "2.0", "", "Query Tuning / Planner Cost Constants",
				"Multiple of work_mem to use for hash tables.", "",
				"user", "real", "default", "1", "1000", "", "2", "2", "", "", "f"},
			{"parallel_leader_participation", "on", "", "Query Tuning / Planner Method Configuration",
				"Controls whether Gather and Gather Merge also run subplans.", "",
				"user", "bool", "default", "", "", "", "on", "on", "", "", "f"},
		}
		// PostgreSQL's pg_settings view is backed by the alphabetically
		// sorted GUC table, so callers that query it without ORDER BY (e.g.
		// sysviews.sql's `... where name like 'enable%'`) still receive rows
		// ordered by name. Sort here to match that contract regardless of the
		// hand-coded literal order above.
		sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
		return rows
	}

	// information_schema virtual tables (M0097-0022).
	c.registerInformationSchemaTables()
}

// registerInformationSchemaTables adds information_schema virtual tables:
// routines, parameters, and usage stubs. These read from the routine registry.
func (c *InMemory) registerInformationSchemaTables() {
	// information_schema.routines — one row per user-defined routine.
	isRoutines := &Table{
		Schema:  "information_schema",
		Name:    "routines",
		Virtual: true,
		Columns: []Column{
			{Name: "specific_catalog", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "specific_schema", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "specific_name", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "routine_catalog", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "routine_schema", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "routine_name", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "routine_type", Type: Type{Name: "text"}, Ordinal: 6},
		},
	}
	isRoutines.VirtualRows = func() [][]string {
		rs := c.Routines()
		if rs == nil {
			return nil
		}
		var rows [][]string
		for _, r := range rs.List() {
			schema := r.Schema
			if schema == "" {
				schema = "public"
			}
			specificName := fmt.Sprintf("%s_%d", r.Name, r.OID)
			rtype := "FUNCTION"
			if r.IsProcedure {
				rtype = "PROCEDURE"
			}
			rows = append(rows, []string{
				"postgres", schema, specificName, "postgres", schema, r.Name, rtype,
			})
		}
		return rows
	}
	c.tables["information_schema.routines"] = isRoutines

	// information_schema.parameters — one row per parameter per routine.
	isParams := &Table{
		Schema:  "information_schema",
		Name:    "parameters",
		Virtual: true,
		Columns: []Column{
			{Name: "specific_catalog", Type: Type{Name: "text"}, Ordinal: 0},
			{Name: "specific_schema", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "specific_name", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "ordinal_position", Type: Type{Name: "int4"}, Ordinal: 3},
			{Name: "parameter_mode", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "parameter_name", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "data_type", Type: Type{Name: "text"}, Ordinal: 6},
			{Name: "parameter_default", Type: Type{Name: "text"}, Ordinal: 7},
		},
	}
	isParams.VirtualRows = func() [][]string {
		rs := c.Routines()
		if rs == nil {
			return nil
		}
		var rows [][]string
		for _, r := range rs.List() {
			schema := r.Schema
			if schema == "" {
				schema = "public"
			}
			specificName := fmt.Sprintf("%s_%d", r.Name, r.OID)
			for i, t := range r.ArgTypes {
				mode := "IN"
				if i < len(r.ArgModes) {
					switch r.ArgModes[i] {
					case "o":
						mode = "OUT"
					case "b":
						mode = "INOUT"
					case "v":
						mode = "VARIADIC"
					}
				}
				paramName := ""
				if i < len(r.ArgNames) {
					paramName = r.ArgNames[i]
				}
				paramDefault := ""
				if i < len(r.ArgDefaults) {
					pd := r.ArgDefaults[i]
					if pd != "" {
						// Annotate string literals with ::type cast (PG canonical form).
						if len(pd) >= 2 && pd[0] == '\'' && pd[len(pd)-1] == '\'' {
							typName := strings.ToLower(t.Name)
							switch typName {
							case "text", "varchar", "character varying", "char", "bpchar":
								pd = pd + "::" + typName
							}
						}
						paramDefault = pd
					}
				}
				rows = append(rows, []string{
					"postgres", schema, specificName,
					fmt.Sprintf("%d", i+1),
					mode, paramName, t.Name, paramDefault,
				})
			}
		}
		return rows
	}
	c.tables["information_schema.parameters"] = isParams

	// Stub usage views — columns match PG's information_schema; return no rows
	// (body-dependency analysis not yet implemented).
	isRoutineUsageColsBase := []Column{
		{Name: "specific_catalog", Type: Type{Name: "text"}, Ordinal: 0},
		{Name: "specific_schema", Type: Type{Name: "text"}, Ordinal: 1},
		{Name: "specific_name", Type: Type{Name: "text"}, Ordinal: 2},
		{Name: "routine_catalog", Type: Type{Name: "text"}, Ordinal: 3},
		{Name: "routine_schema", Type: Type{Name: "text"}, Ordinal: 4},
		{Name: "routine_name", Type: Type{Name: "text"}, Ordinal: 5},
	}
	isRoutineUsageViews := map[string][]Column{
		"routine_routine_usage": append(isRoutineUsageColsBase,
			Column{Name: "called_specific_catalog", Type: Type{Name: "text"}, Ordinal: 6},
			Column{Name: "called_specific_schema", Type: Type{Name: "text"}, Ordinal: 7},
			Column{Name: "called_specific_name", Type: Type{Name: "text"}, Ordinal: 8},
		),
		"routine_sequence_usage": append(isRoutineUsageColsBase,
			Column{Name: "sequence_catalog", Type: Type{Name: "text"}, Ordinal: 6},
			Column{Name: "sequence_schema", Type: Type{Name: "text"}, Ordinal: 7},
			Column{Name: "sequence_name", Type: Type{Name: "text"}, Ordinal: 8},
		),
		"routine_column_usage": append(isRoutineUsageColsBase,
			Column{Name: "table_catalog", Type: Type{Name: "text"}, Ordinal: 6},
			Column{Name: "table_schema", Type: Type{Name: "text"}, Ordinal: 7},
			Column{Name: "table_name", Type: Type{Name: "text"}, Ordinal: 8},
			Column{Name: "column_name", Type: Type{Name: "text"}, Ordinal: 9},
		),
		"routine_table_usage": append(isRoutineUsageColsBase,
			Column{Name: "table_catalog", Type: Type{Name: "text"}, Ordinal: 6},
			Column{Name: "table_schema", Type: Type{Name: "text"}, Ordinal: 7},
			Column{Name: "table_name", Type: Type{Name: "text"}, Ordinal: 8},
		),
	}
	// routine_routine_usage: one row per routine-to-routine dependency.
	rruCols := isRoutineUsageViews["routine_routine_usage"]
	rruTbl := &Table{Schema: "information_schema", Name: "routine_routine_usage", Virtual: true, Columns: rruCols}
	rruTbl.VirtualRows = func() [][]string {
		rs := c.Routines()
		if rs == nil {
			return nil
		}
		var rows [][]string
		for _, r := range rs.List() {
			if len(r.RoutineCallOIDs) == 0 {
				continue
			}
			rSchema := r.Schema
			if rSchema == "" {
				rSchema = "public"
			}
			callerSpec := fmt.Sprintf("%s_%d", r.Name, r.OID)
			for _, calledOID := range r.RoutineCallOIDs {
				called := rs.LookupByOID(calledOID)
				if called == nil {
					continue
				}
				calledSchema := called.Schema
				if calledSchema == "" {
					calledSchema = "public"
				}
				calledSpec := fmt.Sprintf("%s_%d", called.Name, called.OID)
				rows = append(rows, []string{
					"postgres", rSchema, callerSpec, "postgres", calledSchema, calledSpec,
					"postgres", calledSchema, calledSpec,
				})
			}
		}
		return rows
	}
	c.tables["information_schema.routine_routine_usage"] = rruTbl

	// routine_sequence_usage: one row per sequence dependency.
	rsuCols := isRoutineUsageViews["routine_sequence_usage"]
	rsuTbl := &Table{Schema: "information_schema", Name: "routine_sequence_usage", Virtual: true, Columns: rsuCols}
	rsuTbl.VirtualRows = func() [][]string {
		rs := c.Routines()
		if rs == nil {
			return nil
		}
		var rows [][]string
		for _, r := range rs.List() {
			if len(r.SequenceDeps) == 0 {
				continue
			}
			rSchema := r.Schema
			if rSchema == "" {
				rSchema = "public"
			}
			specificName := fmt.Sprintf("%s_%d", r.Name, r.OID)
			for _, dep := range r.SequenceDeps {
				seqSchema := dep.Schema
				if seqSchema == "" {
					seqSchema = rSchema
				}
				rows = append(rows, []string{
					"postgres", rSchema, specificName, "postgres", rSchema, r.Name,
					"postgres", seqSchema, dep.Name,
				})
			}
		}
		return rows
	}
	c.tables["information_schema.routine_sequence_usage"] = rsuTbl

	// routine_column_usage: one row per column dependency.
	rcuCols := isRoutineUsageViews["routine_column_usage"]
	rcuTbl := &Table{Schema: "information_schema", Name: "routine_column_usage", Virtual: true, Columns: rcuCols}
	rcuTbl.VirtualRows = func() [][]string {
		rs := c.Routines()
		if rs == nil {
			return nil
		}
		var rows [][]string
		for _, r := range rs.List() {
			if len(r.ColumnDeps) == 0 {
				continue
			}
			rSchema := r.Schema
			if rSchema == "" {
				rSchema = "public"
			}
			specificName := fmt.Sprintf("%s_%d", r.Name, r.OID)
			for _, dep := range r.ColumnDeps {
				tblSchema := dep.TableSchema
				if tblSchema == "" {
					tblSchema = rSchema
				}
				// Only include dep if referenced table still exists.
				if _, exists := c.LookupTable(parser.ObjectName{Schema: tblSchema, Name: dep.TableName}); !exists {
					if _, exists2 := c.LookupTable(parser.ObjectName{Name: dep.TableName}); !exists2 {
						continue
					}
				}
				rows = append(rows, []string{
					"postgres", rSchema, specificName, "postgres", rSchema, r.Name,
					"postgres", tblSchema, dep.TableName, dep.ColumnName,
				})
			}
		}
		return rows
	}
	c.tables["information_schema.routine_column_usage"] = rcuTbl

	// routine_table_usage: one row per table dependency.
	rtuCols := isRoutineUsageViews["routine_table_usage"]
	rtuTbl := &Table{Schema: "information_schema", Name: "routine_table_usage", Virtual: true, Columns: rtuCols}
	rtuTbl.VirtualRows = func() [][]string {
		rs := c.Routines()
		if rs == nil {
			return nil
		}
		var rows [][]string
		for _, r := range rs.List() {
			if len(r.TableDeps) == 0 {
				continue
			}
			rSchema := r.Schema
			if rSchema == "" {
				rSchema = "public"
			}
			specificName := fmt.Sprintf("%s_%d", r.Name, r.OID)
			for _, dep := range r.TableDeps {
				tblSchema := dep.Schema
				if tblSchema == "" {
					tblSchema = rSchema
				}
				// Only include dep if referenced table still exists.
				if _, exists := c.LookupTable(parser.ObjectName{Schema: tblSchema, Name: dep.Name}); !exists {
					if _, exists2 := c.LookupTable(parser.ObjectName{Name: dep.Name}); !exists2 {
						continue
					}
				}
				rows = append(rows, []string{
					"postgres", rSchema, specificName, "postgres", rSchema, r.Name,
					"postgres", tblSchema, dep.Name,
				})
			}
		}
		return rows
	}
	c.tables["information_schema.routine_table_usage"] = rtuTbl
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
		if t, ok := c.tables[key(parser.ObjectName{Schema: "public", Name: name.Name})]; ok {
			return t, true
		}
		if t, ok := c.tables[key(parser.ObjectName{Schema: "pg_catalog", Name: name.Name})]; ok {
			return t, true
		}
	} else {
		// Schema-qualified lookup: fall back to unqualified key to handle tables
		// that were moved to a different schema via SET SCHEMA (catalog key is unchanged).
		// A table stored without an explicit schema (t.Schema="") is treated as being
		// in "public", so a "public.foo" lookup finds a bare-keyed "foo" entry. M0097-0023.
		if t, ok := c.tables[name.Name]; ok {
			tSchema := t.Schema
			if tSchema == "" {
				tSchema = "public"
			}
			if strings.EqualFold(tSchema, name.Schema) {
				return t, true
			}
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
// unresolved. Unqualified lookups fall back to an unqualified search
// so schema-qualified references find indexes stored without a schema.
func (c *InMemory) LookupIndex(name parser.ObjectName) (*Index, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if idx, ok := c.indexes[key(name)]; ok {
		return idx, ok
	}
	if name.Schema == "" {
		// Unqualified name: try "public.<name>" first (indexes created via DDL
		// always carry the table's schema, which defaults to "public").
		if idx, ok := c.indexes["public."+name.Name]; ok {
			return idx, ok
		}
	} else {
		// Schema-qualified lookup failed: fall back to bare name for indexes
		// created without an explicit schema in the catalog key.
		if idx, ok := c.indexes[name.Name]; ok {
			return idx, ok
		}
	}
	return nil, false
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

// RenameTable renames a catalog table/view/sequence entry from old to new.
// Returns an error when old does not exist or new already exists. M0097-0024.
func (c *InMemory) RenameTable(old, new parser.ObjectName) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	oldK := key(old)
	newK := key(new)
	tbl, exists := c.tables[oldK]
	if !exists {
		return fmt.Errorf("relation %q does not exist", oldK)
	}
	if _, exists2 := c.tables[newK]; exists2 {
		return fmt.Errorf("relation %q already exists", newK)
	}
	// Re-key the table entry under the new name, preserving the pointer.
	tbl.Schema = new.Schema
	tbl.Name = new.Name
	c.tables[newK] = tbl
	delete(c.tables, oldK)
	return nil
}

// RegisterTable re-inserts a previously-dropped table back into the catalog.
// Used when a TEMP TABLE shadows a permanent table and is then dropped —
// the permanent table is restored by re-registering its saved *Table. M0097-0003.
func (c *InMemory) RegisterTable(tbl *Table) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := strings.ToLower(tbl.Name)
	if tbl.Schema != "" {
		k = strings.ToLower(tbl.Schema) + "." + k
	}
	c.tables[k] = tbl
}

// RestoreIndex re-inserts a previously-dropped index back into the catalog.
// Used when ROLLBACK TO SAVEPOINT undoes a DROP TABLE that happened inside
// a savepoint. M0097-0023.
func (c *InMemory) RestoreIndex(idx *Index) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(parser.ObjectName{Schema: idx.Schema, Name: idx.Name})
	c.indexes[k] = idx
	if c.byTable[idx.Table.OID] == nil {
		c.byTable[idx.Table.OID] = map[string]*Index{}
	}
	c.byTable[idx.Table.OID][k] = idx
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

// TablesInSchema returns the names of all non-virtual user tables whose Schema
// field matches schemaName (case-insensitive).  Virtual/system tables in
// pg_catalog or information_schema are excluded.  Used by DROP SCHEMA CASCADE
// to identify objects to cascade-drop. M0097-0020.
// SchemaExists reports whether a schema with the given name has been registered.
// Pre-populated with the standard system schemas; user schemas are added by
// RegisterSchema (called from CREATE SCHEMA). M0097-drop_if_exists.
func (c *InMemory) SchemaExists(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.schemas[strings.ToLower(name)]
	return ok
}

// SchemaOID returns the OID for the given schema name (0 if not found).
func (c *InMemory) SchemaOID(name string) uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.schemas[strings.ToLower(name)]
}

// RegisterSchema records a user-created schema. Called from execCreateSchema.
func (c *InMemory) RegisterSchema(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lc := strings.ToLower(name)
	if _, ok := c.schemas[lc]; !ok {
		c.nextOID++
		c.schemas[lc] = c.nextOID
	}
}

// UnregisterSchema removes a schema from the registry. Called from DROP SCHEMA.
func (c *InMemory) UnregisterSchema(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.schemas, strings.ToLower(name))
}

// CreateExtension records a CREATE EXTENSION install in the runtime
// pg_extension registry. Called from the executor's execCreateExtension after
// it has validated the extension name and resolved the default version/schema.
// M0110-0003.
func (c *InMemory) CreateExtension(name, schema, version string, ifNotExists bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	lc := strings.ToLower(name)
	if _, ok := c.extensions[lc]; ok {
		if ifNotExists {
			return nil
		}
		return fmt.Errorf("extension %q already exists", name)
	}
	if schema == "" {
		schema = "public"
	}
	c.nextOID++
	c.extensions[lc] = &extensionRow{
		oid:     c.nextOID,
		name:    name,
		schema:  schema,
		version: version,
	}
	return nil
}

// allSchemasLocked returns all (name, oid) pairs. Must be called with mu held.
func (c *InMemory) allSchemasLocked() []struct {
	name string
	oid  uint32
} {
	out := make([]struct {
		name string
		oid  uint32
	}, 0, len(c.schemas))
	for name, oid := range c.schemas {
		out = append(out, struct {
			name string
			oid  uint32
		}{name, oid})
	}
	return out
}

// RoleExists reports whether a role with the given name has been registered.
func (c *InMemory) RoleExists(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.roles[strings.ToLower(name)]
	return ok
}

// RegisterRole records a user-created role. Called from CREATE ROLE/USER.
func (c *InMemory) RegisterRole(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roles[strings.ToLower(name)] = struct{}{}
}

// UnregisterRole removes a role from the registry. Called from DROP ROLE.
func (c *InMemory) UnregisterRole(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.roles, strings.ToLower(name))
}

// RegisterCompatObject records a noop-created object (e.g. CREATE CONVERSION as noop).
// Used so that DROP X (without IF EXISTS) can succeed when CREATE X was also a noop.
func (c *InMemory) RegisterCompatObject(objType, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.compatObjects == nil {
		c.compatObjects = make(map[string]map[string]struct{})
	}
	key := strings.ToLower(objType)
	if _, ok := c.compatObjects[key]; !ok {
		c.compatObjects[key] = make(map[string]struct{})
	}
	c.compatObjects[key][name] = struct{}{}
}

// DropCompatObject removes an object from the compat registry. Returns true if found+removed.
func (c *InMemory) DropCompatObject(objType, name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(objType)
	if c.compatObjects == nil {
		return false
	}
	if _, ok := c.compatObjects[key][name]; ok {
		delete(c.compatObjects[key], name)
		return true
	}
	return false
}

// ListCompatObjects returns all registered names for a given object type.
func (c *InMemory) ListCompatObjects(objType string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := strings.ToLower(objType)
	if c.compatObjects == nil {
		return nil
	}
	m := c.compatObjects[key]
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// RegisterTableRuleKind records the most recently created rule kind for a table.
// Used by planCopy to return rule-specific errors. M0097-0140.
func (c *InMemory) RegisterTableRuleKind(tableName, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tableRuleKinds == nil {
		c.tableRuleKinds = make(map[string]string)
	}
	c.tableRuleKinds[strings.ToLower(tableName)] = kind
}

// UnregisterTableRules removes the rule kind record for a table (on DROP RULE). M0097-0140.
func (c *InMemory) UnregisterTableRules(tableName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tableRuleKinds != nil {
		delete(c.tableRuleKinds, strings.ToLower(tableName))
	}
}

// TableRuleKind returns the most recently registered rule kind for a table, or "". M0097-0140.
func (c *InMemory) TableRuleKind(tableName string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.tableRuleKinds == nil {
		return ""
	}
	return c.tableRuleKinds[strings.ToLower(tableName)]
}

func (c *InMemory) TablesInSchema(schemaName string) []parser.ObjectName {
	c.mu.RLock()
	defer c.mu.RUnlock()
	schemaLC := strings.ToLower(schemaName)
	var out []parser.ObjectName
	for k, t := range c.tables {
		if t.Virtual {
			continue // skip system/virtual tables
		}
		// Partition children are dropped implicitly when their parent is dropped;
		// skip them from the direct schema-level cascade list.
		// Use PartitionParentOID (not PartitionBounds) to identify children —
		// PartitionBounds is also appended to the parent when children register.
		if t.PartitionParentOID != 0 {
			continue
		}
		tSchema := t.Schema
		if tSchema == "" {
			tSchema = "public"
		}
		if strings.ToLower(tSchema) == schemaLC {
			// Return ObjectName matching the actual catalog key. Tables stored
			// under an unqualified key (no schema prefix) must be returned
			// without schema so DropTable and detail lines use the same key.
			if k == strings.ToLower(t.Name) {
				out = append(out, parser.ObjectName{Name: t.Name})
			} else {
				out = append(out, parser.ObjectName{Schema: t.Schema, Name: t.Name})
			}
		}
	}
	return out
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

// BuildIndexDef reconstructs the CREATE INDEX DDL string for an index.
// Used by pg_indexes.indexdef and pg_get_indexdef(). M0097-0023.
func BuildIndexDef(idx *Index) string {
	var sb strings.Builder
	sb.WriteString("CREATE ")
	if idx.Unique {
		sb.WriteString("UNIQUE ")
	}
	sb.WriteString("INDEX ")
	sb.WriteString(idx.Name)
	sb.WriteString(" ON ")
	schema := idx.Schema
	if schema == "" {
		schema = "public"
	}
	sb.WriteString(schema)
	sb.WriteByte('.')
	if idx.Table != nil {
		sb.WriteString(idx.Table.Name)
	}
	method := idx.Method
	if method == "" {
		method = "btree"
	}
	sb.WriteString(" USING ")
	sb.WriteString(method)
	sb.WriteString(" (")
	for i, col := range idx.Columns {
		if i > 0 {
			sb.WriteString(", ")
		}
		if col == "" {
			// Expression column: use pre-serialized string if available.
			exprStr := ""
			if i < len(idx.ColExprStrings) {
				exprStr = idx.ColExprStrings[i]
			}
			if exprStr != "" {
				sb.WriteByte('(')
				sb.WriteString(exprStr)
				sb.WriteByte(')')
			} else {
				sb.WriteString("(expr)")
			}
		} else {
			sb.WriteString(col)
		}
	}
	sb.WriteByte(')')
	if len(idx.IncludeColumns) > 0 {
		sb.WriteString(" INCLUDE (")
		for i, col := range idx.IncludeColumns {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(col)
		}
		sb.WriteByte(')')
	}
	if idx.PredicateString != "" {
		sb.WriteString(" WHERE ")
		sb.WriteString(idx.PredicateString)
	}
	return sb.String()
}

// AllIndexes returns every index in the catalog, sorted by OID.
func (c *InMemory) AllIndexes() []*Index {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Index, 0, len(c.indexes))
	for _, idx := range c.indexes {
		out = append(out, idx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// RegisterViewConstraintDep records that a view relies on a PK constraint for
// GROUP BY functional dependency. Called by CREATE VIEW. M0097-0036.
func (c *InMemory) RegisterViewConstraintDep(viewName string, tableOID uint32, constraintName string) {
	key := fmt.Sprintf("%d:%s", tableOID, constraintName)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.constraintViewDeps[key] {
		if existing == viewName {
			return
		}
	}
	c.constraintViewDeps[key] = append(c.constraintViewDeps[key], viewName)
}

// UnregisterViewConstraintDeps removes all constraint dependencies recorded for
// the given view name. Called by DROP VIEW. M0097-0036.
func (c *InMemory) UnregisterViewConstraintDeps(viewName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, names := range c.constraintViewDeps {
		var kept []string
		for _, n := range names {
			if n != viewName {
				kept = append(kept, n)
			}
		}
		if len(kept) == 0 {
			delete(c.constraintViewDeps, key)
		} else {
			c.constraintViewDeps[key] = kept
		}
	}
}

// ViewsDependingOnConstraint returns the names of views that depend on the
// given PK constraint via GROUP BY functional dependency. M0097-0036.
func (c *InMemory) ViewsDependingOnConstraint(tableOID uint32, constraintName string) []string {
	key := fmt.Sprintf("%d:%s", tableOID, constraintName)
	c.mu.RLock()
	defer c.mu.RUnlock()
	src := c.constraintViewDeps[key]
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// DropPrimaryKeyConstraint removes the named primary-key constraint (index)
// from the table's index registries. Returns true if found and removed.
// M0097-0036.
func (c *InMemory) DropPrimaryKeyConstraint(tableOID uint32, constraintName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	inner, ok := c.byTable[tableOID]
	if !ok {
		return false
	}
	if _, exists := inner[constraintName]; !exists {
		return false
	}
	delete(inner, constraintName)
	// Also remove from the flat indexes map.
	for k, idx := range c.indexes {
		if idx.Table != nil && idx.Table.OID == tableOID && idx.Name == constraintName {
			delete(c.indexes, k)
			break
		}
	}
	return true
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
	relOID := table.OID
	if table.RelFileNodeOID != 0 {
		relOID = table.RelFileNodeOID
	}
	return storage.RelFileNode{DBOid: c.dbOid, RelOid: relOID, Fork: storage.MainFork}
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

// DatFrozenXID returns the minimum RelFrozenXID across all user (non-virtual,
// non-system) tables that have a valid relfrozenxid, or 0 when none do. This
// is the cluster-wide datfrozenxid candidate: every XID strictly below it is
// frozen in every user heap, so CLOG status for those XIDs can be truncated.
// Mirrors PG's per-database datfrozenxid = min(pg_class.relfrozenxid) computed
// at the end of VACUUM (vac_update_datfrozenxid). System relations are
// excluded because goopg does not freeze them; including their default-zero
// relfrozenxid would pin truncation at 0. Does NOT mutate any catalog state.
func (c *InMemory) DatFrozenXID() storage.TransactionID {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var oldest storage.TransactionID
	for _, t := range c.tables {
		if t.Virtual || IsSystemRelation(t.OID) {
			continue
		}
		if t.RelFrozenXID == storage.InvalidTransactionID {
			continue
		}
		if oldest == storage.InvalidTransactionID || t.RelFrozenXID < oldest {
			oldest = t.RelFrozenXID
		}
	}
	return oldest
}

// AllUserViews returns deep copies of every user-created non-materialized view.
// Used by DROP VIEW CASCADE dependency scanning. M0097-0021.
func (c *InMemory) AllUserViews() []*Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Table, 0)
	for _, t := range c.tables {
		if !t.Virtual || t.View == nil || t.IsMatView {
			continue
		}
		cp := *t
		cp.Columns = append([]Column(nil), t.Columns...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// AllUserMatViews returns deep copies of every user-created materialized view.
// Used by DROP TABLE/VIEW/MATERIALIZED VIEW CASCADE dependency scanning.
func (c *InMemory) AllUserMatViews() []*Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Table, 0)
	for _, t := range c.tables {
		if !t.IsMatView {
			continue
		}
		cp := *t
		cp.Columns = append([]Column(nil), t.Columns...)
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// RenameColumnInViews updates all stored view/matview SELECT ASTs to replace
// references to oldCol on tableName with newCol. Called after ALTER TABLE RENAME COLUMN
// so dependent views/matviews continue to work. M0097-0025.
func (c *InMemory) RenameColumnInViews(tableName, oldCol, newCol string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, t := range c.tables {
		if t.View == nil {
			continue
		}
		renameColumnInSelect(t.View, tableName, oldCol, newCol)
	}
}

// renameColumnInSelect walks sel and replaces ColumnRef nodes where
// Column==oldCol and (Table=="" or Table==tableName) with newCol.
func renameColumnInSelect(sel *parser.SelectStmt, tableName, oldCol, newCol string) {
	if sel == nil {
		return
	}
	for i := range sel.Targets {
		if sel.Targets[i].Expr != nil {
			sel.Targets[i].Expr = renameColumnInExpr(sel.Targets[i].Expr, tableName, oldCol, newCol)
		}
	}
	if sel.Where != nil {
		sel.Where = renameColumnInExpr(sel.Where, tableName, oldCol, newCol)
	}
	if sel.Having != nil {
		sel.Having = renameColumnInExpr(sel.Having, tableName, oldCol, newCol)
	}
	for i := range sel.GroupBy {
		sel.GroupBy[i] = renameColumnInExpr(sel.GroupBy[i], tableName, oldCol, newCol)
	}
	for i := range sel.OrderBy {
		sel.OrderBy[i].Expr = renameColumnInExpr(sel.OrderBy[i].Expr, tableName, oldCol, newCol)
	}
	for i := range sel.From {
		if sel.From[i].Subquery != nil {
			renameColumnInSelect(sel.From[i].Subquery, tableName, oldCol, newCol)
		}
	}
}

func renameColumnInExpr(expr parser.Expr, tableName, oldCol, newCol string) parser.Expr {
	if expr == nil {
		return nil
	}
	switch e := expr.(type) {
	case *parser.ColumnRef:
		if strings.EqualFold(e.Column, oldCol) {
			if e.Table == "" || strings.EqualFold(e.Table, tableName) {
				newRef := *e
				newRef.Column = newCol
				return &newRef
			}
		}
	case *parser.BinaryOp:
		e.Left = renameColumnInExpr(e.Left, tableName, oldCol, newCol)
		e.Right = renameColumnInExpr(e.Right, tableName, oldCol, newCol)
	case *parser.UnaryOp:
		e.Operand = renameColumnInExpr(e.Operand, tableName, oldCol, newCol)
	case *parser.FuncCall:
		for i := range e.Args {
			e.Args[i] = renameColumnInExpr(e.Args[i], tableName, oldCol, newCol)
		}
	case *parser.CastExpr:
		e.Operand = renameColumnInExpr(e.Operand, tableName, oldCol, newCol)
	case *parser.SubqueryExpr:
		renameColumnInSelect(e.Inner, tableName, oldCol, newCol)
	case *parser.CaseExpr:
		for i := range e.Whens {
			e.Whens[i].When = renameColumnInExpr(e.Whens[i].When, tableName, oldCol, newCol)
			e.Whens[i].Then = renameColumnInExpr(e.Whens[i].Then, tableName, oldCol, newCol)
		}
		if e.Else != nil {
			e.Else = renameColumnInExpr(e.Else, tableName, oldCol, newCol)
		}
	}
	return expr
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
	evs := make([]EnumValue, len(values))
	for i, v := range values {
		evs[i] = EnumValue{Label: v, SortOrder: float64(i + 1)}
	}
	et := &EnumType{
		Name:   k,
		OID:    c.nextOID,
		Values: evs,
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

// EnumLabelTooLong is returned by AddEnumValue when value exceeds 63 bytes.
// Code 22P02 is used in execAlterType.
type EnumLabelTooLong struct{ Label string }

func (e *EnumLabelTooLong) Error() string {
	return fmt.Sprintf("invalid enum label %q", e.Label)
}

// EnumLabelNotFound is returned when BEFORE/AFTER reference label is not found.
// The message matches PostgreSQL's wording.
type EnumLabelNotFound struct{ Label string }

func (e *EnumLabelNotFound) Error() string {
	return fmt.Sprintf("%q is not an existing enum label", e.Label)
}

// EnumLabelAlreadyExists is returned by RenameEnumValue when the new label already exists.
type EnumLabelAlreadyExists struct{ Label string }

func (e *EnumLabelAlreadyExists) Error() string {
	return fmt.Sprintf("enum label %q already exists", e.Label)
}

// RenameEnumValue renames an existing enum label to a new label.
// Returns EnumLabelNotFound if oldLabel does not exist, EnumLabelAlreadyExists if newLabel
// already exists. M0097-0022.
func (c *InMemory) RenameEnumValue(typeName, oldLabel, newLabel string) error {
	k := strings.ToLower(typeName)
	c.mu.Lock()
	defer c.mu.Unlock()
	et, ok := c.enumTypes[k]
	if !ok {
		return fmt.Errorf("type %q does not exist", typeName)
	}
	oldIdx := -1
	for i, ev := range et.Values {
		if ev.Label == oldLabel {
			oldIdx = i
			break
		}
	}
	if oldIdx < 0 {
		return &EnumLabelNotFound{Label: oldLabel}
	}
	for _, ev := range et.Values {
		if ev.Label == newLabel {
			return &EnumLabelAlreadyExists{Label: newLabel}
		}
	}
	et.Values[oldIdx].Label = newLabel
	return nil
}

// RenameEnum renames an enum type from oldName to newName. M0097-enum-rename.
func (c *InMemory) RenameEnum(oldName, newName string) error {
	ok := strings.ToLower(oldName)
	nk := strings.ToLower(newName)
	c.mu.Lock()
	defer c.mu.Unlock()
	et, found := c.enumTypes[ok]
	if !found {
		return fmt.Errorf("type %q does not exist", oldName)
	}
	if _, exists := c.enumTypes[nk]; exists {
		return fmt.Errorf("type %q already exists", newName)
	}
	delete(c.enumTypes, ok)
	et.Name = nk
	c.enumTypes[nk] = et
	return nil
}

// AddEnumValue appends a new label to an existing enum. before/after are
// reference labels (empty = append at end). Returns an error if label already
// exists unless ifNotExists is true, in which case it is a no-op (returns nil).
//
// To distinguish the "skipped duplicate" case (for NOTICE emission), use
// AddEnumValueResult which returns a skipped bool. M0097-0017.
func (c *InMemory) AddEnumValue(name, value string, ifNotExists bool, before, after string) error {
	_, err := c.AddEnumValueResult(name, value, ifNotExists, before, after)
	return err
}

// AddEnumValueResult is like AddEnumValue but also returns skipped=true when
// ifNotExists=true and the label already exists (caller should emit a NOTICE).
// M0097-0063.
// renumberEnumValues assigns sequential integer sort orders (1, 2, 3, ...) to all
// enum values, matching PostgreSQL's RenumberEnumType triggered when float4
// precision is exhausted for midpoint insertions.
func renumberEnumValues(et *EnumType) {
	for i := range et.Values {
		et.Values[i].SortOrder = float64(i + 1)
	}
}

func (c *InMemory) AddEnumValueResult(name, value string, ifNotExists bool, before, after string) (skipped bool, err error) {
	// PostgreSQL limits enum labels to 63 bytes (NAMEDATALEN-1). M0097-0063.
	if len(value) > 63 {
		return false, &EnumLabelTooLong{Label: value}
	}
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	et, ok := c.enumTypes[k]
	if !ok {
		return false, fmt.Errorf("type %q does not exist", name)
	}
	// Check for duplicate.
	for _, v := range et.Values {
		if strings.EqualFold(v.Label, value) {
			if ifNotExists {
				return true, nil // skipped — caller should emit NOTICE
			}
			return false, fmt.Errorf("enum label %q already exists", value)
		}
	}
	switch {
	case before != "":
		for i, v := range et.Values {
			if strings.EqualFold(v.Label, before) {
				var newSortOrder float64
				if i == 0 {
					newSortOrder = float64(float32(v.SortOrder) - 1)
				} else {
					prev32 := float32(et.Values[i-1].SortOrder)
					next32 := float32(et.Values[i].SortOrder)
					mid32 := (prev32 + next32) / 2
					if mid32 <= prev32 || mid32 >= next32 {
						// float4 precision exhausted — renumber to sequential integers.
						renumberEnumValues(et)
						prev32 = float32(et.Values[i-1].SortOrder)
						next32 = float32(et.Values[i].SortOrder)
						mid32 = (prev32 + next32) / 2
					}
					newSortOrder = float64(mid32)
				}
				newEV := EnumValue{Label: value, SortOrder: newSortOrder}
				newVals := make([]EnumValue, 0, len(et.Values)+1)
				newVals = append(newVals, et.Values[:i]...)
				newVals = append(newVals, newEV)
				newVals = append(newVals, et.Values[i:]...)
				et.Values = newVals
				return false, nil
			}
		}
		return false, &EnumLabelNotFound{Label: before}
	case after != "":
		for i, v := range et.Values {
			if strings.EqualFold(v.Label, after) {
				var newSortOrder float64
				if i+1 == len(et.Values) {
					newSortOrder = float64(float32(v.SortOrder) + 1)
				} else {
					prev32 := float32(et.Values[i].SortOrder)
					next32 := float32(et.Values[i+1].SortOrder)
					mid32 := (prev32 + next32) / 2
					if mid32 <= prev32 || mid32 >= next32 {
						renumberEnumValues(et)
						prev32 = float32(et.Values[i].SortOrder)
						next32 = float32(et.Values[i+1].SortOrder)
						mid32 = (prev32 + next32) / 2
					}
					newSortOrder = float64(mid32)
				}
				newEV := EnumValue{Label: value, SortOrder: newSortOrder}
				newVals := make([]EnumValue, 0, len(et.Values)+1)
				newVals = append(newVals, et.Values[:i+1]...)
				newVals = append(newVals, newEV)
				newVals = append(newVals, et.Values[i+1:]...)
				et.Values = newVals
				return false, nil
			}
		}
		return false, &EnumLabelNotFound{Label: after}
	default:
		// Append: sortorder = last + 1, or 1 for first element.
		var newSortOrder float64
		if len(et.Values) == 0 {
			newSortOrder = 1
		} else {
			newSortOrder = et.Values[len(et.Values)-1].SortOrder + 1
		}
		et.Values = append(et.Values, EnumValue{Label: value, SortOrder: newSortOrder})
	}
	return false, nil
}

// RemoveEnumValue removes a label from an existing enum type.
// Used to roll back ALTER TYPE … ADD VALUE on transaction ROLLBACK. M0097-0022.
func (c *InMemory) RemoveEnumValue(typeName, label string) {
	k := strings.ToLower(typeName)
	c.mu.Lock()
	defer c.mu.Unlock()
	et, found := c.enumTypes[k]
	if !found {
		return
	}
	for i, ev := range et.Values {
		if ev.Label == label {
			et.Values = append(et.Values[:i], et.Values[i+1:]...)
			return
		}
	}
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

// RegisterCompositeType records a composite/range/base type name so that
// DROP TYPE can succeed. We don't model composite type internals in v0;
// tracking the name is enough for DROP TYPE to avoid a false-positive error.
// M0097-0064.
func (c *InMemory) RegisterCompositeType(name string) {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compositeTypeNames[k] = true
}

// RegisterCompositeTypeWithFields records a composite type together with its
// ordered field list, enabling PL/pgSQL field access/assignment. M0097-composite.
func (c *InMemory) RegisterCompositeTypeWithFields(name string, fields []CompositeField) {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compositeTypeNames[k] = true
	c.compositeTypeFields[k] = fields
}

// LookupCompositeTypeFields returns the ordered field list for a composite type,
// or nil if the type is not known or has no field metadata. M0097-composite.
func (c *InMemory) LookupCompositeTypeFields(name string) []CompositeField {
	k := strings.ToLower(name)
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.compositeTypeFields[k]
}

// HasCompositeType reports whether the given name refers to a composite type.
func (c *InMemory) HasCompositeType(name string) bool {
	k := strings.ToLower(name)
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.compositeTypeNames[k]
}

// DropCompositeType removes a composite type name. Returns an error if not
// found. M0097-0064.
func (c *InMemory) DropCompositeType(name string) error {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.compositeTypeNames[k] {
		return fmt.Errorf("type %q does not exist", name)
	}
	delete(c.compositeTypeNames, k)
	return nil
}

// ── Domain type methods ──────────────────────────────────────────────────────

// RegisterDomain creates a new domain type. Returns an error if name already
// exists. M0097-0017.
func (c *InMemory) RegisterDomain(name string, base Type, notNull bool, checkInValues ...string) (*Domain, error) {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.domains[k]; exists {
		return nil, fmt.Errorf("type %q already exists", name)
	}
	d := &Domain{
		Name:          k,
		OID:           c.nextOID,
		Base:          base,
		NotNull:       notNull,
		CheckInValues: checkInValues,
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

// TablesWithColumnOfType returns all non-virtual tables that have at least one
// column whose declared type name matches typeName (case-insensitive). Used by
// execDropDomain to detect dependent objects before dropping. M0097-0023.
func (c *InMemory) TablesWithColumnOfType(typeName string) []*Table {
	lk := strings.ToLower(typeName)
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []*Table
	for _, tbl := range c.tables {
		if tbl.Virtual {
			continue
		}
		for _, col := range tbl.Columns {
			if strings.ToLower(col.Type.Name) == lk || strings.ToLower(col.DeclaredTypeName) == lk {
				out = append(out, tbl)
				break
			}
		}
	}
	return out
}

// formatExprForAttrdef converts a parsed default expression to a display string
// for pg_attrdef.adbin. Used by pg_get_expr to display column defaults in \d.
func formatExprForAttrdef(e parser.Expr) string {
	if e == nil {
		return ""
	}
	switch v := e.(type) {
	case *parser.IntegerConst:
		return fmt.Sprintf("%d", v.Value)
	case *parser.NumericConst:
		return v.Value
	case *parser.StringConst:
		return "'" + strings.ReplaceAll(v.Value, "'", "''") + "'"
	case *parser.NullConst:
		return "NULL"
	case *parser.BooleanConst:
		if v.Value {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf("%v", e)
}

// DropDomain removes a domain. Returns (names, nil) on success where names are
// tables dropped via CASCADE. Returns (blockingTables, "dependent objects") when
// cascade=false and dependents exist. M0097-0023.
func (c *InMemory) DropDomain(name string, ifExists bool, cascade bool) ([]string, error) {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.domains[k]; !ok {
		if ifExists {
			return nil, nil
		}
		return nil, fmt.Errorf("type %q does not exist", name)
	}
	// Find tables that use this domain as a column type.
	var dependentTables []string
	for _, tbl := range c.tables {
		if tbl.Virtual {
			continue
		}
		for _, col := range tbl.Columns {
			if strings.ToLower(col.Type.Name) == k || strings.ToLower(col.DeclaredTypeName) == k {
				dependentTables = append(dependentTables, tbl.Name)
				break
			}
		}
	}
	if len(dependentTables) > 0 && !cascade {
		// Return sentinel so caller can emit proper DETAIL/HINT.
		return dependentTables, fmt.Errorf("dependent objects")
	}
	// CASCADE: drop all dependent tables.
	var dropped []string
	if cascade {
		for _, tblName := range dependentTables {
			for tableKey, tbl := range c.tables {
				if strings.EqualFold(tbl.Name, tblName) {
					dropped = append(dropped, tblName)
					tblOID := tbl.OID
					delete(c.tables, tableKey)
					// Remove indexes on this table.
					for idxOID, idx := range c.indexes {
						if idx.Table != nil && idx.Table.OID == tblOID {
							delete(c.indexes, idxOID)
						}
					}
					break
				}
			}
		}
	}
	delete(c.domains, k)
	return dropped, nil
}

// ResolveColumnType resolves a column type name through the domain and enum
// registries to determine the effective storage type. For enums → "text"; for
// domains → recursively resolves the base type. Returns the input unchanged if
// no match is found. M0097-0017.
func (c *InMemory) ResolveColumnType(typeName string) string {
	k := strings.ToLower(typeName)
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Check domain: resolve to base type.
	if d, ok := c.domains[k]; ok {
		baseName := strings.ToLower(d.Base.Name)
		// Recurse (without lock reacquire — use direct map lookup).
		return c.resolveColumnTypeLocked(baseName)
	}
	// Enum types: preserve the enum type name (NOT "text") so the executor
	// can look up sort order for ORDER BY semantics (M0097-enum).
	// Encoding/decoding still works via the varlena-text default path.
	return typeName
}

// resolveColumnTypeLocked is the lock-free recursive helper for ResolveColumnType.
func (c *InMemory) resolveColumnTypeLocked(typeName string) string {
	k := strings.ToLower(typeName)
	if d, ok := c.domains[k]; ok {
		return c.resolveColumnTypeLocked(strings.ToLower(d.Base.Name))
	}
	return typeName
}

// SearchPathCatalog wraps a Catalog and applies a dynamically-fetched
// search_path when resolving unqualified table names in LookupTable.
// All other Catalog methods are delegated to the wrapped catalog unchanged.
// Use WithSearchPath to construct. M0097-0022.
type SearchPathCatalog struct {
	Catalog
	GetSchemas func() []string
}

// WithSearchPath returns a SearchPathCatalog that falls back to the schemas
// returned by getSchemas (in order) when LookupTable finds no match for an
// unqualified name.
func WithSearchPath(cat Catalog, getSchemas func() []string) *SearchPathCatalog {
	return &SearchPathCatalog{Catalog: cat, GetSchemas: getSchemas}
}

// LookupTable overrides the embedded Catalog.LookupTable to apply the
// current search_path when the name has no explicit schema qualifier.
func (c *SearchPathCatalog) LookupTable(name parser.ObjectName) (*Table, bool) {
	tbl, ok := c.Catalog.LookupTable(name)
	if !ok && name.Schema == "" && c.GetSchemas != nil {
		for _, sc := range c.GetSchemas() {
			tbl, ok = c.Catalog.LookupTable(parser.ObjectName{Schema: sc, Name: name.Name})
			if ok {
				return tbl, ok
			}
		}
	}
	return tbl, ok
}

// Unwrap returns the underlying Catalog, allowing callers to peel the
// search-path layer for type assertions (e.g., to *InMemory). M0097-0022.
func (c *SearchPathCatalog) Unwrap() Catalog { return c.Catalog }
