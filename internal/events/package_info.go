// Package events contains event-related Relay functionality that is shared across packages.
//
// Currently, most of the event logic is in the deeper package internal/core/internal/events. This is for
// historical reasons because the "core" code was more encapsulated at one point; eventually we would
// like to simplify things and have just one "internal". In the meantime, internal/events contains a
// subset of code that was easy to move and that does need to be referenced outside of internal/core.
package events
