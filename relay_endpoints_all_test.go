package relay

import "testing"

func DoAllCoreEndpointTests(t *testing.T, constructor TestConstructor) {
	constructor.RunTest(t, "evaluation endpoints", DoEvalEndpointsTests)
	constructor.RunTest(t, "stream endpoints", DoStreamEndpointsTests)
	constructor.RunTest(t, "PHP polling", DoPHPPollingEndpointsTests)
	constructor.RunTest(t, "event forwarding", DoEventProxyTests)
	constructor.RunTest(t, "goals", DoJSClientGoalsEndpointTest)
	constructor.RunTest(t, "status", DoStatusEndpointTest)
}
