package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const maxPanelVideoAttempts = 32

type panelVideoSource struct {
	ProjectID    string `json:"project_id"`
	ComicVersion uint64 `json:"comic_version"`
	PanelID      string `json:"panel_id"`
}

type panelVideoOptions struct {
	ModelID      string         `json:"model_id"`
	ModelVersion string         `json:"model_version"`
	Parameters   TaskParameters `json:"parameters"`
}

type panelVideoRequest struct {
	panelVideoOptions
	ExpectedAttempt int    `json:"expected_attempt"`
	QuoteID         string `json:"quote_id,omitempty"`
}

type panelVideoResult struct {
	panelVideoSource
	panelVideoOptions
	Attempt   int       `json:"attempt"`
	TaskID    string    `json:"task_id"`
	Status    string    `json:"status"`
	QuoteID   string    `json:"quote_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type panelVideoPreview struct {
	panelVideoOptions
	Prompt         string        `json:"prompt"`
	Attempt        int           `json:"attempt"`
	BillingEnabled bool          `json:"billing_enabled"`
	Quote          *BillingQuote `json:"quote,omitempty"`
}

type panelVideoPage struct {
	Items []panelVideoResult `json:"items"`
	Next  *int               `json:"next_before_attempt,omitempty"`
}

func panelVideoPrompt(d ComicDocument, panelID string) (string, error) {
	var panel *ComicPanel
	for i := range d.Panels {
		if d.Panels[i].ID == panelID {
			panel = &d.Panels[i]
			break
		}
	}
	if panel == nil {
		return "", comicMissing()
	}
	card := func(cards []ComicCard, id string) (string, error) {
		for _, c := range cards {
			if c.ID == id {
				return c.Name + "：" + c.Description, nil
			}
		}
		return "", comicInvalid()
	}
	scene, err := card(d.Scenes, panel.SceneID)
	if err != nil {
		return "", err
	}
	lines := []string{"故事：" + d.Title, "梗概：" + d.Story, "镜头：" + panel.Title, "场景：" + scene}
	for _, id := range panel.CharacterIDs {
		role, err := card(d.Characters, id)
		if err != nil {
			return "", err
		}
		lines = append(lines, "角色："+role)
	}
	lines = append(lines, "动作："+panel.Action)
	if panel.Dialogue != "" {
		lines = append(lines, "对白："+panel.Dialogue)
	}
	prompt := strings.Join(lines, "\n")
	if !validatePrompt(prompt) {
		return "", failure(422, "PANEL_VIDEO_PROMPT_LIMIT", "此镜头的完整提示词超过 16000 字节，请缩短后另存并确认新版本")
	}
	return prompt, nil
}

type panelVideoContextKey struct{}
type panelVideoGuard struct {
	source               panelVideoSource
	workspace, actor     string
	keyHash, requestHash [32]byte
	attempt              int
	prompt               string
}

func panelVideoGuardFrom(ctx context.Context) *panelVideoGuard {
	g, _ := ctx.Value(panelVideoContextKey{}).(*panelVideoGuard)
	return g
}

func (g *panelVideoGuard) taskIndex() string {
	return fmt.Sprintf("%s:panel-video:%s:%s:%s", g.workspace, g.source.ProjectID, g.actor, hex.EncodeToString(g.keyHash[:]))
}

// Keep the public TaskInput and all existing price hashes unchanged. The same
// private guard is installed by this route for its own quote and submission.
func (g *panelVideoGuard) quoteHash(in TaskInput) [32]byte {
	original := ledgerTaskRequestHash(in)
	if g == nil {
		return original
	}
	return payloadHash(struct {
		Domain           string
		Source           panelVideoSource
		Workspace, Actor string
		Key              [32]byte
		Attempt          int
		Original         [32]byte
	}{"comic-panel-video-v1", g.source, g.workspace, g.actor, g.keyHash, g.attempt, original})
}

func newPanelVideoGuard(p *principal, s panelVideoSource, key string, r panelVideoRequest) *panelVideoGuard {
	return &panelVideoGuard{workspace: p.Workspace.ID, actor: p.User.ID, source: s, keyHash: sha256.Sum256([]byte(key)),
		requestHash: payloadHash(struct {
			Source  panelVideoSource
			Request panelVideoRequest
		}{s, r})}
}

func panelVideoConflict() error {
	return failure(409, "PANEL_VIDEO_ATTEMPT_CONFLICT", "本镜头的尝试已变化，请核对原收据或重新预览")
}
func panelVideoUnresolved() error {
	return failure(409, "PANEL_VIDEO_UNRESOLVED", "上一尝试尚在执行或受理情况待核对，请先查看原任务")
}
func panelVideoKeyConflict() error {
	return failure(409, "IDEMPOTENCY_CONFLICT", "相同原键已用于另一分镜生成请求")
}
