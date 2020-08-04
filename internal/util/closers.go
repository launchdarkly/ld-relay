package util

import (
	"io"
)

type CleanupTasks []func()

func (t *CleanupTasks) AddCloser(c io.Closer) {
	*t = append(*t, func() { _ = c.Close() })
}

func (t *CleanupTasks) AddFunc(f func()) {
	*t = append(*t, f)
}

func (t *CleanupTasks) Clear() {
	*t = nil
}

func (t *CleanupTasks) Run() {
	for _, tt := range *t {
		tt()
	}
	*t = nil
}
