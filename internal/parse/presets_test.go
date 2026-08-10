package parse

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"logidx/internal/rules"
)

func writeTempPresetRules(t *testing.T, ruleName, preset string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	content := "rules:\n  - name: " + ruleName + "\n    preset: " + preset + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp rules file: %v", err)
	}
	return path
}

func TestPresets_MatchAndConvertSampleLines(t *testing.T) {
	cases := []struct {
		name   string
		preset string
		line   string
		now    time.Time
		want   map[string]any
	}{
		{
			name:   "apache_clf",
			preset: "apache_clf",
			line:   `127.0.0.1 - frank [10/Oct/2023:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 200 2326`,
			now:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"remote_addr": "127.0.0.1",
				"remote_user": "frank",
				"time":        time.Date(2023, 10, 10, 13, 55, 36, 0, time.FixedZone("", -7*3600)),
				"method":      "GET",
				"path":        "/apache_pb.gif",
				"proto":       "HTTP/1.0",
				"status":      int64(200),
				"bytes":       int64(2326),
			},
		},
		{
			name:   "apache_combined",
			preset: "apache_combined",
			line:   `127.0.0.1 - frank [10/Oct/2023:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 200 2326 "http://www.example.com/start.html" "Mozilla/4.08 [en] (Win98; I ;Nav)"`,
			now:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"remote_addr": "127.0.0.1",
				"remote_user": "frank",
				"time":        time.Date(2023, 10, 10, 13, 55, 36, 0, time.FixedZone("", -7*3600)),
				"method":      "GET",
				"path":        "/apache_pb.gif",
				"proto":       "HTTP/1.0",
				"status":      int64(200),
				"bytes":       int64(2326),
				"referer":     "http://www.example.com/start.html",
				"user_agent":  "Mozilla/4.08 [en] (Win98; I ;Nav)",
			},
		},
		{
			name:   "syslog_rfc3164_with_pid",
			preset: "syslog_rfc3164",
			line:   `Oct 11 22:14:15 mymachine su[1234]: 'su root' failed for lonvick on /dev/pts/8`,
			now:    time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"time":    time.Date(2023, 10, 11, 22, 14, 15, 0, time.UTC),
				"host":    "mymachine",
				"tag":     "su",
				"pid":     "1234",
				"message": "'su root' failed for lonvick on /dev/pts/8",
			},
		},
		{
			name:   "syslog_rfc3164_without_pid",
			preset: "syslog_rfc3164",
			line:   `Oct 11 22:14:15 mymachine su: 'su root' failed for lonvick on /dev/pts/8`,
			now:    time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"time":    time.Date(2023, 10, 11, 22, 14, 15, 0, time.UTC),
				"host":    "mymachine",
				"tag":     "su",
				"pid":     "",
				"message": "'su root' failed for lonvick on /dev/pts/8",
			},
		},
		{
			name:   "syslog_rfc5424_with_structured_data",
			preset: "syslog_rfc5424",
			line:   `<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut="3" eventSource="Application" eventID="1011"] An application event log entry`,
			now:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"pri":     int64(165),
				"version": int64(1),
				"time":    time.Date(2003, 10, 11, 22, 14, 15, 3000000, time.UTC),
				"host":    "mymachine.example.com",
				"app":     "evntslog",
				"procid":  "-",
				"msgid":   "ID47",
				"sd":      `[exampleSDID@32473 iut="3" eventSource="Application" eventID="1011"]`,
				"message": "An application event log entry",
			},
		},
		{
			name:   "syslog_rfc5424_without_structured_data",
			preset: "syslog_rfc5424",
			line:   `<13>1 2023-10-11T22:14:15Z host1 myapp - - - Simple message here`,
			now:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"pri":     int64(13),
				"version": int64(1),
				"time":    time.Date(2023, 10, 11, 22, 14, 15, 0, time.UTC),
				"host":    "host1",
				"app":     "myapp",
				"procid":  "-",
				"msgid":   "-",
				"sd":      "-",
				"message": "Simple message here",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempPresetRules(t, tc.name, tc.preset)
			cfg, err := rules.Load(path)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}

			rule, _, values, _, ok := MatchAndConvert(cfg.Rules, tc.line, tc.now)
			if !ok {
				t.Fatalf("expected line to match preset %q, got no match: %q", tc.preset, tc.line)
			}
			if rule.Name != tc.name {
				t.Errorf("matched rule name = %q, want %q", rule.Name, tc.name)
			}

			for field, want := range tc.want {
				got, present := values[field]
				if !present {
					t.Errorf("field %q missing from converted values", field)
					continue
				}
				if wantTime, isTime := want.(time.Time); isTime {
					gotTime, ok := got.(time.Time)
					if !ok || !gotTime.Equal(wantTime) {
						t.Errorf("field %q = %v, want %v", field, got, wantTime)
					}
					continue
				}
				if got != want {
					t.Errorf("field %q = %v (%T), want %v (%T)", field, got, got, want, want)
				}
			}
		})
	}
}
