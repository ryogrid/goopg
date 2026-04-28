// Package catalog is goopg's in-memory schema catalog.
//
// Scope and growth path are documented in
// docs/design/0011-planner.md. v0 keeps tables in a Go map; the
// system-catalog persistence (`pg_class`, `pg_attribute`) lands in
// a follow-up alongside the on-disk catalog work.
package catalog

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
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
	AddColumn(table *Table, col Column) (*Column, error)
	DropTable(name parser.ObjectName) error
	DropIndex(name parser.ObjectName) error
	IndexesOnTable(table *Table) []*Index
	HasPrimaryKey(table *Table) bool
	RelFileNode(table *Table) storage.RelFileNode
	IndexRelFileNode(index *Index) storage.RelFileNode
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
}

// FirstUserOID is the first OID handed out for user-created tables.
// 16384 is upstream's `FirstNormalObjectId` — anything below is
// reserved for system catalogs.
const FirstUserOID uint32 = 16384

// DefaultDBOid is the v0 default database OID. Real multi-database
// support (CREATE DATABASE) lives in milestone 7; until then every
// catalog entry lives in this database.
const DefaultDBOid uint32 = 1

// NewInMemory returns a catalog seeded with the v0 pg_catalog
// virtual views.
func NewInMemory() *InMemory {
	c := &InMemory{
		tables:  make(map[string]*Table),
		indexes: make(map[string]*Index),
		byTable: make(map[uint32]map[string]*Index),
		nextOID: FirstUserOID,
		dbOid:   DefaultDBOid,
	}
	c.registerSystemTables()
	return c
}

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
			out = append(out, []string{
				t.Name,
				t.Name,
				"r",
				"pg_catalog",
			})
		}
		return out
	}
	c.tables["pg_catalog.pg_class"] = pgClass
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
// the name doesn't resolve.
func (c *InMemory) LookupTable(name parser.ObjectName) (*Table, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tables[key(name)]
	return t, ok
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
