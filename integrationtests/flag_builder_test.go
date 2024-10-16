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
	helper               *apiHelper
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
	return fmt.Sprintf("%s in %s/%s", desc, f.projectKey, f.envKey)
}

func (f *flagBuilder) logAPISuccess(desc string) {
	f.helper.loggers.Infof(f.scopedOp(desc))
}

func newFlagBuilder(helper *apiHelper, flagKey string, projectKey string, envKey string) *flagBuilder {
	builder := &flagBuilder{
		key:                  flagKey,
		projectKey:           projectKey,
		envKey:               envKey,
		on:                   true,
		offVariation:         0,
		fallthroughVariation: 1,
		helper:               helper,
	}
	return builder.Variations(ldvalue.Bool(false), ldvalue.Bool(true))
}

func (f *flagBuilder) Variations(variation1 ldvalue.Value, variations ...ldvalue.Value) *flagBuilder {
	f.variations = nil
	for _, value := range append([]ldvalue.Value{variation1}, variations...) {
		valueAsInterface := value.AsArbitraryValue()
		f.variations = append(f.variations, ldapi.Variation{Value: &valueAsInterface})
	}
	return f
}

func (f *flagBuilder) Prerequisites(prerequisites []ldapi.Prerequisite) *flagBuilder {
	f.prerequisites = prerequisites
	return f
}

func (f *flagBuilder) Prerequisite(prerequisiteKey string, variation int32) *flagBuilder {
	return f.Prerequisites([]ldapi.Prerequisite{{Key: prerequisiteKey, Variation: variation}})
}

func (f *flagBuilder) OffVariation(v int) *flagBuilder {
	f.offVariation = v
	return f
}

func (f *flagBuilder) FallthroughVariation(v int) *flagBuilder {
	f.fallthroughVariation = v
	return f
}

func (f *flagBuilder) On(on bool) *flagBuilder {
	f.on = on
	return f
}

func (f *flagBuilder) Create() error {

	if len(f.variations) < 2 {
		return errors.New("must have >= 2 variations")
	}
	if f.offVariation < 0 || f.offVariation >= len(f.variations) {
		return errors.New("offVariation out of range")
	}
	if f.fallthroughVariation < 0 || f.fallthroughVariation >= len(f.variations) {
		return errors.New("fallthroughVariation out of range")
	}

	flagPost := ldapi.FeatureFlagBody{
		Name: f.key,
		Key:  f.key,
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
