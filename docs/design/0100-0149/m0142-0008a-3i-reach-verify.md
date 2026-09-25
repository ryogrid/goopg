# M0142-0008a-3i-reach-verify — reachability at HEAD, and where Q69 actually stops

Status: VERIFICATION COMPLETE 2026-09-20
Kind: recon
Parent: M0142-0008a-3
Milestone: M0142
Evidence: `analysis/m0142/m0142-0008a-3i-reach-verify-trace.txt`

## 1. Why this ran before any more plumbing

The banner unfroze the M0142-0008 chain on 2026-09-20 and named
`M0142-0008-producer` as the sanctioned route; that producer **landed the same
day** (`1f76d83d9`, teach an IN-unnesting path to set `.SJInfo` on a
DP-search-visible `JoinSemi` link). Every sub-task of `M0142-0008a-3i-plumbing`
— `-a`, `-b`, `-b1`, `-b2`, `-c` through `-c14` — is `[x]`. The parent
`M0142-0008a-3` is still `[ ]` on increments (i) and (ii).

Two things had to be established before writing more of it, and neither is
answerable by reading:

1. **Does a semiAnti chain link now get through the seam?** If it does, the
   P0-H11 audit finding becomes a LIVE correctness bug rather than a latent
   one: `cumulativeFromSpans`' span round-trip flattens `buildLeafSpans`'
   out-of-band synthetic Semi/Anti RHS ranges into a plain `[]int` and
   re-spans them contiguously, misattributing the hole to the last real leaf.
   The audit called it "unreachable today … a latent correctness gate for
   whichever task first lets a link through".
2. **Where does the chain's own named witness stop?** `M0142-0008a-3i-recon2`
   named TPC-DS **Q69** as "the real multi-table-EXISTS-body witness for THIS
   mechanism", and the whole leaf-admission increment is scoped against the
   `leaf-count` decline.

## 2. Method

Private `:5595` lane (`tmp/c20a/data-sf025`), HEAD `6909ec616`, binary built
from that tree, `GOOPG_PGSHAPED_DP_TRACE=1`, one `psql` per query with the
server log read by byte offset so each query's trace is exactly delimited.
`EXPLAIN (COSTS OFF)` only — no execution, no write to any shared cluster.

Queries: the five the census named for this mechanism — **Q69, Q16, Q94, Q10,
Q35**.

## 3. Result

```
Q69:  seam-decline reason=lateral     nrels=3 nleaves=6
Q16:  seam-decline reason=leaf-count  nrels=4 nleaves=3
Q94:  seam-decline reason=leaf-count  nrels=4 nleaves=3
Q10:  seam-decline reason=lateral     nrels=3 nleaves=4
Q35:  seam-decline reason=leaf-count  nrels=3 nleaves=2
```

Exactly one seam call per query, and **every one declines**. No `semiAnti`
admission appears anywhere in the capture.

### 3.1 Answer to question 1 — the latent correctness gate is still latent

No semiAnti chain link reaches the search, so `cumulativeFromSpans`' span
round-trip is still unreachable. **There is no live correctness bug** from the
P0-H11 finding at HEAD. It remains exactly what the audit called it: a gate
the first task to let a link through must close first.

### 3.2 Answer to question 2 — Q69 does NOT stop at `leaf-count`

The seam's checks run in source order —
`chain-not-flattenable` (`joinsearchseam.go:315`) → `leaf-count` (`:326`) →
`lateral` (`:330`) — and each returns. So reaching `lateral` **proves
`leaf-count` passed**.

- **Q16, Q94, Q35** decline at `leaf-count`, as the chain's scope assumes.
- **Q69 and Q10 pass `leaf-count` and decline at `lateral`.**

Neither Q69 nor Q10 contains the word `LATERAL` in its SQL. The Lateral join
is goopg's own: `chainCarriesLateral`'s Semi/Anti arm — added deliberately by
**`M0142-0008a-3i-plumbing-c14`** (design doc §48/§49) — exists precisely to
catch "a Lateral join nested inside a Semi/Anti's `Left`, the exact shape a
pre-DP-unnest join-order search (predp.go's Phase A) splices in when its own
winning tree contains a parameterized index probe". Before c14 that arm did
not exist and the decline never fired.

So the chain's last landed increment closed a correctness hole **and, in doing
so, moved its own named witness's blocker** from `leaf-count` to `lateral`.

## 4. What this means for M0142-0008a-3

The leaf-admission increment (i) is still the right work for **Q16, Q94 and
Q35**. It is **not** what unblocks **Q69** — the witness `-3i-recon2` chose
specifically because it exercises the RHS-as-participant shape three times in
one query. Whoever implements (i) against Q69 expecting `leaf-count` to be the
gate will measure no movement and conclude the increment failed.

Q69 needs a separate decision first: whether a chain whose Lateral join is
goopg's OWN Phase-A splice (rather than a user `LATERAL`) may be searched, and
if so how the parameterized probe's dependency is preserved across the
reorder. That is a different question from leaf admission and is filed as
**M0142-0008a-3i-lateral** rather than folded into (i).

## 5. Reporting

`Movement: none` — no production code changed; this is a verification recon.
Seam-decline census by class at a 120 s per-query timeout is §3's table.
