package util

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUncompressed(t *testing.T) {
	// make sure the bytes read are correct for non-gzipped streams
	nonZipBytes := []byte("this is a test")

	reader, _ := NewReader(io.NopCloser(bytes.NewReader(nonZipBytes)), false, 1000)
	payloadReader, _ := reader.(*PayloadReader)
	readBytes, _ := io.ReadAll(reader)

	assert.Equal(t, int64(len(nonZipBytes)), payloadReader.GetBytesRead())
	assert.Equal(t, int64(len(nonZipBytes)), payloadReader.GetUncompressedBytesRead())
	assert.Equal(t, nonZipBytes, readBytes)
}

func TestPayloadBytesTracking(t *testing.T) {
	// build a string
	var ret string
	for i := 0; i < 500; i++ {
		ret += "00"
	}

	// make sure the bytes read are correct for gzipped streams
	nonZipBytes := []byte(ret)

	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(nonZipBytes)
	w.Close()
	zipBytes := b.Bytes()

	reader, _ := NewReader(io.NopCloser(bytes.NewReader(zipBytes)), true, 10000)
	payloadReader, _ := reader.(*PayloadReader)
	readBytes, _ := io.ReadAll(reader)

	assert.Equal(t, int64(len(zipBytes)), payloadReader.GetBytesRead())                // compressed is 32
	assert.Equal(t, int64(len(nonZipBytes)), payloadReader.GetUncompressedBytesRead()) // uncompressed is 1000
	assert.Equal(t, nonZipBytes, readBytes)
}

func TestZipBombing(t *testing.T) {
	// build a string
	var ret string
	for i := 0; i < 500; i++ {
		ret += "00"
	}

	nonZipBytes := []byte(ret)

	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write(nonZipBytes)
	w.Close()
	zipBytes := b.Bytes()

	maxBytes := int64(100)
	reader, _ := NewReader(io.NopCloser(bytes.NewReader(zipBytes)), true, maxBytes)
	payloadReader, _ := reader.(*PayloadReader)
	bytesRead, err := io.ReadAll(reader)

	// we should have an error because the uncompressed stream is larger than maxBytes
	assert.Error(t, err)
	assert.Equal(t, int64(len(bytesRead)), maxBytes)

	assert.Equal(t, int64(len(zipBytes)), payloadReader.GetBytesRead())
	assert.Equal(t, maxBytes, payloadReader.GetUncompressedBytesRead())

}
