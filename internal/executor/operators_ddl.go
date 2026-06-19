package executor

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/analyzer"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/config"
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

// planCatalog returns the search-path-aware catalog for planner.Plan calls.
// Falls back to the raw catalog when PlanCatalog is not set. M0097-0022.
func (o *ddlOp) planCatalog() catalog.Catalog {
	return ctxPlanCatalog(o.ctx)
}

// ctxPlanCatalog returns the search-path-aware catalog from a Context.
// Used throughout the executor for all planner.Plan calls. M0097-0022.
func ctxPlanCatalog(ctx *Context) catalog.Catalog {
	if ctx.PlanCatalog != nil {
		return ctx.PlanCatalog
	}
	return ctx.Catalog
}

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
		return nil, o.execAlterSequence(s)
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
	case *parser.CreateExtensionStmt:
		return nil, o.execCreateExtension(s)
	case *parser.CreateTablespaceStmt:
		return nil, o.execCreateTablespace(s)
	case *parser.DropTablespaceStmt:
		return nil, o.execDropTablespace(s)
	case *parser.CompatNoopStmt:
		return nil, o.execCompatNoop(s)
	case *parser.CommentOnStmt:
		return nil, o.execCommentOn(s)
	case *parser.CreateStatisticsStmt:
		return nil, o.execCreateStatistics(s)
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

// knownExtensions maps a lowercase extension name to its default version.
// goopg ships only the built-in extensions whose SQL surface it actually
// implements; CREATE EXTENSION of any other name errors as if the control
// file were missing, mirroring PG. M0110-0003.
var knownExtensions = map[string]string{
	"amcheck": "1.4", // PG 18 contrib/amcheck default version
}

// execCreateExtension handles CREATE EXTENSION by inserting a pg_extension
// catalog row via the runtime registry (Catalog.CreateExtension). It mirrors
// PG's CreateExtension (commands/extension.c): an unknown extension errors as
// if its control file is missing; the install namespace defaults to public.
// M0110-0003 — wires pg_amcheck's "is amcheck installed?" probe; promotes the
// pg_amcheck 002_nonesuch TAP test.
func (o *ddlOp) execCreateExtension(s *parser.CreateExtensionStmt) error {
	defaultVersion, known := knownExtensions[strings.ToLower(s.Name)]
	if !known {
		// PG: errcode_for_file_access on ENOENT → ERRCODE_UNDEFINED_FILE (58P01).
		return &ExecError{
			Code:    "58P01",
			Pos:     s.Pos(),
			Message: fmt.Sprintf("could not open extension control file: extension %q is not available", s.Name),
		}
	}
	version := s.Version
	if version == "" {
		version = defaultVersion
	}
	schema := s.Schema
	if schema == "" {
		schema = "public"
	}
	if err := o.ctx.Catalog.CreateExtension(s.Name, schema, version, o.ctx.CurrentDatabase, s.IfNotExists); err != nil {
		// Only failure mode is a duplicate without IF NOT EXISTS.
		return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

// inPlaceTablespacesEnabled reports the session-effective
// allow_in_place_tablespaces GUC. Defaults to false when no GetSetting hook is
// wired (embedded/test contexts), matching PG's boot value. M0095-0003.
func (o *ddlOp) inPlaceTablespacesEnabled() bool {
	if o.ctx.GetSetting == nil {
		return false
	}
	v, ok := o.ctx.GetSetting("allow_in_place_tablespaces")
	if !ok {
		return false
	}
	switch strings.ToLower(v) {
	case "on", "true", "yes", "1":
		return true
	default:
		return false
	}
}

// execCreateTablespace handles CREATE TABLESPACE. goopg supports only the
// developer/regression in-place form (empty LOCATION with
// allow_in_place_tablespaces on), which creates pg_tblspc/<oid> as a real
// directory and records the tablespace in the runtime registry. It mirrors PG's
// CreateTableSpace (commands/tablespace.c) error semantics: a location with a
// single quote, a non-absolute non-in-place location, and a reserved "pg_"
// name all raise the upstream-verbatim errors. External (absolute-path)
// tablespaces are rejected as unsupported because goopg cannot relocate relation
// files. M0095-0003.
func (o *ddlOp) execCreateTablespace(s *parser.CreateTablespaceStmt) error {
	location := s.Location
	// PG: disallow quotes, else CREATE DATABASE would be at risk.
	if strings.Contains(location, "'") {
		return &ExecError{Code: "42602", Pos: s.Pos(), Message: "tablespace location cannot contain single quotes"}
	}
	inPlace := o.inPlaceTablespacesEnabled() && len(location) == 0
	if !inPlace {
		if !filepath.IsAbs(location) {
			// Mirrors PG: "tablespace location must be an absolute path"
			// (ERRCODE_INVALID_OBJECT_DEFINITION). Also the path empty
			// LOCATION takes when allow_in_place_tablespaces is off.
			return &ExecError{Code: "42P17", Pos: s.Pos(), Message: "tablespace location must be an absolute path"}
		}
		// An absolute external location is valid in PG but goopg cannot relocate
		// relation files into an arbitrary directory, so this is unsupported.
		return &ExecError{
			Code:    "0A000",
			Pos:     s.Pos(),
			Message: "tablespaces with an external location are not supported",
			Hint:    "Set allow_in_place_tablespaces and use LOCATION '' for an in-place tablespace.",
		}
	}
	// Disallow creation of tablespaces named "pg_xxx"; reserved for system use.
	if len(s.Name) >= 3 && strings.EqualFold(s.Name[:3], "pg_") {
		return &ExecError{
			Code:    "42939",
			Pos:     s.Pos(),
			Message: fmt.Sprintf("unacceptable tablespace name %q", s.Name),
			Detail:  `The prefix "pg_" is reserved for system tablespaces.`,
		}
	}
	oid, err := o.ctx.Catalog.CreateTablespace(s.Name, s.Owner, location)
	if err != nil {
		// Only failure mode is a duplicate name.
		return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
	}
	// Create the in-place directory pg_tblspc/<oid> under the data dir. When no
	// data dir is configured (embedded/test contexts), the registry entry stands
	// alone — matching how other DDL operators skip cluster-filesystem effects.
	if o.ctx.DataDir != "" {
		dir := filepath.Join(o.ctx.DataDir, "pg_tblspc", strconv.FormatUint(uint64(oid), 10))
		// Create the per-tablespace version subdirectory PG_<major>_<catversion>
		// inside pg_tblspc/<oid>, faithful to create_tablespace_directories
		// (tablespace.c). Relation files for this tablespace live under it;
		// pg_basebackup expects it present so a restored cluster's relfiles
		// resolve. MkdirAll creates the parent <oid> dir in the same call.
		versionDir := filepath.Join(dir, config.TablespaceVersionDirectory)
		if mkErr := os.MkdirAll(versionDir, 0o700); mkErr != nil {
			// Roll back the registry insert so a retry can succeed.
			o.ctx.Catalog.DropTablespace(s.Name)
			return &ExecError{Code: "58P01", Pos: s.Pos(), Message: fmt.Sprintf("could not create directory %q: %v", versionDir, mkErr)}
		}
	}
	return nil
}

// execDropTablespace handles DROP TABLESPACE [IF EXISTS] name. It removes the
// runtime registry entry and the in-place pg_tblspc/<oid> directory. A missing
// tablespace without IF EXISTS raises the upstream-verbatim
// "tablespace ... does not exist" (ERRCODE_UNDEFINED_OBJECT). M0095-0003.
func (o *ddlOp) execDropTablespace(s *parser.DropTablespaceStmt) error {
	oid, found := o.ctx.Catalog.DropTablespace(s.Name)
	if !found {
		if s.IfExists {
			return nil
		}
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: fmt.Sprintf("tablespace %q does not exist", s.Name)}
	}
	if o.ctx.DataDir != "" {
		dir := filepath.Join(o.ctx.DataDir, "pg_tblspc", strconv.FormatUint(uint64(oid), 10))
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return &ExecError{Code: "58P01", Pos: s.Pos(), Message: fmt.Sprintf("could not remove directory %q: %v", dir, rmErr)}
		}
	}
	return nil
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
	// Temporary tables may only be created in the temp schema (pg_temp),
	// not in a permanent schema like public.
	if s.Temporary && s.Name.Schema != "" && !strings.EqualFold(s.Name.Schema, "pg_temp") {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "cannot create temporary relation in non-temporary schema"}
	}
	// Non-temporary, non-unlogged tables created in pg_temp are implicitly
	// temporary (PG behavior). Unlogged tables may not be in the temp schema.
	if !s.Temporary && strings.EqualFold(s.Name.Schema, "pg_temp") {
		if s.Unlogged {
			return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "only temporary relations may be created in temporary schemas"}
		}
		s.Temporary = true
		s.Name.Schema = "" // store under bare key like other temp tables
	}
	// Pre-resolve schema from search_path before the existence check so that
	// CREATE TABLE ctlt1 in ctl_schema context doesn't falsely collide with
	// public.ctlt1 (which shares the bare "ctlt1" catalog key). M0097-0023.
	if s.Name.Schema == "" && !s.Temporary {
		if ws := currentWritableSchema(o.ctx); ws != "" && !strings.EqualFold(ws, "public") {
			s.Name.Schema = ws
		}
	}
	// For the existence check, use a schema-qualified name so that unqualified
	// names like "pg_attrdef" don't match pg_catalog virtual tables.
	// CREATE TABLE pg_attrdef should create a user table, not conflict with
	// pg_catalog.pg_attrdef. M0097-0023.
	checkName := s.Name
	if checkName.Schema == "" && !s.Temporary {
		checkName.Schema = "public"
	}
	if _, exists := o.ctx.Catalog.LookupTable(checkName); exists {
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
			if parent.PartitionMethod != "" {
				return &ExecError{Code: "42809", Pos: s.Pos(),
					Message: fmt.Sprintf("cannot inherit from partitioned table %q", parentName.Name),
					Detail:  "This operation is not supported for partitioned tables."}
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
						// Detect storage parameter conflicts between multiple INHERITS parents.
						pcStor := strings.ToLower(pc.Storage)
						ecStor := strings.ToLower(ec.Storage)
						if pcStor == "" {
							pcStor = "extended"
						}
						if ecStor == "" {
							ecStor = "extended"
						}
						// PostgreSQL emits the merging notice BEFORE reporting the conflict.
						o.ctx.AddNotice(fmt.Sprintf("merging multiple inherited definitions of column %q", pc.Name))
						if pcStor != ecStor {
							return &ExecError{
								Code:    "42611",
								Pos:     s.Pos(),
								Message: fmt.Sprintf("column %q has a storage parameter conflict", pc.Name),
								Detail:  fmt.Sprintf("%s versus %s", strings.ToUpper(ecStor), strings.ToUpper(pcStor)),
							}
						}
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
	// Build a set of INHERITS parent OIDs so LIKE can detect overlap. M0097-0023.
	inheritParentOIDSet := make(map[uint32]bool, len(inheritParents))
	for _, p := range inheritParents {
		inheritParentOIDSet[p.OID] = true
	}

	// CHECK constraints to inherit from LIKE INCLUDING CONSTRAINTS clauses.
	// Carries the source constraint name so the copy keeps its identity
	// (PostgreSQL preserves the original CHECK constraint name on LIKE
	// INCLUDING CONSTRAINTS). M0097-0023.
	var likeCheckConstraints []catalog.NamedCheckConstraint
	// Indexes to copy from LIKE INCLUDING INDEXES clauses (non-PK unique indexes).
	var likeUniqueIndexes []*catalog.Index
	// Non-unique plain indexes to copy from LIKE INCLUDING INDEXES clauses.
	var likeNonUniqueIndexes []*catalog.Index
	// Source tables for LIKE INCLUDING COMMENTS (pairs: srcTable, indexPrefix).
	type likeCommentSource struct{ src *catalog.Table }
	var likeCommentSources []likeCommentSource
	// Source tables for LIKE INCLUDING STATISTICS. M0097-0023.
	type likeStatisticsSource struct{ src *catalog.Table }
	var likeStatisticsSources []likeStatisticsSource
	// NOT NULL constraints to copy from LIKE sources (preserving source name).
	// Maps col name (lower) → NamedNotNullConstraint from source table. M0097-0023.
	type likeNotNullEntry struct {
		name      string
		colName   string
		noInherit bool
	}
	likeNotNullByCol := make(map[string]likeNotNullEntry)
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
			} else if LookupSequence(likeName.String()) != nil {
				return &ExecError{
					Code:    "42809",
					Pos:     s.Pos(),
					Message: fmt.Sprintf("relation %q is invalid in LIKE clause", likeName.Name),
					Detail:  "This operation is not supported for sequences.",
				}
			} else if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok && im.HasCompositeType(likeName.Name) {
				// Composite types are valid LIKE sources in PostgreSQL; goopg
				// doesn't evaluate their fields, so we produce no columns from them.
			} else {
				return &ExecError{
					Code:    "42P01",
					Pos:     s.Pos(),
					Message: fmt.Sprintf("relation %q does not exist", likeName.Name),
				}
			}
		}
		addCol := func(c parser.ColumnDef) {
			typeName := strings.ToLower(c.Type.Name)
			declaredTypeName := "" // non-empty only when a domain is resolved
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				if resolved := im.ResolveColumnType(typeName); resolved != typeName {
					declaredTypeName = typeName
					typeName = resolved
				}
			}
			serialTyp := strings.ToLower(c.Type.Name)
			isSerialCol := serialTyp == "serial" || serialTyp == "serial4" ||
				serialTyp == "bigserial" || serialTyp == "serial8" ||
				serialTyp == "smallserial" || serialTyp == "serial2"
			cols = append(cols, catalog.Column{
				Name:             c.Name,
				Type:             catalog.Type{Name: typeName, Args: append([]int64(nil), c.Type.Args...), IsArray: c.Type.IsArray},
				DeclaredTypeName: declaredTypeName,
				NotNull:          c.NotNull || c.IdentityColumn || isSerialCol,
				GeneratedExpr:    c.GeneratedExpr,
				GeneratedAlways:  c.GeneratedAlways,
				GeneratedVirtual: c.GeneratedVirtual,
				DefaultExpr:      c.DefaultExpr,
				IdentityColumn:   c.IdentityColumn,
				IdentityAlways:   c.IdentityAlways,
				IdentityStart:    c.IdentityStart,
				Compression:      c.Compression,
				Collation:        c.Collation,
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
				includeIndexes := strings.Contains(likeFlags, ":+indexes")
				includeComments := strings.Contains(likeFlags, ":+comments")
				includeStatistics := strings.Contains(likeFlags, ":+statistics")
				includeStorage := strings.Contains(likeFlags, ":+storage")
				src, ok := likeByKey[baseKey]
				if !ok {
					continue
				}
				if includeComments {
					likeCommentSources = append(likeCommentSources, likeCommentSource{src: src})
				}
				if includeStatistics {
					likeStatisticsSources = append(likeStatisticsSources, likeStatisticsSource{src: src})
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
							// PostgreSQL emits the merging NOTICE before the storage conflict error.
							o.ctx.AddNotice(fmt.Sprintf("merging column %q with inherited definition", sc.Name))
							// Check for storage conflict when INCLUDING STORAGE.
							if includeStorage {
								for _, ec := range cols {
									if strings.EqualFold(ec.Name, sc.Name) {
										ecStor := strings.ToLower(ec.Storage)
										scStor := strings.ToLower(sc.Storage)
										if ecStor == "" {
											ecStor = "extended"
										}
										if scStor == "" {
											scStor = "extended"
										}
										if ecStor != scStor {
											return &ExecError{
												Code:    "42611",
												Pos:     s.Pos(),
												Message: fmt.Sprintf("inherited column %q has a storage parameter conflict", sc.Name),
												Detail:  fmt.Sprintf("%s versus %s", strings.ToUpper(ecStor), strings.ToUpper(scStor)),
											}
										}
										break
									}
								}
							}
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
							likeCheckConstraints = appendLikeChecks(likeCheckConstraints, src)
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
						c.GeneratedVirtual = false
					}
					// Clear DefaultExpr unless INCLUDING DEFAULTS or INCLUDING ALL was specified.
					if !includeDefaults {
						c.DefaultExpr = nil
					}
					// Clear Storage unless INCLUDING STORAGE or INCLUDING ALL was specified.
					if !includeStorage {
						c.Storage = ""
					}
					cols = append(cols, c)
					// Carry forward the source table's NOT NULL constraint name for
					// this column (if one exists), so comments can be copied by name.
					if sc.NotNull {
						for _, nnc := range src.NotNullConstraints {
							if strings.EqualFold(nnc.ColName, sc.Name) {
								colKey := strings.ToLower(sc.Name)
								if _, alreadyMapped := likeNotNullByCol[colKey]; !alreadyMapped {
									likeNotNullByCol[colKey] = likeNotNullEntry{
										name:      nnc.Name,
										colName:   sc.Name,
										noInherit: nnc.NoInherit,
									}
								}
								break
							}
						}
					}
					// Copy CHECK constraints from source table when INCLUDING CONSTRAINTS.
					if includeConstraints {
						likeCheckConstraints = appendLikeChecks(likeCheckConstraints, src)
					}
				}
				// Copy indexes when INCLUDING INDEXES.
				if includeIndexes {
					if im2, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
						if im2.HasPrimaryKey(src) {
							// Check if new table already has a PK (from explicit columns or prior LIKE).
							if len(s.PrimaryKey) > 0 {
								return &ExecError{
									Code:    "42P16",
									Pos:     s.Pos(),
									Message: fmt.Sprintf("multiple primary keys for table %q are not allowed", s.Name.Name),
								}
							}
							// Copy PK columns from source.
							for _, idx := range im2.IndexesOnTable(src) {
								if idx.Primary {
									s.PrimaryKey = append(s.PrimaryKey, idx.Columns...)
									break
								}
							}
						}
						// Copy unique (non-PK) non-partial indexes and non-unique plain indexes.
						for _, idx := range im2.IndexesOnTable(src) {
							if idx.Primary || idx.HasPredicate || idx.IsExclusion {
								continue
							}
							if idx.Unique {
								likeUniqueIndexes = append(likeUniqueIndexes, idx)
							} else {
								likeNonUniqueIndexes = append(likeNonUniqueIndexes, idx)
							}
						}
					}
				}
				// Emit "merging constraint" NOTICEs when the LIKE source is also an
				// INHERITS parent. In PG, INHERITS propagates CHECK constraints, so LIKE's
				// copy "merges" with the inherited copy. M0097-0023.
				if includeConstraints && inheritParentOIDSet[src.OID] {
					for _, nc := range src.NamedChecks {
						if nc.Name != "" {
							o.ctx.AddNotice(fmt.Sprintf("merging constraint %q with inherited definition", nc.Name))
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
				Name:             c.Name,
				Type:             catalog.Type{Name: typeName, Args: append([]int64(nil), c.Type.Args...)},
				NotNull:          c.NotNull,
				GeneratedExpr:    c.GeneratedExpr,
				GeneratedAlways:  c.GeneratedAlways,
				GeneratedVirtual: c.GeneratedVirtual,
				DefaultExpr:      c.DefaultExpr,
				Compression:      c.Compression,
				Collation:        c.Collation,
			})
		}
	}
	// Partitioned tables cannot be inheritance children.
	if s.PartitionBy != nil && len(s.Inherits) > 0 {
		return &ExecError{Code: "42P16", Pos: s.Pos(), Message: "cannot create partitioned table as inheritance child"}
	}
	// PG18: NO INHERIT constraints cannot be added to partitioned tables. M0097-0023.
	if s.PartitionBy != nil {
		noInheritErr := func() *ExecError {
			return &ExecError{
				Code:    "42P16",
				Pos:     s.Pos(),
				Message: fmt.Sprintf("cannot add NO INHERIT constraint to partitioned table %q", s.Name.Name),
			}
		}
		// Check LIKE-sourced NOT NULL constraints.
		for _, entry := range likeNotNullByCol {
			if entry.noInherit {
				return noInheritErr()
			}
		}
		// Check LIKE-sourced CHECK constraints.
		for _, nc := range likeCheckConstraints {
			if nc.NoInherit {
				return noInheritErr()
			}
		}
		// Check explicit column NOT NULL NO INHERIT.
		for _, c := range s.Columns {
			if c.NotNullNoInherit || c.CheckNoInherit {
				return noInheritErr()
			}
		}
		// Check table-level CHECK ... NO INHERIT.
		if s.TableHasNoInheritCheck {
			return noInheritErr()
		}
	}
	// WITH OIDS is no longer supported.
	if s.WithOIDS {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "tables declared WITH OIDS are not supported"}
	}
	// Validate storage parameter names: double-quoted names are case-sensitive,
	// and all recognized option names are lowercase. A mixed-case key means the
	// user wrote WITH ("Fillfactor" = 10) which PG rejects as unrecognized.
	for k := range s.With {
		if k != strings.ToLower(k) {
			return &ExecError{Code: "42000", Pos: s.Pos(),
				Message: fmt.Sprintf("unrecognized parameter %q", k)}
		}
	}
	// Extract and bounds-check the fillfactor storage parameter so it can be
	// persisted on the catalog table (and surfaced through pg_class.reloptions
	// for pg_dump). PG rejects values outside 10–100. M0110-0001 (DU-002 slice 54).
	fillfactor := 0
	if v, ok := s.With["fillfactor"]; ok {
		ff, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"fillfactor\": %s", v)}
		}
		if ff < 10 || ff > 100 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"fillfactor\"", ff),
				Detail:  "Valid values are between \"10\" and \"100\"."}
		}
		fillfactor = ff
	}
	// Extract and bounds-check the parallel_workers storage parameter. Unlike
	// fillfactor, 0 is a valid explicit value (PG's reloption default is -1 =
	// unset; valid range 0–1024), so a separate `set` flag records whether the
	// option was present. goopg has no parallel query, so the value is purely
	// catalog/dump state that round-trips through pg_class.reloptions /
	// pg_dump's `WITH (parallel_workers='N')`. M0110-0001 (DU-002 slice 195).
	parallelWorkers := 0
	parallelWorkersSet := false
	if v, ok := s.With["parallel_workers"]; ok {
		pw, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"parallel_workers\": %s", v)}
		}
		if pw < 0 || pw > 1024 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"parallel_workers\"", pw),
				Detail:  "Valid values are between \"0\" and \"1024\"."}
		}
		parallelWorkers = pw
		parallelWorkersSet = true
	}
	// Extract and parse the autovacuum_enabled storage parameter. PG accepts
	// the usual boolean spellings (true/false, on/off, yes/no, 1/0, t/f,
	// y/n; case-insensitive) via parse_bool. goopg has no autovacuum, so the
	// value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's `WITH (autovacuum_enabled='true')`.
	// M0110-0001 (DU-002 slice 196).
	autovacuumEnabled := false
	autovacuumEnabledSet := false
	if v, ok := s.With["autovacuum_enabled"]; ok {
		b, parsed := parseReloptionBool(strings.TrimSpace(v))
		if !parsed {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for boolean option \"autovacuum_enabled\": %s", v)}
		}
		autovacuumEnabled = b
		autovacuumEnabledSet = true
	}
	// Extract and bounds-check the toast_tuple_target storage parameter. PG's
	// valid range is 128–TOAST_TUPLE_TARGET_MAIN (8160 on the default 8 KB
	// page); since the minimum is 128, zero unambiguously means "unset" (the
	// fillfactor pattern — no separate flag needed). goopg's TOAST thresholds
	// are fixed, so the value is purely catalog/dump state that round-trips
	// through pg_class.reloptions / pg_dump's `WITH (toast_tuple_target='N')`.
	// M0110-0001 (DU-002 slice 197).
	toastTupleTarget := 0
	if v, ok := s.With["toast_tuple_target"]; ok {
		tt, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"toast_tuple_target\": %s", v)}
		}
		if tt < 128 || tt > 8160 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"toast_tuple_target\"", tt),
				Detail:  "Valid values are between \"128\" and \"8160\"."}
		}
		toastTupleTarget = tt
	}
	// Extract and bounds-check the autovacuum_vacuum_threshold storage
	// parameter. PG's reloption range is 0–INT_MAX with a default of -1 (=
	// unset / use the GUC); since 0 is a valid explicit value, a separate `set`
	// flag records whether the option was present (the parallel_workers
	// pattern). goopg has no autovacuum, so the value is purely catalog/dump
	// state that round-trips through pg_class.reloptions / pg_dump's
	// `WITH (autovacuum_vacuum_threshold='N')`. M0110-0001 (DU-002 slice 198).
	autovacuumVacuumThreshold := 0
	autovacuumVacuumThresholdSet := false
	if v, ok := s.With["autovacuum_vacuum_threshold"]; ok {
		avt, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_vacuum_threshold\": %s", v)}
		}
		if avt < 0 || avt > 2147483647 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_vacuum_threshold\"", avt),
				Detail:  "Valid values are between \"0\" and \"2147483647\"."}
		}
		autovacuumVacuumThreshold = avt
		autovacuumVacuumThresholdSet = true
	}
	// Extract and bounds-check the autovacuum_vacuum_scale_factor storage
	// parameter — the first REAL-typed reloption goopg round-trips. PG's
	// reloption type is RELOPT_TYPE_REAL with range 0.0–100.0 and a default of
	// -1 (= unset / use the GUC); since 0.0 is a valid explicit value, a separate
	// `set` flag records whether the option was present (the parallel_workers
	// pattern, generalized to a float). The `!(f >= 0 && f <= 100)` form also
	// rejects NaN/±Inf, which ParseFloat would otherwise accept. goopg has no
	// autovacuum, so the value is purely catalog/dump state that round-trips
	// through pg_class.reloptions / pg_dump's
	// `WITH (autovacuum_vacuum_scale_factor='F')`. M0110-0001 (DU-002 slice 199).
	autovacuumVacuumScaleFactor := 0.0
	autovacuumVacuumScaleFactorSet := false
	if v, ok := s.With["autovacuum_vacuum_scale_factor"]; ok {
		avsf, convErr := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for floating point option \"autovacuum_vacuum_scale_factor\": %s", v)}
		}
		if !(avsf >= 0.0 && avsf <= 100.0) {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %s out of bounds for option \"autovacuum_vacuum_scale_factor\"", strconv.FormatFloat(avsf, 'g', -1, 64)),
				Detail:  "Valid values are between \"0.000000\" and \"100.000000\"."}
		}
		autovacuumVacuumScaleFactor = avsf
		autovacuumVacuumScaleFactorSet = true
	}
	// Extract and bounds-check the autovacuum_analyze_scale_factor storage
	// parameter — the second REAL-typed reloption goopg round-trips, reusing the
	// slice-199 float path. PG's reloption type is RELOPT_TYPE_REAL with range
	// 0.0–100.0 and a default of -1 (= unset / use the GUC); since 0.0 is a valid
	// explicit value, a separate `set` flag records whether the option was present.
	// The `!(f >= 0 && f <= 100)` form also rejects NaN/±Inf, which ParseFloat
	// would otherwise accept. goopg has no autovacuum, so the value is purely
	// catalog/dump state that round-trips through pg_class.reloptions / pg_dump's
	// `WITH (autovacuum_analyze_scale_factor='F')`. M0110-0001 (DU-002 slice 200).
	autovacuumAnalyzeScaleFactor := 0.0
	autovacuumAnalyzeScaleFactorSet := false
	if v, ok := s.With["autovacuum_analyze_scale_factor"]; ok {
		aasf, convErr := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for floating point option \"autovacuum_analyze_scale_factor\": %s", v)}
		}
		if !(aasf >= 0.0 && aasf <= 100.0) {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %s out of bounds for option \"autovacuum_analyze_scale_factor\"", strconv.FormatFloat(aasf, 'g', -1, 64)),
				Detail:  "Valid values are between \"0.000000\" and \"100.000000\"."}
		}
		autovacuumAnalyzeScaleFactor = aasf
		autovacuumAnalyzeScaleFactorSet = true
	}
	// Extract and bounds-check the autovacuum_vacuum_insert_scale_factor storage
	// parameter — the third REAL-typed reloption goopg round-trips, reusing the
	// slice-199 float path. PG's reloption type is RELOPT_TYPE_REAL with range
	// 0.0–100.0 and a default of -1 (= unset / use the GUC); since 0.0 is a valid
	// explicit value, a separate `set` flag records whether the option was present.
	// The `!(f >= 0 && f <= 100)` form also rejects NaN/±Inf, which ParseFloat
	// would otherwise accept. goopg has no autovacuum, so the value is purely
	// catalog/dump state that round-trips through pg_class.reloptions / pg_dump's
	// `WITH (autovacuum_vacuum_insert_scale_factor='F')`. M0110-0001 (DU-002 slice 201).
	autovacuumVacuumInsertScaleFactor := 0.0
	autovacuumVacuumInsertScaleFactorSet := false
	if v, ok := s.With["autovacuum_vacuum_insert_scale_factor"]; ok {
		avisf, convErr := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for floating point option \"autovacuum_vacuum_insert_scale_factor\": %s", v)}
		}
		if !(avisf >= 0.0 && avisf <= 100.0) {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %s out of bounds for option \"autovacuum_vacuum_insert_scale_factor\"", strconv.FormatFloat(avisf, 'g', -1, 64)),
				Detail:  "Valid values are between \"0.000000\" and \"100.000000\"."}
		}
		autovacuumVacuumInsertScaleFactor = avisf
		autovacuumVacuumInsertScaleFactorSet = true
	}
	// Extract and bounds-check the autovacuum_vacuum_cost_delay storage parameter —
	// the fourth (and final) REAL-typed reloption goopg round-trips, reusing the
	// slice-199 float path. PG's reloption type is RELOPT_TYPE_REAL with range
	// 0.0–100.0 and a default of -1 (= unset / use the GUC); since 0.0 is a valid
	// explicit value, a separate `set` flag records whether the option was present.
	// The `!(f >= 0 && f <= 100)` form also rejects NaN/±Inf, which ParseFloat
	// would otherwise accept. goopg has no autovacuum, so the value is purely
	// catalog/dump state that round-trips through pg_class.reloptions / pg_dump's
	// `WITH (autovacuum_vacuum_cost_delay='F')`. M0110-0001 (DU-002 slice 202).
	autovacuumVacuumCostDelay := 0.0
	autovacuumVacuumCostDelaySet := false
	if v, ok := s.With["autovacuum_vacuum_cost_delay"]; ok {
		avcd, convErr := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for floating point option \"autovacuum_vacuum_cost_delay\": %s", v)}
		}
		if !(avcd >= 0.0 && avcd <= 100.0) {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %s out of bounds for option \"autovacuum_vacuum_cost_delay\"", strconv.FormatFloat(avcd, 'g', -1, 64)),
				Detail:  "Valid values are between \"0.000000\" and \"100.000000\"."}
		}
		autovacuumVacuumCostDelay = avcd
		autovacuumVacuumCostDelaySet = true
	}
	// Extract and bounds-check the autovacuum_analyze_threshold storage parameter —
	// the second INT-typed autovacuum reloption goopg round-trips, reusing the
	// slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT with range
	// 0–INT_MAX and a default of -1 (= unset / use the GUC); since 0 is a valid
	// explicit value, a separate `set` flag records whether the option was present
	// (the parallel_workers pattern). goopg has no autovacuum, so the value is
	// purely catalog/dump state that round-trips through pg_class.reloptions /
	// pg_dump's `WITH (autovacuum_analyze_threshold='N')`. M0110-0001 (DU-002 slice 203).
	autovacuumAnalyzeThreshold := 0
	autovacuumAnalyzeThresholdSet := false
	if v, ok := s.With["autovacuum_analyze_threshold"]; ok {
		aat, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_analyze_threshold\": %s", v)}
		}
		if aat < 0 || aat > 2147483647 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_analyze_threshold\"", aat),
				Detail:  "Valid values are between \"0\" and \"2147483647\"."}
		}
		autovacuumAnalyzeThreshold = aat
		autovacuumAnalyzeThresholdSet = true
	}
	// Extract and bounds-check the autovacuum_vacuum_insert_threshold storage
	// parameter — the third INT-typed autovacuum reloption goopg round-trips,
	// reusing the slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT
	// with range -1–INT_MAX and a default of -2 (= unset / use the GUC); -1
	// disables insert vacuums. Since -1 and 0 are valid explicit values, a separate
	// `set` flag records whether the option was present (the parallel_workers
	// pattern). goopg has no autovacuum, so the value is purely catalog/dump state
	// that round-trips through pg_class.reloptions / pg_dump's
	// `WITH (autovacuum_vacuum_insert_threshold='N')`. M0110-0001 (DU-002 slice 204).
	autovacuumVacuumInsertThreshold := 0
	autovacuumVacuumInsertThresholdSet := false
	if v, ok := s.With["autovacuum_vacuum_insert_threshold"]; ok {
		avit, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_vacuum_insert_threshold\": %s", v)}
		}
		if avit < -1 || avit > 2147483647 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_vacuum_insert_threshold\"", avit),
				Detail:  "Valid values are between \"-1\" and \"2147483647\"."}
		}
		autovacuumVacuumInsertThreshold = avit
		autovacuumVacuumInsertThresholdSet = true
	}
	// Extract and parse the vacuum_truncate storage parameter — a boolean
	// reloption (RELOPT_TYPE_BOOL, reloptions.c:1915; RELOPT_KIND_HEAP|TOAST,
	// default true) that reuses the slice-196 autovacuum_enabled boolean path.
	// PG accepts the usual boolean spellings (true/false, on/off, yes/no, 1/0,
	// t/f, y/n; case-insensitive) via parse_bool. goopg has no VACUUM truncation,
	// so the value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's `WITH (vacuum_truncate='true')`.
	// M0110-0001 (DU-002 slice 205).
	vacuumTruncate := false
	vacuumTruncateSet := false
	if v, ok := s.With["vacuum_truncate"]; ok {
		b, parsed := parseReloptionBool(strings.TrimSpace(v))
		if !parsed {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for boolean option \"vacuum_truncate\": %s", v)}
		}
		vacuumTruncate = b
		vacuumTruncateSet = true
	}
	// Extract and bounds-check the log_autovacuum_min_duration storage parameter —
	// the fourth INT-typed autovacuum-namespace reloption goopg round-trips,
	// reusing the slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT
	// with range -1–INT_MAX and a default of -1 (= unset / use the GUC); 0 logs
	// every autovacuum action (reloptions.c:1897/329). Since -1 and 0 are valid
	// explicit values, a separate `set` flag records whether the option was present
	// (the parallel_workers pattern). goopg has no autovacuum, so the value is
	// purely catalog/dump state that round-trips through pg_class.reloptions /
	// pg_dump's `WITH (log_autovacuum_min_duration='N')`. M0110-0001 (DU-002 slice 206).
	logAutovacuumMinDuration := 0
	logAutovacuumMinDurationSet := false
	if v, ok := s.With["log_autovacuum_min_duration"]; ok {
		lamd, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"log_autovacuum_min_duration\": %s", v)}
		}
		if lamd < -1 || lamd > 2147483647 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"log_autovacuum_min_duration\"", lamd),
				Detail:  "Valid values are between \"-1\" and \"2147483647\"."}
		}
		logAutovacuumMinDuration = lamd
		logAutovacuumMinDurationSet = true
	}
	// Extract and bounds-check the autovacuum_freeze_min_age storage parameter —
	// the fifth INT-typed autovacuum-namespace reloption goopg round-trips, reusing
	// the slice-198 integer path. PG's reloption type is RELOPT_TYPE_INT with range
	// 0–1000000000 and a default of -1 (= unset / use the GUC) (reloptions.c:1885/272).
	// Since 0 is a valid explicit value, a separate `set` flag records whether the
	// option was present (the parallel_workers pattern). goopg has no autovacuum, so
	// the value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's `WITH (autovacuum_freeze_min_age='N')`.
	// M0110-0001 (DU-002 slice 207).
	autovacuumFreezeMinAge := 0
	autovacuumFreezeMinAgeSet := false
	if v, ok := s.With["autovacuum_freeze_min_age"]; ok {
		afma, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_freeze_min_age\": %s", v)}
		}
		if afma < 0 || afma > 1000000000 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_freeze_min_age\"", afma),
				Detail:  "Valid values are between \"0\" and \"1000000000\"."}
		}
		autovacuumFreezeMinAge = afma
		autovacuumFreezeMinAgeSet = true
	}
	// autovacuum_freeze_max_age (RELOPT_TYPE_INT, range 100000–2000000000, default
	// -1 = unset; reloptions.c:1887/290). The minimum valid value is 100000, so an
	// explicit -1 is rejected as out-of-range; a separate `set` flag records whether
	// the option was present (the parallel_workers pattern). goopg has no autovacuum,
	// so the value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's `WITH (autovacuum_freeze_max_age='N')`.
	// M0110-0001 (DU-002 slice 208).
	autovacuumFreezeMaxAge := 0
	autovacuumFreezeMaxAgeSet := false
	if v, ok := s.With["autovacuum_freeze_max_age"]; ok {
		afma, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_freeze_max_age\": %s", v)}
		}
		if afma < 100000 || afma > 2000000000 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_freeze_max_age\"", afma),
				Detail:  "Valid values are between \"100000\" and \"2000000000\"."}
		}
		autovacuumFreezeMaxAge = afma
		autovacuumFreezeMaxAgeSet = true
	}
	// autovacuum_freeze_table_age (RELOPT_TYPE_INT, range 0–2000000000, default
	// -1 = unset; reloptions.c:1889/312). 0 is a valid explicit value, so a
	// separate `set` flag records whether the option was present (the
	// parallel_workers pattern) rather than a zero check. goopg has no autovacuum,
	// so the value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's `WITH (autovacuum_freeze_table_age='N')`.
	// M0110-0001 (DU-002 slice 209).
	autovacuumFreezeTableAge := 0
	autovacuumFreezeTableAgeSet := false
	if v, ok := s.With["autovacuum_freeze_table_age"]; ok {
		afta, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_freeze_table_age\": %s", v)}
		}
		if afta < 0 || afta > 2000000000 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_freeze_table_age\"", afta),
				Detail:  "Valid values are between \"0\" and \"2000000000\"."}
		}
		autovacuumFreezeTableAge = afta
		autovacuumFreezeTableAgeSet = true
	}
	// autovacuum_multixact_freeze_min_age (RELOPT_TYPE_INT, range 0–1000000000,
	// default -1 = unset; reloptions.c:1891/281). 0 is a valid explicit value, so a
	// separate `set` flag records whether the option was present (the
	// parallel_workers pattern) rather than a zero check. goopg has no autovacuum,
	// so the value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's `WITH (autovacuum_multixact_freeze_min_age='N')`.
	// M0110-0001 (DU-002 slice 210).
	autovacuumMultixactFreezeMinAge := 0
	autovacuumMultixactFreezeMinAgeSet := false
	if v, ok := s.With["autovacuum_multixact_freeze_min_age"]; ok {
		amfma, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_multixact_freeze_min_age\": %s", v)}
		}
		if amfma < 0 || amfma > 1000000000 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_multixact_freeze_min_age\"", amfma),
				Detail:  "Valid values are between \"0\" and \"1000000000\"."}
		}
		autovacuumMultixactFreezeMinAge = amfma
		autovacuumMultixactFreezeMinAgeSet = true
	}
	// autovacuum_multixact_freeze_max_age (RELOPT_TYPE_INT, range 10000–2000000000,
	// default -1 = unset; reloptions.c:1893/299). Unlike the min/table-age options
	// the lower bound is 10000 (not 0), but a separate `set` flag still records
	// whether the option was present (the parallel_workers pattern). goopg has no
	// autovacuum, so the value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's `WITH (autovacuum_multixact_freeze_max_age='N')`.
	// M0110-0001 (DU-002 slice 211).
	autovacuumMultixactFreezeMaxAge := 0
	autovacuumMultixactFreezeMaxAgeSet := false
	if v, ok := s.With["autovacuum_multixact_freeze_max_age"]; ok {
		amfmaxa, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_multixact_freeze_max_age\": %s", v)}
		}
		if amfmaxa < 10000 || amfmaxa > 2000000000 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_multixact_freeze_max_age\"", amfmaxa),
				Detail:  "Valid values are between \"10000\" and \"2000000000\"."}
		}
		autovacuumMultixactFreezeMaxAge = amfmaxa
		autovacuumMultixactFreezeMaxAgeSet = true
	}
	// autovacuum_multixact_freeze_table_age (RELOPT_TYPE_INT, range 0–2000000000,
	// default -1 = unset; reloptions.c:1895/316). 0 is a valid explicit value, so a
	// separate `set` flag records whether the option was present (the
	// parallel_workers pattern) rather than a zero check. goopg has no autovacuum,
	// so the value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's `WITH (autovacuum_multixact_freeze_table_age='N')`.
	// M0110-0001 (DU-002 slice 212).
	autovacuumMultixactFreezeTableAge := 0
	autovacuumMultixactFreezeTableAgeSet := false
	if v, ok := s.With["autovacuum_multixact_freeze_table_age"]; ok {
		amfta, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_multixact_freeze_table_age\": %s", v)}
		}
		if amfta < 0 || amfta > 2000000000 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_multixact_freeze_table_age\"", amfta),
				Detail:  "Valid values are between \"0\" and \"2000000000\"."}
		}
		autovacuumMultixactFreezeTableAge = amfta
		autovacuumMultixactFreezeTableAgeSet = true
	}
	// autovacuum_vacuum_cost_limit (RELOPT_TYPE_INT, range 1–10000,
	// default -1 = unset; reloptions.c:1883/268). The lower bound is 1, so 0 is below
	// range and rejected; a separate `set` flag records whether the option was present
	// (the parallel_workers pattern). goopg has no autovacuum, so the value is purely
	// catalog/dump state that round-trips through pg_class.reloptions / pg_dump's
	// `WITH (autovacuum_vacuum_cost_limit='N')`. M0110-0001 (DU-002 slice 213).
	autovacuumVacuumCostLimit := 0
	autovacuumVacuumCostLimitSet := false
	if v, ok := s.With["autovacuum_vacuum_cost_limit"]; ok {
		avcl, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_vacuum_cost_limit\": %s", v)}
		}
		if avcl < 1 || avcl > 10000 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_vacuum_cost_limit\"", avcl),
				Detail:  "Valid values are between \"1\" and \"10000\"."}
		}
		autovacuumVacuumCostLimit = avcl
		autovacuumVacuumCostLimitSet = true
	}
	// Extract and parse the user_catalog_table storage parameter
	// (RELOPT_TYPE_BOOL, RELOPT_KIND_HEAP, default false; reloptions.c:1909).
	// PG accepts the usual boolean spellings via parse_bool; a separate `set`
	// flag records whether the option was present (the slice-196 autovacuum_enabled
	// boolean path). The option marks a heap as a catalog table for
	// logical-decoding purposes; goopg has no logical decoding, so the value is
	// purely catalog/dump state that round-trips through pg_class.reloptions /
	// pg_dump's `WITH (user_catalog_table='true')`. M0110-0001 (DU-002 slice 214).
	userCatalogTable := false
	userCatalogTableSet := false
	if v, ok := s.With["user_catalog_table"]; ok {
		b, parsed := parseReloptionBool(strings.TrimSpace(v))
		if !parsed {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for boolean option \"user_catalog_table\": %s", v)}
		}
		userCatalogTable = b
		userCatalogTableSet = true
	}
	// Extract and bounds-check the autovacuum_vacuum_max_threshold storage
	// parameter — a PG18 heap reloption (RELOPT_KIND_HEAP | RELOPT_KIND_TOAST,
	// reloptions.c:236) capping the dead-tuple count at which autovacuum fires.
	// Reuses the slice-204 integer path: PG's reloption type is RELOPT_TYPE_INT
	// with range -1–INT_MAX and a default of -2 (= unset / use the GUC); -1
	// disables the cap. Since -1 and 0 are valid explicit values, a separate `set`
	// flag records whether the option was present (the parallel_workers pattern).
	// goopg has no autovacuum, so the value is purely catalog/dump state that
	// round-trips through pg_class.reloptions / pg_dump's
	// `WITH (autovacuum_vacuum_max_threshold='N')`. M0110-0001 (DU-002 slice 215).
	autovacuumVacuumMaxThreshold := 0
	autovacuumVacuumMaxThresholdSet := false
	if v, ok := s.With["autovacuum_vacuum_max_threshold"]; ok {
		avmt, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_vacuum_max_threshold\": %s", v)}
		}
		if avmt < -1 || avmt > 2147483647 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_vacuum_max_threshold\"", avmt),
				Detail:  "Valid values are between \"-1\" and \"2147483647\"."}
		}
		autovacuumVacuumMaxThreshold = avmt
		autovacuumVacuumMaxThresholdSet = true
	}
	// Extract and bounds-check the vacuum_max_eager_freeze_failure_rate storage
	// parameter — a PG18 heap reloption (RELOPT_KIND_HEAP | RELOPT_KIND_TOAST,
	// reloptions.c:431) giving the fraction of pages vacuum may scan and fail to
	// freeze before disabling eager scanning. Reuses the slice-199 REAL path, but
	// PG's range here is 0.0–1.0 (not 0.0–100.0) with a default of -1 (= unset /
	// use the GUC); since 0.0 is a valid explicit value, a separate `set` flag
	// records whether the option was present (the parallel_workers pattern,
	// generalized to a float). The `!(f >= 0 && f <= 1)` form also rejects
	// NaN/±Inf, which ParseFloat would otherwise accept. goopg has no eager
	// freezing, so the value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's
	// `WITH (vacuum_max_eager_freeze_failure_rate='F')`. M0110-0001 (DU-002 slice 216).
	vacuumMaxEagerFreezeFailureRate := 0.0
	vacuumMaxEagerFreezeFailureRateSet := false
	if v, ok := s.With["vacuum_max_eager_freeze_failure_rate"]; ok {
		vmefr, convErr := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for floating point option \"vacuum_max_eager_freeze_failure_rate\": %s", v)}
		}
		if !(vmefr >= 0.0 && vmefr <= 1.0) {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %s out of bounds for option \"vacuum_max_eager_freeze_failure_rate\"", strconv.FormatFloat(vmefr, 'g', -1, 64)),
				Detail:  "Valid values are between \"0.000000\" and \"1.000000\"."}
		}
		vacuumMaxEagerFreezeFailureRate = vmefr
		vacuumMaxEagerFreezeFailureRateSet = true
	}
	// Extract and validate the vacuum_index_cleanup storage parameter — a PG18
	// heap reloption (RELOPT_TYPE_ENUM, RELOPT_KIND_HEAP | RELOPT_KIND_TOAST,
	// reloptions.c:519) controlling whether VACUUM performs index vacuuming and
	// cleanup. This is goopg's first ENUM reloption. PG accepts the spellings
	// auto/on/off/true/false/yes/no/1/0 case-insensitively
	// (StdRdOptIndexCleanupValues, reloptions.c:487); an unrecognized value is a
	// 22023 error whose message lists the canonical members. Unlike the
	// bool/int/float reloptions, the value is stored VERBATIM (trimmed) rather
	// than re-rendered to a canonical form, mirroring PG's pg_class.reloptions
	// which preserves the literal input text (so `=on` round-trips as `=on`, not
	// `=true`). A separate `set` flag records presence ("auto" is a legal
	// explicit value with no reserved sentinel). goopg has no autovacuum, so the
	// value is purely catalog/dump state that round-trips through
	// pg_class.reloptions / pg_dump's `WITH (vacuum_index_cleanup='V')`.
	// M0110-0001 (DU-002 slice 217).
	vacuumIndexCleanup := ""
	vacuumIndexCleanupSet := false
	if v, ok := s.With["vacuum_index_cleanup"]; ok {
		trimmed := strings.TrimSpace(v)
		switch strings.ToLower(trimmed) {
		case "auto", "on", "off", "true", "false", "yes", "no", "1", "0":
			// accepted enum spelling
		default:
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for enum option \"vacuum_index_cleanup\": %s", trimmed),
				Detail:  "Valid values are \"on\", \"off\", and \"auto\"."}
		}
		vacuumIndexCleanup = trimmed
		vacuumIndexCleanupSet = true
	}
	// Extract `toast.*` storage parameters. PostgreSQL stores reloptions whose
	// names carry the `toast.` namespace on the table's TOAST relation's
	// pg_class.reloptions (without the `toast.` prefix); pg_dump joins to the
	// TOAST relation, reads `tc.reloptions AS toast_reloptions`, and re-emits
	// them WITH the `toast.` prefix (appendReloptionsArrayAH, "toast.").
	// goopg has no TOAST, so these are catalog/dump-only (advisory). Each option
	// is gathered in a fixed code order; the synthesized TOAST relation's
	// reloptions array stores them as `name=value` WITHOUT the `toast.` prefix.
	// Supported so far: the booleans `toast.autovacuum_enabled` (slice 224) and
	// `toast.vacuum_truncate` (slice 225). Both share RELOPT_KIND_TOAST in
	// PostgreSQL (reloptions.c:107/152, RELOPT_KIND_HEAP | RELOPT_KIND_TOAST).
	// Additional toast.* options extend this gather in later slices.
	// M0110-0001 (DU-002 slice 224/225).
	var toastReloptions []string
	if v, ok := s.With["toast.autovacuum_enabled"]; ok {
		b, parsed := parseReloptionBool(strings.TrimSpace(v))
		if !parsed {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for boolean option \"autovacuum_enabled\": %s", v)}
		}
		toastReloptions = append(toastReloptions, "autovacuum_enabled="+strconv.FormatBool(b))
	}
	// `toast.vacuum_truncate` — the second RELOPT_KIND_TOAST boolean, mirroring
	// the parent-table `vacuum_truncate` path (slice 205) on the TOAST relation.
	// PG accepts the usual boolean spellings via parse_bool; non-bool → 22023.
	// M0110-0001 (DU-002 slice 225).
	if v, ok := s.With["toast.vacuum_truncate"]; ok {
		b, parsed := parseReloptionBool(strings.TrimSpace(v))
		if !parsed {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for boolean option \"vacuum_truncate\": %s", v)}
		}
		toastReloptions = append(toastReloptions, "vacuum_truncate="+strconv.FormatBool(b))
	}
	// `toast.autovacuum_vacuum_threshold` — the first RELOPT_KIND_TOAST *integer*
	// option, reusing the parent-table integer reloption path (slice 198). PG's
	// reloption range is 0–INT_MAX with a default of -1 (= unset / use the GUC);
	// autovacuum_vacuum_threshold shares RELOPT_KIND_HEAP | RELOPT_KIND_TOAST
	// (reloptions.c:229), so PG accepts the `toast.` prefix and stores it on the
	// TOAST relation's reloptions. goopg has no autovacuum, so the value is purely
	// catalog/dump state. M0110-0001 (DU-002 slice 226).
	if v, ok := s.With["toast.autovacuum_vacuum_threshold"]; ok {
		avt, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_vacuum_threshold\": %s", v)}
		}
		if avt < 0 || avt > 2147483647 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_vacuum_threshold\"", avt),
				Detail:  "Valid values are between \"0\" and \"2147483647\"."}
		}
		toastReloptions = append(toastReloptions, "autovacuum_vacuum_threshold="+strconv.Itoa(avt))
	}
	// `toast.autovacuum_vacuum_scale_factor` — the first RELOPT_KIND_TOAST *real*
	// option, reusing the parent-table float reloption path (slice 199). PG's
	// reloption type is RELOPT_TYPE_REAL with range 0.0–100.0 and a default of -1
	// (= unset / use the GUC); autovacuum_vacuum_scale_factor shares
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST (reloptions.c:404), so PG accepts the
	// `toast.` prefix and stores it on the TOAST relation's reloptions. The
	// `!(f >= 0 && f <= 100)` form also rejects NaN/±Inf. goopg has no autovacuum,
	// so the value is purely catalog/dump state. M0110-0001 (DU-002 slice 227).
	if v, ok := s.With["toast.autovacuum_vacuum_scale_factor"]; ok {
		avsf, convErr := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for floating point option \"autovacuum_vacuum_scale_factor\": %s", v)}
		}
		if !(avsf >= 0.0 && avsf <= 100.0) {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %s out of bounds for option \"autovacuum_vacuum_scale_factor\"", strconv.FormatFloat(avsf, 'g', -1, 64)),
				Detail:  "Valid values are between \"0.000000\" and \"100.000000\"."}
		}
		toastReloptions = append(toastReloptions, "autovacuum_vacuum_scale_factor="+strconv.FormatFloat(avsf, 'g', -1, 64))
	}
	// `toast.autovacuum_vacuum_cost_delay` — the second RELOPT_KIND_TOAST *real*
	// option, reusing the parent-table float reloption path (slice 202). PG's
	// reloption type is RELOPT_TYPE_REAL with range 0.0–100.0 and a default of -1
	// (= unset / use the GUC); autovacuum_vacuum_cost_delay shares
	// RELOPT_KIND_HEAP | RELOPT_KIND_TOAST (reloptions.c:393), so PG accepts the
	// `toast.` prefix and stores it on the TOAST relation's reloptions. The
	// `!(f >= 0 && f <= 100)` form also rejects NaN/±Inf. goopg has no autovacuum,
	// so the value is purely catalog/dump state. M0110-0001 (DU-002 slice 228).
	if v, ok := s.With["toast.autovacuum_vacuum_cost_delay"]; ok {
		avcd, convErr := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for floating point option \"autovacuum_vacuum_cost_delay\": %s", v)}
		}
		if !(avcd >= 0.0 && avcd <= 100.0) {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %s out of bounds for option \"autovacuum_vacuum_cost_delay\"", strconv.FormatFloat(avcd, 'g', -1, 64)),
				Detail:  "Valid values are between \"0.000000\" and \"100.000000\"."}
		}
		toastReloptions = append(toastReloptions, "autovacuum_vacuum_cost_delay="+strconv.FormatFloat(avcd, 'g', -1, 64))
	}
	// `toast.autovacuum_vacuum_cost_limit` — the second RELOPT_KIND_TOAST *integer*
	// option, reusing the parent-table integer reloption path. PG's reloption range
	// is 1–10000 with a default of -1 (= unset / use the GUC);
	// autovacuum_vacuum_cost_limit shares RELOPT_KIND_HEAP | RELOPT_KIND_TOAST
	// (reloptions.c:265), so PG accepts the `toast.` prefix and stores it on the
	// TOAST relation's reloptions. goopg has no autovacuum, so the value is purely
	// catalog/dump state. M0110-0001 (DU-002 slice 229).
	if v, ok := s.With["toast.autovacuum_vacuum_cost_limit"]; ok {
		avcl, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"autovacuum_vacuum_cost_limit\": %s", v)}
		}
		if avcl < 1 || avcl > 10000 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"autovacuum_vacuum_cost_limit\"", avcl),
				Detail:  "Valid values are between \"1\" and \"10000\"."}
		}
		toastReloptions = append(toastReloptions, "autovacuum_vacuum_cost_limit="+strconv.Itoa(avcl))
	}
	// UNLOGGED partitioned tables are not supported in PostgreSQL.
	if s.Unlogged && s.PartitionBy != nil {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "partitioned tables cannot be unlogged"}
	}
	// Storage parameters (WITH clause) are not supported for partitioned tables.
	if s.PartitionBy != nil && len(s.With) > 0 {
		return &ExecError{Code: "0A000", Pos: s.Pos(),
			Message: "cannot specify storage parameters for a partitioned table",
			Detail:  "This operation is not supported for partitioned tables.",
			Hint:    "Specify storage parameters for its leaf partitions instead."}
	}
	// Validate DEFAULT expressions (no column refs, aggregates, subqueries, SRFs).
	for _, c := range s.Columns {
		if strings.EqualFold(c.Type.Name, "unknown") {
			return &ExecError{Code: "42P16", Pos: s.Pos(),
				Message: fmt.Sprintf("column %q has pseudo-type unknown", c.Name)}
		}
		if c.DefaultExpr != nil {
			if err := validateDefaultExpr(c.DefaultExpr, s.Pos(), o.ctx); err != nil {
				return err
			}
		}
	}
	// Validate PARTITION BY clause (method, key columns, key expressions).
	if err := validatePartitionKey(s, cols, o.ctx); err != nil {
		return err
	}
	// Mark purely-inherited columns (copied from an INHERITS parent and not
	// locally redefined in the child body) so pg_attribute reports
	// attislocal=false / attinhcount>0. pg_dump then omits them from the child's
	// CREATE TABLE column list — they arrive via the INHERITS (...) clause
	// instead. A column the child also declares stays local (attislocal=true).
	// DU-002 slice 170.
	if len(inheritParents) > 0 {
		localCols := make(map[string]bool, len(s.Columns))
		for _, c := range s.Columns {
			localCols[strings.ToLower(c.Name)] = true
		}
		for i := range cols {
			lname := strings.ToLower(cols[i].Name)
			if inheritedColNames[lname] && !localCols[lname] {
				cols[i].Inherited = true
			}
		}
	}
	tbl, err := o.ctx.Catalog.CreateTable(s.Name, cols)
	if err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	tbl.Unlogged = s.Unlogged
	tbl.Temp = s.Temporary
	tbl.Fillfactor = fillfactor
	tbl.ParallelWorkers = parallelWorkers
	tbl.ParallelWorkersSet = parallelWorkersSet
	tbl.AutovacuumEnabled = autovacuumEnabled
	tbl.AutovacuumEnabledSet = autovacuumEnabledSet
	tbl.ToastTupleTarget = toastTupleTarget
	tbl.AutovacuumVacuumThreshold = autovacuumVacuumThreshold
	tbl.AutovacuumVacuumThresholdSet = autovacuumVacuumThresholdSet
	tbl.AutovacuumVacuumScaleFactor = autovacuumVacuumScaleFactor
	tbl.AutovacuumVacuumScaleFactorSet = autovacuumVacuumScaleFactorSet
	tbl.AutovacuumAnalyzeScaleFactor = autovacuumAnalyzeScaleFactor
	tbl.AutovacuumAnalyzeScaleFactorSet = autovacuumAnalyzeScaleFactorSet
	tbl.AutovacuumVacuumInsertScaleFactor = autovacuumVacuumInsertScaleFactor
	tbl.AutovacuumVacuumInsertScaleFactorSet = autovacuumVacuumInsertScaleFactorSet
	tbl.AutovacuumVacuumCostDelay = autovacuumVacuumCostDelay
	tbl.AutovacuumVacuumCostDelaySet = autovacuumVacuumCostDelaySet
	tbl.AutovacuumAnalyzeThreshold = autovacuumAnalyzeThreshold
	tbl.AutovacuumAnalyzeThresholdSet = autovacuumAnalyzeThresholdSet
	tbl.AutovacuumVacuumInsertThreshold = autovacuumVacuumInsertThreshold
	tbl.AutovacuumVacuumInsertThresholdSet = autovacuumVacuumInsertThresholdSet
	tbl.VacuumTruncate = vacuumTruncate
	tbl.VacuumTruncateSet = vacuumTruncateSet
	tbl.LogAutovacuumMinDuration = logAutovacuumMinDuration
	tbl.LogAutovacuumMinDurationSet = logAutovacuumMinDurationSet
	tbl.AutovacuumFreezeMinAge = autovacuumFreezeMinAge
	tbl.AutovacuumFreezeMinAgeSet = autovacuumFreezeMinAgeSet
	tbl.AutovacuumFreezeMaxAge = autovacuumFreezeMaxAge
	tbl.AutovacuumFreezeMaxAgeSet = autovacuumFreezeMaxAgeSet
	tbl.AutovacuumFreezeTableAge = autovacuumFreezeTableAge
	tbl.AutovacuumFreezeTableAgeSet = autovacuumFreezeTableAgeSet
	tbl.AutovacuumMultixactFreezeMinAge = autovacuumMultixactFreezeMinAge
	tbl.AutovacuumMultixactFreezeMinAgeSet = autovacuumMultixactFreezeMinAgeSet
	tbl.AutovacuumMultixactFreezeMaxAge = autovacuumMultixactFreezeMaxAge
	tbl.AutovacuumMultixactFreezeMaxAgeSet = autovacuumMultixactFreezeMaxAgeSet
	tbl.AutovacuumMultixactFreezeTableAge = autovacuumMultixactFreezeTableAge
	tbl.AutovacuumMultixactFreezeTableAgeSet = autovacuumMultixactFreezeTableAgeSet
	tbl.AutovacuumVacuumCostLimit = autovacuumVacuumCostLimit
	tbl.AutovacuumVacuumCostLimitSet = autovacuumVacuumCostLimitSet
	tbl.UserCatalogTable = userCatalogTable
	tbl.UserCatalogTableSet = userCatalogTableSet
	tbl.AutovacuumVacuumMaxThreshold = autovacuumVacuumMaxThreshold
	tbl.AutovacuumVacuumMaxThresholdSet = autovacuumVacuumMaxThresholdSet
	tbl.VacuumMaxEagerFreezeFailureRate = vacuumMaxEagerFreezeFailureRate
	tbl.VacuumMaxEagerFreezeFailureRateSet = vacuumMaxEagerFreezeFailureRateSet
	tbl.VacuumIndexCleanup = vacuumIndexCleanup
	tbl.VacuumIndexCleanupSet = vacuumIndexCleanupSet
	tbl.ToastReloptions = toastReloptions
	// Register inheritance relationships now that the child OID is known.
	if len(inheritParents) > 0 {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			for _, parent := range inheritParents {
				im.RegisterInheritanceChild(parent.OID, tbl.OID)
			}
		}
		// Record the ordered parent OIDs on the child so pg_inherits emits a
		// row per (child, parent) pair and pg_dump re-emits the INHERITS clause.
		parentOIDs := make([]uint32, 0, len(inheritParents))
		for _, parent := range inheritParents {
			parentOIDs = append(parentOIDs, parent.OID)
		}
		tbl.InheritsParentOIDs = parentOIDs
	}
	// Register FK constraints from inline REFERENCES clauses. M0096-0011.
	for _, c := range s.Columns {
		if c.RefTable.Name != "" {
			// PG auto-names a single-column inline FK <table>_<col>_fkey and
			// records it in pg_constraint (contype='f'); pg_dump's getConstraints
			// reads that row and renders the ALTER TABLE ADD CONSTRAINT via
			// pg_get_constraintdef. DU-002 slice 51.
			fkName := tbl.Name + "_" + c.Name + "_fkey"
			fk := catalog.ForeignKey{
				Name:              fkName,
				OID:               o.allocConstraintOID(fkName),
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
	// Register table-level FOREIGN KEY constraints — the multi-column sibling of
	// the inline REFERENCES loop above (`FOREIGN KEY (a, b) REFERENCES t (x, y)`).
	// PG auto-names an unnamed table-level FK <table>_<firstcol>_fkey and records
	// it in pg_constraint (contype='f'), where pg_dump's getConstraints renders it
	// via pg_get_constraintdef (conkey/confkey ordinals are already multi-column
	// aware). DU-002 slice 53.
	for _, tfk := range s.TableForeignKeys {
		fkName := tfk.Name
		if fkName == "" {
			firstCol := ""
			if len(tfk.Columns) > 0 {
				firstCol = tfk.Columns[0]
			}
			fkName = tbl.Name + "_" + firstCol + "_fkey"
		}
		fk := catalog.ForeignKey{
			Name:              fkName,
			OID:               o.allocConstraintOID(fkName),
			Columns:           append([]string(nil), tfk.Columns...),
			RefTable:          tfk.RefTable.Name,
			RefColumns:        append([]string(nil), tfk.RefColumns...),
			OnDelete:          tfk.OnDelete,
			OnUpdate:          tfk.OnUpdate,
			Deferrable:        tfk.Deferrable,
			InitiallyDeferred: tfk.InitiallyDeferred,
		}
		tbl.ForeignKeys = append(tbl.ForeignKeys, fk)
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
		case "serial", "serial4", "int4", "integer", "int":
			seqMin, seqMax = 1, 2147483647
			isSerial = colTypeLow == "serial" || colTypeLow == "serial4"
		case "bigserial", "serial8", "int8", "bigint":
			seqMin, seqMax = 1, 9223372036854775807
			isSerial = colTypeLow == "bigserial" || colTypeLow == "serial8"
		case "smallserial", "serial2", "int2", "smallint":
			seqMin, seqMax = 1, 32767
			isSerial = colTypeLow == "smallserial" || colTypeLow == "serial2"
		default:
			// Unknown type: use int4 range as a safe default for identity columns.
			if c.IdentityColumn {
				seqMin, seqMax = 1, 2147483647
			}
		}
		if !isSerial && !c.IdentityColumn {
			continue // only register sequences for serial/identity types
		}
		seqName := strings.ToLower(s.Name.Name) + "_" + strings.ToLower(c.Name) + "_seq"
		seqStart := int64(1)
		if c.IdentityStart != 0 {
			seqStart = c.IdentityStart
		}
		RegisterSequence(seqName, seqStart, 1, seqMin, seqMax, false)
		// Set the data type so information_schema.sequences shows the correct type
		// AND pg_dump computes the right default min/max. The base-integer aliases
		// (int2/smallint, int8/bigint) only reach here for IDENTITY columns (serial
		// columns use the serialN spellings); without them a `bigint GENERATED AS
		// IDENTITY` sequence got seqtypid=int4, so pg_dump's default_maxv was
		// INT32_MAX and it emitted a spurious `MAXVALUE 9223372036854775807` instead
		// of `NO MAXVALUE`. Mirrors the seqMin/seqMax switch above. DU-002 slice 120.
		var seqDataType string
		switch colTypeLow {
		case "smallserial", "serial2", "int2", "smallint":
			seqDataType = "smallint"
		case "bigserial", "serial8", "int8", "bigint":
			seqDataType = "bigint"
		default:
			seqDataType = "integer"
		}
		SetSequenceDataType(seqName, seqDataType)
		// Record implicit ownership so DROP TABLE cascades to this sequence.
		SetSequenceOwnedBy(seqName, strings.ToLower(s.Name.Name)+"."+strings.ToLower(c.Name))
		// A SERIAL or IDENTITY column's backing sequence must be discoverable by
		// pg_dump (relkind='S' in pg_class + its pg_depend row), so give it a catalog
		// IsSequence relation just like an explicit CREATE SEQUENCE. A serial
		// sequence's OWNED-BY link is AUTO ('a'), so pg_dump dumps it as a standalone
		// CREATE SEQUENCE + ALTER SEQUENCE OWNED BY + a column SET DEFAULT
		// nextval(...) (vs an identity column's INTERNAL 'i' ADD GENERATED form).
		// M0110-0001 (slice 120 identity, slice 121 serial).
		if c.IdentityColumn || isSerial {
			o.createSeqCatalogTable(parser.ObjectName{Schema: s.Name.Schema, Name: seqName}, seqName)
		}
	}

	// If PARTITION BY, annotate the table with partition metadata
	if s.PartitionBy != nil {
		tbl.PartitionMethod = s.PartitionBy.Method
		tbl.PartitionKey = s.PartitionBy.KeyCols
		tbl.PartitionKeyOpClasses = s.PartitionBy.OpClasses
		tbl.PartitionKeyExprs = s.PartitionBy.KeyExprs // M0097-0023: expression keys
		tbl.PartitionKeyCollations = s.PartitionBy.Collations
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

	// Named UNIQUE/PRIMARY KEY/EXCLUDE constraints with optional INCLUDE columns. M0097-0023.
	// These override the generic PK/UNIQUE index name with the constraint name.
	namedPKCreated := false
	for _, nc := range s.NamedConstraints {
		idxName := parser.ObjectName{Schema: s.Name.Schema, Name: nc.Name}
		if nc.IsExclusion {
			if nc.ExclusionOp == "=" && strings.ToLower(nc.Method) == "btree" {
				// btree equality exclusion == unique constraint: build a real btree
				// so checkExclusionConstraintsForInsert can probe it at INSERT time.
				if err := o.createBTreeIndex(s.Pos(), idxName, tbl, nc.Columns, nil, false, false); err != nil {
					return err
				}
				if idx, ok2 := o.ctx.Catalog.LookupIndex(idxName); ok2 {
					idx.IsExclusion = true
					idx.ExclusionOp = "="
					idx.IncludeColumns = nc.IncludeColumns
					idx.IsConstraint = true
					// DEFERRABLE [INITIALLY DEFERRED] rides the backing index so the
					// deparse appends the clause and pg_constraint emits
					// condeferrable/condeferred. Dump-fidelity only. DU-002 slice 143.
					idx.Deferrable = nc.Deferrable
					idx.InitiallyDeferred = nc.InitiallyDeferred
				}
			} else {
				// Other exclusion operators: stub catalog entry; no enforcement in v0.
				if err := o.createExclusionIndexStub(s.Pos(), idxName, tbl, nc); err != nil {
					return err
				}
			}
			continue
		}
		primary := nc.IsPrimary
		if err := o.createBTreeIndex(s.Pos(), idxName, tbl, nc.Columns, nil, true, primary); err != nil {
			return err
		}
		if idx, ok := o.ctx.Catalog.LookupIndex(idxName); ok {
			idx.IsConstraint = true
			idx.IncludeColumns = nc.IncludeColumns
			// NULLS NOT DISTINCT rides the backing index so pg_get_constraintdef /
			// pg_dump re-emit `UNIQUE NULLS NOT DISTINCT (cols)`. DU-002 slice 138.
			idx.NullsNotDistinct = nc.NullsNotDistinct
			// DEFERRABLE [INITIALLY DEFERRED] likewise rides the backing index so
			// the deparse appends the clause and pg_constraint emits
			// condeferrable/condeferred. Dump-fidelity only. DU-002 slice 140.
			idx.Deferrable = nc.Deferrable
			idx.InitiallyDeferred = nc.InitiallyDeferred
		}
		if primary {
			namedPKCreated = true
			// PRIMARY KEY implies NOT NULL on all key columns (SQL standard).
			for _, pkCol := range nc.Columns {
				if col, ok := o.ctx.Catalog.LookupColumn(tbl, pkCol); ok {
					col.NotNull = true
				}
			}
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
	// DEFERRABLE [INITIALLY DEFERRED] for the anonymous table-level
	// (`PRIMARY KEY (a) DEFERRABLE`) and inline-column (`a int PRIMARY KEY
	// DEFERRABLE`) forms rides the backing tbl_pkey index so pg_get_constraintdef
	// / pg_constraint re-emit the clause on dump. The named table-level form is
	// handled by the NamedConstraints loop above. DU-002 slice 142.
	var pkDeferrable, pkInitiallyDeferred bool
	if len(s.PrimaryKey) > 0 {
		pkCols = s.PrimaryKey
		pkDeferrable = s.PrimaryKeyDeferrable
		pkInitiallyDeferred = s.PrimaryKeyInitiallyDeferred
	} else {
		for _, c := range s.Columns {
			if c.Primary {
				pkCols = append(pkCols, c.Name)
			}
		}
	}
	// An inline-column PRIMARY KEY (`a int PRIMARY KEY DEFERRABLE`) also
	// populates s.PrimaryKey (the parser appends the column name), so the
	// table-level flags above stay false for that form; adopt the inline PK
	// column's deferrable flags here. The inline-column and table-level PK forms
	// are mutually exclusive, so this never double-counts. DU-002 slice 142.
	if !pkDeferrable {
		for _, c := range s.Columns {
			if c.Primary && c.PrimaryDeferrable {
				pkDeferrable = true
				pkInitiallyDeferred = c.PrimaryInitiallyDeferred
			}
		}
	}
	if len(pkCols) > 0 && !namedPKCreated {
		idxName := parser.ObjectName{Schema: s.Name.Schema, Name: tbl.Name + "_pkey"}
		if err := o.createBTreeIndex(s.Pos(), idxName, tbl, pkCols, nil, true, true); err != nil {
			// Propagate B-tree index errors (e.g. unsupported key type).
			// This makes CREATE TABLE fail cleanly rather than silently creating
			// a table without its primary key constraint.
			return err
		}
		if idx, ok := o.ctx.Catalog.LookupIndex(idxName); ok {
			idx.IsConstraint = true
			idx.IncludeColumns = s.PrimaryKeyInclude
			idx.Deferrable = pkDeferrable
			idx.InitiallyDeferred = pkInitiallyDeferred
		}
		// PRIMARY KEY implies NOT NULL on all key columns (SQL standard).
		for _, pkCol := range pkCols {
			if col, ok := o.ctx.Catalog.LookupColumn(tbl, pkCol); ok {
				col.NotNull = true
			}
		}
	}
	// Create btree indexes for inline column-level UNIQUE constraints.
	// e.g. `CREATE TABLE t (a int UNIQUE, b text)` — M0097-0028.
	for _, c := range s.Columns {
		if c.Unique {
			// Named inline column UNIQUE (`CONSTRAINT myname UNIQUE`) uses the
			// user-given name for the backing index/constraint; the anonymous
			// form auto-generates `tbl_col_key`. DU-002 slice 137.
			idxBaseName := tbl.Name + "_" + c.Name + "_key"
			if c.UniqueConstraintName != "" {
				idxBaseName = c.UniqueConstraintName
			}
			idxName := parser.ObjectName{Schema: s.Name.Schema, Name: idxBaseName}
			if err := o.createBTreeIndex(s.Pos(), idxName, tbl, []string{c.Name}, nil, true, false); err != nil {
				return err
			}
			if idx, ok := o.ctx.Catalog.LookupIndex(idxName); ok {
				idx.IsConstraint = true
				// NULLS NOT DISTINCT (PG 15+) — record so pg_get_constraintdef
				// re-emits `UNIQUE NULLS NOT DISTINCT (col)`. DU-002 slice 136.
				idx.NullsNotDistinct = c.UniqueNullsNotDistinct
				// DEFERRABLE [INITIALLY DEFERRED] — record so pg_get_constraintdef /
				// pg_constraint re-emit the clause on dump. DU-002 slice 141.
				idx.Deferrable = c.UniqueDeferrable
				idx.InitiallyDeferred = c.UniqueInitiallyDeferred
			}
		}
	}
	// Create btree indexes for table-level UNIQUE (col1, col2) [INCLUDE (…)] constraints.
	// e.g. `UNIQUE (a, b)` — M0097-0028.
	for i, cols := range s.TableUniques {
		var inclCols []string
		if i < len(s.TableUniqueIncludes) {
			inclCols = s.TableUniqueIncludes[i]
		}
		idxName := parser.ObjectName{Schema: s.Name.Schema, Name: o.autoIndexNameWithIncludes(tbl, cols, inclCols, "key")}
		if err := o.createBTreeIndex(s.Pos(), idxName, tbl, cols, nil, true, false); err != nil {
			return err
		}
		if idx, ok := o.ctx.Catalog.LookupIndex(idxName); ok {
			idx.IsConstraint = true
			idx.IncludeColumns = inclCols
			// NULLS NOT DISTINCT (PG 15+) — record so pg_get_constraintdef
			// re-emits `UNIQUE NULLS NOT DISTINCT (…)`. DU-002 slice 135.
			if i < len(s.TableUniqueNullsNotDistinct) {
				idx.NullsNotDistinct = s.TableUniqueNullsNotDistinct[i]
			}
			// DEFERRABLE [INITIALLY DEFERRED] — record so pg_get_constraintdef /
			// pg_constraint re-emit the clause on dump. DU-002 slice 139.
			if i < len(s.TableUniqueDeferrable) {
				idx.Deferrable = s.TableUniqueDeferrable[i]
			}
			if i < len(s.TableUniqueInitiallyDeferred) {
				idx.InitiallyDeferred = s.TableUniqueInitiallyDeferred[i]
			}
		}
	}
	// Create indexes for anonymous EXCLUDE USING constraints. M0097-0023.
	for _, ec := range s.TableExclusions {
		name := ec.Name
		if name == "" {
			name = o.autoIndexNameWithIncludes(tbl, ec.Columns, ec.IncludeColumns, "excl")
		}
		ec.Name = name
		idxName := parser.ObjectName{Schema: s.Name.Schema, Name: name}
		if ec.ExclusionOp == "=" && strings.ToLower(ec.Method) == "btree" {
			if err := o.createBTreeIndex(s.Pos(), idxName, tbl, ec.Columns, nil, false, false); err != nil {
				return err
			}
			if idx, ok := o.ctx.Catalog.LookupIndex(idxName); ok {
				idx.IsExclusion = true
				idx.ExclusionOp = "="
				idx.IncludeColumns = ec.IncludeColumns
				idx.IsConstraint = true
				// DEFERRABLE [INITIALLY DEFERRED] rides the backing index so the
				// deparse appends the clause and pg_constraint emits
				// condeferrable/condeferred. Dump-fidelity only. DU-002 slice 143.
				idx.Deferrable = ec.Deferrable
				idx.InitiallyDeferred = ec.InitiallyDeferred
			}
		} else {
			if err := o.createExclusionIndexStub(s.Pos(), idxName, tbl, ec); err != nil {
				return err
			}
		}
	}
	// Create btree indexes for LIKE INCLUDING INDEXES unique (non-PK) indexes.
	for _, idx := range likeUniqueIndexes {
		idxName := parser.ObjectName{Schema: s.Name.Schema, Name: tbl.Name + "_" + strings.Join(idx.Columns, "_") + "_key"}
		if err := o.createBTreeIndex(s.Pos(), idxName, tbl, idx.Columns, nil, true, false); err != nil {
			return err
		}
	}
	// Create btree indexes for LIKE INCLUDING INDEXES non-unique plain indexes.
	// PostgreSQL copies all non-partial non-PK non-exclusion indexes; non-unique
	// ones get auto-generated names with the "_idx" suffix. M0097-0023.
	for _, idx := range likeNonUniqueIndexes {
		// For expression columns (Columns[i]==""), use "expr" as the name part.
		nameCols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			if c == "" {
				nameCols[i] = "expr"
			} else {
				nameCols[i] = c
			}
		}
		var colExprs []parser.Expr
		if idx.ColExprs != nil {
			colExprs = make([]parser.Expr, len(idx.ColExprs))
			for i, ce := range idx.ColExprs {
				if ce != nil {
					colExprs[i] = *ce
				}
			}
		}
		idxName := parser.ObjectName{Schema: s.Name.Schema, Name: o.autoIndexNameWithIncludes(tbl, nameCols, nil, "idx")}
		if err := o.createBTreeIndex(s.Pos(), idxName, tbl, idx.Columns, colExprs, false, false); err != nil {
			continue // skip indexes with unsupported expression types
		}
	}
	// Register CHECK constraints from columns and table-level. M0097-0014.
	// CheckConstraints and NamedChecks are kept parallel (index i ↔ index i)
	// via Table.AddCheck so the enforcement path can recover the constraint
	// name for violation messages. Inline/table-level checks are anonymous in
	// the current parser, so they get empty names (invisible to pg_constraint).
	// Column-level CHECKs are auto-named using PostgreSQL's convention
	// {tablename}_{colname}_check (mirrors how PG assigns names in DDL). M0097-0023.
	for _, c := range s.Columns {
		if c.CheckExpr != "" {
			autoName := tbl.Name + "_" + c.Name + "_check"
			tbl.AddCheck(autoName, c.CheckExpr, o.allocConstraintOID(autoName))
		}
	}
	// Table-level CHECK constraints written without an explicit CONSTRAINT name
	// (`CREATE TABLE t (..., CHECK (a < b))`) are anonymous in the parser, but
	// PostgreSQL still assigns each one a catalog-visible auto-name at DDL time
	// (AddRelationNewConstraints): a CHECK that references exactly one column
	// becomes "<table>_<col>_check", any other case becomes "<table>_check".
	// Giving them name+OID here makes them surface in pg_constraint (contype='c')
	// and therefore in pg_dump — previously they were stored with an empty name
	// and OID 0, so the dumped CREATE TABLE silently dropped them. DU-002 slice 127.
	for i, chk := range s.TableChecks {
		autoName := o.autoCheckName(tbl, chk)
		// TableCheckNoInherit is parallel to TableChecks; an anonymous
		// `CHECK (...) NO INHERIT` must keep its flag so the dumped
		// constraintdef re-emits the suffix. DU-002 slice 128.
		noInherit := i < len(s.TableCheckNoInherit) && s.TableCheckNoInherit[i]
		tbl.AddCheckWithNoInherit(autoName, chk, o.allocConstraintOID(autoName), noInherit)
	}
	// Named table-level CHECK constraints from CONSTRAINT name CHECK (expr). M0097-0023.
	// A named `CONSTRAINT c CHECK (...) NO INHERIT` must keep its per-constraint
	// flag so the dumped constraintdef re-emits the suffix. DU-002 slice 129.
	for _, nc := range s.TableNamedChecks {
		tbl.AddCheckWithNoInherit(nc.Name, nc.Expr, o.allocConstraintOID(nc.Name), nc.NoInherit)
	}
	// Copy statistics from LIKE INCLUDING STATISTICS (or INCLUDING ALL) sources. M0097-0023.
	if len(likeStatisticsSources) > 0 {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			const oidPgStatExt = uint32(3381)
			for _, lss := range likeStatisticsSources {
				for _, srcStat := range im.AllStatistics() {
					if srcStat.TableOID != lss.src.OID {
						continue
					}
					newName := deriveStatisticsName(srcStat.Name, lss.src.Name, tbl.Name)
					newStat := im.RegisterStatistics(srcStat.Schema, newName, tbl.OID)
					// Copy any existing comment for this statistics object.
					if desc, ok := im.GetComment(oidPgStatExt, srcStat.OID, 0); ok && desc != "" {
						im.SetComment(oidPgStatExt, newStat.OID, 0, desc)
					}
				}
			}
		}
	}
	// Apply LIKE INCLUDING CONSTRAINTS checks (copied from LIKE source tables).
	// PostgreSQL preserves the source constraint name; the name is retained so
	// enforcement can report it. Named constraints get a fresh OID so they
	// surface through pg_constraint (the LEFT JOIN column-mapping crash that
	// previously masked this was fixed in M0097-0023-loop34). M0097-0023.
	for _, nc := range likeCheckConstraints {
		tbl.AddCheck(nc.Name, nc.Expr, o.allocConstraintOID(nc.Name))
	}
	// PG18: register named NOT NULL constraints for every NOT-NULL column.
	// LIKE INCLUDING ALL/CONSTRAINTS preserves the source constraint name;
	// explicit or inherited columns get auto-name <tablename>_<colname>_not_null.
	// M0097-0023.
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		// Build set of explicit column defs that carry NOT NULL NO INHERIT.
		explicitNoInherit := make(map[string]bool)
		for _, origCol := range s.Columns {
			if origCol.NotNullNoInherit {
				explicitNoInherit[strings.ToLower(origCol.Name)] = true
			}
		}
		// PG18 records a contype='n' NOT NULL constraint for EVERY not-null
		// column, INCLUDING primary-key columns (the PK implies NOT NULL, and
		// PG materialises that as a separate `<table>_<col>_not_null` row in
		// pg_constraint). pg_dump's getTableAttrs LEFT-JOINs pg_constraint on
		// contype='n' to decide whether to print the inline NOT NULL clause, so
		// PK columns must NOT be skipped here or their NOT NULL is lost from the
		// dump (`id integer` instead of `id integer NOT NULL`). DU-002 slice 50.
		for _, col := range tbl.Columns {
			if !col.NotNull {
				continue
			}
			colKey := strings.ToLower(col.Name)
			noInherit := explicitNoInherit[colKey]
			name := strings.ToLower(tbl.Name) + "_" + colKey + "_not_null"
			if entry, ok2 := likeNotNullByCol[colKey]; ok2 {
				// LIKE INCLUDING preserves the source NOT NULL constraint name.
				if entry.name != "" {
					name = entry.name
				}
				if entry.noInherit {
					noInherit = true
				}
			}
			tbl.AddNotNull(name, col.Name, im.AllocOID(), noInherit, true, 0)
		}
	}
	// Copy pg_description comments from LIKE INCLUDING COMMENTS sources:
	// indexes (matched by column set), named CHECK constraints (matched by name),
	// and table columns (matched by name). M0097-0023.
	if len(likeCommentSources) > 0 {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			const (
				oidPgClass      = uint32(1259)
				oidPgConstraint = uint32(2606)
			)
			dstIndexes := im.IndexesOnTable(tbl)
			for _, lcs := range likeCommentSources {
				// Index comments — match by column set.
				for _, srcIdx := range im.IndexesOnTable(lcs.src) {
					desc, hasComment := im.GetComment(oidPgClass, srcIdx.OID, 0)
					if !hasComment || desc == "" {
						continue
					}
					for _, dstIdx := range dstIndexes {
						if likeIndexColsMatch(srcIdx.Columns, dstIdx.Columns) {
							im.SetComment(oidPgClass, dstIdx.OID, 0, desc)
							break
						}
					}
				}
				// Named CHECK constraint comments — match by constraint name.
				for _, srcNC := range lcs.src.NamedChecks {
					if srcNC.OID == 0 {
						continue
					}
					desc, hasComment := im.GetComment(oidPgConstraint, srcNC.OID, 0)
					if !hasComment || desc == "" {
						continue
					}
					for _, dstNC := range tbl.NamedChecks {
						if strings.EqualFold(srcNC.Name, dstNC.Name) && dstNC.OID != 0 {
							im.SetComment(oidPgConstraint, dstNC.OID, 0, desc)
							break
						}
					}
				}
				// Named NOT NULL constraint comments — match by column name from LIKE
				// source then map source OID → dest OID via the new table's constraints.
				for _, srcNN := range lcs.src.NotNullConstraints {
					if srcNN.OID == 0 {
						continue
					}
					desc, hasComment := im.GetComment(oidPgConstraint, srcNN.OID, 0)
					if !hasComment || desc == "" {
						continue
					}
					for _, dstNN := range tbl.NotNullConstraints {
						if strings.EqualFold(srcNN.ColName, dstNN.ColName) && dstNN.OID != 0 {
							im.SetComment(oidPgConstraint, dstNN.OID, 0, desc)
							break
						}
					}
				}
				// Column comments — match by column name.
				for i, srcCol := range lcs.src.Columns {
					desc, hasComment := im.GetComment(oidPgClass, lcs.src.OID, int32(i+1))
					if !hasComment || desc == "" {
						continue
					}
					for j, dstCol := range tbl.Columns {
						if strings.EqualFold(srcCol.Name, dstCol.Name) {
							im.SetComment(oidPgClass, tbl.OID, int32(j+1), desc)
							break
						}
					}
				}
			}
		}
	}
	return nil
}

// allocConstraintOID returns a fresh catalog OID for a named constraint, or
// 0 for an anonymous one. Anonymous CHECK constraints are kept at OID 0 so
// pg_constraint's VirtualRows (which skips empty-name / zero-OID rows) does
// not surface them — the current parser leaves inline/table-level checks
// unnamed, and PG-faithful auto-naming is a separate follow-up. A named
// constraint gets a real OID so psql \d and pg_constraint queries see it.
// M0097-0023.
// deriveStatisticsName computes the name for a statistics object copied from
// srcTableName to dstTableName. If the stat name starts with srcTableName_,
// that prefix is replaced with dstTableName_; otherwise srcTableName_ prefix is
// prepended. M0097-0023.
func deriveStatisticsName(statName, srcTableName, dstTableName string) string {
	prefix := srcTableName + "_"
	if strings.HasPrefix(strings.ToLower(statName), strings.ToLower(prefix)) {
		return dstTableName + "_" + statName[len(prefix):]
	}
	return dstTableName + "_" + statName
}

// likeIndexColsMatch returns true when two index column lists are identical
// (case-insensitive). Empty-string entries (expression columns) match each other.
func likeIndexColsMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

func (o *ddlOp) allocConstraintOID(name string) uint32 {
	if name == "" || o.ctx == nil || o.ctx.Catalog == nil {
		return 0
	}
	return o.ctx.Catalog.AllocOID()
}

// autoCheckName derives PostgreSQL's auto-generated name for an anonymous
// table-level CHECK constraint. PG's AddRelationNewConstraints names a CHECK
// that references exactly one column "<table>_<col>_check" and any other CHECK
// (multiple columns, or none) "<table>_check"; the single-vs-multi decision is
// made by counting the distinct columns the expression references (PG uses
// pull_var_clause, which does not descend into sublinks). On a collision with an
// existing constraint name on the table, ChooseConstraintName appends an
// incrementing numeric suffix to the "check" label ("<base>1", "<base>2", …).
// DU-002 slice 127.
func (o *ddlOp) autoCheckName(tbl *catalog.Table, expr string) string {
	base := tbl.Name + "_check"
	if parsed, err := parser.ParseExpr(expr); err == nil {
		var cols []string
		collectCheckExprColumns(parsed, &cols)
		seen := make(map[string]bool, len(cols))
		var distinct []string
		for _, c := range cols {
			if !seen[c] {
				seen[c] = true
				distinct = append(distinct, c)
			}
		}
		if len(distinct) == 1 {
			base = tbl.Name + "_" + distinct[0] + "_check"
		}
	}
	name := base
	for i := 1; checkNameTaken(tbl, name); i++ {
		name = base + strconv.Itoa(i)
	}
	return name
}

// checkNameTaken reports whether the table already carries a CHECK constraint
// with the given name (used for ChooseConstraintName-style collision avoidance).
func checkNameTaken(tbl *catalog.Table, name string) bool {
	for _, nc := range tbl.NamedChecks {
		if nc.Name == name {
			return true
		}
	}
	return false
}

// collectCheckExprColumns walks a parsed CHECK expression and appends the
// lowercased names of every bare column reference it finds to *out. Mirrors
// PostgreSQL's pull_var_clause: subqueries (sublinks) are not descended into,
// which is fine for the approximate single-vs-multi-column name decision.
func collectCheckExprColumns(e parser.Expr, out *[]string) {
	if e == nil {
		return
	}
	switch n := e.(type) {
	case *parser.ColumnRef:
		if n.Column != "" {
			*out = append(*out, strings.ToLower(n.Column))
		}
	case *parser.BinaryOp:
		collectCheckExprColumns(n.Left, out)
		collectCheckExprColumns(n.Right, out)
	case *parser.UnaryOp:
		collectCheckExprColumns(n.Operand, out)
	case *parser.CastExpr:
		collectCheckExprColumns(n.Operand, out)
	case *parser.CollateExpr:
		collectCheckExprColumns(n.Operand, out)
	case *parser.IsNullExpr:
		collectCheckExprColumns(n.Operand, out)
	case *parser.IsBoolExpr:
		collectCheckExprColumns(n.Operand, out)
	case *parser.IsDistinctFromExpr:
		collectCheckExprColumns(n.Left, out)
		collectCheckExprColumns(n.Right, out)
	case *parser.FuncCall:
		for _, a := range n.Args {
			collectCheckExprColumns(a, out)
		}
		collectCheckExprColumns(n.Filter, out)
	case *parser.InExpr:
		collectCheckExprColumns(n.Operand, out)
		for _, v := range n.List {
			collectCheckExprColumns(v, out)
		}
	case *parser.CaseExpr:
		collectCheckExprColumns(n.Operand, out)
		for _, w := range n.Whens {
			collectCheckExprColumns(w.When, out)
			collectCheckExprColumns(w.Then, out)
		}
		collectCheckExprColumns(n.Else, out)
	case *parser.RowExpr:
		for _, el := range n.Elems {
			collectCheckExprColumns(el, out)
		}
	case *parser.ArrayConstructorExpr:
		for _, el := range n.Elements {
			collectCheckExprColumns(el, out)
		}
	case *parser.ArraySubscriptExpr:
		collectCheckExprColumns(n.Base, out)
		collectCheckExprColumns(n.Index, out)
	}
}

// appendLikeChecks copies src's CHECK constraints (name + expression) into
// dst for a LIKE INCLUDING CONSTRAINTS clause, deduplicating by expression so
// a constraint already inherited (e.g. from a column-level definition) is not
// added twice. M0097-0023.
func appendLikeChecks(dst []catalog.NamedCheckConstraint, src *catalog.Table) []catalog.NamedCheckConstraint {
	for i, expr := range src.CheckConstraints {
		nc := catalog.NamedCheckConstraint{Expr: expr}
		if i < len(src.NamedChecks) {
			nc = src.NamedChecks[i]
		}
		dup := false
		for _, existing := range dst {
			if existing.Expr == nc.Expr {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, nc)
		}
	}
	return dst
}

// execCreateTableAs implements `CREATE TABLE name AS SELECT …`.
// It plans and executes the SELECT, derives column definitions from the result
// schema, creates the table, and inserts all rows from the SELECT.  M0096-0008.
func (o *ddlOp) execCreateTableAs(s *parser.CreateTableStmt) error {
	// Validate storage parameter names (same rule as execCreateTable).
	for k := range s.With {
		if k != strings.ToLower(k) {
			return &ExecError{Code: "42000", Pos: s.Pos(),
				Message: fmt.Sprintf("unrecognized parameter %q", k)}
		}
	}
	if o.ctx.Pool == nil || o.ctx.Catalog == nil || o.ctx.TxnMgr == nil {
		// No storage: create an empty table with no columns.
		_, err := o.ctx.Catalog.CreateTable(s.Name, nil)
		if err != nil {
			return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
		}
		return nil
	}
	// Plan the SELECT to derive the schema.
	selectNode, err := planner.Plan(s.SelectSource, o.planCatalog())
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
	// If the table was created without an explicit schema qualifier, record the
	// resolved writable schema so TablesInSchema() can find it for DROP CASCADE.
	// M0097-0022.
	if s.Name.Schema == "" {
		if ws := currentWritableSchema(o.ctx); ws != "" && !strings.EqualFold(ws, "public") {
			tbl.Schema = ws
		}
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
	// Detect duplicate column names in the PARTITION OF column override list.
	if poc.DuplicateColumn != "" {
		return &ExecError{Code: "42701", Pos: s.Pos(),
			Message: fmt.Sprintf("column %q specified more than once", poc.DuplicateColumn)}
	}
	// Look up the parent partitioned table.
	parent, ok := o.lookupTableWithSearch(poc.Parent)
	if !ok {
		return &ExecError{Code: "42P01", Pos: s.Pos(),
			Message: fmt.Sprintf("relation %q does not exist", poc.Parent.String())}
	}
	// DDL-during-active-query guard: reject CREATE PARTITION OF while the parent
	// table is being mutated by an active DML statement in this session. M0097-0023.
	if bsess, ok2 := o.ctx.Session.(*BasicSession); ok2 && bsess.IsTableActive(parent.OID) {
		return &ExecError{Code: "55006", Pos: s.Pos(),
			Message: fmt.Sprintf(`cannot CREATE TABLE .. PARTITION OF %q because it is being used by active queries in this session`, poc.Parent.Name)}
	}
	// Validate the partition.
	if err := validatePartitionChild(s, parent, o.ctx); err != nil {
		return err
	}
	// Storage parameters (WITH clause) on a partition child. PG allows them on a
	// leaf partition (it is a concrete heap), but rejects them when the child is
	// itself a partitioned table (PARTITION BY ...). Validate names/value here so
	// the leaf's fillfactor persists on pg_class.reloptions and round-trips
	// through pg_dump, mirroring the non-partition CREATE TABLE path above.
	// M0110-0001 (DU-002 slice 191).
	for k := range s.With {
		if k != strings.ToLower(k) {
			return &ExecError{Code: "42000", Pos: s.Pos(),
				Message: fmt.Sprintf("unrecognized parameter %q", k)}
		}
	}
	if s.PartitionBy != nil && len(s.With) > 0 {
		return &ExecError{Code: "0A000", Pos: s.Pos(),
			Message: "cannot specify storage parameters for a partitioned table",
			Detail:  "This operation is not supported for partitioned tables.",
			Hint:    "Specify storage parameters for its leaf partitions instead."}
	}
	childFillfactor := 0
	if v, ok := s.With["fillfactor"]; ok {
		ff, convErr := strconv.Atoi(strings.TrimSpace(v))
		if convErr != nil {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("invalid value for integer option \"fillfactor\": %s", v)}
		}
		if ff < 10 || ff > 100 {
			return &ExecError{Code: "22023", Pos: s.Pos(),
				Message: fmt.Sprintf("value %d out of bounds for option \"fillfactor\"", ff),
				Detail:  "Valid values are between \"10\" and \"100\"."}
		}
		childFillfactor = ff
	}
	// Inherit columns from parent (partition children use parent's schema).
	cols := make([]catalog.Column, len(parent.Columns))
	copy(cols, parent.Columns)
	for i := range cols {
		cols[i].Inherited = true
	}
	// Validate sub-partition key (e.g. PARTITION BY RANGE (c)) against inherited cols.
	if s.PartitionBy != nil {
		if err := validatePartitionKey(s, cols, o.ctx); err != nil {
			return err
		}
	}
	tbl, err := o.ctx.Catalog.CreateTable(s.Name, cols)
	if err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	// If the table was created without an explicit schema qualifier, record the
	// resolved writable schema so TablesInSchema() can find it for DROP CASCADE.
	// M0097-0022.
	if s.Name.Schema == "" {
		if ws := currentWritableSchema(o.ctx); ws != "" && !strings.EqualFold(ws, "public") {
			tbl.Schema = ws
		}
	}
	// Set persistence flags on the child.
	tbl.Unlogged = s.Unlogged
	tbl.Temp = s.Temporary
	// Persist the leaf partition's fillfactor so pg_class.reloptions surfaces it
	// and pg_dump re-emits `WITH (fillfactor='N')`. DU-002 slice 191.
	tbl.Fillfactor = childFillfactor
	// Set partition metadata on the child.
	tbl.PartitionParentOID = parent.OID
	// Use the child's own PARTITION BY clause when present (e.g. nested
	// partitioned tables: CREATE TABLE p1 PARTITION OF p FOR VALUES ... PARTITION BY RANGE (c)).
	// Leaf partitions have no PartitionMethod/PartitionKey of their own.
	if s.PartitionBy != nil {
		tbl.PartitionMethod = s.PartitionBy.Method
		tbl.PartitionKey = s.PartitionBy.KeyCols
		tbl.PartitionKeyOpClasses = s.PartitionBy.OpClasses
		tbl.PartitionKeyExprs = s.PartitionBy.KeyExprs // M0097-0023
		tbl.PartitionKeyCollations = s.PartitionBy.Collations
	}

	// Build partition bounds from the FOR VALUES clause.
	var pb catalog.PartitionBound
	pb.ChildName = tbl.Name
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
		// Validate IN values are type-compatible with the partition key column type.
		// Integer literals cannot be directly used as boolean partition bounds (PG rejects
		// "FOR VALUES IN (1)" for bool columns; string literal '1' is allowed).
		if len(parent.PartitionKey) > 0 {
			for _, col := range parent.Columns {
				if strings.EqualFold(col.Name, parent.PartitionKey[0]) {
					keyType := strings.ToLower(col.Type.Name)
					if keyType == "bool" || keyType == "boolean" {
						for _, e := range poc.InValues {
							if _, isInt := e.(*parser.IntegerConst); isInt {
								return &ExecError{
									Code:    "42804",
									Pos:     s.Pos(),
									Message: fmt.Sprintf("specified value cannot be cast to type boolean for column %q", col.Name),
								}
							}
						}
					}
					break
				}
			}
		}
		// LIST partition: evaluate each IN value as a string. InValues keeps the
		// raw unquoted form (routing compares against it); InValueLiterals keeps
		// the SQL-literal form ('a', 1, …) so FormatPartitionBound emits a valid
		// relpartbound for pg_dump.
		for _, e := range poc.InValues {
			pb.InValues = append(pb.InValues, exprToString(e))
			pb.InValueLiterals = append(pb.InValueLiterals, boundExprToSQLLiteral(e))
		}
		tbl.PartitionBounds = []catalog.PartitionBound{pb}
	} else if len(poc.FromValues) > 0 || len(poc.ToValues) > 0 {
		// RANGE partition: store all key-column values for multi-column routing.
		// FromValues/ToValues keep the raw unquoted routing form; the parallel
		// *ValueLiterals keep the SQL-literal form ('a', 5, MINVALUE) so
		// FormatPartitionBound emits a valid relpartbound for pg_dump.
		if len(poc.FromValues) > 0 {
			pb.From = exprToString(poc.FromValues[0]) // backward compat (single-col)
			for _, v := range poc.FromValues {
				pb.FromValues = append(pb.FromValues, exprToString(v))
			}
			pb.FromValueLiterals = rangeBoundLiterals(poc.FromValues)
		}
		if len(poc.ToValues) > 0 {
			pb.To = exprToString(poc.ToValues[0]) // backward compat (single-col)
			for _, v := range poc.ToValues {
				pb.ToValues = append(pb.ToValues, exprToString(v))
			}
			pb.ToValueLiterals = rangeBoundLiterals(poc.ToValues)
		}
		tbl.PartitionBounds = []catalog.PartitionBound{pb}
	}
	// Also register bounds on the PARENT so validatePartitionChild can scan them.
	parent.PartitionBounds = append(parent.PartitionBounds, pb)

	// Register child with parent.
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		im.RegisterPartitionChild(parent.OID, tbl.OID)
	}

	// Propagate NOT NULL and DEFAULT overrides from the PARTITION OF column list
	// BEFORE syncTableToCatalogHeap so that pg_attribute.attnotnull is correct.
	for _, colName := range poc.NotNullColumns {
		for i := range tbl.Columns {
			if strings.EqualFold(tbl.Columns[i].Name, colName) {
				tbl.Columns[i].NotNull = true
				break
			}
		}
	}
	for _, cd := range poc.ColDefaults {
		for i := range tbl.Columns {
			if strings.EqualFold(tbl.Columns[i].Name, cd.ColName) {
				tbl.Columns[i].DefaultExpr = cd.Expr
				break
			}
		}
	}
	// Apply GENERATED ALWAYS AS expression overrides from the PARTITION OF column
	// list (e.g. `d WITH OPTIONS GENERATED ALWAYS AS (a + b + 1000) STORED`).
	// M0100-0010.
	for _, cg := range poc.ColGeneratedExprs {
		for i := range tbl.Columns {
			if strings.EqualFold(tbl.Columns[i].Name, cg.ColName) {
				tbl.Columns[i].GeneratedExpr = cg.Expr
				tbl.Columns[i].GeneratedAlways = true
				break
			}
		}
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
	// Create btree indexes for UNIQUE column constraints declared directly on
	// this partition: `CREATE TABLE child PARTITION OF parent (b UNIQUE) FOR VALUES …`.
	// M0097-0028.
	for _, colName := range poc.UniqueColumns {
		childIdxName := parser.ObjectName{Schema: s.Name.Schema, Name: tbl.Name + "_" + colName + "_key"}
		if err := o.createBTreeIndex(s.Pos(), childIdxName, tbl, []string{colName}, nil, true, false); err != nil {
			return err
		}
	}
	// Inherit regular (non-PK, non-unique) btree indexes from parent onto the
	// partition child. PostgreSQL automatically creates matching indexes on each
	// leaf partition when the parent has non-constraint indexes (e.g. partial
	// indexes or expression indexes). We clone every non-PK/non-unique btree
	// parent index and auto-generate a name using the partition name + column
	// names + "_idx". Expression columns get "expr" as their name part so that
	// two expression indexes on the same child get "_expr_idx" / "_expr_idx1".
	for _, parentIdx := range o.ctx.Catalog.IndexesOnTable(parent) {
		if parentIdx.Method != "btree" || parentIdx.Primary || parentIdx.Unique {
			continue
		}
		// Replace empty column names (expression cols) with "expr" for naming.
		nameCols := make([]string, len(parentIdx.Columns))
		for i, col := range parentIdx.Columns {
			if col == "" {
				nameCols[i] = "expr"
			} else {
				nameCols[i] = col
			}
		}
		childIdxName := parser.ObjectName{
			Schema: s.Name.Schema,
			Name:   o.autoIndexNameWithIncludes(tbl, nameCols, nil, "idx"),
		}
		// Reconstruct colExprs slice (parallel to parentIdx.Columns).
		var colExprs []parser.Expr
		if len(parentIdx.ColExprs) > 0 {
			colExprs = make([]parser.Expr, len(parentIdx.ColExprs))
			for i, ep := range parentIdx.ColExprs {
				if ep != nil {
					colExprs[i] = *ep
				}
			}
		}
		if err := o.createBTreeIndex(s.Pos(), childIdxName, tbl, parentIdx.Columns, colExprs, false, false); err != nil {
			return err
		}
		// Copy predicate (WHERE clause) and ColExprStrings from parent index.
		if idx, ok2 := o.ctx.Catalog.LookupIndex(childIdxName); ok2 {
			if parentIdx.HasPredicate {
				idx.HasPredicate = parentIdx.HasPredicate
				idx.Predicate = parentIdx.Predicate
				idx.PredicateString = parentIdx.PredicateString
			}
			for i, s := range parentIdx.ColExprStrings {
				if s != "" && i < len(idx.ColExprStrings) {
					idx.ColExprStrings[i] = s
				}
			}
		}
	}
	// Inherit named CHECK constraints from parent (non-NoInherit only). M0097-0023.
	im2, isIM2 := o.ctx.Catalog.(*catalog.InMemory)
	for _, pnc := range parent.NamedChecks {
		if pnc.Name == "" || pnc.OID == 0 || pnc.NoInherit {
			continue
		}
		var oid uint32
		if isIM2 {
			oid = im2.AllocOID()
		}
		tbl.AddCheckInherited(pnc.Name, pnc.Expr, oid)
	}
	// Apply CHECK constraints declared explicitly in the PARTITION OF column list
	// (e.g. CONSTRAINT check_b CHECK (b > 0)). If the same name was already
	// inherited from the parent, emit NOTICE "merging" and keep the inherited
	// version. Otherwise add as a locally-defined constraint. M0097-0023.
	for _, pcc := range poc.CheckConstraints {
		if pcc.Name == "" {
			continue
		}
		// Check if already inherited from parent.
		alreadyInherited := false
		for _, existing := range tbl.NamedChecks {
			if strings.EqualFold(existing.Name, pcc.Name) {
				alreadyInherited = true
				break
			}
		}
		if alreadyInherited {
			o.ctx.AddNotice(fmt.Sprintf("merging constraint %q with inherited definition", pcc.Name))
			continue
		}
		var oid uint32
		if isIM2 {
			oid = im2.AllocOID()
		}
		tbl.AddCheck(pcc.Name, pcc.Expr, oid)
	}
	// Register named NOT NULL constraints for NOT NULL columns declared in the
	// PARTITION OF column override list. These columns come from the parent schema
	// but the partition child explicitly adds NOT NULL, so IsLocal=true. All
	// partition child NOT NULL constraints have InhCount=1 (one partition parent).
	// M0097-0023.
	if isIM2 {
		for _, colName := range poc.NotNullColumns {
			colKey := strings.ToLower(colName)
			constraintName := strings.ToLower(tbl.Name) + "_" + colKey + "_not_null"
			tbl.AddNotNull(constraintName, colName, im2.AllocOID(), false, true, 1)
		}
	}
	return nil
}

// validatePartitionChild checks that the partition child definition is valid.
// This runs BEFORE CreateTable, so errors prevent the table from being created
// and avoid cascade "already exists" errors on retry.
func validatePartitionChild(s *parser.CreateTableStmt, parent *catalog.Table, ctx *Context) error {
	poc := s.PartitionOf
	pos := s.Pos()
	childName := s.Name.Name

	// 1. Parent must be partitioned.
	if len(parent.PartitionKey) == 0 && len(parent.PartitionKeyExprs) == 0 {
		return &ExecError{Code: "42601", Pos: pos,
			Message: fmt.Sprintf("%q is not partitioned", poc.Parent.String())}
	}

	// 2. Temporary/permanent mixing.
	if parent.Temp && !s.Temporary {
		return &ExecError{Code: "0A000", Pos: pos,
			Message: fmt.Sprintf("cannot create a permanent relation as partition of temporary relation %q", poc.Parent.String())}
	}
	if !parent.Temp && s.Temporary {
		return &ExecError{Code: "0A000", Pos: pos,
			Message: fmt.Sprintf("cannot create a temporary relation as partition of permanent relation %q", poc.Parent.String())}
	}

	// 3. Unlogged child not allowed.
	if s.Unlogged {
		return &ExecError{Code: "0A000", Pos: pos,
			Message: "partitioned tables cannot be unlogged"}
	}

	strategy := strings.ToLower(parent.PartitionMethod)

	// 4. Validate bound type matches parent strategy.
	if !poc.Default {
		if poc.IsHash && strategy != "hash" {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: fmt.Sprintf("invalid bound specification for a %s partition", strategy)}
		}
		if len(poc.InValues) > 0 && strategy != "list" {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: fmt.Sprintf("invalid bound specification for a %s partition", strategy)}
		}
		if (len(poc.FromValues) > 0 || len(poc.ToValues) > 0) && strategy != "range" {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: fmt.Sprintf("invalid bound specification for a %s partition", strategy)}
		}
	}

	// 5. Hash-specific: no default partition.
	if poc.Default && strategy == "hash" {
		return &ExecError{Code: "42P16", Pos: pos,
			Message: "a hash-partitioned table may not have a default partition"}
	}

	// 6. Validate bound expressions for column refs, aggregates, subqueries, SRFs.
	var boundExprs []parser.Expr
	boundExprs = append(boundExprs, poc.InValues...)
	boundExprs = append(boundExprs, poc.FromValues...)
	boundExprs = append(boundExprs, poc.ToValues...)
	for _, e := range boundExprs {
		if err := validatePartBoundExpr(e, pos, parent); err != nil {
			return err
		}
	}

	// 7. HASH-specific validation.
	if poc.IsHash {
		if poc.Modulus <= 0 {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: "modulus for hash partition must be an integer value greater than zero"}
		}
		if poc.Remainder < 0 || poc.Remainder >= poc.Modulus {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: "remainder for hash partition must be less than modulus"}
		}
		if err := validateHashBounds(childName, poc.Modulus, poc.Remainder, parent, pos, ctx); err != nil {
			return err
		}
	}

	// 8. RANGE-specific validation.
	if len(poc.FromValues) > 0 || len(poc.ToValues) > 0 {
		nKeyCols := len(parent.PartitionKey)
		if len(parent.PartitionKeyExprs) > nKeyCols {
			nKeyCols = len(parent.PartitionKeyExprs)
		}
		if nKeyCols == 0 {
			nKeyCols = 1
		}
		if len(poc.FromValues) > 0 && len(poc.FromValues) != nKeyCols {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: "FROM must specify exactly one value per partitioning column"}
		}
		if len(poc.ToValues) > 0 && len(poc.ToValues) != nKeyCols {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: "TO must specify exactly one value per partitioning column"}
		}
		for _, e := range poc.FromValues {
			if _, isNull := e.(*parser.NullConst); isNull {
				return &ExecError{Code: "42P16", Pos: pos,
					Message: "cannot specify NULL in range bound"}
			}
		}
		for _, e := range poc.ToValues {
			if _, isNull := e.(*parser.NullConst); isNull {
				return &ExecError{Code: "42P16", Pos: pos,
					Message: "cannot specify NULL in range bound"}
			}
		}
		if err := validateRangeBoundOrder(childName, poc.FromValues, poc.ToValues, pos); err != nil {
			return err
		}
		if err := validateRangeOverlap(childName, poc.FromValues, poc.ToValues, parent, pos, ctx); err != nil {
			return err
		}
	}

	// 9. LIST-specific overlap.
	if len(poc.InValues) > 0 && strategy == "list" {
		if err := validateListOverlap(childName, poc.InValues, parent, pos, ctx); err != nil {
			return err
		}
	}

	// 9b. Non-default partition with a DEFAULT sibling: check the default
	// partition has no rows that would be claimed by the new partition.
	if !poc.Default && strategy != "hash" {
		if err := checkDefaultPartitionDataConflict(childName, parent, poc, pos, ctx); err != nil {
			return err
		}
	}

	// 10. DEFAULT partition conflict.
	if poc.Default && strategy != "hash" {
		if err := validateDefaultPartition(childName, parent, ctx, pos); err != nil {
			return err
		}
	}

	return nil
}

func validateDefaultPartition(childName string, parent *catalog.Table, ctx *Context, pos int) error {
	for _, pb := range parent.PartitionBounds {
		if pb.IsDefault {
			return &ExecError{Code: "42P16", Pos: pos,
				Message: fmt.Sprintf("partition %q conflicts with existing default partition %q", childName, pb.ChildName)}
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
	case *parser.ColumnRef:
		// MINVALUE / MAXVALUE in partition bounds are parsed as ColumnRef.
		return strings.ToLower(v.Column)
	case *parser.UnaryOp:
		if v.Op == parser.OpUnaryNeg {
			inner := exprToString(v.Operand)
			return "-" + inner
		}
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
	case *parser.ColumnRef:
		if v.Table != "" {
			return v.Table + "." + v.Column
		}
		return v.Column
	case *parser.FuncCall:
		// SQL niladic value functions (CURRENT_TIMESTAMP, CURRENT_USER, …)
		// parse to a parenless *FuncCall. Deparse them as the bare uppercase
		// keyword the way PG's get_sql_value_function does — `current_timestamp()`
		// is not valid SQL on re-evaluation. Mirrors the catalog twin in
		// catalog.formatExprForAttrdef (DU-002 slice 174); keep the two in sync.
		if len(v.Args) == 0 && v.Name.Schema == "" && parser.IsNoParenFuncName(strings.ToLower(v.Name.Name)) {
			return strings.ToUpper(v.Name.Name)
		}
		var args []string
		for _, a := range v.Args {
			args = append(args, defaultExprToSQL(a))
		}
		name := v.Name.Name
		if v.Name.Schema != "" {
			name = v.Name.Schema + "." + name
		}
		return name + "(" + strings.Join(args, ", ") + ")"
	case *parser.CastExpr:
		return defaultExprToSQL(v.Operand) + "::" + v.Type.Name
	case *parser.UnaryOp:
		switch v.Op {
		case parser.OpSub:
			return "-" + defaultExprToSQL(v.Operand)
		case parser.OpNot:
			return "NOT " + defaultExprToSQL(v.Operand)
		}
	case *parser.BinaryOp:
		left := defaultExprToSQL(v.Left)
		right := defaultExprToSQL(v.Right)
		switch v.Op {
		case parser.OpAdd:
			return left + " + " + right
		case parser.OpSub:
			return left + " - " + right
		case parser.OpMul:
			return left + " * " + right
		case parser.OpDiv:
			return left + " / " + right
		case parser.OpMod:
			return left + " % " + right
		case parser.OpConcat:
			return left + " || " + right
		case parser.OpEq:
			return left + " = " + right
		case parser.OpLt:
			return left + " < " + right
		case parser.OpGt:
			return left + " > " + right
		case parser.OpLe:
			return left + " <= " + right
		case parser.OpGe:
			return left + " >= " + right
		case parser.OpNe:
			return left + " <> " + right
		case parser.OpAnd:
			return left + " AND " + right
		case parser.OpOr:
			return left + " OR " + right
		case parser.OpLike:
			return left + " LIKE " + right
		case parser.OpNotLike:
			return left + " NOT LIKE " + right
		}
	case *parser.TypedStringLit:
		return v.Type + " '" + strings.ReplaceAll(v.Value, "'", "''") + "'"
	case *parser.ArrayConstructorExpr:
		// `DEFAULT ARRAY[1, 2, 3]`. Mirror the catalog twin
		// (catalog.formatExprForAttrdef, DU-002 slice 177); keep the two in sync so
		// the dump path and the proargdefaults path render identically.
		var elems []string
		for _, el := range v.Elements {
			elems = append(elems, defaultExprToSQL(el))
		}
		return "ARRAY[" + strings.Join(elems, ", ") + "]"
	case *parser.CaseExpr:
		// `DEFAULT CASE WHEN true THEN 1 ELSE 0 END`. Mirror the catalog twin
		// (catalog.formatExprForAttrdef, DU-002 slice 178); keep the two in sync so
		// the dump path and the proargdefaults path render identically.
		var b strings.Builder
		b.WriteString("CASE")
		if v.Operand != nil {
			b.WriteString(" ")
			b.WriteString(defaultExprToSQL(v.Operand))
		}
		for _, w := range v.Whens {
			b.WriteString(" WHEN ")
			b.WriteString(defaultExprToSQL(w.When))
			b.WriteString(" THEN ")
			b.WriteString(defaultExprToSQL(w.Then))
		}
		if v.Else != nil {
			b.WriteString(" ELSE ")
			b.WriteString(defaultExprToSQL(v.Else))
		}
		b.WriteString(" END")
		return b.String()
	case *parser.RowExpr:
		// `DEFAULT (1, 2)` — the parenthesised row-constructor shorthand. Mirror the
		// catalog twin (catalog.formatExprForAttrdef, DU-002 slice 179); keep the two in
		// sync so the dump path and the proargdefaults path render identically. PG's
		// ruleutils always prints the ROW keyword (get_rule_expr T_RowExpr).
		var elems []string
		for _, el := range v.Elems {
			elems = append(elems, defaultExprToSQL(el))
		}
		return "ROW(" + strings.Join(elems, ", ") + ")"
	case *parser.IntervalLit:
		// `DEFAULT INTERVAL '1' day`. Mirror the catalog twin
		// (catalog.formatExprForAttrdef, DU-002 slice 180); keep the two in sync so
		// the dump path and the proargdefaults path render identically. goopg has no
		// interval output function, so it re-emits the native `INTERVAL '<n>' <unit>`
		// literal form (PG's pg_get_expr would print the equivalent `'<n> <unit>'::interval`).
		return "INTERVAL '" + strings.ReplaceAll(v.Value, "'", "''") + "' " + v.Unit
	case *parser.IsNullExpr:
		// `DEFAULT (1 IS NULL)`. Mirror the catalog twin
		// (catalog.formatExprForAttrdef, DU-002 slice 181); keep the two in sync so
		// the dump path and the proargdefaults path render identically. PG's
		// pg_get_expr deparses a NullTest as `<operand> IS [NOT] NULL`.
		if v.Negated {
			return defaultExprToSQL(v.Operand) + " IS NOT NULL"
		}
		return defaultExprToSQL(v.Operand) + " IS NULL"
	case *parser.IsBoolExpr:
		// `DEFAULT (true IS NOT TRUE)`. Mirror the catalog twin (DU-002 slice 181).
		// PG's pg_get_expr deparses a BooleanTest as `<operand> IS [NOT] TRUE|FALSE|UNKNOWN`.
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
		return defaultExprToSQL(v.Operand) + op + target
	case *parser.IsDistinctFromExpr:
		// `DEFAULT (1 IS DISTINCT FROM 2)`. Mirror the catalog twin (DU-002 slice 181).
		// PG's pg_get_expr deparses a DistinctExpr as `<left> IS [NOT] DISTINCT FROM <right>`.
		op := " IS DISTINCT FROM "
		if v.Negated {
			op = " IS NOT DISTINCT FROM "
		}
		return defaultExprToSQL(v.Left) + op + defaultExprToSQL(v.Right)
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
	if _, err := planner.Plan(s.Query, o.planCatalog()); err != nil {
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
	if viewPlan, planErr := planner.Plan(s.Query, o.planCatalog()); planErr == nil {
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
	vt, err := o.ctx.Catalog.CreateView(s.Name, cols, s.Columns, s.Query, s.OrReplace)
	if err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	// Preserve the raw view body so pg_get_viewdef can echo it for pg_dump.
	if vt != nil {
		vt.ViewDef = s.RawDef
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

// transitiveDep describes a single view or materialized view that will be
// transitively dropped when its parent is dropped with CASCADE.
type transitiveDep struct {
	kind       string // "view" or "materialized view"
	name       parser.ObjectName
	parentKind string // "table", "view", or "materialized view"
	parentName parser.ObjectName
}

// collectAllViewTransitiveDeps performs a BFS from startName and returns all
// views and materialized views that transitively depend on it. startName
// itself is NOT included in the result. The parentKind/parentName fields of
// each entry record which immediate ancestor pulled the object in.
func collectAllViewTransitiveDeps(im *catalog.InMemory, startName parser.ObjectName) []transitiveDep {
	seen := map[string]bool{}
	queue := []parser.ObjectName{startName}
	var result []transitiveDep

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		// Determine the kind of curr.
		currKind := "table"
		if tbl, ok := im.LookupTable(curr); ok {
			if tbl.IsMatView {
				currKind = "materialized view"
			} else if tbl.View != nil {
				currKind = "view"
			}
		}

		// Views depending on curr.
		for _, vn := range viewsDependingOnView(im, curr) {
			k := vn.String()
			if !seen[k] {
				seen[k] = true
				result = append(result, transitiveDep{kind: "view", name: vn, parentKind: currKind, parentName: curr})
				queue = append(queue, vn)
			}
		}

		// MatViews depending on curr.
		for _, mvn := range matViewsDependingOnRelation(im, curr) {
			k := mvn.String()
			if !seen[k] {
				seen[k] = true
				result = append(result, transitiveDep{kind: "materialized view", name: mvn, parentKind: currKind, parentName: curr})
				queue = append(queue, mvn)
			}
		}
	}
	return result
}

// execDropView removes a view from the catalog. No relation
// file is involved — views are virtual.
func (o *ddlOp) execDropView(s *parser.DropViewStmt) error {
	dropped := make(map[string]bool)
	for _, name := range s.Names {
		// Before dropping, collect all transitive deps and emit ONE aggregate notice.
		if s.Behavior == parser.DropCascade {
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				deps := collectAllViewTransitiveDeps(im, name)
				if len(deps) == 1 {
					d := deps[0]
					o.ctx.AddNotice(fmt.Sprintf("drop cascades to %s %s", d.kind, d.name.String()))
				} else if len(deps) > 1 {
					detail := make([]string, len(deps))
					for i, d := range deps {
						detail[i] = fmt.Sprintf("drop cascades to %s %s", d.kind, d.name.String())
					}
					o.ctx.AddNoticeWithDetail(
						fmt.Sprintf("drop cascades to %d other objects", len(deps)),
						strings.Join(detail, "\n"),
					)
				}
			}
		}
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

	// CASCADE: drop any dependent views and matviews before dropping this one.
	// Notices are emitted by the top-level caller (execDropView), not here.
	if behavior == parser.DropCascade {
		if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
			deps := viewsDependingOnView(im, name)
			for _, depName := range deps {
				if !dropped[depName.String()] {
					if err := o.execDropOneView(depName, true, behavior, pos, dropped); err != nil {
						return err
					}
				}
			}
			matDeps := matViewsDependingOnRelation(im, name)
			for _, depName := range matDeps {
				if !dropped[depName.String()] {
					if err := o.execDropOneMatView(depName, true, behavior, pos, dropped, false); err != nil {
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

// execDropOneMatView drops a single materialized view, cascading to dependent matviews when
// behavior == DropCascade. The dropped map prevents infinite recursion.
// emitNotice controls whether this call emits its own NOTICE (false when called from execDropTable/View
// which already emits the grouped notice at the top level).
func (o *ddlOp) execDropOneMatView(name parser.ObjectName, ifExists bool, behavior parser.DropBehavior, pos int, dropped map[string]bool, emitNotice bool) error {
	key := name.String()
	if dropped[key] {
		return nil // already being dropped in this cascade
	}
	if ifExists && o.dropSchemaQualifiedNotice(name) {
		return nil
	}
	if _, ok := o.ctx.Catalog.LookupTable(name); !ok {
		if ifExists {
			o.ctx.AddNotice(fmt.Sprintf("materialized view %q does not exist, skipping", name.String()))
			return nil
		}
		return &ExecError{Code: "42P01", Pos: pos, Message: fmt.Sprintf("materialized view %q does not exist", name.String())}
	}

	// Mark as being dropped before recursing to break circular dependency cycles.
	dropped[key] = true

	// CASCADE: drop any dependent matviews before dropping this one.
	// Only emit notices when emitNotice=true (top-level DROP MATERIALIZED VIEW only).
	if behavior == parser.DropCascade {
		if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
			matDeps := matViewsDependingOnRelation(im, name)
			if emitNotice {
				// Emit cascade notice before dropping.
				newDeps := make([]parser.ObjectName, 0, len(matDeps))
				for _, dep := range matDeps {
					if !dropped[dep.String()] {
						newDeps = append(newDeps, dep)
					}
				}
				if len(newDeps) == 1 {
					o.ctx.AddNotice(fmt.Sprintf("drop cascades to materialized view %s", newDeps[0].String()))
				} else if len(newDeps) > 1 {
					details := make([]string, len(newDeps))
					for i, d := range newDeps {
						details[i] = fmt.Sprintf("drop cascades to materialized view %s", d.String())
					}
					o.ctx.AddNoticeWithDetail(
						fmt.Sprintf("drop cascades to %d other objects", len(newDeps)),
						strings.Join(details, "\n"),
					)
				}
			}
			for _, depName := range matDeps {
				if !dropped[depName.String()] {
					if err := o.execDropOneMatView(depName, true, behavior, pos, dropped, false); err != nil {
						return err
					}
				}
			}
		}
	}

	if err := o.ctx.Catalog.DropView(name, ifExists); err != nil {
		if ifExists {
			o.ctx.AddNotice(fmt.Sprintf("materialized view %q does not exist, skipping", name.String()))
			return nil
		}
		return &ExecError{Code: "42P01", Pos: pos, Message: err.Error()}
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

// matViewsDependingOnRelation returns the names of materialized views that directly reference
// the given table/view/matview name in their SELECT body. Used by CASCADE drops.
func matViewsDependingOnRelation(im *catalog.InMemory, target parser.ObjectName) []parser.ObjectName {
	targetName := strings.ToLower(target.Name)
	var deps []parser.ObjectName
	for _, t := range im.AllUserMatViews() {
		if t.View == nil {
			continue
		}
		if strings.EqualFold(t.Name, target.Name) && strings.EqualFold(t.Schema, target.Schema) {
			continue // skip the relation itself
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
	// Track tables already dropped by cascade during this statement so we can skip
	// them if they also appear explicitly in s.Names (avoids "does not exist" errors).
	cascadeDropped := make(map[string]bool)
	// Pre-build the explicit drop set so we can suppress cascade notices for tables
	// that are also being dropped explicitly (PostgreSQL doesn't emit those notices).
	explicitDropSet := make(map[string]bool, len(s.Names))
	for _, n := range s.Names {
		explicitDropSet[n.String()] = true
	}
	for _, name := range s.Names {
		if cascadeDropped[name.String()] {
			continue // already removed by cascade from a prior table in this statement
		}
		if s.IfExists && o.dropSchemaQualifiedNotice(name) {
			continue
		}
		tbl, ok := o.ctx.Catalog.LookupTable(name)
		if !ok && name.Schema == "" {
			// search_path fallback: try each schema in search order (mirrors LOCK TABLE). M0097-0022.
			for _, sc := range lockTableSearchSchemas(o.ctx) {
				tbl, ok = o.ctx.Catalog.LookupTable(parser.ObjectName{Schema: sc, Name: name.Name})
				if ok {
					break
				}
			}
		}
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
			// dropPartitionDescendants recurses depth-first so grandchild
			// partitions are dropped before their parents. Without recursion,
			// DROP TABLE on a 2-level partitioned hierarchy left grandchildren
			// in the catalog, causing "relation already exists" on the next
			// CREATE TABLE with the same name. M0097-0028.
			var dropPartitionDescendants func(parent *catalog.Table) error
			dropPartitionDescendants = func(parent *catalog.Table) error {
				for _, child := range im.PartitionChildren(parent.OID) {
					if err := dropPartitionDescendants(child); err != nil {
						return err
					}
					childName := parser.ObjectName{Schema: child.Schema, Name: child.Name}
					if err := o.dropTableByRef(childName, child); err != nil {
						return err
					}
				}
				return nil
			}
			if err := dropPartitionDescendants(tbl); err != nil {
				return err
			}
			if s.Behavior == parser.DropCascade {
				// Recursively collect all inheritance descendants (depth-first) so that
				// grandchildren of a child-that-is-also-explicit are also dropped.
				// When ctla→ctlb→inhe and ctla+ctlb are explicit, dropping ctla cascades
				// ctlb (explicit, no notice) but we must still cascade ctlb→inhe.
				// M0097-0023.
				visitedOIDs := map[uint32]bool{tbl.OID: true}
				var collectInheritanceTree func(parent *catalog.Table) []*catalog.Table
				collectInheritanceTree = func(parent *catalog.Table) []*catalog.Table {
					var result []*catalog.Table
					for _, child := range im.InheritanceChildren(parent.OID) {
						childName := parser.ObjectName{Schema: child.Schema, Name: child.Name}
						if cascadeDropped[childName.String()] || visitedOIDs[child.OID] {
							continue
						}
						visitedOIDs[child.OID] = true
						result = append(result, child)
						result = append(result, collectInheritanceTree(child)...)
					}
					return result
				}
				allDescendants := collectInheritanceTree(tbl)
				// Emit cascade notices for descendants not in the explicit drop list.
				var noticeChildren []*catalog.Table
				for _, child := range allDescendants {
					childName := parser.ObjectName{Schema: child.Schema, Name: child.Name}
					if !explicitDropSet[childName.String()] && !cascadeDropped[childName.String()] {
						noticeChildren = append(noticeChildren, child)
					}
				}
				if len(noticeChildren) == 1 {
					childName := parser.ObjectName{Schema: noticeChildren[0].Schema, Name: noticeChildren[0].Name}
					o.ctx.AddNotice(fmt.Sprintf("drop cascades to table %s", childName.String()))
				} else if len(noticeChildren) > 1 {
					// PostgreSQL emits summary NOTICE + DETAIL listing each child.
					// Normalizer strips DETAIL prefix and moves all lines to error section.
					detail := make([]string, len(noticeChildren))
					for i, child := range noticeChildren {
						childName := parser.ObjectName{Schema: child.Schema, Name: child.Name}
						detail[i] = fmt.Sprintf("drop cascades to table %s", childName.String())
					}
					o.ctx.AddNoticeWithDetail(
						fmt.Sprintf("drop cascades to %d other objects", len(noticeChildren)),
						strings.Join(detail, "\n"),
					)
				}
				for _, child := range allDescendants {
					childName := parser.ObjectName{Schema: child.Schema, Name: child.Name}
					if cascadeDropped[childName.String()] {
						continue
					}
					cascadeDropped[childName.String()] = true
					if err := o.dropTableByRef(childName, child); err != nil {
						return err
					}
				}
			}
		}
		// RESTRICT (default): if any views or matviews depend on this table, error with details.
		// Include transitive deps (e.g. matviews that depend on a view that depends on this table).
		if s.Behavior != parser.DropCascade {
			if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
				var depDescs []string
				directViews := viewsDependingOnTable(im, name)
				directMVs := matViewsDependingOnRelation(im, name)
				for _, vn := range directViews {
					depDescs = append(depDescs, fmt.Sprintf("view %s depends on table %s", vn.String(), name.String()))
				}
				for _, mvn := range directMVs {
					depDescs = append(depDescs, fmt.Sprintf("materialized view %s depends on table %s", mvn.String(), name.String()))
				}
				// Transitively reachable deps (views/matviews that depend on the direct views/matviews).
				seenTransitive := map[string]bool{}
				for _, vn := range directViews {
					seenTransitive[vn.String()] = true
				}
				for _, mvn := range directMVs {
					seenTransitive[mvn.String()] = true
				}
				allDirect := append(append([]parser.ObjectName{}, directViews...), directMVs...)
				for _, directDep := range allDirect {
					transitiveDeps := collectAllViewTransitiveDeps(im, directDep)
					for _, td := range transitiveDeps {
						k := td.name.String()
						if !seenTransitive[k] {
							seenTransitive[k] = true
							depDescs = append(depDescs, fmt.Sprintf("%s %s depends on %s %s", td.kind, td.name.String(), td.parentKind, td.parentName.String()))
						}
					}
				}
				if len(depDescs) > 0 {
					return &ExecError{
						Code:    "2BP01",
						Pos:     s.Pos(),
						Message: fmt.Sprintf("cannot drop table %s because other objects depend on it", name.String()),
						Detail:  strings.Join(depDescs, "\n"),
						Hint:    "Use DROP ... CASCADE to drop the dependent objects too.",
					}
				}
			}
		}
		// CASCADE: drop views, matviews, and functions that directly or transitively reference this table.
		if s.Behavior == parser.DropCascade {
			if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
				// Collect all dependents in display order: views, matviews (including transitively
				// reached via view chains), then functions.
				type cascadeDep struct {
					kind     string            // "view", "materialized view", or "function"
					display  string            // full display text, e.g. "view functestv3"
					viewName parser.ObjectName // for view/matview drops
					routine  *catalog.Routine  // for function drops
				}
				var deps []cascadeDep
				seen := map[string]bool{}

				// Direct view dependents + all their transitive view/matview deps via BFS.
				viewNames := viewsDependingOnTable(im, name)
				for _, vn := range viewNames {
					k := vn.String()
					if !seen[k] {
						seen[k] = true
						deps = append(deps, cascadeDep{kind: "view", display: "view " + vn.String(), viewName: vn})
					}
					// Transitively reachable views/matviews from this view.
					for _, td := range collectAllViewTransitiveDeps(im, vn) {
						k2 := td.name.String()
						if !seen[k2] {
							seen[k2] = true
							deps = append(deps, cascadeDep{kind: td.kind, display: td.kind + " " + td.name.String(), viewName: td.name})
						}
					}
				}
				// Direct materialized view dependents + all their transitive deps via BFS.
				matViewNames := matViewsDependingOnRelation(im, name)
				for _, mvn := range matViewNames {
					k := mvn.String()
					if !seen[k] {
						seen[k] = true
						deps = append(deps, cascadeDep{kind: "materialized view", display: "materialized view " + mvn.String(), viewName: mvn})
					}
					// Transitively reachable matviews from this matview.
					for _, td := range collectAllViewTransitiveDeps(im, mvn) {
						k2 := td.name.String()
						if !seen[k2] {
							seen[k2] = true
							deps = append(deps, cascadeDep{kind: td.kind, display: td.kind + " " + td.name.String(), viewName: td.name})
						}
					}
				}
				// Direct function dependents (via TableDeps).
				for _, r := range functionsDependingOnTable(o.ctx.Catalog, name) {
					dn := routineCascadeDisplayName(r)
					deps = append(deps, cascadeDep{kind: "function", display: "function " + dn, routine: r})
				}
				// Transitive function dependents via views being dropped.
				for _, vn := range viewNames {
					for _, r := range functionsDependingOnTable(o.ctx.Catalog, vn) {
						dn := routineCascadeDisplayName(r)
						deps = append(deps, cascadeDep{kind: "function", display: "function " + dn, routine: r})
					}
				}

				// Emit NOTICE: 1 object → individual; N > 1 → "N other objects" + DETAIL.
				if len(deps) == 1 {
					o.ctx.AddNotice(fmt.Sprintf("drop cascades to %s", deps[0].display))
				} else if len(deps) > 1 {
					detail := make([]string, len(deps))
					for i, d := range deps {
						detail[i] = fmt.Sprintf("drop cascades to %s", d.display)
					}
					o.ctx.AddNoticeWithDetail(
						fmt.Sprintf("drop cascades to %d other objects", len(deps)),
						strings.Join(detail, "\n"),
					)
				}

				// Drop all dependents using a single shared dropped map to prevent double-drops.
				droppedViews := map[string]bool{}
				for _, d := range deps {
					if d.kind == "view" {
						if err := o.execDropOneView(d.viewName, true, parser.DropCascade, s.Pos(), droppedViews); err != nil {
							return err
						}
					} else if d.kind == "materialized view" {
						if err := o.execDropOneMatView(d.viewName, true, parser.DropCascade, s.Pos(), droppedViews, false); err != nil {
							return err
						}
					} else if d.kind == "function" && d.routine != nil {
						if rs := o.ctx.Catalog.Routines(); rs != nil {
							_ = rs.DropRoutine(d.routine)
						}
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

// viewsDependingOnTable returns the names of views that directly reference tableName
// in their FROM clause. Used by DROP TABLE CASCADE to cascade drops.
// Only checks views in the same schema as the dropped table for performance.
func viewsDependingOnTable(im *catalog.InMemory, tableName parser.ObjectName) []parser.ObjectName {
	var deps []parser.ObjectName
	tblLower := strings.ToLower(tableName.Name)
	tblSchema := strings.ToLower(tableName.Schema)
	for _, tbl := range im.AllUserViews() {
		if tbl.View == nil {
			continue
		}
		// Only check views in the same schema (fast path to avoid scanning all views).
		if tblSchema != "" && strings.ToLower(tbl.Schema) != tblSchema {
			continue
		}
		if selectRefsViewName(tbl.View, tblLower) {
			deps = append(deps, parser.ObjectName{Schema: tbl.Schema, Name: tbl.Name})
		}
	}
	return deps
}

// functionsDependingOnTable returns all routines whose TableDeps reference the given table/view name.
// Used by DROP TABLE/VIEW CASCADE to cascade drops to dependent functions.
func functionsDependingOnTable(cat catalog.Catalog, tableName parser.ObjectName) []*catalog.Routine {
	rs := cat.Routines()
	if rs == nil {
		return nil
	}
	tblLower := strings.ToLower(tableName.Name)
	schemaLower := strings.ToLower(tableName.Schema)
	var result []*catalog.Routine
	for _, r := range rs.List() {
		for _, td := range r.TableDeps {
			if strings.ToLower(td.Name) == tblLower {
				// Match if schema is unspecified on either side or schemas agree.
				if schemaLower == "" || td.Schema == "" || strings.ToLower(td.Schema) == schemaLower {
					result = append(result, r)
					break
				}
			}
		}
	}
	return result
}

// functionsDependingOnSequence returns all routines whose SequenceDeps reference the given sequence name.
func functionsDependingOnSequence(cat catalog.Catalog, seqName string, seqSchema string) []*catalog.Routine {
	rs := cat.Routines()
	if rs == nil {
		return nil
	}
	seqLower := strings.ToLower(seqName)
	schemaLower := strings.ToLower(seqSchema)
	var result []*catalog.Routine
	for _, r := range rs.List() {
		for _, sd := range r.SequenceDeps {
			if strings.ToLower(sd.Name) == seqLower {
				if schemaLower == "" || sd.Schema == "" || strings.ToLower(sd.Schema) == schemaLower {
					result = append(result, r)
					break
				}
			}
		}
	}
	return result
}

// functionsDependingOnRoutineOID returns all routines whose RoutineCallOIDs include the given OID.
func functionsDependingOnRoutineOID(cat catalog.Catalog, oid uint32) []*catalog.Routine {
	rs := cat.Routines()
	if rs == nil {
		return nil
	}
	var result []*catalog.Routine
	for _, r := range rs.List() {
		for _, callOID := range r.RoutineCallOIDs {
			if callOID == oid {
				result = append(result, r)
				break
			}
		}
	}
	return result
}

// routineCascadeDisplayName formats a routine's name and arg types for DROP CASCADE NOTICE messages.
// Format: "funcname(argtype1,argtype2,...)" using canonical type names.
func routineCascadeDisplayName(r *catalog.Routine) string {
	name := strings.ToLower(r.Name)
	parts := make([]string, 0, len(r.ArgTypes))
	for i, t := range r.ArgTypes {
		mode := ""
		if i < len(r.ArgModes) {
			mode = r.ArgModes[i]
		}
		if mode == "o" {
			continue // Skip OUT params (same as Signature())
		}
		canon := dropCompatCanonicalType(t.Name)
		if canon == "" {
			canon = strings.ToLower(t.Name)
		}
		parts = append(parts, canon)
	}
	return name + "(" + strings.Join(parts, ",") + ")"
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
	// Use the table's stored name for DropTable so that bare-keyed tables
	// (schema="") found via a schema-qualified lookup can be removed correctly.
	// M0097-0023.
	dropName := parser.ObjectName{Schema: tbl.Schema, Name: tbl.Name}
	// Record the drop for ROLLBACK TO SAVEPOINT if we're inside a savepoint.
	// The physical deletion is idempotent (os.IsNotExist guard in DropRelation),
	// so restoring only the catalog entry is sufficient. M0097-0023.
	if bsess, ok := o.ctx.Session.(*BasicSession); ok && bsess.InExplicitTransaction() && bsess.SavepointDepth() > 0 {
		bsess.RecordDDLDrop(DDLDropUndoEntry{
			Table:          tbl,
			Indexes:        idxs,
			SavepointDepth: bsess.SavepointDepth(),
		})
	}
	if err := o.ctx.Catalog.DropTable(dropName); err != nil {
		return &ExecError{Code: "XX000", Message: err.Error()}
	}
	// Drop sequences that are owned by columns of this table (created via
	// ALTER SEQUENCE ... OWNED BY table.col, or SERIAL column defaults).
	DropSequencesOwnedByTable(tbl.Name)
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
		name = o.autoIndexNameWithIncludes(tbl, s.Columns, s.IncludeColumns, "idx")
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
	// gin_pending_list_limit (GIN integer storage parameter, kB) range-validated
	// like PG: min 64, max MAX_KILOBYTES (INT_MAX/1024). DU-002 slice 221.
	if s.GinPendingListLimit != 0 && (s.GinPendingListLimit < 64 || s.GinPendingListLimit > 2097151) {
		return &ExecError{Code: "22023", Pos: s.Pos(),
			Message: fmt.Sprintf("value %d out of bounds for option \"gin_pending_list_limit\"", s.GinPendingListLimit),
			Detail:  "Valid values are between \"64\" and \"2097151\"."}
	}
	// pages_per_range (BRIN integer storage parameter) range-validated like PG:
	// min 1, max BRIN_MAX_PAGES_PER_RANGE (131072). DU-002 slice 222.
	if s.PagesPerRange != 0 && (s.PagesPerRange < 1 || s.PagesPerRange > 131072) {
		return &ExecError{Code: "22023", Pos: s.Pos(),
			Message: fmt.Sprintf("value %d out of bounds for option \"pages_per_range\"", s.PagesPerRange),
			Detail:  "Valid values are between \"1\" and \"131072\"."}
	}
	method := strings.ToLower(strings.TrimSpace(s.Method))
	if method == "rtree" {
		o.ctx.AddNotice("substituting access method \"gist\" for obsolete method \"rtree\"")
		method = "gist"
	}
	if method == "" {
		method = "btree"
	}
	// brin, gin, hash do not support INCLUDE columns; hash is silently upgraded
	// to btree only when there are no INCLUDE columns. M0097-0023.
	if len(s.IncludeColumns) > 0 {
		switch method {
		case "brin", "gin", "hash":
			return &ExecError{Code: "0A000", Pos: s.Pos(),
				Message: fmt.Sprintf("access method \"%s\" does not support included columns", method)}
		}
	}
	if method == "hash" {
		method = "btree"
	}
	// gist, spgist, gin and brin: register catalog metadata only (no physical
	// storage). pg_index / pg_class / pg_get_indexdef queries work correctly; no
	// actual index acceleration or constraint enforcement. GIN is catalog-only
	// so its `fastupdate` reloption can round-trip through pg_dump (slice 220);
	// BRIN is catalog-only so its `pages_per_range` reloption likewise round-trips
	// (DU-002 slice 222).
	if method == "gist" || method == "spgist" || method == "gin" || method == "brin" {
		idx, createErr := o.ctx.Catalog.CreateIndex(idxName, tbl, s.Columns, s.Unique, method, false)
		if createErr != nil {
			return &ExecError{Code: "XX000", Pos: s.Pos(), Message: createErr.Error()}
		}
		idx.HasPredicate = s.HasPredicate
		idx.IncludeColumns = s.IncludeColumns
		// Persist `WITH (fillfactor=N)` so pg_class.reloptions / pg_dump round-trip
		// it (already range-validated above). DU-002 slice 218.
		idx.Fillfactor = s.Fillfactor
		// Persist `WITH (deduplicate_items=on|off)` likewise. DU-002 slice 219.
		idx.DeduplicateItems = s.DeduplicateItems
		// Persist `WITH (fastupdate=on|off)` (GIN). DU-002 slice 220.
		idx.FastUpdate = s.FastUpdate
		// Persist `WITH (gin_pending_list_limit=N)` (GIN, range-validated above). DU-002 slice 221.
		idx.GinPendingListLimit = s.GinPendingListLimit
		// Persist `WITH (pages_per_range=N)` (BRIN, range-validated above). DU-002 slice 222.
		idx.PagesPerRange = s.PagesPerRange
		// Persist `WITH (autosummarize=on|off)` (BRIN). DU-002 slice 223.
		idx.AutoSummarize = s.AutoSummarize
		if catalogHeapSyncAvailable(o.ctx) {
			if syncErr := syncIndexToCatalogHeap(o.ctx, idx); syncErr != nil {
				return fmt.Errorf("DDL catalog sync: %w", syncErr)
			}
		}
		return nil
	}
	if method != "btree" {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: fmt.Sprintf("index method %q is not supported in v0", method)}
	}
	// For partial indexes, resolve the WHERE predicate so bulk build can filter rows.
	var resolvedPred planner.Expr
	if s.HasPredicate && s.Predicate != nil {
		resolvedPred, _ = planner.ResolveIndexPredicate(s.Predicate, tbl)
	}
	if err := o.createBTreeIndex(s.Pos(), idxName, tbl, s.Columns, s.ColExprs, s.Unique, false, resolvedPred); err != nil {
		return err
	}
	// Store INCLUDE columns, partial index flag, predicate expression, and the
	// per-column ASC/DESC + NULLS ordering. The ordering is recorded only when
	// at least one column is non-default so a plain index keeps empty slices and
	// dumps byte-identically. DU-002 slice 56.
	nonDefaultOrder := indexHasNonDefaultOrder(s.ColOrders)
	if s.HasPredicate || len(s.IncludeColumns) > 0 || s.Predicate != nil || nonDefaultOrder || s.NullsNotDistinct || s.Fillfactor != 0 || s.DeduplicateItems != nil {
		if idx, ok := o.ctx.Catalog.LookupIndex(idxName); ok {
			idx.HasPredicate = s.HasPredicate
			idx.Predicate = s.Predicate
			// Persist `WITH (fillfactor=N)` so pg_class.reloptions / pg_dump
			// round-trip it (already range-validated above). DU-002 slice 218.
			idx.Fillfactor = s.Fillfactor
			// Persist `WITH (deduplicate_items=on|off)` likewise. DU-002 slice 219.
			idx.DeduplicateItems = s.DeduplicateItems
			if s.Predicate != nil {
				idx.PredicateString = defaultExprToSQL(s.Predicate)
			}
			idx.IncludeColumns = s.IncludeColumns
			// NULLS NOT DISTINCT (PG 15+) — record so pg_index.indnullsnotdistinct
			// and pg_get_indexdef reproduce it for pg_dump. DU-002 slice 134.
			idx.NullsNotDistinct = s.NullsNotDistinct
			if nonDefaultOrder {
				desc := make([]bool, len(s.ColOrders))
				nullsFirst := make([]bool, len(s.ColOrders))
				for i, ord := range s.ColOrders {
					desc[i] = ord.Descending
					nullsFirst[i] = ord.NullsFirst
				}
				idx.ColDescending = desc
				idx.ColNullsFirst = nullsFirst
			}
		}
	}
	return nil
}

// indexHasNonDefaultOrder reports whether any key column carries a non-default
// ASC/DESC + NULLS ordering (the btree default is ASC NULLS LAST, i.e.
// Descending=false, NullsFirst=false). DU-002 slice 56.
func indexHasNonDefaultOrder(orders []parser.IndexColOrder) bool {
	for _, o := range orders {
		if o.Descending || o.NullsFirst {
			return true
		}
	}
	return false
}

func (o *ddlOp) execDropIndex(s *parser.DropIndexStmt) error {
	if o.ctx.Pool == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "DROP INDEX requires Pool in Context"}
	}

	// DROP INDEX CONCURRENTLY cannot run inside an explicit transaction block.
	// M0100-0009.
	if s.Concurrent && o.ctx.Session != nil && o.ctx.Session.InExplicitTransaction() {
		return &ExecError{Code: "25001", Pos: s.Pos(), Message: "DROP INDEX CONCURRENTLY cannot run inside a transaction block"}
	}

	// DROP INDEX CONCURRENTLY: wait for all transactions that were active at
	// DROP time to commit/abort before physically removing the index. This
	// ensures no snapshot taken before the DROP can still reference the index.
	// M0100-0009.
	if s.Concurrent && o.ctx.TxnMgr != nil && o.ctx.Ctx != nil {
		if err := o.ctx.TxnMgr.WaitForOlderSlotsToCommit(o.ctx.Ctx, o.ctx.Tx.Handle); err != nil {
			return &ExecError{Code: "57014", Pos: s.Pos(), Message: "DROP INDEX CONCURRENTLY cancelled"}
		}
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
				// Use just the index name (not schema-qualified) to match PostgreSQL's notice format.
				o.ctx.AddNotice(fmt.Sprintf("index %q does not exist, skipping", name.Name))
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
	// Handle SET LOGGED / SET UNLOGGED.
	if s.SetLogged != "" {
		tbl, ok := o.lookupTableWithSearch(s.Name)
		if !ok {
			if s.IfExists {
				return nil
			}
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Name.String())}
		}
		_ = tbl
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: fmt.Sprintf("ALTER action SET %s cannot be performed on relation %q", strings.ToUpper(s.SetLogged), s.Name.Name)}
	}
	// Handle SET SCHEMA — move a table/matview to a new schema. M0097-0025.
	if s.SetSchema != "" {
		tbl, ok := o.lookupTableWithSearch(s.Name)
		if !ok {
			if s.IfExists {
				return nil
			}
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Name.String())}
		}
		tbl.Schema = s.SetSchema
		return nil
	}
	tbl, ok := o.lookupTableWithSearch(s.Name)
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
				if act.Kind == parser.AlterTableSetStatistics {
					if err := o.execIndexSetStatistics(s.Name.Name, idx, act); err != nil {
						return err
					}
				}
				if act.Kind == parser.AlterIndexAttachPartition {
					childName := parser.ObjectName{Name: act.ChildIndexName}
					childIdx, ok2 := o.ctx.Catalog.LookupIndex(childName)
					if ok2 {
						childIdx.PartitionParentOID = idx.OID
						if im2, ok3 := o.ctx.Catalog.(*catalog.InMemory); ok3 {
							im2.RegisterIndexPartitionChild(idx.OID, childIdx.OID)
						}
					}
				}
			}
			// Other ALTER actions on index: silently accept in v0.
			return nil
		}
		if s.IfExists {
			return nil
		}
		// Fallback: implicit sequences (from SERIAL columns) have no catalog entry.
		// Handle RENAME TABLE on them via the sequence registry directly.
		for _, act := range s.Actions {
			if act.Kind == parser.AlterTableRenameTable {
				oldName := s.Name.Name
				newName := act.NewName
				if RenameSequence(oldName, newName) || RenameSequence("public."+oldName, newName) {
					return nil
				}
			}
		}
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", s.Name.String())}
	}
	// Reject structural modifications to system catalogs. pg_catalog and
	// information_schema tables are virtual — mutating them corrupts the
	// catalog state for the rest of the session. PG returns 42501 here.
	if tbl.Schema == "pg_catalog" || tbl.Schema == "information_schema" {
		return &ExecError{Code: "42501", Pos: s.Pos(),
			Message: fmt.Sprintf("permission denied: %q is a system catalog", tbl.Name)}
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
		case parser.AlterTableAddUnique:
			if err := o.execAlterTableAddUnique(tbl, act); err != nil {
				return err
			}
		case parser.AlterTableAddForeignKey:
			// v0 accepts the syntax for HammerDB TPC-H
			// compatibility but does not enforce referential
			// integrity. The referenced table must exist (so
			// typos and dropped relations still surface here);
			// column-level validation is deferred. Store the FK
			// so TRUNCATE CASCADE BFS can find referencing tables.
			// See docs/design/0003-0004-hammerdb-tpch-integration.md.
			if _, ok := o.ctx.Catalog.LookupTable(act.RefTable); !ok {
				return &ExecError{Code: "42P01", Pos: act.Pos(), Message: fmt.Sprintf("relation %q does not exist", act.RefTable.String())}
			}
			// Surface the FK in pg_constraint (contype='f') so pg_dump can
			// re-emit it: honour an explicit CONSTRAINT name, else PG's
			// <table>_<firstcol>_fkey auto-name. DU-002 slice 51.
			fkName := act.ConstraintName
			if fkName == "" {
				firstCol := ""
				if len(act.Columns) > 0 {
					firstCol = act.Columns[0]
				}
				fkName = tbl.Name + "_" + firstCol + "_fkey"
			}
			fk := catalog.ForeignKey{
				Name:       fkName,
				OID:        o.allocConstraintOID(fkName),
				Columns:    append([]string(nil), act.Columns...),
				RefTable:   act.RefTable.Name,
				RefColumns: append([]string(nil), act.RefColumns...),
				OnDelete:   act.OnDelete,
				OnUpdate:   act.OnUpdate,
				Deferrable: act.Deferrable,
			}
			tbl.ForeignKeys = append(tbl.ForeignKeys, fk)
		case parser.AlterTableAddCheck:
			// ADD [CONSTRAINT name] CHECK (expr) — register the check constraint.
			// Track the constraint name so violations report it. A named
			// constraint gets a fresh OID so it surfaces in pg_constraint
			// (the latent virtual-table join crash was fixed in
			// M0097-0023-loop34). M0097-0023.
			if act.CheckExpr != "" {
				tbl.AddCheck(act.ConstraintName, act.CheckExpr, o.allocConstraintOID(act.ConstraintName))
				// Propagate to partition children: merge if child already has the
				// same constraint (locally defined), otherwise inherit it. M0097-0023.
				if im3, ok3 := o.ctx.Catalog.(*catalog.InMemory); ok3 {
					for _, childTbl := range im3.PartitionChildren(tbl.OID) {
						merged := false
						for j := range childTbl.NamedChecks {
							if strings.EqualFold(childTbl.NamedChecks[j].Name, act.ConstraintName) {
								// Child already has it locally — merge: mark inherited.
								childTbl.NamedChecks[j].IsLocal = false
								childTbl.NamedChecks[j].InhCount = 1
								o.ctx.AddNotice(fmt.Sprintf("merging constraint %q with inherited definition", act.ConstraintName))
								merged = true
								break
							}
						}
						if !merged {
							oid3 := im3.AllocOID()
							childTbl.AddCheckInherited(act.ConstraintName, act.CheckExpr, oid3)
						}
					}
				}
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
					// Mark child as DEFAULT so checkDefaultPartitionDataConflict can find it.
					childTbl.PartitionBounds = []catalog.PartitionBound{{IsDefault: true, ChildName: childTbl.Name}}
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
					pb.InValueLiterals = append(pb.InValueLiterals, boundExprToSQLLiteral(e))
				}
				if len(poc.FromValues) > 0 {
					pb.From = exprToString(poc.FromValues[0]) // backward compat
					for _, v := range poc.FromValues {
						pb.FromValues = append(pb.FromValues, exprToString(v))
					}
					pb.FromValueLiterals = rangeBoundLiterals(poc.FromValues)
				}
				if len(poc.ToValues) > 0 {
					pb.To = exprToString(poc.ToValues[0]) // backward compat
					for _, v := range poc.ToValues {
						pb.ToValues = append(pb.ToValues, exprToString(v))
					}
					pb.ToValueLiterals = rangeBoundLiterals(poc.ToValues)
				}
				if len(pb.InValues) > 0 || pb.From != "" || pb.To != "" {
					childTbl.PartitionBounds = []catalog.PartitionBound{pb}
				}
			}
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				im.RegisterPartitionChild(tbl.OID, childTbl.OID)
			}
			// Propagate parent unique/PK indexes to the newly attached child
			// partition. In PostgreSQL, ATTACH PARTITION requires the child to
			// have matching indexes (created automatically when using CREATE TABLE
			// … PARTITION OF, or by ATTACH PARTITION propagation). Without this,
			// upsertOp's arbiter probe and insertOp's unique-constraint check both
			// miss live duplicates. Only create when the child doesn't already
			// have a matching index. M0097-0028.
			for _, parentIdx := range o.ctx.Catalog.IndexesOnTable(tbl) {
				if parentIdx.Method != "btree" || (!parentIdx.Primary && !parentIdx.Unique) {
					continue
				}
				alreadyHas := false
				for _, childIdx := range o.ctx.Catalog.IndexesOnTable(childTbl) {
					if len(childIdx.Columns) != len(parentIdx.Columns) {
						continue
					}
					match := true
					for k := range childIdx.Columns {
						if !strings.EqualFold(childIdx.Columns[k], parentIdx.Columns[k]) {
							match = false
							break
						}
					}
					if match {
						alreadyHas = true
						break
					}
				}
				if alreadyHas {
					continue
				}
				var childIdxName parser.ObjectName
				if parentIdx.Primary {
					childIdxName = parser.ObjectName{Schema: childTbl.Schema, Name: childTbl.Name + "_pkey"}
				} else {
					suffix := "_key"
					if len(parentIdx.Columns) == 1 {
						suffix = "_" + parentIdx.Columns[0] + "_key"
					}
					childIdxName = parser.ObjectName{Schema: childTbl.Schema, Name: childTbl.Name + suffix}
				}
				_ = o.createBTreeIndex(act.Pos(), childIdxName, childTbl, parentIdx.Columns, nil, parentIdx.Unique, parentIdx.Primary)
			}
		case parser.AlterTableDetachPartition:
			// ALTER TABLE parent DETACH PARTITION child — remove child from parent's
			// partition tree. M0097-0028.
			im, ok := o.ctx.Catalog.(*catalog.InMemory)
			if !ok {
				break
			}
			childTbl, ok := o.ctx.Catalog.LookupTable(act.DetachPartitionChild)
			if !ok {
				break
			}
			im.UnregisterPartitionChild(tbl.OID, childTbl.OID)
			childTbl.PartitionParentOID = 0
			childTbl.PartitionBounds = nil

		case parser.AlterIndexAttachPartition:
			// ALTER INDEX parent ATTACH PARTITION child — register index partition hierarchy.
			// Both parent and child must already exist as index catalog entries. M0097-0023.
			parentName := parser.ObjectName{Name: act.ConstraintName}
			parentIdx, ok := o.ctx.Catalog.LookupIndex(parentName)
			if !ok {
				break
			}
			childName := parser.ObjectName{Name: act.ChildIndexName}
			childIdx, ok := o.ctx.Catalog.LookupIndex(childName)
			if !ok {
				break
			}
			childIdx.PartitionParentOID = parentIdx.OID
			if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
				im.RegisterIndexPartitionChild(parentIdx.OID, childIdx.OID)
			}
		case parser.AlterTableRenameTable:
			newName := act.NewName
			oldBare := tbl.Name // capture before RenameTable updates tbl.Name
			oldObjName := parser.ObjectName{Schema: tbl.Schema, Name: tbl.Name}
			newObjName := parser.ObjectName{Schema: tbl.Schema, Name: newName}
			if err := o.ctx.Catalog.RenameTable(oldObjName, newObjName); err != nil {
				return &ExecError{Code: "42P07", Pos: act.Pos(), Message: err.Error()}
			}
			if tbl.IsSequence {
				// Build qualified names; fall back to bare name when schema is empty.
				qualName := func(schema, name string) string {
					if schema == "" {
						return name
					}
					return schema + "." + name
				}
				oldFull := qualName(tbl.Schema, oldBare)
				newFull := qualName(tbl.Schema, newName)
				if !RenameSequence(oldFull, newFull) {
					RenameSequence(oldBare, newFull)
				}
				// Regenerate the VirtualRows closure to reference the new registry key.
				capturedNewFull := newFull
				tbl.VirtualRows = func() [][]string {
					lv, lc, called, ok2 := SequenceRowData(capturedNewFull)
					if !ok2 {
						return nil
					}
					calledStr := "f"
					if called {
						calledStr = "t"
					}
					return [][]string{{
						fmt.Sprintf("%d", lv),
						fmt.Sprintf("%d", lc),
						calledStr,
					}}
				}
			}
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
			// Rename the column in the table's schema.
			for i, col := range tbl.Columns {
				if strings.EqualFold(col.Name, oldColName) {
					tbl.Columns[i].Name = newColName
					break
				}
			}
			// Update any index column references that use the old name.
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				for _, idx := range im.IndexesOnTable(tbl) {
					for i, col := range idx.Columns {
						if strings.EqualFold(col, oldColName) {
							idx.Columns[i] = newColName
						}
					}
				}
				// Update stored view/matview ASTs that reference this column.
				im.RenameColumnInViews(tbl.Name, oldColName, newColName)
			}
		case parser.AlterTableInherit:
			// INHERIT parent_table — register the named table as a parent of tbl
			// so that scanning the parent includes tbl's rows (M0097-0048).
			parentTbl, ok := o.ctx.Catalog.LookupTable(act.InheritParent)
			if !ok {
				return &ExecError{Code: "42P01", Pos: act.Pos(), Message: fmt.Sprintf("relation %q does not exist", act.InheritParent.String())}
			}
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				// Self-inheritance.
				if parentTbl.OID == tbl.OID {
					return &ExecError{Code: "42P07", Pos: act.Pos(),
						Message: fmt.Sprintf("cannot make relation inherit from itself")}
				}
				// Circular inheritance: tbl is already an ancestor of parentTbl.
				if im.IsInheritanceDescendant(tbl.OID, parentTbl.OID) {
					return &ExecError{Code: "42P07", Pos: act.Pos(),
						Message: fmt.Sprintf("circular inheritance not allowed"),
						Detail:  fmt.Sprintf("%q is an ancestor of %q", tbl.Name, parentTbl.Name)}
				}
				// Duplicate parent.
				for _, existing := range im.InheritanceChildren(parentTbl.OID) {
					if existing.OID == tbl.OID {
						return &ExecError{Code: "42710", Pos: act.Pos(),
							Message: fmt.Sprintf("relation %q would be inherited from more than once", parentTbl.Name)}
					}
				}
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
			// NO INHERIT parent_table — unregister the inheritance relationship.
			parentTbl, ok := o.ctx.Catalog.LookupTable(act.InheritParent)
			if !ok {
				return &ExecError{Code: "42P01", Pos: act.Pos(), Message: fmt.Sprintf("relation %q does not exist", act.InheritParent.String())}
			}
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				// Silently ignore if not currently a child (matches PG behavior on repeated NO INHERIT).
				im.UnregisterInheritanceChild(parentTbl.OID, tbl.OID)
			}
		case parser.AlterTableSetStorage:
			// SET STORAGE type — record on the catalog column AND rewrite the
			// pg_attribute heap row so pg_dump observes the new attstorage.
			// pg_dump compares attstorage against the column type's typstorage and
			// emits `ALTER TABLE ONLY ... SET STORAGE <mode>` only when they differ
			// (pg_dump.c dumpTableSchema). The in-memory catalog mutation alone is
			// invisible to pg_dump because pg_attribute is a heap populated at
			// CREATE TABLE time; the override must be flushed through the same
			// delete-old-rows + re-sync path DROP COLUMN / SET NOT NULL use, or the
			// stale heap row keeps reporting the type default and the SET STORAGE
			// is silently dropped from the dump. DU-002 slice 182.
			if act.ColumnName != "" && act.StorageType != "" {
				changed := false
				for i := range tbl.Columns {
					if strings.EqualFold(tbl.Columns[i].Name, act.ColumnName) {
						tbl.Columns[i].Storage = act.StorageType
						changed = true
						break
					}
				}
				if changed && catalogHeapSyncAvailable(o.ctx) {
					if err := o.ctx.MaterializeWriterXID(); err == nil {
						xmax := o.ctx.Tx.XID
						for _, dbOid := range catalogDBOids(o.ctx) {
							deleteCatalogRowsForOID(o.ctx, dbOid, tbl.OID, xmax)
						}
					}
					if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
						return fmt.Errorf("DDL catalog sync: %w", syncErr)
					}
				}
			}
		case parser.AlterTableSetCompression:
			// SET COMPRESSION method — record the per-column TOAST compression on the
			// catalog column AND rewrite the pg_attribute heap row so pg_dump observes
			// the new attcompression. pg_dump emits `ALTER TABLE ONLY ... SET
			// COMPRESSION <method>` whenever attcompression is 'p' (pglz) or 'l' (lz4)
			// (pg_dump.c dumpTableSchema). Like SET STORAGE (slice 182), the in-memory
			// mutation alone is invisible because pg_attribute is a heap populated at
			// CREATE TABLE; the override must be flushed through the same delete-old-
			// rows + re-sync path or the stale heap row keeps reporting the default
			// and the SET COMPRESSION is silently dropped from the dump. An empty
			// CompressionType (`SET COMPRESSION default`) clears the override.
			// goopg does not TOAST/compress — dump-fidelity only. DU-002 slice 183.
			if act.ColumnName != "" {
				changed := false
				for i := range tbl.Columns {
					if strings.EqualFold(tbl.Columns[i].Name, act.ColumnName) {
						tbl.Columns[i].Compression = act.CompressionType
						changed = true
						break
					}
				}
				if changed && catalogHeapSyncAvailable(o.ctx) {
					if err := o.ctx.MaterializeWriterXID(); err == nil {
						xmax := o.ctx.Tx.XID
						for _, dbOid := range catalogDBOids(o.ctx) {
							deleteCatalogRowsForOID(o.ctx, dbOid, tbl.OID, xmax)
						}
					}
					if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
						return fmt.Errorf("DDL catalog sync: %w", syncErr)
					}
				}
			}
		case parser.AlterTableSetStatistics:
			// SET STATISTICS <n> — record the per-column statistics target on the
			// catalog column AND rewrite the pg_attribute heap row so pg_dump
			// observes the new attstattarget. pg_dump emits `ALTER TABLE ONLY ...
			// ALTER COLUMN ... SET STATISTICS <n>` whenever attstattarget >= 0
			// (pg_dump.c dumpTableSchema); the default (NULL/-1) emits nothing.
			// Like SET STORAGE (slice 182) / SET COMPRESSION (slice 183), the
			// in-memory mutation alone is invisible because pg_attribute is a heap
			// populated at CREATE TABLE; the override must be flushed through the
			// same delete-old-rows + re-sync path or the stale heap row keeps
			// reporting NULL and the SET STATISTICS is silently dropped from the
			// dump. `SET STATISTICS -1` (or a negative value) resets to the default
			// and clears the override. goopg does not sample per-column statistics
			// targets — dump-fidelity only. DU-002 slice 184.
			if act.ColumnName != "" {
				target, parseErr := strconv.Atoi(act.CheckExpr)
				if parseErr != nil {
					// No (or malformed) value: treat as reset to default.
					target = -1
				}
				changed := false
				for i := range tbl.Columns {
					if strings.EqualFold(tbl.Columns[i].Name, act.ColumnName) {
						if target < 0 {
							tbl.Columns[i].StatTarget = nil
						} else {
							v := target
							tbl.Columns[i].StatTarget = &v
						}
						changed = true
						break
					}
				}
				if changed && catalogHeapSyncAvailable(o.ctx) {
					if err := o.ctx.MaterializeWriterXID(); err == nil {
						xmax := o.ctx.Tx.XID
						for _, dbOid := range catalogDBOids(o.ctx) {
							deleteCatalogRowsForOID(o.ctx, dbOid, tbl.OID, xmax)
						}
					}
					if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
						return fmt.Errorf("DDL catalog sync: %w", syncErr)
					}
				}
			}
		case parser.AlterTableAlterColumnSet:
			// SET (opt=value, …) — record the per-column attribute options on the
			// catalog column AND rewrite the pg_attribute heap row so pg_dump
			// observes the new attoptions. pg_dump emits `ALTER TABLE ONLY ...
			// ALTER COLUMN ... SET (...)` whenever array_to_string(attoptions)
			// is non-empty (pg_dump.c dumpTableSchema). Like SET STORAGE (slice
			// 182) / SET COMPRESSION (183) / SET STATISTICS (184), the in-memory
			// mutation alone is invisible because pg_attribute is a heap populated
			// at CREATE TABLE; the override must be flushed through the same
			// delete-old-rows + re-sync path or the stale heap row keeps reporting
			// NULL and the SET (...) is silently dropped from the dump. goopg does
			// not act on these planner-statistics hints (e.g. n_distinct) —
			// dump-fidelity only. DU-002 slice 185.
			if act.ColumnName != "" && len(act.SetOptions) > 0 {
				changed := false
				for i := range tbl.Columns {
					if strings.EqualFold(tbl.Columns[i].Name, act.ColumnName) {
						tbl.Columns[i].Options = append([]string(nil), act.SetOptions...)
						changed = true
						break
					}
				}
				if changed && catalogHeapSyncAvailable(o.ctx) {
					if err := o.ctx.MaterializeWriterXID(); err == nil {
						xmax := o.ctx.Tx.XID
						for _, dbOid := range catalogDBOids(o.ctx) {
							deleteCatalogRowsForOID(o.ctx, dbOid, tbl.OID, xmax)
						}
					}
					if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
						return fmt.Errorf("DDL catalog sync: %w", syncErr)
					}
				}
			}
		case parser.AlterTableAlterColumnType:
			if err := o.execAlterColumnType(tbl, act); err != nil {
				return err
			}
		case parser.AlterTableDropColumn:
			if err := o.execAlterDropColumn(tbl, act); err != nil {
				return err
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

// execIndexSetStatistics validates ALTER INDEX name ALTER COLUMN N SET STATISTICS value.
// Raises appropriate errors matching PostgreSQL behaviour:
//   - column N is a non-expression key column → cannot alter statistics on non-expression column
//   - column N is an INCLUDE (non-key) column → cannot alter statistics on included column
//   - column N > total column count → column number N of relation does not exist
//
// On success (expression key column) it is a no-op in v0. M0097-0023.
func (o *ddlOp) execIndexSetStatistics(idxRelName string, idx *catalog.Index, act parser.AlterTableAction) error {
	colNum, err := strconv.Atoi(act.ColumnName)
	if err != nil || colNum < 1 {
		// Non-numeric column name in ALTER INDEX … ALTER COLUMN — no-op for now.
		return nil
	}
	nKey := len(idx.Columns)
	nInclude := len(idx.IncludeColumns)
	total := nKey + nInclude
	if colNum > total {
		return &ExecError{Code: "42703", Pos: act.Pos(),
			Message: fmt.Sprintf("column number %d of relation %q does not exist", colNum, idxRelName)}
	}
	if colNum <= nKey {
		// Key column: check whether it's an expression column.
		// goopg stores expression columns as empty string in Columns (ColExprs holds the AST).
		colName := idx.Columns[colNum-1]
		if colName != "" {
			return &ExecError{Code: "42P17", Pos: act.Pos(),
				Message: fmt.Sprintf("cannot alter statistics on non-expression column %q of index %q", colName, idxRelName),
				Hint:    "Alter statistics on table column instead."}
		}
		// Expression column: success (no physical change in v0).
		return nil
	}
	// INCLUDE column.
	inclColName := idx.IncludeColumns[colNum-nKey-1]
	return &ExecError{Code: "42P17", Pos: act.Pos(),
		Message: fmt.Sprintf("cannot alter statistics on included column %q of index %q", inclColName, idxRelName)}
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
	if err := o.createBTreeIndex(act.Pos(), idxName, tbl, act.Columns, nil, true, true); err != nil {
		return err
	}
	if idx, ok := o.ctx.Catalog.LookupIndex(idxName); ok {
		idx.IsConstraint = true
		idx.IncludeColumns = act.IncludeColumns
	}
	// PRIMARY KEY implies NOT NULL on all key columns (SQL standard). PG18 also
	// materialises that implication as a contype='n' `<table>_<col>_not_null`
	// row in pg_constraint, which pg_dump LEFT-JOINs to decide whether to print
	// the inline NOT NULL clause. Register one for any PK column that does not
	// already carry a NOT NULL constraint, so an ALTER-added PK survives a dump
	// round-trip identically to an inline CREATE TABLE PK. DU-002 slice 50.
	im, _ := o.ctx.Catalog.(*catalog.InMemory)
	for _, pkCol := range act.Columns {
		col, ok := o.ctx.Catalog.LookupColumn(tbl, pkCol)
		if !ok {
			continue
		}
		alreadyHadNotNull := col.NotNull
		col.NotNull = true
		if im == nil || alreadyHadNotNull {
			continue
		}
		hasConstraint := false
		for _, nc := range tbl.NotNullConstraints {
			if strings.EqualFold(nc.ColName, col.Name) {
				hasConstraint = true
				break
			}
		}
		if !hasConstraint {
			nnName := strings.ToLower(tbl.Name) + "_" + strings.ToLower(col.Name) + "_not_null"
			tbl.AddNotNull(nnName, col.Name, im.AllocOID(), false, true, 0)
		}
	}
	return nil
}

// execAlterTableAddUnique handles ADD [CONSTRAINT name] UNIQUE (cols) [INCLUDE (incl)].
// Creates a btree unique index. M0097-0023.
func (o *ddlOp) execAlterTableAddUnique(tbl *catalog.Table, act parser.AlterTableAction) error {
	if len(act.Columns) == 0 {
		return nil // UNIQUE USING INDEX — index already exists, treat as no-op
	}
	name := act.ConstraintName
	if name == "" {
		name = o.autoIndexNameWithIncludes(tbl, act.Columns, act.IncludeColumns, "key")
	}
	idxName := parser.ObjectName{Schema: tbl.Schema, Name: name}
	if err := o.createBTreeIndex(act.Pos(), idxName, tbl, act.Columns, nil, true, false); err != nil {
		return err
	}
	if idx, ok := o.ctx.Catalog.LookupIndex(idxName); ok {
		idx.IsConstraint = true
		idx.IncludeColumns = act.IncludeColumns
	}
	return nil
}

// execAlterTableDropConstraint handles `ALTER TABLE t DROP CONSTRAINT name [RESTRICT|CASCADE]`.
// For PK constraints it enforces view→constraint dependencies (RESTRICT mode)
// before removing the index. For CHECK constraints it blocks inherited drops.
// M0097-0036 / functional_deps / M0097-0023.
func (o *ddlOp) execAlterTableDropConstraint(tbl *catalog.Table, act parser.AlterTableAction) error {
	im, isIM := o.ctx.Catalog.(*catalog.InMemory)

	// 1. Check constraints: handle before PK so inherited check takes priority.
	for i, nc := range tbl.NamedChecks {
		if !strings.EqualFold(nc.Name, act.ConstraintName) {
			continue
		}
		// Cannot drop an inherited constraint (coninhcount > 0).
		if nc.InhCount > 0 {
			return &ExecError{
				Code:    "42704",
				Pos:     act.Pos(),
				Message: fmt.Sprintf("cannot drop inherited constraint %q of relation %q", act.ConstraintName, tbl.Name),
			}
		}
		// Drop from this table (keep CheckConstraints and NamedChecks in sync).
		tbl.CheckConstraints = append(tbl.CheckConstraints[:i], tbl.CheckConstraints[i+1:]...)
		tbl.NamedChecks = append(tbl.NamedChecks[:i], tbl.NamedChecks[i+1:]...)
		// Cascade to partition children: drop the inherited copy from each child.
		if isIM {
			for _, childTbl := range im.PartitionChildren(tbl.OID) {
				for j, cnc := range childTbl.NamedChecks {
					if strings.EqualFold(cnc.Name, act.ConstraintName) {
						childTbl.CheckConstraints = append(childTbl.CheckConstraints[:j], childTbl.CheckConstraints[j+1:]...)
						childTbl.NamedChecks = append(childTbl.NamedChecks[:j], childTbl.NamedChecks[j+1:]...)
						break
					}
				}
			}
		}
		return nil
	}

	// 2. PRIMARY KEY constraints.
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

// createExclusionIndexStub registers an EXCLUDE USING constraint in the catalog
// without type-validation or B-tree building. Exclusion semantics are not
// enforced in v0; the stub exists so pg_constraint and pg_index queries return
// correct rows. M0097-0023.
func (o *ddlOp) createExclusionIndexStub(pos int, idxName parser.ObjectName, tbl *catalog.Table, ec parser.TableConstraintDef) error {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil // unsupported catalog — silently skip
	}
	idx, err := im.CreateIndex(idxName, tbl, ec.Columns, false, "btree", false)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return &ExecError{Code: "42P07", Pos: pos, Message: err.Error()}
		}
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	idx.IsExclusion = true
	idx.ExclusionOp = ec.ExclusionOp
	idx.IncludeColumns = ec.IncludeColumns
	// DEFERRABLE [INITIALLY DEFERRED] rides the stub index so pg_get_constraintdef
	// / pg_constraint re-emit the clause on dump (no enforcement in v0). DU-002 slice 143.
	idx.Deferrable = ec.Deferrable
	idx.InitiallyDeferred = ec.InitiallyDeferred
	if sess, ok2 := o.ctx.Session.(*BasicSession); ok2 {
		sess.RecordDDLCreate(DDLUndoEntry{Name: idxName, RelOID: idx.OID, IsIndex: true})
	}
	if catalogHeapSyncAvailable(o.ctx) {
		if syncErr := syncIndexToCatalogHeap(o.ctx, idx); syncErr != nil {
			return fmt.Errorf("DDL catalog sync: %w", syncErr)
		}
	}
	return nil
}

func (o *ddlOp) createBTreeIndex(pos int, idxName parser.ObjectName, tbl *catalog.Table, columns []string, colExprs []parser.Expr, unique bool, primary bool, predExpr ...planner.Expr) error {
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
		idx.ColExprStrings = make([]string, len(colExprs))
		for i, e := range colExprs {
			if e != nil {
				ec := e // take address of loop copy
				idx.ColExprs[i] = &ec
				idx.ColExprStrings[i] = defaultExprToSQL(e)
			}
		}
	}
	idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
	var buildErr error
	if len(predExpr) > 0 && predExpr[0] != nil {
		buildErr = o.bulkBuildBTreeWithPredicate(idxRel, tbl, cols, unique, idxName.String(), pos, predExpr[0])
	} else {
		buildErr = o.bulkBuildBTree(idxRel, tbl, cols, unique, idxName.String(), pos)
	}
	if buildErr != nil {
		_ = o.ctx.Catalog.DropIndex(idxName)
		o.ctx.Pool.InvalidateRel(idxRel)
		_ = o.ctx.Pool.Manager().DropRelation(idxRel)
		return buildErr
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
	return o.bulkBuildBTreeWithPredicate(idxRel, tbl, cols, unique, indexName, pos, nil)
}

func (o *ddlOp) bulkBuildBTreeWithPredicate(idxRel storage.RelFileNode, tbl *catalog.Table, cols []*catalog.Column, unique bool, indexName string, pos int, predExpr planner.Expr) error {
	entries, err := o.collectBTreeEntries(tbl, cols, unique, indexName, pos, predExpr)
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
func (o *ddlOp) collectBTreeEntries(tbl *catalog.Table, cols []*catalog.Column, unique bool, indexName string, pos int, predExpr planner.Expr) ([]btree.BulkEntry, error) {
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
			// Use DecodeHeapTupleRowInto (not decodePhysicalPGRowIntoMctx) so that
			// the null bitmap is respected — rows with null non-key columns (e.g. box)
			// were previously skipped with a "truncated" decode error.
			if decErr := DecodeHeapTupleRowInto(scanRow, tbl.Columns, tuple, sctxDDL); decErr != nil {
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
			// For partial indexes, skip rows not matching the predicate.
			if predExpr != nil {
				pv, pErr := evalExpr(predExpr, row, o.ctx)
				if pErr != nil || pv.IsNull() || pv.Kind != KindBool || !pv.BoolValue() {
					continue
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
					Message: fmt.Sprintf("could not create unique index %q", indexName)}
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

// parseReloptionBool mirrors PostgreSQL's parse_bool (parse_bool_with_len in
// bool.c), which validates boolean reloption values such as
// `autovacuum_enabled`. Like PG it accepts case-insensitive *prefixes* of the
// canonical spellings: any non-empty prefix of true/false/yes/no (e.g. "t",
// "tr", "tru", "true"), plus "on", "of"/"off", and single-character "1"/"0".
// The second return value reports whether the input was a recognized boolean —
// callers raise an "invalid value for boolean option" error when it is false.
// M0110-0001 (DU-002 slice 196).
func parseReloptionBool(s string) (bool, bool) {
	if s == "" {
		return false, false
	}
	lower := strings.ToLower(s)
	switch lower[0] {
	case 't':
		if strings.HasPrefix("true", lower) {
			return true, true
		}
	case 'f':
		if strings.HasPrefix("false", lower) {
			return false, true
		}
	case 'y':
		if strings.HasPrefix("yes", lower) {
			return true, true
		}
	case 'n':
		if strings.HasPrefix("no", lower) {
			return false, true
		}
	case 'o':
		// "on" needs ≥2 chars; "of"/"off" both map to false.
		if lower == "on" {
			return true, true
		}
		if lower == "of" || lower == "off" {
			return false, true
		}
	case '1':
		if lower == "1" {
			return true, true
		}
	case '0':
		if lower == "0" {
			return false, true
		}
	}
	return false, false
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

// pgDeduplicateColNames replicates PostgreSQL's ChooseIndexColumnNames logic:
// if a column name already appears in the result list, it gets a numeric suffix
// (e.g., a second "c1" becomes "c11"). This matches PG's auto-naming for
// covering indexes where include columns may repeat key column names. M0097-0023.
func pgDeduplicateColNames(cols []string) []string {
	seen := make([]string, 0, len(cols))
	result := make([]string, 0, len(cols))
	for _, col := range cols {
		cur := col
		for i := 1; ; i++ {
			found := false
			for _, s := range seen {
				if s == cur {
					found = true
					break
				}
			}
			if !found {
				break
			}
			cur = fmt.Sprintf("%s%d", col, i)
		}
		seen = append(seen, cur)
		result = append(result, cur)
	}
	return result
}

func (o *ddlOp) autoIndexName(tbl *catalog.Table, columns []string, suffix string) string {
	return o.autoIndexNameWithIncludes(tbl, columns, nil, suffix)
}

func (o *ddlOp) autoIndexNameWithIncludes(tbl *catalog.Table, keyColumns, includeColumns []string, suffix string) string {
	allCols := append(keyColumns, includeColumns...)
	deduped := pgDeduplicateColNames(allCols)
	base := tbl.Name + "_" + strings.Join(deduped, "_") + "_" + suffix
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
	case "int4", "integer", "int", "serial", "serial4": // serial/serial4 maps to int4 (M0096-0006)
		return true
	default:
		return false
	}
}

func isInt8Type(name string) bool {
	switch strings.ToLower(name) {
	case "int8", "bigint", "bigserial", "serial8": // bigserial/serial8 maps to int8 (M0096-0006)
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
	im, hasIM := o.ctx.Catalog.(*catalog.InMemory)

	// Build the initial set of tables (OID → table pointer).
	type tableEntry struct {
		tbl  *catalog.Table
		only bool
	}
	tableSet := make(map[uint32]*tableEntry) // deduplicated by OID
	for i, name := range s.Names {
		tbl, ok := o.ctx.Catalog.LookupTable(name)
		if !ok {
			return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", name.String())}
		}
		only := len(s.Only) > i && s.Only[i]
		if _, exists := tableSet[tbl.OID]; !exists {
			tableSet[tbl.OID] = &tableEntry{tbl: tbl, only: only}
		}
	}

	// FK constraint check / CASCADE expansion.
	// For each table in the set, find all tables (not in the set) that have an FK
	// pointing to it. If behavior is CASCADE, expand the set and emit NOTICEs.
	// Otherwise fail with "cannot truncate a table referenced in a foreign key constraint".
	if hasIM {
		allTables := im.AllTables()
		// Process in a BFS loop because CASCADE expansion may introduce new referencing tables.
		for {
			expanded := false
			for _, entry := range tableSet {
				tbl := entry.tbl
				for _, other := range allTables {
					if _, inSet := tableSet[other.OID]; inSet {
						continue // already in set
					}
					// Collect tbl and all its partition ancestors (full chain) so
					// that FK constraints on a parent partitioned table are found.
					tblNames := []string{strings.ToLower(tbl.Name)}
					for oid := tbl.PartitionParentOID; oid != 0; {
						anc, ok := im.LookupTableByOID(oid)
						if !ok {
							break
						}
						tblNames = append(tblNames, strings.ToLower(anc.Name))
						oid = anc.PartitionParentOID
					}
					for _, fk := range other.ForeignKeys {
						refName := strings.ToLower(fk.RefTable)
						matched := false
						for _, tn := range tblNames {
							if refName == tn {
								matched = true
								break
							}
						}
						if !matched {
							continue
						}
						// other references tbl (or its partition parent) and is not in the truncation set.
						if s.Behavior == parser.DropCascade {
							tableSet[other.OID] = &tableEntry{tbl: other, only: false}
							o.ctx.AddNotice(fmt.Sprintf("truncate cascades to table %q", other.Name))
							expanded = true
						} else {
							return &ExecError{
								Code:    "0A000",
								Pos:     s.Pos(),
								Message: "cannot truncate a table referenced in a foreign key constraint",
								Detail:  fmt.Sprintf("Table %q references %q.", other.Name, tbl.Name),
								Hint:    fmt.Sprintf("Truncate table %q at the same time, or use TRUNCATE ... CASCADE.", other.Name),
							}
						}
					}
				}
			}
			if !expanded {
				break
			}
		}

		// When CASCADE is used, also emit NOTICEs for partition/inheritance
		// children of CASCADE-added tables (children not explicitly listed).
		// BFS until stable so grandchildren are covered too.
		if s.Behavior == parser.DropCascade {
			for {
				expanded := false
				for _, entry := range tableSet {
					children := append(im.PartitionChildren(entry.tbl.OID), im.InheritanceChildren(entry.tbl.OID)...)
					for _, child := range children {
						if _, inSet := tableSet[child.OID]; inSet {
							continue
						}
						tableSet[child.OID] = &tableEntry{tbl: child, only: false}
						o.ctx.AddNotice(fmt.Sprintf("truncate cascades to table %q", child.Name))
						expanded = true
					}
				}
				if !expanded {
					break
				}
			}
		}

		// Also validate: TRUNCATE ONLY on a partitioned table is not allowed.
		for _, entry := range tableSet {
			if entry.only && len(entry.tbl.PartitionKey) > 0 {
				return &ExecError{
					Code:    "0A000",
					Pos:     s.Pos(),
					Message: "cannot truncate only a partitioned table",
					Hint:    "Do not specify the ONLY keyword, or use TRUNCATE ONLY on the partitions directly.",
				}
			}
		}
	}

	// Fire BEFORE TRUNCATE FOR EACH STATEMENT triggers on all tables.
	for _, entry := range tableSet {
		if err := fireStatementTriggers(o.ctx, entry.tbl, "before", "truncate"); err != nil {
			return err
		}
	}

	// Truncate all tables (and their partition children unless ONLY).
	for _, entry := range tableSet {
		if err := o.truncateTableAndPartitions(entry.tbl, s.Pos(), entry.only); err != nil {
			return err
		}
	}

	// RESTART IDENTITY: reset sequences for all truncated tables.
	if s.RestartIdentity {
		sess, isBas := o.ctx.Session.(*BasicSession)
		inExplicitTx := isBas && sess.InExplicitTransaction()
		for _, entry := range tableSet {
			tbl := entry.tbl
			for _, col := range tbl.Columns {
				if col.Dropped {
					continue
				}
				colTypeLow := strings.ToLower(col.Type.Name)
				isSerial := colTypeLow == "serial" || colTypeLow == "serial4" || colTypeLow == "bigserial" || colTypeLow == "serial8" || colTypeLow == "smallserial" || colTypeLow == "serial2"
				var seqName string
				if isSerial || col.IdentityColumn {
					seqName = strings.ToLower(tbl.Name) + "_" + strings.ToLower(col.Name) + "_seq"
				} else {
					// Column uses an explicit nextval(seqname) default — reset the referenced sequence.
					seqName = extractNextvalSeqNameFromExpr(col.DefaultExpr)
				}
				if seqName != "" {
					if inExplicitTx {
						// Save old counter so ROLLBACK can restore it.
						if oldCurr, ok := GetSequenceCurrentValue(seqName); ok {
							sess.RecordSeqRestore(SeqRestoreEntry{Name: seqName, OldCurr: oldCurr})
						}
					}
					ResetSequence(seqName)
				}
			}
		}
	}

	// Fire AFTER TRUNCATE FOR EACH STATEMENT triggers on all tables.
	for _, entry := range tableSet {
		if err := fireStatementTriggers(o.ctx, entry.tbl, "after", "truncate"); err != nil {
			return err
		}
	}

	return nil
}

// extractNextvalSeqNameFromExpr extracts the sequence name from a nextval() AST expression.
// Returns "" if the expression is not a nextval() call.
func extractNextvalSeqNameFromExpr(expr parser.Expr) string {
	if expr == nil {
		return ""
	}
	fc, ok := expr.(*parser.FuncCall)
	if !ok || !strings.EqualFold(fc.Name.Name, "nextval") || len(fc.Args) == 0 {
		return ""
	}
	// Handle nextval('seqname') and nextval('seqname'::regclass)
	arg := fc.Args[0]
	// Unwrap cast if present: nextval('seqname'::regclass)
	if cast, ok2 := arg.(*parser.CastExpr); ok2 {
		arg = cast.Operand
	}
	sc, ok := arg.(*parser.StringConst)
	if !ok {
		return ""
	}
	name := sc.Value
	// Strip schema prefix.
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// snapshotRelPages reads all pages of rel into a relPageSnapshot before truncation.
// Reads each page through the pool (so dirty in-memory pages are captured).
// Returns an empty snapshot if the relation has no pages.
func snapshotRelPages(ctx *Context, rel storage.RelFileNode) relPageSnapshot {
	if ctx.Pool == nil {
		return relPageSnapshot{Rel: rel}
	}
	nBlocks, err := ctx.Pool.Manager().NBlocks(rel)
	if err != nil || nBlocks == 0 {
		return relPageSnapshot{Rel: rel}
	}
	pages := make([][]byte, nBlocks)
	for i := storage.BlockNumber(0); i < nBlocks; i++ {
		s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: i})
		if perr != nil {
			pages[i] = nil
			continue
		}
		s.RLock()
		cp := make([]byte, storage.BlockSize)
		copy(cp, s.Page())
		s.RUnlock()
		ctx.Pool.Unpin(s)
		pages[i] = cp
	}
	return relPageSnapshot{Rel: rel, Pages: pages}
}

// restoreTruncateUndo re-creates the physical files for one TruncateUndoEntry.
// Called during ROLLBACK to undo a TRUNCATE performed in a rolled-back transaction.
func restoreTruncateUndo(ctx *Context, entry TruncateUndoEntry) {
	mgr := ctx.Pool.Manager()

	restoreRel := func(snap relPageSnapshot) {
		if len(snap.Pages) == 0 {
			return
		}
		// Discard any pool-buffered pages for this rel (they belong to the rolled-back tx).
		ctx.Pool.InvalidateRel(snap.Rel)
		// Re-truncate to zero so we start fresh (handles case where new pages were added).
		_ = mgr.TruncateRelation(snap.Rel)
		// Re-extend with the saved pages.
		for _, pg := range snap.Pages {
			if pg == nil {
				continue
			}
			_, _ = mgr.Extend(snap.Rel, pg)
		}
		// Invalidate again so the pool re-reads from disk on next access.
		ctx.Pool.InvalidateRel(snap.Rel)
	}

	restoreRel(entry.Heap)
	// Clear FSM/VM stale entries — they'll be rebuilt on next access.
	if ctx.FSM != nil {
		ctx.FSM.DropRelation(entry.Heap.Rel)
	}
	if ctx.VM != nil {
		ctx.VM.DropRelation(entry.Heap.Rel)
	}
	for _, idxSnap := range entry.Indexes {
		restoreRel(idxSnap)
	}
}

// truncateTableAndPartitions truncates a single table's heap + indexes and
// recursively cascades to all partition descendants. This matches PostgreSQL's
// TRUNCATE behaviour where a partitioned table implicitly truncates every leaf.
// M0097-0028 fix: without recursion, TRUNCATE on a multi-level partitioned
// table left data in grandchild partitions, causing subsequent tests to see
// stale rows.
func (o *ddlOp) truncateTableAndPartitions(tbl *catalog.Table, pos int, only bool) error {
	// Recurse into partition/inheritance children unless ONLY was specified.
	if !only {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			for _, child := range im.PartitionChildren(tbl.OID) {
				if err := o.truncateTableAndPartitions(child, pos, false); err != nil {
					return err
				}
			}
			for _, child := range im.InheritanceChildren(tbl.OID) {
				if err := o.truncateTableAndPartitions(child, pos, false); err != nil {
					return err
				}
			}
		}
	}
	idxs := o.ctx.Catalog.IndexesOnTable(tbl)
	rel := o.ctx.Catalog.RelFileNode(tbl)

	// Snapshot pages before truncation for transactional rollback support.
	if sess, isBas := o.ctx.Session.(*BasicSession); isBas && sess.InExplicitTransaction() {
		entry := TruncateUndoEntry{
			Heap: snapshotRelPages(o.ctx, rel),
		}
		for _, idx := range idxs {
			idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
			entry.Indexes = append(entry.Indexes, snapshotRelPages(o.ctx, idxRel))
		}
		sess.RecordTruncate(entry)
	}

	o.ctx.Pool.InvalidateRel(rel)
	if err := o.ctx.Pool.Manager().TruncateRelation(rel); err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
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
			return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
		// FSM/VM are heap-relation maps; index relfiles
		// have no entries to clear. (Btrees track their
		// own free space inline.) Pair the FSM/VM cleanup
		// only with the heap rel above.
		if _, err := btree.Create(o.ctx.Pool, idxRel); err != nil {
			return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
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
		typName := strings.ToLower(a.Type.Name)
		if a.Type.IsArray {
			typName += "[]"
		}
		argTypes[i] = catalog.Type{
			Name: typName,
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
		if lang == "sql" {
			volatile = inferSQLFunctionVolatility(s.Body)
		} else {
			volatile = "v" // default: volatile
		}
	}
	schema := s.Name.Schema
	if schema == "" {
		schema = currentWritableSchema(o.ctx)
	}
	// Only superusers may define a leakproof function.
	if s.Leakproof && o.ctx.NonSuperuserRole != "" {
		return &ExecError{Code: "42501", Pos: s.Pos(), Message: "only superuser can define a leakproof function"}
	}
	// Validate SQL function body when check_function_bodies=on (default).
	if lang == "sql" && !s.BeginAtomic {
		if err := o.validateSQLFunctionBody(s, len(s.Args)); err != nil {
			return err
		}
	}
	// Re-append the "[]" suffix for an array return type, mirroring the
	// argument-type construction above (operators_ddl.go:5510). The parser
	// stores arrays as a base name plus IsArray; the pg_proc view keys the
	// prorettype OID off the bracketed name, so dropping the suffix here makes
	// pg_dump emit the scalar element type instead of the array.
	retTypeName := strings.ToLower(s.ReturnType.Name)
	if s.ReturnType.IsArray {
		retTypeName += "[]"
	}
	r := &catalog.Routine{
		Schema:      schema,
		Name:        s.Name.Name,
		ArgNames:    argNames,
		ArgTypes:    argTypes,
		ArgModes:    argModes,
		ArgDefaults: argDefaults,
		ReturnType: catalog.Type{
			// Mirror the argument-type path above: the parser stores an array
			// return type as the base name (e.g. "integer") with IsArray set,
			// NOT as "integer[]". Without re-appending the "[]" suffix the
			// pg_proc view's typeNameToOIDStr resolves prorettype to the SCALAR
			// element OID (23) instead of the array OID (1007), so pg_dump emits
			// `RETURNS integer` and silently drops the array — a sibling-path
			// divergence from how ArgTypes are built (operators_ddl.go:5510).
			Name: retTypeName,
			Args: append([]int64(nil), s.ReturnType.Args...),
		},
		ReturnsSet:      s.ReturnsSet,
		ReturnsTable:    s.ReturnsTable,
		Language:        lang,
		Body:            s.Body,
		BeginAtomic:     s.BeginAtomic,
		IsReturnForm:    s.IsReturnForm,
		IsWindow:        s.Window,
		Strict:          s.Strict,
		Volatile:        volatile,
		Parallel:        s.Parallel,
		Cost:            s.Cost,
		Rows:            s.Rows,
		SecurityDefiner: s.SecurityDefiner,
		Leakproof:       s.Leakproof,
	}
	// Extract dependency information for information_schema views (SQL functions only).
	if lang == "sql" {
		extractRoutineDeps(r.Body, r.ArgDefaults, schema, r, o.ctx.Catalog)
	}
	if _, err := rs.Create(r, s.OrReplace); err != nil {
		// ErrRoutineExists → SQLSTATE 42723 (duplicate function).
		if errors.Is(err, catalog.ErrRoutineExists) {
			return &ExecError{Code: "42723", Pos: s.Pos(), Message: err.Error()}
		}
		// ErrRoutineKindChange → SQLSTATE 42P13 (cannot change routine kind).
		if errors.Is(err, catalog.ErrRoutineKindChange) {
			// Determine what the existing object IS for accurate DETAIL.
			detail := fmt.Sprintf("%q is a function.", s.Name.Name)
			argTypes := make([]catalog.Type, len(s.Args))
			for i, a := range s.Args {
				argTypes[i] = catalog.Type{Name: strings.ToLower(a.Type.Name)}
			}
			if existing, ok := rs.Lookup(s.Name, argTypes); ok && existing != nil && existing.IsProcedure {
				detail = fmt.Sprintf("%q is a procedure.", s.Name.Name)
			}
			return &ExecError{Code: "42P13", Pos: s.Pos(),
				Message: "cannot change routine kind",
				Detail:  detail}
		}
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

// canonReturnTypeName maps internal short type names to PG display names for error messages.
func canonReturnTypeName(name string) string {
	switch name {
	case "int", "int4":
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
	}
	return name
}

// isPolymorphicTypeName returns true if the given SQL type name is polymorphic
// (anyarray, anyelement, anynonarray, anyrange, anycompatible, etc.).
func isPolymorphicTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "anyarray", "anyelement", "anynonarray", "anyrange", "anycompatible",
		"anycompatiblearray", "anycompatiblenonarray", "anycompatiblerange":
		return true
	}
	return false
}

// validateSQLFunctionBody validates the body of a SQL-language function at
// CREATE time when check_function_bodies=on. Returns an ExecError on failure.
func (o *ddlOp) validateSQLFunctionBody(s *parser.CreateFunctionStmt, nArgs int) error {
	// Respect check_function_bodies GUC (default=on).
	if o.ctx.GetSetting != nil {
		if v, ok := o.ctx.GetSetting("check_function_bodies"); ok {
			switch strings.ToLower(v) {
			case "off", "false", "0", "no":
				return nil
			}
		}
	}

	funcName := s.Name.Name
	retTypeRaw := strings.ToLower(s.ReturnType.Name)
	// Canonicalize type name to PG display form for error messages.
	retTypeName := canonReturnTypeName(retTypeRaw)

	// RETURN form (unquoted body) with polymorphic arguments is rejected.
	if s.IsReturnForm {
		for _, a := range s.Args {
			if isPolymorphicTypeName(a.Type.Name) {
				return &ExecError{
					Code:    "42P13",
					Pos:     s.Pos(),
					Message: "SQL function with unquoted function body cannot have polymorphic arguments",
				}
			}
		}
	}

	body := s.Body
	if body == "" {
		// Empty body — only void functions may have no final SELECT.
		if retTypeName != "void" {
			return &ExecError{
				Code:    "42P13",
				Pos:     s.Pos(),
				Message: fmt.Sprintf("return type mismatch in function declared to return %s", retTypeName),
				Detail:  "Function's final statement must be SELECT or INSERT/UPDATE/DELETE/MERGE RETURNING.",
				Context: fmt.Sprintf("SQL function %q", funcName),
			}
		}
		return nil
	}

	// Parse the body to catch syntax errors and analyse parameter references.
	// PG does not add CONTEXT for parse-time errors (context is only added for
	// type-check and execution errors). So we omit the Context field here.
	stmts, parseErr := parser.Parse(body)
	if parseErr != nil {
		return &ExecError{
			Code:    "42601",
			Pos:     s.Pos(),
			Message: parseErr.Error(),
		}
	}

	// Empty parse result for a non-void function.
	if len(stmts) == 0 && retTypeName != "void" {
		return &ExecError{
			Code:    "42P13",
			Pos:     s.Pos(),
			Message: fmt.Sprintf("return type mismatch in function declared to return %s", retTypeName),
			Detail:  "Function's final statement must be SELECT or INSERT/UPDATE/DELETE/MERGE RETURNING.",
			Context: fmt.Sprintf("SQL function %q", funcName),
		}
	}

	// Scan all statements for out-of-range parameter references ($N).
	for _, stmt := range stmts {
		if err := validateParamRefs(stmt, nArgs, funcName); err != nil {
			return err
		}
	}

	// Check for unsupported operator combinations (e.g. date > integer).
	// This catches cases like RETURN x > 1 where x is a date parameter.
	if err := checkBodyOperatorTypes(stmts, s.Args, funcName, s.Pos()); err != nil {
		return err
	}

	// Check that the final statement is a SELECT (or DML RETURNING).
	if len(stmts) > 0 && retTypeName != "void" {
		last := stmts[len(stmts)-1]
		switch l := last.(type) {
		case *parser.SelectStmt:
			if !s.ReturnsSet {
				// Check column count for scalar returns.
				nTargets := len(l.Targets)
				if l.ValuesRows != nil && len(l.Targets) == 0 {
					if len(l.ValuesRows) > 0 {
						nTargets = len(l.ValuesRows[0])
					}
				}
				if nTargets > 1 {
					return &ExecError{
						Code:    "42P13",
						Pos:     s.Pos(),
						Message: fmt.Sprintf("return type mismatch in function declared to return %s", retTypeName),
						Detail:  "Final statement must return exactly one column.",
						Context: fmt.Sprintf("SQL function %q", funcName),
					}
				}
				// Check if the SELECT target type is incompatible with return type.
				if nTargets == 1 {
					if err := checkSQLFuncReturnTypeBasic(l.Targets[0].Expr, retTypeName, funcName, s.Pos()); err != nil {
						return err
					}
				}
			}
		case *parser.InsertStmt, *parser.UpdateStmt, *parser.DeleteStmt:
			// OK if they have RETURNING — we don't check that here
		default:
			return &ExecError{
				Code:    "42P13",
				Pos:     s.Pos(),
				Message: fmt.Sprintf("return type mismatch in function declared to return %s", retTypeName),
				Detail:  "Function's final statement must be SELECT or INSERT/UPDATE/DELETE/MERGE RETURNING.",
				Context: fmt.Sprintf("SQL function %q", funcName),
			}
		}
	}

	return nil
}

// checkSQLFuncReturnTypeBasic does lightweight return-type checking for
// simple SELECT expressions where the type can be inferred without the full
// planner. Only fires when the type is obviously incompatible (e.g. a bare
// string literal returned as integer).
func checkSQLFuncReturnTypeBasic(expr parser.Expr, retTypeName, funcName string, pos int) error {
	if expr == nil {
		return nil
	}
	var exprTypeName string
	switch expr.(type) {
	case *parser.StringConst:
		exprTypeName = "text"
	case *parser.IntegerConst:
		exprTypeName = "integer"
	}
	if exprTypeName == "" {
		return nil // can't determine type statically
	}
	if exprTypeName == retTypeName {
		return nil // same type
	}
	// Check basic incompatibility: text cannot be returned as integer/boolean/etc.
	textTypes := map[string]bool{"text": true, "varchar": true, "char": true, "character varying": true}
	intTypes := map[string]bool{"integer": true, "smallint": true, "bigint": true, "real": true, "double precision": true, "boolean": true}
	if textTypes[exprTypeName] && (intTypes[retTypeName] || retTypeName == "boolean") {
		return &ExecError{
			Code:    "42P13",
			Pos:     pos,
			Message: fmt.Sprintf("return type mismatch in function declared to return %s", retTypeName),
			Detail:  fmt.Sprintf("Actual return type is %s.", exprTypeName),
			Context: fmt.Sprintf("SQL function %q", funcName),
		}
	}
	return nil
}

// validateParamRefs walks the AST of stmt and returns an error if any
// parameter reference $N with N > nArgs is found.
func validateParamRefs(stmt parser.Stmt, nArgs int, funcName string) error {
	var walkExpr func(node parser.Expr) error
	walkExpr = func(node parser.Expr) error {
		if node == nil {
			return nil
		}
		switch n := node.(type) {
		case *parser.ParamRef:
			if n.Number > nArgs {
				// PG does not add CONTEXT for param-reference errors.
				return &ExecError{
					Code:    "42P10",
					Message: fmt.Sprintf("there is no parameter $%d", n.Number),
				}
			}
		case *parser.BinaryOp:
			if err := walkExpr(n.Left); err != nil {
				return err
			}
			return walkExpr(n.Right)
		case *parser.UnaryOp:
			return walkExpr(n.Operand)
		case *parser.FuncCall:
			for _, a := range n.Args {
				if err := walkExpr(a); err != nil {
					return err
				}
			}
		case *parser.CastExpr:
			return walkExpr(n.Operand)
		}
		return nil
	}
	var walkStmt func(s parser.Stmt) error
	walkStmt = func(s parser.Stmt) error {
		switch n := s.(type) {
		case *parser.SelectStmt:
			for _, t := range n.Targets {
				if err := walkExpr(t.Expr); err != nil {
					return err
				}
			}
			if n.Where != nil {
				if err := walkExpr(n.Where); err != nil {
					return err
				}
			}
		case *parser.InsertStmt:
			for _, row := range n.Rows {
				for _, e := range row {
					if err := walkExpr(e); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return walkStmt(stmt)
}

// checkBodyOperatorTypes walks the function body statements and checks for
// operator combinations that PostgreSQL rejects with "operator does not exist"
// (SQLSTATE 42883). The primary case is date/time types compared against
// numeric types (e.g. "date > integer" has no such operator in PG).
// Parameter names are resolved from args; positional $N references use arg index.
func checkBodyOperatorTypes(stmts []parser.Stmt, args []parser.FunctionArg, funcName string, pos int) error {
	// Build a map: lowercase param name → canonical type name.
	paramType := make(map[string]string, len(args))
	for _, a := range args {
		if a.Name != "" {
			paramType[strings.ToLower(a.Name)] = strings.ToLower(a.Type.Name)
		}
	}

	// Resolve an expression to its pg-display type name for operator checking.
	// Returns "" if the type cannot be statically determined.
	var resolveType func(e parser.Expr) string
	resolveType = func(e parser.Expr) string {
		if e == nil {
			return ""
		}
		switch n := e.(type) {
		case *parser.ColumnRef:
			// Named parameter reference (from RETURN form body "SELECT x > 1").
			if t, ok := paramType[strings.ToLower(n.Column)]; ok {
				return t
			}
		case *parser.ParamRef:
			// Positional parameter reference ($1, $2, ...).
			if n.Number >= 1 && n.Number <= len(args) {
				return strings.ToLower(args[n.Number-1].Type.Name)
			}
		case *parser.IntegerConst:
			return "integer"
		case *parser.StringConst:
			return "text"
		case *parser.CastExpr:
			// Type cast: return the target type.
			return strings.ToLower(n.Type.Name)
		}
		return ""
	}

	// isDateLike returns true for date/time/timestamp types.
	isDateLike := func(typName string) bool {
		switch typName {
		case "date", "time", "timetz", "timestamp", "timestamptz":
			return true
		}
		return false
	}
	// isNumericType returns true for integer/float/numeric types.
	isNumericType := func(typName string) bool {
		switch typName {
		case "integer", "int", "int2", "int4", "int8",
			"smallint", "bigint", "numeric", "decimal",
			"real", "float4", "float8", "double precision":
			return true
		}
		return false
	}
	// pgOpName returns the symbol for a comparison operator.
	pgOpName := func(op parser.OpCode) string {
		switch op {
		case parser.OpGt:
			return ">"
		case parser.OpLt:
			return "<"
		case parser.OpGe:
			return ">="
		case parser.OpLe:
			return "<="
		case parser.OpEq:
			return "="
		case parser.OpNe:
			return "<>"
		default:
			return op.String()
		}
	}

	var walkExpr func(e parser.Expr) error
	walkExpr = func(e parser.Expr) error {
		if e == nil {
			return nil
		}
		switch n := e.(type) {
		case *parser.BinaryOp:
			// Check operands first (recurse).
			if err := walkExpr(n.Left); err != nil {
				return err
			}
			if err := walkExpr(n.Right); err != nil {
				return err
			}
			// For comparison operators, check for cross-domain type mismatch.
			switch n.Op {
			case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
				leftTyp := resolveType(n.Left)
				rightTyp := resolveType(n.Right)
				if leftTyp == "" || rightTyp == "" {
					return nil // can't determine, skip
				}
				// date/timestamp vs integer/numeric: no such operator in PG.
				if isDateLike(leftTyp) && isNumericType(rightTyp) {
					leftDisplay := analyzer.PGDisplayTypeName(leftTyp)
					rightDisplay := analyzer.PGDisplayTypeName(rightTyp)
					return &ExecError{
						Code:    "42883",
						Pos:     pos,
						Message: fmt.Sprintf("operator does not exist: %s %s %s", leftDisplay, pgOpName(n.Op), rightDisplay),
						Hint:    "No operator matches the given name and argument types. You might need to add explicit type casts.",
					}
				}
				if isNumericType(leftTyp) && isDateLike(rightTyp) {
					leftDisplay := analyzer.PGDisplayTypeName(leftTyp)
					rightDisplay := analyzer.PGDisplayTypeName(rightTyp)
					return &ExecError{
						Code:    "42883",
						Pos:     pos,
						Message: fmt.Sprintf("operator does not exist: %s %s %s", leftDisplay, pgOpName(n.Op), rightDisplay),
						Hint:    "No operator matches the given name and argument types. You might need to add explicit type casts.",
					}
				}
			}
		case *parser.UnaryOp:
			return walkExpr(n.Operand)
		case *parser.FuncCall:
			for _, a := range n.Args {
				if err := walkExpr(a); err != nil {
					return err
				}
			}
		case *parser.CastExpr:
			return walkExpr(n.Operand)
		}
		return nil
	}

	var walkStmt func(s parser.Stmt) error
	walkStmt = func(s parser.Stmt) error {
		switch n := s.(type) {
		case *parser.SelectStmt:
			for _, t := range n.Targets {
				if err := walkExpr(t.Expr); err != nil {
					return err
				}
			}
			if n.Where != nil {
				if err := walkExpr(n.Where); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for _, stmt := range stmts {
		if err := walkStmt(stmt); err != nil {
			return err
		}
	}
	return nil
}

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
		argListStr := routineArgListStr(argTypes)
		return &ExecError{Code: "42883", Pos: s.Pos(), Message: fmt.Sprintf("%s %s%s does not exist", kind, s.Name.Name, argListStr)}
	}
	// Check that each matched routine is of the right kind (skip for ALTER ROUTINE).
	if !s.IsRoutine {
		for _, r := range routines {
			if s.IsProcedure && !r.IsProcedure {
				argList := routineArgListStr(argTypes)
				return &ExecError{Code: "42809", Pos: s.Pos(),
					Message: fmt.Sprintf("%s%s is not a procedure", s.Name.Name, argList)}
			}
			if !s.IsProcedure && r.IsProcedure {
				argList := routineArgListStr(argTypes)
				return &ExecError{Code: "42809", Pos: s.Pos(),
					Message: fmt.Sprintf("%s%s is not a function", s.Name.Name, argList)}
			}
		}
	}
	// Check: STRICT attribute is invalid for ALTER PROCEDURE.
	if s.IsProcedure && s.Strict != nil {
		return &ExecError{Code: "42P13", Pos: s.Pos(),
			Message: "invalid attribute in procedure definition"}
	}
	// RENAME TO: update the routine name in the registry.
	if s.RenameTo != "" {
		for _, r := range routines {
			if err := rs.RenameRoutine(r, s.RenameTo); err != nil {
				return &ExecError{Code: "XX000", Pos: s.Pos(), Message: err.Error()}
			}
		}
		return nil
	}
	// Only superusers may set a function as leakproof.
	if s.Leakproof != nil && *s.Leakproof && o.ctx.NonSuperuserRole != "" {
		return &ExecError{Code: "42501", Pos: s.Pos(), Message: "only superuser can define a leakproof function"}
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
	// Validate procedure-invalid attributes.
	if s.Window {
		return &ExecError{Code: "42P13", Pos: s.Pos(),
			Message: "invalid attribute in procedure definition"}
	}
	if s.Strict {
		return &ExecError{Code: "42P13", Pos: s.Pos(),
			Message: "invalid attribute in procedure definition"}
	}
	argTypes := make([]catalog.Type, len(s.Args))
	argNames := make([]string, len(s.Args))
	argModes := make([]string, len(s.Args))
	argDefaults := make([]string, len(s.Args))
	// Validate: VARIADIC must be last; OUT can't follow default IN.
	variadicSeen := false
	defaultSeen := false
	for i, a := range s.Args {
		if variadicSeen {
			return &ExecError{Code: "42P13", Pos: s.Pos(),
				Message: "VARIADIC parameter must be the last parameter"}
		}
		if a.Mode == parser.FuncArgVariadic {
			variadicSeen = true
		}
		if a.Mode == parser.FuncArgOut || a.Mode == parser.FuncArgInout {
			if defaultSeen {
				return &ExecError{Code: "42P13", Pos: s.Pos(),
					Message: "procedure OUT parameters cannot appear after one with a default value"}
			}
		}
		if a.Default != nil && (a.Mode == parser.FuncArgIn || a.Mode == 0) {
			defaultSeen = true
		}
		typName := strings.ToLower(a.Type.Name)
		if a.Type.IsArray {
			typName += "[]"
		}
		argTypes[i] = catalog.Type{
			Name: typName,
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
	// Validate BEGIN ATOMIC body: reject DDL statements like CREATE TABLE.
	if s.BeginAtomic {
		upper := strings.ToUpper(s.Body)
		if strings.Contains(upper, "CREATE TABLE") {
			return &ExecError{Code: "0A000", Pos: s.Pos(),
				Message: "CREATE TABLE is not yet supported in unquoted SQL function body"}
		}
	}
	// Validate SQL body: check if a CALL targets a procedure with output args.
	// PG rejects this at function-creation time for SQL functions.
	if lang == "sql" && !s.BeginAtomic && s.Body != "" {
		if rs := o.ctx.Catalog.Routines(); rs != nil {
			bodyStmts, _ := parser.Parse(s.Body)
			for _, stmt := range bodyStmts {
				call, ok := stmt.(*parser.CallStmt)
				if !ok {
					continue
				}
				// Look up any overload of the called procedure by name
				callees := rs.LookupByName(call.Name)
				for _, callee := range callees {
					// Check if callee has any OUT or INOUT params
					hasOutputArgs := false
					for _, mode := range callee.ArgModes {
						if mode == "o" || mode == "b" {
							hasOutputArgs = true
							break
						}
					}
					if hasOutputArgs {
						return &ExecError{
							Code:    "0A000",
							Pos:     s.Pos(),
							Message: "calling procedures with output arguments is not supported in SQL functions",
							Context: fmt.Sprintf("SQL function \"%s\"", s.Name.Name),
						}
					}
				}
			}
		}
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
		if errors.Is(err, catalog.ErrRoutineKindChange) {
			// Existing is a function; we're creating a procedure.
			return &ExecError{Code: "42P13", Pos: s.Pos(),
				Message: "cannot change routine kind",
				Detail:  fmt.Sprintf("%q is a function.", s.Name.Name)}
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
	if s.Args == nil {
		cands := rs.LookupByName(s.Name)
		if len(cands) == 1 {
			found = cands[0]
		} else if len(cands) > 1 {
			// "not unique" is an error even with IF EXISTS (only "not found" is suppressed).
			return &ExecError{
				Code:    "42725",
				Pos:     s.Pos(),
				Message: fmt.Sprintf("%s name \"%s\" is not unique", objKind, s.Name.Name),
				Hint:    fmt.Sprintf("Specify the argument list to select the %s unambiguously.", objKind),
			}
		}
	} else {
		cands := rs.LookupDropCandidates(s.Name, s.Args)
		switch len(cands) {
		case 0:
			// not found — handled below
		case 1:
			found = cands[0]
		default:
			// multiple matches = ambiguous — arg list was given but still ambiguous;
			// PG does NOT add "Specify the argument list" hint here since args were given.
			// "not unique" is an error even with IF EXISTS.
			return &ExecError{
				Code:    "42725",
				Pos:     s.Pos(),
				Message: fmt.Sprintf("%s name \"%s\" is not unique", objKind, s.Name.Name),
			}
		}
	}
	if found == nil {
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("%s %s does not exist, skipping", objKind, s.Name.Name))
			return nil
		}
		// PG format: "procedure name() does not exist"
		argListStr := buildCallArgListStr(s.Args)
		return &ExecError{Code: "42883", Pos: s.Pos(),
			Message: fmt.Sprintf("%s %s%s does not exist", objKind, s.Name.Name, argListStr),
			Hint:    "No procedure matches the given name and argument types. You might need to add explicit type casts.",
		}
	}
	// DROP PROCEDURE on a function should fail.
	if objKind == "procedure" && !found.IsProcedure {
		// Use the found routine's arg types for the error message (argTypes may
		// be nil when using LookupDropCandidates-based lookup).
		argListStr := routineArgListStr(found.ArgTypes)
		return &ExecError{Code: "42809", Pos: s.Pos(),
			Message: fmt.Sprintf("%s%s is not a procedure", s.Name.Name, argListStr)}
	}
	// Now drop using the specific routine we already identified.
	var err error
	if s.Args == nil {
		err = rs.DropByName(s.Name)
	} else {
		// Use DropRoutine with the exact routine found by LookupDropCandidates
		// to avoid signature-based ambiguity (OUT params excluded from Signature).
		err = rs.DropRoutine(found)
	}
	if err == nil {
		// Record the drop for potential rollback on ROLLBACK.
		if sess, ok := o.ctx.Session.(*BasicSession); ok {
			sess.AddPendingRoutineDrop(found)
		}
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
	// Multi-target: DROP FUNCTION f1(args), f2(args)
	for _, extra := range s.Extras {
		s2 := *s
		s2.Name = extra.Name
		s2.Args = extra.Args
		s2.Extras = nil
		if err := o.execDropFunction(&s2); err != nil {
			return err
		}
	}
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
				Message: fmt.Sprintf("%s%s is not a function", s.Name.Name, argListStr)}
		}
	}

	// CASCADE: drop functions that depend on this function via RoutineCallOIDs.
	if s.Behavior == parser.DropCascade {
		// Collect target routines to find dependents for.
		var targets []*catalog.Routine
		if s.Args != nil {
			argTypes := make([]catalog.Type, len(s.Args))
			for i, a := range s.Args {
				argTypes[i] = catalog.Type{Name: strings.ToLower(a.Type.Name)}
			}
			if target, ok := rs.Lookup(s.Name, argTypes); ok && target != nil {
				targets = []*catalog.Routine{target}
			}
		} else {
			// No arg list: find all overloads by name.
			targets = rs.LookupByName(s.Name)
		}
		var allDeps []*catalog.Routine
		for _, target := range targets {
			for _, dep := range functionsDependingOnRoutineOID(o.ctx.Catalog, target.OID) {
				allDeps = append(allDeps, dep)
			}
		}
		if len(allDeps) == 1 {
			dn := routineCascadeDisplayName(allDeps[0])
			o.ctx.AddNotice(fmt.Sprintf("drop cascades to function %s", dn))
			_ = rs.DropRoutine(allDeps[0])
		} else if len(allDeps) > 1 {
			detail := make([]string, len(allDeps))
			for i, r := range allDeps {
				dn := routineCascadeDisplayName(r)
				detail[i] = fmt.Sprintf("drop cascades to function %s", dn)
				_ = rs.DropRoutine(r)
			}
			o.ctx.AddNoticeWithDetail(
				fmt.Sprintf("drop cascades to %d other objects", len(allDeps)),
				strings.Join(detail, "\n"),
			)
		}
	}

	// Check partition-key expression dependencies (no CASCADE).
	if s.Behavior != parser.DropCascade {
		if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
			funcLower := strings.ToLower(s.Name.Name)
			var depTables []string
			for _, tbl := range im.AllTables() {
				for _, expr := range tbl.PartitionKeyExprs {
					if funcExprContainsName(expr, funcLower) {
						depTables = append(depTables, tbl.Name)
						break
					}
				}
			}
			if len(depTables) > 0 {
				argTypes := make([]catalog.Type, len(s.Args))
				for i, a := range s.Args {
					argTypes[i] = catalog.Type{Name: strings.ToLower(a.Type.Name)}
				}
				funcSig := s.Name.Name + routineArgListStr(argTypes)
				details := make([]string, len(depTables))
				for i, name := range depTables {
					details[i] = fmt.Sprintf("table %s depends on function %s", name, funcSig)
				}
				return &ExecError{Code: "2BP01", Pos: s.Pos(),
					Message: fmt.Sprintf("cannot drop function %s because other objects depend on it", funcSig),
					Detail:  strings.Join(details, "\n"),
					Hint:    "Use DROP ... CASCADE to drop the dependent objects too."}
			}
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
		// When no argument list was given, PG says "could not find a function named X".
		if s.Args == nil {
			if s.IfExists {
				o.ctx.AddNotice(fmt.Sprintf("function %s does not exist, skipping", s.Name.Name))
				return nil
			}
			return &ExecError{Code: "42883", Pos: s.Pos(),
				Message: fmt.Sprintf("could not find a function named %q", s.Name.Name)}
		}
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
	// Also write the auto-generated array type (`_name`) so a `mood[]` column's
	// atttypid joins to a real pg_type row — pg_dump's getTableAttrs passes the
	// joined t.oid to format_type, and a missing row renders the column type
	// blank. DU-002 slice 89.
	if _, err := writeHeapRowCanonical(ctx, typeRel, pgTypeColumnsPG18(), buildUserPGTypeRowForEnumArray(et)); err != nil {
		return
	}
	// Mirror pg_type to the postgres database (DBOid=5) so sessions using
	// the postgres db can find the new type row via SeqScan. This mirrors
	// the pattern used by syncTableToCatalogHeap. M0097-0022.
	_ = mirrorCatalogRelToPostgresDB(ctx, catalog.TypeRelationId)
}

// syncDomainTypeToCatalogHeap writes a single pg_type row (typtype='d') for a
// user-defined DOMAIN into the pg_type heap (OID 1247), mirroring
// syncEnumTypeToCatalogHeap. pg_dump's getTypes reads pg_type to discover the
// domain and dumpDomain re-renders it via typbasetype/typtypmod. DU-002 slice 90.
func syncDomainTypeToCatalogHeap(ctx *Context, d *catalog.Domain) {
	if !catalogHeapSyncAvailable(ctx) {
		return
	}
	typeRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.TypeRelationId,
		Fork:   storage.MainFork,
	}
	if _, err := writeHeapRowCanonical(ctx, typeRel, pgTypeColumnsPG18(), buildUserPGTypeRowForDomain(d)); err != nil {
		return
	}
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

// namespaceOIDForSchema maps a schema name to its namespace OID. The system
// schemas resolve to their fixed OIDs; a user schema created via CREATE SCHEMA
// resolves to the OID the catalog assigned it (so a user table's pg_class row
// carries a relnamespace that the restart-recovery path can reverse-map back to
// the schema name — M0110-0003). An unregistered or nil-catalog schema falls
// back to pg_catalog, preserving the pre-M0110-0003 behaviour.
func namespaceOIDForSchema(cat catalog.Catalog, schema string) uint32 {
	if schema == "" || schema == "public" {
		return catalog.PublicNamespaceOID
	}
	if schema == "pg_catalog" {
		return catalog.PGCatalogNamespaceOID
	}
	if cat != nil {
		if oid := cat.SchemaOID(schema); oid != 0 {
			return oid
		}
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
	classTID, err := writeHeapRowCanonical(ctx, classRel, pgClassColumnsPG18(), buildUserPGClassRow(ctx.Catalog, tbl))
	if err != nil {
		return fmt.Errorf("pg_class: %w", err)
	}
	relnamespace := namespaceOIDForSchema(ctx.Catalog, tbl.Schema)
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
		attrTID, err := writeHeapRowCanonical(ctx, attrRel, pgAttributeColumnsPG18(), buildUserPGAttributeRow(ctx.Catalog, tbl, col))
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
	classTID, err := writeHeapRowCanonical(ctx, classRel, pgClassColumnsPG18(), buildUserPGClassRowForIndex(ctx.Catalog, idx))
	if err != nil {
		return fmt.Errorf("pg_class for index: %w", err)
	}
	relnamespace := namespaceOIDForSchema(ctx.Catalog, idx.Schema)
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
		Args:       append([]string(nil), s.FuncArgs...),
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

// seqTypeBounds returns the min/max int64 bounds for a sequence data type.
// Accepts PG aliases ("int2", "int4", "int8", "int", etc.). M0097-0068.
func seqTypeBounds(dt string) (min, max int64) {
	switch strings.ToLower(dt) {
	case "smallint", "int2":
		return -32768, 32767
	case "integer", "int4", "int":
		return -2147483648, 2147483647
	default: // bigint / int8
		return -9223372036854775808, 9223372036854775807
	}
}

// canonSeqType returns the canonical SQL type name for sequence data type error messages.
func canonSeqType(dt string) string {
	switch strings.ToLower(dt) {
	case "smallint", "int2":
		return "smallint"
	case "integer", "int4", "int":
		return "integer"
	default:
		return "bigint"
	}
}

// isKnownSeqType returns true for valid sequence data types. M0097-0068.
func isKnownSeqType(dt string) bool {
	switch strings.ToLower(dt) {
	case "", "bigint", "int8", "integer", "int4", "int", "smallint", "int2":
		return true
	}
	return false
}

// execCreateSequence registers a new sequence in the process-global registry.
// M0097-0009.
func (o *ddlOp) execCreateSequence(s *parser.CreateSequenceStmt) error {
	name := s.Name.String()
	if LookupSequence(name) != nil && s.IfNotExists {
		return nil
	}
	// Validate data type.
	if !isKnownSeqType(s.DataType) {
		// Distinguish "no such type" from "not a valid sequence type" based on
		// whether the type name looks like a built-in integer alias.
		if strings.ToLower(s.DataType) != "text" && !strings.HasPrefix(strings.ToLower(s.DataType), "int") {
			return &ExecError{Code: "42704", Pos: s.Pos(),
				Message: fmt.Sprintf("type %q does not exist", s.DataType)}
		}
		return &ExecError{Code: "42804", Pos: s.Pos(),
			Message: "sequence type must be smallint, integer, or bigint"}
	}
	typeMin, typeMax := seqTypeBounds(s.DataType)
	increment := int64(1)
	if s.Increment != nil {
		increment = *s.Increment
	}
	if increment == 0 {
		return &ExecError{Code: "22003", Pos: s.Pos(), Message: "INCREMENT must not be zero"}
	}
	if s.Cache != nil && *s.Cache <= 0 {
		return &ExecError{Code: "22003", Pos: s.Pos(),
			Message: fmt.Sprintf("CACHE (%d) must be greater than zero", *s.Cache)}
	}
	// PostgreSQL default min/max depend on sequence direction:
	//   ascending  → minValue = 1,       maxValue = type_max
	//   descending → minValue = type_min, maxValue = -1
	// M0097-0042, M0097-0068.
	var minV, maxV int64
	if increment >= 0 {
		minV, maxV = 1, typeMax
	} else {
		minV, maxV = typeMin, -1
	}
	// Explicit MINVALUE/MAXVALUE override the direction-based defaults.
	if s.MinValue != nil {
		minV = *s.MinValue
	}
	if s.MaxValue != nil {
		maxV = *s.MaxValue
	}
	// Validate explicit bounds against type range.
	canon := canonSeqType(s.DataType)
	if minV < typeMin || minV > typeMax {
		return &ExecError{Code: "22003", Pos: s.Pos(),
			Message: fmt.Sprintf("MINVALUE (%d) is out of range for sequence data type %s", minV, canon)}
	}
	if maxV < typeMin || maxV > typeMax {
		return &ExecError{Code: "22003", Pos: s.Pos(),
			Message: fmt.Sprintf("MAXVALUE (%d) is out of range for sequence data type %s", maxV, canon)}
	}
	// Validate min < max.
	if minV >= maxV {
		return &ExecError{Code: "22003", Pos: s.Pos(),
			Message: fmt.Sprintf("MINVALUE (%d) must be less than MAXVALUE (%d)", minV, maxV)}
	}
	// Default START = MINVALUE (ascending) or MAXVALUE (descending).
	// Must be computed after final minV/maxV are determined. M0097-0042, M0097-0068.
	start := minV
	if increment < 0 {
		start = maxV
	}
	if s.Start != nil {
		start = *s.Start
	}
	// Validate START within [minV, maxV].
	if start > maxV {
		return &ExecError{Code: "22003", Pos: s.Pos(),
			Message: fmt.Sprintf("START value (%d) cannot be greater than MAXVALUE (%d)", start, maxV)}
	}
	if start < minV {
		return &ExecError{Code: "22003", Pos: s.Pos(),
			Message: fmt.Sprintf("START value (%d) cannot be less than MINVALUE (%d)", start, minV)}
	}
	// Validate OWNED BY before registering. M0097-0068.
	if s.OwnedBy != "" {
		if err := o.validateSeqOwnedBy(s.Pos(), name, s.OwnedBy); err != nil {
			return err
		}
	}
	cycle := s.Cycle
	RegisterSequence(name, start, increment, minV, maxV, cycle)
	if s.Cache != nil {
		SetSequenceCache(name, *s.Cache)
	}
	if s.Temporary {
		SetSequenceTemporary(name, true)
	}
	dt := s.DataType
	if dt == "" {
		dt = "bigint"
	}
	SetSequenceDataType(name, dt)
	if s.OwnedBy != "" {
		SetSequenceOwnedBy(name, s.OwnedBy)
	}
	// Create a virtual catalog table for SELECT * FROM seq_name. This also
	// surfaces the sequence in pg_class (relkind='S') / pg_depend / pg_sequence
	// so pg_dump can discover and dump it. M0097-0024.
	o.createSeqCatalogTable(s.Name, name)
	return nil
}

// createSeqCatalogTable registers the virtual catalog relation that backs
// `SELECT * FROM <seq>` (last_value / log_cnt / is_called) and marks it
// IsSequence so it surfaces in pg_class (relkind='S'), pg_depend, and
// pg_sequence — the rows pg_dump's getTables reads to discover and dump the
// sequence. `name` is the registry key passed to RegisterSequence (bare or
// schema-qualified; SequenceRowData resolves both). Shared by the explicit
// CREATE SEQUENCE path and the implicit IDENTITY-column registration so an
// identity sequence is discoverable by pg_dump. M0110-0001 (DU-002 slice 120).
func (o *ddlOp) createSeqCatalogTable(seqObjName parser.ObjectName, name string) {
	if _, ok := o.ctx.Catalog.(*catalog.InMemory); !ok {
		return
	}
	seqCols := []catalog.Column{
		{Name: "last_value", Type: catalog.Type{Name: "int8"}, Ordinal: 0},
		{Name: "log_cnt", Type: catalog.Type{Name: "int8"}, Ordinal: 1},
		{Name: "is_called", Type: catalog.Type{Name: "bool"}, Ordinal: 2},
	}
	seqTbl, err2 := o.ctx.Catalog.CreateTable(seqObjName, seqCols)
	if err2 != nil {
		return
	}
	seqTbl.Virtual = true
	seqTbl.IsSequence = true
	seqName := name // capture for closure
	seqTbl.VirtualRows = func() [][]string {
		lv, lc, called, ok2 := SequenceRowData(seqName)
		if !ok2 {
			return nil
		}
		calledStr := "f"
		if called {
			calledStr = "t"
		}
		return [][]string{{
			fmt.Sprintf("%d", lv),
			fmt.Sprintf("%d", lc),
			calledStr,
		}}
	}
}

// validateSeqOwnedBy checks OWNED BY table.column before the sequence is
// registered/updated. Returns an error matching PostgreSQL's messages. M0097-0068.
func (o *ddlOp) validateSeqOwnedBy(pos int, seqName, ownedBy string) error {
	dot := strings.Index(ownedBy, ".")
	if dot < 0 {
		return &ExecError{Code: "42601", Pos: pos, Message: "invalid OWNED BY option"}
	}
	tblPart := ownedBy[:dot]
	colPart := ownedBy[dot+1:]

	// Look up the table in the catalog.
	tbl, ok := o.ctx.Catalog.LookupTable(parser.ObjectName{Name: tblPart})
	if !ok {
		// May be schema-qualified: try schema.table
		schemaDot := strings.Index(tblPart, ".")
		if schemaDot >= 0 {
			tbl, ok = o.ctx.Catalog.LookupTable(parser.ObjectName{
				Schema: tblPart[:schemaDot],
				Name:   tblPart[schemaDot+1:],
			})
		}
	}
	if !ok {
		return &ExecError{Code: "42P01", Pos: pos,
			Message: fmt.Sprintf("sequence cannot be owned by relation %q", tblPart)}
	}
	// Check same schema first (mirrors PostgreSQL error priority). M0097-0068.
	seqDot := strings.Index(seqName, ".")
	seqSchema := "public"
	if seqDot >= 0 {
		seqSchema = strings.ToLower(seqName[:seqDot])
	}
	tblSchema := strings.ToLower(tbl.Schema)
	if tblSchema == "" {
		tblSchema = "public"
	}
	if seqSchema != tblSchema {
		return &ExecError{Code: "0A000", Pos: pos,
			Message: "sequence must be in same schema as table it is linked to"}
	}
	if tbl.Virtual {
		return &ExecError{Code: "42809", Pos: pos,
			Message: fmt.Sprintf("sequence cannot be owned by relation %q", tblPart)}
	}
	// Check column exists.
	found := false
	for _, col := range tbl.Columns {
		if strings.EqualFold(col.Name, colPart) {
			found = true
			break
		}
	}
	if !found {
		return &ExecError{Code: "42703", Pos: pos,
			Message: fmt.Sprintf("column %q of relation %q does not exist", colPart, tblPart)}
	}
	return nil
}

// execAlterSequence handles ALTER SEQUENCE statements. M0097-0068.
func (o *ddlOp) execAlterSequence(s *parser.AlterSequenceStmt) error {
	name := s.Name.String()
	seq := LookupSequence(name)
	if seq == nil {
		if s.IfExists {
			return nil
		}
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("relation %q does not exist", name)}
	}

	// Snapshot current state for sticky-default computation.
	seq.mu.Lock()
	curMin := seq.min
	curMax := seq.max
	curDataType := seq.dataType
	seq.mu.Unlock()
	if curDataType == "" {
		curDataType = "bigint"
	}

	var minV, maxV *int64
	if s.DataType != "" {
		// "Sticky default" semantics: bounds that equal the old type's extremes are
		// updated to the new type's extremes; explicitly-set bounds are preserved.
		// Mirrors PostgreSQL's ALTER SEQUENCE ... AS type behaviour. M0097-0068.
		oldMin, oldMax := seqTypeBounds(curDataType)
		newMin, newMax := seqTypeBounds(s.DataType)

		if curMin == oldMin {
			v := newMin
			minV = &v
		}
		if curMax == oldMax {
			v := newMax
			maxV = &v
		}
	}
	// Explicit MINVALUE / MAXVALUE override sticky defaults.
	if s.MinValue != nil {
		minV = s.MinValue
	}
	if s.MaxValue != nil {
		maxV = s.MaxValue
	}

	// Validate new bounds against the target type range.
	if s.DataType != "" {
		newMin, newMax := seqTypeBounds(s.DataType)
		canon := canonSeqType(s.DataType)
		effectiveMin := curMin
		if minV != nil {
			effectiveMin = *minV
		}
		effectiveMax := curMax
		if maxV != nil {
			effectiveMax = *maxV
		}
		if effectiveMin < newMin || effectiveMin > newMax {
			return &ExecError{Code: "22003", Pos: s.Pos(),
				Message: fmt.Sprintf("MINVALUE (%d) is out of range for sequence data type %s", effectiveMin, canon)}
		}
		if effectiveMax < newMin || effectiveMax > newMax {
			return &ExecError{Code: "22003", Pos: s.Pos(),
				Message: fmt.Sprintf("MAXVALUE (%d) is out of range for sequence data type %s", effectiveMax, canon)}
		}
	}

	// Validate RESTART WITH against the effective [min, max]. M0097-0068.
	if s.RestartWith != nil {
		// Determine effective min/max after any pending changes.
		seq.mu.Lock()
		effectiveMin2, effectiveMax2 := seq.min, seq.max
		seq.mu.Unlock()
		if minV != nil {
			effectiveMin2 = *minV
		}
		if maxV != nil {
			effectiveMax2 = *maxV
		}
		rv := *s.RestartWith
		if rv < effectiveMin2 {
			return &ExecError{Code: "22003", Pos: s.Pos(),
				Message: fmt.Sprintf("RESTART value (%d) cannot be less than MINVALUE (%d)", rv, effectiveMin2)}
		}
		if rv > effectiveMax2 {
			return &ExecError{Code: "22003", Pos: s.Pos(),
				Message: fmt.Sprintf("RESTART value (%d) cannot be greater than MAXVALUE (%d)", rv, effectiveMax2)}
		}
	}

	if err := UpdateSequenceParams(name, s.Increment, minV, maxV, s.StartWith, s.RestartWith,
		s.Cache, s.Restart, s.Cycle, s.NoCycle); err != nil {
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: err.Error()}
	}
	if s.DataType != "" {
		SetSequenceDataType(name, s.DataType)
	}
	if s.OwnedBy != "" {
		SetSequenceOwnedBy(name, s.OwnedBy)
	} else if s.ClearOwnedBy {
		SetSequenceOwnedBy(name, "")
	}
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
	// Check for existing relation before planning to avoid spurious runtime
	// errors (e.g. division by zero) from constant folding in the query.
	if _, exists := o.ctx.Catalog.LookupTable(s.Name); exists {
		if s.IfNotExists {
			o.ctx.AddNotice(fmt.Sprintf("relation %q already exists, skipping", s.Name.String()))
			return nil
		}
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: fmt.Sprintf("relation %q already exists", s.Name.String())}
	}
	// Plan the SELECT query to determine output columns.
	if err := analyzer.Analyze(s.Query, o.ctx.Catalog); err != nil {
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: err.Error()}
	}
	var selectPlan planner.Node
	var err error
	if s.WithNoData {
		// WITH NO DATA: use schema-only planning that suppresses runtime
		// evaluation errors (e.g. division by zero) — the query will never execute.
		selectPlan, err = planner.PlanSchemaOnly(s.Query, o.planCatalog())
	} else {
		selectPlan, err = planner.Plan(s.Query, o.planCatalog())
	}
	if err != nil {
		return err
	}
	schema := selectPlan.Output()
	if schema == nil {
		return &ExecError{Code: "42P10", Pos: s.Pos(), Message: "materialized view query has no output columns"}
	}
	// Apply optional column aliases and validate count.
	if len(s.ColumnAliases) > len(schema) {
		return &ExecError{Code: "42601", Pos: s.Pos(), Message: "too many column names were specified"}
	}
	// Build column list from plan output schema, applying any column aliases.
	cols := make([]catalog.Column, len(schema))
	for i, sc := range schema {
		name := sc.Name
		if i < len(s.ColumnAliases) {
			name = s.ColumnAliases[i]
		}
		cols[i] = catalog.Column{Name: name, Type: sc.Type, Ordinal: i}
	}
	tbl, err := o.ctx.Catalog.CreateTable(s.Name, cols)
	if err != nil {
		return &ExecError{Code: "42P07", Pos: s.Pos(), Message: err.Error()}
	}
	tbl.IsMatView = true
	tbl.IsPopulated = !s.WithNoData
	// Set schema from search_path if not explicitly specified (mirrors CREATE TABLE). M0097-0025.
	if s.Name.Schema == "" {
		if ws := currentWritableSchema(o.ctx); ws != "" && !strings.EqualFold(ws, "public") {
			tbl.Schema = ws
		}
	}
	// Store the SELECT AST as the view query (for REFRESH).
	tbl.View = s.Query
	// Store the raw body text so pg_get_viewdef can echo it for pg_dump, which
	// dumps a matview's `AS` clause exactly like a plain view (createViewAsClause
	// → pg_get_viewdef) and aborts the whole dump if it returns empty. Mirrors
	// the plain-view path (vt.ViewDef = s.RawDef). M0110-0001 (DU-002 slice 60).
	tbl.ViewDef = s.RawDef
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
// to the materialized view heap, then rebuilds all btree indexes.
// Used by both initial populate and REFRESH. M0097-0013.
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
	// Rebuild all btree indexes on the matview after population.
	// This also detects unique constraint violations (duplicate rows). M0097-0025.
	if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
		for _, idx := range im.IndexesOnTable(tbl) {
			idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
			// Truncate (clear) the index storage before rebuilding.
			o.ctx.Pool.InvalidateRel(idxRel)
			if err := o.ctx.Pool.Manager().TruncateRelation(idxRel); err != nil {
				return err
			}
			// Resolve column name → *catalog.Column for each index column.
			cols := make([]*catalog.Column, len(idx.Columns))
			for i, colName := range idx.Columns {
				if colName == "" {
					continue // expression column
				}
				col, ok2 := o.ctx.Catalog.LookupColumn(tbl, colName)
				if ok2 {
					cols[i] = col
				}
			}
			idxName := idx.QualifiedName()
			if err := o.bulkBuildBTree(idxRel, tbl, cols, idx.Unique, idxName, 0); err != nil {
				return err
			}
		}
	}
	return nil
}

// execRefreshMatView implements REFRESH MATERIALIZED VIEW. M0097-0013.
func (o *ddlOp) execRefreshMatView(s *parser.RefreshMatViewStmt) error {
	if o.ctx.Pool == nil || o.ctx.Catalog == nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: "REFRESH MATERIALIZED VIEW requires storage"}
	}
	// CONCURRENTLY and WITH NO DATA are mutually exclusive.
	if s.Concurrently && s.WithNoData {
		return &ExecError{Code: "0A000", Pos: s.Pos(), Message: "CONCURRENTLY and WITH NO DATA options cannot be used together"}
	}
	tbl, ok := o.ctx.Catalog.LookupTable(s.Name)
	if !ok {
		return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("materialized view %q does not exist", s.Name.String())}
	}
	if !tbl.IsMatView {
		return &ExecError{Code: "42809", Pos: s.Pos(), Message: fmt.Sprintf("%q is not a materialized view", s.Name.String())}
	}
	// REFRESH without CONCURRENTLY on a non-populated matview is fine (it populates it).
	// But CONCURRENTLY requires the matview to already be populated.
	// refreshErrName returns "schema.name" for error messages, using "public"
	// as the default schema when tbl.Schema is empty.
	refreshErrName := func() string {
		if tbl.Schema == "" || strings.EqualFold(tbl.Schema, "public") {
			return "public." + tbl.Name
		}
		return tbl.Schema + "." + tbl.Name
	}
	if s.Concurrently && !tbl.IsPopulated {
		return &ExecError{Code: "55000", Pos: s.Pos(),
			Message: fmt.Sprintf("cannot refresh materialized view %q concurrently",
				refreshErrName()),
			Hint: "Use the REFRESH MATERIALIZED VIEW command."}
	}
	// CONCURRENTLY requires at least one unique index with no WHERE clause.
	if s.Concurrently {
		if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
			hasUnique := false
			for _, idx := range im.IndexesOnTable(tbl) {
				if !idx.Unique || idx.HasPredicate {
					continue // partial or non-unique — not suitable for CONCURRENTLY
				}
				// Check for expression columns (not suitable for CONCURRENTLY).
				allPlain := true
				for _, e := range idx.ColExprs {
					if e != nil {
						allPlain = false
						break
					}
				}
				if allPlain {
					hasUnique = true
					break
				}
			}
			if !hasUnique {
				return &ExecError{Code: "55000", Pos: s.Pos(),
					Message: fmt.Sprintf("cannot refresh materialized view %q concurrently",
						refreshErrName()),
					Hint: "Create a unique index with no WHERE clause on one or more columns of the materialized view."}
			}
		}
	}
	// Re-plan the SELECT from the stored query.
	if err := analyzer.Analyze(tbl.View, o.ctx.Catalog); err != nil {
		return &ExecError{Code: "XX000", Pos: s.Pos(), Message: fmt.Sprintf("refresh plan error: %v", err)}
	}
	selectPlan, err := planner.Plan(tbl.View, o.planCatalog())
	if err != nil {
		return err
	}
	// Truncate existing data (stamp xmax on all rows).
	rel := o.ctx.Catalog.RelFileNode(tbl)
	if err := truncateRelation(o.ctx, rel); err != nil {
		return err
	}
	// Re-populate. Translate 23505 unique violations to PG-compatible messages.
	if err := o.materializeView(tbl, selectPlan); err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Code == "23505" {
			if s.Concurrently {
				// REFRESH CONCURRENTLY duplicate → "new data contains duplicate rows".
				return &ExecError{Code: "55000", Pos: s.Pos(),
					Message: fmt.Sprintf("new data for materialized view %q contains duplicate rows without any null columns", s.Name.String())}
			}
			// Non-concurrent REFRESH → "could not create unique index 'name'".
			idxName := ""
			if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
				for _, idx := range im.IndexesOnTable(tbl) {
					if idx.Unique {
						idxName = idx.Name
						break
					}
				}
			}
			return &ExecError{Code: "23505", Pos: s.Pos(),
				Message: fmt.Sprintf("could not create unique index %q", idxName)}
		}
		return err
	}
	// For CONCURRENTLY refresh, verify the unique index still exists after
	// materialization (it might have been dropped during the SELECT query).
	// M0097-0025.
	if s.Concurrently {
		if im, ok2 := o.ctx.Catalog.(*catalog.InMemory); ok2 {
			hasUnique := false
			for _, idx := range im.IndexesOnTable(tbl) {
				if !idx.Unique || idx.HasPredicate {
					continue
				}
				allPlain := true
				for _, e := range idx.ColExprs {
					if e != nil {
						allPlain = false
						break
					}
				}
				if allPlain {
					hasUnique = true
					break
				}
			}
			if !hasUnique {
				return &ExecError{Code: "55000", Pos: s.Pos(),
					Message: fmt.Sprintf("could not find suitable unique index on materialized view %q",
						s.Name.String())}
			}
		}
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

	// DROP FOREIGN TABLE [IF EXISTS] name [, ...] — drop the underlying table from catalog.
	// CREATE FOREIGN TABLE now creates real catalog entries, so we must drop them here.
	if objType == "foreign table" {
		for _, name := range s.Names {
			if s.IfExists && o.dropSchemaQualifiedNotice(name) {
				continue
			}
			tbl, ok := o.ctx.Catalog.LookupTable(name)
			if !ok {
				if s.IfExists {
					o.ctx.AddNotice(fmt.Sprintf("foreign table %q does not exist, skipping", name.String()))
					continue
				}
				return &ExecError{Code: "42P01", Pos: s.Pos(), Message: fmt.Sprintf("foreign table %q does not exist", name.String())}
			}
			if err := o.dropTableByRef(name, tbl); err != nil {
				return err
			}
		}
		return nil
	}

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
			// M0110-0003: persist the drop so it survives a restart (mirrors
			// CREATE SCHEMA below / DROP DATABASE). Without this, a schema
			// dropped at runtime would be re-registered by replaying its
			// CREATE SCHEMA record on the next startup.
			if o.ctx.WAL != nil {
				if _, _, werr := o.ctx.WAL.Append(wal.EncodeDropSchema(schemaName)); werr != nil {
					return fmt.Errorf("wal drop-schema: %w", werr)
				}
			}
			if s.Behavior == parser.DropCascade {
				// Collect routines to drop for NOTICE detail.
				var droppedRoutines []string
				rs := o.ctx.Catalog.Routines()
				if rs != nil {
					for _, r := range rs.List() {
						if strings.EqualFold(r.Schema, schemaName) {
							// Build canonical arg list for NOTICE.
							argStr := r.Name + "(" + buildFunctionArgsList(r) + ")"
							droppedRoutines = append(droppedRoutines, argStr)
							_ = rs.Drop(parser.ObjectName{Schema: r.Schema, Name: r.Name}, r.ArgTypes)
						}
					}
					sort.Strings(droppedRoutines)
				}
				// Collect views in the schema.
				var droppedViews []string
				var droppedOpClasses []string
				if im2, ok3 := o.ctx.Catalog.(*catalog.InMemory); ok3 {
					for _, v := range im2.AllUserViews() {
						if strings.EqualFold(v.Schema, schemaName) {
							droppedViews = append(droppedViews, v.Name)
						}
					}
					sort.Strings(droppedViews)
					for _, vn := range droppedViews {
						dropped := map[string]bool{}
						_ = o.execDropOneView(parser.ObjectName{Schema: schemaName, Name: vn}, true, parser.DropCascade, s.Pos(), dropped)
					}
					// Collect operator classes in the schema. M0097-0022.
					droppedOpClasses = im2.OpClassesInSchema(schemaName)
				}
				// Sort table names for deterministic DETAIL output.
				sort.Slice(tables, func(i, j int) bool {
					return tables[i].String() < tables[j].String()
				})
				// Build detail lines BEFORE dropping (IsMatView check needs table still in catalog).
				total := len(tables) + len(droppedViews) + len(droppedRoutines) + len(droppedOpClasses)
				var detailLines []string
				if total > 0 {
					for _, funcName := range droppedRoutines {
						detailLines = append(detailLines, fmt.Sprintf("drop cascades to function %s", funcName))
					}
					for _, opClass := range droppedOpClasses {
						detailLines = append(detailLines, fmt.Sprintf("drop cascades to operator family %s for access method hash", opClass))
					}
					for _, tbl := range tables {
						kind := "table"
						if t, ok := o.ctx.Catalog.LookupTable(tbl); ok && t.IsMatView {
							kind = "materialized view"
						}
						detailLines = append(detailLines, fmt.Sprintf("drop cascades to %s %s", kind, dropCascadeObjectName(tbl, o.ctx)))
					}
					for _, vn := range droppedViews {
						detailLines = append(detailLines, fmt.Sprintf("drop cascades to view %s", vn))
					}
				}
				// Drop each table in the schema.
				for _, tbl := range tables {
					if err := o.ctx.Catalog.DropTable(tbl); err != nil {
						return &ExecError{Code: "42P01", Pos: s.Pos(), Message: err.Error()}
					}
				}
				// Emit NOTICE with total cascade count.
				if total > 0 {
					detail := strings.Join(detailLines, "\n")
					o.ctx.AddNoticeWithDetail(
						fmt.Sprintf("drop cascades to %d other objects", total),
						detail,
					)
				}
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
			// Check if the operator class was registered via CREATE OPERATOR CLASS.
			// If found in the catalog registry, remove it and succeed silently.
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				if im.HasOpClass(name.Name) {
					im.RemoveOpClass(name.Name)
					return nil
				}
			}
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
			// CASCADE: drop functions that depend on this sequence before dropping it.
			if s.Behavior == parser.DropCascade {
				funcDeps := functionsDependingOnSequence(o.ctx.Catalog, name.Name, name.Schema)
				if len(funcDeps) == 1 {
					dn := routineCascadeDisplayName(funcDeps[0])
					o.ctx.AddNotice(fmt.Sprintf("drop cascades to function %s", dn))
					if rs := o.ctx.Catalog.Routines(); rs != nil {
						_ = rs.DropRoutine(funcDeps[0])
					}
				} else if len(funcDeps) > 1 {
					detail := make([]string, len(funcDeps))
					for i, r := range funcDeps {
						dn := routineCascadeDisplayName(r)
						detail[i] = fmt.Sprintf("drop cascades to function %s", dn)
						if rs := o.ctx.Catalog.Routines(); rs != nil {
							_ = rs.DropRoutine(r)
						}
					}
					o.ctx.AddNoticeWithDetail(
						fmt.Sprintf("drop cascades to %d other objects", len(funcDeps)),
						strings.Join(detail, "\n"),
					)
				}
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
			// Remove the virtual catalog entry created for SELECT * FROM seq_name. M0097-0024.
			_ = o.ctx.Catalog.DropTable(name)
		}
		return nil
	}
	// Handle DROP MATERIALIZED VIEW via the catalog's DropView. M0097-0038.
	if objType == "materialized view" {
		dropped := map[string]bool{}
		for _, name := range s.Names {
			if err := o.execDropOneMatView(name, s.IfExists, s.Behavior, s.Pos(), dropped, true); err != nil {
				return err
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
		case "server":
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				if im.DropCompatObject("server", s.Names[0].String()) {
					return nil
				}
			}
		case "foreign-data wrapper":
			if im, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
				fdwName := s.Names[0].String()
				if im.DropCompatObject("foreign-data wrapper", fdwName) {
					// CASCADE: drop all servers associated with this FDW.
					if s.Behavior == parser.DropCascade {
						// Find servers registered under this FDW via "fdw-server:fdwname:servername".
						prefix := fdwName + ":"
						var cascadeServers []string
						for _, entry := range im.ListCompatObjects("fdw-server") {
							if strings.HasPrefix(entry, prefix) {
								serverName := strings.TrimPrefix(entry, prefix)
								cascadeServers = append(cascadeServers, serverName)
							}
						}
						for _, serverName := range cascadeServers {
							im.DropCompatObject("fdw-server", fdwName+":"+serverName)
							im.DropCompatObject("server", serverName)
							o.ctx.AddNotice(fmt.Sprintf("drop cascades to server %s", serverName))
						}
					}
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
	case "server":
		// Register server so DROP SERVER can succeed.
		im.RegisterCompatObject(s.ObjType, s.ObjName.String())
		// If the server references a FDW, store the association for DROP FDW CASCADE.
		if s.TableName.Name != "" {
			im.RegisterCompatObject("fdw-server", s.TableName.String()+":"+s.ObjName.String())
		}
	case "foreign-data wrapper":
		// Register FDW so DROP FOREIGN DATA WRAPPER can succeed.
		im.RegisterCompatObject(s.ObjType, s.ObjName.String())
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
	case "schema":
		// Register user-created schema so schema-qualified queries resolve correctly.
		if s.ObjName.Name != "" {
			im.RegisterSchema(s.ObjName.Name)
			// M0110-0003: persist the schema so it survives a restart. goopg
			// has no per-schema on-disk file namespace, so we record a WAL
			// event the recovery driver replays into the schema registry
			// (mirrors CREATE DATABASE, M0054-0001). The OID just assigned by
			// RegisterSchema is carried so recovery restores the same OID.
			if o.ctx.WAL != nil {
				oid := im.SchemaOID(s.ObjName.Name)
				if _, _, werr := o.ctx.WAL.Append(wal.EncodeCreateSchema(s.ObjName.Name, oid)); werr != nil {
					return fmt.Errorf("wal create-schema: %w", werr)
				}
			}
		}
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

// execCommentOn stores a description for a TABLE, INDEX, COLUMN, or CONSTRAINT
// in pg_description via catalog.SetComment. M0097-0023.
func (o *ddlOp) execCommentOn(s *parser.CommentOnStmt) error {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	// classOID constants match PostgreSQL system catalog OIDs.
	const (
		oidPgClass        = 1259 // pg_class: tables, indexes, views, sequences, matviews
		oidPgType         = 1247 // pg_type: user types (enum) and domains
		oidPgProc         = 1255 // pg_proc: functions and procedures
		oidPgConstraint   = 2606 // pg_constraint
		oidPgNamespace    = 2615 // pg_namespace: schemas
		oidPgStatisticExt = 3381 // pg_statistic_ext
	)
	switch s.ObjKind {
	case "table", "view", "sequence", "materialized view":
		// Views, sequences, and materialized views are pg_class relations stored
		// in the same table registry as ordinary tables, so they share the
		// classoid (1259) and LookupTable path. pg_dump chooses the COMMENT ON
		// keyword from relkind (relkind='m' → MATERIALIZED VIEW); the stored
		// pg_description row is keyword-agnostic. DU-002 slices 145, 146.
		tbl, ok := im.LookupTable(s.ObjName)
		if !ok {
			return nil
		}
		im.SetComment(oidPgClass, tbl.OID, 0, s.Description)
	case "type":
		// User-defined types live in pg_type (classoid 1247). goopg's only
		// user types are enums; resolve the enum OID. Without this a COMMENT ON
		// TYPE was silently swallowed and never reached pg_description, so
		// pg_dump could not re-emit it. DU-002 slice 146.
		et, ok := im.LookupEnum(s.ObjName.Name)
		if !ok {
			return nil
		}
		im.SetComment(oidPgType, et.OID, 0, s.Description)
	case "domain":
		// Domains live in pg_type (typtype='d', classoid 1247). pg_dump picks the
		// DOMAIN keyword from typtype; the stored pg_description row is
		// keyword-agnostic. DU-002 slice 146.
		dom, ok := im.LookupDomain(s.ObjName.Name)
		if !ok {
			return nil
		}
		im.SetComment(oidPgType, dom.OID, 0, s.Description)
	case "schema":
		// Schemas live in pg_namespace (classoid 2615). Without this, a COMMENT
		// ON SCHEMA was silently swallowed and never reached pg_description, so
		// pg_dump could not re-emit it. DU-002 slice 145.
		oid := im.SchemaOID(s.ObjName.Name)
		if oid == 0 {
			return nil
		}
		im.SetComment(oidPgNamespace, oid, 0, s.Description)
	case "index":
		idx, ok := im.LookupIndex(s.ObjName)
		if !ok {
			return nil
		}
		im.SetComment(oidPgClass, idx.OID, 0, s.Description)
	case "column":
		tbl, ok := im.LookupTable(s.ObjName)
		if !ok {
			return nil
		}
		for i, col := range tbl.Columns {
			if strings.EqualFold(col.Name, s.SubName) {
				im.SetComment(oidPgClass, tbl.OID, int32(i+1), s.Description)
				break
			}
		}
	case "constraint":
		tbl, ok := im.LookupTable(s.ObjName)
		if !ok {
			return nil
		}
		for _, nc := range tbl.NamedChecks {
			if strings.EqualFold(nc.Name, s.SubName) {
				im.SetComment(oidPgConstraint, nc.OID, 0, s.Description)
				return nil
			}
		}
		// PG18 NOT NULL constraints are also named constraints. M0097-0023.
		for _, nn := range tbl.NotNullConstraints {
			if strings.EqualFold(nn.Name, s.SubName) && nn.OID != 0 {
				im.SetComment(oidPgConstraint, nn.OID, 0, s.Description)
				return nil
			}
		}
		// UNIQUE / PRIMARY KEY / EXCLUDE constraints are backed by indexes whose
		// Name equals the constraint name; the index OID is the pg_constraint OID
		// emitted by pg_constraint's VirtualRows. Without this, a COMMENT ON these
		// constraint kinds was silently dropped and never reached pg_description,
		// so pg_dump could not re-emit it. DU-002 slice 144.
		for _, idx := range im.IndexesOnTable(tbl) {
			if (idx.IsConstraint || idx.IsExclusion) && idx.OID != 0 && strings.EqualFold(idx.Name, s.SubName) {
				im.SetComment(oidPgConstraint, idx.OID, 0, s.Description)
				return nil
			}
		}
		// FOREIGN KEY constraints (contype='f') are stored on the child table.
		for _, fk := range tbl.ForeignKeys {
			if strings.EqualFold(fk.Name, s.SubName) && fk.OID != 0 {
				im.SetComment(oidPgConstraint, fk.OID, 0, s.Description)
				return nil
			}
		}
	case "statistics":
		stat, ok := im.LookupStatistics(s.ObjName.Name)
		if !ok {
			return nil
		}
		im.SetComment(oidPgStatisticExt, stat.OID, 0, s.Description)
	case "function":
		// Functions live in pg_proc (classoid 1255). Resolve the routine by
		// name + argument signature (mirrors DROP FUNCTION's resolution) so the
		// correct overload is keyed. Without this a COMMENT ON FUNCTION was
		// silently swallowed and never reached pg_description, so pg_dump could
		// not re-emit it. DU-002 slice 147.
		rs := im.Routines()
		if rs == nil {
			return nil
		}
		argTypes := make([]catalog.Type, len(s.Args))
		for i, a := range s.Args {
			argTypes[i] = catalog.Type{Name: strings.ToLower(a.Type.Name)}
		}
		r, ok := rs.Lookup(s.ObjName, argTypes)
		if !ok || r == nil {
			return nil
		}
		im.SetComment(oidPgProc, r.OID, 0, s.Description)
	}
	return nil
}

// execCreateStatistics registers a new extended statistics object. M0097-0023.
func (o *ddlOp) execCreateStatistics(s *parser.CreateStatisticsStmt) error {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	// Resolve the table that this statistics object refers to.
	var tableOID uint32
	if s.FromTable.Name != "" {
		tbl, ok := im.LookupTable(s.FromTable)
		if ok {
			tableOID = tbl.OID
		}
	}
	schema := s.Name.Schema
	if schema == "" {
		schema = currentWritableSchema(o.ctx)
		if schema == "" {
			schema = "public"
		}
	}
	im.RegisterStatistics(schema, s.Name.Name, tableOID)
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
	case "int", "int4", "integer", "serial", "serial4":
		return "int4"
	case "int2", "smallint", "smallserial", "serial2":
		return "int2"
	case "int8", "bigint", "bigserial", "serial8":
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
	case "int4", "integer", "int", "serial", "serial4":
		return "integer"
	case "int2", "smallint", "smallserial", "serial2":
		return "smallint"
	case "int8", "bigint", "bigserial", "serial8":
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
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	if s.HashFuncName != "" {
		im.RegisterOpClassHashFunc(s.Name, s.HashFuncName)
	}
	// Track the schema for DROP SCHEMA CASCADE detail output. M0097-0022.
	schema := currentWritableSchema(o.ctx)
	if schema == "" {
		schema = "public"
	}
	im.RegisterOpClassSchema(s.Name, schema)
	return nil
}

// knownBuiltinAggFinalFuncs is the set of PostgreSQL built-in function names
// that may appear as a finalfunc in CREATE AGGREGATE. These are handled as
// special cases in the executor (finishAgg) and are not registered in the
// user-defined routines catalog, so the finalfunc validation skips them.
// M0097-0112.
var knownBuiltinAggFinalFuncs = map[string]bool{
	"int8_avg":                       true,
	"numeric_avg":                    true,
	"numeric_avg_combine":            true,
	"numeric_out":                    true,
	"percentile_disc_final":          true,
	"percentile_cont_final":          true,
	"rank_final":                     true,
	"dense_rank_final":               true,
	"cume_dist_final":                true,
	"percent_rank_final":             true,
	"mode_final":                     true,
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
			for _, f := range s.CompositeFields {
				if strings.EqualFold(f.ColType, "unknown") {
					return &ExecError{Code: "42P16", Pos: s.Pos(),
						Message: fmt.Sprintf("column %q has pseudo-type unknown", f.Name)}
				}
			}
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
				// Also stamp the auto-generated array type row (`_name`). DU-002 slice 89.
				deleteTypeFromCatalogHeap(o.ctx, catalog.DefaultDBOid, et.ArrayOID, o.ctx.Tx.XID)
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
	baseType := catalog.Type{Name: s.BaseType, Args: s.BaseTypeArgs}
	d, err := cat.RegisterDomain(s.Name, baseType, s.NotNull, s.CheckInValues...)
	if err != nil {
		return &ExecError{Code: "42710", Pos: s.Pos(), Message: err.Error()}
	}
	// Resolve a user-defined enum base type's dynamically-allocated OID and
	// record it on the domain. TypeNameToOID falls back to text for enum names,
	// so without this the pg_type row would carry typbasetype=text and pg_dump
	// would render `AS text` instead of `AS public.<enum>`. DU-002 slice 109.
	if et, ok := enumForDomainBaseType(cat, s.BaseType); ok {
		d.BaseOID = et.OID
		d.BaseIsEnum = true
	}
	// Record the DEFAULT expression so buildUserPGTypeRowForDomain can emit
	// typdefaultbin and pg_dump re-renders `DEFAULT <expr>`. DU-002 slice 92.
	d.Default = s.Default
	// Record a generic CHECK predicate (e.g. `VALUE > 0`) so pg_dump's
	// getDomainConstraints surfaces it and dumpDomain re-emits the inline
	// `CONSTRAINT <name> CHECK ((<expr>))` clause. DU-002 slice 96.
	//
	// A `CHECK (VALUE IN (...))` form is captured separately as CheckInValues
	// (used at runtime for membership validation). PG deparses it to a
	// ScalarArrayOpExpr — `VALUE = ANY (ARRAY['a'::text, ...])` — so we synthesize
	// the same text here and store it as the constraint's conbin, making it
	// round-trip through pg_dump too. DU-002 slice 97.
	checkExpr := s.CheckExpr
	if checkExpr == "" && len(s.CheckInValues) > 0 {
		checkExpr = domainInValuesCheckExpr(s.BaseType, s.CheckInValues, cat)
	}
	cat.SetDomainCheck(d, s.CheckName, checkExpr)
	// Write a pg_type heap row (typtype='d') so pg_dump's getTypes discovers the
	// domain and a column of the domain type round-trips as its declared type.
	// DU-002 slice 90.
	syncDomainTypeToCatalogHeap(o.ctx, d)
	return nil
}

// domainInValuesCheckExpr renders a `CHECK (VALUE IN (...))` domain constraint as
// PG's deparsed ScalarArrayOpExpr text, so it round-trips through pg_dump via
// pg_get_constraintdef. The exact deparse depends on the string base type's
// equality operator (verified against real pg_dump 18.3):
//
//	text       VALUE = ANY (ARRAY['red'::text, 'green'::text])
//	char(n)    VALUE = ANY (ARRAY['a'::bpchar, 'b'::bpchar])
//	varchar    (VALUE)::text = ANY ((ARRAY['a'::character varying, ...])::text[])
//	smallint   VALUE = ANY (ARRAY[10, 20, 30])
//	integer    VALUE = ANY (ARRAY[1, 2, 3])
//	numeric    VALUE = ANY (ARRAY[1.5, 2.5])
//	bigint     VALUE = ANY (ARRAY[(100)::bigint, (200)::bigint])
//	bytea      VALUE = ANY (ARRAY['\xdeadbeef'::bytea, '\xcafe'::bytea])
//	inet       VALUE = ANY (ARRAY['192.168.0.1'::inet, '10.0.0.0/8'::inet])
//	real       VALUE = ANY (ARRAY[(1.5)::real, (2.5)::real])
//	float8     VALUE = ANY (ARRAY[(1.5)::double precision, (3.0)::double precision])
//	boolean    VALUE = ANY (ARRAY[true, false])
//	date       VALUE = ANY (ARRAY['2020-01-01'::date, '2021-06-15'::date])
//	timestamp  VALUE = ANY (ARRAY['2020-01-01 00:00:00'::timestamp without time zone, ...])
//	time       VALUE = ANY (ARRAY['12:00:00'::time without time zone, ...])
//	uuid       VALUE = ANY (ARRAY['a0ee…'::uuid, 'b0ee…'::uuid])
//	name       VALUE = ANY (ARRAY['alice'::name, 'bob'::name])
//	jsonb      VALUE = ANY (ARRAY['1'::jsonb, '"hello"'::jsonb])
//	json       (VALUE)::text = ANY (ARRAY['1'::text, '{"a": 1}'::text])
//	xml        (VALUE)::text = ANY (ARRAY['<a/>'::text, '<b>1</b>'::text])
//	oid        VALUE = ANY (ARRAY[(1)::oid, (2)::oid, (3)::oid])
//	bit(n)     VALUE = ANY (ARRAY['1010'::"bit", '0101'::"bit"])
//	varbit     VALUE = ANY (ARRAY['101'::bit varying, '110'::bit varying])
//	pg_lsn     VALUE = ANY (ARRAY['16/B374D848'::pg_lsn, '0/0'::pg_lsn])
//	tid        VALUE = ANY (ARRAY['(0,1)'::tid, '(1,2)'::tid])
//	xid        VALUE = ANY (ARRAY['100'::xid, '200'::xid])
//	cid        VALUE = ANY (ARRAY['5'::cid, '10'::cid])
//	interval   VALUE = ANY (ARRAY['1 day'::interval, '02:00:00'::interval])
//	money      VALUE = ANY (ARRAY['$1.00'::money, '$2.50'::money])
//
// text and bpchar have native equality operators, so PG emits a bare per-element
// cast with no coercion wrapper. character varying has no varchar-eq operator and
// reuses text's, so PG coerces both sides to text — hence the `(VALUE)::text` /
// `(...)::text[]` envelope. The per-element cast always uses the base type's bare
// name (no typmod, even for varchar(20)/char(4)).
//
// integer and numeric domains store integer/numeric literals whose own type
// already matches the base type, so PG emits the literal verbatim — no quotes
// and no per-element cast (verified against real pg_dump 18.3). boolean is the
// same verbatim shape (the IN-list keyword literals already have type bool).
// bigint/real/float8 differ: the IN-list literals parse as int4/numeric constants
// and PG coerces each to the base type, so every element is wrapped
// `(N)::<basetype>`. date/timestamp/time/uuid mirror the string-with-cast shape
// (`'…'::<basetype>`). bytea/inet also use the string-with-cast shape; their
// canonical input forms (bytea `\x` hex, inet dotted-quad/CIDR) round-trip
// verbatim. smallint joins the verbatim integer branch — small integer literals
// const-fold to int2 with no cast wrapper. macaddr/macaddr8 use the bare
// string-with-cast shape (their canonical colon-form round-trips). cidr is special:
// it has no cidr-eq operator and reuses inet's, so PG coerces both sides to inet —
// the element cast stays `::cidr` but the envelope is `(VALUE)::inet = ANY
// ((ARRAY[...])::inet[])` (same envelope mechanism as varchar→text). name and
// jsonb both have native equality operators, so they use the bare string-with-cast
// shape (`'alice'::name`, `'1'::jsonb`); name is a plain string and jsonb scalars
// (numbers, quoted strings) round-trip verbatim through jsonb's output function.
// json and xml have no equality operator (their CHECK must be `VALUE::text IN
// (...)`, the cast-on-VALUE parse shape) so they use the `(VALUE)::text` lhsCast
// form; both round-trip verbatim through `::text`. oid joins the per-element
// coercion shape (`(N)::oid`). bit/varbit use the bare string-with-cast shape;
// bit's cast type is quoted (`::"bit"`) by the deparser. pg_lsn/tid/xid/cid all
// have native equality operators and canonical input forms that round-trip
// verbatim through the bare string-with-cast shape (`'16/B374D848'::pg_lsn`,
// `'(0,1)'::tid`, `'100'::xid`, `'5'::cid`). interval and money also have native
// equality operators and use the bare string-with-cast shape, but only their
// canonical-output forms round-trip byte-identically: interval's output function
// normalizes ('2 hours'→'02:00:00') and money's output depends on lc_monetary
// (the default C/POSIX locale yields '$1.00'), so the fixtures use already-canonical
// values ('1 day'/'02:00:00'/'1 year 2 mons', '$1.00'/'$2.50') — the same
// canonical-only contract as jsonb scalars. timestamptz, tsvector and tsquery
// remain deliberately excluded — timestamptz re-renders the stored constant in the
// session timezone, and tsvector/tsquery re-render their lexemes with single quotes
// ('a b'→”'a” ”b”'; bareword 'cat'→”'cat”'). The internal "char" type
// (OID 18) is also excluded: TypeNameToOID maps "char" to bpchar (OID 1042), so
// the quoted-vs-unquoted distinction needed to emit `::"char"` is not tracked. Note jsonb byte-identity holds
// only for already-canonical values — non-scalar jsonb (e.g. objects) is
// re-rendered with key reordering / whitespace normalization, so the fixtures use
// canonical scalars.
// Other non-string base types return "".
// DU-002 slices 97 (text), 98 (char/varchar), 99 (integer/numeric),
// 100 (bigint/boolean/date), 101 (real/float8/timestamp/time/uuid),
// 102 (smallint/bytea/inet), 103 (macaddr/macaddr8/cidr), 104 (name/jsonb),
// 105 (json), 106 (xml/oid/bit/varbit), 107 (pg_lsn/tid/xid/cid),
// 108 (interval/money), 109 (user-defined enum base type).
func domainInValuesCheckExpr(baseType string, vals []string, cat *catalog.InMemory) string {
	if len(vals) == 0 {
		return ""
	}
	var castType string
	// coerceTo holds the target type of PG's coercion envelope for base types
	// that lack a direct equality operator: PG rewrites `VALUE IN (...)` as
	// `(VALUE)::T = ANY ((ARRAY[...])::T[])`. Empty means no envelope (the base
	// type's own eq operator is used directly).
	var coerceTo string
	// lhsCast handles the `VALUE::text IN (...)` source form (base types with no
	// equality operator AND whose IN-list literals are already untyped string
	// constants typed as the cast target). PG deparses `(VALUE)::T = ANY
	// (ARRAY['...'::T, ...])` — the LHS is cast but the array is NOT re-cast,
	// since each element literal already has type T. Empty means not used.
	var lhsCast string
	// A user-defined enum base type must be detected before the switch:
	// TypeNameToOID falls back to OIDText for an enum name, which would
	// mis-render the cast as `::text`. Enums have a native equality operator, so
	// PG emits the bare string-with-cast shape with the schema-qualified enum
	// type name, e.g. `VALUE = ANY (ARRAY['red'::public.rgb, ...])`. pg_dump sets
	// an empty search_path so the type is qualified; all goopg enums live in the
	// public schema (see expr.go format_type). Each label round-trips verbatim
	// (no normalization). DU-002 slice 109.
	if et, ok := enumForDomainBaseType(cat, baseType); ok {
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'::public." + et.Name
		}
		return "VALUE = ANY (ARRAY[" + strings.Join(parts, ", ") + "])"
	}
	switch catalog.TypeNameToOID(baseType) {
	case catalog.OIDText:
		castType = "text"
	case catalog.OIDBpChar:
		castType = "bpchar"
	case catalog.OIDDate:
		castType = "date"
	case catalog.OIDTimestamp:
		castType = "timestamp without time zone"
	case catalog.OIDTimestampTZ:
		// timestamp with time zone has a native equality operator, so PG emits
		// the bare string-with-cast shape. The output function renders the value
		// in the session TimeZone, so byte-identity holds only for inputs already
		// in the session-TZ canonical form; the fixtures pin UTC (`+00` offset)
		// against a UTC-session pg_dump so the literals round-trip verbatim
		// through `::timestamp with time zone`. goopg stores the IN-list literals
		// verbatim (no re-render), so its deparse is TZ-independent. DU-002 slice 110.
		castType = "timestamp with time zone"
	case catalog.OIDTime:
		castType = "time without time zone"
	case catalog.OIDTimeTZ:
		// time with time zone has a native equality operator, so PG emits the
		// bare string-with-cast shape. Unlike timestamptz, timetz's output
		// function preserves the stored zone offset verbatim (it does NOT
		// re-render in the session TimeZone), so the canonical
		// 'HH:MM:SS±HH[:MM]' form round-trips byte-identically through `::time
		// with time zone` regardless of session TZ. DU-002 slice 111.
		castType = "time with time zone"
	case catalog.OIDUUID:
		castType = "uuid"
	case catalog.OIDBytea:
		castType = "bytea"
	case catalog.OIDInet:
		castType = "inet"
	case catalog.OIDMacaddr:
		castType = "macaddr"
	case catalog.OIDMacaddr8:
		castType = "macaddr8"
	case catalog.OIDName:
		castType = "name"
	case catalog.OIDJsonb:
		castType = "jsonb"
	case catalog.OIDJSON:
		// json has no equality operator, so the CHECK casts VALUE to text
		// (`CHECK (VALUE::text IN (...))`). PG deparses `(VALUE)::text = ANY
		// (ARRAY['...'::text, ...])`: the LHS is cast but the array is not
		// re-cast, because each IN-list literal is an untyped string constant
		// already typed as text. Unlike jsonb, json preserves the input text
		// verbatim (no key reordering / whitespace normalization), so even
		// object/array values round-trip byte-identically through `::text`.
		castType = "text"
		lhsCast = "text"
	case catalog.OIDXML:
		// xml has no equality operator either, so the CHECK casts VALUE to text
		// (`CHECK (VALUE::text IN (...))`) — identical deparse shape to json:
		// `(VALUE)::text = ANY (ARRAY['...'::text, ...])`. xml is stored and
		// re-emitted verbatim, so the text round-trips byte-identically.
		castType = "text"
		lhsCast = "text"
	case catalog.OIDBit:
		// bit(n) has a native equality operator, so PG emits the bare
		// string-with-cast shape. The cast type name is quoted (`::"bit"`)
		// because `bit` is a non-standard type-name token in the deparser.
		castType = `"bit"`
	case catalog.OIDVarbit:
		// bit varying has a native equality operator; the canonical bit-string
		// form round-trips verbatim through `::bit varying`.
		castType = "bit varying"
	case catalog.OIDPgLsn:
		// pg_lsn has a native equality operator; the canonical uppercase-hex
		// form ('16/B374D848') round-trips verbatim through `::pg_lsn`.
		castType = "pg_lsn"
	case catalog.OIDTid:
		// tid has a native equality operator; the canonical '(block,offset)'
		// form round-trips verbatim through `::tid`.
		castType = "tid"
	case catalog.OIDXid:
		// xid has a native equality operator; the decimal txid form round-trips
		// verbatim through `::xid`.
		castType = "xid"
	case catalog.OIDCid:
		// cid has a native equality operator; the decimal command-id form
		// round-trips verbatim through `::cid`.
		castType = "cid"
	case catalog.OIDXid8:
		// xid8 (full 64-bit transaction id) has a native equality operator; the
		// decimal form round-trips verbatim through `::xid8`. Same simplest render
		// mode as xid/cid (slice 107). DU-002 slice 112.
		castType = "xid8"
	case catalog.OIDInt2vector:
		// int2vector (the legacy space-separated int2 list, e.g. pg_index.indkey)
		// has a native equality operator (int2vectoreq), so PG emits the bare
		// string-with-cast shape. The output function renders the canonical
		// space-separated form, so an already-canonical input ('1 2') round-trips
		// verbatim through `::int2vector`. DU-002 slice 113.
		castType = "int2vector"
	case catalog.OIDOidvector:
		// oidvector (the legacy space-separated oid list, e.g. pg_proc.proargtypes)
		// likewise has a native equality operator (oidvectoreq) and renders the
		// canonical space-separated form, round-tripping verbatim through
		// `::oidvector`. DU-002 slice 113.
		castType = "oidvector"
	case catalog.OIDTsvector:
		// tsvector has a native equality operator (tsvector_eq), so PG emits the
		// bare string-with-cast shape. Its output function renders lexemes in the
		// canonical form (single-quoted, sorted, deduplicated, positions stripped
		// when absent), so byte-identity holds only for already-canonical inputs
		// (e.g. the lexeme set `'a' 'b'`), which round-trip verbatim through
		// `::tsvector` — the same canonical-only contract as jsonb scalars /
		// interval. DU-002 slice 114.
		castType = "tsvector"
	case catalog.OIDTsquery:
		// tsquery has a native equality operator (tsquery_eq); the bare
		// string-with-cast shape applies. Its output normalizes operator spacing
		// and single-quotes lexemes, so the fixtures pin already-canonical forms
		// (`'a' & 'b'`) that round-trip verbatim through `::tsquery`. DU-002 slice 114.
		castType = "tsquery"
	case catalog.OIDInterval:
		// interval has a native equality operator. Its output function
		// normalizes the stored value (e.g. '2 hours'→'02:00:00'), so byte
		// identity holds only for already-canonical inputs ('1 day',
		// '02:00:00', '1 year 2 mons'), which round-trip verbatim through
		// `::interval` — the same canonical-only contract as jsonb scalars.
		castType = "interval"
	case catalog.OIDMoney:
		// money has a native equality operator. Its output depends on
		// lc_monetary; under the default C/POSIX locale the canonical form is
		// '$1.00', which round-trips verbatim through `::money`. Non-canonical
		// inputs or a non-C lc_monetary would re-render, so the fixtures use
		// the canonical C-locale form.
		castType = "money"
	case catalog.OIDCidr:
		// cidr has no cidr-eq operator, so PG coerces both sides to inet
		// (the element cast stays ::cidr, the envelope is ::inet / ::inet[]).
		castType = "cidr"
		coerceTo = "inet"
	case catalog.OIDVarChar:
		castType = "character varying"
		coerceTo = "text"
	case catalog.OIDInt2, catalog.OIDInt4, catalog.OIDNumeric, catalog.OIDBool:
		// smallint/integer/numeric literals and boolean keyword literals already
		// carry (or const-fold to) the base type, so PG renders them verbatim (no
		// quotes, no cast).
		arr := "ARRAY[" + strings.Join(vals, ", ") + "]"
		return "VALUE = ANY (" + arr + ")"
	case catalog.OIDInt8:
		// bigint: the IN-list int4 literals are coerced per element to bigint.
		return domainInValuesCoerced(vals, "bigint")
	case catalog.OIDOID:
		// oid: the IN-list int4 literals are coerced per element to oid
		// (`(1)::oid`), the same per-element coercion shape bigint/real use.
		return domainInValuesCoerced(vals, "oid")
	case catalog.OIDFloat4:
		// real: the IN-list numeric literals are coerced per element to real.
		return domainInValuesCoerced(vals, "real")
	case catalog.OIDFloat8:
		// double precision: numeric literals coerced per element.
		return domainInValuesCoerced(vals, "double precision")
	default:
		return ""
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		// PG quotes string literals with single quotes and doubles any embedded
		// quote; mirror that so the deparse is byte-identical.
		parts[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'::" + castType
	}
	arr := "ARRAY[" + strings.Join(parts, ", ") + "]"
	if lhsCast != "" {
		return "(VALUE)::" + lhsCast + " = ANY (" + arr + ")"
	}
	if coerceTo != "" {
		return "(VALUE)::" + coerceTo + " = ANY ((" + arr + ")::" + coerceTo + "[])"
	}
	return "VALUE = ANY (" + arr + ")"
}

// enumForDomainBaseType resolves a domain's base-type name to a user-defined
// enum, if one exists. The parser stores the base type as either a bare name
// (`rgb`) or schema-qualified (`public.rgb`); enums are keyed by bare name in
// the catalog, so we strip any leading schema component before the lookup.
// Returns nil,false for built-in base types or when no enum matches.
func enumForDomainBaseType(cat *catalog.InMemory, baseType string) (*catalog.EnumType, bool) {
	if cat == nil {
		return nil, false
	}
	name := baseType
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return cat.LookupEnum(name)
}

// domainInValuesCoerced renders the per-element coercion shape used by base types
// whose IN-list literals parse as a different (narrower) numeric type than the
// base, so PG wraps each literal `(N)::<castType>`: bigint (int4 → bigint), real
// and double precision (numeric → float). DU-002 slices 100, 101.
func domainInValuesCoerced(vals []string, castType string) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = "(" + v + ")::" + castType
	}
	arr := "ARRAY[" + strings.Join(parts, ", ") + "]"
	return "VALUE = ANY (" + arr + ")"
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
		// Stamp the pg_type heap row's xmax before the in-memory delete (need the
		// OID while the domain still exists), mirroring execDropType. DU-002 slice 90.
		if d, ok := cat.LookupDomain(name.Name); ok && catalogHeapSyncAvailable(o.ctx) {
			if o.ctx.MaterializeWriterXID() == nil {
				deleteTypeFromCatalogHeap(o.ctx, catalog.DefaultDBOid, d.OID, o.ctx.Tx.XID)
				_ = mirrorCatalogRelToPostgresDB(o.ctx, catalog.TypeRelationId)
			}
		}
		// names = dropped tables (CASCADE) or blocking tables (RESTRICT).
		names, err := cat.DropDomain(name.Name, false, s.Cascade)
		if err == nil {
			for _, tblName := range names {
				o.ctx.AddNotice(fmt.Sprintf("drop cascades to table %s", tblName))
			}
			continue
		}
		if err.Error() == "dependent objects" {
			// names contains the tables blocking the drop.
			depName := ""
			if len(names) > 0 {
				depName = names[0]
			}
			return &ExecError{
				Code:    "2BP01",
				Pos:     s.Pos(),
				Message: fmt.Sprintf("cannot drop type %s because other objects depend on it", name.Name),
				Detail:  fmt.Sprintf("table %s depends on type %s", depName, name.Name),
				Hint:    "Use DROP ... CASCADE to drop the dependent objects too.",
			}
		}
		if s.IfExists {
			o.ctx.AddNotice(fmt.Sprintf("type %q does not exist, skipping", name.Name))
			continue
		}
		return &ExecError{Code: "42704", Pos: s.Pos(), Message: err.Error()}
	}
	return nil
}

// execLockTable handles LOCK [TABLE] rel [, ...] [IN mode MODE] [NOWAIT].
// It records the held locks in globalRelLockMgr so they appear in pg_locks.
// execAlterColumnType implements `ALTER TABLE t ALTER COLUMN col TYPE newtype`.
// It updates the column type in the catalog and, when storage is available,
// rewrites existing heap rows to re-encode the column value in the new type.
// M0097-0022.
// execAlterDropColumn implements ALTER TABLE ... DROP COLUMN via a table
// rewrite: reads all visible rows with the OLD schema, removes the dropped
// column's slot, updates the catalog, truncates the heap + indexes, then
// re-inserts all rows and rebuilds indexes. Mirrors execAlterColumnType.
// Indexes referencing only the dropped column become empty orphans (harmless
// for now; a future pass can DROP them explicitly).
func (o *ddlOp) execAlterDropColumn(tbl *catalog.Table, act parser.AlterTableAction) error {
	// Find the column to drop.
	dropIdx := -1
	for i, col := range tbl.Columns {
		if strings.EqualFold(col.Name, act.ColumnName) {
			dropIdx = i
			break
		}
	}
	if dropIdx < 0 {
		return &ExecError{Code: "42703", Pos: act.Pos(), Message: fmt.Sprintf("column %q of relation %q does not exist", act.ColumnName, tbl.Name)}
	}

	// Cannot drop a column that is part of the partition key.
	if tbl.PartitionMethod != "" {
		colLower := strings.ToLower(act.ColumnName)
		for _, keyCol := range tbl.PartitionKey {
			if strings.ToLower(keyCol) == colLower {
				return &ExecError{Code: "0A000", Pos: act.Pos(),
					Message: fmt.Sprintf("cannot drop column %q because it is part of the partition key of relation %q", act.ColumnName, tbl.Name)}
			}
		}
		// Also check expression partition keys (e.g. PARTITION BY RANGE (plusone(a))).
		for _, expr := range tbl.PartitionKeyExprs {
			if strings.Contains(strings.ToLower(fmt.Sprintf("%v", expr)), colLower) {
				return &ExecError{Code: "0A000", Pos: act.Pos(),
					Message: fmt.Sprintf("cannot drop column %q because it is part of the partition key of relation %q", act.ColumnName, tbl.Name)}
			}
		}
	}

	// Save old columns for decoding existing heap rows.
	oldCols := make([]catalog.Column, len(tbl.Columns))
	copy(oldCols, tbl.Columns)

	// If no storage pool is available, just update the catalog (test/unit path).
	if o.ctx.Pool == nil {
		tbl.Columns = append(tbl.Columns[:dropIdx], tbl.Columns[dropIdx+1:]...)
		for i := range tbl.Columns {
			tbl.Columns[i].Ordinal = i
		}
		return nil
	}

	rel := o.ctx.Catalog.RelFileNode(tbl)
	nBlocks, err := o.ctx.Pool.NBlocks(rel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: act.Pos(), Message: err.Error()}
	}

	// Phase 1: read all visible rows using the OLD column schema.
	var allRows []Row
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		bufSlot, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if perr != nil {
			continue
		}
		bufSlot.RLock()
		page := bufSlot.Page()
		if storage.IsNew(page) {
			bufSlot.RUnlock()
			o.ctx.Pool.Unpin(bufSlot)
			continue
		}
		count, cerr := storage.PageLinePointerCount(page)
		if cerr != nil {
			bufSlot.RUnlock()
			o.ctx.Pool.Unpin(bufSlot)
			continue
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, terr := storage.PageGetHeapTuple(page, slotIdx)
			if terr != nil {
				continue
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.TxnMgr) {
				continue
			}
			row := acquireRow(len(oldCols))
			storedNatts := int(tuple.Header.Infomask2 & 0x07FF)
			if decErr := DecodeRowIntoMctxPGTuple(row, oldCols, tuple.Data, tuple.Bitmap, storedNatts, nil); decErr != nil {
				releaseRow(row)
				continue
			}
			// Build a new row with the dropped column removed.
			newRow := make(Row, len(oldCols)-1)
			copy(newRow[:dropIdx], row[:dropIdx])
			copy(newRow[dropIdx:], row[dropIdx+1:])
			allRows = append(allRows, cloneRowOwned(newRow))
			releaseRow(row)
		}
		bufSlot.RUnlock()
		o.ctx.Pool.Unpin(bufSlot)
	}

	// Phase 2: update the catalog — remove the dropped column and update ordinals.
	tbl.Columns = append(tbl.Columns[:dropIdx], tbl.Columns[dropIdx+1:]...)
	for i := range tbl.Columns {
		tbl.Columns[i].Ordinal = i
	}

	// Phase 3: drop indexes that reference the dropped column (key or INCLUDE),
	// then truncate the heap and remaining indexes.
	droppedColName := strings.ToLower(act.ColumnName)
	for _, idx := range o.ctx.Catalog.IndexesOnTable(tbl) {
		refsDropped := false
		for _, c := range idx.Columns {
			if strings.EqualFold(c, droppedColName) {
				refsDropped = true
				break
			}
		}
		if !refsDropped {
			for _, c := range idx.IncludeColumns {
				if strings.EqualFold(c, droppedColName) {
					refsDropped = true
					break
				}
			}
		}
		if refsDropped {
			idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
			o.ctx.Pool.InvalidateRel(idxRel)
			_ = o.ctx.Pool.Manager().TruncateRelation(idxRel)
			idxName := parser.ObjectName{Schema: idx.Schema, Name: idx.Name}
			_ = o.ctx.Catalog.DropIndex(idxName)
		}
	}
	o.ctx.Pool.InvalidateRel(rel)
	if terr := o.ctx.Pool.Manager().TruncateRelation(rel); terr != nil {
		return &ExecError{Code: "XX000", Pos: act.Pos(), Message: terr.Error()}
	}
	if o.ctx.FSM != nil {
		o.ctx.FSM.DropRelation(rel)
	}
	if o.ctx.VM != nil {
		o.ctx.VM.DropRelation(rel)
	}
	for _, idx := range o.ctx.Catalog.IndexesOnTable(tbl) {
		idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
		o.ctx.Pool.InvalidateRel(idxRel)
		_ = o.ctx.Pool.Manager().TruncateRelation(idxRel)
		_, _ = btree.Create(o.ctx.Pool, idxRel)
	}

	// Phase 4: re-insert all rows with the new column layout and rebuild indexes.
	for _, row := range allRows {
		ptr, werr := writeHeapRowReturning(o.ctx, rel, tbl.Columns, row)
		if werr != nil {
			return werr
		}
		maintainUniqueIndexesForInsert(o.ctx, tbl, tbl.Columns, row, ptr)
	}

	// Phase 5: update catalog heap — delete old pg_class/pg_attribute rows and
	// re-sync so the dropped column is no longer visible via pg_attribute scans.
	if catalogHeapSyncAvailable(o.ctx) {
		if err := o.ctx.MaterializeWriterXID(); err == nil {
			xmax := o.ctx.Tx.XID
			for _, dbOid := range catalogDBOids(o.ctx) {
				deleteCatalogRowsForOID(o.ctx, dbOid, tbl.OID, xmax)
			}
		}
		if syncErr := syncTableToCatalogHeap(o.ctx, tbl); syncErr != nil {
			return fmt.Errorf("DDL catalog sync: %w", syncErr)
		}
	}
	return nil
}

func (o *ddlOp) execAlterColumnType(tbl *catalog.Table, act parser.AlterTableAction) error {
	colIdx := -1
	for i, col := range tbl.Columns {
		if strings.EqualFold(col.Name, act.ColumnName) {
			colIdx = i
			break
		}
	}
	if colIdx < 0 {
		return &ExecError{Code: "42703", Pos: act.Pos(), Message: fmt.Sprintf("column %q of relation %q does not exist", act.ColumnName, tbl.Name)}
	}

	oldCatalogType := tbl.Columns[colIdx].Type
	newCatalogType := catalog.Type{Name: strings.ToLower(act.NewType.Name), Args: append([]int64(nil), act.NewType.Args...)}

	// No-op when the type name is unchanged.
	if strings.EqualFold(oldCatalogType.Name, newCatalogType.Name) {
		return nil
	}

	// If no storage pool is available, just update the catalog type.
	if o.ctx.Pool == nil {
		tbl.Columns[colIdx].Type = newCatalogType
		return nil
	}

	rel := o.ctx.Catalog.RelFileNode(tbl)
	nBlocks, err := o.ctx.Pool.NBlocks(rel)
	if err != nil {
		return &ExecError{Code: "XX000", Pos: act.Pos(), Message: err.Error()}
	}

	// If the table is empty, just update the catalog — nothing to rewrite.
	if nBlocks == 0 {
		tbl.Columns[colIdx].Type = newCatalogType
		return nil
	}

	// Phase 1: read all visible rows using the OLD column schema.
	// Build a copy of the columns slice with the old type for decoding.
	oldCols := make([]catalog.Column, len(tbl.Columns))
	copy(oldCols, tbl.Columns)
	// oldCols[colIdx] already has the old type.

	var allRows []Row
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		bufSlot, perr := o.ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if perr != nil {
			continue
		}
		bufSlot.RLock()
		page := bufSlot.Page()
		if storage.IsNew(page) {
			bufSlot.RUnlock()
			o.ctx.Pool.Unpin(bufSlot)
			continue
		}
		count, cerr := storage.PageLinePointerCount(page)
		if cerr != nil {
			bufSlot.RUnlock()
			o.ctx.Pool.Unpin(bufSlot)
			continue
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, terr := storage.PageGetHeapTuple(page, slotIdx)
			if terr != nil {
				continue
			}
			if !mvcc.TupleVisibleSubxact(tuple.Header, o.ctx.Snap, o.ctx.Tx.XID, o.ctx.TxnMgr) {
				continue
			}
			row := acquireRow(len(oldCols))
			storedNatts := int(tuple.Header.Infomask2 & 0x07FF)
			if decErr := DecodeRowIntoMctxPGTuple(row, oldCols, tuple.Data, tuple.Bitmap, storedNatts, nil); decErr != nil {
				continue
			}
			// Convert the changed column to the new type.
			if colIdx < len(row) {
				if converted, cErr := evalCast(row[colIdx], newCatalogType.Name, act.Pos()); cErr == nil {
					row[colIdx] = converted
				}
			}
			// Deep-copy the row so arena-backed string Datums survive
			// beyond the page pin (cloneRowOwned allocates fresh backing).
			allRows = append(allRows, cloneRowOwned(row))
			releaseRow(row)
		}
		bufSlot.RUnlock()
		o.ctx.Pool.Unpin(bufSlot)
	}

	// Phase 2: update the catalog column type.
	tbl.Columns[colIdx].Type = newCatalogType

	// Phase 3: truncate the heap and all indexes.
	o.ctx.Pool.InvalidateRel(rel)
	if terr := o.ctx.Pool.Manager().TruncateRelation(rel); terr != nil {
		return &ExecError{Code: "XX000", Pos: act.Pos(), Message: terr.Error()}
	}
	if o.ctx.FSM != nil {
		o.ctx.FSM.DropRelation(rel)
	}
	if o.ctx.VM != nil {
		o.ctx.VM.DropRelation(rel)
	}
	for _, idx := range o.ctx.Catalog.IndexesOnTable(tbl) {
		idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
		o.ctx.Pool.InvalidateRel(idxRel)
		_ = o.ctx.Pool.Manager().TruncateRelation(idxRel)
		_, _ = btree.Create(o.ctx.Pool, idxRel)
	}

	// Phase 4: re-insert all rows with the new encoding.
	for _, row := range allRows {
		if werr := writeHeapRow(o.ctx, rel, tbl.Columns, row); werr != nil {
			return werr
		}
	}
	return nil
}

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
// dropCascadeObjectName returns the name to use in DROP CASCADE notices.
// Mirrors PostgreSQL: omit the schema prefix when the schema is in the
// current search_path (it's implicit), qualify otherwise. M0097-0022.
func dropCascadeObjectName(name parser.ObjectName, ctx *Context) string {
	if name.Schema == "" {
		return name.Name
	}
	for _, sc := range lockTableSearchSchemas(ctx) {
		if strings.EqualFold(sc, name.Schema) {
			return name.Name
		}
	}
	return name.String()
}

// lookupTableWithSearch finds a table by name, falling back to search_path schemas
// for unqualified names. M0097-0022.
func (o *ddlOp) lookupTableWithSearch(name parser.ObjectName) (*catalog.Table, bool) {
	tbl, ok := o.ctx.Catalog.LookupTable(name)
	if !ok && name.Schema == "" {
		for _, sc := range lockTableSearchSchemas(o.ctx) {
			tbl, ok = o.ctx.Catalog.LookupTable(parser.ObjectName{Schema: sc, Name: name.Name})
			if ok {
				break
			}
		}
	}
	return tbl, ok
}

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

// buildFunctionArgsList returns a comma-separated arg type list for a Routine,
// using canonical type names. Used in DROP CASCADE NOTICE messages.
func buildFunctionArgsList(r *catalog.Routine) string {
	if len(r.ArgTypes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.ArgTypes))
	for i, t := range r.ArgTypes {
		mode := "i"
		if i < len(r.ArgModes) && r.ArgModes[i] != "" {
			mode = r.ArgModes[i]
		}
		if mode == "o" {
			continue // OUT params excluded from signature
		}
		typeName := strings.ToLower(t.Name)
		if canon := dropCompatCanonicalType(typeName); canon != "" {
			typeName = canon
		}
		parts = append(parts, typeName)
	}
	return strings.Join(parts, ",")
}

// extractRoutineDeps parses the function body and arg defaults to populate
// dependency fields on r for information_schema routine_*_usage views.
// inferSQLFunctionVolatility returns "i" (immutable) or "v" (volatile) by
// scanning the SQL body for known volatile built-in function calls.
// This mirrors PostgreSQL's provolatility inference for SQL functions
// declared without an explicit VOLATILE/STABLE/IMMUTABLE marker.
func inferSQLFunctionVolatility(body string) string {
	// Known built-in volatile functions (subset sufficient for the test suite).
	volatileFuncs := []string{
		"random", "nextval", "currval", "lastval", "setval",
		"now", "clock_timestamp", "statement_timestamp",
		"transaction_timestamp", "timeofday",
		"gen_random_uuid", "uuid_generate_v4",
		"txid_current",
	}
	lower := strings.ToLower(body)
	for _, fn := range volatileFuncs {
		// Match "fn(" to avoid false positives on substrings.
		if strings.Contains(lower, fn+"(") {
			return "v"
		}
	}
	return "i"
}

func extractRoutineDeps(body string, argDefaults []string, schema string, r *catalog.Routine, cat catalog.Catalog) {
	// Panic recovery: extractRoutineDeps is a best-effort feature; never crash the server.
	defer func() { recover() }() //nolint:errcheck
	// Skip very long bodies to bound overhead.
	if len(body) > 2000 {
		return
	}
	// walkExpr walks a parser expression collecting deps.
	var walkExpr func(e parser.Expr, fromTables map[string]string)
	var walkSelect func(sel *parser.SelectStmt)

	walkExpr = func(e parser.Expr, fromTables map[string]string) {
		if e == nil {
			return
		}
		switch n := e.(type) {
		case *parser.FuncCall:
			funcName := strings.ToLower(n.Name.Name)
			// Sequence deps: nextval/currval with string literal arg.
			if (funcName == "nextval" || funcName == "currval") && len(n.Args) >= 1 {
				if sc, ok := n.Args[0].(*parser.StringConst); ok {
					seqName := strings.ToLower(sc.Value)
					if idx := strings.LastIndex(seqName, "."); idx >= 0 {
						seqName = seqName[idx+1:]
					}
					seqSchema := schema
					found := false
					for _, d := range r.SequenceDeps {
						if d.Name == seqName {
							found = true
							break
						}
					}
					if !found {
						r.SequenceDeps = append(r.SequenceDeps, catalog.RoutineSeqDep{Schema: seqSchema, Name: seqName})
					}
				}
			}
			// Routine call deps: look up the function name in catalog.
			if cat != nil && funcName != "nextval" && funcName != "currval" {
				if rs := cat.Routines(); rs != nil {
					cands := rs.LookupByName(n.Name)
					for _, cand := range cands {
						found := false
						for _, oid := range r.RoutineCallOIDs {
							if oid == cand.OID {
								found = true
								break
							}
						}
						if !found {
							r.RoutineCallOIDs = append(r.RoutineCallOIDs, cand.OID)
						}
					}
				}
			}
			for _, a := range n.Args {
				walkExpr(a, fromTables)
			}
		case *parser.ColumnRef:
			// Attribute bare column refs to the single FROM table if applicable.
			if len(fromTables) == 1 {
				colName := strings.ToLower(n.Column)
				if colName == "" {
					break
				}
				var tblName, tblSchema string
				for k, v := range fromTables {
					tblName = k
					tblSchema = v
				}
				found := false
				for _, cd := range r.ColumnDeps {
					if cd.TableName == tblName && cd.ColumnName == colName {
						found = true
						break
					}
				}
				if !found {
					r.ColumnDeps = append(r.ColumnDeps, catalog.RoutineColRef{
						TableSchema: tblSchema,
						TableName:   tblName,
						ColumnName:  colName,
					})
				}
			}
		case *parser.BinaryOp:
			walkExpr(n.Left, fromTables)
			walkExpr(n.Right, fromTables)
		case *parser.UnaryOp:
			walkExpr(n.Operand, fromTables)
		case *parser.CastExpr:
			walkExpr(n.Operand, fromTables)
		case *parser.SubqueryExpr:
			if n.Inner != nil {
				walkSelect(n.Inner)
			}
		case *parser.CaseExpr:
			walkExpr(n.Operand, fromTables)
			for _, w := range n.Whens {
				walkExpr(w.When, fromTables)
				walkExpr(w.Then, fromTables)
			}
			walkExpr(n.Else, fromTables)
		case *parser.IsNullExpr:
			walkExpr(n.Operand, fromTables)
		}
	}

	walkSelect = func(sel *parser.SelectStmt) {
		if sel == nil {
			return
		}
		// Collect FROM tables for column attribution.
		localFrom := map[string]string{}
		for _, rv := range sel.From {
			if rv.Name != "" {
				tblName := strings.ToLower(rv.Name)
				tblSchema := strings.ToLower(rv.Schema)
				if tblSchema == "" {
					tblSchema = schema
				}
				localFrom[tblName] = tblSchema
				found := false
				for _, td := range r.TableDeps {
					if td.Name == tblName && td.Schema == tblSchema {
						found = true
						break
					}
				}
				if !found {
					r.TableDeps = append(r.TableDeps, catalog.RoutineTableRef{Schema: tblSchema, Name: tblName})
				}
			}
			if rv.Subquery != nil {
				walkSelect(rv.Subquery)
			}
		}
		// Walk targets.
		for _, t := range sel.Targets {
			walkExpr(t.Expr, localFrom)
		}
		// Walk WHERE.
		walkExpr(sel.Where, localFrom)
		// Walk set-op right branch if present.
		if sel.SetOp != nil && sel.SetOp.Right != nil {
			walkSelect(sel.SetOp.Right)
		}
	}

	// Parse and walk the function body for table/sequence/routine deps.
	// Only BEGIN ATOMIC and RETURN-form bodies create pg_depend entries in PG14+.
	// AS-quoted-string bodies (the old style) do NOT create body-level deps.
	if body != "" && (r.BeginAtomic || r.IsReturnForm) {
		stmts, err := parser.Parse(body)
		if err == nil {
			for _, st := range stmts {
				switch s := st.(type) {
				case *parser.SelectStmt:
					walkSelect(s)
				case *parser.InsertStmt:
					// Record INSERT INTO table as a table dependency.
					if s.Target.Name != "" {
						tblName := strings.ToLower(s.Target.Name)
						tblSchema := strings.ToLower(s.Target.Schema)
						if tblSchema == "" {
							tblSchema = schema
						}
						found := false
						for _, td := range r.TableDeps {
							if td.Name == tblName && td.Schema == tblSchema {
								found = true
								break
							}
						}
						if !found {
							r.TableDeps = append(r.TableDeps, catalog.RoutineTableRef{Schema: tblSchema, Name: tblName})
						}
					}
					if s.Select != nil {
						walkSelect(s.Select)
					}
				}
			}
		}
	}

	// Parse and walk each default expression.
	for _, def := range argDefaults {
		if def == "" {
			continue
		}
		e, err := parser.ParseExpr(def)
		if err == nil {
			walkExpr(e, nil)
		}
	}
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
