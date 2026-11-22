// Command kkmock 是桌面演示用的本地 Mock 上游。
//
// 它只做一件事：按 Anthropic /v1/messages 与 OpenAI /v1/chat/completions 的形状
// 返回固定内容与固定 usage（12 输入 / 5 输出 token），让「签发 Key → Agent 调用 →
// 计量入账」这条链路在没有真实厂商凭据时也能跑通。
//
// 与 scripts/acceptance/mock_upstream.py 的区别：那个是验收用的，带故障注入与并发统计；
// 这个随 Windows 安装包分发，只保留演示必需的路径，并额外识别 stream 与工具调用。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	anthropicModel = "claude-3-5-sonnet-20241022"
	openaiModel    = "gpt-4o-mini"

	// 固定用量：验收用例按这两个数复算费用，改成别的数会让既有证据对不上。
	inputTokens  = 12
	outputTokens = 5
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19011", "监听地址")
	quiet := flag.Bool("quiet", false, "不打印每个请求")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "service": "kkmock"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handle(w, r, *quiet)
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Printf("kkmock 演示上游监听 %s（Anthropic /v1/messages · OpenAI /v1/chat/completions）", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "kkmock: %v\n", err)
		os.Exit(1)
	}
}

func handle(w http.ResponseWriter, r *http.Request, quiet bool) {
	path := r.URL.Path
	if !quiet {
		log.Printf("%s %s", r.Method, path)
	}

	if r.Method == http.MethodGet && strings.HasSuffix(path, "/models") {
		writeJSON(w, 200, map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": anthropicModel, "object": "model", "owned_by": "keykeep-mock"},
				{"id": openaiModel, "object": "model", "owned_by": "keykeep-mock"},
			},
		})
		return
	}

	if r.Method != http.MethodPost {
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "path": path}})
		return
	}

	var payload map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&payload); err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"code": "invalid_json"}})
		return
	}
	stream, _ := payload["stream"].(bool)

	switch {
	case strings.HasSuffix(path, "/messages"):
		if stream {
			streamAnthropic(w, payload)
			return
		}
		writeJSON(w, 200, anthropicMessage(payload))
	case strings.HasSuffix(path, "/chat/completions"):
		if stream {
			streamOpenAI(w, payload)
			return
		}
		writeJSON(w, 200, openAIMessage(payload))
	default:
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "path": path}})
	}
}

// ---------------------------------------------------------------- Anthropic

func anthropicMessage(payload map[string]any) map[string]any {
	if name := toolChoiceName(payload); name != "" {
		return map[string]any{
			"id": "msg_kkmock_tool", "type": "message", "role": "assistant",
			"model": modelName(payload, anthropicModel),
			"content": []map[string]any{{
				"type": "tool_use", "id": "toolu_kkmock_1", "name": name,
				"input": map[string]any{"city": "Paris"},
			}},
			"stop_reason": "tool_use",
			"usage":       anthropicUsage(),
		}
	}
	return map[string]any{
		"id": "msg_kkmock", "type": "message", "role": "assistant",
		"model":       modelName(payload, anthropicModel),
		"content":     []map[string]any{{"type": "text", "text": "pong"}},
		"stop_reason": "end_turn",
		"usage":       anthropicUsage(),
	}
}

func anthropicUsage() map[string]any {
	return map[string]any{
		"input_tokens": inputTokens, "output_tokens": outputTokens,
		"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
	}
}

func streamAnthropic(w http.ResponseWriter, payload map[string]any) {
	model := modelName(payload, anthropicModel)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)

	emit := func(name string, body any) {
		data, _ := json.Marshal(body)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
		if flusher != nil {
			flusher.Flush()
		}
	}

	emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_kkmock", "type": "message", "role": "assistant", "model": model,
		"content": []any{}, "stop_reason": nil,
		"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
	}})

	if name := toolChoiceName(payload); name != "" {
		emit("content_block_start", map[string]any{
			"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": "toolu_kkmock_1", "name": name, "input": map[string]any{}},
		})
		emit("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": `{"city":"Paris"}`},
		})
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{
			"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": outputTokens},
		})
		emit("message_stop", map[string]any{"type": "message_stop"})
		return
	}

	emit("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	emit("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": "pong"},
	})
	emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit("message_delta", map[string]any{
		"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outputTokens},
	})
	emit("message_stop", map[string]any{"type": "message_stop"})
}

// ---------------------------------------------------------------- OpenAI

func openAIMessage(payload map[string]any) map[string]any {
	return map[string]any{
		"id": "chatcmpl-kkmock", "object": "chat.completion", "model": modelName(payload, openaiModel),
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": "pong"},
			"finish_reason": "stop",
		}},
		"usage": openAIUsage(),
	}
}

func openAIUsage() map[string]any {
	return map[string]any{
		"prompt_tokens": inputTokens, "completion_tokens": outputTokens,
		"total_tokens": inputTokens + outputTokens,
	}
}

func streamOpenAI(w http.ResponseWriter, payload map[string]any) {
	model := modelName(payload, openaiModel)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)

	base := map[string]any{"id": "chatcmpl-kkmock", "object": "chat.completion.chunk", "model": model}
	chunks := []map[string]any{
		merge(base, map[string]any{"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}}),
		merge(base, map[string]any{"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": "pong"}, "finish_reason": nil}}}),
		merge(base, map[string]any{"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}),
		merge(base, map[string]any{"choices": []any{}, "usage": openAIUsage()}),
	}
	for _, chunk := range chunks {
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func modelName(payload map[string]any, fallback string) string {
	if name, ok := payload["model"].(string); ok && name != "" {
		return name
	}
	return fallback
}

// toolChoiceName 只在「请求带 tools 且消息里还没有 tool_result」时要求调工具，
// 这样多轮工具调用能自然收敛到文本回复，不会无限循环。
func toolChoiceName(payload map[string]any) string {
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) == 0 {
		return ""
	}
	for _, message := range anySlice(payload["messages"]) {
		block, ok := message.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "tool_result" {
			return ""
		}
		for _, part := range anySlice(block["content"]) {
			if partMap, ok := part.(map[string]any); ok && partMap["type"] == "tool_result" {
				return ""
			}
		}
	}
	for _, tool := range tools {
		if toolMap, ok := tool.(map[string]any); ok {
			if name, ok := toolMap["name"].(string); ok && name != "" {
				return name
			}
		}
	}
	return ""
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func merge(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
