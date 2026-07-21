package catalog

import (
	"sort"
	"strconv"
)

// PGStatsRowsForDBOid builds the pg_stats view row set for the tables in the
// database namespace dbOid. pg_stats is upstream a SQL view (system_views.sql)
// that projects pg_statistic JOIN pg_class/pg_attribute/pg_namespace into a
// human-readable, per-column statistics row: one row for every analyzed,
// non-dropped column. goopg has no SQL evaluation over the pg_statistic heap in
// the virtual-catalog path, so this Go builder reproduces the same projection
// directly from the same in-memory ColumnStats that buildUserPGStatisticRow
// writes to the pg_statistic heap — the two are sibling consumers of
// Table.Stats.Columns and must agree (see
// docs/design/0122-0003-pg-stat-user-tables.md).
//
// goopg's ANALYZE collects only the MCV list (upstream STATISTIC_KIND_MCV, slot
// 1) and the equi-depth histogram (STATISTIC_KIND_HISTOGRAM, slot 2). It does
// not collect correlation (kind 3), most-common-elements (kind 4), or range-type
// stats (kinds 5/6), so the corresponding pg_stats columns are always NULL —
// byte-identical to a real PG 18.3 cluster whose ANALYZE happened to compute no
// such slots for the column. Only columns with a materialized Table.Stats entry
// appear (mirrors "a column ANALYZE never touched has no pg_statistic row").
//
// Simplifications (recorded in the deferral ledger): most_common_vals /
// histogram_bounds are rendered from the canonical Datum.Format() text of each
// value (goopg stores stats values as text, not the column's own type — same
// deviation as the pg_statistic heap's text[] stavalues); avg_width mirrors the
// pg_statistic builder's fixed placeholder; and no has_column_privilege / system
// relation filtering is applied (goopg connections are superuser, which sees
// every column, matching the default case).
func (c *InMemory) PGStatsRowsForDBOid(dbOid uint32) [][]string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]string, 0, len(c.ns(dbOid).tables))
	for k := range c.ns(dbOid).tables {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([][]string, 0, len(keys))
	for _, k := range keys {
		t := c.ns(dbOid).tables[k]
		if t == nil || t.Stats == nil || len(t.Stats.Columns) == 0 {
			continue
		}
		schema := t.Schema
		if schema == "" {
			schema = "public"
		}
		for i, cs := range t.Stats.Columns {
			if i >= len(t.Columns) {
				break
			}
			col := t.Columns[i]
			// SET STATISTICS 0 disables collection: examine_attribute returns
			// NULL upstream and buildUserPGStatisticRow's caller writes no
			// pg_statistic row, so pg_stats shows no row either.
			if col.StatTarget != nil && *col.StatTarget == 0 {
				continue
			}

			nullFrac := strconv.FormatFloat(cs.NullFrac, 'g', -1, 32)
			// Same sign convention as the pg_statistic write path: PG's
			// pg_stats.n_distinct is negative when the value scales with the
			// relation's row count.
			var distinctF float64
			switch {
			case cs.NDistinctFrac > 0:
				distinctF = -cs.NDistinctFrac
			case cs.NDistinct > 0:
				distinctF = float64(cs.NDistinct)
			}
			nDistinct := strconv.FormatFloat(distinctF, 'g', -1, 32)

			mcVals := VirtualNull
			mcFreqs := VirtualNull
			if len(cs.MCV) > 0 {
				vals := make([]string, len(cs.MCV))
				freqs := make([]string, len(cs.MCV))
				for j, e := range cs.MCV {
					vals[j] = e.Value
					// float4 round-trip: format the freq as a real, matching
					// the pg_statistic heap's pgFloat4ArrayBytes(float32(...)).
					freqs[j] = strconv.FormatFloat(float64(float32(e.Frequency)), 'g', -1, 32)
				}
				mcVals = arrayTextLiteral(vals)
				mcFreqs = arrayTextLiteral(freqs)
			}
			histBounds := VirtualNull
			if len(cs.Histogram) > 0 {
				histBounds = arrayTextLiteral(cs.Histogram)
			}

			N := VirtualNull
			out = append(out, []string{
				schema,    // 0:  schemaname
				t.Name,    // 1:  tablename
				col.Name,  // 2:  attname
				"f",       // 3:  inherited (stainherit=false; goopg has no inheritance stats)
				nullFrac,  // 4:  null_frac
				"8",       // 5:  avg_width (placeholder, mirrors pg_statistic.stawidth)
				nDistinct, // 6:  n_distinct
				mcVals,    // 7:  most_common_vals
				mcFreqs,   // 8:  most_common_freqs
				histBounds, // 9: histogram_bounds
				N,         // 10: correlation (kind 3 not collected)
				N,         // 11: most_common_elems (kind 4)
				N,         // 12: most_common_elem_freqs (kind 4)
				N,         // 13: elem_count_histogram (kind 5)
				N,         // 14: range_length_histogram (kind 6)
				N,         // 15: range_empty_frac (kind 6)
				N,         // 16: range_bounds_histogram (kind 6)
			})
		}
	}
	return out
}
