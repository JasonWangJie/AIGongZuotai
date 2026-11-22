package studio

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type WorkflowGraph struct {
	Name            string         `json:"name"`
	TemplateID      string         `json:"template_id"`
	TemplateVersion string         `json:"template_version"`
	Nodes           []WorkflowNode `json:"nodes"`
	Edges           []WorkflowEdge `json:"edges"`
}

type WorkflowNode struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Title        string          `json:"title"`
	Prompt       string          `json:"prompt,omitempty"`
	ModelID      string          `json:"model_id,omitempty"`
	ModelVersion string          `json:"model_version,omitempty"`
	Parameters   *TaskParameters `json:"parameters,omitempty"`
}

type WorkflowEdge struct {
	From     string `json:"from"`
	FromPort string `json:"from_port"`
	To       string `json:"to"`
	ToPort   string `json:"to_port"`
}

type WorkflowDefinition struct {
	ID        string        `json:"id"`
	ProjectID string        `json:"project_id"`
	Version   uint64        `json:"version"`
	Graph     WorkflowGraph `json:"graph"`
	CreatedAt time.Time     `json:"created_at"`
}

type WorkflowCandidate struct {
	AssetID        string `json:"asset_id"`
	ArtifactID     string `json:"artifact_id"`
	TaskID         string `json:"task_id"`
	Revision       uint64 `json:"revision"`
	ContentVersion string `json:"content_version"`
}

type WorkflowExecution struct {
	ID                string              `json:"id"`
	SourceWorkflowID  string              `json:"source_workflow_id,omitempty"`
	SourceVersion     uint64              `json:"source_version,omitempty"`
	FromNode          string              `json:"from_node,omitempty"`
	Attempt           uint64              `json:"attempt"`
	ProjectID         string              `json:"project_id"`
	DefinitionID      string              `json:"definition_id"`
	DefinitionVersion uint64              `json:"definition_version"`
	Graph             WorkflowGraph       `json:"graph"`
	Input             string              `json:"input"`
	FrozenModels      map[string]Model    `json:"frozen_models"`
	ProviderMode      string              `json:"provider_mode"`
	Version           uint64              `json:"version"`
	Status            string              `json:"status"`
	TextRunID         string              `json:"text_run_id,omitempty"`
	ImageTaskID       string              `json:"image_task_id,omitempty"`
	VideoTaskID       string              `json:"video_task_id,omitempty"`
	VideoArtifacts    []WorkflowCandidate `json:"video_artifacts,omitempty"`
	Candidates        []WorkflowCandidate `json:"candidates"`
	Selected          *WorkflowCandidate  `json:"selected,omitempty"`
	ErrorCode         string              `json:"error_code,omitempty"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
}

// Keys and frozen child request bodies are internal to admission.
type WorkflowIntent struct {
	Key             string     `json:"-"`
	ExpectedVersion uint64     `json:"-"`
	ActionKey       string     `json:"-"`
	Run             *RunInput  `json:"-"`
	Task            *TaskInput `json:"-"`
}

var workflowNodeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)
var workflowModelVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$`)
var workflowKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

func workflowText(s string, limit int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && len(s) <= limit
}

func (g WorkflowGraph) hasVideo() bool {
	return g.TemplateID == "text-image-approval-video" && g.TemplateVersion == "2"
}

func validateWorkflowGraph(g WorkflowGraph) error {
	invalid := func(message string) error { return failure(422, "INVALID_WORKFLOW_GRAPH", message) }
	video := g.hasVideo()
	if !video && (g.TemplateID != "text-image-approval" || g.TemplateVersion != "1") {
		return invalid("模板或模板版本不受支持")
	}
	nodes, edges := 3, 2
	if video {
		nodes, edges = 4, 4
	}
	if strings.TrimSpace(g.Name) == "" || !workflowText(g.Name, 160) || len(g.Nodes) != nodes || len(g.Edges) != edges {
		return invalid("须提供名称及本模板规定的完整节点与连接")
	}
	if encoded, err := json.Marshal(g); err != nil || len(encoded) > 24<<10 {
		return invalid("流程定义超过 24 KiB")
	}
	byKind := map[string]string{}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		if !workflowNodeID.MatchString(n.ID) || ids[n.ID] || byKind[n.Kind] != "" {
			return invalid("节点 ID、类型必须有效且唯一")
		}
		ids[n.ID], byKind[n.Kind] = true, n.ID
		if strings.TrimSpace(n.Title) == "" || !workflowText(n.Title, 160) || !workflowText(n.Prompt, 4096) {
			return invalid("节点标题或提示词无效")
		}
		switch n.Kind {
		case "text", "image", "video":
			if !catalogID.MatchString(n.ModelID) || !workflowModelVersion.MatchString(n.ModelVersion) {
				return invalid("生成节点须绑定模型 ID 和版本")
			}
			if n.Kind == "text" && n.Parameters != nil {
				return invalid("文本节点不接受图片参数")
			}
			if n.Kind == "video" && (!video || n.Parameters == nil || n.Parameters.Count != 1 || n.Parameters.Seconds < 1 || n.Parameters.Seconds > 600 || !catalogRatio.MatchString(n.Parameters.AspectRatio) ||
				(n.Parameters.Size != "" && !catalogParameter.MatchString(n.Parameters.Size)) || (n.Parameters.Resolution != "" && !catalogParameter.MatchString(n.Parameters.Resolution))) {
				return invalid("视频节点须绑定一种首帧视频规格及一个输出")
			}
			if n.Kind == "image" {
				p := n.Parameters
				if p == nil || p.Count < 1 || p.Count > 4 || p.Seconds != 0 || !catalogRatio.MatchString(p.AspectRatio) ||
					(p.Size != "" && !catalogParameter.MatchString(p.Size)) || (p.Resolution != "" && !catalogParameter.MatchString(p.Resolution)) {
					return invalid("图片节点须提供有效的生成规格，数量为 1 至 4")
				}
			}
		case "approval":
			if n.Prompt != "" || n.Parameters != nil || n.ModelID != "" || n.ModelVersion != "" {
				return invalid("确认节点不能执行生成请求")
			}
		default:
			return invalid("节点类型不属于模板")
		}
	}
	required := map[WorkflowEdge]bool{
		{From: byKind["text"], FromPort: "text", To: byKind["image"], ToPort: "prompt"}:       true,
		{From: byKind["image"], FromPort: "images", To: byKind["approval"], ToPort: "images"}: true,
	}
	if video {
		required[WorkflowEdge{byKind["approval"], "selected_image", byKind["video"], "first_frame"}] = true
		required[WorkflowEdge{byKind["text"], "text", byKind["video"], "prompt"}] = true
	}
	for _, edge := range g.Edges {
		if !required[edge] {
			return invalid("连接重复、端口不匹配或依赖不属于支持的无环模板")
		}
		delete(required, edge)
	}
	return nil
}

func workflowMissing() error {
	return failure(404, "WORKFLOW_NOT_FOUND", "工作流记录不存在或不可访问")
}

func workflowConflict() error {
	return failure(409, "WORKFLOW_CONFLICT", "流程版本或节点状态已变化，请读取当前运行")
}
