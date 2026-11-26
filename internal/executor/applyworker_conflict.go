package executor

// applyworker_conflict.go — the apply worker's unique-key conflict detection
// and report (M0149-0056a, subscription/t/035_conflicts.pl).
//
// PG 18: the apply worker's result relation gets every immediate unique
// index as a conflict index (InitConflictIndexes). When an applied INSERT
// or UPDATE would violate one, CheckAndReportConflict finds the existing
// local tuple for EACH violated index (FindConflictTuple) and
// ReportApplyConflict raises
//
//	ERROR:  conflict detected on relation "s.t": conflict=insert_exists
//	        (update_exists for an UPDATE; multiple_unique_conflicts when
//	        more than one index is violated)
//	DETAIL:  Key already exists in unique index "i", modified in transaction X.
//	         Key (a)=(2); existing local row (2, 2, 2); remote row (2, 3, 4).
//	         ... one pair of lines per violated index ...
//
// (conflict.c errdetail_apply_conflict / build_tuple_value_details; the
// "modified locally ... at <ts>" form needs track_commit_timestamp, which
// goopg does not have). The error aborts the apply transaction and the
// worker restarts and retries, as in PG.

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// applyConflict is one violated unique index and the local row holding the key.
type applyConflict struct {
	idx   *catalog.Index
	local *applyFoundTuple
}

// applyConflictIndexes is InitConflictIndexes: tbl's immediate unique
// indexes in OID order.
func applyConflictIndexes(ctx *Context, tbl *catalog.Table) []*catalog.Index {
	var out []*catalog.Index
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		if (idx.Unique || idx.Primary) && idx.IsImmediate() {
			out = append(out, idx)
		}
	}
	sortIndexesByOID(out)
	return out
}

func sortIndexesByOID(idxs []*catalog.Index) {
	for i := 1; i < len(idxs); i++ {
		for j := i; j > 0 && idxs[j].OID < idxs[j-1].OID; j-- {
			idxs[j], idxs[j-1] = idxs[j-1], idxs[j]
		}
	}
}

// applyFindConflicts is CheckAndReportConflict's search: for each conflict
// index whose key the row fills (a NULL key column never conflicts), the
// visible local row with the same key, ignoring the tuple at skip.
func applyFindConflicts(ctx *Context, rel storage.RelFileNode, tbl *catalog.Table, cols []catalog.Column, row Row, skip *storage.ItemPointer) ([]applyConflict, error) {
	var out []applyConflict
	for _, idx := range applyConflictIndexes(ctx, tbl) {
		if !indexPredicateAdmits(ctx, idx, tbl, cols, row) {
			continue
		}
		key := make(Row, len(cols))
		for i := range key {
			key[i] = NullDatum
		}
		complete := len(idx.Columns) > 0
		for _, name := range idx.Columns {
			p := columnPosition(cols, name)
			if name == "" || p < 0 || p >= len(row) || row[p].IsNull() {
				complete = false
				break
			}
			key[p] = row[p]
		}
		if !complete {
			continue
		}
		found, err := applyFindTupleOpts(ctx, rel, tbl, cols, key, idx, skip, false)
		if err != nil {
			return nil, err
		}
		if found != nil {
			out = append(out, applyConflict{idx: idx, local: found})
		}
	}
	return out, nil
}

// applyConflictError is ReportApplyConflict's ERROR. kind is "insert_exists"
// or "update_exists"; several violated indexes make it
// multiple_unique_conflicts.
func applyConflictError(ctx *Context, tbl *catalog.Table, cols []catalog.Column, kind string, conflicts []applyConflict, remote Row) *ExecError {
	if len(conflicts) > 1 {
		kind = "multiple_unique_conflicts"
	}
	schema := tbl.Schema
	if schema == "" {
		schema = "public"
	}
	var detail strings.Builder
	for i, c := range conflicts {
		if i > 0 {
			detail.WriteByte('\n')
		}
		fmt.Fprintf(&detail, "Key already exists in unique index \"%s\", modified in transaction %d.", c.idx.Name, c.local.xmin)
		fmt.Fprintf(&detail, "\nKey %s; existing local row %s; remote row %s.",
			applyIndexValueDesc(ctx, c.idx, cols, c.local.row), applyRowValueDesc(ctx, cols, c.local.row), applyRowValueDesc(ctx, cols, remote))
	}
	return &ExecError{
		Code:    "23505",
		Message: fmt.Sprintf("conflict detected on relation \"%s.%s\": conflict=%s", schema, tbl.Name, kind),
		Detail:  detail.String(),
	}
}

// applyIndexValueDesc is BuildIndexValueDescription: "(a, b)=(1, 2)".
func applyIndexValueDesc(ctx *Context, idx *catalog.Index, cols []catalog.Column, row Row) string {
	names := make([]string, 0, len(idx.Columns))
	vals := make([]string, 0, len(idx.Columns))
	for _, name := range idx.Columns {
		names = append(names, name)
		v := "null"
		if p := columnPosition(cols, name); p >= 0 && p < len(row) && !row[p].IsNull() {
			v = detailValueText(ctx, cols[p].Type, row[p])
		}
		vals = append(vals, v)
	}
	return "(" + strings.Join(names, ", ") + ")=(" + strings.Join(vals, ", ") + ")"
}

// applyRowValueDesc is ExecBuildSlotValueDescription for a reader allowed to
// see every column: "(v1, v2, ...)", NULL as null, dropped columns omitted.
func applyRowValueDesc(ctx *Context, cols []catalog.Column, row Row) string {
	vals := make([]string, 0, len(cols))
	for i, c := range cols {
		if c.Dropped {
			continue
		}
		v := "null"
		if i < len(row) && !row[i].IsNull() {
			v = detailValueText(ctx, c.Type, row[i])
		}
		vals = append(vals, v)
	}
	return "(" + strings.Join(vals, ", ") + ")"
}

// isApplyConflictError reports a ReportApplyConflict error.
func isApplyConflictError(err error) (*ExecError, bool) {
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23505" || !strings.HasPrefix(ee.Message, "conflict detected on relation ") {
		return nil, false
	}
	return ee, true
}

// pgServerLogText renders an error as PG's server log does: the message,
// then "DETAIL:  " with each further detail line tab-indented
// (send_message_to_server_log, append_with_tabs).
func pgServerLogText(ee *ExecError) string {
	out := ee.Message
	if ee.Detail != "" {
		out += "\nDETAIL:  " + strings.ReplaceAll(ee.Detail, "\n", "\n\t")
	}
	return out
}
