package util

import (
	"io"
)

// CleanupTasks accumulates a list of things which should be done at the end of a method unless Clear()
// is called. Intended usage:
//
//     var cleanup CleanupTasks
//     defer cleanup.Run()
//
//     thing1 := createThing1()
//     cleanup.AddCloser(thing1)
//
//     err := doSomethingElse()
//     if err != nil {
//         return err // thing1 will be disposed of automatically
//     }
//
//     cleanup.Clear() // everything succeeded so we don't want thing1 to be disposed of
type CleanupTasks []func()

// AddCloser adds a task for calling Close on an object
func (t *CleanupTasks) AddCloser(c io.Closer) {
	*t = append(*t, func() { _ = c.Close() })
}

// AddFunc adds a task.
func (t *CleanupTasks) AddFunc(f func()) {
	*t = append(*t, f)
}

// Clear clears all tasks that were added.
func (t *CleanupTasks) Clear() {
	*t = nil
}

// Run runs all of the tasks.
func (t *CleanupTasks) Run() {
	for _, tt := range *t {
		tt()
	}
	*t = nil
}
