package studio

import (
	"encoding/json"
	"time"
)

const canvasMaxRevision = 64
const canvasMaxBytes = 2 << 20

type CanvasNode struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Note          string `json:"note,omitempty"`
	X             int    `json:"x"`
	Y             int    `json:"y"`
	GroupID       string `json:"group_id,omitempty"`
	AssetID       string `json:"asset_id,omitempty"`
	AssetRevision uint64 `json:"asset_revision,omitempty"`
	RunID         string `json:"run_id,omitempty"`
}
type CanvasEdge struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}
type CanvasGroup struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Collapsed bool   `json:"collapsed"`
}
type CanvasDocument struct {
	Title  string        `json:"title"`
	Nodes  []CanvasNode  `json:"nodes"`
	Edges  []CanvasEdge  `json:"edges"`
	Groups []CanvasGroup `json:"groups"`
}
type CanvasSaveInput struct {
	ExpectedRevision *uint64        `json:"expected_revision"`
	Document         CanvasDocument `json:"document"`
}
type CanvasSource struct {
	Available      bool    `json:"available"`
	ContentVersion string  `json:"content_version,omitempty"`
	ThumbnailURL   string  `json:"thumbnail_url,omitempty"`
	Status         string  `json:"status,omitempty"`
	Seq            *uint64 `json:"seq,omitempty"`
}
type CanvasSources struct {
	ProjectID string                  `json:"project_id"`
	Revision  uint64                  `json:"revision"`
	Sources   map[string]CanvasSource `json:"sources"`
}
type CanvasVersion struct {
	CanvasSources
	Document  CanvasDocument `json:"document"`
	CreatedAt time.Time      `json:"created_at"`
}
type CanvasSummary struct {
	Revision  uint64    `json:"revision"`
	Title     string    `json:"title"`
	NodeCount int       `json:"node_count"`
	EdgeCount int       `json:"edge_count"`
	CreatedAt time.Time `json:"created_at"`
}
type CanvasHistory struct {
	Items              []CanvasSummary `json:"items"`
	NextBeforeRevision uint64          `json:"next_before_revision,omitempty"`
}

func canvasMissing() error {
	return failure(404, "CANVAS_NOT_FOUND", "当前项目没有这份画布、修订、来源或保存回执")
}
func canvasUnavailable() error {
	return failure(409, "CANVAS_SOURCE_UNAVAILABLE", "画布来源不在本项目、版本已改变或尚未获准公开")
}
func canvasInvalid() error {
	return failure(422, "INVALID_CANVAS", "请核对画布标题、坐标、分组、连线与来源")
}
func validateCanvas(in CanvasSaveInput) error {
	d := in.Document
	if in.ExpectedRevision == nil || *in.ExpectedRevision > canvasMaxRevision || !validPresentationText(d.Title, 120, true) || d.Nodes == nil || len(d.Nodes) > 2000 || d.Edges == nil || len(d.Edges) > 4000 || d.Groups == nil || len(d.Groups) > 128 {
		return canvasInvalid()
	}
	groups := map[string]bool{}
	nodes := map[string]bool{}
	edges := map[string]bool{}
	links := map[[2]string]bool{}
	for _, g := range d.Groups {
		if !presentationSlideID.MatchString(g.ID) || groups[g.ID] || !validPresentationText(g.Title, 120, true) {
			return canvasInvalid()
		}
		groups[g.ID] = true
	}
	images, runs := 0, 0
	for _, n := range d.Nodes {
		if !presentationSlideID.MatchString(n.ID) || nodes[n.ID] || !validPresentationText(n.Title, 120, true) || !validPresentationText(n.Note, 600, false) || n.X < -1000000 || n.X > 1000000 || n.Y < -1000000 || n.Y > 1000000 || n.GroupID != "" && !groups[n.GroupID] {
			return canvasInvalid()
		}
		if n.AssetID != "" {
			if !libraryIDPattern.MatchString(n.AssetID) || n.AssetRevision == 0 || n.AssetRevision > libraryRevisionLimit || n.RunID != "" {
				return canvasInvalid()
			}
			images++
		} else if n.AssetRevision != 0 {
			return canvasInvalid()
		}
		if n.RunID != "" {
			if !validOperationsID(n.RunID, "run") {
				return canvasInvalid()
			}
			runs++
		}
		nodes[n.ID] = true
	}
	if images > 16 || runs > 16 {
		return failure(422, "CANVAS_SOURCE_LIMIT", "每版最多绑定 16 个图片节点和 16 个运行节点")
	}
	for _, e := range d.Edges {
		pair := [2]string{e.From, e.To}
		if !presentationSlideID.MatchString(e.ID) || edges[e.ID] || !nodes[e.From] || !nodes[e.To] || e.From == e.To || links[pair] {
			return canvasInvalid()
		}
		edges[e.ID] = true
		links[pair] = true
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > canvasMaxBytes {
		return failure(413, "CANVAS_BODY_TOO_LARGE", "画布内容上限为 2 MiB")
	}
	return nil
}
