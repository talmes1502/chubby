"""Hub-run lifecycle. Full impl (manifests, events.ndjson) in Phase 4."""

from __future__ import annotations

from dataclasses import dataclass

from chub.daemon.clock import now_ms
from chub.daemon.ids import new_hub_run_id


@dataclass
class HubRun:
    id: str
    started_at: int


def start_run() -> HubRun:
    return HubRun(id=new_hub_run_id(), started_at=now_ms())
