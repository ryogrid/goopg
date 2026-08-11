Task: **M0131-S7 — `ev_action` capture tooling** — S7.1/S7.2/S7.3/S7.5/S7.6
LANDED this loop (#116). The task stays UNCHECKED: **S7.4 (the Go generator) is
the only remaining slice.**

Files:
- `scripts/capture-ev-action.sh` (NEW) — the throwaway-PG-18.3 oracle: initdb,
  3 queries/view, emits `.dat` + manifest, `--verify` acceptance mode.
- `internal/initdb/nailed_view_manifest.tsv` (NEW, generated) — 6 rel rows +
  87 attr rows, oracle-stamped (PG 18.3, catversion 202506291).
- `internal/initdb/nailed_view_manifest_test.go` (NEW) — offline guard #2.
- `internal/initdb/relcache_init.go`, `pg_replication_views_nailed_test.go` —
  the slot_name bug fix (both siblings carried the same wrong value).
- `docs/design/0131-0007-*.md` (+ README row), `.ralph/fix_plan.md`,
  `.ralph/deferral_ledger.md`.

Key symbols: `pgStatReplicationSlotsViewAttrs`, `nailedAttr`, `nailedLocalRels`,
`systemViewOIDPins`, `readNailedViewManifest`,
`TestNailedViewManifestMatchesGoTables`.

Findings: **`--verify` re-derives all six committed blobs byte-identically**
(guard #1) across three independent initdb runs (guard #6). The `:relid 12261`
landmine (guard #3) never fired — S8a's repin disarmed it, so NO rewriting pass
was grown; identity is *asserted*, not applied. Guard #2 caught a real
transcription bug first try: `pg_stat_replication_slots.slot_name` is
`text`(25,−1) — the SRF's OUT param — not `name`(19,64) from the base view.
Latent only because the view returns zero rows with no slots. Generalises the
S6 `pgSubscriptionAttrs` lesson: hand-transcribed catalog shape needs a machine
oracle, and the ~40 other nailed catalogs are still unaudited.

Deviations from the design (both real, both now documented): PG 18 `initdb` has
no `-q`; psql `-F $'\t'` instead of `||` (ambiguous `text || "char"`).

Next step: **S7.4** — `cmd/gen-nailed-view-tables/main.go` (`//go:build ignore`,
`// Code generated …; DO NOT EDIT.` per `cmd/gen-pg-proc-data/main.go`), reading
`nailed_view_manifest.tsv` → `internal/initdb/nailed_view_seed_data.go`, then
delete the six hand-written `*ViewAttrs()` funcs and repoint guard #2 (it turns
tautological once the tables are generated). Ledger row says: consider landing
it only after S9.1 widens the manifest past six views, so its Go shape is not
fixed prematurely — the banner decides.

Gates run: `internal/initdb` PASS (61 s); whole `^TestE2E_` family PASS (100 s)
— covers `TestE2E_PGColdStartOnGoopgDataDir`, the hosted-PG reader of the
changed pg_attribute row; `scripts/capture-ev-action.sh --verify` PASS; pgbench
smoke via the commit hook. No query-path executor/planner/codec change (initdb
catalog description + a new script), so tpch-spotcheck / TPC-DS SF0.5 not
required.

In-flight: none
