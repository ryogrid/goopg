package tapnode

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// LogSize is `-s $node->logfile`: the log's current length, to read what a
// later step adds (LogFrom).
func (n *Node) LogSize() int64 {
	fi, err := os.Stat(n.LogPath())
	if err != nil {
		return 0
	}
	return fi.Size()
}

// LogFrom is slurp_file($node->logfile, $offset) as the ported tests read
// it: the log text from offset on. A PG node's log is returned as written.
// A goopg node writes structured lines (`time=… level=WARN msg="…" k=v`, or
// the log package's `2006/01/02 15:04:05 WARN …`); each is rendered in PG's
// stderr form, `WARNING:  <message> <attributes>`, so an upstream pattern
// such as qr/WARNING:.*recovery_end_command/ means the same thing on both
// flavours. The message text itself is goopg's — matching PG's wording is
// the server's job, not this reader's. M0149-0018c.
func (n *Node) LogFrom(offset int64) string {
	b, err := os.ReadFile(n.LogPath())
	if err != nil || offset > int64(len(b)) {
		return ""
	}
	text := string(b[offset:])
	if n.flavor != Goopg {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = pgStyleLogLine(l)
	}
	return strings.Join(lines, "\n")
}

var (
	slogLine   = regexp.MustCompile(`^time=\S+ level=(\S+) msg=("(?:[^"\\]|\\.)*"|\S+)(.*)$`)
	stdlogLine = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} (DEBUG|INFO|WARN|ERROR) (.*)$`)
)

// pgLevel maps slog's level names onto PG's elevel words.
func pgLevel(level string) string {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return "DEBUG"
	case "INFO":
		return "LOG"
	case "WARN", "WARNING":
		return "WARNING"
	case "ERROR":
		return "ERROR"
	case "ERROR+4":
		// xlog.LogLevelFatal: goopg's FATAL reports.
		return "FATAL"
	}
	return strings.ToUpper(level)
}

func pgStyleLogLine(l string) string {
	if m := slogLine.FindStringSubmatch(l); m != nil {
		msg := m[2]
		if strings.HasPrefix(msg, `"`) {
			if u, err := strconv.Unquote(msg); err == nil {
				msg = u
			}
		}
		return pgLevel(m[1]) + ":  " + msg + m[3]
	}
	if m := stdlogLine.FindStringSubmatch(l); m != nil {
		return pgLevel(m[1]) + ":  " + m[2]
	}
	return l
}
