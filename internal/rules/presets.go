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
