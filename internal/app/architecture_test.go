package app

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/bridge"
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
)

func TestRetiredArchitectureCannotReturn(t *testing.T) {
	root := repositoryRoot(t)
	for _, retired := range []string{"messaging", "taskqueue", "session", "project", "api"} {
		path := filepath.Join(root, "internal", retired)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("retired package still exists: %s", path)
		}
	}
	for _, name := range []string{"SetCwd"} {
		if _, exists := reflect.TypeOf((*codex.Runtime)(nil)).Elem().MethodByName(name); exists {
			t.Fatalf("retired Codex runtime method returned: %s", name)
		}
	}
	handlerType := reflect.TypeOf((*bridge.Handler)(nil))
	for index := 0; index < handlerType.NumMethod(); index++ {
		if strings.HasPrefix(handlerType.Method(index).Name, "Set") {
			t.Fatalf("bridge runtime exposes mutable dependency setter: %s", handlerType.Method(index).Name)
		}
	}
	for _, retired := range []string{
		filepath.Join(root, "internal", "bridge", "control.go"),
		filepath.Join(root, "internal", "bridge", "control_visual.go"),
		filepath.Join(root, "internal", "control"),
	} {
		if _, err := os.Stat(retired); !os.IsNotExist(err) {
			t.Fatalf("retired WeChat control surface still exists: %s", retired)
		}
	}
}

func TestDomainPackagesDoNotDependOnCompositionOrBridge(t *testing.T) {
	root := repositoryRoot(t)
	for _, directory := range []string{"access", "codex", "delivery", "execution", "preference", "presentation", "request", "thread", "workspace"} {
		path := filepath.Join(root, "internal", directory)
		err := filepath.WalkDir(path, func(file string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || filepath.Ext(file) != ".go" {
				return walkErr
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range parsed.Imports {
				value, _ := strconv.Unquote(spec.Path.Value)
				if strings.Contains(value, "/internal/app") || strings.Contains(value, "/internal/bridge") || strings.Contains(value, "/internal/cli") {
					t.Errorf("domain package %s imports upper layer %s", file, value)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	startFile := filepath.Join(root, "internal", "cli", "start.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), startFile, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	hasApp := false
	for _, spec := range parsed.Imports {
		value, _ := strconv.Unquote(spec.Path.Value)
		hasApp = hasApp || strings.HasSuffix(value, "/internal/app")
		if strings.HasSuffix(value, "/internal/bridge") {
			t.Fatalf("CLI start bypasses composition root")
		}
	}
	if !hasApp {
		t.Fatal("CLI start does not enter the application composition root")
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
