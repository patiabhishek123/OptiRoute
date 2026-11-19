"""Command-line entry point: ``optiroute {solve,bench,fetch-solomon,schema}``."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from optiroute import __version__

DEFAULT_SOLOMON_DIR = Path("data/solomon")
DEFAULT_SCHEMA_DIR = Path(__file__).resolve().parents[4] / "packages/api-contracts/optimizer/v1"


def _solve(args: argparse.Namespace) -> int:
    from optiroute.contracts.v1 import SolveRequest
    from optiroute.matrices import HaversineMatrixProvider
    from optiroute.pipeline import solve_request

    req = SolveRequest.model_validate_json(Path(args.request).read_text())
    result = solve_request(req, HaversineMatrixProvider(), algorithm=args.algorithm)
    text = result.model_dump_json(indent=2)
    if args.output:
        Path(args.output).write_text(text + "\n")
    else:
        print(text)
    print(
        f"{result.status} objective={result.objective.total if result.objective else None} "
        f"violations={len(result.validation.violations)}",
        file=sys.stderr,
    )
    return 0 if result.validation.passed else 1


def _bench(args: argparse.Namespace) -> int:
    from optiroute.benchmarks import runner

    data = Path(args.data)
    if args.instances:
        paths = [data / f"{name}.txt" for name in args.instances]
    else:
        paths = sorted(data.glob("*.txt"))
    if missing := [p for p in paths if not p.exists()]:
        print(
            f"missing {missing[0]} (+{len(missing) - 1}); run `make fetch-solomon`", file=sys.stderr
        )
        return 2
    rows = runner.run(paths, args.algorithm, args.seeds, args.time_limit)
    if args.csv:
        with open(args.csv, "w", newline="") as f:
            runner.write_csv(rows, f)
    else:
        runner.write_csv(rows, sys.stdout)
    print(runner.summary(rows), file=sys.stderr)
    return 0 if all(r.feasible for r in rows) else 1


def _fetch(args: argparse.Namespace) -> int:
    from optiroute.benchmarks import solomon

    paths = solomon.fetch(Path(args.dest), args.instances or None)
    print(f"{len(paths)} instances verified in {args.dest}", file=sys.stderr)
    return 0


def _schema(args: argparse.Namespace) -> int:
    from optiroute.contracts import export_schemas

    for path in export_schemas(Path(args.out)):
        print(path, file=sys.stderr)
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="optiroute")
    parser.add_argument("--version", action="version", version=f"optiroute {__version__}")
    sub = parser.add_subparsers(dest="command")

    p = sub.add_parser("solve", help="solve a SolveRequest JSON file (haversine matrix)")
    p.add_argument("request")
    p.add_argument("-a", "--algorithm", help="override solver.algorithm (e.g. S0)")
    p.add_argument("-o", "--output", help="write SolveResult JSON here (default: stdout)")
    p.set_defaults(func=_solve)

    p = sub.add_parser("bench", help="run Solomon benchmarks, CSV to stdout, summary to stderr")
    p.add_argument("instances", nargs="*", help="e.g. C101 R101 (default: all in --data)")
    p.add_argument("-a", "--algorithm", action="append", help="repeatable; default S0")
    p.add_argument("--seeds", type=int, nargs="+", default=[0])
    p.add_argument("--time-limit", type=float, default=15.0)
    p.add_argument("--data", default=str(DEFAULT_SOLOMON_DIR))
    p.add_argument("--csv", help="write rows here instead of stdout")
    p.set_defaults(func=_bench)

    p = sub.add_parser("fetch-solomon", help="download and checksum-verify Solomon instances")
    p.add_argument("instances", nargs="*", help="default: all 56")
    p.add_argument("--dest", default=str(DEFAULT_SOLOMON_DIR))
    p.set_defaults(func=_fetch)

    p = sub.add_parser("schema", help="export contract JSON Schemas")
    p.add_argument("--out", default=str(DEFAULT_SCHEMA_DIR))
    p.set_defaults(func=_schema)

    args = parser.parse_args(argv)
    if args.command is None:
        parser.print_help()
        return 0
    if args.command == "bench" and not args.algorithm:
        args.algorithm = ["S0"]
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
