(idle — nothing in flight)

Last loop (#64) COMPLETE + committed: PROMOTED `index-only-bitmapscan` (M0118-0002,
design 0118-0122). The spec is an upstream regression guard for an unsound
index-only bitmap heap scan; its real check is the FETCH row counts after a
concurrent VACUUM marks dead pages all-visible (`s1_fetch_1`→1 row,
`s1_fetch_all`→0 rows). goopg already produced those. The SOLE remaining blocker
was step `s1_explain`: `EXPLAIN (COSTS OFF) DECLARE foo ... CURSOR FOR <query>`
raised `0A000 unsupported statement type *parser.DeclareCursorStmt` because
`planner.Plan`'s `ExplainStmt` case planned `s.Inner` directly.

Fix (1 site): `internal/planner/planner.go` ExplainStmt case now unwraps a
`DeclareCursorStmt` inner to its `.Query` before planning (PG
ExplainOneUtility→ExplainOneQuery). Key insight: `normalizeIsoOutput` STRIPS the
EXPLAIN plan block on both sides (established plan-strategy policy, same as
merge-join), so goopg rendering no BitmapOr node is irrelevant — the prior
"must render BitmapOr byte-for-byte" assessment was an over-estimate.

Landed: planner fix + `TestExplainDeclareCursorExplainsInnerQuery` (executor unit)
+ strict `TestPort_IsolationIndexOnlyBitmapscan` + CSV failed→pass + regen
inventory/coverage md + design 0118-0122 + README index + fix_plan M0118-0002 note.
Isolation tally now **117 pass / 4 failed**.

REMAINING failed isolation specs (all genuinely Effort-L unbuilt subsystems):
- `deadlock-parallel`  — M0118-0004; needs a lock-group abstraction goopg lacks.
- `predicate-gin`      — M0118-0002; needs int[] type + GIN AM + AM-grain SIREAD.
- `predicate-gist`     — M0118-0002; needs point type + GiST AM + AM-grain SIREAD.
- `stats`              — M0118-0009; needs the pg_stat_* cumulative subsystem.
(`predicate-hash` already promoted; it over-detected before — coarse relation-grain
SIREAD — but is now strict per memory `goopg_hash_index_ssi_bucket_locking`.)

Gates run: build clean; planner units PASS; executor explain family PASS; strict
TestPort_IsolationIndexOnlyBitmapscan PASS; ralph-state-guard OK; pgbench smoke =
pre-commit hook.
