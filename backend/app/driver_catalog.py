from __future__ import annotations

import asyncio
import json
import os
from pathlib import Path
from typing import Any

import yaml

from .inference import InferenceTarget, inference_target_id


HERMES_MODEL_PROVIDER_ALIASES = {
    "kimi-coding": "kimi-for-coding",
    "copilot": "github-copilot",
}


def _read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
        return value if isinstance(value, dict) else {}
    except (OSError, json.JSONDecodeError):
        return {}


def _hermes_home(profile: str) -> Path:
    root = Path(os.environ.get("HERMES_HOME", Path.home() / ".hermes")).expanduser()
    return root if not profile or profile == "default" else root / "profiles" / profile


def _model_metadata_target(
    *,
    agent_id: str,
    agent_name: str,
    provider_id: str,
    provider_name: str,
    model_id: str,
    selection_id: str,
    metadata: dict[str, Any],
    is_default: bool,
    source: str,
) -> InferenceTarget:
    modalities = metadata.get("modalities") if isinstance(metadata.get("modalities"), dict) else {}
    inputs = modalities.get("input") if isinstance(modalities.get("input"), list) else []
    limit = metadata.get("limit") if isinstance(metadata.get("limit"), dict) else {}
    context = limit.get("context")
    return InferenceTarget(
        target_id=inference_target_id(agent_id, provider_id, model_id),
        agent_id=agent_id,
        agent_name=agent_name,
        provider_id=provider_id,
        provider_name=provider_name,
        model_id=model_id,
        model_name=str(metadata.get("name") or model_id),
        selection_id=selection_id,
        is_default=is_default,
        available=True,
        authenticated=True,
        context_window=int(context) if isinstance(context, int) and context > 0 else None,
        capabilities={
            "tools": bool(metadata.get("tool_call", True)),
            "filesystem": True,
            "vision": "image" in inputs,
        },
        source=source,
    )


async def list_hermes_targets(profile: str) -> tuple[list[InferenceTarget], list[str]]:
    home = _hermes_home(profile)
    warnings: list[str] = []
    try:
        raw = yaml.safe_load((home / "config.yaml").read_text(encoding="utf-8")) or {}
        config = raw if isinstance(raw, dict) else {}
    except FileNotFoundError:
        return [], [f"Hermes profile config not found: {home / 'config.yaml'}"]
    except (OSError, yaml.YAMLError) as exc:
        return [], [f"Could not read Hermes profile config: {exc}"]

    model_config = config.get("model") if isinstance(config.get("model"), dict) else {}
    current_provider = str(model_config.get("provider") or "").strip()
    current_model = str(model_config.get("default") or model_config.get("model") or "").strip()
    provider_ids: set[str] = {current_provider} if current_provider else set()

    auth = _read_json(home / "auth.json")
    pool = auth.get("credential_pool") if isinstance(auth.get("credential_pool"), dict) else {}
    providers_auth = auth.get("providers") if isinstance(auth.get("providers"), dict) else {}
    provider_ids.update(str(key) for key in (*pool.keys(), *providers_auth.keys()) if str(key).strip())

    configured_providers = config.get("providers") if isinstance(config.get("providers"), dict) else {}
    provider_ids.update(str(key) for key in configured_providers if str(key).strip())
    fallbacks = config.get("fallback_providers") if isinstance(config.get("fallback_providers"), list) else []
    for fallback in fallbacks:
        if isinstance(fallback, str) and fallback.strip():
            provider_ids.add(fallback.split(":", 1)[0].strip())
        elif isinstance(fallback, dict):
            provider = str(fallback.get("provider") or "").strip()
            if provider:
                provider_ids.add(provider)

    root = home.parent.parent if home.parent.name == "profiles" else home
    curated = _read_json(root / "cache" / "model_catalog.json")
    curated_providers = curated.get("providers") if isinstance(curated.get("providers"), dict) else {}
    models_dev = _read_json(root / "models_dev_cache.json")
    agent_id = "hermes"
    agent_name = f"Hermes ({profile or 'default'})"
    targets: list[InferenceTarget] = []
    seen: set[str] = set()

    for provider_id in sorted(provider_ids):
        configured = configured_providers.get(provider_id) if isinstance(configured_providers.get(provider_id), dict) else {}
        provider_name = str(configured.get("name") or provider_id)
        model_entries: dict[str, dict[str, Any]] = {}
        configured_models = configured.get("models") if isinstance(configured.get("models"), list) else []
        for model in configured_models:
            if isinstance(model, str) and model.strip():
                model_entries[model.strip()] = {}
        catalog_provider = curated_providers.get(provider_id) if isinstance(curated_providers.get(provider_id), dict) else {}
        catalog_models = catalog_provider.get("models") if isinstance(catalog_provider.get("models"), dict) else {}
        for model_id, metadata in catalog_models.items():
            model_entries[str(model_id)] = metadata if isinstance(metadata, dict) else {}
        models_dev_id = HERMES_MODEL_PROVIDER_ALIASES.get(provider_id, provider_id)
        models_dev_provider = models_dev.get(models_dev_id) if isinstance(models_dev.get(models_dev_id), dict) else {}
        provider_name = str(models_dev_provider.get("name") or provider_name)
        models_dev_models = models_dev_provider.get("models") if isinstance(models_dev_provider.get("models"), dict) else {}
        for model_id, metadata in models_dev_models.items():
            model_entries.setdefault(str(model_id), metadata if isinstance(metadata, dict) else {})
        if provider_id == current_provider and current_model:
            model_entries.setdefault(current_model, {})
        for model_id, metadata in model_entries.items():
            selection_id = f"{provider_id}:{model_id}"
            target_id = inference_target_id(agent_id, provider_id, model_id)
            if target_id in seen:
                continue
            seen.add(target_id)
            targets.append(
                _model_metadata_target(
                    agent_id=agent_id,
                    agent_name=agent_name,
                    provider_id=provider_id,
                    provider_name=provider_name,
                    model_id=model_id,
                    selection_id=selection_id,
                    metadata=metadata,
                    is_default=provider_id == current_provider and model_id == current_model,
                    source="hermes-config-cache",
                )
            )
    if not targets:
        warnings.append("Hermes has no configured provider/model combinations")
    return targets, warnings


async def list_opencode_targets(command: str) -> tuple[list[InferenceTarget], list[str]]:
    try:
        process = await asyncio.create_subprocess_exec(
            command,
            "models",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(process.communicate(), timeout=20)
    except (FileNotFoundError, asyncio.TimeoutError) as exc:
        return [], [f"Could not list OpenCode models: {exc}"]
    if process.returncode != 0:
        detail = stderr.decode("utf-8", errors="replace").strip()[:500]
        return [], [f"OpenCode model discovery failed: {detail or process.returncode}"]
    targets: list[InferenceTarget] = []
    for line in stdout.decode("utf-8", errors="replace").splitlines():
        selection_id = line.strip()
        if not selection_id or "/" not in selection_id:
            continue
        provider_id, model_id = selection_id.split("/", 1)
        targets.append(
            InferenceTarget(
                target_id=inference_target_id("opencode", provider_id, model_id),
                agent_id="opencode",
                agent_name="OpenCode",
                provider_id=provider_id,
                provider_name=provider_id,
                model_id=model_id,
                model_name=model_id,
                selection_id=selection_id,
                authenticated=True,
                capabilities={"tools": True, "filesystem": True},
                source="opencode-cli",
            )
        )
    return targets, []
