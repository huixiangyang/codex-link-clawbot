package messaging

import (
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/internal/runtimecontrol"
	"github.com/huixiangyang/weclaw/internal/statefile"
	"github.com/huixiangyang/weclaw/internal/taskqueue"
)

const (
	staleWeChatPollAfter = 2 * time.Minute
	stuckSyncBatchAfter  = 30 * time.Second
	recentStateFailure   = 15 * time.Minute
	recentTaskFailure    = 24 * time.Hour
)

type runtimeSnapshotProvider interface {
	Snapshot() runtimecontrol.Snapshot
}

type noReplyDiagnosis struct {
	Conclusion string
	Evidence   []string
	Actions    []string
}

func (h *Handler) buildNoReplyDiagnostic(userID string) string {
	diagnosis := h.diagnoseNoReply(userID)
	lines := []string{"为什么没回复", "", "结论：" + diagnosis.Conclusion}
	if len(diagnosis.Evidence) > 0 {
		lines = append(lines, "", "确定性依据")
		for index, evidence := range diagnosis.Evidence {
			if index == 3 {
				break
			}
			lines = append(lines, fmt.Sprintf("%d. %s", index+1, evidence))
		}
	}
	if len(diagnosis.Actions) > 0 {
		lines = append(lines, "", "建议")
		for index, action := range diagnosis.Actions {
			if index == 3 {
				break
			}
			lines = append(lines, fmt.Sprintf("%d. %s", index+1, action))
		}
	}
	return strings.Join(lines, "\n")
}

func (h *Handler) diagnoseNoReply(userID string) noReplyDiagnosis {
	if h.remoteLock != nil && h.remoteLock.IsLocked(userID) {
		return noReplyDiagnosis{
			Conclusion: "WeClaw 已被远程锁定，消息不会进入 Codex。",
			Evidence:   []string{"当前绑定者处于锁定状态。", "锁定时 WeClaw 请求队列保持暂停。"},
			Actions:    []string{"发送“解锁 解锁码”。", "解锁后发送“继续队列”。"},
		}
	}

	snapshot, hasRuntime := h.runtimeSnapshot()
	if hasRuntime {
		switch snapshot.Status {
		case runtimecontrol.StateDraining:
			return noReplyDiagnosis{
				Conclusion: "WeClaw 正在排空，已接收的请求不会开始执行。",
				Evidence:   []string{fmt.Sprintf("等待 %d 项，执行 %d 项，发送 %d 项。", snapshot.Tasks.Queued, snapshot.Tasks.Running, snapshot.Tasks.Delivering)},
				Actions:    []string{"等待部署或重启完成。", "稍后再次发送“为什么没回复”。"},
			}
		case runtimecontrol.StateStopping:
			return noReplyDiagnosis{Conclusion: "服务正在停止。", Actions: []string{"等待 systemd 完成启动后再试。"}}
		case runtimecontrol.StateStarting:
			return noReplyDiagnosis{Conclusion: "服务仍在启动，Codex 和微信监控尚未全部就绪。", Actions: []string{"等待启动完成。", "在主机执行 weclaw status。"}}
		}
		if !snapshot.Codex.Ready {
			return noReplyDiagnosis{
				Conclusion: "Codex 应用服务未就绪，WeClaw 请求不能启动 Codex 轮次。",
				Evidence:   []string{"运行控制器报告 Codex ready=false。"},
				Actions:    []string{"在主机执行 weclaw status。", "检查 Codex 登录后重启服务。"},
			}
		}
		if snapshot.WeChat.Monitors == 0 || snapshot.WeChat.Healthy < snapshot.WeChat.Monitors {
			return noReplyDiagnosis{
				Conclusion: "微信长轮询监控不健康，消息接收可能中断。",
				Evidence:   []string{fmt.Sprintf("健康监控 %d / %d。", snapshot.WeChat.Healthy, snapshot.WeChat.Monitors)},
				Actions:    []string{"在主机执行 weclaw status。", "持续异常时重新登录微信并重启服务。"},
			}
		}
		if snapshot.WeChat.LastSuccessSecondsAgo < 0 || time.Duration(snapshot.WeChat.LastSuccessSecondsAgo)*time.Second > staleWeChatPollAfter {
			lastSuccess := "本次运行尚无成功轮询"
			if snapshot.WeChat.LastSuccessSecondsAgo >= 0 {
				lastSuccess = formatUptime(time.Duration(snapshot.WeChat.LastSuccessSecondsAgo) * time.Second)
			}
			return noReplyDiagnosis{
				Conclusion: "微信长轮询长时间没有成功。",
				Evidence:   []string{"距最近成功轮询：" + lastSuccess + "。"},
				Actions:    []string{"在主机执行 weclaw status。", "检查网络与微信登录状态。"},
			}
		}
		if snapshot.WeChat.PendingBatches > 0 && time.Duration(snapshot.WeChat.OldestPendingSeconds)*time.Second >= stuckSyncBatchAfter {
			return noReplyDiagnosis{
				Conclusion: "微信消息批次长时间未提交，服务正在保留旧游标等待安全重试。",
				Evidence: []string{
					fmt.Sprintf("待提交批次：%d。", snapshot.WeChat.PendingBatches),
					"最老批次已等待 " + formatUptime(time.Duration(snapshot.WeChat.OldestPendingSeconds)*time.Second) + "。",
				},
				Actions: []string{"不要重复发送同一请求。", "在主机检查持久化错误与服务日志。"},
			}
		}
	}

	if failure, exists := statefile.LastFailure(); exists && time.Since(failure.At) >= 0 && time.Since(failure.At) <= recentStateFailure {
		return noReplyDiagnosis{
			Conclusion: "最近一次持久化失败，消息或 WeClaw 请求可能没有安全落盘。",
			Evidence:   []string{"错误分类：" + stateFailureCategoryText(failure.Category) + "。", "发生于 " + formatUptime(time.Since(failure.At)) + " 前。"},
			Actions:    stateFailureActions(failure.Category),
		}
	}

	if h.tasks == nil {
		return noReplyDiagnosis{Conclusion: "WeClaw 请求队列未初始化。", Actions: []string{"在主机重启 WeClaw。"}}
	}
	status := h.tasks.Status(userID)
	if status.Paused {
		return noReplyDiagnosis{
			Conclusion: "当前绑定者的 WeClaw 请求队列已暂停。",
			Evidence:   []string{fmt.Sprintf("等待请求：%d。", status.Queued)},
			Actions:    []string{"发送“继续队列”。", "发送“请求队列”检查等待项。"},
		}
	}
	tasks := h.tasks.List(userID)
	for _, task := range tasks {
		switch task.State {
		case taskqueue.StateRunning:
			return noReplyDiagnosis{
				Conclusion: "WeClaw 已接收请求，Codex 轮次正在执行。",
				Evidence:   []string{"WeClaw 执行状态：运行中。"},
				Actions:    []string{"发送“执行状态”查看进度。", "不再需要时发送“取消当前执行”。"},
			}
		case taskqueue.StateDelivering:
			return noReplyDiagnosis{
				Conclusion: "Codex 已完成，结果正在发送。",
				Evidence:   []string{"WeClaw 执行状态：发送中。"},
				Actions:    []string{"稍候片刻。", "发送“请求队列”查看投递状态。"},
			}
		}
	}
	if status.Queued > 0 {
		evidence := []string{fmt.Sprintf("当前绑定者有 %d 个等待请求。", status.Queued)}
		if hasRuntime && snapshot.Tasks.Running+snapshot.Tasks.Delivering > 0 {
			evidence = append(evidence, "WeClaw 全局协调器正在处理另一个请求。")
		}
		return noReplyDiagnosis{
			Conclusion: "请求已进入 WeClaw 队列，尚未开始 Codex 轮次。",
			Evidence:   evidence,
			Actions:    []string{"发送“请求队列”查看顺序。", "在执行记录中选择“移到最前”。"},
		}
	}

	for _, task := range tasks {
		if task.State != taskqueue.StateFailed && task.State != taskqueue.StateInterrupted {
			continue
		}
		finishedAt := time.Unix(task.FinishedAt, 0)
		if task.FinishedAt <= 0 || time.Since(finishedAt) < 0 || time.Since(finishedAt) > recentTaskFailure {
			continue
		}
		conclusion, evidence, actions := diagnoseTerminalTask(task)
		return noReplyDiagnosis{Conclusion: conclusion, Evidence: evidence, Actions: actions}
	}

	evidence := []string{"Codex 已就绪。", "WeClaw 请求队列未暂停，当前没有等待或执行请求。"}
	if hasRuntime && snapshot.WeChat.LastSuccessSecondsAgo >= 0 {
		evidence = append(evidence, fmt.Sprintf("微信最近成功轮询在 %s 前。", formatUptime(time.Duration(snapshot.WeChat.LastSuccessSecondsAgo)*time.Second)))
	}
	return noReplyDiagnosis{
		Conclusion: "当前没有发现确定性阻断；仅凭现有状态不能断言上一条消息为何不可见。",
		Evidence:   evidence,
		Actions:    []string{"发送“请求队列”确认是否曾入队。", "仍异常时在主机执行 weclaw status。"},
	}
}

func (h *Handler) runtimeSnapshot() (runtimecontrol.Snapshot, bool) {
	provider, ok := h.lifecycle.(runtimeSnapshotProvider)
	if !ok || provider == nil {
		return runtimecontrol.Snapshot{}, false
	}
	return provider.Snapshot(), true
}

func (h *Handler) isNoReplyDiagnostic(text string) bool {
	resolved, found := h.intents.Resolve(text)
	return found && resolved.Definition.ID == IntentNoReplyDiagnostic
}

func diagnoseTerminalTask(task taskqueue.Task) (string, []string, []string) {
	age := formatUptime(time.Since(time.Unix(task.FinishedAt, 0)))
	switch task.Reason {
	case taskqueue.ReasonDeliveryAmbiguous, taskqueue.ReasonRestartDelivery:
		return "最近请求的发送结果不确定，为避免重复内容不会自动补发。",
			[]string{"最近执行记录在发送阶段中断。", "结束于 " + age + " 前。"},
			[]string{"发送“请求队列”打开最近记录。", "从详情取回冻结文字。"}
	case taskqueue.ReasonDeliveryFailed:
		return "最近请求已生成结果，但微信发送明确失败。",
			[]string{"最近失败发生在发送阶段。", "结束于 " + age + " 前。"},
			[]string{"发送“请求队列”打开最近记录。", "从详情取回冻结文字。"}
	case taskqueue.ReasonCodexFailed:
		return "最近请求启动的 Codex 轮次执行失败。",
			[]string{"请求输入仍在 24 小时恢复窗口内。", "结束于 " + age + " 前。"},
			[]string{"发送“请求队列”打开最近记录。", "确认原因后从详情重试。"}
	case taskqueue.ReasonRestartRunning:
		return "最近请求被服务重启中断，没有自动重复执行 Codex。",
			[]string{"中断发生于执行阶段。", "结束于 " + age + " 前。"},
			[]string{"发送“请求队列”打开最近记录。", "确认后从详情重试。"}
	default:
		return "最近请求已失败或中断。",
			[]string{"执行记录状态：失败或中断。", "结束于 " + age + " 前。"},
			[]string{"发送“请求队列”查看详情。"}
	}
}

func stateFailureCategoryText(category statefile.Category) string {
	switch category {
	case statefile.CategoryCapacity:
		return "容量不足"
	case statefile.CategoryPermission:
		return "权限错误"
	case statefile.CategoryCorrupt:
		return "状态损坏"
	case statefile.CategorySchema:
		return "状态格式不匹配"
	case statefile.CategoryConflict:
		return "状态冲突"
	default:
		return "暂不可用"
	}
}

func stateFailureActions(category statefile.Category) []string {
	switch category {
	case statefile.CategoryCapacity:
		return []string{"在主机释放磁盘或配额空间。", "空间恢复后重新发送原请求。"}
	case statefile.CategoryPermission:
		return []string{"在主机检查 ~/.weclaw 所有者和权限。", "修复后重启服务。"}
	case statefile.CategoryCorrupt, statefile.CategorySchema:
		return []string{"停止服务并检查最近备份清单。", "不要手工覆盖损坏状态。"}
	default:
		return []string{"在主机执行 weclaw status。", "检查存储后再重试。"}
	}
}
