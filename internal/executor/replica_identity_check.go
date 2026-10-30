package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
)

// checkCmdReplicaIdentity is CheckCmdReplicaIdentity (execReplication.c),
// called once per UPDATE / DELETE result relation as CheckValidResultRel
// does. A table that publishes UPDATEs (DELETEs) must have a replica
// identity — the primary key under DEFAULT, the indisreplident index under
// USING INDEX, or FULL — because logical decoding identifies the old row by
// it: the Relation message flags exactly those columns, and a subscriber
// locates the row to update or delete by them. Without one PG refuses the
// statement (55000) rather than stream a change no subscriber can apply.
// Partitioned tables are skipped; the operation is checked on its leaves.
//
// Row-filter / column-list / generated-column validity (the pubdesc
// rf_valid / cols_valid / gencols_valid arms) is not checked: goopg
// publications carry neither row filters nor column lists.
func checkCmdReplicaIdentity(ctx *Context, tbl *catalog.Table, isUpdate bool) error {
	if ctx.PubSub == nil || tbl == nil || len(tbl.PartitionKey) > 0 {
		return nil
	}
	pubs := ctx.PubSub.PublicationsForDBOid(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if len(pubs) == 0 {
		return nil
	}
	// Publication table names are stored canonicalised; a table goopg
	// records without a schema is the public one under either spelling.
	qname, alt := tbl.Schema+"."+tbl.Name, ""
	if tbl.Schema == "" {
		qname, alt = tbl.Name, "public."+tbl.Name
	}
	publishes := false
	for _, pub := range pubs {
		if !(isUpdate && pub.PublishUpdate) && !(!isUpdate && pub.PublishDelete) {
			continue
		}
		if pub.AllTables {
			publishes = true
			break
		}
		for _, t := range pub.Tables {
			if t == qname || (alt != "" && t == alt) {
				publishes = true
				break
			}
		}
		if publishes {
			break
		}
	}
	if !publishes || hasReplicaIdentity(ctx, tbl) {
		return nil
	}
	if isUpdate {
		return &ExecError{Code: "55000",
			Message: fmt.Sprintf("cannot update table \"%s\" because it does not have a replica identity and publishes updates", tbl.Name),
			Hint:    "To enable updating the table, set REPLICA IDENTITY using ALTER TABLE."}
	}
	return &ExecError{Code: "55000",
		Message: fmt.Sprintf("cannot delete from table \"%s\" because it does not have a replica identity and publishes deletes", tbl.Name),
		Hint:    "To enable deleting from the table, set REPLICA IDENTITY using ALTER TABLE."}
}

// hasReplicaIdentity reports RelationGetReplicaIndex being valid (the
// immediate primary key under DEFAULT, the immediate indisreplident index
// under USING INDEX) or relreplident = FULL. Sibling of
// xlog.identityColumns, which must pick the same index.
func hasReplicaIdentity(ctx *Context, tbl *catalog.Table) bool {
	mode := catalog.ReplIdentOrDefault(tbl.ReplicaIdentity)
	if mode == "f" {
		return true
	}
	if mode != "d" && mode != "i" {
		return false
	}
	im, ok := ctx.Catalog.(interface {
		IndexesOnTable(*catalog.Table, ...uint32) []*catalog.Index
	})
	if !ok {
		if sp, isSP := ctx.Catalog.(*catalog.SearchPathCatalog); isSP {
			im, ok = sp.Catalog.(interface {
				IndexesOnTable(*catalog.Table, ...uint32) []*catalog.Index
			})
		}
	}
	if !ok {
		return true // no index view: do not refuse what cannot be checked
	}
	for _, idx := range im.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		// RelationGetIndexList: a deferrable (non-immediate) index is never
		// the replica identity — rd_replidindex skips a deferrable primary
		// key under DEFAULT and a non-immediate indisreplident index.
		if idx.IsImmediate() && ((mode == "d" && idx.Primary) || (mode == "i" && idx.IsReplicaIdentity)) {
			return true
		}
	}
	return false
}
