Task: M0139-0007b — port PG's Memoize entry-byte currency (the last sub-task
of the banner's top-priority "M0141-S2a-fix and M0139-0007 — costing-order
unblock" line). **DONE this loop** (`5319e22b7`, committed). New absorption
code (not a re-measurement like 0007a): gave `costMemoizeRescan` a PG-currency
arm behind a new default-off flag, measured, decided HOLD.

Files: `internal/optimizer/memoize_pgentrybytes.go` (new: the
`GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST` switch + ported
`pgMemoizeEntryOverheadBytes`), `internal/optimizer/joinpathsmemoize.go`
(`costMemoizeRescan` gained a `width int` param; call site now passes
`pathWidth(innerPath)`), `internal/optimizer/joinpathsmemoize_test.go` (3
existing call sites updated for the new param), `internal/optimizer/
memoize_pgentrybytes_test.go` (new: 3 tests pinning the two currencies apart),
`internal/optimizer/flaglabels.go` + `scripts/planner-flags.env` (regenerated
via `go run ./cmd/gen-planner-flag-labels`) to name the new flag,
`docs/design/0100-0149/m0139-0007b-memoize-entry-bytes-absorption.md` (new,
full derivation/measurement), `docs/design/README.md` (+1 row),
`.ralph/fix_plan.md` (M0139-0007b checked `[x]`; M0139-0007c filed as the
open follow-up; banner's item-1 marked RESOLVED, pointing the next loop at
item 2); `.ralph/deferral_ledger.md` (+row, task-id `m0139-0007b`),
`analysis/leftdeep-joins/m0139-0007b-memoize-{off,on}.*` +
`analysis/m0139/m0139-0007b-memoize-{off,on}-tpcds-goopg.txt` (6 committed
measurement artefacts).

Key symbols: `pgRelationByteSize`/`pathWidth` (reused verbatim from R113,
`sort_pgrelationbytes.go`/`path.go:691`), `pgMemoizeEntryOverheadBytes` (new
port of `ExecEstimateCacheEntryOverheadBytes`, nodeMemoize.c:1171-1176 —
`48 + 16*tuples` from the actual `MemoizeEntry`(24)/`MemoizeKey`(24)/
`MemoizeTuple`(16) C struct sizes), `costMemoizeRescan` (joinpathsmemoize.go).

Findings: derived the substitution BEFORE measuring (B2 rule 1): term 1
(`relation_byte_size`) and term 2 (`ExecEstimateCacheEntryOverheadBytes`) are
both named PG quantities and got absorbed; term 3 (`get_expr_width` summed
over cache-key exprs) has NO goopg per-expression-width statistic to absorb to
— stays `hashsize.EntryBytes(nkeys,0)` in both currencies, ledgered +
follow-up filed as M0139-0007c (not on the critical path). **Measured:
byte-identical on both corpora.** TPC-H: first diff attempt against an
older committed baseline (`m0141-s2a-fix1-tpch.plans.txt`) showed ~111
modified lines, but ALL were unrelated Seq-Scan/Hash-Join row/cost drift with
different `stats-epoch` stamps — a stale-baseline trap, not a real signal.
Recaptured OFF fresh in the same session/binary and diffed ON vs that: byte-
identical except the header line; the corpus's one Memoize node
(`rows=1 width=490`) prices identically in both arms. TPC-DS SF0.25 (private
clone, ports 5596/5598, never touched shared `:65433`/`:65437`): byte-
identical; 13 Memoize nodes, all `rows=1`. Root cause of the null result
(not a bug): the byte currency only reaches the cost through `evictRatio`,
and every observed candidate's `estCacheEntries` swamps `ndistinct` under a
64MB `work_mem` regardless of which currency computed it — same "arm never
reaches its own memory-constrained regime" shape 0007a found for R108/R113.
**Decision: HOLD, stays default-off** (now the THIRD such arm — AGENT.md's
informal cap is "about four"; this task supplied its own expiry in-loop so it
does not join the debt the cap warns about). **With this, ALL THREE of
M0139-0007's filed pieces (recon, 0007a, 0007b) are resolved**, and the
banner's top-priority line ("M0141-S2a-fix and M0139-0007") is FULLY
discharged — M0141-S2a's fix1/fix2 pair was already landed-and-decided.

In-flight: none. All private artefacts (1 binary `tmp/goopg-m0139-0007b-bin`,
private ports 5596/5598, 1 TPC-DS clone dir, cgroup scopes, server logs under
`tmp/`) removed/stopped after use; shared clusters (`:65432`/`:65433`/
`:65437`/`:65438`) read-only or online-cloned (never stopped/started),
verified quiet after use. `tmp/goopg-audit-arm-tpch-data` intentionally left
in place — it is `tpch-estimate-audit-arm.sh`'s own reusable private-lane
data dir (its `cleanup()` stops the server/scope but not the dir by design;
matches the script's documented convention, not leftover WIP).

Next step: re-read the `## Current Priority` banner fresh (check date/content
match before trusting this note — the banner itself was edited this loop to
mark item 1 RESOLVED). Per the banner, item 1 ("costing-order unblock") is
now fully discharged; **select item 2 next: M0137's re-opened tasks
(0014–0017)** — read `AGENT.md` §"Plan-parity harness" first (binding for
every M0137–M0143 task), then `.ralph/fix_plan.md`'s M0137 section for the
0014–0017 task text and any scoping notes already recorded there. 0015 is
noted as one line; 0016's gate is noted as already satisfied; 0017 is flagged
as mattering more than its size suggests (TPC-H plans captured `-serial`
only, so `parallelism` is unscoreable in the current headline 6/22).

Gates run: `go build ./...` clean. `go vet ./internal/optimizer/...` clean
(no new findings beyond the two pre-existing/verified lostcancel ones in
`cmd/goopg/main.go`). `go test ./internal/optimizer/...` full package `ok`
(includes 3 new + 3 updated Memoize tests). `scripts/tpch-spotcheck.sh`
RESULT=PASS (Q12=2, Q13=34, canonical; private port 5580, clone never
touched `:65433`). Pre-commit pgbench smoke PASS (hook-enforced; tps
~42-152 across the three builtin scripts, 0 failed). `make
ralph-state-guard`: one self-repair (same recurring benign stale-clean-exit-
marker pattern several prior loops have noted — status/progress reconciled,
not a real inconsistency), clean after repair.
