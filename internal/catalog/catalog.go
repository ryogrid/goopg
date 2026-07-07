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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/sqlkeywords"
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

// SeqParams carries one sequence's pg_sequence parameter row, supplied by the
// executor (which owns the runtime sequence registry) so the catalog can build
// pg_sequence rows without importing the executor package. M0110-0001 (DU-002
// slice 115).
type SeqParams struct {
	TypeOID   uint32 // seqtypid: 21 smallint / 23 integer / 20 bigint
	Start     int64
	Increment int64
	Max       int64
	Min       int64
	Cache     int64
	Cycle     bool
	// OwnedBy is the lowercased "table.column" (optionally "schema.table.column")
	// recorded by ALTER/CREATE SEQUENCE ... OWNED BY, or "" for a standalone
	// sequence. The catalog uses it to synthesize the pg_depend AUTO ('a') row
	// that pg_dump's getTables LEFT JOIN reads to emit `ALTER SEQUENCE ... OWNED
	// BY`. M0110-0001 (DU-002 slice 118).
	OwnedBy string
}

// SequenceParamsFunc is set by the executor at init. Given a sequence's
// schema-qualified name (e.g. "public.s") it returns that sequence's
// pg_sequence parameters. nil until the executor registers it (catalog-only
// unit tests then see an empty pg_sequence). M0110-0001 (DU-002 slice 115).
var SequenceParamsFunc func(qualifiedName string) (SeqParams, bool)

// Type is the textual type tag plus an optional typmod argument list.
// v0 keeps types as strings so the planner doesn't need a real type
// system; the executor casts based on Type.Name until the type system
// lands.
type Type struct {
	Name string
	Args []int64
	// IsArray is true for a column declared with the SQL `[]` array suffix
	// (e.g. `tags text[]`). Name still holds the element type ("text"); the
	// array-ness is tracked separately so the runtime evaluator keeps using
	// the element type while catalog builders (pg_attribute.atttypid →
	// _text/_int4/…) and pg_dump's format_type render the array type.
	// DU-002 slice 62.
	IsArray bool
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
	// GeneratedVirtual records the declared storage strategy of a generated
	// column: true for `GENERATED ALWAYS AS (expr) VIRTUAL` (and the bare
	// `GENERATED ALWAYS AS (expr)` form, whose PG18 default is VIRTUAL), false
	// for an explicit STORED. goopg materializes every generated column on write
	// (STORED storage semantics) regardless of this flag — it exists only so
	// pg_attribute.attgenerated reports the PG-faithful discriminator ('v' vs
	// 's') and pg_dump re-emits the original keyword. DU-002 slice 194.
	GeneratedVirtual bool
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
	// IdentityAlways distinguishes the identity KIND: true for GENERATED ALWAYS
	// (pg_attribute.attidentity='a'), false for GENERATED BY DEFAULT (='d').
	// Only meaningful when IdentityColumn is true. pg_dump reads attidentity to
	// emit `ADD GENERATED ALWAYS|BY DEFAULT AS IDENTITY`. M0110-0001 (DU-002 slice 120).
	IdentityAlways bool
	// IdentityStart is the START WITH value from the sequence options (0 = use default=1).
	IdentityStart int64
	// IdentityIncrement/Min/Max/Cache hold the remaining identity sequence options
	// (nil = type/PG default); IdentityCycle records CYCLE. Threaded to the backing
	// sequence so pg_dump's ADD GENERATED ... AS IDENTITY (...) round-trips the
	// non-default INCREMENT BY / MINVALUE / MAXVALUE / CACHE / CYCLE. DU-002.
	IdentityIncrement *int64
	IdentityMin       *int64
	IdentityMax       *int64
	IdentityCache     *int64
	IdentityCycle     bool
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
	// Compression is the column's per-column TOAST compression method as written
	// in `COMPRESSION <method>` (CREATE TABLE) or `ALTER COLUMN ... SET COMPRESSION
	// <method>` — "pglz" or "lz4". Empty means no explicit method (PG stores
	// attcompression='\0', meaning the default_toast_compression GUC applies, and
	// pg_dump emits no SET COMPRESSION clause). goopg does not actually TOAST or
	// compress; this is recorded purely so the column round-trips through pg_dump.
	// DU-002 slice 183.
	Compression string
	// StatTarget is the per-column statistics target set via `ALTER COLUMN ...
	// SET STATISTICS <n>`. nil means unset — PG stores attstattarget=NULL (the
	// default, encoded as -1 to clients) and pg_dump emits no SET STATISTICS
	// clause. A non-nil value >= 0 makes pg_dump re-emit `ALTER TABLE ONLY ...
	// SET STATISTICS <n>`. goopg does not sample at a per-column granularity;
	// this is recorded purely so the column round-trips through pg_dump.
	// DU-002 slice 184.
	StatTarget *int
	// Options holds per-column attribute options set via `ALTER COLUMN ...
	// SET (opt=value, …)` (e.g. "n_distinct=0.5"), each normalized to PG's
	// stored `name=value` form. nil/empty means none — PG stores
	// pg_attribute.attoptions=NULL and pg_dump emits no SET (...) clause. A
	// non-empty list is rendered into the attoptions text-array literal so
	// pg_dump re-emits `ALTER TABLE ONLY ... ALTER COLUMN ... SET (...)`. goopg
	// does not act on these planner statistics hints; recorded purely so the
	// column round-trips through pg_dump. DU-002 slice 185.
	Options []string
	// Collation is the explicit collation name from a column-level `COLLATE
	// <name>` clause — e.g. "C", "POSIX", "ucs_basic" (bare collname, matching
	// pg_collation.collname). Empty means none, in which case pg_attribute.
	// attcollation echoes the column type's typcollation and pg_dump emits no
	// COLLATE clause. When set (and the type is collatable), the synthesized
	// pg_attribute row reports the resolved collation OID so pg_dump's
	// `CASE WHEN a.attcollation <> t.typcollation` test fires and it re-emits
	// `COLLATE <schema>.<name>` inline in the CREATE TABLE column list. goopg
	// does not actually collate; recorded purely for pg_dump round-trip
	// fidelity. DU-002 slice 188.
	Collation string
	// FDWOptions holds per-column foreign-table options set via a
	// `CREATE FOREIGN TABLE (col type OPTIONS (name 'value', …))` clause,
	// each normalized to PG's stored `name=value` form (matching Options'
	// attoptions convention). nil/empty means none — PG stores
	// pg_attribute.attfdwoptions=NULL and pg_dump emits no per-column OPTIONS
	// clause. A non-empty list is rendered into the attfdwoptions text-array
	// literal so pg_dump re-emits `ALTER FOREIGN TABLE ONLY ... ALTER COLUMN
	// ... OPTIONS (...)`. Only meaningful on a foreign table's columns.
	// DU-002 slice 418.
	FDWOptions []string
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
	NotValid  bool   // convalidated='f': added NOT VALID, existing rows not checked yet
	// NotEnforced mirrors pg_constraint.conenforced='f' (PG18 NOT ENFORCED
	// constraints). pg_get_constraintdef_worker checks this FIRST and, when
	// true, appends the trailing ` NOT ENFORCED` regardless of NotValid
	// (PostgreSQL considers validated status irrelevant once a constraint
	// isn't enforced — ruleutils.c's `if (!conenforced) ... else if
	// (!convalidated) ...`). Real PG also implicitly leaves such a constraint
	// unvalidated (skip_validation=!is_enforced in tablecmds.c), so goopg
	// treats NotEnforced as implying "not validated" wherever convalidated is
	// projected. DU-002 slice 430.
	NotEnforced bool
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
	InhCount  int    // coninhcount: how many parents (plain-INHERITS or partition) enforce this NOT NULL
}

// AddNotNull appends a named NOT NULL constraint to the table.
// isLocal=true means the constraint is locally declared (attislocal or explicitly
// re-declared); inhCount counts enforcing parents — 0 for a purely local
// constraint, 1 for one inheriting/partition parent that also enforces it.
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
	t.AddCheckFull(name, expr, oid, false, false, false)
}

// AddCheckWithNotValid is AddCheck for a CHECK added with NOT VALID
// (pg_constraint.convalidated='f'): existing rows were not scanned, so the
// constraint is enforced only for new writes until VALIDATE CONSTRAINT runs.
// PG's pg_get_constraintdef_worker appends a trailing ` NOT VALID` for such a
// constraint, and pg_dump dumps it as a separate ALTER TABLE ADD CONSTRAINT so
// possibly-violating data loads before the constraint. DU-002 slice 308.
func (t *Table) AddCheckWithNotValid(name, expr string, oid uint32, notValid bool) {
	t.AddCheckFull(name, expr, oid, notValid, false, false)
}

// AddCheckWithNoInherit is AddCheck for a CHECK that may carry NO INHERIT
// (PG18 connoinherit='t'). An anonymous table-level `CHECK (...) NO INHERIT`
// must record the flag so pg_get_constraintdef re-emits the ` NO INHERIT`
// suffix on dump and pg_constraint reports connoinherit. DU-002 slice 128.
func (t *Table) AddCheckWithNoInherit(name, expr string, oid uint32, noInherit bool) {
	t.AddCheckFull(name, expr, oid, false, noInherit, false)
}

// AddCheckFull is the fully-parameterized CHECK constraint registration
// underlying AddCheck/AddCheckWithNotValid/AddCheckWithNoInherit, threading
// PG18's NOT VALID / NO INHERIT / NOT ENFORCED flags (pg_constraint's
// convalidated / connoinherit / conenforced columns) through a single call
// site so callers that need more than one flag at once (e.g. ALTER TABLE ADD
// CONSTRAINT CHECK ... NOT ENFORCED) don't need a fresh Add* wrapper.
// DU-002 slice 430.
func (t *Table) AddCheckFull(name, expr string, oid uint32, notValid, noInherit, notEnforced bool) {
	t.CheckConstraints = append(t.CheckConstraints, expr)
	t.NamedChecks = append(t.NamedChecks, NamedCheckConstraint{
		Name: name, Expr: expr, OID: oid, IsLocal: true,
		NotValid: notValid, NoInherit: noInherit, NotEnforced: notEnforced,
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
	// ViewDef holds the raw SQL text of the view body (the SELECT after
	// `AS`), captured verbatim at parse time. pg_get_viewdef returns it so
	// pg_dump can reconstruct `CREATE VIEW … AS <body>`. Empty for non-views.
	// Faithful to the literal text the user wrote; schema-qualification of
	// unqualified relation references (which PG's deparser adds) is NOT
	// performed — a known fidelity gap tracked in the pg_dump TAP port.
	ViewDef string

	// CheckOption captures a view's `WITH [CASCADED|LOCAL] CHECK OPTION` clause:
	// "cascaded", "local", or "" (no clause). PostgreSQL records it as the
	// `check_option=<mode>` pg_class.reloption; pg_dump's getTables strips that
	// element from the reloptions array and instead re-emits the
	// `WITH <MODE> CHECK OPTION` view-definition suffix. M0119-0004 (DU-002 slice 365).
	// goopg does not yet ENFORCE the option on INSERT/UPDATE through the view.
	CheckOption string

	// SecurityBarrier / SecurityBarrierSet capture a view's
	// `WITH (security_barrier = <bool>)` storage option. PostgreSQL records it
	// as the `security_barrier=<bool>` pg_class.reloption; unlike check_option,
	// pg_dump's getTables keeps it in the reloptions array (array_remove strips
	// only check_option=*) and re-emits it as the `WITH (security_barrier='true')`
	// clause after the view name (appendReloptionsArray). SecurityBarrierSet
	// guards whether the option was specified, since false is a meaningful value.
	// M0119-0004 (DU-002 slice 366).
	SecurityBarrier    bool
	SecurityBarrierSet bool

	// SecurityInvoker / SecurityInvokerSet capture a view's
	// `WITH (security_invoker = <bool>)` storage option. PostgreSQL records it
	// as the `security_invoker=<bool>` pg_class.reloption; like security_barrier,
	// pg_dump's getTables keeps it in the reloptions array (array_remove strips
	// only check_option=*) and re-emits it as the `WITH (security_invoker='true')`
	// clause after the view name (appendReloptionsArray). SecurityInvokerSet
	// guards whether the option was specified, since false is a meaningful value.
	// M0119-0004 (DU-002 slice 367).
	SecurityInvoker    bool
	SecurityInvokerSet bool

	// Stats holds the most recent ANALYZE output for this
	// table. nil before ANALYZE has run; the planner treats nil
	// as "no statistics yet" and falls back to the legacy
	// rules-only join order. Mirrors upstream's pg_class
	// reltuples / relpages plus per-column pg_statistic data.
	Stats *TableStats

	// OfTypeOID is the pg_type OID of the composite type a typed table was
	// declared `OF` (`CREATE TABLE name OF type_name`). Zero for ordinary
	// tables. Surfaced as pg_class.reloftype so pg_dump re-emits the `OF
	// type_name` form and suppresses the (type-derived) column list. The
	// columns themselves are materialized normally (attislocal=true,
	// matching PG, which skips them on dump via the reloftype check, not
	// attislocal). DU-002 slice 374.
	OfTypeOID uint32

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

	// IsDMLCTE marks a synthetic table created from a data-modifying CTE
	// (WITH ... AS (UPDATE/DELETE/INSERT)). The analyzer uses this to allow
	// any column reference on the CTE in FROM/USING clauses without strict
	// column-existence validation — the planner resolves column types from
	// the materialized CTE result at execution time.
	IsDMLCTE bool

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

	// ForeignServerName marks this table as a foreign table (`CREATE FOREIGN
	// TABLE ... SERVER <name>`), giving it relkind='f'. Empty for an ordinary
	// table. DU-002 slice 417.
	ForeignServerName string
	// ForeignOptions holds the table-level OPTIONS as "name=value" elements —
	// the pg_foreign_table.ftoptions text[] representation pg_dump's getTables
	// reads via pg_options_to_table. DU-002 slice 417.
	ForeignOptions []string

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
	// DetachPendingEpoch, when non-zero, marks this partition child as
	// "detach pending" by an in-progress ALTER TABLE … DETACH PARTITION …
	// CONCURRENTLY: the partition is still physically registered (so a
	// snapshot taken before the detach still scans it) but is omitted from
	// the partition descriptor for any statement whose snapshot epoch is
	// >= DetachPendingEpoch. It mirrors PostgreSQL's pg_inherits
	// inhdetachpending flag + the snapshot-relative omission performed by
	// find_inheritance_children_extended. Set/cleared via
	// InMemory.MarkPartitionDetachPending / ClearPartitionDetachPending and
	// consulted by VisiblePartitionChildren. Zero for every ordinary
	// partition. Design 0118-0058 (M0118-0008 detach-partition-concurrently).
	DetachPendingEpoch uint64
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

	// InheritsParentOIDs lists the OIDs of the direct parents this table was
	// created from via `CREATE TABLE child (...) INHERITS (parent, ...)`, in
	// declaration order. Distinct from PartitionParentOID (a partition child is
	// not legacy-inheritance). Populates pg_inherits rows so pg_dump re-emits the
	// `INHERITS (...)` clause and omits the parent's columns. DU-002 slice 170.
	InheritsParentOIDs []uint32

	// Unlogged / Temp track relpersistence. 'u' for UNLOGGED, 't' for TEMP, 'p' for permanent.
	Unlogged bool
	Temp     bool

	// ReplicaIdentity is the single-char pg_class.relreplident code set by
	// `ALTER TABLE ... REPLICA IDENTITY {DEFAULT|FULL|NOTHING|USING INDEX idx}`:
	// 'd' (DEFAULT, the implicit value), 'f' (FULL), 'n' (NOTHING), 'i' (USING
	// INDEX). Empty is treated as 'd'. pg_dump emits an `ALTER TABLE ONLY ...
	// REPLICA IDENTITY ...` clause whenever this is not 'd' (pg_dump.c
	// dumpTableSchema). goopg has no logical replication, so this is round-trip
	// fidelity only. Use ReplIdentOrDefault to resolve the effective code.
	// DU-002 slice 305.
	ReplicaIdentity string

	// RowSecurity mirrors pg_class.relrowsecurity: true once `ALTER TABLE ...
	// ENABLE ROW LEVEL SECURITY` has run (reset by DISABLE). pg_dump emits an
	// `ALTER TABLE <t> ENABLE ROW LEVEL SECURITY;` clause whenever this is set
	// (pg_dump.c getPolicies represents an RLS-enabled table with a null-polname
	// PolicyInfo). goopg enforces no row-level security — this is round-trip
	// schema fidelity only. DU-002 slice 322.
	RowSecurity bool
	// ForceRowSecurity mirrors pg_class.relforcerowsecurity: true once `ALTER
	// TABLE ... FORCE ROW LEVEL SECURITY` has run (reset by NO FORCE). pg_dump
	// emits an `ALTER TABLE ONLY <t> FORCE ROW LEVEL SECURITY;` clause whenever
	// this is set (pg_dump.c dumpTableSchema). Round-trip fidelity only. DU-002
	// slice 322.
	ForceRowSecurity bool

	// Policies holds the row-level security policies declared on this table via
	// CREATE POLICY. goopg does NOT enforce RLS; the policies are recorded so
	// they round-trip through pg_dump (the pg_policy virtual catalog →
	// dumpPolicy). DU-002 slice 323.
	Policies []PolicyInfo

	// Rules holds the unconditional DO-NOTHING query-rewrite rules declared on
	// this table via CREATE RULE. goopg does NOT implement the rewrite system;
	// the rules are recorded so they round-trip through pg_dump (the pg_rewrite
	// virtual catalog → pg_get_ruledef → dumpRule). DU-002 slice 324.
	Rules []RuleInfo

	// TempOwner identifies the session that owns this temporary relation. In
	// PostgreSQL every backend has its own temp namespace (pg_temp_N); a temp
	// relation is only part of *its* backend's catalog. goopg keeps all
	// relations in one shared in-memory catalog, so we record the owning
	// session's stable token here to recover that per-session isolation. It is
	// empty for permanent/unlogged tables and for temp tables created without a
	// session identity (internal/test contexts). Consumers must treat an
	// empty-owner temp relation as visible to all sessions to preserve legacy
	// single-session behaviour. See AccessibleInheritanceChildren and design
	// 0118-0036 (RELATION_IS_OTHER_TEMP inheritance exclusion).
	TempOwner string

	// Owner records the table's owning role NAME, as set by
	// `ALTER TABLE ... OWNER TO role`. Empty means the bootstrap superuser (OID
	// 10) — goopg's default for every freshly created relation. It drives the
	// ownership half of the VACUUM/ANALYZE/CLUSTER maintenance-privilege check
	// (vacuum_is_permitted_to_vacuum): a non-superuser session (SET ROLE) may
	// only run those commands on a relation it owns (or holds MAINTAIN on),
	// otherwise the command skips the relation with a WARNING. Stored as the role
	// name (case-insensitive) to compare against Context.NonSuperuserRole. The
	// pg_class.relowner OID column is still rendered as the bootstrap superuser
	// (catalog/dump output unaffected). M0118-0008 (vacuum-conflict / cluster-conflict).
	Owner string

	// Fillfactor stores the table's `WITH (fillfactor=N)` storage parameter
	// (10–100). Zero means unset (PG's default 100 / no reloptions). pg_class's
	// reloptions cell surfaces this as the text[] element `fillfactor=N`, which
	// pg_dump renders back as `WITH (fillfactor='N')`. M0110-0001 (DU-002 slice 54).
	Fillfactor int

	// ParallelWorkers stores the table's `WITH (parallel_workers=N)` storage
	// parameter (0–1024). Unlike fillfactor, 0 is a valid explicit value (PG's
	// reloption default is -1 = unset), so ParallelWorkersSet — not a zero check
	// — guards whether the option was specified. When set, pg_class.reloptions
	// gains the text[] element `parallel_workers=N`, which pg_dump renders back
	// as `WITH (parallel_workers='N')`. goopg has no parallel query, so the value
	// is catalog/dump-only (advisory; runtime unaffected). M0110-0001 (DU-002 slice 195).
	ParallelWorkers    int
	ParallelWorkersSet bool

	// AutovacuumEnabled stores the table's `WITH (autovacuum_enabled=BOOL)`
	// storage parameter. Like parallel_workers, the value itself (true/false)
	// carries no default that a zero check could detect, so AutovacuumEnabledSet
	// guards whether the option was specified. When set, pg_class.reloptions
	// gains the text[] element `autovacuum_enabled=true|false`, which pg_dump
	// renders back as `WITH (autovacuum_enabled='true'|'false')`. goopg has no
	// autovacuum, so the value is catalog/dump-only (advisory; runtime
	// unaffected). M0110-0001 (DU-002 slice 196).
	AutovacuumEnabled    bool
	AutovacuumEnabledSet bool

	// ToastTupleTarget stores the table's `WITH (toast_tuple_target=N)` storage
	// parameter (128–8160). Zero means unset (PG's default; valid values start
	// at 128, so — like fillfactor — a plain zero check unambiguously detects
	// "not specified" without a separate flag). When non-zero, pg_class's
	// reloptions cell gains the text[] element `toast_tuple_target=N`, which
	// pg_dump renders back as `WITH (toast_tuple_target='N')`. goopg's TOAST
	// thresholds are fixed, so the value is catalog/dump-only (advisory; runtime
	// unaffected). M0110-0001 (DU-002 slice 197).
	ToastTupleTarget int

	// AutovacuumVacuumThreshold stores the table's
	// `WITH (autovacuum_vacuum_threshold=N)` storage parameter. PG's reloption
	// range is 0–INT_MAX with a default of -1 (= unset / use the GUC); because 0
	// is a valid explicit value, AutovacuumVacuumThresholdSet — not a zero check
	// — guards whether the option was specified (the parallel_workers pattern).
	// When set, pg_class.reloptions gains the text[] element
	// `autovacuum_vacuum_threshold=N`, which pg_dump renders back as
	// `WITH (autovacuum_vacuum_threshold='N')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 198).
	AutovacuumVacuumThreshold    int
	AutovacuumVacuumThresholdSet bool

	// AutovacuumVacuumScaleFactor stores the table's
	// `WITH (autovacuum_vacuum_scale_factor=F)` storage parameter — the first
	// REAL-typed reloption goopg round-trips. PG's reloption type is
	// RELOPT_TYPE_REAL with range 0.0–100.0 and a default of -1 (= unset / use
	// the GUC); because 0.0 is a valid explicit value, AutovacuumVacuumScaleFactorSet
	// — not a zero check — guards whether the option was specified (the
	// parallel_workers pattern, generalized to a float). When set,
	// pg_class.reloptions gains the text[] element
	// `autovacuum_vacuum_scale_factor=F` (F rendered as its shortest exact
	// decimal), which pg_dump renders back as
	// `WITH (autovacuum_vacuum_scale_factor='F')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 199).
	AutovacuumVacuumScaleFactor    float64
	AutovacuumVacuumScaleFactorSet bool

	// AutovacuumAnalyzeScaleFactor stores the table's
	// `WITH (autovacuum_analyze_scale_factor=F)` storage parameter — the second
	// REAL-typed reloption goopg round-trips, reusing the slice-199 float path.
	// PG's reloption type is RELOPT_TYPE_REAL with range 0.0–100.0 and a default
	// of -1 (= unset / use the GUC); because 0.0 is a valid explicit value,
	// AutovacuumAnalyzeScaleFactorSet — not a zero check — guards whether the
	// option was specified. When set, pg_class.reloptions gains the text[] element
	// `autovacuum_analyze_scale_factor=F` (F rendered as its shortest exact
	// decimal), which pg_dump renders back as
	// `WITH (autovacuum_analyze_scale_factor='F')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 200).
	AutovacuumAnalyzeScaleFactor    float64
	AutovacuumAnalyzeScaleFactorSet bool

	// AutovacuumVacuumInsertScaleFactor stores the table's
	// `WITH (autovacuum_vacuum_insert_scale_factor=F)` storage parameter — the
	// third REAL-typed reloption goopg round-trips, reusing the slice-199 float
	// path. PG's reloption type is RELOPT_TYPE_REAL with range 0.0–100.0 and a
	// default of -1 (= unset / use the GUC); because 0.0 is a valid explicit
	// value, AutovacuumVacuumInsertScaleFactorSet — not a zero check — guards
	// whether the option was specified. When set, pg_class.reloptions gains the
	// text[] element `autovacuum_vacuum_insert_scale_factor=F` (F rendered as its
	// shortest exact decimal), which pg_dump renders back as
	// `WITH (autovacuum_vacuum_insert_scale_factor='F')`. goopg has no
	// autovacuum, so the value is catalog/dump-only (advisory; runtime
	// unaffected). M0110-0001 (DU-002 slice 201).
	AutovacuumVacuumInsertScaleFactor    float64
	AutovacuumVacuumInsertScaleFactorSet bool

	// AutovacuumVacuumCostDelay stores the table's
	// `WITH (autovacuum_vacuum_cost_delay=F)` storage parameter — the fourth (and
	// final) REAL-typed reloption goopg round-trips, reusing the slice-199 float
	// path. PG's reloption type is RELOPT_TYPE_REAL with range 0.0–100.0 and a
	// default of -1 (= unset / use the GUC); because 0.0 is a valid explicit
	// value, AutovacuumVacuumCostDelaySet — not a zero check — guards whether the
	// option was specified. When set, pg_class.reloptions gains the text[] element
	// `autovacuum_vacuum_cost_delay=F` (F rendered as its shortest exact decimal),
	// which pg_dump renders back as `WITH (autovacuum_vacuum_cost_delay='F')`.
	// goopg has no autovacuum, so the value is catalog/dump-only (advisory;
	// runtime unaffected). M0110-0001 (DU-002 slice 202).
	AutovacuumVacuumCostDelay    float64
	AutovacuumVacuumCostDelaySet bool

	// AutovacuumAnalyzeThreshold stores the table's
	// `WITH (autovacuum_analyze_threshold=N)` storage parameter — the second
	// INT-typed autovacuum reloption goopg round-trips, reusing the slice-198
	// integer path. PG's reloption type is RELOPT_TYPE_INT with range 0–INT_MAX
	// and a default of -1 (= unset / use the GUC); because 0 is a valid explicit
	// value, AutovacuumAnalyzeThresholdSet — not a zero check — guards whether the
	// option was specified (the parallel_workers pattern). When set,
	// pg_class.reloptions gains the text[] element
	// `autovacuum_analyze_threshold=N`, which pg_dump renders back as
	// `WITH (autovacuum_analyze_threshold='N')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 203).
	AutovacuumAnalyzeThreshold    int
	AutovacuumAnalyzeThresholdSet bool

	// AutovacuumVacuumInsertThreshold stores the table's
	// `WITH (autovacuum_vacuum_insert_threshold=N)` storage parameter — the third
	// INT-typed autovacuum reloption goopg round-trips, reusing the slice-198
	// integer path. PG's reloption type is RELOPT_TYPE_INT with range -1–INT_MAX
	// and a default of -2 (= unset / use the GUC); -1 disables insert vacuums.
	// Because -1 and 0 are valid explicit values, AutovacuumVacuumInsertThresholdSet
	// — not a zero check — guards whether the option was specified (the
	// parallel_workers pattern). When set, pg_class.reloptions gains the text[]
	// element `autovacuum_vacuum_insert_threshold=N`, which pg_dump renders back as
	// `WITH (autovacuum_vacuum_insert_threshold='N')`. goopg has no autovacuum, so
	// the value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 204).
	AutovacuumVacuumInsertThreshold    int
	AutovacuumVacuumInsertThresholdSet bool

	// VacuumTruncate stores the table's `WITH (vacuum_truncate=BOOL)` storage
	// parameter (RELOPT_TYPE_BOOL, reloptions.c:1915; default true). Like
	// autovacuum_enabled the value itself carries no default that a zero check
	// could detect, so VacuumTruncateSet — not a zero check — guards whether the
	// option was specified. When set, pg_class.reloptions gains the text[]
	// element `vacuum_truncate=true|false`, which pg_dump renders back as
	// `WITH (vacuum_truncate='true'|'false')`. goopg has no VACUUM truncation, so
	// the value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 205).
	VacuumTruncate    bool
	VacuumTruncateSet bool

	// LogAutovacuumMinDuration stores the table's
	// `WITH (log_autovacuum_min_duration=N)` storage parameter — the fourth
	// INT-typed autovacuum-namespace reloption goopg round-trips, reusing the
	// slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT with range
	// -1–INT_MAX and a default of -1 (= unset / use the GUC); 0 logs every
	// autovacuum action (reloptions.c:1897/329). Because -1 and 0 are valid
	// explicit values, LogAutovacuumMinDurationSet — not a zero check — guards
	// whether the option was specified (the parallel_workers pattern). When set,
	// pg_class.reloptions gains the text[] element `log_autovacuum_min_duration=N`,
	// which pg_dump renders back as `WITH (log_autovacuum_min_duration='N')`.
	// goopg has no autovacuum, so the value is catalog/dump-only (advisory;
	// runtime unaffected). M0110-0001 (DU-002 slice 206).
	LogAutovacuumMinDuration    int
	LogAutovacuumMinDurationSet bool

	// AutovacuumFreezeMinAge stores the table's
	// `WITH (autovacuum_freeze_min_age=N)` storage parameter — the fifth INT-typed
	// autovacuum-namespace reloption goopg round-trips, reusing the slice-198
	// integer path. PG's reloption type is RELOPT_TYPE_INT,
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, with range 0–1000000000 and a default
	// of -1 (= unset / use the GUC) (reloptions.c:1885/272). Because 0 is a valid
	// explicit value, AutovacuumFreezeMinAgeSet — not a zero check — guards whether
	// the option was specified (the parallel_workers pattern). When set,
	// pg_class.reloptions gains the text[] element `autovacuum_freeze_min_age=N`,
	// which pg_dump renders back as `WITH (autovacuum_freeze_min_age='N')`. goopg
	// has no autovacuum, so the value is catalog/dump-only (advisory; runtime
	// unaffected). M0110-0001 (DU-002 slice 207).
	AutovacuumFreezeMinAge    int
	AutovacuumFreezeMinAgeSet bool

	// AutovacuumFreezeMaxAge stores the table's
	// `WITH (autovacuum_freeze_max_age=N)` storage parameter — the sixth INT-typed
	// autovacuum-namespace reloption goopg round-trips, reusing the slice-198
	// integer path. PG's reloption type is RELOPT_TYPE_INT,
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, with range 100000–2000000000 and a
	// default of -1 (= unset / use the GUC) (reloptions.c:1887/290). Because the
	// minimum valid value is 100000, an explicit -1 is rejected as out-of-range;
	// AutovacuumFreezeMaxAgeSet records whether the option was specified (the
	// parallel_workers pattern). When set, pg_class.reloptions gains the text[]
	// element `autovacuum_freeze_max_age=N`, which pg_dump renders back as
	// `WITH (autovacuum_freeze_max_age='N')`. goopg has no autovacuum, so the value
	// is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 208).
	AutovacuumFreezeMaxAge    int
	AutovacuumFreezeMaxAgeSet bool

	// AutovacuumFreezeTableAge stores the table's
	// `WITH (autovacuum_freeze_table_age=N)` storage parameter — the seventh
	// INT-typed autovacuum-namespace reloption goopg round-trips, reusing the
	// slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT,
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, with range 0–2000000000 and a default
	// of -1 (= unset / use the GUC) (reloptions.c:1889/312). Because 0 is a valid
	// explicit value, AutovacuumFreezeTableAgeSet — not a zero check — guards
	// whether the option was specified (the parallel_workers pattern). When set,
	// pg_class.reloptions gains the text[] element `autovacuum_freeze_table_age=N`,
	// which pg_dump renders back as `WITH (autovacuum_freeze_table_age='N')`. goopg
	// has no autovacuum, so the value is catalog/dump-only (advisory; runtime
	// unaffected). M0110-0001 (DU-002 slice 209).
	AutovacuumFreezeTableAge    int
	AutovacuumFreezeTableAgeSet bool

	// AutovacuumMultixactFreezeMinAge stores the table's
	// `WITH (autovacuum_multixact_freeze_min_age=N)` storage parameter — the eighth
	// INT-typed autovacuum-namespace reloption goopg round-trips, reusing the
	// slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT,
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, with range 0–1000000000 and a default
	// of -1 (= unset / use the GUC) (reloptions.c:1891/281). Because 0 is a valid
	// explicit value, AutovacuumMultixactFreezeMinAgeSet — not a zero check —
	// guards whether the option was specified (the parallel_workers pattern). When
	// set, pg_class.reloptions gains the text[] element
	// `autovacuum_multixact_freeze_min_age=N`, which pg_dump renders back as
	// `WITH (autovacuum_multixact_freeze_min_age='N')`. goopg has no autovacuum, so
	// the value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 210).
	AutovacuumMultixactFreezeMinAge    int
	AutovacuumMultixactFreezeMinAgeSet bool

	// AutovacuumMultixactFreezeMaxAge stores the table's
	// `WITH (autovacuum_multixact_freeze_max_age=N)` storage parameter — the ninth
	// INT-typed autovacuum-namespace reloption goopg round-trips, reusing the
	// slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT,
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, with range 10000–2000000000 and a default
	// of -1 (= unset / use the GUC) (reloptions.c:1893/299). Unlike the min/table-age
	// options the lower bound is 10000, but AutovacuumMultixactFreezeMaxAgeSet — not a
	// zero check — still guards whether the option was specified (the parallel_workers
	// pattern). When set, pg_class.reloptions gains the text[] element
	// `autovacuum_multixact_freeze_max_age=N`, which pg_dump renders back as
	// `WITH (autovacuum_multixact_freeze_max_age='N')`. goopg has no autovacuum, so
	// the value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 211).
	AutovacuumMultixactFreezeMaxAge    int
	AutovacuumMultixactFreezeMaxAgeSet bool

	// AutovacuumMultixactFreezeTableAge stores the table's
	// `WITH (autovacuum_multixact_freeze_table_age=N)` storage parameter — the tenth
	// INT-typed autovacuum-namespace reloption goopg round-trips, reusing the
	// slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT,
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, with range 0–2000000000 and a default
	// of -1 (= unset / use the GUC) (reloptions.c:1895/316). As with the min-age
	// option 0 is a valid explicit value, so AutovacuumMultixactFreezeTableAgeSet —
	// not a zero check — guards whether the option was specified (the
	// parallel_workers pattern). When set, pg_class.reloptions gains the text[]
	// element `autovacuum_multixact_freeze_table_age=N`, which pg_dump renders back
	// as `WITH (autovacuum_multixact_freeze_table_age='N')`. goopg has no autovacuum,
	// so the value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 212).
	AutovacuumMultixactFreezeTableAge    int
	AutovacuumMultixactFreezeTableAgeSet bool

	// AutovacuumVacuumCostLimit stores the table's
	// `WITH (autovacuum_vacuum_cost_limit=N)` storage parameter — the eleventh
	// INT-typed autovacuum-namespace reloption goopg round-trips, reusing the
	// slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT,
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, with range 1–10000 and a default
	// of -1 (= unset / use the GUC) (reloptions.c:1883/268). Unlike the freeze-age
	// options the lower bound is 1, so 0 is below range and rejected; the WITH-clause
	// parser already refuses negative values, leaving 0, above-range (10001) and
	// non-integer as the reachable invalid cases. AutovacuumVacuumCostLimitSet — not a
	// zero check — guards whether the option was specified (the parallel_workers
	// pattern). When set, pg_class.reloptions gains the text[] element
	// `autovacuum_vacuum_cost_limit=N`, which pg_dump renders back as
	// `WITH (autovacuum_vacuum_cost_limit='N')`. goopg has no autovacuum, so
	// the value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 213).
	AutovacuumVacuumCostLimit    int
	AutovacuumVacuumCostLimitSet bool

	// UserCatalogTable stores the table's `WITH (user_catalog_table=BOOL)`
	// storage parameter. PG's reloption type is RELOPT_TYPE_BOOL,
	// RELOPT_KIND_HEAP, default false (reloptions.c:1909). Like
	// autovacuum_enabled the boolean value carries no default that a zero check
	// could detect, so UserCatalogTableSet guards whether the option was
	// specified (the slice-196 boolean path). When set, pg_class.reloptions
	// gains the text[] element `user_catalog_table=true|false`, which pg_dump
	// renders back as `WITH (user_catalog_table='true'|'false')`. The option
	// marks a heap as a catalog table for logical-decoding tuple-visibility
	// purposes; goopg has no logical decoding, so the value is catalog/dump-only
	// (advisory; runtime unaffected). M0110-0001 (DU-002 slice 214).
	UserCatalogTable    bool
	UserCatalogTableSet bool

	// AutovacuumVacuumMaxThreshold stores the table's
	// `WITH (autovacuum_vacuum_max_threshold=N)` storage parameter — a PG18 heap
	// reloption (RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, reloptions.c:236) that caps
	// the dead-tuple count at which autovacuum triggers. PG's reloption type is
	// RELOPT_TYPE_INT with range -1–INT_MAX and a default of -2 (= unset / use the
	// GUC); -1 disables the cap. Because -1 and 0 are valid explicit values,
	// AutovacuumVacuumMaxThresholdSet — not a zero check — guards whether the
	// option was specified (the autovacuum_vacuum_insert_threshold pattern, slice
	// 204). When set, pg_class.reloptions gains the text[] element
	// `autovacuum_vacuum_max_threshold=N`, which pg_dump renders back as
	// `WITH (autovacuum_vacuum_max_threshold='N')`. goopg has no autovacuum, so the
	// value is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 215).
	AutovacuumVacuumMaxThreshold    int
	AutovacuumVacuumMaxThresholdSet bool

	// VacuumMaxEagerFreezeFailureRate stores the table's
	// `WITH (vacuum_max_eager_freeze_failure_rate=F)` storage parameter — a PG18
	// heap reloption (RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, reloptions.c:431)
	// giving the fraction of pages vacuum may scan and fail to freeze before
	// disabling eager scanning. Reuses the slice-199 REAL path: PG's reloption
	// type is RELOPT_TYPE_REAL but with range 0.0–1.0 (not 0.0–100.0) and a
	// default of -1 (= unset / use the GUC) (reloptions.c:434). Because 0.0 is a
	// valid explicit value, VacuumMaxEagerFreezeFailureRateSet — not a zero check
	// — guards whether the option was specified (the parallel_workers pattern,
	// generalized to a float). When set, pg_class.reloptions gains the text[]
	// element `vacuum_max_eager_freeze_failure_rate=F` (F rendered as its shortest
	// exact decimal), which pg_dump renders back as
	// `WITH (vacuum_max_eager_freeze_failure_rate='F')`. goopg has no eager
	// freezing, so the value is catalog/dump-only (advisory; runtime unaffected).
	// M0110-0001 (DU-002 slice 216).
	VacuumMaxEagerFreezeFailureRate    float64
	VacuumMaxEagerFreezeFailureRateSet bool

	// VacuumIndexCleanup stores the table's `WITH (vacuum_index_cleanup=V)`
	// storage parameter — a PG18 heap reloption (RELOPT_TYPE_ENUM,
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST, reloptions.c:519) controlling whether
	// VACUUM performs index vacuuming and cleanup. This is goopg's first ENUM
	// reloption: PG accepts the spellings auto/on/off/true/false/yes/no/1/0
	// (StdRdOptIndexCleanupValues, reloptions.c:487) case-insensitively, with a
	// default of "auto". Unlike the bool/int/float reloptions, goopg stores the
	// value verbatim (trimmed) rather than re-rendering a canonical form, matching
	// PG's pg_class.reloptions which preserves the literal input text — so
	// `WITH (vacuum_index_cleanup=on)` round-trips as `=on`, not `=true`.
	// VacuumIndexCleanupSet guards presence (there is no reserved sentinel string,
	// and "auto" is itself a legal explicit value). When set, pg_class.reloptions
	// gains the text[] element `vacuum_index_cleanup=V`, which pg_dump renders back
	// as `WITH (vacuum_index_cleanup='V')`. goopg has no autovacuum, so the value
	// is catalog/dump-only (advisory; runtime unaffected). M0110-0001
	// (DU-002 slice 217).
	VacuumIndexCleanup    string
	VacuumIndexCleanupSet bool

	// ToastReloptions holds the table's `toast.*` storage parameters, stored as
	// PG-normalized `name=value` strings WITHOUT the `toast.` namespace prefix
	// (e.g. `autovacuum_enabled=false`), mirroring how PostgreSQL keeps them on
	// the TOAST relation's own pg_class.reloptions. When non-empty, the pg_class
	// virtual view synthesizes a relkind='t' TOAST row (OID = table OID +
	// toastRelidOffset) carrying these as its reloptions and points the main
	// table's reltoastrelid at it, so pg_dump's `LEFT JOIN pg_class tc ON
	// (c.reltoastrelid = tc.oid AND tc.relkind='t')` reads them via
	// `tc.reloptions AS toast_reloptions` and re-emits `WITH (toast.<opt>='…')`.
	// goopg has no TOAST, so these are catalog/dump-only (advisory). M0110-0001
	// (DU-002 slice 224).
	ToastReloptions []string
}

// toastRelidOffset derives a synthetic TOAST relation OID from its parent
// table's OID for the pg_class virtual view. The offset keeps the synthetic
// OID clear of the small sequentially-allocated user-relation OID space; it
// matches the convention used by the executor's TOAST-relation helper
// (internal/executor/toast.go: RelOid + 100_000_000).
const toastRelidOffset = 100_000_000

// toastIndexOidOffset derives the synthetic OID of a TOAST relation's unique
// btree index (pg_toast_<parentOID>_index) from its parent table's OID. PG
// auto-creates this index on every TOAST relation; goopg has no real TOAST
// index, so the OID is catalog/regclass-only. Kept a full 100M above
// toastRelidOffset so the index range [200M, 300M) never overlaps the TOAST
// relation range [100M, 200M) for any realistic user OID (< 100M). M0118-0008
// TOAST-exposure slice 3.
const toastIndexOidOffset = 200_000_000

// columnTypeIsToastable reports whether a column's declared type is a varlena
// (variable-length) type that PostgreSQL would store out-of-line via TOAST.
// Mirrors the executor's isToastableType set (internal/executor/toast.go) plus
// array columns (every SQL array is varlena). Kept in sync with the storage
// path so reltoastrelid existence matches where goopg actually toasts.
func columnTypeIsToastable(col Column) bool {
	if col.Type.IsArray {
		return true
	}
	switch strings.ToLower(col.Type.Name) {
	case "text", "varchar", "character varying",
		"char", "character", "bpchar",
		"bytea", "json", "jsonb", "jsonpath", "xml":
		return true
	}
	return false
}

// tableNeedsToastRelation mirrors PostgreSQL's needs_toast_table
// (src/backend/catalog/toasting.c): a relation gets an auto-created TOAST
// relation when it has at least one non-dropped column of a toastable type.
// PG attaches the TOAST relation only to ordinary tables / materialized views /
// partition leaves — never to partitioned parents (relkind='p', no storage),
// views, sequences, or composite types. The caller restricts this to the
// heap-storage relkinds, so here we only inspect the column set.
func tableNeedsToastRelation(t *Table) bool {
	for _, col := range t.Columns {
		if col.Dropped {
			continue
		}
		if columnTypeIsToastable(col) {
			return true
		}
	}
	return false
}

// tableHasToastRelation reports whether the table owns an auto-exposed TOAST
// relation: it carries explicit `toast.*` reloptions, or it is a USER ordinary
// table / materialized view (relkind 'r' or 'm') with at least one toastable
// (varlena) column. This is the single source of truth for the `hasToastRel`
// gate in the pg_class virtual builder AND for ToastRelName's OID→name
// resolution, so reltoastrelid exposure and `reltoastrelid::regclass` rendering
// can never diverge. Mirrors PG needs_toast_table (src/backend/catalog/
// toasting.c); system catalogs are excluded because goopg serves them virtually
// with no real heap, so a reltoastrelid join target would break pg_amcheck's
// whole-DB walk. M0118-0008 TOAST-exposure (design 0118-0084). The pg_class
// virtual builder emits a relkind='t' TOAST row (and relkind='i' TOAST-index
// row, slice 3) for exactly this set; toastBearingTables enumerates the same
// set for the pg_index builder so the two catalogs never diverge.
// ReplIdentOrDefault resolves a table's effective pg_class.relreplident code,
// mapping an unset (empty) ReplicaIdentity to PG's implicit default 'd'
// (REPLICA_IDENTITY_DEFAULT). DU-002 slice 305.
func ReplIdentOrDefault(replIdent string) string {
	if replIdent == "" {
		return "d"
	}
	return replIdent
}

// boolToPGChar renders a Go bool as PostgreSQL's textual boolean catalog
// representation ('t'/'f'), matching how the virtual-catalog rows encode bool
// columns such as pg_class.relrowsecurity. DU-002 slice 322.
func boolToPGChar(b bool) string {
	if b {
		return "t"
	}
	return "f"
}

func tableHasToastRelation(t *Table) bool {
	if len(t.ToastReloptions) > 0 {
		return true
	}
	if IsSystemRelation(t.OID) {
		return false
	}
	// relkind must be ordinary table ('r') or materialized view ('m'): exclude
	// sequences ('S'), plain views ('v') and partitioned parents ('p'), matching
	// the relkind computation in the virtual builder.
	if t.IsSequence || (t.View != nil && !t.IsMatView) ||
		(t.PartitionMethod != "" && t.PartitionParentOID == 0) {
		return false
	}
	return tableNeedsToastRelation(t)
}

// ToastRelName resolves a synthetic TOAST relation OID (parent OID +
// toastRelidOffset) to its schema-qualified name `pg_toast.pg_toast_<parentOID>`,
// matching PG's regclassout for a relation in the pg_toast namespace (which is
// never in search_path, hence always schema-qualified). The synthetic TOAST
// pg_class row lives only in the virtual builder's output, not in c.tables, so
// `reltoastrelid::regclass` cannot find it via tableByOID and must reconstruct
// the name here. Returns false when the OID is below the TOAST range or its
// parent table owns no auto-exposed TOAST relation. M0118-0008 TOAST-exposure
// slice 2 (design 0118-0084).
func (c *InMemory) ToastRelName(oid uint32) (string, bool) {
	// The TOAST index range [200M, 300M) sits above the TOAST relation range
	// [100M, 200M); check it first. The index name is pg_toast_<parentOID>_index.
	if oid >= toastIndexOidOffset {
		parentOID := oid - toastIndexOidOffset
		c.mu.RLock()
		defer c.mu.RUnlock()
		t, ok := c.tableByOID(parentOID)
		if !ok || !tableHasToastRelation(t) {
			return "", false
		}
		return "pg_toast." + c.toastDisplayNameLocked(oid, "pg_toast_"+strconv.Itoa(int(parentOID))+"_index"), true
	}
	if oid < toastRelidOffset {
		return "", false
	}
	parentOID := oid - toastRelidOffset
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tableByOID(parentOID)
	if !ok || !tableHasToastRelation(t) {
		return "", false
	}
	return "pg_toast." + c.toastDisplayNameLocked(oid, "pg_toast_"+strconv.Itoa(int(parentOID))), true
}

// toastDisplayNameLocked returns the current unqualified name of a synthetic
// TOAST relation/index OID: the override recorded by ALTER … RENAME if present,
// otherwise the default deflt (pg_toast_<parentOID>[_index]). The caller must
// hold c.mu (read or write). M0118-0008 TOAST-exposure slice 4 (design 0118-0087).
func (c *InMemory) toastDisplayNameLocked(oid uint32, deflt string) string {
	if name, ok := c.toastRenames[oid]; ok && name != "" {
		return name
	}
	return deflt
}

// RenameToastRel records a new unqualified name for a synthetic TOAST relation
// or index OID (ALTER TABLE/INDEX … RENAME under allow_system_table_mods). The
// override is read back by the pg_class virtual builder, ToastRelName and
// LookupToastRel. M0118-0008 TOAST-exposure slice 4 (design 0118-0087).
func (c *InMemory) RenameToastRel(oid uint32, newName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toastRenames[oid] = newName
}

// LookupToastRel resolves a schema-qualified pg_toast relation/index name to its
// synthetic OID. It accepts either the default name (pg_toast_<parentOID> for the
// TOAST relation, pg_toast_<parentOID>_index for its unique btree index) or a name
// recorded by ALTER … RENAME. Returns (oid, isIndex, true) when the name resolves
// to a live TOAST object whose parent table still owns an auto-exposed TOAST
// relation; (0, false, false) otherwise. Used by the executor's ALTER TABLE/INDEX
// … RENAME and REINDEX routing. M0118-0008 TOAST-exposure slice 4 (design 0118-0087).
func (c *InMemory) LookupToastRel(schema, name string) (oid uint32, isIndex bool, ok bool) {
	if !strings.EqualFold(schema, "pg_toast") {
		return 0, false, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	// 1) An override name (e.g. reind_con_toast / reind_con_toast_idx).
	for ov, nm := range c.toastRenames {
		if nm != name {
			continue
		}
		var parentOID uint32
		idx := ov >= toastIndexOidOffset
		if idx {
			parentOID = ov - toastIndexOidOffset
		} else {
			parentOID = ov - toastRelidOffset
		}
		if t, found := c.tableByOID(parentOID); found && tableHasToastRelation(t) {
			return ov, idx, true
		}
	}
	// 2) A default name pg_toast_<parentOID>[ _index ].
	const pfx = "pg_toast_"
	if !strings.HasPrefix(name, pfx) {
		return 0, false, false
	}
	body := name[len(pfx):]
	isIdx := false
	if strings.HasSuffix(body, "_index") {
		isIdx = true
		body = strings.TrimSuffix(body, "_index")
	}
	pOID, err := strconv.ParseUint(body, 10, 32)
	if err != nil {
		return 0, false, false
	}
	parentOID := uint32(pOID)
	t, found := c.tableByOID(parentOID)
	if !found || !tableHasToastRelation(t) {
		return 0, false, false
	}
	if isIdx {
		return parentOID + toastIndexOidOffset, true, true
	}
	return parentOID + toastRelidOffset, false, true
}

// ToastParentTable maps a synthetic TOAST relation/index OID back to the user
// table that owns it. REINDEX … CONCURRENTLY pg_toast.<name> routing uses it to
// wait on the parent's relation lockers (the synthetic TOAST relation has no
// heavyweight lock of its own — REINDEX CONCURRENTLY on a TOAST relation waits
// for transactions touching the parent table, exactly like upstream, whose
// toast reindex acquires its lock through the parent). Returns false when the
// OID falls outside the synthetic TOAST ranges or the parent no longer owns an
// auto-exposed TOAST relation. M0118-0008 TOAST-exposure slice 5 (design 0118-0088).
func (c *InMemory) ToastParentTable(toastOID uint32) (*Table, bool) {
	var parentOID uint32
	switch {
	case toastOID >= toastIndexOidOffset:
		parentOID = toastOID - toastIndexOidOffset
	case toastOID >= toastRelidOffset:
		parentOID = toastOID - toastRelidOffset
	default:
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tableByOID(parentOID)
	if !ok || !tableHasToastRelation(t) {
		return nil, false
	}
	return t, true
}

// ToastRelFileNode returns the heavyweight-lock RelFileNode of the synthetic
// TOAST relation owned by the table at parentRel, when it has an auto-exposed
// TOAST relation. The node carries the parent's DB/tablespace with the RelOid
// replaced by the synthetic TOAST relation OID (parentOID + toastRelidOffset);
// only DB+RelOid are significant in a lock tag. DML writes (acquireWriteLockTxn)
// and DROP TABLE (dropTableByRef) register a lock on this node, so REINDEX …
// CONCURRENTLY pg_toast.<name> can wait for transactions that toasted a value or
// dropped the table — a bare LOCK TABLE on the parent never touches it, matching
// PG, whose toast relation is locked only on a toast write or a drop, not on an
// explicit parent LOCK TABLE. M0118-0008 TOAST-exposure slice 5 (design 0118-0088).
func (c *InMemory) ToastRelFileNode(parentRel storage.RelFileNode) (storage.RelFileNode, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tableByOID(parentRel.RelOid)
	if !ok || !tableHasToastRelation(t) {
		return storage.RelFileNode{}, false
	}
	toast := parentRel
	toast.RelOid = parentRel.RelOid + toastRelidOffset
	return toast, true
}

// toastBearingTables returns every relation that owns an auto-exposed TOAST
// relation, under the SAME visibility filter the pg_class virtual builder
// applies to its main table loop (non-virtual ordinary tables plus
// materialized views and user sequences/views are admitted to the loop;
// tableHasToastRelation then keeps only relkind 'r'/'m'). The pg_class builder
// emits the synthetic relkind='t' TOAST row and relkind='i' TOAST-index row for
// exactly this set, so the pg_index builder must enumerate the same set or the
// two catalogs diverge (an indexrelid with no matching pg_class row). Returned
// tables share the registry's backing storage; callers must only read.
// M0118-0008 TOAST-exposure slice 3.
func (c *InMemory) toastBearingTables() []*Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.tables))
	for k := range c.tables {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*Table, 0)
	for _, k := range keys {
		t := c.tables[k]
		// Mirror the pg_class main-loop skip: drop system-catalog virtual
		// tables but keep user views, matviews and sequences (which
		// tableHasToastRelation subsequently rejects unless they are 'r'/'m').
		if t.Virtual && t.View == nil && !t.IsMatView && !t.IsSequence {
			continue
		}
		if tableHasToastRelation(t) {
			out = append(out, t)
		}
	}
	return out
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
	Name string
	// OID is the trigger's pg_trigger.oid, assigned from the catalog OID
	// counter at CREATE TRIGGER time. pg_dump's getTriggers selects
	// pg_get_triggerdef(t.oid, false), so a zero OID means the trigger is
	// invisible to pg_dump (predates OID tracking). DU-002 slice 319.
	OID        uint32
	TableOID   uint32
	Timing     TriggerTiming
	Events     []string // "insert", "update", "delete", "truncate"
	// UpdateColumns is the optional `UPDATE OF col1, col2` column list of a
	// column-specific UPDATE trigger; empty for every other form. pg_dump's
	// pg_get_triggerdef appends ` OF <cols>` right after the UPDATE event and
	// pg_trigger.tgattr carries the column attnums. DU-002 slice 326.
	UpdateColumns []string
	// IsConstraint marks a CREATE CONSTRAINT TRIGGER. pg_get_triggerdef emits
	// `CREATE CONSTRAINT TRIGGER` (instead of `CREATE TRIGGER`) and a
	// `[NOT ]DEFERRABLE INITIALLY {IMMEDIATE|DEFERRED}` clause after the ON-table
	// name. ConstraintOID is the pg_constraint OID PG implicitly creates for the
	// trigger (contype 't'); it feeds pg_trigger.tgconstraint so the trigger is
	// recognised as a constraint trigger. goopg does NOT enforce constraint-
	// trigger semantics — this is dump fidelity only. DU-002 slice 327.
	IsConstraint  bool
	Deferrable    bool
	InitDeferred  bool
	ConstraintOID uint32
	// OldTransitionTable / NewTransitionTable are the REFERENCING clause's
	// `OLD TABLE AS <name>` / `NEW TABLE AS <name>` transition-relation names.
	// pg_dump's pg_get_triggerdef emits `REFERENCING OLD TABLE AS … NEW TABLE
	// AS …` between the ON-table name and FOR EACH ROW; pg_trigger.tgoldtable /
	// tgnewtable carry the names. goopg records them for dump fidelity only — the
	// transition tables are not materialised. DU-002 slice 328.
	OldTransitionTable string
	NewTransitionTable string
	// WhenExpr is the parsed `WHEN (condition)` qualification (an OLD/NEW-aware
	// boolean expression), nil when absent. pg_dump's pg_get_triggerdef re-emits
	// `WHEN (…)` between FOR EACH and EXECUTE FUNCTION; PG stores it as a node tree
	// in pg_trigger.tgqual. goopg keeps the parsed expression for dump fidelity
	// only — the condition is not evaluated at trigger-firing time. DU-002 slice 329.
	WhenExpr           parser.Expr
	ForEachRow         bool
	FuncName           string // function/procedure name (unschemed)
	FuncSchema         string
	Args               []string // trigger function arguments (TG_ARGV)
}

// triggerUpdateColAttrs renders a column-specific UPDATE trigger's column list
// as pg_trigger.tgattr, a space-separated int2vector of 1-based attnums (the
// same text form pg_index.indkey uses). An unresolved column name is skipped;
// an empty/absent list yields "". DU-002 slice 326.
func triggerUpdateColAttrs(tbl *Table, cols []string) string {
	if tbl == nil || len(cols) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cols))
	for _, name := range cols {
		for _, col := range tbl.Columns {
			if col.Name == name {
				parts = append(parts, strconv.Itoa(col.Ordinal+1))
				break
			}
		}
	}
	return strings.Join(parts, " ")
}

// PolicyInfo describes one row-level security policy created with CREATE POLICY.
// goopg does NOT enforce row-level security; the policy is recorded purely so it
// round-trips through pg_dump (the pg_policy virtual catalog → dumpPolicy). The
// field set mirrors the pg_policy columns pg_dump's getPolicies reads. DU-002
// slice 323.
type PolicyInfo struct {
	// Name is pg_policy.polname; OID is pg_policy.oid (assigned from the catalog
	// OID counter at CREATE POLICY time — a zero OID means the policy predates
	// catalog tracking and is invisible to pg_dump).
	Name string
	OID  uint32
	// Command is pg_policy.polcmd: '*' (ALL), 'r' (SELECT), 'a' (INSERT),
	// 'w' (UPDATE), or 'd' (DELETE).
	Command byte
	// Permissive is pg_policy.polpermissive: true for AS PERMISSIVE (the
	// default), false for AS RESTRICTIVE (pg_dump emits ` AS RESTRICTIVE`).
	Permissive bool
	// Roles is pg_policy.polroles: the role OIDs the policy applies to. The
	// special value {0} means PUBLIC (pg_dump omits the TO clause). goopg has no
	// per-role OID registry yet, so only PUBLIC ({0}) round-trips today; named
	// roles are deferred to a follow-up slice.
	Roles []uint32
	// Using / WithCheck are the parsed USING / WITH CHECK expressions (nil when
	// absent). They are rendered to pg_policy.polqual / polwithcheck via
	// formatExprForAttrdef (the catalog-side pg_get_expr deparser), which fully
	// parenthesizes every node like PG's pg_get_expr, so dumpPolicy re-emits
	// ` USING ((expr))` / ` WITH CHECK ((expr))` byte-identically to real pg_dump.
	Using     parser.Expr
	WithCheck parser.Expr
}

// RuleInfo describes one unconditional DO-NOTHING query-rewrite rule created
// with CREATE RULE. goopg does NOT implement the rewrite system; the rule is
// recorded purely so it round-trips through pg_dump (the pg_rewrite virtual
// catalog → pg_get_ruledef → dumpRule). The field set mirrors the pg_rewrite
// columns pg_dump's getRules reads. DU-002 slice 324.
type RuleInfo struct {
	// Name is pg_rewrite.rulename; OID is pg_rewrite.oid (assigned from the
	// catalog OID counter at CREATE RULE time — a zero OID means the rule
	// predates catalog tracking and is invisible to pg_dump).
	Name string
	OID  uint32
	// Event is the firing event: "INSERT", "UPDATE" or "DELETE". pg_rewrite
	// stores it as ev_type '3'/'2'/'4' (ON SELECT '1' view rules are not
	// modelled here).
	Event string
	// Instead is pg_rewrite.is_instead: true for DO INSTEAD NOTHING, false for
	// DO [ALSO] NOTHING.
	Instead bool
	// Qual is the deparsed WHERE qualification of a conditional DO-NOTHING rule,
	// already rendered to the canonical pg_get_ruledef form (e.g.
	// "(old.a <> new.a)", parens included). Empty for an unconditional rule.
	// pg_rewrite stores it as ev_qual; goopg keeps the deparsed text since the
	// rewrite system is not executed. DU-002 slice 359.
	Qual string
	// Enabled is pg_rewrite.ev_enabled set by `ALTER TABLE … {ENABLE|DISABLE}
	// [REPLICA|ALWAYS] RULE`: 'O' (origin — the default), 'D' (disabled),
	// 'R' (replica), 'A' (always). A zero value is treated as 'O'. pg_dump's
	// dumpRule emits an `ALTER TABLE … {ENABLE ALWAYS|ENABLE REPLICA|DISABLE}
	// RULE <name>;` whenever ev_enabled is not 'O'. DU-002 slice 325.
	Enabled byte
}

// EvEnabled returns the rule's pg_rewrite.ev_enabled char, mapping a zero value
// to the 'O' (origin) default. DU-002 slice 325.
func (r RuleInfo) EvEnabled() byte {
	if r.Enabled == 0 {
		return 'O'
	}
	return r.Enabled
}

// EvType maps the rule's firing event to its pg_rewrite.ev_type "char" code
// (rewriteDefine.h: SELECT='1', UPDATE='2', INSERT='3', DELETE='4').
func (r RuleInfo) EvType() string {
	switch strings.ToUpper(r.Event) {
	case "SELECT":
		return "1"
	case "UPDATE":
		return "2"
	case "INSERT":
		return "3"
	case "DELETE":
		return "4"
	}
	return ""
}

// ForeignKey describes one referential integrity constraint stored on a
// child table. M0096-0011.
type ForeignKey struct {
	// Name and OID identify the constraint in pg_constraint (contype='f').
	// Auto-assigned at DDL time using PG's convention <table>_<col>_fkey when
	// no explicit CONSTRAINT name is given. A zero OID / empty Name means the
	// FK predates constraint-catalog tracking and is invisible to pg_dump. DU-002 slice 51.
	Name              string
	OID               uint32
	Columns           []string // columns in THIS table
	RefTable          string   // referenced table name (unschemed)
	RefColumns        []string // referenced columns (empty = use parent PK)
	OnDelete          parser.FKAction
	OnUpdate          parser.FKAction
	// OnDeleteSetCols restricts an `ON DELETE SET NULL|DEFAULT` action to a
	// subset of Columns — PG15 pg_constraint.confdelsetcols. Empty means the
	// whole key. pg_get_constraintdef appends ` (col, …)` after the ON DELETE
	// clause (ruleutils.c:2376). DU-002 slice 311.
	OnDeleteSetCols   []string
	Deferrable        bool
	InitiallyDeferred bool
	// NotValid is true when the constraint was added with NOT VALID — existing
	// rows were not checked, so pg_constraint.convalidated is 'f' until a
	// VALIDATE CONSTRAINT runs. M0118-0008 (alter-table-1/2).
	NotValid bool
	// MatchFull is true for a `MATCH FULL` foreign key — pg_constraint.confmatchtype
	// is 'f' and pg_get_constraintdef emits ` MATCH FULL`. MATCH SIMPLE (the
	// default) leaves it false (confmatchtype='s'). DU-002 slice 309.
	MatchFull bool
	// NotEnforced mirrors pg_constraint.conenforced='f' (PG18 NOT ENFORCED),
	// disabling the constraint's action/check triggers entirely at runtime;
	// pg_get_constraintdef gives it precedence over NotValid in the rendered
	// text, the same way catalog.NamedCheckConstraint.NotEnforced already
	// works for CHECK. The pg_constraint convalidated projection treats this
	// as implying NotValid too (mirrors PG's processCASbits), even though
	// NotValid itself is kept independent here. DU-002 slice 431.
	NotEnforced bool
}

// fkActionChar maps a parsed FK referential action to the single-char code
// PostgreSQL stores in pg_constraint.confupdtype / confdeltype. DU-002 slice 51.
func fkActionChar(a parser.FKAction) byte {
	switch a {
	case parser.FKActionRestrict:
		return 'r'
	case parser.FKActionCascade:
		return 'c'
	case parser.FKActionSetNull:
		return 'n'
	case parser.FKActionSetDefault:
		return 'd'
	default: // FKActionNoAction
		return 'a'
	}
}

// PartitionBound describes the bounds for a single partition child.
// For LIST partitioning, InValues contains the literal string values.
// For RANGE partitioning, From and To contain the bound strings ("MINVALUE", "MAXVALUE", or a literal).
// For HASH partitioning, Modulus and Remainder specify the hash bucket. M0096-0007; HASH M0097-0015.
type PartitionBound struct {
	InValues []string // LIST: values in this partition (raw, used for value routing)
	// InValueLiterals holds the SQL-literal rendering of each LIST value
	// (e.g. 'a' for a text value, 1 for an integer). Parallel to InValues.
	// Populated at partition-creation time from the bound's parser.Expr, since
	// InValues stores the unquoted raw value (needed for routing comparison) and
	// cannot be re-quoted later without the column type. FormatPartitionBound
	// prefers this for relpartbound/pg_dump output so string LIST bounds emit
	// `FOR VALUES IN ('a', 'b')` rather than the invalid `FOR VALUES IN (a, b)`.
	InValueLiterals []string
	From            string   // RANGE: lower bound (single-column, kept for compat)
	To              string   // RANGE: upper bound (single-column, kept for compat)
	FromValues      []string // RANGE: lower bound tuple (raw routing form; multi-column, len==1 for single-col)
	ToValues        []string // RANGE: upper bound tuple (raw routing form; multi-column, len==1 for single-col)
	// FromValueLiterals / ToValueLiterals hold the SQL-literal rendering of each
	// RANGE bound element (parallel to FromValues / ToValues): 'a' for a text
	// value, 5 for an integer, and the bare keyword MINVALUE / MAXVALUE for an
	// unbounded edge. Captured at partition-creation time for the same reason as
	// InValueLiterals: FromValues / ToValues store the unquoted raw form needed
	// for routing comparison and cannot be re-quoted later without the column
	// type. FormatPartitionBound prefers these so a TEXT RANGE bound emits
	// `FOR VALUES FROM ('a') TO ('m')` rather than the invalid `FROM (a) TO (m)`.
	FromValueLiterals []string
	ToValueLiterals   []string
	// FromUnbounded / ToUnbounded flag each RANGE bound element as an unbounded
	// edge (MINVALUE/-∞ when the matching IsMax is false, MAXVALUE/+∞ when true).
	// Parallel to FromValues / ToValues. Without these, an unbounded edge is only
	// distinguishable by the sentinel string "MINVALUE"/"MAXVALUE" in FromValues,
	// which a real text bound value 'MINVALUE' collides with — so routing would
	// treat that text value as ±∞. When absent (len 0, pre-slice-261 bounds),
	// routing falls back to the legacy string-sentinel check. DU-002 slice 261.
	FromUnbounded  []bool
	FromUnboundMax []bool
	ToUnbounded    []bool
	ToUnboundMax   []bool
	Modulus        int64  // HASH: modulus
	Remainder      int64  // HASH: remainder (partition index)
	IsHash         bool   // true for HASH partitions
	IsDefault      bool   // true for DEFAULT partitions
	ChildName      string // name of the child partition that owns this bound
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
		// Prefer the SQL-literal rendering (InValueLiterals) when available: it
		// quotes string values so the bound is valid SQL on restore. InValues
		// holds the raw unquoted form used for routing and cannot be re-quoted
		// here without the column type. Fall back to InValues for bounds created
		// before literals were captured (e.g. integer LIST keys render the same).
		vals := pb.InValueLiterals
		if len(vals) != len(pb.InValues) {
			vals = pb.InValues
		}
		return "FOR VALUES IN (" + strings.Join(vals, ", ") + ")"
	}
	// RANGE partition. Prefer the SQL-literal tuples (FromValueLiterals /
	// ToValueLiterals) when present and length-matched: they quote string bounds
	// and uppercase MINVALUE/MAXVALUE so the relpartbound is valid SQL on restore.
	// Fall back to the raw FromValues/ToValues (integer bounds render the same).
	fromParts := pb.FromValues
	if len(pb.FromValueLiterals) == len(pb.FromValues) && len(pb.FromValueLiterals) > 0 {
		fromParts = pb.FromValueLiterals
	}
	if len(fromParts) == 0 && pb.From != "" {
		fromParts = []string{pb.From}
	}
	toParts := pb.ToValues
	if len(pb.ToValueLiterals) == len(pb.ToValues) && len(pb.ToValueLiterals) > 0 {
		toParts = pb.ToValueLiterals
	}
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
	// DeclaredHash records that the index was created `USING hash`. goopg has no
	// native hash access method — a hash index is built on the B-tree substrate,
	// so Method stays "btree" (catalog/pg_am/pg_dump unchanged) — but a
	// SERIALIZABLE equality scan must take a bucket-grain (page) SIREAD predicate
	// lock on it rather than a relation-grain lock, to reproduce PG's reduced
	// false positives (predicate-hash spec; design 0118-0099). In-memory only:
	// not persisted to the index-DDL WAL record, so it resets to false after a
	// restart (a hash index then reverts to relation-grain SSI locking — a known
	// follow-up, no durability regression over the prior hash→btree rewrite).
	DeclaredHash bool
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
	// ColDescending / ColNullsFirst capture the per-key-column ASC/DESC + NULLS
	// ordering, parallel to Columns. They mirror pg_index.indoption so
	// BuildIndexDef (pg_get_indexdef) can reproduce a non-default ordering.
	// Empty slices mean every key column is the default ASC NULLS LAST. DU-002
	// slice 56.
	ColDescending []bool
	ColNullsFirst []bool
	// ColOpClasses captures the per-key-column explicit operator class name
	// (parallel to Columns), e.g. "text_pattern_ops". It mirrors pg_index.indclass
	// so BuildIndexDef (pg_get_indexdef) can re-emit a non-default operator class
	// after the column. An empty entry (or empty slice) means the column uses its
	// type's default operator class. DU-002 slice 312.
	ColOpClasses []string
	// ColCollations captures the per-key-column explicit collation name (parallel
	// to Columns), e.g. "C". It mirrors pg_index.indcollation so BuildIndexDef
	// (pg_get_indexdef) can re-emit a non-default COLLATE clause after the column
	// and before the opclass. An empty entry (or empty slice) means the column
	// uses its type's default collation. DU-002 slice 313.
	ColCollations []string
	IsConstraint  bool   // true when index backs a named UNIQUE/PK constraint (not bare CREATE INDEX)
	IsExclusion   bool   // true when index backs an EXCLUDE USING constraint
	ExclusionOp   string // per-column exclusion operator (e.g. "=")
	// NullsNotDistinct mirrors pg_index.indnullsnotdistinct: true when a UNIQUE
	// index was declared `NULLS NOT DISTINCT` (PG 15+). pg_get_indexdef /
	// BuildIndexDef re-emits the clause so pg_dump round-trips it. DU-002 slice 134.
	NullsNotDistinct bool
	// Deferrable / InitiallyDeferred mirror pg_constraint.condeferrable /
	// condeferred for the UNIQUE/PRIMARY KEY constraint this index backs: true
	// when the constraint was declared `DEFERRABLE` / `DEFERRABLE INITIALLY
	// DEFERRED`. pg_get_constraintdef re-emits the clause so pg_dump round-trips
	// it. Only meaningful when IsConstraint is true. DU-002 slice 139.
	Deferrable        bool
	InitiallyDeferred bool
	// PartitionParentOID is the OID of the parent index for partition index
	// trees (ALTER INDEX parent ATTACH PARTITION child). Zero if not a partition
	// index child. M0097-0023.
	PartitionParentOID uint32
	// Fillfactor stores the index's `WITH (fillfactor=N)` storage parameter
	// (btree/hash/gist accept it; range 10–100). Zero means unset — no
	// reloptions are emitted, so a plain index dumps byte-identically. The
	// index's pg_class.reloptions virtual row renders `fillfactor=N`, which
	// pg_dump reads via `t.reloptions AS indreloptions` (pg_dump.c:7775) and
	// re-emits as `CREATE INDEX … WITH (fillfactor='N')`. goopg does not honor
	// fill factor for page packing, so this is advisory catalog/dump-only.
	// DU-002 slice 218.
	Fillfactor int
	// DeduplicateItems stores the btree `WITH (deduplicate_items=on|off)`
	// boolean storage parameter. nil means unset (PG default ON) — no
	// reloption is emitted, so a plain index dumps byte-identically. When set,
	// the index's pg_class.reloptions virtual row renders `deduplicate_items=on`
	// (or `off`), which pg_dump re-emits as `CREATE INDEX … WITH
	// (deduplicate_items='on')`. goopg does not perform btree posting-list
	// deduplication, so this is advisory catalog/dump-only. DU-002 slice 219.
	DeduplicateItems *bool
	// FastUpdate stores the GIN `WITH (fastupdate=on|off)` boolean storage
	// parameter. nil means unset (PG default ON) — no reloption is emitted, so a
	// plain index dumps byte-identically. When set, the index's
	// pg_class.reloptions virtual row renders `fastupdate=on` (or `off`), which
	// pg_dump re-emits as `CREATE INDEX … WITH (fastupdate='on')`. goopg has no
	// GIN pending-list, so this is advisory catalog/dump-only. DU-002 slice 220.
	FastUpdate *bool
	// GinPendingListLimit stores the GIN `WITH (gin_pending_list_limit=N)`
	// integer storage parameter (max pending-list size in kB). Zero means unset
	// (PG default -1 = use the gin_pending_list_limit GUC) — no reloption is
	// emitted, so a plain index dumps byte-identically. When set, the index's
	// pg_class.reloptions virtual row renders `gin_pending_list_limit=N`, which
	// pg_dump re-emits as `CREATE INDEX … WITH (gin_pending_list_limit='N')`.
	// goopg has no GIN pending list, so this is advisory catalog/dump-only.
	// DU-002 slice 221.
	GinPendingListLimit int
	// PagesPerRange stores the BRIN `WITH (pages_per_range=N)` integer storage
	// parameter (number of heap pages per summarized range). Zero means unset
	// (PG default 128) — no reloption is emitted, so a plain BRIN index dumps
	// byte-identically. When set, the index's pg_class.reloptions virtual row
	// renders `pages_per_range=N`, which pg_dump re-emits as `CREATE INDEX …
	// WITH (pages_per_range='N')`. goopg has no BRIN summarization, so this is
	// advisory catalog/dump-only. DU-002 slice 222.
	PagesPerRange int
	// AutoSummarize stores the BRIN `WITH (autosummarize=on|off)` boolean storage
	// parameter (summarize the previous range when a new page range is created).
	// nil means unset (PG default OFF) — no reloption is emitted, so a plain BRIN
	// index dumps byte-identically. When set, the index's pg_class.reloptions
	// virtual row renders `autosummarize=on` (or `off`), which pg_dump re-emits as
	// `CREATE INDEX … WITH (autosummarize='on')`. goopg has no BRIN
	// summarization, so this is advisory catalog/dump-only. DU-002 slice 223.
	AutoSummarize *bool
	// IsReplicaIdentity mirrors pg_index.indisreplident: true when this index
	// was selected as the table's replica identity via `ALTER TABLE …
	// REPLICA IDENTITY USING INDEX <idx>` (the table's relreplident becomes
	// 'i'). pg_dump emits the `ALTER TABLE ONLY <t> REPLICA IDENTITY USING
	// INDEX <idx>` clause at index-dump time keyed on this flag (pg_dump.c
	// dumpIndex, NOT at table-dump time). At most one index per table carries
	// it (relation_mark_replica_identity clears the others). goopg has no
	// logical replication, so this is round-trip dump fidelity only.
	// DU-002 slice 306.
	IsReplicaIdentity bool
	// IsClustered mirrors pg_index.indisclustered: true when this index was
	// selected as the table's clustering index via `CLUSTER <t> USING <idx>`
	// (or `ALTER TABLE <t> CLUSTER ON <idx>`). At most one index per table
	// carries it (mark_index_clustered clears the others). pg_dump emits a
	// trailing `ALTER TABLE <t> CLUSTER ON <idx>;` after the index's
	// CREATE INDEX / ADD CONSTRAINT at index-dump time keyed on this flag
	// (pg_dump.c dumpIndex / dumpConstraint). goopg performs no physical
	// heap reorder, so this is round-trip dump fidelity only. DU-002 slice 320.
	IsClustered bool
}

// reloptionList returns the index's storage parameters as ordered key=value
// pairs (value unquoted), in the stable declaration order PG stores them in
// pg_class.reloptions (fillfactor first). Callers format them: the pg_class
// virtual row joins them bare (`{fillfactor=70,deduplicate_items=off}`) while
// pg_get_indexdef single-quotes each value via flatten_reloptions
// (`WITH (fillfactor='70', deduplicate_items='off')`). DU-002 slices 218/219.
func (idx *Index) reloptionList() [][2]string {
	var opts [][2]string
	if idx.Fillfactor != 0 {
		opts = append(opts, [2]string{"fillfactor", strconv.Itoa(idx.Fillfactor)})
	}
	if idx.DeduplicateItems != nil {
		v := "off"
		if *idx.DeduplicateItems {
			v = "on"
		}
		opts = append(opts, [2]string{"deduplicate_items", v})
	}
	if idx.FastUpdate != nil {
		v := "off"
		if *idx.FastUpdate {
			v = "on"
		}
		opts = append(opts, [2]string{"fastupdate", v})
	}
	if idx.GinPendingListLimit != 0 {
		opts = append(opts, [2]string{"gin_pending_list_limit", strconv.Itoa(idx.GinPendingListLimit)})
	}
	if idx.PagesPerRange != 0 {
		opts = append(opts, [2]string{"pages_per_range", strconv.Itoa(idx.PagesPerRange)})
	}
	if idx.AutoSummarize != nil {
		v := "off"
		if *idx.AutoSummarize {
			v = "on"
		}
		opts = append(opts, [2]string{"autosummarize", v})
	}
	return opts
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
	UpdateRelStats(table *Table, pages int, tuples int64)
	AddColumn(table *Table, col Column) (*Column, error)
	DropTable(name parser.ObjectName) error
	DropIndex(name parser.ObjectName) error
	// TablesInSchema returns the names of all non-virtual user tables in the given
	// schema.  Used by DROP SCHEMA CASCADE. M0097-0020.
	TablesInSchema(schemaName string) []parser.ObjectName
	// SchemaExists reports whether a schema has been registered.
	SchemaExists(name string) bool
	// SchemaOID returns the OID of a registered schema (0 if not found). Used by
	// syncTableToCatalogHeap to stamp a user table's pg_class.relnamespace with
	// the real schema OID so the schema survives a restart (M0110-0003).
	SchemaOID(name string) uint32
	// RegisterSchema records a user-created schema. M0097-drop_if_exists.
	RegisterSchema(name string)
	// UnregisterSchema removes a schema from the registry. M0097-drop_if_exists.
	UnregisterSchema(name string)
	// CreateExtension records a CREATE EXTENSION install in the runtime
	// pg_extension registry. schema is the install namespace name (defaulted by
	// the caller), version the extension version string. When the extension
	// already exists it returns nil if ifNotExists is set, else an error.
	// database scopes the install to the connecting database (pg_extension is
	// per-database in PostgreSQL); empty means visible everywhere.
	// M0110-0003 (amcheck SQL surface).
	CreateExtension(name, schema, version, database string, ifNotExists bool) error
	// CreateTablespace records a CREATE TABLESPACE in the runtime tablespace
	// registry and returns the freshly allocated OID (used by the executor to
	// create the in-place pg_tblspc/<oid> directory). An existing name returns a
	// "tablespace already exists" error. M0095-0003 (in-place tablespace).
	CreateTablespace(name, owner, location string) (uint32, error)
	// DropTablespace removes a tablespace from the runtime registry, returning its
	// OID and whether it was present. M0095-0003.
	DropTablespace(name string) (uint32, bool)
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
	// RoleOID returns the OID minted for a registered role (or 10 for the seeded
	// `postgres` superuser); the bool is false for an unknown role. DU-002 slice 330.
	RoleOID(name string) (uint32, bool)
	// IsPredefinedRole reports whether name is one of PG18's 16 built-in
	// "pg_*" predefined roles (pg_authid.dat) — a fixed, install-time fact,
	// not the user-created `roles` registry. M0119-0004-ACLHEAP.
	IsPredefinedRole(name string) bool
	// GrantTablePrivilege records that role may exercise priv on the relation
	// identified by relOID. priv is an upper-cased keyword ("TRUNCATE", …); role
	// is matched case-insensitively. Minimal ACL store for the *-conflict
	// isolation specs. M0118-0008 (design 0118-0039).
	GrantTablePrivilege(relOID uint32, role, priv string)
	// GrantTablePrivilegeWithGrantOption is GrantTablePrivilege plus a grant-
	// option flag: when withGrantOption is true the privilege is recorded as
	// GRANT … WITH GRANT OPTION, so the materialized relacl renders the privilege
	// letter with a trailing `*` and pg_dump re-emits the WITH GRANT OPTION
	// clause. DU-002 slice 332.
	GrantTablePrivilegeWithGrantOption(relOID uint32, role, priv string, withGrantOption bool)
	// GrantTablePrivilegeAs is GrantTablePrivilegeWithGrantOption plus an
	// explicit grantor role name, recorded as the aclitem's true "/grantor"
	// component (default "postgres", the object owner, when grantor is empty)
	// instead of always attributing the grant to the owner. M0119-0004-ACLHEAP
	// (grantor half).
	GrantTablePrivilegeAs(relOID uint32, role, priv string, withGrantOption bool, grantor string)
	// RevokeTablePrivilege removes a single privilege (priv, upper-cased keyword)
	// for role on relOID from the ACL store. When the role's privilege set
	// becomes empty its entry is dropped entirely, so the materialized relacl no
	// longer lists that grantee — matching PostgreSQL, which removes an aclitem
	// once its mask is empty and lets the relacl fall back to the owner default.
	// A revoke of a privilege the role never held is a no-op. DU-002 slice 338.
	RevokeTablePrivilege(relOID uint32, role, priv string)
	// MaterializeOwnerACL records an explicit owner aclitem for relOID holding
	// exactly ownerPrivs (the owner's full default privilege set for the object
	// type), but only when no explicit owner entry exists yet. PostgreSQL leaves
	// relacl NULL while the owner holds its implicit default privileges; the
	// first owner-side REVOKE materializes the owner's default set so the
	// remaining privileges can be stored explicitly. Calling this before a
	// RevokeTablePrivilege against the owner therefore yields the PG-accurate
	// materialized relacl (owner default minus the revoked bits) instead of a
	// NULL fallback. A no-op once an owner entry exists. DU-002 slice 340.
	MaterializeOwnerACL(relOID uint32, owner string, ownerPrivs []string)
	// HasTablePrivilege reports whether role was granted priv on relOID.
	HasTablePrivilege(relOID uint32, role, priv string) bool
	// ProcACLText renders the materialized pg_proc.proacl text for a routine OID,
	// or "" when proacl is still NULL (no GRANT/REVOKE recorded). The function
	// REVOKE recorder uses the NULL result to decide whether to expand the
	// implicit acldefault('f', …) on the first mutation. DU-002 slice 347.
	ProcACLText(procOID uint32) string
	// TypeACLText renders the materialized pg_type.typacl text for a type/domain
	// OID, or "" when typacl is still NULL (no GRANT/REVOKE recorded). A type's
	// acldefault('T', owner) grants USAGE to BOTH the owner and PUBLIC, exactly
	// like a function's EXECUTE default, so the projection is identical to
	// ProcACLText with the USAGE privilege. Unlike proacl, pg_type is heap-backed
	// (M0097-0022), so the GRANT path must re-sync this text into the heap row;
	// this renderer supplies the canonical aclitem[] text. M0119-0004-ACLHEAP.
	TypeACLText(typeOID uint32) string
	// ForeignServerOID returns the stable OID of the named foreign server
	// (CREATE SERVER), or 0 if not found. Used by the FOREIGN SERVER GRANT
	// recorder (internal/server/grant_ddl.go) to resolve the object named in
	// `GRANT … ON FOREIGN SERVER <name> TO …` to the OID-keyed ACL store key.
	// DU-002 slice 427.
	ForeignServerOID(name string) uint32
	// ForeignDataWrapperOID returns the stable OID of the named FDW (CREATE
	// FOREIGN DATA WRAPPER), or 0 if not found. Used by the FOREIGN DATA WRAPPER
	// GRANT recorder (internal/server/grant_ddl.go) to resolve the object named
	// in `GRANT … ON FOREIGN DATA WRAPPER <name> TO …` to the OID-keyed ACL
	// store key. DU-002 slice 428.
	ForeignDataWrapperOID(name string) uint32
	// GrantColumnPrivilege / GrantColumnPrivilegeWithGrantOption record a
	// column-level (pg_attribute.attacl) GRANT of priv on column attNum of
	// relOID to role. RevokeColumnPrivilege removes one. AttrACLText renders the
	// materialized attacl text for a column, or "" when it is still NULL. Unlike
	// relacl/typacl/proacl a column has an empty acldefault, so attacl carries no
	// owner/PUBLIC default entry and returns to NULL once the last column
	// privilege is revoked. M0119-0004-ACLHEAP (attacl half).
	GrantColumnPrivilege(relOID uint32, attNum int16, role, priv string)
	GrantColumnPrivilegeWithGrantOption(relOID uint32, attNum int16, role, priv string, withGrantOption bool)
	// GrantColumnPrivilegeAs is GrantColumnPrivilegeWithGrantOption plus an
	// explicit grantor, the column analogue of GrantTablePrivilegeAs.
	// M0119-0004-ACLHEAP (attacl grantor half).
	GrantColumnPrivilegeAs(relOID uint32, attNum int16, role, priv string, withGrantOption bool, grantor string)
	RevokeColumnPrivilege(relOID uint32, attNum int16, role, priv string)
	AttrACLText(relOID uint32, attNum int16) string
	// DropTableACL forgets all privileges recorded for relOID (called when the
	// relation is dropped so a recycled OID does not inherit stale grants).
	DropTableACL(relOID uint32)
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
	// ListUserAggregates returns every registered user-defined aggregate, for
	// pg_proc/pg_aggregate introspection (pg_dump CREATE AGGREGATE round-trip).
	ListUserAggregates() []*UserAggregate
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
	// LookupEnum finds a user-defined enum type by name (case-insensitive).
	// Exposed on the interface so the catalog-row builders can resolve an enum
	// column's type name to its pg_type OID. DU-002 slice 88.
	LookupEnum(name string) (*EnumType, bool)
	// LookupEnumByOID finds a user-defined enum type by its pg_type OID, used by
	// format_type to render an enum column's declared type. DU-002 slice 88.
	LookupEnumByOID(oid uint32) (*EnumType, bool)
	// LookupEnumByArrayOID finds a user-defined enum type by the pg_type OID of
	// its auto-generated array type (`_name`), used by format_type to render an
	// enum-array column (`mood[]`). DU-002 slice 89.
	LookupEnumByArrayOID(oid uint32) (*EnumType, bool)
	// LookupDomain finds a user-defined domain type by name (case-insensitive).
	// Exposed on the interface so the catalog-row builders can resolve a domain
	// column's type name to its pg_type OID. DU-002 slice 90.
	LookupDomain(name string) (*Domain, bool)
	// LookupDomainByOID finds a user-defined domain type by its pg_type OID, used
	// by format_type to render a domain column's declared type. DU-002 slice 90.
	LookupDomainByOID(oid uint32) (*Domain, bool)
	// LookupDomainByArrayOID finds a user-defined domain type by the pg_type OID
	// of its auto-generated array type (`_name`), used by format_type to render a
	// domain-array column (`d[]`) as the schema-qualified array name. DU-002
	// slice 251.
	LookupDomainByArrayOID(oid uint32) (*Domain, bool)
	// LookupCompositeType returns the OID-bearing metadata for a composite type
	// by name (case-insensitive), or nil. Exposed on the interface so the
	// catalog-row builders can re-resolve a composite field whose declared type
	// is itself a user-defined composite type. DU-002 slice 249.
	LookupCompositeType(name string) *CompositeType
	// LookupCompositeTypeByOID finds a user-defined composite type by its pg_type
	// OID, used by format_type to render a nested-composite column's declared
	// type as its schema-qualified name. DU-002 slice 249.
	LookupCompositeTypeByOID(oid uint32) (*CompositeType, bool)
	// LookupCompositeTypeByArrayOID finds a user-defined composite type by the
	// pg_type OID of its auto-generated array type (`_name`), used by format_type
	// to render a composite-array field (`addr[]`) as the schema-qualified array
	// name. DU-002 slice 250.
	LookupCompositeTypeByArrayOID(oid uint32) (*CompositeType, bool)
	// LookupRangeType finds a user-defined range type by name (case-insensitive).
	// Exposed on the interface so the catalog-row builders can resolve a range
	// (or multirange) column's declared type name to its pg_type OID, mirroring
	// LookupEnum/LookupCompositeType. DU-002 slice 429 follow-up.
	LookupRangeType(name string) (*RangeType, bool)
	// LookupRangeTypeByMultirangeName finds a user-defined range type by its
	// auto-generated multirange type's name (case-insensitive) — a column can be
	// declared directly with the multirange name (e.g. `mymultirange`), not only
	// the range name. DU-002 slice 429 follow-up.
	LookupRangeTypeByMultirangeName(name string) (*RangeType, bool)
	// LookupRangeTypeByOID finds a user-defined range type by its pg_type OID,
	// used by format_type to render a range column's declared type. M0110-0001.
	LookupRangeTypeByOID(oid uint32) (*RangeType, bool)
	// LookupRangeTypeByMultirangeOID finds a user-defined range type by the
	// pg_type OID of its auto-generated multirange type, used by format_type
	// to resolve pg_range.rngmultitypid. M0110-0001.
	LookupRangeTypeByMultirangeOID(oid uint32) (*RangeType, bool)
	// LookupRangeTypeByArrayOID finds a user-defined range type by the
	// pg_type OID of its auto-generated `_name` array type, used by
	// format_type to render a `myrange[]` column. M0110-0001.
	LookupRangeTypeByArrayOID(oid uint32) (*RangeType, bool)
	// LookupRangeTypeByMultirangeArrayOID finds a user-defined range type by
	// the pg_type OID of its multirange's auto-generated `_name` array type,
	// used by format_type to render a `mymultirange[]` column. M0110-0001.
	LookupRangeTypeByMultirangeArrayOID(oid uint32) (*RangeType, bool)
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
	// drained by `DropDatabase`. At startup the catalog seeds the
	// three bootstrap databases `postgres`, `template1` and
	// `template0` (mirroring initdb's pg_database); the recovery
	// driver in `internal/initdb` re-applies WAL-logged
	// CREATE/DROP DATABASE events on top. v0 still routes every
	// relation through DefaultDBOid — the registry exists so
	// `pg_database` returns truthful rows and so connections to
	// recovered databases succeed after a crash, NOT for
	// per-database storage isolation (that lands later).
	databases map[string]bool
	// databaseConnLimit holds runtime `pg_database.datconnlimit` overrides
	// written via `UPDATE pg_database SET datconnlimit = ... WHERE datname =
	// ...` (M-NIGHTLY AI-20260707-000712-004 follow-up / AC-002 residual #1).
	// Absent entries report 0 (PG's "no limit" default), matching the
	// hard-coded value pg_database.VirtualRows used before this map existed.
	databaseConnLimit map[string]int32
	// dbRoleSettings holds per-database `ALTER DATABASE name SET config =
	// value` overrides (pg_db_role_setting, setrole=0 scope only — ALTER
	// ROLE ... SET / ALTER ROLE ... IN DATABASE ... SET are a separate,
	// unimplemented feature). Keyed by FirstUserOID (16384) — the SAME
	// SQL-visible placeholder pg_database.VirtualRows displays for every
	// non-template database (not c.DBOID(), the real on-disk physical OID
	// used to key the datacl ACL store / heap resync: pg_db_role_setting is
	// a pure virtual table with no heap to resync, and pg_dump's
	// dumpDatabaseConfig cross-references setdatabase against the oid it
	// already read from pg_database, so the two must agree). Each value is
	// an ordered list of "name=value" entries in PG's
	// pg_db_role_setting.setconfig on-disk format (mirrors guc.c's
	// flatten_set_variable_args output); SET replaces an existing
	// same-name entry in place or appends, RESET removes the matching
	// entry, RESET ALL clears the whole slice. M0119-0004-ACLHEAP (ALTER
	// DATABASE ... SET follow-up).
	dbRoleSettings map[uint32][]string
	// roleSettings holds `ALTER ROLE name [IN DATABASE dbname] SET config =
	// value` overrides (pg_db_role_setting, setrole != 0 rows — the
	// complement of dbRoleSettings' setrole=0 rows). Keyed by
	// roleSettingKey{RoleOID, DBOid}: DBOid is 0 for a plain cluster-wide
	// `ALTER ROLE ... SET` (setdatabase=0, applies in every database) or
	// FirstUserOID for the `IN DATABASE` form scoped to goopg's single live
	// database (mirrors dbRoleSettings' FirstUserOID keying — the same SQL
	// -visible placeholder oid pg_database.VirtualRows displays). Entry
	// format matches dbRoleSettings (ordered "name=value" strings).
	// M0119-0004-ACLHEAP (ALTER ROLE ... SET follow-up).
	roleSettings map[roleSettingKey][]string
	// roleMembers holds `GRANT <role> TO <role>` role-membership rows
	// (pg_auth_members). Keyed by (RoleOID, MemberOID) — PG allows only one
	// membership row per (roleid, member) pair; a re-GRANT updates the
	// existing row's grantor/admin_option in place rather than duplicating it
	// (mirrors AddRoleMems' ON CONFLICT DO UPDATE, user.c). GRANT/REVOKE ROLE
	// membership (M0119-0004-ACLHEAP).
	roleMembers map[roleMembershipKey]*RoleMembership

	// partitionChildren maps parent table OID → slice of child OIDs
	// for partitioned-table support (M0096-0007).
	partitionChildren map[uint32][]uint32
	// pendingAttachXID maps a partition child's OID → the XID of the
	// transaction whose ATTACH PARTITION has cloned a foreign key onto that
	// child but not yet committed (the registration is deferred to COMMIT, so
	// IsPartitionChild is still false during this window). A concurrent DELETE
	// of a referenced row consults this to wait for the in-flight attach,
	// mirroring PostgreSQL's RI_Initial_Check holding FOR KEY SHARE on the
	// referenced rows until the attaching transaction commits. fk-partitioned-1
	// concurrent Class B (design 0118-0120).
	pendingAttachXID map[uint32]uint32
	// indexPartitionChildren maps parent index OID → slice of child index OIDs
	// for partition index trees (ALTER INDEX parent ATTACH PARTITION child). M0097-0023.
	indexPartitionChildren map[uint32][]uint32

	// toastRenames maps a synthetic TOAST relation/index OID (parent OID +
	// toastRelidOffset for the relation, + toastIndexOidOffset for its index) to
	// a new unqualified name set by ALTER TABLE/INDEX … RENAME under
	// allow_system_table_mods. The synthetic TOAST pg_class/pg_index rows live
	// only in the virtual builders (not c.tables), so a rename cannot mutate a
	// real row; this override is consulted by the pg_class builder (relname),
	// ToastRelName (regclass rendering) and LookupToastRel (name→OID). The
	// reind_con_toast spec renames pg_toast_<oid> → reind_con_toast and its
	// _index → reind_con_toast_idx so REINDEX … CONCURRENTLY can name them
	// deterministically. Not transaction-scoped (only this spec mutates synthetic
	// TOAST objects, always in autocommit). M0118-0008 TOAST-exposure slice 4
	// (design 0118-0087).
	toastRenames map[uint32]string

	// pendingPartitionDetachCount counts partition children currently marked
	// "detach pending" (DetachPendingEpoch != 0) by an in-progress
	// ALTER TABLE … DETACH PARTITION … CONCURRENTLY. Maintained by
	// MarkPartitionDetachPending / ClearPartitionDetachPending so
	// HasPendingPartitionDetach is O(1). Zero in the steady state. Design
	// 0118-0058 (M0118-0008 detach-partition snapshot visibility).
	pendingPartitionDetachCount int

	// inheritanceChildren maps parent table OID → slice of child OIDs
	// for table inheritance support (M0096-0009).
	inheritanceChildren map[uint32][]uint32

	// pendingInheritanceChangeCount counts ALTER TABLE … {NO} INHERIT operations
	// issued inside an in-progress explicit transaction whose catalog mutation is
	// deferred to COMMIT (so the change is invisible to concurrent sessions until
	// commit — PG transactional-DDL visibility, alter-table-4 isolation spec).
	// While > 0 the cross-session plan cache is bypassed so a query scanning the
	// parent re-plans against the current (pre-commit) child set rather than reuse
	// a plan baked across the inheritance change. Maintained by
	// MarkInheritanceChangePending / UnmarkInheritanceChangePending; zero in the
	// steady state. Design 0118-0080 (M0118-0008).
	pendingInheritanceChangeCount int

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
	// compositeTypes holds OID-bearing metadata for composite types created via
	// CREATE TYPE ... AS (...), so a pg_type heap row (typtype='c') can be
	// synthesized for pg_dump / catalog parity. DU-002 slice 242.
	compositeTypes map[string]*CompositeType
	// rangeTypes holds user-defined range types created via
	// CREATE TYPE ... AS RANGE (subtype = ..., ...), keyed by lower-case name,
	// so pg_type (typtype='r'/'m') and pg_range heap/virtual rows can be
	// synthesized for pg_dump / catalog parity. DU-002 (M0110-0001).
	rangeTypes map[string]*RangeType

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

	// userCollations holds collations created via CREATE COLLATION, in
	// creation order (deterministic pg_dump output). Surfaced as extra rows in
	// the virtual pg_collation view so pg_dump round-trips them. M0119-0004.
	userCollations []*UserCollation

	// userConversions holds conversions created via CREATE [DEFAULT] CONVERSION,
	// in creation order (deterministic pg_dump output). Surfaced as rows in the
	// virtual pg_conversion view so pg_dump's getConversions / dumpConversion
	// round-trip them. DU-002 slice 399 (M0119-0004).
	userConversions []*UserConversion

	// userTSDicts holds text search dictionaries created via CREATE TEXT SEARCH
	// DICTIONARY, in creation order (deterministic pg_dump output). Surfaced as
	// rows in the virtual pg_ts_dict view so pg_dump's getTSDictionaries /
	// dumpTSDictionary round-trip them. DU-002 slice 437 (M0119-0004).
	userTSDicts []*UserTSDict

	// userTSConfigs holds text search configurations created via CREATE TEXT
	// SEARCH CONFIGURATION (+ their ADD MAPPING entries), in creation order.
	// Surfaced as rows in the virtual pg_ts_config / pg_ts_config_map views so
	// pg_dump's getTSConfigurations / dumpTSConfig round-trip them. DU-002
	// slice 446 (M0119-0004).
	userTSConfigs []*UserTSConfig

	// schemas tracks user-created schemas (CREATE SCHEMA). Pre-populated
	// with the standard system schemas. Maps lowercase schema name → OID.
	// Used to detect schema-qualified drops and for pg_namespace. M0097-drop_if_exists.
	schemas map[string]uint32

	// schemaOwners maps lowercase schema name → owning role OID, set by
	// `ALTER SCHEMA name OWNER TO role` (DU-002 slice 440 resume point (3),
	// M0110-0001). A schema absent from this map has no explicit owner
	// change recorded and defaults to the bootstrap superuser (OID 10),
	// matching pg_namespace.nspowner's previous hardcoded literal.
	schemaOwners map[string]uint32

	// tempNamespaces maps a session's temp-owner token ("s<id>", see
	// executor.sessionTempOwner) → the OID of that session's temporary
	// namespace (pg_temp_<id>). In PostgreSQL every backend that creates a
	// temporary object gets a per-backend namespace (pg_temp_N); pg_class /
	// pg_proc / pg_type entries for its temp objects carry that namespace OID,
	// pg_my_temp_schema() returns it, and the namespace persists (in the shared
	// pg_namespace catalog) for the life of the backend even after its temp
	// objects are dropped. goopg keeps one shared in-memory catalog, so we model
	// the per-backend namespace here: an entry is allocated lazily on the first
	// CREATE TEMPORARY object (EnsureTempNamespace) and removed on session exit
	// (DropTempNamespace). M0118-0009 (temp-schema-cleanup, design 0118-0091).
	tempNamespaces map[string]uint32

	// roles tracks user-created roles (CREATE ROLE / CREATE USER), mapping the
	// lower-cased role name to the OID minted for it at registration time. Used
	// by DROP ROLE IF EXISTS to produce proper "does not exist" notices, and by
	// pg_roles / CREATE POLICY ... TO <role> so named-role policies round-trip
	// through pg_dump (the OID lands in pg_policy.polroles and pg_dump's
	// getPolicies resolves it back to the name via pg_roles). M0097-drop_if_exists;
	// per-role OID registry added DU-002 slice 330.
	roles map[string]uint32

	// predefinedRoles maps the lower-cased name of each of PG18's 16
	// built-in "pg_*" predefined roles (predefinedRoleSeeds) to its fixed
	// OID. Unlike `roles`, this is a fixed, install-time fact — populated
	// once at construction (newPredefinedRoleMap), never mutated by
	// RegisterRole/UnregisterRole/RenameRole, and deliberately excluded from
	// AllRoleStates() so pg_authid heap sync (which already has its own
	// dedicated predefined-role writer, executor.pgAuthidPredefined) never
	// sees a duplicate row. Consulted by RoleOID/RoleExists/IsPredefinedRole
	// so predefined-role names resolve for GRANT/REVOKE role membership,
	// OWNER TO, CREATE POLICY ... TO, etc. — exactly like a real PG
	// pg_authid row — while staying immune to DROP/ALTER/RENAME ROLE (backed
	// by IsPredefinedRole's "pinned object" guard at the DROP ROLE call
	// site; ALTER/RENAME already gate through the separate server-level
	// `s.roles` registry in internal/server/role_ddl.go, unaffected by this
	// map). M0119-0004-ACLHEAP.
	predefinedRoles map[string]uint32

	// roleAttrs is the attribute/credential sidecar for `roles`, keyed by the
	// same lower-cased role name. Carries what pg_authid carries for a live
	// PostgreSQL role: LOGIN/SUPERUSER flags and the stored password verifier
	// (SCRAM-SHA-256 by default, mirroring rolpassword — upstream
	// postgres/src/backend/libpq/crypt.c encrypt_password). Populated by the
	// server-side CREATE/ALTER ROLE handler and by WAL replay
	// (internal/initdb/role_ddl_recovery.go) so roles survive a restart;
	// cmd/goopg seeds the auth UserStore from it after Open. Entries are
	// optional — a role registered without attributes reads back zero-valued
	// (no credential, LOGIN defaulting per registration path). root-0021.
	roleAttrs map[string]*RoleAttrs

	// tableACLs records per-relation privileges granted to non-owner roles, the
	// minimal ACL store the *-conflict isolation specs need (currently only
	// TRUNCATE — truncate-conflict, design 0118-0039). Keyed relOID →
	// lower-cased role name → upper-cased privilege keyword ("TRUNCATE",
	// "SELECT", …) → grant-option flag (true = GRANT … WITH GRANT OPTION, so
	// aclitemout renders the privilege letter with a trailing `*`). The owning
	// superuser bypasses this map entirely; an empty/absent entry means "no
	// privilege granted". M0118-0008; grant-option DU-002 slice 332.
	tableACLs map[uint32]map[string]map[string]bool

	// tableACLGrantor records, per relOID, the grantor role that performed each
	// non-owner grantee's most recent GRANT — the real PostgreSQL aclitem's
	// trailing "/grantor" component, which goopg otherwise hardcodes to the
	// object owner ("postgres") in relaclTextLockedFor. A role reachable only
	// via SET ROLE / SET SESSION AUTHORIZATION (connTx.NonSuperuserRole) can
	// legitimately GRANT a privilege it holds WITH GRANT OPTION to a further
	// grantee, and real PG's pg_dump wraps that grant in `SET SESSION
	// AUTHORIZATION <grantor>; GRANT ...; RESET SESSION AUTHORIZATION;`
	// (dumputils.c buildACLCommands) once the aclitem's grantor differs from
	// the owner — but that only round-trips if goopg's own relacl string
	// carries the true grantor to begin with. Keyed like tableACLs (lower-cased
	// role name); an absent entry means "granted by the object owner", the
	// pre-existing default. Does not model PostgreSQL's full grant-option
	// delegation chain (select_best_grantor, acl.c) — the recorded grantor is
	// simply whichever role executed the GRANT statement, not the specific
	// upstream role whose grant option was exercised. M0119-0004-ACLHEAP
	// (grantor half).
	tableACLGrantor map[uint32]map[string]string

	// tableACLOrder records, per relOID, the order in which non-owner grantee
	// roles first appeared in a GRANT — PostgreSQL appends a new grantee's
	// aclitem to the end of pg_class.relacl (aclupdate in src/backend/utils/adt/
	// acl.c modifies an existing aclitem in place but appends a brand-new one),
	// so the array preserves grant order, NOT alphabetical order. relacl
	// rendering must follow this list rather than sorting role names, or a
	// reverse-order GRANT (TO z_role before a_role) would emit pg_dump GRANT
	// lines in the wrong order vs real PostgreSQL. Each role appears at most once
	// (first-grant position); a re-GRANT to an existing grantee does not move it.
	// A role is removed when its privilege set is fully revoked. Keyed by the
	// lower-cased role name to match tableACLs. The owner ("postgres") is omitted
	// (it is always rendered first, separately). DU-002 slice 354.
	tableACLOrder map[uint32][]string

	// roleACLDisplay preserves the original-case spelling of a grantee role
	// name recorded in tableACLs (which is keyed by the lower-cased name so
	// privilege lookups stay case-insensitive). Key: lower-cased role name →
	// the exact case it was spelled in the GRANT. PostgreSQL role names are
	// case-significant when double-quoted (CREATE ROLE "MixedCase"), and
	// aclitemout renders the role's true name in pg_class.relacl, so pg_dump's
	// getid/fmtId re-emit GRANT … TO "MixedCase". Without this, the lower-cased
	// store would render `mixedcase` and pg_dump would emit TO mixedcase (a
	// different, nonexistent role). DU-002 slice 337.
	roleACLDisplay map[string]string

	// relACLEmptied records relations whose ACL was explicitly materialized to an
	// empty aclitem array {} — the state PostgreSQL leaves pg_class.relacl in after
	// a `REVOKE ALL ON TABLE t FROM <owner>` (the owner's implicit default
	// privileges are stripped, leaving a non-NULL but empty array). This is
	// distinct from NULL (no GRANT ever recorded): pg_dump's buildACLCommands
	// emits a bare `REVOKE ALL … FROM <owner>;` for {} but nothing for NULL. A
	// later GRANT or owner re-materialize clears the flag. Key: relOID → present.
	// DU-002 slice 341.
	relACLEmptied map[uint32]bool

	// relACLOwnerRevoked records relations whose owner's implicit default aclitem
	// was explicitly revoked, regardless of whether other grantees survive. This
	// is the broader signal relACLEmptied is a special case of: relACLEmptied
	// fires only when the owner revoke also empties the array, but an object whose
	// acldefault grants a *non-owner* implicit privilege (a function's PUBLIC
	// EXECUTE) leaves a surviving aclitem after the owner is revoked
	// (`REVOKE EXECUTE ON FUNCTION f FROM <owner>` → {=X/owner}). In that case
	// relaclTextLockedFor must still suppress the leading owner entry (the owner is
	// absent from the array) even though it is non-empty. An owner-side GRANT
	// re-materializes the owner and clears the flag. Key: relOID → present.
	// DU-002 slice 347.
	relACLOwnerRevoked map[uint32]bool

	// attrACLs records column-level (pg_attribute.attacl) privileges granted to
	// non-owner roles, keyed by (relOID, attnum) → lower-cased role name →
	// upper-cased privilege keyword ("SELECT"/"INSERT"/"UPDATE"/"REFERENCES") →
	// grant-option flag. Unlike relacl/typacl/proacl, a column has an EMPTY
	// acldefault ('c': columns grant no implicit privilege to the owner or
	// PUBLIC), so attacl stays NULL until the first column GRANT and returns to
	// NULL once every column privilege is revoked — there is no owner or PUBLIC
	// default entry to materialize, so the relACLEmptied/relACLOwnerRevoked
	// machinery the table/type/function stores need does not apply here. This is
	// why column ACLs live in their own composite-keyed store rather than the
	// OID-keyed tableACLs map. M0119-0004-ACLHEAP (attacl half).
	attrACLs map[attrACLKey]map[string]map[string]bool

	// attrACLOrder records, per column, the order in which grantee roles first
	// appeared in a column GRANT, so attacl preserves grant order (PostgreSQL
	// appends a new grantee's aclitem to the end of the array) rather than
	// alphabetical order — the column analogue of tableACLOrder. A role is removed
	// when its column privilege set is fully revoked. M0119-0004-ACLHEAP.
	attrACLOrder map[attrACLKey][]string

	// attrACLGrantor records, per column, the grantor role that performed each
	// grantee's most recent column-level GRANT — the column analogue of
	// tableACLGrantor. An absent entry means "granted by the object owner", the
	// pre-existing default. M0119-0004-ACLHEAP (attacl grantor half).
	attrACLGrantor map[attrACLKey]map[string]string

	// parameterACLOIDs assigns a synthetic pg_parameter_acl.oid to each
	// GUC-level `GRANT SET|ALTER SYSTEM ON PARAMETER <name> ...` target, keyed
	// by the lower-cased dotted parameter name (mirroring
	// convert_GUC_name_for_parameter_acl, guc.c). Unlike typacl/datacl,
	// pg_parameter_acl has no backing real-world object to look an OID up
	// against — PostgreSQL lazily creates the row on first GRANT
	// (ParameterAclCreate) — so goopg mints one from the shared nextOID
	// counter the same way. The OID is otherwise a plain key into the shared
	// tableACLs store (privileges rendered via ParameterACLText).
	// M0119-0004-ACLHEAP (parameter ACL half).
	parameterACLOIDs map[string]uint32

	// parameterACLNames is the reverse of parameterACLOIDs (oid → original
	// lower-cased parname), so pg_parameter_acl's VirtualRows can project
	// every GUC that has ever been granted, in a stable order.
	// M0119-0004-ACLHEAP (parameter ACL half).
	parameterACLNames map[uint32]string

	// defaultACLOIDs assigns a synthetic pg_default_acl.oid to each
	// `ALTER DEFAULT PRIVILEGES [FOR ROLE ...] [IN SCHEMA ...] ...` target
	// triple (defaclrole, defaclnamespace-or-0, defaclobjtype), keyed by
	// defaultACLKey — mirrors parameterACLOIDs' lazy-minting pattern (real
	// PostgreSQL only rows a pg_default_acl tuple once a GRANT/REVOKE
	// materializes one, and deletes it again once the ACL returns to its
	// implicit default; SetDefaultACL, aclchk.c). defaultACLKeys is the
	// reverse lookup, so pg_default_acl's VirtualRows can project every
	// minted triple. M0110-0001 (DU-002 slice 438 follow-up).
	defaultACLOIDs map[defaultACLKey]uint32
	defaultACLKeys map[uint32]defaultACLKey

	// defaultACLGlobal records, per minted OID, whether the entry is a
	// global default (no IN SCHEMA — defaclnamespace = 0) or a
	// schema-scoped one. A global entry's implicit baseline is the target
	// role's full acldefault() rights for the object type (merged in via the
	// shared tableACLs/relaclTextLockedFor owner-injection machinery, exactly
	// like pg_class.relacl); a schema-scoped entry's baseline is empty (no
	// implicit owner entry at all) — real PostgreSQL's SetDefaultACL seeds
	// `old_acl` from acldefault(objtype, roleid) only when nspid is invalid
	// (aclchk.c). M0110-0001 (DU-002 slice 438 follow-up).
	defaultACLGlobal map[uint32]bool

	// compatObjects tracks objects created via noop CompatNoopStmt (e.g. CREATE CONVERSION,
	// CREATE OPERATOR). Key: objType (e.g. "conversion") → set of names. M0097-drop_if_exists.
	compatObjects map[string]map[string]struct{}

	// fdws tracks foreign-data wrappers (CREATE FOREIGN DATA WRAPPER) with a
	// stable OID so they round-trip through pg_dump's getForeignDataWrappers
	// (pg_foreign_data_wrapper virtual view → dumpForeignDataWrapper). goopg does
	// not execute FDWs; only enough metadata is kept for dump fidelity. Key:
	// fdwname. DU-002 slice 375.
	fdws map[string]*ForeignDataWrapper

	// accessMethods tracks user-defined access methods (CREATE ACCESS METHOD)
	// with a stable OID so they round-trip through pg_dump's getAccessMethods
	// (pg_am virtual view → dumpAccessMethod). goopg never invokes a
	// user-defined AM (no pluggable storage engine); only enough metadata is
	// kept for dump fidelity. Key: amname. DU-002 (M0119-0004).
	accessMethods map[string]*AccessMethod

	// eventTriggers tracks event triggers (CREATE EVENT TRIGGER) with a stable
	// OID so they round-trip through pg_dump's getEventTriggers
	// (pg_event_trigger virtual view → dumpEventTrigger). goopg does not fire
	// event triggers; only enough metadata is kept for dump fidelity. Key:
	// evtname. DU-002 (M0119-0004).
	eventTriggers map[string]*EventTrigger

	// foreignServers tracks foreign servers (CREATE SERVER) with a stable OID so
	// they round-trip through pg_dump's getForeignServers (pg_foreign_server
	// virtual view → dumpForeignServer). Each server references its FDW by name;
	// the srvfdw OID is resolved from fdws at render time. goopg does not execute
	// foreign servers; only enough metadata is kept for dump fidelity. Key:
	// srvname. DU-002 slice 376.
	foreignServers map[string]*ForeignServer

	// userMappings tracks user mappings (CREATE USER MAPPING FOR <user> SERVER
	// <server>) with a stable OID so they round-trip through pg_dump's
	// dumpUserMappings (pg_user_mappings virtual view, queried per foreign server).
	// goopg does not execute foreign access; only enough metadata is kept for dump
	// fidelity. Key: lowercase "<user>\x00<server>". DU-002 slice 377.
	userMappings map[string]*UserMapping

	// casts tracks user-defined casts (CREATE CAST) with a stable OID so they
	// round-trip through pg_dump's getCasts (pg_cast virtual view → dumpCast).
	// goopg does not perform user casts; only enough metadata is kept for dump
	// fidelity (source/target type OIDs, castcontext, castmethod; castfunc stays 0
	// for WITHOUT FUNCTION / WITH INOUT). Key: lowercase "<source>\x00<target>".
	// DU-002 slice 395.
	casts map[string]*Cast

	// transforms tracks user-defined transforms (CREATE TRANSFORM) with a
	// stable OID so they round-trip through pg_dump's getTransforms
	// (pg_transform virtual view → dumpTransform). goopg does not execute the
	// transform machinery (PL-language argument marshaling); only enough
	// metadata is kept for dump fidelity (type/language OIDs, from/to function
	// OIDs). Key: lowercase "<type>\x00<lang>". DU-002 (M0119-0004).
	transforms map[string]*Transform

	// userOperators tracks user-defined operators (CREATE OPERATOR) with a
	// stable OID so they round-trip through pg_dump's getOperators (pg_operator
	// virtual view → dumpOpr). goopg does not execute the operator (no runtime
	// dispatch through a custom FUNCTION); only enough metadata is kept for
	// dump fidelity (left/right/result type OIDs, oprcode). Key: lowercase
	// "<schema>.<name>(<leftType>,<rightType>)" to allow the same operator
	// symbol to be overloaded across schemas/arg-type pairs, mirroring
	// dropCompat's operator key shape. DU-002 (M0119-0004).
	userOperators map[string]*UserOperator

	// userOperatorFamilies tracks user-defined operator families (CREATE
	// OPERATOR FAMILY name USING method) with a stable OID so they round-trip
	// through pg_dump's getOpfamilies (pg_opfamily virtual view → dumpOpfamily).
	// The family starts empty (PG's CREATE OPERATOR FAMILY grammar has no AS
	// clause); ALTER OPERATOR FAMILY ... ADD to populate it is not yet
	// implemented (deferred). Key: lowercase "<schema>.<name>/<method-oid>" —
	// PG allows the same family name to be reused across access methods.
	// DU-002 (M0119-0004).
	userOperatorFamilies map[string]*UserOperatorFamily

	// userOperatorClasses tracks user-defined operator classes (CREATE
	// OPERATOR CLASS name [DEFAULT] FOR TYPE type USING method [FAMILY
	// family] AS ...) with a stable OID so the class's own pg_opclass row
	// round-trips through pg_dump's getOpclasses (pg_opclass virtual view →
	// dumpOpclass). Only the class-level attributes (method/family/intype/
	// default/keytype) are modeled; OPERATOR/FUNCTION entries tied to the
	// class via pg_amop/pg_amproc + pg_depend are not yet implemented
	// (deferred — see the ledger). Key: lowercase "<schema>.<name>/<method-
	// oid>", mirroring userOperatorFamilies (PG scopes opclass-name
	// uniqueness per namespace+access method too). DU-002 (M0119-0004).
	userOperatorClasses map[string]*UserOperatorClass

	// amOpMembers / amProcMembers back pg_amop/pg_amproc: one entry per
	// resolved OPERATOR/FUNCTION entry in a CREATE OPERATOR CLASS ... AS
	// list. Only user-defined operators/functions are resolvable today
	// (goopg has no builtin-operator catalog for regoper-style name lookup;
	// FUNCTION support procs additionally check the small hand-curated
	// LookupBuiltinProc set) — an entry naming an unresolvable builtin is
	// silently dropped, same as the pre-slice-411 behavior for every entry
	// (deferred, see the ledger). Appended, not keyed: CREATE OPERATOR CLASS
	// has no re-create/replace form. DU-002 (M0119-0004) slice 411.
	amOpMembers   []*AmOpMember
	amProcMembers []*AmProcMember

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

	// tablespaces tracks CREATE TABLESPACE in-place tablespaces, keyed by
	// lowercase tablespace name. The bootstrap pg_default/pg_global tablespaces
	// are NOT held here (they live in the on-disk pg_tablespace heap); this is the
	// runtime registry for developer/regression in-place tablespaces, used to
	// reject duplicates and to map a dropped name back to its OID/directory.
	// M0095-0003 (in-place tablespace).
	tablespaces map[string]*tablespaceRow

	// dbACLChangeXID is the XID of the most recent transaction that performed a
	// GRANT/REVOKE … ON DATABASE (a pg_database ACL change). PostgreSQL records
	// no heavyweight lock for an ACL change — the lock IS the catalog tuple's
	// xmax — so a concurrent in-place update of the same pg_database row
	// (VACUUM advancing datfrozenxid via heap_inplace_update_scan) must wait for
	// that xmax XID to commit/abort before it can rewrite the tuple. goopg has no
	// real pg_database heap tuple, so we record the writer XID here and have a
	// database-wide VACUUM wait on it (mvcc.WaitForXID). Atomic so the VACUUM
	// reader never contends on c.mu. Design 0118-0098 (intra-grant-inplace-db).
	dbACLChangeXID atomic.Uint32

	// tableACLChangeXID maps a table OID → the XID of the most recent transaction
	// that performed a GRANT/REVOKE … ON [TABLE] <name> (a pg_class ACL change).
	// As with dbACLChangeXID, PostgreSQL records no heavyweight lock for the ACL
	// change — its lock is the pg_class tuple's xmax — so a concurrent in-place
	// update of the same row (ALTER TABLE ADD PRIMARY KEY setting relhasindex via
	// heap_inplace_update) must wait for that XID to commit/abort. goopg has no
	// real pg_class heap tuple, so we record the writer XID here and have ADD
	// PRIMARY KEY wait on it (mvcc.WaitForXID). Design 0118-0109
	// (intra-grant-inplace).
	tableACLChangeXID   map[uint32]uint32
	tableACLChangeXIDMu sync.Mutex

	// tablePendingDropXID maps a table OID → the XID of the in-flight transaction
	// that issued a `DROP TABLE` deferred to COMMIT (the catalog row is kept
	// visible until then). PostgreSQL's DROP performs a heap_delete on the
	// pg_class tuple, stamping its xmax; a concurrent explicit rowmark
	// (`SELECT … FROM pg_class … FOR UPDATE`) or in-place updater must wait on
	// that xmax before proceeding, and once the DROP commits the tuple is gone so
	// the locker finds no row. goopg has no real pg_class heap tuple, so the
	// deferred DROP records its writer XID here and the pg_class rowmark waits on
	// it (mvcc.WaitForXID), exactly like the ACL-change xmax above. Design
	// 0118-0117 (intra-grant-inplace perm 10).
	tablePendingDropXID   map[uint32]uint32
	tablePendingDropXIDMu sync.Mutex

	// pgClassRowMarks records explicit row-level locks taken on a pg_class tuple
	// by `SELECT … FROM pg_class WHERE oid = <rel> FOR { KEY SHARE | NO KEY
	// UPDATE | SHARE | UPDATE }`. PostgreSQL takes no heavyweight lock for such a
	// rowmark — it is a tuple lock recorded in the pg_class tuple's xmax (a
	// multixact when several sessions hold it). An in-place catalog update of the
	// same tuple (ALTER TABLE ADD PRIMARY KEY flipping relhasindex via
	// heap_inplace_update) must wait for any concurrent locker whose mode
	// conflicts with that no-key update — i.e. every mode except FOR KEY SHARE.
	// goopg has no real pg_class heap tuple, so we mirror the wait here: each
	// locking SELECT records (relOID → holderXID → conflictsWithInplace) and ADD
	// PRIMARY KEY waits (mvcc.WaitForXID) on every conflicting holder from another
	// transaction. Design 0118-0113 (intra-grant-inplace rowmarks).
	pgClassRowMarks   map[uint32]map[uint32]bool
	pgClassRowMarksMu sync.Mutex
}

// PgClassRowMark is one explicit pg_class tuple lock: the holder's transaction
// id and whether its lock mode conflicts with a concurrent in-place update of
// the same tuple (true for every FOR-clause strength except FOR KEY SHARE).
type PgClassRowMark struct {
	XID                  uint32
	ConflictsWithInplace bool
}

// extensionRow is one runtime CREATE EXTENSION record backing pg_extension.
// The install namespace is stored by name and resolved to an OID at read time
// (in pg_extension's VirtualRows), so it stays consistent if the schema set
// changes. M0110-0003.
//
// database records the name of the database the extension was created in. In
// PostgreSQL pg_extension is a per-database catalog, so an extension installed
// in one database is invisible in every other. goopg shares a single in-memory
// catalog across all databases, so this field is the scope marker used to
// filter pg_extension rows per connecting database (see ExtensionRowsForDB).
// Empty means "visible in every database" (legacy/direct-call inserts).
// M0110-0003 (AC-002 gap #7c).
type extensionRow struct {
	oid      uint32
	name     string
	schema   string
	version  string
	database string
}

// tablespaceRow is one runtime CREATE TABLESPACE record (in-place tablespaces
// only). The in-place directory is pg_tblspc/<oid> under the data dir. owner is
// recorded for completeness; location is the (canonicalized) LOCATION string,
// which is empty for an in-place tablespace. M0095-0003.
type tablespaceRow struct {
	oid      uint32
	name     string
	owner    string
	location string
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
	// Kinds holds the explicitly-requested statistics kinds (lowercased:
	// "ndistinct"/"dependencies"/"mcv"). Empty means the default (all kinds).
	// pg_get_statisticsobjdef re-emits a kinds clause only when not all are
	// enabled and the object spans more than one column. DU-002 slice 314.
	Kinds []string
	// Columns holds the simple column names from the ON list, in order. Used by
	// pg_get_statisticsobjdef to reconstruct the ON clause. DU-002 slice 314.
	Columns []string
	// Exprs holds the deparsed expression targets from the ON list, each already
	// rendered in its final SQL form (a non-function expression is parenthesized,
	// e.g. "(a + b)"; a bare function call stays unparenthesized, e.g. "lower(a)")
	// to mirror ruleutils.c pg_get_statisticsobj_worker. pg_get_statisticsobjdef
	// appends these after the simple columns. DU-002 slice 316.
	Exprs []string
	// HasExpr reports the ON list contained an expression target. When it is set
	// but Exprs is empty the expression could not be captured (e.g. a parse
	// fallback), so the dump path declines to reconstruct the object. DU-002
	// slices 314/316.
	HasExpr bool
	// StatTarget is the per-object statistics target set via
	// `ALTER STATISTICS ... SET STATISTICS n` (pg_statistic_ext.stxstattarget).
	// nil means unset — PG stores stxstattarget=NULL (the default), for which
	// pg_dump emits no ALTER. A non-nil value >= 0 round-trips as an
	// `ALTER STATISTICS ... SET STATISTICS <n>` after the CREATE. DU-002 slice 317.
	StatTarget *int
	// Owner is the stxowner role OID, settable via `ALTER STATISTICS ... OWNER
	// TO`. 0 means "unset, defaults to the bootstrap superuser" — see
	// OwnerOrDefault. DU-002 slice 441.
	Owner uint32
}

// OwnerOrDefault returns s.Owner, falling back to the bootstrap superuser OID
// (10) for statistics objects that never had OWNER TO applied.
func (s *StatisticsObject) OwnerOrDefault() uint32 {
	if s.Owner == 0 {
		return 10
	}
	return s.Owner
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
	Name string
	OID  uint32
	// ArrayOID is the pg_type OID of the auto-generated array type (`_name`)
	// that PostgreSQL creates alongside every enum. It is allocated from the
	// same running counter as OID (OID, then ArrayOID) so a `mood[]` column
	// can resolve to a distinct, stable OID and round-trip through pg_dump as
	// `public.mood[]` rather than `text[]`. DU-002 slice 89.
	ArrayOID uint32
	Values   []EnumValue // ordered by SortOrder; each element stores its own sortorder
	// Owner is the typowner role OID, settable via `ALTER TYPE ... OWNER TO`.
	// 0 means "unset, defaults to the bootstrap superuser" — see
	// OwnerOrDefault. M0122-0005 (m0097-0017 follow-up).
	Owner uint32
}

// OwnerOrDefault returns et.Owner, falling back to the bootstrap superuser OID
// (10) for enum types that never had OWNER TO applied. Mirrors
// StatisticsObject.OwnerOrDefault.
func (et *EnumType) OwnerOrDefault() uint32 {
	if et.Owner == 0 {
		return 10
	}
	return et.Owner
}

// Domain holds one user-defined domain type. M0097-0017.
type Domain struct {
	Name string
	OID  uint32
	// ArrayOID is the pg_type OID of the domain's auto-generated array type
	// (`_name`), allocated immediately after OID at CREATE DOMAIN (same running
	// counter, OID then ArrayOID) so a `d[]` column resolves to a real array
	// pg_type row and pg_dump renders it as `public.d[]` rather than the base
	// type's built-in array. Mirrors EnumType.ArrayOID / CompositeType.ArrayOID.
	// DU-002 slice 251.
	ArrayOID uint32
	Base     Type // resolved base type
	// BaseOID is the pg_type OID of the resolved base type, recorded at CREATE
	// DOMAIN time. Zero means "derive from Base.Name via TypeNameToOID" (the
	// built-in-base default). It is set explicitly for a user-defined enum base,
	// whose OID is dynamically allocated and thus not derivable from the name
	// (TypeNameToOID would fall back to text). With it the domain's pg_type row
	// carries the correct typbasetype and pg_dump renders `AS public.<enum>`.
	// DU-002 slice 109.
	BaseOID uint32
	// BaseIsEnum records that the base type is a user-defined enum, so the
	// domain inherits the enum's physical layout (4-byte, int-aligned, plain
	// storage, 'E' category) rather than the text fallback. DU-002 slice 109.
	BaseIsEnum bool
	NotNull    bool
	Default    parser.Expr // DEFAULT expression AST, nil when no DEFAULT. DU-002 slice 92.
	// Checks holds every CHECK constraint on the domain, each a separate
	// pg_constraint row (contype='c'). A domain may declare several CHECKs, so
	// this replaced the former single CheckExpr/CheckName/CheckInValues fields.
	// DU-002 slice 385 (multi-CHECK; single-CHECK was slices 96/97).
	Checks []DomainCheck
	// Owner is the typowner role OID, settable via `ALTER DOMAIN ... OWNER TO`.
	// 0 means "unset, defaults to the bootstrap superuser" — see
	// OwnerOrDefault. M0122-0005 (domain follow-up).
	Owner uint32
}

// OwnerOrDefault returns d.Owner, falling back to the bootstrap superuser OID
// (10) for domains that never had OWNER TO applied. Mirrors
// EnumType.OwnerOrDefault / RangeType.OwnerOrDefault.
func (d *Domain) OwnerOrDefault() uint32 {
	if d.Owner == 0 {
		return 10
	}
	return d.Owner
}

// DomainCheck is one CHECK constraint on a domain. Name is the resolved
// constraint name (PG's auto-generated `<domain>_check`[N] when unnamed). Expr
// is the conbin source text rendered by pg_get_constraintdef. OID is the
// allocated pg_constraint OID. InValues is non-nil for the
// `CHECK (VALUE IN (...))` form: it drives both the legacy double-paren render
// (the pre-synthesized ScalarArrayOp deparse in Expr) and the cast-time
// membership enforcement. DU-002 slice 385.
type DomainCheck struct {
	Name     string
	Expr     string
	OID      uint32
	InValues []string
}

// DefaultBin renders the domain's DEFAULT expression in the pre-formatted
// pg_node_tree form goopg stores in pg_type.typdefaultbin (the same encoding
// used for pg_attrdef.adbin). pg_dump reads it back via pg_get_expr, which is a
// pass-through in goopg, so this string is what `CREATE DOMAIN ... DEFAULT <x>`
// re-emits. Returns "" when the domain has no default. DU-002 slice 92.
func (d *Domain) DefaultBin() string {
	if d == nil || d.Default == nil {
		return ""
	}
	s := formatExprForAttrdef(d.Default)
	// pg_get_expr decorates a coerced string literal with its target type, e.g.
	// `'foo'::text`, because PG's get_const_expr emits `::type` for every Const
	// whose type is not self-evident from the literal (int4/numeric/bool are
	// printed bare). A string literal defaulting a text/varchar domain therefore
	// round-trips as `DEFAULT 'foo'::text`. Integer defaults (slice 92) stay
	// bare. DU-002 slice 93.
	if _, ok := d.Default.(*parser.StringConst); ok && d.Base.Name != "" {
		s += "::" + domainConstCastTypeName(d.Base.Name)
	}
	return s
}

// domainConstCastTypeName returns the type name PG's get_const_expr appends to a
// coerced string Const defaulting a domain — i.e. format_type(basetype, -1), the
// base type's canonical spelling WITHOUT a typmod. Most base names already match
// that spelling (text→text, uuid→uuid); the two that differ are the
// character-string types, whose user-facing aliases (varchar, char) deparse to
// their format_type names. Note char/bpchar with typmod -1 renders as "bpchar"
// (the internal name), not "character" — matching real pg_dump 18.3
// (`DEFAULT 'ab'::bpchar`). DU-002 slice 94.
func domainConstCastTypeName(baseName string) string {
	switch baseName {
	case "varchar", "character varying":
		return "character varying"
	case "char", "bpchar", "character":
		return "bpchar"
	default:
		return baseName
	}
}

// CompositeField describes one field in a user-defined composite type.
// M0097-composite.
type CompositeField struct {
	Name      string // lower-case field name
	ColType   string // column type string (e.g. "bigint", "text")
	Collation string // per-field COLLATE name (e.g. "C"); empty = type default
}

// CompositeType describes a user-defined composite type created via
// `CREATE TYPE x AS (a int, b text)`. PostgreSQL allocates a pg_type row
// (typtype='c'), an auto-generated array type (`_x`), and an implicit
// pg_class relation (relkind='c') that holds the field columns in
// pg_attribute. goopg currently synthesizes the two pg_type rows (the type
// and its array); the implicit relation + pg_attribute rows are a follow-up
// (see fix_plan DU-002). DU-002 slice 242.
type CompositeType struct {
	Name     string           // lower-case type name
	OID      uint32           // pg_type.oid of the composite type
	ArrayOID uint32           // OID of the auto-generated `_name` array type
	RelOID   uint32           // pg_class.oid of the implicit relation (relkind='c')
	Fields   []CompositeField // ordered field list
	// Owner is the typowner role OID, settable via `ALTER TYPE ... OWNER TO`.
	// 0 means "unset, defaults to the bootstrap superuser" — see
	// OwnerOrDefault. M0122-0005 (m0097-0017 follow-up).
	Owner uint32
}

// OwnerOrDefault returns ct.Owner, falling back to the bootstrap superuser OID
// (10) for composite types that never had OWNER TO applied. Mirrors
// StatisticsObject.OwnerOrDefault.
func (ct *CompositeType) OwnerOrDefault() uint32 {
	if ct.Owner == 0 {
		return 10
	}
	return ct.Owner
}

// RangeType describes a user-defined range type created via
// `CREATE TYPE x AS RANGE (subtype = ..., ...)`. PostgreSQL allocates a
// pg_type row for the range itself (typtype='r'), a pg_range row, an
// auto-generated multirange type (typtype='m', default name derived by
// makeMultirangeTypeName), and an auto-generated `_name` array type for both
// the range and the multirange (typtype='b'/typcategory='A', mirroring
// EnumType.ArrayOID / CompositeType.ArrayOID). goopg synthesizes all four
// pg_type rows. DU-002, M0110-0001.
type RangeType struct {
	Name               string // lower-case range type name
	OID                uint32 // pg_type.oid of the range type
	ArrayOID           uint32 // pg_type.oid of the range's auto-generated `_name` array type
	SubtypeName        string // subtype name as declared (e.g. "int4", "timestamp with time zone")
	OpclassOID         uint32 // btree opclass OID for the subtype, explicit `subtype_opclass` or resolved default (pg_range.rngsubopc)
	CollationOID       uint32 // collation OID for the subtype, explicit `collation` or the subtype's own typcollation; 0 (InvalidOid) if the subtype is not collatable (pg_range.rngcollation)
	MultirangeOID      uint32 // pg_type.oid of the auto-generated multirange type
	MultirangeArrayOID uint32 // pg_type.oid of the multirange's auto-generated `_name` array type
	MultirangeName     string // lower-case multirange type name
	// Owner is the typowner role OID, settable via `ALTER TYPE ... OWNER TO`.
	// 0 means "unset, defaults to the bootstrap superuser" — see
	// OwnerOrDefault. M0122-0005 (range-type follow-up to the enum/composite
	// ALTER TYPE OWNER TO/RENAME TO work).
	Owner uint32
}

// OwnerOrDefault returns rt.Owner, falling back to the bootstrap superuser OID
// (10) for range types that never had OWNER TO applied. Mirrors
// EnumType.OwnerOrDefault / CompositeType.OwnerOrDefault.
func (rt *RangeType) OwnerOrDefault() uint32 {
	if rt.Owner == 0 {
		return 10
	}
	return rt.Owner
}

// UserAggregate holds metadata for a CREATE AGGREGATE user-defined aggregate.
// It is stored in InMemory.userAggregates and looked up by lower-case name.
type UserAggregate struct {
	OID             uint32   // pg_proc.oid / pg_aggregate.aggfnoid (assigned on first RegisterUserAggregate)
	Name            string   // lower-case aggregate name
	ArgTypes        []string // base argument type names (may be empty for zero-arg like count(*))
	SType           string   // state type name
	SFunc           string   // state transition function name
	FinalFunc       string   // final function name (may be empty)
	CombineFunc     string   // combine function name for parallel agg (may be empty)
	InitCond        string   // initial condition string (may be empty)
	FinalFuncModify string   // FINALFUNC_MODIFY: "", "read_only", "shareable", "read_write" (DU-002 slice 405)
	SFuncStrict     bool     // true if sfunc is STRICT (skips NULL inputs)
	Variadic        bool     // true when declared with VARIADIC input arg
	Owner           uint32   // pg_proc.proowner (role OID); 0 means "unset, defaults to bootstrap superuser" — see OwnerOrDefault. M0119-0004.
	NamespaceOID    uint32   // pg_proc.pronamespace (schema OID); 0 means "unset, defaults to public" — see NamespaceOIDOrDefault. DU-002 slice 405 resume point (a).
}

// OwnerOrDefault returns agg.Owner, falling back to the bootstrap superuser
// OID (10) for aggregates registered before the Owner field existed (e.g.
// replayed from an older WAL record that has no owner payload).
func (agg *UserAggregate) OwnerOrDefault() uint32 {
	if agg.Owner == 0 {
		return 10
	}
	return agg.Owner
}

// NamespaceOIDOrDefault returns agg.NamespaceOID, falling back to the public
// schema OID for aggregates registered before the NamespaceOID field existed
// (e.g. replayed from an older WAL record that has no schema payload).
func (agg *UserAggregate) NamespaceOIDOrDefault() uint32 {
	if agg.NamespaceOID == 0 {
		return PublicNamespaceOID
	}
	return agg.NamespaceOID
}

// UserCollation records a collation created via CREATE COLLATION so that
// pg_dump's getCollations / dumpCollation re-emit it. goopg does not use the
// collation for actual string ordering — only the schema-dump round-trip is
// modeled. Stored in InMemory.userCollations and surfaced as extra rows in the
// virtual pg_collation view. DU-002 (M0119-0004).
type UserCollation struct {
	OID           uint32
	Name          string // collation name (bare, no schema)
	NamespaceOID  uint32 // collnamespace (resolved from the schema)
	Owner         uint32 // collowner (role OID; 10 = postgres superuser)
	Provider      byte   // collprovider: 'c' libc, 'i' icu, 'b' builtin, 'd' default
	Encoding      int    // collencoding: -1 = encoding-independent
	Collate       string // collcollate (libc lc_collate); "" → NULL
	Ctype         string // collctype (libc lc_ctype); "" → NULL
	Locale        string // colllocale (builtin/icu locale); "" → NULL
	Rules         string // collicurules (ICU tailoring rules, icu only); "" → NULL
	Deterministic bool   // collisdeterministic
}

// UserConversion records a CREATE [DEFAULT] CONVERSION so pg_dump's
// getConversions / dumpConversion re-emit it. goopg performs no actual encoding
// conversion — only the schema-dump round-trip is modeled. Stored in
// InMemory.userConversions and surfaced as rows in the virtual pg_conversion
// view. Mirrors PG pg_conversion.h. DU-002 slice 399 (M0119-0004).
type UserConversion struct {
	OID          uint32 // pg_conversion.oid (allocated from the catalog OID counter)
	Name         string // conname (bare, no schema)
	NamespaceOID uint32 // connamespace (resolved from the schema)
	Owner        uint32 // conowner (role OID; 10 = postgres superuser)
	ForEncoding  int32  // conforencoding (pg_enc ID of the source encoding)
	ToEncoding   int32  // contoencoding (pg_enc ID of the destination encoding)
	// ProcSchema/ProcName name the conversion function. dumpConversion selects
	// pg_conversion.conproc (a regproc) raw and emits ` FROM <conproc>`; pg_dump
	// runs with search_path='' so regproc qualifies the name with its schema —
	// hence both halves are surfaced and the conproc cell renders `<schema>.<name>`.
	// They are the as-written fallback; FuncOID (below) is the authoritative
	// source once resolved.
	ProcSchema string
	ProcName   string
	// FuncOID is pg_conversion.conproc's underlying pg_proc OID — set from the
	// routine resolveConversionFunc (executor) matched against the fixed
	// (int4,int4,cstring,internal,int4,bool)->int4 signature at CREATE time. 0
	// means unresolved (kept lenient for tests that register a UserConversion
	// directly without a routine registry); the pg_conversion VirtualRows
	// provider falls back to ProcSchema/ProcName text in that case. A non-zero
	// FuncOID makes conproc track a later RENAME/ALTER on the function, like
	// pg_cast.castfunc (slice 397) — unlike the as-written text, which would go
	// stale. DU-002 slice 403 (closes the slice-402 conproc-OID-cross-ref
	// deferral).
	FuncOID uint32
	Default bool // condefault (CREATE DEFAULT CONVERSION)
}

// BuiltinTSTemplateOID maps the fixed real-PG OIDs (pg_ts_template.dat) of the
// four built-in text search templates, keyed by lower-case template name. Only
// these are resolvable by CREATE TEXT SEARCH DICTIONARY ... TEMPLATE = ...;
// goopg implements no CREATE TEXT SEARCH TEMPLATE (a C-function-loading
// feature with no analog here). DU-002 slice 437 (M0119-0004).
var BuiltinTSTemplateOID = map[string]uint32{
	"simple":    3727,
	"synonym":   3730,
	"ispell":    3733,
	"thesaurus": 3742,
}

// tsDictTemplateOptionSpec mirrors each built-in template's own init
// function's option-name whitelist — dsimple_init (dict_simple.c),
// dsynonym_init (dict_synonym.c), dispell_init (dict_ispell.c), and
// thesaurus_init (dict_thesaurus.c) — the target of verify_dictoptions
// (tsearchcmds.c). Both the allowed key set and the literal "unrecognized
// ... parameter" message text differ per template in real PG; there is no
// shared format string to derive this from. DU-002 CREATE/ALTER TEXT SEARCH
// DICTIONARY option-validation follow-up (M0119-0004).
var tsDictTemplateOptionSpec = map[string]struct {
	allowed []string
	label   string
}{
	"simple":    {allowed: []string{"stopwords", "accept"}, label: "simple dictionary"},
	"synonym":   {allowed: []string{"synonyms", "casesensitive"}, label: "synonym"},
	"ispell":    {allowed: []string{"dictfile", "afffile", "stopwords"}, label: "Ispell"},
	"thesaurus": {allowed: []string{"dictfile", "dictionary"}, label: "Thesaurus"},
}

// ValidateTSDictOptions mirrors verify_dictoptions (tsearchcmds.c): rejects
// any option key the named template's own init function doesn't recognize,
// using the exact same message text real PG's four init functions raise
// (e.g. dsimple_init's `unrecognized simple dictionary parameter: "%s"`).
// tmplName is a no-op key (returns nil) for any name not in
// tsDictTemplateOptionSpec — CREATE/ALTER already reject an unresolved
// template name earlier, so this only ever sees the four built-ins here.
func ValidateTSDictOptions(tmplName string, opts []parser.TSDictOption) error {
	spec, ok := tsDictTemplateOptionSpec[tmplName]
	if !ok {
		return nil
	}
	for _, opt := range opts {
		if !slices.Contains(spec.allowed, opt.Key) {
			return fmt.Errorf("unrecognized %s parameter: %q", spec.label, opt.Key)
		}
	}
	return nil
}

// builtinTSTemplateNameForOID reverse-looks-up BuiltinTSTemplateOID (only 4
// entries, so a linear scan is simplest) — used by AlterTSDictOptions, which
// only has the dictionary's stored Template OID, not the name given at
// CREATE time.
func builtinTSTemplateNameForOID(oid uint32) (string, bool) {
	for name, o := range BuiltinTSTemplateOID {
		if o == oid {
			return name, true
		}
	}
	return "", false
}

// UserTSDict records a CREATE TEXT SEARCH DICTIONARY so pg_dump's
// getTSDictionaries / dumpTSDictionary re-emit it. goopg performs no actual
// text-search lexing — only the schema-dump round-trip is modeled, mirroring
// UserConversion. Stored in InMemory.userTSDicts and surfaced as rows in the
// virtual pg_ts_dict view. Mirrors PG pg_ts_dict.h. DU-002 slice 437
// (M0119-0004).
type UserTSDict struct {
	OID          uint32 // pg_ts_dict.oid (allocated from the catalog OID counter)
	Name         string // dictname (bare, no schema)
	NamespaceOID uint32 // dictnamespace (resolved from the schema)
	Owner        uint32 // dictowner (role OID; 10 = postgres superuser)
	Template     uint32 // dicttemplate (FK into the built-in pg_ts_template rows)
	// InitOption is the already-serialized dictinitoption text (PG's
	// serialize_deflist form: `"key1" = 'val1', "key2" = 42`, quote_identifier'd
	// keys, numeric literals bare, everything else single-quoted) — computed once
	// at CREATE time so dumpTSDictionary can re-emit it verbatim. "" (no options)
	// means the pg_ts_dict.dictinitoption column is NULL.
	InitOption string
}

// BuiltinTSParserOID maps the fixed real-PG OID (pg_ts_parser.dat) of the one
// built-in text search parser goopg models, keyed by lower-case parser name.
// dumpTSConfig's own query (`SELECT nspname, prsname FROM pg_ts_parser p,
// pg_namespace n WHERE p.oid = '<cfgparser>' ...`) needs a live pg_ts_parser
// row to resolve a configuration's PARSER = ... clause by OID — CREATE TEXT
// SEARCH PARSER is unimplemented (a C-function-loading feature with no
// analog here), so no user-defined parser can ever be named. DU-002 slice 446
// (M0119-0004).
var BuiltinTSParserOID = map[string]uint32{
	"default": 3722,
}

// BuiltinTSDictOID maps the fixed real-PG OID (pg_ts_dict.dat) of the one
// built-in text search dictionary goopg surfaces in pg_ts_dict, keyed by
// lower-case dictionary name. A CREATE TEXT SEARCH CONFIGURATION's ADD
// MAPPING ... WITH simple clause names this dictionary; dumpTSConfig's
// mapdict::regdictionary cast needs a live pg_ts_dict row (by OID) to
// resolve it back to a bare name. Only "simple" is modeled — it is the only
// dictionary with no external data-file dependency, and the overwhelmingly
// common default in practice. DU-002 slice 446 (M0119-0004).
var BuiltinTSDictOID = map[string]uint32{
	"simple": 3765,
}

// TSTokenType is one row of ts_token_type()'s fixed output for the "default"
// parser: (tokid, alias, description). Mirrors wparser_def.c's static
// lex_descr table (the only parser goopg models). Order matches upstream's
// tokid assignment (prsd_headline.c's lextype array), which is NOT
// alphabetical or otherwise derivable — it is a fixed historical numbering
// pg_dump's dumpTSConfig depends on to resolve a pg_ts_config_map row's
// maptokentype back to its alias. DU-002 slice 446 (M0119-0004).
type TSTokenType struct {
	TokID       int
	Alias       string
	Description string
}

// DefaultParserTokenTypes is the fixed 23-row token-type table for the
// built-in "default" parser (BuiltinTSParserOID["default"] = 3722). Verified
// byte-for-byte against `SELECT * FROM ts_token_type(3722)` on real PG 18.3.
var DefaultParserTokenTypes = []TSTokenType{
	{1, "asciiword", "Word, all ASCII"},
	{2, "word", "Word, all letters"},
	{3, "numword", "Word, letters and digits"},
	{4, "email", "Email address"},
	{5, "url", "URL"},
	{6, "host", "Host"},
	{7, "sfloat", "Scientific notation"},
	{8, "version", "Version number"},
	{9, "hword_numpart", "Hyphenated word part, letters and digits"},
	{10, "hword_part", "Hyphenated word part, all letters"},
	{11, "hword_asciipart", "Hyphenated word part, all ASCII"},
	{12, "blank", "Space symbols"},
	{13, "tag", "XML tag"},
	{14, "protocol", "Protocol head"},
	{15, "numhword", "Hyphenated word, letters and digits"},
	{16, "asciihword", "Hyphenated word, all ASCII"},
	{17, "hword", "Hyphenated word, all letters"},
	{18, "url_path", "URL path"},
	{19, "file", "File or path name"},
	{20, "float", "Decimal notation"},
	{21, "int", "Signed integer"},
	{22, "uint", "Unsigned integer"},
	{23, "entity", "XML entity"},
}

// TSConfigMapping is one `ADD MAPPING FOR <tokentype> WITH <dict1>[, ...]`
// entry — the ordered dictionary list a text search configuration applies to
// tokens of the given type. Mirrors a run of pg_ts_config_map rows sharing a
// maptokentype (mapseqno = the entry's index in Dicts). DU-002 slice 446
// (M0119-0004).
type TSConfigMapping struct {
	TokenType string   // e.g. "asciiword" (validated against DefaultParserTokenTypes)
	DictOIDs  []uint32 // pg_ts_dict.oid values, in mapseqno order
}

// UserTSConfig records a CREATE TEXT SEARCH CONFIGURATION (+ its ADD MAPPING
// entries) so pg_dump's getTSConfigurations / dumpTSConfig re-emit it. goopg
// performs no actual text-search tokenization — only the schema-dump
// round-trip is modeled, mirroring UserTSDict. Stored in
// InMemory.userTSConfigs and surfaced as rows in the virtual pg_ts_config /
// pg_ts_config_map views. Mirrors PG pg_ts_config.h / pg_ts_config_map.h.
// DU-002 slice 446 (M0119-0004).
type UserTSConfig struct {
	OID          uint32 // pg_ts_config.oid (allocated from the catalog OID counter)
	Name         string // cfgname (bare, no schema)
	NamespaceOID uint32 // cfgnamespace (resolved from the schema)
	Owner        uint32 // cfgowner (role OID; 10 = postgres superuser)
	Parser       uint32 // cfgparser (FK into the built-in pg_ts_parser row)
	Mappings     []TSConfigMapping
}

// Fixed OIDs for the three core system catalog heap tables.
// Values match upstream's pg_class.h / pg_attribute.h / pg_type.h
// so tools that query OID columns by numeric value (e.g. ODBC metadata
// probes) see the expected numbers.
const (
	TypeRelationId      uint32 = 1247 // pg_type
	AttributeRelationId uint32 = 1249 // pg_attribute
	ProcedureRelationId uint32 = 1255 // pg_proc
	RelationRelationId  uint32 = 1259 // pg_class
	IndexRelationId     uint32 = 2610 // pg_index
	StatisticRelationId uint32 = 2619 // pg_statistic
	AggregateRelationId uint32 = 2600 // pg_aggregate
)

// FirstUserOID is the first OID handed out for user-created tables.
// 16384 is upstream's `FirstNormalObjectId` — anything below is
// reserved for system catalogs.
const FirstUserOID uint32 = 16384

// VirtualNull is the sentinel a VirtualRows cell uses to denote SQL NULL for a
// column type whose empty string is a legitimate non-NULL value (most notably
// `text`: an empty `collicurules` / `collcollate` must read as NULL, not '').
// planner.TypedVirtualCell maps this sentinel to a NULL constant before any
// type-specific parsing. The byte sequence cannot collide with a real catalog
// value (NUL-delimited marker). Sibling decoders (the executor's
// rematerialiseVirtualRows) share TypedVirtualCell, so they stay in sync.
const VirtualNull = "\x00\x00NULL\x00\x00"

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
		databases:              map[string]bool{"postgres": true, "template1": true, "template0": true},
		databaseConnLimit:      make(map[string]int32),
		dbRoleSettings:         make(map[uint32][]string),
		roleSettings:           make(map[roleSettingKey][]string),
		roleMembers:            make(map[roleMembershipKey]*RoleMembership),
		partitionChildren:      make(map[uint32][]uint32),
		indexPartitionChildren: make(map[uint32][]uint32),
		toastRenames:           make(map[uint32]string),
		inheritanceChildren:    make(map[uint32][]uint32),
		enumTypes:              make(map[string]*EnumType),
		domains:                make(map[string]*Domain),
		compositeTypeNames:     make(map[string]bool),
		compositeTypeFields:    make(map[string][]CompositeField),
		compositeTypes:         make(map[string]*CompositeType),
		rangeTypes:             make(map[string]*RangeType),
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
		schemaOwners:       make(map[string]uint32),
		roles:              make(map[string]uint32),
		predefinedRoles:    newPredefinedRoleMap(),
		roleAttrs:          make(map[string]*RoleAttrs),
		tempNamespaces:     make(map[string]uint32),
		tableACLs:          make(map[uint32]map[string]map[string]bool),
		tableACLGrantor:    make(map[uint32]map[string]string),
		tableACLOrder:      make(map[uint32][]string),
		roleACLDisplay:     make(map[string]string),
		relACLEmptied:      make(map[uint32]bool),
		relACLOwnerRevoked: make(map[uint32]bool),
		attrACLs:           make(map[attrACLKey]map[string]map[string]bool),
		attrACLOrder:       make(map[attrACLKey][]string),
		attrACLGrantor:     make(map[attrACLKey]map[string]string),
		parameterACLOIDs:   make(map[string]uint32),
		parameterACLNames:  make(map[uint32]string),
		defaultACLOIDs:     make(map[defaultACLKey]uint32),
		defaultACLKeys:     make(map[uint32]defaultACLKey),
		defaultACLGlobal:   make(map[uint32]bool),
		comments:           make(map[commentKey]string),
		statisticsObjs:     make(map[string]*StatisticsObject),
		extensions:         make(map[string]*extensionRow),
		tablespaces:        make(map[string]*tablespaceRow),
	}
	c.registerSystemTables()
	return c
}

// RegisterStatistics adds a new statistics object to the catalog and returns it.
// If a statistics object with the same schema-qualified name already exists it
// is overwritten. M0097-0023.
func (c *InMemory) RegisterStatistics(schema, name string, tableOID uint32) *StatisticsObject {
	return c.RegisterStatisticsFull(schema, name, tableOID, nil, nil, nil, false)
}

// RegisterStatisticsFull registers a statistics object carrying its kinds, simple
// column list, and deparsed expression targets so pg_get_statisticsobjdef can
// reconstruct the DDL. DU-002 slices 314/316.
func (c *InMemory) RegisterStatisticsFull(schema, name string, tableOID uint32, kinds, columns, exprs []string, hasExpr bool) *StatisticsObject {
	if schema == "" {
		schema = "public"
	}
	obj := &StatisticsObject{Name: name, Schema: schema, OID: c.AllocOID(), TableOID: tableOID, Kinds: kinds, Columns: columns, Exprs: exprs, HasExpr: hasExpr}
	key := obj.qualifiedKey()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statisticsObjs == nil {
		c.statisticsObjs = make(map[string]*StatisticsObject)
	}
	c.statisticsObjs[key] = obj
	return obj
}

// RegisterStatisticsDuringRecovery re-registers a statistics object from a
// decoded RecordKindCreateStatistics WAL record, overwriting-by-qualified-name
// (mirrors RegisterAccessMethodDuringRecovery/CreateCollationDuringRecovery)
// so replaying the same record twice is idempotent. DU-002 restart-persistence
// follow-up to slice 441.
func (c *InMemory) RegisterStatisticsDuringRecovery(schema, name string, oid, tableOID, ownerOID uint32, kinds, columns, exprs []string, hasExpr bool) {
	if schema == "" {
		schema = "public"
	}
	obj := &StatisticsObject{Name: name, Schema: schema, OID: oid, TableOID: tableOID, Owner: ownerOID, Kinds: kinds, Columns: columns, Exprs: exprs, HasExpr: hasExpr}
	key := obj.qualifiedKey()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statisticsObjs == nil {
		c.statisticsObjs = make(map[string]*StatisticsObject)
	}
	c.statisticsObjs[key] = obj
	if oid >= c.nextOID {
		c.nextOID = oid + 1
	}
}

// DropStatistics removes a statistics object by unqualified name and schema
// (defaulting to "public"), mirroring DropCollation. Returns false if no such
// object is registered.
func (c *InMemory) DropStatistics(name, schema string) bool {
	if schema == "" {
		schema = "public"
	}
	key := strings.ToLower(schema + "." + name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statisticsObjs == nil {
		return false
	}
	if _, ok := c.statisticsObjs[key]; !ok {
		return false
	}
	delete(c.statisticsObjs, key)
	return true
}

// DropStatisticsDuringRecovery is the recovery-replay counterpart to
// DropStatistics; a missing object is a silent no-op (the record it created
// may predate the last checkpoint this recovery pass starts from).
func (c *InMemory) DropStatisticsDuringRecovery(name, schema string) {
	c.DropStatistics(name, schema)
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


// SetStatisticsTarget records the statistics target for the named extended
// statistics object (ALTER STATISTICS ... SET STATISTICS n). A nil target
// resets it to the default (PG stores stxstattarget=NULL). Returns false if no
// such object exists. DU-002 slice 317.
func (c *InMemory) SetStatisticsTarget(name string, target *int) bool {
	key := strings.ToLower(name)
	if !strings.Contains(key, ".") {
		key = "public." + key
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statisticsObjs == nil {
		return false
	}
	obj, ok := c.statisticsObjs[key]
	if !ok {
		return false
	}
	obj.StatTarget = target
	return true
}

// RenameStatisticsObject renames a statistics object (ALTER STATISTICS ...
// RENAME TO), re-keying the schema-qualified map entry. Returns false if no
// such object exists. DU-002 slice 441.
func (c *InMemory) RenameStatisticsObject(name, newName string) bool {
	key := strings.ToLower(name)
	if !strings.Contains(key, ".") {
		key = "public." + key
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statisticsObjs == nil {
		return false
	}
	obj, ok := c.statisticsObjs[key]
	if !ok {
		return false
	}
	delete(c.statisticsObjs, key)
	obj.Name = newName
	c.statisticsObjs[obj.qualifiedKey()] = obj
	return true
}

// SetStatisticsOwner sets the owning role OID of a statistics object (ALTER
// STATISTICS ... OWNER TO). Returns false if no such object exists. DU-002
// slice 441.
func (c *InMemory) SetStatisticsOwner(name string, ownerOID uint32) bool {
	key := strings.ToLower(name)
	if !strings.Contains(key, ".") {
		key = "public." + key
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statisticsObjs == nil {
		return false
	}
	obj, ok := c.statisticsObjs[key]
	if !ok {
		return false
	}
	obj.Owner = ownerOID
	return true
}

// SetStatisticsSchema moves a statistics object to a new schema (ALTER
// STATISTICS ... SET SCHEMA), re-keying the map entry. Returns false if no
// such object exists. DU-002 slice 441.
func (c *InMemory) SetStatisticsSchema(name, newSchema string) bool {
	key := strings.ToLower(name)
	if !strings.Contains(key, ".") {
		key = "public." + key
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.statisticsObjs == nil {
		return false
	}
	obj, ok := c.statisticsObjs[key]
	if !ok {
		return false
	}
	delete(c.statisticsObjs, key)
	obj.Schema = newSchema
	c.statisticsObjs[obj.qualifiedKey()] = obj
	return true
}

// RenameStatisticsObjectDuringRecovery is the discard-result recovery
// counterpart to RenameStatisticsObject, mirroring
// RenameCollationDuringRecovery. DU-002 restart-persistence follow-up
// (resume point (1) of the slice-441/445 ledger rows).
func (c *InMemory) RenameStatisticsObjectDuringRecovery(name, newName string) {
	c.RenameStatisticsObject(name, newName)
}

// SetStatisticsOwnerDuringRecovery is the discard-result recovery
// counterpart to SetStatisticsOwner, mirroring SetCollationOwnerDuringRecovery.
func (c *InMemory) SetStatisticsOwnerDuringRecovery(name string, ownerOID uint32) {
	c.SetStatisticsOwner(name, ownerOID)
}

// SetStatisticsSchemaDuringRecovery is the discard-result recovery
// counterpart to SetStatisticsSchema, mirroring SetCollationSchemaDuringRecovery.
func (c *InMemory) SetStatisticsSchemaDuringRecovery(name, newSchema string) {
	c.SetStatisticsSchema(name, newSchema)
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

// StatisticsByOID finds a statistics object by its pg_statistic_ext OID. DU-002 slice 314.
func (c *InMemory) StatisticsByOID(oid uint32) (*StatisticsObject, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, obj := range c.statisticsObjs {
		if obj.OID == oid {
			return obj, true
		}
	}
	return nil, false
}

// BuildStatisticsObjDef reconstructs the CREATE STATISTICS DDL for an extended
// statistics object, mirroring ruleutils.c pg_get_statisticsobj_worker. pg_dump's
// dumpStatisticsExt calls pg_get_statisticsobjdef(oid) and emits the result
// verbatim (plus a trailing semicolon). The kinds clause is suppressed when all
// three kinds are enabled (the default) or when the object spans a single column;
// the FROM relation is schema-qualified to match pg_dump's empty search_path.
// Returns "" when the object carries an expression target (not reconstructable by
// this simple-column path). DU-002 slice 314.
func (c *InMemory) BuildStatisticsObjDef(obj *StatisticsObject) string {
	// Decline only when the ON list had an expression target that could not be
	// captured (HasExpr set but no deparsed Exprs) — reconstructing it would drop
	// the expression and silently corrupt the object on restore. DU-002 slice 316.
	if obj == nil || (obj.HasExpr && len(obj.Exprs) == 0) {
		return ""
	}
	schema := obj.Schema
	if schema == "" {
		schema = "public"
	}
	var sb strings.Builder
	sb.WriteString("CREATE STATISTICS ")
	sb.WriteString(quoteCollationIdent(schema))
	sb.WriteByte('.')
	sb.WriteString(quoteCollationIdent(obj.Name))

	// Decode requested kinds. An empty Kinds means the default (all enabled).
	ndistinct, dependencies, mcv := false, false, false
	if len(obj.Kinds) == 0 {
		ndistinct, dependencies, mcv = true, true, true
	} else {
		for _, k := range obj.Kinds {
			switch strings.ToLower(k) {
			case "ndistinct":
				ndistinct = true
			case "dependencies":
				dependencies = true
			case "mcv":
				mcv = true
			}
		}
	}
	allEnabled := ndistinct && dependencies && mcv
	// ncolumns counts both simple columns and expression targets, matching PG's
	// pg_get_statisticsobj_worker (stxkeys.dim1 + list_length(exprs)).
	ncolumns := len(obj.Columns) + len(obj.Exprs)
	// Emit the kinds clause only when some kind is disabled and the object spans
	// more than one column (a single-column object is an expression-stats object
	// in PG, where the clause is omitted).
	if !allEnabled && ncolumns > 1 {
		sb.WriteString(" (")
		gotone := false
		if ndistinct {
			sb.WriteString("ndistinct")
			gotone = true
		}
		if dependencies {
			if gotone {
				sb.WriteString(", ")
			}
			sb.WriteString("dependencies")
			gotone = true
		}
		if mcv {
			if gotone {
				sb.WriteString(", ")
			}
			sb.WriteString("mcv")
		}
		sb.WriteByte(')')
	}

	sb.WriteString(" ON ")
	// PG emits all simple columns first (in stxkeys order) then all expression
	// targets, regardless of their original ON-list order; colno spans both lists
	// for comma separation. Mirrors pg_get_statisticsobj_worker (ruleutils.c).
	colno := 0
	for _, col := range obj.Columns {
		if colno > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(quoteCollationIdent(col))
		colno++
	}
	for _, e := range obj.Exprs {
		if colno > 0 {
			sb.WriteString(", ")
		}
		// e is already rendered in its final form (parenthesized when not a bare
		// function call) by the executor's deparser.
		sb.WriteString(e)
		colno++
	}

	// FROM relation, schema-qualified (pg_dump runs with an empty search_path so
	// generate_relation_name always qualifies).
	sb.WriteString(" FROM ")
	relSchema, relName := schema, ""
	if tbl, ok := c.LookupTableByOID(obj.TableOID); ok {
		relName = tbl.Name
		if tbl.Schema != "" {
			relSchema = tbl.Schema
		}
	}
	sb.WriteString(quoteCollationIdent(relSchema))
	sb.WriteByte('.')
	sb.WriteString(quoteCollationIdent(relName))
	return sb.String()
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
	if agg.OID == 0 {
		agg.OID = c.allocOIDLocked()
	}
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

// DropUserAggregate removes a user-defined aggregate by name
// (case-insensitive). Returns true if an aggregate was found and removed.
// Mirrors DropCollation.
func (c *InMemory) DropUserAggregate(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(name)
	if _, ok := c.userAggregates[key]; !ok {
		return false
	}
	delete(c.userAggregates, key)
	return true
}

// DropUserAggregateDuringRecovery is the discard-result recovery
// counterpart to DropUserAggregate, mirroring
// DropCollationDuringRecovery/RenameUserAggregateDuringRecovery.
func (c *InMemory) DropUserAggregateDuringRecovery(name string) {
	c.DropUserAggregate(name)
}

// RegisterUserAggregateDuringRecovery is the idempotent version of
// RegisterUserAggregate used by the WAL-replay driver
// (internal/initdb/aggregate_ddl_recovery.go). Unlike RegisterUserAggregate
// it takes the OID from the WAL record (so the recovered registry matches
// what the pre-crash server assigned) and advances nextOID past it so
// subsequent allocations do not collide. Re-applying a record for an
// aggregate that already exists just refreshes its fields. Mirrors
// RegisterCastDuringRecovery. DU-002 restart-persistence follow-up
// (M0119-0004, slice 405 ledger resume point (c)). `schema` resolves the
// aggregate's NamespaceOID the same way CreateCollationDuringRecovery does
// (unknown/empty → public); this depends on replaySchemaDDLRecords having
// already restored the schema registry, which the caller
// (replayAggregateDDLRecords) guarantees by running after it.
func (c *InMemory) RegisterUserAggregateDuringRecovery(agg *UserAggregate, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userAggregates == nil {
		c.userAggregates = make(map[string]*UserAggregate)
	}
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	agg.NamespaceOID = nsOID
	c.userAggregates[strings.ToLower(agg.Name)] = agg
	if agg.OID >= c.nextOID {
		c.nextOID = agg.OID + 1
	}
}

// RenameUserAggregateDuringRecovery is the discard-result recovery
// counterpart to RenameUserAggregate, mirroring
// RenameCollationDuringRecovery. A rename record can only be replayed after
// its aggregate's CREATE AGGREGATE record (WAL is scanned in order), so a
// not-found error here is not expected in practice, but replay must not
// abort on it. DU-002 restart-persistence follow-up (M0119-0004, slice 405
// ledger resume point (c)).
func (c *InMemory) RenameUserAggregateDuringRecovery(oldName, newName string) {
	_ = c.RenameUserAggregate(oldName, newName)
}

// SetUserAggregateOwner changes an existing user-defined aggregate's owner
// (ALTER AGGREGATE ... OWNER TO). Returns false if name is not found.
// Mirrors SetCollationOwner. M0119-0004.
func (c *InMemory) SetUserAggregateOwner(name string, ownerOID uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	agg, ok := c.userAggregates[strings.ToLower(name)]
	if !ok {
		return false
	}
	agg.Owner = ownerOID
	return true
}

// SetUserAggregateOwnerDuringRecovery is the discard-result recovery
// counterpart to SetUserAggregateOwner, mirroring
// RenameUserAggregateDuringRecovery. DU-002 restart-persistence follow-up
// (M0119-0004).
func (c *InMemory) SetUserAggregateOwnerDuringRecovery(name string, ownerOID uint32) {
	c.SetUserAggregateOwner(name, ownerOID)
}

// ListUserAggregates returns every registered user-defined aggregate in
// OID order (deterministic for pg_proc/pg_aggregate VirtualRows output).
func (c *InMemory) ListUserAggregates() []*UserAggregate {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*UserAggregate, 0, len(c.userAggregates))
	for _, agg := range c.userAggregates {
		out = append(out, agg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
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

// HasTempInheritanceChildren reports whether any inheritance parent currently
// has a session-owned temporary child registered. When true, a query that
// scans an inheritance parent expands to a session-specific child set
// (RELATION_IS_OTHER_TEMP, see AccessibleInheritanceChildren), so its plan must
// NOT be served from the cross-session plan cache. Cheap (O(1)) in the common
// case of no inheritance at all. Design 0118-0037 (M0118-0008 inherit-temp).
func (c *InMemory) HasTempInheritanceChildren() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.inheritanceChildren) == 0 {
		return false
	}
	childOIDs := make(map[uint32]bool)
	for _, children := range c.inheritanceChildren {
		for _, oid := range children {
			childOIDs[oid] = true
		}
	}
	for _, t := range c.tables {
		if t.Temp && t.TempOwner != "" && childOIDs[t.OID] {
			return true
		}
	}
	return false
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

// AccessibleInheritanceChildren filters a list of inheritance/partition child
// relations down to those visible to the session identified by sessionTempOwner,
// mirroring PostgreSQL's RELATION_IS_OTHER_TEMP exclusion in inheritance
// expansion (see expand_single_inheritance_child / find_inheritance_children).
//
// A temporary child relation owned by a *different* session is dropped: in
// upstream PG it lives in another backend's pg_temp_N namespace and is never
// part of this backend's scan of the inheritance parent. A permanent child, an
// own temp child (TempOwner == sessionTempOwner), and an unowned temp child
// (TempOwner == "", i.e. created without a session identity in internal/test
// contexts) are all retained so legacy single-session behaviour is preserved.
//
// The slice is filtered in place when nothing is dropped (returning the same
// backing array); otherwise a fresh slice is returned. nil-in / nil-out.
// Design 0118-0036 (M0118-0008 inherit-temp).
func AccessibleInheritanceChildren(children []*Table, sessionTempOwner string) []*Table {
	if len(children) == 0 {
		return children
	}
	// Fast path: nothing to drop.
	drop := false
	for _, c := range children {
		if c != nil && c.Temp && c.TempOwner != "" && c.TempOwner != sessionTempOwner {
			drop = true
			break
		}
	}
	if !drop {
		return children
	}
	out := make([]*Table, 0, len(children))
	for _, c := range children {
		if c != nil && c.Temp && c.TempOwner != "" && c.TempOwner != sessionTempOwner {
			continue
		}
		out = append(out, c)
	}
	return out
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

// IsPartitionChild reports whether oid is registered as a partition of some
// parent. Unlike the Table.PartitionParentOID field — which is only populated on
// the CREATE TABLE … PARTITION OF path — this consults the live partitionChildren
// map, so it is also true for a table linked via ALTER TABLE … ATTACH PARTITION
// (RegisterPartitionChild updates the map but leaves PartitionParentOID 0).
// fk-partitioned-1: distinguishes a root FK-owning table from a per-partition
// FK clone when naming a referenced-side violation.
func (c *InMemory) IsPartitionChild(oid uint32) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, kids := range c.partitionChildren {
		for _, k := range kids {
			if k == oid {
				return true
			}
		}
	}
	return false
}

// PartitionParentOf returns the OID of the partitioned parent that childOID is
// registered under, consulting the live partitionChildren map. Like
// IsPartitionChild, this is reliable for both CREATE TABLE … PARTITION OF and
// ALTER TABLE … ATTACH PARTITION (the latter leaves Table.PartitionParentOID 0).
// Returns (0,false) when childOID is not a partition of anything.
// fk-partitioned-1: walk a deleted leaf partition up to the referenced parent.
func (c *InMemory) PartitionParentOf(childOID uint32) (uint32, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for parent, kids := range c.partitionChildren {
		for _, k := range kids {
			if k == childOID {
				return parent, true
			}
		}
	}
	return 0, false
}

// SetPendingAttachXID records that the transaction XID has an in-flight ATTACH
// PARTITION of childOID whose foreign-key clone is visible but whose partition
// registration is deferred to COMMIT. fk-partitioned-1 (design 0118-0120).
func (c *InMemory) SetPendingAttachXID(childOID uint32, xid uint32) {
	c.mu.Lock()
	if c.pendingAttachXID == nil {
		c.pendingAttachXID = make(map[uint32]uint32)
	}
	c.pendingAttachXID[childOID] = xid
	c.mu.Unlock()
}

// PendingAttachXID returns the XID of an in-flight ATTACH of childOID, if any.
func (c *InMemory) PendingAttachXID(childOID uint32) (uint32, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	xid, ok := c.pendingAttachXID[childOID]
	return xid, ok
}

// ClearPendingAttachXID drops the in-flight-attach marker for childOID. Called
// from both the COMMIT path (after RegisterPartitionChild) and the ROLLBACK
// path (the deferred attach is discarded).
func (c *InMemory) ClearPendingAttachXID(childOID uint32) {
	c.mu.Lock()
	delete(c.pendingAttachXID, childOID)
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

// MarkPartitionDetachPending stamps the partition child childOID with epoch as
// its "detach pending" boundary (ALTER TABLE … DETACH PARTITION … CONCURRENTLY).
// The child stays physically registered — VisiblePartitionChildren omits it only
// for statements whose snapshot epoch is >= epoch, so a snapshot taken before the
// detach (e.g. a REPEATABLE READ transaction that began earlier) still scans the
// partition while concurrent READ COMMITTED statements stop seeing it. epoch must
// be a fresh value from mvcc.NextPartitionDetachEpoch(). No-op (returns false) if
// the child is unknown. Idempotent: re-stamping the same child does not double the
// pending count. Design 0118-0058 (M0118-0008 detach-partition-concurrently).
func (c *InMemory) MarkPartitionDetachPending(childOID uint32, epoch uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.tables {
		if t.OID == childOID {
			if t.DetachPendingEpoch == 0 {
				c.pendingPartitionDetachCount++
			}
			t.DetachPendingEpoch = epoch
			return true
		}
	}
	return false
}

// ClearPartitionDetachPending removes the detach-pending mark from childOID,
// called when the concurrent detach finalizes (or is rolled back) so the child
// is either fully unregistered or restored to ordinary visibility. Idempotent.
// Design 0118-0058 (M0118-0008 detach-partition-concurrently).
func (c *InMemory) ClearPartitionDetachPending(childOID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.tables {
		if t.OID == childOID {
			if t.DetachPendingEpoch != 0 {
				t.DetachPendingEpoch = 0
				if c.pendingPartitionDetachCount > 0 {
					c.pendingPartitionDetachCount--
				}
			}
			return
		}
	}
}

// HasPendingPartitionDetach reports whether any partition child is currently
// marked detach-pending. When true, a query scanning a partitioned parent
// expands to a snapshot-dependent partition set (see VisiblePartitionChildren),
// so its plan must NOT be served from / stored in the cross-session plan cache —
// the same constraint HasTempInheritanceChildren imposes for temp inheritance.
// O(1) (a maintained counter). Design 0118-0058 (M0118-0008).
func (c *InMemory) HasPendingPartitionDetach() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pendingPartitionDetachCount > 0
}

// MarkInheritanceChangePending increments the deferred-inheritance-change counter
// when an ALTER TABLE … {NO} INHERIT inside an explicit transaction records its
// catalog mutation for replay at COMMIT. Design 0118-0080 (M0118-0008).
func (c *InMemory) MarkInheritanceChangePending() {
	c.mu.Lock()
	c.pendingInheritanceChangeCount++
	c.mu.Unlock()
}

// UnmarkInheritanceChangePending decrements the counter when a deferred
// inheritance change is applied (at COMMIT) or discarded (at ROLLBACK / ROLLBACK
// TO SAVEPOINT). Clamped at zero. Design 0118-0080 (M0118-0008).
func (c *InMemory) UnmarkInheritanceChangePending() {
	c.mu.Lock()
	if c.pendingInheritanceChangeCount > 0 {
		c.pendingInheritanceChangeCount--
	}
	c.mu.Unlock()
}

// HasPendingInheritanceChange reports whether any ALTER TABLE … {NO} INHERIT is
// currently deferred to COMMIT in an in-progress explicit transaction. When true
// the cross-session plan cache must be bypassed so a query scanning the parent
// re-plans against the current child set rather than reuse a plan baked across
// the inheritance change (mirrors HasPendingPartitionDetach for detach). O(1).
// Design 0118-0080 (M0118-0008 alter-table-4).
func (c *InMemory) HasPendingInheritanceChange() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pendingInheritanceChangeCount > 0
}

// VisiblePartitionChildren filters a partition child list down to those visible
// to a statement whose snapshot epoch is snapshotEpoch, mirroring PostgreSQL's
// snapshot-relative omission of a concurrently-detached partition
// (find_inheritance_children_extended honouring inhdetachpending against the
// active snapshot). A child stamped DetachPendingEpoch == e is dropped when
// snapshotEpoch >= e (the detach is "visible" to this statement) and retained
// when snapshotEpoch < e (the statement's snapshot predates the detach) or when
// e == 0 (not detaching). Filtered in place when nothing is dropped (same backing
// array); otherwise a fresh slice is returned. nil-in / nil-out. Design 0118-0058
// (M0118-0008 detach-partition-concurrently).
func VisiblePartitionChildren(children []*Table, snapshotEpoch uint64) []*Table {
	if len(children) == 0 {
		return children
	}
	drop := false
	for _, c := range children {
		if c != nil && c.DetachPendingEpoch != 0 && snapshotEpoch >= c.DetachPendingEpoch {
			drop = true
			break
		}
	}
	if !drop {
		return children
	}
	out := make([]*Table, 0, len(children))
	for _, c := range children {
		if c != nil && c.DetachPendingEpoch != 0 && snapshotEpoch >= c.DetachPendingEpoch {
			continue
		}
		out = append(out, c)
	}
	return out
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
				if rangeStrTupleGE(keyStrs, pb.FromValues, pb.FromUnbounded, pb.FromUnboundMax) &&
					rangeStrTupleLT(keyStrs, pb.ToValues, pb.ToUnbounded, pb.ToUnboundMax) {
					return t
				}
			}
		}
	}
	return defaultPart
}

// rangeStrTupleGE returns true if key >= bound (lexicographic tuple comparison).
// unb/max are the per-element unbounded-edge flags parallel to bound (may be
// shorter/nil for pre-slice-261 bounds, in which case the legacy string sentinel
// is used). DU-002 slice 261.
func rangeStrTupleGE(key, bound []string, unb, max []bool) bool {
	for i := range key {
		if i >= len(bound) {
			break
		}
		cmp := compareKeyToRangeBound(key[i], bound[i], boundElemUnbounded(bound, unb, i), boundElemUnboundMax(bound, max, i))
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
func rangeStrTupleLT(key, bound []string, unb, max []bool) bool {
	for i := range key {
		if i >= len(bound) {
			break
		}
		cmp := compareKeyToRangeBound(key[i], bound[i], boundElemUnbounded(bound, unb, i), boundElemUnboundMax(bound, max, i))
		if cmp < 0 {
			return true
		}
		if cmp > 0 {
			return false
		}
	}
	return false // equal on all compared positions: does NOT satisfy < (exclusive upper bound)
}

// boundElemUnbounded reports whether bound element i is an unbounded edge,
// preferring the explicit flag and falling back to the legacy string sentinel
// for bounds created before slice 261 (no flags captured). DU-002 slice 261.
func boundElemUnbounded(bound []string, unb []bool, i int) bool {
	if i < len(unb) {
		return unb[i]
	}
	return bound[i] == "MINVALUE" || bound[i] == "MAXVALUE"
}

// boundElemUnboundMax reports whether unbounded element i is +∞ (MAXVALUE) vs
// -∞ (MINVALUE). Meaningful only when boundElemUnbounded is true.
func boundElemUnboundMax(bound []string, max []bool, i int) bool {
	if i < len(max) {
		return max[i]
	}
	return bound[i] == "MAXVALUE"
}

// compareKeyToRangeBound compares a string-formatted key value against a
// partition bound element. Returns -1, 0, +1 (sign of key − bound).
// When the bound element is an unbounded edge, isMax distinguishes +∞ (key < it
// → -1) from -∞ (key > it → +1); the bound string is then ignored. Unlike the
// pre-slice-261 helper, the KEY is always treated as a concrete value — a real
// text key "MINVALUE"/"MAXVALUE" is no longer mistaken for ±∞. DU-002 slice 261.
func compareKeyToRangeBound(keyStr, boundStr string, unbounded, isMax bool) int {
	if unbounded {
		if isMax {
			return -1 // anything < +∞
		}
		return 1 // anything > -∞
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
	delete(c.databaseConnLimit, name)
	return nil
}

// DatabaseConnLimit returns the runtime `pg_database.datconnlimit` override
// recorded for name via SetDatabaseConnLimit, or 0 (PG's "no limit" default)
// if none was ever set. M-NIGHTLY AI-20260707-000712-004 / AC-002 residual #1.
func (c *InMemory) DatabaseConnLimit(name string) int32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.databaseConnLimit[name]
}

// SetDatabaseConnLimit records a runtime `datconnlimit` override for an
// existing database — the target of `UPDATE pg_database SET datconnlimit =
// ... WHERE datname = ...` (goopg has no physical, generically-writable
// pg_database heap; this is the same "runtime InMemory truth, no on-disk
// write" pattern CreateCollation/CreateExtension already use). Returns false
// if name is not a registered database (caller reports 0 rows affected).
func (c *InMemory) SetDatabaseConnLimit(name string, limit int32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.databases[name] {
		return false
	}
	c.databaseConnLimit[name] = limit
	return true
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

// dbRoleSettingConfigName returns the GUC name half of a "name=value"
// pg_db_role_setting.setconfig entry, or "" if entry has no '='.
func dbRoleSettingConfigName(entry string) string {
	eq := strings.IndexByte(entry, '=')
	if eq < 0 {
		return ""
	}
	return entry[:eq]
}

// SetDatabaseConfig upserts an `ALTER DATABASE ... SET name = value`
// override into dbOid's pg_db_role_setting.setconfig list: an existing
// entry with the same GUC name (case-insensitive — GUC names are
// case-insensitive) is replaced in place, otherwise the entry is appended.
// Mirrors PG's GUC_array_change ordering. Idempotent, so it is also used
// directly by the WAL-replay recovery driver (no separate DuringRecovery
// variant is needed). M0119-0004-ACLHEAP (ALTER DATABASE ... SET follow-up).
func (c *InMemory) SetDatabaseConfig(dbOid uint32, name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := name + "=" + value
	entries := c.dbRoleSettings[dbOid]
	for i, e := range entries {
		if strings.EqualFold(dbRoleSettingConfigName(e), name) {
			entries[i] = entry
			return
		}
	}
	c.dbRoleSettings[dbOid] = append(entries, entry)
}

// ResetDatabaseConfig removes a single `ALTER DATABASE ... RESET name`
// override, if present. A no-op when the name has no override recorded.
// Deletes the dbOid map key entirely when the last entry is removed (mirrors
// ResetAllDatabaseConfig's full-delete semantics) so pg_db_role_setting stops
// emitting a phantom row with a blank setconfig array.
func (c *InMemory) ResetDatabaseConfig(dbOid uint32, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := c.dbRoleSettings[dbOid]
	for i, e := range entries {
		if strings.EqualFold(dbRoleSettingConfigName(e), name) {
			remaining := append(entries[:i], entries[i+1:]...)
			if len(remaining) == 0 {
				delete(c.dbRoleSettings, dbOid)
			} else {
				c.dbRoleSettings[dbOid] = remaining
			}
			return
		}
	}
}

// ResetAllDatabaseConfig clears every `ALTER DATABASE ... SET` override for
// dbOid (`ALTER DATABASE ... RESET ALL`).
func (c *InMemory) ResetAllDatabaseConfig(dbOid uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.dbRoleSettings, dbOid)
}

// DatabaseConfigEntries returns a copy of dbOid's pg_db_role_setting.setconfig
// entries ("name=value" strings, insertion order), or nil when none are set.
func (c *InMemory) DatabaseConfigEntries(dbOid uint32) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := c.dbRoleSettings[dbOid]
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, len(entries))
	copy(out, entries)
	return out
}

// roleSettingKey identifies one `ALTER ROLE ... SET` override row. See
// InMemory.roleSettings' doc comment for the DBOid=0 vs FirstUserOID
// distinction.
type roleSettingKey struct {
	RoleOID uint32
	DBOid   uint32
}

// SetRoleConfig upserts an `ALTER ROLE ... [IN DATABASE ...] SET name =
// value` override, mirroring SetDatabaseConfig's in-place-replace-or-append
// semantics. M0119-0004-ACLHEAP (ALTER ROLE ... SET follow-up).
func (c *InMemory) SetRoleConfig(roleOid, dbOid uint32, name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := roleSettingKey{RoleOID: roleOid, DBOid: dbOid}
	entry := name + "=" + value
	entries := c.roleSettings[key]
	for i, e := range entries {
		if strings.EqualFold(dbRoleSettingConfigName(e), name) {
			entries[i] = entry
			return
		}
	}
	c.roleSettings[key] = append(entries, entry)
}

// ResetRoleConfig removes a single `ALTER ROLE ... [IN DATABASE ...] RESET
// name` override, if present. Deletes the key entirely when the last entry
// is removed (mirrors ResetAllRoleConfig's full-delete semantics) so
// pg_db_role_setting stops emitting a phantom row with a blank setconfig
// array.
func (c *InMemory) ResetRoleConfig(roleOid, dbOid uint32, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := roleSettingKey{RoleOID: roleOid, DBOid: dbOid}
	entries := c.roleSettings[key]
	for i, e := range entries {
		if strings.EqualFold(dbRoleSettingConfigName(e), name) {
			remaining := append(entries[:i], entries[i+1:]...)
			if len(remaining) == 0 {
				delete(c.roleSettings, key)
			} else {
				c.roleSettings[key] = remaining
			}
			return
		}
	}
}

// ResetAllRoleConfig clears every override for (roleOid, dbOid) (`ALTER
// ROLE ... [IN DATABASE ...] RESET ALL`).
func (c *InMemory) ResetAllRoleConfig(roleOid, dbOid uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.roleSettings, roleSettingKey{RoleOID: roleOid, DBOid: dbOid})
}

// RoleConfigEntries returns a copy of (roleOid, dbOid)'s setconfig entries
// ("name=value" strings, insertion order), or nil when none are set.
func (c *InMemory) RoleConfigEntries(roleOid, dbOid uint32) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := c.roleSettings[roleSettingKey{RoleOID: roleOid, DBOid: dbOid}]
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, len(entries))
	copy(out, entries)
	return out
}

// RoleConfigRow is one pg_db_role_setting row keyed by a non-zero setrole,
// returned by AllRoleConfigRows in deterministic (RoleOID, DBOid) order.
type RoleConfigRow struct {
	RoleOID uint32
	DBOid   uint32
	Entries []string
}

// AllRoleConfigRows returns every `ALTER ROLE ... SET` override currently
// recorded, sorted by (RoleOID, DBOid) for deterministic pg_db_role_setting
// virtual-row output.
func (c *InMemory) AllRoleConfigRows() []RoleConfigRow {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.roleSettings) == 0 {
		return nil
	}
	rows := make([]RoleConfigRow, 0, len(c.roleSettings))
	for key, entries := range c.roleSettings {
		cp := make([]string, len(entries))
		copy(cp, entries)
		rows = append(rows, RoleConfigRow{RoleOID: key.RoleOID, DBOid: key.DBOid, Entries: cp})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].RoleOID != rows[j].RoleOID {
			return rows[i].RoleOID < rows[j].RoleOID
		}
		return rows[i].DBOid < rows[j].DBOid
	})
	return rows
}

// roleMembershipKey identifies one pg_auth_members row: RoleOID is the role
// being granted (roleid), MemberOID is the role receiving membership
// (member), GrantorOID is who granted it (grantor). Real PG's unique index
// is the (roleid, member, grantor) triple (pg_auth_members_role_member_index,
// pg_auth_members.h) — the SAME (role, member) pair can hold one independent
// row per distinct grantor, e.g. two different admins each granting the same
// role to the same member. See InMemory.roleMembers' doc comment.
type roleMembershipKey struct {
	RoleOID    uint32
	MemberOID  uint32
	GrantorOID uint32
}

// RoleMembership is one pg_auth_members row, returned by
// RoleMembershipEntries in deterministic (RoleOID, MemberOID) order.
// M0119-0004-ACLHEAP.
type RoleMembership struct {
	OID           uint32
	RoleOID       uint32
	MemberOID     uint32
	GrantorOID    uint32
	AdminOption   bool
	InheritOption bool
	SetOption     bool
}

// GrantRoleMembership upserts a `GRANT <role> TO <member> [WITH { ADMIN |
// INHERIT | SET } { OPTION | TRUE | FALSE } [, ...]] [GRANTED BY <grantor>]`
// row: a fresh OID is minted the first time (RoleOID, MemberOID, GrantorOID)
// is seen — a DIFFERENT grantor granting the same (role, member) pair mints
// its own independent row, matching real PG's (roleid, member, grantor)
// unique index (AddRoleMems' SearchSysCache3, user.c) — re-granting BY THE
// SAME grantor keeps the existing OID and only updates the option flags.
//
// admin/inherit/set are tri-state: nil means that option was not named in
// this statement (PG's GRANT_ROLE_SPECIFIED_* bitmask unset, GrantRole in
// user.c) — an existing row's value for that option is left untouched
// (mirroring "a plain re-grant never downgrades an unmentioned option"), and
// a fresh row falls back to InitGrantRoleOptions' defaults: admin=false,
// set=true, inherit=the grantee's rolinherit (goopg has no per-role NOINHERIT
// tracking — CREATE/ALTER ROLE never clears it — so every role's rolinherit
// is always true, matching this default exactly). A non-nil pointer is the
// explicit requested value and always applies, including to an existing row
// (may legitimately downgrade e.g. admin_option, unlike an unspecified
// option on a bare re-grant). Returns the row's OID. M0119-0004-ACLHEAP.
func (c *InMemory) GrantRoleMembership(roleOid, memberOid, grantorOid uint32, admin, inherit, set *bool) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := roleMembershipKey{RoleOID: roleOid, MemberOID: memberOid, GrantorOID: grantorOid}
	if existing, ok := c.roleMembers[key]; ok {
		if admin != nil {
			existing.AdminOption = *admin
		}
		if inherit != nil {
			existing.InheritOption = *inherit
		}
		if set != nil {
			existing.SetOption = *set
		}
		return existing.OID
	}
	oid := c.allocOIDLocked()
	c.roleMembers[key] = &RoleMembership{
		OID: oid, RoleOID: roleOid, MemberOID: memberOid,
		GrantorOID:    grantorOid,
		AdminOption:   admin != nil && *admin,
		InheritOption: inherit == nil || *inherit,
		SetOption:     set == nil || *set,
	}
	return oid
}

// RevokeRoleMembership removes or downgrades the ONE `REVOKE <role> FROM
// <member> [GRANTED BY <grantor>]` row identified by (roleOid, memberOid,
// grantorOid) — real PG's plan_single_revoke/DelRoleMems (user.c) only ever
// touches the specific grantor-scoped tuple check_role_grantor resolved,
// leaving any OTHER grantor's independent row on the same (role, member)
// pair untouched. revokeOption is "" for a plain REVOKE (the row is deleted
// entirely) or one of "admin"/"inherit"/"set" for REVOKE's
// `{ADMIN|INHERIT|SET} OPTION FOR` prefix, in which case only that single
// flag is cleared and the membership row survives — matching PG's
// DelRoleMems/plan_single_revoke (RRG_REMOVE_ADMIN_OPTION /
// RRG_REMOVE_INHERIT_OPTION / RRG_REMOVE_SET_OPTION, user.c). Reports
// whether a row existed (REVOKE of a non-existent membership is a silent
// no-op, matching this codebase's other ACL REVOKE paths). M0119-0004-ACLHEAP.
func (c *InMemory) RevokeRoleMembership(roleOid, memberOid, grantorOid uint32, revokeOption string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := roleMembershipKey{RoleOID: roleOid, MemberOID: memberOid, GrantorOID: grantorOid}
	existing, ok := c.roleMembers[key]
	if !ok {
		return false
	}
	switch revokeOption {
	case "admin":
		existing.AdminOption = false
	case "inherit":
		existing.InheritOption = false
	case "set":
		existing.SetOption = false
	default:
		delete(c.roleMembers, key)
	}
	return true
}

// RevokeRoleMembershipCascadeSet computes, WITHOUT mutating any state, the
// additional pg_auth_members rows a whole-row `REVOKE roleOid FROM
// memberOid` or a `REVOKE ADMIN OPTION FOR roleOid FROM memberOid` needs to
// cascade-delete, mirroring plan_recursive_revoke's grantor-chain walk
// (postgres/src/backend/commands/user.c ~2415). Only relevant when
// memberOid's own row currently holds AdminOption==true — a member with no
// ADMIN OPTION on roleOid could not have (re-)granted it to anyone, so
// there is nothing to cascade (PG's early return when the revoked row's
// admin_option is already false); in that case (or when no row exists) this
// returns (nil, false) and the caller applies a plain, non-cascading revoke.
//
// When a cascade is possible: dependentMembers lists every dependent row
// (mirrors PG's per-row full delete, RRG_DELETE_GRANT) that must ALSO be
// revoked — every row transitively granted BY memberOid (regardless of which
// role granted memberOid ITS membership; only the specific (roleOid,
// memberOid, grantorOid) row at the top of the walk is scoped by grantor),
// where the walk continues past a dependent row only if THAT row's own
// AdminOption is also true (it could itself have re-granted further). If
// cascade is false (RESTRICT, including REVOKE's unwritten default) and any
// dependents were found, blocked is true and the caller must raise
// ERRCODE_DEPENDENT_OBJECTS_STILL_EXIST ("2BP01") — "dependent privileges
// exist" / hint "Use CASCADE to revoke them too." — and apply nothing. Each
// DependentRoleMembership pins the exact (member, grantor) row to revoke —
// real PG's "would the member still have admin via ANOTHER untouched row"
// escape hatch (plan_recursive_revoke) is naturally modeled since a member
// can now hold independent rows from multiple grantors and only the one
// implicated by this walk is torn down. M0119-0004-ACLHEAP.
func (c *InMemory) RevokeRoleMembershipCascadeSet(roleOid, memberOid, grantorOid uint32, cascade bool) (dependentMembers []DependentRoleMembership, blocked bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	existing, ok := c.roleMembers[roleMembershipKey{RoleOID: roleOid, MemberOID: memberOid, GrantorOID: grantorOid}]
	if !ok || !existing.AdminOption {
		return nil, false
	}
	var keys []roleMembershipKey
	c.collectRoleMembershipCascadeKeysLocked(roleOid, memberOid, &keys)
	if len(keys) == 0 {
		return nil, false
	}
	if !cascade {
		return nil, true
	}
	dependentMembers = make([]DependentRoleMembership, len(keys))
	for i, k := range keys {
		dependentMembers[i] = DependentRoleMembership{MemberOID: k.MemberOID, GrantorOID: k.GrantorOID}
	}
	return dependentMembers, false
}

// DependentRoleMembership identifies one pg_auth_members row
// RevokeRoleMembershipCascadeSet found downstream of a cascading REVOKE —
// enough to target the exact row via RevokeRoleMembership(roleOid,
// MemberOID, GrantorOID, ""). M0119-0004-ACLHEAP.
type DependentRoleMembership struct {
	MemberOID  uint32
	GrantorOID uint32
}

// collectRoleMembershipCascadeKeysLocked appends every roleOid row granted
// (directly or transitively) BY grantorMember to *out, recursing past a
// found row only when that row's own AdminOption is true. Caller holds
// c.mu (read or write). M0119-0004-ACLHEAP.
func (c *InMemory) collectRoleMembershipCascadeKeysLocked(roleOid, grantorMember uint32, out *[]roleMembershipKey) {
	var children []roleMembershipKey
	for k, m := range c.roleMembers {
		if k.RoleOID == roleOid && m.GrantorOID == grantorMember && k.MemberOID != grantorMember {
			children = append(children, k)
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].MemberOID < children[j].MemberOID })
	for _, k := range children {
		*out = append(*out, k)
		if c.roleMembers[k].AdminOption {
			c.collectRoleMembershipCascadeKeysLocked(roleOid, k.MemberOID, out)
		}
	}
}

// RoleMembershipEntries returns every recorded pg_auth_members row, sorted
// by (RoleOID, MemberOID, GrantorOID) for deterministic virtual-row output —
// the same (RoleOID, MemberOID) pair may now legitimately appear more than
// once, one row per distinct grantor. M0119-0004-ACLHEAP.
func (c *InMemory) RoleMembershipEntries() []RoleMembership {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.roleMembers) == 0 {
		return nil
	}
	out := make([]RoleMembership, 0, len(c.roleMembers))
	for _, m := range c.roleMembers {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RoleOID != out[j].RoleOID {
			return out[i].RoleOID < out[j].RoleOID
		}
		if out[i].MemberOID != out[j].MemberOID {
			return out[i].MemberOID < out[j].MemberOID
		}
		return out[i].GrantorOID < out[j].GrantorOID
	})
	return out
}

// RoleIsMemberOf reports whether memberOid is, directly or transitively, a
// member of roleOid via the recorded pg_auth_members rows (a self-check,
// memberOid == roleOid, always reports true). Mirrors
// is_member_of_role_nosuper's traversal (user.c) — ignoring superuser
// bypass, which does not apply to membership-loop detection. GRANT ROLE
// uses this to reject a membership that would create a cycle (`role "x" is
// a member of role "y"`, ERRCODE_INVALID_GRANT_OPERATION). M0119-0004-ACLHEAP.
func (c *InMemory) RoleIsMemberOf(memberOid, roleOid uint32) bool {
	if memberOid == roleOid {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := map[uint32]bool{memberOid: true}
	queue := []uint32{memberOid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for key := range c.roleMembers {
			if key.MemberOID != cur || seen[key.RoleOID] {
				continue
			}
			if key.RoleOID == roleOid {
				return true
			}
			seen[key.RoleOID] = true
			queue = append(queue, key.RoleOID)
		}
	}
	return false
}

// IsSuperuser reports whether oid names a role with the SUPERUSER attribute:
// the bootstrap superuser (OID 10, always superuser — see
// BootstrapSuperuserOID) or a registered role whose RoleAttrs.Superuser is
// set (CREATE/ALTER ROLE ... SUPERUSER). Mirrors superuser_arg (acl.c).
// Backs check_role_membership_authorization's "to mess with a superuser
// role, you gotta be superuser" gate. M0119-0004-ACLHEAP.
func (c *InMemory) IsSuperuser(oid uint32) bool {
	if oid == BootstrapSuperuserOID {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for name, roid := range c.roles {
		if roid == oid {
			a := c.roleAttrs[name]
			return a != nil && a.Superuser
		}
	}
	return false
}

// IsAdminOfRole reports whether memberOid is the bootstrap/attribute
// superuser, or holds ADMIN OPTION on roleOid — directly, or indirectly via
// ANY membership chain (not gated on INHERIT/SET, matching PG's
// ROLERECURSE_MEMBERS traversal). By policy a role is never its own admin
// (memberOid == roleOid returns false, matching is_admin_of_role's explicit
// carve-out), even though RoleIsMemberOf treats self-membership as true.
// Mirrors is_admin_of_role (postgres/src/backend/utils/adt/acl.c). Backs
// check_role_membership_authorization's "otherwise, must have admin option
// on the role to be changed" branch. M0119-0004-ACLHEAP.
func (c *InMemory) IsAdminOfRole(memberOid, roleOid uint32) bool {
	if c.IsSuperuser(memberOid) {
		return true
	}
	if memberOid == roleOid {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := map[uint32]bool{memberOid: true}
	queue := []uint32{memberOid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for key, m := range c.roleMembers {
			if key.MemberOID != cur {
				continue
			}
			if key.RoleOID == roleOid && m.AdminOption {
				return true
			}
			if !seen[key.RoleOID] {
				seen[key.RoleOID] = true
				queue = append(queue, key.RoleOID)
			}
		}
	}
	return false
}

// HasPrivsOfRole reports whether memberOid inherits the privileges of
// roleOid: memberOid == roleOid, memberOid is a superuser, or roleOid is
// reachable from memberOid via a chain of INHERIT-marked pg_auth_members
// rows (ROLERECURSE_PRIVS). Distinct from RoleIsMemberOf (ignores INHERIT
// entirely, used for membership-cycle detection) and IsAdminOfRole (requires
// ADMIN OPTION, not just membership). Mirrors has_privs_of_role
// (postgres/src/backend/utils/adt/acl.c). Backs check_role_grantor's
// "GRANTED BY must name a role whose privileges the current user possesses"
// gate. M0119-0004-ACLHEAP.
func (c *InMemory) HasPrivsOfRole(memberOid, roleOid uint32) bool {
	if memberOid == roleOid {
		return true
	}
	if c.IsSuperuser(memberOid) {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := map[uint32]bool{memberOid: true}
	queue := []uint32{memberOid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		var next []uint32
		for key, m := range c.roleMembers {
			if key.MemberOID != cur || !m.InheritOption {
				continue
			}
			if key.RoleOID == roleOid {
				return true
			}
			if !seen[key.RoleOID] {
				seen[key.RoleOID] = true
				next = append(next, key.RoleOID)
			}
		}
		sort.Slice(next, func(i, j int) bool { return next[i] < next[j] })
		queue = append(queue, next...)
	}
	return false
}

// SelectBestAdmin finds a role whose privileges memberOid inherits
// (transitively, via INHERIT-marked pg_auth_members rows) that directly
// holds ADMIN OPTION on roleOid — preferring memberOid's own direct ADMIN
// OPTION over an indirect one, and among indirect options the fewest "hops"
// (breadth-first, matching roles_is_member_of's traversal order). Returns 0
// (PG's InvalidOid) if no such role exists. By policy memberOid == roleOid
// never qualifies (a role cannot have ADMIN OPTION on itself). Mirrors
// select_best_admin (postgres/src/backend/utils/adt/acl.c). Backs
// check_role_grantor's implicit-grantor inference (memberOid=currentUserID)
// and its explicit-GRANTED-BY sanity check (memberOid=the named grantor —
// there, the caller requires the return value to equal memberOid itself,
// i.e. the grantor's admin option must be its OWN, not merely inherited).
// M0119-0004-ACLHEAP.
func (c *InMemory) SelectBestAdmin(memberOid, roleOid uint32) uint32 {
	if memberOid == roleOid {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := map[uint32]bool{memberOid: true}
	queue := []uint32{memberOid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for key, m := range c.roleMembers {
			if key.MemberOID == cur && key.RoleOID == roleOid && m.AdminOption {
				return cur
			}
		}
		var next []uint32
		for key, m := range c.roleMembers {
			if key.MemberOID != cur || !m.InheritOption {
				continue
			}
			if !seen[key.RoleOID] {
				seen[key.RoleOID] = true
				next = append(next, key.RoleOID)
			}
		}
		sort.Slice(next, func(i, j int) bool { return next[i] < next[j] })
		queue = append(queue, next...)
	}
	return 0
}

// BootstrapSuperuserOID is OID 10 (BOOTSTRAP_SUPERUSERID / "postgres"),
// goopg's single hardcoded superuser (see the many "OID 10 = bootstrap
// superuser" call sites elsewhere in this file). AddRoleMems' grantor-chain
// circularity check (user.c) exempts grants made BY the bootstrap superuser
// and never allows ADMIN OPTION to be (re-)granted TO it.
const BootstrapSuperuserOID uint32 = 10

// GrantRoleWouldCreateGrantorCycle reports whether granting roleOid's
// membership WITH ADMIN TRUE to newMemberOids (a single `GRANT roleOid TO
// member, ...` statement's full grantee list) would create a "member-grantor
// loop": grantorOid giving ADMIN OPTION on roleOid to someone who is
// grantorOid's ONLY remaining source of ADMIN OPTION on roleOid, which would
// make the grant chain non-acyclic (defeating REVOKE .. CASCADE's ability to
// unwind it). Mirrors AddRoleMems' circularity guard (user.c ~1751), which
// simulates revoking (cascading through) every existing pg_auth_members row
// implicated by the new grantees and then checks whether grantorOid still
// holds an untouched, admin_option row. This is a DIFFERENT check from
// RoleIsMemberOf's role-member loop (A member of B, B member of A) — this
// one is about the ADMIN OPTION GRANT chain, not the membership graph itself.
//
// Caller must already have gated on `admin option requested && grantorOid !=
// bootstrapSuperuserOID` (AddRoleMems' `if (popt->admin && grantorId !=
// BOOTSTRAP_SUPERUSERID)`); grants made by the bootstrap superuser can never
// be circular since it is always everyone's ultimate admin. M0119-0004-ACLHEAP.
func (c *InMemory) GrantRoleWouldCreateGrantorCycle(roleOid uint32, newMemberOids []uint32, grantorOid uint32) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// PG unconditionally rejects (re-)granting ADMIN OPTION to the bootstrap
	// superuser — it needs no source grantor, so any such grant is by
	// definition ungrantable-back-to.
	if slices.Contains(newMemberOids, BootstrapSuperuserOID) {
		return true
	}

	// memlist: every existing pg_auth_members row for THIS roleOid only
	// (SearchSysCacheList1(AUTHMEMROLEMEM, roleid) scopes identically).
	var rows []*RoleMembership
	for key, m := range c.roleMembers {
		if key.RoleOID == roleOid {
			rows = append(rows, m)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].MemberOID < rows[j].MemberOID })

	// deleted[i] tracks whether plan_recursive_revoke would remove rows[i]
	// entirely. This scoped use (only ever reached via plan_member_revoke,
	// never plan_single_revoke) always calls plan_recursive_revoke with
	// revoke_admin_option_only=false and behavior=DROP_CASCADE, so PG's
	// 5-state RevokeRoleGrantAction collapses to this boolean here: every
	// row plan_recursive_revoke would touch ends up RRG_DELETE_GRANT.
	deleted := make([]bool, len(rows))
	var planRecursiveRevoke func(index int)
	planRecursiveRevoke = func(index int) {
		if deleted[index] {
			return
		}
		deleted[index] = true
		member := rows[index].MemberOID
		if !rows[index].AdminOption {
			return
		}
		// Would `member` still hold ADMIN OPTION on roleOid via some other,
		// untouched grant? If so, nothing downstream needs to cascade.
		for i, r := range rows {
			if r.MemberOID == member && r.AdminOption && !deleted[i] {
				return
			}
		}
		// Recurse into grants for which `member` is the grantor — those
		// would lose their ADMIN OPTION basis too.
		for i, r := range rows {
			if r.GrantorOID == member && !deleted[i] {
				planRecursiveRevoke(i)
			}
		}
	}
	for _, mid := range newMemberOids {
		for i, r := range rows {
			if r.MemberOID == mid {
				planRecursiveRevoke(i)
			}
		}
	}

	// If the grantor still holds an untouched, admin_option row on roleOid,
	// it retains the ability to perform this grant — no circularity.
	for i, r := range rows {
		if !deleted[i] && r.MemberOID == grantorOid && r.AdminOption {
			return false
		}
	}
	return true
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
	// Dup-check via lookupIndexLocked (not a bare key(...) map probe): this
	// recovery hook is called from two independent drivers that can disagree
	// on `schema` for the exact same physical index — loadUserIndexesFromHeap
	// resolves the index's real schema from its pg_class namespace OID
	// (e.g. "public"), while replayIndexDDLRecords passes the raw, often
	// unqualified schema captured verbatim in the original CREATE INDEX WAL
	// record (""). A bare key(...) probe treats those as different indexes
	// and registers a second, divergent *Index for the same OID; whichever
	// key a later ALTER INDEX RENAME/DROP recovery call happens to hit then
	// silently misses the other one (an unqualified rename could leave the
	// old name "resurrected" under the untouched duplicate — caught by
	// TestRenameIndexSurvivesRestartViaWAL once loadUserIndexesFromHeap
	// started actually finding pg_index rows, M0119-0004 index-reloptions
	// follow-up). lookupIndexLocked already implements the same "" vs
	// "public." collision fallback reads rely on, so reusing it here keeps
	// recovery's notion of "same index" consistent with LookupIndex's.
	if existing, existingKey, dup := c.lookupIndexLocked(parser.ObjectName{Schema: schema, Name: name}); dup {
		// JSON snapshot or earlier recovery pass already registered
		// this index. Idempotent no-op.
		_ = existing
		_ = existingKey
		c.advanceNextOIDLocked(oid)
		return
	}
	k := key(parser.ObjectName{Schema: schema, Name: name})
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
	// Resolve via lookupIndexLocked, not a bare key(...) probe — same "" vs
	// "public." collision rationale as RegisterIndexDuringRecovery above: a
	// DROP INDEX WAL record's raw (often unqualified) schema must resolve to
	// whatever key the index actually lives under, or the drop silently
	// no-ops and the index is "resurrected" after restart.
	idx, k, ok := c.lookupIndexLocked(parser.ObjectName{Schema: schema, Name: name})
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
	return c.allocOIDLocked()
}

// allocOIDLocked is AllocOID's body for callers that already hold c.mu.
func (c *InMemory) allocOIDLocked() uint32 {
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
		// Columns match the PG18-canonical 34-column pg_class tupdesc written by
		// syncTableToCatalogHeap / pgClassColumnsPG18(). This alignment is required
		// so that scanMatching can decode physical pg_class heap tuples for UPDATE
		// (e.g. "UPDATE pg_class SET reltuples = ... WHERE oid = ...").
		// M0100-0010: sysupd2/sysmerge2 concurrent-update blocking fix.
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "relname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "relnamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "reltype", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "reloftype", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "relowner", Type: Type{Name: "oid"}, Ordinal: 5},
			{Name: "relam", Type: Type{Name: "oid"}, Ordinal: 6},
			{Name: "relfilenode", Type: Type{Name: "oid"}, Ordinal: 7},
			{Name: "reltablespace", Type: Type{Name: "oid"}, Ordinal: 8},
			{Name: "relpages", Type: Type{Name: "int4"}, Ordinal: 9},
			{Name: "reltuples", Type: Type{Name: "float4"}, Ordinal: 10},
			{Name: "relallvisible", Type: Type{Name: "int4"}, Ordinal: 11},
			{Name: "relallfrozen", Type: Type{Name: "int4"}, Ordinal: 12},
			{Name: "reltoastrelid", Type: Type{Name: "oid"}, Ordinal: 13},
			{Name: "relhasindex", Type: Type{Name: "bool"}, Ordinal: 14},
			{Name: "relisshared", Type: Type{Name: "bool"}, Ordinal: 15},
			{Name: "relpersistence", Type: Type{Name: "char"}, Ordinal: 16},
			{Name: "relkind", Type: Type{Name: "char"}, Ordinal: 17},
			{Name: "relnatts", Type: Type{Name: "int2"}, Ordinal: 18},
			{Name: "relchecks", Type: Type{Name: "int2"}, Ordinal: 19},
			{Name: "relhasrules", Type: Type{Name: "bool"}, Ordinal: 20},
			{Name: "relhastriggers", Type: Type{Name: "bool"}, Ordinal: 21},
			{Name: "relhassubclass", Type: Type{Name: "bool"}, Ordinal: 22},
			{Name: "relrowsecurity", Type: Type{Name: "bool"}, Ordinal: 23},
			{Name: "relforcerowsecurity", Type: Type{Name: "bool"}, Ordinal: 24},
			{Name: "relispopulated", Type: Type{Name: "bool"}, Ordinal: 25},
			{Name: "relreplident", Type: Type{Name: "char"}, Ordinal: 26},
			{Name: "relispartition", Type: Type{Name: "bool"}, Ordinal: 27},
			{Name: "relrewrite", Type: Type{Name: "oid"}, Ordinal: 28},
			{Name: "relfrozenxid", Type: Type{Name: "xid"}, Ordinal: 29},
			{Name: "relminmxid", Type: Type{Name: "xid"}, Ordinal: 30},
			{Name: "relacl", Type: Type{Name: "aclitem[]"}, Ordinal: 31},
			{Name: "reloptions", Type: Type{Name: "text[]"}, Ordinal: 32},
			{Name: "relpartbound", Type: Type{Name: "pg_node_tree"}, Ordinal: 33},
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
			if t.Virtual && t.View == nil && !t.IsMatView && !t.IsSequence {
				// Skip system-catalog virtual tables (pg_class, pg_locks, etc.)
				// but include user views (t.View != nil), materialized views, and
				// user sequences (relkind='S'). Sequences are virtual tables too,
				// but unlike system catalogs they must be discoverable by pg_dump's
				// getTables (which selects relkind IN ('r','S','v','c','m','f','p'))
				// so the sequence is dumped via dumpSequence. M0110-0001 (DU-002
				// slice 116).
				continue
			}
			relkind := "r"
			// relam: heap (2) for ordinary relations; 0 for sequences. PG sets
			// pg_class.relam=0 for sequences (RELKIND_HAS_TABLE_AM excludes
			// RELKIND_SEQUENCE — see pg_class.h: sequences use the heap AM only at
			// the relcache level, not via pg_class.relam). This is load-bearing for
			// pg_amcheck parity: its relation CTE selects only relations with
			// relam=HEAP_TABLE_AM_OID for heap verification, so relam=0 keeps the
			// storage-less virtual sequence out of verify_heapam (which would fail).
			// M0110-0001 (DU-002 slice 116).
			relam := "2"
			if t.IsSequence {
				relkind = "S"
				relam = "0"
			} else if t.View != nil && !t.IsMatView {
				relkind = "v"
			} else if t.IsMatView {
				relkind = "m"
			} else if t.PartitionMethod != "" && t.PartitionParentOID == 0 {
				relkind = "p"
			} else if t.ForeignServerName != "" {
				relkind = "f"
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
			// A temporary relation lives in its owning session's per-backend
			// namespace (pg_temp_<id>), not public — mirrors PostgreSQL so a
			// `WHERE relnamespace = pg_my_temp_schema()` scan finds it (and finds
			// nothing once it is cleaned up). M0118-0009 (temp-schema-cleanup,
			// design 0118-0091). Falls back to the schema OID for legacy
			// session-less temp relations (TempOwner == "").
			if t.Temp && t.TempOwner != "" {
				if tns := c.tempNamespaceOIDLocked(t.TempOwner); tns != 0 {
					nsOID = tns
				}
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
			// relchecks must equal the number of contype='c' rows pg_constraint
			// emits for this table (the visible, named+OID'd CHECK constraints).
			// pg_dump gates its per-table CHECK query on relchecks>0 and then
			// asserts the row count matches exactly (getTableAttrs), so a 0 here
			// silently drops every CHECK from the dumped CREATE TABLE. M0110-0001.
			relchecks := 0
			for _, nc := range t.NamedChecks {
				if nc.Name != "" && nc.OID != 0 {
					relchecks++
				}
			}
			// reloptions surfaces the table's storage parameters as a text[]
			// array literal (`{fillfactor=70}`). Empty → "" → planner maps it to
			// SQL NULL (DU-002 slice 47), so a plain table emits no WITH clause;
			// a non-empty one round-trips through pg_dump as `WITH
			// (fillfactor='70')`. M0110-0001 (DU-002 slice 54). BuildTableReloptions
			// is the single source of truth (shared with executor.buildUserPGClassRow's
			// heap-persisted row, M0119-0004) so the two never drift apart.
			reloptions := BuildTableReloptions(t)
			// reltoastrelid: PG auto-creates a TOAST relation for every ordinary
			// table / materialized view with at least one toastable (varlena)
			// column (needs_toast_table, src/backend/catalog/toasting.c), plus
			// any table carrying explicit `toast.*` storage parameters. Point
			// reltoastrelid at the synthesized pg_toast_<oid> row emitted below
			// so `reltoastrelid::regclass` resolves and pg_dump's toast LEFT JOIN
			// re-emits the `toast.*` reloptions. Partitioned parents (relkind='p',
			// no storage), views, sequences and foreign tables never get one.
			// DU-002 slice 224 + M0118-0008 TOAST-exposure slice 1 (0118-0084).
			// Auto-exposure is restricted to USER relations (OID >= 16384):
			// goopg serves system catalogs virtually with no real heap storage,
			// so attaching a reltoastrelid to e.g. pg_type/pg_attribute would make
			// pg_amcheck follow the join and fail to open the non-existent toast
			// heap. Explicit toast.* reloptions only ever land on user tables.
			// hasToastRel is computed by tableHasToastRelation (shared with
			// ToastRelName so exposure and regclass rendering stay in sync). The
			// relkind 'r'/'m' filter lives inside that helper.
			hasToastRel := tableHasToastRelation(t)
			reltoastrelid := "0"
			if hasToastRel {
				reltoastrelid = strconv.Itoa(int(t.OID) + toastRelidOffset)
			}
			// relpages / reltuples: 0 until VACUUM or ANALYZE has run (Stats==nil),
			// then the persisted block count and live-tuple estimate. Mirrors
			// pg_class — a freshly created, never-analyzed relation reads back as
			// relpages=0 / reltuples=-1 in real PG, but goopg has historically
			// surfaced 0 here, so keep 0 for the unanalyzed case to avoid churning
			// every catalog-reading test; populate the real counts once Stats lands.
			// M0118-0008 (vacuum-no-cleanup-lock).
			relpages := "0"
			reltuples := "0"
			if t.Stats != nil {
				relpages = strconv.Itoa(t.Stats.Pages)
				reltuples = strconv.FormatInt(t.Stats.RowCount, 10)
			}
			replIdent := ReplIdentOrDefault(t.ReplicaIdentity) // relreplident (DU-002 slice 305)
			// relhastriggers gates pg_dump's getTriggers: a table whose
			// relhastriggers='f' is excluded from the tbloids array, so pg_dump
			// never probes pg_trigger for it and the trigger is silently dropped.
			// Project 't' whenever the table owns at least one trigger. DU-002 slice 319.
			relHasTriggers := "f"
			if len(t.Triggers) > 0 {
				relHasTriggers = "t"
			}
			// relacl — NULL until a GRANT records non-owner privileges, then the
			// materialized aclitem[] (owner full + each grantee). pg_dump's
			// getTables reads this directly and re-emits GRANTs (buildACLCommands,
			// client-side). DU-002 slice 331. Sequences (relkind 'S') render with
			// the sequence privilege set / owner default "rwU" so a GRANT … ON
			// SEQUENCE round-trips against acldefault('s', owner). DU-002 slice 333.
			// reloftype: the composite type OID for a typed table (`CREATE TABLE
			// name OF type`), "0" otherwise. Hoisted to a local so the row literal
			// keeps its single-token column width (and comment alignment). DU-002 slice 374.
			relOfType := strconv.Itoa(int(t.OfTypeOID))
			relacl := c.relaclTextLocked(t.OID)
			if t.IsSequence {
				relacl = c.relaclTextLockedSeq(t.OID)
			}
			out = append(out, []string{
				strconv.Itoa(int(t.OID)),     // 0:  oid
				t.Name,                       // 1:  relname
				strconv.Itoa(int(nsOID)),     // 2:  relnamespace
				"0",                          // 3:  reltype
				relOfType,                    // 4:  reloftype (typed table `OF type`; 0 otherwise, DU-002 slice 374)
				"10",                         // 5:  relowner (bootstrap superuser)
				relam,                        // 6:  relam (heap=2; 0 for sequences)
				strconv.Itoa(int(t.OID)),     // 7:  relfilenode
				"0",                          // 8:  reltablespace
				relpages,                     // 9:  relpages
				reltuples,                    // 10: reltuples
				"0",                          // 11: relallvisible
				"0",                          // 12: relallfrozen
				reltoastrelid,                // 13: reltoastrelid
				hasIdx,                       // 14: relhasindex
				"f",                          // 15: relisshared
				relpers,                      // 16: relpersistence
				relkind,                      // 17: relkind
				strconv.Itoa(len(t.Columns)), // 18: relnatts
				strconv.Itoa(relchecks),      // 19: relchecks
				"f",                          // 20: relhasrules
				relHasTriggers,               // 21: relhastriggers
				func() string {
					if len(c.partitionChildren[t.OID]) > 0 {
						return "t"
					}
					return "f"
				}(), // 22: relhassubclass
				boolToPGChar(t.RowSecurity),      // 23: relrowsecurity (DU-002 slice 322)
				boolToPGChar(t.ForceRowSecurity), // 24: relforcerowsecurity (DU-002 slice 322)
				populated,                        // 25: relispopulated
				replIdent,                        // 26: relreplident
				isPartition, // 27: relispartition
				"0",         // 28: relrewrite
				"0",         // 29: relfrozenxid
				"1",         // 30: relminmxid
				relacl,      // 31: relacl (NULL until a table GRANT, slice 331)
				reloptions,  // 32: reloptions ({fillfactor=N} or NULL)
				partBound,   // 33: relpartbound
			})
			// Synthesize the TOAST relation (relkind='t') whenever the table owns
			// one (hasToastRel: a toastable column or explicit `toast.*` reloptions).
			// pg_dump joins to it via reltoastrelid and reads its reloptions (stored
			// WITHOUT the `toast.` prefix), re-emitting them as `WITH (toast.<opt>='…')`.
			// The TOAST row is filtered out of pg_dump's getTables WHERE
			// (relkind IN r/S/v/c/m/f/p — not 't') so it is never dumped as an
			// object; it exists only as a join target. Named pg_toast_<oid> in
			// the pg_toast namespace (OID 99), mirroring PG. DU-002 slice 224 +
			// M0118-0008 TOAST-exposure slice 1 (0118-0084).
			if hasToastRel {
				toastOID := int(t.OID) + toastRelidOffset
				// reloptions is NULL unless the table set explicit toast.* params.
				toastReloptions := ""
				if len(t.ToastReloptions) > 0 {
					toastReloptions = arrayTextLiteral(t.ToastReloptions)
				}
				out = append(out, []string{
					strconv.Itoa(toastOID), // 0:  oid
					// relname honours an ALTER … RENAME override (slice 4).
					c.toastDisplayNameLocked(uint32(toastOID), "pg_toast_"+strconv.Itoa(int(t.OID))), // 1:  relname
					"99",                   // 2:  relnamespace (pg_toast)
					"0",                    // 3:  reltype
					"0",                    // 4:  reloftype
					"10",                   // 5:  relowner
					"0",                    // 6:  relam
					strconv.Itoa(toastOID), // 7:  relfilenode
					"0",                    // 8:  reltablespace
					"0",                    // 9:  relpages
					"0",                    // 10: reltuples
					"0",                    // 11: relallvisible
					"0",                    // 12: relallfrozen
					"0",                    // 13: reltoastrelid
					"t",                    // 14: relhasindex (pg_toast_<oid>_index)
					"f",                    // 15: relisshared
					relpers,                // 16: relpersistence (inherits the table's)
					"t",                    // 17: relkind (TOAST)
					"3",                    // 18: relnatts (chunk_id, chunk_seq, chunk_data)
					"0",                    // 19: relchecks
					"f",                    // 20: relhasrules
					"f",                    // 21: relhastriggers
					"f",                    // 22: relhassubclass
					"f",                    // 23: relrowsecurity
					"f",                    // 24: relforcerowsecurity
					"t",                    // 25: relispopulated
					"n",                    // 26: relreplident
					"f",                    // 27: relispartition
					"0",                    // 28: relrewrite
					"0",                    // 29: relfrozenxid
					"1",                    // 30: relminmxid
					"",                     // 31: relacl (NULL)
					toastReloptions,        // 32: reloptions ({autovacuum_enabled=false})
					"",                     // 33: relpartbound
				})
				// Synthesize the TOAST relation's unique btree index
				// pg_toast_<oid>_index (relkind='i'), mirroring the index PG
				// auto-creates on every TOAST relation's (chunk_id, chunk_seq).
				// Lives in the pg_toast namespace (OID 99); the matching pg_index
				// row (indexrelid=toastIdxOID, indrelid=toastOID) is emitted by the
				// pg_index virtual builder. Isolation specs locate it via
				// `indexrelid::regclass` from pg_index. M0118-0008 TOAST-exposure
				// slice 3.
				toastIdxOID := int(t.OID) + toastIndexOidOffset
				out = append(out, []string{
					strconv.Itoa(toastIdxOID), // 0:  oid
					// relname honours an ALTER INDEX … RENAME override (slice 4).
					c.toastDisplayNameLocked(uint32(toastIdxOID), "pg_toast_"+strconv.Itoa(int(t.OID))+"_index"), // 1:  relname
					"99",                      // 2:  relnamespace (pg_toast)
					"0",                       // 3:  reltype
					"0",                       // 4:  reloftype
					"10",                      // 5:  relowner
					"403",                     // 6:  relam (btree)
					strconv.Itoa(toastIdxOID), // 7:  relfilenode
					"0",                       // 8:  reltablespace
					"0",                       // 9:  relpages
					"-1",                      // 10: reltuples (-1 = unknown for indexes)
					"0",                       // 11: relallvisible
					"0",                       // 12: relallfrozen
					"0",                       // 13: reltoastrelid
					"f",                       // 14: relhasindex
					"f",                       // 15: relisshared
					relpers,                   // 16: relpersistence (inherits the table's)
					"i",                       // 17: relkind (index)
					"2",                       // 18: relnatts (chunk_id, chunk_seq)
					"0",                       // 19: relchecks
					"f",                       // 20: relhasrules
					"f",                       // 21: relhastriggers
					"f",                       // 22: relhassubclass
					"f",                       // 23: relrowsecurity
					"f",                       // 24: relforcerowsecurity
					"t",                       // 25: relispopulated
					"n",                       // 26: relreplident
					"f",                       // 27: relispartition
					"0",                       // 28: relrewrite
					"0",                       // 29: relfrozenxid
					"1",                       // 30: relminmxid
					"",                        // 31: relacl (NULL)
					"",                        // 32: reloptions (NULL)
					"",                        // 33: relpartbound
				})
			}
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
			// Index reloptions: `WITH (fillfactor=N[, deduplicate_items=on|off])`.
			// pg_dump reads this via `t.reloptions AS indreloptions` (the index's
			// own pg_class row) and re-emits `CREATE INDEX … WITH (…)`. Empty
			// (NULL) when unset so a plain index dumps byte-identically. Options
			// are joined in declaration-stable order (fillfactor first), mirroring
			// the array order PG stores. DU-002 slices 218/219.
			idxReloptions := BuildIndexReloptions(idx)
			out = append(out, []string{
				strconv.Itoa(int(idx.OID)),  // 0:  oid
				idx.Name,                    // 1:  relname
				strconv.Itoa(int(idxNsOID)), // 2:  relnamespace
				"0",                         // 3:  reltype
				"0",                         // 4:  reloftype
				"10",                        // 5:  relowner
				idxRelam,                    // 6:  relam
				strconv.Itoa(int(idx.OID)),  // 7:  relfilenode
				"0",                         // 8:  reltablespace
				"0",                         // 9:  relpages
				"-1",                        // 10: reltuples (-1 = unknown for indexes)
				"0",                         // 11: relallvisible
				"0",                         // 12: relallfrozen
				"0",                         // 13: reltoastrelid
				"f",                         // 14: relhasindex
				"f",                         // 15: relisshared
				idxPers,                     // 16: relpersistence
				idxRelkind,                  // 17: relkind
				"0",                         // 18: relnatts
				"0",                         // 19: relchecks
				"f",                         // 20: relhasrules
				"f",                         // 21: relhastriggers
				idxHasSubclass,              // 22: relhassubclass
				"f",                         // 23: relrowsecurity
				"f",                         // 24: relforcerowsecurity
				"t",                         // 25: relispopulated
				"n",                         // 26: relreplident
				"f",                         // 27: relispartition
				"0",                         // 28: relrewrite
				"0",                         // 29: relfrozenxid
				"1",                         // 30: relminmxid
				"",                          // 31: relacl (NULL)
				idxReloptions,               // 32: reloptions ({fillfactor=N} or NULL)
				"",                          // 33: relpartbound
			})
		}
		// Emit the implicit relation (relkind='c') backing each composite type
		// (`CREATE TYPE x AS (...)`). pg_dump's getTypes reads
		// `(SELECT relkind FROM pg_class WHERE oid = typrelid)`, and
		// selectDumpableType keeps the type only when that relkind is 'c'
		// (RELKIND_COMPOSITE_TYPE) — otherwise the type is treated as a table
		// rowtype and skipped. The companion pg_attribute field rows are
		// heap-backed (written by syncCompositeTypeToCatalogHeap); pg_class is
		// virtual, so the relation must be surfaced here too. A composite-type
		// relation has no storage and no access method (relam=0, relfilenode=0).
		// DU-002 slice 243.
		ctKeys := make([]string, 0, len(c.compositeTypes))
		for k := range c.compositeTypes {
			ctKeys = append(ctKeys, k)
		}
		sort.Strings(ctKeys)
		for _, k := range ctKeys {
			ct := c.compositeTypes[k]
			out = append(out, []string{
				strconv.Itoa(int(ct.RelOID)), // 0:  oid
				ct.Name,                      // 1:  relname
				"2200",                       // 2:  relnamespace (public)
				strconv.Itoa(int(ct.OID)),    // 3:  reltype (the composite pg_type OID)
				"0",                          // 4:  reloftype
				"10",                         // 5:  relowner
				"0",                          // 6:  relam (no access method)
				"0",                          // 7:  relfilenode (no storage)
				"0",                          // 8:  reltablespace
				"0",                          // 9:  relpages
				"0",                          // 10: reltuples
				"0",                          // 11: relallvisible
				"0",                          // 12: relallfrozen
				"0",                          // 13: reltoastrelid
				"f",                          // 14: relhasindex
				"f",                          // 15: relisshared
				"p",                          // 16: relpersistence
				"c",                          // 17: relkind (composite type)
				strconv.Itoa(len(ct.Fields)), // 18: relnatts
				"0",                          // 19: relchecks
				"f",                          // 20: relhasrules
				"f",                          // 21: relhastriggers
				"f",                          // 22: relhassubclass
				"f",                          // 23: relrowsecurity
				"f",                          // 24: relforcerowsecurity
				"t",                          // 25: relispopulated
				"n",                          // 26: relreplident (no storage)
				"f",                          // 27: relispartition
				"0",                          // 28: relrewrite
				"0",                          // 29: relfrozenxid (InvalidTransactionId)
				"0",                          // 30: relminmxid
				"",                           // 31: relacl (NULL)
				"",                           // 32: reloptions (NULL)
				"",                           // 33: relpartbound
			})
		}
		// Include pg_class itself (OID 1259, relkind='r', pg_catalog namespace OID 11).
		// PostgreSQL's pg_class is a real heap table; oid::int8 queries like
		//   SELECT oid::int8 FROM pg_class WHERE relname = 'pg_class'
		// must return 1259. M0097-0029.
		out = append(out, []string{
			"1259",     // 0:  oid
			"pg_class", // 1:  relname
			"11",       // 2:  relnamespace (pg_catalog OID=11)
			"0",        // 3:  reltype
			"0",        // 4:  reloftype
			"10",       // 5:  relowner
			"2",        // 6:  relam (heap)
			"1259",     // 7:  relfilenode
			"0",        // 8:  reltablespace
			"0",        // 9:  relpages
			"0",        // 10: reltuples
			"0",        // 11: relallvisible
			"0",        // 12: relallfrozen
			"0",        // 13: reltoastrelid
			"t",        // 14: relhasindex
			"t",        // 15: relisshared (pg_class is a shared catalog)
			"p",        // 16: relpersistence
			"r",        // 17: relkind
			"34",       // 18: relnatts (34 columns, PG18-canonical)
			"0",        // 19: relchecks
			"f",        // 20: relhasrules
			"f",        // 21: relhastriggers
			"f",        // 22: relhassubclass
			"f",        // 23: relrowsecurity
			"f",        // 24: relforcerowsecurity
			"t",        // 25: relispopulated
			"n",        // 26: relreplident
			"f",        // 27: relispartition
			"0",        // 28: relrewrite
			"0",        // 29: relfrozenxid
			"1",        // 30: relminmxid
			"",         // 31: relacl (NULL)
			"",         // 32: reloptions (NULL)
			"",         // 33: relpartbound
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
				strconv.Itoa(int(c.SchemaOwnerOID(s.name))), // nspowner (ALTER SCHEMA ... OWNER TO, DU-002 slice 440 resume point (3); defaults to bootstrap superuser)
				c.NamespaceACLText(s.oid),                   // nspacl (NULL until a schema GRANT, slice 335)
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
	// The global VirtualRows returns every extension row (dbFilter==""); the
	// executor swaps in a per-connection, database-scoped view via
	// ExtensionRowsForDB so an extension installed in one database is invisible
	// in another (mirrors PostgreSQL's per-database pg_extension). M0110-0003.
	pgExtension.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.extensionRowsLocked("")
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
			{Name: "datdba", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "encoding", Type: Type{Name: "int4"}, Ordinal: 3},
			// Additional columns for vacuumdb --all (M0095-0004).
			{Name: "datallowconn", Type: Type{Name: "boolean"}, Ordinal: 4},
			{Name: "datconnlimit", Type: Type{Name: "int4"}, Ordinal: 5},
			// datistemplate: standard pg_database column; false for all live databases (M0097-0021).
			{Name: "datistemplate", Type: Type{Name: "boolean"}, Ordinal: 6},
			// datfrozenxid / datminmxid: standard pg_database wraparound-horizon
			// columns. goopg already computes the cluster-wide datfrozenxid
			// candidate (DatFrozenXID = min(relfrozenxid) across user heaps,
			// mirroring vac_update_datfrozenxid) but never exposed it through the
			// catalog; surfacing it here lets monitoring queries such as
			// `SELECT datname, age(datfrozenxid) FROM pg_database` and the
			// intra-grant-inplace-db isolation spec's `SELECT datfrozenxid FROM
			// pg_database` resolve the column instead of erroring 42703. M0117-0008.
			{Name: "datfrozenxid", Type: Type{Name: "xid"}, Ordinal: 7},
			{Name: "datminmxid", Type: Type{Name: "xid"}, Ordinal: 8},
			// dattablespace / datcollate / datctype / datlocprovider / datlocale /
			// daticurules / datcollversion / datacl: added so pg_dump's
			// getDatabases query (dumpDatabase, only issued under -C/--create —
			// dopt.outputCreateDB) resolves instead of erroring 42703 on
			// datcollate. Values mirror what a fresh `initdb --locale=C` libc
			// cluster's real bootstrapPostgresDatabase heap row carries
			// (internal/initdb/initdb.go); goopg v0 does not track per-database
			// locale/tablespace overrides, so every row reports the bootstrap
			// default. M0119-0004-ACLHEAP (datacl half).
			{Name: "dattablespace", Type: Type{Name: "oid"}, Ordinal: 9},
			{Name: "datcollate", Type: Type{Name: "text"}, Ordinal: 10},
			{Name: "datctype", Type: Type{Name: "text"}, Ordinal: 11},
			{Name: "datlocprovider", Type: Type{Name: "char"}, Ordinal: 12},
			{Name: "datlocale", Type: Type{Name: "text"}, Ordinal: 13},
			{Name: "daticurules", Type: Type{Name: "text"}, Ordinal: 14},
			{Name: "datcollversion", Type: Type{Name: "text"}, Ordinal: 15},
			// datacl: the GRANT/REVOKE … ON DATABASE … projection (this slice).
			// NULL (VirtualNull) until a GRANT is recorded, matching
			// acldefault('d', datdba) so pg_dump emits no ACL commands for an
			// ungranted database.
			{Name: "datacl", Type: Type{Name: "aclitem[]"}, Ordinal: 16},
		},
		OID:     1262, // upstream's DatabaseRelationId
		Virtual: true,
	}
	pgDatabase.VirtualRows = func() [][]string {
		// M0054-0001: enumerate the live database registry instead
		// of hard-coding a single `postgres` row. CREATE DATABASE
		// adds entries; the recovery driver replays them.
		names := c.ListDatabases()
		// datfrozenxid: the cluster-wide candidate (min relfrozenxid across user
		// heaps). When no user heap has been frozen yet DatFrozenXID returns
		// InvalidTransactionID(0); report the bootstrap FrozenTransactionID(2)
		// instead so the column never shows a non-existent XID 0 (mirrors PG's
		// fresh-database datfrozenxid). datminmxid is the FirstMultiXactId(1)
		// bootstrap value — goopg never advances a per-database multixact freeze
		// horizon, so 1 is the accurate floor.
		datFrozen := c.DatFrozenXID()
		if datFrozen == storage.InvalidTransactionID {
			datFrozen = storage.FrozenTransactionID
		}
		datFrozenStr := strconv.FormatUint(uint64(datFrozen), 10)
		out := make([][]string, 0, len(names))
		for _, n := range names {
			// The bootstrap template databases carry their canonical PG
			// attributes: template1 (oid 1) is connectable, template0
			// (oid 4) is not (datallowconn=false), and both are templates
			// (datistemplate=true). Mirrors initdb's pg_database seed
			// (initdb.go buildRow). Clients such as pg_amcheck filter on
			// `datallowconn AND datconnlimit != -2`, so template0 is
			// correctly omitted from --all while template1 is included.
			oid, datallowconn, datistemplate := "16384", "true", "false"
			switch n {
			case "template1":
				oid, datallowconn, datistemplate = "1", "true", "true"
			case "template0":
				oid, datallowconn, datistemplate = "4", "false", "true"
			}
			// datacl is keyed by c.DBOID() — the REAL on-disk OID read from the
			// physical global/1262 heap by detectCatalogDBOID at startup (PG18's
			// well-known postgres database OID 5) — NOT by this row's displayed
			// "oid" placeholder above. execDatabaseACLChange / resyncDatabaseACLHeapRow
			// key the ACL store and the physical heap resync under c.DBOID() (the
			// heap resync MUST match the real on-disk tuple's oid column), so this
			// lookup mirrors that key rather than the legacy 16384
			// firstNormalObjectOID placeholder other subsystems (e.g. CREATE
			// SUBSCRIPTION's subdbid) already depend on for "the" connected
			// database — changing the displayed oid to match broke pg_dump's
			// subscription round-trip (subdbid join no longer matched). Only the
			// live "postgres" row can carry a granted ACL (execDatabaseACLChange's
			// v0 single-database scope), so every other row is unconditionally NULL.
			datacl := VirtualNull
			if n == "postgres" {
				if aclText := c.DatabaseACLText(c.DBOID()); aclText != "" {
					datacl = aclText
				}
			}
			out = append(out, []string{
				oid, // oid: conventional database OID (M0097-0021)
				n,
				"10",         // datdba: OID of owner (10 = postgres superuser)
				"6",          // encoding: 6 = UTF8
				datallowconn, // datallowconn: allow connections
				// datconnlimit: runtime override via `UPDATE pg_database SET
				// datconnlimit = ...` (SetDatabaseConnLimit), default 0 = no
				// limit. vacuumdb/pg_amcheck filter on `datconnlimit <> -2`.
				strconv.FormatInt(int64(c.DatabaseConnLimit(n)), 10),
				datistemplate, // datistemplate: true for template0/template1
				datFrozenStr,  // datfrozenxid: cluster-wide min(relfrozenxid), bootstrap floor 2
				"1",           // datminmxid: FirstMultiXactId bootstrap floor
				"1663",        // dattablespace: pg_default (goopg v0 has no per-DB tablespace override)
				"C",           // datcollate: fresh `initdb --locale=C` bootstrap value
				"C",           // datctype: fresh `initdb --locale=C` bootstrap value
				"c",           // datlocprovider: 'c' = libc (bootstrap default provider)
				VirtualNull,   // datlocale: NULL under the libc provider
				VirtualNull,   // daticurules: NULL (no ICU rules)
				VirtualNull,   // datcollversion: NULL (recomputed on restore, mirrors pg_collation)
				datacl,        // datacl: GRANT/REVOKE … ON DATABASE … projection (M0119-0004-ACLHEAP)
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
			// oid first: pg_dump's collectRoleNames issues
			// `SELECT oid, rolname FROM pg_catalog.pg_roles ORDER BY 1`
			// to build its role-oid → name map (pg_dump.c:10548).
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "rolname", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "rolsuper", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "rolcanlogin", Type: Type{Name: "text"}, Ordinal: 3},
		},
		OID:     1259102, // synthetic — upstream's pg_roles is a view, no fixed low OID (1260 is pg_authid's, see below)
		Virtual: true,
	}
	pgRoles.VirtualRows = func() [][]string {
		// OID 10 = BOOTSTRAP_SUPERUSERID (postgres superuser),
		// per postgres/src/include/catalog/pg_authid.dat.
		out := [][]string{{"10", "postgres", "t", "t"}}
		// User-created roles (CREATE ROLE / CREATE USER) follow, each with the
		// OID minted at registration time, sorted by name for deterministic
		// output. pg_dump's getPolicies resolves pg_policy.polroles OIDs back to
		// names through this view, so named-role policies round-trip. DU-002
		// slice 330.
		c.mu.RLock()
		names := make([]string, 0, len(c.roles))
		for name := range c.roles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			// Report the recorded attributes (root-0021); a role with no
			// sidecar entry keeps the historical 'f'/'t' defaults so
			// pre-existing pg_dump policy-role resolution is unchanged.
			rolsuper, rolcanlogin := "f", "t"
			if a := c.roleAttrs[name]; a != nil {
				if a.Superuser {
					rolsuper = "t"
				}
				if !a.CanLogin {
					rolcanlogin = "f"
				}
			}
			out = append(out, []string{fmt.Sprintf("%d", c.roles[name]), name, rolsuper, rolcanlogin})
		}
		c.mu.RUnlock()
		// PG18's 16 built-in "pg_*" predefined roles (pg_authid.dat) — always
		// rolsuper='f'/rolcanlogin='f', matching SyncPgAuthidFile's frozen
		// predefined rows (internal/executor/pg_authid_sync.go). Needed so
		// pg_dumpall's dumpRoleMembership query (which LEFT JOINs pg_roles to
		// resolve a membership row's role/member name) doesn't silently drop a
		// `GRANT pg_read_all_data TO alice`-style row when ur.rolname/
		// um.rolname come back NULL for a predefined grantee (0119-0004ch
		// ledger discovery (a)).
		predefinedNames := make([]string, 0, len(predefinedRoleSeeds))
		for _, s := range predefinedRoleSeeds {
			predefinedNames = append(predefinedNames, s.name)
		}
		sort.Strings(predefinedNames)
		for _, name := range predefinedNames {
			out = append(out, []string{fmt.Sprintf("%d", c.predefinedRoles[name]), name, "f", "f"})
		}
		return out
	}
	c.tables["pg_catalog.pg_roles"] = pgRoles

	// pg_authid — the real, superuser-only role catalog pg_roles is a view
	// over. pg_dumpall's dumpRoles/dumpUserConfig query it directly (not
	// pg_roles) for the full attribute set + rolpassword (M0119-0004-ACLHEAP
	// follow-up: pg_dumpall was failing outright with "relation \"pg_authid\"
	// does not exist" before this — pg_roles alone never covered it). Sourced
	// from the same live c.roles/c.roleAttrs state as pg_roles, not the
	// on-disk global/1260 heap file: that file (pg_authid_sync.go) is a
	// separate crash-recovery mirror for auth credentials, not a live SQL
	// read path. rolinherit is never modelled (goopg's role DDL has no
	// per-role NOINHERIT tracking) and always reports PG's CREATE ROLE
	// default 't'. rolcreaterole/rolcreatedb/rolreplication/rolbypassrls/
	// rolconnlimit/rolvaliduntil now reflect the RoleAttrs sidecar (DU-002
	// slice 439 follow-up); a role with no sidecar entry (predefined pg_*
	// roles, unregistered names) falls back to PG's own CREATE ROLE
	// defaults.
	pgAuthid := &Table{
		Schema: "pg_catalog",
		Name:   "pg_authid",
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "rolname", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "rolsuper", Type: Type{Name: "bool"}, Ordinal: 2},
			{Name: "rolinherit", Type: Type{Name: "bool"}, Ordinal: 3},
			{Name: "rolcreaterole", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "rolcreatedb", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "rolcanlogin", Type: Type{Name: "bool"}, Ordinal: 6},
			{Name: "rolreplication", Type: Type{Name: "bool"}, Ordinal: 7},
			{Name: "rolbypassrls", Type: Type{Name: "bool"}, Ordinal: 8},
			{Name: "rolconnlimit", Type: Type{Name: "int4"}, Ordinal: 9},
			{Name: "rolpassword", Type: Type{Name: "text"}, Ordinal: 10},
			{Name: "rolvaliduntil", Type: Type{Name: "timestamptz"}, Ordinal: 11},
		},
		OID:     1260, // upstream's AuthIdRelationId
		Virtual: true,
	}
	pgAuthid.VirtualRows = func() [][]string {
		rowFor := func(oidStr, name string, a *RoleAttrs) []string {
			rolsuper, rolcanlogin := "f", "t"
			rolcreaterole, rolcreatedb, rolreplication, rolbypassrls := "f", "f", "f", "f"
			rolconnlimit := "-1"
			rolpassword := VirtualNull
			rolvaliduntil := VirtualNull
			if a != nil {
				if a.Superuser {
					rolsuper = "t"
				}
				if !a.CanLogin {
					rolcanlogin = "f"
				}
				if a.CreateRole {
					rolcreaterole = "t"
				}
				if a.CreateDB {
					rolcreatedb = "t"
				}
				if a.Replication {
					rolreplication = "t"
				}
				if a.BypassRLS {
					rolbypassrls = "t"
				}
				rolconnlimit = fmt.Sprintf("%d", a.ConnLimit)
				if a.CredType != 0 {
					rolpassword = a.Secret
				}
				if a.ValidUntil != "" {
					rolvaliduntil = a.ValidUntil
				}
			}
			return []string{
				oidStr, name, rolsuper,
				"t", // rolinherit: PG default, never overridden by goopg's role DDL
				rolcreaterole, rolcreatedb, rolcanlogin, rolreplication, rolbypassrls,
				rolconnlimit, rolpassword, rolvaliduntil,
			}
		}
		c.mu.RLock()
		defer c.mu.RUnlock()
		out := [][]string{rowFor("10", "postgres", c.roleAttrs["postgres"])}
		names := make([]string, 0, len(c.roles))
		for name := range c.roles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out = append(out, rowFor(fmt.Sprintf("%d", c.roles[name]), name, c.roleAttrs[name]))
		}
		// PG18's 16 built-in "pg_*" predefined roles (pg_authid.dat), same
		// gap/rationale as pg_roles above (0119-0004ch ledger discovery (a)):
		// this RoleAttrs{ConnLimit: -1} drives rowFor's defaults to exactly
		// PG's predefined-role shape (rolsuper/rolcanlogin='f', rolpassword
		// NULL, rolconnlimit=-1), matching SyncPgAuthidFile's frozen rows and
		// pg_authid.dat (every seeded role's rolconnlimit is -1, never 0).
		predefinedNames := make([]string, 0, len(predefinedRoleSeeds))
		for _, s := range predefinedRoleSeeds {
			predefinedNames = append(predefinedNames, s.name)
		}
		sort.Strings(predefinedNames)
		for _, name := range predefinedNames {
			out = append(out, rowFor(fmt.Sprintf("%d", c.predefinedRoles[name]), name, &RoleAttrs{ConnLimit: -1}))
		}
		return out
	}
	c.tables["pg_catalog.pg_authid"] = pgAuthid

	// pg_auth_members — role membership (`GRANT <role> TO <role>`), sourced
	// from the roleMembers registry GRANT/REVOKE ROLE maintains
	// (RoleMembershipEntries), including the per-grant WITH INHERIT/SET
	// option values a real GRANT requested (GrantRoleMembership). Registered
	// so pg_dumpall's dumpRoleMembership query resolves instead of failing
	// with "relation does not exist" (M0119-0004-ACLHEAP follow-up).
	pgAuthMembers := &Table{
		Schema: "pg_catalog",
		Name:   "pg_auth_members",
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "roleid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "member", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "grantor", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "admin_option", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "inherit_option", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "set_option", Type: Type{Name: "bool"}, Ordinal: 6},
		},
		OID:     1261, // upstream's AuthMemRelationId
		Virtual: true,
	}
	pgAuthMembers.VirtualRows = func() [][]string {
		entries := c.RoleMembershipEntries()
		if len(entries) == 0 {
			return nil
		}
		tf := func(b bool) string {
			if b {
				return "t"
			}
			return "f"
		}
		out := make([][]string, 0, len(entries))
		for _, m := range entries {
			out = append(out, []string{
				strconv.FormatUint(uint64(m.OID), 10),
				strconv.FormatUint(uint64(m.RoleOID), 10),
				strconv.FormatUint(uint64(m.MemberOID), 10),
				strconv.FormatUint(uint64(m.GrantorOID), 10),
				tf(m.AdminOption),
				tf(m.InheritOption),
				tf(m.SetOption),
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_auth_members"] = pgAuthMembers

	// pg_parameter_acl — GUC-level ACLs (`GRANT SET|ALTER SYSTEM ON PARAMETER
	// ...`). Registered so pg_dumpall's getParameterACLs query resolves
	// instead of failing with "relation does not exist"; rows are projected
	// from the parameterACLOIDs/parameterACLNames registry, populated lazily
	// by execParameterACLChange on the first GRANT ON PARAMETER for a given
	// GUC name (mirrors PostgreSQL's own lazy ParameterAclCreate — a GUC never
	// appears here until it has been granted at least once).
	// M0119-0004-ACLHEAP (parameter ACL half).
	pgParameterACL := &Table{
		Schema: "pg_catalog",
		Name:   "pg_parameter_acl",
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "parname", Type: Type{Name: "text"}, Ordinal: 1},
			{Name: "paracl", Type: Type{Name: "aclitem[]"}, Ordinal: 2},
		},
		OID:     6243, // upstream's ParameterAclRelationId
		Virtual: true,
	}
	pgParameterACL.VirtualRows = func() [][]string {
		entries := c.ParameterACLEntries()
		if len(entries) == 0 {
			return nil
		}
		out := make([][]string, 0, len(entries))
		for _, e := range entries {
			paracl := VirtualNull
			if aclText := c.ParameterACLText(e.OID); aclText != "" {
				paracl = aclText
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(e.OID), 10),
				e.Parname,
				paracl,
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_parameter_acl"] = pgParameterACL

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
	// Static fallback rows in upstream slru_names[] order ("other" last). The
	// executor serves live, snapshot-aware rows for pg_stat_slru (notify
	// blks_zeroed) via fetchSLRURows; these all-zero rows only back non-executor
	// readers (e.g. pg_dump) and must use the PG 17+ pg_stat_slru names (the
	// SimpleLruInit name, not the on-disk directory). M0118-0009.
	pgStatSlru.VirtualRows = func() [][]string {
		reset := "2026-01-01 00:00:00+00"
		return [][]string{
			{"commit_timestamp", "0", "0", "0", "0", "0", "0", "0", reset},
			{"multixact_member", "0", "0", "0", "0", "0", "0", "0", reset},
			{"multixact_offset", "0", "0", "0", "0", "0", "0", "0", reset},
			{"notify", "0", "0", "0", "0", "0", "0", "0", reset},
			{"serializable", "0", "0", "0", "0", "0", "0", "0", reset},
			{"subtransaction", "0", "0", "0", "0", "0", "0", "0", reset},
			{"transaction", "0", "0", "0", "0", "0", "0", "0", reset},
			{"other", "0", "0", "0", "0", "0", "0", "0", reset},
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
	// The static VirtualRows fallback below returns no rows; the real,
	// live row set (upstream's exact 79-row valid-combination shape, with
	// goopg's one instrumented cell filled in) is built by
	// executor.fetchIOStatRows and swapped in at Open time — see
	// valuesOp.Open's "pg_stat_io" case (M0122-0003). This fallback only
	// fires for code paths that read VirtualRows() directly (bypassing the
	// executor), e.g. pg_dump's catalog introspection.
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
	// pg_attrdef holds ordinary column DEFAULTs, the generation expression of
	// GENERATED ALWAYS AS (expr) STORED columns (pg_dump re-emits it inline,
	// keyed on attgenerated='s' — DU-002 slice 59), and the implicit nextval()
	// default of a SERIAL column (slice 121). attrDefRowsLocked builds the
	// deterministic row set so this view and dependVirtualRows agree on the oids.
	pgAttrdef.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		ar := c.attrDefRowsLocked()
		rows := make([][]string, 0, len(ar))
		for _, r := range ar {
			rows = append(rows, []string{
				strconv.FormatUint(uint64(r.oid), 10),
				strconv.FormatUint(uint64(r.tableOID), 10),
				strconv.Itoa(r.attnum),
				r.adbin,
			})
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
				if nc.NotValid || nc.NotEnforced {
					// convalidated='f' — either explicitly added NOT VALID, or
					// NOT ENFORCED (real PG's ATAddCheckNNConstraint sets
					// skip_validation=!is_enforced, so an unenforced constraint
					// is implicitly unvalidated too). DU-002 slice 430.
					row[6] = "f" // convalidated
				} else {
					row[6] = "t" // convalidated
				}
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
				if nc.NoInherit {
					row[17] = "t" // connoinherit (CHECK ... NO INHERIT). DU-002 slice 128.
				} else {
					row[17] = "f"
				}
				row[18] = "f"     // conperiod
				row[24] = nc.Expr // conbin
				if nc.NotEnforced {
					row[25] = "f" // conenforced — CHECK ... NOT ENFORCED. DU-002 slice 430.
				} else {
					row[25] = "t" // conenforced
				}
				out = append(out, row)
			}
		}
		// Emit domain CHECK constraints (contype='c', keyed on contypid = the
		// domain's pg_type OID rather than conrelid). pg_dump's
		// getDomainConstraints reads `WHERE contypid = $1 AND contype IN ('c','n')`
		// and renders each via pg_get_constraintdef ORDER BY conname — the ORDER BY
		// is applied by the executor over these rows, so a domain's multiple CHECKs
		// each get a row here. DU-002 slice 96 (single) / slice 385 (multi).
		for _, d := range c.domains {
			for _, ck := range d.Checks {
				if ck.Expr == "" || ck.OID == 0 {
					continue
				}
				row := make([]string, 26)
				row[0] = fmt.Sprintf("%d", ck.OID) // oid
				row[1] = ck.Name                   // conname
				row[2] = "2200"                    // connamespace (public)
				row[3] = "c"                       // contype = check
				row[4] = "f"                       // condeferrable
				row[5] = "f"                       // condeferred
				row[6] = "t"                       // convalidated
				row[7] = "0"                       // conrelid (none — domain check)
				row[8] = fmt.Sprintf("%d", d.OID)  // contypid = domain OID
				row[9] = "0"                       // conindid
				row[10] = "0"                      // conparentid
				row[11] = "0"                      // confrelid
				row[12] = " "                      // confupdtype
				row[13] = " "                      // confdeltype
				row[14] = " "                      // confmatchtype
				row[15] = "t"                      // conislocal
				row[16] = "0"                      // coninhcount
				row[17] = "f"                      // connoinherit
				row[18] = "f"                      // conperiod
				row[24] = ck.Expr                  // conbin
				row[25] = "t"                      // conenforced: always true in v0
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
			// condeferrable / condeferred: a DEFERRABLE [INITIALLY DEFERRED]
			// UNIQUE/PK constraint round-trips the flags so pg_dump re-emits the
			// clause. DU-002 slice 139.
			row[4] = "f"
			if idx.Deferrable {
				row[4] = "t"
			}
			row[5] = "f"
			if idx.InitiallyDeferred {
				row[5] = "t"
			}
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
		// Emit FOREIGN KEY constraints (contype='f', PG). pg_dump's getConstraints
		// joins `pg_constraint c ON src.tbloid = c.conrelid WHERE contype='f'` and
		// renders each via pg_get_constraintdef(c.oid). DU-002 slice 51.
		for _, tbl := range c.tables {
			if tbl.Virtual || tbl.OID == 0 {
				continue
			}
			colOrd := make(map[string]int, len(tbl.Columns))
			for i, col := range tbl.Columns {
				colOrd[strings.ToLower(col.Name)] = i + 1
			}
			for _, fk := range tbl.ForeignKeys {
				if fk.Name == "" || fk.OID == 0 {
					continue
				}
				// Resolve the referenced table OID + referenced column ordinals.
				var refTbl *Table
				for _, cand := range c.tables {
					if cand.Virtual || cand.OID == 0 {
						continue
					}
					if strings.EqualFold(cand.Name, fk.RefTable) {
						refTbl = cand
						break
					}
				}
				var confrelid uint32
				refColOrd := map[string]int{}
				if refTbl != nil {
					confrelid = refTbl.OID
					for i, col := range refTbl.Columns {
						refColOrd[strings.ToLower(col.Name)] = i + 1
					}
				}
				var conkey, confkey []string
				for _, cn := range fk.Columns {
					if ord, ok := colOrd[strings.ToLower(cn)]; ok {
						conkey = append(conkey, fmt.Sprintf("%d", ord))
					}
				}
				for _, cn := range fk.RefColumns {
					if ord, ok := refColOrd[strings.ToLower(cn)]; ok {
						confkey = append(confkey, fmt.Sprintf("%d", ord))
					}
				}
				// confdelsetcols (PG15): attnums of the columns an ON DELETE SET
				// NULL|DEFAULT is restricted to. NULL when the action covers the
				// whole key, matching PG (decompile_column_index_array emits the
				// ` (col, …)` suffix only when this is non-null). DU-002 slice 311.
				var confdelsetcols []string
				for _, cn := range fk.OnDeleteSetCols {
					if ord, ok := colOrd[strings.ToLower(cn)]; ok {
						confdelsetcols = append(confdelsetcols, fmt.Sprintf("%d", ord))
					}
				}
				row := make([]string, 26)
				row[0] = fmt.Sprintf("%d", fk.OID) // oid
				row[1] = fk.Name                   // conname
				row[2] = "2200"                    // connamespace (public)
				row[3] = "f"                       // contype = foreign key
				if fk.Deferrable {
					row[4] = "t"
				} else {
					row[4] = "f" // condeferrable
				}
				if fk.InitiallyDeferred {
					row[5] = "t"
				} else {
					row[5] = "f" // condeferred
				}
				if fk.NotValid || fk.NotEnforced {
					row[6] = "f" // convalidated — NOT VALID (or implied by NOT ENFORCED)
				} else {
					row[6] = "t" // convalidated
				}
				row[7] = fmt.Sprintf("%d", tbl.OID) // conrelid
				row[8] = "0"                        // contypid
				row[9] = "0"                        // conindid (unique idx on ref tbl; unused by deparse)
				row[10] = "0"                       // conparentid (0 → pg_dump WHERE conparentid=0 keeps it)
				row[11] = fmt.Sprintf("%d", confrelid)
				row[12] = string(fkActionChar(fk.OnUpdate)) // confupdtype
				row[13] = string(fkActionChar(fk.OnDelete)) // confdeltype
				if fk.MatchFull {
					row[14] = "f" // confmatchtype = MATCH FULL
				} else {
					row[14] = "s" // confmatchtype = MATCH SIMPLE (default)
				}
				row[15] = "t"                               // conislocal
				row[16] = "0"                               // coninhcount
				row[17] = "f"                               // connoinherit
				row[18] = "f"                               // conperiod
				row[19] = "{" + strings.Join(conkey, ",") + "}"
				row[20] = "{" + strings.Join(confkey, ",") + "}"
				if len(confdelsetcols) > 0 {
					row[23] = "{" + strings.Join(confdelsetcols, ",") + "}" // confdelsetcols
				}
				if fk.NotEnforced {
					row[25] = "f" // conenforced — FOREIGN KEY ... NOT ENFORCED. DU-002 slice 431.
				} else {
					row[25] = "t" // conenforced
				}
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
			if tbl.Virtual {
				continue
			}
			// Partition children: one row per child → its partition parent.
			if tbl.PartitionParentOID != 0 {
				parentSeq[tbl.PartitionParentOID]++
				seq := parentSeq[tbl.PartitionParentOID]
				out = append(out, []string{
					fmt.Sprintf("%d", tbl.OID),
					fmt.Sprintf("%d", tbl.PartitionParentOID),
					fmt.Sprintf("%d", seq),
					"f",
				})
				continue
			}
			// Legacy inheritance children: one row per (child, parent) pair in
			// declaration order, so inhseqno matches the INHERITS (...) list and
			// pg_dump re-emits the clause. DU-002 slice 170.
			for i, parentOID := range tbl.InheritsParentOIDs {
				out = append(out, []string{
					fmt.Sprintf("%d", tbl.OID),
					fmt.Sprintf("%d", parentOID),
					fmt.Sprintf("%d", i+1),
					"f",
				})
			}
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
				boolStr(idx.NullsNotDistinct),    // indnullsnotdistinct
				boolStr(idx.Primary),             // indisprimary
				boolStr(idx.IsExclusion),         // indisexclusion
				"t",                              // indimmediate
				boolStr(idx.IsClustered),         // indisclustered (DU-002 slice 320)
				"t",                              // indisvalid
				"f",                              // indcheckxmin
				"t",                              // indisready
				"t",                              // indislive
				boolStr(idx.IsReplicaIdentity),   // indisreplident (DU-002 slice 306)
				indkey,                           // indkey
				buildZeroVec(nkeyatts),           // indcollation
				indclass,                         // indclass
				buildZeroVec(nkeyatts),           // indoption
				"",                               // indexprs (NULL)
				"",                               // indpred (NULL)
				"",                               // indcoloptions (NULL)
			})
		}
		// Synthesize the unique btree index PG auto-creates on every TOAST
		// relation (on chunk_id, chunk_seq). The pg_class virtual builder emits
		// the matching relkind='i' row named pg_toast_<oid>_index; this row lets
		//   SELECT indexrelid::regclass FROM pg_index
		//     WHERE indrelid = (SELECT oid FROM pg_class WHERE relname=<toast rel>)
		// resolve (reindex-concurrently-toast setup). toastBearingTables uses the
		// SAME gate as the pg_class TOAST-row emission so the two never diverge.
		// M0118-0008 TOAST-exposure slice 3.
		for _, t := range c.toastBearingTables() {
			toastRelOID := int(t.OID) + toastRelidOffset
			toastIdxOID := int(t.OID) + toastIndexOidOffset
			out = append(out, []string{
				fmt.Sprintf("%d", toastIdxOID), // indexrelid
				fmt.Sprintf("%d", toastRelOID), // indrelid
				"2",                            // indnatts (chunk_id, chunk_seq)
				"2",                            // indnkeyatts
				"t",                            // indisunique
				"f",                            // indnullsnotdistinct
				"f",                            // indisprimary
				"f",                            // indisexclusion
				"t",                            // indimmediate
				"f",                            // indisclustered
				"t",                            // indisvalid
				"f",                            // indcheckxmin
				"t",                            // indisready
				"t",                            // indislive
				"f",                            // indisreplident
				"1 2",                          // indkey (chunk_id=1, chunk_seq=2)
				"0 0",                          // indcollation (int4 columns: no collation)
				"1978 1978",                    // indclass (int4_ops btree)
				"0 0",                          // indoption
				"",                             // indexprs (NULL)
				"",                             // indpred (NULL)
				"",                             // indcoloptions (NULL)
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
			nsOID := c.schemas[strings.ToLower(schema)]
			if nsOID == 0 {
				nsOID = c.schemas["public"]
			}
			row := make([]string, 9)
			row[0] = fmt.Sprintf("%d", obj.OID)              // oid
			row[1] = fmt.Sprintf("%d", obj.TableOID)         // stxrelid
			row[2] = obj.Name                                // stxname
			row[3] = fmt.Sprintf("%d", nsOID)                // stxnamespace
			row[4] = fmt.Sprintf("%d", obj.OwnerOrDefault()) // stxowner
			// stxstattarget: PG18 stores NULL for the default (BKI_FORCE_NULL),
			// which pg_dump's getExtendedStatistics maps to -1 → no ALTER. The
			// string-based virtual-row machinery has no int NULL sentinel (an
			// empty int4 cell parses as 0, which would spuriously dump
			// `SET STATISTICS 0`), so the unset default is projected as the
			// pg_dump-equivalent -1. A non-nil StatTarget (from ALTER STATISTICS
			// ... SET STATISTICS n) projects its value so pg_dump re-emits the
			// `ALTER STATISTICS ... SET STATISTICS <n>`. DU-002 slice 317.
			if obj.StatTarget != nil {
				row[5] = fmt.Sprintf("%d", *obj.StatTarget) // stxstattarget
			} else {
				row[5] = "-1" // default (pg_dump-equivalent of NULL → no ALTER)
			}
			row[6] = "" // stxkeys
			row[7] = "" // stxexprs
			row[8] = "" // stxkind
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
	// Populate the 7 built-in collations from PG18's pg_collation.dat so
	// catalog queries (`SELECT * FROM pg_collation`, psql \dO, collation-OID
	// joins) resolve names instead of seeing an empty relation. These mirror
	// initdb's bootstrapPgCollationTuples seed (the on-disk heap a PG standby
	// reads); the catalog package cannot import initdb (cycle), so the rows
	// are duplicated here as the source of truth for goopg's own SQL queries.
	// All are BKI-pinned (OID < 16384, nsp=pg_catalog=11, owner=10), so pg_dump
	// skips them. collcollate/collctype carry libc-locale rows; colllocale
	// carries builtin/ICU rows; unset fields are NULL (""). collisdeterministic
	// is true for every BKI row; collicurules is NULL for all. DU-002 slice 187.
	pgCollation.VirtualRows = func() [][]string {
		// cols: oid, collname, collnamespace, collowner, collprovider,
		//       collisdeterministic, collencoding, collcollate, collctype,
		//       colllocale, collicurules, collversion
		rows := [][]string{
			{"100", "default", "11", "10", "d", "t", "-1", "", "", "", "", ""},
			{"950", "C", "11", "10", "c", "t", "-1", "C", "C", "", "", ""},
			{"951", "POSIX", "11", "10", "c", "t", "-1", "POSIX", "POSIX", "", "", ""},
			{"962", "ucs_basic", "11", "10", "b", "t", "6", "", "", "C", "", "1"},
			{"963", "unicode", "11", "10", "i", "t", "-1", "", "", "und", "", ""},
			{"811", "pg_c_utf8", "11", "10", "b", "t", "6", "", "", "C.UTF-8", "", "1"},
			{"6411", "pg_unicode_fast", "11", "10", "b", "t", "6", "", "", "PG_UNICODE_FAST", "", "1"},
		}
		// Append user collations (CREATE COLLATION). These carry a user
		// namespace (e.g. public=2200) so pg_dump's getCollations selects them
		// for dump while the BKI-pinned pg_catalog rows above are skipped.
		// M0119-0004.
		c.mu.RLock()
		defer c.mu.RUnlock()
		for _, uc := range c.userCollations {
			det := "t"
			if !uc.Deterministic {
				det = "f"
			}
			// Per-provider NULLs: libc carries collcollate/collctype and leaves
			// colllocale NULL; builtin/icu carry colllocale and leave
			// collcollate/collctype NULL (matching pg_collation, and what
			// dumpCollation's per-provider branches expect). An empty text cell
			// must surface SQL NULL — not '' — or dumpCollation's ICU branch
			// emits a spurious `rules = ''` and warns "invalid collation". The
			// VirtualNull sentinel forces a NULL through TypedVirtualCell.
			nz := func(s string) string {
				if s == "" {
					return VirtualNull
				}
				return s
			}
			rows = append(rows, []string{
				strconv.FormatUint(uint64(uc.OID), 10),
				uc.Name,
				strconv.FormatUint(uint64(uc.NamespaceOID), 10),
				strconv.FormatUint(uint64(uc.Owner), 10),
				string(uc.Provider),
				det,
				strconv.Itoa(uc.Encoding),
				nz(uc.Collate),
				nz(uc.Ctype),
				nz(uc.Locale),
				nz(uc.Rules), // collicurules: ICU tailoring rules; "" → NULL
				VirtualNull,  // collversion: NULL → recomputed on restore
			})
		}
		return rows
	}
	c.tables["pg_catalog.pg_collation"] = pgCollation

	// pg_policy — stores row-level security policies (OID 3256). goopg does NOT
	// enforce RLS, but a policy created via CREATE POLICY is recorded on its
	// table (catalog.Table.Policies) and projected here so pg_dump's getPolicies
	// reads it and dumpPolicy re-emits the CREATE POLICY (DU-002 slice 323).
	// polqual / polwithcheck are pg_node_tree in real PG; declaring them so here
	// gives the correct NULL semantics for a policy with no USING / WITH CHECK
	// (an empty cell decodes to SQL NULL — see planner.TypedVirtualCell — and
	// pg_get_expr(NULL,…) returns NULL, so dumpPolicy omits the clause).
	pgPolicy := &Table{
		Schema: "pg_catalog", Name: "pg_policy", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "polname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "polrelid", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "polcmd", Type: Type{Name: "char"}, Ordinal: 3},
			{Name: "polpermissive", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "polroles", Type: Type{Name: "oid[]"}, Ordinal: 5},
			{Name: "polqual", Type: Type{Name: "pg_node_tree"}, Ordinal: 6},
			{Name: "polwithcheck", Type: Type{Name: "pg_node_tree"}, Ordinal: 7},
		},
		OID: 3256,
	}
	pgPolicy.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		var out [][]string
		for _, tbl := range c.tables {
			if tbl == nil || tbl.Virtual || tbl.OID == 0 || len(tbl.Policies) == 0 {
				continue
			}
			for _, pol := range tbl.Policies {
				if pol.OID == 0 {
					continue // predates OID tracking → invisible to pg_dump
				}
				// polroles: format the OID array as a PostgreSQL array literal.
				// {0} is the PUBLIC sentinel; getPolicies maps it to a NULL TO
				// clause via `CASE WHEN polroles = '{0}' THEN NULL …`.
				roles := pol.Roles
				if len(roles) == 0 {
					roles = []uint32{0}
				}
				parts := make([]string, len(roles))
				for i, r := range roles {
					parts[i] = fmt.Sprintf("%d", r)
				}
				polroles := "{" + strings.Join(parts, ",") + "}"
				// polqual / polwithcheck: render the parsed expression with the
				// catalog-side pg_get_expr deparser, which fully parenthesizes
				// every node so pg_dump emits `USING ((expr))` / `WITH CHECK
				// ((expr))`. An absent expression stays "" (→ SQL NULL → pg_dump
				// omits the clause).
				var polqual, polwithcheck string
				if pol.Using != nil {
					polqual = formatExprForAttrdef(pol.Using)
				}
				if pol.WithCheck != nil {
					polwithcheck = formatExprForAttrdef(pol.WithCheck)
				}
				cmd := pol.Command
				if cmd == 0 {
					cmd = '*'
				}
				permissive := "t"
				if !pol.Permissive {
					permissive = "f"
				}
				out = append(out, []string{
					fmt.Sprintf("%d", pol.OID), // oid
					pol.Name,                   // polname
					fmt.Sprintf("%d", tbl.OID), // polrelid
					string(cmd),                // polcmd
					permissive,                 // polpermissive
					polroles,                   // polroles
					polqual,                    // polqual
					polwithcheck,               // polwithcheck
				})
			}
		}
		return out
	}
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
		rows := [][]string{
			{"2", "heap", "3", "t"},
			{"403", "btree", "330", "i"},
			{"405", "hash", "331", "i"},
			{"783", "gist", "332", "i"},
			{"2742", "gin", "333", "i"},
			{"4000", "spgist", "334", "i"},
			{"3580", "brin", "335", "i"},
		}
		// Surface user-created access methods (CREATE ACCESS METHOD) so they
		// round-trip through pg_dump's getAccessMethods (only oid >=
		// FirstNormalObjectId rows are dumpable — the 7 built-ins above are
		// filtered out there, not here). DU-002 (M0119-0004).
		for _, am := range c.ListAccessMethods() {
			rows = append(rows, []string{
				strconv.FormatUint(uint64(am.OID), 10),
				am.Name,
				strconv.FormatUint(uint64(am.HandlerOID), 10),
				am.AMType,
			})
		}
		return rows
	}
	c.tables["pg_catalog.pg_am"] = pgAm

	// pg_depend — dependency catalog (OID 2608).
	// goopg does not maintain a general dependency graph (extension membership,
	// function/type deps, etc. are not tracked), so this view is empty EXCEPT for
	// the one dependency class pg_dump needs: a sequence's AUTO ('a') ownership of
	// the column named by OWNED BY. pg_dump LEFT JOINs pg_depend in getTables
	// (gated on relkind=RELKIND_SEQUENCE, objsubid=0, refclassid=pg_class,
	// deptype IN ('a','i')) to discover owning_tab/owning_col, then dumpSequence
	// emits `ALTER SEQUENCE ... OWNED BY <table>.<col>`. A standalone sequence has
	// no such row and correctly yields NULL owning_tab. The schema matches PG's
	// pg_depend exactly so the catalog-query column references (classid, objid,
	// objsubid, refclassid, refobjid, refobjsubid, deptype) resolve. M0110-0001
	// (DU-002 slice 118).
	pgDepend := &Table{
		Schema: "pg_catalog", Name: "pg_depend", Virtual: true,
		Columns: []Column{
			{Name: "classid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "objid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "objsubid", Type: Type{Name: "int4"}, Ordinal: 2},
			{Name: "refclassid", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "refobjid", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "refobjsubid", Type: Type{Name: "int4"}, Ordinal: 5},
			{Name: "deptype", Type: Type{Name: "char"}, Ordinal: 6},
		},
		OID: 2608,
	}
	pgDepend.VirtualRows = c.dependVirtualRows
	c.tables["pg_catalog.pg_depend"] = pgDepend

	// pg_tablespace — tablespace catalog (OID 1213). The on-disk shared heap
	// (initialized by initdb with pg_default/pg_global) is not wired into the SQL
	// query layer, so expose a virtual view: the two bootstrap tablespaces plus
	// any in-place tablespaces in the runtime registry (CREATE TABLESPACE,
	// M0095-0003). pg_dump's getTables LEFT JOINs it for spcname; \db and other
	// clients read it directly. M0110-0001 (DU-002). Schema matches PG's
	// pg_tablespace (oid, spcname, spcowner, spcacl, spcoptions).
	pgTablespace := &Table{
		Schema: "pg_catalog", Name: "pg_tablespace", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "spcname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "spcowner", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "spcacl", Type: Type{Name: "aclitem[]"}, Ordinal: 3},
			{Name: "spcoptions", Type: Type{Name: "text[]"}, Ordinal: 4},
		},
		OID: 1213,
	}
	pgTablespace.VirtualRows = c.tablespaceVirtualRows
	c.tables["pg_catalog.pg_tablespace"] = pgTablespace

	// pg_foreign_table — foreign-table catalog (OID 3118). pg_dump's getTables
	// runs a `SELECT ftserver FROM pg_foreign_table WHERE ftrelid = c.oid`
	// subquery in the relkind='f' branch. Schema matches PG's pg_foreign_table
	// (ftrelid, ftserver, ftoptions). M0110-0001 (DU-002); populated by
	// CREATE FOREIGN TABLE (DU-002 slice 417).
	pgForeignTable := &Table{
		Schema: "pg_catalog", Name: "pg_foreign_table", Virtual: true,
		Columns: []Column{
			{Name: "ftrelid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "ftserver", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "ftoptions", Type: Type{Name: "text[]"}, Ordinal: 2},
		},
		OID: 3118,
	}
	// Surface user-created foreign tables (CREATE FOREIGN TABLE ... SERVER ...)
	// so they round-trip. ftserver resolves to the referenced server's stable
	// OID (dumpTableSchema/getTables recover the server name via
	// `SELECT srvname FROM pg_foreign_server WHERE oid = ftserver`); ftoptions
	// is NULL (empty string) when no table-level OPTIONS were given, so
	// dumpTableSchema omits the OPTIONS clause. DU-002 slice 417.
	pgForeignTable.VirtualRows = func() [][]string {
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
			if t.ForeignServerName == "" {
				continue
			}
			var srvOID uint32
			if s, ok := c.foreignServers[t.ForeignServerName]; ok {
				srvOID = s.OID
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(t.OID), 10),  // ftrelid
				strconv.FormatUint(uint64(srvOID), 10), // ftserver
				optionsArrayLiteral(t.ForeignOptions),  // ftoptions
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_foreign_table"] = pgForeignTable

	// pg_init_privs — initial-privileges catalog (OID 3394). PG records here the
	// privileges an object had immediately after initdb (privtype 'i') or after
	// an extension installed it (privtype 'e'); pg_dump diffs the object's current
	// *acl against this to dump only the privilege changes a user made. goopg
	// installs no extensions and does not snapshot initdb-time ACLs, so this view
	// is empty by construction. pg_dump's getTables/getFuncs/getTypes/… LEFT JOIN
	// `pg_init_privs pip ON (c.oid=pip.objoid AND pip.classoid='<catalog>'::regclass
	// AND pip.objsubid=0)`; with no rows the join yields NULL pip.initprivs, so the
	// `relacl IS DISTINCT FROM pip.initprivs` predicate degenerates to "dump the
	// full ACL", which is correct for a server that tracks no initial privileges.
	// Schema matches PG's pg_init_privs (objoid, classoid, objsubid, privtype,
	// initprivs); like the upstream catalog it has NO oid system column.
	// M0110-0001 (DU-002).
	pgInitPrivs := &Table{
		Schema: "pg_catalog", Name: "pg_init_privs", Virtual: true,
		Columns: []Column{
			{Name: "objoid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "classoid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "objsubid", Type: Type{Name: "int4"}, Ordinal: 2},
			{Name: "privtype", Type: Type{Name: "char"}, Ordinal: 3},
			{Name: "initprivs", Type: Type{Name: "aclitem[]"}, Ordinal: 4},
		},
		OID: 3394,
	}
	pgInitPrivs.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_init_privs"] = pgInitPrivs

	// pg_cast — cast catalog (OID 2605). goopg registers no user-defined casts, so
	// this view is empty. pg_dump's getFuncs runs `EXISTS (SELECT 1 FROM pg_cast
	// WHERE pg_cast.oid > <g_last_builtin_oid> AND p.oid = pg_cast.castfunc)` to
	// pull in pg_catalog functions referenced by a user cast; with no rows the
	// subquery is always false, so only the genuine namespace/ACL predicates
	// select rows (correct — built-in casts are never dumped). Schema matches PG's
	// pg_cast (oid, castsource, casttarget, castfunc, castcontext, castmethod).
	// castfunc is typed oid (not regproc) so the `p.oid = pg_cast.castfunc`
	// comparison resolves with goopg's oid equality operator. M0110-0001 (DU-002).
	pgCast := &Table{
		Schema: "pg_catalog", Name: "pg_cast", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "castsource", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "casttarget", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "castfunc", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "castcontext", Type: Type{Name: "char"}, Ordinal: 4},
			{Name: "castmethod", Type: Type{Name: "char"}, Ordinal: 5},
		},
		OID: 2605,
	}
	// Surface user-defined casts (CREATE CAST) so they round-trip. getCasts reads
	// every row (built-in casts are excluded by OID at dump-out time, so an empty
	// view stays correct for the no-user-cast case); dumpCast renders the source/
	// target via getFormattedTypeName(castsource/casttarget) — hence the type names
	// resolve to their canonical pg_type OIDs, which goopg already surfaces in
	// pg_type. castfunc=0 (WITHOUT FUNCTION / WITH INOUT) skips findFuncByOid; a
	// WITH FUNCTION cast carries the resolved pg_proc OID so dumpCast re-emits the
	// `WITH FUNCTION <ns>.<signature>` clause. DU-002 slices 395, 397.
	pgCast.VirtualRows = func() [][]string {
		casts := c.ListCasts()
		if len(casts) == 0 {
			return nil
		}
		out := make([][]string, 0, len(casts))
		for _, cs := range casts {
			out = append(out, []string{
				strconv.FormatUint(uint64(cs.OID), 10),                // oid
				strconv.FormatUint(uint64(TypeNameToOID(cs.SourceType)), 10), // castsource
				strconv.FormatUint(uint64(TypeNameToOID(cs.TargetType)), 10), // casttarget
				strconv.FormatUint(uint64(cs.FuncOID), 10),            // castfunc (0 unless WITH FUNCTION)
				cs.Context,                                            // castcontext
				cs.Method,                                             // castmethod
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_cast"] = pgCast

	// pg_transform — transform catalog (OID 3576). Schema matches PG's
	// pg_transform (oid, trftype, trflang, trffromsql, trftosql); trffromsql/
	// trftosql are typed oid (PG uses regproc, which is oid-compatible). A
	// CREATE TRANSFORM registers a row here (catalog.Transform, ListTransforms)
	// so pg_dump's getTransforms/dumpTransform re-emit it; with none registered
	// this view is empty exactly as before. M0110-0001 (DU-002); CREATE
	// TRANSFORM support added M0119-0004.
	pgTransform := &Table{
		Schema: "pg_catalog", Name: "pg_transform", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "trftype", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "trflang", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "trffromsql", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "trftosql", Type: Type{Name: "oid"}, Ordinal: 4},
		},
		OID: 3576,
	}
	pgTransform.VirtualRows = func() [][]string {
		transforms := c.ListTransforms()
		if len(transforms) == 0 {
			return nil
		}
		out := make([][]string, 0, len(transforms))
		for _, tf := range transforms {
			out = append(out, []string{
				strconv.FormatUint(uint64(tf.OID), 10),                    // oid
				strconv.FormatUint(uint64(TypeNameToOID(tf.TypeName)), 10), // trftype
				strconv.FormatUint(uint64(LanguageNameToOID(tf.Lang)), 10), // trflang
				strconv.FormatUint(uint64(tf.FromFuncOID), 10),            // trffromsql (0 unless resolved)
				strconv.FormatUint(uint64(tf.ToFuncOID), 10),              // trftosql (0 unless resolved)
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_transform"] = pgTransform

	// pg_language — procedural-language catalog (OID 2612). pg_dump's getProcLangs
	// runs `SELECT tableoid, oid, lanname, lanpltrusted, lanplcallfoid, laninline,
	// lanvalidator, lanacl, acldefault('l', lanowner) AS acldefault, lanowner FROM
	// pg_language WHERE lanispl ORDER BY oid`. The `WHERE lanispl` predicate selects
	// only user-installed procedural languages — the built-in internal/c/sql langs
	// have lanispl=false and are never dumped. goopg installs no user PLs, so
	// getProcLangs still finds nothing. BUT dumpFunc joins pg_proc to pg_language
	// WITHOUT a lanispl filter (`WHERE p.oid=$1 AND l.oid=p.prolang`) purely to
	// fetch lanname for the function's prolang; with 0 rows that join returns
	// "0 rows instead of one" and aborts the dump. So this view is populated with
	// the 3 built-in BKI rows (internal/c/sql, OIDs 12/13/14) matching initdb's
	// pgLanguageInitialEntries() PLUS a plpgsql row (OID 13627, DU-002 slice 163)
	// so a `LANGUAGE plpgsql` function's prolang resolves to lanname='plpgsql'.
	// All four rows have lanispl=false so getProcLangs's `WHERE lanispl` returns 0:
	// real PG marks plpgsql lanispl=true but skips dumping CREATE LANGUAGE for it
	// because it is pinned via pg_depend/extension membership; goopg has neither
	// pin nor extension machinery, so setting lanispl=false reproduces the same
	// net dump output (no spurious CREATE LANGUAGE) while still letting dumpFunc's
	// unfiltered join resolve the language name. Schema matches PG's pg_language (oid,
	// lanname name, lanowner oid, lanispl bool, lanpltrusted bool, lanplcallfoid oid,
	// laninline oid, lanvalidator oid, lanacl aclitem[]); lanowner is typed oid so
	// `acldefault('l', lanowner)` resolves. lanvalidator=0 (no validators) and
	// lanacl=NULL (default privileges) for all rows. M0110-0001 (DU-002 slice 42).
	pgLanguage := &Table{
		Schema: "pg_catalog", Name: "pg_language", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "lanname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "lanowner", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "lanispl", Type: Type{Name: "bool"}, Ordinal: 3},
			{Name: "lanpltrusted", Type: Type{Name: "bool"}, Ordinal: 4},
			{Name: "lanplcallfoid", Type: Type{Name: "oid"}, Ordinal: 5},
			{Name: "laninline", Type: Type{Name: "oid"}, Ordinal: 6},
			{Name: "lanvalidator", Type: Type{Name: "oid"}, Ordinal: 7},
			{Name: "lanacl", Type: Type{Name: "aclitem[]"}, Ordinal: 8},
		},
		OID: 2612,
	}
	// 3 built-in languages from postgres/src/include/catalog/pg_language.dat
	// (oid, lanname, lanowner=10, lanispl=f, lanpltrusted, lanplcallfoid=0,
	// laninline, lanvalidator=0, lanacl=NULL). sql is trusted with laninline=2511.
	// plpgsql (OID 13627, matching a stock PG 18.3 initdb) is appended so
	// `LANGUAGE plpgsql` functions round-trip through pg_dump; it is trusted and,
	// per the comment above, kept lanispl=f / handler-OIDs=0 (dumpFunc only reads
	// lanname). DU-002 slice 163.
	pgLanguage.VirtualRows = func() [][]string {
		return [][]string{
			{"12", "internal", "10", "f", "f", "0", "0", "0", ""},
			{"13", "c", "10", "f", "f", "0", "0", "0", ""},
			{"14", "sql", "10", "f", "t", "0", "2511", "0", ""},
			{"13627", "plpgsql", "10", "f", "t", "0", "0", "0", ""},
		}
	}
	c.tables["pg_catalog.pg_language"] = pgLanguage

	// pg_operator — operator catalog (OID 2617). pg_dump's getOperators runs
	// `SELECT tableoid, oid, oprname, oprnamespace, oprowner, oprkind, oprleft,
	// oprright, oprcode::oid AS oprcode FROM pg_operator` — it reads ALL operators
	// (built-ins included) and filters out system-defined ones at dump-out time by
	// namespace dumpability. The built-ins are in pg_catalog (never dumped), so
	// they are correctly never rendered here (matching PG's own dump behavior,
	// not a goopg gap). A CREATE OPERATOR registers a row here
	// (catalog.UserOperator, ListUserOperators) so pg_dump's
	// getOperators/dumpOpr re-emit it; with none registered this view is empty
	// exactly as before. Schema matches PG's pg_operator (pg_operator.h):
	// oprcode is regproc in PG but oid-compatible, so it is typed oid here and
	// `oprcode::oid` resolves as a no-op. M0110-0001 (DU-002 slice 9; CREATE
	// OPERATOR support added M0119-0004).
	pgOperator := &Table{
		Schema: "pg_catalog", Name: "pg_operator", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "oprname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "oprnamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "oprowner", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "oprkind", Type: Type{Name: "char"}, Ordinal: 4},
			{Name: "oprcanmerge", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "oprcanhash", Type: Type{Name: "bool"}, Ordinal: 6},
			{Name: "oprleft", Type: Type{Name: "oid"}, Ordinal: 7},
			{Name: "oprright", Type: Type{Name: "oid"}, Ordinal: 8},
			{Name: "oprresult", Type: Type{Name: "oid"}, Ordinal: 9},
			{Name: "oprcom", Type: Type{Name: "oid"}, Ordinal: 10},
			{Name: "oprnegate", Type: Type{Name: "oid"}, Ordinal: 11},
			{Name: "oprcode", Type: Type{Name: "oid"}, Ordinal: 12},
			{Name: "oprrest", Type: Type{Name: "oid"}, Ordinal: 13},
			{Name: "oprjoin", Type: Type{Name: "oid"}, Ordinal: 14},
		},
		OID: 2617,
	}
	pgOperator.VirtualRows = func() [][]string {
		ops := c.ListUserOperators()
		if len(ops) == 0 {
			return nil
		}
		out := make([][]string, 0, len(ops))
		for _, op := range ops {
			// A shell operator (FuncOID==0) forward-declared purely to mint an
			// OID for another operator's COMMUTATOR/NEGATOR clause is not yet
			// a complete definition. Real PG's dumpOpr explicitly skips these
			// ("some operators are invalid because they were the result of
			// user defining operators before commutators exist",
			// pg_dump.c) via `if (!OidIsValid(oprinfo->oprcode)) return;` —
			// mirrored here.
			if op.FuncOID == 0 {
				continue
			}
			oprkind := "b"
			if op.LeftType == "" {
				oprkind = "l" // prefix/unary: RIGHTARG present, LEFTARG absent (PG14+ has no postfix "r" form)
			}
			leftOID := uint32(0)
			if op.LeftType != "" {
				leftOID = TypeNameToOID(op.LeftType)
			}
			rightOID := uint32(0)
			if op.RightType != "" {
				rightOID = TypeNameToOID(op.RightType)
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(op.OID), 10),                     // oid
				op.Name,                                                   // oprname
				strconv.FormatUint(uint64(op.NamespaceOIDOrDefault()), 10), // oprnamespace
				strconv.FormatUint(uint64(op.OwnerOrDefault()), 10),        // oprowner
				oprkind,                                  // oprkind
				boolToPGChar(op.CanMerge),                // oprcanmerge
				boolToPGChar(op.CanHash),                 // oprcanhash
				strconv.FormatUint(uint64(leftOID), 10),  // oprleft
				strconv.FormatUint(uint64(rightOID), 10), // oprright
				"0", // oprresult (not modeled; pg_dump never reads this column)
				strconv.FormatUint(uint64(op.CommutatorOID), 10), // oprcom
				strconv.FormatUint(uint64(op.NegatorOID), 10),    // oprnegate
				strconv.FormatUint(uint64(op.FuncOID), 10),       // oprcode
				strconv.FormatUint(uint64(op.RestrictOID), 10),   // oprrest
				strconv.FormatUint(uint64(op.JoinOID), 10),       // oprjoin
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_operator"] = pgOperator

	// pg_opclass — operator-class catalog (OID 2616). pg_dump's getOpclasses runs
	// `SELECT tableoid, oid, opcmethod, opcname, opcnamespace, opcowner FROM
	// pg_opclass` — it reads ALL operator classes and filters out system-defined
	// ones at dump-out time by namespace dumpability. A CREATE OPERATOR CLASS
	// registers a row here (catalog.UserOperatorClass, ListUserOperatorClasses)
	// so pg_dump's getOpclasses/dumpOpclass re-emit it. A small set of built-in
	// default btree opclasses (builtinRangeSubtypeOpclasses) is also surfaced —
	// real PG's pg_opclass genuinely contains ~600 built-in rows queryable via
	// plain SQL (pg_dump's own comment above: "we filter out system-defined
	// opclasses at dump-out time", i.e. client-side by namespace, not via a SQL
	// WHERE clause) — goopg exposes just enough of them for a range type's
	// `pg_range.rngsubopc` to resolve via the `dumpRangeType` join. They stay
	// out of the dump: their opcnamespace (pg_catalog, 11) is never dumpable.
	// Schema matches PG's pg_opclass (pg_opclass.h). M0110-0001 (DU-002 slice
	// 10; CREATE OPERATOR CLASS support added M0119-0004; built-in range
	// subtype opclasses added M0110-0001 range-type round-trip).
	pgOpclass := &Table{
		Schema: "pg_catalog", Name: "pg_opclass", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "opcmethod", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "opcname", Type: Type{Name: "name"}, Ordinal: 2},
			{Name: "opcnamespace", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "opcowner", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "opcfamily", Type: Type{Name: "oid"}, Ordinal: 5},
			{Name: "opcintype", Type: Type{Name: "oid"}, Ordinal: 6},
			{Name: "opcdefault", Type: Type{Name: "bool"}, Ordinal: 7},
			{Name: "opckeytype", Type: Type{Name: "oid"}, Ordinal: 8},
		},
		OID: 2616,
	}
	pgOpclass.VirtualRows = func() [][]string {
		classes := c.ListUserOperatorClasses()
		var builtinRows [][]string
		if rts := c.ListRangeTypes(); len(rts) > 0 {
			seen := make(map[uint32]bool)
			for _, rt := range rts {
				if rt.OpclassOID == 0 || seen[rt.OpclassOID] {
					continue
				}
				seen[rt.OpclassOID] = true
				if row, ok := builtinOpclassRowByOID(rt.OpclassOID); ok {
					builtinRows = append(builtinRows, row)
				}
			}
		}
		if len(classes) == 0 && len(builtinRows) == 0 {
			return nil
		}
		out := make([][]string, 0, len(classes)+len(builtinRows))
		out = append(out, builtinRows...)
		for _, oc := range classes {
			out = append(out, []string{
				strconv.FormatUint(uint64(oc.OID), 10),                     // oid
				strconv.FormatUint(uint64(oc.Method), 10),                  // opcmethod
				oc.Name,                                                    // opcname
				strconv.FormatUint(uint64(oc.NamespaceOIDOrDefault()), 10), // opcnamespace
				strconv.FormatUint(uint64(oc.OwnerOrDefault()), 10),        // opcowner
				strconv.FormatUint(uint64(oc.FamilyOID), 10),               // opcfamily
				strconv.FormatUint(uint64(oc.InTypeOID), 10),               // opcintype
				boolToPGChar(oc.IsDefault),                                 // opcdefault
				strconv.FormatUint(uint64(oc.KeyTypeOID), 10),              // opckeytype
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_opclass"] = pgOpclass

	// pg_opfamily — operator-family catalog (OID 2753). pg_dump's getOpfamilies
	// runs `SELECT tableoid, oid, opfmethod, opfname, opfnamespace, opfowner FROM
	// pg_opfamily` — it reads ALL operator families and filters out system-defined
	// ones at dump-out time by namespace dumpability. A CREATE OPERATOR FAMILY
	// registers a row here (catalog.UserOperatorFamily, ListUserOperatorFamilies)
	// so pg_dump's getOpfamilies/dumpOpfamily re-emit it; with none registered
	// this view is empty exactly as before. The built-ins are in pg_catalog
	// (never dumped). Schema matches PG's pg_opfamily (pg_opfamily.h).
	// M0110-0001 (DU-002 slice 11; CREATE OPERATOR FAMILY support added
	// M0119-0004 slice 408).
	pgOpfamily := &Table{
		Schema: "pg_catalog", Name: "pg_opfamily", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "opfmethod", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "opfname", Type: Type{Name: "name"}, Ordinal: 2},
			{Name: "opfnamespace", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "opfowner", Type: Type{Name: "oid"}, Ordinal: 4},
		},
		OID: 2753,
	}
	pgOpfamily.VirtualRows = func() [][]string {
		fams := c.ListUserOperatorFamilies()
		if len(fams) == 0 {
			return nil
		}
		out := make([][]string, 0, len(fams))
		for _, f := range fams {
			out = append(out, []string{
				strconv.FormatUint(uint64(f.OID), 10),                     // oid
				strconv.FormatUint(uint64(f.Method), 10),                  // opfmethod
				f.Name,                                                    // opfname
				strconv.FormatUint(uint64(f.NamespaceOIDOrDefault()), 10), // opfnamespace
				strconv.FormatUint(uint64(f.OwnerOrDefault()), 10),        // opfowner
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_opfamily"] = pgOpfamily

	// pg_ts_parser — text-search parser catalog (OID 3601). pg_dump's
	// getTSParsers runs `SELECT tableoid, oid, prsname, prsnamespace,
	// prsstart::oid, prstoken::oid, prsend::oid, prsheadline::oid,
	// prslextype::oid FROM pg_ts_parser` — it reads ALL TS parsers and filters
	// out system-defined ones at dump-out time by namespace dumpability. goopg
	// defines no user TS parsers, and the built-ins are in pg_catalog (never
	// dumped), so this view is correctly empty (0 rows). The ::oid casts in the
	// query are no-ops since the prs* columns are regproc (oid-compatible).
	// Schema matches PG's pg_ts_parser (pg_ts_parser.h). M0110-0001 (DU-002
	// slice 12).
	pgTSParser := &Table{
		Schema: "pg_catalog", Name: "pg_ts_parser", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "prsname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "prsnamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "prsstart", Type: Type{Name: "regproc"}, Ordinal: 3},
			{Name: "prstoken", Type: Type{Name: "regproc"}, Ordinal: 4},
			{Name: "prsend", Type: Type{Name: "regproc"}, Ordinal: 5},
			{Name: "prsheadline", Type: Type{Name: "regproc"}, Ordinal: 6},
			{Name: "prslextype", Type: Type{Name: "regproc"}, Ordinal: 7},
		},
		OID: 3601,
	}
	// As of DU-002 slice 446 this view is no longer unconditionally empty: it
	// surfaces the one real built-in parser (BuiltinTSParserOID["default"]) in
	// the pg_catalog namespace, because dumpTSConfig's own query (`SELECT
	// nspname, prsname FROM pg_ts_parser p, pg_namespace n WHERE p.oid =
	// '<cfgparser>' ...`) needs a live row to resolve a user-created
	// configuration's PARSER = ... clause by OID — namespace dumpability
	// still filters it out of getTSParsers' own dump list, matching the
	// previous "always empty" behavior for that query. goopg has no real
	// start/token/end/headline/lextype routines behind this built-in, so all
	// four surface as OID 0 (InvalidOid) — never read by dumpTSConfig.
	pgTSParser.VirtualRows = func() [][]string {
		names := make([]string, 0, len(BuiltinTSParserOID))
		for name := range BuiltinTSParserOID {
			names = append(names, name)
		}
		sort.Strings(names) // deterministic row order
		rows := make([][]string, 0, len(names))
		for _, name := range names {
			rows = append(rows, []string{
				strconv.FormatUint(uint64(BuiltinTSParserOID[name]), 10), // oid
				name, // prsname
				"11", // prsnamespace (pg_catalog OID=11)
				"0",  // prsstart
				"0",  // prstoken
				"0",  // prsend
				"0",  // prsheadline
				"0",  // prslextype
			})
		}
		return rows
	}
	c.tables["pg_catalog.pg_ts_parser"] = pgTSParser

	// pg_ts_template — text-search template catalog (OID 3764). pg_dump's
	// getTSTemplates runs `SELECT tableoid, oid, tmplname, tmplnamespace,
	// tmplinit::oid, tmpllexize::oid FROM pg_ts_template` — it reads ALL TS
	// templates and filters out system-defined ones at dump-out time by
	// namespace dumpability. goopg defines no user TS templates (CREATE TEXT
	// SEARCH TEMPLATE stays a parsed-and-discarded compat no-op — a
	// C-function-loading feature with no analog here), so getTSTemplates'
	// own dump output is correctly empty either way. As of DU-002 slice 437
	// this view is no longer unconditionally empty, though: it now surfaces
	// the four real built-in templates (BuiltinTSTemplateOID) in the
	// pg_catalog namespace, because dumpTSDictionary's own query
	// (`SELECT nspname, tmplname FROM pg_ts_template p, pg_namespace n WHERE
	// p.oid = '<dicttemplate>' ...`) needs a live row to resolve a
	// user-created dictionary's TEMPLATE = ... clause by OID — namespace
	// dumpability still filters all four out of getTSTemplates' own dump
	// list, matching the previous "always empty" behavior for that query.
	// The ::oid casts in the query are no-ops since the tmpl* columns are
	// regproc (oid-compatible); goopg has no init/lexize routines behind
	// these built-ins, so both surface as OID 0 (InvalidOid) — never read by
	// dumpTSDictionary, only tmplname/tmplnamespace are. Schema matches PG's
	// pg_ts_template (pg_ts_template.h). M0110-0001 (DU-002 slice 13, slice
	// 437 follow-up).
	pgTSTemplate := &Table{
		Schema: "pg_catalog", Name: "pg_ts_template", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "tmplname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "tmplnamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "tmplinit", Type: Type{Name: "regproc"}, Ordinal: 3},
			{Name: "tmpllexize", Type: Type{Name: "regproc"}, Ordinal: 4},
		},
		OID: 3764,
	}
	pgTSTemplate.VirtualRows = func() [][]string {
		names := make([]string, 0, len(BuiltinTSTemplateOID))
		for name := range BuiltinTSTemplateOID {
			names = append(names, name)
		}
		sort.Strings(names) // deterministic row order
		rows := make([][]string, 0, len(names))
		for _, name := range names {
			rows = append(rows, []string{
				strconv.FormatUint(uint64(BuiltinTSTemplateOID[name]), 10), // oid
				name, // tmplname
				"11", // tmplnamespace (pg_catalog OID=11)
				"0",  // tmplinit (no real init routine)
				"0",  // tmpllexize (no real lexize routine)
			})
		}
		return rows
	}
	c.tables["pg_catalog.pg_ts_template"] = pgTSTemplate

	// pg_ts_dict — text-search dictionary catalog (OID 3600). pg_dump's
	// getTSDictionaries runs `SELECT tableoid, oid, dictname, dictnamespace,
	// dictowner, dicttemplate, dictinitoption FROM pg_ts_dict` — it reads ALL
	// TS dictionaries and filters out system-defined ones at dump-out time by
	// namespace dumpability. goopg defines no built-in TS dictionaries so this
	// view was previously always empty (0 rows); as of DU-002 slice 437 it
	// surfaces user-created dictionaries (CREATE TEXT SEARCH DICTIONARY),
	// stored in InMemory.userTSDicts, so pg_dump's getTSDictionaries /
	// dumpTSDictionary round-trip them. dicttemplate is an oid FK to
	// pg_ts_template (not a regproc); dictinitoption is text (NULL when no
	// options were given). Schema matches PG's pg_ts_dict (pg_ts_dict.h).
	// M0110-0001 (DU-002 slice 14, slice 437 follow-up).
	pgTSDict := &Table{
		Schema: "pg_catalog", Name: "pg_ts_dict", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "dictname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "dictnamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "dictowner", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "dicttemplate", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "dictinitoption", Type: Type{Name: "text"}, Ordinal: 5},
		},
		OID: 3600,
	}
	pgTSDict.VirtualRows = func() [][]string {
		dicts := c.ListUserTSDicts()
		// As of DU-002 slice 446, this view also surfaces the one built-in
		// dictionary (BuiltinTSDictOID["simple"]) in the pg_catalog namespace:
		// a CREATE TEXT SEARCH CONFIGURATION's ADD MAPPING ... WITH simple
		// clause names it, and dumpTSConfig's mapdict::regdictionary cast
		// needs a live row (by OID) to resolve it back to a bare "simple".
		// Namespace dumpability still filters it out of getTSDictionaries'
		// own dump list, matching the previous "always empty" behavior there.
		rows := make([][]string, 0, len(dicts)+1)
		rows = append(rows, []string{
			strconv.FormatUint(uint64(BuiltinTSDictOID["simple"]), 10),   // oid
			"simple", // dictname
			"11",     // dictnamespace (pg_catalog OID=11)
			"10",     // dictowner (bootstrap superuser)
			strconv.FormatUint(uint64(BuiltinTSTemplateOID["simple"]), 10), // dicttemplate
			VirtualNull, // dictinitoption
		})
		for _, ud := range dicts {
			initOpt := VirtualNull
			if ud.InitOption != "" {
				initOpt = ud.InitOption
			}
			rows = append(rows, []string{
				strconv.FormatUint(uint64(ud.OID), 10), // oid
				ud.Name,                                // dictname
				strconv.FormatUint(uint64(ud.NamespaceOID), 10), // dictnamespace
				strconv.FormatUint(uint64(ud.Owner), 10),        // dictowner
				strconv.FormatUint(uint64(ud.Template), 10),     // dicttemplate
				initOpt, // dictinitoption
			})
		}
		return rows
	}
	c.tables["pg_catalog.pg_ts_dict"] = pgTSDict

	// pg_ts_config — text-search configuration catalog (OID 3602). pg_dump's
	// getTSConfigurations runs `SELECT tableoid, oid, cfgname, cfgnamespace,
	// cfgowner, cfgparser FROM pg_ts_config` — it reads ALL TS configurations
	// and filters out system-defined ones at dump-out time by namespace
	// dumpability. cfgparser is an oid FK to pg_ts_parser. Schema matches PG's
	// pg_ts_config (pg_ts_config.h). M0110-0001 (DU-002 slice 15). As of DU-002
	// slice 446 this view surfaces user-created configurations (CREATE TEXT
	// SEARCH CONFIGURATION), stored in InMemory.userTSConfigs, so pg_dump's
	// getTSConfigurations / dumpTSConfig round-trip them.
	pgTSConfig := &Table{
		Schema: "pg_catalog", Name: "pg_ts_config", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "cfgname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "cfgnamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "cfgowner", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "cfgparser", Type: Type{Name: "oid"}, Ordinal: 4},
		},
		OID: 3602,
	}
	pgTSConfig.VirtualRows = func() [][]string {
		cfgs := c.ListUserTSConfigs()
		if len(cfgs) == 0 {
			return nil
		}
		rows := make([][]string, 0, len(cfgs))
		for _, uc := range cfgs {
			rows = append(rows, []string{
				strconv.FormatUint(uint64(uc.OID), 10), // oid
				uc.Name,                                // cfgname
				strconv.FormatUint(uint64(uc.NamespaceOID), 10), // cfgnamespace
				strconv.FormatUint(uint64(uc.Owner), 10),        // cfgowner
				strconv.FormatUint(uint64(uc.Parser), 10),       // cfgparser
			})
		}
		return rows
	}
	c.tables["pg_catalog.pg_ts_config"] = pgTSConfig

	// pg_ts_config_map — per-token-type dictionary mapping for a text search
	// configuration (OID 3603). dumpTSConfig's own query (`SELECT ... FROM
	// pg_catalog.pg_ts_config_map AS m WHERE m.mapcfg = '<cfgoid>' ORDER BY
	// m.mapcfg, m.maptokentype, m.mapseqno`) reads this directly (not via
	// getTSConfigurations' dump-list query), so it must carry live rows for
	// every ALTER TEXT SEARCH CONFIGURATION ... ADD MAPPING applied to a
	// user-created configuration. Schema matches PG's pg_ts_config_map
	// (pg_ts_config_map.h); the on-disk heap-catalog metadata for this
	// relation was already nailed in internal/initdb/relcache_init.go
	// (pgTsConfigMapAttrs, OID 3603) for standby pg_class/pg_attribute
	// parity — this is the query-serving counterpart. DU-002 slice 446
	// (M0119-0004).
	pgTSConfigMap := &Table{
		Schema: "pg_catalog", Name: "pg_ts_config_map", Virtual: true,
		Columns: []Column{
			{Name: "mapcfg", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "maptokentype", Type: Type{Name: "int4"}, Ordinal: 1},
			{Name: "mapseqno", Type: Type{Name: "int4"}, Ordinal: 2},
			{Name: "mapdict", Type: Type{Name: "oid"}, Ordinal: 3},
		},
		OID: 3603,
	}
	pgTSConfigMap.VirtualRows = func() [][]string {
		cfgs := c.ListUserTSConfigs()
		var rows [][]string
		for _, uc := range cfgs {
			tokIDByName := make(map[string]int, len(DefaultParserTokenTypes))
			for _, tt := range DefaultParserTokenTypes {
				tokIDByName[tt.Alias] = tt.TokID
			}
			for _, m := range uc.Mappings {
				tokID, ok := tokIDByName[m.TokenType]
				if !ok {
					continue
				}
				for seq, dictOID := range m.DictOIDs {
					rows = append(rows, []string{
						strconv.FormatUint(uint64(uc.OID), 10), // mapcfg
						strconv.Itoa(tokID),                    // maptokentype
						strconv.Itoa(seq + 1),                  // mapseqno (1-based)
						strconv.FormatUint(uint64(dictOID), 10), // mapdict
					})
				}
			}
		}
		return rows
	}
	c.tables["pg_catalog.pg_ts_config_map"] = pgTSConfigMap

	// pg_foreign_data_wrapper — foreign-data wrapper catalog (OID 2328).
	// pg_dump's getForeignDataWrappers runs `SELECT tableoid, oid, fdwname,
	// fdwowner, fdwhandler::pg_catalog.regproc, fdwvalidator::pg_catalog.regproc,
	// fdwacl, acldefault('F', fdwowner) AS acldefault,
	// array_to_string(ARRAY(SELECT quote_ident(option_name) || ' ' ||
	// quote_literal(option_value) FROM pg_options_to_table(fdwoptions) ORDER BY
	// option_name), E',\n    ') AS fdwoptions FROM pg_foreign_data_wrapper` — it
	// reads ALL FDWs and dumps the user-defined ones. goopg defines no FDWs (no
	// CREATE FOREIGN DATA WRAPPER), so this view is correctly empty (0 rows); the
	// pg_options_to_table SRF in the ARRAY subquery is therefore never evaluated.
	// Schema matches PG's pg_foreign_data_wrapper (pg_foreign_data_wrapper.h):
	// oid, fdwname name, fdwowner oid, fdwhandler oid (FK to pg_proc),
	// fdwvalidator oid (FK to pg_proc), fdwacl aclitem[], fdwoptions text[].
	// M0110-0001 (DU-002 slice 16).
	pgForeignDataWrapper := &Table{
		Schema: "pg_catalog", Name: "pg_foreign_data_wrapper", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "fdwname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "fdwowner", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "fdwhandler", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "fdwvalidator", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "fdwacl", Type: Type{Name: "aclitem[]"}, Ordinal: 5},
			{Name: "fdwoptions", Type: Type{Name: "text[]"}, Ordinal: 6},
		},
		OID: 2328,
	}
	// Surface user-created FDWs (CREATE FOREIGN DATA WRAPPER) so they round-trip.
	// fdwhandler/fdwvalidator hold the real pg_proc OID resolved at CREATE/ALTER
	// time (0 when no HANDLER/VALIDATOR clause was given) — the query's
	// `::regproc` cast renders 0 as '-' (so dumpForeignDataWrapper omits the
	// clause) and a non-zero OID as the resolved function name (DU-002
	// M0119-0004, closing the "HANDLER/VALIDATOR discarded" gap slices
	// 375/380/421 left open). fdwoptions is NULL (empty string) absent an
	// OPTIONS clause, so the OPTIONS clause is skipped. fdwacl materializes via
	// ForeignDataWrapperACLText so a `GRANT … ON FOREIGN DATA WRAPPER …`
	// round-trips (DU-002 slice 428); DU-002 slice 375.
	pgForeignDataWrapper.VirtualRows = func() [][]string {
		fdws := c.ListForeignDataWrappers()
		if len(fdws) == 0 {
			return nil
		}
		out := make([][]string, 0, len(fdws))
		for _, f := range fdws {
			owner := f.Owner
			if owner == 0 {
				owner = 10 // bootstrap superuser (postgres); getRoleName(10) → "postgres"
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(f.OID), 10),          // oid
				f.Name,                                         // fdwname
				strconv.FormatUint(uint64(owner), 10),          // fdwowner
				strconv.FormatUint(uint64(f.HandlerOID), 10),   // fdwhandler
				strconv.FormatUint(uint64(f.ValidatorOID), 10), // fdwvalidator
				c.ForeignDataWrapperACLText(f.OID),             // fdwacl
				optionsArrayLiteral(f.Options),                 // fdwoptions text[] ("{name=value,…}" or "" for NULL)
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_foreign_data_wrapper"] = pgForeignDataWrapper

	// pg_foreign_server — foreign-server catalog (OID 1417). pg_dump's
	// getForeignServers runs `SELECT tableoid, oid, srvname, srvowner,
	// srvfdw, srvtype, srvversion, srvacl,
	// acldefault('S', srvowner) AS acldefault,
	// array_to_string(ARRAY(SELECT quote_ident(option_name) || ' ' ||
	// quote_literal(option_value) FROM pg_options_to_table(srvoptions) ORDER BY
	// option_name), E',\n    ') AS srvoptions FROM pg_foreign_server` after
	// getForeignDataWrappers. goopg defines no foreign servers (no CREATE SERVER),
	// so this view is correctly empty (0 rows); the correlated
	// pg_options_to_table(srvoptions) ARRAY subquery (slice 18) is therefore never
	// evaluated. Schema matches PG's pg_foreign_server (pg_foreign_server.h):
	// oid, srvname name, srvowner oid, srvfdw oid (FK to pg_foreign_data_wrapper),
	// srvtype text, srvversion text, srvacl aclitem[], srvoptions text[].
	// M0110-0001 (DU-002 slice 19).
	pgForeignServer := &Table{
		Schema: "pg_catalog", Name: "pg_foreign_server", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "srvname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "srvowner", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "srvfdw", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "srvtype", Type: Type{Name: "text"}, Ordinal: 4},
			{Name: "srvversion", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "srvacl", Type: Type{Name: "aclitem[]"}, Ordinal: 6},
			{Name: "srvoptions", Type: Type{Name: "text[]"}, Ordinal: 7},
		},
		OID: 1417,
	}
	// Surface user-created foreign servers (CREATE SERVER) so they round-trip.
	// srvfdw resolves to the referenced FDW's stable OID (dumpForeignServer runs
	// `SELECT fdwname FROM pg_foreign_data_wrapper WHERE oid = srvfdw` to recover
	// the wrapper name). srvtype/srvversion are NULL (empty string), so the TYPE/
	// VERSION clauses are omitted; srvoptions is NULL absent an OPTIONS clause, so
	// the OPTIONS clause is skipped. DU-002 slice 376. srvacl renders from the
	// materialized ACL store (ForeignServerACLText) — NULL until a GRANT/REVOKE
	// … ON FOREIGN SERVER … is recorded, matching acldefault('S', srvowner) and
	// producing no spurious dumpACL output for the common no-grant case. DU-002
	// slice 427.
	pgForeignServer.VirtualRows = func() [][]string {
		servers := c.ListForeignServers()
		if len(servers) == 0 {
			return nil
		}
		out := make([][]string, 0, len(servers))
		for _, s := range servers {
			owner := s.Owner
			if owner == 0 {
				owner = 10 // bootstrap superuser (postgres); getRoleName(10) → "postgres"
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(s.OID), 10), // oid
				s.Name,                                // srvname
				strconv.FormatUint(uint64(owner), 10), // srvowner
				strconv.FormatUint(uint64(c.ForeignDataWrapperOID(s.FdwName)), 10), // srvfdw
				s.Type,                         // srvtype ("" → NULL, TYPE clause omitted)
				s.Version,                      // srvversion ("" → NULL, VERSION clause omitted)
				c.ForeignServerACLText(s.OID),  // srvacl (NULL until a GRANT, DU-002 slice 427)
				optionsArrayLiteral(s.Options), // srvoptions text[] ("{name=value,…}" or "" for NULL)
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_foreign_server"] = pgForeignServer

	// pg_user_mappings — the publicly-readable view over pg_user_mapping JOIN
	// pg_foreign_server (system_views.sql). For every foreign server, pg_dump's
	// dumpForeignServer runs dumpUserMappings, which queries
	//   SELECT usename, array_to_string(ARRAY(SELECT quote_ident(option_name) ||
	//   ' ' || quote_literal(option_value) FROM pg_options_to_table(umoptions)
	//   ORDER BY option_name), E',\n    ') AS umoptions FROM pg_user_mappings
	//   WHERE srvid = '<oid>' ORDER BY usename
	// Once any CREATE SERVER exists (slice 376), this query runs for real, so the
	// view MUST exist or pg_dump aborts with `relation "pg_user_mappings" does not
	// exist` (exit 1, empty dump). goopg models it as a virtual relation surfacing
	// the user-mapping registry. Schema mirrors the system view's output columns:
	// umid oid, srvid oid, srvname name, umuser oid, usename name, umoptions text[].
	// DU-002 slice 377.
	pgUserMappings := &Table{
		Schema: "pg_catalog", Name: "pg_user_mappings", Virtual: true,
		Columns: []Column{
			{Name: "umid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "srvid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "srvname", Type: Type{Name: "name"}, Ordinal: 2},
			{Name: "umuser", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "usename", Type: Type{Name: "name"}, Ordinal: 4},
			{Name: "umoptions", Type: Type{Name: "text[]"}, Ordinal: 5},
		},
	}
	// Surface user-created user mappings (CREATE USER MAPPING). srvid resolves to
	// the referenced server's stable OID (the column pg_dump filters on); usename
	// is the mapped role name (PUBLIC → 'public'); umuser is its role OID (0 for
	// PUBLIC). umoptions renders the mapping's OPTIONS as the text[] literal
	// "{name=value,…}" (or NULL when none), which pg_options_to_table(umoptions)
	// expands → dumpUserMappings appends `OPTIONS (\n    name 'value',\n    …\n)`;
	// with no options it emits the bare `CREATE USER MAPPING FOR <usename> SERVER
	// <srv>;`. DU-002 slice 377 (options: slice 379).
	pgUserMappings.VirtualRows = func() [][]string {
		mappings := c.ListUserMappings()
		if len(mappings) == 0 {
			return nil
		}
		out := make([][]string, 0, len(mappings))
		for _, m := range mappings {
			usename := m.UmUser
			umuser := uint32(0) // ACL_ID_PUBLIC
			if usename == "" || strings.EqualFold(usename, "public") {
				usename = "public"
			} else if oid, ok := c.RoleOID(m.UmUser); ok {
				umuser = oid
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(m.OID), 10),                         // umid
				strconv.FormatUint(uint64(c.ForeignServerOID(m.SrvName)), 10), // srvid
				m.SrvName,                              // srvname
				strconv.FormatUint(uint64(umuser), 10), // umuser
				usename,                                // usename
				optionsArrayLiteral(m.Options),         // umoptions text[] ("{name=value,…}" or "" for NULL)
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_user_mappings"] = pgUserMappings

	// pg_default_acl — default-ACL catalog (OID 826). After getForeignServers,
	// pg_dump's getUserMappings short-circuits (no foreign servers → no catalog
	// query), so the next catalog query is getDefaultACLs:
	//   SELECT oid, tableoid, defaclrole, defaclnamespace, defaclobjtype,
	//   defaclacl, CASE WHEN defaclnamespace = 0 THEN acldefault(CASE WHEN
	//   defaclobjtype = 'S' THEN 's'::"char" ELSE defaclobjtype END, defaclrole)
	//   ELSE '{}' END AS acldefault FROM pg_default_acl
	// Rows are projected from the defaultACLOIDs registry, populated by
	// execAlterDefaultPrivileges on `ALTER DEFAULT PRIVILEGES ... GRANT/REVOKE
	// ...` (mirrors pg_parameter_acl's lazy-materialization pattern above) —
	// empty until the first such statement, exactly like real PostgreSQL never
	// rowing a pg_default_acl tuple until SetDefaultACL first materializes one.
	// The acldefault CASE projection above is evaluated by the executor's
	// existing evalAclDefault (expr.go) against this row's own
	// defaclrole/defaclobjtype, not computed here. Schema matches PG's
	// pg_default_acl (pg_default_acl.h): oid, defaclrole oid, defaclnamespace
	// oid, defaclobjtype "char", defaclacl aclitem[]. M0110-0001 (DU-002 slice
	// 20 / slice 438 follow-up).
	pgDefaultACL := &Table{
		Schema: "pg_catalog", Name: "pg_default_acl", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "defaclrole", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "defaclnamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "defaclobjtype", Type: Type{Name: "char"}, Ordinal: 3},
			{Name: "defaclacl", Type: Type{Name: "aclitem[]"}, Ordinal: 4},
		},
		OID: 826,
	}
	pgDefaultACL.VirtualRows = func() [][]string {
		entries := c.DefaultACLEntries()
		if len(entries) == 0 {
			return nil
		}
		out := make([][]string, 0, len(entries))
		for _, e := range entries {
			acl := VirtualNull
			if aclText := c.DefaultACLText(e.OID, e.ObjType); aclText != "" {
				acl = aclText
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(e.OID), 10),
				strconv.FormatUint(uint64(e.RoleOID), 10),
				strconv.FormatUint(uint64(e.SchemaOID), 10),
				string(e.ObjType),
				acl,
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_default_acl"] = pgDefaultACL

	// pg_conversion — encoding-conversion catalog (OID 2607). After
	// getDefaultACLs, pg_dump's getConversions runs:
	//   SELECT tableoid, oid, conname, connamespace, conowner FROM pg_conversion
	// (pg_dump.c getConversions: "find all conversions, including builtin
	// conversions; we filter out system-defined conversions at dump-out time").
	// PG ships ~130 built-in conversions, but every one lives in the pg_catalog
	// namespace and is filtered out at dump-out time (selectDumpableObject marks
	// pg_catalog objects DUMP_COMPONENT_NONE), so only user conversions (CREATE
	// [DEFAULT] CONVERSION, surfaced via ListUserConversions below) appear as
	// dumpable rows. Schema matches PG's pg_conversion (pg_conversion.h): oid,
	// conname name, connamespace oid, conowner oid, conforencoding int4,
	// contoencoding int4, conproc regproc, condefault bool. conproc is typed
	// regproc (not oid) because dumpConversion selects it raw and expects the
	// function name text. M0110-0001 (DU-002 slice 21); user rows DU-002 slice 399.
	pgConversion := &Table{
		Schema: "pg_catalog", Name: "pg_conversion", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "conname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "connamespace", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "conowner", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "conforencoding", Type: Type{Name: "int4"}, Ordinal: 4},
			{Name: "contoencoding", Type: Type{Name: "int4"}, Ordinal: 5},
			{Name: "conproc", Type: Type{Name: "regproc"}, Ordinal: 6},
			{Name: "condefault", Type: Type{Name: "bool"}, Ordinal: 7},
		},
		OID: 2607,
	}
	// Surface user-created conversions (CREATE [DEFAULT] CONVERSION) so they
	// round-trip. getConversions reads every row (built-in conversions live in
	// pg_catalog and are filtered out at dump-out time, so an empty view stays
	// correct for the no-user-conversion case); dumpConversion then queries the
	// per-row detail and emits `CREATE [DEFAULT] CONVERSION <ns>.<name> FOR
	// '<for>' TO '<to>' FROM <conproc>`. conforencoding/contoencoding are the
	// pg_enc integer IDs (dumpConversion wraps them in pg_encoding_to_char), and
	// conproc renders the schema-qualified function name (pg_dump's empty
	// search_path qualifies the regproc). DU-002 slice 399.
	pgConversion.VirtualRows = func() [][]string {
		convs := c.ListUserConversions()
		if len(convs) == 0 {
			return nil
		}
		out := make([][]string, 0, len(convs))
		for _, cv := range convs {
			condefault := "f"
			if cv.Default {
				condefault = "t"
			}
			conproc := cv.ProcName
			if cv.ProcSchema != "" {
				conproc = cv.ProcSchema + "." + cv.ProcName
			}
			// Prefer a live FuncOID->pg_proc lookup over the as-written text so a
			// RENAME on the conversion function after CREATE CONVERSION still
			// dumps correctly (mirrors regproc output semantics; conproc is a
			// real OID reference in PG, not captured text). DU-002 slice 403.
			if cv.FuncOID != 0 && c.routines != nil {
				if r := c.routines.LookupByOID(cv.FuncOID); r != nil {
					conproc = r.Name
					if r.Schema != "" {
						conproc = r.Schema + "." + r.Name
					}
				}
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(cv.OID), 10),          // oid
				cv.Name,                                          // conname
				strconv.FormatUint(uint64(cv.NamespaceOID), 10), // connamespace
				strconv.FormatUint(uint64(cv.Owner), 10),        // conowner
				strconv.FormatInt(int64(cv.ForEncoding), 10),    // conforencoding
				strconv.FormatInt(int64(cv.ToEncoding), 10),     // contoencoding
				conproc,                                          // conproc (regproc text)
				condefault,                                       // condefault
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_conversion"] = pgConversion

	// pg_range — range-type catalog (OID 3541). After getConversions, pg_dump's
	// getCasts runs:
	//   SELECT tableoid, oid, castsource, casttarget, castfunc, castcontext,
	//   castmethod FROM pg_cast c WHERE NOT EXISTS ( SELECT 1 FROM pg_range r
	//   WHERE c.castsource = r.rngtypid AND c.casttarget = r.rngmultitypid )
	//   ORDER BY 3,4
	// (pg_dump.c getCasts: range types' auto-generated casts are excluded via the
	// NOT EXISTS against pg_range so they aren't dumped separately). Schema
	// matches PG's pg_range (pg_range.h): NOTE pg_range has NO oid column;
	// rngtypid is the key. Cols: rngtypid oid, rngsubtype oid, rngmultitypid
	// oid, rngcollation oid, rngsubopc oid, rngcanonical regproc, rngsubdiff
	// regproc. M0110-0001 (DU-002 slice 22; populated M0110-0001 range-type
	// round-trip).
	pgRange := &Table{
		Schema: "pg_catalog", Name: "pg_range", Virtual: true,
		Columns: []Column{
			{Name: "rngtypid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "rngsubtype", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "rngmultitypid", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "rngcollation", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "rngsubopc", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "rngcanonical", Type: Type{Name: "regproc"}, Ordinal: 5},
			{Name: "rngsubdiff", Type: Type{Name: "regproc"}, Ordinal: 6},
		},
		OID: 3541,
	}
	// Surface user-created range types (CREATE TYPE ... AS RANGE) so
	// pg_dump's dumpRangeType join finds a row. rngcanonical/rngsubdiff are
	// always "-" (unsupported `canonical`/`subtype_diff` options — DU-002
	// deferral); rngcollation is RegisterRangeType's resolved
	// RangeType.CollationOID (0/InvalidOid for a non-collatable subtype, the
	// subtype's own default, or an explicit `collation` option override), so
	// dumpRangeType's `CASE WHEN rngcollation = st.typcollation THEN 0 ...`
	// still yields 0 for the common no-explicit-collation case but now also
	// reflects a real override. DU-002 (M0110-0001, slice 429 follow-up
	// sub-item (a)).
	pgRange.VirtualRows = func() [][]string {
		rts := c.ListRangeTypes()
		if len(rts) == 0 {
			return nil
		}
		out := make([][]string, 0, len(rts))
		for _, rt := range rts {
			subtypeOID := TypeNameToOID(rt.SubtypeName)
			out = append(out, []string{
				strconv.FormatUint(uint64(rt.OID), 10),           // rngtypid
				strconv.FormatUint(uint64(subtypeOID), 10),       // rngsubtype
				strconv.FormatUint(uint64(rt.MultirangeOID), 10), // rngmultitypid
				strconv.FormatUint(uint64(rt.CollationOID), 10),  // rngcollation
				strconv.FormatUint(uint64(rt.OpclassOID), 10),    // rngsubopc
				"-", // rngcanonical
				"-", // rngsubdiff
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_range"] = pgRange

	// pg_event_trigger — event-trigger catalog (OID 3466). After getCasts,
	// pg_dump's getEventTriggers runs:
	//   SELECT e.tableoid, e.oid, evtname, evtenabled, evtevent, evtowner,
	//   array_to_string(array(select quote_literal(x) from unnest(evttags) as
	//   t(x)), ', ') as evttags, e.evtfoid::regproc as evtfname FROM
	//   pg_event_trigger e ORDER BY e.oid
	// (pg_dump.c getEventTriggers). goopg does not fire event triggers — this
	// only round-trips CREATE/DROP EVENT TRIGGER through pg_dump (schema
	// fidelity only). Schema matches PG's pg_event_trigger
	// (pg_event_trigger.h): oid, evtname name, evtevent name, evtowner oid,
	// evtfoid oid, evtenabled "char", evttags text[]. M0110-0001 (DU-002 slice
	// 23; populated DU-002 (M0119-0004)).
	pgEventTrigger := &Table{
		Schema: "pg_catalog", Name: "pg_event_trigger", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "evtname", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "evtevent", Type: Type{Name: "name"}, Ordinal: 2},
			{Name: "evtowner", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "evtfoid", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "evtenabled", Type: Type{Name: "char"}, Ordinal: 5},
			{Name: "evttags", Type: Type{Name: "text[]"}, Ordinal: 6},
		},
		OID: 3466,
	}
	// Surface user-created event triggers (CREATE EVENT TRIGGER) so they
	// round-trip. evtfoid renders as a plain OID; the `::regproc` cast in
	// pg_dump's own query (above) resolves it to a name. DU-002 (M0119-0004).
	pgEventTrigger.VirtualRows = func() [][]string {
		ets := c.ListEventTriggers()
		if len(ets) == 0 {
			return nil
		}
		out := make([][]string, 0, len(ets))
		for _, et := range ets {
			owner := et.Owner
			if owner == 0 {
				owner = 10 // bootstrap superuser (postgres); getRoleName(10) → "postgres"
			}
			tags := ""
			if len(et.Tags) > 0 {
				tags = arrayTextLiteral(et.Tags)
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(et.OID), 10),     // oid
				et.Name,                                    // evtname
				et.Event,                                   // evtevent
				strconv.FormatUint(uint64(owner), 10),      // evtowner
				strconv.FormatUint(uint64(et.FuncOID), 10), // evtfoid
				et.Enabled,                                 // evtenabled
				tags,                                       // evttags text[] ("{...}" or "" for NULL)
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_event_trigger"] = pgEventTrigger

	// pg_partitioned_table — partition-key catalog (OID 3350). After
	// getEventTriggers, pg_dump's getTableAttrs / collectComments path probes
	// partitioning metadata. The query that first hits this catalog is:
	//   SELECT partrelid FROM pg_partitioned_table WHERE (SELECT c.oid FROM
	//   pg_opclass c JOIN pg_am a ON c.opcmethod = a.oid WHERE opcname =
	//   'enum_ops' AND opcnamespace = 'pg_catalog'::regnamespace AND amname =
	//   'hash') = ANY(partclass)
	// goopg surfaces partition membership through pg_class.relkind='p'/'P' and
	// pg_inherits, not a separate per-partition-key heap, so an empty view (0
	// rows) is correct here — no user partitioned tables exist in the dumped
	// schema, identical to a stock PG cluster with none. With 0 rows the
	// `= ANY(partclass)` predicate is never evaluated, so the oidvector column
	// is fine. Schema matches PG's pg_partitioned_table (pg_partitioned_table.h):
	// partrelid oid, partstrat "char", partnatts int2, partdefid oid, partattrs
	// int2vector, partclass oidvector, partcollation oidvector, partexprs
	// pg_node_tree. goopg represents int2vector/oidvector as int2[]/oid[] (see
	// pg_index indkey/indclass). M0110-0001 (DU-002 slice 25).
	pgPartitionedTable := &Table{
		Schema: "pg_catalog", Name: "pg_partitioned_table", Virtual: true,
		Columns: []Column{
			{Name: "partrelid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "partstrat", Type: Type{Name: "char"}, Ordinal: 1},
			{Name: "partnatts", Type: Type{Name: "int2"}, Ordinal: 2},
			{Name: "partdefid", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "partattrs", Type: Type{Name: "int2[]"}, Ordinal: 4},
			{Name: "partclass", Type: Type{Name: "oid[]"}, Ordinal: 5},
			{Name: "partcollation", Type: Type{Name: "oid[]"}, Ordinal: 6},
			{Name: "partexprs", Type: Type{Name: "pg_node_tree"}, Ordinal: 7},
		},
		OID: 3350,
	}
	pgPartitionedTable.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_partitioned_table"] = pgPartitionedTable

	// pg_trigger — trigger catalog (OID 2620). After getTableAttrs, pg_dump's
	// getTriggers probes per-table triggers. The query that first hits this
	// catalog is:
	//   SELECT t.tgrelid, t.tgname, pg_catalog.pg_get_triggerdef(t.oid, false)
	//   AS tgdef, t.tgenabled, t.tableoid, t.oid, t.tgparentid <> 0 AS
	//   tgispartition FROM unnest('{}'::pg_catalog.oid[]) AS src(tbloid) JOIN
	//   pg_catalog.pg_trigger t ON (src.tbloid = t.tgrelid) LEFT JOIN
	//   pg_catalog.pg_trigger u ON (u.oid = t.tgparentid) WHERE ((NOT
	//   t.tgisinternal AND t.tgparentid = 0) OR t.tgenabled != u.tgenabled)
	//   ORDER BY t.tgrelid, t.tgname
	// DU-002 slice 319 wires VirtualRows to project goopg's user-defined
	// triggers (catalog.Table.Triggers) so a plain CREATE TRIGGER round-trips
	// through pg_dump. Each row carries the trigger oid/tgrelid/tgname plus the
	// PG tgtype bitmask, tgfoid (resolved from the routine registry),
	// tgenabled='O', tgisinternal='f', and tgparentid=0; pg_get_triggerdef(oid)
	// (expr.go) reconstructs the CREATE TRIGGER statement that pg_dump emits
	// verbatim. The unnest('{oids}') source carries the real table OIDs, the
	// self-JOIN finds no parent (tgparentid=0 ≠ any oid) so the LEFT JOIN keeps
	// the row, and the WHERE's first disjunct (NOT tgisinternal AND
	// tgparentid=0) admits it. Schema
	// matches PG's pg_trigger (pg_trigger.h): tgrelid oid, tgparentid oid,
	// tgname name, tgfoid oid, tgtype int2, tgenabled "char", tgisinternal bool,
	// tgconstrrelid oid, tgconstrindid oid, tgconstraint oid, tgdeferrable bool,
	// tginitdeferred bool, tgnargs int2, tgattr int2vector, tgargs bytea, tgqual
	// pg_node_tree, tgoldtable name, tgnewtable name. goopg represents
	// int2vector as int2[] (see pg_index indkey). M0110-0001 (DU-002 slice 26).
	pgTrigger := &Table{
		Schema: "pg_catalog", Name: "pg_trigger", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "tgrelid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "tgparentid", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "tgname", Type: Type{Name: "name"}, Ordinal: 3},
			{Name: "tgfoid", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "tgtype", Type: Type{Name: "int2"}, Ordinal: 5},
			{Name: "tgenabled", Type: Type{Name: "char"}, Ordinal: 6},
			{Name: "tgisinternal", Type: Type{Name: "bool"}, Ordinal: 7},
			{Name: "tgconstrrelid", Type: Type{Name: "oid"}, Ordinal: 8},
			{Name: "tgconstrindid", Type: Type{Name: "oid"}, Ordinal: 9},
			{Name: "tgconstraint", Type: Type{Name: "oid"}, Ordinal: 10},
			{Name: "tgdeferrable", Type: Type{Name: "bool"}, Ordinal: 11},
			{Name: "tginitdeferred", Type: Type{Name: "bool"}, Ordinal: 12},
			{Name: "tgnargs", Type: Type{Name: "int2"}, Ordinal: 13},
			{Name: "tgattr", Type: Type{Name: "int2[]"}, Ordinal: 14},
			{Name: "tgargs", Type: Type{Name: "bytea"}, Ordinal: 15},
			{Name: "tgqual", Type: Type{Name: "pg_node_tree"}, Ordinal: 16},
			{Name: "tgoldtable", Type: Type{Name: "name"}, Ordinal: 17},
			{Name: "tgnewtable", Type: Type{Name: "name"}, Ordinal: 18},
		},
		OID: 2620,
	}
	pgTrigger.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		var out [][]string
		for _, tbl := range c.tables {
			if tbl == nil || tbl.Virtual || tbl.OID == 0 || len(tbl.Triggers) == 0 {
				continue
			}
			for _, trig := range tbl.Triggers {
				if trig.OID == 0 {
					continue // predates OID tracking → invisible to pg_dump
				}
				// tgtype bitmask (pg_trigger.h): ROW=1, BEFORE=2, INSERT=4,
				// DELETE=8, UPDATE=16, TRUNCATE=32, INSTEAD=64. AFTER is the
				// absence of the BEFORE and INSTEAD bits.
				var tgtype int
				if trig.ForEachRow {
					tgtype |= 1 << 0
				}
				switch trig.Timing {
				case TriggerBefore:
					tgtype |= 1 << 1
				case TriggerInsteadOf:
					tgtype |= 1 << 6
				}
				for _, ev := range trig.Events {
					switch strings.ToLower(ev) {
					case "insert":
						tgtype |= 1 << 2
					case "delete":
						tgtype |= 1 << 3
					case "update":
						tgtype |= 1 << 4
					case "truncate":
						tgtype |= 1 << 5
					}
				}
				// tgfoid: resolve the trigger function's OID from the routine
				// registry. pg_dump's getTriggers does not read tgfoid (it calls
				// pg_get_triggerdef), so 0 (unresolved) is harmless, but project
				// the real OID for catalog faithfulness.
				var tgfoid uint32
				fnSchema := trig.FuncSchema
				if fnSchema == "" {
					fnSchema = "public"
				}
				if c.routines != nil {
					for _, r := range c.routines.LookupByName(parser.ObjectName{Schema: trig.FuncSchema, Name: trig.FuncName}) {
						tgfoid = r.OID
						break
					}
				}
				row := make([]string, 19)
				row[0] = fmt.Sprintf("%d", trig.OID)     // oid
				row[1] = fmt.Sprintf("%d", trig.TableOID) // tgrelid
				row[2] = "0"                              // tgparentid
				row[3] = trig.Name                        // tgname
				row[4] = fmt.Sprintf("%d", tgfoid)        // tgfoid
				row[5] = fmt.Sprintf("%d", tgtype)        // tgtype
				row[6] = "O"                              // tgenabled (origin/enabled)
				row[7] = "f"                              // tgisinternal
				row[8] = "0"                              // tgconstrrelid
				row[9] = "0"                              // tgconstrindid
				// tgconstraint / tgdeferrable / tginitdeferred — a CREATE CONSTRAINT
				// TRIGGER carries a non-zero tgconstraint (the implicit pg_constraint
				// OID) so pg_get_triggerdef emits `CREATE CONSTRAINT TRIGGER` plus the
				// deferrability clause. DU-002 slice 327.
				tgconstraint := "0"
				tgdeferrable := "f"
				tginitdeferred := "f"
				if trig.IsConstraint {
					tgconstraint = fmt.Sprintf("%d", trig.ConstraintOID)
					if trig.Deferrable {
						tgdeferrable = "t"
					}
					if trig.InitDeferred {
						tginitdeferred = "t"
					}
				}
				row[10] = tgconstraint   // tgconstraint
				row[11] = tgdeferrable   // tgdeferrable
				row[12] = tginitdeferred // tginitdeferred
				row[13] = fmt.Sprintf("%d", len(trig.Args)) // tgnargs
				// tgattr (int2vector→int2[], space-separated like pg_index.indkey):
				// the 1-based attnums of an `UPDATE OF col1, col2` column list, or
				// empty for every non-column-specific trigger. DU-002 slice 326.
				row[14] = triggerUpdateColAttrs(tbl, trig.UpdateColumns)
				row[15] = ""                              // tgargs (bytea; def built from catalog.Trigger.Args)
				row[16] = ""                              // tgqual (pg_node_tree; WHEN unsupported)
				// tgoldtable / tgnewtable — the REFERENCING transition-table names
				// (`OLD TABLE AS …` / `NEW TABLE AS …`), empty when absent. DU-002
				// slice 328.
				row[17] = trig.OldTransitionTable        // tgoldtable (name)
				row[18] = trig.NewTransitionTable        // tgnewtable (name)
				out = append(out, row)
			}
		}
		return out
	}
	c.tables["pg_catalog.pg_trigger"] = pgTrigger

	// pg_rewrite — rewrite-rule catalog (OID 2618). After getTriggers, pg_dump's
	// getRules dumps any ON SELECT/INSERT/UPDATE/DELETE rules. The query that
	// first hits this catalog is:
	//   SELECT tableoid, oid, rulename, ev_class AS ruletable, ev_type,
	//   is_instead, ev_enabled FROM pg_rewrite ORDER BY oid
	// goopg has no user-defined rules, so an empty view (0 rows) is correct,
	// identical to a stock PG cluster with none. (Stock PG does carry the
	// internal "_RETURN" SELECT rule per view in pg_rewrite, but goopg has no
	// stored user views feeding this dump path, so 0 rows matches what pg_dump
	// would emit for this cluster.) Schema matches PG's pg_rewrite
	// (pg_rewrite.h): oid, rulename name, ev_class oid, ev_type "char",
	// ev_enabled "char", is_instead bool, ev_qual pg_node_tree, ev_action
	// pg_node_tree. M0110-0001 (DU-002 slice 27).
	pgRewrite := &Table{
		Schema: "pg_catalog", Name: "pg_rewrite", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "rulename", Type: Type{Name: "name"}, Ordinal: 1},
			{Name: "ev_class", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "ev_type", Type: Type{Name: "char"}, Ordinal: 3},
			{Name: "ev_enabled", Type: Type{Name: "char"}, Ordinal: 4},
			{Name: "is_instead", Type: Type{Name: "bool"}, Ordinal: 5},
			{Name: "ev_qual", Type: Type{Name: "pg_node_tree"}, Ordinal: 6},
			{Name: "ev_action", Type: Type{Name: "pg_node_tree"}, Ordinal: 7},
		},
		OID: 2618,
	}
	// A CREATE RULE … [WHERE qual] DO [INSTEAD] NOTHING rule is recorded on its
	// table (catalog.Table.Rules) and projected here so pg_dump's getRules reads
	// it and dumpRule re-emits the CREATE RULE (via pg_get_ruledef, reconstructed
	// from the same RuleInfo, including the conditional WHERE — DU-002 slice 359).
	// getRules selects only oid/rulename/ev_class/ev_type/is_instead/ev_enabled,
	// so ev_qual/ev_action stay empty (→ SQL NULL) even for a conditional rule;
	// the rule text (with its WHERE) comes from the executor's pg_get_ruledef
	// handler, not from these columns. View _RETURN rules (ev_type '1') are still
	// absent — goopg has no stored user views feeding this dump path. DU-002
	// slice 324.
	pgRewrite.VirtualRows = func() [][]string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		var out [][]string
		for _, tbl := range c.tables {
			if tbl == nil || tbl.Virtual || tbl.OID == 0 || len(tbl.Rules) == 0 {
				continue
			}
			for _, r := range tbl.Rules {
				if r.OID == 0 {
					continue // predates OID tracking → invisible to pg_dump
				}
				evType := r.EvType()
				if evType == "" {
					continue
				}
				isInstead := "f"
				if r.Instead {
					isInstead = "t"
				}
				out = append(out, []string{
					fmt.Sprintf("%d", r.OID),   // oid
					r.Name,                     // rulename
					fmt.Sprintf("%d", tbl.OID), // ev_class
					evType,                     // ev_type
					string(r.EvEnabled()),      // ev_enabled ('O' default; ALTER TABLE … RULE sets D/R/A)
					isInstead,                  // is_instead
					"",                         // ev_qual  (NULL — unconditional rule)
					"",                         // ev_action (NULL — DO NOTHING)
				})
			}
		}
		return out
	}
	c.tables["pg_catalog.pg_rewrite"] = pgRewrite

	// pg_largeobject_metadata — large-object ownership/ACL catalog (OID 2995).
	// After getRules, pg_dump's getBlobs probes large objects with:
	//   SELECT oid, lomowner, lomacl, acldefault('L', lomowner) AS acldefault
	//   FROM pg_largeobject_metadata ORDER BY lomowner, lomacl::pg_catalog.text, oid
	// goopg has no large-object support, so an empty view (0 rows) is correct,
	// identical to a stock PG cluster with no large objects. Because the row set
	// is empty, the acldefault('L', lomowner) projection is never evaluated.
	// Schema matches PG's pg_largeobject_metadata (pg_largeobject_metadata.h):
	// oid, lomowner oid, lomacl aclitem[]. M0110-0001 (DU-002 slice 29).
	pgLargeobjectMetadata := &Table{
		Schema: "pg_catalog", Name: "pg_largeobject_metadata", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "lomowner", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "lomacl", Type: Type{Name: "aclitem[]"}, Ordinal: 2},
		},
		OID: 2995,
	}
	pgLargeobjectMetadata.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_largeobject_metadata"] = pgLargeobjectMetadata

	// pg_amop — access-method operator catalog (OID 2602). After getBlobs,
	// pg_dump's getDependencies issues a pg_depend UNION that joins both pg_amop
	// and pg_amproc to resolve operator-family member dependencies (so they are
	// not dumped as standalone objects); dumpOpclass/dumpOpfamily also query
	// this view directly for a class/family's own OPERATOR entries. A CREATE
	// OPERATOR CLASS AS-list OPERATOR entry naming a resolvable (user-defined)
	// operator registers a row here (catalog.AmOpMember, RegisterAmOpMember);
	// an unresolvable builtin-operator reference is silently dropped (goopg has
	// no builtin-operator catalog — deferred, see the ledger). Schema matches
	// PG's pg_amop (pg_amop.h): oid, amopfamily oid, amoplefttype oid,
	// amoprighttype oid, amopstrategy int2, amoppurpose "char", amopopr oid,
	// amopmethod oid, amopsortfamily oid. M0110-0001 (DU-002 slice 30; member
	// store added slice 411).
	pgAmop := &Table{
		Schema: "pg_catalog", Name: "pg_amop", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "amopfamily", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "amoplefttype", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "amoprighttype", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "amopstrategy", Type: Type{Name: "int2"}, Ordinal: 4},
			{Name: "amoppurpose", Type: Type{Name: "char"}, Ordinal: 5},
			{Name: "amopopr", Type: Type{Name: "oid"}, Ordinal: 6},
			{Name: "amopmethod", Type: Type{Name: "oid"}, Ordinal: 7},
			{Name: "amopsortfamily", Type: Type{Name: "oid"}, Ordinal: 8},
		},
		OID: 2602,
	}
	pgAmop.VirtualRows = func() [][]string {
		members := c.ListAmOpMembers()
		if len(members) == 0 {
			return nil
		}
		out := make([][]string, 0, len(members))
		for _, m := range members {
			// amoppurpose: AMOP_ORDER ('o') when the entry has a resolved
			// sort family, else AMOP_SEARCH ('s') — mirrors opclasscmds.c's
			// "oppurpose = OidIsValid(op->sortfamily) ? AMOP_ORDER :
			// AMOP_SEARCH". DU-002 (M0119-0004) slice 414.
			purpose := "s"
			if m.SortFamilyOID != 0 {
				purpose = "o"
			}
			out = append(out, []string{
				strconv.FormatUint(uint64(m.OID), 10),
				strconv.FormatUint(uint64(m.FamilyOID), 10),
				strconv.FormatUint(uint64(m.LeftType), 10),
				strconv.FormatUint(uint64(m.RightType), 10),
				strconv.FormatUint(uint64(m.Strategy), 10),
				purpose,
				strconv.FormatUint(uint64(m.OperOID), 10),
				strconv.FormatUint(uint64(m.Method), 10),
				strconv.FormatUint(uint64(m.SortFamilyOID), 10),
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_amop"] = pgAmop

	// pg_amproc — access-method support-procedure catalog (OID 2603). Joined
	// alongside pg_amop in the same getDependencies pg_depend UNION (see pg_amop
	// above), and by dumpOpclass/dumpOpfamily for a class/family's own FUNCTION
	// entries. A CREATE OPERATOR CLASS AS-list FUNCTION entry naming a
	// resolvable function registers a row here (catalog.AmProcMember,
	// RegisterAmProcMember). Schema matches PG's pg_amproc (pg_amproc.h): oid,
	// amprocfamily oid, amproclefttype oid, amprocrighttype oid, amprocnum
	// int2, amproc regproc. M0110-0001 (DU-002 slice 30; member store added
	// slice 411).
	pgAmproc := &Table{
		Schema: "pg_catalog", Name: "pg_amproc", Virtual: true,
		Columns: []Column{
			{Name: "oid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "amprocfamily", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "amproclefttype", Type: Type{Name: "oid"}, Ordinal: 2},
			{Name: "amprocrighttype", Type: Type{Name: "oid"}, Ordinal: 3},
			{Name: "amprocnum", Type: Type{Name: "int2"}, Ordinal: 4},
			{Name: "amproc", Type: Type{Name: "regproc"}, Ordinal: 5},
		},
		OID: 2603,
	}
	pgAmproc.VirtualRows = func() [][]string {
		members := c.ListAmProcMembers()
		if len(members) == 0 {
			return nil
		}
		out := make([][]string, 0, len(members))
		for _, m := range members {
			out = append(out, []string{
				strconv.FormatUint(uint64(m.OID), 10),
				strconv.FormatUint(uint64(m.FamilyOID), 10),
				strconv.FormatUint(uint64(m.LeftType), 10),
				strconv.FormatUint(uint64(m.RightType), 10),
				strconv.FormatUint(uint64(m.ProcNum), 10),
				strconv.FormatUint(uint64(m.ProcOID), 10),
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_amproc"] = pgAmproc

	// pg_seclabels — system view exposing security labels (a join over the
	// pg_seclabel + pg_shseclabel catalogs). After getDependencies, pg_dump's
	// getSecLabels issues:
	//   SELECT label, provider, classoid, objoid, objsubid
	//   FROM pg_catalog.pg_seclabels ORDER BY classoid, objoid, objsubid
	// to dump SECURITY LABEL statements. goopg supports no SECURITY LABEL, so an
	// empty view (0 rows) is correct, identical to a stock PG cluster with no
	// security labels. pg_seclabels is a VIEW, so it has no oid column; we register
	// it under an unused virtual OID (3597). The full upstream view schema also
	// exposes objtype/objnamespace/objname, included here for parity with
	// catalog-introspection queries. M0110-0001 (DU-002 slice 31).
	pgSeclabels := &Table{
		Schema: "pg_catalog", Name: "pg_seclabels", Virtual: true,
		Columns: []Column{
			{Name: "objoid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "classoid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "objsubid", Type: Type{Name: "int4"}, Ordinal: 2},
			{Name: "objtype", Type: Type{Name: "text"}, Ordinal: 3},
			{Name: "objnamespace", Type: Type{Name: "oid"}, Ordinal: 4},
			{Name: "objname", Type: Type{Name: "text"}, Ordinal: 5},
			{Name: "provider", Type: Type{Name: "text"}, Ordinal: 6},
			{Name: "label", Type: Type{Name: "text"}, Ordinal: 7},
		},
		OID: 3597,
	}
	pgSeclabels.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_seclabels"] = pgSeclabels

	// pg_shseclabel — the raw shared (cluster-wide) security-label catalog
	// (pg_shseclabel.h, SharedSecLabelRelationId 3592). dumpDatabase's
	// dumpSecLabel helper (pg_dump --create only) queries this base table
	// directly for the connected database's row rather than going through the
	// pg_seclabels view above (SELECT provider, label FROM pg_shseclabel WHERE
	// classoid = 'pg_database'::regclass AND objoid = <dboid>). goopg supports
	// no SECURITY LABEL, so an empty table (0 rows) is correct — identical to a
	// stock cluster with no shared security labels applied. M0119-0004-ACLHEAP
	// (datacl half).
	pgShseclabel := &Table{
		Schema: "pg_catalog", Name: "pg_shseclabel", Virtual: true,
		Columns: []Column{
			{Name: "classoid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "objoid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "provider", Type: Type{Name: "text"}, Ordinal: 2},
			{Name: "label", Type: Type{Name: "text"}, Ordinal: 3},
		},
		OID: 3592,
	}
	pgShseclabel.VirtualRows = func() [][]string { return nil }
	c.tables["pg_catalog.pg_shseclabel"] = pgShseclabel

	// pg_db_role_setting — per-database/per-role GUC override catalog
	// (pg_db_role_setting.h, DbRoleSettingRelationId 2964). dumpDatabaseConfig
	// (pg_dump.c, --create only) queries `SELECT unnest(setconfig) FROM
	// pg_db_role_setting WHERE setrole = 0 AND setdatabase = <dboid>` for
	// `ALTER DATABASE ... SET ...` clauses, and separately `SELECT rolname,
	// unnest(setconfig) FROM pg_db_role_setting s, pg_roles r WHERE setrole =
	// r.oid AND setdatabase = <dboid>` for `ALTER ROLE ... IN DATABASE ...
	// SET ...` clauses. `ALTER DATABASE ... SET`/`RESET` writes into
	// SetDatabaseConfig/ResetDatabaseConfig (setrole=0 rows, v0 scope: only
	// the live connected database, mirroring execDatabaseACLChange's datacl
	// restriction), keyed by FirstUserOID (16384) to match the oid pg_dump
	// already read from the pg_database row. `ALTER ROLE ... SET`/`RESET`
	// writes into SetRoleConfig/ResetRoleConfig (setrole != 0 rows), keyed by
	// the role's real OID and either 0 (cluster-wide) or the same
	// FirstUserOID (`IN DATABASE`, same v0 single-live-database scope
	// restriction). M0119-0004-ACLHEAP (ALTER DATABASE/ROLE ... SET
	// follow-up).
	pgDbRoleSetting := &Table{
		Schema: "pg_catalog", Name: "pg_db_role_setting", Virtual: true,
		Columns: []Column{
			{Name: "setdatabase", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "setrole", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "setconfig", Type: Type{Name: "text[]"}, Ordinal: 2},
		},
		OID: 2964,
	}
	pgDbRoleSetting.VirtualRows = func() [][]string {
		var out [][]string
		dbOid := FirstUserOID
		if entries := c.DatabaseConfigEntries(dbOid); len(entries) > 0 {
			out = append(out, []string{
				strconv.FormatUint(uint64(dbOid), 10),
				"0",
				optionsArrayLiteral(entries),
			})
		}
		// setrole != 0 rows: `ALTER ROLE ... [IN DATABASE ...] SET ...`
		// (M0119-0004-ACLHEAP, ALTER ROLE ... SET follow-up). DBOid is 0 for
		// a plain cluster-wide override (setdatabase=0) or FirstUserOID for
		// the IN DATABASE form.
		for _, row := range c.AllRoleConfigRows() {
			out = append(out, []string{
				strconv.FormatUint(uint64(row.DBOid), 10),
				strconv.FormatUint(uint64(row.RoleOID), 10),
				optionsArrayLiteral(row.Entries),
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_db_role_setting"] = pgDbRoleSetting

	// pg_sequence — per-sequence parameter catalog (OID 2224, one row per
	// sequence relation). After getTables, pg_dump's getSequences issues:
	//   SELECT seqrelid, format_type(seqtypid, NULL), seqstart, seqincrement,
	//     seqmax, seqmin, seqcache, seqcycle, last_value, is_called
	//   FROM pg_catalog.pg_sequence, pg_get_sequence_data(seqrelid)
	//   ORDER BY seqrelid
	// (an implicit-LATERAL comma join with the set-returning function
	// pg_get_sequence_data, which supplies the runtime last_value/is_called).
	// As of DU-002 slice 116 the pg_class VirtualRows builder surfaces each
	// IsSequence relation as relkind='S' (relam=0), so pg_dump's getTables
	// discovers it and dumps it via dumpSequence; this pg_sequence row plus the
	// pg_get_sequence_data SRF (slice 115) supply the CREATE SEQUENCE parameters
	// and the trailing setval(). pg_sequence is a real catalog with
	// no `oid` system column; cols match pg_sequence.h (PG18, SequenceRelationId
	// 2224): seqrelid oid, seqtypid oid, seqstart int8, seqincrement int8,
	// seqmax int8, seqmin int8, seqcache int8, seqcycle bool. M0110-0001
	// (DU-002 slice 32). pg_get_sequence_data is registered as a FROM-clause SRF
	// in internal/planner/planner.go.
	pgSequence := &Table{
		Schema: "pg_catalog", Name: "pg_sequence", Virtual: true,
		Columns: []Column{
			{Name: "seqrelid", Type: Type{Name: "oid"}, Ordinal: 0},
			{Name: "seqtypid", Type: Type{Name: "oid"}, Ordinal: 1},
			{Name: "seqstart", Type: Type{Name: "int8"}, Ordinal: 2},
			{Name: "seqincrement", Type: Type{Name: "int8"}, Ordinal: 3},
			{Name: "seqmax", Type: Type{Name: "int8"}, Ordinal: 4},
			{Name: "seqmin", Type: Type{Name: "int8"}, Ordinal: 5},
			{Name: "seqcache", Type: Type{Name: "int8"}, Ordinal: 6},
			{Name: "seqcycle", Type: Type{Name: "bool"}, Ordinal: 7},
		},
		OID: 2224,
	}
	// One row per sequence relation, keyed by the sequence's pg_class OID
	// (seqrelid). The OID lives on the sequence's virtual catalog table
	// (IsSequence); the per-sequence parameters come from the executor's
	// registry via SequenceParamsFunc. pg_dump's getSequences comma-joins this
	// with pg_get_sequence_data(seqrelid). M0110-0001 (DU-002 slice 115).
	pgSequence.VirtualRows = func() [][]string {
		if SequenceParamsFunc == nil {
			return nil
		}
		c.mu.RLock()
		defer c.mu.RUnlock()
		keys := make([]string, 0, len(c.tables))
		for k, t := range c.tables {
			if t.IsSequence {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		rows := make([][]string, 0, len(keys))
		for _, k := range keys {
			t := c.tables[k]
			schema := t.Schema
			if schema == "" {
				schema = "public"
			}
			p, ok := SequenceParamsFunc(schema + "." + t.Name)
			if !ok {
				continue
			}
			cyc := "f"
			if p.Cycle {
				cyc = "t"
			}
			rows = append(rows, []string{
				strconv.Itoa(int(t.OID)),           // 0: seqrelid
				strconv.Itoa(int(p.TypeOID)),       // 1: seqtypid
				strconv.FormatInt(p.Start, 10),     // 2: seqstart
				strconv.FormatInt(p.Increment, 10), // 3: seqincrement
				strconv.FormatInt(p.Max, 10),       // 4: seqmax
				strconv.FormatInt(p.Min, 10),       // 5: seqmin
				strconv.FormatInt(p.Cache, 10),     // 6: seqcache
				cyc,                                // 7: seqcycle
			})
		}
		return rows
	}
	c.tables["pg_catalog.pg_sequence"] = pgSequence

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
	idx, _, ok := c.lookupIndexLocked(name)
	return idx, ok
}

// lookupIndexLocked is the shared name-resolution core behind LookupIndex,
// RenameIndex, and RenameIndexDuringRecovery — callers must already hold
// c.mu. Returns the found index, the actual map key it lives under (which
// may differ from key(name) — see the fallback below), and whether it was
// found.
//
// The "" vs "public." ambiguity this resolves: a live DDL session stores an
// unqualified index under the bare-name key (CREATE TABLE only sets
// tbl.Schema when the writable search_path schema isn't "public", so a
// same-session CreateIndex/RenameIndex/etc. consistently keys off ""); a
// server restart's pg_index-heap reload (loadUserIndexesFromHeap, M0113)
// resolves the namespace OID to the explicit string "public" instead. A
// rename/lookup issued before vs. after a restart can therefore target the
// same logical index under two different literal keys.
func (c *InMemory) lookupIndexLocked(name parser.ObjectName) (*Index, string, bool) {
	if idx, ok := c.indexes[key(name)]; ok {
		return idx, key(name), ok
	}
	if name.Schema == "" {
		// Unqualified name: try "public.<name>" first (indexes created via DDL
		// always carry the table's schema, which defaults to "public").
		if idx, ok := c.indexes["public."+name.Name]; ok {
			return idx, "public." + name.Name, ok
		}
	} else {
		// Schema-qualified lookup failed: fall back to bare name for indexes
		// created without an explicit schema in the catalog key.
		if idx, ok := c.indexes[name.Name]; ok {
			return idx, name.Name, ok
		}
	}
	return nil, "", false
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

// RenameIndex renames a catalog index entry from old to new, re-keying both
// `c.indexes` and the owning table's `c.byTable` slot (mirrors RenameTable's
// shape, DU-002 slice 443). Returns an error when old does not exist or new
// already exists — real PostgreSQL raises 42P07 for the latter
// (RenameRelation -> RangeVarCallbackForAlterRelation's namespace check).
func (c *InMemory) RenameIndex(old, new parser.ObjectName) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, oldK, exists := c.lookupIndexLocked(old)
	if !exists {
		return fmt.Errorf("relation %q does not exist", key(old))
	}
	newK := key(parser.ObjectName{Schema: idx.Schema, Name: new.Name})
	if _, _, collides := c.lookupIndexLocked(parser.ObjectName{Schema: idx.Schema, Name: new.Name}); collides {
		return fmt.Errorf("relation %q already exists", newK)
	}
	idx.Name = new.Name
	c.indexes[newK] = idx
	delete(c.indexes, oldK)
	if idx.Table != nil {
		if perTable := c.byTable[idx.Table.OID]; perTable != nil {
			delete(perTable, oldK)
			perTable[newK] = idx
		}
	}
	return nil
}

// RenameIndexDuringRecovery is the idempotent WAL-replay counterpart to
// RenameIndex, mirroring RegisterIndexDuringRecovery's shape. A missing old
// entry (already renamed by a prior pass, or a JSON snapshot that captured
// the post-rename state) is a silent no-op rather than an error. Uses
// lookupIndexLocked (not a bare key(schema, oldName) lookup) because the
// WAL record's schema reflects the live session's "" convention while a
// prior M0113 pg_index-heap reload may have re-keyed the same index under
// the resolved "public." form — see lookupIndexLocked's doc comment.
func (c *InMemory) RenameIndexDuringRecovery(schema, oldName, newName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, oldK, exists := c.lookupIndexLocked(parser.ObjectName{Schema: schema, Name: oldName})
	if !exists {
		return
	}
	newK := key(parser.ObjectName{Schema: idx.Schema, Name: newName})
	idx.Name = newName
	c.indexes[newK] = idx
	delete(c.indexes, oldK)
	if idx.Table != nil {
		if perTable := c.byTable[idx.Table.OID]; perTable != nil {
			delete(perTable, oldK)
			perTable[newK] = idx
		}
	}
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

// UpdateRelStats publishes VACUUM's pg_class.reltuples / relpages counters
// (vac_update_relstats) without discarding any per-column pg_statistic from a
// prior ANALYZE. Plain VACUUM recomputes the live-tuple count and block count
// but does not sample column distributions, so it must merge into — not replace
// — the existing Stats struct (mirrors upstream, where VACUUM and ANALYZE both
// call vac_update_relstats but only ANALYZE rewrites pg_statistic). A nil Stats
// is created on first VACUUM so pg_class surfaces a non-zero reltuples even
// before the table has ever been ANALYZEd.
func (c *InMemory) UpdateRelStats(table *Table, pages int, tuples int64) {
	if table == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if table.Stats == nil {
		table.Stats = &TableStats{Pages: pages, RowCount: tuples}
		return
	}
	// Pointer-replace so a concurrent reader never sees a torn struct.
	merged := *table.Stats
	merged.Pages = pages
	merged.RowCount = tuples
	table.Stats = &merged
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

// SchemaNameForOID returns the registered schema name for the given namespace
// OID ("" if no schema carries that OID). It is the reverse of SchemaOID and is
// used by the restart recovery path (loadUserTablesFromHeap /
// loadUserIndexesFromHeap) to reconstruct a user table's schema from the
// pg_class.relnamespace OID recovered from the heap. Pre-populated system
// schemas (pg_catalog/public) are handled by their fixed OIDs at the call site;
// this resolves user schemas registered via CREATE SCHEMA. M0110-0003.
func (c *InMemory) SchemaNameForOID(oid uint32) string {
	if oid == 0 {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for name, o := range c.schemas {
		if o == oid {
			return name
		}
	}
	return ""
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

// SchemaOwnerOID returns the owning role OID recorded for the given schema
// (ALTER SCHEMA ... OWNER TO), or the bootstrap superuser (10) — the
// long-standing pg_namespace.nspowner default — when no explicit owner
// change has been recorded. DU-002 slice 440 resume point (3) (M0110-0001).
func (c *InMemory) SchemaOwnerOID(name string) uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if oid, ok := c.schemaOwners[strings.ToLower(name)]; ok {
		return oid
	}
	return 10
}

// SetSchemaOwner records the owning role OID for a schema (ALTER SCHEMA ...
// OWNER TO). Returns false if the schema does not exist. DU-002 slice 440
// resume point (3) (M0110-0001).
func (c *InMemory) SetSchemaOwner(name string, ownerOID uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	lc := strings.ToLower(name)
	if _, ok := c.schemas[lc]; !ok {
		return false
	}
	c.schemaOwners[lc] = ownerOID
	return true
}

// SetSchemaOwnerDuringRecovery is the discard-result recovery counterpart to
// SetSchemaOwner, mirroring SetStatisticsOwnerDuringRecovery.
func (c *InMemory) SetSchemaOwnerDuringRecovery(name string, ownerOID uint32) {
	c.SetSchemaOwner(name, ownerOID)
}

// RenameSchema renames a user schema (ALTER SCHEMA ... RENAME TO). Real
// PostgreSQL's namespace rename is a single pg_namespace row update because
// every other catalog references a schema by OID (relnamespace); goopg's
// Table/Index catalog instead keys directly by schema NAME (see key()), so a
// schema rename must cascade into every table/view/sequence/index/operator
// class/statistics object whose Schema field names the old schema, re-keying
// their map entries too. Returns the tables that were sequences (so the
// caller can additionally cascade the executor-side seqRegistry, mirroring
// the SET SCHEMA-on-single-sequence cascade in execAlterTable) and an error
// if old does not exist or new already exists (mirrors upstream
// RenameSchema, postgres/src/backend/commands/schemacmds.c). DU-002 slice 440
// resume point (3) (M0110-0001); the opClassSchemas/statisticsObjs cascades
// close the slice-440-resume-point(3) ledger row's own follow-up (schema-
// name-keyed registries left un-cascaded). Still not audited/cascaded here:
// userCollations/userConversions (carry a Schema field but aren't in a
// schema-keyed map, so likely unaffected — not verified) and any
// schema-qualified function/type/domain registries.
func (c *InMemory) RenameSchema(old, new string) ([]*Table, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lcOld := strings.ToLower(old)
	lcNew := strings.ToLower(new)
	oid, ok := c.schemas[lcOld]
	if !ok {
		return nil, fmt.Errorf("schema %q does not exist", old)
	}
	if _, exists := c.schemas[lcNew]; exists {
		return nil, fmt.Errorf("schema %q already exists", new)
	}
	delete(c.schemas, lcOld)
	c.schemas[lcNew] = oid
	if owner, ok := c.schemaOwners[lcOld]; ok {
		delete(c.schemaOwners, lcOld)
		c.schemaOwners[lcNew] = owner
	}
	var movedSequences []*Table
	for k, tbl := range c.tables {
		if !strings.EqualFold(tbl.Schema, old) {
			continue
		}
		tbl.Schema = new
		newK := key(parser.ObjectName{Schema: new, Name: tbl.Name})
		if newK != k {
			delete(c.tables, k)
			c.tables[newK] = tbl
		}
		if tbl.IsSequence {
			movedSequences = append(movedSequences, tbl)
		}
	}
	for k, idx := range c.indexes {
		if !strings.EqualFold(idx.Schema, old) {
			continue
		}
		idx.Schema = new
		newK := key(parser.ObjectName{Schema: new, Name: idx.Name})
		if newK != k {
			delete(c.indexes, k)
			c.indexes[newK] = idx
		}
	}
	for name, schema := range c.opClassSchemas {
		if strings.EqualFold(schema, old) {
			c.opClassSchemas[name] = new
		}
	}
	for k, obj := range c.statisticsObjs {
		objSchema := obj.Schema
		if objSchema == "" {
			objSchema = "public"
		}
		if !strings.EqualFold(objSchema, old) {
			continue
		}
		obj.Schema = new
		newK := obj.qualifiedKey()
		if newK != k {
			delete(c.statisticsObjs, k)
			c.statisticsObjs[newK] = obj
		}
	}
	return movedSequences, nil
}

// RenameSchemaDuringRecovery is the idempotent, discard-result recovery
// counterpart to RenameSchema used by the WAL-replay driver. Errors (e.g. a
// stale record replaying after a later drop) are swallowed, mirroring
// RenameStatisticsObjectDuringRecovery.
func (c *InMemory) RenameSchemaDuringRecovery(old, new string) {
	_, _ = c.RenameSchema(old, new)
}

// RegisterSchemaDuringRecovery is the idempotent version of RegisterSchema
// used by the WAL-replay driver. Unlike RegisterSchema it takes the OID from
// the WAL record (so the recovered registry matches what the pre-crash server
// assigned) and advances nextOID past it so subsequent allocations do not
// collide. Re-applying a record whose schema already exists is a no-op.
// Mirrors RegisterDatabaseDuringRecovery (M0054-0001) / RegisterIndexDuringRecovery.
func (c *InMemory) RegisterSchemaDuringRecovery(name string, oid uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lc := strings.ToLower(name)
	c.schemas[lc] = oid
	if oid >= c.nextOID {
		c.nextOID = oid + 1
	}
}

// UnregisterSchemaDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropSchema.
func (c *InMemory) UnregisterSchemaDuringRecovery(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.schemas, strings.ToLower(name))
}

// RegisterTransformDuringRecovery is the idempotent version of
// RegisterTransform used by the WAL-replay driver
// (internal/initdb/transform_ddl_recovery.go). Unlike RegisterTransform it
// takes the OID from the WAL record (so the recovered registry matches what
// the pre-crash server assigned) and advances nextOID past it so subsequent
// allocations do not collide. Re-applying a record for a transform that
// already exists just refreshes its fields. Mirrors
// RegisterSchemaDuringRecovery. DU-002 (M0119-0004) restart persistence.
func (c *InMemory) RegisterTransformDuringRecovery(typeName, lang string, oid, fromFuncOID, toFuncOID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transforms == nil {
		c.transforms = make(map[string]*Transform)
	}
	key := strings.ToLower(typeName) + "\x00" + strings.ToLower(lang)
	c.transforms[key] = &Transform{OID: oid, TypeName: typeName, Lang: lang, FromFuncOID: fromFuncOID, ToFuncOID: toFuncOID}
	if oid >= c.nextOID {
		c.nextOID = oid + 1
	}
}

// DropTransformDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropTransform. DU-002 (M0119-0004) restart persistence.
func (c *InMemory) DropTransformDuringRecovery(typeName, lang string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transforms == nil {
		return
	}
	delete(c.transforms, strings.ToLower(typeName)+"\x00"+strings.ToLower(lang))
}

// RegisterCastDuringRecovery is the idempotent version of RegisterCast used
// by the WAL-replay driver (internal/initdb/cast_ddl_recovery.go). Unlike
// RegisterCast it takes the OID from the WAL record (so the recovered
// registry matches what the pre-crash server assigned) and advances nextOID
// past it so subsequent allocations do not collide. Re-applying a record for
// a cast that already exists just refreshes its fields. Mirrors
// RegisterTransformDuringRecovery. DU-002 restart-persistence follow-up.
func (c *InMemory) RegisterCastDuringRecovery(source, target, context, method string, oid, funcOID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.casts == nil {
		c.casts = make(map[string]*Cast)
	}
	key := castKey(source, target)
	c.casts[key] = &Cast{OID: oid, SourceType: source, TargetType: target, Context: context, Method: method, FuncOID: funcOID}
	if oid >= c.nextOID {
		c.nextOID = oid + 1
	}
}

// DropCastDuringRecovery is the idempotent counterpart used for replaying
// RecordKindDropCast. DU-002 restart-persistence follow-up.
func (c *InMemory) DropCastDuringRecovery(source, target string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.casts == nil {
		return
	}
	delete(c.casts, castKey(source, target))
}

// CreateExtension records a CREATE EXTENSION install in the runtime
// pg_extension registry. Called from the executor's execCreateExtension after
// it has validated the extension name and resolved the default version/schema.
// M0110-0003.
func (c *InMemory) CreateExtension(name, schema, version, database string, ifNotExists bool) error {
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
		oid:      c.nextOID,
		name:     name,
		schema:   schema,
		version:  version,
		database: database,
	}
	return nil
}

// ExtensionOID returns the runtime pg_extension OID for the named extension, or
// 0 if no extension by that name is installed. Used by COMMENT ON EXTENSION to
// key the pg_description row on the extension's catalog OID (classoid 3079) so
// pg_dump's dumpExtension can re-emit the comment. DU-002 slice 388.
func (c *InMemory) ExtensionOID(name string) uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.extensions[strings.ToLower(name)]; ok {
		return e.oid
	}
	return 0
}

// CreateCollation records a CREATE COLLATION in the runtime pg_collation
// registry so pg_dump's getCollations / dumpCollation re-emit it. `schema` is
// the (already-resolved) schema name the collation lives in; an unknown schema
// resolves to the public namespace OID. The collation is keyed by its OID and
// surfaced as an extra virtual pg_collation row. Returns the new OID, or 0 with
// an error if a same-named collation already exists in the same namespace and
// ifNotExists is false. M0119-0004.
func (c *InMemory) CreateCollation(uc *UserCollation, schema string, ifNotExists bool) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, existing := range c.userCollations {
		if existing.NamespaceOID == nsOID && strings.EqualFold(existing.Name, uc.Name) {
			if ifNotExists {
				return existing.OID, nil
			}
			return 0, fmt.Errorf("collation %q already exists", uc.Name)
		}
	}
	uc.OID = c.allocOIDLocked()
	uc.NamespaceOID = nsOID
	c.userCollations = append(c.userCollations, uc)
	return uc.OID, nil
}

// DropCollation removes the user-created collation with the given bare name in
// the given schema from the registry. Returns true if one was found and
// removed. `schema` resolves like CreateCollation (unknown → public). Built-in
// collations are never registered in userCollations, so a DROP COLLATION on
// one of them always returns false (mirrors PG, which also refuses to drop a
// pinned pg_collation row). M0119-0004.
func (c *InMemory) DropCollation(name, schema string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for i, uc := range c.userCollations {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			c.userCollations = append(c.userCollations[:i], c.userCollations[i+1:]...)
			return true
		}
	}
	return false
}

// RenameCollation renames a user-created collation with the given bare name
// in the given schema to newName. Returns an error if the source collation
// does not exist (not found in userCollations — built-in collations are never
// registered there, mirroring DropCollation's refusal to touch them) or a
// collation named newName already exists in the same namespace. `schema`
// resolves like CreateCollation (unknown → public). M0119-0004 (DU-002,
// loop #50 ledger follow-up).
func (c *InMemory) RenameCollation(name, schema, newName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	var target *UserCollation
	for _, uc := range c.userCollations {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			target = uc
			continue
		}
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, newName) {
			return fmt.Errorf("collation %q already exists", newName)
		}
	}
	if target == nil {
		return fmt.Errorf("collation %q does not exist", name)
	}
	target.Name = newName
	return nil
}

// SetCollationOwner sets the owning role OID of a user-created collation with
// the given bare name in the given schema. Returns false if no such collation
// is registered (mirrors RenameCollation/DropCollation). M0119-0004 (DU-002,
// loop #50 ledger follow-up).
func (c *InMemory) SetCollationOwner(name, schema string, ownerOID uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, uc := range c.userCollations {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			uc.Owner = ownerOID
			return true
		}
	}
	return false
}

// SetCollationSchema moves a user-created collation with the given bare name
// from `schema` into `newSchema` (SET SCHEMA), resolving both schema names to
// their namespace OID the same way SetCollationOwner/RenameCollation do
// (unknown → public). Returns false if no such collation is registered.
// DU-002 slice 442.
func (c *InMemory) SetCollationSchema(name, schema, newSchema string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	newNsOID := c.schemas[strings.ToLower(newSchema)]
	if newNsOID == 0 {
		newNsOID = c.schemas["public"]
	}
	for _, uc := range c.userCollations {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			uc.NamespaceOID = newNsOID
			return true
		}
	}
	return false
}

// CreateCollationDuringRecovery is the idempotent version of CreateCollation
// used by the WAL-replay driver (internal/initdb/collation_ddl_recovery.go).
// Unlike CreateCollation it takes the OID from the WAL record (so the
// recovered collation matches the pre-crash OID exactly) and overwrites
// rather than erroring when an entry with the same OID is already present
// (replay may see the same record more than once across a partial-then-full
// replay). Mirrors CreateConversionDuringRecovery. DU-002 restart-persistence
// follow-up.
func (c *InMemory) CreateCollationDuringRecovery(uc *UserCollation, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	uc.NamespaceOID = nsOID
	for i, existing := range c.userCollations {
		if existing.OID == uc.OID {
			c.userCollations[i] = uc
			if uc.OID >= c.nextOID {
				c.nextOID = uc.OID + 1
			}
			return
		}
	}
	c.userCollations = append(c.userCollations, uc)
	if uc.OID >= c.nextOID {
		c.nextOID = uc.OID + 1
	}
}

// DropCollationDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropCollation. Identical to DropCollation but discards
// the found/not-found result — replay does not care whether the collation was
// still present (a subsequent CREATE with the same name after a DROP is a
// valid sequence to replay in order). DU-002 restart-persistence follow-up.
func (c *InMemory) DropCollationDuringRecovery(name, schema string) {
	c.DropCollation(name, schema)
}

// RenameCollationDuringRecovery is the discard-result recovery counterpart to
// RenameCollation, mirroring DropCollationDuringRecovery. A rename record can
// only be replayed after its collation's CREATE COLLATION record (WAL is
// scanned in order), so a not-found error here is not expected in practice,
// but replay must not abort on it — the same "don't care if it was still
// there" tolerance DropCollationDuringRecovery documents. DU-002
// restart-persistence follow-up (M0119-0004).
func (c *InMemory) RenameCollationDuringRecovery(name, schema, newName string) {
	_ = c.RenameCollation(name, schema, newName)
}

// SetCollationOwnerDuringRecovery is the discard-result recovery counterpart
// to SetCollationOwner, mirroring DropCollationDuringRecovery. DU-002
// restart-persistence follow-up (M0119-0004).
func (c *InMemory) SetCollationOwnerDuringRecovery(name, schema string, ownerOID uint32) {
	c.SetCollationOwner(name, schema, ownerOID)
}

// SetCollationSchemaDuringRecovery is the discard-result recovery counterpart
// to SetCollationSchema, mirroring SetCollationOwnerDuringRecovery. DU-002
// slice 442.
func (c *InMemory) SetCollationSchemaDuringRecovery(name, schema, newSchema string) {
	c.SetCollationSchema(name, schema, newSchema)
}

// CreateConversion records a CREATE [DEFAULT] CONVERSION in the runtime
// pg_conversion registry so pg_dump's getConversions / dumpConversion re-emit
// it. `schema` is the (already-resolved) schema name the conversion lives in; an
// unknown schema resolves to the public namespace OID. The conversion is keyed
// by its OID and surfaced as an extra virtual pg_conversion row. Returns the new
// OID, or 0 with an error if a same-named conversion already exists in the same
// namespace (PG enforces a unique (conname, connamespace)). DU-002 slice 399.
func (c *InMemory) CreateConversion(uc *UserConversion, schema string) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, existing := range c.userConversions {
		if existing.NamespaceOID == nsOID && strings.EqualFold(existing.Name, uc.Name) {
			return 0, fmt.Errorf("conversion %q already exists", uc.Name)
		}
	}
	uc.OID = c.allocOIDLocked()
	uc.NamespaceOID = nsOID
	c.userConversions = append(c.userConversions, uc)
	return uc.OID, nil
}

// DropConversion removes the user-created conversion with the given bare name in
// the given schema from the registry. Returns true if one was found and removed.
// `schema` resolves like CreateConversion (unknown → public). DU-002 slice 399.
func (c *InMemory) DropConversion(name, schema string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for i, uc := range c.userConversions {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			c.userConversions = append(c.userConversions[:i], c.userConversions[i+1:]...)
			return true
		}
	}
	return false
}

// CreateConversionDuringRecovery is the idempotent version of CreateConversion
// used by the WAL-replay driver (internal/initdb/conversion_ddl_recovery.go).
// Unlike CreateConversion it takes the OID from the WAL record (so the
// recovered conversion matches the pre-crash OID exactly) and overwrites
// rather than erroring when an entry with the same OID is already present
// (replay may see the same record more than once across a partial-then-full
// replay). Mirrors RegisterCastDuringRecovery / RegisterTransformDuringRecovery.
// DU-002 restart-persistence follow-up.
func (c *InMemory) CreateConversionDuringRecovery(uc *UserConversion, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	uc.NamespaceOID = nsOID
	for i, existing := range c.userConversions {
		if existing.OID == uc.OID {
			c.userConversions[i] = uc
			if uc.OID >= c.nextOID {
				c.nextOID = uc.OID + 1
			}
			return
		}
	}
	c.userConversions = append(c.userConversions, uc)
	if uc.OID >= c.nextOID {
		c.nextOID = uc.OID + 1
	}
}

// DropConversionDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropConversion. Identical to DropConversion but
// discards the found/not-found result — replay does not care whether the
// conversion was still present (a subsequent CREATE with the same name after
// a DROP is a valid sequence to replay in order). DU-002 restart-persistence
// follow-up.
func (c *InMemory) DropConversionDuringRecovery(name, schema string) {
	c.DropConversion(name, schema)
}

// ListUserConversions returns the user-created conversions in creation order.
// DU-002 slice 399.
func (c *InMemory) ListUserConversions() []*UserConversion {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.userConversions) == 0 {
		return nil
	}
	out := make([]*UserConversion, len(c.userConversions))
	copy(out, c.userConversions)
	return out
}

// CreateTSDict records a CREATE TEXT SEARCH DICTIONARY in the runtime
// pg_ts_dict registry so pg_dump's getTSDictionaries / dumpTSDictionary
// re-emit it. `schema` is the (already-resolved) schema name the dictionary
// lives in; an unknown schema resolves to the public namespace OID. Returns
// the new OID, or 0 with an error if a same-named dictionary already exists in
// the same namespace (PG enforces a unique (dictname, dictnamespace)). DU-002
// slice 437 (M0119-0004).
func (c *InMemory) CreateTSDict(ud *UserTSDict, schema string) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, existing := range c.userTSDicts {
		if existing.NamespaceOID == nsOID && strings.EqualFold(existing.Name, ud.Name) {
			return 0, fmt.Errorf("text search dictionary %q already exists", ud.Name)
		}
	}
	ud.OID = c.allocOIDLocked()
	ud.NamespaceOID = nsOID
	c.userTSDicts = append(c.userTSDicts, ud)
	return ud.OID, nil
}

// DropTSDict removes the user-created text search dictionary with the given
// bare name in the given schema from the registry. Returns true if one was
// found and removed. `schema` resolves like CreateTSDict (unknown → public).
// DU-002 slice 437.
func (c *InMemory) DropTSDict(name, schema string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for i, ud := range c.userTSDicts {
		if ud.NamespaceOID == nsOID && strings.EqualFold(ud.Name, name) {
			c.userTSDicts = append(c.userTSDicts[:i], c.userTSDicts[i+1:]...)
			return true
		}
	}
	return false
}

// ListUserTSDicts returns the user-created text search dictionaries in
// creation order. DU-002 slice 437.
func (c *InMemory) ListUserTSDicts() []*UserTSDict {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.userTSDicts) == 0 {
		return nil
	}
	out := make([]*UserTSDict, len(c.userTSDicts))
	copy(out, c.userTSDicts)
	return out
}

// CreateTSDictDuringRecovery is the idempotent version of CreateTSDict used
// by the WAL-replay driver (internal/initdb/tsdict_ddl_recovery.go). Unlike
// CreateTSDict it takes the OID from the WAL record (so the recovered
// dictionary matches the pre-crash OID exactly) and overwrites rather than
// erroring when an entry with the same OID is already present (replay may
// see the same record more than once across a partial-then-full replay).
// Mirrors CreateConversionDuringRecovery. DU-002 restart-persistence
// follow-up to slice 437.
func (c *InMemory) CreateTSDictDuringRecovery(ud *UserTSDict, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	ud.NamespaceOID = nsOID
	for i, existing := range c.userTSDicts {
		if existing.OID == ud.OID {
			c.userTSDicts[i] = ud
			if ud.OID >= c.nextOID {
				c.nextOID = ud.OID + 1
			}
			return
		}
	}
	c.userTSDicts = append(c.userTSDicts, ud)
	if ud.OID >= c.nextOID {
		c.nextOID = ud.OID + 1
	}
}

// DropTSDictDuringRecovery is the idempotent counterpart used for replaying
// RecordKindDropTSDict. Identical to DropTSDict but discards the
// found/not-found result — replay does not care whether the dictionary was
// still present. DU-002 restart-persistence follow-up to slice 437.
func (c *InMemory) DropTSDictDuringRecovery(name, schema string) {
	c.DropTSDict(name, schema)
}

// SerializeTSDictOptions reconstructs a text search dictionary's
// dictinitoption text exactly as PostgreSQL's serialize_deflist
// (tsearchcmds.c) does: each option is rendered `"key" = <val>` (the key
// quote_identifier'd — see pgQuoteIdent in the executor package, applied by
// the caller since it lives there), entries joined by ", ", and the value
// either emitted bare (an integer/numeric literal — TSDictOption.IsNumeric)
// or single-quoted with embedded quotes doubled (every other value,
// matching PG's SQL_STR_DOUBLE escaping). Returns "" (NULL dictinitoption)
// when there are no options. Shared by CREATE TEXT SEARCH DICTIONARY and
// ALTER TEXT SEARCH DICTIONARY's option-merge form (AlterTSDictOptions
// below) so both round-trip through the identical serialized form. DU-002
// slice 437; moved here from the executor package as an ALTER TEXT SEARCH
// DICTIONARY follow-up (M0119-0004) so AlterTSDictOptions can call it
// without an executor->catalog import cycle.
func SerializeTSDictOptions(opts []parser.TSDictOption) string {
	if len(opts) == 0 {
		return ""
	}
	var buf strings.Builder
	for i, opt := range opts {
		if i > 0 {
			buf.WriteString(", ")
		}
		buf.WriteString(pgQuoteIdentForTSDict(opt.Key))
		buf.WriteString(" = ")
		if opt.IsNumeric {
			buf.WriteString(opt.Value)
			continue
		}
		buf.WriteByte('\'')
		buf.WriteString(strings.ReplaceAll(opt.Value, "'", "''"))
		buf.WriteByte('\'')
	}
	return buf.String()
}

// pgQuoteIdentForTSDict mirrors the executor package's own pgQuoteIdent
// (expr.go) byte-for-byte — quote_identifier(): unquoted only when the
// identifier is all-lowercase letters/digits/underscore starting with a
// letter/underscore AND not a reserved keyword (sqlkeywords.
// IsReservedForQuoting), otherwise double-quoted with embedded quotes
// doubled. Duplicated here (rather than exported from the executor package)
// because catalog cannot import executor (executor already imports
// catalog); kept byte-identical to expr.go's pgQuoteIdent so
// SerializeTSDictOptions's output is unchanged from before this function
// moved from the executor package. DU-002 ALTER TEXT SEARCH DICTIONARY
// follow-up (M0119-0004).
func pgQuoteIdentForTSDict(s string) string {
	if s == "" {
		return `""`
	}
	safe := true
	for i, c := range s {
		if i == 0 {
			if !((c >= 'a' && c <= 'z') || c == '_') {
				safe = false
				break
			}
		} else {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
				safe = false
				break
			}
		}
	}
	if safe && sqlkeywords.IsReservedForQuoting(s) {
		safe = false
	}
	if safe {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// DeserializeTSDictOptions is the inverse of SerializeTSDictOptions,
// mirroring deserialize_deflist (tsearchcmds.c): splits a comma-separated
// `"key" = 'value'` / `"key" = 42` list back into structured options.
// InitOption is the only place a dictionary's options are stored (there is
// no parallel structured field on UserTSDict), so ALTER TEXT SEARCH
// DICTIONARY's (key[=value],...) merge form (AlterTSDictOptions below) must
// round-trip through this to remove/replace individual keys without
// discarding the rest. DU-002 ALTER TEXT SEARCH DICTIONARY follow-up
// (M0119-0004).
func DeserializeTSDictOptions(s string) []parser.TSDictOption {
	if s == "" {
		return nil
	}
	var out []parser.TSDictOption
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i >= len(s) {
			break
		}
		var key string
		if s[i] == '"' {
			j := i + 1
			var b strings.Builder
			for j < len(s) {
				if s[j] == '"' {
					if j+1 < len(s) && s[j+1] == '"' {
						b.WriteByte('"')
						j += 2
						continue
					}
					break
				}
				b.WriteByte(s[j])
				j++
			}
			key = b.String()
			i = j + 1
		} else {
			j := i
			for j < len(s) && s[j] != ' ' && s[j] != '=' {
				j++
			}
			key = s[i:j]
			i = j
		}
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i < len(s) && s[i] == '=' {
			i++
		}
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			out = append(out, parser.TSDictOption{Key: key, HasValue: true})
			break
		}
		if s[i] == '\'' {
			j := i + 1
			var b strings.Builder
			for j < len(s) {
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						b.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				b.WriteByte(s[j])
				j++
			}
			out = append(out, parser.TSDictOption{Key: key, Value: b.String(), HasValue: true})
			i = j + 1
		} else {
			j := i
			for j < len(s) && s[j] != ',' {
				j++
			}
			out = append(out, parser.TSDictOption{Key: key, Value: strings.TrimSpace(s[i:j]), IsNumeric: true, HasValue: true})
			i = j
		}
	}
	return out
}

// AlterTSDictOptions implements ALTER TEXT SEARCH DICTIONARY name
// ( key [= value] [, ...] ), mirroring AlterTSDictionary (tsearchcmds.c):
// each named option is first removed from the existing list (regardless of
// whether it currently exists), then re-added only if the directive carries
// a value (HasValue) — a bare `key` in the option list is therefore a
// delete-only directive. The resulting merged list is validated via
// ValidateTSDictOptions, mirroring AlterTSDictionary's own
// verify_dictoptions call on the post-merge list (tsearchcmds.c) — a
// delete-only directive is never validated since it never re-enters the
// merged list, matching real PG exactly. Returns the newly-serialized
// dictinitoption text (for the caller's WAL record) and an error if no such
// dictionary is registered. DU-002 ALTER TEXT SEARCH DICTIONARY follow-up
// (M0119-0004).
func (c *InMemory) AlterTSDictOptions(name, schema string, directives []parser.TSDictOption) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	var target *UserTSDict
	for _, ud := range c.userTSDicts {
		if ud.NamespaceOID == nsOID && strings.EqualFold(ud.Name, name) {
			target = ud
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("text search dictionary %q does not exist", name)
	}
	opts := DeserializeTSDictOptions(target.InitOption)
	for _, directive := range directives {
		kept := opts[:0]
		for _, existing := range opts {
			if !strings.EqualFold(existing.Key, directive.Key) {
				kept = append(kept, existing)
			}
		}
		opts = kept
		if directive.HasValue {
			opts = append(opts, parser.TSDictOption{Key: directive.Key, Value: directive.Value, IsNumeric: directive.IsNumeric, HasValue: true})
		}
	}
	if tmplName, ok := builtinTSTemplateNameForOID(target.Template); ok {
		if verr := ValidateTSDictOptions(tmplName, opts); verr != nil {
			return "", verr
		}
	}
	target.InitOption = SerializeTSDictOptions(opts)
	return target.InitOption, nil
}

// AlterTSDictOptionsDuringRecovery is the idempotent recovery counterpart to
// AlterTSDictOptions: replay carries the already-computed final
// dictinitoption text (recorded once at original-execution time via the WAL
// record), so it just overwrites the field rather than re-running the
// merge. Discards a not-found result, mirroring RenameTSDictDuringRecovery.
// DU-002 ALTER TEXT SEARCH DICTIONARY follow-up (M0119-0004).
func (c *InMemory) AlterTSDictOptionsDuringRecovery(name, schema, initOption string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, ud := range c.userTSDicts {
		if ud.NamespaceOID == nsOID && strings.EqualFold(ud.Name, name) {
			ud.InitOption = initOption
			return
		}
	}
}

// RenameTSDict implements ALTER TEXT SEARCH DICTIONARY name RENAME TO
// newName, mirroring RenameTSConfig. DU-002 ALTER TEXT SEARCH DICTIONARY
// follow-up (M0119-0004).
func (c *InMemory) RenameTSDict(name, schema, newName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	var target *UserTSDict
	for _, ud := range c.userTSDicts {
		if ud.NamespaceOID == nsOID && strings.EqualFold(ud.Name, name) {
			target = ud
			continue
		}
		if ud.NamespaceOID == nsOID && strings.EqualFold(ud.Name, newName) {
			return fmt.Errorf("text search dictionary %q already exists", newName)
		}
	}
	if target == nil {
		return fmt.Errorf("text search dictionary %q does not exist", name)
	}
	target.Name = newName
	return nil
}

// SetTSDictSchema implements ALTER TEXT SEARCH DICTIONARY name SET SCHEMA
// newSchema, mirroring SetTSConfigSchema. Returns false if no such
// dictionary is registered. DU-002 ALTER TEXT SEARCH DICTIONARY follow-up
// (M0119-0004).
func (c *InMemory) SetTSDictSchema(name, schema, newSchema string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	newNsOID := c.schemas[strings.ToLower(newSchema)]
	if newNsOID == 0 {
		newNsOID = c.schemas["public"]
	}
	for _, ud := range c.userTSDicts {
		if ud.NamespaceOID == nsOID && strings.EqualFold(ud.Name, name) {
			ud.NamespaceOID = newNsOID
			return true
		}
	}
	return false
}

// RenameTSDictDuringRecovery is the discard-error recovery counterpart to
// RenameTSDict, mirroring RenameTSConfigDuringRecovery. DU-002 ALTER TEXT
// SEARCH DICTIONARY follow-up (M0119-0004).
func (c *InMemory) RenameTSDictDuringRecovery(name, schema, newName string) {
	_ = c.RenameTSDict(name, schema, newName)
}

// SetTSDictSchemaDuringRecovery is the discard-result recovery counterpart
// to SetTSDictSchema, mirroring SetTSConfigSchemaDuringRecovery. DU-002
// ALTER TEXT SEARCH DICTIONARY follow-up (M0119-0004).
func (c *InMemory) SetTSDictSchemaDuringRecovery(name, schema, newSchema string) {
	c.SetTSDictSchema(name, schema, newSchema)
}

// CreateTSConfig records a CREATE TEXT SEARCH CONFIGURATION in the runtime
// pg_ts_config registry so pg_dump's getTSConfigurations / dumpTSConfig
// re-emit it. `schema` is the (already-resolved) schema name the
// configuration lives in; an unknown schema resolves to the public
// namespace OID. Returns the new OID, or 0 with an error if a same-named
// configuration already exists in the same namespace (PG enforces a unique
// (cfgname, cfgnamespace)). DU-002 slice 446 (M0119-0004).
func (c *InMemory) CreateTSConfig(uc *UserTSConfig, schema string) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, existing := range c.userTSConfigs {
		if existing.NamespaceOID == nsOID && strings.EqualFold(existing.Name, uc.Name) {
			return 0, fmt.Errorf("text search configuration %q already exists", uc.Name)
		}
	}
	uc.OID = c.allocOIDLocked()
	uc.NamespaceOID = nsOID
	c.userTSConfigs = append(c.userTSConfigs, uc)
	return uc.OID, nil
}

// DropTSConfig removes the user-created text search configuration with the
// given bare name in the given schema from the registry. Returns true if one
// was found and removed. `schema` resolves like CreateTSConfig (unknown →
// public). DU-002 slice 446.
func (c *InMemory) DropTSConfig(name, schema string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for i, uc := range c.userTSConfigs {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			c.userTSConfigs = append(c.userTSConfigs[:i], c.userTSConfigs[i+1:]...)
			return true
		}
	}
	return false
}

// ListUserTSConfigs returns the user-created text search configurations in
// creation order. DU-002 slice 446.
func (c *InMemory) ListUserTSConfigs() []*UserTSConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.userTSConfigs) == 0 {
		return nil
	}
	out := make([]*UserTSConfig, len(c.userTSConfigs))
	copy(out, c.userTSConfigs)
	return out
}

// AddTSConfigMapping implements ALTER TEXT SEARCH CONFIGURATION name ADD
// MAPPING FOR tokenType WITH dictOIDs — appends one pg_ts_config_map entry.
// mapseqno restarts at 1 for every plain ADD MAPPING call (MakeConfigurationMapping's
// non-override insert path in tsearchcmds.c), so re-adding an already-mapped
// token type collides with the existing seqno-1 row on the
// (mapcfg, maptokentype, mapseqno) unique index — verified against real PG
// 18.3, this raises a 23505 unique_violation, NOT the 42710 this slice's
// original deferral-ledger row guessed. Returns the matched configuration
// (nil if no configuration with the given schema-resolved name exists) and
// whether tokenType already had a mapping entry (the caller must not append
// in that case).
// DU-002 slice 446 (M0119-0004).
func (c *InMemory) AddTSConfigMapping(name, schema, tokenType string, dictOIDs []uint32) (cfg *UserTSConfig, duplicate bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, uc := range c.userTSConfigs {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			for _, m := range uc.Mappings {
				if strings.EqualFold(m.TokenType, tokenType) {
					return uc, true
				}
			}
			// Defensive copy: execAlterTSConfigAddMapping reuses the same
			// dictOIDs backing array across every token type named in a
			// single multi-token ADD MAPPING FOR t1, t2 WITH d1, d2
			// statement (one call per token type). Without copying here,
			// the resulting TSConfigMapping entries alias one array, so an
			// in-place mutation of one entry's DictOIDs (e.g.
			// ReplaceTSConfigMappingDict) silently corrupts every sibling
			// entry from the same statement. Found via the replacedict
			// follow-up (M0119-0004).
			uc.Mappings = append(uc.Mappings, TSConfigMapping{TokenType: tokenType, DictOIDs: append([]uint32(nil), dictOIDs...)})
			return uc, false
		}
	}
	return nil, false
}

// DropTSConfigMapping implements ALTER TEXT SEARCH CONFIGURATION name DROP
// MAPPING FOR tokenType — removes the pg_ts_config_map entry for tokenType,
// mirroring DropConfigurationMapping in tsearchcmds.c. Returns the matched
// configuration (nil if no configuration with the given schema-resolved name
// exists) and whether a mapping for tokenType was found and removed.
// DU-002 slice 446 follow-up (M0119-0004).
func (c *InMemory) DropTSConfigMapping(name, schema, tokenType string) (cfg *UserTSConfig, found bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, uc := range c.userTSConfigs {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			for i, m := range uc.Mappings {
				if strings.EqualFold(m.TokenType, tokenType) {
					uc.Mappings = append(uc.Mappings[:i], uc.Mappings[i+1:]...)
					return uc, true
				}
			}
			return uc, false
		}
	}
	return nil, false
}

// RenameTSConfig implements ALTER TEXT SEARCH CONFIGURATION name RENAME TO
// newName, mirroring RenameCollation. DU-002 slice 446 follow-up
// (M0119-0004).
func (c *InMemory) RenameTSConfig(name, schema, newName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	var target *UserTSConfig
	for _, uc := range c.userTSConfigs {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			target = uc
			continue
		}
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, newName) {
			return fmt.Errorf("text search configuration %q already exists", newName)
		}
	}
	if target == nil {
		return fmt.Errorf("text search configuration %q does not exist", name)
	}
	target.Name = newName
	return nil
}

// SetTSConfigSchema implements ALTER TEXT SEARCH CONFIGURATION name SET
// SCHEMA newSchema, mirroring SetCollationSchema. Returns false if no such
// configuration is registered. DU-002 slice 446 follow-up (M0119-0004).
func (c *InMemory) SetTSConfigSchema(name, schema, newSchema string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	newNsOID := c.schemas[strings.ToLower(newSchema)]
	if newNsOID == 0 {
		newNsOID = c.schemas["public"]
	}
	for _, uc := range c.userTSConfigs {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			uc.NamespaceOID = newNsOID
			return true
		}
	}
	return false
}

// AlterTSConfigMapping implements ALTER TEXT SEARCH CONFIGURATION name ALTER
// MAPPING FOR tokenType WITH dictOIDs — replaces tokenType's entire mapping
// entry with the new dictionary list wholesale, mirroring
// MakeConfigurationMapping's override=true path in tsearchcmds.c (which
// deletes any existing pg_ts_config_map rows for the token type before
// inserting the new list). Unlike AddTSConfigMapping this never reports a
// duplicate — overriding an already-mapped token type is the entire point of
// this form, so there is no unique_violation to raise; if tokenType has no
// existing entry yet, it is simply appended (same as ADD MAPPING would).
// Returns the matched configuration (nil if no configuration with the given
// schema-resolved name exists). DU-002 slice 446 follow-up (M0119-0004).
func (c *InMemory) AlterTSConfigMapping(name, schema, tokenType string, dictOIDs []uint32) (cfg *UserTSConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, uc := range c.userTSConfigs {
		if uc.NamespaceOID == nsOID && strings.EqualFold(uc.Name, name) {
			newDicts := append([]uint32(nil), dictOIDs...)
			for i, m := range uc.Mappings {
				if strings.EqualFold(m.TokenType, tokenType) {
					uc.Mappings[i].DictOIDs = newDicts
					return uc
				}
			}
			uc.Mappings = append(uc.Mappings, TSConfigMapping{TokenType: tokenType, DictOIDs: newDicts})
			return uc
		}
	}
	return nil
}

// AlterTSConfigMappingDuringRecovery is the discard-result recovery
// counterpart to AlterTSConfigMapping, mirroring
// ReplaceTSConfigMappingDictDuringRecovery. DU-002 slice 446 follow-up
// (M0119-0004).
func (c *InMemory) AlterTSConfigMappingDuringRecovery(name, schema, tokenType string, dictOIDs []uint32) {
	c.AlterTSConfigMapping(name, schema, tokenType, dictOIDs)
}

// ReplaceTSConfigMappingDict implements ALTER TEXT SEARCH CONFIGURATION name
// ALTER MAPPING [FOR tokenTypes [, ...]] REPLACE oldOID WITH newOID —
// substitutes newOID for oldOID in every matched pg_ts_config_map entry,
// mirroring MakeConfigurationMapping's replace path in tsearchcmds.c. An
// empty tokenTypes means "match every mapped token type" (the bare REPLACE
// form). Unlike AddTSConfigMapping/DropTSConfigMapping this never errors
// when nothing matches — real PG's replace loop is an unconditional scan
// with no missing-match check, so a REPLACE that touches zero rows silently
// succeeds. Returns the matched configuration (nil if no configuration with
// the given schema-resolved name exists) and whether any entry was actually
// replaced. DU-002 replacedict follow-up (M0119-0004).
func (c *InMemory) ReplaceTSConfigMappingDict(name, schema string, tokenTypes []string, oldOID, newOID uint32) (cfg *UserTSConfig, replaced bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	for _, uc := range c.userTSConfigs {
		if uc.NamespaceOID != nsOID || !strings.EqualFold(uc.Name, name) {
			continue
		}
		for mi := range uc.Mappings {
			m := &uc.Mappings[mi]
			if len(tokenTypes) > 0 {
				matched := false
				for _, tt := range tokenTypes {
					if strings.EqualFold(m.TokenType, tt) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			for di, d := range m.DictOIDs {
				if d == oldOID {
					m.DictOIDs[di] = newOID
					replaced = true
				}
			}
		}
		return uc, replaced
	}
	return nil, false
}

// ReplaceTSConfigMappingDictDuringRecovery is the discard-result recovery
// counterpart to ReplaceTSConfigMappingDict, mirroring
// AddTSConfigMappingDuringRecovery. DU-002 replacedict follow-up
// (M0119-0004).
func (c *InMemory) ReplaceTSConfigMappingDictDuringRecovery(name, schema string, tokenTypes []string, oldOID, newOID uint32) {
	c.ReplaceTSConfigMappingDict(name, schema, tokenTypes, oldOID, newOID)
}

// CreateTSConfigDuringRecovery is the idempotent version of CreateTSConfig
// used by the WAL-replay driver (internal/initdb/tsconfig_ddl_recovery.go).
// Unlike CreateTSConfig it takes the OID from the WAL record and overwrites
// rather than erroring when an entry with the same OID is already present.
// Mirrors CreateTSDictDuringRecovery. DU-002 restart-persistence follow-up
// to slice 446.
func (c *InMemory) CreateTSConfigDuringRecovery(uc *UserTSConfig, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	uc.NamespaceOID = nsOID
	for i, existing := range c.userTSConfigs {
		if existing.OID == uc.OID {
			c.userTSConfigs[i] = uc
			if uc.OID >= c.nextOID {
				c.nextOID = uc.OID + 1
			}
			return
		}
	}
	c.userTSConfigs = append(c.userTSConfigs, uc)
	if uc.OID >= c.nextOID {
		c.nextOID = uc.OID + 1
	}
}

// AddTSConfigMappingDuringRecovery is the discard-result recovery
// counterpart to AddTSConfigMapping, mirroring DropTSDictDuringRecovery.
// DU-002 restart-persistence follow-up to slice 446.
func (c *InMemory) AddTSConfigMappingDuringRecovery(name, schema, tokenType string, dictOIDs []uint32) {
	c.AddTSConfigMapping(name, schema, tokenType, dictOIDs)
}

// DropTSConfigDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropTSConfig. DU-002 restart-persistence follow-up to
// slice 446.
func (c *InMemory) DropTSConfigDuringRecovery(name, schema string) {
	c.DropTSConfig(name, schema)
}

// DropTSConfigMappingDuringRecovery is the discard-result recovery
// counterpart to DropTSConfigMapping, mirroring AddTSConfigMappingDuringRecovery.
// DU-002 restart-persistence follow-up to the slice 446 RENAME/SET
// SCHEMA/DROP MAPPING follow-up.
func (c *InMemory) DropTSConfigMappingDuringRecovery(name, schema, tokenType string) {
	c.DropTSConfigMapping(name, schema, tokenType)
}

// RenameTSConfigDuringRecovery is the discard-error recovery counterpart to
// RenameTSConfig, mirroring RenameCollationDuringRecovery. DU-002
// restart-persistence follow-up to the slice 446 RENAME/SET SCHEMA/DROP
// MAPPING follow-up.
func (c *InMemory) RenameTSConfigDuringRecovery(name, schema, newName string) {
	_ = c.RenameTSConfig(name, schema, newName)
}

// SetTSConfigSchemaDuringRecovery is the discard-result recovery counterpart
// to SetTSConfigSchema. DU-002 restart-persistence follow-up to the slice
// 446 RENAME/SET SCHEMA/DROP MAPPING follow-up.
func (c *InMemory) SetTSConfigSchemaDuringRecovery(name, schema, newSchema string) {
	c.SetTSConfigSchema(name, schema, newSchema)
}

// CollationAttrsByName resolves a collation's dump-relevant attributes
// (provider, collate, ctype, locale, encoding, deterministic) by its bare name,
// searching the built-in collations and then user-created ones. Used by
// `CREATE COLLATION new FROM existing` to copy the source's attributes. Returns
// false if no collation by that name is known. M0119-0004.
func (c *InMemory) CollationAttrsByName(name string) (*UserCollation, bool) {
	// Built-in collations (mirror the 7 BKI rows surfaced in pg_collation):
	// name → {provider, encoding, collate, ctype, locale}.
	type bi struct {
		provider byte
		encoding int
		collate  string
		ctype    string
		locale   string
	}
	builtins := map[string]bi{
		"default":         {'d', -1, "", "", ""},
		"c":               {'c', -1, "C", "C", ""},
		"posix":           {'c', -1, "POSIX", "POSIX", ""},
		"ucs_basic":       {'b', 6, "", "", "C"},
		"unicode":         {'i', -1, "", "", "und"},
		"pg_c_utf8":       {'b', 6, "", "", "C.UTF-8"},
		"pg_unicode_fast": {'b', 6, "", "", "PG_UNICODE_FAST"},
	}
	lc := strings.ToLower(name)
	if b, ok := builtins[lc]; ok {
		return &UserCollation{
			Name: name, Owner: 10, Provider: b.provider, Encoding: b.encoding,
			Collate: b.collate, Ctype: b.ctype, Locale: b.locale, Deterministic: true,
		}, true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, uc := range c.userCollations {
		if strings.EqualFold(uc.Name, name) {
			cp := *uc
			return &cp, true
		}
	}
	return nil, false
}

// UserCollationOIDByName resolves a user-created collation's OID by its bare
// name (case-insensitive). The attcollation surfacing for a table column /
// composite field uses it to shadow the column type's typcollation when the
// column was declared `COLLATE <usercoll>`, so pg_dump's getTableAttrs reports
// `attcollation <> typcollation` and re-emits the inline COLLATE clause
// (schema-qualified via findCollationByOid → fmtQualifiedDumpable). Returns 0
// when no user collation by that name exists — built-in collations are resolved
// separately (collationNameToOID in the executor), so the caller only falls back
// here for a non-built-in name. M0119-0004 (DU-002 slice 394).
func (c *InMemory) UserCollationOIDByName(name string) uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, uc := range c.userCollations {
		if strings.EqualFold(uc.Name, name) {
			return uc.OID
		}
	}
	return 0
}

// ListUserCollations returns the user-created collations in creation order.
// Mirrors ListUserConversions. M0119-0004.
func (c *InMemory) ListUserCollations() []*UserCollation {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.userCollations) == 0 {
		return nil
	}
	out := make([]*UserCollation, len(c.userCollations))
	copy(out, c.userCollations)
	return out
}

// tablespaceVirtualRows is the VirtualRows callback for the pg_tablespace view.
// It returns the two bootstrap tablespaces (pg_default OID 1663, pg_global OID
// 1664, owned by the bootstrap superuser per pg_tablespace.dat) followed by any
// in-place tablespaces in the runtime registry, ordered by OID for stable
// output. Columns: oid, spcname, spcowner, spcacl (NULL = default), spcoptions
// (NULL). Runtime in-place tablespaces report spcowner=10 (bootstrap superuser);
// goopg does not resolve the recorded owner name to a role OID. M0110-0001.
func (c *InMemory) tablespaceVirtualRows() [][]string {
	rows := [][]string{
		{"1663", "pg_default", "10", "", ""},
		{"1664", "pg_global", "10", "", ""},
	}
	c.mu.RLock()
	extra := make([]*tablespaceRow, 0, len(c.tablespaces))
	for _, ts := range c.tablespaces {
		extra = append(extra, ts)
	}
	c.mu.RUnlock()
	sort.Slice(extra, func(i, j int) bool { return extra[i].oid < extra[j].oid })
	for _, ts := range extra {
		rows = append(rows, []string{strconv.FormatUint(uint64(ts.oid), 10), ts.name, "10", "", ""})
	}
	return rows
}

// dependVirtualRows builds pg_depend's only non-empty dependency class: the
// AUTO ('a') link from an OWNED-BY sequence to the column it is tied to. Each
// row matches what PG records for `ALTER/CREATE SEQUENCE ... OWNED BY t.c`:
//
//	classid     = pg_class (1259)   objid       = sequence's pg_class OID
//	objsubid    = 0                 refclassid  = pg_class (1259)
//	refobjid    = owning table OID  refobjsubid = owning column attnum (1-based)
//	deptype     = 'a'
//
// pg_dump's getTables LEFT JOIN (gated on relkind=RELKIND_SEQUENCE) reads these
// into owning_tab/owning_col so dumpSequence emits the ALTER SEQUENCE OWNED BY.
// Standalone sequences have no OwnedBy and contribute no row. M0110-0001
// (DU-002 slice 118).
func (c *InMemory) dependVirtualRows() [][]string {
	if SequenceParamsFunc == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Index user tables by lower(schema)+"."+lower(name) for owner resolution.
	// Only non-virtual relations can own a sequence (validateSeqOwnedBy rejects
	// virtual relations), so the index excludes them.
	type tblRef struct {
		oid     uint32
		columns []Column
	}
	byName := make(map[string]tblRef, len(c.tables))
	seqKeys := make([]string, 0)
	for k, t := range c.tables {
		if t.IsSequence {
			seqKeys = append(seqKeys, k)
			continue
		}
		if t.Virtual && t.View == nil && !t.IsMatView {
			continue
		}
		sch := strings.ToLower(t.Schema)
		if sch == "" {
			sch = "public"
		}
		byName[sch+"."+strings.ToLower(t.Name)] = tblRef{oid: t.OID, columns: t.Columns}
	}
	sort.Strings(seqKeys)

	rows := make([][]string, 0, len(seqKeys))
	for _, k := range seqKeys {
		t := c.tables[k]
		seqSchema := strings.ToLower(t.Schema)
		if seqSchema == "" {
			seqSchema = "public"
		}
		p, ok := SequenceParamsFunc(seqSchema + "." + t.Name)
		if !ok || p.OwnedBy == "" {
			continue
		}
		// Split "table.column" or "schema.table.column" (already lowercased).
		ownedBy := strings.ToLower(p.OwnedBy)
		lastDot := strings.LastIndex(ownedBy, ".")
		if lastDot < 0 {
			continue
		}
		colPart := ownedBy[lastDot+1:]
		rest := ownedBy[:lastDot]
		tblSchema := seqSchema // OWNED BY table must share the sequence's schema
		tblName := rest
		if firstDot := strings.Index(rest, "."); firstDot >= 0 {
			tblSchema = rest[:firstDot]
			tblName = rest[firstDot+1:]
		}
		ref, ok := byName[tblSchema+"."+tblName]
		if !ok {
			continue
		}
		attnum := 0
		isIdentity := false
		for _, col := range ref.columns {
			if strings.EqualFold(col.Name, colPart) {
				attnum = col.Ordinal + 1
				isIdentity = col.IdentityColumn
				break
			}
		}
		if attnum == 0 {
			continue
		}
		// An identity column's backing sequence is an INTERNAL ('i') dependency,
		// not AUTO ('a'). pg_dump keys is_identity_sequence on deptype='i' and then
		// dumps the sequence via `ALTER TABLE ... ADD GENERATED ... AS IDENTITY`
		// (suppressing the standalone CREATE SEQUENCE + ALTER SEQUENCE OWNED BY a
		// plain OWNED-BY sequence would get). DU-002 slice 120.
		deptype := "a"
		if isIdentity {
			deptype = "i"
		}
		rows = append(rows, []string{
			"1259",                     // 0: classid    = pg_class
			strconv.Itoa(int(t.OID)),   // 1: objid      = sequence OID
			"0",                        // 2: objsubid
			"1259",                     // 3: refclassid = pg_class
			strconv.Itoa(int(ref.oid)), // 4: refobjid   = owning table OID
			strconv.Itoa(attnum),       // 5: refobjsubid = owning col attnum
			deptype,                    // 6: deptype    = AUTO ('a') / INTERNAL ('i')
		})
	}

	// A SERIAL column's nextval() default records a NORMAL ('n') pg_depend link
	// from the pg_attrdef row to the owned sequence. Combined with the AUTO ('a')
	// OWNED-BY row above (sequence → table) and the table → attrdef edge pg_dump
	// adds itself, this closes the table↔sequence dependency loop pg_dump breaks
	// by emitting the default as a separate `ALTER TABLE ... SET DEFAULT
	// nextval(...)` (repairTableAttrDefMultiLoop) — exactly upstream's behavior.
	// The pg_attrdef.oid pg_dump scanned must equal this objid, so both come from
	// the shared attrDefRowsLocked numbering. DU-002 slice 121.
	for _, ad := range c.attrDefRowsLocked() {
		if ad.seqOID == 0 {
			continue
		}
		rows = append(rows, []string{
			"2604",                                 // 0: classid    = pg_attrdef
			strconv.FormatUint(uint64(ad.oid), 10), // 1: objid    = attrdef OID
			"0",                                    // 2: objsubid
			"1259",                                 // 3: refclassid = pg_class
			strconv.FormatUint(uint64(ad.seqOID), 10), // 4: refobjid = sequence OID
			"0", // 5: refobjsubid
			"n", // 6: deptype   = NORMAL
		})
	}

	// CREATE OPERATOR CLASS AS-list members: each pg_amop/pg_amproc row gets
	// two pg_depend rows, mirroring storeOperators/storeProcedures
	// (opclasscmds.c) for a class-attributed ("hard") reference — a NORMAL
	// ('n') dependency on the operator/function itself, and an INTERNAL ('i')
	// dependency on the owning opclass (refclassid=pg_opclass). dumpOpclass's
	// own query filters on refclassid=pg_opclass directly, and getDependencies
	// explicitly excludes 'i' rows from its pg_amop/pg_amproc→pg_opfamily
	// rewrite (both would turn into useless self-dependencies) — 'i' here
	// matches upstream exactly. DU-002 (M0119-0004) slice 411. Called under
	// c.mu.RLock() (this function's own lock) — read c.amOpMembers/
	// c.amProcMembers directly rather than through the public List* accessors
	// to avoid a recursive RLock.
	// A "loose" member — registered via ALTER OPERATOR FAMILY ... ADD rather
	// than a CREATE OPERATOR CLASS AS list — always has ClassOID == 0
	// (InvalidOid; every class-attributed member gets a real, non-zero
	// RegisterUserOperatorClass OID, so 0 is an unambiguous sentinel here).
	// storeOperators/storeProcedures (opclasscmds.c) downgrade EVERY one of a
	// loose member's dependencies from hard to soft: the operator/function
	// reference itself becomes AUTO ('a', not NORMAL 'n'), and the
	// class-or-family reference becomes an AUTO dependency on the FAMILY
	// itself (refclassid=pg_opfamily, refobjid=the owning family's own OID —
	// "Historically, ALTER ADD has created soft dependencies", verbatim
	// AlterOpFamilyAdd comment) instead of an INTERNAL dependency on a class.
	// This is what lets dumpOpfamily's own loose-member query
	// (refclassid=pg_opfamily) find these rows and emit them as a separate
	// `ALTER OPERATOR FAMILY ... ADD` statement. DU-002 (M0119-0004), ALTER
	// OPERATOR FAMILY ADD slice.
	for _, m := range c.amOpMembers {
		refDeptype := "n"
		classOrFamilyRefclassid := "2616" // pg_opclass
		classOrFamilyRefobjid := m.ClassOID
		classOrFamilyDeptype := "i"
		// gistadjustmembers/spgadjustmembers force EVERY OPERATOR member
		// (not just loose ALTER-ADD'd ones) to a soft family-level
		// dependency for these two AMs, regardless of class-attribution —
		// see amForcesSoftOperatorDependency's doc comment. DU-002
		// (M0119-0004).
		if m.ClassOID == 0 || amForcesSoftOperatorDependency(m.Method) {
			refDeptype = "a"
			classOrFamilyRefclassid = "2753" // pg_opfamily
			classOrFamilyRefobjid = m.FamilyOID
			classOrFamilyDeptype = "a"
		}
		rows = append(rows, []string{
			"2602", // 0: classid    = pg_amop
			strconv.FormatUint(uint64(m.OID), 10), // 1: objid  = amop entry OID
			"0",    // 2: objsubid
			"2617", // 3: refclassid = pg_operator
			strconv.FormatUint(uint64(m.OperOID), 10), // 4: refobjid = operator OID
			"0",        // 5: refobjsubid
			refDeptype, // 6: deptype = NORMAL (hard) or AUTO (loose)
		})
		rows = append(rows, []string{
			"2602",
			strconv.FormatUint(uint64(m.OID), 10),
			"0",
			classOrFamilyRefclassid,
			strconv.FormatUint(uint64(classOrFamilyRefobjid), 10),
			"0",
			classOrFamilyDeptype,
		})
		// A FOR ORDER BY entry also gets a dependency on its sort family
		// (opclasscmds.c storeOperators: "A search operator also needs a dep
		// on the referenced opfamily") — NORMAL for a hard/class-attributed
		// member, AUTO for a loose one, same op->ref_is_hard switch as the
		// two rows above. getDependencies rewrites this into an edge from
		// the owning opfamily to the sort family for dump ordering;
		// dumpOpclass's own SQL-text rendering reads amopsortfamily directly
		// and does not need this row. DU-002 (M0119-0004) slice 414.
		if m.SortFamilyOID != 0 {
			rows = append(rows, []string{
				"2602",
				strconv.FormatUint(uint64(m.OID), 10),
				"0",
				"2753", // refclassid = pg_opfamily
				strconv.FormatUint(uint64(m.SortFamilyOID), 10),
				"0",
				refDeptype,
			})
		}
	}
	for _, m := range c.amProcMembers {
		refDeptype := "n"
		classOrFamilyRefclassid := "2616" // pg_opclass
		classOrFamilyRefobjid := m.ClassOID
		classOrFamilyDeptype := "i"
		// A class-attributed FUNCTION member on a GiST/SP-GiST opclass is
		// only hard-on-class when its amprocnum is one of the AM's
		// *required* support procs; every optional one is forced soft on
		// the family, mirroring gistadjustmembers/spgadjustmembers's
		// function loop. DU-002 (M0119-0004).
		if m.ClassOID == 0 || amForcesSoftFunctionDependency(m.Method, m.ProcNum) {
			refDeptype = "a"
			classOrFamilyRefclassid = "2753" // pg_opfamily
			classOrFamilyRefobjid = m.FamilyOID
			classOrFamilyDeptype = "a"
		}
		rows = append(rows, []string{
			"2603", // 0: classid    = pg_amproc
			strconv.FormatUint(uint64(m.OID), 10),
			"0",
			"1255", // refclassid = pg_proc
			strconv.FormatUint(uint64(m.ProcOID), 10),
			"0",
			refDeptype,
		})
		rows = append(rows, []string{
			"2603",
			strconv.FormatUint(uint64(m.OID), 10),
			"0",
			classOrFamilyRefclassid,
			strconv.FormatUint(uint64(classOrFamilyRefobjid), 10),
			"0",
			classOrFamilyDeptype,
		})
	}
	return rows
}

// CreateTablespace records an in-place tablespace in the runtime registry and
// returns the freshly allocated OID. A name already present returns a duplicate
// error mirroring PG's get_tablespace_oid collision message. The caller (the DDL
// executor) is responsible for the pg_-prefix reserved-name check and for
// creating the pg_tblspc/<oid> directory. M0095-0003.
func (c *InMemory) CreateTablespace(name, owner, location string) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lc := strings.ToLower(name)
	if _, ok := c.tablespaces[lc]; ok {
		return 0, fmt.Errorf("tablespace %q already exists", name)
	}
	c.nextOID++
	oid := c.nextOID
	c.tablespaces[lc] = &tablespaceRow{
		oid:      oid,
		name:     name,
		owner:    owner,
		location: location,
	}
	return oid, nil
}

// DropTablespace removes a tablespace from the runtime registry, returning its
// OID and whether it was present. M0095-0003.
func (c *InMemory) DropTablespace(name string) (uint32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lc := strings.ToLower(name)
	ts, ok := c.tablespaces[lc]
	if !ok {
		return 0, false
	}
	delete(c.tablespaces, lc)
	return ts.oid, true
}

// ExtensionRowsForDB returns the pg_extension virtual rows visible to a
// connection bound to database `db`. Because goopg shares one in-memory catalog
// across all databases, this filters the runtime extension registry to rows
// scoped to `db` (plus any unscoped legacy rows), mirroring PostgreSQL's
// per-database pg_extension catalog. An empty `db` returns every row (no
// connection context — e.g. embedded/test callers). M0110-0003 (AC-002 gap #7c).
func (c *InMemory) ExtensionRowsForDB(db string) [][]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.extensionRowsLocked(db)
}

// extensionRowsLocked builds the pg_extension virtual rows, optionally filtered
// to the rows visible in database `dbFilter`. An empty `dbFilter` includes every
// row; otherwise a row is included when it was created in `dbFilter` or carries
// no database scope (legacy/direct-call inserts). Must hold c.mu (R or W).
// M0110-0003 (AC-002 gap #7c).
func (c *InMemory) extensionRowsLocked(dbFilter string) [][]string {
	rows := make([]*extensionRow, 0, len(c.extensions))
	for _, e := range c.extensions {
		if dbFilter != "" && e.database != "" && e.database != dbFilter {
			continue
		}
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

// allSchemasLocked returns all (name, oid) pairs. Must be called with mu held.
func (c *InMemory) allSchemasLocked() []struct {
	name string
	oid  uint32
} {
	out := make([]struct {
		name string
		oid  uint32
	}, 0, len(c.schemas)+len(c.tempNamespaces))
	for name, oid := range c.schemas {
		out = append(out, struct {
			name string
			oid  uint32
		}{name, oid})
	}
	// Surface each session's temporary namespace (pg_temp_<id>) so a
	// cross-session pg_namespace join and pg_my_temp_schema()'s OID resolve
	// to a real catalog row, mirroring PostgreSQL's shared pg_namespace.
	// M0118-0009 (temp-schema-cleanup, design 0118-0091).
	for owner, oid := range c.tempNamespaces {
		out = append(out, struct {
			name string
			oid  uint32
		}{tempNamespaceName(owner), oid})
	}
	return out
}

// tempNamespaceName renders the pg_temp_<id> namespace name for a temp-owner
// token ("s<id>" → "pg_temp_<id>"). Mirrors PostgreSQL's pg_temp_N naming.
func tempNamespaceName(owner string) string {
	return "pg_temp_" + strings.TrimPrefix(owner, "s")
}

// EnsureTempNamespace lazily allocates and returns the OID of the temporary
// namespace owned by the session identified by owner ("s<id>"). Called from the
// CREATE TEMPORARY path on first temp-object creation. Idempotent: a session's
// temp namespace persists for the rest of its life (PostgreSQL reuses pg_temp_N
// even after every temp object is dropped). A blank owner (session-less context)
// gets no namespace and returns 0. M0118-0009 (temp-schema-cleanup, 0118-0091).
func (c *InMemory) EnsureTempNamespace(owner string) uint32 {
	if owner == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if oid, ok := c.tempNamespaces[owner]; ok {
		return oid
	}
	oid := c.nextOID
	c.nextOID++
	c.tempNamespaces[owner] = oid
	return oid
}

// TempNamespaceOID returns the OID of owner's temporary namespace, or 0 if the
// session has not created one. Backs pg_my_temp_schema(). M0118-0009.
func (c *InMemory) TempNamespaceOID(owner string) uint32 {
	if owner == "" {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.tempNamespaces[owner]
}

// DropTempNamespace removes owner's temporary namespace registration. Called on
// session exit, after the session's temp objects have been dropped, so a later
// cross-session pg_namespace scan no longer sees the dead pg_temp_<id> row.
// M0118-0009 (temp-schema-cleanup, design 0118-0091).
func (c *InMemory) DropTempNamespace(owner string) {
	if owner == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tempNamespaces, owner)
}

// tempNamespaceOIDLocked is the lock-free variant for callers already holding
// c.mu (e.g. the pg_class VirtualRows builder).
func (c *InMemory) tempNamespaceOIDLocked(owner string) uint32 {
	if owner == "" {
		return 0
	}
	return c.tempNamespaces[owner]
}

// RoleExists reports whether a role with the given name has been registered,
// or is one of PG18's 16 built-in "pg_*" predefined roles (predefinedRoles —
// M0119-0004-ACLHEAP).
func (c *InMemory) RoleExists(name string) bool {
	key := strings.ToLower(name)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.roles[key]; ok {
		return true
	}
	_, ok := c.predefinedRoles[key]
	return ok
}

// predefinedRoleSeeds lists PG18's 16 built-in "pg_*" predefined roles
// (postgres/src/include/catalog/pg_authid.dat) and their fixed OIDs. Twin of
// internal/initdb/initdb.go's `predefined` slice (heap seeding) and
// internal/executor/pg_authid_sync.go's `pgAuthidPredefined` (heap resync) —
// internal/catalog cannot import either (would create an import cycle), so
// this is a third, deliberately independent copy; keep all three in sync.
var predefinedRoleSeeds = []struct {
	oid  uint32
	name string
}{
	{6171, "pg_database_owner"},
	{6181, "pg_read_all_data"},
	{6182, "pg_write_all_data"},
	{3373, "pg_monitor"},
	{3374, "pg_read_all_settings"},
	{3375, "pg_read_all_stats"},
	{3377, "pg_stat_scan_tables"},
	{4569, "pg_read_server_files"},
	{4570, "pg_write_server_files"},
	{4571, "pg_execute_server_program"},
	{4200, "pg_signal_backend"},
	{4544, "pg_checkpoint"},
	{6337, "pg_maintain"},
	{4550, "pg_use_reserved_connections"},
	{6304, "pg_create_subscription"},
	{6392, "pg_signal_autovacuum_worker"},
}

// RoleOIDPgDatabaseOwner is ROLE_PG_DATABASE_OWNER (postgres/src/include/
// catalog/pg_authid.h): the predefined role whose "charter ... is to have
// exactly one, implicit, situation-dependent member" —
// check_role_membership_authorization (postgres/src/backend/commands/user.c)
// rejects any explicit `GRANT pg_database_owner TO ...`. M0119-0004-ACLHEAP.
const RoleOIDPgDatabaseOwner uint32 = 6171

// newPredefinedRoleMap builds the name->OID lookup for predefinedRoleSeeds,
// used to populate InMemory.predefinedRoles at construction.
func newPredefinedRoleMap() map[string]uint32 {
	m := make(map[string]uint32, len(predefinedRoleSeeds))
	for _, s := range predefinedRoleSeeds {
		m[s.name] = s.oid
	}
	return m
}

// RegisterRole records a user-created role. Called from CREATE ROLE/USER. A
// fresh OID is minted from the running catalog counter the first time a name is
// seen; re-registering an existing name keeps its OID stable so a policy's
// pg_policy.polroles entry stays valid across the session.
func (c *InMemory) RegisterRole(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(name)
	if _, ok := c.roles[key]; ok {
		return
	}
	c.roles[key] = c.nextOID
	c.nextOID++
}

// RoleAttrs carries a registered role's pg_authid-shaped attributes: the
// LOGIN/SUPERUSER flags and the stored password credential. CredType matches
// the RecordKindRoleState WAL encoding (0=none, 1=plaintext, 2=md5,
// 3=scram-sha-256); Secret is the stored verifier in the same shape as
// pg_authid.rolpassword (SCRAM-SHA-256$… by default). root-0021.
//
// CreateDB/CreateRole/Replication/BypassRLS/ConnLimit/ValidUntil mirror the
// remaining CREATE/ALTER ROLE attribute-clause options (postgres/src/backend/
// commands/user.c CreateRole's opt_* booleans) that were previously
// accept-and-ignore (DU-002 slice 439 follow-up). ConnLimit's PG default is
// -1 ("no limit", pg_authid.dat's rolconnlimit for every seeded role) — the
// Go zero value 0 is a DIFFERENT, valid PG setting ("no new connections"), so
// every RoleAttrs constructed as the "no attributes given yet" starting
// point (as opposed to a real all-zero snapshot) MUST set ConnLimit: -1
// explicitly; see tryHandleRoleDDL's two construction sites. ValidUntil is
// the raw `VALID UNTIL '<literal>'` text (empty = NULL/no expiration, PG's
// default); goopg does not evaluate it (no password-expiry enforcement).
type RoleAttrs struct {
	CanLogin    bool
	Superuser   bool
	CreateDB    bool
	CreateRole  bool
	Replication bool
	BypassRLS   bool
	ConnLimit   int32
	ValidUntil  string
	CredType    byte
	Secret      string
}

// RegisterRoleWithOID registers a role preserving a known OID — used by the
// pg_authid heap loader and WAL replay so role OIDs stay stable across
// restarts (pg_policy.polroles and the pg_authid heap reference them).
// nextOID is bumped past the given OID so later mints never collide.
func (c *InMemory) RegisterRoleWithOID(name string, oid uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(name)
	c.roles[key] = oid
	if oid >= c.nextOID {
		c.nextOID = oid + 1
	}
}

// SetRoleAttrs records (or replaces) the attribute/credential sidecar entry
// for an already-registered role. A no-op for unregistered names so callers
// can apply parse results unconditionally after RegisterRole. The bootstrap
// superuser "postgres" is special-cased: it is never in the roles map (its
// OID 10 is implicit) but its rolpassword verifier — written by initdb's
// --pwfile handling into the pg_authid heap — must be recallable so the auth
// UserStore can authenticate it after a restart. root-0021.
func (c *InMemory) SetRoleAttrs(name string, attrs RoleAttrs) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(name)
	if _, ok := c.roles[key]; !ok && key != "postgres" {
		return
	}
	a := attrs
	c.roleAttrs[key] = &a
}

// LookupRoleAttrs returns the attribute sidecar entry for a role. ok is false
// when the role is unregistered or has no recorded attributes.
func (c *InMemory) LookupRoleAttrs(name string) (RoleAttrs, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	a, ok := c.roleAttrs[strings.ToLower(name)]
	if !ok || a == nil {
		return RoleAttrs{}, false
	}
	return *a, true
}

// RoleStateSnapshot is one registered role's full registry state, returned by
// AllRoleStates for WAL logging and UserStore seeding.
type RoleStateSnapshot struct {
	Name  string
	OID   uint32
	Attrs RoleAttrs
}

// AllRoleStates returns every user-registered role (the bootstrap superuser
// `postgres` is not stored in the map) with its attributes, sorted by name.
func (c *InMemory) AllRoleStates() []RoleStateSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]RoleStateSnapshot, 0, len(c.roles))
	for name, oid := range c.roles {
		s := RoleStateSnapshot{Name: name, OID: oid}
		if a := c.roleAttrs[name]; a != nil {
			s.Attrs = *a
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UnregisterRole removes a role from the registry. Called from DROP ROLE.
// Also drops any pg_auth_members rows referencing the role's OID on either
// side (roleid or member) — PG cascades membership removal automatically
// when a role is dropped (DropRole, user.c), unlike an ordinary DROP
// RESTRICT. M0119-0004-ACLHEAP.
func (c *InMemory) UnregisterRole(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := strings.ToLower(name)
	oid, hadOID := c.roles[key]
	delete(c.roles, key)
	delete(c.roleAttrs, key)
	if !hadOID {
		return
	}
	for mk := range c.roleMembers {
		if mk.RoleOID == oid || mk.MemberOID == oid || mk.GrantorOID == oid {
			delete(c.roleMembers, mk)
		}
	}
	for sk := range c.roleSettings {
		if sk.RoleOID == oid {
			delete(c.roleSettings, sk)
		}
	}
}

// RenameRole re-keys a registered role's registry entry (the roles map and
// its roleAttrs sidecar) from oldName to newName, preserving its OID and
// attributes exactly like PostgreSQL's RenameRole (postgres/src/backend/
// commands/user.c) — the role keeps the same pg_authid.oid, so existing
// pg_policy.polroles/ownership references stay valid. Returns false when
// oldName is unregistered (caller should raise "role does not exist");
// callers are responsible for the pre-checks RenameRole itself doesn't
// duplicate (new-name-already-exists, reserved "pg_" prefix). root-0021
// follow-up (M0119-0004).
func (c *InMemory) RenameRole(oldName, newName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	oldKey := strings.ToLower(oldName)
	newKey := strings.ToLower(newName)
	oid, ok := c.roles[oldKey]
	if !ok {
		return false
	}
	delete(c.roles, oldKey)
	c.roles[newKey] = oid
	if a, ok := c.roleAttrs[oldKey]; ok {
		delete(c.roleAttrs, oldKey)
		c.roleAttrs[newKey] = a
	}
	return true
}

// RoleOID returns the OID minted for a registered role, resolving the seeded
// bootstrap superuser (`postgres`, OID 10 = BOOTSTRAP_SUPERUSERID) which is not
// stored in the user-role map, and PG18's 16 built-in "pg_*" predefined roles
// (predefinedRoles — M0119-0004-ACLHEAP). The bool is false for an unknown
// role. Used by CREATE POLICY ... TO <role> to record role OIDs in
// pg_policy.polroles. DU-002 slice 330.
func (c *InMemory) RoleOID(name string) (uint32, bool) {
	key := strings.ToLower(name)
	if key == "postgres" {
		return 10, true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	// predefinedRoles is checked FIRST and always wins: it is a fixed,
	// install-time fact (unlike `roles`, whose CREATE ROLE producer does not
	// yet port PG's IsReservedName "pg_"-prefix rejection — a pre-existing,
	// separate gap), so an exact-name collision must never let a
	// user-registered entry shadow a predefined role's real OID.
	if oid, ok := c.predefinedRoles[key]; ok {
		return oid, true
	}
	oid, ok := c.roles[key]
	return oid, ok
}

// IsPredefinedRole reports whether name is one of PG18's 16 built-in "pg_*"
// predefined roles (predefinedRoleSeeds) — a fixed, install-time fact,
// distinct from the user-created `roles` registry. Backs DROP ROLE/USER/
// GROUP's "pinned object" guard: PostgreSQL's checkSharedDependencies
// (postgres/src/backend/catalog/pg_shdepend.c) rejects dropping any object
// whose OID is below FirstUnpinnedObjectId (12000) with "cannot drop %s
// because it is required by the database system" — every predefined role OID
// qualifies. M0119-0004-ACLHEAP.
func (c *InMemory) IsPredefinedRole(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.predefinedRoles[strings.ToLower(name)]
	return ok
}

// roleNameForOIDLocked resolves oid against `roles` (preferring the
// roleACLDisplay case-preserved spelling) and, failing that, against
// predefinedRoles (whose canonical name IS its map key — predefined roles
// are always registered lower-case, matching PG's own bare pg_authid
// spelling). Caller must already hold c.mu (read or write). Shared by
// RoleNameForOID/RoleNameForOIDOrUnknown so both reverse-lookups recognise a
// predefined-role OID exactly like their forward counterpart, RoleOID.
// M0119-0004-ACLHEAP.
func (c *InMemory) roleNameForOIDLocked(oid uint32) (string, bool) {
	for name, roid := range c.roles {
		if roid == oid {
			if disp, ok := c.roleACLDisplay[name]; ok {
				return disp, true
			}
			return name, true
		}
	}
	for name, roid := range c.predefinedRoles {
		if roid == oid {
			return name, true
		}
	}
	return "", false
}

// RoleNameForOID is the reverse of RoleOID: it resolves a role OID back to the
// name aclitemout / pg_dump prints. OID 0 (ACL_ID_PUBLIC) renders as the empty
// string (the PUBLIC pseudo-role); OID 10 (BOOTSTRAP_SUPERUSERID) is "postgres";
// a registered user role resolves to its case-preserved spelling (the same
// override the relacl renderer uses); an unknown OID falls back to its numeric
// form, matching PostgreSQL's rendering of a since-dropped role. Used by the
// pg_type typacl heap-decode path so a goopg-served `SELECT typacl FROM pg_type`
// returns role NAMES, not OIDs (M0119-0004-ACLHEAP).
func (c *InMemory) RoleNameForOID(oid uint32) string {
	if oid == 0 {
		return ""
	}
	if oid == 10 {
		return "postgres"
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if name, ok := c.roleNameForOIDLocked(oid); ok {
		return name
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// RoleNameForOIDOrUnknown mirrors ruleutils.c's pg_get_userbyid SQL builtin
// exactly: it resolves oid to its role name, or PG's literal fallback string
// "unknown (OID=n)" when no such role exists. This differs from
// RoleNameForOID's own fallback (the bare numeral), which serves ACL-text
// rendering internals rather than the SQL function's documented contract.
// M0119-0004-ACLHEAP (parameter ACL half — used by dumpRoleGUCPrivs's
// pg_get_userbyid(10) call).
func (c *InMemory) RoleNameForOIDOrUnknown(oid uint32) string {
	if oid == 10 {
		return "postgres"
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if name, ok := c.roleNameForOIDLocked(oid); ok {
		return name
	}
	return fmt.Sprintf("unknown (OID=%d)", oid)
}

// GrantTablePrivilege records that role may exercise priv on relOID. See the
// Catalog interface doc. Role names are stored lower-cased and privilege
// keywords upper-cased so lookups are case-insensitive. M0118-0008.
func (c *InMemory) GrantTablePrivilege(relOID uint32, role, priv string) {
	c.GrantTablePrivilegeWithGrantOption(relOID, role, priv, false)
}

// GrantTablePrivilegeWithGrantOption records priv for role on relOID, tracking
// whether it was granted WITH GRANT OPTION. The grant-option flag is OR-ed in:
// once a privilege carries the option a later plain GRANT does not clear it
// (matching PostgreSQL, which retains the grant option until REVOKE GRANT
// OPTION FOR). See the Catalog interface doc. DU-002 slice 332.
func (c *InMemory) GrantTablePrivilegeWithGrantOption(relOID uint32, role, priv string, withGrantOption bool) {
	c.GrantTablePrivilegeAs(relOID, role, priv, withGrantOption, aclOwnerRole)
}

// GrantTablePrivilegeAs is GrantTablePrivilegeWithGrantOption plus an explicit
// grantor — the role that executed the GRANT (aclOwnerRole/"postgres" for the
// common owner/superuser case, or a SET ROLE-impersonated role's name).
// Recorded in tableACLGrantor so relaclTextLockedFor renders the true
// "grantee=privs/grantor" aclitem instead of hardcoding the owner. A later
// GRANT to the same grantee (with any grantor, including the default) always
// overwrites the stored grantor — matching PostgreSQL, where re-granting
// updates the existing aclitem's grantor to whoever issued that GRANT.
// M0119-0004-ACLHEAP (grantor half).
func (c *InMemory) GrantTablePrivilegeAs(relOID uint32, role, priv string, withGrantOption bool, grantor string) {
	display := strings.TrimSpace(role)
	role = strings.ToLower(display)
	priv = strings.ToUpper(strings.TrimSpace(priv))
	if role == "" || priv == "" {
		return
	}
	grantorDisplay := strings.TrimSpace(grantor)
	grantor = strings.ToLower(grantorDisplay)
	if grantor == "" {
		grantor = aclOwnerRole
		grantorDisplay = aclOwnerRole
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Remember the exact case the grantee was spelled with so relacl renders
	// the role's true name (PostgreSQL role names are case-significant when
	// double-quoted). Only record a non-identity spelling; the common
	// all-lowercase case needs no override. DU-002 slice 337.
	if display != role {
		c.roleACLDisplay[role] = display
	}
	if grantorDisplay != grantor {
		c.roleACLDisplay[grantor] = grantorDisplay
	}
	// A GRANT to the owner re-materializes an explicit owner aclitem, so the
	// owner is no longer the zero-privilege (absent) entry an earlier owner-side
	// REVOKE ALL produced — clear the emptied flag. A GRANT to a *grantee*,
	// however, leaves the owner at zero: PostgreSQL keeps relacl as
	// `{grantee=…/postgres}` with NO owner entry, and pg_dump must still emit the
	// owner's `REVOKE ALL …`. So only clear the flag for an owner-side GRANT;
	// otherwise the owner stays absent and coexists with the grantee. DU-002
	// slices 341 / 344.
	if role == aclOwnerRole {
		delete(c.relACLEmptied, relOID)
		delete(c.relACLOwnerRevoked, relOID)
	}
	byRole := c.tableACLs[relOID]
	if byRole == nil {
		byRole = make(map[string]map[string]bool)
		c.tableACLs[relOID] = byRole
	}
	privs := byRole[role]
	if privs == nil {
		privs = make(map[string]bool)
		byRole[role] = privs
		// First grant to this role on this relation: record its position so
		// relacl renders grantees in grant order, matching PostgreSQL's append
		// semantics. The owner is rendered first separately, so it is never
		// recorded here. DU-002 slice 354.
		if role != aclOwnerRole {
			c.tableACLOrder[relOID] = append(c.tableACLOrder[relOID], role)
		}
	}
	privs[priv] = privs[priv] || withGrantOption
	// Every GRANT to this grantee — even a repeat one carrying the default
	// owner grantor — restamps the grantor: PostgreSQL's aclupdate updates an
	// existing aclitem's grantor to whoever issued the latest GRANT.
	if role != aclOwnerRole {
		grantors := c.tableACLGrantor[relOID]
		if grantors == nil {
			grantors = make(map[string]string)
			c.tableACLGrantor[relOID] = grantors
		}
		grantors[role] = grantor
	}
}

// dropTableACLOrderRole removes role from relOID's grant-order list, keeping the
// remaining grantees in their original order. Caller must hold c.mu. DU-002
// slice 354.
func (c *InMemory) dropTableACLOrderRole(relOID uint32, role string) {
	order := c.tableACLOrder[relOID]
	for i, r := range order {
		if r == role {
			c.tableACLOrder[relOID] = append(order[:i], order[i+1:]...)
			break
		}
	}
	if len(c.tableACLOrder[relOID]) == 0 {
		delete(c.tableACLOrder, relOID)
	}
}

// RevokeTablePrivilege removes priv for role on relOID. If the role's privilege
// set becomes empty its entry is dropped (so relaclTextLockedFor no longer
// emits that grantee), and if the relation has no grantees left the whole entry
// is removed (relacl returns to NULL, matching acldefault → pg_dump emits
// nothing). A revoke of a privilege never held is a no-op. The grantee's
// display-case override is intentionally retained: it is keyed by lower-cased
// role and consulted only when that role still appears in some relacl, so a
// stale entry is harmless and a later re-GRANT reuses it. DU-002 slice 338.
func (c *InMemory) RevokeTablePrivilege(relOID uint32, role, priv string) {
	role = strings.ToLower(strings.TrimSpace(role))
	priv = strings.ToUpper(strings.TrimSpace(priv))
	if role == "" || priv == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	byRole := c.tableACLs[relOID]
	if byRole == nil {
		return
	}
	privs := byRole[role]
	if privs == nil {
		return
	}
	delete(privs, priv)
	if len(privs) == 0 {
		delete(byRole, role)
		c.dropTableACLOrderRole(relOID, role)
		if grantors := c.tableACLGrantor[relOID]; grantors != nil {
			delete(grantors, role)
			if len(grantors) == 0 {
				delete(c.tableACLGrantor, relOID)
			}
		}
		if role == aclOwnerRole {
			// The owner's implicit default aclitem has been fully revoked. Record
			// this regardless of whether other grantees survive: an object whose
			// acldefault grants a non-owner implicit privilege (a function's PUBLIC
			// EXECUTE) leaves a surviving aclitem (`{=X/owner}`) after the owner
			// revoke, and relaclTextLockedFor must still suppress the leading owner
			// entry there. relACLEmptied (set below) is the narrower case where the
			// owner revoke also empties the array. DU-002 slice 347.
			c.relACLOwnerRevoked[relOID] = true
		}
	}
	if len(byRole) == 0 {
		// If the relation's last remaining aclitem was the owner's own entry, the
		// owner has just revoked all of its implicit default privileges
		// (REVOKE ALL … FROM owner). PostgreSQL leaves relacl as a non-NULL empty
		// array {} in that case (distinct from NULL); record the emptied state so
		// relaclTextLockedFor renders "{}" and pg_dump re-emits the bare
		// `REVOKE ALL …`. A trailing grantee revoke (slice 338) instead returns
		// relacl to NULL, which is what dropping the entry without this flag does.
		// DU-002 slice 341.
		if role == aclOwnerRole {
			c.relACLEmptied[relOID] = true
		}
		delete(c.tableACLs, relOID)
		delete(c.tableACLOrder, relOID)
	}
}

// GrantColumnPrivilege records that role may exercise the column-level priv
// ("SELECT"/"INSERT"/"UPDATE"/"REFERENCES", upper-cased) on column attNum of
// the relation relOID. role is matched case-insensitively; "PUBLIC" is the
// pseudo-role. The column analogue of GrantTablePrivilege. M0119-0004-ACLHEAP.
func (c *InMemory) GrantColumnPrivilege(relOID uint32, attNum int16, role, priv string) {
	c.GrantColumnPrivilegeWithGrantOption(relOID, attNum, role, priv, false)
}

// GrantColumnPrivilegeWithGrantOption is GrantColumnPrivilege plus a grant-
// option flag, OR-ed in exactly as GrantTablePrivilegeWithGrantOption does (a
// later plain GRANT does not clear a previously set option). M0119-0004-ACLHEAP.
func (c *InMemory) GrantColumnPrivilegeWithGrantOption(relOID uint32, attNum int16, role, priv string, withGrantOption bool) {
	c.GrantColumnPrivilegeAs(relOID, attNum, role, priv, withGrantOption, aclOwnerRole)
}

// GrantColumnPrivilegeAs is GrantColumnPrivilegeWithGrantOption plus an
// explicit grantor — the role that executed the GRANT (aclOwnerRole/"postgres"
// for the common owner/superuser case, or a SET ROLE-impersonated role's
// name). Recorded in attrACLGrantor so AttrACLText renders the true
// "grantee=privs/grantor" aclitem instead of hardcoding the owner. The column
// analogue of GrantTablePrivilegeAs. M0119-0004-ACLHEAP (attacl grantor half).
func (c *InMemory) GrantColumnPrivilegeAs(relOID uint32, attNum int16, role, priv string, withGrantOption bool, grantor string) {
	display := strings.TrimSpace(role)
	role = strings.ToLower(display)
	priv = strings.ToUpper(strings.TrimSpace(priv))
	if role == "" || priv == "" {
		return
	}
	grantorDisplay := strings.TrimSpace(grantor)
	grantor = strings.ToLower(grantorDisplay)
	if grantor == "" {
		grantor = aclOwnerRole
		grantorDisplay = aclOwnerRole
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Preserve the grantee's original-case spelling so attacl renders the role's
	// true name (shared with the relacl store; DU-002 slice 337).
	if display != role {
		c.roleACLDisplay[role] = display
	}
	if grantorDisplay != grantor {
		c.roleACLDisplay[grantor] = grantorDisplay
	}
	key := attrACLKey{relOID: relOID, attNum: attNum}
	byRole := c.attrACLs[key]
	if byRole == nil {
		byRole = make(map[string]map[string]bool)
		c.attrACLs[key] = byRole
	}
	privs := byRole[role]
	if privs == nil {
		privs = make(map[string]bool)
		byRole[role] = privs
		c.attrACLOrder[key] = append(c.attrACLOrder[key], role)
	}
	privs[priv] = privs[priv] || withGrantOption
	// Every GRANT to this grantee — even a repeat one carrying the default
	// owner grantor — restamps the grantor: PostgreSQL's aclupdate updates an
	// existing aclitem's grantor to whoever issued the latest GRANT.
	grantors := c.attrACLGrantor[key]
	if grantors == nil {
		grantors = make(map[string]string)
		c.attrACLGrantor[key] = grantors
	}
	grantors[role] = grantor
}

// RevokeColumnPrivilege removes a single column-level priv for role on column
// attNum of relOID. When the role's column privilege set becomes empty its entry
// is dropped (so AttrACLText no longer lists it); when the column has no
// grantees left the whole entry is removed and attacl returns to NULL (a column
// has no owner default to fall back to). A revoke of a privilege never held is a
// no-op. The column analogue of RevokeTablePrivilege. M0119-0004-ACLHEAP.
func (c *InMemory) RevokeColumnPrivilege(relOID uint32, attNum int16, role, priv string) {
	role = strings.ToLower(strings.TrimSpace(role))
	priv = strings.ToUpper(strings.TrimSpace(priv))
	if role == "" || priv == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := attrACLKey{relOID: relOID, attNum: attNum}
	byRole := c.attrACLs[key]
	if byRole == nil {
		return
	}
	privs := byRole[role]
	if privs == nil {
		return
	}
	delete(privs, priv)
	if len(privs) == 0 {
		delete(byRole, role)
		if order := c.attrACLOrder[key]; len(order) > 0 {
			for i, r := range order {
				if r == role {
					c.attrACLOrder[key] = append(order[:i], order[i+1:]...)
					break
				}
			}
			if len(c.attrACLOrder[key]) == 0 {
				delete(c.attrACLOrder, key)
			}
		}
		if grantors := c.attrACLGrantor[key]; grantors != nil {
			delete(grantors, role)
			if len(grantors) == 0 {
				delete(c.attrACLGrantor, key)
			}
		}
	}
	if len(byRole) == 0 {
		delete(c.attrACLs, key)
		delete(c.attrACLOrder, key)
		delete(c.attrACLGrantor, key)
	}
}

// MaterializeOwnerACL records an explicit owner aclitem for relOID holding
// exactly ownerPrivs, but only if no explicit owner entry exists yet. See the
// Catalog interface doc. The owner privileges are stored without the grant
// option (PostgreSQL prints the owner's self-grant with no "*" markers), so a
// subsequent RevokeTablePrivilege against the owner renders the remaining
// privileges as a plain letter string. DU-002 slice 340.
func (c *InMemory) MaterializeOwnerACL(relOID uint32, owner string, ownerPrivs []string) {
	owner = strings.ToLower(strings.TrimSpace(owner))
	if owner == "" || len(ownerPrivs) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.relACLEmptied[relOID] || c.relACLOwnerRevoked[relOID] {
		return // owner already revoked its implicit default; do not resurrect it
	}
	byRole := c.tableACLs[relOID]
	if byRole == nil {
		byRole = make(map[string]map[string]bool)
		c.tableACLs[relOID] = byRole
	}
	if _, ok := byRole[owner]; ok {
		return // owner entry already materialized; do not clobber a prior revoke
	}
	privs := make(map[string]bool, len(ownerPrivs))
	for _, p := range ownerPrivs {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p != "" {
			privs[p] = false
		}
	}
	byRole[owner] = privs
}

// HasTablePrivilege reports whether role was granted priv on relOID. M0118-0008.
func (c *InMemory) HasTablePrivilege(relOID uint32, role, priv string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	priv = strings.ToUpper(strings.TrimSpace(priv))
	c.mu.RLock()
	defer c.mu.RUnlock()
	byRole := c.tableACLs[relOID]
	if byRole == nil {
		return false
	}
	privs := byRole[role]
	if privs == nil {
		return false
	}
	_, ok := privs[priv]
	return ok
}

// DropTableACL forgets all privileges recorded for relOID. M0118-0008.
func (c *InMemory) DropTableACL(relOID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tableACLs, relOID)
	delete(c.tableACLGrantor, relOID)
	delete(c.tableACLOrder, relOID)
	delete(c.relACLEmptied, relOID)
	delete(c.relACLOwnerRevoked, relOID)
}

// aclPrivLetter pairs a privilege keyword (as stored upper-cased in tableACLs)
// with the single letter aclitemout prints for it.
type aclPrivLetter struct {
	keyword string
	letter  byte
}

// tableACLPrivOrder lists the table privileges in PostgreSQL's canonical
// aclitemout order for relkind 'r' (the bit order in src/include/utils/acl.h).
// Rendering grantee privilege sets in this order matches the aclitem[] text
// PostgreSQL stores in pg_class.relacl, so pg_dump's client-side
// buildACLCommands re-emits GRANTs byte-identically.
var tableACLPrivOrder = []aclPrivLetter{
	{"INSERT", 'a'},
	{"SELECT", 'r'},
	{"UPDATE", 'w'},
	{"DELETE", 'd'},
	{"TRUNCATE", 'D'},
	{"REFERENCES", 'x'},
	{"TRIGGER", 't'},
	{"MAINTAIN", 'm'},
}

// sequenceACLPrivOrder lists the sequence privileges (USAGE/SELECT/UPDATE) in
// the same canonical aclitemout bit order: SELECT('r'), UPDATE('w'), USAGE('U').
// pg_dump diffs a sequence's relacl against acldefault('s', owner) and re-emits
// `GRANT … ON SEQUENCE …` for the grantee. DU-002 slice 333.
var sequenceACLPrivOrder = []aclPrivLetter{
	{"SELECT", 'r'},
	{"UPDATE", 'w'},
	{"USAGE", 'U'},
}

// ownerTableACLString is the privilege-letter string for the owner's full set
// of table privileges (every bit in tableACLPrivOrder), i.e. "arwdDxtm". It
// matches acldefault('r', owner), the baseline pg_dump diffs relacl against, so
// the owner's own entry produces no GRANT/REVOKE on round-trip.
const ownerTableACLString = "arwdDxtm"

// ownerSequenceACLString is the owner's full set of sequence privileges, i.e.
// "rwU". It matches acldefault('s', owner) so the owner's own entry produces no
// GRANT/REVOKE on round-trip. DU-002 slice 333.
const ownerSequenceACLString = "rwU"

// publicPseudoRole is the lower-cased name goopg records a GRANT … TO PUBLIC
// under (PostgreSQL reserves PUBLIC, so no real role can carry this name). It is
// rendered as the empty grantee in the materialized aclitem[]. DU-002 slice 334.
const publicPseudoRole = "public"

// schemaACLPrivOrder lists the schema (namespace) privileges in PostgreSQL's
// canonical aclitemout bit order, taken from ACL_ALL_RIGHTS_STR ("arwdDxtXUCTc…"):
// USAGE('U') precedes CREATE('C'). pg_dump diffs a namespace's nspacl against
// acldefault('n', owner) client-side and re-emits `GRANT … ON SCHEMA …`, so
// projecting the privilege set in this order matches the aclitem[] text PG
// stores in pg_namespace.nspacl. DU-002 slice 335.
var schemaACLPrivOrder = []aclPrivLetter{
	{"USAGE", 'U'},
	{"CREATE", 'C'},
}

// ownerSchemaACLString is the privilege-letter string for the owner's full set
// of schema privileges, i.e. "UC". It matches acldefault('n', owner)
// (ACL_ALL_RIGHTS_SCHEMA = USAGE|CREATE; schemas grant PUBLIC nothing by
// default) so the owner's own entry produces no GRANT/REVOKE on round-trip.
// DU-002 slice 335.
const ownerSchemaACLString = "UC"

// functionACLPrivOrder lists the function (pg_proc) privileges in PostgreSQL's
// canonical aclitemout order. A function has a single privilege, EXECUTE('X').
// pg_dump's getFuncs diffs a routine's proacl against acldefault('f', proowner)
// client-side in buildACLCommands and re-emits `GRANT … ON FUNCTION …`, so
// projecting the privilege set in this order matches the aclitem[] text PG
// stores in pg_proc.proacl. DU-002 slice 345.
var functionACLPrivOrder = []aclPrivLetter{
	{"EXECUTE", 'X'},
}

// ownerFunctionACLString is the privilege-letter string for the owner's full
// set of function privileges, i.e. "X". The owner half of acldefault('f',
// owner) is "postgres=X/postgres"; the other half is the implicit PUBLIC
// EXECUTE grant ("=X/postgres"), which the function GRANT recorder seeds
// explicitly so a materialized proacl reproduces both default entries. DU-002
// slice 345.
const ownerFunctionACLString = "X"

// typeACLPrivOrder lists the type/domain (pg_type) privileges in PostgreSQL's
// canonical aclitemout order. A type has a single privilege, USAGE('U').
// pg_dump's getTypes diffs a type's typacl against acldefault('T', typowner)
// client-side in buildACLCommands and re-emits `GRANT … ON TYPE …`, so
// projecting the privilege set in this order matches the aclitem[] text PG
// stores in pg_type.typacl. M0119-0004-ACLHEAP.
var typeACLPrivOrder = []aclPrivLetter{
	{"USAGE", 'U'},
}

// ownerTypeACLString is the privilege-letter string for the owner's full set of
// type privileges, i.e. "U". The owner half of acldefault('T', owner) is
// "postgres=U/postgres"; the other half is the implicit PUBLIC USAGE grant
// ("=U/postgres"), which the type GRANT recorder seeds explicitly so a
// materialized typacl reproduces both default entries — structurally identical
// to the function EXECUTE default (ownerFunctionACLString). M0119-0004-ACLHEAP.
const ownerTypeACLString = "U"

// parameterACLPrivOrder lists the GUC-level (pg_parameter_acl) privileges in
// PostgreSQL's canonical aclitemout bit order: SET('s') precedes ALTER
// SYSTEM('A') (ACL_ALL_RIGHTS_STR, acl.h — "...csAm"). pg_dumpall's
// dumpRoleGUCPrivs diffs a parameter's paracl against acldefault('p',
// BOOTSTRAP_SUPERUSERID) client-side in buildACLCommands and re-emits `GRANT
// … ON PARAMETER …`, so projecting the privilege set in this order matches
// the aclitem[] text PG stores in pg_parameter_acl.paracl. M0119-0004-ACLHEAP
// (parameter ACL half).
var parameterACLPrivOrder = []aclPrivLetter{
	{"SET", 's'},
	{"ALTER SYSTEM", 'A'},
}

// ownerParameterACLString is the privilege-letter string for the owner's full
// set of parameter privileges, i.e. "sA" (ACL_ALL_RIGHTS_PARAMETER_ACL =
// SET|ALTER_SYSTEM). Unlike TYPE/FUNCTION, PUBLIC gets NO implicit default
// (acldefault('p', …)'s world_default is ACL_NO_RIGHTS) — structurally
// identical to the plain table-privilege pattern (ownerTableACLString), not
// the owner+PUBLIC pattern. PostgreSQL treats every parameter ACL as owned by
// the bootstrap superuser (ExecGrant_Parameter hardcodes ownerId =
// BOOTSTRAP_SUPERUSERID), matching goopg's single "postgres" owner/grantor.
// M0119-0004-ACLHEAP (parameter ACL half).
const ownerParameterACLString = "sA"

// ParameterACLOID returns the synthetic pg_parameter_acl.oid for the
// lower-cased dotted GUC name parname, minting one from the shared nextOID
// counter and registering it in parameterACLNames on first use (mirrors
// PostgreSQL's lazy ParameterAclCreate). Repeated calls for the same
// parname are idempotent. M0119-0004-ACLHEAP (parameter ACL half).
func (c *InMemory) ParameterACLOID(parname string) uint32 {
	parname = strings.ToLower(strings.TrimSpace(parname))
	c.mu.Lock()
	defer c.mu.Unlock()
	if oid, ok := c.parameterACLOIDs[parname]; ok {
		return oid
	}
	oid := c.allocOIDLocked()
	c.parameterACLOIDs[parname] = oid
	c.parameterACLNames[oid] = parname
	return oid
}

// HasParameterACL reports whether parname already has a pg_parameter_acl
// entry, without minting one. Mirrors ParameterAclLookup(parameter,
// missing_ok=true): real PostgreSQL only runs
// check_GUC_name_for_parameter_acl (name validation) inside
// ParameterAclCreate, i.e. the first time a GRANT mints the entry — a
// second GRANT, or any REVOKE, on an already-materialized parameter skips
// the check. Callers use this to gate name validation the same way.
func (c *InMemory) HasParameterACL(parname string) bool {
	parname = strings.ToLower(strings.TrimSpace(parname))
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.parameterACLOIDs[parname]
	return ok
}

// ParameterACLText renders the materialized pg_parameter_acl.paracl text for
// the GUC identified by paramOID, or "" (SQL NULL) when no privilege has been
// granted away. Parameters share the OID-keyed ACL store with relations,
// schemas, routines, types, and databases (goopg mints parameter-ACL OIDs
// from the same nextOID counter, so there is no collision). M0119-0004-ACLHEAP
// (parameter ACL half).
func (c *InMemory) ParameterACLText(paramOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLockedFor(paramOID, parameterACLPrivOrder, ownerParameterACLString)
}

// ParameterACLEntries returns every granted GUC's (oid, parname) pair, sorted
// by parname, so pg_parameter_acl's VirtualRows can project a deterministic
// row set mirroring pg_dumpall's `ORDER BY 1` (getParameterACLs/
// dumpRoleGUCPrivs). Only parameters that have ever received a GRANT appear
// here — PostgreSQL itself never rows a GUC in pg_parameter_acl until its
// first GRANT (ParameterAclCreate is lazy). M0119-0004-ACLHEAP (parameter ACL
// half).
func (c *InMemory) ParameterACLEntries() []struct {
	OID     uint32
	Parname string
} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]struct {
		OID     uint32
		Parname string
	}, 0, len(c.parameterACLNames))
	for oid, name := range c.parameterACLNames {
		out = append(out, struct {
			OID     uint32
			Parname string
		}{OID: oid, Parname: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Parname < out[j].Parname })
	return out
}

// foreignServerACLPrivOrder lists the foreign-server (pg_foreign_server)
// privileges in PostgreSQL's canonical aclitemout order. A foreign server has
// a single privilege, USAGE('U'). pg_dump's getForeignServers diffs a server's
// srvacl against acldefault('S', srvowner) client-side in buildACLCommands and
// re-emits `GRANT … ON FOREIGN SERVER …`, so projecting the privilege set in
// this order matches the aclitem[] text PG stores in pg_foreign_server.srvacl.
// DU-002 slice 427.
var foreignServerACLPrivOrder = []aclPrivLetter{
	{"USAGE", 'U'},
}

// ownerForeignServerACLString is the privilege-letter string for the owner's
// full set of foreign-server privileges, i.e. "U". Unlike a function/type, a
// foreign server's world default is ACL_NO_RIGHTS (acldefault('S', owner) =
// "{postgres=U/postgres}" — PUBLIC gets nothing), so the FOREIGN SERVER GRANT
// recorder does NOT seed an implicit PUBLIC entry — the owner-only default
// mirrors ownerSchemaACLString/ownerTableACLString, not the dual owner+PUBLIC
// shape of ownerFunctionACLString/ownerTypeACLString. DU-002 slice 427.
const ownerForeignServerACLString = "U"

// ForeignServerACLText renders the materialized pg_foreign_server.srvacl text
// for the foreign server identified by srvOID, or "" (SQL NULL) when no
// privileges have been granted away. Foreign servers share the OID-keyed ACL
// store with relations, schemas, routines, and types (goopg mints foreign-
// server OIDs from the same nextOID counter, so there is no collision).
// pg_dump's getForeignServers diffs srvacl against acldefault('S', srvowner)
// client-side in buildACLCommands, so projecting the correct aclitem[] text is
// sufficient for the `GRANT … ON FOREIGN SERVER …` round-trip. DU-002 slice 427.
func (c *InMemory) ForeignServerACLText(srvOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLockedFor(srvOID, foreignServerACLPrivOrder, ownerForeignServerACLString)
}

// foreignDataWrapperACLPrivOrder lists the foreign-data-wrapper
// (pg_foreign_data_wrapper) privileges in PostgreSQL's canonical aclitemout
// order. Like a foreign server, an FDW has a single privilege, USAGE('U')
// (ACL_ALL_RIGHTS_FDW == ACL_USAGE, acl.h) — projecting the privilege set in
// this order matches the aclitem[] text PG stores in
// pg_foreign_data_wrapper.fdwacl. DU-002 slice 428.
var foreignDataWrapperACLPrivOrder = []aclPrivLetter{
	{"USAGE", 'U'},
}

// ownerForeignDataWrapperACLString is the privilege-letter string for the
// owner's full set of foreign-data-wrapper privileges, i.e. "U". An FDW's
// world default is ACL_NO_RIGHTS (acldefault('F', owner) =
// "{postgres=U/postgres}" — PUBLIC gets nothing, same as OBJECT_FOREIGN_SERVER
// right below OBJECT_FDW in acl.c's acldefault switch), so the FOREIGN DATA
// WRAPPER GRANT recorder does NOT seed an implicit PUBLIC entry — mirrors
// ownerForeignServerACLString. DU-002 slice 428.
const ownerForeignDataWrapperACLString = "U"

// ForeignDataWrapperACLText renders the materialized
// pg_foreign_data_wrapper.fdwacl text for the FDW identified by fdwOID, or ""
// (SQL NULL) when no privileges have been granted away. FDWs share the
// OID-keyed ACL store with relations, schemas, routines, types, and foreign
// servers (goopg mints FDW OIDs from the same nextOID counter, so there is no
// collision). pg_dump's getForeignDataWrappers diffs fdwacl against
// acldefault('F', fdwowner) client-side in buildACLCommands, so projecting
// the correct aclitem[] text is sufficient for the `GRANT … ON FOREIGN DATA
// WRAPPER …` round-trip. DU-002 slice 428.
func (c *InMemory) ForeignDataWrapperACLText(fdwOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLockedFor(fdwOID, foreignDataWrapperACLPrivOrder, ownerForeignDataWrapperACLString)
}

// databaseACLPrivOrder lists the database (pg_database) privileges in
// PostgreSQL's canonical aclitemout bit order: CREATE('C'), TEMPORARY('T'),
// CONNECT('c') (ACL_ALL_RIGHTS_DATABASE, acl.h). pg_dump's getDatabases diffs
// datacl against acldefault('d', datdba) client-side in buildACLCommands, so
// projecting the privilege set in this order matches the aclitem[] text PG
// stores in pg_database.datacl. M0119-0004-ACLHEAP (datacl half).
var databaseACLPrivOrder = []aclPrivLetter{
	{"CREATE", 'C'},
	{"TEMPORARY", 'T'},
	{"CONNECT", 'c'},
}

// ownerDatabaseACLString is the privilege-letter string for the owner's full
// set of database privileges, i.e. "CTc". Unlike a type/function (whose
// world default equals the owner default), a database's world_default is
// ACL_CREATE_TEMP | ACL_CONNECT — PUBLIC gets TEMPORARY+CONNECT but NOT
// CREATE — so the DATABASE GRANT recorder seeds PUBLIC's reduced default
// explicitly rather than reusing this owner string. M0119-0004-ACLHEAP
// (datacl half).
const ownerDatabaseACLString = "CTc"

// DatabaseACLText renders the materialized pg_database.datacl text for the
// database identified by dbOID, or "" (SQL NULL) when no privileges have been
// granted away. Databases share the OID-keyed ACL store with relations,
// schemas, routines, types, and foreign objects (goopg mints database OIDs
// from a disjoint range at initdb, so there is no collision). pg_dump's
// getDatabases diffs datacl against acldefault('d', datdba) client-side in
// buildACLCommands, so projecting the correct aclitem[] text is the renderer
// half of the `GRANT … ON DATABASE …` round-trip; the GRANT path must
// additionally re-sync this text into the heap-backed pg_database row
// (M0119-0004-ACLHEAP, pg_database is a SHARED catalog — a single relfilenode,
// not duplicated per connected database like pg_type/pg_attribute).
func (c *InMemory) DatabaseACLText(dbOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLockedFor(dbOID, databaseACLPrivOrder, ownerDatabaseACLString)
}

// attrACLKey identifies one table column for column-level (pg_attribute.attacl)
// privilege tracking: the owning relation's OID plus the column's 1-based
// attribute number. Real table OIDs routinely exceed 2^16, so a packed
// (relOID<<16 | attnum) uint32 would overflow — a struct key is used instead.
// M0119-0004-ACLHEAP (attacl half).
type attrACLKey struct {
	relOID uint32
	attNum int16
}

// attrACLPrivOrder lists the column (pg_attribute.attacl) privileges in
// PostgreSQL's canonical aclitemout bit order. Column-level GRANT supports only
// the subset INSERT(a)/SELECT(r)/UPDATE(w)/REFERENCES(x) — the remaining table
// privilege letters (DELETE/TRUNCATE/TRIGGER/MAINTAIN) never appear in attacl.
// Unlike a table/type/function there is NO owner-default privilege string: a
// column's acldefault('c', owner) is empty, so attacl renders grantees only
// (AttrACLText). M0119-0004-ACLHEAP.
var attrACLPrivOrder = []aclPrivLetter{
	{"INSERT", 'a'},
	{"SELECT", 'r'},
	{"UPDATE", 'w'},
	{"REFERENCES", 'x'},
}

// relaclTextLocked renders the materialized pg_class.relacl text — an aclitem[]
// array literal such as `{postgres=arwdDxtm/postgres,grantee_role=r/postgres}` —
// for relOID from the in-memory GRANT store, or "" (SQL NULL) when no
// privileges have been granted away. PostgreSQL leaves relacl NULL until the
// first GRANT, at which point it materializes the array with the owner's full
// default privileges first (grantor = owner), followed by each grantee's
// entry. goopg has the single bootstrap superuser "postgres" (OID 10) as every
// table's owner and grantor, so the owner entry and each grantor are
// "postgres". Grantee roles are emitted in a stable (sorted) order so the
// projection is deterministic. Caller must hold c.mu (read or write).
//
// DU-002 slice 331 (GRANT/ACL relacl round-trip in pg_dump). pg_dump's
// getTables selects c.relacl directly and parses the aclitem[] text
// client-side in buildACLCommands (src/bin/pg_dump/dumputils.c) — no
// server-side aclexplode/aclitemout is involved — so projecting the correct
// text is sufficient for the round-trip.
func (c *InMemory) relaclTextLocked(relOID uint32) string {
	return c.relaclTextLockedFor(relOID, tableACLPrivOrder, ownerTableACLString)
}

// RelaclText renders the materialized pg_class.relacl text for the table
// identified by relOID, or "" (SQL NULL) when no privileges have been granted
// away. Exported for callers outside the virtual pg_class row builder (which
// calls relaclTextLocked directly while already holding c.mu), such as tests
// that need to inspect relacl without executing a full pg_class SELECT.
func (c *InMemory) RelaclText(relOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLocked(relOID)
}

// relaclTextLockedSeq is relaclTextLocked for a sequence (relkind 'S'): it
// renders with the sequence privilege order (USAGE/SELECT/UPDATE) and the
// sequence owner-default string "rwU", which is what pg_dump diffs against via
// acldefault('s', owner). DU-002 slice 333. Caller must hold c.mu.
func (c *InMemory) relaclTextLockedSeq(relOID uint32) string {
	return c.relaclTextLockedFor(relOID, sequenceACLPrivOrder, ownerSequenceACLString)
}

// NamespaceACLText renders the materialized pg_namespace.nspacl text for the
// schema identified by schemaOID, or "" (SQL NULL) when no privileges have been
// granted away. Schemas share the OID-keyed ACL store with relations (goopg
// mints schema OIDs from the same nextOID counter, so there is no collision),
// and pg_dump diffs nspacl against acldefault('n', owner) client-side in
// buildACLCommands, so projecting the correct aclitem[] text is sufficient for
// the `GRANT … ON SCHEMA …` round-trip. DU-002 slice 335.
func (c *InMemory) NamespaceACLText(schemaOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLockedFor(schemaOID, schemaACLPrivOrder, ownerSchemaACLString)
}

// ProcACLText renders the materialized pg_proc.proacl text for the routine
// identified by procOID, or "" (SQL NULL) when no privileges have been granted
// away. Routines share the OID-keyed ACL store with relations and schemas
// (goopg mints routine OIDs from a disjoint range, so there is no collision).
// A function's acldefault is "{=X/postgres,postgres=X/postgres}" — owner AND
// PUBLIC both hold EXECUTE — so the function GRANT recorder seeds the PUBLIC
// entry explicitly and the owner entry is supplied here by ownerFunctionACLString
// (the owner branch of relaclTextLockedFor). pg_dump's getFuncs diffs proacl
// against acldefault('f', proowner) client-side in buildACLCommands, so
// projecting the correct aclitem[] text is sufficient for the
// `GRANT … ON FUNCTION …` round-trip. DU-002 slice 345.
func (c *InMemory) ProcACLText(procOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLockedFor(procOID, functionACLPrivOrder, ownerFunctionACLString)
}

// TypeACLText renders the materialized pg_type.typacl text for the type/domain
// identified by typeOID, or "" (SQL NULL) when no privileges have been granted
// away. Types share the OID-keyed ACL store with relations, schemas, and
// routines (goopg mints type OIDs from a disjoint range, so there is no
// collision). A type's acldefault('T', owner) is "{=U/owner,owner=U/owner}" —
// owner AND PUBLIC both hold USAGE — so the type GRANT recorder seeds the PUBLIC
// entry explicitly and the owner entry is supplied here by ownerTypeACLString
// (the owner branch of relaclTextLockedFor), exactly as ProcACLText does for the
// function EXECUTE default. pg_dump's getTypes diffs typacl against
// acldefault('T', typowner) client-side in buildACLCommands, so projecting the
// correct aclitem[] text is the renderer half of the `GRANT … ON TYPE …`
// round-trip; the GRANT path must additionally re-sync this text into the
// heap-backed pg_type row (M0119-0004-ACLHEAP, see design 0119-0004).
func (c *InMemory) TypeACLText(typeOID uint32) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relaclTextLockedFor(typeOID, typeACLPrivOrder, ownerTypeACLString)
}

// AttrACLText renders the materialized pg_attribute.attacl text for the column
// identified by (relOID, attNum), or "" (SQL NULL) when no column-level
// privilege has been granted away. Unlike relacl/typacl/proacl, a column's
// acldefault('c', owner) is EMPTY — the owner and PUBLIC hold no implicit
// column privilege — so attacl has no leading owner aclitem and no implicit
// PUBLIC entry: it renders the grantees only, in grant order, and returns to
// NULL once the last column privilege is revoked. This is the column analogue of
// TypeACLText; pg_dump's getTableAttrs selects attacl directly and diffs it
// against the empty default client-side in buildACLCommands, re-emitting
// `GRANT <priv>(<col>) ON TABLE … TO …`, so projecting the correct aclitem[]
// text is the renderer half of the column-GRANT round-trip (the GRANT path must
// additionally re-sync this text into the heap-backed pg_attribute row).
// M0119-0004-ACLHEAP (attacl half).
func (c *InMemory) AttrACLText(relOID uint32, attNum int16) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := attrACLKey{relOID: relOID, attNum: attNum}
	byRole := c.attrACLs[key]
	if len(byRole) == 0 {
		return "" // NULL attacl — empty acldefault, so pg_dump emits no column GRANT
	}
	// Render grantees in grant order (the order roles first appeared in a column
	// GRANT), matching PostgreSQL's append semantics for the aclitem[] array. Fall
	// back to a sorted snapshot only for any role present in byRole but missing
	// from the order list (defensive — never silently drop a grant).
	roles := make([]string, 0, len(byRole))
	seen := make(map[string]bool, len(byRole))
	for _, role := range c.attrACLOrder[key] {
		if seen[role] {
			continue
		}
		if _, ok := byRole[role]; !ok {
			continue // stale order entry (role fully revoked)
		}
		seen[role] = true
		roles = append(roles, role)
	}
	var missing []string
	for role := range byRole {
		if seen[role] {
			continue
		}
		missing = append(missing, role)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		roles = append(roles, missing...)
	}
	var items []string
	for _, role := range roles {
		letters := renderACLLetters(byRole[role], attrACLPrivOrder)
		if letters == "" {
			continue // role holds only non-column privileges
		}
		// PUBLIC renders as an empty grantee ("=<privs>/grantor"); a mixed-case
		// role restores its original spelling; an unsafe name is double-quoted —
		// identical to relaclTextLockedFor's grantee handling.
		grantee := role
		if grantee == publicPseudoRole {
			grantee = ""
		} else if disp, ok := c.roleACLDisplay[role]; ok {
			grantee = disp
		}
		// The grantor defaults to the object owner absent an explicit stamp —
		// identical fallback to relaclTextLockedFor's grantor handling.
		// M0119-0004-ACLHEAP (attacl grantor half).
		grantor := aclOwnerRole
		if g, ok := c.attrACLGrantor[key][role]; ok && g != "" {
			grantor = g
		}
		if disp, ok := c.roleACLDisplay[grantor]; ok {
			grantor = disp
		}
		items = append(items, aclQuoteName(grantee)+"="+letters+"/"+aclQuoteName(grantor))
	}
	return "{" + strings.Join(items, ",") + "}"
}

// relaclTextLockedFor is the object-type-agnostic core of relaclTextLocked: it
// renders the materialized aclitem[] for relOID using the given privilege order
// and owner-default privilege-letter string. Caller must hold c.mu.
func (c *InMemory) relaclTextLockedFor(relOID uint32, privOrder []aclPrivLetter, ownerString string) string {
	byRole := c.tableACLs[relOID]
	if len(byRole) == 0 {
		if c.relACLEmptied[relOID] || c.relACLOwnerRevoked[relOID] {
			// Owner revoked all of its implicit default privileges
			// (REVOKE ALL … FROM owner): PostgreSQL stores a non-NULL empty
			// aclitem array {} and pg_dump emits a bare `REVOKE ALL …`. DU-002
			// slice 341. relACLOwnerRevoked also covers revoking every grantee
			// of a multi-default object (function owner + PUBLIC) in one
			// statement, which empties the array with a non-owner last revoke
			// so relACLEmptied stays unset. DU-002 slice 347.
			return "{}"
		}
		return "" // NULL relacl — matches acldefault, pg_dump emits no GRANT
	}
	// The owner aclitem is normally listed first. PostgreSQL leaves relacl NULL
	// while the owner holds its implicit default set, rendering "postgres=<full>"
	// once any grant materializes the array. An owner-side REVOKE materializes an
	// explicit owner entry with a reduced privilege set (DU-002 slice 340); when
	// present, render the owner's actual remaining privileges instead of the
	// constant default, and skip the owner in the grantee loop below.
	//
	// When the owner has been zeroed by a full REVOKE ALL (relACLEmptied set) and
	// a later GRANT to a grantee re-materialized the array, the owner is absent
	// entirely: PostgreSQL stores `{grantee=…/postgres}` with no owner entry, and
	// pg_dump diffs that against acldefault to re-emit the owner's `REVOKE ALL …`.
	// Suppress the leading owner entry in that case. DU-002 slice 344.
	var items []string
	if !c.relACLEmptied[relOID] && !c.relACLOwnerRevoked[relOID] {
		ownerLetters := ownerString
		if ownerPrivs, ok := byRole[aclOwnerRole]; ok {
			ownerLetters = renderACLLetters(ownerPrivs, privOrder)
		}
		items = append(items, "postgres="+ownerLetters+"/postgres")
	}
	// Render grantees in grant order (the order roles first appeared in a GRANT),
	// matching PostgreSQL's append semantics for pg_class.relacl — NOT alphabetical
	// order. tableACLOrder is the authoritative sequence; fall back to a sorted
	// snapshot only if the order list is somehow out of sync with byRole (defensive,
	// e.g. a role present in byRole but missing from the list), so every grantee is
	// still emitted deterministically. DU-002 slice 354.
	roles := make([]string, 0, len(byRole))
	seen := make(map[string]bool, len(byRole))
	for _, role := range c.tableACLOrder[relOID] {
		if role == aclOwnerRole || seen[role] {
			continue
		}
		if _, ok := byRole[role]; !ok {
			continue // stale order entry (role fully revoked)
		}
		seen[role] = true
		roles = append(roles, role)
	}
	// Append any grantees present in byRole but missing from the order list
	// (defensive — should not happen, but never silently drop a grant). They go
	// in sorted order for determinism.
	var missing []string
	for role := range byRole {
		if role == aclOwnerRole || seen[role] {
			continue
		}
		missing = append(missing, role)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		roles = append(roles, missing...)
	}
	for _, role := range roles {
		privs := byRole[role]
		if len(privs) == 0 {
			continue
		}
		letters := renderACLLetters(privs, privOrder)
		if letters == "" {
			continue
		}
		// PUBLIC is a pseudo-role: PostgreSQL stores its grant with an empty
		// grantee in the aclitem ("=<privs>/postgres"), and pg_dump's
		// buildACLCommands renders an empty grantee as the keyword PUBLIC.
		// goopg records the grant under the reserved role name "public" (no real
		// role may be named that), so map it back to the empty grantee here.
		// DU-002 slice 334.
		grantee := role
		if grantee == publicPseudoRole {
			grantee = ""
		} else if disp, ok := c.roleACLDisplay[role]; ok {
			// Restore the original-case spelling so a quoted mixed-case role
			// renders its true name in the aclitem (PostgreSQL stores the
			// role's actual name, not a case-folded one). DU-002 slice 337.
			grantee = disp
		}
		// A grantee name with characters outside [A-Za-z0-9_] (e.g. a hyphen, a
		// space, or a multibyte char) must be double-quoted in the aclitem text,
		// exactly as PostgreSQL's aclitemout/putid renders it, or pg_dump's getid
		// parser stops at the first unsafe char and mis-reads the grantee. The
		// empty PUBLIC grantee and the all-alnum common case are returned
		// unchanged. DU-002 slice 336.
		grantor := aclOwnerRole
		if g, ok := c.tableACLGrantor[relOID][role]; ok && g != "" {
			grantor = g
		}
		if disp, ok := c.roleACLDisplay[grantor]; ok {
			grantor = disp
		}
		items = append(items, aclQuoteName(grantee)+"="+letters+"/"+aclQuoteName(grantor))
	}
	return "{" + strings.Join(items, ",") + "}"
}

// aclOwnerRole is the lower-cased name of the object owner in goopg's single
// bootstrap-superuser model. The owner aclitem is always listed first in a
// materialized relacl/nspacl and, absent an explicit owner-side REVOKE, renders
// the owner's full default privilege set. DU-002 slice 340.
const aclOwnerRole = "postgres"

// renderACLLetters renders a privilege set as the aclitemout letter string in
// the given canonical bit order, appending "*" after each privilege held WITH
// GRANT OPTION (DU-002 slice 332). Privileges absent from privOrder are skipped.
func renderACLLetters(privs map[string]bool, privOrder []aclPrivLetter) string {
	var letters strings.Builder
	for _, p := range privOrder {
		if withGrantOpt, ok := privs[p.keyword]; ok {
			letters.WriteByte(p.letter)
			if withGrantOpt {
				letters.WriteByte('*')
			}
		}
	}
	return letters.String()
}

// aclQuoteName reproduces PostgreSQL's putid (src/backend/utils/adt/acl.c): a
// role name used inside an aclitem is double-quoted when any byte is not
// "safe", where is_safe_acl_char(c, false) admits only ASCII alphanumerics and
// underscore (a high-bit/multibyte byte is unsafe on output). An internal
// double quote is doubled. The empty string (the PUBLIC pseudo-grantee) and an
// all-safe name are returned verbatim. DU-002 slice 336.
func aclQuoteName(s string) string {
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			b.WriteByte('"')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
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

// ForeignDataWrapper is a user-created foreign-data wrapper (CREATE FOREIGN DATA
// WRAPPER). goopg does not execute FDWs; this records just enough metadata to
// round-trip the CREATE/DROP through pg_dump (pg_foreign_data_wrapper virtual
// view → getForeignDataWrappers/dumpForeignDataWrapper). DU-002 slice 375.
type ForeignDataWrapper struct {
	Name  string // fdwname
	OID   uint32 // pg_foreign_data_wrapper.oid (assigned from the catalog OID counter)
	Owner uint32 // fdwowner; 0 → defaults to the bootstrap superuser at render time
	// Options holds the wrapper's OPTIONS as "name=value" elements, the on-disk
	// pg_foreign_data_wrapper.fdwoptions text[] representation. pg_dump's
	// getForeignDataWrappers expands these via pg_options_to_table(fdwoptions) and
	// dumpForeignDataWrapper re-emits an `OPTIONS (name 'value', …)` clause.
	// Nil/empty → no OPTIONS clause. DU-002 slice 380.
	Options []string
	// HandlerOID / ValidatorOID are pg_foreign_data_wrapper.fdwhandler/
	// fdwvalidator — the pg_proc OIDs of a `HANDLER f`/`VALIDATOR f` clause,
	// resolved by the executor (resolveFDWHandlerFunc/resolveFDWValidatorFunc)
	// at CREATE/ALTER time. 0 (InvalidOid) means no handler/validator; the
	// `::regproc` cast pg_dump's getForeignDataWrappers applies renders 0 as
	// '-', so dumpForeignDataWrapper omits the clause. DU-002 (M0119-0004).
	HandlerOID   uint32
	ValidatorOID uint32
}

// RegisterForeignDataWrapper records an FDW, allocating a stable OID on first
// sight. Idempotent: re-registering an existing name returns the existing entry
// without changing its OID (the OPTIONS are refreshed when non-empty).
// DU-002 slice 375 (options: slice 380).
func (c *InMemory) RegisterForeignDataWrapper(name string, options []string) *ForeignDataWrapper {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fdws == nil {
		c.fdws = make(map[string]*ForeignDataWrapper)
	}
	if f, ok := c.fdws[name]; ok {
		if len(options) > 0 {
			f.Options = options
		}
		return f
	}
	f := &ForeignDataWrapper{Name: name, OID: c.allocOIDLocked(), Options: options}
	c.fdws[name] = f
	return f
}

// DropForeignDataWrapper removes an FDW from the registry. Returns true if found.
// DU-002 slice 375.
func (c *InMemory) DropForeignDataWrapper(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fdws == nil {
		return false
	}
	if _, ok := c.fdws[name]; ok {
		delete(c.fdws, name)
		return true
	}
	return false
}

// ListForeignDataWrappers returns all registered FDWs sorted by name.
// DU-002 slice 375.
func (c *InMemory) ListForeignDataWrappers() []*ForeignDataWrapper {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.fdws) == 0 {
		return nil
	}
	out := make([]*ForeignDataWrapper, 0, len(c.fdws))
	for _, f := range c.fdws {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ForeignDataWrapperOID returns the stable OID of the named FDW, or 0 if no such
// FDW is registered. Used by pg_foreign_server.VirtualRows to populate srvfdw.
// DU-002 slice 376.
func (c *InMemory) ForeignDataWrapperOID(name string) uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if f, ok := c.fdws[name]; ok {
		return f.OID
	}
	return 0
}

// LookupForeignDataWrapper returns the named FDW's registry entry, or
// (nil, false) if no such FDW is registered. Unlike RegisterForeignDataWrapper
// (which creates-or-fetches), this is a read-only lookup — used by
// ALTER FOREIGN DATA WRAPPER, which must error 42704 undefined_object on a
// nonexistent name rather than silently creating one. DU-002 slice 421.
func (c *InMemory) LookupForeignDataWrapper(name string) (*ForeignDataWrapper, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, ok := c.fdws[name]
	return f, ok
}

// AccessMethod is a user-created access method (CREATE ACCESS METHOD). goopg
// never invokes a user-defined AM (no pluggable table/index storage engine);
// this records just enough metadata to round-trip the CREATE through pg_dump
// (pg_am virtual view → getAccessMethods/dumpAccessMethod). DU-002
// (M0119-0004).
type AccessMethod struct {
	Name       string // amname
	OID        uint32 // pg_am.oid (assigned from the catalog OID counter)
	AMType     string // pg_am.amtype: "i" (INDEX) or "t" (TABLE)
	HandlerOID uint32 // pg_am.amhandler — FK to pg_proc
}

// RegisterAccessMethod records a user-defined access method. Returns an error
// if the name collides with a built-in AM (pg_am's static 7 rows) or an
// already-registered user AM — mirrors PostgreSQL's own duplicate-name check
// in CreateAccessMethod (amcmds.c), which errors before this ever reaches the
// catalog. DU-002 (M0119-0004).
func (c *InMemory) RegisterAccessMethod(name, amType string, handlerOID uint32) (*AccessMethod, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if AccessMethodOIDByName(name) != 0 {
		return nil, fmt.Errorf("access method %q already exists", name)
	}
	if c.accessMethods == nil {
		c.accessMethods = make(map[string]*AccessMethod)
	}
	if _, ok := c.accessMethods[name]; ok {
		return nil, fmt.Errorf("access method %q already exists", name)
	}
	am := &AccessMethod{Name: name, OID: c.allocOIDLocked(), AMType: amType, HandlerOID: handlerOID}
	c.accessMethods[name] = am
	return am, nil
}

// DropAccessMethod removes a user-defined access method from the registry.
// Returns true if found. DU-002 (M0119-0004).
func (c *InMemory) DropAccessMethod(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessMethods == nil {
		return false
	}
	if _, ok := c.accessMethods[name]; ok {
		delete(c.accessMethods, name)
		return true
	}
	return false
}

// ListAccessMethods returns all registered user-defined access methods
// sorted by name. DU-002 (M0119-0004).
func (c *InMemory) ListAccessMethods() []*AccessMethod {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.accessMethods) == 0 {
		return nil
	}
	out := make([]*AccessMethod, 0, len(c.accessMethods))
	for _, am := range c.accessMethods {
		out = append(out, am)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UserAccessMethodOID returns the stable OID of the named user-defined access
// method, or 0 if no such AM is registered (including the 7 built-ins, which
// are not tracked in this map — see AccessMethodOIDByName for those). Used by
// COMMENT ON ACCESS METHOD to resolve the pg_am.oid to key the pg_description
// row on, mirroring ForeignServerOID/ForeignDataWrapperOID/ExtensionOID.
// DU-002 slice 434.
func (c *InMemory) UserAccessMethodOID(name string) uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if am, ok := c.accessMethods[name]; ok {
		return am.OID
	}
	return 0
}

// RegisterAccessMethodDuringRecovery is the idempotent version of
// RegisterAccessMethod used by the WAL-replay driver
// (internal/initdb/access_method_ddl_recovery.go). Unlike
// RegisterAccessMethod it takes the OID from the WAL record (so the
// recovered access method matches the pre-crash OID exactly) and overwrites
// rather than erroring when an access method with the same name is already
// present (replay may see the same record more than once across a
// partial-then-full replay). Mirrors catalog.InMemory.
// RegisterEventTriggerDuringRecovery. DU-002 restart-persistence follow-up
// (M0119-0004, DU-002 slice 426 ledger resume point).
func (c *InMemory) RegisterAccessMethodDuringRecovery(am *AccessMethod) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessMethods == nil {
		c.accessMethods = make(map[string]*AccessMethod)
	}
	out := *am
	c.accessMethods[am.Name] = &out
	c.advanceNextOIDLocked(am.OID)
}

// DropAccessMethodDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropAccessMethod. Identical to DropAccessMethod but
// discards the found/not-found result — replay does not care whether the
// access method was still present. DU-002 restart-persistence follow-up
// (M0119-0004, DU-002 slice 426 ledger resume point).
func (c *InMemory) DropAccessMethodDuringRecovery(name string) {
	_ = c.DropAccessMethod(name)
}

// EventTrigger is a user-created event trigger (CREATE EVENT TRIGGER). goopg
// never fires event triggers (no DDL hook invokes evtfoid); this records just
// enough metadata to round-trip the CREATE/DROP through pg_dump
// (pg_event_trigger virtual view → getEventTriggers/dumpEventTrigger).
// DU-002 (M0119-0004).
type EventTrigger struct {
	Name    string // evtname
	OID     uint32 // pg_event_trigger.oid (assigned from the catalog OID counter)
	Event   string // evtevent: ddl_command_start, ddl_command_end, sql_drop, table_rewrite, login
	Owner   uint32 // evtowner; 0 → defaults to the bootstrap superuser at render time
	FuncOID uint32 // evtfoid, resolved at CREATE time (must exist — no deferred/dangling reference)
	// Enabled mirrors pg_event_trigger.evtenabled: 'O' (origin, the CREATE-time
	// default), 'D' disabled, 'A' always, 'R' replica. Mutated by ALTER EVENT
	// TRIGGER ENABLE/DISABLE (DU-002, M0119-0004 loop #69 ledger follow-up).
	Enabled string
	// Tags holds the WHEN TAG IN (...) filter values verbatim (unquoted); nil
	// if the CREATE had no WHEN clause. dumpEventTrigger re-quotes each one.
	Tags []string
}

// ErrEventTriggerNotFound / ErrEventTriggerAlreadyExists are returned by the
// EventTrigger registry mutators (Register/Rename/SetEventTrigger*) so
// callers can map to PostgreSQL's 42704 undefined_object / 42710
// duplicate_object respectively. DU-002 (M0119-0004, loop #69 ledger
// follow-up).
var (
	ErrEventTriggerNotFound      = errors.New("event trigger does not exist")
	ErrEventTriggerAlreadyExists = errors.New("event trigger already exists")
)

// RegisterEventTrigger records an event trigger, allocating a stable OID.
// Returns ErrEventTriggerAlreadyExists if a trigger with this name already
// exists. DU-002 (M0119-0004).
func (c *InMemory) RegisterEventTrigger(name, event string, owner, funcOID uint32, tags []string) (*EventTrigger, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eventTriggers == nil {
		c.eventTriggers = make(map[string]*EventTrigger)
	}
	if _, exists := c.eventTriggers[name]; exists {
		return nil, fmt.Errorf("event trigger %q already exists: %w", name, ErrEventTriggerAlreadyExists)
	}
	et := &EventTrigger{
		Name:    name,
		OID:     c.allocOIDLocked(),
		Event:   event,
		Owner:   owner,
		FuncOID: funcOID,
		Enabled: "O",
		Tags:    tags,
	}
	c.eventTriggers[name] = et
	return et, nil
}

// DropEventTrigger removes an event trigger from the registry. Returns true
// if found. DU-002 (M0119-0004).
func (c *InMemory) DropEventTrigger(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eventTriggers == nil {
		return false
	}
	if _, ok := c.eventTriggers[name]; ok {
		delete(c.eventTriggers, name)
		return true
	}
	return false
}

// ListEventTriggers returns all registered event triggers ordered by OID,
// matching pg_dump's getEventTriggers "ORDER BY e.oid". DU-002 (M0119-0004).
func (c *InMemory) ListEventTriggers() []*EventTrigger {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.eventTriggers) == 0 {
		return nil
	}
	out := make([]*EventTrigger, 0, len(c.eventTriggers))
	for _, et := range c.eventTriggers {
		out = append(out, et)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// LookupEventTrigger returns a deep copy of the named event trigger, or
// (nil, false) if it does not exist. DU-002 restart-persistence follow-up
// (M0119-0004, loop #70 ledger resume point) — mirrors PubSub.
// LookupPublication.
func (c *InMemory) LookupEventTrigger(name string) (*EventTrigger, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	et, ok := c.eventTriggers[name]
	if !ok {
		return nil, false
	}
	out := *et
	out.Tags = append([]string(nil), et.Tags...)
	return &out, true
}

// SetEventTriggerEnabled updates an event trigger's evtenabled state. code
// must be one of "O" (enable), "D" (disable), "A" (enable always), "R"
// (enable replica) — the four values PostgreSQL's pg_event_trigger.evtenabled
// column takes. Backs ALTER EVENT TRIGGER name {ENABLE|DISABLE}. Returns an
// error if name is unknown (42704 undefined_object at the call site). DU-002
// (M0119-0004, loop #69 ledger follow-up).
func (c *InMemory) SetEventTriggerEnabled(name, code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	et, ok := c.eventTriggers[name]
	if !ok {
		return fmt.Errorf("event trigger %q does not exist: %w", name, ErrEventTriggerNotFound)
	}
	et.Enabled = code
	return nil
}

// SetEventTriggerOwner updates an event trigger's owner OID. Backs ALTER
// EVENT TRIGGER name OWNER TO newowner. DU-002 (M0119-0004, loop #69 ledger
// follow-up).
func (c *InMemory) SetEventTriggerOwner(name string, owner uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	et, ok := c.eventTriggers[name]
	if !ok {
		return fmt.Errorf("event trigger %q does not exist: %w", name, ErrEventTriggerNotFound)
	}
	et.Owner = owner
	return nil
}

// RenameEventTrigger renames an event trigger, re-keying the registry map.
// Backs ALTER EVENT TRIGGER name RENAME TO newname. Returns an error if name
// is unknown or newname is already taken (42710 duplicate_object at the call
// site, mirroring RegisterEventTrigger). DU-002 (M0119-0004, loop #69 ledger
// follow-up).
func (c *InMemory) RenameEventTrigger(name, newName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	et, ok := c.eventTriggers[name]
	if !ok {
		return fmt.Errorf("event trigger %q does not exist: %w", name, ErrEventTriggerNotFound)
	}
	if _, exists := c.eventTriggers[newName]; exists {
		return fmt.Errorf("event trigger %q already exists: %w", newName, ErrEventTriggerAlreadyExists)
	}
	delete(c.eventTriggers, name)
	et.Name = newName
	c.eventTriggers[newName] = et
	return nil
}

// RegisterEventTriggerDuringRecovery is the idempotent version of
// RegisterEventTrigger used by the WAL-replay driver
// (internal/initdb/event_trigger_ddl_recovery.go). Unlike
// RegisterEventTrigger it takes the OID from the WAL record (so the
// recovered event trigger matches the pre-crash OID exactly) and
// overwrites rather than erroring when a trigger with the same name is
// already present (replay may see the same record more than once across a
// partial-then-full replay). Mirrors catalog.PubSub.
// CreatePublicationDuringRecovery. DU-002 restart-persistence follow-up
// (M0119-0004, loop #70 ledger resume point).
func (c *InMemory) RegisterEventTriggerDuringRecovery(et *EventTrigger) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.eventTriggers == nil {
		c.eventTriggers = make(map[string]*EventTrigger)
	}
	out := *et
	out.Tags = append([]string(nil), et.Tags...)
	c.eventTriggers[et.Name] = &out
	c.advanceNextOIDLocked(et.OID)
}

// DropEventTriggerDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropEventTrigger. Identical to DropEventTrigger but
// discards the found/not-found result — replay does not care whether the
// event trigger was still present. DU-002 restart-persistence follow-up
// (M0119-0004, loop #70 ledger resume point).
func (c *InMemory) DropEventTriggerDuringRecovery(name string) {
	_ = c.DropEventTrigger(name)
}

// SetEventTriggerEnabledDuringRecovery is the discard-error recovery
// counterpart to SetEventTriggerEnabled, mirroring
// DropEventTriggerDuringRecovery. DU-002 restart-persistence follow-up
// (M0119-0004, loop #70 ledger resume point).
func (c *InMemory) SetEventTriggerEnabledDuringRecovery(name, code string) {
	_ = c.SetEventTriggerEnabled(name, code)
}

// RenameEventTriggerDuringRecovery is the discard-error recovery
// counterpart to RenameEventTrigger. DU-002 restart-persistence follow-up
// (M0119-0004, loop #70 ledger resume point).
func (c *InMemory) RenameEventTriggerDuringRecovery(name, newName string) {
	_ = c.RenameEventTrigger(name, newName)
}

// SetEventTriggerOwnerDuringRecovery is the discard-error recovery
// counterpart to SetEventTriggerOwner. DU-002 restart-persistence follow-up
// (M0119-0004, loop #70 ledger resume point).
func (c *InMemory) SetEventTriggerOwnerDuringRecovery(name string, owner uint32) {
	_ = c.SetEventTriggerOwner(name, owner)
}

// ForeignServer is a user-created foreign server (CREATE SERVER). goopg does not
// execute foreign servers; this records just enough metadata to round-trip the
// CREATE/DROP through pg_dump (pg_foreign_server virtual view →
// getForeignServers/dumpForeignServer). DU-002 slice 376.
// quoteArrayElement renders a single text[] element using PostgreSQL's array_out
// quoting rules (array_out / ARRAY_QUOTE in src/backend/utils/adt/arrayfuncs.c).
// An element is wrapped in double quotes — with embedded `"` and `\` backslash-
// escaped — when it is empty, equals the word NULL case-insensitively, or
// contains a double-quote, backslash, brace, the element delimiter (comma for
// text[]), or ASCII whitespace; otherwise it is emitted bare. Without this an
// option value carrying array metacharacters (e.g. host 'a,b' → element
// `host=a,b`) would be split on its embedded comma when pg_dump re-parses the
// srvoptions/fdwoptions/umoptions text[] via pg_options_to_table, corrupting the
// round-trip. DU-002 slice 384.
func quoteArrayElement(s string) string {
	needquote := s == "" || strings.EqualFold(s, "NULL")
	if !needquote {
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '"', '\\', '{', '}', ',', ' ', '\t', '\n', '\r', '\v', '\f':
				needquote = true
			}
			if needquote {
				break
			}
		}
	}
	if !needquote {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}

// BuildTableReloptions renders a table's or view's storage parameters as the
// PostgreSQL pg_class.reloptions text[] external literal (e.g.
// "{fillfactor=70}"), or "" when the table carries none (planner/heap-encode
// callers must map "" to SQL NULL themselves — see arrayTextLiteral's
// contract). This is the single source of truth for reloptions so the live
// virtual pg_class row (registerSystemTables' VirtualRows) and the
// heap-persisted row written for restart durability (executor's
// buildUserPGClassRow) never drift apart (M0119-0004: buildUserPGClassRow
// used to hardcode "{}", silently losing every reloption across a restart).
//
// Element order matches PostgreSQL's own stored order exactly: fillfactor,
// parallel_workers, autovacuum_enabled, toast_tuple_target, then the
// autovacuum_* family, vacuum_truncate, log_autovacuum_min_duration, the
// autovacuum_*freeze* family, autovacuum_vacuum_cost_limit,
// user_catalog_table, autovacuum_vacuum_max_threshold,
// vacuum_max_eager_freeze_failure_rate, vacuum_index_cleanup, then (views
// only) security_barrier, security_invoker, check_option.
func BuildTableReloptions(t *Table) string {
	relopts := TableReloptionsElements(t)
	if len(relopts) == 0 {
		return ""
	}
	return arrayTextLiteral(relopts)
}

// TableReloptionsElements is BuildTableReloptions's element-list form: the
// raw "key=value" strings before joining into the text[] external literal.
// Physical heap-tuple encoding needs the individual elements (to build a
// proper PG ArrayType blob via pgTextArrayBytes), not the pre-joined
// "{a,b}" string BuildTableReloptions returns for the live virtual pg_class
// row and the planner's NULL-vs-non-empty check. M0119-0004.
func TableReloptionsElements(t *Table) []string {
	var relopts []string
	if t.Fillfactor != 0 {
		relopts = append(relopts, "fillfactor="+strconv.Itoa(t.Fillfactor))
	}
	if t.ParallelWorkersSet {
		relopts = append(relopts, "parallel_workers="+strconv.Itoa(t.ParallelWorkers))
	}
	if t.AutovacuumEnabledSet {
		relopts = append(relopts, "autovacuum_enabled="+strconv.FormatBool(t.AutovacuumEnabled))
	}
	if t.ToastTupleTarget != 0 {
		relopts = append(relopts, "toast_tuple_target="+strconv.Itoa(t.ToastTupleTarget))
	}
	if t.AutovacuumVacuumThresholdSet {
		relopts = append(relopts, "autovacuum_vacuum_threshold="+strconv.Itoa(t.AutovacuumVacuumThreshold))
	}
	if t.AutovacuumVacuumScaleFactorSet {
		relopts = append(relopts, "autovacuum_vacuum_scale_factor="+strconv.FormatFloat(t.AutovacuumVacuumScaleFactor, 'g', -1, 64))
	}
	if t.AutovacuumAnalyzeScaleFactorSet {
		relopts = append(relopts, "autovacuum_analyze_scale_factor="+strconv.FormatFloat(t.AutovacuumAnalyzeScaleFactor, 'g', -1, 64))
	}
	if t.AutovacuumVacuumInsertScaleFactorSet {
		relopts = append(relopts, "autovacuum_vacuum_insert_scale_factor="+strconv.FormatFloat(t.AutovacuumVacuumInsertScaleFactor, 'g', -1, 64))
	}
	if t.AutovacuumVacuumCostDelaySet {
		relopts = append(relopts, "autovacuum_vacuum_cost_delay="+strconv.FormatFloat(t.AutovacuumVacuumCostDelay, 'g', -1, 64))
	}
	if t.AutovacuumAnalyzeThresholdSet {
		relopts = append(relopts, "autovacuum_analyze_threshold="+strconv.Itoa(t.AutovacuumAnalyzeThreshold))
	}
	if t.AutovacuumVacuumInsertThresholdSet {
		relopts = append(relopts, "autovacuum_vacuum_insert_threshold="+strconv.Itoa(t.AutovacuumVacuumInsertThreshold))
	}
	if t.VacuumTruncateSet {
		relopts = append(relopts, "vacuum_truncate="+strconv.FormatBool(t.VacuumTruncate))
	}
	if t.LogAutovacuumMinDurationSet {
		relopts = append(relopts, "log_autovacuum_min_duration="+strconv.Itoa(t.LogAutovacuumMinDuration))
	}
	if t.AutovacuumFreezeMinAgeSet {
		relopts = append(relopts, "autovacuum_freeze_min_age="+strconv.Itoa(t.AutovacuumFreezeMinAge))
	}
	if t.AutovacuumFreezeMaxAgeSet {
		relopts = append(relopts, "autovacuum_freeze_max_age="+strconv.Itoa(t.AutovacuumFreezeMaxAge))
	}
	if t.AutovacuumFreezeTableAgeSet {
		relopts = append(relopts, "autovacuum_freeze_table_age="+strconv.Itoa(t.AutovacuumFreezeTableAge))
	}
	if t.AutovacuumMultixactFreezeMinAgeSet {
		relopts = append(relopts, "autovacuum_multixact_freeze_min_age="+strconv.Itoa(t.AutovacuumMultixactFreezeMinAge))
	}
	if t.AutovacuumMultixactFreezeMaxAgeSet {
		relopts = append(relopts, "autovacuum_multixact_freeze_max_age="+strconv.Itoa(t.AutovacuumMultixactFreezeMaxAge))
	}
	if t.AutovacuumMultixactFreezeTableAgeSet {
		relopts = append(relopts, "autovacuum_multixact_freeze_table_age="+strconv.Itoa(t.AutovacuumMultixactFreezeTableAge))
	}
	if t.AutovacuumVacuumCostLimitSet {
		relopts = append(relopts, "autovacuum_vacuum_cost_limit="+strconv.Itoa(t.AutovacuumVacuumCostLimit))
	}
	if t.UserCatalogTableSet {
		relopts = append(relopts, "user_catalog_table="+strconv.FormatBool(t.UserCatalogTable))
	}
	if t.AutovacuumVacuumMaxThresholdSet {
		relopts = append(relopts, "autovacuum_vacuum_max_threshold="+strconv.Itoa(t.AutovacuumVacuumMaxThreshold))
	}
	if t.VacuumMaxEagerFreezeFailureRateSet {
		relopts = append(relopts, "vacuum_max_eager_freeze_failure_rate="+strconv.FormatFloat(t.VacuumMaxEagerFreezeFailureRate, 'g', -1, 64))
	}
	if t.VacuumIndexCleanupSet {
		relopts = append(relopts, "vacuum_index_cleanup="+t.VacuumIndexCleanup)
	}
	// Views: security_barrier / security_invoker / check_option. See the
	// per-field comments at their catalog.Table struct definitions for why
	// this order matches PostgreSQL's stored order.
	if t.SecurityBarrierSet {
		relopts = append(relopts, "security_barrier="+strconv.FormatBool(t.SecurityBarrier))
	}
	if t.SecurityInvokerSet {
		relopts = append(relopts, "security_invoker="+strconv.FormatBool(t.SecurityInvoker))
	}
	if t.CheckOption != "" {
		relopts = append(relopts, "check_option="+t.CheckOption)
	}
	return relopts
}

// ApplyTableReloptions is BuildTableReloptions's inverse: given a
// pg_class.reloptions text[] external literal in the exact form
// BuildTableReloptions itself produces (e.g. "{fillfactor=70,check_option=local}"),
// it sets the corresponding fields on t. Used by loadUserTablesFromHeap to
// restore a table's/view's storage parameters from the heap-persisted pg_class
// row after a restart — without this, buildUserPGClassRow's reloptions column
// was decoded and silently discarded, so e.g. `WITH (fillfactor=70)` or a
// view's `WITH LOCAL CHECK OPTION` reverted to defaults across a restart
// (M0119-0004). This is intentionally not a general PG array-literal parser
// (no quote/escape handling): goopg only ever needs to round-trip its own
// emitted content here, and every value BuildTableReloptions emits is a bare
// number/bool/enum token that never needs quoting (quoteArrayElement).
// Malformed/unknown keys are silently ignored so a forward-compatible reader
// tolerates options written by a newer goopg version.
func ApplyTableReloptions(t *Table, text string) {
	if len(text) < 2 || text[0] != '{' || text[len(text)-1] != '}' {
		return
	}
	inner := text[1 : len(text)-1]
	if inner == "" {
		return
	}
	for _, kv := range strings.Split(inner, ",") {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch key {
		case "fillfactor":
			t.Fillfactor, _ = strconv.Atoi(val)
		case "parallel_workers":
			t.ParallelWorkers, _ = strconv.Atoi(val)
			t.ParallelWorkersSet = true
		case "autovacuum_enabled":
			t.AutovacuumEnabled = val == "true"
			t.AutovacuumEnabledSet = true
		case "toast_tuple_target":
			t.ToastTupleTarget, _ = strconv.Atoi(val)
		case "autovacuum_vacuum_threshold":
			t.AutovacuumVacuumThreshold, _ = strconv.Atoi(val)
			t.AutovacuumVacuumThresholdSet = true
		case "autovacuum_vacuum_scale_factor":
			t.AutovacuumVacuumScaleFactor, _ = strconv.ParseFloat(val, 64)
			t.AutovacuumVacuumScaleFactorSet = true
		case "autovacuum_analyze_scale_factor":
			t.AutovacuumAnalyzeScaleFactor, _ = strconv.ParseFloat(val, 64)
			t.AutovacuumAnalyzeScaleFactorSet = true
		case "autovacuum_vacuum_insert_scale_factor":
			t.AutovacuumVacuumInsertScaleFactor, _ = strconv.ParseFloat(val, 64)
			t.AutovacuumVacuumInsertScaleFactorSet = true
		case "autovacuum_vacuum_cost_delay":
			t.AutovacuumVacuumCostDelay, _ = strconv.ParseFloat(val, 64)
			t.AutovacuumVacuumCostDelaySet = true
		case "autovacuum_analyze_threshold":
			t.AutovacuumAnalyzeThreshold, _ = strconv.Atoi(val)
			t.AutovacuumAnalyzeThresholdSet = true
		case "autovacuum_vacuum_insert_threshold":
			t.AutovacuumVacuumInsertThreshold, _ = strconv.Atoi(val)
			t.AutovacuumVacuumInsertThresholdSet = true
		case "vacuum_truncate":
			t.VacuumTruncate = val == "true"
			t.VacuumTruncateSet = true
		case "log_autovacuum_min_duration":
			t.LogAutovacuumMinDuration, _ = strconv.Atoi(val)
			t.LogAutovacuumMinDurationSet = true
		case "autovacuum_freeze_min_age":
			t.AutovacuumFreezeMinAge, _ = strconv.Atoi(val)
			t.AutovacuumFreezeMinAgeSet = true
		case "autovacuum_freeze_max_age":
			t.AutovacuumFreezeMaxAge, _ = strconv.Atoi(val)
			t.AutovacuumFreezeMaxAgeSet = true
		case "autovacuum_freeze_table_age":
			t.AutovacuumFreezeTableAge, _ = strconv.Atoi(val)
			t.AutovacuumFreezeTableAgeSet = true
		case "autovacuum_multixact_freeze_min_age":
			t.AutovacuumMultixactFreezeMinAge, _ = strconv.Atoi(val)
			t.AutovacuumMultixactFreezeMinAgeSet = true
		case "autovacuum_multixact_freeze_max_age":
			t.AutovacuumMultixactFreezeMaxAge, _ = strconv.Atoi(val)
			t.AutovacuumMultixactFreezeMaxAgeSet = true
		case "autovacuum_multixact_freeze_table_age":
			t.AutovacuumMultixactFreezeTableAge, _ = strconv.Atoi(val)
			t.AutovacuumMultixactFreezeTableAgeSet = true
		case "autovacuum_vacuum_cost_limit":
			t.AutovacuumVacuumCostLimit, _ = strconv.Atoi(val)
			t.AutovacuumVacuumCostLimitSet = true
		case "user_catalog_table":
			t.UserCatalogTable = val == "true"
			t.UserCatalogTableSet = true
		case "autovacuum_vacuum_max_threshold":
			t.AutovacuumVacuumMaxThreshold, _ = strconv.Atoi(val)
			t.AutovacuumVacuumMaxThresholdSet = true
		case "vacuum_max_eager_freeze_failure_rate":
			t.VacuumMaxEagerFreezeFailureRate, _ = strconv.ParseFloat(val, 64)
			t.VacuumMaxEagerFreezeFailureRateSet = true
		case "vacuum_index_cleanup":
			t.VacuumIndexCleanup = val
			t.VacuumIndexCleanupSet = true
		case "security_barrier":
			t.SecurityBarrier = val == "true"
			t.SecurityBarrierSet = true
		case "security_invoker":
			t.SecurityInvoker = val == "true"
			t.SecurityInvokerSet = true
		case "check_option":
			t.CheckOption = val
		}
	}
}

// BuildIndexReloptions renders an index's storage parameters as the
// PostgreSQL pg_class.reloptions text[] external literal (e.g.
// "{fillfactor=70,fastupdate=off}"), or "" when the index carries none. This
// is the single source of truth for index reloptions so the live virtual
// pg_class row (registerSystemTables' VirtualRows, via idx.reloptionList())
// and the heap-persisted row written for restart durability
// (executor.buildUserPGClassRowForIndex) never drift apart — mirrors
// BuildTableReloptions/TableReloptionsElements for tables/views (M0119-0004
// index-reloptions follow-up: buildUserPGClassRowForIndex used to hardcode
// "{}", silently losing fillfactor/fastupdate/gin_pending_list_limit/
// pages_per_range/autosummarize/deduplicate_items across a restart).
func BuildIndexReloptions(idx *Index) string {
	relopts := IndexReloptionsElements(idx)
	if len(relopts) == 0 {
		return ""
	}
	return arrayTextLiteral(relopts)
}

// IndexReloptionsElements is BuildIndexReloptions's element-list form: the
// raw "key=value" strings before joining into the text[] external literal.
// Physical heap-tuple encoding needs the individual elements (to build a
// proper PG ArrayType blob via pgTextArrayBytes), not the pre-joined "{a,b}"
// string BuildIndexReloptions returns — matches TableReloptionsElements's
// contract for tables.
func IndexReloptionsElements(idx *Index) []string {
	opts := idx.reloptionList()
	if len(opts) == 0 {
		return nil
	}
	relopts := make([]string, len(opts))
	for i, kv := range opts {
		relopts[i] = kv[0] + "=" + kv[1]
	}
	return relopts
}

// ApplyIndexReloptions is BuildIndexReloptions's inverse: given a
// pg_class.reloptions text[] external literal in the exact form
// BuildIndexReloptions itself produces (e.g.
// "{fillfactor=70,fastupdate=off}"), it sets the corresponding fields on idx.
// Used by loadUserIndexesFromHeap to restore an index's storage parameters
// from the heap-persisted pg_class row after a restart — without this, an
// index's fillfactor/deduplicate_items/fastupdate/gin_pending_list_limit/
// pages_per_range/autosummarize silently reverted to defaults across every
// restart (the index-reloptions residual left open by M0119-0004's
// table/view reloptions fix). Mirrors ApplyTableReloptions's contract: no
// general array-literal parsing — goopg only ever needs to round-trip its
// own emitted content here, and every value BuildIndexReloptions emits is a
// bare number/on/off token that never needs quoting. Malformed/unknown keys
// are silently ignored so a forward-compatible reader tolerates options
// written by a newer goopg version.
func ApplyIndexReloptions(idx *Index, text string) {
	if len(text) < 2 || text[0] != '{' || text[len(text)-1] != '}' {
		return
	}
	inner := text[1 : len(text)-1]
	if inner == "" {
		return
	}
	for _, kv := range strings.Split(inner, ",") {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch key {
		case "fillfactor":
			idx.Fillfactor, _ = strconv.Atoi(val)
		case "deduplicate_items":
			v := val == "on"
			idx.DeduplicateItems = &v
		case "fastupdate":
			v := val == "on"
			idx.FastUpdate = &v
		case "gin_pending_list_limit":
			idx.GinPendingListLimit, _ = strconv.Atoi(val)
		case "pages_per_range":
			idx.PagesPerRange, _ = strconv.Atoi(val)
		case "autosummarize":
			v := val == "on"
			idx.AutoSummarize = &v
		}
	}
}

// ArrayTextLiteral exports arrayTextLiteral for executor's physical
// text[]/ArrayType decoder (codec.go), which must join a decoded ArrayType
// blob's elements back into the same "{elem,elem,…}" external-literal form
// BuildTableReloptions/TableReloptionsElements produce, so a decoded
// pg_class.reloptions round-trips byte-for-format-identical through
// catalog.ApplyTableReloptions. M0119-0004.
func ArrayTextLiteral(parts []string) string { return arrayTextLiteral(parts) }

// arrayTextLiteral renders elements as a PostgreSQL text[] external literal
// ("{elem,elem,…}"), quoting each element per array_out's rules (see
// quoteArrayElement). Callers must guard the empty case themselves.
func arrayTextLiteral(parts []string) string {
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = quoteArrayElement(p)
	}
	return "{" + strings.Join(quoted, ",") + "}"
}

// optionsArrayLiteral renders a list of "name=value" option elements as the
// PostgreSQL text[] external literal (e.g. {host=localhost,dbname=mydb}) that
// pg_dump's pg_options_to_table(srvoptions) SRF expands back into one
// (option_name, option_value) row per element. An empty list yields "" (SQL
// NULL, so no OPTIONS clause is emitted). Each element is quoted per PG's
// array_out rules (quoteArrayElement), so option values containing array
// metacharacters (comma, brace, space, quote, backslash) round-trip intact.
// DU-002 slice 378 (metachar quoting: slice 384).
func optionsArrayLiteral(opts []string) string {
	if len(opts) == 0 {
		return ""
	}
	return arrayTextLiteral(opts)
}

type ForeignServer struct {
	Name    string // srvname
	OID     uint32 // pg_foreign_server.oid (assigned from the catalog OID counter)
	Owner   uint32 // srvowner; 0 → defaults to the bootstrap superuser at render time
	FdwName string // the referenced FDW name; resolved to srvfdw OID at render time
	// Type / Version hold the CREATE SERVER TYPE 'x' / VERSION 'y' clauses
	// (pg_foreign_server.srvtype / srvversion). Empty → SQL NULL, so
	// dumpForeignServer omits the corresponding TYPE/VERSION clause. DU-002 slice 381.
	Type    string
	Version string
	// Options holds the server's OPTIONS as "name=value" elements, the on-disk
	// pg_foreign_server.srvoptions text[] representation. pg_dump's
	// getForeignServers expands these via pg_options_to_table(srvoptions) and
	// dumpForeignServer re-emits an `OPTIONS (name 'value', …)` clause. Nil/empty
	// → no OPTIONS clause. DU-002 slice 378.
	Options []string
}

// RegisterForeignServer records a foreign server, allocating a stable OID on
// first sight. Idempotent: re-registering an existing name returns the existing
// entry without changing its OID (the FDW association, TYPE/VERSION, and OPTIONS
// are refreshed when non-empty). DU-002 slice 376 (options: slice 378;
// type/version: slice 381).
func (c *InMemory) RegisterForeignServer(name, fdwName, srvType, srvVersion string, options []string) *ForeignServer {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.foreignServers == nil {
		c.foreignServers = make(map[string]*ForeignServer)
	}
	if s, ok := c.foreignServers[name]; ok {
		if fdwName != "" {
			s.FdwName = fdwName
		}
		if srvType != "" {
			s.Type = srvType
		}
		if srvVersion != "" {
			s.Version = srvVersion
		}
		if len(options) > 0 {
			s.Options = options
		}
		return s
	}
	s := &ForeignServer{Name: name, OID: c.allocOIDLocked(), FdwName: fdwName, Type: srvType, Version: srvVersion, Options: options}
	c.foreignServers[name] = s
	return s
}

// DropForeignServer removes a foreign server from the registry. Returns true if
// found. DU-002 slice 376.
func (c *InMemory) DropForeignServer(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.foreignServers == nil {
		return false
	}
	if _, ok := c.foreignServers[name]; ok {
		delete(c.foreignServers, name)
		return true
	}
	return false
}

// ListForeignServers returns all registered foreign servers sorted by name.
// DU-002 slice 376.
func (c *InMemory) ListForeignServers() []*ForeignServer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.foreignServers) == 0 {
		return nil
	}
	out := make([]*ForeignServer, 0, len(c.foreignServers))
	for _, s := range c.foreignServers {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ForeignServerOID returns the stable OID of the named foreign server, or 0 if
// no such server is registered. Used by pg_user_mappings.VirtualRows to populate
// srvid (the column pg_dump's dumpUserMappings filters on). DU-002 slice 377.
func (c *InMemory) ForeignServerOID(name string) uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if s, ok := c.foreignServers[name]; ok {
		return s.OID
	}
	return 0
}

// Cast is a user-defined cast (CREATE CAST (source AS target) …). goopg does not
// actually perform user casts; this records just enough metadata to round-trip
// the definition through pg_dump (pg_cast virtual view → getCasts/dumpCast).
// DU-002 slice 395.
type Cast struct {
	OID        uint32 // pg_cast.oid (assigned from the catalog OID counter; > last builtin so pg_dump dumps it)
	SourceType string // the parsed source type name; resolved to castsource OID via TypeNameToOID at render time
	TargetType string // the parsed target type name; resolved to casttarget OID at render time
	// FuncOID is pg_cast.castfunc. 0 for WITHOUT FUNCTION (binary-coercible) and
	// WITH INOUT casts — the only forms this slice models. WITH FUNCTION casts
	// reference a pg_proc OID and are deferred (dumpCast's findFuncByOid would need
	// a matching pg_proc row).
	FuncOID uint32
	// Context is pg_cast.castcontext: 'e' explicit (default), 'a' assignment, 'i'
	// implicit. dumpCast emits ` AS ASSIGNMENT` / ` AS IMPLICIT` for 'a' / 'i'.
	Context string
	// Method is pg_cast.castmethod: 'b' binary (WITHOUT FUNCTION), 'i' INOUT
	// (WITH INOUT), 'f' function (WITH FUNCTION). dumpCast renders the matching
	// `WITHOUT FUNCTION` / `WITH INOUT` / `WITH FUNCTION …` clause.
	Method string
}

// castKey builds the cast registry's lookup key from a (source, target) type
// name pair. Real PG's pg_cast.castsource/casttarget are OIDs, not text, so
// "real" and "float4" (or "integer" and "int4") name the same cast — keying
// on TypeNameToOID rather than the raw parsed spelling makes CREATE CAST
// (float4 AS text) and DROP CAST (real AS text) cross-resolve the same entry,
// matching how the pg_cast virtual view already renders castsource/casttarget
// (RegisterCast's registerer, ~getFormattedTypeName call site above).
func castKey(source, target string) string {
	return castKeyTypeName(source) + "\x00" + castKeyTypeName(target)
}

// castKeyTypeName canonicalizes a single type name for castKey. TypeNameToOID
// falls back to OIDText for any name it doesn't recognize as a builtin
// synonym (its documented "safe fallback"), so naively keying on its result
// would collapse every distinct user-defined type (domain, enum, composite)
// into the same OIDText bucket and let unrelated custom-type casts overwrite
// each other in the registry. Only "text" itself legitimately resolves to
// OIDText; anything else landing there is the fallback, so keep the
// lowercased name verbatim to preserve per-type distinctness.
func castKeyTypeName(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	oid := TypeNameToOID(lower)
	if oid == OIDText && lower != "text" {
		return lower
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// RegisterCast records a user-defined cast, allocating a stable OID on first
// sight. Idempotent: re-registering the same (source, target) pair refreshes the
// context/method/funcOID but keeps the OID. funcOID is pg_cast.castfunc — 0 for
// WITHOUT FUNCTION / WITH INOUT, or the resolved pg_proc OID for WITH FUNCTION
// casts (DU-002 slice 397). DU-002 slice 395.
func (c *InMemory) RegisterCast(source, target, context, method string, funcOID uint32) *Cast {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.casts == nil {
		c.casts = make(map[string]*Cast)
	}
	key := castKey(source, target)
	if cs, ok := c.casts[key]; ok {
		if context != "" {
			cs.Context = context
		}
		if method != "" {
			cs.Method = method
		}
		cs.FuncOID = funcOID
		return cs
	}
	cs := &Cast{OID: c.allocOIDLocked(), SourceType: source, TargetType: target, Context: context, Method: method, FuncOID: funcOID}
	c.casts[key] = cs
	return cs
}

// DropCast removes a user-defined cast from the registry. Returns true if found.
// DU-002 slice 395.
func (c *InMemory) DropCast(source, target string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.casts == nil {
		return false
	}
	key := castKey(source, target)
	if _, ok := c.casts[key]; ok {
		delete(c.casts, key)
		return true
	}
	return false
}

// ListCasts returns all registered user casts sorted by OID (stable creation
// order). DU-002 slice 395.
func (c *InMemory) ListCasts() []*Cast {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.casts) == 0 {
		return nil
	}
	out := make([]*Cast, 0, len(c.casts))
	for _, cs := range c.casts {
		out = append(out, cs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// CastByTypes returns the user-defined cast registered for the given source and
// target type names (case-insensitive, same key as RegisterCast/DropCast), or
// nil if none exists. Used by COMMENT ON CAST to resolve the cast's OID so the
// comment is stored under pg_cast (classoid 2605). DU-002 slice 396.
func (c *InMemory) CastByTypes(source, target string) *Cast {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.casts == nil {
		return nil
	}
	key := castKey(source, target)
	if cs, ok := c.casts[key]; ok {
		return cs
	}
	return nil
}

// Transform is a user-defined transform (CREATE TRANSFORM FOR type LANGUAGE
// lang (FROM SQL WITH FUNCTION ... , TO SQL WITH FUNCTION ...)). goopg does
// not execute the transform machinery (PL-language argument marshaling);
// this records just enough metadata to round-trip the definition through
// pg_dump (pg_transform virtual view → getTransforms/dumpTransform). DU-002
// (M0119-0004).
type Transform struct {
	OID      uint32 // pg_transform.oid (assigned from the catalog OID counter; > last builtin so pg_dump dumps it)
	TypeName string // the parsed FOR type name; resolved to trftype OID via TypeNameToOID at render time
	Lang     string // the parsed LANGUAGE name; resolved to trflang OID via LanguageNameToOID at render time
	// FromFuncOID / ToFuncOID are pg_transform.trffromsql / trftosql. 0 when
	// that half is absent (PG allows either or both alone) or when the
	// referenced function could not be resolved to a pg_proc OID — goopg's
	// user routine registry (catalog.Routines) does not cover built-in
	// functions, a gap shared with CREATE CAST's WITH FUNCTION resolution
	// (RegisterCast's funcOID parameter has the same limitation).
	FromFuncOID uint32
	ToFuncOID   uint32
}

// RegisterTransform records a user-defined transform, allocating a stable OID
// on first sight. Idempotent: re-registering the same (type, lang) pair
// refreshes the from/to function OIDs but keeps the OID. DU-002 (M0119-0004).
func (c *InMemory) RegisterTransform(typeName, lang string, fromFuncOID, toFuncOID uint32) *Transform {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transforms == nil {
		c.transforms = make(map[string]*Transform)
	}
	key := strings.ToLower(typeName) + "\x00" + strings.ToLower(lang)
	if tf, ok := c.transforms[key]; ok {
		tf.FromFuncOID = fromFuncOID
		tf.ToFuncOID = toFuncOID
		return tf
	}
	tf := &Transform{OID: c.allocOIDLocked(), TypeName: typeName, Lang: lang, FromFuncOID: fromFuncOID, ToFuncOID: toFuncOID}
	c.transforms[key] = tf
	return tf
}

// TransformExists reports whether a transform for (type, lang) is already
// registered — used to enforce PG's "transform for type %s language %s
// already exists" duplicate-object error when CREATE TRANSFORM (without OR
// REPLACE) targets an existing pair. DU-002 (M0119-0004).
func (c *InMemory) TransformExists(typeName, lang string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.transforms == nil {
		return false
	}
	_, ok := c.transforms[strings.ToLower(typeName)+"\x00"+strings.ToLower(lang)]
	return ok
}

// DropTransform removes a user-defined transform from the registry. Returns
// true if one was found and removed. DU-002 (M0119-0004).
func (c *InMemory) DropTransform(typeName, lang string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.transforms == nil {
		return false
	}
	key := strings.ToLower(typeName) + "\x00" + strings.ToLower(lang)
	if _, ok := c.transforms[key]; ok {
		delete(c.transforms, key)
		return true
	}
	return false
}

// ListTransforms returns all registered user transforms sorted by OID (stable
// creation order). DU-002 (M0119-0004).
func (c *InMemory) ListTransforms() []*Transform {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.transforms) == 0 {
		return nil
	}
	out := make([]*Transform, 0, len(c.transforms))
	for _, tf := range c.transforms {
		out = append(out, tf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// UserOperator is a user-defined operator (CREATE OPERATOR name (FUNCTION =
// fn, LEFTARG = t1, RIGHTARG = t2, ...)). goopg does not execute the
// operator (no expression-evaluator dispatch through a user FUNCTION); this
// records just enough metadata to round-trip the definition through
// pg_dump (pg_operator virtual view → getOperators/dumpOpr), including the
// COMMUTATOR/NEGATOR/RESTRICT/JOIN/MERGES/HASHES clauses and unary (prefix,
// LeftType=="") operators. DU-002 slice 407.
type UserOperator struct {
	OID uint32 // pg_operator.oid (assigned from the catalog OID counter; > last builtin so pg_dump dumps it)
	// Name is the bare operator symbol (e.g. "~~"); NamespaceOID resolves the
	// schema the CREATE OPERATOR statement declared it in (0 = unresolved,
	// falls back to public via NamespaceOIDOrDefault, mirroring
	// UserAggregate/UserCollation).
	Name         string
	NamespaceOID uint32
	// LeftType / RightType are the parsed LEFTARG/RIGHTARG type names (empty
	// when absent — a unary operator has exactly one of the two). Resolved to
	// oprleft/oprright OIDs via TypeNameToOID at render time.
	LeftType  string
	RightType string
	// FuncOID is pg_operator.oprcode, resolved from the FUNCTION/PROCEDURE
	// clause (catalog.LookupBuiltinProc for a builtin, or the user routine
	// registry for a CREATE FUNCTION-defined one). 0 when unresolved — PG
	// itself treats this as invalid (dumpOpr skips an operator whose oprcode
	// is InvalidOid), but goopg still registers it so DROP OPERATOR can find
	// it; VirtualRows below also skips a zero-FuncOID row for the same reason.
	// A zero FuncOID also occurs transiently for a COMMUTATOR/NEGATOR shell
	// operator (OperatorShellMake, pg_operator.c) before its own CREATE
	// OPERATOR statement fills it in.
	FuncOID uint32
	// Owner is pg_operator.oprowner (0 = unset, defaults to the bootstrap
	// superuser via OwnerOrDefault — CREATE OPERATOR has no OWNER TO clause of
	// its own; ownership is always the creating role, which goopg's DDL surface
	// does not track per-session, so every operator defaults to the bootstrap
	// superuser like a fresh single-session CREATE OPERATOR would).
	Owner uint32
	// CommutatorOID / NegatorOID are pg_operator.oprcom / oprnegate — the
	// OIDs of this operator's COMMUTATOR / NEGATOR, resolved (and, if
	// necessary, forward-declared as a shell operator) by the executor's
	// two-pass scheme mirroring PG's get_other_operator/OperatorShellMake/
	// OperatorUpd (pg_operator.c). 0 = none.
	CommutatorOID uint32
	NegatorOID    uint32
	// RestrictOID / JoinOID are pg_operator.oprrest / oprjoin — the resolved
	// pg_proc OIDs of the RESTRICT = / JOIN = selectivity estimator
	// functions. 0 = none.
	RestrictOID uint32
	JoinOID     uint32
	// CanMerge / CanHash are pg_operator.oprcanmerge / oprcanhash — the bare
	// MERGES / HASHES flags.
	CanMerge bool
	CanHash  bool
}

// OwnerOrDefault returns Owner, or the bootstrap superuser OID (10) if unset.
// Mirrors UserAggregate.OwnerOrDefault. DU-002 (M0119-0004).
func (o *UserOperator) OwnerOrDefault() uint32 {
	if o.Owner == 0 {
		return 10
	}
	return o.Owner
}

// NamespaceOIDOrDefault returns NamespaceOID, or PublicNamespaceOID if unset.
// Mirrors UserAggregate.NamespaceOIDOrDefault. DU-002 (M0119-0004).
func (o *UserOperator) NamespaceOIDOrDefault() uint32 {
	if o.NamespaceOID == 0 {
		return PublicNamespaceOID
	}
	return o.NamespaceOID
}

// userOperatorKey builds the operator registry's lookup key. PG allows the
// same operator symbol to be overloaded across distinct (left, right) type
// pairs (and, separately, across schemas), so the key must include both —
// unlike Cast/Transform, which forbid duplicate (source,target)/(type,lang)
// pairs outright.
func userOperatorKey(schema, name, leftType, rightType string) string {
	if schema == "" {
		schema = "public"
	}
	return strings.ToLower(schema) + "." + name + "(" + strings.ToLower(leftType) + "," + strings.ToLower(rightType) + ")"
}

// RegisterUserOperator records a user-defined operator, allocating a stable
// OID on first sight. Idempotent: re-registering the same (schema, name,
// leftType, rightType) key refreshes FuncOID but keeps the OID. DU-002
// (M0119-0004).
func (c *InMemory) RegisterUserOperator(schema, name, leftType, rightType string, namespaceOID, funcOID, owner uint32) *UserOperator {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperators == nil {
		c.userOperators = make(map[string]*UserOperator)
	}
	key := userOperatorKey(schema, name, leftType, rightType)
	if op, ok := c.userOperators[key]; ok {
		op.FuncOID = funcOID
		if namespaceOID != 0 {
			op.NamespaceOID = namespaceOID
		}
		if owner != 0 {
			op.Owner = owner
		}
		return op
	}
	op := &UserOperator{
		OID: c.allocOIDLocked(), Name: name, NamespaceOID: namespaceOID,
		LeftType: leftType, RightType: rightType, FuncOID: funcOID, Owner: owner,
	}
	c.userOperators[key] = op
	return op
}

// DropUserOperator removes a user-defined operator from the registry.
// Returns true if one was found and removed. DU-002 (M0119-0004).
func (c *InMemory) DropUserOperator(schema, name, leftType, rightType string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperators == nil {
		return false
	}
	key := userOperatorKey(schema, name, leftType, rightType)
	if _, ok := c.userOperators[key]; ok {
		delete(c.userOperators, key)
		return true
	}
	return false
}

// RegisterUserOperatorDuringRecovery is the idempotent version of
// RegisterUserOperator used by the WAL-replay driver
// (internal/initdb/operator_ddl_recovery.go). Unlike RegisterUserOperator it
// takes the OID (and every cross-reference field) from the WAL record so the
// recovered operator matches the pre-crash server exactly, and advances
// nextOID past it so subsequent allocations do not collide. Re-applying a
// record for an operator that already exists just overwrites it in place.
// Mirrors RegisterRangeTypeDuringRecovery/RegisterUserAggregateDuringRecovery.
// `schema` resolves the operator's NamespaceOID the same way
// RegisterUserAggregateDuringRecovery does (unknown/empty → public); this
// depends on replaySchemaDDLRecords having already restored the schema
// registry, which the caller (replayOperatorDDLRecords) guarantees by
// running after it. DU-002 restart-persistence follow-up (M0119-0004/
// M0110-0001, discovered while verifying the loop #64 CREATE TYPE ... AS
// RANGE opclass/collation follow-up — see ledger).
func (c *InMemory) RegisterUserOperatorDuringRecovery(op *UserOperator, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperators == nil {
		c.userOperators = make(map[string]*UserOperator)
	}
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	out := *op
	out.NamespaceOID = nsOID
	key := userOperatorKey(schema, op.Name, op.LeftType, op.RightType)
	c.userOperators[key] = &out
	c.advanceNextOIDLocked(op.OID)
}

// DropUserOperatorByOIDDuringRecovery is the recovery counterpart used for
// replaying RecordKindDropOperator. Identical in spirit to DropUserOperator
// but keyed by OID (DROP OPERATOR's own overload resolution already
// happened live, so the WAL record carries the OID directly, mirroring
// DropByOIDDuringRecovery for routines). Discards the found/not-found
// result — replay does not care whether the operator was still present.
func (c *InMemory) DropUserOperatorByOIDDuringRecovery(oid uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, op := range c.userOperators {
		if op.OID == oid {
			delete(c.userOperators, key)
			return
		}
	}
}

// ListUserOperators returns all registered user operators sorted by OID
// (stable creation order). DU-002 (M0119-0004).
func (c *InMemory) ListUserOperators() []*UserOperator {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.userOperators) == 0 {
		return nil
	}
	out := make([]*UserOperator, 0, len(c.userOperators))
	for _, op := range c.userOperators {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// LookupUserOperator finds a previously-registered operator by its identity
// key (schema, name, leftType, rightType), returning both real operators and
// shell operators created via EnsureUserOperatorShell (a shell is just a
// UserOperator with FuncOID==0). Used by the CREATE OPERATOR executor to
// resolve COMMUTATOR/NEGATOR references (PG's OperatorLookup, pg_operator.c).
// DU-002 slice 407.
func (c *InMemory) LookupUserOperator(schema, name, leftType, rightType string) (*UserOperator, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	op, ok := c.userOperators[userOperatorKey(schema, name, leftType, rightType)]
	return op, ok
}

// LookupUserOperatorByOID finds a previously-registered operator by its OID.
// Used to back-patch a COMMUTATOR/NEGATOR's own oprcom/oprnegate to point
// back at the operator that just referenced it (PG's OperatorUpd,
// pg_operator.c). Linear scan: the registry only ever holds the handful of
// user-defined operators a session creates. DU-002 slice 407.
func (c *InMemory) LookupUserOperatorByOID(oid uint32) *UserOperator {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, op := range c.userOperators {
		if op.OID == oid {
			return op
		}
	}
	return nil
}

// LookupUserOperatorByName finds a registered operator by name alone,
// ignoring schema and left/right type — for a CREATE OPERATOR CLASS AS-list
// OPERATOR entry with no explicit operand-type list. Real PG resolves such
// an entry via a search-path operator lookup that errors on ambiguity;
// goopg's compat-scope callers instead deterministically pick the
// lowest-OID match, since this DDL surface only ever creates one operator
// per symbol in practice. Returns the real operator, never a shell
// (FuncOID==0) one. DU-002 (M0119-0004) slice 411.
func (c *InMemory) LookupUserOperatorByName(name string) (*UserOperator, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var best *UserOperator
	for _, op := range c.userOperators {
		if op.FuncOID == 0 {
			continue // shell operator, not yet defined
		}
		if !strings.EqualFold(op.Name, name) {
			continue
		}
		if best == nil || op.OID < best.OID {
			best = op
		}
	}
	if best == nil {
		return nil, false
	}
	return best, true
}

// EnsureUserOperatorShell returns the existing operator registered under
// (schema, name, leftType, rightType) — real or shell — creating a shell
// (FuncOID==0) placeholder with a stable OID if none exists yet. Mirrors
// PG's OperatorShellMake (pg_operator.c): a COMMUTATOR/NEGATOR clause may
// forward-reference an operator that does not exist yet, so a minimal row
// is inserted purely to mint an OID; the operator's own later CREATE
// OPERATOR statement fills it in (RegisterUserOperator is idempotent by the
// same key, so it naturally reuses the shell's OID). DU-002 slice 407.
func (c *InMemory) EnsureUserOperatorShell(schema, name, leftType, rightType string, namespaceOID uint32) *UserOperator {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperators == nil {
		c.userOperators = make(map[string]*UserOperator)
	}
	key := userOperatorKey(schema, name, leftType, rightType)
	if op, ok := c.userOperators[key]; ok {
		return op
	}
	op := &UserOperator{
		OID: c.allocOIDLocked(), Name: name, NamespaceOID: namespaceOID,
		LeftType: leftType, RightType: rightType,
	}
	c.userOperators[key] = op
	return op
}

// UserOperatorFamily models a user-defined operator family (CREATE OPERATOR
// FAMILY name USING method) enough to round-trip through pg_dump's
// getOpfamilies/dumpOpfamily (pg_opfamily virtual view). Unlike CREATE
// OPERATOR CLASS, PG's CREATE OPERATOR FAMILY grammar has no AS clause — the
// family starts empty; OPERATOR/FUNCTION members are added later via a
// separate ALTER OPERATOR FAMILY ... ADD statement (opfamilycmds.c
// CreateOpFamily), which goopg does not yet implement — see the ledger.
// DU-002 (M0119-0004).
type UserOperatorFamily struct {
	OID          uint32 // pg_opfamily.oid
	Name         string
	NamespaceOID uint32
	Method       uint32 // pg_opfamily.opfmethod — a pg_am.oid, resolved via AccessMethodOIDByName
	// Owner is pg_opfamily.opfowner (0 = unset, defaults to the bootstrap
	// superuser via OwnerOrDefault — mirrors UserOperator.Owner: CREATE
	// OPERATOR FAMILY has no OWNER clause of its own, and goopg's DDL surface
	// does not track a per-session creating role).
	Owner uint32
}

// OwnerOrDefault mirrors UserOperator.OwnerOrDefault. DU-002 (M0119-0004).
func (f *UserOperatorFamily) OwnerOrDefault() uint32 {
	if f.Owner == 0 {
		return 10
	}
	return f.Owner
}

// NamespaceOIDOrDefault mirrors UserOperator.NamespaceOIDOrDefault.
// DU-002 (M0119-0004).
func (f *UserOperatorFamily) NamespaceOIDOrDefault() uint32 {
	if f.NamespaceOID == 0 {
		return PublicNamespaceOID
	}
	return f.NamespaceOID
}

// userOpFamilyKey builds the operator-family registry's lookup key. PG scopes
// opfamily-name uniqueness per (namespace, access method), so the same family
// name may be reused across access methods (the key includes the method OID,
// not just schema+name).
func userOpFamilyKey(schema, name string, method uint32) string {
	if schema == "" {
		schema = "public"
	}
	return strings.ToLower(schema) + "." + strings.ToLower(name) + "/" + strconv.FormatUint(uint64(method), 10)
}

// RegisterUserOperatorFamily records a user-defined operator family,
// allocating a stable OID on first sight. Idempotent: re-registering the same
// (schema, name, method) key refreshes namespace/owner but keeps the OID.
// DU-002 (M0119-0004).
func (c *InMemory) RegisterUserOperatorFamily(schema, name string, namespaceOID, method, owner uint32) *UserOperatorFamily {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperatorFamilies == nil {
		c.userOperatorFamilies = make(map[string]*UserOperatorFamily)
	}
	key := userOpFamilyKey(schema, name, method)
	if f, ok := c.userOperatorFamilies[key]; ok {
		if namespaceOID != 0 {
			f.NamespaceOID = namespaceOID
		}
		if owner != 0 {
			f.Owner = owner
		}
		return f
	}
	f := &UserOperatorFamily{
		OID: c.allocOIDLocked(), Name: name, NamespaceOID: namespaceOID,
		Method: method, Owner: owner,
	}
	c.userOperatorFamilies[key] = f
	return f
}

// RegisterUserOperatorFamilyDuringRecovery is the idempotent version of
// RegisterUserOperatorFamily used by the WAL-replay driver
// (internal/initdb/operator_class_ddl_recovery.go). Unlike
// RegisterUserOperatorFamily it takes the OID from the WAL record so the
// recovered family matches the pre-crash server exactly, and advances
// nextOID past it. `schema` resolves the family's NamespaceOID the same way
// RegisterUserOperatorDuringRecovery does (unknown/empty → public); this
// depends on replaySchemaDDLRecords having already restored the schema
// registry. DU-002 restart-persistence follow-up (M0119-0004/M0110-0001,
// closing the loop #65/#66 ledger row's "still open" item (1)).
func (c *InMemory) RegisterUserOperatorFamilyDuringRecovery(f *UserOperatorFamily, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperatorFamilies == nil {
		c.userOperatorFamilies = make(map[string]*UserOperatorFamily)
	}
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	out := *f
	out.NamespaceOID = nsOID
	key := userOpFamilyKey(schema, f.Name, f.Method)
	c.userOperatorFamilies[key] = &out
	c.advanceNextOIDLocked(f.OID)
}

// DropUserOperatorFamily removes a user-defined operator family from the
// registry, along with any pg_amop/pg_amproc member rows attributed to it
// (mirrors DropUserOperatorClass's own member purge). Returns true if one
// was found and removed. DU-002 (M0119-0004).
func (c *InMemory) DropUserOperatorFamily(schema, name string, method uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperatorFamilies == nil {
		return false
	}
	key := userOpFamilyKey(schema, name, method)
	fam, ok := c.userOperatorFamilies[key]
	if !ok {
		return false
	}
	delete(c.userOperatorFamilies, key)
	c.purgeAmMembersForFamilyLocked(fam.OID)
	return true
}

// purgeAmMembersForFamilyLocked removes every pg_amop/pg_amproc row owned by
// familyOID (both class-owned and "loose" ALTER OPERATOR FAMILY ... ADD
// members). Caller must hold c.mu. DU-002 restart-persistence follow-up
// (M0119-0004/M0110-0001, closing the loop #69 ledger row's "DROP OPERATOR
// FAMILY never actually calls DropUserOperatorFamily" discovery).
func (c *InMemory) purgeAmMembersForFamilyLocked(familyOID uint32) {
	kept := c.amOpMembers[:0]
	for _, m := range c.amOpMembers {
		if m.FamilyOID != familyOID {
			kept = append(kept, m)
		}
	}
	c.amOpMembers = kept
	keptP := c.amProcMembers[:0]
	for _, m := range c.amProcMembers {
		if m.FamilyOID != familyOID {
			keptP = append(keptP, m)
		}
	}
	c.amProcMembers = keptP
}

// DropUserOperatorFamilyByOIDDuringRecovery is the recovery counterpart used
// for replaying RecordKindDropOperatorFamily. Identical in spirit to
// DropUserOperatorFamily (removes the family row plus every amop/amproc row
// it owns) but keyed by OID — DROP OPERATOR FAMILY's own existence check and
// name/method resolution already happened live, so the WAL record carries
// the OID directly, mirroring DropUserOperatorClassByOIDDuringRecovery.
// Discards the found/not-found result — replay does not care whether the
// family was still present. DU-002 restart-persistence follow-up
// (M0119-0004/M0110-0001).
func (c *InMemory) DropUserOperatorFamilyByOIDDuringRecovery(oid uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, fam := range c.userOperatorFamilies {
		if fam.OID == oid {
			delete(c.userOperatorFamilies, key)
			break
		}
	}
	c.purgeAmMembersForFamilyLocked(oid)
}

// ListUserOperatorFamilies returns all registered user operator families
// sorted by OID (stable creation order). DU-002 (M0119-0004).
func (c *InMemory) ListUserOperatorFamilies() []*UserOperatorFamily {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.userOperatorFamilies) == 0 {
		return nil
	}
	out := make([]*UserOperatorFamily, 0, len(c.userOperatorFamilies))
	for _, f := range c.userOperatorFamilies {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// LookupUserOperatorFamily finds a previously-registered operator family by
// its identity (schema, name, method). Used by CREATE OPERATOR CLASS to
// resolve an explicit `FAMILY family_name` clause. DU-002 (M0119-0004).
func (c *InMemory) LookupUserOperatorFamily(schema, name string, method uint32) (*UserOperatorFamily, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, ok := c.userOperatorFamilies[userOpFamilyKey(schema, name, method)]
	return f, ok
}

// UserOperatorClass models a user-defined operator class (CREATE OPERATOR
// CLASS name [DEFAULT] FOR TYPE type USING method [FAMILY family] AS ...)
// enough to round-trip the class's own pg_opclass row through pg_dump's
// getOpclasses/dumpOpclass (pg_opclass virtual view). dumpOpclass also emits
// any OPERATOR/FUNCTION entries tied to the class via pg_amop/pg_amproc +
// pg_depend — that member store is not yet implemented (deferred, see the
// ledger), so a class declaring real members currently dumps with only its
// STORAGE clause (or PG's own dummy `STORAGE opcintype` filler when no
// STORAGE was given and no members exist), silently dropping the members.
// DU-002 (M0119-0004).
type UserOperatorClass struct {
	OID          uint32 // pg_opclass.oid
	Name         string
	NamespaceOID uint32
	// Owner is pg_opclass.opcowner (0 = unset, defaults to the bootstrap
	// superuser via OwnerOrDefault — mirrors UserOperatorFamily.Owner: CREATE
	// OPERATOR CLASS has no OWNER clause of its own, and goopg's DDL surface
	// does not track a per-session creating role).
	Owner      uint32
	Method     uint32 // pg_opclass.opcmethod — a pg_am.oid
	FamilyOID  uint32 // pg_opclass.opcfamily — always valid; PG auto-creates an anonymous family (same name as the class) when FAMILY is omitted
	InTypeOID  uint32 // pg_opclass.opcintype
	IsDefault  bool   // pg_opclass.opcdefault
	KeyTypeOID uint32 // pg_opclass.opckeytype — 0 (InvalidOid, dumps as "-") when no STORAGE clause was given
}

// OwnerOrDefault mirrors UserOperatorFamily.OwnerOrDefault. DU-002 (M0119-0004).
func (oc *UserOperatorClass) OwnerOrDefault() uint32 {
	if oc.Owner == 0 {
		return 10
	}
	return oc.Owner
}

// NamespaceOIDOrDefault mirrors UserOperatorFamily.NamespaceOIDOrDefault.
// DU-002 (M0119-0004).
func (oc *UserOperatorClass) NamespaceOIDOrDefault() uint32 {
	if oc.NamespaceOID == 0 {
		return PublicNamespaceOID
	}
	return oc.NamespaceOID
}

// userOpClassKey builds the operator-class registry's lookup key, mirroring
// userOpFamilyKey (PG scopes opclass-name uniqueness per namespace+access
// method too).
func userOpClassKey(schema, name string, method uint32) string {
	if schema == "" {
		schema = "public"
	}
	return strings.ToLower(schema) + "." + strings.ToLower(name) + "/" + strconv.FormatUint(uint64(method), 10)
}

// RegisterUserOperatorClass records a user-defined operator class, allocating
// a stable OID on first sight. Idempotent: re-registering the same (schema,
// name, method) key refreshes the mutable attributes but keeps the OID.
// DU-002 (M0119-0004).
func (c *InMemory) RegisterUserOperatorClass(schema, name string, namespaceOID, owner, method, familyOID, inTypeOID uint32, isDefault bool, keyTypeOID uint32) *UserOperatorClass {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperatorClasses == nil {
		c.userOperatorClasses = make(map[string]*UserOperatorClass)
	}
	key := userOpClassKey(schema, name, method)
	if oc, ok := c.userOperatorClasses[key]; ok {
		if namespaceOID != 0 {
			oc.NamespaceOID = namespaceOID
		}
		if owner != 0 {
			oc.Owner = owner
		}
		oc.FamilyOID = familyOID
		oc.InTypeOID = inTypeOID
		oc.IsDefault = isDefault
		oc.KeyTypeOID = keyTypeOID
		return oc
	}
	oc := &UserOperatorClass{
		OID: c.allocOIDLocked(), Name: name, NamespaceOID: namespaceOID, Owner: owner,
		Method: method, FamilyOID: familyOID, InTypeOID: inTypeOID, IsDefault: isDefault, KeyTypeOID: keyTypeOID,
	}
	c.userOperatorClasses[key] = oc
	return oc
}

// RegisterUserOperatorClassDuringRecovery is the idempotent version of
// RegisterUserOperatorClass used by the WAL-replay driver
// (internal/initdb/operator_class_ddl_recovery.go). Unlike
// RegisterUserOperatorClass it takes the OID (and FamilyOID) from the WAL
// record so the recovered class matches the pre-crash server exactly, and
// advances nextOID past it. Also repopulates the legacy opClassSchemas
// registry (RegisterOpClassSchema's backing map) so a post-restart DROP
// OPERATOR CLASS — which checks HasOpClass, not userOperatorClasses — still
// finds the class, mirroring what the live execCreateOpClass path does via
// its own separate RegisterOpClassSchema call. `schema` resolves
// NamespaceOID the same way RegisterUserOperatorDuringRecovery does. DU-002
// restart-persistence follow-up (M0119-0004/M0110-0001, closing the
// loop #65/#66 ledger row's "still open" item (1)).
func (c *InMemory) RegisterUserOperatorClassDuringRecovery(oc *UserOperatorClass, schema string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperatorClasses == nil {
		c.userOperatorClasses = make(map[string]*UserOperatorClass)
	}
	nsOID := c.schemas[strings.ToLower(schema)]
	if nsOID == 0 {
		nsOID = c.schemas["public"]
	}
	out := *oc
	out.NamespaceOID = nsOID
	key := userOpClassKey(schema, oc.Name, oc.Method)
	c.userOperatorClasses[key] = &out
	if c.opClassSchemas == nil {
		c.opClassSchemas = make(map[string]string)
	}
	c.opClassSchemas[oc.Name] = schema
	c.advanceNextOIDLocked(oc.OID)
}

// LookupUserOperatorClass finds a previously-registered operator class by
// its identity (schema, name, method). Used by execDropCompat's DROP
// OPERATOR CLASS path to recover the class's OID before removing it, so the
// removal can be WAL-logged by OID (mirroring DROP OPERATOR's own
// look-up-then-drop-by-OID shape). DU-002 restart-persistence follow-up
// (M0119-0004/M0110-0001).
func (c *InMemory) LookupUserOperatorClass(schema, name string, method uint32) (*UserOperatorClass, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	oc, ok := c.userOperatorClasses[userOpClassKey(schema, name, method)]
	return oc, ok
}

// DropUserOperatorClass removes a user-defined operator class from the
// registry, along with any pg_amop/pg_amproc member rows attributed to it
// (slice 411). Returns true if one was found and removed. DU-002 (M0119-0004).
func (c *InMemory) DropUserOperatorClass(schema, name string, method uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userOperatorClasses == nil {
		return false
	}
	key := userOpClassKey(schema, name, method)
	oc, ok := c.userOperatorClasses[key]
	if !ok {
		return false
	}
	delete(c.userOperatorClasses, key)
	classOID := oc.OID
	kept := c.amOpMembers[:0]
	for _, m := range c.amOpMembers {
		if m.ClassOID != classOID {
			kept = append(kept, m)
		}
	}
	c.amOpMembers = kept
	keptP := c.amProcMembers[:0]
	for _, m := range c.amProcMembers {
		if m.ClassOID != classOID {
			keptP = append(keptP, m)
		}
	}
	c.amProcMembers = keptP
	return true
}

// DropUserOperatorClassByOIDDuringRecovery is the recovery counterpart used
// for replaying RecordKindDropOperatorClass. Identical in spirit to
// DropUserOperatorClass (removes the class row plus every amop/amproc row it
// owns) but keyed by OID — DROP OPERATOR CLASS's own existence check and
// name/method resolution already happened live, so the WAL record carries
// the OID directly, mirroring DropUserOperatorByOIDDuringRecovery. Also
// removes the legacy opClassSchemas entry, mirroring the live
// execDropCompat path's RemoveOpClass call. Discards the found/not-found
// result — replay does not care whether the class was still present.
// DU-002 restart-persistence follow-up (M0119-0004/M0110-0001).
func (c *InMemory) DropUserOperatorClassByOIDDuringRecovery(oid uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var name string
	for key, oc := range c.userOperatorClasses {
		if oc.OID == oid {
			name = oc.Name
			delete(c.userOperatorClasses, key)
			break
		}
	}
	kept := c.amOpMembers[:0]
	for _, m := range c.amOpMembers {
		if m.ClassOID != oid {
			kept = append(kept, m)
		}
	}
	c.amOpMembers = kept
	keptP := c.amProcMembers[:0]
	for _, m := range c.amProcMembers {
		if m.ClassOID != oid {
			keptP = append(keptP, m)
		}
	}
	c.amProcMembers = keptP
	if name != "" {
		delete(c.opClassSchemas, name)
	}
}

// ListUserOperatorClasses returns all registered user operator classes sorted
// by OID (stable creation order). DU-002 (M0119-0004).
func (c *InMemory) ListUserOperatorClasses() []*UserOperatorClass {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.userOperatorClasses) == 0 {
		return nil
	}
	out := make([]*UserOperatorClass, 0, len(c.userOperatorClasses))
	for _, oc := range c.userOperatorClasses {
		out = append(out, oc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// AmOpMember models one pg_amop row (an OPERATOR entry attributed to an
// operator class via CREATE OPERATOR CLASS's own AS list). Matches PG's
// pg_amop (pg_amop.h). AmopPurpose is derived from SortFamilyOID (AMOP_ORDER
// 'o' when non-zero, else AMOP_SEARCH 's' — opclasscmds.c: "oppurpose =
// OidIsValid(op->sortfamily) ? AMOP_ORDER : AMOP_SEARCH"). ClassOID backs the
// pg_depend INTERNAL ('i') row dumpOpclass's query joins on
// (refclassid=pg_opclass, refobjid=ClassOID). DU-002 (M0119-0004) slice 411;
// SortFamilyOID added slice 414 (FOR ORDER BY).
type AmOpMember struct {
	OID           uint32 // pg_amop.oid
	FamilyOID     uint32 // pg_amop.amopfamily
	LeftType      uint32 // pg_amop.amoplefttype
	RightType     uint32 // pg_amop.amoprighttype
	Strategy      uint32 // pg_amop.amopstrategy
	OperOID       uint32 // pg_amop.amopopr
	Method        uint32 // pg_amop.amopmethod
	ClassOID      uint32 // owning opclass OID, for the pg_depend INTERNAL row
	SortFamilyOID uint32 // pg_amop.amopsortfamily — FOR ORDER BY family, 0 (InvalidOid) for FOR SEARCH
}

// AmProcMember models one pg_amproc row (a FUNCTION entry attributed to an
// operator class via CREATE OPERATOR CLASS's own AS list). Matches PG's
// pg_amproc (pg_amproc.h). ClassOID mirrors AmOpMember.ClassOID. DU-002
// (M0119-0004) slice 411.
type AmProcMember struct {
	OID       uint32 // pg_amproc.oid
	FamilyOID uint32 // pg_amproc.amprocfamily
	LeftType  uint32 // pg_amproc.amproclefttype
	RightType uint32 // pg_amproc.amprocrighttype
	ProcNum   uint32 // pg_amproc.amprocnum
	ProcOID   uint32 // pg_amproc.amproc
	ClassOID  uint32 // owning opclass OID, for the pg_depend INTERNAL row
	Method    uint32 // pg_amproc.amprocfamily's owning pg_am.oid — needed by
	// amForcesSoftFunctionDependency to look up the AM's amadjustmembers
	// required-support-proc policy without a family-OID indirection. Added
	// DU-002 (M0119-0004) alongside AmOpMember.Method (slice 411).
}

// RegisterAmOpMember records one pg_amop row, allocating a stable OID.
// Appended (not keyed/idempotent) since CREATE OPERATOR CLASS has no
// re-create/replace form. sortFamilyOID is 0 (InvalidOid) for a plain FOR
// SEARCH entry, or the resolved FOR ORDER BY family's OID (slice 414).
// DU-002 (M0119-0004) slice 411.
func (c *InMemory) RegisterAmOpMember(familyOID, classOID, leftType, rightType, strategy, operOID, method, sortFamilyOID uint32) *AmOpMember {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := &AmOpMember{
		OID: c.allocOIDLocked(), FamilyOID: familyOID, LeftType: leftType,
		RightType: rightType, Strategy: strategy, OperOID: operOID,
		Method: method, ClassOID: classOID, SortFamilyOID: sortFamilyOID,
	}
	c.amOpMembers = append(c.amOpMembers, m)
	return m
}

// RegisterAmOpMemberDuringRecovery is the idempotent version of
// RegisterAmOpMember used by the WAL-replay driver
// (internal/initdb/operator_class_ddl_recovery.go). Unlike RegisterAmOpMember
// it takes the OID from the WAL record (covers both a CREATE OPERATOR
// CLASS ... AS list entry and an ALTER OPERATOR FAMILY ... ADD entry — both
// go through registerOpClassMembers and share the same AmOpMember shape) and
// advances nextOID past it. Re-applying a record for a member that already
// exists (same OID) just overwrites it in place. DU-002 restart-persistence
// follow-up (M0119-0004/M0110-0001).
func (c *InMemory) RegisterAmOpMemberDuringRecovery(m *AmOpMember) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := *m
	for i, existing := range c.amOpMembers {
		if existing.OID == m.OID {
			c.amOpMembers[i] = &out
			c.advanceNextOIDLocked(m.OID)
			return
		}
	}
	c.amOpMembers = append(c.amOpMembers, &out)
	c.advanceNextOIDLocked(m.OID)
}

// RegisterAmProcMember records one pg_amproc row, allocating a stable OID.
// Mirrors RegisterAmOpMember. DU-002 (M0119-0004) slice 411.
func (c *InMemory) RegisterAmProcMember(familyOID, classOID, leftType, rightType, procNum, procOID, method uint32) *AmProcMember {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := &AmProcMember{
		OID: c.allocOIDLocked(), FamilyOID: familyOID, LeftType: leftType,
		RightType: rightType, ProcNum: procNum, ProcOID: procOID, ClassOID: classOID,
		Method: method,
	}
	c.amProcMembers = append(c.amProcMembers, m)
	return m
}

// RegisterAmProcMemberDuringRecovery is the idempotent version of
// RegisterAmProcMember used by the WAL-replay driver. Mirrors
// RegisterAmOpMemberDuringRecovery. DU-002 restart-persistence follow-up
// (M0119-0004/M0110-0001).
func (c *InMemory) RegisterAmProcMemberDuringRecovery(m *AmProcMember) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := *m
	for i, existing := range c.amProcMembers {
		if existing.OID == m.OID {
			c.amProcMembers[i] = &out
			c.advanceNextOIDLocked(m.OID)
			return
		}
	}
	c.amProcMembers = append(c.amProcMembers, &out)
	c.advanceNextOIDLocked(m.OID)
}

// ListAmOpMembers returns all registered pg_amop rows in creation order.
// DU-002 (M0119-0004) slice 411.
func (c *InMemory) ListAmOpMembers() []*AmOpMember {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.amOpMembers) == 0 {
		return nil
	}
	out := make([]*AmOpMember, len(c.amOpMembers))
	copy(out, c.amOpMembers)
	return out
}

// ListAmProcMembers returns all registered pg_amproc rows in creation order.
// DU-002 (M0119-0004) slice 411.
func (c *InMemory) ListAmProcMembers() []*AmProcMember {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.amProcMembers) == 0 {
		return nil
	}
	out := make([]*AmProcMember, len(c.amProcMembers))
	copy(out, c.amProcMembers)
	return out
}

// RemoveAmOpMember deletes the pg_amop row keyed by (familyOID, leftType,
// rightType, strategy) — the same (amopfamily, amoplefttype, amoprighttype,
// amopstrategy) unique index PG's dropOperators looks up via
// GetSysCacheOid4(AMOPSTRATEGY, ...) (opclasscmds.c). Reports whether a
// matching row was found and removed; the caller raises 42704 (undefined
// object) on false, matching dropOperators' own ereport. Removing the row
// also removes its pg_depend edges, since dependVirtualRows recomputes them
// live from c.amOpMembers on every read. ALTER OPERATOR FAMILY ... DROP,
// DU-002 (M0119-0004).
func (c *InMemory) RemoveAmOpMember(familyOID, leftType, rightType, strategy uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, m := range c.amOpMembers {
		if m.FamilyOID == familyOID && m.LeftType == leftType && m.RightType == rightType && m.Strategy == strategy {
			c.amOpMembers = append(c.amOpMembers[:i], c.amOpMembers[i+1:]...)
			return true
		}
	}
	return false
}

// RemoveAmProcMember deletes the pg_amproc row keyed by (familyOID,
// leftType, rightType, procNum) — mirrors RemoveAmOpMember for
// dropProcedures' AMPROCNUM lookup. ALTER OPERATOR FAMILY ... DROP, DU-002
// (M0119-0004).
func (c *InMemory) RemoveAmProcMember(familyOID, leftType, rightType, procNum uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, m := range c.amProcMembers {
		if m.FamilyOID == familyOID && m.LeftType == leftType && m.RightType == rightType && m.ProcNum == procNum {
			c.amProcMembers = append(c.amProcMembers[:i], c.amProcMembers[i+1:]...)
			return true
		}
	}
	return false
}

// amGISTMethodOID / amSPGistMethodOID are pg_am.oid for "gist"/"spgist"
// (see AccessMethodOIDByName below) — the only two in-tree access methods
// whose amadjustmembers override (gistvalidate.c gistadjustmembers,
// spgvalidate.c spgadjustmembers) forces dependency softness independent of
// class-attribution. Hoisted as constants since amForcesSoftOperatorDependency/
// amForcesSoftFunctionDependency are called per-member from dependVirtualRows.
const (
	amGISTMethodOID   = 783
	amSPGistMethodOID = 4000
)

// gistRequiredSupportProcs / spgistRequiredSupportProcs list the amprocnum
// values gistadjustmembers/spgadjustmembers keep as "required" (hard,
// class-level when class-attributed) — GIST_CONSISTENT/UNION/PENALTY/
// PICKSPLIT/EQUAL_PROC (gist.h) and SPGIST_CONFIG/CHOOSE/PICKSPLIT/
// INNER_CONSISTENT/LEAF_CONSISTENT_PROC (spgist.h) respectively. Every other
// support-function number for these two AMs (COMPRESS/DECOMPRESS/DISTANCE/
// FETCH/OPTIONS/SORTSUPPORT/TRANSLATE_CMPTYPE for GiST, COMPRESS/OPTIONS for
// SP-GiST) is "optional" and gets forced to a soft family-level dependency
// even when class-attributed.
var gistRequiredSupportProcs = map[uint32]bool{1: true, 2: true, 5: true, 6: true, 7: true}
var spgistRequiredSupportProcs = map[uint32]bool{1: true, 2: true, 3: true, 4: true, 5: true}

// amForcesSoftOperatorDependency reports whether methodOID's amadjustmembers
// override forces every OPERATOR member's opclass/opfamily dependency to be
// soft (AUTO, targeting the opfamily) even when the member is
// class-attributed (ClassOID != 0) — "Operator members of a GiST/SP-GiST
// opfamily should never have hard dependencies, since their connection to
// the opfamily depends only on what the support functions think, and that
// can be altered... For consistency, we make all soft dependencies point to
// the opfamily" (gistvalidate.c gistadjustmembers, spgvalidate.c
// spgadjustmembers, verbatim comment). Every other in-tree AM
// (btree/hash/gin/brin) leaves goopg's pre-existing class-attributed-is-hard
// default unchanged. A loose member (ClassOID == 0, ALTER OPERATOR FAMILY
// ... ADD) is already unconditionally soft regardless of AM — this only
// changes the CLASS-attributed case. DU-002 (M0119-0004).
func amForcesSoftOperatorDependency(methodOID uint32) bool {
	return methodOID == amGISTMethodOID || methodOID == amSPGistMethodOID
}

// amForcesSoftFunctionDependency reports whether a class-attributed FUNCTION
// member with the given amprocnum is "optional" for methodOID's AM and
// therefore gets forced to a soft family-level dependency instead of the
// pre-existing hard-on-class default (gistadjustmembers/spgadjustmembers's
// function loop: the AM's required-support-proc subset keeps the hard
// dependency, every other proc number is forced soft). Returns false (no
// override) for any AM other than GiST/SP-GiST, or for a required proc
// number on those two. Only meaningful for a class-attributed member — a
// loose member is already unconditionally soft (see
// amForcesSoftOperatorDependency's doc comment). DU-002 (M0119-0004).
func amForcesSoftFunctionDependency(methodOID, procNum uint32) bool {
	switch methodOID {
	case amGISTMethodOID:
		return !gistRequiredSupportProcs[procNum]
	case amSPGistMethodOID:
		return !spgistRequiredSupportProcs[procNum]
	}
	return false
}

// AccessMethodOIDByName maps an access method name to its pg_am.oid, covering
// the 7 rows pg_am.VirtualRows serves (see the pg_am registration in this
// file). Returns 0 for an unrecognized method. Used by CREATE OPERATOR FAMILY
// and CREATE OPERATOR CLASS to resolve the `USING method`
// clause to pg_opfamily.opfmethod / pg_opclass.opcmethod. DU-002 (M0119-0004).
func AccessMethodOIDByName(name string) uint32 {
	switch strings.ToLower(name) {
	case "heap":
		return 2
	case "btree":
		return 403
	case "hash":
		return 405
	case "gist":
		return 783
	case "gin":
		return 2742
	case "spgist":
		return 4000
	case "brin":
		return 3580
	}
	return 0
}

// LanguageNameToOID maps a language name to its pg_language OID, covering the
// 4 rows pg_language.VirtualRows serves (the 3 built-in BKI languages plus
// plpgsql — see the pg_language registration in this file). Returns 0 for an
// unknown/user-defined language (goopg installs no user procedural
// languages). Used by CREATE TRANSFORM to resolve pg_transform.trflang.
// DU-002 (M0119-0004).
func LanguageNameToOID(name string) uint32 {
	switch strings.ToLower(name) {
	case "internal":
		return 12
	case "c":
		return 13
	case "sql":
		return 14
	case "plpgsql":
		return 13627
	}
	return 0
}

// BuiltinProc is a minimal, hand-curated pg_proc.dat entry for a built-in
// PostgreSQL function that goopg's own DDL surface can reference by name —
// e.g. a CREATE CAST/CONVERSION/TRANSFORM WITH FUNCTION clause naming a
// built-in I/O or internal function such as int4recv. It is NOT the full
// ~3397-row PG18 catalog (that generated table lives in internal/initdb's
// pg_proc_seed_data.go, seeded into the on-disk heap for PG-standby
// consumption); internal/executor cannot import internal/initdb because
// initdb already imports executor, so the reverse import would cycle (see
// the "Version constants must live in leaf config pkg" precedent). Both
// internal/initdb's SQL-queryable pg_proc view and internal/executor's
// WITH-FUNCTION resolution read this single leaf-package table so the two
// never diverge (see "Sibling code paths must stay in sync").
type BuiltinProc struct {
	OID       uint32
	Name      string
	Namespace uint32 // pronamespace OID; every entry so far is pg_catalog (11)
	RetType   string // catalog type name (matches Type.Name / TypeNameToOID)
	ArgTypes  []string
	Volatile  string // provolatile: "i"/"s"/"v"
}

// builtinProcsByName holds only the functions goopg's DU-002 pg_dump test
// fixtures actually reference so far; extend as new CAST/CONVERSION/
// TRANSFORM fixtures need more builtins. OID/rettype/argtypes/provolatile
// are taken from postgres/src/include/catalog/pg_proc.dat (provolatile
// defaults to 'i' — BKI_DEFAULT(i) in pg_proc.h — when the .dat entry omits
// it, which both entries below do).
var builtinProcsByName = map[string]BuiltinProc{
	"int4recv": {
		OID: 2406, Name: "int4recv", Namespace: 11,
		RetType: "int4", ArgTypes: []string{"internal"}, Volatile: "i",
	},
	"prsd_lextype": {
		OID: 3721, Name: "prsd_lextype", Namespace: 11,
		RetType: "internal", ArgTypes: []string{"internal"}, Volatile: "i",
	},
	"iso8859_1_to_utf8": {
		OID: 4374, Name: "iso8859_1_to_utf8", Namespace: 11,
		RetType:  "int4",
		ArgTypes: []string{"int4", "int4", "cstring", "internal", "int4", "bool"},
		Volatile: "i",
	},
	// age(timestamptz) is one of four overloaded pg_proc.dat "age" entries
	// (OIDs 1181/1199/1386/2058/2059); only the single-arg timestamptz->interval
	// form (OID 1386, the DU-002 "CREATE CAST FOR timestamptz" fixture's WITH
	// FUNCTION reference) is curated since LookupBuiltinProc has no overload
	// resolution (name-only) and no other fixture references "age" yet.
	"age": {
		OID: 1386, Name: "age", Namespace: 11,
		RetType:  "interval",
		ArgTypes: []string{"timestamptz"},
		Volatile: "s",
	},
	// int4_avg_accum/int8_avg are the state-transition/final functions a
	// CREATE AGGREGATE fixture references directly by name (e.g. DU-002
	// slice 405's "newavg" mirrors PG's own avg(int4) plumbing:
	// SFUNC=int4_avg_accum, FINALFUNC=int8_avg). Curated so the aggregate's
	// pg_proc/pg_aggregate rows can resolve SFUNC/FINALFUNC to a real OID
	// (mirroring pg_cast.castfunc/pg_conversion.conproc's FuncOID pattern).
	"int4_avg_accum": {
		OID: 1963, Name: "int4_avg_accum", Namespace: 11,
		RetType:  "int8[]",
		ArgTypes: []string{"int8[]", "int4"},
		Volatile: "i",
	},
	"int8_avg": {
		OID: 1964, Name: "int8_avg", Namespace: 11,
		RetType:  "numeric",
		ArgTypes: []string{"int8[]"},
		Volatile: "i",
	},
	// int4eq is the FUNCTION a DU-002 "CREATE OPERATOR" fixture references
	// (mirrors PG's own "=" operator over int4, pg_operator.dat oid 96 ->
	// oprcode int4eq). Curated so the operator's pg_operator.oprcode can
	// resolve to a real OID (mirroring pg_cast.castfunc/pg_aggregate's
	// aggtransfn FuncOID pattern).
	"int4eq": {
		OID: 65, Name: "int4eq", Namespace: 11,
		RetType:  "bool",
		ArgTypes: []string{"int4", "int4"},
		Volatile: "i",
	},
	// btint4cmp is int4's real btree "support function 1" (three-way
	// less-equal-greater compare, pg_proc.dat oid 351) — a CREATE OPERATOR
	// CLASS ... AS FUNCTION 1 entry referencing it exercises the pg_amproc
	// member store against a semantically valid (int4-returning) btree
	// comparator, matching real PG's own opclasscmds.c validation ("ordering
	// comparison functions must return integer"). M0119-0004 (DU-002) slice
	// 411.
	"btint4cmp": {
		OID: 351, Name: "btint4cmp", Namespace: 11,
		RetType:  "int4",
		ArgTypes: []string{"int4", "int4"},
		Volatile: "i",
	},
	// eqsel/eqjoinsel/neqjoinsel are the RESTRICT=/JOIN= selectivity
	// estimators an ALTER OPERATOR ... SET (RESTRICT=/JOIN=) fixture
	// references directly by name (mirrors PG's own "=" operator's
	// oprrest/oprjoin, pg_operator.dat oid 96 -> eqsel/eqjoinsel). Curated
	// so RESTRICT=/JOIN= can resolve to a real OID, same pattern as int4eq
	// above. M0119-0004 (DU-002).
	"eqsel": {
		OID: 101, Name: "eqsel", Namespace: 11,
		RetType:  "float8",
		ArgTypes: []string{"internal", "oid", "internal", "int4"},
		Volatile: "s",
	},
	"eqjoinsel": {
		OID: 105, Name: "eqjoinsel", Namespace: 11,
		RetType:  "float8",
		ArgTypes: []string{"internal", "oid", "internal", "int2", "internal"},
		Volatile: "s",
	},
	"neqjoinsel": {
		OID: 106, Name: "neqjoinsel", Namespace: 11,
		RetType:  "float8",
		ArgTypes: []string{"internal", "oid", "internal", "int2", "internal"},
		Volatile: "s",
	},
	// btint8cmp is int8's real btree "support function 1" (three-way
	// less-equal-greater compare, pg_proc.dat oid 842), the FUNCTION 1
	// member of the upstream `op_class` pg_dump fixture (bigint comparison
	// opclass) — mirrors btint4cmp's curation rationale above. M0119-0004
	// (DU-002) slice 413.
	"btint8cmp": {
		OID: 842, Name: "btint8cmp", Namespace: 11,
		RetType:  "int4",
		ArgTypes: []string{"int8", "int8"},
		Volatile: "i",
	},
}

// LookupBuiltinProc resolves a built-in pg_proc.dat function by name (case-
// insensitive, unqualified — every entry lives in pg_catalog). Returns
// false if name is not in the hand-curated set above.
func LookupBuiltinProc(name string) (BuiltinProc, bool) {
	p, ok := builtinProcsByName[strings.ToLower(name)]
	return p, ok
}

// BuiltinOperator holds a hand-curated built-in pg_operator.dat row — the
// operator analogue of BuiltinProc. Only the handful of built-in operators
// goopg's DU-002 pg_dump test fixtures actually reference are curated here
// (not a full port of pg_operator.dat's ~799 rows — see the ledger); extend
// as new CREATE OPERATOR CLASS/FAMILY fixtures reference more. OID/lefttype/
// righttype/oprcode are taken from postgres/src/include/catalog/pg_operator.dat.
// M0119-0004 (DU-002) slice 413.
type BuiltinOperator struct {
	OID       uint32
	Name      string
	Namespace uint32 // oprnamespace; every entry so far is pg_catalog (11)
	Kind      byte   // oprkind: 'b' binary, 'l' left-unary (prefix); every entry so far is 'b'
	LeftType  string // catalog type name (matches Type.Name / TypeNameToOID)
	RightType string
}

// builtinOperatorKey builds the (name, lefttype OID, righttype OID) lookup
// key for builtinOperatorsByKey. Keying on resolved type OIDs (not the raw
// type-name spelling) makes lookup synonym-proof — a CREATE OPERATOR CLASS
// AS-list entry may spell the same type differently than pg_operator.dat
// does (e.g. "bigint" vs "int8").
func builtinOperatorKey(name string, leftOID, rightOID uint32) string {
	return name + "/" + strconv.FormatUint(uint64(leftOID), 10) + "/" + strconv.FormatUint(uint64(rightOID), 10)
}

// builtinOperatorsByKey holds the curated built-in operator set (see
// BuiltinOperator's doc comment). Currently just the 5 int8 btree comparison
// strategies the upstream `op_class` pg_dump fixture's OPERATOR entries need
// (pg_operator.dat oids 410/412/413/414/415).
var builtinOperatorsByKey = map[string]BuiltinOperator{
	builtinOperatorKey("=", OIDInt8, OIDInt8):  {OID: 410, Name: "=", Namespace: 11, Kind: 'b', LeftType: "int8", RightType: "int8"},
	builtinOperatorKey("<", OIDInt8, OIDInt8):  {OID: 412, Name: "<", Namespace: 11, Kind: 'b', LeftType: "int8", RightType: "int8"},
	builtinOperatorKey(">", OIDInt8, OIDInt8):  {OID: 413, Name: ">", Namespace: 11, Kind: 'b', LeftType: "int8", RightType: "int8"},
	builtinOperatorKey("<=", OIDInt8, OIDInt8): {OID: 414, Name: "<=", Namespace: 11, Kind: 'b', LeftType: "int8", RightType: "int8"},
	builtinOperatorKey(">=", OIDInt8, OIDInt8): {OID: 415, Name: ">=", Namespace: 11, Kind: 'b', LeftType: "int8", RightType: "int8"},
}

// builtinOperatorsByOID is the OID-keyed reverse index of
// builtinOperatorsByKey, built once at init for regoper/regoperator OID→name
// rendering (RegoperatorNameAndSchema, and the bare-name regoper CastExpr in
// internal/executor/expr.go).
var builtinOperatorsByOID = func() map[uint32]BuiltinOperator {
	out := make(map[uint32]BuiltinOperator, len(builtinOperatorsByKey))
	for _, op := range builtinOperatorsByKey {
		out[op.OID] = op
	}
	return out
}()

// LookupBuiltinOperator resolves a built-in pg_operator.dat row by name plus
// explicit (lefttype, righttype) — the shape a CREATE OPERATOR CLASS AS-list
// OPERATOR entry with explicit operand types provides (resolveOpClassOperator,
// internal/executor/operators_ddl.go). leftType/rightType are resolved via
// TypeNameToOID before keying, so a caller's raw type-name spelling (e.g.
// "bigint") matches an entry keyed on pg_operator.dat's own spelling ("int8").
func LookupBuiltinOperator(name, leftType, rightType string) (BuiltinOperator, bool) {
	var leftOID, rightOID uint32
	if leftType != "" {
		leftOID = TypeNameToOID(leftType)
	}
	if rightType != "" {
		rightOID = TypeNameToOID(rightType)
	}
	op, ok := builtinOperatorsByKey[builtinOperatorKey(name, leftOID, rightOID)]
	return op, ok
}

// LookupBuiltinOperatorByOID resolves a built-in operator OID to its curated
// row — the OID→name direction regoper/regoperator rendering needs.
func LookupBuiltinOperatorByOID(oid uint32) (BuiltinOperator, bool) {
	op, ok := builtinOperatorsByOID[oid]
	return op, ok
}

// BuiltinProcs returns all hand-curated built-in pg_proc rows in OID order,
// for callers (e.g. internal/initdb's pg_proc virtual view) that need to
// enumerate the full set rather than look up a single name.
func BuiltinProcs() []BuiltinProc {
	out := make([]BuiltinProc, 0, len(builtinProcsByName))
	for _, p := range builtinProcsByName {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// RegprocName resolves a pg_proc OID to its proname, matching PostgreSQL's
// regprocout (src/backend/utils/adt/regproc.c) — the general OID→name
// lookup a regproc/regprocedure-typed column (e.g. pg_type.typinput,
// pg_operator.oprcode, pg_am.amproc) or a `<oid>::regproc` cast needs to
// render a name instead of a raw number. Backed by the generated
// pgProcNamesByOID (cmd/gen-pg-proc-data -names), a name-only leaf-package
// copy of the ~3397-row PG18 pg_proc.dat table: this is a superset of
// BuiltinProcs/LookupBuiltinProc's small hand-curated forward table (which
// only covers the handful of builtins goopg's own DDL surface references by
// name). ok=false means oid is not a known built-in — callers that also
// need user-defined function OIDs (CREATE FUNCTION) fall back to
// Routines().LookupByOID separately, since a live routine registry isn't
// reachable from this leaf function.
func RegprocName(oid uint32) (string, bool) {
	name, ok := pgProcNamesByOID[oid]
	return name, ok
}

// pgArgTypeDisplayAlias converts an internal base-type spelling (a pg_type.dat
// typname, or a user Routine's stored Type.Name) to PG's format_type_be
// display alias — the handful of base types whose internal name differs from
// its SQL display spelling (int4 -> integer, bool -> boolean, etc). Mirrors
// executor's pgFormatTypeName; duplicated here (not imported) because
// internal/executor imports internal/catalog, not the reverse. Types with no
// alias (composite/domain/array names, "text", "uuid", ...) pass through
// unchanged.
func pgArgTypeDisplayAlias(name string) string {
	switch strings.ToLower(name) {
	case "int4", "int":
		return "integer"
	case "int2":
		return "smallint"
	case "int8":
		return "bigint"
	case "float4":
		return "real"
	case "float8":
		return "double precision"
	case "bool":
		return "boolean"
	case "bpchar":
		return "character"
	case "varchar":
		return "character varying"
	case "timestamptz":
		return "timestamp with time zone"
	case "timestamp":
		return "timestamp without time zone"
	case "timetz":
		return "time with time zone"
	case "time":
		return "time without time zone"
	case "decimal":
		return "numeric"
	}
	return name
}

// RegprocedureName resolves a pg_proc OID to PG's regprocedure display form
// `name(argtype1,argtype2)` — format_procedure/regprocedureout (regproc.c),
// which (unlike regproc's bare name) also renders the INPUT argument-type
// list so an overloaded name is disambiguated. pg_dump relies on this: e.g.
// dumpOpclass/dumpOpfamily cast a pg_amproc.amproc OID to ::regprocedure.
// Tries the generated pg_proc.dat OID index first (built-ins), then the live
// routine registry (routines may be nil if the caller has none to hand — the
// InMemory carries its own via Routines()). OUT-only routine parameters are
// skipped (pg_proc.proargtypes itself only ever lists IN/INOUT/VARIADIC
// args, so a built-in row never needs this filter). ok=false means oid
// resolves to neither source; the caller falls back to the raw OID, matching
// format_procedure's own numeric fallback for an unknown OID.
func RegprocedureName(oid uint32, routines *Routines) (string, bool) {
	_, sig, ok := RegprocedureNameAndSchema(oid, routines)
	return sig, ok
}

// RegprocedureNameAndSchema is RegprocedureName plus the resolved schema, for
// a caller that needs to decide whether to schema-qualify the name (a
// builtin always resolves to "pg_catalog"; a CREATE FUNCTION-defined routine
// resolves to its declared schema, defaulting to "public" like every other
// unset-namespace field in this file). DU-002 (M0119-0004) slice 412.
func RegprocedureNameAndSchema(oid uint32, routines *Routines) (schema, sig string, ok bool) {
	if argNames, ok := pgProcArgTypeNamesByOID[oid]; ok {
		if name, nameOK := pgProcNamesByOID[oid]; nameOK {
			return "pg_catalog", formatProcedureSignature(name, argNames), true
		}
	}
	if routines != nil {
		if r := routines.LookupByOID(oid); r != nil {
			var argNames []string
			for i, t := range r.ArgTypes {
				if i < len(r.ArgModes) && r.ArgModes[i] == "o" {
					continue
				}
				argNames = append(argNames, t.Name)
			}
			schema := r.Schema
			if schema == "" {
				schema = "public"
			}
			return schema, formatProcedureSignature(r.Name, argNames), true
		}
	}
	return "", "", false
}

func formatProcedureSignature(name string, argTypeNames []string) string {
	args := make([]string, len(argTypeNames))
	for i, a := range argTypeNames {
		args[i] = pgArgTypeDisplayAlias(a)
	}
	return name + "(" + strings.Join(args, ",") + ")"
}

// RegoperatorName resolves an operator OID to PG's "opr_name(lefttype,
// righttype)" regoperator rendering (format_operator, regproc.c) — "NONE"
// for a missing (unary) side. Only user-defined operators are resolvable
// (goopg has no builtin-operator catalog — deferred, see the ledger).
// dumpOpclass/dumpOpfamily cast amopopr::pg_catalog.regoperator for a
// class/family's own OPERATOR entries. DU-002 (M0119-0004) slice 411.
func (c *InMemory) RegoperatorName(oid uint32) (string, bool) {
	_, sig, ok := c.RegoperatorNameAndSchema(oid)
	return sig, ok
}

// RegoperatorNameAndSchema is RegoperatorName plus the operator's resolved
// schema, for a caller that needs to decide whether to schema-qualify the
// name (falls back to "public" when the operator's namespace is unset,
// mirroring UserOperator.NamespaceOIDOrDefault). DU-002 (M0119-0004) slice
// 412.
func (c *InMemory) RegoperatorNameAndSchema(oid uint32) (schema, sig string, ok bool) {
	op := c.LookupUserOperatorByOID(oid)
	if op == nil {
		// Not a user-defined operator — try the small hand-curated builtin
		// set (LookupBuiltinOperatorByOID) before giving up. M0119-0004
		// (DU-002) slice 413.
		if bop, found := LookupBuiltinOperatorByOID(oid); found {
			left, right := "NONE", "NONE"
			if bop.LeftType != "" {
				left = pgArgTypeDisplayAlias(bop.LeftType)
			}
			if bop.RightType != "" {
				right = pgArgTypeDisplayAlias(bop.RightType)
			}
			return "pg_catalog", bop.Name + "(" + left + "," + right + ")", true
		}
		return "", "", false
	}
	left, right := "NONE", "NONE"
	if op.LeftType != "" {
		left = pgArgTypeDisplayAlias(op.LeftType)
	}
	if op.RightType != "" {
		right = pgArgTypeDisplayAlias(op.RightType)
	}
	nsOID := op.NamespaceOIDOrDefault()
	if nsOID == PublicNamespaceOID {
		// SchemaNameForOID is ambiguous here: pg_toast shares public's OID in
		// this simplified model (NewInMemory's schemas map), and an operator
		// is never created in pg_toast, so resolve the common case directly
		// rather than risking a nondeterministic map-iteration pick.
		schema = "public"
	} else {
		schema = c.SchemaNameForOID(nsOID)
	}
	if schema == "" {
		schema = "public"
	}
	return schema, op.Name + "(" + left + "," + right + ")", true
}

// UserMapping is a user-created user mapping (CREATE USER MAPPING FOR <user>
// SERVER <server>). goopg does not execute foreign access; this records just
// enough metadata to round-trip the CREATE/DROP through pg_dump (pg_user_mappings
// virtual view → dumpUserMappings). DU-002 slice 377.
type UserMapping struct {
	OID     uint32 // pg_user_mapping.oid (assigned from the catalog OID counter)
	UmUser  string // the mapped role name; "" / "public" → the PUBLIC pseudo-role
	SrvName string // the referenced server name; resolved to srvid OID at render time
	// Options holds the mapping's OPTIONS as "name=value" elements, the on-disk
	// pg_user_mapping.umoptions text[] representation surfaced by the
	// pg_user_mappings view. pg_dump's dumpUserMappings expands these via
	// pg_options_to_table(umoptions); an empty list → NULL → no OPTIONS clause.
	// DU-002 slice 379.
	Options []string
}

// userMappingKey builds the registry key for a (user, server) pair. The user and
// server names are matched case-insensitively (goopg lowercases unquoted idents).
func userMappingKey(user, server string) string {
	return strings.ToLower(user) + "\x00" + strings.ToLower(server)
}

// RegisterUserMapping records a user mapping, allocating a stable OID on first
// sight. Idempotent: re-registering an existing (user, server) pair returns the
// existing entry without changing its OID (OPTIONS are refreshed when non-empty).
// DU-002 slice 377 (options: slice 379).
func (c *InMemory) RegisterUserMapping(user, server string, options []string) *UserMapping {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userMappings == nil {
		c.userMappings = make(map[string]*UserMapping)
	}
	key := userMappingKey(user, server)
	if m, ok := c.userMappings[key]; ok {
		if len(options) > 0 {
			m.Options = options
		}
		return m
	}
	m := &UserMapping{OID: c.allocOIDLocked(), UmUser: user, SrvName: server, Options: options}
	c.userMappings[key] = m
	return m
}

// DropUserMapping removes a user mapping from the registry. Returns true if found.
// DU-002 slice 377.
func (c *InMemory) DropUserMapping(user, server string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userMappings == nil {
		return false
	}
	key := userMappingKey(user, server)
	if _, ok := c.userMappings[key]; ok {
		delete(c.userMappings, key)
		return true
	}
	return false
}

// ListUserMappings returns all registered user mappings sorted by server name
// then user name (deterministic dump order). DU-002 slice 377.
func (c *InMemory) ListUserMappings() []*UserMapping {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.userMappings) == 0 {
		return nil
	}
	out := make([]*UserMapping, 0, len(c.userMappings))
	for _, m := range c.userMappings {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SrvName != out[j].SrvName {
			return out[i].SrvName < out[j].SrvName
		}
		return out[i].UmUser < out[j].UmUser
	})
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
	delete(c.tableACLs, tbl.OID) // forget any granted privileges (M0118-0008)
	delete(c.tableACLOrder, tbl.OID)
	return nil
}

// DropSessionTempObjects removes every temporary relation owned by the session
// identified by owner ("s<id>"), along with their indexes and ACLs. It backs
// DISCARD TEMP (drop the calling session's temp objects, keep the namespace) and
// connection teardown (full cleanup). It does NOT remove the temp namespace
// itself — DISCARD TEMP keeps it (PostgreSQL reuses pg_temp_N), and session exit
// calls DropTempNamespace separately. Returns the number of relations dropped.
// M0118-0009 (temp-schema-cleanup, design 0118-0091).
func (c *InMemory) DropSessionTempObjects(owner string) int {
	if owner == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var victims []string
	for k, t := range c.tables {
		if t != nil && t.Temp && t.TempOwner == owner {
			victims = append(victims, k)
		}
	}
	for _, k := range victims {
		tbl := c.tables[k]
		if idxs, ok := c.byTable[tbl.OID]; ok {
			for idxKey := range idxs {
				delete(c.indexes, idxKey)
			}
			delete(c.byTable, tbl.OID)
		}
		delete(c.tables, k)
		delete(c.tableACLs, tbl.OID)
		delete(c.tableACLOrder, tbl.OID)
	}
	return len(victims)
}

// SessionTempTableNames returns the (bare) names of every temporary relation
// owned by owner ("s<id>"). It is read-only and is used by temporary-object
// cleanup (DISCARD TEMP / backend exit) to cascade drops to (possibly non-temp)
// routines that depend on a temp table's implicit composite rowtype — the
// rowtype shares the table's name, so the table name is the dependency signal
// goopg uses in lieu of an OID-level pg_depend graph. Call it BEFORE
// DropSessionTempObjects (which removes the tables). M0118-0009
// (temp-schema-cleanup).
func (c *InMemory) SessionTempTableNames(owner string) []string {
	if owner == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	var names []string
	for _, t := range c.tables {
		if t != nil && t.Temp && t.TempOwner == owner {
			names = append(names, t.Name)
		}
	}
	return names
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

// QuoteCollationIdent is the exported form of quoteCollationIdent, for callers
// outside this package (e.g. the planner's pg_collation_for fold, M0122-0005).
func QuoteCollationIdent(s string) string { return quoteCollationIdent(s) }

// BuildIndexDef reconstructs the CREATE INDEX DDL string for an index.
// Used by pg_indexes.indexdef and pg_get_indexdef(). M0097-0023.
// quoteCollationIdent renders a collation name the way ruleutils.c
// generate_collation_name → quote_identifier does: a name is left bare only when
// it would re-parse as itself (starts with a lowercase letter or underscore and
// contains only lowercase letters, digits, and underscores); otherwise it is
// double-quoted with embedded quotes doubled. So "C"/"POSIX" become "C"/"POSIX"
// (quoted, uppercase) while "ucs_basic" stays bare — matching pg_dump's COLLATE
// output. Reserved-word quoting is not reproduced (collation names are rarely
// reserved words); the common collations are covered exactly.
func quoteCollationIdent(s string) string {
	if s == "" {
		return `""`
	}
	safe := true
	for i, c := range s {
		if i == 0 {
			if !((c >= 'a' && c <= 'z') || c == '_') {
				safe = false
				break
			}
		} else if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			safe = false
			break
		}
	}
	// Mirror PostgreSQL quote_identifier(): a char-class-safe, all-lowercase
	// identifier must still be quoted when it is a non-UNRESERVED keyword, so a
	// collation named e.g. "select" renders as "select" in pg_get_indexdef.
	if safe && sqlkeywords.IsReservedForQuoting(s) {
		safe = false
	}
	if safe {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// indexKeyIsBareFuncCall reports whether an index key expression is a plain
// function call, mirroring PG's pg_get_indexdef_worker parenthesization test
// (`IsA(indexkey, FuncExpr) && ((FuncExpr *) indexkey)->funcformat ==
// COERCE_EXPLICIT_CALL`, ruleutils.c). Such keys are emitted WITHOUT the
// surrounding parens the worker otherwise adds to every expression column.
// A no-paren SQL value function (CURRENT_TIMESTAMP, CURRENT_USER, …) deparses
// to a bare keyword rather than a COERCE_EXPLICIT_CALL FuncExpr, so it is
// excluded and keeps the parens. DU-002 slice 360.
func indexKeyIsBareFuncCall(e *parser.Expr) bool {
	if e == nil {
		return false
	}
	fc, ok := (*e).(*parser.FuncCall)
	if !ok {
		return false
	}
	if len(fc.Args) == 0 && fc.Name.Schema == "" && parser.IsNoParenFuncName(strings.ToLower(fc.Name.Name)) {
		return false
	}
	return true
}

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
	// goopg has no native hash access method: a `CREATE INDEX … USING hash`
	// is built on the B-tree substrate, so catalog.Index.Method stays "btree"
	// (it routes through createBTreeIndex) while DeclaredHash remembers the
	// declared method (design 0118-0099). pg_get_indexdef_worker (ruleutils.c)
	// prints `USING %s` from pg_am.amname, so real pg_dump 18.3 emits
	// `USING hash (col)`; surface the declared method here so the dump
	// round-trips byte-identically instead of the substrate's `USING btree`.
	// DU-002 slice 361.
	if idx.DeclaredHash {
		method = "hash"
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
				// PG's pg_get_indexdef_worker (ruleutils.c) wraps an expression
				// key column in parens UNLESS it is a bare function call
				// (IsA(indexkey, FuncExpr) && funcformat == COERCE_EXPLICIT_CALL):
				// `lower(name)` dumps WITHOUT the extra parens, while a non-function
				// expression such as `((qty + id) * mgr_id)` keeps the wrapping
				// parens. Mirror that by skipping the wrap when the parsed key AST
				// is a plain function call. DU-002 slice 360.
				var keyAST *parser.Expr
				if i < len(idx.ColExprs) {
					keyAST = idx.ColExprs[i]
				}
				if indexKeyIsBareFuncCall(keyAST) {
					sb.WriteString(exprStr)
				} else {
					sb.WriteByte('(')
					sb.WriteString(exprStr)
					sb.WriteByte(')')
				}
			} else {
				sb.WriteString("(expr)")
			}
		} else {
			sb.WriteString(col)
		}
		// Non-default per-column collation, e.g. ` COLLATE "C"`. ruleutils.c
		// pg_get_indexdef_worker emits it after the column/expression and before
		// the operator class, via generate_collation_name (which quotes the
		// collname as an identifier), suppressing the type's default collation.
		// goopg records only an explicitly-written collation, so a non-empty entry
		// is by construction non-default. DU-002 slice 313.
		if i < len(idx.ColCollations) && idx.ColCollations[i] != "" {
			sb.WriteString(" COLLATE ")
			sb.WriteString(quoteCollationIdent(idx.ColCollations[i]))
		}
		// Non-default per-column operator class, e.g. ` text_pattern_ops`.
		// ruleutils.c get_opclass_name emits it after the column/expression (and
		// after any COLLATE clause) and before the ASC/DESC ordering, suppressing
		// the type's default opclass. goopg records only an explicitly-written
		// opclass, so a non-empty entry is by construction non-default. DU-002
		// slice 312.
		if i < len(idx.ColOpClasses) && idx.ColOpClasses[i] != "" {
			sb.WriteByte(' ')
			sb.WriteString(idx.ColOpClasses[i])
		}
		// Per-column ASC/DESC + NULLS ordering, mirroring ruleutils.c
		// pg_get_indexdef_worker: DESC defaults to NULLS FIRST (print NULLS LAST
		// when overridden); ASC defaults to NULLS LAST (print NULLS FIRST when
		// set). Defaults are suppressed so a plain index dumps byte-identically.
		desc := i < len(idx.ColDescending) && idx.ColDescending[i]
		nullsFirst := i < len(idx.ColNullsFirst) && idx.ColNullsFirst[i]
		if desc {
			sb.WriteString(" DESC")
			if !nullsFirst {
				sb.WriteString(" NULLS LAST")
			}
		} else if nullsFirst {
			sb.WriteString(" NULLS FIRST")
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
	// NULLS NOT DISTINCT follows the (key) INCLUDE (incl) list and precedes WITH /
	// WHERE, mirroring ruleutils.c pg_get_indexdef_worker. Only meaningful for a
	// unique index. DU-002 slice 134.
	if idx.NullsNotDistinct {
		sb.WriteString(" NULLS NOT DISTINCT")
	}
	// Storage parameters: `WITH (fillfactor='N'[, deduplicate_items='on'|'off'])`.
	// ruleutils.c pg_get_indexdef_worker emits reloptions (via flatten_reloptions,
	// which single-quotes each value) after NULLS NOT DISTINCT and before WHERE.
	// This is the dump path for a plain CREATE INDEX (pg_dump emits indexdef
	// verbatim); the index's pg_class.reloptions virtual cell is the sibling
	// surface used by the constraint-backed index path. DU-002 slices 218/219.
	if opts := idx.reloptionList(); len(opts) > 0 {
		sb.WriteString(" WITH (")
		for i, kv := range opts {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(kv[0])
			sb.WriteString("='")
			sb.WriteString(kv[1])
			sb.WriteString("'")
		}
		sb.WriteString(")")
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
	return c.dropIndexByName(tableOID, constraintName)
}

// DropUniqueConstraint removes the named UNIQUE constraint (index-backed,
// like a primary key) from the table's index registries. Returns true if
// found and removed. Shares dropIndexByName with DropPrimaryKeyConstraint —
// both a PK and a named UNIQUE constraint are stored the same way (an Index
// with IsConstraint set), so the removal logic doesn't need to differ; only
// the caller-side lookup that finds the index by name distinguishes Primary
// from plain Unique. DU-002 slice 433 follow-up.
func (c *InMemory) DropUniqueConstraint(tableOID uint32, constraintName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropIndexByName(tableOID, constraintName)
}

// DropExclusionConstraint removes the named EXCLUDE constraint (index-backed,
// distinguished from UNIQUE/PRIMARY KEY by idx.IsExclusion rather than
// idx.Unique/IsConstraint) from the table's index registries. Returns true if
// found and removed. Shares dropIndexByName — an EXCLUDE index is stored the
// same way as a PK/UNIQUE index; only the caller-side lookup differs. DU-002
// slice 433 follow-up (2nd pass).
func (c *InMemory) DropExclusionConstraint(tableOID uint32, constraintName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropIndexByName(tableOID, constraintName)
}

// dropIndexByName removes the named index (backing either a PRIMARY KEY or a
// UNIQUE constraint) from both the per-table and flat index registries.
// Caller must hold c.mu for writing.
func (c *InMemory) dropIndexByName(tableOID uint32, constraintName string) bool {
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

// DropForeignKeyConstraint removes the named foreign-key constraint from the
// table's ForeignKeys slice. Returns true if found and removed. Unlike
// PK/UNIQUE constraints, a foreign key isn't backed by a separate Index
// registry entry — it lives only on Table.ForeignKeys — so this looks the
// table up by OID and mutates that slice directly. DU-002 slice 433
// follow-up.
func (c *InMemory) DropForeignKeyConstraint(tableOID uint32, constraintName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	tbl, ok := c.tableByOID(tableOID)
	if !ok {
		return false
	}
	for i := range tbl.ForeignKeys {
		if strings.EqualFold(tbl.ForeignKeys[i].Name, constraintName) {
			tbl.ForeignKeys = append(tbl.ForeignKeys[:i], tbl.ForeignKeys[i+1:]...)
			return true
		}
	}
	return false
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
		// Select the oldest relfrozenxid using wraparound-safe modular
		// comparison (mirrors PG vac_update_datfrozenxid's
		// TransactionIdPrecedes). Plain `<` would mis-order XIDs that
		// straddle the 2^32 boundary and pick a too-recent horizon,
		// truncating CLOG status still needed by older frozen tuples.
		if oldest == storage.InvalidTransactionID || storage.XIDPrecedes(t.RelFrozenXID, oldest) {
			oldest = t.RelFrozenXID
		}
	}
	return oldest
}

// SetDatabaseACLChangeXID records xid as the writer of the most recent
// GRANT/REVOKE … ON DATABASE (a pg_database ACL change). See the
// dbACLChangeXID field comment. Design 0118-0098 (intra-grant-inplace-db).
func (c *InMemory) SetDatabaseACLChangeXID(xid storage.TransactionID) {
	c.dbACLChangeXID.Store(uint32(xid))
}

// DatabaseACLChangeXID returns the writer XID of the most recent GRANT/REVOKE …
// ON DATABASE, or InvalidTransactionID (0) if none has occurred. A database-wide
// VACUUM consults it and waits (mvcc.WaitForXID) so an in-place datfrozenxid
// update serialises behind a concurrent uncommitted ACL change, mirroring
// PostgreSQL's heap_inplace_update_scan waiting on the catalog tuple's xmax.
func (c *InMemory) DatabaseACLChangeXID() storage.TransactionID {
	return storage.TransactionID(c.dbACLChangeXID.Load())
}

// SetTableACLChangeXID records xid as the writer of the most recent
// GRANT/REVOKE … ON [TABLE] for the relation identified by oid (a pg_class ACL
// change). See the tableACLChangeXID field comment. Design 0118-0109.
func (c *InMemory) SetTableACLChangeXID(oid, xid uint32) {
	c.tableACLChangeXIDMu.Lock()
	defer c.tableACLChangeXIDMu.Unlock()
	if c.tableACLChangeXID == nil {
		c.tableACLChangeXID = make(map[uint32]uint32)
	}
	c.tableACLChangeXID[oid] = xid
}

// TableACLChangeXID returns the writer XID of the most recent GRANT/REVOKE …
// ON [TABLE] for the relation oid, or InvalidTransactionID (0) if none. ALTER
// TABLE ADD PRIMARY KEY consults it and waits (mvcc.WaitForXID) so its in-place
// relhasindex update serialises behind a concurrent uncommitted ACL change,
// mirroring PostgreSQL's heap_inplace_update waiting on the pg_class tuple xmax.
func (c *InMemory) TableACLChangeXID(oid uint32) storage.TransactionID {
	c.tableACLChangeXIDMu.Lock()
	defer c.tableACLChangeXIDMu.Unlock()
	return storage.TransactionID(c.tableACLChangeXID[oid])
}

// SetTablePendingDropXID records xid as the in-flight transaction that issued a
// `DROP TABLE` deferred to COMMIT for the relation identified by oid. See the
// tablePendingDropXID field comment. Design 0118-0117 (intra-grant-inplace perm
// 10). Cleared by ClearTablePendingDropXID once the DROP is applied at COMMIT or
// cancelled at ROLLBACK; a stale entry is harmless because the wait short-
// circuits the moment the recorded XID is no longer active.
func (c *InMemory) SetTablePendingDropXID(oid, xid uint32) {
	if oid == 0 || xid == 0 {
		return
	}
	c.tablePendingDropXIDMu.Lock()
	defer c.tablePendingDropXIDMu.Unlock()
	if c.tablePendingDropXID == nil {
		c.tablePendingDropXID = make(map[uint32]uint32)
	}
	c.tablePendingDropXID[oid] = xid
}

// TablePendingDropXID returns the writer XID of the in-flight deferred DROP TABLE
// for the relation oid, or InvalidTransactionID (0) if none. A pg_class rowmark
// consults it and waits (mvcc.WaitForXID) so its FOR UPDATE/SHARE serialises
// behind the concurrent uncommitted DROP, mirroring PostgreSQL's tuple lock
// waiting on the pg_class tuple's delete xmax. Design 0118-0117.
func (c *InMemory) TablePendingDropXID(oid uint32) storage.TransactionID {
	c.tablePendingDropXIDMu.Lock()
	defer c.tablePendingDropXIDMu.Unlock()
	return storage.TransactionID(c.tablePendingDropXID[oid])
}

// ClearTablePendingDropXID drops the pending-DROP xmax entry for relation oid.
// Called when the deferred DROP is applied at COMMIT or cancelled at ROLLBACK.
// Design 0118-0117.
func (c *InMemory) ClearTablePendingDropXID(oid uint32) {
	if oid == 0 {
		return
	}
	c.tablePendingDropXIDMu.Lock()
	defer c.tablePendingDropXIDMu.Unlock()
	delete(c.tablePendingDropXID, oid)
}

// AddPgClassRowMark records that transaction xid holds an explicit row lock on
// the pg_class tuple for relation relOID. conflictsWithInplace is true when the
// lock mode (FOR SHARE / FOR NO KEY UPDATE / FOR UPDATE) conflicts with a
// concurrent in-place update of that tuple; FOR KEY SHARE records false. See the
// pgClassRowMarks field comment. Design 0118-0113.
func (c *InMemory) AddPgClassRowMark(relOID, xid uint32, conflictsWithInplace bool) {
	if relOID == 0 || xid == 0 {
		return
	}
	c.pgClassRowMarksMu.Lock()
	defer c.pgClassRowMarksMu.Unlock()
	if c.pgClassRowMarks == nil {
		c.pgClassRowMarks = make(map[uint32]map[uint32]bool)
	}
	holders := c.pgClassRowMarks[relOID]
	if holders == nil {
		holders = make(map[uint32]bool)
		c.pgClassRowMarks[relOID] = holders
	}
	// A stronger later acquisition by the same xid (e.g. KEY SHARE then NO KEY
	// UPDATE) must not be downgraded by an earlier weak mark — OR the flag.
	holders[xid] = holders[xid] || conflictsWithInplace
}

// PgClassRowMarks returns the explicit row-lock holders on relation relOID's
// pg_class tuple. The caller filters out its own transaction tree and waits on
// the conflicting remainder. Design 0118-0113.
func (c *InMemory) PgClassRowMarks(relOID uint32) []PgClassRowMark {
	c.pgClassRowMarksMu.Lock()
	defer c.pgClassRowMarksMu.Unlock()
	holders := c.pgClassRowMarks[relOID]
	if len(holders) == 0 {
		return nil
	}
	out := make([]PgClassRowMark, 0, len(holders))
	for xid, conflicts := range holders {
		out = append(out, PgClassRowMark{XID: xid, ConflictsWithInplace: conflicts})
	}
	return out
}

// ClearPgClassRowMark drops the single pg_class rowmark held by transaction xid
// on relation relOID. Called when a FOR UPDATE/SHARE locker that recorded the
// mark up front (so a peer would block behind it) ends up locking no tuple —
// e.g. the relation's pg_class row was concurrently DELETEd and the locker now
// returns 0 rows. PG holds no tuple lock in that case, so a peer waiting behind
// the mark must proceed immediately. Design 0118-0117 (intra-grant-inplace perm
// 10).
func (c *InMemory) ClearPgClassRowMark(relOID, xid uint32) {
	if relOID == 0 || xid == 0 {
		return
	}
	c.pgClassRowMarksMu.Lock()
	defer c.pgClassRowMarksMu.Unlock()
	if holders := c.pgClassRowMarks[relOID]; holders != nil {
		delete(holders, xid)
		if len(holders) == 0 {
			delete(c.pgClassRowMarks, relOID)
		}
	}
}

// ClearPgClassRowMarksForXID drops every pg_class rowmark held by transaction
// xid. Called when the transaction commits or aborts so a finished locker no
// longer appears as a held lock. Design 0118-0113.
func (c *InMemory) ClearPgClassRowMarksForXID(xid uint32) {
	if xid == 0 {
		return
	}
	c.pgClassRowMarksMu.Lock()
	defer c.pgClassRowMarksMu.Unlock()
	for relOID, holders := range c.pgClassRowMarks {
		if _, ok := holders[xid]; ok {
			delete(holders, xid)
			if len(holders) == 0 {
				delete(c.pgClassRowMarks, relOID)
			}
		}
	}
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
	// PostgreSQL allocates two OIDs per enum: the enum type itself, then its
	// auto-generated array type (`_name`). Mirror that ordering so the base OID
	// keeps its historic value and the array column has a distinct OID. DU-002
	// slice 89.
	et := &EnumType{
		Name:     k,
		OID:      c.nextOID,
		ArrayOID: c.nextOID + 1,
		Values:   evs,
	}
	c.nextOID += 2
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

// LookupEnumByOID finds an enum type by its pg_type OID. DU-002 slice 88
// (pg_dump enum round-trip): format_type resolves an enum column's
// pg_attribute.atttypid back to the enum's schema-qualified name, and the
// enum's OID is dynamically allocated at CREATE TYPE time, so a name-only
// lookup is insufficient. Returns nil,false when no enum has the OID.
func (c *InMemory) LookupEnumByOID(oid uint32) (*EnumType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, et := range c.enumTypes {
		if et.OID == oid {
			return et, true
		}
	}
	return nil, false
}

// LookupEnumByArrayOID finds a user-defined enum type by the pg_type OID of its
// auto-generated array type (`_name`). Used by format_type to render an
// enum-array column (`mood[]`) as the schema-qualified array name. DU-002
// slice 89.
func (c *InMemory) LookupEnumByArrayOID(oid uint32) (*EnumType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, et := range c.enumTypes {
		if et.ArrayOID == oid {
			return et, true
		}
	}
	return nil, false
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

// SetEnumOwner records the typowner role OID for an existing enum type.
// Returns false if no such enum is registered. Mirrors SetCollationOwner.
// M0122-0005 (m0097-0017 follow-up).
func (c *InMemory) SetEnumOwner(name string, ownerOID uint32) bool {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	et, ok := c.enumTypes[k]
	if !ok {
		return false
	}
	et.Owner = ownerOID
	return true
}

// RenameCompositeType renames a composite type from oldName to newName,
// mirroring RenameEnum. M0122-0005 (m0097-0017 follow-up): ALTER TYPE ...
// RENAME TO previously always called RenameEnum regardless of the target
// type's kind, so renaming a composite type raised a spurious "type does not
// exist" (42710) instead of renaming it.
func (c *InMemory) RenameCompositeType(oldName, newName string) error {
	ok := strings.ToLower(oldName)
	nk := strings.ToLower(newName)
	c.mu.Lock()
	defer c.mu.Unlock()
	ct, found := c.compositeTypes[ok]
	if !found {
		return fmt.Errorf("type %q does not exist", oldName)
	}
	if _, exists := c.compositeTypes[nk]; exists {
		return fmt.Errorf("type %q already exists", newName)
	}
	delete(c.compositeTypes, ok)
	delete(c.compositeTypeNames, ok)
	delete(c.compositeTypeFields, ok)
	ct.Name = nk
	c.compositeTypes[nk] = ct
	c.compositeTypeNames[nk] = true
	c.compositeTypeFields[nk] = ct.Fields
	return nil
}

// SetCompositeTypeOwner records the typowner role OID for an existing
// composite type. Returns false if no such composite type is registered.
// Mirrors SetEnumOwner. M0122-0005 (m0097-0017 follow-up).
func (c *InMemory) SetCompositeTypeOwner(name string, ownerOID uint32) bool {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	ct, ok := c.compositeTypes[k]
	if !ok {
		return false
	}
	ct.Owner = ownerOID
	return true
}

// RenameRangeType renames a range type from oldName to newName, mirroring
// RenameCompositeType/RenameEnum. `ALTER TYPE ... RENAME TO` previously always
// dispatched to RenameEnum regardless of kind, so renaming a range type raised
// a spurious "type does not exist" (42710) instead of renaming it — the same
// dispatch-by-kind gap RenameCompositeType closed for composite types. The
// auto-generated multirange name is left untouched (it is a distinct pg_type
// row with its own name, unaffected by renaming the range type itself, mirroring
// real PostgreSQL). M0122-0005 (range-type follow-up).
func (c *InMemory) RenameRangeType(oldName, newName string) error {
	ok := strings.ToLower(oldName)
	nk := strings.ToLower(newName)
	c.mu.Lock()
	defer c.mu.Unlock()
	rt, found := c.rangeTypes[ok]
	if !found {
		return fmt.Errorf("type %q does not exist", oldName)
	}
	if _, exists := c.rangeTypes[nk]; exists {
		return fmt.Errorf("type %q already exists", newName)
	}
	delete(c.rangeTypes, ok)
	rt.Name = nk
	c.rangeTypes[nk] = rt
	return nil
}

// SetRangeTypeOwner records the typowner role OID for an existing range type.
// Returns false if no such range type is registered. Mirrors
// SetCompositeTypeOwner/SetEnumOwner. M0122-0005 (range-type follow-up).
func (c *InMemory) SetRangeTypeOwner(name string, ownerOID uint32) bool {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	rt, ok := c.rangeTypes[k]
	if !ok {
		return false
	}
	rt.Owner = ownerOID
	return true
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
func (c *InMemory) RegisterCompositeTypeWithFields(name string, fields []CompositeField) *CompositeType {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compositeTypeNames[k] = true
	c.compositeTypeFields[k] = fields
	// Allocate three OIDs (the type itself, its auto-generated `_name` array
	// type, then the implicit pg_class relation of relkind='c' that carries the
	// field columns) once per type, mirroring RegisterEnum so re-registration
	// keeps the OIDs stable. The relation OID is what pg_type.typrelid points at
	// and what pg_dump's dumpCompositeType walks to find the field list via
	// pg_attribute. DU-002 slice 242 (type+array), slice 243 (relation).
	ct, ok := c.compositeTypes[k]
	if !ok {
		ct = &CompositeType{Name: k, OID: c.nextOID, ArrayOID: c.nextOID + 1, RelOID: c.nextOID + 2}
		c.nextOID += 3
		c.compositeTypes[k] = ct
	}
	ct.Fields = fields
	return ct
}

// LookupCompositeType returns the OID-bearing metadata for a composite type, or
// nil if no such type exists. DU-002 slice 242.
func (c *InMemory) LookupCompositeType(name string) *CompositeType {
	k := strings.ToLower(name)
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.compositeTypes[k]
}

// LookupCompositeTypeByOID finds a composite type by its pg_type OID. Used by
// format_type to render a nested-composite column's declared type as its
// schema-qualified composite name (not `record` or the text fallback). DU-002
// slice 249.
func (c *InMemory) LookupCompositeTypeByOID(oid uint32) (*CompositeType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ct := range c.compositeTypes {
		if ct.OID == oid {
			return ct, true
		}
	}
	return nil, false
}

// LookupCompositeTypeByArrayOID finds a composite type by the pg_type OID of its
// auto-generated array type (`_name`). Used by format_type to render a
// composite-array field (`addr[]`) as the schema-qualified array name. DU-002
// slice 250.
func (c *InMemory) LookupCompositeTypeByArrayOID(oid uint32) (*CompositeType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ct := range c.compositeTypes {
		if ct.ArrayOID == oid {
			return ct, true
		}
	}
	return nil, false
}

// builtinOpclassInfo describes one built-in btree operator class row, enough
// to satisfy pg_dump's `dumpRangeType` join (`pg_range r, pg_type st,
// pg_opclass opc WHERE opc.oid = rngsubopc`) and to serve as the resolved
// default opclass for a `CREATE TYPE ... AS RANGE (subtype = ...)`. OIDs/
// family OIDs are the real PG18 values (postgres/src/include/catalog/
// pg_opclass.dat), duplicated from internal/initdb/initdb.go's
// pgOpclassEntry list (executor/catalog cannot import initdb — import
// cycle — see pgTypeColumnsPG18 for the same duplication pattern).
type builtinOpclassInfo struct {
	OID      uint32
	Name     string
	Family   uint32
	IntypeOID uint32
}

const btreeAccessMethodOID uint32 = 403

// builtinRangeSubtypeOpclasses covers every PG18 built-in scalar type that
// has a real default btree operator class, keyed by the subtype's own pg_type
// OID — not just the five built-in range types' subtypes (int4/int8/numeric/
// date/timestamp/timestamptz). Values were captured empirically from a live
// `postgres/local_install` PG 18.3 instance (`select oid, opcname, opcfamily
// from pg_opclass where opcmethod = btree's oid and opcdefault`, cross-checked
// per-subtype via `CREATE TYPE ... AS RANGE (subtype = ...)` +
// `pg_range.rngsubopc`), since most of these opclass OIDs are genbki-assigned
// (not pinned in pg_opclass.dat) and so aren't derivable from source alone —
// same reasoning as the pgOpclassEntry duplication note above. Two subtypes
// resolve to an opclass whose own opcintype differs from the subtype: varchar
// has no *default* varchar_ops (opcdefault=false), so PG's opclass search
// falls back to the binary-coercible text_ops (IntypeOID=OIDText); cidr binary
// -coerces to inet_ops the same way (IntypeOID=OIDInet). A subtype outside
// this set has no resolvable default opclass in goopg today — RegisterRangeType
// reports that as PG's own ERRCODE_UNDEFINED_OBJECT ("... has no default
// operator class for access method \"btree\"") rather than silently
// registering a broken range. DU-002 (M0110-0001).
var builtinRangeSubtypeOpclasses = map[uint32]builtinOpclassInfo{
	OIDInt2:        {OID: 1979, Name: "int2_ops", Family: 1976, IntypeOID: OIDInt2},
	OIDInt4:        {OID: 1978, Name: "int4_ops", Family: 1976, IntypeOID: OIDInt4},
	OIDInt8:        {OID: 3124, Name: "int8_ops", Family: 1976, IntypeOID: OIDInt8},
	OIDNumeric:     {OID: 3125, Name: "numeric_ops", Family: 1988, IntypeOID: OIDNumeric},
	OIDFloat4:      {OID: 10012, Name: "float4_ops", Family: 1970, IntypeOID: OIDFloat4},
	OIDFloat8:      {OID: 3123, Name: "float8_ops", Family: 1970, IntypeOID: OIDFloat8},
	OIDDate:        {OID: 3122, Name: "date_ops", Family: 434, IntypeOID: OIDDate},
	OIDTime:        {OID: 10038, Name: "time_ops", Family: 1996, IntypeOID: OIDTime},
	OIDTimeTZ:      {OID: 10041, Name: "timetz_ops", Family: 2000, IntypeOID: OIDTimeTZ},
	OIDTimestamp:   {OID: 3128, Name: "timestamp_ops", Family: 434, IntypeOID: OIDTimestamp},
	OIDTimestampTZ: {OID: 3127, Name: "timestamptz_ops", Family: 434, IntypeOID: OIDTimestampTZ},
	OIDInterval:    {OID: 10022, Name: "interval_ops", Family: 1982, IntypeOID: OIDInterval},
	OIDText:        {OID: 3126, Name: "text_ops", Family: 1994, IntypeOID: OIDText},
	OIDVarChar:     {OID: 3126, Name: "text_ops", Family: 1994, IntypeOID: OIDText},
	OIDBpChar:      {OID: 10004, Name: "bpchar_ops", Family: 426, IntypeOID: OIDBpChar},
	OIDName:        {OID: 10028, Name: "name_ops", Family: 1994, IntypeOID: OIDName},
	OIDChar:        {OID: 10007, Name: "char_ops", Family: 429, IntypeOID: OIDChar},
	OIDBool:        {OID: 10003, Name: "bool_ops", Family: 424, IntypeOID: OIDBool},
	OIDBytea:       {OID: 10006, Name: "bytea_ops", Family: 428, IntypeOID: OIDBytea},
	OIDOID:         {OID: 1981, Name: "oid_ops", Family: 1989, IntypeOID: OIDOID},
	OIDTid:         {OID: 10050, Name: "tid_ops", Family: 2789, IntypeOID: OIDTid},
	OIDOidvector:   {OID: 10032, Name: "oidvector_ops", Family: 1991, IntypeOID: OIDOidvector},
	OIDUUID:        {OID: 10065, Name: "uuid_ops", Family: 2968, IntypeOID: OIDUUID},
	OIDPgLsn:       {OID: 10067, Name: "pg_lsn_ops", Family: 3253, IntypeOID: OIDPgLsn},
	OIDXid8:        {OID: 10053, Name: "xid8_ops", Family: 5067, IntypeOID: OIDXid8},
	OIDMoney:       {OID: 10047, Name: "money_ops", Family: 2099, IntypeOID: OIDMoney},
	OIDBit:         {OID: 10002, Name: "bit_ops", Family: 423, IntypeOID: OIDBit},
	OIDVarbit:      {OID: 10043, Name: "varbit_ops", Family: 2002, IntypeOID: OIDVarbit},
	OIDMacaddr:     {OID: 10024, Name: "macaddr_ops", Family: 1984, IntypeOID: OIDMacaddr},
	OIDMacaddr8:    {OID: 10026, Name: "macaddr8_ops", Family: 3371, IntypeOID: OIDMacaddr8},
	OIDInet:        {OID: 10015, Name: "inet_ops", Family: 1974, IntypeOID: OIDInet},
	OIDCidr:        {OID: 10015, Name: "inet_ops", Family: 1974, IntypeOID: OIDInet},
}

// DefaultBtreeOpclassForSubtype returns the default btree operator class OID
// for a range subtype's pg_type OID, or ok=false if goopg has no default
// opclass on record for it (see builtinRangeSubtypeOpclasses). M0110-0001.
func DefaultBtreeOpclassForSubtype(subtypeOID uint32) (uint32, bool) {
	oc, ok := builtinRangeSubtypeOpclasses[subtypeOID]
	if !ok {
		return 0, false
	}
	return oc.OID, true
}

// builtinOpclassRowByOID renders a single builtinRangeSubtypeOpclasses entry
// as a pg_opclass virtual-table row (see pg_opclass's VirtualRows), so the
// pg_dump `dumpRangeType` join finds a matching row for a range type's
// resolved default opclass. Only emitted lazily, keyed by the OIDs actually
// referenced by a registered range type — pg_opclass must otherwise stay
// exactly as populated by CREATE OPERATOR CLASS (TestCreateOperatorClassPopulatesOpclassRow
// and siblings assert an exact row count). opcnamespace=11 (pg_catalog),
// opcowner=10 (bootstrap superuser), opcdefault=true, opckeytype=0 for all of
// them — matching PG18's real rows. M0110-0001.
func builtinOpclassRowByOID(oid uint32) ([]string, bool) {
	for _, oc := range builtinRangeSubtypeOpclasses {
		if oc.OID != oid {
			continue
		}
		return []string{
			strconv.FormatUint(uint64(oc.OID), 10),               // oid
			strconv.FormatUint(uint64(btreeAccessMethodOID), 10), // opcmethod
			oc.Name,                                    // opcname
			"11",                                        // opcnamespace = pg_catalog
			"10",                                        // opcowner = bootstrap superuser
			strconv.FormatUint(uint64(oc.Family), 10),   // opcfamily
			strconv.FormatUint(uint64(oc.IntypeOID), 10), // opcintype
			"t", // opcdefault
			"0", // opckeytype
		}, true
	}
	return nil, false
}

// deriveMultirangeTypeName mirrors PostgreSQL's makeMultirangeTypeName
// (postgres/src/backend/catalog/pg_type.c): if the range type name contains
// "range", replace the first occurrence with "multirange"; otherwise append
// "_multirange". M0110-0001.
func deriveMultirangeTypeName(rangeName string) string {
	if idx := strings.Index(rangeName, "range"); idx >= 0 {
		return rangeName[:idx] + "multi" + rangeName[idx:]
	}
	return rangeName + "_multirange"
}

// RangeTypeOptionError carries the PostgreSQL SQLSTATE for a `CREATE TYPE
// ... AS RANGE` option-resolution failure (missing default opclass, a named
// `subtype_opclass` that doesn't exist or doesn't accept the subtype, or a
// `collation` that doesn't exist or was given for a non-collatable subtype),
// so the executor can report PG's own code instead of collapsing every
// RegisterRangeType failure onto 42704 undefined_object. DU-002 (M0110-0001,
// slice 429 follow-up sub-item (a)).
type RangeTypeOptionError struct {
	Code    string
	Message string
}

func (e *RangeTypeOptionError) Error() string { return e.Message }

// builtinCollationOIDByName resolves one of PG18's 7 BKI-pinned collation
// names to its OID. Duplicated from the `pg_collation` VirtualRows seed
// above / the executor's own `collationNameToOID` (internal/executor/
// pg18_user_catalog_rows.go) rather than shared, since RegisterRangeType
// lives in the catalog package and executor cannot be imported back into it
// — same import-cycle reasoning as builtinOpclassInfo/builtinRangeSubtypeOpclasses.
// Matching is case-sensitive, mirroring real PostgreSQL identifier semantics
// (`collation = "C"` requires the exact case; there is no unquoted `c`
// built-in). DU-002 (M0110-0001, slice 429 follow-up sub-item (a)).
func builtinCollationOIDByName(name string) (uint32, bool) {
	switch name {
	case "default":
		return 100, true
	case "C":
		return 950, true
	case "POSIX":
		return 951, true
	case "ucs_basic":
		return 962, true
	case "unicode":
		return 963, true
	case "pg_c_utf8":
		return 811, true
	case "pg_unicode_fast":
		return 6411, true
	}
	return 0, false
}

// builtinOpclassOIDByName looks up a builtinRangeSubtypeOpclasses entry by
// its opclass name (case-sensitive), for resolving an explicit
// `subtype_opclass = ...` range option. DU-002 (M0110-0001, slice 429
// follow-up sub-item (a)).
func builtinOpclassOIDByName(name string) (builtinOpclassInfo, bool) {
	for _, oc := range builtinRangeSubtypeOpclasses {
		if oc.Name == name {
			return oc, true
		}
	}
	return builtinOpclassInfo{}, false
}

// defaultUserBtreeOpclassForSubtype mirrors GetDefaultOpClass's (postgres/
// src/backend/catalog/pg_opclass.c) pg_opclass scan for a user-created
// default btree opclass over the given subtype. Real PG stores builtin and
// user opclasses in the same pg_opclass table, so a single scan finds
// either; goopg splits them into the curated `builtinRangeSubtypeOpclasses`
// map (checked first by callers via DefaultBtreeOpclassForSubtype, since it
// never changes at runtime) and the live `userOperatorClasses` registry
// (this method) — callers must check both to reproduce the single-scan
// behavior. DU-002 (M0110-0001, slice 429 follow-up sub-item (b)).
func (c *InMemory) defaultUserBtreeOpclassForSubtype(subtypeOID uint32) (uint32, bool) {
	for _, uoc := range c.ListUserOperatorClasses() {
		if uoc.Method == btreeAccessMethodOID && uoc.InTypeOID == subtypeOID && uoc.IsDefault {
			return uoc.OID, true
		}
	}
	return 0, false
}

// resolveRangeOpclass implements PG's findRangeSubOpclass (postgres/src/
// backend/commands/typecmds.c): an explicit `subtype_opclass` name must
// name an existing btree opclass that accepts (is binary-coercible with)
// the subtype, else the default opclass is used. DU-002 (M0110-0001, slice
// 429 follow-up sub-item (a)).
func (c *InMemory) resolveRangeOpclass(subtypeName string, subtypeOID uint32, opclassName string) (uint32, *RangeTypeOptionError) {
	if opclassName == "" {
		if oid, ok := DefaultBtreeOpclassForSubtype(subtypeOID); ok {
			return oid, nil
		}
		if oid, ok := c.defaultUserBtreeOpclassForSubtype(subtypeOID); ok {
			return oid, nil
		}
		return 0, &RangeTypeOptionError{Code: "42704", Message: fmt.Sprintf(
			"data type %s has no default operator class for access method %q", subtypeName, "btree")}
	}
	if oc, ok := builtinOpclassOIDByName(opclassName); ok {
		if oc.IntypeOID != subtypeOID {
			return 0, &RangeTypeOptionError{Code: "42804", Message: fmt.Sprintf(
				"operator class %q does not accept data type %s", opclassName, subtypeName)}
		}
		return oc.OID, nil
	}
	for _, uoc := range c.ListUserOperatorClasses() {
		if uoc.Method != btreeAccessMethodOID || !strings.EqualFold(uoc.Name, opclassName) {
			continue
		}
		if uoc.InTypeOID != subtypeOID {
			return 0, &RangeTypeOptionError{Code: "42804", Message: fmt.Sprintf(
				"operator class %q does not accept data type %s", opclassName, subtypeName)}
		}
		return uoc.OID, nil
	}
	return 0, &RangeTypeOptionError{Code: "42704", Message: fmt.Sprintf(
		"operator class %q does not exist for access method %q", opclassName, "btree")}
}

// rangeCollatableSubtypes are the subtype OIDs RegisterRangeType treats as
// collatable for a range's `collation` option — the same set the pg_range
// VirtualRows builder above already special-cases for the implicit-default
// case. Real PostgreSQL's `type_is_collatable` covers more built-ins, but
// these are the only range subtypes goopg's own typcollation resolution
// models today. DU-002 (M0110-0001, slice 429 follow-up sub-item (a)).
func rangeSubtypeIsCollatable(subtypeOID uint32) bool {
	switch subtypeOID {
	case OIDText, OIDVarChar, OIDBpChar:
		return true
	}
	return false
}

// resolveRangeCollation implements PG's DefineRange collation resolution
// (postgres/src/backend/commands/typecmds.c): an explicit `collation` name
// is only legal for a collatable subtype and must resolve to a known
// collation (built-in or `CREATE COLLATION`-registered); otherwise the
// subtype's own default collation (DEFAULT_COLLATION_OID for text/varchar/
// bpchar) applies, and a non-collatable subtype gets InvalidOid (0).
// DU-002 (M0110-0001, slice 429 follow-up sub-item (a)).
func (c *InMemory) resolveRangeCollation(subtypeOID uint32, collationName string) (uint32, *RangeTypeOptionError) {
	if !rangeSubtypeIsCollatable(subtypeOID) {
		if collationName != "" {
			return 0, &RangeTypeOptionError{Code: "42809", Message: "range collation specified but subtype does not support collation"}
		}
		return 0, nil
	}
	if collationName == "" {
		return 100, nil // DEFAULT_COLLATION_OID — subtype's own typcollation
	}
	if oid, ok := builtinCollationOIDByName(collationName); ok {
		return oid, nil
	}
	if oid := c.UserCollationOIDByName(collationName); oid != 0 {
		return oid, nil
	}
	return 0, &RangeTypeOptionError{Code: "42704", Message: fmt.Sprintf(
		"collation %q for encoding %q does not exist", collationName, "UTF8")}
}

// RegisterRangeType records a `CREATE TYPE ... AS RANGE (subtype = ...)`
// range type, allocating pg_type OIDs for the range itself, its
// auto-generated `_name` array type, its auto-generated multirange type, and
// the multirange's own auto-generated array type — matching PG's real
// allocation order (range, range-array, multirange, multirange-array).
// opclassName/collationName are the (possibly empty) `subtype_opclass` /
// `collation` option values; empty means "use PG's default resolution".
// Returns a *RangeTypeOptionError (carrying PG's own SQLSTATE) if any option
// fails to resolve. M0110-0001.
func (c *InMemory) RegisterRangeType(name, subtypeName, explicitMultirangeName, opclassName, collationName string) (*RangeType, error) {
	subtypeOID := TypeNameToOID(subtypeName)
	opclassOID, rerr := c.resolveRangeOpclass(subtypeName, subtypeOID, opclassName)
	if rerr != nil {
		return nil, rerr
	}
	collationOID, rerr := c.resolveRangeCollation(subtypeOID, collationName)
	if rerr != nil {
		return nil, rerr
	}
	k := strings.ToLower(name)
	mrName := strings.ToLower(explicitMultirangeName)
	if mrName == "" {
		mrName = deriveMultirangeTypeName(k)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rt, exists := c.rangeTypes[k]
	if !exists {
		rt = &RangeType{
			Name:               k,
			OID:                c.nextOID,
			ArrayOID:           c.nextOID + 1,
			MultirangeOID:      c.nextOID + 2,
			MultirangeArrayOID: c.nextOID + 3,
		}
		c.nextOID += 4
		c.rangeTypes[k] = rt
	}
	rt.SubtypeName = subtypeName
	rt.OpclassOID = opclassOID
	rt.CollationOID = collationOID
	rt.MultirangeName = mrName
	return rt, nil
}

// LookupRangeType finds a user-defined range type by name (case-insensitive).
// M0110-0001.
func (c *InMemory) LookupRangeType(name string) (*RangeType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rt, ok := c.rangeTypes[strings.ToLower(name)]
	return rt, ok
}

// LookupRangeTypeByMultirangeName finds a user-defined range type by its
// auto-generated multirange type's name (case-insensitive). rangeTypes is
// keyed by the range name only, so this is a linear scan — mirrors the other
// ByOID-style lookups below, which scan the same small map. M0110-0001 DU-002
// slice 429 follow-up.
func (c *InMemory) LookupRangeTypeByMultirangeName(name string) (*RangeType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	lower := strings.ToLower(name)
	for _, rt := range c.rangeTypes {
		if rt.MultirangeName == lower {
			return rt, true
		}
	}
	return nil, false
}

// LookupRangeTypeByOID finds a user-defined range type by its pg_type OID,
// used by format_type to render a range column's declared type. M0110-0001.
func (c *InMemory) LookupRangeTypeByOID(oid uint32) (*RangeType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, rt := range c.rangeTypes {
		if rt.OID == oid {
			return rt, true
		}
	}
	return nil, false
}

// LookupRangeTypeByMultirangeOID finds a user-defined range type by the
// pg_type OID of its auto-generated multirange type, used by format_type to
// resolve pg_range.rngmultitypid via `format_type(rngmultitypid, NULL)`.
// M0110-0001.
func (c *InMemory) LookupRangeTypeByMultirangeOID(oid uint32) (*RangeType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, rt := range c.rangeTypes {
		if rt.MultirangeOID == oid {
			return rt, true
		}
	}
	return nil, false
}

// LookupRangeTypeByArrayOID finds a user-defined range type by the pg_type
// OID of its auto-generated `_name` array type, used by format_type to
// render a `myrange[]` column as the schema-qualified array name. Mirrors
// LookupCompositeTypeByArrayOID / LookupEnumByArrayOID. DU-002 (M0110-0001).
func (c *InMemory) LookupRangeTypeByArrayOID(oid uint32) (*RangeType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, rt := range c.rangeTypes {
		if rt.ArrayOID == oid {
			return rt, true
		}
	}
	return nil, false
}

// LookupRangeTypeByMultirangeArrayOID finds a user-defined range type by the
// pg_type OID of its multirange's auto-generated `_name` array type, used by
// format_type to render a `mymultirange[]` column as the schema-qualified
// array name. DU-002 (M0110-0001).
func (c *InMemory) LookupRangeTypeByMultirangeArrayOID(oid uint32) (*RangeType, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, rt := range c.rangeTypes {
		if rt.MultirangeArrayOID == oid {
			return rt, true
		}
	}
	return nil, false
}

// ListRangeTypes returns every registered range type, for pg_type/pg_range
// virtual-row generation (pg_dump CREATE TYPE ... AS RANGE round-trip).
// M0110-0001.
func (c *InMemory) ListRangeTypes() []*RangeType {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*RangeType, 0, len(c.rangeTypes))
	for _, rt := range c.rangeTypes {
		out = append(out, rt)
	}
	return out
}

// DropRangeType removes a range type. Returns an error if not found.
// M0110-0001.
func (c *InMemory) DropRangeType(name string) error {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.rangeTypes[k]; !ok {
		return fmt.Errorf("type %q does not exist", name)
	}
	delete(c.rangeTypes, k)
	return nil
}

// RegisterRangeTypeDuringRecovery is the idempotent version of
// RegisterRangeType used by the WAL-replay driver
// (internal/initdb/range_type_ddl_recovery.go). Unlike RegisterRangeType it
// takes both OIDs from the WAL record (so the recovered range type matches
// the pre-crash OIDs exactly) and overwrites rather than erroring when a
// range type with the same name is already present (replay may see the same
// record more than once across a partial-then-full replay). Mirrors
// catalog.InMemory.RegisterAccessMethodDuringRecovery. DU-002
// restart-persistence follow-up (M0110-0001, DU-002 slice 429 ledger resume
// point, sub-item (c)).
func (c *InMemory) RegisterRangeTypeDuringRecovery(rt *RangeType) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rangeTypes == nil {
		c.rangeTypes = make(map[string]*RangeType)
	}
	out := *rt
	c.rangeTypes[rt.Name] = &out
	c.advanceNextOIDLocked(rt.OID)
	c.advanceNextOIDLocked(rt.ArrayOID)
	c.advanceNextOIDLocked(rt.MultirangeOID)
	c.advanceNextOIDLocked(rt.MultirangeArrayOID)
}

// RenameRangeTypeDuringRecovery is the idempotent version of RenameRangeType
// for WAL replay, mirroring RenameCollationDuringRecovery. M0122-0005
// restart-persistence follow-up.
func (c *InMemory) RenameRangeTypeDuringRecovery(oldName, newName string) {
	_ = c.RenameRangeType(oldName, newName)
}

// SetRangeTypeOwnerDuringRecovery is the idempotent version of
// SetRangeTypeOwner for WAL replay, mirroring SetCollationOwnerDuringRecovery.
// M0122-0005 restart-persistence follow-up.
func (c *InMemory) SetRangeTypeOwnerDuringRecovery(name string, ownerOID uint32) {
	c.SetRangeTypeOwner(name, ownerOID)
}

// DropRangeTypeDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropRangeType. Identical to DropRangeType but discards
// the found/not-found result — replay does not care whether the range type
// was still present. DU-002 restart-persistence follow-up (M0110-0001,
// DU-002 slice 429 ledger resume point, sub-item (c)).
func (c *InMemory) DropRangeTypeDuringRecovery(name string) {
	_ = c.DropRangeType(name)
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
	delete(c.compositeTypeFields, k)
	delete(c.compositeTypes, k)
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
	// PostgreSQL allocates two OIDs per domain: the domain type itself, then its
	// auto-generated array type (`_name`). Mirror that ordering (OID, then
	// ArrayOID) so a `d[]` column joins to a real array pg_type row. DU-002
	// slice 251 (matches RegisterEnum / RegisterCompositeType).
	d := &Domain{
		Name:     k,
		OID:      c.nextOID,
		ArrayOID: c.nextOID + 1,
		Base:     base,
		NotNull:  notNull,
	}
	c.nextOID += 2
	c.domains[k] = d
	return d, nil
}

// RegisterDomainDuringRecovery re-registers a domain (including every CHECK
// constraint) reconstructed from a RecordKindCreateDomain WAL record,
// preserving its original OID/ArrayOID/CHECK OIDs and advancing nextOID past
// them so subsequent allocations don't collide. Mirrors
// RegisterRangeTypeDuringRecovery. M0122-0005 restart-persistence follow-up.
func (c *InMemory) RegisterDomainDuringRecovery(d *Domain) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.domains == nil {
		c.domains = make(map[string]*Domain)
	}
	out := *d
	c.domains[strings.ToLower(d.Name)] = &out
	c.advanceNextOIDLocked(d.OID)
	c.advanceNextOIDLocked(d.ArrayOID)
	for _, chk := range d.Checks {
		c.advanceNextOIDLocked(chk.OID)
	}
}

// DropDomainDuringRecovery removes a domain reconstructed during WAL replay,
// mirroring DropRangeTypeDuringRecovery. Unlike the live DropDomain path, it
// does not scan for or cascade to dependent tables — a WAL replay drop
// record is only ever emitted after the live DROP DOMAIN already succeeded
// (and any CASCADE-dropped tables have their own drop records), so re-running
// that dependency check here is unnecessary.
func (c *InMemory) DropDomainDuringRecovery(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.domains, strings.ToLower(name))
}

// AddDomainCheck appends a CHECK constraint to a domain and allocates its
// pg_constraint OID. expr is the conbin source text (the deparsed predicate);
// inValues is non-nil for the `CHECK (VALUE IN (...))` form. No-op when expr is
// empty. An unnamed check (name == "") gets PG's generated `<domain>_check`,
// with `<domain>_check1`, `_check2`, … on collision with an already-added check
// (PG's ChooseConstraintName disambiguation). The OID is drawn from the same
// running counter as every other user object so it stays stable and distinct.
// DU-002 slice 96 (single) / slice 385 (multi-CHECK).
func (c *InMemory) AddDomainCheck(d *Domain, name, expr string, inValues []string) {
	if d == nil || expr == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addDomainCheckLocked(d, name, expr, inValues)
}

// addDomainCheckLocked is the shared body of AddDomainCheck/AddDomainConstraint.
// Caller holds c.mu.
func (c *InMemory) addDomainCheckLocked(d *Domain, name, expr string, inValues []string) {
	if name == "" {
		base := d.Name + "_check"
		name = base
		for n := 1; c.domainCheckNameTaken(d, name); n++ {
			name = fmt.Sprintf("%s%d", base, n)
		}
	}
	d.Checks = append(d.Checks, DomainCheck{
		Name:     name,
		Expr:     expr,
		OID:      c.nextOID,
		InValues: inValues,
	})
	c.nextOID++
}

// AddDomainConstraint adds a named CHECK constraint to an already-registered
// domain (`ALTER DOMAIN name ADD [CONSTRAINT name] CHECK (expr)`), mirroring
// AlterDomainAddConstraint's domainAddCheckConstraint call: an explicit name
// colliding with an existing CHECK on the same domain is a duplicate-object
// error, matching real PG's `constraint "%s" for domain "%s" already exists`
// (typecmds.c); an unnamed constraint gets the same `<domain>_check`/
// `_check1`/... auto-naming AddDomainCheck already does. Existing-column-data
// validation (real PG's `!skip_validation` scan of every table column typed
// with this domain) is not performed — see deferral ledger. M0122-0005 domain
// follow-up (ADD CONSTRAINT).
func (c *InMemory) AddDomainConstraint(domainName, name, expr string, inValues []string) error {
	k := strings.ToLower(domainName)
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.domains[k]
	if !ok {
		return fmt.Errorf("type %q does not exist", domainName)
	}
	if name != "" && c.domainCheckNameTaken(d, name) {
		return fmt.Errorf("constraint %q for domain %q already exists", name, d.Name)
	}
	c.addDomainCheckLocked(d, name, expr, inValues)
	return nil
}

// DropDomainConstraint removes a named CHECK constraint from an existing
// domain (`ALTER DOMAIN name DROP CONSTRAINT [IF EXISTS] name [RESTRICT |
// CASCADE]`), mirroring AlterDomainDropConstraint. ifExists suppresses the
// "constraint does not exist" error into a silent no-op, matching DropDomain's
// own ifExists convention (goopg's catalog layer has no per-command NOTICE
// channel to emit real PG's "skipping" notice through instead). M0122-0005
// domain follow-up (DROP CONSTRAINT).
func (c *InMemory) DropDomainConstraint(domainName, constrName string, ifExists bool) error {
	k := strings.ToLower(domainName)
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.domains[k]
	if !ok {
		return fmt.Errorf("type %q does not exist", domainName)
	}
	for i, ck := range d.Checks {
		if ck.Name == constrName {
			d.Checks = append(d.Checks[:i], d.Checks[i+1:]...)
			return nil
		}
	}
	if ifExists {
		return nil
	}
	return fmt.Errorf("constraint %q of domain %q does not exist", constrName, d.Name)
}

// domainCheckNameTaken reports whether the domain already has a CHECK with the
// given constraint name. Caller holds c.mu. DU-002 slice 385.
func (c *InMemory) domainCheckNameTaken(d *Domain, name string) bool {
	for _, ck := range d.Checks {
		if ck.Name == name {
			return true
		}
	}
	return false
}

// LookupDomain finds a domain by name (case-insensitive). M0097-0017.
func (c *InMemory) LookupDomain(name string) (*Domain, bool) {
	k := strings.ToLower(name)
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.domains[k]
	return d, ok
}

// AllDomains returns a snapshot slice of every registered domain. Used by
// pg_get_constraintdef to find a domain CHECK constraint by its OID. DU-002 slice 96.
func (c *InMemory) AllDomains() []*Domain {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Domain, 0, len(c.domains))
	for _, d := range c.domains {
		out = append(out, d)
	}
	return out
}

// LookupDomainByOID finds a domain type by its pg_type OID. Used by format_type
// to render a domain column's declared type as its schema-qualified domain name
// (not the base type). DU-002 slice 90.
func (c *InMemory) LookupDomainByOID(oid uint32) (*Domain, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, d := range c.domains {
		if d.OID == oid {
			return d, true
		}
	}
	return nil, false
}

// LookupDomainByArrayOID finds a user-defined domain type by the pg_type OID of
// its auto-generated array type (`_name`). Used by format_type to render a
// domain-array column (`d[]`) as the schema-qualified array name. Mirrors
// LookupEnumByArrayOID / LookupCompositeTypeByArrayOID. DU-002 slice 251.
func (c *InMemory) LookupDomainByArrayOID(oid uint32) (*Domain, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, d := range c.domains {
		if d.ArrayOID == oid {
			return d, true
		}
	}
	return nil, false
}

// RenameDomain renames a domain from oldName to newName, mirroring
// RenameRangeType/RenameCompositeType/RenameEnum. M0122-0005 (domain
// follow-up).
func (c *InMemory) RenameDomain(oldName, newName string) error {
	ok := strings.ToLower(oldName)
	nk := strings.ToLower(newName)
	c.mu.Lock()
	defer c.mu.Unlock()
	d, found := c.domains[ok]
	if !found {
		return fmt.Errorf("type %q does not exist", oldName)
	}
	if _, exists := c.domains[nk]; exists {
		return fmt.Errorf("type %q already exists", newName)
	}
	delete(c.domains, ok)
	d.Name = nk
	c.domains[nk] = d
	return nil
}

// RenameDomainConstraint renames one of a domain's CHECK constraints
// (`ALTER DOMAIN name RENAME CONSTRAINT old TO new`), mirroring real PG's
// rename_constraint_internal's domain branch (get_domain_constraint_oid +
// RenameConstraintById): "constraint %q for domain %s does not exist" when
// oldName isn't found (also covers an unknown domain), "constraint %q for
// domain %s already exists" when newName collides with another CHECK already
// on the same domain. M0122-0005 domain follow-up (RENAME CONSTRAINT).
func (c *InMemory) RenameDomainConstraint(domainName, oldName, newName string) error {
	k := strings.ToLower(domainName)
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.domains[k]
	if !ok {
		return fmt.Errorf("constraint %q for domain %s does not exist", oldName, domainName)
	}
	idx := -1
	for i, ck := range d.Checks {
		if ck.Name == oldName {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("constraint %q for domain %s does not exist", oldName, d.Name)
	}
	if oldName != newName {
		for _, ck := range d.Checks {
			if ck.Name == newName {
				return fmt.Errorf("constraint %q for domain %s already exists", newName, d.Name)
			}
		}
	}
	d.Checks[idx].Name = newName
	return nil
}

// SetDomainOwner records the typowner role OID for an existing domain.
// Returns false if no such domain is registered. Mirrors
// SetRangeTypeOwner/SetCompositeTypeOwner/SetEnumOwner. M0122-0005 (domain
// follow-up).
func (c *InMemory) SetDomainOwner(name string, ownerOID uint32) bool {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.domains[k]
	if !ok {
		return false
	}
	d.Owner = ownerOID
	return true
}

// SetDomainDefault sets or clears an existing domain's DEFAULT expression
// (`ALTER DOMAIN name SET DEFAULT expr` / `ALTER DOMAIN name DROP DEFAULT`,
// the latter passing a nil expr), mirroring AlterDomainDefault. Returns false
// if no such domain is registered. M0122-0005 domain follow-up (SET/DROP
// DEFAULT).
func (c *InMemory) SetDomainDefault(name string, expr parser.Expr) bool {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.domains[k]
	if !ok {
		return false
	}
	d.Default = expr
	return true
}

// SetDomainNotNull sets or clears a domain's NOT NULL flag (`ALTER DOMAIN
// name SET NOT NULL` / `DROP NOT NULL`), mirroring AlterDomainNotNull.
// Returns false if no such domain is registered. Unlike real PG's
// AlterDomainNotNull, SET NOT NULL does not scan existing table columns of
// this domain type for NULL values already present (validateDomainNotNull
// Constraint's cross-table walk) — the same simplification goopg's
// ALTER TABLE ... SET NOT NULL already makes for plain columns. M0122-0005
// domain follow-up (SET/DROP NOT NULL).
func (c *InMemory) SetDomainNotNull(name string, notNull bool) bool {
	k := strings.ToLower(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.domains[k]
	if !ok {
		return false
	}
	d.NotNull = notNull
	return true
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

// IsSerialTypeName reports whether a column type name is one of the SERIAL
// pseudo-types. SERIAL columns store a plain integer with an implicit
// nextval()-default and an owned sequence; pg_dump dumps that default + sequence
// separately. DU-002 slice 121.
func IsSerialTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "serial", "serial4", "bigserial", "serial8", "smallserial", "serial2":
		return true
	default:
		return false
	}
}

// attrDefRow is one synthesized pg_attrdef entry. The same deterministic row set
// feeds the pg_attrdef virtual table and the pg_depend NORMAL link from a SERIAL
// column's default to its owned sequence, so both must agree on the synthetic
// `oid`. seqOID is non-zero only for a SERIAL column (the sequence the nextval()
// default references). DU-002 slice 121.
type attrDefRow struct {
	oid      uint32
	tableOID uint32
	attnum   int // 1-based
	adbin    string
	seqOID   uint32
}

// attrDefRowsLocked builds the deterministic pg_attrdef row set shared by the
// pg_attrdef virtual table and dependVirtualRows. The caller MUST hold c.mu (R).
// Tables are walked in sorted-key order so the synthetic oids are stable across
// calls — pg_dump matches the pg_attrdef.oid it scanned against the pg_depend
// objid, so the two producers cannot disagree. DU-002 slice 121.
func (c *InMemory) attrDefRowsLocked() []attrDefRow {
	// Index sequence relations by their bare (lowercased) name so a SERIAL
	// column's default can resolve the owned sequence's pg_class OID.
	seqOIDByName := make(map[string]uint32)
	for _, t := range c.tables {
		if t.IsSequence {
			seqOIDByName[strings.ToLower(t.Name)] = t.OID
		}
	}
	keys := make([]string, 0, len(c.tables))
	for k, t := range c.tables {
		if t.Virtual {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows []attrDefRow
	var oid uint32 = 1
	for _, k := range keys {
		tbl := c.tables[k]
		for _, col := range tbl.Columns {
			var adbin string
			var seqOID uint32
			switch {
			case IsSerialTypeName(col.Type.Name):
				// SERIAL default = nextval('<schema>.<table>_<col>_seq'::regclass).
				// pg_dump runs with search_path='' so the regclass is fully
				// schema-qualified; goopg's pg_get_expr is a pass-through, so adbin
				// must already carry that qualified form.
				sch := strings.ToLower(tbl.Schema)
				if sch == "" {
					sch = "public"
				}
				seqName := strings.ToLower(tbl.Name) + "_" + strings.ToLower(col.Name) + "_seq"
				adbin = fmt.Sprintf("nextval('%s.%s'::regclass)", sch, seqName)
				seqOID = seqOIDByName[seqName]
			case col.DefaultExpr != nil:
				adbin = formatExprForAttrdef(col.DefaultExpr)
			case col.GeneratedExpr != "":
				adbin = col.GeneratedExpr
			default:
				continue
			}
			rows = append(rows, attrDefRow{
				oid:      oid,
				tableOID: tbl.OID,
				attnum:   col.Ordinal + 1,
				adbin:    adbin,
				seqOID:   seqOID,
			})
			oid++
		}
	}
	return rows
}

// binaryOpSymbol maps a parser.BinaryOp operator to the SQL operator text PG's
// pg_get_expr emits between the operands. Returns "" for an operator the
// renderer does not model (the caller then falls through). Shared by
// formatExprForAttrdef's BinaryOp parenthesization (DU-002 slice 297).
func binaryOpSymbol(op parser.OpCode) string {
	switch op {
	case parser.OpAdd:
		return "+"
	case parser.OpSub:
		return "-"
	case parser.OpMul:
		return "*"
	case parser.OpDiv:
		return "/"
	case parser.OpMod:
		return "%"
	case parser.OpConcat:
		return "||"
	case parser.OpEq:
		return "="
	case parser.OpLt:
		return "<"
	case parser.OpGt:
		return ">"
	case parser.OpLe:
		return "<="
	case parser.OpGe:
		return ">="
	case parser.OpNe:
		return "<>"
	case parser.OpAnd:
		return "AND"
	case parser.OpOr:
		return "OR"
	case parser.OpLike:
		return "LIKE"
	case parser.OpNotLike:
		return "NOT LIKE"
	}
	return ""
}

// formatExprForAttrdef converts a parsed default expression to a display string
// for pg_attrdef.adbin. Used by pg_get_expr to display column defaults in \d.
// FormatExprForAttrdef deparses a column DEFAULT / CHECK / generated-column
// expression to SQL text, the same rendering pg_attrdef's adbin display uses.
// Exported for the column-defaults WAL persistence (RecordKindColumnDefaults):
// syncTableToCatalogHeap serializes each DefaultExpr with it and startup
// replay round-trips the text through parser.ParseExpr. root-0020 follow-up.
func FormatExprForAttrdef(e parser.Expr) string { return formatExprForAttrdef(e) }

func formatExprForAttrdef(e parser.Expr) string {
	if e == nil {
		return ""
	}
	switch v := e.(type) {
	case *parser.ColumnRef:
		// A bare column reference. PG's pg_get_expr deparses a column ref inside a
		// single-relation context (CHECK predicate, policy USING/WITH CHECK,
		// generated-column expression) as just the unqualified column name. A
		// table/schema qualifier is dropped because the expression is already
		// scoped to one relation. DU-002 slice 323. (Previously absent: a column
		// ref fell through to fmt.Sprintf("%v"), emitting a Go pointer string.)
		return v.Column
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
	case *parser.FuncCall:
		// SQL niladic value functions written without parens (`DEFAULT
		// CURRENT_TIMESTAMP`, `DEFAULT CURRENT_DATE`, `DEFAULT CURRENT_USER`,
		// …) parse to a parenless *FuncCall (parser.IsNoParenFuncName). PG
		// stores these as a SQLValueFunction node, and pg_get_expr deparses
		// them as the bare UPPERCASE keyword — `CURRENT_TIMESTAMP`, never
		// `current_timestamp()`. Rendering them like an ordinary call (the
		// generic branch below) added spurious parens that break the DEFAULT
		// clause pg_dump re-emits — `current_timestamp()` is not even valid
		// SQL on restore. Verified against PG 18.3. DU-002 slice 174.
		if len(v.Args) == 0 && v.Name.Schema == "" && parser.IsNoParenFuncName(strings.ToLower(v.Name.Name)) {
			return strings.ToUpper(v.Name.Name)
		}
		// Function-call defaults (`DEFAULT now()`, `DEFAULT gen_random_uuid()`)
		// are accepted by validateDefaultExpr, so the parsed *FuncCall reaches
		// pg_attrdef.adbin. Without this case it fell through to fmt.Sprintf("%v")
		// — a Go pointer string — corrupting the DEFAULT clause pg_dump re-emits
		// (DU-002 slice 173). Mirror executor.defaultExprToSQL so the dump path
		// (catalog) and the proargdefaults path (executor) agree: render
		// `[schema.]name(arg, …)` with each argument recursively rendered. pg_dump
		// runs with search_path='' so a schema-qualified call survives restore.
		args := make([]string, 0, len(v.Args))
		for _, a := range v.Args {
			args = append(args, formatExprForAttrdef(a))
		}
		name := v.Name.Name
		if v.Name.Schema != "" {
			name = v.Name.Schema + "." + name
		}
		return name + "(" + strings.Join(args, ", ") + ")"
	case *parser.CastExpr:
		// `DEFAULT '{}'::jsonb`, `DEFAULT 0::numeric`. validateDefaultExpr accepts a
		// CastExpr (recursing into its operand), so the parsed node reaches
		// pg_attrdef.adbin and pg_dump re-emits it via pg_get_expr (goopg pass-through).
		// Render `operand::type` mirroring executor.defaultExprToSQL (the proargdefaults
		// twin); keep the two in sync. Typmods are dropped — same as the executor twin —
		// because validateDefaultExpr never inspects them and PG's coercion re-applies
		// the column typmod on restore. DU-002 slice 176.
		return formatExprForAttrdef(v.Operand) + "::" + v.Type.Name
	case *parser.UnaryOp:
		// `DEFAULT -1` parses to UnaryOp(OpUnaryNeg, IntegerConst) — the parser
		// tags unary minus with OpUnaryNeg, NOT OpSub (OpSub is binary `a - b`).
		// The previous `case parser.OpSub` arm never matched a real unary minus, so
		// a `DEFAULT -…` fell through to fmt.Sprintf("%v") and corrupted the dump
		// with a Go pointer string (DU-002 slice 302). PG's parser folds a unary
		// minus applied DIRECTLY to a numeric literal into a negative typed Const
		// (gram.y doNegate), deparsed by get_const_expr as the quoted-value-plus-cast
		// `'-N'::type` so it re-parses as one constant. parser.NegatedLiteralSQL
		// reproduces that exact form (and PG's literal-type resolution); it returns
		// "" for a non-literal operand. For a unary minus on a COMPOUND operand (an
		// OpExpr PG does NOT fold), get_rule_expr deparses `(- (operand))`. Mirror the
		// executor twin executor.defaultExprToSQL. DU-002 slice 364 (resolves the
		// slice 302/360(a)/362(b)/363 deferred `'-N'::type` gap).
		switch v.Op {
		case parser.OpUnaryNeg:
			if neg := parser.NegatedLiteralSQL(v.Operand); neg != "" {
				return neg
			}
			return "(- " + formatExprForAttrdef(v.Operand) + ")"
		case parser.OpNot:
			return "NOT " + formatExprForAttrdef(v.Operand)
		}
	case *parser.BinaryOp:
		// `DEFAULT (1 + 2) * 3`, `DEFAULT 'a' || 'b'`. PG's pg_get_expr (with
		// prettyFlags=0, the mode pg_dump uses for pg_attrdef.adbin) FULLY
		// parenthesizes every binary OpExpr/BoolExpr node — `(1 + 2) * 3`
		// deparses to `((1 + 2) * 3)`, NOT the precedence-minimized `(1 + 2) * 3`.
		// Empirically verified vs real PG 18.3: a bare `1 + 1` dumps as `(1 + 1)`
		// and a nested `(1 + 2) * 3` as `((1 + 2) * 3)`. Before this slice the
		// renderer emitted the operator without parens, so a nested-arithmetic
		// default round-tripped as `1 + 2 * 3` — a SILENT precedence change that
		// evaluates to a different value on restore (DU-002 slice 297). Wrap each
		// node `(left op right)`; the recursion naturally parenthesizes operands.
		// Mirror the executor twin's operator set. NOTE: the executor twin
		// (executor.defaultExprToSQL, reused for index predicates / expression-index
		// columns / function-arg defaults / partition keys) is NOT yet wrapped — that
		// is a separate blast radius deferred to a follow-up slice; this slice fixes
		// only the isolated pg_attrdef column-default path.
		left := formatExprForAttrdef(v.Left)
		right := formatExprForAttrdef(v.Right)
		if op := binaryOpSymbol(v.Op); op != "" {
			return "(" + left + " " + op + " " + right + ")"
		}
	case *parser.TypedStringLit:
		// e.g. `DEFAULT DATE '2020-01-01'`. Mirror the executor twin.
		return v.Type + " '" + strings.ReplaceAll(v.Value, "'", "''") + "'"
	case *parser.ArrayConstructorExpr:
		// `DEFAULT ARRAY[1, 2, 3]` on an array column. validateDefaultExpr rejects
		// only column refs / subqueries / aggregate-or-SRF calls and accepts every
		// other node, so the parsed *ArrayConstructorExpr reaches pg_attrdef.adbin
		// (atthasdef=true). Without this case it fell through to fmt.Sprintf("%v") —
		// a Go pointer string — corrupting the DEFAULT clause pg_dump re-emits
		// (DU-002 slice 177). Render `ARRAY[e1, e2, …]` with each element recursively
		// rendered, matching PG's pg_get_expr deparse; mirror executor.defaultExprToSQL
		// (the proargdefaults twin) so the dump path and the runtime path agree.
		elems := make([]string, 0, len(v.Elements))
		for _, el := range v.Elements {
			elems = append(elems, formatExprForAttrdef(el))
		}
		return "ARRAY[" + strings.Join(elems, ", ") + "]"
	case *parser.CaseExpr:
		// `DEFAULT CASE WHEN true THEN 1 ELSE 0 END` (searched form) and
		// `DEFAULT CASE 1 WHEN 1 THEN 'x' ELSE 'y' END` (simple form).
		// validateDefaultExpr rejects only column refs / subqueries /
		// aggregate-or-SRF calls and accepts every other node, so the parsed
		// *CaseExpr reaches pg_attrdef.adbin (atthasdef=true). Without this case
		// it fell through to fmt.Sprintf("%v") — a Go pointer string — corrupting
		// the DEFAULT clause pg_dump re-emits (DU-002 slice 178). PG's pg_get_expr
		// pretty-prints CASE across multiple lines; a single-line render is valid,
		// re-parseable SQL that round-trips to the same node. Mirror
		// executor.defaultExprToSQL (the proargdefaults twin) so the dump path and
		// the runtime path agree.
		var b strings.Builder
		b.WriteString("CASE")
		if v.Operand != nil {
			b.WriteString(" ")
			b.WriteString(formatExprForAttrdef(v.Operand))
		}
		for _, w := range v.Whens {
			b.WriteString(" WHEN ")
			b.WriteString(formatExprForAttrdef(w.When))
			b.WriteString(" THEN ")
			b.WriteString(formatExprForAttrdef(w.Then))
		}
		if v.Else != nil {
			b.WriteString(" ELSE ")
			b.WriteString(formatExprForAttrdef(v.Else))
		}
		b.WriteString(" END")
		return b.String()
	case *parser.RowExpr:
		// `DEFAULT (1, 2)` parses to a *RowExpr — the parenthesised row-constructor
		// shorthand (`(a, b)`; the explicit `ROW(a, b)` form parses to a FuncCall named
		// "row" instead, handled above). validateDefaultExpr rejects only column refs /
		// subqueries / aggregate-or-SRF calls and accepts every other node, so the parsed
		// *RowExpr reaches pg_attrdef.adbin (atthasdef=true). Without this case it fell
		// through to fmt.Sprintf("%v") — a Go pointer string — corrupting the DEFAULT
		// clause pg_dump re-emits (DU-002 slice 179). PG's ruleutils always prints the ROW
		// keyword for a RowExpr (get_rule_expr T_RowExpr: "SQL99 allows ROW to be omitted …
		// but for simplicity we always print it"), so render `ROW(e1, e2, …)` with each
		// element rendered recursively — matches PG's pg_get_expr deparse and re-parses to
		// an equivalent node. Mirror executor.defaultExprToSQL (the proargdefaults twin) so
		// the dump path and the runtime path agree.
		elems := make([]string, 0, len(v.Elems))
		for _, el := range v.Elems {
			elems = append(elems, formatExprForAttrdef(el))
		}
		return "ROW(" + strings.Join(elems, ", ") + ")"
	case *parser.IntervalLit:
		// `DEFAULT INTERVAL '1' day` on an interval column. validateDefaultExpr
		// rejects only column refs / subqueries / aggregate-or-SRF calls and
		// accepts every other node, so the parsed *IntervalLit reaches
		// pg_attrdef.adbin (atthasdef=true). Without this case it fell through to
		// fmt.Sprintf("%v", e) — a Go pointer string — corrupting the DEFAULT
		// clause pg_dump re-emits (DU-002 slice 180). PG const-folds the literal
		// and pg_get_expr deparses it as `'<n> <unit>'::interval`; goopg has no
		// interval output function, so it re-emits the equivalent native
		// `INTERVAL '<n>' <unit>` literal form its own parser produces (the
		// `interval '<N>' <unit>` shape) — valid, re-parseable SQL that
		// round-trips to the same node. Mirror executor.defaultExprToSQL (the
		// proargdefaults twin) so the dump path and the runtime path render
		// identically.
		return "INTERVAL '" + strings.ReplaceAll(v.Value, "'", "''") + "' " + v.Unit
	case *parser.IsNullExpr:
		// `DEFAULT (1 IS NULL)` on a boolean column. validateDefaultExpr rejects
		// only column refs / subqueries / aggregate-or-SRF calls and accepts every
		// other node, so the parsed *IsNullExpr reaches pg_attrdef.adbin
		// (atthasdef=true). Without this case it fell through to fmt.Sprintf("%v", e)
		// — a Go pointer string — corrupting the DEFAULT clause pg_dump re-emits
		// (DU-002 slice 181). Render `<operand> IS [NOT] NULL`, matching PG's
		// pg_get_expr deparse of a NullTest; mirror executor.defaultExprToSQL (the
		// proargdefaults twin) so the dump path and the runtime path agree.
		if v.Negated {
			return formatExprForAttrdef(v.Operand) + " IS NOT NULL"
		}
		return formatExprForAttrdef(v.Operand) + " IS NULL"
	case *parser.IsBoolExpr:
		// `DEFAULT (true IS NOT TRUE)`. Render `<operand> IS [NOT] TRUE|FALSE|UNKNOWN`,
		// matching PG's pg_get_expr deparse of a BooleanTest; mirror the executor twin
		// (DU-002 slice 181).
		target := "UNKNOWN"
		if v.TestTrue {
			target = "TRUE"
		} else if v.TestFalse {
			target = "FALSE"
		}
		op := " IS "
		if v.Negated {
			op = " IS NOT "
		}
		return formatExprForAttrdef(v.Operand) + op + target
	case *parser.IsDistinctFromExpr:
		// `DEFAULT (1 IS DISTINCT FROM 2)`. Render `<left> IS [NOT] DISTINCT FROM
		// <right>`, matching PG's pg_get_expr deparse of a DistinctExpr; mirror the
		// executor twin (DU-002 slice 181).
		op := " IS DISTINCT FROM "
		if v.Negated {
			op = " IS NOT DISTINCT FROM "
		}
		return formatExprForAttrdef(v.Left) + op + formatExprForAttrdef(v.Right)
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
	// TempOwnerToken is the querying session's temp-relation ownership token
	// ("s<id>", see executor.sessionTempOwner / config.SessionRegistry.UniqueID).
	// Empty for session-less contexts. The planner reads it via CurrentTempOwner
	// to drop other-session temp inheritance children during expansion
	// (RELATION_IS_OTHER_TEMP). Design 0118-0036 (M0118-0008 inherit-temp).
	TempOwnerToken string
	// SnapshotPartitionDetachEpoch is the querying statement's snapshot
	// partition-detach epoch (mvcc.Snapshot.PartitionDetachEpoch). The planner
	// reads it via CurrentPartitionDetachEpoch to drop partition children that
	// became detach-pending at or before this snapshot (VisiblePartitionChildren).
	// Zero for snapshot-less contexts (no filtering). Design 0118-0059
	// (M0118-0008 detach-partition-concurrently).
	SnapshotPartitionDetachEpoch uint64
	// DisableSeqScan mirrors the querying session's `enable_seqscan = off` GUC.
	// goopg's planner is otherwise rule-based and ignores the planner toggle
	// GUCs, but the planner reads this one (via SeqScanDisabled) to promote an
	// ordered full-index scan that covers the projection to an IndexOnlyScan —
	// eliminating the Sort the way PG does once a SeqScan is disabled. Bounded:
	// false in the default (toggle-untouched) case so legacy plans are unchanged.
	// Design 0118-0103 (M0118-0009 horizons enabler).
	DisableSeqScan bool
}

// WithSearchPath returns a SearchPathCatalog that falls back to the schemas
// returned by getSchemas (in order) when LookupTable finds no match for an
// unqualified name.
func WithSearchPath(cat Catalog, getSchemas func() []string) *SearchPathCatalog {
	return &SearchPathCatalog{Catalog: cat, GetSchemas: getSchemas}
}

// CurrentTempOwner returns the querying session's temp-relation ownership token
// so inheritance-expansion sites can apply AccessibleInheritanceChildren. The
// planner discovers it via a tempOwnerCarrier interface walk over the catalog
// wrapper chain. Design 0118-0036.
func (c *SearchPathCatalog) CurrentTempOwner() string { return c.TempOwnerToken }

// SeqScanDisabled reports the querying session's `enable_seqscan = off` GUC so
// the planner can promote an ordered covering index scan to an IndexOnlyScan
// (dropping the Sort). The planner discovers it via a seqScanToggleCarrier
// interface walk over the catalog wrapper chain. Design 0118-0103 (horizons).
func (c *SearchPathCatalog) SeqScanDisabled() bool { return c.DisableSeqScan }

// CurrentPartitionDetachEpoch returns the querying statement's snapshot
// partition-detach epoch so the planner's partition-expansion site can apply
// VisiblePartitionChildren. The planner discovers it via a
// partitionDetachEpochCarrier interface walk over the catalog wrapper chain.
// Design 0118-0059 (M0118-0008 detach-partition-concurrently).
func (c *SearchPathCatalog) CurrentPartitionDetachEpoch() uint64 {
	return c.SnapshotPartitionDetachEpoch
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
