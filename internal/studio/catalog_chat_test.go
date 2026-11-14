package studio

import (
	"bytes"
	"testing"
)

func TestCatalogChatBudgetDefaultsPrivacyAndVersion(t *testing.T) {
	config := customCatalogFixture()
	path := writeCatalogFixture(t, config)
	load := func() *modelCatalog {
		t.Helper()
		catalog, err := loadModelCatalog(Config{Mode: "demo", ModelCatalogFile: path})
		if err != nil {
			t.Fatal(err)
		}
		return catalog
	}
	base := load()
	chat := base.Entries["custom-chat"]
	if chat.Model.Capabilities.ContextWindowTokens != 32768 || chat.Model.Capabilities.MaxOutputTokens != 4096 || chat.ChatOutputLimitField != "max_tokens" {
		t.Fatal("legacy catalog lacks explicit effective chat defaults")
	}
	if bytes.Contains(base.JSON, []byte("chat_output_limit_field")) || bytes.Contains(base.JSON, []byte("max_tokens")) || bytes.Contains(base.JSON, []byte("private-chat-v1")) {
		t.Fatal("private wire config leaked through public model catalog")
	}
	mediaVersion := base.Entries["custom-image"].Model.Version
	config.Models[0].ChatOutputLimitField = "max_completion_tokens"
	rewriteCatalogFixture(t, path, config)
	changed := load()
	if changed.Entries["custom-chat"].Model.Version == chat.Model.Version || changed.Public.Version == base.Public.Version || changed.Entries["custom-image"].Model.Version != mediaVersion {
		t.Fatal("wire field failed to version only its own model")
	}
	config.Models[0].Capabilities.ContextWindowTokens = 2048
	config.Models[0].Capabilities.MaxOutputTokens = 128
	// max_prompt_bytes is a per-input ceiling, not a guarantee that its entire
	// maximum must fit every model window. Request validation decides that.
	rewriteCatalogFixture(t, path, config)
	small := load()
	if small.Entries["custom-chat"].Model.Capabilities.MaxPromptBytes != 16000 || small.Entries["custom-chat"].Model.Capabilities.ContextWindowTokens != 2048 {
		t.Fatal("legitimate smaller model window was normalized away")
	}
}

func TestCatalogChatBudgetRejectsInvalidOrMediaSettings(t *testing.T) {
	for name, change := range map[string]func(*catalogFile){
		"window low":  func(c *catalogFile) { c.Models[0].Capabilities.ContextWindowTokens = 2047 },
		"window high": func(c *catalogFile) { c.Models[0].Capabilities.ContextWindowTokens = 1048577 },
		"output low":  func(c *catalogFile) { c.Models[0].Capabilities.MaxOutputTokens = 127 },
		"output high": func(c *catalogFile) { c.Models[0].Capabilities.MaxOutputTokens = 32769 },
		"no input room": func(c *catalogFile) {
			c.Models[0].Capabilities.ContextWindowTokens = 2048
			c.Models[0].Capabilities.MaxOutputTokens = 1776
		},
		"unknown wire field": func(c *catalogFile) { c.Models[0].ChatOutputLimitField = "max_output_tokens" },
		"image context":      func(c *catalogFile) { c.Models[1].Capabilities.ContextWindowTokens = 32768 },
		"video output":       func(c *catalogFile) { c.Models[2].Capabilities.MaxOutputTokens = 512 },
		"media wire field":   func(c *catalogFile) { c.Models[1].ChatOutputLimitField = "max_tokens" },
	} {
		t.Run(name, func(t *testing.T) {
			config := customCatalogFixture()
			change(&config)
			if _, err := loadModelCatalog(Config{Mode: "demo", ModelCatalogFile: writeCatalogFixture(t, config)}); err == nil {
				t.Fatal("invalid chat setting accepted")
			}
		})
	}
}
