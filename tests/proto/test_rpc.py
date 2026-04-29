import json

import pytest

from chub.proto.errors import ChubError, ErrorCode
from chub.proto.rpc import Request, Response, Event, decode_message, encode_message


def test_request_round_trip() -> None:
    req = Request(id=1, method="ping", params={"x": 1})
    raw = encode_message(req)
    out = decode_message(raw)
    assert isinstance(out, Request)
    assert out.id == 1
    assert out.method == "ping"
    assert out.params == {"x": 1}


def test_response_success() -> None:
    resp = Response(id=1, result={"ok": True})
    raw = encode_message(resp)
    out = decode_message(raw)
    assert isinstance(out, Response)
    assert out.result == {"ok": True}
    assert out.error is None


def test_response_error() -> None:
    err = ChubError(ErrorCode.SESSION_NOT_FOUND, "missing")
    resp = Response.from_error(id=1, err=err)
    raw = encode_message(resp)
    out = decode_message(raw)
    assert isinstance(out, Response)
    assert out.error == err.to_dict()


def test_event_has_no_id() -> None:
    ev = Event(method="session_added", params={"id": "s_1"})
    raw = encode_message(ev)
    out = decode_message(raw)
    assert isinstance(out, Event)
    assert out.method == "session_added"
    assert out.params == {"id": "s_1"}


def test_decode_rejects_unknown_envelope() -> None:
    with pytest.raises(ChubError) as exc:
        decode_message(json.dumps({"foo": "bar"}).encode())
    assert exc.value.code is ErrorCode.INVALID_PAYLOAD


def test_decode_rejects_invalid_json() -> None:
    with pytest.raises(ChubError) as exc:
        decode_message(b"not json")
    assert exc.value.code is ErrorCode.INVALID_PAYLOAD
