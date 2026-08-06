(idle — nothing in flight)

Last loop: **M0127 S7 gate DISCHARGED. P6.1 is selectable — take it next.**

Nightly `20260807-004620` (sha `5045ee3b`, 67 min) passed **every** stage —
preflight/units/race/**testport**/pgbench/tpch/tpcds. testport: 1053 s, **zero
`FAIL` lines**, no `suite-wedge`. TPC-H 22/22 ok (Q12=2, Q13=35, Q21 207.9 s);
TPC-DS 99/99 ok; no perf-drastic.

Literal verdict is `status: fail` on **one** item — `regress/truncate`, the
exact spoiler the previous loop pre-registered by name. Re-verified, not
assumed: it is a `SKIP`; it PASSES standalone here (ran it with
`GOOPG_REGRESS_DIFF_DIR` set — zero diffs written); the captured diff is a
single FK `DETAIL:`/`HINT:` pair out of order (Go map iteration in the TRUNCATE
dependency walk). No planner/join/index content ⇒ clean cycle for S5-ON.

Caveat kept on the record: this run's testport compiled 00:46:21, **before**
`bedd50fd` (00:59:25) — so S5-ON is proven clean *without* the root-0041
partial-index guard (stronger, not weaker), and `portals_p2`/`select` passed on
unfixed code, i.e. their nightly manifestation was order/state-dependent even
though root-0041's bug is real and its guards bite.

Files: `.ralph/fix_plan.md` (S7 eleventh amendment = gate MET + P6.1
selectable; truncate item stamped "IT FIRED, carve-out trigger met"),
`docs/design/0127-pg-shaped-join-search.md` §6 (S7-gate row),
`analysis/m0127-p61-fusion-inventory.md` (NEW).

Gates run: `make ralph-state-guard` OK (auto-repaired the stale
completed-marker); pgbench smoke via the commit hook. No code changed this
loop, so no UNITS/SPOT — the nightly *was* the gate.

**NEXT LOOP — M0127-P6.1 (delete fusion).** Inventory already captured in
`analysis/m0127-p61-fusion-inventory.md`, do not re-derive: delete
`internal/executor/fused_hash_join.go` + `fused_hash_join_test.go` whole;
**two** hook sites `executor.go:171` and `:570`; env
`GOOPG_RUNTIME_JOIN_FUSION{,_MIN_LEVELS}` (both default OFF) + the fusion
fields on `env` (keep `inWorker`, it has other readers); then unexport/delete
orphan `planner.IsCanonicalKeyEquality` (`bushy.go:1751` — its only non-comment
callers are the two inside the deleted file). Closes the fused half of the
`markJoinPreserveCTID` row-lock gap **by construction** — cite ledger rows
`2026-08-06 M-NIGHTLY (root-0038)` and `(AI-20260806-011323-001)`; do not add a
walker arm to code being deleted. Bar: grep-clean + UNITS + SPOT. Never run
SPOT while a nightly's tpch/tpcds stages are live.

In-flight: none.
