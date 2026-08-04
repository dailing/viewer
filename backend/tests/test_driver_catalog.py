import asyncio
import json
import tempfile
from pathlib import Path
from unittest.mock import patch

import yaml

from backend.app.driver_catalog import list_hermes_targets


def test_hermes_catalog_reads_provider_model_combinations_without_credentials():
    with tempfile.TemporaryDirectory() as directory:
        home = Path(directory)
        (home / "cache").mkdir()
        (home / "config.yaml").write_text(
            yaml.safe_dump({"model": {"provider": "deepseek", "default": "deepseek-chat"}}),
            encoding="utf-8",
        )
        (home / "auth.json").write_text(
            json.dumps({"providers": {"deepseek": {"api_key": "must-not-leak"}}}),
            encoding="utf-8",
        )
        (home / "cache" / "model_catalog.json").write_text(
            json.dumps({"providers": {"deepseek": {"models": {"deepseek-chat": {"name": "DeepSeek Chat"}}}}}),
            encoding="utf-8",
        )

        with patch.dict("os.environ", {"HERMES_HOME": str(home)}):
            targets, warnings = asyncio.run(list_hermes_targets("default"))

    assert warnings == []
    assert len(targets) == 1
    assert targets[0].agent_id == "hermes"
    assert targets[0].provider_id == "deepseek"
    assert targets[0].model_id == "deepseek-chat"
    assert targets[0].selection_id == "deepseek:deepseek-chat"
    assert "must-not-leak" not in targets[0].model_dump_json()
