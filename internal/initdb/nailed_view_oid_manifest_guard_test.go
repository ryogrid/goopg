// M0131-S8b.1 — the S8 acceptance guard, anchored DIRECTLY to the oracle
// capture.
//
// docs/design/0131-0008-system-view-oid-policy.md guard #1 is worded against
// S7's manifest: "walk nailedSharedRels + nailedLocalRels, select every entry
// with RelKind == 'v', assert its OID equals the OID recorded for that view
// name in internal/initdb/nailed_view_manifest.tsv". When S8a landed, S7's
// manifest did not exist yet, so `TestNailedViewOIDsMatchUpstreamPins`
// compares the nailed tables against `systemViewOIDPins()` — a hand-written Go
// table — and `TestNailedViewManifestMatchesGoTables` separately ties that Go
// table to the TSV. The chain closes, but only transitively, and the middle
// link is the one artefact that is NOT machine-derived from a real PG.
//
// This test removes the intermediary: it reads the TSV the oracle produced and
// holds the nailed pg_class rows to it directly. If a future loop regenerates
// `systemViewOIDPins()` from anything other than a real PG 18.3 initdb, the
// pins-mediated guards can all agree with one another while disagreeing with
// upstream; this one cannot.
//
// The failure it protects against is concrete: a nailed view whose OID differs
// from upstream's makes every `:relid` embedded in a captured `ev_action` blob
// name a different relation than the one the rule belongs to, and a hosted PG
// resolving `pg_catalog.<view>::regclass` gets an OID no rule points at.

package initdb

import "testing"

// TestNailedViewOIDsMatchOracleManifest is design guard #1 of 0131-0008,
// evaluated against the capture rather than against the pin table.
func TestNailedViewOIDsMatchOracleManifest(t *testing.T) {
	manifest := readNailedViewManifest(t)
	if len(manifest) == 0 {
		t.Fatal("manifest carries no rel rows — guard is vacuous")
	}

	byName := map[string]uint32{}
	for _, m := range manifest {
		// Only the oracle's own view rows are relevant; the capture tool
		// emits nothing else today, but assert rather than assume.
		if m.RelKind != 'v' {
			t.Errorf("%s: manifest relkind %q, want 'v'", m.Name, m.RelKind)
			continue
		}
		byName[m.Name] = m.OracleOID
	}

	nailedViews := map[string]nailedRel{}
	for _, group := range [][]nailedRel{nailedSharedRels, nailedLocalRels} {
		for _, r := range group {
			if r.RelKind != 'v' {
				continue
			}
			if prev, dup := nailedViews[r.RelName]; dup {
				t.Errorf("nailed view %q declared twice (OIDs %d and %d)",
					r.RelName, prev.OID, r.OID)
			}
			nailedViews[r.RelName] = r
		}
	}
	if len(nailedViews) == 0 {
		t.Fatal("no relkind='v' nailed relations found — guard is vacuous")
	}

	// Direction 1: every nailed view carries the OID upstream's initdb
	// assigned to that name. A nailed view the oracle never captured is an
	// unverified in-band assignment, which the Option-A policy forbids just as
	// firmly as a wrong one.
	for name, rel := range nailedViews {
		oracleOID, ok := byName[name]
		if !ok {
			t.Errorf("nailed view %q (OID %d) has no row in %s — capture it with "+
				"scripts/capture-ev-action.sh, or move it above "+
				"goopgOnlySystemViewOIDBase (%d)",
				name, rel.OID, nailedViewManifestPath, goopgOnlySystemViewOIDBase)
			continue
		}
		if rel.OID != oracleOID {
			t.Errorf("%s: nailed OID %d, oracle (PG 18.3 initdb) OID %d",
				name, rel.OID, oracleOID)
		}
	}

	// Direction 2: every captured view is actually nailed. Deleting a nailed
	// entry must not silently shrink what this guard checks.
	for name, oracleOID := range byName {
		if _, ok := nailedViews[name]; !ok {
			t.Errorf("manifest captured %s (OID %d) but no nailed pg_class row declares it",
				name, oracleOID)
		}
	}

	t.Logf("checked %d nailed views against the oracle capture", len(nailedViews))
}
