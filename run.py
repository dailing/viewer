#!/usr/bin/env python3
import argparse
import os
import subprocess
import sys
from datetime import datetime
from pathlib import Path

import uvicorn


PROJECT_ROOT = Path(__file__).resolve().parent
BACKEND_DIR = PROJECT_ROOT / "backend"
FRONTEND_DIR = PROJECT_ROOT / "frontend"
DEFAULT_FRONTEND_DIST = FRONTEND_DIR / "dist"
DEFAULT_ROOT = Path("~/Sync").expanduser()
DEFAULT_PORT = 18989
DEFAULT_HOST = "0.0.0.0"
DEFAULT_LOG_DIR = Path(os.environ.get("VIEWER_HOME", "~/.view")).expanduser() / "logs"
PROJECT_ENV_PATH = PROJECT_ROOT / ".viewer.env"
DEFAULT_VOICE_SERVICE_WS = "ws://127.0.0.1:8765/v1/voice/ws"


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
    parser.add_argument(
        "--debug",
        action="store_true",
        help="Enable debug logging and build frontend sourcemaps when --build-frontend is used.",
    )
    parser.add_argument(
        "--log-dir",
        default=DEFAULT_LOG_DIR,
        type=Path,
        help="Directory for timestamped log files. Defaults to ./logs.",
    )
    parser.add_argument(
        "--log-file",
        default=None,
        type=Path,
        help="Explicit log file path. Overrides --log-dir.",
    )
    parser.add_argument(
        "--no-voice",
        action="store_true",
        help="Disable voice input, which is enabled by default.",
    )
    parser.add_argument(
        "--voice-model",
        default="large-v3-turbo",
        help="WhisperLiveKit model size/name for voice input. Defaults to large-v3-turbo.",
    )
    parser.add_argument(
        "--voice-language",
        default="auto",
        help="Source language code for voice input, or auto. Defaults to auto.",
    )
    parser.add_argument(
        "--voice-target-language",
        default="",
        help="Optional target language code for WhisperLiveKit translation output.",
    )
    parser.add_argument(
        "--voice-service-ws",
        default=DEFAULT_VOICE_SERVICE_WS,
        help=f"Standalone voice service WebSocket URL. Defaults to {DEFAULT_VOICE_SERVICE_WS}.",
    )
    return parser.parse_args()


def resolve_project_path(path: Path) -> Path:
    path = path.expanduser()
    if path.is_absolute():
        return path.resolve()
    return (PROJECT_ROOT / path).resolve()


def load_project_env(path: Path = PROJECT_ENV_PATH) -> None:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError:
        return
    for raw in lines:
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        name, value = line.split("=", 1)
        name = name.strip()
        if not name or name in os.environ:
            continue
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {"'", '"'}:
            value = value[1:-1]
        os.environ[name] = value


def build_frontend(debug: bool) -> None:
    package_json = FRONTEND_DIR / "package.json"
    if not package_json.exists():
        raise SystemExit(f"Frontend package.json does not exist: {package_json}")

    print("Building frontend with npm run build...")
    env = {
        **os.environ,
        "VIEWER_DEBUG": "1" if debug else "0",
        "VITE_VIEWER_DEBUG": "1" if debug else "0",
    }
    try:
        subprocess.run(["npm", "run", "build"], cwd=FRONTEND_DIR, env=env, check=True)
    except FileNotFoundError as exc:
        raise SystemExit("Cannot build frontend because npm was not found.") from exc
    except subprocess.CalledProcessError as exc:
        raise SystemExit(f"Frontend build failed with exit code {exc.returncode}.") from exc


def default_log_file(log_dir: Path) -> Path:
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    target = log_dir.expanduser()
    if target.is_absolute():
        return target.resolve() / f"viewer-{stamp}.log"
    return resolve_project_path(target) / f"viewer-{stamp}.log"


def main() -> None:
    load_project_env()
    args = parse_args()
    root_arg = args.serve_dir if args.serve_dir is not None else args.root
    root = root_arg.expanduser().resolve()
    if not root.exists():
        raise SystemExit(f"Root folder does not exist: {root}")
    if not root.is_dir():
        raise SystemExit(f"Root path is not a directory: {root}")

    os.environ["VIEWER_ROOT"] = root.as_posix()
    os.environ["VIEWER_HOST"] = args.host
    os.environ["VIEWER_PORT"] = str(args.port)
    os.environ["VIEWER_DEBUG"] = "1" if args.debug else "0"
    os.environ["VIEWER_VOICE_ENABLED"] = "0" if args.no_voice else "1"
    os.environ["VIEWER_VOICE_MODEL"] = os.environ.get("VIEWER_VOICE_MODEL", args.voice_model)
    os.environ["VIEWER_VOICE_LANGUAGE"] = os.environ.get("VIEWER_VOICE_LANGUAGE", args.voice_language)
    os.environ["VIEWER_VOICE_TARGET_LANGUAGE"] = os.environ.get("VIEWER_VOICE_TARGET_LANGUAGE", args.voice_target_language)
    os.environ["VIEWER_VOICE_SERVICE_WS"] = os.environ.get("VIEWER_VOICE_SERVICE_WS", args.voice_service_ws)
    os.environ["VIEWER_VOICE_BACKEND"] = os.environ.get("VIEWER_VOICE_BACKEND", "faster-whisper")
    os.environ["VIEWER_VOICE_BACKEND_POLICY"] = os.environ.get("VIEWER_VOICE_BACKEND_POLICY", "localagreement")
    log_file = resolve_project_path(args.log_file) if args.log_file else default_log_file(args.log_dir)
    os.environ["VIEWER_LOG_FILE"] = log_file.as_posix()
    if args.frontend_dist is not None:
        os.environ["VIEWER_FRONTEND_DIST"] = resolve_project_path(args.frontend_dist).as_posix()
    else:
        os.environ["VIEWER_FRONTEND_DIST"] = DEFAULT_FRONTEND_DIST.as_posix()

    sys.path.insert(0, BACKEND_DIR.as_posix())
    from app.logging import configure_logging

    configure_logging(log_file, args.debug)

    if args.build_frontend:
        build_frontend(args.debug)

    display_host = "127.0.0.1" if args.host == "0.0.0.0" else args.host
    base_url = f"http://{display_host}:{args.port}"
    print(f"Serving {root} at http://{args.host}:{args.port}")
    print(f"Log file: {log_file}")
    print(f"Log URL:  {base_url}/api/debug/log")
    uvicorn.run(
        "app.main:app",
        host=args.host,
        port=args.port,
        reload=args.reload,
        log_config=None,
        log_level="debug" if args.debug else "info",
    )


if __name__ == "__main__":
    main()
