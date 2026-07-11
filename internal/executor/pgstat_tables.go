package executor

import "github.com/goopg/goopg/internal/catalog"

// fetchStatTablesRows builds the live, per-connection database-scoped row set for
// the pg_stat_all_tables / pg_stat_user_tables / pg_stat_sys_tables virtual views
// (scope selects which). It mirrors the pg_class per-DB lister: unwrap ctx.Catalog
// to the concrete *catalog.InMemory (ctx.Catalog may be a search-path wrapper) and
// scope to the connection's own database via ctx.CurrentDatabaseOid — so a table in
// one database is invisible from another. Falls back to the static DefaultDBOid
// VirtualRows shape (returns nil here) when no InMemory catalog is reachable.
//
// goopg has no incremental pgstat mutation counters (no PgStat_StatTabEntry), so
// every per-tuple/scan counter is a faithful 0 and every last_* timestamp is NULL;
// relid / schemaname / relname are real and n_live_tup is backed by the ANALYZE
// reltuples estimate. See catalog.PGStatTablesRowsForDBOid and
// docs/design/0122-0003-pg-stat-user-tables.md.
func fetchStatTablesRows(ctx *Context, scope catalog.StatTableScope) [][]string {
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	type unwrapper interface{ Unwrap() catalog.Catalog }
	base := ctx.Catalog
	for {
		if im, ok := base.(*catalog.InMemory); ok {
			return im.PGStatTablesRowsForDBOid(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid), scope)
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
			continue
		}
		return nil
	}
}

// fetchStatIndexesRows is the per-index sibling of fetchStatTablesRows: it builds
// the live, per-connection database-scoped row set for the pg_stat_all_indexes /
// pg_stat_user_indexes / pg_stat_sys_indexes virtual views (scope selects which),
// using the identical ctx.Catalog-unwrap + ctx.CurrentDatabaseOid scoping. See
// catalog.PGStatIndexesRowsForDBOid and docs/design/0122-0003-pg-stat-user-tables.md.
func fetchStatIndexesRows(ctx *Context, scope catalog.StatTableScope) [][]string {
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	type unwrapper interface{ Unwrap() catalog.Catalog }
	base := ctx.Catalog
	for {
		if im, ok := base.(*catalog.InMemory); ok {
			return im.PGStatIndexesRowsForDBOid(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid), scope)
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
			continue
		}
		return nil
	}
}
