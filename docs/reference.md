# Reference

Full details of the `rules.yaml` format and command behavior. For installation and a quick start, see the main [README](../README.md).

## Contents

- [rules.yaml structure](#rulesyaml-structure)
- [Presets (`preset:`)](#presets-preset)
- [Timestamp `format`](#timestamp-format)
- [`format: auto`](#format-auto)
- [Field value transforms (`replace:` / `normalize:`)](#field-value-transforms-replace--normalize)
- [Partial structured-data parsing (`structured:` / `key:` / `extra:`)](#partial-structured-data-parsing-structured--key--extra)
- [Sensitive data masking (`mask:`)](#sensitive-data-masking-mask)
- [Source location metadata (`meta:`)](#source-location-metadata-meta)
- [Compression settings](#compression-settings)
- [Row group size](#row-group-size)
- [Merge order across multiple input files](#merge-order-across-multiple-input-files)
- [Multi-line log entries (`continuation`)](#multi-line-log-entries-continuation)
- [Compressed input auto-detection](#compressed-input-auto-detection)
- [`dump` / `restore` text format](#dump--restore-text-format)
- [`expand` / `collapse`](#expand--collapse)

## rules.yaml structure

```yaml
compression:            # optional, see "Compression settings"
  codec: zstd
  level: 2

row_group:               # optional, see "Row group size"
  max_rows: 500000

rules:
  - name: access_log      # output file becomes <name>.parquet
    pattern: '^(?P<remote_addr>\S+) ...$'
    continuation: '...'   # optional, see "Multi-line log entries"
    structured:            # optional, see "Partial structured-data parsing"
      source: json
      format: json
    fields:
      remote_addr: string
      status: int
      time:
        type: timestamp
        format: clf
```

- Each rule's `pattern` is matched against a log line in declaration order; the first rule whose pattern matches (and whose fields convert successfully) wins.
- The output Parquet column order follows the order fields are listed under `fields:` (not alphabetical). If the same rule `name` is used in multiple rules, every field's name, type, *and* order must match exactly across them, since they share one output file.
- After each output file is written, logidx logs the row count, compressed/uncompressed byte size, and compression ratio (`msg="output parquet file"`).
- Lines that don't match any rule are written to `unmatched.txt` in the output directory, as `<source-file>\t<line-number>\t<raw-line>` (tab-separated). The source file column exists because multiple input files are merged into shared output, so the line number alone wouldn't identify which file a line came from.

## Presets (`preset:`)

Common log formats (Apache/nginx Common Log Format and Combined Log Format, BSD syslog / RFC 3164, and syslog protocol / RFC 5424) can be used with a single `preset:` line instead of writing out `pattern:`/`fields:` by hand:

```yaml
rules:
  - name: access_log
    preset: apache_clf
  - name: syslog
    preset: syslog_rfc3164
```

- `preset:` cannot be combined with `pattern:`/`fields:` on the same rule (startup error). An unknown preset name is also a startup error.
- Presets are fixed as-is; there's no partial customization (e.g. overriding just one field's `format`). To customize, copy the `pattern`/`fields` shown below into your own rule and edit from there.
- A preset can be freely combined with `continuation:`/`compression:` etc. It cannot be combined with `structured:` at the rule level, though — `structured:` needs fields with `key:`/`extra:` set, and those can't be declared when `preset:` already owns `fields:`. If part of a log line (not the whole line) is in a preset format, see [structured.format as a preset name](#structuredformat-as-a-preset-name) instead.

Available presets:

| Preset | Format |
|---|---|
| `apache_clf` | Apache/nginx Common Log Format |
| `apache_combined` | Apache/nginx Combined Log Format (CLF + referer/user-agent) |
| `syslog_rfc3164` | BSD syslog (RFC 3164) |
| `syslog_rfc5424` | syslog protocol (RFC 5424) |

#### `apache_clf`

```yaml
pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
fields:
  remote_addr: string
  remote_user: string
  time:
    type: timestamp
    format: clf
  method: string
  path: string
  proto: string
  status: int
  bytes: int
```

#### `apache_combined`

```yaml
pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+) "(?P<referer>[^"]*)" "(?P<user_agent>[^"]*)"$'
fields:
  remote_addr: string
  remote_user: string
  time:
    type: timestamp
    format: clf
  method: string
  path: string
  proto: string
  status: int
  bytes: int
  referer: string
  user_agent: string
```

#### `syslog_rfc3164`

The `[pid]` part of `tag[pid]:` is omitted by many daemons, so `pid` is `string` (empty string when absent).

```yaml
pattern: '^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<tag>[^:\[\s]+)(?:\[(?P<pid>\d+)\])?: (?P<message>.*)$'
fields:
  time:
    type: timestamp
    format: syslog
  host: string
  tag: string
  pid: string
  message: string
```

#### `syslog_rfc5424`

`procid`/`msgid`/STRUCTURED-DATA (`sd`) can hold the RFC's "no value" marker `-`, so they're `string`. `sd` is stored as raw text without being parsed (to extract keys from structured data, see [structured.format as a preset name](#structuredformat-as-a-preset-name)).

```yaml
pattern: '^<(?P<pri>\d+)>(?P<version>\d+) (?P<time>\S+) (?P<host>\S+) (?P<app>\S+) (?P<procid>\S+) (?P<msgid>\S+) (?P<sd>-|(?:\[[^\]]*\])+) (?P<message>.*)$'
fields:
  pri: int
  version: int
  time:
    type: timestamp
    format: iso8601
  host: string
  app: string
  procid: string
  msgid: string
  sd: string
  message: string
```

## Timestamp `format`

A `timestamp`-typed field's `format` can be written in one of four ways. logidx infers which one you mean from the value itself, so there's no separate key to declare it:

0. **`auto`** (see below) — tries several candidate layouts in order instead of committing to one.
1. **a preset name** (table below)
2. **strptime notation** (a string starting with `%`; only the directives listed below are supported)
3. **a raw Go layout string** (anything not matching the above is passed straight through to Go's `time.Parse`; this is how pre-existing `rules.yaml` files keep working unchanged)

Presets:

| Preset | Meaning | Notes |
|---|---|---|
| `iso8601` / `rfc3339` | `2006-01-02T15:04:05.999999999Z07:00` | Fractional seconds are optional (aliases of each other) |
| `rfc822` | `02 Jan 06 15:04 -0700` | Numeric timezone offset |
| `rfc2822` | `Mon, 02 Jan 2006 15:04:05 -0700` | Same shape as an email `Date:` header |
| `clf` | `02/Jan/2006:15:04:05 -0700` | Apache/nginx Common Log Format |
| `syslog` | `Jan _2 15:04:05` | No year. Traditional BSD syslog format (space-padded, e.g. `Aug  7`) |
| `pylog` | `2006-01-02 15:04:05,999999999` | Python logging's default `%(asctime)s` format (comma-separated fractional seconds) |
| `unix` | epoch seconds (integer or float) | |
| `unix_ms` / `unix_us` / `unix_ns` | epoch milli-/micro-/nanoseconds (integer) | |

strptime directive table:

| Directive | Meaning | Go token |
|---|---|---|
| `%Y` / `%y` | year (4-digit / 2-digit) | `2006` / `06` |
| `%m` | month (2-digit) | `01` |
| `%d` | day (2-digit) | `02` |
| `%H` / `%I` | hour (24h / 12h) | `15` / `03` |
| `%M` | minute | `04` |
| `%S` | second | `05` |
| `%f` | fractional seconds (variable width, accepts either `.` or `,`) | `999999999` |
| `%z` | UTC offset | `-0700` |
| `%Z` | timezone name | `MST` |
| `%a` / `%A` | weekday (abbreviated / full) | `Mon` / `Monday` |
| `%b` / `%B` | month name (abbreviated / full) | `Jan` / `January` |
| `%p` | AM/PM | `PM` |
| `%%` | literal `%` | `%` |

A directive not in this table (`%j`, `%U`, etc.) is a startup error. A string that starts with something other than `%` and doesn't match a preset name falls through to the raw Go layout case (no validation happens up front — an invalid layout only surfaces once it's used to parse an actual value, same as before this feature existed).

Year-less presets/strptime formats (e.g. `syslog`) use the existing year-completion logic: the year closest to (but not after) the current time is assumed.

## `format: auto`

When you don't know the format ahead of time, or a log mixes formats, `format: auto` tries the following six layout presets **in this order** and uses the first one that parses without error:

| Order | Preset |
|---|---|
| 1 | `iso8601` (= `rfc3339`) |
| 2 | `rfc2822` |
| 3 | `rfc822` |
| 4 | `clf` |
| 5 | `syslog` |
| 6 | `pylog` |

Once a format succeeds for a field, it's tried first on subsequent lines for that field, so in practice the overhead is a single extra attempt per run as long as the format doesn't actually change line to line.

`unix`/`unix_ms`/`unix_us`/`unix_ns` (epoch-based formats) are **not** candidates for `auto`: numeric strings parse successfully under any epoch unit, and the digit count alone can't disambiguate the unit, so mixing them with layout-based presets would risk misdetection. Use an explicit preset name if you need epoch timestamps. strptime notation and raw Go layout strings are also not `auto` candidates.

When a year-less preset (`syslog`) matches via `auto`, the same year-completion logic described above still applies.

## Field value transforms (`replace:` / `normalize:`)

Each field under `fields:` can have `replace:` and `normalize:`. These are distinct concepts, always applied in this order: `replace` → `normalize`.

- **`replace:`** substitutes parts of the value using a regular expression. Rules apply in declaration order, each one's output feeding the next (a chain). `value: ''` deletes the matched substring. Capture-group backreferences like `$1` work too (this is Go's standard `regexp.ReplaceAllString` behavior, no extra implementation needed). If you need a literal `$` in `value:`, escape it as `$$` (`regexp.ReplaceAllString` always treats an unescaped `$` as the start of a capture-group expansion, so e.g. `value: '$USD'` would try to expand a nonexistent group named `USD` and produce an empty string instead). Good for stripping noise embedded in a value, like octal control-character escapes (`#015`) or ANSI color escape sequences (`\x1b[31m`).
- **`normalize:`** replaces the **entire** value with the first matching rule's fixed value, as soon as the pattern matches **anywhere** in the value (partial match, via `regexp.MatchString`). It cannot preserve part of the original value. In the example below, `(?i)^warn(ing)?$` → `WARN` looks like "only replaces on a full match" — but that's because the pattern itself is anchored with `^`/`$`, not an inherent property of `normalize:`. If you want to match only part of the pattern, anchor explicitly with `^`/`$` — without anchors, the pattern merely appearing somewhere in the value replaces the whole thing.

```yaml
fields:
  message:
    type: string
    replace:
      - pattern: '#\d{3}'
        value: ''
      - pattern: '\x1b\[[0-9;]*m'
        value: ''
    normalize:
      - pattern: '(?i)^warn(ing)?$'
        value: WARN
```

In this example, `message` first has `#\d{3}` (octal control-character escapes) and ANSI color escape sequences stripped via `replace`, and `normalize`'s pattern matching then runs against that cleaned-up value.

## Partial structured-data parsing (`structured:` / `key:` / `extra:`)

For log lines where part of the line is JSON/LTSV/logfmt (e.g. a container log line ending in a JSON payload), the raw text captured by a named group in `pattern` can be parsed again by key and mapped into fields:

```yaml
rules:
  - name: container_log
    pattern: '^(?P<time>\S+) (?P<host>\S+) (?P<tag>\S+) (?P<json>\{.*\})$'
    structured:
      source: json
      format: json
    fields:
      time:
        type: timestamp
        format: iso8601
      host: string
      tag: string
      level:
        type: string
        key: level
      event_time:
        type: timestamp
        format: iso8601
        key: time
      message:
        type: string
        key: msg
      extra:
        type: string
        extra: true
```

- `structured.source` is the name of the named capture group holding the structured data (`json` in the example above). `structured.format` is `json`/`ltsv`/`logfmt`, or a preset name (see [structured.format as a preset name](#structuredformat-as-a-preset-name)). At most one `structured:` per rule.
- Setting `key:` on a field under `fields:` pulls that key's value from the structured data. The field name doesn't need to match the key name (`event_time` above reads the JSON's `time` key, which is unrelated to the top-level `time` field captured from the line prefix).
- A field with `extra: true` collects every structured-data key *not* consumed by a `key:` field into a JSON string. At most one `extra:` field per rule.
- A field with neither `key:` nor `extra:` set behaves as before, pulling its value from the same-named capture group in `pattern` (existing rules keep working unchanged).
- The capture group named by `structured.source` doesn't need to be listed under `fields:`. If you want to keep the raw structured-data text as its own column, just add it as a normal field without `key:` (this is fine alongside the mapped fields, as `json` is in the example above).
- **The `extra:` column preserves the original JSON types when `structured.format` is `json`**: an unmapped `signal` key that was originally the number `15` stays `{"signal":15}` in the extra column (not stringified). Nested objects/arrays stay nested JSON too (e.g. `{"listen":{"IP":"::","Port":3000}}` — never double-encoded as a broken JSON string). `ltsv`/`logfmt`/preset formats are all-string formats to begin with, so their extra columns are string-valued too (e.g. `{"status":"200"}`).
- A value pulled out via `key:` is converted according to the field's `type:` as usual (with `type: string`, JSON numbers/booleans/nested objects-arrays are all stored as strings, with nested values compacted to a JSON string; there's no way to address a nested key path individually).
- A structured-data parse failure (malformed JSON, top-level JSON that isn't an object, an empty string, etc.) is treated the same as an existing "type conversion failure". A `key:` referencing a key that isn't present in the actual log line is treated the same way, regardless of the field's type (previously, a missing key silently became Go's zero value `""`, which a `string` field would happily accept; now a missing key is itself detected as an error).
- When a rule's `pattern` matches but a later field conversion fails (type conversion failure, structured-data parse failure, or a missing `key:`, as above), the line isn't immediately treated as unmatched — the next candidate rule in declaration order is tried instead. Only a line that fails conversion under every candidate rule ends up in `unmatched.txt`. A `continuation` entry (see [Multi-line log entries](#multi-line-log-entries-continuation)) that fails type conversion at finalize time falls back the same way: only its first line is retried against the next candidate rule, and lines from the second line onward are reset completely and re-matched independently, one line at a time (there's no replay of the candidate rule's continuation pattern consuming the same number of lines). So a failed entry's later lines can end up matching a different rule, or become separate `unmatched` records.

#### `structured.format` as a preset name

`structured.format` also accepts the preset names usable with `preset:` (`apache_clf`/`apache_combined`/`syslog_rfc3164`/`syslog_rfc5424`), in addition to `json`/`ltsv`/`logfmt`. Useful when only part of a log line — not the whole line — is in a preset format (e.g. a syslog-forwarded container log ending in a CLF-style access log).

```yaml
rules:
  - name: docker_apprise_access
    pattern: '^(?P<ts>\S+) (?P<host>\S+) (?P<tag>[^\[]+)\[(?P<pid>\d+)\] (?P<access>.*)$'
    structured:
      source: access
      format: apache_clf
    fields:
      ts:
        type: timestamp
        format: iso8601
      host: string
      tag: string
      pid: string
      remote_addr:
        type: string
        key: remote_addr
      method:
        type: string
        key: method
      path:
        type: string
        key: path
      status:
        type: int
        key: status
      access_time:
        type: timestamp
        format: clf
        key: time
      extra:
        type: string
        extra: true
```

- The names usable in `key:` are the field names listed in that preset's own `fields:` definition (`apache_clf`: `remote_addr`/`remote_user`/`time`/`method`/`path`/`proto`/`status`/`bytes`; `apache_combined`: the same 8 plus `referer`/`user_agent`, 10 total; `syslog_rfc3164`: `time`/`host`/`tag`/`pid`/`message`; `syslog_rfc5424`: `pri`/`version`/`time`/`host`/`app`/`procid`/`msgid`/`sd`/`message`). As with ordinary `structured:` usage, you can pick any subset of keys and give them whatever field name/type you like (the example above receives `time` under the name `access_time`).
- If the preset's fixed pattern doesn't match the text captured by `structured.source`, it's treated the same as an ordinary structured-data parse failure and written to `unmatched.txt`.
- This is independent of the rule-level `preset:` shortcut (which replaces the whole `pattern`/`fields`); there's no special interaction between the two.

## Sensitive data masking (`mask:`)

`mask:` is a `rules.yaml`-wide, top-level list (like `compression:`/`row_group:`, not nested under any one rule) that redacts or deterministically hashes sensitive values before they reach Parquet or `unmatched.txt`:

```yaml
mask:
  - type: key
    pattern: '(?i)^(password|pwd|secret|api[_-]?key|token)$'
    action: redact
    value: '[MASKED]'
  - type: key
    pattern: '(?i)^(email|user_email)$'
    action: hash
    length: 8
  - type: pattern
    pattern: '[\w.+-]+@[\w.-]+\.\w+'
    action: hash
    length: 8
```

- **`type: key`** matches a **key name** in a rule's `structured:`-parsed JSON/LTSV/logfmt data and replaces that key's *entire value*. For JSON, this recurses into nested objects and arrays at any depth (a `password` key three levels deep is masked just like a top-level one); LTSV/logfmt have no nesting, so only their top-level keys are checked. It fires for both `key:`-mapped fields and whatever lands in an `extra:` column, and does nothing on a rule with no `structured:` block. It does **not** apply to preset structured formats (`structured.format: apache_clf` etc.) — presets have a small, fixed set of key names, so masking them by regex isn't useful the way it is for free-form JSON/LTSV/logfmt.
- **`type: pattern`** matches a substring inside a **`type: string` field's value** (mapped fields and `extra:` alike, since `extra:` is always a JSON string) and replaces just the matched part — the same "keep the rest of the value" idea as `replace:`. It also applies to raw lines written to `unmatched.txt`. It is never applied to `int`/`float`/`timestamp` fields, to avoid turning a valid number into unparsable masked text.
- **`action: redact`** replaces the match with the fixed `value:` string (`value: ''` deletes it, same as `replace:`'s `value: ''`). **`action: hash`** replaces it with the first `length` (1-64) hex characters of its SHA-256 digest — no secret key, so the same input always hashes to the same short value. That's deliberate: values stay hidden, but rows sharing the same original value (e.g. the same user's email) still hash identically, so you can still correlate/group by them.
- Multiple `mask:` entries matching the same key (for `type: key`) or the same value (for `type: pattern`) chain in declaration order — each rule's output feeds the next.
- `mask:` has no per-rule override; it's one global list applied identically everywhere it can fire.

## Source location metadata (`meta:`)

Because `logidx import` merges multiple input files into one Parquet output, matched rows normally don't retain which input file/line they came from (`unmatched.txt` already carries this, in `<source>\t<lineNum>\t<raw>\n` form). Setting `meta:` on a field saves that information as a column:

```yaml
rules:
  - name: access
    pattern: '^(?P<remote>\S+) (?P<msg>.*)$'
    fields:
      remote: string
      msg: string
      log_file:
        type: string
        meta: source_file
      log_line:
        type: int
        meta: source_line
```

- `meta: source_file` requires `type: string`. Its value is the input path the row came from (`-` for stdin, same notation as `unmatched.txt`).
- `meta: source_line` requires `type: int`. Its value is the row's 1-based line number. For a `continuation:` rule that merges multiple lines into one entry, it's the entry's first physical line number (not a continuation line's own number).
- The column name is whatever you name the field under `fields:` (not limited to `log_file`/`log_line`).
- `replace:`/`normalize:` apply to `meta` fields just like any other (e.g. a regex to extract just the filename out of a full path).
- `meta:` is opt-in per rule; it's never added automatically. Existing rules keep working unchanged.
- `meta:` and `key:`/`extra:` can't both be set on the same field (a field has exactly one value source).

## Compression settings

The compression codec and level are resolved in this priority order: **CLI flag > `rules.yaml`'s `compression` > default (zstd)**.

In `rules.yaml`:

```yaml
compression:
  codec: gzip
  level: 9

rules:
  - name: app_log
    ...
```

Level range per codec:

| codec | level range | notes |
|---|---|---|
| `zstd` (default) | 1-4 | 1 = fastest .. 4 = highest compression |
| `gzip` | -2-9 | -2 = Huffman-only, -1 = default, 0 = no compression, 9 = highest compression |
| `brotli` | 0-11 | higher = more compression, slower |
| `lz4` | 0-9 | 0 = fast, 1-9 = higher compression |
| `snappy` / `uncompressed` | not settable | specifying a level is an error |

If `--compression` is given without `--compression-level`, and `rules.yaml` has a level set, that level carries over (since a codec change can change what the level means, an out-of-range level is an error).

## Row group size

The Parquet row group row-count limit is resolved in this priority order: **CLI flag (`--max-rows-per-row-group`) > `rules.yaml`'s `row_group.max_rows` > default (unlimited)**.

In `rules.yaml`:

```yaml
row_group:
  max_rows: 500000

rules:
  - name: app_log
    ...
```

Row count is a proxy for compressed byte size, since parquet-go doesn't offer a way to target byte size directly. To aim for a target file size, work backward from the average row size (post-compression) for the rule in question.

## Merge order across multiple input files

When `logidx import` is given multiple input files, and at least one rule has a `type: timestamp` field, logidx merges rows across all input files in ascending order by that rule's first-declared timestamp field, before writing (automatic, no configuration needed).

For rules with no timestamp field (no merge key), rows keep their original order *within* the file that produced them, but if any rule in the config has a merge key, rows from file A and file B can end up interleaved — the old guarantee of "all of file A's rows, then all of file B's" no longer holds. The same applies to `unmatched.txt`: each row's line number within its own source file stays in ascending order, but rows from different input files can be interleaved (previously they were always grouped by file).

This is a streaming merge that assumes each input file is already in chronological order internally; if you pass a file whose lines are out of order, the output order for that file is undefined.

The merge holds every input file open simultaneously (a k-way merge needs a cursor into every file at once for comparison), so the number of input files is bounded by the process's open-file limit (`ulimit -n`; commonly 256–1024). Exceeding it doesn't produce a distinct error — files beyond the limit fail with a plain "open input" error and are dropped from the merge.

## Multi-line log entries (`continuation`)

When a single logical log entry spans multiple lines (e.g. the indented lines that follow macOS syslog's `Configuration Notice:`), setting `continuation` (a regex that detects continuation lines) on a rule appends continuation-line content to the matching field, newline (`\n`)-joined, before the entry is written as one Parquet row.

```yaml
rules:
  - name: syslog
    pattern: '^(?P<time>\w+ +\d+ \d+:\d+:\d+) (?P<host>\S+) (?P<process>\S+): (?P<message>.*)$'
    continuation: '^\s+(?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: syslog
      host: string
      process: string
      message: string
```

- A `continuation` pattern's named capture group(s) identify which field to append to (it must match a field name declared under `fields:`, or it's a startup error). A single continuation pattern can have multiple named captures to append to several fields from the same continuation line at once.
- A continuation pattern with zero named captures is also valid — a matching line is recognized as a continuation of the entry (the entry stays open) but nothing is appended to any field, useful for skipping decorative separator lines.
- The join character is always newline (`\n`), not configurable.
- An entry is finalized (and its fields converted) once a line is hit that doesn't match the continuation pattern, starts a new entry, or reaches end of file. If a line matches both the continuation pattern and a new entry's start pattern, it's treated as a continuation line (continuation takes priority) — so write your continuation pattern narrowly enough that it won't also match the start of a new entry. An overly broad continuation pattern will silently swallow subsequent entries into one (no error or warning).
- An entry stays open, accumulating lines, for as long as the continuation pattern keeps matching, so memory use for that entry grows with its line count in what's normally a streaming, one-line-at-a-time design. An overly broad continuation pattern is a risk here too.
- `replace`/`normalize` are applied once to the final, newline-joined value (not per line). If your regex uses `^`/`$`, keep in mind it's matching against a string that may contain embedded newlines.
- A line that matches the continuation pattern while no entry is currently open (an orphaned continuation line) is written to `unmatched.txt` like any other unmatched line.
- A multi-line entry that fails to finalize (type conversion error) falls back the same way described in [Partial structured-data parsing](#partial-structured-data-parsing-structured--key--extra): only the first line is retried against the next candidate rule, and lines from the second line onward are reprocessed independently, one at a time. If every remaining candidate rule fails too, only that first line is written to `unmatched.txt` (at its original line number).
- A rule without `continuation` behaves as before (one line = one entry).

## Compressed input auto-detection

`logidx import` input files are transparently decompressed based on their extension. No external command (`gzip`, etc.) is needed — it's handled entirely by Go libraries.

| Extension | Format |
|---|---|
| `.gz` | gzip |
| `.xz` | xz |
| `.bz2` | bzip2 |
| `.zst` | zstd |

- Extension matching is case-insensitive (`.GZ` is treated the same as `.gz`).
- Any other extension (including none) is treated as uncompressed — there's no CLI flag to force a format.
- Standard input (`-`) is always treated as uncompressed. To feed compressed data via stdin, decompress it on the calling side first (e.g. `gzip -dc access.log.gz | logidx import --rules rules.yaml -`).
- gzip/xz/zstd headers are validated as soon as the file is opened; a malformed or mismatched file fails immediately and just that file is dropped from the merge (other input files keep processing). bzip2 validation is streaming, so a corrupt file fails partway through reading.

## `dump` / `restore` text format

```
logidx dump src.parquet dst.txt
logidx restore [--compression <codec>] [--compression-level <n>] dst.txt restored.parquet
```

`dump` converts a Parquet file to a text (JSON Lines) format: line 1 is a header recording the schema and compression settings, and each line after that is one record as a JSON object:

```jsonl
{"columns":[{"name":"level","type":"string"},{"name":"message","type":"string"},{"name":"ts","type":"timestamp"}],"compression":{"codec":"gzip"}}
{"level":"INFO","message":"hello world","ts":"2026-08-07T03:34:56Z"}
{"level":"WARN","message":"careful now","ts":"2026-08-07T03:35:10Z"}
```

- `type` uses the same vocabulary as `rules.yaml` field types (`string`/`int`/`float`/`timestamp`).
- `timestamp` columns are written as RFC3339Nano strings (UTC). Internally they're stored as microsecond-precision int64, so no precision is lost (restoring produces the exact same value).
- The header is always line 1 (there's no reserved-key sniffing).

Passing `-` as `dst.txt` writes to stdout. In that case, the `dumped N rows: ...` completion message goes to stderr instead of stdout, so stdout can be piped straight into `jq` or `logidx restore -` with nothing but dump content in it.

`restore` rebuilds a Parquet file from a dump file, using the schema recorded in its header. It prints row count and compressed/uncompressed byte size and ratio to stdout on completion.

- If `--compression` is omitted, the codec recorded in the header carries over (same default behavior as `cat`).
- If `--compression-level` is omitted, the codec's default level is used.
- Passing `-` as `dump.txt` reads from stdin (e.g. `logidx dump src.parquet - | logidx restore - dst.parquet`).

Parquet files themselves (`src.parquet`/`dst.parquet`, etc.) always need a real file path (no stdin/stdout support). Only the text-format log/dump I/O supports `-` (`import`'s input log files, `dump`'s destination, `restore`'s source).

## `expand` / `collapse`

```
logidx expand   [--log-format text|json] [-v|--verbose] <src.yaml> <dst.yaml>
logidx collapse [--log-format text|json] [-v|--verbose] <src.yaml> <dst.yaml>
```

`expand` rewrites a rule's `preset: <name>` into the `pattern:`/`fields:` that preset expands to. Useful for inspecting what a preset contains, or as a starting point for a partial customization (since presets only support all-or-nothing use as a non-goal — the intended workflow is `expand`, then hand-edit).

`collapse` does the reverse: if a rule's `pattern:`/`fields:` exactly match a preset's definition (after normalizing regex notation differences), it's rewritten to a single `preset: <name>` line. Useful for compacting a hand-written pattern that happens to match a preset.

- Both commands leave everything outside the converted parts (comments, key order, indentation, other rules) untouched.
- `src`/`dst` follow the same convention as `dump`/`restore`: `-` for `src` reads from stdin, `-` for `dst` writes to stdout.
- On completion, the number of converted rules is logged (`expanded rules count=N` / `collapsed rules count=N`). Zero matches is not an error.
- `expand` aborts with an error if a rule references an unknown preset name.
- `collapse` simply skips a rule that doesn't match any preset; this is normally not an error.
- There's no in-place edit flag — pass the same path for `<src>` and `<dst>` to get the same effect.
