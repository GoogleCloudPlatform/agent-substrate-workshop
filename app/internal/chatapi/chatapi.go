// Package chatapi holds the JSON shapes shared by the agent, the gateway, and the load tool.
package chatapi

// Message is one turn in a conversation. Role is "user" or "assistant".
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is what a caller POSTs to an actor's /chat.
type ChatRequest struct {
	Message string `json:"message"`
}

// ChatResponse is what an actor's /chat returns.
type ChatResponse struct {
	Reply   string `json:"reply"`
	Turn    int    `json:"turn"`
	Backend string `json:"backend"`
}

// ModelRequest is what an actor POSTs to the gateway's /v1/model.
type ModelRequest struct {
	Messages []Message `json:"messages"`
}

// ModelResponse is what the gateway's /v1/model returns.
type ModelResponse struct {
	Reply        string `json:"reply"`
	Backend      string `json:"backend"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
}

// Error is the body of every non-2xx JSON response.
type Error struct {
	Error string `json:"error"`
}
