#!/usr/bin/env python3
"""Project-scoped loopback writer for AgentFlow review comments."""

from __future__ import annotations

import argparse
import json
import os
import re
import tempfile
import threading
import time
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, urlparse

FORMAT_VERSION = 3
MAX_BODY_BYTES = 1024 * 1024
MAX_INBOX_FILES = 500
ID_PATTERN = re.compile(r"^[a-z0-9]+(?:[._-][a-z0-9]+)*$")


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def read_object(path: Path) -> dict[str, Any]:
    if path.is_symlink() or not path.is_file() or path.stat().st_size > MAX_BODY_BYTES:
        raise ValueError("Invalid AgentFlow file.")
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError("AgentFlow file must contain an object.")
    return value


def atomic_json(path: Path, value: dict[str, Any], mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(
        dir=path.parent,
        prefix=f".{path.name}.",
        suffix=".tmp",
    )
    temporary_path = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=True, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary_path, mode)
        os.replace(temporary_path, path)
    finally:
        temporary_path.unlink(missing_ok=True)


def safe_id(value: Any, label: str) -> str:
    if not isinstance(value, str) or len(value) > 80 or ID_PATTERN.fullmatch(value) is None:
        raise ValueError(f"Invalid {label}.")
    return value


def require_directory(path: Path, label: str, create: bool = False) -> Path:
    if path.is_symlink():
        raise ValueError(f"{label} must not be a symbolic link.")
    if create:
        path.mkdir(parents=True, exist_ok=True)
    if not path.is_dir():
        raise ValueError(f"Missing {label}.")
    return path


class BridgeState:
    def __init__(self, idle_seconds: int) -> None:
        self.idle_seconds = idle_seconds
        self.last_activity = time.monotonic()
        self.lock = threading.Lock()
        self.write_lock = threading.Lock()

    def touch(self) -> None:
        with self.lock:
            self.last_activity = time.monotonic()

    def expired(self) -> bool:
        with self.lock:
            return time.monotonic() - self.last_activity >= self.idle_seconds


def comment_directory(harness: Path, proposal_id: str, create: bool) -> Path:
    proposals = require_directory(harness / "proposals", "proposals directory")
    proposal = proposals / proposal_id
    review = proposal / "review.json"
    if proposal.is_symlink() or not proposal.is_dir() or review.is_symlink() or not review.is_file():
        raise ValueError("Unknown proposal.")
    inbox = harness / "comment-inbox"
    if inbox.is_symlink():
        raise ValueError("Comment inbox must not be a symbolic link.")
    if not inbox.exists():
        if not create:
            return inbox / proposal_id
        inbox.mkdir(parents=True)
    require_directory(inbox, "comment inbox")
    directory = inbox / proposal_id
    if directory.is_symlink():
        raise ValueError("Proposal comment inbox must not be a symbolic link.")
    if create:
        directory.mkdir(parents=True, exist_ok=True)
    if not directory.exists():
        return directory
    return require_directory(directory, "proposal comment inbox")


def pending_requests(harness: Path, proposal_id: str) -> list[dict[str, Any]]:
    directory = comment_directory(harness, proposal_id, create=False)
    if not directory.exists():
        return []
    paths = sorted(directory.glob("*.json"))
    if len(paths) > MAX_INBOX_FILES:
        raise ValueError("Comment inbox is too large.")
    requests: list[dict[str, Any]] = []
    for path in paths:
        try:
            request = read_object(path)
        except (OSError, ValueError, json.JSONDecodeError):
            continue
        if request.get("id") == path.stem and request.get("proposalId") == proposal_id:
            requests.append(request)
    return requests


def write_request(harness: Path, request: dict[str, Any]) -> str:
    proposal_id = safe_id(request.get("proposalId"), "proposal id")
    request_id = safe_id(request.get("id"), "request id")
    if request.get("schemaVersion") != FORMAT_VERSION:
        raise ValueError("Unsupported comment request version.")
    directory = comment_directory(harness, proposal_id, create=True)
    destination = directory / f"{request_id}.json"
    if destination.is_symlink():
        raise ValueError("Comment request must not be a symbolic link.")
    if destination.exists():
        if read_object(destination) == request:
            return request_id
        raise FileExistsError("Comment request id already exists.")
    atomic_json(destination, request)
    return request_id


def handler_factory(
    harness: Path,
    project_id: str,
    token: str,
    activity: BridgeState,
) -> type[BaseHTTPRequestHandler]:
    class CommentHandler(BaseHTTPRequestHandler):
        server_version = "AgentFlowCommentBridge/1"

        def log_message(self, _format: str, *_args: object) -> None:
            return

        def end_headers(self) -> None:
            origin = self.headers.get("Origin")
            if origin == "null":
                self.send_header("Access-Control-Allow-Origin", "null")
                self.send_header("Vary", "Origin")
            super().end_headers()

        def send_json(self, status: int, payload: dict[str, Any]) -> None:
            encoded = json.dumps(payload, ensure_ascii=True, separators=(",", ":")).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(encoded)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(encoded)

        def request_allowed(self, require_token: bool = True) -> bool:
            host = self.headers.get("Host", "").split(":", 1)[0]
            if host not in {"127.0.0.1", "localhost"}:
                self.send_json(403, {"ok": False, "error": "Request host is not allowed."})
                return False
            if self.headers.get("Origin") not in {None, "null"}:
                self.send_json(403, {"ok": False, "error": "Request origin is not allowed."})
                return False
            if require_token and self.headers.get("X-AgentFlow-Token") != token:
                self.send_json(401, {"ok": False, "error": "Invalid comment writer token."})
                return False
            return True

        def do_OPTIONS(self) -> None:
            if not self.request_allowed(require_token=False):
                return
            requested_headers = self.headers.get("Access-Control-Request-Headers", "")
            allowed_headers = {
                item.strip().lower()
                for item in requested_headers.split(",")
                if item.strip()
            }
            if not allowed_headers.issubset({"content-type", "x-agentflow-token"}):
                self.send_json(403, {"ok": False, "error": "Request headers are not allowed."})
                return
            self.send_response(204)
            self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
            self.send_header("Access-Control-Allow-Headers", "Content-Type, X-AgentFlow-Token")
            self.send_header("Access-Control-Max-Age", "600")
            if self.headers.get("Access-Control-Request-Private-Network") == "true":
                self.send_header("Access-Control-Allow-Private-Network", "true")
            self.end_headers()

        def do_GET(self) -> None:
            if not self.request_allowed():
                return
            activity.touch()
            parsed = urlparse(self.path)
            if parsed.path == "/health":
                self.send_json(200, {"ok": True, "projectId": project_id})
                return
            if parsed.path != "/comments":
                self.send_json(404, {"ok": False, "error": "Unknown endpoint."})
                return
            try:
                proposal_id = safe_id(
                    parse_qs(parsed.query).get("proposal", [None])[0],
                    "proposal id",
                )
                requests = pending_requests(harness, proposal_id)
            except (OSError, ValueError) as error:
                self.send_json(400, {"ok": False, "error": str(error)})
                return
            self.send_json(200, {"ok": True, "requests": requests})

        def do_POST(self) -> None:
            if not self.request_allowed():
                return
            activity.touch()
            if urlparse(self.path).path != "/comments":
                self.send_json(404, {"ok": False, "error": "Unknown endpoint."})
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                length = 0
            if length < 2 or length > MAX_BODY_BYTES:
                self.send_json(413, {"ok": False, "error": "Comment request is too large."})
                return
            try:
                request = json.loads(self.rfile.read(length))
                if not isinstance(request, dict):
                    raise ValueError("Comment request must be an object.")
                with activity.write_lock:
                    request_id = write_request(harness, request)
            except FileExistsError as error:
                self.send_json(409, {"ok": False, "error": str(error)})
                return
            except (OSError, ValueError, json.JSONDecodeError) as error:
                self.send_json(400, {"ok": False, "error": str(error)})
                return
            self.send_json(201, {"ok": True, "requestId": request_id})

    return CommentHandler


def remove_own_state(path: Path, token: str) -> None:
    try:
        state = read_object(path)
        if state.get("pid") == os.getpid() and state.get("token") == token:
            path.unlink(missing_ok=True)
    except (OSError, ValueError, json.JSONDecodeError):
        return


def run_bridge(harness: Path, state_path: Path, token: str, idle_seconds: int) -> None:
    if harness.is_symlink():
        raise ValueError("Invalid AgentFlow harness directory.")
    harness = harness.resolve()
    if not harness.is_dir():
        raise ValueError("Invalid AgentFlow harness directory.")
    expected_state_path = harness / ".runtime" / "comment-bridge.json"
    if state_path.expanduser().resolve() != expected_state_path:
        raise ValueError("Invalid comment writer state path.")
    state_path = expected_state_path
    config = read_object(harness / "config.json")
    project_id = safe_id(config.get("projectId"), "project id")
    activity = BridgeState(idle_seconds)
    server = ThreadingHTTPServer(
        ("127.0.0.1", 0),
        handler_factory(harness, project_id, token, activity),
    )
    server.daemon_threads = True
    server.timeout = 1
    state_path.parent.mkdir(parents=True, exist_ok=True)
    os.chmod(state_path.parent, 0o700)
    atomic_json(
        state_path,
        {
            "schemaVersion": FORMAT_VERSION,
            "pid": os.getpid(),
            "port": server.server_port,
            "projectId": project_id,
            "token": token,
            "startedAt": utc_now(),
        },
    )
    try:
        while not activity.expired():
            server.handle_request()
    finally:
        server.server_close()
        remove_own_state(state_path, token)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Run the AgentFlow review comment writer.")
    parser.add_argument("--harness", required=True)
    parser.add_argument("--state", required=True)
    parser.add_argument("--token", required=True)
    parser.add_argument("--idle-seconds", type=int, default=28800)
    return parser


def main() -> int:
    args = build_parser().parse_args()
    if len(args.token) < 32 or args.idle_seconds < 60:
        return 2
    try:
        run_bridge(
            Path(args.harness).expanduser(),
            Path(args.state).expanduser(),
            args.token,
            args.idle_seconds,
        )
        return 0
    except (OSError, ValueError, json.JSONDecodeError):
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
