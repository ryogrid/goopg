//go:build ignore

// gen-information-schema-views renders
// internal/initdb/information_schema_view_manifest.tsv — the oracle capture
// written by `scripts/capture-ev-action.sh --information-schema` from a
// throwaway real PostgreSQL 18.3 cluster — into
// internal/initdb/information_schema_view_seed_data.go, the nailedRel /
// pgRewriteEntry tables that seed the bootstrap pg_class, pg_attribute and
// pg_rewrite heaps for goopg's on-disk information_schema views (M0133-S4).
//
// Run from the repository root:
//
//	go run cmd/gen-information-schema-views/main.go > internal/initdb/information_schema_view_seed_data.go
//
// It is a near-copy of cmd/gen-nailed-view-tables, split for the same reason
// cmd/gen-information-schema-procs was (M0133-S2): the information_schema view
// corpus lives in a DIFFERENT namespace (13273, not 11) and is seeded by a
// DIFFERENT list — informationSchemaViewRels, which (like the data tables in
// information_schema_tables.go) rides the on-disk catalogs but NOT
// pg_internal.init, because upstream never nails information_schema relations.
// The pg_catalog generator owns nailedViewSeedRels and its stdout stream; this
// one owns informationSchemaViewSeedRels and informationSchemaViewRewriteEntries.
//
// Same capture rules apply unchanged: OIDs are emitted verbatim (Option-A
// identity pinning, information_schema_view_oid_pins.go), RelType comes from
// the manifest's goopg_reltype column (2249, RECORDOID — the deliberate
// M0131-S6.5 divergence), and nothing is derived.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const manifestPath = "internal/initdb/information_schema_view_manifest.tsv"

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
	fmt.Fprintf(os.Stderr, "gen-information-schema-views: "+format+"\n", args...)
	os.Exit(1)
}

func parseManifest(path string) ([]rel, string) {
	f, err := os.Open(path)
	if err != nil {
		die("open manifest: %v (regenerate with scripts/capture-ev-action.sh --information-schema)", err)
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
		if int(r.RelNatts) != len(r.Attrs) {
			die("%s: relnatts %d but %d attribute rows in the manifest", r.Name, r.RelNatts, len(r.Attrs))
		}
		if r.OracleOID != r.GoopgOID {
			die("%s: manifest maps oracle OID %d to goopg OID %d, but M0133-S4 pins them equal",
				r.Name, r.OracleOID, r.GoopgOID)
		}
		if r.RelKind != 'v' {
			die("%s: relkind %q — information_schema views are relkind 'v'", r.Name, r.RelKind)
		}
		for i, a := range r.Attrs {
			if int(a.Num) != i+1 {
				die("%s: attribute %d is out of order (attnum %d)", r.Name, i+1, a.Num)
			}
		}
		if r.RuleOID == 0 {
			die("%s: manifest carries rule OID 0 — re-run scripts/capture-ev-action.sh --information-schema %s", r.Name, r.Name)
		}
		if r.RuleOID == r.GoopgOID {
			die("%s: rule OID %d collides with the view OID", r.Name, r.RuleOID)
		}
	}
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
	fmt.Fprintf(out, "// Code generated by cmd/gen-information-schema-views/main.go; DO NOT EDIT.\n")
	fmt.Fprintf(out, "package initdb\n\n")
	fmt.Fprintf(out, "// informationSchemaViewSeedRels returns the %d on-disk information_schema\n", len(rels))
	fmt.Fprintf(out, "// views' bootstrap pg_class row and pg_attribute descriptors, rendered from\n")
	fmt.Fprintf(out, "// internal/initdb/information_schema_view_manifest.tsv (M0133-S4).\n")
	fmt.Fprintf(out, "//\n")
	fmt.Fprintf(out, "// %s\n", stamp)
	fmt.Fprintf(out, "//\n")
	fmt.Fprintf(out, "// The rows carry goopg's RelType (2249, RECORDOID) rather than upstream's\n")
	fmt.Fprintf(out, "// per-view composite type, the same deliberate divergence as the pg_catalog\n")
	fmt.Fprintf(out, "// corpus (M0131-S6.5); the upstream value is in each view's comment.\n")
	fmt.Fprintf(out, "func informationSchemaViewSeedRels() []nailedRel {\n")
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

	// Second emitter: the ON-SELECT _RETURN rule seed rows, mirroring
	// nailedViewRewriteEntries for the pg_catalog corpus.
	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "// informationSchemaViewRewriteEntries returns the %d ON-SELECT (_RETURN)\n", len(rels))
	fmt.Fprintf(out, "// rules that back the on-disk information_schema views, rendered from the same\n")
	fmt.Fprintf(out, "// manifest as informationSchemaViewSeedRels. Identical rule form to the\n")
	fmt.Fprintf(out, "// pg_catalog corpus: _RETURN / CMD_SELECT / ALWAYS / INSTEAD, empty ev_qual.\n")
	fmt.Fprintf(out, "func informationSchemaViewRewriteEntries() []pgRewriteEntry {\n")
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
