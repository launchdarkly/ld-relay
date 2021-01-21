package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/internal/core/sdks"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest/testenv"

	"gopkg.in/launchdarkly/go-sdk-common.v2/lduser"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
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
		evaluateAllFeatureFlags(sdks.JSClient)(resp, req)
	}
}
