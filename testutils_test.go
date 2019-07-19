package relay

import (
	"bufio"
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
)

var nullLogger = log.New(ioutil.Discard, "", 0)
var emptyStore = ld.NewInMemoryFeatureStore(nullLogger)
var zero = 0

type testFlag struct {
	flag              ld.FeatureFlag
	expectedValue     interface{}
	expectedVariation int
	expectedReason    ld.EvalReasonKind
}

var flag1ServerSide = testFlag{
	flag:              ld.FeatureFlag{Key: "some-flag-key", OffVariation: &zero, Variations: []interface{}{true}, Version: 2},
	expectedValue:     true,
	expectedVariation: 0,
	expectedReason:    ld.EvalReasonOff,
}
var flag2ServerSide = testFlag{
	flag:              ld.FeatureFlag{Key: "another-flag-key", On: true, Fallthrough: ld.VariationOrRollout{Variation: &zero}, Variations: []interface{}{3}, Version: 1},
	expectedValue:     3,
	expectedVariation: 0,
	expectedReason:    ld.EvalReasonFallthrough,
}
var flag3ServerSide = testFlag{
	flag:           ld.FeatureFlag{Key: "off-variation-key", Version: 3},
	expectedValue:  nil,
	expectedReason: ld.EvalReasonOff,
}
var flag4ClientSide = testFlag{
	flag:              ld.FeatureFlag{Key: "client-flag-key", OffVariation: &zero, Variations: []interface{}{5}, Version: 2, ClientSide: true},
	expectedValue:     5,
	expectedVariation: 0,
	expectedReason:    ld.EvalReasonOff,
}
var allFlags = []testFlag{flag1ServerSide, flag2ServerSide, flag3ServerSide, flag4ClientSide}
var clientSideFlags = []testFlag{flag4ClientSide}

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
		client: FakeLDClient{initialized: true},
		store:  makeStoreWithData(true),
		logger: nullLogger,
	}
}

func makeEvalBody(flags []testFlag, fullData bool, reasons bool) string {
	items := []string{}
	for _, f := range flags {
		value := f.expectedValue
		if fullData {
			m := map[string]interface{}{"value": value, "version": f.flag.Version, "trackEvents": f.flag.TrackEvents}
			if value != nil {
				m["variation"] = f.expectedVariation
			}
			if reasons {
				m["reason"] = map[string]interface{}{"kind": f.expectedReason}
			}
			value = m
		}
		j, _ := json.Marshal(value)
		s := "\"" + f.flag.Key + "\":" + string(j)
		items = append(items, s)
	}
	return "{" + strings.Join(items, ",") + "}"
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
