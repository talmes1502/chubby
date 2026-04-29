import asyncio
import io

import pytest

from chub.proto import frame


def test_encode_prepends_4_byte_be_length() -> None:
    out = frame.encode(b'{"a":1}')
    assert out[:4] == b"\x00\x00\x00\x07"
    assert out[4:] == b'{"a":1}'


def test_decode_reverses_encode() -> None:
    payload = b'{"hello":"world"}'
    assert frame.decode_one(frame.encode(payload)) == (payload, b"")


def test_decode_returns_remainder() -> None:
    a = frame.encode(b'{"a":1}')
    b = frame.encode(b'{"b":2}')
    payload, rest = frame.decode_one(a + b)
    assert payload == b'{"a":1}'
    assert rest == b


def test_decode_partial_returns_none() -> None:
    a = frame.encode(b'{"a":1}')
    assert frame.decode_one(a[:3]) == (None, a[:3])
    assert frame.decode_one(a[:6]) == (None, a[:6])


def test_decode_oversized_raises() -> None:
    too_big = (frame.MAX_FRAME_SIZE + 1).to_bytes(4, "big") + b"x"
    with pytest.raises(frame.FrameTooLarge):
        frame.decode_one(too_big)


async def test_read_frame_from_stream() -> None:
    payload = b'{"hello":"world"}'
    raw = frame.encode(payload)
    reader = asyncio.StreamReader()
    reader.feed_data(raw)
    reader.feed_eof()
    got = await frame.read_frame(reader)
    assert got == payload


async def test_read_frame_eof_returns_none() -> None:
    reader = asyncio.StreamReader()
    reader.feed_eof()
    assert await frame.read_frame(reader) is None
