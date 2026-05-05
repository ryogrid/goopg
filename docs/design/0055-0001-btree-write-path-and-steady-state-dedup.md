# B-tree Write Path and Steady-State Dedup (M0055)

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| supersedes | — |

## 1. Problem

Current insert and split behavior still carries avoidable CPU and allocation overhead in hot write paths:

- insertion often rewrites whole page item arrays
- split point is count-based rather than byte-aware
- dedup is strong at bulk build time but degrades after incremental inserts

These are directly visible in the M0055 input reports and should be addressed before higher-risk multi-writer protocol work.

## 2. Design

### 2.1 In-page binary-position insert

- Keep key-position lookup binary-search based.
- Replace decode-all + rewrite-all insertion path with slot-level in-page insertion where the page format permits.
- Keep existing full-rewrite utility as fallback for rare repair/repack paths only.

### 2.2 Byte-aware split-loc policy

- Choose split location by accumulated serialized item bytes and target free-space profile.
- Handle variable-width keys and posting entries explicitly.
- Keep separator correctness and right-link/high-key invariants unchanged.

### 2.3 Rightmost-leaf fastpath

- Maintain per-backend rightmost leaf candidate.
- Try conditional latch on cached leaf; on validation failure fall back to normal descent.
- Restrict to safe no-structure-change fastpath.

### 2.4 Incremental dedup retention

- On duplicate-key insert, first try posting-list extension in place.
- If single posting item would exceed size constraints, split posting into bounded chunks.
- Optionally run local dedup compaction before split when duplicate density is high.

## 3. Compatibility and Safety

- No SQL-visible behavior change.
- Preserve existing key encoding and compare semantics.
- Keep WAL semantics compatible with current replay model; where new record detail is needed, add in dedicated WAL slices with recovery tests.

## 4. Tests

- Insert correctness parity vs baseline path.
- Duplicate-key stress with posting growth/chunking.
- Variable-width key split correctness under random and monotonic key distributions.
- Regression guard: no key loss, no ordering violations, no duplicate suppression mistakes.

## 5. Acceptance Metrics

- Lower allocator pressure and CPU in insert-heavy pprof snapshots.
- Reduced split frequency and lower p95/p99 insert latency on variable-width key workloads.
- Duplicate-heavy index size drift bounded vs post-bulk-build baseline under sustained writes.