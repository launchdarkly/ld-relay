package util

import (
	"compress/gzip"
	"errors"
	"io"

	ct "github.com/launchdarkly/go-configtypes"
)

// PayloadReader is an implementation of io.Reader that reads bytes off the request body
// optionally decompresses them, and has a limit attached.
// If the limit is reached, an error will be returned and the underlying stream will be closed.
//
// Note: limit is applied to *both* compressed and uncompressed number of bytes.  This
// protects us from potential zipbombs
type PayloadReader struct {
	IsGzipped bool
	MaxBytes  int64

	uncompressedBytesRead int64

	wrappedBaseStream *byteCountingReader
	stream            *io.LimitedReader
}

// NewReader creates a new reader
func NewReader(r io.ReadCloser, isGzipped bool, maxInboundPayloadSize ct.OptBase2Bytes) (io.ReadCloser, error) {
	// If this isn't compressed, and we don't want to limit the size, just
	// return the original reader
	if !isGzipped && !maxInboundPayloadSize.IsDefined() {
		return r, nil
	}

	var baseStream = &byteCountingReader{
		bytesRead:  0,
		baseStream: r,
	}
	var s io.Reader = baseStream

	if isGzipped {
		var err error
		gzipReader, err := gzip.NewReader(s)
		if err != nil {
			return nil, err
		}

		// If we don't want to limit the size, just return the gzip reader
		if !maxInboundPayloadSize.IsDefined() {
			return gzipReader, nil
		}

		s = gzipReader
	}

	maxBytes := int64(maxInboundPayloadSize.GetOrElse(0))
	stream := io.LimitReader(s, maxBytes).(*io.LimitedReader)

	payloadReader := &PayloadReader{
		IsGzipped:         isGzipped,
		MaxBytes:          maxBytes,
		wrappedBaseStream: baseStream,
		stream:            stream,
	}
	return payloadReader, nil
}

// GetBytesRead returns the total number of bytes read off the original stream
func (pr *PayloadReader) GetBytesRead() int64 {
	return pr.wrappedBaseStream.bytesRead
}

// GetUncompressedBytesRead Total number of Bytes in the uncompressed stream.
//
//	GetBytesRead and  GetUncompressedBytesRead will return the same value if the stream is uncompressed.
func (pr *PayloadReader) GetUncompressedBytesRead() int64 {
	return pr.uncompressedBytesRead
}

func (pr *PayloadReader) Read(p []byte) (int, error) {
	n, err := pr.stream.Read(p)
	pr.uncompressedBytesRead += int64(n)
	if err != nil {
		return n, err
	}
	if pr.stream.N <= 0 {
		_ = pr.Close()
		return n, errors.New("max bytes exceeded")
	}
	return n, err
}

func (pr *PayloadReader) Close() error {
	c, ok := pr.wrappedBaseStream.baseStream.(io.Closer)
	if ok {
		return c.Close()
	}
	return nil
}

// a simple reader decorator that keeps a running total of bytes
type byteCountingReader struct {
	baseStream io.Reader
	bytesRead  int64
}

func (bt *byteCountingReader) Read(p []byte) (int, error) {
	n, err := bt.baseStream.Read(p)
	bt.bytesRead += int64(n)
	return n, err
}
