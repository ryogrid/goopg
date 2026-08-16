package nodes

import (
	"strconv"
	"strings"
)

// OutRuleAction serializes a pg_rewrite.ev_action value: a List of query trees
// (for a view there is exactly one). PostgreSQL stores ev_action as
// nodeToString(list_of_Query), i.e. "({QUERY ...})", so this writes the outer
// "(...)" list wrapper directly rather than a single braced node.
func OutRuleAction(nodes []Node) string {
	var sb strings.Builder
	sb.WriteByte('(')
	for i, n := range nodes {
		if i > 0 {
			sb.WriteByte(' ')
		}
		outNode(&sb, n)
	}
	sb.WriteByte(')')
	return sb.String()
}

// --- additional WRITE_* field helpers (outfuncs.c macros) ---

// wString mirrors WRITE_STRING_FIELD: a NULL/empty char* prints "<>", otherwise
// the value is written via outToken (which escapes only when necessary).
func wString(sb *strings.Builder, name, s string) {
	sb.WriteString(" :")
	sb.WriteString(name)
	sb.WriteByte(' ')
	if s == "" {
		sb.WriteString("<>")
		return
	}
	outToken(sb, s)
}

// wChar mirrors WRITE_CHAR_FIELD. The catalog chars goopg emits (relkind etc.)
// are always printable ASCII letters, which outfuncs writes verbatim.
func wChar(sb *strings.Builder, name string, c byte) {
	sb.WriteString(" :")
	sb.WriteString(name)
	sb.WriteByte(' ')
	sb.WriteByte(c)
}

// wUint64 mirrors WRITE_UINT64_FIELD.
func wUint64(sb *strings.Builder, name string, v uint64) {
	sb.WriteString(" :")
	sb.WriteString(name)
	sb.WriteByte(' ')
	sb.WriteString(strconv.FormatUint(v, 10))
}

// wBitmapset mirrors WRITE_BITMAPSET_FIELD / outBitmapset: "(b m0 m1 ...)", or
// "(b)" for an empty set.
func wBitmapset(sb *strings.Builder, name string, bms Bitmapset) {
	sb.WriteString(" :")
	sb.WriteString(name)
	sb.WriteString(" (b")
	for _, m := range bms {
		sb.WriteByte(' ')
		sb.WriteString(strconv.FormatInt(int64(m), 10))
	}
	sb.WriteByte(')')
}

// wStringList mirrors a WRITE_NODE_FIELD holding a List of String value nodes
// (e.g. Alias.colnames). Each element is written quoted as "name" — the form
// outNode uses for a T_String node — and an empty list prints "<>".
func wStringList(sb *strings.Builder, name string, ss []string) {
	sb.WriteString(" :")
	sb.WriteString(name)
	sb.WriteByte(' ')
	if len(ss) == 0 {
		sb.WriteString("<>")
		return
	}
	sb.WriteByte('(')
	for i, s := range ss {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteByte('"')
		outToken(sb, s)
		sb.WriteByte('"')
	}
	sb.WriteByte(')')
}

// outToken is a Go port of outfuncs.c:outToken. It escapes the characters
// PostgreSQL's tokenizer treats specially so the value reads back as a single
// token. A leading special/digit char and any embedded special char are
// backslash-escaped; the empty string prints "<>".
func outToken(sb *strings.Builder, s string) {
	if s == "" {
		sb.WriteString("<>")
		return
	}
	// A token whose first char could be misread (a node marker, a quote, a
	// number, or a signed number) is prefixed with a backslash.
	c0 := s[0]
	if c0 == '<' || c0 == '"' || (c0 >= '0' && c0 <= '9') ||
		((c0 == '+' || c0 == '-') && len(s) > 1 && ((s[1] >= '0' && s[1] <= '9') || s[1] == '.')) {
		sb.WriteByte('\\')
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\n' || c == '\t' || c == '(' || c == ')' ||
			c == '{' || c == '}' || c == '\\' {
			sb.WriteByte('\\')
		}
		sb.WriteByte(c)
	}
}

// --- per-tag out functions (field order mirrors outfuncs.c EXACTLY) ---

// outQuery mirrors _outQuery. Only commandType/rtable/rteperminfos/jointree/
// targetList carry data for the supported view shape; the remaining fields are
// emitted at their fixed view-default values so the byte stream matches PG.
func outQuery(sb *strings.Builder, q *Query) {
	sb.WriteString("QUERY")
	wInt(sb, "commandType", q.CommandType)
	wInt(sb, "querySource", 0)
	wBool(sb, "canSetTag", true)
	wNode(sb, "utilityStmt", nil)
	wInt(sb, "resultRelation", 0)
	wBool(sb, "hasAggs", false)
	wBool(sb, "hasWindowFuncs", false)
	wBool(sb, "hasTargetSRFs", false)
	wBool(sb, "hasSubLinks", false)
	wBool(sb, "hasDistinctOn", false)
	wBool(sb, "hasRecursive", false)
	wBool(sb, "hasModifyingCTE", false)
	wBool(sb, "hasForUpdate", false)
	wBool(sb, "hasRowSecurity", false)
	wBool(sb, "hasGroupRTE", false)
	wBool(sb, "isReturn", false)
	wNodeList(sb, "cteList", nil)
	wNodeList(sb, "rtable", q.Rtable)
	wNodeList(sb, "rteperminfos", q.RtePermInfos)
	wNode(sb, "jointree", q.Jointree)
	wNodeList(sb, "mergeActionList", nil)
	wInt(sb, "mergeTargetRelation", 0)
	wNode(sb, "mergeJoinCondition", nil)
	wNodeList(sb, "targetList", q.TargetList)
	wInt(sb, "override", 0)
	wNode(sb, "onConflict", nil)
	wString(sb, "returningOldAlias", "")
	wString(sb, "returningNewAlias", "")
	wNodeList(sb, "returningList", nil)
	wNodeList(sb, "groupClause", nil)
	wBool(sb, "groupDistinct", false)
	wNodeList(sb, "groupingSets", nil)
	wNode(sb, "havingQual", nil)
	wNodeList(sb, "windowClause", nil)
	wNodeList(sb, "distinctClause", nil)
	wNodeList(sb, "sortClause", nil)
	wNode(sb, "limitOffset", nil)
	wNode(sb, "limitCount", nil)
	wInt(sb, "limitOption", 0)
	wNodeList(sb, "rowMarks", nil)
	wNode(sb, "setOperations", nil)
	wNodeList(sb, "constraintDeps", nil)
	wNodeList(sb, "withCheckOptions", nil)
	wInt(sb, "stmt_location", -1)
	wInt(sb, "stmt_len", -1)
}

// outRangeTblEntry mirrors _outRangeTblEntry for RTE_RELATION: alias, eref,
// rtekind, relid, inh, relkind, rellockmode, perminfoindex, tablesample,
// lateral, inFromCl, securityQuals.
func outRangeTblEntry(sb *strings.Builder, r *RangeTblEntry) {
	sb.WriteString("RANGETBLENTRY")
	wNode(sb, "alias", r.Alias)
	wNode(sb, "eref", r.Eref)
	wInt(sb, "rtekind", r.Rtekind)
	wOid(sb, "relid", r.Relid)
	wBool(sb, "inh", r.Inh)
	wChar(sb, "relkind", r.Relkind)
	wInt(sb, "rellockmode", r.Rellockmode)
	wInt(sb, "perminfoindex", r.Perminfoindex)
	wNode(sb, "tablesample", nil)
	wBool(sb, "lateral", r.Lateral)
	wBool(sb, "inFromCl", r.InFromCl)
	wNodeList(sb, "securityQuals", nil)
}

// outRTEPermissionInfo mirrors _outRTEPermissionInfo.
func outRTEPermissionInfo(sb *strings.Builder, r *RTEPermissionInfo) {
	sb.WriteString("RTEPERMISSIONINFO")
	wOid(sb, "relid", r.Relid)
	wBool(sb, "inh", r.Inh)
	wUint64(sb, "requiredPerms", r.RequiredPerms)
	wOid(sb, "checkAsUser", r.CheckAsUser)
	wBitmapset(sb, "selectedCols", r.SelectedCols)
	wBitmapset(sb, "insertedCols", r.InsertedCols)
	wBitmapset(sb, "updatedCols", r.UpdatedCols)
}

// outFromExpr mirrors _outFromExpr: fromlist, quals.
func outFromExpr(sb *strings.Builder, f *FromExpr) {
	sb.WriteString("FROMEXPR")
	wNodeList(sb, "fromlist", f.Fromlist)
	wNode(sb, "quals", f.Quals)
}

// outRangeTblRef mirrors _outRangeTblRef: rtindex.
func outRangeTblRef(sb *strings.Builder, r *RangeTblRef) {
	sb.WriteString("RANGETBLREF")
	wInt(sb, "rtindex", r.Rtindex)
}

// outTargetEntry mirrors _outTargetEntry.
func outTargetEntry(sb *strings.Builder, t *TargetEntry) {
	sb.WriteString("TARGETENTRY")
	wNode(sb, "expr", t.Expr)
	wInt(sb, "resno", t.Resno)
	wString(sb, "resname", t.Resname)
	wInt(sb, "ressortgroupref", t.Ressortgroupref)
	wOid(sb, "resorigtbl", t.Resorigtbl)
	wInt(sb, "resorigcol", t.Resorigcol)
	wBool(sb, "resjunk", t.Resjunk)
}

// outVar mirrors _outVar.
func outVar(sb *strings.Builder, v *Var) {
	sb.WriteString("VAR")
	wInt(sb, "varno", v.Varno)
	wInt(sb, "varattno", v.Varattno)
	wOid(sb, "vartype", v.Vartype)
	wInt(sb, "vartypmod", v.Vartypmod)
	wOid(sb, "varcollid", v.Varcollid)
	wBitmapset(sb, "varnullingrels", v.Varnullingrels)
	wInt(sb, "varlevelsup", v.Varlevelsup)
	wInt(sb, "varreturningtype", v.Varreturningtype)
	wInt(sb, "varnosyn", v.Varnosyn)
	wInt(sb, "varattnosyn", v.Varattnosyn)
	wLoc(sb, "location", v.Location)
}

// outAlias mirrors _outAlias: aliasname, colnames.
func outAlias(sb *strings.Builder, a *Alias) {
	sb.WriteString("ALIAS")
	wString(sb, "aliasname", a.Aliasname)
	wStringList(sb, "colnames", a.Colnames)
}
