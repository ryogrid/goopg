Task: M0139-0007c — port `get_expr_width` for Memoize's cache-key width term
(the deferral M0139-0007b filed). DONE and committed this loop
(`6faa73776`). Banner item 3's whole chain (fix1-sweep → sweep-a → sweep-b →
M0141-S2b-6-resume → M0139-0007c) is now fully closed.

**NEXT LOOP should re-read the banner first** (S1 precedence). Banner items
0 (P0, P0-E7 closed 2026-09-18d), 1 (no new selectable regression — P0-H11
is `[!]`-equivalent blocked on the owner, P0-H12 done), 2
(M0141-S2a-fix2r, done) and 3 (this loop) are all exhausted. **Item 4,
M0141-S7 ("cost diagnosis only"), is next** — `.ralph/fix_plan.md:3587`
`M0141-S7 — re-adjudicate and implement Incremental Sort` is `[ ]` and
selectable. Read that item's banner text carefully before starting: it caps
scope hard — "No production-code change under this item — not even a trace
inside an existing trace guard. If instrumentation is genuinely needed, file
a separate `Kind: impl` task, run the values gates and report the parity
numbers (this is what `073ab2748`/`c7e231ae1` got wrong)." Also read
`docs/design/0100-0149/m0141-s7-*.md` (the eight split-out docs from P0-D3)
before touching it — do not re-read the frozen 226-line parent wholesale.

Files this loop: `internal/optimizer/joinpathsmemoize.go` (new
`memoizeKeyWidths`, `costMemoizeRescan` gained a `keyWidth float64` param).
`internal/optimizer/joinpathsmemoize_test.go`,
`memoize_pgentrybytes_test.go` (two new tests, five pre-existing
`costMemoizeRescan` call sites updated). Design doc:
`docs/design/0100-0149/m0139-0007c-memoize-key-width-absorption.md` (new,
indexed in `docs/design/README.md`). `.ralph/fix_plan.md` (M0139-0007c
ticked `[x]`).

Key symbols: `memoizeKeyWidths` (new, mirrors `memoizeKeyNDistinct`'s loop
over `innerPath.IndexClauses`) — reads `ColumnStats.AvgWidth` via
`examineJoinVar`, falls back to `typeWidth(cr.Type)`. `costMemoizeRescan`'s
`perKeyBytes` selection (only uses the ported `keyWidth` when
`pgMemoizeEntryBytesCostEnabled()` AND `pgRelationByteSize` succeeded — same
gate as the tuple-bytes term beside it).

Finding: the port is real (cited PG `get_expr_width`, `costsize.c:6404`) but
inert by construction — `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST` stays
default-off (M0139-0007b's HOLD, unchanged), so no plan can move from this
commit. Confirmed via `TestCostMemoizeRescanPGEntryBytesSwitchOffIsLegacy`
(unmodified, still passes) and the acceptance-arm/sf025 gates below (both
byte-identical to HEAD). Does NOT reopen 0007b's HOLD decision.

Gates run: `go build ./...` clean. `go vet ./internal/optimizer/...` clean.
`go test ./internal/optimizer/...` PASS (full package, no `-count=1`;
includes the two new tests). `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34)
against the staged tree. `scripts/tpcds-sf025-regression.sh sweep` PASS=96
MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, PLAN-SHAPE queries=99 same=99
changed=0, against the staged tree. `scripts/tpch-acceptance-arm.sh`
PGSHAPED=1 baseline (HEAD `459e4e30d`, built via a `git stash`/`stash pop`
round-trip of just the three touched `.go` files, private port 5583) vs
this staged tree: VERDICT PASS, 24/24 labels MATCH. All three gate stamps
(`tmp/gate-stamps/{tpch-spotcheck,tpcds-sf025,tpch-acceptance-arm}.json`)
verified `code_tree` == `sha256(git ls-files -s -- internal cmd go.mod
go.sum)` of the committed index (`a0ede9c4f8…`) before committing — this is
now a hard `commit-msg` hook requirement (G2) for any `internal/optimizer/`
change, not optional; the commit body carries the required
`CATEGORIES-EXCL-MATCH:`/`PARITY: N/A —` lines. `python3
scripts/ralph_protected_regions.py check-designdocs` exit 0. `python3
scripts/ralph-lineage-guard.py` clean (after moving a mid-sentence
`Parent:` field onto its own line — the guard rejects a field buried inside
a prose line even when `Kind:`/`Parent:` both appear, per S3). `make
ralph-state-guard`: found the same status="running"/progress="completed"
inconsistency the last two loops also hit (prior loop's clean-exit marker),
auto-repaired to `in_progress`, then clean.

Learning for next loop: **any commit touching `internal/optimizer/`,
`internal/executor/`, `internal/planner/`, or a `*cost*/*stat*/*selfuncs*`
path now requires a FRESH `tpch-acceptance-arm` PASS stamp matching the
staged tree**, not just `tpch-spotcheck` + `tpcds-sf025` — budget ~15-20 min
of wall clock for the two-arm baseline/mine acceptance-arm round-trip
(build + 22-query digest run twice) in addition to the sf025 sweep (~5 min)
and spotcheck (~1 min). The `git stash push -- <the changed .go files>` /
build baseline / `git stash pop` / re-`git add` / build+diff-mine pattern
used this loop is the clean way to get a same-binary-family A/B without a
worktree.

In-flight: none. Both private-lane arm binaries/runners (port 5583,
`tmp/goopg-acceptance-{baseline,mine}-bin` and their runner counterparts)
and the two `/tmp/arm-m0139-0007c-*.txt` digest files deleted after the
diff; port 5583 verified free (`ss -ltnp`) before finishing. No shared
cluster (`:65432`/`:65433`/`:65437`/`:65438`) was started, stopped, reset,
or written beyond the online `pg_basebackup -X fetch` clone source reads the
private-clone scripts already do.
