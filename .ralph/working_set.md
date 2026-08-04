(idle — nothing in flight)

M0127-P5.5-a is CLOSED and committed. **P5.5's stated prerequisite is now met.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item, which is `P5.5` proper (`createPlan` arms + the 03 §10 search-boundary
coordinate map).**

Carry-over facts a next loop should not re-derive:

- **P5.5-a landed the index/direction half only.** `Path.IndexInfo` +
  `Path.IndexScanDir` are filled at BOTH index-path constructors via
  `indexPathOrdering` (`pathindexcarrier.go`), which returns pathkeys AND
  direction together so they cannot drift. `ScanDirection` uses PG's -1/0/+1
  encoding; the zero value means "not an index path".
- **`IndexPath.indexclauses` is STILL not carried** (new ledger row). The
  blocker found this loop: PG's `indexclauses` are in INDEX-COLUMN order
  (`match_clauses_to_index`), goopg's `bound []paramIndexClause` is in
  CANDIDATE order, and the executor's `IndexScan.Keys[i]` binds
  `Index.Columns[i]` POSITIONALLY — a verbatim copy would bind the wrong
  column. P5.5's createPlan arm needs this sorted carrier to build `Keys`.
- **`createPlan` today handles `PathPrebuilt` only** and panics on every other
  kind (`createplan.go:37`). Live kinds it must grow arms for: PathIndexScan,
  PathSeqScan, PathHashJoin, PathMergeJoin, PathNestLoop, PathSort.
- **Merge path anatomy for the createPlan arm:** `Children[0]` = outer,
  `Children[1]` = inner (a `PathSort` child when that side needed sorting);
  `HashKeys` = the ORDERED merge clauses; `Residual` = everything else,
  INCLUDING clauses a truncated merge demoted. `Pathkeys` is the OUTER PATH's
  full ordering, which may be longer than the merge key list.
- **`P5.4b-ii-b-2` stays DEPENDENCY-DEFERRED until after P5.5** (needs a built
  `*Join` NODE).
- **An ordered index path is never `CheapestTotal`** (`indexCorrelationFor` is
  0). Anything wanting one must walk `rel.Pathlist`.
- **`sizeJoinRel` is STILL the open half of the `joinRelBuilder` seam** (P5.6).
  `GOOPG_PGSHAPED_DP` stays OFF. Do not write a stand-in sizer.
- **P4.1 ledger row #3 still open**: `mergeJoinStream.bufferGroup` twin.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never wholesale `-w`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Gate recipes** — SPOT: `scripts/tpch-spotcheck.sh`. DS05:
  `scripts/tpcds-sf05-regression.sh sweep` (~1 h). PLAN:
  `bench/tpch/setup_goopg.sh` → `PATH=$PWD/postgres/local_install/bin:$PATH
  make plan-gate` → `bench/tpch/stop_goopg.sh`.

Gates run this loop: UNITS PASS (exit 0, 0 FAILs, `/tmp/units_p55a.log`); SPOT
PASS (`/tmp/spot_p55a.log`, Q12=2 Q13=35, canonical); pgbench SMOKE PASS via the
commit hook; build + `go vet ./internal/planner` + gofmt clean on every touched
file. DS05 not applicable — the new fields are written by a search with no
`planSelect` caller and `GOOPG_PGSHAPED_DP` is OFF.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed
(001 individually, 002..016 as a batch, 017 individually). Nothing new.

In-flight: none.
