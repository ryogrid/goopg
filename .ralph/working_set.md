# Working set — M0134-0002 C19 landed; C16/C17/C18 filed as milestone-sized

**Task:** M0134-0002 (`alter_table.sql`), class C19. Selected per the Current
Priority banner (M0134 next after M-NIGHTLY). M-NIGHTLY drained —
`ci/logs/action-items.md` is still run `20260817-011734`, all 6 filed and `[x]`;
nothing new to file.

**Landed (two halves).** (1) **Harness:** `scripts/pg-regress-runner.sh` now
passes `-v HIDE_TABLEAM=on -v HIDE_TOAST_COMPRESSION=on`, which upstream
`pg_regress` sets on every psql (`pg_regress_main.c:74-79`). The previous loop's
symptom (d) was INVERTED — goopg *over*-produced the `Compression` column and
`Access method:` footer. Zero engine content; corrects the comparison
suite-wide. (2) **Engine:** `pg_get_indexdef` discarded its `colno` argument and
always returned the whole `CREATE INDEX`; new
`catalog.BuildIndexDefColumn` mirrors `ruleutils.c:pg_get_indexdef_worker`'s
`attrsOnly` branch (`ruleutils.c:1198-1217`, decorations gated at :1459).
Oracle correction found while implementing: out-of-range `colno` on a *valid*
index OID returns the EMPTY STRING, not NULL.

**Files:** `scripts/pg-regress-runner.sh`, `internal/catalog/catalog.go`
(`BuildIndexDefColumn`), `internal/executor/expr.go` (`pg_get_indexdef` case),
`internal/executor/pg_get_indexdef_test.go` (new guard),
`docs/design/0134-0002-alter-table-sql-divergence.md` (C16–C19 rows +
re-characterisation section + "C19 — LANDED" section + 2 oracle citations),
`.ralph/fix_plan.md`, `.ralph/deferral_ledger.md` (2 rows 2026-08-18).

**Sibling audit:** all 18 `pg_get_indexdef|buildIndexDefString|BuildIndexDef`
hits resolve to one canonical chain; no second index renderer, no separate
`\d`-support path — no twin to change.

**KEY FINDING for the next loop.** C16 (ownership/ACL absent), C17 (`pg_locks`
always empty — ~180 lines, the largest single class), C18 (no constraint
exclusion) are now filed as classes and are each **milestone-sized**, NOT
`alter_table` slices. Do not attempt them under M0134-0002. Their cheapest
independently-landable pieces are in the ledger: C16 = a `requireRelationOwner`
helper applied to the RENAME family only; C17 = one blanket `AccessExclusiveLock`
acquire at the top of `execAlterTable` (fixes row existence, not `mode` strings);
C18 = a real optimizer pass, entangled with the M0134-0001 S22 per-child
range-table gap.

**Next step:** re-measure `alter_table` residual at the new baseline (3968) and
decide whether M0134-0002 still has a bounded slice left (C7 residuals:
`CHECK ((a>10.2))` double-parens, partition-child index `_0_key` naming; plus
C12/C13) — or whether M0134-0002 should be parked like M0134-0001 and the next
M0134 case selected. Note the harness fix shifts every other regress case's
baseline; re-measure before comparing to any older number.

**Gates run:** `go build ./...` clean; `go test ./internal/executor/
./internal/parser/` ok; guard `TestBuildIndexDefColumn` FAIL-pre (compile
error) / PASS-post; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS (`internal/initdb` cold 455s, `cmd/goopg` 87s, rest cached);
`scripts/pg-regress-runner.sh --verbose alter_table` 4039→3981→**3968** lines
(hunks 108→111 — the same benign `--unified=5` windowing split verified in C7
slice 1, proof point `atnnpart1_pkey` Definition now byte-identical to PG).

**Delegation:** `tmp/ralph-handoffs/m0134-0002-s03-c16-c19-charaterise/`
(researcher `aba34bf23278450cc`, 1 round, findings adopted) and
`tmp/ralph-handoffs/m0134-0002-s04-c19-indexdef/` (implementer
`afaad5bdf1161dfb9`, 1 round, DONE; tester `a97d372532e8a93f4`, PASS).

**In-flight:** none.
