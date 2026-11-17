#!/usr/bin/env python3
"""Generate internal/catalog/pg_catalog_seed_rows_generated.go
(M0149-0001h1a20): the built-in rows a PostgreSQL 18.3 initdb holds in
pg_cast, pg_opfamily, pg_opclass, pg_amop, pg_amproc, pg_conversion and pg_range
(h1a22), rendered as each
virtual view's text cells in its column order (catalog.go). SQL NULL is
catalog.VirtualNull, booleans 't' / 'f', regproc columns numeric OIDs.
Also format_type(oid, NULL) for every pg_catalog type (M0149-0001h1a21).
Captured from a throwaway initdb whose superuser is "postgres".

Usage: scripts/gen-pg-catalog-seed-rows.py | gofmt > internal/catalog/pg_catalog_seed_rows_generated.go
"""
import json
import os
import shutil
import subprocess
import tempfile

# (Go var, catalog, [columns in the view's order]); bool / regproc columns
# are rendered by the per-column expressions below.
TABLES = [
    ("seedPgCastRows", "pg_cast", ["oid", "castsource", "casttarget", "castfunc", "castcontext", "castmethod"]),
    ("seedPgOpfamilyRows", "pg_opfamily", ["oid", "opfmethod", "opfname", "opfnamespace", "opfowner"]),
    ("seedPgOpclassRows", "pg_opclass", ["oid", "opcmethod", "opcname", "opcnamespace", "opcowner", "opcfamily",
                                         "opcintype", "opcdefault", "opckeytype"]),
    ("seedPgAmopRows", "pg_amop", ["oid", "amopfamily", "amoplefttype", "amoprighttype", "amopstrategy",
                                   "amoppurpose", "amopopr", "amopmethod", "amopsortfamily"]),
    ("seedPgAmprocRows", "pg_amproc", ["oid", "amprocfamily", "amproclefttype", "amprocrighttype", "amprocnum",
                                       "amproc"]),
    ("seedPgConversionRows", "pg_conversion", ["oid", "conname", "connamespace", "conowner", "conforencoding",
                                               "contoencoding", "conproc", "condefault"]),
    ("seedPgRangeRows", "pg_range", ["rngtypid", "rngsubtype", "rngmultitypid", "rngcollation", "rngsubopc",
                                     "rngcanonical", "rngsubdiff"]),
]
# Key column per table (pg_range has no oid); default "oid".
KEY = {"pg_range": "rngtypid"}
SPECIAL = {"opcdefault": "case when opcdefault then 't' else 'f' end", "amproc": "amproc::oid::int8::text",
           "conproc": "conproc::oid::int8::text", "condefault": "case when condefault then 't' else 'f' end",
           "rngcanonical": "rngcanonical::oid::int8::text", "rngsubdiff": "rngsubdiff::oid::int8::text"}

root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
bindir = os.path.join(root, "postgres", "local_install", "bin")
env = dict(os.environ, LD_LIBRARY_PATH=os.path.join(root, "postgres", "local_install", "lib"))
port = os.environ.get("GEN_PORT", "5547")
tmp = tempfile.mkdtemp()
data = os.path.join(tmp, "data")
results = {}
try:
    subprocess.run([os.path.join(bindir, "initdb"), "-D", data, "-U", "postgres", "--no-sync", "-E", "UTF8", "--locale=C"],
                   env=env, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run([os.path.join(bindir, "pg_ctl"), "-D", data, "-l", os.path.join(tmp, "log"), "-w",
                    "-o", "-p %s -c listen_addresses=127.0.0.1 -c unix_socket_directories=%s" % (port, tmp), "start"],
                   env=env, check=True, stdout=subprocess.DEVNULL)
    for var, table, cols in TABLES:
        sel = ", ".join(SPECIAL.get(c, c + "::text") for c in cols)
        key = KEY.get(table, "oid")
        q = "select coalesce(json_agg(json_build_array(%s) order by %s), '[]') from %s where %s < 16384" % (
            sel, key, table, key)
        out = subprocess.run([os.path.join(bindir, "psql"), "-X", "-h", "127.0.0.1", "-p", port, "-U", "postgres",
                              "-d", "postgres", "-At", "-c", q], env=env, check=True, capture_output=True,
                             text=True).stdout
        results[var] = (table, cols, json.loads(out))
    # format_type(oid, NULL) — what regtypeout / format_type_be print — for
    # every pg_catalog type (always visible, so never schema-qualified).
    q = ("select coalesce(json_agg(json_build_array(oid::int8, format_type(oid, NULL)) order by oid), '[]') "
         "from pg_type where oid < 16384 and typnamespace = 11")
    out = subprocess.run([os.path.join(bindir, "psql"), "-X", "-h", "127.0.0.1", "-p", port, "-U", "postgres",
                          "-d", "postgres", "-At", "-c", q], env=env, check=True, capture_output=True,
                         text=True).stdout
    format_names = json.loads(out)
    # pg_get_function_result / _arguments / _identity_arguments for every
    # built-in function (h1a23): psql's \df and pg_dump read them.
    q = ("select coalesce(json_agg(json_build_array(oid::int8, pg_get_function_result(oid), "
         "pg_get_function_arguments(oid), pg_get_function_identity_arguments(oid)) order by oid), '[]') "
         "from pg_proc where oid < 16384")
    out = subprocess.run([os.path.join(bindir, "psql"), "-X", "-h", "127.0.0.1", "-p", port, "-U", "postgres",
                          "-d", "postgres", "-At", "-c", q], env=env, check=True, capture_output=True,
                         text=True).stdout
    func_sigs = json.loads(out)
    # pg_get_functiondef for every non-aggregate built-in (psql \sf), and the
    # internal-function symbol set fmgr_internal_function resolves against
    # (h1a24).
    q = ("select coalesce(json_agg(json_build_array(oid::int8, pg_get_functiondef(oid)) order by oid), '[]') "
         "from pg_proc where oid < 16384 and prokind <> 'a'")
    out = subprocess.run([os.path.join(bindir, "psql"), "-X", "-h", "127.0.0.1", "-p", port, "-U", "postgres",
                          "-d", "postgres", "-At", "-c", q], env=env, check=True, capture_output=True,
                         text=True).stdout
    func_defs = json.loads(out)
    q = ("select coalesce(json_agg(distinct prosrc order by prosrc), '[]') from pg_proc "
         "where oid < 16384 and prolang = 12")
    out = subprocess.run([os.path.join(bindir, "psql"), "-X", "-h", "127.0.0.1", "-p", port, "-U", "postgres",
                          "-d", "postgres", "-At", "-c", q], env=env, check=True, capture_output=True,
                         text=True).stdout
    internal_syms = json.loads(out)
finally:
    subprocess.run([os.path.join(bindir, "pg_ctl"), "-D", data, "-m", "immediate", "stop"], env=env,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    shutil.rmtree(tmp, ignore_errors=True)
cell = lambda v: "VirtualNull" if v is None else json.dumps(v, ensure_ascii=False)
lines = ["// Code generated by scripts/gen-pg-catalog-seed-rows.py from a PostgreSQL 18.3 initdb; DO NOT EDIT.\n\n",
         "package catalog\n"]
for var, table, cols in TABLES:
    _, _, rows = results[var]
    lines.append("\n// %s are the %d built-in %s rows (%s).\n" % (var, len(rows), table, ", ".join(cols)))
    lines.append("var %s = [][]string{\n" % var)
    for r in rows:
        lines.append("\t{" + ", ".join(cell(v) for v in r) + "},\n")
    lines.append("}\n")
lines.append("\n// builtinFormatTypeNames is format_type(oid, NULL) for the %d pg_catalog types.\n" % len(format_names))
lines.append("var builtinFormatTypeNames = map[uint32]string{\n")
for oid, name in format_names:
    lines.append("\t%d: %s,\n" % (oid, json.dumps(name)))
lines.append("}\n")
lines.append("\n// builtinFunctionSignatures is pg_get_function_result / _arguments /\n")
lines.append("// _identity_arguments for the %d built-in functions; HasResult is false when\n" % len(func_sigs))
lines.append("// pg_get_function_result is NULL (procedures).\n")
lines.append("var builtinFunctionSignatures = map[uint32]BuiltinFunctionSignature{\n")
for oid, res, args, ident in func_sigs:
    lines.append("\t%d: {Result: %s, HasResult: %s, Arguments: %s, Identity: %s},\n" % (
        oid, json.dumps(res or "", ensure_ascii=False), "false" if res is None else "true",
        json.dumps(args or "", ensure_ascii=False), json.dumps(ident or "", ensure_ascii=False)))
lines.append("}\n")
lines.append("\n// builtinFunctionDefs is pg_get_functiondef for the %d non-aggregate built-ins.\n" % len(func_defs))
lines.append("var builtinFunctionDefs = map[uint32]string{\n")
for oid, d in func_defs:
    lines.append("\t%d: %s,\n" % (oid, json.dumps(d, ensure_ascii=False)))
lines.append("}\n")
lines.append("\n// builtinInternalFunctionSymbols are the %d prosrc names of LANGUAGE internal\n" % len(internal_syms))
lines.append("// built-ins: what fmgr_internal_function resolves an AS clause against.\n")
lines.append("var builtinInternalFunctionSymbols = map[string]bool{\n")
for n in internal_syms:
    lines.append("\t%s: true,\n" % json.dumps(n))
lines.append("}\n")
print("".join(lines), end="")
