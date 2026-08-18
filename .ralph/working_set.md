# Working set — M0134-0005af LANDED (two exclusion-constraint gaps)

**Task:** M0134-0005 / sub-item **M0134-0005af** — LANDED.
Selected per the Current Priority banner (M-NIGHTLY had nothing new; M0134 next).

**Nightly triage:** `ci/logs/action-items.md` STILL shows run `20260819-011823`,
items: 1 — the same AI-20260819-011823-001 fixed six loops ago (`2289e149`).
Nothing to file. **Stale for SIX loops.** The prior baton already said: if the
nightly does not refresh, treat the nightly lane itself as not running and
investigate that. It has not refreshed. **Next loop should spend one researcher
on why ci/batch has not produced a new run** before taking another M0134 slice.

**What landed (bucket F, its 2 located items).**
**F9** (silent integrity gap) `ALTER TABLE … ADD EXCLUDE (col WITH =)` never
scanned existing rows: the build's duplicate check in `collectBTreeEntries` was
gated on the same `unique` flag that sets `idx.Unique`, hardcoded false for
exclusions. Fixed by decoupling a new `CheckDup` (on `btreeIndexProps`) from
`unique`, across BOTH the ALTER and CREATE-TABLE sibling call sites. Needed
`IsExclusion`/`ExclusionOp` on the same struct — `execAlterTableAddExclude` set
`idx.IsExclusion` only AFTER `LookupIndex`, too late for the build to pick its
error shape. PG: `execIndexing.c:893-918`, two-sided 23P01.
**F8** ON CONFLICT arbiter validation gated on `idx.Unique` alone in BOTH
`analyzeOnConflict` and `resolveArbiterIndex` → rejected every exclusion
constraint with 42P10. PG accepts them; only `INITIALLY DEFERRED` (i.e.
`indimmediate=false`, NOT plain `Deferrable`) is rejected, with 55000, and with
NO cursor position (`Pos: 0` — PG raises it at execution time).

**Measurement:** constraints **322 → 294 lines, 16 → 14 hunks**, no new `@@`.
Never compare to a pre-2026-08-19 number.

**Gates run:** `go build ./...` PASS; `go test ./internal/executor/
./internal/parser/analyzer/ ./internal/optimizer/` PASS; `pg-regress-runner
--verbose constraints` 322/16 → 294/14; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; all 7 guards re-run by me PASS; pgbench
smoke via the hook. Cache warm.

**Next step:** after the nightly-lane check above, continue M0134-0005 at the
**294/14** baseline. Remaining, ranked:
1. **bucket D** — `pg_get_partition_constraintdef` missing builtin, 1 hunk,
   breaks `\d+` on ANY partition; from-scratch builtin. Landing it ALSO unblocks
   closing G4's shared hunk.
2. **G4** — `ALTER TABLE ONLY … DROP CONSTRAINT`/`DROP NOT NULL` never orphans
   the child copy (real `coninhcount`/`conislocal` corruption); two sibling call
   sites; ledgered; closes no hunk without D.
3. **G2** — unqualified `nextval(…)` in `\d+`; **do NOT brief as a one-line
   fix** — the only nextval-constructing site already qualifies, so the real
   row-source is unknown and needs a live `pg_attrdef` probe first. Ledgered.
4. **F7** — gist exclusions past single-column box overlap; the LARGEST hunk
   left (67 lines) but a multi-piece feature; ledgered, needs its own design.
   Census's "missing circle opclass seed data" framing is WRONG — it is seeded.

**Delegation:** `tmp/ralph-handoffs/m0134-0005af-{research,impl}/` (researcher
`a3094e9c20bd811f8` DONE 1 round — it also CORRECTED the census on F7;
implementer `a770eec137031210d` DONE 1 round, one accepted deviation: `CheckDup`
as a `btreeIndexProps` field rather than a positional param, which avoided
editing ~15 unrelated `createBTreeIndex` call sites).

**In-flight:** none.
