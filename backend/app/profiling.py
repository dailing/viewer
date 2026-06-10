from __future__ import annotations

import html
import os
import time
from collections import Counter, defaultdict, deque
from dataclasses import dataclass, field
from threading import Lock
from typing import Any

from starlette.requests import Request

MAX_SAMPLES_PER_ROUTE = 500
MAX_RECENT_REQUESTS = 200


@dataclass
class RouteProfile:
    method: str
    route: str
    count: int = 0
    total_ms: float = 0.0
    min_ms: float | None = None
    max_ms: float = 0.0
    status_counts: Counter[int] = field(default_factory=Counter)
    samples_ms: deque[float] = field(default_factory=lambda: deque(maxlen=MAX_SAMPLES_PER_ROUTE))
    recent: deque[dict[str, Any]] = field(default_factory=lambda: deque(maxlen=20))

    def record(self, elapsed_ms: float, status_code: int, path: str, query: str) -> None:
        self.count += 1
        self.total_ms += elapsed_ms
        self.min_ms = elapsed_ms if self.min_ms is None else min(self.min_ms, elapsed_ms)
        self.max_ms = max(self.max_ms, elapsed_ms)
        self.status_counts[status_code] += 1
        self.samples_ms.append(elapsed_ms)
        self.recent.appendleft(
            {
                "at": time.time(),
                "elapsed_ms": elapsed_ms,
                "status": status_code,
                "path": path,
                "query": query,
            }
        )


class ApiProfiler:
    def __init__(self) -> None:
        self._lock = Lock()
        self._started_at = time.time()
        self._routes: dict[tuple[str, str], RouteProfile] = {}
        self._recent: deque[dict[str, Any]] = deque(maxlen=MAX_RECENT_REQUESTS)

    def record(self, request: Request, status_code: int, elapsed_ms: float) -> None:
        path = request.url.path
        if path == "/profile" or path == "/api/profile":
            return
        route = self._route_label(request)
        method = request.method
        query = request.url.query
        entry = {
            "at": time.time(),
            "method": method,
            "route": route,
            "path": path,
            "query": query,
            "status": status_code,
            "elapsed_ms": elapsed_ms,
        }
        with self._lock:
            profile = self._routes.get((method, route))
            if profile is None:
                profile = RouteProfile(method=method, route=route)
                self._routes[(method, route)] = profile
            profile.record(elapsed_ms, status_code, path, query)
            self._recent.appendleft(entry)

    def reset(self) -> None:
        with self._lock:
            self._started_at = time.time()
            self._routes.clear()
            self._recent.clear()

    def snapshot(self) -> dict[str, Any]:
        with self._lock:
            routes = [self._route_snapshot(profile) for profile in self._routes.values()]
            recent = list(self._recent)
            started_at = self._started_at
        routes.sort(key=lambda item: item["total_ms"], reverse=True)
        return {
            "started_at": started_at,
            "uptime_sec": time.time() - started_at,
            "process": self._process_snapshot(),
            "routes": routes,
            "recent": recent,
        }

    def html_report(self) -> str:
        data = self.snapshot()
        rows = "\n".join(self._route_row(route) for route in data["routes"])
        recent_rows = "\n".join(self._recent_row(item) for item in data["recent"][:60])
        process = data["process"]
        sqlite_paths = "".join(
            f"<li><code>{html.escape(path)}</code></li>"
            for path in process["sqlite_like_fds"]
        )
        return f"""<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Viewer API Profile</title>
  <style>
    :root {{
      color-scheme: light dark;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: Canvas;
      color: CanvasText;
    }}
    body {{ margin: 0; padding: 24px; }}
    h1 {{ font-size: 22px; margin: 0 0 6px; }}
    h2 {{ font-size: 16px; margin: 28px 0 10px; }}
    .meta {{ color: color-mix(in srgb, CanvasText 65%, Canvas); margin-bottom: 18px; }}
    table {{ width: 100%; border-collapse: collapse; font-size: 13px; }}
    th, td {{ border-bottom: 1px solid color-mix(in srgb, CanvasText 16%, Canvas); padding: 7px 8px; text-align: right; vertical-align: top; }}
    th {{ position: sticky; top: 0; background: Canvas; font-weight: 650; }}
    td.route, th.route, td.left, th.left {{ text-align: left; }}
    code {{ font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }}
    .pill {{ display: inline-block; padding: 2px 6px; border: 1px solid color-mix(in srgb, CanvasText 20%, Canvas); border-radius: 999px; }}
    .actions {{ display: flex; gap: 10px; margin: 18px 0; }}
    button, a.button {{ border: 1px solid color-mix(in srgb, CanvasText 24%, Canvas); color: CanvasText; background: Canvas; border-radius: 6px; padding: 7px 10px; text-decoration: none; cursor: pointer; }}
  </style>
</head>
<body>
  <h1>Viewer API Profile</h1>
  <div class="meta">Uptime {data["uptime_sec"]:.0f}s · PID {process["pid"]} · fd {process["fd_count"]} · sqlite-like fd {process["sqlite_like_fd_count"]} · sorted by total handler time · <code>/api/profile</code> returns JSON</div>
  <div class="actions">
    <a class="button" href="/profile">Refresh</a>
    <a class="button" href="/api/profile">JSON</a>
    <button onclick="fetch('/api/profile/reset', {{method:'POST'}}).then(() => location.reload())">Reset</button>
  </div>
  <h2>Open SQLite FDs</h2>
  <ul>{sqlite_paths or "<li>None</li>"}</ul>
  <h2>Routes</h2>
  <table>
    <thead>
      <tr>
        <th class="left">Method</th>
        <th class="route">Route</th>
        <th>Count</th>
        <th>Total</th>
        <th>Avg</th>
        <th>P50</th>
        <th>P95</th>
        <th>P99</th>
        <th>Max</th>
        <th>Status</th>
      </tr>
    </thead>
    <tbody>{rows}</tbody>
  </table>
  <h2>Recent Requests</h2>
  <table>
    <thead>
      <tr>
        <th class="left">Method</th>
        <th class="route">Route</th>
        <th>Status</th>
        <th>Time</th>
        <th class="route">Path</th>
      </tr>
    </thead>
    <tbody>{recent_rows}</tbody>
  </table>
</body>
</html>"""

    def _route_label(self, request: Request) -> str:
        route = request.scope.get("route")
        route_path = getattr(route, "path", None)
        if route_path:
            return str(route_path)
        return request.url.path

    def _process_snapshot(self) -> dict[str, Any]:
        pid = os.getpid()
        fd_dir = f"/proc/{pid}/fd"
        try:
            names = os.listdir(fd_dir)
        except OSError:
            return {"pid": pid, "fd_count": None, "sqlite_like_fd_count": None, "sqlite_like_fds": []}
        sqlite_like: list[str] = []
        for name in names:
            try:
                target = os.readlink(os.path.join(fd_dir, name))
            except OSError:
                continue
            if "sqlite" in target or "agent-history" in target or "state.db" in target:
                sqlite_like.append(target)
        sqlite_like.sort()
        return {
            "pid": pid,
            "fd_count": len(names),
            "sqlite_like_fd_count": len(sqlite_like),
            "sqlite_like_fds": sqlite_like,
        }

    def _route_snapshot(self, profile: RouteProfile) -> dict[str, Any]:
        samples = sorted(profile.samples_ms)
        return {
            "method": profile.method,
            "route": profile.route,
            "count": profile.count,
            "total_ms": profile.total_ms,
            "avg_ms": profile.total_ms / profile.count if profile.count else 0.0,
            "min_ms": profile.min_ms or 0.0,
            "max_ms": profile.max_ms,
            "p50_ms": self._percentile(samples, 0.50),
            "p95_ms": self._percentile(samples, 0.95),
            "p99_ms": self._percentile(samples, 0.99),
            "status_counts": dict(profile.status_counts),
            "recent": list(profile.recent),
        }

    def _percentile(self, samples: list[float], percent: float) -> float:
        if not samples:
            return 0.0
        index = min(len(samples) - 1, max(0, round((len(samples) - 1) * percent)))
        return samples[index]

    def _route_row(self, item: dict[str, Any]) -> str:
        status = " ".join(f"{code}:{count}" for code, count in sorted(item["status_counts"].items()))
        return (
            "<tr>"
            f"<td class=\"left\"><span class=\"pill\">{html.escape(item['method'])}</span></td>"
            f"<td class=\"route\"><code>{html.escape(item['route'])}</code></td>"
            f"<td>{item['count']}</td>"
            f"<td>{item['total_ms']:.1f}ms</td>"
            f"<td>{item['avg_ms']:.1f}ms</td>"
            f"<td>{item['p50_ms']:.1f}ms</td>"
            f"<td>{item['p95_ms']:.1f}ms</td>"
            f"<td>{item['p99_ms']:.1f}ms</td>"
            f"<td>{item['max_ms']:.1f}ms</td>"
            f"<td>{html.escape(status)}</td>"
            "</tr>"
        )

    def _recent_row(self, item: dict[str, Any]) -> str:
        path = item["path"] + (f"?{item['query']}" if item.get("query") else "")
        return (
            "<tr>"
            f"<td class=\"left\"><span class=\"pill\">{html.escape(item['method'])}</span></td>"
            f"<td class=\"route\"><code>{html.escape(item['route'])}</code></td>"
            f"<td>{item['status']}</td>"
            f"<td>{item['elapsed_ms']:.1f}ms</td>"
            f"<td class=\"route\"><code>{html.escape(path)}</code></td>"
            "</tr>"
        )


api_profiler = ApiProfiler()
