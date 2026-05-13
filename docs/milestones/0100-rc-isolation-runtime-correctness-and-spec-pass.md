# Milestone 0100 — RC Isolation-Test Suite: Runtime Correctness Closure & 21-Spec Pass

**Status:** in-progress
**Filed:** 2026-05-13
**Depends on:** M0060 (oracle test-port foundation), M0096-0001..-0012 (feature surface for 21 RC isolation specs)
**Closes:** M0096-0005 (ON CONFLICT executor correctness — wait-state propagation), M0096-0013 (E2E pass confirmation for all 21 dedicated RC isolation tests)
**Reference plan:** `.ralph/fix_plan.md` (M0100 section)

## 運用追記 (2026-05-13)

- **本マイルストーンでは、サブタスクを DEFERRED として扱うことを原則として認めない。** ここで列挙された全ての項目は 21 個の RC isolation テストを実際にパスさせるための残存ランタイム正当性ギャップであり、いずれかを未実装のまま残すと M0100 の Definition of Done を達成できない。「後続マイルストーンに送る」「次ループに回す」といった逃げ道は使わないこと。
- 例外として DEFERRED が許容されるのは、(a) goopg の Go 実装制約あるいは設計制約により本リリースで実装不可能であることが明確に立証され、(b) その理由が当該サブマイルストーンの本文に明記されており、(c) 21 テストのうち当該項目がブロックするものを `excluded` ではなく `pass` させるための代替経路が同マイルストーン内で提示されている場合 — の三点を **全て** 満たすときに限る。これに該当しない理由で DEFERRED 化することは許可しない。
- blocker の存在や goopg 未対応で途中までしか進められない項目は、blocker 解消までを本マイルストーンの実施範囲に含める。
- blocker 解消により先に進める項目は、解消実装と再検証が完了するまで完了扱いにしない。

## Goal

Make all **21 dedicated `TestPort_Isolation*` test functions** (added by M0096-0001 in
`internal/testport/isolation_port_test.go`) report `pass` — none `defer`,
none `excluded`. The 21 specs are the strongest proxy for goopg's READ
COMMITTED correctness story and the dependency target for closing
M0096-0005 and M0096-0013.

The parser / planner / catalog / DDL feature surface for these specs
landed across M0096-0002..-0012. What remains is **runtime correctness**
in MVCC, the dispatcher, and the heap/DML operator path. This milestone
closes those gaps and does not introduce new SQL features.

## In-scope sub-milestones

Authoritative sub-milestone detail and progress lives in `.ralph/fix_plan.md`
under the M0100 heading. Summary:

1. **M0100-0001** — RR/Serializable BEGIN-time snapshot (stop refreshing snapshot
   per statement when isolation ≠ ReadCommitted).
   Design: `../design/0100-0001-isolation-level-snapshot-semantics.md`.
2. **M0100-0002** — Eager XID materialisation so concurrent INSERTs can detect
   each other in `findInProgressConflict` and `WaitForXID` actually blocks.
   Closes M0096-0005. Design: `../design/0100-0002-eager-xid-materialization-at-begin.md`.
3. **M0100-0003** — Row-level wait on in-progress xmax for UPDATE/DELETE
   (PG-parity `XactLockTableWait` + re-fetch).
   Design: `../design/0100-0003-row-level-wait-on-in-progress-xmax.md`.
4. **M0100-0004** — EvalPlanQual concurrent UPDATE recheck (re-evaluate the
   UPDATE qual against the post-wait tuple version).
   Design: `../design/0100-0004-evalplanqual-recheck.md`.
5. **M0100-0005** — End-to-end pass confirmation: run all 21 `TestPort_Isolation*`
   tests, every one must report `pass`. Flip the 21 entries in
   `docs/test-port/executable-isolation-tests.md` from `defer` → `port`,
   `pass_required` → `yes`. Mark M0096-0005 and M0096-0013 closed via
   cross-reference.

## Definition of Done

- All four design docs (0100-0001..-0004) at status `accepted`.
- `go test -v -run TestPort_Isolation -timeout 30m ./internal/testport/`
  reports every `TestPort_Isolation*` from the M0096-0001 list as `pass`.
- M0093's read-only-commit pgbench-S regression mitigation is intact
  (≥ 2,000 TPS at `-c 10`, vs the M0093-accepted 2,740 baseline).
- `gofmt -l .` empty; `go vet ./...` clean; `make ralph-state-guard` passes.
- `docs/test-port/executable-isolation-tests.md` lists all 21 specs as `port`.
- `.ralph/fix_plan.md` shows M0096-0005 and M0096-0013 as `[x]` with the
  "closed via M0100-…" cross-reference note.

## Out of scope

- New parser / DDL / planner features (already landed in M0096-0002..-0012).
- Non-RC isolation specs (SI/SSI suites). Promoted independently as future
  milestones if needed.
- The "stale" residual notes from M0096-0013 that were verified non-gaps
  during M0100 planning: RAISE NOTICE trigger output (already correct in
  `internal/executor/plpgsql_runtime.go:1053-1056` per M0096-0012) and
  the `---+---` column width in `pqprintFormat` (already matches libpq
  `PQprint` align-mode behaviour). Re-open as a separate sub-milestone only
  if 21-spec pass surfaces a real divergence at these sites.
