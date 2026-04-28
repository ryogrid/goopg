// Package catalog is goopg's in-memory schema catalog.
//
// Scope and growth path are documented in
// docs/design/0011-planner.md. v0 keeps tables in a Go map; the
// system-catalog persistence (`pg_class`, `pg_attribute`) lands in
// a follow-up alongside the on-disk catalog work.
package catalog

import (
	"fmt"
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
}

// QualifiedName renders the table's name in the canonical
// `schema.name` form when schema-qualified, otherwise just `name`.
func (t *Table) QualifiedName() string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// Catalog is the lookup interface the planner uses.
type Catalog interface {
	LookupTable(name parser.ObjectName) (*Table, bool)
	LookupColumn(table *Table, name string) (*Column, bool)
	CreateTable(name parser.ObjectName, cols []Column) (*Table, error)
	DropTable(name parser.ObjectName) error
	RelFileNode(table *Table) storage.RelFileNode
}

// InMemory is the v0 implementation: a sync.RWMutex-guarded map.
//
// OIDs are assigned sequentially starting at FirstUserOID. The DBOid
// field on the produced RelFileNode is fixed at DefaultDBOid for v0
// — the multi-database layer arrives with milestone 7.
type InMemory struct {
	mu      sync.RWMutex
	tables  map[string]*Table
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

// NewInMemory returns an empty catalog.
func NewInMemory() *InMemory {
	return &InMemory{
		tables:  make(map[string]*Table),
		nextOID: FirstUserOID,
		dbOid:   DefaultDBOid,
	}
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

// DropTable removes a table from the catalog. Returns an error when
// the name doesn't resolve.
func (c *InMemory) DropTable(name parser.ObjectName) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(name)
	if _, exists := c.tables[k]; !exists {
		return fmt.Errorf("relation %q does not exist", k)
	}
	delete(c.tables, k)
	return nil
}

// RelFileNode returns the storage manager identity for a table.
func (c *InMemory) RelFileNode(table *Table) storage.RelFileNode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return storage.RelFileNode{DBOid: c.dbOid, RelOid: table.OID, Fork: storage.MainFork}
}
