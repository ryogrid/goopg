#!/usr/bin/env bash
# census.sh <sf025|sf1> : clone the shipped corpus, start it on a private port, dump the pg_class census for every
# public table and index, and the same from :65438 (SELECT only); print the diff.
set -u
REPO=/home/ryo/work/goopg/goopg; source "$REPO/bench/tpcds/env_tpcds.sh"; export PATH="$REPO/postgres/local_install/bin:$PATH"
SF=$1; OUT=$REPO/analysis/m0146/m0146-0216/closure
if [[ $SF == sf025 ]]; then SRC=$SF025_GOOPG_DATA; PORT=5599; PGDB=tpcds025; else SRC=$TPCDS_PGDATA; PORT=5598; PGDB=tpcds; fi
C=$REPO/tmp/m0216c/data-$SF; BIN=$REPO/tmp/m0216c/goopg; UNIT=goopg-m0216c-$SF
[[ -f "$SRC.HOLD" || -f "$SRC/postmaster.pid" ]] && { echo "FATAL src busy"; exit 3; }
rm -rf "$C"; cp -a "$SRC" "$C"; rm -f "$C/postmaster.pid"
systemctl --user reset-failed "$UNIT.scope" >/dev/null 2>&1
GOOPG_CG_UNIT=$UNIT setsid nohup "$REPO/scripts/goopg-test-run.sh" "$BIN" start -D "$C" --listen 127.0.0.1:$PORT --hba "$C/pg_hba.conf" > "$REPO/tmp/m0216c/server-$SF.log" 2>&1 < /dev/null &
for _ in $(seq 1 300); do pg_isready -h 127.0.0.1 -p $PORT -U postgres -q && break; sleep 1; done
Q="select c.relname, c.relkind, c.relpages, c.relallvisible, c.reltuples::bigint, pg_relation_size(c.oid)/8192 from pg_class c join pg_namespace n on n.oid = c.relnamespace where n.nspname = 'public' and c.relkind in ('r','i') order by 1"
psql -X -At -h 127.0.0.1 -p $PORT -U postgres -d postgres -c "$Q" > "$OUT/goopg-$SF-pg_class.txt"
psql -X -At -h 127.0.0.1 -p 65438 -U ryo -d $PGDB -c "$Q" > "$OUT/pg-$SF-pg_class.txt"
[[ -n "${KEEP:-}" ]] || { "$BIN" stop -D "$C" >/dev/null 2>&1; rm -rf "$C"; }
python3 - "$OUT/goopg-$SF-pg_class.txt" "$OUT/pg-$SF-pg_class.txt" "$SF" <<'PY'
import sys
def load(p): return {l.split('|')[0]: l.strip().split('|') for l in open(p) if l.strip()}
g, p, sf = load(sys.argv[1]), load(sys.argv[2]), sys.argv[3]
print(f"== {sf}: goopg {len(g)} rels, PG {len(p)} rels")
only_g = sorted(set(g) - set(p)); only_p = sorted(set(p) - set(g))
if only_g: print("  only in goopg:", " ".join(only_g))
if only_p: print("  only in PG:", " ".join(only_p))
fields = [(2, 'relpages'), (3, 'allvis'), (4, 'reltuples'), (5, 'phys')]
nd = 0
for k in sorted(set(g) & set(p)):
    diffs = [f"{n} {g[k][i]}/{p[k][i]}" for i, n in fields if g[k][i] != p[k][i]]
    if diffs:
        nd += 1
        print(f"  {k:32s} {g[k][1]} " + "; ".join(diffs))
print(f"  -> {nd} of {len(set(g) & set(p))} common relations differ")
PY
