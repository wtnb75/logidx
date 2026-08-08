// Package decompress wraps an input reader in a decompressing reader based
// on the compressed file's extension, so logidx import can read
// gzip/xz/bzip2/zstd-compressed log files directly.
package decompress

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// closerFunc adapts a plain func() error to io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// Wrap inspects path's extension and, if it names a supported compression
// format, wraps r in the matching decompressing reader. If the extension is
// unrecognized (including no extension), r is returned unchanged along with
// a nil Closer. The returned io.Closer releases any resources the
// decompressor itself holds beyond r (currently only gzip and zstd need
// this); callers must call it (if non-nil) in addition to closing the
// original reader/file.
//
// gzip, xz, and zstd validate their header immediately - a mismatched or
// corrupt input makes this call return an error right away. bzip2's
// decoder does not validate anything until it is first read from; a
// corrupt bzip2 input surfaces as a read error later instead.
func Wrap(path string, r io.Reader) (io.Reader, io.Closer, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gz":
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("open gzip reader: %w", err)
		}
		return gr, gr, nil
	case ".xz":
		xr, err := xz.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("open xz reader: %w", err)
		}
		return xr, nil, nil
	case ".bz2":
		return bzip2.NewReader(r), nil, nil
	case ".zst":
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("open zstd reader: %w", err)
		}
		// Validate header by attempting to read one byte
		b := make([]byte, 1)
		n, err := zr.Read(b)
		if err != nil && err != io.EOF {
			return nil, nil, fmt.Errorf("validate zstd reader: %w", err)
		}
		// If we read a byte, replay it with MultiReader
		var reader io.Reader
		if n > 0 {
			reader = io.MultiReader(bytes.NewReader(b[:n]), zr)
		} else {
			reader = zr
		}
		return reader, closerFunc(func() error { zr.Close(); return nil }), nil
	default:
		return r, nil, nil
	}
}
