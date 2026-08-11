// Manifest ↔ Go-table agreement guard (M0131-S7.2/S7.3).
//
// scripts/capture-ev-action.sh captures every nailed system view's pg_class
// row and pg_attribute rows from a throwaway real PG 18.3 and writes them to
// internal/initdb/nailed_view_manifest.tsv. The Go tables those artefacts
// describe — the nailedRel entries in nailedLocalRels and their nailedAttr
// lists in relcache_init.go — were hand-transcribed from system_views.sql and
// pg_proc.dat before the tool existed.
//
// This test is design guard #2 of docs/design/0131-0007: it re-checks the
// hand-written tables against the oracle's own capture. Drift is either a
// transcription bug in the committed table or a bug in the tool — both worth
// finding. The failure mode it protects against is not hypothetical: M0131-S6
// found pgSubscriptionAttrs declaring 9 of pg_subscription's 18 columns, which
// a hosted PG reported as `cache lookup failed for attribute 10 of relation
// 6100`. A tupledesc that disagrees with the tuples a rule's plan produces
// deforms garbage (tupdesc.c:105).
//
// The manifest is checked in, so this runs offline with no PG involved. Only
// re-capturing (scripts/capture-ev-action.sh --verify) needs the oracle.

package initdb

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

const nailedViewManifestPath = "nailed_view_manifest.tsv"

type manifestRel struct {
	Name          string
	OracleOID     uint32
	GoopgOID      uint32
	RuleOID       uint32
	OracleRelType uint32
	GoopgRelType  uint32
	RelKind       byte
	RelNatts      int16
	Attrs         []nailedAttr
}

// readNailedViewManifest parses the TSV emitted by
// scripts/capture-ev-action.sh, preserving capture order.
func readNailedViewManifest(t *testing.T) []manifestRel {
	t.Helper()

	f, err := os.Open(nailedViewManifestPath)
	if err != nil {
		t.Fatalf("open manifest: %v (regenerate with scripts/capture-ev-action.sh)", err)
	}
	defer f.Close()

	var (
		rels  []manifestRel
		byIdx = map[string]int{}
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		switch fields[0] {
		case "rel":
			if len(fields) != 9 {
				t.Fatalf("manifest line %d: rel row has %d fields, want 9: %q", line, len(fields), text)
			}
			if len(fields[7]) != 1 {
				t.Fatalf("manifest line %d: relkind %q is not one character", line, fields[7])
			}
			r := manifestRel{
				Name:          fields[1],
				OracleOID:     mustU32(t, line, fields[2]),
				GoopgOID:      mustU32(t, line, fields[3]),
				RuleOID:       mustU32(t, line, fields[4]),
				OracleRelType: mustU32(t, line, fields[5]),
				GoopgRelType:  mustU32(t, line, fields[6]),
				RelKind:       fields[7][0],
				RelNatts:      int16(mustU32(t, line, fields[8])),
			}
			if _, dup := byIdx[r.Name]; dup {
				t.Fatalf("manifest line %d: duplicate rel row for %s", line, r.Name)
			}
			byIdx[r.Name] = len(rels)
			rels = append(rels, r)
		case "attr":
			if len(fields) != 8 {
				t.Fatalf("manifest line %d: attr row has %d fields, want 8: %q", line, len(fields), text)
			}
			idx, ok := byIdx[fields[1]]
			if !ok {
				t.Fatalf("manifest line %d: attr row for %s precedes its rel row", line, fields[1])
			}
			rels[idx].Attrs = append(rels[idx].Attrs, nailedAttr{
				Num:       int16(mustU32(t, line, fields[2])),
				Name:      fields[3],
				TypeOID:   mustU32(t, line, fields[4]),
				Len:       int16(mustI64(t, line, fields[5])),
				NotNull:   mustBool(t, line, fields[6]),
				IsDropped: mustBool(t, line, fields[7]),
			})
		default:
			t.Fatalf("manifest line %d: unknown record kind %q", line, fields[0])
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan manifest: %v", err)
	}
	return rels
}

func mustU32(t *testing.T, line int, s string) uint32 {
	t.Helper()
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		t.Fatalf("manifest line %d: %q is not a uint32: %v", line, s, err)
	}
	return uint32(v)
}

func mustI64(t *testing.T, line int, s string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("manifest line %d: %q is not an integer: %v", line, s, err)
	}
	return v
}

func mustBool(t *testing.T, line int, s string) bool {
	t.Helper()
	switch s {
	case "t":
		return true
	case "f":
		return false
	}
	t.Fatalf("manifest line %d: %q is not psql's t/f boolean", line, s)
	return false
}

// TestNailedViewManifestMatchesGoTables is guard #2: every rel row and every
// attribute row the oracle captured must equal what the hand-written Go tables
// declare.
func TestNailedViewManifestMatchesGoTables(t *testing.T) {
	rels := readNailedViewManifest(t)

	// Non-vacuity: the manifest must cover exactly the pinned view set, or
	// this test silently checks nothing.
	if got, want := len(rels), len(systemViewOIDPins()); got != want {
		t.Fatalf("manifest describes %d views, systemViewOIDPins() has %d — re-run scripts/capture-ev-action.sh", got, want)
	}

	local := map[uint32]nailedRel{}
	for _, r := range nailedLocalRels {
		local[r.OID] = r
	}

	totalAttrs := 0
	for _, m := range rels {
		totalAttrs += len(m.Attrs)

		// The OID mapping is the identity function under the M0131-S8a
		// Option-A policy. If these ever diverge, a captured blob's embedded
		// :relid names the wrong relation in goopg's catalog.
		if m.OracleOID != m.GoopgOID {
			t.Errorf("%s: manifest maps oracle OID %d to goopg OID %d, but M0131-S8a pins them equal",
				m.Name, m.OracleOID, m.GoopgOID)
		}
		pin, ok := systemViewOIDPinByName(m.Name)
		if !ok {
			t.Errorf("%s: in the manifest but not in systemViewOIDPins()", m.Name)
			continue
		}
		if pin.ViewOID != m.GoopgOID || pin.RuleOID != m.RuleOID ||
			pin.UpstreamRelType != m.OracleRelType || pin.RelNatts != int(m.RelNatts) {
			t.Errorf("%s: pin{view %d, rule %d, upstream reltype %d, natts %d} != manifest{view %d, rule %d, reltype %d, natts %d}",
				m.Name, pin.ViewOID, pin.RuleOID, pin.UpstreamRelType, pin.RelNatts,
				m.GoopgOID, m.RuleOID, m.OracleRelType, m.RelNatts)
		}

		// goopg deliberately carries RECORDOID where upstream mints a
		// per-view composite pg_type row (relcache_init.go:679-682). The
		// manifest records both; assert goopg's column still says 2249 so
		// M0131-S6.5 flipping it is a deliberate edit, not a drift.
		if m.GoopgRelType != 2249 {
			t.Errorf("%s: manifest goopg reltype is %d, want 2249 (RECORDOID) until M0131-S6.5 lands real composite types",
				m.Name, m.GoopgRelType)
		}
		if m.RelKind != 'v' {
			t.Errorf("%s: manifest relkind is %q, want 'v'", m.Name, m.RelKind)
		}

		rel, ok := local[m.GoopgOID]
		if !ok {
			t.Errorf("%s: no nailedLocalRels entry with OID %d", m.Name, m.GoopgOID)
			continue
		}
		if rel.RelName != m.Name {
			t.Errorf("OID %d: nailedRel name %q != manifest %q", m.GoopgOID, rel.RelName, m.Name)
		}
		if rel.RelType != m.GoopgRelType {
			t.Errorf("%s: nailedRel reltype %d != manifest goopg reltype %d", m.Name, rel.RelType, m.GoopgRelType)
		}
		if rel.RelKind != m.RelKind {
			t.Errorf("%s: nailedRel relkind %q != manifest %q", m.Name, rel.RelKind, m.RelKind)
		}
		if rel.RelNatts != m.RelNatts {
			t.Errorf("%s: nailedRel relnatts %d != manifest %d", m.Name, rel.RelNatts, m.RelNatts)
		}

		// The attribute table is the part that silently deforms garbage when
		// wrong, so compare field by field rather than with a length check.
		if len(rel.Attrs) != len(m.Attrs) {
			t.Errorf("%s: nailedRel has %d attributes, oracle captured %d",
				m.Name, len(rel.Attrs), len(m.Attrs))
			continue
		}
		for i, want := range m.Attrs {
			got := rel.Attrs[i]
			if got.Name != want.Name || got.TypeOID != want.TypeOID || got.Num != want.Num ||
				got.Len != want.Len || got.NotNull != want.NotNull || got.IsDropped != want.IsDropped {
				t.Errorf("%s attribute %d: Go %+v != oracle %+v", m.Name, want.Num, got, want)
			}
		}
	}

	if totalAttrs == 0 {
		t.Fatal("manifest carries no attribute rows — the guard would be vacuous")
	}
	t.Logf("checked %d views / %d attributes against the oracle capture", len(rels), totalAttrs)
}

// TestNailedViewManifestOracleStampMatchesPins keeps the manifest's captured-from
// header honest: it must name the same oracle build systemViewOIDPins() was
// pinned against, or the two artefacts describe different PostgreSQL versions.
func TestNailedViewManifestOracleStampMatchesPins(t *testing.T) {
	raw, err := os.ReadFile(nailedViewManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var stamp string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "# Oracle:") {
			stamp = line
			break
		}
	}
	if stamp == "" {
		t.Fatal("manifest has no '# Oracle:' stamp line")
	}
	if !strings.Contains(stamp, systemViewOIDOracleVersion) {
		t.Errorf("manifest stamp %q does not name the pinned oracle %q", stamp, systemViewOIDOracleVersion)
	}
	if !strings.Contains(stamp, strconv.Itoa(systemViewOIDOracleCatVersion)) {
		t.Errorf("manifest stamp %q does not name the pinned catalog version %d",
			stamp, systemViewOIDOracleCatVersion)
	}
}
