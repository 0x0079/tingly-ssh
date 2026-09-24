package main

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"
)

// TestStdlibLogFollowsLogLevel covers quic-go's UDP buffer warning, which is
// a bare log.Printf: it must obey --log-level like everything else, so a
// proxy at its default level prints nothing into the user's terminal.
func TestStdlibLogFollowsLogLevel(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, tc := range []struct {
		level string
		shown bool
	}{
		{defaultLogLevel(true), false}, // proxy
		{defaultLogLevel(false), true}, // client and server
		{"debug", true},
	} {
		var buf bytes.Buffer
		c := commonFlags{logLevel: tc.level}
		c.loggerTo(&buf)
		log.Printf("failed to sufficiently increase receive buffer size")
		got := buf.String()
		if shown := strings.Contains(got, "receive buffer"); shown != tc.shown {
			t.Errorf("level %s: shown=%v, want %v (output %q)", tc.level, shown, tc.shown, got)
		}
		if tc.shown && !strings.Contains(got, "level=INFO") {
			t.Errorf("level %s: not formatted by slog: %q", tc.level, got)
		}
	}
}

func TestProxyDefaultsToWarn(t *testing.T) {
	if defaultLogLevel(true) != "warn" || defaultLogLevel(false) != "info" {
		t.Fatalf("defaults: proxy=%s client=%s", defaultLogLevel(true), defaultLogLevel(false))
	}
}
