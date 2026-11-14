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
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const moderationPolicyLimit = 64 << 10
const moderationTimeout = 10 * time.Second
const moderationContract = "text-v1"

// Runtime secrets and destinations never enter a persisted policy or public API.
type ModerationConfig struct{ Mode, PolicyFile, Endpoint, APIKey, CredentialID string }
type ModerationRule struct {
	Phase    string `json:"phase"`
	Contains string `json:"contains"`
	Decision string `json:"decision"`
}
type moderationPolicyFile struct {
	SchemaVersion  int              `json:"schema_version"`
	PolicyID       string           `json:"policy_id"`
	PolicyVersion  string           `json:"policy_version"`
	MaxInputBytes  int              `json:"max_input_bytes"`
	MaxOutputBytes int              `json:"max_output_bytes"`
	Rules          []ModerationRule `json:"rules"`
}

// ModerationPolicy is the complete, non-secret accepted policy. Digest
// seals the policy itself; it never contains a user prompt or model response.
type ModerationPolicy struct {
	SchemaVersion   int              `json:"schema_version"`
	PolicyID        string           `json:"policy_id"`
	PolicyVersion   string           `json:"policy_version"`
	Mode            string           `json:"mode"`
	ProviderMode    string           `json:"provider_mode"`
	Contract        string           `json:"contract"`
	AdapterIdentity string           `json:"adapter_identity"`
	MaxInputBytes   int              `json:"max_input_bytes"`
	MaxOutputBytes  int              `json:"max_output_bytes"`
	Rules           []ModerationRule `json:"rules"`
	Digest          string           `json:"digest"`
}
type ModerationDecision struct {
	State      string `json:"state"`
	ReasonCode string `json:"reason_code"`
}
type ModerationSummary struct {
	PolicyID      string `json:"policy_id"`
	PolicyVersion string `json:"policy_version"`
	Phase         string `json:"phase"`
	State         string `json:"state"`
	Version       uint64 `json:"version"`
	ReasonCode    string `json:"reason_code,omitempty"`
}
type ModerationCapabilities struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode"`
	PolicyID       string `json:"policy_id,omitempty"`
	PolicyVersion  string `json:"policy_version,omitempty"`
	TextDelivery   string `json:"text_delivery"`
	MediaSupported bool   `json:"media_supported"`
}
type moderationEngine struct {
	policy       ModerationPolicy
	providerMode string
	slots        chan struct{}
	http         *moderationHTTPClient
}

var moderationNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var moderationDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func newModerationEngine(cfg ModerationConfig, providerMode string) (*moderationEngine, error) {
	if cfg.Mode == "" || cfg.Mode == "disabled" {
		return nil, nil
	}
	if cfg.Mode != "rules" && cfg.Mode != "http" {
		return nil, errors.New("moderation mode must be disabled, rules or http")
	}
	if providerMode != "demo" && providerMode != "live" {
		return nil, errors.New("moderation requires an explicit provider mode")
	}
	if cfg.Mode == "rules" && providerMode != "demo" {
		return nil, errors.New("moderation rules are limited demo fixtures; live requires a private HTTP adapter")
	}
	if cfg.PolicyFile == "" {
		return nil, errors.New("moderation requires a policy file")
	}
	f, err := os.Open(cfg.PolicyFile)
	if err != nil {
		return nil, errors.New("moderation policy file unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, moderationPolicyLimit+1))
	if err != nil || len(raw) > moderationPolicyLimit {
		return nil, errors.New("moderation policy exceeds 64 KiB")
	}
	var config moderationPolicyFile
	if err = decodeModerationPolicy(raw, &config); err != nil {
		return nil, err
	}
	engine := &moderationEngine{providerMode: providerMode, slots: make(chan struct{}, 4)}
	policy := ModerationPolicy{SchemaVersion: config.SchemaVersion, PolicyID: config.PolicyID, PolicyVersion: config.PolicyVersion, Mode: cfg.Mode, ProviderMode: providerMode, Contract: moderationContract, MaxInputBytes: config.MaxInputBytes, MaxOutputBytes: config.MaxOutputBytes, Rules: append([]ModerationRule{}, config.Rules...)}
	if cfg.Mode == "http" {
		if len(policy.Rules) != 0 {
			return nil, errors.New("HTTP moderation policy cannot contain local rules")
		}
		engine.http, err = newModerationHTTPClient(cfg, providerMode)
		if err != nil {
			return nil, err
		}
		policy.AdapterIdentity = engine.http.identity
	} else {
		if cfg.Endpoint != "" || cfg.APIKey != "" || cfg.CredentialID != "" {
			return nil, errors.New("demo moderation rules do not accept HTTP credentials or endpoints")
		}
		policy.AdapterIdentity = moderationSHA([]byte("demo-literal-rules-v1"))
	}
	policy.Digest = moderationPolicyDigest(policy)
	if !validModerationPolicy(&policy) {
		return nil, errors.New("moderation policy fields or limits are invalid")
	}
	engine.policy = policy
	return engine, nil
}

func decodeModerationPolicy(raw []byte, out *moderationPolicyFile) error {
	if !utf8.Valid(raw) || validateCatalogJSONKeys(raw) != nil {
		return errors.New("moderation policy requires strict UTF-8 JSON without duplicate keys")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("moderation policy must be a JSON object")
	}
	allowed := map[string]bool{"schema_version": true, "policy_id": true, "policy_version": true, "max_input_bytes": true, "max_output_bytes": true, "rules": true}
	for k := range fields {
		if !allowed[k] {
			return errors.New("moderation policy contains an unknown field")
		}
	}
	if len(fields) != len(allowed) {
		return errors.New("moderation policy is missing required fields")
	}
	var rules []map[string]json.RawMessage
	if bytes.Equal(bytes.TrimSpace(fields["rules"]), []byte("null")) || json.Unmarshal(fields["rules"], &rules) != nil {
		return errors.New("moderation rules must be an array")
	}
	for _, rule := range rules {
		if len(rule) != 3 {
			return errors.New("moderation rule requires phase, contains and decision")
		}
		for k := range rule {
			if k != "phase" && k != "contains" && k != "decision" {
				return errors.New("moderation rule contains an unknown field")
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("moderation policy fields have invalid types")
	}
	return nil
}
func moderationSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func moderationPolicyDigest(p ModerationPolicy) string {
	p.Digest = ""
	raw, _ := json.Marshal(p)
	return moderationSHA(raw)
}
func validModerationPolicy(p *ModerationPolicy) bool {
	if p == nil || p.SchemaVersion != 1 || !moderationNamePattern.MatchString(p.PolicyID) || !moderationNamePattern.MatchString(p.PolicyVersion) || p.Contract != moderationContract || !moderationDigestPattern.MatchString(p.AdapterIdentity) || !moderationDigestPattern.MatchString(p.Digest) || p.Digest != moderationPolicyDigest(*p) || p.MaxInputBytes < 1 || p.MaxInputBytes > MaxChatContentBytes || p.MaxOutputBytes < 1 || p.MaxOutputBytes > maxRunOutputBytes || len(p.Rules) > 64 {
		return false
	}
	if p.ProviderMode != "demo" && p.ProviderMode != "live" {
		return false
	}
	if p.Mode == "http" {
		return len(p.Rules) == 0
	}
	if p.Mode != "rules" || p.ProviderMode != "demo" || len(p.Rules) == 0 || p.AdapterIdentity != moderationSHA([]byte("demo-literal-rules-v1")) {
		return false
	}
	for _, r := range p.Rules {
		if r.Phase != "input" && r.Phase != "output" && r.Phase != "both" || r.Decision != "block" && r.Decision != "review" || len(r.Contains) < 1 || len(r.Contains) > 256 || !utf8.ValidString(r.Contains) || strings.ContainsRune(r.Contains, 0) {
			return false
		}
	}
	return true
}
func (e *moderationEngine) Snapshot() *ModerationPolicy {
	if e == nil {
		return nil
	}
	p := e.policy
	p.Rules = append([]ModerationRule{}, p.Rules...)
	return &p
}
func InitialModerationSummary(p *ModerationPolicy) *ModerationSummary {
	if p == nil {
		return nil
	}
	return &ModerationSummary{PolicyID: p.PolicyID, PolicyVersion: p.PolicyVersion, Phase: "output", State: "PENDING", Version: 1}
}
func moderationCapabilities(e *moderationEngine) ModerationCapabilities {
	if e == nil {
		return ModerationCapabilities{Mode: "disabled", TextDelivery: "stream", MediaSupported: true}
	}
	return ModerationCapabilities{Enabled: true, Mode: e.policy.Mode, PolicyID: e.policy.PolicyID, PolicyVersion: e.policy.PolicyVersion, TextDelivery: "after_review", MediaSupported: false}
}
func (e *moderationEngine) Capabilities() ModerationCapabilities { return moderationCapabilities(e) }
func moderationReview(reason string) ModerationDecision {
	return ModerationDecision{State: "REVIEW", ReasonCode: reason}
}

func (e *moderationEngine) CheckInput(ctx context.Context, request ChatRequest) (*ModerationPolicy, error) {
	if e == nil {
		return nil, nil
	}
	policy := e.Snapshot()
	decision := e.CheckInputPolicy(ctx, policy, request)
	switch decision.State {
	case "ALLOWED":
		return policy, nil
	case "BLOCKED":
		return nil, failure(422, "MODERATION_INPUT_BLOCKED", "输入未通过当前内容政策，未发起生成")
	default:
		switch decision.ReasonCode {
		case "policy_review":
			return nil, failure(409, "MODERATION_INPUT_REVIEW", "输入需要进一步核实，当前不会生成")
		case "content_too_large", "invalid_content":
			return nil, failure(422, "MODERATION_INPUT_INVALID", "输入不符合当前审核文本与大小限制，未发起生成")
		case "check_busy":
			return nil, failure(429, "MODERATION_BUSY", "审核处理繁忙，请稍后使用原请求核对")
		default:
			return nil, failure(503, "MODERATION_UNAVAILABLE", "内容审核未能确认，未发起生成")
		}
	}
}
func (e *moderationEngine) CheckInputPolicy(ctx context.Context, p *ModerationPolicy, request ChatRequest) ModerationDecision {
	if !validModerationPolicy(p) {
		return moderationReview("policy_unavailable")
	}
	if _, err := validateChatWireRequest(request); err != nil {
		return moderationReview("invalid_content")
	}
	n := 0
	for _, message := range request.Messages {
		if strings.ContainsRune(message.Content, 0) {
			return moderationReview("invalid_content")
		}
		n += len(message.Content)
	}
	if n > p.MaxInputBytes {
		return moderationReview("content_too_large")
	}
	return e.check(ctx, p, "input", request.Messages, "")
}
func (e *moderationEngine) CheckOutput(ctx context.Context, p *ModerationPolicy, text string) ModerationDecision {
	if !validModerationPolicy(p) {
		return moderationReview("policy_unavailable")
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || strings.TrimSpace(text) == "" {
		return moderationReview("invalid_content")
	}
	if len(text) > p.MaxOutputBytes {
		return moderationReview("content_too_large")
	}
	return e.check(ctx, p, "output", nil, text)
}
func (e *moderationEngine) check(ctx context.Context, p *ModerationPolicy, phase string, messages []ChatMessage, text string) ModerationDecision {
	if e == nil || p.Mode != e.policy.Mode || p.ProviderMode != e.providerMode || p.AdapterIdentity != e.policy.AdapterIdentity {
		return moderationReview("policy_unavailable")
	}
	if ctx.Err() != nil {
		return moderationReview("check_unavailable")
	}
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		return moderationReview("check_busy")
	}
	if p.Mode == "http" {
		return e.http.check(ctx, p, phase, messages, text)
	}
	result := ModerationDecision{State: "ALLOWED", ReasonCode: "allowed"}
	for _, rule := range p.Rules {
		if rule.Phase != phase && rule.Phase != "both" {
			continue
		}
		match := strings.Contains(text, rule.Contains)
		for _, message := range messages {
			match = match || strings.Contains(message.Content, rule.Contains)
		}
		if !match {
			continue
		}
		if rule.Decision == "block" {
			return ModerationDecision{State: "BLOCKED", ReasonCode: "policy_block"}
		}
		result = moderationReview("policy_review")
	}
	if ctx.Err() != nil {
		return moderationReview("check_unavailable")
	}
	return result
}

func validModerationSummary(s *ModerationSummary) bool {
	if s == nil || !moderationNamePattern.MatchString(s.PolicyID) || !moderationNamePattern.MatchString(s.PolicyVersion) || s.Phase != "output" || s.Version < 1 || s.Version > 9007199254740991 {
		return false
	}
	switch s.State {
	case "PENDING":
		return s.ReasonCode == ""
	case "ALLOWED":
		return s.ReasonCode == "allowed"
	case "BLOCKED":
		return s.ReasonCode == "policy_block"
	case "REVIEW":
		switch s.ReasonCode {
		case "policy_review", "check_unavailable", "check_protocol", "check_timeout", "check_busy", "invalid_content", "content_too_large", "policy_unavailable", "generation_incomplete", "execution_incomplete", "process_restarted", "cancelled":
			return true
		}
	}
	return false
}

// The caller creates the Run first and commits these rows with the acceptance,
// reservation, conversation and Outbox. No candidate output is persisted here.
func insertRunModerationSQL(ctx context.Context, tx pgx.Tx, run Run) error {
	p, s := run.ModerationPolicy, run.Moderation
	if p == nil && s == nil {
		return nil
	}
	if !validModerationPolicy(p) || !validModerationSummary(s) || s.State != "PENDING" || s.Version != 1 || s.PolicyID != p.PolicyID || s.PolicyVersion != p.PolicyVersion || run.ProviderMode != p.ProviderMode {
		return failure(409, "MODERATION_POLICY_INVALID", "审核政策快照与受理状态不一致")
	}
	raw, _ := json.Marshal(p)
	if _, err := tx.Exec(ctx, `INSERT INTO studio_run_moderation(run_id,policy_id,policy_version,policy_digest,policy,state,version,reason_code) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, run.ID, p.PolicyID, p.PolicyVersion, p.Digest, raw, s.State, s.Version, s.ReasonCode); err != nil {
		return err
	}
	return insertModerationDecisionSQL(ctx, tx, run.ID, *s)
}
func saveRunModerationSQL(ctx context.Context, tx pgx.Tx, run Run) error {
	var policyID, policyVersion, policyDigest, state, reason string
	var version uint64
	err := tx.QueryRow(ctx, `SELECT policy_id,policy_version,policy_digest,state,version,reason_code FROM studio_run_moderation WHERE run_id=$1 FOR UPDATE`, run.ID).Scan(&policyID, &policyVersion, &policyDigest, &state, &version, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		if run.Moderation == nil && run.ModerationPolicy == nil {
			return nil
		}
		return failure(409, "MODERATION_POLICY_MISSING", "原审核政策不存在，不能发布内容")
	}
	if err != nil {
		return err
	}
	s := run.Moderation
	if !validModerationSummary(s) || s.PolicyID != policyID || s.PolicyVersion != policyVersion {
		return failure(409, "MODERATION_POLICY_INVALID", "审核状态与原政策不一致")
	}
	if p := run.ModerationPolicy; p != nil && (!validModerationPolicy(p) || p.Digest != policyDigest) {
		return failure(409, "MODERATION_POLICY_INVALID", "不得覆盖原审核政策")
	}
	if s.Version == version && s.State == state && s.ReasonCode == reason {
		return nil
	}
	if state != "PENDING" || s.State == "PENDING" || s.Version != version+1 {
		return failure(409, "MODERATION_STATE_CHANGED", "审核结果已确定或版本不一致")
	}
	tag, err := tx.Exec(ctx, `UPDATE studio_run_moderation SET state=$2,version=$3,reason_code=$4,updated_at=clock_timestamp() WHERE run_id=$1 AND version=$5 AND state='PENDING'`, run.ID, s.State, s.Version, s.ReasonCode, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return failure(409, "MODERATION_STATE_CHANGED", "审核状态已变化")
	}
	return insertModerationDecisionSQL(ctx, tx, run.ID, *s)
}
func insertModerationDecisionSQL(ctx context.Context, tx pgx.Tx, runID string, s ModerationSummary) error {
	_, err := tx.Exec(ctx, `INSERT INTO studio_moderation_decisions(run_id,version,state,reason_code) VALUES($1,$2,$3,$4)`, runID, s.Version, s.State, s.ReasonCode)
	return err
}
