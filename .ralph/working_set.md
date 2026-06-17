(idle — nothing in flight)

Last landed: DU-002 slice 125 (loop #89) — a REWOUND sequence
(`setval(seq, N, false)` with `N != start`) dumps byte-identically. This is the
FIRST production code change in the sequence-dump slice series (115–125); all
prior slices were verification/regression guards.

Bug (discovered in slice 124): `SequenceRowData`'s not-called branch returned the
bare `s.start`, so after `setval('rewound_seq', 30, false)` (which rewinds value
to 30 WITHOUT marking called) goopg dumped `setval(.., 5, false)` while real PG
dumps `setval(.., 30, false)` (PG stores last_value=30/is_called=false). Fix:
not-called branch now returns `current + increment` — the registry stores
`current = nextTarget - increment`, so this is the exact on-disk last_value
(`start` for fresh, `N` after rewind/RESTART WITH N). `SequenceRowData` is the
single shared function behind both `SELECT * FROM <seq>` and the
`pg_get_sequence_data` SRF → both sibling paths fixed in one place. The
`pg_sequences` view is unaffected (sources AllSequenceInfos, emits NULL
last_value while not-called).

Files: internal/executor/operators_sequence.go (SequenceRowData not-called
branch + doc comment), internal/testport/pgdump_connsetup_test.go (rewound_seq
fixture + asserts), docs/design/0110-0001-pg-dump-tap-port.md (Slice 125),
.ralph/fix_plan.md. Reference /tmp/du125_pgdata. Committed + pushed.

Next direction (slice 126): a table+VIEW dependency-ordering case (view depends
on a table; verify topological emission ORDER, not just presence), OR a CHECK
constraint / multi-column UNIQUE constraint dump case.
