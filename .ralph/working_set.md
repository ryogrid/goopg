(idle — nothing in flight)

Last loop (#115): **M0131-S6 — relhasrules flip for the 6 nailed system views**
— DONE, ticked, committed, pushed to `make-db-cluster-compat`.

M-NIGHTLY duty: `ci/logs/action-items.md` still run `20260811-014635` (12 items,
unchanged since loop #100). All already filed; the open ones stay PARKED per
banner. No new filing needed.

**Headline: a real PG 18.3 hosted on a goopg-initdb'd directory now evaluates
ALL SIX nailed views** (`assertNailedSystemViewsAreEvaluable`, S4 cold-start
E2E). The acceptance gate is restored: `waitForPhysicalStreamingPGtoGoopg`
queries the `pg_stat_replication` VIEW again (AI-20260810-011258-003's SRF-join
workaround retired).

**The one real failure was NOT the predicted tupledesc/attalign shape.**
`pg_stat_subscription` gave `cache lookup failed for attribute 10 of relation
6100`: `pgSubscriptionAttrs` declared **9 of pg_subscription's 18 columns**
while goopg's own writer `PGSubscriptionColumnsPG18`
(`internal/executor/sys_pg_subscription.go`) has emitted all 18 since B4.4 — a
silent sibling divergence. Fixed to 18 (+ relnatts 9→18, `substream`
bool(16)→char(18)), values read from a live PG 18.3 initdb's own pg_attribute.

**Hand forward:** no other nailed catalog was audited for the same truncation.
The cheap oracle is one throwaway `initdb` + `SELECT attnum,attname,atttypid,
attlen,attnotnull FROM pg_attribute WHERE attrelid='<cat>'::regclass` — a
table-driven guard over every `nailedAttr` list would close S13.3/S14.1's shared
root (trailing nullable catalog attributes) in one slice. Ledgered.

S6.1 was already discharged by S8a's repin (no blob patch). S6.5 (per-view
composite `reltype` vs 2249 RECORDOID) stays open, ledgered under S8a.
S6.6's bisect was met more cheaply by t.Errorf-per-view than by flipping one at
a time.

Next loop: per banner — M-NIGHTLY filing, then M0131 top-to-bottom. Next
unchecked after S6 is **M0131-S7** (the ev_action capture tool; under Option A
the mapping is the identity function, so S7.6 is a plain `cmp` against
upstream's bytes — do NOT grow a rewriting pass).

Gates: `internal/initdb` PASS (61 s); `TestE2E_PGColdStartOnGoopgDataDir` PASS;
`TestE2E_PGStandbyFullCycle` PASS; whole `^TestE2E_` family PASS (99 s);
`internal/estimateaudit` + `internal/executor` PASS; UNITS PASS (no FAILs);
pgbench smoke PASS via the commit hook. No query-path executor/planner/codec
change (initdb catalog description + one bootstrap flag), so tpch-spotcheck /
TPC-DS SF0.5 not required.

In-flight: none
