//go:build ignore

// gen-information-schema-procs renders
// internal/initdb/information_schema_proc_manifest.tsv — the oracle capture
// written by `scripts/capture-ev-action.sh --prosqlbody` from a throwaway real
// PostgreSQL 18.3 cluster — into
// internal/initdb/information_schema_proc_seed.go, the 11 information_schema
// helper pg_proc entries that a hosted PG 18.3 needs to rewrite the schema's
// 65 views (M0133-S2).
//
// Run from the repository root:
//
//	go run cmd/gen-information-schema-procs/main.go > internal/initdb/information_schema_proc_seed.go
//
// The split between this tool and the shell script mirrors the view corpus
// (docs/design/0131-0007-ev-action-capture-tooling.md, "Emitters"): the only
// non-reproducible step — running a real PG — lives in the script, so this
// emitter is deterministic, offline and diffable. It is a separate generator
// from cmd/gen-nailed-view-tables because the two manifests have disjoint
// schemas (rel/attr vs proc row) and that generator owns a single stdout
// stream; the `.dat` blob + manifest + render-into-Go shape is the same.
//
// What it does NOT do: derive anything. Every emitted field is a manifest
// column. The prosqlbody node tree itself is NOT emitted here — it is captured
// verbatim into a `<name>_prosqlbody.dat` blob and resolved at runtime through
// nailedProcSqlBody (internal/initdb/information_schema_proc_sqlbody.go), for
// the same reason ev_action is: capture, do not generate.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const manifestPath = "internal/initdb/information_schema_proc_manifest.tsv"

// proc is one information_schema helper pg_proc row.
type proc struct {
	Name       string
	OID        uint32
	Namespace  uint32
	Cost       int64
	Rows       int64
	Support    uint32
	Kind       byte
	Volatile   byte
	Parallel   byte
	IsStrict   bool
	RetSet     bool
	Lang       uint32
	RetType    uint32
	ArgTypes   []uint32
	AllArgType []uint32
	ArgModes   []byte
	ArgNames   []string
	ProcSrc    string
	HasSqlBody bool
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen-information-schema-procs: "+format+"\n", args...)
	os.Exit(1)
}

func mustU32(field string, s string) uint32 {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		die("%s: %q is not a uint32: %v", field, s, err)
	}
	return uint32(v)
}

func mustI64(field string, s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		die("%s: %q is not an int64: %v", field, s, err)
	}
	return v
}

func mustBool(field string, s string) bool {
	switch s {
	case "t":
		return true
	case "f":
		return false
	}
	die("%s: %q is not psql's t/f boolean", field, s)
	return false
}

// mustChar reads a single-character pg_proc field ("char").
func mustChar(field string, s string) byte {
	if len(s) != 1 {
		die("%s: %q is not a single char", field, s)
	}
	return s[0]
}

// parseOidVector parses proargtypes' oidvector ::text (space-separated OIDs).
func parseOidVector(field string, s string) []uint32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Fields(s)
	out := make([]uint32, len(parts))
	for i, p := range parts {
		out[i] = mustU32(field, p)
	}
	return out
}

// parsePGArrayText parses a PG array literal's ::text ("{a,b,c}"), or the "-"
// NULL sentinel (returning nil). Elements are double-quote-escaped in the
// canonical form, so an empty string element is "" and a bare element is
// unquoted. Empty arrays do not occur in this corpus.
func parsePGArrayText(field, s string) []string {
	if s == "-" {
		return nil
	}
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		die("%s: %q is not a PG array literal", field, s)
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []string{}
	}
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if inQuote {
			switch c {
			case '\\':
				i++
				if i < len(inner) {
					cur.WriteByte(inner[i])
				}
			case '"':
				inQuote = false
			default:
				cur.WriteByte(c)
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}

func parseManifest(path string) ([]proc, string) {
	f, err := os.Open(path)
	if err != nil {
		die("open manifest: %v (regenerate with scripts/capture-ev-action.sh --prosqlbody)", err)
	}
	defer f.Close()

	var (
		procs []proc
		byOID = map[uint32]string{}
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
		if fields[0] != "proc" {
			die("line %d: unknown record kind %q", line, fields[0])
		}
		if len(fields) != 20 {
			die("line %d: proc row has %d fields, want 20: %q", line, len(fields), text)
		}
		// fields[1..19] are the 19 data columns (see the manifest header).
		p := proc{
			Name:       fields[1],
			OID:        mustU32("oid", fields[2]),
			Namespace:  mustU32("pronamespace", fields[3]),
			Cost:       mustI64("procost", fields[4]),
			Rows:       mustI64("prorows", fields[5]),
			Support:    mustU32("prosupport", fields[6]),
			Kind:       mustChar("prokind", fields[7]),
			Volatile:   mustChar("provolatile", fields[8]),
			Parallel:   mustChar("proparallel", fields[9]),
			IsStrict:   mustBool("proisstrict", fields[10]),
			RetSet:     mustBool("proretset", fields[11]),
			Lang:       mustU32("prolang", fields[12]),
			RetType:    mustU32("prorettype", fields[13]),
			ArgTypes:   parseOidVector("proargtypes", fields[14]),
			ProcSrc:    fields[18],
			HasSqlBody: fields[19] != "-",
		}
		if oids := fields[15]; oids != "-" {
			for _, s := range parsePGArrayText("proallargtypes", oids) {
				p.AllArgType = append(p.AllArgType, mustU32("proallargtypes", s))
			}
		}
		if modes := fields[16]; modes != "-" {
			for _, s := range parsePGArrayText("proargmodes", modes) {
				p.ArgModes = append(p.ArgModes, mustChar("proargmodes", s))
			}
		}
		if names := fields[17]; names != "-" {
			p.ArgNames = parsePGArrayText("proargnames", names)
		}
		if prev, dup := byOID[p.OID]; dup {
			die("OID %d is claimed by both %s and %s", p.OID, prev, p.Name)
		}
		byOID[p.OID] = p.Name
		procs = append(procs, p)
	}
	if err := sc.Err(); err != nil {
		die("scan manifest: %v", err)
	}
	if len(procs) == 0 {
		die("manifest describes no procs")
	}
	if stamp == "" {
		die("manifest has no '# Oracle:' stamp line")
	}
	return procs, stamp
}

func main() {
	procs, stamp := parseManifest(manifestPath)

	out := os.Stdout
	fmt.Fprintf(out, "// Code generated by cmd/gen-information-schema-procs/main.go; DO NOT EDIT.\n")
	fmt.Fprintf(out, "package initdb\n\n")
	fmt.Fprintf(out, "// informationSchemaHelperProcs returns the %d helper functions that\n", len(procs))
	fmt.Fprintf(out, "// information_schema.sql creates before its domains and views, rendered from\n")
	fmt.Fprintf(out, "// internal/initdb/information_schema_proc_manifest.tsv (M0133-S2).\n")
	fmt.Fprintf(out, "//\n")
	fmt.Fprintf(out, "// %s\n", stamp)
	fmt.Fprintf(out, "//\n")
	fmt.Fprintf(out, "// Ten of the eleven are new-style SQL-standard bodies: prosrc is empty and\n")
	fmt.Fprintf(out, "// the body lives in pg_proc.prosqlbody, captured verbatim into a\n")
	fmt.Fprintf(out, "// <name>_prosqlbody.dat blob (information_schema_proc_sqlbody.go) and\n")
	fmt.Fprintf(out, "// resolved at runtime through nailedProcSqlBody. _pg_expandarray is the\n")
	fmt.Fprintf(out, "// only one with a textual prosrc and prosqlbody = NULL.\n")
	fmt.Fprintf(out, "func informationSchemaHelperProcs() []pgProcEntry {\n")
	fmt.Fprintf(out, "\treturn []pgProcEntry{\n")
	for _, p := range procs {
		fmt.Fprintf(out, "\t\t{\n")
		fmt.Fprintf(out, "\t\t\tOID:       %d,\n", p.OID)
		fmt.Fprintf(out, "\t\t\tName:      %q,\n", p.Name)
		fmt.Fprintf(out, "\t\t\tNamespace: %d,\n", p.Namespace)
		fmt.Fprintf(out, "\t\t\tRetType:   %d,\n", p.RetType)
		fmt.Fprintf(out, "\t\t\tLang:      %d,\n", p.Lang)
		fmt.Fprintf(out, "\t\t\tCost:      %d,\n", p.Cost)
		if p.Rows != 0 {
			fmt.Fprintf(out, "\t\t\tRows:      %d,\n", p.Rows)
		}
		if p.Support != 0 {
			fmt.Fprintf(out, "\t\t\tSupport:   %d,\n", p.Support)
		}
		fmt.Fprintf(out, "\t\t\tVolatile:  '%c',\n", p.Volatile)
		fmt.Fprintf(out, "\t\t\tParallel:  '%c',\n", p.Parallel)
		if p.RetSet {
			fmt.Fprintf(out, "\t\t\tRetSet:    true,\n")
		}
		fmt.Fprintf(out, "\t\t\tArgTypes:  %s,\n", oidSliceLit(p.ArgTypes))
		if p.ProcSrc != "" {
			fmt.Fprintf(out, "\t\t\tHandlerName: %q, // prosrc\n", p.ProcSrc)
		}
		if p.AllArgType != nil {
			fmt.Fprintf(out, "\t\t\tAllArgTypes: %s,\n", oidSliceLit(p.AllArgType))
		}
		if p.ArgModes != nil {
			fmt.Fprintf(out, "\t\t\tArgModes:    %s,\n", charSliceLit(p.ArgModes))
		}
		if p.ArgNames != nil {
			fmt.Fprintf(out, "\t\t\tArgNames:    %s,\n", strSliceLit(p.ArgNames))
		}
		if p.HasSqlBody {
			fmt.Fprintf(out, "\t\t\tSqlBody:   %q,\n", p.Name)
		}
		fmt.Fprintf(out, "\t\t},\n")
	}
	fmt.Fprintf(out, "\t}\n")
	fmt.Fprintf(out, "}\n")
}

func oidSliceLit(v []uint32) string {
	if len(v) == 0 {
		return "nil"
	}
	parts := make([]string, len(v))
	for i, o := range v {
		parts[i] = strconv.FormatUint(uint64(o), 10)
	}
	return "[]uint32{" + strings.Join(parts, ", ") + "}"
}

func charSliceLit(v []byte) string {
	parts := make([]string, len(v))
	for i, c := range v {
		parts[i] = "'" + string(c) + "'"
	}
	return "[]byte{" + strings.Join(parts, ", ") + "}"
}

func strSliceLit(v []string) string {
	parts := make([]string, len(v))
	for i, s := range v {
		parts[i] = strconv.Quote(s)
	}
	return "[]string{" + strings.Join(parts, ", ") + "}"
}
