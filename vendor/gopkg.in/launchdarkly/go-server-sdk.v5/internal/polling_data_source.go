package internal

import (
	"sync"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

const (
	pollingErrorContext     = "on polling request"
	pollingWillRetryMessage = "will retry at next scheduled poll interval"
)

// PollingProcessor is the internal implementation of the polling data source.
//
// This type is exported from internal so that the PollingDataSourceBuilder tests can verify its
// configuration. All other code outside of this package should interact with it only via the
// DataSource interface.
type PollingProcessor struct {
	dataSourceUpdates  interfaces.DataSourceUpdates
	requestor          requestor
	pollInterval       time.Duration
	loggers            ldlog.Loggers
	setInitializedOnce sync.Once
	isInitialized      bool
	quit               chan struct{}
	closeOnce          sync.Once
}

// NewPollingProcessor creates the internal implementation of the polling data source.
func NewPollingProcessor(
	context interfaces.ClientContext,
	dataSourceUpdates interfaces.DataSourceUpdates,
	baseURI string,
	pollInterval time.Duration,
) *PollingProcessor {
	requestor := newRequestorImpl(context, context.GetHTTP().CreateHTTPClient(), baseURI, true)
	return newPollingProcessor(context, dataSourceUpdates, requestor, pollInterval)
}

func newPollingProcessor(
	context interfaces.ClientContext,
	dataSourceUpdates interfaces.DataSourceUpdates,
	requestor requestor,
	pollInterval time.Duration,
) *PollingProcessor {
	pp := &PollingProcessor{
		dataSourceUpdates: dataSourceUpdates,
		requestor:         requestor,
		pollInterval:      pollInterval,
		loggers:           context.GetLogging().GetLoggers(),
		quit:              make(chan struct{}),
	}

	return pp
}

//nolint:golint,stylecheck // no doc comment for standard method
func (pp *PollingProcessor) Start(closeWhenReady chan<- struct{}) {
	pp.loggers.Infof("Starting LaunchDarkly polling with interval: %+v", pp.pollInterval)

	ticker := newTickerWithInitialTick(pp.pollInterval)

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
				pp.loggers.Info("Polling has been shut down")
				return
			case <-ticker.C:
				if err := pp.poll(); err != nil {
					if hse, ok := err.(httpStatusError); ok {
						errorInfo := interfaces.DataSourceErrorInfo{
							Kind:       interfaces.DataSourceErrorKindErrorResponse,
							StatusCode: hse.Code,
							Time:       time.Now(),
						}
						recoverable := checkIfErrorIsRecoverableAndLog(
							pp.loggers,
							httpErrorDescription(hse.Code),
							pollingErrorContext,
							hse.Code,
							pollingWillRetryMessage,
						)
						if recoverable {
							pp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateInterrupted, errorInfo)
						} else {
							pp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateOff, errorInfo)
							notifyReady()
							return
						}
					} else {
						errorInfo := interfaces.DataSourceErrorInfo{
							Kind:    interfaces.DataSourceErrorKindNetworkError,
							Message: err.Error(),
							Time:    time.Now(),
						}
						if _, ok := err.(malformedJSONError); ok {
							errorInfo.Kind = interfaces.DataSourceErrorKindInvalidData
						}
						checkIfErrorIsRecoverableAndLog(pp.loggers, err.Error(), pollingErrorContext, 0, pollingWillRetryMessage)
						pp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateInterrupted, errorInfo)
					}
					continue
				}
				pp.dataSourceUpdates.UpdateStatus(interfaces.DataSourceStateValid, interfaces.DataSourceErrorInfo{})
				pp.setInitializedOnce.Do(func() {
					pp.isInitialized = true
					pp.loggers.Info("First polling request successful")
					notifyReady()
				})
			}
		}
	}()
}

func (pp *PollingProcessor) poll() error {
	allData, cached, err := pp.requestor.requestAll()

	if err != nil {
		return err
	}

	// We initialize the store only if the request wasn't cached
	if !cached {
		pp.dataSourceUpdates.Init(makeAllStoreData(allData.Flags, allData.Segments))
	}
	return nil
}

//nolint:golint,stylecheck // no doc comment for standard method
func (pp *PollingProcessor) Close() error {
	pp.closeOnce.Do(func() {
		close(pp.quit)
	})
	return nil
}

//nolint:golint,stylecheck // no doc comment for standard method
func (pp *PollingProcessor) IsInitialized() bool {
	return pp.isInitialized
}

// GetBaseURI returns the configured polling base URI, for testing.
func (pp *PollingProcessor) GetBaseURI() string {
	return (pp.requestor.(*requestorImpl)).baseURI
}

// GetPollInterval returns the configured polling interval, for testing.
func (pp *PollingProcessor) GetPollInterval() time.Duration {
	return pp.pollInterval
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
