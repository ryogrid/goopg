(idle — nothing in flight)

## Loop summary (2026-07-12, loop #70)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(TestPort_IsolationTimeouts / TuplelockUpgradeNoDeadlock /
PgWaldumpVacuumPruneRoundtrip) already `[x]` in M-NIGHTLY (co-load timing
flakes, not regressions). No new nightly work.

**Task — PGLZ varlena compression (encode + decode, PG-faithful). COMPLETE.**
Discovered while scoping unimplemented_feat #151 (TOAST compression): goopg's
bootstrap PGLZ compressor (`internal/initdb/pglz.go`) was NOT PG-compatible on
three axes, and there was NO decompressor — both varlena decode twins
hard-errored "compressed varlena not supported". A real PG standby could not
read goopg's `pg_rewrite.ev_action`.

Landed:
- NEW leaf pkg `internal/pglz` (stdlib-only): Compress/Decompress/
  BuildCompressedVarlena/DecodeInlineCompressed — faithful port of
  pg_lzcompress.c + varatt.h. Fixed: inverted control-bit polarity (bit1=match),
  swapped match-tag nibble layout + extension byte for len 18..273, and
  `va_tcinfo` = low-30-bits rawsize | top-2-bits method (was `rawSize<<2`).
- Wired `pglz.DecodeInlineCompressed` into BOTH decode siblings:
  executor/codec.go `decodePhysicalPGVarlena` + wal/pgoutput.go
  `pgoDecodePhysicalVarlena`.
- initdb/pglz.go delegates to pglz.Compress + BuildCompressedVarlena.
- Tests: internal/pglz/pglz_test.go (round-trip + HAND-AUTHORED PG-spec token
  stream independent of our encoder + corrupt + framing/bit-layout + LZ4-reject);
  executor/codec_compressed_test.go; wal/pgoutput_compressed_test.go; fixed
  tcinfo assertions in initdb pg_rewrite_bootstrap_test.go + btree_search_test.go.
- Design: docs/design/0046-0007-pglz-varlena-compression.md; README row;
  deferral_ledger.md new row.

Deferred (ledger): user-data compress-on-write (item #151 still open), LZ4
method, external-TOAST-pointer logical decode.

Gates: go build ./... clean; go vet pglz/initdb/wal clean; full pglz/initdb/
wal/executor suites PASS; tpch-spotcheck PASS (Q12=2/Q13=33); ralph-state-guard
repaired+consistent; pgbench smoke via pre-commit hook (see commit).

In-flight: none
