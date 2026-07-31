package streams

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturingHandler struct{ recs *[]slog.Record }

func (h capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h capturingHandler) Handle(_ context.Context, r slog.Record) error {
	*h.recs = append(*h.recs, r)
	return nil
}
func (h capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h capturingHandler) WithGroup(string) slog.Handler      { return h }

func TestSSELoggerDistinguishesCutFromDisconnect(t *testing.T) {
	var recs []slog.Record
	l := sseLogger{log: slog.New(capturingHandler{&recs})}

	// A write-deadline cut (the limiter reclaiming a slot) surfaces as a deadline error and is
	// logged at warn; an ordinary client disconnect is logged at debug.
	l.Println(os.ErrDeadlineExceeded)
	l.Println(errors.New("write: broken pipe"))

	require.Len(t, recs, 2)
	assert.Equal(t, slog.LevelWarn, recs[0].Level)
	assert.Contains(t, recs[0].Message, "write deadline exceeded")
	assert.Equal(t, slog.LevelDebug, recs[1].Level)
}

func TestWithLoggerSetsServerSideSSELogger(t *testing.T) {
	sp := NewStreamProvider(basictypes.ServerSideStream, 0, 0, WithLogger(slog.Default())).(*serverSideStreamProvider)
	defer sp.Close()
	assert.NotNil(t, sp.fdv1Server.Logger, "server-side FDv1 SSE server should have a logger set")
	assert.NotNil(t, sp.fdv2Server.Logger, "server-side FDv2 SSE server should have a logger set")

	// Without WithLogger, the logger stays unset (unchanged default behavior).
	sp2 := NewStreamProvider(basictypes.ServerSideStream, 0, 0).(*serverSideStreamProvider)
	defer sp2.Close()
	assert.Nil(t, sp2.fdv1Server.Logger)
}
