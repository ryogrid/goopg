(idle — nothing in flight)

Last landed: DU-002 slice 124 (loop #88) — an ADVANCED sequence (`is_called=true`)
dumps `SELECT pg_catalog.setval('public.bumped_seq', 42, true)` byte-identically vs
real pg_dump 18.3. First slice over the called branch; every prior sequence slice
(115–123) dumps `(name, start, false)` (never-called). After `setval(bumped_seq, 42,
true)` the process-global `seqRegistry` state is current=42/called=true, so
`SequenceRowData` → (42, true), the `pg_get_sequence_data` SRF projects it, pg_dump
emits the called form. NO production code change — `SequenceRowData`'s called=true
branch already returns `current` as last_value. Regression guard with exact `(42,
true)` positive assert + 3 negative guards (rejects `(1,false)`, `(42,false)`,
`(1,true)`). Reference `/tmp/du124_pgdata`.
Files: internal/testport/pgdump_connsetup_test.go (bumped_seq fixture+setval+asserts),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 124 section), .ralph/fix_plan.md.

DISCOVERED BUG (→ slice 125 candidate): `SequenceRowData` (internal/executor/
operators_sequence.go ~line 201) called=FALSE branch returns `s.start`, NOT
`current+increment`. So `setval(seq, N, false)` with N != start (e.g. `START WITH 5;
setval(.., 30, false)`) diverges — goopg dumps `setval(.., 5, false)`, real PG dumps
`setval(.., 30, false)`. Fix = return `current + increment` when !called (equals
start for a fresh seq, N after setval(N,false)). Touches the SHARED pg_sequences
view + `SELECT * FROM <seq>` sibling paths → needs full sequence-path testing, own
task. Slice 124 deliberately avoided this case.

Next direction (slice 125): either fix the called=false non-default-value divergence
above (production change, sibling-path care), OR a table+VIEW dependency-ordering
case (view depends on table; verify topological emission ORDER, not just presence).
