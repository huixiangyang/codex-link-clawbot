package control

import (
	"fmt"
	"sort"
	"strings"
)

type ID string

const (
	IntentMenu              ID = "system.menu"
	IntentGuide             ID = "system.guide"
	IntentRuntime           ID = "system.runtime"
	IntentNoReplyDiagnostic ID = "system.no_reply_diagnostic"
	IntentCancel            ID = "queue.cancel"
	IntentTaskStatus        ID = "queue.status"
	IntentTaskCenter        ID = "queue.center"
	IntentTaskContinue      ID = "queue.continue_in_thread"
	IntentTaskRerun         ID = "queue.rerun"
	IntentTaskRerunNew      ID = "queue.continue_in_new_thread"
	IntentQueuePause        ID = "queue.pause"
	IntentQueueResume       ID = "queue.resume"
	IntentQueueClear        ID = "queue.clear"
	IntentProjectCenter     ID = "workspace.center"
	IntentProjectSelect     ID = "workspace.select"
	IntentSessionCenter     ID = "thread.center"
	IntentSessionSelect     ID = "thread.select"
	IntentSessionSearch     ID = "thread.search"
	IntentSessionCurrent    ID = "thread.current"
	IntentThreadRelations   ID = "thread.relations"
	IntentSessionRestore    ID = "thread.restore"
	IntentSessionNew        ID = "thread.new"
	IntentSessionRename     ID = "thread.rename"
	IntentSessionArchive    ID = "thread.archive"
	IntentThreadFork        ID = "thread.fork"
	IntentThreadPin         ID = "thread.pin"
	IntentThreadCompact     ID = "thread.compact"
	IntentThreadGoal        ID = "thread.goal.set"
	IntentThreadGoalClear   ID = "thread.goal.clear"
	IntentThreadReview      ID = "thread.review"
	IntentThreadDelete      ID = "thread.delete"
	IntentThreadModels      ID = "thread.models"
	IntentThreadEffort      ID = "thread.effort"
	IntentTurnSteer         ID = "turn.steer"
	IntentResponseModes     ID = "preference.response_modes"
	IntentResponseVoice     ID = "preference.response_voice"
	IntentResponseAdaptive  ID = "preference.response_adaptive"
	IntentResponseReading   ID = "preference.response_reading"
	IntentVisualStyle       ID = "preference.visual_style"
	IntentDeliveryBox       ID = "delivery.center"
	IntentVoiceBriefing     ID = "result.voice_briefing"
	IntentRemoteLock        ID = "security.remote_lock"
)

type Definition struct {
	ID                   ID
	Domain               Domain
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
	definition Definition
}

type Registry struct {
	exact    map[string]Definition
	prefixes []intentPrefix
	byID     map[ID]Definition
}

type Resolved struct {
	Definition Definition
	Argument   string
}

func NewRegistry(definitions []Definition) (*Registry, error) {
	registry := &Registry{
		exact: make(map[string]Definition),
		byID:  make(map[ID]Definition),
	}
	prefixOwner := make(map[string]Definition)
	for _, source := range definitions {
		definition := cloneIntentDefinition(source)
		if strings.TrimSpace(string(definition.ID)) == "" || !definition.Domain.Valid() || strings.TrimSpace(definition.AuditEvent) == "" {
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

func (registry *Registry) Resolve(text string) (Resolved, bool) {
	if registry == nil {
		return Resolved{}, false
	}
	text = normalizeControlPhrase(text)
	key := intentPhraseKey(text)
	if key == "" {
		return Resolved{}, false
	}
	if definition, exists := registry.exact[key]; exists {
		return Resolved{Definition: cloneIntentDefinition(definition)}, true
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
		return Resolved{Definition: cloneIntentDefinition(candidate.definition), Argument: argument}, true
	}
	return Resolved{}, false
}

func intentPhraseKey(value string) string {
	return strings.ToLower(normalizeControlPhrase(value))
}

func normalizeControlPhrase(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	return strings.TrimSpace(strings.TrimRight(value, "？?。！!"))
}

func (registry *Registry) Definition(id ID) (Definition, bool) {
	if registry == nil {
		return Definition{}, false
	}
	definition, exists := registry.byID[id]
	return cloneIntentDefinition(definition), exists
}

func cloneIntentDefinition(source Definition) Definition {
	copy := source
	copy.ExactPhrases = append([]string(nil), source.ExactPhrases...)
	copy.ArgumentPrefixes = append([]string(nil), source.ArgumentPrefixes...)
	return copy
}

func DefaultRegistry() (*Registry, error) {
	return NewRegistry(DefaultDefinitions())
}

func DefaultDefinitions() []Definition {
	return []Definition{
		{ID: IntentMenu, Domain: DomainSystem, ExactPhrases: []string{"菜单", "打开菜单", "功能", "操作", "功能菜单", "打开功能"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_menu"},
		{ID: IntentGuide, Domain: DomainSystem, ExactPhrases: []string{"帮助", "怎么用", "使用说明"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_guide"},
		{ID: IntentRuntime, Domain: DomainSystem, ExactPhrases: []string{"运行中心", "运行信息", "系统信息", "服务信息", "Codex 信息", "Codex信息"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_runtime"},
		{ID: IntentNoReplyDiagnostic, Domain: DomainSystem, ExactPhrases: []string{"为什么没回复", "怎么没回复", "为什么没反应", "怎么没反应", "没响应", "没有响应", "没回复"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "diagnose_no_reply"},
		{ID: IntentCancel, Domain: DomainQueue, ExactPhrases: []string{"取消", "取消当前执行", "停止", "停止当前执行", "停下", "停一下"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "cancel_queue_execution"},
		{ID: IntentTaskStatus, Domain: DomainQueue, ExactPhrases: []string{"状态", "查看状态", "执行状态", "进度", "执行进度", "查看进度", "进度怎么样", "现在怎么样了", "怎么样了"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_queue_status"},
		{ID: IntentTaskCenter, Domain: DomainQueue, ExactPhrases: []string{"请求队列", "排队请求", "执行记录"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_queue"},
		{ID: IntentTaskContinue, Domain: DomainQueue, ExactPhrases: []string{"继续处理", "继续最近结果", "继续这个结果"}, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "continue_queue_result_thread"},
		{ID: IntentTaskRerun, Domain: DomainQueue, ExactPhrases: []string{"再次执行最近请求", "重试最近请求", "再执行一次"}, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "rerun_queue_request"},
		{ID: IntentTaskRerunNew, Domain: DomainQueue, ExactPhrases: []string{"在新线程继续", "在新线程再次执行"}, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "rerun_queue_request_in_new_thread"},
		{ID: IntentQueuePause, Domain: DomainQueue, ExactPhrases: []string{"暂停队列", "暂停请求队列", "停止处理请求"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "pause_queue"},
		{ID: IntentQueueResume, Domain: DomainQueue, ExactPhrases: []string{"继续队列", "恢复队列", "继续请求队列"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "resume_queue"},
		{ID: IntentQueueClear, Domain: DomainQueue, ExactPhrases: []string{"清空队列", "删除排队请求"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "request_clear_queue"},
		{ID: IntentProjectCenter, Domain: DomainProject, ExactPhrases: []string{"项目", "项目中心", "项目列表", "查看项目", "当前项目"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_project_center"},
		{ID: IntentProjectSelect, Domain: DomainProject, ArgumentPrefixes: []string{"切换项目", "切到项目", "进入项目"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "select_project"},
		{ID: IntentSessionCenter, Domain: DomainSession, ExactPhrases: []string{"线程", "线程中心"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_thread_center"},
		{ID: IntentSessionSelect, Domain: DomainSession, ExactPhrases: []string{"线程列表", "选择线程"}, ArgumentPrefixes: []string{"切换线程", "切换到线程"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "select_thread"},
		{ID: IntentSessionSearch, Domain: DomainSession, ArgumentPrefixes: []string{"搜索线程", "查找线程"}, AllowEmptyArgument: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "search_thread"},
		{ID: IntentSessionCurrent, Domain: DomainSession, ExactPhrases: []string{"当前线程", "这个线程", "线程详情"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_current_thread"},
		{ID: IntentThreadRelations, Domain: DomainSession, ExactPhrases: []string{"线程关系图", "线程关系", "分叉关系"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_thread_relations"},
		{ID: IntentSessionRestore, Domain: DomainSession, ExactPhrases: []string{"已归档线程"}, ArgumentPrefixes: []string{"恢复线程", "找回线程"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "restore_thread"},
		{ID: IntentSessionNew, Domain: DomainSession, ArgumentPrefixes: []string{"新建线程", "创建线程"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "create_thread"},
		{ID: IntentSessionRename, Domain: DomainSession, ArgumentPrefixes: []string{"重命名当前线程", "重命名线程", "当前线程改名"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "rename_thread"},
		{ID: IntentSessionArchive, Domain: DomainSession, ExactPhrases: []string{"归档当前线程", "归档这个线程", "归档线程"}, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "archive_thread"},
		{ID: IntentThreadFork, Domain: DomainSession, ExactPhrases: []string{"分叉当前线程", "分叉线程"}, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "fork_thread"},
		{ID: IntentThreadPin, Domain: DomainSession, ExactPhrases: []string{"置顶当前线程", "置顶线程"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "pin_thread"},
		{ID: IntentThreadCompact, Domain: DomainSession, ExactPhrases: []string{"压缩上下文", "压缩线程上下文"}, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "compact_thread"},
		{ID: IntentThreadGoal, Domain: DomainSession, ExactPhrases: []string{"设置线程目标", "线程目标"}, ArgumentPrefixes: []string{"设置线程目标为", "设置目标"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "set_thread_goal"},
		{ID: IntentThreadGoalClear, Domain: DomainSession, ExactPhrases: []string{"清除线程目标", "清除目标"}, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "clear_thread_goal"},
		{ID: IntentThreadReview, Domain: DomainSession, ExactPhrases: []string{"审查未提交改动", "代码审查"}, MutatesState: true, RequiresReceipt: true, AllowDuringDrain: true, AuditEvent: "review_thread"},
		{ID: IntentThreadDelete, Domain: DomainSession, ExactPhrases: []string{"永久删除线程", "删除当前线程"}, AllowDuringDrain: true, AuditEvent: "confirm_delete_thread"},
		{ID: IntentThreadModels, Domain: DomainSession, ExactPhrases: []string{"线程模型", "Codex 模型", "选择模型"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_thread_models"},
		{ID: IntentThreadEffort, Domain: DomainSession, ExactPhrases: []string{"推理强度", "思考强度"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_thread_effort"},
		{ID: IntentTurnSteer, Domain: DomainSession, ArgumentPrefixes: []string{"追加指令", "调整当前轮次", "调整轮次"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "steer_turn"},
		{ID: IntentResponseModes, Domain: DomainPreference, ExactPhrases: []string{"回答方式", "回复方式", "输出方式", "偏好设置", "语音模式"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_response_modes"},
		{ID: IntentResponseVoice, Domain: DomainPreference, ExactPhrases: []string{"开启语音模式", "打开语音模式", "启用语音模式"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "set_response_voice"},
		{ID: IntentResponseAdaptive, Domain: DomainPreference, ExactPhrases: []string{"关闭语音模式", "退出语音模式", "自适应模式"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "set_response_adaptive"},
		{ID: IntentResponseReading, Domain: DomainPreference, ExactPhrases: []string{"开启阅读模式", "打开阅读模式", "阅读模式"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "set_response_reading"},
		{ID: IntentVisualStyle, Domain: DomainPreference, ExactPhrases: []string{"主题风格"}, ArgumentPrefixes: []string{"视觉风格", "卡片风格", "更换风格", "切换风格"}, AllowEmptyArgument: true, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "set_visual_style"},
		{ID: IntentDeliveryBox, Domain: DomainDelivery, ExactPhrases: []string{"交付箱", "交付记录", "交付中心"}, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "open_delivery_box"},
		{ID: IntentVoiceBriefing, Domain: DomainQueue, ExactPhrases: []string{"语音简报", "播放简报", "工作简报", "发语音", "发个语音", "来段语音", "播报一下", "读给我听"}, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, RequiresContextToken: true, AuditEvent: "send_result_voice_briefing"},
		{ID: IntentRemoteLock, Domain: DomainSecurity, ExactPhrases: []string{"远程锁定", "锁定 codex-link-clawbot", "锁定codex-link-clawbot", "锁定服务"}, MutatesState: true, RequiresReceipt: true, AllowDuringTask: true, AllowDuringDrain: true, AuditEvent: "remote_lock"},
	}
}
