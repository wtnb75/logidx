package convert

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/logging"
	"logidx/internal/rules"
	"logidx/internal/schema"
)

const specExampleRulesYAML = `
rules:
  - name: nginx_access
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: "02/Jan/2006:15:04:05 -0700"
      method: string
      path: string
      proto: string
      status: int
      bytes: int

  - name: app_log
    pattern: '^(?P<time>\S+) \[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: "2006-01-02T15:04:05Z07:00"
      level: string
      message: string
`

const specExampleLog = `192.168.1.1 - - [06/Aug/2026:12:00:00 +0900] "GET /index.html HTTP/1.1" 200 512
2026-08-06T12:00:01+09:00 [INFO] user logged in
this is a garbled line that matches nothing
192.168.1.2 - - [06/Aug/2026:12:00:02 +0900] "GET /api HTTP/1.1" 200 128
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestFile_SpecExample_ProducesExpectedOutputs(t *testing.T) {
	dir := t.TempDir()
	rulesPath := writeFile(t, dir, "rules.yaml", specExampleRulesYAML)
	logPath := writeFile(t, dir, "access.log", specExampleLog)
	outDir := filepath.Join(dir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	cfg, err := rules.Load(rulesPath)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "text", false)

	if err := File(logPath, outDir, cfg, logger); err != nil {
		t.Fatalf("File: %v", err)
	}

	built, err := schema.BuildAll(cfg.Rules)
	if err != nil {
		t.Fatalf("schema.BuildAll: %v", err)
	}

	nginxPath := filepath.Join(outDir, "access.nginx_access.parquet")
	if countParquetRows(t, nginxPath, built["nginx_access"].Schema) != 2 {
		t.Errorf("expected 2 rows in %s", nginxPath)
	}

	appPath := filepath.Join(outDir, "access.app_log.parquet")
	if countParquetRows(t, appPath, built["app_log"].Schema) != 1 {
		t.Errorf("expected 1 row in %s", appPath)
	}

	unmatchedContent, err := os.ReadFile(filepath.Join(outDir, "access.unmatched.txt"))
	if err != nil {
		t.Fatalf("read unmatched file: %v", err)
	}
	want := "3\tthis is a garbled line that matches nothing\n"
	if string(unmatchedContent) != want {
		t.Errorf("got %q, want %q", string(unmatchedContent), want)
	}
}

func countParquetRows(t *testing.T, path string, sch *parquet.Schema) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	reader := parquet.NewGenericReader[map[string]any](f, sch)
	defer reader.Close()

	total := 0
	buf := make([]map[string]any, 8)
	for i := range buf {
		buf[i] = map[string]any{}
	}
	for {
		n, err := reader.Read(buf)
		total += n
		if err != nil {
			break
		}
	}
	return total
}
