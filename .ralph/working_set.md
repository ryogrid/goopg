(idle — nothing in flight)

## Loop #1 (2026-09-01) result — M0134-0180 (tsrf.sql) sized & PARKED; LIMIT/OFFSET SRF check landed as `8519d98b7`

**Nightly triage:** `ci/logs/action-items.md` run `20260831-013952` (2 new
items) filed as unconditional obligation, NOT selected (banner still names
M0134): `AI-...-001` testport isolation-suite timeout (17 specs stuck
`[running]` 1h56m, one blocked on a `lib/pq` conn stuck in IO wait — needs a
clean-tree confirmation run before trusting it as a real hang, since the
run's `meta.json` shows 18 dirty files incl. a concurrently-modified
`postgres` submodule) and `AI-...-002` build-broke-mid-stage (confirmed
stale — `go build ./...` clean at HEAD). Both rows in fix_plan.md's
M-NIGHTLY section.

**Task:** M0134-0180 — `tsrf.sql`. **PARKED** (CSV `not-tried` → `failed`),
793→785 diff lines. Design `docs/design/0100-0149/m0134-0180-tsrf-sizing.md`
(landed by a CONCURRENT session's doc-reorg commit `17e695d08` — see note 4
below — not by my own commit `8519d98b7`, which is why it isn't in that
commit's file list).

**Landed:** `exprHasSRF` walker (`internal/parser/analyzer/analyzer.go`,
mirrors `exprHasWindowFunc`) rejects a set-returning function in LIMIT/OFFSET
(SQLSTATE 0A000) instead of silently evaluating it via the executor's
`generate_series`-as-scalar fallback (wrong single-row result). Guards:
`TestAnalyzeSRFInLimitOffsetRejected`, `TestAnalyzeLimitOffsetNonSRFStillAccepted`.

**Four things worth carrying:**

1. **`tsrf.sql` is ~10 independent placement rules, not one gap.** Several
   already matched the oracle byte-for-byte with ZERO code change: sibling-SRF
   lockstep zipping (`operators_project_set.go: openSelectSrfMode` already
   NULL-pads to maxLen correctly), post-aggregation SRF timing when not
   GROUP-BY-referenced, DISTINCT ON placement, GROUP BY CUBE+SRF. Don't assume
   a `failed` case needs its dominant gap fixed — verify each assertion
   independently first (an `Explore` subagent sweep is the efficient way).
2. **Three REFACTOR-tier gaps ledgered, NOT attempted**: `0180a` six more
   "SRF not allowed in X" contexts (CASE/COALESCE/aggregate-arg/window-arg/
   UPDATE-SET/RETURNING/VALUES — aggregate/window-arg has ZERO existing
   precedent in goopg, needs a `parse_agg.c`-style sublevels-up walker);
   `0180b` nested-SRF-as-another-SRF's-own-argument (`generate_series(1,
   generate_series(1,3))` → 1 row not 6) AND GROUP-BY-referencing-a-
   target-list-SRF (changes pre-aggregation cardinality) — BOTH need the SAME
   primitive, a recursive/stacked ProjectSet, not independent patches;
   `0180c` `|@|` as a user-defined prefix operator fails at the parser's
   closed `{-,+,NOT,~}` prefix set (`select.go:parseUnary`,
   `support.go:prefixOp`) — unrelated to SRFs, same class as the M0134-0179
   closed-`OpCode`-enum follow-up.
3. **`isBuiltinSRF` now has TWO copies** (`internal/executor/
   operators_ddl_partition.go` and the new `builtinSRFNames` in
   `internal/parser/analyzer/analyzer.go`) — unavoidable, executor imports
   analyzer not the reverse. Cross-referenced by comment; keep them in sync
   by hand if either SRF list changes (Hard-won Rule #2).
4. **A concurrent session (PID from `ps`, session-id `3ac4bff9...`) was
   running a 1273-file design-doc reorg (buckets `0000-0049/`, `0050-0099/`,
   `0100-0149/`) IN THE SAME WORKING TREE throughout this loop** and committed
   it (`17e695d08`) mid-loop, between my `git add` of the new design doc +
   README.md edit and my own commit. Because git's index/working tree is
   shared, that commit swept up my ALREADY-STAGED `docs/design/
   0100-0149/m0134-0180-tsrf-sizing.md` and my `docs/design/README.md`
   one-line index-entry edit (both landed correctly, verified post-hoc via
   `git show HEAD:<path>` — the reorg's own link-path rewrite happened to
   match the bucket I'd already chosen). My own commit `8519d98b7` therefore
   does NOT list the design doc or README.md, even though both exist and are
   committed. **If touching `docs/design/README.md` again while another
   session may be live, `git add` it immediately before committing (don't
   leave it staged-but-uncommitted across tool calls) to shrink this race
   window.**

**Gates run:** `go build ./...` clean; `go test ./internal/parser/...`
(incl. `TestParityGoldensAreCurrent`, zero golden diff) / `./internal/
optimizer/...` / `./internal/executor/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12
rows=2 22.7s, Q13 rows=34 6.9s); `make check-testport-inventory` PASS;
`make regen-testport` clean regen; `make ralph-state-guard` PASS. Pre-commit
hook pgbench smoke PASS (10942 TPS select-only).

**NEXT LOOP:** Re-check banner (M0134 still priority as of this writing).
Next unclaimed M0134 case per fix_plan.md ordering, OR pick up ledger row
0180a/0180b/0180c if the banner ever names "M0134 follow-ups" specifically.
Also worth a quick check: did the AI-...-001 testport-hang nightly item get
a clean-tree re-run yet (see M-NIGHTLY section)?

**In-flight:** none.
