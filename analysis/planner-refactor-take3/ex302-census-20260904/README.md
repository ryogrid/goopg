# EX3-02 Cut 0 — measure-only census (no behaviour change)

```
label: EX3-02-cut0-census | date: 2026-09-04
method: unit-level only (no bench servers). Synthetic harness in
  internal/executor/ driving ownedBuildRow / MaterializeArena /
  lazyHashInsertDatum with representative row shapes; allocs via
  testing.AllocsPerRun; throwaway test created, run, DELETED (no stray files).
  No non-test code touched. No commit.
shapes: A = 7-col narrowed Q9-class (int key, 3 arena strings s_name 25 B /
  nation 6-10 B / p_name 30-50 B, 2 int64 numerics, 1 int), N=2000
  B = 7-col int-lane (Q21-class lineitem build: ints + 1 int64 numeric), N=2000
  C = 7-col big-numeric (Q8-class: int key, arena big-numeric ~9 B body,
  1 arena string 25 B, rest ints/numeric), N=2000
```

## 1. Census numbers

### 1.1 Lane counts (per-build `ownedBuildRow` lane taken)

| shape | N | arena lane (`cloneRowOwned`) | `make` lane | payload mean B/row | min | max |
|---|---|---|---|---|---|---|
| A/q9-7col-3str | 2000 | 2000 (100%) | 0 | 72.0 | 61 | 85 |
| B/int-7col | 2000 | 0 | 2000 (100%) | 0.0 | 0 | 0 |
| C/bignum-7col | 2000 | 2000 (100%) | 0 | 34.0 | 34 | 34 |

Lane choice is deterministic by construction (any `ArenaID≠0` Datum ⇒ arena
lane); the Q9-class build is 100% arena-lane, the int-lane build 100% `make`.

### 1.2 Payload bytes/row histogram (bytes stratum B would absorb)

| bucket (B/row) | A (n=2000) | B (n=2000) | C (n=2000) |
|---|---|---|---|
| 0 | 0 | 2000 | 0 |
| 1–31 | 0 | 0 | 0 |
| 32–63 | 212 | 0 | 2000 |
| 64–95 | 1788 | 0 | 0 |
| 96–127 | 0 | 0 | 0 |
| 128+ | 0 | 0 | 0 |

A is tight (61–85 B, mean 72): nation mix (6/6/6/7/10) × p_name sweep
(30–50) — no long tail. C is a point mass (9 B sign+magnitude + 25 B string).
Max single payload observed: 50 B (p_name) — 640× below the 32 KB
oversize threshold, which therefore never fires on these shapes.

### 1.3 Allocs/row (testing.AllocsPerRun)

| probe | allocs |
|---|---|
| backing `acquireRow(7)` (never released, build path) | 2.00 |
| backing `make(Row, 7)` | 1.00 |
| `MaterializeArena` string 25 B | 1.00 |
| `MaterializeArena` big-numeric (incl. Perm bump + big.Int decode) | 3.00 |
| `MaterializeArena` non-arena no-op | 0.00 |
| `ownedBuildRow` shape A | **5.00** (= 2.00 acquire + 3×1.00 payloads) |
| `ownedBuildRow` shape B | **1.00** (= 1.00 make, zero payloads) |
| `ownedBuildRow` shape C | **6.00** (= 2.00 acquire + 1.00 string + 3.00 big-numeric) |
| `datumKey` string lane (in-loop) | 1.00 |
| `datumKey` int lane (demote path only; in-loop int cost is 0) | 1.00 |

Full build loop (`ownedBuildRow` + `lazyHashInsertDatum`, per row):

| arm | A | B | C |
|---|---|---|---|
| distinct keys, cold map | 7.02 | 2.02 | 8.02 |
| distinct keys, presized map | 7.01 | 2.01 | 8.01 |
| 50 distinct keys / 2000 rows (fanout) | 6.18 | 1.18 | 7.18 |

Reconciliation (exact): distinct-key arm = owned + key + ~1.02, where ~1.02
= one first-row bucket slice per distinct key (2000 slices) + map growth;
presizing barely moves it (7.02→7.01), proving map growth is NOT the cost —
first-row slices are. Fanout arm = owned + key + 0.18, trending to owned+key
at Q9 scale. Steady-state bucket append ≈ 0/row. Values spot check: owned row
reads back intact after producer-arena `Reset` (M0097-0058 boundary holds).

## 2. Predicted (§1) vs actual — gap closed

| §1 item | predicted | actual | delta |
|---|---|---|---|
| (1) row backing 1/row always | 1.00 | 1.00 make lane / **2.00** arena lane (`acquireRow` pool-New = slice + interface box; never returned by construction) | arena lane costs 2×, not 1× |
| (2) var-width payloads k/row | 1 per Datum | 1.00 per string Datum (A k=3 → 3.00) | none |
| (3) big-numeric 1 Perm-alloc/row | 1.00 | **3.00** through `MaterializeArena` (`bi.Bytes()` temp + `make` temp + Perm bump copy) | 3× worse than predicted — single dearest datum |
| (4) bucket append amortised ~0 | ~0 | 0.18/row at N=2000/50-keys → ~0 at scale; 1.0 per *distinct key* (first-row slice) | confirmed, with the distinct-key mechanism named |
| (5) key string 1/row string lane, 0 int lane | 1 / 0 | 1.00 string in-loop; 0 int in-loop | none |

## 3. 64 KB chunk-constant sizing (measured widths)

Datum is 48 B (pinned by `TestM0107DatumStructSize`), so stratum D at w=7 is
336 B/row: ⌊65536/336⌋ = **195 rows/chunk** (195×336 = 65520, 16 B slack) —
matches the design's ~190.

| stratum | width basis | rows / 64 KB chunk |
|---|---|---|
| D cells, w=7 narrowed | 336 B/row | **195** |
| B payloads, shape A mean 72 B (range 61–85) | 72 B/row | **~910** (771–1074 across histogram) |
| B payloads, shape C 34 B | 34 B/row | **~1927** |
| B payloads, shape B | 0 | n/a (no B traffic) |

Recommendation: **keep `denseChunkSize = 64<<10`, `denseOversizeThreshold =
denseChunkSize/2` unchanged.** One B-chunk covers ~4.7× the rows of one
D-chunk on the Q9 shape, so a single constant serves both strata with no
tuning; the oversize rule is dormant-but-safe on measured shapes (max payload
50 B vs 32 KB threshold) and only fires at TOAST scale, which is out of scope.
No resize needed — constant confirmed, tunable as designed.

## 4. Cut-1 go/no-go (stratum-B-first?)

**GO.** On the Q9-class shape 3 of 5 allocs/row (60%) are stratum-B payloads,
and the big-numeric lane (3.00/row, dearest single datum, plus a
process-lifetime `Perm` leak per row) also moves to stratum B — Cut 1 takes
shape A 5.00→~2.00 and shape C 6.00→~2.00 allocs/row with Row headers still
per-row, readers unchanged (same `(offset,length)`+`ArenaID` encoding), no
alignment need (byte payloads), and none of the stratum-D F1 GC-noscan hazard
(byte slabs hold no pointers). Int-lane rows stay 1.00/row — untouched by
Cut 1 as designed; they are Cut 2's stratum-D price. At the ~6 M-row Q9
anchor this is ~30 M → ~12 M build-path allocs (−60% objects) plus the
Q8-shape Perm-growth fix. Bucket append (≈0 steady-state) and key strings
(EX4-02's item) correctly stay out.
