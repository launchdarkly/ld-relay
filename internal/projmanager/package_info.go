// Package projmanager executes the commands that the auto-configuration stream delivers: it creates,
// updates, and deletes entities (environments and filters), routing each one by its project key. It
// also owns the interfaces that describe those commands (AutoConfigActions, EnvironmentActions) and
// the queue that keeps one environment's slow command from delaying another's
// (AutoConfigActionQueue).
package projmanager
