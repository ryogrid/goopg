Task: storage/aio-relfile-mu-bypass (fix_plan.md ~line 1300) — PARTIALLY
RESOLVED this loop (checksum-bypass half fixed; item stays unchecked
because the original per-block ordering half is still open).

Files modified (all committed as f1228a65, pushed):
internal/aio/aio.go (new ChecksumFile interface), internal/aio/
method_iouring_linux.go (Submit/submitOne/completeOne/pendingOp wired
to call PrepareWrite before SQE submission for writes and VerifyRead
after completion for reads), internal/aio/method_iouring_linux_test.go
(new TestEngineIOUringChecksumFileHooks), internal/storage/smgr.go
(relFile.PrepareWrite/VerifyRead implementing ChecksumFile
structurally), internal/storage/checksum_io_test.go (new
TestChecksumRelFilePrepareWriteVerifyRead), docs/design/
0009-0006-aio-io-uring.md (new section), docs/design/README.md
(0009-0006 row appended), .ralph/fix_plan.md (task entry updated,
left unchecked).

Findings this loop: investigating the 13th-loop's "relFile.mu bypass"
finding showed MethodWorker/MethodSync were NEVER actually bypassed
(runOp calls op.File.ReadAt/WriteAt, and relFile.ReadAt/WriteAt already
take relFile.mu + apply checksums) — only methodIOUring's raw-fd
fdHaver path bypasses File.ReadAt/WriteAt. That bypass had a second,
more concrete consequence nobody had named yet: on a checksummed
cluster with io_method=io_uring, every AIO write persisted a stale
pd_checksum (never stamped) and every AIO read skipped verification —
and a LATER synchronous read of an AIO-written block would then report
a false-positive ChecksumError for a page that was never corrupted.
Fixed via aio.ChecksumFile{PrepareWrite,VerifyRead}, wired into
Submit/completeOne, implemented by relFile via the same helpers
WriteAt/ReadAt already use. Verified non-vacuous (reverted wiring only,
confirmed the new aio test fails: 0 PrepareWrite calls, unstamped bytes
on disk).

Next step for a future loop: the ORIGINAL per-(rel,block) same-tag
ordering concern (a concurrent synchronous readBlock/writeBlock/extend,
or another io_uring op, on the SAME block of the SAME relFile is still
not serialized against an in-flight io_uring raw-fd op) remains open.
Per the 2026-07-08 M-NIGHTLY deferral-ledger row's hand-trace,
Slot.contentMu's RWMutex already prevents this from being reachable via
the ONE production call site (Pool.flushBatch's WriteBlockAIO) today —
so this is latent-risk, not confirmed-reachable, but still worth
closing properly. Design (don't rush) a per-(rel,block) in-flight-AIO
registry consulted by readBlock/writeBlock/WriteBlockAIO/PrefetchBlock,
NOT a blanket per-file mutex (would regress checkpointer throughput via
methodIOUring's already-file-scoped submitMu comparison point). Verify
with a targeted concurrency test: real io_uring engine + a concurrent
synchronous smgr call to the exact same (rel,block).

Gates run this loop: go build ./... / go vet ./... clean; go test
./internal/aio/... ./internal/storage/... ./internal/initdb/... PASS;
go test -race -run TestEngineIOUring ./internal/aio/... PASS;
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33);
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh PASS (0
failed txns, all 3 workloads, both standalone and via the pre-commit
hook); make ralph-state-guard PASS (auto-repaired a stale
status/progress marker, unrelated to this loop's work).

In-flight: none. No background processes left running. Commit
f1228a65 pushed to origin/align-data-structure-with-pg.
