from chubby.proto.errors import ChubError, ErrorCode


def test_error_codes_exist() -> None:
    expected = {
        "SESSION_NOT_FOUND",
        "INJECTION_NOT_SUPPORTED",
        "SESSION_DEAD",
        "WRAPPER_UNREACHABLE",
        "NAME_TAKEN",
        "INVALID_PAYLOAD",
        "DAEMON_BUSY",
        "HUB_RUN_LOCKED",
        "TMUX_NOT_FOUND",
        "TMUX_TARGET_INVALID",
        "ATTACH_PROMOTE_REQUIRED",
        "ALREADY_ATTACHED",
        "INTERNAL",
    }
    assert {e.name for e in ErrorCode} == expected


def test_chub_error_carries_code_and_message() -> None:
    err = ChubError(ErrorCode.SESSION_NOT_FOUND, "no session named frontend")
    assert err.code is ErrorCode.SESSION_NOT_FOUND
    assert "frontend" in str(err)


def test_chub_error_to_dict_is_jsonrpc_shape() -> None:
    err = ChubError(ErrorCode.NAME_TAKEN, "name `x` is already in use", data={"name": "x"})
    assert err.to_dict() == {
        "code": ErrorCode.NAME_TAKEN.value,
        "message": "name `x` is already in use",
        "data": {"name": "x"},
    }
