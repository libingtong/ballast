#!/usr/bin/env python3
"""按脚本应答的最小 Anthropic Messages API 桩(M0b §6.6,mode:"mock")。

沙箱、permissions 与 MCP 白名单都由客户端执行,模型只负责发出 tool_use;这个桩让"模型"
发出指定的工具调用,从而在没有凭据的情况下观察客户端的强制效果。它不实现任何真实模型
能力,也不接收真实凭据(客户端用 dummy key,运行期没有外网)。

用法:mock-api.py <port> <script.json>
script.json:{"steps":[{"tool":"Bash","input":{"command":"..."}}, ...], "final":"done"}
  - 带 tools 的请求(主循环)按"历史里已有的 tool_use 个数"取第 N 步;步骤用完后回 final 文本。
  - 不带 tools 的请求(标题、摘要等辅助调用)回固定文本 "ok"。
  - 其余端点回 200 {}。
所有请求的 方法 路径 与 是否流式 记入 /tmp/mock-requests.log(不记请求体,不含 prompt)。
"""
import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 18081
SCRIPT = json.load(open(sys.argv[2])) if len(sys.argv) > 2 else {"steps": [], "final": "done"}
LOCK = threading.Lock()


def log(line):
    with open("/tmp/mock-requests.log", "a") as f:
        f.write(f"{time.time():.3f} {line}\n")


def count_tool_uses(messages):
    n = 0
    for m in messages:
        c = m.get("content")
        if m.get("role") == "assistant" and isinstance(c, list):
            n += sum(1 for b in c if isinstance(b, dict) and b.get("type") == "tool_use")
    return n


def log_tool_results(messages):
    """把最近一次 assistant 消息之后的 tool_result 摘要(前 160 字符)记入 /tmp/mock-toolresults.log,便于判断工具为什么失败。"""
    tail = []
    for m in reversed(messages or []):
        if m.get("role") == "assistant":
            break
        tail.append(m)
    with open("/tmp/mock-toolresults.log", "a") as f:
        for m in reversed(tail):
            c = m.get("content")
            if not isinstance(c, list):
                continue
            for b in c:
                if isinstance(b, dict) and b.get("type") == "tool_result":
                    body = b.get("content")
                    if isinstance(body, list):
                        body = " ".join(x.get("text", "") for x in body if isinstance(x, dict))
                    f.write("%s error=%s %s\n" % (b.get("tool_use_id"), bool(b.get("is_error")), str(body).replace("\n", " ")[:160]))


def sse(event, data):
    return f"event: {event}\ndata: {json.dumps(data)}\n\n".encode()


def stream_blocks(model, blocks, stop_reason):
    out = [sse("message_start", {"type": "message_start", "message": {
        "id": "msg_mock", "type": "message", "role": "assistant", "model": model, "content": [],
        "stop_reason": None, "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 1}}})]
    for i, b in enumerate(blocks):
        if b["type"] == "text":
            out.append(sse("content_block_start", {"type": "content_block_start", "index": i, "content_block": {"type": "text", "text": ""}}))
            out.append(sse("content_block_delta", {"type": "content_block_delta", "index": i, "delta": {"type": "text_delta", "text": b["text"]}}))
        else:
            out.append(sse("content_block_start", {"type": "content_block_start", "index": i, "content_block": {"type": "tool_use", "id": b["id"], "name": b["name"], "input": {}}}))
            out.append(sse("content_block_delta", {"type": "content_block_delta", "index": i, "delta": {"type": "input_json_delta", "partial_json": json.dumps(b["input"])}}))
        out.append(sse("content_block_stop", {"type": "content_block_stop", "index": i}))
    out.append(sse("message_delta", {"type": "message_delta", "delta": {"stop_reason": stop_reason, "stop_sequence": None}, "usage": {"output_tokens": 20}}))
    out.append(sse("message_stop", {"type": "message_stop"}))
    return b"".join(out)


def message_json(model, blocks, stop_reason):
    content = []
    for b in blocks:
        if b["type"] == "text":
            content.append({"type": "text", "text": b["text"]})
        else:
            content.append({"type": "tool_use", "id": b["id"], "name": b["name"], "input": b["input"]})
    return json.dumps({"id": "msg_mock", "type": "message", "role": "assistant", "model": model, "content": content,
                       "stop_reason": stop_reason, "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 20}}).encode()


class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *a):
        pass

    def _send(self, code, body, ctype="application/json"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        log(f"GET {self.path}")
        self._send(200, b"{}")

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) if n else b""
        path = self.path.split("?")[0]
        if not path.endswith("/messages"):
            log(f"POST {self.path}")
            self._send(200, b"{}")
            return
        try:
            req = json.loads(raw or b"{}")
        except ValueError:
            req = {}
        model = req.get("model", "claude-mock")
        stream = bool(req.get("stream"))
        tools = req.get("tools") or []
        with LOCK:
            if tools:
                log_tool_results(req.get("messages", []))
                idx = count_tool_uses(req.get("messages", []))
                steps = SCRIPT.get("steps", [])
                if idx < len(steps):
                    st = steps[idx]
                    blocks = [{"type": "tool_use", "id": f"toolu_mock{idx:04d}", "name": st["tool"], "input": st["input"]}]
                    stop = "tool_use"
                    log(f"POST {self.path} stream={stream} step={idx} tool={st['tool']}")
                else:
                    blocks = [{"type": "text", "text": SCRIPT.get("final", "done")}]
                    stop = "end_turn"
                    log(f"POST {self.path} stream={stream} final")
            else:
                blocks = [{"type": "text", "text": "ok"}]
                stop = "end_turn"
                log(f"POST {self.path} stream={stream} aux")
        if stream:
            body = stream_blocks(model, blocks, stop)
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        else:
            self._send(200, message_json(model, blocks, stop))


if __name__ == "__main__":
    srv = ThreadingHTTPServer(("127.0.0.1", PORT), H)
    srv.daemon_threads = True
    srv.serve_forever()
