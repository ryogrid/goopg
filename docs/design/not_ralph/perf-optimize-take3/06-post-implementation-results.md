# 06 — Post-implementation results (candidates A, B, C)

Candidates **A** (PG fast-path weak relation locks), **B** (allocation-free
btree comparison) and **C** (pooled goyacc parser) from
[05-improvement-plan.md](05-improvement-plan.md) are implemented and landed in
commit `ac0fd1267`. This chapter reports the measured outcome and re-profiles
the engine on top of them.

- **Base commit for this chapter**: `ac0fd1267` (branch `perf-opt-take3`)
- **Baseline it is compared against**: `9ecc840b5`, the study in
  [01-results.md](01-results.md)
- **Date**: 2026-08-30
- **Raw artifacts**: `tmp/take3/runs/{ab-S,ab-N-r1r2,ab-N,N1-post-S,N2-post-N,N3-post-S-contention,N4-post-S-alloc}/`

## 1. Headline

Configuration is unchanged from [00-methodology.md](00-methodology.md): scale
100, `-c 50 -j 50`, simple protocol, `fsync=on`, `synchronous_commit=on`.

### 1.1 A/B, alternating, one cluster

The throughput claim comes from an **alternating A/B** — both binaries against
the same data directory, fresh server restart before every run, runs interleaved
so page-cache state, index growth and thermal drift cancel. No profiler
attached.

| workload | baseline `9ecc840b5` | with A+B+C | delta |
|---|---:|---:|---:|
| `-S` select-only | 92,191 tps (n=2, sd 2,307) | **111,927 tps** (n=2, sd 395) | **+21.4 %** |
| `-N` simple-update | 8,487 tps (n=5, sd 788) | 8,360 tps (n=5, sd 660) | −1.5 % |

`-S` ranges are **disjoint** (base 90,559–93,822; new 111,648–112,206). `-N`
ranges **overlap heavily** (base 7,712–9,803; new 7,273–8,939) and the −1.5 %
difference is a fraction of one standard deviation.

The baseline arm reproduces the original study (92,191 vs 93,083 for `-S`),
which is the control that makes the comparison trustworthy.

### 1.2 Against the PostgreSQL oracle

| workload | goopg before | goopg after | PG 18.3 | gap before | gap after |
|---|---:|---:|---:|---:|---:|
| `-S` select-only | 93,083 | **111,927** | 114,388 | 1.23× | **1.02×** |
| `-N` simple-update | 10,786 | (unchanged) | 11,994 | 1.11× | 1.11× |

**The read gap is effectively closed** — goopg is within 2.2 % of PostgreSQL
18.3 on the same host under identical settings.

### 1.3 Why `-N` did not move, and why that is the expected result

Not a disappointment; it is what [01 §2](01-results.md) predicted:

- `-N` is **commit-flush-bound**. `END` is ~70 % of its transaction latency and
  was already at **1.008× of PostgreSQL** ([04](04-wal-persistence.md)). None of
  A, B or C touches the WAL path.
- **Candidate A does not apply to `-N` at all.** The fast path is used only by
  the transient acquire-then-release path, which is the *autocommit* route. A
  pgbench `-N` transaction is explicit (`BEGIN … END`), so
  `TxnLockBackendID != 0` and every statement takes `acquireRelLockTxn`, whose
  locks are held to end-of-transaction and must remain visible in the table.
- B and C do help `-N`'s statements, but those statements are ~30 % of a
  transaction whose remaining 70 % is an `fdatasync`.

`-N` is also the noisiest workload measured here (sd ≈ 700 tps ≈ 8 %), because
its cost is dominated by disk flush timing. Any future `-N` claim needs more
repetitions than a read-path claim.

## 2. Did the three mechanisms actually do what they were supposed to?

Each candidate was predicted to remove a specific, named cost. All three did,
and each is verified by an instrument independent of the throughput number.

### 2.1 A — `Lock:relation` is gone, and mutex delay halved

| measure (`-S`) | before | after |
|---|---:|---:|
| backend samples in `active / Lock:relation` | 3,521 (**19.9 %**) | **0 (0.0 %)** |
| total mutex delay | 1,715.19 s | **778.55 s (−54.6 %)** |
| `acquireRelLockMaybeTransient` share of mutex delay | 65.12 % | **absent from the profile** |
| release path (`ReleaseTupleLocks` → `ReleaseAll`) | 25.71 % | **absent** |

The wait-event distribution now has the same shape as PostgreSQL's:

| goopg after | % | | PG 18.3 | % |
|---|---:|---|---|---:|
| `idle` / (none) | 51.2 | | `idle` / (none) | 49.6 |
| `active` / (on-CPU) | 28.8 | | `idle` / `Client:ClientRead` | 29.0 |
| `idle` / `Client:ClientRead` | 11.5 | | `active` / (on-CPU) | 12.2 |
| `idle` / `Client:ClientWrite` | 8.3 | | `active` / `Client:ClientRead` | 9.1 |

Note the wait-event share fell to *exactly* zero rather than merely shrinking.
That is the correct outcome and worth stating precisely: the probe window was
opened around **every** transient acquire, contended or not, so it was reporting
uncontended lock-table bookkeeping as a wait. Upstream reports no wait for an
uncontended fast-path grant either, so removing the window is parity, not
under-reporting. Real contention still surfaces — a conflicting strong lock
takes the slow path and the probe with it.

### 2.2 B and C — allocation cut by two thirds

| measure (`-S`) | before | after | delta |
|---|---:|---:|---:|
| allocated **bytes** per transaction | 48.4 KiB | **15.2 KiB** | **−68.7 %** |
| allocated **objects** per transaction | 494.5 | **210.4** | **−57.5 %** |
| `parser.yyNewParser` share of bytes | 54.2 % | **absent** | — |
| `nbtree.DeformPGIndexTuple` share of objects | 55.9 % | **absent** | — |
| `runtime.mallocgc` (CPU, cum) | 19.57 % | **10.74 %** | −45 % |

Microbenchmarks, now landed as permanent regression guards
(`internal/access/nbtree/pgcompare_alloc_bench_test.go`,
`internal/parser/parse_alloc_bench_test.go`):

| benchmark | before | after |
|---|---:|---:|
| `ComparePGIndexTuples` | 74.6 ns/op, 50 B, **4 allocs** | 33.2 ns/op, 0 B, **0 allocs** |
| `Parse/BEGIN` | 6,244 ns, 27,696 B | **1,375 ns (4.5×)**, 409 B |
| `Parse/SelectAbalance` | 10,271 ns, 28,718 B | 5,768 ns, 1,436 B |
| `Parse/InsertHistory` | 13,900 ns, 29,182 B | 9,321 ns, 1,900 B |

### 2.3 Efficiency, not just throughput

CPU consumed per transaction, from the profiled runs:

| | before (R2) | after (N1) |
|---|---:|---:|
| profile total / duration | 2,392 s / 13.29 cores | 2,474 s / 13.74 cores |
| transactions | 16,754,498 | 19,062,693 |
| **CPU per transaction** | 142.8 µs | **129.8 µs (−9.1 %)** |

The engine does the same query for ~9 % less CPU *and* spends far less time
parked on a mutex, which is why wall-clock throughput rose more (+21.4 %) than
per-transaction CPU fell.

## 3. Re-profile: what the read path looks like now

`-S`, 180 s, profiled run `N1-post-S` (105,907 tps — lower than the A/B figure
because a CPU profile is attached, exactly as the baseline `R2` was).

| frame | flat | cum | was (cum) |
|---|---:|---:|---:|
| `executor.opOpen` | 0.10 % | **24.91 %** | 28.11 % |
| ` └ nbtree.(*BTree).rangeScanPos` | 1.50 % | 21.71 % | 21.98 % |
| `syscall.Syscall6` | **16.00 %** | 16.00 % | 15.51 % |
| `optimizer.Plan` | 0.04 % | **13.39 %** | 11.94 % |
| `nbtree.comparePGIndexTuples` | 4.09 % | 11.09 % | 14.26 % |
| ` └ nbtree.deformPGIndexTupleInto[…]` | 4.28 % | 5.26 % | 11.48 % |
| `runtime.mallocgc` | 0.73 % | **10.74 %** | 19.57 % |
| `parser.(*yyParserImpl).Parse` | 6.13 % | **10.47 %** | 8.70 % |
| `runtime.newobject` | 1.33 % | 7.62 % | 8.92 % |
| `runtime.memclrNoHeapPointers` | 2.09 % | 2.09 % | 2.91 % |
| `strings.ToLower` | 1.59 % | 1.89 % | 1.82 % |

Reading this correctly requires remembering that the denominator shrank: shares
that look flat in percentage terms fell in absolute terms. `Parse` **rose** from
8.70 % to 10.47 % of a smaller total — its absolute cost fell, but less than
everything around it, so it is now a larger slice of a smaller pie.

Garbage collection remains a non-issue: `gcBgMarkWorker` 0.036 %, `scanObject`
0.026 %, background sweep 1.06 %.

Hardware counters (`perf stat`, user-mode only) move in a direction worth
stating honestly:

| counter | before | after |
|---|---:|---:|
| CPUs utilized (user) | 13.29 | 13.49 |
| **IPC** | 0.85 | **0.77** |
| branch-misses | 1.98 % | 2.44 % |
| LLC miss rate (misses / LLC refs) | 42.02 % | 42.56 % |

**IPC went down while throughput went up.** That is not a regression: the work
removed — bulk zeroing and allocator fast paths — is high-IPC, sequential,
cache-friendly work. Removing it leaves a mix that is proportionally more
pointer-chasing (btree descent, plan building), so the remaining instructions
retire more slowly even though there are far fewer of them per transaction. IPC
is a ratio, not a throughput measure, and this is a case where it must not be
read as one.

## 4. The next bottleneck, newly exposed

With the lock manager out of the way, the `-S` mutex profile has a new and
unambiguous leader:

| frame | cum | share of mutex delay |
|---|---:|---:|
| `postmaster.(*planCache).Put` | 514.25 s | **66.05 %** |
| `sync.(*RWMutex).Unlock` (flat, mostly the above) | 39.08 s | 5.02 % |

This is the second-order item [05 §6](05-improvement-plan.md) predicted in
writing before any of this was implemented:

> Under the simple protocol the cache is **net-negative**: every statement pays a
> `Put`, a write-lock and FIFO eviction against a 512-entry cache
> (`plancache.go:29-31`) it will never read back.

Under pgbench's literal-substituted SQL the cache key
(`planCacheKey`, `internal/postmaster/dispatch.go:2397`) is unique per
statement, so every query misses, then **writes** — taking a shard write-lock
and evicting a live entry. It was previously hidden behind the lock manager;
it is now two thirds of all remaining mutex delay.

**Do not simply stop populating it.** Ledger row `:1332` (M0132-S13) records
that goopg's simple path *deliberately* reads the cross-session plan cache
(`dispatch.go:1156-1157`, M0098-0005) — which is why goopg's `-M prepared` is
−1 % against simple where PostgreSQL's is +11 % — and that de-scoping it
"mirrors PG but slows goopg's simple path". The 100 % miss is a property of
pgbench's SQL, not of the simple protocol in general. A safe fix suppresses the
**`Put`** when the key is provably single-use, or makes insertion lock-free,
rather than disabling the cache.

The CPU profile agrees that the *cost* is contention rather than computation:
`planCache.Put` is only 0.52 % of CPU and `planCacheKey` 0.58 %.

## 5. Revised candidate ranking

Superseding [05 §1](05-improvement-plan.md):

| # | candidate | status / evidence | est. ceiling |
|---|---|---|---|
| ~~A~~ | fast-path weak relation locks | **LANDED** — `Lock:relation` 19.9 % → 0 %, mutex delay −54.6 % | realised |
| ~~B~~ | allocation-free btree compare | **LANDED** — objects/txn −57.5 % | realised |
| ~~C~~ | pooled parser | **LANDED** — bytes/txn −68.7 % | realised |
| **H** | plan-cache `Put` on a provably-single-use key | 66.05 % of remaining `-S` mutex delay | contention only; ~0.5 % CPU |
| **D** | `OpIndexScan` concrete operator kind | unchanged; adapter overhead still ~0.4 % of CPU | ~0.4 % |
| **E** | CLOG xid cache, then bank locks | untouched; still 13.9 % of `-N` mutex delay | ~2–4 % on `-N` |
| **F** | cheap per-statement wins | `strings.ToLower` 1.89 %, `mapaccess2_faststr`, `FSM.GetCandidates` | ~5 % combined |
| **G** | btree dead-entry reclamation | untouched; still no safe mechanism | space, not TPS |

Two items are now **larger relative shares** than before and are the honest
next targets on the read path, both architectural rather than incidental:

1. **`optimizer.Plan` at 13.39 %** (was 11.94 %). Every statement is planned
   from scratch; the cache cannot help under literal-substituted SQL, and
   PostgreSQL pays this too — but evidently far more cheaply.
2. **`parser.Parse` at 10.47 %** (was 8.70 %). Candidate C removed the parser's
   *allocation*; what remains is the LALR work itself. The `yySymType` shrink
   (C slice 2, [05 §C](05-improvement-plan.md)) is still unimplemented and is now
   the only remaining lever here — the 1,568-byte union member still has to be
   zeroed on every parse, which is what `memclrNoHeapPointers` (2.09 %) mostly is.

## 6. Correctness evidence

Every gate the project requires for this change class, plus two new tests:

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units` | PASS |
| `make race-gate` | PASS, **0 data races** (mandatory: `lmgr` is concurrency-critical) |
| `go test -race ./internal/storage/lmgr/` | PASS |
| `internal/parser/testdata/parity_goldens.txt` | **byte-identical** (the parser playbook's required review artifact) |
| `scripts/tpch-spotcheck.sh` | PASS — Q12=2, Q13=34 (canonical) |
| pre-commit pgbench smoke | PASS |
| `TestFastPathStrongCounterIsExact` | new — pins the strong counter across `Release`, `ReleaseAll`, every strong mode, and bucket independence |
| `TestFastPathStrongCounterAfterWaiterPromotion` | new — pins it on the `wakePassLocked` promotion route |
| every `-N`/`-S` run in this chapter | `0 failed` transactions |

### The trap worth recording

Candidate C's clearing is **mandatory, not defensive**. goyacc seeds `$$` for an
ε-production from the slot *above* the stack top —

```go
// reduced production is ε, $1 is possibly out of range.
yyVAL = yyS[yyp+1]                    // internal/parser/yacc_parser.go:10527
```

— which reads as a zero `yySymType` only because the parser was freshly
allocated. Recycling without clearing would hand an `opt_*` action the
**previous statement's** data: a silently wrong AST, the same failure mode that
got the M0069-0001 slot pool reverted twice. The measurement then showed the
safe version is also the fast one — the win is allocator bookkeeping, heap-bitmap
writes and GC pressure, not the zeroing, so paying the `clear()` costs far less
than the `malloc` it replaces.

## 7. Methodology notes and one trap re-encountered

- **Server age dominates a naive A/B, again.** The first post-change `-N`
  measurement showed an apparent 22 % *regression*. The cause was not code: that
  server had performed the `pgbench -i` load in the same process lifetime and was
  sitting at **11.2 GB RSS** against `GOMEMLIMIT=15GiB`, with GC behaving
  accordingly. `END` — a statement none of A/B/C touches — had "slowed" from
  3.215 ms to 4.226 ms, which is the tell. Restarting the server before the
  measurement removed most of it, and the alternating A/B removed the rest. This
  is the documented sweep-tail confound; it cost two runs to re-learn.
- **Cross-session comparison is not sound for `-N`.** Absolute `-N` numbers drift
  down across a session as the primary-key index bloats (still +104 B/txn,
  candidate G). Only same-session alternating A/B numbers are quoted for `-N`.
- **`perf stat -p PID -- sleep N` silently counts nothing**; the working form
  omits `--`. Carried forward from [00 §5](00-methodology.md).
