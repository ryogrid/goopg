(idle — nothing in flight)

Loop #22 landed the M0119-0004 slice-437/446 restart-persistence follow-up:
`CREATE TEXT SEARCH DICTIONARY` and `CREATE TEXT SEARCH CONFIGURATION` /
`ALTER ... ADD MAPPING` now survive a server restart (previously vanished —
recorded as a deferral in both slice 437 and 446's own ledger rows).
Mirrors the CAST/TRANSFORM/CONVERSION/COLLATION restart-persistence
precedent exactly.

Files: `internal/wal/recovery.go` (5 new record kinds 104-108 + Encode/Decode
pairs + ApplyRecord case), `internal/catalog/catalog.go` (5 new
`*DuringRecovery` hooks), new `internal/initdb/tsdict_ddl_recovery.go` +
`internal/initdb/tsconfig_ddl_recovery.go` (wired into `open.go` right after
`replayConversionDDLRecords`), `internal/executor/operators_ddl.go` (WAL
emission at all 5 mutation sites: CREATE TSDict/TSConfig, ADD MAPPING, DROP
TSDict/TSConfig). New tests: `internal/wal/tsdict_tsconfig_ddl_test.go` (16
encode/decode cases), `internal/initdb/tsdict_tsconfig_ddl_recovery_test.go`
(6 end-to-end Init→Open→WAL-append→Close→Open cycles). Design doc:
`docs/design/0110-0001-pg-dump-tap-port.md` new "Slice 437/446 follow-up"
section; `docs/design/README.md` row updated. Deferral ledger: new resolved
row closing both slices' restart-persistence deferral in full (other
deferred items from those rows — 42710 duplicate-mapping, other ALTER TEXT
SEARCH CONFIGURATION forms, option validation, PARSER/TEMPLATE — remain
open, untouched this loop).

Gates this loop: `go build ./...` clean; `go test -race ./internal/wal/...`
PASS; `go test -count=1 ./internal/wal/... ./internal/catalog/...
./internal/initdb/...` PASS (all new tests green); `go test -count=1
./internal/executor/... ./internal/parser/... ./internal/planner/...
./internal/analyzer/...` PASS; `go test -count=1 -run
TestPort_PgDumpConnectionSetup ./internal/testport/ -v` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `make ralph-state-guard`
OK (auto-repaired the running/completed status mismatch, as every loop
does). pgbench pre-commit smoke runs via the git hook at commit time.

Next candidate (not started): per the fix_plan "Next up" addendum, either
(a) 42710 duplicate-mapping detection in `execAlterTSConfigAddMapping`
(`internal/executor/operators_ddl.go` — needs a pre-check against
`UserTSConfig.Mappings` for an existing `TokenType` before appending), or
(b) the `ALTER TEXT SEARCH CONFIGURATION RENAME TO/SET SCHEMA/DROP MAPPING`
forms (parser dispatch in `internal/parser/ddl.go` currently discards all
non-ADD-MAPPING forms as a compat no-op) — check `.ralph/fix_plan.md` and
re-scan the deferral ledger first in case a concurrent loop already picked
one up.
