# logidx

logidx is a batch CLI tool that turns mixed, free-form text logs into structured [Parquet](https://parquet.apache.org/) files. You describe how to parse each kind of log line as a regular expression rule in a YAML file, and logidx matches, type-converts, and writes one Parquet file per rule.

- Match log lines by regular expression, with built-in presets for common formats (Apache/nginx access logs, BSD/RFC5424 syslog)
- Convert matched fields to typed columns (`string` / `int` / `float` / `timestamp`), with flexible timestamp parsing
- Merge multiple input files, in chronological order, into one output per rule
- Handle multi-line log entries and embedded JSON/LTSV/logfmt payloads
- Transparently read gzip/xz/bzip2/zstd-compressed input
- Inspect, concatenate, and round-trip Parquet files (`info`, `cat`, `dump`, `restore`)

## Install

**Prebuilt binaries** — download from [GitHub Releases](https://github.com/wtnb75/logidx/releases) (linux/darwin/windows, amd64/arm64; a wasm/wasip1 build is also published).

**Docker** — images are published to `ghcr.io/wtnb75/logidx` (see [Packages](https://github.com/wtnb75/logidx/pkgs/container/logidx) for available tags; `main` tracks the `main` branch, version tags are published on tagged releases):

```sh
docker run --rm ghcr.io/wtnb75/logidx:main version
```

**From source** (requires Go 1.25+):

```sh
go install github.com/wtnb75/logidx/cmd/logidx@latest
```

or, from a checkout:

```sh
git clone https://github.com/wtnb75/logidx.git
cd logidx
go build -o bin/logidx ./cmd/logidx
```

(with [go-task](https://taskfile.dev/), `task build` does the same.)

## Quick start

Write a rules file describing how to parse your logs. This example uses the built-in `apache_clf` preset for access logs:

```yaml
# rules.yaml
rules:
  - name: access_log
    preset: apache_clf
```

Then convert one or more log files:

```sh
logidx import --rules rules.yaml --out ./out access.log
```

This writes `./out/access_log.parquet`. Any line that doesn't match a rule is written to `./out/unmatched.txt` instead of being silently dropped. Pass `-` as an input file to read from stdin, and pass multiple files to merge them into shared output.

Custom formats look like this — `pattern` is a regexp with named capture groups, and `fields` declares each group's output type:

```yaml
rules:
  - name: app_log
    pattern: '^(?P<time>\S+) (?P<level>\S+) (?P<message>.*)$'
    fields:
      time:
        type: timestamp
        format: iso8601
      level: string
      message: string
```

See the [reference](docs/reference.md) for the full `rules.yaml` format: presets, timestamp formats, field value transforms, multi-line entries, embedded structured data, compression, and more.

## Commands

| Command | Purpose |
|---|---|
| `logidx import --rules rules.yaml --out ./out <file\|->...` | Match, convert, and merge logs into Parquet |
| `logidx info <file.parquet>...` | Show schema, compression, and row-count info |
| `logidx cat --output dst.parquet <file.parquet>...` | Concatenate same-schema Parquet files (merges by timestamp if present) |
| `logidx dump src.parquet dst.txt` | Export a Parquet file to JSON Lines |
| `logidx restore dst.txt restored.parquet` | Rebuild a Parquet file from a `dump` file |
| `logidx expand src.yaml dst.yaml` | Expand a rule's `preset:` into its `pattern:`/`fields:` |
| `logidx collapse src.yaml dst.yaml` | Collapse a rule's `pattern:`/`fields:` into `preset:` where it matches one exactly |
| `logidx scaffold` | Print a minimal rules.yaml template to start from |
| `logidx schema` | Print the JSON Schema for rules.yaml (for editor integration) |
| `logidx version` | Print version, commit, and build date |

Run `logidx <command> --help` for the full flag list of any command; see the [reference](docs/reference.md) for detailed behavior.

## Editor integration

`logidx schema` prints a JSON Schema for `rules.yaml` that editors with a yaml-language-server integration (e.g. VS Code's YAML extension) can use for autocompletion and type checking. Point to it from the top of your `rules.yaml`:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/wtnb75/logidx/main/schema/rules.schema.json
```

The schema covers syntax and types only - semantic checks (e.g. a field name matching a named capture group in `pattern`) are still only caught by `logidx import` itself.

## Development

```sh
task test   # go test ./...
task lint   # golangci-lint run ./...
task fmt    # gofmt -l -w .
task build  # go build -o bin/logidx ./cmd/logidx
```

## License

[MIT](LICENSE)
