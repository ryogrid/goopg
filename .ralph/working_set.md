(idle — nothing in flight)

Loop #37 landed doc 04 §5.1-5.3 + §6 in full (the WAL native→PG-format
rework's canonical-family/knob/skip-tag REMOVAL — the last unimplemented
part of doc 04, following loop #36's §5.4 atomic classify+recovery dispatch
landing). This closes the epic's dispatch/removal scope entirely; only the
separately-scoped record body/content rewrite (docs 01/03) remains, not
started.

Summary of what landed (one commit):
1. Deleted `internal/catalog/canonical.go` + `internal/wal/parameter_change.go`
   (relocated `GUCParameters`/`DefaultGUCParameters` into `checkpointer.go`
   first) + 10 pure-canonical test files.
2. Unwired every `LogCanonical`/`PgCanonical*` call site across
   executor/initdb/server/vacuum. `writeHeapRowCanonical` now just delegates
   to `writeHeapRowReturningPG` (its ~20 catalog-heap-sync callers unchanged).
3. Removed the `GOOPG_WAL_CANONICAL` knob (`emitCanonicalDefault`,
   `wal.Config.EmitCanonical`, `Writer.CanonicalEnabled()`, the startup/
   BASE_BACKUP warnings).
4. Removed the `payload[0]==0xFE` branches in `wrapXLogMainData`/
   `classifyXLogRecord` (format.go) + the mirrored branch in
   `predictXLogRecordLen` (this one had a live keystone-test dependency —
   `TestPredictXLogRecordLenMatchesEncodeRecordXLog`'s `canonical_minimal`/
   `canonical_medium` subtests failed until the mirror was fixed too; deleted
   `TestPredictXLogRecordLenCanonicalShortCircuitsFirstByte` outright) +
   `RecordKindCanonical`.
5. Converted 4 `skipUnlessCanonicalWAL`-gated tests to unconditional
   `t.Skip` (doc §6); removed the now-redundant `TestKillKillRecoveryNativeOnly`
   and 3 now-inert `t.Setenv("GOOPG_WAL_CANONICAL",...)` calls; removed the
   nightly canonical-on lane from `ci/batch/stages/stage-testport.sh`; fixed
   `TestBaseBackupWireProtocolFraming` (asserted a NoticeResponse this loop
   removed).

Empirical deviation from doc §6's own prediction (recorded in the ledger,
not silently dropped): `TestPort_WALPgWaldumpCompat` (W-001) and
`TestPGWaldumpParsesEmittedWAL` were expected to become structurally-failing
once real rmids sit over native bodies — verified by direct re-run that
BOTH STILL PASS (pg_waldump's basic record walk doesn't validate per-rmgr
body content deeply enough to catch the mismatch). The port-status CSV
`port→defer` flip doc §6 specified was therefore deliberately NOT applied.

Gates run this loop (all green): `go build ./...`/`go vet ./...` clean
repo-wide; grep-audit clean (no `LogCanonical`/`PgCanonical`/
`RecordKindCanonical`/`GOOPG_WAL_CANONICAL`/`EmitCanonical`/
`CanonicalEnabled` outside the retained `xlogInfoDefault` legacy-decode arm);
G-crash (`go test -run 'Crash|Recovery|Durability' ./internal/initdb/
./internal/wal/'` + `TestKillKillRecovery`); goopg↔goopg E2E
(`TestE2E_NativeOnlyReplicationAndPromotion`,
`TestE2E_StandbyAttachRetainsUpstreamRowsAfterRestart`); G-race on
`./internal/wal/ ./internal/executor/ ./internal/catalog/`; full
`./internal/vacuum/... ./internal/server/... ./internal/initdb/...` (217s)
`./internal/testport/...` (1031s, whole package, no -run filter) all green;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke
bash scripts/ralph-precommit-test.sh` PASS (0 failed, all 3 workloads);
`make ralph-state-guard` clean (one auto-repaired stale-progress-marker
inconsistency, expected/benign, same pattern as loop #36).

Doc 04 marked landed (§2-§6 complete). `docs/design/README.md` updated.
`.ralph/deferral_ledger.md` gained a resolved-style row (status `-` per
ledger convention — M0119 flips status) that supersedes the 2026-07-13
perf-optimize3-dash S4 rows 756/757 (their "resume via
GOOPG_WAL_CANONICAL=on" path no longer exists).

Next step: no WAL-epic item left in fix_plan.md's dispatch/removal scope.
If picking up the epic again, start the record body/content rewrite from
`docs/design/wal-native-pg-format/01-emitted-wal-record-inventory.md` +
`03-pg183-wal-record-schemas.md` (already-landed blueprints) — this is a
large, separately-scoped epic, not a quick follow-up. Otherwise select the
next fix_plan.md priority (e.g. the Nightly whole-suite regression batch
implementation item, currently the next unchecked entry after the WAL
section).

In-flight: none.
