"""Tests for the ``chubby update`` / startup-update-check helpers."""

from __future__ import annotations

import json
import time
from pathlib import Path

import pytest

from chubby.cli.commands import _update_check


@pytest.fixture(autouse=True)
def _isolated_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Pin CHUBBY_HOME so cache writes don't leak into the real one."""
    monkeypatch.setenv("CHUBBY_HOME", str(tmp_path))
    return tmp_path


def test_is_newer_release_tuple_compare() -> None:
    """Sanity that we're using PEP 440 ordering (delegated to
    ``packaging.version``). We don't re-test ``packaging``'s edge-
    case matrix — those are its contract — just spot-check the
    representative cases the prompt cares about."""
    # Release vs release.
    assert _update_check.is_newer("0.2.0", "0.1.9")
    assert _update_check.is_newer("0.1.10", "0.1.9")
    assert not _update_check.is_newer("0.1.0", "0.1.0")
    assert not _update_check.is_newer("0.1.0", "0.2.0")
    # Dev build of a release is *less than* the release per PEP 440 —
    # so a user on 0.1.2.dev0 gets prompted to install 0.1.2.
    assert _update_check.is_newer("0.1.2", "0.1.2.dev0")
    # And a dev build of a future release is still newer.
    assert _update_check.is_newer("0.1.3.dev0", "0.1.2")


def test_is_newer_returns_false_on_garbage_tag() -> None:
    """A malformed tag from GitHub must not raise — failure to parse
    means "no upgrade prompt" rather than crashing ``chubby start``."""
    assert not _update_check.is_newer("not-a-version", "0.1.0")
    assert not _update_check.is_newer("0.1.0", "?")


def test_latest_release_tag_uses_cache_within_ttl(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Once cached, latest_release_tag returns the cached value without
    hitting GitHub for ``_CACHE_TTL_S`` (default 24h)."""
    cache_path = _update_check._cache_path()
    cache_path.parent.mkdir(parents=True, exist_ok=True)
    cache_path.write_text(
        json.dumps({"tag": "9.9.9", "fetched_at": time.time()}),
        encoding="utf-8",
    )

    # If we hit the API we'd see something other than 9.9.9.
    fetch_calls: list[None] = []

    def boom(timeout: float = 2.0) -> dict | None:
        fetch_calls.append(None)
        return {"tag_name": "v0.0.1"}

    monkeypatch.setattr(_update_check, "_fetch_latest_release_json", boom)

    tag = _update_check.latest_release_tag()
    assert tag == "9.9.9"
    assert fetch_calls == []  # cache hit; no API call


def test_latest_release_tag_falls_back_silently_on_api_error(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """If GitHub is unreachable, latest_release_tag returns None
    rather than raising — startup must never break on a flaky
    network."""
    monkeypatch.setattr(
        _update_check,
        "_fetch_latest_release_json",
        lambda timeout=2.0: None,
    )
    assert _update_check.latest_release_tag(use_cache=False) is None


def test_latest_release_tag_strips_leading_v(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        _update_check,
        "_fetch_latest_release_json",
        lambda timeout=2.0: {"tag_name": "v1.2.3"},
    )
    assert _update_check.latest_release_tag(use_cache=False) == "1.2.3"


def test_latest_release_tag_handles_unprefixed_tag(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        _update_check,
        "_fetch_latest_release_json",
        lambda timeout=2.0: {"tag_name": "1.2.3"},
    )
    assert _update_check.latest_release_tag(use_cache=False) == "1.2.3"
