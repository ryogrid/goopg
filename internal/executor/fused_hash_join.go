// Package executor — runtime hash-join-cascade fusion.
//
// M0126-0006 Stage 1: scaffolding, decision function, differential
// harness. The kill switch GOOPG_RUNTIME_JOIN_FUSION defaults OFF so
// production behaviour is bit-identical to the pre-task run by
// construction.
//
// Design of record: analysis/cost-driven-second-try-200731/
//   04-fusion-site-and-data-structures.md (site + data structures)
//   05-qualification-predicate.md (Q0-Q9 predicate)
//   10-rollback-and-kill-switches.md (KS1/KS2)

package executor

import (
	"os"
	"strconv"

	"github.com/goopg/goopg/internal/planner"
)

// ---- buildEnv — per-build context threaded through Build/buildRec ----

// buildEnv carries the per-Build context needed by tryFuseHashCascade.
// It is stored in a package-level variable (buildEnvInFlight) set by
// Build before the recursive walk and cleared on return. This avoids
// threading a new parameter through every arm of two large switches
// (bundle 04 §1.1 + finding F3).
type buildEnv struct {
	root      planner.Node // the plan root for this Build call
	inWorker  bool         // set by newGatherOp's per-worker closure (C10/F4)
	fusionCfg fusionConfig // resolved once, from env snapshot

	// Q0 memoised results — computed once per Build, not once per Join.
	q0 struct {
		ran          bool
		hasLockRows  bool
		hasGather    bool
		hasMHJ       bool
	}
}

// buildEnvInFlight is the package-level slot. Set by Build/buildRec
// before recursion, cleared on return. Not guarded by a mutex — Build
// is single-threaded and not re-entrant.
var buildEnvInFlight *buildEnv

// ---- kill switches (bundle 10) ----

// fusionConfig holds the resolved kill-switch state, read once from
// the environment at buildEnv construction time.
type fusionConfig struct {
	enabled   bool // GOOPG_RUNTIME_JOIN_FUSION=1 (KS1, default OFF)
	minLevels int  // GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS (KS2, default 3)
}

func readFusionConfig() fusionConfig {
	cfg := fusionConfig{minLevels: 3}
	if v := os.Getenv("GOOPG_RUNTIME_JOIN_FUSION"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.enabled = b
		} else if iv, err := strconv.Atoi(v); err == nil && iv != 0 {
			cfg.enabled = true
		}
	}
	if v := os.Getenv("GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			cfg.minLevels = n
		}
	}
	return cfg
}

// ---- tryFuseHashCascade — the qualification predicate + builder ----

// tryFuseHashCascade inspects the immutable plan subtree rooted at p
// and returns a fused operator when the whole cascade qualifies. It
// NEVER mutates p. Second return false ⇒ caller builds the ordinary
// cascade unchanged.
//
// Called as the first statement of the *planner.Join arm in BOTH
// builders (bundle 04 §1).
func tryFuseHashCascade(env *buildEnv, p *planner.Join) (Operator, bool) {
	if env == nil || !env.fusionCfg.enabled {
		return nil, false
	}
	// TODO: implement Q0-Q9 predicate, chain collection, operator construction
	return nil, false
}

// ---- fusedHashJoinOp — the fused operator ----

// fusedHashJoinOp executes a fused left-deep hash-join cascade as a
// single operator. See bundle 04 §5-7 for the data structure and
// §6-7 for Open/Next semantics.
type fusedHashJoinOp struct {
	// TODO: implement
}

func (o *fusedHashJoinOp) Open(ctx *Context) error {
	return nil // TODO
}

func (o *fusedHashJoinOp) Next() (TupleSlot, error) {
	return nil, nil // TODO
}

func (o *fusedHashJoinOp) Close() {}
