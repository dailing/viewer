from pathlib import Path

from pydantic_settings import BaseSettings, SettingsConfigDict

PROJECT_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_FRONTEND_DIST = PROJECT_ROOT / "frontend" / "dist"


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="VIEWER_")

    root: Path = Path.cwd()
    host: str = "0.0.0.0"
    port: int = 8000
    frontend_dist: Path | None = DEFAULT_FRONTEND_DIST
    max_text_preview_bytes: int = 2_000_000
    show_hidden: bool = True
    poll_delay_ms: int = 500
    terminal_shell: str = "zsh"
    voice_enabled: bool = False
    voice_upstream_ws: str = ""
    voice_model: str = "base"
    voice_language: str = "en"
    voice_target_language: str = ""
    voice_backend: str = "faster-whisper"
    voice_backend_policy: str = "localagreement"
    voice_direct_english_translation: bool = False
    voice_min_chunk_size: float = 0.1
    voice_stop_timeout_seconds: float = 10.0
    voice_vac: bool = True
    voice_vad: bool = True
    debug: bool = False
    log_file: Path | None = None

    @property
    def root_resolved(self) -> Path:
        return self.root.expanduser().resolve()

    @property
    def frontend_dist_resolved(self) -> Path | None:
        if self.frontend_dist is None:
            return None
        return self.frontend_dist.expanduser().resolve()


settings = Settings()
