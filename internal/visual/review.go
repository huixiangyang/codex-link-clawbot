package visual

import (
	"context"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"strings"
	"time"
)

type ReviewVerdict string

const (
	ReviewVerdictClear     ReviewVerdict = "clear"
	ReviewVerdictAttention ReviewVerdict = "attention"
	ReviewVerdictAdvisory  ReviewVerdict = "advisory"
)

type ReviewFinding struct {
	Priority string
	Title    string
	Detail   string
	Location string
}

// Review 是移动端代码审查的专用结构，只保留手机上做判断所需的信息。
type Review struct {
	Theme     Theme
	Style     presentation.Style
	Verdict   ReviewVerdict
	Headline  string
	Summary   string
	Workspace string
	Thread    string
	Target    string
	Highest   string
	Facts     []Fact
	Findings  []ReviewFinding
	Options   []Option
	Footer    string
	Height    int
}

func (r *Renderer) RenderReview(ctx context.Context, review Review) (*Artifact, error) {
	review, err := prepareReview(review, r.currentTime())
	if err != nil {
		return nil, err
	}
	var output strings.Builder
	if err := r.tmpl.ExecuteTemplate(&output, reviewTemplateName(review.Style), review); err != nil {
		return nil, fmt.Errorf("execute review template: %w", err)
	}
	return r.renderArtifact(ctx, "review-*", review.Height, []byte(output.String()))
}

func prepareReview(review Review, now time.Time) (Review, error) {
	review.Style = presentation.NormalizeStyle(review.Style)
	if review.Theme != ThemeDay && review.Theme != ThemeNight {
		review.Theme = ThemeForTime(now)
	}
	review.Headline = strings.TrimSpace(review.Headline)
	review.Summary = strings.TrimSpace(review.Summary)
	review.Workspace = strings.TrimSpace(review.Workspace)
	review.Thread = strings.TrimSpace(review.Thread)
	review.Target = strings.TrimSpace(review.Target)
	review.Highest = strings.TrimSpace(review.Highest)
	review.Footer = strings.TrimSpace(review.Footer)
	if review.Headline == "" || review.Workspace == "" || review.Thread == "" || review.Target == "" {
		return Review{}, fmt.Errorf("review requires headline, workspace, thread and target")
	}
	if runeCount(review.Headline) > 34 || runeCount(review.Summary) > 96 || runeCount(review.Workspace) > 36 ||
		runeCount(review.Thread) > 48 || runeCount(review.Target) > 24 || runeCount(review.Footer) > 100 {
		return Review{}, fmt.Errorf("review summary exceeds mobile limits")
	}
	switch review.Verdict {
	case ReviewVerdictClear, ReviewVerdictAttention, ReviewVerdictAdvisory:
	default:
		return Review{}, fmt.Errorf("invalid review verdict")
	}
	if len(review.Facts) > 3 || len(review.Findings) > 3 || len(review.Options) == 0 || len(review.Options) > 3 {
		return Review{}, fmt.Errorf("review exceeds mobile content limits")
	}
	for index := range review.Facts {
		fact := &review.Facts[index]
		fact.Label = strings.TrimSpace(fact.Label)
		fact.Value = strings.TrimSpace(fact.Value)
		if fact.Label == "" || fact.Value == "" || runeCount(fact.Label) > 12 || runeCount(fact.Value) > 48 {
			return Review{}, fmt.Errorf("invalid review fact")
		}
	}
	for index := range review.Findings {
		finding := &review.Findings[index]
		finding.Priority = strings.TrimSpace(finding.Priority)
		finding.Title = strings.TrimSpace(finding.Title)
		finding.Detail = strings.TrimSpace(finding.Detail)
		finding.Location = strings.TrimSpace(finding.Location)
		if !validReviewPriority(finding.Priority) || finding.Title == "" || runeCount(finding.Title) > 54 ||
			runeCount(finding.Detail) > 84 || runeCount(finding.Location) > 72 {
			return Review{}, fmt.Errorf("invalid review finding")
		}
	}
	for index := range review.Options {
		option := &review.Options[index]
		option.Number = strings.TrimSpace(option.Number)
		option.Label = strings.TrimSpace(option.Label)
		if option.Number == "" || option.Label == "" {
			return Review{}, fmt.Errorf("invalid review option")
		}
		if option.DisplayLabel == "" {
			option.DisplayLabel, option.Meta = splitOptionMeta(option.Label)
		}
	}
	review.Height = reviewHeight(len(review.Findings), len(review.Facts))
	return review, nil
}

func reviewHeight(findings, facts int) int {
	height := 0
	switch findings {
	case 0:
		height = 1050
	case 1:
		height = 920
	case 2:
		height = 1100
	default:
		height = 1360
	}
	if facts > 0 {
		height += 142
	}
	return height
}

func runeCount(value string) int {
	return len([]rune(strings.TrimSpace(value)))
}

func validReviewPriority(priority string) bool {
	switch priority {
	case "P0", "P1", "P2", "P3":
		return true
	default:
		return false
	}
}

func reviewTemplateName(style presentation.Style) string {
	return "review." + string(presentation.NormalizeStyle(style))
}
