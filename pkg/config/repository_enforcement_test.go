package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProductionConfigWritersUseRepository(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve enforcement test path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	for _, productionRoot := range []string{"cmd", "pkg", "web"} {
		root := filepath.Join(repositoryRoot, productionRoot)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "vendor" || entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			return rejectLegacyConfigWriter(t, repositoryRoot, path)
		})
		if err != nil {
			t.Fatalf("scan production config writers under %s: %v", productionRoot, err)
		}
	}
}

func rejectLegacyConfigWriter(t *testing.T, repositoryRoot, path string) error {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		return err
	}
	configAliases := make(map[string]struct{})
	for _, importSpec := range file.Imports {
		if strings.Trim(importSpec.Path.Value, `"`) != "github.com/bogdanovich/mintclaw/pkg/config" {
			continue
		}
		alias := "config"
		if importSpec.Name != nil {
			alias = importSpec.Name.Name
		}
		configAliases[alias] = struct{}{}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		violation := ""
		switch function := call.Fun.(type) {
		case *ast.Ident:
			_, dotConfigImport := configAliases["."]
			if function.Name == "SaveConfig" && (file.Name.Name == "config" || dotConfigImport) {
				violation = "legacy SaveConfig writer"
			}
		case *ast.SelectorExpr:
			receiver, receiverOK := function.X.(*ast.Ident)
			if receiverOK {
				_, configImport := configAliases[receiver.Name]
				if configImport && function.Sel.Name == "SaveConfig" {
					violation = "legacy SaveConfig writer"
				}
			}
			if violation == "" && isConfigFileMutation(function.Sel.Name, call.Args) &&
				!strings.HasPrefix(path, filepath.Join(repositoryRoot, "pkg", "config")+string(filepath.Separator)) {
				violation = "direct config file writer"
			}
		}
		if violation != "" {
			position := fileSet.Position(call.Pos())
			relativePath, relErr := filepath.Rel(repositoryRoot, path)
			if relErr != nil {
				relativePath = path
			}
			t.Errorf("%s %s:%d must use config.Repository", violation, relativePath, position.Line)
		}
		return true
	})
	return nil
}

func isConfigFileMutation(functionName string, args []ast.Expr) bool {
	switch functionName {
	case "WriteFile", "WriteFileAtomic", "Create", "OpenFile", "Rename", "ReplaceFile", "CopyFile":
	default:
		return false
	}
	for _, argument := range args {
		matched := false
		ast.Inspect(argument, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.Ident:
				name := strings.ToLower(value.Name)
				matched = matched || strings.Contains(name, "configpath") || strings.Contains(name, "securitypath")
			case *ast.SelectorExpr:
				matched = matched || value.Sel.Name == "GetConfigPath" || value.Sel.Name == "SecurityConfigFile"
			case *ast.BasicLit:
				literal := strings.ToLower(value.Value)
				matched = matched || strings.Contains(literal, "config.json") ||
					strings.Contains(literal, ".security.yml")
			}
			return !matched
		})
		if matched {
			return true
		}
	}
	return false
}
