package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
)

// StartThread 创建一个持久化 Client 线程，归属关系由上层会话管理器保存。
func (a *Client) StartThread(ctx context.Context, workspaceRoot string) (codex.ThreadInfo, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return codex.ThreadInfo{}, err
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return codex.ThreadInfo{}, fmt.Errorf("workspace root is required")
	}
	model := a.defaultModel()
	params := map[string]interface{}{
		"approvalPolicy": "never",
		"cwd":            workspaceRoot,
		"sandbox":        "danger-full-access",
		"serviceName":    "codex-link-clawbot",
	}
	if model != "" {
		params["model"] = model
	}
	result, err := a.rpc(ctx, "thread/start", params)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, instructions, err := decodeOpenedThread(result, "thread/start")
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	a.mu.Lock()
	if a.instructions == nil {
		a.instructions = make(map[string][]string)
	}
	a.loadedThreads[thread.ID] = true
	a.threadStatus[thread.ID] = thread.Status
	a.instructions[thread.ID] = append([]string(nil), instructions...)
	a.mu.Unlock()
	thread.InstructionSources = instructions
	return thread, nil
}

// ResumeThread 从磁盘加载线程并订阅其事件。
func (a *Client) ResumeThread(ctx context.Context, threadID, workspaceRoot string) (codex.ThreadInfo, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return codex.ThreadInfo{}, err
	}
	if strings.TrimSpace(threadID) == "" {
		return codex.ThreadInfo{}, fmt.Errorf("thread id is required")
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return codex.ThreadInfo{}, fmt.Errorf("workspace root is required")
	}
	model := a.defaultModel()
	params := map[string]interface{}{
		"threadId":       threadID,
		"approvalPolicy": "never",
		"cwd":            workspaceRoot,
		"sandbox":        "danger-full-access",
		"serviceName":    "codex-link-clawbot",
	}
	if model != "" {
		params["model"] = model
	}
	result, err := a.rpc(ctx, "thread/resume", params)
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, instructions, err := decodeOpenedThread(result, "thread/resume")
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	a.mu.Lock()
	if a.instructions == nil {
		a.instructions = make(map[string][]string)
	}
	a.loadedThreads[thread.ID] = true
	a.threadStatus[thread.ID] = thread.Status
	a.instructions[thread.ID] = append([]string(nil), instructions...)
	a.mu.Unlock()
	thread.InstructionSources = instructions
	return thread, nil
}

// ReadThread 只读取线程摘要，不加载完整轮次历史。
func (a *Client) ReadThread(ctx context.Context, threadID string) (codex.ThreadInfo, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return codex.ThreadInfo{}, err
	}
	result, err := a.rpc(ctx, "thread/read", map[string]interface{}{
		"threadId":     threadID,
		"includeTurns": false,
	})
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	thread, err := decodeCodexThread(result, "thread/read")
	if err != nil {
		return codex.ThreadInfo{}, err
	}
	a.mu.Lock()
	// 线程读取已返回服务端当前状态，覆盖可能过期的通知缓存。
	a.threadStatus[threadID] = thread.Status
	if a.instructions != nil {
		thread.InstructionSources = append([]string(nil), a.instructions[threadID]...)
	}
	a.mu.Unlock()
	return thread, nil
}

// ListThreads 查询 Client 全局线程页；上层必须按受信任工作空间过滤工作目录。
func (a *Client) ListThreads(ctx context.Context, options codex.ThreadListOptions) (codex.ThreadPage, error) {
	if err := a.ensureCodexReady(ctx); err != nil {
		return codex.ThreadPage{}, err
	}
	params := map[string]interface{}{
		"archived":      options.Archived,
		"sortKey":       "recency_at",
		"sortDirection": "desc",
	}
	if options.Cursor != "" {
		params["cursor"] = options.Cursor
	}
	if options.Limit > 0 {
		params["limit"] = options.Limit
	}
	if len(options.SourceKinds) > 0 {
		params["sourceKinds"] = options.SourceKinds
	}
	if options.Cwd != "" {
		params["cwd"] = options.Cwd
	}
	if options.SearchTerm != "" {
		params["searchTerm"] = options.SearchTerm
	}
	if options.Pinned != nil {
		params["isPinned"] = *options.Pinned
	}
	result, err := a.rpc(ctx, "thread/list", params)
	if err != nil {
		return codex.ThreadPage{}, err
	}
	var response struct {
		Data       []codex.ThreadInfo `json:"data"`
		NextCursor *string            `json:"nextCursor"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return codex.ThreadPage{}, fmt.Errorf("parse thread/list result: %w", err)
	}
	page := codex.ThreadPage{Threads: response.Data}
	if response.NextCursor != nil {
		page.NextCursor = *response.NextCursor
	}
	return page, nil
}

func (a *Client) SetThreadName(ctx context.Context, threadID, name string) error {
	if err := a.ensureCodexReady(ctx); err != nil {
		return err
	}
	_, err := a.rpc(ctx, "thread/name/set", map[string]string{"threadId": threadID, "name": name})
	return err
}

func (a *Client) ArchiveThread(ctx context.Context, threadID string) error {
	if err := a.ensureCodexReady(ctx); err != nil {
		return err
	}
	_, err := a.rpc(ctx, "thread/archive", map[string]string{"threadId": threadID})
	if err == nil {
		a.mu.Lock()
		delete(a.loadedThreads, threadID)
		delete(a.threadStatus, threadID)
		a.mu.Unlock()
	}
	return err
}

func decodeCodexThread(result json.RawMessage, method string) (codex.ThreadInfo, error) {
	var response struct {
		Thread codex.ThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return codex.ThreadInfo{}, fmt.Errorf("parse %s result: %w", method, err)
	}
	if response.Thread.ID == "" {
		return codex.ThreadInfo{}, fmt.Errorf("%s returned empty thread id", method)
	}
	return response.Thread, nil
}

func decodeOpenedThread(result json.RawMessage, method string) (codex.ThreadInfo, []string, error) {
	var response struct {
		Thread             codex.ThreadInfo `json:"thread"`
		InstructionSources []string         `json:"instructionSources"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return codex.ThreadInfo{}, nil, fmt.Errorf("parse %s result: %w", method, err)
	}
	if response.Thread.ID == "" {
		return codex.ThreadInfo{}, nil, fmt.Errorf("%s returned empty thread id", method)
	}
	return response.Thread, response.InstructionSources, nil
}

func (a *Client) ensureThreadLoaded(ctx context.Context, threadID, workspaceRoot string) error {
	a.mu.Lock()
	loaded := a.loadedThreads[threadID]
	a.mu.Unlock()
	if loaded {
		return nil
	}
	_, err := a.ResumeThread(ctx, threadID, workspaceRoot)
	return err
}
