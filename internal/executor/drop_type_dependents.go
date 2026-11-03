package executor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0148-0001j1 — DROP TYPE honours the functions and aggregates that depend
// on the type, as dependency.c's findDependentObjects / reportDependentObjects
// do over pg_depend.
//
// goopg keeps no pg_depend rows for routines, so the edges PG records are
// rebuilt from the catalog:
//
//   - a function depends on every argument type and its result type
//     (ProcedureCreate's recordDependencyOn calls);
//   - an array type is a separate type that depends INTERNALLY on its
//     element type — dropping the element takes it along without reporting
//     it, and a function over `t[]` depends on the array type;
//   - a typed table (`CREATE TABLE … OF t`) depends on t;
//   - an aggregate's pg_proc row depends on its argument and result types,
//     and pg_aggregate adds its transition, final, combine and serialization
//     functions (AggregateCreate) — NOT its state type, so `st_avg(int)`
//     with STYPE st is reached through its functions.
//
// The walk is findDependentObjects': each object's dependents are visited in
// descending OID order (object_address_comparator), depth first, each object
// appended after its dependents (post-order) with the object it was first
// reached from as its dependee; reportDependentObjects then reports the list
// back to front, skipping the original target and internal dependents.
// Before this, DROP TYPE on a composite type checked nothing: RESTRICT
// dropped it under its functions, and CASCADE left them behind, so a
// re-CREATE failed "function already exists" and the stale function
// returned NULL.

// M0148-0001j2 extends the walk to the remaining pg_depend edges a type
// carries (heap_create_with_catalog / AddNewAttributeTuples,
// DefineDomain's GenerateTypeDependencies):
//
//   - a table column of the type depends on it — object (pg_class, table
//     OID, attnum), described `column x of table t`; CASCADE drops the
//     column, not the table;
//   - an attribute of a composite type is the same edge on the composite
//     type's relation (`column p of composite type ct2`);
//   - a domain depends NORMALLY on its base type, and is itself a type
//     with its own array type and dependents (`type dct`), so the walk
//     recurses through it.
//
// A column typed `t[]` depends on the array type, which is why PG reports
// it as `column z of table tc depends on type t[]`.
//
// Siblings are ordered by object_address_comparator: OID descending, then
// classId ascending (pg_type 1247 < pg_proc 1255 < pg_class 1259), then
// objsubid ascending.

const (
	depClassType  = 1247 // pg_type
	depClassProc  = 1255 // pg_proc
	depClassClass = 1259 // pg_class
)

type typeDepNode struct {
	kind     string // "type", "arraytype", "domain", "function", "aggregate", "table", "column", "attribute"
	oid      uint32
	classID  uint32
	subID    int
	desc     string
	routine  *catalog.Routine
	agg      *catalog.UserAggregate
	table    *catalog.Table
	column   string                 // column / attribute name
	comp     *catalog.CompositeType // attribute owner
	domain   *catalog.Domain
	internal bool
	// Type-like nodes ("type", "domain", and their "arraytype"): the type's
	// name and OIDs the dependents are matched against; elem links an array
	// node to its element type node.
	tname     string
	tOID      uint32
	tArrayOID uint32
	elem      *typeDepNode
}

type typeDepTarget struct {
	node     *typeDepNode
	dependee *typeDepNode
}

// depTypeRef parses a type spelling (`public.ct`, `ct[]`, `varchar(5)`,
// `"CT"`) to its bare lower-case name and array-ness.
func depTypeRef(spelling string) (string, bool) {
	s := strings.TrimSpace(spelling)
	isArray := false
	for strings.HasSuffix(s, "[]") {
		s, isArray = strings.TrimSpace(strings.TrimSuffix(s, "[]")), true
	}
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = depTypeBaseName(s)
	return strings.ToLower(strings.Trim(s, `"`)), isArray
}

// columnTypeRef is a column's declared type: the domain name when it was
// declared over a domain (Type.Name then holds the resolved base type),
// else its type.
func columnTypeRef(col catalog.Column) (string, bool) {
	if col.DeclaredTypeName != "" {
		name, arr := depTypeRef(col.DeclaredTypeName)
		return name, arr || col.Type.IsArray
	}
	name, arr := depTypeRef(col.Type.Name)
	return name, arr || col.Type.IsArray
}

// relationDependencyDesc is getRelationDescription's kind word.
func relationDependencyDesc(tbl *catalog.Table) string {
	switch {
	case tbl.ForeignServerName != "":
		return "foreign table " + tbl.Name
	case tbl.IsMatView:
		return "materialized view " + tbl.Name
	case tbl.View != nil:
		return "view " + tbl.Name
	default:
		return "table " + tbl.Name
	}
}

// typeDependentObjects returns the objects DROP TYPE / DROP DOMAIN of the
// type (oid, arrayOID, display name) must drop, in deletion order
// (dependents first), each with the object it was reached from. The type
// itself and its array type are not included.
func typeDependentObjects(cat catalog.Catalog, dbOid uint32, typeName string, oid, arrayOID uint32) []typeDepTarget {
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		return nil
	}
	var routines []*catalog.Routine
	if rs := cat.Routines(); rs != nil {
		routines = rs.List()
	}
	aggs := im.ListUserAggregates()
	tables := im.UserTableHandles(catalog.NamespaceDBOid(dbOid))
	var comps []*catalog.CompositeType
	for _, ct := range im.AllCompositeTypes() {
		if im.LookupCompositeType(ct.Name, dbOid) == ct {
			comps = append(comps, ct)
		}
	}
	sort.Slice(comps, func(i, j int) bool { return comps[i].OID < comps[j].OID })
	var domains []*catalog.Domain
	for _, d := range im.AllDomains() {
		if ld, ok := im.LookupDomain(d.Name, dbOid); ok && ld == d {
			domains = append(domains, d)
		}
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i].OID < domains[j].OID })

	// matches reports whether a reference (static type t / OID tOID) names
	// the type-like node base (its array type when wantArray).
	matches := func(base *typeDepNode, t catalog.Type, tOID uint32, wantArray bool) bool {
		if tOID != 0 {
			if wantArray {
				return base.tArrayOID != 0 && tOID == base.tArrayOID
			}
			return base.tOID != 0 && tOID == base.tOID
		}
		name, isArray := depTypeBaseName(t.Name), t.IsArray
		if strings.HasSuffix(name, "[]") {
			name, isArray = strings.TrimSuffix(name, "[]"), true
		} else if strings.HasPrefix(name, "_") && strings.EqualFold(name[1:], base.tname) {
			name, isArray = name[1:], true
		}
		return isArray == wantArray && strings.EqualFold(name, base.tname)
	}
	matchesName := func(base *typeDepNode, spelling string, wantArray bool) bool {
		name, isArray := depTypeRef(spelling)
		return isArray == wantArray && name == strings.ToLower(base.tname)
	}
	routineUses := func(base *typeDepNode, r *catalog.Routine, wantArray bool) bool {
		for i, t := range r.ArgTypes {
			var tOID uint32
			if i < len(r.ArgTypeOIDs) {
				tOID = r.ArgTypeOIDs[i]
			}
			if matches(base, t, tOID, wantArray) {
				return true
			}
		}
		return matches(base, r.ReturnType, r.ReturnTypeOID, wantArray)
	}
	aggResultType := func(a *catalog.UserAggregate) string {
		if a.FinalFunc != "" {
			for _, r := range routines {
				if strings.EqualFold(r.Name, a.FinalFunc) {
					rt := r.ReturnType.Name
					if r.ReturnType.IsArray {
						rt += "[]"
					}
					return rt
				}
			}
			return ""
		}
		return a.SType
	}
	aggUses := func(base *typeDepNode, a *catalog.UserAggregate, wantArray bool) bool {
		for _, t := range a.ArgTypes {
			if matchesName(base, t, wantArray) {
				return true
			}
		}
		return matchesName(base, aggResultType(a), wantArray)
	}

	nodes := map[string]*typeDepNode{}
	node := func(kind string, key uint32, sub int, mk func() *typeDepNode) *typeDepNode {
		k := fmt.Sprintf("%s/%d/%d", kind, key, sub)
		if n, ok := nodes[k]; ok {
			return n
		}
		n := mk()
		nodes[k] = n
		return n
	}
	fnNode := func(r *catalog.Routine) *typeDepNode {
		return node("function", r.OID, 0, func() *typeDepNode {
			return &typeDepNode{kind: "function", oid: r.OID, classID: depClassProc, desc: "function " + routineDependencyDesc(r), routine: r}
		})
	}
	aggNode := func(a *catalog.UserAggregate) *typeDepNode {
		return node("aggregate", a.OID, 0, func() *typeDepNode {
			return &typeDepNode{kind: "aggregate", oid: a.OID, classID: depClassProc, desc: "function " + aggregateDependencyDesc(a), agg: a}
		})
	}
	arrayNode := func(base *typeDepNode) *typeDepNode {
		return node("arraytype", base.tArrayOID, int(base.tOID), func() *typeDepNode {
			return &typeDepNode{kind: "arraytype", oid: base.tArrayOID, classID: depClassType,
				desc: "type " + base.tname + "[]", internal: true, tname: base.tname, elem: base}
		})
	}
	root := &typeDepNode{kind: "type", oid: oid, classID: depClassType, desc: "type " + typeName,
		tname: typeName, tOID: oid, tArrayOID: arrayOID}

	dependents := func(n *typeDepNode) []*typeDepNode {
		var out []*typeDepNode
		switch n.kind {
		case "type", "domain", "arraytype":
			base, wantArray := n, false
			if n.kind == "arraytype" {
				base, wantArray = n.elem, true
			}
			if !wantArray && (base.tArrayOID != 0 || base.tOID == 0) {
				out = append(out, arrayNode(base))
			}
			for _, r := range routines {
				if routineUses(base, r, wantArray) {
					out = append(out, fnNode(r))
				}
			}
			for _, a := range aggs {
				if aggUses(base, a, wantArray) {
					out = append(out, aggNode(a))
				}
			}
			// A typed table (`CREATE TABLE … OF type`) depends on its
			// type (heap_create_with_catalog records reloftype).
			if !wantArray && base.tOID != 0 {
				for _, tbl := range tables {
					if tbl.OfTypeOID == base.tOID {
						tb := tbl
						out = append(out, node("table", tb.OID, 0, func() *typeDepNode {
							return &typeDepNode{kind: "table", oid: tb.OID, classID: depClassClass, desc: "table " + tb.Name, table: tb}
						}))
					}
				}
			}
			// Columns of the type (AddNewAttributeTuples records each
			// attribute's dependency on atttypid).
			lname := strings.ToLower(base.tname)
			for _, tbl := range tables {
				if tbl.IsSequence {
					continue
				}
				for ci, col := range tbl.Columns {
					if col.Dropped {
						continue
					}
					name, isArr := columnTypeRef(col)
					if name != lname || isArr != wantArray {
						continue
					}
					tb, cname, sub := tbl, col.Name, ci+1
					out = append(out, node("column", tb.OID, sub, func() *typeDepNode {
						return &typeDepNode{kind: "column", oid: tb.OID, classID: depClassClass, subID: sub,
							desc: "column " + cname + " of " + relationDependencyDesc(tb), table: tb, column: cname}
					}))
				}
			}
			// Attributes of composite types (the composite type's
			// relation carries the same per-attribute dependency).
			for _, ct := range comps {
				for fi, f := range ct.Fields {
					if !matchesName(base, f.ColType, wantArray) {
						continue
					}
					c, fname, sub := ct, f.Name, fi+1
					out = append(out, node("attribute", c.RelOID, sub, func() *typeDepNode {
						return &typeDepNode{kind: "attribute", oid: c.RelOID, classID: depClassClass, subID: sub,
							desc: "column " + fname + " of composite type " + c.Name, comp: c, column: fname}
					}))
				}
			}
			// Domains over the type (GenerateTypeDependencies: a domain
			// depends normally on its base type).
			for _, d := range domains {
				if d.OID == base.tOID {
					continue
				}
				hit := false
				if d.BaseOID != 0 && !d.Base.IsArray && !wantArray {
					hit = d.BaseOID == base.tOID
				} else {
					name, isArr := depTypeRef(d.Base.Name)
					hit = name == lname && (isArr || d.Base.IsArray) == wantArray
				}
				if !hit {
					continue
				}
				dd := d
				out = append(out, node("domain", dd.OID, 0, func() *typeDepNode {
					return &typeDepNode{kind: "domain", oid: dd.OID, classID: depClassType, desc: "type " + dd.Name,
						domain: dd, tname: dd.Name, tOID: dd.OID, tArrayOID: dd.ArrayOID}
				}))
			}
		case "function":
			name := n.routine.Name
			for _, a := range aggs {
				for _, f := range []string{a.SFunc, a.FinalFunc, a.CombineFunc, a.SerialFunc} {
					if f != "" && strings.EqualFold(depTypeBaseName(f), name) {
						out = append(out, aggNode(a))
						break
					}
				}
			}
		}
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if a.oid != b.oid {
				return a.oid > b.oid
			}
			if a.classID != b.classID {
				return a.classID < b.classID
			}
			return a.subID < b.subID
		})
		return out
	}

	var targets []typeDepTarget
	visited := map[*typeDepNode]bool{root: true}
	var visit func(n, from *typeDepNode)
	visit = func(n, from *typeDepNode) {
		for _, d := range dependents(n) {
			if visited[d] {
				continue
			}
			visited[d] = true
			visit(d, n)
		}
		if n != root {
			targets = append(targets, typeDepTarget{node: n, dependee: from})
		}
	}
	visit(root, nil)
	return targets
}

// typeBaseName strips a schema qualifier.
func depTypeBaseName(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

// routineDependencyDesc is getObjectDescription's `name(argtypes)` for a
// function: format_type names, `[]` for arrays, OUT arguments omitted.
func routineDependencyDesc(r *catalog.Routine) string {
	parts := make([]string, 0, len(r.ArgTypes))
	for i, t := range r.ArgTypes {
		if i < len(r.ArgModes) && r.ArgModes[i] == "o" {
			continue
		}
		parts = append(parts, dependencyTypeName(t.Name, t.IsArray))
	}
	return strings.ToLower(r.Name) + "(" + strings.Join(parts, ",") + ")"
}

// aggregateDependencyDesc is the same description for an aggregate's
// pg_proc row.
func aggregateDependencyDesc(a *catalog.UserAggregate) string {
	parts := make([]string, 0, len(a.ArgTypes))
	for _, t := range a.ArgTypes {
		parts = append(parts, dependencyTypeName(strings.TrimSuffix(t, "[]"), strings.HasSuffix(t, "[]")))
	}
	return strings.ToLower(a.Name) + "(" + strings.Join(parts, ",") + ")"
}

func dependencyTypeName(name string, isArray bool) string {
	canon := dropCompatCanonicalType(name)
	if canon == "" {
		canon = strings.ToLower(depTypeBaseName(name))
	}
	if isArray {
		canon += "[]"
	}
	return canon
}

// dropTypeDependents applies reportDependentObjects to DROP TYPE: RESTRICT
// with a reportable dependent is 2BP01; CASCADE reports them (one NOTICE, or
// a count with a DETAIL list) and drops them in deletion order.
func (o *ddlOp) dropTypeDependents(typeName string, oid, arrayOID uint32, cascade bool, pos int) error {
	targets := typeDependentObjects(o.ctx.Catalog, o.ctx.CurrentDatabaseOid, typeName, oid, arrayOID)
	var reported []typeDepTarget
	for i := len(targets) - 1; i >= 0; i-- {
		if !targets[i].node.internal {
			reported = append(reported, targets[i])
		}
	}
	if len(reported) == 0 {
		return nil
	}
	if !cascade {
		lines := make([]string, len(reported))
		for i, t := range reported {
			lines[i] = t.node.desc + " depends on " + t.dependee.desc
		}
		return &ExecError{Code: "2BP01", Pos: pos,
			Message: fmt.Sprintf("cannot drop type %s because other objects depend on it", typeName),
			Detail:  strings.Join(lines, "\n"),
			Hint:    "Use DROP ... CASCADE to drop the dependent objects too."}
	}
	if len(reported) == 1 {
		o.ctx.AddNotice("drop cascades to " + reported[0].node.desc)
	} else {
		lines := make([]string, len(reported))
		for i, t := range reported {
			lines[i] = "drop cascades to " + t.node.desc
		}
		o.ctx.AddNoticeWithDetail(fmt.Sprintf("drop cascades to %d other objects", len(reported)), strings.Join(lines, "\n"))
	}
	im, _ := o.ctx.Catalog.(*catalog.InMemory)
	rs := routineRegistry(o.ctx)
	for _, t := range targets {
		switch t.node.kind {
		case "aggregate":
			if im != nil && im.DropUserAggregate(t.node.agg.Name) && t.node.agg.OID != 0 {
				deleteAggregateCatalogRows(o.ctx, t.node.agg.OID)
			}
		case "table":
			name := parser.ObjectName{Schema: t.node.table.Schema, Name: t.node.table.Name}
			if err := o.dropTableByRef(name, t.node.table, false); err != nil {
				return err
			}
		case "column":
			// The column, not its table (ATExecDropColumn via
			// doDeletion's RemoveAttributeById). A table dropped earlier
			// in this walk takes its columns with it.
			tbl := t.node.table
			if im == nil || !tableStillRegistered(im, o.ctx.CurrentDatabaseOid, tbl) {
				continue
			}
			act := parser.AlterTableAction{Kind: parser.AlterTableDropColumn, ColumnName: t.node.column}
			if err := o.dropColumn(tbl, act, false, true); err != nil {
				return err
			}
		case "attribute":
			if im == nil {
				continue
			}
			if err := o.dropCompositeAttribute(im, t.node.comp, t.node.column); err != nil {
				return err
			}
		case "domain":
			if im == nil {
				continue
			}
			o.dropDomainObject(im, t.node.domain)
		case "function":
			if rs == nil {
				continue
			}
			if err := rs.DropRoutine(t.node.routine); err == nil {
				if werr := o.walLogDropRoutine(t.node.routine.OID); werr != nil {
					return werr
				}
			}
		}
	}
	return nil
}

// tableStillRegistered reports whether tbl is still a live relation of the
// database (a cascade may have dropped it before reaching its column).
func tableStillRegistered(im *catalog.InMemory, dbOid uint32, tbl *catalog.Table) bool {
	for _, t := range im.UserTableHandles(catalog.NamespaceDBOid(dbOid)) {
		if t == tbl {
			return true
		}
	}
	return false
}

// dropCompositeAttribute removes attribute name from composite type ct, as
// ALTER TYPE … DROP ATTRIBUTE does (execAlterTypeAttrCmds' re-sync).
func (o *ddlOp) dropCompositeAttribute(cat *catalog.InMemory, ct *catalog.CompositeType, name string) error {
	fields := make([]catalog.CompositeField, 0, len(ct.Fields))
	for _, f := range ct.Fields {
		if !strings.EqualFold(f.Name, name) {
			fields = append(fields, f)
		}
	}
	o.replaceCompositeFields(cat, ct, fields)
	return nil
}

// dropDomainObject removes a domain whose dependents are already gone: its
// pg_type rows (the domain and its array) and CHECK constraint rows are
// stamped deleted, then the catalog entry is removed.
func (o *ddlOp) dropDomainObject(cat *catalog.InMemory, d *catalog.Domain) {
	if catalogHeapSyncAvailable(o.ctx) && o.ctx.MaterializeWriterXID() == nil {
		deleteTypeFromCatalogHeap(o.ctx, tableCatalogHeapDBOid(o.ctx), d.OID, o.ctx.Tx.XID)
		if d.ArrayOID != 0 {
			deleteTypeFromCatalogHeap(o.ctx, tableCatalogHeapDBOid(o.ctx), d.ArrayOID, o.ctx.Tx.XID)
		}
		for _, chk := range d.Checks {
			deleteConstraintRowByOID(o.ctx, chk.OID, o.ctx.Tx.XID)
		}
		mirrorTypeCatalogFiles(o.ctx)
		mirrorConstraintCatalogFiles(o.ctx)
	}
	_, _ = cat.DropDomain(d.Name, true, false, o.ctx.CurrentDatabaseOid)
}
