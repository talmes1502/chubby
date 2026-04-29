"""Deterministic stand-in for the `claude` binary, used by integration tests.

Reads stdin line-by-line; for each line:
  - prints "USER: <line>"
  - prints "ASSISTANT: ack-<line>"
  - prints "THINK_DONE"
On EOF, exits 0. SIGTERM is honored. Prints "READY" on startup.
Logs every received line to ``$FAKECLAUDE_LOG`` if set.
"""

from __future__ import annotations

import os
import signal
import sys
from typing import Any


def main() -> None:
    log_path = os.environ.get("FAKECLAUDE_LOG")
    log = open(log_path, "a", encoding="utf-8") if log_path else None

    def _bye(*_args: Any) -> None:
        if log is not None:
            log.close()
        sys.exit(0)

    signal.signal(signal.SIGTERM, _bye)
    print("READY", flush=True)
    try:
        for raw in sys.stdin:
            line = raw.rstrip("\n").rstrip("\r")
            if log is not None:
                log.write(line + "\n")
                log.flush()
            print(f"USER: {line}", flush=True)
            print(f"ASSISTANT: ack-{line}", flush=True)
            print("THINK_DONE", flush=True)
    finally:
        if log is not None:
            log.close()


if __name__ == "__main__":
    main()
