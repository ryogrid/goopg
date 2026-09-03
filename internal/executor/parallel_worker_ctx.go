package executor

// parallel_worker_ctx.go — P3 of docs/design/parallel-query/, implementing
// chapter 03's ownership contract. Nothing here executes anything in parallel
// yet; P4 is the first stage that does.
//
// The problem this file solves: *executor.Context is a ~130-field struct that
// mixes three kinds of state, and handing the same pointer to N goroutines
// would corrupt most of it. The split is by field rather than by introducing a
// type hierarchy — a full refactor of a struct used in every operator is a much
// larger change than this feature needs, and would touch code that has nothing
// to do with parallelism.
//
// Three categories (03 §2):
//
//   - SHARED, read-only: storage, catalog, transaction identity, snapshot,
//     settings. Copied by value into the worker context, which for pointers
//     means the worker sees the same object — that is intended, because these
//     are either immutable for the statement or internally synchronised.
//
//   - COPIED BY VALUE at fan-out: ParamExec/ParamSet/ParamDirty and OuterRows.
//     These carry live values the worker's subtree will READ. Cloning them
//     empty is a correctness bug, not an optimisation: ExecParamRef raises
//     XX000 "SubPlan parameter $N read before assignment" on an unset slot
//     (expr.go), and the slots are bound by the ENCLOSING sublink's eval site
//     before the inner plan runs. The slices are copied (not aliased) because
//     SetParamExec grows them lazily and a shared backing array would race on
//     append.
//
//   - CREATED EMPTY: every per-statement cache, stats map, arena, and the
//     notice/warning buffers. Sharing these is what would actually break.
//     Three of them — CTERowCache, MaterializedCTEs, WorkTableRows — are
//     written DURING execution, and a concurrent write to a shared Go map is a
//     fatal runtime throw, not a race-detector report: `go test -race` will not
//     save you, the process dies.
//
// Plus a fourth rule with no field list: the ~40 connection callbacks are
// NIL'd, so a worker that reaches one panics at the call site instead of
// silently mutating session state from the wrong goroutine. That is the
// mechanical backstop behind the planner's parallel-safety gate (P6): when the
// gate misses something, the failure is loud and local rather than subtle.

import (
	"context"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// NewWorkerContext derives a context for one parallel worker from the leader's.
//
// `workerMctx` must be pre-allocated BY THE LEADER via mctx.Acquire. Workers
// must not allocate their own: Acquire appends to parent.children without
// synchronisation (internal/mctx/mctx.go), so concurrent acquisition is itself
// a slice-append race. `workerCtx` is the cancellation context shared by the
// fan-out — cancelling it stops every worker.
//
// The returned context is safe to hand to exactly one goroutine.
func NewWorkerContext(leader *Context, workerMctx *mmgr.Context, workerCtx context.Context) *Context {
	w := NewContext()

	// ── shared, read-only ────────────────────────────────────────────────
	// Storage and catalog are internally synchronised (the buffer pool is
	// lock-free on the pin path; the catalog is RWMutex-guarded with no
	// mutation on read). Transaction identity and the snapshot are values:
	// mvcc.Snapshot holds two slices and a *CLog, so this copy is shallow and
	// aliases the manager's arrays — which is sound ONLY because those arrays
	// are never mutated after capture. That is an invariant to preserve, not
	// a property of the copy.
	w.Pool = leader.Pool
	w.Catalog = leader.Catalog
	w.PlanCatalog = leader.PlanCatalog
	w.TxnMgr = leader.TxnMgr
	w.Tx = leader.Tx
	w.Snap = leader.Snap
	w.MultiXact = leader.MultiXact
	w.FSM = leader.FSM
	w.VM = leader.VM
	w.WAL = leader.WAL
	w.LockMgr = leader.LockMgr
	w.BackendID = leader.BackendID
	w.TxnLockBackendID = leader.TxnLockBackendID
	w.ProcNum = leader.ProcNum
	w.Params = leader.Params
	w.Now = leader.Now
	w.MaxRows = leader.MaxRows
	w.WorkMem = leader.WorkMem
	w.StatsTarget = leader.StatsTarget
	w.DataDir = leader.DataDir
	w.CurrentDatabase = leader.CurrentDatabase
	w.CurrentDatabaseOid = leader.CurrentDatabaseOid
	w.IsStandby = leader.IsStandby
	w.EnableOpportunisticPrune = leader.EnableOpportunisticPrune
	w.FreezeMinAge = leader.FreezeMinAge
	w.SyncCommitMode = leader.SyncCommitMode
	w.AsyncCommit = leader.AsyncCommit
	w.NonSuperuserRole = leader.NonSuperuserRole
	// SessionUser/SetRoleIsActive are NonSuperuserRole's M0134-0009
	// siblings — PostgreSQL propagates session/current user identity to
	// parallel workers deliberately (variable.c check_session_authorization's
	// InitializingParallelWorker branch), so a worker evaluating
	// current_user()/session_user() must see the same values as the leader,
	// not the connection's login default. Round-2 review R4.
	w.SessionUser = leader.SessionUser
	w.SetRoleIsActive = leader.SetRoleIsActive
	w.AnalyzeRandSeed = leader.AnalyzeRandSeed

	// Parallel settings ride along so a worker can reason about its own
	// fan-out (and so nested planning inside a worker sees the same limits).
	w.MaxParallelWorkersPerGather = leader.MaxParallelWorkersPerGather
	w.MaxParallelWorkers = leader.MaxParallelWorkers
	w.MinParallelTableScanBlocks = leader.MinParallelTableScanBlocks
	w.ParallelLeaderParticipation = leader.ParallelLeaderParticipation
	w.DebugParallelQuery = leader.DebugParallelQuery

	// P8: shared hash tables, by reference. This is deliberately in the
	// SHARED category rather than copied: the map and every table in it are
	// frozen before this worker exists, so sharing costs nothing and copying
	// would defeat the entire point of building once.
	w.SharedHashBuilds = leader.SharedHashBuilds

	// P9: partial-aggregate accumulators, by reference. Shared and WRITTEN by
	// this worker — each accumulator serialises its own merges.
	w.PartialAggStates = leader.PartialAggStates

	// M0127-P3.3: the spill-file registry, by reference and WRITTEN by this
	// worker (mutex-guarded). A worker's spill files belong to the LEADER's
	// statement — the worker's Context dies when its goroutine returns,
	// which is exactly the moment a per-worker registry would lose track of
	// a file whose operator Close never ran (a cancelled fan-out).
	w.tempFiles = leader.tempFiles

	// ── per-worker: cancellation and arena ───────────────────────────────
	w.Ctx = workerCtx
	w.Mctx = workerMctx

	// ── copied BY VALUE: live param and correlation state ────────────────
	// See the file header. Empty clones here would fail at the first
	// ExecParamRef below a Gather.
	w.ParamExec = append([]Datum(nil), leader.ParamExec...)
	w.ParamSet = append([]bool(nil), leader.ParamSet...)
	w.ParamDirty = append([]bool(nil), leader.ParamDirty...)
	w.OuterRows = append([]Row(nil), leader.OuterRows...)

	// ── created empty ────────────────────────────────────────────────────
	// NewContext() already gives fresh maps/slices for the caches, stats,
	// notices and CTE state, so there is deliberately nothing to do here.
	// Listing the reasoning rather than the assignments keeps this function
	// from drifting into a second, stale copy of NewContext.

	// ── forbidden: connection callbacks stay nil ─────────────────────────
	// NewContext() leaves every func field nil, so this is also a no-op by
	// construction. Stated explicitly because it is a load-bearing property:
	// a worker calling GetSetting/SetSetting/CancelBackend/QueueNotify/the
	// Pg*Rows catalog row sources must panic at the call site, not silently
	// mutate connection state. The planner refuses such plans (P6); this is
	// the backstop for when it misses one.
	//
	// Consequence the planner must respect: virtual catalog relations are
	// backed by the Pg*Rows callbacks, so a Gather over pg_class would die
	// here. That refusal belongs in the post-pass, not in this file.

	return w
}

// MergeWorkerContext folds a finished worker's accumulated output back into
// the leader. Call it AFTER joining the worker — it is not synchronised, and
// it does not need to be, because the join provides the happens-before edge.
//
// Notices are appended in worker order rather than completion order so the
// result is deterministic; PG has no ordering guarantee here either, but a
// stable one costs nothing and makes tests possible.
func MergeWorkerContext(leader, worker *Context) {
	if leader == nil || worker == nil {
		return
	}
	leader.Notices = append(leader.Notices, worker.Notices...)
	leader.NoticesWithDetail = append(leader.NoticesWithDetail, worker.NoticesWithDetail...)
	leader.Warnings = append(leader.Warnings, worker.Warnings...)

	// EX0-03 (a): fold the worker's hash-join instrumentation into the
	// leader map, PER KEY, taking the MAX of each of the 6 fields
	// independently (NBuckets, OrigNBuckets, NBatch, OrigNBatch, SpacePeak,
	// BuildTimeNs). That is PG's rule (ExecHashAccumInstrumentation aside,
	// the merge itself is explain.c:3398-3422's independent-field Max),
	// including its cross-worker field-mixing quirk: the merged row may
	// take NBuckets from one worker and NBatch from another. The leader map
	// is allocated lazily so a worker that built nothing leaves it nil.
	for j, ws := range worker.HashJoinStats {
		if ws == nil {
			continue
		}
		if leader.HashJoinStats == nil {
			leader.HashJoinStats = make(map[*optimizer.Join]*HashJoinStats)
		}
		ls, ok := leader.HashJoinStats[j]
		if !ok {
			cp := *ws
			leader.HashJoinStats[j] = &cp
			continue
		}
		if ws.NBuckets > ls.NBuckets {
			ls.NBuckets = ws.NBuckets
		}
		if ws.OrigNBuckets > ls.OrigNBuckets {
			ls.OrigNBuckets = ws.OrigNBuckets
		}
		if ws.NBatch > ls.NBatch {
			ls.NBatch = ws.NBatch
		}
		if ws.OrigNBatch > ls.OrigNBatch {
			ls.OrigNBatch = ws.OrigNBatch
		}
		if ws.SpacePeak > ls.SpacePeak {
			ls.SpacePeak = ws.SpacePeak
		}
		if ws.BuildTimeNs > ls.BuildTimeNs {
			ls.BuildTimeNs = ws.BuildTimeNs
		}
	}
}
