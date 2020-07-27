package streams

import (
	"net/http"
	"time"

	"github.com/launchdarkly/eventsource"
)

// Publisher defines the interface for publishing SSE events. This interface exists so test code does
// not have to use a real eventsource.Server.
type Publisher interface {
	Handler(channel string) http.HandlerFunc
	Publish(channels []string, event eventsource.Event)
	PublishComment(channels []string, text string)
	Register(channel string, repo eventsource.Repository)
	Unregister(channel string, forceDisconnect bool)
	Close()
}

// Publishers encapsulates all of the SSE Server instances used by Relay. These are normally instances
// of eventsource.Server. They are not specific to any one environment; instead, each Server has a set
// of channel registrations, with one channel per environment.
type Publishers struct {
	ServerSideAll   Publisher
	ServerSideFlags Publisher
	Mobile          Publisher
	JSClient        Publisher
}

// NewPublishers creates a Publishers instance and all related eventsource.Server instances.
func NewPublishers(maxConnTime time.Duration) *Publishers {
	makeSSEServer := func() *eventsource.Server {
		s := eventsource.NewServer()
		s.Gzip = false
		s.AllowCORS = true
		s.ReplayAll = true
		s.MaxConnTime = maxConnTime
		return s
	}
	return &Publishers{
		ServerSideAll:   makeSSEServer(),
		ServerSideFlags: makeSSEServer(),
		Mobile:          makeSSEServer(),
		JSClient:        makeSSEServer(),
	}
}

// Close shuts down all of the eventsource.Server instances in the Publishers.
func (p *Publishers) Close() {
	p.ServerSideAll.Close()
	p.ServerSideFlags.Close()
	p.Mobile.Close()
	p.JSClient.Close()
}
