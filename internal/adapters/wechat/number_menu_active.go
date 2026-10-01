package wechat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/visual"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
)

type activeMenuSession struct {
	thread.GlobalThread
	TargetID string
	Stage    string
}

// 合并本地未完成请求与受信任目录中的活跃会话，预览本身不创建或切换目标。
func (m *numberMenus) activeSessions(ctx context.Context, owner string) ([]activeMenuSession, error) {
	active := []activeMenuSession{}
	seen := map[string]int{}
	if m.h.tasks != nil {
		for _, task := range m.h.tasks.List(owner) {
			if task.State.Terminal() {
				continue
			}
			if _, exists := seen[task.ThreadID]; task.ThreadID != "" && exists {
				continue
			}
			if task.ThreadID != "" {
				seen[task.ThreadID] = len(active)
			}
			active = append(active, activeMenuSession{
				GlobalThread: thread.GlobalThread{
					Info:        codex.ThreadInfo{ID: task.ThreadID, Name: task.Summary, UpdatedAt: max(task.CreatedAt, task.StartedAt, task.ExecutionCompletedAt)},
					WorkspaceID: task.ProjectID, WorkspaceName: m.taskWorkspaceName(task),
				},
				TargetID: task.TargetID, Stage: task.Stage,
			})
		}
	}
	var catalog thread.GlobalPage
	var err error
	if m.h.sessions == nil || m.codex == nil || m.h.projects == nil {
		err = fmt.Errorf("会话服务暂不可用")
	} else {
		catalog, err = m.h.sessions.GlobalList(ctx, owner, m.codex, m.h.projects.List(), false, false, "", 1, 10000)
	}
	for _, item := range catalog.Items {
		if i, exists := seen[item.Info.ID]; exists {
			local := &active[i]
			if strings.TrimSpace(item.Info.Name) != "" {
				local.Info.Name = item.Info.Name
			}
			local.Info.UpdatedAt = max(local.ActivityAt(), item.ActivityAt())
			continue
		}
		if item.Info.Status.Type == "active" {
			active = append(active, activeMenuSession{GlobalThread: item, Stage: "会话正在执行"})
		}
	}
	for i := range active {
		item := &active[i]
		item.Info.Name = presentation.NormalizeLine(item.Info.Name, 80)
		if item.Info.Name == "" {
			item.Info.Name = presentation.NormalizeLine(item.Info.Preview, 80)
		}
		if item.Info.Name == "" {
			item.Info.Name = "未命名会话"
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].ActivityAt() != active[j].ActivityAt() {
			return active[i].ActivityAt() > active[j].ActivityAt()
		}
		return active[i].Info.ID+active[i].TargetID < active[j].Info.ID+active[j].TargetID
	})
	return active, err
}

func (m *numberMenus) activePreview(ctx context.Context, owner string, p *numberedPage, current target.Intent) {
	active, err := m.activeSessions(ctx, owner)
	p.ActiveTitle = fmt.Sprintf("活跃会话 · %d", len(active))
	if len(active) == 0 {
		p.ActiveNotice = "暂无活跃会话"
	} else if len(active) > visual.MenuPreviewLimit {
		p.ActiveNotice = fmt.Sprintf("另有 %d 个 · #4 会话状态", len(active)-visual.MenuPreviewLimit)
	}
	if err != nil {
		p.ActiveTitle = "活跃会话 · 状态不完整"
		p.ActiveNotice = "其他会话暂不可查询，稍后回复 # 刷新"
	}
	for _, item := range active[:min(len(active), visual.MenuPreviewLimit)] {
		detail := item.Stage + " · " + item.WorkspaceName
		if item.Info.ID != "" {
			detail += " · " + thread.ShortCode(item.Info.ID)
		}
		p.Active = append(p.Active, visual.MenuPreview{
			Title: item.Info.Name, Detail: detail, Time: item.ActivityLabel(m.now()),
			Current: item.TargetID != "" && item.TargetID == current.ID || item.Info.ID != "" && item.Info.ID == current.ThreadID && item.WorkspaceID == current.WorkspaceID,
		})
	}
}
