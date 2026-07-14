// SASLprep normalization for SCRAM authentication, per [RFC3454]
// ("stringprep") and [RFC4013] ("SASLprep: Stringprep Profile for User
// Names and Passwords").
//
// This is a straight port of postgres/src/common/saslprep.c's pg_saslprep,
// including its exact table data (see saslprep_tables.go, mechanically
// extracted from the same C source) and its one notable quirk: the
// "prohibited output" and bidi checks below run against the MAPPED input
// (post step 1, pre step 2 NFKC normalization) rather than the normalized
// output — upstream's own comment says "if any [prohibited characters] are
// found" in the output, but the code checks input_chars, not
// output_chars. We replicate this exactly rather than "fixing" it, since
// goopg's contract is byte-for-byte compatibility with real PostgreSQL's
// derived SCRAM secrets and password verification, not a from-scratch
// reading of the RFC.
package auth

import (
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// saslPrepRange is an inclusive [lo, hi] Unicode codepoint range, matching
// postgres/src/common/saslprep.c's pg_wchar range-pair arrays.
type saslPrepRange struct {
	lo, hi rune
}

// inRanges mirrors upstream's is_code_in_table: a binary search over
// sorted, non-overlapping ranges. Our tables are extracted verbatim from
// upstream's own sorted arrays (saslprep_tables.go), so this is safe.
func inRanges(r rune, ranges []saslPrepRange) bool {
	lo, hi := 0, len(ranges)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case r < ranges[mid].lo:
			hi = mid - 1
		case r > ranges[mid].hi:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

// pgSASLPrepRC mirrors pg_saslprep_rc (postgres/src/include/common/saslprep.h).
type pgSASLPrepRC int

const (
	saslPrepSuccess     pgSASLPrepRC = 0
	saslPrepInvalidUTF8 pgSASLPrepRC = -2
	saslPrepProhibited  pgSASLPrepRC = -3
)

// pgSASLPrep normalizes input per pg_saslprep (postgres/src/common/saslprep.c).
// Callers that only care about the "did it work" question should use
// saslPrepOrOriginal instead, which applies upstream's own fallback rule.
func pgSASLPrep(input string) (string, pgSASLPrepRC) {
	if isASCII(input) {
		return input, saslPrepSuccess
	}
	if !utf8.ValidString(input) {
		return "", saslPrepInvalidUTF8
	}

	// Step 1: Map -- non-ASCII space -> U+0020; "commonly mapped to
	// nothing" characters are dropped.
	mapped := make([]rune, 0, len(input))
	for _, r := range input {
		switch {
		case inRanges(r, nonASCIISpaceRanges):
			mapped = append(mapped, ' ')
		case inRanges(r, commonlyMappedToNothingRanges):
			// map to nothing
		default:
			mapped = append(mapped, r)
		}
	}
	if len(mapped) == 0 {
		return "", saslPrepProhibited // don't allow empty password
	}

	// Step 2: Normalize -- NFKC over the mapped codepoints. The output of
	// this step is what gets returned on success, but (matching upstream's
	// own code, not just its comment) it is NOT what steps 3-4 validate
	// below.
	normalized := norm.NFKC.String(string(mapped))

	// Step 3: Prohibit -- checked against `mapped`, per upstream's actual
	// (not documented) behavior; see the file-level doc comment.
	for _, r := range mapped {
		if inRanges(r, prohibitedOutputRanges) || inRanges(r, unassignedCodepointRanges) {
			return "", saslPrepProhibited
		}
	}

	// Step 4: Check bidi, also against `mapped`.
	containsRandALCat := false
	for _, r := range mapped {
		if inRanges(r, randALCatCodepointRanges) {
			containsRandALCat = true
			break
		}
	}
	if containsRandALCat {
		for _, r := range mapped {
			if inRanges(r, lCatCodepointRanges) {
				return "", saslPrepProhibited
			}
		}
		first, last := mapped[0], mapped[len(mapped)-1]
		if !inRanges(first, randALCatCodepointRanges) || !inRanges(last, randALCatCodepointRanges) {
			return "", saslPrepProhibited
		}
	}

	return normalized, saslPrepSuccess
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// saslPrepOrOriginal applies pgSASLPrep and falls back to the original,
// unnormalized password whenever SASLprep fails (invalid UTF-8 or
// prohibited output) — matching pg_be_scram_build_secret's and
// scram_verify_plain_password's identical
// "if (rc == SASLPREP_SUCCESS) password = prep_password;" fallback in
// postgres/src/backend/libpq/auth-scram.c. SASLprep is a best-effort
// canonicalization, never a hard validation gate.
func saslPrepOrOriginal(password string) string {
	prepped, rc := pgSASLPrep(password)
	if rc == saslPrepSuccess {
		return prepped
	}
	return password
}
