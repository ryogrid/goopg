# WordPress-on-goopg — Verification Execution Flow

How to run the [`CHECKLIST.md`](./CHECKLIST.md) items and capture, for **every**
item (passing ones included), the three evidence streams the verification
requires:

1. **WordPress side** — WP-CLI stdout/stderr + exit code, and the PG4WP SQL log
   (the MySQL→PostgreSQL rewrite + the exact SQL sent to goopg).
2. **goopg side** — the statement log (`GOOPG_LOG_STATEMENT`, root-0023): every
   query goopg received, with `proto` and, inside an explicit transaction, the
   `xid`.
3. **Transaction / result confirmation** — a confirming `SELECT` proving the DB
   state changed as expected.

Running this flow and storing the evidence is milestone **M0120**; remediating
failures is **M0121**.

## 0. Prerequisites

- The stack is up per [`../README.md`](../README.md): the `goopg-wordpress`
  container on `:8080` and host-side goopg on `:5544` backing the `postgres`
  database. `wp/.env` has a current `GOOPG_DB_HOST`.
- WP-CLI helper (see CHECKLIST §Conventions):
  ```bash
  WP() { docker compose -f wp/docker-compose.yml run --rm --no-TTY wpcli wp "$@"; }
  ```

## 1. Enable evidence capture (pre-run)

### 1a. goopg statement logging

goopg must run with `GOOPG_LOG_STATEMENT=all` so every received query is logged
to `wp/goopg-wp.log`. If goopg is already running without it, restart it (data
survives) through the mandatory memory cap:

```bash
# stop the current wp goopg scope (data dir is preserved)
systemctl --user stop goopg-wp.scope 2>/dev/null || make stop DATA_DIR="$PWD/wp/goopg-data"

# relaunch with statement logging enabled (mirrors setup.sh's invocation)
GOOPG_CG_UNIT=goopg-wp GOOPG_LOG_STATEMENT=all nohup \
  scripts/goopg-test-run.sh ./bin/goopg start -D wp/goopg-data \
  --listen 0.0.0.0:5544 --hba wp/pg_hba.conf >>wp/goopg-wp.log 2>&1 &
# wait for readiness (goopg cold start replays WAL, ~45s):
#   until PGHOST=127.0.0.1 PGPORT=5544 PGUSER=postgres psql -c 'select 1'; do :; done
```

The log line `msg="statement logging enabled" log_statement=all` confirms it is
on. Levels other than `all` (`ddl`, `mod`) exist but a verification run wants
everything, so use `all`.

### 1b. PG4WP query logging

Enable PG4WP debug logging so the WordPress-side rewrite and the exact SQL sent
to goopg are captured to `wp-content/pg4wp/logs/` inside the container. PG4WP
reads `PG4WP_DEBUG` (default `false`) via `if (!defined('PG4WP_DEBUG'))` in
`pg4wp/db.php`, so define it earlier. Add to the `wordpress` service's
`WORDPRESS_CONFIG_EXTRA` in `docker-compose.yml`:

```php
define('DB_DRIVER', 'pgsql');
define('PG4WP_DEBUG', true);        // logs pg4wp_*.log: converted + unmodified SQL
```

then recreate the container: `docker compose -f wp/docker-compose.yml up -d --force-recreate wordpress`.

**IMPORTANT — recreate alone is not enough on an existing `wp_html` volume.**
The WordPress entrypoint only writes `WORDPRESS_CONFIG_EXTRA` into `wp-config.php`
when that file does **not** already exist. Because `wp_html` persists across
recreates, a `--force-recreate` leaves the old `wp-config.php` in place and
`PG4WP_DEBUG` never lands (verified 2026-07-03, M0120-0001). So after recreating,
inject the define directly (idempotent) — the file lives at the doc root
`wp-config.php`, not under `wp-content/`:

```bash
docker compose -f wp/docker-compose.yml run --rm --no-TTY wpcli \
  sh -c "grep -q PG4WP_DEBUG wp-config.php || sed -i \"1a define('PG4WP_DEBUG', true);\" wp-config.php"
```

Confirm with `grep -n PG4WP_DEBUG wp-config.php`, then run any `wp` command and
check `wp-content/pg4wp/logs/pg4wp_*.log` appears. PG4WP then writes:
- `pg4wp/logs/pg4wp_*.log` — each `initial → converted` rewrite,
- `pg4wp/logs/pg4wp_unmodified.log` — queries passed through unchanged,
- `pg4wp/logs/pg4wp_errors.log` — error-generating queries (`PG4WP_LOG_ERRORS`,
  on by default).

### 1c. (optional) WordPress debug log

For PHP-level errors during an item, enable in `WORDPRESS_CONFIG_EXTRA`:
`define('WP_DEBUG', true); define('WP_DEBUG_LOG', true);` → `wp-content/debug.log`.

### 1d. Baseline snapshot

Record starting counts so relative assertions (e.g. WP-06 "+20 posts") are
robust:

```bash
WP post list --format=count; WP user list --format=count; WP comment list --format=count
```

## 2. Per-item procedure

For each checklist item, capture all streams keyed by a byte offset into
`goopg-wp.log` (goopg emits to a single shared log, so slice by offset, not by
grep-all). A harness skeleton:

```bash
RUN=wp/verification/results/$(date +%Y%m%d-%H%M%S)     # git-ignored
mkdir -p "$RUN"
GLOG=wp/goopg-wp.log

run_item() {
  id="$1"; shift                                        # e.g. WP-01
  d="$RUN/$id"; mkdir -p "$d"
  goff=$(wc -c < "$GLOG")                                # goopg log offset BEFORE
  # 1) run the WP-CLI command, capturing stdout/stderr/exit
  WP "$@" >"$d/stdout.txt" 2>"$d/stderr.txt"; echo $? >"$d/exit.txt"
  # 2) slice goopg's statement log for exactly this item's window
  tail -c +$((goff+1)) "$GLOG" | grep 'msg=statement' >"$d/goopg_statements.log" || true
  # 3) snapshot the PG4WP logs (copied out of the container volume)
  docker compose -f wp/docker-compose.yml run --rm --no-TTY --entrypoint sh wpcli \
    -c 'cat wp-content/pg4wp/logs/pg4wp_*.log 2>/dev/null' >"$d/pg4wp.log" || true
  echo "$*" >"$d/command.txt"
}
```

Then, per item, run the command **and** its confirming read from the checklist,
storing the confirming read's output as the PASS/FAIL evidence:

```bash
run_item WP-01 post create --post_title='goopg test post' --post_status=publish --porcelain
postID=$(cat "$RUN/WP-01/stdout.txt")
WP post get "$postID" --field=post_status >"$RUN/WP-01/confirm.txt"   # expect: publish
```

Record for each item: `exit.txt`, `stdout.txt`, `stderr.txt`,
`goopg_statements.log`, `pg4wp.log`, `confirm.txt`, and a one-line verdict.
Note: capture the goopg statements and PG4WP SQL **even when the item passes** —
the issued-query evidence is a deliverable, not just failure diagnostics.

## 3. Evidence layout

```
wp/verification/results/<timestamp>/
├── report.md                 # summary table: id | op | PASS/FAIL | note
├── baseline.txt              # §1d starting counts
├── WP-01/{command,stdout,stderr,exit,goopg_statements,pg4wp,confirm}.txt/.log
├── WP-02/…
└── …
```

`wp/verification/results/` is git-ignored (see `wp/.gitignore`); only
`CHECKLIST.md` and `FLOW.md` are committed. A run may attach a curated
`report.md` to the M0120 milestone evidence.

## 4. Verdict & triage

Each item is **PASS** if its confirming read matches the checklist's expected
result (exit code alone is insufficient), else **FAIL**. Classify every FAIL by
root cause, using the captured goopg statement + PG4WP SQL as the anchor:

| Class | Meaning | Owner |
|-------|---------|-------|
| `goopg-bug` | goopg executed the SQL but returned a wrong result / error | M0121 fix |
| `goopg-missing` | goopg rejected valid SQL (feature not implemented / wrong SQLSTATE) | M0121 implement |
| `pg4wp-limitation` | PG4WP failed to translate before goopg saw it (e.g. `information_schema`) | out of scope (document) |
| `harness` | test/setup error (wrong command, missing dependency) | fix the checklist/flow |

For any `goopg-bug` / `goopg-missing`, append a `.ralph/deferral_ledger.md` row
(upstream PG citation + resume point) and open the corresponding `M0121-NNNN`
task — this is the M0120→M0121 handoff.

## 5. Teardown

After a run, restore normal operation:

```bash
# revert PG4WP debug (remove the define + recreate) if you want quieter logs
# restart goopg without statement logging (optional — it is off by default):
systemctl --user stop goopg-wp.scope
GOOPG_CG_UNIT=goopg-wp nohup scripts/goopg-test-run.sh ./bin/goopg start \
  -D wp/goopg-data --listen 0.0.0.0:5544 --hba wp/pg_hba.conf >>wp/goopg-wp.log 2>&1 &
```

Statement logging adds a log line per query; leaving it on is harmless for a dev
instance but grows `goopg-wp.log`, so disable it once evidence is captured.
