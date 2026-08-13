package config

import "testing"

func FuzzDecodeConfig(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"projects":[{"id":"project","name":"Project","root":"/srv/project"}],"codex":{"command":"codex"}}`))
	f.Add([]byte(`{"schema_version":5,"codex":{"command":"codex"},"codex-link-clawbot":{"project_entries":[{"id":"project","name":"Project","root":"/srv/project"}]}}`))
	f.Add([]byte(`{"unknown":true}`))
	f.Add([]byte(`{} trailing`))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// 配置文件有明确体积边界，避免模糊测试把资源消耗本身当作解析缺陷。
		if len(data) > 1<<20 {
			t.Skip()
		}
		cfg, err := decodeConfig(data)
		if err != nil {
			return
		}
		if cfg == nil {
			t.Fatal("successful decode returned nil config")
		}
		_ = cfg.validate()
	})
}
