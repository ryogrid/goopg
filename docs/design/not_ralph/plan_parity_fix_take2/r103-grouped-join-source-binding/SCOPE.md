# R103 SCOPE — end-to-end source bindings for unaliased grouped JOIN LATERAL

R102 (`dd3ca6e41`) proves PostgreSQL 18 semantics for a later LATERAL item
after an unaliased parenthesized JOIN, and proves Goopg needs planner range
bindings as well as analyzer resolution. This scope addresses that one
representation boundary end to end; it is not a Q96 cost change.

## Authorized change

Represent an unaliased grouped JOIN distinctly from an ordinary alias-omitted
derived subquery at the parser's generated and legacy construction points.
Thread its source range bindings, source order, aliases, offsets, and `USING`
visibility through analyzer scope construction and planner `planFromItem` /
`resolveContext` construction. A later JOIN `ON` clause and an explicit
LATERAL right subquery must resolve the grouped sources exactly as PostgreSQL
does, while the physical grouped subquery plan/output remains unchanged.

Add focused parser, analyzer, planner, and executor/integration tests that
assert the following R102 PG18 witness results and SQLSTATEs:

1. unaliased grouped JOIN: both base aliases work in a later ON clause and in
   LATERAL, returning the recorded one-row values;
2. ordinary unparenthesized JOIN-chain LATERAL remains valid and returns its
   recorded one-row value;
3. explicitly aliased grouped JOIN: join alias output works, original base
   aliases fail with SQLSTATE `42P01`, with both successful output and error
   class asserted;
4. `USING` preserves both qualified source columns and the one merged
   unqualified column with their recorded values, while the stated duplicate
   reference fails with SQLSTATE `42702`; and
5. a non-LATERAL right subquery cannot see the left grouped sources, asserting
   SQLSTATE `42P01`.

The executor test must show correlated values, not only successful planning.
After those controls pass, rerun R101's unchanged `/tmp` forms on the
isolated Goopg SF0.25 clone and record values and repeated plans. Only if both
values remain 266 and EXPLAIN retains the intended first-two leaves plus final
time probe may R101 measurement resume; this still does not authorize a cost
or natural-election conclusion.

## Boundaries

No parser grammar expansion, generic derived-subquery visibility, join-order
or cost/selectivity change, executor algorithm change, GUC default change,
benchmark SQL change, `postgres/` edit, reference-data mutation, or ANALYZE
is authorized. The representation marker must not let a synthetic `__sq_*`
name leak to SQL resolution. `grammar/pg_grammar.y` is the authoritative yacc
source: make parser changes there, run `make gen-parser`, and commit the
regenerated `internal/parser/yacc_parser.go` and applicable token artifacts
with their source; never directly edit generated files. Stop if source
indices, outer-reference levels, or aliased/USING controls cannot be preserved
without broadening visibility beyond the listed PG witnesses.

Before code: agent review, correction, `git commit -n`, and push. Afterwards:
focused tests, relevant package tests, required parity gates, `git diff
--check`, then commit/push code and English report without unrelated files.
