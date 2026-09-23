# Working Set — ralph2 loop #1 end-state

Task: M0145-0007 slice 5 (`fillJoinHashKeys` fold) → adjudicated by
measurement; M0145-0007 CLOSED `[x]` (docs/bookkeeping, no product code).

Files:
- `docs/design/0100-0149/m0145-0007-single-path-node-lowering.md` — Status
  CLOSED, slice table row 5, new section "Slice 5 RE-ADJUDICATED".
- `docs/design/0100-0149/m0145-0007-hashkeyprobe.patch` — the throwaway probe
  (`git apply`, `GOOPG_HASHKEY_PROBE=1`, HASHKEYPROBE stderr lines).
- `docs/design/README.md` — m0145-0007 row → CLOSED.
- `.ralph/fix_plan.md` — 0007 `[x]` + slice-5 bullet; new `[ ]` M0145-0026.
- `.ralph/deferral_ledger.md` — row: late pass alive for non-lowering joins.

Key symbols: `fillJoinHashKeys`/`fillOneJoinHashKeys` (join_hash_keys.go),
`createHashJoinPlan`/`createMergeJoinPlan` (createplanjoin.go, already set
HashKeys), non-lowering builders unnest.go (SEMI :3739-ish, INNER :2777-ish),
planner.go FULL merge (~:4347), pushdown.go CROSS→hash promotion.

Findings: 0 structural key drift on 670 lowering-built joins; 5 Q78 ANTI
joins carry a duplicate pair in the PATH key list (→ M0145-0026); late pass
still needed for 33/51 (SF0.25 knob/default) + 6 (TPC-H) non-lowering joins.
TRAP: `jointree-parity-capture.sh tpch` defaults PGSHAPED=0 — pass PGSHAPED=1.

Next step: re-read the banner. Item-3 chain after 0007 → M0145-0008
(cutover; check its prerequisite text now that 0007 is `[x]`), then 0010,
0024, 0025, new 0026 (recon).

Gates run: none needed for product code (probe reverted; `go build
./internal/optimizer` clean on the reverted tree). Pre-commit pgbench hook
on commit.

In-flight: none.
