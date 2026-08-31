# goopg Fix Plan

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless the Current
Priority banner below or a dependency forces another order**. **As of 2026-08-15:
M-NIGHTLY is the standing filing obligation (highest priority); M0134
(regress-sql `failed`/`not-tried` digestion) is the next-priority milestone after
M-NIGHTLY (user directive 2026-08-15).**
The banner is the sole ordering
authority — `.ralph/working_set.md`'s "NEXT LOOP" note carries state, not
priority, and does not outrank it.
**M0134 (regress-sql `failed`/`not-tried` test-case digestion) was filed
2026-08-15 at the foot of this file and is the next-priority milestone after
M-NIGHTLY** (user directive 2026-08-15) — it is selected immediately after
M-NIGHTLY's regression fixes, ahead of M0119 and M0122's remaining items. M0132
and M0133 are COMPLETE; M0131 is closed except S24 (deferred, not selectable);
M0130 is closed.

## Notes / rules

- This is the authoritative TODO list for Ralph. Update it after every meaningful
  change (tick boxes, add newly-discovered follow-ups). ONE item per loop;
  decompose any item larger than a single agent invocation.
- Every non-trivial subsystem must land with (or just before) a design doc under
  `docs/design/<id>-NNNN-*.md` **and** a `docs/design/README.md` index entry —
  hard requirement, same loop.
- Deferrals: never close a task silently with a forward reference. Append one row
  to `.ralph/deferral_ledger.md` (`date | task-id | landed | deferred | resume
  point | why`) and leave the fix_plan item unchecked. **The ledger is the source
  of truth for every "DEFERRED" note below** — consult it for full context/resume
  points.
- Completed milestones are archived under `completed_milestones/` (latest:
  `completed_fix_plan_011.md`); they are reference-only, NOT actionable, and must
  not be copied back here.

## Current Priority (per 2026-08-15 — M0134)

**M-NIGHTLY is the standing filing obligation (unconditional, highest priority):
every loop reads `ci/logs/action-items.md` and files each new `## AI-` subject
under the M-NIGHTLY milestone below.**

**M0134 (regress-sql `failed`/`not-tried` test-case digestion) is the
next-priority milestone after M-NIGHTLY** (user directive 2026-08-15) — work it
immediately after M-NIGHTLY's regression fixes. **Within M0134, selection is by task
ID ascending, and the IDs were RENUMBERED 2026-08-19 by user directive** so the
eighteen highest-value cases occupy M0134-0006..0023 (details in the M0134 section
preamble at the foot of this file). **M0134-0002, -0003, -0004 and -0005 are all
PARKED** — 0005 was parked 2026-08-19 by the same directive — and **0006
(`select_having.sql`) and 0007 (`select_implicit.sql`) were both CLOSED
2026-08-19 as stale `failed` statuses with no goopg change, and **0008
(`select_parallel.sql`) was PARKED 2026-08-19** — it asserts
`pg_stat_database.parallel_workers_launched` and goopg has no parallel-worker
execution path at all, so it is unreachable until a parallel-query milestone
lands (re-arm trigger recorded on the task), and **0009 (`select_views.sql`) was
PARKED 2026-08-19** — it needs three independent parser/DDL gaps (`?#` operator
lexing, unary prefix `#`, `CREATE SCHEMA ... CREATE TABLE` sub-commands) before
it can pass, though the loop that sized it landed a real engine fix out of it
(session identity for `current_user`/`session_user`, design
`docs/design/m0134-0009-session-user-identity.md`) — and **0010
(`predicate.sql`) was PARKED 2026-08-19** on the same pattern: sized at 18/22
diverging EXPLAINs with zero hard blockers but five independent root causes, of
which the loop shipped the smallest (single-baserel NOT NULL qual reduction,
design `docs/design/m0134-0010-notnull-qual-reduction.md`), taking it to 14/22;
the rest need outer-join nullability tracking first (re-arm trigger on the task)
— and **0011 (`subselect.sql`) was PARKED 2026-08-19** on the same pattern: sized at ~90-120 of ~335 statements diverging across seven independent root causes (no missing prerequisite — verified against `parallel_schedule`), of which the loop shipped the only contained one (`IN (subquery)` in a `JOIN ... ON` clause, design `docs/design/m0134-0011-join-on-sublink-catalog.md`), clearing all 5 of the case's SQLSTATE 0A000 errors; the highest-value remainder needs a parser-AST + join-tree refactor first (re-arm trigger on the task) — and **0012 (`update.sql`) was PARKED 2026-08-20** on the very same pattern — sized at 841 diff lines across eight root causes whose dominant bucket (multi-level partition row routing, ~300 lines) is REFACTOR-tier, while the loop shipped the one contained cause (LIST partition routing dropped every non-int/string/bool key kind, design `docs/design/m0134-0012-list-partition-numeric-routing.md`, 841 → 823 lines and 13 → 11 `^+ERROR`) — and **0013 (`insert.sql`) was PARKED 2026-08-20** on the identical pattern — sized at 1062 diff lines across eight root causes whose dominant bucket (INSERT target-list indirection, ~330 lines, unparsed grammar) is REFACTOR-tier, while the loop shipped the one contained correctness bug (a dropped DEFAULT partition left a stale parent-side bounds entry that blocked every future DEFAULT partition, design `docs/design/m0134-0013-default-partition-stale-bounds-cache.md`, 1062 → 1051 lines and 58 → 50 `^+ERROR`) — and **0014 (`mvcc.sql`) was PARKED 2026-08-20**: the standing "possible regression, verify" rule was applied first and the case still FAILS at HEAD (17 diff lines / 2 `^+ERROR`), so it is not a stale status and the CSV row stays `failed`; it has two serially-masked causes, of which the loop shipped the contained one (sublink-bearing PL/pgSQL expressions now fall back to the SQL engine, design `docs/design/m0134-0014-plpgsql-sublink-sql-fallback.md`), while the newly-unmasked one — `substitutePlpgsqlFrameVarsInSQL` binding plpgsql variables by TEXTUAL substitution before parsing, corrupting the alias list `g(i)` into `g(1)` — is REFACTOR-tier (parse-then-bind, PG'"'"'s parser-hook + `PARAM_EXTERN` model) with a re-arm trigger on the task — and 0020 (`stats.sql`) was PARKED 2026-08-20 after being RUN for the first time (its `not-tried` status resolved to genuinely failing, 1391 → 1351 diff lines / 101 → 80 `^+ERROR` after shipping the engine-wide transaction-scoped pgstat getters, design `docs/design/m0134-0020-xact-pgstat-getters.md`), and 0021 (`vacuum.sql`) was PARKED 2026-08-20 on the same pattern (496 diff lines / 18 `^+ERROR` at HEAD, six root-cause buckets, shipped the per-relation VACUUM/ANALYZE maintenance-permission check — two tiers, not one — design `docs/design/m0134-0021-vacuum-partition-child-permission.md`, taking it to 393 / 14 with all six ownership permutations of `expected/vacuum.out:593-684` now byte-identical), and 0022 (`window.sql`) was PARKED 2026-08-20 on the same pattern (4575 diff lines / 90 `^+ERROR` at HEAD, nine buckets, shipped the four-gate unification that lets ordinary aggregates be used as window functions, design `docs/design/m0134-0022-window-aggregate-gates.md`, taking it to 4604 / 64 — errors down 26 while lines rose 29, because rejections became value comparisons), and 0023 (`write_parallel.sql`) was PARKED 2026-08-20 after being RUN for the first time (its `not-tried` status resolved to genuinely failing, 86 -> 80 diff lines / **12 -> 0 `^+ERROR`** after shipping the same-transaction drop/recreate name-reuse fix, design `docs/design/m0134-0023-txn-drop-recreate-name-reuse.md`; the case is structurally unreachable because 55% of its expected output is parallel-plan-shaped and goopg has no parallel-worker execution path), and 0024 (`generated_virtual.sql`) was PARKED 2026-08-20 on the same pattern — re-run at HEAD confirmed it is not stale (4438 diff lines / 114 `^+ERROR`), and the loop shipped a fix that was NOT a generated-column bug at all but an engine-wide one the case merely exposed: unqualified `INHERITS`/`ALTER TABLE ... INHERIT` parent lookup ignored `search_path` (design `docs/design/m0134-0024-inherits-searchpath-lookup.md`, 4438 -> 4397 lines and 114 -> 96 `^+ERROR`), and 0025 (`groupingsets.sql`) was PARKED 2026-08-20 on the same pattern — re-run at HEAD confirmed it is not stale (2373 diff lines / 25 `^+ERROR`), and sizing found that **63% of the diff was a single cascading hunk caused by a real backend PANIC**, not by distinct mismatches: `resolveExprAfterAggregate` (`internal/optimizer/planner.go:7386`) hard-cast `resolveColumnRef`'s output to `*ColumnRef`, so ANY correlated outer-level reference in the target list of a subquery containing an aggregate killed the connection — reproduced live with NO grouping sets and even with NO GROUP BY, i.e. engine-wide, the case merely being the first regress file to combine LATERAL + aggregate + correlated target-list ref (design `docs/design/m0134-0025-lateral-outer-colref-aggregate-crash.md`, 2373 -> 2689 lines / 25 -> 41 `^+ERROR` with the connection-loss cascade GONE — the counts rising is the expected shape of progress and was PROVEN by a stash A/B: the pre-crash region that ran in both builds is byte-identical, every new error belongs to a statement that previously never executed, verdict NO REGRESSION); its two largest remaining buckets (grouping-sets aggregation STRATEGY selection and tied-row emission order) are REFACTOR-tier with a re-arm trigger on the task, and **0026 (`guc.sql`) was PARKED 2026-08-20** on the same pattern — re-run at HEAD confirmed it is not stale (767 diff lines / 27 `^+ERROR` / 11 `^-ERROR`) — and the loop shipped an **engine-wide silent wrong-value bug** the case merely exposed: a `timestamptz` input string carrying no zone was parsed with a zone-less Go layout that defaults the location to UTC, while PG reads those digits as local time in the session `TimeZone` GUC (`DecodeDateTime`, `postgres/src/backend/utils/adt/datetime.c:1573-1583`), so every zone-less `timestamptz` input in a non-UTC session stored the WRONG INSTANT with no error raised (design `docs/design/m0134-0026-timestamptz-literal-session-timezone.md`, **760 -> 536 diff lines, -224**). **That improvement is invisible under the default harness** (which reports 767 -> 767) because `scripts/pg-regress-runner.sh` never exports what real `pg_regress.c:764-804` sets, so the case never enters a non-UTC session — a FALSE NEGATIVE now ledgered as its own re-baselining task. Its remaining buckets (top-level `SET LOCAL` persisting with no `WarnNoTransactionBlock` warning, CONTAINED and the natural next slice; `ROLLBACK TO SAVEPOINT` not restoring GUCs, REFACTOR-tier; assorted missing builtins) are ledgered, and 0027 (`copy.sql`), 0028 (`horology.sql`) and 0029 (`identity.sql`) were each PARKED 2026-08-20 on the same pattern (see their own entries below for shipped-fix detail), and **0030 (`incremental_sort.sql`) was PARKED 2026-08-20** on the same pattern — sized at HEAD (953 diff lines / 14 `^+ERROR`), dominated (~700+ lines) by goopg having **no Incremental Sort plan node or executor at all** (REFACTOR-tier, own milestone), while the loop shipped the one contained, generically-useful fix among the 14 `^+ERROR`s: `targetMeta` (`internal/optimizer/planner.go:11566-11579`) had no arm for `*OuterColumnRef`, so a LATERAL derived table whose sole projected column is a bare correlated outer-column reference got its synthetic schema column mislabeled `?column?`, breaking qualified lookup from the outer query — a general LATERAL-subquery bug, not incremental_sort-specific (14 -> 8 `^+ERROR`), so the next M0134 task to select is **M0134-0031 (`copy2.sql`, status `failed`)** — 0015 (`join.sql`), 0016 (`create_table.sql`) 0018 (`create_index.sql`) and 0019 (`indexing.sql`) were each PARKED on the same pattern after shipping one contained fix, and 0017 (`hash_index.sql`) CLOSED green. **0031 (`copy2.sql`) was PARKED 2026-08-20** on the same pattern — sized at HEAD (955 diff lines / 60 `^+ERROR`), dominated by three REFACTOR-tier missing-feature buckets (legacy `WITH DELIMITER AS`/`NULL AS` option grammar ~90 lines; `COPY ... FROM stdin WHERE <expr>` unparsed ~120+ lines; `COPY ... WITH (DEFAULT '…')` option unimplemented ~150 lines), while the loop shipped the smallest CONTAINED bucket: PG's CSV writer force-quotes a field colliding with the NULL marker or (single-column) the `\.` EOF marker (`CopyAttributeOutCSV`, `postgres/src/backend/commands/copyto.c:1300-1350`), a rule `EncodeCopyCsvRow` (`internal/executor/copy_csv.go:126-144`) lacked entirely — design `docs/design/m0134-0031-copy-csv-force-quote-null-eof.md`, 955 → 888 diff lines (`^+ERROR` unchanged at 60, all remaining belong to the excluded buckets). **0032 (`inherit.sql`) was PARKED 2026-08-20** on the same pattern — sized at HEAD (3310 diff lines / 38 `^+ERROR` / 40 `^-ERROR`), dominated by a REFACTOR-tier missing subsystem (the ALTER TABLE inheritance validation matrix, ~1000+ lines) plus several independent smaller gaps (EXPLAIN plan-shape divergence, `pg_get_expr`/CHECK-constraint raw-text deparse, a `circle` GiST opclass gap, an unconfirmed ORDER BY/Sort correctness bug, DROP CASCADE ordering), while the loop shipped the smallest CONTAINED bucket: `buildForeignKeyDefString` (`internal/executor/expr.go:6345`) unconditionally schema-qualified `pg_get_constraintdef`'s FK `REFERENCES` clause regardless of session search_path visibility — design `docs/design/m0134-0032-fk-constraintdef-schema-visibility.md`, 3310 → 3300 diff lines, the `test_foreign_constraints_id1_fkey` mismatch fully resolved, and **0033 (`create_procedure.sql`) was PARKED 2026-08-20** on the same pattern — sized at HEAD (131 diff lines / 2 `^+ERROR` / 1 `^-ERROR`), a small case with five independent root causes, two REFACTOR-tier (missing `LINE N`/`^` error-position pointer — `Pos == 0` sentinel collision across the wire-encoding guards in `internal/postmaster/copy.go`/`txn_verb.go`; `pg_get_functiondef` BEGIN-ATOMIC body deparse is raw-text substitution not AST re-deparse) and one a standalone engine-wide gap (no `ACL_EXECUTE` enforcement anywhere in `internal/executor`, so `CALL` on an EXECUTE-revoked procedure never raises "permission denied"), while the loop shipped the smallest CONTAINED bucket: `internal/executor/operators_ddl.go:16743-16753`'s DROP PROCEDURE/FUNCTION not-found branch unconditionally attached the CALL-only "No procedure matches…" HINT, which real PG's DROP name-resolution path (`LookupFuncName`, `namespace.c`) never emits — design `docs/design/m0134-0033-drop-procedure-notfound-hint.md`, 131 → 124 diff lines, and **0034 (`insert_conflict.sql`) was PARKED 2026-08-20** on the same pattern — sized at HEAD (539 diff lines / 2 `^+ERROR` / 9 `^-ERROR`), six independent root causes, of which the loop shipped the CONTAINED one: `resolveArbiterIndex` (`internal/optimizer/planner.go:10691-10806`) used subset/liberal `ON CONFLICT` arbiter-index matching instead of PG's exact-set match (`bms_equal` for plain columns, real expression-AST equality for expression columns via new `parserExprStructEqual`), silently accepting and *executing* ON CONFLICT clauses PG rejects — a genuine data-correctness bug, not cosmetic — design `docs/design/m0134-0034-arbiter-index-exact-set-match.md`, 539 → 422 diff lines, 9 → 6 `^-ERROR` (one new `^+ERROR` also surfaced — a pre-existing, out-of-scope check-ordering gap between arbiter resolution and UPDATE-SET-target validation in `planOnConflict`, ledgered separately — net improvement accepted). Remaining buckets (EXPLAIN plan shape for ON CONFLICT, GiST exclusion-constraint physical enforcement, `excluded` whole-row WHERE-clause reference, attached-partition local-index arbiter lookup) are REFACTOR-tier or unsized, ledgered, so the next M0134 task to select is **M0134-0035 (`interval.sql`, status `failed`)**. **M0134-0159 (`regproc.sql`) was PARKED 2026-08-29** on the same pattern — sized live for the first time (`not-tried` → `failed`, 766 → 758 diff lines / `^+ERROR` 63 → 61) — and the loop shipped an **engine-wide** fix the case merely exposed rather than anything regproc-specific: goopg's compat tier classifies the statement classes the grammar deliberately does not carry (role DDL, database DDL) by prefix-matching `normalizeCompatSQL`, which kept comments verbatim where PG's lexer folds them into `{whitespace}` (`scan.l:213-215`), so **CREATE/ALTER/DROP ROLE|USER|GROUP and CREATE/ALTER/DROP DATABASE were unreachable from any commented SQL script** (design `docs/design/m0134-0159-sql-comment-stripping-compat-intercepts.md`); its remaining buckets are filed as 0159a/0159b/0159c. **M0134-0160 (`reloptions.sql`) was PARKED 2026-08-29** on the same pattern — sized live for the first time (`not-tried` → `failed`, 232 → **201** diff lines / `^+ERROR` 17 → **6**) — and the loop again shipped an **engine-wide** fix the case merely exposed: goopg validated storage parameters only by *recognising* them, so a `WITH (...)` name nobody looked for was silently accepted and dropped (`CREATE TABLE t(i int) WITH (not_existing_option=2)` SUCCEEDED, as did the CREATE INDEX / ALTER TABLE SET / bad-namespace forms), where PG raises 22023 from `parseRelOptions`/`transformRelOptions` (`reloptions.c:1488`/`:1275`) — a silent-acceptance correctness gap that also cascades into spurious "relation already exists" on every later negative case (design `docs/design/m0134-0160-reloption-name-registry.md`; **twelve of a 14-case regress A/B byte-identical**, `alter_table` 3792 → 3784). Its remaining buckets are filed as 0160a/0160b. **M0134-0161 (`replica_identity.sql`) was PARKED 2026-08-29** on the same pattern — sized live for the first time (`not-tried` → `failed`, 194 → **189** diff lines / `^-ERROR` 3 → **2**) — and the loop again shipped an **engine-wide** fix the case merely exposed: `pg_index.indimmediate` is keyed on the `DEFERRABLE` flag ALONE (`index.c:1049`, `index.c:2080-2082`), never on INITIALLY DEFERRED, and seven goopg consumers had drifted into three different answers — both pg_index row builders hardcoded `true`, the `REPLICA IDENTITY USING INDEX` check and both ON CONFLICT arbiter checks keyed on `InitiallyDeferred`, and the inferred-by-column arbiter branch had no check at all — so a `UNIQUE (b) DEFERRABLE` constraint was silently ACCEPTED as both a replica identity and an ON CONFLICT arbiter where PG rejects it (design `docs/design/m0134-0161-indimmediate-deferrable-key.md`; 13-case regress A/B, **twelve byte-identical**, zero regressions). Its remaining buckets are filed as 0161a-0161h. **M0134-0162 (`roleattributes.sql`) was CLOSED GREEN 2026-08-29** — the first full close in this run of the sequence, not a park: sized live for the first time at 28 diff lines, whose entirety was ONE root cause, and the case now passes byte-identically (`not-tried` → **`pass`**, 28 → **0**). `[NO]INHERIT` was accept-and-ignore engine-wide (`catalog.RoleAttrs` had no `Inherit` field, `applyRoleAttrOptions` never probed for it, and both `pg_authid` row builders hardcoded `rolinherit = 't'`), and the fix had to carry a second, non-cosmetic half: `rolinherit` is PG's default for `pg_auth_members.inherit_option` (`user.c:1924-1939`) and goopg's `HasPrivsOfRole`/`SelectBestAdmin` traverse only inherit-marked rows, so shipping the catalog column alone would have left a NOINHERIT role inheriting every privilege of every role granted to it (design `docs/design/m0134-0162-rolinherit-attribute.md`; 8-case regress A/B vs a HEAD worktree, zero regressions). Its remaining buckets are filed as 0162a-0162c. **M0134-0163 (`rowsecurity.sql`) was PARKED 2026-08-29** on the same pattern — sized live for the first time (`not-tried` → `failed`, `^+ERROR` 301 → **137**, of which spurious `permission denied for table` 165 → **11**) — and the loop shipped an **engine-wide ACL** fix the case merely exposed (GRANT on an unqualified table outside `public` recorded nothing; `HasTablePrivilege` was not `aclmask()`, so `GRANT … TO PUBLIC` and group grants conferred nobody anything); its remaining buckets are filed as 0163a-0163c. **M0134-0164 (`sanity_check.sql`) was PARKED 2026-08-29** on the same pattern — sized live for the first time (`not-tried` → `failed`, 77 → **21** diff lines) — and the loop again shipped an **engine-wide** fix the case merely exposed: `pg_class.relfilenode` was the relation OID for EVERY relkind in both virtual builders, where PG leaves the storage-less kinds (`v`/`c`/`f`/`p`/`I`) at 0 (`RELKIND_HAS_STORAGE`, `pg_class.h:200`; `heap_create`, `heap.c:335-345`) — a four-way sibling divergence in which the heap builder, initdb and the composite builder each already had (some of) the right answer and the runtime virtual builder had none of it (design `docs/design/m0134-0164-relfilenode-storage-less-relkinds.md`; 13-case regress A/B, **10 byte-identical**, zero regressions, and `alter_table` 3800 → 3798 as independent confirmation). Its remainder is filed as 0164a. **M0134-0165 (`security_label.sql`) CLOSED GREEN 2026-08-29** — the second full close in the run sequence, not a park: sized live for the first time at 16 diff lines, the entirety of which was ONE root cause, and the case now passes byte-identically (`not-tried` → **`pass`**, 16 → **0**). The `SECURITY LABEL` statements themselves already matched; the diff was the case's `SET client_min_messages TO 'warning'` scaffold failing to suppress two `DROP ROLE IF EXISTS` NOTICEs. **Engine-wide**: `client_min_messages` was a GUC declaration that NOTHING consumed, so every NOTICE/WARNING goopg ever produced reached the client unconditionally; fixed at the single wire choke point (`WriteNoticeResponse` + a per-connection `FrameWriter.ClientMinMessagesFn` hook) mirroring `should_output_to_client` (`elog.c`), design `docs/design/m0134-0165-client-min-messages-notice-filter.md`; 16-case regress A/B net **−127 diff lines, zero regressions**. Remaining buckets filed as 0165a/0165b. **The next M0134 task to select is M0134-0166 (`float8.sql`, status `failed`).** **M0134-0166 (`float8.sql`) PARKED 2026-08-29** on the same pattern — sized live for the first time (1311 diff lines / 56 `^+ERROR`) — and the loop again shipped an **engine-wide** fix the case merely exposed: `evalCast` had **no `KindString` arm at all** for `float4`/`float8`, so `'10e400'::float8` and even `'N A N'::float8` returned the raw text unvalidated; float input existed four times in four different states and now shares one `float8in_internal`-faithful `floatIn` (design `docs/design/m0134-0166-float8in-shared-input.md`; 20-case regress A/B, `float4` −192, `float8` −107, **18 byte-identical**, zero regressions). Remaining buckets filed 0166a–0166d. **M0134-0167 (`spgist.sql`) was PARKED 2026-08-29** on the same pattern — sized live for the first time (`not-tried` → `failed`, **23 diff lines, ONE hunk**), and the case's entire divergence is that goopg has **no SP-GiST access method at all** (`USING spgist` registers catalog metadata only), which is REFACTOR-tier; everything else in the file already matched byte-for-byte. The loop again shipped an **engine-wide** fix the case merely exposed: `catalog.IndexAMCapability` (M0134-0090) is a 1:1 mirror of the six in-tree AMs' `IndexAmRoutine` flags that had **one consumer**, `pg_indexam_has_property` — so the compat surface could REPORT that spgist cannot do unique indexes while `execCreateIndex` happily created one. Of upstream's five `DefineIndex` capability checks (`indexcmds.c:869/874/879`, `:2228/2233`) goopg enforced one, as a hardcoded AM-name list, so `CREATE UNIQUE INDEX … USING spgist|gist|gin|brin|hash`, multicolumn spgist/hash and `DESC`/`NULLS FIRST` on every orderless AM were silently ACCEPTED — not cosmetic, since those four AMs are catalog-only in goopg and the index advertised `indisunique` while enforcing nothing (design `docs/design/m0134-0167-index-am-capability-gate.md`; 15-statement oracle A/B byte-identical, 15-case regress A/B zero regressions, guarded by a revert-checked unit test because **no upstream regress case exercises any of the five errors**). Its remaining buckets are filed as 0167a–0167d, so the next M0134 task to select is **M0134-0168 (`sqljson.sql`, status `not-tried`)**. **M0134-0168 (`sqljson.sql`) was PARKED 2026-08-29** on the SQL/JSON constructor subsystem (ledger 0168a), shipping the engine-wide `'name'::regclass` → `regclassin` delegation instead. **M0134-0169 (`sqljson_jsontable.sql`) was PARKED 2026-08-29** on the SAME blocker — sized live for the first time (`not-tried` → `failed`, 1347 → **1335** diff lines / `^+ERROR` 116 → **115**), its 90 syntax errors resolving to just four tokens (`COLUMNS` x68, `PASSING` x12, `AS` x9 — all `JSON_TABLE` — and `(` x1). The lone `(` was a separate, **engine-wide grammar bug**: CTAS's source and both view bodies took `select_bare`, so `CREATE TABLE t AS (SELECT 1)`, `CREATE VIEW v AS (SELECT 1)` and `CREATE MATERIALIZED VIEW mv AS (SELECT 1)` were rejected where upstream's `CreateAsStmt` (`gram.y:4807`) and `ViewStmt` (`:11287`) take `SelectStmt`. It had been recorded as intentional in three places (grammar comment, `assertBothReject` guard, golden corpus), each a faithful record of the LEGACY parser's limit mistaken for a PostgreSQL rule (design `docs/design/m0134-0169-ctas-view-source-parenthesised-query.md`; four productions changed, **no new grammar conflicts**; 15-case regress A/B, 11 byte-identical, `privileges` 3885 → 3878, zero regressions). Its remainder is filed as 0169a/0169b, so **the next M0134 task to select is M0134-0170 (`sqljson_queryfuncs.sql`, status `not-tried`)** — note 0168a gates it too, so expect the same sizing-and-park unless the SQL/JSON subsystem is opened. **M0134-0170 (`sqljson_queryfuncs.sql`) was PARKED 2026-08-29** on that same blocker, as predicted — sized live for the first time (`not-tried` → `failed`, **2021 diff lines / 259 `^+ERROR` / 113 `^-ERROR`**), 100% of which is the SQL/JSON **query-function** family (218 syntax errors over `RETURNING` x121 / `PASSING` x38 / `DEFAULT` x11 / …, 33 `function json_exists|json_value|json_query does not exist`, 8 relation-cascade). The loop again shipped an **engine-wide** fix the case merely POINTS AT but cannot itself exercise: its `:360-389` block is 39 `CREATE INDEX ON t (JSON_QUERY(js, '…'))` statements asserting `functions in index expression must be marked IMMUTABLE` 28 times, and **goopg implemented that check nowhere** — `CREATE INDEX ON t ((clock_timestamp()::text))`, `((a + (random()*10)::int))`, a user VOLATILE/STABLE function key and `WHERE a > (random()*10)::int` were all accepted, which is a correctness bug, not a cosmetic one (an index entry computed from a non-IMMUTABLE expression cannot be reproduced at probe time, so the index silently disagrees with the heap). It is the *sibling paths must agree* pattern again: goopg had ONE of upstream's three ports of the same predicate (`validatePartKeyExprInner` = `ComputePartitionAttrs`, `tablecmds.c:19966`) and neither index half (`CheckPredicate` `indexcmds.c:1843-1857` called at `:906`; `ComputeIndexAttrs` `:2016-2019`) — and the survivor had its own gap, consulting only user-defined routines so a bare volatile BUILT-IN in a partition key slipped through. All three now share one `pg_proc.dat`-derived classifier (design `docs/design/m0134-0170-index-expression-mutability.md`; 14-case DDL-heavy regress A/B, **13 byte-identical, zero regressions**; guard revert-checked, and necessary because the two messages appear in exactly ONE upstream expected file — the case that cannot run). The loop also made `scripts/pg-regress-runner.sh` fail loudly on a busy auto-start port and on a `psql` exit-2 connection loss: both used to yield a plausible-looking case size, and this very case was first "sized" at a fabricated 1291 lines. Its remainder is filed as 0170a-0170c, so **the next M0134 task to select is M0134-0171 (`foreign_key.sql`, status `failed`)**. **M0134-0171 (`foreign_key.sql`) was PARKED 2026-08-29** on the same pattern — re-sized at HEAD (stays `failed`, **3490 → 3343 diff lines / `^+ERROR` 279 → 253**), its residual being REFACTOR-tier (113 cascaded `relation does not exist` plus the partitioned-FK `fkpart*` matrix) — and the loop again shipped an **engine-wide** fix the case merely exposed, this time a silent **data-integrity** bug rather than a message gap: an FK written `REFERENCES <table>` with no referenced-column list takes the referenced table's PRIMARY KEY (`transformFkeyGetPrimaryKey`, `tablecmds.c:13382`, called from `ATAddForeignKeyConstraint` `:10190`), but goopg's `pkColumns` returned the referenced table's **first column** — under a doc comment that already described the index scan its body never performed, the *dead-documentation* twin of *dead code is not a reference implementation*. It produced three wrong answers and the shape of the existing coverage is why only the harmless one was ever exercised: a **multi-column PK** yielded 1 of N columns, so the arity mismatch made the check compare N values against 1 column and **every valid row was rejected** with a bogus 23503; a **single-column PK that is not column 1** silently enforced the FK against the **WRONG column** (PG refuses to create that constraint at all, and the `"id"` in its DETAIL is proof upstream resolved the PK); PK-is-column-1 was right by luck, and every pre-existing FK test used exactly that shape. Fixed via the `IndexesOnTable` accessor already on the catalog interface, with `ctx` threaded to all five consumers, and deliberately kept at the runtime resolver: `execCreateTable` registers FKs (`:3758`/`:3795`) BEFORE it creates the PK index (`:4024`), so upstream's store-it-concretely model would break self-referencing FKs — pinned by a `SelfReferencingFK` subtest and ledgered as 0171a along with the `pg_constraint.confkey = {}` divergence it leaves behind (design `docs/design/m0134-0171-fk-omitted-refcolumns-primary-key.md`; 14-case regress A/B, **13 byte-identical, zero regressions**). The one bucket that ROSE is the expected shape of progress: upstream's block headed *"Test a primary key with attributes located in later attnum positions compared to the fk attributes"* previously ran against the wrong column and produced garbage, and now executes correctly, diverging only on the auto-generated constraint NAME (0171c). Its remainder is filed as 0171a-0171d, so **the next M0134 task to select is M0134-0172 (`stats_ext.sql`, status `not-tried`)**. **M0134-0172 (`stats_ext.sql`) was PARKED 2026-08-29** on the extended-statistics subsystem (ledger rows 0172a-0172c), shipping the engine-wide `RETURN QUERY` / `FOR ... IN <query>` frame-variable substitution fix instead. **M0134-0173 (`stats_import.sql`) was PARKED 2026-08-29** on the same pattern — sized live for the first time (`not-tried` -> `failed`, 1461 -> **1457** diff lines / `^+ERROR` 74 -> **73**), ~100% of its residual being the PG 18 statistics-IMPORT function family plus the absence of a queryable `pg_statistic` relation (ledger rows 0173c/0173d) — and the loop again shipped an **engine-wide** fix the case merely POINTS AT with one statement: goopg treated every range-typed value as **opaque, unvalidated text**, so `'garbage'::int4range` succeeded and, far worse, **no discrete range was ever canonicalized**, making `'[1,4]'::int4range` and `'[1,5)'::int4range` — the same value in PG — compare UNEQUAL through every equality, `ORDER BY`, btree probe and exclusion constraint (design `docs/design/m0134-0173-range-type-input-and-constructors.md`; `rangetypes` 2543 -> **2166** lines / 234 -> **182** `^+ERROR` in a 14-case A/B with zero regressions). So **the next M0134 task to select is M0134-0174 (`subscription.sql`, status `not-tried`)**. 0019's shipped fix was the widest-reaching yet — the `^` operator was missing from the parser entirely, so it was an engine-wide parse failure rather than a case-local gap. Standing rule for the ONE remaining
"possible regression, verify" case (`reindex_catalog`; `mvcc` was checked and is
genuinely failing): re-run `scripts/pg-regress-runner.sh --verbose <case>` at HEAD FIRST
and, if it already passes, flip the CSV row to `pass` / `pass_required=yes`
with a "stale — already fixed" note instead of implementing anything. M0132 and M0133 are COMPLETE,
M0131 is closed except S24 (durable MultiXact SLRU — explicitly DEFERRED with an
executable re-arm trigger; not selectable), and M0130 is closed. Below M0134 the
next milestones are **M0119 (deferral-ledger backlog consumption)**, then M0122 —
the two remaining M0119 items (M0119-0005 pg_waldump server tier, M0119-0006
pg_amcheck server tier) are selected top-to-bottom from this file.
Document order does NOT reflect priority here; this banner does.
**M0119 selection rule: pick a M0119 task ONLY when no milestone above M0119 in
the priority order has a remaining task that should be done** — "should be done"
= unchecked and not parked or deferred, with prerequisites met. M0119 is a
backlog-consumption milestone and the terminal drain: it never runs ahead of
feature/build work. M-NIGHTLY's standing filing obligation is not itself a bar;
its tasks block M0119 only while selectable per the M-NIGHTLY selection rule
(after M0134 clears). M0122 (below M0119) does not block M0119.
The banner is the sole ordering authority — `.ralph/working_set.md`'s notes carry
state, not priority, and do not outrank it.

**M-NIGHTLY selection rule (inherits prior M-NIGHTLY procedure §2, per
ci/design/07-ralph-feedback.md §B):**
1. Before investigating, re-run the item's repro at HEAD — the log reflects the
   last nightly run and may be stale.
2. Fix with the normal gates (practice cards apply), cite the AI-id in the
   commit message, check the task off.
3. The next nightly run confirms and drops the item from the log.

## M-NIGHTLY — Nightly regression triage (STANDING — ACTIVE since 2026-08-08)

<!-- Standing milestone: never complete it, never archive it, keep it directly
     under the Current Priority banner. Source of work: ci/logs/action-items.md
     (regenerated by every nightly batch run; design ci/design/07-ralph-feedback.md).
     As of 2026-08-09 ALL priority milestones (M0124/M0125/M0127/M0128/M0123/M0129) are
     CLOSED. **As of 2026-08-15 M0134 is the next-priority milestone after
     M-NIGHTLY** (user directive 2026-08-15 — see the Current Priority banner and
     the M0134 section at the END of this file), ahead of M0119 and M0122's
     remaining items;
     M-NIGHTLY filing stays unconditional, and selection is subordinate to M0134
     while any M0134 task is unchecked, then to M0119, then to M0122.
     Loop rule:
       1. Read ci/logs/action-items.md (absent file = nothing to do). For each
          `## AI-` item whose `subject:` has no OPEN (unchecked) task below,
          add one task:
            - [ ] <subject> — <one-line what> (AI-<id>; repro: <cmd>)
          If an unchecked task for the same subject already exists, do NOT add
          another — append the new AI-id to that task's line instead. If only a
          CHECKED task exists for the subject, the failure REOPENED: add a new
          task and note the earlier fix didn't hold.
       2. Before investigating, re-run the item's repro at HEAD — the log
          reflects the last nightly run and may be stale; if it passes, check
          the task off with a "stale — already fixed" note.
       3. Fix with the normal gates (practice cards apply), cite the AI-id in
          the commit message, check the task off. The next nightly run confirms
          and drops the item from the log.
     (Tasks are added here by the in-loop agent, one per subject. This
     placeholder is a comment, not a checkbox, so the plan-complete exit
     heuristic stays live.) -->



### Nightly run 20260805-014309 (2 items, sha `ce027cee` — status fail)

**The run shrank 17 → 2.** The 15 `regress/*` "output mismatch" subjects and the
`regress/suite-wedge` item filed for 20260802/03/04 are ABSENT tonight, which is
the first independent evidence for the phantom-divergence-downstream-of-the-wedge
reading recorded above (they were never 15 regressions). Their tasks stay open —
one clean night is not a fix — but do not re-file them per night.

- [ ] **testport/TestPort_PgDumpConnectionSetup (AI-20260822-001356-001)** — new
      tonight. Repro: `go test -v -run '^TestPort_PgDumpConnectionSetup$'
      ./internal/testport/`. Evidence:
      `ci/logs/20260822-001356/testport/go-test.log`. (Re-failed in
      20260823-011911: `ci/logs/20260823-011911/testport/go-test.log`.)
- [ ] **testport/TestPort_RegressSuite — limit, numerology (AI-20260822-001356-002)**
      — new tonight. Repro: `go test -v -run '^TestPort_RegressSuite$'
      ./internal/testport/`. Evidence:
      `ci/logs/20260822-001356/testport/go-test.log`.
- [ ] **race/stage (AI-20260822-001356-004)** — stage race FAILED with no
      parseable cause; inspect stage logs before triaging further. Repro: `bash
      ci/batch/stages/stage-race.sh` (REPO_ROOT/RUN_DIR set; see
      `ci/logs/20260822-001356/race/`). Evidence:
      `ci/logs/20260822-001356/race/stage.log`. Note item -005 below records that
      the working tree mutated mid-run (a concurrent Ralph loop's edit/commit),
      so this result may be contaminated — re-verify at a clean HEAD before
      trusting it.
- [ ] **race/build-broke-mid-stage (AI-20260822-001356-005)** — `internal/executor`
      failed to compile mid-stage so it never ran (not a regression by itself);
      the working tree mutated mid-run (preflight fp `8a4496c2c9eed0be`, race fp
      `e7b294ceca30ceee`) — a concurrent edit/commit, not a code defect. Repro:
      `go build ./...` at the run's recorded sha (clean sha ⇒ nothing to fix in
      the code). Evidence: `ci/logs/20260822-001356/race/go-test.log`.

### Nightly run 20260824-013441 (sha `e7495e712dda`, 2 items) — filed 2026-08-24

Filed per the standing M-NIGHTLY obligation; NOT selected on the filing loop
(the Current Priority banner puts M0134 next-priority and its M0134-0091 task
had just landed). Item -002 is a repeat of the already-open
AI-20260822-001356-003 row above (see that row's update). Triage each per the
M-NIGHTLY selection rule before working (re-run the repro at HEAD first).

- [ ] **units/testport — TestPort_PgDumpConnectionSetup FAILed (AI-20260825-003932-001)**
      — new tonight. `go test -v -run '^TestPort_PgDumpConnectionSetup$'
      ./internal/testport/`. Evidence:
      `ci/logs/20260825-003932/testport/go-test.log`.
- [ ] **testport/TestPort_InitiallyDeferredFKCommit (AI-20260827-052222-013)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationAbortedKeyrevoke (AI-20260827-052222-016)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationAlterTable2 (AI-20260827-052222-017)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationAlterTable3 (AI-20260827-052222-018)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationAlterTable4 (AI-20260827-052222-019)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationClassroomScheduling (AI-20260827-052222-020)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationClusterConflictPartition (AI-20260827-052222-021)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationCreateTrigger (AI-20260827-052222-022)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationDeleteAbortSavept (AI-20260827-052222-023)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationDeleteAbortSavept2 (AI-20260827-052222-024)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationDetachPartitionConcurrently4 (AI-20260827-052222-025)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationDropIndexConcurrently1 (AI-20260827-052222-026)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationEvalPlanQual (AI-20260827-052222-027)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationFkContention (AI-20260827-052222-028)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationFkDeadlock (AI-20260827-052222-029)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationFkSnapshot (AI-20260827-052222-030)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationHorizons (AI-20260827-052222-031)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationInsertConflictDoNothing2 (AI-20260827-052222-032)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationInsertConflictDoUpdate2 (AI-20260827-052222-033)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationInsertConflictDoUpdate3 (AI-20260827-052222-034)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationInsertConflictDoUpdate4 (AI-20260827-052222-035)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationInsertConflictSpecconflict (AI-20260827-052222-036)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationIntraGrantInplace (AI-20260827-052222-037)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationLockCommittedKeyupdate (AI-20260827-052222-038)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationLockCommittedUpdate (AI-20260827-052222-039)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationLockNowait (AI-20260827-052222-040)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationLockUpdateDelete (AI-20260827-052222-041)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationLockUpdateTraversal (AI-20260827-052222-042)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationMatviewWriteSkew (AI-20260827-052222-043)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationMergeMatchRecheck (AI-20260827-052222-044)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationMergeUpdate (AI-20260827-052222-045)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationMultipleCic (AI-20260827-052222-046)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationNowait (AI-20260827-052222-047)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationNowait2 (AI-20260827-052222-048)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationNowait3 (AI-20260827-052222-049)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationNowait4 (AI-20260827-052222-050)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationNowait5 (AI-20260827-052222-051)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationPartialIndex (AI-20260827-052222-052)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationPartitionDropIndexLocking (AI-20260827-052222-053)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationPlpgsqlToast (AI-20260827-052222-054)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationPredicateGin (AI-20260827-052222-055)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationPredicateGist (AI-20260827-052222-056)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationPredicateHash (AI-20260827-052222-057)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationPreparedTransactionsCIC (AI-20260827-052222-058)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationPropagateLockDelete (AI-20260827-052222-059)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationReadOnlyAnomaly (AI-20260827-052222-060)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationReadOnlyAnomaly2 (AI-20260827-052222-061)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationReadOnlyAnomaly3 (AI-20260827-052222-062)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationReceiptReport (AI-20260827-052222-063)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationSerializableParallel (AI-20260827-052222-064)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationSerializableParallel2 (AI-20260827-052222-065)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationSerializableParallel3 (AI-20260827-052222-066)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationSkipLocked (AI-20260827-052222-067)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationSkipLocked2 (AI-20260827-052222-068)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationSkipLocked3 (AI-20260827-052222-069)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationSkipLocked4 (AI-20260827-052222-070)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationStats (AI-20260827-052222-071)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationTimeouts (AI-20260827-052222-072)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationTuplelockConflict (AI-20260827-052222-073)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationTuplelockPartition (AI-20260827-052222-074)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationTuplelockUpdate (AI-20260827-052222-075)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationTuplelockUpgradeNoDeadlock (AI-20260827-052222-076)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationTwoIds (AI-20260827-052222-077)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationUpdateConflictOut (AI-20260827-052222-078)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_IsolationVacuumNoCleanupLock (AI-20260827-052222-079)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_LockRowsSortOverJoinTakesRowLock (AI-20260827-052222-080)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NonPartitionedDeferredFKStillCatchesViolationAtCommit (AI-20260827-052222-082)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullAddConstraintCascadesUnderParentName (AI-20260827-052222-083)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullAddConstraintNotValidCascadesNotValid (AI-20260827-052222-084)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullAttachPartitionAbsorbs (AI-20260827-052222-085)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullAttachPartitionMissingChildConstraint (AI-20260827-052222-086)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullAttachPartitionNotValidConflict (AI-20260827-052222-087)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullAttachPartitionStillClearsLocal (AI-20260827-052222-088)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullCascadeSkipsUnrelatedSibling (AI-20260827-052222-089)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullCascadesMultiLevel (AI-20260827-052222-090)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullDetachPartitionUnabsorbs (AI-20260827-052222-091)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullDiamondConinhcount (AI-20260827-052222-092)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullInheritAbsorbsButKeepsLocal (AI-20260827-052222-093)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullInheritNoInheritCycleDoesNotDriftCoinhcount (AI-20260827-052222-094)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullInheritTransactionalFormAbsorbs (AI-20260827-052222-095)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullNoInheritUnabsorbs (AI-20260827-052222-096)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullSetNotNullCascadesToChildren (AI-20260827-052222-097)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_NotNullSetNotNullOnExistingDoesNotDoubleCascade (AI-20260827-052222-098)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PartitionedDeferrableUniqueDefersToCommitViaSetConstraintsAll (AI-20260827-052222-099)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PartitionedDeferrableUniqueDefersToCommitViaSetConstraintsNamed (AI-20260827-052222-100)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PartitionedDeferrableUniqueNamedChildOwnConstraintStillDefers (AI-20260827-052222-101)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PartitionedDeferredFKCatchesViolationAtCommit (AI-20260827-052222-102)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PartitionedDeferredFKMultiLevelCatchesViolationAtCommit (AI-20260827-052222-103)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PartitionedDeferredFKSatisfiedCommitsCleanly (AI-20260827-052222-104)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PartitionedUniqueConstraintFansOutToPgConstraint (AI-20260827-052222-105)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PgAmcheck005OpclassDamage (AI-20260827-052222-106)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PgDumpConnectionSetup (AI-20260827-052222-107)** — repeat of the already-open task for this subject above; see that row (re-failed tonight).
- [ ] **testport/TestPort_PgDumpDatabaseConfigSet (AI-20260827-052222-108)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PgDumpDatabaseGrantACL (AI-20260827-052222-109)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PgDumpRoleConfigSet (AI-20260827-052222-110)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PgDumpallGlobalsOnly (AI-20260827-052222-111)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PgDumpallParameterACL (AI-20260827-052222-112)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PgDumpallPredefinedRoleMembership (AI-20260827-052222-113)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PgDumpallRoleMembership (AI-20260827-052222-114)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_PgoutputInteropGoopgToPG (AI-20260827-052222-115)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_RegressSuite (AI-20260827-052222-116)** — repeat of the already-open task for this subject above; see that row (re-failed tonight).
- [ ] **testport/TestPort_SetConstraintsDeferral (AI-20260827-052222-117)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_TimeoutsRowLevel (AI-20260827-052222-121)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_TwoPhaseCommitSameBackend (AI-20260827-052222-122)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestPort_ZeroColumnJoinDoesNotCrashBackend (AI-20260827-052222-123)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestSyntax_DML_Delete (AI-20260827-052222-125)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestSyntax_DML_Update (AI-20260827-052222-126)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestSyntax_Locking_ForShare (AI-20260827-052222-127)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestSyntax_Locking_ForUpdate (AI-20260827-052222-128)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestSyntax_Locking_Nowait (AI-20260827-052222-129)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestSyntax_Select_Case (AI-20260827-052222-130)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.
- [ ] **testport/TestSyntax_Select_CurrentSetting (AI-20260827-052222-131)** — presumed stale (same mid-migration parser sha); pending the 20260828-235424 verdict.

Non-testport items from the same run:

- [ ] **tpcds/stage (AI-20260827-052222-132)** — stage tpcds FAILED (fail(sweep)) with no parseable cause — inspect the stage logs. Evidence: `ci/logs/20260827-052222/`.
- [ ] **tpch/Q5-timeout (AI-20260827-052222-133)** — Q5 hit its per-query budget (57014/cancel). Evidence: `ci/logs/20260827-052222/`.


### Nightly run 20260828-235424 (sha `5773b884c5bf`, 2 items) — filed 2026-08-29

Both subjects already had M-NIGHTLY rows; no new subjects tonight. Recorded here
for run-to-row traceability only.

- [ ] **tpch/Q5-timeout (AI-20260828-235424-002)** — duplicate of the still-open
      AI-20260827-052222-133 row above (Q5 per-query budget, 57014/cancel).
      Evidence: `ci/logs/20260828-235424/tpch/run.log`.

### Nightly run 20260831-013952 (sha `c051b81fa596`, 2 items) — filed 2026-09-01

`meta.json` records 18 dirty files at run start (`.claude/settings.local.json`,
`.ralph/progress.json`, `postgres`, `third-party/tpcds-postgres`, …) — same
shared-working-tree contamination pattern as prior "presumed stale" rows.
`go build ./...` is clean at current HEAD (`b43609840`, a descendant of the
run's sha), so nothing here blocks the build.

- [ ] **testport/stage (AI-20260831-013952-001)** — `TestPort_IsolationSuite`
      panicked with "test timed out after 2h0m0s"; NOT the "no parseable
      cause" the nightly report claims — the dump shows 17 specs still
      `[running]` at 1h56m (e.g. `deadlock-hard`, `multiple-row-versions`,
      `prepared-transactions-cic`, `two-ids`) each blocked on a `lib/pq`
      connection stuck in `IO wait` (`conn.recv1` never returning), i.e. some
      isolation-suite session's goopg backend stopped answering.
      Evidence: `ci/logs/20260831-013952/testport/go-test.log:20168` (panic
      dump), `stage.log` (`testport FAIL (rc=1)`). **Needs a clean-tree
      confirmation run before treating as a real hang** — this run's 18 dirty
      files include a concurrently-modified `postgres` submodule and shared
      Ralph state, so a rebuild racing the isolation harness's own goopg
      server (same shape as AI-20260831-013952-002 below) is at least as
      likely as a genuine deadlock. Repro:
      `go test -v -run TestPort_IsolationSuite ./internal/testport/` on a
      clean checkout of `c051b81fa596`; if it hangs there too, bisect which
      spec's session actually stalls (add per-spec timing, not just the
      aggregate 2h test timeout).
- [ ] **testport/build-broke-mid-stage (AI-20260831-013952-002)** — the build
      broke mid-testport-stage per the batch's own detector; `go build ./...`
      is clean at HEAD, consistent with the same concurrent-edit pattern as
      AI-20260815-011722-005 / AI-20260816-005117-003 (a live Ralph loop
      rebuilding the working tree while the nightly batch's own `go test`
      compiled it). No code fix — re-run on an isolated checkout
      (`ci/batch` clones to its own worktree per `M-NIGHTLY selection rule`)
      to get an attributable result. Evidence:
      `ci/logs/20260831-013952/testport/go-test.log`.

### Nightly run 20260901-010436 (sha `d93fb9edc669`, 7 items) — filed 2026-09-01

5 of 7 subjects already had open M-NIGHTLY rows above (re-failed tonight, not
new): `TestPort_IsolationIntraGrantInplace` (AI-20260827-052222-037),
`TestPort_IsolationStats` (AI-20260827-052222-071),
`TestPort_LockRowsSortOverJoinTakesRowLock` (AI-20260827-052222-080),
`TestPort_PgDumpConnectionSetup` (AI-20260822-001356-001 / -107 / etc.),
`TestPort_RegressSuite` (limit, numerology — AI-20260822-001356-002 / -116).
2 subjects are genuinely new tonight:

- [ ] **testport/TestPort_PgStatActivity (AI-20260901-010436-005)** — new
      tonight. Repro: `go test -v -run '^TestPort_PgStatActivity$'
      ./internal/testport/`. Evidence:
      `ci/logs/20260901-010436/testport/go-test.log`.
- [ ] **testport/TestSyntax_Catalog_PgStatActivity (AI-20260901-010436-007)**
      — new tonight, same subject family as -005 above (`pg_stat_activity`
      catalog view) — likely one root cause covering both. Repro:
      `go test -v -run '^TestSyntax_Catalog_PgStatActivity$'
      ./internal/testport/`. Evidence:
      `ci/logs/20260901-010436/testport/go-test.log`.

## Archived — complete (see `completed_milestones/completed_fix_plan_012.md`)

M0130 (Cluster-directory compat with PG 18.3 + PG physical replication).

## Archived — complete (see `completed_milestones/completed_fix_plan_009.md`)

M0117 (CLOG ↔ PostgreSQL subsystem alignment), M0118 (Upstream Isolation Spec
Suite Pass-Through), M0120 (WordPress WP-CLI verification execution + evidence),
M0121 (WordPress WP-CLI verification remediation).

## Archived — complete (see `completed_milestones/completed_fix_plan_008.md`)

M0096 (RC isolation feature impl + spec pass), M0100 (RC isolation runtime
closure / 21-spec pass), M0102 (heterogeneous streaming-replication +
SIGKILL-failover E2E), and the two completed Maintenance fixes
(MAINT-STATEGUARD-RECONCILE, MAINT-TPCH-RELOAD). Earlier milestones:
`completed_fix_plan_001.md` .. `completed_fix_plan_007.md`.

---

## M0095 — Client-Tools TAP Test Porting (filed 2026-05-12)

Design: `docs/design/0095-0003-*`. Goal: port the client-tools-tap suite and the
engine features its `t.Skip`'d scripts need. (`pg_ctl` 001–004 already PASS.)

_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_

- [ ] **M0095-0003** — `pg_basebackup` 010/011/020 PASS (backup execution,
      `-X stream`/`-X fetch`, manifest + SHA-family checksums, in-place tablespace,
      `READ_REPLICATION_SLOT`). **Remaining:** `030 recvlogical` — blocked on logical
      decoding (not implemented; tracks with the logical-replication milestone / D-004).
      Deferred: on-disk `pg_tablespace` heap visibility (independent shared-catalog
      runtime write — see ledger). **Not actionable until logical decoding lands.**

## M0110 — Additional TAP Test Porting (beyond M0094/M0095) (filed 2026-05-22)

Scope = `docs/test-port/upstream-tap-coverage.md` tests not covered by M0094
(recovery/subscription) or M0095. Tags: SHOULD_PASS / BUG_FIX / UNIMPLEMENTED.
Already complete within M0110 (detail in git history): **M0110-0004** pg_resetwal
(RW-001..004 PASS), **M0110-0007 / M0110-0010** B-tree split & vacuum sibling
prev-link fixes.

- [ ] **M0110-0001 — pg_dump TAP** — `001_basic` ported (DU-001, CLI-only).
      `002–010` (schema dump, dump/restore round-trip, parallel, filter-file,
      connstr) DEFERRED on broad catalog-view parity + round-trip; being advanced
      one catalog gap at a time via the self-promoting
      `TestPort_PgDumpConnectionSetup` guard (CSV row DU-002, slice-by-slice).
      Design `0110-0001-pg-dump-tap-port.md`. **2026-07-06:** the guard now also
      probes the actual dump+restore round trip (pipe `pg_dump`'s stdout into
      `psql` against a fresh `CREATE DATABASE`). Found + fixed the `xmloption`
      GUC gap (every pg_dump archive opens with `SET xmloption = content;`).
      That probe then surfaced the REAL remaining blocker for 002–010: goopg's
      `catalog.InMemory` has no per-database namespace at all (`CreateDatabase`
      only registers a name; every object store — tables/schemas/collations/
      etc. — is one flat server-wide map), so a dump can never restore into a
      genuinely separate database. This is milestone-scale (per-database
      catalog + storage isolation throughout `internal/catalog`), not a slice
      — see the 2026-07-06 deferral-ledger row for the resume point. Until that
      lands, further DU-002 slices should keep targeting catalog-view parity
      (the round-trip probe stays a soft `t.Logf`, not a hard gate).
- [ ] **M0110-0002 — pg_waldump TAP** — `001_basic` CLI tier ported (WD-001);
      WAL-format readability guarded by W-001 (`TestPort_WALPgWaldumpCompat`).
      **Remaining (WD-002, deferred):** `002_save_fullpage` — needs goopg to emit
      PG-decodable FPI/heap WAL with backup blocks (+ hash/gin/gist/spgist/brin AMs
      for the server tier). Design `0110-0002-*`.
- [ ] **M0110-0003 — pg_amcheck TAP** — `001_basic` (AC-001) + `002_nonesuch`
      (AC-002) ported; CREATE SCHEMA + user-schema table restart-durability enablers
      landed. **Remaining (AC-003, deferred):** `003_check`, `004_verify_heapam`,
      `005_opclass_damage` — need `verify_heapam()` SRF + opclass catalog parity +
      index AMs. (2026-07-07: the `datconnlimit=-2` invalid-DB filter sub-section is
      now fully closed, both its SQL-visibility half — M0119-0006 AC-002 — and its
      connect-time-enforcement half — M0119-0006 AC-002 residual #1 follow-up;
      **2026-07-07, same day:** positive `datconnlimit` connection-count throttling
      (residual #2) is also now closed — `activity.ActivityRegistry.CountByDatName`
      + a `Server.handleStartup` check reject a non-superuser connection once a
      database's live connection count exceeds its configured limit, mirroring
      `postinit.c`'s `CheckMyDatabase`/`CountDBConnections` (FATAL `53300`). AC-002
      now has zero remaining residuals; per-role `rolconnlimit` throttling (a
      separate PG mechanism) remains untracked, per the matching ledger row.) Design
      `0110-0003-*`.

## M0119 — Deferral-Ledger Backlog Consumption (filed 2026-06-29)

Milestone: `docs/milestones/0119-deferral-ledger-backlog-consumption.md`
(**living milestone** — tasks are appended over time). Source of truth:
`.ralph/deferral_ledger.md`. Goal: drive every open (`status = -`) ledger row to
closure — implement the deferred scope, or verify it already landed and mark the
row `resolved`.

**Selection rule (see the Current Priority banner): pick a M0119 task ONLY when
no milestone above M0119 in the priority order has a remaining task that should
be done** (unchecked, not parked or deferred, prerequisites met). M0119 is the
terminal drain of the deferral ledger — it never runs ahead of feature/build work
in any higher-priority milestone.

**Per-task rule (applies to every M0119 implementation task):** before
implementation begins, the picking agent MUST (1) create a design doc at
`docs/design/<source-id>-NNNN-*.md` and index it in `docs/design/README.md`, and
(2) have that design doc pass an agent review. Implementation starts only after
the reviewed design doc exists. (The triage task M0119-0001 was doc-only, exempt.)

**Already landed (see git history / deferral ledger):** M0119-0001 triage
(2026-06-29: 224 open rows → 178 resolved, 46 remain), M0119-0002 (CLOG tail),
M0119-0003 (initdb options — empty backlog), M0119-0008 (isolation residual —
only the infeasible `deadlock-parallel` spec remains), M0119-0009 (UPDATE/DELETE
conflict-wait), plus the landed sub-slices of -0004 (NULLS NOT DISTINCT
enforcement + upsert arbiter) and -0005 (pg_waldump WD-003/WD-004 canonical
prune-WAL round-trip). The four open items below carry the remaining unbuilt scope.

- [ ] **M0119-0006 — pg_amcheck server tier** (source: M0110-0003). `002_nonesuch`
      … `005_opclass_damage`; `CREATE EXTENSION amcheck` + `verify_heapam()` SRF on
      top of `internal/amcheck` + opclass catalog parity. Largest open cluster
      (~29 ledger rows): index AMs, `box`/`int4range`/`int4[]` types, STORAGE
      EXTERNAL TOAST corruption, and the heapallindexed heap-scan producer.
      **Slice landed 2026-08-10 — operator-class comparator dispatch** (the
      `005_opclass_damage.pl` read-side unlock, successor to the
      `nextVirtualPgAmproc` write-side prerequisite): `bt_index_check` on an
      index declaring a user operator class is now judged under that class's
      `pg_amproc` FUNCTION 1, resolved live, so repointing the amproc row makes
      the physically-unchanged index report `item order invariant violated`.
      Three seams — `amcheck.KeyComparator`/`VerifyBtreeItemOrderCmp`,
      `catalog.InMemory.LookupOpClassSupportProcOID`,
      `executor.btIndexOpClassComparator`. Gate:
      `TestBtIndexCheck_OpClassDamageDetected` (verified non-vacuous). Design:
      `docs/design/0119-0006-opclass-comparator-dispatch-amcheck.md`.
      **Slice landed 2026-08-11 (33rd) — hour 24 and the leap second are
      ordinary TIMESTAMP input, and the day they carry is a `timestamp`'s, not a
      `date`'s**: `'2020-01-01 24:00:00'`, `'…23:59:60'`, `'…10:00:60'`,
      `'2020-12-31 24:00:00'` and `'…240000'` were flat 22007 on the timestamp
      path while the TIME path answered them all correctly. `time_overflows()`
      admits hour 24 / second 60 per-field and then separately caps the TOTAL at
      24:00:00 (so `'24:00:00.5'`, `'23:59:60.5'`, `'24:00:01'` are 22008 —
      goopg said 22007); the rollover itself is `tm2timestamp`'s, so `date_in`
      must NOT roll (`'2020-01-01 24:00:00'::date` is the 1st). Seams:
      `pgdatetime.TimeOfDay.Overflows`/`Normalize`, `CanonicalizeTimeToken`'s
      typed error + `dayCarry`, `parsePGTimestampTextParts` /
      `parseCopyTimestampZoneParts` (carry unapplied), `parseDateInputText`.
      Gate: `TestTimestampInputHour24AndLeapSecond`. Design:
      `docs/design/0125-0007-pg-faithful-date-field-decode.md` §11.
      **Slice landed 2026-08-12 (27th) — array index keys render
      date/time/timestamp/timestamptz/bytea elements again**: the five element
      types the 25th slice refused for lack of a heap image to agree with (the
      26th slice gave them one) are back on the index-only-scan fast path, via
      five arms in `arrayKeyElemRenderer` that convert the key Datum to the heap
      IMAGE and call the heap decode's own leaf renderers. `interval`/`timetz`
      stay refused (lossy comparison span). New gates
      `TestArrayKeyTextMatchesHeapText` (key text == heap text, every indexable
      array type) and `TestArrayIndexOnlyScanAnswersFromKey` (E2E, plan must be
      an IndexOnlyScan, rows == PG 18.3 `array_out`), both mutation-checked.
      Found + deferred: an IOS over `numeric`/`numeric[]` prints `1.5` where PG
      and the heap print `1.50` (display scale lost by `EncodeNumericKey`).
      Design: `docs/design/0119-0006-array-key-datetime-renderers.md`.
      **Slice landed 2026-08-13 (34th) — the numeric index key has no display
      scale, and cannot be given one**: the 27th slice's deferred divergence,
      closed the opposite way from its own resume point. An IOS over `numeric[]`
      printed `{2.7}` where the heap and PG print `{2.70}` — one stored row
      spelled two ways depending on the plan, silently, with no error. Carrying
      the scale in the key is unimplementable: `EncodeNumericKey` strips trailing
      mantissa zeros so `1.0` and `1.00` encode IDENTICALLY, and that byte
      identity is how `UNIQUE` on `numeric` raises 23505 on the second insert
      (`numeric_cmp` ignores display scale). Equality, not order, is the binding
      constraint. So the two questions the scan was asking as one are split:
      `indexKeyColumnIsDecodable` (value fidelity — `bt_index_check`'s
      comparator; `numeric` still yes) vs the new
      `indexKeyColumnRendersHeapText` (text fidelity — `numeric` no), the second
      asked only of the BLOB key format since the PG tuple-image key carries
      per-attribute datums and loses no spelling. Refused ⇒ the scan reads the
      heap, as `interval[]` does; `bt_index_check` on numeric indexes is
      untouched, which the containment weighed in the 27th slice would have cost.
      Gates: `TestNumericIndexOnlyScanKeepsDisplayScale` (E2E, scalar + array,
      mutation-checked), `TestNumericUniqueCollapsesDisplayScale` (holds the
      other end of the trade down), `TestIndexKeyRenderableIsNarrowerThanDecodable`,
      and `TestArrayKeyTextMatchesHeapText` with its `scaleLossyType` exception
      removed. Found + deferred: goopg has no key format that stores the datum
      and compares type-aware (upstream's model) — the tuple-image key is that
      seam but does not cover array key columns.
      Design: `docs/design/0119-0006-numeric-key-display-scale.md`.
      **Slice landed 2026-08-12 (29th) — the ISO 8601 `T` separator and the `Z`
      zone**: `'2020-01-01T10:00:00'` (plain ISO 8601 — what every JSON encoder
      and `date -Is` emits) raised 22007, as did `…t10:00:00`, `2020-01-01
      10:00:00Z`, `…z`, `… Z`, every `T`-separated offset form, and on BOTH
      separators any offset wider than two digits (`+0530`, `+05:30`). Go's
      `RFC3339`/`RFC3339Nano` constants were the trap: they demand the `T` AND a
      zone, so a zone-less `T` form matched neither them nor the space layouts.
      Fixed structurally — this was the THIRD consecutive slice to find goopg's
      two timestamp layout tables disagreeing: one shared `pgTimestampLayouts`
      (separator x offset-width, via Go's `Z07*` elements) is now iterated by
      `parseCopyTimestamp` AND `evalTypedStringLit`, with case/spacing folded
      upstream in `pgdatetime.NormalizeInput` (`canonicalZulu`, which requires a
      digit before the letter so it cannot touch zone ABBREVIATIONS — folding
      `'10:00:00 NZ'` to UTC would be a silent 12-hour error). Gates:
      `TestTimestampLiteralAndCopyPathsAgree` asserts the two paths agree on
      every form, so widening one alone fails the build; plus a 20-form accepted
      table, the `timestamp[]` element round-trip and a refusal guard
      (`timestamp_iso8601_tz_input_test.go`), all mutation-checked. Found +
      deferred: a zone on a bare DATE (`'2020-01-01Z'`, PG accepts) and
      timezone-abbreviation lookup (`NZ`/`EST`/`PDT`, needs a `datetbl` port).
      Design: `docs/design/0125-0007-pg-faithful-date-field-decode.md` §7.
      **Slice landed 2026-08-12 (30th) — the BC era, and the nanosecond carrier
      underneath it**: `'2020-01-01 BC'` raised 22007. New leaf
      `internal/pgdatetime/era.go` (`SplitEra`/`ApplyEra`/`EraYear`) reads PG's
      trailing `ADBC` token (case-insensitive, whitespace optional, digit-before
      -the-token so `'BC BC'`/`'BC'`/`'B.C.'` stay refused as PG refuses them),
      converts to the astronomical year `date2j`/`j2date` use (1 BC = year 0),
      and enforces PG's no-year-zero rule as 22008. Fourth consecutive two-table
      drift ended by sharing the whole ENTRY POINT, not one more table:
      `parsePGTimestampText`/`parsePGDateText` now sit behind the literal, cast,
      COPY and `pg_input_is_valid` paths. Output learns the era in all four
      DateStyles (`eraDisplay`, with the `Postgres` style's WEEKDAY still taken
      from the real instant). **What the probe FOUND is bigger than the era**:
      the `KindTime` Datum counts NANOSECONDS since 1970 (Go's `UnixNano`
      domain, 1677..2262) where PG counts MICROSECONDS since 2000 (4713 BC ..
      294276 AD), so outside that window `UnixNano` overflowed and goopg
      answered ordinary PG input with a plausible WRONG DATE and no diagnostic
      (`'1000-01-01'::date` → `2169-02-08`, `'2300-01-01'` → `1715-06-13`,
      `'0000-01-01'` → `1753-08-29`). This slice makes the wrap LOUD (22008
      naming the range) — a BC date can now be read but still not stored, and
      the honest cost is an acceptance regression on valid far dates. Gates:
      `TestDateEraLiteralAndCopyPathsAgree` (sibling agreement, so the carrier
      fix must widen both paths together), `date_era_and_range_input_test.go`,
      `era_test.go`, `datestyle_era_test.go`; units + `TestPort_RegressSuite`
      (632 s) + `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35). Deferred (ledger):
      move `Datum.Int` for `KindTime` to MICROSECONDS and drop the guard.
      Design: `docs/design/0125-0007-pg-faithful-date-field-decode.md` §8.
      **Slice landed 2026-08-11 (31st) — the time-of-day FIELD ROLES, and a
      second silent twelve-hour error**: the time half of the input path still
      ran a Go layout list where PG assigns a ROLE to each numeric run after
      splitting (`DecodeTimeCommon` + the time arms of `DecodeNumberField`). So
      `'10:00.5'` (PG: `00:10:00.5` — two fields plus a fraction are MINUTE TO
      SECOND, the fields SHIFT), `'10::00'`/`'10:'` (an empty subfield is 0),
      `':10:00'` (leading punctuation is a delimiter), `'040506'`/`'0405'`/
      `'040506.5'` (run-together `hhmmss`/`hhmm`), `'T040506'`, `'10:00AM'` and
      `'allballs'` were all 22007 — and `'12:00 AM'` was WORSE: decoded as
      `12:00:00` against PG's `00:00:00` (`DTK_AM` maps hour 12 to 0), twelve
      hours wrong with no diagnostic, while `'2020-01-01 12:00 AM'::timestamp`
      lost its meridiem to a zone stripper that truncated at the first space.
      New leaf `internal/pgdatetime/timeofday.go` (`ParseTimeOfDay`,
      `CanonicalizeTimeToken`) owns field decoding for `parseTimeString`,
      `parseTimeTZString` and `parsePGTimestampText`, splits 22007 from 22008 as
      `DTERR_BAD_FORMAT`/`DTERR_FIELD_OVERFLOW` do, and lets the hour-24 rewrite
      and `:60` string surgery go with the layouts. Gates: `timeofday_test.go`,
      `time_field_roles_input_test.go` (36-case `::time` + 16-case `::timestamp`
      psql batteries against PG 18.3, 0 divergences left in the time battery);
      units + `TestPort_RegressSuite` + `scripts/tpch-spotcheck.sh` (Q12=2,
      Q13=35). Two §6/§7 mutation guards named `'10:00.5'`/`'10::00'` as
      "refuse until a real DecodeTime walk lands" — this is that walk, so both
      moved to their PG readings. Deferred (ledger): unvalidated TIME zone
      suffixes (`'10:00 A.M.'`), the hour-24/leap-second rollover for TIMESTAMP,
      `timetz`'s session-zone default, and the pre-existing `timestamp`-applies
      -a-decoded-offset defect this slice made reachable from more spellings.
      Design: `docs/design/0125-0007-pg-faithful-date-field-decode.md` §9.
      **Slice landed 2026-08-11 (32nd) — the zone a `timestamp` and a `date`
      must THROW AWAY**: closes the 31st slice's deferral (4), and the probe
      found the same root cause producing a WHOLE-DAY error one type over.
      Upstream runs all three input functions through one `DecodeDateTime()` and
      differs only after it (`timestamptz_in` passes `&tz` to `tm2timestamp()`,
      `timestamp_in` passes `NULL`, `date_in` never looks at the zone), while
      goopg had ONE shared layout table with Go `Z07*` elements and `.UTC()`'d
      every match — the timestamptz rule applied to all three. Silently wrong,
      never errors: `'2020-01-01 10:00:00+05:30'::timestamp` = `04:30:00`
      (PG `10:00:00`), and `'2020-01-02 02:00:00+05:30'::date` = `2020-01-01`
      (PG `2020-01-02`) — an offset crossing midnight moved the STORED DAY, so
      `WHERE d = DATE '2020-01-02'` silently missed the row it had just
      inserted. New `tsZoneMode`/`tsZoneModeForType`/`applyTSZoneMode` +
      mode-taking `parsePGTimestampTextZone`/`parseCopyTimestampZone`
      (`copy_text.go`); typed literal, the three casts, `pg_input_is_valid`,
      the COPY TEXT reader and `codec.go`'s encoder all pass their target
      type's rule. Gate: `timestamp_zone_discard_input_test.go`, plus a
      literal/cast/INSERT/COPY E2E diff of a throwaway goopg against a
      throwaway PG 18.3 (every cell agrees). Deferred (ledger): the four
      target-type-less paths (`tryParseStringAs`, `EXTRACT`, `date_trunc`,
      `pg_authid` validuntil) that keep the timestamptz reading, and the
      `timestamptz` OUTPUT missing its `+00` suffix. Design: same doc §10.
      **Slice landed 2026-08-12 — the checkunique posting-list arm END TO END**
      (`TestBtIndexCheck_CheckUniquePostingListRealTree`): the tier now runs over
      posting lists goopg's own bulk build wrote, on a real heap, under the
      executor's snapshot; deleting the duplicate rows leaves the pages unchanged
      and the tier clean. Found: goopg's INSERT path never writes a posting list
      (`dedupConsolidate` only drops exact `(key,tid)` duplicates), so no goopg
      unique index can hold one — ledger row 2026-08-12. Design §Gates in
      `docs/design/0119-0006-checkunique-tier-amcheck.md`.
      **Slice landed 2026-08-12 (40th) — a `timestamptz` survives the cast to
      text, and the cast to `timestamp` stops being the identity**: closes the
      residual the 39th slice filed against ITSELF. That slice fixed the two
      output paths that know the declared column type but could not fix
      `formatTimeDatumDateStyle`, which sees a bare Datum — so
      `('2020-01-01 10:00:00+05:30'::timestamptz)::text` under
      `TimeZone='Asia/Kolkata'` returned `2020-01-01 10:00:00` while goopg's OWN
      `SELECT` of the same value returned `2020-01-01 15:30:00+05:30`: text
      denoting a different instant than the one stored, no diagnostic.
      `TimeSubTimestampTZ` had been DECLARED since M0127-P5.9-u as "not yet
      populated by their producers"; it has producers now
      (`NewTimestampTZDatum`, at the typed literal, the cast, the on-disk decode
      and the five `prorettype` 1184 functions), and the input rule
      (`tsZoneModeForType`) and the output rule now share one predicate,
      `isTimestampTZTypeName`. **What tagging the datum then exposed is bigger
      than the suffix**: `ts::timestamptz` / `tstz::timestamp` returned the datum
      UNTOUCHED, which is the identity only while `TimeZone` is UTC — every
      goopg cluster ships UTC, which is why it hid; upstream
      `timestamp2timestamptz` reads the wall clock as LOCAL (the instant moves)
      and `timestamptz2timestamp` keeps the wall clock the instant has in the
      session zone, so BOTH directions were off by the offset. New
      `config.TimestampToTimestampTZ`/`TimestampTZToTimestamp`. A 20-probe
      end-to-end diff of a throwaway goopg against a throwaway PG 18.3 is
      byte-identical. Gates: `timestamptz_cast_text_test.go` (6 oracle cells, a
      sibling guard over 4 DateStyles x 4 zones, a no-zone-leak negative, the
      producers, the cross-cast, ±infinity), 3 mutations verified to fail; units
      + `TestPort_RegressSuite` (265 s) + `scripts/tpch-spotcheck.sh` (Q12=2,
      Q13=35). Deferred (2 ledger rows): the spring-forward gap hour, POSIX zone
      spellings, and the ~40 un-audited `NewTimeDatum` producers. Design:
      `docs/design/0119-0006-timestamptz-cast-text-rendering.md`.
      **Slice landed 2026-08-12 (41st) — the DOORS a `timestamptz` (and a
      `date`) comes through, not the renderer**: closes the 40th slice's own
      residual by auditing all 50 non-test `NewTimeDatum(` sites against one
      question — is the declared SQL type in reach here? Five had it and were
      not tagging: `copyBinaryToDatum`, `copyTextToDatum`, BOTH index-key
      decoder siblings (`decodeIndexKeyColumn` / `decodeBTreeKeyToDatum`, which
      therefore decoded the same column two different ways in a composite vs a
      single-column index-only scan) and `pg_authid.rolvaliduntil`, whose column
      type is a compile-time constant. Untagged, those rendered a timestamptz
      through `timestamp_out`, so `col::text` denoted a different instant than
      goopg's own SELECT of the same column while the identical value read from
      the HEAP rendered correctly. The negative half of the guard found a
      SECOND, unpredicted divergence one type over: `date`'s behavioural
      `TimeSubDate` (M0097-0063 — date-only `Format()`, `date + integer`
      dispatch) is set by the heap decode and by none of the four decoders, so a
      COPY'd or index-answered date printed `2020-01-01 00:00:00`. Fixed in the
      same arms. Gates: `timestamptz_origin_tag_test.go` (3 tests, verified red
      at HEAD with 7 failing sub-cases). 3 ledger rows (the `TimeSubTime`
      producer gap; the target-type-less `expr.go` paths; the pgoutput suspect
      RETRACTED — no such site at HEAD). Design:
      `docs/design/0119-0006-timestamptz-datum-origin-tags.md`.
      **Second slice landed 2026-08-10 — general key decode:** the
      single-int4-key-column restriction is lifted. `btIndexOpClassComparator`
      now walks a composite key column by column (upstream `_bt_compare`'s
      contract) via the shared `decodeIndexKeyColumn`, so a user opclass on any
      invertible type (int4/int8/float8/date/timestamp(tz)/text-like/enum) and
      in any key position dispatches; columns without a user class keep byte
      order for their own slice. Gates:
      `TestBtIndexCheck_OpClassDamageDetectedText` +
      `TestBtIndexCheck_OpClassDamageDetectedComposite`.
      **Third slice landed 2026-08-10 — the `checkunique` tier:**
      `bt_index_check(..., checkunique := true)` previously accepted the
      argument and ran nothing, so `pg_amcheck --checkunique` could not fail.
      It now runs upstream's `bt_entry_unique_check`: engine
      `amcheck.VerifyBtreeUnique` walks the whole leaf level carrying
      `BtreeLastVisibleEntry` across page boundaries under the injected
      `KeyComparator` (so a user opclass governs uniqueness as it governs item
      order), `btree.PageLeafItems` retains slot/posting position for the
      errdetail, and executor `btIndexCheckUnique` gates on `idx.Unique` and
      supplies heap visibility from `ctx.Snap`. A duplicate is reported only
      when BOTH heap tuples are visible — the dead-row-version duplicates every
      healthy unique index accumulates stay clean. Gates:
      `TestVerifyBtreeUnique_*` (7),
      `TestBtIndexCheck_CheckUniqueDetectsLiveDuplicate`,
      `TestBtIndexCheck_CheckUniqueSkipsNonUniqueIndex`. Design:
      `docs/design/0119-0006-checkunique-tier-amcheck.md`.
      **Fourth slice landed 2026-08-10 — `INCLUDE` (covering) indexes:** the
      `len(idx.IncludeColumns) > 0` decline in `btIndexOpClassComparator` was
      over-conservative. goopg encodes a stored B-tree key from the KEY columns
      alone (`encodeCompositeBTreeKey` walks `idx.Columns`; no non-key attribute
      is appended), so a covering index's key bytes are exactly the existing
      column-by-column walk — upstream's rule too (`_bt_compare` stops at
      `IndexRelationGetNumberOfKeyAttributes`). A user opclass on a covering
      index now dispatches, and `checkunique` inherits it. Gate:
      `TestBtIndexCheck_OpClassDamageDetectedInclude` (non-vacuous).
      **Fifth slice landed 2026-08-10 — `NUMERIC` key columns:** new
      `btree.DecodeNumericKey` inverts `EncodeNumericKey`, whose doc comment had
      declared decoding "intentionally not provided" — the format is in fact
      invertible and self-delimiting (`0x01` zero sentinel, else sign byte +
      biased 4-byte exponent + ASCII digit run + a terminator that can never be a
      digit byte), so it also reports the byte width the composite walk needs.
      Wired into BOTH key-decode siblings via `numericDatumFromBig`:
      `decodeIndexKeyColumn` (composite/amcheck) and `decodeBTreeKeyToDatum`
      (single-column IOS). That second wiring repairs a latent index-only-scan
      defect — `NUMERIC` used to fall through the shared `default:` arm, which
      reads 8 bytes as a float8 and returns an enum `Datum`, and since it never
      errors the comparator's decode-failure fallback never fired. Gates:
      `TestBtIndexCheck_OpClassDamageDetectedNumeric`,
      `TestDecodeNumericKey{RoundTrip,BigMantissa,Composite,RejectsGarbage}`,
      `TestNumericIndexKeyDecode{SiblingParity,CompositeWalk}`. Still open for
      005: expression key columns, posting-list duplicate coverage, and the
      remaining non-invertible key types (box/int4range/int4[]) — ledger rows
      2026-08-10.
      **Sixth slice landed 2026-08-10 — `005_opclass_damage.pl` PORTED:**
      `TestPort_PgAmcheck005OpclassDamage`
      (`internal/testport/pgamcheck005_opclass_test.go`) runs the real upstream
      `pg_amcheck` binary through all four upstream phases against ONE
      unchanging set of index pages — clean → repoint `int4_fickle_ops`
      FUNCTION 1 at a descending comparator via `UPDATE pg_catalog.pg_amproc`
      → exit 2 with `item order invariant violated for index "fickleidx"` →
      repair the amproc row → clean again under `--checkunique` → repoint
      `int4_unique_ops` FUNCTION 1 at a comparator declaring 768 and 769 equal
      → exit 2 with `index uniqueness is violated for index
      "bttest_unique_idx"`. Nothing on disk is ever corrupted; every verdict is
      decided by the live `pg_amproc` row, which is the property 005 exists to
      prove. Also repaired a pre-existing defect this required: two unquoted
      commas in the `W-001` row of
      `docs/test-port/postgres-oracle-port-status.csv` made the whole file fail
      to parse, so `cmd/gen-oracle-port-status` could not regenerate the `.md`
      at all. CSV row `AC-003` rationale updated + `.md` regenerated; `AC-003`
      stays `defer` because its 003/004 tiers still need index AMs goopg lacks.
      **Slice landed 2026-08-10 (7th) — expression key columns in the B-tree
      BULK BUILD** (design `docs/design/0119-0006-expression-index-bulk-build.md`).
      Chasing the expression-key arm of the comparator dispatch surfaced a real
      data defect one layer down: the bulk-build encoder skipped expression key
      columns outright while the runtime maintain path evaluated them, so
      `CREATE INDEX ON t(lower(b))` over pre-existing rows built a physically
      EMPTY index, REINDEX (plain and CONCURRENTLY) discarded the entries INSERT
      had written, and a mixed `(a, lower(b))` index stored keys missing the
      expression component. Both encoders now share `encodeArbiterExprKey` via
      `encodeCompositeBTreeKeyWithExprs`/`resolveIndexKeyExprs`; gates
      `TestExpressionIndexBuildIndexesExistingRows` +
      `TestReindexExpressionIndexKeepsEntries` (physical entry counts, confirmed
      non-vacuous).
      **Slice landed 2026-08-10 (9th) — index-expression RESULT-TYPE resolution
      + expression key columns in the opclass comparator** (design
      `docs/design/0119-0006-expression-index-result-type.md`). New exported
      `planner.ExprResultType` reads the type out of the PG 18 pg_proc /
      pg_operator seed goopg already ships (not a hand-written name table) and
      DECLINES rather than falling back to text the way `inferExprType` does;
      its decode-side twin `exprKeyDecodeType` maps that SQL type onto the
      surrogate the kind-dispatching expression-key ENCODER actually produced
      (int4 expression key = 8 bytes, date = int64 micros), so
      `btIndexOpClassComparator` no longer declines a whole index because one
      key column is an expression. Gates
      `TestBtIndexCheck_OpClassDamageDetectedExpression` and
      `TestBtIndexCheck_ExpressionKeyCleanUnderPlainOpClass` (the decode-width
      gate), both confirmed non-vacuous.
      **Slice landed 2026-08-10 (10th) — FLOAT expression key encoding, made
      type-directed** (design
      `docs/design/0119-0006-expression-index-float-key.md`). The kind-dispatch
      assumption behind `encodeArbiterExprKey` broke on float INSIDE one column:
      goopg has no `KindFloat`, so `codec.go` re-parses a stored float's
      `PGFloatOut` text and yields `KindNumeric` for `1.5` but `KindString` for
      `1e+30`/`Infinity`/`NaN` — the same expression index was written by
      `EncodeNumericKey` AND `EncodeVarchar`, whose byte spaces do not
      interleave, so it was not ordered at all (a range scan could miss
      arbitrarily many live rows). The encoder now takes the resolved key
      expression and routes every row of a float-typed expression through
      `btree.EncodeFloat8` (PG's `float8_cmp_internal` order, NaN last), with
      `datumToFloat64ForKey` shared with the float COLUMN path. Gates
      `TestEncodeArbiterExprKeyFloatIsTypeDirected` +
      `TestExpressionIndexBuildFloatKey` (physical tree scan, bulk build and
      post-build INSERT), confirmed non-vacuous.
      **Slice landed 2026-08-10 (11th) — ENUM expression key encoding, made
      type-directed** (design
      `docs/design/0119-0006-expression-index-enum-key.md`). The second and
      worse kind-dispatch failure in `encodeArbiterExprKey`, and unlike float it
      was wrong for EVERY enum expression index, not just exotic values. An enum
      COLUMN key is `EncodeFloat8(enumsortorder)` — the type's ordering, since
      upstream `enum_ops` compares by `enumsortorder` (`enum_cmp`,
      `src/backend/utils/adt/enum.c`) — and every column path converts a
      `KindString` label to `KindEnum` before encoding so that holds
      (M0097-0022). An expression key column has no catalog column, so that
      conversion never ran: over `ENUM ('sad','ok','happy')` a real build stored
      `686170707900`/`6f6b00`/`73616400`, i.e. ALPHABETICAL label order, the
      exact reverse of declaration order. Latent second defect: a datum that did
      arrive as `KindEnum` (the seq-scan path injects those) wrote 8 float bytes
      into the same index as the variable-width label bytes. Fixed as for float:
      `encodeArbiterExprKey` takes a `*Context` (all three call sites had one),
      `exprKeyEnumType` resolves `planner.ExprResultType`'s type NAME through
      `catalog.InMemory.LookupEnum`, and `enumSortOrderForKey` maps either datum
      kind onto the sort order, so column and expression keys over one enum are
      byte-identical. Gates
      `TestEncodeArbiterExprKeyEnumIsTypeDirected` +
      `TestExpressionIndexBuildEnumKey` (physical tree scan over bulk build and
      post-build INSERT), both confirmed non-vacuous.
      **Slice landed 2026-08-10 (12th) — ARRAY key columns** (design
      `docs/design/0119-0006-array-index-key-encoding.md`). The `int4[]` row of
      the type cluster, and the same class of defect the float/enum slices
      found, one layer out: an array column is
      `catalog.Type{Name:<ELEMENT type>, IsArray:true}`, so every `Name`-only
      predicate in `encodeBTreeKeyForColumn` answered for the ELEMENT and fed
      the array its own TEXT (`"{1,2}"`) as a scalar — and
      `isSupportedBTreeKeyType` admitted the index for the same reason. The two
      write paths then failed differently and both silently: the bulk build
      aborted with a bogus `22P02 invalid input syntax for type integer:
      "{1,2}"`, while the runtime maintain path wrote NO entry at all
      (`maintainUniqueIndexesForInsert` swallows key-encode errors by design),
      so `CREATE INDEX` on an empty table SUCCEEDED and left a permanently
      EMPTY index — an index scan reads it as "no rows" and a UNIQUE array
      index enforces nothing (confirmed at HEAD: five rows in, zero leaf
      entries). New `encodeArrayBTreeKey` reproduces upstream `array_ops`
      (`btarraycmp`→`array_cmp`) in bytes: `0x01 ++ <element key>` per non-NULL
      element, `0xFF` for a NULL element (NULL sorts after not-NULL), `0x00`
      closing the array. Element keys recurse into `encodeBTreeKeyForColumn`,
      so an array key is byte-identical to the scalar keys of its elements.
      Neither marker is optional — without the tag the NULL marker collides
      with `EncodeInt4(maxint32)`, and without the end marker the segment is
      not self-delimiting inside a COMPOSITE key (`('{1}',2)` vs `('{1,2}',0)`)
      and `{}` encodes to a zero-length key, which `encodeIndexKeyFromCols`
      cannot tell from "no key" and dropped from the index. Gates:
      `TestEncodeArrayBTreeKey{MatchesArrayCmpOrder,TextElements,DeclinesMultidim}`
      + `TestArrayIndex{BuildAndMaintainKeys,CompositeKeyIsSelfDelimiting}`,
      the orderings captured from the PG 18.3 reference cluster, all five
      confirmed non-vacuous.
      **Slice landed 2026-08-10 (13th) — `int2`/`oid`/`bool`/`bytea`/`time` key
      encodings** (design `docs/design/0119-0006-scalar-index-key-encodings.md`).
      Five ordinary PG types could not be indexed AT ALL — `isSupportedBTreeKeyType`
      rejected them, so even `PRIMARY KEY (smallint_col)` raised `0A000 btree v0
      only supports int4 / numeric keys`. New `internal/executor/btree_scalar_keys.go`
      encodes each to its DEFAULT-OPCLASS order (captured from the PG 18.3
      reference cluster, not read off the comparators): int2 widens to the
      signed int4 key; **`oid` widens to the INT8 key because `oidcmp` compares
      UNSIGNED** — the obvious int4 key sorts every OID ≥ 2³¹ below OID 0;
      bool = int4 key of 0/1; bytea rides `EncodeVarchar`, whose escaped
      `0x00`-terminated form is exactly `byteacmp` (memcmp, then shorter-first)
      for arbitrary bytes, so an embedded NUL cannot forge the terminator and
      bytea is safe as a composite key column; `time` = int64 micros-of-day via
      the codec's own `pgTimeMicros`. `timetz` is deliberately DECLINED (its
      `timetz_cmp_internal` is two-part: time-minus-zone, then zone). Both
      key-decode siblings route to one shared decoder BEFORE their own switches —
      their common `default:` arm reads any 8 leading bytes as an enum float8
      and never errors, so an 8-byte oid/time key would otherwise decode as a
      bogus enum. Gates: `TestEncodeScalarBTreeKeyMatchesPGOrder`,
      `TestScalarBTreeKeyProbeMatchesStoredKey`,
      `TestScalarBTreeKeyDecodeSiblingParity`,
      `TestScalarIndexBuildAndMaintainKeys` (both stored-key writers, physical
      `btree.RangeScan` counts), `TestByteaIndexKeyIsSelfDelimiting`,
      `TestTimeTzIndexKeyDeclined` — all non-vacuous. 2 ledger rows (timetz;
      and the INSERT-path gap this surfaced: `VALUES ('true')` into a bool
      column raises XX000 because the codec bool arm demands `KindBool`).
      **Slice landed 2026-08-10 (14th) — ARRAY key DECODING** (design
      `docs/design/0119-0006-array-index-key-decoding.md`). The decode sibling
      the array ENCODE slice left open, and its absence was a live MISREAD, not
      a gap (Hard-won Rule #2): both decode siblings dispatch on
      `col.Type.Name`, which for an array column is the ELEMENT type name, so an
      `int4[]` key column reached `decodeIndexKeyColumn`'s int4 arm and an
      `int2[]`/`oid[]`/`bool[]` one reached `decodeScalarBTreeKey`, each
      consuming the ELEMENT's width out of a longer array segment (for `int4[]`:
      the `0x01` presence tag plus three bytes of the first element key). A
      single-column index-only scan over an array column therefore returned a
      garbage integer, and — worse — a COMPOSITE key walk desynchronized at the
      array column so every LATER key column decoded from the wrong offset. That
      walk is `btIndexOpClassComparator`'s: confirmed at HEAD, `bt_index_check`
      reported a composite `(a int4[], i int4 <user opclass>)` index CLEAN with
      its `FUNCTION 1` support proc repointed at a descending comparator. New
      `decodeArrayBTreeKey` inverts the encoding (tag dispatch, recursion into
      `decodeIndexKeyColumn` for each element, byte width reported so the walk
      can advance) and returns the array's canonical text form — the same
      representation the heap-side `decodeArrayValuePG` produces. Arrays route
      FIRST in both siblings, mirroring the encoder; `decodeBTreeKeyToDatum`
      additionally requires the segment to consume the WHOLE key. Element
      rendering is per-type (`PGFloatOut` for floats, `quoteArrayTextElem` for
      text-likes, shared with the heap path); types with no faithful rendering
      are refused rather than guessed, which also re-arms the comparator's
      decode-failure fallback. Gates:
      `TestDecodeArrayBTreeKey{RoundTrip,CompositeWalk,RejectsMalformed}`,
      `TestArrayBTreeKeyDecodeSiblingParity`,
      `TestBtIndexCheck_OpClassDamageDetectedAfterArrayColumn` — all five
      non-vacuous. 2 ledger rows (no decoding for bytea/date/time/enum elements;
      and the encode-side defect this surfaced — a QUOTED `"NULL"` text element
      is encoded as an array NULL because `parseTextArray` strips quoting before
      the NULL test).
      **Slice landed 2026-08-10 (15th) — `timetz` key encoding, retracting a
      wrong refusal** (design
      `docs/design/0119-0006-timetz-index-key-encoding.md`). The 13th slice
      declined `timetz` on the grounds that `timetz_cmp_internal`
      (`postgres/src/backend/utils/adt/date.c`) is two-part — GMT-equivalent
      time first, zone only to break ties — and that "a single ordered key
      column cannot represent it". The premise is right and the conclusion is
      wrong: both parts are FIXED-WIDTH integers whose order-preserving key
      encodings goopg already ships, so `EncodeInt8(gmt) ++ EncodeInt4(zone)`
      compares lexicographically exactly as upstream compares — the same
      structure that makes a two-column composite key work. Arity was never the
      obstacle; a part that is not INDIVIDUALLY order-preserving is, which is
      why `interval` (a lossy 128-bit span, `'1 mon'` = `'30 days'`) stays open
      as its own slice with an IOS-behaviour decision attached. Landed as
      `isTimeTzType` (kept disjoint from `isTimeOfDayType` — had the plain-time
      predicate claimed timetz, the key would be 8 bytes of LOCAL time-of-day
      and the zone would drop out of the ordering entirely, a silently wrong
      index rather than a refused one), `timeTzKeyParts`,
      `encodeTimeTzBTreeKey`, arms in all three of `encodeScalarBTreeKey` /
      `decodeScalarBTreeKey` / `coerceScalarKeyStringDatum`, and `timetz` in
      `isSupportedBTreeKeyType`. The load-bearing detail is the SIGN
      CONVENTION: upstream `TimeTzADT.zone` is seconds WEST of UTC where
      goopg's `Datum.Scale` is minutes EAST, so the encoder negates — and the
      ledger row's own proposed resume encoding had that secondary part
      backwards. Getting it wrong leaves the primary part correct (the local
      time compensates) and reverses every tie: PG orders `13:00:00+01` <
      `12:00:00+00` < `11:00:00-01` although all three are the same instant
      (captured from the PG 18.3 reference cluster). The decode arm is not
      optional, for the reason the 14th slice found at HEAD — the two decode
      siblings' shared `default:` arm reads any 8 leading bytes as an enum
      float8 and never errors, so an unrouted 12-byte timetz key would decode
      as a bogus enum AND consume 8 bytes in the composite walk,
      desynchronizing every later key column. Gates: the four
      `scalarKeyCases()` table-driven gates gain a `timetz` row, plus
      `TestTimeTzIndexKeyIsTwoPart` (tie-break direction asserted on
      same-instant values — a whole-ordering table can pass with an inverted
      tie-break if no two of its values are the same instant) and
      `TestTimeTzCompositeKeyIsSelfDelimiting` (non-final composite position,
      and the walk resynchronizing so the trailing column decodes); all
      confirmed non-vacuous by four separate source mutations. 2 ledger rows
      (interval; and `timetz[]` elements, which now encode but have no
      renderer in `decodeArrayKeyElemText`).
      **Slice landed 2026-08-10 (16th) — `boolean` input from an unknown
      literal** (design
      `docs/design/0119-0006-bool-unknown-literal-input.md`). The INSERT-path
      coercion defect the 13th slice's E2E test had to work around by inserting
      booleans UNQUOTED: `INSERT INTO t(b) VALUES ('true')` raised
      `XX000 expected bool, got kind 3` because `encodeValuePG`'s bool arm
      demanded `KindBool` strictly. Upstream types a bare literal `unknown` and
      coerces it through the column type's input function — `boolin`
      (`postgres/src/backend/utils/adt/bool.c`) — so every `pg_dump` archive /
      COPY-style loader script that quotes its booleans loads on PG and failed
      here, and a `boolean[]` column was unwritable by ANY spelling
      (`encodeArrayValuePG` recurses per element and an element is always
      element TEXT). Auditing all 15 `expected …, got kind` sites in
      `encodeValuePG` shows bool was the lone holdout — every other scalar arm
      already routes `KindString` — so this is one arm, not a sweep. New
      `pgBoolIn` reproduces `boolin` (trim + `parse_bool_with_len`) and becomes
      the SINGLE source of the spelling table, which four sites had each copied
      (`evalTypedStringLit`, `evalCast`, `isValidBoolInput`, the codec arm) —
      Hard-won Rule #2. Two upstream details are load-bearing and gated: a lone
      `o` is REJECTED (it prefixes both `on` and `off`, hence upstream's
      minimum compare length of 2) while `of` is accepted, and `1`/`0` count
      only at length one. `KindInt` is deliberately NOT accepted — `VALUES (1)`
      into a bool column is an error upstream too. Gates:
      `TestPgBoolInMatchesParseBoolWithLen`,
      `TestEncodeValuePGBoolAcceptsUnknownLiteral`,
      `TestBoolColumnAcceptsQuotedLiteralEndToEnd` (scalar AND array column
      through the real INSERT path, plus a refusal case), and
      `TestScalarIndexBuildAndMaintainKeys/bool`, whose fixture drops its bool
      exception so `sqlLiteralForKeyType` now quotes every type — all
      non-vacuous under one source mutation, which reproduced the exact
      reported error in three of them. 1 ledger row (ASCII-vs-Unicode
      whitespace trim, which applies to every type input function and deserves
      one answer).
      **Slice landed 2026-08-10 (17th) — `interval` key encoding, the first
      goopg index key with NO decode sibling** (design
      `docs/design/0119-0006-interval-index-key-encoding.md`). `CREATE INDEX ON
      t(interval_col)` raised `0A000 btree v0 only supports int4 / numeric
      keys`, so an interval column could not be indexed at all. Upstream
      `interval_cmp_value` (`postgres/src/backend/utils/adt/timestamp.c`)
      collapses months/days/micros into ONE signed 128-bit span
      `(month*30 + day)*USECS_PER_DAY + time` and `interval_cmp` compares
      nothing else, so the key is that span alone — deliberately lossy, and the
      loss is the correct behaviour: `'1 mon' = '30 days'` is TRUE in PG
      (captured from the 18.3 reference cluster), so a field-preserving key
      would pass an ordering test and still order values PG calls equal, let a
      UNIQUE index accept a duplicate PG rejects, and make a probe for
      `'30 days'` miss a stored `'1 mon'`. Landed as `btree.EncodeInt128` (the
      sign-bit flip applied to the high half only; 128 bits is not optional —
      the day total reaches 6.4e10 and scaling by USECS_PER_DAY overflows
      int64) plus new `internal/executor/btree_interval_key.go`, routed through
      the same three seams as the timetz slice. Text is parsed with
      `parser.ParseIntervalBody`, the entry point `'…'::interval` uses, which
      matters because goopg holds an interval COLUMN as text — `KindString` is
      the shape that actually reaches both stored-key writers. The decode arm
      REFUSES rather than being absent (the siblings' shared `default:` arm
      reads any 8 leading bytes as an enum float8 and never errors), and new
      `indexOnlyScanOp.indexKeyIsDecodable` makes the index-only scan decline
      its decode-from-key fast path and read the heap instead — without it the
      query fails `XX000 IOS decode: …`, confirmed by mutation. Gates:
      `TestEncodeIntervalBTreeKeyMatchesPGOrder`,
      `TestIntervalKeyIsTheComparisonSpan`,
      `TestIntervalKeyStringAndIntervalDatumAgree`,
      `TestIntervalKeyRejectsUnparseableText`,
      `TestIntervalKeyDecodeIsRefused`,
      `TestIntervalIndexBuildAndMaintainKeys`,
      `TestIntervalCompositeKeyIsSelfDelimiting`,
      `TestIntervalIndexOnlyScanReadsHeap` — each non-vacuous under one of five
      source mutations. 2 ledger rows, the first of them significant: an
      interval COLUMN still compares as TEXT at runtime (seq-scan
      `WHERE i > '10 days'` drops a stored `'1 mon'`, `ORDER BY i` is
      alphabetical), so with the index order now correct the answer is
      plan-dependent.
      **Slice landed 2026-08-10 (18th) — the interval COLUMN gets PG's native
      16-byte layout** (design
      `docs/design/0119-0006-interval-column-storage.md`), closing the
      significant ledger row the 17th slice opened. An `interval` column had no
      `case` in `encodeValuePG`, so it fell to the varlena `default:` arm and
      goopg stored **the literal characters the user typed**. Against the PG
      18.3 oracle that was three wrong answers plus a wrong rendering, not a
      sort nit: `ORDER BY i` put `2 hours` after `10 days`;
      `WHERE i > interval '10 days'` returned a DIFFERENT SET (admitted
      `2 hours`, dropped `1 mon`); `WHERE i = interval '30 days'` missed the
      `1 mon` PG calls equal; `min(i)` was `1 mon`; and the column echoed
      `2 hours` where PG prints `02:00:00`. With the 17th slice's B-tree key
      landed and the heap still text, the answer had become PLAN-DEPENDENT.
      The discovery worth keeping: goopg already had every interval
      *mechanism* — `KindInterval`, `compareDatum`'s `interval_cmp_value` port,
      `formatInterval`, `parser.ParseIntervalBody` — and all four were
      reachable from expressions and none from a stored column, so this was a
      missing routing arm, not a missing algorithm, which is exactly why every
      in-memory interval unit test passed throughout. Landed as PG's `Interval`
      struct (`{time int64 @0, day int32 @8, month int32 @12}`, typlen 16 /
      typalign 'd' — values `pg_type` has asserted since initdb, so the heap
      now agrees with the catalog rather than the reverse) across five seams:
      encode (accepting the bare `unknown` literal through `interval_in`'s own
      tokenizer, and raising `22007` where text storage accepted anything),
      decode, `physicalPGTypeAlign`, `pgPhysicalTypeIsVarlena`, and a
      `tryParseStringAs` arm so `i > '10 days'` does not fall back to
      `Format()`-vs-`Format()` (text comparison one level down).
      `formatInterval` MOVED to leaf `internal/pgdatetime` because the layout
      has two decoders and `internal/wal`'s pgoutput cannot import the
      executor — left unrouted it would have read the microsecond field as a
      varlena header and shipped garbage to a subscriber with no error. Also
      removes `interval` from the heap-side-divergence class
      `pgindex_keydesc.go` names (a real PG standby was misreading the column);
      `numeric` and `uuid` remain. Gates: mixed-column oracle diff
      byte-identical (NULL / ±infinity / `2147483647 days` / UPDATE / two
      interval columns), index-path diff clean, COPY + restart durable, 10 unit
      tests with 7/7 source mutations caught, units + tpch-spotcheck +
      `TestPort_RegressSuite` PASS. 3 ledger rows: `interval(3)` typmod not
      applied at storage (`AdjustIntervalForTypmod`), `interval hour to minute`
      unparseable in a column-type position, and `interval[]` elements still
      text (`c[1] = c[2]` is `f` where PG says `t`).
      **Slice landed 2026-08-10 (19th) — the `uuid` COLUMN gets PG's native
      16-byte `pg_uuid_t`** (design
      `docs/design/0119-0006-uuid-column-storage.md`). Same shape as the
      interval slice one type later, and the interesting half is that **no
      goopg answer was wrong**: a `uuid` column fell to `encodeValuePG`'s
      varlena `default:` arm and was stored as the 36-character canonical TEXT
      (37 bytes behind a varlena header) while goopg's OWN `pg_attribute` row
      said `attlen 16, attalign 'c', attstorage 'p'` (`userTypeAttrsForOID`,
      from the pg_type OID 2950 seed). `uuid_cmp` is a `memcmp` and the
      canonical lowercase-hex text compares in the same order, so ORDER BY,
      `=`, `min()` and the rendering were already right — the defect is
      invisible from inside the engine and visible only to a reader that
      trusts the descriptor: a PG standby deforms 16 raw bytes at the column's
      offset (the first 16 characters of the text) and finds every FOLLOWING
      column 21 bytes out of position. Five seams: encode (`string_to_uuid`
      port), decode (`uuid_out` port), `physicalPGTypeAlign` → 1,
      `pgPhysicalTypeIsVarlena` → false, and the `internal/wal` pgoutput
      sibling decoder. The Datum stays the canonical `KindString` the whole
      engine speaks, so no index-key/comparison/analyzer site moves. One
      unlock came free and was found BY the units gate: the
      `pgIndexKeyImageIsPGFaithful` guard that refused uuid was written to
      FAIL the moment the codec became faithful, so uuid now takes the
      PG-format index-tuple key path under `btree.PGCompareUUID` and joins
      `TestPGIndexTupleKeyOrdersEveryDescribableType`; `numeric` is the last
      member of that divergence class. Gates: 9 new unit tests
      (`codec_uuid_column_test.go`, `pgoutput_uuid_test.go`), units +
      tpch-spotcheck (Q12=2, Q13=35) + `TestPort_RegressSuite` PASS. 2 ledger
      rows: `uuid[]` elements still text, and no on-disk migration for heaps
      written by an older goopg (the general gap, recorded once).
      **Slice landed 2026-08-11 (20th) — the `numeric` COLUMN stops being
      text** (design `docs/design/0119-0006-numeric-column-storage.md`), the
      LAST member of the heap-side-divergence class and the one the descriptor
      could not have caught: `pg_type` agrees numeric is a varlena, so goopg's
      `pg_attribute` row was right and no following column moved — the PAYLOAD
      was the divergence. `encodeValuePG` stored the DECIMAL STRING, and every
      reader that trusts the TYPE feeds it to `numeric_out` as a
      `NumericData`, reading `"1234"` as `n_header 0x3231` (NUMERIC_POS,
      dscale 12849) with weight 13363: a PG 18.3 standby, `pg_amcheck`'s heap
      tier, and goopg's OWN PG-format index tuples, where `PGCompareNumeric`
      falls back to `bytes.Compare` and orders `-1000` above `0`. The
      serializer already existed — `internal/pgnodes/datum.go` ported
      `numeric_in`/`numeric_out` in full for pg_node_tree — so the slice
      EXPORTED it for the heap (`internal/pgnodes/numeric_storage.go`) instead
      of writing a second port. Four seams: encode (via the new
      `varlenaBytes`), decode, the `internal/wal` pgoutput sibling (unrouted it
      would have shipped the raw digit array to a subscriber WITHOUT erroring),
      and `pgIndexKeyImageIsPGFaithful`, which now refuses NO type and closes
      the M0130-S11.4 B2-a ledger row. Unlike uuid/interval there IS pre-flip
      data everywhere, so `NumericTextFromStoredPayload` reads both forms and
      the two are EXACTLY disjoint (a payload spellable from `[0-9+-.eE]` is
      always legacy text: short/special headers have a high byte >= 0x80,
      long-form digits <= 0x27, long-form zero 0x00). Six executor tests that
      probed numeric indexes with hand-built BLOB keys moved onto the engine
      funnels (`openIndexTreeForTest`/`indexProbeForTest`/the new
      `indexProbeMultiForTest`/`compositeUpperBound`), and the relhasindex
      suite's "undescribable index" premise moved from numeric to `interval`.
      Gates: 9 new unit tests across executor/wal/pgnodes + the inverted
      `TestPGIndexKeyImagesStayPGFaithful` guard + numeric's row in
      `TestPGIndexTupleKeyOrdersEveryDescribableType`; units PASS;
      tpch-spotcheck PASS (Q12=2, Q13=35) ON THE PRE-FLIP CLUSTER, i.e. the
      legacy read path against real data; TPC-DS SF0.5 sweep PASS=95
      MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 with 57 value checksums over
      decimal-heavy queries. 4 ledger rows: numeric-keyed indexes built before
      this loop need REINDEX (the key format is recomputed from the catalog,
      not stamped on disk), the dual read is goopg-side only (a PG standby
      still misreads a pre-flip row), `numeric[]` is built with elemtype OID
      25 (text) because `arrayElemTypeInfo` has no numeric case, and numeric
      NaN/+-Infinity still has no Datum representation.
      **Slice landed 2026-08-11 (21st) — `interval[]` / `uuid[]` / `numeric[]`
      ELEMENT images** (design
      `docs/design/0119-0006-array-element-pg-images.md`). The residue the three
      column slices each left behind, closed as ONE slice because all three
      ledger rows named the same seam. `arrayElemTypeInfo` had no arm for any of
      the three, so all three fell to the *unknown element type* fallback and
      were written as an `ArrayType` whose `elemtype` field says 25 (text) while
      `pg_attribute.atttypid` for the same column says `_interval`/`_uuid`/
      `_numeric` — the blob and the catalog disagreed about one column's element
      type, which is worse than "the elements are text". The layout was pinned
      from the oracle, not derived: PG 18.3 `pg_column_size` is 56/56/44 for a
      2-element interval/uuid/numeric array = 24-byte header + two 16-byte
      fields at align 8, + two at align 1, + a 10-byte varlena padded to 12 plus
      an 8-byte one at align 4, i.e. the new `(2950,16,1,false)` /
      `(1186,16,8,false)` / `(1700,-1,4,true)` arms. All six encode/decode arms
      are ports of the SCALAR arms, not second implementations; numeric is the
      one varlena element whose body is not its own text, so
      `encodeArrayElem`'s `if varlena` short-circuit grew a case ahead of it.
      Only interval moves a user-visible answer, onto PG:
      `{1 mon,30 days,2 hours}` → `{"1 mon","30 days",02:00:00}`. Pre-flip data
      needs no byte analysis this time — the blob STATES its own element type,
      and elemtype 25 under one of these columns can only be the old fallback.
      Gates: `TestArrayCodecPGType{ElementRoundTrip,OnDiskLayout,
      LegacyTextBlob,InvalidElement}` (the layout test is the one a round-trip
      cannot make) + a live throwaway server reproducing all three renderings
      end to end. 3 ledger rows: array SUBSCRIPT still yields a `KindString` so
      `c[1] = c[2]` over `{'1 mon','30 days'}` is still `f` vs PG's `t` (the
      cause moved from storage to the expression evaluator),
      `internal/wal/pgoutput.go` ignores `t.IsArray` so logical replication
      decodes ANY array as its scalar element type (pre-existing), and
      `interval[]` index-key elements are still refused by
      `decodeArrayKeyElemText`.
      **Slice landed 2026-08-11 (22nd) — `arr[i]` yields the ELEMENT TYPE's
      Datum, not text** (design
      `docs/design/0119-0006-array-subscript-element-typing.md`). Closes the
      subscript row the 21st slice opened: the storage flip did not move
      `c[1] = c[2]` over `ARRAY['1 mon','30 days']::interval[]`, because the
      subscript evaluator decoded the array to TEXT and returned a
      `KindString`, so `compareDatum` never reached its `interval_cmp_value` /
      numeric ladders (upstream `ExecEvalSubscriptingRef` returns a Datum OF
      the element type). One wrong-shape Datum, four wrong answers vs the
      oracle — `interval[]` equality, `numeric[]` equality (`'1.50' = '1.5'`),
      `numeric[]` ordering (`'10' > '9'`) and `float8[]` ordering (`9.5 > 10.2`
      answered `t`) — plus `a[1] + a[3]` over an `int4[]` column failing
      ANALYSIS with 42804. Three sites shared one blind spot (a user array
      column is `catalog.Type{Name:<ELEM>, IsArray:true}`, never `elem[]` and
      never `_elem`): the analyzer's `ArraySubscriptExpr` arm, the planner's
      `exprType` arm, and the executor via a new `FuncCall.ReturnType` stamp in
      `resolveExpr`. The fourth site is the one with reach beyond arrays: five
      `case *FuncCall:` clone-with-rewritten-children helpers (`FoldConstants`,
      `remapColumnRefsToSchema`, `shiftColumnRefsBy`, `shiftExprColumnIdx`,
      `unnest.go`'s local `rewriteIdx`) rebuild the node field by field and
      never listed `ReturnType`, so a USER-DEFINED function call that survived
      constant folding or a column remap was silently re-typed as unknown too.
      Gates: 21 new end-to-end SQL assertions all captured from the PG 18.3
      reference cluster (a `compareDatum` unit test would have passed
      throughout — which is how this survived the storage slice) + a guard test
      for the shapes that already worked, units, tpch-spotcheck (Q12=2/Q13=35),
      `TestPort_RegressSuite`. 3 ledger rows: date/time element types still
      text on purpose, the `ReturnType` stamp drops the element typmod, and
      array SLICES (`a[1:2]`) are rejected by the LEXER — unimplemented one
      layer below this slice.
      **Slice landed 2026-08-11 — pgoutput decodes array columns:**
      `internal/wal/pgoutput.go` switched on `catalog.Type.Name` alone, and a
      goopg array column is `Type{Name:<ELEMENT>, IsArray:true}`, so under
      logical replication EVERY array column was decoded as its scalar element
      type — an `int4[]` shipped `128` (the ArrayType header's `len<<2`), a
      `uuid[]` shipped `e0000000-0100-…`, an `interval[]` shipped
      `98 years 11 mons 01:11:34.96752`. Worse than a wrong value: the byte
      count is wrong too, so every FOLLOWING column decoded from the middle of
      the array body (gated — the trailing `int4` came back `1`, not `42`).
      `pgoPhysicalAlign` had the mirror bug (an `interval[]` aligned to the
      ELEMENT's `'d'`, where an ArrayType is a varlena at `'i'`), and the `R`
      message advertised the ELEMENT's OID (23, not `_int4` 1007). Because
      `internal/wal` cannot import `internal/executor` — which is WHY the
      support was missing — the renderer moved down to a new leaf package
      `internal/pgarray` (`ElemTypeInfo`/`RenderText`/`DecodeElem`/
      `QuoteTextElem`) that the executor now delegates to, so the tree holds
      ONE element table. Gates: `TestPgoDecodeArrayColumns` (5 element types,
      expected texts from the PG 18.3 oracle on 65432),
      `TestPgoPhysicalAlignArrayIsVarlenaAlign`,
      `TestEncodePgoTuplePhysicalArrayDoesNotShiftFollowingColumn`,
      `TestPgOutputRelationAdvertisesArrayTypeOID` — all four non-vacuous.
      Design: `docs/design/0119-0006-pgoutput-array-columns.md`. 3 ledger rows:
      no subscriber round-trip E2E, TOASTed arrays still refused,
      multi-dimensional / NULL-element arrays unhandled.
      **Slice landed 2026-08-11 (24th) — the checkunique tier's POSTING-LIST
      arm is under test** (design `docs/design/0119-0006-checkunique-tier-amcheck.md`
      §Gates). A deduplicated leaf item holds ONE key over many heap TIDs, so a
      uniqueness violation can live entirely inside a single line pointer —
      upstream loops over the posting list for exactly that case
      (`bt_entry_unique_check`) and `bt_report_duplicate` prints ` posting N`
      instead of a second `tid=` when the two entries share the line pointer.
      goopg's arm shipped with the tier but was reachable by no test, because
      goopg only deduplicates under specific write churn and driving a real tree
      cannot place a posting list where a test needs one. Landed
      `btree.IndexFormat.PGBTPostingRaw` (the exported face of the tree's own
      `marshalPosting`, sibling of the already-exported `PGBTItemRaw` — fixtures
      must not hand-roll the alt-TID header, which moved once already in
      M0130-S11.4) plus five fixtures: duplicate inside one posting list,
      dead-version posting list (clean), posting-vs-plain (the mixed errdetail),
      adjacent distinct-key posting lists (no false positive), and the TUPLE
      format, where each expanded key carries its own heap TID so the duplicate
      shows only under the TID-blind `CompareKeyAttrs` — the same page under the
      bytewise default is asserted to report NOTHING, which is what makes the
      comparator argument load-bearing. Non-vacuity mutation-checked twice
      (collapsing the posting expansion; neutralising the ` posting N`
      rendering). Ledger row: goopg's deduplication is still not driven
      end-to-end into a posting list a live `--checkunique` run then reads.
      **Slice landed 2026-08-12 (25th) — array index-key DECODABILITY** (design
      `docs/design/0119-0006-array-index-key-decodability.md`). An index-only
      scan over an ALL_VISIBLE page answers the query FROM the key, so a key
      encoding goopg cannot invert must be declined UP FRONT — and the predicate
      that did so read `!col.Type.IsArray && isIntervalTypeName(...)`. The
      `!IsArray` guard is array-shaped on purpose (a goopg array column is
      `Type{Name:<ELEMENT>, IsArray:true}`, so unguarded it would have matched
      for the wrong reason), and it inverted the answer: `interval[]`, whose
      element key is the SAME non-invertible `interval_cmp_value` span, was
      declared decodable. Confirmed at HEAD with no corruption and no exotic
      plan — `SELECT i FROM av WHERE i = '{3 days}'` over an indexed
      `interval[]` column failed the whole statement with `XX000: IOS decode:
      btree: interval key is the comparison span …`, and the same for every
      element type `decodeArrayKeyElemText` refuses (`date[]`, `time[]`,
      `timetz[]`, `timestamp[]`, `timestamptz[]`, `bytea[]`). Decodability is
      now answered by the key layer (`indexKeyColumnIsDecodable`, recursing into
      the element exactly as `decodeArrayBTreeKey` does) over a rendering table
      lifted out of the decoder as `arrayKeyElemRenderer`, so one table both
      renders and predicts. The refused element types STAY refused and the open
      ledger row's proposed `formatInterval` arm is RETRACTED as wrong:
      interval/timetz keys keep no split to render (PG calls `'1 mon'` and
      `'30 days'` equal, so the key must lose the difference), and
      date/time/timestamp/bytea have no heap element image to agree with — a
      key-side rendering would make index text and heap text disagree. The
      parity gate surfaced a second drift (Hard-won Rule #2): `uuid` was listed
      with the text-likes in `decodeIndexKeyColumn` but not in
      `decodeBTreeKeyToDatum`, so the single-column lane let it reach the
      `default:` arm that reads any 8 leading bytes as an ENUM sort order and
      never errors — an empty Datum for a real uuid, latent only because a uuid
      index takes the PG tuple-image key path. Gates:
      `TestArrayIndexOnlyScanReadsHeapForRefusedElement`,
      `TestIndexKeyDecodableMatchesDecoder` (20 types × {scalar, array}, both
      directions),  `TestIndexKeyDecodeSiblingsAgree` — each non-vacuous under
      its own mutation. 2 ledger rows (no heap element images for
      date/time/timestamp/bytea/enum arrays; array-keyed indexes cost heap
      fetches PG answers index-only).
      **Slice landed 2026-08-12 (26th) — `date[]`/`time[]`/`timestamp[]`/
      `timestamptz[]`/`timetz[]`/`bytea[]` ELEMENT images** (design
      `docs/design/0119-0006-array-element-datetime-images.md`). Part 2 of the
      21st slice, one type family later: `pgarray.ElemTypeInfo` had no arm for
      any date-time type or for `bytea`, so all six fell to the *unknown
      element* fallback — an `ArrayType` whose `elemtype` says **25 (text)** over
      the literal characters the user typed, while `pg_attribute.atttypid` for
      the same column says `_date`/`_time`/`_timestamp`/`_timestamptz`/`_timetz`/
      `_bytea` (confirmed at HEAD on a throwaway cluster: OIDs
      1182/1183/1115/1185/1270/1001 over text bodies). goopg read its own text
      straight back, so the defect is invisible from inside the engine and
      visible only to a descriptor-trusting reader — a PG 18.3 standby,
      `pg_amcheck`'s heap tier, the pgoutput decoder. The user-visible half is
      that the text path echoed the INPUT spelling instead of the type's OUTPUT
      function: `'{2020-1-2}'::date[]`, `'{1:2:3}'::time[]`,
      `'{04:05:06.100000}'::time[]`, a `+02` timestamptz and
      `'{01:02:03+05:00}'::timetz[]` were five wrong answers against the PG 18.3
      oracle, all five now byte-identical to it. Widths/alignments are
      `pg_type`'s own, cross-checked against `pg_column_size` (32/40/40/40/56/44).
      The encode side DELEGATES to `encodeValuePG` with the scalar element type
      instead of re-deriving the image, so element and column cannot drift
      (Hard-won Rule #2 made structural); the decode side renders through new
      leaf `pgdatetime.Format{Date,Time,Timestamp,TimestampTZUTC,TimeTZ}` —
      ports of `date_out`/`time_out`/`timestamp_out`/`timetz_out` over a `j2date`
      port, in the leaf package because `internal/wal` cannot import the
      executor — and `executor.byteaOutHex` moved down to `pgarray.ByteaOutHex`
      for the same reason. The slice also surfaced a fidelity bug no reader could
      notice: upstream `construct_md_array` re-aligns the running length after
      EVERY element including the last, so a 1-element `timetz[]` is 40 bytes in
      PG and was 36 in goopg — goopg's arrays were a different SIZE from PG's for
      the same value (previously-landed element types unaffected: 56/56/44).
      Gates: 4 `pgdatetime` format tests (the timetz one asserts the zone SIGN
      direction, which a whole-value table can pass with the sign inverted),
      5 array-codec tests (round-trip normalisation, on-disk layout,
      element-bytes-equal-scalar-column-bytes, legacy pre-flip blob, invalid
      element) and 3 new `TestPgoDecodeArrayColumns` rows; three
      mutation-checked. Units + `TestPort_RegressSuite` + tpch-spotcheck
      (Q12=2/Q13=35) PASS. 3 ledger rows: timestamptz elements render in UTC
      only, the date/timestamp input functions reject `BC` and `HH:MM` spellings
      PG accepts (pre-existing in the scalar column, now inherited by arrays),
      and `decodeArrayKeyElemText` still refuses these element types although the
      "no heap image to agree with" half of that refusal is now gone.
      Remaining for M0119-0006: the whole-database (unscoped) pg_amcheck run —
      ledger row 2026-08-10. (Corrected 2026-08-14 by the 83rd slice: the
      "`box`/`int4range` key encodings" half was a misattribution — box has no PG
      btree opclass at all, and int4range is blocked on the range value model;
      the expression-key gate for both is now landed.)
      **86th slice (2026-08-14): box rejection SQLSTATE polish (ledger row 1356
      item 2).** `btreeKeyTypeRejectionError` emits PG's exact `42704 "data type
      box has no default operator class for access method \"btree\""` + HINT
      (indexcmds.c:2270-2277) for a `box` key on BOTH the named-column and
      expression branches, instead of goopg's internal `0A000`; every other
      unsupported key type (int4range, …) keeps `0A000`. Gate:
      `TestExpressionIndexKeyRejectsBoxAndInt4Range` (split expectations) +
      `TestNamedColumnIndexRejectsBoxWith42704` + units + tpch-spotcheck
      (Q12=2/Q13=35).
      **87th slice (2026-08-14): logical walsender cross-DB dbOid threading
      (ledger row 1354 claim 2).** The walsender resolved every catalog lookup
      against `DefaultDBOid` (DB 1), so a logical slot on a non-default database
      decoded NOTHING — the DB-1-scoped snapshot dropped every change at
      `snap.Lookup` — and rendered wrong/numeric `reg*` names. Three consumers
      in `runLogicalWalsender` (reg* renderer, catalog snapshot, publication
      filter) all now receive the threaded dbOid under the ≠0 guard (a
      renderer-only fix is dead code). `BuildCatalogSnapshot`'s `regOut` became
      a nil-able single param so `dbOid ...uint32` could be the variadic (Go
      forbids two variadics). Tests: `TestBuildCatalogSnapshotScopesToDBOid`,
      `TestPgOutputEmitsChangeForNonDefaultDB` (the "was silently nothing"
      proof), `TestBuildPublicationFilterResolvesCrossDB`,
      `TestRegOutRendererCrossDB`. Design:
      `docs/design/0119-0006-walsender-cross-db-dboid-threading.md`.
      **88th slice (2026-08-14): regtype catalog-representation — NamespaceOID on
      the four user-type registries (ledger row 1355 slice A).** The
      enum/domain/composite/range registries (`EnumType`/`Domain`/`CompositeType`/
      `RangeType` in `internal/catalog/catalog.go`) tracked no schema, so the
      regtype renderer could not know a user type's real namespace. Added
      `NamespaceOID uint32` to all four structs (mirroring `UserCollation`),
      populated at CREATE TYPE (all four branches in `execCreateType`) and CREATE
      DOMAIN (`execCreateDomain`) via the CREATE AGGREGATE schema-with-public-
      fallback pattern, and at WAL/startup reload (`reloadUserEnumsFromHeap`/
      `reloadUserDomainsFromHeap`/`reloadUserRangeTypesFromHeap` now capture
      pg_type typnamespace col 2, previously dropped). ALTER TYPE needs no change
      (it re-registers through `RegisterCompositeTypeWithFields`, reusing the same
      struct pointer). Test: `TestCreateTypeDomainRecordsNamespaceOID`. No
      renderer touched here — slice B consumes the field.
      **89th slice (2026-08-14): regtype renders a user type's ACTUAL schema +
      quote_identifier (ledger row 1355 slice B).** `userTypeNameForOID`/`RegtypeName`
      (`internal/executor/expr.go`) take a per-schema `qualify func(schema string) bool`
      instead of a fixed bool; all ten user-type arms capture `NamespaceOID`, resolve
      via the new `Catalog.SchemaNameForOID`, and render `regOutQualified(schema, name,
      qualify(schema))` with the `"[]"`/multirange suffix kept outside quoting. New
      `regOutQualifySchema` defaults ""→"public" before evaluating the predicate so a
      NamespaceOID==0 type behaves like public. `regOutShared` drops the separate
      `regtypeQualify` bool (regtype arm now uses the same `qualify` as regclass/
      regcollation/regprocedure); the `::regtype` cast / `format_type()` / walsender /
      RAISE now render an off-path non-public type as `schema.name`. Design:
      `docs/design/0119-0006-regtype-actual-schema-qualifier.md`. Tests:
      `regtype_actual_schema_test.go` (off-path actual schema, mixed-case quoting,
      sibling agreement). Gates: go build + executor/server/wal/catalog/plpgsql suites
      + tpch-spotcheck (Q12=2/Q13=35) + pre-commit units all PASS. Residual: the
      wire/COPY/`reg*`→text/array paths still pass a constant-public predicate (row
      1339 family) — ledgered as a follow-up row.
      **90th slice (2026-08-14): regprocedure arglist carries the resolved
      arg-type OID (ledger row 1351 second half).** A BARE `char` arg (bpchar,
      parser `Args=[1]`) was indistinguishable from OID-18 `"char"` in the
      regprocedure arglist — both rendered `"char"`, where PG's `format_type_be`
      renders `character` for the bare form. New `Routine.ArgTypeOIDs []uint32`
      (parallel to ArgTypes/ArgTypeSchemas, `json:",omitempty"`), captured at
      CREATE FUNCTION/PROCEDURE by `argTypeOID` keyed on the raw element name
      (`a.Name=="char"`, BEFORE `routineArgTypeName` bakes `[]`; `len(Args)==0 →
      OIDChar(18)` else `OIDBpChar(1042)`, 0 elsewhere); `RegprocArg.OID` threads
      it to BOTH sibling renderers (`regprocedureArglist` executor /
      `argListTypeDisplay` catalog, re-signed `(name, oid)`), whose `char` arm
      renders OIDBpChar→`character` and OIDChar/0→`"char"` (0 = builtin/pre-change,
      backward-compat free). `buildPGProcRow` AND its index-key sibling
      `insertPgProcIndexEntries` prefer the stored OID (guarded `!=0`), fixing
      proargtypes for a quoted `"char"` (18 not 1042). Design:
      `docs/design/0119-0006-char-arg-oid-per-arg.md`. Tests:
      `TestRegprocedureArglistCatalogAndExecutorAgree` char scalar+array cases,
      `TestCreateFunctionCapturesCharArgOID`,
      `TestRegprocedureCharArgCastAndWireAgree`. Gates: package suites +
      pre-commit units + `TestPort_RegressSuite` + tpch-spotcheck (Q12=2/Q13=35)
      all PASS. Deferred (2 new rows): the pg_get_function_* `canonicalTypeName`
      renders `char`→`character` unconditionally (pg_dump signature-rebuild path);
      quoted `"char"(N)` misclassified by the `len(Args)` heuristic.
      **91st slice (2026-08-15): arg-type name case preservation (ledger row
      1344).** `routineArgTypeName` (operators_call.go:671) folded a quoted arg
      type's name with `strings.ToLower`, so `CREATE FUNCTION f(offpath."MyType")`
      stored `ArgTypes[0].Name="mytype"` and rendered `offpath.mytype` where PG's
      `format_type_be` emits `offpath."MyType"`. Fix: drop the fold (the parser
      already lowercases unquoted `TokenIdent` and preserves `TokenQuotedIdent`;
      `Signature()`/`TypeNameToOID`/`ArgTypeDisplayAlias` all resolve
      case-insensitively, so DROP/ALTER/COMMENT matching is unchanged) and teach
      the executor `regprocedureArglist` visible arm (schema non-empty,
      non-pg_catalog, on-path) to render `pgQuoteIdent(base)` — the
      `format_type_be` default path — so an on-path `"MyType"` renders `"MyType"`
      not `MyType`. Design: `docs/design/0119-0006-arg-type-case-preservation.md`.
      Tests: `TestCreateFunctionCapturesArgTypeCase`,
      `TestRegprocedureArglistQuotesMixedCaseUserType`. Gates: catalog+executor
      package suites + pre-commit units + `TestPort_RegressSuite` (31 PASS/0 FAIL,
      `-timeout 55m`) + tpch-spotcheck (Q12=2/Q13=35) all PASS. Deferred (1 new
      row): the catalog sibling `argListTypeDisplay` is left bare — name-only, it
      cannot quote a mixed-case user type without also quoting builtin display
      strings that pass `ArgTypeDisplayAlias` unchanged; OID-keying is blocked on
      row 1343's namespace/OID work.
      **92nd slice (2026-08-15): the pg_get_function_* `char` arg now renders
      OID-accurately (deferral row 1358).** `canonicalTypeName` (expr.go) was
      name-keyed and rendered `char`→`character` unconditionally, so a quoted
      `"char"` arg (CHAROID 18) rendered `character` where PG's `format_type_be`
      renders `"char"` — a pg_dump signature-rebuild divergence. Re-signed to
      `canonicalTypeName(name string, oid uint32)`; its `char` arm emits `"char"`
      for OIDChar(18) and `character` for 1042/0 (0 = no-OID baseline, aggregates +
      pre-90th routines — deliberately NOT the sibling regprocedure arm's 0→`"char"`).
      All 7 call sites threaded: `buildFunctionArguments`/`buildTableResult`/
      `buildFunctionDef` arg-arms read `r.ArgTypeOIDs[i]` under a nil/len guard;
      `buildFunctionDef` RETURNS-clause, `pg_get_function_result` scalar, and
      `routineArgListStr` pass 0. Test `TestPgGetFunctionArgsQuotedCharRendersQuoted`.
      Gates: go build + executor package suite + pre-commit units + tpch-spotcheck
      (Q12=2/Q13=35) + `TestPort_RegressSuite` (31 PASS/0 FAIL) all PASS. Deferred
      (2 new rows 1361/1362): the return-type path (`RETURNS "char"`) is still
      OID-less (no `ReturnTypeOID` on `Routine`), and the arg-list parser rejects a
      named arg `g(x "char")` where PG accepts `argname argtype`. Design updated in
      `0119-0006-char-arg-oid-per-arg.md` §6 (open item marked resolved).
      **93rd slice (2026-08-15): a bare user-type arg name resolves its owner
      schema (ledger row 1343).** `argTypeSchema(t, cat, dbOid)` (operators_call.go)
      probes `LookupEnum`→`LookupDomain`→`LookupCompositeType`→`LookupRangeType`→
      `LookupRangeTypeByMultirangeName` on the ELEMENT name (`[]` stripped) and
      returns `SchemaNameForOID(hit.NamespaceOID)`, so `CREATE FUNCTION
      g(offpath.mytype)` captures the owner schema at CREATE time for LIVE-created
      types; both capture sites (execCreateFunction/execCreateProcedure,
      operators_ddl.go:11889/12651) pass the RAW `o.ctx.CurrentDatabaseOid` (type
      registries are raw-keyed). Test `TestCreateFunctionCapturesBareArgTypeSchema`.
      Gates: go build + executor/catalog suites + pre-commit units + tpch-spotcheck
      (Q12=2/Q13=35) + `TestPort_RegressSuite` (45 PASS/0 FAIL) all PASS. Deferred
      (row 1363): type-registry dbOid keying mismatch (live DDL raw vs recovery
      `DefaultDBOid`) — a bare type in a non-default DB after restart may miss the
      probe.
      **94th slice (2026-08-15): the pg_get_function_* RETURN type renders
      OID-accurately for the `"char"` spelling (deferral row 1361).** `RETURNS
      "char"` rendered `character` because `catalog.Routine` had no return-type OID.
      Added `ReturnTypeOID uint32` (catalog/routines.go); `argTypeOID`'s body is
      extracted as shared `charTypeOID(parser.ColumnType)` (quoted `"char"`→OIDChar
      18, bare `char`→OIDBpChar 1042, else 0) so the argument and RETURN paths
      cannot drift, and `execCreateFunction` stores `charTypeOID(s.ReturnType)`
      (procedures have no RETURNS). Both render sites — `pg_get_function_result`
      (expr.go:10282) and `buildFunctionDef` RETURNS-clause (expr.go:15188) — pass
      `r.ReturnTypeOID` instead of 0, and `buildPGProcRow` (sys_pg_proc.go) prefers
      it for prorettype (sibling of proargtypes). Test
      `TestPgGetFunctionResultQuotedCharRendersQuoted`. Gates: go build +
      executor/catalog suites + pre-commit units + tpch-spotcheck (Q12=2/Q13=35) +
      `TestPort_RegressSuite` (47 PASS/0 FAIL) all PASS. Deferred (row 1364):
      ARRAY-typed args/returns write the ELEMENT OID (or a name-only fallback) into
      proargtypes/prorettype where PG writes the ARRAY OID — `charTypeOID`/`argTypeOID`
      key on the element name and `TypeNameToOID` has no `[]` arm; the same latent
      gap exists on the arg side from the 90th slice.
      **28th slice (2026-08-12): the `HH:MM` half of that inherited input gap is
      closed.** A time-of-day with no seconds field is ordinary PG input
      (`DecodeTime` reads seconds only `if (*cp == ':')`, leaving `tm_sec = 0`),
      but goopg's two layout tables disagreed: `evalTypedStringLit` lists
      `"2006-01-02 15:04"`, `parseCopyTimestamp` did not — and the latter is what
      COPY TEXT, `encodeValuePG` and the array-element encoder funnel through, so
      `INSERT INTO t(ts) VALUES ('2020-01-01 10:00')` raised 22007 while
      `timestamp '2020-01-01 10:00'` parsed. Supplied once in `padTimeFields`
      (`internal/pgdatetime/normalize.go`) instead of per table, incl. `10:00+05`,
      `10:00 PM` and the empty trailing field `10:00:`. `'10:00.5'` (PG reads
      `MM:SS.f` → `00:10:00.5`) and `'10::00'` (empty MINUTE field) stay refused
      — guessing there would be a wrong time, not an error — as ledger rows, each
      with a test pinning the refusal. Design `0125-0007-…` §6 + README row.
      Gates: units, `TestPort_RegressSuite`, tpch-spotcheck (Q12=2/Q13=35) PASS;
      mutation-checked. (This note used to end "`BC` era input remains open" —
      stale since the 30th slice landed `internal/pgdatetime/era.go`; corrected
      2026-08-13 after it cost a loop's selection time.)
      **35th slice (2026-08-11) — `ValidateDate()`'s month/day RANGE check is now
      a real port**: `'20201301'`, `'2020-13-01'`, `'2020-01-32'` were flat 22007
      ("invalid input syntax") — DecodeDateTime recognises the shape, only
      ValidateDate rejects the values, and goopg had no ValidateDate step at all.
      New `pgdatetime.ValidateMonthDay`/`ValidateDateToken` (month 1..12, day
      1..31 — both of ValidateDate's overflow arms map to the same SQLSTATE
      22008) wired into `parsePGDateText` and `parsePGTimestampTextParts`.
      `ValidateDateToken` locates MM/DD from the trailing `-MM-DD` so it survives
      a run-together year of any width. NOT covered: the day-in-month/leap-year
      arm (`'2020-02-30'` still silently accepted — needs the era-adjusted year,
      which is applied AFTER this check runs today; separate follow-up, ledger
      row 2026-08-11). Gates: `TestValidateDateToken` (pgdatetime),
      `TestDateTimeInputErrorSeparatesRangeFromSyntax` (updated expectations) +
      full executor/pgdatetime suites green; `scripts/tpch-spotcheck.sh` PASS
      (Q12=2/Q13=35). Design `0125-0007-pg-faithful-date-field-decode.md` §13.
      **36th slice (2026-08-11) — the day-in-month arm landed, ordering fix
      different than planned**: waiting for `ApplyEra`'s post-`time.Parse`
      astronomical year never works — Go's `time.Parse` (unlike
      `time.Date`/`AddDate`) already rejects an impossible calendar day itself
      (`"2020-02-30": "day out of range"`) before `ApplyEra` runs, so the check
      must run BEFORE `time.Parse`, not after. New
      `pgdatetime.DateTokenYear`/`AstronomicalYear` compute the astronomical
      year straight from the token's digits (no `time.Time` needed), feeding a
      new `pgdatetime.ValidateDayOfMonth` (`day_tab[isleap]` port). Shared
      `validateDateTokenFull` in `internal/executor/copy_text.go` composes all
      three ValidateDate() checks at both call sites. `'2020-02-30'`,
      `'2021-02-29'`, `'2021-04-31'` are now 22008, verified live against PG
      18.3. Deferred (ledger, new row): a BC February-29 date where the
      literal year's leap-ness disagrees with the astronomical year's
      (`'0001-02-29 BC'`, PG accepts) hits the same `time.Parse` race one layer
      down, since `time.Parse` still sees the literal (pre-era) year. Gates:
      `TestValidateDayOfMonth`/`TestDateTokenYear`/`TestAstronomicalYear`
      (pgdatetime), `TestDateTimeInputErrorSeparatesRangeFromSyntax` extended
      (executor); full executor/pgdatetime suites green;
      `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35). Design
      `0125-0007-pg-faithful-date-field-decode.md` §14.
      **37th slice (2026-08-11) — the DATE half of the BC leap-day race is
      fixed.** New `bcLeapDateFallback` (`internal/executor/copy_text.go`) re
      -derives month/day/astronomical-year straight from the token
      `validateDateTokenFull` already validated, and builds the `time.Time`
      via `time.Date` directly instead of `time.Parse` — bypassing both
      `time.Parse`'s literal-year day check AND `ApplyEra`'s literal→
      astronomical shift together. `'0001-02-29 BC'::date` no longer raises
      the syntax-shaped 22007; it now reaches the carrier-range 22008 the
      un-representable astronomical year 0 still deserves (same 282-ref
      `KindTime` nanosecond-carrier blocker as the sibling rows). Scoped to
      `parsePGDateText` only — `parsePGTimestampTextParts` has the identical
      bug but composes date+time-of-day in one `time.Parse` call, so its fix
      needs the time fields threaded through too; deferred, ledger row
      appended. Gates: `TestDateTimeInputErrorSeparatesRangeFromSyntax` new
      `'0001-02-29 BC'` case, `TestDateEraLiteralAndCopyPathsAgree` extended;
      full executor/pgdatetime suites green. Design
      `0125-0007-pg-faithful-date-field-decode.md` §15.

      **38th slice (2026-08-11) — the TIMESTAMP half of that race is fixed
      too, closing the 37th slice's deferral.** New
      `bcLeapTimestampFallback` hooks INSIDE `parsePGTimestampTextParts`'s
      candidate loop (so the hour-24 / leap-second canonicalized candidate
      still takes its own turn — `'0001-02-29 24:00:00 BC'` composes to
      March 1st with `carry=1`). The 37th slice's open question — how to
      thread the time-of-day fields through the `time.Date` construction —
      is answered by NOT hand-parsing them: a leap PROXY YEAR (2000) is
      substituted into the date token and the candidate re-parsed through
      the ordinary `pgTimestampLayouts` table, so time of day, fractional
      seconds, the `T` separator and every zone spelling stay owned by the
      shared table (a private parser here would be a fourth instance of the
      two-table drift that table exists to end); the decoded wall clock is
      then rebuilt at the real astronomical year. The zone rule is applied
      to the proxy BEFORE the rebuild and its whole-day movement re-applied,
      because `tsApplyZone` can cross midnight
      (`'0001-02-29 00:30:00+05:30 BC'::timestamptz` is the 28th in UTC).
      Six PG 18.3 oracle cells captured live, all matching. Gates: two new
      tests (`TestBCLeapDayTimestampDecodesAtTheAstronomicalYear` — asserts
      the DECODED FIELDS, since a SQLSTATE-only assertion would pass with
      the fields still wrong — and
      `TestBCLeapDayTimestampFallbackKeepsTheErrorClasses`), both mutation
      -checked; `RALPH_PRECOMMIT_SCOPE=units` PASS; `tpch-spotcheck.sh` PASS
      (Q12=2, Q13=35). Design
      `0125-0007-pg-faithful-date-field-decode.md` §16.
      **39th slice (2026-08-11) — `timestamptz` OUTPUT renders its zone and
      leaves UTC.** goopg printed a `timestamp with time zone` exactly like a
      plain `timestamp`: stored instant, UTC, no zone marker, `SET TimeZone`
      ignored — so under a non-UTC session the text goopg returned denoted a
      DIFFERENT instant than the one it stored, with no error. New
      `config.FormatTimestampTZ` + `encodeTimezone`
      (`internal/config/timestamptz_out.go`) port `EncodeDateTime` with
      `print_tz=true` and `EncodeTimezone`
      (`postgres/src/backend/utils/adt/datetime.c`). Conversion and marker land
      together because neither is right alone. The per-DateStyle zone spelling
      is NOT uniform (ISO = numeric offset, no space; SQL/Postgres/German = the
      ABBREVIATION after a space), `EncodeTimezone` has three widths that all
      occur in real tzdata (`+00`, `+05:30`, `+05:53:28` — Kolkata LMT), and
      `" BC"` trails the ZONE, not the seconds. `Datum.TimeSub` turned out NOT
      to be a prerequisite: the two output paths that matter know the declared
      column type, and were split off the shared `case "timestamp",
      "timestamptz"` as siblings — `dispatch.go`'s `appendTypedCellText` and
      `copy_text.go`'s `datumToCopyText` (`timeZone` threaded through
      `EncodeCopyTextRow`/`EncodeCopyCsvRow`/`RunCopyTo`). Wiring the COPY half
      surfaced a pre-existing bug: standalone `COPY … TO STDOUT`
      (`dispatchCopyViaExecutor`) built its executor context by hand and never
      attached the session GUC hooks, so it read NO GUCs at all — `SET
      datestyle` was ignored there too, though the same statement inside a `\;`
      batch honoured it. Still deferred: the `::text` cast path (bare Datum, no
      `TimeSub` producer — M0127-P5.9-u) and POSIX `TimeZone` spellings
      (`'+05:30'`, inverted sign) which fall back to UTC. Gates: 19 PG-18.3
      oracle cells (`TestFormatTimestampTZAgainstPG18Oracle`) + one sibling test
      per output path, each also asserting the plain-`timestamp` column does NOT
      move; `TestPort_RegressSuite` PASS; `RALPH_PRECOMMIT_SCOPE=units` PASS;
      `tpch-spotcheck.sh` PASS (Q12=2, Q13=35). Design
      `0119-0006-timestamptz-output-zone-rendering.md` + README row
      `0119-0006v`.

      **40th slice (2026-08-11) — textual month names on date/timestamp
      INPUT.** `'May 1, 2002'::date` was a syntax error on goopg and an
      ordinary date on PG, as were `'1-Jan-2020'`, `'2002-May-1'`,
      `'1/May/2002'` and `'sept 1 2002'` — every spelling that writes the month
      out. On the COMPARISON path that is not even an error: `tryParseStringAs`
      reports a failed coercion as "leave it a string", so
      `d_date = 'May 1, 2002'` matched zero rows in silence (the M0125-0007
      wrong-answer shape). The textual month is implementable even though goopg
      still does not model the `DateOrder` GUC on input, and upstream says why:
      `DecodeDate` runs a first pass that "look[s] first for text fields, since
      that will be unambiguous month", so the month is in `fmask` before any
      numeric run is decoded and `'May 1 2002'` / `'1-May-2002'` /
      `'2002-May-1'` take one identical path. New
      `internal/pgdatetime/monthname.go` ports `DecodeNumber`'s
      `case DTK_M(MONTH)` arm minus its two `DATEORDER_YMD`-gated branches, and
      hooks into `normalizeInput` behind the `runTogetherDate` flag —
      `'May 1, 2002'::time` must stay an error. Two refusals are oracle-checked
      rather than omissions: `'2002-May-1T10:20:30'` IS an error on PG 18.3
      where the numeric-month spelling parses, and `:` is barred from the date
      token so `'10:00 May'` (a time plus a month) cannot build a date. Three
      ledger rows: PG's third-numeric-field reading (`'May 1 2 2002'` is
      `2002-05-01 20:02:00` — a windowed 2-digit year plus a run-together
      TIME), the 3-digit day's 22008-vs-22007 (shared with `padDateFields`),
      and the un-widened planner-side `pgnodes.parseDateFields`. Gates: 40
      normalizer cells + 5 executor entry-point tests, all probed against the
      65432 PG 18.3 reference cluster; `TestPort_RegressSuite` PASS;
      `RALPH_PRECOMMIT_SCOPE=units` PASS; `tpch-spotcheck.sh` PASS. Design
      `0119-0006-textual-month-name-date-input.md` + README row `0119-0006w`.

      **42nd slice (2026-08-12) — array elements render under the session
      DateStyle/TimeZone.** `array_out` formats nothing itself: it calls the
      ELEMENT type's output function per element, so a `timestamptz` inside an
      array honours `TimeZone` and `DateStyle` exactly as a scalar column does.
      goopg rendered every date-time element ISO/UTC, so a session in any other
      zone read a correct instant back under the WRONG offset, silently — the
      2026-08-12 ledger row, now `resolved`. Measuring the oracle found a second
      divergence the row did not predict: `date[]`/`timestamp[]` elements
      ignored `DateStyle` too, so the defect was never `timestamptz`-only
      (`time`/`timetz` ARE style-independent — oracle-checked, not assumed by
      analogy). **The blocker the row named was wrong**: it said no leaf package
      had a tzdata-backed zone lookup, but the 39th slice had already put one in
      `internal/config`, which `go list -deps` shows is a true leaf `pgarray`
      already depended on. The real, unrecorded blocker is structural — goopg
      fixes an array's `{…}` text at HEAP-DECODE time where upstream defers to
      `array_out` at OUTPUT time, and `DecodeRowIntoMctxPGTuple` has ~70 call
      sites (catalog reload, VACUUM, ANALYZE, DDL rescans) with no session at
      all. Hence an explicit `pgarray.OutputStyle` with a pinned default rather
      than an ambient lookup: every plain entry point survives as a
      default-style wrapper, and `seqScanOp`/`bitmapHeapScanOp`/
      `indexOnlyScanOp` resolve it ONCE in Open (the heap scans only when
      `colsHaveArray`, so an array-free relation pays nothing). The element
      formatters call `internal/config`'s own functions — the ones the SCALAR
      path calls — so element and column text agree by construction. The
      index-key sibling moved in the same commit (Rule #2): an index-only scan
      rebuilds array text from the KEY, so fixing only the heap path would make
      the same row print differently depending on the chosen plan. Four ledger
      rows: the fix, the `date`/`timestamp` widening, pgoutput's deliberate
      default (upstream runs output functions under the WALSENDER's GUCs), and a
      pre-existing defect found while verifying — `COPY … TO` of ANY non-text
      array column errors at HEAD (`int4[]` included, hence unrelated to
      date-time), because `datumToCopyText` ignores `Type.IsArray`. Gates: 33
      PG-18.3 oracle cells + 4 executor tests (sibling guard verified red by
      scripted revert); `TestPort_RegressSuite` PASS;
      `RALPH_PRECOMMIT_SCOPE=units` PASS; `tpch-spotcheck.sh` PASS. Design
      `0119-0006-array-element-output-style.md` + README row `0119-0006z`.

      **43rd slice (2026-08-12) — `COPY` of an array column, both directions.**
      Closes the row the 42nd slice filed against itself: `COPY … TO` of any
      NON-TEXT array column failed outright at HEAD (`int4[]` = "expected int
      datum for int4, got kind 3", `date[]` = "expected time datum for date,
      got kind 3"), and `COPY … FROM` could not read back its own output
      (`invalid integer "{1,2}"`). A user array column is
      `catalog.Type{Name:<ELEMENT>, IsArray:true}`, so both halves of the codec
      claimed the array under its ELEMENT's name; `text[]` worked only by
      accident, `text` matching no arm and falling through to the default
      `KindString` case — which is the correct behaviour for every array type.
      Upstream has no such table (`CopyOneRowTo` calls the COLUMN's output
      function, i.e. `array_out`), and goopg arrives at the same place by
      rendering the array text at HEAP-DECODE time, so the codec's whole job is
      to pass it through: `datumToCopyText` and `copyTextToDatum` now branch on
      `t.IsArray` before the type-name switch, the third pair of sites needing
      that exact guard after `encodeValuePG` (M0118-0002) and `pgoutput`.
      Escaping/quoting were deliberately NOT special-cased, and that is what
      reproduces PG byte-for-byte — the array text runs through the ordinary
      TEXT escaper and CSV field quoter, so `{"has,comma"}` comes out
      whole-array-quoted with doubled inner quotes. **Binary COPY is refused
      (`0A000`) rather than attempted, on a silent-corruption finding:** there
      `int4[]` also mismatched, but `text[]`/`bytea[]` fell through to the
      raw-bytes arm and SHIPPED the `{a,b}` text where upstream ships
      `array_send`'s binary shape — a stream no PG client can read, worse than
      an error. Gates: 5 new tests in
      `internal/executor/copy_array_test.go`, all verified red by scripted
      neutering of both guards, pinned to 3 oracle lines (TEXT, CSV, and
      `DateStyle='German, DMY'`) captured on the 65432 reference cluster and
      reproduced byte-for-byte on a live goopg;
      `RALPH_PRECOMMIT_SCOPE=units` + `tpch-spotcheck.sh`. Four ledger rows —
      `array_send`/`array_recv`, plus three findings NOT introduced here:
      `COPY … FROM` ignores `FORMAT csv` entirely (`copy_csv.go` is write-side
      only; no reader exists, so even `plain,7` fails "row has 1 fields"), the
      `CopyDone` frame after a failed COPY FROM is unhandled (session
      desync), and COPY FROM does not honour `DateStyle` on INPUT, so a
      `German, DMY` session cannot COPY back in the `{15.06.2020}` it just
      COPYed out (measured on both engines). Design
      `0119-0006-copy-array-columns.md` + README row `0119-0006aa`.

      **44th slice (2026-08-13) — the COPY stream nobody drained after the
      COPY had already failed.** Closes the 43rd slice's `CopyDone` row. A
      `COPY … FROM STDIN` that failed mid-stream left the session PERMANENTLY
      one `ReadyForQuery` ahead: goopg reports the decode error the instant the
      bad line is pushed (`handleCopyInFrame`, `copy.go:542-560`) and clears
      `copyIn`, but the frontend has already pipelined the rest of the file
      plus `CopyDone`, and those frames then hit the main loop's `default`
      arm — which answered EACH with `message type 'c' not yet supported`
      **and a second RFQ**, so every later statement read the wrong frame
      (`message type 0x5a arrived from server while idle`). Wrong-answer risk
      zero (the COPY correctly failed); session desync total. Upstream
      `postgres.c:5004-5013` accepts and ignores all three of
      `CopyData`/`CopyDone`/`CopyFail` with a bare `break` — no ErrorResponse
      and, critically, no RFQ — so the fix is ONE deliberately-empty `switch`
      arm in `internal/server/server.go`, placed after the `copyIn != nil`
      fast path so a live COPY is untouched. The inline-batch path
      (`runInlineCopyFromStdin`) had the identical exposure and is covered by
      the same arm. goopg's skip-until-`Sync` guard already matched upstream's
      `ignore_till_sync` shape and needed no change. Gates: 2 new tests in
      `internal/server/copy_error_drain_test.go`, both verified red by
      deleting the case (each then reports `frames="EZ"`, the live symptom);
      they had to move OFF `startTestServer`, which is storage-less and whose
      COPY FROM falls into a row-counting stub that happily reports `COPY 1`
      for `notanint`. E2E on a capped throwaway goopg (5533) and on the PG
      18.3 oracle (65432): both engines error, keep the session usable, and
      leave `count(*) = 0`. Two ledger rows — goopg's COPY error text leaks a
      raw Go `strconv` error and emits NO `CONTEXT:` line where PG gives
      `COPY <rel>, line 2, column a`, and the `startTestServer` COPY stub is a
      false-negative harness. Design `0119-0006-copy-error-frame-drain.md` +
      README row `0119-0006ab`.

      **45th slice (2026-08-13) — the CSV reader `COPY … FROM` never had.**
      Closes the 43rd slice's row: `COPY … FROM` ignored `FORMAT csv`
      ENTIRELY. `internal/executor/copy_csv.go` had a write side only
      (`EncodeCopyCsvRow`, M0097-0024), nothing routed to a reader —
      `PushLine` called `DecodeCopyTextRow` unconditionally and read exactly
      ONE option (`NULL`) — so a CSV stream was split on TAB and even an
      unquoted `plain,7` into a two-column table failed `COPY: row has 1
      fields, expected 2`; a session could not read back what its own
      `COPY … TO … (FORMAT csv)` had just written. New
      `parseCopyCsvFields`/`DecodeCopyCsvRow` port `CopyReadAttributesCSV`:
      quoted sections that may open and close MID-field (`"ab"cd` is one
      field `abcd`), the escape character (default = quote, hence doubled
      quotes), NO backslash escapes, and the NULL rule keyed on QUOTING not
      content — with the default null string `,,` is two NULLs but `"",""`
      is two empty strings, the one rule that corrupts silently rather than
      erroring. **The record boundary was the structural call:** upstream
      splits `CopyReadLineText` (tracks quote state, hands over COMPLETE
      records) from `CopyReadAttributesCSV`, but goopg's wire layer already
      splits `CopyData` on `\n` across four call sites, so the re-join lives
      in the executor (`pushCsvLine` + `csvPartial`, restoring the removed
      newline) instead of making that splitter CSV-aware. That makes
      "PushLine returned nil" no longer mean "a row was inserted", so two
      ends are closed explicitly: `Finish()` (both `CopyDone` sites +
      `RunCopyFromFile`) reports `unterminated CSV quoted field`, and
      `InCsvQuotedField()` stops the deprecated `\.` marker being honoured
      inside a quoted field — DATA there, as the oracle proves by swallowing
      it into the field. Collateral: `HEADER` on INPUT was never honoured in
      EITHER format (the skip now sits ahead of the format split, as upstream
      does it), and the two constructors had diverged BY CONSTRUCTION —
      `NewCopyFromExecutor` and `RunCopyFromFile` each hand-built the struct
      reading only `NULL`, so the file endpoint would have kept ignoring CSV;
      both now share `newCopyFromExecutor` and one `copyToFormat`, the same
      struct `COPY … TO` reads. Gates: 4 tests / 6 cases in
      `internal/server/copy_csv_from_test.go` on `startCopyExecServer`, all
      verified red by deleting the two-line route (each reproducing the filed
      symptom verbatim), 3 in `internal/executor/copy_csv_read_test.go`;
      `go test ./internal/executor/ ./internal/server/`,
      `RALPH_PRECOMMIT_SCOPE=units`, `scripts/tpch-spotcheck.sh` (Q12=2,
      Q13=35). The full oracle transcript replayed on a live goopg (5533)
      matches PG 18.3 byte for byte, per-column NULL flags and all three
      error messages included. Three ledger rows —
      `FORCE_NOT_NULL`/`FORCE_NULL` (planner-VALIDATED, reader-ignored, each
      inverting the NULL rule for its columns), `HEADER match` skipping
      without validating names, and the TEXT path keeping goopg's own
      field-count message while CSV uses upstream's two. Design
      `0119-0006-copy-csv-reader.md` + README row `0119-0006ac`.
      **46th slice (2026-08-13) — the trailing zone field a `time` accepted
      WITHOUT LOOKING AT IT.** The §9 ledger row said `'10:00 A.M.'::time` was
      wrongly accepted; the probe found the whole class — `stripTimeZoneSuffix`
      peeled EVERY trailing space-separated token that was not `AM`/`PM` and
      threw it away, so `'10:00 GARBAGE'`, `'10:00 Japan'`, `'10:00 zzz'` and
      `'10:00 pst pdt'` each stored a guessed `10:00:00` with no diagnostic —
      and the COPY TEXT reader shares the function, so a corrupt zone field in
      a load file was absorbed rather than reported. PG has THREE verdicts here
      and the TOKENIZER, not the decoder, picks between them: letters followed
      by `.`/`/`/`-` — or by `+`/a digit while the letters name no `datetktbl`
      keyword — become `DTK_DATE` and reach `pg_tzset()`, so a miss is 22023
      `time zone "a.m." not recognized` on the LOWERCASED token; a bare word
      stays `DTK_STRING`, never reaches the zone database at all, and a miss is
      22007 — hence `'10:00 Japan'` is an ERROR although `Japan` is a real zone
      name, while `'10:00 Etc/GMT'::time` is ACCEPTED and
      `'10:00 America/New_York'::time` is not (`pg_get_timezone_offset()`
      resolves a fixed-offset zone with no date; a DST zone needs one). Two
      measured corrections: **`datetktbl` holds not one timezone abbreviation**
      (they live in the GUC-selected `timezone_abbreviations` table), so
      `'10:00 UTC+5'` is ONE POSIX zone-spec token — `-05`, the POSIX sign being
      inverted, not UTC-plus-five — and era/meridiem are ordinary fields that may
      FOLLOW a zone (`'10:00:00 PST BC'`, `'10:00 AM BC'` parse) while two zone
      fields may not. New leaf `internal/executor/time_zone_token.go`
      (`classifyZoneToken`, `pgDateTimeKeywords`, `parsePOSIXZoneOffset`,
      `fixedZoneOffset`, `stripValidatedZoneSuffix`) is SHARED by
      `parseTimeString` and `parseTimeTZString` — which fixes three `timetz`
      bugs for free (`'10:00 BC'` rejected, a fixed-offset zone name rejected,
      22007 where PG says 22023) and picks up the attached `Z` that
      `NormalizeInput` folds a spaced zulu into, so `'10:00 Z'::time` stops
      failing as a malformed hour. Gates: 31 oracle-pinned inputs driven through
      BOTH paths from one shared table in
      `internal/executor/time_zone_token_test.go` (the sibling-paths rule),
      mutation-checked twice (collapsing the 22023 arm → 8 red; calling a DST
      zone fixed → 7 red); `go test ./internal/executor/ ./internal/pgdatetime/
      ./internal/config/ ./internal/pgarray/ ./internal/pgnodes/`,
      `TestPort_RegressSuite` (558 s), `RALPH_PRECOMMIT_SCOPE=units`,
      `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) all PASS; 14/14 probes
      byte-identical to PG 18.3 on a live capped server (5533), COPY TEXT
      included. Three ledger rows: `pg_tzset()`'s POSIX `tzparse()` is not
      ported (`'10:00 EST.5'`), `fixedZoneOffset` SAMPLES ten instants instead
      of reading the transition list, and the abbreviation table is still
      goopg's 40-entry map rather than the GUC-selected file. Design
      `0125-0007-…` §17 + README row.
      **54th slice (2026-08-13): binary `COPY` of the unsigned-identifier family
      (`oid`/`regproc`/`xid`/`xid8`) and `uuid`, both directions.** The default's
      `KindInt` escape shipped EIGHT bytes where `oidsend`/`regprocsend`/`xidsend`
      are `pq_sendint32`, and `uuid` shipped its 36-char TEXT where `uuid_send` is
      `pq_sendbytes(…, 16)`; the decode half handed all five back as raw-bytes
      string Datums. Coercion + upstream's 22003 range rule extracted into a
      shared `pgUnsignedIDFromDatum` so heap and wire cannot drift (the 53rd
      slice's `pgFloatFromDatum` move repeated). **The finding was `xid8`, and
      only the heap-agreement twin test could reach it:** its COPY arm was
      ACCIDENTALLY right at HEAD, so the pin pointed at the heap instead —
      `encodeValuePG` shared the 4-byte `xid` arm and truncated a
      `FullTransactionId` to 32 bits, `physicalPGTypeAlign` returned 4 where
      `pg_type` 5069 says `'d'`, and `internal/wal/pgoutput.go` had the same
      4-byte arm. Item stays UNCHECKED (standing slice-by-slice cluster).
      7 fail-when-broken guards red at HEAD; oracle E2E byte-identical both
      directions incl. `xid8 = 9007199254740993`. 1 ledger row resolved, 3 filed
      (`reg*`/`cid` still varlena text on the heap; `xid8` above
      `math.MaxInt64` unrepresentable in the signed-`int64` Datum;
      `interval`/`jsonb`/`bpchar` arms still missing). Design
      `0119-0006-copy-binary-oid-family.md` + README row.

      **55th slice (2026-08-13): binary `COPY` of `interval`, both directions.**
      A stored interval is a `KindInterval` Datum matching none of the default's
      `Kind` cases, so encode shipped the interval's TEXT (21 bytes for `'1 mon
      2 days 03:00:00'`) where `interval_send` is `pq_sendint64(time)` +
      `pq_sendint32(day)` + `pq_sendint32(month)` — a fixed 16; decode handed the
      16 bytes back as a raw-bytes string Datum, putting an interval column back
      in the lexicographic world the heap codec's own `interval` arm was written
      to escape. The heap has stored PG's `{time,day,month}` at exactly those
      offsets all along, so the wire differs ONLY in byte order — and the fields
      are now taken from a shared `pgIntervalFieldsFromDatum` (third repetition
      of the `pgFloatFromDatum`/`pgUnsignedIDFromDatum` extraction), carrying the
      `KindString` bare-literal entry point and its 22007 with it. **The finding
      is that this time the third twin was already right:** the same
      `…AgreesWithHeapEncode` pin that caught the 53rd's `float` spelling bug and
      the 54th's halved `xid8` came back clean here (heap width/offsets,
      `physicalPGTypeAlign` = 8, `pgoutput.go` already correct) — because
      `interval` got its fixed-width layout in a dedicated slice that fixed all
      three twins at once. A clean pin is now EVIDENCE, not an assumption.
      Item stays UNCHECKED (standing slice-by-slice cluster). 6 fail-when-broken
      guards, all red at HEAD; oracle E2E byte-identical in both directions
      (203 bytes, 9 values). 1 ledger item resolved in place, 2 filed
      (`AdjustIntervalForTypmod` — this arm is its first consumer, same blocker
      as the `time`/`timetz` rows; plus a NON-defect row recording that
      `interval_recv` has no range check upstream either). Design
      `0119-0006-copy-binary-interval.md` + README row. Next candidates:
      `jsonb` (leading version byte), `bpchar`.

      **56th slice (2026-08-13): binary `COPY` of `jsonb`, both directions.**
      goopg carries `json`/`jsonb` as a `KindString` Datum holding the JSON text,
      so both halves fell through to the default's `KindString` case and were
      wrong by exactly ONE BYTE at each end: `jsonb_send` (`jsonb.c:124`) is
      `pq_sendint8(version=1)` + `pq_sendtext(JsonbToCString(...))`, so encode
      omitted the version byte and decode failed to strip it. **The pair is
      symmetric, which is why it survived** — goopg↔goopg round-trips perfectly,
      so only a real PG exposes it; proven on the oracle by stripping the version
      byte from a PG-authored stream and feeding it back
      (`ERROR: unsupported jsonb version number 123`, 123 = `0x7b` = `{`), and in
      the other direction a PG stream landed in a goopg column as `\x01{...}`,
      text that is no longer valid JSON. The decode arm also runs
      `jsonb_from_cstring`'s PARSE (22P02) rather than poisoning the column.
      `json` deliberately gets NO arm — `json_send` IS `textsend` — pinned so a
      later edit cannot give it the version byte too. **The finding is a LIMIT,
      not a clean result:** the `…AgreesWithHeapEncode` pin passes only because
      both twins are wrong together — goopg's heap `jsonb` is varlena TEXT where
      upstream's is a `JsonbContainer`/`JEntry` tree, so the 55th slice's rule of
      thumb was right about WHERE the adjacent defect is and wrong only about its
      SIZE. Item stays UNCHECKED (standing slice-by-slice cluster). 7 guards,
      5 red at HEAD (the 2 that pass are the round-trip pins — the symmetric-bug
      signature); oracle E2E byte-identical `TO` plus identical cross-ingest both
      ways. 1 ledger item resolved in place, 2 filed (heap JEntry-tree storage;
      `jsonb` input canonicalisation). Design `0119-0006-copy-binary-jsonb.md` +
      README row. `bpchar` is the last type in this chain, and its remaining gap
      is `bpchar_recv`'s blank padding to the typmod — so it should land together
      with the `copyBinaryToDatum` typmod widening the three `Adjust*ForTypmod`
      rows are blocked on, collapsing four ledger rows into one slice.

      **57th slice (2026-08-13): a `bpchar` value loses its declared width at
      every render boundary.** The 56th's resume point was refuted in BOTH
      halves before code was written. It read `bpcharsend` IS `textsend` as
      "the bytes are accidentally right" — but `textsend` ships the STORED
      image, and upstream stores a `bpchar` blank-padded where goopg stores it
      trimmed, so a `char(10)` holding `'ab'` was a **2-byte** binary field
      where PG writes **10**: the defect was on the ENCODE side the row had
      cleared. And no `copyBinaryToDatum` signature widening was needed — it
      already takes a `catalog.Type` whose `Args` IS the typmod, passed from
      `cols[i].Type` all along (the three `Adjust*ForTypmod` rows are corrected
      in place: they are blocked on the unported FUNCTIONS, not on plumbing).
      **The defect was not COPY-local either:** the same missing padding
      appeared at FOUR boundaries — the `SELECT` DataRow, `COPY … TO` in text,
      CSV and binary, and the pgoutput change message — now all served by one
      shared `catalog.PadBpchar`, sited on the package that owns `Type` because
      `internal/executor` and `internal/wal` both need it and neither may
      import the other. It survived because the two natural ways to eyeball a
      `bpchar` in `psql` hide it: `length()` uses `bcTruelen` and a `||`
      operand goes through the rtrimming `bpchar`→`text` cast — a pre-existing
      `dispatch.go` comment had drawn exactly that wrong conclusion, since
      `bpcharout` is a bare `TextDatumGetCString` that trims nothing. The
      multibyte probe found a SECOND divergence one layer up:
      `coerceTextLikeDatum` measured the declared length in BYTES, so `'あい'`
      into a `char(5)` was a spurious 22001 where PG accepts it at 9 bytes.
      The decode half deliberately gets NO arm (padding there would make one
      column two widths depending on whether it was `INSERT`ed or `COPY`ed),
      pinned by a test that says so. Item stays UNCHECKED (standing
      slice-by-slice cluster). 6 guards mutation-checked three ways (30 / 10 /
      1 failing sub-tests); 1 pre-existing test corrected because it had
      encoded the bug; E2E byte-identical to PG 18.3 on all 8 `cmp`
      comparisons. `TestPort_RegressSuite` PASS (Hard-won Rule #5), UNITS PASS,
      tpch-spotcheck PASS (Q12=2/Q13=35), TPC-DS SF0.5 PASS=95 MISMATCH=0
      CKMISMATCH=0. 1 ledger row resolved, 3 filed (`octet_length` still reads
      the trimmed image; bare `bpchar` still treated as `char(1)`; the heap
      image stays trimmed). Design `0119-0006-bpchar-declared-width.md` +
      README row.
      **58th slice (2026-08-13): a bare `bpchar` is unbounded, and its blanks
      are data.** Second of the three rows the 57th filed. Where that slice
      restored a width goopg failed to RENDER, this one removes one goopg
      INVENTED: `coerceTextLikeDatum`'s `n := 1` default held every `Args`-less
      char-family type to `character(1)`, so `INSERT INTO t(c bpchar) VALUES
      ('abc')` was a spurious 22001. The implicit length of 1 belongs to the
      GRAMMAR, not the type — upstream reduces bare `char`/`character` to bpchar
      with typmod 1 (which `parseColumnType` already mirrors), while `bpchar`
      spelled directly carries typmod −1 and `bpchar_input`'s `atttypmod <
      VARHDRSZ` arm sets `maxlen` to the value's own length. Measured:
      `atttypmod` −1 / 5 / 5 for `bpchar` / `char` / `character`. The resume
      point held but was **incomplete**: the same arm also TRIMS, and a
      width-carrying `bpchar` may be stored trimmed only because the render
      boundaries re-pad it from `Args[0]`. An unbounded one has no width to
      re-pad FROM, so trimming destroys the blanks instead of deferring them
      (PG: `octet_length` 4 for a `bpchar` holding `'ab  '`, 6 for a `char(6)`
      holding the same) — unbounded values are now stored verbatim, and the
      trimmed convention is untouched for everything else. The row's stated
      precondition was MEASURED, not assumed: the heap reload does rename OID
      1042 to `bpchar`, but `pgTypeArgsFromTypmod` decodes the typmod back into
      `Args`, so empty `Args` can only mean typmod −1 and the gate is sound.
      Sibling audit cleared `PadBpchar`, the `expr.go` cast path (guarded by
      `Typmod > 0`) and the parser, and caught one that was NOT clear:
      `validateTypedLen`, `pg_input_error_info`'s private copy of the rule,
      still counted BYTES after the 57th moved the codec path to runes. Item
      stays UNCHECKED (standing slice-by-slice cluster). 2 guards
      mutation-checked (4 / 3 failing assertions on the pre-fix source); E2E
      `COPY` byte-identical to PG 18.3. `TestPort_RegressSuite` PASS (Hard-won
      Rule #5), UNITS PASS, tpch-spotcheck PASS (Q12=2/Q13=35). 2 ledger rows
      resolved, 2 filed (`pg_input_error_info` returns 0 rows where PG returns
      one all-NULL row; `validateTypedLen` matches its type by text prefix).
      Design `0119-0006-bare-bpchar-unbounded-typmod.md` + README row.
      **59th slice (2026-08-13): `pg_input_error_info` returns ONE all-NULL row
      for valid input, not zero.** The deferral the 58th slice filed in its own
      ledger row's `deferred` column. `Next()` returned `nil, EOF` on the valid
      path where upstream `pg_input_error_info` (`misc.c:731-733`) memsets
      `isnull[0..3]` true and `heap_form_tuple`s one tuple unconditionally — the
      SRF never returns zero rows, so any caller that counts rows rather than
      testing `message IS NULL` saw the opposite answer from PG. Fixed in
      `operators_pg_input_error_info.go` (the `if message == ""` branch now emits
      the 4-NULL row); the `enum_validation_test.go` "valid → 0 rows" arm was
      rewritten to assert the row. The pg_regress int2/int4/varchar consumers are
      unaffected — each of their `SELECT * FROM pg_input_error_info(...)` calls
      uses an INVALID input, so no expected file changed. Item stays UNCHECKED
      (standing slice-by-slice cluster). Gates: `go test ./internal/executor/`
      PASS. 1 ledger row resolved; 0 filed (the NULL-argument path is left
      unchanged and recorded as UNMEASURED in the resolved row's deferred column,
      not as a new deferral).
      **60th slice (2026-08-13): `validateTypedLen` resolves its type instead of
      prefix-matching it.** The deferral the 58th slice filed in its other ledger
      row's `deferred` column (row 1318): a schema-qualified spelling
      (`pg_catalog.varchar(5)`), a domain over `varchar(5)`, or whitespace
      between the name and `(` (`varchar (5)`) silently validated NOTHING. Fixed
      in `operators_pg_input_error_info.go`: `validateTypedLen` now parses the
      type text (`parseTypeNameAndTypmod` → `stripTypeSchema` drops the schema,
      the `(N)` typmod is parsed with whitespace tolerated), resolves it through
      `catalog.TypeNameToOID` plus a `LookupDomain` follow-to-base
      (`resolveLengthType`), and drives the width check off the resolved
      `catalog.Type` through `coerceTextLikeDatum` — the sibling the 57th/58th
      slices already converted to runes, so the two now share ONE rule (Hard-won
      Rule #2). The `char`/`character`→`character(1)` and bare-`bpchar`→unbounded
      grammar defaults are preserved. Item stays UNCHECKED (standing
      slice-by-slice cluster). Gates: `go test ./internal/executor/` PASS;
      `go build ./...` clean. 1 ledger row resolved (1318); 0 filed.
      **61st slice (2026-08-13): the serial-family spellings store the
      fixed-width int image, not varlena text.** The deferral the 52nd slice
      filed (ledger row 1292): `smallserial`/`serial2`/`serial4`/`serial8` are
      the sequence-backed spellings of int2/int4/int8, but `codec.go`'s heap
      arms listed only `serial`/`bigserial`, so those four spellings fell
      through to the varlena default and stored their value as TEXT where PG
      stores the 2/4/8-byte int image (feedback_pg_faithful_binary_over_text,
      inverted). Added the missing spellings to the FOUR codec.go dispatch
      sites that must agree with each other (Hard-won Rule #2): `encodeValuePG`
      (heap encode), `decodePhysicalPGValueMctx` (heap decode),
      `physicalPGTypeAlign`, and — the one a first pass missed —
      `pgPhysicalTypeIsVarlena`, whose own comment warns the PG18 nocachegetattr
      attcacheoff walker trips if it disagrees with encodeValuePG. `copy_binary.go`'s
      int2/int4/int8 arms (both directions) gained the WHOLE serial family: it
      was missing `serial`/`bigserial` too, so a `serial` column shipped TEXT
      under FORMAT binary. The text-COPY path needed no edit — its KindString
      default already hands the int arms a string datum they coerce, the same
      route `int2`/`smallint` take. New `internal/executor/codec_serial_spellings_test.go`
      (6 tests: fixed-width encode == canonical spelling, KindString coercion,
      align, not-varlena, heap round-trip, binary-COPY twin + round-trip). Item
      stays UNCHECKED (standing slice-by-slice cluster). Gates: `go test
      ./internal/executor/` PASS; UNITS pre-commit PASS; `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=35). 1 ledger row resolved (1292); 0 filed.
      **62nd slice (2026-08-13): a `time(N)`/`timetz(N)` column ROUNDS at INPUT,
      not truncates at OUTPUT.** Closes the three `AdjustTimeForTypmod` deferral
      rows the 49th/51st/55th slices filed. goopg stored full microseconds and
      TRUNCATED the fractional seconds at three output boundaries in three
      hand-maintained copies — the `::time(N)`/`::timetz(N)` cast arm (`expr.go`),
      `copyTimeOfDayMicros` (COPY text/CSV) and `appendTimeText` (SELECT DataRow) —
      where upstream `time_in`/`timetz_in` ROUND half-away-from-zero at INPUT via
      `AdjustTimeForTypmod` (date.c:1710). Measured: `'23:59:59.999999'` into
      `time(2)` is `24:00:00` on PG 18.3 and was `23:59:59.99` on goopg — a stored
      value, not just display. One `internal/pgdatetime/adjust_typmod.go`
      `AdjustTimeForTypmod` (literal port of the TimeScales/TimeOffsets tables) +
      one `roundTimeDatumToPrecision` wrapper (`codec.go`, over the hour-24
      `pgTimeMicros`/`pgTimeFromMicros` pair the 50th slice established), applied
      at every input site that holds the precision — the cast arm,
      `copyTextToDatum`, `copyBinaryToDatum`, and `coerceRowForConstraintChecks`
      (INSERT/UPDATE, which had NO `time` case so the column precision never
      reached the value) — plus an `encodeValuePG` storage-choke safety net for
      the DEFAULT/generated path the `!insertMissing` filter skips (rounding is
      idempotent). The three output truncators are deleted so the stored value
      renders verbatim. Gates: `internal/pgdatetime`+`internal/executor`+
      `internal/server` tests PASS; `go build ./...` clean; UNITS pre-commit
      PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35). 2 ledger rows
      resolved (1286, 1289); 0 filed (the interval-column-typmod and probe-
      extraction residuals are already-ledgered). Design
      `0119-0006-time-typmod-rounding.md` + README row.
      **63rd slice (2026-08-13): an `interval(N)` / `interval <field> [TO <lo>]
      [(p)]` column ROUNDS/zeroes at INPUT via `AdjustIntervalForTypmod`.**
      Closes the `AdjustIntervalForTypmod` column-typmod deferral row the 55th
      slice filed. The interval typmod is a packed range+precision value
      (`INTERVAL_TYPMOD`), so it had to become reachable before it could be
      applied: `parseColumnType` did not parse `interval year to month` at all,
      and the `interval(N)` it did parse was dropped to `atttypmod = -1` on disk.
      Landed: `internal/pgdatetime/interval_typmod.go` `AdjustIntervalForTypmod`
      (literal port of the `INTERVAL_MASK(field)` range switch + `IntervalScales`/
      `IntervalOffsets` rounding + ±infinity no-op); `parseIntervalColumnQualifier`
      parses the column-position qualifier carrying the FULL range mask (`year to
      month` keeps `YEAR|MONTH`, unlike the low-field collapse the internal-only
      cast path uses); `pgAttTypmod`/`pgTypeArgsFromTypmod` (1186) round-trip the
      typmod through the catalog; `formatTypeOID` (via new `formatIntervalTypmod`)
      renders `interval year to month`/`interval second(2)`/`interval(2)` so
      pg_dump keeps the spelling; and a `roundIntervalDatumToTypmod` wrapper
      (parsing via the shared `pgIntervalFieldsFromDatum`/`ParseIntervalBody`
      tokenizer, NOT `evalCast`'s limited `<n> <unit>` arm) wired into
      `coerceRowForConstraintChecks`, `copyTextToDatum`, `copyBinaryToDatum` and
      `encodeValuePG`. Gates: `internal/pgdatetime`+`internal/parser`+
      `internal/executor`+`internal/initdb` tests PASS; `go build ./...` + `go
      vet` clean; UNITS pre-commit PASS; `scripts/tpch-spotcheck.sh` PASS
      (Q12=2/Q13=35); pgbench smoke via the hook. 1 ledger row resolved (1302).
      Design `0119-0006-interval-typmod-at-input.md` + README row.
      **65th slice (2026-08-14): `octet_length()`/`bit_length()` on a `bpchar`
      answer from the declared width, not the trimmed heap image.** Closes the
      deferral the 57th slice filed (ledger row 1314). The four render
      boundaries the 57th closed already held the column's `catalog.Type`, but
      these two are EXPRESSION evaluations — the argument's declared type isn't
      threaded to the builtin, so `octet_length('ab'::char(10))` answered 2
      where PG 18.3 says 10, and `bit_length` had NO case at all (fell to the
      `evalStoredRoutineFuncCall` fallback → "function does not exist").
      New `declaredBpcharTypmod(e planner.Expr)` (expr.go) recovers the width
      from a `ColumnRef`'s `Type.Args[0]` (array guarded), a `CastExpr`'s
      `Typmod`, or the first arg of `coalesce`/`greatest`/`least`/`nullif`
      (missing/`<=0` → 1 for bare `char`); `octet_length` then answers
      `len(catalog.PadBpchar(...))` (upstream `bpcharoctetlen` measures the
      PADDED stored image — `'あ'::char(5)` = 7). **`bit_length` is the
      OPPOSITE**: it resolves through the implicit bpchar→text cast, which
      trims trailing blanks (`bpchartotext`), so `bit_length('ab'::char(10))` =
      16, NOT 80 — the row's "2 where PG says 10 for both" was wrong for
      bit_length. New `bit_length` case (bytea = `8*len(Bytes)`, string =
      `8*len(String)`, else 42883); both functions gain the `length` sibling's
      non-string `42883` guard with PG-exact messages. 19 oracle-pinned cells
      (incl. bare `''::char` 1/0, multibyte, `::text` cast-override twins,
      column + coalesce sources, and both 42883 guards), E2E-verified on a live
      goopg (5533) vs PG 18.3. Item stays UNCHECKED (standing slice-by-slice
      cluster). Gates: `go build ./internal/...` clean; UNITS pre-commit run —
      EVERY package PASS except the pre-existing foreign
      `TestRegIdentifierInputResolvesRegtypeName` (untracked reg_identifier WIP
      from a prior loop; `catalog.TypeNameToOID`'s `default: return OIDText`
      fallback defeats its regtypein 42704 miss-path — unrelated to this slice;
      the committed tree builds/tests clean without it). 1 ledger row resolved
      (1314). Design `0119-0006-bpchar-octet-bit-length.md` + README row.
      **66th slice (2026-08-14): the reg* family and `cid` store as 4-byte OIDs,
      and the regtypein 42704 miss-path fires.** Lands the untracked
      `reg_identifier.go` WIP the 65th slice's gate note names as its only
      failure (the `TypeNameToOID` OIDText fallback defeated the `oid != 0`
      test, so `regIdentifierInput("no_such_type", "regtype")` resolved to
      text's OID 25 instead of raising 42704). The heap codec's `"oid",
      "regproc"` arms now cover `regprocedure`/`regclass`/`regtype`/`cid` (4-byte
      LE, typalign 'i' — ledger row 1300's source); binary COPY shares the arm
      (all their send/recv ARE oidsend/oidrecv upstream); the third physical
      decoder `pgoDecodePhysicalValue` (internal/wal/pgoutput.go) gains the
      same arm — before it read the 4-byte image through the varlena fall-through
      (silent garbage), the sibling-pair gap this slice pins with
      `pgoutput_reg_identifier_test.go`; and `coerceRowForConstraintChecks`
      resolves a bare quoted name (`INSERT INTO t(r regclass) VALUES
      ('mytable')`) to its OID via `regIdentifierInput` (regclassin/regtypein/
      regprocin semantics, 42P01/42704/42883 on a miss) instead of handing the
      numeric oid arm a name to misparse. regrole/regcollation stay varlena
      (no name-resolution seam — see the 54th-slice ledger row). Gates:
      `RALPH_PRECOMMIT_SCOPE=units` PASS (the regtype blocker is gone),
      `TestPort_RegressSuite` PASS (346 s), `scripts/tpch-spotcheck.sh` PASS
      (Q12=2, Q13=35). Design: `docs/design/0119-0006-reg-identifier-family-storage.md`
      + README row.
      **67th slice (2026-08-14): `regrole`/`regcollation` store as 4-byte OIDs —
      the object-identifier family is now complete.** The two members the 66th
      slice excluded for lack of a role/collation name→OID seam now have one:
      `regIdentifierInput` gains `regrole` (via `InMemory.RoleOID`; a qualified
      name is 42602 invalid name syntax, a miss 42704 `role "%s" does not
      exist`) and `regcollation` (via the new exported `InMemory.CollationOIDByName`,
      builtin-then-user REUSING `builtinCollationOIDByName` + `UserCollationOIDByName`
      — no third map copy; qualified names via `FindCollation`; a miss 42704
      `collation "%s" for encoding "UTF8" does not exist`), routed from
      `coerceRowForConstraintChecks`. All four physical-codec twins move to the
      4-byte layout — heap codec (`codec.go`), binary COPY (`copy_binary.go`),
      `pgoDecodePhysicalValue` (`internal/wal/pgoutput.go`). Also closed the
      family-wide `parseDashOrOid` latent gap the 66th slice left (`'-'` →
      InvalidOid 0, pure-digit → numeric OID, uint32 overflow → 22003), and
      `appendTypedCellText` renders regrole/regcollation as names
      (regroleout/regcollationout; OID 0 → "-", dangling → numeric) so SELECT
      output stays `postgres`/`C` instead of regressing to raw OIDs. Ledger row
      1302 resolved; a new row filed for the pre-existing family-wide TEXT/CSV
      COPY numeric-OID gap (the 66th slice shipped the same for the other four
      members — lossless cross-engine, a catalog-threading refactor). Gates:
      `go build ./internal/...` clean; package suites PASS; pre-commit units
      PASS; `TestPort_RegressSuite` PASS (245 s); `scripts/tpch-spotcheck.sh`
      PASS (Q12=2, Q13=35). Design:
      `docs/design/0119-0006-regrole-regcollation-4byte-storage.md` + README row.
      — blocked on logical decoding (tracks the logical-replication milestone / D-004).
      **68th slice (2026-08-14): TEXT/CSV COPY of a `reg*` column renders its
      NAME, not its numeric OID — the family-wide gap ledger row 1303 named.**
      `datumToCopyText` (shared by TEXT `EncodeCopyTextRow` and CSV
      `EncodeCopyCsvRow`) gains a reg* guard routing any
      `regproc`/`regprocedure`/`regclass`/`regtype`/`regrole`/`regcollation`
      KindInt datum through the new exported `executor.RegOut` — the SAME
      OID→name renderer `appendTypedCellText`'s six reg* cases now collapse
      onto (one call; no duplication, Hard-won Rule #2), so COPY TO cannot
      drift from SELECT and OID 0 → "-" for every family (fixing the pre-68th
      SELECT regclass case that matched an OID-0 information_schema virtual
      table for a nondeterministic name). The renderers gain `(cat
      catalog.Catalog, qualify bool)`, threaded from `RunCopyTo` with `qualify
      = !regObjectSchemaVisible(ctx, "public")` (the server's
      `!publicSchemaVisible(getSetting)`). COPY FROM routes the decoded row
      through `coerceRowForConstraintChecks` at `insertSourceRow` with a
      reg*-only include filter (`isRegIdentifierTypeName` — the exact 6-name
      family, numeric-only `oid`/`cid` excluded so the wider encode/align
      lists are untouched), so a name field resolves to its OID via the 67th
      slice's choke point with the family's OWN SQLSTATE unwrapped (42P01
      regclass / 42704 regrole+collation — NOT the 22P04 wrap `copyTextToDatum`
      would add), and `-`/pure-digit fields stay numeric OIDs via the 66th
      slice's `parseDashOrOid`. New tests: `internal/executor/reg_copy_test.go`
      (TO renders names across TEXT+CSV for all six incl. pg_class 1259 / role
      alice / collation mycoll; OID 0 → "-"; KindString passthrough; FROM
      resolves name/-/numeric; the include filter leaves a non-reg* column
      untouched; the family predicate) + `internal/server/reg_copy_sibling_test.go`
      (SELECT vs COPY byte-agreement at qualify false AND true). Ledger row
      1303 resolved; 4 new rows filed (reg*out schema-qualification + quoting,
      regclassout TOAST-relation name, array-of-reg* COPY FROM, the general
      COPY-FROM 22P04 wrap). Gates: package suites PASS; pre-commit units
      PASS; `TestPort_RegressSuite` PASS (340 s); `scripts/tpch-spotcheck.sh`
      PASS (Q12=2, Q13=35). Design:
      `docs/design/0119-0006-reg-copy-text-name-rendering.md` + README row.
      **69th slice (2026-08-14): `RegOut` schema-qualifies and quotes the names
      the reg*out family emits — ledger row 1304 resolved.** New
      `quoteQualifiedIdentifier` (expr.go, the ruleutils.c
      `quote_qualified_identifier` port over the shared `pgQuoteIdent` guard)
      + `regOutQualified` (reg_identifier.go) + `InMemory.RoleNameAtOID`
      (catalog.go; distinguishes a real role from a dangling OID, since
      `RoleNameForOID` renders both numerically). The RegOut
      regclass/regproc/regrole/regcollation arms run resolved names through
      the shared rule: a name NOT visible on the session's effective
      search_path renders `schema.name`, always quote_identifier'd; pg_catalog
      NEVER qualifies (implicitly visible); a builtin proc never qualifies;
      regrole quote_identifiers the role name but a dangling OID falls to the
      unquoted `%u`; regcollation quote_identifiers every name (`C` →
      `"C"`, `default` → `"default"`). Measured against PG 18.3: `public`
      itself is UNQUOTED (`public."My Table"`), 1259 stays `pg_class` at
      qualify=true. COPY computes qualify as
      `!regObjectSchemaVisible(ctx, "public")`, SELECT as
      `!publicSchemaVisible(getSetting)`; `TestRegCopyAndSelectSiblingQualifyAgree`
      strengthened to exercise a REAL user table so a disagreement between the
      two computations is observable. regtype keeps its own format_type_be
      path; regprocedure keeps its bare signature (deferred). New tests:
      `internal/executor/reg_qualify_test.go` (qualify=true qualification,
      pg_catalog-never-qualifies, identifier quoting, dangling-role numeric).
      Ledger: row 1304 resolved; 3 new rows filed (regprocedure format_procedure
      signature qualification, regcollation user-schema hardcoded "public",
      mixed-case role-name catalog folding). Design:
      `docs/design/0119-0006-regout-schema-qualification.md` + README row.
      **70th slice (2026-08-14): regcollation qualifies with the collation's
      ACTUAL schema — ledger row 1339 resolved.** The regcollation arm had
      hardcoded `quoteQualifiedIdentifier("public", n)`, right for a
      default-session (public) creation schema and wrong for any non-public
      `CREATE COLLATION` schema. It now routes through the family's shared
      `regOutQualified(im.SchemaNameForOID(uc.NamespaceOID), n, qualify)` —
      `SchemaNameForOID` is the `get_namespace_name(collnamespace)` port, and
      `regOutQualified` also closes the pg_catalog edge the literal could not
      express (a collation created in pg_catalog is always visible → bare name,
      where the old code emitted `public.<name>`); qualify=false behavior is
      unchanged. Measured against a throwaway PG 18.3 oracle (port 5599):
      `search_path=''` renders `ragout70.mycoll` / `ragout70."My Other Coll"`,
      `search_path=ragout70` renders bare `mycoll`. Tests:
      `TestRegCollationQualifiesWithActualSchema` (reg_qualify_test.go:
      non-public plain + quoted-name collations, qualify=false bare, public
      still `public.mycoll`) + `TestRegCopyAndSelectSiblingQualifyAgree`
      extended with the non-public collation. Design:
      `docs/design/0119-0006-regcollation-actual-schema-qualifier.md` + README
      row (`0119-0006av`). Gates: package suites + pre-commit units +
      `TestPort_RegressSuite` + `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35)
      all PASS.
      **71st slice (2026-08-14): regprocedure qualifies only the routine NAME —
      ledger row 1338 resolved.** The regprocedure arm returned the BARE
      signature (`my_udf()` via `catalog.RegprocedureName`) where upstream
      `regprocedureout` → `format_procedure_extended` (regproc.c:326)
      schema-qualifies the routine NAME when it is off the session's effective
      search_path and quote_identifiers the name in BOTH arms; the `format_type_be`
      arglist is appended UNQUOTED. New `catalog.RegprocedureNameParts` resolves
      an OID to the `(schema, name, arglist)` halves (refactored out of
      `RegprocedureNameAndSchema`); the RegOut regprocedure arm routes them
      through the family's shared `regOutQualified(schema, name, qualify)` and
      appends the unquoted arglist. The `::regprocedure` cast path (expr.go) —
      the sibling renderer — switched from the old `schema + "." + sig`
      whole-signature prefix to the same form, fixing the on-path mixed-case
      case too (`"MyFunc"(integer)` not `MyFunc(integer)`) and keeping the two
      renderers byte-identical. Measured against a throwaway PG 18.3 oracle
      (port 5599): default path renders `udf71(integer,text)` /
      `"MyFunc71"(integer)` / `ragout71.other_func()` / `ragout71."Quoted
      Other"(integer)`, `search_path=''` renders `public.udf71(integer,text)` /
      `public."MyFunc71"(integer)`, `search_path=ragout71` renders bare
      `other_func()` / `"Quoted Other"(integer)`, builtin `int4out(integer)`
      never qualifies. Tests: `TestRegOutRegprocedureQualifiesNameOnly`,
      `TestRegprocedureCastQuotesRoutineName`, sibling
      `TestRegCopyAndSelectSiblingQualifyAgree` extended with a user routine.
      Ledger: row 1338 resolved; 2 NEW rows filed (regprocin's name→OID input
      still ToLower's quoted identifiers; the arglist's `format_type_be` does
      not schema-qualify non-visible arg types). Design:
      `docs/design/0119-0006-regprocedure-qualified-name.md` + README row
      (`0119-0006aw`). Gates: package suites + pre-commit units +
      `TestPort_RegressSuite` (242.3 s) + `scripts/tpch-spotcheck.sh`
      (Q12=2, Q13=35) all PASS. Remaining open reg* deferrals: mixed-case
      role-name catalog folding (row 1340), regprocin quoted-identifier input,
      format_type_be arglist qualification.

      **72nd slice (2026-08-14): regprocin quoted-identifier INPUT — ledger
      row 1341 resolved.** The reg* name→OID input half now honors
      double-quoted identifiers exactly as upstream
      `stringToQualifiedNameList` → `SplitIdentifierString` (varlena.c:3581)
      does: a `"…"` segment keeps its case with `""`→`"` collapse, an unquoted
      segment is downcased, whitespace around segments is skipped, `.` inside
      quotes is not a separator, and a syntax error (mismatched quote, empty
      segment) raises 42602. Before, every reg* input arm ran the whole
      candidate through `strings.ToLower` + a dumb first-dot
      `splitQualifiedTable`, so `'"MyFunc"'::regproc` reached LookupByName with
      literal quotes → 42883 (PG 18.3 resolves it). New shared parser
      `splitRegIdentifiers`/`splitRegQualifiedName` in reg_identifier.go feeds
      every arm of `regIdentifierInput` (regclass/regtype/regproc/regrole/
      regcollation) AND the expr.go siblings — `::regproc`/`::regprocedure`
      cast, `::regclass` cast, `regclass()` function-call, `pg_get_functiondef`
      name fallback — the input counterpart of the 69th/70th/71st slices'
      quote-emission (sibling renderers must agree). Two faithful addenda found
      while implementing: `regprocedureNamePart` strips the `(…)` arg list
      (parseNameAndArgTypes' leading scan) so `'"MyFunc"(integer)'::regprocedure`
      does not regress to 42602; and the parser's downcasting now makes
      `'C'::regcollation` FAIL with 42704 exactly like PG 18.3 (only `'"C"'`
      resolves to 950 — the collation store is case-sensitive), updating the
      old divergent test. Miss messages match PG: regclass/regrole/regcollation/
      regtype print the STRIPPED parsed name (NameListToString), regproc/
      regprocedure keep the RAW input. Tests: new
      `internal/executor/reg_input_quoted_test.go` (quoted mixed-case routine
      on both cast paths, dotted quoted schema, quote-quote collapse,
      family siblings, 42602 syntax errors on both paths, coercion route,
      stripped-vs-raw miss messages). Ledger: row 1341 resolved. Design:
      `docs/design/0119-0006-reg-input-quoted-identifiers.md` + README row
      (`0119-0006ax`). Gates: package suites + pre-commit units +
      `TestPort_RegressSuite` (237.3 s on the confirming run) +
      `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35). Gate note: the FIRST
      TestPort_RegressSuite attempt FAILed at its 600 s timeout — the known
      intermittent "suite wedge" (documented in regress_wedge_probe_test.go's
      header) hit the `returning` case: `CREATE FUNCTION … BEGIN ATOMIC
      RETURNING` parked in `walLogCreateRoutine → syncRoutineToCatalogHeap →
      mirrorProcCatalogFiles → … → (*Pool).Pin → pinLoad` waiting on the buffer
      pool RWMutex write lock while the WAL checkpointer waited on the same
      pool's RLock in `flushBatch` (FlushAllPaced) — so NO checkpoint could
      run, WAL accumulated to 7.4 GB, and the subsequent cluster-restart's
      crash recovery could not finish inside the 20 s start timeout, turning
      one wedged case into a whole-suite FAIL. Completely disjoint from this
      slice (storage/WAL buffer-pool lock path; the slice touched only reg*
      input arms). Confirmed environmental: the clean re-run PASSed in 237.3 s
      (vs 242.3 s last slice), and the regproc regress case PASSes standalone
      in 6.2 s. Bundle preserved at tmp/regress-wedge/returning/ (goroutine
      dump, pg_stat_activity, server-log-tail). Remaining open reg* deferrals:
      mixed-case role-name catalog folding (row 1340) plus the six the 73rd
      slice filed below (bare arg-type schema resolution, quoted-name case loss,
      catalog builtin-array alias gap, ArgTypeDisplayAlias switch gaps,
      empty-schema visibility proxy, regproc/regprocedure INPUT DB-scoping bug).

      **73rd slice (2026-08-14): regprocedure arglist schema-qualifies
      non-visible arg types — ledger row 1342 resolved.** The arglist now
      reproduces `format_type_be`'s per-arg qualification:
      `format_procedure_extended` (regproc.c:326) passes each arg type through
      `format_type_be` (format_type.c:314), which emits `schema.typename` when
      the type's namespace is off the session's effective search_path and the
      bare alias otherwise. goopg captured the missing half at CREATE — new
      `Routine.ArgTypeSchemas` (parallel to `ArgTypes`,
      internal/catalog/routines.go) records each arg type's EXPLICIT schema via
      `argTypeSchema` returning the parser's `ColumnType.Schema` verbatim
      (operators_ddl.go, both CREATE FUNCTION and CREATE PROCEDURE), and rides
      the existing proargdefaults JSON round-trip so pre-73rd data dirs reload
      with nil → "" → bare (backward compatible). The render half:
      `catalog.RegprocedureNameParts` now returns `[]RegprocArg{Name, Schema}`
      per arg (builtin path stamps `pg_catalog`; user path reads
      `ArgTypeSchemas[i]` nil-defensively), and the executor-side
      `regprocedureArglist` (reg_identifier.go) aliases builtin/pg_catalog args
      through the exported `catalog.ArgTypeDisplayAlias`, schema-qualifies a
      non-visible user arg via `quoteQualifiedIdentifier` with the `[]` array
      suffix split/re-appended (`offpath."mytype[]"` never happens; a user-path
      builtin array aliases `integer[]`), and leaves a BARE-name user arg bare
      (owner schema unresolvable — deferral). The session visibility predicate
      threads as a variadic `visible` param through `RegOutArgVisible` /
      `appendTypedCellText` (SELECT simple-query) / `EncodeCopyTextRow` /
      `EncodeCopyCsvRow` (COPY TO) and the `::regprocedure` cast sibling in
      expr.go — all four paths agree (Hard-won Rule #2). Measured against a
      fresh goopg cluster + PG 18.3 oracle: `f_offarg(offpath.mytype)` (was
      `f_offarg(mytype)`), `f_offarr(offpath.mytype[])`, `f_offrow(offpath.ct)`,
      `f_onarg(onpath.mytype)` (name + off-path arg both qualify),
      `f_builtin(integer)` stays bare, a user type NAMED `int` quotes like PG
      (`offpath."int"`), and the cast path additionally qualifies the NAME
      (`offpath.f_offboth(offpath.mytype)`) where the wire path's name stays bare
      via the documented 69th-slice proxy. One measured limitation filed as a
      deferral: `SET search_path = public, offpath` cannot make the `offpath`
      arg render bare yet (searchPathSchemas' LookupTable existence proxy never
      sees an empty schema). Tests:
      `TestRegOutRegprocedureQualifiesArgTypes` (reg_qualify_test.go),
      `TestRegprocedureCastArgTypesQualify` +
      `TestCreateFunctionCapturesArgTypeSchemas` (regoperator_schema_qualify_test.go),
      sibling `TestRegCopyAndSelectSiblingArgQualifyAgree`
      (reg_copy_sibling_test.go). Ledger: row 1342 resolved; 6 NEW rows filed
      (bare-name arg-type schema resolution, quoted-name case loss at CREATE,
      catalog builtin-array alias gap, ArgTypeDisplayAlias switch gaps,
      empty-schema visibility proxy, regproc/regprocedure INPUT DB-scoping bug).
      Design: `docs/design/0119-0006-regprocedure-argtype-schema-qualify.md` +
      README row. Gates: package suites + pre-commit units +
      `TestPort_RegressSuite` (239.7 s) + `scripts/tpch-spotcheck.sh`
      (Q12=2, Q13=35) all PASS.
      **74th slice (2026-08-14): `ArgTypeDisplayAlias` becomes a faithful
      `format_type_be` port + the catalog bare arglist builder aliases builtin
      arrays — ledger rows 1345 + 1346 resolved.** Two siblings diverged from
      PG 18.3 on the regprocedure arglist. (a) The shared alias table had no
      `varbit → bit varying` arm (the one missing VARBITOID special-case switch
      entry — `bit`/`interval`/`json`/`numeric` are identities, every other
      case was already present) and no keyword-quoting path at all, so the
      single-byte `char` (CHAROID, distinct from bpchar/1042) rendered bare
      `char` where `format_type_be`'s DEFAULT path runs
      `quote_qualified_identifier` and `quote_identifier` wraps the lexer
      keyword → `"char"`. (b) The catalog BARE builder `formatProcedureArglist`
      (behind `RegprocedureName`/`RegprocedureNameAndSchema`) passed the WHOLE
      stored name — baked-in `[]` array suffix included — to the alias, so
      `int[]` found no switch case and rendered `f(int[])` where the executor's
      pg-faithful `regprocedureArglist` already emitted `f(integer[])`: the two
      sibling renderers diverged on a builtin array arg (Hard-won Rule #2).
      Fix: two new arms in `catalog.ArgTypeDisplayAlias` (`varbit → bit
      varying`, `char → "char"`) and a package-local `argListTypeDisplay`
      helper (catalog cannot import executor) that splits a `[]` suffix, aliases
      the ELEMENT, re-appends — mirroring executor's `splitArraySuffix`. The
      executor renderer needed NO change; it picks up the new arms via the
      shared alias. Measured vs a throwaway PG 18.3 oracle (port 5533) on the
      wire path (`oid::regprocedure`): `f_varbit(bit varying)`, `f_char("char")`,
      `f_chararr("char"[])`, `f_intarr(integer[])` now byte-identical; the two
      siblings agree on `integer[],bit varying,"char","char"[],double
      precision[],text`. Two FRESH deferrals filed (rows 1349/1350, out of
      scope): multi-word type names in CREATE FUNCTION args store the LAST word
      (`bit varying`→`varying`, `timestamp with time zone` is a syntax error) —
      so `f_vchar(bit varying)` renders `f_vchar(varying)` where PG keeps
      `bit varying` — and a reg* → text/name/varchar cast on a STRING-LITERAL
      source renders the raw OID (`'f_varbit(varbit)'::regprocedure::text` →
      `131072`); regtype/regrole/regcollation and non-literal sources are
      unaffected. Tests: `TestArgTypeDisplayAliasFormatTypeBePort` +
      `TestRegprocedureName` array/varbit/char cases (catalog),
      `TestRegOutRegprocedureArgTypesVarbitChar` +
      `TestRegprocedureArglistCatalogAndExecutorAgree` (executor).
      Design: `docs/design/0119-0006-argtype-alias-format-type-be-port.md` +
      README row `0119-0006az`. Gates: package suites (catalog 0.063s,
      executor 6.103s, server 55.151s) + pre-commit units +
      `TestPort_RegressSuite` + `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) all
      PASS.
      **75th slice (2026-08-14): regproc/regprocedure NAME→OID INPUT is scoped
      to the connection's database — deferral row 1348 resolved.** The reg*
      INPUT half resolved every routine name through `Routines.LookupByName` with
      NO dbOid, so it always resolved `DefaultDBOid`. The 4e-series routine
      registry (M0122-0007 slice 4e) keys routines by `(dbOid, schema, name)`;
      a LIVE-created routine (registered under its real dbOid) was invisible by
      name from a distinct-dbOid connection — and worse, a same-named routine in
      ANOTHER database resolved THAT routine's OID: a silent cross-dbOid leak
      (`'shared_fn'::regproc` from db2 returned DefaultDBOid's 131072 instead of
      its own 131073). An initdb-reloaded routine (DefaultDBOid) still resolved,
      which hid the bug on default-database connections. Fix: both sibling paths
      (Hard-won Rule #2) thread `catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)`
      into `LookupByName` — `regIdentifierInput`'s regproc/regprocedure arm
      (internal/executor/reg_identifier.go, feeds COPY FROM coercion + constraint
      checks) and expr.go's `::regproc`/`::regprocedure` cast arm (feeds
      `'name'::regproc` in expressions) — mirroring the regclass arm's existing
      connDBOid. Builtins still resolve via the global `LookupBuiltinProc`
      pg_proc index (pg_catalog implicitly visible in every database, matching
      PG). Tests: `TestRegProcInputScopedToConnectionDBOid`,
      `TestRegProcInputSchemaQualifiedScopedToConnectionDBOid`,
      `TestRegProcInputDistinctDBOidMissIsNotDefaultLeak`
      (internal/executor/reg_identifier_dbid_scoping_test.go) — all FAIL pre-fix
      (the first two leaked the wrong OID, the third resolved instead of raising
      42883) and PASS post-fix. Live E2E on a throwaway goopg (5533) +
      byte-identical PG 18.3 oracle (5534): db2's `'shared_fn'::regproc` → 42883
      before its own routine exists, then each database resolves its own OID
      (131072 vs 131073). Design:
      `docs/design/0119-0006-regproc-input-dbid-scoping.md` + README row
      `0119-0006ba`. Gates: package suites + pre-commit units +
      `TestPort_RegressSuite` + `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) all
      PASS.
      **76th slice (2026-08-14): multi-word built-in type names in CREATE
      FUNCTION args are captured faithfully — deferral row 1349 (first half)
      resolved.** `parseArgNameAndType`'s `ident ident` heuristic read the FIRST
      word of a multi-word built-in type as an ARG NAME: `f(bit varying)` stored
      arg name="bit" type="varying" (regprocedure arglist rendered `f(varying)`
      where PG 18.3 renders `f(bit varying)`), `double precision`→`f(precision)`,
      and `timestamp with time zone` — whose continuation `with` is the KwWith
      keyword — was a SYNTAX ERROR in CREATE FUNCTION args. Fix: new
      `isMultiWordTypeStart(nameTok, next Token)` (internal/parser/function.go)
      recognizes a multi-word-type leader (double→precision, character→varying,
      bit→varying, timestamp/time→with|without time zone,
      interval→year|month|day|hour|minute|second) by its NEXT token and rewinds
      `p.idx = save` so `parseColumnType` consumes the whole spelling — the same
      canonical collapse CREATE TABLE columns already used (`bit varying`→`varbit`,
      `double precision`→`float8`, `timestamp with time zone`→`timestamptz`,
      `time with time zone`→`timetz`, `interval year to month`→`interval`+packed
      typmod); the arg gets `Name=""` (bare, unnamed) exactly as if the canonical
      single-word name had been written. Output side needed NO change: the
      executor's `regprocedureArglist` renders the canonical name through the
      shared `catalog.ArgTypeDisplayAlias` (74th slice), mapping varbit→bit
      varying / float8→double precision / timestamptz→timestamp with time zone /
      timetz→time with time zone, so the stored canonical name round-trips to
      the user's SQL spelling byte-identically. Verified live vs a throwaway PG
      18.3 oracle (5534): all seven multi-word CREATE FUNCTIONs succeed and
      `oid::regprocedure` renders byte-identical signatures (`f_vchar(bit
      varying)`, `f_cvarchar(character varying)`, `f_dp(double precision)`,
      `f_ts(timestamp with time zone)`, `f_t(time with time zone)`,
      `f_int(interval year to month)`, named `f_named(a bit, b double
      precision)`); the created functions are callable with matching arg types
      and DROP FUNCTION by the multi-word signature works on both engines.
      Test: `TestParseCreateFunctionMultiWordArgTypes` (parser). Design:
      `docs/design/0119-0006-multiword-arg-type-capture.md` + README row
      `0119-0006bb`. Gates: package suites + pre-commit units +
      `TestPort_RegressSuite` + `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) all
      PASS. Re-filed as row 1351: the arglist still carries only the arg's NAME,
      so a bare `char` arg is indistinguishable from OID-18 `"char"`, and char
      varying/nchar/national character [varying] are not yet accepted as bare
      types.

      **77th slice (2026-08-14): SQL national-char aliases in CREATE FUNCTION
      args + bare-`character` column typmod — deferral row 1351 (first half)
      resolved.** `char varying` / `nchar [varying]` / `national character
      [varying]` / `national char [varying]` — PG's aliases of `character` /
      `character varying` (gram.y `character` nonterminal: `CHARACTER|CHAR_P|NCHAR
      opt_varying`) — are now accepted as bare types in CREATE FUNCTION args.
      `isMultiWordTypeStart` (internal/parser/function.go) treats
      `character`/`char`/`nchar`/`national` as multi-word-type leaders whenever
      the NEXT token is an identifier (a following `varying` continues the type;
      a following OTHER identifier is PG's syntax-error shape `f(char int)` — we
      still rewind and let `parseColumnType` consume the leading word, and the
      dangling ident errors out exactly as on PG). `parseMultiWordTypeName`
      (internal/parser/ddl.go) collapses every spelling to the canonical
      `character`/`varchar` (`nchar`→`character`, `national character
      [varying]`→`character [varying]`, `char varying`→`varchar`), so the
      executor's `regprocedureArglist` renders through the shared
      `catalog.ArgTypeDisplayAlias` byte-identically to PG — the output side
      needs NO change. Along the way it fixes a pre-existing goopg divergence
      surfaced by the same family: `parseColumnType`'s grammar-default length-1
      stamp (gram.y `CharacterWithoutLength` → bpchar typmod 1) covered only bare
      `char`, so a bare `character`/`nchar`/`national character` COLUMN was
      `character(-1)` where PG 18.3 makes it `character(1)` — the stamp now fires
      for `character` too (`bpchar` spelled directly and the cast path are
      deliberately untouched; the live `'cd'::nchar(3)` → `[cd]` padding probe
      agrees on both engines). Verified live vs a throwaway PG 18.3 oracle
      (5534): `CREATE TABLE t (c char, d character, e nchar, f char varying,
      g national character, h nchar(5), i national character varying(10))` →
      `format_type` yields `character(1)/character(1)/character(1)/character
      varying/character(1)/character(5)/character varying(10)` on BOTH engines;
      and all ten alias-spelling CREATE FUNCTIONs render byte-identical
      `oid::regprocedure` output (`f_charvar(character varying)`,
      `f_nchar(character)`, `f_ncharvar(character varying)`, `f_nchar5(character)`,
      `f_natchar(character)`, `f_natchar2(character)`,
      `f_natcharvar(character varying)`, `f_natcharvar2(character varying)`,
      `f_charvar_named(character varying,character varying)`,
      `f_named(character,character varying)`). Test:
      `TestParseCreateFunctionCharFamilyArgTypes` (parser; 11 success + 4
      syntax-error cases). Design:
      `docs/design/0119-0006-char-family-arg-aliases.md` + README row
      `0119-0006bc`. Ledger row 1351 updated: first half resolved, OID-per-arg
      half re-filed. Gates: package suites + pre-commit units +
      `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) all PASS. STILL OPEN (row 1351
      second half): the arglist carries only the arg's NAME, so a bare `char`
      arg remains indistinguishable from OID-18 `"char"` and renders `"char"`
      where PG renders `character` — an OID-per-arg catalog-representation
      change. Unrelated pre-existing note observed during probing (not
      introduced here): `CREATE OR REPLACE FUNCTION` of a pre-existing routine
      can hit "catalog update: freshly extended page did not accept tuple" when
      the pg_proc heap page is full — a pg_proc heap-page-extension limitation.

      **78th slice (2026-08-14): reg* → text/varchar/name/bpchar cast renders
      the name — deferral row 1350 resolved.** A reg* datum is a plain KindInt
      holding the object OID, so casting one to a string type rendered the raw
      OID, not the name: `'pg_type'::regclass::text` → `1247` (PG `pg_type`),
      `'f_varbit(varbit)'::regprocedure::text` → `131072` (PG `f_varbit(bit
      varying)`), `'f_varbit'::regproc::text` → `131072` (PG `f_varbit`), and
      `'pg_type'::regclass::name` passed the KindInt through unchanged. The
      `::reg*` INPUT half resolved correctly; only the downstream string cast
      rendered the numeric datum. `evalCastTyped` (internal/executor/expr.go) —
      which has the source-type name + `*Context` that `evalCast`'s frozen
      signature lacks — now guards: when sourceType ∈ {regclass,regproc,
      regprocedure,regtype,regrole,regcollation} and targetType ∈ {text,varchar,
      name,bpchar} and d.Kind==KindInt, it returns `RegOut(sourceType, oid,
      ctx.Catalog, qualify)` — the 68th slice's shared SELECT+COPY renderer, so
      the cast is the missing third sibling (pattern_sibling_paths_must_agree) —
      `qualify` mirroring the SELECT path's `!publicSchemaVisible` (no per-schema
      qualification, row 1347 stays open); `char` (CHAROID) is excluded
      (charin/charout first-byte semantics). Because the planner stamps
      `CastExpr.SourceType` from the operand type, this also fixes the unprobed
      `regcol::text` column shape. The regclass arm of `regOut` gained dbOid
      scoping (a `dbOid ...uint32` variadic through `RegOut`/`RegOutArgVisible`/
      `regOut` → `LookupTableByOID`/`LookupIndexByOID`) so a regclass cast never
      renders ANOTHER database's relation name — the connDBOid scoping the
      `oid::regclass` CastExpr arm already threads (M0122-0007 4e follow-up 33);
      existing SELECT/COPY callers pass no dbOid and keep DefaultDBOid. Tests
      `internal/executor/reg_cast_to_text_test.go` (SQL battery over six sources
      × 4 targets, a 24-cell direct KindInt matrix, the `regcol::text` shape,
      OID-0→`-`/dangling→numeric, and a cast==SELECT sibling-agreement test),
      mutation-checked. Design `docs/design/0119-0006-reg-cast-to-text-name-rendering.md`
      + README row `0119-0006bd`. Gates: executor package + pre-commit units +
      `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) + `TestPort_RegressSuite/oid`
      PASS.

      **79th slice (2026-08-14): toast-relation OIDs render their `pg_toast`
      name through the shared `RegOut` — deferral row 1305 resolved.** The
      `regclass` arm of `regOut` (`internal/executor/reg_identifier.go`) resolved
      ordinary relations/indexes by OID but fell through to the numeric fallback
      for a synthetic TOAST relation/index OID (parent OID + 100M / +200M),
      which live only in the virtual pg_class builder, never
      `c.tables`/`c.indexes`. The `oid::regclass` CastExpr arm (`expr.go:826-828`)
      already resolved them via `InMemory.ToastRelName`; SELECT
      (`appendTypedCellText`) and COPY (`datumToCopyText`) — and the 78th slice's
      `reg*→text` cast — did not, so a toast OID rendered its `pg_toast` name in
      the cast but its numeric OID in SELECT/COPY. Fix: the regclass arm now
      falls through to `im.ToastRelName(oid, dbOid...)` after both real lookups
      miss, returning the already-schema-qualified `pg_toast.pg_toast_<oid>[_index]`
      name verbatim (never routed through `regOutQualified` — `pg_toast` is off
      every search_path, so qualification is irrelevant), byte-identical to the
      cast arm. Because the fix sits inside shared `RegOut`, all three callers
      inherit it (pattern_sibling_paths_must_agree). Tests:
      `TestRegOutToastrelnameRendersSchemaQualified` (`reg_qualify_test.go`),
      toast rows in `TestRegCopyAndSelectSiblingRenderersAgree`
      (`reg_copy_sibling_test.go`), and `TestRegCastToTextDirectKindIntMatrix`
      (`reg_cast_to_text_test.go`), all mutation-checked. Gates: executor+server
      packages PASS; `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) +
      `TestPort_RegressSuite` (0 FAIL) PASS.

      **80th slice (2026-08-14): array-of-`reg*` columns store 4-byte OID
      elements, not text — deferral row 1306 resolved.** A `regclass[]` (and
      `regtype[]`/`regprocedure[]`/`regrole[]`/`regcollation[]`/`regproc[]`) column
      stored its elements as varlena text (`elemtype=25` in the array blob header)
      where PG stores a 4-byte OID per element resolved through the element type's
      input function (`array_in` → `ReadArrayStr` → `InputFunctionCallSafe`,
      arrayfuncs.c) — the descriptor-vs-blob disagreement the scalar reg* family
      (66th–68th slices) fixed for non-array columns. Two seams:
      `coerceRowForConstraintChecks` skips `col.Type.IsArray`
      (`operators_storage.go:2305`), so the 68th-slice name→OID route never ran;
      and `pgarray.ElemTypeInfo` had no reg* arms, so `encodeArrayValuePG` fell to
      text-element storage. Sibling-triplet fix: `ElemTypeInfo` gains the six
      `isRegIdentifierTypeName` members (fixed 4-byte, align 'i', varlena=false);
      `encodeArrayElem` gains a reg* case resolving name→OID via the shared
      `regIdentifierInput` (ctx+pos threaded through the new `EncodeRowPGCtx`/
      `encodeValuePGCtx`/`encodeArrayValuePGCtx` ctx-carrying siblings; the no-ctx
      wrappers stay for non-writer callers and error on a name, never silently
      store it); and `DecodeElemStyled` renders OID→name via the executor-threaded
      `OutputStyle.RegOut` value (pgarray stays leaf — a value param, not an
      import). Correction to row 1306's premise: the non-indexed defect was silent
      TEXT storage, not a numeric-parse error — the "errors" symptom was the
      INDEXED path only (filed below). Tests: `internal/pgarray/reg_elem_test.go` +
      `internal/executor/reg_array_elem_test.go` (elemtype/size/align, name→OID
      SQLSTATEs, OID 0 → `-`, sibling agreement), mutation-checked. Gates:
      pgarray+executor packages, pre-commit units, `scripts/tpch-spotcheck.sh`
      (Q12=2, Q13=35), `TestPort_RegressSuite` all PASS. Two new deferral rows:
      btree array-key 0A000 (indexed `regclass[]`), WAL pgoutput reg*[] numeric
      rendering. Design `0119-0006-reg-array-element-fidelity.md`.
      **81st slice (2026-08-14): btree `reg*[]` (and scalar `reg*`) keys encode as
      8-byte oidcmp — deferral row 1352 resolved.** `CREATE INDEX` over a `reg*[]`
      column (and over a scalar `reg*` column — the row's "scalar arm already
      exists" premise was wrong; the 66th slice was heap-only, so scalar regclass/
      regtype/regprocedure/regrole/regcollation indexed columns ALSO 0A000'd today)
      raised `0A000` because `encodeBTreeKeyForColumn` had no reg* arm. Two
      corrections drive the design: the KEY is the **8-byte unsigned oidcmp** form
      (`btree.EncodeInt8`), NOT 4 bytes — every reg* type's default opclass is
      `oid_ops` and `array_cmp` compares elements with unsigned `oidcmp`
      (arrayfuncs.c:3991); and `regproc` joins the reg* family (`isRegType`, six
      members) leaving `isOidType` oid-only, so a regproc name element resolves
      instead of 22P02. Name→OID via `regIdentifierInput` (`parseDashOrOid` first,
      then per-type catalog miss SQLSTATEs 42P01/42704/42883/42602/22003 preserved
      via `keyExecError`), `ctx`+`pos` threaded through `encodeBTreeKeyForColumn`/
      `encodeArrayBTreeKey` with a nil-ctx numeric-passthrough contract for the
      fingerprint path. Decode twin lands together: `arrayKeyElemRenderer` reg* arm
      (OID→name via `st.RegOut`, mirroring `DecodeElemStyled`), which makes reg*[]
      decodable automatically (no `btree_key_decodable.go` edit) so index-only scans
      activate. `isSupportedBTreeKeyType` admits `isRegType` (the CREATE INDEX gate
      that actually fired the 0A000). Tests: six reg* rows in `scalarKeyCases`/
      `indexKeyTypeCases`/`arrayKeyDecodeCases` + reg*[] name-literal IOS cases +
      `TestScalarRegIndexDDLMaintains` (E2E build+maintain), mutation-checked.
      Gates: pre-commit units + `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) PASS.
      Design `0119-0006-btree-reg-array-key-oidcmp.md`.
      **82nd slice (2026-08-14): pgoutput renders reg* column values as names,
      not numeric OIDs — deferral row 1353 resolved.** The logical-replication
      `pgoutput` decoder emitted a reg* value as its numeric OID (`1259` /
      `{1259}`) where PG 18.3's TEXT-mode pgoutput emits the NAME (`pg_class` /
      `{pg_class}`). Root cause was a send/out conflation: `logicalrep_write_typ`
      (proto.c:848) serializes text mode via `OidOutputFunctionCall(typclass->typoutput, …)`
      = `regclassout` → name (regproc.c:940); the 4-byte-OID form is BINARY mode's
      `typsend`. goopg's text-only `pgoDecodePhysicalValue` had shipped the binary
      image — and its SCALAR arm too (the six reg* types rode the oid/cid/xid arm,
      justified by a `regclasssend` comment), not just the array arm the row named.
      Fix threads `executor.RegOut` (the existing reg*out port, single source of
      truth) into the wal layer as a leaf closure — `CatalogSnapshot.RegOut
      func(typeName, oid) string` (nil = numeric fallback), bound by the publisher
      walsender via new exported `executor.RegOutRenderer(im, false)` (server→
      executor→wal; wal→executor is a CYCLE so the renderer is a value, not an
      import). `oid`/`cid`/`xid` stay numeric (no name form). Both twins: scalar
      reg* arm split out of the oid arm, array arm `RenderText`→`RenderTextStyled(…,
      OutputStyle{RegOut})`. Tests reworked + new (`TestPgoutputSnapshotRegOutRendererWired`
      vs a real catalog); mutation-checked. Gates: `go test ./internal/wal/
      ./internal/server/`, pre-commit units, `scripts/tpch-spotcheck.sh` (Q12=2,
      Q13=35) PASS. New deferral row: off-path schema qualification (qualify=false
      renders a bare non-public-schema regclass) + cross-DB regclass resolution (no
      dbOid bound). Design `0119-0006-pgoutput-reg-names.md`.
      **83rd slice (2026-08-14): expression-key btree type gate — box/int4range
      expressions no longer silently build.** `CREATE INDEX ON t ((box_col))` /
      `((int4range_col))` bypassed the named-column `isSupportedBTreeKeyType` gate
      (`createBTreeIndex` skipped `name == ""` columns) and silently built a B-tree
      index encoding the value's TEXT in varchar order (`encodeArbiterExprKey`'s
      KindString arm, operators_upsert.go:1649). PG 18.3 rejects a btree index on a
      box expression with 42704 (box has no btree opclass — `GetDefaultOpClass`→
      InvalidOid, indexcmds.c:2270-2277); int4range PG accepts via `range_ops`
      (binary-coercible-to-anyrange, pg_opclass.dat:230) but goopg has no range
      value model, so it must reject honestly. The expression-key branch now applies
      the SAME `isSupportedBTreeKeyType` + enum check as the named-column branch,
      returning the SAME 0A000 (the 42704 polish for box is deferred). Resolves via
      `planner.ResolveIndexPredicate` + `planner.ExprResultType` (the build path's
      own pair); gates only when both resolve, so float/enum/text expression indexes
      are untouched. Gates: `TestExpressionIndexKeyRejectsBoxAndInt4Range` +
      `TestExpressionIndexKeyStillAllowsFloatEnumText` (mutation-witnessed);
      executor/planner/btree packages, pre-commit units, tpch-spotcheck (Q12=2,
      Q13=35) PASS. This closes the "box/int4range key encodings" half of the
      remaining-scope note below; box is NOT a valid key target (no PG btree opclass)
      and int4range is blocked on the range value model (see the 2026-08-14 ledger
      row).
      **84th slice (2026-08-14): pgoutput renders an off-path reg* value
      schema-qualified — deferral row 1354 claim 1 resolved.** The publisher
      walsender bound the pgoutput reg* renderer with a fixed `qualify=false`
      (`logicalwalsender.go:75`), so a regclass in a non-public schema rendered its
      BARE name where PG 18.3's `regclassout` schema-qualifies via `RelationIsVisible`
      (regproc.c:973-981 → namespace.c) — the object's schema is qualified iff NOT on
      the effective search_path (a TERNARY visibility rule, not "always qualify
      non-public"). The fix threads a per-schema visibility predicate instead of the
      fixed flag: `regOut`'s switch body moved to `regOutShared(..., qualify
      func(schema string) bool, regtypeQualify bool, argVisible, dbOid...)`, with
      `RegOut`/`RegOutArgVisible` as byte-identical wrappers (constant predicate) so
      the SELECT/COPY/cast siblings are provably unchanged; new
      `RegOutRendererVisible(cat, visible, dbOid...)` drives the five
      `regOutQualified` call sites (regclass table/index, regproc user, regprocedure,
      regcollation user) with `qualify(schema)`. The walsender binds
      `RegOutRendererVisible(im, func(s){s==""||s=="pg_catalog"||s=="public"})` —
      the publisher never SETs search_path (`postgres/src/backend/replication/` has
      zero publisher-side writes), so the visible set is the default `{pg_catalog,
      public}` ($user-schema edge approximated away, documented). regrole/TOAST stay
      untouched; the regtype arm keeps its fixed bool (a separate catalog-schema gap,
      new ledger row). Gates: `go test ./internal/executor/ ./internal/server/
      ./internal/wal/`, pre-commit units, tpch-spotcheck (Q12=2, Q13=35) PASS. Tests:
      `TestRegOutRendererVisibleOffPathQualifies` +
      `TestPgoutputSnapshotRegOutRendererVisibleOffPathQualifies`. Design:
      `docs/design/0119-0006-pgoutput-reg-names.md` §"Off-path schema qualification".

> This task list is **seeded, not exhaustive.** M0119-0001 triage plus every future
> deferral-ledger entry (any new `status = -` row) feed additional M0119 tasks over
> time; the milestone's living nature means it need not be complete at filing.

## M0122 — Unimplemented-Feature Backlog Consumption (filed 2026-07-04)

Milestone: `docs/milestones/0122-unimplemented-feature-backlog-consumption.md`
(**living milestone** — tasks are appended over time). Source of truth:
`unimplemented_feat.json` (repo root; 181 entries generated 2026-07-02 from the
commit log). Goal: drive every `open` feature entry to closure — implement the
deferred scope, or verify it already landed and mark the entry `resolved`.

**⚠️ Verify-before-implement (READ FIRST):** `unimplemented_feat.json` is a
2026-07-02 snapshot and **may list features that are already implemented** — 24
entries have an `unclear`/absent `code_audit` and 61 have an open matching ledger
row (7 overlap both). When you pick up ANY M0122 task, FIRST re-verify each
candidate against current HEAD (grep/read code, probe a live goopg, check
ledger/fix_plan/git log). If it already exists, set the entry's `status` to
`resolved` (cite the proof) and DO NOT re-implement. Only build genuinely-missing
scope.

**Per-task rule (applies to every M0122 implementation task):** before
implementation begins, the picking agent MUST (1) create a design doc at
`docs/design/<id>-NNNN-*.md` and index it in `docs/design/README.md`, and (2) have
that design doc pass an agent review. Implementation starts only after the
reviewed design doc exists. (The triage task M0122-0001 is doc-only, exempt.)
Tracking field = a per-entry `status` (`open`/`resolved`) added by M0122-0001,
mirroring M0119's ledger `status` column.

_(completed `[x]` subtasks archived → `completed_milestones/completed_fix_plan_010.md`)_

- [ ] **M0122-0008 — Auth / roles / multi-DB isolation / encoding** (~6). SASLprep
      / channel binding / `scram_iterations`, RBAC + `SET SESSION AUTHORIZATION`,
      encoding constraints during bootstrap/runtime.
      **Status (2026-08-09):** SASLprep, scram_iterations, RBAC for DML/SELECT/
      view-owner, and SET SESSION AUTHORIZATION (full end-to-end SetSessionAuthorization
      callback in both simple/extended query paths) all LANDED. CREATE DATABASE ENCODING
      validation landed this loop (M0122-0008: ValidServerEncodingName +
      extractEncodingRawFromSQL + catalog.databaseEncoding storage + heap-row override).
      **Genuinely remaining:** (1) channel binding — blocked on TLS infrastructure
      (SCRAM-SHA-256-PLUS needs tls-server-end-point); (2) additional encoding
      pairs beyond LATIN1↔UTF8 (EUC_JP, SJIS, BIG5, GBK, etc. — each follows
      the same proc-registration pattern); (3) query-text transcoding on
      simple-query and Parse paths (Bind parameter transcoding is wired).
      **Byte-level encoding conversion first slice LANDED (2026-08-09, this loop):**
      New `internal/mb/` package (mirrors PG's `src/backend/utils/mb/`) with
      `DoEncodingConversion` dispatch, LATIN1↔UTF8 conversion procs (OIDs
      4374/4375), and `pg_utf_mblen`/`pg_utf8_islegal` validation. Wired into
      all three DataRow output paths (simple-query, cursor FETCH, extended-query
      Execute) via `Server.maybeConvertCellsForClientEncoding`, plus Bind
      parameter input transcoding in `handleExecuteFrame`. Catalog
      `FindDefaultConversionProc(forEnc, toEnc)` resolves user-created
      conversions. Design: `docs/design/0122-0020-encoding-conversion-mb-layer.md`.
      Verified: live `psql` with `client_encoding=LATIN1` correctly transcodes
      accented characters; TPC-H spotcheck PASS; pgbench smoke 0 failed.
      **Bootstrap encoding enforcement (name validation) verified COMPLETE (2026-08-09):**
      initdb resolveEncoding validates --encoding flag; catalog ValidServerEncodingName
      validates CREATE DATABASE ENCODING; all three tiers reject invalid/client-only
      encoding names. Only actual byte-level transcoding remains unimplemented.
      **pg_client_encoding() / getdatabaseencoding() builtins landed (2026-08-09, this loop):**
      Both functions now return correct results through the executor funcCall dispatch.
      pg_client_encoding() reads the live client_encoding GUC via GetSetting.
      getdatabaseencoding() reads the encoding ID from catalog.InMemory.DatabaseEncoding
      and maps it to a canonical name via catalog.EncodingIDToName. Tests:
      TestEvalPgClientEncoding + TestEvalGetDatabaseEncoding
      (internal/executor/encoding_builtins_test.go) and live-psql verification.
      **pg_char_to_encoding() builtin landed (2026-08-09, this loop):**
      pg_char_to_encoding(name) → int4 dispatch arm added in evalFuncCall; delegates
      to catalog.EncodingNameToID for canonical-name/alias/clean-name resolution.
      Tests: TestEvalPgCharToEncoding (7 subtests) + live-psql verification.
      **client_encoding GUC validation landed (2026-08-09, previous loop):**
      SET client_encoding TO 'INVALID' now raises 22023 (invalid_parameter_value).
      Added CheckFn callback to config.Variable; checkClientEncoding resolves the
      value against the PG 18.3 pg_enc2name_tbl (42 canonical names + alias table),
      accepting all valid encodings including client-only ones (SJIS, BIG5, GBK,
      etc.) since client_encoding is per-connection. Encoding table duplicated in
      config/encoding_guc.go (config is a leaf package; import cycle prevents
      sharing). Tests: TestClientEncodingValidation (guc_test.go) +
      TestEncodingTableIntegrity (encoding_guc_test.go).
      **RBAC for INSERT/UPDATE/DELETE landed (2026-07-05, this loop,
      M0097-0040):** `dmlPrivilegePermitted` (`internal/executor/
      operators_storage.go`) checks the existing `tableACLs`/
      `HasTablePrivilege` store (TRUNCATE/MAINTAIN already consulted it;
      plain DML never did) pre-lock in `insertOp`/`updateOp`/
      `deleteOp.Open`, raising `42501` for a non-superuser, non-owner role
      missing the matching GRANT. FK-cascade deletes and the logical-
      replication apply worker write heap pages directly and are
      unaffected. Tests: `internal/executor/storage_dml_test.go`'s
      `TestDMLRequiresTablePrivilege`. Design:
      `docs/design/0118-0039-truncate-conflict-privilege-model.md` Follow-up
      section; `unimplemented_feat.json` M0097-0040 updated in place.
      **`SELECT` enforcement landed (2026-07-05, same day):**
      `seqScanOp.Open`/`indexScanOp.openPrep`/`indexOnlyScanOp.Open` now call
      `dmlPrivilegePermitted(ctx, tbl, "SELECT")`, with a
      `catalog.IsSystemRelation(tbl.OID)` carve-out that always permits
      SELECT on pg_catalog/information_schema (no pg_init_privs-equivalent
      default-ACL seeding exists). Tests:
      `TestSeqScanRequiresSelectPrivilege`,
      `TestIndexScansRequireSelectPrivilege`,
      `TestSystemCatalogSelectAlwaysPermitted`. Design doc Follow-up section
      extended; `unimplemented_feat.json` updated in place.
      **View-owner privilege check landed (2026-07-06):** `execCreateView`
      now stamps the creating role as `Owner` (previously every view was
      silently owned by the bootstrap superuser); new
      `planner.tagViewOwnerScans` (`internal/planner/view_privilege.go`)
      tags every scan inside an inlined view's plan tree with the view
      owner's role (skipped under `WITH (security_invoker = true)`, now
      actually enforced for the first time); `dmlPrivilegePermittedAs`
      lets the three SELECT-gated scan operators check that tagged role
      instead of the querying session's own. `GRANT SELECT ON view TO
      role` alone (no base-table grant) now works. Tests:
      `internal/planner/view_privilege_test.go`,
      `internal/executor/storage_dml_test.go`'s
      `TestScanOperatorsUseViewOwnerPrivilegeOverride`,
      `internal/executor/view_owner_privilege_test.go`. Design:
      `docs/design/0118-0039-truncate-conflict-privilege-model.md` Follow-up
      section; ledger row (resolved). **Still open (ledger, scope
      boundary):** the view's own ACL is never checked against the
      querying role (no plan node represents "scan the view itself"), so a
      role with zero grants anywhere can still read a view whose owner has
      base-table access — needs a preliminary per-statement RTE-style
      permission pass, materially larger than this follow-up.
      SASLprep/channel binding/`scram_iterations`, encoding constraints.
      **`scram_iterations` wired into password hashing landed (2026-07-08,
      this loop):** the GUC (`internal/config/defaults.go`, registered
      since earlier but never read anywhere) is now actually consulted by
      `CREATE`/`ALTER ROLE ... PASSWORD 'plain'` — `auth.NewSCRAMSecret`'s
      hardcoded `scramDefaultIterations` (4096) call site is replaced with
      a new `auth.NewSCRAMSecretWithIterations(pw, iterations)` sibling,
      and `applyRoleAttrOptions` (`internal/server/role_ddl.go`) now takes
      the same `currentGUCResolver` its two callers already had in scope
      (previously only used for `SET ... FROM CURRENT`); a new
      `resolveScramIterations` helper reads the live `scram_iterations`
      value. The auth/verification side needed no change — `scram.go:326`'s
      server-first-message already renders `s.secret.Iterations` parsed
      back out of the stored verifier, not a constant, so it was already
      correct; only the write path was pinned to the default. Tests:
      `internal/server/role_ddl_scram_iterations_test.go`
      (`TestCreateAlterRolePasswordHonorsScramIterationsGUC`), confirmed
      non-vacuous via `git stash`. Design: `docs/design/
      root-0021-role-auth-persistence.md` new "Follow-up: `scram_iterations`
      GUC wired into password hashing" section; `docs/design/README.md`
      root-0021 row extended. Gates: `go build ./...`/`go vet ./...` clean;
      `go test ./internal/server/... ./internal/auth/... ./internal/config/...`
      PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
      failed transactions, all 3 workloads). SASLprep and TLS channel
      binding remain fully unimplemented in this cluster (separate,
      larger slices — SASLprep needs a Unicode-normalization dependency
      not currently in `go.mod`; channel binding needs TLS
      tls-server-end-point wiring).
      **SASLprep landed (2026-07-08, this loop):** ported `pg_saslprep`
      (`postgres/src/common/saslprep.c`, RFC 4013) to
      `internal/auth/saslprep.go`, including its exact algorithm quirk
      (prohibited-output/bidi checks run against the mapped-but-pre-NFKC
      codepoints, not the final normalized output) and its six Unicode
      range tables, mechanically extracted from the C source by a one-off
      script into `internal/auth/saslprep_tables.go` (not hand-transcribed,
      to guarantee byte-identical data — 396+360+36+... range pairs).
      NFKC normalization added via a new `golang.org/x/text` dependency
      (`unicode/norm.NFKC`, NOT `secure/precis.OpaqueString`, which is
      NFC per RFC 8265 — a different, non-upstream-compatible form).
      Wired into `auth.NewSCRAMSecretWithIterations` (mirrors
      `pg_be_scram_build_secret`) and
      `SCRAMSecret.VerifySCRAMSecretFromPassword` (mirrors
      `scram_verify_plain_password`), both falling back to the raw
      password on SASLprep failure like upstream. The live SCRAM
      handshake itself needed no change — it never re-derives from a
      plaintext password, only checks the client's proof against the
      already-prepped stored secret. Tests:
      `TestPGSASLPrepRFC4013Examples`/`TestPGSASLPrepInvalidUTF8`/
      `TestSCRAMSecretNormalizesEquivalentUnicodeForms`
      (`internal/auth`) plus a differential e2e test against a REAL
      libpq client — `TestE2E_SASLPrepMatchesRealLibpqClient`
      (`internal/testport`), since lib/pq's own Go SCRAM client does no
      SASLprep at all (confirmed by reading its `scram` package), so only
      real `psql` (linked against upstream's own saslprep.c) meaningfully
      proves cross-implementation byte parity; a role's password
      containing U+2168 ROMAN NUMERAL NINE, stored via `CREATE ROLE`,
      authenticates against the plain ASCII canonical form "IX" over a
      real SCRAM handshake. Added `cluster.PSQLWithPassword` test-infra
      helper (`internal/testutil/cluster/cluster.go`) since none of the
      existing psql helpers allowed a non-empty `PGPASSWORD`. Design:
      `docs/design/0049-0003-scram-sha-256.md` new §3.1 + README row.
      Deferral-ledger row appended (channel binding — the other named gap
      — remains open, needs TLS wiring that doesn't exist anywhere in the
      server yet, a materially larger separate slice). Gates:
      `go build ./...`/`go vet ./...` clean; `go mod tidy` clean;
      `go test ./internal/auth/... ./internal/server/...` PASS; targeted
      `internal/testport` e2e SCRAM/role tests PASS;
      `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS
      (0 failed transactions, all 3 workloads).
- [ ] **M0122-0009 — WAL / recovery / crash-consistency infra** (~16). WAL segment
      recycling, `WALInsertLock` array (parallel inserts), MultiXact WAL,
      `pg_subtrans` truncation. Gate: `-race` + recovery E2E (WAL practice card).
      **`pg_subtrans` truncation landed (2026-07-09, this loop):** the bucket's
      one previously-untouched item with no prior progress notes.
      `internal/mvcc/subxact_visibility.go`'s `SubxactMap` (in-memory
      `parents`/`aborted` maps) and `internal/mvcc/subxact_slru.go`'s
      `SubtransSLRU` (`pg_subtrans/` SLRU mirror, M0117-0003) had no removal
      primitive at all — both grew without bound for the lifetime of a
      cluster, a gap the M0117-0003 design doc's own "Known follow-ups"
      section had already flagged and left for later. New
      `SubtransSLRU.TruncateBefore(oldestXact)` unlinks segment files whose
      highest page strictly precedes `oldestXact`'s SLRU page (new
      `SubtransPagePrecedes`, `CLOGPagePrecedes`'s twin scaled to
      `subtransXactsPerPage`), mirroring `clog.go`'s `truncateSLRUSegments`
      (reuses the same-package `parseSLRUSegName` helper). New
      `SubxactMap.Truncate(oldestXact)` prunes both in-memory maps
      (wraparound-safe via `storage.XIDPrecedes`) and calls through to the
      SLRU when persistence is enabled; nil-safe when it isn't. New
      `CheckpointerConfig.TruncateSubtransFn` (`internal/wal/checkpointer.go`)
      invoked from `runCheckpoint` right after `TruncateCLOGFn`, same
      best-effort/non-fatal error treatment. `internal/initdb/open.go` wires
      it to the identical `horizon = min(datfrozenxid, OldestXmin)`
      computation `TruncateCLOGFn` already uses — safe because any subxid
      below that horizon's top-level xact already has a direct CLOG
      `Committed`/`Aborted` status (never `SubCommitted`), so its parent link
      is never consulted again; reusing the existing, already-tested horizon
      avoids introducing a second, subtly-different computation. No WAL
      record emitted — matches upstream `TruncateSUBTRANS`, which PG likewise
      never WAL-logs (`pg_subtrans` is disposable across a crash;
      `StartupSUBTRANS` just zeroes it on restart — goopg's restore-on-restart
      choice per M0117-0003 is an orthogonal, deliberate divergence unrelated
      to this fix). Tests: `TestSubtransSLRUTruncateBefore`/
      `TestSubxactMapTruncate`/`TestSubxactMapTruncateNoPersistence`
      (`internal/mvcc/subxact_truncate_test.go`),
      `TestCheckpointerCallsTruncateSubtransFn`/
      `TestCheckpointerTruncateSubtransFnErrorIsNonFatal`
      (`internal/wal/checkpointer_test.go`). Design:
      `docs/design/0122-0009-pg-subtrans-truncation.md` (new);
      `docs/design/0117-0003-pg-subtrans-restore-on-restart.md`'s "Known
      follow-ups" section updated to point at it; `docs/design/README.md`
      index updated (both the new row and the 0117-0003 row's stale
      follow-up note). Gates: `go build ./...` clean; `go vet`/`go test`
      clean+PASS across `internal/mvcc`/`internal/wal`/`internal/initdb`
      (the `internal/initdb` package test takes ~5 min, ran to completion);
      `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
      failed transactions, all 3 pgbench workloads).
      **WAL segment recycling landed (2026-07-09, next loop):**
      `Writer.RemoveOldSegments` previously unlinked every obsolete segment;
      upstream recycles some of them (rename into a future segment slot,
      `RemoveXlogFile`/`InstallXLogFileSegment`) so a later `openSegment`
      skips its own create+zero-fill+directory-fsync. New `Config.MinWALSize`
      (wired from the previously-unread `min_wal_size` GUC via
      `internal/initdb/open.go`'s `OpenOptions.WALMinSize`, read in
      `cmd/goopg/main.go` the same way `max_wal_size` already is) caps how
      many of the newest obsolete segments `state.removeOldSegments`
      (`internal/wal/writer.go`) recycles via the new `recycleSegmentFile`
      helper (rename + zero-fill + fsync, reusing `preallocateSegment`) vs
      unlinks; `<= 0` (default) disables recycling, byte-identical to prior
      behaviour. The recycle target is the lowest free segment slot at or
      after the keep segment (mirrors upstream's `find_free` scan, never
      clobbers a live/already-recycled segment). Diverges from upstream by
      zero-filling the recycled segment (upstream leaves old content as-is,
      relying on per-record CRC to bound recovery scans) because goopg's
      `reader.go` graceful-EOS heuristic checks for an all-zero tail instead
      — an unzeroed recycled segment's leftover well-formed old record would
      pass CRC validation and be misread as live WAL. `SlotAwareRetainer.Retain`
      (`internal/wal/retention.go`) threads the new `recycled` count through
      to its summary log (`segments_recycled` alongside `segments_removed`).
      Tests: `TestRemoveOldSegmentsRecyclesUpToMinWALSize` (confirms recycled
      files are genuinely zero, not stale content — the load-bearing
      correctness check), `TestRemoveOldSegmentsRecycleCapExceedsObsoleteCount`
      (`internal/wal/retention_test.go`); pre-existing `TestRemoveOldSegments*`
      tests (implicit `MinWALSize=0`) continue to pin the recycling-disabled
      default. Design: `docs/design/0122-0009-wal-segment-recycling.md` (new,
      cites upstream `xlog.c` source); `docs/design/README.md` index updated.
      Deferral-ledger row filed: only the `min_wal_size` floor half of
      upstream's `XLOGfileslop` sizing is implemented, not the
      checkpoint-distance-estimate/`max_wal_size`-ceiling halves. Gates:
      `go build ./...`/`go vet ./...` clean; `go test`/`go test -race` PASS
      across `internal/wal`, `internal/mvcc`; `go test ./internal/initdb/...`
      PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
      `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0
      failed transactions, all 3 pgbench workloads). **Still open in this
      bucket (at that point):** `WALInsertLock` array (parallel inserts),
      MultiXact WAL, eager next-segment lookahead.
      **Eager next-segment lookahead landed (2026-07-09, next loop):**
      closes the `unimplemented_feat.json` M0007 entry left over from the
      original 0007-0001 preallocation design (deferred there as "gives
      lower commit-path tail latency at rollover but adds a background
      goroutine"). `state.openSegment(segNo)` (`internal/wal/writer.go`) now
      calls a new `state.eagerPreallocSegment(segNo+1)` right after handling
      `segNo` itself, spawning a background goroutine that zero-fills a
      `<segfile>.eager<pid>.tmp` file and durably links it into place
      (`os.Link`, EEXIST-tolerant no-clobber — mirrors upstream
      `XLogFileInit`'s temp-then-link pattern) so a genuine rollover usually
      finds the next segment already preallocated instead of paying for it
      synchronously; new `state.eagerInFlight`/`eagerMu` dedupe concurrent
      triggers for the same segment, `state.eagerWG` lets `close()` wait for
      any still-running job before tearing down `s.files`. Found and fixed a
      real correctness hazard this exposed on the way: `detectWritePos`
      (consulted only at writer-reopen time, e.g. after a restart) used to
      trust every non-last on-disk segment as "fully used" via file size
      alone, content-scanning only the literal highest-numbered file — a
      crash between eagerly preallocating `segNo+1` and the writer ever
      really reaching it leaves a fully zero, never-written `segNo+1` file
      *above* the genuinely partially-written `segNo`, which the old logic
      would silently overshoot past (trusting `segNo` as full while
      content-scanning the empty phantom instead). Fixed by walking backward
      from the highest segNo, trimming any segment that is both full-size
      and scans as entirely empty, before running the existing (otherwise
      unchanged) last-segment scan logic — the full-size guard is what keeps
      this from misclassifying a genuine short/legacy empty-last segment
      (already handled correctly, unchanged). Also fixed a pre-existing
      pg_waldump test (`TestPGWaldumpParsesEmittedWAL`) that the new second
      on-disk segment file exposed: bare `pg_waldump -p walDir -s .. -e ..`
      (no explicit filename) auto-detects `WalSegSz` by opening "any
      WAL-looking file" via unordered `readdir()` (`identify_target_directory`
      / `search_directory`, `pg_waldump.c`), which can hand it the all-zero
      segment 1 and misread its zeroed long-page-header as `xlp_seg_size=0`
      — a pre-existing upstream pg_waldump quirk (real PG WAL directories
      have the same kind of preallocated future segment during normal
      operation), fixed by naming the exact start segment as a positional
      argument, the standard unambiguous invocation form. Tests:
      `internal/wal/writer_detect_test.go`'s new
      `TestDetectWritePos_IgnoresEagerPhantomFutureSegment` (confirmed
      non-vacuous by reverting the trim loop — fails with the exact
      predicted writePos overshoot); `internal/wal/wal_test.go`'s
      `TestPreallocationCounters` updated to `w.stateRef.eagerWG.Wait()`
      before each assertion and re-derive the new one-segment-ahead expected
      totals (was implicitly relying on the background goroutine losing a
      race it had no guaranteed way to lose). **Independent review caught a
      genuine bug in the first cut:** `close()`'s `eagerWG.Wait()` ran
      *before* `flushUpTo`, but with `Config.WALBuffers > 0` (the default)
      `flushUpTo` can itself be the first caller of `openSegment` for a
      segment (buffered bytes never drained until Close), which then kicks
      off a brand-new eager job with zero chance to have started before that
      earlier `Wait()` already returned — `Close()` could return while a
      background goroutine was still writing into the WAL directory. Fixed
      by moving `Wait()` to after `flushUpTo` (the last remaining
      `openSegment` caller inside `close()`). New test
      `TestClose_WaitsForEagerJobTriggeredByItsOwnFlush`
      (`writer_detect_test.go`, confirmed non-vacuous — fails ~95% of runs
      with the ordering reverted, a real race not a rare corner case).
      Design:
      `docs/design/0007-0001-wal-segment-preallocation.md` new "Follow-up
      (2026-07-09): eager next-segment lookahead" section;
      `docs/design/README.md` row updated; `unimplemented_feat.json`'s
      matching M0007 entry flipped to `resolved` (task_id retagged
      `M0122-0009`). No deferral-ledger row needed — nothing new was left
      unimplemented (the pre-existing `posix_fallocate` deferral,
      unaffected by this loop, was already tracked in the design doc before
      this change). Gates: `go build ./...` clean; `go test`/`go test -race`
      PASS across `internal/wal`; `go test ./internal/initdb/...` PASS (no
      regression in Init+Open+restart recovery, ~5 min); `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
      scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
      pgbench workloads). **Still open in this bucket:** `WALInsertLock`
      array (parallel inserts), MultiXact WAL.
      **2026-07-09 (next loop) — reconciliation, no code change:** verified
      the `WALInsertLock` array line item is in fact already fully landed
      (M0107-0007 slice B, `docs/design/0107-0007ah-wal-tryappend-rwmutex.md`
      / `0107-0007aj-wal-segment-cross-reservation.md` and ~28 sibling design
      docs `0107-0007a`..`0107-0007aj`) — it was a stale leftover in this
      bucket's summary line, not real remaining work. Confirmed by code
      reading, not just docs: `internal/wal/padded_mutex.go`'s
      `appendLockSet` is an 8-stripe `[8]paddedMutex` array
      (`appendLockStripes = 8`, matching PG's `NUM_XLOGINSERT_LOCKS` /
      `WALInsertLocks[]`, `xlog.c`/`xlog.h`), genuinely wired (not dead code)
      into every hot append path via `stripe_writer_core.go`'s
      `c.locks`/`stripeAppend`/`stripeAppendBuild`/`stripeAppendBuiltEmitted`,
      selected per-caller by `stripeForProcNum(procNum)`. `writer.go`'s
      `tryAppend` fast path takes `state.appendMu.RLock()` (shared) then the
      one stripe lock via `AppendXLogPayload`, so up to 8 concurrent
      backends genuinely append into disjoint WAL-buffer regions in
      parallel; only the replica WAL-apply path (`appendRaw`, sequential by
      nature — a single WAL receiver, matching upstream) and
      checkpoint/recovery resets take the exclusive `Lock()`. Re-ran the
      three tests that pin this concurrency model at HEAD (unmodified):
      `go test -race -run
      'TestConcurrentTryAppendProceedsInParallel|TestTryAppendRLockDoesNotBlockSiblings|TestConcurrentAppendAcrossSegmentBoundariesNoOverflow'
      ./internal/wal/...` — all 3 PASS. No fix_plan/deferral-ledger row
      needed (nothing was actually missing); this bucket's remaining named
      item is `MultiXact WAL` only. Surveyed that one too before choosing
      this reconciliation instead: `internal/multixact/` is an explicitly
      unwired, in-memory-only primitive (package doc: "the risky hot-path
      integration ... lands in later loops on top of this verified
      primitive") — no SLRU-backed offsets/members store, no xmax-stamping
      wiring, no WAL record kinds at all (`grep -rn Multixact
      internal/wal/*.go` only hits two placeholder comments). WAL-logging
      multixact creation presupposes a durable multixact SLRU exists to
      protect first — that foundation doesn't exist yet, and building it
      plus wiring it into the tuple-header hot path is multi-loop,
      feature-sized work on the same class of hot path (xmax) that has
      already cost this project many multi-loop corruption-hunt threads
      (see the `M-NIGHTLY (AI-20260709-010336-082)` btree thread above) —
      correctly left deferred rather than rushed into one loop.
      **`max_wal_size` ceiling + `CheckPointDistanceEstimate` — done
      (2026-07-09, next loop, closes the deferral-ledger row from the
      original WAL segment recycling loop):** the bucket's other named
      sizing gap. New `computeSpareSegments` (`internal/wal/writer.go`)
      ports upstream's `XLOGfileslop` (xlog.c) formula as segment counts
      relative to the retention keep-segment rather than absolute
      LSN/segNo math (behaviourally equivalent, avoids needing goopg's
      1-based LSN encoding to line up bit-for-bit with upstream's 0-based
      `XLogSegNo` arithmetic); new `Checkpointer.CheckPointDistanceEstimate()`
      ports `UpdateCheckPointDistanceEstimate`'s jump-up-immediately/
      decay-slowly (90/10) EMA verbatim, fed from each `runCheckpoint`
      cycle's redo-LSN delta. New `Writer.RemoveOldSegmentsWithEstimate` +
      `SlotAwareRetainer.CheckPointDistanceEstimateFn`/`CompletionTarget`
      wire it through Retain; `cmd/goopg/main.go` reads `max_wal_size`
      (new `wal.Config.MaxWALSize` via `initdb.OpenOptions.WALMaxSize`,
      default 1024 MB matching upstream) and `checkpoint_completion_target`
      the same way `min_wal_size`/`checkpoint_completion_target` already
      feed the checkpointer's other knobs. The pre-existing
      `RemoveOldSegments` public API is unchanged behaviourally — it now
      forwards to the same formula with both new inputs zeroed, proven to
      reduce to the original `ceil(MinWALSize/SegmentSize)` floor exactly
      (every pre-existing test using it, e.g.
      `TestRemoveOldSegmentsRecyclesUpToMinWALSize`, still passes
      unmodified). Tests:
      `TestComputeSpareSegmentsMatchesMinWALSizeFloorWhenNoEstimate`/
      `TestComputeSpareSegmentsGrowsWithDistanceEstimate`/
      `TestComputeSpareSegmentsCapsAtMaxWALSize`/
      `TestRemoveOldSegmentsWithEstimateHonoursDistanceAndMax`/
      `TestSlotAwareRetainerUsesCheckPointDistanceEstimateFn`
      (`internal/wal/retention_test.go`),
      `TestCheckpointerUpdatesCheckPointDistanceEstimate`
      (`internal/wal/checkpointer_test.go`, pins the jump-up/decay-down
      shape across real `CheckpointNow()` cycles without asserting exact
      byte counts). Design: `docs/design/0122-0009-wal-segment-recycling.md`
      new "Follow-up (2026-07-09)" section; `docs/design/README.md` row
      updated; deferral-ledger row flipped to `resolved`, new row appended
      closing it. Gates: `go build ./...`/`go vet ./...` clean; `go test`/
      `go test -race ./internal/wal/...` PASS; `go test
      ./internal/initdb/... ./cmd/goopg/...` PASS; `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
      scripts/ralph-precommit-test.sh` PASS (0 failed transactions, all 3
      pgbench workloads). **M0122-0009's WAL-segment-recycling sizing
      sub-bucket now has no known open gap; MultiXact WAL remains the
      bucket's sole open item** (multi-loop, feature-sized — see the
      survey directly above).
- [ ] **M0122-0010 — Concurrency: buffer pool & btree locking** (~17, LARGE).
      Lehman/Yao crab-walk, `splitMu` removal, storage-pool pin-count race,
      re-enable the `-race` gate. Gate: race detector mandatory.
      **2026-07-09 loop — fixed the internal-page sibling-relink
      cross-connection race** (continuation of the M-NIGHTLY
      AI-20260709-010336-082 pgbench-reopen thread's closing note: "a
      future structural-write path added without the same re-validation
      discipline... should be treated as suspect until it's audited the
      same way"). Audited `internal/access/btree/btree_vacuum.go`'s
      remaining structural-mutation call sites for the exact bug class
      just fixed there (leaf sibling-relink using a stale unlocked
      `liveSibling` capture instead of a fresh re-derivation under the
      write-side `pinW`) and found the IDENTICAL gap one level up:
      `unlinkEmptyInternalPage` (WAL path) and
      `unlinkEmptyInternalPageFPI` (FPI fallback) — used by
      `maybeCascadeEmptyInternal` to unlink a vacuumed-empty internal
      page — both computed `leftLive`/`rightLive` via the same unlocked
      pre-pass and wrote them verbatim, exposed to the same cross-
      connection splice-then-stomp corruption `bt.splitMu` cannot
      prevent (per-`*BTree`-Go-instance only, not cross-connection).
      Fixed both to re-derive the live neighbour via a fresh
      `liveSibling` walk inside the same `pinW` hold that performs the
      write, mirroring the leaf-level fix exactly. New regression test
      `TestUnlinkEmptyInternalPagePreservesConcurrentSplice`
      (`internal/access/btree/btree_vacuum_internal_race_test.go`)
      deterministically reproduces the race with no goroutines needed:
      builds a real 3-level (root/internal/leaf) tree via `BulkCreate`
      (n=900000, same recipe as the existing
      `TestVacuumIndexPagesCascadesEmptyInternalPage`), captures a
      target internal page's real live prev/next exactly like
      `maybeCascadeEmptyInternal` does, splices a synthetic live page in
      between (simulating a same-window concurrent split on a different
      connection), then invokes the low-level unlink with the STALE
      pre-splice prev/next and asserts the splice survives instead of
      being stomped. Confirmed non-vacuous via `git stash` on
      `btree_vacuum.go` alone (fails pre-fix with the exact "stale stomp
      regression" symptom the test asserts against). Design doc
      `docs/design/0055-0003-btree-page-deletion-and-recycling-protocol.md`
      new §2.5; `docs/design/README.md` row extended. Gates: `go build
      ./...` clean; `go test ./internal/access/btree/...
      ./internal/amcheck/... ./internal/executor/...` PASS; `go test
      -race ./internal/access/btree/...` PASS; `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33). **New gap found while fixing the above,
      deferred (ledger row appended 2026-07-09):**
      `applyParentDownlinkRemoval` (shared by both the leaf and
      internal-page unlink WAL paths) removes the parent's downlink
      purely by a previously-captured slot INDEX, with no re-validation
      at write time that the item still at that index is the intended
      child's downlink — the exact index-drift race
      AI-20260706-201855-001 fixed for the intra-instance case (there
      `splitMu` closed it), but NOT for a concurrent split racing from a
      DIFFERENT connection's instance on the same parent page. This is
      the epic's next concrete resume point (see the ledger row's
      "resume point" column for the exact fix shape); the larger
      `splitMu` removal / Lehman-Yao crab-walk items in this bucket
      remain untouched by this loop.
      **2026-07-09 loop (same day, continuation) — fixed the
      `applyParentDownlinkRemoval` index-drift race named above.**
      Changed the function's signature from
      `(parentBlk storage.BlockNumber, removeSlot uint16, lsn
      storage.LSN)` to `(parentBlk, childBlk storage.BlockNumber, lsn
      storage.LSN)`: instead of trusting a slot index resolved well
      before the removal actually runs (WAL emission + sibling-relink
      writes happen in between), it now re-scans the parent's CURRENT
      item list for `it.ptr.Block == childBlk` under the same `pinW`
      that performs the removal — mirrors the §2.5 sibling-relink fix
      pattern and `findParentDownlinkByBlock`'s existing by-block
      matching, self-correcting if a cross-connection split raced in,
      idempotent no-op if the downlink was already removed by a racing
      unlink. Both call sites (`unlinkEmptyLeaf`'s and
      `unlinkEmptyInternalPage`'s WAL-emitting paths, lines ~408/~981)
      now pass the child block (`leaf.blk`/`blk`) instead of
      `req.ParentRemoveSlot`; the WAL record's own `ParentRemoveSlot`
      field is untouched (crash replay is single-threaded, so the
      stale-index concern is live-apply-only). New regression test
      `TestApplyParentDownlinkRemovalIgnoresStaleIndex`
      (`internal/access/btree/btree_vacuum_parent_downlink_race_test.go`)
      deterministically reproduces the drift (no goroutines needed):
      resolves a target leaf's parent slot on a real 2-level tree
      (`BulkCreate`, n=3000), splices a synthetic live downlink into
      the front of the parent's item list (shifting the target's true
      position by one, so the pre-splice stale slot now points at a
      different, live "victim" child), then invokes the fixed removal
      keyed on the target's block and asserts: the target's downlink is
      gone, the victim's downlink survives (proving no
      wrong-item-by-stale-index deletion), and the spliced item
      survives untouched. Confirmed non-vacuous via `git stash` on
      `btree_vacuum.go` alone — the test fails to even COMPILE pre-fix
      (`cannot use targetBlk (BlockNumber) as uint16 value`), a stronger
      signal than a runtime assertion failure. Design doc
      `docs/design/0055-0003-btree-page-deletion-and-recycling-protocol.md`
      new §2.6; `docs/design/README.md` row updated. Deferral ledger row
      dated 2026-07-09 (`M0122-0010`, "applyParentDownlinkRemoval...")
      flipped to `resolved`. Gates: `go build ./...` clean; `go test
      ./internal/access/btree/... ./internal/amcheck/...
      ./internal/executor/...` PASS; `go test -race
      ./internal/access/btree/...` PASS; `scripts/tpch-spotcheck.sh`
      PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
      scripts/ralph-precommit-test.sh` PASS (0 failed txns, all 3
      workloads). **Standing gap unchanged (not this loop's scope):**
      `bt.splitMu` is still not a real cross-connection mutex — this
      fix (like §2.5's) tolerates that by re-validating at the
      individual write site; the larger `splitMu` removal / Lehman-Yao
      crab-walk items in this bucket remain untouched.
- [ ] **M0122-0012 — Perf infra: vectorization / slot-pipeline / harness** (~19,
      ARCHITECTURAL). Borrow-semantics allocation rewrite, plannode migration,
      vectorized FilterOp/SeqScanOp, plan cache, HammerDB SF1 validation.
- [ ] **M0122-0013 — Physical/streaming replication & standby** (~10, EPIC/blocked).
      Streaming-replication epic (~25 sub-items), cascading replication,
      `STANDBY_SNAPSHOT_READY` transition.
- [ ] **M0122-0014 — Logical replication / decoding / subscription** (~11, EPIC).
      pgoutput DELETE identity, subscriber apply worker, DDL replication. Blocked
      on logical decoding (tracks D-004; overlaps M0119-0007 — dedupe).
- [ ] **M0122-0015 — Test-suite porting: amcheck / verify_heapam / pg_dump** (~8).
      `verify_heapam()` SRF + opclass parity, AC-002..005, pg_dump 002-010.
      **Overlaps M0119-0004/0006 — the triage assigns each item to ONE milestone;
      do not double-work.**


## M0131 — Bidirectional cluster-directory cold-start + real-PG system-view hosting (filed 2026-08-11)

> **Demoted from top priority by the 2026-08-13 user directive** — M0132 is now
> the next-priority milestone after M-NIGHTLY. This section sits at the end of
> the file for append-safety only — **document order does NOT reflect
> priority.** The `## Current Priority` banner at the top of this file is the
> sole ordering authority. Work M0131's remaining tasks after M0132, before any
> remaining M0130, M0119 or M0122 item.

**Milestone doc:** `docs/milestones/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
**Implementation plan:** `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
**Source:** M0130 Acceptance-bar item 1 (never discharged — every M0130 acceptance item was proven through the `pg_basebackup` lane, not a cold start); `docs/design/0130-0002-pg-class-heap-persistence.md` §"Remaining for full reverse-path parity" items 1-3 (no ledger rows, contrary to the filing rule); deferral-ledger rows #428, #490, #995, #996.

**Filing rule (inherited from M0130):** no task deferred without a ledger-recorded strong reason; subtasks inline in fix_plan; every non-trivial subsystem lands its design doc (draft → accepted) within M0131.

**Citation precedence:** the ten `0131-000N-*.md` sub-design docs re-verified every citation used here against the repo and the oracle. **Where a sub-doc and this section disagree, the sub-doc wins.** Corrections already folded in below: S1's blast radius is six unregistered GUC names, not four; S2 needs a new `ControlFileData` field decode before it can read pg_control; S4's "three deltas" reduce to one; `tupdesc.c:105` is an `elog(ERROR)` not a FATAL; `pg_stat_activity` and `pg_settings` swap S9 buckets. Ledger references written `#NNN` are LINE NUMBERS in `.ralph/deferral_ledger.md` — that file has no ID column.

**Three corrections this milestone carries (diagnosed at filing 2026-08-11):**
1. Ledger rows #428/#995/#996 blame *"a goopg-built `pg_internal.init` leaves `rd_rules` empty"* and prescribe populating it. **That fix is not expressible.** Upstream `load_relcache_init_file` (`postgres/src/backend/utils/cache/relcache.c:6443-6453`) sets `rd_rules = NULL` unconditionally ("Rules and triggers are not saved"); `write_relcache_init_file` never serialises them; views never pass `RelationIdIsInInitFile`; and `StartupXLOG` deletes every init file at `postgres/src/backend/access/transam/xlog.c:5633` before any backend reads one. The real causes are S5 and S6 below.
2. Those rows name index **2620**. 2620 is `pg_trigger`. The index `RelationBuildRuleLock` scans is **2693** (`pg_rewrite_rel_rulename_index`, `postgres/src/include/catalog/pg_rewrite.h:57`).
3. `copyInitFiles` (`internal/testport/e2e_failover_goopg_to_pg_test.go:808-844`, 3 call sites) is inert — its own adding commit `30b0716f` (2026-05-17, subject ends "add copyInitFiles workaround") admits *"PG's load_relcache_init_file still rejects the file silently"*, and it was superseded next day by `c09d519e` ("step 3cq proper"). S10 deletes it.

Theme A — Reverse cold start (goopg on a PG-initdb'd directory):
Theme B — Forward cold start (real PG on a goopg-created directory):
Theme C — Real PG hosted on goopg evaluates views (closes "goopg cannot host a real PG that reads any system view"):
Theme F — Findings measured by M0131-S4 (filed 2026-08-11; each is locked into `TestE2E_PGColdStartOnGoopgDataDir` in the FAIL-WHEN-FIXED direction, so landing one turns that assertion red until it is inverted):
Theme D — Hygiene:
Theme F — Crash-state cluster-directory interchange (added 2026-08-11, user directive; removes Themes A/B's clean-shutdown precondition):

**Why:** S3 and S4 both assert `DB_SHUTDOWNED` before handing the directory over, per `0130-0002` §"WAL replay constraint". Two engines are not interchangeable on a directory if the interchange only works when the previous engine exited politely. **Theme F's filing investigation found that each direction already LOSES COMMITTED DATA today** — S11 and S12 are live bug fixes, not features, and land before everything else in the theme.
**Bounds established at filing (do NOT re-plan these):** WAL segment zeroing is a non-issue — goopg zero-fills recycled segments (`internal/wal/writer.go:2369-2379`) where upstream's `InstallXLogFileSegment` does not, so goopg is strictly safer; `CheckRequiredParameterValues` is a no-op in crash recovery (every branch gated on `ArchiveRecoveryRequested`, `xlog.c:5429`/`:5442`); and empty `pg_twophase`/`pg_commit_ts`/`pg_multixact` are all fine forward (`PrescanPreparedTransactions` over an empty dir returns `nextXid`, which is what `StartupSUBTRANS` wants; `StartupCommitTs` is gated on `track_commit_timestamp=false`; `TrimMultiXact` succeeds because initdb's zeroed `pg_multixact/offsets/0000` placeholder is load-bearing). Unlogged relations having no `_init` fork is a "too durable" divergence, not corruption — ledger it, do not absorb it here.
**Theme design:** `docs/design/0131-0012-crash-state-cluster-dir-interchange.md`. Theme F is independent of Themes C/D and may proceed in parallel.

- [ ] **M0131-S24 — MultiXact: durable `pg_multixact` SLRU + `multixact_redo`** (est ~4 loops, **LARGE/RISKY, deferrable — decide explicitly**). RM_MULTIXACT_ID (6) is the **only genuinely unavoidable missing rmgr**: independent of `wal_level` and produced by ordinary concurrency — two sessions taking `FOR SHARE` on one row; `FOR UPDATE` + `FOR KEY SHARE` from different xacts; UPDATE of a row already locked by a live xact; **two concurrent sessions inserting children referencing the same parent row** (FK RI checks take `FOR KEY SHARE` — the commonest real-world source); and VACUUM via both `heap_prepare_freeze_tuple` and `TruncateMultiXact`. goopg has a real in-memory engine (`internal/multixact/multixact.go`, `store.go`) but it is process-local and transient; `pg_multixact/{offsets,members}` are created empty at initdb and never written. Needs a durable offsets+members SLRU modelled on `internal/mvcc/clog_bufferpool.go` — but its 2-bits-per-key locate math does **not** generalise to variable-length member runs, and there is no shared SLRU abstraction (`internal/mvcc/subxact_slru.go` already duplicates the constants; extract one rather than writing a third hand-roll). Then declare `RmgrMultiXact = 6` and port `multixact_redo` (`postgres/src/backend/access/transam/multixact.c:3481`, 4 opcodes), **carrying the `pre_initialized_offsets_page` flag across records** (`multixact.c:383`, `:969`, `:3500`, `:3539`) — skipping it double-zeroes a live offsets page. **Two findings worth ledger rows regardless of whether this lands:** (a) goopg's emit side stamps multi xmax with NO WAL record (`internal/executor/operators_lockrows.go:2040`, `:2126`; `operators_storage.go:3468`, `:3485`) — defensible for lock-only multis, NOT for the updater-bearing producers, so goopg's OWN crash recovery has this defect today; (b) `internal/mvcc/visibility.go:126-146` makes a tuple with an unresolvable multi xmax **invisible**, so a PG-authored multi xmax silently hides rows rather than erroring. **Scoping DECISION (recorded, not omitted): S28's workload is single-session, so S24 is DEFERRED out of M0131** — a single-session PG workload emits no multixact record, so S16-S23 + S25 suffice for S28. The re-arm trigger is executable: S28 ships a third `..._concurrent` variant (two sessions `FOR SHARE` on one row + concurrent FK inserts) carrying `t.Skip("re-arm trigger for M0131-S24")`; un-skipping it re-opens S24. **DEFERRAL CLOSED 2026-08-12 (loop #43): three ledger rows filed under `M0131-S24`** — (1) the S24 deferral itself with its executable re-arm trigger; (2) the producer-side gap, **corrected on verification at HEAD**: finding (a) above is wrong that all four sites emit no record — only the two row-lock sites are `MarkDirtyUnlogged`; `stampUpdaterXmaxNonHOT` / `carryForwardLockersToNewTuple` (now `operators_storage.go:3626`/`:3643`) DO ride a logged delete/update whose `xmax` is `effectiveWriterXID` and whose `infobits_set` is hardcoded 0, so redo clears `HEAP_XMAX_IS_MULTI` and silently drops the preserved lockers — pinned by `internal/wal/multixact_producer_redo_gap_test.go` (2 tests, fail-when-broken); (3) the consumer-side hazard, with the stale "unreachable today" comment at `internal/mvcc/visibility.go:126` corrected in place (it is live after every restart since S20.4). The item stays **unchecked** — ledger row + unchecked item is the only permitted form of deferral. **Original scoping note:** the trigger is concurrency — if the S23 workload is single-session this can be deferred with a clear re-arm trigger; decide explicitly, not by omission. Design: `docs/design/0131-0016-multixact-durable-slru.md` (status: accepted — S24 deferred).
## Archived — complete (see `completed_milestones/completed_fix_plan_012.md`)

M0132 (Explicit transactions across the extended query protocol), M0133 (information_schema on disk).

## M0134 — regress-sql `failed`/`not-tried` test-case digestion (filed 2026-08-15)

**Priority: next after M-NIGHTLY (user directive 2026-08-15).** The `## Current
Priority` banner names M0134 immediately after M-NIGHTLY's standing filing
obligation; work M0134 tasks right after M-NIGHTLY's regression fixes, ahead of
M0119 and M0122's remaining items. Milestone doc:
`docs/milestones/0134-regress-sql-failed-not-tried-digestion.md`.

**Per-task discipline (binding, from the milestone doc):**
1. **Design note when a task is selected.** Before implementing, write a design
   note under `docs/design/<task-id>-NNNN-short-slug.md` (`draft` → `accepted`)
   recording the SQL surface, the goopg↔PG 18.3 divergence, the root cause, and
   the PG-oracle citation; index it in `docs/design/README.md`.
2. **Update the inventory CSV when status changes.** When a task's
   implementation changes a case's `status`, update the row in
   `docs/test-port/postgres-oracle-target-inventory.csv` in the same commit:
   **if the status changes to `pass`, the `pass_required` column must also be
   set to `yes`** (in addition to `status → pass`, `rationale` naming the
   verification). Run `make check-testport-inventory`.
3. The four `failed` cases flagged "possible regression, verify" (`mvcc`,
   `reindex_catalog`, `select_having`, `select_implicit`) are re-run at HEAD
   first; a stale pass is flipped to `pass` with a note, not implemented against.

189 cases, one task each. **Filed** as 87 `failed` (M0134-0001..0087, CSV order)
then 102 `not-tried` (M0134-0088..0189, CSV order); **RENUMBERED 2026-08-19 by user
directive** (see below). Per-case gate: `scripts/pg-regress-runner.sh <case>`.

**Priority renumbering — 2026-08-19 (user directive).** Eighteen cases the user
named as higher-value were pair-swapped into the block **M0134-0006..0023**, and
the sixteen tasks they displaced took the vacated numbers in ascending order. The
other 155 tasks keep their filed numbers. **The consequence, stated so no later
loop assumes otherwise: the "`failed` = 0001..0087 / `not-tried` = 0088..0189"
band invariant no longer holds** — e.g. `select_parallel` (`not-tried`) is now
0008 and `float4` (`failed`) is now 0153. Each task line carries its own
`` `failed` ``/`` `not-tried` `` word, which is the authority; the ID band is not.
The new 0006..0023, in the order the user listed them: `select_having` (was 0066),
`select_implicit` (0067), `select_parallel` (0166), `select_views` (0068),
`predicate` (0153), `subselect` (0071), `update` (0082), `insert` (0033), `mvcc`
(0048), `join` (0036), `create_table` (0010), `hash_index` (0027), `create_index`
(0008), `indexing` (0031 — the user's "index.sql", which does not exist upstream),
`stats` (0171), `vacuum` (0084), `window` (0085), `write_parallel` (0187). The
listed `select.sql`, `delete.sql` and `sysviews.sql` already carry CSV status
`pass` and so have no M0134 task to promote. Full old→new table:
`docs/milestones/0134-regress-sql-failed-not-tried-digestion.md`.

- [ ] **M0134-0001 — aggregates.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
      **Residual re-scoped 2026-08-17 — the previous "all 30 hunks are parallel-query (S10)"
      attribution was WRONG.** A hunk-by-hunk re-classification at HEAD f3c148c3
      (`tmp/ralph-handoffs/m0134-0001-s10-scope/report.md`) found only **5 of 30** hunks are
      parallel-query, and parallel query is an already-complete, measured milestone
      (`docs/design/parallel-query/IMPLEMENTATION-TODO.md`, P0-P10 all `[x]`) — so **no new
      milestone is needed**. Current buckets: deparser/`pg_get_viewdef` (C11c) 8; S6
      min/max-InitPlan 5; brand-new isolated bugs 4; enable_hashjoin/nestloop + incremental
      sort + dup-filter 3; qualification rendering 3; join-shape/func-dep 2 (one is a real
      correctness bug — goopg **over-permits** a GROUP BY that PG rejects with 42803,
      diff line 391); parallel 5 (2 already-ledgered S10b/Append-recursion, 2 a NEW
      **over-eager** parallelization bug where goopg adds a Gather that PG's small-table size
      rule excludes, 1 the ledgered `balk` user-combinefunc case).
      **S12 landed** (`bytea_output='escape'`, design `docs/design/0134-0001-p4-bytea-output-escape.md`):
      fixed the GUC plus a pre-existing COPY TO bug that wrote raw unencoded bytes for bytea.
      The case gate did **not** move (1096 lines / 30 hunks before and after) — hunk 18 is
      blocked by an independent untyped-literal delimiter drop in `string_agg`, ledgered
      2026-08-17.
      **S13 landed 2026-08-17 — 42803 GROUP BY over-permit** (design
      `docs/design/0134-0001-p5-groupby-name-resolution.md`): `select t1.f1 from t1 left join
      t2 using (f1) group by f1` returned `(0 rows)` where PG rejects it with 42803. goopg's
      downstream guard (`resolveExprAfterAggregate`, M0097-0155) was already CORRECT but
      **starved of its input signal** — `buildAggregateStage` ran the shared ORDER-BY alias
      substitution on every GROUP BY item (`planner.go:6515`), rewriting the bare `f1` into the
      target list's `t1.f1` *before* the USING-merge tracking loop (`:6554`, which requires
      `cr.Table == ""`) could see it, so `groupByMergedByName` stayed empty. PG resolves GROUP BY
      names in the OPPOSITE order from ORDER BY: `parse_clause.c:2056-2076
      findTargetlistEntrySQL92` gates the alias match on `EXPR_KIND_GROUP_BY` via `colNameToVar`
      — the **FROM-clause column wins**, the target-list alias is only a fallback. Fix = a new
      `groupByNameIsInputColumn` name-*visibility* probe gating the single GROUP BY call site;
      the seven ORDER BY / DISTINCT ON call sites are already correct and stay untouched.
      Diff **1096→1079 lines, 30→29 hunks** (owning hunk `@@ -1363,13 +1287,13 @@` closed);
      blast-radius sentinels `functional_deps` 56→56 and `groupingsets` 2373→2373
      byte-identical. Also corrects alias shadowing (`SELECT a AS b … GROUP BY b` now binds
      `t.b`; under strict PG semantics that query then errors 42803 — verified as real PG
      behaviour, not a goopg bug). Positional GROUP BY and genuine alias GROUP BY (TPC-H Q7)
      are unaffected by construction. Guards:
      `internal/optimizer/groupby_from_column_priority_test.go` (8 tests, FAIL-pre/PASS-post).
      Highest-leverage next slices: the
      `string_agg` delimiter coercion (closes hunk 18), and the over-eager-parallelization
      size-threshold audit in `computeParallelWorkers`.
  - Progress (see `docs/design/0134-0001-p2-explain-format.md`): S1/S2/S4/S5/S6/S7/S10a landed; **S3 (class 7a) landed 2026-08-15** — join labels now PG-interpolated (commit `aa71b24d`). **S8 (class 6) landed through Slice 2b 2026-08-15**: Slice 1 = sorted GroupAggregate executor (`AggStrategySorted` + `openSorted`); Slice 2a = presorted-aggregate planner rule (`applyPresortedAggregateRule`, port of `adjust_group_pathkeys_for_groupagg`); Slice 2b = `enable_hashagg` bridge (`applyEnableHashAggRule`, `SET enable_hashagg=off` → `GroupAggregate`+`Sort`). class 10 blocked by the varno deferral (ledger 615). **S8 Slice 2c-i landed 2026-08-17** — `applyIndexOrderedGroupingRule` (`internal/optimizer/groupagg_indexorder.go`): when the GROUP BY keys are a permutation of an exact leading prefix of a usable btree index, the `*SeqScan` child becomes an ascending full-range `*IndexOnlyScan`/`*IndexScan`, `Strategy` becomes `AggStrategySorted`, and **no `Sort` is inserted**; PG's reordered `Group Key:` line comes from a new EXPLAIN-only `Aggregate.GroupKeyOrder` (`GroupExprs` is never permuted — the output bindings are positional). Diff 1311/44/661 → **1296/44/651**. Measurement re-attributed the old "Rule 3" scope (3 ledger rows 2026-08-17): the rule is gated on `enable_hashagg=off` pending a cost comparison; **Slice 2c-ii (partial prefix ⇒ needs an Incremental Sort node goopg lacks entirely — 5 of the 7 `btg` EXPLAINs)** and **Slice 2c-iii (ORDER-BY-aware ordering choice)** are DEFERRED; and `enable_hashjoin`/`enable_nestloop` non-honoring, join-aware functional-dependency GROUP BY reduction, and a duplicate residual `Filter` alongside an `Index Cond` were found to be separate gaps, not Rule 3 — while `agg_sort_order` is not a plan divergence at all (only the EXPLAIN underline-width formatter). **S11 landed 2026-08-17 — PG-faithful cumulative EXPLAIN indentation** (`internal/executor/operators_explain.go` + `explain_cte.go`): the "underline-width cosmetic gap" was a MISDIAGNOSIS — both `walkPlanFiltered` and its ANALYZE twin `walkPlanAnalyzeFiltered` computed the prefix as flat `strings.Repeat("  ", depth)`, while PG threads a cumulative `es->indent` (`explain.c:1616-1635 ExplainNode`; `->` marker at raw cols 0/2/8/14, deltas 2/6/6). The models coincide at depths 0-1, which is why the whole S8 line rendered byte-identical and the bug survived five slices; it reached the diff only because `psql` computes the header underline from the RAW widest cell while `pg-regress-runner.sh`'s `normalise_output()` collapses space runs (ledger row: the regress gate is blind to whitespace-layout divergence). Two corrections: PG's `plan_name` (CTE/InitPlan) branch bumps indent by only **+1** (verified against live PG + `rowsecurity.out:3333-3336` — the existing `explain_cte_test.go` assertion of 4 spaces was already right and was kept), and the VERBOSE `Output:` line carried a second independent indent bug, now on `detailIndent`. Diff **1296→1096 lines, 44→30 hunks, 14→0 dash-only**; the 30 remaining hunks are all S10 parallel-query plan shapes. Guards `TestExplainIndentDeepNesting` / `TestExplainAnalyzeIndentDeepNesting` (twin) / `TestExplainIndentInitPlanBranch` in `internal/executor/explain_indent_test.go`. Cross-case: every M0134 case emitting a nested EXPLAIN inherits this, `explain.sql` (M0134-0017) most directly. Remaining: S10 parallel-agg, those re-attributed gaps, scalar-subquery nesting, inheritance MergeAppend (dead-end). **S15 landed 2026-08-17 — plain `SET` is now reverted by transaction ABORT** (`docs/design/0134-0001-p6-guc-transaction-rollback.md`): the "parallel bucket" was a MISATTRIBUTION for its two largest hunks, the milestone's second such (cf. S11). `aggregates.sql:1448-1488` sets five parallel GUCs with plain `SET` inside a `BEGIN; … ROLLBACK;`, and goopg leaked all five past the ROLLBACK — so the later `agg_data_20k` EXPLAINs (sql:1544/1580) over-parallelised with a stale `min_parallel_table_scan_size=0`. `computeParallelWorkers` was correct for the session state it believed it was in; the bug was in `internal/utils/misc/session.go`, which journalled only `SET LOCAL`. PG stacks plain `SET` too (`guc.c set_config_option`/`GucStack`) — the two differ at COMMIT, not at ABORT (`xact.c` → `AtEOXact_GUC(false, …)`). Fix: `Context.EndLocalTransaction` becomes `func(committed bool)` (all 4 call sites + both wiring closures pass their honest verb; `endExplicitBlock` reuses its documented `undoEnumDDL` invariant), plus a `txPrior map[string]*string` undo journal restored by `EndTransaction(false)` — nil prior = DELETE the key, not write `""`, and the restore re-fires `onReportableChange` only on a real move so a rolled-back `SET DateStyle` can't desync `ParameterStatus`. Diff **1029→1001 lines, 30→29 hunks**; both predicted hunks closed. 9 new tests incl. a SQL-level E2E guard for the hook rewiring; sentinels byte-identical. Three ledger rows 2026-08-17: savepoint/subxact GUC granularity DEFERRED (flat journal — `ROLLBACK TO SAVEPOINT` does not revert GUCs; oracle `AtEOSubXact_GUC`); the over-broad `subtreeHasUnsafeNode`/`AggregateIsOrderSensitive` whole-statement parallel veto (PG only refuses the SPLIT — narrowing it closes the `array_dims(array_agg(s))` hunk, the `v_pagg_test` one additionally needs the S10b combine functions); and an EXPLAIN `Group Key:`/`Sort Key:` under-parenthesisation of non-Var keys (`((g % 10000))` vs `(g % 10000)`). **S16 landed 2026-08-17 — an order-sensitive aggregate refuses its SPLIT, not the whole plan** (`docs/design/0134-0001-p7-parallel-aggregate-veto.md`), executing the second of those three rows. `subtreeHasUnsafeNode`'s `*Aggregate` arm (`internal/optimizer/parallel.go:184-194`) suppressed every `Gather` in the statement whenever an undecorated `array_agg`/`string_agg`/`json*_agg`/`xmlagg` appeared anywhere — both redundant (the split is refused independently by `aggregateSplitIsSafe` → `AggregateIsDecomposable`, which the veto was never even wired to) and too strong (PG's `max_parallel_hazard_walker`, `clauses.c:827-970`, has NO `Aggref` order case; `Gather` is documented to destroy input order, `parallel.sgml:101-104`). Arm deleted; `*LockRows`, the DML/DDL whole-plan refusals and the four `tableIsUnsafeForParallel` arms stay. **The predicted line-count win did NOT materialise and that is the slice's real output:** `aggregates` **1001 → 999 lines, 29 → 29 hunks**. The structural claim is nonetheless verified by the hunk's own context — `Aggregate`, `Gather` and `Workers Planned: 2` all moved from divergence into unchanged context — and what survives is ONE line, `Parallel Seq Scan` vs `Seq Scan`, a **renderer** gap in the P2 formatter family, not a planner one: PG prefixes `"Parallel "` whenever `plan->parallel_aware` is set (`explain.c:1630-1631`, beside the sibling `"Async "` prefix) and goopg's EXPLAIN walkers have no such prefix. That surface was unobservable before this slice — with the veto in place no `Gather` was ever planted, so there was no scan-below-a-Gather to mislabel. Third bucket-verification lesson of the milestone, and the first where the bucket label held: after S11 ("underline width") and S15 ("parallel planner") were both plan-shape misattributions, this one was right about the cause and wrong only about how much of the diff sat behind it. `AggregateIsOrderSensitive` deliberately retained as documented zero-caller code for the deferred S10b split gating. Also relaxed `TestPartialAggregateRefusals` (`internal/executor/parallel_agg_split_test.go`) to sorted-multiset comparison for its `string_agg`/`array_agg` subtests only — with a `Gather` legal below the unsplit `Aggregate`, element order is genuinely nondeterministic (two orderings observed from consecutive runs of one binary) and positional equality would pin a property PG does not offer; `isSplit`-false, the exact row count, and `count(DISTINCT v)`'s exact comparison all stay. Gates: UNITS PASS, `scripts/tpch-spotcheck.sh` PASS Q12=2/Q13=35, sentinels byte-identical (`functional_deps` 56, `groupingsets` 2373). 2 ledger rows 2026-08-17: the `"Parallel "` label prefix, and the untested `array_agg(v ORDER BY v)` decorated form. **S17 landed 2026-08-17 — the `"Parallel "` node-label prefix** (design: the P2 doc's S17 section), executing that row. PG's `ExplainNode` prints the token generically for any node kind (`explain.c:1630-1631`, before the `pname` append, beside the sibling `"Async "`), but `parallel_aware` is set only at PATH construction — `create_seqscan_path` (`pathnode.c:996`, gated `parallel_workers > 0`), `create_bitmap_heap_path` (`:1115`), `create_append_path` (`:1338`), and `create_hashjoin_path` (`:2861-2862`, the shared-hash BUILD node; the join itself is never `parallel_aware`). So **"any node below a `Gather`" is the wrong rule** — PG admits a single-copy `Gather` over a non-partial subtree whose scan takes no prefix — and a render-time inference was rejected; it would also have duplicated `drivingScan`'s traversal. Implementation: `Parallel bool` on `SeqScan`/`BitmapHeapScan` only (the two types `drivingScan` recognises; `IndexScan`/`IndexOnlyScan` deliberately skipped as dead weight until parallel index-scan eligibility exists), stamped once by `stampParallelScan` — `drivingScan`'s copy-on-write sibling, repeating that traversal exactly and honouring `parallel.go`'s non-mutating discipline (identical pointer back when no eligible scan is reached) — applied at **both** `rebuildWithGather` branches and `splitAggregate`'s partial side, then rendered in `describePlan`. **The brief's premise was wrong about the twin:** `describePlanVerbose`'s `*SeqScan` case has three independent `return`s and does NOT fall through to `describePlan`, so `EXPLAIN VERBOSE` would have kept rendering a bare `Seq Scan` — shipping that would have CREATED a divergence rather than preserved one. The implementer caught it; both cases now carry the prefix plus reciprocal sibling-pair comments (same failure mode as S11's two walkers). Measurement: `aggregates` **999 → 981 lines, 29 → 28 hunks**, the `array_dims(array_agg(s))` block gone from the diff entirely — the pure-label thesis confirmed, and the class exists beyond this case as predicted: `select_distinct` **304 → 301** with its `Gather → Limit → Parallel Seq Scan` shape now matching. Guards `TestExplainParallelSeqScanLabel` + its ANALYZE and VERBOSE twins (`internal/executor/parallel_label_test.go`) and `TestStampParallelScanIsNonMutating` (`internal/optimizer/parallel_test.go`), all FAIL-pre/PASS-post; `TestMaybeAddGatherSharesUntouchedSubtrees`'s pointer-identity assertion was relaxed to the invariant that actually holds (the caller's tree is unmutated) since copy-on-write legitimately breaks identity, with a do-not-revert comment. Gates: UNITS PASS, sentinels byte-identical (`functional_deps` 56, `groupingsets` 2373), `scripts/tpch-spotcheck.sh` PASS Q12=2/Q13=35. 3 ledger rows 2026-08-17: `BitmapHeapScan` has no EXPLAIN label case at all (renders via the Go `%T` fallback, so its correctly stamped flag has no reader), the non-text `"Parallel Aware"` JSON property (`explain.c:1652`, emitted on EVERY node so it is a JSON-format-wide change), and the absent parallel index-scan eligibility that makes the `balk` block's `Parallel Index Only Scan` unreachable. **S18 landed 2026-08-17 — `Sort Key:`/`Group Key:` parenthesisation is a *reference* property** (design: the P2 doc's S18 section), executing the third of S15's rows. PG's `show_sort_group_keys` (`explain.c:2767-2823`) adds nothing of its own; the extra pair comes from `ruleutils.c get_special_variable`'s "force parentheses for a non-Var referent", reached only when the key is an `OUTER_VAR` chased into a child's target list by `resolve_special_varno` — so it marks WHERE the value is evaluated, not what the expression looks like. The corpus proves both directions for one expression: `aggregates.out:3464-3465` `GroupAggregate` over a `Sort` prints `((g % 10000))`, `:3500-3501` `HashAggregate` over the scan prints `(g % 10000)`. "Always add a paren" would therefore have been wrong — the fourth time this milestone that the obvious rule was the wrong one. goopg has no varno machinery (standing class-10 deferral, ledger row 615), so the rule is approximated structurally at the two emitters in `internal/executor/operators_explain.go`: `*optimizer.Sort` wraps every non-`ColumnRef` key unconditionally (a Sort never evaluates), `*optimizer.Aggregate` wraps iff `p.Child` is a `*optimizer.Sort`. The wrap goes INSIDE the `DESC`/`NULLS`/`COLLATE` decoration (`Sort Key: ((g % 10000)) DESC`). New `forceParen` helper kept deliberately distinct from the existing idempotent `wrapParen`, which would have silently no-opped on an already-parenthesised `OpExpr` — the trap the scoping research flagged in advance. Sibling pair honoured (one PG call site, two goopg emitters, reciprocal comments); the grouping-sets branch emits no key detail lines at all and was left untouched. Measurement: `aggregates` **981 → 963 lines, 28 → 27 hunks**, better than the predicted ~968; blast radius measured across 8 cases (`groupingsets`, `functional_deps`, `select_distinct`, `subselect`, `union`, `window`, `partition_aggregate`) with **net −18 and zero growth**. Guard `internal/executor/explain_sortgroup_paren_test.go` (5 subtests incl. the `HashAggregate` carve-out and an ANALYZE/VERBOSE agreement check), 3 FAIL-pre/PASS-post. Gates: UNITS PASS, `scripts/tpch-spotcheck.sh` PASS Q12=2/Q13=35. 3 ledger rows 2026-08-17: the structural proxy's two blind spots (a non-`Sort` pass-through under the Aggregate ⇒ under-wrap; the corpus's rarer single-wrapped `Sort Key: (COALESCE(t3.q1))` ⇒ over-wrap), the absent per-set grouping-sets key lines that must adopt this rule when M0125-0048 adds them, and the discovery that `scripts/pg-regress-runner.sh` is **not run-to-run deterministic** for `groupingsets` (2373-2377) and `subselect` (2845-2846) on an unchanged tree — only `functional_deps` (56) is a trustworthy sentinel. **Next slices:** the deparser/C11c bucket (8 hunks — confirmed `ruleutils.c`-grade, its own milestone), S6 min/max-InitPlan (5 hunks — `rewriteMinMaxAggregates` at `planner.go:8814` already ports `planagg.c` faithfully but its gate at `:8842-8848` bails on `OrderBy`/`Distinct`/multi-target), the new unledgered VERBOSE `Output:` column-pruning gap (2 hunks), and the isolated-bug residue. **S19 landed 2026-08-17 — ORDER BY / SELECT DISTINCT never gated the min/max InitPlan rewrite** (design: the P2 doc's S19 section), opening the S6 bucket. goopg's gate was **structural, not semantic**, and its own comment said so: `planSelect` early-`return`s on a successful rewrite (`planner.go:1258-1262`) ABOVE the shared ORDER BY sort (`~1428-1518`) and the plain-DISTINCT `Unique` wrap (`~1824-1855`), so a rewritten `Result` would have silently skipped them — a row-order/row-count bug, which is why the previous slices left the gate alone. PG cannot gate on them at all: `preprocess_minmax_aggregates` (`planagg.c:73-224`) is called from `planner.c:1617-1618` **before** `query_planner()`, while `sortClause`/`distinctClause` are consumed by the same generic `grouping_planner` tail whichever aggregation strategy was chosen. Fix: drop `s.Distinct` and `len(s.OrderBy) > 0` from the gate, re-attach the nodes at the call site via `wrapMinMaxOrderByDistinct` (bottom-up `Result -> Sort -> Distinct`, reusing planSelect's own `Sort{pos,Child,Keys}` / `Distinct{pos,Child,schema}` idiom rather than a second construction pattern). ORDER BY items are resolved against the InitPlan output column in exactly three forms — ordinal, the exact aggregate `FuncCall` (matched via the existing `parserExprKey`, the same key `aggregateSurface` uses), and an expression *containing* it (`max(unique2)+1`, substituted through `BinaryOp`/`UnaryOp`/`CollateExpr`). **The escape hatch is the design:** anything else — an unhandled wrapper node, `DISTINCT ON` — declines and falls through to the pre-S19 Aggregate path, so relaxing a correctness-load-bearing gate carried no proportional correctness risk. That is what separates it from Slice 3f, reverted for growing the diff. Measurement: `aggregates` **963 → 956 lines, 27 hunks (unchanged)**; all four target shapes now plan PG's `InitPlan 1 -> Limit -> Index Only Scan Backward -> Result`, and every data row printed in the corpus is byte-identical before and after — the executed-semantics proof. What still holds those hunks open is render-only: `Sort Key: max` vs PG's `Sort Key: ((InitPlan 1).col1)` (rides the class-10 varno deferral), and goopg's system-wide `Unique` where PG prints `HashAggregate / Group Key:`. Guard `internal/optimizer/minmax_distinct_orderby_test.go` (10 tests: 4 target shapes + `min()` mirrors for the forward/backward sibling pair, plus explicit decline assertions for multi-target, `DISTINCT ON`, and an unresolvable ORDER BY item — asserting the `*Aggregate` fallback, not merely "no crash"). Gates: `internal/optimizer` + `internal/executor` PASS, UNITS PASS, `scripts/tpch-spotcheck.sh` PASS Q12=2/Q13=35, `functional_deps` sentinel 56 unchanged. 3 ledger rows 2026-08-17: multi-target min/max still gated (and its only corpus witness, `minmaxtest`, is additionally blocked by the inheritance/`Merge Append` gap); `LIMIT`/`OFFSET` still gated for the same structural reason plus the per-aggregate `min(DISTINCT x)` reject PG explicitly does not make (`planagg.c:265`) — neither has a corpus witness; and the two render-only residuals above. **Next slices:** the deparser/C11c bucket (8 hunks — `ruleutils.c`-grade, its own milestone), the multi-target + inheritance/`Merge Append` min/max bundle, the VERBOSE `Output:` column-pruning gap (2 hunks), and the isolated-bug residue. **S20 landed 2026-08-17 — an ordered-set aggregate's collation comes from its `WITHIN GROUP` key** (design `docs/design/0134-0001-p8-ordered-set-agg-collation.md`). **The VERBOSE `Output:` candidate above was REFUTED by scoping and is NOT what landed:** its two witnesses are confounded by a much larger parallel-Append planning gap, and its own root is planner-side target-list pruning goopg lacks entirely (PG's `show_plan_tlist` deparses an ALREADY-pruned `Plan.targetlist`, `explain.c:2478-2486`, and separately suppresses `Output:` for `Append` outright, `:2449-2456`) — ledgered, not sliced. S20 took the smallest fully isolated hunk instead: `pg_collation_for(percentile_disc(1) within group (order by x collate "POSIX"))` returned `default` where PG returns `"POSIX"` — the milestone's first **semantics** slice since S15. PG's rule is general, not percentile_disc-specific: `assign_ordered_set_collations` (`parse_collate.c:918-943`) merges the sort key's collation into the aggregate's result **only when** `list_length(aggref->args)==1 && get_func_variadictype(...)==InvalidOid` (`:926-927`); with 2+ keys each key is collated in isolation (`:941`), which is exactly what lets `agg(...) WITHIN GROUP (ORDER BY x COLLATE a, y COLLATE b)` avoid erroring (PG's rationale comment `:901-916`). **Two wrong turns, both recorded:** (a) the obvious fix — a `foldPgCollationFor` case recursing into the aggregate's WITHIN GROUP keys — *cannot work*, and a probe proved it: by then the argument is already `ColumnRef{Name:"percentile_disc",Type:text}` and `SchemaColumn` has no collation field; (b) the design's first prescribed site was itself **dead code** for this query — `Plan()` routes the ENTIRE target list through `resolveExprAfterAggregate`, not `resolveExpr`, whenever any target contains an aggregate (`planner.go:4241-4247`), and `percentile_disc(...)` is one. Round 1 wrote the correct rule at the wrong address and correctly escalated rather than thrashing; `pg_typeof` turned out to be the exact structural precedent, already solved in that same function (`:7376-7392`, M0097-0035). Landed as ONE shared helper `foldPgCollationForWithinGroup` called from BOTH resolvers — an explicit sibling pair (the milestone's fourth after S11/S17/S18) rather than two copies that drift — with the mandatory decline-and-fall-through escape hatch (2+ keys, unresolvable key, non-`FuncCall` arg all fall back to the pre-S20 answer; never error, never guess). The executor path (`internal/executor/expr.go:8294-8320`) is deliberately NOT a twin and is unchanged, carrying only a pointer comment. Measurement: `aggregates` **956 -> 943 lines, 27 -> 26 hunks** (beat the ~953 prediction — closing the hunk collapsed its context block); sentinel `functional_deps` 56 unchanged. Guard: 5 new subtests in the existing table-driven `TestPgCollationForFolds` (`internal/optimizer/pg_collation_for_test.go`), 1 FAIL-pre/PASS-post plus explicit decline assertions and a `wantDeclined` mode. Gates: `go build ./...` PASS, `internal/optimizer`+`internal/executor` PASS, both regress gates as above. 3 ledger rows 2026-08-17: PG's non-variadic half of the merge condition not ported (no witness — every ordered-set builtin goopg has is non-variadic); the plain-aggregate `max(x COLLATE ...)` form still answering `default` (`assign_aggregate_collations`, `:880-899`) — declined by design but now pinned by a characterisation subtest; and the class-level finding that goopg's TWO target-list resolvers mean any `resolveExpr` special-cased builtin may lack a post-aggregate sibling, unaudited. Also corrected a scoping error the design first propagated: the conflicting-collation SQLSTATE is **42P21** (`errcodes.txt:352`), not 42P22 — goopg was already right. **Next slices:** the deparser/C11c bucket (8 hunks — `ruleutils.c`-grade, own milestone), the multi-target + inheritance/`Merge Append` min/max bundle, the plain-aggregate collation successor, and the isolated-bug residue (the `LINE 1:`/`^` pointer hunk and the NOTICE-ordering hunk). **S21 landed 2026-08-17 — a deferred coercion must still carry the literal's position** (design `docs/design/0134-0001-p9-agg-coercion-error-position.md`), taking the first of those two isolated-bug hunks. `select rank('fred') within group (order by x) from generate_series(1,5) x;` emitted its `ERROR:  invalid input syntax for type integer: "fred"` with NO `LINE 1:`/`^` pointer lines. The decisive clue was inside the hunk itself and is the reusable technique: the *sibling* query two lines below (`rank('adam'::text collate "C") …`, a 42P21 collation mismatch) already rendered its pointer lines correctly and was unchanged by the diff — so the position-rendering plumbing was never the defect and only ONE error's position was missing, which collapsed the search space before any code was read. Root cause: `buildAggregateCall` (`internal/optimizer/planner.go:8091`) defers a `text`/`unknown` direct arg against a numeric ORDER BY column to a runtime cast (`:8374-8379`) and built `&CastExpr{Operand: argExpr, TargetType: orderT}` without the type's **unexported** `pos` field (`plan.go:525-534`), leaving it 0 — which is goopg's own established convention for "suppress `LINE 1`" (`internal/executor/operators_ddl.go:3207,10043`, enforced by `operators_ddl_system_column_test.go:34`). The entire downstream chain (`CastExpr.Pos()` → `evalCast` → `if ee, ok := err.(*ExecError); ok { ee.Pos = pos }` at `internal/executor/expr.go:3536-3543` int4 / `:3560-3573` int8 → renderer) was already correct and already exercised; it was simply fed a zero. One-line fix, `pos: argE.Pos()` from the pre-wrap argument, matching the sibling `PlanError` twelve lines below at `:8386` exactly. PG never has the problem because it does not defer at all: `coerce_type` (`postgres/src/backend/parser/parse_coerce.c:157`) folds the literal to a `Const` at parse-analysis time and takes two deliberate steps to preserve position — `newcon->location = con->location` "regardless of the position of the coercion" (`:294-298`) and `setup_parser_errposition_callback(&pcbstate, pstate, con->location)` (`:300-304`). goopg reaches byte-identical output by a *different mechanism*, and the residual is ledgered rather than papered over (a shape that never evaluates the direct arg — zero rows, `WHERE false` — should error in PG and stays silent in goopg; no corpus witness, so the row carries the probe, not a confirmed failure). **Two process points did the real work.** (a) The single identified risk — whether the wire-protocol layer computes the caret column identically for a runtime `ExecError.Pos` and a parse-time `PlanError.Pos` — was named in the brief as blocking, with escalation mandated over any offset fudge, and was then **verified empirically** against `postgres/local_install/bin/psql` on a capped throwaway server: byte-identical for both errors, so the two origins do share one basis. (b) The twin search is recorded as a *positive negative* — one evaluator site (`internal/executor/expr.go:514` in `evalExprSlot`, shared by general expressions AND the hypothetical-set direct arg via `operators_join_agg.go:2588,2631`; there is no separate aggregate-argument evaluator, and the two other `*optimizer.CastExpr` cases at `:8050`/`:13980` never call `evalCast`) and one construction site whose two callers (`planner.go:6773` SELECT-list, `:6826` HAVING) share it. That is worth stating because four prior slices in this milestone (S11/S17/S18/S20) were each bitten by an unpaired twin; "searched and there is none" is a finding, not an omission. Measurement: `aggregates` **943 → 930 lines, 26 → 25 hunks** — the scoping prediction hit exactly, hunk #12 closing entirely with no adjacent hunk merging; sentinel `functional_deps` 56 unchanged. Guards `TestHypotheticalSetAggRuntimeCastErrorCarriesPosition` (FAIL-pre/PASS-post, verified by temporary revert) and `TestHypotheticalSetAggCollationMismatchCarriesPosition` in the new `internal/executor/hypothetical_set_agg_errpos_test.go`, the latter pinning the sibling 42P21 `*optimizer.PlanError` so the pair cannot drift apart. Gates: `go build ./...` PASS, `internal/optimizer`+`internal/executor` PASS, UNITS PASS, regress + sentinel as above. 1 ledger row 2026-08-17 (the parse-time-vs-runtime coercion timing). **Next slices:** the deparser/C11c bucket (8 hunks — `ruleutils.c`-grade, own milestone), the NOTICE trans-function ordering hunk (real, but a `nodeAgg.c`-style execution-order semantics gap, not string-only), the `Group Key:` qualification-under-`Append`/inheritance pair (hunks #8/#9 — small, root cause not yet traced), and the multi-target + inheritance/`Merge Append` min/max bundle. Ruled out by the S21 scoping pass: the plain-aggregate collation successor has **zero regress-diff witness** and would not move the hunk count. **S22 scoping 2026-08-17 — the `Group Key:` qualification pair (hunks #8/#9) is NO-GO, evidence in `tmp/ralph-handoffs/m0134-0001-s22-groupkey-qual/report.md`.** Baseline reconfirmed at HEAD `45cb67c0`: **930 lines / 25 hunks** (S21's prediction hit exactly). The gap is NOT one root cause but **three independent PG mechanisms**: (a) goopg's emitter (`internal/executor/operators_explain.go:778-816`) already reuses the shared `formatExprQual`/`qualify` helper — no missing plumbing; the bare rendering comes from `internal/optimizer/planner.go:3013,3059` assigning the **same `SourceTableIdx` to every leaf/child scan** of an inherited/partitioned table, so `qualify()`/`register()` (`internal/executor/explain_names.go:96-98,189-205`) count a multi-child `Append` as "1 relation", whereas PG's real rule is `useprefix = es->rtable_size > 1` (`postgres/src/backend/commands/explain.c:774-782`) over the **planned** range table that inheritance/partition expansion inflates; (b) hunk #9's correct qualifier `p_t1` names a relation existing **nowhere** in goopg's plan tree (no parent scan node for a partitioned table) — new plumbing required; (c) hunk #8 additionally needs PG's `inherit.c`-style positional Seq-Scan aliasing (`t1 t1_1`/`t1c t1_2`), unimplemented. A naive cardinality-test fix would trade recognizably-incomplete output for **plausible-but-wrong** output on #9 and still not close #8. Blast radius also entangles the already-failing `partition_aggregate.out` (1670-line diff), which uses the **opposite** per-partition-alias rule for partitionwise aggregation, tying this to the ruled-out class-10 varno bucket. Confirmed S18 (`c63e336d`) did **not** touch `explain_names.go` (paren-wrapping only) — no ready-made helper to extend. **S22 round 2 — the NOTICE trans-function ordering hunk (#14) is ALSO NO-GO, and the S21 map's hypothesis for it is REFUTED.** goopg's `1,3,1,3` order is **not a bug**: it is PG's own baseline (non-optimized) aggregate semantics, correctly implemented. PG's `1,1,3,3` is an emergent side effect of the **presorted-DISTINCT-aggregate optimization** goopg lacks — the planner sets `aggpresorted` (`postgres/src/backend/executor/nodeAgg.c:4260-4271`) via `adjust_group_pathkeys_for_groupagg` (`postgres/src/backend/optimizer/plan/planner.c:3199-3227`, GUC `enable_presorted_aggregate`), so the pertrans dedups INLINE in the single per-row scan instead of buffering + draining a tuplesort at group end. **Note the trap:** goopg's existing `applyPresortedAggregateRule` (S8 slice 2a) ports only the GROUP BY-pathkey-**ordering** half of that same PG function — it sets nothing equivalent to `aggpresorted`, so "we already ported `adjust_group_pathkeys_for_groupagg`" is false for this purpose. Payoff/risk inverted: closes ONE hunk (930 → ~917, 25 → 24) for a new cost-based planner feature **plus** a third code path through `internal/executor/operators_join_agg.go`'s `applyAgg` (`:2540-`)/`finishAgg` (`:3542-`) — the hottest shared aggregate-advance functions, on every GROUP BY/DISTINCT query including TPC-H/TPC-DS. Positively-verified asymmetry compounds it: **built-in DISTINCT aggregates already advance inline; only user-defined ones buffer-then-drain** (`:2975-3004`), so naive unification risks regressing built-ins. 2 ledger rows 2026-08-17. **Residual re-characterisation (the S22 loop's real deliverable): after S21 there are NO remaining small isolated slices in `aggregates`.** All 25 hunks are now either explicitly ruled out (deparser/C11c 8 hunks — own milestone; class-10 varno 1; VERBOSE-`Output:`/parallel 4; correlated-subquery SubPlan-absence 3) or confirmed large/confounded (min/max inheritance+`Merge Append` bundle; Incremental Sort — operator absent entirely; join-method selection; and now #8/#9 and #14). The next candidate, hunk #7 (multi-relation GROUP BY PK-pruning **confounded with a join-method swap** in one hunk), needs de-confounding before any fix can be proposed. **Recommendation: M0134-0001 has passed the point of cheap incremental wins — the remaining work is 3-4 genuine feature milestones, and the item should be re-scoped or parked rather than sliced further.**
- [ ] **M0134-0002 — alter_table.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-18 (not selectable) — re-measured 3968 lines / 111 hunks at `aa1f40ea`; a hunk-by-hunk re-classification of ~87% of the diff found NO bounded slice left.** The residual is a flat long tail: already-filed milestones (C17 `pg_locks` ~180, C18 EXPLAIN Append ~35, C16 ownership ~30, C11c deparser — which is also the real owner of the C7(a) `CHECK ((…))` double-paren lines ~70), three NEWLY FILED milestone-scale classes (**C20** ATTACH PARTITION validation family mostly absent ~120; **C21** non-table `SET SCHEMA` a silent no-op ~90, highest cascade; **C22** `SET LOGGED`/`UNLOGGED` unimplemented ~20), and ~15-line fragments not worth a brief (**C23** `ADD COLUMN NOT NULL` on non-empty ~15, C7(b) partition-child `_0_key` naming ~15, C9 duplicate-merged-column `\d` rendering ~15). Two corrections carried forward: C7(a) is NOT a paren-counting bug (PG also emits explicit casts like `10.2::double precision`), and C12 is NOT one formatter class (~20 independent one-off wrong strings, 1-3 lines each). **Re-arm trigger:** any of C11c / C17 / C20 / C21 landing as its own milestone — re-measure then. See the "PARKED (2026-08-18)" section of `docs/design/0134-0002-alter-table-sql-divergence.md`. Earlier progress: diff 4073→4048→4039→3981→3968. **C19 LANDED (2026-08-18)** — the `\d+` describe drift, and its premise was INVERTED: goopg *over*-produced the `Compression` column and `Access method:` footer because our regress runner never passed upstream `pg_regress`'s own `-v HIDE_TABLEAM=on -v HIDE_TOAST_COMPRESSION=on` (`pg_regress_main.c:74-79`) — a harness bug fixed in `scripts/pg-regress-runner.sh`, worth 4039→3981 here and yield across the WHOLE suite (`create_table` 831→791). The engine half: `pg_get_indexdef` ignored its `colno` argument and always returned the full `CREATE INDEX`; new `catalog.BuildIndexDefColumn` mirrors `ruleutils.c:pg_get_indexdef_worker`'s `attrsOnly` branch (3981→3968). **C16/C17/C18 FILED as classes (2026-08-18) and are each MILESTONE-SIZED, not slices** — C16 ownership/ACL checks absent entirely (`must be owner of ...` exists only in `database_ddl.go`), C17 `pg_locks` always empty (~180 lines, the largest class; lock machinery + view are both correct, most ALTER sub-actions simply never acquire), C18 no constraint exclusion at all (dead GUC stub) — do NOT attempt these as `alter_table` slices; see the ledger rows dated 2026-08-18 for resume points. Earlier: **C7 slice 1 LANDED** — an inline column-level `CONSTRAINT <name> CHECK (...)` now keeps its user-given name (`ColumnDef.CheckConstraintName`, parser + `execCreateTable`); three `RENAME CONSTRAINT con1` statements now succeed. **Residual re-characterised (2026-08-18, ledger row):** the remaining diff is NO LONGER dominated by the C7/C12/C13/C14 formatter tail — four classes outside the 14-class frame carry most of it (absent ownership/ACL checks; always-empty `pg_locks`; an EXPLAIN Append/constraint-exclusion *planner* gap; a `\d+` describe drift — missing `Compression`/`Access method`, Index `Definition` renders the whole `CREATE INDEX`). **Next loop should file these as C16–C19 in the design doc before slicing further.** Earlier progress: C1/C15/C8/C2/C3/C4/C5/C9/C10 landed; **C11a landed** (ALTER-on-view relkind guard, 42809). C11 was split — **C11b** (`to_json` / JSON-producing builtin family) and **C11c** (no `ruleutils.c` SQL deparser; `pg_get_viewdef` echoes raw text — the real cause of the mislabelled "CREATE OR REPLACE VIEW propagation" symptom, and also of the ledgered CHECK-constraint rendering gap) are DEFERRED with ledger rows; C11c warrants its own milestone. Remaining named work is the formatter tail (C7/C12/C13/C14) plus the C9 residuals in the ledger. See `docs/design/0134-0002-alter-table-sql-divergence.md`.
  - Progress (see `docs/design/0134-0002-alter-table-sql-divergence.md`): **Slice 1 landed 2026-08-15 (commit `dc8c0b9d`)** — server-crash fix: `viewColumnMap`'s bare-`*` arm now maps positionally over the frozen column count, so `update v2` (bug #17811) executes instead of panicking. Diff 4668→4671 lines / 44→81 hunks (the tail ~45% previously lost to the crash is now populated). Remaining 14 classes: C1 `text[]||text[]` op missing, C2 ALTER-TABLE grammar cluster (largest), C3/C4/C8/C9/C10/C11 correctness (C10 = data-loss on failed ALTER TYPE; C11 = rules-subsystem + read-path at_view_2 + top-level-* freeze, all ledgered), C5 btree-inet, C6 catalog gaps, C7/C12/C13/C14 formatter.
  - **C1 landed 2026-08-15 (commit `a3dad96f`)** — array `||` operator binding: the analyzer/planner `OpConcat` type-checks now accept array operands (both spellings) and return the array side's type (array_cat/append/prepend); executor + pg_operator seeds already existed. Surfaced **C15**: `pg_catalog.col_description()` builtin unimplemented — the second `\d+` describe blocker (12 errors remain; ledgered). Next in the `\d+` chain: C15, then C2 grammar.
  - **C15 landed 2026-08-15 (commit `044057b9`)** — `pg_catalog.col_description(oid,int4)` builtin: the executor function-name switch now has `case "col_description":` beside `obj_description` (`internal/executor/expr.go` :9840), reading `GetComment(1259, objoid, attnum)`; pg_description catalog + COMMENT ON already existed, only dispatch was missing (pg_proc seed OID 1216 pre-present). 12 `col_description` errors → 0, no new class, diff 4673→4677 (the previously-masked describe output now renders). Remaining: next `\d+` blocker, then C2 grammar (largest class).
  - **C8 landed 2026-08-15 (commit `0f6945bc`)** — system-column name rejection: a case-sensitive `isSystemColumn` helper (ctid/xmin/cmin/xmax/cmax/tableoid, no `oid`) now rejects at all four entry points (`execCreateTable`, `execCreateTableAs`, `execAlterTableAddColumn`, RENAME COLUMN arm) with 42701 + the PG-exact message; the RENAME check was corrected (42P20→42701, `oid` dropped, case-sensitive) and `validatePartitionKey` reuses the one helper. Diff 4677→4664 (−13). Deferral ledger row appended for the remaining DROP/ALTER-on-system-column gap (0A000, needs a system-column model or per-path guards). Remaining: C2 grammar (largest), then C3/C4/C9/C10/C11 correctness.
  - **C5 landed 2026-08-16 (commit `df3ee98b`)** — btree-inet: the full btree/inet_ops opclass stack was already seeded; the rejection was a hardcoded Go allow-list (`isSupportedBTreeKeyType`) missing `"inet"`/`"cidr"`. Added both to the allow-list plus a new order-preserving encoder/decoder arm (`encodeInetBTreeKey`/`decodeInetBTreeKey`, fixed-width `[family][masked-network-addr][bits][full-addr]` key reproducing `network_cmp_internal` byte-wise; cidr shares inet_ops via binary coercion). The expression-key gate routes through the same allow-list. 5 new table-driven tests incl. a 32-literal corpus byte-order==`network_cmp_internal` check + encode/decode round-trip. Diff 4664→4645, `got "inet"` 1→0 (the C5 PKTABLE block no longer emits the btree rejection; hunk count unchanged 84 — the block shares a hunk with the out-of-scope C4 FKTABLE lines). Deferral ledger row appended for the network_* comparison-operator Go bodies (predicate eval + FK = class C4). Remaining: C2 grammar (largest), then C3/C4/C9/C10/C11 correctness.
  - **C2 first slice landed 2026-08-16 (commit `8afe5bf7`)** — ADD COLUMN IF NOT EXISTS: parser consumes `IF NOT EXISTS` (`acceptKeyword(KwIf)`, mirrors the DROP-ATTRIBUTE pattern) + `AlterTableAction.IfExists` flag; executor `execAlterTableAddColumn` emits PG's NOTICE `column "c" of relation "r" already exists, skipping` and skips. Researcher decomposition (`0134-0002-c2-grammar-research`) split C2 into 14 sub-gaps (11 doc-listed + 3 new: ADD COLUMN IF NOT EXISTS, DROP CONSTRAINT IF EXISTS, ALTER TABLE OF/NOT OF), ~60 of 88 syntax-error lines. Diff 4645→4602, `expected identifier (got not)` 8→0. Remaining C2 sub-gaps ranked: NO INHERIT trailer, TYPE…USING (C10-entangled), RENAME CONSTRAINT (C7-partial), comma multi-action (structural), RENAME `<col>` TO bare, ANALYZE tab(col) (re-route — an ANALYZE/VACUUM statement gap, not ALTER TABLE), NOT VALID, STORAGE, OF/NOT OF, DROP COLUMN IF EXISTS, DROP CONSTRAINT IF EXISTS, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 second slice landed 2026-08-16 — NO INHERIT trailer** — parser's ADD [CONSTRAINT] CHECK arm consumes `NO INHERIT` at both orderings (`acceptIdentKeyword("no")`/`"inherit"`) + `act.NoInherit`; executor threads `act.NoInherit` into `AddCheckFull` and raises 42P16 `cannot add NO INHERIT constraint to partitioned table %q` on a partitioned target. 7 `(got no)` sites closed (80→73), partitioned ERROR byte-matches PG. Unmasked two C3-class gaps (ADD CONSTRAINT CHECK without NOT VALID does not validate existing rows; INSERT/UPDATE does not enforce CHECK) — recorded in the deferral ledger. Remaining C2 sub-gaps: TYPE…USING (C10-entangled), RENAME CONSTRAINT (C7-partial), comma multi-action (structural), RENAME `<col>` TO bare, ANALYZE tab(col), NOT VALID, STORAGE, OF/NOT OF, DROP COLUMN IF EXISTS, DROP CONSTRAINT IF EXISTS, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 third slice landed 2026-08-16 — RENAME CONSTRAINT** — new parser kind `AlterTableRenameConstraint` + `OldConstraintName` field (reuses `NewName`) + a RENAME CONSTRAINT arm (mirrors the DOMAIN arm); executor renames CHECK (OID-stable, partition-child cascade)/FK (slice mutation)/UNIQUE/PK/EXCLUDE (via `InMemory.RenameIndex` re-keying the backing index + `resyncIndexClassHeapRow`). Errors byte-match PG (42704/42710/42P07). Closes con2/con3/cache-pkey rename sites + `\d` `"con3foo" PRIMARY KEY, btree (a)`. The `onek` block (:294-296) stays open on a pre-existing `DROP INDEX` gap — `execDropIndex` silently drops a constraint-backed index where PG raises 2BP01 `cannot drop index … because constraint … requires it` (ledger row appended). Next C2 slice: the DROP INDEX constraint-guard (unblocks onek), then TYPE…USING (C10-entangled) or comma multi-action. Remaining: TYPE…USING (C10-entangled), DROP INDEX guard, comma multi-action (structural), RENAME `<col>` TO bare, ANALYZE tab(col), NOT VALID, STORAGE, OF/NOT OF, DROP COLUMN IF EXISTS, DROP CONSTRAINT IF EXISTS, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 fourth slice landed 2026-08-16 — DROP INDEX constraint-guard** — `execDropIndex` now raises 2BP01 `cannot drop index %s because constraint %s on table %s requires it` + HINT `You can drop constraint %s on table %s instead.` (unquoted names, PG `getObjectDescription` / `dependency.c:780-795`) when the target index backs a live UNIQUE/PK/EXCLUDE constraint (`idx.IsConstraint || idx.IsExclusion`); a bare `CREATE UNIQUE INDEX` (no constraint) still drops cleanly. Closes the onek :294-296 `DROP INDEX`→`RENAME CONSTRAINT`→`DROP INDEX <new>` sequence (0 occurrences of `onek_unique1_constraint` in the diff). Remaining C2 sub-gaps: TYPE…USING (C10-entangled), comma multi-action (structural), RENAME `<col>` TO bare, ANALYZE tab(col), NOT VALID, STORAGE, OF/NOT OF, DROP COLUMN IF EXISTS, DROP CONSTRAINT IF EXISTS, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 fifth slice landed 2026-08-16 — TYPE…USING** — parser captures the optional USING expression (`UsingExpr` on `AlterTableAction`, `p.parseExpr()` after `parseColumnType()`, mirrors the SET DEFAULT arm); new `planner.ResolveAlterColumnTypeUsing` resolves it against the OLD column schema (`resolveExpr` + `singleBindingContext`, so ColumnRefs stay positional); `execAlterColumnType` evaluates it per-row (`evalExpr`) and coerces via `evalCast`, propagating a PG-exact 42804 error (two variants + hints, tablecmds.c:14495-14511) BEFORE the Phase-3 truncation so a failed rewrite leaves the table intact (C10 root), plus a slot RUnlock/Unpin leak fix on the error path. Closes the 11 `syntax error at or near (got using)` sites (`got using` → 0 in the diff). Deferral ledger rows appended for: evalCast's permissive coercion set (int→bool succeeds / int8→int4 narrowing not enforced — the `anothertab` cascade), the whole-row / generated-column / `SET DATA TYPE` edge rejections, and DEFAULT revalidation + typmod/`format_type_be` rendering. Remaining C2 sub-gaps: comma multi-action (structural), RENAME `<col>` TO bare, ANALYZE tab(col), NOT VALID, STORAGE, OF/NOT OF, DROP COLUMN IF EXISTS, DROP CONSTRAINT IF EXISTS, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 sixth slice landed 2026-08-16 — comma multi-action** — the ALTER COLUMN block moved out of `parseAlter`'s early-return path (`ddl.go:8778-8985`) into a new `parseAlterColumnAction() (AlterTableAction, error)` helper, dispatched from the top of `parseAlterTableAction` on the bare `ALTER` token, so the pre-existing comma loop (`first := parseAlterTableAction()` then `for p.acceptSymbol(",")`) now builds a multi-action list. Every arm converted append+`return stmt`→`return AlterTableAction{…}`; the no-op tail breaks on `,` as well as `;` and returns `AlterTableNoOp` (already ignored at `operators_ddl.go:7730`). No AST field / executor change needed (`AlterTableStmt.Actions` is a slice; `execAlterTable` already ranges it, mutating one shared `tbl`). Closes the 7 `(got ,)` + 3 `expected ADD or DROP (got alter)` sites (both → 0). Deferral ledger row for the sequential-apply gap (goopg mutates `tbl` per action; PG's `ATController` preps all first). Remaining C2 sub-gaps: RENAME `<col>` TO bare, ANALYZE tab(col), NOT VALID, STORAGE, OF/NOT OF, DROP COLUMN IF EXISTS, DROP CONSTRAINT IF EXISTS, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 seventh slice landed 2026-08-16 — DROP COLUMN/CONSTRAINT IF EXISTS** — both arms of `parseAlterTableAction` now consume `IF EXISTS` via `acceptKeyword(KwIf)`/`acceptKeyword(KwExists)` and set the existing `AlterTableAction.IfExists` flag (the DROP COLUMN arm had never actually consumed it — its old `acceptIdentKeyword` call only matched `TokenIdent`, never the `KwIf`/`KwExists` keyword tokens, so `DROP COLUMN IF EXISTS` was a syntax error; the slice-1 comment at `ddl.go:9701` already documented this trap). `execAlterDropColumn` (`operators_ddl.go:21278`) and `execAlterTableDropConstraint` (`:10011`) emit PG's NOTICE and `return nil` when the object is missing: `column %q of relation %q does not exist, skipping` / `constraint %q of relation %q does not exist, skipping` — byte-exact (ATExecDropColumn tablecmds.c:9326-9328; ATExecDropConstraint :14060-14062). The drop-constraint skip fires only at the `pkIdx == nil` fall-through (after all five kinds miss), never at a single-kind miss, so a real constraint of another kind is not falsely skipped. Closes the `drop column if exists non_existing` + `drop constraint IF EXISTS anothertab_chk` divergence lines (8 → 0). Remaining C2 sub-gaps: RENAME `<col>` TO bare, ANALYZE tab(col), NOT VALID, STORAGE, OF/NOT OF, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 eighth slice landed 2026-08-16 — RENAME `<col>` TO bare** — the parser's RENAME arm required the `COLUMN` keyword (`acceptKeyword(KwColumn)`), so the bare `RENAME a TO b` form fell through to `expectKeyword(KwTo)` and errored `expected keyword to (got a)` (gram.y:9974 `opt_column: COLUMN | /*EMPTY*/` proves COLUMN optional). Reordered the arm: RENAME CONSTRAINT (unchanged) → RENAME VALUE no-op (unchanged) → `RENAME TO` table rename moved up as `acceptKeyword(KwTo)` (must precede the fallthrough — TO is RESERVED, `parseIdent` can't consume it) → column fallthrough `_ = p.acceptKeyword(KwColumn)` + `parseIdent`/`expectKeyword(KwTo)`/`parseIdent` (mirrors the ALTER VIEW arm ddl.go:7751-7777). Parser-only; the existing `AlterTableRenameColumn` executor already emits 42703 `column "…" does not exist`. New test `TestParseAlterTableRenameColumn` (bare + COLUMN + table + constraint forms). Closes the 3 bare-form sites (`rename test2 to testx`, `rename a to x`, `rename "........pg.dropped.1........" to x`) — `expected keyword to (got …)` → 0 in the diff; sites 2/3 now reach the executor and emit PG's 42703. Remaining C2 sub-gaps: ANALYZE tab(col), NOT VALID, STORAGE, OF/NOT OF, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 ninth slice landed 2026-08-16 — STORAGE column clause** — `parseColumnConstraintList` had a `COMPRESSION` case but no `STORAGE` case, so the CREATE TABLE column-definition `storage` clause (`col type STORAGE {plain|external|extended|main}`) fell to the switch default → `expected ',' or ')'` (the ALTER `SET STORAGE` arm already parses+executes — ddl.go:8918-8930 / operators_ddl.go:8573-8604). Added a `STORAGE` case (mirrors COMPRESSION) onto a new `ColumnDef.Storage` field; `execCreateTable` threads it onto `catalog.Column.Storage` on both the BodyOrder `addCol` path and the fallback no-BodyOrder path, and a new `validateColumnStorage` enforces PG's `GetAttributeStorage` 0A000 rule (`column data type integer can only have storage PLAIN`, tablecmds.c:22082-22112, type name via `pgFormatTypeName` so `int`/`int4` render `integer`). Closes the 2 `got storage` sites (diff:2032/2066) → 0 (34→32 syntax-error lines). Residuals ledgered (TOAST `has_toast_table`; `STORAGE DEFAULT` + runtime-22023 invalid-mode). Remaining C2 sub-gaps: ANALYZE tab(col), NOT VALID, OF/NOT OF, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 tenth slice landed 2026-08-16 — NOT VALID** — two sites, both byte-matching PG. (a) CREATE TABLE table-level CHECK: both arms (`parseTableConstraintElement` anonymous + the `CONSTRAINT name CHECK` twin) consume-and-drop a trailing `NOT VALID` after `NO INHERIT` (PG auto-validates at CREATE TABLE — `transformCheckConstraints` parse_utilcmd.c:2946 + `heap.c:2584-2587`, so no `convalidated='f'` is recorded). (b) ALTER ADD [CONSTRAINT] NOT NULL: the arm consumes `NOT VALID` order-independently (before OR after `NO INHERIT`, per gram.y `ConstraintAttributeSpec` gram.y:6213-6252) onto `AlterTableAction.NotValid`, threaded through a new `notValid` param on `AddNotNull` onto `NamedNotNullConstraint.NotValid`, written to pg_constraint contype='n' convalidated `row[6]='f'`, and flipped back to 't' by a new `NotNullConstraints` name-match loop in the VALIDATE CONSTRAINT arm (PG excludes CONSTR_NOTNULL from the Phase-3 pre-scan, tablecmds.c:9956). Closes `nv_parent` (diff:534) + `atnnparted` (diff:1075) → 0 (32→30 syntax-error lines); `\d+` renders `"dummy_constr" NOT NULL "id" NOT VALID`, then without the suffix after VALIDATE. Deferral ledger row for the order-dependent CREATE TABLE trailer loop (a bare `CHECK (...) NOT VALID` still fails). Remaining C2 sub-gaps: ANALYZE tab(col), OF/NOT OF, SET WITHOUT OIDS, ENFORCED dup.
  - **C2 eleventh slice landed 2026-08-16 — OF / NOT OF** — two new executor methods close the typed-table reassignment forms. Parser arms (ddl.go:9461-9481) capture `OF type_name` → `AlterTableAddOf` (with `OfType`) and `NOT OF` → `AlterTableDropOf` onto new AST kinds (ast.go:3180-3189); `execAlterTableAddOf` (operators_ddl.go:9387) resolves the composite type, rejects an inheritance parent (42809 `typed tables cannot inherit`), then order-strictly zips the composite's (compacted) fields against the table's non-dropped columns emitting the four 42804 messages in PG's exact order (`table has column "…" where type requires "…"`, `table "…" has different type for column "…"`, `table is missing column "…"`, `table has extra column "…"`) — the type match derives the expected canonical `catalog.Type` exactly as CREATE's `addCol` does, so `numeric(9,2)` vs `numeric(8,2)` (typmod) and `bigint` vs `numeric` (base) both fail while NOT NULL is ignored (PG: attnotnull need not match). Success stamps `tbl.OfTypeOID = ct.OID`; `execAlterTableDropOf` (operators_ddl.go:9462) clears it (42809 `"…" is not a typed table` when never typed). Closes the 3 OF/NOT OF syntax-error sites + all 6 validation errors byte-exact. Residuals ledgered: check_of_type 42809 parity, reloftype restart-durability, missing table↔type dependency edge. Remaining C2 sub-gaps: ANALYZE tab(col), SET WITHOUT OIDS, ENFORCED dup.
  - **C2 twelfth slice landed 2026-08-16 — SET WITHOUT/WITH OIDS + duplicate [NOT] ENFORCED** — the last two tiny C2 grammar sub-gaps, both parser-only. (a) `SET WITHOUT OIDS` now hits the `SET WITHOUT CLUSTER` arm (ddl.go:9332-9341) → `AlterTableNoOp` (PG `AT_DropOids`, tablecmds.c:5528-5530, silent). (b) `SET WITH OIDS` → new guard before the `expected ADD or DROP` fallthrough emits `syntax error at or near "WITH"` (no gram.y production; keyword re-uppercased). (c) duplicate `[NOT] ENFORCED`: new `rejectDuplicateEnforced`/`isEnforcedAttr` helpers (Raw `multiple ENFORCED/NOT ENFORCED clauses not allowed`, parse_utilcmd.c:3999-4027) after the single-shot consume at the 5 CHECK sites + a `sawEnforced` check in `parseFKConstraintAttrs` (3 callers threaded). Closes SET WITHOUT/WITH OIDS + ENFORCED-dup diff sites (→ shared lines). Residual ledgered: `parseAlterConstraintAttrs` (ALTER CONSTRAINT `<name>`) still silently overwrites a dup. Remaining C2 sub-gap: ANALYZE tab(col) (re-route — ANALYZE/VACUUM statement gap, not ALTER TABLE); the sql:1205/1208 `only … add column` block is C9.
  - **C2 thirteenth slice landed 2026-08-16 — ANALYZE tab(col) / VACUUM ANALYZE tab(col) column-list** — the final C2-adjacent gap, re-routed to the ANALYZE/VACUUM statement parser. New `parseVacuumTargets` (`internal/parser/parser.go`) parses each relation target plus an optional `(col, …)` list (PG `vacuum_relation: relation_expr opt_name_list`, gram.y:12021-12026), feeding parallel `TargetCols [][]string` on `VacuumStmt`/`AnalyzeStmt` (`ast.go`); new `resolveAnalyzeColumns` (`operators_analyze.go`) reproduces PG `attnameAttNum` (parse_relation.c:3589-3609 — case-sensitive, dropped-skipping; NOT the case-insensitive `InMemory.LookupColumn`) and emits 42703 `column "%s" of relation "%s" does not exist` / 42701 `… appears more than once` (dedup on column Ordinal, analyze.c:372-400), wired into `expandAnalyzeTargets` and `expandVacuumTargets` (the latter only when `vs.Analyze` — plain VACUUM ignores `va_cols`). Closes the 4 alter_table sites (alter_table.sql:1056-1059, all dropped columns of `atacc1`) → byte-exact 42703. **C2 is now COMPLETE.** Deferral ledger row for the per-column stats restriction (valid-column ANALYZE still analyzes all columns — exercised by vacuum.sql M0134-0084). Remaining alter_table work: correctness classes C3/C4/C9/C10/C11.
  - **C9 first slice landed 2026-08-16 — inherited-DDL guards** — five ALTER-TABLE executor refusals (DROP/RENAME COLUMN, ADD COLUMN ONLY, DROP/RENAME CONSTRAINT on inherited columns/constraints), byte-exact 42P16 messages from `ATExecDropColumn`/`renameatt_internal`/`ATExecAddColumn`/`dropconstraint_internal`/`rename_constraint_internal` (tablecmds.c), gated through new `hasInheritanceChildren`/`colStillInherited`/`parentStillHasColumn` live-hierarchy narrowing (stale-flag-safe after parent-side DROP / NO INHERIT) + a NO INHERIT Inherited-flag clear; parser records `AlterTableStmt.Only` (was accepted-and-discarded). Diff 4349→4298 (−51), zero new divergence; 8 tests in `operators_ddl_inherit_guards_test.go`. Deferred (3 ledger rows): `Column.InhCount int` multi-parent bookkeeping (attinhcount 1-vs-2 `c1` merge), LIKE+ATTACH-PARTITION `Inherited`, INHERIT child-validation, INHERITS merge NOTICEs, inline `CONSTRAINT con1` name (C7). **Newly-surfaced classes (researcher reassessment, not C9):** PLPGSQL (`v := expr FROM table` assignment rejected — blocks all 6 `check_ddl_rewrite`, 39 lines; pl_gram.y:972/gram.y:929) and TYPEDS (`'epoch'` timestamp literal rejected, 7 lines). Remaining: C9 residuals, C4/C10/C11.
  - **C3 first slice landed 2026-08-16 — constraint row-validation scans** — ADD CHECK (no NOT VALID/NOT ENFORCED), SET NOT NULL + ADD CONSTRAINT NOT NULL (both spellings), and VALIDATE CONSTRAINT (CHECK) now scan existing rows and refuse where goopg silently accepted: new `forEachLiveRow` (page-Pin live-row iterator, operators_ddl.go:10603) + `validateCheckConstraintRows` (:10671 — `parser.ParseExpr` → `planner.ResolveIndexPredicate` once → per-row `evalExpr`, 23514 on a definite boolean FALSE only — SQL 3-valued logic, NULL/UNKNOWN passes), plus a 55000 `cannot validate NOT ENFORCED constraint` guard and a VALIDATE-CHECK convalidated flip. Messages byte-exact from `ATRewriteTable` (tablecmds.c:6125/6450-6463/6493-6498), all Pos 0. Diff 4298→4185 (−113), zero raw `(byte N)` leaks; 8 tests in `operators_ddl_constraint_scan_test.go`. Deferred (5 ledger rows): C3 slice 2 (ADD PK/UNIQUE 23505 `DETAIL: Key … is duplicated.` + PK-over-NULL 23502 + Pos 0 on 23505), FK-VALIDATE shadowing (C4 ADD-FK duplicate-name), and three pre-existing bugs surfaced by the scans (nondeterministic partition-key DROP COLUMN guard, `parseCheckExpr` quote-loss, ONLY-partitioned DROP COLUMN).
  - **C3 second slice landed 2026-08-16 — the index-build path (C3 COMPLETE)** — ADD PK/UNIQUE on duplicates now emits PG's 23505 `could not create unique index %q` + `DETAIL: Key (…) is duplicated.` (new `btree.BulkEntry.KeyDesc` value-capture field filled at entry construction; `sortBuildEntriesFindDuplicate` now returns the dup index `int`; new `btreeBuildKeyDescription` renderer mirroring `BuildIndexValueDescription` genam.c:178-276, NULL→`null` for NULLS NOT DISTINCT) with `Pos=0` on both 23505 raises + the NULLS-NOT-DISTINCT raise; ADD PRIMARY KEY over a NULL key column now raises 23502 `column %q of relation %q contains null values` (new `forEachLiveRow` scan in `execAlterTableAddPrimaryKey`, dup-then-null ordering = PG pass order, ADD UNIQUE deliberately exempt); and the REFRESH MATVIEW non-concurrent re-wrap propagates `ee.Detail` + `Pos=0`. Diff 4185→4157 (−28), 7 tests in `operators_ddl_constraint_scan_test.go`. Deferred (1 ledger row, 4 residuals): float4/float8 DETAIL rendering (Datum.Format no float kind), multi-column PK null attnum-order, `Duplicate keys exist.` ACL case, 42703/42P16 Pos on the ADD PK/UNIQUE arms. **C3 is now COMPLETE.** Remaining alter_table work: C9 residuals, C4/C10/C11.
  - **partition-key DROP COLUMN guard (surfaced by C3) landed 2026-08-16** — `execAlterDropColumn`'s expression-partition-key check replaced the nondeterministic `strings.Contains(strings.ToLower(fmt.Sprintf("%v", expr)), colLower)` (matched ASLR pointer hex → the `DROP COLUMN b` regress section flipped silent↔error between runs) with a structural `partitionKeyExprUsesColumn(e parser.Expr, colLower string) bool` walker (`operators_ddl_partition.go`, beside `funcExprContainsName`) covering ColumnRef/FuncCall/BinaryOp/UnaryOp/CastExpr/CollateExpr/CaseExpr/ExtractExpr/IsNullExpr; both raise sites corrected 0A000→42P16 and `Pos: act.Pos()`→`Pos: 0` (PG `has_partition_attrs` partition.c:255, tablecmds.c:9358). Diff 4157→4153, regress body byte-deterministic across runs; 16-row table-driven test. tpch-spotcheck Q12=2/Q13=35. Sibling ledgered: ALTER-TYPE partition-key guard (`execAlterColumnType` :22127).
  - **ALTER-TYPE partition-key guard landed 2026-08-16 (the DROP COLUMN sibling)** — `execAlterColumnType` (`operators_ddl.go:22137`) now raises 42P16 `cannot alter column %q because it is part of the partition key of relation %q` before any rewrite, walking `tbl.PartitionKey` (bare key) + `tbl.PartitionKeyExprs` (via `partitionKeyExprUsesColumn`). Unlike the DROP COLUMN arm (Pos 0), `ATExecAlterColumnType` carries `parser_errposition(pstate, def->location)` (tablecmds.c:14450), so this raise uses `Pos: act.Pos()` — threaded by `parseAlterColumnAction`'s new `colPos` capture (`internal/parser/ddl.go`) — and the `LINE 1 … ^` caret points at the column name, byte-matching PG (alter_table.out:3977-3983). The two 42804 coercion-failure arms (evaluation-time, no source location) were corrected `Pos: act.Pos()`→`Pos: 0`. Diff 4153→4145 (−8); errposition verified byte-exact via raw psql (no off-by-one). 4-subtest `TestAlterTablePartitionKeyGuardAlterType`. Residual ledgered: descendant-partition recursion (PG recurses into `part_5`'s key on `ALTER TABLE ONLY list_parted2 …`, goopg reports 42703 — diff :3929/:3934, pre-existing). Remaining alter_table work: C9 residuals, C4/C10/C11.
  - **C4 landed 2026-08-16 — ADD FOREIGN KEY validation semantics** — the ADD FK arm (`execAlterTableAddForeignKey`, operators_ddl.go:7731-7789) now checks in PG order: 42710 duplicate-name guard (cross-kind FK+CHECK+PK/UNIQUE/EXCLUDE index+NOT NULL via new `fkConstraintNameInUse`, explicit `CONSTRAINT name` only — PG skips auto-names via `ChooseConstraintName`, tablecmds.c:9824-9833/`ConstraintNameIsUsed` pg_constraint.c:412); 42703 source-column then 42703 ref-column (new `fkColumnExists`, case-sensitive dropped-skipping mirroring `resolveAnalyzeColumns`/`transformColumnNameList` tablecmds.c:13327-13346; ref-loop skipped when no ref cols — PK inferred); 23503 existing-row scan when `!NotValid` (reuse the C3 `validateFKConstraintExistingRows`; byte-exact error+DETAIL via `assertParentExists`, propagated with Pos 0). VALIDATE FK arm's 23503 Pos-suppressed (removed `ee.Pos = act.Pos()` — `ri_ReportViolation` ri_triggers.c:2778 has no errposition). Root cause it closed: the four same-name `attmpconstr` ADDs (sql:355/358/361) used to pile up in `tbl.ForeignKeys` (goopg silently appended where PG rejected), so `VALIDATE CONSTRAINT` broke on the first stale `NotValid=false` entry and skipped the scan — masking out:499-500. Diff 4145→4113 (−32), FK block byte-green; 5 new tests in `operators_ddl_fk_add_validation_test.go`. Residuals ledgered: 42804 type-compat, 42830 no-unique-constraint, 42908 column-count, 0A000 system-column, EqualFold-vs-strcmp. Remaining alter_table work: C9 residuals, C10/C11.
  - **C10 landed 2026-08-16 — static assignment-coercibility gate** — the C10 data-loss crash was already closed by C2 slice 5 (`fec178bd`); the remaining genuine divergence was `ALTER COLUMN atcol1 TYPE boolean` (int8→bool, sql:1356) silently succeeding where PG raises 42804. New `canAssignCast` (rejects int2/int4/int8 → bool + text → int2/int4/int8) + `noUsingCoercionError` (byte-exact no-USING 42804, Pos 0) at the top of `execAlterColumnType` (no-USING path, before the `Pool==nil`/`nBlocks==0` early returns — PG's 42804 fires even on an empty table); the per-row no-USING raise refactored onto the helper (bytes unchanged), `evalCast` + WITH-USING arm untouched. Diff 4113→4110 (−3), sql:1356 byte-matches PG; verified against real PG 18.3 (explicit `1::boolean` preserved, empty + non-empty int8→bool both 42804, int8→int4 narrowing preserves value). 6 subtests in `operators_ddl_alter_type_coercion_test.go`. Residuals ledgered (WITH-USING int→bool, DEFAULT re-coercion, format_type_be, full matrix). Remaining alter_table work: C9 residuals, C11.
  - **C9 residuals landed 2026-08-16 — ONLY-on-partitioned guard + descendant-partition recursion** — three guards close the partitioned-parent block's guard statements: (1) `execAlterDropColumn` raises 42P16 `cannot drop column from only the partitioned table when partitions exist` + HINT `Do not specify the ONLY keyword.` (`only && PartitionMethod != "" && hasInheritanceChildren`, tablecmds.c:9385-9389, Pos 0); (2) descendant-partition recursion in both `execAlterDropColumn` (Pos 0) and `execAlterColumnType` (Pos `act.Pos()`) walks `allDescendants` (OID-sorted) applying `partitionKeyUsesColumn` per descendant and names the DESCENDANT in the 42P16 (`... relation "part_5"`); (3) `execAlterColumnType` gains the missing `cannot alter inherited column "%s"` guard (42P16 + `act.Pos()`, tablecmds.c:14436-14440, before the own-key guard). `only` threaded from `execAlterTable` (`s.Only`). Shared `partitionKeyUsesColumn` helper factored; `allDescendants` gained a `visited` set (cyclic-ATTACH guard). Diff 4110→4102 (−8); `:2850`/`:2902`/`:2903` byte-green. Remaining C9 residuals (ledgered, NOT closed): `part_2 ADD COLUMN c text` guard (tablecmds.c:7250), the ATTACH-PARTITION `Inherited`-marking gap (row 1410a) blocking `part_2 DROP/RENAME/ALTER` inherited refusals, cyclic-ATTACH 42P17. Then C11 (rules/view-DML + at_view_2 + top-level-* freeze).
  - **C9 final landed 2026-08-17 — partition-child DDL refusals + circular ATTACH (C9 COMPLETE for this block)** — S1 `execAlterTableAddColumn` FIRST guard: `im.IsPartitionChild(tbl.OID)` ⇒ 42809 `cannot add column to a partition` (Pos 0, no detail/hint; `ATExecAddColumn` tablecmds.c:7247-7250 — gated on the live partitionChildren map, NOT `PartitionParentOID`, which is 0 for an ATTACHed child). S2 new `markAttachedColumnsInherited` called from BOTH ATTACH arms (immediate + COMMIT-deferred `ApplyPendingPartitionAttaches`) and its sibling `clearAttachedColumnsInherited` from BOTH DETACH arms (plain + CONCURRENTLY finalize) — mirrors `MergeAttributesIntoExisting` (tablecmds.c:17500) / `RemoveInheritance` (:18009-18014); this closed the ATTACH `Inherited`-marking gap (ledger row 1410a). S3 `colStillInherited` falls back to `im.PartitionParentOf(tbl.OID)` when `PartitionParentOID == 0`, so the inherited DROP/RENAME/ALTER refusals finally fire on the ATTACHed `part_2`. S4 ATTACH arm rejects self-attach + back-edges (`allDescendants(childTbl)` contains the parent) with **42P07** `circular inheritance not allowed` + detail `"parent" is already a child of "child".` — the real SQLSTATE per `ATExecAttachPartition` tablecmds.c:20338-20362 (the previously-recorded 42P17 was a placeholder). S4 unmasked a pre-existing bug: `execAlterTableDropConstraint` cascaded to children even under ONLY — now takes `only bool` (threaded from `s.Only`) and skips the cascade (`dropconstraint_internal` tablecmds.c:14025-14110). Diff 4102→4073 (−29); `:2848-2858` + cyclic-ATTACH byte-green. Tests: `operators_ddl_c9_residuals_final_test.go` (S1/S2/S4 + DROP-CONSTRAINT-ONLY) + extended `TestAlterTableDescendantWalkCycleSafe`. Residuals ledgered 2026-08-17 (NOT closed): ADD CONSTRAINT duplicate-name merge accounting (extra `merging constraint` NOTICEs), already-a-partition 42809 re-ATTACH guard (sql:2697), ONLY-guards for SET NOT NULL / ADD CONSTRAINT (row 1423), constraint-deparse `((b <> 'zz'))` vs `(b <> 'zz'::bpchar)`. Remaining alter_table work: C11 (rules/view-DML + at_view_2 + top-level-* freeze).
- [ ] **M0134-0003 — arrays.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
      **PARKED 2026-08-18 — not selectable.** Design:
      `docs/design/0134-0003-arrays-sql-divergence.md`. Re-measured post-C19-harness-fix:
      **3311 lines / 24 hunks**; six classes bucketed hunk-by-hunk. **S1 LANDED** —
      `[NOT] LIKE|ILIKE ANY|SOME|ALL (…)`, a sibling-path omission in `parseExprPrec`
      (the ordering comparisons and the POSIX regex operators both had quantifier
      wiring; the adjacent LIKE family did not). Parser-only; `evalInExpr` already
      dispatches `AnyOp` through the general `evalBinary`. Diff **3311→3251**, all 8
      statements of `arrays.sql:463-470` now match PG; sentinel `char` unchanged.
      Residual is a long tail with no bounded slice: **A** slice subscripting
      `a[lo:hi]` absent read+write (~900) and **B** assignment-target indirection
      `SET col[i]=…` (~250) are ~1150 *coupled* lines sharing one missing
      representation; **C** is 13 catalog-registered array builtins with no executor
      dispatch (~600); **D′** (~10) and **E** (~40) are fragments.
      **Correction — do not re-derive:** class D is NOT "generalize ANY/ALL to any
      operator". A precision pass found the only operators failing with ANY/ALL in
      this file are the LIKE family and `*`; no `@>`/`<@`/`&&`/`~`/`IS DISTINCT FROM`
      appears with a quantifier anywhere here, so a general rewrite has no corpus
      witness. **Re-arm trigger:** array slice subscripting (A+B) landing as its own
      milestone, or class C's builtin set being filled in — then re-measure from
      scratch (never compare to a pre-2026-08-18 `arrays` number; those predate the
      C19 harness fix). Cheapest standalone follow-ons if small wins are wanted
      without re-arming: individual class-C functions that wrap existing array-decode
      helpers (`cardinality`, `array_reverse`, `trim_array`), one ticket each.
- [ ] **M0134-0004 — cluster.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-18 — do not re-select until the re-arm trigger fires.** Measured post-C19 harness fix: **5747 lines** (5223 `+` / 257 `-`), one hunk ~4700. Full six-bucket map in `docs/design/0134-0004-cluster-sql-divergence.md`. **Bucket 1 dominates (~4900 lines): `CLUSTER` is a no-op stub** — `clusterOp.Next()` (`internal/executor/operators_cluster.go:1-97`) locks, checks existence, and flips `pg_index.indisclustered`, but never tuplesorts the heap, swaps the relfilenode, or rebuilds indexes (PG: `cluster.c:cluster_rel`→`rebuild_relation`→`copy_table_data`→`finish_heap_swap`), so every `SELECT` after the first `CLUSTER` diverges on row order. VACUUM-FULL-scale, not a slice. Buckets 2 (old-style `CLUSTER idx ON tbl` syntax), 3 (`SUBSTRING(str FOR n)` without `FROM`), 5 (`maintenance_work_mem` GUC) are each bounded but **inert** — tens of lines out of 5747, since Bucket 1 governs everything downstream; Bucket 6 (nested default-partition conflict) is **inferred, not verified** — do not brief from it without another caller-chain read. **Bucket 4 LANDED 2026-08-18** (see the follow-up item below). **Re-arm trigger:** a real CLUSTER implementation lands as its own milestone (no design doc exists yet — writing one is step 1); then re-measure from scratch and re-classify. Ledger: 3 rows 2026-08-18.
- [ ] **M0134-0004-a — `CREATE DATABASE ... TEMPLATE` drops table owners** — follow-up discovered while classifying `cluster.sql`. `internal/postmaster/database_ddl.go:917-923` builds a fresh `&catalog.Table{...}` per cloned table without copying `srcTbl.Owner`, so every table in a template-cloned database silently reverts to bootstrap-superuser ownership. Same failure shape as the landed Bucket 4 fix, but a different package and call site, so it needs its own brief plus a CREATE DATABASE-level test. Also fold in the no-storage CTAS fallback (`internal/executor/operators_ddl.go` ~4225-4232), which discards the `*catalog.Table` returned by `Catalog.CreateTable` and so cannot stamp an owner. Ledger: 2026-08-18.
- [ ] **M0134-0005 — constraints.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-19 — user directive: deprioritised in favour of the renumbered M0134-0006..0023 block; do not re-select while any of those is unchecked.** Parked at the **176 lines / 8 hunks** baseline left by 0005at (`836dd34a`), with 46 sub-items landed. Resume ranking, carried from `.ralph/working_set.md` so the baton can be recycled: (1) **parent-level constraint MERGE short-circuit** — `postgres/src/backend/commands/tablecmds.c:9998-9999` returns before computing `children` when the relation already holds an identical constraint, so `ALTER TABLE ONLY p ADD CONSTRAINT c CHECK …` on a parent already holding `c` must SUCCEED where goopg now raises 42P16; needs expression-equality plus `conislocal`/`coninhcount` arithmetic (`heap.c:2774-2845`) — a semantic slice, not a guard. (2) re-join 0005an's split `parent_noinh_convalid` fixture (`internal/executor/operators_ddl_check_validate_cascade_test.go:212-226`) into a literal upstream port — its `DELETE FROM ONLY` blocker cleared in 0005ao. (3) `validateCheckConstraintRows` whole-tree → per-relation walk: PG skips re-scanning an already-`convalidated` descendant (`tablecmds.c:12960`). (4) the other 3 of PG's 4 `dropconstraint_internal` NOT NULL refusals (replica-identity `:14162`, identity-column `:14174`) — absent from BOTH spellings, must land together. **TRAP:** `ADD GENERATED … AS IDENTITY` looks ~2 lines of diff but does not parse at all; closing it means porting `ATExecAddIdentity` (`tablecmds.c:8240-8362`), a feature slice — ledgered 5×.
- [ ] **M0134-0008 — select_parallel.sql** — **PARKED 2026-08-19: structurally
      unreachable until goopg has real parallel-query execution.** Sized over two
      delegated rounds (`tmp/ralph-handoffs/M0134-0008a`, `-0008b`): the case is
      0/1 FAIL with a 1526-line diff, 90% of it one cascade. Root-caused twice —
      first to a harness gap (the runner never ran `create_misc.sql`, so `a_star`
      did not exist; **FIXED this loop**, see below), then, once unmasked, to a
      goopg parser gap (`ALTER TABLE <table>*` wildcard suffix, postfix
      `ISNULL`/`NOTNULL`) that stops `create_misc.sql` renaming `a_star.a`→`aa`.
      **Neither is the blocker.** The file's tail asserts
      `pg_stat_database.parallel_workers_launched` increased
      (`select_parallel.sql` ~1341-1347, expects `t|t`, goopg gives `f|f`), and
      goopg has no `Gather`/parallel-worker path whatsoever — so no harness or
      parser fix can make this case PASS. **Re-arm trigger:** file and land a
      parallel-query execution milestone; then re-run
      `scripts/pg-regress-runner.sh --verbose select_parallel` and re-size.
      Ledger rows appended 2026-08-19 for the parallel-query blocker, the two
      parser gaps, `SET SESSION AUTHORIZATION`/`current_setting`, function-level
      `SET ROLE`, and a pre-existing `aggregates` planner bug found en route.
      **Landed this loop (net progress, kept):** `scripts/pg-regress-runner.sh`
      RUN_SETUP now runs `create_misc.sql` in upstream's first-group position
      (`postgres/src/test/regress/parallel_schedule:45`), which upstream records
      as a prerequisite for `select_parallel`, **`join` (M0134-0015) and `with`**
      (`:62,88,114`) — verified no regression: `select_having` and
      `select_implicit` still 1/1 PASS, `aggregates` FAILs identically before and
      after (clean stashed-baseline re-run). CSV row stays `not-tried`.
- [ ] **M0134-0009 — select_views.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-20** — sizing found the diff dominated by a harness gap, not a goopg bug: `select_views.sql` reads views (`street`, `iexit`, `toyemp`, `my_property_secure`, ...) created by `create_view.sql`, which `scripts/pg-regress-runner.sh` never ran as a setup prerequisite (`postgres/src/test/regress/parallel_schedule:46,103,105` documents the dependency explicitly). Fixed by adding a fourth best-effort prerequisite block mirroring the existing `create_misc.sql`/`create_index.sql`/`create_aggregate.sql` ones — pure runner-script change, zero engine code. Result **1520 → 1512** diff lines (small, not the hoped-for large drop): most of the masked diff stayed masked because `street`/`iexit` still fail to be created — their definitions use PG's geometric containment operator `?#` (`create_view.sql:34-41`) and goopg's lexer rejects the bare `?` character outright, so the two views never exist and `select_views.sql` still cascades "relation does not exist". CSV row stays `failed`, `pass_required=no`, no `make regen-testport`. **Re-arm trigger:** once a `?`-prefixed geometric-operator lexer family lands (ledgered 2026-08-20, folds in with the already-ledgered `#thepath` unary path-length gap, bucket C), re-run `scripts/pg-regress-runner.sh --verbose select_views` — remaining known buckets from sizing: (B) runner doesn't set `PGDATESTYLE`/`PGTZ`/`PGOPTIONS` like real `pg_regress` (cross-cutting, own task), (D) `CURRENT_USER` deparses as `current_user()` in EXPLAIN text instead of PG's paren-less form (cosmetic, needs the EXPLAIN expr-to-string printer located), (E) `security_barrier` qual-pushdown restriction absent (REFACTOR-tier planner gap, do not attempt as a slice).
      **PARKED 2026-08-19 after landing the dominant fix.** Sizing found the case
      blocked on three independent parser/DDL gaps, but ALSO found one real
      engine bug worth more than the case itself, which was fixed and shipped:
      `current_user`/`current_role`/`user`/`session_user` were the hardcoded
      literal `"postgres"` (`internal/executor/expr.go`), so
      `SET SESSION AUTHORIZATION` was invisible to every query and every
      `WHERE name = current_user` leaky-view predicate returned 0 rows. Design:
      `docs/design/m0134-0009-session-user-identity.md`. Seven sibling sites
      moved together; an adversarial review caught three of them (DO-NOT-SHIP →
      fixed → GO).
      **Re-arm trigger** (all three needed before this case can pass): (1) lex
      `?`-prefixed geometric operators — `?#` is currently a lex error, blocking
      the `street`/`iexit` views; (2) unary prefix `#` (path point-count);
      (3) `CREATE SCHEMA <n> CREATE TABLE ...` sub-commands, which today
      silently no-op and cascade ~13 "relation does not exist" errors (52
      `ERROR:` lines total when `create_view.sql` loads). Then re-run
      `scripts/pg-regress-runner.sh --verbose select_views` and re-size.
      Upstream prerequisite: `postgres/src/test/regress/parallel_schedule:103`
      (`# select_views depends on create_view`). CSV row stays `failed` —
      **no `make regen-testport` needed**. Ledger rows appended 2026-08-19.
- [ ] **M0134-0010 — predicate.sql** — **PARKED 2026-08-19.** Sized at HEAD
      (`scripts/pg-regress-runner.sh --verbose predicate`): the file is 100%
      `EXPLAIN (COSTS OFF)` plan-shape assertions, **18 of 22 diverging with ZERO
      `ERROR` lines** — no hard blockers, but five *independent* root causes, four
      of which are separate features. The loop that sized it landed the smallest
      shippable one: **NOT NULL-driven reduction of `IS NULL`/`IS NOT NULL`
      restriction quals for single-baserel queries**, mirroring
      `initsplan.c restriction_is_always_true/_false` — design
      `docs/design/m0134-0010-notnull-qual-reduction.md` (indexed). That took the
      case **18/22 → 14/22**, with the entire single-table restriction/OR-clause
      block now byte-identical to PG. Also fixed en route: the always-false `Result`
      is now childless (PG emits no `->` line) and `One-Time Filter:` no longer
      parenthesizes a bare literal. **Re-arm trigger:** the remaining 14 need
      (a) outer-join nullability tracking (`Var.varnullingrels` — goopg has no
      equivalent; this is the HARD prerequisite, since the "must NOT reduce" cases
      pass today only accidentally), then (b) join `ON`-clause qual reduction,
      (c) inheritance per-child constraint exclusion / qual pushdown, and
      (d) `Sort`/`Materialize` emission parity. CSV row stays `not-tried` —
      **no `make regen-testport` needed**. Three ledger rows appended 2026-08-19.
- [ ] **M0134-0011 — subselect.sql** — **PARKED 2026-08-19.** Sized at HEAD
      (`scripts/pg-regress-runner.sh --verbose subselect`): ~90-120 of ~335
      statements diverging, 2831 diff lines, across **seven independent root
      causes**. Confirmed NOT a stale status and NOT a missing-prerequisite
      artifact — `parallel_schedule` attaches the "depends on `create_misc`" note
      to `join`, not `subselect`, and every table the file touches comes from
      `test_setup.sql`, which the runner already runs. Like 0009 and 0010, the
      sizing round yielded a **real, shipped engine fix**: `IN (subquery)` inside
      a `JOIN ... ON` clause was rejected outright with SQLSTATE 0A000
      (`IN (subquery) not supported in this context`) because
      `newResolveContext` (`internal/optimizer/planner.go:486-493`) never sets
      `.cat` and the top-level context gets its catalog only in a post-hoc
      patch-up (`:1085-1088`) that runs AFTER every ON clause is resolved —
      hence the asymmetry where the identical sublink worked in `WHERE`/the
      target list. Fix: three lines in `planFromItem` giving `leftCtx`,
      `rightCtx` and `mergedCtx` the catalog already in scope. All 5 of the
      case's 0A000 errors are gone (`^+ERROR` 36 → 31); the diff-line count rose
      2831 → 2848 because each fixed statement traded a one-line ERROR for a
      multi-line **plan-shape** mismatch — goopg emits a `SubPlan` in the join
      qual where PG's `pull_up_sublinks` builds a semijoin (explicit non-goal,
      ledgered). Design `docs/design/m0134-0011-join-on-sublink-catalog.md`.
      **Why parked:** the highest-value remaining pair (~27 statements — nested
      parenthesized-JOIN scoping + VALUES-to-Array with correlated elements) is
      blocked on an **architectural** gap, not a bug: goopg's parser lowers a
      parenthesized join to an opaque derived table (`tryParseParenJoin`,
      `internal/parser/select.go:1175-1245`) and `JoinExpr.Right`
      (`internal/parser/ast.go:697-704`) cannot hold a nested subtree, so inner
      aliases are sealed inside a subquery scope before the planner sees them,
      where PG treats parenthesization as pure grouping with no scoping effect
      (`parse_clause.c:1149 transformFromClauseItem`, flat
      `list_concat(l_namespace, r_namespace)` at `:1218`). Fixing that means
      parser AST + `planFromItem` recursion + the eight optimizer files that
      consume the flat join-tree shape (`collapse.go deconstructJointree`,
      `reduce_outer_joins.go`, `joinorder.go`, `with.go` + corpora) — every
      multi-table query, all of TPC-H/TPC-DS, runs through those. The other four
      buckets are a SubPlan parameter-lifecycle bug, systemic `EXPLAIN VERBOSE`
      fidelity (the largest share of the diff, recurs in every regress file),
      array-subscript result naming, and ten unrelated one-statement bugs.
      Four deferral rows appended 2026-08-19. CSV row stays `failed` → **no
      `make regen-testport`**. **RE-ARM TRIGGER:** reselect once either the
      nested-JOIN scope refactor or an EXPLAIN-VERBOSE-fidelity milestone lands.
- [ ] **M0134-0012 — update.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
  **PARKED 2026-08-20** — sized at 841 diff lines / 13 `^+ERROR` / 17 `^-ERROR` across EIGHT independent root causes, with no missing prerequisite (the case creates every table it touches). The dominant bucket (~300 of 841 lines) is multi-level partition row routing through column-reordered intermediate partitions — REFACTOR-tier. The loop shipped the one cleanly contained cause: `routeToPartitionDepth`'s LIST arm formatted the routing key with a closed if/else and no default, so a LIST-partitioned table with a `numeric` (or float/date/uuid) key could not accept ANY row; design `docs/design/m0134-0012-list-partition-numeric-routing.md`. `update.sql` went 841 → **823** diff lines and 13 → **11** `^+ERROR`; Q12=2/Q13=35 spotcheck anchors unchanged. CSV row stays `failed` (no `make regen-testport`). **Three deferral rows** appended 2026-08-20. **Re-arm trigger:** re-run `scripts/pg-regress-runner.sh --verbose update` once the nested-partition-routing ledger row lands — buckets 2 (partition constraint not enforced on UPDATE, 9 stmts) and 3 (RLS on row movement, 4 stmts) are partially CONFOUNDED by it and cannot be measured honestly before then.
- [ ] **M0134-0013 — insert.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
  **PARKED 2026-08-20** — sized at 1062 diff lines / 58 `^+ERROR` / 57 `^-ERROR` across EIGHT independent root causes, with **no** missing `parallel_schedule` prerequisite (`insert` is self-contained). The dominant bucket (~330 of 1062 lines) is INSERT target-list indirection — `INSERT INTO t (col[i]) ...` / `(col.field) ...` are not parsed at all — needing new grammar productions plus a `transformInsertStmt`-equivalent rewrite (REFACTOR-tier). The loop shipped the one bucket that was both a genuine correctness bug and cleanly contained: after `DROP TABLE` of a DEFAULT partition, the parent could never accept another DEFAULT partition, because `validateDefaultPartition` scanned `parent.PartitionBounds` — a denormalized aggregate of the children's bounds that no removal path prunes. Fix = filter that list by the live-child set from `im.PartitionChildren(parent.OID)`; design `docs/design/m0134-0013-default-partition-stale-bounds-cache.md`. `insert.sql` went 1062 → **1051** diff lines and 58 → **50** `^+ERROR`; Q12=2/Q13=35 spotcheck anchors unchanged. CSV row stays `failed` (no `make regen-testport`). **Three deferral rows** appended 2026-08-20.
  **Cost-of-being-wrong note for the next loop:** the first attempt scanned each live *child's* own `PartitionBounds`, mirroring the "correct-looking" dead sibling `validateDefaultPartitionConflict`. That inspects the parent's GRANDchildren and merely swapped one wrong error for another — caught only by the regress gate, not by unit tests. **Unreachable code has never been executed and is evidence of correctness in neither direction**; do not read the M0134-0012 "which sibling copy is stale" lesson as "the dead copy is the good one".
  **Re-arm trigger:** re-select this task to pick off ledger bucket 3 (missing `DETAIL: Failing row contains (...)` on both partition-routing errors, `internal/executor/operators_storage.go:2536` and `:3079` — a working `formatRowForDetail` helper already exists at `:2699`) or bucket 2 (partition-bound constraint unenforced on direct INSERT into a leaf partition); bucket 1 needs its own milestone. Bucket 4 is CONFOUNDED by bucket 2 and cannot be measured honestly until it lands.
- [ ] **M0134-0014 — mvcc.sql** — **PARKED 2026-08-20** (commit `d2460abe`). The standing "possible regression, verify" rule was applied FIRST: `scripts/pg-regress-runner.sh --verbose mvcc` at HEAD **still fails** (17 diff lines / 2 `^+ERROR`), so it is NOT a stale status — no CSV flip, row stays `failed`, `make regen-testport` NOT run. Two serially-masked root causes. (1) SHIPPED: `IF EXISTS(SELECT ...)` in a `DO` block raised `EXISTS is not supported in PL/pgSQL expressions in v0` — a routing gap, since goopg'"'"'s SQL layer implements sublinks fully while `lowerPLpgSQLExpr` cannot represent a sub-`SELECT`; sublink-bearing plpgsql expressions now fall back to a synthetic `SELECT <expr>` through `optimizer.Plan`/`Build` (design `docs/design/m0134-0014-plpgsql-sublink-sql-fallback.md`). (2) BLOCKING, REFACTOR-tier: with EXISTS fixed the loop reached a previously-unreachable bug — `substitutePlpgsqlFrameVarsInSQL` binds variables by TEXTUAL substitution before parsing and splices the `FOR i` loop variable into the FROM-item column-alias list `g(i)` -> `g(1)`. That and the shipped path'"'"'s missing frame-variable resolution are one design fault from opposite sides; PG uses parser hooks + `PARAM_EXTERN` bound parameters instead (three ledger rows, 2026-08-20). **Re-arm trigger:** when parse-then-bind lands, re-run `scripts/pg-regress-runner.sh --verbose mvcc` (17 lines / 2 `^+ERROR` at `d2460abe`) and flip the CSV row if clean.
- [ ] **M0134-0015 — join.sql** — **PARKED 2026-08-20** on the established M0134 pattern. Sized at HEAD at 20920 diff lines / 146 `^+ERROR` across six independent root causes, most of the raw line count being out-of-scope cost-model plan-shape noise. The loop shipped the two contained causes (design `docs/design/m0134-0015-join-sql-star-suffix-and-function-scan.md`): the legacy inheritance star suffix `ALTER TABLE tbl*` failed to parse because `internal/parser/ddl.go:8829` tested `TokenOperator` where the lexer emits `TokenSymbol` (high leverage — the statement lives in the SHARED `create_misc.sql` setup that `join` depends on), and non-SETOF routines were rejected as FROM-clause sources because `planTableFuncRangeVar` gated its whole user-routine branch on `if r.ReturnsSet`. Result **146 -> 131 `^+ERROR`** (diff lines rose to 20946 because formerly-aborting statements now emit real rows and EXPLAIN output — the error count is the meaningful metric; Q12=2/Q13=35 exact). CSV row stays `failed`, `pass_required=no`, **no `make regen-testport`**. Remaining buckets are ledgered (2026-08-20): the dominant one is a REFACTOR-tier recursive `table_ref`/`joined_table` grammar for chained/nested join trees. **Re-arm trigger:** re-select once that parser grammar refactor lands. One discovery worth its own task — slice D unmasked that `ALTER TABLE <root>* RENAME COLUMN` does not propagate the rename to inheritance children.
- [ ] **M0134-0016 — create_table.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
      **PARKED 2026-08-20** — the standing "possible stale status, verify first"
      rule was applied and the case still FAILS at HEAD, so the CSV row stays
      `failed` and NO `make regen-testport` was run. Sized at 762 diff lines /
      17 `^+ERROR` across seven independent root causes. The loop shipped the
      single contained highest-leverage one — bucket A, ~114 of the 178 missing
      lines (~64%): PG annotates most CREATE TABLE validation errors with an
      `errposition` (psql's `LINE n:`/`^`) and goopg emitted none, with zero
      message-text mismatches underneath. Root cause was a sentinel collision —
      `ExecError.Pos` is 0-based with `0` meaning "unset", and both partition
      validators stamped every error with the statement's own `s.Pos()`, which
      is 0 for a regress statement starting at `CREATE`. Fix stamps each error
      with the offending sub-node's own `.Pos()`, and adds
      `PartitionByClause.MethodPos`/`KeyColPos` (upstream's
      `PartitionSpec.location`/`PartitionElem.location`) for the two errors that
      operate on bare strings. Design:
      `docs/design/m0134-0016-createtable-errposition.md`. Result 762 -> 610
      diff lines, `-LINE` 57 -> 29, `+ERROR` unchanged at 17.
      **Re-arm trigger:** select again once bucket B has a standalone repro (the
      range-partition MINVALUE overlap miss, which serially masks ~13 downstream
      errors) — it is the next-largest contained candidate. The other five
      unshipped buckets and the two remaining errposition gaps are in
      `.ralph/deferral_ledger.md` (2026-08-20, M0134-0016) with resume points.
- [ ] **M0134-0018 — create_index.sql** — **PARKED 2026-08-20** on the established M0134 pattern, but the loop shipped a genuine correctness fix found while sizing it. **Sizing methodology note (reusable):** the regress runner's setup phase already runs `create_index.sql` once as a prerequisite for `aggregates.sql`, then runs it again as the test — the double-run inflates the diff with ~28 spurious `already exists` errors; size this case with a manual `test_setup.sql`+`create_misc.sql` prep and `--no-setup`. Clean size at HEAD: **3475 diff lines, 43 hunks, 112 `^+ERROR` across ~55 message shapes**, almost no cosmetic EXPLAIN noise. Two REFACTOR-tier buckets carry **74%** of the errors (geometric-operator lexer whitelist, 58; `REINDEX`/`CREATE INDEX CONCURRENTLY` grammar+feature absence, 25) — both ledgered with re-arm triggers, and fixing both hypothetically still leaves 29/112. **What the loop shipped instead** (design `docs/design/m0134-0018-temp-shadow-drop-rollback.md`): bisecting a `point_tbl` that silently vanished mid-case exposed a general, case-independent **silent catalog-loss bug** — a failing `CREATE TEMP TABLE x ...` permanently removed the pre-existing permanent `public.x` from the live catalog for **every** session until restart. `execCreateTable` (`internal/executor/operators_ddl.go:1713`) implemented TEMP shadowing (M0097-0003) by destructive pre-emption — stash the permanent table in `TempTableShadows` and `DropTable` it at `:1750-1766`, restoring it only from `DROP TABLE` on the temp relation — a handshake that assumes the CREATE always succeeds. For the self-shadowing CTAS at `create_index.sql:84` failure is guaranteed, since the drop removes the SELECT's own source and `optimizer.Plan` fails at `:4714-4716` before any `CreateTable`, so no `DROP TABLE` can ever restore the shadow. Probe established the loss is catalog-entry-only (heap file untouched), cluster-wide (visible from a fresh connection), and heals on restart — which is what made it silent. Fix: a named-error-return `defer` restores the stash via the same `restoreTempShadow` helper the DROP path now shares, covering EVERY post-drop error exit; the success path is bit-identical. Gates: units suite PASS, tpch-spotcheck PASS with **Q12=2 / Q13=35** exactly, `TestDDL*` (47 subtests) PASS incl. FAIL-pre/PASS-post guard `TestDDLFailedTempShadowCreateRestoresPermanentTable`, and an end-to-end real-server repro confirming the permanent table survives from a second connection. CSV row stays `failed`, `pass_required=no`, **no `make regen-testport`**. Also root-caused but deliberately NOT fixed (one task per loop, ledgered): `SET SESSION ROLE` has no case in the string-prefix SET dispatcher — sibling pair `internal/postmaster/query.go` + `extended.go`, ~15 LOC, and the whole `SET SESSION <any-guc>` form is affected. **Re-arm trigger:** re-select once a general operator lexer or a geometric-type milestone lands.
- [ ] **M0134-0019 — indexing.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-20** — sized at HEAD at 1517 diff lines / 39 hunks / 41 `^+ERROR` across five root causes (verified genuinely `failed`, not a stale status). The dominant bucket (partitioned-index ATTACH/DETACH semantics, ~700+ lines / ~47% of the remainder) is REFACTOR-tier. The loop shipped the one bucket that is both contained and NOT case-specific: goopg had no `^` exponentiation operator at all, so `SELECT 2^16` was a syntax error engine-wide (design `docs/design/m0134-0019-exponentiation-operator.md`); 1517 → 1492 lines, 41 → 39 `^+ERROR`, zero `syntax error` lines left in the diff. CSV row stays `failed`/`pass_required=no`; no `make regen-testport`. **Re-arm trigger:** re-select once a partitioned-index DDL milestone opens — nothing short of bucket (1) moves this case near green. Remaining buckets and their resume points are in `.ralph/deferral_ledger.md` (2026-08-20, M0134-0019).
- [ ] **M0134-0020 — stats.sql** — regress-sql `not-tried`: make the case match PG 18.3 (normalise against `./postgres/`). Run the case, fix the divergence; on pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-20** — the `not-tried` status was resolved by RUNNING the case first: 1391 diff lines / 31 hunks / 101 `^+ERROR` at HEAD, so it is genuinely failing and the CSV row goes to `failed`/`pass_required=no` (no `make regen-testport`). The loop shipped the one bucket that is contained AND engine-wide rather than case-specific: goopg implemented PG's shared/flushed statistics tier but had **no member of the transaction-scoped tier at all**, so `pg_stat_get_xact_function_calls`/`pg_stat_get_xact_tuples_inserted` were 42883 errors everywhere, not just here (design `docs/design/m0134-0020-xact-pgstat-getters.md`); same half-built shape as 0019's missing `^` — correct `pg_proc` seed rows existed with no Go code behind their HandlerNames. Result 1391 → 1351 diff lines, 101 → **80** `^+ERROR` (the `+ERROR` shrink hit the predicted 21 exactly; the diff-line shrink fell short of the predicted ~95 because of a separately-ledgered engine-wide integer column-alignment divergence that keeps fixed call sites showing as one-line diffs). **Re-arm trigger:** re-select once a cumulative-statistics milestone opens (`pg_stat_have_stats`, `pg_stat_io`/backend-IO instrumentation, the `pg_stat_reset_*` family) or once general FROM-clause table-valued-function support lands — nothing short of those moves this case near green. Two real bugs the getters newly EXPOSED (in-xact `DROP TABLE` does not drop staged rel stats; staged counters survive at least one transaction-end path, suspected to over-count the shared tier too) and the alignment divergence are all in `.ralph/deferral_ledger.md` (2026-08-20, M0134-0020) with resume points.
- [ ] **M0134-0021 — vacuum.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-20** — measured at HEAD (496 diff lines / 18 `^+ERROR` / 16 `^-ERROR`, genuinely failing) and sized into six root-cause buckets; the loop shipped the largest contained one — the per-relation VACUUM/ANALYZE maintenance-permission check, which turned out to be two tiers rather than one (a denied partitioned parent must still expand its children, and every flattened target is re-checked in the main execution loop), design `docs/design/m0134-0021-vacuum-partition-child-permission.md`, plus two inert GUC registrations. Result: **393 / 14 / 16**, with all six ownership permutations of `expected/vacuum.out:593-684` now byte-identical. CSV row stays `failed` (no status change → no `make regen-testport`). **Re-arm trigger:** re-run `scripts/pg-regress-runner.sh --verbose vacuum`, then take bucket A (option-literal grammar, `internal/parser/parser.go:1971-1982,1997-2021` vs `gram.y:utility_option_arg`) followed by bucket B (`VACUUM/ANALYZE ONLY`); buckets D/E/F and the bare-`VACUUM;` ownership gap are ledgered 2026-08-20.
- [ ] **M0134-0022 — window.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-20** — measured at HEAD (4575 diff lines / 90 `^+ERROR` / 23 `^-ERROR`, genuinely failing) and sized into nine root-cause buckets; the loop shipped the largest contained one: ordinary aggregates (`var_pop`/`var_samp`/`variance`/`stddev*`/`bool_and`/`bool_or`/`array_agg`) were rejected as window functions by FOUR serial name gates even though every one is already implemented as an ordinary aggregate, so widening any single gate would have reduced ZERO diff lines. Design `docs/design/m0134-0022-window-aggregate-gates.md`. Result: **4604 / 64 / 23** — line count rose 29 while `^+ERROR` fell 26, because single-line rejections became multi-row value comparisons once the queries execute; `bool_and`/`bool_or` and `array_agg`'s frame case are now byte-identical to the oracle. CSV row 227 stays `failed` (no status change → no `make regen-testport`). **Re-arm trigger:** re-run `scripts/pg-regress-runner.sh --verbose window`, then take bucket B+C (RANGE-frame `in_range` arithmetic: `internal/executor/operators_window.go:904-925` `(*windowOp).inRange` uses generic `evalBinary` instead of PG's saturating `in_range_*` family, `postgres/src/backend/utils/adt/float.c:1027` et al., ~1744 lines) followed by bucket D (EXPLAIN plan shape + missing `Window:` line). Buckets E/F/G, the missing moving-aggregate inverse-transition model, and the variance/stddev numeric display-scale bug are ledgered 2026-08-20.
- [ ] **M0134-0023 — write_parallel.sql** — **PARKED 2026-08-20** (status was `not-tried`; RUN for the first time, resolved to genuinely FAILING, so the CSV row stays `failed` — not stale). **Unreachable:** 44 of the 80 expected-output lines (55%) are `Gather`/`Parallel Seq Scan` plan shapes and goopg has no parallel-worker execution path — the same structural blocker that parked M0134-0008 (`select_parallel`). All six non-EXPLAIN features it exercises already work. Shipped the real defect found underneath: the false `42P07` on same-transaction create/drop/recreate of a name — a THREE-piece fix (visibility + explicit catalog opt-in + both transaction exits), design `docs/design/m0134-0023-txn-drop-recreate-name-reuse.md`, taking the case 86 -> 80 diff lines and **12 -> 0 `^+ERROR`**. RE-ARM TRIGGER: re-run `scripts/pg-regress-runner.sh --verbose write_parallel` after a parallel-query milestone lands. Ledgered: the same blind spot at `CREATE MATERIALIZED VIEW` / `CREATE INDEX` / `CREATE VIEW`, and the NET-0 EXPLAIN-of-CTAS unwrap.
- [ ] **M0134-0024 — generated_virtual.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-20** — re-run at HEAD per the standing staleness rule: still FAILS (4438 diff lines / 114 `^+ERROR` / 102 `^-ERROR`), so the row is NOT stale and stays `failed` (no status change -> no `make regen-testport`). Sized into three buckets; the loop shipped a fix that is **not a generated-column bug at all** — unqualified `INHERITS` / `ALTER TABLE ... INHERIT` parent lookup ignored `search_path`, so inheritance only worked when the parent lived in `public`, while `SELECT * FROM parent` in the same session succeeded. Live-reproduced with a PLAIN table; it surfaced here only because this case runs entirely under a non-public schema. Fix reuses the existing `lookupTableWithSearch` helper at `internal/executor/operators_ddl.go:1931` and `:9803`; design `docs/design/m0134-0024-inherits-searchpath-lookup.md`. Result **4397 / 96 / 102** (-41 lines, -18 `^+ERROR`). **Re-arm trigger:** re-run `scripts/pg-regress-runner.sh --verbose generated_virtual`, then take Bucket 1 — the implicit INSERT/UPDATE target list excluding every `GeneratedAlways` column — whose two sibling sites (`internal/parser/analyzer/analyzer.go:2988-3003` and `internal/optimizer/planner.go:10078-10092`) MUST move atomically or every currently-passing plain insert into a generated-column table breaks. The ~25 sibling raw-`LookupTable` DDL sites (silent-degradation class first), Bucket 1 and the `VIRTUAL`-treated-as-`STORED` storage-model gap are all ledgered 2026-08-20.
- [ ] **M0134-0025 — groupingsets.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **PARKED 2026-08-20** — not stale (re-run at HEAD: 2373 lines / 25 `^+ERROR`). Shipped the engine-wide crash fix (correlated `*OuterColumnRef` pass-through in `resolveExprAfterAggregate`), 2373 -> 2689 / 25 -> 41 with the connection-loss cascade gone and NO regression (stash A/B proved the pre-crash region byte-identical). CSV row stays `failed`. Next CONTAINED slice if re-selected: nested `GROUPING SETS(...)` in `internal/parser/select.go:792-848` `parseGroupingSetsList` (142 lines / 9 `^+ERROR`), then bare `GROUP BY ()`. RE-ARM for a full pass: needs the grouping-sets aggregation-strategy work (`GroupAggregate`/`MixedAggregate` chains vs one `HashAggregate`) — see the two M0134-0025 deferral rows.
- [ ] **M0134-0026 — guc.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`, no `make regen-testport` needed). Re-run at HEAD per the staleness rule: NOT stale (767 lines / 27 `^+ERROR` / 11 `^-ERROR`). Shipped the engine-wide zone-less-`timestamptz` session-TimeZone fix (design `docs/design/m0134-0026-timestamptz-literal-session-timezone.md`), measured **760 -> 536 diff lines** under a correctly-configured harness. Re-arm trigger: pick this up again once the `SET LOCAL` outside-explicit-transaction slice lands (CONTAINED, ledgered) — that is the largest remaining bucket; the savepoint-GUC-stack bucket is REFACTOR-tier and gated behind a nesting-level `GucStack`. See `.ralph/deferral_ledger.md` rows dated 2026-08-20 for all four buckets and the harness re-baselining task.
- [ ] **M0134-0027 — copy.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`, no `make regen-testport` needed). Re-run at HEAD: 364 lines / 21 `^+ERROR` / 15 `^-ERROR`; re-run under the PG-parity env (`PGTZ`/`PGDATESTYLE`/`PGOPTIONS` intervalstyle/`LC_MESSAGES=C`) was byte-identical — this case has no timezone/DateStyle sensitivity, unlike guc.sql, so no false negative here. Shipped the largest CONTAINED bucket: `validateCopyOptions` (`internal/optimizer/copy.go`) rejected the legacy bareword `COPY ... CSV`/`COPY ... BINARY` trail even though the executor already understood the option shape the parser emits for it — PG's grammar folds both keywords into one `format` DefElem (`gram.y` `copy_opt_item`), mirrored here by sharing `formatSpecified` across the new cases and the existing `format` case. 364 -> 334 lines (21 -> 19 `^+ERROR`). Remaining buckets (largest first): file-based `COPY <query> TO 'file'` unsupported (~35 lines, needs a write-side file-endpoint executor), `HEADER MATCH` column-name/count validation entirely missing (~31 lines), lone `\.` end-of-copy-marker-not-alone-on-line detection missing (~16 lines), `COPY ... FROM 'file' WHERE (...)` clause unparsed (~7 lines, PG12 feature), `COPY (DEFAULT '')` option unrecognized (~7 lines), misc tail (COPY FREEZE on foreign/partitioned tables not rejected, `pg_stat_progress_copy` absent, on_error ignore doesn't skip malformed rows) (~13 lines). See `.ralph/deferral_ledger.md` rows dated 2026-08-20 for all buckets.
- [ ] **M0134-0028 — horology.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`, no `make regen-testport` needed). Re-run at HEAD both ways: default env 4291 lines / 259 `^+ERROR`; PG-parity env (`PGTZ`/`PGDATESTYLE`/`PGOPTIONS` intervalstyle/`LC_MESSAGES=C`) 4206 lines / 259 `^+ERROR` — genuinely different (parity is the true baseline, unlike `copy.sql`). Sized into three buckets: ~73% is a **harness gap** (single-file runner doesn't seed the `TIMESTAMP_TBL`/`TIMESTAMPTZ_TBL`/`TIME_TBL`/`TIMETZ_TBL`/`INTERVAL_TBL`/`DATE_TBL` fixture tables real `pg_regress`'s `parallel_schedule` grouping populates from sibling files, ledgered not fixed here); `to_timestamp`/`to_date` format-string parsing is REFACTOR-tier (`evalToTimestamp`, `internal/executor/expr.go:13439`, a deliberately-scoped Go-`time.Layout` stand-in vs PG's stateful token scanner, ledgered). Shipped the CONTAINED bucket: PG's grammar treats `TIME ZONE` as a dedicated two-word alias for the `timezone` GUC in `SET`/`SHOW`/`RESET` (`gram.y:1709,1904,1974`), not a name looked up via generic GUC lookup — goopg had TWO independent GUC dispatchers with no `TIME ZONE` case in either: the parser (`internal/parser/parser.go` `parseSet`/`parseShow`/`parseReset`) and the simple-query fast path (`internal/postmaster/query.go` `handleQuery`, which intercepts `SHOW`/`SET`/`RESET` via string-prefix matching BEFORE the parser ever runs — round 1 shipped only the parser fix and confirmed it was inert for `psql -f` traffic, motivating a round-2 scope extension to mirror the same three cases into `query.go`). Design `docs/design/m0134-0028-set-show-reset-time-zone-alias.md`. Result: 4291 -> 4287 lines, 259 -> 252 `^+ERROR`. A third, smaller gap was flagged but not fixed: `SET TIME ZONE '-1.5'`/numeric fixed-offset zone values now parse without error but don't actually change the session's effective UTC offset (ledgered). See `.ralph/deferral_ledger.md` rows dated 2026-08-20 (M0134-0028) for all three buckets.
- [ ] **M0134-0029 — identity.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`, no `make regen-testport` needed). Sized at 3581 diff lines / 78 `^+ERROR` / 49 `^-ERROR` (identical in default and PG-parity env — no timezone/DateStyle sensitivity, self-contained fixture — no harness-gap risk like `horology.sql`). Shipped TWO contained, general (not identity-specific) correctness fixes: (1) `internal/executor/codec.go` `decodePhysicalPGValueMctxStyled`'s `"char"` (OID 18, no args) decode branch now normalizes a stored NUL byte to the empty string, mirroring the encode (`encodeValuePGCtx`) and display (`charTypeDisplayForm`) paths that already did this — previously `WHERE col = ''` never matched a stored-NUL `"char"` column (e.g. `pg_attribute.attidentity`), producing ~2500 spurious rows in the case's opening sanity check; (2) `internal/executor/operators_ddl_partition.go` `parseBoundValue`'s `StringConst` case now requires full-string integer consumption (`strconv.ParseInt` instead of `fmt.Sscanf("%d")`), fixing a false `42P16 empty range bound specified` on ordered date-string RANGE partition bounds like `'2016-07-01'`/`'2016-08-01'` (both used to truncate-parse to the same int). Result: 3581 -> 1035 lines, 78 -> 55 `^+ERROR` (~71% drop). Remaining buckets (largest first): `OVERRIDING [USER|SYSTEM] VALUE` clause entirely unparsed (REFACTOR-tier, new grammar), a multi-subcommand `ALTER TABLE ... ADD COLUMN, ALTER COLUMN ADD GENERATED IDENTITY` bug (wrongly succeeds + appears to lose OTHER columns' identity/sequence state — needs a dedicated probe), plus misc missing validations. See `.ralph/deferral_ledger.md` rows dated 2026-08-20 (M0134-0029) for all buckets, including an incidentally-found MERGE-INSERT identity-override-check gap.
- [ ] **M0134-0030 — incremental_sort.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`, no `make regen-testport` needed). Sized at HEAD: 953 diff lines / 14 `^+ERROR` / 0 `^-ERROR`. The dominant remainder (~700+ diff lines) is that goopg has **no Incremental Sort plan node or executor at all** — every EXPLAIN that PG plans as `Incremental Sort` comes out as a plain `Sort`, REFACTOR-tier (new planner path mirroring `postgres/src/backend/optimizer/path/pathkeys.c`/`costsize.c:cost_incremental_sort` plus a new executor node mirroring `nodeIncrementalSort.c`) and this case cannot reach PASS without it regardless of which small bucket ships. Shipped the one CONTAINED, generically-useful fix among the 14 `^+ERROR`s: `targetMeta` (`internal/optimizer/planner.go:11566-11579`) had no arm for `*OuterColumnRef`, so a LATERAL derived table whose sole projected column is a bare correlated outer-column reference (`(SELECT t.a) AS sub`) got the synthetic schema column mislabeled `?column?`, and the outer query's qualified lookup (`sub.a`) failed with "column does not exist" even though the row data was correct — not incremental_sort-specific, a general LATERAL-subquery correctness bug. Result: 14 -> 8 `^+ERROR` (the six previously-hard-erroring statements now execute, though raw diff lines rose slightly, 953 -> 959, because those statements now surface pre-existing plan-content gaps — missing parallel-scan/Gather-Merge plans and the Incremental Sort gap itself — instead of collapsing to one ERROR line). Remaining buckets (largest first): Incremental Sort itself (see above, own milestone); `jsonb_array_elements`/`jsonb_each`/etc. as FROM-clause table-valued functions (4 `^+ERROR`, needs a new JSONB-array-iteration executor primitive, not just planner wiring); point `<->` distance operator mis-tokenized by the lexer (2 `^+ERROR`, lexer half is a trivial safe fix but insufficient alone — also needs the operator itself + GiST KNN-ordering plan support); `explain_analyze_without_memory` "does not exist" despite being created earlier in the script (2 `^+ERROR`, root cause not pinned — needs a live-session repro). See `.ralph/deferral_ledger.md` rows dated 2026-08-20 (M0134-0030) for all buckets.
- [ ] **M0134-0031 — copy2.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`, no `make regen-testport` needed). Sized at HEAD: 955 diff lines / 60 `^+ERROR` / 30 `^-ERROR`. Three dominant buckets are REFACTOR-tier missing-feature gaps: legacy `WITH DELIMITER AS 'x' NULL AS 'y'` pre-9.0 COPY-option grammar (~90 lines); `COPY ... FROM stdin WHERE <expr>` clause entirely unparsed (~120+ lines, cascading); `COPY ... WITH (DEFAULT '…')` option (PG≥14) entirely unimplemented (~150 lines). Shipped the smallest CONTAINED bucket: PG's CSV output writer (`CopyAttributeOutCSV`, `postgres/src/backend/commands/copyto.c:1300-1350`) force-quotes a field colliding with the NULL marker (disambiguating an actual empty string from NULL) or, on a single-column row, the `\.` end-of-data marker — a rule `EncodeCopyCsvRow` (`internal/executor/copy_csv.go:126-144`) lacked entirely. Design `docs/design/m0134-0031-copy-csv-force-quote-null-eof.md`. Result: 955 -> 888 diff lines; `^+ERROR` unchanged at 60 (remaining errors all belong to the excluded buckets — this fix converts value-mismatch noise into byte-identical lines, not error-count reduction). See `.ralph/deferral_ledger.md` row dated 2026-08-20 (M0134-0031) for the full remaining-bucket list.
- [ ] **M0134-0032 — inherit.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`, no `make regen-testport` needed). Sized at HEAD: 3310 diff lines / 38 `^+ERROR` / 40 `^-ERROR`. Dominated by a REFACTOR-tier missing subsystem — the ALTER TABLE inheritance validation matrix (inherited-column/constraint rejection, multi-parent DEFAULT conflicts, no propagation of parent DDL to existing children, ~1000+ lines, `postgres/src/backend/commands/tablecmds.c` `MergeAttributes`/`MergeConstraintsIntoExisting`) — plus several independent smaller gaps: EXPLAIN plan-shape divergence on inherited/partitioned tables (Result-elision, join selection); `pg_get_expr`/CHECK-constraint raw-text deparse (architectural, shared with the `adbin`/`conbin` text-storage deviation); a `circle` GiST opclass gap blocking `EXCLUDE USING gist`; an attribute-number-remapping bug across partitions with differently-ordered columns (`errtst_*` block, ~140 lines); an unconfirmed ORDER BY/Sort correctness bug on an inherited-table query (needs a live-session repro, not chased this loop); DROP CASCADE NOTICE/DETAIL ordering. Shipped the smallest CONTAINED bucket: `buildForeignKeyDefString` (`internal/executor/expr.go:6345`), the sole `pg_get_constraintdef` FK renderer, unconditionally schema-qualified the referenced table (`REFERENCES public.foo(id)`) regardless of session search_path — correct for pg_dump (`search_path=''`) but wrong for an ordinary `\d+`/interactive session, where PG's `generate_relation_name` (`postgres/src/backend/utils/adt/ruleutils.c` `pg_get_constraintdef_worker`) omits the schema once it resolves via `RelationIsVisible`. Threaded `ctx *Context` through and reused the existing `RegObjectSchemaVisible` visibility check (`expr.go:14435`). Design `docs/design/m0134-0032-fk-constraintdef-schema-visibility.md`. Result: 3310 -> 3300 diff lines, the `test_foreign_constraints_id1_fkey` mismatch fully resolved (byte-identical); `^+ERROR` unchanged (this bucket was value-mismatch, not an error). See `.ralph/deferral_ledger.md` row dated 2026-08-20 (M0134-0032) for the full remaining-bucket list.
- [ ] **M0134-0033 — create_procedure.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
- [ ] **M0134-0034 — insert_conflict.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
- [ ] **M0134-0035 — interval.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`, no `make regen-testport` needed). Re-run at HEAD both env variants (default and PG-parity `PGTZ`/`PGDATESTYLE`/`PGOPTIONS` intervalstyle/`LC_MESSAGES=C`) — byte-identical (case sets its own `DATESTYLE`/`IntervalStyle` mid-script, so no harness-gap risk here). Sized at HEAD: 3016 diff lines / 132 `^+ERROR` / 214 `^-ERROR`, bucketed into eight root causes (two REFACTOR-tier: `@`/`ago`/special-value interval literal parsing needing tokenizer changes across the parser + `pg_input_is_valid`/`pg_input_error_info`; `IntervalStyle=postgres_verbose`/`sql_standard`/`iso_8601` output formats needing three new renderers, shared with `horology.sql`/`timestamp*.sql`/`date.sql`). Shipped the two smallest CONTAINED buckets: (1) `interval * numeric/int/float` and `interval / numeric/int/float` were entirely unimplemented (`internal/executor/expr.go` `evalBinary`'s `OpMul`/`OpDiv` case had no `KindInterval` arm) — ported PG's `interval_mul`/`interval_div` (`postgres/src/backend/utils/adt/timestamp.c`) field-by-field with the month/day/time carry-rounding logic, ±infinity/NaN factor handling, int32/int64 overflow guards, and `division_by_zero` on a zero divisor; this also needed two sibling fixes discovered mid-implementation — the parser-analyzer type-checker (`internal/parser/analyzer/analyzer.go`) rejected `interval * numeric` with 42804 before the executor ever ran it, and `planner.go`'s `exprType` mistyped the `BinaryOp` result as `numeric` (wrong wire TypeOID, wrong psql column alignment); (2) `targetMeta` (`internal/optimizer/planner.go`) gained an `IntervalLit` arm so a bare `SELECT interval '1 day'` names its column `interval` instead of `?column?`, mirroring the existing `TypedStringLit`/`CastExpr` arms (`parse_target.c` `FigureColnameInternal` `T_TypeCast`). Result: 3016 -> 2711 diff lines, 132 -> 104 `^+ERROR`, 214 -> 195 `^-ERROR`. **Re-arm trigger:** re-run `scripts/pg-regress-runner.sh --verbose interval` and take the `@`/`ago` literal-parsing bucket next (largest remaining `^+ERROR` contributor), or the IntervalStyle output-format bucket if a broader multi-file win is wanted. See `.ralph/deferral_ledger.md` rows dated 2026-08-20 (M0134-0035) for all remaining buckets, including a systemic `ExecError.Pos`/`LINE N:` position-annotation gap on runtime (non-parse-time) interval errors discovered while verifying this loop's fix, and a sibling `pg_input_is_valid`/`pg_input_error_info` gap missing `date`/`timestamp` cases too.
- [ ] **M0134-0036 — create_table_like.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`, no `make regen-testport` needed). Sized at HEAD (sorted/normalized comparator, the harness's actual PASS/FAIL basis): 32 content lines changed, 7 `^+ERROR`, 2 `^-ERROR` (raw ordered diff is 494 lines but mostly stderr-at-EOF reordering noise, not real divergence). Bucketed into four root causes: (1) `LIKE ... INCLUDING/EXCLUDING COMPRESSION` entirely unimplemented — parser had no `compression` case in the INCLUDING/EXCLUDING loop and the executor copied a LIKE'd column's `Compression` field unconditionally; (2) `LIKE <sequence>` should error 42809 but silently succeeds, `LIKE <composite type>` should succeed but errors 42P01 — root cause: `SearchPathCatalog` (the real runtime wrapper) has no `HasCompositeType` passthrough, so an existing type-assertion to `*catalog.InMemory` fails silently (same "dead code behind a live wrapper" pattern as `dead_code_is_not_a_reference_impl`); (3) `pg_get_statisticsobjdef_columns` seeded in `pg_proc` but never implemented in the executor's builtin-function switch, breaking psql's `\d+` "Statistics objects:" footer; (4) multi-parent `INHERITS` (no LIKE) storage-parameter-conflict detection doesn't fire at runtime, and its error message wording looks swapped with the sibling LIKE-merge path's message (needs a live repro to confirm root cause, not chased). Shipped the two smallest CONTAINED buckets, (1) and (3): parser `ddl.go` gained a `compression` case (mirroring `storage`) plus `:+compression` in the `ALL` expansion; executor `operators_ddl.go`'s LIKE-merge loop gained `includeCompression` gating (mirroring `includeStorage`); catalog `catalog.go`'s `BuildStatisticsObjDef` had its "ON columns/exprs" list-rendering factored into `statisticsObjColumnsList`, reused by a new `BuildStatisticsObjDefColumns` (columns-only, no `CREATE STATISTICS` prefix/kinds clause/`FROM` — mirrors `ruleutils.c` `pg_get_statisticsobj_worker(columns_only=true)`); `expr.go` gained a `pg_get_statisticsobjdef_columns` case mirroring the existing `pg_get_statisticsobjdef` case's OID-resolution boilerplate. Result: 7 → 2 `^+ERROR` (both remaining errors are the out-of-scope buckets 2 and 4). Buckets 2 and 4 need separate slices — 2 needs the `SearchPathCatalog`/`InMemory` wrapper gap fixed first (risk of reintroducing a dead-branch-behind-a-live-wrapper bug), 4 needs a throwaway-server repro to confirm root cause before sizing. See `.ralph/deferral_ledger.md` rows dated 2026-08-20 (M0134-0036) for three further deferral candidates discovered while verifying this fix (LIKE-copied statistics objects losing their column list, a `regnamespace` cast bug in the `\d+` footer, and `INCLUDING ALL` on a foreign table wrongly copying COMPRESSION).
- [ ] **M0134-0037 — join_hash.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose join_hash` (no env-sensitivity — case only sets hashjoin/work_mem/parallel GUCs): 1001 raw diff lines, 21 `^+ERROR` / 0 `^-ERROR`, five root-cause buckets. Dominant bucket A (~15/21 errors, ~71%): a `RETURNS TABLE`-declared plpgsql function used unaliased in FROM fails 42703 on any explicit OUT-column reference at plan time and even `SELECT *` returns the OUT columns as NULL — reproduced live with a throwaway 2-line function, engine-wide (not join_hash-specific), traced to the `isSimpleSingle` fast path in `internal/optimizer/planner.go` (~1001-1054) but not pinned to one line; needs a live-debug trace session, not attempted this loop (top re-arm priority — likely affects other regress cases using `RETURNS TABLE`). Bucket A2 (CONTAINED, 4/21 errors) was shipped: `json_extract_path`/`json_extract_path_text`/`jsonb_extract_path`/`jsonb_extract_path_text` were seeded in `pg_proc` but had no case in the executor's builtin-function dispatch — added a shared `jsonPathStep` path-walk primitive in `internal/executor/expr.go` mirroring PG's `get_path_all`/`get_worker` (`postgres/src/backend/utils/adt/jsonfuncs.c`), reusing `evalJSONArrow`'s rendering tail (`jsonElemAsJSONDatum`/`jsonElemAsTextDatum`, factored out as a pure refactor). Result: the 4 `json_extract_path` `^+ERROR`s are gone (verified live: `hash_join_batches('select 1')` no longer 42883s on the missing builtin, though it still fails on Bucket A). Remaining buckets B (no Parallel Hash/Parallel Hash Join executor node — sibling of M0134-0023's parallel-worker gap), C (FULL JOIN planned Merge not Hash — same cost-model territory as the closed M0126 dead end, do not re-attempt), D (correlated-subplan-as-HashCond planning + EXPLAIN VERBOSE subplan rendering), and E (LATERAL subquery's nested JOIN ON-clause can't see the lateral sibling's column, likely CONTAINED once traced) are all REFACTOR-tier or unsized — see `.ralph/deferral_ledger.md` rows dated 2026-08-20 (M0134-0037) for full detail and PG oracle citations.
- [ ] **M0134-0038 — json.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose json`: 3156 raw diff lines, 325 `^+ERROR` / 93 `^-ERROR`, 0/1 PASS. Unlike prior M0134 cases, no CONTAINED bucket exists to ship — the dominant cost driver is a genuinely missing feature family, not a bug: `internal/executor/expr.go:11697` has exactly one JSON builtin case (`json_extract_path`/`jsonb_extract_path` family, shipped M0134-0037) and every other JSON constructor/deconstructor (`to_json`, `json_build_object`, `json_build_array`, `json_object`, `row_to_json`, `array_to_json`, `json_strip_nulls`, `json_array_length`, `json_object_keys`, ~15+ functions) is unimplemented past its `pg_proc` seed row — Bucket 1, REFACTOR-tier (PG oracle `postgres/src/backend/utils/adt/json.c`; already flagged systemic in the M0134-0002 ledger row). Bucket 2 (REFACTOR-tier): table-valued JSON SRFs (`json_each`, `json_array_elements`, `json_populate_record[set]`) entirely unimplemented, needs real SRF/tupledesc plumbing (`postgres/src/backend/utils/adt/jsonfuncs.c`). Bucket 3 (REFACTOR-tier): the parser has no support at all for function *column-definition* lists (`AS q(a text, b text)`, PG's `func_alias_clause`/`TableFuncElementList`) — `internal/parser/select.go:1591-1632` only parses bare identifier lists, blocking `json_populate_recordset`/`json_to_record`/`jsonb_to_recordset` even once Bucket 2 lands. Bucket 4 (out of scope, ~40 lines): `to_tsvector`/`json_to_tsvector`/`ts_headline` full-text-search calls incidental to this file, unrelated JSON subsystem. Bucket 5 (unsized, likely CONTAINED): `'...'::json#>array[...]` raises a parser syntax error on `#>` — not root-caused to a specific tokenizer gap this loop. Confirmed json.sql has zero `RETURNS TABLE` plpgsql functions, so M0134-0037's Bucket A does not resurface here. Most-leveraged next slice if re-armed: scalar-only `to_json`/`row_to_json`/`array_to_json` (no SRF, no Bucket-3 parser work needed) — resolves ~20-30 `^+ERROR` lines and doubles as the fix for the already-ledgered M0134-0002 `alter_table.sql` gap, but is net-new implementation (needs a JSON-value internal encoder), not a bug fix, so it should be scoped as its own task rather than folded into a "size json.sql" loop. See `.ralph/deferral_ledger.md` rows dated 2026-08-20 (M0134-0038) for full detail.
- [ ] **M0134-0039 — jsonb.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose jsonb`: 6676 diff lines, 681 `^+ERROR` / 110 `^-ERROR`, 0/1 PASS, nine root-cause buckets, large overlap with json.sql's (M0134-0038) missing-JSON-builtin-family bucket (~240/681 `^+ERROR`, REFACTOR-tier, same `internal/executor/expr.go:11697` gap). Shipped the smallest CONTAINED bucket: `#>`/`#>>` jsonb/json path-extraction operators were entirely unlexed (`#` alone lexed as a single-char operator, so `col#>array[...]` tokenized as `#` then `>` and blew up in the parser) — added the 2/3-char lexer token (mirroring the existing `->`/`->>` pattern exactly), new `OpJSONPathGet`/`OpJSONPathGetText` OpCodes at the same `precJSON` precedence, and `evalJSONPathGet` in `internal/executor/expr.go` reusing the M0134-0037 `jsonPathStep` walk plus the existing `jsonElemAsJSONDatum`/`jsonElemAsTextDatum` renderers — design `docs/design/0118-0100-json-arrow-operators.md` (extended, was `->`/`->>`-only). Result: 6676 → 6372 diff lines, 681 → 619 `^+ERROR` (all 38 `#>` lex-error lines gone), verified live via psql against a throwaway capped server (nested path walk, array-index path element, missing-path NULL, empty-path identity, both `json` and `jsonb` left operands, 22P02 on invalid JSON). This also **resolves** the `#>` sizing gap M0134-0038 had left unsized (ledger row flipped to `resolved`). Remaining buckets — jsonb `@>`/`<@` deep containment (dispatches to the geometric-box evaluator and errors instead; CONTAINED but needs new recursive-comparison logic, not a mirror of existing code), `?`/`?|`/`?&` existence operators (lexer trivial, evaluator new), `@@`/`@?` jsonpath match (REFACTOR-tier, no jsonpath subsystem exists), `jsonb::<numeric type>` cast over-strictness (small but needs per-type oracle cross-check), function column-definition lists and table-valued JSON SRFs (both shared REFACTOR-tier gaps with json.sql) — are all ledgered (`.ralph/deferral_ledger.md` rows dated 2026-08-20, M0134-0039). Re-arm trigger: re-run `scripts/pg-regress-runner.sh --verbose jsonb` and take the `@>`/`<@` containment bucket next (largest remaining CONTAINED-ish bucket), or promote the shared JSON-builtin-family epic (blocks both json.sql and jsonb.sql) to its own dedicated multi-loop task. Next M0134 task to select: **M0134-0040 (`jsonb_jsonpath.sql`, status `failed`)**.
- [ ] **M0134-0040 — jsonb_jsonpath.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose jsonb_jsonpath`: 6175 diff lines, 818 `^+ERROR` / 244 `^-ERROR`, 0/1 PASS. Unlike M0134-0037/0038/0039, this file has **no independently-shippable CONTAINED bucket** — 817/818 (99.9%) `^+ERROR` lines trace to the single absent jsonpath (SQL/JSON path language) subsystem confirmed missing in the M0134-0039 ledger row: `jsonb_path_query`/`_match`/`_exists` family catalogued in `pg_proc` but zero executor dispatch (717 `^+ERROR`), `@?`/`@@` operators entirely unlexed (87 `^+ERROR`), plus 13 downstream parse-cascade lines. No parser or evaluator exists anywhere in goopg — only catalog scaffolding (`pg_type`/`pg_proc` rows, `internal/catalog/codec.go` type-name mapping); confirmed distinct from the unrelated `#>`/`#>>` path-extraction work in `internal/executor/expr.go:1867-2010` (M0134-0039). Ledger row appended (`.ralph/deferral_ledger.md`, 2026-08-20, M0134-0040) with full bucket breakdown, PG oracle citations (`postgres/src/backend/utils/adt/jsonpath.c`, `jsonpath_exec.c`), and resume plan. **Two of three jsonpath-touching regress files now confirmed dominated by this exact gap** (jsonb.sql + jsonb_jsonpath.sql); the third, `jsonpath.sql` (M0134-0041, next in queue), is almost certainly the same subsystem by name. Recommend the next M0134 loop size `jsonpath.sql` first to get a 3-for-3 confirmation, then promote "implement the SQL/JSON jsonpath grammar+evaluator" to its own dedicated multi-loop epic (own design doc under `docs/design/`) rather than re-parking a fourth file against the same unresolved gap.
- [ ] **M0134-0041 — jsonpath.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose jsonpath`: 1443 diff lines, 0/1 PASS, only 1 `^+ERROR` / 36 `^-ERROR` — a DIFFERENT shape than M0134-0039/0040's `+ERROR`-dominated failures. This file never calls `jsonb_path_query`/`@?`/`@@`; it exclusively exercises `'<text>'::jsonpath` (type input/output), and goopg's cast is a naive text passthrough with zero real parser (confirmed: zero `internal/executor/` dispatch for `jsonpath_in`/`jsonpath_out` despite `pg_proc` rows at `internal/initdb/pg_proc_seed_data.go:2713-2715`). ~950/1443 lines are canonicalization mismatches (no `."key"` quoting, no `?(...)` compaction, no numeric-literal normalization); 36 `^-ERROR` lines are malformed jsonpath text goopg silently accepts (bad numeric-literal grammar, `last`/`@` context-rule violations, bad regex flags) that PG rejects. **3-for-3 confirmed**: all three jsonpath-touching files (jsonb.sql, jsonb_jsonpath.sql, jsonpath.sql) are dominated by the same absent jsonpath subsystem, so per the deferred plan this is now promoted to its own milestone rather than parked as an isolated file: **M0135 — SQL/JSON `jsonpath` subsystem** (see that section below), design `docs/design/m0135-0001-jsonpath-subsystem.md`. One CONTAINED-but-unrelated bug surfaced incidentally: `FROM unnest(ARRAY[...]) str, LATERAL pg_input_error_info(str,'jsonpath')` errors `column "str" does not exist` — PG's rule that a bare alias on a single-column SRF names both the RTE and its sole output column isn't implemented; ledgered separately, not part of M0135 (fixing it alone can't pass this test line, which also needs the jsonpath subsystem). Re-arm trigger: select M0135-S1 (jsonpath lexer/parser/pretty-printer, no evaluator needed) — it alone is expected to flip this case.
- [ ] **M0134-0042 — lock.sql** — regress-sql `failed`: **PARKED 2026-08-20** (case still FAILS; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose lock`: 179 diff lines, 0/1 PASS, 0 `^+ERROR` / 4 `^-ERROR` (goopg silently succeeds where PG raises `permission denied`) — three unrelated root-cause buckets. **Landed** the smallest CONTAINED one: `DROP VIEW ... CASCADE` notice text double-counted a circular view dependency (`lock_view2`/`lock_view3` mutual-reference cycle) because `collectAllViewTransitiveDeps`'s BFS `seen` map was never seeded with the start view before recursing — one-line fix (`internal/executor/operators_ddl.go` ~line 6167, seed `seen[startName.String()] = true`), confirmed the actual drop-execution path (`execDropOneView`'s separate `dropped` map) was already correct so only the *notice* was wrong. New regression test `TestCollectAllViewTransitiveDepsExcludesStartOnCycle` (`internal/executor/operators_ddl_view_cascade_cycle_test.go`) reproduces the exact lock.sql cycle. Verified live: the "-NOTICE: drop cascades to view lock_view2 / +NOTICE: drop cascades to 2 other objects" hunk is gone from the diff. Remaining two buckets (both ledgered, `.ralph/deferral_ledger.md` rows dated 2026-08-20, M0134-0042): (A) `pg_locks` under-reports plain tables transitively locked through a view (`LOCK TABLE lock_viewN` + `SELECT * FROM pg_locks` misses the underlying `lock_tbl1`/`lock_tbl1a` — view→view recursion is correct, view→table is not; root cause unpinned between `collectSelectTableRefs` ref-collection vs. `cat.LookupTable` resolution, needs a probe test), (B) `LOCK TABLE` has zero ACL/permission enforcement (no `LockTableAclCheck` analog; all 4 `^-ERROR` lines are missing `permission denied` — PG oracle `postgres/src/backend/commands/lockcmds.c:104,212-256`). Re-arm trigger: probe `lockRelationTransitively`'s visited-table recursion for Bucket A first (smaller, more isolated), then a dedicated ACL-check slice for Bucket B reusing goopg's existing GRANT/REVOKE ACL helper. Next M0134 task to select: **M0134-0043 (`matview.sql`, status `failed`)**.
- [ ] **M0134-0043 — matview.sql** — regress-sql `failed`: **PARKED 2026-08-21** (case still FAILS overall; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose matview`: 410 diff lines, 0/1 PASS, only 2 `^-ERROR` / 3 `^+ERROR` (mostly formatting-shape mismatches, not missing behavior) — seven root-cause buckets, none of them matview-specific "materialization" bugs (REFRESH/CONCURRENTLY/`relispopulated` all pass cleanly). **Landed** the smallest CONTAINED bucket (G): goopg's `materialized view "%s" has not been populated` (`internal/executor/operators_storage.go:1394-1397`) and `cannot lock rows in materialized view "%s"` (`internal/executor/operators_lockrows.go:657-663`) `ExecError`s both carried a non-zero `Pos`, which the wire layer (`internal/postmaster/copy.go:855-857`, `ee.Pos > 0` sentinel) turns into a `FieldPosition`, causing psql to print a spurious `LINE N: ...`/`^` pointer PG never emits for these two runtime/semantic checks (`postgres/src/backend/commands/matview.c`, `postgres/src/backend/executor/nodeLockRows.c` — no `errposition()` call in either). Fix: drop `Pos` from both constructions (defaults to 0 = omit). Verified live (throwaway capped server) and via the regress diff: all `LINE`/`^` hunks for these two error strings are gone (byte-identical to PG oracle at those 3 occurrences). Remaining six buckets (all ledgered, `.ralph/deferral_ledger.md` rows dated 2026-08-21, M0134-0043): (A) `EXPLAIN [ANALYZE] CREATE MATERIALIZED VIEW`/CTAS prints a `DDL *parser.CreateMatViewStmt` placeholder instead of the real inner-SELECT plan — twin gap, CTAS has the identical hole, medium-effort but CONTAINED, shared fix; (B) view-definition text echoes raw source instead of PG's `ruleutils`-deparsed/normalized form — REFACTOR-tier, the well-known missing deparser, not matview-specific, do not scope into this file; (C) bare integer literal (`SELECT 42`) types as `int8` instead of PG's int4-first-then-widen rule — CONTAINED but exact code site (parser/analyzer numeric-literal typing) not yet pinned; (D) CASCADE-drop DETAIL/NOTICE dependency-list ordering is nondeterministic vs PG's DFS scan order — CONTAINED, sort the dependency slice deterministically, recurring pattern likely elsewhere; (E) `DROP OWNED BY <role> [CASCADE]` is entirely unimplemented (no parser AST node, no `pg_shdepend`-style executor enumeration) — borderline REFACTOR, incidental to matview.sql (used only for role cleanup); (F) structured `*optimizer.PlanError`/analyzer errors leak the raw Go `"CODE: message (byte N)"` string to the client because the wire classifier (`internal/postmaster/dispatch.go:807,837,881,894,1158`) only special-cases the concrete `*executor.ExecError` type via type assertion — CONTAINED, high-leverage (likely fixes the same symptom in other regress files), causes the two `division by zero` mismatches in this file. Re-arm trigger: take bucket F next (smallest remaining, high shared leverage across regress files), then D, then A (shared CTAS twin). Next M0134 task to select: **M0134-0044 (`merge.sql`, status `failed`)**.
- [ ] **M0134-0044 — merge.sql** — regress-sql `failed`: **PARKED 2026-08-21** (case still FAILS overall; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose merge`: 1978 diff lines, 0/1 PASS, 80 `^+ERROR` / 30 `^-ERROR`. MERGE itself **is implemented** (parser `internal/parser/parser.go:2701`, planner `internal/optimizer/planner.go` ~11400-11515, executor `internal/executor/operators_merge.go`) — the huge diff is fallout from ~8 distinct root causes, not "MERGE unimplemented." **Landed** the largest single-cause bucket: `WHEN NOT MATCHED THEN INSERT (col-subset) VALUES (...)` left every omitted target column NULL instead of applying its column DEFAULT / auto-generating SERIAL-IDENTITY values, unlike plain INSERT and `INSERT ... ON CONFLICT` (`operators_storage.go:2373-2440`, `operators_upsert.go:198-214`) — both already build a `missing []bool` mask and call `applyDefaultsForMissing`/`autoGenerateSerialValues` before the insert but MERGE's NOT-MATCHED path (`internal/executor/operators_merge.go` Step 3) never did. Added the same mask+call pair (mirrors the upsert sibling exactly), plus `TestMergeNotMatchedInsertAppliesDefaults`/`TestMergeNotMatchedInsertAutoGeneratesSerial` (`internal/executor/merge_insert_defaults_test.go`) and a design-doc follow-up subsection in `docs/design/0118-0022-merge-insert-conflict-promotion.md`. Verified live (capped throwaway server): the previously-NULL `balance` column now correctly defaults to `-1`, and downstream `WHEN MATCHED AND balance = ...` predicates in the same fixture now discriminate real vs. previously-NULL values (diff's `wq_target`/`wq_source` section is now byte-identical to the PG oracle). Diff: 1978 → 1965 lines (`^+ERROR`/`^-ERROR` unchanged at 80/30 — this bucket was row-content divergence, not error-producing statements). Remaining buckets (all ledgered, `.ralph/deferral_ledger.md` rows dated 2026-08-21, M0134-0044): (1) `scripts/pg-regress-runner.sh` doesn't export `PGDATESTYLE`/`PGTZ`/`PGOPTIONS=-c intervalstyle=postgres_verbose` the way real `pg_regress.c:783-798` does — inflates roughly the back half of this file's diff with date-literal/display mismatches that look like engine bugs but are a harness config gap, likely affects MANY other parked M0134 files too, high-leverage but out of this file's scope (test-infra change, not engine); (2) MERGE RETURNING doesn't accept bare `old`/`new` whole-row references, only `old.col`/`new.col` (`internal/optimizer/planner.go` MERGE RETURNING bindings ~11503-11507, `qualifiedOnly: true`); (3) MERGE never fires statement-level BEFORE/AFTER triggers (`fireStatementTriggers` has zero call sites in `operators_merge.go`, row-level firing is fine); (4) MERGE NOT MATCHED clauses with a `Condition` fail to insert at all (0 rows where PG expects 1) — found incidentally during this loop's live smoke, unconditional NOT MATCHED works fine both before and after the defaults fix, so this is a distinct `clause.Condition` evaluation gap in the NOT-MATCHED path; (5) several independent MERGE-specific semantic/validation checks entirely missing (wrong-action-per-clause-type parse errors, duplicate target/source name detection, WITH-MERGE-needs-RETURNING, COPY(MERGE) source rejection, materialized-view-target rejection, unreachable-WHEN-after-unconditional-WHEN, system-column-in-WHEN-condition, ACL/permission checks) — all trace to the same underlying gap (MERGE resolves all WHEN-clause conditions against one flat binding set instead of PG's per-clause-type namespace-visibility rules) plus missing ACL wiring, REFACTOR-tier as a whole though individually pickable; (6) MERGE unsupported inside plpgsql DO-blocks/functions (statement-kind whitelist omits MERGE) — REFACTOR-tier; (7) `DO LANGUAGE plpgsql $$...$$` syntax variant (language-before-body) unsupported — small parser-ordering gap, unrelated to MERGE; (8) `EXPLAIN (COSTS OFF)` for MERGE always renders a stub `Merge on target / -> Seq Scan on source` regardless of real join strategy — possible but unconfirmed overlap with the M0134-0043 bucket B ruleutils/deparser gap, needs more digging before sizing. Re-arm trigger: bucket 4 (NOT MATCHED condition evaluation) is the next-best CONTAINED single-cause fix and likely the second-largest diff-line collapse after this loop's defaults fix; bucket 1 (harness env vars) deserves its own dedicated cross-file task given the "affects many other files" signal — do not fold into a single-file M0134 slice. Next M0134 task to select: **M0134-0045 (`misc.sql`, status `failed`)**.
- [ ] **M0134-0045 — misc.sql** — regress-sql `failed`: **PARKED 2026-08-21, no fix landed** (case still FAILs, 0/1; CSV row stays `failed`). NOT a diff-mismatch case like prior M0134 files — the ENTIRE failure is a single server crash: `UPDATE onek SET unique1 = unique1 - 1;` (the second full-table UPDATE, after `create_index.sql`'s `onek_unique1`/`onek_unique2` non-unique btree indexes) panics `storage: not enough free space in page` inside the nbtree split path, dropping the connection; ~340 of the 361 diff lines are pure fallout from the dropped connection, not independent bugs. Two research rounds plus one implementer diagnosis round (see `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0045, for full detail) pinned the real root cause: `dedupConsolidate` (`internal/access/nbtree/btree.go` ~3551) does not actually build posting tuples despite its name/comment claiming it does — it only drops byte-identical duplicates — while `pageItems` (~2171) unconditionally expands on-page postings into individual items before any split/dedup-recovery decision runs. A page holding compact posting-tuple content (plausible from the bulk loader, for onek's duplicate-heavy update-churned columns) gets permanently unpacked, and the expanded form's real combined size legitimately exceeds two fresh pages even though the packed form fit one. Two initially-plausible candidates were investigated and RULED OUT: (A) blob-format `truncateSeparator` missing a duplicate-key heap-TID tiebreaker (`pgtruncate.go:59-66`) — unreached (panic fires before separator construction) and moot for a different reason too (onek's indexes are confirmed live to use blob format regardless — they declare explicit opclasses, which `buildPGIndexKeyDesc` refuses even though `pgIndexTupleKeys=true` by default); (B) `byteAwareSplitLoc`'s hardcoded per-item cost formula vs. the real `f.itemEncodedSize` — a genuine metric mismatch, but an implementer empirically swapped it and the panic still reproduced identically (uniform item width means the split POINT doesn't move, only the reported margin does — the real blocker is aggregate capacity, not split-point balance). This is a storage-engine design gap (real posting-tuple construction mirroring PG oracle `postgres/src/backend/access/nbtree/nbtdedup.c`'s `_bt_dedup_pass`), not a single-file-scoped fix — needs a `docs/design/` pass before an implementer is briefed for the actual fix. Blocks `misc.sql` at 0/1 indefinitely until fixed; likely also blocks other bulk-UPDATE-heavy regress files sharing duplicate-key btree churn (not yet swept). Re-arm trigger: write the posting-tuple-construction design doc (extend `docs/design/0130-0011-nbtree-pg-on-disk-format.md` or a new doc), then brief an implementer against it. Given the crash blocks this file entirely, consider selecting **M0134-0046 (`misc_functions.sql`)** next while the nbtree design work is scoped, rather than re-attempting `misc.sql` immediately.
- [ ] **M0134-0046 — misc_functions.sql** — regress-sql `failed`: **PARKED 2026-08-21** (case still FAILS overall, 0/1; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose misc_functions`: 1006 diff lines, genuine row/output mismatch (not a crash, unlike M0134-0045). **Landed** the largest single-cause CONTAINED bucket: `internal/pl/plpgsql/parser.go` `(*bodyParser).parseStmt` had no case for `KwSet`, so the file's `explain_mask_costs()` helper function (opens with `SET LOCAL jit = 0;`) failed to parse at CREATE FUNCTION time with "unsupported PL/pgSQL statement" — erroring identically at all ~14 `SELECT explain_mask_costs(...)` call sites later in the file. Fix mirrors the existing GRANT/REVOKE routing (parser.go:326-334): route `KwSet` through `parseSQLStmt()`, matching PG's `pl_gram.y` which has no dedicated SET statement form (captured as an ordinary `stmt_execsql`). Traced the runtime path to confirm no bespoke-scoping issue: `execPLpgSQLEmbeddedSQL` → `*parser.SetStmt` → `utilitySettingsOp` runs under the SAME `*executor.Context` the plpgsql frame already uses — identical GUC-set semantics to a standalone top-level SET. Added `TestParseSetLocalEmbeddedSQL` (`internal/pl/plpgsql/parser_test.go`). Diff: 1006 → 988 lines (all 14 `explain_mask_costs` parse-error occurrences gone). Remaining buckets (all ledgered, `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0046): (1) table-valued-function FROM-clause allowlist (`internal/optimizer/planner.go` ~4542 `planTableFuncRangeVar`) rejects `pg_control_checkpoint`/`pg_ls_waldir`/etc. — same class as M0134-0020/M0134-0030, already-known-gap; (2) entire `has_*_privilege()` ACL-lookup builtin family absent from `internal/executor` — no sibling implementation exists at all, likely its own milestone-sized task; (3) filesystem-introspection builtins (`pg_read_file`, `pg_read_binary_file`, `pg_ls_dir`) unimplemented, pairs with (1); (4) `\gset`/`:varname` psql client-side variable substitution unsupported by the regress runner's SQL front-end lexer — same class as the M0134-0044 harness-gap bucket, fold in there rather than a new task; (5) `CREATE FUNCTION ... LANGUAGE C` (`regresslib`) DDL is silently accepted but calls return blank/no rows — C extension libraries aren't actually loaded/executed, likely out of scope entirely; (6) `generate_series(timestamptz,timestamptz,interval,text)` 4-arg tz-aware overload unresolved, plus its row-estimate support function mismatches PG for several timestamp ranges — found live this loop, not yet located to a specific file:line. Re-arm trigger: none of these individually justified expanding this loop's scope; pick per-bucket when M0134 selection returns to this file, or route (4) into the M0134-0044 harness task and (1)+(3) together as one planner-allowlist task. Next M0134 task to select: **M0134-0047 (`multirangetypes.sql`)**.
- [ ] **M0134-0047 — multirangetypes.sql** — regress-sql `failed`: **PARKED 2026-08-21, no fix landed** (case still FAILs, 0/1; CSV row stays `failed`). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose multirangetypes`: 4267 diff lines, `^+ERROR`=400 vs `^-ERROR`=35 (goopg erroring where PG succeeds — missing-feature signature, not a crash or content-mismatch case). Multirange (PG14+) support in goopg is catalog scaffolding only — `pg_type` rows exist (`internal/executor/pg18_user_catalog_rows.go:2507`) but essentially no runtime behavior: (A) multirange I/O parser is a no-op, accepts malformed literals it should reject (~50 lines); (B) range constructor functions (`numrange()`, `int4range()`, `textrange()`, `int8range()`, `float8range()`) have proc-name-table OIDs (`internal/catalog/pg_proc_names_generated.go:2598-2599`) but zero executor dispatch (~195 lines, cascades into most of the rest of the file since fixtures build multiranges from range constructors first); (C) multirange constructors/operators (`nummultirange()`, `multirange_contains_elem`, `isempty`, `lower_inc`, etc) unimplemented (~32 lines); (D) range set-operator lexer tokens (`&<`, `&>`, `-|-`) missing from `internal/parser` (~85 lines); (E) `@>`/`<@`/`&&` on ranges/multiranges resolves to the `box` opclass instead (`internal/executor/expr.go`, ~17 lines); (F) `range_agg`/`range_intersect_agg`/`multirange_intersect_agg` aggregates registered by name only, no executor impl (~18 lines); (G)/(H) polymorphic `OUT anymultirange` `CREATE FUNCTION` DDL parse error + table-valued-function allowlist gap (same class as M0134-0046 bucket (1)/M0134-0020/M0134-0030, ~13 lines); (I) btree opclass rejects multirange index keys (~2 lines); (J) user-defined `CREATE TYPE ... AS RANGE` constructor auto-gen missing, same root as (B) (~6 lines). No single CONTAINED fix exists (unlike M0134-0046's plpgsql `SET` case) — bucket B is largest but only unlocks constructor calls, while I/O/operators/aggregates are each independent non-trivial subsystem work. Full detail + bucket table: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0047. Mirrors the M0134-0045 storage-engine-gap deferral pattern: needs a `docs/design/` scoping pass (suggested split: (i) range constructors + multirange I/O, (ii) operators incl. lexer tokens, (iii) aggregates, (iv) OUT-param CREATE FUNCTION syntax + multirange btree opclass as smaller side-gaps) before an implementer slice is briefed. No overlap with the already-known cross-file PGDATESTYLE/PGTZ/PGOPTIONS or `\gset` harness gaps. Next M0134 task to select: **M0134-0048 (`create_view.sql`)**.
- [ ] **M0134-0048 — create_view.sql** — regress-sql `failed`: **PARKED 2026-08-21, no engine fix landed** (case still FAILs, 0/1; CSV row stays `failed`). Sizing was contaminated by a harness bug: `scripts/pg-regress-runner.sh`'s `RUN_SETUP` prerequisite phase re-ran `create_view.sql` (and `create_misc.sql`/`create_index.sql`/`create_aggregate.sql`) unconditionally even when one of those was ALSO the requested named test, so the named-test run then re-executed the same file a second time against the same live DB, producing ~350 lines of "already exists" cascade noise unrelated to goopg's engine. **Landed** the fix: added an `is_named_test()` guard so each of those four setup prerequisites is skipped (with a one-line explanatory echo) when it matches a caller-requested named test — verified `create_view` diff drops 2756→2505 lines / 152→50 `^+ERROR`, `select_views`/`int2`/`int4` (unaffected names) still run all four prerequisites normally. This is cross-cutting: it improves sizing accuracy for ANY future M0134 task named `create_misc`/`create_index`/`create_view`/`create_aggregate`. The real (post-fix) 50-`^+ERROR` diff for `create_view.sql` itself is dominated (~80%) by a genuine subsystem gap: goopg has no `ruleutils.c`-equivalent view/rule deparser — `pg_get_viewdef()`/`pg_get_ruledef()` (`internal/executor/expr.go:8970-9018`) just echo the raw CREATE VIEW SQL text captured at parse time instead of PG's canonicalized re-derivation from the analyzed Query tree (confirmed live: `ALTER TABLE ... RENAME` doesn't propagate into a dependent view's stored body the way PG's rule-based re-derivation does). Smaller CONTAINED-ish gaps found alongside: (i) `CREATE SCHEMA name CREATE TABLE ...` nested schema-elements silently dropped (no `CreateSchemaStmt.Elements` AST field anywhere in `internal/parser`, `internal/postmaster/dispatch.go:1896-1936` intercepts standalone CREATE SCHEMA before element lists are ever parsed) — cascades into ~20 later `CREATE VIEW ... FROM base_table` failures in this file; (ii) temp-view auto-promotion missing (`execCreateView`, `internal/executor/operators_ddl.go:5940`); (iii) view WITH-options validation gaps (`security_barrier`/`security_invoker` boolean-value checking, unrecognized-option rejection). Full detail: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0048. Mirrors the M0134-0045/0047 storage/subsystem-gap deferral pattern — needs a `docs/design/` scoping pass on the deparser (also backs `pg_get_ruledef`/`pg_rewrite`) before an implementer slice; CREATE SCHEMA nested elements is a separate, smaller, reusable CONTAINED task worth picking up independently. Next M0134 task to select: **M0134-0049 (`numeric.sql`)**.
- [ ] **M0134-0049 — numeric.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing found 6 independent bugs (diff 3282 lines, 57 `^+ERROR`/134 `^-ERROR`, majority of `-ERROR` a cascade artifact from bucket #1's crash). **Landed** bucket #1: fixed a server-crashing panic in `toCharScientific` (`internal/executor/expr.go`) — `to_char(numeric_special_value, '9.999EEEE')` for `Infinity`/`-Infinity`/`NaN` sliced Go's `%e`-formatted Inf/NaN output (no `"e"` substring) at index -1. Now reproduces PG's fixed `#.#######` output exactly (`postgres/src/backend/utils/adt/formatting.c` isnan/isinf EEEE branch); added `TestToCharScientificSpecialValues`. The regress case no longer crashes/disconnects mid-run. Remaining 5 buckets PARKED: (2) INSERT/assignment-time `numeric(P,S)` typmod truncation never applied outside explicit casts, and is itself float64-lossy; (3) numeric transcendentals (`ln`/`log`/`log10`/`exp`/`power`/`sqrt`) are float64-approximate with hardcoded 6-decimal output instead of PG's arbitrary-precision digit-array math — largest true gap, design-doc scale, comparable to cost-model/WAL bundles; (4) missing builtins `width_bucket`/`div`/1-arg `log10` — small contained addition; (5) column-alias resolution bug on repeated CTE self-reference with per-instance rename; (6) numeric→intN cast of Inf/NaN raises the wrong error message/SQLSTATE. Full detail: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0049. Next M0134 task to select: **M0134-0050 (`numeric_big.sql`)**.
- [ ] **M0134-0050 — numeric_big.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing (diff 1766 lines, 0/1 PASS, 12 `^+ERROR`/0 `^-ERROR`, clean content-mismatch, no crash) found the failure collapses almost entirely into buckets already ledgered under M0134-0049 (typmod truncation not applied at INSERT time, float64-approximate transcendentals now confirmed to include `sqrt`, missing builtin `trim_scale`), plus one genuinely new CONTAINED bug. **Landed** the new bug: `numericMaxDisplayScale` (`internal/executor/numeric.go:157-160`) mis-used PG's typmod-only `NUMERIC_MAX_PRECISION=1000` ceiling as an internal-arithmetic ceiling on intermediate `+`/`-`/`*`/`/` results, spuriously erroring `numeric scale N exceeds 1000 in multiply` on `numeric(1000,800) * numeric(1000,800)`; raised to PG's real internal limit `NUMERIC_DSCALE_MASK=16383`. Verified live: the specific "exceeds ... in multiply" error is gone (diff 1766→1799 lines, `^+ERROR` 12→11 — net diff-line growth is the newly-succeeding multiply now emitting full-width un-truncated rows, expected per the M0134-0049 bucket-2 gap it unmasks, not a regression). Full detail: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0050. No new design-doc work needed — resume via the existing M0134-0049 numeric-precision design-doc scoping. Next M0134 task to select: **M0134-0051 (`partition_info.sql`)**.
- [ ] **M0134-0052 — partition_join.sql** — regress-sql `failed`: **PARKED 2026-08-21, sizing only, no code fix landed.** Diff 6275 lines, 0/1 PASS, every `EXPLAIN (COSTS OFF)` block diverges. Dominant bucket (~95% of diff): partition-wise join is entirely unimplemented in `internal/optimizer` — goopg always joins a plain `Append` of all partition children against the other side instead of pushing the join down per-partition (PG oracle `postgres/src/backend/optimizer/path/joinrels.c:1422 try_partitionwise_join` / `allpaths.c:4362 generate_partitionwise_join_paths`) — a full new optimizer subsystem, not a slice. Four smaller independent gaps also found: parenthesized-join scoping bugs in `internal/parser/select.go` (`tryParseParenJoin`/`isSubqueryStart`, 2 distinct failure modes), unimplemented `TABLESAMPLE` (parser+executor, 2 occurrences), and an expression-keyed LIST-partition INSERT routing failure (1 occurrence, cascades). Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0052. Next M0134 task to select: **M0134-0053 (`partition_prune.sql`)**.
- [ ] **M0134-0053 — partition_prune.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing (diff 6417 lines, 47 hunks, 37 `^+ERROR`, 0/1 PASS) found partition pruning (planner-time AND executor-time) is entirely unimplemented in `internal/optimizer`/`internal/executor` (zero hits for `PartitionPrun`/pruning logic; zero `"Subplans Removed"` output vs 41 in the PG-expected file) — dominant bucket (~85-90% of diff), same "needs a large unimplemented subsystem" class as M0134-0052's partition-wise-join gap; PG oracle `postgres/src/backend/partitioning/partprune.c` (`get_matching_partitions`, `prune_append_rel_partitions`) + `postgres/src/backend/executor/execPartition.c` (`ExecInitPartitionPruning`/`ExecFindMatchingSubPlans`). **Landed** a genuinely independent, contained bug found during sizing: INSERT-time HASH partition routing (`routeToPartitionDepth`, `internal/executor/operators_storage.go`) only ever hashed partition-key column 0, silently ignoring all other columns of a multi-column `partition by hash (a, b, ...)` table (rows with matching `a` but different `b` collided into the same bucket). Fixed by extracting the already-correct multi-column fold algorithm from the `satisfies_hash_partition()` builtin (`internal/executor/expr.go`) into a shared helper `computeHashPartitionRowHash` (`internal/executor/hash_partition.go`) — per-column opclass FUNCTION 2 hash or built-in type hash, NULLs skipped, folded via `pgHashCombine64`, matching PG's `satisfies_hash_partition()`/`compute_partition_hash_value()` (`postgres/src/backend/partitioning/partbounds.c`) — and reusing it at both call sites; routing now always calls `im.FindHashPartitionByHash` (removed the separate, non-PG-faithful string-hash `FindHashPartitionForValue` default-branch call). Added `TestHashPartitionMultiColumnRouting` + `TestHashPartitionSingleColumnRoutingRegression` (`internal/executor/hash_partition_multicol_routing_test.go`); full `internal/executor` package PASS. Verified live: re-running `pg-regress-runner.sh --verbose partition_prune` shows zero remaining "no partition of relation ... found for row" lines (the HASH-routing-error symptom) — the rest of the diff is 100% pruning plan-shape output, unaffected as expected (pruning itself stays PARKED). Two more independent contained bugs sized but NOT landed this loop (left for a future slice, not re-ledgered separately since they're recorded in the M0134-0053 deferral row): (3) nested LIST/RANGE partition overlap false-positive (`internal/executor/operators_ddl_partition.go` `validateListOverlap`/`validateRangeOverlap`/`validateHashBounds` read a contaminated multi-level `PartitionBounds` field — same root-cause family as the already-fixed M0134-0013b `validateDefaultPartition`, needs the identical live-children-filter rewrite); (5) custom multi-char operator `===` fails to lex (2 occurrences, low priority, not investigated). Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0053. Next M0134 task to select: **M0134-0054 (`plancache.sql`)**.
- [ ] **M0134-0054 — plancache.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing (diff 283 lines, 0/1 PASS) found 6 independent buckets. **Landed** 2 contained, independently-real fixes: (A) DETAIL clause on partition-constraint violations (`checkDefaultPartitionInsertConstraint`, `internal/executor/operators_storage.go:3046`, now attaches `Detail: formatRowForDetail(...)` matching PG's `errdetail("Failing row contains %s.", val_desc)` pattern, `postgres/src/backend/executor/execMain.c`); (B) cached-plan result-type-change detection for simple-protocol PREPARE/EXECUTE (`internal/postmaster/conn_tx.go`+`dispatch.go`, EXECUTE now re-plans against the live catalog and raises SQLSTATE `0A000` `"cached plan must not change result type"` on a result-descriptor mismatch, mirroring PG's `RevalidateCachedQuery`, `postgres/src/backend/utils/cache/plancache.c:858`). Added `TestDefaultPartitionConstraintDetail` + `TestPrepareExecuteRejectsResultTypeChange`; full `internal/executor`+`internal/postmaster` packages PASS. Verified live: `pg-regress-runner.sh --verbose plancache` diff no longer contains any `Failing row contains`/`must not change result type` mismatch lines. 4 buckets remain unlanded (temp-namespace shadowing over `t1`/`v1`, plpgsql `RETURN ... FROM relation` raw-SQL-tail parsing, `CREATE SCHEMA` nested elements — already M0134-0048, custom-vs-generic plan-cache heuristic/`plan_cache_mode`). Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0054. Next M0134 task to select: **M0134-0055 (`plpgsql.sql`)**.
- [ ] **M0134-0055 — plpgsql.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing (diff 4602 lines, 112 hunks, 352 `^+ERROR`, 0/1 PASS) found ≥4 independent large-subsystem gaps plus several contained ones. **Landed** 2 contained fixes: (A) `CREATE TABLE ... WITHOUT OIDS` — the mainline `(col defs...)` arm of `parseCreateTableTail` (`internal/parser/ddl.go`) never called `consumeCreateTableSuffix` (only the empty-column-list and `OF type_name` arms did), so it rejected the trailing `WITH[OUT] OIDS` clause with a syntax error; now wired to accept-and-discard `WITHOUT OIDS` and extract `stmt.WithOIDS` so the executor's existing PG18-faithful `WITH OIDS` rejection (`internal/executor/operators_ddl.go:2425`, SQLSTATE `0A000`) fires correctly on this path too — this single miss had cascaded into ~27 `relation "transition_table_level*" does not exist` errors in the "transition tables" section; (B) `ROW(...)` constructor unsupported in PL/pgSQL expression lowering — `lowerPLpgSQLExpr` (`internal/executor/plpgsql_runtime.go`, `default:` branch) had no `case *parser.RowExpr:`, now lowers each field and reuses the existing `optimizer.RowExpr`/`evalRowExpr` composite-text machinery already shared with the SQL planner. Added `TestParseCreateTableWithoutOidsAccepted`, `TestParseCreateTableWithOidsParsesAndFlags` (`internal/parser/ddl_test.go`), `TestPLpgSQLRowExprLowering` (`internal/executor/plpgsql_row_expr_test.go`); full `internal/parser`+`internal/executor` packages PASS. Verified live: `pg-regress-runner.sh --verbose plpgsql` `^+ERROR` count 352→316 (forward progress), case stays overall `0/1 PASS` as expected (still PARKED — remaining gaps are large subsystems). 4 large-subsystem buckets remain PARKED: (2) PL/pgSQL boolean/IF conditions embedding `FROM`/`WHERE` (needs SPI-style full-SQL-grammar expr eval, not a local grammar tweak — `parser.ParseExpr` scalar-only vs PG's `read_sql_expression`/`exec_eval_expr`); (3) `FOREACH` statement entirely unimplemented (new statement kind + runtime loop-over-array, PG oracle `pl_exec.c:3008 exec_stmt_foreach_a`); (7) table-valued PL/pgSQL functions not callable via `SELECT * FROM f(...)`, compounded by qualified `table.column%TYPE` resolution in function signatures failing to parse; (1 remainder) `ALIAS FOR`/parameterized-cursor/`SCROLL` cursor DECLARE-section grammar gaps, `=` as initializer operator. Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0055. Next M0134 task to select: **M0134-0056 (`portals.sql`)**.
- [ ] **M0134-0056 — portals.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing (diff 1213 lines, 15 hunks, 25 `^+ERROR`/14 `^-ERROR`, 0/1 PASS) found 7 independent buckets. **Landed** 3 contained fixes: (A) `FETCH BACKWARD n` (finite) off-by-one — `executeFetch` (`internal/postmaster/dispatch.go`) treated `cur.Pos` (the already-returned current row) as part of the backward window, so it re-returned the just-fetched row instead of stopping before it; fixed to exclude the current row from the finite-backward window (`BACKWARD ALL` was already correct and untouched), verified against PG oracle `postgres/src/test/regress/{sql,expected}/portals.sql,portals.out` foo22/foo23 fixture (`FETCH 23 in foo23; FETCH backward 1 in foo23;` now returns `unique2=21` not `unique2=22`); (B) `DECLARE CURSOR ... WITH HOLD` dead keyword match — `parseDeclareCursor` (`internal/parser/parser.go`) called `p.acceptIdentKeyword("with")`, but `WITH` lexes as reserved `TokenKeyword`/`KwWith`, never `TokenIdent`, so the branch could never match; fixed to `p.acceptKeyword(KwWith)`; (C) `FETCH ABSOLUTE -1`/`FETCH RELATIVE -1` negative-literal parsing — added `parseSignedIntLit` helper, wired into both arms of `parseFetchCursor`. Added `TestCursorFetchBackwardExcludesCurrentRow` (`internal/postmaster/cursor_fetch_backward_test.go`), `TestParseDeclareCursorWithHold`, `TestParseFetchAbsoluteNegative` (`internal/parser/parser_test.go`); full `internal/parser`+`internal/postmaster` packages PASS. Verified live: `pg-regress-runner.sh --verbose portals` `^+ERROR` count 25→4 (forward progress; the 4 remaining are pre-existing unrelated gaps — SQL-function-body `DECLARE CURSOR`, `BINARY`/`INSENSITIVE` cursor keywords), case stays overall `0/1 PASS` as expected (still PARKED — 4 large-subsystem buckets remain). Diff *line count* rose (1213→10874) as a side effect of fix C: `FETCH ABSOLUTE -1` now parses and runs to completion (dumping ~10000 unmatched rows) instead of aborting the transaction with a terse parse-error cascade — expected and reported by the implementer, not a regression (true `ABSOLUTE`/`RELATIVE` positioning semantics are explicitly deferred, see below). 4 buckets remain PARKED: (1, dominant ~45% of diff) `WHERE CURRENT OF` on UPDATE/DELETE parsed but never enforced by the executor (`DeleteStmt.CurrentOf`/`UpdateStmt.CurrentOf` set by parser, zero executor read sites) — needs a live portal current-TID concept, PG oracle `postgres/src/backend/executor/execCurrent.c`; (2) `ABSOLUTE`/`RELATIVE` true positioning semantics not implemented (folded into generic forward/backward count today) and `MOVE` is a complete no-op (`CompatNoopStmt`, never repositions the cursor) — both need a real `Direction` field + shared fetch/move positioning logic; (3) `pg_cursors` system view is a permanent stub (`internal/catalog/catalog.go`, `VirtualRows` returns `nil` unconditionally) — needs per-connection cursor-registry wiring, same pattern as `per_connection_virtual_catalog_scoping`; (4) `BINARY`/`INSENSITIVE` cursor modifiers not parsed, `DECLARE CURSOR` inside a SQL-function body unsupported (`ERROR: unsupported statement type *parser.DeclareCursorStmt`), `EXPLAIN` Materialize-node cosmetics for SCROLL cursors. Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0056. Next M0134 task to select: **M0134-0057 (`prepared_xacts.sql`)**.
- [ ] **M0134-0057 — prepared_xacts.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing (diff 234 lines, 12 `^+ERROR`/7 `^-ERROR`, 0/1 PASS) found 2 LARGE buckets plus 1 CONTAINED bug plus cascade. **Landed** the contained bug: `PREPARE TRANSACTION` of an already-in-use gid on the SERIALIZABLE same-backend keep-open path silently succeeded instead of raising `transaction identifier "..." is already in use` — `execPrepareTransaction` (`internal/postmaster/twophase.go`) only checked the duplicate-gid registry on the RC/RR detach branch; moved the check to run unconditionally and registered a shared marker (`s.preparedXacts.put(st.Gid, nil)`) so both isolation paths see each other's in-use gids, with `execFinalizePrepared` freeing the marker on same-backend finalise and guarding `px == nil` on the detached-path lookup (treats a still-open SERIALIZABLE marker as "does not exist" rather than crashing — cross-backend finalisation of a SERIALIZABLE prepared xact stays out of scope per the file's documented header). Added `TestPrepareTransactionDuplicateGidSerializable`; full `internal/postmaster` package PASS. Verified live: `pg-regress-runner.sh --verbose prepared_xacts` — the targeted `regress_foo3` "already in use" line now matches PG byte-for-byte. 2 LARGE buckets remain PARKED: (1, dominant ~90% of diff) `PREPARE TRANSACTION` on SERIALIZABLE keeps the transaction open on the same connTx handle instead of truly dissociating the backend (PG's `PrepareTransaction` releases the PGPROC and gives the backend a fresh transaction state) — statements on the originating connection between PREPARE and finalise still see the prepared transaction's own uncommitted writes/locks/cursors; needs `DetachToDedicatedSlot` extended to SERIALIZABLE with SSI predicate-lock state re-keyed off the transaction Handle, design-doc scale; (2) `pg_prepared_xacts` system view is a permanent 0-row stub, same pattern class as the already-ledgered `pg_cursors` stub (M0134-0056). Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0057. Next M0134 task to select: **M0134-0058 (`random.sql`)**.
- [ ] **M0134-0058 — random.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing (~230-line diff) found 4 buckets: 2 CONTAINED, 2 LARGE (already ledgered 2026-07-14). **Landed** both contained bugs: (A) parser rejected `( WITH ... SELECT ... )` as a scalar subquery (used by all 5 `ks_test_*()` plpgsql helpers) — added `KwWith` to the paren-subquery-start detection in `parsePrimary` (`internal/parser/select.go:2951,2959`) and its 2 confirmed twins (`parseAnyTail`/`select.go:2440`, `parseInTail`/`select.go:3807`); (B) `trim_scale`/`min_scale` numeric builtins registered in pg_proc but unimplemented (silent NULL) — added both to the builtin dispatch switch in `internal/executor/expr.go` (`evalFuncCall`) per PG's `get_min_scale`/`numeric_trim_scale` (`postgres/src/backend/utils/adt/numeric.c:4253,4302,4323`). Added `TestParseSubqueryExprWithClause`, `TestTrimScale`/`TestTrimScaleNull`/`TestMinScale`; full `internal/parser`+`internal/executor` PASS. Verified live: bucket B's 41-row `trim_scale` query now returns valid numerics with no NULL/error. 2 LARGE buckets remain PARKED (already ledgered 2026-07-14, out of this loop's scope): (C) `random()`/`random_normal()`/`setseed()` use Go's `math/rand` not PG's exact `pg_prng_state` (splitmix64+xoshiro256**) algorithm; (D) `random(numeric,numeric)` computes in float64 with no scale tracking instead of PG's exact big-integer `random_var()`. New bucket found this loop, NOT landed: (E) fixing bucket A unmasked a downstream plpgsql-variable-binder gap (`ERROR: 42703: column "n" does not exist` — a DECLAREd var referenced inside a nested WITH-CTE isn't substituted), affecting all 5 `ks_test_*()` calls; needs its own sizing pass. Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0058. Next M0134 task to select: **M0134-0059 (`rangefuncs.sql`)**.
- [ ] **M0134-0059 — rangefuncs.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing found the diff (originally ~2330 lines) was dominated (~2280 lines) by a server crash truncating the rest of the file, first triggered by `CREATE TEMPORARY VIEW ... JOIN rngfunct(1) WITH ORDINALITY ... ON (n=ord)`, immediately preceded by a related "out of Slot range" error on a bare `select * from rngfunct(1) with ordinality as z(a,b,ord)`. **Landed** the shared root cause: a SQL-language `RETURNS SETOF <composite-table-type>` function's result rows were collapsed to `row[0]` only inside `evalSQLFunctionSetof` (`internal/executor/plpgsql_runtime.go`), silently dropping every column past the first — fixed by packing multi-column rows into composite text via the existing `rowToCompositeText` helper so `userSrfScanOp`/`decomposeCompositeText` can decompose them back on read. Added `TestOrdinalityCompositeSRFSelectStar`; full `internal/executor`+`internal/optimizer` PASS. Verified live: both named queries now match PG, and the crash trigger no longer crashes the server (same root cause). Re-running `pg-regress-runner.sh --verbose rangefuncs` post-fix shows the file no longer crash-truncates (diff now 2524 lines, further into the file) but surfaces several more independent un-landed gaps, first of which is explicit `ord`-column-name resolution in the SELECT list (distinct from `SELECT *`, and from the already-ledgered M0122-0002 scalar-SRF case). Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0059. Next M0134 task to select: **M0134-0060 (`rangetypes.sql`)**.
- [ ] **M0134-0060 — rangetypes.sql** — regress-sql `failed`: **PARTIAL (sizing-only) 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing (diff 2543 lines, `^+ERROR`=234/`^-ERROR`=30, no server-crash) found the same gap family already deeply scoped by the M0134-0047 ledger row (`multirangetypes.sql`): (1) range literal I/O parser near-zero validation/quoting; (2, DOMINANT ~60-70% via cascade) range constructor functions (`numrange()`/`int4range()`/etc) have `pg_proc` metadata but zero executor dispatch case; (3) `-\|-` adjacent-range operator token not lexed; (4) `@>`/`<@`/`&&` on ranges falls through to the `box` opclass; (5) btree rejects range-typed index keys; (6) `OUT`-param `CREATE FUNCTION` without explicit `RETURNS` for polymorphic anyrange functions. No CONTAINED single-function fix exists — cross-referenced rather than re-derived; resume via the M0134-0047 row's bucket taxonomy once a range-constructor/I-O design pass lands. Full breakdown: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0060. Next M0134 task to select: **M0134-0061 (`regex.sql`)**.
- [ ] **M0134-0061 — regex.sql** — regress-sql `failed`: **PARTIAL 2026-08-21, case still `failed` (0/1), CSV row unchanged.** Sizing (669-line diff, 41 `^+ERROR`/5 `^-ERROR`, no server-crash) found the dominant divergence is architectural: goopg's regex evaluator is Go's `regexp` (RE2), which cannot express PG ARE's backreferences (~35% of diff) or lookaround constraints (~30%) by construction, plus silent submatch/complexity-limit semantic drift even where RE2 does compile (~15%) — all LARGE, design-doc-scale (same tier as the M0134-0047/-0060 range-type family), out of scope this loop. **Landed** the one CONTAINED bucket: the SQL-standard regex form `SUBSTRING(str FROM pattern)` was silently misrouted into the numeric-substring path and hard-errored on every call — added a `fromArg.Kind == KindString` branch in `evalSubstr` (`internal/executor/expr.go`) dispatching to new `evalSubstrRegex`, mirroring PG's `textregexsubstr` (`postgres/src/backend/utils/adt/regexp.c:583-627`) via the existing shared `pgPatternToGoRE2`+`regexp.Compile` helper. Added `TestSubstringRegexForm` (8 subtests, verified the non-participating-subexpression-returns-NULL edge case against a live throwaway PG 18.3) + `TestSubstringNumericFormUnaffected` (regression guard); full `internal/executor` PASS. Verified live: the 3 targeted statements now match PG exactly. Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0061. Next M0134 task to select: **M0134-0062 (`reindex_catalog.sql`)**.
- [ ] **M0134-0063 — returning.sql** — regress-sql `failed`: **PARKED 2026-08-21, sizing-only, case still `failed` (0/1, 863-line diff), CSV row unchanged.** Dominant gap (~600+ of 863 lines) is PostgreSQL 18's brand-new `RETURNING old.*/new.*` / `RETURNING WITH (OLD AS alias, NEW AS alias)` feature — 0% implemented in goopg (no `old`/`new` pseudo-relation binding anywhere, no WITH-clause parsing, no dual pre/post-image row materialization for UPDATE/DELETE RETURNING). Second bucket (~150-200 lines): RETURNING-vs-rule-rewrite interaction (missing "cannot perform INSERT RETURNING on relation" check for rule-covered views; auto-updatable-view rejection fires ahead of a covering `DO INSTEAD` rule). Third bucket (small, non-flipping): a couple of unrelated parser gaps (`UPDATE...FROM <bare func call>`, empty-target-list `INSERT...SELECT RETURNING`) plus a cosmetic error-message qualifier omission. No CONTAINED single-slice fix exists — Bucket A alone spans parser grammar + analyzer namespace/alias resolution + planner Var-tagging + executor dual-tuple RETURNING projection + EXPLAIN deparse, design-doc scale. Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0063. Next M0134 task to select: **M0134-0064 (`rowtypes.sql`)**.
- [ ] **M0134-0064 — rowtypes.sql** — regress-sql `failed`: **PARKED 2026-08-21, sizing-only, case still `failed`, CSV row unchanged.** Five independent buckets, none contained: (A, dominant) general `(expr).field` postfix indirection unimplemented (`internal/parser/select.go:3038-3052` only special-cases `(expr).*`) — gates several other buckets from being assessed independently; (B) `record_image_*` operator family (`*<`,`*<=`,`*=`,`*<>`,`*>=`,`*>`) entirely unlexed; (C) `row_to_json()` seeded in `pg_proc` but no executor dispatch exists; (D) composite text I/O fidelity — text-literal-to-named-composite-type cast doesn't quote whitespace/backslash fields and malformed composite literals are silently accepted (also breaks `pg_input_is_valid` for composite types); (E) assorted small gaps gated behind A. Each bucket is independently design-doc scale. Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0064. Next M0134 task to select: **M0134-0065 (`rules.sql`)**.
- [ ] **M0134-0065 — rules.sql** — regress-sql `failed`: **PARKED 2026-08-21, case still `failed` (0/1, 3403-line diff), CSV row unchanged.** Dominant gap (~35+ diverging blocks) is architectural: goopg's `CREATE RULE` only reifies the DO-NOTHING rule form into `catalog.RuleInfo`; any rule with a real DO INSTEAD/DO ALSO action becomes a no-op with no query-rewrite execution anywhere in the executor (no `pg_rules`/`pg_views` either) — REFACTOR-tier, spans parser+catalog+a new planner/rewrite phase+executor. **Landed** the one CONTAINED bucket: `ALTER RULE <name> ON <table> RENAME TO <newname>` (previously a syntax error, 5 occurrences) — new `AlterRuleRenameStmt` parsed in `internal/parser/ddl.go` `parseAlter`, executed by `execAlterRuleRename` (`internal/executor/operators_ddl.go`) renaming `catalog.RuleInfo.Name` in place, 42704/42939/42710 error paths per `postgres/src/backend/rewrite/rewriteDefine.c:793 RenameRewriteRule`. Added `TestParseAlterRuleRename` + `TestDDLAlterRuleRename`; full `internal/parser`+`internal/executor` PASS. This fix does not move the case toward PASS (dominant bucket untouched). Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0065. Next M0134 task to select: **M0134-0066 (`date.sql`)**.
- [ ] **M0134-0066 — date.sql** — regress-sql `failed`: **PARKED 2026-08-21, case still `failed` (0/1, 1568-line diff post-fix), CSV row unchanged.** Dominant gap is architectural: `KindTime` Datum stores date/time as int64 **nanoseconds** since epoch (`internal/executor/datum.go:507-520`), a ±292-year range vs PG's day-count `date`/microsecond `timestamp` — cascades into BC-date rejection, broken `infinity`/`-infinity` sentinels, wrong EXTRACT of ancient dates, and silent wraparound garbage in `make_date`/`make_timestamp`/`make_time` for out-of-range inputs. **Landed** a test-infra fix instead: `scripts/pg-regress-runner.sh` now exports `PGTZ`/`PGDATESTYLE`/`PGOPTIONS` intervalstyle matching upstream `pg_regress.c:783-796` exactly, de-noising every datetime-adjacent regress case's sizing diff (verified zero regression on int2/int4/text quick set; date.sql diff shrank 1606→1568 lines). Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0066. Next M0134 task to select: **M0134-0067 (`domain.sql`)**.
- [ ] **M0134-0067 — domain.sql** — regress-sql `failed`: **PARKED 2026-08-21, case still `failed` (0/1, 1758-line diff), CSV row unchanged.** Two dominant buckets are both already-ledgered cross-case architectural gaps: btree opclass hard-codes int4/numeric comparators (blocks any UNIQUE/PK column of composite/array-of-composite type, ~350-400 diff lines, same as M0134-0047/-0060/-0064); and general subscript/field indirection unimplemented in INSERT column-target/UPDATE SET-target grammar (`col[1]`, `col[1].field`, array-slice — ~250-300 diff lines, extends M0134-0064 Bucket A from SELECT-context into assignment targets). **Landed** the one CONTAINED bucket: `ALTER DOMAIN <name> ADD CONSTRAINT` now rejects with PG's exact `cannot alter type "X" because column "T.C" uses it` (0A000) whenever any table column's type transitively contains the domain via a composite field, domain-of-domain, or range subtype (`catalog.InMemory.FindColumnUsingDomainTransitively`, `internal/executor/operators_ddl.go` `execAlterDomain` "addconstraint" case) — new test `internal/executor/alter_domain_add_constraint_dependency_test.go` covers all 5 PG shapes + a regression guard. Full bucket breakdown + PG oracle citations: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0067. Next M0134 task to select: **M0134-0068 (`drop_if_exists.sql`)**.
- [ ] **M0134-0069 — sequence.sql** — regress-sql `failed`: **IN PROGRESS 2026-08-21, case still `failed` (0/1, 330-line diff, was 359), CSV row unchanged.** Sizing found 6 independent buckets (full breakdown: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0069); none architectural, all CONTAINED but too large for one round. **Landed Buckets 1+2 this loop**: (1) `roundNumericToInt` (`internal/executor/expr.go`) had no overflow check on numeric-literal→int8 coercion — Go's float64→int64 conversion on out-of-range values silently produced garbage instead of erroring `22003 bigint out of range`; rewritten to exact `big.Int` mantissa/scale arithmetic (a float64 bounds check can't detect boundary-adjacent overflow since `strconv.ParseFloat` rounds `-9223372036854775809` to exactly `MinInt64`), matching PG oracle `numeric.c numericvar_to_int64`. (2) unqualified `DROP TABLE t1` RESTRICT-mode dependency scan (`internal/executor/operators_ddl.go` `execDropTable`) passed the raw unresolved parsed name (empty `Schema`) into `viewsDependingOnTable`/`matViewsDependingOnRelation` instead of the resolved `tbl.Schema`/`tbl.Name`, so a same-named table/view in a different schema OR a temp table shadowing a same-named permanent table with real view dependents falsely blocked the drop; fixed to scan by resolved identity plus a Temp-mismatch filter (PG forbids non-temp views from referencing temp relations, so a genuine cross-temp-boundary "dependent" can't exist). New tests `internal/executor/m0134_0069_test.go` (`TestRoundNumericToIntOverflow`, `TestDropTableUnqualifiedIgnoresOtherSchemaDependents`, `TestDropTableTempShadowIgnoresPermanentViewDependents`); full `internal/executor` PASS. **Landed Bucket 4 (partial) this loop**: `ALTER TABLE/SEQUENCE ... RENAME` now propagates to every OTHER table's column `DEFAULT nextval('<oldname>')` literal (rewrites in place + re-syncs the catalog heap), fixing the EXPLICIT-DEFAULT case (new test `TestAlterSequenceRenamePropagatesToDependentDefault`). Diff line count unchanged at 330 — the regress anchor (`sequence.sql:143-145`) exercises an IMPLICIT SERIAL column instead, whose sequence name is synthesized by `<table>_<col>_seq` convention in `internal/optimizer/planner.go` `defaultMarkerReplacement`/`rewriteInsertDefaultMarkers` (no `DefaultExpr` literal to patch there, and that path has no rename-survival fallback yet — see the fresh deferral-ledger row dated 2026-08-21 for the resume point). **Bucket 4 fully landed 2026-08-21 (implicit-serial half)**: added `catalog.FindSequenceOwnedByFunc` (function-var hook wired by `internal/executor/operators_sequence.go` init, mirroring `autoGenerateSerialValues`'s two-step lookup) so `internal/optimizer/planner.go`'s `defaultMarkerReplacement` resolves the CURRENT (renamed) sequence name for implicit SERIAL/IDENTITY DEFAULTs without an optimizer↔executor import cycle; new test `TestAlterSequenceRenamePropagatesToImplicitSerialDefault`; diff shrank 330→307, the `serialtest1_f2_foo` anchor now matches PG byte-for-byte. **Bucket 3 fully landed 2026-08-21 (DROP SEQUENCE RESTRICT dependency check)**: new `columnDefaultsDependingOnSequence` helper (`internal/executor/operators_ddl.go`, sibling to `functionsDependingOnSequence`) scans `im.AllTables(dbOid)` for column DEFAULTs depending on the target sequence — covers implicit SERIAL/IDENTITY-owned sequences (via `catalog.FindSequenceOwnedByFunc`, Bucket 4) and explicit `nextval('seqname')`/`nextval('seqname'::regclass)` literals, correctly EXEMPTING `::text`-cast literals (PG's real `parse_utilcmd.c` rule); `execDropCompat`'s sequence-RESTRICT branch now returns `2BP01`+DETAIL+HINT before dropping. New tests `TestDropSequenceRestrictBlocksImplicitSerialDefault`, `TestDropSequenceRestrictBlocksExplicitNextvalDefault`, `TestDropSequenceRestrictAllowsTextCastNextvalDefault`; diff shrank 307→286, the `t1_f1_seq`/`myseq2`/`myseq3` anchor (sequence.sql:159-167) now matches PG byte-for-byte. Case still `failed` (0/1, 286-line diff) — remaining buckets (full list: ledger row dated 2026-08-21, third M0134-0069 entry): Bucket 5 (sequence ACL/owner enforcement), Bucket 6 (small text gaps), plus `pg_sequence_parameters()` SRF missing, `\d` UNLOGGED-sequence header, orphaned catalog row after cascading `DROP TABLE`, `pg_get_sequence_data` cache-vs-persisted mismatch, and CASCADE-mode column-DEFAULT cascade drop (new, narrower deferral — not exercised by this fixture). **Bucket 5 fully landed 2026-08-21 (sequence ACL/owner enforcement)**: `evalNextval`/`evalCurrval`/`evalSetval`/`evalLastval` (`internal/executor/operators_sequence.go`) now resolve each sequence's catalog table and require USAGE|UPDATE / SELECT|USAGE / UPDATE-only respectively via new `dmlPrivilegePermittedAsAny` OR-wrapper (`operators_storage.go`), denying 42501 `permission denied for sequence <name>`; `execAlterSequence` now owner-checks before mutating params, denying `must be owner of sequence <name>`; sequences now get `Owner` stamped at creation (`createSeqCatalogTable`, previously always empty). New tests `internal/executor/sequence_acl_test.go` (5 tests). Diff shrank 286→275; the ALTER-SEQUENCE-as-non-owner anchor now matches PG byte-for-byte. Case still `failed` (0/1, 275-line diff) — a broader, table-wide gap was discovered and deliberately left unfixed per the brief (see ledger row dated 2026-08-21, fourth M0134-0069 entry): `dmlPrivilegePermittedAs`'s owner-bypass is unconditional, but PG's owner privilege is a revocable implicit aclitem, so `sequence.sql`'s REVOKE-then-selective-GRANT sub-block (lines 645-786) can't fully match without a shared, not sequence-specific, fix. **Bucket 7 partially landed 2026-08-21 (owner-ACL-revocation enforcement, autocommit path only)**: `Catalog.IsOwnerACLRevoked`/`HasOwnerPrivilege` (`internal/catalog/catalog.go`) plus a now-conditional owner-bypass in `dmlPrivilegePermittedAs` (`internal/executor/operators_storage.go`), and a twin sentinel-vs-actual-owner routing fix in `internal/postmaster/grant_ddl.go`'s `tryRecordTableGrant`/`tryRecordTableRevoke` (see ledger row dated 2026-08-21, fifth M0134-0069 entry, for full detail). New tests `TestSequenceOwnerACLRevokedDeniesOwner`, `TestTryRecordTableRevokeMaterializesActualOwner`; no regression (`TestDMLRequiresTablePrivilege` unaffected). Diff HELD at 275 lines (unchanged) — `sequence.sql`'s owner-ACL sub-block runs inside `BEGIN...ROLLBACK`, which GRANT/REVOKE recording doesn't reach (autocommit-only gate in `internal/postmaster/query.go`); a second blocker (`GrantTablePrivilegeAs` wholesale-clears the revoked flags on ANY owner-targeted re-GRANT, not just a full re-grant) would still block full closure even once the first is fixed. Both are new deferral-ledger rows (2026-08-21, sixth/seventh M0134-0069 entries) — resume points there. Remaining buckets: Bucket 6 (small text/HINT/DETAIL gaps), plus `pg_sequence_parameters()` SRF, `\d` UNLOGGED header, orphaned catalog row, `pg_get_sequence_data` cache mismatch, CASCADE-mode column-DEFAULT drop, transactional ACL-recording gap, and the GrantTablePrivilegeAs flag-clearing gap. **Bucket 6 partially landed 2026-08-21 (3 of 5 small text/HINT/DETAIL items)**: `validateSeqOwnedBy` (`internal/executor/operators_ddl.go`) now sets `Hint: "Specify OWNED BY table.column or OWNED BY NONE."` on the `invalid OWNED BY option` error; `execCreateSequence` now emits `NOTICE: relation "<name>" already exists, skipping` on `CREATE SEQUENCE IF NOT EXISTS` against an existing sequence; `execAlterSequence` now emits `NOTICE: relation "<name>" does not exist, skipping` on `ALTER SEQUENCE IF EXISTS` against a missing sequence — all three match `postgres/src/backend/commands/sequence.c` (`process_owned_by`/`DefineSequence`/`AlterSequence`) byte-for-byte. Diff shrank 275→265 lines. **Bucket 6 item 4 landed 2026-08-21**: `validateSeqOwnedBy` (`internal/executor/operators_ddl.go`) now probes `LookupIndex` (with the same schema-qualified fallback as the table lookup) before falling through to the generic 42P01 "does not exist" — naming an index (e.g. `OWNED BY pg_class_oid_index.oid`) now returns `42809` (`ERRCODE_WRONG_OBJECT_TYPE`) `sequence cannot be owned by relation "%s"` + `DETAIL: This operation is not supported for indexes.`, matching PG oracle `postgres/src/backend/commands/sequence.c:1629-1638` (`process_owned_by`) + `errdetail_relkind_not_supported` (`pg_class.c:24-52`) byte-for-byte, confirmed live via cgroup-capped psql probe. New test `TestValidateSeqOwnedByIndexTarget` (`internal/executor/sequence_acl_test.go`). Diff shrank 265→253 lines. Remaining Bucket 6 item: (5) `ALTER SEQUENCE`/other sequence ops on a non-sequence relation give `relation "x" does not exist` (42P01) instead of PG's `cannot open relation "x"` + relkind DETAIL (`ERRCODE_WRONG_OBJECT_TYPE`) — needs a lookup-miss vs wrong-relkind disambiguation, likely shared across `nextval`/`currval`/`setval`/`DROP SEQUENCE` sibling call sites too. See ledger row dated 2026-08-21 (eighth M0134-0069 entry, updated) for resume points. Also newly surfaced in the shrunk 253-line diff (not yet triaged into buckets): `ALTER SEQUENCE ... SET UNLOGGED` doesn't update `\d` header text; ACL enforcement still allows some `nextval`/`currval`/`setval`/`lastval` calls PG denies (bucket 5/7 territory); `information_schema.sequences.sequence_catalog` reports connected DB name not regress DB name (possible harness artifact); `pg_get_sequence_data('test_seq1').last_value` after `CACHE 10` returns 3 instead of PG's 10. **Bucket 6 item 5 landed 2026-08-21 — Bucket 6 now FULLY CLOSED**: new `seqWrongRelkindError`/`seqRelkindNotSupportedDetail` helpers (`internal/executor/operators_sequence.go`) gate `evalNextval`/`evalCurrval`/`evalSetval` and `execAlterSequence` (`internal/executor/operators_ddl.go`) so an existing non-sequence relation name returns `42809 cannot open relation "%s"` + relkind DETAIL BEFORE the generic 42P01/auto-create fallback (critically ordered ahead of `evalSetval`'s phantom-sequence auto-create), matching PG oracle `sequence.c:37-46/66-77` (`sequence_open`/`validate_relation_kind`); `execDropCompat`'s sequence branch got a separate `42809 "%s" is not a sequence"` (no DETAIL) check matching PG's DIFFERENT `objectaddress.c:1362-1368` template — confirmed these are genuinely two distinct PG message shapes, not one shared choke point, before implementing both. `evalLastval` confirmed N/A (no name arg). New tests `internal/executor/sequence_relkind_test.go` (7 cases). Diff shrank 253→239 lines; the `ALTER SEQUENCE serialTest1 CYCLE` anchor now matches byte-for-byte. Case still `failed` (0/1, 239-line diff) — remaining gaps: `pg_sequence_parameters()` SRF, `\d` UNLOGGED header, a newly-discovered `SET LOCAL SESSION AUTHORIZATION`-scoped ACL enforcement gap (distinct from Bucket 5/7), `information_schema.sequences.sequence_catalog` DB-name mismatch, `pg_get_sequence_data` cache mismatch. See ledger row dated 2026-08-21 (ninth M0134-0069 entry) for the full resume point — next slice should size the SESSION AUTHORIZATION ACL gap plus recheck which of the remaining 239 diff lines are still fixture-exercised before continuing. **`\d` UNLOGGED header fully landed 2026-08-21**: researcher audit refuted the "SESSION AUTHORIZATION ACL" gap as new — it's the SAME Bucket-7 blocker already ledgered (GRANT/REVOKE-inside-transaction not reaching the catalog, autocommit-only gate in `internal/postmaster/query.go`), not a distinct SESSION-AUTHORIZATION-plumbing gap; `ctx.NonSuperuserRole` is already correctly threaded from SET SESSION AUTHORIZATION into all four sequence ACL checks. The audit also found `CREATE UNLOGGED SEQUENCE` was a genuine engine bug, not cosmetic: `internal/parser/ddl.go`'s `parseCreateSequenceTail` reused ONE bool param for both `Temporary` and `Unlogged`, so `CREATE UNLOGGED SEQUENCE x` silently marked `x` session-TEMPORARY instead of setting `relpersistence='u'`; `ALTER SEQUENCE ... SET LOGGED/UNLOGGED` was parsed then discarded as a no-op. Fixed: new `CreateSequenceStmt.Unlogged`/`AlterSequenceStmt.SetLogged` fields, both now correctly set `catalog.Table.Unlogged` (which `buildUserPGClassRow` already reads for `pg_class.relpersistence`), confirmed live via cgroup-capped psql — `\d sequence_test_unlogged` now emits `Unlogged sequence "..."` vs `Sequence "..."` matching PG's `describe.c:1857-1861`. New tests `internal/executor/sequence_unlogged_test.go`. Diff shrank 239→226 lines. Case still `failed` (0/1, 226-line diff) — remaining gaps unchanged: `pg_sequence_parameters()` SRF missing, Bucket 7's transactional-ACL-recording blocker (already ledgered, resume point there), `information_schema.sequences.sequence_catalog` DB-name mismatch (harness artifact, not an engine bug — `pg-regress-runner.sh` connects to DB `postgres` not `regression`), `pg_get_sequence_data` cache-vs-persisted mismatch (already ledgered). Next slice: either finish Bucket 7 (the transactional GRANT/REVOKE deferred-catalog-mutation plumbing — sized as NOT a small fix in the 2026-08-21 researcher audit, needs a design pass) or scope the `pg_sequence_parameters()` SRF as a standalone smaller slice. **`pg_sequence_parameters()` SRF landed 2026-08-21**: implemented end-to-end mirroring the existing `pg_get_sequence_data(regclass)` twin across the same 5 dispatch touch-points (`internal/parser/analyzer/analyzer.go` `tableFuncBaseColumns`, new `optimizer.PgSequenceParameters` plan node in `internal/optimizer/plan.go`, `planPgSequenceParameters` in `internal/optimizer/planner.go` wired above the generic "table-valued function not supported" fallback, executor dispatch in `internal/executor/executor.go`, new `internal/executor/operators_pg_sequence_parameters.go` reusing `verifyHeapamResolveTable` for regclass resolution and the existing `catalog.SequenceParamsFunc`/`SeqParams` hook for the 7 output columns — no new plumbing needed, genuinely contained per the researcher's pre-scoping). Matches PG oracle `sequence.c:1740` `pg_sequence_parameters` / `pg_proc.dat:3426-3431` byte-for-byte (verified live against a throwaway PG 18.3 oracle for both default- and explicit-parameter sequences). New test `TestPgSequenceParametersBasic`. Diff shrank 226→217 lines. Case still `failed` (0/1, 217-line diff) — remaining gaps unchanged from the prior entry (Bucket 7 transactional-ACL-recording blocker, `information_schema.sequences.sequence_catalog` DB-name harness artifact, `pg_get_sequence_data` cache-vs-persisted mismatch) plus newly-visible ones noted by the implementer: missing ACL checks on `nextval`/`lastval`/`setval` beyond what Bucket 5/7 already cover. Next slice: pick one of the remaining ledgered gaps, or start the Bucket 7 design doc. **PARKED 2026-08-21** on the same pattern used for every other large M0134 case: 7 buckets landed across many rounds (359→217 diff lines), and every remaining gap is either design-scale (Bucket 7 transactional GRANT/REVOKE catalog-mutation plumbing — needs its own design pass reconciling `relACLEmptied`/`relACLOwnerRevoked`'s display-only origins with a new enforcement consumer) or a harness artifact (`information_schema.sequences.sequence_catalog` DB-name mismatch) or an already-ledgered narrower gap (`pg_get_sequence_data` cache-vs-persisted mismatch). Re-arm trigger: once a Bucket 7 design lands (own milestone-scale task) OR a future loop finds the remaining items add up to a small contained slice, resume here. Next M0134 task to select: **M0134-0070 (`strings.sql`)**.
- [ ] **M0134-0070 — strings.sql** — regress-sql `failed`: **IN PROGRESS 2026-08-21, case still `failed`, CSV row unchanged.** Sizing (researcher) found `scripts/pg-regress-runner.sh --verbose strings` doesn't even complete: the server crashed mid-file on `lpad('hi', -5, 'xy')` (Go rune-slice-bounds panic on a negative target length; PG clamps to 0 and returns `''`), so most of the file had never been diffed. Full bucket list: crash bug (lpad/rpad negative length — fixed this loop); a second, still-open crash bug (`repeat()` with a negative count, same clamp-to-0 pattern, different function); missing `U&'...'`/`U&"..."` Unicode-escape string literals + `UESCAPE` (parser/lexer, REFACTOR-tier); missing string-literal continuation across whitespace; missing `POSITION(x IN y)`/`OVERLAY(...)`/`LIKE ... ESCAPE`/`SIMILAR TO` special grammar forms (REFACTOR-tier, parser/grammar); `reverse(bytea)` wrongly rune-reverses instead of byte-reversing; missing `regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr` builtin family (REFACTOR-tier, 4 new builtins); `regexp_replace` missing backreference (`\1`/`\&`) support; `regexp_matches(...,'g')` only returns the first match instead of iterating; `regexp_split_to_table` entirely unimplemented; `pg_input_is_valid`/`pg_input_error_info` wrong for bytea. **Landed this loop**: `padLeft`/`padRight` (`internal/executor/expr.go`) now clamp negative target length to 0 before rune-slicing, matching PG's `text_lpad`/`text_rpad` (varlena.c) — fixes the crash, `lpad`/`rpad` with negative length now correctly return `''`. New test `internal/executor/pad_negative_length_test.go`. Full diff line count is now 2594 (was unmeasurable past the crash point before this fix) — the run still crashes later, now on `repeat('Pg', -4)` (see resume point). Full breakdown: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0070. **`repeat()` crash fixed 2026-08-21**: `internal/executor/expr.go`'s `"repeat"` case now clamps a negative count to 0 before calling `strings.Repeat`, matching PG's `text_repeat` (varlena.c) — no more crash. New test `TestRepeatNegativeCount` (`internal/executor/pad_negative_length_test.go`). `scripts/pg-regress-runner.sh --verbose strings` now runs to completion (no crash) for the first time — TRUE end-to-end diff is **2624 lines** (0/1 PASS), dominated by pre-existing, unrelated gaps: string-literal continuation syntax errors, bytea trim/overlay-with-`PLACING` syntax gaps, missing `bit_count()`/`crc32c()`/`unistr()` functions, substring-length error-message DETAIL/LINE-context inconsistencies, plus the already-sized REFACTOR-tier buckets (Unicode-escape string literals + `UESCAPE`, `POSITION`/`OVERLAY`/`LIKE ESCAPE`/`SIMILAR TO` grammar forms, `regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr` family, `regexp_replace` backreferences, `regexp_matches(...,'g')` multi-match, `regexp_split_to_table`), and the `reverse(bytea)` rune-vs-byte bug. Also newly discovered this loop (not yet triaged): an apparent empty-string-vs-NULL DataRow wire-encoding bug — `SELECT '';`/`SELECT lower('');`/`SELECT repeat('Pg',0);` may serialize a correctly-empty-string Datum as SQL NULL over the wire in some contexts (implementer's sanity probe found this while testing `repeat('Pg', -4)`; the executor's own Datum was confirmed non-null via `IS NULL` → `f`, so this looks like a constant-projection/DataRow-serialization gap, not an evaluator bug — see ledger row for resume point). Next slice: pick one of the untriaged 2624-line-diff buckets above and size it (start with the string-literal continuation syntax error, both likely small/contained; REFACTOR-tier buckets are multi-file and should be split into their own milestone-scale slices). **DataRow empty-string-vs-NULL bug FIXED 2026-08-21**: confirmed real and higher-severity than initially scoped — unconditionally broken under the extended query protocol (Bind/Execute; hits every JDBC/psycopg-style prepared-statement client, not just a narrow constant-projection edge case) plus a scratch-buffer-state-dependent variant under simple query. Root cause: Go's `nilSlice[0:0]`/`append(nil, ...zero bytes...)` stays `nil`, and 4 call sites that build DataRow cells from non-null `Datum`s treated a `nil` render result identically to the `d.IsNull()` NULL sentinel. Fixed at all 4 sites — `internal/postmaster/dispatch_extended.go` (Bind/Execute row builder, ~521/525) and `internal/postmaster/dispatch.go` (SELECT result loop ~3046, FETCH-from-cursor loop ~3813) — by coercing a nil post-render slice to `[]byte{}` when the source Datum is confirmed non-null. `internal/libpq/frame.go`'s `PutDataRowScratch`/`WriteDataRow` were untouched (correct by contract). New tests `internal/postmaster/datarow_empty_string_test.go` (4 cases, verified load-bearing pre-fix). Full detail: `.ralph/deferral_ledger.md` row dated 2026-08-21, `M0134-datarow-empty-string`. Diff line count for `strings.sql` not yet re-measured post-fix (expected to shrink since several fixture lines depend on empty-string results). Next slice: re-run `scripts/pg-regress-runner.sh --verbose strings` to get the updated diff count, then continue sizing the remaining buckets (string-literal continuation gap first). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit. **String-literal continuation landed 2026-08-21**: `internal/parser/lexer.go`'s plain `'...'` branch and `lexEscapeString` (`E'...'`) both now loop through a new `tryQuoteContinuation()` lookahead (newline-tracking, mirrors `skipWhitespaceAndComments`), matching PG oracle `scan.l:224-239,574-631` (`<xqs>` lookahead state, escape-decoding rules persist across the continuation per `state_before_str_stop`). New test `internal/parser/string_continuation_test.go` (5 cases). `strings.sql` diff shrank 2624→2614 lines (measured post-fix via `scripts/pg-regress-runner.sh --verbose strings`, 0/1 PASS still). Full detail: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0070 (string-literal-continuation entry). Case still `failed` — remaining buckets unchanged from the prior entry (missing builtins `unistr`/`bit_count`/`regexp_instr`/`regexp_substr` now drive the largest remaining diff share; Unicode-escape/bit-string/hex-string literals; `POSITION`/`OVERLAY`/`LIKE ESCAPE`/`SIMILAR TO` grammar; `regexp_replace` backreferences; `regexp_matches(...,'g')` multi-match; `regexp_split_to_table`; bytea trim/overlay edge cases). Next slice: pick one of these — each is REFACTOR-tier (new literal kind, new builtin family, or new grammar form), size as its own milestone-scale slice per the researcher's original sizing pass. **Missing-builtins bucket landed 2026-08-21**: `crc32(bytea)`/`crc32c(bytea)` (`internal/executor/expr.go`, `hash/crc32` stdlib), `bit_count(bytea)` (`math/bits` popcount), and `unistr(text)` (new `internal/executor/unistr.go`, full escape-scanner + UTF-16 surrogate-pair port of PG oracle `varlena.c:6762-6925`) all implemented and value/error-text-matched byte-for-byte against PG (new test `internal/executor/builtin_crc32_bitcount_unistr_test.go`, 18 subtests). `strings.sql` diff shrank 2614→2539 lines. Full detail: `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0070 (missing-builtins entry). Case still `failed` — remaining buckets unchanged (Unicode-escape/bit-string/hex-string literals; `POSITION`/`OVERLAY`/`LIKE ESCAPE`/`SIMILAR TO` grammar; `regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr` family; `regexp_replace` backreferences; `regexp_matches(...,'g')` multi-match; `regexp_split_to_table`) plus a newly-visible cross-cutting gap: goopg emits `LINE N: <query>` + caret-pointer context after `ERROR:` lines that PG's regress fixtures omit — likely worth sizing next since it may be false-diff noise shared across many regress files, not `strings.sql`-specific. Next slice: pick a remaining bucket above, or size the LINE/caret-suppression gap. **LINE/caret cross-cutting gap fixed 2026-08-21 (BinaryExpr/UnaryExpr/shared-arithmetic-helper sites only)**: `internal/executor/expr.go`'s `ExecError` raise sites for pure row-by-row runtime evaluation (division-by-zero, arithmetic overflow, `pg_lsn` overflow, invalid-regex, negative-substring-length, timestamp/interval out-of-range) no longer set `Pos`, matching PG oracle (`postgres/src/backend/utils/adt/{int,int8,float,pg_lsn,timestamp,regexp}.c` — no `errposition()` calls in runtime execution; only lex/parse + literal-constant type-coercion get it, `parse_node.c:140,354-459`). Compiled twin `internal/executor/exprnode.go` mirrored (Rule 4 sibling-path sync); `expr_sibling_parity_test.go`'s wrong-per-PG `Pos != 0` assumption corrected to `Pos == 0`. New tests `internal/executor/expr_error_position_test.go`. `strings.sql` diff shrank 2539→2501 lines; live psql probe confirmed before/after. Full detail + 3 discovered follow-on gaps (unistr.go NOT actually Pos-less as previously assumed; `roundNumericToInt` literal-vs-column-cast Pos conflation; `abs`/`gcd`/`lcm`/`mod` FuncCall sites same pattern, not yet fixed): `.ralph/deferral_ledger.md` row dated 2026-08-21, M0134-0070 (LINE/caret entry). Case still `failed` (0/1, 2501-line diff). Next slice: fix `unistr.go`'s 3 Pos-setting sites (small/contained, same pattern as this loop), or pick a remaining REFACTOR-tier bucket (Unicode-escape/bit-string/hex-string literals; `POSITION`/`OVERLAY`/`LIKE ESCAPE`/`SIMILAR TO` grammar; `regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr` family; `regexp_replace` backreferences; `regexp_matches(...,'g')` multi-match; `regexp_split_to_table`). **`unistr.go` and `abs`/`gcd`/`lcm`/`mod` Pos-strips both landed in later loops (commits c4e230eb, ef633438). `roundNumericToInt`+int2/int4-narrowing Pos-strip landed 2026-08-21**: re-verified against PG oracle (`numeric.c` `numericvar_to_int64`/`numeric_int8_opt_error`/int4/int2 narrowing, none call `errposition()`) that the prior "literal-vs-column classification" framing was wrong — stripped `Pos` unconditionally from all 4 raise sites in `internal/executor/expr.go` (`roundNumericToInt`'s 2 bigint sites, int2/int4 narrowing-check sites in `evalCast`), corrected the backwards `TestLiteralCastOverflowStillCarriesPos` guard into `TestNumericToIntCastOverflowsCarryNoPos` (6 cases). Confirmed this does NOT move `strings.sql`'s diff (no numeric-overflow CAST in that fixture; the real owner is `sequence.sql`/M0134-0069). This closes the LINE/caret-hygiene series's discovered follow-on items (1)(2)(3) — series structurally complete. **Quote-continuation-across-block-comment regression fixed 2026-08-21**: re-running `scripts/pg-regress-runner.sh --verbose strings` (per the "re-run to confirm 2501" next step) surfaced a NEW regression introduced by this same day's earlier string-literal-continuation fix — `internal/parser/lexer.go`'s `tryQuoteContinuation()` wrongly treated a `/* block comment */` in the continuation gap as skippable whitespace (its newlines counting toward the "must contain a newline" rule), so `SELECT 'a' 'b' /* c */ 'd';` wrongly continued/concatenated where PG raises a syntax error — PG's `scan.l` `quotecontinue`/`whitespace_with_newline`/`comment` macros (~lines 215-239) only ever admit `--` line comments into the gap; block comments are a wholly separate `<xc>` start-condition with no reachability into `quotecontinue` at all. Fixed by deleting the block-comment-skip branch from `tryQuoteContinuation`'s scan loop entirely, so a `/*` in the gap now falls straight to `default: break scan` and fails the lookahead (matching PG's `<xqs>{quotecontinuefail}`/`yyless(0)` fallback). New tests in `internal/parser/string_continuation_test.go`: block-comment-in-gap now correctly fails continuation, `--` line-comment-in-gap regression-guarded as still succeeding, and block-comment-as-ordinary-pre-token-whitespace confirmed unaffected. `strings.sql` diff shrank 2501→2461 lines (measured live). The specific fixture line is STILL a diff (not fully closed) — both sides now raise a syntax error for the illegal continuation, but the error-message TEXT still differs (PG: `syntax error at or near "' - third line'"`; goopg: `syntax error at or near "expected ';' or end of input (got  - third line)"`) — this is now a pure error-message-wording gap, not a behavioral one; not sized further this loop. Full detail: `.ralph/deferral_ledger.md` row dated 2026-08-21 (quote-continuation-block-comment entry). Case still `failed` (0/1, 2461-line diff). Next slice: pick a fresh REFACTOR-tier bucket from the list above (Unicode-escape/bit-string/hex-string literals; `POSITION`/`OVERLAY`/`LIKE ESCAPE`/`SIMILAR TO` grammar; `regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr` family; `regexp_replace` backreferences; `regexp_matches(...,'g')` multi-match; `regexp_split_to_table`), or the smaller error-message-text gap just found for the continuation-failure case. **Error-message-wording gap closed 2026-08-21 (commit `eba6009e`)**: `Parse()`'s trailing-token error now uses `errSyntaxAtCur()` (PG-style `syntax error at or near "TOKEN"`, with quote-doubling for string/quoted-ident near-text) instead of a goopg-specific `expected ';' or end of input` message; the continuation-failure fixture now matches PG byte-for-byte. Diff shrank 2461→2454. **`POSITION(sub IN str)` grammar landed 2026-08-21 (commit `c13eba8c`)**: new `parsePositionFuncCall` (`internal/parser/select.go`, mirrors `parseSubstringFuncCall`'s dual-form template) parses both `POSITION(sub IN str)` and the pre-existing comma form, desugaring to the existing `position()`/`strpos` FuncCall dispatch (`internal/executor/expr.go`, no executor change) — args reordered to `{str, sub}` for the IN form to match that dispatch's haystack-first convention (caught via a live regress-gate bug the first implementer round got backwards; fixed in round 2). New tests `internal/parser/position_test.go`. Diff shrank 2454→2404. Case still `failed` — remaining REFACTOR-tier buckets, sized this loop by a researcher pass (full detail in `.ralph/working_set.md`): `regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr`/`regexp_replace`-backrefs/`regexp_matches(...,'g')`/`regexp_split_to_table` family (dominant, ~1215 of 1688 diff lines — needs its own decomposition pass before briefing); `OVERLAY(... PLACING ... FROM ...)` (parser + new executor case); `LIKE ... ESCAPE` (parser-only, smallest remaining); `SIMILAR TO` (parser + new executor POSIX-conversion helper, no `similar_escape` equivalent exists); Unicode-escape `U&'...'`/`UESCAPE` + bit/hex-string literals (~57 lines); `ascii()`/`bit_count()` spacing (~4 lines in this fixture but root cause looks systemic/shared with `domain.diff`/`misc_functions.diff` — do not fix blind here); `chr(0)`/bytea-trim NUL handling (~4 lines). Next slice: `LIKE ... ESCAPE` (parser-only) recommended as the next smallest contained bucket. **`LIKE ... ESCAPE` grammar landed 2026-08-22**: new unreserved keyword `KwEscape`; new `parser.LikeEscapePattern`/`optimizer.LikeEscapePattern` wrapper node (kept out of `BinaryOp` to avoid touching its 43 existing switch sites) wired through the analyzer, planner, and `internal/executor/expr.go`'s `evalExprSlot`, implementing PG's exact `do_like_escape` pattern-rewrite (`postgres/src/backend/utils/adt/like_match.c:392-486`) ahead of the unmodified `matchSQLLike`; covers `LIKE`/`NOT LIKE`/`ILIKE`/`NOT ILIKE ... ESCAPE ...` for both text and bytea. New tests `internal/parser/like_escape_test.go`, `internal/executor/like_escape_test.go`. All 32 `LIKE .. ESCAPE ..` lines in `strings.sql:443-492` now byte-identical to the PG oracle; diff shrank 2404→2135 lines. Case still `failed` — deparser round-trip (CHECK/DEFAULT-expression rendering of a LIKE...ESCAPE predicate) deliberately left unfixed, see ledger row dated 2026-08-22. Remaining REFACTOR-tier buckets unchanged except LIKE-ESCAPE now closed: `regexp_*` family (dominant), `OVERLAY(... PLACING ... FROM ...)`, `SIMILAR TO`, Unicode-escape/bit/hex-string literals, `ascii()`/`bit_count()` spacing, `chr(0)`/bytea-trim NUL handling. Next slice: `OVERLAY` (parser + one new executor case) recommended as the next smallest remaining bucket, or decompose the `regexp_*` family into per-function slices. **`OVERLAY(... PLACING ... FROM ... [FOR ...])` landed 2026-08-22**: new `parseOverlayFuncCall` (`internal/parser/select.go`, mirrors `parseSubstringFuncCall`/`parsePositionFuncCall`; `PLACING` via `acceptIdentKeyword` since it's not a `KwXxx` token) desugars to a single `overlay(...)` FuncCall (3 or 4 args, no `_no_len` split); new `evalOverlay` (`internal/executor/expr.go`, mirrors `evalSubstr`'s byte-indexed algorithm and text/bytea `Kind` branch, reuses the existing `22011` "negative substring length not allowed" for `sp<=0`); `exprType` (`internal/optimizer/planner.go`) gained an `overlay` wire-type case alongside `substr`/`substring`. All 7 OVERLAY lines in `strings.sql:399-406,900-902` now byte-identical to the PG oracle; diff shrank 2135→2076 lines. Deferred: int32-overflow on `sp+sl` (see ledger row dated 2026-08-22, M0134-0070 OVERLAY entry) — not exercised by this fixture. Case still `failed`. **`SIMILAR TO`/`NOT SIMILAR TO [ESCAPE ...]` landed 2026-08-22**: new `KwSimilar` keyword + grammar production (`internal/parser/select.go`, same precedence level as LIKE/ILIKE) with parse-time constant-folding (`buildSimilarTo`) that runs PG's `similar_escape_internal` SQL→POSIX-ERE conversion (new shared leaf package `internal/utils/adt/similarto`, byte-for-byte port of `postgres/src/backend/utils/adt/regexp.c:768-1063`) immediately when Pattern/Escape are literal, emitting a plain `BinaryOp{Op: OpRegexMatch/OpRegexNoMatch, Right: &TypedStringLit{...}}` — the same shape PG's own planner constant-folding produces, so `EXPLAIN (COSTS OFF)` renders `Filter: (f1 ~ '^(?:...)$'::text)` byte-identically (this is why SIMILAR TO could NOT reuse the `LikeEscapePattern` runtime-wrapper template — EXPLAIN parity required constant-folding instead). `ESCAPE ''`/`ESCAPE NULL`/`ESCAPE '##'` (22025) all handled at parse time with PG's exact hint text. All SIMILAR TO/NOT SIMILAR TO statements in `strings.sql:188-221` (8 plain SELECTs + 7 EXPLAIN character-class-preservation cases) now byte-identical to the PG oracle; diff shrank 2076→1915 lines. Deferred (ledger row 2026-08-22, M0134-0070 SIMILAR TO entry): non-literal-pattern runtime-eval path (unwired `parser.SimilarToPattern` AST node, fails cleanly with 0A000, not exercised here) and `SUBSTRING(... SIMILAR ... ESCAPE ...)` (separate unimplemented builtin). Case still `failed` (0/1, 1915-line diff). Next slice: decompose the dominant `regexp_*` family into per-function slices (`regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr`, `regexp_replace` backreferences, `regexp_matches(...,'g')` multi-match, `regexp_split_to_table`) — largest remaining bucket, needs its own sizing pass before briefing — or Unicode-escape `U&'...'`/`UESCAPE` + bit/hex-string literals (~57 lines, smaller, not yet sized in detail). **`E'...'` `\u`/`\U` escape validation landed 2026-08-22 (round A of the 2-round Unicode-escape sizing pass)**: `scanEscapeQuoteInto` (`internal/parser/lexer.go`) now enforces PG's exact-digit-count requirement (22025 "invalid Unicode escape" + HINT on short `\u`/`\U` digit counts), codepoint-validity range (`0 < c <= 0x10FFFF`, 42601 "invalid Unicode escape value at or near ..."), and UTF-16 surrogate pairing (a surrogate-first must be immediately followed by exactly one more `\u`/`\U` escape depicting a valid surrogate-second, else 42601 "invalid Unicode surrogate pair at or near ...") — full port of `postgres/src/backend/parser/scan.l:266-267,642-705,1378-1395` + `pg_wchar.h:534-556`, all 8 error shapes live-oracle-verified. New test `internal/parser/unicode_escape_test.go`. `strings.sql` diff shrank 1915→1848 lines. Round B (`U&'...'`/`U&"..."` Unicode-escape STRING LITERAL syntax + `UESCAPE` clause — new token kind, ~147 diff lines, needs new lexer recognition + shared unescape helper + UESCAPE lookahead, sizing already done — see researcher findings dated 2026-08-22 in `tmp/ralph-handoffs/m0134-0070-unicode-escape-validation/` and this file's history) remains unbriefed. Case still `failed` (0/1, 1848-line diff). Next slice: Round B (`U&` literal support), or decompose `regexp_*`. **Round B (`U&'...'`/`U&"..."` + `UESCAPE`) landed 2026-08-22**: new `lexUnicodeEscapeQuote` dispatch (`internal/parser/lexer.go`, sibling to the existing `E'...'` branch) and `decodeUnicodeEscapes` mirroring PG's `str_udeescape` (`\XXXX` 4-hex, `\+XXXXXX` 6-hex-wide forms — no 8-hex `\U` form for `U&`, that stays `E'...'`-only; reuses Round A's surrogate-pair helpers verbatim plus a newly shared `scanUnicodeEscapeDigitsAt`), and `isValidUescapeChar` mirroring `check_uescapechar` (`postgres/src/backend/parser/parser.c:352-362`). `UESCAPE` is a lexer-local raw-text peek, not a registered keyword. Design `docs/design/m0134-0070-uescape-unicode-literals.md`. New test `internal/parser/unicode_escape_literal_test.go` (14 subtests). `strings.sql` diff shrank 1848→1804 lines. Deferred (ledger row 2026-08-22, M0134-0070 U&/UESCAPE entry): `standard_conforming_strings=off` gate (goopg has no functioning off-mode string lexing at all — dead code otherwise) and PG's dedicated "UESCAPE must be followed by a simple string literal" diagnostic (goopg falls back to a generic syntax error). Case still `failed` (0/1, 1804-line diff). Remaining REFACTOR-tier buckets unchanged: `regexp_*` family (dominant, still needs its own sizing/decomposition pass), `ascii()`/`bit_count()` spacing, `chr(0)`/bytea-trim NUL handling. Next slice: decompose the `regexp_*` family (`regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr`, `regexp_replace` backreferences, `regexp_matches(...,'g')` multi-match, `regexp_split_to_table`) — largest remaining bucket, needs its own sizing pass before briefing. **Round E (`regexp_count`/`regexp_like`/`regexp_instr`/`regexp_substr`) landed 2026-08-22**: new `FuncCall` case arms (`internal/executor/expr.go`) plus shared `regexpInstrSubstrLocate`/`regexpWindowCharPos` helpers and LOCAL dot-matches-newline/expanded-whitespace handling (scoped to only these 4 case arms, does not touch the shared `pgRegexFlagsToGoModifiers`); `exprType` (`internal/optimizer/planner.go`) gained int4/bool/int4/text wire-type cases. All 43 happy-path shapes + 6 named PG error cases in `strings.sql:254-321` (incl. the full endoption×subexpr sweep on a 4-capture-group nested pattern) byte-identical to the PG oracle, live-verified via psql. New test `internal/executor/regexp_instr_family_test.go`. Full detail: `.ralph/deferral_ledger.md` row dated 2026-08-22 (M0134-0070 Round E entry; 2 deferrals: shared flags-helper dot-matches-newline default mismatch for other `regexp_*` callers, and a `^`/`$` re-anchoring simplification under `start>1` window-slicing, neither exercised by this fixture). Case still `failed`. **Round F (`regexp_replace` extended 4/5/6-arg start/N/flags overloads) landed 2026-08-22 (commit `fe97e75f`)**: dispatch by `len(x.Args)` (oids 6251/6252/6253), fixing a pre-existing arg-misread bug (any `len(Args)>=4` blindly read `Args[3]` as flags, silently mis-parsing a real 6-arg call's `start` int). New test `internal/executor/regexp_replace_extended_test.go`. **Round G (`regexp_replace` replacement-string backreference generalization: `\1`-`\9`/`\&`/`\\`/other-`\c` passthrough, was hardcoded `\1`/`\2` only) landed 2026-08-22 (commit `770d6a6d`)**: new `pgRegexpReplacementTemplate` helper (`internal/executor/expr.go`), closes `strings.sql:224`. Pattern-side backreferences (`(.)\1`, RE2-vs-ARE engine gap) remain out of scope/deferred. The `regexp_*` bucket is now fully closed. Case still `failed` — remaining `strings.sql` buckets: `ascii()`/`bit_count()` spacing (~4 lines, verify if systemic/shared with `domain.diff`/`misc_functions.diff` before fixing blind) and `chr(0)`/bytea-trim NUL handling (~4 lines). Next slice: size and fix one of these two small remaining buckets; if both close, re-measure the full `strings.sql` diff and decide whether any residual lines block flipping the CSV row. **`chr(0)`/bytea-trim bucket landed 2026-08-22**: sizing (researcher) CONFIRMED the `ascii()`/`bit_count()` spacing bucket is a cross-cutting `psql` aligned-output column-width bug also present in `misc_functions.diff`/`stats.sql` (`num_nonnulls`, `pg_stat_get_live_tuples` etc.) — NOT a `strings.sql`-specific fix, needs its own dedicated wire-trace investigation slice (deferred, see ledger). The `chr(0)`/bytea-trim bucket was CONTAINED and landed this loop: `chr()` (`internal/executor/expr.go`) now rejects `arg<0` (22023 "character number must be positive") and `arg==0` (54000 "null character not permitted") per PG oracle `oracle_compat.c:1030-1047`, no `Pos` set (matches sibling regexp_* validation errors); `btrim`/`ltrim`/`rtrim` (`internal/executor/expr.go`) gained a `Kind==KindBytes` branch using `bytes.Trim`/`TrimLeft`/`TrimRight` (byte-set membership, matches PG's `dobyteatrim` family, `oracle_compat.c:638-703`) returning `NewBytesDatum`, instead of unconditionally mis-tagging bytea results as text (which then serialized a raw embedded 0x00 on the wire and truncated/blanked the client display — this was the actual end-to-end symptom, not just an internal Datum bug). Also required `exprType()` (`internal/optimizer/planner.go`) to gain a `btrim`/`ltrim`/`rtrim` wire-type case (mirrors the pre-existing `substr`/`overlay` arms) — without it a top-level untyped call still advertised `text`/`unknown` on the wire and hit the same truncation bug even though the internal Datum was correctly `KindBytes`. New test `internal/executor/chr_bytea_trim_test.go` (15 subtests, all 5 brief acceptance criteria + text-mode regression guards). `strings.sql` diff shrank 1804→1029 lines (43% drop, largest single-round drop of the whole sizing pass). Case still `failed`. Discovered follow-on (deferred, not fixed here, orthogonal): `pg_typeof()` on scalar builtin calls without a type-pinning arg (e.g. `pg_typeof(lower('a'))`) returns `unknown` instead of PG's `text`/`int4` — broad pre-existing `exprType()` default-case gap. Remaining buckets in the 1029-line diff: the deferred `ascii()`/`bit_count()` wire-trace bucket, obsolete-SQL99 `SUBSTRING ... SIMILAR ... ESCAPE ...`, `reverse(bytea)` mis-rendering under `bytea_output=hex`, and residual Unicode-escape parser error-message/DETAIL mismatches. Next slice: re-measure/triage the 1029-line diff to size the remaining buckets (start with `reverse(bytea)`, likely small/contained), or open the dedicated `ascii()`/`bit_count()` wire-trace investigation slice per the ledger. **`reverse(bytea)` + `split_part()` buckets landed 2026-08-22**: `reverse()` (`internal/executor/expr.go`) now branches on `Kind==KindBytes` to byte-reverse via `NewBytesDatum` (matches `bytea_reverse`, `varlena.c:3458-3474`) instead of unconditionally rune-reversing `s.StringValue()` (which produced U+FFFD garbage on non-UTF8 bytea payloads); `split_part()` rewritten to match PG's `split_part` (`varlena.c:4621-4750`) exactly — `fldnum==0` now raises `22023` "field position must not be zero", negative field indices now count from the end, and an empty delimiter now returns the whole string for field ±1 / empty otherwise instead of Go's per-rune `strings.Split(s, "")`. `exprType()` (`internal/optimizer/planner.go`) gained a matching `reverse` bytea/text wire-type case (same pattern as the prior loop's `btrim`/`ltrim`/`rtrim` fix — without it the wire layer advertised `text`/`unknown` for `reverse(bytea)` and rendered the byte-correct result as mangled UTF-8). New test `internal/executor/reverse_splitpart_test.go` (15 subtests). `strings.sql` diff shrank 1029→952 lines. Case still `failed`. Deferred (ledger row 2026-08-22, M0134-0070 reverse/split_part entry): `to_hex(int)` on negative arguments prints a signed hex string instead of PG's unsigned two's-complement form — ~4-line follow-on bucket, not fixed this round. Remaining buckets: the deferred `ascii()`/`bit_count()` wire-trace bucket, obsolete-SQL99 `SUBSTRING ... SIMILAR ... ESCAPE ...`, residual Unicode-escape parser error-message/DETAIL mismatches, and now the small `to_hex` negative-int gap. Next slice: `to_hex` negative-int fix (small/contained, same shape as this loop), or open the dedicated `ascii()`/`bit_count()` wire-trace investigation slice per the ledger. **`to_hex(int)` negative-argument two's-complement fix landed 2026-08-22**: new `FuncCall.ArgWidth` plan-time stamp (`internal/optimizer/plan.go`) resolved by a `to_hex` intercept in `resolveExpr` (`internal/optimizer/planner.go`) — defaults to `"int4"` unless the arg's static type is `int8`/`bigint` and the arg is not a bare (or unary-negated bare) integer literal, mirroring the `generate_series` literal-defaults-to-int4 carve-out; `internal/executor/expr.go`'s `case "to_hex":` now `uint32`- or `uint64`-casts before `%x` formatting (was unconditional signed `%x`) matching PG oracle `to_hex32`/`to_hex64` (`varlena.c:5254-5267`); `FoldConstants` (`internal/optimizer/foldconst.go`) preserves `ArgWidth` across FuncCall clones; `exprType` gained a `to_hex`→`text` wire-type case (was falling through to `unknown`). New test `internal/executor/to_hex_test.go` (4 subtests, byte-exact vs `strings.out:2306-2326`). `strings.sql` diff shrank 952→941 lines. Case still `failed`. Deferred (ledger row 2026-08-22, M0134-0070 to_hex entry): `to_bin`/`to_oct` are cataloged but wholly unimplemented and will need the identical `ArgWidth` treatment when added. Remaining buckets: the deferred `ascii()`/`bit_count()` wire-trace bucket, obsolete-SQL99 `SUBSTRING ... SIMILAR ... ESCAPE ...`, residual Unicode-escape parser error-message/DETAIL mismatches. Next slice: open the dedicated `ascii()`/`bit_count()` wire-trace investigation slice (largest remaining named bucket), or size the `SUBSTRING ... SIMILAR ... ESCAPE ...` obsolete-SQL99 form. **`SUBSTRING(str SIMILAR pattern ESCAPE escape)` SQL:2003 form landed 2026-08-22** (commit `0a04e518`): `parseSubstringFuncCall` (`internal/parser/select.go`) gained a mandatory-`ESCAPE` `SIMILAR` branch that parse-time constant-folds literal operands via new `similarto.ConvertSubstring`, landing unchanged on the existing `evalSubstrRegex` path. `strings.sql` diff shrank 941→857 lines (11/13 statements byte-exact). Deferred (ledger row 2026-08-22): (1) 1/13 case mismatches — Go RE2 leftmost-first vs PG POSIX-ARE leftmost-longest, engine-swap scale; (2) `2200C` error omits PG's CONTEXT-stack line, no such mechanism exists in goopg yet. **`to_bin(int)`/`to_oct(int)` landed 2026-08-22**: `internal/executor/expr.go` gained `case "to_bin":`/`case "to_oct":` (base-2/8 `convert_to_base`-shape zero-extension via `strconv.FormatUint`); `internal/optimizer/planner.go`'s `to_hex` ArgWidth intercept folded into one shared `to_hex`/`to_bin`/`to_oct` branch. New test `internal/executor/to_bin_to_oct_test.go`. `strings.sql` diff shrank 857→783 lines. Deferred (ledger row 2026-08-22): (a)/(b) `escape_string_warning` GUC + `standard_conforming_strings=off` backslash-lexing entirely unimplemented (REFACTOR-tier, new bucket); (c) psql column-width padding mismatch — duplicate sighting of the already-tracked `ascii()`/`bit_count()` bug. Case still `failed`, CSV row unchanged. Next slice: size/brief the `escape_string_warning`/`standard_conforming_strings=off` bucket, or one of the other CONTAINED buckets from the 2026-08-22 sizing pass (get_bit/set_bit/get_byte/set_byte — cleanest remaining; bytea↔intN casts; sha224/sha384 + sha256/512 bytea-return fix; pg_input_is_valid/pg_input_error_info bytea fix). **`get_bit`/`set_bit`/`get_byte`/`set_byte` landed 2026-08-22 (commit `e93800b1`)**: all 4 bytea builtins (previously catalogued but entirely unimplemented — errored `function X does not exist`) added as new `case` arms in `internal/executor/expr.go`'s function-call switch, matching PG oracle `byteaGetByte`/`byteaSetByte`/`byteaGetBit`/`byteaSetBit` (`varlena.c:3310-3448`): LSB-first bit numbering, SQLSTATE `2202E` out-of-range errors with PG's exact message text, `set_bit`'s `22023` new-bit-must-be-0-or-1 validation, no `Pos` set (no PG oracle site calls `errposition()`). Also required `exprType` (`internal/optimizer/planner.go`) to gain `set_byte`/`set_bit`→bytea and `get_byte`/`get_bit`→int4 wire-type stamps — without them the wire renderer mis-serialized the correctly-computed `KindBytes` result (same M0125-0021-class gap already fixed for `decode`/`substr`/`overlay`/`btrim` et al.), so this bucket was NOT self-contained as the sizing pass assumed; the planner fix was folded in rather than escalated (2-case addition mirroring an already-landed pattern). New test `internal/executor/get_bit_set_bit_test.go`. `strings.sql` diff shrank 783→728 lines, bucket fully cleared (grep-confirmed zero residual `get_bit`/`set_bit`/`get_byte`/`set_byte` diff lines). Case still `failed`. Next slice: bytea↔intN casts (~138 lines, needs a new width-disambiguation mechanism, bigger lift), sha224/sha384 missing + sha256/sha512 wrongly TEXT-not-BYTEA (~60 lines), or pg_input_is_valid/pg_input_error_info bytea fix (~30 lines) — all from the same 2026-08-22 sizing pass, re-verify sizes against the fresh 728-line diff before picking since fixes may have shifted residuals. **`pg_input_is_valid`/`pg_input_error_info` bytea fix landed 2026-08-22 (commit `121a72b2`)**: both switches — the inline `pg_input_is_valid` case block in `internal/executor/expr.go` and the `pg_input_error_info` SRF switch in `internal/executor/operators_pg_input_error_info.go` — lacked a `case "bytea":` and fell through to the varchar-length `default:`, so malformed bytea literals were always reported valid with all-NULL error columns. Both now call the existing `byteaIn` parser (`bytea.go`, mirrors PG oracle `byteain()` in `varlena.c`) and surface its `*ExecError` Message/Code (`22023` odd-hex-digits/bad-hex-digit, `22P02` invalid-escape-syntax) verbatim — sibling-pair fix, no new mechanism, reuse only. New test `internal/executor/bytea_input_error_info_test.go`. `strings.sql` diff shrank 728→691 lines. Case still `failed`. Next slice: bytea↔intN casts (~138 lines, still needs the width-disambiguation mechanism) or sha224/sha384 missing + sha256/sha512 wrongly TEXT-not-BYTEA (~60 lines, also needs a wire-type stamp per the researcher's Round-note) — re-verify sizes against the fresh 691-line diff before picking. **`escape_string_warning` GUC no-op registered 2026-08-22**: `internal/utils/misc/defaults.go` `BuildDefaultRegistry()` now registers `escape_string_warning` (bool, BootVal `on`, ContextUserset, ScopeSession|ScopeTransaction, no FlagReport — matching PG oracle `guc_tables.c:1844-1851`: PGC_USERSET, boot_val `true`, flags NULL/not GUC_REPORT), so `SET`/`SHOW escape_string_warning` succeed instead of erroring `unrecognized configuration parameter`; `postgresql.conf.sample` gains the matching comment line; new test `internal/utils/misc/escape_string_warning_test.go`. The actual lexer-side deprecation warning (`nonstandard use of \\ in a string literal` under `standard_conforming_strings=off`) stays unimplemented — deferral ledger row 2026-08-22. Case still `failed`. **sha224/sha384 + sha256/sha512 bytea-return landed 2026-08-22 (commit `4109aea4`)**: sha224/sha384 added as new `case` arms (`crypto/sha256.Sum224`/`crypto/sha512.Sum384`, previously fell through the switch → `function does not exist`) and sha256/sha512 fixed to return `NewBytesDatum(h[:])` (KindBytes) instead of hex TEXT (`NewStringDatum(hex.EncodeToString(...))`); input path mirrors the sibling `crc32` arm (`BytesValue()` + `byteaIn` fallback for non-bytea Kind); `exprType` (`internal/optimizer/planner.go`) gained the mandatory `case "sha224","sha256","sha384","sha512" -> bytea` wire-type stamp (the builtin pg_proc seed does not feed `ReturnType`); `isKnownBuiltinFunction` (`internal/executor/operators_call.go`) gained the three new names. New test `internal/executor/sha_test.go` (8 subtests, KindBytes + byte-exact digests + advertised bytea column type vs `strings.out:2334-2380`). `strings.sql` diff shrank 691→599 lines (grep-confirmed zero residual sha divergence). Deferred (ledger 2026-08-22): the adjacent pgcrypto `digest()` arm still returns hex TEXT not bytea. **bytea↔intN casts landed 2026-08-22 (commit `c262a3ea`)**: all six explicit int↔bytea casts (`pg_cast.dat:323-335`, funcOIDs 6367-6372) now byte-exact — forward `intN_bytea` via an `evalCastTyped` intercept keyed on the already-stamped `CastExpr.SourceType` (the anticipated "new width-disambiguation mechanism" was WRONG — it already existed as `plan.go:540-546`, set `planner.go:13662`, preserved by fold/remap/shift), reverse `bytea_intN` via new `case KindBytes:` in the int2/int4/int8 arms of `evalCast` + shared `byteaToIntN`/`byteaIntSourceWidth` (MSB-first, len>width→22003 no-Pos, short zero-extend, uint→signed min-value wrap). No parser/planner/catalog/fold change needed. New `internal/executor/bytea_int_cast_test.go` (27 subtests). `strings.sql` diff shrank 599→451 lines, bucket fully cleared. Deferred (ledger row 2026-08-22, M0134-0070 bytea↔intN entry): (1) `typename(expr)` functional-cast syntax `bytea(int4_col)`, (2) EXECUTE-bound-param `coerceExecParam` bytea arm, (3) bare `5::bytea` int8-vs-int4 literal-typing quirk. Remaining buckets in the 451-line diff: the deferred `ascii()`/`bit_count()` psql-column-width wire-trace (ledgered, needs a dedicated wire-trace slice), `standard_conforming_strings=off` lexing + `escape_string_warning` warning path (REFACTOR-tier, ledgered), residual Unicode-escape error-message/DETAIL mismatches, and the deferred pgcrypto `digest()` hex-vs-bytea. Next slice: re-measure/triage the 451-line diff to confirm the remaining buckets and pick the next contained fix (or open the dedicated `ascii()`/`bit_count()` wire-trace investigation slice per the ledger). **`ascii()`/`crc32`/`crc32c`/`bit_count` wire-TypeOID landed 2026-08-22 (commit `e8eb7214`)**: a researcher retriage root-caused the deferred `ascii()`/`bit_count()` column-width bucket — it needed NO wire-trace; `exprType` (`internal/optimizer/planner.go`) had no cases for these four builtins, so they fell through to the default `catalog.Type{Name:"unknown"}` and the wire layer (`typeOIDFor`, `internal/postmaster/dispatch.go:3639`) advertised TypeOID 25 (text), making psql's `column_type_alignment` (print.c:3614-3638) left-align the numeric columns. Fix: `case "ascii": -> int4` and `case "crc32","crc32c","bit_count": -> int8` arms mirroring the `get_byte`/`get_bit` arm (pg_proc.dat: ascii→int4 :3610; crc32/crc32c→int8 :7954/:7957; bit_count→int8 :1534/:4201). No sibling/twin — all RowDescription paths call `typeOIDFor(sc.Type)`. New test `internal/optimizer/expr_type_wire_test.go`; `\gdesc`-verified wire flip (ascii→OID 23, crc32/crc32c/bit_count→OID 20). `strings.sql` diff shrank 451→395 lines (three hunks closed). Cross-cutting: also fixes the same column-width class in misc_functions/domain/stats. Deferred (ledger 2026-08-22): `bit_count(bit)` overload (OID 6163) unimplemented at runtime (only bytea OID 6162). Remaining buckets in the 395-line diff: `standard_conforming_strings=off` lexing + `escape_string_warning` (REFACTOR-tier, 69), RE2-vs-ARE regex backrefs/zero-width/SIMILAR-ambiguity (28), Unicode-escape error-message/DETAIL text (16), `char(20) '...'` typed-literal grammar (14), SQL99 `SUBSTRING FROM..FOR` + missing CONTEXT (8), regexp error-path HINT (7), lpad/rpad empty-fill value bug (6), bytea LIKE (6), toasttest `pg_relation_size` NULL (2). Next slice: `lpad`/`rpad` empty-fill value bug (expr.go:15049-15051/15077-15079, `if fill == "" { fill = " " }` — PG does NOT pad, oracle_compat.c:196-197) — smallest remaining CONTAINED value-bug (~6 lines). **lpad/rpad empty-fill value bug landed 2026-08-22 (commit `db647aad`)**: `padLeft`/`padRight` (`internal/executor/expr.go:15049-15051/15077-15079`) substituted a space for an explicitly-empty third argument (`if fill == "" { fill = " " }`), so `lpad('hi',5,'')` returned `'   hi'` and `lpad('hi',1,'')` returned `' '`; PG (`oracle_compat.c:196-197/294-295`) sets `len=s1len` when `s2len<=0` — no padding, but truncation (which runs BEFORE the empty-fill check) still applies. Fixed both siblings to `if fill == "" { return s }` (the pre-existing `len(runes) >= n` early return already does the truncation half; the 2-arg call-site default `fill := " "` is PG's separate `lpad(text,int)` overload and untouched). New test `internal/executor/pad_empty_fill_test.go` (8 subtests, criteria 1-6 + rpad 2-arg sibling). `strings.sql` diff shrank 395→367 lines (grep-confirmed zero residual lpad/rpad lines). Remaining buckets in the 367-line diff: `standard_conforming_strings=off` lexing + `escape_string_warning` (REFACTOR-tier), RE2-vs-ARE regex backrefs/zero-width/SIMILAR-ambiguity, Unicode-escape error-message/DETAIL text, `char(20) '...'` typed-literal grammar, SQL99 `SUBSTRING FROM..FOR` + missing CONTEXT, regexp error-path HINT, bytea LIKE, toasttest `pg_relation_size` NULL. Next slice: `bytea LIKE` (~6 lines, next-smallest CONTAINED) — re-verify its size against the fresh 367-line diff before briefing, or pick one of the other small contained buckets (regexp error-path HINT, toasttest `pg_relation_size` NULL). **`bytea LIKE` bucket landed 2026-08-22 (commit `0ea6836d`)**: the ANALYZER gate (`internal/parser/analyzer/analyzer.go:1499-1505`, `OpLike`/`OpNotLike`) required both operands string-like and excluded `bytea`, so `SELECT * FROM byteatest WHERE a LIKE '%1%'` raised `ERROR: operator LIKE requires string operands` instead of PG's `(0 rows)`. Widened with a bytea lane via new `isByteaOrUnknown` helper — accepts string/string (existing) OR bytea-or-unknown/bytea-or-unknown (bytea/bytea, bytea/unknown, unknown/bytea); still rejects bytea/text (42804). Executor untouched (already bytea-ready: `datumAsString`/`matchSQLLike` = PG `bytealike`, `like.c:326`); `bytea ~~ bytea` operator row already seeded (`pg_operator.dat:2383-2384`); `isStringLike` deliberately left un-widened (shared with OpConcat where bytea must stay rejected). Design `docs/design/m0134-0070-bytea-like.md`. New test `internal/parser/analyzer/analyzer_test.go::TestAnalyzeByteaLike` (all lanes incl. bytea/text 42804 + int||bytea 42883 negative guards). `strings.sql` diff shrank 367→350 lines (grep-confirmed zero residual bytea-LIKE lines). Deferred (ledger row 2026-08-22, M0134-0070 bytea-LIKE entry): `bytea_col LIKE pat ESCAPE e` lane — `LikeEscapePattern` analyzer arm (`analyzer.go:1257-1268`) hardcodes `text` for the wrapped right operand. Remaining buckets in the 350-line diff: `standard_conforming_strings=off` lexing + `escape_string_warning` (REFACTOR-tier), RE2-vs-ARE regex backrefs/zero-width/SIMILAR-ambiguity, Unicode-escape error-message/DETAIL text, `char(20) '...'` typed-literal grammar, SQL99 `SUBSTRING FROM..FOR` + missing CONTEXT, regexp error-path HINT, toasttest `pg_relation_size` NULL. **toasttest `pg_relation_size(reltoastrelid)` NULL — hunk 1 landed 2026-08-22 (commit `db50b5de`, J1 only)**: new `catalog.InMemory.ToastRelFileNodeByOID` (`catalog.go:1282`) maps the synthetic TOAST relation range `[100M,200M)` to its main-fork RelFileNode (`ToastParentTable` + `ToastRelFileNode(RelFileNode(parent))`; the index range `[200M,300M)` deliberately still returns false — never routed through `ToastRelFileNode`, which returns the wrong file); wired as a third branch in `relationFileNodeForOID` (`expr.go:5094`) so `evalPgRelationSize`/`evalPgTableSize` resolve a toast relid instead of NULL. New test `TestPgRelationSizeResolvesToastRelid`. `strings.sql` diff shrank 350→348 lines (hunk 1 closed, grep-confirmed). Deferred (ledger row 2026-08-22): hunk 2 (`toast_tuple_target=4080` keeping 3000-byte values inline) — `ToastLargeColumnsIfNeeded` uses a fixed `ToastThreshold=2000`, and threading the target touches the shared runtime-toast path (J2, wider blast radius). Remaining buckets in the 347-line diff: `standard_conforming_strings=off` lexing + `escape_string_warning` (REFACTOR-tier), RE2-vs-ARE regex backrefs/zero-width/SIMILAR-ambiguity, Unicode-escape error-message/DETAIL text, `char(20) '...'` typed-literal grammar, SQL99 `SUBSTRING FROM..FOR` + missing CONTEXT, toasttest `pg_relation_size` hunk 2 (J2). **regexp error-path HINT landed 2026-08-22 (commit `1c6e8a86`)**: new digit-first guard in `evalFuncCall` `case "regexp_replace"` `case 4:` flags branch (`internal/executor/expr.go:12474`) returns `&ExecError{Code:"22023", Message:"invalid regular expression option: \"" + flagsStr + "\"", Hint:"If you meant to use regexp_replace() with a start parameter, cast the fourth argument to integer explicitly."}` — mirrors PG oracle `textregexreplace` (`postgres/src/backend/utils/adt/regexp.c:673-684`), which hints when the 4th arg is non-empty and its first byte is `'0'..'9'` (the user probably meant the start-parameter form). Prints the WHOLE `flagsStr` (`pg_mblen_range`) so `"1z"` does not truncate to `"1"`. Single-site — the shared `pgRegexFlagsToGoModifiers` (8 callers) stays unhinted, because PG fires this hint only in `textregexreplace`; `regexp_matches(...,'1')` etc. raise the same 22023 via `parse_re_flags` with no hint. New test `internal/executor/regexp_replace_hint_test.go` (4 tests: digit-first hint, whole-opt `"1z"`, non-digit `"z"` unhinted, sibling `regexp_matches` digit-first unhinted). `strings.sql` diff shrank 348→347 lines (the one missing `-HINT:` line at `strings.diff:193`, grep-confirmed zero residual regexp-HINT lines). Case still `failed`. **Unicode-escape error-message wording landed 2026-08-22 (commit `f6557f64`)**: `decodeUnicodeEscapes` (`internal/parser/lexer.go`) dropped the E-string `at or near "…"` suffix from its six 42601 surrogate-pair/escape-value messages (bare `invalid Unicode surrogate pair` / `invalid Unicode escape value`, matching PG oracle `str_udeescape`/`check_unicode_value` `parser.c:341-348,371-527` — position-only carets); `lexUnicodeEscapeQuote` gained the dedicated `UESCAPE must be followed by a simple string literal at or near "<token>"` fallthrough for a non-SCONST third token (PG `base_yylex` `parser.c:271-274`, raw-source echo via `scanner_yyerror` `scan.l:1221-1240`) and appends the raw SCONST text to the `invalid Unicode escape character` message. `scanEscapeQuoteInto` (E-string) and `unistr.go` deliberately untouched (different PG convention there). `strings.sql` diff shrank 327→279 lines, the `@@ -56,39 +56,39 @@` hunk fully closed (grep-confirmed). No deferral — pure message parity, complete. Case still `failed` (0/1). Remaining buckets in the 279-line diff (researcher re-size 2026-08-22): `char(20) '...'` typed-literal grammar (~13, CONTAINED, parser-only — recommended next), SQL99 `SUBSTRING FROM..FOR` C1 half (~7, contained, `similarto.ConvertSubstring` fold) vs C3 CONTEXT-line (cross-cutting `sql_exec_error_callback` gap) + `bcdefg` SIMILAR RE2-vs-ARE result (separate), toasttest `pg_relation_size` hunk 2 (J2, wider toast-path blast radius), `standard_conforming_strings=off` lexing + `escape_string_warning` (REFACTOR-tier). **`char(20)`/`varchar(N)`/`bpchar(N)` typed-literal grammar landed 2026-08-22 (commit `c0eeda76`)**: `tryTypedLiteral` (`internal/parser/select.go:3210-3233`) now detects `typename ( N ) 'string'` for `char`/`varchar`/`bpchar` (peek `( intlit ) stringlit`), consuming 5 tokens into `TypedStringLit{Type: name, Value: strTok.Value}` and discarding the typmod — mirrors the existing `interval ( p ) 'lit'` arm; `text`/`numeric`/etc. and a non-int typmod (`char(x)`) deliberately fall through to the normal parse. The two `char(20) '...'` concat statements in `strings.out:1926/1932` now match PG byte-for-byte; `strings.sql` diff shrank 279→253 lines (grep-confirmed zero residual `char(20)`). New test `internal/parser/char_typmod_literal_test.go`. Deferred (ledger row 2026-08-22): character-type typmod enforcement — PG's `bpchartypmodin`/`varchartypmodin` blank-pad `char(N)` to N and raise 22001/22023 for over-length/`char(0)`, but goopg returns the raw string for every N (`TypedStringLit` carries no `Typmod` field yet; both fixture lines are concat forms where PG's observable output is unpadded, so ignoring the typmod is byte-correct here). Case still `failed` (0/1, 253-line diff). **SQL99 `SUBSTRING(str FROM pattern FOR escape)` obsolete-form C1 half landed 2026-08-22 (commit `9eb53c97`)**: `parseSubstringFuncCall`'s FROM/FOR branch now detects both operands as string-or-NULL literals (`similarToLiteralValue`) and routes through the existing `buildSubstringSimilar` fold — PG's `substring(text,text,text)` overload resolution (gram.y `substr_list` `a_expr FROM a_expr FOR a_expr`). Position form (integer literals) unaffected. New test `internal/parser/substring_from_for_test.go`; design `docs/design/m0134-0070-substring-similar-escape.md` addendum. `strings.sql` diff shrank 253→235 lines (grep-zero residual `SUBSTRING('abcdefg' FROM 'a#`). Deferred (ledger 2026-08-22, M0134-0070 FROM/FOR row): typed/non-literal text operands (`FROM 'pat'::text FOR '#'`) still route to the position form — full plan-time overload dispatch not implemented. Case still `failed` (0/1, 235-line diff). **PARKED 2026-08-22** on the established park-when-REFACTOR-tier-remains pattern: every remaining bucket is non-contained — `standard_conforming_strings=off` off-mode string lexing + `escape_string_warning` deprecation-warning path (REFACTOR-tier, multi-file lexer, largest remaining share); RE2-vs-ARE regex-engine gaps (`bcdefg` SIMILAR leftmost-longest, pattern-side backrefs `(.)\1`, `regexp_matches(...,'g')` multiline zero-width adjacency, invalid-regex compile-error text) — all require a backtracking/ARE-compatible engine, no small fix; the `CONTEXT: SQL function "substring" statement 1` line on the 2200C error (cross-cutting `sql_exec_error_callback` stack mechanism, none exists in goopg); and toasttest `pg_relation_size` hunk 2 (J2, `toast_tuple_target` threading into the shared runtime-toast path, wide blast radius). Re-arm trigger: a future loop lands `standard_conforming_strings=off` lexing or an ARE-compatible regex engine, at which point the remaining `strings.sql` diff lines become reachable. Next M0134 task to select: **M0134-0071 (`equivclass.sql`)**.
- [ ] **M0134-0071 — equivclass.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
  **PARKED 2026-08-22** — sized at HEAD (`05a3448a`): 594 diff lines / 11 hunks / 40 `^+ERROR` / 0 `^-ERROR`, deterministic across two fresh-cluster runs (NOT stale; CSV row `failed` accurate). 34/40 error lines were one root cause + cascade: goopg's `execCreateFunction` language allowlist (plpgsql/sql/c only) rejected `LANGUAGE internal`, so the `int8alias1`/`int8alias2` shell types never got their I/O/comparison/hash routines. Shipped Bucket A (design `docs/design/m0134-0071-language-internal-function.md`, commit `f70edc85`): accept `internal` in the allowlist (sibling pair `execCreateFunction` ↔ `execCreateProcedure`), bind `AS '<name>'` against the pg_proc seed via new `catalog.LookupBuiltinProcByProname` (unknown → 42883, PG's `fmgr_internal_validator` `pg_proc.c:746/770-771` — NOT 42704 as the sizing first cited), real Datum-level dispatch in `dispatchStoredRoutineByLanguage` (int8eq/ne/lt/le/gt/ge, btint8cmp, int8in/out, hashint8), plus `pgHashInt8` (PG `hashfunc.c:84`). Result **594 → 573 diff lines, 40 → 28 `^+ERROR`** (the 10 `language "internal"` lines gone; 2 of 4 cross-type `only boolean operators` cleared). The sizing's "cascade of A" attribution for the 13 `incompatible operand types` errors was WRONG — they are an INDEPENDENT analyzer gap: `isComparable(int8,int8alias1)` = false because the BinaryOp type-check (`analyzer.go:1359-1362`/`:3186`) is a builtin name-switch with no user-operator lookup, so a user-defined `= (int8,int8alias1)` operator is never consulted. CSV row stays `failed`/`pass_required=no`; no `make regen-testport`. **Re-arm trigger:** re-run `scripts/pg-regress-runner.sh --verbose equivclass`, then take the analyzer user-operator-resolution gap (16 of 28 remaining errors — the largest contained next slice, engine-wide) first, then Bucket C (built-in `integer_ops` opfamily rows absent, 6 errors). Both ledgered 2026-08-22.
- [ ] **M0134-0072 — temp.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
      **Bucket A + B landed 2026-08-22** (design
      `docs/design/m0134-0072-oncommit-temp-table.md`): `ON COMMIT {DELETE ROWS|DROP}`
      is now parsed (was parsed-and-discarded) and executed at transaction commit.
      Parser captures `OnCommit` at all four CREATE TABLE arms + the CTAS `(col) ON
      COMMIT … AS` lookahead; 42P16 `ON COMMIT can only be used on temporary tables`
      guard (both `execCreateTable` and `execCreateTableAs`); per-session registration
      list (`BasicSession.RegisterOnCommitAction`) run from BOTH commit paths — executor
      `transactionOp.execCommit` AND the simple-query `applyTransactionVerb` — before
      the SSI check, mirroring `xact.c:2311→2339`; DELETE ROWS via
      `execTruncate(tempTables=true)`, DROP via `dropOnCommitTable` (partition +
      inheritance cascade); temp FK 0A000 `unsupported ON COMMIT and foreign key
      combination` + DETAIL (`heap.c:3738`); plan-cache invalidation via
      `Context.OnCommitDDL`. Three extra PG-semantics bugs fixed en route: plan-cache
      not invalidated by commit-time DROP; autonomous-CREATE registration lost across
      the BEGIN session swap (persisted on `connTxState`); failed COMMIT not undoing
      DDL before `TxnMgr.Rollback`. Diff **507→288 lines, 11→4 hunks, 37→30
      `^+ERROR`** (every ON COMMIT region byte-green). Remaining 4 hunks are
      independent out-of-scope gaps — Bucket C (PREPARE TRANSACTION temp guard, the
      natural next tiny slice), G (DECLARE CURSOR/portal, milestone), `temp_buffers`
      GUC, temp `current_schema()` search-path. CSV row stays `failed`/
      `pass_required=no`; no `make regen-testport`.
- [ ] **M0134-0073 — tidrangescan.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
      **Bucket B landed 2026-08-22** (design `docs/design/m0134-0073-ctid-function-arg-eval.md`). Sized at HEAD: 590 diff lines / 7 hunks / 0 `^+ERROR` / 0 `^-ERROR`, deterministic, four buckets. The dominant bucket (B, ~420 lines) was a CONTAINED correctness bug: ctid lives only on the scan slot (`MaterializedSlot.hasCTID/ctidBlock/ctidOff`, `slot.go:60-65`), not the `Row []Datum`, so `evalExprSlot`'s `FuncCall` case (`expr.go:1186`) reduced the slot to a bare Row via `slotToRow`, and any builtin re-evaluating an arg via `evalExpr(x.Args[i], row, ctx)` (`evalSubstr`, inline `"length"`, …) hit the `*CTIDExpr` case (`expr.go:480-495`) under a `rowSlotView` and fell through to `NullDatum` — `DELETE … WHERE substring(ctid::text FROM …)::integer > 2` deleted 0 rows where PG prunes all but two. Fix (fork c, over a rejected ambient-ctx side-channel and a deferred resjunk-Row fork b): thread `SlotView` through `evalFuncCall` + its 25-handler family, re-evaluating args via `evalExprSlot(x.Args[i], slot, ctx)` (297 sites); the compiled twin (`exprnode.go` ExprAdapter) delegates to `evalExprSlot` so one fix covers both evaluators. Result **590 → 254 diff lines** (Bucket B collapsed; `ctid < '(1,0)'` returns 10 rows not 58). New test `internal/executor/ctid_function_arg_test.go` (regression-verified: reverse-applied → FAILS, re-applied → PASS); live-oracle byte-identical. CSV row stays `failed`/`pass_required=no`; no `make regen-testport`. Remaining buckets (ledgered 2026-08-22, M0134-0073): Bucket A (no TidRangeScan node — REFACTOR, own milestone), Bucket C (outer-ctid across subquery/LATERAL → self-comparison — REFACTOR, seeds the `0129-0003` resjunk-ctid path), Bucket D (SCROLL `FETCH FIRST/LAST` aliased to NEXT/PRIOR — contained but gated on B's pruned layout), plus the latent ctid-drop in the other 9 `slotToRow` sites and the xmin/xmax system-column gap. **Re-arm trigger:** Bucket D is the next contained slice once B's data layout is re-measured; A and C are their own milestone-scale tasks.
- [ ] **M0134-0074 — tidscan.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
      **Bucket E landed 2026-08-22** (`WHERE CURRENT OF` → full-table UPDATE/DELETE, design
      `docs/design/m0134-0074-current-of-tid-resolution.md`): the parser captured
      `stmt.CurrentOf` but the optimizer never copied it, so `UPDATE/DELETE … WHERE CURRENT OF c`
      swept the whole table and the past-end case ran another full update instead of erroring.
      Resolved in the postmaster at dispatch time (where `connTx.Cursors` lives — the executor
      has zero cursor access): `cursorEntry` gained `AtEnd bool` + `TIDs []storage.ItemPointer`,
      `materializeCursor` captures per-row tids via a new `TupleSlot.TID()` accessor (which also
      surfaced and fixed a fast-path `projectOpNext` ctid-propagation sibling gap vs the
      interpreted twin), FETCH arms set/clear `AtEnd`, and `resolveCurrentOf` substitutes a
      concrete `ctid = '(block,off)'` equality flowing through the existing ctid-string-equality
      path (no optimizer/executor logic change); 34000 `cursor "%s" does not exist` / 24000
      `cursor "%s" is not positioned on a row`, and CURRENT OF statements bypass the plan cache.
      Diff **301 → 279 lines / 8 → 9 hunks**; the destructive UPDATE/DELETE now updates exactly
      the cursor's row (`actual rows=1.00`, `SELECT *` → `1/-2/-3`) and the past-end case raises
      24000. Remaining buckets ledgered 2026-08-22: **A** (no `Tid Scan` node — REFACTOR, shared
      with 0073 Bucket A), **D** (ctid self-join 0 rows — first-class tid Datum), **B/C**
      (tid[] deparser + `::tid` cast), **F** (SERIALIZABLE SIReadLock), **h6** (FETCH BACKWARD
      off-by-one), plus the materialized-tid-vs-re-resolve fidelity gap.
- [ ] **M0134-0075 — timestamp.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
      **Bucket D1 landed 2026-08-22** (`RESET` restores the session-start GUC value, design
      `docs/design/m0134-0075-guc-reset-session-start-value.md`): sized at HEAD
      (`scripts/pg-regress-runner.sh --verbose timestamp`) at 2493 diff lines / 8 hunks /
      90 `^+ERROR` / 23 `^-ERROR`, no crash. D1 was the highest-leverage CONTAINED fix:
      `RESET datestyle` restored the compiled `BootVal` (`ISO, MDY`) instead of the
      session-start value (`Postgres, MDY`, from `PGDATESTYLE` via the startup packet),
      so every surviving column SELECT after `timestamp.sql:93` rendered ISO instead of
      Postgres style. Root cause: `SessionRegistry.Reset`
      (`internal/utils/misc/session.go:217`) deleted the session override outright,
      exposing the global `Variable.Value`; the startup-packet GUC loop
      (`server.go:1214-1226`) used the same `Set` a client SET uses, so nothing recorded
      the session-start value. Fix mirrors PG's `reset_val` (`guc.c:3308-3309,3679,3727`
      — RESET restores `reset_val` not `boot_val`; `reset_val` updated only for
      `source <= PGC_S_OVERRIDE`, startup packet = `PGC_S_CLIENT` at `postinit.c:1298`):
      a third per-session `startup` map, a new `SetStartup` writer (writes session AND
      startup), and `Reset` restores `startup[key]` when present (else falls through to
      the global default as before); both `server.go` startup call sites swapped to
      `SetStartup`. Diff **2493 → 2467 lines (−26, ~13 Postgres-vs-ISO pairs)** — the
      predicted whole-block collapse did NOT materialise (the `@@ -696,1382` block is
      dominated by out-of-scope buckets, not the rendering baseline); the fix is
      nonetheless verified by a wire probe (PGDATESTYLE=Postgres,MDY → `set datestyle to
      ymd` → `reset` → `Postgres, MDY`; no PGDATESTYLE → `reset` → `ISO, MDY` global
      fall-through). Guard `TestSessionResetRestoresStartupValue`
      (`internal/utils/misc/guc_test.go`, 2 cases, FAIL-pre/PASS-post). Remaining buckets
      ledgered 2026-08-22: **D2** (function-result timestamps bypass DateStyle —
      `exprType` FuncCall arm returns "unknown" for date_trunc/date_bin/make_timestamp/
      age/generate_series + `date_part` hardcoded int8 vs PG float8), **E** (`evalDateBin`
      day-only), **E2** (`evalDateTrunc` invalid-unit silent), **F**
      (`extractTimestampField` missing msec/usec/julian), **H** (`evalMakeTimestamp` drops
      micros), **I** (`generate_series(timestamp,...)` 1 row), **J** (`age` infinity), **K**
      (`pg_input_is_valid` stubs), **A** (input literal parser — ISO-only layouts), **B**
      (int64-ns carrier vs PG int64-us, REFACTOR), **G** (`to_char`, REFACTOR ~35%), **M**
      (IntervalStyle, REFACTOR, ledger 1680), **O** (`interval * 2`, ledger 1679). CSV row
      stays `failed`/`pass_required=no`; no `make regen-testport`.
- [ ] **M0134-0076 — timestamptz.sql** — regress-sql `failed`: make the case match PG 18.3 (normalise against `./postgres/`). On pass, flip the CSV row to `pass` / `pass_required=yes` in the same commit.
      **Bucket F landed 2026-08-22** (design
      `docs/design/m0134-0076-extract-datepart-session-zone.md`): sized at HEAD
      (`scripts/pg-regress-runner.sh --verbose timestamptz`, PGTZ=America/Los_Angeles)
      at 3969 diff lines / 5 hunks / 204 `^+ERROR` / 32 `^-ERROR`, no crash. F was the
      largest CONTAINED slice: `extractTimestampField` (`expr.go:5810`) and both callers
      `evalExtract` (`:5567`) / `evalDatePart` (`:5907`) extracted in `.UTC()`, so
      calendar fields ignored the session `TimeZone`, `msec`/`usec`/`julian` were
      missing, `timezone*` returned 0/error, and `±infinity` rendered boundary values.
      Fix: apply the session zone (timestamptz only, via `timeZoneFromCtx` +
      `sessionTimeZoneLocation`), add the field aliases + `julian` (`date2j` port),
      real session-offset `timezone*` arms, and the `NonFiniteTimestampTzPart`
      oscillating→NULL / monotonic→±Infinity arms (plus a companion `round(±Infinity)`
      pass-through so the regress `round(julian)` column renders `Infinity` not `+Inf`).
      Result **3969 → 3945 diff lines** (extract region 455 → 418); `msec`/`usec`/`julian`
      `+ERROR` lines → 0, every loaded row's extract values now byte-identical to PG.
      The originally-projected ≥250-line collapse did NOT materialise — only 7/66 rows
      load (bucket A) and B2 leaves 2 boundary rows/block. Gates: tpch-spotcheck
      Q12=2/Q13=35 PASS, units pre-commit PASS, regress measured. CSV row stays
      `failed`/`pass_required=no`; no `make regen-testport`. Remaining buckets ledgered
      2026-08-22: **A** (input-literal tokenizer, REFACTOR, dominant), **G** (`to_char`
      template engine, REFACTOR, ~1200 lines), **B2** (infinity renderer sentinel,
      CONTAINED next), **C** (`AT TIME ZONE`), **E/E2** (`date_bin`/`date_trunc`),
      **H/H2** (`make_timestamptz`/`to_timestamp`), **I** (`generate_series` timestamptz),
      **J/K** (`age` infinity / `pg_input_is_valid`), **B1** (int64-ns carrier REFACTOR),
      plus the `date_part` wire-type (already ledgered under M0134-0075) and the
      timezone-field 0A000-vs-22023 error-class gap. **Next slice:** B2 (3 one-line
      sentinel checks) or A (the re-baselining prerequisite for the whole case).
- [ ] **M0134-0077 — transactions.sql** — regress-sql `failed`: **PARKED 2026-08-22** (case still FAILs; CSV row stays `failed`, no `make regen-testport` needed). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose transactions`: the gate **HANGS** deterministically at file line 344 (duplicate-key `INSERT INTO koju VALUES (1)` inside `SAVEPOINT x`), truncating coverage at 530/1101 normalized lines; defusing the hang gives the full diff 940 lines / 16 hunks / `+ERROR` 93 / `-ERROR` 33, actual 1057 vs 1101. Bucket A (the hang, CONTAINED) shipped: tuple xmin is stamped with the session sub-XID (`EffectiveWriterXID`, `session.go:721`) while `ctx.Tx.XID` is rebuilt per statement from the top-level xact (`dispatch.go:316`), so `uniqueCheckWithWait`'s in-flight test `xmin != selfXID && IsXIDActive(xmin)` (`operators_storage.go:8636-8643`) waited forever on goopg's own sub-XID. Fix: a `xidIsSelf(ctx, xid)` helper (top-level-or-subxact, via `TxnMgr.TopLevelXid`, mirroring `lockRowsOp.isSelfXID` and PG `TransactionIdIsCurrentTransactionId` `xact.c:941`) used by both `uniqueCheckWithWait`'s wait branch and `isLiveForUniqueCheck`'s own-xact arm; regression test `TestUniqueCheckWait_SubXidSelfNoDeadlock` (`insert_unique_constraint_test.go`). Result: gate no longer hangs; `koju_a_key` 23505 lines appear; run reaches the post-hang region (938 diff lines, residual = Bucket B). Remaining buckets B–L ledgered 2026-08-22: **B** (subxact ROLLBACK does not un-create relations — `koju` "already exists" after `rollback to x`, CONTAINED next), **C** (cursor laziness/portal cleanup), **D** (transaction GUCs `transaction_read_only`/`transaction_deferrable` + read-only enforcement, ~45 lines), **E** (`SET SESSION CHARACTERISTICS AS`), **F** (`COMMIT/ROLLBACK AND CHAIN`), **G** (`SET/START TRANSACTION` option-list), **H** (`xmin` system column), **I** (volatile-function snapshot), **J/K/L** (plpgsql gaps / implicit-block / `SET TRANSACTION SNAPSHOT`). Also a follow-up: `operators_upsert.go:864-931`/`:1418` carry the same latent top-level-only self-XID classification. **Next slice:** Bucket B.
- [ ] **M0134-0078 — triggers.sql** — regress-sql `failed`: **PARKED 2026-08-23** (case still FAILs; CSV row stays `failed`, no `make regen-testport` needed). Sized at HEAD via `scripts/pg-regress-runner.sh triggers`: 2853 raw diff lines / 67 hunks / `+ERROR` 108 / `-ERROR` 64, gate runs to completion (no hang). Bucket B7 (the one contained correctness bug) shipped: a C-language (non-plpgsql) BEFORE trigger suppressed every row it fired on because `executePLpgSQLTriggerBody`'s non-plpgsql arm returned `(nil, true, nil)` — `ok==true` with a nil row — so `fireTriggers` (`operators_trigger.go:64-83`) read it as "RETURN NULL → suppress the row" and every INSERT yielded `(0 rows)`. Fix: return `(nil, false, nil)` (`plpgsql_runtime.go:2913`) so `!ok → continue` leaves the row unmodified, matching goopg's established skip stance for un-executable triggers (`lookupTriggerRoutine == nil → continue`); PG never fires an unloadable function at all (`CREATE TRIGGER` resolves it first, `trigger.c CreateTrigger`). Guard test `TestExecutePLpgSQLTriggerBody_NonPlpgsqlPassThrough` (`operators_trigger_test.go`). Measured post-fix: suppression fully eliminated — zero `(0 rows)` remain in the `trigger_return_old`/`f1_times_10`/`trigger_zed` sections; diff 2853→2818 raw lines (hunks 67→69); Region-1 byte-parity blocked only by R1 (C bodies that actually execute and transform the tuple). Design: `docs/design/m0134-0078-non-plpgsql-trigger-suppression.md`. Remaining buckets B1–B6/B8–B13 + R1 ledgered 2026-08-23: **B9** ALTER TRIGGER RENAME parser gap (NEXT-BEST CONTAINED, ~45-80 lines), **B2** DROP COLUMN ignores trigger dependencies (~300, largest raw), **B3** INSTEAD OF view triggers (~200), **B4** partition trigger cloning (~170), **B5** transition tables not materialised (~180), **B6** enable/disable no-op (`catalog.Trigger` has no `Enabled`; design 0118-0032 unlanded) + `session_replication_role`, **B8** WHEN clause never evaluated at firing (over-fires), **B1** statement-level firing unwired beyond BEFORE INSERT/TRUNCATE, **B10** plpgsql TG_*/expr gaps, **B11** CREATE TRIGGER validation bundle, **B12** information_schema.triggers invisible on fresh initdb, **B13** deferred-trigger ACL, **R1** C-trigger return-value semantics. **Next slice:** B9.
- [ ] **M0134-0079 — tuplesort.sql** — regress-sql `failed`: **PARKED 2026-08-23** (case still FAILs; CSV row stays `failed`, no `make regen-testport` needed). Sized at HEAD via `scripts/pg-regress-runner.sh --verbose tuplesort`: 473 raw diff lines / 11 hunks / `+ERROR` 2 / `-ERROR` 0, gate runs to completion. Bucket C (the one contained fix) shipped: `arr[lower:upper]` array-slice syntax (and PG's `arr[:5]`/`arr[5:]`/`arr[:]` omitted-bound forms) failed to parse at all — `lex error … unexpected character ':'` — because the lexer (`internal/parser/lexer.go`) treated any bare `:` as a hard error instead of PG's array-slice separator (`indirection_el`, `gram.y`). Fix: lexer emits bare `:` as `TokenSymbol`; `ArraySubscriptExpr` (`internal/parser/expr.go`) gained `IsSlice`/`Upper`; the parser's `expr[...]` postfix arm (`select.go`) parses optional lower/upper bounds; the planner (`resolveExpr`) lowers to `FuncCall{Name:"array_slice"}` (omitted bound → `&NullConst{}`), and `targetMeta` gained an `array_slice` arm matching PG's `FigureColnameInternal` `T_A_Indirection` skip-pure-subscripts rule (`(array_agg(x))[1:2]` labels as `array_agg`); the evaluator (`internal/executor/expr.go`) reuses `splitArrayElements` and clamps bounds per PG's `array_ref` (`arrayfuncs.c`: out-of-range/reversed bounds clamp or return `{}`, never NULL/error); five sibling AST-traversal sites (CHECK-constraint columns, partition-DEFAULT validation, ON CONFLICT columns, PL/pgSQL lowering, analyzer type-check) that assumed `Index` was always non-nil got `IsSlice` guards — the analyzer one was a confirmed-live nil-interface panic (`analyzeExpr`, `analyzer.go:1634`). Regression test `TestArraySlice` (`internal/executor/array_slice_test.go`). Design: `docs/design/m0134-0079-array-slice-syntax.md`. Measured: the two slice-using statements now parse/execute past the array slice, surfacing a different, unrelated ordered-set-aggregate bug (`WITHIN GROUP types uuid and text cannot be matched`) instead of aborting at parse time (ledgered separately). Diff count materially unchanged (473→477 — the lex-error line was replaced by a different still-wrong ERROR line at the same two spots), because that newly-unmasked bug still blocks those two statements from reaching the case's other four buckets. Remaining buckets A (EXPLAIN plan shape, index-vs-sort for ORDER BY+LIMIT — REFACTOR-tier), B (CLUSTER physical order + aborted-tuple retention, largest ~230 lines, REFACTOR-tier, no contained slice), D (backward cursor FETCH LAST/BACKWARD scroll semantics, unsized), E (EXPLAIN plan shape for count-DISTINCT + mark/restore, overlaps M0134-0030's Incremental Sort re-arm trigger) all ledgered 2026-08-23. **Next slice:** none identified as CONTAINED.
- [ ] **M0134-0080 — txid.sql** — regress-sql `failed`: **PARKED 2026-08-23** (case still FAILs; CSV row stays `failed`). Sized/landed via a direct diff against `./postgres/src/test/regress/expected/txid.out`. Landed this loop: `parsePgSnapshot` (`internal/executor/expr.go`) validates/normalises the `xmin:xmax[:xip,...]` text form matching upstream's `parse_snapshot` (`xid8funcs.c`) — rejects `xmin==0`/`xmax==0`/`xmin>xmax`/out-of-order or out-of-range xip, dedups equal consecutive xip — wired into both the `::txid_snapshot`/`::pg_snapshot` cast (`evalCast`) and the `typename 'string'` typed-literal syntax (new parser case in `tryTypedLiteral`, `select.go`, plus `evalTypedStringLit` arm); 6 of the 7 pg_proc-registered-but-undispatched functions gained executor arms: `txid_current_if_assigned`, `txid_current_snapshot` (built from `ctx.Snap`), `txid_snapshot_xmin`/`txid_snapshot_xmax`, `txid_visible_in_snapshot`, `txid_status` (via `TxnMgr.ClassifyXID` + a future-xid 22023 error). Remaining gaps ledgered 2026-08-23: (1) `txid_snapshot_xip` is a set-returning function used in a plain target list — needs a new SRF plan/exec column kind (goopg's `ProjectSet` has no scalar-arg-builtin-SRF case at all, CONTAINED-sized but a distinct mechanism from the rest of this slice); (2) goopg's `ctx.Snap.InProgress` excludes the calling backend's own xid (self-visibility is special-cased elsewhere) while PG's real snapshot includes it, so `txid_visible_in_snapshot(txid_current(), txid_current_snapshot())` returns the wrong polarity; (3) an unexplained `txid_current() >= txid_snapshot_xmin(txid_current_snapshot())` returning false — not yet root-caused, needs a throwaway probe test; (4) `txid_status`'s too-old→NULL case (oldestClogXid truncation) is unmodelled. **Next slice:** (1) SRF column kind for scalar-arg builtin SRFs, OR (3) the Xmin-comparison probe (smaller, might unblock without new mechanism).
- [ ] **M0134-0081 — updatable_views.sql** — regress-sql `failed`: **PARKED 2026-08-23, sizing-only, case still `failed` (0/1, 1823-line diff), CSV row unchanged.** Six independent buckets, none flip the case alone: (A, largest) `information_schema.tables/views/columns` omits view rows entirely for `is_updatable`/`is_insertable_into` — a separate info_schema virtual-catalog population gap, unrelated to view DML. (B) `planMerge` (`internal/optimizer/planner.go:11529`) has zero `tbl.View` awareness — MERGE INTO a view silently affects 0 rows (confirmed live repro: `MERGE INTO rw_view15 ... WHEN MATCHED THEN UPDATE` reports `SELECT 0` — even the command tag is wrong — and leaves the base table untouched) instead of rewriting through `viewAutoUpdatableChain`/`viewProxyTable` the way `planInsert`/`planUpdate`/`planDelete` already do (root-0025) — same silent-no-op class root-0025 originally fixed, just never extended to MERGE. (C) `MERGE ... RETURNING old.*/new.*` — PG18 feature, identical to the already-parked M0134-0063 (`returning.sql`) Bucket A, 0% implemented. (D) system columns (`ctid`) in a view's target list are rejected as non-updatable: `viewColumnMap` (`internal/optimizer/view_dml.go:103`) only searches `b.Columns` (physical user columns), no path for a system-column `Var` — confirmed live repro: `CREATE VIEW rw_view14 AS SELECT ctid, a, b FROM base_tbl; INSERT INTO rw_view14 (a,b) VALUES (3,'Row 3')` → `cannot insert into view "rw_view14"` even though PG allows it (ctid "may be part of an updatable view" per the test's own comment). (E) a repeated column reference to the same base ordinal (`SELECT a, b, a AS aa FROM base_tbl`) breaks resolution of the ORIGINAL name: `viewProxyTable` (`internal/optimizer/view_dml.go:177`) assigns one `Name` per base ordinal, so a later `viewOrd` sharing a `baseOrd` overwrites an earlier alias — confirmed live repro: `UPDATE rw_view16 SET b='x' WHERE a=-2` (view = `a, b, a AS aa`) → `column "a" does not exist` (only `aa` survives). (F) `INSERT ... DEFAULT VALUES` through a view producing empty/wrong rows (`rw_view1`, expected 2 rows with mostly-default columns, actual 0 rows) — not yet root-caused, needs a throwaway probe test. Ledger row `.ralph/deferral_ledger.md` dated 2026-08-23, M0134-0081, has full detail + PG oracle citations. Next M0134 task to select: **M0134-0082 (`explain.sql`)**. D and E are the smallest CONTAINED buckets and the best candidates for a future dedicated M0134-0081 slice (neither alone flips the case, since A/B/C/F remain).
- [ ] **M0134-0082 — explain.sql** — regress-sql `failed`: **PARKED 2026-08-23, sizing-only, case still `failed` (0/1, 869-line diff), CSV row unchanged.** At least six independent buckets, none flip the case alone: (A, largest) the structured-format (JSON/XML/YAML) plan-node builder `planToJSON`/`planToJSONWithStats` (`internal/executor/operators_explain.go:1684-1717`) is far less complete than the text renderer — it emits only `"Node Type"` (via `describePlan`) and `"Plan Rows"`, missing `Startup Cost`/`Total Cost`/`Relation Name`/`Alias`/`Plan Width`/`Parallel Aware`/`Async Capable`/`Disabled` entirely (confirmed live repro: `explain (format json) select * from int8_tbl i8` omits all of those keys PG always emits). (B) sibling-path divergence within the same bucket: the text renderer skips `*optimizer.Project` wrapper nodes (comment at `operators_explain.go:388,412`, "PG has no Projection plan node") but `planToJSON`'s `describePlan` call (line 1858-1861) has no such skip, so every structured-format output gets a spurious extra `"Node Type": "Projection"` node the text format correctly omits (pattern: sibling paths must change together, `pattern_sibling_paths_must_agree` memory). (C) `SERIALIZE` EXPLAIN option is not parsed at all — `explain (analyze,buffers off,serialize) …` errors `unknown EXPLAIN option "serialize"` in the parser, 0% implemented (PG: `explain.c` `ExplainOptionList`/`serialize` symbol, format text/binary). (D) `GENERIC_PLAN` is a no-op stub: goopg always emits `NOTICE: generic plan is not available for this statement; using custom plan` and runs the custom plan instead (wrong plan shape — Index Scan instead of PG's Bitmap Heap Scan for `$1`-parameterized quals, and Append child ordering differs for partition pruning); the `ANALYZE + GENERIC_PLAN` mutual-exclusion check PG raises (`EXPLAIN options ANALYZE and GENERIC_PLAN cannot be used together`) is also missing, so goopg instead tries to literal-bind the raw EXPLAIN SQL text as a query parameter and throws an unrelated `22P02` decode error. (E) `MEMORY` option is only wired for the `ANALYZE+MEMORY` combo (`operators_explain.go:1656-1663`, gated inside the stats-only `planToJSONWithStats` path) — plain `explain (memory) select …` and `EXECUTE`-based explain both silently drop the `Memory: used=…kB allocated=…kB` line PG always emits once the option is given, regardless of ANALYZE. (F) `WindowAgg` display is doubly wrong: missing the `Window: wN AS (...)` and `Storage: Memory|Disk  Maximum Storage: NkB` detail lines entirely (0% implemented — no per-window tuplestore accounting), AND the verbose `Output:` column list for a `WindowAgg` node prints the *raw base-table columns* instead of the computed window-function expressions PG shows (`sum(unique1) OVER w`, …) — separate root cause from the missing Window/Storage lines, not yet investigated. (G, smaller) `EXPLAIN (MEMORY) EXECUTE <prepared>` and `EXPLAIN … CREATE TABLE AS …` render the *unplanned* statement node (`"Utility *parser.ExecuteStmt"` / `"DDL *parser.CreateTableStmt"`) instead of walking through to the underlying executable plan (`Result`/`Seq Scan`) the way PG does — the EXPLAIN dispatcher isn't unwrapping EXECUTE/CTAS to the inner plan before rendering. No ledger row filed for buckets A/B/E/F/G if they were already implicit in the "structured format incomplete" class — see the new ledger row below for full detail + PG oracle citations (all buckets). Next M0134 task to select: **M0134-0083 (`uuid.sql`)**. Bucket B (skip the `Project` wrapper in `planToJSON`/`planToJSONWithStats`, mirroring the text renderer's existing skip) is the smallest CONTAINED slice and the best candidate for a future dedicated M0134-0082 resume (won't flip the case alone — A/C/D/E/F/G remain).
- [ ] **M0134-0083 — uuid.sql** — regress-sql `failed`: **PARKED 2026-08-23** (case still `failed`, CSV row unchanged). Landed a real fix this loop: `genUUIDv7FromTime` (`internal/executor/expr.go`) builds `uuidv7(interval)`'s ms-since-epoch from `ts.Unix()`/`ts.Nanosecond()` instead of the overflow-prone `ts.UnixNano()` (`genUUIDv7`/new `genUUIDv7FromMs` share the byte-building tail). Three buckets remain, none flip the case alone: **(A)** missing `LINE n:`/`^` caret on runtime `22P02` errors — the ALREADY-LEDGERED "goopg never emits FieldPosition" gap (M0127-PS6.2 row). **(B)** EXPLAIN drops the `::type` cast on every string-literal comparison against a non-text builtin column (confirmed NOT uuid-specific — same gap for `date`/`inet`/`macaddr` against a live PG 18.3 oracle); root cause is upstream of `formatExprQual`'s `CastExpr` case — the planner only inserts a coercion `CastExpr` for `name`/enum/domain comparisons (`internal/optimizer/planner.go:13466`), never for builtin types, so there is no cast node for EXPLAIN to render at all. **(C, blocks the largest diff block)** the `uuidv7`/`uuid_extract_timestamp` monotonicity check still fails — root cause is the PRE-EXISTING, ALREADY-LEDGERED `KindTime` nanosecond-carrier overflow (confirmed live: `SELECT now() + interval '236 years'` alone returns `1678-02-01`, no UUID function involved), the same M0119-0006 carrier-range migration ledgered 2026-08-11/12 ("move `Datum.Int` to microseconds... needs its own loop"). Full detail + PG-oracle citations: `.ralph/deferral_ledger.md` 2026-08-23 M0134-0083 row. Next M0134 task: **M0134-0084 (`expressions.sql`)**.
- [ ] **M0134-0084 — expressions.sql** — regress-sql `failed`: **PARKED 2026-08-24** (case still `failed`, CSV row unchanged; 232 → 201 diff lines this loop, 0 `^+ERROR` either side). Landed a real fix: an engine-wide SQLValueFunction/TimeZone bug the case's own SQLValueFunction block exposed, root-caused with a throwaway cgroup-capped goopg + `PGTZ=America/Los_Angeles` (matching `scripts/pg-regress-runner.sh`'s session zone). Three independent sub-bugs, all in `internal/executor/expr.go`'s `evalFuncCall`: **(1)** `current_date` built its Datum via `NewTimeDatum` with no `TimeSub` tag, so `current_date::text` rendered the full `"YYYY-MM-DD 00:00:00"` timestamp shape instead of a bare date — `date(now())::text = current_date::text` compared unequal on every run regardless of the actual day; fixed via `NewDateDatum` (the same site every other date-producing path already uses). **(2)** `localtimestamp` returned the raw UTC instant relabelled as local wall clock with zero TimeZone conversion, while its declared-equivalent `now()::timestamp` CastExpr path already applies `misc.TimestampTZToTimestamp` (M0119-0006 40th slice) — `now()::timestamp::text = localtimestamp::text` was wrong by the full UTC offset (7-8h under `America/Los_Angeles`) on every run, not just near a boundary; fixed by routing `localtimestamp` through the same `misc.TimestampTZToTimestamp(ctx.Now, timeZoneFromCtx(ctx))` call. **(3)** `current_time(N)`/`localtime(N)` floored the fractional seconds via integer division, while the `::time(N)` CastExpr Typmod path (`roundTimeDatumToPrecision` → `datetime.AdjustTimeForTypmod`, `postgres/src/backend/utils/adt/date.c:1710`) ROUNDS — confirmed genuinely flaky live (5 reps of `now()::time(3)::text = localtime(3)::text` split 3 `f`/2 `t`); fixed by routing both through `roundTimeDatumToPrecision` instead of the ad hoc floor. All three landed with regression tests (`internal/executor/sqlvalue_func_timezone_test.go`: `TestCurrentDateIsTaggedDate`, `TestLocalTimestampMatchesTimestamptzCastToTimestamp`, `TestCurrentTimeAndLocaltimeRoundLikeTimeCast`) and verified live (8/8 reps of the flaky comparison now `t`). Five independent buckets remain, none flip the case alone: **(A)** `pg_get_viewdef`/EXPLAIN typmod-through-cast loss for `numeric(16,4)`/`character(14)` view columns — same textual-substitution-vs-AST-deparse class already ledgered elsewhere (view definition renders the raw SQL text, `\d+` re-derives column types without the cast's typmod, and EXPLAIN Output drops the cast entirely — the same planner CastExpr-insertion gap as the M0134-0083 uuid.sql Bucket B). **(B)** `explain (verbose, costs off) select random() IN (1, 4, 8.0)` — PG folds the IN-list into a `ScalarArrayOpExpr` (`Result / Output: (random() = ANY (...))`), goopg instead produces a bare `Values (1 rows)` node with no expression shown — the IN→ScalarArrayOpExpr planner transform (`convert_saop_to_hashed_saop`-adjacent, `postgres/src/backend/optimizer/util/clauses.c`) is unimplemented for a FROM-less SELECT. **(C)** `select '(0,0)'::point in ('(0,0,0,0)'::box, point(0,0))` — PG raises `42883 operator does not exist: point = box` (no common supertype, no cross-type `=`), goopg silently accepts and returns `t` — the `IN`/`InExpr` type-checking path is missing PG's per-element operator-existence validation entirely (a real correctness gap, not cosmetic — confirmed live against PG 18.3). **(D)** `CREATE FUNCTION ... internal AS 'int4in'` for a still-shell type omits the `NOTICE: return/argument type … is only a shell` diagnostic PG emits (`postgres/src/backend/commands/functioncmds.c`) — same already-ledgered "goopg under-emits NOTICE" class as M0134-0083 Bucket A. **(E)** an `IN (... , null)` list against a `myint` domain built via `CREATE TYPE ... (like = int4)` reports `(1 row)` where PG reports `(2 rows)` for an identical single printed data row `1` — not yet root-caused (the row COUNT differs while the visible rows are byte-identical in the diff context), needs a dedicated throwaway probe; flagged but not sized. Ledger row: `.ralph/deferral_ledger.md` 2026-08-24, M0134-0084. Next M0134 task: **M0134-0085 (`fast_default.sql`)**.
- [ ] **M0134-0085 — fast_default.sql** — regress-sql `failed`: **PARKED 2026-08-24** (case still `failed`, CSV row unchanged; 806 → 469 diff lines this loop). Landed two independent, engine-wide fixes, both root-caused with a throwaway cgroup-capped goopg + live psql probe (search_path/heredoc repro, not just the regress diff): **(1)** `ALTER TABLE ... ADD COLUMN ... NOT NULL DEFAULT <const>` on a non-empty table unconditionally raised a goopg-invented `0A000` error — `"ALTER TABLE ADD COLUMN ... NOT NULL is only supported on empty tables"` — a message that does not exist anywhere in PostgreSQL's own source (`grep` over `postgres/src/backend/commands/tablecmds.c` confirms it). Real PG's `ATExecAddColumn` (tablecmds.c:7217+) has no such restriction at all: with a constant DEFAULT it records `attmissingval` and every pre-existing row is satisfied via the fast-default backfill goopg already implements (`constDefaultDatum`/`Column.MissingValue`, M0097-0077) — no rewrite, no per-row scan. The bug was ordering: the NOT-NULL/non-empty-table guard in `execAlterTableAddColumn` (`internal/executor/operators_ddl.go`) ran BEFORE the fast-default datum was ever computed, so it fired even when a usable default existed. Fixed by hoisting the `constDefaultDatum` evaluation above the guard and gating the guard on "no fast-default value available" instead of "table is non-empty" (a bare `ADD COLUMN ... NOT NULL` with no default, or a non-constant default, on a non-empty table is still correctly rejected — PG itself would instead raise a later, different "column contains null values" error for that case, tablecmds.c:6458, not yet matched — flagged as bucket F below). **(2)** `CREATE INDEX ON <unqualified-table>(...)` and `CREATE TRIGGER ... ON <unqualified-table> ...` both called `o.ctx.Catalog.LookupTable(s.Table, ...)` directly with NO `search_path` fallback for an unqualified name — confirmed live: under `SET search_path = fast_default`, `SELECT`/`DROP TABLE` resolved an unqualified `t` fine (both already have search_path fallback logic) but `CREATE INDEX`/`CREATE TRIGGER` raised a spurious 42P01 "relation does not exist" for the exact same name in the exact same session — same defect class as the already-fixed M0134-0024b (`ALTER TABLE ... INHERIT`). Fixed by switching both call sites, plus the 7 other trigger-DDL functions sharing the identical unguarded pattern (DROP/ALTER/ENABLE/DISABLE TRIGGER), to the existing `lookupTableWithSearch` helper (`execAlterTable` already uses it). Both fixes landed with regression tests: `internal/executor/operators_ddl_addcolumn_notnull_default_test.go` (`TestAddColumnNotNullDefaultAllowedOnNonEmptyTable`, `TestCreateIndexAndCreateTriggerHonourSearchPath`) plus a live-server verification (psql against a throwaway cgroup-capped instance). Six buckets remain, none flip the case alone: **(A)** the file's `set()`/`comp()`-driven large-datatype-sample block (lines ~113-260) shows systematically blank columns after several `ALTER TABLE ... ADD COLUMN ... DEFAULT` steps plus PG-array-literal-format divergence (`{This,is,...}` vs goopg's `{"This", "is", ...}` — a pre-existing, already-ledgered array-output-format gap) — not yet root-caused to a single mechanism, likely several independent gaps in this custom-function-driven harness; needs a dedicated throwaway probe. **(B)** `ALTER TABLE vtype ALTER b TYPE text USING b::text, ALTER c TYPE text USING c::text` on a table with a fast-default `MissingValue` column silently DROPS the row entirely (PG: `(1 row)`, goopg: `(0 rows)`) — the ALTER-TYPE rewrite path (`postgres/src/backend/commands/tablecmds.c` ATRewriteTable) apparently doesn't carry the fast-default value through the USING-expression rewrite for a row that was never physically written with that column; a genuine data-loss bug, needs its own investigation. **(C)** the same statement no longer emits `NOTICE: rewriting table vtype for reason 4` (three more `NOTICE: rewriting table ... for reason N` diagnostics also missing earlier in the file for `has_volatile`) — goopg's ALTER-TYPE/ADD-COLUMN rewrite path doesn't emit PG's `ATRewriteTable`/`tablecmds.c` rewrite-reason NOTICE at all; already the ledgered "under-emits NOTICE" class. **(D)** inside a `BEGIN;` block, `CREATE TABLE t(); INSERT ...; ALTER TABLE t ADD COLUMN a int DEFAULT 1; CREATE INDEX ON t(a);` still raises "relation t does not exist" even after fix (2) — this is CREATE INDEX's *auto-named* form (no explicit index name, `s.Table` still present) inside an explicit transaction block, so it's a different, transaction-visibility-scoped variant of the search_path gap, not yet isolated. **(E)** `to_json(OLD)` inside the file's `test_trigger()` plpgsql trigger renders empty (`NOTICE: old tuple:`) instead of the full row (`NOTICE: old tuple: {"id":1,...}`) once fix (2) let `CREATE TRIGGER` succeed — the OLD row passed into a trigger isn't going through the same fast-default "expand_tuple" backfill a plain heap read gets; the file's own comment names this exact scenario ("test that we account for missing columns without defaults correctly in expand_tuple, and that rows are correctly expanded for triggers") — a genuine correctness gap in trigger-row construction, not yet root-caused. **(F)** `ADD COLUMN ... NOT NULL` with no default on a non-empty table: goopg keeps the invented `0A000` message from before this loop (still technically wrong — PG's actual error is `23502`/"column %s of relation %s contains null values", tablecmds.c:6458) — deliberately left as-is rather than guessed at under a `BEGIN;...ROLLBACK;`-wrapped block the same diff also flags. Ledger row: `.ralph/deferral_ledger.md` 2026-08-24, M0134-0085. Next M0134 task: **M0134-0086 (`with.sql`)**.
- [ ] **M0134-0086 — with.sql** — regress-sql `failed`: **PARKED 2026-08-24** (case still `failed`, CSV row unchanged; not a diff-line-count sizing loop — pass/fail status unaffected). Sizing the case live (not the harness diff) found a real, PG-accepted query (`postgres/src/test/regress/expected/with.out:508-545`) that made goopg's server RSS climb without bound (22+ GB and rising, confirmed live before being killed) instead of terminating at the LIMIT-bounded 32 rows real PG produces: a `WITH RECURSIVE` nested inside another `WITH RECURSIVE` whose recursive term references back out to the still-open outer query (`with recursive q as (... union all (with recursive x as (... union all (select * from q union all select * from x)) select * from x)) select * from q limit 32`). Root cause: `recursiveUnionOp.Next()` (`internal/executor/operators_recursive_cte.go`) fully drains one fixpoint iteration to EOF before returning any row to its caller, instead of real PG's row-at-a-time `nodeRecursiveunion.c` pull model that lets an outer LIMIT stop the pull chain early even on a naturally-infinite query graph — here the inner CTE's recursive term can never reach EOF on its own (draining it requires draining the still-open outer CTE, and vice versa), so the existing `maxRecursiveDepth` guard (which only advances between *completed* iterations) never fires; the process is stuck forever inside iteration one. Landed a SAFETY-NET fix, not a correctness fix (the real fix — row-at-a-time pull plus a shared/reentrant CTE instance across nested `WITH` scopes — is REFACTOR-tier, out of scope): new `maxRecursiveIterationRows` (var, default 2,000,000, `internal/executor/operators_recursive_cte.go`) caps a single iteration's row accumulation, raising the existing `54001` "exceeded maximum recursion depth" error instead of growing unbounded — verified live (repro now errors in ~9s at ~800 MB RSS instead of climbing past 22 GB) and unit-tested (`TestRecursiveUnionCapsRunawaySingleIteration`, `internal/executor/operators_recursive_cte_iteration_cap_test.go`). Design doc: `docs/design/m0134-0086-recursive-cte-iteration-oom-guard.md`. Ledger row: `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0086. Next M0134 task to select: **M0134-0087 (`xid.sql`)**.
- [ ] **M0134-0087 — xid.sql** — regress-sql `failed`: **PARKED 2026-08-24** (case still `failed`, CSV row unchanged; 472 → 435 diff lines, 20 → 19 `^+ERROR` — most of the remaining diff is cosmetic column-alignment even though the underlying VALUES are now correct). Landed a three-site sibling-path fix (all confirmed live): **(1)** `evalCast` (`internal/executor/expr.go`) had NO case for `"xid"`/`"xid8"` at all — `'…'::xid`/`'…'::xid8` and any implicit column coercion fell to the default arm and returned the input string UNCHANGED (no parsing, no validation, `'asdf'::xid` silently "succeeded"); this was the exact CastExpr-vs-TypedStringLit split `pattern_sibling_paths_must_agree` warns about (`xid '42'`'s `evalTypedStringLit` already worked). Fixed via a shared `case "xid", "xid8":` reusing `parseXid`/`parseXid8` (plus xid8→xid 32-bit truncation, `xid8toxid`). **(2)** `appendTypedCellText` (`internal/postmaster/dispatch.go`, shared by simple- and extended-query TEXT paths) had no `"xid8"` case, so a wrapped uint64 (e.g. 2^64-1 stored as `int64(-1)`) rendered as literal `"-1"` over the wire instead of `"18446744073709551615"` — the binary/COPY encoders already got this right via `pgUnsignedIDFromDatum`, just never wired into TEXT. Fixed with `strconv.AppendUint`. **(3)** `pgUnsignedIDFromDatum`'s `KindString` branch (`internal/executor/codec.go`) — used by heap-encode-time implicit string→column coercion, e.g. `INSERT INTO t(x xid8) VALUES ('0x2a')` — routed through decimal-only `coerceStringToInt64` for xid/xid8 too, so a hex/octal xid8 INSERT raised a spurious 22003; fixed by special-casing xid/xid8 to `parseXid`/`parseXid8`. Also fixed `parseXid8` to accept octal (`0NNN`), matching `parseXid`'s existing branch (`xid8in` is base-0 auto-detect same as `xidin`). Landed with 3 new test files: `internal/executor/xid_cast_test.go`, 10 new cases in `internal/executor/pg_unsigned_id_wrap_test.go`, `internal/postmaster/xid8_output_test.go`. Five buckets remain, none flip the case alone: **(A)** `min(x)`/`max(x)` over xid8 compare the stored int64 SIGNED not UNSIGNED (confirmed live: min/max swapped for a column holding uint64-max values) — structural gap, `Datum`/`KindInt` has no type tag distinguishing xid8 from int8, the same problem `TimeSubtype` solved for `KindTime` but never extended here. **(B)** goopg's `xid` type wrongly ALLOWS `<`/`<=`/`>`/`>=` (PG raises 42883 — no btree opclass on xid, "we don't want relational operators... due to modular arithmetic"). **(C)** `pg_input_error_info('0xffffffffff','xid')` returns empty message/sql_error_code instead of forwarding the caught 22003. **(D)** three more pg_proc-registered-but-undispatched builtins (`pg_visible_in_snapshot`, `pg_current_xact_id_if_assigned`, `pg_xact_status`) — same "6 of 7" remainder class M0134-0080 already ledgered for a different subset. **(E, deliberately out of scope)** oid/regproc's own implicit-coercion path is ALSO base-0/hex/octal in real PG but left decimal-only here to keep the slice CONTAINED (oid has its own regress file + wider blast radius across regclass/regtype/regrole/regcollation/cid). Ledger row: `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0087. Next M0134 task to select: **M0134-0088 (`alter_generic.sql`)**.
- [ ] **M0134-0088 — alter_generic.sql** — regress-sql `not-tried`, sized live: **PARKED 2026-08-24** (case resolves to genuinely FAILING, CSV row unchanged; 849 → 850 diff lines, near-net-zero since the file is dominated by five unrelated multi-feature gaps below, not by more instances of the bug fixed here). Landed a genuine engine-wide correctness fix: `catalog.Routines.RenameRoutine`/`SetSchema` (`internal/catalog/routines.go`) re-keyed a routine's index entries with NO duplicate-target check — `ALTER FUNCTION alt_func1(int) RENAME TO alt_func2` (where `alt_func2(int)` already existed) silently SUCCEEDED and displaced the existing entry instead of raising PG's `42723 duplicate_function`; same gap on `SET SCHEMA` onto a colliding destination. Confirmed this was the file's TRUE first divergence — goopg's silent acceptance put later statements in a different function-identity state than PG's, cascading into many unrelated-looking wrong errors downstream. Fixed with a new `catalog.ErrRoutineNameConflict` guard in both `RenameRoutine`/`SetSchema` (mirrors PG's `IsThereFunctionInNamespace()`, `functioncmds.c:2052-2073`), translated to `42723` with PG's exact message shape in `execAlterFunction` (`internal/executor/operators_ddl.go`) at both call sites. Verified live: both touched lines now match PG byte-for-byte. Landed with `internal/executor/alter_function_rename_conflict_test.go` (`TestAlterFunctionRenameConflict`). Five buckets remain, none flip the case alone: **(A)** `CREATE LANGUAGE` has NO parser support (bare syntax error) — every language-DDL block unreachable. **(B)** `ALTER AGGREGATE` uses a separate `InMemory` UserAggregate registry instead of the unified pg_proc-backed `catalog.Routines`, so it can't distinguish "no such routine" from "routine exists but isn't an aggregate" (wrong error text/code) — architecture gap. **(C)** `ALTER FUNCTION/AGGREGATE OWNER TO` has no role-membership permission check at all (PG's `must be able to SET ROLE`). **(D)** the full `CREATE TEXT SEARCH DICTIONARY/CONFIGURATION/TEMPLATE/PARSER` DDL family is entirely absent. **(E)** FDW/SERVER duplicate-detection has the same missing-guard pattern as the bug fixed here, but for an unimplemented object family. Ledger row: `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0088. Next M0134 task to select: **M0134-0089 (`alter_operator.sql`)**.
- [ ] **M0134-0089 — alter_operator.sql** — regress-sql `not-tried`, sized live: **PARKED 2026-08-24** (case resolves to genuinely FAILING, CSV row unchanged — 237 → 237 diff lines, near-net-zero because a second, larger blocker (`pg_describe_object`, entirely unimplemented) surfaces immediately behind the two fixes below and produces a similarly-sized error block). Landed two genuine engine-wide correctness fixes, both confirmed live: **(1)** `catalog.InMemory.PGDependRowsForDBOid` (`internal/catalog/catalog.go`) had NO code path for `c.userOperators` at all — every `CREATE OPERATOR` reported ZERO `pg_depend` rows; fixed by adding the namespace/left-type/right-type/result-type/oprcode/oprrest/oprjoin dependency loop `makeOperatorDependencies` (`postgres/src/backend/catalog/pg_operator.c:853-937`) performs, including its pinned-object skip (`IsPinnedObject`, OID < 12000 except the public namespace). **(2)** the CastExpr `regoperator` branch (`internal/executor/expr.go`) only ever handled a `KindInt` (OID) input; a `KindString` (name) input like `'===(bool,bool)'::regoperator` fell through UNCHANGED as raw text, so every `WHERE objid = '...'::regoperator` comparison against an oid column silently evaluated false — fixed with a new `regoperatorNameAndArgs` parser (`internal/executor/reg_identifier.go`) plus a new `catalog.InMemory.LookupUserOperatorByNameAndTypeOIDs` (type-OID-normalized so `bool` matches a `boolean`-declared LEFTARG), mirroring the `regclass` CastExpr arm's existing string→OID pattern exactly. Landed with `internal/executor/create_operator_depend_test.go` (`TestCreateOperatorPopulatesPGDepend`, `TestRegoperatorCastResolvesToOID`). Five buckets remain, none flip the case alone: **(A)** `pg_describe_object` is entirely unimplemented — own multi-day milestone (PG's real version dispatches over ~40 catalog classes). **(B)** several builtin selectivity-estimator functions used in the file (`contsel`, `contjoinsel`, `_int_contsel`, `_int_contjoinsel`) aren't in goopg's curated builtin-proc set. **(C)** `ALTER OPERATOR ... SET ("Restrict" = ...)` (quoted option name) is a goopg PARSER syntax error where PG accepts it and raises a semantic error instead. **(D)** `ALTER OPERATOR ... SET (...)` has no ownership permission check at all (same class as M0134-0088 Bucket C). **(E)** the "cannot change an already-set COMMUTATOR/NEGATOR via SET" collision guard is missing for the reverse-collision case. Ledger row: `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0089. Next M0134 task to select: **M0134-0090 (`amutils.sql`)**.
- [ ] **M0134-0090 — amutils.sql** — regress-sql `not-tried`, sized live: **PARKED 2026-08-24** (case resolves to genuinely FAILING, diff 247 → 87 lines). Landed the entire `pg_indexam_has_property`/`pg_index_has_property`/`pg_index_column_has_property` feature (`postgres/src/backend/utils/adt/amutils.c`), previously seeded in `pg_proc` but wholly undispatched (`42883` from the file's first query). New `catalog.IndexAMCapability` table (`internal/catalog/catalog.go`) transcribes each of the 6 in-tree AMs' static `amroutine` flags from `postgres/src/backend/access/{nbtree,hash,gist,gin,spgist,brin}/*.c`; new `internal/executor/amutils.go` reproduces `indexam_property`'s AM/index/column-level switch against it; 3 new `case` arms in `evalFuncCall` (`expr.go`). Also fixed a test-harness gap (not an engine gap): `scripts/pg-regress-runner.sh` never ran `amutils.sql`'s actual prerequisites (`geometry`/`create_index_spgist`/`hash_index`/`brin`), so several target indexes never existed — added a gated prerequisite block. Every AM-level/index-level property and every column-level property except `gist`/`spgist` `distance_orderable`/`returnable` (approximated with a column-type heuristic) now match the PG 18.3 oracle byte-for-byte. Landed with `internal/executor/amutils_test.go` (`TestIndexAMHasPropertyFunctions`). Two buckets remain: **(A)** `gist`/`spgist` per-opclass `DISTANCE_ORDERABLE`/`RETURNABLE` needs a real pg_amop/pg_amproc per-opclass lookup, not the type-name heuristic (goopg doesn't populate built-in opclasses into that registry). **(B)** the file's true remaining blocker — `geometry.sql`'s point/circle operator lexer gaps (`?`/`#`/`@`) block `circle_tbl`, so `gcircleind` is never created and every `gist`-AM row stays NULL; this is the point/box/circle operator-parsing milestone, own scope. Ledger row: `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0090. Design doc: `docs/design/m0134-0090-index-am-has-property.md`. Next M0134 task to select: **M0134-0091 (`async.sql`)**.
- [ ] **M0134-0141 — memoize.sql** — **PARKED 2026-08-25** (status was `not-tried`; RUN for the first time, sized at 0/1 PASS, 506-line diff). **Unreachable:** the case exists solely to exercise the planner/executor `Memoize` node — a whole plan-node type goopg has never implemented — and every one of its 15+ `EXPLAIN (ANALYZE)` blocks expects `Memoize` / `Cache Key` / `Cache Mode` / `Hits:`/`Misses:`/`Evictions:` lines; goopg instead plans the same joins as `Hash Join`/`Nested Loop` with no caching layer, so 100% of the diff is this one structural gap (same pattern as M0134-0008 `select_parallel`/M0134-0023 `write_parallel`). The tail of the file additionally exercises `enable_partitionwise_join` index-only-scan shape and real parallel-worker (`Gather`/`Parallel ... Workers Planned`) plans, both independently missing. RE-ARM TRIGGER: re-run `scripts/pg-regress-runner.sh --verbose memoize` after a dedicated Memoize-node milestone lands (and again after a parallel-query milestone, for the tail). Bonus discovery ledgered separately below: the final parallel-plan LATERAL query (`postgres/src/test/regress/sql/memoize.sql:239-241`, no `OFFSET 0`) throws a live goopg engine error `outer column ref twenty/level=1 out of range (depth=0)` at `internal/executor/expr.go:410` — the *earlier*, near-identical `OFFSET 0` variant of the same query (line 50-53) returns correct results, so the un-parenthesized/non-materialized LATERAL form has a real outer-row-binding bug independent of the Memoize/parallel gaps that block the rest of the file.
- [ ] **M0134-0142 — misc_sanity.sql** — regress-sql `not-tried` → **PARKED 2026-08-25, two contained fixes shipped** (0% parity, 72-line diff → 69-line diff). Fixed: (1) `pg_attribute.attoptions`/`attfdwoptions` were declared scalar `text` in `pgAttrColDefs` (`internal/initdb/initdb.go`) instead of PG's actual `text[]` (`postgres/src/include/catalog/pg_attribute.h:175,178`); (2) `oidToBuiltinTypeName` (`internal/executor/expr.go`) had no `::regtype` name resolution for OID 194 (`pg_node_tree`) or OID 2277 (`anyarray`), both fell through to raw-numeric-OID. Remaining 69-line diff needs: `pg_shdepend` catalog entirely unimplemented (standing item, confirmed a 4th time), no generic system-catalog TOAST-table registration (`pgClassReltoastrelidFor` special-cases only `pg_rewrite`, so `pg_type`'s varlena columns spuriously show as untoasted), and missing `pg_largeobject`/`pg_largeobject_metadata`/`pg_replication_origin` catalogs plus possibly `pg_authid.rolpassword`. Ledger row: `.ralph/deferral_ledger.md` 2026-08-25 M0134-0142. CSV row flipped `not-tried` → `failed` via `make regen-testport`, `pass_required` stays `no`. Gates: `go build ./...` PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (all packages); `scripts/tpch-spotcheck.sh` PASS (Q12=2 rows 17.75s, Q13=35 rows 7.53s); `make regen-testport`/`make check-testport-inventory` PASS. Next M0134 task to select: **M0134-0143**.
- [ ] **M0134-0143 — money.sql** — regress-sql `not-tried` → **PARKED 2026-08-25, sizing only** (0% parity, 691-line diff, no code shipped). The `money`/`cash` type is registered in `pg_type`/`pg_proc` for catalog lookups but has ZERO real implementation — every `cash_*` builtin (`cash_in`/`cash_out`/arithmetic/`cashlarger`/`cashsmaller`/`cash_words`) is a bare name-table entry with no Go handler, so `money` falls through `evalCast`'s catch-all pass-through as an undecorated int8/numeric: no cent scaling, no `$`/comma output formatting, no arithmetic operators, no overflow detection at the documented bounds. From-scratch type implementation (storage/parsing/output/arithmetic/overflow/functions) — REFACTOR-tier, matches the parked geometry-type gap in size/shape, not a bounded single-slice fix. Ledger row: `.ralph/deferral_ledger.md` 2026-08-25 M0134-0143. CSV row flipped `not-tried` → `failed` via `make regen-testport`, `pass_required` stays `no`. Next M0134 task to select: **M0134-0144**.
- [ ] **M0134-0145 — object_address.sql** — regress-sql `not-tried` → **PARKED 2026-08-25, sizing only** (0% parity, 598-line diff, no code shipped). Dominant gap (majority of the ~90 assertions): `pg_get_object_address`/`pg_identify_object`/`pg_identify_object_as_address`/`pg_describe_object` are ALL entirely unimplemented catalog functions — RE-CONFIRMS the standing pg_shdepend-shaped object-enumeration engine item (standing item 11) a 5th time. Two independent secondary gaps: `DROP OWNED BY` has zero parser AST node (blocked on the same object-enumeration engine); `CREATE PUBLICATION ... FOR TABLES IN SCHEMA <name>` is unparsed (`parseCreatePublicationTail`, `internal/parser/ddl.go:2301`) and real support needs a new `pg_publication_namespace` catalog + schema-membership filter in `internal/replication/logicalwalsender.go:373-377`, itself bounded-but-nontrivial, not a single-loop slice. Ledger row: `.ralph/deferral_ledger.md`, 2026-08-25, M0134-0145. CSV row flipped `not-tried` → `failed` via `make regen-testport`, `pass_required` stays `no`. Gates: `scripts/pg-regress-runner.sh --verbose object_address` (live sizing); `make regen-testport`/`make check-testport-inventory` PASS. Next M0134 task to select: **M0134-0146**.
- [ ] **M0134-0146 — oidjoins.sql** — regress-sql `not-tried` → **PARKED 2026-08-25, two real fixes landed, not sizing-only** (still 0% parity, diverges at check #5/219). Landed: (1) `pg_get_catalog_foreign_keys()` implemented end-to-end (was zero-handler `0A000` error) — 219-row static SRF (`internal/executor/pg_catalog_fk_data.go`, transcribed from `postgres/src/include/catalog/system_fk_info.h`) + `PgGetCatalogForeignKeys` plan node wired through the standard FROM-clause-SRF path (mirrors `pg_available_wal_summaries`). (2) Fixed a genuine cross-cutting plpgsql bug the SRF exposed: regclass-typed record fields (`FOR rec IN SELECT ...`) collapsed to bare OID digits on `rec.field`/`rec.field::text`/RAISE `%` access (`bindRecordRowComposite` + `lowerPLpgSQLExpr`'s `*parser.ColumnRef` composite-field branch in `internal/executor/plpgsql_runtime.go`); extracted the existing CastExpr regclass-resolution logic into a reusable `regclassOIDToName` helper (`internal/executor/expr.go`) and threaded `ctx` through the record-binding call chain to reach it. Verified live: 4 correct NOTICE lines with resolved catalog names before the next divergence. Remaining gap (independent, systemic): `pg_proc`'s live/queryable column set is missing 6 real PG18 columns present in its own heap-encode schema (`provariadic`/`pronargdefaults`/`proargmodes`/`proargnames`/`proargdefaults`/`prosqlbody` — `sys_pg_proc.go`'s `PGProcColumnsPG18()` declares them but a live `SELECT provariadic FROM pg_proc` 42703s) — very likely the first of several similar per-catalog column drifts the 219-row FK sweep would surface one at a time. Ledger row: `.ralph/deferral_ledger.md`, 2026-08-25, M0134-0146. CSV row stays `failed` (not `pass` — sweep still doesn't complete clean), `pass_required` stays `no`. Gates: `scripts/pg-regress-runner.sh --verbose oidjoins` (live); `go build ./...`; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); TPC-DS SF0.5 gate BLOCKED this loop (concurrent nightly CI batch holding the resource lock — `FATAL: the nightly CI batch is running`); `make regen-testport`/`make check-testport-inventory` PASS. Next resume point: find and unify/backfill pg_proc's queryable-column builder against `PGProcColumnsPG18()`, then re-run oidjoins to find the next-divergent catalog/column.
- [ ] **M0134-0147 — opr_sanity.sql** — regress-sql `not-tried` → **PARKED 2026-08-25, real fix shipped, not sizing-only** (0% parity, 1886-line diff → 1833-line diff). Landed: pg_proc's live/queryable schema (`registerPgProcView`, `internal/initdb/pg_proc_view.go`) was missing 7 real PG18 columns vs its heap-encode schema (`PGProcColumnsPG18()`, `internal/executor/sys_pg_proc.go`) — `provariadic`/`pronargdefaults`/`proallargtypes`/`proargmodes`/`proargnames`/`proargdefaults`/`prosqlbody` — the exact resume point M0134-0146 left. Added all 7 to `Columns` + populated across all 4 row-building blocks (builtin stubs, user routines, `catalog.BuiltinProcs()`, aggregates); `proargmodes`/`proargnames`/`proallargtypes` carry real per-routine data via two new helpers (`pgArgModesLiteral`/`pgArgNamesLiteral`/`pgAllArgTypesLiteral`); `provariadic`/`prosqlbody` stay constant (0/NULL, real gaps — deferred); `pronargdefaults`/`proargdefaults` are a real count / non-NULL placeholder pair kept mutually consistent. Verified live: every "column ... does not exist" divergence in this file is gone (only unrelated `amvalidate` remains), AND the same fix flipped oidjoins.sql's 219-row FK sweep from diverging at check #5 to running all 219 checks clean (new unrelated divergence at the very last non-pg_proc check — CSV row for oidjoins.sql updated in place, no status change). Ledger row: `.ralph/deferral_ledger.md`, 2026-08-25, M0134-0147. CSV row flipped `not-tried` → `failed`, `pass_required` stays `no`. Gates: `go build ./...` PASS; `go test ./internal/initdb/... -run TestPgProcView` PASS (16/16); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (all packages); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); `scripts/pg-regress-runner.sh --verbose opr_sanity` and `--verbose oidjoins` (live, before/after); `TestPort_PgDumpConnectionSetup` pre-existing unrelated failure confirmed via git-stash bisect (not a regression); TPC-DS SF0.5 gate BLOCKED (concurrent nightly CI batch holding the resource lock, same as M0134-0146 — change is orthogonal to TPC-DS); `make regen-testport`/`make check-testport-inventory` PASS. Next M0134 task to select: **M0134-0148**.
- [ ] **M0134-0148 — password.sql** — regress-sql `not-tried` → **PARKED 2026-08-25, real fix shipped, not sizing-only** (0% parity, 87-line diff → 77-line diff). Landed: `SET`'s enum-GUC validation error (e.g. `SET password_encryption = 'novalue'`) was missing the `HINT: Available values: ...` line PG attaches (`guc.c` `set_config_option`'s `PGC_ENUM` branch uses a separate `errhint()`) — added `misc.ValidationError{Msg,Hint}` and threaded it through `canonicalizeFrom` (`internal/utils/misc/guc.go`) and all 3 sibling SET call sites (`internal/postmaster/query.go` `handleSet`, `internal/postmaster/extended.go` SET/SET LOCAL fast paths, `internal/executor/operators_utility_settings.go` `utilitySettingsOp`). Verified live: both `SET ... = 'novalue'`/`= true` HINT-missing lines are gone. Remaining 77-line diff is 3 role-DDL NOTICE/WARNING messages (`user.c`/`crypt.c`: MD5-encrypted-password WARNING, MD5-cleared-on-rename NOTICE, empty-password-cleared NOTICE) that `internal/postmaster/role_ddl.go` already computes the trigger condition for but has no wire-protocol notice sink to emit through — `tryHandleRoleDDL` has no notice-carrying return and fans out to ~32 test call sites; full wiring deferred as a bounded follow-up (ledger row: `.ralph/deferral_ledger.md`, 2026-08-25, M0134-0148). CSV row flipped `not-tried` → `failed`, `pass_required` stays `no`. Gates: `go build ./...` PASS; `scripts/pg-regress-runner.sh --verbose password` (live, before/after — 87→77-line diff); `make check-testport-inventory`/`make regen-testport` PASS. Next M0134 task to select: **M0134-0149**.
- [ ] **M0134-0149 — path.sql** — regress-sql `not-tried` → **PARKED 2026-08-25, real fix shipped, not sizing-only** (0% parity, 111-line diff → 31-line diff, 0 residual `^ERROR`/`^-ERROR`). `path` was a raw-varlena pass-through with zero validation, the same state box/circle/line/lseg were in before their M0134 slices (M0134-0094/-0098/-0136/-0137). Landed `parsePathLiteral` (`internal/executor/expr.go`), a faithful port of `path_in`/`path_decode`/`pair_count` (`postgres/src/backend/utils/adt/geo_ops.c`) reusing the existing `linePairDecode`/`lineSingleDecode` primitives, plus `pathCanonicalText` (mirrors `path_out`); wired into `coerceTextLikeDatum` (`internal/executor/codec.go`), `pg_input_is_valid`/`pg_input_error_info` (`expr.go` + `operators_pg_input_error_info.go`), and the four functions `pg_proc` already had OIDs for but no dispatch (`isopen`/`isclosed`/`pclose`/`popen`). Remaining 31-line diff is entirely the already-ledgered box.sql/circle.sql/line.sql/lseg.sql-shared psql LINE-position-echo gap (`coerceTextLikeDatum` never threads `ExecError.Pos`) — no new ledger row needed, same gap the prior four geometry slices already recorded. Design: `docs/design/m0134-0149-path-typed-literal.md`. CSV row flipped `not-tried` → `failed`, `pass_required` stays `no`. Gates: `go build ./...` PASS; `scripts/pg-regress-runner.sh --verbose path` (live, before/after); `make check-testport-inventory`/`make regen-testport` PASS. Next M0134 task to select: **M0134-0150**.
- [ ] **M0134-0157a — parser: statement nodes reporting `Pos() == 0`** (filed 2026-08-29 out of M0134-0157). After the parser migration `*parser.CreateTableStmt` and `*parser.AlterTableStmt` return `Pos() == 0`; only `PrepareStmt`/`ExecuteStmt` carry real offsets in `CREATE TABLE rtchg (i int); PREPARE p AS SELECT * FROM rtchg; ALTER TABLE rtchg ADD COLUMN q int; EXECUTE p;`. This crashed the backend (`stmtSQL` sliced `sql[28:0]`, panic → socket closed) until M0134-0157 clamped it; the clamp contains the crash but `pg_prepared_statements.statement` still records the WRONG raw text for any PREPARE that is not the last statement of its batch, where PG stores exactly the text captured at PREPARE time. Fix = thread the statement start offset through the goyacc grammar's CreateTable/AlterTable productions. **Hard-won Rule #6 applies: read `docs/design/not_ralph/06-goyacc-parser-playbook.md` §12 before touching `grammar/*.y`**, build with `make gen-parser`, and treat the `parity_goldens.txt` diff as the review artifact. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0157 (by-catch, crash).
- [ ] **M0134-0157b — non-deterministic function overload resolution** (filed 2026-08-29 out of M0134-0157). `scripts/pg-regress-runner.sh plpgsql` alternates 4401/4402 diff lines across consecutive runs of the IDENTICAL binary: `select * from f1(42)` sometimes returns its 2 rows and sometimes raises `ERROR: function f1 is not unique`. A run-to-run-varying answer points at map-iteration order over the `pg_proc` candidate set; PG's order is deterministic (`func_match_argtypes` / `func_select_candidate`, `postgres/src/backend/parser/parse_func.c`). Until this is fixed, plpgsql diff-line counts are not a valid A/B signal. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0157 (sweep observation).
- [ ] **M0134-0158a — publication grammar subset + `pg_relation_is_publishable`** (filed 2026-08-29 out of M0134-0158). What still keeps `publication.sql` at 2233 diff lines: `FOR TABLES IN SCHEMA <s>` (28 errors), row filters `FOR TABLE t WHERE (…)` (12), per-table column lists `FOR TABLE t (a, b)` (6) and comma-separated publication objects (6) are all unparsed — upstream's `PublicationObjSpec`/`pub_obj_list` (`gram.y:10504-10585`); downstream of them, `pg_catalog.pg_relation_is_publishable` is undispatched (9 errors, newly REACHABLE only because `\dRp+` now parses) and 22 `publication does not exist` cascades follow the CREATEs that failed. Note `FOR TABLES IN SCHEMA` is also the gap M0134-0145 recorded from `object_address.sql`, and real support needs a `pg_publication_namespace` catalog plus a schema-membership filter in `internal/replication/logicalwalsender.go`. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0158.
- [ ] **M0134-0158b — `~~`/`!~~`/`~~*`/`!~~*` are not operators in goopg** (filed 2026-08-29 out of M0134-0158). `ParseBinaryOp` (`internal/parser/op.go:101`) has no case for PG's LIKE/ILIKE operator spellings (`postgres/src/include/catalog/pg_operator.dat`). On the `OPERATOR(...)` path that is an honest `unsupported operator "!~~"`; on the BARE path it is a silent **misparse** — the lexer emits `~~` as two `~` tokens, so `'a' ~~ 'b'` parses as `'a' ~ (~ 'b')` and reaches the executor as a regex match over a bitwise NOT. Fixing needs BOTH the OpCode mapping and a lexer/adapter change to rejoin a `~~` run outside `OPERATOR(...)` (`op_run` only rejoins inside the parens), so it is not OpCode-only. Pinned as a known gap by `TestPatternOperatorSpellingsStillUnsupported` so closing it is a reviewed golden drift. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0158 (by-catch, misparse).
- [ ] **M0134-0159 — regproc.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-08-29** (766 → 758 diff lines, `^+ERROR` 63 → 61). Sized live for the first time. The loop's shipped fix was **engine-wide, not regproc-specific**: the case's very first statement is `/* If objects exist, return oids */\nCREATE ROLE regress_regrole_test;` and it failed, because goopg's postmaster classifies the statement classes the goyacc grammar deliberately does not carry (role DDL, database DDL, the `CREATE SCHEMA` header — playbook §12) by PREFIX-matching `normalizeCompatSQL`, and that normalizer kept comments verbatim where PG's lexer folds them into `{whitespace}` (`scan.l:213-215`, rule body `/* ignore */` at `:443`). **CREATE/ALTER/DROP ROLE|USER|GROUP and CREATE/ALTER/DROP DATABASE were therefore unreachable from any commented SQL script.** Fixed with a `stripSQLComments` pre-pass (design `docs/design/m0134-0159-sql-comment-stripping-compat-intercepts.md`). Verified no-regression by a 12-case A/B against a HEAD worktree: ten cases byte-identical, and `privileges`'s apparent +10 lines is cross-case contamination — run alone on a fresh server it is byte-identical. Remaining buckets are ledgered and independent: the whole `to_reg*` soft-error family is undispatched (~35 of 61 errors); function-style reg* casts (`internal/executor/expr.go:14296`) are echo STUBS so `regclass('pg_class')` prints `1259`; `regoper`/`regoperator` have no name-resolution seam; `pg_input_error_info` returns an empty row. **Re-arm trigger:** select again once the reg* input/output connection lands (that slice must carry `TestRegCastToStringRendersName`, which M0134-0005a's revert was pinned by).
- [ ] **M0134-0159a — reg\* function-style casts are echo stubs** (filed 2026-08-29 out of M0134-0159). `evalFuncCall`'s `regproc, regprocedure, regclass, regtype, regnamespace` arm (`internal/executor/expr.go:14296`) returns its argument UNCHANGED (regclass alone resolves, and only to a bare OID), so `regclass('pg_class')` prints `1259` where PG prints `pg_class`, and `regproc('pg_catalog.now')` / `regtype('pg_catalog.int4')` echo the unresolved input where PG prints `now` / `integer` — a silent wrong value, not an error. PG runs the type's INPUT function then its OUTPUT function (`regprocin`/`regtypein`, `postgres/src/backend/utils/adt/regproc.c`); goopg already HAS both halves (`regIdentifierInput` in `internal/executor/reg_identifier.go`, `RegOut`, and the OID→name render already works: `1259::regclass` → `pg_class`, `23::regtype` → `integer`) and simply does not connect them here. `regrole`/`regcollation`/`regoper`/`regoperator` additionally 42883 in the function-call form. **Hazard:** M0134-0005a already tried widening `evalCast`'s regclass case and REVERTED it after it regressed `TestRegCastToStringRendersName` — that guard must be run in front of any attempt. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0159.
- [ ] **M0134-0159b — the `to_reg*` soft-error family is undispatched** (filed 2026-08-29 out of M0134-0159). `to_regclass`, `to_regoper`, `to_regoperator`, `to_regproc`, `to_regprocedure`, `to_regtype`, `to_regrole`, `to_regnamespace`, `to_regcollation` and `to_regtypemod` all raise 42883 — ~35 of `regproc.sql`'s remaining 61 `^+ERROR`s and its largest bucket. Upstream defines them as the `reg*in` functions wrapped so a lookup MISS returns NULL instead of raising (`postgres/src/backend/utils/adt/regproc.c`). `pg_input_error_info(text, text)` likewise returns an EMPTY row rather than the soft error's (message, detail, hint, sql_error_code). Blocked behind **M0134-0159a**: without the input/output connection these would return bare OIDs, trading 42883 for a wrong value, so the two land together. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0159.
- [ ] **M0134-0159c — goopg's system B-tree cannot split** (filed 2026-08-29 out of M0134-0159, by-catch). Once `pg_authid_rolname_index` (OID 2676) outgrows a single page, every further `CREATE ROLE` hard-fails with `ERROR: split sys btree 2676: split: unsupported system btree OID 2676`, and a later `DROP ROLE` does not reclaim the slot. Roughly ten roles in one cluster reach the ceiling, so any multi-case regress run — or any real multi-tenant setup — hits it. Real PG splits system indexes through the same `_bt_split` path as user indexes. Found while proving M0134-0159 caused no regression (it moved the run one role closer to the ceiling; it did not create it). Resume: grep `unsupported system btree OID` in `internal/` for the split guard; the regression guard is simply `CREATE ROLE` in a loop past the page boundary. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0159 (by-catch, engine limit).
- [ ] **M0134-0160 — reloptions.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-08-29** (232 → **201** diff lines, `^+ERROR` 17 → **6**, `^-ERROR` 18 → **7**). Sized live for the first time. The loop's shipped fix was **engine-wide, not reloptions-specific**: goopg validated storage parameters only by *recognising* them — every `WITH (...)` consumer was a chain of `if v, ok := s.With["fillfactor"]; ok { … }` — so a name nobody looked for was **silently accepted and dropped**. `CREATE TABLE t(i int) WITH (not_existing_option=2)`, `WITH (bad_ns.fillfactor=2)`, `CREATE INDEX … WITH (not_existing_option=2)` and `ALTER TABLE … SET (no_such_option=1)` all SUCCEEDED where PG raises 22023 — a correctness gap on its own (a typo'd `autovacuum_enable=false` looks like it took effect and does nothing) that also **cascades**: the silently-created relation turns every later negative case into a spurious "relation already exists". Fixed with a PG-faithful registry, `internal/executor/reloptions_catalog.go` — PG's five static option tables (`reloptions.c`) as a name → `relopt_kind` bitmask, plus the two-pass check upstream runs (`transformRelOptions` validates NAMESPACES over the whole list first, `:1275`; then `parseRelOptions` validates NAMES, `:1488`) — wired at six sites with upstream's own per-caller parameters. All three parameters are load-bearing: `DefineRelation`/CTAS get HEAP + `{"toast"}` + `acceptOidsOff=true`, `ATExecSetRelOptions` dispatches HEAP-vs-VIEW by relkind, and `DefineIndex` gets the AM's kind with validnsps NULL. `acceptOidsOff` (`:1307-1322`) is not trivia — a first cut without it broke `CREATE TEMP TABLE withoutoid() WITH (oids = false)`, caught by the A/B. Ordering matters too: `index_reloptions()` runs before `index_create()`'s name-conflict test, so the check sits BEFORE the duplicate-name check in `execCreateIndex`. `CreateIndexStmt` gained `WithOptionNames []string` (the index WITH clause reached the AST as seven typed fields with every other name discarded, so the executor could not tell "absent" from "unrecognized"); golden regeneration was **37 rows all differing ONLY by the added field**, purely additive. Three existing tests pinned non-PG behavior and were corrected after verifying each against a **live PG 18.3 oracle**: `buffering` is GiST-only (the test used `USING btree`), `fastupdate` is GIN-only (the test ALTERed a btree), and `TestAlterTableSetReloptionsBounds` explicitly pinned "an unrecognized option is accepted and ignored" — the bug itself. Design `docs/design/m0134-0160-reloption-name-registry.md`, indexed in `docs/design/README.md`. Guards: `internal/executor/reloptions_catalog_test.go` (registry kind membership incl. the HEAP-only asymmetries, both error shapes, pass ordering, `acceptOidsOff`, AM mapping, end-to-end no-relation-created cascade guard). Gates: **14-case regress A/B vs a HEAD worktree** — `reloptions` 232→201, `alter_table` 3792→3784 (two previously-missing errors now raised, no new error class), **twelve cases byte-identical**; `go test ./internal/executor/` + `./internal/parser/` PASS; `RALPH_PRECOMMIT_SCOPE=units` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=34); `make regen-testport` / `make check-testport-inventory` PASS. Remaining buckets are ledgered and independent: duplicate-option detection (`parameter "x" specified more than once`) needs the same ordered-name-list treatment on `CreateTableStmt`; `ALTER … SET` still APPLIES only 4 of the 24 options it now validates; `RESET (x = v)` is accepted where PG errors and `RESET (toast.x)` is a parse error; numeric error messages reformat the value instead of echoing the input; a bare `WITH (fillfactor)` is dropped rather than read as `fillfactor=true`. **Re-arm trigger:** select again once the WITH clause carries an ORDERED name list on `CreateTableStmt`/`AlterTableAction` — that unblocks duplicate detection and the last three `^+ERROR`s at once.
- [ ] **M0134-0160a — the WITH clause is a map, so PG's list semantics are lost** (filed 2026-08-29 out of M0134-0160). `CreateTableStmt.With` / `AlterTableAction.With` are `map[string]string`, so a duplicate storage parameter silently keeps the last binding instead of raising `parameter "x" specified more than once` (`reloptions.c:1230`), and error reporting picks the lexicographically-first offender rather than the source-order-first one. Fix shape is known and already prototyped on the index side this loop: mirror `CreateIndexStmt.WithOptionNames []string` onto both, populate it where `str_pair_list` is folded into the map (`internal/parser/support.go`), then add the duplicate scan and switch `validateRelOptionNames` off its sorted copy. Unblocks the last three `^+ERROR`s of `reloptions.sql`. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0160.
- [ ] **M0134-0160b — `ALTER … SET (reloptions)` applies 4 of the 24 options it validates** (filed 2026-08-29 out of M0134-0160). `execAlterTableSetReloptions` (`internal/executor/operators_ddl.go`) handles `parallel_workers`, `fillfactor`, `autovacuum_enabled`, `toast_tuple_target` plus the view trio and silently ignores everything else, including the whole `toast.` namespace; `ALTER INDEX … SET` applies only `fastupdate`, so `ALTER INDEX i SET (fillfactor=40)` validates and then does nothing (PG stores it — verified against the 18.3 oracle). PG merges into the existing array and re-validates the whole thing (`ATExecSetRelOptions`, `tablecmds.c:16690`), so every option CREATE accepts, ALTER accepts too. `TestAlterTableSetReloptionsMatchesCreateWith` already exists as the sibling-path pin. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0160.
- [ ] **M0134-0161 — replica_identity.sql** — **PARKED 2026-08-29** (regress-sql `not-tried` → `failed`; 194 → **189** diff lines, `^-ERROR` 3 → **2**). Sized live for the first time; the case is genuinely failing, not a stale status. The loop shipped an **engine-wide** fix the case merely exposed: `pg_index.indimmediate` is keyed on the `DEFERRABLE` flag ALONE (`index.c:1049`, `index.c:2080-2082`), never on INITIALLY DEFERRED, and seven goopg consumers had drifted into three different answers — both pg_index row builders hardcoded `true`, the `REPLICA IDENTITY USING INDEX` check and both ON CONFLICT arbiter checks keyed on `InitiallyDeferred`, and the inferred-by-column arbiter branch had no check at all — so a `UNIQUE (b) DEFERRABLE` constraint was **silently ACCEPTED** as both a replica identity and an ON CONFLICT arbiter where PG rejects it. Unified on `(*catalog.Index).IsImmediate()`; oracle-verified on a live PG 18.3 before touching the sites that existing tests pinned. Design `docs/design/m0134-0161-indimmediate-deferrable-key.md`. 13-case regress A/B vs a HEAD worktree: **twelve byte-identical**, zero regressions. Re-arm: pick this case back up once 0161d (pg_get_expr default schema-qualification) lands, which alone clears three of the remaining hunks.
  - [ ] **M0134-0161a** — `'pg_constraint'::regclass` resolves to no `pg_class` row (0 rows vs PG's `n`); system-catalog pg_class builder coverage hole.
  - [ ] **M0134-0161b** — a foreign table's index gives 42704 `index "x" ... does not exist` where PG gives 42809 `"x" is not an index for table "t"`; needs a namespace-wide index-by-name lookup in `resolveReplicaIdentityIndex`.
  - [ ] **M0134-0161c** — inline `id serial constraint pk primary key` does not name the backing index `pk` (goopg auto-names `<table>_pkey`).
  - [ ] **M0134-0161d** — `pg_get_expr` schema-qualifies column defaults unconditionally (`nextval('public.x_id_seq'::regclass)`); same class as m0134-0032, also visible in `alter_table`/`dependency`/`publication`.
  - [ ] **M0134-0161e** — partial-index predicate deparse drops the literal cast and adds parens: `WHERE (keyb <> '3')` vs PG's `WHERE keyb <> '3'::text`.
  - [ ] **M0134-0161f** — `ALTER COLUMN id TYPE bigint` is not reflected in `\d` (still reports `integer`).
  - [ ] **M0134-0161g** — `CREATE TABLE ... PARTITION OF` leaves `Number of partitions: 0`, and a partitioned PK index is not marked `INVALID` before `ALTER INDEX ... ATTACH PARTITION`.
  - [ ] **M0134-0161h** — spurious `LINE 1: ... ^` position pointer on `DROP CONSTRAINT` not-found (PG's `ATExecDropConstraint` has no `errposition()`); blocked on the `Pos == 0` sentinel audit from m0134-0033.
- [ ] **M0134-0162a** — `pg_authid`'s rolname btree (2676) and oid btree (2677) have no `keyMetaForSysBtree` entry, so the index cannot SPLIT: once one index page fills, every further `CREATE ROLE` dies with `split: unsupported system btree OID 2676`, capping a cluster at ~one page of roles. Pre-existing; it is why `roleattributes.sql` still fails inside a multi-case sequence though it passes standalone. Resume: add both OIDs to the registry in `internal/executor/sys_catalog_btree_split.go`, mirroring the pg_tablespace 2697/2698 entries.
- [ ] **M0134-0162b** — `pg_roles` exposes only 4 of pg_authid's 12 columns, so psql's `\du` cannot render the "No inheritance" attribute even now that `pg_authid.rolinherit` is correct. Resume: widen the `pg_roles` virtual builder in `internal/catalog/catalog.go` to PG's `system_views.sql` definition (all columns, rolpassword → `'********'`), reusing the adjacent pg_authid `rowFor` closure.
- [ ] **M0134-0162c** — `SET ROLE` consults neither `rolinherit` nor `pg_auth_members.set_option`: PG lets a NOINHERIT member reach a granted role's privileges through an explicit `SET ROLE` gated on `set_option` (`acl.c` `member_can_set_role`). Resume: add `MemberCanSetRole` to `internal/catalog/catalog.go` and gate the SET ROLE handler on it.
- [ ] **M0134-0163 — rowsecurity.sql** — **PARKED 2026-08-29** (regress-sql `not-tried` → `failed`; `^+ERROR` 301 → **137**, of which `permission denied for table` 165 → **11**; raw diff lines 5389 → 6047 — the expected direction, since 154 statements that used to abort with a single `+ERROR` line now return a full, RLS-unfiltered result set, so line count is the wrong metric for this change and `^+ERROR` is the honest one). Sized live for the first time. The shipped fix is **engine-wide, not rowsecurity-specific**: over half the case's errors were spurious `permission denied` with nothing to do with row security, caused by two independent ACL defects that each made an ordinary GRANT confer nothing. (1) The GRANT/REVOKE recorders (`internal/postmaster/grant_ddl.go`) run on `query.go`'s autocommit fast path BEFORE the executor, so they hold no `executor.Context` and resolved the object name with a bare `Catalog.LookupTable`, which tries only the bare key, `public.<name>` and `pg_catalog.<name>` — a GRANT naming an unqualified table in ANY other schema resolved to nothing, `continue`d past the grant loop, and returned a successful `GRANT` tag having recorded no aclitem at all (silent and total; hits every case that does `SET search_path = <schema>` then grants unqualified, and every app whose tables live outside `public`). Fixed with `grantTableLookup`, wrapping the catalog in the same `catalog.WithSearchPath` the planner uses, threaded from `searchPathSchemas(sess)` — and given to REVOKE too, or the pair desynchronises and a REVOKE targets a different relation than the GRANT it undoes. (2) `HasTablePrivilege` was a single map probe on the querying role's own key — only half of `aclmask()`'s first pass (`postgres/src/backend/utils/adt/acl.c:1389`): it never matched an aclitem granted to PUBLIC (`ai_grantee == ACL_ID_PUBLIC`), and never ran the second pass' `has_privs_of_role(roleid, ai_grantee)` membership test, so `GRANT … TO PUBLIC` and `GRANT … TO <group>` both landed in relacl and granted nobody anything. It is now aclmask, structured like it (pass 2 keeps aclmask's `ai_privs & remaining` pre-filter); NOINHERIT is honoured for free through M0134-0162's `rolinherit`-aware `HasPrivsOfRole`. Locking: pass 2's candidates are collected under the read lock and adjudicated after it drops — `HasPrivsOfRole`/`RoleOID` take `c.mu` themselves and nesting the RLock deadlocks against a waiting writer. Design `docs/design/m0134-0163-grant-public-and-searchpath-acl.md`. Gates: 8-case ACL A/B vs a HEAD worktree (`privileges`, `sequence`, `roleattributes`, `create_role`, `security_label`, `init_privs`, `dependency`, `password`) byte-identical — ZERO regressions; `privileges.sql` did not move because it operates in `public`, where the bare lookup already resolved. PARKED because the remainder is dominated by RLS enforcement itself (0163a) — REFACTOR-tier.
- [ ] **M0134-0163a — row-level security is never enforced at scan time** (filed 2026-08-29 out of M0134-0163). `CREATE POLICY` stores `pg_policy` rows correctly, but no policy is ever applied: `SELECT * FROM document` as `regress_rls_bob` returns all 10 rows where PG returns the 5 the policy admits, and `f_leak` fires on every row — the exact leak the case exists to catch. PG injects policy quals during rewrite (`postgres/src/backend/rewrite/rowsecurity.c` `get_row_security_policies`, called from `fireRIRrules`), wrapping the relation in a security-barrier subquery so leakproof ordering is preserved against functions like `f_leak`; goopg has no rewrite-time RLS stage at all. Resume: add an RLS rewrite stage between parse and plan, honouring `ALTER TABLE … ENABLE/FORCE ROW LEVEL SECURITY`, the `row_security` GUC, PERMISSIVE-vs-RESTRICTIVE composition, per-command (`FOR SELECT/INSERT/…`) policy selection, `WITH CHECK`, and `BYPASSRLS`. REFACTOR-tier (rewriter/planner), which is why -0163 is parked rather than closed.
- [ ] **M0134-0163b — `pg_policies` system view does not exist** (filed 2026-08-29 out of M0134-0163). `SELECT * FROM pg_policies` errors `relation "pg_policies" does not exist`, the largest single contributor to the case's 51 remaining `relation does not exist` errors, and `\dp`/policy introspection depends on it. Resume: add the virtual builder in `internal/catalog/catalog.go` alongside the other `pg_*` views, projecting `pg_policy` per `postgres/src/backend/catalog/system_views.sql`'s definition (schemaname, tablename, policyname, permissive, roles, cmd, qual, with_check).
- [ ] **M0134-0163c — `CREATE POLICY … AS <bogus>` is accepted instead of erroring** (filed 2026-08-29 out of M0134-0163). PG raises `unrecognized row security option "ugly"` with `HINT: Only PERMISSIVE or RESTRICTIVE policies are supported currently.`; goopg parses the unknown option as if absent and then fails downstream with `policy "p1" for table "document" already exists`, masking the real error. Also filed here: `row_security_active()` is missing (2 errors). Resume: validate the `AS <ident>` token against {PERMISSIVE, RESTRICTIVE} in the CREATE POLICY path and raise the upstream message+hint; add `row_security_active(regclass)` as a builtin.
- [ ] **M0134-0164 — sanity_check.sql** — **PARKED 2026-08-29** (regress-sql `not-tried` → `failed`; 77 → **21** diff lines standalone, 129 → **21** inside the 13-case A/B, whose baseline is higher only because that run loads all 13 cases' objects first — both sides saw the identical load). Sized live for the first time. `sanity_check.sql` carries no schema of its own — a `VACUUM` plus two catalog-invariant queries that audit whatever the schedule already built — so it is a pure invariant probe over `pg_class`, and its diff split cleanly in two: 2 lines for the first query, **59 for the second**. The shipped fix is **engine-wide, not sanity_check-specific**: every view in the database reported a non-zero `pg_class.relfilenode`, where PG's `heap_create` assigns a relfilenumber only when `RELKIND_HAS_STORAGE` holds (`postgres/src/backend/catalog/heap.c:335-345`, macro at `postgres/src/include/catalog/pg_class.h:200`), leaving `v`/`c`/`f`/`p`/`I` at 0 — exactly the set the case's second query names. goopg has FOUR pg_class row builders and they disagreed: both VIRTUAL ones (`internal/catalog/catalog.go` `PGClassRowsForDBOid`, the table row AND the index row) handed out the relation OID unconditionally; the HEAP one (`internal/executor/pg18_user_catalog_rows.go` `buildUserPGClassRow` — the row a real PG 18.3 attached to the cluster reads) already zeroed `p`/`v` via an ad-hoc `relkind == "p" || relkind == "v"` check that had no virtual twin and predates foreign tables and partitioned indexes; the composite builder hardcoded 0. The classic sibling-path divergence — goopg's own introspection and a streamed catalog disagreed about whether a view has a data file — and `initdb` had independently encoded the correct convention (`relcache_init.go:770`, `initdb.go:6072`), leaving the runtime virtual builder the sole outlier. Fixed with ONE shared rule, `catalog.RelkindHasStorage` + `RelfilenodeForRelkind`, routed through all three divergent builders with the ad-hoc check deleted rather than duplicated, rendered via a hoisted local per the existing `relOfType`/`idxTablespace` convention so the row literals keep their single-token column width (a multi-token expression re-aligns ~50 unrelated comment columns and churns the gofmt baseline). Deliberately NOT changed: `reltablespace` has the analogous `RELKIND_HAS_TABLESPACE` rule (`pg_class.h:219`) but no goopg surface can set a tablespace on a storage-less relation, so the field is already 0 — noted in the design doc, not ledgered as a defect. Design `docs/design/m0134-0164-relfilenode-storage-less-relkinds.md`. Gates: 13-case regress A/B vs a HEAD worktree (`create_view`, `create_table`, `alter_table`, `rules`, `dependency`, `inherit`, `matview`, `foreign_data`, `sequence`, `indexing`, `create_index`, `psql`, `sanity_check`) — **10 byte-identical, ZERO regressions**; `alter_table` 3800 → 3798 independently confirms the fix (it snapshots `relfilenode` into a temp table and reports each relation's storage as `own`/`none`: `at_partitioned` now reads `none` and matches expected byte-for-byte, and the `relkind='I'` parent index moved `own` → `none`); `create_index` holds the same 3340 lines but is not byte-identical because a **Go pointer address leaks into `pg_get_indexdef`** (`WHERE (c1::text > &{105 0xcf77f334c00 C})`), which varies run to run — pre-existing at HEAD, ledgered separately. New unit guard `internal/executor/pg_class_relfilenode_storage_test.go` (property asserted over the whole virtual `pg_class` enumeration with a non-vacuity guard, plus the rule pinned to the macro over the full relkind alphabet); confirmed to FAIL on a reverted builder rather than pass vacuously. PARKED because the residual 21 lines are one independent cause, 0164a.
- [ ] **M0134-0164a — `pg_index` describes no bootstrap system-catalog index** (filed 2026-08-29 out of M0134-0164). `PGIndexRowsForDBOid` (`internal/catalog/catalog.go`) enumerates `c.AllIndexes(dbOid)` — user indexes only — so `sanity_check.sql`'s first query reports `pg_class` and `pg_type` as system catalogs with an `oid` column and no unique immediate index on it. goopg DOES maintain the physical files (`pg_class_oid_index` 2662, `pg_type_oid_index` 2703 — see `internal/executor/sys_catalog_index_insert.go`), and the virtual pg_class row for `pg_class` already advertises `relhasindex='t'`, so the catalog is internally inconsistent today. Resume: emit pg_index rows for the bootstrap catalog indexes goopg really writes, cross-checked against upstream's `DECLARE_UNIQUE_INDEX` entries. NOT a one-liner: these rows tell a real PG standby which catalog indexes it may descend, which is PG-standby catalog-consumption territory with its own blockers — which is why -0164 is parked rather than closed.
- [ ] **M0134-0165a — `client_min_messages` rejects upstream's hidden `info`/`debug` aliases** (filed 2026-08-29 out of M0134-0165). `client_message_level_options` (`postgres/src/backend/utils/misc/guc_tables.c`) marks `debug` (alias for `debug2`) and `info` `hidden: true`: `config_enum_lookup_by_name` ACCEPTS them as input while `config_enum_get_options` omits them from `pg_settings.enumvals`. goopg's `EnumOptions` lists only the nine visible spellings, so `SET client_min_messages TO 'info'` errors where PG succeeds. NOT a one-liner: adding them straight to `EnumOptions` would trade an input-rejection divergence for an `enumvals` divergence. Resume: give `misc.Variable` a hidden-option concept (e.g. `EnumHiddenOptions`, consulted by input validation but not by the `enumvals` renderer), then add both spellings; `clientMinMessagesElevel` (`internal/libpq/messages.go`) already understands them. Generalises to any other GUC with hidden aliases.
- [ ] **M0134-0165b — `plpgsql.sql` is nondeterministic at `select * from f1(42)`** (filed 2026-08-29 out of M0134-0165). The same build reports three different outcomes across runs — `ERROR: missing expression`, `ERROR: function f1 is not unique`, and a successful two-row result; an A/A on one unchanged tree produced 4400 / 4402 / 4401 diff lines. "not unique" means an earlier `drop function f1(...)` left a stale overload behind, so the polymorphic create/drop sequence at `postgres/src/test/regress/sql/plpgsql.sql:1565-1695` is not converging to a single `f1`. Resume: run `scripts/pg-regress-runner.sh plpgsql` two or three times, diff the `.diff` files, then find which `drop function f1(x anyelement|anyarray|anyrange|anycompatible...)` fails to remove its overload (suspect polymorphic-signature matching in DROP FUNCTION name resolution, `internal/executor/operators_ddl.go`). **Until fixed, any loop reading `plpgsql.sql`'s line count must run an A/A first** — byte-level A/B on that case is not trustworthy.
- [ ] **M0134-0166a — hyperbolic / degree-trig / gamma / error-function family entirely unimplemented** (filed 2026-08-29 out of M0134-0166). `sinh cosh tanh asinh acosh atanh sind cosd tand asind acosd atand atan2d erf erfc gamma lgamma` all raise "function … does not exist" — ~40 of the 52 `^+ERROR`s left in `float8.sql`. Resume: add each to `evalFuncCall`'s math arms in `internal/executor/expr.go` (near `sqrt`/`power`/`exp`/`ln`) per `postgres/src/backend/utils/adt/float.c`, plus a `pg_proc` seed row each. The degree variants need upstream's exactness machinery (`init_degree_constants`, `sind_0_to_30`, `asind_q1`) so `sind(30)` is exactly `0.5`.
- [ ] **M0134-0166b — `@` (float8abs) and `&#124;/` (dsqrt) prefix operators are unlexed** (filed 2026-08-29 out of M0134-0166). `SELECT @f.f1` gives "operator does not exist: @"; `SELECT &#124;/ float8 '64'` gives "syntax error at or near /". Same `@` gap already ledgered for `float4.sql` (M0134-0153). Resume: `grammar/*.y` + `internal/parser` lexer — both are operator tokens in `postgres/src/backend/parser/scan.l`. **Read `docs/design/not_ralph/06-goyacc-parser-playbook.md` §12 first.**
- [ ] **M0134-0166c — `trunc`/`ceil`/`ceiling`/`floor` of a large float8 overflow through int64** (filed 2026-08-29 out of M0134-0166). `trunc(1.2345678901234e+200)` returns `-9.223372036854776e+18` — a silent wrong value, engine-wide, not float8.sql-specific. Resume: `internal/executor/expr.go`'s `trunc`/`ceil`/`floor` arms; PG's `dtrunc`/`dceil`/`dfloor` (`float.c`) stay in float64 and never convert to an integer type.
- [ ] **M0134-0166d — float8 arithmetic is evaluated in decimal, not float64** (filed 2026-08-29 out of M0134-0166). `1004.3 / '-10'` gives `-100.43` where PG gives `-100.42999999999999`, and `'nan'::float8 / '0'::float8` raises "division by zero" where PG returns NaN (`float8div`, `float.c:842`, errors only when the divisor is zero *and* the dividend is not NaN). REFACTOR-tier: goopg has no float Datum Kind, so this is a carrier-representation change. Also filed here: `float8send('1.5'::float8)` renders a garbled/empty bytea though the arm exists (M0134-0153).
- [ ] **M0134-0167a — no SP-GiST access method** (filed 2026-08-29 out of M0134-0167). `USING spgist` is catalog-only, so no `spgist.sql` EXPLAIN can ever match and the case cannot close. Needs its own milestone: page layout, `spgdoinsert`/`spgscan`, WAL records and the five opclass support functions per `postgres/src/backend/access/spgist/`, then remove `spgist` from `execCreateIndex`'s catalog-only branch (`internal/executor/operators_ddl.go`). Re-arm trigger: an SP-GiST milestone landing.
- [ ] **M0134-0167b — explicit `ASC` / `NULLS LAST` still accepted on orderless AMs** (filed 2026-08-29 out of M0134-0167). PG errors on `USING spgist(p ASC)` (`SORTBY_ASC != SORTBY_DEFAULT`, `indexcmds.c:2225-2229`) and on `USING spgist(p NULLS LAST)` (`:2230-2234`); goopg's AST cannot represent either — `opt_index_dir` (`grammar/goopg_ext.y`) is a plain `bool` and `newIndexElem` (`internal/parser/support.go:1488`) defaults `IndexColOrder.NullsFirst` from `Descending`. Resume: tri-state `opt_index_dir` + `OrderingExplicit`/`NullsOrderExplicit` on `parser.IndexColOrder` (`internal/parser/ast.go:1745`), then drop the `co.NullsFirst != co.Descending` proxy. **Grammar edit — read `docs/design/not_ralph/06-goyacc-parser-playbook.md` §12 first.**
- [ ] **M0134-0167c — capability gate not applied to the constraint-side index paths** (filed 2026-08-29 out of M0134-0167). Inline `UNIQUE`/`PRIMARY KEY`/`EXCLUDE` table constraints and `ALTER TABLE ADD CONSTRAINT … USING INDEX` never call `checkIndexAMCapabilities`; upstream's `amgettuple == NULL` → "does not support exclusion constraints" (`indexcmds.c:883`) and gist-only "does not support WITHOUT OVERLAPS constraints" (`:888`) are unported. Resume: a struct-free second entry point in `internal/executor/amutils.go` (exclusion needs `IndexAMCapability.HasGetTuple`, already in the table).
- [ ] **M0134-0167d — `pg_get_indexdef` prints a Go value dump for a COLLATE in a partial-index predicate** (filed 2026-08-29 out of M0134-0167). `create_index.sql`'s `concur_exprs_index_pred` renders as `WHERE (c1::text > &{105 0xbd0b65152c0 C})` — a raw pointer, so the output is nondeterministic run-to-run and made two otherwise byte-identical A/B runs differ. Resume: `internal/executor/expr.go`'s `pg_get_indexdef` predicate deparse — `*parser.CollateExpr` hits a `%v` default arm instead of `expr COLLATE "name"` (ruleutils.c `get_rule_expr`, T_CollateExpr).
- [ ] **M0134-0168 — sqljson.sql** — regress-sql, **sized live 2026-08-29 (`not-tried` → `failed`) and PARKED**. 1771 diff lines / 206 `^+ERROR` / 53 `^-ERROR`, and *every one* of them is ONE REFACTOR-tier missing subsystem: goopg's grammar has no SQL/JSON constructor or predicate support at all (`JSON()`, `JSON_SCALAR`, `JSON_SERIALIZE`, `JSON_OBJECT`, `JSON_ARRAY`, `JSON_OBJECTAGG`, `JSON_ARRAYAGG`, `IS [NOT] JSON`, plus `FORMAT JSON [ENCODING …]` / `RETURNING <type>` / `{WITH|WITHOUT} UNIQUE KEYS` / `{NULL|ABSENT} ON NULL` / query-expression arguments). No second cause to slice off. That blocker also gates M0134-0169 and -0170, so size it once for all three (ledger row 0168a). The loop instead shipped the **engine-wide** silent-acceptance bug the case exposed: `evalExprSlot`'s CastExpr `regclass` arm re-implemented the string-input lookup inline and FELL THROUGH on a miss, so `'nosuch'::regclass` evaluated to the raw name string — surfacing as the wrong error elsewhere (psql `\sv`'s `::regclass::oid` chain said `invalid input syntax for type oid`) and as silently EMPTY result sets (an unresolved regclass compares as text, so `\d`'s "Referenced by:" query matched nothing and the whole section was missing). The arm now delegates to `regIdentifierInput` — goopg's already-faithful `reg*in` port, whose callers were only the heap-write/`reg*[]`/EXECUTE-parameter paths — and that shared arm gained `makeRangeVarFromNameList`'s segment-count rules (42601 too-many-dotted-names, 0A000 cross-database). Design `docs/design/m0134-0168-regclass-string-input-regclassin.md`; 15-case regress A/B vs a HEAD worktree: 10 byte-identical, `create_index` 3340→3335, `alter_table` 3754→3728, `inherit` 3251→3238, `foreign_key` 3486→3490 (net-forward, unmasked a pre-existing describe bug). Guard `internal/executor/regclass_input_test.go`, revert-checked. **Re-arm trigger:** flip back to selectable once a SQL/JSON constructor milestone lands. Follow-ups 0168a–0168f below.
- [ ] **M0134-0168a — SQL/JSON constructor & predicate family (whole subsystem)** (filed 2026-08-29 out of M0134-0168). Port `json_value_expr` / `json_object_constructor` / `json_array_constructor` / `json_aggregate_func` / `json_predicate_type_constraint` from `postgres/src/backend/parser/gram.y`, then `transformJsonValueExpr` & friends (`parse_expr.c`) and the executor side (`utils/adt/json.c`, `jsonfuncs.c`). Blocks M0134-0168, -0169 (`sqljson_jsontable.sql`) and -0170 (`sqljson_queryfuncs.sql`) — size all three together. **Grammar edit — read `docs/design/not_ralph/06-goyacc-parser-playbook.md` §12 first.**
- [ ] **M0134-0168b — `\d` lists a partitioned table's per-partition FK constraints under "Referenced by:"** (filed 2026-08-29 out of M0134-0168). Newly unmasked when the section started rendering at all: `foreign_key.sql`'s `\d fk_notpartitioned_pk` gains `fk_partitioned_fk_2`/`_3` rows and a second `…_fkey_1` where PG lists only the parent's constraint; surviving rows are also ordered differently. Resume: the describe query behind "Referenced by:" must filter `conparentid = 0` (`describe.c:2470`).
- [ ] **M0134-0168c — reg\* input errors report the cast position, not the literal's** (filed 2026-08-29 out of M0134-0168). PG puts `^` under the string literal, goopg under the `::`. Family-wide (regclass/regproc/regprocedure/regcollation), pre-existing. Resume: pass `x.Operand.Pos()` instead of `x.Pos()` in `internal/executor/expr.go`'s CastExpr arm, and A/B `regproc.sql` — the caret line is in every regress expected file, so this moves lines in both directions.
- [ ] **M0134-0168d — `regtype`/`regrole`/`regnamespace` string casts still fall through on a miss** (filed 2026-08-29 out of M0134-0168). Identical silent-acceptance bug: `'nosuch'::regtype`/`::regrole`/`::regnamespace` return the raw name; `'int4'::regtype` renders `int4` where PG gives `integer`; `'int4'::regtype::oid` raises 22P02 instead of 23. Resume: delegate those CastExpr `KindString` arms to `regIdentifierInput` too, PRESERVING the regtype arm's oidvector and numeric-string→name renderings ahead of it; `regnamespace` first needs a `regnamespacein` arm (`InMemory.SchemaOID`, regproc.c:1441, 3F000).
- [ ] **M0134-0168e — `pg_get_viewdef('nosuch'::regclass)` returns empty instead of raising** (filed 2026-08-29 out of M0134-0168). The compat intercept consumes its argument without evaluating the cast, so the new 42P01 never fires there. Resume: evaluate the argument through `evalExprSlot` before the name-based shortcut, or resolve it through `regIdentifierInput` inside the intercept.
- [ ] **M0134-0168f — the whole `to_reg*` builtin family is missing** (filed 2026-08-29 out of M0134-0168). `to_regclass`/`to_regtype`/`to_regproc`/`to_regprocedure`/`to_regoper`/`to_regoperator`/`to_regrole`/`to_regnamespace`/`to_regcollation` all error `function to_regX does not exist`. These are the `missing_ok=true` twins of the reg\*in casts (NULL instead of an error). Resume: `internal/executor/reg_identifier.go` already resolves six of them — add builtins that call `regIdentifierInput` and map its undefined-object error to NULL (regproc.c:1000-1120).
- [ ] **M0134-0169 — sqljson_jsontable.sql** — regress-sql, **sized live 2026-08-29 (`not-tried` → `failed`) and PARKED**. 1347 → **1335** diff lines / `^+ERROR` 116 → **115**. The `^+ERROR` histogram is 90 syntax errors plus a 25-line `relation/view "X" does not exist` cascade, and the syntax errors point at only four tokens — `COLUMNS` x68, `PASSING` x12, `AS` x9, `(` x1. The first three are the SQL/JSON `JSON_TABLE` construct: the same REFACTOR-tier missing subsystem that parked M0134-0168, ledgered as **0168a** (which also gates -0170). No second cause inside the case to slice off. The loop instead shipped the **engine-wide grammar bug** the lone `(` exposed: `CREATE TABLE t AS (SELECT 1)`, `CREATE VIEW v AS (SELECT 1)` and `CREATE MATERIALIZED VIEW mv AS (SELECT 1)` were rejected outright, though upstream's `CreateAsStmt` (`gram.y:4807`, `:4821`) and `ViewStmt` (`:11287`) all take `SelectStmt`, whose second alternative is `select_with_parens` — goopg routed all three through `select_bare`. It survived the parser migration because it was recorded as INTENTIONAL WITH THE WRONG JUSTIFICATION in three places at once (`pg_grammar.y:634`'s comment claimed *"CREATE VIEW's body and CTAS's source reject `AS (SELECT 1)`"*, `select_layering_test.go` pinned both under `assertBothReject`, and the goldens recorded both as `!syntax error`) — all three faithful records of the LEGACY hand parser's limit, promoted to a claim about PostgreSQL that nothing checked against `gram.y`. Fix is four productions `select_bare` → `SelectStmt` with **no new grammar conflicts** (still exactly 59). Design `docs/design/m0134-0169-ctas-view-source-parenthesised-query.md`; all ten forms verified live end-to-end; 15-case regress A/B vs a HEAD worktree: **11 byte-identical**, `privileges` 3885 → 3878, zero regressions. Guard `TestCtasAndViewSourceAcceptsParenthesisedQuery` (`internal/parser/select_layering_test.go`), revert-checked. **Re-arm trigger:** flip back to selectable once the SQL/JSON `JSON_TABLE` subsystem (0168a) lands. Follow-ups 0169a/0169b below.
- [ ] **M0134-0169a — `pg_get_viewdef` echoes a view's raw source text instead of re-deparsing it** (filed 2026-08-29 out of M0134-0169). Newly reachable now that `CREATE VIEW v AS (SELECT 1)` parses: `\sv` returns `(SELECT 1)` where PG prints ` SELECT 1 AS a;`. Pre-existing and general — goopg's `RawDef` already differs from PG's deparse in whitespace, casing and the trailing semicolon for EVERY view. Resume: `buildView` (`grammar/goopg_ext.y`) + the `pg_get_viewdef` path — deparse `CreateViewStmt.Query` the way `pg_get_viewdef_worker` (`ruleutils.c`) does; a real AST deparser is the prerequisite.
- [ ] **M0134-0169b — decide `copy_inner`'s `select_bare` against `gram.y`** (filed 2026-08-29 out of M0134-0169). The one remaining `select_bare` call site that was NOT changed, because this case did not exercise it and changing it on a guess would repeat the very mistake -0169 fixed. Resume: read `gram.y`'s `CopyStmt` / `PreparableStmt` (upstream spells it `COPY '(' PreparableStmt ')'`), then either widen `copy_inner` or record the narrowing in playbook §12.5 as deliberate, with the citation.
- [ ] **M0134-0170 — sqljson_queryfuncs.sql** — regress-sql `failed` (was `not-tried`; RUN 2026-08-29: **2021 diff lines / 259 `^+ERROR`**). **PARKED** — 100% of the diff is the SQL/JSON query-function family (`JSON_EXISTS`/`JSON_VALUE`/`JSON_QUERY` + `PASSING`/`RETURNING`/`ON ERROR`/`ON EMPTY`/`QUOTES`), the REFACTOR-tier subsystem ledgered as 0168a that also parks -0168 and -0169; **re-arm when that subsystem lands** (all three cases together). The loop shipped the engine-wide error class the case asserts but cannot reach: index-expression / partial-index-predicate IMMUTABILITY (42P17) plus the built-in half of the partition-key sibling — design `docs/design/m0134-0170-index-expression-mutability.md`, guard `internal/executor/index_mutability_test.go` (revert-checked). Remainder filed as ledger 0170a (mixed-volatility overloads excluded by name), 0170b (no 42883 for an unknown function name), 0170c (`pg_get_indexdef` leaks a Go pointer into a partial-index predicate deparse).
- [ ] **M0134-0171 — foreign_key.sql** — regress-sql `failed` (re-sized at HEAD 2026-08-29: **3490 → 3343 diff lines / `^+ERROR` 279 → 253**). **PARKED** — the residual is REFACTOR-tier: 113 cascaded `relation "X" does not exist` plus the partitioned-FK (`fkpart*`) matrix (multi-level partition FK routing, ATTACH/DETACH FK inheritance, inherited-constraint DROP rules); **re-arm when the partitioned-DML milestone lands**. The loop shipped the engine-wide **data-integrity** bug the case exposed: an FK written `REFERENCES <table>` with no referenced-column list must resolve to the referenced table's PRIMARY KEY (`transformFkeyGetPrimaryKey`, `tablecmds.c:13382`), but `pkColumns` returned the referenced table's FIRST column — under a doc comment that already described the index scan its body never performed. A multi-column PK yielded 1 of N columns, so the arity mismatch rejected **every valid row** with a bogus 23503; a single-column PK that is not column 1 silently enforced the FK against the **wrong column**; only PK-is-column-1 was accidentally right, which is exactly why the existing single-column FK tests never caught it. Design `docs/design/m0134-0171-fk-omitted-refcolumns-primary-key.md`, guard `TestFKOmittedRefColumnsResolveToPrimaryKey` (revert-checked, 3 of 4 subtests fail on the old body); 14-case regress A/B **13 byte-identical, zero regressions**. Remainder filed as ledger 0171a (resolve at DDL time so `pg_constraint.confkey` stops being `{}` — must go AFTER PK-index creation or self-referencing FKs break), 0171b (no 42704 `there is no primary key for referenced table`), 0171c (multi-column FK auto-naming uses only the first column), 0171d (`ON DELETE RESTRICT` reports the NO ACTION message).
- [ ] **M0134-0172 — stats_ext.sql** — regress-sql `failed` (was `not-tried`; RUN 2026-08-29: **3754 diff lines / 435 `^+ERROR`** → **3451 / 54** after this loop's fix). **PARKED** — the residual is REFACTOR-tier: goopg parses `CREATE STATISTICS` but validates almost nothing (~40 missing PG errors: column resolution, duplicate column/expression, >8 columns, unrecognized kind, <2 columns, system/virtual-generated-column rejection, no-default-btree-opclass, ACL/ownership), `ALTER STATISTICS` is a no-op, and **the planner applies NO extended statistics at all** (functional dependencies / ndistinct / MCV), which is the bulk of the remaining 3451 lines and is the case's entire point. Resume points in `.ralph/deferral_ledger.md` rows 0172a/0172b/0172c — port `statscmds.c CreateStatistics`'s checks first (self-contained, clears ~40 of the 50 `^-ERROR`). **Shipped instead (engine-wide PL/pgSQL correctness fix, design `docs/design/m0134-0172-plpgsql-query-stmt-frame-var-substitution.md`):** `RETURN QUERY <query>` and the static form of `FOR rec IN <query> LOOP` never called `substitutePlpgsqlFrameVarsInSQL`, so **no** PL/pgSQL variable was visible to either — not a declared local, not even a function parameter (`return query select v + n` raised 42703 on `v`). Two of the four SQL-string-capturing handlers substituted; these two did not. That accounted for 381 of the case's 435 errors via the opening `check_estimated_rows()` helper. Also fixed in the same helper: a subscript of a NULL or out-of-range array now renders `NULL` (PG `ExecEvalSubscriptingRef`) instead of leaking the bare `tmp[1]` text to the planner; and the routine's own OUT/`RETURNS TABLE` names are excluded from substitution (new `plpgsqlFrame.outParamNames`) so `select a from q2` is not rewritten to `select NULL from q2`. Guard `TestPlpgSQLQueryStmtsSubstituteFrameVars` (7 subtests, revert-checked 6/7). 14-case regress A/B: 13 byte-identical; `plpgsql`'s +10 lines traced to statement level on two fresh clusters = **exactly one statement changed**, `ret_query2(8)`, from 42703 to all 9 rows byte-identical to PG.
- [ ] **M0134-0173 — stats_import.sql** — regress-sql `failed` (was `not-tried`; RUN 2026-08-29: **1461 diff lines / 74 `^+ERROR`** -> **1457 / 73**). **PARKED** — ~100% of the residual is the PG 18 statistics-IMPORT function family (`pg_restore_relation_stats`, `pg_restore_attribute_stats`, `pg_clear_relation_stats`, `pg_clear_attribute_stats` = 55 of the 73 `^+ERROR`s) plus the absence of a queryable `pg_statistic` relation (5 more); resume points in `.ralph/deferral_ledger.md` rows 0173c/0173d. **Shipped instead (engine-wide silent wrong-answer fix, design `docs/design/m0134-0173-range-type-input-and-constructors.md`):** goopg treated every range-typed value as **opaque, unvalidated text** — `evalCast` had no arm for any range type, so `'garbage'::int4range` succeeded, `'[5,1)'::int4range` succeeded where PG raises 22000, and **no discrete range was ever canonicalized**, so `'[1,4]'::int4range` and `'[1,5)'::int4range` — the SAME value in PG — compared UNEQUAL. That is not a message gap: canonicalization to `[)` (`int4range_canonical` & friends) is exactly what makes equality, `ORDER BY`, btree probes and exclusion constraints agree, so a row inserted as `[1,4]` and probed with `[1,5)` silently did not match. Fourth instance of *missing evalCast arm = unvalidated text* (xid, circle, float8). The case's own error — `function int4range does not exist` — was the second half: goopg's `pg_proc` seed has carried all twelve `range_constructor2`/`range_constructor3` rows since the range-type catalog work, so **the catalog advertised a function the executor never implemented** (the M0134-0167 pattern again). New `internal/executor/rangetypes.go` ports `range_in`/`range_parse`/`range_serialize`/`make_range`/`range_deparse`/`range_bound_escape` + the three canonical procs; bounds go through the SUBTYPE's own I/O so `'[a,4)'::int4range` raises int4in's message byte-identically. User `AS RANGE` types share the input pipeline (never canonicalized, matching the `rngcanonical = 0` goopg writes). 43-statement oracle A/B: every value, message, DETAIL and HINT matches. 14-case regress A/B vs a HEAD worktree: **`rangetypes` 2543 -> 2166 lines / 234 -> 182 `^+ERROR`**, `multirangetypes` 4252 -> 4235, nine byte-identical, `create_index`'s delta a pre-existing nondeterministic pointer-address leak in `pg_get_indexdef`, and `plpgsql` +5 traced line by line to the polymorphic `f1(anyrange, ...)` block where `int4range(42,49)` used to abort the statement — no statement went from matching to diverging. Guard `TestRangeTypeInputAndConstructors` (8 subtests), revert-checked at BOTH wiring points. Remainder filed as ledger rows 0173a-0173d.
- [ ] **M0134-0174 — subscription.sql** — **PARKED 2026-08-29** (regress-sql `not-tried` → `failed`; 552 → **526** diff lines, `^+ERROR` 46 → **29**, `^-ERROR` 48 → **31**). Sized live for the first time. The shipped fix is **engine-wide, not subscription.sql-specific**: `execCreateSubscription` read exactly two keys out of the `WITH` map (`enabled`, `slot_name`) and dropped every other name, and `CreateSubscriptionStmt.Conninfo` went into the catalog row unread — there was **no validation stage of any kind**. `CONNECTION 'foo'`, `CONNECTION 'i_dont_exist=param'`, `WITH (connect = false, enabled = true)`, `WITH (not_an_option = 3)` and `PUBLICATION foo, testpub, foo` ALL SUCCEEDED, where PG raises five distinct errors. That is a correctness gap on its own — a typo'd conninfo or a misspelt option yields a subscription that looks created and can never replicate, with nothing anywhere to say so — and it **cascades** the same way M0134-0160's reloption gap did: the case reuses one subscription name across its negative cases, so **20 of the 46 divergences were a spurious `subscription already exists`** rather than the statement's own error (now 3, all downstream of the still-unimplemented permission checks). Third instance of *the surface accepts a name nobody looks for* after M0134-0160 and -0165a. Fixed with `internal/executor/subscription_options.go`, porting `parse_subscription_options` (`subscriptioncmds.c:124`, CREATE's `supported_opts` at `:560-567`), `defGetBoolean`/`defGetStreamingMode`/`ReplicationSlotValidateNameInternal`, `check_duplicates_in_publist` (`:2362`) and `walrcv_check_conninfo`'s `PQconninfoParse` half (`conninfo_parse`, `fe-connect.c:6290`, plus the full 50-name `PQconninfoOptions` keyword table). **Upstream's check ORDER is reproduced verbatim and is load-bearing**: options → duplicate-name → `slot_name` default → conninfo → duplicate publications, because a statement wrong in two ways must report the one PG reports, and because any check placed after the registry insert leaves the silently created subscription behind — which IS the cascade. Upstream's `specified_opts` (mirrored as `subOpts.specified`) is not bookkeeping: it is what makes the same clash yield `slot_name = NONE and enabled = true are mutually exclusive options` versus `subscription with slot_name = NONE must also set enabled = false`, and **four of the eight incompatibility messages are wrong without it**. Two behaviour bugs fell out of the same absence and are fixed too: `WITH (connect = false)` created an **enabled** subscription (upstream's post-pass overrides the defaults of `enabled`/`create_slot`/`copy_data`, `:390-392`), and an unspecified `slot_name` stayed empty instead of defaulting to the subscription name (`:632`); the duplicate-name error also stopped being the bare unquoted registry sentinel `catalog.ErrSubscriptionExists`. Design `docs/design/m0134-0174-create-subscription-validation.md`, indexed in `docs/design/README.md`. Guard `internal/executor/subscription_options_test.go` — every upstream message and SQLSTATE pinned (they are **not** uniform: 42601 options/conninfo, 22023 `origin`, 42602/42622 slot names, 42710 duplicate name/publication) plus the end-to-end cascade guard; **revert-checked at BOTH wiring points**. Gates: **8-case regress A/B vs a HEAD worktree** (`subscription`, `object_address`, `publication`, `psql`, `dependency`, `misc_functions`, `alter_generic`, `event_trigger`) — **seven byte-identical**, `subscription` 552 → 526; `alter_generic` moved 843 → 841 on one run and an **A/A on the unchanged patched tree reproduced 843 byte-identically**, so that case is nondeterministic (`catalog update: freshly extended page did not accept tuple`), pre-existing at HEAD and unrelated. `go test ./internal/executor/ ./internal/catalog/ ./internal/parser/ ./internal/replication/` PASS; `RALPH_PRECOMMIT_SCOPE=units` PASS; `scripts/tpch-spotcheck.sh` PASS; `make regen-testport` / `make check-testport-inventory` PASS. **PARKED** because the residual is three independent causes, each larger than a contained fix: 0174a (`pg_subscription` column width — 19 of the 29 remaining `+ERROR`s), 0174b (ALTER SUBSCRIPTION is a no-op), 0174c (no permission or transaction-block checks). **Re-arm trigger:** select again once 0174a lands, which alone clears two-thirds of the remaining `^+ERROR`s.
- [ ] **M0134-0174a — **`pg_subscription` carries none of the PG-18 columns `\dRs+` selects** (filed 2026-08-29 out of M0134-0174). `subskiplsn`, `subbinary`, `substream`, `subtwophasestate`, `subdisableonerr`, `suborigin`, `subpasswordrequired`, `subrunasowner`, `subfailover` and `subsynccommit` do not exist, so EVERY `\dRs+` in the case errors `column "subskiplsn" does not exist` — **19 of the 29 remaining `^+ERROR`s**, the single largest residual bucket, and the reason no subscription-introspection output can be compared at all. NOT a projection fix: `catalog.Subscription` (`internal/catalog/pubsub.go`) has no backing fields, so this is a catalog-widening change. Resume: add the fields per `postgres/src/include/catalog/pg_subscription.h`, carry them through `CreateSubscriptionAsOwner` and `syncSubscriptionRow`, then widen the pg_subscription row builder to `describeSubscriptions`' column set (`postgres/src/bin/psql/describe.c`). Pairs with the validated-but-discarded options (same ledger date).
- [ ] **M0134-0174b — **`ALTER SUBSCRIPTION` is a `CompatNoopStmt` — it neither acts nor validates** (filed 2026-08-29 out of M0134-0174). Every form except `OWNER TO` drains to the statement end and silently returns the `ALTER SUBSCRIPTION` tag (`internal/parser/ddl.go:8660`), so `SET (...)`, `SKIP (lsn = ...)`, `CONNECTION`, `ENABLE`/`DISABLE`, `REFRESH PUBLICATION` and `ADD`/`DROP PUBLICATION` all do nothing. Accounts for most of the 31 remaining `^-ERROR`s. This is also **why M0134-0174's validators are not shared with an ALTER path: there is none**, so the sibling-path rule has no second twin here — but it acquires one the moment this lands. Resume: model `AlterSubscriptionStmt` in the parser (read `docs/design/not_ralph/06-goyacc-parser-playbook.md` §12 first), then `execAlterSubscription` per `AlterSubscription` (`subscriptioncmds.c:1105`); `parseSubscriptionOptions` needs only a supported-set parameter, since the ALTER-only `refresh`/`lsn` names are already excluded from `createSubscriptionSupportedOpts`. REFACTOR-tier (new statement node + nine sub-forms), which is why -0174 is parked rather than closed.
- [ ] **M0134-0174c — **CREATE SUBSCRIPTION runs no permission or transaction-block checks** (filed 2026-08-29 out of M0134-0174). Missing: `has_privs_of_role(owner, ROLE_PG_CREATE_SUBSCRIPTION)` → `permission denied to create subscription` + its DETAIL, the database `CREATE` ACL → `permission denied for database ...`, `password_required=false is superuser-only` + HINT, `walrcv_check_conninfo`'s `must_use_password` half → `password is required`, and `PreventInTransactionBlock` for `WITH (create_slot = true)`. **All 3 surviving spurious `subscription already exists` errors sit downstream of these.** Resume: `internal/executor/operators_ddl.go` `execCreateSubscription`, between `parseSubscriptionOptions` and the duplicate-name check, mirroring `subscriptioncmds.c:576-609` — `HasPrivsOfRole` already exists from M0134-0162/-0163, but `pg_create_subscription` must become a real grantable predefined role first and `isTopLevel` must be threaded to the DDL operator.
- [ ] **M0134-0174d — **The subscription `WITH` clause is a map, so PG's DefElem list semantics are lost** (filed 2026-08-29 out of M0134-0174 — the exact twin of M0134-0160a). `CreateSubscriptionStmt.With` is `map[string]string`, so a duplicate option cannot raise `errorConflictingDefElem`, with two bad names goopg reports the lexicographically-first where PG reports the source-order-first, and `binary = 0` is indistinguishable from `binary = '0'` (upstream accepts only the integer). Also filed here: a bare `WITH (create_slot)` with no `= value` is a parse error in `parsePubSubWithList`, where PG's `def_arg` reads a valueless option as true — one of the case's 3 remaining `^+ERROR: syntax error` lines. Resume: add an ordered name list plus a value-present flag alongside the map in `internal/parser/ddl.go` `parsePubSubWithList` (shared by CREATE PUBLICATION and CREATE SUBSCRIPTION), then walk it instead of the sorted keys in `parseSubscriptionOptions`. Read the goyacc playbook §12 first.
- [ ] **M0134-0174e — **Eight subscription options are validated and then discarded** (filed 2026-08-29 out of M0134-0174 — the M0134-0160b shape). `binary`, `streaming`, `two_phase`, `disable_on_error`, `password_required`, `run_as_owner`, `failover` and `origin` now have their values checked and are then thrown away, because `catalog.Subscription` has no fields for them; `synchronous_commit` is additionally accepted UNVALIDATED, where upstream runs it through `set_config_option` in `PGC_S_TEST` mode (`subscriptioncmds.c:229-231`). Also filed here: the URI conninfo form is accepted unvalidated (`parse_connection_string` dispatches `postgres://` to `conninfo_uri_parse`, unported), and the new `validateReplicationSlotName` is **not** shared with the slot registry — the walsender `CREATE_REPLICATION_SLOT` path and `pg_create_*_replication_slot` still accept any name (that sharing needs the helper moved to a leaf package to avoid an import cycle). Resume: fields on `internal/catalog/pubsub.go` `Subscription`, carried through `CreateSubscriptionAsOwner` + `syncSubscriptionRow`; only useful together with 0174a.
- [ ] **M0134-0175 — tablesample.sql** — **PARKED 2026-08-29** (regress-sql `not-tried` → `failed`; 402 → **304** diff lines, `^+ERROR` 46 → **6**, `^-ERROR` 10 → **3**). Sized live for the first time. **TABLESAMPLE did not exist at all** — the keyword was in `grammar/kwlists_gen.y:425` with a token number and the correct `type_func_name_keyword` category, but no production consumed it (`internal/parser/keyword_reachability_test.go` carried it on the `notYetPortedKeywords` allowlist, and that test flagged its own stale entry the moment the rule landed). The loop shipped the **whole feature**: grammar (`tablesample_clause` / `opt_repeatable_clause`, gram.y:14001, **zero new conflicts** — still the pinned 59), `RangeVar.TableSample`, `optimizer.TableSampleSpec` on `SeqScan` resolved above the inheritance/partition expansion so every Append leaf becomes a Sample Scan, the SYSTEM and BERNOULLI samplers, all four validation errors, and EXPLAIN's `Sample Scan` label + `Sampling:` line. Design `docs/design/m0134-0175-tablesample.md`. **The discovery that made an exact port possible:** PG's two built-in methods are **deterministic hash functions, not PRNG streams** — `bernoulli` hashes `{blockno, offset, seed}`, `system` hashes `{blockno, seed}`, both with `hash_any` against a `rint((PG_UINT32_MAX+1)*pct/100)` cutoff held in a uint64 so 100% is representable, seeded by `hashfloat8(REPEATABLE)` whose zero short-circuit is exactly why `REPEATABLE (0)` is machine-independent. goopg already had `hash_any` as `pgHashBytesExtended` (`hash_partition.go`), so the port needed **no new primitive** and is exact: the guard pins all three sampled row sets from `expected/tablesample.out` (3,4,5,6,7,8 / 4,5,6,7,8 / 7) and they match byte-for-byte. Three structural decisions, each with its reason in the design doc: **one scan node not two** (`SeqScan` switches on `TableSample != nil`, so sampling composes with visibility/SSI/ring-buffers/parallel block claiming instead of duplicating them); the sampler runs **before** visibility checking (upstream hashes a line-pointer offset whether or not it holds a live tuple, so a dead tuple consumes its slot rather than shifting later tuples into it); and the sampler is **stateless** (the block cursor is an argument), so a LATERAL rescan is correct for free. Check ORDER is load-bearing — upstream resolves the method in the parser and the percentage in the executor, so `TABLESAMPLE FOOBAR (-1)` reports the unknown method; revert-checked. **All four error messages + the derived-table syntax rejection are now byte-identical to the oracle.** CSV row flipped `not-tried` → `failed` (`pass_required=no`), `make regen-testport` run. Golden-corpus review artifact (playbook §12): 450 lines changed and stripping `,TableSample=∅` reproduces the previous file **byte-for-byte**, proving the grammar change altered no other pinned AST. **Re-arm trigger:** land M0134-0175a (fillfactor at insert) — bucket A alone governs every remaining row-set divergence and every cursor FETCH — then re-run `scripts/pg-regress-runner.sh --verbose tablesample`. Remaining buckets are 0175a-d below plus a pre-existing inheritance-child EXPLAIN alias gap (`person person_1`, 4 lines) and the already-filed M0134-0169a `pg_get_viewdef` deparse gap (~6 lines). Ledger: 4 rows 2026-08-29. **Re-arm FIRED 2026-08-29**: M0134-0175a landed and the case was re-run — **304 → 214** diff lines, `^+ERROR` 6 and `^-ERROR` 3 unchanged, and the first 63 lines (every sampled `SELECT`) now match the oracle byte-for-byte. Still PARKED. Remaining buckets, all filed: M0134-0175b (5 of the 6 `^+ERROR`s), -0175c (all 3 `^-ERROR`s), -0175d (the 6th `^+ERROR`), M0134-0169a `pg_get_viewdef` deparse (~10 lines, both `\d+` views), the pre-existing inheritance-child EXPLAIN alias gap (`person person_1`, 4 lines), and the NEW M0134-0175e scroll-cursor rewind bug (~30 lines) that 0175a uncovered.
- [ ] **M0134-0175b — a LATERAL outer column cannot be used as a sample argument** (filed 2026-08-29 out of M0134-0175; **5 of the 6 remaining `^+ERROR`s**). `lateral (select count(*) from tenk1 tablesample bernoulli (pct)) ss` raises `column "pct" does not exist`; PG plans it as `Sampling: bernoulli ("*VALUES*".column1)` and re-evaluates per rescan, giving `pct=0 -> 0` / `pct=100 -> 10000`. Resume: `internal/optimizer/planner.go` `resolveTableSample` resolves against the scan-local `ctx` only — thread `planScanRangeVar`'s `lateralCtx` in. The executor half should need no change: `seqScanOp.Open` already rebuilds the sampler from the spec, and the sampler is stateless, so rescans re-evaluate correctly once resolution succeeds.
- [ ] **M0134-0175c — TABLESAMPLE on a view or CTE is silently honoured instead of raising 42809** (filed 2026-08-29 out of M0134-0175, ~25 lines). PG raises `TABLESAMPLE clause can only be applied to tables and materialized views` when the relkind is not r/m/p (`parse_clause.c:1140`); goopg samples the inlined view body or CTE, so `SELECT id FROM test_tablesample_v1 TABLESAMPLE BERNOULLI (1)` returns rows and the `WITH query_select AS (...)` form returns the whole table. `INSERT INTO <view containing TABLESAMPLE>` must likewise raise `cannot insert into view` with the `Views containing TABLESAMPLE are not automatically updatable.` DETAIL and its HINT. Resume: gate the CTE-substitution and view-inlining branches of `planScanRangeVar` on `rv.TableSample == nil`, stamping the error position from `TableSampleSpec.Pos()` so the caret lands on the relation name. **Trap:** the gate has to fire at three separate return points that each build a different node shape; a miss silently disables sampling rather than rejecting.
- [ ] **M0134-0175d — sample arguments are not coerced to float4, and bool→int has no cast arm** (filed 2026-08-29 out of M0134-0175). Upstream coerces each argument to the method's declared `parameterTypes` (FLOAT4OID) during parse analysis, so EXPLAIN reads the coerced Const straight off the plan; goopg does not coerce, so `internal/executor/operators_explain.go`'s `constSampleValue` synthesises the `'20'::real` / `'2'::double precision` deparse at print time and falls back to the ordinary expression printer outside {integer, numeric, + - * /}. Separately and engine-wide, `SELECT count(*) FROM test_tablesample TABLESAMPLE bernoulli (('1'::text < '0'::text)::int)` raises `cannot cast to integer` — a **missing bool→int `evalCast` arm**, the 4th instance of the missing-cast-arm pattern (see the xid/circle/float8 memory). Resume: coerce in `resolveTableSample` so the deparse falls out of a typed Const; add the bool→int arm in `internal/executor/expr.go`'s CastExpr handling.
- [ ] **M0134-0175e — a second `FETCH FIRST` on an already-scrolled SCROLL CURSOR returns 0 rows** (filed 2026-08-29 out of M0134-0175a; ~30 lines of `tablesample.sql`). `DECLARE tablesample_cur SCROLL CURSOR FOR SELECT id FROM test_tablesample TABLESAMPLE SYSTEM (50) REPEATABLE (0)` fetches correctly on its first pass — that half now matches the oracle byte-for-byte once M0134-0175a fixed the page layout — but a subsequent `FETCH FIRST` returns `(0 rows)`, as does every `FETCH NEXT` after it, where PG restarts the scan and returns 3, 4, 5, …. **Not sampling-specific**: the first pass is byte-correct, so this is a rewind/restart gap in the scroll-cursor machinery, and it was masked before 0175a because with all ten rows on one block the sampled cursor was already wrong on its first pass. Resume: find the `FETCH FIRST` / `FETCH ABSOLUTE 1` handler for a SCROLL cursor in `internal/executor` and check whether it re-Opens (rescans) the underlying plan or merely resets a row index that the exhausted operator no longer honours; compare against PG's `PortalRunFetch` / `DoPortalRewind` (`postgres/src/backend/tcop/pquery.c:1400`). Likely affects every SCROLL cursor, not just sampled ones — worth a non-sampling fixture first.
- [ ] **M0134-0176 — tablespace.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-08-29** (854 → **811** diff lines, `^+ERROR` 32 → **25**, `^-ERROR` 27 → **24**). Sized live for the first time. The shipped fix is **engine-wide, not tablespace.sql-specific**, and is the exact M0134-0160 shape one level up: `pg_tablespace.spcoptions` was hardcoded NULL in all three row builders and `CREATE TABLESPACE ... WITH (...)` was parsed into a **raw token dump no consumer ever read**, so `WITH (some_nonexistent_parameter = true)` SUCCEEDED where PG raises 22023 — *declared but unconsumed* a fourth time — and the tablespace it wrongly created then turned the next valid `CREATE` of that name into a spurious "already exists". Separately, **`ALTER TABLESPACE` did not parse at all**: all four forms fell through every arm of the hand-written `parseAlter` to its closing `expectKeyword(KwTable)` and surfaced as ``syntax error at or near "expected keyword table (got tablespace)"``. Fixed by extending the M0134-0160 registry with `RELOPT_KIND_TABLESPACE` + its four upstream names (`internal/executor/reloptions_catalog.go`) rather than writing a bespoke checker — upstream funnels tablespaces through the same `transformRelOptions`/`tablespace_reloptions` pair as every other kind — plus `tablespaceOptionArray` (the merge+validate port, `internal/executor/tablespace_options.go`), a structured `[]TablespaceOption` on the AST, `parseTablespaceOptionList`/`parseAlterTablespaceTail`, four catalog mutators and `spcoptions` rendering in `tablespaceVirtualRows`. **No grammar change was needed** — tablespace DDL is one of the classes the goyacc port deliberately does not carry (playbook §12), so the new `ALTER` sits beside its existing hand-written `CREATE` sibling. Three upstream properties are load-bearing, each mirrored and each **verified against a live PG 18.3 oracle**: validation runs on the **merged** array so `RESET (bogus_never_set)` SUCCEEDS (a name is only checked on the way *in*); `RESET (name = value)` is a **42601 syntax** error raised downstream because "the grammar doesn't enforce it" (`reloptions.c:1228-1243`), expressible only because the clause is now an ordered list with a `HasValue` bit rather than a map (contrast M0134-0160a); and emptying the array returns spcoptions to **SQL NULL, not `{}`**. Merge ORDER is upstream's too — survivors first, replacements appended, so `SET` on an existing option moves it to the end. Design `docs/design/m0134-0176-tablespace-storage-parameters.md`, indexed in `docs/design/README.md`. Guards: `internal/executor/tablespace_options_test.go` (unknown-option rejection **plus the no-relation-left-behind cascade guard**, kind separation via `fillfactor`, the full oracle-verified SET/RESET sequence, merge order, RENAME OID-preservation, all four error SQLSTATEs), `internal/catalog/tablespace_test.go` (spcoptions rendering + NULL-not-`{}` + array quoting), `internal/parser/create_tablespace_test.go` (option structure + all four ALTER forms). One existing test pinned the bug shape (`TestParseCreateTablespace` asserted the 3-token raw dump) and was corrected. Gates: `go build ./...` PASS; `go test` on `internal/{executor,parser,catalog,optimizer,postmaster}` PASS; `scripts/pg-regress-runner.sh tablespace` (live before/after); live A/B against a throwaway PG 18.3 oracle for every semantic above. Remaining buckets are ledgered and independent: no option **value typing** (`SET (seq_page_cost)` stores `=true` where PG rejects the type — same gap M0134-0160 recorded for relations); no owner/permission checks and `spcowner` still hardcoded 10; spcoptions are registry-only (the heap row still writes NULL, so they are lost on restart); and `REINDEX (..., CONCURRENTLY)` / `PRIMARY KEY USING INDEX TABLESPACE` / `CREATE INDEX CONCURRENTLY` are separate unparsed forms. **Re-arm trigger:** select again once `ALTER ... ALL IN TABLESPACE` (M0134-0176a) lands — it alone clears 3 `^+ERROR`s and un-breaks the case's final `DROP TABLESPACE`.
- [ ] **M0134-0176a — `ALTER {TABLE|INDEX|MATERIALIZED VIEW} ALL IN TABLESPACE` is unparsed** (filed 2026-08-29 out of M0134-0176). `ALTER TABLE ALL IN TABLESPACE ts SET TABLESPACE pg_default` raises `syntax error at or near "expected identifier (got all)"` (3 errors in `tablespace.sql`). Because the relations are therefore never moved off the tablespace, M0134-0176's now-working `RENAME` **unmasked** a downstream cascade: the case's final `DROP TABLESPACE regress_tblspace_renamed` reports `tablespace "..." is not empty` where PG succeeds. Upstream is `gram.y`'s `AlterTblSpcStmt` → `ATExecSetTableSpaceAll` (`tablecmds.c`), including the `NOTICE: no matching relations in tablespace "x" found` for an empty match and the optional `OWNED BY role` filter. Resume: add an `ALL IN TABLESPACE` arm to `parseAlter` before the `expectKeyword(KwTable)` fall-through (`internal/parser/ddl.go`, the same place M0134-0176 added its ALTER TABLESPACE arm), then a bulk-relocate executor reusing `execAlterTableSetTablespace`'s per-relation physical move. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0176.
- [ ] **M0134-0176b — `pg_tablespace_location()` is catalogued but has no handler** (filed 2026-08-29 out of M0134-0176). *Declared but unconsumed*, again: `pg_proc` carries OID 3778 (`internal/initdb/pg_proc_seed_data.go:2542`) and the name is listed in `internal/executor/pg_nonimmutable_builtins.go:289`, but no executor dispatch exists, so `tablespace.sql`'s very first `SELECT` returns an EMPTY row instead of `pg_tblspc/NNN` — a silent wrong value, not an error. `pg_tablespace_databases` (OID 2556) is in the same state. Upstream returns `''` for pg_default/pg_global, `pg_tblspc/<oid>` for an in-place tablespace and the symlink target otherwise (`pg_tablespace_location`, `postgres/src/backend/commands/tablespace.c:1546`). Resume: the `location` field already exists on `catalog.tablespaceRow`, but an in-place tablespace stores `""` there, so the `pg_tblspc/<oid>` form must be synthesised from the OID. Ledger row `.ralph/deferral_ledger.md` 2026-08-29 M0134-0176.
- [ ] **M0134-0178 — tsdicts.sql** — regress-sql `failed` (was `not-tried`; RUN 2026-08-29: **899 diff lines / 100 `^+ERROR`**). **PARKED** — 94% of the diff is one REFACTOR-tier subsystem goopg does not have at all: the text-search DICTIONARY engine (`ts_lexize` 71 errors, `to_tsvector`/`to_tsquery`/`phraseto_tsquery` 21, snowball `english_stem`, ispell affix-file validation). goopg models text search as a **pg_dump round trip only** — catalog rows, no tokenizer or lexizer — so closing it means porting `postgres/src/backend/tsearch/` (`spell.c` ~1700 lines, `dict_ispell/synonym/thesaurus.c`, `regis.c`, the generated stemmers, `share/tsearch_data/`); ledgered **0178a**, and it parks -0179 (`tsearch.sql`) and -0180 (`tsrf.sql`) too — **re-arm all three together**. The loop shipped the one contained cause, which is fully independent of every dictionary: `ALTER TEXT SEARCH CONFIGURATION`'s mapping forms never implemented `getTokenTypes` (`tsearchcmds.c:1229`) — no deduplication of the `FOR tok [, ...]` list and **no validation of a token name against the configuration's parser**, so `ADD MAPPING FOR not_a_token` silently wrote a `pg_ts_config_map` row with `maptokentype = -1` (a mapping no parser can match, which pg_dump would re-emit un-restorably) while duplicated tokens made statements collide with themselves. Design `docs/design/m0134-0178-tsconfig-token-type-validation.md`, guard `internal/executor/tsconfig_token_type_validation_test.go` (revert-checked). Quoted mixed-case token names filed as ledger 0178b. **899 → 863 lines, `^+ERROR` 100 → 97, `^-ERROR` 8 → 5.**
- [ ] **M0134-0179 — tsearch.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-08-29** (sized live for the first time: **3750 diff lines / 379 `^+ERROR`**, unchanged by this loop's fix — see "the trap" below). **319 of the 379 errors (84%) are the absent text-search engine** — `to_tsquery` 70, `websearch_to_tsquery` 67, the `@@` match operator 163, plus `ts_rewrite`/`to_tsvector`/`ts_headline`/`ts_lexize`/`ts_debug`/`ts_parse` — i.e. **the same blocker as M0134-0178**, exactly as the previous loop's baton predicted. Re-arm with ledger row **0178a** (port `postgres/src/backend/tsearch/`). Note `-0180` (`tsrf.sql`) is **set-returning functions and is NOT blocked by this** — size it normally. **Shipped instead (engine-wide scanner fix, design `docs/design/m0134-0179-operator-maximal-munch-lexing.md`):** goopg recognised multi-character operators from a **hand-maintained allowlist** of 15 two-char spellings and emitted everything else **one character at a time**. PG matches `{op_chars}+` greedily (`scan.l:886`) over an **open** alphabet — which is what makes `CREATE OPERATOR` possible at all — then trims at an embedded `/*`/`--`, strips a trailing `+`/`-` unless an earlier char is one of ``~!@#^&|`?%`` (so `a=-1` is two tokens but `a@>-1` is one), and errors at NAMEDATALEN. The failure was **structural, not cosmetic**: `a @@ 'x'` split into two `@` tokens and reduced as a **prefix** `@` over the literal (hence `operator does not exist: @` out of `prefixOp` — goopg was building a *different tree*, not mis-naming an infix), and `a @@ any (...)` was a **syntax error at "any"** because the quantifier rules take exactly one `subq_op`. `?` and `` ` ``, both legal op_chars, never reached the operator case and died as `unexpected character`, asserting the character is illegal in SQL. The `{self}` and `<= >= <> != =>` remappings `scan.l` does inline were **already correct** in `adapter.go`, so single-char behaviour is bit-for-bit unchanged. Result: **79 bogus lex errors → 0** (jsonb+geometry), 163 mis-named `@` → correctly-named `@@` (tsearch), **zero golden-corpus diff** (1.5 MB `parity_goldens.txt` — the playbook's required review artifact), 18 of 20 regress cases byte-identical. **The trap worth carrying:** raw diff lines grew (jsonb 6387→6517, geometry 5646→5674) while fidelity *improved* — the delta is exactly 65×2 and 14×2, one-line lex errors becoming PG's three-line `ERROR`/`LINE`/caret shape. **`^-` lines (output goopg failed to produce) is the regression metric and it did not move** (2529/4748 both sides). Guard `internal/parser/operator_maximal_munch_test.go`, revert-checked **40 failures** at HEAD. Two follow-ups ledgered: the closed 36-entry `OpCode` enum has no `pg_operator` lookup (so `@@` now fails as `unsupported operator "@@"` rather than PG's 42883 — same blocker as M0134-0158), and `*`-initial runs still split.
- [ ] **M0134-0180 — tsrf.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-09-01** (sized live: 793 → 785 diff lines, `^-` 235 → 232, `^+ERROR` unchanged at 10; unlike most M0134 cases this is ~10 largely independent PG SRF-placement rules, not one gap with a long error tail, and several already matched byte-for-byte with zero code change: sibling-SRF lockstep zipping, SRFs computed after aggregation when not GROUP-BY-referenced, DISTINCT ON placement, GROUP BY CUBE + SRF). **Shipped:** PG's `transformLimitClause` runs SRF-placement checking (`parse_func.c:2500-2680`) before the numeric-type coercion, so `SELECT 1 LIMIT generate_series(1,3)` must raise `set-returning functions are not allowed in LIMIT` even though `generate_series` is int8-typed; goopg had no such check and silently evaluated the SRF via the executor's `generate_series`-as-scalar fallback (reduces to the first arg), returning a wrong single-row result instead of erroring. New `exprHasSRF` walker in `internal/parser/analyzer/analyzer.go` (same node coverage as the existing `exprHasWindowFunc`; builtin-SRF name set + catalog `ReturnsSet` lookup — cannot import executor's sibling `isBuiltinSRF`, layering runs the other way) wired into the LIMIT/OFFSET analyzer block. Guard `TestAnalyzeSRFInLimitOffsetRejected` + `TestAnalyzeLimitOffsetNonSRFStillAccepted` (`internal/parser/analyzer/analyzer_test.go`). Design `docs/design/0100-0149/m0134-0180-tsrf-sizing.md`. **PARKED** — three REFACTOR-tier gaps remain, ledgered: **0180a** six more "not allowed in X" contexts (CASE/COALESCE/aggregate-arg/window-arg/UPDATE-SET/RETURNING/VALUES — aggregate/window-arg has zero existing precedent); **0180b** nested-SRF-as-another-SRF's-argument (`generate_series(1, generate_series(1,3))` returns 1 row not 6) and GROUP-BY-referencing-a-target-list-SRF (changes pre-aggregation cardinality) both need a recursive/stacked `ProjectSet` model, not a patch to the flat "zip to maxLen" one; **0180c** `|@|` as a user-defined prefix operator over `unnest` fails at the parser's closed `{-,+,NOT,~}` prefix set, unrelated to SRFs (same class as the M0134-0179 closed-`OpCode`-enum follow-up). A fourth item (correlated `LIMIT ... OFFSET <outer column>` inside a scalar subquery) is out of scope, own resume point, not ledgered as a numbered follow-up.
- [ ] **M0134-0181 — tstypes.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-09-01** (sized live: 1839 diff lines, 159 `^+ERROR`, 4 `^-ERROR`). **No contained fix landed** — unlike `tsrf.sql` (M0134-0180, ~10 independent placement rules) this file is dominated by ONE absent subsystem with no narrow slice inside it: the `tsvector`/`tsquery` core **type kernel** (parse + canonical output + compare), narrower than the already-parked M0134-0178/0179 dictionary/stemmer gap — most assertions build `tsvector`/`tsquery` directly from literals (`'a:1 b:2'::tsvector`), never calling `to_tsvector`, so no tokenizer/stemmer/`pg_ts_config` lookup is needed, only the type itself. Code search confirmed there is no `tsvectorin`-equivalent anywhere in goopg (only catalog OID/name-formatting plumbing in `internal/executor/expr.go`/`internal/catalog/codec.go`) — the apparently-working `::tsvector` cast is an opaque-type text-passthrough fallback, not real parsing; calling `tsvectorout(...)` explicitly errors `function tsvectorout does not exist`, proving no real function backs the type. Buckets: `@@` match operator `unsupported operator "@@"` (~89 occurrences, the largest), `ts_rank`/`ts_rank_cd` uncalled (29), `ts_delete` uncalled (12), `setweight` uncalled (6), `<->` phrase-distance operator `unsupported` (4), `array_to_tsvector`/`tsvector_to_array`/`strip`/`ts_filter`/`numnode`/`tsquery_phrase` uncalled (16 combined), plus 2 unrelated pre-existing `box`-type geometry errors caught in the same diff window (out of scope here). No narrow slice was found — even the most contained-looking item (`tsvectorout` quoting) can't be fixed in isolation, since the "cast" that looks like it works has no real parser behind it to format the output of. Filed **M0136** (own new milestone, below) rather than folding into ledger row 0178a — this gap is strictly narrower and more foundational than the dictionary/stemmer engine (needs none of its machinery) though 0178a's `to_tsvector` result value will eventually need M0136's canonical-output/compare machinery too. Design `docs/design/0100-0149/m0134-0181-tstypes-sizing.md`. Re-arm trigger: M0136-S1 landing (re-measure `tstypes` live).
- [ ] **M0134-0182 — type_sanity.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-09-01** (sized live: 1151 diff lines / 9 `^+ERROR`, unchanged in count before/after — one error swapped for another, see below). **Shipped:** PG's RESERV date/time input keywords (`now`/`today`/`tomorrow`/`yesterday`/`epoch` — `DecodeDateTime`/`DecodeTimeOnly`, `datetime.c`'s `datetbl`) were entirely unimplemented for `date`/`time`/`timetz`/`timestamp`/`timestamptz` literal input (only `infinity`/`-infinity` worked), so `'today'::date` inside the file's `CREATE TABLE tab_core_types AS SELECT ...` raised 22007, cascading into a second `relation does not exist` error at the tail of the file — 2 of the 9 `^+ERROR`s from one root cause. New shared `parseDateSpecialLiteral`/`parseTimestampSpecialLiteral`/`parseTimeSpecialLiteral`/`parseTimeTZSpecialLiteral` (`internal/executor/copy_text.go`) resolve against a new `nowFromCtx(ctx)` helper (mirrors `timeZoneFromCtx`), threaded through all 10 literal/cast/`pg_input_is_valid`/row-encode call sites; `today`/`tomorrow`/`yesterday` deliberately mirror `current_date`'s existing UTC-only midnight simplification. New test `internal/executor/date_time_reserv_literal_test.go` (`TestDateTimeReservedLiteral`, 20 self-consistent cases). Fixing it unmasked a DIFFERENT, previously-hidden bug in the same CTAS statement — `'1 2'::int2vector` materializes fine standalone but the CTAS row-encode path errors `expected bytes for int2vector, got kind 3` — net error count unchanged (serially-masked-cause shape, same pattern as M0134-0014/-0025/-0026). **PARKED** — dominant remaining bucket (REFACTOR-tier, own milestone): `pg_proc`/`regproc` expose only ~32 hand-curated builtins at runtime (`catalog.builtinProcsByName`) despite a full 3397-row `pg_proc.dat` mirror (`internal/initdb.pgProcInitialEntries()`) written to the on-disk heap at initdb time — confirmed live (`base/*/1255` is 778 KB but `SELECT count(*) FROM pg_proc` returns 32; `int4pl`/`array_in`/every other PG builtin misses `'name'::regproc` despite being used constantly by the evaluator itself) — a write-only heap the live query engine and `regproc` cast fallback never read, blocking `array_subscript_handler`/`array_in`/`array_recv`/`range_typanalyze`/`array_typanalyze`/`raw_array_subscript_handler` (7 of the 9 `^+ERROR`s) and very likely much wider "function does not exist" noise elsewhere. Other buckets ledgered (`.ralph/deferral_ledger.md` 2026-09-01, three rows): `int2vector` CTAS materialization; missing `pg_range` rows for the 6 built-in range types; `pg_class.relam` sign-flip (TOAST tables read 0, views read nonzero — backwards); `pg_attribute.attnum > pg_class.relnatts` for index relations; a ~500-row attribute type-metadata (`attlen`/`attalign`/`attbyval`) mismatch bucket, almost entirely `information_schema` view columns. Design `docs/design/0100-0149/m0134-0182-type-sanity-sizing.md`. Re-arm trigger: pg_proc-exposure fix or int2vector-CTAS fix landing (either should move the `^+ERROR` count for the first time).
- [ ] **M0134-0183 — typed_table.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-09-01** (sized live: 150 → 135 diff lines). **Shipped:** PG's five typed-table `ALTER TABLE` restrictions (ADD COLUMN/DROP COLUMN/RENAME COLUMN/ALTER COLUMN TYPE/INHERIT all refused with 42809 `cannot ... typed table` when `tbl.OfTypeOID != 0`, `tablecmds.c` `ATPrepAddColumn:7200`/`ATPrepDropColumn:9260`/`renameatt_check:3798`/`ATPrepAlterColumnType:14395`/`ATPrepAddInherit:17237`) were entirely unchecked — e.g. `DROP COLUMN` silently succeeded on a typed table, so a later `ALTER COLUMN TYPE` on the same (now-missing) column then reported the wrong error, a second symptom of the same root cause. Each check placed as the FIRST line of its `internal/executor/operators_ddl.go` handler to mirror PG's prep-pass-before-exec-pass ordering. New test `internal/executor/alter_table_typed_table_restrictions_test.go` (`TestAlterTableTypedTableRestrictions`, 5 exact SQLSTATE/message pins + a plain-table regression guard). **PARKED** — dominant remaining bucket (REFACTOR-tier): `DROP TYPE` has no dependency tracking against tables/functions referencing the composite type (RESTRICT silently succeeds instead of refusing; CASCADE doesn't cascade-drop) — desyncs most of the file's back half. Other buckets ledgered (`.ralph/deferral_ledger.md`, 2026-09-01, three rows): `SELECT * FROM <SETOF-composite function>` doesn't star-expand composite columns; text-column `''` default missing `::text` cast decoration in `\d`; duplicate `WITH OPTIONS` on one column not detected; `$1.name` composite-field dot-access is a parser gap. Design `docs/design/0100-0149/m0134-0183-typed-table-sizing.md`. Re-arm trigger: `DROP TYPE` dependency-tracking fix landing.
- [ ] **M0134-0186 — without_overlaps.sql** — **PARKED 2026-09-01** (status was `not-tried`; RUN for the first time, resolved to genuinely FAILING, CSV row flipped to `failed`, `pass_required=no`). Sized live: 0/1 PASS, 3572-line diff, 445 `^+ERROR`, 0% parity. **Unreachable:** the whole file exercises PG's SQL:2011 temporal `PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY` support (`WITHOUT OVERLAPS`/`PERIOD`), which is entirely unimplemented across four buckets, none independently fixable: (1) `PRIMARY KEY (... WITHOUT OVERLAPS)` doesn't parse — `pk_cols` (`grammar/goopg_ext.y`) has no such alternative, and `uq_cols`'s existing alternative is a documented golden-AST placeholder that mistreats `WITHOUT`/`OVERLAPS` as two literal column names, not real support; (2) `FOREIGN KEY (col, PERIOD ref_col)` doesn't parse either, a separate grammar production; (3) no GiST access method exists to back the exclusion constraint even if (1)/(2) parsed (`btree v0 only supports int4 / numeric keys`, the same limitation several other M0134 cases hit); (4) no range/multirange operator family (`@>`/`<@`/`&&`/`+`) — confirmed live to be exactly the already-fully-ledgered M0134-0173 gap (`internal/executor/expr.go`'s `evalBinaryOp` `OpContains`/`OpContainedBy`/`OpOverlap` case dispatches on *textual shape*, not static type, so a range operand falls through to the box-operand branch and errors). Landing grammar support for (1)/(2) alone would move **zero** diff lines, since every statement needs (3) or (4) to actually execute — unlike `vacuum_parallel.sql` (M0134-0185), there is no grammar-only win here. `regressExcluded`'s pre-existing "out of scope for goopg v0" policy note for this file confirmed accurate, left unchanged. **RE-ARM TRIGGER:** re-run `scripts/pg-regress-runner.sh --verbose without_overlaps` after BOTH a GiST-access-method milestone and the M0134-0173 range-operator-family follow-up land. Ledgered: buckets (1)/(2), the WITHOUT OVERLAPS/PERIOD grammar gap specifically (buckets 3/4 already covered by existing ledger rows). Sizing detail: `docs/design/0100-0149/m0134-0186-without-overlaps-sizing.md`.
- [x] **M0134-0187 — generated_stored.sql** — regress-sql `failed`, sized live: 0/1 PASS, 1675 → 1608 diff lines, 72 → 59 `^+ERROR` after three contained fixes, still short of pass. **Contained fixes shipped** (design `docs/design/0100-0149/m0134-0187-generated-stored-sizing.md`): (1) the implicit INSERT column list now includes `GENERATED ALWAYS AS … STORED` columns, matching PG's `checkInsertTargets` (which never filtered `attgenerated` out) — three colIndex-building sites brought into lockstep (`internal/parser/analyzer/analyzer.go`'s `resolveInsertTargetColumns`, `internal/optimizer/planner.go`'s `rewriteInsertDefaultMarkers` and `planInsert`'s VALUES-form branch; `INSERT ... SELECT`'s implicit-list form deliberately left unchanged, gated on `s.Select == nil`), plus a new 428C9 "cannot insert a non-DEFAULT value into column" check (`rewriteInsertDefaultMarkers`) for a real value landing on a generated column's cell — this alone fixed the 14× fabricated `INSERT row has N values, target expects M` errors from `INSERT INTO gtest1 VALUES (2, DEFAULT)`-shaped statements; (2) `computeGeneratedColumns` moved in `internal/executor/operators_storage.go` from just-before-partition-routing to just-after before-insert-triggers / before NOT NULL and CHECK enforcement, matching upstream's `ExecBRInsertTriggers` → `ExecComputeStoredGenerated` → `ExecConstraints` order — previously a NOT NULL-declared generated column with a genuinely non-null computed result raised a false violation against the pre-computation NULL placeholder; (3) `evalGenFuncCall` (`internal/executor/operators_generated.go`) gained `nullif`/`coalesce` arms mirroring `expr.go`'s `evalExprSlot` reference implementation — an unrecognised function previously fell through to a silent `NullDatum, nil` (indistinguishable from a legitimate NULL), so `GENERATED ALWAYS AS (nullif(a, 0)) STORED` always computed NULL regardless of the input. New tests: `TestPlanInsertDefaultValuesIncludesGeneratedColumns`, `TestPlanInsertValuesRejectsNonDefaultIntoGeneratedColumn` (`internal/optimizer/planner_test.go`, the latter replacing the now-inverted `TestPlanInsertDefaultValuesSkipsGeneratedColumns`), `TestInsertGeneratedColumnComputedBeforeNotNullCheck`, `TestInsertGeneratedColumnAcceptsDefaultRejectsValue`, `TestGeneratedColumnEvaluatesCoalesce` (`internal/executor/generated_column_insert_test.go`, new file). CSV stays `failed`/`pass_required=no` — six further independently-verified buckets ledgered (`.ralph/deferral_ledger.md`, 2026-09-01, M0134-0187), none attempted this loop: missing `information_schema.columns`/`.column_column_usage` views; entirely-absent CREATE-TABLE-time generated-column semantic validation (duplicate/self-ref/whole-row-var/invalid-ref/immutability/DEFAULT-conflict/IDENTITY-conflict, 7 distinct PG errors none of which goopg raises); whole-row `Var` evaluation inside CHECK constraints; INSERT-through-VIEW bypassing the new 428C9 check (`rewriteInsertDefaultMarkers` doesn't follow the view→base-table chain `planInsert` resolves); a `LIKE INCLUDING GENERATED` interaction; and misc (GRANT/REVOKE on a generated-column table, extended statistics DDL, a domain/generated-column function case). Gates: `go build ./...` PASS; `go test ./internal/optimizer/... ./internal/parser/... ./internal/parser/analyzer/... ./internal/catalog/... ./internal/executor/...` PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=34). Next M0134 task to select: **M0134-0188** (`xml.sql`).
- [x] **M0134-0188 — xml.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-09-01** (sized live: 2222 → 2202 diff lines, 38 → 37 `^-ERROR`). **Shipped:** the fifth "missing evalCast arm = unvalidated text" fix (after xid/circle/float8/range types) — goopg had no `evalCast` arm for `xml` at all, so `'<wrong'::xml` and (worse) an *implicit* column-coercion `INSERT INTO t(x xml) VALUES ('<wrong')` both silently succeeded. New `xmlValidate` (`internal/executor/xmltypes.go`) checks well-formedness per the session `xmloption` GUC (declared but previously unconsumed) via `encoding/xml.Decoder` strict mode — a well-formedness check, not a full XML engine (no libxml2-style DETAIL text, no DTD/entity validation). Wired into **two** sibling paths that both needed fixing (`pattern_sibling_paths_must_agree`): `evalCast`'s new `"xml"` case (also backs `pg_input_is_valid`/`pg_input_error_info` via their own new `"xml"` arms), and `encodeValuePGCtx`'s new `"xml"` case (`internal/executor/codec.go`) — the physical row-encoder INSERT/UPDATE actually calls for an implicit (no explicit `::xml`) coercion, which never routed through `evalCast` at all. New test `TestXMLWellFormedness` (`internal/executor/xmltypes_test.go`). Regression A/B (stash-based, `create_table`/`alter_table`/`type_sanity`) byte-identical, zero regressions. CSV stays `failed`/`pass_required=no` — dominant remainder is two REFACTOR-tier subsystems ledgered (`.ralph/deferral_ledger.md`, 2026-09-01, M0134-0188): (a) the SQL/XML publishing-function grammar (`XMLELEMENT`/`XMLTABLE`/`XMLCONCAT`/… have no grammar production at all, same shape as the already-filed SQL/JSON gap M0134-0168a — blocks M0134-0189 `xmlmap.sql` too); (b) XPath evaluation (`xpath`/`xpath_exists`) plus three contained leaf functions (`xmlcomment`/`xmltext`/`xml_is_well_formed*`); (c) `SET XML OPTION DOCUMENT|CONTENT`'s own grammar form; (d) declaration-level well-formedness checks `encoding/xml` doesn't model. Gates: `go build ./...` PASS; `go test ./internal/optimizer/... ./internal/parser/... ./internal/parser/analyzer/... ./internal/catalog/... ./internal/executor/...` PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (full suite); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=34). Design `docs/design/0100-0149/m0134-0188-xml-wellformedness.md`. Next M0134 task to select: **M0134-0189** (`xmlmap.sql`).
- [x] **M0134-0189 — xmlmap.sql** — regress-sql `not-tried` → **`failed`**, **PARKED 2026-09-01** (sized live: 0/1 PASS, 1340 → 1338 diff lines, 13 → 12 distinct `^+ERROR` shapes). **Shipped:** the file's own setup INSERT carries `'2009-06-08 21:07:30 -07'` into a `timestamptz` column — a SPACE before the numeric zone offset — which goopg rejected with 22007 (`invalid input syntax for type timestamp`) while the byte-identical no-space spelling already worked; verified against a local PG 18.3 cluster that PG accepts both and reads them as the same instant (`DecodeDateTime` tokenizes on whitespace, so an offset is legal whether or not it touches the time field — the same rule `canonicalZulu` already applies to the `Z` spelling, just never extended to a numeric offset). Fix: added six space-variant entries to the shared `pgTimestampLayouts` table (`internal/executor/copy_text.go`), covering the `HH`/`HHMM`/`HH:MM` offset widths on both the `' '`/`'T'` date-time separators, plus a seconds-less `15:04 Z07` form. Because `pgTimestampLayouts` already backs BOTH the CAST/typed-literal path (`evalTypedStringLit`) and the COPY/encode path (`parseCopyTimestampZoneSession`) since M0119-0006's unification, one table edit fixed both sibling paths — no second call site needed a matching change. New test `TestParseCopyTimestampSpaceBeforeOffset` (`internal/executor/timestamp_iso8601_tz_input_test.go`). This removed the file's ONLY non-XML error; CSV stays `failed`/`pass_required=no` — the entire remainder is the SQL/XML publishing-function family (`table_to_xml`/`table_to_xmlschema`/`table_to_xml_and_xmlschema`/`query_to_xml`/`query_to_xmlschema`/`query_to_xml_and_xmlschema`/`cursor_to_xml`/`cursor_to_xmlschema`/`schema_to_xml`/`schema_to_xmlschema`/`schema_to_xml_and_xmlschema` — registered in `pg_proc` but zero executor implementation, ~600-line reference in `postgres/src/backend/utils/adt/xml.c:2867-3465`, REFACTOR-tier) plus `xmlforest`'s own grammar production (already-filed M0134-0188 gap (a), which had already named this file as blocked by it — see ledger row M0134-0189, 2026-09-01). Gates: `go build ./...` PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (full suite incl. `internal/initdb`); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=34). Design `docs/design/0100-0149/m0134-0189-xmlmap-sizing.md`. **M0134-0189 is the highest-numbered M0134 task ID filed in this file — no M0134-0190+ exists.** This does NOT mean M0134 is exhausted: the section preamble records several earlier IDs (0002-0005, 0008, 0010, …) as PARKED-not-selectable pending a re-arm trigger, and the CSV still carries two `not-tried` regress-sql rows (`predicate.sql`, `select_parallel.sql`) whose fix_plan entries claim they were already sized 2026-08-19 — a pre-existing discrepancy, not introduced this loop, that the next M0134 selection should resolve by re-reading the section preamble (foot of this file) rather than assuming either source is authoritative.

## M0135 — SQL/JSON `jsonpath` subsystem (filed 2026-08-20, from M0134-0039/0040/0041 sizing)

Design: `docs/design/m0135-0001-jsonpath-subsystem.md`. goopg has no jsonpath
(SQL/JSON path language) lexer, parser, canonical pretty-printer, or evaluator
anywhere — only `pg_type`/`pg_proc` catalog scaffolding for the `jsonpath` type
and the `jsonb_path_*` function family. Confirmed the dominant root cause of
three regress-sql cases (M0134-0039 `jsonb.sql`'s `@@`/`@?` bucket, M0134-0040
`jsonb_jsonpath.sql` 99.9% of its diff, M0134-0041 `jsonpath.sql` 100% of its
diff via a different failure shape — type-I/O canonicalization, not uncalled
functions). Selection order **within M0134's normal ordering** — not
auto-prioritized ahead of M0134's remaining single-file tasks; select per the
`## Current Priority` banner when it names M0135, or opportunistically if a
loop lands on one of the three still-open regress files and wants to unblock it
properly instead of parking a fourth time.

- [ ] **M0135-S1 — jsonpath lexer + parser + canonical pretty-printer (type I/O
      only)** — new `internal/executor/jsonpath/` package (`lexer.go`/
      `parser.go`/`ast.go`, hand-written recursive-descent mirroring goopg's SQL
      parser precedent). Grammar per `docs/design/m0135-0001-jsonpath-subsystem.md`
      §S1. Wire `jsonpath_in`/`jsonpath_out`
      (`internal/initdb/pg_proc_seed_data.go:2713-2715` has the catalog rows,
      zero executor dispatch today). Acceptance: `scripts/pg-regress-runner.sh
      jsonpath` diff shrinks from 1443 lines toward the S4/unnest-alias residual;
      targeted unit tests for lexer/parser/pretty-printer edge cases (numeric
      literal grammar, `last`/`@` context rules, `?(...)` compaction). Expected to
      flip M0134-0041 (`jsonpath.sql`) outright without needing S2/S3.
- [ ] **M0135-S2 — jsonpath evaluator core** — `internal/executor/jsonpath/eval.go`,
      tree-walking evaluator over decoded jsonb reusing the jsonb-decode helpers
      from `evalJSONPathGet` (`internal/executor/expr.go:1867-2010` —
      `jsonElemAsJSONDatum`/`jsonElemAsTextDatum`), NOT its path grammar (that's
      PG's separate simpler path-extraction type, not SQL/JSON jsonpath). Design
      §S2. Acceptance: `internal/executor/jsonpath/eval_test.go` covering
      lax/strict mode, path steps, arithmetic/comparison, filter `?(...)`,
      `@`/`$`/variables, method calls — not yet wired to SQL functions.
- [ ] **M0135-S3 — wire `jsonb_path_*` functions + `@?`/`@@` operators** — lex
      `@?`/`@@` as new 2-char operator tokens (mirror the M0134-0039 `#>` lexer
      precedent, `internal/parser/lexer.go`); dispatch
      `jsonb_path_query[_tz/_array/_first]`/`_match`/`_exists` and `@?`/`@@` in
      `internal/executor/expr.go`, calling the S2 evaluator. Design §S3.
      Acceptance: `scripts/pg-regress-runner.sh jsonb_jsonpath` and
      `scripts/pg-regress-runner.sh jsonb` re-run; M0134-0039/0040's ledgered
      `@@`/`@?`/`jsonb_path_*` buckets clear.
- [ ] **M0135-S4 — `pg_input_is_valid`/`pg_input_error_info('jsonpath')` soft-error
      surfacing** — small slice once S1 exists; check whether goopg already has
      this machinery for another type before building new plumbing. Design §S4.

## M0136 — `tsvector`/`tsquery` core type engine (filed 2026-09-01, from M0134-0181 sizing)

Design: `docs/design/0100-0149/m0134-0181-tstypes-sizing.md`. goopg has NO
`tsvector`/`tsquery` type kernel anywhere — only `pg_type`/`pg_proc` catalog
scaffolding. `SELECT '1'::tsvector` "succeeds" only because goopg falls back
to an opaque-type text passthrough for a type with no registered I/O
function, not because a real parser exists; calling `tsvectorout(...)`
explicitly errors `function tsvectorout does not exist`. This milestone is
narrower and more foundational than the already-parked M0134-0178/0179
dictionary/stemmer gap (ledger row 0178a, `postgres/src/backend/tsearch/`):
it needs no tokenizer, stemmer, or `pg_ts_config`/`pg_ts_dict` lookup — only
the type itself, its operators, and its non-dictionary utility functions.
Landing S1 alone unblocks a real re-measurement of M0134-0181 (`tstypes.sql`)
and is a genuine prerequisite for the *result* type of `to_tsvector` once
0178a eventually lands. Selection order **within M0134's normal ordering**
— not auto-prioritized ahead of M0134's remaining single-file tasks; select
per the `## Current Priority` banner when it names M0136, or opportunistically
if a loop lands on `tstypes.sql`/`tsdicts.sql`/`tsearch.sql`/`tsrf.sql` and
wants to unblock the shared type-kernel gap properly.

- [ ] **M0136-S1 — `tsvector`/`tsquery` type kernel (parse + canonical
      output only)** — new `internal/executor/tsearch/` package (or
      `internal/executor/tsvector.go`/`tsquery.go` if small enough to stay
      flat — decide during implementation) parsing the `'lexeme:weight,pos
      ...'` tsvector grammar and the `!`/`&`/`|`/`<->`/`()`/weight-label
      tsquery grammar, plus canonical pretty-printers (quoting/escaping
      rules — PG quotes every lexeme and backslash-escapes embedded quotes/
      backslashes). Oracle: `postgres/src/backend/utils/adt/tsvector.c`
      (`tsvectorin`/`tsvectorout`, lines 174/313/407/446),
      `tsvector_parser.c` (shared lexeme-position-list scanner), `tsquery.c`
      (`tsqueryin`/`tsqueryout`, operator-precedence parser: `!` highest,
      then `<->` [with optional `<N>` distance], then `&`, then `|`
      lowest). Wire `tsvectorin`/`tsvectorout`/`tsqueryin`/`tsqueryout`
      (`internal/initdb/pg_proc_seed_data.go` has the catalog rows, zero
      executor dispatch today) plus the `::tsvector`/`::tsquery` CAST path
      (currently a passthrough in `evalCast`/`coerceTextLikeDatum`).
      Acceptance: `scripts/pg-regress-runner.sh tstypes` diff shrinks
      substantially on the pure-literal assertions (lines 6-79 of the file);
      targeted unit tests for parser/pretty-printer edge cases (empty-lexeme
      rejection, escaped quotes/backslashes, weight-label combinations,
      `<N>` phrase-distance parsing, operator precedence/parenthesization).
- [ ] **M0136-S2 — `tsvector` comparison + editing/utility functions** —
      `tsvector_cmp` (lexicographic-then-position-array compare, needed for
      `<`/`>` and btree opclass), `strip`, `setweight` (2-arg + 3-arg lexeme-
      filter form), `ts_delete` (text + text[] forms), `ts_filter` (by
      weight array), `numnode`, `tsquery_phrase` (3-arg with explicit
      distance), `tsvector_to_array`, `array_to_tsvector` (must sort+dedup,
      reject NULL/empty lexemes), `unnest(tsvector)` (table function returning
      lexeme/positions/weights). Oracle:
      `postgres/src/backend/utils/adt/tsvector_op.c:168(strip),
      211(setweight),273(setweight_by_filter),554/578(delete_str/arr),
      632(unnest),720(to_array),747(array_to_tsvector),819(filter)`,
      `tsquery_util.c` (numnode/tsquery_phrase). Depends on S1's parsed
      representation. Acceptance: `tstypes.sql`'s "tsvector editing
      operations" block (lines 235-281) matches the oracle.
- [ ] **M0136-S3 — `@@` match operator + `<->` phrase-distance operator** —
      `ts_match_tq`/`ts_match_vq`/`ts_match_qv`/`ts_match_tt` (`@@` over all
      four tsvector/tsquery operand-type combinations) and the phrase-
      distance execution mode PG's `TS_execute`/`ts_phrase_execute` use for
      `<->` inside a tsquery. Oracle: `postgres/src/backend/utils/adt/
      tsvector_op.c:2206-2310` (`ts_match_*`) plus the phrase-execute path
      referenced from there. Needs new operator lexing/dispatch (mirror the
      M0135-S3 `@?`/`@@` precedent for jsonpath — `@@` is likely already a
      lexable token per M0134-0179's maximal-munch scanner fix, just
      unwired at the operator-dispatch layer, `unsupported operator "@@"`).
      Acceptance: `tstypes.sql`'s "tsvector-tsquery operations" and "phrase
      search" blocks (lines 104-189) match the oracle; re-run
      `tsearch.sql`/`tsdicts.sql` to confirm their `@@` buckets (163/many
      occurrences, ledger 0178a) shrink even though `to_tsvector` itself
      stays blocked.
- [ ] **M0136-S4 — `ts_rank`/`ts_rank_cd` scoring** — all 4 arities each
      (`ts_rank(vector, query)`, `+weights`, `+normalization`, both).
      Oracle: `postgres/src/backend/utils/adt/tsrank.c:439-1010`
      (`ts_rank_wttf`/`_wtt`/`_ttf`/`_tt`, `ts_rankcd_*`) — cover-density
      ranking is a materially different algorithm from plain ranking, not a
      normalization-flag variant. Acceptance: `tstypes.sql`'s "ranking"
      block (lines 191-222) matches the oracle bit-for-bit (floating-point
      formatting sensitivity — the file's own `SET extra_float_digits = 0`
      preamble exists for this).
