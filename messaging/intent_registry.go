package messaging

import (
	"fmt"
	"sort"
	"strings"
)

type IntentID string

const (
	IntentMenu              IntentID = "system.menu"
	IntentGuide             IntentID = "system.guide"
	IntentRuntime           IntentID = "system.runtime"
	IntentNoReplyDiagnostic IntentID = "system.no_reply_diagnostic"
	IntentCancel            IntentID = "task.cancel"
	IntentTaskStatus        IntentID = "task.status"
	IntentTaskCenter        IntentID = "task.center"
	IntentTaskContinue      IntentID = "task.continue_in_thread"
	IntentTaskRerun         IntentID = "task.rerun"
	IntentTaskRerunNew      IntentID = "task.continue_in_new_thread"
	IntentQueuePause        IntentID = "task.queue_pause"
	IntentQueueResume       IntentID = "task.queue_resume"
	IntentQueueClear        IntentID = "task.queue_clear"
	IntentProjectCenter     IntentID = "project.center"
	IntentProjectQuickTasks IntentID = "project.quick_tasks"
	IntentWorkflowNew       IntentID = "project.workflow_new"
	IntentWorkflowSaveLast  IntentID = "project.workflow_save_last"
	IntentProjectSelect     IntentID = "project.select"
	IntentSessionCenter     IntentID = "session.center"
	IntentSessionSelect     IntentID = "session.select"
	IntentSessionSearch     IntentID = "session.search"
	IntentSessionCurrent    IntentID = "session.current"
	IntentSessionRestore    IntentID = "session.restore"
	IntentSessionNew        IntentID = "session.new"
	IntentSessionRename     IntentID = "session.rename"
	IntentSessionArchive    IntentID = "session.archive"
	IntentResponseModes     IntentID = "preference.response_modes"
	IntentResponseVoice     IntentID = "preference.response_voice"
	IntentResponseAdaptive  IntentID = "preference.response_adaptive"
	IntentResponseReading   IntentID = "preference.response_reading"
	IntentVisualStyle       IntentID = "preference.visual_style"
	IntentLibraryCenter     IntentID = "library.center"
	IntentAutomationCenter  IntentID = "automation.center"
	IntentVoiceBriefing     IntentID = "automation.voice_briefing"
	IntentRemoteLock        IntentID = "security.remote_lock"
)

type IntentDefinition struct {
	ID                   IntentID
	Domain               ActionDomain
	ExactPhrases         []string
	ArgumentPrefixes     []string
	AllowEmptyArgument   bool
	AllowAttachments     bool
	MutatesState         bool
	AllowDuringTask      bool
	AllowDuringDrain     bool
	RequiresContextToken bool
	RequiresReceipt      bool
	AuditEvent           string
}

type intentPrefix struct {
	phrase     string
	runeCount  int
	definition IntentDefinition
}

type IntentRegistry struct {
	exact    map[string]IntentDefinition
	prefixes []intentPrefix
	byID     map[IntentID]IntentDefinition
}

type ResolvedIntent struct {
	Definition IntentDefinition
	Argument   string
}

func NewIntentRegistry(definitions []IntentDefinition) (*IntentRegistry, error) {
	registry := &IntentRegistry{
		exact: make(map[string]IntentDefinition),
		byID:  make(map[IntentID]IntentDefinition),
	}
	prefixOwner := make(map[string]IntentDefinition)
	for _, source := range definitions {
		definition := cloneIntentDefinition(source)
		if strings.TrimSpace(string(definition.ID)) == "" || !definition.Domain.valid() || strings.TrimSpace(definition.AuditEvent) == "" {
			return nil, fmt.Errorf("invalid intent definition %q", definition.ID)
		}
		if definition.MutatesState && !definition.RequiresReceipt {
			return nil, fmt.Errorf("mutating intent %s requires an idempotency receipt", definition.ID)
		}
		if len(definition.ExactPhrases) == 0 && len(definition.ArgumentPrefixes) == 0 {
			return nil, fmt.Errorf("intent %s has no phrases", definition.ID)
		}
		if _, exists := registry.byID[definition.ID]; exists {
			return nil, fmt.Errorf("duplicated intent id %s", definition.ID)
		}
		registry.byID[definition.ID] = definition
		for _, raw := range definition.ExactPhrases {
			phrase := intentPhraseKey(raw)
			if phrase == "" {
				return nil, fmt.Errorf("intent %s has an empty phrase", definition.ID)
			}
			if previous, exists := registry.exact[phrase]; exists {
				return nil, fmt.Errorf("intent phrase %q belongs to both %s and %s", phrase, previous.ID, definition.ID)
			}
			registry.exact[phrase] = definition
		}
		for _, raw := range definition.ArgumentPrefixes {
			normalized := normalizeControlPhrase(raw)
			phrase := intentPhraseKey(raw)
			if phrase == "" {
				return nil, fmt.Errorf("intent %s has an empty argument prefix", definition.ID)
			}
			if previous, exists := prefixOwner[phrase]; exists {
				return nil, fmt.Errorf("intent prefix %q belongs to both %s and %s", phrase, previous.ID, definition.ID)
			}
			prefixOwner[phrase] = definition
			registry.prefixes = append(registry.prefixes, intentPrefix{phrase: phrase, runeCount: len([]rune(normalized)), definition: definition})
		}
	}

	for phrase, exactDefinition := range registry.exact {
		for _, prefix := range registry.prefixes {
			if exactDefinition.ID != prefix.definition.ID && strings.HasPrefix(phrase, prefix.phrase) {
				return nil, fmt.Errorf("exact intent %q for %s is captured by prefix for %s", phrase, exactDefinition.ID, prefix.definition.ID)
			}
		}
	}
	for left := range registry.prefixes {
		for right := left + 1; right < len(registry.prefixes); right++ {
			first, second := registry.prefixes[left], registry.prefixes[right]
			if first.definition.ID != second.definition.ID && (strings.HasPrefix(first.phrase, second.phrase) || strings.HasPrefix(second.phrase, first.phrase)) {
				return nil, fmt.Errorf("overlapping intent prefixes %q and %q", first.phrase, second.phrase)
			}
		}
	}
	sort.Slice(registry.prefixes, func(left, right int) bool {
		if len(registry.prefixes[left].phrase) == len(registry.prefixes[right].phrase) {
			return registry.prefixes[left].phrase < registry.prefixes[right].phrase
		}
		return len(registry.prefixes[left].phrase) > len(registry.prefixes[right].phrase)
	})
	return registry, nil
}

func (registry *IntentRegistry) Resolve(text string) (ResolvedIntent, bool) {
	if registry == nil {
		return ResolvedIntent{}, false
	}
	text = normalizeControlPhrase(text)
	key := intentPhraseKey(text)
	if key == "" {
		return ResolvedIntent{}, false
	}
	if definition, exists := registry.exact[key]; exists {
		return ResolvedIntent{Definition: cloneIntentDefinition(definition)}, true
	}
	for _, candidate := range registry.prefixes {
		if !strings.HasPrefix(key, candidate.phrase) {
			continue
		}
		runes := []rune(text)
		if len(runes) < candidate.runeCount {
			continue
		}
		argument := strings.TrimSpace(string(runes[candidate.runeCount:]))
		if argument == "" && !candidate.definition.AllowEmptyArgument {
			continue
		}
		return ResolvedIntent{Definition: cloneIntentDefinition(candidate.definition), Argument: argument}, true
	}
	return ResolvedIntent{}, false
}

func intentPhraseKey(value string) string {
	return strings.ToLower(normalizeControlPhrase(value))
}

func (registry *IntentRegistry) Definition(id IntentID) (IntentDefinition, bool) {
	if registry == nil {
		return IntentDefinition{}, false
	}
	definition, exists := registry.byID[id]
	return cloneIntentDefinition(definition), exists
}

func cloneIntentDefinition(source IntentDefinition) IntentDefinition {
	copy := source
	copy.ExactPhrases = append([]string(nil), source.ExactPhrases...)
	copy.ArgumentPrefixes = append([]string(nil), source.ArgumentPrefixes...)
	return copy
}

func mustDefaultIntentRegistry() *IntentRegistry {
	registry, err := NewIntentRegistry(defaultIntentDefinitions())
	if err != nil {
		panic(err)
	}
	return registry
}

func defaultIntentDefinitions() []IntentDefinition {
	return []IntentDefinition{
		{ID: IntentMenu, Domain: DomainSystem, ExactPhrases: []string{"/", "菜单", "打开菜单", "功能", "操作", "功能菜单", "打开功能"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_menu"},
		{ID: IntentGuide, Domain: DomainSystem, ExactPhrases: []string{"帮助", "怎么用", "使用说明"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_guide"},
		{ID: IntentRuntime, Domain: DomainSystem, ExactPhrases: []string{"运行中心", "运行信息", "系统信息", "服务信息", "Codex 信息", "Codex信息"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_runtime"},
		{ID: IntentNoReplyDiagnostic, Domain: DomainSystem, ExactPhrases: []string{"为什么没回复", "怎么没回复", "为什么没反应", "怎么没反应", "没响应", "没有响应", "没回复"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "diagnose_no_reply"},
		{ID: IntentCancel, Domain: DomainTask, ExactPhrases: []string{"取消", "取消任务", "停止", "停止任务", "停下", "停一下"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "cancel_task_or_operation"},
		{ID: IntentTaskStatus, Domain: DomainTask, ExactPhrases: []string{"状态", "查看状态", "看下状态", "任务状态", "进度", "任务进度", "查看进度", "进度怎么样", "现在怎么样了", "怎么样了"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_task_status"},
		{ID: IntentTaskCenter, Domain: DomainTask, ExactPhrases: []string{"任务队列", "排队任务", "任务中心"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_task_center"},
		{ID: IntentTaskContinue, Domain: DomainTask, ExactPhrases: []string{"继续处理", "继续上次任务", "继续这个任务"}, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "continue_task_session"},
		{ID: IntentTaskRerun, Domain: DomainTask, ExactPhrases: []string{"重跑上次任务", "重试上次任务", "再执行一次"}, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "rerun_last_task"},
		{ID: IntentTaskRerunNew, Domain: DomainTask, ExactPhrases: []string{"在新会话继续", "新会话重跑"}, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "rerun_last_task_in_new_session"},
		{ID: IntentQueuePause, Domain: DomainTask, ExactPhrases: []string{"暂停队列", "暂停任务队列", "停止排队任务"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "pause_queue"},
		{ID: IntentQueueResume, Domain: DomainTask, ExactPhrases: []string{"继续队列", "恢复队列", "继续任务队列"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "resume_queue"},
		{ID: IntentQueueClear, Domain: DomainTask, ExactPhrases: []string{"清空队列", "删除排队任务"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "request_clear_queue"},
		{ID: IntentProjectCenter, Domain: DomainProject, ExactPhrases: []string{"项目", "项目中心", "项目列表", "查看项目", "当前项目"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_project_center"},
		{ID: IntentProjectQuickTasks, Domain: DomainProject, ExactPhrases: []string{"快捷任务", "项目快捷任务", "快速任务"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_quick_tasks"},
		{ID: IntentWorkflowNew, Domain: DomainProject, ExactPhrases: []string{"新建快捷任务", "创建快捷任务", "添加快捷任务"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "prompt_workflow_create"},
		{ID: IntentWorkflowSaveLast, Domain: DomainProject, ExactPhrases: []string{"保存为快捷任务", "把上次任务保存为快捷任务"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "prompt_workflow_save_from_task"},
		{ID: IntentProjectSelect, Domain: DomainProject, ArgumentPrefixes: []string{"切换项目", "切到项目", "进入项目"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "select_project"},
		{ID: IntentSessionCenter, Domain: DomainSession, ExactPhrases: []string{"会话", "查看会话", "看看会话", "会话菜单"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_session_center"},
		{ID: IntentSessionSelect, Domain: DomainSession, ExactPhrases: []string{"会话列表", "列出会话", "选择会话"}, ArgumentPrefixes: []string{"切换会话", "切换到会话", "切到会话", "进入会话"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "select_session"},
		{ID: IntentSessionSearch, Domain: DomainSession, ArgumentPrefixes: []string{"搜索会话", "查找会话", "找会话"}, AllowEmptyArgument: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "search_session"},
		{ID: IntentSessionCurrent, Domain: DomainSession, ExactPhrases: []string{"当前会话", "这个会话", "会话详情"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_current_session"},
		{ID: IntentSessionRestore, Domain: DomainSession, ExactPhrases: []string{"已归档会话"}, ArgumentPrefixes: []string{"恢复会话", "找回会话"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "restore_session"},
		{ID: IntentSessionNew, Domain: DomainSession, ArgumentPrefixes: []string{"新建会话", "创建会话", "开一个新会话", "开个新会话"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "create_session"},
		{ID: IntentSessionRename, Domain: DomainSession, ArgumentPrefixes: []string{"重命名当前会话", "当前会话改名", "把当前会话改名"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "rename_session"},
		{ID: IntentSessionArchive, Domain: DomainSession, ExactPhrases: []string{"归档当前会话", "把当前会话归档", "归档这个会话"}, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "archive_session"},
		{ID: IntentResponseModes, Domain: DomainPreference, ExactPhrases: []string{"回答方式", "回复方式", "输出方式", "偏好设置", "语音模式"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_response_modes"},
		{ID: IntentResponseVoice, Domain: DomainPreference, ExactPhrases: []string{"开启语音模式", "打开语音模式", "启用语音模式"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "set_response_voice"},
		{ID: IntentResponseAdaptive, Domain: DomainPreference, ExactPhrases: []string{"关闭语音模式", "退出语音模式", "自适应模式"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "set_response_adaptive"},
		{ID: IntentResponseReading, Domain: DomainPreference, ExactPhrases: []string{"开启阅读模式", "打开阅读模式", "阅读模式"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "set_response_reading"},
		{ID: IntentVisualStyle, Domain: DomainPreference, ExactPhrases: []string{"主题风格"}, ArgumentPrefixes: []string{"视觉风格", "卡片风格", "更换风格", "切换风格"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "set_visual_style"},
		{ID: IntentLibraryCenter, Domain: DomainLibrary, ExactPhrases: []string{"素材箱", "素材中心", "收藏链接", "交付记录", "交付中心"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_library"},
		{ID: IntentAutomationCenter, Domain: DomainAutomation, ExactPhrases: []string{"自动化", "自动化中心", "自动检查", "检查计划"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_automation"},
		{ID: IntentVoiceBriefing, Domain: DomainAutomation, ExactPhrases: []string{"语音简报", "播放简报", "工作简报", "发语音", "发个语音", "来段语音", "播报一下", "读给我听"}, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, RequiresContextToken: true, AuditEvent: "send_voice_briefing"},
		{ID: IntentRemoteLock, Domain: DomainSecurity, ExactPhrases: []string{"远程锁定", "锁定 WeClaw", "锁定WeClaw", "锁定服务"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "remote_lock"},
	}
}
