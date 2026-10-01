package initwrite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testUnitLimits = Limits{MinBytesPerSecond: 1024, Slack: time.Second, MaxHold: 10 * time.Second}

func wrapStreamClocked(base *deadlineConn, delivery Limits) (*Writer, *fakeClock) {
	c := newFakeClock()
	base.clock = c
	w := WrapStream(base, testUnitLimits, delivery)
	w.now = c.now
	return w, c
}

func TestDeliveryLimits(t *testing.T) {
	t.Run("without unit limits", func(t *testing.T) {
		assert.Equal(t, Limits{MinBytesPerSecond: minBytesPerSec, Slack: WriteSlack, MaxHold: time.Minute},
			DeliveryLimits(time.Minute, nil))
	})
	t.Run("a lower unit floor does not apply", func(t *testing.T) {
		unit := Limits{MinBytesPerSecond: 1024, Slack: time.Second}
		assert.Equal(t, minBytesPerSec, DeliveryLimits(time.Minute, &unit).MinBytesPerSecond)
	})
	t.Run("a higher unit floor applies, but not its slack or cap", func(t *testing.T) {
		unit := Limits{MinBytesPerSecond: 2 * minBytesPerSec, Slack: time.Second, MaxHold: time.Second}
		assert.Equal(t, Limits{MinBytesPerSecond: 2 * minBytesPerSec, Slack: WriteSlack, MaxHold: time.Minute},
			DeliveryLimits(time.Minute, &unit))
	})
}

func TestWrapResponseUsesItsLimits(t *testing.T) {
	base := newDeadlineConn()
	c := newFakeClock()
	w := WrapResponse(base, testUnitLimits)
	w.now = c.now

	start := c.now()
	_, err := w.Write(make([]byte, 2048))
	require.NoError(t, err)
	armed := base.armed()
	require.Len(t, armed, 1)
	// 2 KiB at 1 KiB/s is 2s, plus 1s of slack.
	assert.Equal(t, 3*time.Second, armed[0].Sub(start))
}

func TestStreamArmsAndClearsEachUnit(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapStreamClocked(base, DeliveryLimits(time.Minute, nil))

	start := c.now()
	_, err := w.WriteString("data: one\n\n")
	require.NoError(t, err)
	w.Flush()

	// The idle period between units carries no deadline.
	c.advance(time.Hour)
	start2 := c.now()
	_, err = w.WriteString("data: two\n\n")
	require.NoError(t, err)
	w.Flush()

	assert.Equal(t, []string{"arm", "write", "flush", "arm", "arm", "write", "flush", "arm"}, base.eventLog())
	armed := base.armed()
	require.Len(t, armed, 4)
	// Each unit is anchored to its own start and uses the unit limits: 11 bytes at 1 KiB/s
	// plus 1s of slack.
	unitBudget := testUnitLimits.budget(len("data: one\n\n"))
	assert.Equal(t, unitBudget, armed[0].Sub(start))
	assert.True(t, armed[1].IsZero(), "the first unit's flush must clear its deadline")
	assert.Equal(t, unitBudget, armed[2].Sub(start2), "the second unit must not inherit the first's start")
	assert.True(t, armed[3].IsZero(), "the second unit's flush must clear its deadline")
}

func TestStreamFlushWithNothingWrittenIsAUnit(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapStreamClocked(base, DeliveryLimits(time.Minute, nil))

	// The response headers reach the connection at the first flush, so that flush is bounded.
	start := c.now()
	w.Flush()
	assert.Equal(t, []string{"arm", "flush", "arm"}, base.eventLog())
	armed := base.armed()
	require.Len(t, armed, 2)
	assert.Equal(t, testUnitLimits.Slack, armed[0].Sub(start))
	assert.True(t, armed[1].IsZero())
}

func TestStreamUnitSpansWritesUntilFlush(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapStreamClocked(base, DeliveryLimits(time.Minute, nil))

	// A replay batch is several writes and one flush. The deadline counts all of its bytes.
	start := c.now()
	for i := 0; i < 3; i++ {
		_, err := w.Write(make([]byte, 1024))
		require.NoError(t, err)
	}
	armed := base.armed()
	require.Len(t, armed, 3)
	assert.Equal(t, 4*time.Second, armed[2].Sub(start), "3 KiB at 1 KiB/s plus 1s of slack")
	w.Flush()
	armed = base.armed()
	assert.True(t, armed[len(armed)-1].IsZero())
}

func TestStreamUnitCapDoesNotSetCapEngaged(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapStreamClocked(base, DeliveryLimits(time.Minute, nil))

	start := c.now()
	_, err := w.Write(make([]byte, 64*1024)) // 64s at the unit floor, above the 10s cap
	require.NoError(t, err)
	armed := base.armed()
	require.Len(t, armed, 1)
	assert.Equal(t, testUnitLimits.MaxHold, armed[0].Sub(start))
	assert.False(t, w.CapEngaged(), "CapEngaged reports gated deliveries only")
}

func TestStreamGatedDeliverySpansFlushes(t *testing.T) {
	base := newDeadlineConn()
	delivery := DeliveryLimits(time.Minute, nil)
	w, c := wrapStreamClocked(base, delivery)

	w.Begin()
	start := c.now()
	_, err := w.Write(make([]byte, 64*1024))
	require.NoError(t, err)
	w.Flush() // a flush inside the delivery does not end it
	armed := base.armed()
	require.Len(t, armed, 1)
	assert.Equal(t, delivery.budget(64*1024), armed[0].Sub(start), "the delivery uses the delivery limits")
	assertOpen(t, w.Done(), "a flush before End must not end the delivery")

	w.End()
	w.Flush()
	assertClosed(t, w.Done(), "the flush after End must end the delivery")
	armed = base.armed()
	assert.True(t, armed[len(armed)-1].IsZero())

	// After the delivery, writes are stream units again.
	unitStart := c.now()
	_, err = w.WriteString("data: delta\n\n")
	require.NoError(t, err)
	armed = base.armed()
	assert.Equal(t, testUnitLimits.budget(len("data: delta\n\n")), armed[len(armed)-1].Sub(unitStart))
}

func TestStreamBeginAbsorbsUnitInProgress(t *testing.T) {
	base := newDeadlineConn()
	delivery := DeliveryLimits(time.Minute, nil)
	w, c := wrapStreamClocked(base, delivery)

	_, err := w.WriteString(":heartbeat\n")
	require.NoError(t, err)
	w.Begin()
	start := c.now()
	_, err = w.Write(make([]byte, 1024))
	require.NoError(t, err)
	// The heartbeat's flush arrives inside the delivery, so it must not clear the deadline.
	w.Flush()
	armed := base.armed()
	require.Len(t, armed, 2)
	assert.Equal(t, delivery.budget(1024), armed[1].Sub(start), "the delivery restarts the accounting")
	assertOpen(t, w.Done(), "a flush before End must not end the delivery")
}

func TestStreamFlushDoesNotClearDeliveryThatBeginsDuringIt(t *testing.T) {
	base := newDeadlineConn()
	w, _ := wrapStreamClocked(base, DeliveryLimits(time.Minute, nil))

	_, err := w.WriteString(":heartbeat\n")
	require.NoError(t, err)
	// The producer begins a delivery while the handler is flushing the heartbeat.
	base.onFlush = w.Begin
	w.Flush()
	base.onFlush = nil

	for _, dl := range base.armed() {
		assert.False(t, dl.IsZero(), "the heartbeat's flush must not clear the new delivery's deadline")
	}
}

func TestStreamCutWhileIdleAppliesToNextUnit(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapStreamClocked(base, DeliveryLimits(time.Minute, nil))

	w.Cut()
	assert.Empty(t, base.armed(), "an idle stream is left alone")
	_, err := w.WriteString("data: x\n\n")
	require.NoError(t, err)
	armed := base.armed()
	require.Len(t, armed, 1)
	assert.Equal(t, c.now(), armed[0], "a unit after a cut must fail at once")
}

func TestStreamCutMidUnitMovesDeadlineToNow(t *testing.T) {
	base := newDeadlineConn()
	w, c := wrapStreamClocked(base, DeliveryLimits(time.Minute, nil))

	_, err := w.Write(make([]byte, 1024))
	require.NoError(t, err)
	c.advance(time.Second)
	w.Cut()
	armed := base.armed()
	require.Len(t, armed, 2)
	assert.Equal(t, c.now(), armed[1])
}

func TestExit(t *testing.T) {
	t.Run("stream shape sets the unit slack", func(t *testing.T) {
		base := newDeadlineConn()
		w, c := wrapStreamClocked(base, DeliveryLimits(time.Minute, nil))
		w.Exit()
		armed := base.armed()
		require.Len(t, armed, 1)
		assert.Equal(t, testUnitLimits.Slack, armed[0].Sub(c.now()))
	})
	t.Run("gated shape sets the delivery slack", func(t *testing.T) {
		base := newDeadlineConn()
		w, c := wrapClocked(base, time.Minute, true)
		w.Exit()
		armed := base.armed()
		require.Len(t, armed, 1)
		assert.Equal(t, WriteSlack, armed[0].Sub(c.now()))
	})
	t.Run("a cut connection gets now", func(t *testing.T) {
		base := newDeadlineConn()
		w, c := wrapStreamClocked(base, DeliveryLimits(time.Minute, nil))
		w.Cut()
		w.Exit()
		armed := base.armed()
		require.Len(t, armed, 1)
		assert.Equal(t, c.now(), armed[0])
	})
}
