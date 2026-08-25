// grammar/goopg_ext.y — goopg-only grammar extensions.
//
// Policy (docs/design/not_ralph/02-grammar-porting-guide.md §7): every rule
// here must carry a `// GOOPG-EXT: <reason>` tag. Rules splice BEFORE
// pg_grammar.y's final "%%" (the Makefile appends the closing %% itself), so
// alternatives may extend extension points defined in the main file.
//
// Currently EMPTY: the survey found no goopg-specific syntax. Statements
// upstream has but goopg has not implemented are expressed as faithful
// pg_grammar rules producing the existing compat stubs, not as extensions.
