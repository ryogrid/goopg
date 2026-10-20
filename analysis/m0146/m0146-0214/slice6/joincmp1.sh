#!/usr/bin/env bash
# joincmp1.sh <eval>: SF1 twin of tmp/m0214s3/joincmp.sh (PG db tpcds on 5547, goopg SF1 clone on 5598).
set -u
R=/home/ryo/work/goopg/goopg; E=$1; P=$R/tmp/pgdbg/inst/bin; O=$R/tmp/m0231
$P/pg_ctl -D $R/tmp/m21-refclone stop -m fast >/dev/null 2>&1
GEQO_TRACE_EVAL=$E $P/pg_ctl -D $R/tmp/m21-refclone -l $O/pg1.log -o "-p 5547 -c autovacuum=off -c listen_addresses=127.0.0.1 -c unix_socket_directories=/tmp" start >/dev/null 2>&1; sleep 2
L=$(wc -l < $O/pg1.log); $P/psql -X -h 127.0.0.1 -p 5547 -d tpcds -f $R/tmp/m0214s3/q64.sql >/dev/null 2>&1
tail -n +$L $O/pg1.log | grep -a 'GEQO' | sed 's/^.*LOG: *//' | grep -v '^GEQOEVAL\|STATEMENT' > $O/pg-trace$E.txt
grep -a '^GEQOJOIN ' $O/pg-trace$E.txt | sed 's/type=[0-9]* //' > $O/pg-join$E.txt
GOOPG_GEQO_TRACE_EVAL=$E $O/start1.sh >/dev/null
L=$(wc -l < $O/server1.log); $R/postgres/local_install/bin/psql -X -h 127.0.0.1 -p 5598 -U postgres -d postgres -f $R/tmp/m0214s3/q64.sql >/dev/null 2>&1
tail -n +$L $O/server1.log | grep -a '^GEQO' | grep -v '^GEQOEVAL' > $O/go-trace$E.txt
grep -a '^GEQOJOIN ' $O/go-trace$E.txt | sed 's/kind=[0-9]* //' > $O/go-join$E.txt
python3 - $O/pg-join$E.txt $O/go-join$E.txt <<'PY'
import re,sys
def rd(f): return [dict(re.findall(r'(\w+)=(\([^)]*\)|\S+)', l)) for l in open(f)]
P=rd(sys.argv[1]); G=rd(sys.argv[2])
for i,(p,g) in enumerate(zip(P,G)):
    rel=lambda a,b: abs(float(a)-float(b))/max(abs(float(a)),1)
    if p['relids']!=g['relids'] or p['rows']!=g['rows'] or rel(p['startup'],g['startup'])>1e-7 or rel(p['total'],g['total'])>1e-7:
        print('first differing join',i); print(' PG',p); print(' GO',g)
        if i>0: print(' prev',P[i-1]['relids'],P[i-1]['total'],G[i-1]['total'])
        break
else: print('all',len(P),'joins equal')
PY
