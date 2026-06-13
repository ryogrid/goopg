(idle — M0102-0010 gate (b) landed & committed this loop)

Loop #34 landed **gate (b)** of the `--data-checksums` default-ON flip
(standby-read / physical-replication validation — the last gate).

- New `internal/testport/e2e_checksum_replication_test.go`
  `TestE2E_ChecksumStreamingGoopgToPG`: a `--data-checksums` goopg primary fills
  ~115 heap pages, `CHECKPOINT`s before the clone, `pg_basebackup -X stream`s to
  a **real PG** standby that verifies goopg's `pd_checksum` on read
  (`SHOW data_checksums = on` + full seq-scan of 4000 rows +
  `sum(length(payload))`). A wrong checksum byte aborts the scan with
  `invalid page in block N` — byte-level cross-impl proof gate (a) cannot give.
  **PASS 2.45s** against real PG binaries.
- Harness change (additive): `cluster.Options.InitArgs []string` threads extra
  `init` args (`--data-checksums` here). Empty default → byte-identical to before.
- Files: internal/testutil/cluster/cluster.go (InitArgs),
  internal/testport/e2e_checksum_replication_test.go (new),
  docs/design/0102-0019-initdb-data-checksums.md (gate (b) DONE + Testing + status),
  .ralph/fix_plan.md (M0102-0010 progress), .ralph/deferral_ledger.md.
- Gates: gofmt/vet clean; `go test ./internal/testutil/cluster
  ./internal/testutil/replcluster` PASS; TestE2E_ChecksumStreamingGoopgToPG PASS;
  make ralph-state-guard OK (reset stale progress.json completed→running again).

**Both flip-gates now pass** (gate (a) FPI-replay loop #32; gate (b) loop #34).

Next loop candidate: the **`--data-checksums` default-ON flip** itself — the
one-line `init` default false→true (`cmd/goopg/main.go:180`). DEFERRED to a
dedicated loop (ledger 2026-06-13): it changes every new cluster's on-disk
format, so it MUST be gated by the full regress-port suite + a TPC-H
re-load/spot-check (M0106 codec/format lesson) and re-init of every test/bench
data dir the format change invalidates.

⚠️ WORKING-TREE CONTAMINATION (separate session, DO NOT commit): ~18 modified
files + 2 new test files belong to an UNRELATED partition generated-column-
override feature (analyzer, catalog, executor/*, mvcc, parser, planner/*,
server/dispatch.go + gen_override tests). Commit ONLY the gate-(b) files above.
