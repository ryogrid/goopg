//go:build ignore

// gen-pg-operator-data parses postgres/src/include/catalog/pg_type.dat,
// postgres/src/include/catalog/pg_proc.dat, and
// postgres/src/include/catalog/pg_operator.dat and emits
// internal/initdb/pg_operator_seed_data.go containing pgOperatorAllEntries().
//
// Run from the repository root:
//
//	go run cmd/gen-pg-operator-data/main.go > internal/initdb/pg_operator_seed_data.go
package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var kvRe = regexp.MustCompile(`(\w+)\s*=>\s*'([^']*)'`)

// splitBlocks splits catalog .dat text into per-entry { ... } blocks.
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

func parseKV(block string) map[string]string {
	m := map[string]string{}
	for _, match := range kvRe.FindAllStringSubmatch(block, -1) {
		if match[1] != "descr" {
			m[match[1]] = match[2]
		}
	}
	return m
}

// parseTypeDat builds typname→OID map; includes _typname→array_type_oid.
func parseTypeDat(path string) (map[string]uint32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	typeMap := map[string]uint32{}
	for _, block := range splitBlocks(string(data)) {
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
		if name := m["typname"]; name != "" {
			typeMap[name] = oid
		}
		if arrayOIDStr := m["array_type_oid"]; arrayOIDStr != "" {
			if arrayOID, err := strconv.ParseUint(arrayOIDStr, 10, 32); err == nil && arrayOID > 0 {
				if name := m["typname"]; name != "" {
					typeMap["_"+name] = uint32(arrayOID)
				}
			}
		}
	}
	return typeMap, nil
}

type procInfo struct {
	OID      uint32
	ArgTypes []uint32 // resolved type OIDs
}

// parseProcDat builds procname→[]procInfo map for disambiguation.
func parseProcDat(path string, typeMap map[string]uint32) (map[string][]procInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	procMap := map[string][]procInfo{}
	for _, block := range splitBlocks(string(data)) {
		m := parseKV(block)
		oidStr, ok := m["oid"]
		if !ok {
			continue
		}
		oidVal, err := strconv.ParseUint(oidStr, 10, 32)
		if err != nil {
			continue
		}
		name := m["proname"]
		if name == "" {
			continue
		}
		oid := uint32(oidVal)
		var argTypes []uint32
		if raw := m["proargtypes"]; raw != "" {
			for _, tn := range strings.Fields(raw) {
				argTypes = append(argTypes, resolveType(tn, typeMap))
			}
		}
		procMap[name] = append(procMap[name], procInfo{OID: oid, ArgTypes: argTypes})
	}
	return procMap, nil
}

// oprKey is a lookup key for an operator by (name, leftTypeOID, rightTypeOID).
type oprKey struct {
	Name  string
	Left  uint32
	Right uint32
}

type operatorEntry struct {
	OID        uint32
	Name       string
	Namespace  uint32
	Owner      uint32
	Kind       byte
	CanMerge   bool
	CanHash    bool
	LeftType   uint32
	RightType  uint32
	ResultType uint32
	Commutator uint32
	Negator    uint32
	Code       uint32
	Restrict   uint32
	Join       uint32
}

// parseOprRef parses a cross-reference like '=(int4,int8)' into (name, leftName, rightName).
func parseOprRef(ref string) (name, left, right string) {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "-" {
		return "", "", ""
	}
	parenIdx := strings.Index(ref, "(")
	if parenIdx < 0 {
		return ref, "", ""
	}
	name = ref[:parenIdx]
	rest := strings.TrimSuffix(ref[parenIdx+1:], ")")
	parts := strings.SplitN(rest, ",", 2)
	if len(parts) == 2 {
		left = strings.TrimSpace(parts[0])
		right = strings.TrimSpace(parts[1])
	}
	return name, left, right
}

func resolveType(name string, typeMap map[string]uint32) uint32 {
	if name == "" || name == "0" || name == "-" {
		return 0
	}
	if oid, ok := typeMap[name]; ok {
		return oid
	}
	fmt.Fprintf(os.Stderr, "WARNING: unknown type %q\n", name)
	return 0
}

// resolveProc resolves an oprcode/oprrest/oprjoin reference.
// Handles plain names ("int48eq") and qualified names ("jsonb_delete(jsonb,text)").
func resolveProc(ref string, procMap map[string][]procInfo, typeMap map[string]uint32) uint32 {
	if ref == "" || ref == "0" || ref == "-" {
		return 0
	}
	parenIdx := strings.Index(ref, "(")
	if parenIdx < 0 {
		// Plain name — resolve directly (unique names confirmed by analysis).
		infos := procMap[ref]
		if len(infos) == 0 {
			fmt.Fprintf(os.Stderr, "WARNING: unknown proc %q\n", ref)
			return 0
		}
		if len(infos) > 1 {
			fmt.Fprintf(os.Stderr, "WARNING: ambiguous proc %q (%d matches), using first OID %d\n", ref, len(infos), infos[0].OID)
		}
		return infos[0].OID
	}
	// Qualified name: "funcname(type1,type2,...)".
	funcName := ref[:parenIdx]
	argStr := strings.TrimSuffix(ref[parenIdx+1:], ")")
	var wantArgs []uint32
	for _, tn := range strings.Split(argStr, ",") {
		wantArgs = append(wantArgs, resolveType(strings.TrimSpace(tn), typeMap))
	}
	infos := procMap[funcName]
	for _, info := range infos {
		if len(info.ArgTypes) == len(wantArgs) {
			match := true
			for i, a := range wantArgs {
				if info.ArgTypes[i] != a {
					match = false
					break
				}
			}
			if match {
				return info.OID
			}
		}
	}
	// Fallback: take first with matching name.
	if len(infos) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: no exact arg match for %q, using first OID %d\n", ref, infos[0].OID)
		return infos[0].OID
	}
	fmt.Fprintf(os.Stderr, "WARNING: unknown proc %q\n", ref)
	return 0
}

func resolveOpr(ref string, typeMap map[string]uint32, keyMap map[oprKey]uint32) uint32 {
	name, leftName, rightName := parseOprRef(ref)
	if name == "" {
		return 0
	}
	leftOID := resolveType(leftName, typeMap)
	rightOID := resolveType(rightName, typeMap)
	if oid, ok := keyMap[oprKey{name, leftOID, rightOID}]; ok {
		return oid
	}
	fmt.Fprintf(os.Stderr, "WARNING: unresolved operator ref %q (name=%q left=%d right=%d)\n", ref, name, leftOID, rightOID)
	return 0
}

// parseOperatorDatFirstPass builds the (name, leftOID, rightOID)→OID map.
func parseOperatorDatFirstPass(path string, typeMap map[string]uint32) (map[oprKey]uint32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	keyMap := map[oprKey]uint32{}
	for _, block := range splitBlocks(string(data)) {
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
		name := m["oprname"]
		leftOID := resolveType(m["oprleft"], typeMap)
		rightOID := resolveType(m["oprright"], typeMap)
		keyMap[oprKey{name, leftOID, rightOID}] = oid
	}
	return keyMap, nil
}

func parseOperatorDat(path string, typeMap map[string]uint32, procMap map[string][]procInfo, keyMap map[oprKey]uint32) ([]operatorEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []operatorEntry
	for _, block := range splitBlocks(string(data)) {
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
		name := m["oprname"]
		if name == "" {
			continue
		}

		kind := byte('b')
		if m["oprkind"] == "l" {
			kind = 'l'
		}

		entries = append(entries, operatorEntry{
			OID:        oid,
			Name:       name,
			Namespace:  11,
			Owner:      10,
			Kind:       kind,
			CanMerge:   m["oprcanmerge"] == "t",
			CanHash:    m["oprcanhash"] == "t",
			LeftType:   resolveType(m["oprleft"], typeMap),
			RightType:  resolveType(m["oprright"], typeMap),
			ResultType: resolveType(m["oprresult"], typeMap),
			Commutator: resolveOpr(m["oprcom"], typeMap, keyMap),
			Negator:    resolveOpr(m["oprnegate"], typeMap, keyMap),
			Code:       resolveProc(m["oprcode"], procMap, typeMap),
			Restrict:   resolveProc(m["oprrest"], procMap, typeMap),
			Join:       resolveProc(m["oprjoin"], procMap, typeMap),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].OID < entries[j].OID })
	return entries, nil
}

func main() {
	typeMap, err := parseTypeDat("postgres/src/include/catalog/pg_type.dat")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: parseTypeDat: %v\n", err)
		os.Exit(1)
	}

	procMap, err := parseProcDat("postgres/src/include/catalog/pg_proc.dat", typeMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: parseProcDat: %v\n", err)
		os.Exit(1)
	}

	keyMap, err := parseOperatorDatFirstPass("postgres/src/include/catalog/pg_operator.dat", typeMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: parseOperatorDatFirstPass: %v\n", err)
		os.Exit(1)
	}

	entries, err := parseOperatorDat("postgres/src/include/catalog/pg_operator.dat", typeMap, procMap, keyMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: parseOperatorDat: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("// Code generated by cmd/gen-pg-operator-data/main.go; DO NOT EDIT.\n")
	fmt.Printf("package catalog\n\n")
	fmt.Printf("// PGOperatorAllEntries returns all %d pg_operator.dat entries for PG18.\n", len(entries))
	fmt.Printf("func PGOperatorAllEntries() []OperatorEntry {\n")
	fmt.Printf("\treturn []OperatorEntry{\n")
	for _, e := range entries {
		fmt.Printf("\t\t{OID: %d, Name: %q, Namespace: %d, Owner: %d, Kind: '%c', CanMerge: %v, CanHash: %v, LeftType: %d, RightType: %d, ResultType: %d, Commutator: %d, Negator: %d, Code: %d, Restrict: %d, Join: %d},\n",
			e.OID, e.Name, e.Namespace, e.Owner, e.Kind, e.CanMerge, e.CanHash,
			e.LeftType, e.RightType, e.ResultType, e.Commutator, e.Negator,
			e.Code, e.Restrict, e.Join)
	}
	fmt.Printf("\t}\n")
	fmt.Printf("}\n")
}
