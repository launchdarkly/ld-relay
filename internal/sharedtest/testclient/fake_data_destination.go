package testclient

import "github.com/launchdarkly/go-server-sdk/v7/subsystems"

type FakeDataDestination struct {
	store *FakeStore
}

func NewFakeDataDestination(store *FakeStore) *FakeDataDestination {
	return &FakeDataDestination{store: store}
}

func (d *FakeDataDestination) Selector() subsystems.Selector {
	return d.store.Selector()
}

func (d *FakeDataDestination) SetBasis(changes []subsystems.Change, selector subsystems.Selector, persist bool) {
	d.store.SetBasis(changes, selector, persist)
}

func (d *FakeDataDestination) ApplyDelta(changes []subsystems.Change, selector subsystems.Selector, persist bool) {
	d.store.ApplyDelta(changes, selector, persist)
}
