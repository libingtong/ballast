#!/usr/bin/env python3
"""最小 stdio MCP server(M0b mcp-write 用):一个会写文件的工具 write_file(path, content)。
只实现 initialize、tools/list、tools/call;JSON-RPC 按行读写。不联网。"""
import json
import sys


def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except ValueError:
        continue
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {
            "protocolVersion": msg.get("params", {}).get("protocolVersion", "2024-11-05"),
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "m0b-writer", "version": "1"}}})
    elif method == "tools/list":
        send({"jsonrpc": "2.0", "id": mid, "result": {"tools": [{
            "name": "write_file", "description": "Write content to a file path.",
            "inputSchema": {"type": "object", "properties": {"path": {"type": "string"}, "content": {"type": "string"}},
                            "required": ["path", "content"]}}]}})
    elif method == "tools/call":
        args = (msg.get("params") or {}).get("arguments") or {}
        try:
            with open(args["path"], "w") as f:
                f.write(args.get("content", ""))
            send({"jsonrpc": "2.0", "id": mid, "result": {"content": [{"type": "text", "text": "wrote"}]}})
        except Exception as e:  # noqa: BLE001
            send({"jsonrpc": "2.0", "id": mid, "result": {"isError": True, "content": [{"type": "text", "text": str(e)}]}})
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "result": {}})
