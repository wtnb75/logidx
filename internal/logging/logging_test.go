package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestNew_TextFormatWritesKeyValueLines(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "text", false)
	logger.Info("file processed", "file", "access.log", "unmatched", 3)

	out := buf.String()
	if !strings.Contains(out, "msg=\"file processed\"") {
		t.Errorf("expected text-formatted msg, got: %s", out)
	}
	if !strings.Contains(out, "file=access.log") {
		t.Errorf("expected file attribute, got: %s", out)
	}
}

func TestNew_JSONFormatWritesJSONLines(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "json", false)
	logger.Info("file processed", "file", "access.log")

	out := buf.String()
	if !strings.Contains(out, `"msg":"file processed"`) {
		t.Errorf("expected JSON-formatted msg, got: %s", out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("expected JSON object, got: %s", out)
	}
}

func TestNew_VerboseEnablesDebugLevel(t *testing.T) {
	var buf bytes.Buffer
	quiet := New(&buf, "text", false)
	quiet.Debug("should not appear")
	if buf.Len() != 0 {
		t.Errorf("expected no output at Info level for Debug message, got: %s", buf.String())
	}

	buf.Reset()
	verbose := New(&buf, "text", true)
	verbose.Debug("should appear")
	if buf.Len() == 0 {
		t.Error("expected Debug output when verbose=true")
	}
}
