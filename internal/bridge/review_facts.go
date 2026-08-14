package bridge

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
)

const maxReviewFactCommandBytes = 8 << 20

type workspaceChangeFacts struct {
	Available    bool
	Files        int
	New          int
	Modified     int
	Deleted      int
	Renamed      int
	Conflicted   int
	AddedLines   int64
	DeletedLines int64
	BinaryFiles  int
	HasLineStats bool
}

type mobileReviewEvidence struct {
	Changes      workspaceChangeFacts
	Verification codex.ThreadVerificationFacts
	Deliveries   delivery.ThreadSummary
}

type mobileReviewFact struct {
	Label string
	Value string
}

// inspectWorkspaceChangeFacts 只统计受信任工作空间，不读取或返回文件路径与差异正文。
func inspectWorkspaceChangeFacts(ctx context.Context, root string) workspaceChangeFacts {
	factContext, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	statusOutput, err := runReviewFactCommand(factContext, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--")
	if err != nil {
		return workspaceChangeFacts{}
	}
	facts := parseWorkspaceStatus(statusOutput)
	facts.Available = true
	numstatOutput, err := runReviewFactCommand(factContext, root, "diff", "--numstat", "-z", "HEAD", "--")
	if err == nil {
		facts.AddedLines, facts.DeletedLines, facts.BinaryFiles = parseWorkspaceNumstat(numstatOutput)
		facts.HasLineStats = true
	}
	return facts
}

func runReviewFactCommand(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	commandArguments := append([]string{"-C", root}, arguments...)
	command := exec.CommandContext(ctx, "git", commandArguments...)
	output, err := command.Output()
	if err != nil || len(output) > maxReviewFactCommandBytes {
		return nil, errReviewFactsUnavailable
	}
	return output, nil
}

var errReviewFactsUnavailable = &reviewFactError{}

type reviewFactError struct{}

func (*reviewFactError) Error() string { return "review facts unavailable" }

func parseWorkspaceStatus(output []byte) workspaceChangeFacts {
	var facts workspaceChangeFacts
	records := bytes.Split(output, []byte{0})
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) < 3 {
			continue
		}
		status := string(record[:2])
		if status == "!!" {
			continue
		}
		facts.Files++
		switch {
		case strings.ContainsAny(status, "U") || status == "AA" || status == "DD":
			facts.Conflicted++
		case status == "??" || strings.ContainsRune(status, 'A'):
			facts.New++
		case strings.ContainsAny(status, "RC"):
			facts.Renamed++
			// -z 模式下重命名和复制记录会追加第二个路径字段。
			index++
		case strings.ContainsRune(status, 'D'):
			facts.Deleted++
		default:
			facts.Modified++
		}
	}
	return facts
}

func parseWorkspaceNumstat(output []byte) (int64, int64, int) {
	var added int64
	var deleted int64
	var binary int
	for _, record := range bytes.Split(output, []byte{0}) {
		fields := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(fields) < 2 {
			continue
		}
		if string(fields[0]) == "-" || string(fields[1]) == "-" {
			binary++
			continue
		}
		addedValue, addErr := strconv.ParseInt(string(fields[0]), 10, 64)
		deletedValue, deleteErr := strconv.ParseInt(string(fields[1]), 10, 64)
		if addErr == nil && deleteErr == nil {
			added += addedValue
			deleted += deletedValue
		}
	}
	return added, deleted, binary
}

func mobileReviewFacts(evidence mobileReviewEvidence) []mobileReviewFact {
	facts := make([]mobileReviewFact, 0, 3)
	if evidence.Changes.Available {
		value := formatReviewChangeFact(evidence.Changes)
		facts = append(facts, mobileReviewFact{Label: "变更事实", Value: value})
	}
	if evidence.Verification.Available {
		facts = append(facts, mobileReviewFact{Label: "验证事实", Value: formatReviewVerificationFact(evidence.Verification)})
	}
	if evidence.Deliveries.Available {
		facts = append(facts, mobileReviewFact{Label: "交付事实", Value: formatReviewDeliveryFact(evidence.Deliveries)})
	}
	return facts
}

func formatReviewChangeFact(facts workspaceChangeFacts) string {
	value := strconv.Itoa(facts.Files) + " 个文件"
	if facts.HasLineStats {
		value += " · +" + strconv.FormatInt(facts.AddedLines, 10) + " / −" + strconv.FormatInt(facts.DeletedLines, 10)
	}
	if facts.Conflicted > 0 {
		value += " · " + strconv.Itoa(facts.Conflicted) + " 个冲突"
	}
	return value
}

func formatReviewVerificationFact(facts codex.ThreadVerificationFacts) string {
	if facts.Total == 0 {
		return "最近线程记录未识别到测试、检查或构建"
	}
	value := strconv.Itoa(facts.Total) + " 项 · " + strconv.Itoa(facts.Passed) + " 通过"
	if facts.Failed > 0 {
		value += " · " + strconv.Itoa(facts.Failed) + " 失败"
	}
	if facts.Incomplete > 0 {
		value += " · " + strconv.Itoa(facts.Incomplete) + " 未完成"
	}
	labels := make([]string, 0, len(facts.Kinds))
	for _, kind := range facts.Kinds {
		switch kind {
		case codex.VerificationTest:
			labels = append(labels, "测试")
		case codex.VerificationCheck:
			labels = append(labels, "检查")
		case codex.VerificationBuild:
			labels = append(labels, "构建")
		}
	}
	if len(labels) > 0 {
		value += " · " + strings.Join(labels, "/")
	}
	return value
}

func formatReviewDeliveryFact(summary delivery.ThreadSummary) string {
	if summary.Total == 0 {
		return "当前线程暂无文件交付"
	}
	value := strconv.Itoa(summary.Resendable) + " 项可再次发送"
	if summary.Unavailable > 0 {
		value += " · " + strconv.Itoa(summary.Unavailable) + " 项已失效"
	}
	return value
}
