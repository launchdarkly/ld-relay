package relay

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/sharedtest/testenv"

	"github.com/launchdarkly/go-sdk-common/v3/lduser"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v2/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v6/interfaces/ldstoretypes"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents/ldstoreimpl"
)

func BenchmarkEvaluateAllFlags(b *testing.B) {
	numFlags := 50

	allData := []ldstoretypes.Collection{
		{
			Kind:  ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{},
		},
		{
			Kind:  ldstoreimpl.Segments(),
			Items: []ldstoretypes.KeyedItemDescriptor{},
		},
	}

	for i := 0; i < numFlags; i++ {
		flag := ldbuilders.NewFlagBuilder(fmt.Sprintf("flag%d", i)).Version(1).
			SingleVariation(ldvalue.String(fmt.Sprintf("value%d", i))).
			ClientSideUsingEnvironmentID(true).
			Build()
		allData[0].Items = append(allData[0].Items, ldstoretypes.KeyedItemDescriptor{flag.Key, sharedtest.FlagDesc(flag)})
	}

	user := lduser.NewUserBuilder("user-key").Name("name").Email("email").Custom("a", ldvalue.String("b")).Build()

	store := sharedtest.NewInMemoryStore()
	store.Init(allData)
	ctx := testenv.NewTestEnvContext("", false, store)
	userData := []byte(user.String())
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := buildPreRoutedRequest("REPORT", userData, headers, nil, ctx)
		resp := httptest.NewRecorder()
		evaluateAllFeatureFlags(basictypes.JSClientSDK)(resp, req)
	}
}
