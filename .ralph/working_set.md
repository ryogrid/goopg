Task: `wal_sync_method` GUC real implementation (`unimplemented_feat.json`
entry, M0007). COMPLETE and committed this loop (`44772d9d`).

Files: internal/config/defaults.go (new `wal_sync_method` TypeEnum GUC,
ContextSigHup, options fsync/fdatasync/open_sync/open_datasync, default
fdatasync); internal/config/postgresql.conf.sample (documented entry);
internal/wal/writer.go (new `SyncMethod string` Config field,
`withDefaults` normalizes empty→"fdatasync", `NewWriter` validates and
rejects unimplemented values via new `ErrUnsupportedSyncMethod`, new
`state.doSync(f)` dispatch used by `flushUpTo` instead of calling
`dataSync` directly); internal/wal/sync_linux.go + sync_other.go (new
`fullSync` helper alongside existing `dataSync`, same build-tag split);
internal/wal/sync_method_test.go (new: default→fdatasync, fsync+fdatasync
both round-trip Append→FlushUpTo→Close→ReadAll, open_sync/open_datasync/
bogus all reject via errors.Is(ErrUnsupportedSyncMethod)); cmd/goopg/main.go
+ internal/initdb/open.go (GUC→OpenOptions.WALSyncMethod→wal.Config.SyncMethod
plumbing, same pattern as wal_init_zero/wal_buffers); unimplemented_feat.json
(surgical 2-field edit only, per house rule — status→resolved, code_audit
rewritten; verified valid JSON via json.load after, did NOT run
json.load+json.dump); .ralph/deferral_ledger.md (new row: open_sync/
open_datasync deliberately NOT implemented — need O_SYNC/O_DSYNC at every
WAL segment-open site, not just the flush barrier; resume point is in the
ledger row); docs/design/0007-0002-fdatasync-commit-path.md (new
"Follow-up (2026-07-08)" section) + docs/design/README.md (0007-0002 row
addendum).

Key symbols: `state.doSync` (writer.go, new dispatch point);
`ErrUnsupportedSyncMethod` (writer.go); `fullSync`/`dataSync`
(sync_linux.go / sync_other.go); `wal_sync_method` GUC (defaults.go).

Findings: confirmed via `git stash` on the 4 implementation files (writer.go,
sync_linux.go, sync_other.go, defaults.go) that sync_method_test.go fails to
even COMPILE without them (not just fails an assertion) — non-vacuous.
Scoping came from an Explore-agent investigation first: single fsync call
site (writer.go flushUpTo), one GUC registration pattern to follow
(io_method's ErrUnsupportedMethod precedent in internal/aio), and Linux only
realistically supports fdatasync/fsync out of the box — open_sync/
open_datasync need O_SYNC/O_DSYNC at file-open time across multiple call
sites, correctly scoped out as a separate follow-up rather than attempted
in the same loop.

Next step: pick a fresh item next loop. Un-investigated candidates from
unimplemented_feat.json (grep `"status": "open"` + `"confidence": "high"`,
avoid multi-loop architectural items like M0119-0006's btree opclass
dispatch or the ~25-item streaming-replication group): "Counters for WAL
preallocation metrics" (task_id M0007, small/self-contained, sibling of this
loop's work); "parsePrimaryConninfo sslmode/password" (PARTIAL per the
existing code_audit — user/application_name already landed, only sslmode/
password remain, plus a stale doc comment to fix); "restart command for
goopg ctl control socket" (currently a stub — scope-check first whether a
foreground v0 server can meaningfully support `restart` at all before
committing to it). Also: the open_sync/open_datasync O_SYNC/O_DSYNC follow-up
from THIS loop's deferral ledger row is itself a viable next pick if nothing
above looks better-scoped.

Gates run: go build ./... clean. go vet ./... clean. go test
./internal/wal/... (full package, includes new sync_method_test.go) PASS.
go test ./internal/config/... PASS (includes postgresql.conf.sample↔registry
coverage check, which initially failed until the sample file was updated).
go test ./internal/executor/... ./internal/planner/... ./internal/catalog/...
PASS (cached, unaffected). go test ./internal/initdb/... (full package,
234s, exercises the real wal.Config wiring end-to-end) PASS.
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33). RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh PASS (0 failed, all 3 workloads) — run twice,
once standalone and once as the actual git pre-commit hook on the real
commit. make ralph-state-guard: 1 benign issue auto-repaired (same recurring
status/progress clean-exit-vs-in_progress reconciliation noted every prior
loop — not new, do not chase it).

In-flight: none directly mine. The independent nightly-batch run
(ci/logs/20260708-064334, sha=2e435e91, started 06:43 this morning per
[[goopg_nightly_ci_batch]] memory) was still mid-flight at this loop's start
(race PASS at 07:08, testport/pgbench/tpch stages not yet reached — testport
has a 120m budget) and is NOT something this loop started or should wait on.
Its base sha includes the M-NIGHTLY pgbench/nightly item's real root-cause
fix (`8ebb71cd` flushBatch stale-tag fix, landed loop #17 of that 18-loop
investigation) — once this run completes, check `ci/logs/latest/stages/
pgbench/` for a clean result; if clean, that's the real confirmation the
`pgbench/nightly` M-NIGHTLY bullet (fix_plan.md line ~64, currently `[x]`
but deliberately left un-archived pending this exact confirmation per its
own note at line ~624) has been waiting on — archive/drop it then. If
`ci/logs/action-items.md` has a newer mtime than this loop's start
(2026-07-08 ~07:11) by the time the next loop begins, re-run the standard
nightly-triage step (grep `## AI-` items lacking an open fix_plan task)
before picking new work — this loop found nothing new to triage (all 8 items
from the stale 20260707-000712 run already have checked M-NIGHTLY bullets).
