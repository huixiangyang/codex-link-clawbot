package messaging

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/session"
)

type handlerThreadAgent struct {
	next           int
	threads        map[string]agent.ThreadInfo
	archived       map[string]bool
	implicitCalled bool
	chatThreadID   string
}

func newHandlerThreadAgent() *handlerThreadAgent {
	return &handlerThreadAgent{
		threads:  make(map[string]agent.ThreadInfo),
		archived: make(map[string]bool),
	}
}

func (a *handlerThreadAgent) Chat(context.Context, string, agent.ChatRequest) (string, error) {
	a.implicitCalled = true
	return "", fmt.Errorf("implicit chat should not be used")
}

func (a *handlerThreadAgent) Info() agent.AgentInfo {
	return agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}
}

func (a *handlerThreadAgent) SetCwd(string) {}

func (a *handlerThreadAgent) StartThread(context.Context) (agent.ThreadInfo, error) {
	a.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", a.next)
	thread := agent.ThreadInfo{
		ID: id, Preview: fmt.Sprintf("测试会话 %d", a.next), Cwd: "/workspace",
		CreatedAt: int64(100 + a.next), UpdatedAt: int64(100 + a.next),
		Status: agent.ThreadStatus{Type: "idle"},
	}
	a.threads[id] = thread
	return thread, nil
}

func (a *handlerThreadAgent) ResumeThread(_ context.Context, threadID string) (agent.ThreadInfo, error) {
	thread, ok := a.threads[threadID]
	if !ok || a.archived[threadID] {
		return agent.ThreadInfo{}, fmt.Errorf("thread unavailable")
	}
	return thread, nil
}

func (a *handlerThreadAgent) ReadThread(_ context.Context, threadID string) (agent.ThreadInfo, error) {
	thread, ok := a.threads[threadID]
	if !ok {
		return agent.ThreadInfo{}, fmt.Errorf("thread not found")
	}
	return thread, nil
}

func (a *handlerThreadAgent) ListThreads(_ context.Context, options agent.ThreadListOptions) (agent.ThreadPage, error) {
	var threads []agent.ThreadInfo
	for id, thread := range a.threads {
		if a.archived[id] == options.Archived {
			threads = append(threads, thread)
		}
	}
	return agent.ThreadPage{Threads: threads}, nil
}

func (a *handlerThreadAgent) SetThreadName(_ context.Context, threadID, name string) error {
	thread, ok := a.threads[threadID]
	if !ok {
		return fmt.Errorf("thread not found")
	}
	thread.Name = name
	a.threads[threadID] = thread
	return nil
}

func (a *handlerThreadAgent) ArchiveThread(_ context.Context, threadID string) error {
	if _, ok := a.threads[threadID]; !ok {
		return fmt.Errorf("thread not found")
	}
	a.archived[threadID] = true
	return nil
}

func (a *handlerThreadAgent) UnarchiveThread(_ context.Context, threadID string) (agent.ThreadInfo, error) {
	thread, ok := a.threads[threadID]
	if !ok || !a.archived[threadID] {
		return agent.ThreadInfo{}, fmt.Errorf("archived thread not found")
	}
	delete(a.archived, threadID)
	return thread, nil
}

func (a *handlerThreadAgent) UnsubscribeThread(context.Context, string) error { return nil }

func (a *handlerThreadAgent) ChatThread(_ context.Context, threadID string, _ agent.ChatRequest) (string, error) {
	a.chatThreadID = threadID
	return "显式线程回复", nil
}

func newSessionHandler(t *testing.T) (*Handler, *handlerThreadAgent) {
	t.Helper()
	manager, err := session.NewManager(t.TempDir() + "/session-index.json")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(nil, nil)
	handler.SetSessionManager(manager)
	threadAgent := newHandlerThreadAgent()
	handler.SetDefaultAgent("codex", threadAgent)
	return handler, threadAgent
}

func TestSessionCommandsCreateListSwitchRenameArchiveRestore(t *testing.T) {
	handler, _ := newSessionHandler(t)
	ctx := context.Background()
	if got := handler.handleSessionReadCommand(ctx, "owner-1", "/session"); !strings.Contains(got, "当前没有会话") {
		t.Fatalf("initial /session = %q", got)
	}
	firstReply := handler.handleSessionMutationCommand(ctx, "owner-1", "/session new 登录排障")
	if !strings.Contains(firstReply, "已创建并切换") || !strings.Contains(firstReply, "00000001") {
		t.Fatalf("first new reply = %q", firstReply)
	}
	secondReply := handler.handleSessionMutationCommand(ctx, "owner-1", "/session new 发布检查")
	if !strings.Contains(secondReply, "00000002") {
		t.Fatalf("second new reply = %q", secondReply)
	}
	list := handler.handleSessionReadCommand(ctx, "owner-1", "/sessions")
	for _, want := range []string{"会话 1/1，共 2 个", "当前", "发布检查", "登录排障"} {
		if !strings.Contains(list, want) {
			t.Fatalf("/sessions missing %q: %q", want, list)
		}
	}
	switched := handler.handleSessionMutationCommand(ctx, "owner-1", "/session use 00000001")
	if !strings.Contains(switched, "已切换会话") || !strings.Contains(switched, "登录排障") {
		t.Fatalf("use reply = %q", switched)
	}
	renamed := handler.handleSessionMutationCommand(ctx, "owner-1", "/session rename 微信登录修复")
	if !strings.Contains(renamed, "会话已重命名") || !strings.Contains(renamed, "微信登录修复") {
		t.Fatalf("rename reply = %q", renamed)
	}
	detail := handler.handleSessionReadCommand(ctx, "owner-1", "/session")
	if !strings.Contains(detail, "完整编号") || !strings.Contains(detail, "微信登录修复") {
		t.Fatalf("detail reply = %q", detail)
	}
	archived := handler.handleSessionMutationCommand(ctx, "owner-1", "/session archive")
	if !strings.Contains(archived, "会话已归档") || !strings.Contains(archived, "00000002") {
		t.Fatalf("archive reply = %q", archived)
	}
	archivedList := handler.handleSessionReadCommand(ctx, "owner-1", "/sessions archived")
	if !strings.Contains(archivedList, "已归档会话") || !strings.Contains(archivedList, "微信登录修复") {
		t.Fatalf("archived list = %q", archivedList)
	}
	restored := handler.handleSessionMutationCommand(ctx, "owner-1", "/session restore 00000001")
	if !strings.Contains(restored, "会话已恢复") {
		t.Fatalf("restore reply = %q", restored)
	}
}

func TestChatWithAgentUsesOwnedExplicitThread(t *testing.T) {
	handler, threadAgent := newSessionHandler(t)
	reply, err := handler.chatWithAgent(
		context.Background(), "codex", threadAgent, "owner-1",
		agent.ChatRequest{Text: "检查项目"}, nil,
	)
	if err != nil || reply != "显式线程回复" {
		t.Fatalf("chatWithAgent() = %q, %v", reply, err)
	}
	if threadAgent.implicitCalled {
		t.Fatal("generic Chat() must not be used for Codex thread agents")
	}
	if threadAgent.chatThreadID == "" {
		t.Fatal("ChatThread() did not receive an explicit thread id")
	}
}

func TestSessionCommandsDoNotExposeForeignThreads(t *testing.T) {
	handler, _ := newSessionHandler(t)
	ctx := context.Background()
	_ = handler.handleSessionMutationCommand(ctx, "owner-1", "/session new 私有会话")
	got := handler.handleSessionMutationCommand(ctx, "owner-2", "/session use 00000001")
	if !strings.Contains(got, "没有找到属于当前微信用户的会话") {
		t.Fatalf("foreign use reply = %q", got)
	}
}
