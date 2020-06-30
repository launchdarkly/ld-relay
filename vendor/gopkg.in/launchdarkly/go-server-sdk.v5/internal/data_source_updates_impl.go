package internal

import (
	"fmt"
	"sync"
	"time"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	intf "gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// DataSourceUpdatesImpl is the internal implementation of DataSourceUpdates. It is exported
// because the actual implementation type, rather than the interface, is required as a dependency
// of other SDK components.
type DataSourceUpdatesImpl struct {
	store                       intf.DataStore
	dataStoreStatusProvider     intf.DataStoreStatusProvider
	dataSourceStatusBroadcaster *DataSourceStatusBroadcaster
	flagChangeEventBroadcaster  *FlagChangeEventBroadcaster
	dependencyTracker           *dependencyTracker
	outageTracker               *outageTracker
	loggers                     ldlog.Loggers
	currentStatus               intf.DataSourceStatus
	lastStoreUpdateFailed       bool
	lock                        sync.Mutex
}

// NewDataSourceUpdatesImpl creates the internal implementation of DataSourceUpdates.
func NewDataSourceUpdatesImpl(
	store intf.DataStore,
	dataStoreStatusProvider intf.DataStoreStatusProvider,
	dataSourceStatusBroadcaster *DataSourceStatusBroadcaster,
	flagChangeEventBroadcaster *FlagChangeEventBroadcaster,
	logDataSourceOutageAsErrorAfter time.Duration,
	loggers ldlog.Loggers,
) *DataSourceUpdatesImpl {
	return &DataSourceUpdatesImpl{
		store:                       store,
		dataStoreStatusProvider:     dataStoreStatusProvider,
		dataSourceStatusBroadcaster: dataSourceStatusBroadcaster,
		flagChangeEventBroadcaster:  flagChangeEventBroadcaster,
		dependencyTracker:           newDependencyTracker(),
		outageTracker:               newOutageTracker(logDataSourceOutageAsErrorAfter, loggers),
		loggers:                     loggers,
		currentStatus: intf.DataSourceStatus{
			State:      intf.DataSourceStateInitializing,
			StateSince: time.Now(),
		},
	}
}

//nolint:golint,stylecheck // no doc comment for standard method
func (d *DataSourceUpdatesImpl) Init(allData []intf.StoreCollection) bool {
	var oldData map[intf.StoreDataKind]map[string]intf.StoreItemDescriptor

	if d.flagChangeEventBroadcaster.HasListeners() {
		// Query the existing data if any, so that after the update we can send events for whatever was changed
		oldData = make(map[intf.StoreDataKind]map[string]intf.StoreItemDescriptor)
		for _, kind := range intf.StoreDataKinds() {
			if items, err := d.store.GetAll(kind); err == nil {
				m := make(map[string]intf.StoreItemDescriptor)
				for _, item := range items {
					m[item.Key] = item.Item
				}
				oldData[kind] = m
			}
		}
	}

	err := d.store.Init(sortCollectionsForDataStoreInit(allData))
	updated := d.maybeUpdateError(err)

	if updated {
		// We must always update the dependency graph even if we don't currently have any event listeners, because if
		// listeners are added later, we don't want to have to reread the whole data store to compute the graph
		d.updateDependencyTrackerFromFullDataSet(allData)

		// Now, if we previously queried the old data because someone is listening for flag change events, compare
		// the versions of all items and generate events for those (and any other items that depend on them)
		if oldData != nil {
			d.sendChangeEvents(d.computeChangedItemsForFullDataSet(oldData, fullDataSetToMap(allData)))
		}
	}

	return updated
}

//nolint:golint,stylecheck // no doc comment for standard method
func (d *DataSourceUpdatesImpl) Upsert(
	kind intf.StoreDataKind,
	key string,
	item intf.StoreItemDescriptor,
) bool {
	updated, err := d.store.Upsert(kind, key, item)
	didNotGetError := d.maybeUpdateError(err)

	if updated {
		d.dependencyTracker.updateDependenciesFrom(kind, key, item)
		if d.flagChangeEventBroadcaster.HasListeners() {
			affectedItems := make(kindAndKeySet)
			d.dependencyTracker.addAffectedItems(affectedItems, kindAndKey{kind, key})
			d.sendChangeEvents(affectedItems)
		}
	}

	return didNotGetError
}

func (d *DataSourceUpdatesImpl) maybeUpdateError(err error) bool {
	if err == nil {
		d.lock.Lock()
		defer d.lock.Unlock()
		d.lastStoreUpdateFailed = false
		return true
	}

	d.UpdateStatus(
		intf.DataSourceStateInterrupted,
		intf.DataSourceErrorInfo{
			Kind:    intf.DataSourceErrorKindStoreError,
			Message: err.Error(),
			Time:    time.Now(),
		},
	)

	shouldLog := false
	d.lock.Lock()
	shouldLog = !d.lastStoreUpdateFailed
	d.lastStoreUpdateFailed = true
	d.lock.Unlock()
	if shouldLog {
		d.loggers.Warnf("Unexpected data store error when trying to store an update received from the data source: %s", err)
	}

	return false
}

//nolint:golint,stylecheck // no doc comment for standard method
func (d *DataSourceUpdatesImpl) UpdateStatus(
	newState intf.DataSourceState,
	newError intf.DataSourceErrorInfo,
) {
	if newState == "" {
		return
	}
	if statusToBroadcast, changed := d.maybeUpdateStatus(newState, newError); changed {
		d.dataSourceStatusBroadcaster.Broadcast(statusToBroadcast)
	}
}

func (d *DataSourceUpdatesImpl) maybeUpdateStatus(
	newState intf.DataSourceState,
	newError intf.DataSourceErrorInfo,
) (intf.DataSourceStatus, bool) {
	d.lock.Lock()
	defer d.lock.Unlock()

	oldStatus := d.currentStatus

	if newState == intf.DataSourceStateInterrupted && oldStatus.State == intf.DataSourceStateInitializing {
		newState = intf.DataSourceStateInitializing // see comment on DataSourceUpdates.UpdateStatus
	}

	if newState == oldStatus.State && newError.Kind == "" {
		return intf.DataSourceStatus{}, false
	}

	stateSince := oldStatus.StateSince
	if newState != oldStatus.State {
		stateSince = time.Now()
	}
	lastError := oldStatus.LastError
	if newError.Kind != "" {
		lastError = newError
	}
	d.currentStatus = intf.DataSourceStatus{
		State:      newState,
		StateSince: stateSince,
		LastError:  lastError,
	}

	d.outageTracker.trackDataSourceState(newState, newError)

	return d.currentStatus, true
}

//nolint:golint,stylecheck // no doc comment for standard method
func (d *DataSourceUpdatesImpl) GetDataStoreStatusProvider() intf.DataStoreStatusProvider {
	return d.dataStoreStatusProvider
}

// GetLastStatus is used internally by SDK components.
func (d *DataSourceUpdatesImpl) GetLastStatus() intf.DataSourceStatus {
	d.lock.Lock()
	defer d.lock.Unlock()
	return d.currentStatus
}

func (d *DataSourceUpdatesImpl) waitFor(desiredState intf.DataSourceState, timeout time.Duration) bool {
	d.lock.Lock()
	if d.currentStatus.State == desiredState {
		d.lock.Unlock()
		return true
	}
	if d.currentStatus.State == intf.DataSourceStateOff {
		d.lock.Unlock()
		return false
	}

	statusCh := d.dataSourceStatusBroadcaster.AddListener()
	defer d.dataSourceStatusBroadcaster.RemoveListener(statusCh)
	d.lock.Unlock()

	var deadline <-chan time.Time
	if timeout > 0 {
		deadline = time.After(timeout)
	}

	for {
		select {
		case newStatus := <-statusCh:
			if newStatus.State == desiredState {
				return true
			}
			if newStatus.State == intf.DataSourceStateOff {
				return false
			}
		case <-deadline:
			return false
		}
	}
}

func (d *DataSourceUpdatesImpl) sendChangeEvents(affectedItems kindAndKeySet) {
	for item := range affectedItems {
		if item.kind == intf.DataKindFeatures() {
			d.flagChangeEventBroadcaster.Broadcast(intf.FlagChangeEvent{Key: item.key})
		}
	}
}

func (d *DataSourceUpdatesImpl) updateDependencyTrackerFromFullDataSet(allData []intf.StoreCollection) {
	d.dependencyTracker.reset()
	for _, coll := range allData {
		for _, item := range coll.Items {
			d.dependencyTracker.updateDependenciesFrom(coll.Kind, item.Key, item.Item)
		}
	}
}

func fullDataSetToMap(allData []intf.StoreCollection) map[intf.StoreDataKind]map[string]intf.StoreItemDescriptor {
	ret := make(map[intf.StoreDataKind]map[string]intf.StoreItemDescriptor, len(allData))
	for _, coll := range allData {
		m := make(map[string]intf.StoreItemDescriptor, len(coll.Items))
		for _, item := range coll.Items {
			m[item.Key] = item.Item
		}
		ret[coll.Kind] = m
	}
	return ret
}

func (d *DataSourceUpdatesImpl) computeChangedItemsForFullDataSet(
	oldDataMap map[intf.StoreDataKind]map[string]intf.StoreItemDescriptor,
	newDataMap map[intf.StoreDataKind]map[string]intf.StoreItemDescriptor,
) kindAndKeySet {
	affectedItems := make(kindAndKeySet)
	for _, kind := range intf.StoreDataKinds() {
		oldItems := oldDataMap[kind]
		newItems := newDataMap[kind]
		allKeys := make([]string, 0, len(oldItems)+len(newItems))
		for key := range oldItems {
			allKeys = append(allKeys, key)
		}
		for key := range newItems {
			if _, found := oldItems[key]; !found {
				allKeys = append(allKeys, key)
			}
		}
		for _, key := range allKeys {
			oldItem, haveOld := oldItems[key]
			newItem, haveNew := newItems[key]
			if haveOld || haveNew {
				if !haveOld || !haveNew || oldItem.Version < newItem.Version {
					d.dependencyTracker.addAffectedItems(affectedItems, kindAndKey{kind, key})
				}
			}
		}
	}
	return affectedItems
}

type outageTracker struct {
	outageLoggingTimeout time.Duration
	loggers              ldlog.Loggers
	inOutage             bool
	errorCounts          map[intf.DataSourceErrorInfo]int
	timeoutCloser        chan struct{}
	lock                 sync.Mutex
}

func newOutageTracker(outageLoggingTimeout time.Duration, loggers ldlog.Loggers) *outageTracker {
	return &outageTracker{
		outageLoggingTimeout: outageLoggingTimeout,
		loggers:              loggers,
	}
}

func (o *outageTracker) trackDataSourceState(newState intf.DataSourceState, newError intf.DataSourceErrorInfo) {
	if o.outageLoggingTimeout == 0 {
		return
	}

	o.lock.Lock()
	defer o.lock.Unlock()

	if newState == intf.DataSourceStateInterrupted || newError.Kind != "" ||
		(newState == intf.DataSourceStateInitializing && o.inOutage) {
		// We are in a potentially recoverable outage. If that wasn't the case already, and if we've been
		// configured with a timeout for logging the outage at a higher level, schedule that timeout.
		if o.inOutage {
			// We were already in one - just record this latest error for logging later.
			o.recordError(newError)
		} else {
			// We weren't already in one, so set the timeout and start recording errors.
			o.inOutage = true
			o.errorCounts = make(map[intf.DataSourceErrorInfo]int)
			o.recordError(newError)
			o.timeoutCloser = make(chan struct{})
			go o.awaitTimeout(o.timeoutCloser)
		}
	} else {
		if o.timeoutCloser != nil {
			close(o.timeoutCloser)
			o.timeoutCloser = nil
		}
		o.inOutage = false
	}
}

func (o *outageTracker) recordError(newError intf.DataSourceErrorInfo) {
	// Accumulate how many times each kind of error has occurred during the outage - use just the basic
	// properties as the key so the map won't expand indefinitely
	basicErrorInfo := intf.DataSourceErrorInfo{Kind: newError.Kind, StatusCode: newError.StatusCode}
	o.errorCounts[basicErrorInfo]++
}

func (o *outageTracker) awaitTimeout(closer chan struct{}) {
	select {
	case <-closer:
		return
	case <-time.After(o.outageLoggingTimeout):
		break
	}

	o.lock.Lock()
	if !o.inOutage {
		// COVERAGE: there is no way to make this happen in unit tests; it is a very unlikely race condition
		o.lock.Unlock()
		return
	}
	errorsDesc := o.describeErrors()
	o.timeoutCloser = nil
	o.lock.Unlock()

	o.loggers.Errorf(
		"LaunchDarkly data source outage - updates have been unavailable for at least %s with the following errors: %s",
		o.outageLoggingTimeout,
		errorsDesc,
	)
}

func (o *outageTracker) describeErrors() string {
	ret := ""
	for err, count := range o.errorCounts {
		if ret != "" {
			ret += ", "
		}
		times := "times"
		if count == 1 {
			times = "time"
		}
		ret += fmt.Sprintf("%s (%d %s)", err, count, times)
	}
	return ret
}
