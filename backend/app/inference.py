from __future__ import annotations

import hashlib
import json
from enum import StrEnum
from pydantic import BaseModel, Field


class InferenceTarget(BaseModel):
    target_id: str
    agent_id: str
    agent_name: str
    provider_id: str
    provider_name: str
    model_id: str
    model_name: str
    selection_id: str
    is_default: bool = False
    available: bool = True
    authenticated: bool | None = None
    context_window: int | None = None
    capabilities: dict[str, bool] = Field(default_factory=dict)
    source: str = ""


class TargetHealth(BaseModel):
    scope_type: str
    scope_id: str
    status: str = "healthy"
    error_category: str = ""
    consecutive_failures: int = 0
    retry_after: float | None = None
    last_error: str = ""
    updated_at: float = 0


class InferenceCatalog(BaseModel):
    targets: list[InferenceTarget] = Field(default_factory=list)
    health: list[TargetHealth] = Field(default_factory=list)
    warnings: list[str] = Field(default_factory=list)
    refreshed_at: float


class FailureScope(StrEnum):
    QUERY = "query"
    TARGET = "target"
    PROVIDER = "provider"
    AGENT = "agent"


class DriverError(RuntimeError):
    def __init__(
        self,
        message: str,
        *,
        category: str = "runtime",
        scope: FailureScope = FailureScope.QUERY,
        scope_id: str = "",
        retryable: bool = False,
        retry_after: float | None = None,
        safe_to_failover: bool = False,
    ) -> None:
        super().__init__(message)
        self.category = category
        self.scope = scope
        self.scope_id = scope_id
        self.retryable = retryable
        self.retry_after = retry_after
        self.safe_to_failover = safe_to_failover


def inference_target_id(agent_id: str, provider_id: str, model_id: str) -> str:
    payload = json.dumps([agent_id, provider_id, model_id], ensure_ascii=False, separators=(",", ":"))
    return f"target-{hashlib.sha256(payload.encode('utf-8')).hexdigest()[:20]}"
