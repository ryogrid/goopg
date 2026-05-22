# Regress Test Root Cause Analysis (2026-05-22)

Baseline: `docs/test-port/regress-diff-baseline.csv` (126 tests, sorted by diff count).
Generated from normalized diff inspection + source-code cross-reference.

Each entry categorises the primary reason the test output diverges from
PostgreSQL's expected output.  Categories:

- **MISSING_COERCION** — string→numeric type coercion missing in PG-format encode path
- **TOAST** — TOAST out-of-line storage fails silently (>2000-byte values lost)
- **MISSING_FEATURE** — SQL feature / function not yet implemented
- **TYPE_GAP** — data type partially or not implemented
- **PLAN_DIFF** — planner produces a different plan structure
- **FORMAT** — output format mismatch (column width, error wording, whitespace)
- **CATALOG** — system catalog / virtual table missing or wrong
- **DEP_TRACKING** — dependency tracking for DROP ... CASCADE missing
- **FUNC_DEP** — functional-dependency inference for GROUP BY missing
- **INFRA** — test infrastructure issue (file path, psql flag, double-run)
- **UNKNOWN** — not yet triaged

When a test has multiple root causes the most impactful (largest diff
contributor) is listed first.

---

## Root Cause Summary

| Root Cause | Tests Affected | Impact |
|------------|---------------|--------|
| MISSING_COERCION | ~40 | string→numeric INSERT fails, shared tables empty |
| TOAST | ~30 | rows with >2000-byte text silently lost |
| MISSING_FEATURE | ~25 | EXISTS in PL/pgSQL, geometric types, etc. |
| TYPE_GAP | ~15 | point, interval, numeric, timestamptz gaps |
| PLAN_DIFF | ~8 | EXPLAIN output structurally different |
| FORMAT | ~5 | column width, error message wording |
| CATALOG | ~3 | system view / SRF stub coverage |
| DEP_TRACKING | ~2 | DROP CONSTRAINT CASCADE not tracked |
| FUNC_DEP | ~2 | functional dependency inference |
| INFRA | ~1 | COPY file path resolution |
| UNKNOWN | ~5 | needs deeper triage |

## Per-Test Analysis

### delete (5 diffs) — TOAST
- First divergence: expected 2 rows after invalid-alias DELETE, actual 1 row.
- Root cause: `INSERT INTO delete_test (a, b) VALUES (50, repeat('x', 10000))`
  silently fails.  `repeat('x', 10000)` → 10000-byte text → TOAST path activated.
  `rows_affected=1` reported but tuple is invisible (TOAST chunks written
  with xmin that doesn't match the main tuple's xmin, or detoast fails
  and seqScan skips the tuple).
- Planner alias enforcement (`blockOriginalName`) is CORRECT — confirmed by
  unit test: `TestDeleteAliasBlockOriginalName` PASS.
- Code: `internal/executor/toast.go::toastStore` +
  `internal/executor/operators_storage.go::writeHeapRowReturning`

### mvcc (12 diffs) — MISSING_FEATURE
- First divergence: missing `size_before | size_after` rows, extra
  `ERROR: EXISTS is not supported in PL/pgSQL expressions in v0`.
- Root cause: PL/pgSQL `EXISTS(SELECT ...)` in a DO block not supported.
- Code: `internal/executor/plpgsql_runtime.go` — no EXISTS evaluation.

### functional_deps (34 diffs) — FUNC_DEP + DEP_TRACKING
- Multiple divergences: missing rows from functional-dependency queries,
  missing DROP CONSTRAINT CASCADE errors, missing GROUP BY errors.
- Root cause: planner does not infer functional dependencies from PRIMARY KEY /
  UNIQUE constraints.  Dependency tracking for DROP ... CASCADE not implemented.
- Code: `internal/planner/planner.go` — no FD inference;
  `internal/catalog/` — no pg_depend tracking.

### select_having (42 diffs) — MISSING_COERCION
- All queries return 0 rows instead of expected data.
- Root cause: shared tables (INT2_TBL, INT4_TBL, FLOAT8_TBL, etc.) empty
  because test_setup.sql string→numeric coercion fails at INSERT time.
  Partially mitigated by encodeValuePG fix (2026-05-22).

### test_setup (44 diffs) — MISSING_COERCION + TYPE_GAP + INFRA
- Errors: `kind 3 cannot encode as float8`, `expected int, got kind 3`,
  `column "f1" has type "point"`, COPY file path resolution failures.
- Root cause: (a) string→numeric coercion missing (encodeValuePG fix in
  progress), (b) point type unsupported, (c) `\getenv abs_srcdir` not
  resolved for COPY file paths, (d) `allow_in_place_tablespaces` GUC missing.
- Note: `runRegressSetup` runs test_setup.sql before the suite; tables are
  created but many INSERTs fail silently, leaving tables empty.

### copydml (57 diffs) — MISSING_FEATURE
- COPY with DML (INSERT/SELECT via COPY) partially implemented.
- Code: `internal/server/copy.go`

### time (64 diffs) — TYPE_GAP
- Time type arithmetic and formatting differences.
- Time type is partially implemented (M0097-0003); remaining gaps in
  `EXTRACT`, time zone handling, `time + time` operator, etc.

### portals_p2 (65 diffs) — MISSING_FEATURE
- Cursor / portal operations partially implemented.

### errors (72 diffs) — FORMAT + MISSING_FEATURE
- Error message wording and LINE/POSITION differences.
- Some SQL features produce different error codes or messages.

### char (75 diffs) — FORMAT
- bpchar/char output width and padding differences.
- Normalizer `appendCharText` handles some but not all cases.

### copyselect (85 diffs) — MISSING_FEATURE
- COPY (SELECT ...) TO STDOUT partially implemented.

### varchar (86 diffs) — FORMAT
- Varchar output formatting differences.

### boolean (89 diffs) — FORMAT
- Boolean output format differences (`t`/`f` vs `true`/`false`).

### hash_part (92 diffs) — MISSING_FEATURE + PLAN_DIFF
- Hash partitioning partially implemented; plan differences.

### numerology (94→87 diffs) — MISSING_COERCION + MISSING_FEATURE
- Improved by encodeValuePG fix (94→87).
- Remaining: SELECT DISTINCT, negative-zero display, parameter error messages.

### sysviews (95 diffs) — CATALOG
- System view SRF stubs missing or returning wrong row counts.
- `pg_available_extensions`, `pg_backend_memory_contexts`, etc. return
  different data than PG.
- Code: `internal/server/query.go`, `internal/catalog/`

### tid (102 diffs) — MISSING_FEATURE
- TID scan partially implemented.

### name (117 diffs) — MISSING_FEATURE
- name type truncation, array subscript, RAISE NOTICE not emitting.

### oid (118→24 diffs) — MISSING_COERCION + FORMAT
- Major improvement from encodeValuePG fix (118→24).
- Remaining: output format, error message wording.

### hash_index (127 diffs) — MISSING_FEATURE
- Hash index partially implemented.

### index_including_gist (131 diffs) — TYPE_GAP
- Requires GIST index support (excluded per M0097-0002).

### select_into (140 diffs) — MISSING_FEATURE
- SELECT INTO for table creation partially implemented.

### lock (151 diffs) — MISSING_FEATURE
- Locking operations (LOCK TABLE, SELECT FOR UPDATE) partially implemented.

### prepare (151 diffs) — MISSING_FEATURE
- PREPARE/EXECUTE/DEALLOCATE partially implemented.

### dbsize (153 diffs) — MISSING_FEATURE + CATALOG
- `pg_database_size`, `pg_relation_size` return stub values.
- `pg_size_pretty` implemented but some edge cases differ.

### expressions (156 diffs) — MISSING_FEATURE
- Various SQL expression functions partially implemented.
- `SUBSTRING`, `OVERLAY`, `regexp_replace` stubs.

### drop_if_exists (165 diffs) — FORMAT + MISSING_FEATURE
- DROP IF EXISTS NOTICE messages differ.
- Some object types not supported.

### advisory_lock (180 diffs) — MISSING_FEATURE
- Advisory lock functions partially implemented.
- Some variants (`pg_advisory_xact_lock_shared`, `pg_advisory_unlock_shared`)
  not implemented.

### prepared_xacts (180 diffs) — MISSING_FEATURE
- Two-phase commit PREPARE TRANSACTION partially implemented.

### select_distinct_on (201 diffs) — MISSING_FEATURE
- SELECT DISTINCT ON partially implemented.

### case (203 diffs) — MISSING_FEATURE + MISSING_COERCION
- CASE expression partially implemented.
- Dependent on shared tables (INT4_TBL, etc.) for many queries.

### select_implicit (208 diffs) — MISSING_COERCION
- Most queries return 0 rows; shared table population issue.
- encodeValuePG fix improves but doesn't fully resolve.

### uuid (212 diffs) — MISSING_COERCION + TYPE_GAP
- UUID type partially implemented.
- encodeValuePG fix doesn't address UUID string→uuid coercion.

### partition_info (235 diffs) — MISSING_FEATURE
- Partition metadata / information functions partially implemented.

### pg_lsn (237 diffs) — TYPE_GAP
- pg_lsn type partially implemented.

### timetz (245 diffs) — TYPE_GAP
- Time with time zone type partially implemented.

### misc (262 diffs) — MISSING_FEATURE
- Miscellaneous function coverage gaps.

### tidscan (271 diffs) — MISSING_FEATURE
- TID scan partially implemented.

### txid (294 diffs) — TYPE_GAP
- Transaction ID type (`xid8`, `xid`) partially implemented.

### index_including (299 diffs) — MISSING_FEATURE
- INDEX INCLUDE clause partially implemented.

### tidrangescan (300 diffs) — MISSING_FEATURE
- TID range scan partially implemented.

### int2 (309→71 diffs) — MISSING_COERCION + FORMAT
- Major improvement from encodeValuePG fix (309→71).
- Remaining: output format, error message wording.

### temp (319 diffs) — MISSING_FEATURE + MISSING_COERCION
- TEMP TABLE partially implemented.
- Some queries return 0 rows (shared table issue).

### truncate (324 diffs) — MISSING_FEATURE
- TRUNCATE partially implemented (RESTART IDENTITY, CASCADE variants).

### create_procedure (325 diffs) — MISSING_FEATURE
- CREATE PROCEDURE / CALL partially implemented.

### int4 (337→120 diffs) — MISSING_COERCION + FORMAT
- Major improvement from encodeValuePG fix (337→120).
- Remaining: output format, arithmetic overflow messages.

### plancache (357 diffs) — MISSING_FEATURE
- Plan cache / prepared statement invalidation partially implemented.

### equivclass (361 diffs) — FUNC_DEP + MISSING_FEATURE
- Planner equivalence class derivation partially implemented.

### text (377 diffs) — MISSING_COERCION + FORMAT
- Text type operations; some queries return 0 rows.

### xid (415 diffs) — TYPE_GAP
- xid type partially implemented.

### enum (419 diffs) — TYPE_GAP
- ENUM type partially implemented (CREATE TYPE AS ENUM, ALTER TYPE ADD VALUE).

### random (444 diffs) — MISSING_FEATURE + FORMAT
- `random()` function implemented but output format differs.
- Uses `generate_series` which returns rows but formatting may differ.

### select_distinct (462 diffs) — MISSING_COERCION + MISSING_FEATURE
- SELECT DISTINCT partially implemented.
- Shared table population issue (0 rows).

### vacuum (477 diffs) — MISSING_FEATURE
- VACUUM partially implemented (VACUUM FULL supported, others stubbed).

### cluster (482 diffs) — MISSING_FEATURE
- CLUSTER statement implemented but several edge cases not covered.

### regex (528 diffs) — MISSING_FEATURE
- POSIX regex (`~`, `~*`, `!~`, `!~*`) implemented but some patterns differ.

### btree_index (537 diffs) — MISSING_FEATURE + PLAN_DIFF
- B-tree index operations partially implemented.
- EXPLAIN output plan structure differs.

### create_function_sql (578 diffs) — MISSING_FEATURE
- SQL-language functions partially implemented.
- Multiple statements, RETURNS TABLE, RETURNS SETOF gaps.

### matview (586 diffs) — MISSING_FEATURE
- Materialized views (CREATE MATERIALIZED VIEW, REFRESH) partially implemented.

### tuplesort (586 diffs) — MISSING_FEATURE + PLAN_DIFF
- Incremental sort / tuplesort partially implemented.

### create_table_like (597 diffs) — MISSING_FEATURE
- CREATE TABLE ... LIKE partially implemented.

### create_table (608 diffs) — MISSING_FEATURE
- CREATE TABLE variants (PARTITION BY, INHERITS, GENERATED, etc.) partially
  implemented.

### insert_conflict (625 diffs) — MISSING_FEATURE
- INSERT ... ON CONFLICT partially implemented.

### limit (657 diffs) — MISSING_FEATURE + MISSING_COERCION
- LIMIT/OFFSET implemented; shared table issue causes 0 rows for many queries.

### sequence (665 diffs) — MISSING_FEATURE
- Sequence functions (nextval, currval, setval) partially implemented.

### update (665 diffs) — MISSING_FEATURE + MISSING_COERCION
- UPDATE partially implemented.
- Known hang: RANGE partition row-movement with multi-level hierarchies.
- Shared table issue causes 0 rows for many queries.

### explain (733 diffs) — PLAN_DIFF
- EXPLAIN output format structurally different from PG.
- Plan tree structure, cost display, row estimates all differ.

### identity (734 diffs) — MISSING_FEATURE
- GENERATED ALWAYS AS IDENTITY partially implemented.

### misc_functions (734 diffs) — MISSING_FEATURE
- Miscellaneous function coverage gaps.

### transactions (735 diffs) — MISSING_FEATURE
- Transaction statements (SAVEPOINT, ROLLBACK TO, CHAIN) partially implemented.

### float4 (750→739 diffs) — MISSING_COERCION + TYPE_GAP
- EncodeValuePG fix reduced diffs (750→739).
- Float4 type partially implemented.  Remaining gaps in NaN/Inf handling,
  scientific notation formatting, extreme-value encoding.

### guc (750 diffs) — MISSING_FEATURE + CATALOG
- SHOW/SET/RESET implemented but GUC coverage incomplete.
- `pg_settings` view differs.  Many GUCs not registered.

### insert (758 diffs) — MISSING_COERCION + MISSING_FEATURE
- INSERT partially implemented.  Shared table issue (0 rows for many queries).
- RETURNING, DEFAULT, multi-row VALUES gaps.

### fast_default (764 diffs) — MISSING_FEATURE
- Fast default column optimization partially implemented.

### returning (838 diffs) — MISSING_FEATURE
- INSERT/UPDATE/DELETE RETURNING partially implemented.

### copy2 (860 diffs) — MISSING_FEATURE
- COPY format options (CSV, HEADER, FORCE_QUOTE, etc.) partially implemented.

### join_hash (883 diffs) — PLAN_DIFF + MISSING_FEATURE
- Hash join implemented but plan output and row estimates differ.

### select (898 diffs) — MISSING_COERCION + MISSING_FEATURE
- SELECT partially implemented.  Shared table issue (0 rows for many queries).

### indexing (1066 diffs) — MISSING_FEATURE
- Index maintenance (CREATE INDEX, REINDEX) partially implemented.

### domain (1082 diffs) — TYPE_GAP
- DOMAIN type partially implemented.

### int8 (1124 diffs) — MISSING_COERCION + TYPE_GAP
- encodeValuePG fix does NOT improve int8 significantly.
- Possible separate issue with bigint string coercion or display.

### numeric_big (1187 diffs) — TYPE_GAP
- NUMERIC type partially implemented; big-number path has gaps.

### rowtypes (1194 diffs) — TYPE_GAP
- Composite/row types partially implemented.

### constraints (1210 diffs) — MISSING_FEATURE + DEP_TRACKING
- CHECK constraints, FK constraints partially implemented.

### float8 (1253→1246 diffs) — MISSING_COERCION + TYPE_GAP + TOAST
- EncodeValuePG fix reduced diffs (1253→1246).
- Float8 type partially implemented.  NaN/Inf, scientific notation,
  extreme-value encoding gaps remain.  TOAST issue may also contribute.

### copy (1259 diffs) — MISSING_FEATURE
- COPY FROM/TO partially implemented.

### jsonpath (1290 diffs) — TYPE_GAP
- JSONPath type partially implemented.

### union (1298 diffs) — MISSING_COERCION + MISSING_FEATURE
- UNION/INTERSECT/EXCEPT partially implemented.
- Shared table issue (0 rows for many queries).

### portals (1334 diffs) — MISSING_FEATURE
- Cursors/DECLARE/FETCH partially implemented.

### date (1368 diffs) — TYPE_GAP
- Date type partially implemented.

### generated_stored (1405 diffs) — MISSING_FEATURE
- GENERATED ALWAYS AS (expr) STORED partially implemented.

### select_views (1478 diffs) — MISSING_FEATURE
- SELECT on views partially implemented.

### generated_virtual (1574 diffs) — MISSING_FEATURE
- GENERATED ALWAYS AS (expr) VIRTUAL partially implemented.

### incremental_sort (1647 diffs) — MISSING_FEATURE
- Incremental sort partially implemented.

### triggers (1649 diffs) — MISSING_FEATURE
- Triggers (CREATE TRIGGER, trigger body execution) partially implemented.

### partition_aggregate (1653 diffs) — MISSING_FEATURE
- Partition-wise aggregation partially implemented.

### rangetypes (1844 diffs) — TYPE_GAP
- Range types partially implemented.

### interval (1928 diffs) — TYPE_GAP
- Interval type partially implemented.

### create_view (2012 diffs) — MISSING_FEATURE
- CREATE VIEW (including OR REPLACE, WITH CHECK OPTION) partially implemented.

### timestamp (2142 diffs) — TYPE_GAP
- Timestamp type partially implemented.

### rangefuncs (2207 diffs) — MISSING_FEATURE
- Range functions (generate_series, etc.) partially implemented.

### plpgsql (2365 diffs) — MISSING_FEATURE
- PL/pgSQL partially implemented.  Missing: EXCEPTION handlers, RETURN NEXT,
  EXECUTE INTO, FOR IN SELECT, etc.

### strings (2366 diffs) — MISSING_FEATURE
- String functions (overlay, substring, replace, split_part, etc.)
  partially implemented.

### groupingsets (2383 diffs) — MISSING_FEATURE
- GROUPING SETS / ROLLUP / CUBE not implemented.

### subselect (2470 diffs) — MISSING_FEATURE
- Subquery in SELECT/FROM/WHERE partially implemented.

### json (2536 diffs) — TYPE_GAP
- JSON type partially implemented.

### merge (2571 diffs) — MISSING_FEATURE
- MERGE INTO partially implemented.  Missing: RETURNING old/new,
  WHEN NOT MATCHED BY SOURCE.

### aggregates (2621 diffs) — MISSING_FEATURE
- Aggregate functions partially implemented.
- FILTER (WHERE), ordered-set aggregates (percentile_cont), GROUPING not
  implemented.

### arrays (2814 diffs) — TYPE_GAP
- Array type partially implemented.

### alter_table (2896 diffs) — MISSING_FEATURE
- ALTER TABLE variants partially implemented.

### create_index (2937 diffs) — MISSING_FEATURE
- CREATE INDEX variants partially implemented.

### with (2976 diffs) — MISSING_FEATURE
- CTE (WITH) partially implemented.
- Recursive CTE, DML CTE gaps.

### rules (3106 diffs) — MISSING_FEATURE
- Rule system (CREATE RULE) partially implemented.

### foreign_key (3167 diffs) — MISSING_FEATURE
- Foreign key constraints partially implemented.

### inherit (3208 diffs) — MISSING_FEATURE
- Table inheritance (INHERITS) partially implemented.

### multirangetypes (3328 diffs) — TYPE_GAP
- Multirange types partially implemented.

### updatable_views (3400 diffs) — MISSING_FEATURE
- Updatable views (INSERT/UPDATE/DELETE via views) partially implemented.

### timestamptz (3428 diffs) — TYPE_GAP
- Timestamptz type partially implemented.

### horology (3923 diffs) — TYPE_GAP
- Date/time functions (age, date_trunc, extract, etc.) partially implemented.

### numeric (4080 diffs) — TYPE_GAP
- Numeric type partially implemented (int64 fast-path OK, big-number path gaps).

### window (4225 diffs) — MISSING_FEATURE
- Window functions partially implemented.

### partition_prune (4764 diffs) — MISSING_FEATURE
- Partition pruning partially implemented.

### jsonb_jsonpath (4792 diffs) — TYPE_GAP
- JSONB + JSONPath partially implemented.

### jsonb (5847 diffs) — TYPE_GAP
- JSONB type partially implemented.

### partition_join (5904 diffs) — MISSING_FEATURE + PLAN_DIFF
- Partition-wise join partially implemented.

### join (10417 diffs) — MISSING_FEATURE + MISSING_COERCION + PLAN_DIFF
- JOIN queries heavily affected by empty shared tables + plan differences.
- Most queries in join.sql depend on INT4_TBL, INT8_TBL, etc.
