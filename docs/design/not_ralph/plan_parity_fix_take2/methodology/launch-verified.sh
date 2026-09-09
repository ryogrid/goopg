#!/usr/bin/env bash
# My lane launcher (pp2): like r1 launch-verified's inner script but with
# caller-chosen GC/mem (execution workloads need GOGC=100; EXPLAIN-only
# arms use defaults). Backgrounding (&) is INSIDE this file; the script
# waits for readiness in the foreground. Never touches foreign dirs/ports.
# usage: launch.sh <bin> <datadir> <port> <log> <scope> [gogc] [gomemlimit]
set -euo pipefail
BIN="$1"; DATA="$2"; PORT="$3"; LOG="$4"; SCOPE="$5"
GOGC="${6:-off}"; GOMEMLIMIT="${7:-12GiB}"
export PATH="/home/ryo/work/goopg/goopg/postgres/local_install/bin:${PATH}"
export LD_LIBRARY_PATH="/home/ryo/work/goopg/goopg/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
export GOOPG_ANALYZE_SEED=20260905 GOMEMLIMIT GOGC GOOPG_CG_UNIT="${SCOPE}"
"$BIN" stop -D "$DATA" >/dev/null 2>&1 || true
rm -f "${DATA}/postmaster.pid"
/home/ryo/work/goopg/goopg/scripts/goopg-test-run.sh \
    "${BIN}" start -D "${DATA}" \
    --listen "127.0.0.1:${PORT}" --hba "${DATA}/pg_hba.conf" \
    >>"${LOG}" 2>&1 &
for i in $(seq 1 90); do
    if pg_isready -h 127.0.0.1 -p "${PORT}" -U postgres >/dev/null 2>&1; then
        pid=$(ss -lptnH "sport = :${PORT}" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1)
        got=$(stat -Lc %i "/proc/${pid}/exe" 2>/dev/null)
        want=$(stat -c %i "$BIN")
        if [ -n "$pid" ] && [ "$got" = "$want" ]; then
            echo "READY+VERIFIED :${PORT} pid=${pid}"
            exit 0
        fi
        echo "FATAL: :${PORT} held by foreign exe" >&2; exit 3
    fi
    sleep 2
done
echo "FATAL: :${PORT} not ready" >&2; exit 1
