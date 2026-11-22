package studio

import (
	"strings"
	"testing"
)

func TestLongConversationHistoryRequiresPersistentStore(t *testing.T) {
	app, err := New(Config{LongConversationHistory: true})
	if app != nil || err == nil || !strings.Contains(err.Error(), "requires PostgreSQL") {
		t.Fatal("memory mode accepted larger conversation limits", err)
	}
}
