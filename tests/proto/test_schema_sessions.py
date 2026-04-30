import pytest
from pydantic import ValidationError

from chubby.proto.schema import (
    RegisterWrappedParams,
    RegisterWrappedResult,
    ListSessionsParams,
    ListSessionsResult,
    RenameSessionParams,
    RecolorSessionParams,
)


def test_register_wrapped_params_required_fields() -> None:
    p = RegisterWrappedParams.model_validate(
        {"name": "frontend", "cwd": "/tmp", "pid": 42}
    )
    assert p.name == "frontend"
    assert p.tags == []


def test_register_wrapped_params_extra_rejected() -> None:
    with pytest.raises(ValidationError):
        RegisterWrappedParams.model_validate(
            {"name": "x", "cwd": "/tmp", "pid": 1, "evil": True}
        )


def test_list_sessions_params_optional() -> None:
    p = ListSessionsParams.model_validate({})
    assert p.kind is None


def test_list_sessions_result_carries_session_dicts() -> None:
    r = ListSessionsResult.model_validate({"sessions": []})
    assert r.sessions == []


def test_rename_session_params_required() -> None:
    p = RenameSessionParams.model_validate({"id": "s_1", "name": "x"})
    assert p.id == "s_1"


def test_recolor_session_params_required() -> None:
    p = RecolorSessionParams.model_validate({"id": "s_1", "color": "#abcdef"})
    assert p.color == "#abcdef"
