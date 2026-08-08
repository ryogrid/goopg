//go:build ignore

// gen-pg-proc-data parses postgres/src/include/catalog/pg_type.dat and
// postgres/src/include/catalog/pg_proc.dat and emits
// internal/initdb/pg_proc_seed_data.go containing pgProcAllEntries().
//
// Run from the repository root:
//
//	go run cmd/gen-pg-proc-data/main.go > internal/initdb/pg_proc_seed_data.go
//
// With -names, it instead emits a name-only OID→proname reverse index plus an
// OID→argument-type-names index (package catalog) for regproc/regprocedure
// output rendering — a leaf-package copy of the same generated source, since
// internal/executor and internal/server cannot import internal/initdb (import
// cycle: initdb already imports executor):
//
//	go run cmd/gen-pg-proc-data/main.go -names > internal/catalog/pg_proc_names_generated.go
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// kvRe matches a single key => 'value' pair. We use a non-greedy match on the
// value so that multiple pairs on the same line are each captured separately.
// Escaped single-quotes (e.g. descr => 'foo\'bar') are handled by skipping the
// descr field entirely rather than trying to parse through escaped quotes.
var kvRe = regexp.MustCompile(`(\w+)\s*=>\s*'([^']*)'`)

// parseTypeDat builds a typname→OID map from pg_type.dat. It also adds
// _typname→array_type_oid for entries that carry array_type_oid.
func parseTypeDat(path string) (map[string]uint32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	typeMap := map[string]uint32{}

	// Split into blocks (one Perl hash per proc). Each block starts with '{'
	// and ends with '}'. We split on '},' or '}' + newline.
	text := string(data)
	// Use a simple state machine: collect text between { and }
	blocks := splitBlocks(text)
	for _, block := range blocks {
		m := parseKV(block)
		oidStr, ok := m["oid"]
		if !ok {
			continue
		}
		oidVal, err := strconv.ParseUint(oidStr, 10, 32)
		if err != nil {
			continue
		}
		oid := uint32(oidVal)
		if name, ok := m["typname"]; ok && name != "" {
			typeMap[name] = oid
		}
		// Array type OID mapping: _typname → array_type_oid
		if arrayOIDStr, ok := m["array_type_oid"]; ok && arrayOIDStr != "" {
			arrayOID, err := strconv.ParseUint(arrayOIDStr, 10, 32)
			if err == nil && arrayOID > 0 {
				if name, ok := m["typname"]; ok && name != "" {
					typeMap["_"+name] = uint32(arrayOID)
				}
			}
		}
	}
	return typeMap, nil
}

// splitBlocks splits the catalog .dat text into per-entry blocks by finding
// '{' ... '}' at the top-level (not nested).
func splitBlocks(text string) []string {
	var blocks []string
	depth := 0
	start := -1
	for i, ch := range text {
		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				blocks = append(blocks, text[start:i+1])
				start = -1
			}
		}
	}
	return blocks
}

// parseKV extracts all key => 'value' pairs from a block. It intentionally
// skips the 'descr' key because the description strings may contain escaped
// single quotes that defeat the simple regex.
func parseKV(block string) map[string]string {
	m := map[string]string{}
	matches := kvRe.FindAllStringSubmatch(block, -1)
	for _, match := range matches {
		key := match[1]
		val := match[2]
		if key == "descr" {
			continue
		}
		m[key] = val
	}
	return m
}

type procEntry struct {
	OID          uint32
	Name         string
	RetType      uint32
	ArgTypes     []uint32 // nil means absent (→ use default [2281] for AM handlers)
	ArgTypeNames []string // raw proargtypes typname tokens (INPUT args only), nil if the key was absent
	Volatile     byte     // 0 means absent (default 'v')
	Parallel     byte     // 0 means absent (default 's')
	RetSet       bool
	NotStrict    bool // inverse of proisstrict; absent → strict (false)
	Lang         uint32
	HandlerName  string
	AllArgTypes  []uint32
	ArgModes     []byte
	ArgNames     []string
}

func resolveType(name string, typeMap map[string]uint32) uint32 {
	if name == "" {
		return 0
	}
	if oid, ok := typeMap[name]; ok {
		return oid
	}
	fmt.Fprintf(os.Stderr, "WARNING: unknown type name %q — using OID 0\n", name)
	return 0
}

func parseArgTypes(raw string, typeMap map[string]uint32) []uint32 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []uint32{} // explicitly empty (non-nil)
	}
	parts := strings.Fields(raw)
	out := make([]uint32, 0, len(parts))
	for _, p := range parts {
		out = append(out, resolveType(p, typeMap))
	}
	return out
}

// parseArgTypeNames splits a raw proargtypes field into its raw typname
// tokens, without resolving them to OIDs — this preserves the exact typname
// spelling pg_type.dat uses (e.g. "int4", not the display alias "integer"),
// which RegprocedureName's display-alias table (catalog.pgArgTypeDisplayAlias)
// then converts at render time, mirroring format_type_be.
func parseArgTypeNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	return strings.Fields(raw)
}

func parseArrayField(raw string) []string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "{")
	raw = strings.TrimSuffix(raw, "}")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func parseOIDArray(raw string, typeMap map[string]uint32) []uint32 {
	parts := parseArrayField(raw)
	if len(parts) == 0 {
		return nil
	}
	out := make([]uint32, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		out = append(out, resolveType(p, typeMap))
	}
	return out
}

func parseArgModes(raw string) []byte {
	parts := parseArrayField(raw)
	if len(parts) == 0 {
		return nil
	}
	out := make([]byte, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) > 0 {
			out = append(out, p[0])
		}
	}
	return out
}

func parseArgNames(raw string) []string {
	parts := parseArrayField(raw)
	if len(parts) == 0 {
		return nil
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func parseProcDat(path string, typeMap map[string]uint32) ([]procEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(data)
	blocks := splitBlocks(text)

	var entries []procEntry
	for _, block := range blocks {
		m := parseKV(block)
		oidStr, ok := m["oid"]
		if !ok {
			continue
		}
		oidVal, err := strconv.ParseUint(oidStr, 10, 32)
		if err != nil {
			continue
		}
		oid := uint32(oidVal)
		name := m["proname"]
		if name == "" {
			continue
		}

		// prorettype
		retTypeName := m["prorettype"]
		retType := resolveType(retTypeName, typeMap)

		// proargtypes: absent means nil (AM handler default); empty string means []uint32{}
		var argTypes []uint32
		if raw, present := m["proargtypes"]; present {
			argTypes = parseArgTypes(raw, typeMap)
		}
		// If absent from the KV map (e.g. no proargtypes key at all), leave nil.
		// But wait: the regex only finds keys with values. If proargtypes is absent
		// from the block entirely, it won't be in m. If it's proargtypes => '',
		// it will be in m with value "".
		// We need to distinguish absent vs empty. Let's check if key is present.
		if _, present := m["proargtypes"]; !present {
			// Key absent → use nil (AM handler default = [internal])
			argTypes = nil
		}

		// argTypeNames: same presence rule as argTypes, but keeping the raw
		// typname tokens (see parseArgTypeNames doc comment).
		var argTypeNames []string
		if raw, present := m["proargtypes"]; present {
			argTypeNames = parseArgTypeNames(raw)
		}

		// provolatile: absent = 'v'
		var vol byte
		if v, ok := m["provolatile"]; ok && len(v) > 0 {
			vol = v[0]
		}

		// proparallel: absent = 's'
		var parallel byte
		if p, ok := m["proparallel"]; ok && len(p) > 0 {
			parallel = p[0]
		}

		// proretset: absent = false
		retSet := m["proretset"] == "t"

		// proisstrict: absent = 't' (strict), so NotStrict = (proisstrict == 'f')
		notStrict := m["proisstrict"] == "f"

		// prolang: absent = 12 (internal), 'sql' = 14, 'c' = 13
		var lang uint32
		switch m["prolang"] {
		case "sql":
			lang = 14
		case "c":
			lang = 13
		default:
			lang = 0 // 0 → use 12 (internal)
		}

		// prosrc
		handlerName := m["prosrc"]

		// proallargtypes
		var allArgTypes []uint32
		if raw, ok := m["proallargtypes"]; ok && raw != "" {
			allArgTypes = parseOIDArray(raw, typeMap)
		}

		// proargmodes
		var argModes []byte
		if raw, ok := m["proargmodes"]; ok && raw != "" {
			argModes = parseArgModes(raw)
		}

		// proargnames
		var argNames []string
		if raw, ok := m["proargnames"]; ok && raw != "" {
			argNames = parseArgNames(raw)
		}

		entries = append(entries, procEntry{
			OID:          oid,
			Name:         name,
			RetType:      retType,
			ArgTypes:     argTypes,
			ArgTypeNames: argTypeNames,
			Volatile:     vol,
			Parallel:     parallel,
			RetSet:       retSet,
			NotStrict:    notStrict,
			Lang:         lang,
			HandlerName:  handlerName,
			AllArgTypes:  allArgTypes,
			ArgModes:     argModes,
			ArgNames:     argNames,
		})
	}

	// Sort by OID
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].OID < entries[j].OID
	})
	return entries, nil
}

func goUint32Slice(s []uint32) string {
	var sb strings.Builder
	sb.WriteString("[]uint32{")
	for i, v := range s {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%d", v)
	}
	sb.WriteString("}")
	return sb.String()
}

func goByteSlice(s []byte) string {
	var sb strings.Builder
	sb.WriteString("[]byte{")
	for i, b := range s {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "'%c'", b)
	}
	sb.WriteString("}")
	return sb.String()
}

func goStringSlice(s []string) string {
	var sb strings.Builder
	sb.WriteString("[]string{")
	for i, v := range s {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%q", v)
	}
	sb.WriteString("}")
	return sb.String()
}

// emitNamesOnly writes a name-only OID→proname reverse index into package
// catalog, mirroring PG's regprocout (src/backend/utils/adt/regproc.c):
// direct column output of a built-in regproc/regprocedure value (e.g.
// pg_type.typinput, pg_operator.oprcode, pg_am.amproc) renders the function
// name, not the raw OID.
func emitNamesOnly(entries []procEntry) {
	fmt.Printf("// Code generated by cmd/gen-pg-proc-data/main.go -names; DO NOT EDIT.\n")
	fmt.Printf("package catalog\n\n")
	fmt.Printf("// pgProcNamesByOID is a generated reverse index of every PG18 pg_proc.dat\n")
	fmt.Printf("// OID -> proname (%d entries), backing RegprocName. It duplicates (name-only)\n", len(entries))
	fmt.Printf("// the fuller pgProcEntry table in internal/initdb/pg_proc_seed_data.go — this\n")
	fmt.Printf("// leaf-package copy exists because internal/executor and internal/server\n")
	fmt.Printf("// cannot import internal/initdb (initdb already imports executor).\n")
	fmt.Printf("var pgProcNamesByOID = map[uint32]string{\n")
	for _, e := range entries {
		fmt.Printf("\t%d: %q,\n", e.OID, e.Name)
	}
	fmt.Printf("}\n\n")

	fmt.Printf("// pgProcArgTypeNamesByOID is a generated reverse index of every PG18\n")
	fmt.Printf("// pg_proc.dat OID -> raw proargtypes typname list (INPUT args only, in the\n")
	fmt.Printf("// exact pg_type.dat spelling), backing RegprocedureName's \"name(argtypes)\"\n")
	fmt.Printf("// rendering. Same leaf-package-copy rationale as pgProcNamesByOID above.\n")
	fmt.Printf("var pgProcArgTypeNamesByOID = map[uint32][]string{\n")
	for _, e := range entries {
		if e.ArgTypeNames == nil {
			continue
		}
		fmt.Printf("\t%d: %s,\n", e.OID, goStringSlice(e.ArgTypeNames))
	}
	fmt.Printf("}\n\n")

	fmt.Printf("// pgProcRetTypeByOID is a generated OID -> prorettype (result type OID)\n")
	fmt.Printf("// index of every PG18 pg_proc.dat entry (%d entries), backing\n", len(entries))
	fmt.Printf("// ProcResultType. The canonical pg_node_tree resolver (02e item C) needs\n")
	fmt.Printf("// a function's funcresulttype to emit a FuncExpr, which the reverse\n")
	fmt.Printf("// name/arg-type indexes above do not carry. Same leaf-package-copy\n")
	fmt.Printf("// rationale as pgProcNamesByOID above.\n")
	fmt.Printf("var pgProcRetTypeByOID = map[uint32]uint32{\n")
	for _, e := range entries {
		fmt.Printf("\t%d: %d,\n", e.OID, e.RetType)
	}
	fmt.Printf("}\n\n")

	fmt.Printf("// pgProcIsStrictByOID is a generated OID -> proisstrict index of every\n")
	fmt.Printf("// PG18 pg_proc.dat entry (%d entries), backing IsStrictProc.\n", len(entries))
	fmt.Printf("// true means the function is strict (returns NULL on NULL input).\n")
	fmt.Printf("// Same leaf-package-copy rationale as pgProcNamesByOID above.\n")
	fmt.Printf("var pgProcIsStrictByOID = map[uint32]bool{\n")
	for _, e := range entries {
		fmt.Printf("\t%d: %v,\n", e.OID, !e.NotStrict)
	}
	fmt.Printf("}\n")
}

func main() {
	namesOnly := flag.Bool("names", false, "emit a name-only OID→proname reverse index (package catalog) instead of the full pgProcAllEntries table")
	flag.Parse()

	typeMap, err := parseTypeDat("postgres/src/include/catalog/pg_type.dat")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: parseTypeDat: %v\n", err)
		os.Exit(1)
	}

	entries, err := parseProcDat("postgres/src/include/catalog/pg_proc.dat", typeMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: parseProcDat: %v\n", err)
		os.Exit(1)
	}

	if *namesOnly {
		emitNamesOnly(entries)
		return
	}

	fmt.Printf("// Code generated by cmd/gen-pg-proc-data/main.go; DO NOT EDIT.\n")
	fmt.Printf("package initdb\n\n")
	fmt.Printf("// pgProcAllEntries returns all %d pg_proc.dat entries for PG18.\n", len(entries))
	fmt.Printf("func pgProcAllEntries() []pgProcEntry {\n")
	fmt.Printf("\treturn []pgProcEntry{\n")

	for _, e := range entries {
		fmt.Printf("\t\t{OID: %d, Name: %q, RetType: %d", e.OID, e.Name, e.RetType)

		// ArgTypes
		if e.ArgTypes == nil {
			// nil = use AM-handler default [2281]; don't emit (zero value)
		} else {
			fmt.Printf(", ArgTypes: %s", goUint32Slice(e.ArgTypes))
		}

		// Volatile: 0 means absent (default 'v'); emit only if non-zero and not 'v'
		if e.Volatile != 0 && e.Volatile != 'v' {
			fmt.Printf(", Volatile: '%c'", e.Volatile)
		}

		// Parallel: 0 means absent (default 's'); emit only if non-zero and not 's'
		if e.Parallel != 0 && e.Parallel != 's' {
			fmt.Printf(", Parallel: '%c'", e.Parallel)
		}

		if e.RetSet {
			fmt.Printf(", RetSet: true")
		}

		if e.NotStrict {
			fmt.Printf(", NotStrict: true")
		}

		if e.Lang != 0 {
			fmt.Printf(", Lang: %d", e.Lang)
		}

		fmt.Printf(", HandlerName: %q", e.HandlerName)

		if e.AllArgTypes != nil {
			fmt.Printf(", AllArgTypes: %s", goUint32Slice(e.AllArgTypes))
		}

		if e.ArgModes != nil {
			fmt.Printf(", ArgModes: %s", goByteSlice(e.ArgModes))
		}

		if e.ArgNames != nil {
			fmt.Printf(", ArgNames: %s", goStringSlice(e.ArgNames))
		}

		fmt.Printf("},\n")
	}

	fmt.Printf("\t}\n")
	fmt.Printf("}\n")
}
