from chubby import __version__


def test_version_string() -> None:
    assert isinstance(__version__, str)
    # Hatch-vcs-derived versions can be a tagged release ("0.1.2") or
    # a dev build between tags ("0.1.2.dev0"). Either is fine — just
    # require a leading semver triplet.
    parts = __version__.split(".")
    assert len(parts) >= 3
    for piece in parts[:3]:
        # numeric major/minor/patch
        int(piece)
