package optimizer

import "github.com/goopg/goopg/internal/parser"

// M0146-0112 — a dummy UNION ALL member is dropped from the Append.
//
// PG plans a UNION ALL as an Append over its members and skips every member
// whose rel is dummy (set_append_rel_pathlist / generate_union_paths ignore
// is_dummy_rel children). With one member left the Append itself is removed
// (a single-child Append is elided by setrefs), and with none left the whole
// set operation is a dummy rel — a childless `Result  One-Time Filter:
// false`. A member is dummy when its WHERE is constant false (M0146-0111):
//
//	select a from f1 where false union all select a from f2
//	PG:     Seq Scan on f2
//	goopg:  Append -> Result (One-Time Filter: false), Seq Scan on f2
//
// A UNION (distinct) keeps its dummy member under the Append, as PG's
// distinct path does (`HashAggregate -> Append -> Result, Seq Scan`), so
// only the UNION ALL links outside a distinct step are pruned.

// pruneDummyUnionAllArms rewrites the UNION ALL links of a planned set
// operation tree, dropping members that produce no rows. The surviving
// member keeps the link's output column names through a renaming Project
// (never printed), because the set operation's columns are named by its
// first member.
func pruneDummyUnionAllArms(n Node) Node {
	s, ok := n.(*SetOp)
	if !ok || s.Op != parser.SetOpUnion || !s.All || s.UnionDistinctInput || s.Left == nil || s.Right == nil {
		return n
	}
	left, right := pruneDummyUnionAllArms(s.Left), pruneDummyUnionAllArms(s.Right)
	ld, rd := isDummyRelNode(left), isDummyRelNode(right)
	switch {
	case ld && rd:
		schema := s.Output()
		return &Result{pos: s.pos, Targets: identityResultTargets(schema),
			OneTimeFilter: &BooleanConst{pos: s.pos, Value: false}, schema: schema}
	case ld:
		return renameSetOpMember(right, s.Output(), s.pos)
	case rd:
		return renameSetOpMember(left, s.Output(), s.pos)
	}
	if left == s.Left && right == s.Right {
		return s
	}
	cp := *s
	cp.Left, cp.Right = left, right
	return &cp
}

// isDummyRelNode reports whether n produces no rows by construction: a
// childless Result whose one-time filter is constant false (PG's dummy rel),
// possibly under the projection and label wrappers a member carries.
func isDummyRelNode(n Node) bool {
	for {
		switch x := n.(type) {
		case *Project:
			n = x.Child
		case *SubqueryScan:
			n = x.Child
		case *Result:
			if x.Child != nil {
				return false
			}
			b, ok := x.OneTimeFilter.(*BooleanConst)
			return ok && !b.Value
		default:
			return false
		}
	}
}

// renameSetOpMember returns member publishing schema's column names: the
// surviving member of a pruned UNION ALL link stands for the link, whose
// columns are named by its first member.
func renameSetOpMember(member Node, schema Schema, pos int) Node {
	out := member.Output()
	if len(out) != len(schema) {
		return member
	}
	same := true
	for i := range out {
		if out[i].Name != schema[i].Name {
			same = false
			break
		}
	}
	if same {
		return member
	}
	targets := make([]Expr, len(out))
	for i, c := range out {
		targets[i] = &ColumnRef{pos: pos, Index: i, Name: c.Name, Type: c.Type, SourceTableIdx: c.SourceTableIdx}
	}
	renamed := make(Schema, len(schema))
	copy(renamed, schema)
	return &Project{pos: pos, Child: member, Targets: targets, schema: renamed}
}
