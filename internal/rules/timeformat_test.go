package rules

import (
	"strings"
	"testing"
	"time"
)

func TestResolveFormat_Presets(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		wantLayout string
	}{
		{"iso8601", "iso8601", "2006-01-02T15:04:05.999999999Z07:00"},
		{"rfc3339 alias", "rfc3339", "2006-01-02T15:04:05.999999999Z07:00"},
		{"rfc822", "rfc822", "02 Jan 06 15:04 -0700"},
		{"rfc2822", "rfc2822", "Mon, 02 Jan 2006 15:04:05 -0700"},
		{"clf", "clf", "02/Jan/2006:15:04:05 -0700"},
		{"syslog", "syslog", "Jan _2 15:04:05"},
		{"pylog", "pylog", "2006-01-02 15:04:05,999999999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveFormat(tt.format)
			if err != nil {
				t.Fatalf("ResolveFormat(%q): %v", tt.format, err)
			}
			if got.Layout != tt.wantLayout {
				t.Errorf("Layout = %q, want %q", got.Layout, tt.wantLayout)
			}
			if got.EpochUnit != 0 {
				t.Errorf("EpochUnit = %v, want 0", got.EpochUnit)
			}
		})
	}
}

func TestResolveFormat_EpochPresets(t *testing.T) {
	tests := []struct {
		format string
		want   time.Duration
	}{
		{"unix", time.Second},
		{"unix_ms", time.Millisecond},
		{"unix_us", time.Microsecond},
		{"unix_ns", time.Nanosecond},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			got, err := ResolveFormat(tt.format)
			if err != nil {
				t.Fatalf("ResolveFormat(%q): %v", tt.format, err)
			}
			if got.EpochUnit != tt.want {
				t.Errorf("EpochUnit = %v, want %v", got.EpochUnit, tt.want)
			}
			if got.Layout != "" {
				t.Errorf("Layout = %q, want empty for an epoch format", got.Layout)
			}
		})
	}
}

func TestResolveFormat_Strptime(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		wantLayout string
	}{
		{"date and time", "%Y-%m-%d %H:%M:%S", "2006-01-02 15:04:05"},
		{"with numeric offset", "%Y-%m-%dT%H:%M:%S%z", "2006-01-02T15:04:05-0700"},
		{"comma fractional seconds", "%Y-%m-%d %H:%M:%S,%f", "2006-01-02 15:04:05,999999999"},
		{"rfc2822-equivalent", "%a, %d %b %Y %H:%M:%S %z", "Mon, 02 Jan 2006 15:04:05 -0700"},
		{"12-hour clock", "%I:%M %p", "03:04 PM"},
		{"literal percent", "%Y%%", "2006%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveFormat(tt.format)
			if err != nil {
				t.Fatalf("ResolveFormat(%q): %v", tt.format, err)
			}
			if got.Layout != tt.wantLayout {
				t.Errorf("Layout = %q, want %q", got.Layout, tt.wantLayout)
			}
			if got.EpochUnit != 0 {
				t.Errorf("EpochUnit = %v, want 0", got.EpochUnit)
			}
		})
	}
}

func TestResolveFormat_StrptimeUnsupportedDirectiveIsError(t *testing.T) {
	_, err := ResolveFormat("%j")
	if err == nil {
		t.Fatal("expected error for unsupported directive %j")
	}
	if !strings.Contains(err.Error(), "%j") {
		t.Errorf("expected error to mention %%j, got: %v", err)
	}
}

func TestResolveFormat_StrptimeTrailingPercentIsError(t *testing.T) {
	_, err := ResolveFormat("%Y-%")
	if err == nil {
		t.Fatal("expected error for trailing %")
	}
}

func TestResolveFormat_RawGoLayoutPassesThroughUnchanged(t *testing.T) {
	format := "02/Jan/2006:15:04:05 -0700"
	got, err := ResolveFormat(format)
	if err != nil {
		t.Fatalf("ResolveFormat(%q): %v", format, err)
	}
	if got.Layout != format {
		t.Errorf("Layout = %q, want %q (unchanged)", got.Layout, format)
	}
	if got.EpochUnit != 0 {
		t.Errorf("EpochUnit = %v, want 0", got.EpochUnit)
	}
}

func TestResolveFormat_EmptyStringIsNotAnError(t *testing.T) {
	got, err := ResolveFormat("")
	if err != nil {
		t.Fatalf(`ResolveFormat(""): %v`, err)
	}
	if got.Layout != "" || got.EpochUnit != 0 {
		t.Errorf("got %+v, want zero value", got)
	}
}

func TestResolveFormat_Auto(t *testing.T) {
	wantCandidates := []string{
		"2006-01-02T15:04:05.999999999Z07:00", // iso8601 / rfc3339
		"Mon, 02 Jan 2006 15:04:05 -0700",     // rfc2822
		"02 Jan 06 15:04 -0700",               // rfc822
		"02/Jan/2006:15:04:05 -0700",          // clf
		"Jan _2 15:04:05",                     // syslog
		"2006-01-02 15:04:05,999999999",       // pylog
	}

	got, err := ResolveFormat("auto")
	if err != nil {
		t.Fatalf(`ResolveFormat("auto"): %v`, err)
	}
	if got.Layout != "" {
		t.Errorf("Layout = %q, want empty for auto", got.Layout)
	}
	if got.EpochUnit != 0 {
		t.Errorf("EpochUnit = %v, want 0 for auto", got.EpochUnit)
	}
	if len(got.Candidates) != len(wantCandidates) {
		t.Fatalf("Candidates = %v, want %v", got.Candidates, wantCandidates)
	}
	for i, want := range wantCandidates {
		if got.Candidates[i] != want {
			t.Errorf("Candidates[%d] = %q, want %q", i, got.Candidates[i], want)
		}
	}
	if got.LastGood == nil {
		t.Fatal("LastGood = nil, want a non-nil pointer")
	}
	if *got.LastGood != 0 {
		t.Errorf("*LastGood = %d, want 0", *got.LastGood)
	}
}

func TestResolveFormat_Auto_EachCallGetsIndependentLastGood(t *testing.T) {
	first, err := ResolveFormat("auto")
	if err != nil {
		t.Fatalf(`ResolveFormat("auto"): %v`, err)
	}
	second, err := ResolveFormat("auto")
	if err != nil {
		t.Fatalf(`ResolveFormat("auto"): %v`, err)
	}

	*first.LastGood = 3
	if *second.LastGood != 0 {
		t.Errorf("mutating first.LastGood affected second.LastGood (got %d) - LastGood must not be shared across ResolveFormat calls", *second.LastGood)
	}
}
