//go:build integrationtests

package integrationtests

import (
	"errors"
	"fmt"

	ldapi "github.com/launchdarkly/api-client-go/v13"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
)

type flagBuilder struct {
	key                  string
	projectKey           string
	envKey               string
	offVariation         int
	fallthroughVariation int
	on                   bool
	variations           []ldapi.Variation
	prerequisites        []ldapi.Prerequisite
	clientSide           ldapi.ClientSideAvailabilityPost
	helper               *apiHelper
}

// newFlagBuilder creates a builder for a flag which will be created in the specified project and environment.
// By default, the flag has two variations: off = false, and on = true. The flag is on by default.
// Additionally, the flag is available to both mobile and client SDKs.
func newFlagBuilder(helper *apiHelper, flagKey string, projectKey string, envKey string) *flagBuilder {
	builder := &flagBuilder{
		key:                  flagKey,
		projectKey:           projectKey,
		envKey:               envKey,
		on:                   true,
		offVariation:         0,
		fallthroughVariation: 1,
		helper:               helper,
		clientSide: ldapi.ClientSideAvailabilityPost{
			UsingMobileKey:     true,
			UsingEnvironmentId: true,
		},
	}
	return builder.Variations(ldvalue.Bool(false), ldvalue.Bool(true))
}

// Variations overwrites the flag's variations. A valid flag has two or more variations.
func (f *flagBuilder) Variations(variation1 ldvalue.Value, variations ...ldvalue.Value) *flagBuilder {
	f.variations = nil
	for _, value := range append([]ldvalue.Value{variation1}, variations...) {
		valueAsInterface := value.AsArbitraryValue()
		f.variations = append(f.variations, ldapi.Variation{Value: &valueAsInterface})
	}
	return f
}

// ClientSideUsingEnvironmentID enables the flag for clients that use environment ID for auth.
func (f *flagBuilder) ClientSideUsingEnvironmentID(usingEnvID bool) *flagBuilder {
	f.clientSide.UsingEnvironmentId = usingEnvID
	return f
}

// Prerequisites overwrites the flag's prerequisites.
func (f *flagBuilder) Prerequisites(prerequisites []ldapi.Prerequisite) *flagBuilder {
	f.prerequisites = prerequisites
	return f
}

// Prerequisite is a helper that calls Prerequisites with a single value.
func (f *flagBuilder) Prerequisite(prerequisiteKey string, variation int32) *flagBuilder {
	return f.Prerequisites([]ldapi.Prerequisite{{Key: prerequisiteKey, Variation: variation}})
}

// OffVariation sets the flag's off variation.
func (f *flagBuilder) OffVariation(v int) *flagBuilder {
	f.offVariation = v
	return f
}

// FallthroughVariation sets the flag's fallthrough variation.
func (f *flagBuilder) FallthroughVariation(v int) *flagBuilder {
	f.fallthroughVariation = v
	return f
}

// On enables or disables flag targeting.
func (f *flagBuilder) On(on bool) *flagBuilder {
	f.on = on
	return f
}

// Create creates the flag using the LD REST API.
func (f *flagBuilder) Create() error {
	flagPost := ldapi.FeatureFlagBody{
		Name:                   f.key,
		Key:                    f.key,
		ClientSideAvailability: &f.clientSide,
	}

	_, _, err := f.helper.apiClient.FeatureFlagsApi.
		PostFeatureFlag(f.helper.apiContext, f.projectKey).
		FeatureFlagBody(flagPost).
		Execute()

	if err != nil {
		return f.logAPIError("create flag", err)
	} else {
		f.logAPISuccess("create flag")
	}

	envPrefix := fmt.Sprintf("/environments/%s", f.envKey)
	patch := ldapi.PatchWithComment{
		Patch: []ldapi.PatchOperation{
			makePatch("replace", envPrefix+"/offVariation", f.offVariation),
			makePatch("replace", envPrefix+"/fallthrough/variation", f.fallthroughVariation),
			makePatch("replace", envPrefix+"/on", f.on),
			makePatch("replace", envPrefix+"/prerequisites", f.prerequisites),
		},
	}

	_, _, err = f.helper.apiClient.FeatureFlagsApi.
		PatchFeatureFlag(f.helper.apiContext, f.projectKey, f.key).
		PatchWithComment(patch).
		Execute()

	if err != nil {
		return f.logAPIError("patch flag", err)
	} else {
		f.logAPISuccess("patch flag")
	}

	return nil
}

func (f *flagBuilder) logAPIError(desc string, err error) error {
	var apiError ldapi.GenericOpenAPIError
	if errors.As(err, &apiError) {
		body := string(apiError.Body())
		f.helper.loggers.Errorf("%s: %s (response body: %s)", f.scopedOp(desc), err, body)
	} else {
		f.helper.loggers.Errorf("%s: %s", f.scopedOp(desc), err)
	}
	return err
}

func (f *flagBuilder) scopedOp(desc string) string {
	return fmt.Sprintf("%s %s in %s/%s", desc, f.key, f.projectKey, f.envKey)
}

func (f *flagBuilder) logAPISuccess(desc string) {
	f.helper.loggers.Infof(f.scopedOp(desc))
}
