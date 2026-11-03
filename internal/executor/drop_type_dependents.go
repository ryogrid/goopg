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

type typeDepNode struct {
	kind     string // "type", "arraytype", "function", "aggregate", "table"
	oid      uint32
	desc     string
	routine  *catalog.Routine
	agg      *catalog.UserAggregate
	table    *catalog.Table
	internal bool
}

type typeDepTarget struct {
	node     *typeDepNode
	dependee *typeDepNode
}

// typeDependentObjects returns the objects DROP TYPE of the type (oid,
// arrayOID, display name) must drop, in deletion order (dependents first),
// each with the object it was reached from. The type itself and its array
// type are not included.
func typeDependentObjects(cat catalog.Catalog, typeName string, oid, arrayOID uint32) []typeDepTarget {
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		return nil
	}
	var routines []*catalog.Routine
	if rs := cat.Routines(); rs != nil {
		routines = rs.List()
	}
	aggs := im.ListUserAggregates()

	matches := func(t catalog.Type, tOID uint32, wantArray bool) bool {
		if tOID != 0 {
			if wantArray {
				return arrayOID != 0 && tOID == arrayOID
			}
			return oid != 0 && tOID == oid
		}
		name, isArray := depTypeBaseName(t.Name), t.IsArray
		if strings.HasSuffix(name, "[]") {
			name, isArray = strings.TrimSuffix(name, "[]"), true
		} else if strings.HasPrefix(name, "_") && strings.EqualFold(name[1:], typeName) {
			name, isArray = name[1:], true
		}
		return isArray == wantArray && strings.EqualFold(name, typeName)
	}
	matchesName := func(name string, wantArray bool) bool {
		isArray := strings.HasSuffix(name, "[]")
		return isArray == wantArray && strings.EqualFold(depTypeBaseName(strings.TrimSuffix(name, "[]")), typeName)
	}
	routineUses := func(r *catalog.Routine, wantArray bool) bool {
		for i, t := range r.ArgTypes {
			var tOID uint32
			if i < len(r.ArgTypeOIDs) {
				tOID = r.ArgTypeOIDs[i]
			}
			if matches(t, tOID, wantArray) {
				return true
			}
		}
		return matches(r.ReturnType, r.ReturnTypeOID, wantArray)
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
	aggUses := func(a *catalog.UserAggregate, wantArray bool) bool {
		for _, t := range a.ArgTypes {
			if matchesName(t, wantArray) {
				return true
			}
		}
		return matchesName(aggResultType(a), wantArray)
	}

	nodes := map[string]*typeDepNode{}
	node := func(kind string, key uint32, mk func() *typeDepNode) *typeDepNode {
		k := fmt.Sprintf("%s/%d", kind, key)
		if n, ok := nodes[k]; ok {
			return n
		}
		n := mk()
		nodes[k] = n
		return n
	}
	fnNode := func(r *catalog.Routine) *typeDepNode {
		return node("function", r.OID, func() *typeDepNode {
			return &typeDepNode{kind: "function", oid: r.OID, desc: "function " + routineDependencyDesc(r), routine: r}
		})
	}
	aggNode := func(a *catalog.UserAggregate) *typeDepNode {
		return node("aggregate", a.OID, func() *typeDepNode {
			return &typeDepNode{kind: "aggregate", oid: a.OID, desc: "function " + aggregateDependencyDesc(a), agg: a}
		})
	}
	root := &typeDepNode{kind: "type", oid: oid, desc: "type " + typeName}
	arr := &typeDepNode{kind: "arraytype", oid: arrayOID, desc: "type " + typeName + "[]", internal: true}

	dependents := func(n *typeDepNode) []*typeDepNode {
		var out []*typeDepNode
		switch n.kind {
		case "type", "arraytype":
			wantArray := n.kind == "arraytype"
			if n.kind == "type" && (arrayOID != 0 || oid == 0) {
				out = append(out, arr)
			}
			for _, r := range routines {
				if routineUses(r, wantArray) {
					out = append(out, fnNode(r))
				}
			}
			for _, a := range aggs {
				if aggUses(a, wantArray) {
					out = append(out, aggNode(a))
				}
			}
			// A typed table (`CREATE TABLE … OF type`) depends on its
			// type (heap_create_with_catalog records reloftype).
			if !wantArray && oid != 0 {
				for _, tbl := range im.AllTables() {
					if tbl.OfTypeOID == oid {
						tb := tbl
						out = append(out, node("table", tb.OID, func() *typeDepNode {
							return &typeDepNode{kind: "table", oid: tb.OID, desc: "table " + tb.Name, table: tb}
						}))
					}
				}
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
		sort.SliceStable(out, func(i, j int) bool { return out[i].oid > out[j].oid })
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
	targets := typeDependentObjects(o.ctx.Catalog, typeName, oid, arrayOID)
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
