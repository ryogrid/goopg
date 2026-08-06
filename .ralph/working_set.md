(idle — nothing in flight)

Last loop: **M0127-P5.7 roll-up CLOSED.** Both sub-items were already `[x]`; the
residue was the roll-up's own BAR, and the finding is that **the bar's premise
expired between the sub-items landing and this read.**

-a and -b each recorded "PLAN not applicable, and the reason is structural, not
a skip: every consumer is behind an OFF-by-default gate". True when written
(2026-08-05 12:20 / 12:47), FALSE at HEAD: P5.9 flipped `GOOPG_PGSHAPED_DP` ON
by default at 2026-08-06 02:22 (`pgShapedDPFromEnv` = `v != "0"`; the spotcheck
banner prints `GOOPG_PGSHAPED_DP=unset(on)`). `hashJoinCost` is now reached on
every hash path the live search considers (`addHashJoinPath` ← joinpaths.go:197)
and `finalPath` → `getCheapestFractionalPath` runs on every searched statement.

**"Zero diffs in the default arm" is void, not failed** — it was a containment
check on an inert search; post-flip the default arm is *supposed* to move plans.
What discharges P5.7 is that both halves were in the tree the flip's own
acceptance measured (run 4 `23dcc60e` 01:04, and the default-arm audit: Q9 final
joinrel 6.3×, `parity_violations=0`) — measured under P5.9's label, never P5.7's.

**The defect was bigger than P5.7 and is fixed in the same commit: 28 files in
`internal/planner/` still carried "Still inert: `GOOPG_PGSHAPED_DP` is OFF … so
they cannot change a plan" headers** — including `cost_funcs.go`, the file
`hashJoinCost` lives in. Those banners are read as gate-skip authority. Each
corrected to verified post-flip reachability, per file, not blanket-rewritten.
Three came out differently: `collapse.go` (joinlist IS consumed at
joinsearchseam.go:162/170; only the narrower `GOOPG_PGSHAPED_COLLAPSE`
flattening arm is off), `tuplefraction.go` (below), `exprwalk.go` (stale from a
DIFFERENT cause — M0125-0002's walker conversion — left alone + ledgered rather
than corrected on a guess).

**Discharged:** P5.7-b's ledger row "no production PRODUCER of a fraction yet" —
P5.9-b added `searchTupleFraction` (joinsearchseam.go:579) ← planner.go:1128.
Checked rule #2: no gap, the only arm skipping it is `isSimpleSingle`, which is
single-relation and never reaches the search. Note the half live even with NO
LIMIT: `ConsiderStartup` is `tupleFraction > 0`, so fraction 0 actively PRUNES
fast-start paths rather than abstaining. Caller-fraction half re-filed (no
`cursor_tuple_fraction` source).

Files: 28 planner files (comment-only) + fix_plan (P5.7 tick, -a/-b EXPIRED
notes), IMPLEMENTATION-TODO (P5.7 roll-up entry), 04 §4.4 (new), design README,
3 ledger rows (1 flipped `resolved`, 2 new).

Gates run: UNITS 0 FAIL; **SPOT PASS (Q12=2, Q13=35)**. DS05 not re-run — the
diff is comment-only, zero executable change (`go build` + `go vet` clean).

NEXT LOOP (banner wins — M0127 is #3 and current). Topmost unchecked M0127 items
are now **PS6.1/PS6.2**, then the P6.x deletion series (P6.3 deletes the legacy
enumerator + `rewriteJoinsToNLI`/`tryBuildNLI`, which is what collapses the two
NLI constructors back to one — see the 2026-08-06 P5.4b-ii-b-2 ledger row).
**Carry this loop's lesson in: a flag flip invalidates comments, task
justifications and skipped gates that cited the old default, and none of them
appear in the flip's diff — when P6.x deletes the legacy arm, sweep its claims
in the same commit rather than leaving them for a later audit.**

Nightly triage: `ci/logs/action-items.md` still run 20260806-011323, 18 items,
all subjects already filed under M-NIGHTLY. Nothing new.

In-flight: none.
