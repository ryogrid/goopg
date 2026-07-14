# perf-optimize3-dash — single-stream (native-only) WAL

date: 2026-07-13 · designs against goopg `e453e3f2` (all file:line citations)
— repo tip at authoring time `8c727dad` (adds only analysis/docs on top of
`e453e3f2`; no code drift in the cited paths) · status: design only
(implementation not started)

## Goal

Change goopg's internal WAL design so that **only one record family is
written — the native goopg family**, which already behaves like PostgreSQL's
WAL: a full-page image only on the first modification of a page after a
checkpoint, compact incremental records otherwise. The canonical (PG-format)
family — every record carrying an unconditional 8 KB FPI, double-logging every
heap write, measured at ~2×8.24 KB/txn of pure image payload — stops being
emitted (behind a default-off switch, not deleted).

This is the pragmatic alternative to
[`../perf-optimize3/05-improvement-designs/01`](../perf-optimize3/05-improvement-designs/01-c1-incremental-canonical-heap-wal.md)
(C1), which kept both families and made canonical incremental. C1 becomes the
**resume path** for when real-PG-standby compatibility returns.

## Non-goals (deferred, recorded in `.ralph/deferral_ledger.md`)

- **Real-PG-standby replication** (a PostgreSQL 18 instance attaching to goopg
  and replaying its WAL) — the only consumer that needs canonical records.
- **pg_waldump content parity** (WD-003 savefullpage, WD-004 vacuum-prune
  rmgr decoding). Structural parity (**W-001**) is retained and must keep
  passing — native records are valid PG `XLogRecord`s (`RmgrXLog`/`0xF0`).

Everything else survives: **goopg→goopg physical replication** (the
walreceiver copies raw bytes — family-agnostic), BASE_BACKUP, pgoutput logical
replication, PG→goopg failover, and goopg's own crash recovery (which has
always replayed the native family).

## Documents

| Doc | Content |
|---|---|
| [01-single-stream-wal-design.md](01-single-stream-wal-design.md) | the `EmitCanonical` switch, the three emission choke points, retained control records, PageHeaders decoupling, mixed-family WAL dirs |
| [02-canonical-only-coverage-audit.md](02-canonical-only-coverage-audit.md) | the correctness core: every state change that today has ONLY a canonical record, and why native-only recovery still works (or what covers it) |
| [03-checkpoint-redo-ordering-fix.md](03-checkpoint-redo-ordering-fix.md) | the latent image-less replay window (epoch reset after redo sample) — mandatory to fix once the native FPI epoch is the sole torn-page protection |
| [04-test-and-compat-fallout.md](04-test-and-compat-fallout.md) | the 4 breaking tests → assert-skip, CSV flips, BASE_BACKUP guard, survivors matrix, ledger rows (verbatim), doc supersessions |
| [05-expected-performance.md](05-expected-performance.md) | volume model (−50 % immediate; PG-convergent with amortization), measurement protocol |

## Migration slices (implementation roadmap the docs specify)

| # | slice | behavior | gates |
|---|---|---|---|
| **S1** | Introduce `EmitCanonical` (wal.Config bool + `GOOPG_WAL_CANONICAL` env), wired at the three choke points, **default ON** — pure refactor | identical | G-unit, G-race, G-standby + G-waldump quick pass (stream unchanged) |
| **S2** | Checkpoint redo-publication fix (doc 03, **option (b): per-record `page_lsn ≤ publishedRedo` test** — the sweep-based option (a) was rejected by adversarial review) — mode-independent; lands while canonical is still ON so double coverage masks any regression | behavioral (checkpoint + MarkDirty internals) | G-crash full (incl. `TestKillKillRecovery` + the new window tests incl. the catalog variant), G-race, G-unit |
| **S3a** | Coverage-audit hardening, tests only (doc 02): full-server crash suites under canonical-off (mechanism: the `Open()`-time env default / `WithCanonical` helper — doc 01 §3.1; for the cluster harness, add `cluster.Options.Env` or confirm the `start` subprocess inherits `os.Environ()`); catalog-heap + ALTER-re-sync + datfrozenxid crash tests; mixed-family flip restart test; §7 completeness guard | identical in default mode | G-crash **in both modes**, G-unit, G-race |
| **S3b** | BASE_BACKUP off-mode WARN (prod code — `replyBaseBackup` has no in-file notice sender today; wire the protocol NoticeResponse helper + read `walWriter.CanonicalEnabled()`); promotion + mixed-family e2e; sys-btree accept-drift decision + ledger row | behavioral (one WARN) | G-unit, basebackup ports (BB-010/011/020), G-race |
| **S4** | **Default flip OFF** + the 4 assert-skip refits + CSV flips + ledger rows + doc supersessions; **W-001 must PASS** | **behavioral (headline)** | G-unit, G-crash (native-only primary), G-standby→assert-skip, WD-003/004→assert-skip, W-001 PASS, G-tpch |
| **S5** | Perf re-measure both modes (`run_rw50.sh` + `aux2_fsync_probe.sh`); record vs the doc-05 model | measurement | G-perf |

**Critical ordering: S2 and S3a/S3b before S4** — the flip must land with
the torn-page fix in place and the native-only crash gate already green.

Gate names (G-race/G-crash/G-standby/G-waldump/G-unit/G-tpch/G-perf) and their
**activation requirements** (non-`-short`, PG 18 binaries on PATH, env gates —
a default `go test ./...` silently skips the headline ones) are defined in
[`../perf-optimize3/05-improvement-designs/README.md`](../perf-optimize3/05-improvement-designs/README.md#common-gates-referenced-as-g--by-every-slice-table)
and reused verbatim, with these flips from S4 on: G-standby's goopg→PG
failover tests and G-waldump's WD-003/004 become **assert-skip** (the test
must detect canonical-off and skip with the ledger reference — a hard failure
if it runs and passes/fails any other way); W-001 stays a hard PASS.

## Risk register

- **R1 — catalog heap recovery is load-bearing on WAL replay.**
  `loadUserTablesFromHeap` is "the sole catalog recovery path"
  (open.go:1228). Under native-only, catalog heap inserts automatically start
  emitting native `RecordKindHeapInsert` (the nil-hook interaction, doc 02
  §1) — S3's crash test is the proof obligation, not an optional extra.
- **R2 — the latent image-less window (doc 03) exists TODAY** for the native
  family; canonical's unconditional FPIs currently mask it — most sharply on
  the catalog-insert path (`MarkDirtyLogicalChange`), where a torn
  pg_attribute page would corrupt the schema itself. Removing canonical makes
  it the primary torn-page surface — S2 (option (b), per-record redo test)
  precedes S4, no exceptions.
- **R3 — BASE_BACKUP under canonical-off is a trap for real PG**: the copy
  succeeds, the subsequent WAL is unreplayable by PG → fails mid-replay, not
  at attach. S3 adds a WARN (or refusal) at BASE_BACKUP time when
  `EmitCanonical` is off (doc 04 §3).
- **R4 — sys-catalog btree intra-epoch drift** (doc 02 §2): accepted, ledger
  row; the pages goopg itself never reads for lookups.
- **R5 — mixed-family WAL directories** (a cluster flipped off mid-life):
  recovery replays both families already; stated as supported, cheap test in
  S3.
- **R6 — W-001 is the tripwire**: if native records ever stop being
  structurally-valid PG XLogRecords, the whole "PG-framed stream, native
  content" premise breaks. W-001 stays pass-required in every slice.

## Relationship to prior work

- `../perf-optimize3/` — the measurement + attribution this acts on.
- `../perf-optimize3/05-improvement-designs/` — C1 (incremental canonical) is
  **partially superseded**: its gating machinery (canonical-image token +
  RedoRecPtr publication, its §4.2) is *inherited* here as doc 03; its
  record-shape work (incremental `xl_heap_*`) is deferred to the resume path.
  C2 (CLOG fsync) and C3 (btree LP_DEAD) are unaffected and remain live.
- `docs/design/wal-backend-flush/` — the locking architecture underneath;
  untouched.
