(idle — nothing in flight)

Last loop (#6): M-NIGHTLY items (a) MEASURED and (b) CLOSED as **root-0034**
(`docs/design/root-0034-float-type-alias-opt-float-reduction.md`).

(a) The 176-case alphabetical prefix (622 s) now shows 3 `restarting the
cluster` / 3 `cluster recovered` / **0 `restart failed`** — root-0032+0033 hold.
`portals_p2`, `select`, `select_distinct` have real results at HEAD for the
first time and all three genuinely diverge (diffs in `/tmp/rdiff-loop6`).

(b) `index_including` was never an index bug. §10's fixture is
`CREATE TABLE nametbl (c1 int, c2 name, c3 float)`, and the row vanished from a
plain seq scan on an index-less table: `float` has no `pg_type` entry (PG
resolves `FLOAT [ (p) ]` in the grammar, `gram.y` opt_float), goopg's parser
kept the literal token, `catalog.TypeNameToOID`'s `default: return OIDText`
made the column text, while `internal/executor`'s own tables (`codec.go:482`,
`expr.go:3035`) knew `"float"` and encoded 8 IEEE-754 bytes. `INSERT 0 1` then
zero rows forever. Fixed by doing PG's reduction in the parser
(`normalizeFloatTypeName` → `parseColumnType`, `parseCreateDomain`,
`parseCastTail`, `parseCastFuncExpr`), incl. opt_float's two 22023 errors
(`parser.SyntaxError.Code` + `syntaxErrorCode`). `index_including` PASSES in
full-suite ordering.

**NEXT LOOP — highest value, already bisected:** `TestRestartAfterRetention`
(`internal/server`) is RED at HEAD (`wal: xlog heap-insert apply: storage: not
enough free space in page`, deterministic, 1.9 s). PASSES at `3716d5cd`, FAILS
at `fa90714a` → **root-0032 introduced it**. Same shape as root-0033 but the
INSERT arm: diff the redo page reconstruction for `xl_heap_insert`
(`internal/wal/recovery.go`) against its runtime sibling. Filed as an M-NIGHTLY
task in fix_plan + ledger row. NOTE `RALPH_PRECOMMIT_SCOPE=units` does NOT
cover `internal/server`.

Then: M-NIGHTLY (e) portals_p2/select/select_distinct, and (d) the harness's
phantom `deferred:` per case after a failed restart.

Gates run: `go test ./internal/parser/ ./internal/executor/` PASS; negative
control on both new tests (0 rows / raw LE float64 bytes with the reduction
short-circuited — non-vacuous); `TestPort_RegressSuite` 88-case prefix PASS
incl. `index_including` (244 s); `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` exit 0; `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35); pgbench smoke via the commit hook.
In-flight: none.
