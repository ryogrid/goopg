# WordPress on goopg

This directory runs a **WordPress** blog whose entire database is served by
**goopg** — the from-scratch Go reimplementation of PostgreSQL in this repo.

WordPress is built for MySQL, so it reaches goopg through
[**PG4WP**](https://github.com/PostgreSQL-For-Wordpress/postgresql-for-wordpress),
a drop-in `wp-content/db.php` that reimplements WordPress's `mysqli_*` calls on
top of the PHP `pgsql` extension and translates MySQL SQL to PostgreSQL.

```
┌──────────────────────────┐        PostgreSQL wire protocol
│  Docker                  │        (pg_connect, port 5544)
│  ┌────────────────────┐  │                 │
│  │ WordPress (PHP)    │  │                 ▼
│  │  + PG4WP (db.php)  │──┼──────►  ┌─────────────────────┐
│  │  + pgsql extension │  │        │ goopg  (on the host)│
│  └────────────────────┘  │        │ listens 0.0.0.0:5544│
└──────────────────────────┘        │ db=postgres, trust  │
   http://localhost:8080            └─────────────────────┘
```

goopg runs **on the host** (not in a container) so it goes through the project's
mandatory memory-cap wrapper (`scripts/goopg-test-run.sh`). The WordPress and
wp-cli containers reach it via the host's LAN IP.

---

## Prerequisites

- Docker + Docker Compose (this setup was validated with Docker Engine 29 /
  Compose v5 running natively inside WSL2).
- The goopg repo builds (`make build` from the repo root works).
- No PHP/MySQL on the host is required — everything WordPress-side is in Docker.

## First-time setup

From this directory:

```bash
./setup.sh
```

`setup.sh` is idempotent and does everything:

1. Fetches the PG4WP connector into `wp/pg4wp/`.
2. Builds goopg and initialises a dedicated data directory at `wp/goopg-data/`.
3. Starts goopg on `0.0.0.0:5544`, memory-capped (systemd scope `goopg-wp`),
   using `wp/pg_hba.conf` (trusts the Docker subnet).
4. Records the host IP the containers use in `wp/.env`.
5. Builds the WordPress + wp-cli images and starts the WordPress container.
6. Installs WordPress non-interactively in **English** and seeds sample posts.

When it finishes it prints the site URL and the generated admin password (also
saved to `wp/.wp-admin-password`).

## Startup after setup

The goopg server lives in a transient systemd user scope and the containers stop
when Docker/the host restarts, so after a reboot just re-run:

```bash
./setup.sh
```

It detects what is already done and only (re)starts what is needed — it will
restart goopg, refresh `wp/.env` (the host IP can change across reboots), and
bring the container back up. Existing WordPress data is preserved (see
*Data persistence* below); the install/seed steps are skipped.

> **Note:** goopg cold-start replays its WAL and can take **~45 seconds** to
> begin accepting connections. `setup.sh` waits for readiness automatically.

If goopg is already running and `wp/.env` is current, you can start just the web
container directly:

```bash
docker compose up -d
```

## Access

| What        | URL / value                                            |
|-------------|--------------------------------------------------------|
| Blog        | http://localhost:8080                                  |
| Admin       | http://localhost:8080/wp-admin (user `admin`, password in `wp/.wp-admin-password`) |
| Language    | English (`en_US`)                                      |

### Database connection (goopg)

| Setting   | Value                          |
|-----------|--------------------------------|
| Host/port | `127.0.0.1:5544` (host) — containers use the host LAN IP from `wp/.env` |
| Database  | `postgres`                     |
| User      | `postgres` (superuser)         |
| Auth      | `trust` (no password; goopg v0 enforces only trust/reject) |

Inspect the data directly with the in-tree psql:

```bash
make -C .. psql LISTEN=127.0.0.1:5544
# or:
LD_LIBRARY_PATH=../postgres/local_install/lib \
  ../postgres/local_install/bin/psql -h 127.0.0.1 -p 5544 -U postgres -d postgres \
  -c "SELECT ID, post_title FROM wp_posts WHERE post_status='publish';"
```

## Stopping

```bash
docker compose down                         # stop WordPress
make -C .. stop DATA_DIR="$PWD/goopg-data"   # stop goopg
# or: systemctl --user stop goopg-wp.scope
```

## Data persistence

WordPress stores everything (users, posts, options, comments) as **tables** in
goopg's `postgres` database, and those tables are checkpointed to disk — they
**survive a goopg restart** (verified: stop goopg, restart, posts still render).

We deliberately use the built-in `postgres` database and `postgres` superuser
because goopg's `CREATE DATABASE` / `CREATE ROLE` are in-memory-only in v0 and do
**not** persist across a clean restart, whereas table data in `postgres` does.

## Known limitations

Most of the gaps this setup originally surfaced have since been **fixed in
goopg** (see `docs/design/root-0019` … `root-0021`): unknown-literal coercion
(`WHERE ID = '1'`), sequence/SERIAL restart persistence, column DEFAULT
restart persistence, upsert serial/DEFAULT parity, and persistent roles/auth.
The full stack now works: public blog, **wp-admin dashboard**, post creation,
and restart durability for schema, ids, defaults, and roles.

Remaining known issues:

- **Oversized-option page corruption after restart (open goopg bug).** goopg
  has no TOAST; WordPress stores a few >8KB option values (theme-patterns /
  block-CSS transients, up to ~30KB). After heavy admin traffic followed by a
  goopg restart, a *neighboring* small row (`wp_user_roles`) has twice been
  observed reading back with foreign bytes, which fatals WordPress
  (`array_keys(): Argument #1 must be of type array`). Tracked in
  `.ralph/deferral_ledger.md` with the evidence and repro plan. **Repair**,
  should it happen:

  ```bash
  psql -h 127.0.0.1 -p 5544 -U postgres -d postgres <<'SQL'
  DELETE FROM wp_options WHERE option_name LIKE '\_site\_transient\_wp\_theme\_files\_patterns%'
     OR option_name IN ('_transient_wp_core_block_css_files','_transient_wp_styles_for_blocks');
  UPDATE wp_options SET option_value = 'a:0:{}' WHERE option_name = 'wp_user_roles';
  SQL
  docker compose run --rm wpcli wp eval \
    'require_once(ABSPATH . "wp-admin/includes/schema.php"); populate_roles();'
  ```

- **WordPress update checks are disabled** via a mu-plugin
  (`mu-plugins/disable-update-checks.php`): `wp_version_check()`'s
  database-size probe queries MySQL's `information_schema`, which the PG4WP
  connector cannot translate (it would fatal the dashboard). Core/plugin/theme
  updates must be applied by rebuilding the image.

## Files

| File                | Purpose                                                     |
|---------------------|-------------------------------------------------------------|
| `setup.sh`          | Idempotent orchestrator (build/init/start/install/seed).    |
| `docker-compose.yml`| `wordpress` (web, :8080) + on-demand `wpcli` services.       |
| `Dockerfile`        | `wordpress` image + `pgsql` extension + PG4WP drop-in.       |
| `Dockerfile.cli`    | `wordpress:cli` image + `pgsql` extension (install/seed).    |
| `pg_hba.conf`       | Trust policy allowing the Docker subnet to reach goopg.     |
| `seed/seed.sh`      | Runs `wp core install` (English) and creates sample posts.  |
| `pg4wp/`            | Fetched PG4WP connector (git-ignored).                       |
| `goopg-data/`       | goopg data directory for this instance (git-ignored).        |
| `.env`              | Generated host IP for container→goopg access (git-ignored).  |
| `.wp-admin-password`| Generated admin password (git-ignored).                      |
