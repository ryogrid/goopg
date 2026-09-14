Task: M0139-S3 — "measure the residue against K67's floor" (plan-parity
milestone group, recon task). **COMPLETE and committed** (`9fb1f1f88`) and
pushed this loop, branch `plan-parity-with-pg-take2-ralph`.

Files: `docs/design/0100-0149/m0139-s3-k67-residue-measurement.md` (new,
full method + numbers), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0139-S3 checked off + summary). No production `.go` file changed — this
was a recon task per the plan-parity harness's carve-out (measurement + a
design note, no production diff). The throwaway probe
(`internal/testutil/tpch/zz_probe_m0139s3_test.go`) was deleted before
commit, per the M0139-S1/S2 precedent — do not go looking for it.

What was done: K67's "72 B/row -> 103 MB, narrowed to one column" TPC-H Q12
anchor was analytical (`hashsize.EntryBytes(1,0)=48+24+0=72`), never
measured, and predates S1/S2's real join-leg narrowing. Built a PRIVATE,
disposable goopg cluster from HEAD (`internal/testutil/cluster` +
`tpch_scale_run_test.go`'s existing `scaleLoader`, 20,000 real-DDL
orders/lineitem rows, real TPC-H categorical vocabularies) — deliberately
never touched the shared, peer-owned `:65433` TPC-H bench server (a
read-only EXPLAIN there first showed the shared binary is STALE relative to
HEAD: it exhibits ZERO narrowing on Q12, so it was not used for any
measurement). On the private HEAD cluster, `EXPLAIN (VERBOSE)` confirmed
the join-leg hook DOES fire on Q12 (Hash Join `Output:` narrows from 16+9
columns at the two scans down to 7), and the real orders-side retained set
is `{o_orderkey, o_orderpriority}` — **2 columns, not K67's assumed 1** —
because the join key must stay in the stored entry for probe-time
verification even though nothing above the join references it. Real
`pg_stats.avg_width(o_orderpriority)=8.3701` (K67 assumed 0). Formula
`EntryBytes(2, 8.3701) ≈ 128.37 B/row`, cross-checked against the
executor's own `EXPLAIN (ANALYZE, VERBOSE)` measured `Buckets: 32768
Batches: 1 Memory Usage: 4044kB` (subtracting the `MapSlotBytes×NBuckets`
bucket-table term reproduces 128.4 B/row to within rounding — two
independent derivations agree). Extrapolated to SF=1's 1.5M orders the same
way K67 did (entries only, apples-to-apples): **≈193 MB, vs K67's stated
103 MB and PG's unchanged 22 B/row → 31 MB anchor.**

Key symbols: `hashsize.EntryBytes`/`hashsize.MapSlotBytes`
(`internal/executor/hashsize/hashsize.go:144,80`), `entrywidth.go`'s
`buildAvgVarBytes` (the real consumer of `pg_stats.avg_width` via
`RelOptInfo.AvgVarBytes`), `narrowJoinLeg` (joinleghook.go, the S1/S2
mechanism being measured, unchanged this loop), `internal/testutil/cluster`
+ `scaleLoader` (`internal/testutil/tpch/tpch_scale_run_test.go`, the reused
private-cluster harness — this is the pattern to reuse for any future
"real number off a live plan" measurement without touching a shared bench
server).

Hypothesis/Findings: **verdict is that S1/S2's real narrowing did NOT close
the gap to K67's floor — the measured residue (128.4 B/row → ~193 MB) is
WORSE than the anchor the campaign had been budgeting against, not better.**
K67's "one column, 72 B/row" was itself too optimistic: it assumed a hash
entry could shed the join key (it cannot — verification needs it) and that
the surviving payload column costs 0 extra bytes (real TPC-H text content
never does). Both engines have non-zero residues once real narrowing is
applied; goopg's is now measured at ~5.8× PG's (128.4 vs 22 B/row), not the
~3.3× K67's numbers implied. This feeds M0139-0006 (put the packed-retention
decision to the owner) with a real number — it does NOT itself decide or
advance `minimize_datum` (still NOT APPROVED TO START). A useful general
lesson banked in the design doc: a shared bench-lane binary can silently
predate the very mechanism you're trying to measure — always confirm
against a fresh-from-HEAD private cluster before trusting a shared server's
plan shape as "current behavior."

Gates run: recon task, no production diff, so the heavy practice-card
gates (tpch-spotcheck, tpcds sweep) do not apply — confirmed via `AGENT.md`
§"Plan-parity harness" → "Way of working" (a recon task's own commit is a
scope violation if it contains a production diff; this one has none). `go
build ./...` clean (confirmed unaffected). Throwaway probe
(`TestZZProbeM0139S3Residue`) passed standalone before deletion (`go test
-run TestZZProbeM0139S3Residue ./internal/testutil/tpch/`, ~5s, private
cluster, never touched shared ports). `make ralph-state-guard`: same
recurring stale status/progress.json pattern as every prior loop
(status="running"/progress="completed" from the previous loop's clean
exit), auto-repaired to consistent, then confirmed consistent.

In-flight: none. Nightly triage for this loop: `ci/logs/action-items.md`'s
newest run (`20260914-235643`, 14 items) was already fully filed as of
2026-09-15 by a prior loop (verified — every AI-id either has its own new
task line or is appended to an existing open task per the "do not add
another" rule); no new filing needed this loop.

Next step: select the next M0139 slice per the banner order. Two of the
three remaining M0139 items are now directly informed by this loop's
number: **M0139-0006** ("put the packed-retention decision to the owner")
can now cite S3's real 128.4 B/row / 193 MB figures instead of K67's
analytical ones — still do NOT implement `minimize_datum`, only write up
the decision packet. **M0139-0005** ("re-measure Q4's grouping election
ratio") is independent and still open — R81 located the divergence in
`electOrderedGrouping` (`upperorderedgrouping.go:148`) at a startup ratio
vs `stdFuzzFactor=1.01` (goopg 1.0086 inside fuzz vs PG's 1.0118 outside);
report whether S1/S2's narrowed widths move that ratio across the band —
the private-cluster probe pattern from this loop is directly reusable there.
**M0139-0004** ("re-measure the duplicate hash build map premise") is also
still open and independent, cheap to take if a slice is blocked.
Alternatively M0140's still-open M0140-0003 remains valid per the banner
("M0140 does not wait on M0139"). Do not re-open S1/S2/S3 — all three fully
done, gated, and pushed.
