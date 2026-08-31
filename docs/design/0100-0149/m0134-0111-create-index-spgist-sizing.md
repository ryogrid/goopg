# M0134-0111 — `create_index_spgist.sql`: sizing + two contained fixes

**Status:** PARKED (`failed`, 0% parity). Two contained fixes landed; the
dominant gaps are new-subsystem-scale and out of scope for a single
test-port loop.

## Oracle case

`postgres/src/test/regress/sql/create_index_spgist.sql` (437 lines) exercises
SP-GiST indexes over `point` (quad-tree `quad_point_ops`, kd-tree
`kd_point_ops`) and `text` (radix-tree, default opclass) columns: geometric
containment/direction/distance operators, KNN (`ORDER BY ... <->`) scans with
`INCLUDE` columns, text pattern-comparison operators, and `starts_with()`,
each checked three times (seqscan / plain indexscan / bitmapscan) via
`enable_seqscan`/`enable_indexscan`/`enable_bitmapscan`.

Sized live via `scripts/pg-regress-runner.sh -v create_index_spgist` against
the PG 18.3 oracle: 0% parity, diff 1576 lines pre-fix (whole-file
divergence — even the pure-seqscan section near the top of the file, which
needs no SP-GiST index at all, hard-errors).

## Landed this loop

1. **`point <@ box` / `box @> point` containment** — `internal/executor/expr.go`,
   the `parser.OpContainedBy`/`OpContains`/`OpOverlap` arm (added by
   M0097-0023) only ever attempted box-vs-box parsing (`parseBoxText` on
   both operands). Real PG dispatches `<@`/`@>` by operand type
   (`point_box.c` vs `box.c`), so `point <@ box '...'` and
   `box '...' @> point` are legitimate, separate operator instances — goopg
   raised `operator <@: invalid box value` for both, in the file's very
   first seqscan-section queries. Fixed by falling back to
   `parsePointText` (treating a point as a degenerate box with equal upper-
   right/lower-left corners) when box-parsing fails on either operand — the
   existing containment-comparison formulas need no further branching for
   this case.
2. **`starts_with(text, text)`** (`pg_proc` oid 3696) — fully registered in
   the catalog (`isKnownBuiltinFunction`, `pg_proc_names_generated.go`) but
   had zero `evalFuncCall` dispatch arm, so every call raised
   `42883 function starts_with does not exist`. Added a case beside the
   existing `left`/`right` string-function arms (`internal/executor/expr.go`)
   doing a plain `strings.HasPrefix`.

Verified both live against the oracle: diff went from 1576 → 1516 lines
(`scripts/pg-regress-runner.sh -v create_index_spgist`).

## Why parked — two independent multi-file gaps

### (a) Operator lexer only recognizes a hardcoded whitelist

`internal/parser/lexer.go:548-575` matches multi-char operators via an
explicit `switch two { case "<=", ">=", ... }` list plus a few manually
special-cased 3-char forms (`!~*`, `->>`, `#>>`). Real PG's `scan.l` `Op`
production instead accepts *any* run of the graphic operator characters
(`+-*/<>=~!@#%^&|`), with special-casing only for trailing `+`/`-` (must not
end a multi-char operator name unless it also contains one of
`~!@#%^&|` — `SELF` chars are excluded) and for sequences that would
otherwise swallow a comment start (`--`, `/*`).

This file alone exercises eight operator spellings goopg's lexer rejects
outright as a syntax error: `<<|`, `|>>`, `~=` (point/box direction and
same-point operators), and `~<~`, `~<=~`, `~>=~`, `~>~`, `^@` (text
pattern-comparison and prefix operators — several are exactly the ones
SP-GiST's own radix-tree opclass dispatches on). This is a systemic lexer
gap, not specific to SP-GiST or to this file: any SQL surface using a
non-whitelisted operator spelling hits the same wall.

**Resume point:** `internal/parser/lexer.go:548`, replace the `switch two`
whitelist with PG's real multi-char-operator scan rule (oracle:
`postgres/src/backend/parser/scan.l`, the `operator` start-condition rules
and `SELF`/`OP_CHARS` definitions). This is a lexer-wide behavior change
touching every SQL statement parsed, so it needs the full parser regression
sweep (not just this one file) before landing — out of a single contained
test-port fix's scope.

### (b) SP-GiST has zero physical index storage

`internal/executor/amutils.go` and `operators_ddl.go:7537-7543` register
SP-GiST catalog metadata only — `CREATE INDEX ... USING spgist` succeeds and
populates `pg_class`/`pg_index`/`pg_opclass` rows, but there is no quad-tree,
kd-tree, or radix-tree structure built anywhere, and no index-scan operator
consults one. This is the same class of gap as GiST (see
`docs/design/` GiST grid-cell SSI notes) and BRIN
(M0134-0095/-0096/-0097): every `enable_indexscan`/`enable_bitmapscan`
section of this file — roughly two-thirds of its 437 lines — expects a real
`Index Scan`/`Bitmap Index Scan` plan and instead gets `Seq Scan`, even once
gap (a) above is fixed and every operator parses.

**Resume point:** unstarted anywhere in the codebase. A real fix needs (at
minimum) a quad-tree page-splitting structure for `quad_point_ops`, a
kd-tree variant for `kd_point_ops`, a radix-tree variant for the default
text opclass, KNN-ordered (`ORDER BY <->`) scan support with a distance
priority queue, and `INCLUDE` column storage in leaf tuples — a
from-scratch index-AM implementation, multi-milestone scope.

## Ledger

`.ralph/deferral_ledger.md`, 2026-08-24, M0134-0111.
