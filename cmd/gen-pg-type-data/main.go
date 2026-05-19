//go:build ignore

// gen-pg-type-data parses postgres/src/include/catalog/pg_type.dat and
// postgres/src/include/catalog/pg_proc.dat and emits
// internal/initdb/pg_type_seed_data.go containing pgTypeAllEntries().
//
// Run from the repository root:
//
//	go run cmd/gen-pg-type-data/main.go > internal/initdb/pg_type_seed_data.go
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// kvRe matches a single key => 'value' pair.
var kvRe = regexp.MustCompile(`(\w+)\s*=>\s*'([^']*)'`)

// splitBlocks splits .dat text into per-entry blocks by finding '{' ... '}'.
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

// parseKV extracts all key => 'value' pairs from a block.
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

// parseProcDat builds a function-name → OID map from pg_proc.dat.
// When multiple procs share a name, keeps the first (lowest OID), which is
// what PG18's regprocin resolves for unambiguous I/O function references.
func parseProcDat(path string) (map[string]uint32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	procByName := map[string]uint32{}
	blocks := splitBlocks(string(data))
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
		name, ok := m["proname"]
		if !ok || name == "" {
			continue
		}
		if _, exists := procByName[name]; !exists {
			procByName[name] = uint32(oidVal)
		}
	}
	return procByName, nil
}

// procOID returns the OID for a function name, logging a warning for unknowns.
func procOID(name string, procByName map[string]uint32) uint32 {
	if name == "" || name == "-" {
		return 0
	}
	if oid, ok := procByName[name]; ok {
		return oid
	}
	fmt.Fprintf(os.Stderr, "WARNING: proc name %q not found — using 0\n", name)
	return 0
}

// typeEntry mirrors internal/initdb.pgTypeEntry for generation purposes.
type typeEntry struct {
	OID      uint32
	Name     string
	Len      int16
	ByVal    bool
	Type     byte // typtype
	Category byte // typcategory
	Align    byte // typalign: 'c','s','i','d'
	Storage  byte // typstorage: 'p','e','x','m'
	Input    uint32
	Output   uint32
	Receive  uint32
	Send     uint32
}

// resolveLen converts pg_type.dat typlen strings to int16.
func resolveLen(s string) int16 {
	s = strings.TrimSpace(s)
	switch s {
	case "-1", "ANYSIZE":
		return -1
	case "NAMEDATALEN":
		return 64
	case "SIZEOF_POINTER", "ALIGNOF_POINTER":
		return 8
	}
	v, err := strconv.ParseInt(s, 10, 16)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: unresolved typlen %q\n", s)
		return -1
	}
	return int16(v)
}

// resolveByVal converts pg_type.dat typbyval strings to bool.
func resolveByVal(s string) bool {
	switch strings.TrimSpace(s) {
	case "t", "FLOAT8PASSBYVAL": // 64-bit platforms pass float8 by value
		return true
	}
	return false
}

// resolveAlign converts pg_type.dat typalign strings to byte.
func resolveAlign(s string) byte {
	switch strings.TrimSpace(s) {
	case "s":
		return 's'
	case "i":
		return 'i'
	case "d", "ALIGNOF_POINTER": // 64-bit
		return 'd'
	default:
		return 'c' // 'c' is the default/minimum
	}
}

// resolveStorage converts pg_type.dat typstorage strings to byte.
func resolveStorage(s string) byte {
	switch strings.TrimSpace(s) {
	case "e":
		return 'e'
	case "x":
		return 'x'
	case "m":
		return 'm'
	default:
		return 'p' // 'p' is the default
	}
}

// resolveType converts pg_type.dat typtype strings to byte.
func resolveType(s string) byte {
	switch strings.TrimSpace(s) {
	case "c":
		return 'c'
	case "p":
		return 'p'
	case "d":
		return 'd'
	case "e":
		return 'e'
	case "r":
		return 'r'
	case "m":
		return 'm'
	default:
		return 'b' // 'b' is the default (base type)
	}
}

func main() {
	typeDatPath := "postgres/src/include/catalog/pg_type.dat"
	procDatPath := "postgres/src/include/catalog/pg_proc.dat"

	procByName, err := parseProcDat(procDatPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR parsing %s: %v\n", procDatPath, err)
		os.Exit(1)
	}

	typeDatData, err := os.ReadFile(typeDatPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR reading %s: %v\n", typeDatPath, err)
		os.Exit(1)
	}

	type rawEntry struct {
		kv           map[string]string
		arrayTypeOID uint32
	}

	blocks := splitBlocks(string(typeDatData))
	rawEntries := make([]rawEntry, 0, len(blocks))
	for _, block := range blocks {
		m := parseKV(block)
		if _, ok := m["oid"]; !ok {
			continue
		}
		var arrayOID uint32
		if s, ok := m["array_type_oid"]; ok && s != "" {
			v, err := strconv.ParseUint(s, 10, 32)
			if err == nil {
				arrayOID = uint32(v)
			}
		}
		rawEntries = append(rawEntries, rawEntry{kv: m, arrayTypeOID: arrayOID})
	}

	// Build base entries.
	var entries []typeEntry
	// Track which OIDs we've generated to avoid duplicates.
	seen := map[uint32]bool{}

	for _, re := range rawEntries {
		m := re.kv
		oidStr := m["oid"]
		oidVal, err := strconv.ParseUint(oidStr, 10, 32)
		if err != nil {
			continue
		}
		oid := uint32(oidVal)
		if seen[oid] {
			continue
		}
		seen[oid] = true

		name := m["typname"]
		typLen := resolveLen(m["typlen"])
		typByVal := resolveByVal(m["typbyval"])
		typType := resolveType(m["typtype"])
		typCat := byte('U') // default
		if cat, ok := m["typcategory"]; ok && len(cat) > 0 {
			typCat = cat[0]
		}
		typAlign := resolveAlign(m["typalign"])
		typStorage := resolveStorage(m["typstorage"])
		input := procOID(m["typinput"], procByName)
		output := procOID(m["typoutput"], procByName)
		receive := procOID(m["typreceive"], procByName)
		send := procOID(m["typsend"], procByName)

		entries = append(entries, typeEntry{
			OID:      oid,
			Name:     name,
			Len:      typLen,
			ByVal:    typByVal,
			Type:     typType,
			Category: typCat,
			Align:    typAlign,
			Storage:  typStorage,
			Input:    input,
			Output:   output,
			Receive:  receive,
			Send:     send,
		})
	}

	// Build array peer entries.
	// Array I/O functions are the generic array quartet.
	const (
		arrayIn   uint32 = 750
		arrayOut  uint32 = 751
		arrayRecv uint32 = 2400
		arraySend uint32 = 2401
	)
	for _, re := range rawEntries {
		if re.arrayTypeOID == 0 {
			continue
		}
		if seen[re.arrayTypeOID] {
			continue
		}
		seen[re.arrayTypeOID] = true

		baseName := re.kv["typname"]
		baseAlign := resolveAlign(re.kv["typalign"])
		// Array alignment: 'd' if element is 'd', otherwise 'i'.
		arrayAlign := byte('i')
		if baseAlign == 'd' {
			arrayAlign = 'd'
		}
		entries = append(entries, typeEntry{
			OID:      re.arrayTypeOID,
			Name:     "_" + baseName,
			Len:      -1,
			ByVal:    false,
			Type:     'b',
			Category: 'A',
			Align:    arrayAlign,
			Storage:  'x',
			Input:    arrayIn,
			Output:   arrayOut,
			Receive:  arrayRecv,
			Send:     arraySend,
		})
	}

	// Sort by OID ascending.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].OID < entries[j].OID
	})

	// Emit the Go source file.
	fmt.Printf("// Code generated by cmd/gen-pg-type-data/main.go; DO NOT EDIT.\n")
	fmt.Printf("package initdb\n\n")
	fmt.Printf("// pgTypeAllEntries returns all PG18 base type entries from pg_type.dat\n")
	fmt.Printf("// plus auto-generated array peer entries. Includes %d entries total.\n", len(entries))
	fmt.Printf("func pgTypeAllEntries() []pgTypeEntry {\n")
	fmt.Printf("\treturn []pgTypeEntry{\n")
	for _, e := range entries {
		fmt.Printf("\t\t{OID: %d, Name: %q, Len: %d, ByVal: %v, Type: '%c', Category: '%c', Align: '%c', Storage: '%c', Input: %d, Output: %d, Receive: %d, Send: %d},\n",
			e.OID, e.Name, e.Len, e.ByVal,
			e.Type, e.Category, e.Align, e.Storage,
			e.Input, e.Output, e.Receive, e.Send)
	}
	fmt.Printf("\t}\n")
	fmt.Printf("}\n")
}
