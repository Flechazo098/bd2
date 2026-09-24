package gacha

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Captures are evidence and golden-test inputs. This audit prevents a server
// source file from acquiring a runtime dependency on the fixture reader or an
// on-disk capture path as gacha evolves.
func TestServerRuntimeHasNoCaptureFixtureDependency(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, directory := range []string{filepath.Join(root, "cmd"), filepath.Join(root, "internal")} {
		err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if filepath.Base(path) == "fixture" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			for _, imported := range file.Imports {
				value, _ := strconv.Unquote(imported.Path.Value)
				if strings.HasSuffix(value, "/internal/fixture") {
					t.Errorf("runtime file %s imports capture fixture package", path)
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, _ := strconv.Unquote(literal.Value)
				normalized := strings.ToLower(strings.ReplaceAll(value, "\\", "/"))
				if strings.Contains(normalized, "data/capture/") {
					t.Errorf("runtime file %s contains capture path literal", path)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
