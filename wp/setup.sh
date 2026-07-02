#!/usr/bin/env bash
#
# setup.sh — one-shot, idempotent setup of WordPress (on PostgreSQL via PG4WP)
# backed by a goopg server.
#
# Steps:
#   1. Fetch the PG4WP connector into wp/pg4wp (if absent).
#   2. Build goopg and initialise a dedicated data directory (if absent).
#   3. Start goopg on 0.0.0.0:5544, memory-capped, trusting the Docker subnet.
#   4. Build the WordPress + wp-cli images and start the WordPress container.
#   5. Install WordPress non-interactively in English and seed sample posts.
#
# Re-running is safe: each step is skipped when already done.
set -euo pipefail

# ---- paths & config ---------------------------------------------------------
WP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$WP_DIR/.." && pwd)"
DATA_DIR="$WP_DIR/goopg-data"
HBA_FILE="$WP_DIR/pg_hba.conf"
LOG_FILE="$WP_DIR/goopg-wp.log"
PID_FILE="$DATA_DIR/postmaster.pid"
PW_FILE="$WP_DIR/.wp-admin-password"

GOOPG_BIN="$REPO_ROOT/bin/goopg"
CAP_WRAPPER="$REPO_ROOT/scripts/goopg-test-run.sh"
PG_BIN="$REPO_ROOT/postgres/local_install/bin"
PG_LIB="$REPO_ROOT/postgres/local_install/lib"

LISTEN_HOST="0.0.0.0"
PGPORT_WP="5544"
PGUSER_WP="postgres"
PGDB_WP="postgres"

WP_URL="http://localhost:8080"
WP_TITLE="goopg WordPress Blog"

PG4WP_REPO="https://github.com/PostgreSQL-For-Wordpress/postgresql-for-wordpress.git"

psql_wp() {
    LD_LIBRARY_PATH="$PG_LIB" "$PG_BIN/psql" \
        -h 127.0.0.1 -p "$PGPORT_WP" -U "$PGUSER_WP" -d "$PGDB_WP" "$@"
}

log() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

# ---- 1. PG4WP connector -----------------------------------------------------
if [ ! -f "$WP_DIR/pg4wp/db.php" ]; then
    log "Fetching PG4WP connector into wp/pg4wp"
    tmp="$(mktemp -d)"
    git clone --depth 1 "$PG4WP_REPO" "$tmp/src"
    cp -r "$tmp/src/pg4wp" "$WP_DIR/pg4wp"
    rm -rf "$tmp"
else
    log "PG4WP connector already present (wp/pg4wp)"
fi

# ---- 2. build goopg + init data dir ----------------------------------------
log "Building goopg (make build)"
make -C "$REPO_ROOT" build

if [ ! -f "$DATA_DIR/PG_VERSION" ] && [ ! -f "$DATA_DIR/global/pg_control" ]; then
    log "Initialising goopg data directory: $DATA_DIR"
    "$GOOPG_BIN" init -D "$DATA_DIR" -A trust
else
    log "goopg data directory already initialised"
fi

# ---- 3. start goopg (memory-capped) ----------------------------------------
if psql_wp -tAc 'select 1' >/dev/null 2>&1; then
    log "goopg already running and reachable on 127.0.0.1:$PGPORT_WP"
else
    log "Starting goopg on $LISTEN_HOST:$PGPORT_WP (capped scope: goopg-wp)"
    # Clear any stale scope/pid from a previous run.
    systemctl --user stop goopg-wp.scope 2>/dev/null || true
    systemctl --user reset-failed goopg-wp.scope 2>/dev/null || true
    [ -f "$PID_FILE" ] && "$GOOPG_BIN" stop -D "$DATA_DIR" 2>/dev/null || true

    GOOPG_CG_UNIT=goopg-wp nohup "$CAP_WRAPPER" \
        "$GOOPG_BIN" start -D "$DATA_DIR" \
        --listen "$LISTEN_HOST:$PGPORT_WP" --hba "$HBA_FILE" \
        >"$LOG_FILE" 2>&1 &

    log "Waiting for goopg to accept connections (log: $LOG_FILE)"
    for i in $(seq 1 60); do
        if psql_wp -tAc 'select 1' >/dev/null 2>&1; then
            echo "goopg is up."
            break
        fi
        if [ "$i" = 60 ]; then
            echo "ERROR: goopg did not become ready; last 40 log lines:" >&2
            tail -n 40 "$LOG_FILE" >&2 || true
            exit 1
        fi
        sleep 1
    done
fi

# ---- 3b. record the host IP the containers use to reach goopg --------------
# goopg listens on all host interfaces; a container reaches it via the host's
# primary LAN IP (native Docker Engine in WSL2 has no host.docker.internal).
HOST_IP="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1); exit}}')"
[ -n "${HOST_IP:-}" ] || HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
if [ -z "${HOST_IP:-}" ]; then
    echo "ERROR: could not determine the host IP for the containers to reach goopg." >&2
    exit 1
fi
log "Host IP for goopg access from containers: $HOST_IP"
printf 'GOOPG_DB_HOST=%s:%s\n' "$HOST_IP" "$PGPORT_WP" > "$WP_DIR/.env"

# ---- 4. build images + start WordPress -------------------------------------
log "Building Docker images (wordpress + wp-cli)"
docker compose -f "$WP_DIR/docker-compose.yml" build

log "Starting the WordPress container"
docker compose -f "$WP_DIR/docker-compose.yml" up -d wordpress

log "Waiting for WordPress (Apache) to respond on $WP_URL"
for i in $(seq 1 60); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "$WP_URL/" || true)"
    # Any HTTP response (200/302/500) means Apache+PHP is up; installer redirects.
    if [ "$code" != "000" ] && [ -n "$code" ]; then
        echo "WordPress container responding (HTTP $code)."
        break
    fi
    if [ "$i" = 60 ]; then
        echo "ERROR: WordPress container did not respond." >&2
        docker compose -f "$WP_DIR/docker-compose.yml" logs --tail=40 wordpress >&2 || true
        exit 1
    fi
    sleep 1
done

# ---- 5. install WordPress + seed sample posts ------------------------------
if [ -f "$PW_FILE" ]; then
    WP_ADMIN_PASSWORD="$(cat "$PW_FILE")"
else
    WP_ADMIN_PASSWORD="$(LD_LIBRARY_PATH="$PG_LIB" "$PG_BIN/psql" -tAc "select md5(random()::text)" -h 127.0.0.1 -p "$PGPORT_WP" -U "$PGUSER_WP" -d "$PGDB_WP" 2>/dev/null | head -c 16)"
    [ -n "$WP_ADMIN_PASSWORD" ] || WP_ADMIN_PASSWORD="goopg-admin-pw"
    printf '%s' "$WP_ADMIN_PASSWORD" > "$PW_FILE"
    chmod 600 "$PW_FILE"
fi

log "Installing WordPress (English) and seeding sample posts via wp-cli"
docker compose -f "$WP_DIR/docker-compose.yml" run --rm \
    -e WP_URL="$WP_URL" \
    -e WP_TITLE="$WP_TITLE" \
    -e WP_ADMIN_PASSWORD="$WP_ADMIN_PASSWORD" \
    --entrypoint sh wpcli /seed/seed.sh

log "Setup complete."
cat <<EOF

  WordPress:   $WP_URL
  Admin:       $WP_URL/wp-admin   (user: admin,  password: $WP_ADMIN_PASSWORD)
  Database:    goopg on 127.0.0.1:$PGPORT_WP  (db=$PGDB_WP, user=$PGUSER_WP, trust auth)
  goopg log:   $LOG_FILE

  Stop:  docker compose -f wp/docker-compose.yml down
         make stop DATA_DIR=$DATA_DIR   (or: systemctl --user stop goopg-wp.scope)
EOF
