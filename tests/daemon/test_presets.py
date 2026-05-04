"""Tests for the spawn-preset storage + template rendering."""

from __future__ import annotations

import json
from datetime import date
from pathlib import Path

import pytest

from chubby.daemon import presets as presets_mod
from chubby.daemon.presets import (
    Preset,
    delete_preset,
    get_preset,
    load_presets,
    save_presets,
    upsert_preset,
)


@pytest.fixture(autouse=True)
def _isolated_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    """Redirect chubby's home dir so tests don't touch real config."""
    monkeypatch.setenv("CHUBBY_HOME", str(tmp_path))
    return tmp_path


def test_load_returns_empty_when_file_missing() -> None:
    assert load_presets() == []


def test_save_and_load_roundtrip() -> None:
    save_presets(
        [
            Preset(name="web", cwd="~/repo/web"),
            Preset(name="api", cwd="~/repo/api", tags=["backend"]),
        ]
    )
    out = load_presets()
    names = [p.name for p in out]
    assert sorted(names) == ["api", "web"]
    api = next(p for p in out if p.name == "api")
    assert api.tags == ["backend"]


def test_save_dedupes_by_name_keeping_last() -> None:
    """Save with two entries sharing a name → only the last is kept."""
    save_presets(
        [
            Preset(name="web", cwd="~/old"),
            Preset(name="web", cwd="~/new"),
        ]
    )
    out = load_presets()
    assert len(out) == 1
    assert out[0].cwd == "~/new"


def test_upsert_replaces_existing() -> None:
    upsert_preset(Preset(name="web", cwd="~/v1"))
    upsert_preset(Preset(name="web", cwd="~/v2"))
    assert len(load_presets()) == 1
    assert load_presets()[0].cwd == "~/v2"


def test_delete_removes_only_named() -> None:
    upsert_preset(Preset(name="web", cwd="/x"))
    upsert_preset(Preset(name="api", cwd="/y"))
    assert delete_preset("web") is True
    out = load_presets()
    assert [p.name for p in out] == ["api"]


def test_delete_unknown_returns_false() -> None:
    assert delete_preset("ghost") is False


def test_get_preset_by_name() -> None:
    upsert_preset(Preset(name="web", cwd="/x", tags=["w"]))
    p = get_preset("web")
    assert p is not None
    assert p.cwd == "/x"
    assert get_preset("ghost") is None


def test_render_substitutes_date_and_name() -> None:
    p = Preset(name="wip-{date}", cwd="~/repo", branch="wip-{date}")
    rendered = p.render()
    today = date.today().isoformat()
    assert rendered["name"] == f"wip-{today}"
    assert rendered["branch"] == f"wip-{today}"


def test_render_overrides_win() -> None:
    """Apply-time overrides (e.g., user typed a custom name) take
    precedence over the preset's templated values."""
    p = Preset(name="wip-{date}", cwd="~/repo")
    rendered = p.render(overrides={"name": "explicit"})
    assert rendered["name"] == "explicit"


def test_render_expands_user() -> None:
    """``~`` in cwd is expanded so the daemon doesn't have to know
    the user's home dir."""
    p = Preset(name="x", cwd="~/foo")
    rendered = p.render()
    assert rendered["cwd"].startswith("/")
    assert "~" not in rendered["cwd"]


def test_render_omits_branch_when_unset() -> None:
    p = Preset(name="x", cwd="/tmp")
    rendered = p.render()
    assert "branch" not in rendered


def test_render_includes_tags() -> None:
    p = Preset(name="x", cwd="/tmp", tags=["one", "two"])
    rendered = p.render()
    assert rendered["tags"] == ["one", "two"]


def test_malformed_json_falls_back_to_empty(tmp_path: Path) -> None:
    """A corrupted presets.json shouldn't crash chubby — log + empty
    list, user fixes the file at their leisure."""
    presets_mod.presets_path().parent.mkdir(parents=True, exist_ok=True)
    presets_mod.presets_path().write_text("{ this is invalid json")
    assert load_presets() == []


def test_unknown_fields_are_ignored() -> None:
    """Forward-compat: a presets.json from a newer chubby that adds
    a ``model`` field is read without crashing — the extra key is
    just dropped."""
    presets_mod.presets_path().parent.mkdir(parents=True, exist_ok=True)
    presets_mod.presets_path().write_text(
        json.dumps([{"name": "web", "cwd": "/x", "model": "claude-opus-9"}])
    )
    out = load_presets()
    assert len(out) == 1
    assert out[0].name == "web"
