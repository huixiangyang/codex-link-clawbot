package messaging

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huixiangyang/weclaw/internal/taskqueue"
	"github.com/huixiangyang/weclaw/internal/workflow"
)

const workflowPageSize = 6

var readableWorkflowSlotRE = regexp.MustCompile(`「([^「」]+)」`)

type workflowCreateForm struct {
	Name    string
	Content compiledWorkflowContent
}

type compiledWorkflowContent struct {
	Prompt string
	Slots  []workflow.Slot
}

func (h *Handler) openWorkflowCenter(userID, projectID string, page int) string {
	if h.projects == nil || h.workflows == nil {
		return "快捷任务当前不可用。"
	}
	if pending, exists, err := h.workflows.PendingRun(userID); err != nil {
		return workflowUnavailableText()
	} else if exists {
		return workflowParameterPrompt(pending)
	}
	projectInfo, exists := h.projects.Get(strings.TrimSpace(projectID))
	if !exists {
		return "这个项目已经不可用。发送“项目”刷新列表。"
	}
	definitions := h.workflows.List(userID, projectInfo.ID)
	totalPages := (len(definitions) + workflowPageSize - 1) / workflowPageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page <= 0 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * workflowPageSize
	end := start + workflowPageSize
	if end > len(definitions) {
		end = len(definitions)
	}
	options := make([]controlOption, 0, workflowPageSize+3)
	options = append(options, controlOption{
		Label: "新建快捷任务", Action: actionPromptWorkflowCreate, Query: projectInfo.ID, Page: page,
	})
	for _, definition := range definitions[start:end] {
		label := definition.Name
		if len(definition.Slots) > 0 {
			label += fmt.Sprintf(" · %d 个参数", len(definition.Slots))
		}
		options = append(options, controlOption{
			Label: label, Action: actionWorkflowDetail, Value: definition.ID, Query: projectInfo.ID, Page: page,
		})
	}
	if page > 1 {
		options = append(options, controlOption{
			Label: fmt.Sprintf("上一页 · %d/%d", page-1, totalPages), Action: actionProjectQuickTasks,
			Query: projectInfo.ID, Page: page - 1,
		})
	}
	if page < totalPages {
		options = append(options, controlOption{
			Label: fmt.Sprintf("下一页 · %d/%d", page+1, totalPages), Action: actionProjectQuickTasks,
			Query: projectInfo.ID, Page: page + 1,
		})
	}
	lines := []string{
		"快捷任务",
		"",
		"项目：" + projectInfo.Name,
		fmt.Sprintf("数量：%d 项", len(definitions)),
		fmt.Sprintf("页码：%d / %d", page, totalPages),
	}
	if len(definitions) == 0 {
		lines = append(lines, "", "这里还没有快捷任务。可以直接在微信里创建第一项。")
	}
	lines = append(lines, "", renderControlOptions(options))
	back := controlOption{Action: actionProjectCenter}
	if !h.storeChoiceWithBack(userID, viewProjectQuickTasks, options, back) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复数字管理，0 返回项目中心。"
}

func (h *Handler) openWorkflowDetail(userID string, source controlOption) string {
	if h.projects == nil || h.workflows == nil {
		return "快捷任务当前不可用。"
	}
	projectInfo, exists := h.projects.Get(source.Query)
	if !exists {
		return "这个项目已经不可用。发送“项目”刷新列表。"
	}
	definition, exists := h.workflows.Find(userID, projectInfo.ID, source.Value)
	if !exists {
		return "快捷任务已经变化。发送“快捷任务”刷新列表。"
	}
	if source.Page <= 0 {
		source.Page = 1
	}
	parameterText := "无"
	if len(definition.Slots) > 0 {
		labels := make([]string, 0, len(definition.Slots))
		for _, slot := range definition.Slots {
			labels = append(labels, slot.Label)
		}
		parameterText = strings.Join(labels, "、")
	}
	options := []controlOption{
		{Label: "运行这个快捷任务", Action: actionRunQuickTask, Value: definition.ID, Query: projectInfo.ID, Page: source.Page},
		{Label: "重命名", Action: actionPromptWorkflowRename, Value: definition.ID, Query: projectInfo.ID, Page: source.Page},
		{Label: "编辑内容与参数", Action: actionPromptWorkflowEdit, Value: definition.ID, Query: projectInfo.ID, Page: source.Page},
		{Label: "删除", Action: actionConfirmWorkflowDelete, Value: definition.ID, Query: projectInfo.ID, Page: source.Page},
	}
	back := controlOption{Action: actionProjectQuickTasks, Query: projectInfo.ID, Page: source.Page}
	lines := []string{
		"快捷任务详情",
		"",
		"名称：" + definition.Name,
		"项目：" + projectInfo.Name,
		"参数：" + parameterText,
		"更新：" + formatSessionTime(definition.UpdatedAt),
		"",
		"提示内容默认不展示，避免私密指令出现在菜单快照中。",
		"",
		renderControlOptions(options),
	}
	if !h.storeChoiceWithBack(userID, viewWorkflowDetail, options, back) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复数字管理，0 返回原列表。"
}

func (h *Handler) promptWorkflowCreate(userID, projectID string, page int) string {
	if h.projects == nil || h.workflows == nil {
		return "快捷任务当前不可用。"
	}
	projectInfo, exists := h.projects.Get(projectID)
	if !exists {
		return "这个项目已经不可用。发送“项目”刷新列表。"
	}
	if page <= 0 {
		page = 1
	}
	back := controlOption{Action: actionProjectQuickTasks, Query: projectInfo.ID, Page: page}
	if !h.storeInputWithBack(userID, viewWorkflowCreate, controlWorkflowCreate, back) {
		return controlStateFailureResult().Text
	}
	return strings.Join([]string{
		"新建快捷任务",
		"",
		"项目：" + projectInfo.Name,
		"按下面格式发送一条消息：",
		"",
		"名称：发布检查",
		"内容：检查「分支」并按「格式」输出结果",
		"",
		"书名号中的文字会依次成为运行参数；不需要参数时直接写普通内容。回复 0 返回。",
	}, "\n")
}

func (h *Handler) promptWorkflowRename(userID string, source controlOption) string {
	definition, projectName, ok := h.workflowForControl(userID, source.Query, source.Value)
	if !ok {
		return "快捷任务已经变化。发送“快捷任务”刷新列表。"
	}
	if source.Page <= 0 {
		source.Page = 1
	}
	back := controlOption{Action: actionWorkflowDetail, Query: source.Query, Value: source.Value, Page: source.Page}
	if !h.storeInputWithBack(userID, viewWorkflowRename, controlWorkflowRename, back) {
		return controlStateFailureResult().Text
	}
	return "重命名快捷任务\n\n项目：" + projectName + "\n当前：" + definition.Name + "\n\n发送新名称，回复 0 返回。"
}

func (h *Handler) promptWorkflowEdit(userID string, source controlOption) string {
	definition, projectName, ok := h.workflowForControl(userID, source.Query, source.Value)
	if !ok {
		return "快捷任务已经变化。发送“快捷任务”刷新列表。"
	}
	if source.Page <= 0 {
		source.Page = 1
	}
	back := controlOption{Action: actionWorkflowDetail, Query: source.Query, Value: source.Value, Page: source.Page}
	if !h.storeInputWithBack(userID, viewWorkflowEdit, controlWorkflowEdit, back) {
		return controlStateFailureResult().Text
	}
	return strings.Join([]string{
		"编辑快捷任务",
		"",
		"项目：" + projectName,
		"名称：" + definition.Name,
		"发送完整的新内容；用「参数名」标记运行时需要填写的位置。",
		"",
		"示例：检查「分支」并按「格式」输出结果",
		"",
		"原内容不会显示在微信界面。回复 0 返回。",
	}, "\n")
}

func (h *Handler) confirmWorkflowDelete(userID string, source controlOption) string {
	definition, projectName, ok := h.workflowForControl(userID, source.Query, source.Value)
	if !ok {
		return "快捷任务已经变化。发送“快捷任务”刷新列表。"
	}
	if source.Page <= 0 {
		source.Page = 1
	}
	options := []controlOption{{
		Label: "确认删除", Action: actionWorkflowDelete,
		Value: definition.ID, Query: source.Query, Page: source.Page,
	}}
	back := controlOption{Action: actionWorkflowDetail, Value: definition.ID, Query: source.Query, Page: source.Page}
	prompt := "删除快捷任务\n\n项目：" + projectName + "\n名称：" + definition.Name + "\n\n删除后无法恢复。\n\n" + renderControlOptions(options)
	if !h.storeChoiceWithBack(userID, viewWorkflowDelete, options, back) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复 1 确认，0 返回详情。"
}

func (h *Handler) promptWorkflowSaveFromTask(userID string, source controlOption) string {
	if h.tasks == nil || h.workflows == nil || h.projects == nil {
		return "任务复用当前不可用。"
	}
	task, exists := h.tasks.Find(userID, source.Value)
	if !exists || task.State != taskqueue.StateSucceeded || task.ProjectID != source.Query {
		return "这个成功任务已经变化。发送“任务中心”刷新。"
	}
	if _, exists := h.projects.Get(task.ProjectID); !exists {
		return "任务所属项目已经不可用。"
	}
	prompt, err := h.tasks.LoadReusablePrompt(userID, task.ID)
	if err != nil || !reusablePromptCanBecomeWorkflow(prompt) {
		return "原始请求已过期、包含附件或不符合快捷任务格式，无法保存。"
	}
	if source.Page <= 0 {
		source.Page = 1
	}
	back := controlOption{Action: actionActivityDetail, Value: task.ID, Page: source.Page}
	// Query 冻结任务所属项目；Value 冻结任务 ID，表单中不持久化原始请求。
	back.Query = task.ProjectID
	if !h.storeInputWithBack(userID, viewWorkflowSave, controlWorkflowSave, back) {
		return controlStateFailureResult().Text
	}
	return strings.Join([]string{
		"保存为快捷任务",
		"",
		"项目：" + task.ProjectID,
		"来源：" + task.Summary,
		"",
		"发送快捷任务名称。保存的是原始请求，不包含 Codex 回答、附件或交付内容。",
		"回复 0 返回任务详情。",
	}, "\n")
}

func (h *Handler) createWorkflow(userID, projectID string, form workflowCreateForm) string {
	if h.workflows == nil {
		return "快捷任务当前不可用。"
	}
	definition, err := h.workflows.Create(workflow.CreateInput{
		OwnerID: userID, ProjectID: projectID, Name: form.Name,
		PromptTemplate: form.Content.Prompt, Slots: form.Content.Slots,
	})
	if err != nil {
		return workflowMutationErrorText(err)
	}
	return h.workflowMutationResult(userID, projectID, definition.ID, 1, "快捷任务已创建。")
}

func (h *Handler) renameWorkflow(userID, projectID, workflowID, name string, page int) string {
	if h.workflows == nil {
		return "快捷任务当前不可用。"
	}
	definition, exists := h.workflows.Find(userID, projectID, workflowID)
	if !exists {
		return "快捷任务已经变化。发送“快捷任务”刷新列表。"
	}
	updated, err := h.workflows.Update(userID, projectID, workflowID, workflow.UpdateInput{
		Name: name, PromptTemplate: definition.PromptTemplate, Slots: definition.Slots,
	})
	if err != nil {
		return workflowMutationErrorText(err)
	}
	return h.workflowMutationResult(userID, projectID, updated.ID, page, "快捷任务已重命名。")
}

func (h *Handler) editWorkflow(userID, projectID, workflowID string, content compiledWorkflowContent, page int) string {
	if h.workflows == nil {
		return "快捷任务当前不可用。"
	}
	definition, exists := h.workflows.Find(userID, projectID, workflowID)
	if !exists {
		return "快捷任务已经变化。发送“快捷任务”刷新列表。"
	}
	updated, err := h.workflows.Update(userID, projectID, workflowID, workflow.UpdateInput{
		Name: definition.Name, PromptTemplate: content.Prompt, Slots: content.Slots,
	})
	if err != nil {
		return workflowMutationErrorText(err)
	}
	return h.workflowMutationResult(userID, projectID, updated.ID, page, "快捷任务内容已更新。")
}

func (h *Handler) deleteWorkflow(userID, projectID, workflowID string, page int) string {
	if h.workflows == nil {
		return "快捷任务当前不可用。"
	}
	if err := h.workflows.Delete(userID, projectID, workflowID); err != nil {
		return workflowMutationErrorText(err)
	}
	if page <= 0 {
		page = 1
	}
	options := []controlOption{{Label: "返回快捷任务", Action: actionProjectQuickTasks, Query: projectID, Page: page}}
	prompt := "快捷任务已删除。\n\n" + renderControlOptions(options)
	if !h.storeChoiceWithBack(userID, viewWorkflowResult, options, controlOption{Action: actionProjectCenter}) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回项目中心。"
}

func (h *Handler) saveTaskAsWorkflow(userID, projectID, taskID, name string, page int) string {
	if h.tasks == nil || h.workflows == nil {
		return "任务复用当前不可用。"
	}
	task, exists := h.tasks.Find(userID, taskID)
	if !exists || task.State != taskqueue.StateSucceeded || task.ProjectID != projectID {
		return "这个成功任务已经变化。发送“任务中心”刷新。"
	}
	prompt, err := h.tasks.LoadReusablePrompt(userID, task.ID)
	if err != nil || !reusablePromptCanBecomeWorkflow(prompt) {
		return "原始请求已过期、包含附件或不符合快捷任务格式，无法保存。"
	}
	definition, err := h.workflows.Create(workflow.CreateInput{
		OwnerID: userID, ProjectID: projectID, Name: name,
		PromptTemplate: prompt, Slots: []workflow.Slot{},
	})
	if err != nil {
		return workflowMutationErrorText(err)
	}
	return h.workflowMutationResult(userID, projectID, definition.ID, page, "已从成功任务保存快捷任务。")
}

func (h *Handler) workflowMutationResult(userID, projectID, workflowID string, page int, headline string) string {
	if page <= 0 {
		page = 1
	}
	options := []controlOption{
		{Label: "查看详情", Action: actionWorkflowDetail, Query: projectID, Value: workflowID, Page: page},
		{Label: "返回快捷任务", Action: actionProjectQuickTasks, Query: projectID, Page: page},
	}
	prompt := headline + "\n\n" + renderControlOptions(options)
	if !h.storeChoiceWithBack(userID, viewWorkflowResult, options, controlOption{Action: actionProjectQuickTasks, Query: projectID, Page: page}) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回快捷任务。"
}

func (h *Handler) workflowForControl(userID, projectID, workflowID string) (workflow.Definition, string, bool) {
	if h.projects == nil || h.workflows == nil {
		return workflow.Definition{}, "", false
	}
	projectInfo, exists := h.projects.Get(projectID)
	if !exists {
		return workflow.Definition{}, "", false
	}
	definition, exists := h.workflows.Find(userID, projectInfo.ID, workflowID)
	return definition, projectInfo.Name, exists
}

func parseWorkflowCreateForm(raw string) (workflowCreateForm, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	lines := strings.Split(raw, "\n")
	if len(lines) < 2 {
		return workflowCreateForm{}, fmt.Errorf("请同时填写名称和内容")
	}
	name, ok := cutWorkflowField(strings.TrimSpace(lines[0]), "名称")
	if !ok || !validWorkflowDisplayName(name) {
		return workflowCreateForm{}, fmt.Errorf("名称不能为空，且最多 32 个字")
	}
	remainder := strings.TrimSpace(strings.Join(lines[1:], "\n"))
	content, ok := cutWorkflowField(remainder, "内容")
	if !ok {
		return workflowCreateForm{}, fmt.Errorf("第二项必须以“内容：”开头")
	}
	compiled, err := compileReadableWorkflowContent(content)
	if err != nil {
		return workflowCreateForm{}, err
	}
	return workflowCreateForm{Name: name, Content: compiled}, nil
}

func cutWorkflowField(value, field string) (string, bool) {
	for _, separator := range []string{"：", ":"} {
		prefix := field + separator
		if strings.HasPrefix(value, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(value, prefix)), true
		}
	}
	return "", false
}

func compileReadableWorkflowContent(content string) (compiledWorkflowContent, error) {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" || !utf8.ValidString(content) {
		return compiledWorkflowContent{}, fmt.Errorf("内容不能为空")
	}
	if strings.ContainsRune(content, '\x00') || strings.Contains(content, "{{") || strings.Contains(content, "}}") {
		return compiledWorkflowContent{}, fmt.Errorf("内容包含不支持的模板符号，请使用「参数名」")
	}
	matches := readableWorkflowSlotRE.FindAllStringSubmatchIndex(content, -1)
	labels := make(map[string]string)
	slots := make([]workflow.Slot, 0, len(matches))
	var prompt strings.Builder
	cursor := 0
	for _, match := range matches {
		if strings.ContainsAny(content[cursor:match[0]], "「」") {
			return compiledWorkflowContent{}, fmt.Errorf("参数标记没有成对闭合")
		}
		prompt.WriteString(content[cursor:match[0]])
		label := strings.TrimSpace(content[match[2]:match[3]])
		if label == "" || strings.ContainsAny(label, "\r\n\x00") || len([]rune(label)) > 24 {
			return compiledWorkflowContent{}, fmt.Errorf("参数名不能为空，且最多 24 个字")
		}
		key, exists := labels[label]
		if !exists {
			if len(slots) >= workflow.MaxSlots {
				return compiledWorkflowContent{}, fmt.Errorf("一个快捷任务最多包含 %d 个参数", workflow.MaxSlots)
			}
			key = fmt.Sprintf("slot_%d", len(slots)+1)
			labels[label] = key
			slots = append(slots, workflow.Slot{Key: key, Label: label})
		}
		prompt.WriteString("{{" + key + "}}")
		cursor = match[1]
	}
	if strings.ContainsAny(content[cursor:], "「」") {
		return compiledWorkflowContent{}, fmt.Errorf("参数标记没有成对闭合")
	}
	prompt.WriteString(content[cursor:])
	if len([]rune(prompt.String())) > 8000 {
		return compiledWorkflowContent{}, fmt.Errorf("内容最多 8,000 个字")
	}
	return compiledWorkflowContent{Prompt: prompt.String(), Slots: slots}, nil
}

func validWorkflowDisplayName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && utf8.ValidString(name) && !strings.ContainsAny(name, "\r\n\x00") && len([]rune(name)) <= 32
}

func reusablePromptCanBecomeWorkflow(prompt string) bool {
	prompt = strings.TrimSpace(prompt)
	return prompt != "" && utf8.ValidString(prompt) && !strings.ContainsRune(prompt, '\x00') &&
		len([]rune(prompt)) <= 8000 && !strings.Contains(prompt, "{{") && !strings.Contains(prompt, "}}")
}

func workflowParameterPrompt(status workflow.RunStatus) string {
	return strings.Join([]string{
		"填写快捷任务参数",
		"",
		"任务：" + status.WorkflowName,
		fmt.Sprintf("进度：%d / %d", status.Position, status.Total),
		"当前：" + status.Slot.Label,
		"",
		"直接发送参数值；回复“取消”停止本次运行。",
	}, "\n")
}

func workflowMutationErrorText(err error) string {
	if err == nil {
		return "快捷任务操作失败。"
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "name already exists"):
		return "当前项目已有同名快捷任务，请换一个名称。"
	case strings.Contains(message, "capacity"):
		return "快捷任务数量已达上限，请先删除不再需要的快捷任务。"
	case strings.Contains(message, "not found"):
		return "快捷任务已经变化。发送“快捷任务”刷新列表。"
	case strings.Contains(message, "name is invalid"):
		return "名称不能为空，且最多 32 个字。"
	case strings.Contains(message, "prompt template is invalid"), strings.Contains(message, "slot is invalid"), strings.Contains(message, "placeholders"):
		return "快捷任务内容或参数格式无效，请重新编辑。"
	default:
		return workflowUnavailableText()
	}
}

func workflowUnavailableText() string {
	return "快捷任务状态暂不可用，请稍后重新打开。"
}
