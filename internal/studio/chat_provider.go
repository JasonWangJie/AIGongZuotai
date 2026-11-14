package studio

import (
	"context"
	"unicode/utf8"
)

const (
	MaxChatMessages                = 129
	MaxChatContentBytes            = 256 << 10
	ChatMessageOverhead            = 16
	ChatSafetyReserve              = 256
	DefaultChatContextWindowTokens = 32768
	DefaultChatMaxOutputTokens     = 4096
	minChatContextWindowTokens     = 2048
	maxChatContextWindowTokens     = 1048576
	minChatOutputTokens            = 128
	maxChatOutputTokens            = 32768
)

// ChatRequest is an immutable, private execution snapshot assembled by the
// authorized conversation service. Providers never look up or truncate history.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ChatRequest struct {
	Messages         []ChatMessage `json:"messages"`
	MaxOutputTokens  int           `json:"max_output_tokens"`
	OutputLimitField string        `json:"output_limit_field"`
}

// EstimateChatContext is a conservative admission heuristic, not a tokenizer,
// invoice, or a guarantee about a custom gateway's actual token accounting.
// Raw UTF-8 bytes are counted directly; roles/framing and safety are reserved.
func EstimateChatContext(messages []ChatMessage) (int, error) {
	if len(messages) < 1 || len(messages) > MaxChatMessages || len(messages)%2 == 0 {
		return 0, failure(422, "INVALID_CHAT_CONTEXT", "对话上下文需要完整的文本轮次，并以当前用户消息结束")
	}
	bytes := 0
	for i, message := range messages {
		expected := "user"
		if i%2 == 1 {
			expected = "assistant"
		}
		if message.Role != expected || message.Content == "" || !utf8.ValidString(message.Content) {
			return 0, failure(422, "INVALID_CHAT_CONTEXT", "对话上下文包含不支持的角色、空消息或无效文本")
		}
		if len(message.Content) > MaxChatContentBytes-bytes {
			return 0, failure(422, "CONTEXT_TOO_LARGE", "选中的对话正文超过平台上下文大小限制")
		}
		bytes += len(message.Content)
	}
	return bytes + len(messages)*ChatMessageOverhead + ChatSafetyReserve, nil
}

func validChatOutputLimitField(field string) bool {
	return field == "max_tokens" || field == "max_completion_tokens"
}

func validateChatWireRequest(request ChatRequest) (int, error) {
	estimate, err := EstimateChatContext(request.Messages)
	if err != nil {
		return 0, err
	}
	if request.MaxOutputTokens < minChatOutputTokens || request.MaxOutputTokens > maxChatOutputTokens || !validChatOutputLimitField(request.OutputLimitField) {
		return 0, failure(422, "INVALID_CHAT_CONTEXT", "对话输出预算或服务端协议配置无效")
	}
	return estimate, nil
}

func ValidateChatRequest(request ChatRequest, contextWindow int) error {
	estimate, err := validateChatWireRequest(request)
	if err != nil {
		return err
	}
	if contextWindow < minChatContextWindowTokens || contextWindow > maxChatContextWindowTokens || request.MaxOutputTokens+ChatSafetyReserve+ChatMessageOverhead >= contextWindow {
		return failure(422, "INVALID_CHAT_CONTEXT", "模型上下文窗口与输出预算配置无效")
	}
	if estimate+request.MaxOutputTokens > contextWindow {
		return failure(422, "CONTEXT_TOO_LARGE", "当前消息与必须保留的上下文超出模型窗口，请缩短输入或选择更大窗口的模型")
	}
	return nil
}

// The demonstration accepts the same immutable message contract. Its scripted
// reply remains based on the last user text and is not model reasoning.
func (d demoChat) StreamMessages(ctx context.Context, _ string, request ChatRequest, emit func(string) error) error {
	if _, err := validateChatWireRequest(request); err != nil {
		return err
	}
	return d.Stream(ctx, request.Messages[len(request.Messages)-1].Content, emit)
}
