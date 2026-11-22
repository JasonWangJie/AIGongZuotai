// Package studio implements a single-process, local-development AI workbench.
// Its in-memory store is deliberately not a production repository.
package studio

import "time"

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Artifact struct {
	StorageInfo *AssetInfo `json:"-"`
	ID          string     `json:"id"`
	URL         string     `json:"url"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
}

type Task struct {
	Moderation         *ModerationSummary `json:"moderation,omitempty"`
	Billing            *BillingSummary    `json:"billing,omitempty"`
	BillingRequestHash [32]byte           `json:"-"`
	BillingQuoteID     string             `json:"-"`
	Operation          string             `json:"operation,omitempty"`
	ReferenceAssetIDs  []string           `json:"reference_asset_ids,omitempty"`
	FirstFrame         *WorkflowCandidate `json:"first_frame,omitempty"`
	ModelName          string             `json:"model_name,omitempty"`
	ModelVersion       string             `json:"model_version,omitempty"`
	ModelCapabilities  *ModelCapabilities `json:"model_capabilities,omitempty"`
	Parameters         *TaskParameters    `json:"parameters,omitempty"`
	CanReconcile       bool               `json:"can_reconcile"`
	ReconcileAfter     *time.Time         `json:"reconcile_after,omitempty"`
	ProviderMode       string             `json:"provider_mode"`
	WorkspaceID        string             `json:"workspace_id"`
	ID                 string             `json:"id"`
	ProjectID          string             `json:"project_id"`
	Kind               string             `json:"kind"`
	ModelID            string             `json:"model_id"`
	Prompt             string             `json:"prompt"`
	Status             string             `json:"status"`
	Progress           int                `json:"progress"`
	Version            uint64             `json:"version"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
	Artifacts          []Artifact         `json:"artifacts"`
	Error              *APIError          `json:"error,omitempty"`
}

type Run struct {
	ModerationPolicy    *ModerationPolicy   `json:"-"`
	Moderation          *ModerationSummary  `json:"moderation,omitempty"`
	BillingUsage        *ChatUsage          `json:"-"`
	Billing             *BillingSummary     `json:"billing,omitempty"`
	BillingRequestHash  [32]byte            `json:"-"`
	BillingQuoteID      string              `json:"-"`
	ConversationID      string              `json:"conversation_id,omitempty"`
	UserMessageID       string              `json:"user_message_id,omitempty"`
	AssistantMessageID  string              `json:"assistant_message_id,omitempty"`
	ConversationVersion uint64              `json:"conversation_version,omitempty"`
	ContextSummary      *ChatContextSummary `json:"context_summary,omitempty"`
	ModelName           string              `json:"model_name,omitempty"`
	ModelVersion        string              `json:"model_version,omitempty"`
	ModelCapabilities   *ModelCapabilities  `json:"model_capabilities,omitempty"`
	ProviderMode        string              `json:"provider_mode"`
	WorkspaceID         string              `json:"workspace_id"`
	ID                  string              `json:"id"`
	ProjectID           string              `json:"project_id"`
	ModelID             string              `json:"model_id"`
	Prompt              string              `json:"prompt"`
	Status              string              `json:"status"`
	Output              string              `json:"output"`
	Seq                 uint64              `json:"seq"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
	Error               *APIError           `json:"error,omitempty"`
}

type RunInput struct {
	WorkflowAttemptID string `json:"workflow_attempt_id,omitempty"`
	QuoteID           string `json:"quote_id,omitempty"`
	ModelVersion      string `json:"model_version,omitempty"`
	ProjectID         string `json:"project_id"`
	ModelID           string `json:"model_id"`
	Input             string `json:"input"`
}

type TaskInput struct {
	WorkflowAttemptID string `json:"workflow_attempt_id,omitempty"`
	QuoteID           string `json:"quote_id,omitempty"`
	Operation         string `json:"operation,omitempty"`
	ModelVersion      string `json:"model_version,omitempty"`
	ProjectID         string `json:"project_id"`
	Kind              string `json:"kind"`
	ModelID           string `json:"model_id"`
	Inputs            struct {
		Prompt            string             `json:"prompt"`
		ReferenceAssetIDs []string           `json:"reference_asset_ids,omitempty"`
		WorkflowID        string             `json:"workflow_id,omitempty"`
		FirstFrame        *WorkflowCandidate `json:"first_frame,omitempty"`
	} `json:"inputs"`
	Parameters TaskParameters `json:"parameters"`
}

type TaskParameters struct {
	AspectRatio string `json:"aspect_ratio"`
	Count       int    `json:"count"`
	Size        string `json:"size,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	Seconds     int    `json:"seconds,omitempty"`
}

type event struct {
	Cursor string
	Seq    uint64
	Name   string
	Data   []byte
}

func terminal(status string) bool {
	return status == "SUCCEEDED" || status == "COMPLETED" || status == "FAILED" || status == "CANCELLED" || status == "INTERRUPTED" || status == "PARTIAL"
}

type User struct {
	ID              string     `json:"id"`
	Email           string     `json:"email"`
	Name            string     `json:"name"`
	EmailVerified   bool       `json:"email_verified"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
}
type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Project struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
}

func copyTask(t Task) Task {
	if t.FirstFrame != nil {
		source := *t.FirstFrame
		t.FirstFrame = &source
	}
	t.Moderation = copyRunModeration(t.Moderation)
	if !taskArtifactsReleased(t) {
		t.Artifacts = nil
	}
	t.Billing = copyBilling(t.Billing)
	t.ReferenceAssetIDs = append([]string(nil), t.ReferenceAssetIDs...)
	t.Artifacts = append([]Artifact{}, t.Artifacts...)
	t.ModelCapabilities = copyCapabilities(t.ModelCapabilities)
	if t.Parameters != nil {
		parameters := *t.Parameters
		t.Parameters = &parameters
	}
	return t
}

func taskArtifactsReleased(t Task) bool {
	return t.Moderation == nil || t.Moderation.State == "ALLOWED" && (t.Status == "SUCCEEDED" || t.Status == "PARTIAL")
}

func copyCapabilities(c *ModelCapabilities) *ModelCapabilities {
	if c == nil {
		return nil
	}
	next := *c
	next.ParameterPresets = append([]ParameterPreset{}, c.ParameterPresets...)
	return &next
}
func copyRun(r Run) Run {
	r.Moderation = copyRunModeration(r.Moderation)
	if !runOutputReleased(r) {
		r.Output = ""
	}
	r.Billing = copyBilling(r.Billing)
	r.ModelCapabilities = copyCapabilities(r.ModelCapabilities)
	if r.ContextSummary != nil {
		summary := *r.ContextSummary
		r.ContextSummary = &summary
	}
	return r
}

func copyRunModeration(summary *ModerationSummary) *ModerationSummary {
	if summary == nil {
		return nil
	}
	next := *summary
	return &next
}

func runOutputReleased(r Run) bool {
	return r.Moderation == nil || r.Moderation.State == "ALLOWED" && r.Status == "COMPLETED"
}
