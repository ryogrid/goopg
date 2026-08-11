(idle — nothing in flight)

Last loop (#118) landed **M0131-S9.0**, the tooling precondition for S9's
corpus widening. Committed; fix_plan's S9 entry records it, S9 itself stays `[ ]`.

Two of the three hand-edits S7.4 ledgered are now closed — the `pg_rewrite`
`_RETURN` seed rows are generated from the manifest's `rule_oid`, and the
per-view `//go:embed` lines are one glob into an `embed.FS`. Adding a view is
now: `scripts/capture-ev-action.sh <view>` then
`go run cmd/gen-nailed-view-tables/main.go > internal/initdb/nailed_view_seed_data.go`.

**Next per the banner: M0131-S9.1** — capture the 23 SRF-only views listed in
`docs/design/0131-0009-system-view-corpus-widening.md` §"S9.1". Two notes from
that doc worth carrying: capture `pg_stat_bgwriter`/`pg_stat_checkpointer`
**last** (they have no `FROM` at all — an unmeasured `RTE_RESULT` shape — so an
unknown node tag surfaces against a two-view blast radius), and add the
`MaxHeapTupleSize` (~8160 B) assertion to the capture script's blob emitter
first, since `pg_rewrite`'s TOAST pair (2838/2839) is not bootstrapped and an
overflowing capture would otherwise seed an unreadable row.

Remaining unchecked M0131 tasks after S9: S12, S13, S14, S15, S8b.
