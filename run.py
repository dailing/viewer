#!/usr/bin/env python3
import argparse
import os
import subprocess
import sys
from pathlib import Path

import uvicorn


PROJECT_ROOT = Path(__file__).resolve().parent
BACKEND_DIR = PROJECT_ROOT / "backend"
FRONTEND_DIR = PROJECT_ROOT / "frontend"
DEFAULT_FRONTEND_DIST = FRONTEND_DIR / "dist"
DEFAULT_ROOT = Path("~/Sync").expanduser()
DEFAULT_PORT = 18989
DEFAULT_HOST = "0.0.0.0"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Start the local live file viewer.")
    parser.add_argument(
        "root",
        nargs="?",
        default=DEFAULT_ROOT,
        type=Path,
        help="Folder to serve. Defaults to ~/Sync.",
    )
    parser.add_argument(
        "--serve-dir",
        "--root",
        dest="serve_dir",
        default=None,
        type=Path,
        help="Folder to serve. Overrides the positional root argument.",
    )
    parser.add_argument(
        "-p",
        "--port",
        default=DEFAULT_PORT,
        type=int,
        help="Port to listen on. Defaults to 18989.",
    )
    parser.add_argument(
        "--host",
        default=DEFAULT_HOST,
        help="Host/interface to bind. Defaults to 0.0.0.0.",
    )
    parser.add_argument(
        "--frontend-dist",
        default=None,
        type=Path,
        help="Optional frontend dist directory. Defaults to frontend/dist.",
    )
    parser.add_argument(
        "--build-frontend",
        action="store_true",
        help="Run npm run build in frontend/ before starting the server.",
    )
    parser.add_argument(
        "--reload",
        action="store_true",
        help="Enable uvicorn reload for backend development.",
    )
    return parser.parse_args()


def resolve_project_path(path: Path) -> Path:
    path = path.expanduser()
    if path.is_absolute():
        return path.resolve()
    return (PROJECT_ROOT / path).resolve()


def build_frontend() -> None:
    package_json = FRONTEND_DIR / "package.json"
    if not package_json.exists():
        raise SystemExit(f"Frontend package.json does not exist: {package_json}")

    print("Building frontend with npm run build...")
    try:
        subprocess.run(["npm", "run", "build"], cwd=FRONTEND_DIR, check=True)
    except FileNotFoundError as exc:
        raise SystemExit("Cannot build frontend because npm was not found.") from exc
    except subprocess.CalledProcessError as exc:
        raise SystemExit(f"Frontend build failed with exit code {exc.returncode}.") from exc


def main() -> None:
    args = parse_args()
    root_arg = args.serve_dir if args.serve_dir is not None else args.root
    root = root_arg.expanduser().resolve()
    if not root.exists():
        raise SystemExit(f"Root folder does not exist: {root}")
    if not root.is_dir():
        raise SystemExit(f"Root path is not a directory: {root}")

    if args.build_frontend:
        build_frontend()

    os.environ["VIEWER_ROOT"] = root.as_posix()
    if args.frontend_dist is not None:
        os.environ["VIEWER_FRONTEND_DIST"] = resolve_project_path(args.frontend_dist).as_posix()
    else:
        os.environ["VIEWER_FRONTEND_DIST"] = DEFAULT_FRONTEND_DIST.as_posix()

    sys.path.insert(0, BACKEND_DIR.as_posix())
    print(f"Serving {root} at http://{args.host}:{args.port}")
    uvicorn.run("app.main:app", host=args.host, port=args.port, reload=args.reload)


if __name__ == "__main__":
    main()
