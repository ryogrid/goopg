(idle — nothing in flight)

M0131-S14.2 + S14.4 LANDED (loop #15), see commit below; S14 checked off,
S14.3 deferred with a ledger row.

Files: `internal/executor/pg18_user_catalog_rows.go` (fast-default write +
`pgSingletonArrayBytes`/`pgSingletonArrayElement`), `internal/executor/codec.go`
+ `internal/initdb/initdb.go` (`anyarray` typalign 'd'), `internal/catalog/
codec.go` + the two other column lists (`attmissingval` text→anyarray),
`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go` (S14.4 inversion),
new `internal/executor/pg_attribute_missingval_test.go`,
`internal/executor/codec_empty_array_test.go` (4→8 inversion), design 0131-0004
§F3 + README, fix_plan S14, 1 ledger row.

NOTE: the four source files were UNCOMMITTED WIP from loop #14 (working_set said
"idle" — loop #14 was cut off before rewriting the baton). They built clean and
were correct; this loop finished them (guards, E2E inversion, docs, gates).

Worth carrying:
- Fifth sibling-drift bug of this milestone, same shape every time: a column
  that is ALWAYS NULL hides disagreement among the sibling definitions until
  something finally writes a value. Here it hid BOTH a wrong type
  (`attmissingval` declared `text`, PG has `anyarray` 2277) and a wrong
  alignment (`anyarray` is typalign='d'/8, not the 'i'/4 every other varlena
  array uses — `pg_type.dat:573`).
- The 8-byte `anyarray` padding also covers `pg_statistic.stavalues1..5`, so an
  EXISTING goopg `$PGDATA` with non-NULL stavalues decodes shifted after this —
  no in-place upgrade, ledgered.
- The relid-1249 tripwire in the E2E had to be SCOPED (`WHERE attrelid = 1249`):
  the unscoped `attmissingval IS NOT NULL` count is legitimately non-zero now.

Gates: UNITS precommit PASS, `internal/{executor,catalog,initdb}` PASS,
`TestE2E_PGColdStartOnGoopgDataDir` PASS (it FAILED first with count=1, which is
the fix proving itself), whole `^TestE2E_` family PASS (80 s),
`^TestPort_(Regress|PgDump|Initdb)` PASS (600 s), `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=35), pgbench smoke via the commit hook,
`make ralph-state-guard` OK.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): pick the next unchecked M0131
slice. S14.3 (make the heap's `attmissingval`, not `catalog.Column.MissingValue`,
goopg's OWN read path — `pgSingletonArrayElement` is the reader it needs) is
ledgered, not a fix_plan task; S15 (`could not open critical system index 2662`
on a goopg-CREATE DATABASE-minted database) is the next measured gap.

In-flight: none.
