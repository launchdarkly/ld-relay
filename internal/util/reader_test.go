package util

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/alecthomas/units"
	ct "github.com/launchdarkly/go-configtypes"
	"github.com/stretchr/testify/assert"
)

func TestUncompressed(t *testing.T) {
	// make sure the bytes read are correct for non-gzipped streams
	nonZipBytes := []byte("this is a test")

	reader, _ := NewReader(io.NopCloser(bytes.NewReader(nonZipBytes)), false, ct.NewOptBase2Bytes(units.KiB))
	payloadReader, _ := reader.(*PayloadReader)
	readBytes, _ := io.ReadAll(reader)

	assert.Equal(t, int64(len(nonZipBytes)), payloadReader.GetBytesRead())
	assert.Equal(t, int64(len(nonZipBytes)), payloadReader.GetUncompressedBytesRead())
	assert.Equal(t, nonZipBytes, readBytes)
}

func TestPayloadBytesTracking(t *testing.T) {
	ret := strings.Repeat("0", 1_000)

	// make sure the bytes read are correct for gzipped streams
	nonZipBytes := []byte(ret)

	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(nonZipBytes)
	w.Close()
	zipBytes := b.Bytes()

	reader, _ := NewReader(io.NopCloser(bytes.NewReader(zipBytes)), true, ct.NewOptBase2Bytes(10*units.KiB))
	payloadReader, _ := reader.(*PayloadReader)
	readBytes, _ := io.ReadAll(reader)

	assert.Equal(t, int64(len(zipBytes)), payloadReader.GetBytesRead())                // compressed is 32
	assert.Equal(t, int64(len(nonZipBytes)), payloadReader.GetUncompressedBytesRead()) // uncompressed is 1000
	assert.Equal(t, nonZipBytes, readBytes)
}

func TestZipBombing(t *testing.T) {
	// build a string
	ret := strings.Repeat("0", 1_000)

	nonZipBytes := []byte(ret)

	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(nonZipBytes)
	w.Close()
	zipBytes := b.Bytes()

	maxBytes := int64(100)
	reader, _ := NewReader(io.NopCloser(bytes.NewReader(zipBytes)), true, ct.NewOptBase2Bytes(units.Base2Bytes(maxBytes)))
	payloadReader, _ := reader.(*PayloadReader)
	bytesRead, err := io.ReadAll(reader)

	// we should have an error because the uncompressed stream is larger than maxBytes
	assert.Error(t, err)
	assert.Equal(t, int64(len(bytesRead)), maxBytes)

	assert.Equal(t, int64(len(zipBytes)), payloadReader.GetBytesRead())
	assert.Equal(t, maxBytes, payloadReader.GetUncompressedBytesRead())

}
