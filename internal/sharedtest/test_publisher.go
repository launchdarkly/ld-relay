package sharedtest

import (
	"net/http"
	"sync"

	"github.com/launchdarkly/eventsource"
)

type PublishedEvent struct {
	Channel string
	Event   eventsource.Event
}

type PublishedComment struct {
	Channel string
	Text    string
}

type TestPublisher struct {
	Events   []PublishedEvent
	Comments []PublishedComment
	Repos    map[string]eventsource.Repository
	lock     sync.Mutex
}

type TestPublishers struct {
	ServerSideAll   TestPublisher
	ServerSideFlags TestPublisher
	Mobile          TestPublisher
	JSClient        TestPublisher
}

func (p *TestPublisher) Publish(channels []string, event eventsource.Event) {
	for _, c := range channels {
		p.Events = append(p.Events, PublishedEvent{c, event})
	}
}

func (p *TestPublisher) PublishComment(channels []string, text string) {
	p.lock.Lock()
	defer p.lock.Unlock()
	for _, c := range channels {
		p.Comments = append(p.Comments, PublishedComment{c, text})
	}
}

func (p *TestPublisher) Register(channel string, repo eventsource.Repository) {
	if p.Repos == nil {
		p.Repos = make(map[string]eventsource.Repository)
	}
	p.Repos[channel] = repo
}

func (p *TestPublisher) Unregister(channel string, forceDisconnect bool) {
	delete(p.Repos, channel)
}

func (p *TestPublisher) Close() {}

func (p *TestPublisher) Handler(string) http.HandlerFunc { return nil }

func (p *TestPublisher) GetComments() []PublishedComment {
	p.lock.Lock()
	defer p.lock.Unlock()
	ret := make([]PublishedComment, len(p.Comments))
	copy(ret, p.Comments)
	return ret
}
