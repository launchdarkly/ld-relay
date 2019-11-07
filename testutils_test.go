package relay

import (
	"bufio"
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
	"gopkg.in/launchdarkly/go-server-sdk.v4/ldlog"
)

var nullLogger = log.New(ioutil.Discard, "", 0)
var emptyStore = ld.NewInMemoryFeatureStore(nullLogger)
var zero = 0

func makeNullLoggers() ldlog.Loggers {
	ls := ldlog.Loggers{}
	ls.SetMinLevel(ldlog.None)
	return ls
}

type testFlag struct {
	flag              ld.FeatureFlag
	expectedValue     interface{}
	expectedVariation int
	expectedReason    map[string]interface{}
	isExperiment      bool
}

var flag1ServerSide = testFlag{
	flag:              ld.FeatureFlag{Key: "some-flag-key", OffVariation: &zero, Variations: []interface{}{true}, Version: 2},
	expectedValue:     true,
	expectedVariation: 0,
	expectedReason:    map[string]interface{}{"kind": "OFF"},
}
var flag2ServerSide = testFlag{
	flag:              ld.FeatureFlag{Key: "another-flag-key", On: true, Fallthrough: ld.VariationOrRollout{Variation: &zero}, Variations: []interface{}{3}, Version: 1},
	expectedValue:     3,
	expectedVariation: 0,
	expectedReason:    map[string]interface{}{"kind": "FALLTHROUGH"},
}
var flag3ServerSide = testFlag{
	flag:           ld.FeatureFlag{Key: "off-variation-key", Version: 3},
	expectedValue:  nil,
	expectedReason: map[string]interface{}{"kind": "OFF"},
}
var flag4ClientSide = testFlag{
	flag:              ld.FeatureFlag{Key: "client-flag-key", OffVariation: &zero, Variations: []interface{}{5}, Version: 2, ClientSide: true},
	expectedValue:     5,
	expectedVariation: 0,
	expectedReason:    map[string]interface{}{"kind": "OFF"},
}
var flag5ClientSide = testFlag{
	flag: ld.FeatureFlag{
		Key:                    "fallthrough-experiment-flag-key",
		On:                     true,
		Fallthrough:            ld.VariationOrRollout{Variation: &zero},
		Variations:             []interface{}{3},
		TrackEventsFallthrough: true,
		Version:                1,
		ClientSide:             true,
	},
	expectedValue:  3,
	expectedReason: map[string]interface{}{"kind": "FALLTHROUGH"},
	isExperiment:   true,
}
var flag6ClientSide = testFlag{
	flag: ld.FeatureFlag{
		Key:         "rule-match-experiment-flag-key",
		On:          true,
		Fallthrough: ld.VariationOrRollout{},
		Rules: []ld.Rule{
			ld.Rule{
				VariationOrRollout: ld.VariationOrRollout{Variation: &zero},
				ID:                 "rule-id",
				TrackEvents:        true,
				Clauses: []ld.Clause{
					ld.Clause{
						Attribute: "key",
						Op:        "in",
						Values:    []interface{}{"not-a-real-user-key "},
						Negate:    true,
					},
				},
			},
		},
		Variations: []interface{}{4},
		Version:    1,
		ClientSide: true,
	},
	expectedValue:  4,
	expectedReason: map[string]interface{}{"kind": "RULE_MATCH", "ruleIndex": 0, "ruleId": "rule-id"},
	isExperiment:   true,
}
var allFlags = []testFlag{flag1ServerSide, flag2ServerSide, flag3ServerSide, flag4ClientSide,
	flag5ClientSide, flag6ClientSide}
var clientSideFlags = []testFlag{flag4ClientSide, flag5ClientSide, flag6ClientSide}

var segment1 = ld.Segment{Key: "segment-key"}

// Returns a key matching the UUID header pattern
func key() string {
	return "mob-ffffffff-ffff-4fff-afff-ffffffffffff"
}

func user() string {
	return "eyJrZXkiOiJ0ZXN0In0="
}

func makeStoreWithData(initialized bool) ld.FeatureStore {
	store := ld.NewInMemoryFeatureStore(nullLogger)
	addAllFlags(store, initialized)
	return store
}

func addAllFlags(store ld.FeatureStore, initialized bool) {
	if initialized {
		store.Init(nil)
	}
	for _, flag := range allFlags {
		f := flag
		store.Upsert(ld.Features, &f.flag)
	}
	store.Upsert(ld.Segments, &segment1)
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
		client:  FakeLDClient{initialized: true},
		store:   makeStoreWithData(true),
		loggers: makeNullLoggers(),
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
