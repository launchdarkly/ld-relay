package ldclient

import (
	"sync"
	"time"
)

type pollingProcessor struct {
	store              FeatureStore
	requestor          *requestor
	config             Config
	setInitializedOnce sync.Once
	isInitialized      bool
	quit               chan bool
}

func newPollingProcessor(config Config, requestor *requestor) updateProcessor {
	pp := &pollingProcessor{
		store:     config.FeatureStore,
		requestor: requestor,
		config:    config,
		quit:      make(chan bool),
	}

	return pp
}

func (pp *pollingProcessor) start(ch chan<- bool) {
	go func() {
		for {
			select {
			case <-pp.quit:
				return
			default:
				then := time.Now()
				err := pp.poll()
				if err == nil {
					pp.setInitializedOnce.Do(func() {
						pp.isInitialized = true
						ch <- true
					})
				}
				delta := pp.config.PollInterval - time.Since(then)

				if delta > 0 {
					time.Sleep(delta)
				}
			}
		}
	}()
}

func (pp *pollingProcessor) poll() error {
	features, cached, err := pp.requestor.makeAllRequest(true)

	if err != nil {
		return err
	}

	// We initialize the store only if the request wasn't cached
	if !cached {
		return pp.store.Init(features)
	}
	return nil
}

func (pp *pollingProcessor) close() {
	pp.quit <- true
}

func (pp *pollingProcessor) initialized() bool {
	return pp.isInitialized
}
