# M0125-0002 commit 7 of 8 — `visitColumnRefsByName` re-based onto `walkExprRefs`

Date: 2026-08-03. Branch `planner-dp-and-related-refactor`, base `900990a2`
(commit 6). This is the LAST commit of the walker-conversion series and the one
D2 row 7 named as having "the largest and least predictable effect on MHJ
composition".

## What changed

`visitColumnRefsByName` (`bushy.go`) was a 7-of-32-arm hand switch. It is now
`walkExprRefs` under `scopeSignal`, and it **returns a second result**: whether
the name test COVERED the expression.

The signature change is the point. All three consumers seed a verdict `true`
and falsify it only from inside the callback:

```go
allMatched := true
visitColumnRefsByName(c, func(name string) { if !found { allMatched = false } })
return allMatched
```

A conjunct built entirely from kinds the old switch did not enumerate produced
ZERO callbacks and returned a **vacuous true**. For `extraInScans` that vacuous
true ADMITS the conjunct into `MultiHashJoin.Filters`, where it is evaluated on
the MHJ output row; for `allColumnRefNamesInScope` and
`pushOuterQualsIntoLaterals` it authorises a push. D3 predetermined the
inversion — "plan slots signal, and the caller must treat *an opaque child
exists* as not matched" — before any code was written.

Non-total means: an inner-scope plan was crossed, an unenumerated type was met,
or the expression reads row data **without naming the column it reads**
(`*OuterColumnRef`, `*CTIDExpr`, `*MergeWholeRowRef`, and a `*ColumnRef` whose
`Name` is empty — `Name` is "for diagnostics" per its own struct comment and IS
empty on some construction paths, which the old body skipped silently).
`*ParamRef` / `*ExecParamRef` / `*TableOidExpr` / `*MergeActionExpr` stay total:
they read no row column.

## Measurement

| instrument | reading |
|---|---|
| TPC-H plan A/B (`plan_snapshots/m0125-0002-c7-{before,after}.txt`) | **22/22 byte-identical** |
| TPC-H cumulative vs `post-mhj-retire` and vs `m0125-0002-c3-before` | **byte-identical** — the whole series moved no TPC-H plan |
| TPC-DS SF0.5 EXPLAIN A/B (`before/` vs `after/`) | 95/96, one apparent hunk at **Q85** |
| SF0.5 repeat runs (`before2/`, `after2/`) | **Q85 hunk REFUTED — see below** |
| divergence probe (`probe.server.log`, `probe-source.md`) | **0 verdict deltas** at all three call sites while planning Q85 |
| planning-time A/B (`plantime-ab.txt`) | +2.9 % total over 22 queries, inside the instrument's resolution |
| pins (`pin-proof.txt`, `oldbody-harness.md`) | 18 pins proved to FAIL against the old body first |

### The Q85 hunk is the instrument, not the change

The first A/B showed TPC-DS Q85 with `cd1` and `cd2` (the two
`customer_demographics` aliases of its self-join) exchanged between two join
positions of otherwise identical cost and shape. Four checks refuted the
attribution:

1. `before` vs `before2` (same binary, two full sweeps): **96/96 identical**.
2. `after2` vs `before`: **96/96 identical** — the second run of the *after*
   binary reproduces the *before* plan set exactly.
3. `after2` vs `after`: 95/96, differing only at Q85 — i.e. the same binary
   produced both orderings.
4. Three fresh single-query server starts per binary: **both binaries** produced
   the `cd2/cd1` ordering all six times.

Plus the probe: while planning Q85 the new and old bodies disagreed on **zero**
verdicts, across only 2 `allColumnRefNamesInScope` calls and 0 `extraInScans`
calls.

So the honest reading of the SF0.5 arm is **96/96 byte-identical**, and Q85 has
a nondeterministic join-order tie-break that both binaries exhibit. It appears
only in the long-lived-server sweep context, not when Q85 is planned alone.
Filed as a deferral-ledger row: the plan-snapshot instrument that accepted
commits 2–7 has a nondeterminism floor, and a single-run A/B at any of those
commits could have attributed this flake to the code under test.

### Why no 22-query execution power run

D4 item 3 asks for one per commit, to catch a regression caused by a moved
plan. Commit 7 moved no plan, and neither did the series: the CUMULATIVE TPC-H
diff against `post-mhj-retire` is byte-empty. An execution run would re-execute
an unchanged plan set, and round-5 §3 puts its noise floor (2–8 % moves
unattributable) wider than any effect it could attribute.

What the eight conversions DID change is the planning code path — a hand switch
became a generic driver that builds an `[]exprSlot` per node visited. That cost
is invisible to a plan diff and would be buried under 20-minute scans in an
execution run, so it was measured head-on instead (`capture-plantime.sh`):
22 queries × 5 sweeps of `EXPLAIN` in one session with `\timing`, median per
query. Total 4.41 → 4.54 ms (+2.9 %, i.e. ~6 µs per query). The *within-arm*
spread is larger than the between-arm delta — Q1 alone ranges 0.15–0.20 ms
inside a single arm — so the reading is "unchanged within resolution", not
"+2.9 %". The execution arm remains owed at milestone level; ledger row.

Two false starts of this instrument are preserved in the script's comments
because both are easy to repeat: a per-query `date +%s%N` around `psql` reports
a flat 4 ms for every query (that is `psql`'s fork+connect floor, ~20× the
planning time), and `tpch.Queries()` carries no trailing semicolon, without
which `psql` never terminates the statement, every `\echo` flushes at scan time
and the whole sweep arrives as ONE query with ONE `Time:` line.

## Reproducing

```bash
go build -o tmp/goopg-c7-before ./cmd/goopg     # at 900990a2
go build -o tmp/goopg-c7-after  ./cmd/goopg     # at this commit

./capture-tpch.sh  m0125-0002-c7-before tmp/goopg-c7-before
./capture-tpch.sh  m0125-0002-c7-after  tmp/goopg-c7-after
diff plan_snapshots/m0125-0002-c7-{before,after}.txt

./capture-plans.sh before tmp/goopg-c7-before   # SF0.5 EXPLAIN, 96 queries
./capture-plans.sh after  tmp/goopg-c7-after
./capture-plans.sh after2 tmp/goopg-c7-after    # the run that refuted Q85

# capture-plantime.sh reads tmp/c7-tpch-queries/qN.sql; regenerate with a
# 6-line main() that writes internal/testutil/tpch.Queries() out to files.
./capture-plantime.sh before tmp/goopg-c7-before 5
./capture-plantime.sh after  tmp/goopg-c7-after  5
```

## Files

- `capture-tpch.sh`, `capture-plans.sh` — c6's instruments, retargeted.
- `capture-plantime.sh` — new; the planning-cost A/B.
- `before/`, `after/`, `before2/`, `after2/` — SF0.5 EXPLAIN, 96 queries each.
- `sf05-q85.diff`, `tpch-ab.diff` (empty) — the diffs.
- `plantime-{before,after}.txt`, `plantime-ab.txt`.
- `probe-source.md`, `probe.server.log` — the divergence probe.
- `oldbody-harness.md`, `pin-proof.txt` — the "prove the pin fails first" run.
