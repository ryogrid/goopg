(idle — nothing in flight)

Last loop (#50): M-NIGHTLY regress divergences — the surviving three were
**never measured**, and the real defect was **crash restart**. Fixed as
root-0032 (`docs/design/root-0032-crash-restart-wal-stream-anchoring.md`).

The prefix run through `select_distinct` (176 cases, 670 s) showed `misc`
timed out, root-0029's recovery restart then FAILED, and all 53 remaining
cases (#123..#176 — including `portals_p2`, `select`, `select_distinct`)
reported a phantom `deferred: cluster restart failed`. Only `index_including`
produced a genuine `output mismatch`. The restart failed with
`wal replay: decode at offset 771751920: invalid record header: unknown
rmid=31` — goopg could not start after a crash once retention had run.
Reproduced standalone (`analysis/wal-crash-restart-repro.sh`: pgbench -c 16,
kill -9, restart; fails at ~570 MB of WAL). Three causes, one theme — both
scanners assumed the stream begins on a record boundary and stays valid:
a hole in pg_wal (normal residue of a SIGKILL during `removeOldSegments`,
which walks newest-first) was fatal; a segment opening with
XLP_FIRST_IS_CONTRECORD had its continuation decoded as a record header
(which was ALSO destroying 54–97 durable records on every clean reopen);
and an unreadable tail was an error instead of PG's end-of-WAL.

Next M-NIGHTLY step (still open, preempts M0124): (a) re-run the same prefix
and confirm it now REACHES `portals_p2`/`select`/`select_distinct`, then
measure them; (b) `index_including`'s real divergence; (c) root-0032 §5 — the
same repro now fails one stage later in redo (`heap-update add new tuple: not
enough free space in page`), so a crash under load still leaves an unstartable
cluster (ledger 2026-07-28); (d) the harness's phantom `deferred:` per case
after a failed restart (ledger, same date). ALWAYS grep a suite log for
`restarting the cluster` / `restart failed` before believing a case result.

Gates run: `go test ./internal/wal/ ./internal/initdb/ ./internal/storage/`
PASS; `go test -race ./internal/wal/` PASS; negative control on both halves of
the fix (each new test fails with its fix disabled, so non-vacuous);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (exit 0);
pgbench smoke via the commit hook. tpch-spotcheck deliberately not run — no
planner/executor/codec change (WAL read path only); the crash repro is the
end-to-end evidence instead.
In-flight: none.
