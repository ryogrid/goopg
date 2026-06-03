package executor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/analyzer"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/plpgsql"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// pgEpoch is the PostgreSQL epoch: 2000-01-01 00:00:00 UTC.
// Timestamps stored as KindTime datums are converted to microseconds
// relative to this epoch for B-tree key encoding.
var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ddlOp is a one-shot operator that runs a DDL statement against the
// catalog and (when applicable) the storage manager. It produces no
// output rows; the wire-protocol path emits the canonical
// CommandComplete tag for the verb.
type ddlOp struct {
	plan *planner.DDL
	ctx  *Context
	done bool
}

func newDDLOp(p *planner.DDL) *ddlOp { return &ddlOp{plan: p} }

func (o *ddlOp) Schema() planner.Schema { return nil }
func (o *ddlOp) Open(ctx *Context) error {
	if ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: o.plan.Pos(), Message: "DDL requires Catalog in Context"}
	}
	o.ctx = ctx
	return nil
}
func (o *ddlOp) Close() error { return nil }

func (o *ddlOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	switch s := o.plan.Stmt.(type) {
	case *parser.CreateTableStmt:
		return nil, o.execCreateTable(s)
	case *parser.CreateIndexStmt:
		return nil, o.execCreateIndex(s)
	case *parser.CreateViewStmt:
		return nil, o.execCreateView(s)
	case *parser.DropTableStmt:
		return nil, o.execDropTable(s)
	case *parser.DropIndexStmt:
		return nil, o.execDropIndex(s)
	case *parser.DropViewStmt:
		return nil, o.execDropView(s)
	case *parser.TruncateStmt:
		return nil, o.execTruncate(s)
	case *parser.AlterTableStmt:
		return nil, o.execAlterTable(s)
	case *parser.CreatePublicationStmt:
		return nil, o.execCreatePublication(s)
	case *parser.DropPublicationStmt:
		return nil, o.execDropPublication(s)
	case *parser.CreateSubscriptionStmt:
		return nil, o.execCreateSubscription(s)
	case *parser.DropSubscriptionStmt:
		return nil, o.execDropSubscription(s)
	case *parser.CreateFunctionStmt:
		return nil, o.execCreateFunction(s)
	case *parser.AlterFunctionStmt:
		return nil, o.execAlterFunction(s)
	case *parser.DropFunctionStmt:
		return nil, o.execDropFunction(s)
	case *parser.CreateProcedureStmt:
		return nil, o.execCreateProcedure(s)
	case *parser.DropProcedureStmt:
		return nil, o.execDropProcedure(s)
	case *parser.CreateTriggerStmt:
		return nil, o.execCreateTrigger(s)
	case *parser.DropRuleStmt:
		return nil, o.execDropRule(s)
	case *parser.DropTriggerStmt:
		return nil, o.execDropTrigger(s)
	case *parser.DropCompatStmt:
		return nil, o.execDropCompat(s)
	case *parser.CreateSequenceStmt:
		return nil, o.execCreateSequence(s)
	case *parser.AlterSequenceStmt:
		return nil, nil // ALTER SEQUENCE accepted, no-op executor
	case *parser.CreateMatViewStmt:
		return nil, o.execCreateMatView(s)
	case *parser.RefreshMatViewStmt:
		return nil, o.execRefreshMatView(s)
	case *parser.CreateAggregateStmt:
		return nil, o.execCreateAggregate(s)
	case *parser.AlterAggregateRenameStmt:
		return nil, o.execAlterAggregateRename(s)
	case *parser.CreateOpClassStmt:
		return nil, o.execCreateOpClass(s)
	case *parser.CompatNoopStmt:
		return nil, o.execCompatNoop(s)
	case *parser.LockTableStmt:
		return nil, o.execLockTable(s)
	case *parser.DoStmt:
		return nil, o.execDoBlock(s)
	case *parser.CreateTypeStmt:
		return nil, o.execCreateType(s)
	case *parser.AlterTypeStmt:
		return nil, o.execAlterType(s)
	case *parser.DropTypeStmt:
		return nil, o.execDropType(s)
	case *parser.CreateDomainStmt:
		return nil, o.execCreateDomain(s)
	case *parser.DropDomainStmt:
		return nil, o.execDropDomain(s)
	}
	return nil, &ExecError{Code: "0A000", Pos: o.plan.Pos(), Message: fmt.Sprintf("DDL %T not supported in v0 executor", o.plan.Stmt)}
}

// execCreatePublication / execDropPublication / execCreateSubscription
// / execDropSubscription drive the *catalog.PubSub registry attached
// via Context.PubSub. The five virtual catalog views
// (pg_publication, pg_publication_rel, pg_publication_tables,
// pg_subscription, pg_subscription_rel) read the same registry, so
// the SQL surface and the views stay coherent. See
// docs/design/0008-0003-publication-subscription-ddl.md.
func (o *ddlOp) execCreatePublication(s *parser.CreatePublicationStmt) error {
	if o.ctx.PubSub == nil {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "CREATE PUBLICATION requires PubSub registry in Context"}
	}
	opts := catalog.DefaultPublicationOptions()
	opts.AllTables = s.AllTables
	if pub, ok := s.With["publish"]; ok {
		opts.PublishInsert = false
		opts.PublishUpdate = false
		opts.PublishDelete = false
		for _, kind := range splitCommaList(pub) {
			switch kind {
			case "insert":
				opts.PublishInsert = true
			case "update":
				opts.PublishUpdate = true
			case "delete":
				opts.PublishDelete = true
			case "truncate":
				// Out of scope: silently accept.
			default:
				return &ExecError{Code: "22023", Pos: s.Pos(), Message: fmt.Sprintf("unrecognized publish action %q", kind)}
			}
		}
	}
	// Canonicalise each table reference to the qualified form the
	// walsender's publication filter compares against
	// (`wal.RelationDef.Schema + "." + Name`). Upstream PG resolves
	// the OID at DDL time and stores `pg_publication_rel.prrelid`;
	// goopg's PubSub keys by string, so the qualified name produced
	// here must match what `relQualifiedName` (server) returns at
	// decode time. Unqualified names fall back to `public` to mirror
	// PG's default search_path. See 0103-0015.
	tables := make([]string, 0, len(s.Tables))
	for _, t := range s.Tables {
		tbl, ok := o.ctx.Catalog.LookupTable(t)
		if !ok && t.Schema == "" {
			tbl, ok = o.ctx.Catalog.LookupTable(parser.ObjectName{Schema: "public", Name: t.Name})
		}
		if !ok {
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", qualifiedTableName(t))}
		}
		tables = append(tables, tbl.QualifiedName())
	}
	if _, err := o.ctx.PubSub.CreatePublication(s.Name, tables, opts); err != nil {
		return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

func (o *ddlOp) execDropPublication(s *parser.DropPublicationStmt) error {
	if o.ctx.PubSub == nil {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "DROP PUBLICATION requires PubSub registry in Context"}
	}
	if err := o.ctx.PubSub.DropPublication(s.Name); err != nil {
		if s.IfExists {
			return nil
		}
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

func (o *ddlOp) execCreateSubscription(s *parser.CreateSubscriptionStmt) error {
	if o.ctx.PubSub == nil {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "CREATE SUBSCRIPTION requires PubSub registry in Context"}
	}
	enabled := true
	if v, ok := s.With["enabled"]; ok {
		enabled = v == "true" || v == "on" || v == "yes" || v == "1"
	}
	slotName := s.With["slot_name"]
	if _, err := o.ctx.PubSub.CreateSubscription(s.Name, s.Conninfo, s.Publications, slotName, enabled); err != nil {
		return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
	}
	if o.ctx.OnSubscriptionChange != nil {
		o.ctx.OnSubscriptionChange()
	}
	return nil
}

func (o *ddlOp) execDropSubscription(s *parser.DropSubscriptionStmt) error {
	if o.ctx.PubSub == nil {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "DROP SUBSCRIPTION requires PubSub registry in Context"}
	}
	if err := o.ctx.PubSub.DropSubscription(s.Name); err != nil {
		if s.IfExists {
			return nil
		}
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: err.Error()}
	}
	if o.ctx.OnSubscriptionChange != nil {
		o.ctx.OnSubscriptionChange()
	}
	return nil
}

// qualifiedTableName renders an ObjectName as "schema.name" or
// just "name" when the schema is empty. Mirrors the format
// catalog.PubSub stores.
func qualifiedTableName(o parser.ObjectName) string {
	if o.Schema == "" {
		return o.Name
	}
	return o.Schema + "." + o.Name
}

// splitCommaList trims whitespace + lowercases each comma-separated
// value in s. Used by the publish=... option parser.
func splitCommaList(s string) []string {
	var out []string
	start := 0
	add := func(end int) {
		raw := s[start:end]
		// trim ASCII spaces.
		i, j := 0, len(raw)
		for i < j && (raw[i] == ' ' || raw[i] == '\t') {
			i++
		}
		for j > i && (raw[j-1] == ' ' || raw[j-1] == '\t') {
			j--
		}
		if i < j {
			lower := make([]byte, j-i)
			for k := i; k < j; k++ {
				c := raw[k]
				if c >= 'A' && c <= 'Z' {
					c += 'a' - 'A'
				}
				lower[k-i] = c
			}
			out = append(out, string(lower))
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			add(i)
			start = i + 1
		}
	}
	add(len(s))
	return out
}

// execDoBlock executes an anonymous PL/pgSQL block (DO $$ ... $$). M0097-0003.
func (o *ddlOp) execDoBlock(s *parser.DoStmt) error {
	if strings.ToLower(s.Language) != "plpgsql" {
		return &ExecError{Code: "0A000", Pos: s.Pos(),
			Message: fmt.Sprintf("DO block language %q is not supported in v0", s.Language)}
	}
	block, err := plpgsql.Parse(s.Body)
	if err != nil {
		return &ExecError{Code: "P0000", Pos: s.Pos(),
			Message: fmt.Sprintf("invalid PL/pgSQL DO body: %v", err)}
	}
	r := &catalog.Routine{Name: "(anonymous)", Language: s.Language, Body: s.Body}
	frame := newPLpgSQLFrame()
	for _, d := range block.Declarations {
		typ := catalogTypeFromColumnType(d.Type)
		value := NullDatum
		if d.Default != nil {
			value, err = evalPLpgSQLExpr(d.Default, frame, o.ctx)
			if err != nil {
				return err
			}
		}
		if addErr := frame.add(d.Name, typ, value); addErr != nil {
			return &ExecError{Code: "42P13", Pos: s.Pos(), Message: addErr.Error()}
		}
	}
	// Execute statements directly using the parent context so NOTICEs propagate. M0097-0003.
	_, flow, execErr := executePLpgSQLStmtList(block.Statements, r, frame, o.ctx)
	if execErr != nil {
		return execErr
	}
	_ = flow // DO blocks don't return values; any flow is acceptable.
	return nil
}

func (o *ddlOp) execCreateTable(s *parser.CreateTableStmt) error {
	if _, exists := o.ctx.Catalog.LookupTable(s.Name); exists {
		if s.IfNotExists {
			o.ctx.Notices = append(o.ctx.Notices,
				fmt.Sprintf("relation %q already exists, skipping", s.Name.String()))
			return nil
		}
		// TEMP TABLE shadows the permanent table: save the permanent table for
		// restoration when the temp table is later dropped. M0097-0003.
		if s.Temporary {
			permTbl, _ := o.ctx.Catalog.LookupTable(s.Name)
			// Save the permanent table in the context's shadow registry for
			// restoration when the TEMP table is later dropped. M0097-0003.
			if o.ctx.TempTableShadows == nil {
				o.ctx.TempTableShadows = make(map[string]*catalog.Table)
			}
			key := strings.ToLower(s.Name.Name)
			o.ctx.TempTableShadows[key] = permTbl
			// Drop the existing catalog entry (heap data preserved at old OID).
			if err := o.ctx.Catalog.DropTable(s.Name); err != nil {
				return &ExecError{Code: "42P01", Pos: s.Pos(), Message: err.Error()}
			}
		} else {
			return &ExecError{Code: "42P07", Pos: s.Pos(), Message: fmt.Sprintf("relation %q already exists", s.Name.String())}
		}
	}

	// CREATE TABLE child PARTITION OF parent FOR VALUES … (M0096-0007)
	if s.PartitionOf != nil {
		return o.execCreatePartitionChild(s)
	}

	// CREATE TABLE name AS SELECT … (CTAS). M0096-0008.
	// Create a table whose columns are derived from the SELECT result.
	// The data is populated by the SELECT execution.
	if s.SelectSource != nil {
		return o.execCreateTableAs(s)
	}

	// Build the column list, merging inherited columns first (M0096-0009).
	// For `CREATE TABLE child () INHERITS (parent)`, the child table starts
	// with all of the parent's columns, then any additional columns defined
	// in the child's body are appended.
	var cols []catalog.Column
	var inheritParents []*catalog.Table // collect for post-creation registration
	if len(s.Inherits) > 0 {
		for _, parentName := range s.Inherits {
			parent, ok := o.ctx.Catalog.LookupTable(parentName)
			if !ok {
				// Parent might not exist yet or may be a virtual table; skip silently.
				continue
			}
			inheritParents = append(inheritParents, parent)
			// Append parent columns (deep copy to avoid aliasing).
			// Deduplicate: skip columns whose name already exists — multiple
			// inheritance parents may share columns (e.g. emp and student both
			// inherit name/age/location from person, so stud_emp INHERITS (emp,
			// student) should have those columns only once).  M0097-0046.
			for _, pc := range parent.Columns {
				found := false
				for _, ec := range cols {
					if strings.EqualFold(ec.Name, pc.Name) {
						found = true
						break
					}
				}
				if !found {
					c := pc
					cols = append(cols, c)
				}
			}
		}
	}
	// Track which column names came from INHERITS (used below for LIKE merge vs error).
	inheritedColNames := make(map[string]bool, len(cols))
	for _, c := range cols {
		inheritedColNames[strings.ToLower(c.Name)] = true
	}

	// CHECK constraints to inherit from LIKE INCLUDING CONSTRAINTS clauses.
	var likeCheckConstraints []string
	// Append body elements in declaration order: explicit columns and LIKE clauses
	// interleaved by their BodyOrder. M0097-0069.
	if len(s.BodyOrder) > 0 {
		// Build a lookup map for explicit columns by name.
		colByName := make(map[string]parser.ColumnDef, len(s.Columns))
		for _, c := range s.Columns {
			colByName[strings.ToLower(c.Name)] = c
		}
		// Build a lookup for LIKE sources.
		_ = likeCheckConstraints // will be populated below for LIKE INCLUDING CONSTRAINTS
		likeByKey := make(map[string]*catalog.Table)
		for _, likeName := range s.LikeTables {
			src, ok := o.ctx.Catalog.LookupTable(likeName)
			if ok {
				likeByKey["@@LIKE:"+likeName.String()] = src
			}
		}
		addCol := func(c parser.ColumnDef) {
			typeName := strings.ToLower(c.Type.Name)
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				if resolved := im.ResolveColumnType(typeName); resolved != typeName {
					typeName = resolved
				}
			}
			cols = append(cols, catalog.Column{
				Name:            c.Name,
				Type:            catalog.Type{Name: typeName, Args: append([]int64(nil), c.Type.Args...)},
				NotNull:         c.NotNull || c.IdentityColumn,
				GeneratedExpr:   c.GeneratedExpr,
				GeneratedAlways: c.GeneratedAlways,
				DefaultExpr:     c.DefaultExpr,
				IdentityColumn:  c.IdentityColumn,
			})
		}
		for _, item := range s.BodyOrder {
			if strings.HasPrefix(item, "@@LIKE:") {
				// Parse the like key and flags: "@@LIKE:tablename[:+identity][:+generated]"
				// Strip flags from the key to look up the source table.
				likeFlags := item
				baseParts := strings.Split(item, ":")
				baseKey := baseParts[0] + ":" + baseParts[1] // "@@LIKE:tablename"
				includeIdentity := strings.Contains(likeFlags, ":+identity")
				includeGenerated := strings.Contains(likeFlags, ":+generated")
				includeDefaults := strings.Contains(likeFlags, ":+defaults")
				includeConstraints := strings.Contains(likeFlags, ":+constraints")
				src, ok := likeByKey[baseKey]
				if !ok {
					continue
				}
				for _, sc := range src.Columns {
					colNameLower := strings.ToLower(sc.Name)
					found := false
					for _, ec := range cols {
						if strings.EqualFold(ec.Name, sc.Name) {
							found = true
							break
						}
					}
					if found {
						if inheritedColNames[colNameLower] {
							// Column came from INHERITS — emit NOTICE "merging column X with
							// inherited definition" and skip (matches PostgreSQL merge semantics).
							o.ctx.AddNotice(fmt.Sprintf("merging column %q with inherited definition", sc.Name))
						} else {
							// Column already defined by an explicit column or a previous LIKE clause.
							// PostgreSQL raises "column X specified more than once" (42701).
							return &ExecError{
								Code:    "42701",
								Pos:     s.Pos(),
								Message: fmt.Sprintf("column %q specified more than once", sc.Name),
							}
						}
						// Either way, skip (don't add the column again) and continue.
						if includeConstraints {
							for _, chk := range src.CheckConstraints {
								found := false
								for _, existing := range likeCheckConstraints {
									if existing == chk {
										found = true
										break
									}
								}
								if !found {
									likeCheckConstraints = append(likeCheckConstraints, chk)
								}
							}
						}
						continue
					}
					c := sc
					// Clear IdentityColumn unless INCLUDING IDENTITY or INCLUDING ALL was specified.
					if !includeIdentity {
						c.IdentityColumn = false
					}
					// Clear GeneratedAlways/GeneratedExpr unless INCLUDING GENERATED or INCLUDING ALL.
					if !includeGenerated {
						c.GeneratedAlways = false
						c.GeneratedExpr = ""
					}
					// Clear DefaultExpr unless INCLUDING DEFAULTS or INCLUDING ALL was specified.
					if !includeDefaults {
						c.DefaultExpr = nil
					}
					cols = append(cols, c)
					// Copy CHECK constraints from source table when INCLUDING CONSTRAINTS.
					if includeConstraints {
						for _, chk := range src.CheckConstraints {
							// Avoid duplicating constraints already present (e.g. from column-level).
							found := false
							for _, existing := range likeCheckConstraints {
								if existing == chk {
									found = true
									break
								}
							}
							if !found {
								likeCheckConstraints = append(likeCheckConstraints, chk)
							}
						}
					}
				}
			} else {
				c, ok := colByName[strings.ToLower(item)]
				if ok {
					addCol(c)
				}
			}
		}
	} else {
		// Fallback: no BodyOrder (e.g. empty column list or old path).
		for _, c := range s.Columns {
			typeName := strings.ToLower(c.Type.Name)
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				if resolved := im.ResolveColumnType(typeName); resolved != typeName {
					typeName = resolved
				}
			}
			cols = append(cols, catalog.Column{
				Name:            c.Name,
				Type:            catalog.Type{Name: typeName, Args: append([]int64(nil), c.Type.Args...)},
				NotNull:         c.NotNull,
				GeneratedExpr:   c.GeneratedExpr,
				GeneratedAlways: c.GeneratedAlways,
				DefaultExpr:     c.DefaultExpr,
			})
		}
	}
	tbl, err := o.ctx.Catalog.CreateTable(s.Name, cols)
	if err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	// Register inheritance relationships now that the child OID is known.
	if len(inheritParents) > 0 {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			for _, parent := range inheritParents {
				im.RegisterInheritanceChild(parent.OID, tbl.OID)
			}
		}
	}
	// Register FK constraints from inline REFERENCES clauses. M0096-0011.
	for _, c := range s.Columns {
		if c.RefTable.Name != "" {
			fk := catalog.ForeignKey{
				Columns:           []string{c.Name},
				RefTable:          c.RefTable.Name,
				RefColumns:        c.RefColumns,
				OnDelete:          c.OnDelete,
				OnUpdate:          c.OnUpdate,
				Deferrable:        c.FKDeferrable,
				InitiallyDeferred: c.FKInitiallyDeferred,
			}
			// Check type compatibility between referencing and referenced column.
			if err := checkFKColumnTypeCompatibility(o.ctx, tbl, fk, c.Type.Name, s.Pos()); err != nil {
				return err
			}
			tbl.ForeignKeys = append(tbl.ForeignKeys, fk)
		}
	}
	// Register implicit sequences for SERIAL / BIGSERIAL / SMALLSERIAL columns
	// and GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY columns.
	// Uses `cols` (not s.Columns) so LIKE-copied identity columns are also covered.
	// M0097-0009: creates the sequence so nextval() works for default generation.
	for _, c := range cols {
		colTypeLow := strings.ToLower(c.Type.Name)
		var seqMin, seqMax int64
		isSerial := false
		switch colTypeLow {
		case "serial", "int4", "integer":
			seqMin, seqMax = 1, 2147483647
			isSerial = colTypeLow == "serial"
		case "bigserial", "int8", "bigint":
			seqMin, seqMax = 1, 9223372036854775807
			isSerial = colTypeLow == "bigserial"
		case "smallserial", "int2", "smallint":
			seqMin, seqMax = 1, 32767
			isSerial = colTypeLow == "smallserial"
		}
		if !isSerial && !c.IdentityColumn {
			continue // only register sequences for serial/identity types
		}
		seqName := strings.ToLower(s.Name.Name) + "_" + strings.ToLower(c.Name) + "_seq"
		RegisterSequence(seqName, 1, 1, seqMin, seqMax, false)
	}

	// If PARTITION BY, annotate the table with partition metadata
	if s.PartitionBy != nil {
		tbl.PartitionMethod = s.PartitionBy.Method
		tbl.PartitionKey = s.PartitionBy.KeyCols
		tbl.PartitionKeyOpClasses = s.PartitionBy.OpClasses
		// Partitioned tables are "virtual" for storage purposes:
		// they never hold rows directly — all data lives in children.
		// But we still create a heap so the table exists for metadata.
	}
	// M0054-0010: tag known small dimension tables (canonical
	// TPC-H tiny tables: region 5 rows, nation 25 rows). The flag
	// lets the planner pin the small side as the hash-join build
	// side regardless of stats availability.
	switch strings.ToLower(s.Name.Name) {
	case "region", "nation":
		tbl.SmallDimension = true
	}
	// Record for rollback before heap sync — if heap sync fails the catalog
	// entry is already live and must be cleaned up on ROLLBACK.
	if sess, ok := o.ctx.Session.(*BasicSession); ok {
		sess.RecordDDLCreate(DDLUndoEntry{Name: s.Name, RelOID: tbl.OID, IsIndex: false})
	}
	if catalogHeapSyncAvailable(o.ctx) {
		if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
			return fmt.Errorf("DDL catalog sync: %w", syncErr)
		}
	}

	// Primary key index creation (M0096-0005).
	//
	// Two syntactic forms are supported:
	//   a) Inline: `col type PRIMARY KEY`         → ColumnDef.Primary == true
	//   b) Table-level: `PRIMARY KEY (col1, col2)` → s.PrimaryKey != nil
	//
	// Both need a B-tree index with unique=true, primary=true so that
	// ON CONFLICT (col) can match the constraint via resolveArbiterIndex.
	var pkCols []string
	if len(s.PrimaryKey) > 0 {
		pkCols = s.PrimaryKey
	} else {
		for _, c := range s.Columns {
			if c.Primary {
				pkCols = append(pkCols, c.Name)
			}
		}
	}
	if len(pkCols) > 0 {
		idxName := parser.ObjectName{Schema: s.Name.Schema, Name: tbl.Name + "_pkey"}
		if err := o.createBTreeIndex(s.Pos(), idxName, tbl, pkCols, nil, true, true); err != nil {
			// Propagate B-tree index errors (e.g. unsupported key type).
			// This makes CREATE TABLE fail cleanly rather than silently creating
			// a table without its primary key constraint.
			return err
		}
	}
	// Register CHECK constraints from columns and table-level. M0097-0014.
	for _, c := range s.Columns {
		if c.CheckExpr != "" {
			tbl.CheckConstraints = append(tbl.CheckConstraints, c.CheckExpr)
		}
	}
	tbl.CheckConstraints = append(tbl.CheckConstraints, s.TableChecks...)
	// Apply LIKE INCLUDING CONSTRAINTS checks (copied from LIKE source tables).
	tbl.CheckConstraints = append(tbl.CheckConstraints, likeCheckConstraints...)
	return nil
}

// execCreateTableAs implements `CREATE TABLE name AS SELECT …`.
// It plans and executes the SELECT, derives column definitions from the result
// schema, creates the table, and inserts all rows from the SELECT.  M0096-0008.
func (o *ddlOp) execCreateTableAs(s *parser.CreateTableStmt) error {
	if o.ctx.Pool == nil || o.ctx.Catalog == nil || o.ctx.TxnMgr == nil {
		// No storage: create an empty table with no columns.
		_, err := o.ctx.Catalog.CreateTable(s.Name, nil)
		if err != nil {
			return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
		}
		return nil
	}
	// Plan the SELECT to derive the schema.
	selectNode, err := planner.Plan(s.SelectSource, o.ctx.Catalog)
	if err != nil {
		return &ExecError{Code: "42601", Pos: s.Pos(), Message: err.Error()}
	}
	outSchema := selectNode.Output()
	// Validate column alias count: aliases must not exceed column count. M0097-0020.
	if len(s.ColumnAliases) > len(outSchema) {
		return &ExecError{
			Code:    "42601",
			Pos:     s.Pos(),
			Message: "too many column names were specified",
		}
	}
	cols := make([]catalog.Column, len(outSchema))
	for i, sc := range outSchema {
		typeName := sc.Type.Name
		if typeName == "" || typeName == "unknown" {
			typeName = "text"
		}
		colName := sc.Name
		if i < len(s.ColumnAliases) {
			colName = s.ColumnAliases[i]
		}
		cols[i] = catalog.Column{
			Name: colName, Type: catalog.Type{Name: strings.ToLower(typeName)},
		}
	}
	tbl, err := o.ctx.Catalog.CreateTable(s.Name, cols)
	if err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	if sess, ok := o.ctx.Session.(*BasicSession); ok {
		sess.RecordDDLCreate(DDLUndoEntry{Name: s.Name, RelOID: tbl.OID, IsIndex: false})
	}
	if catalogHeapSyncAvailable(o.ctx) {
		if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
			return fmt.Errorf("DDL catalog sync: %w", syncErr)
		}
	}
	// WITH NO DATA: create table structure only, skip row insertion.
	if s.WithNoData {
		return nil
	}
	// Execute the SELECT and insert all rows.
	op, buildErr := Build(selectNode)
	if buildErr != nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: buildErr.Error()}
	}
	if err := op.Open(o.ctx); err != nil {
		_ = op.Close()
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
	}
	defer op.Close()
	rel := o.ctx.Catalog.RelFileNode(tbl)
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
		row := slotRow(slot)
		if err := writeHeapRow(o.ctx, rel, tbl.Columns, row); err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
	}
	return nil
}

// execCreatePartitionChild handles CREATE TABLE child PARTITION OF parent FOR VALUES ….
// It creates the child table, copies columns from the parent, and registers
// the child in the partition-children registry.  M0096-0007.
func (o *ddlOp) execCreatePartitionChild(s *parser.CreateTableStmt) error {
	poc := s.PartitionOf
	// Look up the parent partitioned table.
	parent, ok := o.ctx.Catalog.LookupTable(poc.Parent)
	if !ok {
		return &ExecError{Code: "42P01", Pos: s.Pos(),
			Message: fmt.Sprintf("relation %q does not exist", poc.Parent.String())}
	}
	// Inherit columns from parent (partition children use parent's schema).
	cols := make([]catalog.Column, len(parent.Columns))
	copy(cols, parent.Columns)
	tbl, err := o.ctx.Catalog.CreateTable(s.Name, cols)
	if err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	// Set partition metadata on the child.
	tbl.PartitionParentOID = parent.OID
	// Use the child's own PARTITION BY clause when present (e.g. nested
	// partitioned tables: CREATE TABLE p1 PARTITION OF p FOR VALUES ... PARTITION BY RANGE (c)).
	// Leaf partitions have no PartitionMethod/PartitionKey of their own.
	if s.PartitionBy != nil {
		tbl.PartitionMethod = s.PartitionBy.Method
		tbl.PartitionKey = s.PartitionBy.KeyCols
		tbl.PartitionKeyOpClasses = s.PartitionBy.OpClasses
	}

	// Build partition bounds from the FOR VALUES clause.
	var pb catalog.PartitionBound
	if poc.Default {
		// DEFAULT partition: catches all values not matched by other partitions.
		pb.IsDefault = true
		tbl.PartitionBounds = []catalog.PartitionBound{pb}
	} else if poc.IsHash {
		// HASH partition: MODULUS + REMAINDER. M0097-0015.
		pb.IsHash = true
		pb.Modulus = poc.Modulus
		pb.Remainder = poc.Remainder
		tbl.PartitionBounds = []catalog.PartitionBound{pb}
	} else if len(poc.InValues) > 0 {
		// LIST partition: evaluate each IN value as a string.
		for _, e := range poc.InValues {
			pb.InValues = append(pb.InValues, exprToString(e))
		}
		tbl.PartitionBounds = []catalog.PartitionBound{pb}
	} else if len(poc.FromValues) > 0 || len(poc.ToValues) > 0 {
		// RANGE partition: store all key-column values for multi-column routing.
		if len(poc.FromValues) > 0 {
			pb.From = exprToString(poc.FromValues[0]) // backward compat (single-col)
			for _, v := range poc.FromValues {
				pb.FromValues = append(pb.FromValues, exprToString(v))
			}
		}
		if len(poc.ToValues) > 0 {
			pb.To = exprToString(poc.ToValues[0]) // backward compat (single-col)
			for _, v := range poc.ToValues {
				pb.ToValues = append(pb.ToValues, exprToString(v))
			}
		}
		tbl.PartitionBounds = []catalog.PartitionBound{pb}
	}

	// Register child with parent.
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		im.RegisterPartitionChild(parent.OID, tbl.OID)
	}

	// Record for rollback.
	if sess, ok := o.ctx.Session.(*BasicSession); ok {
		sess.RecordDDLCreate(DDLUndoEntry{Name: s.Name, RelOID: tbl.OID, IsIndex: false})
	}
	if catalogHeapSyncAvailable(o.ctx) {
		if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
			return fmt.Errorf("DDL catalog sync: %w", syncErr)
		}
	}
	// Inherit parent's primary key / unique indexes onto the partition
	// child (M0100-0005t).  Without these, INSERTs that route to a leaf
	// never populate any unique index (the parent's index has no entries
	// because writes go to leaves; the leaf had no indexes pre-fix), so
	// upsertOp's arbiter probe and insertOp's runtime unique-constraint
	// check both miss live duplicates on partitioned tables.  We mirror
	// upstream PG's behaviour of materialising a matching index on each
	// child partition.  Naming uses the standard auto-generated form
	// (`<child>_pkey` for PRIMARY, `<child>_<col>_key` for UNIQUE) so
	// adjacency between catalog/btree state and pg_class is predictable.
	for _, parentIdx := range o.ctx.Catalog.IndexesOnTable(parent) {
		if parentIdx.Method != "btree" || (!parentIdx.Primary && !parentIdx.Unique) {
			continue
		}
		var childIdxName parser.ObjectName
		if parentIdx.Primary {
			childIdxName = parser.ObjectName{Schema: s.Name.Schema, Name: tbl.Name + "_pkey"}
		} else {
			suffix := "_key"
			if len(parentIdx.Columns) == 1 {
				suffix = "_" + parentIdx.Columns[0] + "_key"
			}
			childIdxName = parser.ObjectName{Schema: s.Name.Schema, Name: tbl.Name + suffix}
		}
		if err := o.createBTreeIndex(s.Pos(), childIdxName, tbl, parentIdx.Columns, nil, parentIdx.Unique, parentIdx.Primary); err != nil {
			return err
		}
	}
	return nil
}

// exprToString converts a simple parser expression to a string for partition bounds.
func exprToString(e parser.Expr) string {
	switch v := e.(type) {
	case *parser.IntegerConst:
		return fmt.Sprintf("%d", v.Value)
	case *parser.StringConst:
		return v.Value
	case *parser.NullConst:
		return "null"
	}
	return fmt.Sprintf("%v", e)
}

// defaultExprToSQL converts a default-argument expression to a SQL string
// that can be re-evaluated at call time. Handles literal constants; for
// complex expressions it falls back to a best-effort fmt.Sprintf form.
func defaultExprToSQL(e parser.Expr) string {
	if e == nil {
		return ""
	}
	switch v := e.(type) {
	case *parser.IntegerConst:
		return fmt.Sprintf("%d", v.Value)
	case *parser.StringConst:
		return "'" + strings.ReplaceAll(v.Value, "'", "''") + "'"
	case *parser.NumericConst:
		return v.Value
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

// execCreateView registers a view in the catalog. Column
// types default to `unknown` because v0 doesn't run the
// type-inference pass against the inner SELECT during DDL —
// the planner re-runs Plan() on the stored SELECT at every
// reference, and unknown's coercion rules let the planner-
// produced types flow into outer comparisons. The optional
// alias-list is preserved on the catalog Table so reference
// sites can rename columns.
func (o *ddlOp) execCreateView(s *parser.CreateViewStmt) error {
	if s.Query == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "CREATE VIEW requires a SELECT body"}
	}
	// Validate the view's SELECT by planning it. PostgreSQL analyzes the view
	// query at creation time, so semantic errors — e.g. a column that is
	// neither in the GROUP BY nor wrapped in an aggregate — surface here rather
	// than only at first reference. Without this, `CREATE VIEW v AS SELECT a
	// FROM t GROUP BY b` was silently accepted, and a subsequent legal
	// re-CREATE of the same name then failed with a spurious "already exists".
	// v0-planner "feature not supported" (0A000) errors are ignored so the
	// planner's incompleteness does not reject views upstream would accept;
	// those still fail at reference time. M0097-0003 (functional_deps).
	if _, err := planner.Plan(s.Query, o.ctx.Catalog); err != nil {
		// Surface the validation failure as an *ExecError so the wire
		// layer renders a clean "ERROR:  <message>" line. Returning the
		// raw *planner.PlanError would let its Error() string —
		// "<code>: <message> (byte <pos>)" — leak into the message
		// field, which diverges from the direct-SELECT path (the simple
		// query handler extracts Code/Message separately). v0-planner
		// "feature not supported" (0A000) errors are still ignored so
		// the planner's incompleteness does not reject views upstream
		// would accept; those fail at reference time instead.
		if pe, ok := err.(*planner.PlanError); ok {
			// Ignore feature-not-supported (0A000) and circular-view (42P10)
			// errors — PostgreSQL allows circular/forward-referencing views;
			// they only error when accessed, not when defined.
			if pe.Code != "0A000" && pe.Code != "42P10" {
				return &ExecError{Code: pe.Code, Message: pe.Message, Hint: pe.Hint, Pos: pe.Pos}
			}
		} else {
			return err
		}
	}
	if !s.OrReplace {
		if existing, ok := o.ctx.Catalog.LookupTable(s.Name); ok && existing.View == nil {
			return &ExecError{Code: "42P07", Pos: s.Pos(), Message: fmt.Sprintf("relation %q already exists", s.Name.String())}
		}
	}
	// Compute the column count + names. Use planSchema (which expands
	// * and handles multi-table FROM) as the authoritative column list.
	// Aliases override the planner's names; otherwise derive from the
	// planned output. M0097-0038.
	//
	// For OR REPLACE: temporarily remove the existing view so that the
	// new body's plan doesn't recurse back into the old definition
	// (circular-view stack overflow). The view is removed only for the
	// duration of planning, then re-inserted when we call CreateView below.
	var planSchema planner.Schema
	if s.OrReplace {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			_ = im.DropView(s.Name, true) // remove old def so plan can't cycle back to it
		}
	}
	if viewPlan, planErr := planner.Plan(s.Query, o.ctx.Catalog); planErr == nil {
		planSchema = viewPlan.Output()
	}
	// Ignore plan errors during view creation (including circular-view 42P10).
	// PostgreSQL allows circular view definitions; they fail only when accessed.
	var cols []catalog.Column
	if len(planSchema) > 0 {
		// Use plan-derived schema: handles * expansion, multi-table FROM, etc.
		cols = make([]catalog.Column, len(planSchema))
		for i, sc := range planSchema {
			name := ""
			if i < len(s.Columns) {
				name = s.Columns[i]
			} else if i < len(s.Query.Targets) {
				tgt := s.Query.Targets[i]
				if tgt.Alias != "" {
					name = tgt.Alias
				} else {
					name = deriveTargetName(tgt.Expr)
				}
			}
			if name == "" {
				name = sc.Name
			}
			if name == "" {
				name = fmt.Sprintf("?column?%d", i+1)
			}
			typ := sc.Type
			if typ.Name == "" {
				typ = catalog.Type{Name: "unknown"}
			}
			cols[i] = catalog.Column{Name: name, Type: typ}
		}
	} else {
		// Fallback: use target list when planning fails (0A000 cases or cycles).
		// If explicit column aliases were provided, use them to determine count.
		if len(s.Columns) > 0 {
			cols = make([]catalog.Column, len(s.Columns))
			for i, alias := range s.Columns {
				cols[i] = catalog.Column{Name: alias, Type: catalog.Type{Name: "unknown"}}
			}
		} else {
			cols = make([]catalog.Column, 0, len(s.Query.Targets))
			for i, tgt := range s.Query.Targets {
				name := ""
				if tgt.Alias != "" {
					name = tgt.Alias
				} else {
					name = deriveTargetName(tgt.Expr)
				}
				if name == "" {
					name = fmt.Sprintf("?column?%d", i+1)
				}
				cols = append(cols, catalog.Column{Name: name, Type: catalog.Type{Name: "unknown"}})
			}
		}
	}
	if _, err := o.ctx.Catalog.CreateView(s.Name, cols, s.Columns, s.Query, s.OrReplace); err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	// Register view→PK-constraint dependencies so DROP CONSTRAINT RESTRICT
	// can detect that this view relies on the constraint. M0097-0036.
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		viewKey := s.Name.String()
		for _, dep := range collectViewPKDeps(s.Query, o.ctx.Catalog) {
			im.RegisterViewConstraintDep(viewKey, dep.tableOID, dep.constraintName)
		}
	}
	return nil
}

// deriveTargetName picks the implicit column name for a SELECT
// target — matches upstream's behaviour: bare ColumnRef →
// its column name, FuncCall → function name. Anything else
// returns "" and the caller falls back to a `?column?N`
// placeholder.
func deriveTargetName(e parser.Expr) string {
	switch x := e.(type) {
	case *parser.ColumnRef:
		return x.Column
	case *parser.FuncCall:
		return x.Name.Name
	}
	return ""
}

// execDropView removes a view from the catalog. No relation
// file is involved — views are virtual.
func (o *ddlOp) execDropView(s *parser.DropViewStmt) error {
	dropped := make(map[string]bool)
	for _, name := range s.Names {
		if err := o.execDropOneView(name, s.IfExists, s.Behavior, s.Pos(), dropped); err != nil {
			return err
		}
	}
	return nil
}

// execDropOneView drops a single view, cascading to dependent views when
// behavior == DropCascade. The dropped map prevents infinite recursion on
// circular view definitions. M0097-0021.
func (o *ddlOp) execDropOneView(name parser.ObjectName, ifExists bool, behavior parser.DropBehavior, pos int, dropped map[string]bool) error {
	key := name.String()
	if dropped[key] {
		return nil // already being dropped in this cascade
	}
	if ifExists && o.dropSchemaQualifiedNotice(name) {
		return nil
	}
	if _, ok := o.ctx.Catalog.LookupTable(name); !ok {
		if ifExists {
			o.ctx.AddNotice(fmt.Sprintf("view %q does not exist, skipping", name.String()))
			return nil
		}
		return &ExecError{Code: "42P01", Pos: pos, Message: fmt.Sprintf("view %q does not exist", name.String())}
	}

	// Mark as being dropped before recursing to break circular dependency cycles.
	dropped[key] = true

	// CASCADE: drop any dependent views before dropping this one.
	if behavior == parser.DropCascade {
		if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
			deps := viewsDependingOnView(im, name)
			for _, depName := range deps {
				if !dropped[depName.String()] {
					o.ctx.AddNotice(fmt.Sprintf("drop cascades to view %s", depName.String()))
					if err := o.execDropOneView(depName, true, behavior, pos, dropped); err != nil {
						return err
					}
				}
			}
		}
	}

	if err := o.ctx.Catalog.DropView(name, ifExists); err != nil {
		if ifExists {
			o.ctx.AddNotice(fmt.Sprintf("view %q does not exist, skipping", name.String()))
			return nil
		}
		return &ExecError{Code: "42P01", Pos: pos, Message: err.Error()}
	}
	// Clean up constraint dependencies registered by CREATE VIEW. M0097-0036.
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		im.UnregisterViewConstraintDeps(name.String())
	}
	return nil
}

// viewsDependingOnView returns all views whose body directly references the given view name.
// Used by DROP VIEW CASCADE to find dependent views. M0097-0021.
func viewsDependingOnView(im *catalog.InMemory, target parser.ObjectName) []parser.ObjectName {
	targetName := target.Name
	var deps []parser.ObjectName
	for _, t := range im.AllUserViews() {
		if t.View == nil || t.IsMatView {
			continue
		}
		if t.Name == target.Name && t.Schema == target.Schema {
			continue // skip the view itself
		}
		if selectRefsViewName(t.View, targetName) {
			deps = append(deps, parser.ObjectName{Schema: t.Schema, Name: t.Name})
		}
	}
	return deps
}

// selectRefsViewName reports whether sel or any sub-select references the given view/table name.
func selectRefsViewName(sel *parser.SelectStmt, name string) bool {
	if sel == nil {
		return false
	}
	for _, rv := range sel.From {
		if rangeVarRefsName(&rv, name) {
			return true
		}
	}
	for _, fe := range sel.FromExprs {
		if rangeVarRefsName(&fe.Base, name) {
			return true
		}
		for _, j := range fe.Joins {
			if rangeVarRefsName(&j.Right, name) {
				return true
			}
		}
	}
	// Check set-op right branch (UNION/INTERSECT/EXCEPT).
	if sel.SetOp != nil && selectRefsViewName(sel.SetOp.Right, name) {
		return true
	}
	return false
}

// rangeVarRefsName checks if rv or its subquery references name.
func rangeVarRefsName(rv *parser.RangeVar, name string) bool {
	if rv.Subquery != nil {
		return selectRefsViewName(rv.Subquery, name)
	}
	return strings.EqualFold(rv.Name, name)
}

func (o *ddlOp) execDropTable(s *parser.DropTableStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP TABLE requires Pool in Context"}
	}
	for _, name := range s.Names {
		if s.IfExists && o.dropSchemaQualifiedNotice(name) {
			continue
		}
		tbl, ok := o.ctx.Catalog.LookupTable(name)
		if !ok {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("table %q does not exist, skipping", name.String()))
				continue
			}
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("table %q does not exist", name.String())}
		}
		// Drop dependent tables before dropping the parent:
		// - Partition children: ALWAYS (they can't exist without the parent).
		// - Inheritance children: only with CASCADE (M0100-0004).
		if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
			for _, child := range im.PartitionChildren(tbl.OID) {
				childName := parser.ObjectName{Schema: child.Schema, Name: child.Name}
				if err := o.dropTableByRef(childName, child); err != nil {
					return err
				}
			}
			if s.Behavior == parser.DropCascade {
				inheritChildren := im.InheritanceChildren(tbl.OID)
				if len(inheritChildren) == 1 {
					childName := parser.ObjectName{Schema: inheritChildren[0].Schema, Name: inheritChildren[0].Name}
					o.ctx.AddNotice(fmt.Sprintf("drop cascades to table %s", childName.String()))
				} else if len(inheritChildren) > 1 {
					// PostgreSQL emits summary NOTICE + DETAIL listing each child.
					// Normalizer strips DETAIL prefix and moves all lines to error section.
					detail := make([]string, len(inheritChildren))
					for i, child := range inheritChildren {
						childName := parser.ObjectName{Schema: child.Schema, Name: child.Name}
						detail[i] = fmt.Sprintf("drop cascades to table %s", childName.String())
					}
					o.ctx.AddNoticeWithDetail(
						fmt.Sprintf("drop cascades to %d other objects", len(inheritChildren)),
						strings.Join(detail, "\n"),
					)
				}
				for _, child := range inheritChildren {
					childName := parser.ObjectName{Schema: child.Schema, Name: child.Name}
					if err := o.dropTableByRef(childName, child); err != nil {
						return err
					}
				}
			}
		}
		if err := o.dropTableByRef(name, tbl); err != nil {
			return err
		}
	}
	return nil
}

// dropTableByRef drops a single table by its catalog.Table reference.
func (o *ddlOp) dropTableByRef(name parser.ObjectName, tbl *catalog.Table) error {
	idxs := o.ctx.Catalog.IndexesOnTable(tbl)
	idxRels := make([]storage.RelFileNode, 0, len(idxs))
	idxOIDs := make([]uint32, 0, len(idxs))
	for _, idx := range idxs {
		idxRels = append(idxRels, o.ctx.Catalog.IndexRelFileNode(idx))
		idxOIDs = append(idxOIDs, idx.OID)
	}
	rel := o.ctx.Catalog.RelFileNode(tbl)
	relOID := tbl.OID
	if err := o.ctx.Catalog.DropTable(name); err != nil {
		return &ExecError{Code: "XX000", Message: err.Error()}
	}
	// If this table was shadowing a permanent one, restore it. M0097-0003.
	if o.ctx.TempTableShadows != nil {
		key := strings.ToLower(name.Name)
		if permTbl, hasShadow := o.ctx.TempTableShadows[key]; hasShadow && permTbl != nil {
			if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
				im.RegisterTable(permTbl)
			}
			delete(o.ctx.TempTableShadows, key)
		}
	}
	o.ctx.Pool.InvalidateRel(rel)
	if err := o.ctx.Pool.Manager().DropRelation(rel); err != nil {
		return &ExecError{Code: "XX000", Message: err.Error()}
	}
	if o.ctx.FSM != nil {
		o.ctx.FSM.DropRelation(rel)
	}
	if o.ctx.VM != nil {
		o.ctx.VM.DropRelation(rel)
	}
	for _, idxRel := range idxRels {
		o.ctx.Pool.InvalidateRel(idxRel)
		if err := o.ctx.Pool.Manager().DropRelation(idxRel); err != nil {
			return &ExecError{Code: "XX000", Message: err.Error()}
		}
	}
	// M0106-0011: DROP TABLE removes pg_class / pg_attribute / pg_index rows
	// for the relation. Flag the txn so the commit-time xact-marker hook
	// (open.go) emits RecordKindXactCommitInval and unlinks + regenerates
	// both pg_internal.init files; without this a PG18 standby reconnecting
	// after the DDL keeps a stale relcache entry for the dropped relation.
	if o.ctx.TxnMgr != nil {
		o.ctx.TxnMgr.SetRelcacheInvalPending()
	}
	// M0106-0011 follow-up (a): persist the catalog heap mutation by
	// stamping xmax on the on-disk pg_class / pg_attribute rows for the
	// dropped table and its indexes. Without this, the in-memory drop is
	// not reflected in the heap, and a subsequent re-Open (after clean
	// shutdown or WAL replay) re-resolves the dropped relation through the
	// loadUserTablesFromHeap scan. `markHeapDeleteDirty` (inside
	// `stampCatalogRows`) WAL-logs the xmax bump so the stamp survives a
	// crash. Gated on `catalogHeapSyncAvailable` to skip pre-M0030-0001
	// fixtures (and the in-memory test fixture in newDDLFixture).
	// MaterializeWriterXID ensures the transaction has a real XID before
	// stamping xmax; DROP TABLE itself never calls writeHeapRowReturningPG
	// so the XID would otherwise remain InvalidTransactionID (0).
	if catalogHeapSyncAvailable(o.ctx) {
		if err := o.ctx.MaterializeWriterXID(); err == nil {
			xmax := o.ctx.Tx.XID
			for _, dbOid := range catalogDBOids(o.ctx) {
				deleteCatalogRowsForOID(o.ctx, dbOid, relOID, xmax)
				for _, idxOID := range idxOIDs {
					deleteCatalogRowsForOID(o.ctx, dbOid, idxOID, xmax)
				}
			}
		}
	}
	return nil
}

func (o *ddlOp) execCreateIndex(s *parser.CreateIndexStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "CREATE INDEX requires Pool in Context"}
	}
	tbl, ok := o.ctx.Catalog.LookupTable(s.Table)
	if !ok {
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Table.String())}
	}
	name := s.Name
	if name == "" {
		name = o.autoIndexName(tbl, s.Columns, "idx")
	}
	idxName := parser.ObjectName{Schema: tbl.Schema, Name: name}
	if _, exists := o.ctx.Catalog.LookupIndex(idxName); exists {
		if s.IfNotExists {
			return nil
		}
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: fmt.Sprintf("relation %q already exists", idxName.String())}
	}
	if s.OpClassWithOptions != "" {
		return &ExecError{Code: "42704", Pos: s.Pos(),
			Message: fmt.Sprintf("operator class %s has no options", s.OpClassWithOptions)}
	}
	if s.Fillfactor != 0 && (s.Fillfactor < 10 || s.Fillfactor > 100) {
		return &ExecError{Code: "22023", Pos: s.Pos(),
			Message: fmt.Sprintf("value %d out of bounds for option \"fillfactor\"", s.Fillfactor),
			Detail:  "Valid values are between \"10\" and \"100\"."}
	}
	method := strings.ToLower(strings.TrimSpace(s.Method))
	if method == "" || method == "hash" {
		method = "btree"
	}
	if method != "btree" {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: fmt.Sprintf("index method %q is not supported in v0", method)}
	}
	return o.createBTreeIndex(s.Pos(), idxName, tbl, s.Columns, s.ColExprs, s.Unique, false)
}

func (o *ddlOp) execDropIndex(s *parser.DropIndexStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP INDEX requires Pool in Context"}
	}
	flagInval := false
	droppedOIDs := make([]uint32, 0, len(s.Names))
	for _, name := range s.Names {
		if s.IfExists && o.dropSchemaQualifiedNotice(name) {
			continue
		}
		idx, ok := o.ctx.Catalog.LookupIndex(name)
		if !ok {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("index %q does not exist, skipping", name.String()))
				continue
			}
			return &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("index %q does not exist", name.String())}
		}
		rel := o.ctx.Catalog.IndexRelFileNode(idx)
		dropOID := idx.OID
		dropSchema := idx.Schema
		dropName := idx.Name
		if err := o.ctx.Catalog.DropIndex(name); err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
		flagInval = true
		droppedOIDs = append(droppedOIDs, dropOID)
		o.ctx.Pool.InvalidateRel(rel)
		if err := o.ctx.Pool.Manager().DropRelation(rel); err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
		// M0079-0001: emit a DROP INDEX WAL record so a
		// post-crash recovery doesn't resurrect the index from
		// an earlier RecordKindCreateIndex still in the WAL.
		// Best-effort: the catalog mutation has already
		// completed; logging a failure is preferable to
		// resurrecting a half-dropped index.
		if o.ctx.Pool != nil {
			payload := wal.EncodeDropIndex(wal.DropIndexPayload{
				OID:    dropOID,
				Schema: dropSchema,
				Name:   dropName,
			})
			if _, err := o.ctx.Pool.LogChangeRecord(payload); err != nil {
				return &ExecError{Code: "XX000", Pos: s.Pos(), Message: fmt.Sprintf("drop-index WAL append: %v", err)}
			}
		}
	}
	// M0106-0011: DROP INDEX removes the index's pg_class row. Flag the txn
	// so the commit-time xact-marker hook emits RecordKindXactCommitInval
	// and refreshes pg_internal.init; without this a PG18 standby keeps a
	// stale relcache entry for the dropped index.
	if flagInval && o.ctx.TxnMgr != nil {
		o.ctx.TxnMgr.SetRelcacheInvalPending()
	}
	// M0106-0011 follow-up (a): stamp xmax on the on-disk pg_class row for
	// the dropped index so a re-Open does not re-resolve the now-deleted
	// index from `loadUserIndexesFromHeap`. `deleteCatalogRowsForOID` also
	// touches pg_attribute (a no-op for indexes — indexes don't have
	// pg_attribute rows in goopg's runtime layout). WAL-logged via
	// markHeapDeleteDirty inside stampCatalogRows.
	// MaterializeWriterXID ensures a real XID for the xmax stamp; DROP INDEX
	// never calls writeHeapRowReturningPG so the XID would otherwise remain 0.
	if catalogHeapSyncAvailable(o.ctx) {
		if err := o.ctx.MaterializeWriterXID(); err == nil {
			xmax := o.ctx.Tx.XID
			for _, dbOid := range catalogDBOids(o.ctx) {
				for _, oid := range droppedOIDs {
					deleteCatalogRowsForOID(o.ctx, dbOid, oid, xmax)
				}
			}
		}
	}
	return nil
}

func (o *ddlOp) execAlterTable(s *parser.AlterTableStmt) error {
	tbl, ok := o.ctx.Catalog.LookupTable(s.Name)
	if !ok {
		// Not a heap table — check if it's an index.
		if idx, isIdx := o.ctx.Catalog.LookupIndex(s.Name); isIdx {
			for _, act := range s.Actions {
				if act.Kind == parser.AlterTableAlterColumnSet {
					detail := "This operation is not supported for indexes."
					if idx.Table != nil && len(idx.Table.PartitionKey) > 0 {
						detail = "This operation is not supported for partitioned indexes."
					}
					return &ExecError{
						Code:    "0A000",
						Pos:     s.Pos(),
						Message: fmt.Sprintf("ALTER action ALTER COLUMN ... SET cannot be performed on relation %q", s.Name.Name),
						Detail:  detail,
					}
				}
			}
			// Other ALTER actions on index: silently accept in v0.
			return nil
		}
		if s.IfExists {
			return nil
		}
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Name.String())}
	}
	for _, act := range s.Actions {
		switch act.Kind {
		case parser.AlterTableAddColumn:
			if err := o.execAlterTableAddColumn(s, tbl, act); err != nil {
				return err
			}
		case parser.AlterTableAddPrimaryKey:
			if err := o.execAlterTableAddPrimaryKey(tbl, act); err != nil {
				return err
			}
		case parser.AlterTableAddForeignKey:
			// v0 accepts the syntax for HammerDB TPC-H
			// compatibility but does not enforce. The
			// referenced table must exist (so typos and
			// dropped relations still surface here); column-
			// level validation is deferred. See
			// docs/design/0003-0004-hammerdb-tpch-integration.md.
			if _, ok := o.ctx.Catalog.LookupTable(act.RefTable); !ok {
				return &ExecError{Code: "42P01", Pos: act.Pos(), Message: fmt.Sprintf("relation %q does not exist", act.RefTable.String())}
			}
		case parser.AlterTableAddCheck:
			// ADD [CONSTRAINT name] CHECK (expr) — register the check constraint.
			if act.CheckExpr != "" {
				tbl.CheckConstraints = append(tbl.CheckConstraints, act.CheckExpr)
			}
		case parser.AlterTableNoOp:
			// Unknown ADD CONSTRAINT type — no-op.
		case parser.AlterTableDropConstraint:
			if err := o.execAlterTableDropConstraint(tbl, act); err != nil {
				return err
			}
		case parser.AlterTableAttachPartition:
			// ATTACH PARTITION child FOR VALUES … (M0096-0007)
			if act.AttachPartitionOf == nil {
				break
			}
			// Handle DEFAULT partition — same as regular partition attachment
			// for catalog purposes (partition child must be registered so DROP
			// TABLE parent CASCADE can find and drop it). M0100-0005.
			if act.AttachPartitionOf.Default {
				childTbl, ok := o.ctx.Catalog.LookupTable(act.AttachPartitionOf.Parent)
				if ok {
					childTbl.PartitionParentOID = tbl.OID
					if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
						im.RegisterPartitionChild(tbl.OID, childTbl.OID)
					}
				}
				break
			}
			poc := act.AttachPartitionOf
			// poc.Parent contains the child table name here (set in parser).
			childTbl, ok := o.ctx.Catalog.LookupTable(poc.Parent)
			if !ok {
				break // child doesn't exist yet, skip
			}
			// Set partition metadata on the child.
			// ATTACH PARTITION only establishes the parent-child relationship and
			// partition bounds. The child's PartitionKey/Method are properties of its
			// OWN PARTITION BY clause and must NOT be overwritten by the parent's values.
			// Build partition bounds. M0097-0015 adds HASH.
			var pb catalog.PartitionBound
			if poc.IsHash {
				pb.IsHash = true
				pb.Modulus = poc.Modulus
				pb.Remainder = poc.Remainder
				childTbl.PartitionBounds = []catalog.PartitionBound{pb}
			} else {
				for _, e := range poc.InValues {
					pb.InValues = append(pb.InValues, exprToString(e))
				}
				if len(poc.FromValues) > 0 {
					pb.From = exprToString(poc.FromValues[0]) // backward compat
					for _, v := range poc.FromValues {
						pb.FromValues = append(pb.FromValues, exprToString(v))
					}
				}
				if len(poc.ToValues) > 0 {
					pb.To = exprToString(poc.ToValues[0]) // backward compat
					for _, v := range poc.ToValues {
						pb.ToValues = append(pb.ToValues, exprToString(v))
					}
				}
				if len(pb.InValues) > 0 || pb.From != "" || pb.To != "" {
					childTbl.PartitionBounds = []catalog.PartitionBound{pb}
				}
			}
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				im.RegisterPartitionChild(tbl.OID, childTbl.OID)
			}
		case parser.AlterTableRenameTable:
			newName := act.NewName
			probe := parser.ObjectName{Schema: tbl.Schema, Name: newName}
			if _, exists := o.ctx.Catalog.LookupTable(probe); exists {
				return &ExecError{Code: "42P07", Pos: act.Pos(), Message: fmt.Sprintf("relation %q already exists", newName)}
			}
			// No actual rename implemented yet — just validate the conflict.
		case parser.AlterTableRenameColumn:
			oldColName := act.OldColumnName
			newColName := act.NewName

			// Check old column exists.
			colExists := false
			for _, col := range tbl.Columns {
				if strings.EqualFold(col.Name, oldColName) {
					colExists = true
					break
				}
			}
			if !colExists {
				return &ExecError{Code: "42703", Pos: act.Pos(), Message: fmt.Sprintf("column %q does not exist", oldColName)}
			}

			// Check new name is not a system column name.
			sysColumns := []string{"ctid", "tableoid", "xmin", "cmin", "xmax", "cmax", "oid"}
			for _, sc := range sysColumns {
				if strings.EqualFold(newColName, sc) {
					return &ExecError{Code: "42P20", Pos: act.Pos(), Message: fmt.Sprintf("column name %q conflicts with a system column name", newColName)}
				}
			}

			// Check inheritance children for name conflict.
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				children := im.InheritanceChildren(tbl.OID)
				for _, child := range children {
					for _, col := range child.Columns {
						if strings.EqualFold(col.Name, newColName) {
							return &ExecError{Code: "42701", Pos: act.Pos(), Message: fmt.Sprintf("column %q of relation %q already exists", newColName, child.Name)}
						}
					}
				}
			}
			// No actual rename implemented yet — just validate.
		case parser.AlterTableInherit:
			// INHERIT parent_table — register the named table as a parent of tbl
			// so that scanning the parent includes tbl's rows (M0097-0048).
			parentTbl, ok := o.ctx.Catalog.LookupTable(act.InheritParent)
			if !ok {
				return &ExecError{Code: "42P01", Pos: act.Pos(), Message: fmt.Sprintf("relation %q does not exist", act.InheritParent.String())}
			}
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				im.RegisterInheritanceChild(parentTbl.OID, tbl.OID)
			}
			// Copy parent columns that the child doesn't already have.
			// In PostgreSQL, ALTER TABLE child INHERIT parent validates that child
			// already has matching columns; goopg v0 just ensures they are present.
			childColByName := make(map[string]bool, len(tbl.Columns))
			for _, c := range tbl.Columns {
				childColByName[strings.ToLower(c.Name)] = true
			}
			for _, pc := range parentTbl.Columns {
				if !childColByName[strings.ToLower(pc.Name)] {
					tbl.Columns = append(tbl.Columns, pc)
				}
			}
		case parser.AlterTableNoInherit:
			// NO INHERIT parent_table — no-op in v0; just accept the syntax.
		case parser.AlterTableAlterColumnSet:
			// SET (options) on a column of a heap table: no-op in goopg v0.
		default:
			return &ExecError{Code: "0A000", Pos: act.Pos(), Message: "ALTER TABLE action is not supported in v0"}
		}
	}
	return nil
}

func (o *ddlOp) execAlterTableAddColumn(stmt *parser.AlterTableStmt, tbl *catalog.Table, act parser.AlterTableAction) error {
	col := act.Column
	if col.NotNull {
		rel := o.ctx.Catalog.RelFileNode(tbl)
		n, err := o.ctx.Pool.NBlocks(rel)
		if err != nil {
			return &ExecError{Code: "XX000", Pos: act.Pos(), Message: err.Error()}
		}
		if n > 0 {
			return &ExecError{Code: "0A000", Pos: act.Pos(), Message: "ALTER TABLE ADD COLUMN ... NOT NULL is only supported on empty tables"}
		}
	}
	newCol := catalog.Column{
		Name:        col.Name,
		Type:        catalog.Type{Name: strings.ToLower(col.Type.Name), Args: append([]int64(nil), col.Type.Args...)},
		NotNull:     col.NotNull,
		DefaultExpr: col.DefaultExpr,
	}
	// "Fast default" backfill (mirrors PostgreSQL's attmissingval): when the
	// new column has a constant DEFAULT, evaluate it once at ALTER time and
	// record the Datum on the column. The heap decoder uses it to fill the
	// column for rows that pre-date this ALTER (storedNatts < new ordinal),
	// so existing rows surface the default without a table rewrite. M0097-0077.
	if col.DefaultExpr != nil {
		if d, ok := constDefaultDatum(col.DefaultExpr, newCol.Type); ok && !d.IsNull() {
			newCol.MissingValue = d
		}
	}
	if err := o.addColumnRecursive(tbl, newCol, act, stmt, true); err != nil {
		return err
	}
	// M0106-0011: ALTER TABLE ADD COLUMN mutates the relation's
	// pg_attribute row set and bumps pg_class.relnatts. Flag the txn
	// so the commit-time xact-marker hook unlinks + regenerates
	// pg_internal.init; a stale init file would keep a PG18 standby
	// reading the pre-ALTER attribute count.
	if o.ctx.TxnMgr != nil {
		o.ctx.TxnMgr.SetRelcacheInvalPending()
	}
	return nil
}

// addColumnRecursive applies a `catalog.AddColumn` to tbl and every
// transitive inheritance / partition child. Mirrors PostgreSQL's
// `ATAddColumn` recursion: a child that already has a same-named column
// is silently skipped (PG attaches by name + type-compatibility merge);
// goopg v0 just treats "already exists" as a no-op so subsequent SELECTs
// project the inherited column rather than erroring. M0097-0077.
func (o *ddlOp) addColumnRecursive(tbl *catalog.Table, col catalog.Column, act parser.AlterTableAction, stmt *parser.AlterTableStmt, isRoot bool) error {
	if _, err := o.ctx.Catalog.AddColumn(tbl, col); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return &ExecError{Code: "XX000", Pos: stmt.Pos(), Message: err.Error()}
		}
		// "Already exists" on the root (named) relation is a genuine user
		// error — 42701 duplicate_column, matching PostgreSQL. On a recursed
		// inheritance/partition child it's the column-merge case PG accepts
		// silently: the child keeps its existing same-named column and the
		// ADD becomes a no-op there. M0097-0077.
		if isRoot {
			return &ExecError{Code: "42701", Pos: act.Pos(), Message: err.Error()}
		}
	}
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		for _, child := range im.InheritanceChildren(tbl.OID) {
			if err := o.addColumnRecursive(child, col, act, stmt, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// constDefaultDatum evaluates a constant DEFAULT expression — the kinds
// PostgreSQL records as `attmissingval`-eligible — against the column's
// declared type. Returns (zero, false) for non-constant or unsupported
// expressions; the caller then falls back to "decode missing trailing
// columns as NULL". Handles bare literals plus unary `-` over numeric
// literals (so `DEFAULT -1` survives). Cast wrappers (`CAST(x AS T)`)
// are unwrapped — the target type comes from the column anyway. M0097-0077.
func constDefaultDatum(e parser.Expr, t catalog.Type) (Datum, bool) {
	switch v := e.(type) {
	case *parser.NullConst:
		return NullDatum, true
	case *parser.BooleanConst:
		if v.Value {
			return Datum{Kind: KindBool, Int: 1}, true
		}
		return Datum{Kind: KindBool, Int: 0}, true
	case *parser.IntegerConst:
		return integerConstAsType(v.Value, t)
	case *parser.NumericConst:
		return numericConstAsType(v.Value, t)
	case *parser.StringConst:
		return stringConstAsType(v.Value, t)
	case *parser.UnaryOp:
		if v.Op != parser.OpUnaryNeg {
			return Datum{}, false
		}
		switch inner := v.Operand.(type) {
		case *parser.IntegerConst:
			return integerConstAsType(-inner.Value, t)
		case *parser.NumericConst:
			s := inner.Value
			if strings.HasPrefix(s, "-") {
				s = s[1:]
			} else {
				s = "-" + s
			}
			return numericConstAsType(s, t)
		}
	}
	return Datum{}, false
}

func integerConstAsType(v int64, t catalog.Type) (Datum, bool) {
	switch strings.ToLower(t.Name) {
	case "int2", "int4", "int8", "smallint", "integer", "int", "bigint", "oid":
		return Datum{Kind: KindInt, Int: v}, true
	case "bool", "boolean":
		if v == 0 {
			return Datum{Kind: KindBool, Int: 0}, true
		}
		return Datum{Kind: KindBool, Int: 1}, true
	case "numeric", "decimal":
		return Datum{Kind: KindNumeric, Int: v, Scale: 0}, true
	case "float4", "float8", "real", "double precision", "double":
		return Datum{Kind: KindNumeric, Int: v, Scale: 0}, true
	case "text", "varchar", "bpchar", "char", "name":
		s := strconv.FormatInt(v, 10)
		return Datum{Kind: KindString, Buf: []byte(s)}, true
	}
	return Datum{}, false
}

func numericConstAsType(s string, t catalog.Type) (Datum, bool) {
	switch strings.ToLower(t.Name) {
	case "int2", "int4", "int8", "smallint", "integer", "int", "bigint", "oid":
		// Reject if it has a decimal point — let it route through NUMERIC
		// instead so we don't silently truncate.
		if strings.ContainsAny(s, ".eE") {
			return Datum{}, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return Datum{}, false
		}
		return Datum{Kind: KindInt, Int: n}, true
	case "numeric", "decimal", "float4", "float8", "real", "double precision", "double":
		// Parse mantissa+scale: split on the decimal point. Scientific
		// notation (1e9) is not handled here — caller leaves MissingValue
		// nil and decoder falls back to NULL (rare for DEFAULT).
		if strings.ContainsAny(s, "eE") {
			return Datum{}, false
		}
		neg := strings.HasPrefix(s, "-")
		if neg {
			s = s[1:]
		}
		dot := strings.IndexByte(s, '.')
		var mantissaStr string
		var scale int16
		if dot < 0 {
			mantissaStr = s
		} else {
			mantissaStr = s[:dot] + s[dot+1:]
			scale = int16(len(s) - dot - 1)
		}
		n, err := strconv.ParseInt(mantissaStr, 10, 64)
		if err != nil {
			return Datum{}, false
		}
		if neg {
			n = -n
		}
		return Datum{Kind: KindNumeric, Int: n, Scale: scale}, true
	case "text", "varchar", "bpchar", "char", "name":
		return Datum{Kind: KindString, Buf: []byte(s)}, true
	}
	return Datum{}, false
}

func stringConstAsType(s string, t catalog.Type) (Datum, bool) {
	switch strings.ToLower(t.Name) {
	case "text", "varchar", "bpchar", "char", "name":
		return Datum{Kind: KindString, Buf: []byte(s)}, true
	case "bytea":
		return Datum{Kind: KindBytes, Buf: []byte(s)}, true
	}
	return Datum{}, false
}

func (o *ddlOp) execAlterTableAddPrimaryKey(tbl *catalog.Table, act parser.AlterTableAction) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: act.Pos(), Message: "ALTER TABLE ADD PRIMARY KEY requires Pool in Context"}
	}
	if o.ctx.Catalog.HasPrimaryKey(tbl) {
		return &ExecError{Code: "42P16", Pos: act.Pos(), Message: fmt.Sprintf("multiple primary keys for table %q are not allowed", tbl.QualifiedName())}
	}
	name := act.ConstraintName
	if name == "" {
		name = tbl.Name + "_pkey"
	}
	idxName := parser.ObjectName{Schema: tbl.Schema, Name: name}
	return o.createBTreeIndex(act.Pos(), idxName, tbl, act.Columns, nil, true, true)
}

// execAlterTableDropConstraint handles `ALTER TABLE t DROP CONSTRAINT name [RESTRICT|CASCADE]`.
// For PK constraints it enforces view→constraint dependencies (RESTRICT mode)
// before removing the index. M0097-0036 / functional_deps.
func (o *ddlOp) execAlterTableDropConstraint(tbl *catalog.Table, act parser.AlterTableAction) error {
	// Find the named constraint among this table's primary-key indexes.
	var pkIdx *catalog.Index
	for _, idx := range o.ctx.Catalog.IndexesOnTable(tbl) {
		if idx.Primary && strings.EqualFold(idx.Name, act.ConstraintName) {
			pkIdx = idx
			break
		}
	}
	if pkIdx == nil {
		return &ExecError{
			Code:    "42704",
			Pos:     act.Pos(),
			Message: fmt.Sprintf("constraint %q of relation %q does not exist", act.ConstraintName, tbl.Name),
		}
	}
	im, isIM := o.ctx.Catalog.(*catalog.InMemory)
	if isIM && act.Restrict {
		deps := im.ViewsDependingOnConstraint(tbl.OID, act.ConstraintName)
		if len(deps) > 0 {
			viewName := deps[0]
			return &ExecError{
				Code: "2BP01",
				Pos:  act.Pos(),
				Message: fmt.Sprintf(
					"cannot drop constraint %s on table %s because other objects depend on it",
					act.ConstraintName, tbl.Name),
				Detail: fmt.Sprintf(
					"view %s depends on constraint %s on table %s",
					viewName, act.ConstraintName, tbl.Name),
				Hint: "Use DROP ... CASCADE to drop the dependent objects too.",
			}
		}
	}
	// No blocking dependencies (or CASCADE) — remove the PK index.
	if isIM {
		im.DropPrimaryKeyConstraint(tbl.OID, act.ConstraintName)
	}
	return nil
}

// pkConstraintRef is a (tableOID, constraintName, tableName) triple recording
// a view's dependency on a PK constraint for GROUP BY functional dependency.
type pkConstraintRef struct {
	tableOID       uint32
	constraintName string
	tableName      string
}

// collectViewPKDeps scans a view's SELECT body AST and returns all PK constraints
// that the view relies on via GROUP BY functional dependency. Used by CREATE VIEW
// to register dependencies for DROP CONSTRAINT RESTRICT enforcement. M0097-0036.
func collectViewPKDeps(sel *parser.SelectStmt, cat catalog.Catalog) []pkConstraintRef {
	seen := make(map[string]bool)
	var out []pkConstraintRef
	walkSelectPKDeps(sel, cat, &out, seen)
	return out
}

func walkSelectPKDeps(sel *parser.SelectStmt, cat catalog.Catalog, out *[]pkConstraintRef, seen map[string]bool) {
	if sel == nil {
		return
	}
	// UNION/INTERSECT/EXCEPT: this SelectStmt is the left branch; recurse into right.
	if sel.SetOp != nil {
		walkSelectPKDeps(sel.SetOp.Right, cat, out, seen)
	}
	// Main SELECT body with GROUP BY.
	if len(sel.GroupBy) > 0 {
		addGroupByPKDeps(sel, cat, out, seen)
	}
	// Recurse into WHERE subqueries.
	if sel.Where != nil {
		walkExprPKDeps(sel.Where, cat, out, seen)
	}
}

func walkExprPKDeps(e parser.Expr, cat catalog.Catalog, out *[]pkConstraintRef, seen map[string]bool) {
	if e == nil {
		return
	}
	switch v := e.(type) {
	case *parser.InExpr:
		if v.Subquery != nil {
			walkSelectPKDeps(v.Subquery, cat, out, seen)
		}
	case *parser.SubqueryExpr:
		walkSelectPKDeps(v.Inner, cat, out, seen)
	case *parser.ExistsExpr:
		walkSelectPKDeps(v.Subquery, cat, out, seen)
	}
}

func addGroupByPKDeps(sel *parser.SelectStmt, cat catalog.Catalog, out *[]pkConstraintRef, seen map[string]bool) {
	// Build the set of column names present in GROUP BY (lower-cased).
	groupBySet := make(map[string]bool)
	for _, gb := range sel.GroupBy {
		if cr, ok := gb.(*parser.ColumnRef); ok {
			col := strings.ToLower(cr.Column)
			groupBySet[col] = true
			if cr.Table != "" {
				groupBySet[strings.ToLower(cr.Table)+"."+col] = true
			}
		}
	}
	// For each table-valued FROM entry, check if its full PK is covered.
	for _, rv := range sel.From {
		if rv.Subquery != nil {
			walkSelectPKDeps(rv.Subquery, cat, out, seen)
			continue
		}
		if rv.Name == "" {
			continue
		}
		tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
		if !ok {
			continue
		}
		alias := strings.ToLower(rv.Alias)
		if alias == "" {
			alias = strings.ToLower(rv.Name)
		}
		tblNameLower := strings.ToLower(rv.Name)
		for _, idx := range cat.IndexesOnTable(tbl) {
			if !idx.Primary {
				continue
			}
			allCovered := true
			for _, col := range idx.Columns {
				c := strings.ToLower(col)
				if !groupBySet[c] && !groupBySet[alias+"."+c] && !groupBySet[tblNameLower+"."+c] {
					allCovered = false
					break
				}
			}
			if !allCovered {
				continue
			}
			key := fmt.Sprintf("%d:%s", tbl.OID, idx.Name)
			if !seen[key] {
				seen[key] = true
				*out = append(*out, pkConstraintRef{
					tableOID:       tbl.OID,
					constraintName: idx.Name,
					tableName:      tbl.Name,
				})
			}
		}
	}
}

func (o *ddlOp) createBTreeIndex(pos int, idxName parser.ObjectName, tbl *catalog.Table, columns []string, colExprs []parser.Expr, unique bool, primary bool) error {
	if len(columns) == 0 {
		return &ExecError{Code: "42601", Pos: pos, Message: "index must have at least one key column"}
	}
	cols := make([]*catalog.Column, len(columns))
	for i, name := range columns {
		if name == "" {
			// Expression column (e.g. lower(col)) — no catalog column to look up.
			// cols[i] remains nil; bulkBuildBTree skips expression columns when
			// there are no existing rows.
			continue
		}
		col, ok := o.ctx.Catalog.LookupColumn(tbl, name)
		if !ok {
			return &ExecError{Code: "42703", Pos: pos, Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name)}
		}
		if !isSupportedBTreeKeyType(col.Type.Name) {
			// Also accept user-defined enum types. M0097-0022.
			isEnum := false
			if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
				_, isEnum = im.LookupEnum(col.Type.Name)
			}
			if !isEnum {
				return &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("btree v0 only supports int4 / numeric keys, got %q", col.Type.Name)}
			}
		}
		cols[i] = col
	}
	idx, err := o.ctx.Catalog.CreateIndex(idxName, tbl, columns, unique, "btree", primary)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return &ExecError{Code: "42P07", Pos: pos, Message: err.Error()}
		}
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	// Store parsed expressions for expression-based index columns so the
	// planner and executor can evaluate them at conflict-detection time.
	if len(colExprs) > 0 {
		idx.ColExprs = make([]*parser.Expr, len(colExprs))
		for i, e := range colExprs {
			if e != nil {
				ec := e // take address of loop copy
				idx.ColExprs[i] = &ec
			}
		}
	}
	idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
	if err := o.bulkBuildBTree(idxRel, tbl, cols, unique, idxName.String(), pos); err != nil {
		_ = o.ctx.Catalog.DropIndex(idxName)
		o.ctx.Pool.InvalidateRel(idxRel)
		_ = o.ctx.Pool.Manager().DropRelation(idxRel)
		return err
	}
	// Record for rollback before heap sync (index is live in catalog now).
	if sess, ok := o.ctx.Session.(*BasicSession); ok {
		sess.RecordDDLCreate(DDLUndoEntry{Name: idxName, RelOID: idx.OID, IsIndex: true})
	}
	if catalogHeapSyncAvailable(o.ctx) {
		if syncErr := syncIndexToCatalogHeap(o.ctx, idx); syncErr != nil {
			return fmt.Errorf("DDL catalog sync: %w", syncErr)
		}
	}
	// M0079-0001: emit a CREATE INDEX WAL record so post-crash
	// recovery can reconstruct the in-memory catalog entry. The
	// pg_class heap row written above only carries OID + name +
	// relkind='i'; the column list, unique flag, primary flag,
	// and owning-table OID would otherwise be lost on a non-
	// graceful restart. The WAL record carries the full
	// metadata; replay happens in
	// `internal/initdb.replayIndexDDLRecords` after physical
	// recovery finishes.
	if o.ctx.Pool != nil {
		payload := wal.EncodeCreateIndex(wal.CreateIndexPayload{
			OID:      idx.OID,
			TableOID: tbl.OID,
			Schema:   idxName.Schema,
			Name:     idxName.Name,
			Method:   "btree",
			Columns:  append([]string(nil), columns...),
			Unique:   unique,
			Primary:  primary,
		})
		if _, err := o.ctx.Pool.LogChangeRecord(payload); err != nil {
			// Best-effort: roll back the in-memory mutation so
			// memory and (now-incomplete) WAL agree, mirroring
			// the CreateDatabase emit-then-rollback discipline at
			// internal/server/database_ddl.go:162-166. The on-
			// disk btree pages remain — they are harmless without
			// a catalog entry — but the next graceful SaveCatalog
			// won't capture this index either.
			_ = o.ctx.Catalog.DropIndex(idxName)
			o.ctx.Pool.InvalidateRel(idxRel)
			_ = o.ctx.Pool.Manager().DropRelation(idxRel)
			return &ExecError{Code: "XX000", Pos: pos, Message: fmt.Sprintf("create-index WAL append: %v", err)}
		}
	}
	return nil
}

// bulkBuildBTree collects all heap entries into memory, then calls
// btree.BulkCreate for a sort-then-build pass (M0047-0001). This replaces
// the old Create+backfillBTree flow and is significantly faster for large
// tables because it avoids per-key tree traversals and page splits.
func (o *ddlOp) bulkBuildBTree(idxRel storage.RelFileNode, tbl *catalog.Table, cols []*catalog.Column, unique bool, indexName string, pos int) error {
	entries, err := o.collectBTreeEntries(tbl, cols, unique, indexName, pos)
	if err != nil {
		return err
	}
	_, err = btree.BulkCreate(o.ctx.Pool, idxRel, entries)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	return nil
}

// collectBTreeEntries scans the heap, decodes visible tuples, encodes
// B-tree keys, enforces uniqueness, and returns the entries for bulk build.
func (o *ddlOp) collectBTreeEntries(tbl *catalog.Table, cols []*catalog.Column, unique bool, indexName string, pos int) ([]btree.BulkEntry, error) {
	rel := o.ctx.Catalog.RelFileNode(tbl)
	nBlocks, err := o.ctx.Pool.NBlocks(rel)
	if err != nil {
		return nil, &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	var entries []btree.BulkEntry
	var scanRow Row // M0054-0005c: reusable decode buffer (see comment below).
	// M0074-0004 / M0107-0001: per-page mctx for varchar / char / text payloads.
	// Reset on page advance; Release on return.
	// Datum lifetime ends at encodeBTreeKeyForColumn — the encoded
	// BulkEntry.Key is an explicit append-copy, so no Datum reference
	// outlives the Reset boundary.
	sctxDDL := mctx.Acquire(o.ctx.Mctx, mctx.KindExpr)
	defer sctxDDL.Release()
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return nil, &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
		page := slot.Page()
		if storage.IsNew(page) {
			o.ctx.Pool.Unpin(slot)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			o.ctx.Pool.Unpin(slot)
			return nil, &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
		for i := uint16(1); i <= uint16(count); i++ {
			tuple, err := storage.PageGetHeapTuple(page, i)
			if err != nil {
				continue
			}
			if tuple.Header.Xmin == storage.InvalidTransactionID || tuple.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			// M0054-0005c: reuse a per-CREATE-INDEX decode buffer to
			// avoid the per-row `make(Row, len(tbl.Columns))` that
			// the M0054-0004 idx-window pprof showed at 39 % cum.
			if scanRow == nil || len(scanRow) != len(tbl.Columns) {
				scanRow = make(Row, len(tbl.Columns))
			}
			// Single on-disk row format (PG-physical) since M0111-0002.
			decErr := decodePhysicalPGRowIntoMctx(scanRow, tbl.Columns, tuple.Data, sctxDDL)
			if decErr != nil {
				continue
			}
			row := scanRow
			// For enum columns, convert KindString labels to KindEnum (sort order)
			// so encodeBTreeKeyForColumn can use float64 encoding consistently
			// with the probe path. M0097-0022.
			if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
				for _, c := range cols {
					if c == nil {
						continue
					}
					et, isEnum := im.LookupEnum(c.Type.Name)
					if !isEnum {
						continue
					}
					idx := c.Ordinal
					if idx < 0 || idx >= len(row) {
						continue
					}
					if row[idx].Kind == KindString {
						label := row[idx].StringValue()
						for _, ev := range et.Values {
							if ev.Label == label {
								row[idx] = NewEnumDatum(ev.SortOrder, label)
								break
							}
						}
					}
				}
			}
			key, encErr := encodeCompositeBTreeKey(row, cols, pos)
			if encErr != nil {
				o.ctx.Pool.Unpin(slot)
				return nil, encErr
			}
			if key == nil {
				// All index columns are expression-based (cols[i]==nil); we
				// cannot encode an expression key from raw heap data here.
				// Skip this row — the index will be empty for expression-only
				// indexes, which suppresses spurious duplicate-key violations
				// during bulk build while expression evaluation is unsupported.
				continue
			}
			entries = append(entries, btree.BulkEntry{Key: append([]byte(nil), key...), Ptr: storage.ItemPointer{Block: blk, Offset: i}})
		}
		// M0074-0004 / M0107-0001: page boundary — reset sctx. All
		// Datums from this page were consumed by encodeBTreeKeyForColumn
		// and the resulting BulkEntry.Key is an explicit append-copy,
		// so no Datum reference outlives this point.
		sctxDDL.Reset()
		o.ctx.Pool.Unpin(slot)
	}
	// M0055-0006 Phase E: sorted-stream uniqueness check. The
	// pre-existing seen map was O(N) memory; the post-sort
	// adjacency walk is O(1) auxiliary space. BulkBuild sorts
	// the entries by key before inserting, so we sort here
	// (matching its convention) and walk adjacencies for the
	// unique check.
	if unique && len(entries) > 1 {
		sortBulkEntriesByKey(entries)
		for i := 1; i < len(entries); i++ {
			if bytesEqual(entries[i].Key, entries[i-1].Key) {
				return nil, &ExecError{Code: "23505", Pos: pos,
					Message: fmt.Sprintf("duplicate key value violates unique index %q", indexName)}
			}
		}
	}
	return entries, nil
}

// sortBulkEntriesByKey (M0055-0006 Phase E) sorts in place by
// byte-wise key order, the same order BulkBuild expects.
func sortBulkEntriesByKey(entries []btree.BulkEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return string(entries[i].Key) < string(entries[j].Key)
	})
}

// bytesEqual is a small no-allocation byte slice equality check.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (o *ddlOp) backfillBTree(tree *btree.BTree, tbl *catalog.Table, cols []*catalog.Column, unique bool, indexName string, pos int) error {
	rel := o.ctx.Catalog.RelFileNode(tbl)
	nBlocks, err := o.ctx.Pool.NBlocks(rel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	seen := map[string]struct{}{}
	var scanRow Row // M0054-0005c: reusable decode buffer.
	// M0074-0004 / M0107-0001: per-page mctx for varchar / char / text payloads.
	// Resulting key is copied by caller (`seen[string(key)]` and `tree.Insert`),
	// so Datums need not outlive the per-page Reset boundary.
	sctxDDL := mctx.Acquire(o.ctx.Mctx, mctx.KindExpr)
	defer sctxDDL.Release()
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
		page := slot.Page()
		if storage.IsNew(page) {
			o.ctx.Pool.Unpin(slot)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			o.ctx.Pool.Unpin(slot)
			return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
		for i := uint16(1); i <= uint16(count); i++ {
			tuple, err := storage.PageGetHeapTuple(page, i)
			if err != nil {
				// Corrupt or unsupported tuples are silently
				// skipped so backfill does not abort on
				// partial page writes or WAL-replay debris.
				continue
			}
			if tuple.Header.Xmin == storage.InvalidTransactionID || tuple.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			// M0054-0005c: reuse the decode buffer.
			if scanRow == nil || len(scanRow) != len(tbl.Columns) {
				scanRow = make(Row, len(tbl.Columns))
			}
			// Use the format-agnostic decoder (handles both goopg and PG
			// physical format) so rows written via COPY with LogCanonical
			// wired (EncodeRowPG) are correctly decoded.
			storedNatts := int(tuple.Header.Infomask2 & 0x07FF)
			if err := DecodeRowIntoMctxPGTuple(scanRow, tbl.Columns, tuple.Data, tuple.Bitmap, storedNatts, sctxDDL); err != nil {
				continue
			}
			row := scanRow
			key, encErr := encodeCompositeBTreeKey(row, cols, pos)
			if encErr != nil {
				o.ctx.Pool.Unpin(slot)
				return encErr
			}
			if unique {
				if _, exists := seen[string(key)]; exists {
					o.ctx.Pool.Unpin(slot)
					return &ExecError{Code: "23505", Pos: pos, Message: fmt.Sprintf("duplicate key value violates unique index %q", indexName)}
				}
				seen[string(key)] = struct{}{}
			}
			if err := tree.Insert(key, storage.ItemPointer{Block: blk, Offset: i}); err != nil {
				o.ctx.Pool.Unpin(slot)
				return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
			}
		}
		// M0074-0004 / M0107-0001: page boundary — reset sctx.
		sctxDDL.Reset()
		o.ctx.Pool.Unpin(slot)
	}
	return nil
}

// encodeCompositeBTreeKey builds a composite btree key by concatenating
// per-column encodings. Bytewise comparison of the result matches the
// SQL multi-column ordering semantics (compare col1, if equal compare
// col2, ...). Each column's encoding is self-terminating (fixed-length
// for int4/int8, terminator byte for numeric), so concatenation is
// unambiguous without a separator.
// nil entries in cols (expression index columns) are skipped — expression
// columns must be evaluated separately via encodeArbiterKey.
func encodeCompositeBTreeKey(row Row, cols []*catalog.Column, pos int) ([]byte, *ExecError) {
	var out []byte
	for _, col := range cols {
		if col == nil {
			// Expression column — cannot encode from raw row during bulk build.
			// Callers building expression indexes must handle this separately.
			continue
		}
		v := row[col.Ordinal]
		if v.IsNull() {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is null and cannot be indexed", col.Name)}
		}
		k, err := encodeBTreeKeyForColumn(v, col, pos)
		if err != nil {
			return nil, err
		}
		out = append(out, k...)
	}
	return out, nil
}

// encodeBTreeKeyForColumn turns a runtime Datum into the byte-form
// key that the B-tree stores. Shared between backfill and index-scan
// lookup so the encoding is symmetric — a probe key built from the
// query literal lands on the same bytes the backfill produced for the
// stored row.
//
// int4 path: KindInt range-checked into int32 then EncodeInt4.
// int8 path: KindInt directly into EncodeInt8 (full int64 range).
// numeric path: KindInt promoted to (int, scale=0); KindNumeric
// passes (mantissa, scale) straight through. Anything else surfaces
// 42804 — the analyzer should have caught it but the runtime guard
// makes the failure mode crisp.
func encodeBTreeKeyForColumn(v Datum, col *catalog.Column, pos int) ([]byte, *ExecError) {
	switch {
	case isInt4Type(col.Type.Name):
		const minInt32 = -1 << 31
		const maxInt32 = 1<<31 - 1
		if v.Kind != KindInt {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not integer at runtime", col.Name)}
		}
		if v.Int < minInt32 || v.Int > maxInt32 {
			return nil, &ExecError{Code: "22003", Pos: pos, Message: fmt.Sprintf("value %d out of int4 range for index key", v.Int)}
		}
		return btree.EncodeInt4(int32(v.Int)), nil
	case isInt8Type(col.Type.Name):
		if v.Kind != KindInt {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not integer at runtime", col.Name)}
		}
		return btree.EncodeInt8(v.Int), nil
	case isNumericType(col.Type.Name):
		switch v.Kind {
		case KindNumeric:
			return btree.EncodeNumericKey(numericMant(v), v.Scale), nil
		case KindInt:
			return btree.EncodeNumericKey(big.NewInt(v.Int), 0), nil
		}
		return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not numeric at runtime", col.Name)}
	case isVarcharType(col.Type.Name):
		if v.Kind != KindString {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not a string at runtime", col.Name)}
		}
		return btree.EncodeVarchar([]byte(v.StringValue())), nil
	case isCharType(col.Type.Name):
		if v.Kind != KindString {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not a string at runtime", col.Name)}
		}
		return btree.EncodeChar([]byte(v.StringValue())), nil
	case isTimestampType(col.Type.Name):
		if v.Kind != KindTime {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not a timestamp at runtime", col.Name)}
		}
		micros := v.TimeValue().Sub(pgEpoch).Microseconds()
		return btree.EncodeTimestamp(micros), nil
	case strings.ToLower(col.Type.Name) == "uuid":
		// uuid stored as canonical lowercase-dashes text; sort order matches byte order.
		if v.Kind != KindString {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not uuid at runtime", col.Name)}
		}
		return btree.EncodeVarchar([]byte(v.StringValue())), nil
	case strings.ToLower(col.Type.Name) == "text":
		// text type: encode as varchar bytes. M0096-0008.
		var s string
		switch v.Kind {
		case KindString:
			s = v.StringValue()
		case KindInt:
			s = fmt.Sprintf("%d", v.Int)
		default:
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not text at runtime", col.Name)}
		}
		return btree.EncodeVarchar([]byte(s)), nil
	case strings.ToLower(col.Type.Name) == "name":
		// name type: encode as varchar bytes (max 63 chars).
		if v.Kind != KindString {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not a string at runtime", col.Name)}
		}
		return btree.EncodeVarchar([]byte(v.StringValue())), nil
	case isFloat8Type(col.Type.Name):
		// float4/float8 stored as text; decode then re-encode sortably.
		var f float64
		switch v.Kind {
		case KindString:
			var err error
			f, err = strconv.ParseFloat(v.StringValue(), 64)
			if err != nil {
				return nil, &ExecError{Code: "22003", Pos: pos, Message: fmt.Sprintf("invalid float value %q for index key", v.StringValue())}
			}
		case KindInt:
			f = float64(v.Int)
		case KindNumeric:
			// Convert NUMERIC datum (mantissa * 10^-scale) to float64.
			m := numericMant(v)
			fv, _ := new(big.Float).SetInt(m).Float64()
			if v.Scale > 0 {
				fv /= math.Pow10(int(v.Scale))
			} else if v.Scale < 0 {
				fv *= math.Pow10(-int(v.Scale))
			}
			f = fv
		default:
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not float at runtime (kind %d)", col.Name, v.Kind)}
		}
		return btree.EncodeFloat8(f), nil
	}
	// Enum types: encode sort order as float64. M0097-0022.
	// The caller (collectBTreeEntries) pre-converts KindString enum labels to KindEnum
	// so both backfill and probe paths use the same float64 encoding.
	if v.Kind == KindEnum {
		return btree.EncodeFloat8(v.EnumSortOrder()), nil
	}
	return nil, &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("btree v0 cannot index column %q of type %q", col.Name, col.Type.Name)}
}

func (o *ddlOp) autoIndexName(tbl *catalog.Table, columns []string, suffix string) string {
	base := tbl.Name + "_" + strings.Join(columns, "_") + "_" + suffix
	candidate := base
	for i := 1; ; i++ {
		if _, exists := o.ctx.Catalog.LookupIndex(parser.ObjectName{Schema: tbl.Schema, Name: candidate}); !exists {
			return candidate
		}
		candidate = fmt.Sprintf("%s%d", base, i)
	}
}

func isInt4Type(name string) bool {
	switch strings.ToLower(name) {
	case "int4", "integer", "int", "serial": // serial maps to int4 (M0096-0006)
		return true
	default:
		return false
	}
}

func isInt8Type(name string) bool {
	switch strings.ToLower(name) {
	case "int8", "bigint", "bigserial": // bigserial maps to int8 (M0096-0006)
		return true
	default:
		return false
	}
}

func isNumericType(name string) bool {
	switch strings.ToLower(name) {
	case "numeric", "decimal":
		return true
	default:
		return false
	}
}

// isVarcharType returns true for the variable-length character types
// accepted by M0044-0001 B-tree key encoding.
func isVarcharType(name string) bool {
	switch strings.ToLower(name) {
	case "varchar", "character varying":
		return true
	default:
		return false
	}
}

// isCharType returns true for the fixed-length blank-padded character
// types accepted by M0044-0002 B-tree key encoding.
func isCharType(name string) bool {
	switch strings.ToLower(name) {
	case "char", "character", "bpchar":
		return true
	default:
		return false
	}
}

// isTimestampType returns true for timestamp (without time zone) types
// accepted by M0044-0003 B-tree key encoding.
func isTimestampType(name string) bool {
	switch strings.ToLower(name) {
	case "timestamp", "timestamp without time zone":
		return true
	default:
		return false
	}
}

// isFloat8Type returns true for float8 / float4 / real / double precision.
func isFloat8Type(name string) bool {
	switch strings.ToLower(name) {
	case "float8", "float4", "real", "double precision", "double", "float":
		return true
	default:
		return false
	}
}

func isTextType(name string) bool {
	return strings.ToLower(name) == "text"
}

func isNameType(name string) bool {
	return strings.ToLower(name) == "name"
}

// isSupportedBTreeKeyType lists the column types accepted by
// createSingleColumnBTreeIndex. int4 is the original v0 path; int8
// and numeric landed for HammerDB TPC-H compatibility. varchar landed
// in M0044-0001; char in M0044-0002; timestamp in M0044-0003.
func isSupportedBTreeKeyType(name string) bool {
	if isTextType(name) || isNameType(name) {
		return true
	}
	return isInt4Type(name) || isInt8Type(name) || isNumericType(name) ||
		isVarcharType(name) || isCharType(name) || isTimestampType(name) ||
		isFloat8Type(name) || strings.ToLower(name) == "uuid"
}

func (o *ddlOp) execTruncate(s *parser.TruncateStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "TRUNCATE requires Pool in Context"}
	}
	for _, name := range s.Names {
		tbl, ok := o.ctx.Catalog.LookupTable(name)
		if !ok {
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", name.String())}
		}
		idxs := o.ctx.Catalog.IndexesOnTable(tbl)
		rel := o.ctx.Catalog.RelFileNode(tbl)
		o.ctx.Pool.InvalidateRel(rel)
		if err := o.ctx.Pool.Manager().TruncateRelation(rel); err != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
		// M0090-0001: clear FSM + VM entries for the truncated
		// heap. Without this, the next INSERT consults the FSM,
		// gets a stale block number, calls Pin → ReadBlock and
		// errors with `short read at block` because nblocks=0
		// post-truncate.
		if o.ctx.FSM != nil {
			o.ctx.FSM.DropRelation(rel)
		}
		if o.ctx.VM != nil {
			o.ctx.VM.DropRelation(rel)
		}
		for _, idx := range idxs {
			idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
			o.ctx.Pool.InvalidateRel(idxRel)
			if err := o.ctx.Pool.Manager().TruncateRelation(idxRel); err != nil {
				return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
			}
			// FSM/VM are heap-relation maps; index relfiles
			// have no entries to clear. (Btrees track their
			// own free space inline.) Pair the FSM/VM cleanup
			// only with the heap rel above.
			if _, err := btree.Create(o.ctx.Pool, idxRel); err != nil {
				return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
			}
		}
	}
	return nil
}

// execCreateFunction registers a routine in the catalog's
// Routines() registry (M0015 Stage A step 3). Body is stored
// verbatim — the PL/pgSQL parser/interpreter that executes it
// arrives in step 4+. Stage A pins LANGUAGE to plpgsql or sql;
// other languages surface a typed diagnostic.
func (o *ddlOp) execCreateFunction(s *parser.CreateFunctionStmt) error {
	rs := o.ctx.Catalog.Routines()
	if rs == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "CREATE FUNCTION requires routine registry"}
	}
	lang := strings.ToLower(s.Language)
	if lang == "" {
		if s.BeginAtomic || s.IsReturnForm {
			lang = "sql" // BEGIN ATOMIC / RETURN form implies SQL language
		} else {
			return &ExecError{Code: "42P13", Pos: s.Pos(), Message: "CREATE FUNCTION requires a LANGUAGE clause"}
		}
	}
	if lang != "plpgsql" && lang != "sql" && lang != "c" {
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("language %q is not supported (Stage A: plpgsql, sql)", s.Language)}
	}
	// LANGUAGE C: store as a stub. When called, evalFuncCall detects lang=="c"
	// and returns a type-appropriate default value (true for bool, 0 for int, etc.).
	argTypes := make([]catalog.Type, len(s.Args))
	argNames := make([]string, len(s.Args))
	argModes := make([]string, len(s.Args))
	argDefaults := make([]string, len(s.Args))
	for i, a := range s.Args {
		argTypes[i] = catalog.Type{
			Name: strings.ToLower(a.Type.Name),
			Args: append([]int64(nil), a.Type.Args...),
		}
		argNames[i] = a.Name
		switch a.Mode {
		case parser.FuncArgIn:
			argModes[i] = "i"
		case parser.FuncArgOut:
			argModes[i] = "o"
		case parser.FuncArgInout:
			argModes[i] = "b"
		case parser.FuncArgVariadic:
			argModes[i] = "v"
		default:
			argModes[i] = "i"
		}
		if a.Default != nil {
			argDefaults[i] = defaultExprToSQL(a.Default)
		}
	}
	volatile := s.Volatile
	if volatile == "" {
		volatile = "v" // default: volatile
	}
	schema := s.Name.Schema
	if schema == "" {
		schema = currentWritableSchema(o.ctx)
	}
	r := &catalog.Routine{
		Schema:   schema,
		Name:     s.Name.Name,
		ArgNames: argNames,
		ArgTypes: argTypes,
		ArgModes: argModes,
		ArgDefaults: argDefaults,
		ReturnType: catalog.Type{
			Name: strings.ToLower(s.ReturnType.Name),
			Args: append([]int64(nil), s.ReturnType.Args...),
		},
		ReturnsSet:      s.ReturnsSet,
		Language:        lang,
		Body:            s.Body,
		BeginAtomic:     s.BeginAtomic,
		IsReturnForm:    s.IsReturnForm,
		Strict:          s.Strict,
		Volatile:        volatile,
		SecurityDefiner: s.SecurityDefiner,
		Leakproof:       s.Leakproof,
	}
	if _, err := rs.Create(r, s.OrReplace); err != nil {
		// ErrRoutineExists → SQLSTATE 42723 (duplicate function).
		if errors.Is(err, catalog.ErrRoutineExists) {
			return &ExecError{Code: "42723", Pos: s.Pos(), Message: err.Error()}
		}
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

// execAlterFunction updates mutable attributes of an existing function/procedure.
func (o *ddlOp) execAlterFunction(s *parser.AlterFunctionStmt) error {
	rs := o.ctx.Catalog.Routines()
	if rs == nil {
		return nil // no routine registry, silently skip
	}
	var argTypes []catalog.Type
	for _, a := range s.Args {
		argTypes = append(argTypes, catalog.Type{Name: strings.ToLower(a.Type.Name)})
	}
	var routines []*catalog.Routine
	if s.Args == nil {
		// No arg list: update all overloads
		routines = rs.LookupByName(s.Name)
	} else {
		r, ok := rs.Lookup(s.Name, argTypes)
		if ok && r != nil {
			routines = []*catalog.Routine{r}
		}
	}
	if len(routines) == 0 {
		kind := "function"
		if s.IsProcedure {
			kind = "procedure"
		}
		return &ExecError{Code: "42883", Pos: s.Pos(), Message: fmt.Sprintf("%s %s does not exist", kind, s.Name.Name)}
	}
	// Check that each matched routine is of the right kind.
	for _, r := range routines {
		if s.IsProcedure && !r.IsProcedure {
			// ALTER PROCEDURE on a function → "is not a procedure"
			argList := routineArgListStr(argTypes)
			return &ExecError{Code: "42809", Pos: s.Pos(),
				Message: fmt.Sprintf("%s%s is not a procedure", s.Name.Name, argList),
				Hint:    "To alter a function, use ALTER FUNCTION."}
		}
		if !s.IsProcedure && r.IsProcedure {
			// ALTER FUNCTION on a procedure → "is not a function"
			argList := routineArgListStr(argTypes)
			return &ExecError{Code: "42809", Pos: s.Pos(),
				Message: fmt.Sprintf("%s%s is not a function", s.Name.Name, argList),
				Hint:    "To alter a procedure, use ALTER PROCEDURE."}
		}
	}
	for _, r := range routines {
		if s.Volatile != nil {
			r.Volatile = *s.Volatile
		}
		if s.SecurityDefiner != nil {
			r.SecurityDefiner = *s.SecurityDefiner
		}
		if s.Leakproof != nil {
			r.Leakproof = *s.Leakproof
		}
		if s.Strict != nil {
			r.Strict = *s.Strict
		}
	}
	return nil
}

// execCreateProcedure registers a procedure in the catalog's
// Routines() registry (M0015 Stage B). Mirror of execCreateFunction
// but without RETURNS — procedures use OUT/INOUT params instead.
func (o *ddlOp) execCreateProcedure(s *parser.CreateProcedureStmt) error {
	rs := o.ctx.Catalog.Routines()
	if rs == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "CREATE PROCEDURE requires routine registry"}
	}
	lang := strings.ToLower(s.Language)
	if lang == "" {
		if s.BeginAtomic {
			lang = "sql" // BEGIN ATOMIC implies SQL language
		} else {
			return &ExecError{Code: "42P13", Pos: s.Pos(), Message: "CREATE PROCEDURE requires a LANGUAGE clause"}
		}
	}
	if lang != "plpgsql" && lang != "sql" && lang != "c" {
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("language %q is not supported (Stage B: plpgsql, sql)", s.Language)}
	}
	argTypes := make([]catalog.Type, len(s.Args))
	argNames := make([]string, len(s.Args))
	argModes := make([]string, len(s.Args))
	argDefaults := make([]string, len(s.Args))
	for i, a := range s.Args {
		argTypes[i] = catalog.Type{
			Name: strings.ToLower(a.Type.Name),
			Args: append([]int64(nil), a.Type.Args...),
		}
		argNames[i] = a.Name
		switch a.Mode {
		case parser.FuncArgIn:
			argModes[i] = "i"
		case parser.FuncArgOut:
			argModes[i] = "o"
		case parser.FuncArgInout:
			argModes[i] = "b"
		case parser.FuncArgVariadic:
			argModes[i] = "v"
		default:
			argModes[i] = "i"
		}
		if a.Default != nil {
			argDefaults[i] = defaultExprToSQL(a.Default)
		}
	}
	procSchema := s.Name.Schema
	if procSchema == "" {
		procSchema = currentWritableSchema(o.ctx)
	}
	r := &catalog.Routine{
		Schema:          procSchema,
		Name:            s.Name.Name,
		ArgNames:        argNames,
		ArgTypes:        argTypes,
		ArgModes:        argModes,
		ArgDefaults:     argDefaults,
		Language:        lang,
		Body:            s.Body,
		BeginAtomic:     s.BeginAtomic,
		IsProcedure:     true,
		SecurityDefiner: s.SecurityDefiner,
		Volatile:        "v", // procedures default to volatile
	}
	if _, err := rs.Create(r, s.OrReplace); err != nil {
		if errors.Is(err, catalog.ErrRoutineExists) {
			return &ExecError{Code: "42723", Pos: s.Pos(), Message: err.Error()}
		}
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

// execDropProcedure removes a procedure from the routine registry
// (mirrors execDropFunction).
func (o *ddlOp) execDropProcedure(s *parser.DropProcedureStmt) error {
	// Drop all names in the list (multi-name DROP PROCEDURE a, b, c).
	for _, extraName := range s.Names {
		s2 := *s
		s2.Name = extraName
		s2.Names = nil
		if err := o.execDropProcedure(&s2); err != nil {
			return err
		}
	}
	if s.IfExists && o.dropSchemaQualifiedNotice(s.Name) {
		return nil
	}
	// ObjKind is "procedure" or "routine" depending on which keyword was used.
	objKind := s.ObjKind
	if objKind == "" {
		objKind = "procedure"
	}
	rs := o.ctx.Catalog.Routines()
	if rs == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP PROCEDURE requires routine registry"}
	}
	// Verify the target exists and is a procedure BEFORE dropping.
	var found *catalog.Routine
	var argTypes []catalog.Type
	if s.Args == nil {
		cands := rs.LookupByName(s.Name)
		if len(cands) == 1 {
			found = cands[0]
		} else if len(cands) > 1 {
			if !s.IfExists {
				return &ExecError{
					Code:    "42725",
					Pos:     s.Pos(),
					Message: fmt.Sprintf("%s name \"%s\" is not unique", objKind, s.Name.Name),
					Hint:    fmt.Sprintf("Specify the argument list to select the %s unambiguously.", objKind),
				}
			}
		}
	} else {
		argTypes = make([]catalog.Type, len(s.Args))
		for i, a := range s.Args {
			argTypes[i] = catalog.Type{Name: strings.ToLower(a.Type.Name)}
		}
		found, _ = rs.Lookup(s.Name, argTypes)
	}
	if found == nil {
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("%s %s does not exist, skipping", objKind, s.Name.Name))
			return nil
		}
		// PG format: "procedure name() does not exist"
		argListStr := buildCallArgListStr(s.Args)
		return &ExecError{Code: "42883", Pos: s.Pos(),
			Message:  fmt.Sprintf("%s %s%s does not exist", objKind, s.Name.Name, argListStr),
			Hint:     "No procedure matches the given name and argument types. You might need to add explicit type casts.",
		}
	}
	// DROP PROCEDURE on a function should fail.
	if objKind == "procedure" && !found.IsProcedure {
		argListStr := routineArgListStr(argTypes)
		return &ExecError{Code: "42809", Pos: s.Pos(),
			Message: fmt.Sprintf("%s%s is not a procedure", s.Name.Name, argListStr),
			Hint:    "Use DROP FUNCTION to remove a function."}
	}
	// Now drop.
	var err error
	if s.Args == nil {
		err = rs.DropByName(s.Name)
	} else {
		dropArgTypes := make([]catalog.Type, len(s.Args))
		for i, a := range s.Args {
			dropArgTypes[i] = catalog.Type{
				Name: strings.ToLower(a.Type.Name),
				Args: append([]int64(nil), a.Type.Args...),
			}
		}
		err = rs.Drop(s.Name, dropArgTypes)
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, catalog.ErrRoutineNotFound) {
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("%s %s does not exist, skipping", objKind, s.Name.Name))
			return nil
		}
		return &ExecError{Code: "42883", Pos: s.Pos(), Message: err.Error()}
	}
	if errors.Is(err, catalog.ErrRoutineAmbiguous) {
		return &ExecError{
			Code:    "42725",
			Pos:     s.Pos(),
			Message: fmt.Sprintf("%s name \"%s\" is not unique", objKind, s.Name.Name),
			Hint:    fmt.Sprintf("Specify the argument list to select the %s unambiguously.", objKind),
		}
	}
	return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
}

// execDropFunction removes a routine from the registry. With an
// argument list, drops the matching overload; without it, drops
// the unique overload (and surfaces 42725 "ambiguous function"
// if more than one exists).
func (o *ddlOp) execDropFunction(s *parser.DropFunctionStmt) error {
	if s.IfExists && o.dropSchemaQualifiedNotice(s.Name) {
		return nil
	}
	// Check if any arg type is schema-qualified with a non-existent schema,
	// or is a completely unknown type. M0097-drop_if_exists.
	if s.IfExists && s.Args != nil {
		for _, a := range s.Args {
			argTypeName := a.Type.Name
			if a.Type.Schema != "" {
				if !o.ctx.Catalog.SchemaExists(a.Type.Schema) {
					o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", a.Type.Schema))
					return nil
				}
			} else if dropCompatCanonicalType(argTypeName) == "" {
				// Unknown non-schema-qualified type → type notice.
				displayName := strings.ToLower(argTypeName)
				if a.Type.IsArray {
					displayName += "[]"
				}
				o.ctx.AddNotice(fmt.Sprintf("type %q does not exist, skipping", displayName))
				return nil
			}
		}
	}
	rs := o.ctx.Catalog.Routines()
	if rs == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP FUNCTION requires routine registry"}
	}

	// Pre-check: look up the routine to verify it is a function, not a procedure.
	if s.Args != nil {
		argTypes := make([]catalog.Type, len(s.Args))
		for i, a := range s.Args {
			argTypes[i] = catalog.Type{Name: strings.ToLower(a.Type.Name)}
		}
		if found, ok := rs.Lookup(s.Name, argTypes); ok && found != nil && found.IsProcedure {
			// "X(type) is not a function" — matches PG error for DROP FUNCTION on a procedure.
			argListStr := routineArgListStr(argTypes)
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("%s%s is not a function, skipping", s.Name.Name, argListStr))
				return nil
			}
			return &ExecError{Code: "42809", Pos: s.Pos(),
				Message: fmt.Sprintf("%s%s is not a function", s.Name.Name, argListStr),
				Hint:    "To call a procedure, use CALL."}
		}
	}

	var err error
	if s.Args == nil {
		err = rs.DropByName(s.Name)
	} else {
		argTypes := make([]catalog.Type, len(s.Args))
		for i, a := range s.Args {
			argTypes[i] = catalog.Type{
				Name: strings.ToLower(a.Type.Name),
				Args: append([]int64(nil), a.Type.Args...),
			}
		}
		err = rs.Drop(s.Name, argTypes)
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, catalog.ErrRoutineNotFound) {
		// Build the function signature for error/notice messages.
		// PG format for IF EXISTS notice: "function name(pg_catalog.type,...) does not exist, skipping"
		// PG format for ERROR: "function name(canonical_type,...) does not exist"
		buildFuncSigNotice := func() string {
			sig := s.Name.Name + "("
			if s.Args != nil {
				parts := make([]string, len(s.Args))
				for i, a := range s.Args {
					canon := dropCompatCanonicalType(a.Type.Name)
					if canon == "" {
						canon = strings.ToLower(a.Type.Name)
					}
					parts[i] = dropCompatPGCatalogType(canon)
					if parts[i] == "" {
						parts[i] = canon
					}
					if a.Type.IsArray {
						parts[i] += "[]"
					}
				}
				sig += strings.Join(parts, ",")
			}
			return sig + ")"
		}
		buildFuncSigError := func() string {
			sig := s.Name.Name + "("
			if s.Args != nil {
				parts := make([]string, len(s.Args))
				for i, a := range s.Args {
					canon := dropCompatCanonicalType(a.Type.Name)
					if canon == "" {
						canon = strings.ToLower(a.Type.Name)
					}
					if a.Type.IsArray {
						canon += "[]"
					}
					parts[i] = canon
				}
				sig += strings.Join(parts, ", ")
			}
			return sig + ")"
		}
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("function %s does not exist, skipping", buildFuncSigNotice()))
			return nil
		}
		return &ExecError{Code: "42883", Pos: s.Pos(), Message: fmt.Sprintf("function %s does not exist", buildFuncSigError())}
	}
	if errors.Is(err, catalog.ErrRoutineAmbiguous) {
		return &ExecError{
			Code:    "42725",
			Pos:     s.Pos(),
			Message: fmt.Sprintf("function name \"%s\" is not unique", s.Name.Name),
			Hint:    "Specify the argument list to select the function unambiguously.",
		}
	}
	return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
}

// --- DDL catalog heap sync (M0030-0001 Phase 4) ---

// stampCatalogRows pins every page of rel and sets xmax on each live tuple
// (xmin≠0, xmax=0) for which match returns true. Used by
// deleteCatalogRowsForOID to physically mark rolled-back catalog rows as
// deleted so the startup scan in loadUserTablesFromHeap skips them.
func stampCatalogRows(ctx *Context, rel storage.RelFileNode, xmax storage.TransactionID, match func(data []byte) bool) {
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil || nBlocks == 0 {
		return
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		pinned, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			continue
		}
		pinned.Lock()
		page := pinned.Page()
		count, err := storage.PageLinePointerCount(page)
		if err == nil {
			for lineNo := uint16(1); lineNo <= uint16(count); lineNo++ {
				ht, err := storage.PageGetHeapTuple(page, lineNo)
				if err != nil {
					continue
				}
				if ht.Header.Xmin == storage.InvalidTransactionID {
					continue
				}
				if ht.Header.Xmax != storage.InvalidTransactionID {
					continue
				}
				if !match(ht.Data) {
					continue
				}
				if err := storage.PageSetHeapTupleXmax(page, lineNo, xmax); err != nil {
					continue
				}
				// Use MarkDirtyForceFPI to emit a fresh full-page image of
				// the post-stamp page. This overrides any stale FPI that
				// was captured before the row existed (e.g. the mirror
				// pg_class FPI taken at CREATE TABLE time for DBOid=5
				// does not contain the index row added later). Without
				// a fresh FPI, WAL replay would restore the pre-index
				// state and the subsequent xmax stamp (if WAL-logged)
				// would reference an invalid slot. Catalog xmax stamps
				// survive crash recovery via the DDL WAL replay path
				// (replayDatabaseDDLRecords / replayIndexDDLRecords);
				// the FPI here is only needed for the heap-based catalog
				// loader (loadUserTablesFromHeap / loadUserIndexesFromHeap).
				ctx.Pool.MarkDirtyForceFPI(pinned)
			}
		}
		pinned.Unlock()
		ctx.Pool.Unpin(pinned)
	}
}

// deleteCatalogRowsForOID stamps xmax on all live pg_class and pg_attribute
// rows for relOID. Called from rollbackDDLCreate so that after a crash+restart
// the startup catalog loader's xmax==0 filter skips the rolled-back rows.
func deleteCatalogRowsForOID(ctx *Context, dbOid uint32, relOID uint32, xmax storage.TransactionID) {
	classRel := storage.RelFileNode{
		DBOid:  dbOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	stampCatalogRows(ctx, classRel, xmax, func(data []byte) bool {
		// Try native format first, then PG18-canonical physical format.
		// syncTableToCatalogHeap writes physical rows; loadUserTablesFromHeap
		// also handles both, so we must mirror that here.
		row, err := catalog.DecodePGClassRow(data)
		if err != nil {
			row, err = catalog.DecodePGClassPhysicalRow(data)
		}
		return err == nil && row.OID == relOID
	})
	attrRel := storage.RelFileNode{
		DBOid:  dbOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	stampCatalogRows(ctx, attrRel, xmax, func(data []byte) bool {
		row, err := catalog.DecodePGAttributeRow(data)
		if err != nil {
			row, err = catalog.DecodePGAttributePhysicalRow(data)
		}
		return err == nil && row.AttRelID == relOID
	})
}

// syncEnumTypeToCatalogHeap writes a single pg_type row for an enum type into
// the pg_type heap (OID 1247). Called after RegisterEnum so that queries like
// `SELECT 1 FROM pg_type WHERE oid = enumtypid` return the expected rows.
// Mirrors to DBOid=5 (postgres db) so the seqScan (which reads from the
// session's DBOID) finds the row. M0097-0022 (enum → pg_type parity).
func syncEnumTypeToCatalogHeap(ctx *Context, et *catalog.EnumType) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	typeRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.TypeRelationId,
		Fork:   storage.MainFork,
	}
	if _, err := writeHeapRowCanonical(ctx, typeRel, pgTypeColumnsPG18(), buildUserPGTypeRowForEnum(et)); err != nil {
		return
	}
	// Mirror pg_type to the postgres database (DBOid=5) so sessions using
	// the postgres db can find the new type row via SeqScan. This mirrors
	// the pattern used by syncTableToCatalogHeap. M0097-0022.
	_ = mirrorCatalogRelToPostgresDB(ctx, catalog.TypeRelationId)
}

// deleteTypeFromCatalogHeap stamps xmax on the pg_type row for typeOID.
// Called by execDropType so dropped enums don't leave orphan pg_type rows.
// pg_type rows are written by syncEnumTypeToCatalogHeap using pgTypeColumnsPG18
// which has oid (4-byte LE uint32) as column 0. M0097-0022.
func deleteTypeFromCatalogHeap(ctx *Context, dbOid uint32, typeOID uint32, xmax storage.TransactionID) {
	typeRel := storage.RelFileNode{
		DBOid:  dbOid,
		RelOid: catalog.TypeRelationId,
		Fork:   storage.MainFork,
	}
	stampCatalogRows(ctx, typeRel, xmax, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		// Column 0 of pgTypeColumnsPG18 is oid (4-byte LE uint32) at offset 0.
		return binary.LittleEndian.Uint32(data[0:4]) == typeOID
	})
}

// catalogHeapSyncAvailable returns true when the M0030-0001 system catalog
// heap relfiles are accessible and DDL operations should write rows to
// pg_class / pg_attribute. The proxy indicator is whether pg_attribute is
// registered as a real (non-virtual) table — that is set by
// loadSystemCatalogsIfPresent in initdb.Open when the relfiles are present.
func catalogHeapSyncAvailable(ctx *Context) bool {
	if ctx == nil || ctx.Pool == nil {
		return false
	}
	pgAttr, ok := ctx.Catalog.LookupTable(
		parser.ObjectName{Schema: "pg_catalog", Name: "pg_attribute"})
	return ok && !pgAttr.Virtual
}

// catalogDBOids returns the set of database OIDs that hold catalog heap
// pages for user relations. syncTableToCatalogHeap writes to DefaultDBOid
// (1) and mirrors to the catalog's actual DBOID (e.g. 5 for "postgres").
// DROP TABLE / DROP INDEX must stamp xmax in both so loadUserTablesFromHeap
// (which reads from cat.DBOID()) does not re-resolve the dropped relation
// after restart. Deduplication ensures DefaultDBOid is never stamped twice.
func catalogDBOids(ctx *Context) []uint32 {
	oids := []uint32{catalog.DefaultDBOid}
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		if dbOid := im.DBOID(); dbOid != catalog.DefaultDBOid {
			oids = append(oids, dbOid)
		}
	}
	return oids
}

// namespaceOIDForSchema maps a schema name to its pg_catalog namespace OID.
func namespaceOIDForSchema(schema string) uint32 {
	if schema == "" || schema == "public" {
		return catalog.PublicNamespaceOID
	}
	return catalog.PGCatalogNamespaceOID
}

// syncTableToCatalogHeap writes one pg_class row and one pg_attribute row per
// column for tbl. Called by execCreateTable after in-memory catalog is updated.
//
// The rows are emitted in PG18-canonical layout (34-column pg_class,
// 25-column pg_attribute) so that a PostgreSQL 18 standby attaching to a
// goopg basebackup can deform the tuple with its native tupdesc and locate
// the user table by name. The historical goopg-native short-row layout
// blocked PG-standby parse-analyze at `relation public.bench_log does not
// exist` (M0106-0010 batched-36 loop 7 → loop 8).
func syncTableToCatalogHeap(ctx *Context, tbl *catalog.Table) error {
	classRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	classTID, err := writeHeapRowCanonical(ctx, classRel, pgClassColumnsPG18(), buildUserPGClassRow(tbl))
	if err != nil {
		return fmt.Errorf("pg_class: %w", err)
	}
	relnamespace := namespaceOIDForSchema(tbl.Schema)
	if err := insertPgClassOidIndexEntry(ctx, tbl.OID, classTID); err != nil {
		return fmt.Errorf("pg_class_oid_index: %w", err)
	}
	if err := insertPgClassRelnameNspIndexEntry(ctx, tbl.Name, relnamespace, classTID); err != nil {
		return fmt.Errorf("pg_class_relname_nsp_index: %w", err)
	}

	attrRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	for _, col := range tbl.Columns {
		attrTID, err := writeHeapRowCanonical(ctx, attrRel, pgAttributeColumnsPG18(), buildUserPGAttributeRow(tbl, col))
		if err != nil {
			return fmt.Errorf("pg_attribute col %q: %w", col.Name, err)
		}
		// attnum is the 1-based ordinal of the column in PG18.
		if err := insertPgAttributeRelidAttnumIndexEntry(ctx, tbl.OID, int16(col.Ordinal+1), attrTID); err != nil {
			return fmt.Errorf("pg_attribute_relid_attnum_index col %q: %w", col.Name, err)
		}
	}

	// Signal that this transaction wrote to nailed catalog relations (pg_class
	// and pg_attribute). The xact-marker hook in open.go reads this flag at
	// commit time to emit RecordKindXactCommitInval and unlink both
	// pg_internal.init files so the next backend reloads fresh descriptors.
	if ctx.TxnMgr != nil {
		ctx.TxnMgr.SetRelcacheInvalPending()
	}

	// M0106-0010 batched-41: mirror catalog pages updated by this DDL into
	// the `postgres` database (DBOid=5) so a PG18 standby connecting via
	// `dbname=postgres` reads the runtime-written pg_class /
	// pg_attribute rows. batched-41's multi-level descend + rebuild path
	// keeps the source layout consistent, so the mirror's page-by-page
	// copy now lands a well-formed btree in base/5/.
	if err := mirrorTouchedCatalogsToPostgresDB(ctx); err != nil {
		return fmt.Errorf("mirror catalogs to postgres db: %w", err)
	}

	return nil
}


// syncIndexToCatalogHeap writes a pg_class row and a pg_index row for idx.
// Called by createBTreeIndex after the full index build succeeds. The row
// layouts match PG18 canonical format so the index is visible to an attaching
// PG18 standby and is recoverable via heap scan on restart (M0113).
func syncIndexToCatalogHeap(ctx *Context, idx *catalog.Index) error {
	classRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	classTID, err := writeHeapRowCanonical(ctx, classRel, pgClassColumnsPG18(), buildUserPGClassRowForIndex(idx))
	if err != nil {
		return fmt.Errorf("pg_class for index: %w", err)
	}
	relnamespace := namespaceOIDForSchema(idx.Schema)
	if err := insertPgClassOidIndexEntry(ctx, idx.OID, classTID); err != nil {
		return fmt.Errorf("pg_class_oid_index for index: %w", err)
	}
	if err := insertPgClassRelnameNspIndexEntry(ctx, idx.Name, relnamespace, classTID); err != nil {
		return fmt.Errorf("pg_class_relname_nsp_index for index: %w", err)
	}

	// M0113: write pg_index row so the index is recoverable from the heap
	// on restart without relying on goopg-private WAL records.
	pgIndexRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.IndexRelationId,
		Fork:   storage.MainFork,
	}
	if _, err := writeHeapRowCanonical(ctx, pgIndexRel, pgIndexColumnsPG18(), buildUserPGIndexRow(idx)); err != nil {
		return fmt.Errorf("pg_index: %w", err)
	}

	// M0106-0011: CREATE INDEX (and ALTER TABLE ADD PRIMARY KEY) writes a
	// pg_class row for the new index relation. Flag the txn so the commit
	// hook emits RecordKindXactCommitInval and refreshes pg_internal.init;
	// without this the relcache on a PG18 standby misses the new index when
	// re-opening the parent table.
	if ctx.TxnMgr != nil {
		ctx.TxnMgr.SetRelcacheInvalPending()
	}
	// M0106-0010 batched-41: mirror catalog pages to DBOid=5 (the `postgres`
	// database) so a PG18 standby connecting via `dbname=postgres` reads
	// the runtime-written rows. Multi-level descent+rebuild in
	// `insertCanonicalSysBtreeLeaf` keeps the source layout consistent.
	if err := mirrorTouchedCatalogsToPostgresDB(ctx); err != nil {
		return fmt.Errorf("mirror catalogs to postgres db: %w", err)
	}
	return nil
}

// writeHeapRowCanonical writes a heap row to a catalog relation and, when
// ctx.LogCanonical is set, emits a PG-canonical XLOG_HEAP_INSERT WAL record
// (with full-page image) so a vanilla PG18 standby can replay the catalog
// insertion. The FPI approach ensures the standby can restore the page without
// parsing heap-tuple internals. M0106-0010 batched-32.
func writeHeapRowCanonical(ctx *Context, rel storage.RelFileNode, cols []catalog.Column, row Row) (storage.ItemPointer, error) {
	ptr, err := writeHeapRowReturningPG(ctx, rel, cols, row)
	if err != nil {
		return ptr, err
	}
	if ctx.LogCanonical == nil || ctx.Pool == nil {
		return ptr, nil
	}
	// Re-pin the page to capture a stable FPI after the insert.
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
	if err != nil {
		return ptr, fmt.Errorf("canonical WAL pin: %w", err)
	}
	page := make(storage.Page, storage.BlockSize)
	slot.Lock()
	copy(page, slot.Page())
	xid := uint32(ctx.Tx.XID)
	endLSN, emitErr := catalog.PgCanonicalHeapInsert(rel, ptr.Block, page, ptr.Offset, xid, ctx.LogCanonical)
	if emitErr == nil && endLSN != 0 {
		// M0106-0010 batched-42 H1: stamp pd_lsn so a PG18 standby's recovery
		// can detect "already applied" via the lsn comparison in
		// XLogReadBufferForRedo (xlogutils.c). Without this, the basebackup
		// snapshot's pd_lsn=0 page is unconditionally clobbered by the WAL
		// FPI on every replay pass.
		storage.MustHeader(slot.Page()).SetLSN(storage.LSN(endLSN))
		ctx.Pool.MarkDirty(slot)
	}
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	if emitErr != nil {
		return ptr, emitErr
	}
	return ptr, nil
}

// execCreateTrigger registers a trigger on a table. M0096-0012.
func (o *ddlOp) execCreateTrigger(s *parser.CreateTriggerStmt) error {
	tbl, ok := o.ctx.Catalog.LookupTable(s.Table)
	if !ok {
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Table.Name)}
	}
	trig := catalog.Trigger{
		Name:       s.Name,
		TableOID:   tbl.OID,
		Timing:     catalog.TriggerTiming(s.Timing),
		Events:     append([]string(nil), s.Events...),
		ForEachRow: s.ForEachRow,
		FuncName:   s.FuncName.Name,
		FuncSchema: s.FuncName.Schema,
	}
	// Remove any existing trigger with the same name on this table.
	filtered := tbl.Triggers[:0]
	for _, t := range tbl.Triggers {
		if t.Name != s.Name {
			filtered = append(filtered, t)
		}
	}
	tbl.Triggers = append(filtered, trig)
	return nil
}

// execDropTrigger removes a trigger from a table. M0096-0012.
func (o *ddlOp) execDropTrigger(s *parser.DropTriggerStmt) error {
	// Check schema-qualified table name for non-existent schema first.
	if sc := s.Table.Schema; sc != "" {
		switch strings.ToLower(sc) {
		case "public", "pg_catalog", "information_schema", "pg_toast":
		default:
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", sc))
				return nil
			}
			return &ExecError{Code: "3F000", Pos: s.Pos(), Message: fmt.Sprintf("schema %q does not exist", sc)}
		}
	}
	tbl, ok := o.ctx.Catalog.LookupTable(s.Table)
	if !ok {
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("relation %q does not exist, skipping", s.Table.Name))
			return nil
		}
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Table.Name)}
	}
	filtered := tbl.Triggers[:0]
	found := false
	for _, t := range tbl.Triggers {
		if t.Name == s.Name {
			found = true
			continue
		}
		filtered = append(filtered, t)
	}
	tbl.Triggers = filtered
	if !found {
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("trigger %q for relation %q does not exist, skipping", s.Name, s.Table.Name))
			return nil
		}
		return &ExecError{Code: "42704", Pos: s.Pos(),
			Message: fmt.Sprintf("trigger %q for table %q does not exist", s.Name, s.Table.Name)}
	}
	return nil
}

// execDropRule handles DROP RULE. Rules are not implemented; always reports
// "rule does not exist".
func (o *ddlOp) execDropRule(s *parser.DropRuleStmt) error {
	_, tblOk := o.ctx.Catalog.LookupTable(s.Table)
	if !tblOk {
		// When the table is schema-qualified and the schema is not a built-in,
		// PG emits "schema X does not exist" rather than a rule/relation error.
		if sc := s.Table.Schema; sc != "" {
			switch strings.ToLower(sc) {
			case "public", "pg_catalog", "information_schema", "pg_toast":
			default:
				if s.IfExists {
					o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", sc))
					return nil
				}
				return &ExecError{Code: "3F000", Pos: s.Pos(), Message: fmt.Sprintf("schema %q does not exist", sc)}
			}
		}
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("relation %q does not exist, skipping", s.Table.Name))
			return nil
		}
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Table.Name)}
	}
	if s.IfExists {
		o.ctx.AddNotice(fmt.Sprintf("rule %q for relation %q does not exist, skipping", s.Name, s.Table.Name))
		return nil
	}
	// Check compat registry: if the rule was registered via CREATE RULE (noop), succeed silently.
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		key := s.Name + "@" + s.Table.String()
		if im.DropCompatObject("rule", key) {
			// Also remove the table rule kind so future COPY DML sees no rule. M0097-0140.
			im.UnregisterTableRules(s.Table.Name)
			return nil
		}
	}
	return &ExecError{Code: "42704", Pos: s.Pos(),
		Message: fmt.Sprintf("rule %q for relation %q does not exist", s.Name, s.Table.Name)}
}

// execCreateSequence registers a new sequence in the process-global registry.
// M0097-0009.
func (o *ddlOp) execCreateSequence(s *parser.CreateSequenceStmt) error {
	name := s.Name.String()
	if LookupSequence(name) != nil && s.IfNotExists {
		return nil
	}
	// Determine defaults based on data type.
	var minV, maxV int64
	switch strings.ToLower(s.DataType) {
	case "smallint", "int2":
		minV, maxV = -32768, 32767
	case "integer", "int4", "int":
		minV, maxV = -2147483648, 2147483647
	default: // bigint / int8 (default)
		minV, maxV = -9223372036854775808, 9223372036854775807
	}
	// Apply explicit options.
	if s.MinValue != nil {
		minV = *s.MinValue
	}
	if s.MaxValue != nil {
		maxV = *s.MaxValue
	}
	increment := int64(1)
	if s.Increment != nil {
		increment = *s.Increment
	}
	// Default start values follow PostgreSQL convention (not the type minimum):
	// ascending (increment > 0) → start = 1; descending → start = -1.
	// M0097-0042.
	start := int64(1)
	if increment < 0 {
		start = int64(-1)
	}
	if s.Start != nil {
		start = *s.Start
	}
	cycle := s.Cycle
	RegisterSequence(name, start, increment, minV, maxV, cycle)
	return nil
}

// execDropCompat handles DROP SEQUENCE, DROP SCHEMA, DROP TYPE, DROP DOMAIN,
// and other object types not fully implemented in goopg v0. For IF EXISTS,
// truncateRelation stamps xmax on all visible tuples in the relation,
// effectively truncating it. Used by REFRESH MATERIALIZED VIEW. M0097-0013.
func truncateRelation(ctx *Context, rel storage.RelFileNode) error {
	if ctx.Pool == nil {
		return nil
	}
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return nil // empty, nothing to do
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		s.Lock()
		page := s.Page()
		if storage.IsNew(page) {
			s.Unlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			s.Unlock()
			ctx.Pool.Unpin(s)
			continue
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			tuple, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr) {
				continue
			}
			_ = storage.PageSetHeapTupleXmax(page, slot, ctx.Tx.XID)
		}
		s.Unlock()
		ctx.Pool.Unpin(s)
	}
	return nil
}

// execCreateMatView implements CREATE MATERIALIZED VIEW. M0097-0013.
// The matview is stored as a regular table (for heap storage) with
// IsMatView=true. WITH NO DATA skips the initial population.
func (o *ddlOp) execCreateMatView(s *parser.CreateMatViewStmt) error {
	if o.ctx.Pool == nil || o.ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "CREATE MATERIALIZED VIEW requires storage"}
	}
	// Plan the SELECT query to determine output columns.
	if err := analyzer.Analyze(s.Query, o.ctx.Catalog); err != nil {
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: err.Error()}
	}
	selectPlan, err := planner.Plan(s.Query, o.ctx.Catalog)
	if err != nil {
		return err
	}
	schema := selectPlan.Output()
	if schema == nil {
		return &ExecError{Code: "42P10", Pos: s.Pos(), Message: "materialized view query has no output columns"}
	}
	// Build column list from plan output schema.
	cols := make([]catalog.Column, len(schema))
	for i, sc := range schema {
		cols[i] = catalog.Column{Name: sc.Name, Type: sc.Type, Ordinal: i}
	}
	tbl, err := o.ctx.Catalog.CreateTable(s.Name, cols)
	if err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	tbl.IsMatView = true
	tbl.IsPopulated = !s.WithNoData
	// Store the SELECT AST as the view query (for REFRESH).
	tbl.View = s.Query
	if sess, ok := o.ctx.Session.(*BasicSession); ok {
		sess.RecordDDLCreate(DDLUndoEntry{Name: s.Name, RelOID: tbl.OID, IsIndex: false})
	}
	if catalogHeapSyncAvailable(o.ctx) {
		if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
			return fmt.Errorf("DDL catalog sync: %w", syncErr)
		}
	}
	// Populate immediately unless WITH NO DATA.
	if !s.WithNoData {
		if err := o.materializeView(tbl, selectPlan); err != nil {
			return err
		}
	}
	return nil
}

// materializeView executes the view's SELECT query and writes results
// to the materialized view heap. Used by both initial populate and REFRESH.
func (o *ddlOp) materializeView(tbl *catalog.Table, selectPlan planner.Node) error {
	op, err := Build(selectPlan)
	if err != nil {
		return err
	}
	if err := op.Open(o.ctx); err != nil {
		op.Close()
		return err
	}
	defer op.Close()
	rel := o.ctx.Catalog.RelFileNode(tbl)
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		row := slotRow(slot)
		if werr := writeHeapRow(o.ctx, rel, tbl.Columns, row); werr != nil {
			return werr
		}
	}
	return nil
}

// execRefreshMatView implements REFRESH MATERIALIZED VIEW. M0097-0013.
func (o *ddlOp) execRefreshMatView(s *parser.RefreshMatViewStmt) error {
	if o.ctx.Pool == nil || o.ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "REFRESH MATERIALIZED VIEW requires storage"}
	}
	tbl, ok := o.ctx.Catalog.LookupTable(s.Name)
	if !ok {
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("materialized view %q does not exist", s.Name.String())}
	}
	if !tbl.IsMatView {
		return &ExecError{Code: "42809", Pos: s.Pos(), Message: fmt.Sprintf("%q is not a materialized view", s.Name.String())}
	}
	// Re-plan the SELECT from the stored query.
	if err := analyzer.Analyze(tbl.View, o.ctx.Catalog); err != nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: fmt.Sprintf("refresh plan error: %v", err)}
	}
	selectPlan, err := planner.Plan(tbl.View, o.ctx.Catalog)
	if err != nil {
		return err
	}
	// Truncate existing data (stamp xmax on all rows).
	rel := o.ctx.Catalog.RelFileNode(tbl)
	if err := truncateRelation(o.ctx, rel); err != nil {
		return err
	}
	// Re-populate.
	if err := o.materializeView(tbl, selectPlan); err != nil {
		return err
	}
	tbl.IsPopulated = true
	return nil
}

// it emits a NOTICE; otherwise it silently succeeds (no catalog check). M0097-0008.
// dropSchemaQualifiedNotice emits "schema X does not exist, skipping" when
// the name is schema-qualified and the schema is not registered. Returns true
// if the notice was emitted (and the caller should skip this name).
func (o *ddlOp) dropSchemaQualifiedNotice(name parser.ObjectName) bool {
	if name.Schema == "" {
		return false
	}
	if !o.ctx.Catalog.SchemaExists(name.Schema) {
		o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", name.Schema))
		return true
	}
	return false
}

func (o *ddlOp) execDropCompat(s *parser.DropCompatStmt) error {
	objType := strings.ToLower(s.ObjType)

	// DROP SCHEMA [IF EXISTS] name [CASCADE|RESTRICT] — M0097-0020.
	// Find all user tables in the schema and cascade-drop them.
	if objType == "schema" {
		for _, name := range s.Names {
			schemaName := name.Name
			if name.Schema != "" {
				schemaName = name.Schema + "." + name.Name
			}
			// Use schema registry when available. If registered → exists (even if empty).
			schemaRegistered := o.ctx.Catalog.SchemaExists(schemaName)
			tables := o.ctx.Catalog.TablesInSchema(schemaName)
			if s.IfExists && !schemaRegistered {
				o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", schemaName))
				continue
			}
			if !s.IfExists && !schemaRegistered {
				return &ExecError{Code: "3F000", Pos: s.Pos(),
					Message: fmt.Sprintf("schema %q does not exist", schemaName)}
			}
			// Schema is registered; unregister it.
			o.ctx.Catalog.UnregisterSchema(schemaName)
			// Also drop functions/procedures in this schema.
			if s.Behavior == parser.DropCascade {
				rs := o.ctx.Catalog.Routines()
				if rs != nil {
					for _, r := range rs.List() {
						if strings.EqualFold(r.Schema, schemaName) {
							_ = rs.DropByName(parser.ObjectName{Schema: r.Schema, Name: r.Name})
						}
					}
				}
			}
			if s.Behavior == parser.DropCascade && len(tables) > 0 {
				// Sort table names for deterministic DETAIL output.
				sort.Slice(tables, func(i, j int) bool {
					return tables[i].String() < tables[j].String()
				})
				// Drop each table in the schema.
				for _, tbl := range tables {
					if err := o.ctx.Catalog.DropTable(tbl); err != nil {
						return &ExecError{Code: "42P01", Pos: s.Pos(), Message: err.Error()}
					}
				}
				// Emit NOTICE "drop cascades to N other objects" with DETAIL.
				// PostgreSQL's DETAIL field is multi-line; psql prints the first
				// line with "DETAIL:" and continuation lines as plain text.
				detailLines := make([]string, len(tables))
				for i, tbl := range tables {
					detailLines[i] = fmt.Sprintf("drop cascades to table %s", tbl.String())
				}
				detail := strings.Join(detailLines, "\n")
				o.ctx.AddNoticeWithDetail(
					fmt.Sprintf("drop cascades to %d other objects", len(tables)),
					detail,
				)
			}
		}
		return nil
	}

	// DROP USER / DROP ROLE / DROP GROUP — check catalog role registry.
	// M0097-drop_if_exists.
	if objType == "user" || objType == "role" || objType == "group" {
		for _, name := range s.Names {
			roleName := strings.ToLower(name.Name)
			exists := o.ctx.Catalog.RoleExists(roleName)
			if s.IfExists {
				if !exists {
					o.ctx.AddNotice(fmt.Sprintf("role %q does not exist, skipping", name.Name))
				} else {
					o.ctx.Catalog.UnregisterRole(roleName)
				}
			} else {
				if !exists {
					return &ExecError{Code: "42704", Pos: s.Pos(),
						Message: fmt.Sprintf("role %q does not exist", name.Name)}
				}
				o.ctx.Catalog.UnregisterRole(roleName)
			}
		}
		return nil
	}

	// DROP DATABASE [IF EXISTS] name — goopg is a single-database system; always reports not found.
	if objType == "database" {
		for _, name := range s.Names {
			dbName := name.Name
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("database %q does not exist, skipping", dbName))
			} else {
				return &ExecError{Code: "3D000", Pos: s.Pos(),
					Message: fmt.Sprintf("database %q does not exist", dbName)}
			}
		}
		return nil
	}
	// DROP CAST (fromType AS toType) — PG error: "cast from type X to type Y does not exist".
	// M0097-0071. Validate source/target types; generate PG-style error message.
	if objType == "cast" && len(s.CastTypes) == 2 {
		fromType := s.CastTypes[0]
		toType := s.CastTypes[1]
		// Check for schema-qualified types with non-existent schemas first.
		fromSchema := ""
		toSchema := ""
		if idx := strings.LastIndex(fromType, "."); idx >= 0 {
			fromSchema = fromType[:idx]
		}
		if idx := strings.LastIndex(toType, "."); idx >= 0 {
			toSchema = toType[:idx]
		}
		if fromSchema != "" && !o.ctx.Catalog.SchemaExists(fromSchema) {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", fromSchema))
				return nil
			}
			return &ExecError{Code: "3F000", Pos: s.Pos(),
				Message: fmt.Sprintf("schema %q does not exist", fromSchema)}
		}
		if toSchema != "" && !o.ctx.Catalog.SchemaExists(toSchema) {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", toSchema))
				return nil
			}
			return &ExecError{Code: "3F000", Pos: s.Pos(),
				Message: fmt.Sprintf("schema %q does not exist", toSchema)}
		}
		// Canonicalize type names (int → integer, etc.).
		fromCanon := dropCompatCanonicalType(fromType)
		toCanon := dropCompatCanonicalType(toType)
		// Validate unknown types only when they don't look like schema-qualified names.
		if fromSchema == "" && fromCanon == "" {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("type %q does not exist, skipping", fromType))
				return nil
			}
			return &ExecError{Code: "42704", Pos: s.Pos(),
				Message: fmt.Sprintf("type %q does not exist", fromType)}
		}
		if toSchema == "" && toCanon == "" {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("type %q does not exist, skipping", toType))
				return nil
			}
			return &ExecError{Code: "42704", Pos: s.Pos(),
				Message: fmt.Sprintf("type %q does not exist", toType)}
		}
		if fromCanon == "" {
			fromCanon = fromType
		}
		if toCanon == "" {
			toCanon = toType
		}
		msg := fmt.Sprintf("cast from type %s to type %s does not exist", fromCanon, toCanon)
		if s.IfExists {
			o.ctx.AddNotice(msg + ", skipping")
			return nil
		}
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: msg}
	}

	// DROP OPERATOR CLASS/FAMILY name USING method — M0097-0071.
	// PG validates the access method first; if unknown, always errors (even with IF EXISTS).
	// Known access methods: btree, hash, gist, gin, spgist, brin, heap.
	if objType == "operator class" || objType == "operator family" {
		// Schema-qualified name with non-existent schema takes priority. M0097-0071.
		if s.IfExists && len(s.Names) > 0 && o.dropSchemaQualifiedNotice(s.Names[0]) {
			return nil
		}
		knownAMs := map[string]bool{
			"btree": true, "hash": true, "gist": true,
			"gin": true, "spgist": true, "brin": true, "heap": true,
		}
		method := strings.ToLower(s.UsingMethod)
		if method != "" && !knownAMs[method] {
			// Unknown access method → ERROR regardless of IF EXISTS.
			return &ExecError{
				Code:    "42704",
				Pos:     s.Pos(),
				Message: fmt.Sprintf("access method %q does not exist", s.UsingMethod),
			}
		}
		// Known or missing access method.
		if len(s.Names) > 0 {
			name := s.Names[0]
			var msg string
			if method != "" {
				msg = fmt.Sprintf("%s %q does not exist for access method %q", s.ObjType, name.String(), s.UsingMethod)
			} else {
				msg = fmt.Sprintf("%s %q does not exist", s.ObjType, name.String())
			}
			if s.IfExists {
				o.ctx.AddNotice(msg + ", skipping")
				return nil
			}
			return &ExecError{Code: "42704", Pos: s.Pos(), Message: msg}
		}
		return nil
	}

	// Handle sequence drops against the in-memory registry. Must run before
	// the generic IF EXISTS block so IF EXISTS correctly checks existence. M0097-0038.
	if objType == "sequence" {
		for _, name := range s.Names {
			if o.dropSchemaQualifiedNotice(name) {
				continue
			}
			if !DropSequence(name.String()) {
				if s.IfExists {
					o.ctx.AddNotice(fmt.Sprintf("sequence %q does not exist, skipping", name.String()))
					continue
				}
				return &ExecError{
					Code:    "42704",
					Pos:     s.Pos(),
					Message: fmt.Sprintf("sequence %q does not exist", name.String()),
				}
			}
		}
		return nil
	}
	// Handle DROP MATERIALIZED VIEW via the catalog's DropView. M0097-0038.
	if objType == "materialized view" {
		for _, name := range s.Names {
			if s.IfExists && o.dropSchemaQualifiedNotice(name) {
				continue
			}
			if err := o.ctx.Catalog.DropView(name, s.IfExists); err != nil {
				return &ExecError{Code: "42P01", Pos: s.Pos(), Message: err.Error()}
			}
		}
		return nil
	}
	// DROP AGGREGATE: validate the arg type and emit PG-style error messages.
	// PG format: "aggregate name(canonicaltype) does not exist". M0097-regress.
	if objType == "aggregate" && len(s.Names) > 0 {
		aggName := s.Names[0]
		// Schema-qualified with non-existent schema.
		if aggName.Schema != "" && !o.ctx.Catalog.SchemaExists(aggName.Schema) {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", aggName.Schema))
				return nil
			}
			return &ExecError{Code: "3F000", Pos: s.Pos(),
				Message: fmt.Sprintf("schema %q does not exist", aggName.Schema)}
		}
		// Build PG-style arg list for the error message.
		argList := ""
		if len(s.ArgTypes) > 0 {
			argType := s.ArgTypes[0]
			if argType == "*" {
				// DROP AGGREGATE name(*) — non-IF EXISTS: "aggregate name(*) does not exist"
				// IF EXISTS notice: "aggregate name() does not exist, skipping"
				if s.IfExists {
					o.ctx.AddNotice(fmt.Sprintf("aggregate %s() does not exist, skipping", aggName.Name))
					return nil
				}
				return &ExecError{Code: "42883", Pos: s.Pos(),
					Message: fmt.Sprintf("aggregate %s(*) does not exist", aggName.Name)}
			} else if argType != "" {
				// Check if arg type schema doesn't exist.
				if idx := strings.LastIndex(argType, "."); idx >= 0 {
					argSchema := argType[:idx]
					if !o.ctx.Catalog.SchemaExists(argSchema) {
						if s.IfExists {
							o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", argSchema))
							return nil
						}
						return &ExecError{Code: "3F000", Pos: s.Pos(),
							Message: fmt.Sprintf("schema %q does not exist", argSchema)}
					}
				}
				canonical := dropCompatCanonicalType(argType)
				if canonical == "" {
					if s.IfExists {
						o.ctx.AddNotice(fmt.Sprintf("type %q does not exist, skipping", argType))
						return nil
					}
					return &ExecError{Code: "42704", Pos: s.Pos(),
						Message: fmt.Sprintf(`type %q does not exist`, argType)}
				}
				// NOTICE uses pg_catalog-qualified type; ERROR uses canonical unqualified name.
				// e.g. NOTICE: aggregate foo(pg_catalog.int4) vs ERROR: aggregate foo(integer).
				pgCanon := dropCompatPGCatalogType(canonical)
				argList = pgCanon // notice form
			}
		}
		// Compute canonical form for ERROR (unqualified: real, integer, etc.)
		// dropCompatCanonicalType handles bare names; strip pg_catalog. prefix first.
		argListErr := argList
		if argList != "" {
			bare := argList
			if strings.HasPrefix(bare, "pg_catalog.") {
				bare = bare[len("pg_catalog."):]
			}
			if can := dropCompatCanonicalType(bare); can != "" {
				argListErr = can
			}
		}
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("aggregate %s(%s) does not exist, skipping", aggName.Name, argList))
			return nil
		}
		return &ExecError{Code: "42883", Pos: s.Pos(),
			Message: fmt.Sprintf("aggregate %s(%s) does not exist", aggName.Name, argListErr)}
	}
	// DROP OPERATOR: validate types and emit PG-style error messages.
	// ArgTypes = [leftType, rightType]; "" means single-arg (missing second arg). M0097-regress.
	if objType == "operator" && len(s.Names) > 0 && len(s.ArgTypes) == 2 {
		opNameObj := s.Names[0]
		leftType := s.ArgTypes[0]
		rightType := s.ArgTypes[1]

		// Schema-qualified operator name with non-existent schema.
		if opNameObj.Schema != "" && !o.ctx.Catalog.SchemaExists(opNameObj.Schema) {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", opNameObj.Schema))
				return nil
			}
			return &ExecError{Code: "3F000", Pos: s.Pos(),
				Message: fmt.Sprintf("schema %q does not exist", opNameObj.Schema)}
		}

		// Helper: check if a type arg is schema-qualified with non-existent schema.
		checkTypeSchema := func(t string) (schemaErr bool) {
			if idx := strings.LastIndex(t, "."); idx >= 0 {
				argSchema := t[:idx]
				if !o.ctx.Catalog.SchemaExists(argSchema) {
					if s.IfExists {
						o.ctx.AddNotice(fmt.Sprintf("schema %q does not exist, skipping", argSchema))
					}
					return true
				}
			}
			return false
		}
		if checkTypeSchema(leftType) {
			if s.IfExists {
				return nil
			}
			// Unknown type in schema
			return &ExecError{Code: "42704", Pos: s.Pos(),
				Message: fmt.Sprintf(`type %q does not exist`, leftType)}
		}
		if checkTypeSchema(rightType) {
			if s.IfExists {
				return nil
			}
			return &ExecError{Code: "42704", Pos: s.Pos(),
				Message: fmt.Sprintf(`type %q does not exist`, rightType)}
		}

		// Single type argument (no comma) → PG reports "missing argument".
		if rightType == "" && leftType != "none" {
			return &ExecError{Code: "42P13", Pos: s.Pos(),
				Message: "missing argument",
				Hint:    "Use NONE to denote the missing argument of a unary operator."}
		}
		// Validate left type (non-schema-qualified).
		if leftType != "" && leftType != "none" {
			if dropCompatCanonicalType(leftType) == "" {
				if s.IfExists {
					// Unknown type in operator args → PG says "type X does not exist, skipping"
					o.ctx.AddNotice(fmt.Sprintf("type %q does not exist, skipping", leftType))
					return nil
				}
				return &ExecError{Code: "42704", Pos: s.Pos(),
					Message: fmt.Sprintf(`type %q does not exist`, leftType)}
			}
		}
		// Validate right type.
		if rightType != "" && rightType != "none" {
			if dropCompatCanonicalType(rightType) == "" {
				if s.IfExists {
					o.ctx.AddNotice(fmt.Sprintf("type %q does not exist, skipping", rightType))
					return nil
				}
				return &ExecError{Code: "42704", Pos: s.Pos(),
					Message: fmt.Sprintf(`type %q does not exist`, rightType)}
			}
		}
		// Both types valid → operator does not exist.
		leftCanon := dropCompatCanonicalType(leftType)
		rightCanon := dropCompatCanonicalType(rightType)
		if leftCanon == "" {
			leftCanon = leftType
		}
		if rightCanon == "" {
			rightCanon = rightType
		}
		opName := opNameObj.Name
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("operator %s does not exist, skipping", opName))
			return nil
		}
		// Check compat registry: if the operator was registered via CREATE OPERATOR (noop), succeed silently.
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			key := opName + "(" + leftCanon + "," + rightCanon + ")"
			if im.DropCompatObject("operator", key) {
				return nil
			}
		}
		return &ExecError{Code: "42883", Pos: s.Pos(),
			Message: fmt.Sprintf("operator does not exist: %s %s %s", leftCanon, opName, rightCanon)}
	}
	// Generic IF EXISTS / non-IF-EXISTS fallthrough for all remaining object types
	// (text search, collation, conversion, language, server, FDW, trigger, etc.).
	if s.IfExists {
		for _, name := range s.Names {
			if o.dropSchemaQualifiedNotice(name) {
				continue
			}
			o.ctx.AddNotice(fmt.Sprintf("%s %q does not exist, skipping", s.ObjType, name.String()))
		}
		return nil
	}
	// Without IF EXISTS, pretend the first name doesn't exist (generates error).
	// Check compat registry for noop-created objects that can be silently dropped.
	if len(s.Names) > 0 {
		switch objType {
		case "conversion", "text search configuration", "text search dictionary",
			"text search parser", "text search template":
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				if im.DropCompatObject(objType, s.Names[0].String()) {
					return nil
				}
			}
		}
		return &ExecError{
			Code:    "42704",
			Pos:     s.Pos(),
			Message: fmt.Sprintf("%s %q does not exist", s.ObjType, s.Names[0].String()),
		}
	}
	return nil
}

// execCompatNoop handles CompatNoopStmt (GRANT/REVOKE/COMMENT/CREATE RULE/etc).
// If the statement carries ObjType+ObjName, it registers the object in the compat
// registry so subsequent DROP statements can verify its existence. M0097-drop_if_exists.
func (o *ddlOp) execCompatNoop(s *parser.CompatNoopStmt) error {
	if s.ObjType == "" {
		return nil // pure no-op (GRANT, REVOKE, COMMENT, etc.)
	}
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	switch s.ObjType {
	case "operator":
		// Build the compat key as opName(leftCanon,rightCanon) to match DROP OPERATOR lookup.
		leftArg, rightArg := "", ""
		if len(s.ArgTypes) >= 2 {
			leftArg = s.ArgTypes[0]
			rightArg = s.ArgTypes[1]
		}
		leftCanon := dropCompatCanonicalType(leftArg)
		rightCanon := dropCompatCanonicalType(rightArg)
		if leftCanon == "" {
			leftCanon = leftArg
		}
		if rightCanon == "" {
			rightCanon = rightArg
		}
		key := s.ObjName.Name + "(" + leftCanon + "," + rightCanon + ")"
		im.RegisterCompatObject("operator", key)
	case "rule":
		// Key format must match DROP RULE: ruleName@tableName.
		if s.TableName.Name != "" {
			key := s.ObjName.Name + "@" + s.TableName.String()
			im.RegisterCompatObject("rule", key)
			// Also record the rule kind for COPY DML rule-specific errors. M0097-0140.
			if s.RuleKind != "" {
				im.RegisterTableRuleKind(s.TableName.Name, s.RuleKind)
			}
		}
	default:
		// conversion, text search dictionary/configuration/parser/template, etc.
		im.RegisterCompatObject(s.ObjType, s.ObjName.String())
	}
	return nil
}

// dropCompatCanonicalType maps PostgreSQL short type names to their canonical
// names used in error messages (e.g. "int4" → "integer", "float4" → "real").
// Returns "" for unknown/invalid type names.
// dropCompatPGCatalogType maps a canonical type name to pg_catalog-qualified form
// as PostgreSQL uses in DROP AGGREGATE/FUNCTION notices. M0097-drop_if_exists.
func dropCompatPGCatalogType(canonical string) string {
	switch canonical {
	case "integer":
		return "pg_catalog.int4"
	case "smallint":
		return "pg_catalog.int2"
	case "bigint":
		return "pg_catalog.int8"
	case "boolean":
		return "pg_catalog.bool"
	case "text":
		return "text" // text is not schema-qualified in PG routine signatures
	case "real":
		return "pg_catalog.float4"
	case "double precision":
		return "pg_catalog.float8"
	case "oid":
		return "pg_catalog.oid"
	case "name":
		return "pg_catalog.name"
	default:
		return canonical
	}
}

// dropCompatFuncTypeCanon maps type names to function-signature canonical form
// (e.g. int → int4) as PG uses in error messages. M0097-drop_if_exists.
func dropCompatFuncTypeCanon(typeName string) string {
	switch strings.ToLower(typeName) {
	case "int", "int4", "integer", "serial":
		return "int4"
	case "int2", "smallint", "smallserial":
		return "int2"
	case "int8", "bigint", "bigserial":
		return "int8"
	case "bool", "boolean":
		return "bool"
	case "float4", "real":
		return "float4"
	case "float8", "double precision":
		return "float8"
	}
	return dropCompatCanonicalType(typeName)
}

func dropCompatCanonicalType(typeName string) string {
	switch strings.ToLower(typeName) {
	case "int4", "integer", "int", "serial":
		return "integer"
	case "int2", "smallint", "smallserial":
		return "smallint"
	case "int8", "bigint", "bigserial":
		return "bigint"
	case "bool", "boolean":
		return "boolean"
	case "text":
		return "text"
	case "varchar", "character varying":
		return "character varying"
	case "char", "character", "bpchar":
		return "character"
	case "numeric", "decimal":
		return "numeric"
	case "date":
		return "date"
	case "time":
		return "time without time zone"
	case "timetz", "time with time zone":
		return "time with time zone"
	case "timestamp":
		return "timestamp without time zone"
	case "timestamptz", "timestamp with time zone":
		return "timestamp with time zone"
	case "interval":
		return "interval"
	case "float4", "real":
		return "real"
	case "float8", "double precision", "double":
		return "double precision"
	case "bytea":
		return "bytea"
	case "oid":
		return "oid"
	case "name":
		return "name"
	case "none":
		return "none"
	}
	return ""
}

// execCreateAggregate validates and registers a CREATE AGGREGATE statement.
// It rejects missing basetype, then registers the aggregate in the catalog
// so subsequent queries can invoke it. M0097-regress.
func (o *ddlOp) execCreateAggregate(s *parser.CreateAggregateStmt) error {
	if !s.HasBaseType {
		return &ExecError{Code: "42P13", Pos: s.Pos(),
			Message: "aggregate input type must be specified"}
	}
	// Validate finalfunc existence before registering. PostgreSQL looks up the
	// finalfunc in pg_proc and returns 42883 if no overload matches the stype.
	// We check our user-defined routines registry first, then fall back to the
	// built-in finalfunc allowlist (PostgreSQL internal functions we handle but
	// don't register in the catalog). M0097-0112.
	if s.FinalFunc != "" {
		ffLower := strings.ToLower(s.FinalFunc)
		found := knownBuiltinAggFinalFuncs[ffLower]
		if !found {
			if rs := o.ctx.Catalog.Routines(); rs != nil {
				if candidates := rs.LookupByName(parser.ObjectName{Name: s.FinalFunc}); len(candidates) > 0 {
					found = true
				}
			}
		}
		if !found {
			stypeName := aggregatePgTypeName(s.SType)
			return &ExecError{Code: "42883", Pos: s.Pos(),
				Message: fmt.Sprintf("function %s(%s) does not exist", ffLower, stypeName)}
		}
	}
	// Register in the catalog so the planner and executor can find it.
	agg := &catalog.UserAggregate{
		Name:        strings.ToLower(s.Name.Name),
		SType:       s.SType,
		SFunc:       s.SFunc,
		FinalFunc:   s.FinalFunc,
		CombineFunc: s.CombineFunc,
		InitCond:    s.InitCond,
		Variadic:    s.Variadic,
	}
	if s.HasBaseType && s.BaseType != "" && s.BaseType != "*" && s.BaseType != "any" {
		agg.ArgTypes = []string{s.BaseType}
	}
	// Check if the sfunc is STRICT by looking it up in the routine registry.
	// A strict sfunc skips NULL inputs (the aggregate state is unchanged on NULL rows). M0097-0035.
	if rs := o.ctx.Catalog.Routines(); rs != nil && s.SFunc != "" {
		sfuncName := parser.ObjectName{Name: s.SFunc}
		if candidates := rs.LookupByName(sfuncName); len(candidates) > 0 {
			agg.SFuncStrict = candidates[0].Strict
		}
	}
	o.ctx.Catalog.RegisterUserAggregate(agg)
	return nil
}

// execAlterAggregateRename handles ALTER AGGREGATE name(args) RENAME TO newname. M0097-0035.
func (o *ddlOp) execAlterAggregateRename(s *parser.AlterAggregateRenameStmt) error {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "ALTER AGGREGATE RENAME requires InMemory catalog"}
	}
	oldName := s.OldName.Name
	newName := s.NewName
	if !im.RenameUserAggregate(oldName, newName) {
		return &ExecError{Code: "42883", Pos: s.Pos(),
			Message: fmt.Sprintf("aggregate %s does not exist", oldName)}
	}
	return nil
}

// execCreateOpClass registers the hash extended support function for an
// operator class. Only the FUNCTION 2 entry is used; everything else is
// silently accepted. M0097-0027.
func (o *ddlOp) execCreateOpClass(s *parser.CreateOpClassStmt) error {
	if s.HashFuncName == "" {
		return nil // no hash support function — nothing to register
	}
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	im.RegisterOpClassHashFunc(s.Name, s.HashFuncName)
	return nil
}

// knownBuiltinAggFinalFuncs is the set of PostgreSQL built-in function names
// that may appear as a finalfunc in CREATE AGGREGATE. These are handled as
// special cases in the executor (finishAgg) and are not registered in the
// user-defined routines catalog, so the finalfunc validation skips them.
// M0097-0112.
var knownBuiltinAggFinalFuncs = map[string]bool{
	"int8_avg":              true,
	"numeric_avg":           true,
	"numeric_avg_combine":   true,
	"numeric_out":           true,
	"percentile_disc_final": true,
	"percentile_cont_final": true,
	"rank_final":            true,
	"dense_rank_final":      true,
	"cume_dist_final":       true,
	"percent_rank_final":    true,
	"mode_final":            true,
	"hypothetical_rank_common_final": true,
}

// aggregatePgTypeName maps an internal type name to the SQL type name used
// in PostgreSQL error messages (e.g. "int4" → "integer"). M0097-regress.
func aggregatePgTypeName(t string) string {
	switch strings.ToLower(t) {
	case "int4", "integer", "int":
		return "integer"
	case "int2", "smallint":
		return "smallint"
	case "int8", "bigint":
		return "bigint"
	case "float4", "real":
		return "real"
	case "float8", "double precision":
		return "double precision"
	case "bool", "boolean":
		return "boolean"
	case "text":
		return "text"
	case "numeric", "decimal":
		return "numeric"
	default:
		return t
	}
}

// ── Enum / Domain DDL executors ──────────────────────────────────────────────
// M0097-0017.

func (o *ddlOp) execCreateType(s *parser.CreateTypeStmt) error {
	cat, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	if !s.IsEnum {
		// Composite / range / base types — register the name so DROP TYPE can
		// succeed without error. If the type has a parsed field list, also
		// store the fields to enable PL/pgSQL field access/assignment.
		// M0097-0064, M0097-composite.
		if s.IsComposite && len(s.CompositeFields) > 0 {
			fields := make([]catalog.CompositeField, len(s.CompositeFields))
			for i, f := range s.CompositeFields {
				fields[i] = catalog.CompositeField{Name: f.Name, ColType: f.ColType}
			}
			cat.RegisterCompositeTypeWithFields(s.Name, fields)
		} else {
			cat.RegisterCompositeType(s.Name)
		}
		return nil
	}
	et, err := cat.RegisterEnum(s.Name, s.EnumValues)
	if err != nil {
		return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
	}
	// Write a pg_type heap row so `SELECT 1 FROM pg_type WHERE oid = enumtypid`
	// returns a match for the new type. M0097-0022.
	syncEnumTypeToCatalogHeap(o.ctx, et)
	// Track enum type creation so ROLLBACK can drop it.  M0097-0022.
	if o.ctx.Session != nil && o.ctx.Session.InExplicitTransaction() {
		if o.ctx.PendingCreatedEnums == nil {
			o.ctx.PendingCreatedEnums = make(map[string]bool)
		}
		o.ctx.PendingCreatedEnums[strings.ToLower(s.Name)] = true
	}
	return nil
}

func (o *ddlOp) execAlterType(s *parser.AlterTypeStmt) error {
	cat, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	// RENAME VALUE 'old' TO 'new' — M0097-0022.
	if s.RenameOldValue != "" {
		err := cat.RenameEnumValue(s.Name, s.RenameOldValue, s.RenameNewValue)
		if err == nil {
			return nil
		}
		switch e := err.(type) {
		case *catalog.EnumLabelNotFound:
			return &ExecError{Code: "42710", Pos: s.Pos(), Message: e.Error()}
		case *catalog.EnumLabelAlreadyExists:
			return &ExecError{Code: "42710", Pos: s.Pos(), Message: e.Error()}
		default:
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
		}
	}
	// RENAME TO new_name — rename the enum type. Track for ROLLBACK.  M0097-0022.
	if s.RenameTo != "" {
		err := cat.RenameEnum(s.Name, s.RenameTo)
		if err != nil {
			return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
		}
		if o.ctx.Session != nil && o.ctx.Session.InExplicitTransaction() {
			oldK := strings.ToLower(s.Name)
			newK := strings.ToLower(s.RenameTo)
			o.ctx.PendingEnumRenames = append(o.ctx.PendingEnumRenames, EnumRenameEntry{OldName: oldK, NewName: newK})
			// Update PendingCreatedEnums to track the current name so that
			// isUnsafeEnumValue and rollback undo both use the right key.
			// undoEnumDDLFromContext applies inverse renames to PendingCreatedEnums
			// during rollback so the final drop uses the (then-restored) old name.
			if o.ctx.PendingCreatedEnums[oldK] {
				delete(o.ctx.PendingCreatedEnums, oldK)
				o.ctx.PendingCreatedEnums[newK] = true
			}
		}
		return nil
	}
	if s.AddValue == "" {
		return nil // OWNER TO — no-op
	}
	skipped, err := cat.AddEnumValueResult(s.Name, s.AddValue, s.IfNotExists, s.Before, s.After)
	if err == nil {
		if skipped {
			// IF NOT EXISTS with existing label: emit NOTICE, continue.
			o.ctx.AddNotice(fmt.Sprintf("enum label %q already exists, skipping", s.AddValue))
		} else if o.ctx.Session != nil && o.ctx.Session.InExplicitTransaction() {
			// Value added inside an uncommitted transaction: mark as "unsafe" ONLY if
			// the type was NOT created in the same transaction (newly-created enum values
			// are immediately safe — only pre-existing-type additions need the guard).
			// M0097-0022.
			typeName := strings.ToLower(s.Name)
			if !o.ctx.PendingCreatedEnums[typeName] {
				if o.ctx.PendingEnumValues == nil {
					o.ctx.PendingEnumValues = make(map[string]map[string]bool)
				}
				if o.ctx.PendingEnumValues[typeName] == nil {
					o.ctx.PendingEnumValues[typeName] = make(map[string]bool)
				}
				o.ctx.PendingEnumValues[typeName][s.AddValue] = true
			}
		}
		return nil
	}
	switch e := err.(type) {
	case *catalog.EnumLabelTooLong:
		// PostgreSQL: "invalid enum label %q" DETAIL "Labels must be 63 bytes or less."
		return &ExecError{
			Code:    "22023",
			Pos:     s.Pos(),
			Message: fmt.Sprintf("invalid enum label %q", e.Label),
			Detail:  "Labels must be 63 bytes or less.",
		}
	case *catalog.EnumLabelNotFound:
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: e.Error()}
	default:
		return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
	}
}

func (o *ddlOp) execDropType(s *parser.DropTypeStmt) error {
	cat, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	for _, name := range s.Names {
		if s.IfExists && o.dropSchemaQualifiedNotice(name) {
			continue
		}
		n := name.Name
		// Try to drop as enum first, then as composite type. M0097-0064.
		// Stamp pg_type row before in-memory delete (need OID while et still exists).
		// MaterializeWriterXID ensures a real non-zero XID is used; DROP TYPE
		// does not call writeHeapRowReturningPG so the XID would otherwise
		// remain InvalidTransactionID (0), which is a no-op stamp. M0097-0022.
		if et, ok := cat.LookupEnum(n); ok && catalogHeapSyncAvailable(o.ctx) {
			if o.ctx.MaterializeWriterXID() == nil {
				deleteTypeFromCatalogHeap(o.ctx, catalog.DefaultDBOid, et.OID, o.ctx.Tx.XID)
				// Mirror pg_type to postgres db so the xmax stamp is visible via SeqScan.
				_ = mirrorCatalogRelToPostgresDB(o.ctx, catalog.TypeRelationId)
			}
		}
		enumErr := cat.DropEnum(n, s.Cascade)
		if enumErr == nil {
			continue // successfully dropped as enum
		}
		compErr := cat.DropCompositeType(n)
		if compErr == nil {
			continue // successfully dropped as composite type
		}
		// Neither enum nor composite — report error or IF EXISTS notice.
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("type %q does not exist, skipping", n))
			continue
		}
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("type %q does not exist", n)}
	}
	return nil
}

func (o *ddlOp) execCreateDomain(s *parser.CreateDomainStmt) error {
	cat, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	baseType := catalog.Type{Name: s.BaseType}
	_, err := cat.RegisterDomain(s.Name, baseType, s.NotNull, s.CheckInValues...)
	if err != nil {
		return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

func (o *ddlOp) execDropDomain(s *parser.DropDomainStmt) error {
	cat, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	for _, name := range s.Names {
		if s.IfExists && o.dropSchemaQualifiedNotice(name) {
			continue
		}
		// Always try to drop with ifExists=false first to detect not-found.
		// We need to emit a notice when IF EXISTS + not found.
		err := cat.DropDomain(name.Name, false, s.Cascade)
		if err == nil {
			continue // dropped successfully
		}
		if s.IfExists {
			// Domain does not exist; emit PG-style notice and continue.
			o.ctx.AddNotice(fmt.Sprintf("type %q does not exist, skipping", name.Name))
			continue
		}
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

// execLockTable handles LOCK [TABLE] rel [, ...] [IN mode MODE] [NOWAIT].
// It records the held locks in globalRelLockMgr so they appear in pg_locks.
// PostgreSQL transitively locks all relations that a view depends on, so
// locking a view also locks its underlying tables/views recursively. M0097.
// The locks are released when the session's transaction ends (execCommit/execRollback).
func (o *ddlOp) execLockTable(s *parser.LockTableStmt) error {
	sess := o.ctx.Session
	if sess == nil {
		return nil
	}
	dbOID := uint32(16384) // default DB OID
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		dbOID = im.DBOID()
	}
	// Resolve search_path schemas for unqualified names.
	searchSchemas := lockTableSearchSchemas(o.ctx)
	visited := make(map[uint32]bool)
	for _, rel := range s.Relations {
		tbl, ok := o.ctx.Catalog.LookupTable(parser.ObjectName{Schema: rel.Schema, Name: rel.Name})
		if !ok && rel.Schema == "" {
			for _, sc := range searchSchemas {
				tbl, ok = o.ctx.Catalog.LookupTable(parser.ObjectName{Schema: sc, Name: rel.Name})
				if ok {
					break
				}
			}
		}
		if !ok {
			return &ExecError{
				Code:    "42P01",
				Pos:     s.Pos(),
				Message: fmt.Sprintf("relation \"%s\" does not exist", rel.Name),
			}
		}
		lockRelationTransitively(sess, dbOID, s.Mode, tbl, o.ctx.Catalog, visited)
	}
	return nil
}

// lockRelationTransitively registers a lock on tbl and recursively locks:
// (a) for views: all tables/views referenced by the view's body;
// (b) for tables: all inheritance children.
// This mirrors PostgreSQL's behaviour for LOCK TABLE. M0097.
func lockRelationTransitively(sess Session, dbOID uint32, mode string, tbl *catalog.Table, cat catalog.Catalog, visited map[uint32]bool) {
	if visited[tbl.OID] {
		return
	}
	visited[tbl.OID] = true
	globalRelLockMgr.AddRelationLock(sess, dbOID, tbl.OID, mode)
	if tbl.View != nil {
		// Walk the view body to find referenced tables/views.
		refs := collectSelectTableRefs(tbl.View)
		for _, ref := range refs {
			dep, ok := cat.LookupTable(parser.ObjectName{Schema: ref.Schema, Name: ref.Name})
			if !ok && ref.Schema == "" {
				dep, ok = cat.LookupTable(parser.ObjectName{Schema: "public", Name: ref.Name})
			}
			if !ok {
				continue
			}
			lockRelationTransitively(sess, dbOID, mode, dep, cat, visited)
		}
	} else {
		// Lock inheritance children transitively (PostgreSQL also acquires
		// locks on all children when a parent table is locked).
		if im, ok := cat.(*catalog.InMemory); ok {
			for _, child := range im.InheritanceChildren(tbl.OID) {
				lockRelationTransitively(sess, dbOID, mode, child, cat, visited)
			}
		}
	}
}

// collectSelectTableRefs walks a SelectStmt and collects all table/view
// RangeVar references, including those inside subquery expressions. M0097.
func collectSelectTableRefs(sel *parser.SelectStmt) []parser.RangeVar {
	if sel == nil {
		return nil
	}
	var refs []parser.RangeVar
	// Walk From (flat list) and FromExprs (JOIN structure).
	collectFromRangeVars(sel.From, &refs)
	for _, fe := range sel.FromExprs {
		collectFromRangeVars([]parser.RangeVar{fe.Base}, &refs)
		for _, j := range fe.Joins {
			collectFromRangeVars([]parser.RangeVar{j.Right}, &refs)
		}
	}
	// Walk SELECT target expressions for scalar subqueries.
	for _, tgt := range sel.Targets {
		collectExprTableRefs(tgt.Expr, &refs)
	}
	// Walk WHERE for IN-subquery and correlated subqueries.
	collectExprTableRefs(sel.Where, &refs)
	// Walk set-operation right side (UNION/INTERSECT/EXCEPT).
	if sel.SetOp != nil && sel.SetOp.Right != nil {
		refs = append(refs, collectSelectTableRefs(sel.SetOp.Right)...)
	}
	return refs
}

// collectFromRangeVars recursively collects table/view RangeVars from a FROM list.
func collectFromRangeVars(from []parser.RangeVar, out *[]parser.RangeVar) {
	for _, rv := range from {
		if rv.Name != "" {
			*out = append(*out, rv)
		}
		if rv.Subquery != nil {
			refs := collectSelectTableRefs(rv.Subquery)
			*out = append(*out, refs...)
		}
	}
}

// collectExprTableRefs walks an expression tree for subquery table refs.
func collectExprTableRefs(expr parser.Expr, out *[]parser.RangeVar) {
	if expr == nil {
		return
	}
	switch e := expr.(type) {
	case *parser.SubqueryExpr:
		if e.Inner != nil {
			*out = append(*out, collectSelectTableRefs(e.Inner)...)
		}
	case *parser.InExpr:
		collectExprTableRefs(e.Operand, out)
		if e.Subquery != nil {
			*out = append(*out, collectSelectTableRefs(e.Subquery)...)
		}
		for _, v := range e.List {
			collectExprTableRefs(v, out)
		}
	case *parser.BinaryOp:
		collectExprTableRefs(e.Left, out)
		collectExprTableRefs(e.Right, out)
	case *parser.UnaryOp:
		collectExprTableRefs(e.Operand, out)
	case *parser.FuncCall:
		for _, a := range e.Args {
			collectExprTableRefs(a, out)
		}
		collectExprTableRefs(e.Filter, out)
	case *parser.CastExpr:
		collectExprTableRefs(e.Operand, out)
	case *parser.CaseExpr:
		collectExprTableRefs(e.Operand, out)
		for _, w := range e.Whens {
			collectExprTableRefs(w.When, out)
			collectExprTableRefs(w.Then, out)
		}
		collectExprTableRefs(e.Else, out)
	case *parser.ExistsExpr:
		if e.Subquery != nil {
			*out = append(*out, collectSelectTableRefs(e.Subquery)...)
		}
	}
}

// lockTableSearchSchemas returns the ordered list of schemas to search when
// resolving an unqualified LOCK TABLE target. Reads search_path GUC; falls
// back to "public" when not available.
func lockTableSearchSchemas(ctx *Context) []string {
	sp := `"$user", public`
	if ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("search_path"); ok {
			sp = v
		}
	}
	var result []string
	for _, raw := range strings.Split(sp, ",") {
		s := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), `"'`))
		if s == "" || s == "$user" {
			continue
		}
		result = append(result, s)
	}
	if len(result) == 0 {
		result = []string{"public"}
	}
	return result
}

// currentWritableSchema returns the first writable schema from the session's
// search_path GUC. Used by CREATE FUNCTION/PROCEDURE when no schema qualifier
// is provided. Defaults to "public" if search_path is empty or not set.
func currentWritableSchema(ctx *Context) string {
	if ctx == nil || ctx.GetSetting == nil {
		return "public"
	}
	searchPath, ok := ctx.GetSetting("search_path")
	if !ok || searchPath == "" {
		return "public"
	}
	for _, raw := range strings.Split(searchPath, ",") {
		s := strings.TrimSpace(raw)
		s = strings.Trim(s, `"'`)
		if s == "" || s == "$user" {
			continue
		}
		lc := strings.ToLower(s)
		if lc == "pg_catalog" || lc == "information_schema" {
			continue
		}
		return s
	}
	return "public"
}

// buildCallArgListStr returns "()" for no arg list or "(type, ...)" for typed args.
func buildCallArgListStr(args []parser.FunctionArg) string {
	if args == nil {
		return "()"
	}
	if len(args) == 0 {
		return "()"
	}
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = strings.ToLower(a.Type.Name)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
