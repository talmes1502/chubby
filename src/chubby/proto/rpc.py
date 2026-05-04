"""JSON-RPC 2.0 envelope. We use a subset:
- Request: {"jsonrpc": "2.0", "id": int, "method": str, "params": obj}
- Response: {"jsonrpc": "2.0", "id": int, "result": ...} OR {... "error": {code, message, data?}}
- Event (server-push, no id): {"jsonrpc": "2.0", "method": str, "params": obj}
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Any

from chubby.proto.errors import ChubError, ErrorCode

JSON = dict[str, Any]


@dataclass(frozen=True)
class Request:
    id: int
    method: str
    params: JSON = field(default_factory=dict)


@dataclass(frozen=True)
class Response:
    id: int
    result: Any = None
    error: JSON | None = None

    @classmethod
    def from_error(cls, id: int, err: ChubError) -> Response:
        return cls(id=id, result=None, error=err.to_dict())


@dataclass(frozen=True)
class Event:
    method: str
    params: JSON = field(default_factory=dict)


Message = Request | Response | Event


def encode_message(msg: Message) -> bytes:
    body: JSON = {"jsonrpc": "2.0"}
    if isinstance(msg, Request):
        body.update({"id": msg.id, "method": msg.method, "params": msg.params})
    elif isinstance(msg, Response):
        body["id"] = msg.id
        if msg.error is not None:
            body["error"] = msg.error
        else:
            body["result"] = msg.result
    else:
        body.update({"method": msg.method, "params": msg.params})
    return json.dumps(body, separators=(",", ":")).encode("utf-8")


def decode_message(raw: bytes) -> Message:
    try:
        body = json.loads(raw)
    except json.JSONDecodeError as e:
        raise ChubError(ErrorCode.INVALID_PAYLOAD, f"invalid JSON: {e}")
    if not isinstance(body, dict) or body.get("jsonrpc") != "2.0":
        raise ChubError(ErrorCode.INVALID_PAYLOAD, "missing jsonrpc=2.0")
    if "method" in body and "id" in body:
        return Request(
            id=int(body["id"]), method=str(body["method"]), params=body.get("params") or {}
        )
    if "method" in body:
        return Event(method=str(body["method"]), params=body.get("params") or {})
    if "id" in body:
        return Response(id=int(body["id"]), result=body.get("result"), error=body.get("error"))
    raise ChubError(ErrorCode.INVALID_PAYLOAD, "unknown envelope")
