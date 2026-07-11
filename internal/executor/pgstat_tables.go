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

// fetchStatXactTablesRows is the per-transaction sibling of fetchStatTablesRows: it
// builds the live, per-connection database-scoped row set for the
// pg_stat_xact_all_tables / pg_stat_xact_user_tables / pg_stat_xact_sys_tables virtual
// views (scope selects which), using the identical ctx.Catalog-unwrap +
// ctx.CurrentDatabaseOid scoping. goopg has no per-transaction pgstat accumulator, so
// every delta counter is a faithful 0. See catalog.PGStatXactTablesRowsForDBOid and
// docs/design/0122-0003-pg-stat-user-tables.md.
func fetchStatXactTablesRows(ctx *Context, scope catalog.StatTableScope) [][]string {
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	type unwrapper interface{ Unwrap() catalog.Catalog }
	base := ctx.Catalog
	for {
		if im, ok := base.(*catalog.InMemory); ok {
			return im.PGStatXactTablesRowsForDBOid(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid), scope)
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

// fetchStatioTablesRows is the buffer-pool I/O sibling of fetchStatTablesRows: it
// builds the live, per-connection database-scoped row set for the
// pg_statio_all_tables / pg_statio_user_tables / pg_statio_sys_tables virtual views
// (scope selects which), using the identical ctx.Catalog-unwrap +
// ctx.CurrentDatabaseOid scoping. goopg has no per-relation buffer-pool counters, so
// every block counter is a faithful 0. See catalog.PGStatioTablesRowsForDBOid and
// docs/design/0122-0003-pg-stat-user-tables.md.
func fetchStatioTablesRows(ctx *Context, scope catalog.StatTableScope) [][]string {
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	type unwrapper interface{ Unwrap() catalog.Catalog }
	base := ctx.Catalog
	for {
		if im, ok := base.(*catalog.InMemory); ok {
			return im.PGStatioTablesRowsForDBOid(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid), scope)
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
			continue
		}
		return nil
	}
}

// fetchStatioIndexesRows is the per-index buffer-pool I/O sibling of
// fetchStatioTablesRows: it builds the live, per-connection database-scoped row set
// for the pg_statio_all_indexes / pg_statio_user_indexes / pg_statio_sys_indexes
// virtual views (scope selects which). See catalog.PGStatioIndexesRowsForDBOid and
// docs/design/0122-0003-pg-stat-user-tables.md.
func fetchStatioIndexesRows(ctx *Context, scope catalog.StatTableScope) [][]string {
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	type unwrapper interface{ Unwrap() catalog.Catalog }
	base := ctx.Catalog
	for {
		if im, ok := base.(*catalog.InMemory); ok {
			return im.PGStatioIndexesRowsForDBOid(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid), scope)
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
			continue
		}
		return nil
	}
}

// fetchStatioSequencesRows is the per-sequence buffer-pool I/O sibling of
// fetchStatioTablesRows: it builds the live, per-connection database-scoped row set
// for the pg_statio_all_sequences / pg_statio_user_sequences / pg_statio_sys_sequences
// virtual views (scope selects which), using the identical ctx.Catalog-unwrap +
// ctx.CurrentDatabaseOid scoping. See catalog.PGStatioSequencesRowsForDBOid and
// docs/design/0122-0003-pg-stat-user-tables.md.
func fetchStatioSequencesRows(ctx *Context, scope catalog.StatTableScope) [][]string {
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	type unwrapper interface{ Unwrap() catalog.Catalog }
	base := ctx.Catalog
	for {
		if im, ok := base.(*catalog.InMemory); ok {
			return im.PGStatioSequencesRowsForDBOid(catalog.NamespaceDBOid(ctx.CurrentDatabaseOid), scope)
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
			continue
		}
		return nil
	}
}
