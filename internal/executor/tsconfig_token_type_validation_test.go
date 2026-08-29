package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0134-0178 (tsdicts.sql) guard: ALTER TEXT SEARCH CONFIGURATION's four
// mapping forms must run their `FOR tok [, ...]` list through
// getTokenTypes' semantics (tsearchcmds.c:1229) before touching
// pg_ts_config_map — deduplicating repeated names and rejecting any name the
// configuration's parser cannot emit.
//
// goopg previously did neither, which the upstream case's trailing
// "Test grammar for configurations" block exposes in six places: an unknown
// token type reached the mapping lookup and surfaced as the WRONG error
// ("mapping for token type ... does not exist", 42704) or was swallowed
// entirely by DROP MAPPING's IF EXISTS, while a duplicated token type made
// `DROP MAPPING FOR word, word` fail on its own second pass and
// `ADD MAPPING FOR word, word WITH d` collide with itself on
// pg_ts_config_map_index.

// TestAlterTSConfigMappingRejectsUnknownTokenType pins the parser-validation
// half. The error is ERRCODE_INVALID_PARAMETER_VALUE (22023) with upstream's
// exact wording, and it is raised by ADD, ALTER and DROP alike.
func TestAlterTSConfigMappingRejectsUnknownTokenType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_tok_cfg (PARSER = default)`); err != nil {
		t.Fatalf("create text search configuration: %v", err)
	}

	for _, stmt := range []string{
		`ALTER TEXT SEARCH CONFIGURATION ts_tok_cfg ADD MAPPING FOR not_a_token WITH simple`,
		`ALTER TEXT SEARCH CONFIGURATION ts_tok_cfg ALTER MAPPING FOR not_a_token WITH simple`,
		`ALTER TEXT SEARCH CONFIGURATION ts_tok_cfg DROP MAPPING FOR not_a_token`,
		// IF EXISTS covers a missing MAPPING, never a token type the parser
		// cannot emit: upstream still errors here.
		`ALTER TEXT SEARCH CONFIGURATION ts_tok_cfg DROP MAPPING IF EXISTS FOR not_a_token, not_a_token`,
	} {
		ctx.TakeNotices()
		err := runDDL(t, ctx, stmt)
		if err == nil {
			t.Errorf("%s: want 22023, got nil", stmt)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "22023" {
			t.Errorf("%s: want *ExecError 22023, got %T %v", stmt, err, err)
			continue
		}
		if want := `token type "not_a_token" does not exist`; ee.Message != want {
			t.Errorf("%s: Message=%q want %q", stmt, ee.Message, want)
		}
		if n := ctx.TakeNotices(); len(n) != 0 {
			t.Errorf("%s: emitted notices %v, want none (the error pre-empts the mapping lookup)", stmt, n)
		}
	}
}

// TestAlterTSConfigMappingDeduplicatesTokenTypes pins the deduplication half
// across all three mapping forms that act per token type. Each statement here
// FAILS at the pre-fix HEAD: DROP errors on its own second pass, ADD collides
// with its own first insert (23505), and IF EXISTS emits the skip NOTICE
// twice.
func TestAlterTSConfigMappingDeduplicatesTokenTypes(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("fixture catalog is %T, want *catalog.InMemory", cat)
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_dedup_cfg (PARSER = default)`); err != nil {
		t.Fatalf("create text search configuration: %v", err)
	}

	// ALTER (override) with a duplicated token leaves exactly one mapping.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_dedup_cfg ALTER MAPPING FOR word, word WITH simple`); err != nil {
		t.Fatalf("ALTER MAPPING FOR word, word: %v", err)
	}
	if got := mappingDictOIDs(im, "ts_dedup_cfg", "word"); len(got) != 1 {
		t.Errorf("after ALTER MAPPING FOR word, word: DictOIDs=%v, want exactly one", got)
	}

	// DROP with a duplicated token deletes once and succeeds — the second
	// occurrence must never be looked up.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_dedup_cfg DROP MAPPING FOR word, word`); err != nil {
		t.Fatalf("DROP MAPPING FOR word, word: %v", err)
	}

	// IF EXISTS on a now-unmapped token emits ONE skip NOTICE, not two.
	ctx.TakeNotices()
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_dedup_cfg DROP MAPPING IF EXISTS FOR word, word`); err != nil {
		t.Fatalf("DROP MAPPING IF EXISTS FOR word, word: %v", err)
	}
	notices := ctx.TakeNotices()
	if len(notices) != 1 {
		t.Errorf("DROP MAPPING IF EXISTS FOR word, word: notices=%v, want exactly one", notices)
	} else if want := `mapping for token type "word" does not exist, skipping`; notices[0] != want {
		t.Errorf("notice=%q want %q", notices[0], want)
	}

	// ADD with a duplicated token inserts one row instead of colliding with
	// itself on pg_ts_config_map_index.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_dedup_cfg ADD MAPPING FOR word, word WITH simple`); err != nil {
		t.Fatalf("ADD MAPPING FOR word, word: %v", err)
	}
	if got := mappingDictOIDs(im, "ts_dedup_cfg", "word"); len(got) != 1 {
		t.Errorf("after ADD MAPPING FOR word, word: DictOIDs=%v, want exactly one", got)
	}

	// The genuine duplicate — a SECOND statement re-adding an already-mapped
	// token — must still raise 23505 (TestAlterTSConfigAddMappingDuplicate-
	// Raises23505's invariant): dedup is per-statement, not a global
	// suppression.
	err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_dedup_cfg ADD MAPPING FOR word WITH simple`)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23505" {
		t.Fatalf("re-ADD in a separate statement: want *ExecError 23505, got %T %v", err, err)
	}
}
