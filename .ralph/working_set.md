# Working set — M0134-0002 alter_table.sql (Slice 1 crash-fix LANDED)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed
**Slice 1** — the server-crash fix: `viewColumnMap`'s bare-`*` arm
(`internal/planner/view_dml.go`) now maps POSITIONALLY over the view's frozen
column count (`len(view.Columns)`), not `len(b.Columns)`. `update v2 set
q1=q1+1 where q1=123` (bug #17811) executes (2 rows flip 123→124) instead of
panicking `index out of range [1] with length 1` in `viewProxyTable`.

**Status:** Slice 1 COMPLETE + committed + pushed (`dc8c0b9d`). Design note
`docs/design/0134-0002-alter-table-sql-divergence.md` (draft) + index; 2 ledger
rows (rules subsystem, read-path/top-level-* freeze); row 1385 corrected
(underline = psql symptom of subplan-subtree indent depth, not a fixed-width
renderer).

**Files:** `internal/planner/view_dml.go` (positional star map),
`internal/executor/view_dml_test.go` (2 tests: frozen-growth + column-list
rename); `docs/design/0134-0002-alter-table-sql-divergence.md` + README index.

**Key symbols:** `viewColumnMap` (new `view *catalog.Table` param, positional
star arm), `viewAutoUpdatableChain`, `viewProxyTable`.

**Findings:** alter_table.sql = 4668-line diff (was 44 hunks), crash lost ~45%
of the tail; post-fix 4671 lines / 81 hunks (tail populated, bug-#17811 UPDATE
matches PG). 14 divergence classes: C1 `text[]||text[]` op missing (unblocks all
13 `\d+`); C2 ALTER-TABLE grammar cluster (largest — RENAME CONSTRAINT, RENAME
col TO, TYPE USING, comma multi-action, NO INHERIT/NOT VALID, DROP COLUMN IF
EXISTS, SET WITHOUT OIDS, STORAGE); C3/C4/C8/C9/C10/C11 correctness (C10 =
data-loss on failed ALTER TYPE; C11 = rules + read-path + freeze); C5 btree-inet;
C6 catalog gaps; C7/C12/C13/C14 formatter.

**Next step:** C1 (`text[] || text[]` operator — self-contained, `array_cat`
already exists at `expr.go:11543`, unblocks every `\d+` describe block) or C2
(the parser grammar cluster — largest single class). Recommend C1 first (small,
independent) then C2.

**Gates run (this loop):** `go test ./internal/planner/ ./internal/executor/`
PASS (2 new tests); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35);
`scripts/pg-regress-runner.sh alter_table` no-crash (4671 lines, tail populated);
pre-commit pgbench smoke PASS (0 failed txns).

**Delegation:** researcher `0134-0002-alter-table-research` DONE (crash + 14
classes + underline correction); implementer `0134-0002-s1-view-crash` DONE
(2 rounds — by-name → positional refinement); tester tpch-spotcheck DONE.

**In-flight:** none.
