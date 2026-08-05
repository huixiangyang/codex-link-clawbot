package workflow

import "time"

const (
	MaxWorkflowsPerProject = 24
	MaxSlots               = 8
)

// Slot 是工作流执行前需要逐项收集的显式参数，不允许自由脚本或隐式推断。
type Slot struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Definition 保存可重复执行的原始用户意图；不会保存 Codex 回答。
type Definition struct {
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	Name           string `json:"name"`
	PromptTemplate string `json:"prompt_template"`
	Slots          []Slot `json:"slots"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

type CreateInput struct {
	OwnerID        string
	ProjectID      string
	Name           string
	PromptTemplate string
	Slots          []Slot
}

type UpdateInput struct {
	Name           string
	PromptTemplate string
	Slots          []Slot
}

type ownerState struct {
	Projects map[string][]Definition `json:"projects"`
}

type stateFile struct {
	Version int                   `json:"version"`
	Owners  map[string]ownerState `json:"owners"`
}

func cloneDefinition(source Definition) Definition {
	copy := source
	copy.Slots = cloneSlots(source.Slots)
	return copy
}

func cloneSlots(source []Slot) []Slot {
	if source == nil {
		return nil
	}
	result := make([]Slot, len(source))
	copy(result, source)
	return result
}

func cloneState(source stateFile) stateFile {
	result := stateFile{Version: source.Version, Owners: make(map[string]ownerState, len(source.Owners))}
	for ownerID, owner := range source.Owners {
		projects := make(map[string][]Definition, len(owner.Projects))
		for projectID, definitions := range owner.Projects {
			copy := make([]Definition, 0, len(definitions))
			for _, definition := range definitions {
				copy = append(copy, cloneDefinition(definition))
			}
			projects[projectID] = copy
		}
		result.Owners[ownerID] = ownerState{Projects: projects}
	}
	return result
}

func normalizedUnix(now time.Time) int64 {
	value := now.Unix()
	if value <= 0 {
		return 1
	}
	return value
}
