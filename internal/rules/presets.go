package rules

// presetDefinition holds the fixed Pattern/Fields a named preset expands
// into. See the design doc for the exact source of each definition.
type presetDefinition struct {
	Pattern string
	Fields  []Field
}

// presetRegistry maps a Rule.Preset name to its fixed definition. Looked up
// by Load (to expand Pattern/Fields before compiling) and by Validate (to
// reject unknown preset names). Presets are intentionally all-or-nothing:
// there is no partial-override mechanism - see the design doc's Non-goals.
var presetRegistry = map[string]presetDefinition{
	"apache_clf": {
		Pattern: `^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$`,
		Fields: []Field{
			{Name: "remote_addr", Type: "string"},
			{Name: "remote_user", Type: "string"},
			{Name: "time", Type: "timestamp", Format: "clf"},
			{Name: "method", Type: "string"},
			{Name: "path", Type: "string"},
			{Name: "proto", Type: "string"},
			{Name: "status", Type: "int"},
			{Name: "bytes", Type: "int"},
		},
	},
	"apache_combined": {
		Pattern: `^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+) "(?P<referer>[^"]*)" "(?P<user_agent>[^"]*)"$`,
		Fields: []Field{
			{Name: "remote_addr", Type: "string"},
			{Name: "remote_user", Type: "string"},
			{Name: "time", Type: "timestamp", Format: "clf"},
			{Name: "method", Type: "string"},
			{Name: "path", Type: "string"},
			{Name: "proto", Type: "string"},
			{Name: "status", Type: "int"},
			{Name: "bytes", Type: "int"},
			{Name: "referer", Type: "string"},
			{Name: "user_agent", Type: "string"},
		},
	},
	"syslog_rfc3164": {
		// pid is deliberately `string`, not `int`: many daemons omit the
		// `[pid]` suffix, and an int field would fail type conversion on
		// every such line, sending it to unmatched.txt.
		Pattern: `^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<tag>[^:\[\s]+)(?:\[(?P<pid>\d+)\])?: (?P<message>.*)$`,
		Fields: []Field{
			{Name: "time", Type: "timestamp", Format: "syslog"},
			{Name: "host", Type: "string"},
			{Name: "tag", Type: "string"},
			{Name: "pid", Type: "string"},
			{Name: "message", Type: "string"},
		},
	},
	// ufwPrefix is the common lead-in shared by ufw_tcp/ufw_udp/ufw_icmp/
	// ufw_other: the syslog-forwarded kernel line up through PROTO=. mac is
	// optional (absent on interfaces with no L2 header, e.g. IN=lo); DF is
	// optional (only set when the packet's don't-fragment bit is set).
	"ufw_tcp": {
		Pattern: `^(?P<time>\S+) (?P<host>\S+) kernel: \[[\d.]+\] \[UFW (?P<action>[A-Z ]+)\] IN=(?P<in>\S*) OUT=(?P<out>\S*)(?: MAC=(?P<mac>\S*))? SRC=(?P<src>\S+) DST=(?P<dst>\S+) LEN=(?P<len>\d+) TOS=(?P<tos>\S+) PREC=(?P<prec>\S+) TTL=(?P<ttl>\d+) ID=(?P<id>\d+)(?: DF)? PROTO=(?P<proto>TCP) SPT=(?P<sport>\d+) DPT=(?P<dport>\d+) WINDOW=(?P<window>\d+) RES=(?P<res>\S+) (?P<flags>[A-Z]+(?: [A-Z]+)*) URGP=(?P<urgp>\d+)$`,
		Fields: []Field{
			{Name: "time", Type: "timestamp", Format: "iso8601"},
			{Name: "host", Type: "string"},
			{Name: "action", Type: "string"},
			{Name: "in", Type: "string"},
			{Name: "out", Type: "string"},
			{Name: "mac", Type: "string"},
			{Name: "src", Type: "string"},
			{Name: "dst", Type: "string"},
			{Name: "len", Type: "int"},
			{Name: "tos", Type: "string"},
			{Name: "prec", Type: "string"},
			{Name: "ttl", Type: "int"},
			{Name: "id", Type: "int"},
			{Name: "proto", Type: "string"},
			{Name: "sport", Type: "int"},
			{Name: "dport", Type: "int"},
			{Name: "window", Type: "int"},
			{Name: "res", Type: "string"},
			{Name: "flags", Type: "string"},
			{Name: "urgp", Type: "int"},
		},
	},
	"ufw_udp": {
		Pattern: `^(?P<time>\S+) (?P<host>\S+) kernel: \[[\d.]+\] \[UFW (?P<action>[A-Z ]+)\] IN=(?P<in>\S*) OUT=(?P<out>\S*)(?: MAC=(?P<mac>\S*))? SRC=(?P<src>\S+) DST=(?P<dst>\S+) LEN=(?P<len>\d+) TOS=(?P<tos>\S+) PREC=(?P<prec>\S+) TTL=(?P<ttl>\d+) ID=(?P<id>\d+)(?: DF)? PROTO=(?P<proto>UDP) SPT=(?P<sport>\d+) DPT=(?P<dport>\d+) LEN=(?P<udp_len>\d+)$`,
		Fields: []Field{
			{Name: "time", Type: "timestamp", Format: "iso8601"},
			{Name: "host", Type: "string"},
			{Name: "action", Type: "string"},
			{Name: "in", Type: "string"},
			{Name: "out", Type: "string"},
			{Name: "mac", Type: "string"},
			{Name: "src", Type: "string"},
			{Name: "dst", Type: "string"},
			{Name: "len", Type: "int"},
			{Name: "tos", Type: "string"},
			{Name: "prec", Type: "string"},
			{Name: "ttl", Type: "int"},
			{Name: "id", Type: "int"},
			{Name: "proto", Type: "string"},
			{Name: "sport", Type: "int"},
			{Name: "dport", Type: "int"},
			{Name: "udp_len", Type: "int"},
		},
	},
	"ufw_icmp": {
		// icmp_id/icmp_seq are `string`, not `int`: ID=/SEQ= are only
		// emitted for echo/timestamp-style ICMP types (e.g. ping), so an
		// int field would fail conversion on every other ICMP type (e.g.
		// destination-unreachable), sending it to unmatched.txt.
		Pattern: `^(?P<time>\S+) (?P<host>\S+) kernel: \[[\d.]+\] \[UFW (?P<action>[A-Z ]+)\] IN=(?P<in>\S*) OUT=(?P<out>\S*)(?: MAC=(?P<mac>\S*))? SRC=(?P<src>\S+) DST=(?P<dst>\S+) LEN=(?P<len>\d+) TOS=(?P<tos>\S+) PREC=(?P<prec>\S+) TTL=(?P<ttl>\d+) ID=(?P<id>\d+)(?: DF)? PROTO=(?P<proto>ICMP) TYPE=(?P<icmp_type>\d+) CODE=(?P<icmp_code>\d+)(?: ID=(?P<icmp_id>\d+) SEQ=(?P<icmp_seq>\d+))?$`,
		Fields: []Field{
			{Name: "time", Type: "timestamp", Format: "iso8601"},
			{Name: "host", Type: "string"},
			{Name: "action", Type: "string"},
			{Name: "in", Type: "string"},
			{Name: "out", Type: "string"},
			{Name: "mac", Type: "string"},
			{Name: "src", Type: "string"},
			{Name: "dst", Type: "string"},
			{Name: "len", Type: "int"},
			{Name: "tos", Type: "string"},
			{Name: "prec", Type: "string"},
			{Name: "ttl", Type: "int"},
			{Name: "id", Type: "int"},
			{Name: "proto", Type: "string"},
			{Name: "icmp_type", Type: "int"},
			{Name: "icmp_code", Type: "int"},
			{Name: "icmp_id", Type: "string"},
			{Name: "icmp_seq", Type: "string"},
		},
	},
	"ufw_other": {
		// Catch-all for protocols other than TCP/UDP/ICMP (e.g. IGMP, ESP,
		// AH): everything after PROTO=<name> is kept as raw text in extra,
		// since the fields netfilter logs past that point vary per
		// protocol. Rules using this preset should be listed after
		// ufw_tcp/ufw_udp/ufw_icmp, since its PROTO= match is unrestricted
		// and would otherwise shadow the more specific presets.
		Pattern: `^(?P<time>\S+) (?P<host>\S+) kernel: \[[\d.]+\] \[UFW (?P<action>[A-Z ]+)\] IN=(?P<in>\S*) OUT=(?P<out>\S*)(?: MAC=(?P<mac>\S*))? SRC=(?P<src>\S+) DST=(?P<dst>\S+) LEN=(?P<len>\d+) TOS=(?P<tos>\S+) PREC=(?P<prec>\S+) TTL=(?P<ttl>\d+) ID=(?P<id>\d+)(?: DF)? PROTO=(?P<proto>\S+)(?: (?P<extra>.*))?$`,
		Fields: []Field{
			{Name: "time", Type: "timestamp", Format: "iso8601"},
			{Name: "host", Type: "string"},
			{Name: "action", Type: "string"},
			{Name: "in", Type: "string"},
			{Name: "out", Type: "string"},
			{Name: "mac", Type: "string"},
			{Name: "src", Type: "string"},
			{Name: "dst", Type: "string"},
			{Name: "len", Type: "int"},
			{Name: "tos", Type: "string"},
			{Name: "prec", Type: "string"},
			{Name: "ttl", Type: "int"},
			{Name: "id", Type: "int"},
			{Name: "proto", Type: "string"},
			{Name: "extra", Type: "string"},
		},
	},
	"syslog_rfc5424": {
		// procid/msgid are `string`, not `int`: RFC 5424 allows the
		// nilvalue "-" for either. sd (STRUCTURED-DATA) is kept as raw,
		// unparsed text - see the design doc's Non-goals.
		Pattern: `^<(?P<pri>\d+)>(?P<version>\d+) (?P<time>\S+) (?P<host>\S+) (?P<app>\S+) (?P<procid>\S+) (?P<msgid>\S+) (?P<sd>-|(?:\[[^\]]*\])+) (?P<message>.*)$`,
		Fields: []Field{
			{Name: "pri", Type: "int"},
			{Name: "version", Type: "int"},
			{Name: "time", Type: "timestamp", Format: "iso8601"},
			{Name: "host", Type: "string"},
			{Name: "app", Type: "string"},
			{Name: "procid", Type: "string"},
			{Name: "msgid", Type: "string"},
			{Name: "sd", Type: "string"},
			{Name: "message", Type: "string"},
		},
	},
}
