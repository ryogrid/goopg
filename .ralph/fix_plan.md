## Milestone 0027 — Low-risk performance optimisations

See `docs/milestones/0027-readability-preserving-optimisations.md`
and `docs/design/0027-0001-hot-path-micro-optimisations.md`.

- [x] DecodeRowInto — reuse row buffer in scanMatching (avoids 300K allocations per SeqScan)
- [x] Pre-allocate pending/matches slices (common case: 1-row match)
- [x] CRC-32 cache for WAL encodeRecord (avoids recomputation for repeated payloads)
- [ ] sort.Search in B-tree — already implemented (findChildBlock uses sort.Search)
- [ ] Benchmark: micro-optimisations below noise floor at current throughput (~1500 TPS)