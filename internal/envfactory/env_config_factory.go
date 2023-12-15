package envfactory

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/launchdarkly/ld-relay/v8/config"

	ct "github.com/launchdarkly/go-configtypes"
)

// EnvConfigFactory is an abstraction of the logic for generating environment configurations that
// are partly parameterized, instead of each environment being manually configured. This is used
// in both auto-configuration mode and offline mode.
type EnvConfigFactory struct {
	// DataStorePrefix is the configured data store prefix, which may contain a per-environment placeholder.
	DataStorePrefix string
	// DataStorePrefixTemplate is a Go text/template for a data store prefix, which may reference per-environment values.
	DataStorePrefixTemplate string
	// TableName is the configured data store table name, which may contain a per-environment placeholder.
	TableName string
	// TableNameTemplate is a Go text/template for the data store table name, which may reference per-environment values.
	TableNameTemplate string
	//
	AllowedOrigin ct.OptStringList
	AllowedHeader ct.OptStringList

	// parsedDataStorePrefixTemplate is a parsed text/template.Template representing DataStorePrefixTemplate, if present.
	parsedDataStorePrefixTemplate *template.Template
	// parsedTableNameTemplate is a parsed text/template.Template representing TableNameTemplate, if present.
	parsedTableNameTemplate *template.Template
}

// maybeParseTemplates parses the datastore prefix and tablename templates,
// if present, adding them to the EnvConfigFactory.
func (f *EnvConfigFactory) maybeParseTemplates() error {
	if f.DataStorePrefixTemplate != "" {
		t, err := template.New("datastore-prefix-template").Parse(f.DataStorePrefixTemplate)
		if err != nil {
			return fmt.Errorf("error parsing Datastore prefix template: %w", err)
		}
		f.parsedDataStorePrefixTemplate = t
	}
	if f.TableNameTemplate != "" {
		t, err := template.New("tablename-template").Parse(f.TableNameTemplate)
		if err != nil {
			return fmt.Errorf("error parsing Table name template: %w", err)
		}
		f.parsedTableNameTemplate = t
	}
	return nil
}

// NewEnvConfigFactoryForAutoConfig creates an EnvConfigFactory based on the auto-configuration mode settings.
func NewEnvConfigFactoryForAutoConfig(c config.AutoConfigConfig) (*EnvConfigFactory, error) {
	f := &EnvConfigFactory{
		DataStorePrefix:         c.EnvDatastorePrefix,
		DataStorePrefixTemplate: c.EnvDatastorePrefixTemplate,
		TableName:               c.EnvDatastoreTableName,
		TableNameTemplate:       c.EnvDatastoreTableNameTemplate,
		AllowedOrigin:           c.EnvAllowedOrigin,
		AllowedHeader:           c.EnvAllowedHeader,
	}
	err := f.maybeParseTemplates()
	return f, err
}

// NewEnvConfigFactoryForOfflineMode creates an EnvConfigFactory based on the offline mode settings.
func NewEnvConfigFactoryForOfflineMode(c config.OfflineModeConfig) (*EnvConfigFactory, error) {
	f := &EnvConfigFactory{
		DataStorePrefix:         c.EnvDatastorePrefix,
		DataStorePrefixTemplate: c.EnvDatastorePrefixTemplate,
		TableName:               c.EnvDatastoreTableName,
		TableNameTemplate:       c.EnvDatastoreTableNameTemplate,
		AllowedOrigin:           c.EnvAllowedOrigin,
		AllowedHeader:           c.EnvAllowedHeader,
	}
	err := f.maybeParseTemplates()
	return f, err
}

// MakeEnvironmentConfig creates an EnvConfig based on both the individual EnvironmentParams and the
// properties of the EnvConfigFactory.
func (f EnvConfigFactory) MakeEnvironmentConfig(params EnvironmentParams) (config.EnvConfig, error) {
	prefix, err := maybeSubstituteEnvironment(f.DataStorePrefix, f.parsedDataStorePrefixTemplate, params)
	if err != nil {
		return config.EnvConfig{}, fmt.Errorf("error performing template evaluation for datastore prefix: %w", err)
	}
	tableName, err := maybeSubstituteEnvironment(f.TableName, f.parsedTableNameTemplate, params)
	if err != nil {
		return config.EnvConfig{}, fmt.Errorf("error performing template evaluation for table name: %w", err)
	}

	ret := config.EnvConfig{
		SDKKey:        params.SDKKey,
		MobileKey:     params.MobileKey,
		EnvID:         params.EnvID,
		Prefix:        prefix,
		TableName:     tableName,
		AllowedOrigin: f.AllowedOrigin,
		AllowedHeader: f.AllowedHeader,
		SecureMode:    params.SecureMode,
		FilterKey:     params.Identifiers.FilterKey,
	}
	if params.TTL != 0 {
		ret.TTL = ct.NewOptDuration(params.TTL)
	}

	fmt.Printf("--------------------------------------------------------------------------------\n")
	fmt.Printf("%#v\n", ret)
	fmt.Printf("--------------------------------------------------------------------------------\n")

	return ret, nil
}

// simpleParams holds the attributes passed if for template evaluation
// for database prefixes and table names.
type simpleParams struct {
	// CID holds the value that would be substituted for "$CID" in database prefixes and table names.
	CID string

	// EnvKey is the environment key (normally a lowercase string like "production").
	EnvKey string

	// EnvName is the environment name (normally a title-cased string like "Production").
	EnvName string

	// ProjKey is the project key (normally a lowercase string like "my-application").
	ProjKey string

	// ProjName is the project name (normally a title-cased string like "My Application").
	ProjName string

	// FilterKey is the environment's payload filter. Empty string indicates no filter.
	FilterKey string
}

func maybeSubstituteEnvironment(templateString string, tpl *template.Template, params EnvironmentParams) (string, error) {
	id := string(params.EnvID)
	if params.Identifiers.FilterKey != "" {
		id = id + "." + string(params.Identifiers.FilterKey)
	}

	// If we have no template, use the simple string.
	if tpl == nil {
		return strings.ReplaceAll(templateString, config.AutoConfigEnvironmentIDPlaceholder, id), nil
	}

	sp := simpleParams{
		CID:       id,
		EnvKey:    params.Identifiers.EnvKey,
		EnvName:   params.Identifiers.EnvName,
		ProjKey:   params.Identifiers.ProjKey,
		ProjName:  params.Identifiers.ProjName,
		FilterKey: string(params.Identifiers.FilterKey),
	}
	var b bytes.Buffer
	if err := tpl.Execute(&b, sp); err != nil {
		return "", err
	}
	return b.String(), nil
}
