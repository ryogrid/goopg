# R110 SCOPE — preserve in-range varchar trailing whitespace

R109 proves a real data-fidelity failure: `store.s_street_name varchar(60)`
loses in-range trailing whitespace on Goopg COPY input/output. The immediate
source is `encodeValuePG`'s varchar coercion, whose existing test explicitly
expects unconditional trimming. PostgreSQL's varchar rule is narrower: a
value whose character length exceeds typmod may discard only excess trailing
spaces, while trailing spaces that already fit must remain stored and output.

## Authorized change

For assignment/input paths (`encodeValuePG`, COPY, INSERT, and UPDATE),
preserve every in-range byte, including spaces; when over typmod, remove only
enough trailing ASCII spaces to fit and reject remaining excess with 22001.
Explicit `::varchar(n)` casts retain their existing PG truncation semantics
for arbitrary overlength characters, but must also preserve in-range trailing
spaces. Keep UTF-8 character counting, heap/TOAST encode-decode, index
behavior, extended protocol, and COPY TO output coherent. Do not change
`char(n)`/bpchar, text, name, numeric, generic whitespace handling, or the
physical varlena representation.

## Required evidence

Use a fresh native PG18.3 probe to record values, octet lengths, successful
assignment excess-space truncation, assignment failure for excess non-space
characters, and explicit overlength non-space cast truncation. Add focused
unit and protocol/COPY tests for in-range spaces, multibyte input,
cast/INSERT/COPY/UPDATE round trips, index lookup, and output. Rebuild private
Goopg and rerun the R109 `store` full normalized witness; it must match PG.
This scope alone does not authorize a cost change or R108.

## Boundaries

No planner cost/selectivity, path/election, executor algorithm, schema/index,
benchmark SQL, `postgres/`, existing oracle/data mutation, or GUC/default
change is authorized. Use new disposable probe data only and stop private
services. Before code: agent review, correction if needed, `git commit -n`,
and push. Afterwards run focused and relevant package tests, required parity
gates, `git diff --check`, then commit/push code and English report separately
from unrelated work.
