package relay

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/internal/store"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"

	"github.com/stretchr/testify/assert"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

var emptyStore = sharedtest.NewInMemoryStore()
var emptyStoreAdapter = store.NewSSERelayDataStoreAdapterWithExistingStore(emptyStore)
var zero = 0

type testFlag struct {
	flag              ldmodel.FeatureFlag
	expectedValue     interface{}
	expectedVariation int
	expectedReason    map[string]interface{}
	isExperiment      bool
}

var flag1ServerSide = testFlag{
	flag:              ldbuilders.NewFlagBuilder("some-flag-key").OffVariation(0).Variations(ldvalue.Bool(true)).Version(2).Build(),
	expectedValue:     true,
	expectedVariation: 0,
	expectedReason:    map[string]interface{}{"kind": "OFF"},
}
var flag2ServerSide = testFlag{
	flag:              ldbuilders.NewFlagBuilder("another-flag-key").On(true).FallthroughVariation(0).Variations(ldvalue.Int(3)).Version(1).Build(),
	expectedValue:     3,
	expectedVariation: 0,
	expectedReason:    map[string]interface{}{"kind": "FALLTHROUGH"},
}
var flag3ServerSide = testFlag{
	flag:           ldbuilders.NewFlagBuilder("off-variation-key").Version(3).Build(),
	expectedValue:  nil,
	expectedReason: map[string]interface{}{"kind": "OFF"},
}
var flag4ClientSide = testFlag{
	flag:              ldbuilders.NewFlagBuilder("client-flag-key").OffVariation(0).Variations(ldvalue.Int(5)).Version(2).ClientSide(true).Build(),
	expectedValue:     5,
	expectedVariation: 0,
	expectedReason:    map[string]interface{}{"kind": "OFF"},
}
var flag5ClientSide = testFlag{
	flag: ldbuilders.NewFlagBuilder("fallthrough-experiment-flag-key").On(true).FallthroughVariation(0).Variations(ldvalue.Int(3)).
		TrackEventsFallthrough(true).ClientSide(true).Version(1).Build(),
	expectedValue:  3,
	expectedReason: map[string]interface{}{"kind": "FALLTHROUGH"},
	isExperiment:   true,
}
var flag6ClientSide = testFlag{
	flag: ldbuilders.NewFlagBuilder("rule-match-experiment-flag-key").On(true).
		AddRule(ldbuilders.NewRuleBuilder().ID("rule-id").Variation(0).TrackEvents(true).
			Clauses(ldbuilders.Negate(ldbuilders.Clause(lduser.KeyAttribute, ldmodel.OperatorIn, ldvalue.String("not-a-real-user-key"))))).
		Variations(ldvalue.Int(4)).ClientSide(true).Version(1).Build(),
	expectedValue:  4,
	expectedReason: map[string]interface{}{"kind": "RULE_MATCH", "ruleIndex": 0, "ruleId": "rule-id"},
	isExperiment:   true,
}
var allFlags = []testFlag{flag1ServerSide, flag2ServerSide, flag3ServerSide, flag4ClientSide,
	flag5ClientSide, flag6ClientSide}
var clientSideFlags = []testFlag{flag4ClientSide, flag5ClientSide, flag6ClientSide}

var segment1 = ldbuilders.NewSegmentBuilder("segment-key").Build()

// Returns a key matching the UUID header pattern
func key() string {
	return "mob-ffffffff-ffff-4fff-afff-ffffffffffff"
}

func user() string {
	return "eyJrZXkiOiJ0ZXN0In0="
}

func makeStoreWithData(initialized bool) interfaces.DataStore {
	store := sharedtest.NewInMemoryStore()
	addAllFlags(store, initialized)
	return store
}

func addAllFlags(store interfaces.DataStore, initialized bool) {
	if initialized {
		store.Init(nil)
	}
	for _, flag := range allFlags {
		sharedtest.UpsertFlag(store, flag.flag)
	}
	sharedtest.UpsertSegment(store, segment1)
}

func flagsMap(testFlags []testFlag) map[string]interface{} {
	ret := make(map[string]interface{})
	for _, f := range testFlags {
		ret[f.flag.Key] = f.flag
	}
	return ret
}

func makeTestContextWithData() *clientContextImpl {
	return &clientContextImpl{
		client:       FakeLDClient{initialized: true},
		storeAdapter: store.NewSSERelayDataStoreAdapterWithExistingStore(makeStoreWithData(true)),
		loggers:      ldlog.NewDisabledLoggers(),
	}
}

func makeEvalBody(flags []testFlag, fullData bool, reasons bool) string {
	obj := make(map[string]interface{})
	for _, f := range flags {
		value := f.expectedValue
		if fullData {
			m := map[string]interface{}{"value": value, "version": f.flag.Version}
			if value != nil {
				m["variation"] = f.expectedVariation
			}
			if reasons || f.isExperiment {
				m["reason"] = f.expectedReason
			}
			if f.flag.TrackEvents || f.isExperiment {
				m["trackEvents"] = true
			}
			if f.isExperiment {
				m["trackReason"] = true
			}
			value = m
		}
		obj[f.flag.Key] = value
	}
	out, _ := json.Marshal(obj)
	return string(out)
}

// jsonFind returns the nested entity at a path in a json obj
func jsonFind(obj map[string]interface{}, paths ...string) interface{} {
	var value interface{} = obj
	for _, p := range paths {
		if v, ok := value.(map[string]interface{}); !ok {
			return nil
		} else {
			value = v[p]
		}
	}
	return value
}

type bodyMatcher func(t *testing.T, body []byte)

func expectBody(expectedBody string) bodyMatcher {
	return func(t *testing.T, body []byte) {
		assert.EqualValues(t, expectedBody, body)
	}
}

func expectJSONBody(expectedBody string) bodyMatcher {
	return func(t *testing.T, body []byte) {
		assert.JSONEq(t, expectedBody, string(body))
	}
}

type StreamRecorder struct {
	*bufio.Writer
	*httptest.ResponseRecorder
	closer chan bool
}

func (r StreamRecorder) CloseNotify() <-chan bool {
	return r.closer
}

func (r StreamRecorder) Close() {
	r.closer <- true
}

func (r StreamRecorder) Write(data []byte) (int, error) {
	return r.Writer.Write(data)
}

func (r StreamRecorder) Flush() {
	r.Writer.Flush()
}

func NewStreamRecorder() (StreamRecorder, io.Reader) {
	reader, writer := io.Pipe()
	recorder := httptest.NewRecorder()
	return StreamRecorder{
		ResponseRecorder: recorder,
		Writer:           bufio.NewWriter(writer),
		closer:           make(chan bool),
	}, reader
}
