package nodes

import (
	"fmt"
	"strconv"
)

// ReadRuleAction parses a pg_rewrite.ev_action value ("({QUERY ...})") back into
// its list of query trees. It is the inverse of OutRuleAction. Any tag or field
// outside the supported single-base-relation view shape is reported as an error,
// which callers use as their "not canonical, keep the SQL text" signal.
func ReadRuleAction(s string) ([]Node, error) {
	t := &tokenizer{s: s}
	open, ok := t.next()
	if !ok || open != "(" {
		return nil, fmt.Errorf("pgnodes: expected '(' to start rule action, got %q", open)
	}
	var nodes []Node
	for {
		p, ok := t.peek()
		if !ok {
			return nil, fmt.Errorf("pgnodes: unterminated rule action list")
		}
		if p == ")" {
			t.next()
			break
		}
		n, err := readNode(t)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	if tok, ok := t.next(); ok {
		return nil, fmt.Errorf("pgnodes: trailing token %q after rule action", tok)
	}
	return nodes, nil
}

// --- additional READ_* field helpers ---

// readStringField mirrors READ_STRING_FIELD: "<>" -> "", otherwise the token is
// de-escaped (the inverse of outToken).
func readStringField(t *tokenizer) (string, error) {
	t.skipField()
	tok, ok := t.next()
	if !ok {
		return "", fmt.Errorf("pgnodes: expected string value")
	}
	if tok == "<>" {
		return "", nil
	}
	return unToken(tok), nil
}

// readCharField mirrors READ_CHAR_FIELD for the printable-ASCII chars goopg
// emits (relkind etc.).
func readCharField(t *tokenizer) (byte, error) {
	t.skipField()
	tok, ok := t.next()
	if !ok || len(tok) == 0 {
		return 0, fmt.Errorf("pgnodes: expected char value")
	}
	return tok[0], nil
}

func readUint64Field(t *tokenizer) (uint64, error) {
	t.skipField()
	tok, ok := t.next()
	if !ok {
		return 0, fmt.Errorf("pgnodes: expected uint64 value")
	}
	v, err := strconv.ParseUint(tok, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("pgnodes: bad uint64 %q: %w", tok, err)
	}
	return v, nil
}

// readBitmapsetField mirrors READ_BITMAPSET_FIELD: "(b m0 m1 ...)" -> members,
// "(b)" -> empty.
func readBitmapsetField(t *tokenizer) (Bitmapset, error) {
	t.skipField()
	if open, ok := t.next(); !ok || open != "(" {
		return nil, fmt.Errorf("pgnodes: expected '(' to start bitmapset, got %q", open)
	}
	if b, ok := t.next(); !ok || b != "b" {
		return nil, fmt.Errorf("pgnodes: expected 'b' bitmapset marker, got %q", b)
	}
	var bms Bitmapset
	for {
		p, ok := t.peek()
		if !ok {
			return nil, fmt.Errorf("pgnodes: unterminated bitmapset")
		}
		if p == ")" {
			t.next()
			break
		}
		tok, _ := t.next()
		v, err := strconv.ParseInt(tok, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("pgnodes: bad bitmapset member %q: %w", tok, err)
		}
		bms = append(bms, int32(v))
	}
	return bms, nil
}

// readStringListField parses a List of String value nodes: "(\"a\" \"b\")" ->
// []string{"a","b"}, "<>" -> nil.
func readStringListField(t *tokenizer) ([]string, error) {
	t.skipField()
	tok, ok := t.next()
	if !ok {
		return nil, fmt.Errorf("pgnodes: expected string list")
	}
	if tok == "<>" {
		return nil, nil
	}
	if tok != "(" {
		return nil, fmt.Errorf("pgnodes: expected '(' to start string list, got %q", tok)
	}
	var ss []string
	for {
		p, ok := t.peek()
		if !ok {
			return nil, fmt.Errorf("pgnodes: unterminated string list")
		}
		if p == ")" {
			t.next()
			break
		}
		el, _ := t.next()
		ss = append(ss, unquoteStringNode(el))
	}
	return ss, nil
}

// unToken reverses outToken's backslash escaping.
func unToken(s string) string {
	if s == "" {
		return ""
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		out = append(out, s[i])
	}
	return string(out)
}

// unquoteStringNode strips the surrounding double quotes of a serialized String
// value node ("name" -> name) and de-escapes the interior.
func unquoteStringNode(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return unToken(s)
}

// --- Query shape-gate validators. These read a fixed field and error if it is
// not at its supported view-default value; the error is the caller's fallback
// signal. ---

func mustInt(t *tokenizer, name string, want int32) error {
	v, err := readInt32(t)
	if err != nil {
		return err
	}
	if v != want {
		return fmt.Errorf("pgnodes: unsupported Query.%s=%d (want %d)", name, v, want)
	}
	return nil
}

func mustBool(t *tokenizer, name string, want bool) error {
	v, err := readBool(t)
	if err != nil {
		return err
	}
	if v != want {
		return fmt.Errorf("pgnodes: unsupported Query.%s=%v (want %v)", name, v, want)
	}
	return nil
}

func mustNilNode(t *tokenizer, name string) error {
	n, err := readNodeField(t)
	if err != nil {
		return err
	}
	if n != nil {
		return fmt.Errorf("pgnodes: unsupported non-nil Query.%s", name)
	}
	return nil
}

func mustEmptyList(t *tokenizer, name string) error {
	l, err := readNodeListField(t)
	if err != nil {
		return err
	}
	if len(l) != 0 {
		return fmt.Errorf("pgnodes: unsupported non-empty Query.%s", name)
	}
	return nil
}

func mustEmptyString(t *tokenizer, name string) error {
	s, err := readStringField(t)
	if err != nil {
		return err
	}
	if s != "" {
		return fmt.Errorf("pgnodes: unsupported non-empty Query.%s=%q", name, s)
	}
	return nil
}

// --- per-tag read functions (field order mirrors the out functions) ---

// readQuery mirrors _readQuery for the supported view shape and validates every
// fixed field along the way.
func readQuery(t *tokenizer) (*Query, error) {
	q := &Query{}
	var err error
	if q.CommandType, err = readInt32(t); err != nil { // commandType
		return nil, err
	}
	for _, f := range []struct {
		name string
		fn   func(*tokenizer, string) error
	}{
		{"querySource", func(t *tokenizer, n string) error { return mustInt(t, n, 0) }},
		{"canSetTag", func(t *tokenizer, n string) error { return mustBool(t, n, true) }},
		{"utilityStmt", mustNilNode},
		{"resultRelation", func(t *tokenizer, n string) error { return mustInt(t, n, 0) }},
		{"hasAggs", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"hasWindowFuncs", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"hasTargetSRFs", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"hasSubLinks", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"hasDistinctOn", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"hasRecursive", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"hasModifyingCTE", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"hasForUpdate", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"hasRowSecurity", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"hasGroupRTE", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"isReturn", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"cteList", mustEmptyList},
	} {
		if err = f.fn(t, f.name); err != nil {
			return nil, err
		}
	}
	if q.Rtable, err = readNodeListField(t); err != nil { // rtable
		return nil, err
	}
	if q.RtePermInfos, err = readNodeListField(t); err != nil { // rteperminfos
		return nil, err
	}
	if q.Jointree, err = readNodeField(t); err != nil { // jointree
		return nil, err
	}
	if err = mustEmptyList(t, "mergeActionList"); err != nil {
		return nil, err
	}
	if err = mustInt(t, "mergeTargetRelation", 0); err != nil {
		return nil, err
	}
	if err = mustNilNode(t, "mergeJoinCondition"); err != nil {
		return nil, err
	}
	if q.TargetList, err = readNodeListField(t); err != nil { // targetList
		return nil, err
	}
	for _, f := range []struct {
		name string
		fn   func(*tokenizer, string) error
	}{
		{"override", func(t *tokenizer, n string) error { return mustInt(t, n, 0) }},
		{"onConflict", mustNilNode},
		{"returningOldAlias", mustEmptyString},
		{"returningNewAlias", mustEmptyString},
		{"returningList", mustEmptyList},
		{"groupClause", mustEmptyList},
		{"groupDistinct", func(t *tokenizer, n string) error { return mustBool(t, n, false) }},
		{"groupingSets", mustEmptyList},
		{"havingQual", mustNilNode},
		{"windowClause", mustEmptyList},
		{"distinctClause", mustEmptyList},
		{"sortClause", mustEmptyList},
		{"limitOffset", mustNilNode},
		{"limitCount", mustNilNode},
		{"limitOption", func(t *tokenizer, n string) error { return mustInt(t, n, 0) }},
		{"rowMarks", mustEmptyList},
		{"setOperations", mustNilNode},
		{"constraintDeps", mustEmptyList},
		{"withCheckOptions", mustEmptyList},
		{"stmt_location", func(t *tokenizer, n string) error { return mustInt(t, n, -1) }},
		{"stmt_len", func(t *tokenizer, n string) error { return mustInt(t, n, -1) }},
	} {
		if err = f.fn(t, f.name); err != nil {
			return nil, err
		}
	}
	return q, nil
}

func readRangeTblEntry(t *tokenizer) (*RangeTblEntry, error) {
	r := &RangeTblEntry{}
	var err error
	if r.Alias, err = readNodeField(t); err != nil { // alias
		return nil, err
	}
	if r.Eref, err = readNodeField(t); err != nil { // eref
		return nil, err
	}
	if r.Rtekind, err = readInt32(t); err != nil {
		return nil, err
	}
	if r.Rtekind != 0 {
		return nil, fmt.Errorf("pgnodes: unsupported RangeTblEntry.rtekind=%d (only RTE_RELATION)", r.Rtekind)
	}
	if r.Relid, err = readUint32(t); err != nil {
		return nil, err
	}
	if r.Inh, err = readBool(t); err != nil {
		return nil, err
	}
	if r.Relkind, err = readCharField(t); err != nil {
		return nil, err
	}
	if r.Rellockmode, err = readInt32(t); err != nil {
		return nil, err
	}
	if r.Perminfoindex, err = readInt32(t); err != nil {
		return nil, err
	}
	if ts, err := readNodeField(t); err != nil { // tablesample
		return nil, err
	} else if ts != nil {
		return nil, fmt.Errorf("pgnodes: unsupported RangeTblEntry.tablesample")
	}
	if r.Lateral, err = readBool(t); err != nil {
		return nil, err
	}
	if r.InFromCl, err = readBool(t); err != nil {
		return nil, err
	}
	if sq, err := readNodeListField(t); err != nil { // securityQuals
		return nil, err
	} else if len(sq) != 0 {
		return nil, fmt.Errorf("pgnodes: unsupported RangeTblEntry.securityQuals")
	}
	return r, nil
}

func readRTEPermissionInfo(t *tokenizer) (*RTEPermissionInfo, error) {
	r := &RTEPermissionInfo{}
	var err error
	if r.Relid, err = readUint32(t); err != nil {
		return nil, err
	}
	if r.Inh, err = readBool(t); err != nil {
		return nil, err
	}
	if r.RequiredPerms, err = readUint64Field(t); err != nil {
		return nil, err
	}
	if r.CheckAsUser, err = readUint32(t); err != nil {
		return nil, err
	}
	if r.SelectedCols, err = readBitmapsetField(t); err != nil {
		return nil, err
	}
	if r.InsertedCols, err = readBitmapsetField(t); err != nil {
		return nil, err
	}
	if r.UpdatedCols, err = readBitmapsetField(t); err != nil {
		return nil, err
	}
	return r, nil
}

func readFromExpr(t *tokenizer) (*FromExpr, error) {
	f := &FromExpr{}
	var err error
	if f.Fromlist, err = readNodeListField(t); err != nil {
		return nil, err
	}
	if f.Quals, err = readNodeField(t); err != nil {
		return nil, err
	}
	return f, nil
}

func readRangeTblRef(t *tokenizer) (*RangeTblRef, error) {
	r := &RangeTblRef{}
	var err error
	if r.Rtindex, err = readInt32(t); err != nil {
		return nil, err
	}
	return r, nil
}

func readTargetEntry(t *tokenizer) (*TargetEntry, error) {
	te := &TargetEntry{}
	var err error
	if te.Expr, err = readNodeField(t); err != nil {
		return nil, err
	}
	if te.Resno, err = readInt32(t); err != nil {
		return nil, err
	}
	if te.Resname, err = readStringField(t); err != nil {
		return nil, err
	}
	if te.Ressortgroupref, err = readInt32(t); err != nil {
		return nil, err
	}
	if te.Resorigtbl, err = readUint32(t); err != nil {
		return nil, err
	}
	if te.Resorigcol, err = readInt32(t); err != nil {
		return nil, err
	}
	if te.Resjunk, err = readBool(t); err != nil {
		return nil, err
	}
	return te, nil
}

func readVar(t *tokenizer) (*Var, error) {
	v := &Var{}
	var err error
	if v.Varno, err = readInt32(t); err != nil {
		return nil, err
	}
	if v.Varattno, err = readInt32(t); err != nil {
		return nil, err
	}
	if v.Vartype, err = readUint32(t); err != nil {
		return nil, err
	}
	if v.Vartypmod, err = readInt32(t); err != nil {
		return nil, err
	}
	if v.Varcollid, err = readUint32(t); err != nil {
		return nil, err
	}
	if v.Varnullingrels, err = readBitmapsetField(t); err != nil {
		return nil, err
	}
	if v.Varlevelsup, err = readInt32(t); err != nil {
		return nil, err
	}
	if v.Varreturningtype, err = readInt32(t); err != nil {
		return nil, err
	}
	if v.Varnosyn, err = readInt32(t); err != nil {
		return nil, err
	}
	if v.Varattnosyn, err = readInt32(t); err != nil {
		return nil, err
	}
	if v.Location, err = readInt32(t); err != nil {
		return nil, err
	}
	return v, nil
}

func readAlias(t *tokenizer) (*Alias, error) {
	a := &Alias{}
	var err error
	if a.Aliasname, err = readStringField(t); err != nil {
		return nil, err
	}
	if a.Colnames, err = readStringListField(t); err != nil {
		return nil, err
	}
	return a, nil
}
