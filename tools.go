//go:build tools

// Package tools pins development-only tool dependencies into go.mod so the
// whole team/CI resolves identical generator versions. Nothing here is
// imported by production builds (the `tools` build tag excludes this file).
//
// Pinned: golang.org/x/tools v0.49.0 — the version the grammar-port probe
// validated against the full upstream gram.y (0 conflicts, 6,501 states;
// docs/design/not_ralph/02-grammar-porting-guide.md §1). Bump deliberately,
// rerun the conflict-gate check, and update the pinned-version note in
// internal/sqlparser's gen-parser target when you do.
package tools

import (
	_ "golang.org/x/tools/cmd/goyacc"
)
