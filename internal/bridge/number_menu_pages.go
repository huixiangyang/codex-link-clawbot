package bridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
)

func (m *numberMenus) contextLabel(owner string) (string, target.Intent) {
	intent := target.Intent{}
	if m.h.targets != nil {
		intent = m.h.targets.Current(owner)
	}
	label := ""
	if m.h.projects != nil {
		if intent.WorkspaceID != "" {
			current, _ := m.h.projects.Get(intent.WorkspaceID)
			label = current.Name
		} else {
			current := m.h.projects.List()[0]
			label = current.Name
			intent.WorkspaceID = current.ID
		}
	}
	if label == "" {
		label = "尚未选择工作空间"
	}
	if intent.ThreadID != "" {
		return label + " · " + thread.ShortCode(intent.ThreadID), intent
	}
	return label + " · 新对话", intent
}

func (m *numberMenus) show(ctx context.Context, owner string, s *menuSession, location menuAction) error {
	if location.page < 1 {
		location.page = 1
	}
	if (location.kind == "home" || location.kind == "session") && !m.locked(owner) && m.h.targets != nil {
		if _, err := m.h.targets.Capture(owner); err != nil {
			return err
		}
	}
	contextLabel, current := m.contextLabel(owner)
	p := newNumberedPage("主菜单", contextLabel)
	var readingText string
	if m.locked(owner) && location.kind != "unlock" {
		location.kind = "locked"
	}
	switch location.kind {
	case "home":
		p.add(1, "新建对话", "", menuAction{kind: "new", workspaceID: current.WorkspaceID}, false)
		for i, item := range []struct{ label, kind string }{{"切换会话", "threads"}, {"工作空间", "workspaces"}, {"会话状态", "running"}, {"最近结果", "results"}, {"回复设置", "settings"}} {
			action := menuAction{kind: item.kind}
			if item.kind == "session" {
				action.targetID, action.threadID = current.ID, current.ThreadID
			}
			p.add(i+2, item.label, "", action, false)
		}
	case "locked":
		p.Title, p.Notice = "入口已锁定", "回复 1，然后输入解锁码，即可继续工作。"
		p.add(1, "解锁入口", "", menuAction{kind: "unlock"}, false)
	case "unlock":
		p.Title, p.Notice = "输入解锁码", "请发送解锁码。此处输入不会提交给 Codex。0 返回，9 退出。"
	case "threads":
		p.Title = "切换会话"
		if m.codex == nil || m.h.sessions == nil || m.h.targets == nil {
			return fmt.Errorf("会话服务暂不可用，请稍后重试")
		}
		workspaces := []thread.Workspace{}
		for _, w := range m.h.projects.List() {
			workspaces = append(workspaces, thread.Workspace{ID: w.ID, Name: w.Name, Root: w.Root})
		}
		page, err := m.h.sessions.GlobalList(ctx, owner, m.codex, workspaces, false, false, "", location.page, menuPageSize)
		if err != nil {
			return fmt.Errorf("暂时无法读取会话，请稍后重新选择")
		}
		for i, item := range page.Items {
			title := strings.TrimSpace(item.Info.Name)
			if title == "" {
				title = strings.TrimSpace(item.Info.Preview)
			}
			if title == "" {
				title = "未命名会话"
			}
			p.add(i+1, title, menuThreadStatus(item.Info.Status.Type)+" · "+thread.ShortCode(item.Info.ID)+" · "+item.WorkspaceName, menuAction{kind: "select-thread", workspaceID: item.WorkspaceID, threadID: item.Info.ID, value: title}, current.ThreadID == item.Info.ID && current.WorkspaceID == item.WorkspaceID)
		}
		location.page = page.Number
		menuPagination(&p, location, page.TotalPages)
		if len(page.Items) == 0 {
			p.Notice = "还没有会话。回复 0 回首页，选择 1 新建对话。"
		}
	case "running":
		p.Title, p.Context = "运行中会话", "选择会话管理，不改变输入目标"
		active := []request.Task{}
		for _, task := range m.h.tasks.List(owner) {
			if !task.State.Terminal() {
				active = append(active, task)
			}
		}
		seen := map[string]bool{}
		for _, task := range active {
			seen[task.ThreadID] = true
		}
		workspaces := []thread.Workspace{}
		for _, w := range m.h.projects.List() {
			workspaces = append(workspaces, thread.Workspace{ID: w.ID, Name: w.Name, Root: w.Root})
		}
		catalog, catalogErr := m.h.sessions.GlobalList(ctx, owner, m.codex, workspaces, false, true, "", 1, 10000)
		if catalogErr != nil {
			p.Notice = "本地状态已显示，其他会话暂不可查询。回复 0 再选 4 刷新。"
		}
		for _, item := range catalog.Items {
			if seen[item.Info.ID] {
				continue
			}
			intent, err := m.h.targets.Remember(owner, item.WorkspaceID, item.Info.ID, false)
			if err != nil {
				return err
			}
			title := strings.TrimSpace(item.Info.Name)
			if title == "" {
				title = "会话 " + thread.ShortCode(item.Info.ID)
			}
			active = append(active, request.Task{TargetID: intent.ID, ThreadID: item.Info.ID, ProjectID: item.WorkspaceID, Summary: title, Stage: "会话正在执行"})
		}
		if len(active) == 0 {
			return m.show(ctx, owner, s, menuAction{kind: "session", targetID: current.ID, threadID: current.ThreadID})
		}
		location.page = min(location.page, max(1, (len(active)+menuPageSize-1)/menuPageSize))
		start := (location.page - 1) * menuPageSize
		for i, task := range active[start:min(len(active), start+menuPageSize)] {
			p.add(i+1, task.Summary, task.Stage+" · "+m.taskWorkspaceName(task), menuAction{kind: "session", targetID: task.TargetID, threadID: task.ThreadID}, task.TargetID == current.ID)
		}
		menuPagination(&p, location, max(1, (len(active)+menuPageSize-1)/menuPageSize))
	case "workspaces":
		p.Title = "工作空间"
		items := m.h.projects.List()
		location.page = min(location.page, max(1, (len(items)+menuPageSize-1)/menuPageSize))
		start := (location.page - 1) * menuPageSize
		for i, item := range items[start:min(len(items), start+menuPageSize)] {
			p.add(i+1, item.Name, item.ID, menuAction{kind: "select-workspace", workspaceID: item.ID}, current.WorkspaceID == item.ID)
		}
		menuPagination(&p, location, max(1, (len(items)+menuPageSize-1)/menuPageSize))
	case "session":
		if location.targetID == "" {
			var err error
			current, err = m.h.targets.Capture(owner)
			if err != nil {
				return err
			}
			location.targetID, location.threadID = current.ID, current.ThreadID
		}
		activity, err := m.h.sessionActivity(ctx, owner, location.targetID, location.threadID)
		if err != nil {
			return fmt.Errorf("无法读取会话状态，请回复 0 重新选择")
		}
		p.Title, p.Notice = "会话状态", activity.Stage
		if activity.ThreadID != "" {
			p.Context = thread.ShortCode(activity.ThreadID)
		}
		p.add(1, "刷新会话状态", "", location, false)
		if activity.CanInterrupt {
			p.add(2, "打断本次执行", "", menuAction{kind: "confirm-cancel", targetID: activity.TargetID, threadID: activity.ThreadID, taskID: activity.TaskID, turnID: activity.TurnID}, false)
		}
		p.add(3, "切换会话", "", menuAction{kind: "threads"}, false)
		p.add(4, "新建对话", "", menuAction{kind: "new", workspaceID: activity.WorkspaceID}, false)
	case "results":
		if m.h.tasks == nil {
			return fmt.Errorf("结果服务暂不可用")
		}
		p.Title, p.Context = "最近结果", "所有工作空间 · 按编号选择结果"
		items := []request.Task{}
		for _, task := range m.h.tasks.List(owner) {
			if task.State.Terminal() {
				items = append(items, task)
			}
		}
		location.page = min(location.page, max(1, (len(items)+menuPageSize-1)/menuPageSize))
		start := (location.page - 1) * menuPageSize
		for i, task := range items[start:min(len(items), start+menuPageSize)] {
			p.add(i+1, task.Summary, thread.ShortCode(task.ID)+" · "+menuTaskState(task)+" · "+m.taskWorkspaceName(task), menuAction{kind: "task", taskID: task.ID}, false)
		}
		menuPagination(&p, location, max(1, (len(items)+menuPageSize-1)/menuPageSize))
		if len(items) == 0 {
			p.Notice = "暂时没有结果。回复 0 管理会话，或回复 9 退出。"
		}
	case "task":
		task, ok := m.h.tasks.Find(owner, location.taskID)
		if !ok {
			return fmt.Errorf("请求已不可用，请回复 0 重新选择")
		}
		p.Title, p.Context = "请求详情", menuTaskState(task)+" · "+thread.ShortCode(task.ID)
		p.Notice = normalizeSessionLine(task.Summary, 42) + " · " + normalizeSessionLine(m.taskWorkspaceName(task), 12)
		if task.Reason != "" {
			p.Notice += "。" + request.ReasonLabel(task.Reason)
		}
		if task.ArchiveFailed && task.ResultExpiresAt > m.now().Unix() {
			p.add(4, "恢复保存结果", "只取回成果，不重新执行", menuAction{kind: "restore-result", taskID: task.ID}, false)
		}
		if task.State.Terminal() {
			p.add(5, "清理这条记录", "释放内容与名额", menuAction{kind: "confirm-release", taskID: task.ID}, false)
		}
		switch task.State {
		case request.StateRunning:
			p.add(1, "打断本次执行", "", menuAction{kind: "confirm-cancel", taskID: task.ID, targetID: task.TargetID, threadID: task.ThreadID}, false)
		case request.StateDelivering:
			p.Notice += "。正在发送结果，请稍后刷新。"
		default:
			if receipt, err := m.h.tasks.SummarizeResult(owner, task.ID); err == nil {
				if receipt.Outcome == request.DeliveryExplicitFailure || receipt.Outcome == request.DeliveryAmbiguous || receipt.Outcome == request.DeliveryPending {
					p.Context = "结果已保存 · 微信待取回 · " + thread.ShortCode(task.ID)
				}
				p.add(1, "阅读结果文字", "", menuAction{kind: "read", taskID: task.ID}, false)
				if task.State == request.StateSucceeded {
					p.add(2, "重发回答与文件", "", menuAction{kind: "confirm-redeliver", taskID: task.ID}, false)
				}
			} else if task.ExecutionCompletedAt > 0 && !task.ArchiveFailed {
				p.Notice += "。结果已过期或不可用。"
			}
			if task.Reason != request.ReasonInterruptUnconfirmed && task.ExecutionCompletedAt == 0 && (task.State == request.StateFailed || task.State == request.StateInterrupted) && task.PayloadExpiresAt > m.now().Unix() {
				p.add(3, "重新执行", "", menuAction{kind: "confirm-retry", taskID: task.ID}, false)
			}
		}
		p.add(6, "刷新详情", "", location, false)
	case "read":
		result, err := m.h.tasks.LoadResult(owner, location.taskID)
		if err != nil {
			return fmt.Errorf("结果已过期或不可用，请选择其他请求")
		}
		// 长回答每次只发送一页，用户以 7/8 继续阅读，避免手机消息刷屏。
		text := []rune(MarkdownToPlainText(result.Reply))
		const pageRunes = 500
		pages := max(1, (len(text)+pageRunes-1)/pageRunes)
		location.page = min(location.page, pages)
		start := (location.page - 1) * pageRunes
		body := string(text[start:min(len(text), start+pageRunes)])
		if len(text) == 0 {
			body = "结果没有文字。回复 6 查看详情，可选择重发文件。"
		}
		p.Title = "结果文字"
		p.Context = thread.ShortCode(location.taskID)
		p.add(6, "请求详情", "", menuAction{kind: "task", taskID: location.taskID}, false)
		menuPagination(&p, location, pages)
		navigation := []string{}
		if p.Previous {
			navigation = append(navigation, "7 上一页")
		}
		if p.Next {
			navigation = append(navigation, "8 下一页")
		}
		navigation = append(navigation, "6 详情 · 0 首页 · 9 退出")
		readingText = fmt.Sprintf("结果文字 %d/%d\n\n%s\n\n%s", location.page, pages, body, strings.Join(navigation, " · "))
	case "settings":
		p.Title = "回复设置"
		if m.h.preferences == nil {
			return fmt.Errorf("设置服务暂不可用")
		}
		preferences := m.h.preferences.Get(owner)
		for i, mode := range presentation.ResponseModes() {
			if mode.ID == presentation.ResponseReading && m.h.visual == nil || mode.ID == presentation.ResponseVoice && m.h.voice == nil {
				continue
			}
			label, detail := mode.Name, mode.Description
			p.add(i+1, label, detail, menuAction{kind: "mode", value: string(mode.ID)}, preferences.ResponseMode == mode.ID)
		}
		if m.h.visual != nil {
			p.add(4, "回答图片风格", "", menuAction{kind: "styles"}, false)
		}
		if m.h.remoteLock != nil && m.h.remoteLock.Enabled() {
			p.add(5, "锁定微信入口", "", menuAction{kind: "confirm-lock"}, false)
		}
		p.add(6, "网页工作台地址", "", menuAction{kind: "console"}, false)
	case "styles":
		p.Title = "回答图片风格"
		current := m.h.preferences.Get(owner).Style
		for i, style := range presentation.Styles() {
			p.add(i+1, style.Name, "", menuAction{kind: "style", value: string(style.ID)}, style.ID == current)
		}
	default:
		if !strings.HasPrefix(location.kind, "confirm-") {
			return fmt.Errorf("菜单不可用，请回复 0 回首页")
		}
		action := location
		action.kind = strings.TrimPrefix(location.kind, "confirm-")
		action.operationID = uuid.NewString()
		labels := map[string]string{"cancel": "打断执行", "retry": "重新执行", "redeliver": "重新发送", "lock": "锁定入口", "release": "清理内容"}
		p.Title = "确认" + labels[action.kind]
		if action.taskID != "" {
			task, ok := m.h.tasks.Find(owner, action.taskID)
			if !ok {
				return fmt.Errorf("请求已不可用")
			}
			p.Context = thread.ShortCode(task.ID) + " · " + normalizeSessionLine(m.taskWorkspaceName(task), 8) + " · " + normalizeSessionLine(task.Summary, 12)
		} else if action.threadID != "" {
			p.Context = thread.ShortCode(action.threadID)
		}
		p.Notice = map[string]string{"release": "会删除本次保存的输入、回答与文件。同时清理记录名额，保留来源回执防止重复执行。此操作不可恢复。", "cancel": "只打断本次执行，已产生的修改会保留。新指令请在结束后重新发送。", "retry": "会新建请求再次执行，可能重复之前已完成的修改。", "redeliver": "只重发已保存的回答和文件，不重新执行；可能出现重复消息。", "lock": "停止接收新工作；当前请求继续。再次使用需要输入解锁码。"}[action.kind]
		p.add(1, "确认"+labels[action.kind], "", action, false)
	}
	s.page, s.location, s.closed, s.text = p, location, false, ""
	s.textOnly = readingText != ""
	if s.textOnly {
		s.text = readingText
	}
	return nil
}

func menuPagination(p *numberedPage, location menuAction, pages int) {
	p.Page = fmt.Sprintf("%d / %d", location.page, pages)
	p.Previous, p.Next = location.page > 1, location.page < pages
	if p.Previous {
		a := location
		a.page--
		p.actions[7] = a
	}
	if p.Next {
		a := location
		a.page++
		p.actions[8] = a
	}
}

func menuTaskState(task request.Task) string {
	if task.Reason == request.ReasonInterruptUnconfirmed {
		return "打断未确认"
	}
	if task.ArchiveFailed {
		return "已完成，待保存"
	}
	if task.State == request.StateSucceeded {
		return "已完成"
	}
	return map[request.State]string{request.StateRunning: "正在执行", request.StateDelivering: "正在发送", request.StateFailed: "执行失败", request.StateInterrupted: "执行中断", request.StateCancelled: "已取消"}[task.State]
}

func (m *numberMenus) taskWorkspaceName(task request.Task) string {
	if m.h.projects != nil {
		if definition, ok := m.h.projects.Get(task.ProjectID); ok {
			return definition.Name
		}
	}
	return task.ProjectID
}

func menuThreadStatus(status string) string {
	if status == "active" {
		return "正在执行"
	}
	return "空闲"
}
