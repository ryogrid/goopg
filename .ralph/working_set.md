(idle — nothing in flight)

M0131-S20.1 landed and committed. `DECLARE_TOAST(pg_rewrite, 2838, 2839)` — the
blocker for EIGHT of the nine remaining `pg_catalog` views — now exists on a
freshly `goopg init`'d directory, and a hosted real PG resolves
`pg_toast.pg_toast_2618` by name and scans it.

Landed: `internal/initdb/pg_rewrite_toast_bootstrap.go` (declaration as DATA:
`nailedToastPairs`/`nailedToastRels`), pg_class + pg_attribute rows, the
pg_index row for 2839, `pg_class(2618).reltoastrelid = 2838`, both physical
files in base/{1,5}; `bootstrapPgClassRelnameNspIndex` learned the namespace it
had hardcoded to 11. Design `docs/design/0131-0035-pg-rewrite-toast.md`.

Worth carrying:
- **The pair must NOT join `nailedLocalRels`.** That list also drives
  `bootstrapPgTypeTuples` (a TOAST relation has NO pg_type row; a defaulted
  reltype=OID trips PG's `tdtypeid` assertion, relcache.c:4293) and
  `writeRelcacheInitFile` (PG never opens a TOAST relation in the critical
  relcache phase). Separate list + explicit wiring at the three sites that DO
  want it.
- **Oracle first, always.** Every field came from a throwaway PG 18.3
  (`initdb` + `pg_ctl -o "-k $D -c listen_addresses=''"` on a unix socket —
  TCP 5599 was already occupied). Notable: `attstorage='p'`, `reltype 0`,
  no pg_depend rows, chunk size 1996, and the compressed payload is what gets
  chunked (`sum(length(chunk_data)) == pg_column_size`).
- **79 of the oracle's ~160 pg_rewrite rows are toasted**, including views
  goopg hosts INLINE today. Inline vs external is not a divergence PG can
  observe — only the eight oversize captures are forced out of line.
- Adding a pg_index entry breaks `TestPgIndexInitialEntriesIndkeyMatchesPG18`
  (a pinned count + map) — extend it in the same edit.
- `go test ./internal/testport/` does NOT invalidate its cache when
  `internal/initdb` changes (it drives a built binary), so a non-vacuity probe
  there needs an explicit one-off `-count=1`.

Gates: `internal/initdb` PASS (113 s), `^TestE2E_` family PASS (96 s),
`TestE2E_PGColdStartOnGoopgDataDir` PASS + deliberate fail-when-broken run,
UNITS PASS, `go build ./...` + `go vet` clean, pgbench smoke via the hook.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all four
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): **S20.2** — the chunk writer
(1996-byte chunks, multi-page heap; `pg_seclabels` is 18 chunks), the
`loadViewsFromHeap` detoast SIBLING, capture guard #5 → "inline OR toastable",
then the eight captures and the `pg_indexes` guard inversion.

In-flight: none.
