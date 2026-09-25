# R104 SCOPE — grouped JOIN USING source/output map for LATERAL

R103 (`577cefe61`) proved that restoring only parent resolver bindings makes
the two Q96 forced forms execute, but it is semantically incomplete: a
LATERAL child of an unaliased grouped `JOIN ... USING` loses both qualified
source references with 42703. PostgreSQL 18.3 accepts the qualified sources
and the merged unqualified USING column, while retaining normal ambiguity for
other duplicate names.

## Authorized change

Introduce a durable, parser-originated map for an unaliased grouped JOIN from
each visible source binding to its physical output columns, including the
merged-column rule for `USING`. Carry that map through synthetic subquery
analysis, planner resolution contexts, lateral-child planning, and any
post-plan re-resolution that reconstructs output names. It must distinguish:

* `a.aid` and `b.aid` as qualified source references to the same single
  merged physical output slot (not separate physical columns);
* unqualified `aid` as the one merged USING column; and
* unqualified non-USING duplicates (for example `bid`) as SQLSTATE `42702`.

The physical plan's row layout must remain coherent with the binding offsets;
do not paper over a missing source by name-only lookup or by treating an
explicitly aliased grouped join as unaliased. Parser changes originate in
`grammar/pg_grammar.y`; run `make gen-parser` and commit all regenerated
artifacts, never direct generated-file edits.

## Required evidence

Use a freshly reproducible native PG18.3 one-row witness. The report records
the complete setup SQL, every PG probe statement, `server_version` and binary
identity, successful scalar values, exact failure SQLSTATEs, and the complete
matching Goopg statements/results. Add parser/analyzer/planner/executor tests
proving:

1. unaliased grouped `USING` accepts `a.aid`, `b.aid`, and unqualified `aid`,
   each with the recorded value;
2. non-USING duplicate `bid` fails 42702;
3. an explicitly aliased grouped join accepts its output alias and rejects
   inner source aliases with 42P01;
4. an otherwise identical non-LATERAL derived table rejects the outer source
   reference with 42P01; and
5. the same source map resolves both a following ON clause and LATERAL body.
   In both locations, tests assert that qualified `a.aid` and `b.aid` resolve
   to the one merged physical output slot rather than distinct positions.

After all semantic controls pass, rebuild an isolated Goopg binary and rerun
both unchanged R101 SQL files twice for repeatable EXPLAIN plus once for each
value. Each must return 266; plans must retain the forced first-two leaves and
final `time_dim` probe. This is measurement-only and never authorizes a cost
or natural-election conclusion.

## Boundaries

No generic derived-table visibility, parser grammar extension, join-cost or
selectivity change, join enumeration/election change, executor algorithm
change, GUC/default change, benchmark SQL change, `postgres/` edit, reference
cluster mutation, or ANALYZE is authorized. Stop and report if the map needs a
different physical row layout or changes an ordinary/aliased JOIN namespace.
Before code: agent review, correction, `git commit -n`, and push. Afterwards:
focused tests, relevant package tests, required parity gates, `git diff
--check`, then commit/push code and English report separately from unrelated
work.
