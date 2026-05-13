import json
import re
from pathlib import Path

from fastapi import HTTPException

from .config import settings
from .models import UserProfile
from .storage import CONFIG_PATH, USERS_DIR, ensure_view_home, migrate_legacy_state

USER_ID_PATTERN = re.compile(r"^[A-Za-z0-9_.-]+$")
INITIAL_USER_PROFILES = [
    UserProfile(id="dailing", name="d AI Ling g", home="~/Sync"),
    UserProfile(id="maomao", name="毛毛", home="~/maomao"),
]
INITIAL_DEFAULT_USER = "dailing"


def normalize_user_id(value: str | None) -> str:
    user_id = (value or "").strip()
    if not user_id:
        return default_user_id()
    if not USER_ID_PATTERN.fullmatch(user_id):
        raise HTTPException(status_code=400, detail="User id may only contain letters, numbers, dots, dashes, and underscores")
    return user_id


def _raw_config() -> dict:
    migrate_legacy_state()
    if not CONFIG_PATH.exists():
        return {}
    try:
        raw = json.loads(CONFIG_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    return raw if isinstance(raw, dict) else {}


def list_user_profiles() -> list[UserProfile]:
    raw = _raw_config()
    profiles: list[UserProfile] = []
    seen: set[str] = set()
    for item in raw.get("users") or []:
        if not isinstance(item, dict):
            continue
        try:
            profile = UserProfile.model_validate(item)
        except Exception:
            continue
        user_id = profile.id.strip()
        if not user_id or not USER_ID_PATTERN.fullmatch(user_id) or user_id in seen:
            continue
        profiles.append(UserProfile(id=user_id, name=profile.name.strip() or user_id, home=_normalize_home(profile.home)))
        seen.add(user_id)
    return profiles or INITIAL_USER_PROFILES


def default_user_id() -> str:
    raw = _raw_config()
    configured = str(raw.get("default_user") or "").strip()
    profiles = list_user_profiles()
    if configured and USER_ID_PATTERN.fullmatch(configured) and any(profile.id == configured for profile in profiles):
        return configured
    if any(profile.id == INITIAL_DEFAULT_USER for profile in profiles):
        return INITIAL_DEFAULT_USER
    return profiles[0].id


def get_user_profile(user_id: str | None) -> UserProfile:
    normalized = normalize_user_id(user_id)
    for profile in list_user_profiles():
        if profile.id == normalized:
            return profile
    raise HTTPException(status_code=404, detail="User profile not found")


def user_home_relative(user_id: str | None) -> str:
    try:
        relative = user_home_path(user_id).relative_to(settings.root_resolved).as_posix()
        return "" if relative == "." else relative
    except ValueError:
        return ""


def user_home_path(user_id: str | None) -> Path:
    profile = get_user_profile(user_id)
    raw = profile.home.strip()
    if not raw:
        return settings.root_resolved
    path = Path(raw).expanduser()
    if path.is_absolute():
        return path.resolve()
    return settings.root_resolved.joinpath(_normalize_relative_home(raw)).resolve()


def user_state_dir(user_id: str | None) -> Path:
    profile = get_user_profile(user_id)
    path = USERS_DIR / profile.id
    path.mkdir(parents=True, exist_ok=True)
    return path


def user_workspaces_path(user_id: str | None) -> Path:
    ensure_view_home()
    profile = get_user_profile(user_id)
    path = user_state_dir(profile.id) / "workspaces.json"
    return path


def _normalize_home(value: str | None) -> str:
    raw = (value or "").replace("\\", "/").strip()
    if not raw:
        return ""
    if raw.startswith("~") or Path(raw).expanduser().is_absolute():
        return raw
    return _normalize_relative_home(raw)


def _normalize_relative_home(value: str) -> str:
    raw = value.replace("\\", "/").strip("/")
    parts = [part for part in raw.split("/") if part and part != "."]
    if any(part == ".." for part in parts):
        return ""
    return "/".join(parts)
