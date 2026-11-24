package studio

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestSourceVideoContainerAndLocalTrack(t *testing.T) {
	original, err := os.ReadFile("testdata/moderation/allow.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err = checkSourceMP4(bytes.NewReader(original), int64(len(original))); err != nil {
		t.Fatal(err)
	}
	mutate := func(tag, replacement string, offset int) []byte {
		t.Helper()
		pos := bytes.Index(original, []byte(tag))
		if pos < 0 {
			t.Fatalf("missing fixture box %s", tag)
		}
		out := bytes.Clone(original)
		copy(out[pos+offset:], replacement)
		return out
	}
	for name, contents := range map[string][]byte{
		"header alone": original[:32], "truncated": original[:len(original)-1],
		"external data":        mutate("url ", "\x00\x00\x00\x00", 4),
		"missing reference":    mutate("dref", "free", 0),
		"no video description": mutate("stsd", "free", 0),
		"unknown media extent": mutate("mdat", "free", 0),
		"wrong entry count":    mutate("stsd", "\xff\xff\xff\xff", 8),
	} {
		t.Run(name, func(t *testing.T) {
			if checkSourceMP4(bytes.NewReader(contents), int64(len(contents))) == nil {
				t.Fatal("accepted invalid source")
			}
		})
	}
}

func TestSourceVideoExplicitCatalogAndRequestDomains(t *testing.T) {
	base := defaultCatalogConfig(Config{Mode: "demo"}).Models[2]
	legacy := contentVersion(base)
	caps := &SourceVideoCapabilities{EditPresets: []string{"preset-1"}, ExtendPresets: []string{"preset-2"}, Directions: []string{"backward"}}
	base.Capabilities.SourceVideo = caps
	if err := validateCatalogModel(base); err != nil {
		t.Fatal(err)
	}
	if contentVersion(base) == legacy {
		t.Fatal("operation capabilities did not change model version")
	}
	for _, change := range []func(*SourceVideoCapabilities){
		func(c *SourceVideoCapabilities) { c.EditPresets = []string{"absent"} },
		func(c *SourceVideoCapabilities) { c.ExtendPresets = nil },
		func(c *SourceVideoCapabilities) { c.Directions = []string{"a:b"} },
		func(c *SourceVideoCapabilities) { c.Directions = []string{"backward", "backward"} },
	} {
		copy := caps.clone()
		change(copy)
		if copy.validate("video", base.Capabilities.ParameterPresets) == nil {
			t.Fatal("accepted incompatible capabilities")
		}
	}
	if caps.validate("image", base.Capabilities.ParameterPresets) == nil {
		t.Fatal("image advertised source video")
	}
	m := Model{Capabilities: ModelCapabilities{SourceVideo: caps, ParameterPresets: base.Capabilities.ParameterPresets}}
	input := taskInput("video")
	input.Operation, input.ModelVersion = "video_edit", "v239"
	input.Parameters.Seconds = m.Capabilities.ParameterPresets[0].Seconds
	input.Inputs.SourceVideo = &SourceVideoRef{AssetID: "lib_" + strings.Repeat("a", 64), AssetRevision: 1}
	if _, err := taskOperation(input); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceVideoPreset(input, m); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*TaskInput){
		func(i *TaskInput) { i.Operation = "generate" }, func(i *TaskInput) { i.Inputs.SourceVideo = nil },
		func(i *TaskInput) { i.Inputs.ExtensionDirection = "backward" }, func(i *TaskInput) { i.Parameters.Count = 2 },
		func(i *TaskInput) { i.Inputs.ReferenceAssetIDs = []string{"ref"} }, func(i *TaskInput) { i.WorkflowAttemptID = "other" },
	} {
		copy := input
		change(&copy)
		if _, err := taskOperation(copy); err == nil {
			t.Fatal("accepted mismatched input domain")
		}
	}
	input.Operation, input.Inputs.ExtensionDirection = "video_extend", "backward"
	if validateSourceVideoPreset(input, m) == nil {
		t.Fatal("edit-only preset accepted for extend")
	}
	input.Parameters.AspectRatio = m.Capabilities.ParameterPresets[1].AspectRatio
	if err := validateSourceVideoPreset(input, m); err != nil {
		t.Fatal(err)
	}
	frozen := payloadHash(input)
	next := *input.Inputs.SourceVideo
	next.AssetRevision++
	input.Inputs.SourceVideo = &next
	if payloadHash(input) == frozen {
		t.Fatal("revision did not change the payload hash")
	}
}
