#!/usr/bin/env python3
"""本地演示 / 测试用假上游（仅标准库，零依赖）。

支持 Anthropic /v1/messages 与 OpenAI /v1/chat/completions：
- 非流式与 SSE 流式；
- 工具调用回显（第一轮 tool_use，第二轮 tool_result 后回文本）；
- 故障注入（fail_status / hang_ms / truncate_after）与并发统计；
- /__stats、/__last、/__config 仅供测试读取，不记录凭据原文。

固定 usage：12 输入 / 5 输出 token（便于复算计费）。
演示脚本 scripts/demo/quickstart.py 默认配合它使用。

  python3 mock_upstream.py --host 0.0.0.0 --port 19011
"""

import argparse
import hashlib
import json
import re
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOCK = threading.Lock()
STATS = {
    "models": 0,
    "messages": 0,
    "chat": 0,
    "messages_stream": 0,
    "messages_nonstream": 0,
    "chat_stream": 0,
    "chat_nonstream": 0,
    "active": 0,
    "peak_active": 0,
    "by_auth": {},
}
CONFIG = {"delay_ms": 0, "fail_status": 0, "hang_ms": 0, "truncate_after": 0}
LAST = {}

TOOL_INPUT = {"city": "Paris"}
TOOL_NAME = "get_weather"
TOOL_ID = "toolu_accept_1"


def bump(key):
    with LOCK:
        STATS[key] = STATS.get(key, 0) + 1


def bump_auth(headers):
    raw = headers.get("Authorization") or headers.get("x-api-key") or ""
    if not raw:
        return
    fp = hashlib.sha256(raw.encode()).hexdigest()[:8]
    with LOCK:
        STATS["by_auth"][fp] = STATS["by_auth"].get(fp, 0) + 1


def enter():
    with LOCK:
        STATS["active"] += 1
        if STATS["active"] > STATS["peak_active"]:
            STATS["peak_active"] = STATS["active"]


def leave():
    with LOCK:
        STATS["active"] = max(STATS["active"] - 1, 0)


def delay_seconds():
    with LOCK:
        return CONFIG["delay_ms"] / 1000.0


def settings():
    with LOCK:
        return dict(CONFIG)


def capture(parsed, headers, path):
    messages = parsed.get("messages") or []
    prompt_text = json.dumps(messages, ensure_ascii=False)
    tool_names = [tool.get("name") for tool in parsed.get("tools") or [] if isinstance(tool, dict)]
    has_tool_result = any(
        isinstance(block, dict) and block.get("type") == "tool_result"
        for message in messages
        if isinstance(message, dict) and isinstance(message.get("content"), list)
        for block in message["content"]
    )
    with LOCK:
        LAST.clear()
        LAST.update({
            "path": path,
            "model": parsed.get("model"),
            "stream": bool(parsed.get("stream")),
            "system_present": bool(parsed.get("system")),
            "tool_names": tool_names,
            "has_tool_result": has_tool_result,
            "anthropic_version": headers.get("anthropic-version"),
            "message_count": len(messages),
            # 只暴露“是否已打码 / 是否仍有秘密形状”，不把正文写进统计。
            "masked": "⟦PII-" in prompt_text,
            "secret_like": bool(re.search(r"\bsk-[A-Za-z0-9_-]{20,}\b|\bkk_[A-Za-z0-9_-]{20,}\b", prompt_text)),
            "prompt_sha256": hashlib.sha256(prompt_text.encode("utf-8")).hexdigest(),
        })


def anthropic_text() -> dict:
    return {
        "id": "msg_mock", "type": "message", "role": "assistant",
        "model": "claude-3-5-sonnet-20241022",
        "content": [{"type": "text", "text": "pong"}],
        "stop_reason": "end_turn",
        "usage": {"input_tokens": 12, "output_tokens": 5,
                  "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
    }


def anthropic_tool() -> dict:
    return {
        "id": "msg_tool", "type": "message", "role": "assistant",
        "model": "claude-3-5-sonnet-20241022",
        "content": [{"type": "tool_use", "id": TOOL_ID, "name": TOOL_NAME, "input": TOOL_INPUT}],
        "stop_reason": "tool_use",
        "usage": {"input_tokens": 12, "output_tokens": 5,
                  "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
    }


def anthropic_sse(tool: bool) -> list[tuple[str, dict]]:
    if tool:
        partial = json.dumps(TOOL_INPUT)
        split = max(1, len(partial) // 2)
        return [
            ("message_start", {"type": "message_start", "message": {
                "id": "msg_tool", "type": "message", "role": "assistant",
                "model": "claude-3-5-sonnet-20241022", "content": [],
                "stop_reason": None, "usage": {"input_tokens": 12, "output_tokens": 0}}}),
            ("content_block_start", {"type": "content_block_start", "index": 0,
                                     "content_block": {"type": "tool_use", "id": TOOL_ID,
                                                       "name": TOOL_NAME, "input": {}}}),
            ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                     "delta": {"type": "input_json_delta",
                                               "partial_json": partial[:split]}}),
            ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                     "delta": {"type": "input_json_delta",
                                               "partial_json": partial[split:]}}),
            ("content_block_stop", {"type": "content_block_stop", "index": 0}),
            ("message_delta", {"type": "message_delta",
                               "delta": {"stop_reason": "tool_use", "stop_sequence": None},
                               "usage": {"output_tokens": 5}}),
            ("message_stop", {"type": "message_stop"}),
        ]
    return [
        ("message_start", {"type": "message_start", "message": {
            "id": "msg_mock", "type": "message", "role": "assistant",
            "model": "claude-3-5-sonnet-20241022", "content": [],
            "stop_reason": None, "usage": {"input_tokens": 12, "output_tokens": 0}}}),
        ("content_block_start", {"type": "content_block_start", "index": 0,
                                 "content_block": {"type": "text", "text": ""}}),
        ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                 "delta": {"type": "text_delta", "text": "pong"}}),
        ("content_block_stop", {"type": "content_block_stop", "index": 0}),
        ("message_delta", {"type": "message_delta",
                           "delta": {"stop_reason": "end_turn", "stop_sequence": None},
                           "usage": {"output_tokens": 5}}),
        ("message_stop", {"type": "message_stop"}),
    ]


def openai_text() -> dict:
    return {
        "id": "chatcmpl-mock", "object": "chat.completion", "model": "gpt-4o-mini",
        "choices": [{"index": 0, "message": {"role": "assistant", "content": "pong"},
                     "finish_reason": "stop"}],
        "usage": {"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17},
    }


def openai_stream_chunks() -> list[dict]:
    base = {"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": "gpt-4o-mini"}
    return [
        {**base, "choices": [{"index": 0, "delta": {"role": "assistant"}, "finish_reason": None}]},
        {**base, "choices": [{"index": 0, "delta": {"content": "pong"}, "finish_reason": None}]},
        {**base, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]},
        {**base, "choices": [],
         "usage": {"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17}},
    ]


def openai_has_tool_request(payload: dict) -> bool:
    """OpenAI 形状的「第一轮工具请求」：带 tools、没有 role=tool 的结果消息。

    与 Anthropic 分支的 has_tools and not has_tool_result 对齐，使两条协议在
    「带 tools 的第一轮回 tool_use / 回传结果后回文本」的行为一致
    （供应商台账 §二 项 3 要求两条协议都能验多轮工具调用）。
    """
    if not payload.get("tools"):
        return False
    for message in payload.get("messages") or []:
        if isinstance(message, dict) and message.get("role") == "tool":
            return False
        if isinstance(message, dict) and isinstance(message.get("content"), list):
            for block in message["content"]:
                if isinstance(block, dict) and block.get("type") == "tool_result":
                    return False
    return True


def openai_tool() -> dict:
    return {
        "id": "chatcmpl-mock-tool", "object": "chat.completion", "model": "gpt-4o-mini",
        "choices": [{
            "index": 0,
            "message": {
                "role": "assistant", "content": None,
                "tool_calls": [{"id": "call_mock_1", "type": "function",
                                "function": {"name": TOOL_NAME,
                                             "arguments": json.dumps(TOOL_INPUT)}}],
            },
            "finish_reason": "tool_calls",
        }],
        "usage": {"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17},
    }


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def _json(self, status, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _sse(self, events):
        data = b"".join(
            f"event: {name}\ndata: {json.dumps(payload)}\n\n".encode("utf-8")
            for name, payload in events
        )
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def _openai_sse(self, chunks):
        data = b"".join(f"data: {json.dumps(chunk)}\n\n".encode("utf-8") for chunk in chunks)
        data += b"data: [DONE]\n\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt, *args):  # 静默：不把请求头写进日志
        pass

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path.startswith("/__stats"):
            with LOCK:
                self._json(200, dict(STATS, by_auth=dict(STATS["by_auth"])))
            return
        if path.startswith("/__last"):
            with LOCK:
                self._json(200, dict(LAST))
            return
        if path.endswith("/models"):
            bump("models")
            self._json(200, {
                "object": "list",
                "data": [{"id": "claude-3-5-sonnet-20241022", "object": "model"},
                         {"id": "gpt-4o-mini", "object": "model"}],
            })
            return
        self._json(404, {"error": {"code": "not_found"}})

    def do_POST(self):
        path = self.path.split("?", 1)[0]
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            payload = json.loads(raw or b"{}")
        except json.JSONDecodeError:
            self._json(400, {"error": {"code": "invalid_json"}})
            return

        if path.startswith("/__config"):
            with LOCK:
                for key in ("delay_ms", "fail_status", "hang_ms", "truncate_after"):
                    if key in payload:
                        CONFIG[key] = max(int(payload.get(key) or 0), 0)
                if payload.get("reset_stats"):
                    for key in list(STATS):
                        if key == "by_auth":
                            STATS[key] = {}
                        else:
                            STATS[key] = 0
            self._json(200, {"ok": True})
            return

        capture(payload, self.headers, path)
        bump_auth(self.headers)
        stream = bool(payload.get("stream"))
        cfg = settings()
        if cfg["fail_status"] > 0:
            self._json(cfg["fail_status"], {"error": {"code": "mock_failure",
                                                      "status": cfg["fail_status"]}})
            return

        # 计数在 hang 之前：客户端超时时，请求确实已经到达上游（T-21 的"可能已执行"）。
        if path.endswith("/messages"):
            bump("messages")
            bump("messages_stream" if stream else "messages_nonstream")
        elif path.endswith("/chat/completions"):
            bump("chat")
            bump("chat_stream" if stream else "chat_nonstream")

        pause = delay_seconds() + cfg["hang_ms"] / 1000.0
        enter()
        try:
            if pause > 0:
                time.sleep(pause)
            self._respond(payload, stream, cfg, path)
        finally:
            leave()

    def _respond(self, payload, stream, cfg, path):
        model = payload.get("model") or "claude-3-5-sonnet-20241022"
        has_tools = bool(payload.get("tools"))
        has_tool_result = any(
            isinstance(block, dict) and block.get("type") == "tool_result"
            for message in payload.get("messages") or []
            if isinstance(message, dict) and isinstance(message.get("content"), list)
            for block in message["content"]
        )

        if path.endswith("/messages"):
            tool = has_tools and not has_tool_result
            if stream:
                events = anthropic_sse(tool)
                if cfg["truncate_after"] > 0:
                    events = events[: cfg["truncate_after"]]
                self._sse(events)
                return
            self._json(200, anthropic_tool() if tool else anthropic_text())
            return

        if path.endswith("/chat/completions"):
            if stream:
                chunks = openai_stream_chunks()
                if cfg["truncate_after"] > 0:
                    chunks = chunks[: cfg["truncate_after"]]
                self._openai_sse(chunks)
                return
            body = openai_tool() if openai_has_tool_request(payload) else openai_text()
            body["model"] = model
            self._json(200, body)
            return

        self._json(404, {"error": {"code": "not_found"}})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=19011)
    parser.add_argument("--host", default="127.0.0.1")
    args = parser.parse_args()
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    print(f"mock upstream listening on 127.0.0.1:{args.port}", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
