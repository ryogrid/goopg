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
	"sync"

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

// replOrigin is the apply worker's replication origin id
// (replorigin_session_origin): nonzero for a subscription's worker, 0
// (InvalidRepOriginId) otherwise. goopg has no pg_replication_origin
// catalog; the id is derived from the subscription's OID and its name is
// PG's "pg_<suboid>". M0149-0061j.
func (w *ApplyWorker) replOrigin() uint16 {
	if w.pubsub == nil || w.subName == "" {
		return 0
	}
	sub, ok := w.pubsub.LookupSubscription(w.subName, w.dbOid())
	if !ok || sub == nil {
		return 0
	}
	id := uint16(sub.OID%65534) + 1
	replOriginNames.Store(id, fmt.Sprintf("pg_%d", sub.OID))
	return id
}

// replOriginNames maps a replication origin id to its name
// (replorigin_by_oid).
var replOriginNames sync.Map

// applyIdentityOrPK is GetRelationIdentityOrPK: the replica identity index,
// else the primary key; nil when there is neither.
func applyIdentityOrPK(ctx *Context, tbl *catalog.Table) *catalog.Index {
	idxs := ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if catalog.ReplIdentOrDefault(tbl.ReplicaIdentity)[0] == 'i' {
		for _, idx := range idxs {
			if idx.IsReplicaIdentity {
				return idx
			}
		}
	}
	for _, idx := range idxs {
		if idx.Primary {
			return idx
		}
	}
	return nil
}

// logApplyConflict is ReportApplyConflict at LOG level — the conflicts PG
// logs and resolves (update_missing / delete_missing skip the change,
// *_origin_differs apply it): the message, the conflict's detail line, then
// build_tuple_value_details' "Existing local row …; remote row …; replica
// identity …." line. M0149-0061e.
func (w *ApplyWorker) logApplyConflict(ctx *Context, tbl *catalog.Table, cols []catalog.Column, kind, head string, local, remote, search Row) {
	var parts []string
	add := func(lower, desc string) {
		if len(parts) == 0 {
			lower = strings.ToUpper(lower[:1]) + lower[1:]
		}
		parts = append(parts, lower+" "+desc)
	}
	if local != nil {
		add("existing local row", applyRowValueDesc(ctx, cols, local))
	}
	if remote != nil {
		add("remote row", applyRowValueDesc(ctx, cols, remote))
	}
	if search != nil {
		if idx := applyIdentityOrPK(ctx, tbl); idx != nil {
			add("replica identity", applyIndexValueDesc(ctx, idx, cols, search))
		} else {
			add("replica identity full", applyRowValueDesc(ctx, cols, search))
		}
	}
	detail := head
	if len(parts) > 0 {
		detail += "\n" + strings.Join(parts, "; ") + "."
	}
	schema := tbl.Schema
	if schema == "" {
		schema = "public"
	}
	w.log().Info(pgServerLogText(&ExecError{
		Message: fmt.Sprintf("conflict detected on relation \"%s.%s\": conflict=%s", schema, tbl.Name, kind),
		Detail:  detail,
	}))
}

// checkOriginDiffers is the update_origin_differs / delete_origin_differs
// detection of apply_handle_update_internal / apply_handle_delete_internal:
// with track_commit_timestamp on, a located row last modified by another
// origin than the apply worker's (a local transaction, or another
// subscription) is logged; the change still applies. M0149-0061j.
func (w *ApplyWorker) checkOriginDiffers(ctx *Context, tbl *catalog.Table, cols []catalog.Column, op string, found *applyFoundTuple, remote, search Row) {
	if found == nil || w.txnMgr == nil || !w.txnMgr.TrackCommitTimestamp() {
		return
	}
	info, ok := w.txnMgr.CommitTsData(storage.TransactionID(found.xmin))
	if !ok || info.Origin == w.replOrigin() {
		return
	}
	verb := "Updating"
	if op == "delete" {
		verb = "Deleting"
	}
	at := info.Time.Format("2006-01-02 15:04:05.999999-07")
	var head string
	switch {
	case info.Origin == 0:
		head = fmt.Sprintf("%s the row that was modified locally in transaction %d at %s.", verb, found.xmin, at)
	default:
		if name, ok := replOriginNames.Load(info.Origin); ok {
			head = fmt.Sprintf("%s the row that was modified by a different origin \"%s\" in transaction %d at %s.", verb, name, found.xmin, at)
		} else {
			head = fmt.Sprintf("%s the row that was modified by a non-existent origin in transaction %d at %s.", verb, found.xmin, at)
		}
	}
	w.logApplyConflict(ctx, tbl, cols, op+"_origin_differs", head, found.row, remote, search)
}
