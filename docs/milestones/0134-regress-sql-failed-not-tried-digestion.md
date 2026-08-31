# Milestone 0134 — regress-sql `failed` / `not-tried` test-case digestion

**Status:** EXHAUSTED (2026-09-01) — all 189 filed cases sized; none currently
selectable. See "Exhaustion note (2026-09-01)" below.
**Filed:** 2026-08-15 (user directive)
**Priority placement:** **next after M-NIGHTLY.** The `## Current Priority`
banner in `.ralph/fix_plan.md` ranks M-NIGHTLY (nightly regression fixes) first
and M0134 immediately after it (user directive 2026-08-15), ahead of M0119 and
M0122's remaining items. **As of 2026-09-01, M0134 has no remaining selectable
work (see exhaustion note below), so the banner's active milestone falls
through to M0119.**
**Reference plan:** `.ralph/fix_plan.md` (M0134 section, at the foot of the file)
**Prerequisite infrastructure:** the D-001 pg_regress runner
(`scripts/pg-regress-runner.sh`, see `docs/test-port/README.md`) — the SQL-level
harness these cases run under, with output normalised against vanilla PG 18.3
from `./postgres/`.

## Per-task discipline (READ FIRST — binding)

1. **Design note when a task is selected.** Before implementing an M0134 task,
   write a design note under `docs/design/` named
   `<task-id>-NNNN-short-slug.md` (status `draft` → `accepted`) recording the
   failing SQL surface, the exact goopg↔PG 18.3 divergence (`pg-regress-runner.sh
   <case>` normalised against `./postgres/`), the goopg root cause, and the
   PG-oracle citation (`file:function`). Every non-trivial subsystem change lands
   its design doc in the same loop; index it in `docs/design/README.md`.
2. **Update the inventory CSV when status changes.** Whenever a task's
   implementation changes a case's `status`, update the row in
   `docs/test-port/postgres-oracle-target-inventory.csv` **in the same commit**:
   `status → pass`, `pass_required → yes`, `rationale` names the verification
   (the `pg-regress-runner` PASS or the `TestPort_*` func), clear `deferred_to`.
   Run `make check-testport-inventory` before committing.
3. **Verify-before-work on possible regressions.** The four `failed` cases whose
   rationale reads "possible regression, verify" (`mvcc`, `reindex_catalog`,
   `select_having`, `select_implicit`) must be re-run at HEAD first; if one
   already passes, flip the row to `pass` with a "stale — already fixed" note
   instead of implementing anything.

   **Outcome so far (keep this list current):** **two of the four are now
   confirmed stale, zero engine changes between them.** `select_having` was
   re-run at HEAD on 2026-08-19 (M0134-0006) and PASSes — 1/1, 100.0% parity,
   84/84 lines byte-identical, corroborated by the same night's nightly
   `TestPort_RegressSuite/select_having,PASS`. `select_implicit` was re-run the
   same day (M0134-0007) and also PASSes — 1/1, 100.0% parity, 316 lines
   byte-identical, no diff emitted under `tmp/regress-diffs/`, corroborated by
   `ci/logs/20260819-011823/testport/results.csv`
   (`TestPort_RegressSuite/select_implicit,PASS`). Each cost one gate run
   instead of an implementation slice, which is the whole point of this rule;
   the pattern is that these four rows were captured by a baseline that
   disagreed with the inventory's own status, so treat the remaining two
   (`mvcc`, `reindex_catalog`) as *probably* stale too — but re-run each, never
   flip on inference. Note both confirmed-stale cases so far are cheap
   `select_*` cases; `mvcc` and `reindex_catalog` exercise far more engine
   surface, so a genuine divergence there is likelier than this 2-for-2 start
   suggests.

## Priority renumbering — 2026-08-19 (user directive)

Task IDs are the selection order, so re-prioritising this milestone means
renumbering. On 2026-08-19 the user named eighteen higher-value cases and asked
for them to occupy the block starting at **M0134-0006** (M0134-0001..0005 were
left alone: 0001 is in progress and 0002-0005 are parked).

The method is a **pair swap**, not a shift: the eighteen named tasks took
0006..0023 in the order the user listed them, and the sixteen tasks they
displaced took the vacated numbers in ascending order. The other 155 tasks keep
their filed numbers.

| new | case | was | | displaced case | was | now |
|---|---|---|---|---|---|---|
| M0134-0006 | `select_having.sql` | 0066 | | `copy.sql` | 0006 | 0027 |
| M0134-0007 | `select_implicit.sql` | 0067 | | `copy2.sql` | 0007 | 0031 |
| M0134-0008 | `select_parallel.sql` | 0166 | | `create_procedure.sql` | 0009 | 0033 |
| M0134-0009 | `select_views.sql` | 0068 | | `create_table_like.sql` | 0011 | 0036 |
| M0134-0010 | `predicate.sql` | 0153 | | `create_view.sql` | 0012 | 0048 |
| M0134-0011 | `subselect.sql` | 0071 | | `date.sql` | 0013 | 0066 |
| M0134-0012 | `update.sql` | 0082 | | `domain.sql` | 0014 | 0067 |
| M0134-0013 | `insert.sql` | 0033 | | `drop_if_exists.sql` | 0015 | 0068 |
| M0134-0014 | `mvcc.sql` | 0048 | | `equivclass.sql` | 0016 | 0071 |
| M0134-0015 | `join.sql` | 0036 | | `explain.sql` | 0017 | 0082 |
| M0134-0016 | `create_table.sql` | 0010 | | `expressions.sql` | 0018 | 0084 |
| M0134-0017 | `hash_index.sql` | 0027 | | `fast_default.sql` | 0019 | 0085 |
| M0134-0018 | `create_index.sql` | 0008 | | `float4.sql` | 0020 | 0153 |
| M0134-0019 | `indexing.sql` | 0031 | | `float8.sql` | 0021 | 0166 |
| M0134-0020 | `stats.sql` | 0171 | | `foreign_key.sql` | 0022 | 0171 |
| M0134-0021 | `vacuum.sql` | 0084 | | `generated_stored.sql` | 0023 | 0187 |
| M0134-0022 | `window.sql` | 0085 | | | | |
| M0134-0023 | `write_parallel.sql` | 0187 | | | | |

`create_index` (0008) and `create_table` (0010) already sat inside 0006..0023 and
are themselves promoted, so they are not in the displaced column.

**Three resolutions the user confirmed when the list was reconciled against the
suite:**

- **`index.sql` does not exist** in PG 18.3's regress suite. The three `index*`
  cases are `indexing.sql` (`failed`), `index_including.sql` and
  `index_including_gist.sql` (both already `pass`). Read as `indexing.sql`.
- **`select*.sql` expands to the four not-yet-passing cases only** —
  `select_having`, `select_implicit`, `select_parallel`, `select_views`, placed in
  that (alphabetical) order. `select_distinct`, `select_distinct_on` and
  `select_into` already carry `pass`.
- **`select.sql`, `delete.sql` and `sysviews.sql` already carry `pass`** and so
  have no M0134 task to promote; they are absent from the table above by design,
  not by oversight.

**Invariant broken on purpose — do not assume it elsewhere.** After this swap the
ID band no longer segregates `failed` from `not-tried`: `select_parallel`
(`not-tried`) is M0134-0008 and `float4` (`failed`) is M0134-0153. Read the
`status` column, never the number.

## Background

`docs/test-port/postgres-oracle-target-inventory.csv` is the authoritative
inventory of upstream PostgreSQL 18.3 test cases and their goopg port status. The
`regress-sql` suite (`postgres/src/test/regress/sql/*.sql`) is the D-001 SQL-level
suite. At filing (2026-08-15) **189** regress-sql cases are not passing: **87
`failed`** (attempted via the regress runner; output diverges from expected) and
**102 `not-tried`** (in scope, not yet executed). This milestone consumes them
one case per task, **`failed` first with the smaller item numbers**.

## Goals

1. Every `regress-sql` case with `status = failed` or `not-tried` at filing is
   either made to **pass** against PG 18.3 (CSV row flipped to `pass`,
   `pass_required = yes`) or explicitly **deferred** with a deferral-ledger row
   naming the blocker and resume point.
2. One task per case. **At filing** `failed` cases carried M0134-0001..0087 and
   `not-tried` carried M0134-0088..0189, in that order; the 2026-08-19 priority
   renumbering (below) broke that band correspondence for 34 tasks. The per-task
   `status` column of the table below is the authority, not the ID band.
3. Status changes land in the inventory CSV in the same commit.
4. No case is closed silently: a green case with an un-updated CSV row, or a
   skipped case without a ledger row, is a bookkeeping defect.

## Task list (one case per task)

Order at filing: **87 `failed`** (M0134-0001..M0134-0087, CSV order) then **102
`not-tried`** (M0134-0088..M0134-0189, CSV order). **Current order = the table
below**, after the 2026-08-19 priority renumbering; rows that moved say so in the
`note` column. Each task body in `.ralph/fix_plan.md` carries the same text.

| task | case | status | note |
|---|---|---|---|
| M0134-0001 | `aggregates.sql` | failed |  |
| M0134-0002 | `alter_table.sql` | failed |  |
| M0134-0003 | `arrays.sql` | failed |  |
| M0134-0004 | `cluster.sql` | failed |  |
| M0134-0005 | `constraints.sql` | failed |  |
| M0134-0006 | `select_having.sql` | **pass** | renumbered 2026-08-19 (was M0134-0066); **DONE 2026-08-19 — stale `failed`, no goopg change: runner 1/1 PASS at HEAD, CSV flipped to `pass`/`pass_required=yes`** |
| M0134-0007 | `select_implicit.sql` | **pass** | renumbered 2026-08-19 (was M0134-0067); **DONE 2026-08-19 — stale `failed`, no goopg change: runner 1/1 PASS at HEAD (316 lines byte-identical), CSV flipped to `pass`/`pass_required=yes`** |
| M0134-0008 | `select_parallel.sql` | not-tried | **PARKED 2026-08-19** — needs parallel-query execution (see "Prerequisite fixtures, and the first parked case") |
| M0134-0009 | `select_views.sql` | failed | renumbered 2026-08-19 (was M0134-0068) |
| M0134-0010 | `predicate.sql` | not-tried | renumbered 2026-08-19 (was M0134-0153) |
| M0134-0011 | `subselect.sql` | failed | renumbered 2026-08-19 (was M0134-0071) |
| M0134-0012 | `update.sql` | failed | renumbered 2026-08-19 (was M0134-0082) |
| M0134-0013 | `insert.sql` | failed | renumbered 2026-08-19 (was M0134-0033) |
| M0134-0014 | `mvcc.sql` | failed | renumbered 2026-08-19 (was M0134-0048) |
| M0134-0015 | `join.sql` | failed | renumbered 2026-08-19 (was M0134-0036) |
| M0134-0016 | `create_table.sql` | failed | renumbered 2026-08-19 (was M0134-0010) |
| M0134-0017 | `hash_index.sql` | failed | renumbered 2026-08-19 (was M0134-0027) |
| M0134-0018 | `create_index.sql` | failed | renumbered 2026-08-19 (was M0134-0008) |
| M0134-0019 | `indexing.sql` | failed | renumbered 2026-08-19 (was M0134-0031) |
| M0134-0020 | `stats.sql` | not-tried | renumbered 2026-08-19 (was M0134-0171) |
| M0134-0021 | `vacuum.sql` | failed | renumbered 2026-08-19 (was M0134-0084) |
| M0134-0022 | `window.sql` | failed | renumbered 2026-08-19 (was M0134-0085) |
| M0134-0023 | `write_parallel.sql` | not-tried | renumbered 2026-08-19 (was M0134-0187) |
| M0134-0024 | `generated_virtual.sql` | failed |  |
| M0134-0025 | `groupingsets.sql` | failed |  |
| M0134-0026 | `guc.sql` | failed |  |
| M0134-0027 | `copy.sql` | failed | renumbered 2026-08-19 (was M0134-0006) |
| M0134-0028 | `horology.sql` | failed |  |
| M0134-0029 | `identity.sql` | failed |  |
| M0134-0030 | `incremental_sort.sql` | failed |  |
| M0134-0031 | `copy2.sql` | failed | renumbered 2026-08-19 (was M0134-0007) |
| M0134-0032 | `inherit.sql` | failed |  |
| M0134-0033 | `create_procedure.sql` | failed | renumbered 2026-08-19 (was M0134-0009) |
| M0134-0034 | `insert_conflict.sql` | failed |  |
| M0134-0035 | `interval.sql` | failed |  |
| M0134-0036 | `create_table_like.sql` | failed | renumbered 2026-08-19 (was M0134-0011) |
| M0134-0037 | `join_hash.sql` | failed |  |
| M0134-0038 | `json.sql` | failed |  |
| M0134-0039 | `jsonb.sql` | failed |  |
| M0134-0040 | `jsonb_jsonpath.sql` | failed |  |
| M0134-0041 | `jsonpath.sql` | failed |  |
| M0134-0042 | `lock.sql` | failed |  |
| M0134-0043 | `matview.sql` | failed |  |
| M0134-0044 | `merge.sql` | failed |  |
| M0134-0045 | `misc.sql` | failed |  |
| M0134-0046 | `misc_functions.sql` | failed |  |
| M0134-0047 | `multirangetypes.sql` | failed |  |
| M0134-0048 | `create_view.sql` | failed | renumbered 2026-08-19 (was M0134-0012) |
| M0134-0049 | `numeric.sql` | failed |  |
| M0134-0050 | `numeric_big.sql` | failed |  |
| M0134-0051 | `partition_info.sql` | failed |  |
| M0134-0052 | `partition_join.sql` | failed |  |
| M0134-0053 | `partition_prune.sql` | failed |  |
| M0134-0054 | `plancache.sql` | failed |  |
| M0134-0055 | `plpgsql.sql` | failed |  |
| M0134-0056 | `portals.sql` | failed |  |
| M0134-0057 | `prepared_xacts.sql` | failed |  |
| M0134-0058 | `random.sql` | failed |  |
| M0134-0059 | `rangefuncs.sql` | failed |  |
| M0134-0060 | `rangetypes.sql` | failed |  |
| M0134-0061 | `regex.sql` | failed |  |
| M0134-0062 | `reindex_catalog.sql` | failed | verify at HEAD first (possible regression) |
| M0134-0063 | `returning.sql` | failed |  |
| M0134-0064 | `rowtypes.sql` | failed |  |
| M0134-0065 | `rules.sql` | failed |  |
| M0134-0066 | `date.sql` | failed | renumbered 2026-08-19 (was M0134-0013) |
| M0134-0067 | `domain.sql` | failed | renumbered 2026-08-19 (was M0134-0014) |
| M0134-0068 | `drop_if_exists.sql` | failed | renumbered 2026-08-19 (was M0134-0015) |
| M0134-0069 | `sequence.sql` | failed |  |
| M0134-0070 | `strings.sql` | failed |  |
| M0134-0071 | `equivclass.sql` | failed | renumbered 2026-08-19 (was M0134-0016) |
| M0134-0072 | `temp.sql` | failed |  |
| M0134-0073 | `tidrangescan.sql` | failed |  |
| M0134-0074 | `tidscan.sql` | failed |  |
| M0134-0075 | `timestamp.sql` | failed |  |
| M0134-0076 | `timestamptz.sql` | failed |  |
| M0134-0077 | `transactions.sql` | failed |  |
| M0134-0078 | `triggers.sql` | failed |  |
| M0134-0079 | `tuplesort.sql` | failed |  |
| M0134-0080 | `txid.sql` | failed |  |
| M0134-0081 | `updatable_views.sql` | failed |  |
| M0134-0082 | `explain.sql` | failed | renumbered 2026-08-19 (was M0134-0017) |
| M0134-0083 | `uuid.sql` | failed |  |
| M0134-0084 | `expressions.sql` | failed | renumbered 2026-08-19 (was M0134-0018) |
| M0134-0085 | `fast_default.sql` | failed | renumbered 2026-08-19 (was M0134-0019) |
| M0134-0086 | `with.sql` | failed |  |
| M0134-0087 | `xid.sql` | failed |  |
| M0134-0088 | `alter_generic.sql` | not-tried |  |
| M0134-0089 | `alter_operator.sql` | not-tried |  |
| M0134-0090 | `amutils.sql` | not-tried |  |
| M0134-0091 | `async.sql` | not-tried |  |
| M0134-0092 | `bit.sql` | not-tried |  |
| M0134-0093 | `bitmapops.sql` | not-tried |  |
| M0134-0094 | `box.sql` | not-tried |  |
| M0134-0095 | `brin.sql` | not-tried |  |
| M0134-0096 | `brin_bloom.sql` | not-tried |  |
| M0134-0097 | `brin_multi.sql` | not-tried |  |
| M0134-0098 | `circle.sql` | not-tried |  |
| M0134-0099 | `collate.icu.utf8.sql` | not-tried |  |
| M0134-0100 | `collate.linux.utf8.sql` | not-tried |  |
| M0134-0101 | `collate.sql` | not-tried |  |
| M0134-0102 | `collate.utf8.sql` | not-tried |  |
| M0134-0103 | `collate.windows.win1252.sql` | not-tried |  |
| M0134-0104 | `combocid.sql` | not-tried |  |
| M0134-0105 | `compression.sql` | not-tried |  |
| M0134-0106 | `conversion.sql` | not-tried |  |
| M0134-0107 | `copyencoding.sql` | not-tried |  |
| M0134-0108 | `create_aggregate.sql` | not-tried |  |
| M0134-0109 | `create_am.sql` | not-tried |  |
| M0134-0110 | `create_cast.sql` | not-tried |  |
| M0134-0111 | `create_index_spgist.sql` | not-tried |  |
| M0134-0112 | `create_misc.sql` | not-tried |  |
| M0134-0113 | `create_operator.sql` | not-tried |  |
| M0134-0114 | `create_role.sql` | not-tried |  |
| M0134-0115 | `create_schema.sql` | not-tried |  |
| M0134-0116 | `create_type.sql` | not-tried |  |
| M0134-0117 | `database.sql` | not-tried |  |
| M0134-0118 | `dependency.sql` | not-tried |  |
| M0134-0119 | `drop_operator.sql` | not-tried |  |
| M0134-0120 | `encoding.sql` | not-tried |  |
| M0134-0121 | `euc_kr.sql` | not-tried |  |
| M0134-0122 | `event_trigger.sql` | not-tried |  |
| M0134-0123 | `event_trigger_login.sql` | not-tried |  |
| M0134-0124 | `foreign_data.sql` | not-tried |  |
| M0134-0125 | `geometry.sql` | not-tried |  |
| M0134-0126 | `gin.sql` | not-tried |  |
| M0134-0127 | `gist.sql` | not-tried |  |
| M0134-0128 | `hash_func.sql` | not-tried |  |
| M0134-0129 | `indirect_toast.sql` | not-tried |  |
| M0134-0130 | `inet.sql` | not-tried |  |
| M0134-0131 | `infinite_recurse.sql` | not-tried |  |
| M0134-0132 | `init_privs.sql` | not-tried |  |
| M0134-0133 | `json_encoding.sql` | not-tried |  |
| M0134-0134 | `jsonpath_encoding.sql` | not-tried |  |
| M0134-0135 | `largeobject.sql` | not-tried |  |
| M0134-0136 | `line.sql` | not-tried |  |
| M0134-0137 | `lseg.sql` | not-tried |  |
| M0134-0138 | `macaddr.sql` | not-tried |  |
| M0134-0139 | `macaddr8.sql` | not-tried |  |
| M0134-0140 | `maintain_every.sql` | not-tried |  |
| M0134-0141 | `memoize.sql` | not-tried |  |
| M0134-0142 | `misc_sanity.sql` | not-tried |  |
| M0134-0143 | `money.sql` | not-tried |  |
| M0134-0144 | `namespace.sql` | not-tried |  |
| M0134-0145 | `object_address.sql` | not-tried |  |
| M0134-0146 | `oidjoins.sql` | not-tried |  |
| M0134-0147 | `opr_sanity.sql` | not-tried |  |
| M0134-0148 | `password.sql` | not-tried |  |
| M0134-0149 | `path.sql` | not-tried |  |
| M0134-0150 | `point.sql` | not-tried |  |
| M0134-0151 | `polygon.sql` | not-tried |  |
| M0134-0152 | `polymorphism.sql` | not-tried |  |
| M0134-0153 | `float4.sql` | failed | renumbered 2026-08-19 (was M0134-0020) |
| M0134-0154 | `privileges.sql` | not-tried |  |
| M0134-0155 | `psql.sql` | not-tried |  |
| M0134-0156 | `psql_crosstab.sql` | not-tried |  |
| M0134-0157 | `psql_pipeline.sql` | not-tried |  |
| M0134-0158 | `publication.sql` | not-tried |  |
| M0134-0159 | `regproc.sql` | not-tried |  |
| M0134-0160 | `reloptions.sql` | not-tried |  |
| M0134-0161 | `replica_identity.sql` | not-tried |  |
| M0134-0162 | `roleattributes.sql` | not-tried |  |
| M0134-0163 | `rowsecurity.sql` | not-tried |  |
| M0134-0164 | `sanity_check.sql` | not-tried |  |
| M0134-0165 | `security_label.sql` | not-tried |  |
| M0134-0166 | `float8.sql` | failed | renumbered 2026-08-19 (was M0134-0021) |
| M0134-0167 | `spgist.sql` | not-tried |  |
| M0134-0168 | `sqljson.sql` | not-tried |  |
| M0134-0169 | `sqljson_jsontable.sql` | not-tried |  |
| M0134-0170 | `sqljson_queryfuncs.sql` | not-tried |  |
| M0134-0171 | `foreign_key.sql` | failed | renumbered 2026-08-19 (was M0134-0022) |
| M0134-0172 | `stats_ext.sql` | not-tried |  |
| M0134-0173 | `stats_import.sql` | not-tried |  |
| M0134-0174 | `subscription.sql` | not-tried |  |
| M0134-0175 | `tablesample.sql` | not-tried |  |
| M0134-0176 | `tablespace.sql` | not-tried |  |
| M0134-0177 | `test_setup.sql` | not-tried |  |
| M0134-0178 | `tsdicts.sql` | not-tried |  |
| M0134-0179 | `tsearch.sql` | not-tried |  |
| M0134-0180 | `tsrf.sql` | not-tried |  |
| M0134-0181 | `tstypes.sql` | not-tried |  |
| M0134-0182 | `type_sanity.sql` | not-tried |  |
| M0134-0183 | `typed_table.sql` | not-tried |  |
| M0134-0184 | `unicode.sql` | not-tried |  |
| M0134-0185 | `vacuum_parallel.sql` | not-tried |  |
| M0134-0186 | `without_overlaps.sql` | not-tried |  |
| M0134-0187 | `generated_stored.sql` | failed | renumbered 2026-08-19 (was M0134-0023) |
| M0134-0188 | `xml.sql` | not-tried |  |
| M0134-0189 | `xmlmap.sql` | not-tried |  |

## Prerequisite fixtures, and the first parked case (added 2026-08-19, M0134-0008)

`select_parallel.sql` was the milestone's first `not-tried` case, and sizing it
produced two findings worth generalising.

**(a) The runner was missing an upstream prerequisite.** Upstream's
`postgres/src/test/regress/parallel_schedule` documents cross-case dependencies
explicitly — `# select_parallel depends on create_misc` (`:88`), and the same
for `join` (`:62`) and `with` (`:114`), with `create_misc` running in the first
group at `:45`. `scripts/pg-regress-runner.sh`'s `RUN_SETUP` phase ran
`test_setup.sql`, `create_index.sql` and `create_aggregate.sql` but not
`create_misc.sql`, so the `a_star`..`f_star` inheritance chain never existed and
`select_parallel.sql:23` failed on its very first statement — 90% of the
1526-line diff was one 25P02 cascade from that single missing fixture, masking
everything real underneath. The step was added in upstream's position (before
`create_index.sql`), verified non-regressive (`select_having` and
`select_implicit` still 1/1 PASS; `aggregates` FAILs identically before and
after, confirmed against a stashed clean baseline).

**Generalised rule for the rest of this milestone: before sizing any case,
check `parallel_schedule` for a documented `depends on` line.** A case that
fails on its first statement is far more likely to be missing a fixture than to
have found an engine bug, and the diff line count will not tell you apart — when
the prerequisite landed here, the diff stayed *byte-identically* 1526 lines
because a second root cause took over at the same position.

**(b) Not every case is reachable, and parking beats forcing.** With the fixture
in place, `create_misc.sql` still cannot finish: goopg's parser rejects
`ALTER TABLE <table>*` (the legacy trailing-`*` wildcard suffix) and postfix
`ISNULL`/`NOTNULL`, so `a_star.a` is never renamed to `aa`. But that is *also*
not the blocker. `select_parallel.sql`'s tail asserts
`pg_stat_database.parallel_workers_launched` increased (expects `t|t`, goopg
gives `f|f`), and goopg has no `Gather`/parallel-worker execution path at all —
so no harness or parser fix can make this case PASS. M0134-0008 is therefore
**PARKED** with an executable re-arm trigger (land a parallel-query milestone,
then re-run and re-size), its CSV row left `not-tried`, and six deferral-ledger
rows appended 2026-08-19 covering the parallel-query blocker, the two parser
gaps, `SET SESSION AUTHORIZATION` not reaching `current_setting`, function-level
`SET ROLE`, and a pre-existing `aggregates` planner bug found en route.

**Precedent this sets:** a case that cannot pass without a milestone-sized
capability is parked with a re-arm trigger and full ledger coverage — never
closed with a forward reference, and never forced green by weakening the case.

## Definition of done

- All 189 tasks are either `[x]` with the CSV row at `pass` / `pass_required=yes`
  (rationale naming the verification), or `[x]` with a deferral-ledger row and the
  CSV row set to `defer` / `deferred_to` naming the resume point.
- `make check-testport-inventory` passes.
- `scripts/pg-regress-runner.sh <case>` is the per-case gate; no case is checked
  off on a skipped or erroring run.

## Exhaustion note (2026-09-01)

Loop #12 resolved the open question three prior loops (#9-#11) had each partly
investigated but not closed: **is there any regress-sql case with a `failed`/
`not-tried` CSV status that was never assigned an M0134 task ID?** Verified
mechanically, not by inference:

1. Extracted the 189-row canonical `task ↔ case` table from this doc's own
   "Task list" section (all `M0134-0001`..`M0134-0189` IDs, 189 unique).
2. Extracted every regress-sql row from
   `docs/test-port/postgres-oracle-target-inventory.csv` currently at status
   `failed` or `not-tried` (167 rows as of this date — the difference from 189
   is cases that flipped to `pass`, e.g. `select_having`/`select_implicit`
   confirmed-stale, `roleattributes`/`security_label`/`async`/`maintain_every`
   closed green).
3. `comm -23` between the two filename sets: **empty**. Every currently
   failed/not-tried regress-sql case already has an M0134 task ID.
4. Cross-checked for boilerplate (never-attempted) task bodies in
   `.ralph/fix_plan.md` — the original filing template ("regress-sql
   `failed`/`not-tried`: make the case match PG 18.3 … Run the case, fix the
   divergence …") — **zero matches** in the active file. (Tasks 0091-0140's
   bullets are not in the active file at all, but that is the *expected*
   result of the legitimate archival commit `1d74052c5`, which moved 46
   completed M0134 sub-task items to
   `completed_milestones/completed_fix_plan_012.md`; every one of those IDs
   carries a real "CLOSED"/"PARKED" narrative there, confirmed via `git log`
   against their individual landing commits, e.g. `eac970d26` M0134-0092,
   `608a2bb81` M0134-0140.)

**Conclusion: M0134 has no remaining selectable regress-sql work.** All 189
filed cases have been sized at least once and are each in a terminal state:
CLOSED green (passing), CLOSED stale (flipped to `pass` with no code change),
or PARKED (blocked on a REFACTOR-tier prerequisite, each with its own re-arm
trigger and deferral-ledger rows — parallel-query execution, SQL/JSON
constructors, outer-join nullability tracking, a physical GiST/GIN/SP-GiST/BRIN
index-scan plan integration, the geometry operator-lexer family, the `money`
type, `pg_shdepend`/object-address enumeration, and others named throughout the
task list above). The milestone is not "complete" in the Definition-of-Done
sense (most PARKED cases still fail the case gate), but it is **exhausted**:
no loop can make further M0134 progress without first landing one of those
named prerequisite milestones. **Re-arm rule:** re-open M0134 selection only
when one of the named prerequisites lands as its own milestone — re-run
`scripts/pg-regress-runner.sh --verbose <case>` for every PARKED case blocked
on it and re-size.

Per the `## Current Priority` banner's own documented fallback ("Below M0134
the next milestones are M0119 …, then M0122"), active selection now falls
through to **M0119** (sole remaining task: M0119-0006, pg_amcheck server
tier) — see the banner update in `.ralph/fix_plan.md` made in the same commit
as this note.
