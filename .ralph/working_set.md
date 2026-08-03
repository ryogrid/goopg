(idle — nothing in flight)

M0127-P4.2 is CLOSED and committed. **S4 is OPEN; P4.3 is next.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P4.3` (`Materialize` operator: plan node +
path + rescan replay, memory→spill; NL joins stream the outer with the
inner under Materialize; delete drain-both `runNestedLoop` buffering and
`concatRows`-per-pair). IMPLEMENTATION-TODO P4.3; 07 §4. Bar: UNITS +
SPOT + DS05.**

Carry-over facts a next loop should not re-derive:

- **P4.1's ledger row #3 schedules mark/restore WITH P4.3.** goopg has no
  operator-level `MarkPos`/`RestrPos`; `mergeJoinStream.bufferGroup` and
  P4.3's `Materialize` want the same mechanism. Add the capability
  interface in P4.3, where the first implementor arrives.
- **P4.2's hash fill is LIVE in the executor but GATED in the planner.**
  `GOOPG_HASH_OUTER_JOIN=1` is what makes RIGHT/FULL plan as hash;
  default is still merge. Verified on a live server: FULL → `Hash Join
  (FULL)`, RIGHT → `Hash Join (RIGHT, build=left)`, and both are
  byte-identical to PG 18.3 on a NULL-key fixture.
- **Why the gate exists (do not "fix" it):** ungated, regress `join`
  moves 210 diff lines FURTHER from upstream — pure row ORDER on
  unordered queries. `costInnerMerge` charges an 11-row sort like a real
  one; PG 18.3, asked directly, picks Merge Right/Full Join on
  J1_TBL/J2_TBL. The default flip needs doc 04's cost currency = P5.
- **`GOOPG_RELSIZE_FALLBACK` defaults to stage 2**, so SeqScan leaves
  carry a block-count `EstRelRows` even S-cold — which is why the gated
  cost rule fires on TPC-DS despite no ANALYZE.
- **REGRESS technique (repeat of P4.1's, still correct):**
  `scripts/pg-regress-runner.sh --out-dir <d> <tests>` here and in a HEAD
  worktree, compare with `tail -n +3` + `sed` on the repo path. The
  worktree needs `rmdir postgres && ln -s <main>/postgres postgres` — git
  checks `postgres` out as an EMPTY DIR, so a bare `ln -s` lands inside
  it. `--port` means "already running", not "start here".
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never `gofmt -w`.
  `operators_join_agg.go`, `planner.go`, `joincost_test.go` are already
  gofmt-dirty AT HEAD under the local tool; verify your own added lines
  with `gofmt -d <file> | grep <your symbol>` instead.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified
  except its IMPLEMENTATION-TODO checkboxes.

Gates run this loop: UNITS PASS; REGRESS **zero delta** vs a HEAD-worktree
baseline on join/join_hash/select/subselect/union (all five byte-identical
after path normalisation); DS05 PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0
over all 99 (Q72 TIMEOUT pre-existing) run with `GOOPG_HASH_OUTER_JOIN=1`
— `analysis/leftdeep-joins/2026-08-04-p42-ds05-sweep.txt`; PG-oracle
byte-parity on a FULL/RIGHT NULL-key fixture; SMOKE via the commit hook.
SPOT not run — the default plan shape is unchanged (gate off) and no TPC-H
query has a RIGHT/FULL join. RACE not run — no new shared state (the
bitmap is per-joinOp; the parallel path still refuses RIGHT/FULL).

In-flight: none.
