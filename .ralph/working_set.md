Loop #35 landed doc 04 §5.4's THIRD additive slice: `internal/wal/format.go`
gained `recordKindToRmgrInfo(kind byte) (Rmgr, uint8)` — the full §3.1
PG-analog mapping table + §3.2 custom-rmgr default, unit-tested against
every doc row (`internal/wal/record_kind_rmgr_mapping_test.go`). Needed
opcode consts added to `pg_xlog_decode.go` (`xlogHeapLock`/
`xlogBtreeInsertLeaf`/`SplitL`/`SplitR`/`UnlinkPage`/`NewRoot`/
`MarkPageHalfDead`/`Vacuum`/`xlogSmgrCreate`/`xlogXLogFPI`/
`xlogClogTruncate`), confirmed against PG 18.3 source. Deliberately NOT
wired into `classifyXLogRecord` — the working-set baton from loop #34
assumed that was next, but tracing it found it CANNOT land alone (would
break goopg's own crash recovery). Full findings recorded in doc 04 §5.4
(4th bullet, 5 numbered points) and mirrored in the ledger row appended
this loop:
1. `ApplyRecord`'s rmid gate (`recovery.go`, "M0106-0011" comment) assumes
   every native record classifies `RmgrXLog`/`0xF0` — must become
   `!isGoopgOwnedRmgr(Rmid)` (new helper: Heap/Heap2/Btree/Xact/Storage/
   CLOG/GoopgCatalog).
2. Decode side is unaffected FOR FREE — `nativeHeaderMatchesMainData`
   (`pg_xlog_decode.go:241-247`) already recomputes `classifyXLogRecord`
   symmetrically at decode time, so `r.Payload` still populates correctly
   for every no-block native record once classify's output changes.
3. `replayDecodedXLogRecord`'s `default:` arm HARD-ERRORS (aborts
   recovery, not silent) for ~80 catalog/DDL kinds not in
   `nativeApplyRecordKindKnown`'s allow-list, once they get
   `Rmid=RmgrGoopgCatalog` — needs one new `case RmgrGoopgCatalog: return
   false, nil`.
4. `RecordKindPageImage` → `RmgrXLog`/`XLOG_FPI` (§3.1) genuinely collides
   with real PG semantics: would route to `replayDecodedXLogRecord`'s
   `RmgrXLog` default (`return false, nil`), SILENTLY dropping a real
   page-image restore that `replayPageImage` currently performs — the R1
   failure mode materializing concretely. Needs a new `case xlogXLogFPI:`
   arm delegating to `replayPageImage(mgr, r.Payload)`.
5. Canonical (0xFE) FPI arms (`RmgrHeap`/`RmgrBtree` in
   `replayDecodedXLogRecord`) must stay UNTOUCHED this slice — they're
   reached via the empty-payload gate regardless of rmid, and canonical
   emission (§5.1-5.3) hasn't been removed yet (active call sites still
   depend on FPI replay). Do NOT "replace" them yet as the original §4
   draft pseudocode suggested.

Also found (documented, left untouched): a STALE, uncommitted worktree at
`.claude/worktrees/wal-canonical-removal/` (branch
`wal-canonical-removal`, HEAD `e9884a60` — before the additive-first
slices landed) attempting the OPPOSITE (subtractive-first: deletes
canonical.go outright) ordering. Orphaned, not referenced anywhere in
fix_plan/ledger — do not resume/merge as-is. Full note in doc 04 §8 R3.

Next step for the WAL epic: implement the ONE atomic change — `internal/
wal/format.go`'s `classifyXLogRecord` rewrite (call `recordKindToRmgrInfo`)
+ `internal/wal/recovery.go`'s §4 dispatch rework (the 5 points above) +
`internal/wal/stream_replayer.go:159`'s `replayedXactInfo` (add a
`RecordKindXactCommitInval` case) — ALL IN ONE COMMIT, not split across
loops (splitting leaves HEAD's crash recovery broken in between). Read
doc 04 §5.4 (4th/5th bullets) for the exact per-point fix before starting.
Gate: full G-crash (`go test -run 'Crash|Recovery|Durability'
./internal/initdb/ ./internal/wal/` + `TestKillKillRecovery`) BEFORE
touching anything (establish the pre-change baseline) AND after, per §8
R1. When done, delete `TestRecordKindToRmgrInfoNotYetWired`
(`internal/wal/record_kind_rmgr_mapping_test.go`) — it's designed to fail
the moment wiring lands.

Gates run this loop: `go build ./...`, `go vet ./...` clean; `go test`
and `go test -race ./internal/wal/...` full-package green; verified
`recordKindToRmgrInfo` inert (no non-test caller, grepped); `make
ralph-state-guard` clean; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS
(0 failed, all 3 workloads).

In-flight: none.
