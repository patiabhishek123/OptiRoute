"""Command-line entry point. Subcommands (solve, bench) arrive in Phase 1."""

import argparse

from optiroute import __version__


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="optiroute")
    parser.add_argument("--version", action="version", version=f"optiroute {__version__}")
    parser.parse_args(argv)
    parser.print_help()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
