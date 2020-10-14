package relay

import (
	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/core/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/core/sharedtest"
	"github.com/launchdarkly/ld-relay/v6/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v6/internal/filedata"

	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldbuilders"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces/ldstoretypes"
	"gopkg.in/launchdarkly/go-server-sdk.v5/ldcomponents/ldstoreimpl"
)

var testFileDataFlag1 = ldbuilders.NewFlagBuilder("flag1").Version(1).Build()
var testFileDataFlag2 = ldbuilders.NewFlagBuilder("flag2").Version(1).Build()

var testFileDataEnv1 = filedata.ArchiveEnvironment{
	Params: envfactory.EnvironmentParams{
		EnvID:     config.EnvironmentID("env1"),
		SDKKey:    config.SDKKey("sdkkey1"),
		MobileKey: config.MobileKey("mobilekey1"),
		Identifiers: relayenv.EnvIdentifiers{
			ProjName: "Project",
			ProjKey:  "project",
			EnvName:  "Env1",
			EnvKey:   "env1",
		},
	},
	SDKData: []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: testFileDataFlag1.Key, Item: sharedtest.FlagDesc(testFileDataFlag1)},
			},
		},
	},
}

var testFileDataEnv2 = filedata.ArchiveEnvironment{
	Params: envfactory.EnvironmentParams{
		EnvID:     config.EnvironmentID("env2"),
		SDKKey:    config.SDKKey("sdkkey2"),
		MobileKey: config.MobileKey("mobilekey2"),
		Identifiers: relayenv.EnvIdentifiers{
			ProjName: "Project",
			ProjKey:  "project",
			EnvName:  "Env2",
			EnvKey:   "env2",
		},
	},
	SDKData: []ldstoretypes.Collection{
		{
			Kind: ldstoreimpl.Features(),
			Items: []ldstoretypes.KeyedItemDescriptor{
				{Key: testFileDataFlag2.Key, Item: sharedtest.FlagDesc(testFileDataFlag2)},
			},
		},
	},
}
