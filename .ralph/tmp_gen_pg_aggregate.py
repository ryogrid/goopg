#!/usr/bin/env python3
"""Generate pg_aggregate_bootstrap.go for the goopg initdb package.

Reads pg_aggregate.dat, pg_proc.dat, pg_type.dat, pg_operator.dat
from the postgres source tree and emits a Go source file with all
161 pg_aggregate BKI rows.
"""

import re
import sys
import os

PG_CATALOG = "/home/ryo/work/goopg/goopg/postgres/src/include/catalog"

# ---------------------------------------------------------------------------
# Generic .dat parser — extracts list of {key: value} dicts
# ---------------------------------------------------------------------------

def parse_dat(path):
    """Parse a PostgreSQL .dat catalog file into a list of dicts.

    Uses a brace-counter to correctly extract top-level { ... } blocks,
    so that values containing nested { } (e.g. agginitval => '{0,0}')
    are preserved verbatim.
    """
    with open(path) as f:
        text = f.read()
    # Remove C-style /* */ comments
    text = re.sub(r'/\*.*?\*/', '', text, flags=re.DOTALL)
    # Remove # line comments only when # appears as the first non-whitespace
    # character on a line (Perl/dat style). Do NOT remove # inside values
    # such as oprname => '?#' or oprcom => '?#(line,line)'.
    text = re.sub(r'(?m)^[ \t]*#[^\n]*', '', text)

    # Extract top-level { ... } blocks using brace counting.
    # Values like agginitval => '{0,0}' contain nested braces that must be
    # skipped over (they appear inside single-quoted strings).
    results = []
    i = 0
    while i < len(text):
        if text[i] == '{':
            depth = 1
            start = i + 1
            i += 1
            in_quote = False
            while i < len(text) and depth > 0:
                ch = text[i]
                if ch == '\\' and in_quote:
                    # Perl single-quoted escape: \' or \\ — skip the next char.
                    i += 2
                    continue
                if ch == "'" and not in_quote:
                    in_quote = True
                elif ch == "'" and in_quote:
                    in_quote = False
                elif ch == '{' and not in_quote:
                    depth += 1
                elif ch == '}' and not in_quote:
                    depth -= 1
                i += 1
            block = text[start:i - 1]
            d = {}
            for k, v in re.findall(r"(\w+)\s*=>\s*'([^']*)'", block):
                d[k] = v
            if d:
                results.append(d)
        else:
            i += 1
    return results

# ---------------------------------------------------------------------------
# Build lookup tables
# ---------------------------------------------------------------------------

def build_type_map(type_rows):
    """Returns typname -> oid and also handles array types (_typname -> oid)."""
    m = {}
    for r in type_rows:
        if 'typname' in r and 'oid' in r:
            m[r['typname']] = int(r['oid'])
    # Array types: for each type with array_type_oid, add _typname -> array_type_oid
    for r in type_rows:
        if 'typname' in r and 'array_type_oid' in r:
            arr_name = '_' + r['typname']
            if arr_name not in m:
                m[arr_name] = int(r['array_type_oid'])
    return m

def build_proc_map(proc_rows, type_map):
    """Returns (proname, argtypes_tuple) -> oid.
    Also builds proname -> list of oids (for unambiguous lookups).
    """
    by_sig = {}
    by_name = {}
    for r in proc_rows:
        if 'proname' not in r or 'oid' not in r:
            continue
        oid = int(r['oid'])
        name = r['proname']
        # Parse proargtypes (space-separated type names)
        argtypes_str = r.get('proargtypes', '')
        if argtypes_str.strip():
            argtypes = tuple(argtypes_str.strip().split())
        else:
            argtypes = ()
        sig = (name, argtypes)
        by_sig[sig] = oid
        if name not in by_name:
            by_name[name] = []
        by_name[name].append(oid)
    return by_sig, by_name

def build_operator_map(op_rows, type_map):
    """Returns (oprname, oprleft_oid, oprright_oid) -> oid."""
    m = {}
    by_name = {}
    for r in op_rows:
        if 'oprname' not in r or 'oid' not in r:
            continue
        oid = int(r['oid'])
        name = r['oprname']
        left = type_map.get(r.get('oprleft', ''), 0)
        right = type_map.get(r.get('oprright', ''), 0)
        m[(name, left, right)] = oid
        if name not in by_name:
            by_name[name] = []
        by_name[name].append((oid, left, right))
    return m, by_name

# ---------------------------------------------------------------------------
# Resolve functions referenced in pg_aggregate.dat
# ---------------------------------------------------------------------------

def resolve_func_with_args(spec, proc_by_sig, proc_by_name, type_map):
    """Resolve 'funcname(type1, type2)' or 'funcname' to OID.
    Returns 0 for '-' (BKI_DEFAULT(-) meaning NULL/0).
    """
    if spec == '-' or not spec:
        return 0
    # Check for parentheses: 'funcname(type1, type2, ...)'
    m = re.match(r'^(\w+)\(([^)]*)\)$', spec)
    if m:
        funcname = m.group(1)
        argstr = m.group(2).strip()
        if argstr:
            argtypes = tuple(a.strip() for a in argstr.split(','))
        else:
            argtypes = ()
        sig = (funcname, argtypes)
        if sig in proc_by_sig:
            return proc_by_sig[sig]
        # Try with type aliases
        # Try without array prefix issues
        print(f"  WARN: func sig not found: {spec!r} -> {sig}", file=sys.stderr)
        # Try name-only fallback
        candidates = proc_by_name.get(funcname, [])
        if len(candidates) == 1:
            print(f"    -> fallback to unique name {funcname} OID={candidates[0]}", file=sys.stderr)
            return candidates[0]
        if candidates:
            print(f"    -> multiple candidates for {funcname}: {candidates}", file=sys.stderr)
        return 0
    else:
        # Plain name, no args
        funcname = spec
        candidates = proc_by_name.get(funcname, [])
        if len(candidates) == 1:
            return candidates[0]
        if len(candidates) > 1:
            print(f"  WARN: ambiguous func name {funcname!r}: {candidates}", file=sys.stderr)
            # Return first as best guess
            return candidates[0]
        print(f"  WARN: unknown func name {funcname!r}", file=sys.stderr)
        return 0

def resolve_type(name, type_map):
    """Resolve type name to OID. Returns 0 for '-'."""
    if name == '-' or not name:
        return 0
    oid = type_map.get(name)
    if oid is None:
        print(f"  WARN: unknown type {name!r}", file=sys.stderr)
        return 0
    return oid

def resolve_sortop(spec, op_map, op_by_name, type_map):
    """Resolve aggsortop from operator spec like '>(int8,int8)' or '<(text,text)' or '-'.

    pg_aggregate.dat uses the form 'oprname(lefttype,righttype)' for aggsortop.
    """
    if spec == '-' or not spec:
        return 0
    # Try 'oprname(lefttype,righttype)' form first
    m = re.match(r'^([^(]+)\((\w+),(\w+)\)$', spec)
    if m:
        oprname = m.group(1).strip()
        left_name = m.group(2).strip()
        right_name = m.group(3).strip()
        left_oid = type_map.get(left_name, 0)
        right_oid = type_map.get(right_name, 0)
        key = (oprname, left_oid, right_oid)
        if key in op_map:
            return op_map[key]
        print(f"  WARN: operator not found {spec!r} -> key={key}", file=sys.stderr)
        # Fallback: search by name only
        candidates = op_by_name.get(oprname, [])
        if candidates:
            print(f"    -> candidates: {candidates[:3]}", file=sys.stderr)
        return 0
    # Plain operator name (no type args)
    candidates = op_by_name.get(spec, [])
    if not candidates:
        print(f"  WARN: unknown operator {spec!r}", file=sys.stderr)
        return 0
    if len(candidates) == 1:
        return candidates[0][0]
    print(f"  WARN: ambiguous operator {spec!r}: {candidates}", file=sys.stderr)
    return candidates[0][0]

# ---------------------------------------------------------------------------
# Build OID assignment (aggregates don't have oid in pg_aggregate.dat,
# they are assigned sequentially by genbki.pl starting from 10000+)
# genbki.pl assigns OIDs to aggregates based on scan of pg_proc for the
# aggfnoid row — the aggregate OID IS the aggfnoid OID (the pg_proc row).
# So we use the aggfnoid OID as the row OID.
# ---------------------------------------------------------------------------

def main():
    pg_catalog = PG_CATALOG

    print("Parsing catalog files...", file=sys.stderr)

    type_rows = parse_dat(os.path.join(pg_catalog, 'pg_type.dat'))
    proc_rows = parse_dat(os.path.join(pg_catalog, 'pg_proc.dat'))
    op_rows = parse_dat(os.path.join(pg_catalog, 'pg_operator.dat'))
    agg_rows = parse_dat(os.path.join(pg_catalog, 'pg_aggregate.dat'))

    print(f"types={len(type_rows)}, procs={len(proc_rows)}, ops={len(op_rows)}, aggs={len(agg_rows)}", file=sys.stderr)

    type_map = build_type_map(type_rows)
    proc_by_sig, proc_by_name = build_proc_map(proc_rows, type_map)
    op_map, op_by_name = build_operator_map(op_rows, type_map)

    # ---------------------------------------------------------------------------
    # Resolve each pg_aggregate row
    # ---------------------------------------------------------------------------

    rows = []
    errors = []

    for i, agg in enumerate(agg_rows):
        aggfnoid_spec = agg.get('aggfnoid', '')

        # Resolve aggfnoid — this is the primary key, also the OID of the agg
        aggfnoid = resolve_func_with_args(aggfnoid_spec, proc_by_sig, proc_by_name, type_map)
        if aggfnoid == 0:
            errors.append(f"row {i}: cannot resolve aggfnoid={aggfnoid_spec!r}")
            continue

        aggkind = agg.get('aggkind', 'n')
        aggnumdirectargs = int(agg.get('aggnumdirectargs', '0'))

        aggtransfn_oid = resolve_func_with_args(agg.get('aggtransfn', '-'), proc_by_sig, proc_by_name, type_map)
        aggfinalfn_oid = resolve_func_with_args(agg.get('aggfinalfn', '-'), proc_by_sig, proc_by_name, type_map)
        aggcombinefn_oid = resolve_func_with_args(agg.get('aggcombinefn', '-'), proc_by_sig, proc_by_name, type_map)
        aggserialfn_oid = resolve_func_with_args(agg.get('aggserialfn', '-'), proc_by_sig, proc_by_name, type_map)
        aggdeserialfn_oid = resolve_func_with_args(agg.get('aggdeserialfn', '-'), proc_by_sig, proc_by_name, type_map)
        aggmtransfn_oid = resolve_func_with_args(agg.get('aggmtransfn', '-'), proc_by_sig, proc_by_name, type_map)
        aggminvtransfn_oid = resolve_func_with_args(agg.get('aggminvtransfn', '-'), proc_by_sig, proc_by_name, type_map)
        aggmfinalfn_oid = resolve_func_with_args(agg.get('aggmfinalfn', '-'), proc_by_sig, proc_by_name, type_map)

        aggfinalextra_str = agg.get('aggfinalextra', 'f')
        aggfinalextra = aggfinalextra_str == 't'
        aggmfinalextra_str = agg.get('aggmfinalextra', 'f')
        aggmfinalextra = aggmfinalextra_str == 't'

        aggfinalmodify = agg.get('aggfinalmodify', 'r')
        aggmfinalmodify = agg.get('aggmfinalmodify', 'r')

        # aggtranstype
        transtype_name = agg.get('aggtranstype', '-')
        aggtranstype_oid = resolve_type(transtype_name, type_map)
        if aggtranstype_oid == 0 and transtype_name != '-':
            errors.append(f"row {i} ({aggfnoid_spec}): cannot resolve aggtranstype={transtype_name!r}")

        aggtransspace = int(agg.get('aggtransspace', '0'))

        # aggmtranstype
        mtranstype_name = agg.get('aggmtranstype', '-')
        aggmtranstype_oid = resolve_type(mtranstype_name, type_map)
        aggmtransspace = int(agg.get('aggmtransspace', '0'))

        # aggsortop — sort operator e.g. '>(int8,int8)'
        sortop_spec = agg.get('aggsortop', '-')
        if sortop_spec != '-' and sortop_spec:
            aggsortop_oid = resolve_sortop(sortop_spec, op_map, op_by_name, type_map)
        else:
            aggsortop_oid = 0

        # agginitval, aggminitval — text fields (nullable)
        agginitval = agg.get('agginitval', '')
        aggminitval = agg.get('aggminitval', '')
        # _null_ means NULL
        if agginitval == '_null_':
            agginitval = ''
        if aggminitval == '_null_':
            aggminitval = ''

        rows.append({
            'aggfnoid': aggfnoid,
            'aggkind': aggkind,
            'aggnumdirectargs': aggnumdirectargs,
            'aggtransfn': aggtransfn_oid,
            'aggfinalfn': aggfinalfn_oid,
            'aggcombinefn': aggcombinefn_oid,
            'aggserialfn': aggserialfn_oid,
            'aggdeserialfn': aggdeserialfn_oid,
            'aggmtransfn': aggmtransfn_oid,
            'aggminvtransfn': aggminvtransfn_oid,
            'aggmfinalfn': aggmfinalfn_oid,
            'aggfinalextra': aggfinalextra,
            'aggmfinalextra': aggmfinalextra,
            'aggfinalmodify': aggfinalmodify,
            'aggmfinalmodify': aggmfinalmodify,
            'aggsortop': aggsortop_oid,
            'aggtranstype': aggtranstype_oid,
            'aggtransspace': aggtransspace,
            'aggmtranstype': aggmtranstype_oid,
            'aggmtransspace': aggmtransspace,
            'agginitval': agginitval,
            'aggminitval': aggminitval,
            '_spec': aggfnoid_spec,
        })

    if errors:
        print("ERRORS:", file=sys.stderr)
        for e in errors:
            print(f"  {e}", file=sys.stderr)

    print(f"Resolved {len(rows)} rows", file=sys.stderr)

    # ---------------------------------------------------------------------------
    # Generate Go code
    # ---------------------------------------------------------------------------

    def go_bool(b):
        return 'true' if b else 'false'

    def go_char(c):
        return f"'{c}'"

    def go_str(s):
        # Escape Go string
        return s.replace('\\', '\\\\').replace('"', '\\"')

    lines = []
    lines.append("// Code generated by .ralph/tmp_gen_pg_aggregate.py. DO NOT EDIT.")
    lines.append("")
    lines.append("package initdb")
    lines.append("")
    lines.append("import (")
    lines.append('\t"fmt"')
    lines.append("")
    lines.append('\t"github.com/goopg/goopg/internal/catalog"')
    lines.append('\t"github.com/goopg/goopg/internal/executor"')
    lines.append(")")
    lines.append("")
    lines.append("// pgAggregateEntry mirrors one row of PG18's pg_aggregate (OID 2600).")
    lines.append("// The first 20 columns are fixed-size; agginitval and aggminitval are")
    lines.append("// variable-length text (nullable).")
    lines.append("type pgAggregateEntry struct {")
    lines.append("\tAggFnOID        uint32 // aggfnoid (regproc) — also the row OID")
    lines.append("\tAggKind         byte   // aggkind: 'n'=normal, 'o'=ordered-set, 'h'=hypothetical")
    lines.append("\tAggNumDirectArgs int16  // aggnumdirectargs (direct args for ordered-set)")
    lines.append("\tAggTransFn      uint32 // aggtransfn")
    lines.append("\tAggFinalFn      uint32 // aggfinalfn (0 if none)")
    lines.append("\tAggCombineFn    uint32 // aggcombinefn (0 if none)")
    lines.append("\tAggSerialFn     uint32 // aggserialfn (0 if none)")
    lines.append("\tAggDeserialFn   uint32 // aggdeserialfn (0 if none)")
    lines.append("\tAggMTransFn     uint32 // aggmtransfn (0 if none)")
    lines.append("\tAggMInvTransFn  uint32 // aggminvtransfn (0 if none)")
    lines.append("\tAggMFinalFn     uint32 // aggmfinalfn (0 if none)")
    lines.append("\tAggFinalExtra   bool   // aggfinalextra")
    lines.append("\tAggMFinalExtra  bool   // aggmfinalextra")
    lines.append("\tAggFinalModify  byte   // aggfinalmodify: 'r','s','w'")
    lines.append("\tAggMFinalModify byte   // aggmfinalmodify: 'r','s','w'")
    lines.append("\tAggSortOp       uint32 // aggsortop (0 if none)")
    lines.append("\tAggTransType    uint32 // aggtranstype")
    lines.append("\tAggTransSpace   int32  // aggtransspace")
    lines.append("\tAggMTransType   uint32 // aggmtranstype (0 if none)")
    lines.append("\tAggMTransSpace  int32  // aggmtransspace")
    lines.append('\tAggInitVal      string // agginitval (empty = NULL)')
    lines.append('\tAggMInitVal     string // aggminitval (empty = NULL)')
    lines.append("}")
    lines.append("")
    lines.append("// pgAggregateColDefs returns the 22-column PG18 pg_aggregate schema.")
    lines.append("// Columns 1-20 are fixed-size; 21-22 (agginitval, aggminitval) are nullable text.")
    lines.append("// Column order matches FormData_pg_aggregate so PG's GETSTRUCT cast works.")
    lines.append("func pgAggregateColDefs() []catalog.Column {")
    lines.append("\treturn []catalog.Column{")
    lines.append('\t\t{Name: "aggfnoid",        Type: catalog.Type{Name: "regproc"}}, // 1  regproc (=oid 24)')
    lines.append('\t\t{Name: "aggkind",          Type: catalog.Type{Name: "char"}},   // 2')
    lines.append('\t\t{Name: "aggnumdirectargs", Type: catalog.Type{Name: "int2"}},   // 3')
    lines.append('\t\t{Name: "aggtransfn",       Type: catalog.Type{Name: "regproc"}}, // 4')
    lines.append('\t\t{Name: "aggfinalfn",       Type: catalog.Type{Name: "regproc"}}, // 5')
    lines.append('\t\t{Name: "aggcombinefn",     Type: catalog.Type{Name: "regproc"}}, // 6')
    lines.append('\t\t{Name: "aggserialfn",      Type: catalog.Type{Name: "regproc"}}, // 7')
    lines.append('\t\t{Name: "aggdeserialfn",    Type: catalog.Type{Name: "regproc"}}, // 8')
    lines.append('\t\t{Name: "aggmtransfn",      Type: catalog.Type{Name: "regproc"}}, // 9')
    lines.append('\t\t{Name: "aggminvtransfn",   Type: catalog.Type{Name: "regproc"}}, // 10')
    lines.append('\t\t{Name: "aggmfinalfn",      Type: catalog.Type{Name: "regproc"}}, // 11')
    lines.append('\t\t{Name: "aggfinalextra",    Type: catalog.Type{Name: "bool"}},   // 12')
    lines.append('\t\t{Name: "aggmfinalextra",   Type: catalog.Type{Name: "bool"}},   // 13')
    lines.append('\t\t{Name: "aggfinalmodify",   Type: catalog.Type{Name: "char"}},   // 14')
    lines.append('\t\t{Name: "aggmfinalmodify",  Type: catalog.Type{Name: "char"}},   // 15')
    lines.append('\t\t{Name: "aggsortop",        Type: catalog.Type{Name: "oid"}},    // 16')
    lines.append('\t\t{Name: "aggtranstype",     Type: catalog.Type{Name: "oid"}},    // 17')
    lines.append('\t\t{Name: "aggtransspace",    Type: catalog.Type{Name: "int4"}},   // 18')
    lines.append('\t\t{Name: "aggmtranstype",    Type: catalog.Type{Name: "oid"}},    // 19')
    lines.append('\t\t{Name: "aggmtransspace",   Type: catalog.Type{Name: "int4"}},   // 20')
    lines.append('\t\t{Name: "agginitval",       Type: catalog.Type{Name: "text"}},   // 21 nullable')
    lines.append('\t\t{Name: "aggminitval",      Type: catalog.Type{Name: "text"}},   // 22 nullable')
    lines.append("\t}")
    lines.append("}")
    lines.append("")
    lines.append("// aggNullableText returns NullDatum for an empty string (BKI _null_),")
    lines.append("// or a text Datum for a non-empty agginitval / aggminitval value.")
    lines.append("func aggNullableText(s string) executor.Datum {")
    lines.append('\tif s == "" {')
    lines.append("\t\treturn executor.NullDatum")
    lines.append("\t}")
    lines.append("\treturn executor.NewStringDatum(s)")
    lines.append("}")
    lines.append("")
    lines.append("// pgAggregateRow encodes one pg_aggregate row as a 22-column executor.Row.")
    lines.append("// pg_aggregate has no separate oid column — aggfnoid (column 1) is the")
    lines.append("// primary key. The row order matches FormData_pg_aggregate exactly.")
    lines.append("func pgAggregateRow(e pgAggregateEntry) executor.Row {")
    lines.append("\treturn executor.Row{")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggFnOID)),                          // 1  aggfnoid")
    lines.append('\t\texecutor.NewStringDatum(string([]byte{e.AggKind})),               // 2  aggkind')
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggNumDirectArgs)),                  // 3  aggnumdirectargs")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggTransFn)),                        // 4  aggtransfn")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggFinalFn)),                        // 5  aggfinalfn")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggCombineFn)),                      // 6  aggcombinefn")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggSerialFn)),                       // 7  aggserialfn")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggDeserialFn)),                     // 8  aggdeserialfn")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggMTransFn)),                       // 9  aggmtransfn")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggMInvTransFn)),                    // 10 aggminvtransfn")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggMFinalFn)),                       // 11 aggmfinalfn")
    lines.append("\t\texecutor.NewBoolDatum(e.AggFinalExtra),                           // 12 aggfinalextra")
    lines.append("\t\texecutor.NewBoolDatum(e.AggMFinalExtra),                          // 13 aggmfinalextra")
    lines.append('\t\texecutor.NewStringDatum(string([]byte{e.AggFinalModify})),         // 14 aggfinalmodify')
    lines.append('\t\texecutor.NewStringDatum(string([]byte{e.AggMFinalModify})),        // 15 aggmfinalmodify')
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggSortOp)),                         // 16 aggsortop")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggTransType)),                      // 17 aggtranstype")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggTransSpace)),                     // 18 aggtransspace")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggMTransType)),                     // 19 aggmtranstype")
    lines.append("\t\texecutor.NewIntDatum(int64(e.AggMTransSpace)),                    // 20 aggmtransspace")
    lines.append('\t\taggNullableText(e.AggInitVal),                                    // 21 agginitval (nullable)')
    lines.append('\t\taggNullableText(e.AggMInitVal),                                   // 22 aggminitval (nullable)')
    lines.append("\t}")
    lines.append("}")
    lines.append("")
    lines.append("// pgAggregateInitialEntries returns all 161 BKI rows for pg_aggregate")
    lines.append("// (OID 2600), sourced from PG18's pg_aggregate.dat.")
    lines.append("// aggfnoid is the proc OID of the aggregate function (= the row key).")
    lines.append("// All proc/type/operator OIDs are resolved from the respective .dat files.")
    lines.append("func pgAggregateInitialEntries() []pgAggregateEntry {")
    lines.append("\treturn []pgAggregateEntry{")

    for r in rows:
        spec = r['_spec']
        # Format the entry
        kind_char = r['aggkind']
        fmod = r['aggfinalmodify']
        mfmod = r['aggmfinalmodify']

        init_val = r['agginitval']
        minit_val = r['aggminitval']

        lines.append(f"\t\t// {spec}")
        lines.append(f"\t\t{{")
        lines.append(f"\t\t\tAggFnOID: {r['aggfnoid']}, AggKind: '{kind_char}', AggNumDirectArgs: {r['aggnumdirectargs']},")
        lines.append(f"\t\t\tAggTransFn: {r['aggtransfn']}, AggFinalFn: {r['aggfinalfn']}, AggCombineFn: {r['aggcombinefn']},")
        lines.append(f"\t\t\tAggSerialFn: {r['aggserialfn']}, AggDeserialFn: {r['aggdeserialfn']},")
        lines.append(f"\t\t\tAggMTransFn: {r['aggmtransfn']}, AggMInvTransFn: {r['aggminvtransfn']}, AggMFinalFn: {r['aggmfinalfn']},")
        lines.append(f"\t\t\tAggFinalExtra: {go_bool(r['aggfinalextra'])}, AggMFinalExtra: {go_bool(r['aggmfinalextra'])},")
        lines.append(f"\t\t\tAggFinalModify: '{fmod}', AggMFinalModify: '{mfmod}',")
        lines.append(f"\t\t\tAggSortOp: {r['aggsortop']}, AggTransType: {r['aggtranstype']},")
        lines.append(f"\t\t\tAggTransSpace: {r['aggtransspace']}, AggMTransType: {r['aggmtranstype']}, AggMTransSpace: {r['aggmtransspace']},")
        if init_val:
            lines.append(f'\t\t\tAggInitVal: "{go_str(init_val)}",')
        if minit_val:
            lines.append(f'\t\t\tAggMInitVal: "{go_str(minit_val)}",')
        lines.append(f"\t\t}},")

    lines.append("\t}")
    lines.append("}")
    lines.append("")
    lines.append("// bootstrapPgAggregateTuples writes all 161 pg_aggregate rows to")
    lines.append("// base/{1,5}/2600. The aggfnoid column doubles as the row's logical key")
    lines.append("// (pg_aggregate has no separate 'oid' column — the PKEY is aggfnoid).")
    lines.append("// Returns a map keyed by aggfnoid OID for index seeding.")
    lines.append("func bootstrapPgAggregateTuples(dataDir string) (map[uint32]heapTID, error) {")
    lines.append("\tcols := pgAggregateColDefs()")
    lines.append("\tentries := pgAggregateInitialEntries()")
    lines.append("\trows := make([]executor.Row, len(entries))")
    lines.append("\tfor i, e := range entries {")
    lines.append("\t\trows[i] = pgAggregateRow(e)")
    lines.append("\t}")
    lines.append('\trawTIDs, err := writeMultiPageHeapRows(dataDir, "2600", cols, rows)')
    lines.append("\tif err != nil {")
    lines.append('\t\treturn nil, fmt.Errorf("bootstrapPgAggregateTuples: %w", err)')
    lines.append("\t}")
    lines.append("\ttidMap := make(map[uint32]heapTID, len(entries))")
    lines.append("\tfor i, e := range entries {")
    lines.append("\t\ttidMap[e.AggFnOID] = rawTIDs[i]")
    lines.append("\t}")
    lines.append("\treturn tidMap, nil")
    lines.append("}")
    lines.append("")

    print('\n'.join(lines))

if __name__ == '__main__':
    main()
