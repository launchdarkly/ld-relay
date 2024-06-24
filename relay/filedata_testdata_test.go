package relay

import (
	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/internal/envfactory"
	"github.com/launchdarkly/ld-relay/v8/internal/filedata"
	"github.com/launchdarkly/ld-relay/v8/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
	"time"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoreimpl"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems/ldstoretypes"
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

func RotateSDKKey(primary config.SDKKey) filedata.ArchiveEnvironment {
	return RotateSDKKeyWithGracePeriod(primary, "", time.Time{})
}

func RotateSDKKeyWithGracePeriod(primary config.SDKKey, expiring config.SDKKey, expiry time.Time) filedata.ArchiveEnvironment {
	return filedata.ArchiveEnvironment{
		Params: envfactory.EnvironmentParams{
			EnvID:  "env1",
			SDKKey: primary,
			ExpiringSDKKey: envfactory.ExpiringSDKKey{
				Key:        expiring,
				Expiration: expiry,
			},
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
		}}
}
