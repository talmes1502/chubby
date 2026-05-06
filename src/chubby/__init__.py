# version.py is generated at build time by hatch-vcs from the
# current git tag (or the dev-version derivation between tags).
# Source-of-truth lives in `pyproject.toml`'s
# [tool.hatch.version] / [tool.hatch.build.hooks.vcs] block —
# never edit version.py by hand.
try:
    from chubby.version import __version__
except ImportError:  # pragma: no cover — pre-build / editable-from-fresh-clone
    __version__ = "0.0.0+unknown"

__all__ = ["__version__"]
