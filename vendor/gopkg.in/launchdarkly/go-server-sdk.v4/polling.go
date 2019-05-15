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
	quit               chan struct{}
	closeOnce          sync.Once
}

func newPollingProcessor(config Config, requestor *requestor) *pollingProcessor {
	pp := &pollingProcessor{
		store:     config.FeatureStore,
		requestor: requestor,
		config:    config,
		quit:      make(chan struct{}),
	}

	return pp
}

func (pp *pollingProcessor) Start(closeWhenReady chan<- struct{}) {
	pp.config.Logger.Printf("Starting LaunchDarkly polling processor with interval: %+v", pp.config.PollInterval)

	ticker := newTickerWithInitialTick(pp.config.PollInterval)

	go func() {
		defer ticker.Stop()

		var readyOnce sync.Once
		notifyReady := func() {
			readyOnce.Do(func() {
				close(closeWhenReady)
			})
		}
		// Ensure we stop waiting for initialization if we exit, even if initialization fails
		defer notifyReady()

		for {
			select {
			case <-pp.quit:
				pp.config.Logger.Printf("Polling Processor closed.")
				return
			case <-ticker.C:
				if err := pp.poll(); err != nil {
					pp.config.Logger.Printf("ERROR: Error when requesting feature updates: %+v", err)
					if hse, ok := err.(HttpStatusError); ok {
						pp.config.Logger.Printf("ERROR: %s", httpErrorMessage(hse.Code, "polling request", "will retry"))
						if !isHTTPErrorRecoverable(hse.Code) {
							notifyReady()
							return
						}
					}
					continue
				}
				pp.setInitializedOnce.Do(func() {
					pp.isInitialized = true
					notifyReady()
				})
			}
		}
	}()
}

func (pp *pollingProcessor) poll() error {
	allData, cached, err := pp.requestor.requestAll()

	if err != nil {
		return err
	}

	// We initialize the store only if the request wasn't cached
	if !cached {
		return pp.store.Init(MakeAllVersionedDataMap(allData.Flags, allData.Segments))
	}
	return nil
}

func (pp *pollingProcessor) Close() error {
	pp.closeOnce.Do(func() {
		pp.config.Logger.Printf("Closing Polling Processor")
		close(pp.quit)
	})
	return nil
}

func (pp *pollingProcessor) Initialized() bool {
	return pp.isInitialized
}

type tickerWithInitialTick struct {
	*time.Ticker
	C <-chan time.Time
}

func newTickerWithInitialTick(interval time.Duration) *tickerWithInitialTick {
	c := make(chan time.Time)
	ticker := time.NewTicker(interval)
	t := &tickerWithInitialTick{
		C:      c,
		Ticker: ticker,
	}
	go func() {
		c <- time.Now() // Ensure we do an initial poll immediately
		for tt := range ticker.C {
			c <- tt
		}
	}()
	return t
}
