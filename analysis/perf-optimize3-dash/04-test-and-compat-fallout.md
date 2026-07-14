# 04 — Test and compatibility fallout

status: design · date: 2026-07-13 · base: `e453e3f2` · slices: S3 (guard),
S4 (refits/flips/ledger/docs)

## 1. The four breaking tests → assert-skip

Exactly four tests consume goopg-emitted canonical records and break under
native-only. Each is refit in S4 to **assert-skip**: the test detects
`EmitCanonical` off (env or a server probe) and `t.Skip`s with the
deferral-ledger reference — so a future canonical re-enable automatically
re-activates them, and an accidental non-skip run is loud.

| test | file | why it breaks |
|---|---|---|
| `TestE2E_FailoverGoopgToPG` (async + sync_remote_apply) | `internal/testport/e2e_failover_goopg_to_pg_test.go` | a **real PG standby** can only apply canonical records; a native-only stream is a no-op for it — the standby never sees rows |
| `TestE2E_ChecksumStreamingGoopgToPG` | `internal/testport/e2e_checksum_replication_test.go` | same consumer (pg_basebackup `-X stream` into real PG) |
| `TestPort_PgWaldump002SaveFullpage` (**WD-003**) | `internal/testport/pgwaldump_savefullpage_test.go` | extracts the FPI from the canonical `XLOG_HEAP_INPLACE`; native records carry no PG block-ref image |
| `TestPort_PgWaldumpVacuumPruneRoundtrip` (**WD-004**) | `internal/testport/pgwaldump_vacuum_prune_test.go` | decodes canonical `XLOG_HEAP2_PRUNE_VACUUM_SCAN` via `--rmgr=Heap2`; none emitted |

("Exactly four" was established by enumerating `LogCanonical` / `PgCanonical*`
/ `buildCanonicalPayload` call sites and grepping test assertions over live
WAL streams. False-positive warning for implementers: the
`TestPgOutput…EmitsCanonicalShape` tests in `pgoutput_test.go` assert pgoutput
*message* shape and consume native records — survivors, not breakers.)

## 2. Oracle-port CSV obligations (S4, same commit as the flip)

`docs/test-port/postgres-oracle-port-status.csv` — the validator
(`framework/status.go`) requires `defer` rows to carry a non-empty
`deferred_to`:

| row | id | change |
|---|---|---|
| 56 | WD-003 | `port`→`defer`, `deferred_to` = "native-only WAL (perf-optimize3-dash); resumes with EmitCanonical + C1", rationale updated |
| 57 | WD-004 | same |
| 71 | e2e-failover-goopg-to-pg-async | same |
| 72 | e2e-failover-goopg-to-pg-sync | same |

Then regenerate: `go run ./cmd/gen-oracle-port-status` (rewrites the .md and
re-runs the validator). `TestE2E_ChecksumStreamingGoopgToPG` has **no CSV
row** — code-level skip only. **W-001 and WD-001 stay `port`/`pass_required=yes`**
(structural readability and CLI-only; both keep passing — W-001 is the
design's tripwire, README R6).

## 3. BASE_BACKUP guard (S3)

`BASE_BACKUP` itself is family-agnostic (data files + pg_control + raw
pg_wal; `internal/server/basebackup.go`) and keeps working for
goopg→goopg cloning. The trap: a **real PG** bootstrapped from a goopg
basebackup will restore files fine and then fail (or silently no-op)
replaying native-only WAL — a delayed, confusing failure. S3 adds to the
BASE_BACKUP handler: when `EmitCanonical` is off, emit a **WARNING** to the
client + server log ("WAL stream is native-only; a PostgreSQL standby cannot
replay it — see deferral ledger"). Refusal is rejected (it would break
goopg→goopg cloning and pg_basebackup-based goopg backups, which remain
first-class).

## 4. Survivors matrix (no action; pinned here so nobody "fixes" them)

| surface | why it survives |
|---|---|
| goopg→goopg physical replication (`TestE2E_PhysicalReplication*`, standby-attach) | walreceiver copies raw bytes (`AppendRaw`, walreceiver.go:394); goopg standby replays native records |
| BASE_BACKUP / pg_basebackup ports (BB-010/011/020) | data-copy + raw-WAL streaming; no canonical decode |
| pgoutput logical replication (all `TestPort_PgoutputInterop*`) | classifier consumes native records only |
| PG→goopg failover (`TestE2E_FailoverPGtoGoopg`) | PG is the producer; goopg replays PG's canonical WAL — untouched |
| W-001 structural pg_waldump parity | native records are valid `RmgrXLog/0xF0` XLogRecords (doc 01 §3.4) |
| canonical machinery unit tests (`canonical_heap_roundtrip_test.go`, `canonical_tuple_bytes_test.go`, `catalog/canonical_test.go`, `parameter_change_test.go`, `copy_canonical_encoding_test.go`, `hot_update_encoding_test.go`) | they inject their own `LogCanonical`; keep as the resume path's regression guard (doc 01 D1: gate, don't delete) |

## 4a. Promotion + mixed-family e2e (S3, adversarial F-6)

goopg→goopg standby **promotion** under native-only and a **mixed-family WAL
directory** (canonical→native flip mid-life, incl. a basebackup taken across
the flip) get one explicit e2e each in S3 — both are believed-safe by
construction (fixed TLI=1 checkpoint records survive untouched; recovery
replays both families) but neither is currently pinned by a test.

## 5. Nightly lane (O-01-2, recommended)

One nightly matrix lane runs the unit + crash suites with
`GOOPG_WAL_CANONICAL=on` so the gated path keeps compiling and passing until
the resume. Cheap (env var), prevents bit-rot of the resume path.

## 6. Deferral-ledger rows (S4 — copy verbatim, 7-column format)

Convention note (implementability review F6): existing ledger rows use an
`M…` milestone id in `task-id`, and the CSV convention expects `deferred_to`
to reference a follow-up milestone. **Assign the owning milestone id at
implementation time** and substitute it for the `perf-optimize3-dash`
placeholder below (both in the ledger `task-id` column and the CSV
`deferred_to`); keep the prose in the rationale/why columns.

> `| - | <date> | perf-optimize3-dash | Single-stream native-only WAL landed
> (EmitCanonical default off; slices S1-S4 of analysis/perf-optimize3-dash/).
> W-001 structural parity retained. | Real-PG-standby compatibility:
> TestE2E_FailoverGoopgToPG (async+sync) + TestE2E_ChecksumStreamingGoopgToPG
> assert-skip when canonical off; CSV rows 71/72 port→defer. | Re-enable via
> GOOPG_WAL_CANONICAL=on, then land analysis/perf-optimize3/
> 05-improvement-designs/01 (C1 incremental canonical records) so re-enabling
> does not reintroduce the ~2×8.24KB/txn unconditional-FPI cost. | The
> canonical family double-logged every heap write with an unconditional 8KB
> image (perf-optimize3: 33KB WAL/txn vs PG 1.8-2.9KB); native family is
> already PG-shaped and structurally-valid PG WAL. |`

> `| - | <date> | perf-optimize3-dash | (same landing) | pg_waldump CONTENT
> parity: WD-003 (savefullpage) + WD-004 (vacuum-prune rmgr decode)
> assert-skip; CSV rows 56/57 port→defer. W-001 structural parity remains
> pass-required. | Same resume as the row above — content tests are only
> meaningful when canonical records exist. | Content tests decode canonical
> rmgr payloads that native-only does not emit. |`

> `| - | <date> | perf-optimize3-dash | Sys-catalog btree pages (2662/2663/
> 2659) keep torn-page safety via the native first-touch FPI (MarkDirty →
> maybeEmitFPI). | Intra-epoch sys-btree increments are unreplayed after a
> crash → possible missing on-disk index entries on the (deferred) real-PG
> read surface only; goopg never reads these btrees for its own lookups
> (in-memory catalog is authoritative; catalog HEAP is natively replayed). |
> Route the sys-btree writes through the native LogBtreeInsert hook, or
> rebuild sys-btrees from the catalog heap at startup, when real-PG compat
> resumes (rebuildSysBtreeWithNewEntry is the precedent). | Belt-and-
> suspenders WAL for a surface nobody reads would cost complexity now;
> acceptance is scoped to the deferred surface (audit: perf-optimize3-dash/02
> §2). |`

## 7. Doc supersessions / annotations (S4)

| doc | action |
|---|---|
| `analysis/perf-optimize3/05-improvement-designs/README.md` + `01-c1-…md` | one-line note: C1's FPI-gating machinery (§4.2) is inherited by perf-optimize3-dash/03; its record-shape work becomes the canonical **resume path**; C2/C3 unaffected |
| `docs/design/0103-0018-heap-fpi-and-logical-record-coexistence.md` | note: the canonical half of the coexistence is gated off by default; native/logical half unchanged |
| `docs/design/0101-0001-wal-page-header-compat-default.md` | note: PageHeaders (frame) and EmitCanonical (content) are now independent; PageHeaders stays ON |
| `docs/design/0101-0002-wal-pg-waldump-validation-test.md`, `0110-0002-pg-waldump-tap-port.md` | note: W-001 structural stays required; WD-003/004 deferred with ledger ref |
| M0102 failover designs (`0102-0003/0005/0006`) | annotate: goopg→real-PG replay deferred |
| M0106-0010 canonical wiring notes | annotate: gated off by default |
| `docs/design/wal_fsync_flow_primary.md` | minor: the commit path no longer appends/waits on the canonical commit record when off |
| `docs/test-port/postgres-oracle-port-status.md` | regenerated (§2) |

## 8. Open questions (flagged)

- **O-04-1**: assert-skip probe mechanism — env var visible to the test vs a
  `SHOW`-style server introspection; pick one and use it in all four refits.
- **O-04-2**: should the WARN in §3 also fire on walsender START_REPLICATION
  from a non-goopg client (real PG walreceiver identifies via
  application_name/protocol nuances)? Nice-to-have; not required for S4.
