package oshelpers

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

// LineParsingWriter is a Writer that buffers output by line, and calls the specified function for each line.
type LineParsingWriter struct {
	outputFn func(string)
	buf      *bytes.Buffer
	scan     *bufio.Scanner
}

// NewLineParsingWriter creates a LineParsingWriter.
func NewLineParsingWriter(outputFn func(string)) *LineParsingWriter {
	buf := bytes.NewBuffer(nil)
	scan := bufio.NewScanner(buf)
	return &LineParsingWriter{outputFn, buf, scan}
}

func (w *LineParsingWriter) Write(p []byte) (int, error) {
	n, _ := w.buf.Write(p)
	for {
		if line, ok := w.nextLine(); ok {
			w.outputFn(line)
		} else {
			break
		}
	}
	return n, nil
}

// Flush writes any buffered incomplete line.
func (w *LineParsingWriter) Flush() {
	if w.buf.Len() != 0 {
		w.outputFn(w.buf.String())
		w.buf.Reset()
	}
}

// Close closes the writer.
func (w *LineParsingWriter) Close() {
	w.Flush()
}

func (w *LineParsingWriter) nextLine() (string, bool) {
	line, err := w.buf.ReadString('\n')
	if err != nil {
		w.buf.Reset()
		w.buf.Write([]byte(line))
		return "", false
	}
	return strings.TrimSuffix(line, "\n"), true
}

// NewLogWriter returns a LineParsingWriter that writes to log.Println with the specified prefix.
func NewLogWriter(dest io.Writer, logPrefix string) *LineParsingWriter {
	l := log.New(os.Stdout, fmt.Sprintf("[%s] ", logPrefix), log.LstdFlags)
	return NewLineParsingWriter(func(line string) { l.Println(line) })
}
