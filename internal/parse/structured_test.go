package parse

import "testing"

func TestParseStructured_JSON_FlatValues(t *testing.T) {
	got, err := ParseStructured("json", `{"level":"INFO","msg":"caught signal"}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["level"] != "INFO" || got["msg"] != "caught signal" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_JSON_NumberKeepsOriginalDigits(t *testing.T) {
	got, err := ParseStructured("json", `{"count":123456789012345678901234567890}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	want := "123456789012345678901234567890"
	if got["count"] != want {
		t.Errorf("count = %q, want %q (float64 rounding would corrupt this)", got["count"], want)
	}
}

func TestParseStructured_JSON_BooleanBecomesTrueFalseString(t *testing.T) {
	got, err := ParseStructured("json", `{"ok":true,"bad":false}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["ok"] != "true" || got["bad"] != "false" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_JSON_NullBecomesEmptyString(t *testing.T) {
	got, err := ParseStructured("json", `{"x":null}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["x"] != "" {
		t.Errorf("x = %q, want empty string", got["x"])
	}
}

func TestParseStructured_JSON_NestedObjectReencodedAsCompactJSON(t *testing.T) {
	got, err := ParseStructured("json", `{"listen":{"IP":"::","Port":3000,"Zone":""}}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	want := `{"IP":"::","Port":3000,"Zone":""}`
	if got["listen"] != want {
		t.Errorf("listen = %q, want %q", got["listen"], want)
	}
}

func TestParseStructured_JSON_ArrayReencodedAsCompactJSON(t *testing.T) {
	got, err := ParseStructured("json", `{"items":[1,"two",true]}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	want := `[1,"two",true]`
	if got["items"] != want {
		t.Errorf("items = %q, want %q", got["items"], want)
	}
}

func TestParseStructured_JSON_DuplicateKeyLastWins(t *testing.T) {
	got, err := ParseStructured("json", `{"a":"first","a":"second"}`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["a"] != "second" {
		t.Errorf("a = %q, want %q (last value should win)", got["a"], "second")
	}
}

func TestParseStructured_JSON_TopLevelArrayIsError(t *testing.T) {
	_, err := ParseStructured("json", `[1,2,3]`)
	if err == nil {
		t.Error("expected error for top-level JSON array")
	}
}

func TestParseStructured_JSON_TopLevelScalarIsError(t *testing.T) {
	_, err := ParseStructured("json", `"just a string"`)
	if err == nil {
		t.Error("expected error for top-level JSON scalar")
	}
}

func TestParseStructured_JSON_TopLevelNullIsError(t *testing.T) {
	// Unmarshaling JSON null into a map target is Go's documented no-op
	// (leaves the map nil, err == nil) - ParseStructured must reject it
	// explicitly since null is not an object.
	_, err := ParseStructured("json", `null`)
	if err == nil {
		t.Error("expected error for top-level JSON null")
	}
}

func TestParseStructured_JSON_TrailingDataIsError(t *testing.T) {
	// json.Decoder.Decode only consumes one JSON value; anything left over
	// (a second value, or plain garbage) must not be silently ignored.
	_, err := ParseStructured("json", `{"a":"b"} garbage`)
	if err == nil {
		t.Error("expected error for trailing data after the top-level JSON value")
	}
}

func TestParseStructured_JSON_InvalidJSONIsError(t *testing.T) {
	_, err := ParseStructured("json", `{not valid`)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestParseStructured_JSON_EmptyInputIsError(t *testing.T) {
	_, err := ParseStructured("json", "")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseStructured_LTSV_TabSeparated(t *testing.T) {
	got, err := ParseStructured("ltsv", "host:example.com\tstatus:200\tmsg:hello world")
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["host"] != "example.com" || got["status"] != "200" || got["msg"] != "hello world" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_LTSV_ValueContainingColonSplitsOnFirstOnly(t *testing.T) {
	got, err := ParseStructured("ltsv", "url:http://example.com/path")
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["url"] != "http://example.com/path" {
		t.Errorf("url = %q, want %q", got["url"], "http://example.com/path")
	}
}

func TestParseStructured_LTSV_EmptyInputIsError(t *testing.T) {
	_, err := ParseStructured("ltsv", "")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseStructured_Logfmt_SpaceSeparated(t *testing.T) {
	got, err := ParseStructured("logfmt", "level=info pid=123")
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["level"] != "info" || got["pid"] != "123" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_Logfmt_QuotedValueWithSpaces(t *testing.T) {
	got, err := ParseStructured("logfmt", `level=info msg="hello world" pid=123`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	if got["msg"] != "hello world" {
		t.Errorf("msg = %q, want %q", got["msg"], "hello world")
	}
	if got["level"] != "info" || got["pid"] != "123" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestParseStructured_Logfmt_EscapedQuoteInsideQuotedValue(t *testing.T) {
	got, err := ParseStructured("logfmt", `msg="say \"hi\" to me"`)
	if err != nil {
		t.Fatalf("ParseStructured returned error: %v", err)
	}
	want := `say "hi" to me`
	if got["msg"] != want {
		t.Errorf("msg = %q, want %q", got["msg"], want)
	}
}

func TestParseStructured_Logfmt_UnterminatedQuoteIsError(t *testing.T) {
	_, err := ParseStructured("logfmt", `msg="unterminated`)
	if err == nil {
		t.Error("expected error for unterminated quoted value")
	}
}

func TestParseStructured_Logfmt_EmptyInputIsError(t *testing.T) {
	_, err := ParseStructured("logfmt", "")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseStructured_UnknownFormatIsError(t *testing.T) {
	_, err := ParseStructured("xml", "<a>1</a>")
	if err == nil {
		t.Error("expected error for unsupported format")
	}
}
