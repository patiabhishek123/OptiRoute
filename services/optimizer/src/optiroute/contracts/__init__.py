"""Versioned wire contracts between the Go backend and the optimizer."""

import json
from pathlib import Path

from optiroute.contracts import v1


def export_schemas(out_dir: Path) -> list[Path]:
    """Write JSON Schemas for the current contract version (checked in under packages/)."""
    out_dir.mkdir(parents=True, exist_ok=True)
    written = []
    for name, model in (("solve-request", v1.SolveRequest), ("solve-result", v1.SolveResult)):
        path = out_dir / f"{name}.schema.json"
        path.write_text(json.dumps(model.model_json_schema(), indent=2, sort_keys=True) + "\n")
        written.append(path)
    return written


__all__ = ["export_schemas", "v1"]
