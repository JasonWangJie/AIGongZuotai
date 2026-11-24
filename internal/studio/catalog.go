package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

type ParameterPreset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AspectRatio string `json:"aspect_ratio"`
	Size        string `json:"size,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	Seconds     int    `json:"seconds,omitempty"`
}

func (p ParameterPreset) matches(parameters TaskParameters) bool {
	return p.AspectRatio == parameters.AspectRatio && p.Size == parameters.Size &&
		p.Resolution == parameters.Resolution && p.Seconds == parameters.Seconds
}

type ModelCapabilities struct {
	ContextWindowTokens      int                      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens          int                      `json:"max_output_tokens,omitempty"`
	ImageEditSupported       bool                     `json:"image_edit_supported"`
	VideoFirstFrameSupported bool                     `json:"video_first_frame_supported,omitempty"`
	SourceVideo              *SourceVideoCapabilities `json:"source_video,omitempty"`
	MaxReferenceImages       int                      `json:"max_reference_images"`
	CancelSupported          bool                     `json:"cancel_supported"`
	MaxCount                 int                      `json:"max_count"`
	MaxPromptBytes           int                      `json:"max_prompt_bytes"`
	ParameterPresets         []ParameterPreset        `json:"parameter_presets"`
}
type Model struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Kind               string            `json:"kind"`
	Description        string            `json:"description"`
	BuiltinDescription bool              `json:"builtin_description,omitempty"`
	Enabled            bool              `json:"enabled"`
	Reason             string            `json:"reason,omitempty"`
	Version            string            `json:"version"`
	Capabilities       ModelCapabilities `json:"capabilities"`
}
type Catalog struct {
	Version string  `json:"version"`
	Models  []Model `json:"models"`
}
type catalogEntry struct {
	Model                Model
	UpstreamModel        string
	ChatOutputLimitField string
}
type modelCatalog struct {
	Public  Catalog
	Entries map[string]catalogEntry
	JSON    []byte
}
type catalogFile struct {
	SchemaVersion int                  `json:"schema_version"`
	Models        []catalogConfigModel `json:"models"`
}
type catalogConfigModel struct {
	ChatOutputLimitField string                    `json:"chat_output_limit_field,omitempty"`
	ID                   string                    `json:"id"`
	Name                 string                    `json:"name"`
	Kind                 string                    `json:"kind"`
	Description          string                    `json:"description"`
	Enabled              *bool                     `json:"enabled"`
	UpstreamModel        string                    `json:"upstream_model"`
	Capabilities         catalogConfigCapabilities `json:"capabilities"`
}
type catalogConfigCapabilities struct {
	ContextWindowTokens      int                      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens          int                      `json:"max_output_tokens,omitempty"`
	ImageEditSupported       bool                     `json:"image_edit_supported,omitempty"`
	VideoFirstFrameSupported bool                     `json:"video_first_frame_supported,omitempty"`
	SourceVideo              *SourceVideoCapabilities `json:"source_video,omitempty"`
	MaxReferenceImages       int                      `json:"max_reference_images,omitempty"`
	MaxCount                 int                      `json:"max_count"`
	MaxPromptBytes           int                      `json:"max_prompt_bytes"`
	ParameterPresets         []ParameterPreset        `json:"parameter_presets"`
}

var catalogID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,63}$`)
var catalogRatio = regexp.MustCompile(`^[1-9][0-9]{0,3}:[1-9][0-9]{0,3}$`)
var catalogParameter = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,31}$`)

func loadModelCatalog(c Config) (*modelCatalog, error) {
	if c.ModelCatalogFile == "" && c.Mode == "live" && c.AsyncVideoModel != "" && (c.AsyncVideoSeconds < 1 || c.AsyncVideoSeconds > 600) {
		return nil, errors.New("legacy live video configuration requires explicit ASYNC_VIDEO_SECONDS between 1 and 600")
	}
	config := defaultCatalogConfig(c)
	if c.ModelCatalogFile != "" {
		file, err := os.Open(c.ModelCatalogFile)
		if err != nil {
			return nil, errors.New("MODEL_CATALOG_FILE cannot be opened")
		}
		defer file.Close()
		blob, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
		if err != nil || len(blob) > 64<<10 || !utf8.Valid(blob) {
			return nil, errors.New("MODEL_CATALOG_FILE must contain at most 64 KiB of UTF-8 JSON")
		}
		config = catalogFile{}
		if err := validateCatalogJSONKeys(blob); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(blob))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return nil, errors.New("MODEL_CATALOG_FILE has invalid JSON or unknown fields")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, errors.New("MODEL_CATALOG_FILE must contain one JSON object")
		}
	}
	if config.SchemaVersion != 1 || len(config.Models) < 1 || len(config.Models) > 32 {
		return nil, errors.New("model catalog requires schema_version 1 and 1 to 32 models")
	}
	result := &modelCatalog{Public: Catalog{Models: []Model{}}, Entries: make(map[string]catalogEntry)}
	for _, raw := range config.Models {
		if raw.Kind == "chat" {
			if raw.Capabilities.ContextWindowTokens == 0 {
				raw.Capabilities.ContextWindowTokens = DefaultChatContextWindowTokens
			}
			if raw.Capabilities.MaxOutputTokens == 0 {
				raw.Capabilities.MaxOutputTokens = DefaultChatMaxOutputTokens
			}
			if raw.ChatOutputLimitField == "" {
				raw.ChatOutputLimitField = "max_tokens"
			}
		}
		if err := validateCatalogModel(raw); err != nil {
			return nil, err
		}
		if _, duplicate := result.Entries[raw.ID]; duplicate {
			return nil, errors.New("duplicate public model ID")
		}
		model := Model{ID: raw.ID, Name: raw.Name, Kind: raw.Kind, Description: raw.Description, Enabled: *raw.Enabled,
			Capabilities: ModelCapabilities{ContextWindowTokens: raw.Capabilities.ContextWindowTokens, MaxOutputTokens: raw.Capabilities.MaxOutputTokens, ImageEditSupported: raw.Capabilities.ImageEditSupported, MaxReferenceImages: raw.Capabilities.MaxReferenceImages, CancelSupported: raw.Kind == "chat" || c.Mode == "demo", MaxCount: raw.Capabilities.MaxCount, MaxPromptBytes: raw.Capabilities.MaxPromptBytes, ParameterPresets: append([]ParameterPreset{}, raw.Capabilities.ParameterPresets...)}}
		if !model.Enabled {
			model.Reason = "disabled_by_operator"
		} else if c.Mode == "live" && ((raw.Kind == "chat" && (c.BaseURL == "" || c.APIKey == "")) || (raw.Kind != "chat" && (c.AsyncImageBaseURL == "" || c.AsyncImageAPIKey == ""))) {
			model.Enabled = false
			model.Reason = "provider_not_configured"
		}
		model.Capabilities.VideoFirstFrameSupported = raw.Capabilities.VideoFirstFrameSupported
		model.Capabilities.SourceVideo = raw.Capabilities.SourceVideo.clone()
		// The private binding affects the opaque version but is never serialized
		// in the public catalog. Credentials and provider URLs are not hashed here.
		model.Version = contentVersion(struct {
			Model                Model
			Binding              string
			ChatOutputLimitField string `json:",omitempty"`
		}{model, raw.UpstreamModel, raw.ChatOutputLimitField})
		result.Entries[model.ID] = catalogEntry{Model: model, UpstreamModel: raw.UpstreamModel, ChatOutputLimitField: raw.ChatOutputLimitField}
		// Display provenance belongs to the public catalog, outside execution snapshots.
		model.BuiltinDescription = c.ModelCatalogFile == ""
		result.Public.Models = append(result.Public.Models, model)
	}
	result.Public.Version = contentVersion(result.Public.Models)
	result.JSON, _ = json.Marshal(result.Public)
	return result, nil
}

func validateCatalogJSONKeys(blob []byte) error {
	d := json.NewDecoder(bytes.NewReader(blob))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 16 {
			return errors.New("model catalog JSON nesting exceeds limit")
		}
		token, err := d.Token()
		if err != nil {
			return errors.New("invalid model catalog JSON")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return errors.New("model catalog JSON contains duplicate or invalid keys")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid model catalog JSON")
		}
		_, err = d.Token()
		return err
	}
	return value(0)
}
func contentVersion(value any) string {
	blob, _ := json.Marshal(value)
	sum := sha256.Sum256(blob)
	return "v1_" + hex.EncodeToString(sum[:])
}

func validateCatalogModel(m catalogConfigModel) error {
	invalid := func(message string) error { return fmt.Errorf("invalid catalog model %q: %s", m.ID, message) }
	if !catalogID.MatchString(m.ID) || m.Enabled == nil || strings.TrimSpace(m.Name) == "" || utf8.RuneCountInString(m.Name) > 80 || utf8.RuneCountInString(m.Description) > 500 || strings.ContainsAny(m.Name+m.Description, "\x00\r\n") {
		return invalid("ID, explicit enabled, name or description is invalid")
	}
	if m.Kind != "chat" && m.Kind != "image" && m.Kind != "video" {
		return invalid("kind must be chat, image or video")
	}
	for _, kind := range []string{"chat", "image", "video"} {
		if m.ID == "studio-"+kind && m.Kind != kind {
			return invalid("reserved studio model ID has a fixed kind")
		}
	}
	if strings.TrimSpace(m.UpstreamModel) == "" || len(m.UpstreamModel) > 128 || strings.ContainsAny(m.UpstreamModel, "\x00\r\n") {
		return invalid("upstream_model must contain 1 to 128 safe bytes")
	}
	cap := m.Capabilities
	if err := cap.SourceVideo.validate(m.Kind, cap.ParameterPresets); err != nil {
		return invalid(err.Error())
	}
	if cap.VideoFirstFrameSupported && m.Kind != "video" {
		return invalid("video_first_frame_supported requires a video model")
	}
	if m.Kind == "chat" {
		if cap.ContextWindowTokens < minChatContextWindowTokens || cap.ContextWindowTokens > maxChatContextWindowTokens || cap.MaxOutputTokens < minChatOutputTokens || cap.MaxOutputTokens > maxChatOutputTokens || cap.MaxOutputTokens+ChatSafetyReserve+ChatMessageOverhead >= cap.ContextWindowTokens || !validChatOutputLimitField(m.ChatOutputLimitField) {
			return invalid("chat window/output budget or output-limit field is invalid")
		}
	} else if cap.ContextWindowTokens != 0 || cap.MaxOutputTokens != 0 || m.ChatOutputLimitField != "" {
		return invalid("media models cannot contain chat context or output-limit settings")
	}
	if cap.ImageEditSupported && (m.Kind != "image" || cap.MaxReferenceImages < 1 || cap.MaxReferenceImages > MaxReferenceFiles) || !cap.ImageEditSupported && cap.MaxReferenceImages != 0 {
		return invalid("image editing requires image kind and 1 to 4 references; disabled editing requires zero references")
	}
	if cap.MaxPromptBytes < 1 || cap.MaxPromptBytes > 16000 || cap.MaxCount < 1 || cap.MaxCount > 4 || (m.Kind != "image" && cap.MaxCount != 1) {
		return invalid("prompt/count limits exceed the supported protocol")
	}
	if (m.Kind == "chat" && len(cap.ParameterPresets) != 0) || (m.Kind != "chat" && (len(cap.ParameterPresets) < 1 || len(cap.ParameterPresets) > 32)) {
		return invalid("chat requires no presets; media requires 1 to 32 presets")
	}
	seenIDs, seenValues := map[string]bool{}, map[string]bool{}
	for _, p := range cap.ParameterPresets {
		if !catalogID.MatchString(p.ID) || seenIDs[p.ID] || strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 80 || strings.ContainsAny(p.Name, "\x00\r\n") || !catalogRatio.MatchString(p.AspectRatio) {
			return invalid("preset ID/name/aspect ratio is invalid or duplicated")
		}
		if p.Size != "" && !catalogParameter.MatchString(p.Size) || p.Resolution != "" && !catalogParameter.MatchString(p.Resolution) {
			return invalid("preset size or resolution is invalid")
		}
		if m.Kind == "image" && p.Seconds != 0 || m.Kind == "video" && (p.Seconds < 1 || p.Seconds > 600) {
			return invalid("image/video preset fields are incompatible")
		}
		values := fmt.Sprintf("%s|%s|%s|%d", p.AspectRatio, p.Size, p.Resolution, p.Seconds)
		if seenValues[values] {
			return invalid("duplicate effective parameter preset")
		}
		seenIDs[p.ID] = true
		seenValues[values] = true
	}
	return nil
}

func defaultCatalogConfig(c Config) catalogFile {
	enabled := true
	models := []catalogConfigModel{}
	for _, kind := range []string{"chat", "image", "video"} {
		upstream := map[string]string{"chat": c.ChatModel, "image": c.AsyncImageModel, "video": c.AsyncVideoModel}[kind]
		if upstream == "" {
			upstream = "demo-" + kind
		}
		m := catalogConfigModel{ID: "studio-" + kind, Name: "Studio " + strings.ToUpper(kind[:1]) + kind[1:], Kind: kind, Description: "本地演示模型 · 不调用真实 AI", Enabled: &enabled, UpstreamModel: upstream, Capabilities: catalogConfigCapabilities{MaxCount: 1, MaxPromptBytes: 16000, ParameterPresets: []ParameterPreset{}}}
		if c.Mode == "live" {
			m.Description = "已配置的服务端模型 · 请求可能计费"
			missing := map[string]string{"chat": c.ChatModel, "image": c.AsyncImageModel, "video": c.AsyncVideoModel}[kind] == ""
			if missing {
				off := false
				m.Enabled = &off
			}
		}
		if kind == "image" {
			m.Capabilities.MaxCount = 4
			if c.Mode == "demo" {
				m.Capabilities.ImageEditSupported = true
				m.Capabilities.MaxReferenceImages = MaxReferenceFiles
			}
		}
		if kind != "chat" {
			for i, ratio := range []string{"16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"} {
				p := ParameterPreset{ID: fmt.Sprintf("preset-%d", i+1), Name: ratio, AspectRatio: ratio}
				if kind == "video" {
					p.Seconds = c.AsyncVideoSeconds
					if p.Seconds == 0 {
						p.Seconds = 5
					}
					p.Resolution = c.AsyncVideoResolution
				}
				m.Capabilities.ParameterPresets = append(m.Capabilities.ParameterPresets, p)
			}
		}
		models = append(models, m)
	}
	return catalogFile{SchemaVersion: 1, Models: models}
}

func (a *App) resolveModel(id, kind, version, prompt string) (catalogEntry, error) {
	entry, ok := a.catalog.Entries[id]
	if !ok || entry.Model.Kind != kind {
		return catalogEntry{}, failure(422, "MODEL_NOT_FOUND", "模型不存在或类型不匹配")
	}
	if version != "" && version != entry.Model.Version {
		return catalogEntry{}, failure(409, "MODEL_VERSION_CHANGED", "模型能力已更新；本次未受理，请刷新目录后确认参数")
	}
	if !entry.Model.Enabled {
		return catalogEntry{}, failure(422, "MODEL_DISABLED", "当前模型不可用于新生成；已有任务可继续查询")
	}
	if !validatePrompt(prompt) || len(prompt) > entry.Model.Capabilities.MaxPromptBytes {
		return catalogEntry{}, failure(422, "INVALID_MODEL_PARAMETERS", "提示词超过当前模型支持范围")
	}
	return entry, nil
}

func resolveTaskParameters(input TaskParameters, model Model) (TaskParameters, error) {
	if input.Count == 0 {
		input.Count = 1
	}
	if input.Count < 1 || input.Count > model.Capabilities.MaxCount {
		return TaskParameters{}, failure(422, "INVALID_MODEL_PARAMETERS", "数量超出当前模型支持范围")
	}
	for _, p := range model.Capabilities.ParameterPresets {
		if p.matches(input) {
			return input, nil
		}
	}
	// Fill only omitted values from one exact preset; never combine the first
	// size of one preset with the resolution/duration of another.
	for _, p := range model.Capabilities.ParameterPresets {
		if input.AspectRatio != "" && input.AspectRatio != p.AspectRatio || input.Size != "" && input.Size != p.Size || input.Resolution != "" && input.Resolution != p.Resolution || input.Seconds != 0 && input.Seconds != p.Seconds {
			continue
		}
		return TaskParameters{AspectRatio: p.AspectRatio, Count: input.Count, Size: p.Size, Resolution: p.Resolution, Seconds: p.Seconds}, nil
	}
	return TaskParameters{}, failure(422, "INVALID_MODEL_PARAMETERS", "参数组合不属于当前模型支持的预设")
}

func legacyTaskInput(input TaskInput) TaskInput {
	if input.Parameters.Count == 0 {
		input.Parameters.Count = 1
	}
	if input.Parameters.AspectRatio == "" {
		input.Parameters.AspectRatio = "16:9"
	}
	return input
}
func (a *App) legacyCapabilities() map[string]any {
	result := map[string]any{}
	for _, kind := range []string{"chat", "image", "video"} {
		enabled, maxCount := false, 1
		ratios := []string{}
		seen := map[string]bool{}
		for _, m := range a.catalog.Public.Models {
			if m.Kind != kind {
				continue
			}
			enabled = enabled || m.Enabled
			maxCount = max(maxCount, m.Capabilities.MaxCount)
			for _, p := range m.Capabilities.ParameterPresets {
				if !seen[p.AspectRatio] {
					ratios = append(ratios, p.AspectRatio)
					seen[p.AspectRatio] = true
				}
			}
		}
		result[kind] = map[string]any{"enabled": enabled, "cancel_supported": kind == "chat" || a.config.Mode == "demo", "max_count": maxCount, "aspect_ratios": ratios}
	}
	return result
}
func (a *App) modelsHTTP(w http.ResponseWriter, r *http.Request) {
	etag := `"` + a.catalog.Public.Version + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == etag || candidate == "W/"+etag || candidate == "*" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(a.catalog.JSON)
}
