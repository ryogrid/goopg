package parser

import "testing"

func TestResolveTimeOperator(t *testing.T) {
	cases := []struct {
		op          OpCode
		left, right string
		result      string
		status      TimeOperatorStatus
	}{
		{OpAdd, "time", "interval", "time", TimeOperatorFound},
		{OpAdd, "interval", "timetz", "timetz", TimeOperatorFound},
		{OpAdd, "time", "unknown", "time", TimeOperatorFound},
		{OpSub, "time", "unknown", "interval", TimeOperatorFound},
		{OpSub, "unknown", "time", "interval", TimeOperatorFound},
		{OpSub, "timetz", "unknown", "timetz", TimeOperatorFound},
		{OpSub, "timetz", "time", "timetz", TimeOperatorFound},
		{OpAdd, "date", "timetz", "timestamptz", TimeOperatorFound},
		{OpAdd, "time", "timestamp", "timestamp", TimeOperatorFound},
		{OpSub, "date", "time", "timestamp", TimeOperatorFound},
		{OpSub, "interval", "time", "interval", TimeOperatorFound},
		{OpAdd, "time", "time", "", TimeOperatorAmbiguous},
		{OpAdd, "timetz", "unknown", "", TimeOperatorAmbiguous},
		{OpAdd, "unknown", "timetz", "", TimeOperatorAmbiguous},
		{OpSub, "unknown", "timetz", "", TimeOperatorMissing},
		{OpSub, "timetz", "timetz", "", TimeOperatorMissing},
		{OpSub, "time", "date", "", TimeOperatorMissing},
		{OpSub, "time", "timestamp", "", TimeOperatorMissing},
		{OpAdd, "timestamp", "timetz", "", TimeOperatorMissing},
		{OpAdd, "timestamp", "interval", "", TimeOperatorNone},
		{OpAdd, "time", "int4", "", TimeOperatorNone},
		{OpMul, "time", "interval", "", TimeOperatorNone},
	}
	for _, c := range cases {
		result, status := ResolveTimeOperator(c.op, c.left, c.right)
		if result != c.result || status != c.status {
			t.Errorf("%s %s %s = %q, %d; want %q, %d", c.left, c.op, c.right, result, status, c.result, c.status)
		}
	}
}

func TestTimeOperatorError(t *testing.T) {
	code, message, _ := TimeOperatorError(TimeOperatorAmbiguous, OpAdd, "timetz", "unknown")
	if code != "42725" || message != "operator is not unique: time with time zone + unknown" {
		t.Errorf("ambiguous: %s %q", code, message)
	}
	code, message, _ = TimeOperatorError(TimeOperatorMissing, OpSub, "time", "date")
	if code != "42883" || message != "operator does not exist: time without time zone - date" {
		t.Errorf("missing: %s %q", code, message)
	}
}

func TestResolveDateTimeLiteralOperator(t *testing.T) {
	cases := []struct {
		op              OpCode
		left, right     string
		result, literal string
		status          TimeOperatorStatus
	}{
		{OpSub, "timestamptz", "unknown", "interval", "timestamptz", TimeOperatorFound},
		{OpAdd, "timestamptz", "unknown", "timestamptz", "interval", TimeOperatorFound},
		{OpAdd, "unknown", "timestamp", "timestamp", "interval", TimeOperatorFound},
		{OpSub, "unknown", "timestamp", "interval", "timestamp", TimeOperatorFound},
		{OpSub, "date", "unknown", "int4", "date", TimeOperatorFound},
		{OpAdd, "date", "unknown", "", "", TimeOperatorAmbiguous},
		{OpSub, "interval", "unknown", "interval", "interval", TimeOperatorFound},
		{OpMul, "number", "interval", "interval", "", TimeOperatorFound},
		{OpMul, "unknown", "interval", "interval", "float8", TimeOperatorFound},
		{OpDiv, "interval", "number", "interval", "", TimeOperatorFound},
		{OpDiv, "number", "interval", "", "", TimeOperatorMissing},
		{OpMul, "interval", "interval", "", "", TimeOperatorMissing},
		{OpAdd, "timestamp", "interval", "", "", TimeOperatorNone},
		{OpAdd, "unknown", "unknown", "", "", TimeOperatorNone},
		{OpMul, "number", "number", "", "", TimeOperatorNone},
	}
	for _, c := range cases {
		result, literal, status := ResolveDateTimeLiteralOperator(c.op, c.left, c.right)
		if result != c.result || literal != c.literal || status != c.status {
			t.Errorf("%s %s %s = %q %q %d", c.left, c.op, c.right, result, literal, status)
		}
	}
}
