# Working set — M0134-0005m landed (INSERT … SELECT DEFAULT-fill)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005m LANDED**. Sub-item `[x]`,
parent case stays `[ ]`. Selected per the Current Priority banner (M0134 after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed:** `INSERT INTO t(collist) SELECT …` (and the no-column-list
narrower-SELECT form) dropped omitted columns' DEFAULT expressions.
Planner-only fix; executor untouched. Design §20.

**Four things worth not re-learning:**
- **The obvious diagnosis was the wrong half.** `rewriteInsertDefaultMarkers`
  bails on `s.Select != nil` (`planner.go:9877`) — but the DEFAULT was still
  *attempted*: the executor derives `insertMissing` from "not in ColumnIndex"
  and routes to `applyDefaultsForMissing`, whose lightweight
  `evalGenExpr`/`evalGenFuncCall` has **no `currval` case** and silently returns
  `NullDatum, nil`. Fix = route through the FULL evaluator (Project-wrap), not
  widen the lightweight one. **Always ask "is it never attempted, or attempted
  by the wrong evaluator?"**
- **That silent-NULL fallback is now the root cause behind THREE slices (j, l,
  m).** Ledgered 2026-08-18 as a deliberate central fix — make the unhandled
  case *error* instead of returning NULL. Do it once on purpose, not a fourth
  time by accident.
- **`SELECT 1, 2` already plans to a `*Project`.** A test asserting
  "Source is/isn't a `*Project`" proves nothing; assert
  `len(ins.Source.Output())`. (My brief got this wrong; the gate caught it.)
- **Renaming a test can silently drop a guard.** The renamed
  `…TruncatesColumnIndex` → `…AppendsOmittedDefault` was the sole guard for the
  0118-0038 panic regression; a sibling test had to be re-added.

**Gates run:** `go build ./...`; `go test ./internal/{optimizer,executor,catalog}/`;
`go test -run 'TestPort_.*Constraint' ./internal/testport/`;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (7m50s; initdb
cold after the implementer's `go clean -testcache`, rest warm);
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1); pre-commit
pgbench smoke via the hook.

**Next step — two ranked candidates, neither yet re-derived from its fixture
(do that first, per the 0005l lesson):**
1. **The central evaluator fix** (ledgered above) — make
   `evalGenExpr`/`evalGenFuncCall` error on an unhandled function instead of
   returning NULL, then route `copy.go:insertSourceRow` and the upsert insert
   branch through the full evaluator. Highest leverage: closes the shared root
   cause of j/l/m. Caution: shared with the apply worker; no `constraints.sql`
   fixture guards it, so it needs its own tests.
2. **COPY BINARY** (`PushBinaryData`, ledgered 2026-08-18 under 0005l) — pure
   wiring, reuses the `missing[]`/`needsConstraints` state 0005l computes.
   Cheapest remaining item.
Also open from 0005l: COPY FROM's 9 error sites in `internal/postmaster/copy.go`
omit `execErrDetailFields(perr)...`, so `DETAIL:`/`CONTEXT:` never reach the wire.

**Delegation:** `tmp/ralph-handoffs/m0134-0005m-insert-select-defaults/`
(researcher `a3a6a6eba2acb01f1`, 1 round, DONE — it **corrected the coordinator's
framing** and its citations held up; trustworthy. implementer
`a8fc0ed05f2d22958`, 3 rounds, DONE. tester `a2b1e033535e4a505`, 1 round, DONE.)

**In-flight:** none.
