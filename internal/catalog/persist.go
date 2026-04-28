package catalog

import (
	"fmt"
	"sort"

	"github.com/goopg/goopg/internal/parser"
)

// Snapshot is the on-disk-friendly view of an InMemory catalog. It's
// the seam between the in-memory schema map and the JSON encoder
// the server bootstrap writes under <DataDir>/global/pg_catalog.json.
//
// Field order is stable across calls (sorted by OID) so two
// Snapshot()s of the same catalog produce byte-identical encodings.
//
// v0 stores the snapshot as JSON rather than upstream's binary
// pg_class / pg_attribute layout because the executor doesn't yet
// read those system tables — when it does, the encoder migrates and
// CatalogVersion bumps. See docs/design/0017-data-directory.md for
// the migration gate.
type Snapshot struct {
	NextOID uint32       `json:"next_oid"`
	// NextXID is the mvcc.Manager.nextXID at save time. Restored on
	// the next startup so persisted heap tuples (whose xmin/xmax
	// come from previous sessions) are visible to the new session's
	// snapshots — without this restore, every new session starts at
	// xid=3 and treats the persisted xids as in-progress/future,
	// hiding all the durable data.
	NextXID uint32       `json:"next_xid,omitempty"`
	Tables  []TableEntry `json:"tables,omitempty"`
	Indexes []IndexEntry `json:"indexes,omitempty"`
}

// TableEntry mirrors Table for serialization.
type TableEntry struct {
	OID     uint32   `json:"oid"`
	Schema  string   `json:"schema,omitempty"`
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}

// IndexEntry mirrors Index for serialization. TableOID points at
// the owning Table; Restore wires up the *Table pointer.
type IndexEntry struct {
	OID      uint32   `json:"oid"`
	Schema   string   `json:"schema,omitempty"`
	Name     string   `json:"name"`
	TableOID uint32   `json:"table_oid"`
	Columns  []string `json:"columns"`
	Unique   bool     `json:"unique,omitempty"`
	Method   string   `json:"method,omitempty"`
	Primary  bool     `json:"primary,omitempty"`
}

// Snapshot returns a deep, deterministically-ordered copy of the
// catalog state. Safe to call concurrently with reads; concurrent
// writers may produce a snapshot that includes some-but-not-all of
// their changes (the catalog mutex is held only during the copy).
func (c *InMemory) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := Snapshot{NextOID: c.nextOID}
	for _, t := range c.tables {
		if t.Virtual {
			// Virtual catalog views are re-registered on every
			// startup; persisting them would just churn the JSON
			// snapshot without preserving anything useful.
			continue
		}
		s.Tables = append(s.Tables, TableEntry{
			OID:     t.OID,
			Schema:  t.Schema,
			Name:    t.Name,
			Columns: append([]Column(nil), t.Columns...),
		})
	}
	for _, idx := range c.indexes {
		s.Indexes = append(s.Indexes, IndexEntry{
			OID:      idx.OID,
			Schema:   idx.Schema,
			Name:     idx.Name,
			TableOID: idx.Table.OID,
			Columns:  append([]string(nil), idx.Columns...),
			Unique:   idx.Unique,
			Method:   idx.Method,
			Primary:  idx.Primary,
		})
	}
	sort.Slice(s.Tables, func(i, j int) bool { return s.Tables[i].OID < s.Tables[j].OID })
	sort.Slice(s.Indexes, func(i, j int) bool { return s.Indexes[i].OID < s.Indexes[j].OID })
	return s
}

// Restore replaces the catalog's contents with the given snapshot.
// On error the catalog is left empty rather than half-populated;
// callers that care about atomicity should check the error and
// either retry or abort startup.
func (c *InMemory) Restore(s Snapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tables = make(map[string]*Table, len(s.Tables))
	c.indexes = make(map[string]*Index, len(s.Indexes))
	c.byTable = make(map[uint32]map[string]*Index)
	c.nextOID = s.NextOID
	if c.nextOID < FirstUserOID {
		c.nextOID = FirstUserOID
	}

	byOID := make(map[uint32]*Table, len(s.Tables))
	for _, te := range s.Tables {
		cols := make([]Column, len(te.Columns))
		for i, col := range te.Columns {
			col.Ordinal = i
			cols[i] = col
		}
		t := &Table{
			Schema:  te.Schema,
			Name:    te.Name,
			Columns: cols,
			OID:     te.OID,
		}
		k := key(parser.ObjectName{Schema: te.Schema, Name: te.Name})
		if _, dup := c.tables[k]; dup {
			c.tables = map[string]*Table{}
			return fmt.Errorf("catalog.Restore: duplicate table %q", k)
		}
		c.tables[k] = t
		byOID[te.OID] = t
	}
	for _, ie := range s.Indexes {
		t, ok := byOID[ie.TableOID]
		if !ok {
			c.tables = map[string]*Table{}
			c.indexes = map[string]*Index{}
			c.byTable = map[uint32]map[string]*Index{}
			return fmt.Errorf("catalog.Restore: index %q references unknown table OID %d", ie.Name, ie.TableOID)
		}
		idx := &Index{
			Schema:  ie.Schema,
			Name:    ie.Name,
			Table:   t,
			Columns: append([]string(nil), ie.Columns...),
			Unique:  ie.Unique,
			Method:  ie.Method,
			Primary: ie.Primary,
			OID:     ie.OID,
		}
		k := key(parser.ObjectName{Schema: ie.Schema, Name: ie.Name})
		c.indexes[k] = idx
		if c.byTable[t.OID] == nil {
			c.byTable[t.OID] = map[string]*Index{}
		}
		c.byTable[t.OID][k] = idx
	}
	// Re-install the pg_catalog virtual views: they're not part of
	// the persisted snapshot but must be present on every startup.
	c.registerSystemTables()
	return nil
}
