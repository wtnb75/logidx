package decompress

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

func TestWrap_GzipRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte("hello, gzip\n")); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	r, closer, err := Wrap("input.gz", &buf)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer == nil {
		t.Fatal("expected a non-nil Closer for gzip")
	}
	defer func() { _ = closer.Close() }()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello, gzip\n" {
		t.Errorf("got %q, want %q", got, "hello, gzip\n")
	}
}

func TestWrap_GzipUppercaseExtensionIsRecognized(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}

	r, _, err := Wrap("input.GZ", &buf)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("got %q, want %q", got, "hello\n")
	}
}

func TestWrap_XzRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	xw, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("xz.NewWriter: %v", err)
	}
	if _, err := xw.Write([]byte("hello, xz\n")); err != nil {
		t.Fatalf("write xz: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("close xz writer: %v", err)
	}

	r, closer, err := Wrap("input.xz", &buf)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer != nil {
		t.Error("expected a nil Closer for xz")
	}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello, xz\n" {
		t.Errorf("got %q, want %q", got, "hello, xz\n")
	}
}

func TestWrap_Bzip2RoundTrip(t *testing.T) {
	// Precomputed with: printf 'hello, bzip2\n' | bzip2 -9
	data := []byte{
		0x42, 0x5a, 0x68, 0x39, 0x31, 0x41, 0x59, 0x26, 0x53, 0x59, 0xb1, 0x23,
		0xde, 0x43, 0x00, 0x00, 0x03, 0x59, 0x80, 0x00, 0x10, 0x40, 0x04, 0x10,
		0x00, 0x12, 0x64, 0xc0, 0x10, 0x20, 0x00, 0x31, 0x03, 0x40, 0xd0, 0x20,
		0x01, 0xa6, 0x91, 0x03, 0xab, 0x6c, 0x82, 0x84, 0xf8, 0xbb, 0x92, 0x29,
		0xc2, 0x84, 0x85, 0x89, 0x1e, 0xf2, 0x18,
	}

	r, closer, err := Wrap("input.bz2", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer != nil {
		t.Error("expected a nil Closer for bzip2")
	}

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello, bzip2\n" {
		t.Errorf("got %q, want %q", got, "hello, bzip2\n")
	}
}

func TestWrap_ZstdRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, err := zw.Write([]byte("hello, zstd\n")); err != nil {
		t.Fatalf("write zstd: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zstd writer: %v", err)
	}

	r, closer, err := Wrap("input.zst", &buf)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer == nil {
		t.Fatal("expected a non-nil Closer for zstd")
	}
	defer func() { _ = closer.Close() }()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read decompressed data: %v", err)
	}
	if string(got) != "hello, zstd\n" {
		t.Errorf("got %q, want %q", got, "hello, zstd\n")
	}
}

func TestWrap_UnrecognizedExtensionReturnsReaderUnchanged(t *testing.T) {
	src := bytes.NewReader([]byte("plain text\n"))

	r, closer, err := Wrap("input.log", src)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer != nil {
		t.Error("expected a nil Closer for an unrecognized extension")
	}
	if r != src {
		t.Error("expected the original reader to be returned unchanged")
	}
}

func TestWrap_NoExtensionReturnsReaderUnchanged(t *testing.T) {
	src := bytes.NewReader([]byte("plain text\n"))

	r, closer, err := Wrap("input", src)
	if err != nil {
		t.Fatalf("Wrap returned error: %v", err)
	}
	if closer != nil {
		t.Error("expected a nil Closer with no extension")
	}
	if r != src {
		t.Error("expected the original reader to be returned unchanged")
	}
}

func TestWrap_CorruptGzipReturnsError(t *testing.T) {
	_, _, err := Wrap("input.gz", bytes.NewReader([]byte("not actually gzip data")))
	if err == nil {
		t.Fatal("expected an error for corrupt gzip data")
	}
}

func TestWrap_CorruptXzReturnsError(t *testing.T) {
	_, _, err := Wrap("input.xz", bytes.NewReader([]byte("not actually xz data")))
	if err == nil {
		t.Fatal("expected an error for corrupt xz data")
	}
}

func TestWrap_CorruptZstdReturnsError(t *testing.T) {
	_, _, err := Wrap("input.zst", bytes.NewReader([]byte("not actually zstd data")))
	if err == nil {
		t.Fatal("expected an error for corrupt zstd data")
	}
}
