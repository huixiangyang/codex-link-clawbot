package workflow

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/huixiangyang/weclaw/internal/statefile"
)

const (
	stateVersion     = 1
	stateMaxBytes    = 2 << 20
	maxOwners        = 64
	maxTotalWorkflow = 128
	maxNameRunes     = 32
	maxPromptRunes   = 8000
	maxValueRunes    = 1000
	maxRenderedRunes = 12000
	maxRunReceipts   = 512
	runTTL           = 10 * time.Minute
	runReceiptTTL    = 24 * time.Hour
)

var (
	workflowIDPattern = regexp.MustCompile(`^workflow-[a-f0-9]{32}$`)
	projectIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	slotKeyPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	placeholderRE     = regexp.MustCompile(`\{\{([a-z][a-z0-9_]{0,31})\}\}`)
)

type Store struct {
	mu       sync.RWMutex
	path     string
	projects map[string]bool
	state    stateFile
	now      func() time.Time
	fault    func(statefile.FaultPoint) error
}

func NewStore(path string, projectIDs []string) (*Store, error) {
	return newStore(path, projectIDs, time.Now)
}

func newStore(path string, projectIDs []string, now func() time.Time) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve workflow state path: %w", err)
		}
		path = filepath.Join(home, ".weclaw", "workflows.json")
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("workflow state path must be absolute")
	}
	projects := make(map[string]bool, len(projectIDs))
	for _, projectID := range projectIDs {
		if !projectIDPattern.MatchString(projectID) || projects[projectID] {
			return nil, fmt.Errorf("workflow project allowlist is invalid")
		}
		projects[projectID] = true
	}
	if len(projects) == 0 {
		return nil, fmt.Errorf("workflow project allowlist is empty")
	}
	if now == nil {
		now = time.Now
	}
	store := &Store{
		path: path, projects: projects, now: now,
		state: stateFile{
			Version: stateVersion, Owners: make(map[string]ownerState),
			Runs: make(map[string]pendingRun), Receipts: make(map[string]runReceipt),
		},
	}
	found, err := statefile.ReadJSON(store.path, &store.state, statefile.Options{
		MaxBytes: stateMaxBytes,
		Validate: func() error { return store.validateState(store.state) },
	})
	if err != nil {
		return nil, fmt.Errorf("load workflows: %w", err)
	}
	if !found {
		if err := store.saveLocked(store.state); err != nil {
			return nil, fmt.Errorf("initialize workflows: %w", err)
		}
	}
	return store, nil
}

func (store *Store) Create(input CreateInput) (Definition, error) {
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.Name = strings.TrimSpace(input.Name)
	input.PromptTemplate = strings.TrimSpace(input.PromptTemplate)
	if err := store.validateOwnerProject(input.OwnerID, input.ProjectID); err != nil {
		return Definition{}, err
	}
	id, err := newWorkflowID()
	if err != nil {
		return Definition{}, fmt.Errorf("create workflow id: %w", err)
	}
	now := normalizedUnix(store.now())
	definition := Definition{
		ID: id, ProjectID: input.ProjectID, Name: input.Name,
		PromptTemplate: input.PromptTemplate, Slots: cloneSlots(input.Slots),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := validateDefinition(definition); err != nil {
		return Definition{}, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	next := cloneState(store.state)
	owner := next.Owners[input.OwnerID]
	if owner.Projects == nil {
		owner.Projects = make(map[string][]Definition)
	}
	definitions := owner.Projects[input.ProjectID]
	if len(definitions) >= MaxWorkflowsPerProject {
		return Definition{}, fmt.Errorf("project workflow capacity is exhausted")
	}
	for _, existing := range definitions {
		if existing.Name == definition.Name {
			return Definition{}, fmt.Errorf("workflow name already exists in project")
		}
	}
	owner.Projects[input.ProjectID] = append(definitions, definition)
	next.Owners[input.OwnerID] = owner
	if err := store.saveLocked(next); err != nil {
		return Definition{}, err
	}
	store.state = next
	return cloneDefinition(definition), nil
}

func (store *Store) Update(ownerID, projectID, workflowID string, input UpdateInput) (Definition, error) {
	ownerID, projectID, workflowID = strings.TrimSpace(ownerID), strings.TrimSpace(projectID), strings.TrimSpace(workflowID)
	input.Name = strings.TrimSpace(input.Name)
	input.PromptTemplate = strings.TrimSpace(input.PromptTemplate)
	if err := store.validateOwnerProject(ownerID, projectID); err != nil {
		return Definition{}, err
	}
	if !workflowIDPattern.MatchString(workflowID) {
		return Definition{}, fmt.Errorf("workflow id is invalid")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	next := cloneState(store.state)
	owner, exists := next.Owners[ownerID]
	if !exists {
		return Definition{}, fmt.Errorf("workflow not found")
	}
	definitions := owner.Projects[projectID]
	index := definitionIndex(definitions, workflowID)
	if index < 0 {
		return Definition{}, fmt.Errorf("workflow not found")
	}
	for candidateIndex, existing := range definitions {
		if candidateIndex != index && existing.Name == input.Name {
			return Definition{}, fmt.Errorf("workflow name already exists in project")
		}
	}
	updated := definitions[index]
	updated.Name = input.Name
	updated.PromptTemplate = input.PromptTemplate
	updated.Slots = cloneSlots(input.Slots)
	updated.UpdatedAt = normalizedUnix(store.now())
	if updated.UpdatedAt < updated.CreatedAt {
		updated.UpdatedAt = updated.CreatedAt
	}
	if err := validateDefinition(updated); err != nil {
		return Definition{}, err
	}
	definitions[index] = updated
	owner.Projects[projectID] = definitions
	next.Owners[ownerID] = owner
	if err := store.saveLocked(next); err != nil {
		return Definition{}, err
	}
	store.state = next
	return cloneDefinition(updated), nil
}

func (store *Store) Delete(ownerID, projectID, workflowID string) error {
	ownerID, projectID, workflowID = strings.TrimSpace(ownerID), strings.TrimSpace(projectID), strings.TrimSpace(workflowID)
	if err := store.validateOwnerProject(ownerID, projectID); err != nil {
		return err
	}
	if !workflowIDPattern.MatchString(workflowID) {
		return fmt.Errorf("workflow id is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	next := cloneState(store.state)
	owner, exists := next.Owners[ownerID]
	if !exists {
		return fmt.Errorf("workflow not found")
	}
	definitions := owner.Projects[projectID]
	index := definitionIndex(definitions, workflowID)
	if index < 0 {
		return fmt.Errorf("workflow not found")
	}
	owner.Projects[projectID] = append(definitions[:index], definitions[index+1:]...)
	if len(owner.Projects[projectID]) == 0 {
		delete(owner.Projects, projectID)
	}
	if len(owner.Projects) == 0 {
		delete(next.Owners, ownerID)
	} else {
		next.Owners[ownerID] = owner
	}
	if err := store.saveLocked(next); err != nil {
		return err
	}
	store.state = next
	return nil
}

func (store *Store) List(ownerID, projectID string) []Definition {
	ownerID, projectID = strings.TrimSpace(ownerID), strings.TrimSpace(projectID)
	store.mu.RLock()
	definitions := store.state.Owners[ownerID].Projects[projectID]
	result := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, cloneDefinition(definition))
	}
	store.mu.RUnlock()
	sort.Slice(result, func(left, right int) bool {
		if result[left].UpdatedAt == result[right].UpdatedAt {
			return result[left].ID < result[right].ID
		}
		return result[left].UpdatedAt > result[right].UpdatedAt
	})
	return result
}

func (store *Store) Find(ownerID, projectID, workflowID string) (Definition, bool) {
	ownerID, projectID, workflowID = strings.TrimSpace(ownerID), strings.TrimSpace(projectID), strings.TrimSpace(workflowID)
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, definition := range store.state.Owners[ownerID].Projects[projectID] {
		if definition.ID == workflowID {
			return cloneDefinition(definition), true
		}
	}
	return Definition{}, false
}

// Import 只用于离线迁移。已存在的同 ID 定义必须完全相同，保证重复迁移不会覆盖用户修改。
func (store *Store) Import(ownerID string, definition Definition) (bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	if err := store.validateOwnerProject(ownerID, definition.ProjectID); err != nil {
		return false, err
	}
	definition.Name = strings.TrimSpace(definition.Name)
	definition.PromptTemplate = strings.TrimSpace(definition.PromptTemplate)
	if err := validateDefinition(definition); err != nil {
		return false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	next := cloneState(store.state)
	owner := next.Owners[ownerID]
	if owner.Projects == nil {
		owner.Projects = make(map[string][]Definition)
	}
	definitions := owner.Projects[definition.ProjectID]
	for _, existing := range definitions {
		if existing.ID == definition.ID {
			if equalDefinitionCore(existing, definition) {
				return false, nil
			}
			return false, fmt.Errorf("imported workflow conflicts with existing definition")
		}
		if existing.Name == definition.Name {
			return false, fmt.Errorf("imported workflow name conflicts with existing definition")
		}
	}
	if len(definitions) >= MaxWorkflowsPerProject {
		return false, fmt.Errorf("project workflow capacity is exhausted")
	}
	owner.Projects[definition.ProjectID] = append(definitions, cloneDefinition(definition))
	next.Owners[ownerID] = owner
	if err := store.saveLocked(next); err != nil {
		return false, err
	}
	store.state = next
	return true, nil
}

func StableImportID(projectID, sourceID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(projectID) + "\x00" + strings.TrimSpace(sourceID)))
	return "workflow-" + hex.EncodeToString(digest[:16])
}

func Render(definition Definition, values map[string]string) (string, error) {
	if err := validateDefinition(definition); err != nil {
		return "", err
	}
	if len(values) != len(definition.Slots) {
		return "", fmt.Errorf("workflow values do not match declared slots")
	}
	result := definition.PromptTemplate
	for _, slot := range definition.Slots {
		value, exists := values[slot.Key]
		value = strings.TrimSpace(value)
		if !exists || value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || len([]rune(value)) > maxValueRunes {
			return "", fmt.Errorf("workflow value for %s is invalid", slot.Key)
		}
		result = strings.ReplaceAll(result, "{{"+slot.Key+"}}", value)
	}
	if strings.Contains(result, "{{") || strings.Contains(result, "}}") || len([]rune(result)) > maxRenderedRunes {
		return "", fmt.Errorf("rendered workflow prompt is invalid")
	}
	return result, nil
}

func (store *Store) validateOwnerProject(ownerID, projectID string) error {
	if ownerID == "" || len(ownerID) > 512 || strings.ContainsAny(ownerID, "\r\n\x00") {
		return fmt.Errorf("workflow owner is invalid")
	}
	if !store.projects[projectID] {
		return fmt.Errorf("workflow project is not configured")
	}
	return nil
}

func (store *Store) saveLocked(state stateFile) error {
	return statefile.WriteJSON(store.path, state, statefile.Options{
		MaxBytes: stateMaxBytes,
		Validate: func() error { return store.validateState(state) },
		Fault:    store.fault,
	})
}

func (store *Store) validateState(state stateFile) error {
	if state.Version != stateVersion || state.Owners == nil || state.Runs == nil || state.Receipts == nil ||
		len(state.Owners) > maxOwners || len(state.Runs) > maxOwners || len(state.Receipts) > maxRunReceipts {
		return fmt.Errorf("invalid workflow state schema")
	}
	total := 0
	for ownerID, owner := range state.Owners {
		if owner.Projects == nil || ownerID == "" || len(ownerID) > 512 || strings.ContainsAny(ownerID, "\r\n\x00") {
			return fmt.Errorf("invalid workflow owner state")
		}
		for projectID, definitions := range owner.Projects {
			if !store.projects[projectID] || len(definitions) == 0 || len(definitions) > MaxWorkflowsPerProject {
				return fmt.Errorf("invalid workflow project state")
			}
			ids := make(map[string]bool, len(definitions))
			names := make(map[string]bool, len(definitions))
			for _, definition := range definitions {
				if definition.ProjectID != projectID || ids[definition.ID] || names[definition.Name] {
					return fmt.Errorf("duplicated or misplaced workflow")
				}
				if err := validateDefinition(definition); err != nil {
					return err
				}
				ids[definition.ID], names[definition.Name] = true, true
				total++
			}
		}
	}
	if total > maxTotalWorkflow {
		return fmt.Errorf("workflow state capacity is exhausted")
	}
	for ownerID, run := range state.Runs {
		if ownerID == "" || len(ownerID) > 512 || strings.ContainsAny(ownerID, "\r\n\x00") || !store.projects[run.ProjectID] ||
			!workflowIDPattern.MatchString(run.WorkflowID) || run.Values == nil || len(run.Values) > MaxSlots ||
			run.StartedAt <= 0 || run.ExpiresAt <= run.StartedAt || run.ExpiresAt-run.StartedAt > int64(runTTL/time.Second)+1 {
			return fmt.Errorf("invalid pending workflow run")
		}
		definition, exists := findDefinitionState(state, ownerID, run.ProjectID, run.WorkflowID)
		if !exists || len(run.Values) >= len(definition.Slots) {
			return fmt.Errorf("pending workflow definition is unavailable")
		}
		for index, slot := range definition.Slots {
			value, exists := run.Values[slot.Key]
			if index < len(run.Values) {
				if !exists || !validRunValue(value) {
					return fmt.Errorf("invalid pending workflow value")
				}
			} else if exists {
				return fmt.Errorf("pending workflow values are out of order")
			}
		}
	}
	for key, receipt := range state.Receipts {
		if len(key) != 64 || !isLowerHex(key) || receipt.CreatedAt <= 0 || receipt.ExpiresAt <= receipt.CreatedAt ||
			receipt.ExpiresAt-receipt.CreatedAt > int64(runReceiptTTL/time.Second)+1 {
			return fmt.Errorf("invalid workflow run receipt")
		}
	}
	return nil
}

func validateDefinition(definition Definition) error {
	if !workflowIDPattern.MatchString(definition.ID) || !projectIDPattern.MatchString(definition.ProjectID) {
		return fmt.Errorf("workflow identity is invalid")
	}
	if definition.Name == "" || !utf8.ValidString(definition.Name) || strings.ContainsAny(definition.Name, "\r\n\x00") || len([]rune(definition.Name)) > maxNameRunes {
		return fmt.Errorf("workflow name is invalid")
	}
	if definition.PromptTemplate == "" || !utf8.ValidString(definition.PromptTemplate) || strings.ContainsRune(definition.PromptTemplate, '\x00') || len([]rune(definition.PromptTemplate)) > maxPromptRunes {
		return fmt.Errorf("workflow prompt template is invalid")
	}
	if definition.Slots == nil || len(definition.Slots) > MaxSlots || definition.CreatedAt <= 0 || definition.UpdatedAt < definition.CreatedAt {
		return fmt.Errorf("workflow metadata is invalid")
	}
	declared := make(map[string]bool, len(definition.Slots))
	for _, slot := range definition.Slots {
		if !slotKeyPattern.MatchString(slot.Key) || slot.Label == "" || !utf8.ValidString(slot.Label) || strings.ContainsAny(slot.Label, "\r\n\x00") || len([]rune(slot.Label)) > 24 || declared[slot.Key] {
			return fmt.Errorf("workflow slot is invalid")
		}
		declared[slot.Key] = true
	}
	used := make(map[string]bool)
	cleaned := placeholderRE.ReplaceAllStringFunc(definition.PromptTemplate, func(match string) string {
		parts := placeholderRE.FindStringSubmatch(match)
		if len(parts) == 2 {
			used[parts[1]] = true
		}
		return ""
	})
	if strings.Contains(cleaned, "{{") || strings.Contains(cleaned, "}}") || len(used) != len(declared) {
		return fmt.Errorf("workflow placeholders do not match declared slots")
	}
	for key := range used {
		if !declared[key] {
			return fmt.Errorf("workflow placeholder is undeclared")
		}
	}
	return nil
}

func definitionIndex(definitions []Definition, workflowID string) int {
	for index, definition := range definitions {
		if definition.ID == workflowID {
			return index
		}
	}
	return -1
}

func equalDefinitionCore(left, right Definition) bool {
	if left.ID != right.ID || left.ProjectID != right.ProjectID || left.Name != right.Name || left.PromptTemplate != right.PromptTemplate || len(left.Slots) != len(right.Slots) {
		return false
	}
	for index := range left.Slots {
		if left.Slots[index] != right.Slots[index] {
			return false
		}
	}
	return true
}

func newWorkflowID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "workflow-" + hex.EncodeToString(data), nil
}

func findDefinitionState(state stateFile, ownerID, projectID, workflowID string) (Definition, bool) {
	for _, definition := range state.Owners[ownerID].Projects[projectID] {
		if definition.ID == workflowID {
			return definition, true
		}
	}
	return Definition{}, false
}

func validRunValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') && len([]rune(value)) <= maxValueRunes
}

func isLowerHex(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
