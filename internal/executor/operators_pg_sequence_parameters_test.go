package executor

// operators_pg_sequence_parameters_test.go — pg_sequence_parameters(regclass)
// SRF parity. Covers a default-parameter sequence and one created with
// explicit START/INCREMENT/MINVALUE/MAXVALUE/CACHE/CYCLE. PG oracle:
// postgres/src/backend/commands/sequence.c:1740 pg_sequence_parameters.
// M0134-0069.

import "testing"

// TestPgSequenceParametersBasic exercises both the default-parameter and
// explicit-parameter shapes of pg_sequence_parameters(regclass).
func TestPgSequenceParametersBasic(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE SEQUENCE public.defseq"); err != nil {
		t.Fatalf("CREATE SEQUENCE defseq: %v", err)
	}
	drows := runQueryRows(t, ctx,
		"SELECT start_value, minimum_value, maximum_value, increment, cycle_option, "+
			"cache_size, data_type FROM pg_sequence_parameters('defseq'::regclass)")
	if len(drows) != 1 {
		t.Fatalf("defseq row count = %d, want 1", len(drows))
	}
	d := drows[0]
	if d[0].Int != 1 { // start_value: PG default is 1
		t.Errorf("defseq start_value = %d, want 1", d[0].Int)
	}
	if d[1].Int != 1 { // minimum_value: PG default is 1
		t.Errorf("defseq minimum_value = %d, want 1", d[1].Int)
	}
	if d[2].Int != 9223372036854775807 { // maximum_value: PG default is bigint max
		t.Errorf("defseq maximum_value = %d, want 9223372036854775807", d[2].Int)
	}
	if d[3].Int != 1 { // increment: PG default is 1
		t.Errorf("defseq increment = %d, want 1", d[3].Int)
	}
	if d[4].BoolValue() {
		t.Errorf("defseq cycle_option = true, want false")
	}
	if d[5].Int != 1 { // cache_size: PG default is 1
		t.Errorf("defseq cache_size = %d, want 1", d[5].Int)
	}
	if d[6].Int != 20 { // data_type: bigint OID
		t.Errorf("defseq data_type = %d, want 20 (bigint)", d[6].Int)
	}

	if err := runDDL(t, ctx,
		"CREATE SEQUENCE public.customseq START WITH 5 INCREMENT BY 2 MINVALUE 1 "+
			"MAXVALUE 100 CACHE 3 CYCLE"); err != nil {
		t.Fatalf("CREATE SEQUENCE customseq: %v", err)
	}
	crows := runQueryRows(t, ctx,
		"SELECT start_value, minimum_value, maximum_value, increment, cycle_option, "+
			"cache_size, data_type FROM pg_sequence_parameters('customseq'::regclass)")
	if len(crows) != 1 {
		t.Fatalf("customseq row count = %d, want 1", len(crows))
	}
	c := crows[0]
	if c[0].Int != 5 {
		t.Errorf("customseq start_value = %d, want 5", c[0].Int)
	}
	if c[1].Int != 1 {
		t.Errorf("customseq minimum_value = %d, want 1", c[1].Int)
	}
	if c[2].Int != 100 {
		t.Errorf("customseq maximum_value = %d, want 100", c[2].Int)
	}
	if c[3].Int != 2 {
		t.Errorf("customseq increment = %d, want 2", c[3].Int)
	}
	if !c[4].BoolValue() {
		t.Errorf("customseq cycle_option = false, want true")
	}
	if c[5].Int != 3 {
		t.Errorf("customseq cache_size = %d, want 3", c[5].Int)
	}
	if c[6].Int != 20 {
		t.Errorf("customseq data_type = %d, want 20 (bigint)", c[6].Int)
	}
}
