package filedata

import (
	"os"
	"sync"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/fsnotify/fsnotify"
)

const (
	defaultRetryInterval       = time.Second
	maxRetriesIfFileNotChanged = 2
)

// ArchiveManager manages the file data source.
//
// That includes reading and unarchiving the data file, watching for changes to the file, and maintaining the
// last known state of the data so that it can determine what environmennts if any are affected by a change.
//
// Relay provides an implementation of the UpdateHandler interface which will be called for all changes that
// it needs to know about.
type ArchiveManager struct {
	filePath            string
	handler             UpdateHandler
	retryInterval       time.Duration
	lastKnownEnvs       map[config.EnvironmentID]environmentMetadata
	lastFailureFileInfo os.FileInfo
	watcher             *fsnotify.Watcher
	loggers             ldlog.Loggers
	closeCh             chan struct{}
	closeOnce           sync.Once
}

// NewArchiveManager creates the ArchiveManager instance and attempts to read the initial file data.
func NewArchiveManager(
	filePath string,
	handler UpdateHandler,
	retryInterval time.Duration,
	loggers ldlog.Loggers,
) (*ArchiveManager, error) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, errCannotOpenArchiveFile(filePath, err)
	}

	am := &ArchiveManager{
		filePath:      filePath,
		handler:       handler,
		retryInterval: retryInterval,
		lastKnownEnvs: make(map[config.EnvironmentID]environmentMetadata),
		loggers:       loggers,
		closeCh:       make(chan struct{}),
	}
	if am.retryInterval == 0 {
		am.retryInterval = defaultRetryInterval
	}
	am.loggers.SetPrefix("[FileDataSource]")

	ar, err := newArchiveReader(filePath)
	if err != nil {
		return nil, err
	}
	defer ar.Close()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, errCreateArchiveManagerFailed(filePath, err) // COVERAGE: can't cause this condition in unit tests
	}
	if err := watcher.Add(filePath); err != nil {
		return nil, errCreateArchiveManagerFailed(filePath, err) // COVERAGE: can't cause this condition in unit tests
	}
	am.watcher = watcher

	am.updatedArchive(ar)
	go am.run(fileInfo)

	return am, nil
}

// Close shuts down the ArchiveManager.
func (am *ArchiveManager) Close() {
	am.closeOnce.Do(func() {
		close(am.closeCh)
	})
}

func (am *ArchiveManager) run(originalFileInfo os.FileInfo) {
	lastFileInfo := originalFileInfo
	retryCh := make(chan struct{})
	needRetry := false
	retriedCountSinceLastChange := 0
	var lastError error

	scheduleRetry := func() {
		am.loggers.Debug("Will schedule retry")
		needRetry = true
		time.AfterFunc(am.retryInterval, func() {
			// Use non-blocking write because we never need to queue more than one retry signal
			select {
			case retryCh <- struct{}{}:
				break
			default:
				break
			}
		})
	}

	maybeReload := func() {
		curFileInfo, err := os.Stat(am.filePath)
		if err == nil {
			if fileMayHaveChanged(curFileInfo, lastFileInfo) {
				// If the file's mod time or size has changed, we will always try to reload.
				retriedCountSinceLastChange = 0
				lastError = nil
				am.loggers.Debugf("File info changed: old (size=%d, mtime=%s), new(size=%d, mtime=%s)",
					lastFileInfo.Size(), lastFileInfo.ModTime(), curFileInfo.Size(), curFileInfo.ModTime())
				lastFileInfo = curFileInfo
				ar, err := newArchiveReader(am.filePath)
				needRetry = false
				if err != nil {
					// A failure here might be a real failure, or it might be that the file is being copied
					// over non-atomically so that we're seeing an invalid partial state. So we'll always
					// retry at least once in this case.
					am.loggers.Warnf(logMsgReloadError, err.Error())
					lastError = err
					scheduleRetry()
					return
				}
				am.loggers.Warnf(logMsgReloadedData, am.filePath)
				am.updatedArchive(ar)
				ar.Close()
				return
			}
			am.loggers.Debug("File has not changed")
			if lastError == nil {
				// This was a spurious file watch notification - the file hasn't changed and we're not retrying
				// after an error, so there's nothing to do
				return
			}
		} else {
			if lastError == nil {
				am.loggers.Warn(logMsgReloadFileNotFound)
				lastError = err
			}
		}
		// If we got here, then either the file was not found, or we triggered a delayed retry after
		// an earlier error and the file has not changed since the last failed attempt.
		//
		// So there's no point in trying to reload it now, but it's still possible that there's a slow
		// copy operation in progress, so we'll schedule another retry-- up to a limit. We won't rely on
		// the file watching mechanism for this, because its granularity might be too large to detect
		// consecutive changes that happen close together.
		if retriedCountSinceLastChange < maxRetriesIfFileNotChanged {
			retriedCountSinceLastChange++
			am.loggers.Warn(logMsgReloadUnchangedRetry)
			scheduleRetry()
		} else {
			am.loggers.Errorf(logMsgReloadUnchangedNoMoreRetries, lastError)
		}
	}

	for {
		select {
		case <-am.closeCh:
			am.watcher.Close()
			return

		case event := <-am.watcher.Events:
			am.loggers.Debugf("Got file watcher event: %+v", event)
			// Consume any redundant change events that may have already piled up in the queue
			am.consumeExtraEvents()
			maybeReload()

		case <-retryCh:
			// If needRetry is false, this is an obsolete signal - we've already successfully reloaded
			if needRetry {
				am.loggers.Debug("Got retry signal")
				maybeReload()
			} else {
				am.loggers.Debug("Ignoring obsolete retry signal") // COVERAGE: can't cause this condition in unit tests
			}
		}
	}
}

func (am *ArchiveManager) consumeExtraEvents() {
	for {
		select {
		case <-am.watcher.Events: // COVERAGE: can't simulate this condition in unit tests
		default:
			return
		}
	}
}

func (am *ArchiveManager) updatedArchive(ar *archiveReader) {
	unusedEnvs := make(map[config.EnvironmentID]environmentMetadata)
	for envID, envData := range am.lastKnownEnvs {
		unusedEnvs[envID] = envData
	}
	envIDs := ar.GetEnvironmentIDs()
	if len(envIDs) == 0 {
		am.loggers.Warn(logMsgNoEnvs)
	}
	for _, envID := range envIDs {
		envMetadata, err := ar.GetEnvironmentMetadata(envID)
		if err != nil {
			am.loggers.Errorf(logMsgBadEnvData, envID)
			continue
		}
		envName := envMetadata.params.Identifiers.GetDisplayName()
		delete(unusedEnvs, envID)
		if old, found := am.lastKnownEnvs[envID]; found {
			if old.dataID == envMetadata.dataID && old.version == envMetadata.version {
				continue
			}
			ae := ArchiveEnvironment{Params: envMetadata.params}
			am.loggers.Debugf("*** old = %+v, new = %+v", old, envMetadata)
			if old.dataID != envMetadata.dataID {
				ae.SDKData, err = ar.GetEnvironmentSDKData(envID)
				if err != nil {
					am.loggers.Errorf(logMsgBadEnvData, envID)
					continue
				}
			}
			am.loggers.Infof(logMsgUpdateEnv, envID, envName)
			am.handler.UpdateEnvironment(ae)
		} else {
			ae := ArchiveEnvironment{Params: envMetadata.params}
			ae.SDKData, err = ar.GetEnvironmentSDKData(envID)
			if err != nil {
				am.loggers.Errorf(logMsgBadEnvData, envID)
				continue
			}
			am.loggers.Infof(logMsgAddEnv, envID, envName)
			am.handler.AddEnvironment(ae)
		}
		am.lastKnownEnvs[envID] = envMetadata
	}
	for envID, envData := range unusedEnvs {
		am.loggers.Infof(logMsgDeleteEnv, envID, envData.params.Identifiers.GetDisplayName())
		delete(am.lastKnownEnvs, envID)
		am.handler.DeleteEnvironment(envID)
	}
}

func fileMayHaveChanged(oldInfo, newInfo os.FileInfo) bool {
	return oldInfo.ModTime() != newInfo.ModTime() || oldInfo.Size() != newInfo.Size()
}
