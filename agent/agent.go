package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentInfo holds metadata about an agent for logging/debugging.
type AgentInfo struct {
	Name    string // e.g. "claude-acp", "claude", "gpt-4o"
	Type    string // e.g. "acp", "cli", "http"
	Model   string // e.g. "sonnet", "gpt-4o-mini"
	Command string // binary path, e.g. "/usr/local/bin/claude-agent-acp"
	PID     int    // subprocess PID (0 if not applicable, e.g. http agent)
}

// ProgressKind 描述 Agent 在一次长任务中的安全进度类型。
// 桥接层只消费结构化状态，不转发命令原始输出。
type ProgressKind string

const (
	ProgressCommentary ProgressKind = "commentary"
	ProgressPlan       ProgressKind = "plan"
	ProgressActivity   ProgressKind = "activity"
)

// ProgressEvent 是可安全发送给聊天端的任务进度。
type ProgressEvent struct {
	Kind      ProgressKind
	Text      string
	Completed int
	Total     int
}

// ProgressHandler 接收一次任务中的阶段更新。
type ProgressHandler func(ProgressEvent)

// ChatRequest 是一次 Agent turn 的结构化用户输入。
// LocalImages 只接受本机绝对路径，文件生命周期由调用方管理。
type ChatRequest struct {
	Text        string
	LocalImages []string
}

// ProgressAgent 为支持阶段更新的 Agent 提供显式接口。
// 未实现该接口的 Agent 仍可通过 Agent.Chat 正常返回最终答案。
type ProgressAgent interface {
	ChatWithProgress(ctx context.Context, conversationID string, request ChatRequest, onProgress ProgressHandler) (string, error)
}

// String returns a human-readable summary for logging.
func (i AgentInfo) String() string {
	s := fmt.Sprintf("name=%s, type=%s, model=%s, command=%s", i.Name, i.Type, i.Model, i.Command)
	if i.PID > 0 {
		s += fmt.Sprintf(", pid=%d", i.PID)
	}
	return s
}

// defaultWorkspace returns ~/.weclaw/workspace as the default working directory.
func defaultWorkspace() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	dir := filepath.Join(home, ".weclaw", "workspace")
	os.MkdirAll(dir, 0o755)
	return dir
}

// mergeEnv merges extra environment variables into the base environment.
func mergeEnv(base []string, extra map[string]string) ([]string, error) {
	if len(extra) == 0 {
		return base, nil
	}

	merged := append([]string(nil), base...)
	indexByKey := make(map[string]int, len(base))
	for i, entry := range merged {
		key, _, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		indexByKey[key] = i
	}

	newKeys := make([]string, 0, len(extra))
	for key, value := range extra {
		if key == "" || strings.Contains(key, "=") {
			return nil, fmt.Errorf("invalid env key %q", key)
		}
		entry := key + "=" + value
		if idx, ok := indexByKey[key]; ok {
			merged[idx] = entry
			continue
		}
		newKeys = append(newKeys, key)
	}

	sort.Strings(newKeys)
	for _, key := range newKeys {
		merged = append(merged, key+"="+extra[key])
	}

	return merged, nil
}

// Agent is the interface for AI chat agents.
type Agent interface {
	// Chat sends structured user input to the agent and returns the response.
	// conversationID is used to maintain conversation history per user.
	Chat(ctx context.Context, conversationID string, request ChatRequest) (string, error)

	// ResetSession clears the existing session for the given conversationID and
	// starts a new one. Returns the new session ID if immediately available
	// (ACP mode), or an empty string if the ID will be assigned on next Chat
	// (CLI mode) or is not applicable (HTTP mode).
	ResetSession(ctx context.Context, conversationID string) (string, error)

	// Info returns metadata about this agent.
	Info() AgentInfo

	// SetCwd changes the working directory for subsequent operations.
	SetCwd(cwd string)
}
