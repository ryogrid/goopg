# 0110-0006 — amcheck Bloom filter (heapallindexed primitive)

Status: accepted (partial)
Milestone: M0110-0003
Date: 2026-06-14 (loop #60)

> Scope note: this is a focused, standalone slice of the amcheck verify engine
> tracked by [0110-0005](0110-0005-verify-heapam-engine.md). It lands the
> probabilistic set-membership primitive that the B-tree `heapallindexed`
> verification tier will consume; that tier and the SQL surface remain deferred
> (see "Deferred" below). It follows the same engine-first/wire-later pattern as
> the rest of `internal/amcheck`.

## Goal

Port upstream PostgreSQL's space-efficient Bloom filter
(`postgres/src/backend/lib/bloomfilter.c`, `src/include/lib/bloomfilter.h`) into
the `internal/amcheck` package as `bloomfilter.go`. This is the foundational
data structure for amcheck's last remaining B-tree verification tier,
`heapallindexed`.

## Why this is the right next slice

The B-tree page-/cross-page-/cross-level-structural engine is essentially
complete at the page-bytes level (loops #55–#59: `VerifyBtreePage`,
`VerifyBtreeItemOrder`, `VerifyBtreeLevelSiblingLinks`,
`VerifyBtreeParentDownlinks`). The only verification logic upstream's
`bt_check_every_level()` performs that goopg's engine still lacks is the
optional `heapallindexed` check: after fingerprinting every index tuple into a
Bloom filter, amcheck scans the heap and, for each visible heap tuple, forms the
index tuple it *should* produce and asserts that tuple is present in the filter
(`verify_nbtree.c:bt_tuple_present_callback`). A heap tuple the filter reports
absent is a genuine "heap tuple not represented in index" corruption.

That heapallindexed scan needs the heap relation and the index `TupleDesc` (to
re-form index tuples from heap tuples), which couples it to the catalog/SQL
surface — and that surface is currently blocked on a clean working tree (a
separate manual session's uncommitted gen-column WIP spans
parser/planner/executor/catalog). The Bloom filter, by contrast, is a
self-contained data structure with **no** clog / TupleDesc / catalog / SQL
coupling. Porting it now:

- lands a real, exhaustively-tested prerequisite for heapallindexed without
  touching any contaminated file (new files only: `bloomfilter.go`,
  `bloomfilter_test.go`);
- shrinks the remaining heapallindexed work to "heap scan + index-tuple
  formation + filter probe", all of which is naturally SQL-surface-coupled and
  lands together once the tree is clean;
- keeps the engine-first/wire-later cadence the milestone has followed since
  loop #51.

## What the Bloom filter must guarantee

amcheck relies on exactly the Bloom filter contract:

- **No false negatives.** If the filter says an element is absent, it is
  genuinely absent. This is load-bearing: a false negative would surface as a
  *spurious* "heap tuple missing from index" corruption report. The structure
  guarantees this unconditionally — an element is "absent" only if at least one
  of its k bits is unset, and `bloomAddElement` sets all k bits, so any added
  element always tests present.
- **Bounded (~1–2%) false positives.** A heap tuple genuinely absent from the
  index may occasionally be reported present, which only *weakens* the check
  (a real corruption is missed), never *falsifies* it (no false alarm).
  Upstream accepts the same trade-off.

## Port fidelity and the one documented divergence

Every part of the algorithm is ported verbatim from `bloomfilter.c`:

| upstream | goopg | notes |
|----------|-------|-------|
| `bloom_create` | `bloomCreate` | two-bytes-per-element target; 1MB floor / 512MB (2^32-bit) cap; power-of-two bitset |
| `bloom_add_element` | `bloomAddElement` | sets k bits |
| `bloom_lacks_element` | `bloomLacksElement` | "definitely absent" test |
| `bloom_prop_bits_set` | `bloomPropBitsSet` | saturation instrumentation |
| `my_bloom_power` | `myBloomPower` | highest power of two ≤ target, capped at 2^32 |
| `optimal_k` | `optimalK` | `round(ln2·m/n)`, clamped to `[1, 10]` |
| `k_hashes` | `kHashesValues` | enhanced double hashing (two 32-bit halves of one 64-bit hash) |
| `mod_m` | `modM` | power-of-two bitmask modulo |

The **single divergence** is the underlying hash. Upstream seeds enhanced double
hashing from `hash_any_extended()` (Jenkins lookup3, `src/common/hashfn.c`).
goopg already mirrors that exact function as the unexported `pgHashBytesExtended`
in `internal/executor/hash_partition.go`, but importing it into `internal/amcheck`
would entangle the verify engine with the executor package (and executor is one
of the files carrying the uncommitted gen-column WIP). Because the Bloom
filter's correctness (no false negatives) holds for **any** hash that
`bloomAddElement` and `bloomLacksElement` share, and its observable contract is
"absent ⇒ definitely absent / present ⇒ probably present" rather than
byte-for-byte parity with a PG bitset, the port uses a self-contained seeded
64-bit hash: **FNV-1a** with the seed folded into the offset basis, followed by
the **MurmurHash3 `fmix64`** finalizer so that both 32-bit halves are
independently well avalanched for the double-hashing split. The enhanced
double-hashing recurrence itself, and all sizing math, are upstream-verbatim.

When the working tree is clean, the Jenkins hash can be promoted to a shared
package and substituted here without changing the filter's contract — a
follow-up that only affects the false-positive *distribution*, never
correctness.

## Tests

`bloomfilter_test.go` (8 tests, all passing):

- `TestBloomCreateSizing` — bitset is power-of-two bits, within `[1MB, 512MB]`,
  byte length matches `m/8`, `kHashFuncs ∈ [1,10]`, fresh filter has 0 bits set;
  covers the zero/negative-estimate guard.
- `TestBloomNoFalseNegatives` — the load-bearing property: 50k added elements all
  test present.
- `TestBloomFalsePositiveRate` — sized into the realistic density regime (1M
  elements ⇒ `m=2^23`, `k=6`, ~0.5 saturation); observed FP rate < 5%, bits-set
  in `[0.2,0.8]`. (Documents that the 1MB floor over-provisions small sets,
  driving FP→0.)
- `TestBloomSeedDistinguishesFalsePositives` — distinct seeds disagree on absent
  probes (the reason `bloom_create` takes a seed), with zero false negatives
  under either.
- `TestBloomEmptyAndVariableLengthElements` — empty slice + varied lengths
  round-trip without panics / false negatives.
- `TestMyBloomPower`, `TestOptimalK`, `TestModM` — the sizing/hash helpers
  against hand-computed values, including the 2^32 cap and `[1,10]` clamping.

Gates: `go test ./internal/amcheck` PASS; `go build ./...` OK; `gofmt -l` clean;
`go vet ./internal/amcheck` clean.

## Deferred (resume points)

- **heapallindexed verification tier** — heap scan + per-heap-tuple index-tuple
  formation + `bloomLacksElement` probe; needs the heap relation + index
  `TupleDesc`, so it lands with the SQL surface.
- **SQL surface** — `CREATE EXTENSION amcheck` + `bt_index_check` /
  `verify_heapam` SRFs wiring the page-structural tiers and heapallindexed;
  blocked on a clean working tree. Promotes `AC-002` (`002_nonesuch` first).
- **Hash unification** — substitute the shared Jenkins `hash_bytes_extended`
  for `bloomHash64` once it can be promoted out of `internal/executor` without
  cross-package entanglement (distribution-only change).

The keystone blocker for all three remains the uncommitted gen-column WIP in the
working tree; see [0110-0005](0110-0005-verify-heapam-engine.md).
