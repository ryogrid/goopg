Loop #17 implemented + committed M0119-0004 pg_dump parity slice 436
(commit a783a8a1, NOT pushed yet — push next loop or on request).

Task: continued the long-running `M0119-0004` pg_dump catalog-view/dump-
fidelity parity slice series (guard `TestPort_PgDumpConnectionSetup`,
`internal/testport/pgdump_connsetup_test.go`). Landed slice 436 (435 was
prior max): `ALTER TYPE ... ADD VALUE [BEFORE|AFTER]` + `RENAME VALUE` on
an enum, asserting `pg_dump`'s folded `CREATE TYPE ... AS ENUM (...)`
label order matches real local PG 18.3's own pg_dump output for the
identical DDL sequence (verified by spinning up a real PG 18.3 cluster
from `postgres/local_install/bin` as ground truth — no engine bug found,
`catalog.AddEnumValueResult`/`RenameEnumValue` already compute correct
float4 midpoint sort orders; this was previously only unit-tested at the
parser layer, never end-to-end through pg_dump). Regression-guard-only
slice, no production code changed.

Files (all committed, see a783a8a1): `internal/testport/
pgdump_connsetup_test.go` (+3 ALTER TYPE statements to the `mood` enum
fixture, presence check + strict exact-sequence assertion on the full
`CREATE TYPE public.mood AS ENUM (...)` block), `.ralph/deferral_ledger.md`
(new resolved row), `docs/design/0110-0001-pg-dump-tap-port.md` (new
"Slice 436" section with real-PG verification transcript). `.ralph/
fix_plan.md` intentionally untouched (M0119-0004 is a living slice-by-
slice item, not closable in one slice).

Key symbols: `catalog.AddEnumValueResult`/`catalog.RenameEnumValue`
(internal/catalog/catalog.go ~16330-16530, float4-midpoint sort-order +
`renumberEnumValues` fallback), `pg_enum` VirtualRows (same file ~6425).

Next step: pick the next untested pg_dump construct for slice 437. Two
candidates the agent surfaced but did NOT bundle into this slice (each
needs its own real-PG-verified probe first): (1) `GRANT ... GRANTED BY`
grantor round-trip fidelity, (2) user-defined `CREATE TEXT SEARCH
{PARSER,TEMPLATE,DICTIONARY,CONFIGURATION}` objects (only the empty-view
built-in-filtered case is covered so far, per slices 12-15 in the same
file's package doc comment). Otherwise keep following the established
slice pattern: grep `Slice [0-9]+` in the test file's doc comment for
already-covered ground, spin up real local PG 18.3
(`postgres/local_install/bin`) to get verified ground-truth output before
assuming any divergence is a bug, land ONE slice per loop.

Gates run (this loop, via the delegated agent + my own re-verification):
`go build ./...` clean; `go test -count=1 -run
TestPort_PgDumpConnectionSetup ./internal/testport/ -v` PASS; `go test
-count=1 ./internal/executor/... ./internal/planner/... ./internal/
parser/... ./internal/catalog/...` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pre-commit pgbench smoke PASS at commit time; `make
ralph-state-guard` OK (auto-repaired the usual stale completed-marker
pattern).

Note: an untracked `postgres` directory shows build-artifact content
(GNUmakefile, config.log, etc.) — pre-existing, not touched or committed
(carried forward from prior loops). `.ralph/progress.json` shows as
modified (driver-owned state file, not mine to stage/commit).
