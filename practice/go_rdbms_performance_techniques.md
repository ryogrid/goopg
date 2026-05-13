# Go Performance Techniques for an RDBMS (Idiomaticity-Sacrificing Edition)

A reference for coding agents building a relational database management system in Go where raw throughput, predictable latency, and low memory overhead matter more than idiomatic style. **CGO is out of scope**; everything below stays inside the Go toolchain (assembler, `unsafe`, runtime knobs).

> Always profile before applying. Use `pprof` (CPU, heap, allocs, mutex, block), `runtime/trace`, `benchstat`, and `go test -bench` with `-benchmem`. RDBMS workloads vary wildly between OLTP point queries, OLAP scans, bulk loads, and recovery — optimize the hot path the workload actually exercises.

---

## 1. Memory Allocation & GC Pressure

GC pauses are the enemy of tail latency. An RDBMS allocates per-tuple, per-row, per-batch — eliminate or reuse.

- **Eliminate heap escapes.** Verify with `go build -gcflags="-m=2"`. Common causes in DB code: returning `*Tuple` from helpers, storing scalars into `interface{}` for generic operators, capturing in closures passed to executors, sending values on channels.
- **Pre-size everything.** `make([]Row, 0, batchSize)`, `make(map[Key]Value, expectedRows)`. Hash joins, GROUP BY, and sort buffers all benefit.
- **`sync.Pool` for transient objects:** result rows, parsed AST nodes, scan iterators, network message buffers, executor operator state. Reset on `Get`; never retain refs after `Put`. Pool entries are GC'd between cycles — it is a cache, not a freelist.
- **Reuse buffers** with `buf = buf[:0]` and `clear(m)` (1.21+). Persistent pre-allocated buffers per session/connection are common in RDBMSes.
- **Arena allocation for query lifetime.** Allocate one large `[]byte` per query, sub-slice for AST/plan/intermediate strings, drop the whole arena at end-of-query. Zero pointers inside means zero GC scan time. Implement as a slice-bump allocator; expose `Alloc(n int) []byte` and `AllocFor[T any]() *T` via `unsafe`.
- **Avoid `interface{}`/`any` boxing of scalars** in tuple values. Boxing each `int64` into a heap-allocated `iface` destroys cache locality and inflates GC scan. Use a tagged union `Datum` struct (kind tag + fixed-size payload) instead.
- **Avoid closures in executor pipelines.** They allocate when capturing. Use struct-with-method operators with explicit state.
- **Avoid `defer` in tightest loops** (per-row, per-page). Modern `defer` is cheap but not free; in row-at-a-time inner loops, hoist the defer to the batch level.
- **Avoid finalizers** on cached pages or buffers — they extend object lifetime by a GC cycle and serialize on one goroutine.
- **Disable scavenging during bulk load** via `GOMEMLIMIT` to prevent OS-level page release thrash.

## 2. Runtime Tuning

- **`GOGC`**: raise (200, 500, `off`) to trade memory for CPU. `GOGC=off` for batch ETL paths that exit before OOM is tolerable.
- **`GOMEMLIMIT`** (1.19+): soft heap cap. Pair with high `GOGC` to suppress mid-query collections while still bounding RSS — the modern default knob for servers.
- **Manual `runtime.GC()`** between queries or at quiescent points; combine with `debug.SetGCPercent(-1)` to suppress GC during latency-critical sections (e.g., the WAL flush path).
- **`GOMAXPROCS`** to match real CPU quota. In containers, pre-1.25 runtimes ignored cgroup CPU limits — use `automaxprocs` or set explicitly. Oversubscription causes scheduler-induced tail latency.
- **`runtime.LockOSThread()`** for goroutines doing many syscalls (WAL writer, network poller using direct `syscall.Read`), or that need thread-local OS state (signal masks, `io_uring` rings, `O_DIRECT` aligned buffers tied to a thread).
- **`debug.SetMaxStack`** if recursive plan walkers hit growth costs (stack copying is O(n)).
- **`runtime.SetMutexProfileFraction(0)` and `SetBlockProfileRate(0)`** in production unless actively profiling — they impose nontrivial overhead on hot mutexes (i.e., the lock manager and buffer pool latches).

## 3. Build & Compilation

- **PGO (Profile-Guided Optimization, 1.21 GA).** Capture a `default.pgo` from production-like workloads (mixed OLTP / scan). Rebuild. Inlining decisions on hot operator dispatch paths and parser walks improve significantly. Typically 2–10% wall-clock; sometimes more on dispatch-bound interpreters.
- **`GOAMD64=v3`** (or `v4`) emits AVX2/BMI2/FMA — meaningful for hashing, checksums, vectorized scans. Same intent on ARM with `GOARM64`.
- **`-ldflags="-s -w"`** strips symbols/debug info; smaller binary, better i-cache.
- **`-trimpath`** for reproducible builds.
- **Build tags** for arch-specific kernels: `crc_amd64.s`, `crc_arm64.s`, `crc_generic.go`. Standard pattern for checksums, hashing, varint decoding.
- **Avoid `-gcflags="-B"`** (global bounds-check disable) — too dangerous. Use targeted BCE instead (§13).

## 4. Compiler Directives (Pragmas)

Place directly above declarations.

- **`//go:noinline`** — prevent inlining (benchmarking; controlling code size for cold paths).
- **`//go:noescape`** — declare a function does not escape its arguments. Crucial for asm-implemented hashers, encoders, and copy routines so callers keep buffers on the stack.
- **`//go:nosplit`** — skip stack-overflow prologue. Used in lowest-level page-access primitives. Dangerous: silent stack corruption on overflow.
- **`//go:linkname localname importpath.remotename`** — link to unexported symbols (e.g., `runtime.nanotime` for cheap monotonic timestamps in WAL records, `runtime.memmove`, `runtime.fastrand`). Requires `import _ "unsafe"`. Brittle across Go versions; gate with build tags per minor version.
- **`//go:norace`** — disable race detector for a function (only when you've proved correctness via other means, e.g., a hand-rolled latch protocol).
- **`//go:nocheckptr`** — disable `unsafe.Pointer` validity checks under `-race` for legitimate type-pun sites.

## 5. `unsafe` for Page & Tuple Access

The lifeblood of a Go-based storage engine.

- **Cast page bytes to typed structs:** `(*PageHeader)(unsafe.Pointer(&page[0]))`. Avoid copying 8 KB pages just to read a 24-byte header.
- **Zero-copy `string`↔`[]byte`** via `unsafe.String`/`unsafe.StringData`/`unsafe.Slice` (1.20+). Returning string column values from page memory is the canonical use.
- **Read/write fixed-width fields** with `*(*uint64)(unsafe.Pointer(&page[off]))` instead of `binary.LittleEndian.Uint64(page[off:])`. The compiler optimizes `binary` on aligned fast paths but not always; the unsafe form is unconditionally one MOV.
- **Pointer arithmetic** with `unsafe.Add` (1.17+) for slot-array walks in slotted-page layouts.
- **Reinterpret slices**: `unsafe.Slice((*uint64)(unsafe.Pointer(&b[0])), len(b)/8)` for batch hashing.
- **Aliasing rules**: keep the underlying allocation alive (`runtime.KeepAlive`) when you've cast pointers across types. `-race` and `-d=checkptr` flag misuse — run tests under both.
- **Alignment**: `O_DIRECT` and many SIMD paths require alignment. Allocate aligned buffers via `make([]byte, size+align)` and reslice from the first aligned offset, or use a custom aligned-page allocator.

## 6. Data Layout & CPU Cache

An RDBMS is a memory-hierarchy machine. Treat it as such.

- **Field ordering** in structs: largest types first, descending. Pad holes consciously. Use `fieldalignment` linter from `golang.org/x/tools/go/analysis/passes/fieldalignment`.
- **Hot/cold splitting.** Keep frequently-accessed fields (e.g., `PageID`, `LSN`, `pinCount`) in a small dense struct; move rarely-touched fields (statistics, debug info) behind a pointer to a side struct.
- **Cache-line alignment.** 64 bytes on x86/most ARM. Pad shared-write fields (latch counters, per-page pin counts, atomic counters in transaction managers) to a full cache line: `_ [64]byte`. Prevents false sharing across cores.
- **Per-CPU sharding.** For statistics counters (rows scanned, bytes read), use `[N]struct{ count int64; _ [56]byte }` indexed by `runtime_procPin` (via `//go:linkname`) to avoid contention.
- **Row-store vs column-store layout.** For OLAP workloads, column-store at the executor level (Arrow-style fixed-width column batches) hammers cache and enables SIMD. Even row-storage engines can adopt column-batch executors (vectorized execution, §17).
- **Avoid pointer-chasing in tuple values.** Inline scalars; intern strings into a per-batch dictionary rather than heap-allocating each. Variable-length data goes into a backing buffer with offset+length pointers into it.
- **Pack hot fields tight.** A Volcano-style operator base struct with a function-pointer `Next` plus 5–6 hot fields fits in one cache line.

## 7. Slices

- **Pre-allocate capacity** for tuple batches, sort runs, scan results.
- **`copy(dst, src)`** is intrinsified and beats per-element loops.
- **Index loops over `range`** when iterating large struct slices (`range` copies). Use `for i := range s { p := &s[i]; ... }`.
- **Reslice for ring buffers**, e.g., a WAL group-commit queue or a network read buffer.
- **`s[:0]` reuse** keeps capacity; the GC won't collect until the slice header is replaced.
- **In-place delete** patterns: ordered `s = append(s[:i], s[i+1:]...)`; unordered `s[i] = s[len(s)-1]; s = s[:len(s)-1]`.

## 8. Maps & Hash Tables

Critical for hash joins, GROUP BY, plan cache, lock table.

- **Pre-size**: `make(map[K]V, n)`.
- **Integer keys beat string keys.** When keying on TIDs or RowIDs, use `uint64`, not the textual representation.
- **`struct{}` value type** for sets — zero bytes per entry.
- **Modern Go maps use Swiss tables** (1.24+ runtime); retest custom hash tables against the new baseline before adopting.
- **Custom open-addressing maps** still win for fixed key types in the hash join build side. See `dolthub/swiss` (linear probing, SIMD-friendly), `cornelk/hashmap` (concurrent). Roll your own when you control hash distribution and load factor.
- **Don't use `map` for the lock table.** Sharded `map[KeyHash%N]*lockEntry` behind per-shard mutexes vastly reduces contention. Or use a hand-rolled chaining hash table protected by per-bucket spinlocks.
- **`m[string(b)]` pattern** is specially optimized — the compiler avoids allocating the string when looking up with a `[]byte`. Keep that exact form; any other `[]byte`→`string` dance allocates.
- **Iteration is randomized and not free.** Maintain a parallel keys slice for ordered or hot iteration.

## 9. Strings & Bytes

- **Operate on `[]byte`** in the parser, wire protocol, and storage engine. Convert to `string` only at session boundaries.
- **`strings.Builder`** for textual error message construction (cold path); for hot paths use a pooled `[]byte` and `strconv.AppendXxx`.
- **`strconv.AppendInt`/`AppendFloat`/`AppendQuote`**: orders of magnitude faster than `fmt.Sprintf`. The wire protocol's text format encoder must use these.
- **No `fmt` in hot paths.** Logging, EXPLAIN output, and error messages can — query execution and protocol encoding cannot.
- **No `regexp` in hot paths.** Even compiled regexes are slow. The SQL lexer should be hand-rolled with `bytes.IndexByte` and switch tables. If forced to use regex (e.g., `LIKE` translated to RE2), cache `*regexp.Regexp` per pattern; consider a custom `LIKE` matcher for `%foo%` patterns that beats RE2.
- **Pre-tokenize keywords** with a perfect hash (e.g., `gperf`-style or hand-tuned switch on first 1–2 chars and length) instead of map lookups.

## 10. Encoding & Serialization

- **Wire protocol (Postgres/MySQL):** hand-write the binary encoder/decoder against `[]byte` buffers. Pool the buffer per connection. Avoid `binary.Read`/`binary.Write` (reflection-based and slow); use direct `LittleEndian.PutUint*` or unsafe casts.
- **Tuple encoding:** custom format with explicit varints (`encoding/binary.PutUvarint`) for variable-length, fixed offsets for fixed-width columns. Generate marshalers per table schema if you have schema-level codegen.
- **Replace `encoding/json`** if used in admin/REST surfaces:
  - `bytedance/sonic` (amd64 JIT, very fast),
  - `goccy/go-json` (broader portability),
  - `mailru/easyjson` (codegen, no reflection).
- **Replace `encoding/gob`** for internal RPC unless cross-language is needed.
- **Replace stock protobuf** with `planetscale/vtprotobuf` (codegen, zero-allocation) for Raft/replication protocols.
- **WAL records:** fixed binary layout, hand-encoded; CRC over the encoded bytes; never reflect.

## 11. Reflection

- **Don't use `reflect`** in the executor, parser, or storage engine.
- **Code-generate** marshalers, comparators, hashers, and per-type operator implementations with `go generate` + `text/template` or `dave/jennifer`. A schema-driven RDBMS naturally generates per-table fast paths.
- **Generics (1.18+)** replace many reflect uses. But: with interface constraints the compiler may use a dictionary-based dispatch (slower than direct calls). Concrete-type union constraints (`~int | ~int64 | ~float64`) stencil per type and inline. Benchmark the actual generated code with `-gcflags="-m"`.
- **Cache reflection metadata** (`reflect.Type`, field offsets) at startup if you must reflect, then use `unsafe.Pointer` arithmetic for actual access at runtime (DDL-time cost is fine; DML-time cost is not).

## 12. Function Call & Operator Dispatch

The single biggest interpreter overhead in a Volcano-style executor.

- **Devirtualize interfaces** by type-asserting once outside loops: `if c, ok := r.(*HashJoin); ok { /* tight loop */ }`. Worth it for inner-loop operators.
- **Avoid storing `func` values** as fields if you can avoid it. Inline the call or use a switch-on-kind.
- **Vectorized execution** turns one virtual call per row into one virtual call per batch (e.g., 1024 rows). This single change typically yields 5–20× on scan/aggregation kernels. Adopt it for analytical paths.
- **Compile to closures (codegen at plan time).** For OLTP, generate a per-statement closure that does one tight pass over input batches. Combine with prepared statements and a plan cache.
- **Generics for typed kernels.** A `Sum[T constraints.Integer | constraints.Float](col []T) T` instantiates per concrete `T`, inlines, and vectorizes far better than an `any`-typed kernel.

## 13. Bounds Check Elimination (BCE)

Inner loops over pages and columns are the biggest beneficiaries.

- **Hint with an early access:** `_ = page[PageSize-1]` before a loop guarantees subsequent `page[i]` for `i < PageSize` are unchecked.
- **Capture length once:** `n := len(col); for i := 0; i < n; i++` — and don't mutate `col` inside.
- **`for i, v := range col`** is BCE-friendly.
- **Verify** with `-gcflags="-d=ssa/check_bce/debug=1"`. The compiler will print remaining checks. Tune the loop until they're gone in the kernel.

## 14. Loops

- **Manual unrolling** for fixed inner sizes (e.g., decoding 8 columns of a known schema). The compiler does not unroll automatically.
- **Hoist invariants** out of loops — pointer derefs through interfaces in particular are not always hoisted.
- **Process in chunks**, not bytes. Hash 8 bytes at a time with `*(*uint64)(unsafe.Pointer(&buf[i]))`. Apply to checksums, varint scans, and row decoders that can pre-validate alignment.

## 15. SIMD & Hand-Written Assembly

Go has no SIMD intrinsics, but the Go assembler (PLAN9 syntax) is available.

- **`math/bits`** intrinsics: `LeadingZeros`, `TrailingZeros`, `OnesCount`, `RotateLeft`. The compiler turns these into single instructions. Use for bitmap ops, hash mixing, varint length.
- **Hand-written `.s` files** for hot kernels: CRC32C (the runtime already uses asm), xxhash, AES-CTR (used in some page encryption), memcmp on column batches, vectorized filters.
- **`mmcloughlin/avo`** is the preferred SIMD generator — write Go code that emits AVX2/AVX-512 assembly at build time. Far more maintainable than raw `.s`.
- **Look at `klauspost/cpuid`** to dispatch between scalar/SSE/AVX2/AVX-512 kernels at startup.
- **Existing libraries to lift:** `klauspost/compress` (asm-accelerated zstd/snappy/gzip), `cespare/xxhash`, `zeebo/xxh3`, `minio/sha256-simd`. Use them for WAL/page checksums and compression.

## 16. Concurrency

The lock manager, buffer pool, and MVCC are concurrency-bound by definition.

- **Avoid channels in hot paths.** Channels involve a mutex, scheduler interaction, and value copies. Replace with mutex + ring queue, or `sync/atomic` for single-producer counters.
- **`sync/atomic` typed wrappers** (`atomic.Int64`, `atomic.Pointer[T]`, 1.19+) for counters, version numbers, and lock-free pointer swaps. Cleaner and aligned correctly on 32-bit.
- **Sharded mutexes / sharded maps** for the lock table and buffer pool hash table. One mutex per N hash buckets; bucket count = power of two; index via `hash & (N-1)`.
- **Latches vs locks.** Latches (page-level, very short) should be `sync.RWMutex` only if read parallelism dominates and contention is moderate; otherwise plain `sync.Mutex` or a hand-rolled spinlock (atomic CAS with bounded backoff) is faster. Benchmark; `RWMutex`'s internal accounting hurts under high write contention.
- **Optimistic latch-coupling** for B-tree traversals: read with a version counter, retry on version change. Eliminates most latch acquisitions in the read path.
- **Lock-free freelist** for buffer pool descriptors: Treiber stack via `atomic.CompareAndSwapPointer`.
- **Worker pools** for query execution rather than spawning unbounded goroutines per request.
- **Per-P data** via `runtime_procPin`/`runtime_procUnpin` (`//go:linkname`-imported). Used by `sync.Pool` internally; useful for per-P transaction ID allocators and statistics.
- **Batch under lock.** WAL group commit, lock manager wakeups, buffer pool eviction — take the lock once, do N items, release.
- **Avoid `time.After` in long-running loops** — leaks until fired. Use `time.NewTimer` with explicit `Stop` and `Reset`.
- **`sync.Cond` is acceptable** for waiter queues (lock manager waits, buffer pool wait-for-free) but consider channel-of-`struct{}` per waiter if the wait list is short.

## 17. Vectorized Execution

Specifically RDBMS-relevant. Worth its own section.

- **Process columns in batches** (e.g., 1024 or 2048 rows). Each operator's `Next()` returns a batch, not a tuple.
- **Columnar batch layout** in memory: one `[]T` per column, plus a selection vector (`[]uint16`) for filtered rows. Variable-length columns use offsets+bytes.
- **Per-type kernels.** A typed `FilterInt64Eq(col []int64, val int64, sel []uint16) []uint16` is a tight loop the compiler vectorizes well.
- **Generics for kernel families** (`FilterEq[T constraints.Ordered]`) — instantiated per type, no virtual dispatch in the loop.
- **Selection-vector vs null-bitmap modes** — pick one and stick with it for the kernel suite.
- **Avoid branches in inner loops** by writing branch-free predicates (e.g., `sel[outIdx] = i; outIdx += boolToInt(predicate)`).
- **Combine kernels** (filter+project, hash+probe) when profiling shows kernel-call overhead is itself meaningful.

## 18. I/O

- **Buffered I/O** via `bufio.Reader`/`Writer` only at boundaries. The storage engine should manage its own page-aligned buffers directly.
- **`io.CopyBuffer`** with a pooled buffer instead of `io.Copy` (which allocates 32 KB).
- **Vectored writes (`writev`)** via `net.Buffers` and `(*os.File).WriteAt` patterns in combination with `golang.org/x/sys/unix.Writev`. Critical for WAL group commit batching multiple records into one syscall.
- **`O_DIRECT`** via `golang.org/x/sys/unix` for the data file path: bypasses OS page cache, requires aligned buffers (typically 512 B or 4 KB). Combine with the buffer pool's own caching.
- **`O_DSYNC` / `fdatasync`** for WAL: prefer `Fdatasync` over `Fsync` (skips inode metadata flush). Group commit amortizes the fsync cost across transactions.
- **`mmap`** via `golang.org/x/exp/mmap` or `unix.Mmap` for read-mostly files (e.g., immutable LSM SSTables). Pros: zero-copy reads, OS-level caching. Cons: page faults are uninterruptible by Go's scheduler — long-tail latency risk; SIGBUS on truncation