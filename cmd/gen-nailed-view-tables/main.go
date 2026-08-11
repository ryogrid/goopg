//go:build ignore

// gen-nailed-view-tables renders internal/initdb/nailed_view_manifest.tsv —
// the oracle capture written by scripts/capture-ev-action.sh from a throwaway
// real PostgreSQL 18.3 cluster — into internal/initdb/nailed_view_seed_data.go,
// the nailedRel/nailedAttr tables that seed the bootstrap pg_class and
// pg_attribute heaps for goopg's on-disk system views (M0131-S7.4).
//
// Run from the repository root:
//
//	go run cmd/gen-nailed-view-tables/main.go > internal/initdb/nailed_view_seed_data.go
//
// The split between this tool and the shell script is deliberate
// (docs/design/0131-0007-ev-action-capture-tooling.md, "Emitters"): the only
// non-reproducible step — running a real PG — lives in the script, so this
// emitter is deterministic, offline and diffable. Re-run it whenever the
// manifest changes; internal/initdb/nailed_view_manifest_test.go fails if the
// two artefacts drift, which is exactly the "forgot to regenerate" case.
//
// What it does NOT do: derive anything. Every emitted field is a manifest
// column. The two judgement calls the design records are encoded as follows:
//
//   - RelType comes from the manifest's goopg_reltype column (2249, RECORDOID),
//     not upstream's per-view composite type in oracle_reltype. goopg has no
//     real composite pg_type row for a system view yet (M0131-S6.5); the
//     upstream value is emitted as a comment so the divergence stays visible.
//   - OIDs are emitted verbatim because M0131-S8a's Option A pins goopg's
//     assignments to upstream's own (the mapping is the identity function).
//     internal/initdb/system_view_oid_pins_test.go ties the hand-written OID
//     constants to the same pin table, so the literals here cannot drift from
//     the constants without a guard firing.
//
// M0131-S9.0 added the second emitter, nailedViewRewriteEntries(): the
// Form_pg_rewrite _RETURN seed row per view, whose rule OID is the manifest's
// rule_oid column. Every other field of that row is a constant of the ON-SELECT
// rule form (rulename "_RETURN", ev_type CMD_SELECT, ev_enabled ALWAYS,
// is_instead true, empty ev_qual), and the ev_action body is resolved at
// runtime from the embedded blob by view name rather than through a per-view
// //go:embed var — see internal/initdb/nailed_view_ev_action.go. Together those
// close two of the three hand-edits S7.4 ledgered as S9.1's precondition.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const manifestPath = "internal/initdb/nailed_view_manifest.tsv"

type attr struct {
	Num       int16
	Name      string
	TypeOID   uint32
	Len       int16
	NotNull   bool
	IsDropped bool
}

type rel struct {
	Name          string
	OracleOID     uint32
	GoopgOID      uint32
	RuleOID       uint32
	OracleRelType uint32
	GoopgRelType  uint32
	RelKind       byte
	RelNatts      int16
	Attrs         []attr
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen-nailed-view-tables: "+format+"\n", args...)
	os.Exit(1)
}

// parseManifest reads the TSV in capture order. The parser is intentionally
// strict: a malformed row is a bug in the capture script, and emitting a
// half-understood pg_attribute row is how a hosted PG comes to deform garbage
// (tupdesc.c:105).
func parseManifest(path string) ([]rel, string) {
	f, err := os.Open(path)
	if err != nil {
		die("open manifest: %v (regenerate with scripts/capture-ev-action.sh)", err)
	}
	defer f.Close()

	var (
		rels  []rel
		byIdx = map[string]int{}
		stamp string
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if strings.HasPrefix(text, "# Oracle:") {
			stamp = strings.TrimPrefix(text, "# ")
		}
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		switch fields[0] {
		case "rel":
			if len(fields) != 9 {
				die("line %d: rel row has %d fields, want 9: %q", line, len(fields), text)
			}
			if len(fields[7]) != 1 {
				die("line %d: relkind %q is not one character", line, fields[7])
			}
			r := rel{
				Name:          fields[1],
				OracleOID:     mustU32(line, fields[2]),
				GoopgOID:      mustU32(line, fields[3]),
				RuleOID:       mustU32(line, fields[4]),
				OracleRelType: mustU32(line, fields[5]),
				GoopgRelType:  mustU32(line, fields[6]),
				RelKind:       fields[7][0],
				RelNatts:      int16(mustU32(line, fields[8])),
			}
			if _, dup := byIdx[r.Name]; dup {
				die("line %d: duplicate rel row for %s", line, r.Name)
			}
			byIdx[r.Name] = len(rels)
			rels = append(rels, r)
		case "attr":
			if len(fields) != 8 {
				die("line %d: attr row has %d fields, want 8: %q", line, len(fields), text)
			}
			idx, ok := byIdx[fields[1]]
			if !ok {
				die("line %d: attr row for %s precedes its rel row", line, fields[1])
			}
			rels[idx].Attrs = append(rels[idx].Attrs, attr{
				Num:       int16(mustU32(line, fields[2])),
				Name:      fields[3],
				TypeOID:   mustU32(line, fields[4]),
				Len:       int16(mustI64(line, fields[5])),
				NotNull:   mustBool(line, fields[6]),
				IsDropped: mustBool(line, fields[7]),
			})
		default:
			die("line %d: unknown record kind %q", line, fields[0])
		}
	}
	if err := sc.Err(); err != nil {
		die("scan manifest: %v", err)
	}
	if len(rels) == 0 {
		die("manifest describes no relations")
	}
	if stamp == "" {
		die("manifest has no '# Oracle:' stamp line")
	}
	return rels, stamp
}

func mustU32(line int, s string) uint32 {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		die("line %d: %q is not a uint32: %v", line, s, err)
	}
	return uint32(v)
}

func mustI64(line int, s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		die("line %d: %q is not an integer: %v", line, s, err)
	}
	return v
}

func mustBool(line int, s string) bool {
	switch s {
	case "t":
		return true
	case "f":
		return false
	}
	die("line %d: %q is not psql's t/f boolean", line, s)
	return false
}

func main() {
	rels, stamp := parseManifest(manifestPath)

	for _, r := range rels {
		// A rel row whose attribute count disagrees with relnatts would
		// produce a pg_class row promising N columns over a pg_attribute
		// list of M — the shape M0131-S6 hit as
		// "cache lookup failed for attribute 10 of relation 6100".
		if int(r.RelNatts) != len(r.Attrs) {
			die("%s: relnatts %d but %d attribute rows in the manifest", r.Name, r.RelNatts, len(r.Attrs))
		}
		if r.OracleOID != r.GoopgOID {
			die("%s: manifest maps oracle OID %d to goopg OID %d, but M0131-S8a pins them equal",
				r.Name, r.OracleOID, r.GoopgOID)
		}
		for i, a := range r.Attrs {
			if int(a.Num) != i+1 {
				die("%s: attribute %d is out of order (attnum %d)", r.Name, i+1, a.Num)
			}
		}
		// A rule OID of 0 would seed a pg_rewrite row that the 2692 oid index
		// cannot address, and InvalidOid is not a value upstream's initdb ever
		// assigns — so it means the capture script failed to read pg_rewrite
		// for this view (a view captured with no _RETURN rule at all).
		if r.RuleOID == 0 {
			die("%s: manifest carries rule OID 0 — re-run scripts/capture-ev-action.sh %s", r.Name, r.Name)
		}
		// The rule OID shares the 12000..16383 initdb band with the view OID,
		// and a collision between the two would make pg_class and pg_rewrite
		// disagree about which object an OID names.
		if r.RuleOID == r.GoopgOID {
			die("%s: rule OID %d collides with the view OID", r.Name, r.RuleOID)
		}
	}
	// Cross-view OID uniqueness: with 80 views to come (M0131-S9), a duplicate
	// pinned OID is a realistic capture/merge mistake and its symptom on a
	// hosted PG is an arbitrary one of the two objects winning a syscache
	// lookup.
	seen := map[uint32]string{}
	for _, r := range rels {
		for _, p := range []struct {
			oid  uint32
			what string
		}{{r.GoopgOID, "view"}, {r.RuleOID, "rule"}} {
			if prev, dup := seen[p.oid]; dup {
				die("OID %d is claimed by both %s and %s (%s)", p.oid, prev, r.Name, p.what)
			}
			seen[p.oid] = r.Name
		}
	}

	out := os.Stdout
	fmt.Fprintf(out, "// Code generated by cmd/gen-nailed-view-tables/main.go; DO NOT EDIT.\n")
	fmt.Fprintf(out, "package initdb\n\n")
	fmt.Fprintf(out, "// nailedViewSeedRels returns the %d on-disk system views' bootstrap pg_class\n", len(rels))
	fmt.Fprintf(out, "// row and pg_attribute descriptors, rendered from\n")
	fmt.Fprintf(out, "// internal/initdb/nailed_view_manifest.tsv (M0131-S7.4).\n")
	fmt.Fprintf(out, "//\n")
	fmt.Fprintf(out, "// %s\n", stamp)
	fmt.Fprintf(out, "//\n")
	fmt.Fprintf(out, "// The rows carry goopg's RelType (2249, RECORDOID) rather than upstream's\n")
	fmt.Fprintf(out, "// per-view composite type, which goopg does not mint yet (M0131-S6.5); the\n")
	fmt.Fprintf(out, "// upstream value is in each view's comment. IsShared is false for every\n")
	fmt.Fprintf(out, "// system view — they are per-database relations.\n")
	fmt.Fprintf(out, "func nailedViewSeedRels() []nailedRel {\n")
	fmt.Fprintf(out, "\treturn []nailedRel{\n")
	for _, r := range rels {
		fmt.Fprintf(out, "\t\t// %s — _RETURN rule OID %d, upstream reltype %d.\n", r.Name, r.RuleOID, r.OracleRelType)
		fmt.Fprintf(out, "\t\t{\n")
		fmt.Fprintf(out, "\t\t\tOID:      %d,\n", r.GoopgOID)
		fmt.Fprintf(out, "\t\t\tRelName:  %q,\n", r.Name)
		fmt.Fprintf(out, "\t\t\tRelType:  %d,\n", r.GoopgRelType)
		fmt.Fprintf(out, "\t\t\tRelKind:  '%c',\n", r.RelKind)
		fmt.Fprintf(out, "\t\t\tRelNatts: %d,\n", r.RelNatts)
		fmt.Fprintf(out, "\t\t\tAttrs: []nailedAttr{\n")
		for _, a := range r.Attrs {
			fmt.Fprintf(out, "\t\t\t\t{Name: %q, TypeOID: %d, Num: %d, Len: %d", a.Name, a.TypeOID, a.Num, a.Len)
			if a.NotNull {
				fmt.Fprintf(out, ", NotNull: true")
			}
			if a.IsDropped {
				fmt.Fprintf(out, ", IsDropped: true")
			}
			fmt.Fprintf(out, "},\n")
		}
		fmt.Fprintf(out, "\t\t\t},\n")
		fmt.Fprintf(out, "\t\t},\n")
	}
	fmt.Fprintf(out, "\t}\n")
	fmt.Fprintf(out, "}\n")

	// Second emitter (M0131-S9.0): the ON-SELECT rule seed rows.
	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "// nailedViewRewriteEntries returns the %d ON-SELECT (_RETURN) rules that back\n", len(rels))
	fmt.Fprintf(out, "// the on-disk system views, rendered from the same manifest as\n")
	fmt.Fprintf(out, "// nailedViewSeedRels. A hosted PG opening a view with relhasrules=true\n")
	fmt.Fprintf(out, "// resolves the rule through SearchSysCache2(RULERELNAME, view_oid, \"_RETURN\")\n")
	fmt.Fprintf(out, "// and FATALs with \"cache lookup failed for rule …\" if the row is absent, so\n")
	fmt.Fprintf(out, "// every rel row above owes exactly one entry here.\n")
	fmt.Fprintf(out, "//\n")
	fmt.Fprintf(out, "// Only the OIDs and the ev_class link vary: an ON-SELECT view rule is always\n")
	fmt.Fprintf(out, "// _RETURN / CMD_SELECT ('1') / ALWAYS ('O') / INSTEAD, with an empty ev_qual\n")
	fmt.Fprintf(out, "// (\"<>\"). The ev_action body is read from the embedded capture by view name\n")
	fmt.Fprintf(out, "// (nailed_view_ev_action.go), which is why adding a view needs no //go:embed\n")
	fmt.Fprintf(out, "// line.\n")
	fmt.Fprintf(out, "func nailedViewRewriteEntries() []pgRewriteEntry {\n")
	fmt.Fprintf(out, "\treturn []pgRewriteEntry{\n")
	for _, r := range rels {
		fmt.Fprintf(out, "\t\t{\n")
		fmt.Fprintf(out, "\t\t\tOID:       %d,\n", r.RuleOID)
		fmt.Fprintf(out, "\t\t\tRuleName:  \"_RETURN\",\n")
		fmt.Fprintf(out, "\t\t\tEvClass:   %d, // %s\n", r.GoopgOID, r.Name)
		fmt.Fprintf(out, "\t\t\tEvType:    '1',\n")
		fmt.Fprintf(out, "\t\t\tEvEnabled: 'O',\n")
		fmt.Fprintf(out, "\t\t\tIsInstead: true,\n")
		fmt.Fprintf(out, "\t\t\tEvQual:    \"<>\",\n")
		fmt.Fprintf(out, "\t\t\tEvAction:  nailedViewEvAction(%q),\n", r.Name)
		fmt.Fprintf(out, "\t\t},\n")
	}
	fmt.Fprintf(out, "\t}\n")
	fmt.Fprintf(out, "}\n")
}
