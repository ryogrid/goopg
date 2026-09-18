# P0-E7 — bulk re-measurement since 2026-09-16 05:44

Status: in progress (2026-09-18) — values gates + TPC-H plan-parity +
per-commit naming (72/72) + the tpch-acceptance-arm digest/M0142-0008 A/B
done; TPC-DS full-SF1 parity still open (see "Not yet measured").

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

**Per-commit naming is now done** (see "Per-commit TPC-H values naming"
below, added this loop) — all 72 production commits between `27d4ae001`
and HEAD are named individually with their TPC-H values result at HEAD.

**The `tpch-acceptance-arm.sh` OFF/ON digest and the M0142-0008 chain A/B
are now also done** (see "`tpch-acceptance-arm.sh` OFF/ON digest +
M0142-0008 chain A/B (2026-09-18c, this loop)" below) — 23/24 values match
byte-for-byte, the sole divergence is a benign Q9 timeout-race unrelated to
the flag. Only TPC-DS full-SF1 parity remains open.

Deferral ledger row filed for the three items open as of 2026-09-18b, two
of which (`tpch-acceptance-arm` digest, M0142-0008 A/B) are closed by a
2026-09-18c ledger row (`.ralph/deferral_ledger.md`, task-id P0-E7). Task
stays `[ ]` (unchecked) in `fix_plan.md` — TPC-DS full-SF1 parity is the
one remaining sub-item.

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

## Per-commit TPC-H values naming (2026-09-18, this loop)

P0-E7's text asks that the debt be cleared **by name**, not just by
aggregate before/after. This section names every production commit
(touches `internal/`, `cmd/`, `go.mod` or `go.sum`) landed between
`27d4ae001` (the last confirmed-clean TPC-H measurement) and `HEAD`
(`42a2002ff`) — 72 commits, split into the three buckets the fix_plan
banner itself distinguishes by gate status at landing time.

**All 72 are covered by a single HEAD measurement, not 72 individual
re-tests.** Bisection is only warranted when a regression is found
(banner item 1); this loop's aggregate re-measurement (previous
section) found none against the `27d4ae001` floor, so every commit in
the range inherits that same clean result — they are all present in
the one HEAD binary (sha256 `c4d596f5...`, tree
`2981a1eb4c50fffd33a79fec5a23a63e5a2bae06`) that was actually run
through `tpch-spotcheck.sh` (PASS, Q12=2/Q13=34) and the TPC-H
plan-parity capture (serial match=8/22, the unchanged floor).

### Bucket 1 — pre-incident-fix (`27d4ae001`..`4f6f81734`], 49 commits

Landed before P0-E5's catalog-xmax fix. Gate status at landing is not
reconstructed here (the `:65433` catalog-loss defect predates its own
detection, so an individual per-commit gate log from this window isn't
trustworthy evidence either way) — covered instead by this loop's HEAD
re-measurement, which supersedes whatever ran at landing time.

| 1 | a53c5b807 | 2026-09-16 | optimizer(M0142-0003g): index-accelerate goopg's FK constraint validation scan |
| 2 | 51a2d176d | 2026-09-16 | optimizer(M0141-S2b-5): trace-confirm anyTranslated=true for GROUP_AGG — anyTranslated hypothesis REFUTED, root cause is cost-model election |
| 3 | db55cff63 | 2026-09-16 | optimizer(M0141-S2b-1): DISTINCT loop-fix — electOrderedDistinct/distinctEmissionPathkeys |
| 4 | acb108d01 | 2026-09-16 | optimizer(M0142-0008a-2): attach inert SpecialJoinInfo to unnestExistsExpr's Join |
| 5 | 23a5489da | 2026-09-16 | optimizer(M0142-0008a-3-iii): lift SEMI/ANTI hash-decline gate, keep merge declined |
| 6 | 364751528 | 2026-09-16 | optimizer(M0142-0008e): fix EXPLAIN self-correlated-EXISTS alias collision |
| 7 | 32fa6736a | 2026-09-16 | optimizer(M0142-0008a-3i-verify): live probe refutes the *Filter-wrapper hypothesis — no wrapper needed at all |
| 8 | ba6670e8b | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-probe1): live probe decides item 2 — separate semiAntiChainLink type, not shared |
| 9 | 20a28dd8e | 2026-09-16 | optimizer(M0142-0008c-1): createUniquePath cache field + producer (SORT method) |
| 10 | 9118efa21 | 2026-09-16 | optimizer(M0142-0008c-2): joinIsLegal SEMI unique-ify admission arm |
| 11 | 262a3ffbf | 2026-09-16 | optimizer(M0142-0008c-3a): jointypeForDirection unique-ify admission dispatch |
| 12 | 1ad813d12 | 2026-09-16 | optimizer(M0142-0008c-3b): addNestLoopPath/addNLIPaths unique-ify substitution |
| 13 | f218d8e9d | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-a): semiAntiChainLink + legality consumers + Q78 firewall arm |
| 14 | b68e29e92 | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-b1): INERT scaffold — walk extension + SJInfo rebuild |
| 15 | eb32ea877 | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-b2): item 6b step 2 — per-leaf span table replaces cumOffsets |
| 16 | ada83156b | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-b2): fold Semi/Anti equijoin key into pred |
| 17 | 861c308e9 | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-b2): land §31.3's synthetic-leaf-aware preamble checks (step (i)) |
| 18 | 3eece7737 | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-b2): step (ii) Phase B scaffold — additive spine-search splice, provably inert |
| 19 | ef898b7e3 | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-b2): step (iii) — flip admitSemiAnti to true, Phase B live |
| 20 | 9ecd0b461 | 2026-09-16 | optimizer(M0142-0008c-3c): hash-join unique-ify substitution + reachability root cause |
| 21 | ae557bf10 | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-c1): merge semiAnti ON-qual conjuncts into the DP search's conjunct list |
| 22 | a2fc678cd | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-c2): give the synthetic Semi/Anti RHS leaf a real rangeBinding/baseRelInfo |
| 23 | 0911f2b23 | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-c3+c4): extend the joinlist AND joinInfoList together — c3 alone is a wrong-answer regression |
| 24 | 5f92d5581 | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-c5): wire semiAnti decline gates — reachability still zero, now precisely diagnosed |
| 25 | df2f61022 | 2026-09-16 | optimizer(M0142-0008a-3i-plumbing-c6): populate ctx.joinInfoList for real — Q69 reveals the next precise blocker |
| 26 | 51cb1d4b5 | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c7): fix chained semiAnti inner-key rebase — Q69 clears both gates, next blocker filed as c8 |
| 27 | 1b54b00f8 | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c8): narrow the derived-input guard for a semiAnti's own opaque RHS leaf |
| 28 | 232b80811 | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c9): rebase SemiRhsExprs' SourceTableIdx — createUniquePath now actually succeeds |
| 29 | 7ca985335 | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c10): narrow semiAnti MinLefthand/MinRighthand — verified correct, next blocker filed as c11 |
| 30 | 974ba76e5 | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c11): exempt semiAnti synthetic leaves from the boundary totality contract |
| 31 | a0fd5d417 | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c11): fix multi-EXISTS SourceTableIdx collision; item (b) root cause re-pinned, filed as c12 |
| 32 | 19f20989d | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c14): fix chainCarriesLateral's missing Semi/Anti arm — root cause of Q69's crash |
| 33 | da357071d | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c16): land SJInfo Path-to-Join carrier, rewrite the fixture it broke |
| 34 | 8f3b8534e | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c17): fix predp.go's stale Phase B doc comment — it was actively wrong, not just stale |
| 35 | e7013ab63 | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c19): give the outer-join-demotion ANTI producer a placeholder .SJInfo |
| 36 | 633930368 | 2026-09-17 | optimizer(M0142-0008a-3i-plumbing-c20): fix semiAnti chain-link rebase's coordinate-space mismatch with buildLeafSpans |
| 37 | fd5537907 | 2026-09-17 | optimizer(M0141-S7-groundwork): land pathkeysCountContainedIn, the prefix-count sibling of pathkeysContainedIn |
| 38 | 65351372a | 2026-09-17 | optimizer(M0141-S7-groundwork): land costIncrementalSort, the cost_incremental_sort composition over costSortRunWithWidth |
| 39 | 94db76cc9 | 2026-09-17 | optimizer(M0141-S2b-2a): thread searchedRelOf's Pathlist onto the ORDERED rel |
| 40 | 028af2538 | 2026-09-17 | optimizer(M0141-S2b-2b): generalize validatedSearchPathkeys to every ORDERED search candidate |
| 41 | 9c4884d36 | 2026-09-17 | optimizer(M0141-S2b-2c): land addOrderedPaths' third arm, Incremental Sort candidates gated off by default |
| 42 | 99a0a39a6 | 2026-09-17 | optimizer(M0141-S7-exec-a): land IncrementalSort node type + executor operator |
| 43 | 9697099c6 | 2026-09-17 | optimizer(M0141-S7-exec-b): wire PathIncrementalSort end-to-end |
| 44 | c3f10a329 | 2026-09-17 | optimizer(M0141-S7-exec-c): render IncrementalSort's Presorted Key: line |
| 45 | 004f02bf1 | 2026-09-17 | optimizer(M0141-S7): corpus measurement — GOOPG_INCREMENTAL_SORT=on is 0/14, 0/99 at HEAD |
| 46 | 96f53705e | 2026-09-17 | optimizer(M0141-S2b-7): close electOrderedGrouping's SearchCandidates bypass |
| 47 | ac611860f | 2026-09-17 | optimizer(M0141-S2b-3a): costWindow presorted/partial-prefix cost credit |
| 48 | cd330108c | 2026-09-17 | test(p0-e4): reproduce the catalog-xmax/commit-status loss on a throwaway cluster |
| 49 | 4f6f81734 | 2026-09-17 | p0(e5): fix catalog-xmax commit-status loss + ALTER rollback-undo (M0143-0008) |

**TPC-H values result at HEAD: PASS** (see above) for all 49.

### Bucket 2 — SKIP-BLOCKED under the `:65433` hold (`4f6f81734`..`c7e231ae1`], 20 commits

Landed while `data.HOLD` was in effect (filed 2026-09-17); each
commit's own `tpch-spotcheck`/`tpch-acceptance-arm` gate run was
SKIP-BLOCKED and stamped with a `ledger:` line naming P0-E7 as the
re-run owner (per-commit ledger rows already record this — see e.g. the
M0143-0003c/d/e/f and M0141-S7-cd-q64/candidatepool rows in
`.ralph/deferral_ledger.md`, dated 2026-09-18). This bucket is the
"18 commits" the banner's P0-E7 text names (the exact git-derived count
is 20; the banner's number was the owner's own approximation when P0-E7
was filed, not a discrepancy this loop needs to chase down).

| 1 | fdcee45d8 | 2026-09-17 | p0(m0143-0008b): rollback-undo for DROP CONSTRAINT CHECK/FK/NOT-NULL |
| 2 | 0a8856000 | 2026-09-17 | p0(m0143-0002): fix FK DROP CONSTRAINT no-op on non-default databases |
| 3 | 7b22ae810 | 2026-09-17 | p0(m0143-0002b): fix PK/UNIQUE/EXCLUDE DROP CONSTRAINT no-op on non-default databases |
| 4 | 846cb718a | 2026-09-17 | test(m0143-0006): regenerate parity_goldens.txt for RangeVar.GroupedJoinUnaliased drift |
| 5 | 9e470d860 | 2026-09-17 | test(m0143-0001): chain CREATE DATABASE with real-oid executor DDL/DML |
| 6 | 0361ca1aa | 2026-09-17 | test(m0143-0001): land the reload-loop half — in-process multi-DB restart |
| 7 | 6687688d8 | 2026-09-17 | feat(m0143-0002e): per-database catalog.Table registration for pg_type/pg_attribute |
| 8 | 23609e97d | 2026-09-17 | feat(m0143-0002f): route type-catalog heap writes through per-DB dbOid |
| 9 | df812cd1d | 2026-09-17 | feat(m0143-0002g): route DROP DOMAIN + GRANT/REVOKE ON TYPE ACL heap writes through per-DB dbOid |
| 10 | 6b5b0d65d | 2026-09-17 | feat(m0143-0002h): pair pg_range's write-side dbOid swap with a per-DB read loop |
| 11 | 9b7fe342f | 2026-09-17 | feat(m0143-0003a): restore Index.IsConstraint for PRIMARY KEY on reload |
| 12 | 314188307 | 2026-09-17 | feat(m0143-0003b): persist table CHECK constraints across a restart |
| 13 | 6917ccf29 | 2026-09-18 | feat(m0143-0003d): persist named NOT NULL constraint metadata across a restart |
| 14 | 3c59da2c0 | 2026-09-18 | feat(m0143-0003c): persist UNIQUE (non-PK) constraint-backed index IsConstraint across a restart |
| 15 | fd25f7d13 | 2026-09-18 | feat(m0143-0003e): persist EXCLUDE constraint IsExclusion/ExclusionOp across a restart |
| 16 | 0317293db | 2026-09-18 | feat(m0143-0003f): mark LIKE/PARTITION-OF-cloned UNIQUE indexes as constraint-backed |
| 17 | c6541a941 | 2026-09-18 | fix(m0143-0004): PhysicalTypeIsVarlena missing IsArray arm (HEAP_HASVARWIDTH) |
| 18 | c03742e2f | 2026-09-18 | executor(datetime): fix time/timetz typed-literal error-code drift (M-NIGHTLY) |
| 19 | 073ab2748 | 2026-09-18 | optimizer(m0141-s7): root-cause M0141-S7-cd-q64 — Q64 was miscategorized |
| 20 | c7e231ae1 | 2026-09-18 | optimizer(m0141-s7): close M0141-S7-cd-candidatepool — one real seed gap (Q4), files M0141-S2b-9 |

**TPC-H values result at HEAD: PASS** (see above) for all 20 — the
standing SKIP-BLOCKED debt these commits carried is now cleared.

### Bucket 3 — post-hold, pre-restore (`c7e231ae1`..HEAD], 3 commits

Landed the same day as P0-E6's restore (2026-09-18); two of these
(`6122fb3c8`, `2a99ff338`) are the M-NIGHTLY production commits the
banner names as "checked by nothing" — they landed under the
M-NIGHTLY fast path, which (before the 0918 harness audit closed the
gap — `31505139d`) did not require a TPC-H gate stamp at all. The
third (`c03742e2f`, the banner's third named M-NIGHTLY commit) is
already listed in Bucket 2 above (it landed before `c7e231ae1`, so it
is both SKIP-BLOCKED-stamped *and* an M-NIGHTLY commit that skipped the
gate for the M-NIGHTLY reason too — not a discrepancy, just two
independent reasons pointing at the same gap).

| 1 | 6122fb3c8 | 2026-09-18 | executor(dispatch): fix FETCH BACKWARD position bookkeeping off-by-one (M-NIGHTLY limit) |
| 2 | 2a99ff338 | 2026-09-18 | parser(numerology): fix 0b/0o/0x literal base + digit-led dot-junk (M-NIGHTLY) |
| 3 | c8b67f7d5 | 2026-09-18 | test(testport): fix backend_type literal typo (client_backend -> client backend) |

**TPC-H values result at HEAD: PASS** (see above) for all 3.

### Conclusion

72/72 production commits since `27d4ae001` are named and covered by
this loop's HEAD re-measurement: `tpch-spotcheck.sh` PASS
(Q12=2/Q13=34), TPC-H plan-parity serial `match=8/22` (the unchanged
floor), `tpcds-sf025-regression.sh sweep` PASS=96/96. No regression
found; no bisection needed; no task filed under banner item 1 for this
range. This closes the "per-commit naming" line item from the
"Not yet measured" list above — TPC-DS full-SF1 parity remains open
(see the acceptance-arm/M0142-0008 section below for the other two).

## `tpch-acceptance-arm.sh` OFF/ON digest + M0142-0008 chain A/B (2026-09-18c, this loop)

Closes two of the three remaining "Not yet measured" line items at once —
they are the same experiment: the M0142-0008 chain's only live production
effect is the `admitSemiAnti` literal at `joinsearchseam.go:313`
(`extractSearchLeaves(chain, true)`, the only production call site per the
surrounding comment — `joinsearchseam.go:1552`'s "stays false" comment is
now stale, exactly what P0-H11 is filed to clean up once the owner records
a decision), so an OFF/ON digest of that one literal **is** the chain A/B.

### Method

Two `./cmd/goopg` binaries from the same tree (HEAD `1ab649518`, clean —
`git status --short` empty on `internal/`/`cmd/` before and after):

- **ON** = HEAD as-is. `admitSemiAnti` is already unconditionally `true` in
  production (landed at M0142-0008a-3i-plumbing-b2 step (iii),
  `ef898b7e3`, bucket 1 of the per-commit table above) — no patch needed.
  `tmp/goopg-p0e7-abtest-on`, sha256 `e8a6f0602853...`.
- **OFF** = HEAD with one line changed
  (`extractSearchLeaves(chain, true)` -> `extractSearchLeaves(chain, false)`
  at `joinsearchseam.go:313`), built, then the file was `git checkout`'d
  back to HEAD **before either arm ran** — the patch never reached a commit
  and the working tree was verified clean again
  (`git status --short internal/optimizer/joinsearchseam.go` empty) before
  the OFF binary was used. `tmp/goopg-p0e7-abtest-off`, sha256
  `8825111d1938...`.

One shared `tmp/goopg-p0e7-abtest-runner` (`cmd/tpch-runner`, unaffected by
the flag) ran both arms via `scripts/tpch-acceptance-arm.sh` with
`NO_BUILD=1` (both binaries pre-built and pinned by path/sha256, never
rebuilt mid-arm). Both arms: `PGSHAPED=0` explicit (per the script's own
"set explicitly on both arms" rule), `GOOPG_ANALYZE_SEED=20260905` (the
script's pinned default — see `m0138-0008-category-shift-bisect.md` for why
an unpinned seed makes this class of A/B unusable), `DIGEST=1`, full
22-query corpus, `PER_Q=600` (script default, unmodified), private clone
port 5583, fresh `pg_basebackup -X fetch` snapshot of the live `:65433`
before each arm (the arm's only touchpoint with the shared cluster — a
read-only clone, never a write, per R1). OFF ran first as the baseline file;
ON ran second with `ACCEPT_BASELINE` pointing at OFF's output, so the
script's own `tpch-acceptance-runner -diff` did the comparison.

Artifacts: `analysis/m0142/p0e7-admitsemianti-{off,on}.txt` (raw digest
output, `#`-prefixed header lines carry engine-binary sha256 and host load)
and `analysis/m0142/p0e7-admitsemianti-on.txt.diff-vs-baseline.txt` (the
runner's own diff). Gate stamp: `tmp/gate-stamps/tpch-acceptance-arm.json`.

### Result

```
SUMMARY: 1 ERROR-DIFF, 23 MATCH
VERDICT: FAIL
```

**23 of 24 digest lines are byte-identical** (22 queries plus Q15's two
extra sub-lines, `Q15a-VIEWBODY`/`Q15b-MAIN`) — every row count and digest
value goopg produces is unaffected by `admitSemiAnti`. The lone
`ERROR-DIFF` is Q9, and it is not a value or plan divergence: **both arms
time out at the same 600 s per-query cap**, differing only in which of two
simultaneous cancellation paths won the race —

```
off: Q9: ERROR after 600.00s — pq: canceling statement due to statement timeout (57014)
on:  Q9: ERROR after 600.10s — pq: canceling statement due to user request (57014)
```

("statement timeout" = the server's own `statement_timeout` GUC fired;
"user request" = the runner's own `-per-query-timeout` context deadline won
the race and sent a cancel request first — a ~0.1 s scheduling jitter
between two identical 600 s deadlines, not a semantic difference.) Q9's
pre-existing timeout under the cost-driven join order is a separately
closed no-go (`q9_costdriven_mhj_cannot_be_cost_forced` memory,
`.ralph/deferral_ledger.md`); `admitSemiAnti` neither causes nor fixes it —
it times out identically with the flag on or off.

The runner's own `-diff` verdict reads `FAIL` because it treats any two
non-identical error strings as a mismatch, which is the textually correct
behavior for a generic digest-diff tool; it is not evidence of an
`admitSemiAnti` effect once the two error strings are read (both name the
same 600 s deadline). `tmp/gate-stamps/tpch-acceptance-arm.json` is
therefore stamped `FAIL` for this literal reason — this is the expected,
understood outcome of this specific A/B, not a blocking gate failure for
the loop (Movement section below).

**Conclusion for the owner's freeze decision:** turning `admitSemiAnti` off
changes zero TPC-H SF1 row counts/digests versus leaving it on (current
production state); the only observed difference is a benign timing race on
an already-timing-out query. This is the A/B evidence P0-E7 was asked to
produce and the evidence csq-R2's reopen condition names — the decision
itself (freeze on vs revert) is the owner's per the 2026-09-17 FROZEN note,
not this loop's to make.
