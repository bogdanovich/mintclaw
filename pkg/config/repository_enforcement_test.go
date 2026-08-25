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
			return rejectDirectConfigWriter(t, repositoryRoot, path)
		})
		if err != nil {
			t.Fatalf("scan production config writers under %s: %v", productionRoot, err)
		}
	}
}

func rejectDirectConfigWriter(t *testing.T, repositoryRoot, path string) error {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		return err
	}
	configPathAliases := collectConfigPathAliases(file)

	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		function, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isConfigFileMutation(function.Sel.Name, call.Args, configPathAliases) ||
			strings.HasPrefix(path, filepath.Join(repositoryRoot, "pkg", "config")+string(filepath.Separator)) {
			return true
		}
		position := fileSet.Position(call.Pos())
		relativePath, relErr := filepath.Rel(repositoryRoot, path)
		if relErr != nil {
			relativePath = path
		}
		t.Errorf("direct config file writer %s:%d must use config.Repository", relativePath, position.Line)
		return true
	})
	return nil
}

func collectConfigPathAliases(file *ast.File) map[string]struct{} {
	aliases := make(map[string]struct{})
	changed := true
	for changed {
		changed = false
		ast.Inspect(file, func(node ast.Node) bool {
			switch declaration := node.(type) {
			case *ast.AssignStmt:
				for index, value := range declaration.Rhs {
					if index >= len(declaration.Lhs) || !expressionDerivesConfigPath(value, aliases) {
						continue
					}
					identifier, ok := declaration.Lhs[index].(*ast.Ident)
					if !ok {
						continue
					}
					if _, exists := aliases[identifier.Name]; !exists {
						aliases[identifier.Name] = struct{}{}
						changed = true
					}
				}
			case *ast.ValueSpec:
				for index, value := range declaration.Values {
					if index >= len(declaration.Names) || !expressionDerivesConfigPath(value, aliases) {
						continue
					}
					name := declaration.Names[index].Name
					if _, exists := aliases[name]; !exists {
						aliases[name] = struct{}{}
						changed = true
					}
				}
			}
			return true
		})
	}
	return aliases
}

func isConfigFileMutation(functionName string, args []ast.Expr, aliases map[string]struct{}) bool {
	pathArgumentCount := 1
	switch functionName {
	case "WriteFile", "WriteFileAtomic", "Create", "OpenFile", "Rename", "ReplaceFile", "CopyFile", "Remove",
		"RemoveAll", "Truncate":
		if functionName == "Rename" || functionName == "ReplaceFile" || functionName == "CopyFile" {
			pathArgumentCount = 2
		}
	default:
		return false
	}
	for index, argument := range args {
		if index >= pathArgumentCount {
			break
		}
		if expressionUsesConfigPath(argument, aliases) {
			return true
		}
	}
	return false
}

func expressionDerivesConfigPath(expression ast.Expr, aliases map[string]struct{}) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		_, knownAlias := aliases[value.Name]
		name := strings.ToLower(value.Name)
		return knownAlias || strings.Contains(name, "configpath") || strings.Contains(name, "securitypath")
	case *ast.SelectorExpr:
		return value.Sel.Name == "GetConfigPath" || value.Sel.Name == "SecurityConfigFile"
	case *ast.BasicLit:
		literal := strings.ToLower(value.Value)
		return strings.Contains(literal, "config.json") || strings.Contains(literal, ".security.yml")
	case *ast.ParenExpr:
		return expressionDerivesConfigPath(value.X, aliases)
	case *ast.CallExpr:
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
			if selector.Sel.Name == "GetConfigPath" {
				return true
			}
			switch selector.Sel.Name {
			case "Join", "Clean", "Abs", "EvalSymlinks", "Dir", "Base":
				for _, argument := range value.Args {
					if expressionUsesConfigPath(argument, aliases) {
						return true
					}
				}
			}
		}
	}
	return false
}

func expressionUsesConfigPath(expression ast.Expr, aliases map[string]struct{}) bool {
	matched := false
	ast.Inspect(expression, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.Ident:
			name := strings.ToLower(value.Name)
			_, knownAlias := aliases[value.Name]
			matched = matched || knownAlias || strings.Contains(name, "configpath") ||
				strings.Contains(name, "securitypath")
		case *ast.SelectorExpr:
			matched = matched || value.Sel.Name == "GetConfigPath" || value.Sel.Name == "SecurityConfigFile"
		case *ast.BasicLit:
			literal := strings.ToLower(value.Value)
			matched = matched || strings.Contains(literal, "config.json") ||
				strings.Contains(literal, ".security.yml")
		}
		return !matched
	})
	return matched
}

func TestConfigWriterEnforcementTracksCanonicalPathAliases(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "fixture.go", `package fixture
func writeDirectly() {
	cfgPath := internal.GetConfigPath()
	_ = os.WriteFile(cfgPath, nil, 0o600)
}`, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	aliases := collectConfigPathAliases(file)
	if _, exists := aliases["cfgPath"]; !exists {
		t.Fatal("cfgPath derived from GetConfigPath was not tracked")
	}
	mutationFound := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if function, ok := call.Fun.(*ast.SelectorExpr); ok {
			mutationFound = mutationFound || isConfigFileMutation(function.Sel.Name, call.Args, aliases)
		}
		return true
	})
	if !mutationFound {
		t.Fatal("direct write through cfgPath alias was not detected")
	}
}

func TestConfigWriterEnforcementRejectsDeleteThroughCanonicalPathAlias(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "fixture.go", `package fixture
func deleteDirectly() {
	cfgPath := internal.GetConfigPath()
	_ = os.Remove(cfgPath)
}`, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	aliases := collectConfigPathAliases(file)
	mutationFound := false
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if function, ok := call.Fun.(*ast.SelectorExpr); ok {
			mutationFound = mutationFound || isConfigFileMutation(function.Sel.Name, call.Args, aliases)
		}
		return true
	})
	if !mutationFound {
		t.Fatal("direct delete through cfgPath alias was not detected")
	}
}
