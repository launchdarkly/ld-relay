package oshelpers

import (
	"bufio"
	"bytes"
	"strings"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

type LineParsingWriter struct {
	outputFn func(string)
	buf      *bytes.Buffer
	scan     *bufio.Scanner
}

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

func (w *LineParsingWriter) Flush() {
	if w.buf.Len() != 0 {
		w.outputFn(w.buf.String())
		w.buf.Reset()
	}
}

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

func newWriterToLogger(logger ldlog.BaseLogger, prefix string) *LineParsingWriter {
	return NewLineParsingWriter(func(line string) { logger.Println(prefix + line) })
}
