package autoconf

type AutoConfigEnvironmentRep struct {
	EnvID      string `json:"envID"`
	EnvKey     string `json:"envKey"`
	EnvName    string `json:"envName"`
	MobKey     string `json:"mobKey"`
	ProjKey    string `json:"projKey"`
	ProjName   string `json:"projName"`
	SDKKey     string `json:"sdkKey"`
	DefaultTTL int    `json:"defaultTtl"`
	SecureMode bool   `json:"secureMode"`
}
