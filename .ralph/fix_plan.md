# goopg Fix Plan

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless the Current
Priority banner below or a dependency forces another order**. **As of 2026-08-15:
M-NIGHTLY is the standing filing obligation (highest priority); M0134
(regress-sql `failed`/`not-tried` digestion) was the next-priority milestone
after M-NIGHTLY (user directive 2026-08-15), and as of 2026-09-01 M0134 is
EXHAUSTED (see below) so active selection falls through to M0119.**
The banner is the sole ordering
authority — `.ralph/working_set.md`'s "NEXT LOOP" note carries state, not
priority, and does not outrank it.
**M0134 (regress-sql `failed`/`not-tried` test-case digestion) was filed
2026-08-15 at the foot of this file and was the next-priority milestone after
M-NIGHTLY** (user directive 2026-08-15) — it was selected immediately after
M-NIGHTLY's regression fixes, ahead of M0119 and M0122's remaining items.
**EXHAUSTED 2026-09-01: all 189 filed M0134 cases have been sized at least
once (verified by a full ID↔case cross-reference against the CSV's current
`failed`/`not-tried` regress-sql rows — zero rows lack an ID, and zero active
task bodies still carry the original unattempted-boilerplate text); every one
is now CLOSED (green or stale-pass) or PARKED on a named REFACTOR-tier
prerequisite with its own re-arm trigger** — see "Exhaustion note (2026-09-01)"
in `docs/milestones/0134-regress-sql-failed-not-tried-digestion.md` for the
full verification method and the re-arm rule. No M0134 task is currently
selectable. M0132 and M0133 are COMPLETE; M0131 is closed except S24 (deferred,
not selectable); M0130 is closed. **Active priority after M-NIGHTLY is now
M0119** (sole remaining task: M0119-0006, pg_amcheck server tier), then M0122.

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

## Current Priority (per 2026-08-15 — M0134; UPDATED 2026-09-01 — M0134 EXHAUSTED, falls through to M0119)

**M-NIGHTLY is the standing filing obligation (unconditional, highest priority):
every loop reads `ci/logs/action-items.md` and files each new `## AI-` subject
under the M-NIGHTLY milestone below.**

**M0134 EXHAUSTED as of 2026-09-01 — no task currently selectable.** All 189
filed regress-sql cases have been sized at least once and are each CLOSED
(green or stale-pass) or PARKED on a named REFACTOR-tier prerequisite with its
own re-arm trigger; verification method and re-arm rule are in
`docs/milestones/0134-regress-sql-failed-not-tried-digestion.md` ("Exhaustion
note (2026-09-01)"). **Active selection (after M-NIGHTLY) is now M0119**
(sole remaining task: M0119-0006, pg_amcheck server tier), then M0122. Do not
re-attempt an M0134 task unless its re-arm trigger has fired — check the
milestone doc's exhaustion note first. The long narrative paragraph below is
kept as the historical record of how each of the 189 cases was sized; it is
no longer an active work queue.

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
**M0119 is now the active milestone** (M0134 exhausted, see above); its sole
remaining task is M0119-0006 (pg_amcheck server tier) — M0119-0005 (pg_waldump
server tier) is already fully landed (see the M0119 section's "Already landed"
note).
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
- [ ] **testport/TestPort_PgDumpConnectionSetup (AI-20260822-001356-001)**.
- [ ] **testport/TestPort_RegressSuite — limit, numerology (AI-20260822-001356-002)**.
- [ ] **race/stage (AI-20260822-001356-004)**.
- [ ] **race/build-broke-mid-stage (AI-20260822-001356-005)**.
### Nightly run 20260824-013441 (sha `e7495e712dda`, 2 items) — filed 2026-08-24
- [ ] **units/testport — TestPort_PgDumpConnectionSetup FAILed (AI-20260825-003932-001)**.
- [ ] **testport/TestPort_InitiallyDeferredFKCommit (AI-20260827-052222-013)**.
- [ ] **testport/TestPort_IsolationAbortedKeyrevoke (AI-20260827-052222-016)**.
- [ ] **testport/TestPort_IsolationAlterTable2 (AI-20260827-052222-017)**.
- [ ] **testport/TestPort_IsolationAlterTable3 (AI-20260827-052222-018)**.
- [ ] **testport/TestPort_IsolationAlterTable4 (AI-20260827-052222-019)**.
- [ ] **testport/TestPort_IsolationClassroomScheduling (AI-20260827-052222-020)**.
- [ ] **testport/TestPort_IsolationClusterConflictPartition (AI-20260827-052222-021)**.
- [ ] **testport/TestPort_IsolationCreateTrigger (AI-20260827-052222-022)**.
- [ ] **testport/TestPort_IsolationDeleteAbortSavept (AI-20260827-052222-023)**.
- [ ] **testport/TestPort_IsolationDeleteAbortSavept2 (AI-20260827-052222-024)**.
- [ ] **testport/TestPort_IsolationDetachPartitionConcurrently4 (AI-20260827-052222-025)**.
- [ ] **testport/TestPort_IsolationDropIndexConcurrently1 (AI-20260827-052222-026)**.
- [ ] **testport/TestPort_IsolationEvalPlanQual (AI-20260827-052222-027)**.
- [ ] **testport/TestPort_IsolationFkContention (AI-20260827-052222-028)**.
- [ ] **testport/TestPort_IsolationFkDeadlock (AI-20260827-052222-029)**.
- [ ] **testport/TestPort_IsolationFkSnapshot (AI-20260827-052222-030)**.
- [ ] **testport/TestPort_IsolationHorizons (AI-20260827-052222-031)**.
- [ ] **testport/TestPort_IsolationInsertConflictDoNothing2 (AI-20260827-052222-032)**.
- [ ] **testport/TestPort_IsolationInsertConflictDoUpdate2 (AI-20260827-052222-033)**.
- [ ] **testport/TestPort_IsolationInsertConflictDoUpdate3 (AI-20260827-052222-034)**.
- [ ] **testport/TestPort_IsolationInsertConflictDoUpdate4 (AI-20260827-052222-035)**.
- [ ] **testport/TestPort_IsolationInsertConflictSpecconflict (AI-20260827-052222-036)**.
- [ ] **testport/TestPort_IsolationIntraGrantInplace (AI-20260827-052222-037)**.
- [ ] **testport/TestPort_IsolationLockCommittedKeyupdate (AI-20260827-052222-038)**.
- [ ] **testport/TestPort_IsolationLockCommittedUpdate (AI-20260827-052222-039)**.
- [ ] **testport/TestPort_IsolationLockNowait (AI-20260827-052222-040)**.
- [ ] **testport/TestPort_IsolationLockUpdateDelete (AI-20260827-052222-041)**.
- [ ] **testport/TestPort_IsolationLockUpdateTraversal (AI-20260827-052222-042)**.
- [ ] **testport/TestPort_IsolationMatviewWriteSkew (AI-20260827-052222-043)**.
- [ ] **testport/TestPort_IsolationMergeMatchRecheck (AI-20260827-052222-044)**.
- [ ] **testport/TestPort_IsolationMergeUpdate (AI-20260827-052222-045)**.
- [ ] **testport/TestPort_IsolationMultipleCic (AI-20260827-052222-046)**.
- [ ] **testport/TestPort_IsolationNowait (AI-20260827-052222-047)**.
- [ ] **testport/TestPort_IsolationNowait2 (AI-20260827-052222-048)**.
- [ ] **testport/TestPort_IsolationNowait3 (AI-20260827-052222-049)**.
- [ ] **testport/TestPort_IsolationNowait4 (AI-20260827-052222-050)**.
- [ ] **testport/TestPort_IsolationNowait5 (AI-20260827-052222-051)**.
- [ ] **testport/TestPort_IsolationPartialIndex (AI-20260827-052222-052)**.
- [ ] **testport/TestPort_IsolationPartitionDropIndexLocking (AI-20260827-052222-053)**.
- [ ] **testport/TestPort_IsolationPlpgsqlToast (AI-20260827-052222-054)**.
- [ ] **testport/TestPort_IsolationPredicateGin (AI-20260827-052222-055)**.
- [ ] **testport/TestPort_IsolationPredicateGist (AI-20260827-052222-056)**.
- [ ] **testport/TestPort_IsolationPredicateHash (AI-20260827-052222-057)**.
- [ ] **testport/TestPort_IsolationPreparedTransactionsCIC (AI-20260827-052222-058)**.
- [ ] **testport/TestPort_IsolationPropagateLockDelete (AI-20260827-052222-059)**.
- [ ] **testport/TestPort_IsolationReadOnlyAnomaly (AI-20260827-052222-060)**.
- [ ] **testport/TestPort_IsolationReadOnlyAnomaly2 (AI-20260827-052222-061)**.
- [ ] **testport/TestPort_IsolationReadOnlyAnomaly3 (AI-20260827-052222-062)**.
- [ ] **testport/TestPort_IsolationReceiptReport (AI-20260827-052222-063)**.
- [ ] **testport/TestPort_IsolationSerializableParallel (AI-20260827-052222-064)**.
- [ ] **testport/TestPort_IsolationSerializableParallel2 (AI-20260827-052222-065)**.
- [ ] **testport/TestPort_IsolationSerializableParallel3 (AI-20260827-052222-066)**.
- [ ] **testport/TestPort_IsolationSkipLocked (AI-20260827-052222-067)**.
- [ ] **testport/TestPort_IsolationSkipLocked2 (AI-20260827-052222-068)**.
- [ ] **testport/TestPort_IsolationSkipLocked3 (AI-20260827-052222-069)**.
- [ ] **testport/TestPort_IsolationSkipLocked4 (AI-20260827-052222-070)**.
- [ ] **testport/TestPort_IsolationStats (AI-20260827-052222-071)**.
- [ ] **testport/TestPort_IsolationTimeouts (AI-20260827-052222-072)**.
- [ ] **testport/TestPort_IsolationTuplelockConflict (AI-20260827-052222-073)**.
- [ ] **testport/TestPort_IsolationTuplelockPartition (AI-20260827-052222-074)**.
- [ ] **testport/TestPort_IsolationTuplelockUpdate (AI-20260827-052222-075)**.
- [ ] **testport/TestPort_IsolationTuplelockUpgradeNoDeadlock (AI-20260827-052222-076)**.
- [ ] **testport/TestPort_IsolationTwoIds (AI-20260827-052222-077)**.
- [ ] **testport/TestPort_IsolationUpdateConflictOut (AI-20260827-052222-078)**.
- [ ] **testport/TestPort_IsolationVacuumNoCleanupLock (AI-20260827-052222-079)**.
- [ ] **testport/TestPort_LockRowsSortOverJoinTakesRowLock (AI-20260827-052222-080)**.
- [ ] **testport/TestPort_NonPartitionedDeferredFKStillCatchesViolationAtCommit (AI-20260827-052222-082)**.
- [ ] **testport/TestPort_NotNullAddConstraintCascadesUnderParentName (AI-20260827-052222-083)**.
- [ ] **testport/TestPort_NotNullAddConstraintNotValidCascadesNotValid (AI-20260827-052222-084)**.
- [ ] **testport/TestPort_NotNullAttachPartitionAbsorbs (AI-20260827-052222-085)**.
- [ ] **testport/TestPort_NotNullAttachPartitionMissingChildConstraint (AI-20260827-052222-086)**.
- [ ] **testport/TestPort_NotNullAttachPartitionNotValidConflict (AI-20260827-052222-087)**.
- [ ] **testport/TestPort_NotNullAttachPartitionStillClearsLocal (AI-20260827-052222-088)**.
- [ ] **testport/TestPort_NotNullCascadeSkipsUnrelatedSibling (AI-20260827-052222-089)**.
- [ ] **testport/TestPort_NotNullCascadesMultiLevel (AI-20260827-052222-090)**.
- [ ] **testport/TestPort_NotNullDetachPartitionUnabsorbs (AI-20260827-052222-091)**.
- [ ] **testport/TestPort_NotNullDiamondConinhcount (AI-20260827-052222-092)**.
- [ ] **testport/TestPort_NotNullInheritAbsorbsButKeepsLocal (AI-20260827-052222-093)**.
- [ ] **testport/TestPort_NotNullInheritNoInheritCycleDoesNotDriftCoinhcount (AI-20260827-052222-094)**.
- [ ] **testport/TestPort_NotNullInheritTransactionalFormAbsorbs (AI-20260827-052222-095)**.
- [ ] **testport/TestPort_NotNullNoInheritUnabsorbs (AI-20260827-052222-096)**.
- [ ] **testport/TestPort_NotNullSetNotNullCascadesToChildren (AI-20260827-052222-097)**.
- [ ] **testport/TestPort_NotNullSetNotNullOnExistingDoesNotDoubleCascade (AI-20260827-052222-098)**.
- [ ] **testport/TestPort_PartitionedDeferrableUniqueDefersToCommitViaSetConstraintsAll (AI-20260827-052222-099)**.
- [ ] **testport/TestPort_PartitionedDeferrableUniqueDefersToCommitViaSetConstraintsNamed (AI-20260827-052222-100)**.
- [ ] **testport/TestPort_PartitionedDeferrableUniqueNamedChildOwnConstraintStillDefers (AI-20260827-052222-101)**.
- [ ] **testport/TestPort_PartitionedDeferredFKCatchesViolationAtCommit (AI-20260827-052222-102)**.
- [ ] **testport/TestPort_PartitionedDeferredFKMultiLevelCatchesViolationAtCommit (AI-20260827-052222-103)**.
- [ ] **testport/TestPort_PartitionedDeferredFKSatisfiedCommitsCleanly (AI-20260827-052222-104)**.
- [ ] **testport/TestPort_PartitionedUniqueConstraintFansOutToPgConstraint (AI-20260827-052222-105)**.
- [ ] **testport/TestPort_PgAmcheck005OpclassDamage (AI-20260827-052222-106)**.
- [ ] **testport/TestPort_PgDumpConnectionSetup (AI-20260827-052222-107)**.
- [ ] **testport/TestPort_PgDumpDatabaseConfigSet (AI-20260827-052222-108)**.
- [ ] **testport/TestPort_PgDumpDatabaseGrantACL (AI-20260827-052222-109)**.
- [ ] **testport/TestPort_PgDumpRoleConfigSet (AI-20260827-052222-110)**.
- [ ] **testport/TestPort_PgDumpallGlobalsOnly (AI-20260827-052222-111)**.
- [ ] **testport/TestPort_PgDumpallParameterACL (AI-20260827-052222-112)**.
- [ ] **testport/TestPort_PgDumpallPredefinedRoleMembership (AI-20260827-052222-113)**.
- [ ] **testport/TestPort_PgDumpallRoleMembership (AI-20260827-052222-114)**.
- [ ] **testport/TestPort_PgoutputInteropGoopgToPG (AI-20260827-052222-115)**.
- [ ] **testport/TestPort_RegressSuite (AI-20260827-052222-116)**.
- [ ] **testport/TestPort_SetConstraintsDeferral (AI-20260827-052222-117)**.
- [ ] **testport/TestPort_TimeoutsRowLevel (AI-20260827-052222-121)**.
- [ ] **testport/TestPort_TwoPhaseCommitSameBackend (AI-20260827-052222-122)**.
- [ ] **testport/TestPort_ZeroColumnJoinDoesNotCrashBackend (AI-20260827-052222-123)**.
- [ ] **testport/TestSyntax_DML_Delete (AI-20260827-052222-125)**.
- [ ] **testport/TestSyntax_DML_Update (AI-20260827-052222-126)**.
- [ ] **testport/TestSyntax_Locking_ForShare (AI-20260827-052222-127)**.
- [ ] **testport/TestSyntax_Locking_ForUpdate (AI-20260827-052222-128)**.
- [ ] **testport/TestSyntax_Locking_Nowait (AI-20260827-052222-129)**.
- [ ] **testport/TestSyntax_Select_Case (AI-20260827-052222-130)**.
- [ ] **testport/TestSyntax_Select_CurrentSetting (AI-20260827-052222-131)**.
Non-testport items from the same run:

- [ ] **tpcds/stage (AI-20260827-052222-132)**.
- [ ] **tpch/Q5-timeout (AI-20260827-052222-133)**.
### Nightly run 20260828-235424 (sha `5773b884c5bf`, 2 items) — filed 2026-08-29
- [ ] **tpch/Q5-timeout (AI-20260828-235424-002)**.
### Nightly run 20260831-013952 (sha `c051b81fa596`, 2 items) — filed 2026-09-01
- [ ] **testport/stage (AI-20260831-013952-001)**.
- [ ] **testport/build-broke-mid-stage (AI-20260831-013952-002)**.
### Nightly run 20260901-010436 (sha `d93fb9edc669`, 7 items) — filed 2026-09-01
- [ ] **testport/TestPort_PgStatActivity (AI-20260901-010436-005)**.
- [ ] **testport/TestSyntax_Catalog_PgStatActivity (AI-20260901-010436-007)**.
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

- [ ] **M0095-0003**.
## M0110 — Additional TAP Test Porting (beyond M0094/M0095) (filed 2026-05-22)

Scope = `docs/test-port/upstream-tap-coverage.md` tests not covered by M0094
(recovery/subscription) or M0095. Tags: SHOULD_PASS / BUG_FIX / UNIMPLEMENTED.
Already complete within M0110 (detail in git history): **M0110-0004** pg_resetwal
(RW-001..004 PASS), **M0110-0007 / M0110-0010** B-tree split & vacuum sibling
prev-link fixes.

- [ ] **M0110-0001 — pg_dump TAP**.
- [ ] **M0110-0002 — pg_waldump TAP**.
- [ ] **M0110-0003 — pg_amcheck TAP**.
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
prune-WAL round-trip — M0119-0005 is now fully landed, no open bullet
remains). The one open item below carries the remaining unbuilt scope, and is
now the milestone's (and, as of 2026-09-01, the whole file's) active task per
the Current Priority banner (M0134 exhausted).

- [ ] **M0119-0006 — pg_amcheck server tier**.
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

- [ ] **M0122-0008 — Auth / roles / multi-DB isolation / encoding**.
- [ ] **M0122-0009 — WAL / recovery / crash-consistency infra**.
- [ ] **M0122-0010 — Concurrency: buffer pool & btree locking**.
- [ ] **M0122-0012 — Perf infra: vectorization / slot-pipeline / harness**.
- [ ] **M0122-0013 — Physical/streaming replication & standby**.
- [ ] **M0122-0014 — Logical replication / decoding / subscription**.
- [ ] **M0122-0015 — Test-suite porting: amcheck / verify_heapam / pg_dump**.
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

- [ ] **M0131-S24 — MultiXact: durable `pg_multixact` SLRU + `multixact_redo`** — DEFERRED.
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

- [ ] **M0134-0001 — aggregates.sql** — regress-sql `failed`.
- [ ] **M0134-0002 — alter_table.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0003 — arrays.sql** — regress-sql `failed`.
- [ ] **M0134-0004 — cluster.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0004-a — `CREATE DATABASE ... TEMPLATE` drops table owners**.
- [ ] **M0134-0005 — constraints.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0008 — select_parallel.sql** — PARKED.
- [ ] **M0134-0009 — select_views.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0010 — predicate.sql** — PARKED.
- [ ] **M0134-0011 — subselect.sql** — PARKED.
- [ ] **M0134-0012 — update.sql** — regress-sql `failed`.
- [ ] **M0134-0013 — insert.sql** — regress-sql `failed`.
- [ ] **M0134-0014 — mvcc.sql** — PARKED.
- [ ] **M0134-0015 — join.sql** — PARKED.
- [ ] **M0134-0016 — create_table.sql** — regress-sql `failed`.
- [ ] **M0134-0018 — create_index.sql** — PARKED.
- [ ] **M0134-0019 — indexing.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0020 — stats.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0021 — vacuum.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0022 — window.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0023 — write_parallel.sql** — PARKED.
- [ ] **M0134-0024 — generated_virtual.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0025 — groupingsets.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0026 — guc.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0027 — copy.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0028 — horology.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0029 — identity.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0030 — incremental_sort.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0031 — copy2.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0032 — inherit.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0033 — create_procedure.sql** — regress-sql `failed`.
- [ ] **M0134-0034 — insert_conflict.sql** — regress-sql `failed`.
- [ ] **M0134-0035 — interval.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0036 — create_table_like.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0037 — join_hash.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0038 — json.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0039 — jsonb.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0040 — jsonb_jsonpath.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0041 — jsonpath.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0042 — lock.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0043 — matview.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0044 — merge.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0045 — misc.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0046 — misc_functions.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0047 — multirangetypes.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0048 — create_view.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0049 — numeric.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0050 — numeric_big.sql** — regress-sql `failed`.
- [ ] **M0134-0052 — partition_join.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0053 — partition_prune.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0054 — plancache.sql** — regress-sql `failed`.
- [ ] **M0134-0055 — plpgsql.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0056 — portals.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0057 — prepared_xacts.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0058 — random.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0059 — rangefuncs.sql** — regress-sql `failed`.
- [ ] **M0134-0060 — rangetypes.sql** — regress-sql `failed`.
- [ ] **M0134-0061 — regex.sql** — regress-sql `failed`.
- [ ] **M0134-0063 — returning.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0064 — rowtypes.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0065 — rules.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0066 — date.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0067 — domain.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0069 — sequence.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0070 — strings.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0071 — equivclass.sql** — regress-sql `failed`.
- [ ] **M0134-0072 — temp.sql** — regress-sql `failed`.
- [ ] **M0134-0073 — tidrangescan.sql** — regress-sql `failed`.
- [ ] **M0134-0074 — tidscan.sql** — regress-sql `failed`.
- [ ] **M0134-0075 — timestamp.sql** — regress-sql `failed`.
- [ ] **M0134-0076 — timestamptz.sql** — regress-sql `failed`.
- [ ] **M0134-0077 — transactions.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0078 — triggers.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0079 — tuplesort.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0080 — txid.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0081 — updatable_views.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0082 — explain.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0083 — uuid.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0084 — expressions.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0085 — fast_default.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0086 — with.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0087 — xid.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0088 — alter_generic.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0089 — alter_operator.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0090 — amutils.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0141 — memoize.sql** — PARKED.
- [ ] **M0134-0142 — misc_sanity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0143 — money.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0145 — object_address.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0146 — oidjoins.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0147 — opr_sanity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0148 — password.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0149 — path.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0157a — parser: statement nodes reporting `Pos() == 0`**.
- [ ] **M0134-0157b — non-deterministic function overload resolution**.
- [ ] **M0134-0158a — publication grammar subset + `pg_relation_is_publishable`**.
- [ ] **M0134-0158b — `~~`/`!~~`/`~~*`/`!~~*` are not operators in goopg**.
- [ ] **M0134-0159 — regproc.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0159a — reg\* function-style casts are echo stubs**.
- [ ] **M0134-0159b — the `to_reg*` soft-error family is undispatched**.
- [ ] **M0134-0159c — goopg's system B-tree cannot split**.
- [ ] **M0134-0160 — reloptions.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0160a — the WITH clause is a map, so PG's list semantics are lost**.
- [ ] **M0134-0160b — `ALTER … SET (reloptions)` applies 4 of the 24 options it validates**.
- [ ] **M0134-0161 — replica_identity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0162a**.
- [ ] **M0134-0162b**.
- [ ] **M0134-0162c**.
- [ ] **M0134-0163 — rowsecurity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0163a — row-level security is never enforced at scan time**.
- [ ] **M0134-0163b — `pg_policies` system view does not exist**.
- [ ] **M0134-0163c — `CREATE POLICY … AS <bogus>` is accepted instead of erroring**.
- [ ] **M0134-0164 — sanity_check.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0164a — `pg_index` describes no bootstrap system-catalog index**.
- [ ] **M0134-0165a — `client_min_messages` rejects upstream's hidden `info`/`debug` aliases**.
- [ ] **M0134-0165b — `plpgsql.sql` is nondeterministic at `select * from f1(42)`**.
- [ ] **M0134-0166a — hyperbolic / degree-trig / gamma / error-function family entirely unimplemented**.
- [ ] **M0134-0166b — `@` (float8abs) and `&#124;/` (dsqrt) prefix operators are unlexed**.
- [ ] **M0134-0166c — `trunc`/`ceil`/`ceiling`/`floor` of a large float8 overflow through int64**.
- [ ] **M0134-0166d — float8 arithmetic is evaluated in decimal, not float64**.
- [ ] **M0134-0167a — no SP-GiST access method**.
- [ ] **M0134-0167b — explicit `ASC` / `NULLS LAST` still accepted on orderless AMs**.
- [ ] **M0134-0167c — capability gate not applied to the constraint-side index paths**.
- [ ] **M0134-0167d — `pg_get_indexdef` prints a Go value dump for a COLLATE in a partial-index predicate**.
- [ ] **M0134-0168 — sqljson.sql** — PARKED.
- [ ] **M0134-0168a — SQL/JSON constructor & predicate family (whole subsystem)**.
- [ ] **M0134-0168b — `\d` lists a partitioned table's per-partition FK constraints under "Referenced by:"**.
- [ ] **M0134-0168c — reg\* input errors report the cast position, not the literal's**.
- [ ] **M0134-0168d — `regtype`/`regrole`/`regnamespace` string casts still fall through on a miss**.
- [ ] **M0134-0168e — `pg_get_viewdef('nosuch'::regclass)` returns empty instead of raising**.
- [ ] **M0134-0168f — the whole `to_reg*` builtin family is missing**.
- [ ] **M0134-0169 — sqljson_jsontable.sql** — PARKED.
- [ ] **M0134-0169a — `pg_get_viewdef` echoes a view's raw source text instead of re-deparsing it**.
- [ ] **M0134-0169b — decide `copy_inner`'s `select_bare` against `gram.y`**.
- [ ] **M0134-0170 — sqljson_queryfuncs.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0171 — foreign_key.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0172 — stats_ext.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0173 — stats_import.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0174 — subscription.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0174a — **.
- [ ] **M0134-0174b — **.
- [ ] **M0134-0174c — **.
- [ ] **M0134-0174d — **.
- [ ] **M0134-0174e — **.
- [ ] **M0134-0175 — tablesample.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0175b — a LATERAL outer column cannot be used as a sample argument**.
- [ ] **M0134-0175c — TABLESAMPLE on a view or CTE is silently honoured instead of raising 42809**.
- [ ] **M0134-0175d — sample arguments are not coerced to float4, and bool→int has no cast arm**.
- [ ] **M0134-0175e — a second `FETCH FIRST` on an already-scrolled SCROLL CURSOR returns 0 rows**.
- [ ] **M0134-0176 — tablespace.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0176a — `ALTER {TABLE|INDEX|MATERIALIZED VIEW} ALL IN TABLESPACE` is unparsed**.
- [ ] **M0134-0176b — `pg_tablespace_location()` is catalogued but has no handler**.
- [ ] **M0134-0178 — tsdicts.sql** — regress-sql `failed` (PARKED).
- [ ] **M0134-0179 — tsearch.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0180 — tsrf.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0181 — tstypes.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0182 — type_sanity.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0183 — typed_table.sql** — regress-sql `not-tried` (PARKED).
- [ ] **M0134-0186 — without_overlaps.sql** — PARKED.
- [x] **M0134-0187 — generated_stored.sql** — regress-sql `failed`.
- [x] **M0134-0188 — xml.sql** — regress-sql `not-tried` (PARKED).
- [x] **M0134-0189 — xmlmap.sql** — regress-sql `not-tried` (PARKED).
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

- [ ] **M0135-S1 — jsonpath lexer + parser + canonical pretty-printer (type I/O only)**.
- [ ] **M0135-S2 — jsonpath evaluator core**.
- [ ] **M0135-S3 — wire `jsonb_path_*` functions + `@?`/`@@` operators**.
- [ ] **M0135-S4 — `pg_input_is_valid`/`pg_input_error_info('jsonpath')` soft-error surfacing**.

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

- [ ] **M0136-S1 — `tsvector`/`tsquery` type kernel (parse + canonical output only)**.
- [ ] **M0136-S2 — `tsvector` comparison + editing/utility functions**.
- [ ] **M0136-S3 — `@@` match operator + `<->` phrase-distance operator**.
- [ ] **M0136-S4 — `ts_rank`/`ts_rank_cd` scoring**.