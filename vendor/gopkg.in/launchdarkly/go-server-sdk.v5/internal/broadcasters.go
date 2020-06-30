package internal

import (
	"sync"

	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// This file contains all of the types we use for the publish-subscribe model for various status types.
// The logic is very repetitive, due to Go's lack of generics; in each case, we're just maintaining a list
// of subscription channels that we will broadcast some value to.

// Arbitrary buffer size to make it less likely that we'll block when broadcasting to channels. It is still
// the consumer's responsibility to make sure they're reading the channel.
const subscriberChannelBufferLength = 10

// DataStoreStatusBroadcaster is the internal implementation of publish-subscribe for DataStoreStatus values.
type DataStoreStatusBroadcaster struct {
	subscribers []chan interfaces.DataStoreStatus
	lock        sync.Mutex
}

// NewDataStoreStatusBroadcaster creates an instance of DataStoreStatusBroadcaster.
func NewDataStoreStatusBroadcaster() *DataStoreStatusBroadcaster {
	return &DataStoreStatusBroadcaster{}
}

// AddListener creates a new channel for listening to broadcast values. This is created with a small
// channel buffer, but it is the consumer's responsibility to consume the channel to avoid blocking an
// SDK goroutine.
func (b *DataStoreStatusBroadcaster) AddListener() <-chan interfaces.DataStoreStatus {
	ch := make(chan interfaces.DataStoreStatus, subscriberChannelBufferLength)
	b.lock.Lock()
	defer b.lock.Unlock()
	b.subscribers = append(b.subscribers, ch)
	return ch
}

// RemoveListener stops broadcasting to a channel that was created with AddListener.
func (b *DataStoreStatusBroadcaster) RemoveListener(ch <-chan interfaces.DataStoreStatus) {
	b.lock.Lock()
	defer b.lock.Unlock()
	ss := b.subscribers
	for i, s := range ss {
		if s == ch {
			copy(ss[i:], ss[i+1:])
			ss[len(ss)-1] = nil
			b.subscribers = ss[:len(ss)-1]
			close(s)
			break
		}
	}
}

// Broadcast broadcasts a new value to the registered listeners, if any.
func (b *DataStoreStatusBroadcaster) Broadcast(value interfaces.DataStoreStatus) {
	var ss []chan interfaces.DataStoreStatus
	b.lock.Lock()
	if len(b.subscribers) > 0 {
		ss = make([]chan interfaces.DataStoreStatus, len(b.subscribers))
		copy(ss, b.subscribers)
	}
	b.lock.Unlock()
	for _, ch := range ss {
		ch <- value
	}
}

// Close closes all currently registered listener channels.
func (b *DataStoreStatusBroadcaster) Close() {
	b.lock.Lock()
	defer b.lock.Unlock()
	for _, s := range b.subscribers {
		close(s)
	}
	b.subscribers = nil
}

// DataSourceStatusBroadcaster is the internal implementation of publish-subscribe for DataSourceStatus values.
type DataSourceStatusBroadcaster struct {
	subscribers []chan interfaces.DataSourceStatus
	lock        sync.Mutex
}

// NewDataSourceStatusBroadcaster creates an instance of DataSourceStatusBroadcaster.
func NewDataSourceStatusBroadcaster() *DataSourceStatusBroadcaster {
	return &DataSourceStatusBroadcaster{}
}

// AddListener creates a new channel for listening to broadcast values. This is created with a small
// channel buffer, but it is the consumer's responsibility to consume the channel to avoid blocking an
// SDK goroutine.
func (b *DataSourceStatusBroadcaster) AddListener() <-chan interfaces.DataSourceStatus {
	ch := make(chan interfaces.DataSourceStatus, subscriberChannelBufferLength)
	b.lock.Lock()
	defer b.lock.Unlock()
	b.subscribers = append(b.subscribers, ch)
	return ch
}

// RemoveListener stops broadcasting to a channel that was created with AddListener.
func (b *DataSourceStatusBroadcaster) RemoveListener(ch <-chan interfaces.DataSourceStatus) {
	b.lock.Lock()
	defer b.lock.Unlock()
	ss := b.subscribers
	for i, s := range ss {
		if s == ch {
			copy(ss[i:], ss[i+1:])
			ss[len(ss)-1] = nil
			b.subscribers = ss[:len(ss)-1]
			close(s)
			break
		}
	}
}

// Broadcast broadcasts a new value to the registered listeners, if any.
func (b *DataSourceStatusBroadcaster) Broadcast(value interfaces.DataSourceStatus) {
	var ss []chan interfaces.DataSourceStatus
	b.lock.Lock()
	if len(b.subscribers) > 0 {
		ss = make([]chan interfaces.DataSourceStatus, len(b.subscribers))
		copy(ss, b.subscribers)
	}
	b.lock.Unlock()
	for _, ch := range ss {
		ch <- value
	}
}

// Close closes all currently registered listener channels.
func (b *DataSourceStatusBroadcaster) Close() {
	b.lock.Lock()
	defer b.lock.Unlock()
	for _, s := range b.subscribers {
		close(s)
	}
	b.subscribers = nil
}

// FlagChangeEventBroadcaster is the internal implementation of publish-subscribe for FlagChangeEvent values.
type FlagChangeEventBroadcaster struct {
	subscribers []chan interfaces.FlagChangeEvent
	lock        sync.Mutex
}

// NewFlagChangeEventBroadcaster creates an instance of FlagChangeEventBroadcaster.
func NewFlagChangeEventBroadcaster() *FlagChangeEventBroadcaster {
	return &FlagChangeEventBroadcaster{}
}

// AddListener creates a new channel for listening to broadcast values. This is created with a small
// channel buffer, but it is the consumer's responsibility to consume the channel to avoid blocking an
// SDK goroutine.
func (b *FlagChangeEventBroadcaster) AddListener() <-chan interfaces.FlagChangeEvent {
	ch := make(chan interfaces.FlagChangeEvent, subscriberChannelBufferLength)
	b.lock.Lock()
	defer b.lock.Unlock()
	b.subscribers = append(b.subscribers, ch)
	return ch
}

// RemoveListener stops broadcasting to a channel that was created with AddListener.
func (b *FlagChangeEventBroadcaster) RemoveListener(ch <-chan interfaces.FlagChangeEvent) {
	b.lock.Lock()
	defer b.lock.Unlock()
	ss := b.subscribers
	for i, s := range ss {
		if s == ch {
			copy(ss[i:], ss[i+1:])
			ss[len(ss)-1] = nil
			b.subscribers = ss[:len(ss)-1]
			close(s)
			break
		}
	}
}

// HasListeners returns true if any listeners are registered.
func (b *FlagChangeEventBroadcaster) HasListeners() bool {
	return len(b.subscribers) > 0
}

// Broadcast broadcasts a new value to the registered listeners, if any.
func (b *FlagChangeEventBroadcaster) Broadcast(value interfaces.FlagChangeEvent) {
	var ss []chan interfaces.FlagChangeEvent
	b.lock.Lock()
	if len(b.subscribers) > 0 {
		ss = make([]chan interfaces.FlagChangeEvent, len(b.subscribers))
		copy(ss, b.subscribers)
	}
	b.lock.Unlock()
	for _, ch := range ss {
		ch <- value
	}
}

// Close closes all currently registered listener channels.
func (b *FlagChangeEventBroadcaster) Close() {
	b.lock.Lock()
	defer b.lock.Unlock()
	for _, s := range b.subscribers {
		close(s)
	}
	b.subscribers = nil
}
