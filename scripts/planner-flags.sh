#!/usr/bin/env bash
# planner-flags.sh — the shared provenance stamp for benchmark artefacts.
#
# WHY (M0127-P5.9-q, 2026-08-06). Every gate that produces a comparable artefact
# — the TPC-DS SF0.5 sweep/plan capture, the TPC-H spot-check — prints a
# `planner-flags:` line naming the flags in force, so a diff between two
# captures is attributable to a known arm instead of to the operator's memory of
# what was exported. The exported case is trivially true; the UNSET case has to
# state what the binary does BY DEFAULT, and that half used to be a hand-written
# string in each script's printf, checked by nothing.
#
# It shipped wrong twice, identically: M0125-0005 flipped
# GOOPG_RELSIZE_FALLBACK's default and `unset(off)` survived; M0127-P5.9 flipped
# GOOPG_PGSHAPED_DP's default and `unset(off)` survived again — mis-stamping the
# acceptance run of the flip itself. A mis-stamped artefact is worse than an
# unstamped one, because it is the record a later loop reads to decide what an
# A/B measured.
#
# So the labels are GENERATED from the Go defaults into planner-flags.env
# (`go run ./cmd/gen-planner-flag-labels`), a flipped default that is not
# regenerated fails `TestFlagProvenanceEnvIsGenerated`, and this file is the one
# place that renders them. Adding a flag to internal/planner's provenance table
# makes both gates stamp it with no shell edit at all.
#
# Usage:
#   source "${SCRIPT_DIR}/planner-flags.sh"
#   echo "# planner-flags: $(planner_flags_body)"

# shellcheck disable=SC1090,SC1091
_planner_flags_env="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/planner-flags.env"
if [[ -r "${_planner_flags_env}" ]]; then
    source "${_planner_flags_env}"
fi

# planner_flags_body — `VAR=value VAR=value …` for every flag in the generated
# table, in table order.
#
# An EXPORTED variable is echoed verbatim (that is the arm the operator chose);
# an unset one gets the generated label; a RETIRED one always gets its
# retirement marker, because printing a live-looking value for a variable no
# code reads any more invites a later loop to A/B something the binary cannot
# see.
#
# If the generated file is missing (a partial checkout, or a gate copied
# somewhere without it) the line says so instead of guessing. An honest
# `UNKNOWN` costs one attribution; a guessed label costs a wrong conclusion.
planner_flags_body() {
    if [[ -z "${GOOPG_PLANNER_FLAG_VARS:-}" ]]; then
        printf 'UNKNOWN(scripts/planner-flags.env missing — regenerate: go run ./cmd/gen-planner-flag-labels > scripts/planner-flags.env)'
        return 0
    fi
    local var label_var retired_var out=""
    for var in ${GOOPG_PLANNER_FLAG_VARS}; do
        label_var="GOOPG_PLANNER_FLAG_LABEL_${var}"
        retired_var="GOOPG_PLANNER_FLAG_RETIRED_${var}"
        if [[ -n "${!retired_var:-}" ]]; then
            out+="${var}=${!label_var:-retired} "
        else
            out+="${var}=${!var:-${!label_var:-unset(?)}} "
        fi
    done
    printf '%s' "${out% }"
}
