package sharedtest

import (
	"fmt"
	"testing"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlogtest"
)

func DumpLogIfTestFailed(t *testing.T, mockLog *ldlogtest.MockLog) {
	if t.Failed() {
		for _, line := range mockLog.GetAllOutput() {
			fmt.Println(line.Level.Name() + ": " + line.Message)
		}
	}
}
