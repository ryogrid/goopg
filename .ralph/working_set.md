(idle — nothing in flight)

## Loop summary (loop #25, 2026-08-01)

**M0126-0004 DEFERRED** — one ledger row 2026-08-01. buildRec migrates no
Aggregate (bundle 02 §9), so the legacy Build path IS structurally in use
and slot chaining IS needed. But the implementation (deep nextLazy rewrite
touching lazyProbeSlot/lazyRow/lazyVirtualOut) is high-risk — 0b's Q12=0
regression demonstrated the fragility. Deferred to post-M0126-0005, which
quantifies the legacy-path cost and justifies the risk.

Partial changes reverted before commit (lazyProbeSourceIdx field +
ensureLazyVirtual source tracking).

**Next: M0126-0005** — Stage 0 A/B + fusion go/no-go decision. No code.
TPC-H SF1 A/B (65433) with/without -0003 changes, mhjPackingEnabled
forced off, matched server age/GOGC/GOMEMLIMIT.

M0126 progress: -0001 ✓, -0002 ✓, -0003 ✓, -0004 (deferred).
In-flight: none.
