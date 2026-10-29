package misc

import "math/bits"

// RecoveryTargetTimelineGoal is recovery_target_timeline's parsed form
// (RecoveryTargetTimeLineGoal in xlogrecovery.h).
type RecoveryTargetTimelineGoal int

const (
	// RecoveryTargetTimelineLatest is 'latest' (the default): the newest
	// timeline found in the archive (findNewestTimeLine).
	RecoveryTargetTimelineLatest RecoveryTargetTimelineGoal = iota
	// RecoveryTargetTimelineCurrent is 'current': pg_control's timeline
	// (RECOVERY_TARGET_TIMELINE_CONTROLFILE).
	RecoveryTargetTimelineCurrent
	// RecoveryTargetTimelineNumeric is an explicit timeline ID.
	RecoveryTargetTimelineNumeric
)

// ParseRecoveryTargetTimeline is check_recovery_target_timeline plus
// assign_recovery_target_timeline (xlogrecovery.c): "current" and "latest"
// name their goals; anything else is a timeline number read as strtoul(s,
// NULL, 0) does — leading whitespace, an optional sign, base from the prefix
// ("0x" hex, "0" octal), the longest digit prefix, 0 when there is none —
// and truncated to a TimeLineID. ok is false only when strtoul would set
// ERANGE (the value overflows unsigned long), PG's one rejected case.
func ParseRecoveryTargetTimeline(value string) (goal RecoveryTargetTimelineGoal, tli uint32, ok bool) {
	switch value {
	case "current":
		return RecoveryTargetTimelineCurrent, 0, true
	case "latest":
		return RecoveryTargetTimelineLatest, 0, true
	}
	n, ok := cStrtoul(value)
	return RecoveryTargetTimelineNumeric, uint32(n), ok
}

// cStrtoul is glibc strtoul(s, NULL, 0) on an LP64 platform: ok is false on
// ERANGE. A leading '-' negates the result modulo 2^64, as C specifies.
func cStrtoul(s string) (uint64, bool) {
	i := 0
	for i < len(s) && (s[i] == ' ' || (s[i] >= '\t' && s[i] <= '\r')) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	base := uint64(10)
	if i+1 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') && i+2 < len(s) && hexDigit(s[i+2]) >= 0 {
		base, i = 16, i+2
	} else if i < len(s) && s[i] == '0' {
		base = 8
	}
	var n uint64
	overflow := false
	for ; i < len(s); i++ {
		d := hexDigit(s[i])
		if d < 0 || uint64(d) >= base {
			break
		}
		hi, lo := bits.Mul64(n, base)
		sum, carry := bits.Add64(lo, uint64(d), 0)
		if hi != 0 || carry != 0 {
			overflow = true
		}
		n = sum
	}
	if overflow {
		return ^uint64(0), false
	}
	if neg {
		n = -n
	}
	return n, true
}

func hexDigit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// checkRecoveryTargetTimeline is recovery_target_timeline's check hook.
func checkRecoveryTargetTimeline(value string) error {
	if _, _, ok := ParseRecoveryTargetTimeline(value); !ok {
		return &ValidationError{
			Msg:    `invalid value for parameter "recovery_target_timeline": "` + value + `"`,
			Detail: `"recovery_target_timeline" is not a valid number.`,
		}
	}
	return nil
}
