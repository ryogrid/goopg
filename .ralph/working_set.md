(idle — nothing in flight)

Last loop: **M0127-P5.9-l-i DONE** — clause 6's missing instrument is built,
and its first measurement refutes run 4's manual reading.

1. `internal/estimateaudit/spine.go` (new): per join node, the relsets of its
   two immediate children — the PAIRING, the unit clause 6 is stated on, which
   a relset name cannot carry. Bushy iff both children are joins after
   descending the single-child pipeline nodes between them (arity rule, not a
   label whitelist). `SpineDiff`/`CountSpine`/`RenderSpine` diff both engines'
   chosen spines and name the clause-6 candidates. Renders from
   `cmd/estimate-audit` whenever `--reference` is present ⇒
   `scripts/tpch-estimate-audit-arm.sh` needed NO change.
2. **Measured offline over run 4's committed plans (no re-run, no server).**
   09 §3.10's "goopg produces no bushy spine on any of the 22" is FALSE: the
   ON arm goes bushy on six (Q2, Q7, Q8, Q9, Q10, Q20), the OFF arm on two —
   and **on Q20 the ON arm chooses PG's bushy partition exactly**
   (`{nation+supplier} ⋈ {lineitem+part+partsupp}`, printed `both`). First
   evidence phase 2 + `add_path` keep a bushy pair over a real 5-relation
   TPC-H relset, not the synthetic 4-rel chain its unit tests use.
   Flag moves every spine number toward PG: matched 13→24, PG-only 44→33,
   goopg-only 45→32, bushy 2→6.
3. **Two clause-6 candidates remain and only two**: PG's bushy top on Q7
   (`{customer+lineitem+n2+orders} ⋈ {n1+supplier}`) and Q8
   (`{lineitem+orders+part} ⋈ {customer+n1+region}`).

Files: `internal/estimateaudit/spine.go` + `spine_test.go` (6 tests),
`parity.go` (`planParents` extracted), `cmd/estimate-audit/main.go` (render),
09 §3.11 + §4 + the run-4 clause table, bundle README, `docs/design/README.md`,
IMPLEMENTATION-TODO (P5.9-l split into -l-i done / -l-ii new), fix_plan,
2 ledger rows. Evidence:
`analysis/leftdeep-joins/2026-08-06-p59l-spine-{on,off}.txt` + `-README.md`.

Gates run: `go vet` + `go build ./...`; `go test -v -run TestSpine
./internal/estimateaudit/` (6/6 PASS); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` (all packages ok, no FAIL); pgbench smoke via
the commit hook; `make ralph-state-guard` (repaired a stale progress marker).
No planner/executor code changed, so no spotcheck/DS05 arm was warranted.

Next step: **M0127-P5.9-l-ii** — the search-side half. Record every
`(outer relset, inner relset, phase)` triple `makeJoinRel` is offered plus the
relid→relation-name map, export it on a channel an arm run can harvest, and
test membership of Q7's and Q8's partitions with Q20's matched pairing as the
positive control. Then P5.9 re-runs clause 6 alone and flips or attributes.

In-flight: none.
