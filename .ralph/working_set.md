Task: M-NIGHTLY — graceful shutdown hang: server-side deadline (engine fix)

Files:
  - internal/server/server.go: Config.ShutdownDeadline field, Server.shutdownDeadline,
    timed connWG.Wait() in Run(), OnStop/OnStopImmediate set deadline
  - docs/design/root-0037-nightly-server-shutdown-ladder.md: updated "not fixed" → "NOW FIXED"
  - .ralph/fix_plan.md: marked graceful-shutdown task [x], filed EvalPlanQual REOPENED

Key symbols: Config.ShutdownDeadline, Server.shutdownDeadline, Run(), OnStop, OnStopImmediate

Hypothesis/Findings:
  - OnStop had no deadline — one stuck backend hung the process indefinitely.
  - Fix: Config.ShutdownDeadline (default 120s) bounds connWG.Wait() in Run().
  - On timeout, dumps goroutine stacks to <DataDir>/shutdown_goroutines.txt.
  - Immediate shutdown uses 0 deadline (no wait). Zero = backward compat.
  - Backward compat: embedded test servers (DataDir="") never start control plane,
    so shutdownDeadline stays 0 → unbounded wait (old behaviour).

Next step: Continue M-NIGHTLY — next highest-impact unchecked items:
  suite-wedge (line 1569 — likely stale after root-0040), EvalPlanQual REOPENED
  (order-dependent, passes in isolation), or regress output-normalization.

Gates run: server pkg tests PASS, build ./... PASS, pre-commit units PASS,
  ralph-state-guard OK (auto-repaired)

In-flight: none
