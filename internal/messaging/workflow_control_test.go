package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/internal/config"
	"github.com/huixiangyang/weclaw/internal/ilink"
	"github.com/huixiangyang/weclaw/internal/project"
	"github.com/huixiangyang/weclaw/internal/workflow"
)

type workflowTestEnvironment struct {
	handler      *Handler
	projects     *project.Manager
	workflows    *workflow.Store
	workflowPath string
	controlPath  string
}

func newWorkflowTestEnvironment(t *testing.T) workflowTestEnvironment {
	t.Helper()
	root := t.TempDir()
	projectRoot := filepath.Join(root, "alpha")
	if err := os.Mkdir(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	projects, err := project.NewManager([]config.ProjectConfig{{
		ID: "alpha", Name: "Alpha", Root: projectRoot,
	}}, filepath.Join(root, "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(root, "workflows.json")
	workflows, err := workflow.NewStore(workflowPath, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(root, "control-state.json")
	controls, err := NewControlStateStore(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(newHandlerThreadClient())
	handler.SetProjectManager(projects)
	attachTestSessionManager(t, handler)
	handler.SetWorkflowStore(workflows)
	handler.SetControlStateStore(controls)
	return workflowTestEnvironment{
		handler: handler, projects: projects, workflows: workflows,
		workflowPath: workflowPath, controlPath: controlPath,
	}
}

func TestReadableWorkflowTemplateCompilesStableSlots(t *testing.T) {
	form, err := parseWorkflowCreateForm("名称：发布检查\n内容：检查「分支」并按「格式」输出，再确认「分支」")
	if err != nil {
		t.Fatal(err)
	}
	if form.Name != "发布检查" || form.Content.Prompt != "检查{{slot_1}}并按{{slot_2}}输出，再确认{{slot_1}}" {
		t.Fatalf("compiled form = %#v", form)
	}
	if len(form.Content.Slots) != 2 || form.Content.Slots[0].Label != "分支" || form.Content.Slots[1].Label != "格式" {
		t.Fatalf("compiled slots = %#v", form.Content.Slots)
	}
	for _, raw := range []string{
		"名称：错误\n内容：检查「分支",
		"名称：错误\n内容：检查 {{branch}}",
		"名称：错误\n内容：检查「一」「二」「三」「四」「五」「六」「七」「八」「九」",
	} {
		if _, parseErr := parseWorkflowCreateForm(raw); parseErr == nil {
			t.Fatalf("invalid form accepted: %q", raw)
		}
	}
}

func TestCommandDirectoryOpensPromptTemplateManagement(t *testing.T) {
	environment := newWorkflowTestEnvironment(t)
	definition, err := environment.workflows.Create(workflow.CreateInput{
		OwnerID: "owner-1", ProjectID: "alpha", Name: "发布检查", PromptTemplate: "检查发布", Slots: []workflow.Slot{},
	})
	if err != nil {
		t.Fatal(err)
	}
	environment.handler.openMainMenu(context.Background(), "owner-1")
	management, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "56", false, nextTestControlSource(),
	)
	if !handled || !strings.HasPrefix(management.Text, "提示词模板\n") || !strings.Contains(management.Text, "2  发布检查") {
		t.Fatalf("workflow management = %#v handled=%v", management, handled)
	}
	state, status, err := environment.handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || state.View != viewProjectQuickTasks || len(state.Options) != 2 ||
		state.Options[1].Action != actionWorkflowDetail || state.Options[1].Value != definition.ID {
		t.Fatalf("workflow run state = %#v status=%v err=%v", state, status, err)
	}
}

func TestWorkflowCRUDCompletesInsideWeChatControlFlow(t *testing.T) {
	environment := newWorkflowTestEnvironment(t)
	handler := environment.handler

	prompt := controlReply(t, handler, "owner-1", "新建提示词模板")
	if !strings.Contains(prompt, "名称：发布检查") || !strings.Contains(prompt, "内容：检查") {
		t.Fatalf("create prompt = %q", prompt)
	}
	createSource := nextTestControlSource()
	created, handled := handler.handleControlInput(
		context.Background(), "owner-1",
		"名称：发布检查\n内容：检查「分支」并按「格式」输出", false, createSource,
	)
	if !handled || !strings.Contains(created.Text, "已创建") {
		t.Fatalf("created = %#v, handled=%v", created, handled)
	}
	duplicate, handled := handler.handleControlInput(
		context.Background(), "owner-1",
		"名称：发布检查\n内容：检查「分支」并按「格式」输出", false, createSource,
	)
	if !handled || !strings.Contains(duplicate.Text, "不会重复执行") || len(environment.workflows.List("owner-1", "alpha")) != 1 {
		t.Fatalf("duplicate create = %#v, handled=%v", duplicate, handled)
	}

	menu := controlReply(t, handler, "owner-1", "提示词模板")
	if !strings.Contains(menu, "发布检查 · 2 个参数") {
		t.Fatalf("workflow menu = %q", menu)
	}
	detail := controlReply(t, handler, "owner-1", "2")
	if !strings.Contains(detail, "参数：分支、格式") || strings.Contains(detail, "{{slot_") {
		t.Fatalf("workflow detail = %q", detail)
	}
	_ = controlReply(t, handler, "owner-1", "2")
	renamed := controlReply(t, handler, "owner-1", "上线检查")
	if !strings.Contains(renamed, "已重命名") {
		t.Fatalf("rename result = %q", renamed)
	}

	_ = controlReply(t, handler, "owner-1", "提示词模板")
	_ = controlReply(t, handler, "owner-1", "2")
	_ = controlReply(t, handler, "owner-1", "3")
	edited := controlReply(t, handler, "owner-1", "审查「目标」并输出结论")
	if !strings.Contains(edited, "内容已更新") {
		t.Fatalf("edit result = %q", edited)
	}
	definitions := environment.workflows.List("owner-1", "alpha")
	if len(definitions) != 1 || definitions[0].Name != "上线检查" || definitions[0].PromptTemplate != "审查{{slot_1}}并输出结论" || len(definitions[0].Slots) != 1 {
		t.Fatalf("updated workflow = %#v", definitions)
	}

	_ = controlReply(t, handler, "owner-1", "提示词模板")
	_ = controlReply(t, handler, "owner-1", "2")
	confirm := controlReply(t, handler, "owner-1", "4")
	if !strings.Contains(confirm, "删除后无法恢复") {
		t.Fatalf("delete confirmation = %q", confirm)
	}
	deleted := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(deleted, "已删除") || len(environment.workflows.List("owner-1", "alpha")) != 0 {
		t.Fatalf("delete result = %q", deleted)
	}

	controlRaw, err := os.ReadFile(environment.controlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, privateText := range []string{"检查「分支」", "审查「目标」", "上线检查"} {
		if strings.Contains(string(controlRaw), privateText) {
			t.Fatalf("control state leaked display or template content %q: %s", privateText, controlRaw)
		}
	}
}

func TestWorkflowCreateValidationKeepsFormAndCenterPaginates(t *testing.T) {
	environment := newWorkflowTestEnvironment(t)
	_ = controlReply(t, environment.handler, "owner-1", "新建提示词模板")
	invalid, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "名称：缺少内容", false, nextTestControlSource(),
	)
	if !handled || !strings.Contains(invalid.Text, "格式未通过") {
		t.Fatalf("invalid create form = %#v, handled=%v", invalid, handled)
	}
	state, status, err := environment.handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || state.Mode != controlWorkflowCreate {
		t.Fatalf("create form was not preserved = %#v, status=%v err=%v", state, status, err)
	}
	for index := 1; index <= 7; index++ {
		if _, err := environment.workflows.Create(workflow.CreateInput{
			OwnerID: "owner-1", ProjectID: "alpha",
			Name: "任务" + string(rune('A'+index-1)), PromptTemplate: "执行检查", Slots: []workflow.Slot{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	menu := environment.handler.openWorkflowCenter("owner-1", "alpha", 1)
	if !strings.Contains(menu, "数量：7 项") {
		t.Fatalf("first workflow page = %q", menu)
	}
	state, status, err = environment.handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || len(state.Options) != 8 || state.Options[7].Action != actionProjectQuickTasks || state.Options[7].Page != 2 {
		t.Fatalf("first workflow page state = %#v, status=%v err=%v", state, status, err)
	}
	second := controlReply(t, environment.handler, "owner-1", "8")
	if !strings.Contains(second, "页码：2 / 2") {
		t.Fatalf("second workflow page = %q", second)
	}
}

func TestWorkflowParameterCancellationIsIdempotent(t *testing.T) {
	environment := newWorkflowTestEnvironment(t)
	definition, err := environment.workflows.Create(workflow.CreateInput{
		OwnerID: "owner-1", ProjectID: "alpha", Name: "检查",
		PromptTemplate: "检查 {{slot_1}}", Slots: []workflow.Slot{{Key: "slot_1", Label: "目标"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = environment.handler.runProjectQuickTask("owner-1", "alpha", definition.ID)
	source := nextTestControlSource()
	cancelled, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "取消", false, source,
	)
	if !handled || !strings.Contains(cancelled.Text, "已取消本次模板运行") {
		t.Fatalf("cancelled = %#v, handled=%v", cancelled, handled)
	}
	if _, exists, err := environment.workflows.PendingRun("owner-1"); err != nil || exists {
		t.Fatalf("cancelled run exists=%v err=%v", exists, err)
	}
	duplicate, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "取消", false, source,
	)
	if !handled || !strings.Contains(duplicate.Text, "不会重复执行") {
		t.Fatalf("duplicate cancellation = %#v, handled=%v", duplicate, handled)
	}
}

func TestWorkflowParametersResumeAfterRestartAndEnqueueOnce(t *testing.T) {
	environment := newWorkflowTestEnvironment(t)
	definition, err := environment.workflows.Create(workflow.CreateInput{
		OwnerID: "owner-1", ProjectID: "alpha", Name: "发布检查",
		PromptTemplate: "检查 {{slot_1}}，输出 {{slot_2}}", Slots: []workflow.Slot{
			{Key: "slot_1", Label: "分支"}, {Key: "slot_2", Label: "格式"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	started := environment.handler.runProjectQuickTask("owner-1", "alpha", definition.ID)
	if started.Effect.Kind != EffectNone || !strings.Contains(started.Text, "进度：1 / 2") {
		t.Fatalf("started = %#v", started)
	}
	menu, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "/", false, nextTestControlSource(),
	)
	if !handled || !strings.Contains(menu.Text, "WeClaw") {
		t.Fatalf("menu during parameters = %#v, handled=%v", menu, handled)
	}
	projectCenter, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "2", false, nextTestControlSource(),
	)
	if !handled || !strings.Contains(projectCenter.Text, "Codex 执行环境") {
		t.Fatalf("menu choice during parameters = %#v, handled=%v", projectCenter, handled)
	}
	resumed, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "提示词模板", false, nextTestControlSource(),
	)
	if !handled || !strings.Contains(resumed.Text, "进度：1 / 2") {
		t.Fatalf("resumed parameters = %#v, handled=%v", resumed, handled)
	}
	attachment, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "图片参数", true, nextTestControlSource(),
	)
	if !handled || !strings.Contains(attachment.Text, "只接受文字") {
		t.Fatalf("attachment parameter = %#v, handled=%v", attachment, handled)
	}
	first, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "123", false, nextTestControlSource(),
	)
	if !handled || !strings.Contains(first.Text, "进度：2 / 2") || !strings.Contains(first.Text, "格式") {
		t.Fatalf("first parameter = %#v, handled=%v", first, handled)
	}

	reopenedWorkflows, err := workflow.NewStore(environment.workflowPath, []string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	reopenedControls, err := NewControlStateStore(environment.controlPath)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewHandler(newHandlerThreadClient())
	restarted.SetProjectManager(environment.projects)
	restarted.SetWorkflowStore(reopenedWorkflows)
	restarted.SetControlStateStore(reopenedControls)
	finalSource := nextTestControlSource()
	completed, handled := restarted.handleControlInput(
		context.Background(), "owner-1", "简报", false, finalSource,
	)
	if !handled || completed.Effect.Kind != EffectEnqueuePrompt || completed.Effect.Value != "检查 123，输出 简报" || completed.Effect.ProjectID != "alpha" || completed.workflowRollback == nil {
		t.Fatalf("completed = %#v, handled=%v", completed, handled)
	}
	duplicate, handled := restarted.handleControlInput(
		context.Background(), "owner-1", "简报", false, finalSource,
	)
	if !handled || duplicate.Effect.Kind != EffectNone || !strings.Contains(duplicate.Text, "不会重复执行") {
		t.Fatalf("duplicate final parameter = %#v, handled=%v", duplicate, handled)
	}
}

func TestWorkflowFinalParameterRestoresOnQueueFailure(t *testing.T) {
	environment := newWorkflowTestEnvironment(t)
	definition, err := environment.workflows.Create(workflow.CreateInput{
		OwnerID: "owner-1", ProjectID: "alpha", Name: "发布检查",
		PromptTemplate: "检查 {{slot_1}}，输出 {{slot_2}}", Slots: []workflow.Slot{
			{Key: "slot_1", Label: "分支"}, {Key: "slot_2", Label: "格式"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = environment.handler.runProjectQuickTask("owner-1", "alpha", definition.ID)
	if result, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "main", false, nextTestControlSource(),
	); !handled || !strings.Contains(result.Text, "进度：2 / 2") {
		t.Fatalf("first parameter = %#v, handled=%v", result, handled)
	}
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1"})
	message := ilink.WeixinMessage{
		MessageID: 9901, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "简报"}}},
	}
	if err := environment.handler.HandleMessage(context.Background(), client, message); err == nil || !strings.Contains(err.Error(), "task queue is not initialized") {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	status, exists, err := environment.workflows.PendingRun("owner-1")
	if err != nil || !exists || status.Position != 2 || status.Slot.Label != "格式" {
		t.Fatalf("restored pending run = %#v, exists=%v err=%v", status, exists, err)
	}
	sourceKey, err := sourceMessageKey(client, message)
	if err != nil {
		t.Fatal(err)
	}
	if environment.workflows.HasRunReceipt("owner-1", sourceKey) {
		t.Fatal("failed final parameter kept its workflow receipt")
	}
	retry, handled := environment.handler.handleControlInput(
		context.Background(), "owner-1", "简报", false, sourceKey,
	)
	if !handled || retry.Effect.Kind != EffectEnqueuePrompt || retry.Effect.Value != "检查 main，输出 简报" {
		t.Fatalf("retried final parameter = %#v, handled=%v", retry, handled)
	}
}
