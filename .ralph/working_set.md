Task: M0142-0003a — "join-search candidate trace for Q9" for the M0142
group. **DONE, committed this loop** (branch `plan-parity-with-pg-take2-ralph`,
commit `592a06505`). Measurement-only recon, no production code changed.

Files: `docs/design/0100-0149/m0142-0003a-q9-joinsearch-candidate-trace.md`
(new design doc), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0142-0003a checked off with findings; M0142-0003b rewritten from an
unscoped placeholder into a concrete precision-bump task now that its fork
is resolved), `.ralph/deferral_ledger.md` (+1 row, task-id `m0142-0003a`).
Scratch artefacts `analysis/m0142/m0142-0003a-q9-{plan,dptrace,dppath}.txt`
committed (trace evidence, not scratch-only — kept as provenance, same
pattern the corpus-capture tasks use for `.plans.txt` outputs).

What was found: the task line proposed building a new env-gated DP-candidate
trace. It turned out one already exists — `GOOPG_PGSHAPED_DP_TRACE=1`
(`internal/optimizer/joinsearchtrace.go`'s `DPTRACE pair/cost` lines,
`internal/optimizer/pathtrace.go`'s `DPPATH` per-PATH partition attribution
— both landed by the prior methodology phase's R53 Step-0/slice-1 rounds,
entered via this task's own citation right). So the task became: build a
throwaway instrumented server (private clone of
`bench/tpch/runtime_goopg/data`, port 5534, `GOOPG_CG_UNIT=m0142-0003a`,
cleaned up after use — the shared `:6543x` bench clusters were never
touched), run Q9 with the trace on, and read the result.
- **Finding 1: PG's topology is fully enumerated at every level (L2-L6),
  never declined.** Rules out the "join-search completeness gap" branch of
  M0142-0003b's fork definitively.
- **Finding 2: at L6, PG's own chain's cheapest candidate
  (`nestloop.index`, +orders onto PG's own L5) renders IDENTICALLY to
  goopg's actual winner (`join.hash`, +nation) at the trace's two-decimal
  precision — both `total=80099.64`** — yet is marked `dominated`. Very
  different per-step marginal costs (PG's spine: cheaper 4-relation base,
  ~33 marginal to close with `orders`; goopg's spine: pricier base, ~2.6
  marginal to close with `nation`) land almost exactly on top of each
  other. Reproduces R53 Step-0's pre-M0138 structural finding (same
  asymmetry) with a far tighter margin (two-decimal tie now vs 2.5% then).
- **Caveat recorded in the doc**: this run's absolute costs are NOT
  byte-comparable to M0142-0001's captured numbers (ANALYZE sampling noise
  on a fresh clone, no seed pin — known issue,
  `goopg_plan_pin_three_drift_sources`). What's load-bearing (topology,
  the qualitative pricing story) reproduces exactly; only the byte-exact
  digits differ.

Key symbols: `internal/optimizer/joinsearchtrace.go` (`DPTRACE`, gate
`GOOPG_PGSHAPED_DP_TRACE`), `internal/optimizer/pathtrace.go`
(`DPPATH`, `formatPathLine`'s `%.2f` total — the precision limit
M0142-0003b needs to lift), `internal/optimizer/path.go:405`
(`Path.OuterRelids`/`InnerRelids`, R53 slice-1's partition-attribution
fields already on `Path`).

Gates run: `go build ./...` clean (no `.go` file touched — pure recon, same
precedent as M0138-0001/-0005/-0006, M0141-S2a, M0142-0001). No `go test`
run for the same reason. No values/plan-gate sweep (no behavior changed).
Pre-commit hook's pgbench smoke: ran and PASSED as part of `git commit`
(0 failed, ~42-144 TPS across the three pgbench arms). `make
ralph-state-guard`: found the same "status=running vs stale
progress=completed" pattern as the last two loops (left over from a prior
loop's clean exit), self-repaired to "in_progress", re-ran and confirmed OK.

In-flight: none. Throwaway server/clone (`/tmp/pp-m0142-0003a`,
`GOOPG_CG_UNIT=m0142-0003a`, port 5534) and the throwaway binary
(`/tmp/goopg-m0142-0003a`) were stopped/removed after use. All shared bench
clusters (`:65432`/`:65433` TPC-H, `:65437`/`:65438` TPC-DS) untouched.

Next step: M0142-0003b is now concretely scoped (rewritten in
`.ralph/fix_plan.md` this loop) — bump `DPPATH`'s `formatPathLine` total
precision (or read `Path.Cost.Total` via a throwaway non-production probe)
to resolve the L6 tie's sign: does goopg's chosen plan genuinely win on
true cost, or does it only win on DP processing/insertion-order tie-break
(goopg's own partition was paired first at L6 per the `DPTRACE pair`
trace)? That answer decides whether the follow-on is a costing-term fix
(B8/B10 named suspects) or a DP tie-break-rule fix — do not assume which
before measuring. Re-read the `## Current Priority` banner fresh next loop
before picking: M0137-M0140 are fully closed, M0141's two live slices
(`M0141-S2a-fix`, `M0141-S2b`) both still need their own scoping pass
before being same-loop-sized (unchanged from prior loops' findings), and
M0142 now has M0142-0003b as its selectable task, alongside M0143 (gated on
nothing). Do not re-run M0142-0001/-0002/-0003a or re-derive Q9's
Finding-1/Finding-2 verdicts — closed facts recorded in this loop's design
doc and ledger row.
