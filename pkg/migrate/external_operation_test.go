package migrate_test

import "github.com/bogdanovich/mintclaw/pkg/migrate"

type externalOperation struct{}

var _ migrate.Operation = externalOperation{}

func (externalOperation) GetSourceName() string                       { return "external" }
func (externalOperation) GetSourceHome() (string, error)              { return "", nil }
func (externalOperation) GetSourceWorkspace() (string, error)         { return "", nil }
func (externalOperation) GetSourceConfigFile() (string, error)        { return "", nil }
func (externalOperation) ExecuteConfigMigration(string, string) error { return nil }
func (externalOperation) WorkspaceFiles() []migrate.WorkspaceFile     { return nil }
func (externalOperation) WorkspaceDirs() []string                     { return nil }
