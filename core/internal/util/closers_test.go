package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type closeableThing struct {
	closed bool
}

func (c *closeableThing) Close() error {
	c.closed = true
	return nil
}

func TestCleanupTasks(t *testing.T) {
	var tasks CleanupTasks
	var c1 closeableThing
	var c2 closeableThing
	var c3 bool
	tasks.AddCloser(&c1)
	tasks.Clear()
	tasks.AddCloser(&c2)
	tasks.AddFunc(func() { c3 = true })
	tasks.Run()
	assert.False(t, c1.closed)
	assert.True(t, c2.closed)
	assert.True(t, c3)
}
