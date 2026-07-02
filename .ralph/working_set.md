Task: M0120-0001 — Verification harness + pre-run capture setup (WordPress
WP-CLI verification, FLOW.md §1-2). Priority banner says run M0120 before
resuming M0110.

Files:
- wp/docker-compose.yml — added `define('PG4WP_DEBUG', true);` to the
  `wordpress` service's WORDPRESS_CONFIG_EXTRA (enables pg4wp/logs/pg4wp_*.log).
  NOT yet applied — needs `docker compose -f wp/docker-compose.yml up -d
  --force-recreate wordpress` to take effect.
- wp/verification/run_item.sh (NEW) — implements FLOW.md §2's per-item capture
  skeleton almost verbatim: `run_item <id> <wp-cli-args...>` runs the command
  via the wpcli container, slices wp/goopg-wp.log by byte offset for exactly
  that item's window (`msg=statement` lines only), and pulls PG4WP's
  pg4wp_*.log out of the container. Also `baseline_snapshot <file>` for
  FLOW.md §1d (post/user/comment counts). `source`d, not executed standalone
  (uses `run_item`/`WP` as shell functions, needs `$RUN` set by caller). Syntax
  checked (`bash -n`) and functions load cleanly; NOT yet run against the live
  stack.

Key symbols: run_item(), baseline_snapshot(), WP() in run_item.sh.

Hypothesis/Findings:
- The wp goopg server (systemd scope `goopg-wp.scope`, pid on :5544, data dir
  `wp/goopg-data/`) is currently running WITHOUT `GOOPG_LOG_STATEMENT` set
  (confirmed via /proc/<pid>/environ — only GOOPG_CG_UNIT=goopg-wp present).
  FLOW.md §1a requires restarting it with `GOOPG_LOG_STATEMENT=all` before any
  checklist item can be captured with statement-log evidence (data survives
  the restart; goopg persists table data in the `postgres` DB).
- **BLOCKED**: attempting `systemctl --user stop goopg-wp.scope` (the first
  step of that restart) was DENIED by the Claude Code auto-mode permission
  classifier, citing my own memory note
  `interactive_vs_ralph_stop_stash_restore.md` ("leave the :5544 wp goopg
  server running; test on :5533"). That note is about a DIFFERENT scenario
  (pausing the Ralph loop itself to hand off to an interactive session) — it
  is not a blanket ban on ever restarting the wp verification server for its
  own FLOW.md-documented procedure. The classifier applied it too broadly. Per
  tool-denial policy I did not attempt to route around it (no alternate
  stop/kill command, no --no-verify-style bypass).
- The binary at bin/goopg is already fresh (built today 07:10, after the
  ACLHEAP GUC-name-validation commit), so no rebuild is needed before restart
  — only the env-var change and a plain restart.

Next step: **needs explicit human confirmation** to run the restart sequence
from FLOW.md §1a:
  1. `systemctl --user stop goopg-wp.scope` (or `make stop
     DATA_DIR="$PWD/wp/goopg-data"`) — data dir is preserved.
  2. `GOOPG_CG_UNIT=goopg-wp GOOPG_LOG_STATEMENT=all nohup
     scripts/goopg-test-run.sh ./bin/goopg start -D wp/goopg-data --listen
     0.0.0.0:5544 --hba wp/pg_hba.conf >>wp/goopg-wp.log 2>&1 &`, wait ~45s for
     readiness (`psql -h 127.0.0.1 -p 5544 -U postgres -c 'select 1'`).
  3. `docker compose -f wp/docker-compose.yml up -d --force-recreate
     wordpress` to pick up the new PG4WP_DEBUG define already committed to
     docker-compose.yml.
  4. `source wp/verification/run_item.sh && baseline_snapshot
     wp/verification/results/<ts>/baseline.txt` to close out M0120-0001, then
     proceed to M0120-0002 (WP-01..WP-16).
If the user grants a standing Bash allow-rule for `systemctl --user
{stop,start} goopg-wp.scope` (or equivalent), the rest of M0120-0001..0005 can
proceed autonomously in later loops.

Gates run: `make ralph-state-guard` PASS (auto-repaired a stale
running/completed status mismatch, unrelated to this task). No Go code
touched this loop — no unit/race/tpch-spotcheck gates apply (shell script +
docker-compose env change only). `bash -n wp/verification/run_item.sh` clean.
