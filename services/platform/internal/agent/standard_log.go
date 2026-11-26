package agent

import (
	"context"
	"log"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// The Go standard library logs a few events through the log package. net/http logs an answer a
// server sent on a connection with no request open, quoting what it sent, so a registry, a proxy
// or anything else the agent talks to could put its own text into the agent's log. The agent
// therefore takes over the log package's output: what a peer sent is left out, and anything else
// is escaped, bounded and logged through the agent's own log.

// SetLogger makes logger the agent's log, for its own events and for what the standard library
// logs. logger's handler must not write through the log package, as slog's default one does.
func SetLogger(logger *slog.Logger) {
	slog.SetDefault(logger)
	log.SetFlags(0)
	log.SetPrefix("")
	log.SetOutput(standardLog{logger.Handler()})
}

// standardLog is the log package's output: each write is one line the standard library logged.
type standardLog struct{ handler slog.Handler }

// unsolicitedResponse begins what net/http logs, followed by the answer's first bytes, when a
// server sends an answer the agent did not ask for.
const unsolicitedResponse = "Unsolicited response received on idle HTTP channel"

// standardLogBytes is the most of a line the agent logs.
const standardLogBytes = 300

func (l standardLog) Write(p []byte) (int, error) {
	ctx := context.Background()
	if !l.handler.Enabled(ctx, slog.LevelWarn) {
		return len(p), nil
	}
	line := strings.TrimSuffix(string(p), "\n")
	var record slog.Record
	if strings.HasPrefix(line, unsolicitedResponse) {
		record = slog.NewRecord(time.Now(), slog.LevelWarn, "a server sent the agent an answer it did not ask for, so the agent closed the connection", 0)
	} else {
		record = slog.NewRecord(time.Now(), slog.LevelWarn, "the Go standard library logged an event", 0)
		record.AddAttrs(slog.String("event", escapedLine(line)))
	}
	_ = l.handler.Handle(ctx, record)
	return len(p), nil
}

// escapedLine is line, at most standardLogBytes of it, with every character that isn't graphic,
// control characters and invalid UTF-8 included, escaped as Go would quote it.
func escapedLine(line string) string {
	quoted := strconv.QuoteToGraphic(truncated(line, standardLogBytes))
	return quoted[1 : len(quoted)-1]
}
