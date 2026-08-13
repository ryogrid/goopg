(idle — nothing in flight)

M0132-S2..S5 landed as one land-together commit: the extended protocol now
drives `applyTransactionVerb` (S2), in-block `Execute` reuses `connTx.Tx()`
(S3), the deferred FK/UNIQUE/EXCLUDE + SSI sequence reaches the extended
COMMIT (S4), and aborted-block semantics land with both SIMPLE-path gaps
closed (S5). `m0132ExtendedBlocksLanded` flipped to `true`; all 8 bar tests
green. Next per the `## Current Priority` banner: M0132-S6 (isolation level
over the extended protocol).
