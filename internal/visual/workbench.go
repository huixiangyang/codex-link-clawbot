package visual

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	workbenchCanvasWidth   = 1080
	workbenchCanvasHeight  = 780
	workbenchMaxThreads    = 4
	workbenchActionCount   = 5
	workbenchCommandGroups = 3
	workbenchCommandCount  = 17
)

type WorkbenchTarget struct {
	Title     string
	Workspace string
	Status    string
	Time      string
	Available bool
}

type WorkbenchThread struct {
	Code      string
	Title     string
	Workspace string
	Status    string
	Time      string
	Current   bool
	Wechat    string
	Tone      string
}

type WorkbenchAction struct {
	Code  string
	Label string
	Meta  string
	Icon  string
}

type WorkbenchCommand struct {
	Label   string
	Command string
	Tone    string
}

type WorkbenchCommandGroup struct {
	Title    string
	Commands []WorkbenchCommand
}

// Workbench 是全局首页的专用视图，线程事实与控制动作保持结构隔离。
type Workbench struct {
	Theme    Theme
	Style    Style
	Title    string
	Subtitle string
	State    string
	Facts    []Fact
	Target   WorkbenchTarget
	Threads  []WorkbenchThread
	Actions  []WorkbenchAction
	Commands []WorkbenchCommandGroup
	Footer   string
	Height   int
}

func (r *Renderer) RenderWorkbench(ctx context.Context, workbench Workbench) (*Artifact, error) {
	workbench, err := prepareWorkbench(workbench, r.currentTime())
	if err != nil {
		return nil, err
	}
	var output strings.Builder
	if err := r.tmpl.ExecuteTemplate(&output, "workbench", workbench); err != nil {
		return nil, fmt.Errorf("execute workbench template: %w", err)
	}
	return r.renderArtifactSized(ctx, "workbench-*", workbenchCanvasWidth, workbench.Height, []byte(output.String()))
}

func prepareWorkbench(workbench Workbench, now time.Time) (Workbench, error) {
	workbench.Style = NormalizeStyle(workbench.Style)
	if workbench.Theme != ThemeDay && workbench.Theme != ThemeNight {
		if hour := now.Hour(); hour >= 7 && hour < 19 {
			workbench.Theme = ThemeDay
		} else {
			workbench.Theme = ThemeNight
		}
	}
	workbench.Title = strings.TrimSpace(workbench.Title)
	workbench.Subtitle = strings.TrimSpace(workbench.Subtitle)
	workbench.State = strings.TrimSpace(workbench.State)
	workbench.Footer = strings.TrimSpace(workbench.Footer)
	if workbench.Title == "" || len(workbench.Threads) > workbenchMaxThreads || len(workbench.Actions) != workbenchActionCount {
		return Workbench{}, fmt.Errorf("invalid workbench structure")
	}
	if len(workbench.Facts) == 0 || len(workbench.Facts) > 4 {
		return Workbench{}, fmt.Errorf("invalid workbench facts")
	}
	for index := range workbench.Facts {
		workbench.Facts[index].Label = strings.TrimSpace(workbench.Facts[index].Label)
		workbench.Facts[index].Value = strings.TrimSpace(workbench.Facts[index].Value)
		if workbench.Facts[index].Label == "" || workbench.Facts[index].Value == "" {
			return Workbench{}, fmt.Errorf("invalid workbench fact")
		}
	}
	workbench.Target.Title = strings.TrimSpace(workbench.Target.Title)
	workbench.Target.Workspace = strings.TrimSpace(workbench.Target.Workspace)
	workbench.Target.Status = strings.TrimSpace(workbench.Target.Status)
	workbench.Target.Time = strings.TrimSpace(workbench.Target.Time)
	if workbench.Target.Title == "" {
		return Workbench{}, fmt.Errorf("workbench target is required")
	}

	seen := make(map[string]bool, workbenchMaxThreads+workbenchActionCount)
	for index := range workbench.Threads {
		thread := &workbench.Threads[index]
		thread.Code = strings.TrimSpace(thread.Code)
		thread.Title = strings.TrimSpace(thread.Title)
		thread.Workspace = strings.TrimSpace(thread.Workspace)
		thread.Status = strings.TrimSpace(thread.Status)
		thread.Time = strings.TrimSpace(thread.Time)
		thread.Wechat = strings.TrimSpace(thread.Wechat)
		thread.Tone = workbenchStatusTone(thread.Status)
		if !validDirectoryCode(thread.Code) || thread.Title == "" || thread.Workspace == "" || thread.Status == "" || seen[thread.Code] {
			return Workbench{}, fmt.Errorf("invalid workbench thread")
		}
		seen[thread.Code] = true
	}
	for index := range workbench.Actions {
		action := &workbench.Actions[index]
		action.Code = strings.TrimSpace(action.Code)
		action.Label = strings.TrimSpace(action.Label)
		action.Meta = strings.TrimSpace(action.Meta)
		action.Icon = strings.TrimSpace(action.Icon)
		if !validDirectoryCode(action.Code) || action.Label == "" || action.Icon == "" || seen[action.Code] {
			return Workbench{}, fmt.Errorf("invalid workbench action")
		}
		seen[action.Code] = true
	}
	if len(workbench.Commands) != workbenchCommandGroups {
		return Workbench{}, fmt.Errorf("invalid workbench command groups")
	}
	commandCount := 0
	seenCommands := make(map[string]bool, workbenchCommandCount)
	for groupIndex := range workbench.Commands {
		group := &workbench.Commands[groupIndex]
		group.Title = strings.TrimSpace(group.Title)
		if group.Title == "" || len(group.Commands) == 0 {
			return Workbench{}, fmt.Errorf("invalid workbench command group")
		}
		for commandIndex := range group.Commands {
			command := &group.Commands[commandIndex]
			command.Label = strings.TrimSpace(command.Label)
			command.Command = strings.TrimSpace(command.Command)
			command.Tone = strings.TrimSpace(command.Tone)
			if command.Label == "" || !strings.HasPrefix(command.Command, "/") || seenCommands[command.Command] || !validWorkbenchCommandTone(command.Tone) {
				return Workbench{}, fmt.Errorf("invalid workbench command")
			}
			seenCommands[command.Command] = true
			commandCount++
		}
	}
	if commandCount != workbenchCommandCount {
		return Workbench{}, fmt.Errorf("invalid workbench command count")
	}
	workbench.Height = workbenchHeight(len(workbench.Threads))
	return workbench, nil
}

func validWorkbenchCommandTone(tone string) bool {
	switch tone {
	case "native", "adapted":
		return true
	default:
		return false
	}
}

// workbenchHeight 使用固定艺术画布；顶部会话带和底部可用命令区不再由数据量改变构图。
func workbenchHeight(_ int) int {
	return workbenchCanvasHeight
}

func workbenchStatusTone(status string) string {
	switch strings.TrimSpace(status) {
	case "运行中", "执行中", "等待确认":
		return "live"
	case "空闲":
		return "idle"
	case "未加载":
		return "offline"
	default:
		return "neutral"
	}
}
