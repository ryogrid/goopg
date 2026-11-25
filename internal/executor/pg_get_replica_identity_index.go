package executor

import "github.com/goopg/goopg/internal/catalog"

// evalPgGetReplicaIdentityIndex is pg_get_replica_identity_index(regclass)
// (utils/adt/misc.c → RelationGetReplicaIndex): the OID of the index that is
// the relation's replica identity — the primary key under REPLICA IDENTITY
// DEFAULT, the index marked by REPLICA IDENTITY USING INDEX — or NULL under
// FULL / NOTHING, without a primary key, or for an unknown relation. A
// deferrable index never qualifies (RelationGetIndexList skips it). A PG
// subscriber's table sync calls it on the publisher (fetch_remote_table_info)
// to learn the key columns. M0149-0041c.
func evalPgGetReplicaIdentityIndex(arg Datum, ctx *Context) Datum {
	if arg.IsNull() || ctx == nil || ctx.Catalog == nil {
		return NullDatum
	}
	relOID, ok := partitionResolveRegclass(arg, ctx)
	if !ok {
		return NullDatum
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return NullDatum
	}
	dbOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	tbl, ok := im.LookupTableByOID(relOID, dbOid)
	if !ok {
		return NullDatum
	}
	ident := catalog.ReplIdentOrDefault(tbl.ReplicaIdentity)[0]
	if ident != 'd' && ident != 'i' {
		return NullDatum
	}
	for _, idx := range im.IndexesOnTable(tbl, dbOid) {
		if idx.IsImmediate() && ((ident == 'd' && idx.Primary) || (ident == 'i' && idx.IsReplicaIdentity)) {
			return NewIntDatum(int64(idx.OID))
		}
	}
	return NullDatum
}
