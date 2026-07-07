Task: M0122-0007 — "SIGHUP config reload" item (one of ~14 in the
bucket). COMPLETE and committed this loop (e146b321).

Files: internal/server/server.go (startControlPlane: cl.OnReload now
calls new s.reloadConfig() instead of just logging; added a
signal.Notify(syscall.SIGHUP) goroutine tied to runCtx that calls the
same reloadConfig; new Config.ConfigPath field; new imports
os/signal, syscall), internal/config/guc.go (new
Registry.ApplyReloadEntries + ReloadResult type — context-gates
ContextPostmaster/ContextInternal out with a warning, applies
everything else + fires invokeOnChange, never hard-fails), internal/
config/defaults.go (stale "today the reload is a no-op" comment
fixed), internal/config/guc_test.go (2 new tests:
TestApplyReloadEntriesAppliesSigHupSkipsPostmaster,
TestApplyReloadEntriesFiresOnChange), internal/server/reload_test.go
(new file, 2 tests: TestReloadConfigAppliesSigHupSkipsPostmaster,
TestReloadConfigNoPathIsNoop), cmd/goopg/main.go (cfg.ConfigPath =
*confPath wired next to cfg.Registry; runReload's stdout message
dropped the stale "(v0 no-op)" suffix), docs/design/
root-0004-configuration-and-guc.md (new "Hot reload (2026-07-08)"
section; "What this doc does NOT cover" bullet updated), docs/design/
README.md (root-0004 row extended), .ralph/fix_plan.md (M0122-0007
bullet gained the done writeup + "still open" note for the other ~13
items in the bucket).

Key symbols: Server.reloadConfig (internal/server/server.go) —
re-parses cfg.ConfigPath via config.ParseConfigFile, calls
cfg.Registry.ApplyReloadEntries, logs warnings + a changed/warnings
summary; never errors out (matches ProcessConfigFile's log-and-keep-
running contract). Registry.ApplyReloadEntries (internal/config/
guc.go) — per-entry: unknown name / ContextInternal / ContextPostmaster
all become a Warnings entry and are skipped; everything else
canonicalizes, sets v.Value+v.Source=SourceConfigFile, calls
r.invokeOnChange(v.Name, canon) (only if changed), appends to Changed.

Findings: Root gap was literally a stub — cl.OnReload just logged
"(v0 no-op)"; nothing re-read the file. Distinguishing feature vs.
boot-time ApplyConfigEntries: boot bypasses Variable.Set's context
gating entirely (setFromFile lets ANY context, including
ContextPostmaster, load at startup) and never calls invokeOnChange
(nothing has read the registry yet); reload is the opposite on both
counts — it must NOT silently apply ContextPostmaster values to an
already-running process (would lie about e.g. shared_buffers, already
baked into a fixed-size buffer pool), and it MUST fire OnChange since
a live process-global toggle (enable_nestloop_index →
planner.SetNLIEnabled) has to observe the change immediately, the same
as SET would. SessionRegistry.Get falls through to the live global
Registry's *Variable pointer when a session has no local/session
override, so a reload's new value is picked up by every future SHOW /
current_setting() without any SessionRegistry-side change needed — a
new connection (or any existing one that never SET the var itself)
just sees it. Verified non-vacuous via the two new test files (context
split + OnChange firing). Live end-to-end verified against the real
cmd/goopg binary: (1) goopg reload -D <datadir> after editing
checkpoint_timeout (PGC_SIGHUP, 600->900) + adding max_connections=5
(PGC_POSTMASTER) — SHOW checkpoint_timeout moved 10min->15min, SHOW
max_connections stayed at 100, server log showed the "cannot be
changed without restarting the server" warning for max_connections
only; (2) repeated with a literal `kill -HUP <pid>` instead of the
control-socket command — identical result (checkpoint_timeout
900->1200, SHOW confirmed 20min).

Next step: pick the next task. M-NIGHTLY is clean (still run
20260707-000712, all 8 items resolved; ci/logs/action-items.md
unchanged — re-verify at next loop start per the standing rule).
Candidates carried from prior loops: (a) M0122-0006's opclass/
collation OID resolution gap (indclass/indcollation real OID
resolution, live AND heap-restore paths — sizeable, needs a full
builtin-opclass/collation name<->OID registry, may warrant its own
design doc / >1 loop) and its pg_tablespace-visibility item (CREATE
TABLESPACE only updates the runtime registry + in-place dir, never the
on-disk pg_tablespace heap — needs the "no runtime in-place update for
shared catalogs" capability per that memory, flagged in the ledger as
"defer indefinitely" back on 2026-06-15 but M0122-0006's fix_plan
bullet still lists it open — worth re-reading that ledger row before
committing a loop to it); (b) M0122-0007's other ~13 items (CREATE/DROP
DATABASE full DDL, `goopg ctl restart`, REINDEX — check current state
first since operators_reindex.go already exists, tablespaces, ALTER
FUNCTION/COLUMN, planner/jit GUC stubs); (c) M0122-0008 (SASLprep/
channel binding/scram_iterations still open; RBAC mostly done per
2026-07-05/06 notes, view's-own-ACL gap remains, materially larger);
(d) M0119-0004/0005/0006/0007 per the Current Priority banner
(M0119-0004 pg_dump DU-002 residual, M0119-0005 hash/gin/gist/spgist/
brin AM gap, M0119-0006 pg_amproc dispatch gap — check overlap with
candidate (a)'s opclass work before picking both).

Gates run: go build ./... clean; go vet ./internal/config/...
./internal/server/... ./cmd/goopg/... clean; go test
./internal/config/... ./internal/server/... ./internal/control/...
./cmd/... PASS. scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33).
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh PASS (0
failed, all 3 workloads, both the manual pre-verification run and the
git-hook run at commit time). Live e2e: real cmd/goopg binary, both
goopg-reload-CLI and kill -HUP triggers, verified via psql SHOW.
make ralph-state-guard: 2 benign issues auto-repaired (identical
pattern to every prior loop — status/progress running-vs-completed
reconciliation).

In-flight: none. Manual verification server (port 65499,
/tmp/goopg-reload-verify) was stopped via `goopg stop` and its temp
dir + binary removed before this loop ended.
