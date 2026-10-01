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

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/visual"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
)

func TestRetiredArchitectureCannotReturn(t *testing.T) {
	root := repositoryRoot(t)
	for _, name := range []string{"SetCwd"} {
		if _, exists := reflect.TypeOf((*codex.Runtime)(nil)).Elem().MethodByName(name); exists {
			t.Fatalf("retired Codex runtime method returned: %s", name)
		}
	}
	handlerType := reflect.TypeOf((*wechat.Handler)(nil))
	for index := 0; index < handlerType.NumMethod(); index++ {
		if strings.HasPrefix(handlerType.Method(index).Name, "Set") {
			t.Fatalf("WeChat runtime exposes mutable dependency setter: %s", handlerType.Method(index).Name)
		}
	}
	for _, retired := range []string{
		filepath.Join(root, "internal", "adapters", "wechat", "control.go"),
		filepath.Join(root, "internal", "adapters", "wechat", "control_visual.go"),
		filepath.Join(root, "internal", "adapters", "wechat", "coordinator.go"),
		filepath.Join(root, "internal", "adapters", "wechat", "task_executor.go"),
		filepath.Join(root, "internal", "adapters", "wechat", "request_prompt.go"),
		filepath.Join(root, "internal", "adapters", "wechat", "visual_renderer.go"),
	} {
		if _, err := os.Stat(retired); !os.IsNotExist(err) {
			t.Fatalf("retired WeChat control surface still exists: %s", retired)
		}
	}
	if _, exists := reflect.TypeOf((*visual.Renderer)(nil)).MethodByName("Render"); exists {
		t.Fatal("retired generic card renderer returned")
	}
	if paths, err := filepath.Glob(filepath.Join(root, "internal", "adapters", "wechat", "visual", "assets", "card*.html")); err != nil || len(paths) != 0 {
		t.Fatalf("retired generic card templates returned: %v (%v)", paths, err)
	}
}

func TestInternalDependencyBoundaries(t *testing.T) {
	root := filepath.Join(repositoryRoot(t), "internal")
	groups := map[string]bool{"app": true, "cli": true, "core": true, "adapters": true, "platform": true}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && !groups[entry.Name()] {
			t.Errorf("package outside the five internal groups: %s", entry.Name())
		}
	}
	// 同时检查测试代码，避免用集成夹具掩盖核心层对协议适配器的依赖。
	err = filepath.WalkDir(root, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(file) != ".go" {
			return walkErr
		}
		relative, err := filepath.Rel(root, filepath.Dir(file))
		if err != nil {
			return err
		}
		source := filepath.ToSlash(relative)
		var allowed []string
		switch {
		case inPackage(source, "platform"):
			allowed = []string{"platform"}
		case inPackage(source, "core"):
			allowed = []string{"core", "platform"}
		case inPackage(source, "adapters"):
			parts := strings.Split(source, "/")
			if len(parts) < 2 {
				t.Errorf("adapter implementation must belong to a concrete protocol: %s", file)
				return nil
			}
			allowed = []string{"core", "platform", "adapters/" + parts[1]}
		case inPackage(source, "app/config"):
			allowed = []string{"app/config", "platform"}
		case inPackage(source, "app"):
			allowed = []string{"app", "adapters", "core", "platform"}
		case source == "cli":
			allowed = []string{"cli", "app", "adapters", "core", "platform"}
		default:
			t.Errorf("package has no dependency rule: %s", source)
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range parsed.Imports {
			value, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			dependency, local := strings.CutPrefix(value, "github.com/huixiangyang/codex-link-clawbot/internal/")
			if !local {
				continue
			}
			valid := false
			for _, prefix := range allowed {
				valid = valid || inPackage(dependency, prefix)
			}
			if !valid {
				t.Errorf("%s imports outside its dependency boundary: %s", file, dependency)
			}
			if inPackage(dependency, "app/migration") && source != "cli" && !inPackage(source, "app/migration") {
				t.Errorf("offline migration imported by runtime code: %s", file)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCLIStartsAtCompositionRoot(t *testing.T) {
	root := repositoryRoot(t)
	startFile := filepath.Join(root, "internal", "cli", "start.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), startFile, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	hasApp := false
	for _, spec := range parsed.Imports {
		value, _ := strconv.Unquote(spec.Path.Value)
		hasApp = hasApp || strings.HasSuffix(value, "/internal/app")
		if strings.HasSuffix(value, "/internal/adapters/wechat") || strings.HasSuffix(value, "/internal/adapters/appserver") {
			t.Fatalf("CLI start bypasses composition root")
		}
	}
	if !hasApp {
		t.Fatal("CLI start does not enter the application composition root")
	}
}

func inPackage(value, prefix string) bool {
	return value == prefix || strings.HasPrefix(value, prefix+"/")
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
