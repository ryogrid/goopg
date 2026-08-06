(idle — nothing in flight)

Last loop: **M0125-0003 CLOSED and committed** — the fourth and last consumer of
the `GOOPG_RELSIZE_FALLBACK` block-count estimate is wired, so **M0125 has no
open item left that is not `[→ M0127: absorbed]`.**

1. `reorderCommaFromByCardinality` (`internal/planner/joinorder.go`) bailed the
   moment any table had `Stats.RowCount <= 0` — which, since RowCount does not
   survive a restart (ledger pq-P6), is EVERY table on an S-cold server. The
   guard is now a tier: stored count → `relSizeFallbackRows(2, cat, tbl)` →
   decline.
2. **Stage 2, not a fourth stage.** M0127-P5.6 retired stage 3, leaving stage 2
   defined as "the consumers that move the join ORDER". A `4` would ship it
   default-off behind a value no script sets.
3. **Measured effect is ZERO plan movement, and that IS the evidence**: DS05
   plan channel `queries=99 same=99 changed=0` on a restarted (S-cold) cluster,
   so the pass runs where it used to decline and nothing moves. Mechanism =
   M0127-P5.9-r inverted: `extractScans` descends `JoinTypeCross` only, so a
   comma-FROM list is exactly what reaches `tryPGShapedJoinSearch`, and the
   search re-derives the order. Live residue = what the search declines
   (explicit `JOIN`, over-limit relsets) = `joinorder.go`'s P6.3 role.
4. Sweep deliberately NOT re-run: identical plan text for 99/99 means identical
   execution; that is what the plan channel exists to license.

Files: `internal/planner/joinorder.go`,
`internal/planner/joinorder_relsize_test.go` (4 tests; the landing test proven
to fail pre-fix by checking out HEAD's joinorder.go), design doc
`0125-0003-relsize-fallback-and-tpch-stats-tradeoff.md` §I20–I23 + README index
row, fix_plan (-0003 ticked), 3 ledger motions (1 flip + 2 new).

Gates run: planner PASS; executor/analyzer PASS; units gate 0 FAIL;
`scripts/tpch-spotcheck.sh` PASS (Q12=2 Q13=35, 28.1 s); DS05 plan channel
99/99 same (`plans-20260806-191105.txt`); pgbench smoke via the commit hook.

NEXT LOOP (banner: M0124 closed → M0125 → M0127 → M-NIGHTLY → M0123).
**M0125 is now effectively closed**, so M0127 is the live milestone and
**M0127-P6.1 (delete fusion) is the next selection — IF the gate is met.** Its
bar is "S5-ON survives a clean nightly cycle": read `ci/logs/action-items.md`
FIRST; it was still run `20260806-011323` (`status: fail`, all 18 filed, nothing
new) this loop, and every one of its 4 genuine items is now fixed, so a fresh
nightly at `status: pass` unlocks P6.1. If the file is still that same run, no
new nightly has run and P6.1 stays unselectable on missing evidence.

In-flight: none. Private bench binary at `tmp/goopg-m0125-0003-fourth` (used for
the DS05 plan capture so the nightly's shared `tmp/goopg-bench-bin` was not
touched); the SF0.5 server on 65437 was started and stopped by the gate script.
