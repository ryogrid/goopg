package executor

// agg_state_serial.go — M0146-0003b (M0141-S3/S4, adopted by M0146-0003).
//
// Serialising an aggRuntime into a bytea-shaped Datum and back. This is
// PG's aggserialfn/aggdeserialfn pair (nodeAgg.c): the transition state of
// a Partial aggregate must cross the Gather inside an ordinary row, and a
// row column cannot carry pointer fields (big.Int, big.Rat, maps), so the
// state is flattened to bytes the way PG's `internal` type flattens it —
// except goopg's transport stays in-process, so the encoding is ours and
// intentionally simple: fixed-layout fields per aggregate family, not
// PG's wire format.
//
// The surface is bounded by the SAME whitelist combineAggRuntime serves
// (optimizer.AggregateIsDecomposable): only fields a decomposable
// aggregate can actually populate are encoded. Everything else — the
// order-sensitive accumulators (distinct sets, strAccum, arrayElems,
// strElems), the WITHIN GROUP surface, user-state — both REFUSES to
// serialise (a state carrying it errors rather than silently dropping it)
// and is unreachable by construction anyway. A wrong partial state is a
// wrong final answer, and there is no error message at the user-visible
// level that would explain it — so the belt checks fail loudly.

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"

	"github.com/goopg/goopg/internal/optimizer"
)

// aggStateFieldBelt reports whether st carries any field outside the
// serialisable surface. A state that does cannot be transported honestly:
// the deserialize side would rebuild a runtime missing part of what the
// transition function computed.
func aggStateFieldBelt(st *aggRuntime) error {
	switch {
	case len(st.distinct) > 0:
		return fmt.Errorf("internal error: cannot serialise a DISTINCT aggregate state")
	case len(st.strAccum) > 0, len(st.strElems) > 0, len(st.strDelims) > 0:
		return fmt.Errorf("internal error: cannot serialise a string_agg state")
	case len(st.arrayElems) > 0, len(st.arrayElemKeys) > 0:
		return fmt.Errorf("internal error: cannot serialise an array_agg state")
	case len(st.withinGroupElems) > 0, st.withinGroupDirectArgSet, len(st.withinGroupDirectArgs) > 0:
		return fmt.Errorf("internal error: cannot serialise a WITHIN GROUP state")
	case st.userStateSet:
		return fmt.Errorf("internal error: cannot serialise a user-aggregate state")
	case len(st.distinctUserAggRows) > 0:
		return fmt.Errorf("internal error: cannot serialise a DISTINCT user-aggregate state")
	}
	return nil
}

// serializeAggRuntime flattens st into a KindBytes Datum for the named
// aggregate. The name keys the layout, exactly as combineAggRuntime keys
// its merge rules: the decoder must reconstruct the same fields the
// combine and finalize paths read.
func serializeAggRuntime(name string, st *aggRuntime) (Datum, error) {
	shadow := *st
	switch normalizeAggName(name) {
	case "string_agg":
		// The unordered accumulator is this aggregate's own state; the
		// ordered (strElems) form still refuses.
		shadow.strAccum = nil
	case "array_agg":
		shadow.arrayElems = nil
	}
	if err := aggStateFieldBelt(&shadow); err != nil {
		return Datum{}, err
	}
	var buf []byte
	switch normalizeAggName(name) {
	case "string_agg":
		buf = append(buf, boolByte(st.hasValue), boolByte(st.boolResult))
		buf = appendStr(buf, st.strFirstDelim)
		buf = appendStr(buf, string(st.strAccum))

	case "array_agg":
		if len(st.arrayElemKeys) > 0 {
			return Datum{}, fmt.Errorf("internal error: cannot serialise an ordered array_agg state")
		}
		buf = append(buf, boolByte(st.hasValue), boolByte(st.arrayOfArrays))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(st.arrayShape)))
		for _, d := range st.arrayShape {
			buf = binary.LittleEndian.AppendUint64(buf, uint64(int64(d)))
		}
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(st.arrayElems)))
		for i, e := range st.arrayElems {
			buf = appendStr(buf, e)
			buf = append(buf, boolByte(i < len(st.arrayElemNull) && st.arrayElemNull[i]))
		}

	case "count":
		buf = binary.LittleEndian.AppendUint64(buf, uint64(st.count))

	case "sum", "avg":
		buf = append(buf, boolByte(st.hasValue), byte(st.floatSpecial))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(st.sum))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(st.count))
		buf = encodeDatum(st.numericSum, buf)

	case "min", "max", "any_value":
		buf = append(buf, boolByte(st.hasValue))
		buf = encodeDatum(st.value, buf)

	case "bool_and", "every", "bool_or":
		buf = append(buf, boolByte(st.hasValue), boolByte(st.boolResult))

	case "bit_and", "bit_or", "bit_xor":
		buf = append(buf, boolByte(st.hasValue))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(st.intResult))
		buf = appendStr(buf, st.strResult)

	case "var_pop", "var_samp", "variance", "stddev_pop", "stddev_samp", "stddev":
		buf = append(buf, boolByte(st.hasValue), boolByte(st.intExact), boolByte(st.numericExact))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(st.count))
		buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(st.floatSx))
		buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(st.floatM2))
		sx, sxx := st.intSums()
		buf = appendBigInt(buf, sx)
		buf = appendBigInt(buf, sxx)
		buf = appendBigRat(buf, st.numericSx)
		buf = appendBigRat(buf, st.numericSxx)

	case "regr_count", "regr_avgx", "regr_avgy", "regr_sxx", "regr_syy", "regr_sxy",
		"covar_pop", "covar_samp", "regr_r2", "regr_slope", "regr_intercept", "corr":
		buf = binary.LittleEndian.AppendUint64(buf, uint64(st.regrN))
		buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(st.regrSumX))
		buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(st.regrSumY))
		buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(st.regrSumXX))
		buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(st.regrSumXY))
		buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(st.regrSumYY))

	default:
		return Datum{}, fmt.Errorf("internal error: no serialise rule for aggregate %q; "+
			"aggregateIsDecomposable should have refused the parallel split", name)
	}
	return NewBytesDatum(buf), nil
}

// deserializeAggRuntime rebuilds an aggRuntime from a serialized column.
// Truncated or trailing bytes are errors: a transport row is written and
// read inside one plan, so a length mismatch means the writer and reader
// disagree about the layout — a bug, not data.
func deserializeAggRuntime(name string, data []byte) (aggRuntime, error) {
	var st aggRuntime
	rd := &byteReader{data: data}
	switch normalizeAggName(name) {
	case "string_agg":
		st.hasValue = rd.bool()
		st.boolResult = rd.bool()
		st.strFirstDelim = rd.str()
		if acc := rd.str(); acc != "" {
			st.strAccum = []byte(acc)
		}

	case "array_agg":
		st.hasValue = rd.bool()
		st.arrayOfArrays = rd.bool()
		if n := int(rd.u32()); n > 0 && rd.err == nil {
			st.arrayShape = make([]int, n)
			for i := range st.arrayShape {
				st.arrayShape[i] = int(int64(rd.u64()))
			}
		}
		if n := int(rd.u32()); n > 0 && rd.err == nil {
			st.arrayElems = make([]string, n)
			st.arrayElemNull = make([]bool, n)
			for i := 0; i < n && rd.err == nil; i++ {
				st.arrayElems[i] = rd.str()
				st.arrayElemNull[i] = rd.bool()
			}
		}

	case "count":
		st.count = int64(rd.u64())

	case "sum", "avg":
		st.hasValue = rd.bool()
		st.floatSpecial = floatSpecialKind(rd.u8())
		st.sum = int64(rd.u64())
		st.count = int64(rd.u64())
		st.numericSum = rd.datum()

	case "min", "max", "any_value":
		st.hasValue = rd.bool()
		st.value = rd.datum()

	case "bool_and", "every", "bool_or":
		st.hasValue = rd.bool()
		st.boolResult = rd.bool()

	case "bit_and", "bit_or", "bit_xor":
		st.hasValue = rd.bool()
		st.intResult = int64(rd.u64())
		st.strResult = rd.str()

	case "var_pop", "var_samp", "variance", "stddev_pop", "stddev_samp", "stddev":
		st.hasValue = rd.bool()
		st.intExact = rd.bool()
		st.numericExact = rd.bool()
		st.count = int64(rd.u64())
		st.floatSx = math.Float64frombits(rd.u64())
		st.floatM2 = math.Float64frombits(rd.u64())
		st.intSx = rd.bigInt()
		st.intSxx = rd.bigInt()
		st.numericSx = rd.bigRat()
		st.numericSxx = rd.bigRat()

	case "regr_count", "regr_avgx", "regr_avgy", "regr_sxx", "regr_syy", "regr_sxy",
		"covar_pop", "covar_samp", "regr_r2", "regr_slope", "regr_intercept", "corr":
		st.regrN = int64(rd.u64())
		st.regrSumX = math.Float64frombits(rd.u64())
		st.regrSumY = math.Float64frombits(rd.u64())
		st.regrSumXX = math.Float64frombits(rd.u64())
		st.regrSumXY = math.Float64frombits(rd.u64())
		st.regrSumYY = math.Float64frombits(rd.u64())

	default:
		return st, fmt.Errorf("internal error: no deserialise rule for aggregate %q", name)
	}
	if rd.err != nil {
		return st, fmt.Errorf("internal error: truncated serialized state for aggregate %q: %v", name, rd.err)
	}
	if rd.pos != len(data) {
		return st, fmt.Errorf("internal error: %d trailing bytes in serialized state for aggregate %q",
			len(data)-rd.pos, name)
	}
	return st, nil
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// appendStr encodes a length-prefixed string (bit_*'s strResult width tag).
func appendStr(buf []byte, s string) []byte {
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(s)))
	return append(buf, s...)
}

// appendBigInt encodes a possibly-nil *big.Int: a present flag, then the
// signed-magnitude body (1 sign byte + magnitude bytes).
func appendBigInt(buf []byte, v *big.Int) []byte {
	if v == nil {
		return append(buf, 0)
	}
	buf = append(buf, 1)
	sign := byte(0)
	if v.Sign() < 0 {
		sign = 1
	}
	mag := new(big.Int).Abs(v).Bytes()
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(mag)))
	buf = append(buf, sign)
	return append(buf, mag...)
}

// appendBigRat encodes a possibly-nil *big.Rat as numerator+denominator
// big.Ints. Rat strings would also round-trip, but the binary form is
// fixed-width-friendly and shares appendBigInt's nil convention.
func appendBigRat(buf []byte, v *big.Rat) []byte {
	if v == nil {
		return append(buf, 0)
	}
	buf = append(buf, 1)
	buf = appendBigInt(buf, v.Num())
	return appendBigInt(buf, v.Denom())
}

// byteReader is the decode cursor. Every read funnels through rd.err so a
// truncated frame collapses to one check at the end rather than an error
// return per field.
type byteReader struct {
	data []byte
	pos  int
	err  error
}

func (r *byteReader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if r.pos+n > len(r.data) {
		r.err = fmt.Errorf("need %d bytes at %d of %d", n, r.pos, len(r.data))
		return false
	}
	return true
}

func (r *byteReader) u8() byte {
	if !r.need(1) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *byteReader) bool() bool { return r.u8() != 0 }

func (r *byteReader) u64() uint64 {
	if !r.need(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(r.data[r.pos:])
	r.pos += 8
	return v
}

func (r *byteReader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v
}

func (r *byteReader) str() string {
	n := r.u32()
	if r.err != nil {
		return ""
	}
	if !r.need(int(n)) {
		return ""
	}
	s := string(r.data[r.pos : r.pos+int(n)])
	r.pos += int(n)
	return s
}

func (r *byteReader) datum() Datum {
	d, n, err := decodeDatum(r.data[r.pos:])
	if err != nil {
		r.err = err
		return Datum{}
	}
	r.pos += n
	return d
}

func (r *byteReader) bigInt() *big.Int {
	if !r.need(1) {
		return nil
	}
	if r.data[r.pos] == 0 {
		r.pos++
		return nil
	}
	r.pos++
	n := r.u32()
	if r.err != nil || !r.need(int(n)+1) {
		return nil
	}
	sign := r.data[r.pos]
	mag := r.data[r.pos+1 : r.pos+1+int(n)]
	r.pos += 1 + int(n)
	v := new(big.Int).SetBytes(mag)
	if sign != 0 {
		v.Neg(v)
	}
	return v
}

func (r *byteReader) bigRat() *big.Rat {
	if !r.need(1) {
		return nil
	}
	if r.data[r.pos] == 0 {
		r.pos++
		return nil
	}
	r.pos++
	num := r.bigInt()
	den := r.bigInt()
	if r.err != nil || num == nil || den == nil {
		if r.err == nil {
			r.err = fmt.Errorf("truncated rational at %d", r.pos)
		}
		return nil
	}
	return new(big.Rat).SetFrac(num, den)
}

// serializeAggCall is serializeAggRuntime keyed by the call: a user-defined
// aggregate (CREATE AGGREGATE … COMBINEFUNC, M0148-0001i) ships its
// transition-state Datum — PG ships a non-internal transition type as is,
// and an internal one only through its SERIALFUNC, which the planner then
// requires (optimizer.AggregateIsDecomposable) — plus hasValue and
// userStateSet, the two flags the combine and finalize paths read.
func serializeAggCall(call optimizer.AggregateCall, st *aggRuntime) (Datum, error) {
	if call.UserAgg == nil {
		return serializeAggRuntime(call.Name, st)
	}
	shadow := *st
	shadow.userStateSet, shadow.userState = false, Datum{}
	if err := aggStateFieldBelt(&shadow); err != nil {
		return Datum{}, err
	}
	buf := []byte{boolByte(st.hasValue), boolByte(st.userStateSet)}
	if st.userStateSet {
		buf = encodeDatum(st.userState.MaterializeArena(), buf)
	}
	return NewBytesDatum(buf), nil
}

// deserializeAggCall is serializeAggCall's inverse.
func deserializeAggCall(call optimizer.AggregateCall, data []byte) (aggRuntime, error) {
	if call.UserAgg == nil {
		return deserializeAggRuntime(call.Name, data)
	}
	var st aggRuntime
	rd := &byteReader{data: data}
	st.hasValue = rd.bool()
	st.userStateSet = rd.bool()
	if st.userStateSet {
		st.userState = rd.datum()
	}
	if rd.err != nil {
		return st, fmt.Errorf("internal error: truncated serialized state for aggregate %q: %v", call.Name, rd.err)
	}
	if rd.pos != len(data) {
		return st, fmt.Errorf("internal error: %d trailing bytes in serialized state for aggregate %q",
			len(data)-rd.pos, call.Name)
	}
	return st, nil
}
