# P0-E7 — bulk re-measurement since 2026-09-16 05:44

Status: in progress (2026-09-18) — values gates + TPC-H plan-parity done;
TPC-DS full parity, tpch-acceptance-arm digest and the M0142-0008 A/B still
open (see "Remaining scope").

## Context

`.ralph/fix_plan.md` banner item 0 names P0-E7 as "bulk re-measurement of
everything landed since 2026-09-16 05:44" — the debt from the `:65433`
catalog-loss incident (see `p0-e4-catalog-xmax-loss-repro.md`) plus 18
commits between `4f6f81734` and `c7e231ae1` that landed on a SKIP-BLOCKED
`tpch-spotcheck` stamp, plus three M-NIGHTLY production commits checked by
nothing (`c03742e2f`, `6122fb3c8`, `2a99ff338`). P0-E6 restored `:65433`
2026-09-18 (owner-run), releasing the standing SKIP-BLOCKED exception, so
this task became selectable.

## R1-safety correction to the M0137-0003 baseline-capture procedure

`docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md` (accepted
2026-09-15) tells the TPC-H capture recipe to point `estimate-audit
-ref-port` directly at `:65432`. That is now unsafe: `estimate-audit`'s
`session.ensure` runs a bare `ANALYZE <table>` on **every** connection it
opens (`cmd/estimate-audit/main.go:371-377`) when `-warm-stats` (default
`true`) is set, including the reference connection opened by
`referenceReports` -> `capture(f, f.refPort, ...)`
(`cmd/estimate-audit/main.go:244`) — i.e. the documented one-invocation
recipe issues `ANALYZE` against `:65432` every time. AGENT.md's Plan-parity
harness R1 (hardened 2026-09-17, after the M0137-0003 doc was accepted) now
forbids `ANALYZE` on any reference cluster (`:65432`/`:65433`/`:65438`)
outright. The two dates place the harness rule strictly after the doc's
acceptance, so the doc is stale, not this task's approach.

**Fix used here: two separate invocations, never one.**
1. goopg side: private clone of `bench/tpch/runtime_goopg/data` onto a
   `55xx` port (`scripts/lib/tpch-private-clone.sh`, same mechanism
   `tpch-spotcheck.sh`/`tpch-acceptance-arm.sh` already use), binary built
   fresh from HEAD, `-warm-stats=true` (goopg's stats are per-connection —
   this ANALYZE runs against the *private clone*, not the shared cluster).
2. PG reference side: a **second** `estimate-audit` invocation with `-port`
   (not `-ref-port`) pointed directly at `:65432`, `-warm-stats=false`
   (PostgreSQL's stats are global and persistent — the reference cluster
   was already `ANALYZE`d when its data was loaded, so skipping it here
   only skips a redundant write, per the tool's own comment at
   `main.go:200-202`) and `-plan-only` (so the session issues bare
   `EXPLAIN`, never `EXPLAIN ANALYZE`, which would *execute* the query — a
   read either way, but plan-only keeps the reference session's footprint
   to what R1 explicitly allows). `capture()` is engine-agnostic and always
   emits the same `=== Qn` plans-file format regardless of which port it
   talked to, so the resulting file is a drop-in `pg` argument for
   `pg-plan-parity-diff.py`.

Driver used: `tmp/p0e7-tpch-parity-capture.sh` (scratch, not committed —
`tmp/` is gitignored) for the goopg-side half; the PG-side half is a second
bare `estimate-audit` invocation with no wrapper needed (it never starts or
stops anything). Neither half stops, starts, resets or writes to
`:65432`/`:65433` themselves — the goopg half's `pg_basebackup -X fetch`
clone reads from the live `:65433` (a `CHECKPOINT`-class load on the
*source*, not a write to its data — the same operation `tpch-spotcheck.sh`
already performs routinely against the same cluster).

Binary: `tmp/goopg-p0e7-bin`, sha256
`c4d596f557eb6a1b8131508d1c221d6e3a9ac07f1fb9d51c1a63b6fe0b94f091`, built
from tree `42a2002ffed18c25c5c87c3944fa4b4575f61b3a` (HEAD at capture time;
`080323cbd`..HEAD touches no `internal/`/`cmd/`/`go.mod`/`go.sum` file, so
this binary is also byte-equivalent in behaviour to the one P0-E6 already
validated with `tpch-spotcheck` — confirmed identical `binary_sha256` in
both `tmp/gate-stamps/tpch-spotcheck.json` and `tpcds-sf025.json`, both
timestamped this loop).

## Results

### Values gates (private-lane, HEAD binary)

- `scripts/tpch-spotcheck.sh`: **PASS** — Q12=2 (expected 2), Q13=34
  (expected 34), query-phase wall clock 10.3s, peak scope memory 9673 MB.
- `scripts/tpcds-sf025-regression.sh sweep`: **PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3** (Q36/Q70/Q86 dsqgen-artifact
  skips, pre-existing). Plan-shape channel vs the previous sweep
  (`e5046bc31` -> `42a2002ff`): `queries=99 same=99 changed=0 added=0
  removed=0`. Status-delta channel: one runtime move (Q8 2s->5s, 2.5x,
  non-blocking, aggregate +4.2%), zero verdict changes.

Both gate stamps recorded at `tmp/gate-stamps/{tpch-spotcheck,tpcds-sf025}.json`,
tree `2981a1eb4c50fffd33a79fec5a23a63e5a2bae06`, `dirty_code: false`,
`binary_sha256: c4d596f5...` (matches this doc's binary above).

### TPC-H plan-parity vs PG 18.3 (estimate-audit -plan-only, private lane)

Captures: `analysis/m0137/p0e7-{goopg,pg}-{serial,parallel}.plans.txt`
(`.txt` siblings hold the `# stats-epoch:` header + tool banner).

**Serial** (`max_parallel_workers_per_gather=0` both sides — the corpus the
`match >= 8` floor is defined over):

```
PLAN-PARITY: queries=22 match=8 shapediff=12 unparsed=0 missingnode=2 error=0 timeout=0
CATEGORIES: join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8 sort-strategy=6 parallelism=0 qual-placement=4 rendering=2
CATEGORIES-EXCL-MATCH: join-order=12 join-method=10 scan-type=9 parameterisation=5 aggregation-strategy=8 sort-strategy=6 parallelism=0 qual-placement=4 rendering=1
```

`match=8` is **exactly** the floor and exactly the last-measured value at
`27d4ae001` ("TPC-H after-vs-PG match=8/22 against the floor of 6" — the
banner's own floor line has since been raised to 8, i.e. this reading holds
the *higher* current floor too). **No regression** across the 35 production
commits landed since `27d4ae001` (2026-09-16 04:18) through HEAD
(`42a2002ff`, 2026-09-18), as far as the serial TPC-H corpus measures it.

**Parallel** (`-serial=false` both sides): `match=3/22`. Per
`m0137-0003`'s own caveat (still valid — only the direct-cluster-access half
of that doc is stale), a `-plan-only` parallel reading is not comparable to
a floor or to a prior "real" (non-serial) reading; recorded here for the
record, not as a regression signal. No prior same-methodology parallel
`-plan-only` capture exists to diff against.

### Not yet measured (this loop's scope cutoff)

- **TPC-DS full-SF1 parity** (goopg-vs-`:65438`, the `match >= 2` (Q9, Q41)
  floor) — `scripts/capture-tpcds.sh` + `pg-plan-parity-diff.py`. Not run
  this loop: a full SF=1 sweep is a documented 4-5 hour operation
  (`scripts/tpcds-sf025-regression.sh`'s own header) and did not fit this
  loop's budget after the TPC-H captures above. The SF0.25 fast-regression
  gate (previous section) already gives strong indirect evidence of no
  regression — 99/99 plan shapes byte-identical to the last sweep — but it
  is not a substitute for the SF1 goopg-vs-PG parity floor check, which
  compares against a *different* engine's plans, not against goopg's own
  prior run.
- **`scripts/tpch-acceptance-arm.sh`** OFF/ON digest comparison (values,
  not just plan shape) for the M0142-0008 chain.
- **A/B the M0142-0008 chain** (`admitSemiAnti` on vs off) for the owner's
  freeze decision — this is also csq-R2's reopen-condition evidence.
- **Per-commit naming** of the 35 production commits between `27d4ae001`
  and HEAD with each one's individual TPC-H values result (P0-E7's text
  asks for this to "clear the debt" commit-by-commit, not just an
  aggregate before/after).

Deferral ledger row filed for all four (`.ralph/deferral_ledger.md`,
task-id P0-E7, dated 2026-09-18). Task stays `[ ]` (unchecked) in
`fix_plan.md` — this loop's contribution is partial.

## Gates run this loop

`go build ./...` clean (before and after — no production file touched).
`tpch-spotcheck.sh` PASS, `tpcds-sf025-regression.sh sweep` PASS (both
above). `pg-plan-parity-diff.py` on both TPC-H arms (report-only, no pass/
fail semantics of its own — the floor comparison is manual, per G3/D2).
No `internal/`/`cmd/`/`go.mod`/`go.sum` file changed this loop, so no G2
commit-msg stamp requirement applies (D5: `analysis(...)`/`docs(...)` commit).

## Movement

`Movement: none` — this is a recon/measurement task (Kind: impl per the
banner's pre-existing classification, but this loop's own diff touches no
production file); no PG-match count changed, no `CATEGORIES-EXCL-MATCH`
category moved by this loop's own doing (the numbers above are a
*measurement* of the pre-existing state across the 35 commits, not a
change this loop made to it).
