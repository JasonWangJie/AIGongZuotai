package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const mediaModerationContract = "media-v1"
const mediaModerationTimeout = 60 * time.Second
const mediaModerationImageLimit int64 = 32 << 20
const mediaModerationVideoLimit int64 = 256 << 20

// Credentials and destinations are runtime-only and never enter policy JSON.
type MediaModerationConfig struct{ Mode, PolicyFile, Endpoint, APIKey, CredentialID string }
type MediaModerationFixture struct {
	Kind     string `json:"kind"`
	Phase    string `json:"phase"`
	SHA256   string `json:"sha256"`
	ByteSize int64  `json:"byte_size"`
	Decision string `json:"decision"`
}
type mediaModerationPolicyFile struct {
	SchemaVersion int                      `json:"schema_version"`
	PolicyID      string                   `json:"policy_id"`
	PolicyVersion string                   `json:"policy_version"`
	MaxImageBytes int64                    `json:"max_image_bytes"`
	MaxVideoBytes int64                    `json:"max_video_bytes"`
	Fixtures      []MediaModerationFixture `json:"fixtures"`
}
type MediaModerationPolicy struct {
	SchemaVersion   int                      `json:"schema_version"`
	PolicyID        string                   `json:"policy_id"`
	PolicyVersion   string                   `json:"policy_version"`
	Mode            string                   `json:"mode"`
	ProviderMode    string                   `json:"provider_mode"`
	Contract        string                   `json:"contract"`
	AdapterIdentity string                   `json:"adapter_identity"`
	MaxImageBytes   int64                    `json:"max_image_bytes"`
	MaxVideoBytes   int64                    `json:"max_video_bytes"`
	Fixtures        []MediaModerationFixture `json:"fixtures"`
	Digest          string                   `json:"digest"`
}
type MediaModerationContent struct {
	Kind        string `json:"kind"`
	ContentType string `json:"content_type"`
	ByteSize    int64  `json:"byte_size"`
	SHA256      string `json:"sha256"`
}

// This record belongs only to private persistence. Artifact.StorageInfo has a
// public json:"-" tag, so the SQL codec explicitly preserves it separately.
type MediaModerationRecord struct {
	Policy     *MediaModerationPolicy
	TextPolicy *ModerationPolicy
	Summary    *ModerationSummary
	Candidates []Artifact
}

type privateMediaCandidate struct {
	Artifact    Artifact   `json:"artifact"`
	StorageInfo *AssetInfo `json:"storage_info"`
}

func encodeMediaCandidates(candidates []Artifact, kind string) ([]byte, error) {
	if len(candidates) > 4 {
		return nil, errors.New("media candidate count invalid")
	}
	stored := make([]privateMediaCandidate, 0, len(candidates))
	seen := map[string]bool{}
	for _, a := range candidates {
		info := a.StorageInfo
		if !localAssetID.MatchString(a.ID) || seen[a.ID] || a.URL != "/api/v1/assets/"+a.ID || a.Kind != kind || (kind != "image" && kind != "video") || info == nil || !validAssetInfo(*info, a.ID) || !moderationDigestPattern.MatchString(info.SHA256) || !utf8.ValidString(a.Title) || len(a.Title) > 512 || bytes.IndexByte([]byte(a.Title), 0) >= 0 {
			return nil, errors.New("private media candidate invalid")
		}
		p := &MediaModerationPolicy{MaxImageBytes: mediaModerationImageLimit, MaxVideoBytes: mediaModerationVideoLimit}
		if !validMediaModerationContent(MediaModerationContent{Kind: kind, ContentType: info.ContentType, ByteSize: info.ByteSize, SHA256: info.SHA256}, p) {
			return nil, errors.New("private media candidate identity invalid")
		}
		if kind == "video" && len(candidates) > 1 {
			return nil, errors.New("video candidate count invalid")
		}
		seen[a.ID] = true
		stored = append(stored, privateMediaCandidate{Artifact: a, StorageInfo: info})
	}
	return json.Marshal(stored)
}
func decodeMediaCandidates(raw []byte) ([]Artifact, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("private media candidate record too large")
	}
	var stored []privateMediaCandidate
	if json.Unmarshal(raw, &stored) != nil || stored == nil || len(stored) < 1 || len(stored) > 4 {
		return nil, errors.New("private media candidates invalid")
	}
	result := make([]Artifact, 0, len(stored))
	for _, c := range stored {
		a := c.Artifact
		a.StorageInfo = c.StorageInfo
		result = append(result, a)
	}
	if _, err := encodeMediaCandidates(result, result[0].Kind); err != nil {
		return nil, err
	}
	return result, nil
}
func mediaModerationStateError() error {
	return failure(409, "MEDIA_MODERATION_STATE_CHANGED", "媒体审核快照或决定已变化，不能发布候选内容")
}
func mediaSummaryMatchesPolicy(p *MediaModerationPolicy, s *ModerationSummary, phase string) bool {
	return validMediaModerationPolicy(p) && validMediaModerationSummary(s) && s.Phase == phase && s.PolicyID == p.PolicyID && s.PolicyVersion == p.PolicyVersion
}

func insertTaskMediaModerationSQL(ctx context.Context, tx pgx.Tx, task Task, state *MediaModerationRecord) error {
	if state == nil {
		if task.Moderation != nil {
			return mediaModerationStateError()
		}
		return nil
	}
	p, s := state.Policy, state.Summary
	if !mediaSummaryMatchesPolicy(p, s, "output") || s.State != "PENDING" || s.Version != 1 || len(state.Candidates) != 0 || len(task.Artifacts) != 0 || !validModerationPolicy(state.TextPolicy) || p.ProviderMode != task.ProviderMode || state.TextPolicy.ProviderMode != task.ProviderMode || task.Moderation == nil || *task.Moderation != *s {
		return mediaModerationStateError()
	}
	raw, _ := json.Marshal(p)
	textRaw, _ := json.Marshal(state.TextPolicy)
	_, err := tx.Exec(ctx, `INSERT INTO studio_task_media_moderation(task_id,workspace_id,project_id,policy, text_policy,policy_id,policy_version,policy_digest,state,version,reason_code) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'PENDING',1,'')`, task.ID, task.WorkspaceID, task.ProjectID, raw, textRaw, p.PolicyID, p.PolicyVersion, p.Digest)
	if err != nil {
		return err
	}
	return insertMediaModerationDecisionSQL(ctx, tx, task.ID, *s)
}

func loadTaskMediaModerationSQL(ctx context.Context, conn sqlQueryRower, taskID string) (*MediaModerationRecord, error) {
	var policy, textPolicy, candidates []byte
	var s ModerationSummary
	s.Phase = "output"
	err := conn.QueryRow(ctx, `SELECT m.policy,m.text_policy,m.policy_id,m.policy_version,m.state,m.version,m.reason_code,c.candidates FROM studio_task_media_moderation m LEFT JOIN studio_task_media_candidates c ON c.task_id=m.task_id WHERE m.task_id=$1`, taskID).Scan(&policy, &textPolicy, &s.PolicyID, &s.PolicyVersion, &s.State, &s.Version, &s.ReasonCode, &candidates)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeTaskMediaModeration(policy, textPolicy, candidates, s)
}

// Shared by single-resource reads and bounded startup page reads.
func decodeTaskMediaModeration(policy, textPolicy, candidates []byte, s ModerationSummary) (*MediaModerationRecord, error) {
	var p MediaModerationPolicy
	var tp ModerationPolicy
	if json.Unmarshal(policy, &p) != nil || json.Unmarshal(textPolicy, &tp) != nil || !mediaSummaryMatchesPolicy(&p, &s, "output") || !validModerationPolicy(&tp) || tp.ProviderMode != p.ProviderMode {
		return nil, mediaModerationStateError()
	}
	items, err := decodeMediaCandidates(candidates)
	if err != nil {
		return nil, err
	}
	if s.State == "ALLOWED" && len(items) == 0 {
		return nil, mediaModerationStateError()
	}
	return &MediaModerationRecord{Policy: &p, TextPolicy: &tp, Summary: &s, Candidates: items}, nil
}

// Call after the task lease/version fence and before library, billing and
// Outbox writes. Nil private state on cancel/operator paths loads original
// policy/candidates; it never reconstructs them from current configuration.
func saveTaskMediaModerationSQL(ctx context.Context, tx pgx.Tx, task Task, state *MediaModerationRecord) error {
	var lockedID, workspace, project string
	err := tx.QueryRow(ctx, `SELECT task_id,workspace_id,project_id FROM studio_task_media_moderation WHERE task_id=$1 FOR UPDATE`, task.ID).Scan(&lockedID, &workspace, &project)
	if errors.Is(err, pgx.ErrNoRows) {
		if state == nil && task.Moderation == nil {
			return nil
		}
		return mediaModerationStateError()
	}
	if err != nil {
		return err
	}
	if workspace != task.WorkspaceID || project != task.ProjectID {
		return mediaModerationStateError()
	}
	original, err := loadTaskMediaModerationSQL(ctx, tx, task.ID)
	if err != nil {
		return err
	}
	s := task.Moderation
	if !mediaSummaryMatchesPolicy(original.Policy, s, "output") {
		return mediaModerationStateError()
	}
	candidates := original.Candidates
	if state != nil {
		if !mediaSummaryMatchesPolicy(state.Policy, state.Summary, "output") || *state.Summary != *s || state.Policy.Digest != original.Policy.Digest || !validModerationPolicy(state.TextPolicy) || state.TextPolicy.Digest != original.TextPolicy.Digest {
			return mediaModerationStateError()
		}
		candidates = state.Candidates
	}
	raw, err := encodeMediaCandidates(candidates, task.Kind)
	if err != nil {
		return err
	}
	oldRaw, err := encodeMediaCandidates(original.Candidates, task.Kind)
	if err != nil {
		return err
	}
	if len(original.Candidates) > 0 && !bytes.Equal(raw, oldRaw) {
		return mediaModerationStateError()
	}
	if len(original.Candidates) == 0 && len(candidates) > 0 {
		if original.Summary.State != "PENDING" {
			return mediaModerationStateError()
		}
		if _, err = tx.Exec(ctx, `INSERT INTO studio_task_media_candidates(task_id,candidates) VALUES($1,$2)`, task.ID, raw); err != nil {
			return err
		}
	}
	if s.State == "ALLOWED" && len(candidates) == 0 {
		return mediaModerationStateError()
	}
	// Enforce the public release invariant even for exact-summary saves from
	// operator or billing paths. JSON-restored public artifacts intentionally
	// lack StorageInfo; compare only their ordered public delivery identity.
	if !validMediaModerationRelease(task, candidates) {
		return mediaModerationStateError()
	}
	old := original.Summary
	if *s == *old {
		return nil
	}
	if old.State != "PENDING" || s.State == "PENDING" || s.Version != old.Version+1 {
		return mediaModerationStateError()
	}
	tag, err := tx.Exec(ctx, `UPDATE studio_task_media_moderation SET state=$2,version=$3,reason_code=$4,updated_at=clock_timestamp() WHERE task_id=$1 AND version=$5 AND state='PENDING'`, task.ID, s.State, s.Version, s.ReasonCode, old.Version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return mediaModerationStateError()
	}
	return insertMediaModerationDecisionSQL(ctx, tx, task.ID, *s)
}
func validMediaModerationRelease(task Task, candidates []Artifact) bool {
	if task.Moderation == nil {
		return false
	}
	if task.Moderation.State != "ALLOWED" {
		return len(task.Artifacts) == 0
	}
	if (task.Status != "SUCCEEDED" && task.Status != "PARTIAL") || len(candidates) == 0 || len(task.Artifacts) != len(candidates) {
		return false
	}
	for i, actual := range task.Artifacts {
		frozen := candidates[i]
		if actual.ID != frozen.ID || actual.Kind != frozen.Kind || actual.URL != frozen.URL {
			return false
		}
	}
	return true
}
func insertMediaModerationDecisionSQL(ctx context.Context, tx pgx.Tx, taskID string, s ModerationSummary) error {
	_, err := tx.Exec(ctx, `INSERT INTO studio_media_moderation_decisions(task_id,version,state,reason_code) VALUES($1,$2,$3,$4)`, taskID, s.Version, s.State, s.ReasonCode)
	return err
}

func insertReferenceMediaModerationSQL(ctx context.Context, tx pgx.Tx, refID, workspaceID, projectID string, policy *MediaModerationPolicy, summary *ModerationSummary) error {
	if policy == nil && summary == nil {
		return nil
	}
	if !mediaSummaryMatchesPolicy(policy, summary, "input") || summary.State != "ALLOWED" || summary.Version != 2 || !referenceAssetID.MatchString(refID) {
		return mediaModerationStateError()
	}
	raw, _ := json.Marshal(policy)
	_, err := tx.Exec(ctx, `INSERT INTO studio_reference_media_moderation(reference_id,workspace_id,project_id,policy,policy_id,policy_version,policy_digest,state,version,reason_code) VALUES($1,$2,$3,$4,$5,$6,$7,'ALLOWED',2,'allowed')`, refID, workspaceID, projectID, raw, policy.PolicyID, policy.PolicyVersion, policy.Digest)
	return err
}
func loadReferenceMediaModerationSQL(ctx context.Context, conn sqlQueryRower, refID string) (*MediaModerationPolicy, *ModerationSummary, error) {
	var raw []byte
	var s ModerationSummary
	s.Phase = "input"
	err := conn.QueryRow(ctx, `SELECT policy,policy_id,policy_version,state,version,reason_code FROM studio_reference_media_moderation WHERE reference_id=$1`, refID).Scan(&raw, &s.PolicyID, &s.PolicyVersion, &s.State, &s.Version, &s.ReasonCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var p MediaModerationPolicy
	if json.Unmarshal(raw, &p) != nil || !mediaSummaryMatchesPolicy(&p, &s, "input") || s.State != "ALLOWED" || s.Version != 2 {
		return nil, nil, mediaModerationStateError()
	}
	return &p, &s, nil
}

type mediaModerationEngine struct {
	policy       MediaModerationPolicy
	providerMode string
	slots        chan struct{}
	http         *mediaModerationHTTPClient
	permits      mediaPermitArbiter
}
type sqlQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func newMediaModerationEngine(cfg MediaModerationConfig, providerMode string) (*mediaModerationEngine, error) {
	if cfg.Mode == "" || cfg.Mode == "disabled" {
		return nil, nil
	}
	if cfg.Mode != "fixtures" && cfg.Mode != "http" {
		return nil, errors.New("media moderation mode must be disabled, fixtures or http")
	}
	if providerMode != "demo" && providerMode != "live" {
		return nil, errors.New("media moderation requires explicit provider mode")
	}
	if cfg.Mode == "fixtures" && providerMode != "demo" {
		return nil, errors.New("media moderation fixtures are demo-only byte identities")
	}
	f, err := os.Open(cfg.PolicyFile)
	if err != nil {
		return nil, errors.New("media moderation policy unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, moderationPolicyLimit+1))
	if err != nil || len(raw) > moderationPolicyLimit {
		return nil, errors.New("media moderation policy exceeds 64 KiB")
	}
	var config mediaModerationPolicyFile
	if !utf8.Valid(raw) || validateCatalogJSONKeys(raw) != nil {
		return nil, errors.New("media moderation policy requires strict UTF-8 JSON")
	}
	var fields map[string]json.RawMessage
	keys := map[string]bool{"schema_version": true, "policy_id": true, "policy_version": true, "max_image_bytes": true, "max_video_bytes": true, "fixtures": true}
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
		return nil, errors.New("media moderation policy fields invalid")
	}
	for k := range fields {
		if !keys[k] {
			return nil, errors.New("media moderation policy fields invalid")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || config.Fixtures == nil {
		return nil, errors.New("media moderation policy fields invalid")
	}
	// encoding/json matches struct fields case-insensitively. Validate each
	// nested fixture explicitly, including required zero/nonzero fields.
	var fixtures []map[string]json.RawMessage
	if json.Unmarshal(fields["fixtures"], &fixtures) != nil {
		return nil, errors.New("media moderation fixture fields invalid")
	}
	fixtureKeys := map[string]bool{"kind": true, "phase": true, "sha256": true, "byte_size": true, "decision": true}
	for _, fields := range fixtures {
		if len(fields) != len(fixtureKeys) {
			return nil, errors.New("media moderation fixture fields invalid")
		}
		for k := range fields {
			if !fixtureKeys[k] {
				return nil, errors.New("media moderation fixture fields invalid")
			}
		}
	}
	p := MediaModerationPolicy{SchemaVersion: config.SchemaVersion, PolicyID: config.PolicyID, PolicyVersion: config.PolicyVersion, Mode: cfg.Mode, ProviderMode: providerMode, Contract: mediaModerationContract, MaxImageBytes: config.MaxImageBytes, MaxVideoBytes: config.MaxVideoBytes, Fixtures: append([]MediaModerationFixture{}, config.Fixtures...)}
	e := &mediaModerationEngine{providerMode: providerMode, slots: make(chan struct{}, 2)}
	if cfg.Mode == "http" {
		if len(p.Fixtures) != 0 {
			return nil, errors.New("HTTP media moderation cannot contain fixtures")
		}
		e.http, err = newMediaModerationHTTPClient(cfg, providerMode)
		if err != nil {
			return nil, err
		}
		p.AdapterIdentity = e.http.identity
	} else {
		if cfg.Endpoint != "" || cfg.APIKey != "" || cfg.CredentialID != "" {
			return nil, errors.New("media fixtures cannot accept HTTP configuration")
		}
		p.AdapterIdentity = moderationSHA([]byte("demo-exact-media-bytes-v1"))
	}
	p.Digest = mediaModerationPolicyDigest(p)
	if !validMediaModerationPolicy(&p) {
		return nil, errors.New("media moderation policy limits or identities invalid")
	}
	e.policy = p
	return e, nil
}
func mediaModerationPolicyDigest(p MediaModerationPolicy) string {
	p.Digest = ""
	raw, _ := json.Marshal(p)
	return moderationSHA(raw)
}
func validMediaModerationPolicy(p *MediaModerationPolicy) bool {
	if p == nil || p.SchemaVersion != 1 || !moderationNamePattern.MatchString(p.PolicyID) || !moderationNamePattern.MatchString(p.PolicyVersion) || p.Contract != mediaModerationContract || !moderationDigestPattern.MatchString(p.AdapterIdentity) || p.MaxImageBytes < 1 || p.MaxImageBytes > mediaModerationImageLimit || p.MaxVideoBytes < 1 || p.MaxVideoBytes > mediaModerationVideoLimit || p.Fixtures == nil || len(p.Fixtures) > 64 || p.Digest != mediaModerationPolicyDigest(*p) {
		return false
	}
	if p.ProviderMode != "demo" && p.ProviderMode != "live" {
		return false
	}
	if p.Mode == "http" {
		return len(p.Fixtures) == 0
	}
	if p.Mode != "fixtures" || p.ProviderMode != "demo" || p.AdapterIdentity != moderationSHA([]byte("demo-exact-media-bytes-v1")) {
		return false
	}
	seen := map[string]bool{}
	for _, f := range p.Fixtures {
		limit := p.MaxImageBytes
		if f.Kind == "video" {
			limit = p.MaxVideoBytes
		} else if f.Kind != "image" {
			return false
		}
		if (f.Phase != "input" && f.Phase != "output") || (f.Decision != "allow" && f.Decision != "block" && f.Decision != "review") || !moderationDigestPattern.MatchString(f.SHA256) || f.ByteSize < 1 || f.ByteSize > limit {
			return false
		}
		key := f.Kind + "\n" + f.Phase + "\n" + f.SHA256
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
func (e *mediaModerationEngine) Snapshot() *MediaModerationPolicy {
	if e == nil {
		return nil
	}
	p := e.policy
	p.Fixtures = append([]MediaModerationFixture{}, p.Fixtures...)
	return &p
}
func InitialMediaModerationSummary(p *MediaModerationPolicy, phase string) *ModerationSummary {
	if p == nil {
		return nil
	}
	return &ModerationSummary{PolicyID: p.PolicyID, PolicyVersion: p.PolicyVersion, Phase: phase, State: "PENDING", Version: 1}
}
func validMediaModerationSummary(s *ModerationSummary) bool {
	if s == nil || (s.Phase != "input" && s.Phase != "output") {
		return false
	}
	copy := *s
	copy.Phase = "output"
	return validModerationSummary(&copy)
}
func validMediaModerationContent(c MediaModerationContent, p *MediaModerationPolicy) bool {
	if c.ByteSize < 1 || !moderationDigestPattern.MatchString(c.SHA256) {
		return false
	}
	if c.Kind == "video" {
		return c.ContentType == "video/mp4" && c.ByteSize <= p.MaxVideoBytes
	}
	return c.Kind == "image" && c.ByteSize <= p.MaxImageBytes && (c.ContentType == "image/png" || c.ContentType == "image/jpeg" || c.ContentType == "image/webp" || c.ContentType == "image/gif")
}
func verifyMediaModerationBytes(ctx context.Context, c MediaModerationContent, reader io.ReadSeeker) error {
	if reader == nil {
		return errors.New("missing private media")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	var total int64
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := reader.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > c.ByteSize {
				return errors.New("private media length changed")
			}
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	if total != c.ByteSize || hex.EncodeToString(h.Sum(nil)) != c.SHA256 {
		return errors.New("private media identity changed")
	}
	_, err := reader.Seek(0, io.SeekStart)
	return err
}
func (e *mediaModerationEngine) Check(ctx context.Context, p *MediaModerationPolicy, phase string, c MediaModerationContent, reader io.ReadSeeker) ModerationDecision {
	if !e.compatiblePolicy(p) {
		return moderationReview("policy_unavailable")
	}
	if (phase != "input" && phase != "output") || !validMediaModerationContent(c, p) {
		return moderationReview("invalid_content")
	}
	permit, ok := e.tryAcquireReviewPermit()
	if !ok {
		return moderationReview("check_busy")
	}
	defer permit.Close()
	return permit.Check(ctx, p, phase, c, reader)
}

// checkMediaWithPermit performs only bounded byte/protocol work. All callers
// must own this engine's permit; it deliberately does not acquire another slot.
func (e *mediaModerationEngine) checkMediaWithPermit(ctx context.Context, p *MediaModerationPolicy, phase string, c MediaModerationContent, reader io.ReadSeeker) ModerationDecision {
	return e.checkMediaWithObservation(ctx, p, phase, c, reader, &MediaScheduleObservation{Scope: "review", Class: "protocol"})
}
func (e *mediaModerationEngine) checkMediaWithObservation(ctx context.Context, p *MediaModerationPolicy, phase string, c MediaModerationContent, reader io.ReadSeeker, observation *MediaScheduleObservation) ModerationDecision {
	if err := verifyMediaModerationBytes(ctx, c, reader); err != nil {
		if ctx.Err() != nil {
			return moderationReview("check_timeout")
		}
		return moderationReview("invalid_content")
	}
	actualType, err := validateAssetEncoding(reader, c.Kind, c.ByteSize)
	if err != nil || actualType != c.ContentType {
		return moderationReview("invalid_content")
	}
	if _, err = reader.Seek(0, io.SeekStart); err != nil {
		return moderationReview("invalid_content")
	}
	if ctx.Err() != nil {
		return moderationReview("check_timeout")
	}
	if p.Mode == "http" {
		decision, observed := e.http.checkObserved(ctx, p, phase, c, reader)
		*observation = *observed
		return decision
	}
	observation.Class = "success"
	for _, f := range p.Fixtures {
		if f.Kind == c.Kind && f.Phase == phase && f.SHA256 == c.SHA256 && f.ByteSize == c.ByteSize {
			switch f.Decision {
			case "allow":
				return ModerationDecision{State: "ALLOWED", ReasonCode: "allowed"}
			case "block":
				return ModerationDecision{State: "BLOCKED", ReasonCode: "policy_block"}
			default:
				return moderationReview("policy_review")
			}
		}
	}
	return moderationReview("policy_review")
}
