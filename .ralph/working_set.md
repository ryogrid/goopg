(idle — nothing in flight)

Last completed: M0143-0004 (`PhysicalTypeIsVarlena` had no `IsArray` arm),
commit `c6541a941`. Also filed (and closed as stale, same-loop) the 5
nightly items from `ci/logs/action-items.md` run `20260918-010720` — all
five shared one root cause (a mid-build race in the nightly batch's live
working-tree checkout captured a half-applied tree between two concurrent
Ralph commits; `go build ./...` was already clean at HEAD before this
loop's own changes). See `.ralph/fix_plan.md`'s
`### Nightly run 20260918-010720` section.

What landed (M0143-0004): one-line `if t.IsArray { return true }`
short-circuit at the top of `catalog.PhysicalTypeIsVarlena`
(`internal/catalog/physical_align.go`), mirroring `PhysicalTypeAlign`'s
existing `IsArray` arm. A user array column carries `Type{Name:<element>,
IsArray:true}` (Name = element type); every fixed-width element name
(`int4`, `bool`, `date`, `uuid`, `xid`, …) was misclassified as non-varlena.
Confirmed LIVE against real heap bytes (not just code reading): built
pre-fix/post-fix binaries, started throwaway clusters, parsed raw page
bytes for `CREATE TABLE onlyarr (tags int4[]); INSERT ... VALUES
(ARRAY[1,2,3])` — pre-fix `t_infomask=0x0800` (HEAP_HASVARWIDTH unset),
post-fix `0x0802` (correct). This is the exact `nocachegetattr` fast-path
hazard `codec.go:1550-1557` already documents. Also confirmed (not
assumed) that D-09's own encode-side pack/align mechanism is UNAFFECTED:
`encodeArrayValuePGCtx` always emits the long-form varlena header, so
`packableShortColumn`'s short-header gate was already false for arrays
either way (byte-identical relation file sizes pre/post fix, verified for
two table shapes at 2000/5000 rows).

New tests: `TestPhysicalTypeIsVarlenaArray`
(`internal/catalog/physical_align_test.go`), a new `{Name:"int4",
IsArray:true}` case in `TestPgRowHasVarWidthDetectsVarlenaCols`
(`internal/executor/pg18_user_catalog_rows_test.go`).

Gates run: `go build ./...` clean; `go test ./internal/catalog/...
./internal/executor/... ./internal/access/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` full green; `FORCE=1
scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0 ERROR=0
TIMEOUT=0, plan-shapes 99/99 identical (re-run against the staged tree for
the gate-stamp check); `scripts/tpch-spotcheck.sh` SKIP-BLOCKED (exit 3,
expected — `:65433` still under the P0-E6 evidence hold; gate stamp
refreshed against staged tree); commit accepted via the documented
`PARITY: N/A`/`ledger:` exception (same as M0143-0003b/c/d/e/f); pgbench
smoke PASS (pre-commit hook); `make ralph-state-guard` clean (auto-repaired
the same stale status/progress marker as the last two loops — worth a
look if it recurs a fourth time).

Docs updated: `.ralph/fix_plan.md` (M0143-0004 `[ ]`->`[x]`, nightly-run
section appended), `.ralph/deferral_ledger.md` (2 new M0143-0004 rows:
the fix itself + the standing gate-blocker note), new design doc
`docs/design/0100-0149/m0143-0004-physicaltypeisvarlena-isarray-gap.md`
(indexed in `docs/design/README.md`), addendum section 6 added to the
pre-existing `docs/design/executor-d09-alignment/DESIGN.md`.

Next step: re-read `.ralph/fix_plan.md`'s `## Current Priority` banner.
Check whether P0-E6 flipped `[x]` (HOLD marker
`bench/tpch/runtime_goopg/data.HOLD` was still present as of this loop).
If still `[!]`, continue the P0-E6-wait fallback: **M0143-0005** (`ParamRef`
LIMIT + DISTINCT returns wrong rows, fix_plan ~line 7461) is the next
unchecked M0143 task whose gate doesn't need TPC-H data — take it next
unless the banner has changed. M0143-0007 (relpages/storage) is also
selectable if 0005 turns out blocked. Read AGENT.md §"Plan-parity harness"
before selecting, per the banner's standing instruction.

In-flight: none.
