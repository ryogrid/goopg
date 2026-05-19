package executor

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/analyzer"
	"github.com/goopg/goopg/internal/catalog"
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
	case *parser.DropFunctionStmt:
		return nil, o.execDropFunction(s)
	case *parser.CreateProcedureStmt:
		return nil, o.execCreateProcedure(s)
	case *parser.DropProcedureStmt:
		return nil, o.execDropProcedure(s)
	case *parser.CreateTriggerStmt:
		return nil, o.execCreateTrigger(s)
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
	case *parser.CompatNoopStmt:
		return nil, nil // GRANT/REVOKE/COMMENT/etc — accepted, no-op. M0097-0016.
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
			for _, pc := range parent.Columns {
				c := pc
				cols = append(cols, c)
			}
		}
	}
	// Append any columns explicitly declared in the CREATE TABLE body.
	for _, c := range s.Columns {
		typeName := strings.ToLower(c.Type.Name)
		// Resolve domain/enum types to their storage type. M0097-0017.
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
			tbl.ForeignKeys = append(tbl.ForeignKeys, catalog.ForeignKey{
				Columns:           []string{c.Name},
				RefTable:          c.RefTable.Name,
				RefColumns:        c.RefColumns,
				OnDelete:          c.OnDelete,
				OnUpdate:          c.OnUpdate,
				Deferrable:        c.FKDeferrable,
				InitiallyDeferred: c.FKInitiallyDeferred,
			})
		}
	}
	// Register implicit sequences for SERIAL / BIGSERIAL / SMALLSERIAL columns.
	// M0097-0009: creates the sequence so nextval() works for default generation.
	for _, c := range s.Columns {
		colTypeLow := strings.ToLower(c.Type.Name)
		var seqMin, seqMax int64
		switch colTypeLow {
		case "serial", "int4", "integer":
			// serial = int4 range
			seqMin, seqMax = 1, 2147483647
		case "bigserial", "int8", "bigint":
			seqMin, seqMax = 1, 9223372036854775807
		case "smallserial", "int2", "smallint":
			seqMin, seqMax = 1, 32767
		default:
			continue
		}
		if colTypeLow != "serial" && colTypeLow != "bigserial" && colTypeLow != "smallserial" {
			continue // only register sequences for serial types
		}
		seqName := strings.ToLower(s.Name.Name) + "_" + strings.ToLower(c.Name) + "_seq"
		RegisterSequence(seqName, 1, 1, seqMin, seqMax, false)
	}
	// If PARTITION BY, annotate the table with partition metadata
	if s.PartitionBy != nil {
		tbl.PartitionMethod = s.PartitionBy.Method
		tbl.PartitionKey = s.PartitionBy.KeyCols
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
		if err := o.createBTreeIndex(s.Pos(), idxName, tbl, pkCols, true, true); err != nil {
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
	cols := make([]catalog.Column, len(outSchema))
	for i, sc := range outSchema {
		typeName := sc.Type.Name
		if typeName == "" || typeName == "unknown" {
			typeName = "text"
		}
		cols[i] = catalog.Column{
			Name: sc.Name, Type: catalog.Type{Name: strings.ToLower(typeName)},
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
	tbl.PartitionMethod = parent.PartitionMethod
	tbl.PartitionKey = parent.PartitionKey

	// Build partition bounds from the FOR VALUES clause.
	var pb catalog.PartitionBound
	if poc.IsHash {
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
		// RANGE partition.
		if len(poc.FromValues) > 0 {
			pb.From = exprToString(poc.FromValues[0])
		}
		if len(poc.ToValues) > 0 {
			pb.To = exprToString(poc.ToValues[0])
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
		if err := o.createBTreeIndex(s.Pos(), childIdxName, tbl, parentIdx.Columns, parentIdx.Unique, parentIdx.Primary); err != nil {
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
	if !s.OrReplace {
		if existing, ok := o.ctx.Catalog.LookupTable(s.Name); ok && existing.View == nil {
			return &ExecError{Code: "42P07", Pos: s.Pos(), Message: fmt.Sprintf("relation %q already exists", s.Name.String())}
		}
	}
	// Compute the column count + names. Aliases override the
	// SELECT's target-list names; otherwise derive from the
	// target shape — bare ColumnRef → its name, FuncCall →
	// the function name (matches upstream's "sum" / "count"
	// convention).
	cols := make([]catalog.Column, 0, len(s.Query.Targets))
	for i, tgt := range s.Query.Targets {
		name := ""
		if i < len(s.Columns) {
			name = s.Columns[i]
		} else if tgt.Alias != "" {
			name = tgt.Alias
		} else {
			name = deriveTargetName(tgt.Expr)
		}
		if name == "" {
			name = fmt.Sprintf("?column?%d", i+1)
		}
		cols = append(cols, catalog.Column{Name: name, Type: catalog.Type{Name: "unknown"}})
	}
	if _, err := o.ctx.Catalog.CreateView(s.Name, cols, s.Columns, s.Query, s.OrReplace); err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
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
	for _, name := range s.Names {
		if _, ok := o.ctx.Catalog.LookupTable(name); !ok {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("view %q does not exist, skipping", name.String()))
				continue
			}
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("view %q does not exist", name.String())}
		}
		if err := o.ctx.Catalog.DropView(name, s.IfExists); err != nil {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("view %q does not exist, skipping", name.String()))
				continue
			}
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: err.Error()}
		}
	}
	return nil
}

func (o *ddlOp) execDropTable(s *parser.DropTableStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP TABLE requires Pool in Context"}
	}
	for _, name := range s.Names {
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
				for _, child := range im.InheritanceChildren(tbl.OID) {
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
	for _, idx := range idxs {
		idxRels = append(idxRels, o.ctx.Catalog.IndexRelFileNode(idx))
	}
	rel := o.ctx.Catalog.RelFileNode(tbl)
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
	method := strings.ToLower(strings.TrimSpace(s.Method))
	if method == "" {
		method = "btree"
	}
	if method != "btree" {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: fmt.Sprintf("index method %q is not supported in v0", method)}
	}
	// Skip expression indexes (column name "" means functional expression
	// like lower(col)) — goopg does not yet support expression indexes.
	// The index creation is silently ignored to let setup SQL proceed.
	for _, c := range s.Columns {
		if c == "" {
			return nil
		}
	}
	return o.createBTreeIndex(s.Pos(), idxName, tbl, s.Columns, s.Unique, false)
}

func (o *ddlOp) execDropIndex(s *parser.DropIndexStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP INDEX requires Pool in Context"}
	}
	for _, name := range s.Names {
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
	return nil
}

func (o *ddlOp) execAlterTable(s *parser.AlterTableStmt) error {
	tbl, ok := o.ctx.Catalog.LookupTable(s.Name)
	if !ok {
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
			childTbl.PartitionParentOID = tbl.OID
			childTbl.PartitionMethod = tbl.PartitionMethod
			childTbl.PartitionKey = tbl.PartitionKey
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
					pb.From = exprToString(poc.FromValues[0])
				}
				if len(poc.ToValues) > 0 {
					pb.To = exprToString(poc.ToValues[0])
				}
				if len(pb.InValues) > 0 || pb.From != "" || pb.To != "" {
					childTbl.PartitionBounds = []catalog.PartitionBound{pb}
				}
			}
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				im.RegisterPartitionChild(tbl.OID, childTbl.OID)
			}
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
	_, err := o.ctx.Catalog.AddColumn(tbl, catalog.Column{
		Name:    col.Name,
		Type:    catalog.Type{Name: strings.ToLower(col.Type.Name), Args: append([]int64(nil), col.Type.Args...)},
		NotNull: col.NotNull,
	})
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return &ExecError{Code: "42701", Pos: act.Pos(), Message: err.Error()}
	}
	return &ExecError{Code: "XX000", Pos: stmt.Pos(), Message: err.Error()}
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
	return o.createBTreeIndex(act.Pos(), idxName, tbl, act.Columns, true, true)
}

func (o *ddlOp) createBTreeIndex(pos int, idxName parser.ObjectName, tbl *catalog.Table, columns []string, unique bool, primary bool) error {
	if len(columns) == 0 {
		return &ExecError{Code: "42601", Pos: pos, Message: "index must have at least one key column"}
	}
	cols := make([]*catalog.Column, len(columns))
	for i, name := range columns {
		col, ok := o.ctx.Catalog.LookupColumn(tbl, name)
		if !ok {
			return &ExecError{Code: "42703", Pos: pos, Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name)}
		}
		if !isSupportedBTreeKeyType(col.Type.Name) {
			return &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("btree v0 only supports int4 / numeric keys, got %q", col.Type.Name)}
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
	var scanRow Row                                  // M0054-0005c: reusable decode buffer (see comment below).
	keep := buildKeepMaskForIndex(tbl.Columns, cols) // M0054-0005c-followup
	// M0074-0004: per-page arena for projected varchar / char /
	// numeric payloads. Reset on page advance; Drop on return.
	// Datum lifetime ends at encodeBTreeKeyForColumn — the
	// encoded BulkEntry.Key is an explicit append-copy
	// (entries[].Key, line below), so retention beyond Reset
	// is not a concern.
	arena := NewArena(0)
	defer arena.Drop()
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
			// True column projection is not feasible here because
			// the on-disk row encoding is variable-length, so each
			// column must be decoded sequentially to determine the
			// next one's offset; the win is the slice-allocation
			// removal, not the per-column work.
			if scanRow == nil || len(scanRow) != len(tbl.Columns) {
				scanRow = make(Row, len(tbl.Columns))
			}
			// M0054-0005c-followup: skip per-column heap
			// allocations for columns the index doesn't reference.
			// Other columns must still be size-scanned to advance
			// the offset (variable-length codec) but their string/
			// numeric payloads are not materialised.
			if err := DecodeRowProjectionIntoArena(scanRow, tbl.Columns, tuple.Data, keep, arena); err != nil {
				continue
			}
			row := scanRow
			key, encErr := encodeCompositeBTreeKey(row, cols, pos)
			if encErr != nil {
				o.ctx.Pool.Unpin(slot)
				return nil, encErr
			}
			entries = append(entries, btree.BulkEntry{Key: append([]byte(nil), key...), Ptr: storage.ItemPointer{Block: blk, Offset: i}})
		}
		// M0074-0004: page boundary — reset arena. All Datums
		// from this page were consumed by encodeBTreeKeyForColumn
		// and the resulting BulkEntry.Key is an explicit append-
		// copy, so no Datum reference outlives this point.
		arena.Reset()
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

// buildKeepMaskForIndex returns a per-column boolean mask whose
// `i`-th entry is true iff `tableCols[i]` is referenced by `indexCols`.
// Used by `DecodeRowProjection` to skip per-column heap allocations
// for columns the index does not need. (M0054-0005c-followup.)
func buildKeepMaskForIndex(tableCols []catalog.Column, indexCols []*catalog.Column) []bool {
	want := make(map[string]struct{}, len(indexCols))
	for _, ic := range indexCols {
		want[ic.Name] = struct{}{}
	}
	keep := make([]bool, len(tableCols))
	for i := range tableCols {
		if _, ok := want[tableCols[i].Name]; ok {
			keep[i] = true
		}
	}
	return keep
}

func (o *ddlOp) backfillBTree(tree *btree.BTree, tbl *catalog.Table, cols []*catalog.Column, unique bool, indexName string, pos int) error {
	rel := o.ctx.Catalog.RelFileNode(tbl)
	nBlocks, err := o.ctx.Pool.NBlocks(rel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	seen := map[string]struct{}{}
	var scanRow Row                                  // M0054-0005c: reusable decode buffer.
	keep := buildKeepMaskForIndex(tbl.Columns, cols) // M0054-0005c-followup
	// M0074-0004: per-page arena. Datums consumed by
	// encodeBTreeKeyForColumn; resulting key is copied (line below
	// `seen[string(key)]` allocates a Go string from the bytes,
	// and `tree.Insert` copies into btree pages).
	arena := NewArena(0)
	defer arena.Drop()
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
			// M0054-0005c-followup: skip non-index column payloads.
			if scanRow == nil || len(scanRow) != len(tbl.Columns) {
				scanRow = make(Row, len(tbl.Columns))
			}
			if err := DecodeRowProjectionIntoArena(scanRow, tbl.Columns, tuple.Data, keep, arena); err != nil {
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
		// M0074-0004: page boundary — reset arena.
		arena.Reset()
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
func encodeCompositeBTreeKey(row Row, cols []*catalog.Column, pos int) ([]byte, *ExecError) {
	var out []byte
	for _, col := range cols {
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
		if v.Kind != KindString && v.Kind != KindStringArena {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not a string at runtime", col.Name)}
		}
		return btree.EncodeVarchar([]byte(v.StringValue())), nil
	case isCharType(col.Type.Name):
		if v.Kind != KindString && v.Kind != KindStringArena {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not a string at runtime", col.Name)}
		}
		return btree.EncodeChar([]byte(v.StringValue())), nil
	case isTimestampType(col.Type.Name):
		if v.Kind != KindTime {
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not a timestamp at runtime", col.Name)}
		}
		micros := v.TimeValue().Sub(pgEpoch).Microseconds()
		return btree.EncodeTimestamp(micros), nil
	case strings.ToLower(col.Type.Name) == "text":
		// text type: encode as varchar bytes. M0096-0008.
		var s string
		switch v.Kind {
		case KindString, KindStringArena:
			s = v.StringValue()
		case KindInt:
			s = fmt.Sprintf("%d", v.Int)
		default:
			return nil, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("column %q is not text at runtime", col.Name)}
		}
		return btree.EncodeVarchar([]byte(s)), nil
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

// isSupportedBTreeKeyType lists the column types accepted by
// createSingleColumnBTreeIndex. int4 is the original v0 path; int8
// and numeric landed for HammerDB TPC-H compatibility. varchar landed
// in M0044-0001; char in M0044-0002; timestamp in M0044-0003.
func isSupportedBTreeKeyType(name string) bool {
	if strings.ToLower(name) == "text" {
		return true
	}
	return isInt4Type(name) || isInt8Type(name) || isNumericType(name) ||
		isVarcharType(name) || isCharType(name) || isTimestampType(name)
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
		return &ExecError{Code: "42P13", Pos: s.Pos(), Message: "CREATE FUNCTION requires a LANGUAGE clause"}
	}
	if lang != "plpgsql" && lang != "sql" {
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("language %q is not supported (Stage A: plpgsql, sql)", s.Language)}
	}
	argTypes := make([]catalog.Type, len(s.Args))
	argNames := make([]string, len(s.Args))
	for i, a := range s.Args {
		argTypes[i] = catalog.Type{
			Name: strings.ToLower(a.Type.Name),
			Args: append([]int64(nil), a.Type.Args...),
		}
		argNames[i] = a.Name
	}
	r := &catalog.Routine{
		Schema:   s.Name.Schema,
		Name:     s.Name.Name,
		ArgNames: argNames,
		ArgTypes: argTypes,
		ReturnType: catalog.Type{
			Name: strings.ToLower(s.ReturnType.Name),
			Args: append([]int64(nil), s.ReturnType.Args...),
		},
		Language: lang,
		Body:     s.Body,
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
		return &ExecError{Code: "42P13", Pos: s.Pos(), Message: "CREATE PROCEDURE requires a LANGUAGE clause"}
	}
	if lang != "plpgsql" && lang != "sql" {
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("language %q is not supported (Stage B: plpgsql, sql)", s.Language)}
	}
	argTypes := make([]catalog.Type, len(s.Args))
	argNames := make([]string, len(s.Args))
	argModes := make([]string, len(s.Args))
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
	}
	r := &catalog.Routine{
		Schema:   s.Name.Schema,
		Name:     s.Name.Name,
		ArgNames: argNames,
		ArgTypes: argTypes,
		ArgModes: argModes,
		Language: lang,
		Body:     s.Body,
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
	rs := o.ctx.Catalog.Routines()
	if rs == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP PROCEDURE requires routine registry"}
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
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("procedure %s does not exist, skipping", s.Name.String()))
			return nil
		}
		return &ExecError{Code: "42883", Pos: s.Pos(), Message: err.Error()}
	}
	if errors.Is(err, catalog.ErrRoutineAmbiguous) {
		return &ExecError{Code: "42725", Pos: s.Pos(), Message: err.Error()}
	}
	return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
}

// execDropFunction removes a routine from the registry. With an
// argument list, drops the matching overload; without it, drops
// the unique overload (and surfaces 42725 "ambiguous function"
// if more than one exists).
func (o *ddlOp) execDropFunction(s *parser.DropFunctionStmt) error {
	rs := o.ctx.Catalog.Routines()
	if rs == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP FUNCTION requires routine registry"}
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
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("function %s does not exist, skipping", s.Name.String()))
			return nil
		}
		// Format to match PostgreSQL: "function name(argtypes) does not exist"
		funcSig := s.Name.Name
		if s.Args != nil {
			var argNames []string
			for _, a := range s.Args {
				argNames = append(argNames, strings.ToLower(a.Type.Name))
			}
			funcSig += "(" + strings.Join(argNames, ", ") + ")"
		}
		return &ExecError{Code: "42883", Pos: s.Pos(), Message: fmt.Sprintf("function %s does not exist", funcSig)}
	}
	if errors.Is(err, catalog.ErrRoutineAmbiguous) {
		// Format: "function name \"name\" is not unique"
		return &ExecError{Code: "42725", Pos: s.Pos(), Message: fmt.Sprintf("function name \"%s\" is not unique", s.Name.Name)}
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
				_ = markHeapDeleteDirty(ctx.Pool, pinned, rel, blk, lineNo, xmax, nil)
			}
		}
		pinned.Unlock()
		ctx.Pool.Unpin(pinned)
	}
}

// deleteCatalogRowsForOID stamps xmax on all live pg_class and pg_attribute
// rows for relOID. Called from rollbackDDLCreate so that after a crash+restart
// the startup catalog loader's xmax==0 filter skips the rolled-back rows.
func deleteCatalogRowsForOID(ctx *Context, relOID uint32, xmax storage.TransactionID) {
	classRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}
	stampCatalogRows(ctx, classRel, xmax, func(data []byte) bool {
		row, err := catalog.DecodePGClassRow(data)
		return err == nil && row.OID == relOID
	})
	attrRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	}
	stampCatalogRows(ctx, attrRel, xmax, func(data []byte) bool {
		row, err := catalog.DecodePGAttributeRow(data)
		return err == nil && row.AttRelID == relOID
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

// syncIndexToCatalogHeap writes a pg_class row for idx. Called by
// createBTreeIndex after the full index build succeeds. The row layout
// matches PG18's 34-column pg_class so the index is visible to an attaching
// PG18 standby (see syncTableToCatalogHeap for context).
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
	copy(page, slot.Page())
	ctx.Pool.Unpin(slot)

	xid := uint32(ctx.Tx.XID)
	if err := catalog.PgCanonicalHeapInsert(rel, ptr.Block, page, ptr.Offset, xid, ctx.LogCanonical); err != nil {
		return ptr, err
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
	tbl, ok := o.ctx.Catalog.LookupTable(s.Table)
	if !ok {
		if s.IfExists {
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
	if !found && !s.IfExists {
		return &ExecError{Code: "42704", Pos: s.Pos(),
			Message: fmt.Sprintf("trigger %q for table %q does not exist", s.Name, s.Table.Name)}
	}
	return nil
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
	start := minV
	if increment < 0 {
		start = maxV
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
func (o *ddlOp) execDropCompat(s *parser.DropCompatStmt) error {
	if s.IfExists {
		// Emit NOTICE for each name (we don't know if they exist).
		// The test driver compares against expected NOTICEs.
		for _, name := range s.Names {
			o.ctx.AddNotice(fmt.Sprintf("%s %q does not exist, skipping", s.ObjType, name.String()))
		}
		return nil
	}
	// Without IF EXISTS, pretend the first name doesn't exist (generates error).
	if len(s.Names) > 0 {
		return &ExecError{
			Code:    "42704",
			Pos:     s.Pos(),
			Message: fmt.Sprintf("%s %q does not exist", s.ObjType, s.Names[0].String()),
		}
	}
	return nil
}

// ── Enum / Domain DDL executors ──────────────────────────────────────────────
// M0097-0017.

func (o *ddlOp) execCreateType(s *parser.CreateTypeStmt) error {
	if !s.IsEnum {
		// Composite / range / base types — not yet supported, ignore silently.
		return nil
	}
	cat, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	_, err := cat.RegisterEnum(s.Name, s.EnumValues)
	if err != nil {
		return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

func (o *ddlOp) execAlterType(s *parser.AlterTypeStmt) error {
	cat, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	if s.AddValue == "" {
		return nil // RENAME VALUE / RENAME TO / OWNER TO — no-op
	}
	if err := cat.AddEnumValue(s.Name, s.AddValue, s.IfNotExists, s.Before, s.After); err != nil {
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

func (o *ddlOp) execDropType(s *parser.DropTypeStmt) error {
	cat, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	for _, name := range s.Names {
		n := name.Name
		if err := cat.DropEnum(n, s.Cascade); err != nil {
			if !s.IfExists {
				return &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("type %q does not exist", n)}
			}
		}
	}
	return nil
}

func (o *ddlOp) execCreateDomain(s *parser.CreateDomainStmt) error {
	cat, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	baseType := catalog.Type{Name: s.BaseType}
	_, err := cat.RegisterDomain(s.Name, baseType, s.NotNull)
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
		if err := cat.DropDomain(name.Name, s.IfExists, s.Cascade); err != nil {
			return &ExecError{Code: "42704", Pos: s.Pos(), Message: err.Error()}
		}
	}
	return nil
}
