package xmigrator_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	x "github.com/sxwebdev/xmigrator"
)

type nilLogger struct{}

func (*nilLogger) Debugw(string, ...any) { panic("typed nil used") }
func (*nilLogger) Infow(string, ...any)  { panic("typed nil used") }
func (*nilLogger) Warnw(string, ...any)  { panic("typed nil used") }
func (*nilLogger) Errorw(string, ...any) { panic("typed nil used") }
func TestLogger(t *testing.T) {
	var buffer bytes.Buffer
	logger := x.NewSlogLogger(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	logger.Debugw("debug", "key", 1)
	logger.Infow("info", "key", 2)
	logger.Warnw("warn", "key", 3)
	logger.Errorw("error", "key", 4)
	for _, word := range []string{"debug", "info", "warn", "error"} {
		if !strings.Contains(buffer.String(), "msg="+word) {
			t.Fatal(buffer.String())
		}
	}
	n := x.NewSlogLogger(nil)
	n.Debugw("x")
	n.Infow("x")
	n.Warnw("x")
	n.Errorw("x")
	var typed *nilLogger
	for _, l := range []x.Logger{nil, typed, logger} {
		m := makeMigrator(t, fake(t), validFiles(), x.WithLogger[*fakeDriver](l))
		if _, e := m.Up(t.Context(), 0); e != nil {
			t.Fatal(e)
		}
	}
	if strings.Contains(buffer.String(), "SELECT") || strings.Contains(buffer.String(), "password") {
		t.Fatal("sensitive log")
	}
}

type recordingLogger struct{ commits, unknowns, failures int }

func (*recordingLogger) Debugw(string, ...any) {}
func (l *recordingLogger) Infow(message string, _ ...any) {
	if message == "migration committed" {
		l.commits++
	}
}
func (*recordingLogger) Warnw(string, ...any) {}
func (l *recordingLogger) Errorw(_ string, fields ...any) {
	l.failures++
	for i := 0; i < len(fields)-1; i += 2 {
		if fields[i] == "error_kind" && fields[i+1] == "commit_outcome_unknown" {
			l.unknowns++
		}
	}
}

func TestLoggerUsesConfirmedOutcome(t *testing.T) {
	d := fake(t)
	l := &recordingLogger{}
	m := makeMigrator(t, d, validFiles(), x.WithLogger[*fakeDriver](l))
	d.outcome = x.TxUnknown
	m.Up(t.Context(), 0)
	if l.commits != 0 || l.unknowns != 1 || l.failures != 1 {
		t.Fatalf("unknown commit logged as success: %+v", l)
	}
	d.outcome = x.TxCommitted
	d.fail = "cleanup"
	m.Up(t.Context(), 0)
	if l.commits != 1 || l.failures != 2 {
		t.Fatalf("confirmed cleanup error lost commit: %+v", l)
	}
}

func TestPanicIsNotLoggedAsSuccessfulRun(t *testing.T) {
	d := fake(t)
	files := validFiles()
	files["1_m1.up.sql"].Data = []byte("-- xmigrator:hooks=panic\nSELECT 1;")
	l := &recordingLogger{}
	m := makeMigrator(t, d, files, x.WithLogger[*fakeDriver](l), x.WithHooks(map[x.Version]x.Hooks[*fakeDriver]{1: {UpRevision: "panic", AfterUp: func(context.Context, *fakeDriver) error { panic("fixture panic") }}}))
	func() {
		defer func() {
			if v := recover(); v != "fixture panic" {
				t.Fatalf("panic value %v", v)
			}
		}()
		m.Up(t.Context(), 0)
	}()
	if l.commits != 0 || l.failures != 1 {
		t.Fatalf("panic events %+v", l)
	}
}
