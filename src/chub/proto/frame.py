"""Length-prefixed frame codec. 4-byte BE length, then JSON bytes."""

from __future__ import annotations

import asyncio
import struct

MAX_FRAME_SIZE = 16 * 1024 * 1024


class FrameTooLarge(Exception):
    pass


def encode(payload: bytes) -> bytes:
    return struct.pack(">I", len(payload)) + payload


def decode_one(buf: bytes) -> tuple[bytes | None, bytes]:
    """Try to decode one frame. Return (payload | None, remainder)."""
    if len(buf) < 4:
        return None, buf
    (length,) = struct.unpack(">I", buf[:4])
    if length > MAX_FRAME_SIZE:
        raise FrameTooLarge(f"frame size {length} > MAX {MAX_FRAME_SIZE}")
    if len(buf) < 4 + length:
        return None, buf
    return buf[4 : 4 + length], buf[4 + length :]


async def read_frame(reader: asyncio.StreamReader) -> bytes | None:
    """Read exactly one frame. Return None on clean EOF."""
    try:
        header = await reader.readexactly(4)
    except asyncio.IncompleteReadError:
        return None
    (length,) = struct.unpack(">I", header)
    if length > MAX_FRAME_SIZE:
        raise FrameTooLarge(f"frame size {length} > MAX {MAX_FRAME_SIZE}")
    return await reader.readexactly(length)


async def write_frame(writer: asyncio.StreamWriter, payload: bytes) -> None:
    writer.write(encode(payload))
    await writer.drain()
