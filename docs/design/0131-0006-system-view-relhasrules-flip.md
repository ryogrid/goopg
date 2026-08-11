# System-view `relhasrules` flip — a hosted PG stops short-circuiting on the six nailed views

**Status:** accepted (implemented 2026-08-11 — see Findings)
**Date:** 2026-08-11
**Milestone:** M0131 (S6)

## Problem

goopg bootstraps six replication system views on disk with complete pg_class
rows, PG-faithful pg_attribute rows, verbatim upstream `ev_action` blobs, and
**both** `pg_rewrite` btrees populated. A hosted PG still cannot evaluate any of
them: every bootstrapped view row carries `relhasrules = false`, so
`RelationBuildDesc` takes the `else` arm at
`postgres/src/backend/utils/cache/relcache.c:1249-1255` and never scans
`pg_rewrite`. The rule is present and indexed; nothing looks at it.

The enabling line exists, commented out — `internal/initdb/initdb.go:5806-5818`,
inside `pgClassRow`:

```go
	// M0106-0010 Step 3dl: views have no physical storage. …
	// pg_class.relhasrules is true so that PG's relcache fetches the view's
	// ON-SELECT rewrite rule from pg_rewrite when the view is opened.
	relFilenode := int64(rel.OID)
	relHasRules := false
	if rel.RelKind == 'v' {
		relFilenode = 0
		// Keep relHasRules=false: the view is found in pg_class (name lookup works)
		// and PG won't try to load the rewrite rule. Querying the view will return
		// an error (no storage) but won't crash. Needed until the ev_action format
		// is fully compatible with the running PG18 version.
		// relHasRules = true
	}
```

Two defects. The rationale is stale — the blobs *are* verbatim PG 18.3 dumps of
`system_views.sql`, so "until the ev_action format is fully compatible" was
discharged when they landed. And the prose three lines above already asserts
`relhasrules is true`, contradicted by the code; the same contradiction is
repeated at `internal/initdb/relcache_init.go:683-684`.

`pgClassRow` is consumed by `bootstrapPgClassTuples`
(`internal/initdb/initdb.go:2185-2200`), which walks
`nailedSharedRels ++ nailedLocalRels` through `writeMultiPageHeap`; that writer
lands the same byte image in **`base/1/1259` and `base/5/1259`**
(`internal/initdb/initdb.go:6140-6150`). The flip is one line reaching both
databases with no mirror plumbing.

## Design

### The six views, as the code pairs them

`internal/initdb/relcache_init.go:688`, `:693-697` — `{OID, name, RelType,
RelKind, RelNatts, IsShared, attrs}`:

| OID | name | natts |
|---|---|---|
| 12100 | `pg_stat_wal_receiver` | 15 |
| 12102 | `pg_stat_replication` | 20 |
| 12103 | `pg_stat_recovery_prefetch` | 10 |
| 12104 | `pg_stat_subscription` | 11 |
| 12105 | `pg_replication_slots` | 21 |
| 12106 | `pg_stat_replication_slots` | 10 |

Their pg_attribute rows are transcribed from `system_views.sql` + `pg_proc.dat`
with PG-exact `TypeOID`/`Len` (`pgStatReplicationViewAttrs`,
`relcache_init.go:2597-2620`: `pg_lsn` 3220 len 8, `interval` 1186 len 16,
`xid` 28, `inet` 869 len -1).

All six carry `RelType: 2249` (`RECORDOID`) where real PG creates a per-view
composite `pg_type` row. Plain SELECT after rule expansion substitutes the
rule's `Query` for the RTE and should never consult `reltype`, but
`relcache_init.go:679-681` justifies 2249 only by the underlying SRF's
`prorettype`. **Probe, do not assume** — anything taking the view's row type
(`::pg_stat_replication`, a composite Var, `record_out`) may diverge. Ledger if
it does.

### The landmine — measured, not inferred

`internal/initdb/pg_rewrite_bootstrap.go:15-17` claims: *"No view-side relid
appears in the tree (the RTE references the underlying function, not the view's
pg_class OID), so no OID rewriting is needed when porting the dump across
PG/goopg."* True for five blobs, **false for the sixth**. Across the
`//go:embed` set (`pg_rewrite_bootstrap.go:29-45`):

```
$ grep -o ':relid [0-9]*' internal/initdb/*_ev_action.dat | sort | uniq -c
      2 pg_replication_slots_ev_action.dat::relid 1262         # pg_database     — pinned, clean
      2 pg_stat_replication_ev_action.dat::relid 1260          # pg_authid       — pinned, clean
      2 pg_stat_replication_slots_ev_action.dat::relid 12261   # ← THE LANDMINE
      2 pg_stat_subscription_ev_action.dat::relid 6100         # pg_subscription — pinned, clean
```

`pg_stat_wal_receiver` and `pg_stat_recovery_prefetch` contain no `:relid` at
all (pure `rtekind 3` function RTEs). Note `grep -c` reports **1** for the
landmine file — every `.dat` is a single line, 5397 B here — while `grep -o`
correctly reports **2 occurrences**, at byte offsets 707 and 1797:

- 707, in the `RANGETBLENTRY`:
  `… :rtekind 0 :relid 12261 :inh true :relkind v :rellockmode 1 :perminfoindex 1 …`
- 1797, in `:rteperminfos`:
  `{RTEPERMISSIONINFO :relid 12261 :inh true :requiredPerms 2 :checkAsUser 0 …}`

`:relkind v` plus the 21-name `eref` colnames list preceding occurrence 1
(`slot_name … synced`) identifies it: a **view-on-view** reference. Upstream
confirms — `postgres/src/backend/catalog/system_views.sql:1045-1059` defines
`pg_stat_replication_slots` as `FROM pg_replication_slots as r, LATERAL
pg_stat_get_replication_slot(slot_name) as s`. 12261 is upstream initdb's OID
for `pg_replication_slots`; goopg assigns **12105**
(`relcache_init.go:696`, natts 21 — matching the colnames list).

The instant `relhasrules` flips, opening `pg_stat_replication_slots` makes PG
resolve relation 12261, which does not exist in a goopg catalog.

**S6.1 fix:** rewrite `:relid 12261` → `12105` at both offsets, **or** repin the
view OIDs to upstream's per S8. If S8 has not landed, patch the blob and let S8
revisit — S7's capture tool re-derives it either way and independently
re-surfaces the bug if the patch route was taken.

### S6.3/S6.4 — the flip and its lock-in test

Uncomment `relHasRules = true` at `internal/initdb/initdb.go:5817`, replace the
stale rationale, and fix the contradicting prose at `:5806-5809` and
`relcache_init.go:683-684`.

`internal/initdb/pg_stat_wal_receiver_nailed_test.go:111-118` pins the old
behaviour and must be inverted:

```go
	// NAILED replication system views (12100-12106) keep relhasrules=false: their
	// canonical ev_action IS present, but PG serves these views from its own
	// built-in relcache entries, so enabling standby-side rule expansion for them
	// is a separate track. …
	if row[20].BoolValue() {
		t.Fatalf("nailed view pg_class.relhasrules=true want false")
	}
```

Its stated reason does not hold on a goopg-created directory, where PG has no
built-in entry for anything and builds every relcache entry from goopg's heaps.
`row[20]` is `relhasrules` per the 0-indexed layout the test documents at
`:100-104`. Invert to `if !row[20].BoolValue()`.

### Why S6 is small

The bootstrap side is done. `bootstrapPgRewriteTuples`
(`internal/initdb/pg_rewrite_bootstrap.go:221-236`) writes all six `_RETURN`
rows to `base/{1,5}/2618`, and **both** indexes are bootstrapped from the
returned TID map: `bootstrapPgRewriteOidIndex` (2692) at
`internal/initdb/btree_index_bootstrap.go:1610-1644` and
`bootstrapPgRewriteRelRulenameIndex` (2693) at `:1659-1712`, the latter keying
through `pgBuildIndexTupleOidNameKey` with the C-locale `(ev_class, rulename)`
sort; its comment (`:1651-1654`) already names `RelationBuildRuleLock` and says
the leaf "must exist before PG's first probe of the seeded view." S5 supplies
the *runtime* equivalent for user views; S6 supplies the flag.

## Guards

1. **Invariant guard test** (acceptance item 5, in `internal/initdb`): scan every
   `*_ev_action.dat`, extract every `:relid <n>`, fail on any `n` that is neither
   a pinned catalog OID (`< FirstUnpinnedObjectId`, 12000) nor a goopg-assigned
   view OID from `nailedLocalRels`. It **must** fail on the unfixed blob — run it
   before S6.1 to confirm. Count occurrences, not lines: the files are
   single-line, so a per-line test undercounts.
2. Bootstrap-coverage assertion: every `relkind='v'` tuple in `base/1/1259` and
   `base/5/1259` has `relhasrules = true`; every `relkind='r'` tuple still false.
3. Inverted `pg_stat_wal_receiver_nailed_test.go:111-118` with a rewritten
   rationale.
4. Per-view reachability probe on a hosted PG: `SELECT * FROM <view> LIMIT 0` for
   all six — empty result passes; 42809 or 42P01 fails.
5. **Risk control.** Flip the six one at a time if a backend dies. The known
   failure shape is a tupledesc build on a bad `attalign`:
   `populate_compact_attribute_internal` at
   `postgres/src/backend/access/common/tupdesc.c:105`
   (`elog(ERROR, "invalid attalign value: %c")`), recorded as the M0106 shape at
   `internal/initdb/pg_type_bootstrap.go:322-331`. The blobs are independent, so
   a bisect by view is cheap. *(Correction to the M0131 plan: upstream raises
   `ERROR`, not `FATAL`, at that line — the FATAL wording is goopg's comment.
   Treat "backend dies or errors on open" as the trigger either way.)*
6. **Acceptance gate.** Restore `waitForPhysicalStreamingPGtoGoopg`
   (`internal/testport/e2e_pg183_standby_full_cycle_test.go:330-366`) to query the
   view. Its current body (`:346-350`) is the AI-20260810-011258-003 workaround:

   ```go
   		pgReady := pgPrimary.QueryScalar(t, fmt.Sprintf(
   			`SELECT count(*) FROM pg_stat_get_activity(NULL) AS s
   			   JOIN pg_stat_get_wal_senders() AS w ON (s.pid = w.pid)
   			  WHERE s.application_name = '%s' AND w.state = 'streaming'`,
   			appName)) == "1"
   ```

   Replace with `SELECT count(*) FROM pg_stat_replication WHERE application_name
   = '…' AND state = 'streaming'`, and delete the `:334-345` comment block —
   including its `pg_internal.init is written ruleless` attribution, which S10
   retires. Reverting that harness downgrade *is* the gate.
7. `go test -v -run '^TestE2E_PGStandbyFullCycle$' ./internal/testport/` (~40 s)
   green.
8. `reltype` probe (S6.5): a composite-typed reference to one of the six on a
   hosted PG. Ledger, do not assume.
9. UNITS + SMOKE green.

## References

- `postgres/src/backend/utils/cache/relcache.c:1249-1255`, `:752-806`
- `postgres/src/backend/catalog/system_views.sql:1019`, `:1045-1059`
- `postgres/src/backend/access/common/tupdesc.c:60-107`
- `internal/initdb/initdb.go:5806-5818`, `:2185-2200`, `:6140-6150`
- `internal/initdb/relcache_init.go:683-697`, `:2597-2620`
- `internal/initdb/pg_rewrite_bootstrap.go:15-17`, `:29-45`, `:221-236`
- `internal/initdb/btree_index_bootstrap.go:1610-1644`, `:1659-1712`
- `internal/initdb/pg_stat_wal_receiver_nailed_test.go:111-118`
- `internal/initdb/pg_type_bootstrap.go:322-331`
- `internal/testport/e2e_pg183_standby_full_cycle_test.go:330-366`
- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md` §S6
- `docs/design/0131-0005-pg-rewrite-runtime-index-maintenance.md` (S5, runtime side)

## Findings (implementation, 2026-08-11)

**The flip works, and it works for all six views.** A real PG 18.3 hosted on a
directory `goopg init` created now evaluates every one of
`pg_stat_replication`, `pg_stat_wal_receiver`, `pg_stat_recovery_prefetch`,
`pg_stat_subscription`, `pg_replication_slots` and
`pg_stat_replication_slots` — it scans goopg's own `pg_rewrite` heap through
index 2693, finds the `_RETURN` rule and substitutes its `Query` for the RTE.
`assertNailedSystemViewsAreEvaluable`
(`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go`) is the probe;
`waitForPhysicalStreamingPGtoGoopg` querying `pg_stat_replication` again is the
gate.

Five deltas against the design as written:

1. **S6.1 needed no work.** The `:relid 12261` landmine was discharged by
   M0131-S8a taking the "repin per S8" branch: `pg_replication_slots` *is*
   12261 now, so the verbatim blob is correct as captured. The blob-patch route
   was never taken. The OID table in §"The six views" above is pre-S8a
   (12100-12106) and is superseded by
   `internal/initdb/system_view_oid_pins.go`.
2. **One view did fail, for a reason the design did not predict** —
   `pg_stat_subscription`, with `ERROR: cache lookup failed for attribute 10 of
   relation 6100`. Not a tupledesc/`attalign` failure (guard 5's predicted
   shape) but a **truncated catalog self-description**: `pgSubscriptionAttrs`
   declared 9 of pg_subscription's 18 columns, while that view's `ev_action`
   carries Vars up to `varattno 18`. goopg's own heap *writer*
   (`PGSubscriptionColumnsPG18`) had emitted all 18 columns since B4.4 — the
   two siblings had disagreed silently the whole time. Fixed here: 18 columns,
   `relnatts` 9 → 18, and `substream` corrected from bool (16) to char (18),
   with every value read from a live PG 18.3 `initdb`'s own `pg_attribute`
   rather than transcribed from the header. Ledgered, including the fact that
   no other nailed catalog was audited for the same truncation (same root as
   S13.3 / S14.1).
3. **Guard 4's probe reports per view rather than bisecting by hand.** The six
   blobs are independent, so `t.Errorf` per view names every failure in one
   run — that is what isolated finding 2 (five passes, one failure) without a
   manual flip-one-at-a-time cycle. S6.6's risk control is satisfied more
   cheaply than by the procedure it prescribed.
4. **Guard 2 pins both directions on disk**, not just `pgClassRow` in isolation:
   `TestBootstrappedViewsCarryRelhasrules` reads FormData_pg_class offset 124
   out of `base/1/1259` and `base/5/1259` and requires `true` for every
   `relkind='v'` tuple and `false` for every `relkind='r'` tuple. The false
   direction matters — a spurious `true` on a rule-less catalog sends PG into
   `RelationBuildRuleLock` for nothing, and that failure is *silent*
   (`relcache.c:4313-4318` retries once, then quietly clears its local copy of
   the flag).
5. **S6.5 (`reltype`) remains open and is not this slice's.** The six views
   still carry 2249 RECORDOID where PG mints a per-view composite type;
   M0131-S8a captured upstream's values (12233/12242/12246/12250/12263/12268)
   and pins the divergence as deliberate. Adopting them needs real `pg_type`
   rows — ledgered under S8a. Nothing in the six probes above consults
   `reltype`, so the flip did not surface it.

**Scope limit (ledgered).** `relhasrules` is written at initdb time, so the flip
reaches only freshly `goopg init`'d directories. Every existing goopg `$PGDATA`
keeps `relhasrules='f'` on disk; re-initdb is the only path, and every M0131
gate initdbs fresh so nothing catches it.

**Gates:** `internal/initdb` (61 s) PASS incl. the new and the inverted guard;
`TestE2E_PGColdStartOnGoopgDataDir` PASS; `TestE2E_PGStandbyFullCycle` PASS
(the restored view query); whole `^TestE2E_` family PASS (99 s);
`internal/estimateaudit` + `internal/executor` PASS; UNITS PASS; pgbench smoke
via the commit hook.
