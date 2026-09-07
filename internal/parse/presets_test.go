package parse

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wtnb75/logidx/internal/rules"
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
		{
			name:   "ufw_tcp_with_mac",
			preset: "ufw_tcp",
			line:   `2026-09-07T20:02:47.748925+09:00 wtnb4 kernel: [147637.897439] [UFW BLOCK] IN=eth0 OUT= MAC=fa:16:3e:cc:e8:bf:94:8e:d3:fd:1e:b7:08:00 SRC=178.23.184.138 DST=160.251.139.20 LEN=60 TOS=0x08 PREC=0x80 TTL=43 ID=26735 PROTO=TCP SPT=53824 DPT=110 WINDOW=64240 RES=0x00 SYN URGP=0`,
			now:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"time":   time.Date(2026, 9, 7, 20, 2, 47, 748925000, time.FixedZone("", 9*3600)),
				"host":   "wtnb4",
				"action": "BLOCK",
				"in":     "eth0",
				"out":    "",
				"mac":    "fa:16:3e:cc:e8:bf:94:8e:d3:fd:1e:b7:08:00",
				"src":    "178.23.184.138",
				"dst":    "160.251.139.20",
				"len":    int64(60),
				"tos":    "0x08",
				"prec":   "0x80",
				"ttl":    int64(43),
				"id":     int64(26735),
				"proto":  "TCP",
				"sport":  int64(53824),
				"dport":  int64(110),
				"window": int64(64240),
				"res":    "0x00",
				"flags":  "SYN",
				"urgp":   int64(0),
			},
		},
		{
			name:   "ufw_tcp_no_mac_with_df_and_multiple_flags",
			preset: "ufw_tcp",
			line:   `2026-09-07T20:02:47.748925+09:00 wtnb4 kernel: [147637.897439] [UFW ALLOW] IN=lo OUT= SRC=127.0.0.1 DST=127.0.0.1 LEN=60 TOS=0x00 PREC=0x00 TTL=64 ID=1 DF PROTO=TCP SPT=80 DPT=54321 WINDOW=502 RES=0x00 ACK PSH URGP=0`,
			now:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"action": "ALLOW",
				"in":     "lo",
				"out":    "",
				"mac":    "",
				"flags":  "ACK PSH",
				"urgp":   int64(0),
			},
		},
		{
			name:   "ufw_udp",
			preset: "ufw_udp",
			line:   `2026-09-07T20:02:47.748925+09:00 wtnb4 kernel: [147637.897439] [UFW BLOCK] IN=eth0 OUT= MAC=fa:16:3e:cc:e8:bf:94:8e:d3:fd:1e:b7:08:00 SRC=203.0.113.5 DST=160.251.139.20 LEN=71 TOS=0x00 PREC=0x00 TTL=51 ID=54321 PROTO=UDP SPT=53124 DPT=53 LEN=51`,
			now:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"proto":   "UDP",
				"len":     int64(71),
				"sport":   int64(53124),
				"dport":   int64(53),
				"udp_len": int64(51),
			},
		},
		{
			name:   "ufw_icmp_with_id_seq",
			preset: "ufw_icmp",
			line:   `2026-09-07T20:02:47.748925+09:00 wtnb4 kernel: [147637.897439] [UFW BLOCK] IN=eth0 OUT= MAC=fa:16:3e:cc:e8:bf:94:8e:d3:fd:1e:b7:08:00 SRC=198.51.100.7 DST=160.251.139.20 LEN=84 TOS=0x00 PREC=0x00 TTL=52 ID=9999 PROTO=ICMP TYPE=8 CODE=0 ID=52820 SEQ=1`,
			now:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"id":        int64(9999),
				"proto":     "ICMP",
				"icmp_type": int64(8),
				"icmp_code": int64(0),
				"icmp_id":   "52820",
				"icmp_seq":  "1",
			},
		},
		{
			name:   "ufw_icmp_without_id_seq",
			preset: "ufw_icmp",
			line:   `2026-09-07T20:02:47.748925+09:00 wtnb4 kernel: [147637.897439] [UFW BLOCK] IN=eth0 OUT= MAC=fa:16:3e:cc:e8:bf:94:8e:d3:fd:1e:b7:08:00 SRC=198.51.100.7 DST=160.251.139.20 LEN=56 TOS=0xc0 PREC=0xc0 TTL=52 ID=10000 PROTO=ICMP TYPE=3 CODE=3`,
			now:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"proto":     "ICMP",
				"icmp_type": int64(3),
				"icmp_code": int64(3),
				"icmp_id":   "",
				"icmp_seq":  "",
			},
		},
		{
			name:   "ufw_other_with_extra",
			preset: "ufw_other",
			line:   `2026-09-07T20:02:47.748925+09:00 wtnb4 kernel: [147637.897439] [UFW BLOCK] IN=eth0 OUT= MAC=fa:16:3e:cc:e8:bf:94:8e:d3:fd:1e:b7:08:00 SRC=192.168.1.10 DST=192.168.1.20 LEN=88 TOS=0x00 PREC=0x00 TTL=64 ID=1 PROTO=ESP SPI=0x12345678`,
			now:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"proto": "ESP",
				"extra": "SPI=0x12345678",
			},
		},
		{
			name:   "ufw_other_without_extra",
			preset: "ufw_other",
			line:   `2026-09-07T20:02:47.748925+09:00 wtnb4 kernel: [147637.897439] [UFW BLOCK] IN=eth0 OUT= MAC=fa:16:3e:cc:e8:bf:94:8e:d3:fd:1e:b7:08:00 SRC=192.168.1.10 DST=224.0.0.1 LEN=32 TOS=0x00 PREC=0x00 TTL=1 ID=1 PROTO=2`,
			now:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: map[string]any{
				"proto": "2",
				"extra": "",
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

			rule, _, values, _, ok := MatchAndConvert(cfg.Rules, tc.line, SourceMeta{}, tc.now, 0, nil)
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
