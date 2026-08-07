package compression

import "github.com/parquet-go/parquet-go/format"

// NameFromFormatCodec maps a Parquet-format compression codec (as read from
// a file's on-disk metadata) to the codec name used in Settings.Codec. It
// returns ("", false) for LZO and the deprecated non-raw LZ4, which this
// package does not support selecting.
func NameFromFormatCodec(codec format.CompressionCodec) (string, bool) {
	switch codec {
	case format.Uncompressed:
		return "uncompressed", true
	case format.Snappy:
		return "snappy", true
	case format.Gzip:
		return "gzip", true
	case format.Brotli:
		return "brotli", true
	case format.Zstd:
		return "zstd", true
	case format.Lz4Raw:
		return "lz4", true
	default:
		return "", false
	}
}
