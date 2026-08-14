package visual

import (
	"context"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"strings"
	"time"
)

const (
	threadMapCanvasWidth  = 1080
	threadMapCanvasHeight = 1180
	threadMapMaxChildren  = 5
)

type ThreadMapRole string

const (
	ThreadMapParent  ThreadMapRole = "parent"
	ThreadMapCurrent ThreadMapRole = "current"
	ThreadMapChild   ThreadMapRole = "child"
)

type ThreadMapNode struct {
	Code      string
	Role      ThreadMapRole
	RoleLabel string
	Title     string
	Workspace string
	Status    string
	Tone      string
	X         int
	Y         int
	Width     int
	Height    int
}

type ThreadMapEdge struct {
	Path string
	Tone string
}

type ThreadMapAction struct {
	Code  string
	Label string
	Icon  string
}

// ThreadMap 是目标线程的一层原生分叉关系图，不承载对话内容。
type ThreadMap struct {
	Theme             Theme
	Style             presentation.Style
	Workspace         string
	Current           ThreadMapNode
	Parent            *ThreadMapNode
	Children          []ThreadMapNode
	Edges             []ThreadMapEdge
	Actions           []ThreadMapAction
	ParentUnavailable bool
	Truncated         int
	RelationTip       string
	Footer            string
	Height            int
}

func (r *Renderer) RenderThreadMap(ctx context.Context, threadMap ThreadMap) (*Artifact, error) {
	threadMap, err := prepareThreadMap(threadMap, r.currentTime())
	if err != nil {
		return nil, err
	}
	var output strings.Builder
	if err := r.tmpl.ExecuteTemplate(&output, "thread-map", threadMap); err != nil {
		return nil, fmt.Errorf("execute thread map template: %w", err)
	}
	return r.renderArtifactSized(ctx, "thread-map-*", threadMapCanvasWidth, threadMap.Height, []byte(output.String()))
}

func prepareThreadMap(threadMap ThreadMap, now time.Time) (ThreadMap, error) {
	threadMap.Style = presentation.NormalizeStyle(threadMap.Style)
	if threadMap.Theme != ThemeDay && threadMap.Theme != ThemeNight {
		threadMap.Theme = ThemeForTime(now)
	}
	threadMap.Workspace = strings.TrimSpace(threadMap.Workspace)
	threadMap.Footer = strings.TrimSpace(threadMap.Footer)
	if threadMap.Workspace == "" || len(threadMap.Children) > threadMapMaxChildren || len(threadMap.Actions) != 2 || threadMap.Truncated < 0 {
		return ThreadMap{}, fmt.Errorf("invalid thread map structure")
	}

	seenCodes := make(map[string]bool, threadMapMaxChildren+3)
	current, err := prepareThreadMapNode(threadMap.Current, ThreadMapCurrent, seenCodes)
	if err != nil {
		return ThreadMap{}, err
	}
	current.X, current.Y, current.Width, current.Height = 320, 374, 440, 190
	threadMap.Current = current

	if threadMap.Parent != nil {
		parent, err := prepareThreadMapNode(*threadMap.Parent, ThreadMapParent, seenCodes)
		if err != nil {
			return ThreadMap{}, err
		}
		parent.X, parent.Y, parent.Width, parent.Height = 360, 188, 360, 150
		threadMap.Parent = &parent
		threadMap.Edges = append(threadMap.Edges, ThreadMapEdge{
			Path: "M540 338 C540 350 540 360 540 374", Tone: "ancestry",
		})
	}

	positions := [threadMapMaxChildren][2]int{
		{64, 696}, {396, 696}, {728, 696}, {230, 868}, {562, 868},
	}
	for index := range threadMap.Children {
		child, err := prepareThreadMapNode(threadMap.Children[index], ThreadMapChild, seenCodes)
		if err != nil {
			return ThreadMap{}, err
		}
		child.X, child.Y, child.Width, child.Height = positions[index][0], positions[index][1], 288, 150
		threadMap.Children[index] = child
		centerX := child.X + child.Width/2
		threadMap.Edges = append(threadMap.Edges, ThreadMapEdge{
			Path: fmt.Sprintf("M540 564 C540 632 %d 626 %d %d", centerX, centerX, child.Y), Tone: "descendant",
		})
	}

	for index := range threadMap.Actions {
		action := &threadMap.Actions[index]
		action.Code = strings.TrimSpace(action.Code)
		action.Label = strings.TrimSpace(action.Label)
		action.Icon = strings.TrimSpace(action.Icon)
		if !validDirectoryCode(action.Code) || action.Label == "" || action.Icon == "" || seenCodes[action.Code] {
			return ThreadMap{}, fmt.Errorf("invalid thread map action")
		}
		seenCodes[action.Code] = true
	}

	switch {
	case threadMap.ParentUnavailable && len(threadMap.Children) == 0:
		threadMap.RelationTip = "父线程不可用 · 当前分支尚无直接子线程"
	case threadMap.ParentUnavailable:
		threadMap.RelationTip = "父线程不可用 · 仅显示可信直接子线程"
	case threadMap.Parent == nil && len(threadMap.Children) == 0:
		threadMap.RelationTip = "当前目标尚未产生可见分叉"
	case threadMap.Parent == nil:
		threadMap.RelationTip = "原生根线程"
	case len(threadMap.Children) == 0:
		threadMap.RelationTip = "当前分支尚无直接子线程"
	default:
		threadMap.RelationTip = "仅显示一层直接关系"
	}
	threadMap.Height = threadMapCanvasHeight
	return threadMap, nil
}

func prepareThreadMapNode(node ThreadMapNode, expected ThreadMapRole, seenCodes map[string]bool) (ThreadMapNode, error) {
	node.Code = strings.TrimSpace(node.Code)
	node.Title = strings.TrimSpace(node.Title)
	node.Workspace = strings.TrimSpace(node.Workspace)
	node.Status = strings.TrimSpace(node.Status)
	node.Role = expected
	switch expected {
	case ThreadMapParent:
		node.RoleLabel = "父线程"
	case ThreadMapCurrent:
		node.RoleLabel = "当前目标"
	case ThreadMapChild:
		node.RoleLabel = "直接子线程"
	default:
		return ThreadMapNode{}, fmt.Errorf("invalid thread map role")
	}
	if node.Title == "" || node.Workspace == "" || node.Status == "" {
		return ThreadMapNode{}, fmt.Errorf("invalid thread map node")
	}
	if expected == ThreadMapCurrent {
		if node.Code != "" {
			return ThreadMapNode{}, fmt.Errorf("current thread map node cannot be selectable")
		}
	} else if !validDirectoryCode(node.Code) || seenCodes[node.Code] {
		return ThreadMapNode{}, fmt.Errorf("invalid thread map node code")
	} else {
		seenCodes[node.Code] = true
	}
	node.Tone = workbenchStatusTone(node.Status)
	return node, nil
}
