#!/usr/bin/env bash
# Start a private goopg server and PROVE the listener is the binary we built.
# The R0->R1 contamination: pg_isready answered from a SURVIVING older server,
# so launch.sh reported READY while its own instance never bound the port and
# the arm silently measured the previous binary.
# usage: launch-verified.sh <bin> <datadir> <port> <log> <scope>
set -uo pipefail
BIN="$1"; DATA="$2"; PORT="$3"; LOG="$4"; SCOPE="$5"
export PATH="/home/ryo/work/goopg/goopg/postgres/local_install/bin:${PATH}"
# 1. Stop anything holding this datadir, then prove the port is free.
"$BIN" stop -D "$DATA" >/dev/null 2>&1
for i in $(seq 1 30); do
    pg_isready -h 127.0.0.1 -p "$PORT" -U postgres >/dev/null 2>&1 || break
    sleep 1
done
if pg_isready -h 127.0.0.1 -p "$PORT" -U postgres >/dev/null 2>&1; then
    echo "FATAL: :${PORT} still answering before start — a foreign server holds it" >&2; exit 2
fi
/tmp/parity-r0/launch.sh "$BIN" "$DATA" "$PORT" "$LOG" "$SCOPE" || exit 1
# 2. Prove the process listening on PORT runs OUR binary inode.
want=$(stat -c %i "$BIN")
pid=$(ss -lptnH "sport = :${PORT}" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1)
[ -n "$pid" ] || { echo "FATAL: no listener pid found for :${PORT}" >&2; exit 3; }
got=$(stat -Lc %i "/proc/${pid}/exe" 2>/dev/null)
if [ "$want" != "$got" ]; then
    echo "FATAL: :${PORT} pid ${pid} runs $(readlink -f /proc/${pid}/exe), not ${BIN}" >&2; exit 4
fi
echo "VERIFIED :${PORT} pid=${pid} exe=$(readlink -f /proc/${pid}/exe)"
