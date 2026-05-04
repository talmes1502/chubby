from chubby.daemon.ids import new_hub_run_id, new_session_id


def test_session_id_prefix() -> None:
    assert new_session_id().startswith("s_")
    assert len(new_session_id()) == 2 + 26


def test_hub_run_id_prefix() -> None:
    assert new_hub_run_id().startswith("hr_")
    assert len(new_hub_run_id()) == 3 + 26


def test_uniqueness() -> None:
    ids = {new_session_id() for _ in range(1000)}
    assert len(ids) == 1000
