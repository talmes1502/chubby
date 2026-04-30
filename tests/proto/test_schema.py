import pytest
from pydantic import ValidationError

from chubby.proto.schema import PingParams, PingResult, VersionResult


def test_ping_params_accepts_optional_message() -> None:
    p = PingParams.model_validate({})
    assert p.message is None
    p2 = PingParams.model_validate({"message": "hi"})
    assert p2.message == "hi"


def test_ping_result_echoes() -> None:
    r = PingResult.model_validate({"echo": "hi", "server_time_ms": 1700000000000})
    assert r.echo == "hi"
    assert r.server_time_ms == 1700000000000


def test_version_result_required_fields() -> None:
    r = VersionResult.model_validate({"version": "0.1.0", "protocol": 1})
    assert r.version == "0.1.0"
    assert r.protocol == 1
    with pytest.raises(ValidationError):
        VersionResult.model_validate({"version": "0.1.0"})
