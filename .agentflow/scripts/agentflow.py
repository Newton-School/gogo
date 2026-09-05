#!/usr/bin/env python3
"""Manage a self-contained AgentFlow architecture harness."""

from __future__ import annotations

import argparse
import copy
import fcntl
import hashlib
import http.client
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import uuid
import webbrowser
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterator


ARCHITECTURE_SCHEMA_VERSION = 3
ARCHITECTURE_DIRECTORY = "architecture"
ARCHITECTURE_TRANSACTION = ".architecture-transaction.json"
V2_MIGRATION_TRANSACTION = ".migration-v2.json"
PAGE_TYPES = ("table", "flow", "document")
DEFAULT_HARNESS_PROFILE = "generic"
SUPPORTED_MIGRATION_PROFILES = {"generic", "django"}
LEGACY_DISPLAY_TITLE_MAXIMUM = 120
LEGACY_NAVIGATION_TITLE_MAXIMUM = 100
LEGACY_DESCRIPTION_MAXIMUM = 12000
PROPOSAL_STATUSES = {
    "draft",
    "under_review",
    "changes_requested",
    "approved",
    "rejected",
}
COMMENT_STATUSES = {"open", "resolved"}
COMMENT_ELEMENT_TYPES = {
    "page",
    "column",
    "row",
    "cell",
    "node",
    "edge",
    "group",
    "section",
    "block",
    "navigation",
}
TABLE_FORMATS = {"text", "code", "badge", "boolean", "reference"}
DOCUMENT_BLOCK_TYPES = {"paragraph", "list", "callout", "code", "key-value"}
EDGE_STYLES = {"normal", "async", "error"}
EVIDENCE_KINDS = {"code", "migration", "test", "config", "runtime", "documentation"}
ID_PATTERN = re.compile(r"^[a-z0-9]+(?:[._-][a-z0-9]+)*$")
HASH_PATTERN = re.compile(r"^[a-f0-9]{64}$")
REFERENCE_PATTERN = re.compile(
    r"^(?:navigation:[a-z0-9][a-z0-9._-]*|page:[a-z0-9][a-z0-9._-]*(?:#(?:column|row|cell|node|edge|group|section|block):[a-z0-9][a-z0-9._-]*)?)$"
)
MAX_FILE_BYTES = 8 * 1024 * 1024
MAX_COMMENT_INBOX_FILES = 500
COMMENT_BRIDGE_IDLE_SECONDS = 8 * 60 * 60

# Clients choose from this palette so the same technical concept always looks familiar.
FLOW_NODE_TYPES = {
    "actor": {"label": "Actor", "shape": "capsule", "tone": "stone"},
    "interface": {"label": "Interface", "shape": "window", "tone": "blue"},
    "action": {"label": "Action", "shape": "rounded", "tone": "blue"},
    "api": {"label": "API", "shape": "endpoint", "tone": "cyan"},
    "decision": {"label": "Decision", "shape": "diamond", "tone": "amber"},
    "database": {"label": "Database", "shape": "database", "tone": "green"},
    "cache": {"label": "Cache", "shape": "database", "tone": "teal"},
    "queue": {"label": "Queue", "shape": "queue", "tone": "orange"},
    "job": {"label": "Async job", "shape": "job", "tone": "orange"},
    "event": {"label": "Event", "shape": "event", "tone": "rose"},
    "external": {"label": "External service", "shape": "cloud", "tone": "cyan"},
    "subprocess": {"label": "Subflow", "shape": "subprocess", "tone": "slate"},
    "terminal": {"label": "Outcome", "shape": "capsule", "tone": "green"},
    "error": {"label": "Failure", "shape": "capsule", "tone": "red"},
}


class AgentFlowError(Exception):
    def __init__(self, code: str, message: str, details: Any = None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.details = details


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def canonical_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True)


def content_hash(value: Any) -> str:
    return hashlib.sha256(canonical_json(value).encode("utf-8")).hexdigest()


def duplicate_safe_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    value: dict[str, Any] = {}
    for key, item in pairs:
        if key in value:
            raise AgentFlowError("DUPLICATE_JSON_KEY", f"Duplicate JSON key: {key}")
        value[key] = item
    return value


def read_json(path: Path) -> Any:
    if path.is_symlink() or not path.is_file():
        raise AgentFlowError("FILE_NOT_FOUND", f"AgentFlow file is missing: {path}")
    if path.stat().st_size > MAX_FILE_BYTES:
        raise AgentFlowError("FILE_TOO_LARGE", f"AgentFlow file is too large: {path}")
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=duplicate_safe_object)
    except json.JSONDecodeError as error:
        raise AgentFlowError("INVALID_JSON", f"Invalid JSON in {path}: {error}") from error


def atomic_json(path: Path, value: Any, mode: int = 0o644) -> None:
    if path.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", f"Refusing to replace symbolic link: {path}")
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(
        dir=path.parent, prefix=f".{path.name}.", suffix=".tmp"
    )
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=True, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def safe_id(value: Any, label: str = "id", maximum: int = 160) -> str:
    if (
        not isinstance(value, str)
        or not value
        or len(value) > maximum
        or ID_PATTERN.fullmatch(value) is None
    ):
        raise AgentFlowError(
            "INVALID_ID",
            f"{label} must use lowercase letters and numbers separated by '.', '_', or '-'.",
        )
    return value


def safe_hash(value: Any, label: str = "architecture hash") -> str:
    if not isinstance(value, str) or HASH_PATTERN.fullmatch(value) is None:
        raise AgentFlowError("INVALID_HASH", f"{label} must be a SHA-256 content hash.")
    return value


def resolve_project(argument: str) -> tuple[Path, Path]:
    current = Path(argument).expanduser().resolve()
    if current.is_file():
        current = current.parent
    for candidate in (current, *current.parents):
        harness = candidate / ".agentflow"
        if harness.is_symlink():
            raise AgentFlowError("UNSAFE_PATH", ".agentflow must not be a symbolic link.")
        if (harness / "config.json").is_file():
            return candidate, harness
    raise AgentFlowError("HARNESS_NOT_FOUND", "No .agentflow/config.json found in this project.")


def require_directory(path: Path, label: str, create: bool = False) -> Path:
    if path.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", f"{label} must not be a symbolic link: {path}")
    if create:
        path.mkdir(parents=True, exist_ok=True)
    if not path.is_dir():
        raise AgentFlowError("DIRECTORY_NOT_FOUND", f"{label} is missing: {path}")
    return path


@contextmanager
def project_lock(harness: Path) -> Iterator[None]:
    lock_path = harness / ".lock"
    if lock_path.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", "AgentFlow lock must not be a symbolic link.")
    descriptor = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o600)
    with os.fdopen(descriptor, "r+", encoding="utf-8") as handle:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def issue(issues: list[dict[str, str]], path: str, message: str) -> None:
    issues.append({"path": path, "message": message})


def expect_object(value: Any, path: str, issues: list[dict[str, str]]) -> dict[str, Any] | None:
    if not isinstance(value, dict):
        issue(issues, path, "Must be an object.")
        return None
    return value


def expect_list(value: Any, path: str, issues: list[dict[str, str]]) -> list[Any] | None:
    if not isinstance(value, list):
        issue(issues, path, "Must be an array.")
        return None
    return value


def expect_string(
    value: Any,
    path: str,
    issues: list[dict[str, str]],
    *,
    required: bool = True,
    maximum: int = 1000,
    identifier: bool = False,
) -> str | None:
    if value is None and not required:
        return None
    if not isinstance(value, str) or (required and not value.strip()):
        issue(issues, path, "Must be a non-empty string." if required else "Must be a string.")
        return None
    if len(value) > maximum:
        issue(issues, path, f"Must be at most {maximum} characters.")
    if identifier and ID_PATTERN.fullmatch(value) is None:
        issue(issues, path, "Must be a stable lowercase identifier.")
    return value


def validate_exact_keys(
    value: dict[str, Any],
    path: str,
    issues: list[dict[str, str]],
    required: set[str],
    optional: set[str] | None = None,
) -> None:
    optional = optional or set()
    for key in sorted(required - value.keys()):
        issue(issues, f"{path}.{key}", "Field is required.")
    for key in sorted(value.keys() - required - optional):
        issue(issues, f"{path}.{key}", "Unknown field.")


def validate_details(value: Any, path: str, issues: list[dict[str, str]]) -> None:
    if value is None:
        return
    details = expect_list(value, path, issues)
    if details is None:
        return
    seen: set[str] = set()
    for index, raw in enumerate(details):
        item_path = f"{path}[{index}]"
        item = expect_object(raw, item_path, issues)
        if item is None:
            continue
        validate_exact_keys(item, item_path, issues, {"id", "label", "value"})
        item_id = expect_string(item.get("id"), f"{item_path}.id", issues, identifier=True)
        expect_string(item.get("label"), f"{item_path}.label", issues, maximum=80)
        expect_string(
            item.get("value"),
            f"{item_path}.value",
            issues,
            maximum=LEGACY_DESCRIPTION_MAXIMUM,
        )
        if item_id in seen:
            issue(issues, f"{item_path}.id", "Detail ids must be unique.")
        elif item_id:
            seen.add(item_id)


def validate_table_page(page: dict[str, Any], path: str, issues: list[dict[str, str]]) -> None:
    columns = expect_list(page.get("columns"), f"{path}.columns", issues)
    rows = expect_list(page.get("rows"), f"{path}.rows", issues)
    column_ids: set[str] = set()
    if columns is not None:
        if not columns or len(columns) > 16:
            issue(issues, f"{path}.columns", "A table needs between 1 and 16 columns.")
        for index, raw in enumerate(columns):
            item_path = f"{path}.columns[{index}]"
            column = expect_object(raw, item_path, issues)
            if column is None:
                continue
            validate_exact_keys(column, item_path, issues, {"id", "label"}, {"format", "description"})
            column_id = expect_string(
                column.get("id"), f"{item_path}.id", issues, identifier=True, maximum=80
            )
            expect_string(column.get("label"), f"{item_path}.label", issues, maximum=80)
            expect_string(
                column.get("description"),
                f"{item_path}.description",
                issues,
                required=False,
                maximum=300,
            )
            if column.get("format", "text") not in TABLE_FORMATS:
                issue(issues, f"{item_path}.format", "Unknown table column format.")
            if column_id in column_ids:
                issue(issues, f"{item_path}.id", "Column ids must be unique.")
            elif column_id:
                column_ids.add(column_id)
    if rows is not None:
        if len(rows) > 2000:
            issue(issues, f"{path}.rows", "A table may contain at most 2000 rows.")
        row_ids: set[str] = set()
        for index, raw in enumerate(rows):
            item_path = f"{path}.rows[{index}]"
            row = expect_object(raw, item_path, issues)
            if row is None:
                continue
            validate_exact_keys(row, item_path, issues, {"id", "cells"}, {"details"})
            row_id = expect_string(row.get("id"), f"{item_path}.id", issues, identifier=True)
            cells = expect_object(row.get("cells"), f"{item_path}.cells", issues)
            if cells is not None:
                for column_id, cell in cells.items():
                    if column_id not in column_ids:
                        issue(issues, f"{item_path}.cells.{column_id}", "Cell uses an unknown column.")
                    if not (
                        cell is None
                        or isinstance(cell, (str, int, float, bool))
                        or (
                            isinstance(cell, list)
                            and all(isinstance(item, str) and len(item) <= 300 for item in cell)
                        )
                    ):
                        issue(
                            issues,
                            f"{item_path}.cells.{column_id}",
                            "Cell value must be scalar or a string array.",
                        )
            validate_details(row.get("details"), f"{item_path}.details", issues)
            if row_id in row_ids:
                issue(issues, f"{item_path}.id", "Row ids must be unique.")
            elif row_id:
                row_ids.add(row_id)


def validate_flow_page(page: dict[str, Any], path: str, issues: list[dict[str, str]]) -> None:
    nodes = expect_list(page.get("nodes"), f"{path}.nodes", issues)
    edges = expect_list(page.get("edges"), f"{path}.edges", issues)
    groups = expect_list(page.get("groups", []), f"{path}.groups", issues)
    node_ids: set[str] = set()
    node_types: dict[str, str] = {}
    if nodes is not None:
        if not nodes or len(nodes) > 300:
            issue(issues, f"{path}.nodes", "A flow needs between 1 and 300 nodes.")
        for index, raw in enumerate(nodes):
            item_path = f"{path}.nodes[{index}]"
            node = expect_object(raw, item_path, issues)
            if node is None:
                continue
            validate_exact_keys(
                node,
                item_path,
                issues,
                {"id", "type", "title"},
                {"subtitle", "description", "details"},
            )
            node_id = expect_string(node.get("id"), f"{item_path}.id", issues, identifier=True)
            node_type = node.get("type")
            if node_type not in FLOW_NODE_TYPES:
                issue(issues, f"{item_path}.type", "Unknown AgentFlow node type.")
            expect_string(node.get("title"), f"{item_path}.title", issues, maximum=120)
            expect_string(
                node.get("subtitle"), f"{item_path}.subtitle", issues, required=False, maximum=180
            )
            expect_string(
                node.get("description"),
                f"{item_path}.description", issues, required=False, maximum=LEGACY_DESCRIPTION_MAXIMUM,
            )
            validate_details(node.get("details"), f"{item_path}.details", issues)
            if node_id in node_ids:
                issue(issues, f"{item_path}.id", "Node ids must be unique.")
            elif node_id:
                node_ids.add(node_id)
                if isinstance(node_type, str):
                    node_types[node_id] = node_type
    outgoing: dict[str, list[dict[str, Any]]] = {node_id: [] for node_id in node_ids}
    incoming: dict[str, int] = {node_id: 0 for node_id in node_ids}
    edge_ids: set[str] = set()
    if edges is not None:
        if len(edges) > 600:
            issue(issues, f"{path}.edges", "A flow may contain at most 600 edges.")
        for index, raw in enumerate(edges):
            item_path = f"{path}.edges[{index}]"
            edge = expect_object(raw, item_path, issues)
            if edge is None:
                continue
            validate_exact_keys(
                edge,
                item_path,
                issues,
                {"id", "from", "to"},
                {"label", "style", "description"},
            )
            edge_id = expect_string(edge.get("id"), f"{item_path}.id", issues, identifier=True)
            source = expect_string(edge.get("from"), f"{item_path}.from", issues, identifier=True)
            target = expect_string(edge.get("to"), f"{item_path}.to", issues, identifier=True)
            label = expect_string(
                edge.get("label"), f"{item_path}.label", issues, required=False, maximum=80
            )
            expect_string(
                edge.get("description"),
                f"{item_path}.description",
                issues,
                required=False,
                maximum=LEGACY_DESCRIPTION_MAXIMUM,
            )
            if edge.get("style", "normal") not in EDGE_STYLES:
                issue(issues, f"{item_path}.style", "Edge style must be normal, async, or error.")
            if source not in node_ids:
                issue(issues, f"{item_path}.from", "Edge source does not exist.")
            if target not in node_ids:
                issue(issues, f"{item_path}.to", "Edge target does not exist.")
            if source == target:
                issue(issues, item_path, "Self-referencing edges are not supported.")
            if edge_id in edge_ids:
                issue(issues, f"{item_path}.id", "Edge ids must be unique.")
            elif edge_id:
                edge_ids.add(edge_id)
            if source in outgoing and target in incoming:
                outgoing[source].append(edge)
                incoming[target] += 1
            if source in node_types and node_types[source] == "decision" and not label:
                issue(issues, f"{item_path}.label", "Edges leaving a decision require a label.")
    for node_id, node_type in node_types.items():
        count = len(outgoing.get(node_id, []))
        if node_type == "decision" and count < 2:
            issue(issues, f"{path}.nodes.{node_id}", "A decision needs at least two outgoing edges.")
        if node_type == "decision" and count >= 2:
            labels = [str(edge.get("label", "")).strip().casefold() for edge in outgoing[node_id]]
            if len(labels) != len(set(labels)):
                issue(issues, f"{path}.nodes.{node_id}", "Decision edge labels must be distinct.")
        if node_type in {"terminal", "error"} and count:
            issue(issues, f"{path}.nodes.{node_id}", "Outcome and failure nodes cannot have outgoing edges.")
        if node_type not in {"terminal", "error", "job"} and not count:
            issue(issues, f"{path}.nodes.{node_id}", "Non-outcome nodes must lead somewhere.")
        if len(node_ids) > 1 and not outgoing.get(node_id) and incoming.get(node_id, 0) == 0:
            issue(issues, f"{path}.nodes.{node_id}", "Node is disconnected from the flow.")
    entry_nodes = [node_id for node_id in node_ids if incoming.get(node_id, 0) == 0]
    reachable: set[str] = set()
    pending = list(entry_nodes)
    while pending:
        node_id = pending.pop()
        if node_id in reachable:
            continue
        reachable.add(node_id)
        pending.extend(
            edge["to"]
            for edge in outgoing.get(node_id, [])
            if isinstance(edge.get("to"), str) and edge["to"] not in reachable
        )
    for node_id in sorted(node_ids - reachable):
        issue(issues, f"{path}.nodes.{node_id}", "Node is not reachable from a flow entry.")
    if groups is not None:
        group_ids: set[str] = set()
        grouped_nodes: set[str] = set()
        for index, raw in enumerate(groups):
            item_path = f"{path}.groups[{index}]"
            group = expect_object(raw, item_path, issues)
            if group is None:
                continue
            validate_exact_keys(group, item_path, issues, {"id", "title", "nodeIds"})
            group_id = expect_string(group.get("id"), f"{item_path}.id", issues, identifier=True)
            expect_string(group.get("title"), f"{item_path}.title", issues, maximum=100)
            member_ids = expect_list(group.get("nodeIds"), f"{item_path}.nodeIds", issues)
            if member_ids is not None:
                if not member_ids:
                    issue(issues, f"{item_path}.nodeIds", "A group needs at least one node.")
                for member_index, member_id in enumerate(member_ids):
                    if member_id not in node_ids:
                        issue(
                            issues,
                            f"{item_path}.nodeIds[{member_index}]",
                            "Group node does not exist.",
                        )
                    if member_id in grouped_nodes:
                        issue(
                            issues,
                            f"{item_path}.nodeIds[{member_index}]",
                            "A node may belong to only one group.",
                        )
                    elif isinstance(member_id, str):
                        grouped_nodes.add(member_id)
            if group_id in group_ids:
                issue(issues, f"{item_path}.id", "Group ids must be unique.")
            elif group_id:
                group_ids.add(group_id)
    layout = page.get("layout")
    if layout is not None:
        layout_value = expect_object(layout, f"{path}.layout", issues)
        if layout_value is not None:
            validate_exact_keys(layout_value, f"{path}.layout", issues, set(), {"direction"})
            if layout_value.get("direction", "down") != "down":
                issue(issues, f"{path}.layout.direction", "AgentFlow flows must use vertical direction: down.")


def validate_document_page(page: dict[str, Any], path: str, issues: list[dict[str, str]]) -> None:
    sections = expect_list(page.get("sections"), f"{path}.sections", issues)
    if sections is None:
        return
    if not sections or len(sections) > 80:
        issue(issues, f"{path}.sections", "A document needs between 1 and 80 sections.")
    section_ids: set[str] = set()
    block_ids: set[str] = set()
    for section_index, raw in enumerate(sections):
        section_path = f"{path}.sections[{section_index}]"
        section = expect_object(raw, section_path, issues)
        if section is None:
            continue
        validate_exact_keys(section, section_path, issues, {"id", "title", "blocks"})
        section_id = expect_string(section.get("id"), f"{section_path}.id", issues, identifier=True)
        expect_string(section.get("title"), f"{section_path}.title", issues, maximum=140)
        blocks = expect_list(section.get("blocks"), f"{section_path}.blocks", issues)
        if blocks is not None:
            if not blocks:
                issue(issues, f"{section_path}.blocks", "A section needs at least one block.")
            for block_index, raw_block in enumerate(blocks):
                block_path = f"{section_path}.blocks[{block_index}]"
                block = expect_object(raw_block, block_path, issues)
                if block is None:
                    continue
                block_type = block.get("type")
                block_fields = {
                    "paragraph": {"text"},
                    "list": {"items"},
                    "callout": {"text", "tone"},
                    "code": {"text", "language"},
                    "key-value": {"values"},
                }
                validate_exact_keys(
                    block,
                    block_path,
                    issues,
                    {"id", "type"},
                    block_fields.get(
                        block_type,
                        {"text", "items", "language", "values", "tone"},
                    ),
                )
                block_id = expect_string(block.get("id"), f"{block_path}.id", issues, identifier=True)
                if block_type not in DOCUMENT_BLOCK_TYPES:
                    issue(issues, f"{block_path}.type", "Unknown document block type.")
                if block_type in {"paragraph", "callout", "code"}:
                    expect_string(block.get("text"), f"{block_path}.text", issues, maximum=12000)
                if block_type == "code":
                    expect_string(
                        block.get("language"),
                        f"{block_path}.language",
                        issues,
                        required=False,
                        maximum=40,
                    )
                if block_type == "callout" and block.get("tone", "note") not in {
                    "note",
                    "warning",
                    "success",
                }:
                    issue(
                        issues,
                        f"{block_path}.tone",
                        "Callout tone must be note, warning, or success.",
                    )
                if block_type == "list":
                    items = expect_list(block.get("items"), f"{block_path}.items", issues)
                    if items is not None and (
                        not items
                        or not all(isinstance(item, str) and 0 < len(item) <= 1000 for item in items)
                    ):
                        issue(issues, f"{block_path}.items", "List items must be non-empty strings.")
                if block_type == "key-value":
                    values = expect_list(block.get("values"), f"{block_path}.values", issues)
                    if values is not None:
                        if not values:
                            issue(
                                issues,
                                f"{block_path}.values",
                                "Key-value blocks need at least one item.",
                            )
                        for value_index, raw_value in enumerate(values):
                            value_path = f"{block_path}.values[{value_index}]"
                            value = expect_object(raw_value, value_path, issues)
                            if value is not None:
                                validate_exact_keys(value, value_path, issues, {"label", "value"})
                                expect_string(
                                    value.get("label"), f"{value_path}.label", issues, maximum=100
                                )
                                expect_string(
                                    value.get("value"), f"{value_path}.value", issues, maximum=2000
                                )
                if block_id in block_ids:
                    issue(issues, f"{block_path}.id", "Block ids must be unique within the page.")
                elif block_id:
                    block_ids.add(block_id)
        if section_id in section_ids:
            issue(issues, f"{section_path}.id", "Section ids must be unique.")
        elif section_id:
            section_ids.add(section_id)


def validate_page(raw: Any, path: str, issues: list[dict[str, str]]) -> None:
    page = expect_object(raw, path, issues)
    if page is None:
        return
    page_type = page.get("type")
    required = {"schemaVersion", "id", "type", "title"}
    optional = {"summary", "details"}
    if page_type == "table":
        required |= {"columns", "rows"}
    elif page_type == "flow":
        required |= {"nodes", "edges"}
        optional |= {"groups", "layout"}
    elif page_type == "document":
        required |= {"sections"}
    validate_exact_keys(page, path, issues, required, optional)
    if page.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION:
        issue(issues, f"{path}.schemaVersion", f"Must equal {ARCHITECTURE_SCHEMA_VERSION}.")
    expect_string(page.get("id"), f"{path}.id", issues, identifier=True)
    if page_type not in PAGE_TYPES:
        issue(issues, f"{path}.type", "Page type must be table, flow, or document.")
    expect_string(page.get("title"), f"{path}.title", issues, maximum=180)
    expect_string(page.get("summary"), f"{path}.summary", issues, required=False, maximum=1200)
    validate_details(page.get("details"), f"{path}.details", issues)
    if page_type == "table":
        validate_table_page(page, path, issues)
    elif page_type == "flow":
        validate_flow_page(page, path, issues)
    elif page_type == "document":
        validate_document_page(page, path, issues)


def validate_navigation(
    raw: Any,
    pages: dict[str, dict[str, Any]],
    path: str,
    issues: list[dict[str, str]],
) -> None:
    navigation = expect_object(raw, path, issues)
    if navigation is None:
        return
    validate_exact_keys(navigation, path, issues, {"schemaVersion", "items"})
    if navigation.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION:
        issue(issues, f"{path}.schemaVersion", f"Must equal {ARCHITECTURE_SCHEMA_VERSION}.")
    items = expect_list(navigation.get("items"), f"{path}.items", issues)
    navigation_ids: set[str] = set()
    page_references: dict[str, int] = {}

    def visit(raw_item: Any, item_path: str, depth: int) -> None:
        item = expect_object(raw_item, item_path, issues)
        if item is None:
            return
        if depth > 8:
            issue(issues, item_path, "Navigation may be nested at most 8 levels.")
        item_type = item.get("type")
        required = (
            {"id", "type", "title", "children"}
            if item_type == "folder"
            else {"id", "type", "title", "pageId"}
        )
        validate_exact_keys(item, item_path, issues, required)
        item_id = expect_string(item.get("id"), f"{item_path}.id", issues, identifier=True)
        expect_string(item.get("title"), f"{item_path}.title", issues, maximum=100)
        if item_type not in {"folder", "page"}:
            issue(issues, f"{item_path}.type", "Navigation item type must be folder or page.")
        if item_id in navigation_ids:
            issue(issues, f"{item_path}.id", "Navigation ids must be globally unique.")
        elif item_id:
            navigation_ids.add(item_id)
        if item_type == "folder":
            children = expect_list(item.get("children"), f"{item_path}.children", issues)
            if children is not None:
                if not children:
                    issue(issues, f"{item_path}.children", "Navigation folders cannot be empty.")
                for index, child in enumerate(children):
                    visit(child, f"{item_path}.children[{index}]", depth + 1)
        elif item_type == "page":
            page_id = expect_string(
                item.get("pageId"), f"{item_path}.pageId", issues, identifier=True
            )
            if page_id and page_id not in pages:
                issue(issues, f"{item_path}.pageId", "Navigation references an unknown page.")
            if page_id:
                page_references[page_id] = page_references.get(page_id, 0) + 1

    if items is not None:
        for index, item in enumerate(items):
            visit(item, f"{path}.items[{index}]", 1)
    for page_id in sorted(pages):
        count = page_references.get(page_id, 0)
        if count == 0:
            issue(issues, path, f"Page is missing from navigation: {page_id}")
        elif count > 1:
            issue(issues, path, f"Page appears more than once in navigation: {page_id}")


def validate_snapshot(snapshot: dict[str, Any]) -> list[dict[str, str]]:
    issues: list[dict[str, str]] = []
    pages = snapshot.get("pages")
    if not isinstance(pages, dict):
        issue(issues, "pages", "Must be an object keyed by page id.")
        pages = {}
    for page_id, page in pages.items():
        if not isinstance(page_id, str) or ID_PATTERN.fullmatch(page_id) is None:
            issue(issues, f"pages.{page_id}", "Page map key must be a stable id.")
        validate_page(page, f"pages.{page_id}", issues)
        if isinstance(page, dict) and page.get("id") != page_id:
            issue(issues, f"pages.{page_id}.id", "Page id must match its map key.")
    validate_navigation(snapshot.get("navigation"), pages, "navigation", issues)
    return issues


def snapshot_payload(snapshot: dict[str, Any]) -> dict[str, Any]:
    return {
        "navigation": copy.deepcopy(snapshot["navigation"]),
        "pages": [
            copy.deepcopy(snapshot["pages"][page_id]) for page_id in sorted(snapshot["pages"])
        ],
    }


def snapshot_integrity_payload(snapshot: dict[str, Any]) -> dict[str, Any]:
    return {
        "navigation": snapshot["navigation"],
        "pages": {
            page_id: snapshot["pages"][page_id] for page_id in sorted(snapshot["pages"])
        },
    }


def architecture_directory(harness: Path, create: bool = False) -> Path:
    return require_directory(harness / ARCHITECTURE_DIRECTORY, "current architecture directory", create)


def legacy_revision_directory(harness: Path, revision_id: str) -> Path:
    safe_id(revision_id, "legacy revision id")
    archived = harness / "legacy-v3" / "revisions" / revision_id
    revisions_root = harness / "legacy-v3" / "revisions" if archived.exists() else harness / "revisions"
    revisions = require_directory(revisions_root, "legacy revisions directory")
    directory = revisions / revision_id
    if directory.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", f"Legacy revision must not be a symbolic link: {revision_id}")
    return directory


def architecture_page_path(pages_directory: Path, page_id: str) -> Path:
    safe_id(page_id, "page id")
    return pages_directory / f"{page_id}.json"


def architecture_manifest(
    snapshot: dict[str, Any],
    proposal_id: str | None,
    applied_at: str,
    applied_by: str,
) -> dict[str, Any]:
    digest = content_hash(snapshot_integrity_payload(snapshot))
    return {
        "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
        "hash": digest,
        "pageCount": len(snapshot["pages"]),
        "proposalId": proposal_id,
        "appliedAt": applied_at,
        "appliedBy": applied_by,
    }


def write_architecture_directory(
    destination: Path,
    snapshot: dict[str, Any],
    proposal_id: str | None,
    applied_at: str,
    applied_by: str,
) -> dict[str, Any]:
    if destination.exists() or destination.is_symlink():
        raise AgentFlowError("ARCHITECTURE_EXISTS", "Current architecture already exists.")
    manifest = architecture_manifest(snapshot, proposal_id, applied_at, applied_by)
    destination.mkdir()
    atomic_json(destination / "navigation.json", snapshot["navigation"])
    pages_directory = destination / "pages"
    pages_directory.mkdir()
    for page_id, page in sorted(snapshot["pages"].items()):
        atomic_json(architecture_page_path(pages_directory, page_id), page)
    atomic_json(destination / "manifest.json", manifest)
    return manifest


def load_architecture_without_recovery(harness: Path) -> tuple[dict[str, Any], dict[str, Any]]:
    directory = architecture_directory(harness)
    manifest = read_json(directory / "manifest.json")
    navigation = read_json(directory / "navigation.json")
    pages_directory = require_directory(directory / "pages", "current architecture pages directory")
    pages: dict[str, dict[str, Any]] = {}
    for path in sorted(pages_directory.glob("*.json")):
        page = read_json(path)
        if not isinstance(page, dict):
            raise AgentFlowError("CORRUPT_ARCHITECTURE", f"Architecture page must be an object: {path}")
        page_id = safe_id(page.get("id"), "page id")
        if path.stem != page_id or page_id in pages:
            raise AgentFlowError("CORRUPT_ARCHITECTURE", f"Architecture page identity mismatch: {path}")
        pages[page_id] = page
    snapshot = {"navigation": navigation, "pages": pages}
    issues = validate_snapshot(snapshot)
    digest = content_hash(snapshot_integrity_payload(snapshot))
    if (
        not isinstance(manifest, dict)
        or manifest.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION
        or manifest.get("hash") != digest
        or manifest.get("pageCount") != len(pages)
        or (
            manifest.get("proposalId") is not None
            and not isinstance(manifest.get("proposalId"), str)
        )
        or not isinstance(manifest.get("appliedAt"), str)
        or not isinstance(manifest.get("appliedBy"), str)
    ):
        raise AgentFlowError("CORRUPT_ARCHITECTURE", "Current architecture manifest is invalid.")
    if issues:
        raise AgentFlowError(
            "CORRUPT_ARCHITECTURE", "Current architecture is invalid.", {"issues": issues}
        )
    return manifest, snapshot


def restore_architecture_page(pages_directory: Path, page_id: str, page: Any) -> None:
    path = architecture_page_path(pages_directory, page_id)
    if page is None:
        if path.is_symlink():
            raise AgentFlowError("UNSAFE_PATH", f"Architecture page must not be a symbolic link: {path}")
        path.unlink(missing_ok=True)
        return
    atomic_json(path, page)


def recover_architecture_transaction(harness: Path) -> None:
    journal_path = harness / ARCHITECTURE_TRANSACTION
    if not journal_path.exists():
        return
    journal = read_json(journal_path)
    if not isinstance(journal, dict):
        raise AgentFlowError("CORRUPT_ARCHITECTURE", "Architecture recovery journal is invalid.")
    required = {
        "schemaVersion",
        "beforeManifest",
        "afterManifest",
        "beforeNavigation",
        "afterNavigation",
        "beforePages",
        "afterPages",
        "beforeState",
        "afterState",
        "beforeReview",
        "afterReview",
        "reviewPath",
    }
    if set(journal) != required or journal.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION:
        raise AgentFlowError("CORRUPT_ARCHITECTURE", "Architecture recovery journal is invalid.")
    relative_review, review_path = project_relative_path(
        harness, journal["reviewPath"], "Architecture recovery review path"
    )
    if relative_review != journal["reviewPath"] or not relative_review.startswith("proposals/"):
        raise AgentFlowError("CORRUPT_ARCHITECTURE", "Architecture recovery journal is invalid.")
    directory = architecture_directory(harness)
    pages_directory = require_directory(directory / "pages", "current architecture pages directory")
    manifest_path = directory / "manifest.json"
    current_manifest = read_json(manifest_path) if manifest_path.exists() else None
    completed = (
        isinstance(current_manifest, dict)
        and isinstance(journal["afterManifest"], dict)
        and current_manifest.get("hash") == journal["afterManifest"].get("hash")
    )
    if completed:
        atomic_json(harness / "state.json", journal["afterState"])
        atomic_json(review_path, journal["afterReview"])
    else:
        for page_id, page in journal["beforePages"].items():
            restore_architecture_page(pages_directory, page_id, page)
        atomic_json(directory / "navigation.json", journal["beforeNavigation"])
        atomic_json(manifest_path, journal["beforeManifest"])
        atomic_json(harness / "state.json", journal["beforeState"])
        atomic_json(review_path, journal["beforeReview"])
    if journal_path.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", "Architecture recovery journal must not be a symbolic link.")
    journal_path.unlink()


def load_architecture(harness: Path) -> tuple[dict[str, Any], dict[str, Any]]:
    recover_architecture_transaction(harness)
    return load_architecture_without_recovery(harness)


def initialize_architecture(
    harness: Path,
    snapshot: dict[str, Any],
    proposal_id: str | None,
    applied_at: str,
    applied_by: str,
) -> dict[str, Any]:
    issues = validate_snapshot(snapshot)
    if issues:
        raise AgentFlowError("VALIDATION_FAILED", "Architecture is invalid.", {"issues": issues})
    destination = harness / ARCHITECTURE_DIRECTORY
    if destination.exists() or destination.is_symlink():
        raise AgentFlowError("ARCHITECTURE_EXISTS", "Current architecture already exists.")
    temporary = harness / f".{ARCHITECTURE_DIRECTORY}-{uuid.uuid4().hex}"
    try:
        manifest = write_architecture_directory(
            temporary, snapshot, proposal_id, applied_at, applied_by
        )
        os.replace(temporary, destination)
    finally:
        if temporary.exists():
            shutil.rmtree(temporary)
    return manifest


def apply_architecture(
    harness: Path,
    before_manifest: dict[str, Any],
    before: dict[str, Any],
    candidate: dict[str, Any],
    proposal_id: str,
    approved_at: str,
    approved_by: str,
    before_state: dict[str, Any],
    after_state: dict[str, Any],
    review_path: Path,
    before_review: dict[str, Any],
    after_review: dict[str, Any],
) -> dict[str, Any]:
    after_manifest = architecture_manifest(candidate, proposal_id, approved_at, approved_by)
    changed_page_ids = sorted(
        page_id
        for page_id in set(before["pages"]) | set(candidate["pages"])
        if before["pages"].get(page_id) != candidate["pages"].get(page_id)
    )
    navigation_changed = before["navigation"] != candidate["navigation"]
    if not changed_page_ids and not navigation_changed:
        raise AgentFlowError("VALIDATION_FAILED", "Proposal must change the architecture.")
    relative_review = review_path.relative_to(harness).as_posix()
    journal = {
        "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
        "beforeManifest": before_manifest,
        "afterManifest": after_manifest,
        "beforeNavigation": before["navigation"],
        "afterNavigation": candidate["navigation"],
        "beforePages": {page_id: before["pages"].get(page_id) for page_id in changed_page_ids},
        "afterPages": {page_id: candidate["pages"].get(page_id) for page_id in changed_page_ids},
        "beforeState": before_state,
        "afterState": after_state,
        "beforeReview": before_review,
        "afterReview": after_review,
        "reviewPath": relative_review,
    }
    journal_path = harness / ARCHITECTURE_TRANSACTION
    atomic_json(journal_path, journal)
    directory = architecture_directory(harness)
    pages_directory = require_directory(directory / "pages", "current architecture pages directory")
    try:
        for page_id, page in journal["afterPages"].items():
            restore_architecture_page(pages_directory, page_id, page)
        if navigation_changed:
            atomic_json(directory / "navigation.json", candidate["navigation"])
        atomic_json(harness / "state.json", after_state)
        atomic_json(review_path, after_review)
        atomic_json(directory / "manifest.json", after_manifest)
    except Exception:
        recover_architecture_transaction(harness)
        raise
    journal_path.unlink(missing_ok=True)
    return after_manifest


def load_state(harness: Path) -> dict[str, Any]:
    recover_architecture_transaction(harness)
    state = read_json(harness / "state.json")
    if not isinstance(state, dict):
        raise AgentFlowError("CORRUPT_STATE", "AgentFlow state must be an object.")
    if state.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION:
        raise AgentFlowError(
            "MIGRATION_REQUIRED",
            "This project uses an older AgentFlow architecture format. Run migration check first.",
        )
    safe_hash(state.get("implementedArchitectureHash"), "implemented architecture hash")
    if not isinstance(state.get("updatedAt"), str):
        raise AgentFlowError("CORRUPT_STATE", "AgentFlow state is missing its update timestamp.")
    load_architecture_without_recovery(harness)
    return state


def proposal_paths(harness: Path, proposal_id: str) -> tuple[Path, Path, Path]:
    safe_id(proposal_id, "proposal id")
    proposals = require_directory(harness / "proposals", "proposals directory")
    directory = proposals / proposal_id
    if directory.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", f"Proposal must not be a symbolic link: {proposal_id}")
    return directory, directory / "proposal.json", directory / "review.json"


def normalize_proposal(raw: Any, base_architecture_hash: str) -> dict[str, Any]:
    if not isinstance(raw, dict):
        raise AgentFlowError("INVALID_PROPOSAL", "Proposal input must be an object.")
    proposal = copy.deepcopy(raw)
    proposal.setdefault("schemaVersion", ARCHITECTURE_SCHEMA_VERSION)
    proposal.setdefault("baseArchitectureHash", base_architecture_hash)
    proposal.setdefault("createdAt", utc_now())
    return proposal


def validate_proposal_shape(proposal: dict[str, Any]) -> list[dict[str, str]]:
    issues: list[dict[str, str]] = []
    validate_exact_keys(
        proposal,
        "proposal",
        issues,
        {
            "schemaVersion",
            "id",
            "title",
            "summary",
            "baseArchitectureHash",
            "createdAt",
            "changes",
        },
    )
    if proposal.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION:
        issue(
            issues,
            "proposal.schemaVersion",
            f"Must equal {ARCHITECTURE_SCHEMA_VERSION}.",
        )
    expect_string(proposal.get("id"), "proposal.id", issues, identifier=True)
    expect_string(proposal.get("title"), "proposal.title", issues, maximum=200)
    expect_string(proposal.get("summary"), "proposal.summary", issues, maximum=1400)
    try:
        safe_hash(proposal.get("baseArchitectureHash"), "proposal.baseArchitectureHash")
    except AgentFlowError as error:
        issue(issues, "proposal.baseArchitectureHash", error.message)
    expect_string(proposal.get("createdAt"), "proposal.createdAt", issues, maximum=100)
    changes = expect_list(proposal.get("changes"), "proposal.changes", issues)
    seen_pages: set[str] = set()
    navigation_changes = 0
    if changes is not None:
        if not changes:
            issue(issues, "proposal.changes", "At least one explicit change is required.")
        for index, raw in enumerate(changes):
            path = f"proposal.changes[{index}]"
            change = expect_object(raw, path, issues)
            if change is None:
                continue
            operation = change.get("op")
            if operation == "set-navigation":
                validate_exact_keys(change, path, issues, {"op", "navigation"})
                navigation_changes += 1
                # Page references are validated after changes are applied.
                navigation = change.get("navigation")
                if not isinstance(navigation, dict):
                    issue(issues, f"{path}.navigation", "Must be an object.")
            elif operation == "upsert-page":
                validate_exact_keys(change, path, issues, {"op", "page"})
                page = change.get("page")
                validate_page(page, f"{path}.page", issues)
                page_id = page.get("id") if isinstance(page, dict) else None
                if isinstance(page_id, str):
                    if page_id in seen_pages:
                        issue(issues, f"{path}.page.id", "A proposal may change a page only once.")
                    seen_pages.add(page_id)
            elif operation == "delete-page":
                validate_exact_keys(change, path, issues, {"op", "pageId"})
                page_id = expect_string(change.get("pageId"), f"{path}.pageId", issues, identifier=True)
                if page_id in seen_pages:
                    issue(issues, f"{path}.pageId", "A proposal may change a page only once.")
                elif page_id:
                    seen_pages.add(page_id)
            else:
                issue(
                    issues,
                    f"{path}.op",
                    "Operation must be set-navigation, upsert-page, or delete-page.",
                )
    if navigation_changes > 1:
        issue(issues, "proposal.changes", "A proposal may set navigation only once.")
    return issues


def apply_proposal(before: dict[str, Any], proposal: dict[str, Any]) -> tuple[dict[str, Any], list[dict[str, str]]]:
    issues = validate_proposal_shape(proposal)
    candidate = copy.deepcopy(before)
    changes = proposal.get("changes")
    if not isinstance(changes, list):
        return candidate, issues
    for index, change in enumerate(changes):
        if not isinstance(change, dict):
            continue
        operation = change.get("op")
        if operation == "set-navigation" and isinstance(change.get("navigation"), dict):
            candidate["navigation"] = copy.deepcopy(change["navigation"])
        elif operation == "upsert-page" and isinstance(change.get("page"), dict):
            page_id = change["page"].get("id")
            if isinstance(page_id, str):
                candidate["pages"][page_id] = copy.deepcopy(change["page"])
        elif operation == "delete-page" and isinstance(change.get("pageId"), str):
            page_id = change["pageId"]
            if page_id not in candidate["pages"]:
                issue(
                    issues,
                    f"proposal.changes[{index}].pageId",
                    "Cannot delete a page that is not in the base revision.",
                )
            else:
                del candidate["pages"][page_id]
    issues.extend(validate_snapshot(candidate))
    if canonical_json(snapshot_integrity_payload(candidate)) == canonical_json(
        snapshot_integrity_payload(before)
    ):
        issue(issues, "proposal.changes", "Proposal must change the architecture.")
    return candidate, issues


def load_proposal(harness: Path, proposal_id: str) -> tuple[dict[str, Any], dict[str, Any]]:
    directory, proposal_path, review_path = proposal_paths(harness, proposal_id)
    if not directory.is_dir():
        raise AgentFlowError("PROPOSAL_NOT_FOUND", f"Proposal does not exist: {proposal_id}")
    proposal = read_json(proposal_path)
    review = read_json(review_path)
    if not isinstance(proposal, dict) or proposal.get("id") != proposal_id:
        raise AgentFlowError("CORRUPT_PROPOSAL", f"Proposal identity mismatch: {proposal_id}")
    if not isinstance(review, dict) or review.get("proposalId") != proposal_id:
        raise AgentFlowError("CORRUPT_PROPOSAL", f"Review identity mismatch: {proposal_id}")
    if review.get("status") not in PROPOSAL_STATUSES or not isinstance(review.get("comments"), list):
        raise AgentFlowError("CORRUPT_PROPOSAL", f"Proposal review is invalid: {proposal_id}")
    current_version = review.get("currentVersion")
    if not isinstance(current_version, int) or isinstance(current_version, bool) or current_version < 0:
        raise AgentFlowError("CORRUPT_PROPOSAL", f"Proposal version is invalid: {proposal_id}")
    if review["status"] in {"under_review", "approved", "rejected"}:
        versions = require_directory(directory / "versions", "proposal versions directory")
        submitted = read_json(versions / f"v{current_version}.json")
        if (
            not isinstance(submitted, dict)
            or submitted.get("hash") != content_hash(submitted.get("proposal"))
            or submitted.get("hash") != review.get("submittedHash")
        ):
            raise AgentFlowError("CORRUPT_PROPOSAL", f"Submitted proposal is invalid: {proposal_id}")
        if review["status"] in {"under_review", "approved"} and content_hash(proposal) != review.get(
            "submittedHash"
        ):
            raise AgentFlowError(
                "UNSUBMITTED_CHANGES",
                f"Proposal content changed after submission: {proposal_id}",
            )
    return proposal, review


def proposal_candidate(harness: Path, proposal: dict[str, Any]) -> tuple[dict[str, Any], dict[str, Any]]:
    before_manifest, before = load_architecture(harness)
    base_architecture_hash = safe_hash(
        proposal.get("baseArchitectureHash"), "proposal base architecture hash"
    )
    if base_architecture_hash != before_manifest["hash"]:
        raise AgentFlowError(
            "STALE_PROPOSAL",
            "Proposal is not based on the current approved architecture.",
        )
    candidate, issues = apply_proposal(before, proposal)
    if issues:
        raise AgentFlowError(
            "VALIDATION_FAILED", "Architecture proposal is invalid.", {"issues": issues}
        )
    return before, candidate


def create_proposal(harness: Path, input_path: str) -> dict[str, Any]:
    state = load_state(harness)
    manifest, _ = load_architecture(harness)
    proposal = normalize_proposal(
        read_json(Path(input_path).expanduser().resolve()), manifest["hash"]
    )
    issues = validate_proposal_shape(proposal)
    if proposal.get("baseArchitectureHash") != manifest["hash"]:
        issue(
            issues,
            "proposal.baseArchitectureHash",
            "A proposal must use the current approved architecture.",
        )
    proposal_id = proposal.get("id")
    if issues:
        raise AgentFlowError("VALIDATION_FAILED", "Architecture proposal is invalid.", {"issues": issues})
    proposal_id = safe_id(proposal_id, "proposal id")
    directory, proposal_path, review_path = proposal_paths(harness, proposal_id)
    if directory.exists():
        raise AgentFlowError("PROPOSAL_EXISTS", f"Proposal already exists: {proposal_id}")
    _, candidate = proposal_candidate(harness, proposal)
    directory.mkdir()
    (directory / "versions").mkdir()
    atomic_json(proposal_path, proposal)
    atomic_json(
        review_path,
        {
            "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
            "proposalId": proposal_id,
            "status": "draft",
            "currentVersion": 0,
            "submittedHash": None,
            "comments": [],
            "history": [{"at": utc_now(), "action": "created"}],
        },
    )
    return {
        "proposalId": proposal_id,
        "baseArchitectureHash": proposal["baseArchitectureHash"],
        "pageCount": len(candidate["pages"]),
        "status": "draft",
    }


def revise_proposal(harness: Path, proposal_id: str, input_path: str) -> dict[str, Any]:
    _, review = load_proposal(harness, proposal_id)
    if review["status"] not in {"draft", "changes_requested"}:
        raise AgentFlowError(
            "INVALID_PROPOSAL_STATE",
            "Only draft or changes-requested proposals can be revised.",
        )
    state = load_state(harness)
    manifest, _ = load_architecture(harness)
    revised = normalize_proposal(
        read_json(Path(input_path).expanduser().resolve()), manifest["hash"]
    )
    if revised.get("id") != proposal_id:
        raise AgentFlowError("PROPOSAL_ID_MISMATCH", "Revised proposal id must not change.")
    if revised.get("baseArchitectureHash") != manifest["hash"]:
        raise AgentFlowError("STALE_PROPOSAL", "Revise against the latest approved architecture.")
    _, candidate = proposal_candidate(harness, revised)
    _, proposal_path, review_path = proposal_paths(harness, proposal_id)
    review["status"] = "draft"
    review["submittedHash"] = None
    review["history"].append({"at": utc_now(), "action": "revised"})
    atomic_json(proposal_path, revised)
    atomic_json(review_path, review)
    return {"proposalId": proposal_id, "pageCount": len(candidate["pages"]), "status": "draft"}


def open_comments(review: dict[str, Any]) -> list[dict[str, Any]]:
    return [comment for comment in review["comments"] if comment.get("status") == "open"]


def submit_proposal(harness: Path, proposal_id: str) -> dict[str, Any]:
    proposal, review = load_proposal(harness, proposal_id)
    if review["status"] not in {"draft", "changes_requested"}:
        raise AgentFlowError(
            "INVALID_PROPOSAL_STATE",
            "Only draft or changes-requested proposals can be submitted.",
        )
    if open_comments(review):
        raise AgentFlowError("OPEN_COMMENTS", "Resolve every open comment before submission.")
    state = load_state(harness)
    manifest, _ = load_architecture(harness)
    if proposal["baseArchitectureHash"] != manifest["hash"]:
        raise AgentFlowError("STALE_PROPOSAL", "Proposal is not based on the current approved architecture.")
    proposal_candidate(harness, proposal)
    version = review["currentVersion"] + 1
    submitted_hash = content_hash(proposal)
    directory, _, review_path = proposal_paths(harness, proposal_id)
    atomic_json(
        directory / "versions" / f"v{version}.json",
        {"schemaVersion": ARCHITECTURE_SCHEMA_VERSION, "hash": submitted_hash, "proposal": proposal},
    )
    review.update(
        {
            "status": "under_review",
            "currentVersion": version,
            "submittedHash": submitted_hash,
        }
    )
    review["history"].append({"at": utc_now(), "action": "submitted", "version": version})
    atomic_json(review_path, review)
    return {"proposalId": proposal_id, "version": version, "status": "under_review"}


def approve_proposal(
    harness: Path,
    proposal_id: str,
    approved_by: str,
    implemented_baseline: bool = False,
) -> dict[str, Any]:
    proposal, review = load_proposal(harness, proposal_id)
    if review["status"] != "under_review":
        raise AgentFlowError("INVALID_PROPOSAL_STATE", "Only a submitted proposal can be approved.")
    if open_comments(review):
        raise AgentFlowError("OPEN_COMMENTS", "Resolve every open comment before approval.")
    state = load_state(harness)
    before_manifest, before = load_architecture(harness)
    if state["implementedArchitectureHash"] != before_manifest["hash"]:
        raise AgentFlowError(
            "IMPLEMENTATION_PENDING",
            "Implement the currently approved architecture before approving another proposal.",
        )
    if proposal["baseArchitectureHash"] != before_manifest["hash"]:
        raise AgentFlowError("STALE_PROPOSAL", "Proposal is not based on the current approved architecture.")
    before, candidate = proposal_candidate(harness, proposal)
    approved_at = utc_now()
    approved_by = approved_by.strip()
    if not approved_by or len(approved_by) > 200:
        raise AgentFlowError("INVALID_APPROVER", "Approved by must contain 1 to 200 characters.")
    after_manifest = architecture_manifest(candidate, proposal_id, approved_at, approved_by)
    after_state = dict(state)
    after_state["updatedAt"] = approved_at
    if implemented_baseline:
        after_state["implementedArchitectureHash"] = after_manifest["hash"]
    after_review = copy.deepcopy(review)
    after_review["status"] = "approved"
    after_review["appliedArchitectureHash"] = after_manifest["hash"]
    after_review["requiredReferences"] = sorted(required_proposal_references(before, candidate))
    after_review["history"].append(
        {
            "at": approved_at,
            "action": "approved",
            "by": approved_by,
            "architectureHash": after_manifest["hash"],
            "implementedBaseline": implemented_baseline,
        }
    )
    _, _, review_path = proposal_paths(harness, proposal_id)
    apply_architecture(
        harness,
        before_manifest,
        before,
        candidate,
        proposal_id,
        approved_at,
        approved_by,
        state,
        after_state,
        review_path,
        review,
        after_review,
    )
    return {
        "proposalId": proposal_id,
        "architectureHash": after_manifest["hash"],
        "implementedArchitectureHash": after_state["implementedArchitectureHash"],
        "status": "approved",
    }


def reject_proposal(harness: Path, proposal_id: str, reason: str) -> dict[str, Any]:
    _, review = load_proposal(harness, proposal_id)
    if review["status"] != "under_review":
        raise AgentFlowError("INVALID_PROPOSAL_STATE", "Only a submitted proposal can be rejected.")
    review["status"] = "rejected"
    review["history"].append({"at": utc_now(), "action": "rejected", "reason": reason.strip()})
    _, _, review_path = proposal_paths(harness, proposal_id)
    atomic_json(review_path, review)
    return {"proposalId": proposal_id, "status": "rejected"}


def list_proposals(harness: Path) -> list[dict[str, Any]]:
    proposals = require_directory(harness / "proposals", "proposals directory")
    values: list[dict[str, Any]] = []
    for path in sorted(proposals.iterdir()):
        if path.name.startswith("."):
            continue
        if path.is_symlink() or not path.is_dir():
            raise AgentFlowError("CORRUPT_PROPOSAL", f"Invalid proposal path: {path}")
        proposal, review = load_proposal(harness, safe_id(path.name, "proposal id"))
        values.append(
            {
                "id": proposal["id"],
                "title": proposal["title"],
                "summary": proposal["summary"],
                "baseArchitectureHash": proposal["baseArchitectureHash"],
                "status": review["status"],
                "version": review["currentVersion"],
                "openComments": len(open_comments(review)),
                "createdAt": proposal["createdAt"],
                "appliedArchitectureHash": review.get("appliedArchitectureHash"),
            }
        )
    values.sort(key=lambda item: (item["createdAt"], item["id"]), reverse=True)
    return values


def flatten_navigation(navigation: dict[str, Any]) -> dict[str, dict[str, Any]]:
    flattened: dict[str, dict[str, Any]] = {}

    def visit(items: list[Any], parent: str | None) -> None:
        for order, item in enumerate(items):
            if not isinstance(item, dict) or not isinstance(item.get("id"), str):
                continue
            value = {key: copy.deepcopy(raw) for key, raw in item.items() if key != "children"}
            value["parentId"] = parent
            value["order"] = order
            flattened[item["id"]] = value
            if item.get("type") == "folder" and isinstance(item.get("children"), list):
                visit(item["children"], item["id"])

    visit(navigation.get("items", []), None)
    return flattened


def page_element_records(page: dict[str, Any]) -> dict[str, Any]:
    records: dict[str, Any] = {
        "page": {
            key: copy.deepcopy(page.get(key))
            for key in ("type", "title", "summary", "details")
            if key in page
        }
    }
    page_type = page.get("type")
    if page_type == "table":
        for column in page.get("columns", []):
            records[f"column:{column['id']}"] = copy.deepcopy(column)
        for row in page.get("rows", []):
            records[f"row:{row['id']}"] = {
                "id": row["id"],
                "details": copy.deepcopy(row.get("details", [])),
            }
            for column_id, value in row.get("cells", {}).items():
                records[f"cell:{row['id']}.{column_id}"] = copy.deepcopy(value)
    elif page_type == "flow":
        for kind, values in (
            ("node", page.get("nodes", [])),
            ("edge", page.get("edges", [])),
            ("group", page.get("groups", [])),
        ):
            for value in values:
                records[f"{kind}:{value['id']}"] = copy.deepcopy(value)
    elif page_type == "document":
        for section in page.get("sections", []):
            records[f"section:{section['id']}"] = {
                "id": section["id"],
                "title": section["title"],
            }
            for block in section.get("blocks", []):
                records[f"block:{block['id']}"] = copy.deepcopy(block)
    return records


def change_status(before: Any, after: Any, missing: object) -> str:
    if before is missing:
        return "added"
    if after is missing:
        return "removed"
    return "unchanged" if canonical_json(before) == canonical_json(after) else "modified"


def architecture_diff(before: dict[str, Any], after: dict[str, Any]) -> dict[str, Any]:
    missing = object()
    before_navigation = flatten_navigation(before["navigation"])
    after_navigation = flatten_navigation(after["navigation"])
    navigation_changes = {
        item_id: change_status(
            before_navigation.get(item_id, missing), after_navigation.get(item_id, missing), missing
        )
        for item_id in sorted(before_navigation.keys() | after_navigation.keys())
    }
    pages: dict[str, Any] = {}
    for page_id in sorted(before["pages"].keys() | after["pages"].keys()):
        old_page = before["pages"].get(page_id, missing)
        new_page = after["pages"].get(page_id, missing)
        status = change_status(old_page, new_page, missing)
        old_records = {} if old_page is missing else page_element_records(old_page)
        new_records = {} if new_page is missing else page_element_records(new_page)
        element_changes = {
            key: change_status(old_records.get(key, missing), new_records.get(key, missing), missing)
            for key in sorted(old_records.keys() | new_records.keys())
        }
        pages[page_id] = {"status": status, "elements": element_changes}
    return {"navigation": navigation_changes, "pages": pages}


def architecture_references(snapshot: dict[str, Any]) -> set[str]:
    references = {f"navigation:{item_id}" for item_id in flatten_navigation(snapshot["navigation"])}
    for page_id, page in snapshot["pages"].items():
        for element in page_element_records(page):
            suffix = "" if element == "page" else f"#{element}"
            references.add(f"page:{page_id}{suffix}")
    return references


def required_proposal_references(before: dict[str, Any], after: dict[str, Any]) -> set[str]:
    diff = architecture_diff(before, after)
    references = {
        f"navigation:{item_id}"
        for item_id, status in diff["navigation"].items()
        if status != "unchanged"
    }
    for page_id, page_change in diff["pages"].items():
        for element, status in page_change["elements"].items():
            if status == "unchanged":
                continue
            suffix = "" if element == "page" else f"#{element}"
            references.add(f"page:{page_id}{suffix}")
    return references


def target_exists(snapshot: dict[str, Any], target: dict[str, Any]) -> bool:
    kind = target.get("kind")
    if kind == "navigation":
        return target.get("navigationId") in flatten_navigation(snapshot["navigation"])
    if kind != "page":
        return False
    page_id = target.get("pageId")
    page = snapshot["pages"].get(page_id)
    if page is None:
        return False
    element_type = target.get("elementType")
    element_id = target.get("elementId")
    if element_type is None and element_id is None:
        return True
    if element_type not in COMMENT_ELEMENT_TYPES - {"page", "navigation"} or not isinstance(
        element_id, str
    ):
        return False
    return f"{element_type}:{element_id}" in page_element_records(page)


def validate_comment_target(
    target: Any,
    before: dict[str, Any],
    proposed: dict[str, Any],
    path: str = "target",
) -> dict[str, Any]:
    issues: list[dict[str, str]] = []
    value = expect_object(target, path, issues)
    if value is None:
        raise AgentFlowError("INVALID_COMMENT_TARGET", "Comment target is invalid.", {"issues": issues})
    if value.get("kind") == "navigation":
        validate_exact_keys(value, path, issues, {"kind", "navigationId"})
        expect_string(value.get("navigationId"), f"{path}.navigationId", issues, identifier=True)
    elif value.get("kind") == "page":
        validate_exact_keys(value, path, issues, {"kind", "pageId"}, {"elementType", "elementId"})
        expect_string(value.get("pageId"), f"{path}.pageId", issues, identifier=True)
        element_type = value.get("elementType")
        element_id = value.get("elementId")
        if (element_type is None) != (element_id is None):
            issue(issues, path, "elementType and elementId must be provided together.")
        if element_type is not None and element_type not in COMMENT_ELEMENT_TYPES - {"page", "navigation"}:
            issue(issues, f"{path}.elementType", "Unknown comment element type.")
        if element_id is not None:
            expect_string(element_id, f"{path}.elementId", issues, identifier=True)
    else:
        issue(issues, f"{path}.kind", "Comment target kind must be page or navigation.")
    if not issues and not (target_exists(before, value) or target_exists(proposed, value)):
        issue(issues, path, "Comment target does not exist in the baseline or proposal.")
    if issues:
        raise AgentFlowError("INVALID_COMMENT_TARGET", "Comment target is invalid.", {"issues": issues})
    return copy.deepcopy(value)


def comment_inbox_path(harness: Path, proposal_id: str | None = None) -> Path | None:
    root = harness / "comment-inbox"
    if root.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", "Comment inbox must not be a symbolic link.")
    if not root.exists():
        return None
    require_directory(root, "comment inbox")
    if proposal_id is None:
        return root
    directory = root / safe_id(proposal_id, "proposal id")
    if directory.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", f"Comment inbox is unsafe: {proposal_id}")
    if not directory.exists():
        return None
    return require_directory(directory, "proposal comment inbox")


def require_open_comment(review: dict[str, Any], comment_id: str) -> dict[str, Any]:
    safe_id(comment_id, "comment id")
    for comment in review["comments"]:
        if comment.get("id") == comment_id:
            if comment.get("status") != "open":
                raise AgentFlowError("COMMENT_NOT_OPEN", "Only open comments can be changed.")
            return comment
    raise AgentFlowError("COMMENT_NOT_FOUND", f"Comment does not exist: {comment_id}")


def add_comment(
    harness: Path, proposal_id: str, target: dict[str, Any], message: str
) -> dict[str, Any]:
    proposal, review = load_proposal(harness, proposal_id)
    before, proposed = proposal_candidate(harness, proposal)
    normalized_target = validate_comment_target(target, before, proposed)
    message = message.strip()
    if review["status"] not in {"under_review", "changes_requested"}:
        raise AgentFlowError("INVALID_PROPOSAL_STATE", "Comments require a submitted proposal.")
    if not message or len(message) > 4000:
        raise AgentFlowError("INVALID_COMMENT", "Comment must contain 1 to 4000 characters.")
    created_at = utc_now()
    comment = {
        "id": f"comment-{uuid.uuid4().hex[:16]}",
        "status": "open",
        "target": normalized_target,
        "message": message,
        "createdAt": created_at,
        "proposalVersion": review["currentVersion"],
        "history": [{"at": created_at, "action": "added"}],
    }
    review["comments"].append(comment)
    review["status"] = "changes_requested"
    review["history"].append({"at": created_at, "action": "comment-added", "commentId": comment["id"]})
    _, _, review_path = proposal_paths(harness, proposal_id)
    atomic_json(review_path, review)
    return comment


def edit_comment(harness: Path, proposal_id: str, comment_id: str, message: str) -> dict[str, Any]:
    _, review = load_proposal(harness, proposal_id)
    comment = require_open_comment(review, comment_id)
    message = message.strip()
    if not message or len(message) > 4000:
        raise AgentFlowError("INVALID_COMMENT", "Comment must contain 1 to 4000 characters.")
    edited_at = utc_now()
    comment["message"] = message
    comment["editedAt"] = edited_at
    comment["history"].append({"at": edited_at, "action": "edited"})
    review["status"] = "changes_requested"
    review["history"].append({"at": edited_at, "action": "comment-edited", "commentId": comment_id})
    _, _, review_path = proposal_paths(harness, proposal_id)
    atomic_json(review_path, review)
    return comment


def delete_comment(harness: Path, proposal_id: str, comment_id: str) -> dict[str, Any]:
    proposal, review = load_proposal(harness, proposal_id)
    comment = require_open_comment(review, comment_id)
    deleted_at = utc_now()
    review["comments"] = [item for item in review["comments"] if item.get("id") != comment_id]
    review.setdefault("commentHistory", []).append(
        {**comment, "deletedAt": deleted_at, "history": comment["history"] + [{"at": deleted_at, "action": "deleted"}]}
    )
    if not open_comments(review) and review.get("submittedHash") == content_hash(proposal):
        review["status"] = "under_review"
    review["history"].append({"at": deleted_at, "action": "comment-deleted", "commentId": comment_id})
    _, _, review_path = proposal_paths(harness, proposal_id)
    atomic_json(review_path, review)
    return {"id": comment_id, "deleted": True}


def resolve_comment(
    harness: Path, proposal_id: str, comment_id: str, resolution: str
) -> dict[str, Any]:
    _, review = load_proposal(harness, proposal_id)
    comment = require_open_comment(review, comment_id)
    resolution = resolution.strip()
    if not resolution or len(resolution) > 4000:
        raise AgentFlowError("INVALID_RESOLUTION", "Resolution must contain 1 to 4000 characters.")
    resolved_at = utc_now()
    comment.update({"status": "resolved", "resolution": resolution, "resolvedAt": resolved_at})
    comment["history"].append({"at": resolved_at, "action": "resolved", "resolution": resolution})
    review["history"].append({"at": resolved_at, "action": "comment-resolved", "commentId": comment_id})
    _, _, review_path = proposal_paths(harness, proposal_id)
    atomic_json(review_path, review)
    return comment


def normalize_inbox_request(
    path: Path,
    raw: Any,
    proposal_id: str,
    review: dict[str, Any],
    before: dict[str, Any],
    proposed: dict[str, Any],
) -> dict[str, Any]:
    if not isinstance(raw, dict):
        raise AgentFlowError("INVALID_COMMENT_REQUEST", f"Comment request must be an object: {path}")
    if raw.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION:
        raise AgentFlowError("INVALID_COMMENT_REQUEST", f"Unsupported comment request version: {path}")
    request_id = safe_id(raw.get("id"), "comment request id")
    if path.stem != request_id or raw.get("proposalId") != proposal_id:
        raise AgentFlowError("INVALID_COMMENT_REQUEST", f"Comment request identity mismatch: {path}")
    proposal_version = raw.get("proposalVersion")
    if (
        not isinstance(proposal_version, int)
        or isinstance(proposal_version, bool)
        or proposal_version != review["currentVersion"]
    ):
        raise AgentFlowError(
            "STALE_COMMENT_REQUEST",
            f"Comment request targets an older proposal version: {path}",
            {"currentVersion": review["currentVersion"], "commentVersion": proposal_version},
        )
    requested_at = raw.get("requestedAt")
    if not isinstance(requested_at, str) or not requested_at:
        requested_at = datetime.fromtimestamp(path.stat().st_mtime, timezone.utc).isoformat(
            timespec="seconds"
        ).replace("+00:00", "Z")
    operation = raw.get("operation")
    normalized: dict[str, Any] = {
        "id": request_id,
        "operation": operation,
        "proposalId": proposal_id,
        "proposalVersion": proposal_version,
        "requestedAt": requested_at,
    }
    if operation == "add":
        normalized["commentId"] = safe_id(raw.get("commentId"), "comment id")
        normalized["target"] = validate_comment_target(raw.get("target"), before, proposed)
        message = raw.get("message")
        if not isinstance(message, str) or not message.strip() or len(message.strip()) > 4000:
            raise AgentFlowError("INVALID_COMMENT_REQUEST", f"Invalid comment message: {path}")
        normalized["message"] = message.strip()
    elif operation == "edit":
        normalized["commentId"] = safe_id(raw.get("commentId"), "comment id")
        message = raw.get("message")
        if not isinstance(message, str) or not message.strip() or len(message.strip()) > 4000:
            raise AgentFlowError("INVALID_COMMENT_REQUEST", f"Invalid comment message: {path}")
        normalized["message"] = message.strip()
    elif operation == "delete":
        normalized["commentId"] = safe_id(raw.get("commentId"), "comment id")
    else:
        raise AgentFlowError("INVALID_COMMENT_REQUEST", f"Unknown comment operation: {path}")
    return normalized


def import_comment_inbox(harness: Path, proposal_id: str) -> list[dict[str, Any]]:
    directory = comment_inbox_path(harness, proposal_id)
    if directory is None:
        return []
    paths = sorted(directory.glob("*.json"))
    if len(paths) > MAX_COMMENT_INBOX_FILES:
        raise AgentFlowError("COMMENT_INBOX_FULL", f"Comment inbox is too large: {proposal_id}")
    if not paths:
        return []
    proposal, review = load_proposal(harness, proposal_id)
    before, proposed = proposal_candidate(harness, proposal)
    requests = [
        (path, normalize_inbox_request(path, read_json(path), proposal_id, review, before, proposed))
        for path in paths
    ]
    requests.sort(key=lambda item: (item[1]["requestedAt"], item[1]["id"]))
    imported: list[dict[str, Any]] = []
    for path, request in requests:
        operation = request["operation"]
        comment_id = request["commentId"]
        if operation == "add":
            if any(comment.get("id") == comment_id for comment in review["comments"]):
                raise AgentFlowError("COMMENT_EXISTS", f"Comment already exists: {comment_id}")
            comment = {
                "id": comment_id,
                "status": "open",
                "target": request["target"],
                "message": request["message"],
                "createdAt": request["requestedAt"],
                "proposalVersion": request["proposalVersion"],
                "history": [{"at": request["requestedAt"], "action": "added"}],
            }
            review["comments"].append(comment)
            review["status"] = "changes_requested"
        elif operation == "edit":
            comment = require_open_comment(review, comment_id)
            comment["message"] = request["message"]
            comment["editedAt"] = request["requestedAt"]
            comment["history"].append({"at": request["requestedAt"], "action": "edited"})
            review["status"] = "changes_requested"
        else:
            comment = require_open_comment(review, comment_id)
            review["comments"] = [
                item for item in review["comments"] if item.get("id") != comment_id
            ]
            review.setdefault("commentHistory", []).append(
                {
                    **comment,
                    "deletedAt": request["requestedAt"],
                    "history": comment["history"]
                    + [{"at": request["requestedAt"], "action": "deleted"}],
                }
            )
            if not open_comments(review) and review.get("submittedHash") == content_hash(proposal):
                review["status"] = "under_review"
        review["history"].append(
            {
                "at": request["requestedAt"],
                "action": f"comment-{operation}",
                "commentId": comment_id,
                "requestId": request["id"],
            }
        )
        imported.append(request)
        path.unlink()
    _, _, review_path = proposal_paths(harness, proposal_id)
    atomic_json(review_path, review)
    return imported


def import_all_comment_inboxes(harness: Path) -> list[dict[str, Any]]:
    root = comment_inbox_path(harness)
    if root is None:
        return []
    imported: list[dict[str, Any]] = []
    for path in sorted(root.iterdir()):
        if path.name.startswith("."):
            continue
        if path.is_symlink() or not path.is_dir():
            raise AgentFlowError("UNSAFE_PATH", f"Invalid comment inbox path: {path}")
        imported.extend(import_comment_inbox(harness, safe_id(path.name, "proposal id")))
    return imported


def list_comments(harness: Path, proposal_id: str | None, status: str | None) -> list[dict[str, Any]]:
    import_all_comment_inboxes(harness)
    proposal_ids = [proposal_id] if proposal_id else [proposal["id"] for proposal in list_proposals(harness)]
    values: list[dict[str, Any]] = []
    for identifier in proposal_ids:
        proposal, review = load_proposal(harness, identifier)
        for comment in review["comments"]:
            if status is not None and comment.get("status") != status:
                continue
            values.append(
                {
                    **copy.deepcopy(comment),
                    "proposalId": identifier,
                    "proposalTitle": proposal["title"],
                    "proposalStatus": review["status"],
                }
            )
    values.sort(key=lambda item: (item.get("createdAt", ""), item.get("id", "")), reverse=True)
    return values


def project_relative_path(project: Path, value: Any, label: str) -> tuple[str, Path]:
    if not isinstance(value, str) or not value.strip():
        raise AgentFlowError("INVALID_PATH", f"{label} must be a non-empty relative path.")
    relative = Path(value)
    if relative.is_absolute() or ".." in relative.parts:
        raise AgentFlowError("INVALID_PATH", f"{label} must stay inside the project.")
    resolved = (project / relative).resolve()
    try:
        resolved.relative_to(project.resolve())
    except ValueError as error:
        raise AgentFlowError("INVALID_PATH", f"{label} escapes the project.") from error
    return relative.as_posix(), resolved


def approved_proposal_references(
    harness: Path, proposal_id: str, architecture_hash: str
) -> set[str]:
    _, review = load_proposal(harness, proposal_id)
    if review.get("status") != "approved":
        raise AgentFlowError("PLAN_PROPOSAL_UNAPPROVED", "Plan proposal is not approved.")
    if review.get("appliedArchitectureHash") != architecture_hash:
        raise AgentFlowError(
            "PLAN_ARCHITECTURE_MISMATCH",
            "Plan proposal does not own the current architecture.",
        )
    references = review.get("requiredReferences")
    if not isinstance(references, list) or not references:
        raise AgentFlowError(
            "PLAN_REFERENCES_MISSING",
            "Approved proposal is missing its architecture reference record.",
        )
    if any(not isinstance(reference, str) or REFERENCE_PATTERN.fullmatch(reference) is None for reference in references):
        raise AgentFlowError(
            "PLAN_REFERENCES_INVALID",
            "Approved proposal has an invalid architecture reference record.",
        )
    return set(references)


def validate_plan_manifest(
    project: Path, harness: Path, raw: Any
) -> tuple[dict[str, Any], list[dict[str, str]], dict[str, Any]]:
    issues: list[dict[str, str]] = []
    value = expect_object(raw, "plan", issues)
    if value is None:
        return {}, issues, {}
    validate_exact_keys(
        value,
        "plan",
        issues,
        {"schemaVersion", "id", "title", "architectureHash", "proposalId", "document", "tasks"},
    )
    if value.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION:
        issue(issues, "plan.schemaVersion", f"Must equal {ARCHITECTURE_SCHEMA_VERSION}.")
    plan_id = expect_string(value.get("id"), "plan.id", issues, identifier=True)
    expect_string(value.get("title"), "plan.title", issues, maximum=200)
    architecture_hash = ""
    try:
        architecture_hash = safe_hash(value.get("architectureHash"), "plan.architectureHash")
    except AgentFlowError as error:
        issue(issues, "plan.architectureHash", error.message)
    proposal_id = expect_string(value.get("proposalId"), "plan.proposalId", issues, identifier=True)
    document = expect_string(value.get("document"), "plan.document", issues, maximum=300)
    required: set[str] = set()
    if architecture_hash:
        manifest, _ = load_architecture(harness)
        if architecture_hash != manifest["hash"]:
            issue(issues, "plan.architectureHash", "Plan must target the current approved architecture.")
        try:
            if proposal_id:
                required = approved_proposal_references(harness, proposal_id, architecture_hash)
        except AgentFlowError as error:
            issue(issues, "plan.proposalId", error.message)
    if document:
        try:
            normalized_document, document_path = project_relative_path(project, document, "Plan document")
            if not document_path.is_file() or document_path.is_symlink():
                issue(issues, "plan.document", "Plan document does not exist or is unsafe.")
            if not normalized_document.startswith(".agentflow/plans/"):
                issue(issues, "plan.document", "Plan document must be stored under .agentflow/plans/.")
        except AgentFlowError as error:
            issue(issues, "plan.document", error.message)
    tasks = expect_list(value.get("tasks"), "plan.tasks", issues)
    task_ids: set[str] = set()
    covered: set[str] = set()
    reference_owners: dict[str, str] = {}
    if tasks is not None:
        if not tasks:
            issue(issues, "plan.tasks", "A plan needs at least one task.")
        for index, raw_task in enumerate(tasks):
            path = f"plan.tasks[{index}]"
            task = expect_object(raw_task, path, issues)
            if task is None:
                continue
            validate_exact_keys(task, path, issues, {"id", "title", "sourceRefs"}, {"description"})
            task_id = expect_string(task.get("id"), f"{path}.id", issues, identifier=True)
            expect_string(task.get("title"), f"{path}.title", issues, maximum=200)
            expect_string(
                task.get("description"), f"{path}.description", issues, required=False, maximum=2000
            )
            source_refs = expect_list(task.get("sourceRefs"), f"{path}.sourceRefs", issues)
            if source_refs is not None:
                if not source_refs:
                    issue(issues, f"{path}.sourceRefs", "Every plan task needs an architecture source.")
                for ref_index, reference in enumerate(source_refs):
                    if not isinstance(reference, str) or REFERENCE_PATTERN.fullmatch(reference) is None:
                        issue(
                            issues,
                            f"{path}.sourceRefs[{ref_index}]",
                            "Invalid architecture reference.",
                        )
                    elif reference not in required:
                        issue(
                            issues,
                            f"{path}.sourceRefs[{ref_index}]",
                            "Reference is not part of the approved architecture change.",
                        )
                    else:
                        owner = reference_owners.get(reference)
                        if owner is not None:
                            issue(
                                issues,
                                f"{path}.sourceRefs[{ref_index}]",
                                f"Reference is already assigned to plan task {owner}.",
                            )
                        elif task_id:
                            reference_owners[reference] = task_id
                        covered.add(reference)
            if task_id in task_ids:
                issue(issues, f"{path}.id", "Plan task ids must be unique.")
            elif task_id:
                task_ids.add(task_id)
    missing = sorted(required - covered)
    if missing:
        issue(issues, "plan.tasks", f"Plan is missing {len(missing)} architecture references.")
    coverage = {
        "required": sorted(required),
        "covered": sorted(covered),
        "missing": missing,
        "taskCount": len(task_ids),
        "planId": plan_id,
    }
    return copy.deepcopy(value), issues, coverage


def register_plan(project: Path, harness: Path, input_path: str) -> dict[str, Any]:
    raw = read_json(Path(input_path).expanduser().resolve())
    manifest, issues, coverage = validate_plan_manifest(project, harness, raw)
    if issues:
        raise AgentFlowError("PLAN_VALIDATION_FAILED", "AgentFlow plan is invalid.", {"issues": issues})
    plan_id = safe_id(manifest["id"], "plan id")
    destination = harness / "plans" / plan_id / "manifest.json"
    if destination.exists() or destination.is_symlink():
        raise AgentFlowError("PLAN_EXISTS", f"Plan already exists: {plan_id}")
    manifest["registeredAt"] = utc_now()
    atomic_json(destination, manifest)
    return {**coverage, "architectureHash": manifest["architectureHash"], "registered": True}


def load_plan(harness: Path, plan_id: str) -> dict[str, Any]:
    plan_id = safe_id(plan_id, "plan id")
    path = harness / "plans" / plan_id / "manifest.json"
    value = read_json(path)
    if not isinstance(value, dict) or value.get("id") != plan_id:
        raise AgentFlowError("CORRUPT_PLAN", f"Plan manifest is invalid: {plan_id}")
    return value


def list_plans(harness: Path) -> list[dict[str, Any]]:
    directory = require_directory(harness / "plans", "plans directory")
    values: list[dict[str, Any]] = []
    for path in sorted(directory.iterdir()):
        if path.name.startswith(".") or not path.is_dir():
            continue
        plan = load_plan(harness, path.name)
        evidence_path = harness / "implementation" / path.name / "evidence.json"
        values.append(
            {
                "id": plan["id"],
                "title": plan["title"],
                "architectureHash": plan["architectureHash"],
                "proposalId": plan["proposalId"],
                "taskCount": len(plan["tasks"]),
                "registeredAt": plan.get("registeredAt"),
                "implemented": evidence_path.is_file() and not evidence_path.is_symlink(),
            }
        )
    return values


def validate_implementation_evidence(
    project: Path, harness: Path, raw: Any
) -> tuple[dict[str, Any], list[dict[str, str]], dict[str, Any]]:
    issues: list[dict[str, str]] = []
    value = expect_object(raw, "implementation", issues)
    if value is None:
        return {}, issues, {}
    validate_exact_keys(
        value,
        "implementation",
        issues,
        {"schemaVersion", "planId", "architectureHash", "items", "changes"},
    )
    if value.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION:
        issue(issues, "implementation.schemaVersion", f"Must equal {ARCHITECTURE_SCHEMA_VERSION}.")
    plan_id = expect_string(value.get("planId"), "implementation.planId", issues, identifier=True)
    architecture_hash = ""
    try:
        architecture_hash = safe_hash(value.get("architectureHash"), "implementation.architectureHash")
    except AgentFlowError as error:
        issue(issues, "implementation.architectureHash", error.message)
    plan: dict[str, Any] = {}
    if plan_id:
        try:
            plan = load_plan(harness, plan_id)
        except AgentFlowError as error:
            issue(issues, "implementation.planId", error.message)
    if plan and architecture_hash != plan.get("architectureHash"):
        issue(issues, "implementation.architectureHash", "Evidence must target the plan architecture.")
    task_ids = {task["id"] for task in plan.get("tasks", [])}
    covered_items: set[str] = set()
    blocked: set[str] = set()
    items = expect_list(value.get("items"), "implementation.items", issues)
    if items is not None:
        for index, raw_item in enumerate(items):
            path = f"implementation.items[{index}]"
            item = expect_object(raw_item, path, issues)
            if item is None:
                continue
            validate_exact_keys(item, path, issues, {"taskId", "status", "summary"})
            task_id = expect_string(item.get("taskId"), f"{path}.taskId", issues, identifier=True)
            if task_id not in task_ids:
                issue(issues, f"{path}.taskId", "Evidence references an unknown plan task.")
            if task_id in covered_items:
                issue(issues, f"{path}.taskId", "Each plan task must appear exactly once.")
            elif task_id:
                covered_items.add(task_id)
            if item.get("status") not in {"implemented", "blocked"}:
                issue(issues, f"{path}.status", "Status must be implemented or blocked.")
            elif item.get("status") == "blocked" and task_id:
                blocked.add(task_id)
            expect_string(item.get("summary"), f"{path}.summary", issues, maximum=2000)
    changed_tasks: set[str] = set()
    changes = expect_list(value.get("changes"), "implementation.changes", issues)
    if changes is not None:
        for index, raw_change in enumerate(changes):
            path = f"implementation.changes[{index}]"
            change = expect_object(raw_change, path, issues)
            if change is None:
                continue
            validate_exact_keys(change, path, issues, {"path", "kind", "taskIds"}, {"symbol", "evidence"})
            change_path = expect_string(change.get("path"), f"{path}.path", issues, maximum=500)
            if change_path:
                try:
                    project_relative_path(project, change_path, "Implementation path")
                except AgentFlowError as error:
                    issue(issues, f"{path}.path", error.message)
            if change.get("kind") not in EVIDENCE_KINDS:
                issue(issues, f"{path}.kind", "Unknown implementation evidence kind.")
            expect_string(change.get("symbol"), f"{path}.symbol", issues, required=False, maximum=300)
            expect_string(change.get("evidence"), f"{path}.evidence", issues, required=False, maximum=2000)
            source_tasks = expect_list(change.get("taskIds"), f"{path}.taskIds", issues)
            if source_tasks is not None:
                if not source_tasks:
                    issue(issues, f"{path}.taskIds", "Every changed artifact needs a plan task.")
                for task_index, task_id in enumerate(source_tasks):
                    if task_id not in task_ids:
                        issue(
                            issues,
                            f"{path}.taskIds[{task_index}]",
                            "Changed artifact references an unknown task.",
                        )
                    elif isinstance(task_id, str):
                        changed_tasks.add(task_id)
    missing_items = sorted(task_ids - covered_items)
    missing_evidence = sorted((task_ids - blocked) - changed_tasks)
    if missing_items:
        issue(issues, "implementation.items", f"Evidence is missing {len(missing_items)} plan tasks.")
    if missing_evidence:
        issue(
            issues,
            "implementation.changes",
            f"Implemented tasks without file or runtime evidence: {len(missing_evidence)}.",
        )
    audit = {
        "planId": plan_id,
        "architectureHash": architecture_hash,
        "taskCount": len(task_ids),
        "coveredTasks": sorted(covered_items),
        "blockedTasks": sorted(blocked),
        "missingTasks": missing_items,
        "missingEvidence": missing_evidence,
        "complete": not issues and not blocked,
    }
    return copy.deepcopy(value), issues, audit


def audit_implementation(project: Path, harness: Path, input_path: str, complete: bool) -> dict[str, Any]:
    evidence, issues, audit = validate_implementation_evidence(
        project, harness, read_json(Path(input_path).expanduser().resolve())
    )
    if issues:
        raise AgentFlowError(
            "IMPLEMENTATION_AUDIT_FAILED",
            "Implementation does not match the AgentFlow plan.",
            {"issues": issues, "audit": audit},
        )
    if complete and audit["blockedTasks"]:
        raise AgentFlowError(
            "IMPLEMENTATION_BLOCKED",
            "Blocked plan tasks prevent implementation completion.",
            {"audit": audit},
        )
    if not complete:
        return audit
    state = load_state(harness)
    manifest, _ = load_architecture(harness)
    architecture_hash = evidence["architectureHash"]
    if architecture_hash != manifest["hash"]:
        raise AgentFlowError(
            "STALE_IMPLEMENTATION",
            "Implementation must complete the latest approved architecture.",
        )
    evidence["completedAt"] = utc_now()
    destination = harness / "implementation" / evidence["planId"] / "evidence.json"
    if destination.exists() or destination.is_symlink():
        raise AgentFlowError("IMPLEMENTATION_EXISTS", "Implementation evidence is already complete.")
    atomic_json(destination, evidence)
    state.update(
        {
            "implementedArchitectureHash": architecture_hash,
            "updatedAt": evidence["completedAt"],
        }
    )
    atomic_json(harness / "state.json", state)
    return {**audit, "complete": True, "implementedArchitectureHash": architecture_hash}


def load_legacy_revision(harness: Path, revision_id: str) -> dict[str, list[dict[str, Any]]]:
    directory = legacy_revision_directory(harness, revision_id)
    if not directory.is_dir():
        raise AgentFlowError("REVISION_NOT_FOUND", f"Legacy revision does not exist: {revision_id}")
    values: dict[str, list[dict[str, Any]]] = {}
    for kind in ("features", "models", "flows", "documents"):
        kind_directory = directory / kind
        values[kind] = []
        if not kind_directory.exists():
            continue
        require_directory(kind_directory, f"legacy {kind} directory")
        for path in sorted(kind_directory.glob("*.json")):
            value = read_json(path)
            if isinstance(value, dict):
                values[kind].append(value)
    return values


def installed_harness_profile(harness: Path) -> str:
    metadata_path = harness / "harness.json"
    if not metadata_path.exists():
        return DEFAULT_HARNESS_PROFILE
    metadata = read_json(metadata_path)
    if not isinstance(metadata, dict):
        raise AgentFlowError("CORRUPT_HARNESS", "AgentFlow harness metadata must be an object.")
    profile = metadata.get("harnessProfile", DEFAULT_HARNESS_PROFILE)
    if not isinstance(profile, str) or profile not in SUPPORTED_MIGRATION_PROFILES:
        raise AgentFlowError(
            "MIGRATION_PROFILE_UNSUPPORTED",
            "The installed AgentFlow profile does not support v3 migration.",
            {"profile": profile, "supportedProfiles": sorted(SUPPORTED_MIGRATION_PROFILES)},
        )
    return profile


def migration_ownership_error(artifact_id: str, reason: str) -> None:
    raise AgentFlowError(
        "MIGRATION_OWNERSHIP_UNRESOLVED",
        f"Cannot determine Django ownership for legacy artifact {artifact_id}: {reason}",
        {
            "artifactId": artifact_id,
            "resolution": "Add explicit app ownership in the v3 architecture, then rerun migration.",
        },
    )


def concise_legacy_label(value: Any, maximum: int, label: str) -> tuple[str, str | None]:
    text = str(value)
    if len(text) <= maximum:
        return text, None
    if len(text) > LEGACY_DESCRIPTION_MAXIMUM:
        raise AgentFlowError(
            "MIGRATION_LABEL_TOO_LONG",
            f"Legacy {label} exceeds the {LEGACY_DESCRIPTION_MAXIMUM}-character lossless migration limit.",
            {"label": label, "length": len(text)},
        )
    return text[: maximum - 3].rstrip() + "...", text


def preserve_legacy_label(
    details: list[dict[str, str]], label: str, full_value: str | None
) -> None:
    if full_value is None:
        return
    detail_id = re.sub(r"[^a-z0-9]+", "-", f"legacy-{label}".lower()).strip("-")
    details.append({"id": detail_id, "label": f"Legacy {label}", "value": full_value})


def append_legacy_description(existing: Any, label: str, full_value: str | None) -> str | None:
    text = str(existing).strip() if existing is not None else ""
    if full_value is None:
        return text or None
    retained = f"Legacy {label}: {full_value}"
    combined = f"{text}\n\n{retained}" if text else retained
    if len(combined) > LEGACY_DESCRIPTION_MAXIMUM:
        raise AgentFlowError(
            "MIGRATION_LABEL_TOO_LONG",
            f"Legacy {label} cannot fit in the v4 lossless description field.",
            {"label": label, "length": len(combined)},
        )
    return combined


def title_case_identifier(value: str) -> str:
    return value.replace("_", " ").replace("-", " ").title()


def django_app_id(value: Any, artifact_id: str) -> str:
    if not isinstance(value, str) or not value or ID_PATTERN.fullmatch(value) is None:
        migration_ownership_error(artifact_id, "the app identifier is missing or invalid")
    if value in {"data", "db", "models", "features", "feature", "documentation", "documents"}:
        migration_ownership_error(artifact_id, f"{value!r} is a generic grouping, not a Django app")
    return value


def django_model_location(model: dict[str, Any]) -> tuple[str, str]:
    artifact_id = str(model.get("id", "model"))
    parts = artifact_id.split(".")
    if len(parts) < 2 or parts[0] not in {"data", "db", "models"}:
        migration_ownership_error(
            artifact_id,
            "model ids must use data.<app>, db.<app>, or models.<app> ownership",
        )
    app = django_app_id(parts[1], artifact_id)
    category = "mixins" if any(part in {"mixin", "mixins"} for part in parts[2:]) else "models"
    return app, category


def legacy_model_page_app(model: dict[str, Any]) -> str:
    """Keep the historical v4 page-id derivation stable during navigation correction."""
    model_id = str(model.get("id", "model"))
    parts = model_id.split(".")
    return parts[1] if len(parts) > 2 and parts[0] in {"db", "data", "models"} else parts[0]


DJANGO_DOCUMENT_CATEGORIES = {
    "admin": "admin",
    "api": "apis",
    "apis": "apis",
    "beat": "tasks",
    "beats": "tasks",
    "contracts": "contracts",
    "deployment": "deployment",
    "domain": "domain",
    "enums": "enums",
    "environment": "environment",
    "frontend": "frontend",
    "integration": "integrations",
    "integrations": "integrations",
    "lifecycle": "domain",
    "mixin": "mixins",
    "mixins": "mixins",
    "model": "models",
    "models": "models",
    "observability": "observability",
    "performance": "performance",
    "repository": "repository",
    "routes": "apis",
    "routing": "apis",
    "schedule": "tasks",
    "schedules": "tasks",
    "security": "security",
    "settings": "settings",
    "task": "tasks",
    "tasks": "tasks",
    "testing": "testing",
    "verification": "testing",
}

DJANGO_CATEGORY_TITLES = {
    "admin": "Admin",
    "apis": "APIs",
    "contracts": "Contracts",
    "deployment": "Deployment",
    "documents": "Documents",
    "domain": "Domain",
    "enums": "Enums",
    "environment": "Environment",
    "flows": "Flows",
    "frontend": "Frontend",
    "integrations": "Integrations",
    "mixins": "Mixins",
    "models": "Models",
    "observability": "Observability",
    "performance": "Performance",
    "repository": "Repository",
    "security": "Security",
    "settings": "Settings",
    "tasks": "Tasks",
    "testing": "Testing",
}

DJANGO_CATEGORY_ORDER = (
    "models",
    "mixins",
    "enums",
    "apis",
    "tasks",
    "admin",
    "integrations",
    "contracts",
    "domain",
    "security",
    "frontend",
    "testing",
    "settings",
    "observability",
    "deployment",
    "environment",
    "repository",
    "performance",
    "flows",
    "documents",
)


def django_document_location(document: dict[str, Any]) -> tuple[str, str]:
    artifact_id = str(document.get("id", "document"))
    parts = artifact_id.split(".")
    declared_app = document.get("app")
    app = django_app_id(declared_app if declared_app is not None else parts[0], artifact_id)
    if not parts or parts[0] != app:
        migration_ownership_error(
            artifact_id,
            "document id must begin with its explicit Django app owner",
        )
    category = DJANGO_DOCUMENT_CATEGORIES.get(parts[1] if len(parts) > 1 else "", "documents")
    return app, category


def django_flow_location(flow: dict[str, Any]) -> tuple[str, str]:
    artifact_id = str(flow.get("id", "flow"))
    parts = artifact_id.split(".")
    if len(parts) < 2:
        migration_ownership_error(artifact_id, "flow ids must begin with a Django app identifier")
    app = django_app_id(parts[0], artifact_id)
    second = parts[1]
    if second in {"task", "tasks", "beat", "beats", "scheduled", "schedules"}:
        return app, "tasks"
    nodes = [node for node in flow.get("nodes", []) if isinstance(node, dict)]
    node_types = {node.get("type") for node in nodes}
    entry_ids = flow.get("entryNodeIds")
    entry_types = {
        node.get("type")
        for node in nodes
        if isinstance(entry_ids, list) and node.get("id") in entry_ids
    }
    if "job" in node_types and entry_types and entry_types <= {"event", "job"}:
        return app, "tasks"
    if "api" in node_types:
        return app, "apis"
    return app, "flows"


def legacy_detail_items(value: dict[str, Any]) -> list[dict[str, str]]:
    details: list[dict[str, str]] = []
    reference = value.get("reference")
    if isinstance(reference, dict):
        for key, item in reference.items():
            if isinstance(item, (str, int, float, bool)):
                detail_id = re.sub(r"[^a-z0-9]+", "-", str(key).lower()).strip("-") or "value"
                details.append({"id": detail_id, "label": str(key), "value": str(item)})
    processing = value.get("processing")
    if isinstance(processing, list):
        for index, step in enumerate(processing):
            if not isinstance(step, dict):
                continue
            step_id = step.get("id") if isinstance(step.get("id"), str) else f"step-{index + 1}"
            text = step.get("title", "")
            reads = step.get("reads", [])
            writes = step.get("writes", [])
            suffixes = []
            if reads:
                suffixes.append("Reads: " + ", ".join(str(item) for item in reads))
            if writes:
                suffixes.append("Writes: " + ", ".join(str(item) for item in writes))
            details.append(
                {
                    "id": f"processing-{safe_id(step_id, 'legacy processing id', 80)}",
                    "label": "Processing",
                    "value": "; ".join([str(text), *suffixes]),
                }
            )
    return details


def legacy_document_sections(document: dict[str, Any]) -> list[dict[str, Any]]:
    converted: list[dict[str, Any]] = []
    for section_index, section in enumerate(document.get("sections", [])):
        if not isinstance(section, dict):
            continue
        section_id = section.get("id") if isinstance(section.get("id"), str) else f"section-{section_index + 1}"
        blocks: list[dict[str, Any]] = []
        section_type = section.get("type", "text")
        if section_type == "text":
            blocks.append(
                {
                    "id": f"{section_id}-text",
                    "type": "paragraph",
                    "text": str(section.get("content", section.get("text", ""))) or "No content recorded.",
                }
            )
        elif section_type == "list":
            items = section.get("items", [])
            blocks.append(
                {
                    "id": f"{section_id}-list",
                    "type": "list",
                    "items": [
                        str(item.get("text", item.get("title", item))) if isinstance(item, dict) else str(item)
                        for item in items
                    ]
                    or ["No items recorded."],
                }
            )
        elif section_type == "code":
            blocks.append(
                {
                    "id": f"{section_id}-code",
                    "type": "code",
                    "language": str(section.get("language", "text")),
                    "text": str(section.get("content", section.get("code", ""))) or "# No code recorded",
                }
            )
        else:
            values: list[dict[str, str]] = []
            for row in section.get("rows", []):
                if isinstance(row, dict):
                    cells = row.get("cells", row)
                    values.append(
                        {
                            "label": str(row.get("title", row.get("id", "Row"))),
                            "value": ", ".join(f"{key}: {item}" for key, item in cells.items())
                            if isinstance(cells, dict)
                            else str(cells),
                        }
                    )
            blocks.append(
                {
                    "id": f"{section_id}-values",
                    "type": "key-value",
                    "values": values or [{"label": "Content", "value": "No values recorded."}],
                }
            )
        converted.append(
            {
                "id": safe_id(section_id, "legacy section id"),
                "title": str(section.get("title", section.get("name", "Section"))),
                "blocks": blocks,
            }
        )
    return converted or [
        {
            "id": "overview",
            "title": "Overview",
            "blocks": [{"id": "overview-text", "type": "paragraph", "text": "No content recorded."}],
        }
    ]


def convert_legacy_snapshot_generic(legacy: dict[str, list[dict[str, Any]]]) -> dict[str, Any]:
    pages: dict[str, dict[str, Any]] = {}
    app_pages: dict[str, list[dict[str, str]]] = {}

    def add_page(app: str, page: dict[str, Any], title: str) -> None:
        pages[page["id"]] = page
        navigation_title, _ = concise_legacy_label(
            title, LEGACY_NAVIGATION_TITLE_MAXIMUM, "navigation title"
        )
        app_pages.setdefault(app, []).append(
            {
                "id": f"nav.{page['id']}",
                "type": "page",
                "title": navigation_title,
                "pageId": page["id"],
            }
        )

    for model in legacy["models"]:
        app = legacy_model_page_app(model)
        relationships = model.get("relationships", [])
        relationship_by_field = {
            relationship.get("source", {}).get("fieldId"): relationship
            for relationship in relationships
            if isinstance(relationship, dict) and isinstance(relationship.get("source"), dict)
        }
        for entity in model.get("entities", []):
            if not isinstance(entity, dict) or not isinstance(entity.get("id"), str):
                continue
            page_id = f"table.{app}.{entity['id']}"
            rows: list[dict[str, Any]] = []
            for field in entity.get("fields", []):
                if not isinstance(field, dict) or not isinstance(field.get("id"), str):
                    continue
                flags = [
                    key.replace("Key", " key").replace("primary key", "primary key")
                    for key in ("primaryKey", "unique", "indexed", "nullable", "generated", "sensitive")
                    if field.get(key) is True
                ]
                relationship = relationship_by_field.get(field["id"])
                relation = ""
                if isinstance(relationship, dict):
                    target = relationship.get("target", {})
                    relation = f"{relationship.get('cardinality', '')} -> {target.get('entityId', '')}.{target.get('fieldId', '')}"
                rows.append(
                    {
                        "id": field["id"],
                        "cells": {
                            "kind": "Field",
                            "name": str(field.get("name", field["id"])),
                            "definition": str(field.get("type", "")),
                            "rules": ", ".join(flags),
                            "relation": relation,
                        },
                    }
                )
            for kind, values in (("Index", entity.get("indexes", [])), ("Constraint", entity.get("constraints", []))):
                for value in values:
                    if not isinstance(value, dict) or not isinstance(value.get("id"), str):
                        continue
                    definition = value.get("expression") or ", ".join(value.get("fields", []))
                    rules = "unique" if value.get("unique") else ""
                    rows.append(
                        {
                            "id": value["id"],
                            "cells": {
                                "kind": kind,
                                "name": str(value.get("name", value["id"])),
                                "definition": str(definition),
                                "rules": rules,
                                "relation": "",
                            },
                        }
                    )
            details = [{"id": "table", "label": "Table", "value": str(entity.get("table", ""))}]
            page_title, full_title = concise_legacy_label(
                entity.get("name", entity["id"]), LEGACY_DISPLAY_TITLE_MAXIMUM, "table title"
            )
            preserve_legacy_label(details, "title", full_title)
            page = {
                "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
                "id": page_id,
                "type": "table",
                "title": page_title,
                "summary": str(model.get("summary", "")),
                "details": details,
                "columns": [
                    {"id": "kind", "label": "Kind", "format": "badge"},
                    {"id": "name", "label": "Name", "format": "code"},
                    {"id": "definition", "label": "Definition", "format": "code"},
                    {"id": "rules", "label": "Rules"},
                    {"id": "relation", "label": "Relation", "format": "reference"},
                ],
                "rows": rows,
            }
            add_page(app, page, page["title"])

    node_type_map = {
        "actor": "actor",
        "ui_action": "action",
        "form": "interface",
        "api": "api",
        "function": "action",
        "decision": "decision",
        "db_read": "database",
        "db_write": "database",
        "cache": "cache",
        "queue": "queue",
        "job": "job",
        "event": "event",
        "external": "external",
        "external_service": "external",
        "subprocess": "subprocess",
        "terminal": "terminal",
        "error": "error",
    }
    for flow in legacy["flows"]:
        flow_id = str(flow.get("id", "flow"))
        app = flow_id.split(".", 1)[0]
        page_id = f"flow.{flow_id}"
        nodes = []
        for node in flow.get("nodes", []):
            if not isinstance(node, dict) or not isinstance(node.get("id"), str):
                continue
            node_title, full_title = concise_legacy_label(
                node.get("title", node["id"]), LEGACY_DISPLAY_TITLE_MAXIMUM, "node title"
            )
            converted = {
                "id": node["id"],
                "type": node_type_map.get(str(node.get("type")), "action"),
                "title": node_title,
            }
            details = legacy_detail_items(node)
            if details:
                converted["details"] = details
            description = append_legacy_description(
                node.get("description"), "title", full_title
            )
            if description:
                converted["description"] = description
            nodes.append(converted)
        edges = []
        for edge in flow.get("edges", []):
            if not isinstance(edge, dict) or not isinstance(edge.get("id"), str):
                continue
            edge_type = edge.get("type")
            style = "async" if edge_type == "async" else "error" if edge_type == "error" else "normal"
            converted_edge = {
                "id": edge["id"],
                "from": str(edge.get("source", edge.get("from", ""))),
                "to": str(edge.get("target", edge.get("to", ""))),
                "style": style,
            }
            if edge.get("label"):
                edge_label, full_label = concise_legacy_label(
                    edge["label"], 80, "edge label"
                )
                converted_edge["label"] = edge_label
                description = append_legacy_description(
                    edge.get("description"), "label", full_label
                )
                if description:
                    converted_edge["description"] = description
            elif edge.get("description"):
                converted_edge["description"] = str(edge["description"])
            edges.append(converted_edge)
        details: list[dict[str, str]] = []
        page_title, full_title = concise_legacy_label(
            flow.get("name", flow_id), LEGACY_DISPLAY_TITLE_MAXIMUM, "flow title"
        )
        preserve_legacy_label(details, "title", full_title)
        page = {
            "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
            "id": page_id,
            "type": "flow",
            "title": page_title,
            "summary": str(flow.get("summary", "")),
            "layout": {"direction": "down"},
            "nodes": nodes,
            "edges": edges,
        }
        if details:
            page["details"] = details
        add_page(app, page, page["title"])

    for document in legacy["documents"]:
        document_id = str(document.get("id", "document"))
        app = str(document.get("app", document_id.split(".", 1)[0]))
        page_id = f"document.{document_id}"
        details = []
        page_title, full_title = concise_legacy_label(
            document.get("name", document_id), LEGACY_DISPLAY_TITLE_MAXIMUM, "document title"
        )
        preserve_legacy_label(details, "title", full_title)
        page = {
            "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
            "id": page_id,
            "type": "document",
            "title": page_title,
            "summary": str(document.get("summary", "")),
            "sections": legacy_document_sections(document),
        }
        if details:
            page["details"] = details
        add_page(app, page, page["title"])

    navigation_items = []
    for app, children in sorted(app_pages.items()):
        navigation_items.append(
            {
                "id": f"area.{app}",
                "type": "folder",
                "title": app.replace("_", " ").replace("-", " ").title(),
                "children": children,
            }
        )
    return {
        "navigation": {"schemaVersion": ARCHITECTURE_SCHEMA_VERSION, "items": navigation_items},
        "pages": pages,
    }


def django_navigation_for_legacy(
    legacy: dict[str, list[dict[str, Any]]], pages: dict[str, dict[str, Any]]
) -> dict[str, Any]:
    app_pages: dict[str, dict[str, list[dict[str, str]]]] = {}

    def add_page(app: str, category: str, page_id: str) -> None:
        page = pages.get(page_id)
        if page is None:
            raise AgentFlowError(
                "MIGRATION_FAILED",
                f"Converted page is missing for legacy artifact: {page_id}",
            )
        title, _ = concise_legacy_label(
            page["title"], LEGACY_NAVIGATION_TITLE_MAXIMUM, "navigation title"
        )
        app_pages.setdefault(app, {}).setdefault(category, []).append(
            {"id": f"nav.{page_id}", "type": "page", "title": title, "pageId": page_id}
        )

    for model in legacy["models"]:
        app, category = django_model_location(model)
        page_app = legacy_model_page_app(model)
        for entity in model.get("entities", []):
            if isinstance(entity, dict) and isinstance(entity.get("id"), str):
                add_page(app, category, f"table.{page_app}.{entity['id']}")

    for flow in legacy["flows"]:
        app, category = django_flow_location(flow)
        flow_id = str(flow.get("id", "flow"))
        add_page(app, category, f"flow.{flow_id}")

    for document in legacy["documents"]:
        app, category = django_document_location(document)
        document_id = str(document.get("id", "document"))
        add_page(app, category, f"document.{document_id}")

    category_order = {category: index for index, category in enumerate(DJANGO_CATEGORY_ORDER)}
    navigation_items: list[dict[str, Any]] = []
    for app in sorted(app_pages, key=lambda value: (value == "platform", value)):
        folders = []
        for category, children in sorted(
            app_pages[app].items(), key=lambda item: (category_order.get(item[0], 999), item[0])
        ):
            if not children:
                continue
            folders.append(
                {
                    "id": f"folder.{app}.{category}",
                    "type": "folder",
                    "title": DJANGO_CATEGORY_TITLES.get(category, title_case_identifier(category)),
                    "children": children,
                }
            )
        if folders:
            navigation_items.append(
                {
                    "id": f"area.{app}",
                    "type": "folder",
                    "title": "Platform" if app == "platform" else title_case_identifier(app),
                    "children": folders,
                }
            )
    return {"schemaVersion": ARCHITECTURE_SCHEMA_VERSION, "items": navigation_items}


def convert_legacy_snapshot(
    legacy: dict[str, list[dict[str, Any]]], profile: str
) -> dict[str, Any]:
    snapshot = convert_legacy_snapshot_generic(legacy)
    if profile == DEFAULT_HARNESS_PROFILE:
        return snapshot
    if profile != "django":
        raise AgentFlowError(
            "MIGRATION_PROFILE_UNSUPPORTED",
            "The installed AgentFlow profile does not support v3 migration.",
            {"profile": profile, "supportedProfiles": sorted(SUPPORTED_MIGRATION_PROFILES)},
        )
    snapshot["navigation"] = django_navigation_for_legacy(legacy, snapshot["pages"])
    return snapshot


def load_v2_revision(harness: Path, revision_id: str) -> tuple[dict[str, Any], dict[str, Any]]:
    safe_id(revision_id, "v2 revision id")
    directory = require_directory(harness / "revisions", "v2 revisions directory") / revision_id
    if directory.is_symlink() or not directory.is_dir():
        raise AgentFlowError("REVISION_NOT_FOUND", f"v2 revision does not exist: {revision_id}")
    manifest = read_json(directory / "manifest.json")
    navigation = read_json(directory / "navigation.json")
    pages_directory = require_directory(directory / "pages", "v2 revision pages directory")
    pages: dict[str, dict[str, Any]] = {}
    for path in sorted(pages_directory.glob("*.json")):
        page = read_json(path)
        if not isinstance(page, dict):
            raise AgentFlowError("CORRUPT_REVISION", f"v2 revision page must be an object: {path}")
        page_id = safe_id(page.get("id"), "page id")
        if path.stem != page_id or page_id in pages:
            raise AgentFlowError("CORRUPT_REVISION", f"v2 revision page identity mismatch: {path}")
        pages[page_id] = page
    legacy_snapshot = {"navigation": navigation, "pages": pages}
    digest = content_hash(snapshot_integrity_payload(legacy_snapshot))
    snapshot = v2_snapshot_to_current(legacy_snapshot)
    issues = validate_snapshot(snapshot)
    if (
        not isinstance(manifest, dict)
        or manifest.get("schemaVersion") != 2
        or manifest.get("id") != revision_id
        or manifest.get("hash") != digest
        or manifest.get("pageCount") != len(pages)
    ):
        raise AgentFlowError("CORRUPT_REVISION", f"v2 revision manifest is invalid: {revision_id}")
    if issues:
        raise AgentFlowError("CORRUPT_REVISION", f"v2 revision is invalid: {revision_id}", {"issues": issues})
    return manifest, snapshot


def load_v2_revisions(harness: Path) -> dict[str, tuple[dict[str, Any], dict[str, Any]]]:
    directory = require_directory(harness / "revisions", "v2 revisions directory")
    revisions: dict[str, tuple[dict[str, Any], dict[str, Any]]] = {}
    for path in sorted(directory.iterdir()):
        if path.name.startswith("."):
            continue
        if path.is_symlink() or not path.is_dir():
            raise AgentFlowError("CORRUPT_REVISION", f"Invalid v2 revision path: {path}")
        revision_id = safe_id(path.name, "v2 revision id")
        revisions[revision_id] = load_v2_revision(harness, revision_id)
    return revisions


def v2_revision_references(
    revision_id: str, revisions: dict[str, tuple[dict[str, Any], dict[str, Any]]]
) -> set[str]:
    manifest, snapshot = revisions[revision_id]
    parent_id = manifest.get("parentRevision")
    if not isinstance(parent_id, str):
        return architecture_references(snapshot)
    if parent_id not in revisions:
        raise AgentFlowError(
            "CORRUPT_REVISION",
            f"v2 revision parent is missing: {revision_id} -> {parent_id}",
        )
    return required_proposal_references(revisions[parent_id][1], snapshot)


def v2_hash_for_revision(
    revision_id: Any, hashes: dict[str, str], label: str
) -> str:
    safe_id(revision_id, label)
    if revision_id not in hashes:
        raise AgentFlowError("MIGRATION_UNSUPPORTED", f"{label} is not present in v2 history: {revision_id}")
    return hashes[revision_id]


def v2_snapshot_to_current(legacy_snapshot: dict[str, Any]) -> dict[str, Any]:
    snapshot = copy.deepcopy(legacy_snapshot)
    navigation = snapshot.get("navigation")
    if isinstance(navigation, dict):
        navigation["schemaVersion"] = ARCHITECTURE_SCHEMA_VERSION
    pages = snapshot.get("pages")
    if isinstance(pages, dict):
        for page in pages.values():
            if isinstance(page, dict):
                page["schemaVersion"] = ARCHITECTURE_SCHEMA_VERSION
    return snapshot


def transform_v2_proposal(
    proposal: Any, hashes: dict[str, str]
) -> dict[str, Any]:
    if not isinstance(proposal, dict):
        raise AgentFlowError("CORRUPT_PROPOSAL", "v2 proposal must be an object.")
    value = copy.deepcopy(proposal)
    revision_id = value.pop("baseRevision", None)
    value["baseArchitectureHash"] = v2_hash_for_revision(
        revision_id, hashes, "proposal base revision"
    )
    value["schemaVersion"] = ARCHITECTURE_SCHEMA_VERSION
    for change in value.get("changes", []):
        if not isinstance(change, dict):
            continue
        navigation = change.get("navigation")
        if isinstance(navigation, dict):
            navigation["schemaVersion"] = ARCHITECTURE_SCHEMA_VERSION
        page = change.get("page")
        if isinstance(page, dict):
            page["schemaVersion"] = ARCHITECTURE_SCHEMA_VERSION
    return value


def v2_migration_updates(
    harness: Path,
    revisions: dict[str, tuple[dict[str, Any], dict[str, Any]]],
) -> list[tuple[Path, dict[str, Any]]]:
    hashes = {
        revision_id: content_hash(snapshot_integrity_payload(snapshot))
        for revision_id, (_, snapshot) in revisions.items()
    }
    updates: list[tuple[Path, dict[str, Any]]] = []
    proposals = require_directory(harness / "proposals", "proposals directory")
    for directory in sorted(proposals.iterdir()):
        if directory.name.startswith("."):
            continue
        if directory.is_symlink() or not directory.is_dir():
            raise AgentFlowError("CORRUPT_PROPOSAL", f"Invalid v2 proposal path: {directory}")
        proposal_path = directory / "proposal.json"
        review_path = directory / "review.json"
        proposal = transform_v2_proposal(read_json(proposal_path), hashes)
        review = read_json(review_path)
        if not isinstance(review, dict):
            raise AgentFlowError("CORRUPT_PROPOSAL", f"v2 proposal review is invalid: {directory.name}")
        review = copy.deepcopy(review)
        approved_revision = review.pop("approvedRevision", None)
        if approved_revision is not None:
            applied_hash = v2_hash_for_revision(
                approved_revision, hashes, "approved proposal revision"
            )
            review["appliedArchitectureHash"] = applied_hash
            review["requiredReferences"] = sorted(v2_revision_references(approved_revision, revisions))
        review["schemaVersion"] = ARCHITECTURE_SCHEMA_VERSION
        history = review.get("history")
        if isinstance(history, list):
            for entry in history:
                if isinstance(entry, dict) and "revision" in entry:
                    entry["architectureHash"] = v2_hash_for_revision(
                        entry.pop("revision"), hashes, "proposal history revision"
                    )
        versions = require_directory(directory / "versions", "v2 proposal versions directory")
        version_hashes: dict[int, str] = {}
        for version_path in sorted(versions.glob("v*.json")):
            version = read_json(version_path)
            if not isinstance(version, dict):
                raise AgentFlowError("CORRUPT_PROPOSAL", f"v2 proposal version is invalid: {version_path}")
            transformed = transform_v2_proposal(version.get("proposal"), hashes)
            version_hash = content_hash(transformed)
            try:
                version_number = int(version_path.stem.removeprefix("v"))
            except ValueError as error:
                raise AgentFlowError(
                    "CORRUPT_PROPOSAL", f"v2 proposal version is invalid: {version_path}"
                ) from error
            version_hashes[version_number] = version_hash
            updates.append(
                (
                    version_path,
                    {
                        "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
                        "hash": version_hash,
                        "proposal": transformed,
                    },
                )
            )
        if review.get("status") in {"under_review", "approved", "rejected"}:
            current_version = review.get("currentVersion")
            if current_version not in version_hashes:
                raise AgentFlowError(
                    "CORRUPT_PROPOSAL", f"v2 proposal has no current submitted version: {directory.name}"
                )
            review["submittedHash"] = version_hashes[current_version]
        updates.extend(((proposal_path, proposal), (review_path, review)))
    for directory_name, filename, field in (
        ("plans", "manifest.json", "architectureRevision"),
        ("implementation", "evidence.json", "architectureRevision"),
    ):
        directory = harness / directory_name
        if not directory.exists():
            continue
        require_directory(directory, f"v2 {directory_name} directory")
        for child in sorted(directory.iterdir()):
            if child.name.startswith(".") or not child.is_dir() or child.is_symlink():
                continue
            path = child / filename
            if not path.exists():
                continue
            value = read_json(path)
            if not isinstance(value, dict):
                raise AgentFlowError("CORRUPT_STATE", f"v2 {directory_name} record is invalid: {path}")
            value = copy.deepcopy(value)
            revision_id = value.pop(field, None)
            value["architectureHash"] = v2_hash_for_revision(
                revision_id, hashes, f"{directory_name} architecture revision"
            )
            value["schemaVersion"] = ARCHITECTURE_SCHEMA_VERSION
            updates.append((path, value))
    inbox = harness / "comment-inbox"
    if inbox.exists():
        require_directory(inbox, "v2 comment inbox directory")
        for path in sorted(inbox.rglob("*.json")):
            if path.is_symlink() or not path.is_file():
                raise AgentFlowError("UNSAFE_PATH", f"v2 comment request is unsafe: {path}")
            request = read_json(path)
            if not isinstance(request, dict):
                raise AgentFlowError("CORRUPT_PROPOSAL", f"v2 comment request is invalid: {path}")
            request = copy.deepcopy(request)
            request["schemaVersion"] = ARCHITECTURE_SCHEMA_VERSION
            updates.append((path, request))
    return updates


def archive_legacy_v3(harness: Path, raw_state: dict[str, Any]) -> None:
    legacy_root = harness / "legacy-v3"
    if legacy_root.exists() or legacy_root.is_symlink():
        raise AgentFlowError("MIGRATION_FAILED", "Legacy v3 archive already exists.")
    legacy_root.mkdir()
    atomic_json(legacy_root / "state.json", raw_state)
    active_revision = safe_id(raw_state.get("activeRevision"), "legacy active revision")
    revisions = require_directory(harness / "revisions", "legacy revisions directory")
    active_source = revisions / active_revision
    if active_source.is_symlink() or not active_source.is_dir():
        raise AgentFlowError("REVISION_NOT_FOUND", "Legacy active revision is missing.")
    (legacy_root / "revisions").mkdir()
    os.replace(active_source, legacy_root / "revisions" / active_revision)
    shutil.rmtree(revisions)
    for name in ("proposals", "comment-inbox", "plans", "implementation"):
        source = harness / name
        if source.exists():
            os.replace(source, legacy_root / name)
    (harness / "proposals").mkdir()
    (harness / "plans").mkdir()
    (harness / "implementation").mkdir()


def archived_legacy_state(harness: Path) -> dict[str, Any]:
    path = harness / "legacy-v3" / "state.json"
    if not path.exists():
        raise AgentFlowError(
            "MIGRATION_REBUILD_UNAVAILABLE",
            "No preserved .agentflow/legacy-v3 state is available for rebuild.",
        )
    value = read_json(path)
    if not isinstance(value, dict) or value.get("schemaVersion") != 1:
        raise AgentFlowError(
            "MIGRATION_REBUILD_UNAVAILABLE",
            "The preserved legacy state is not a valid AgentFlow v3 state.",
        )
    if not isinstance(value.get("activeRevision"), str):
        raise AgentFlowError(
            "MIGRATION_REBUILD_UNAVAILABLE",
            "The preserved legacy state has no active revision.",
        )
    return value


def has_client_entries(path: Path) -> bool:
    if not path.exists():
        return False
    if path.is_symlink() or not path.is_dir():
        raise AgentFlowError("UNSAFE_PATH", f"Migration directory is invalid: {path}")
    return any(entry.name != ".gitkeep" for entry in path.iterdir())


def require_rebuild_safe(harness: Path, state: dict[str, Any], legacy_revision: str) -> None:
    manifest, _ = load_architecture(harness)
    implemented = safe_hash(state.get("implementedArchitectureHash"), "implemented architecture hash")
    problems: list[str] = []
    if manifest["hash"] != implemented:
        problems.append("current architecture is awaiting implementation")
    for name in ("proposals", "plans", "implementation", "comment-inbox"):
        if has_client_entries(harness / name):
            problems.append(f".agentflow/{name} contains v4 client work")
    if problems:
        raise AgentFlowError(
            "MIGRATION_REBUILD_UNSAFE",
            "Cannot rebuild from legacy v3 because the current v4 state contains work that could be superseded.",
            {
                "problems": problems,
                "resolution": "Resolve or archive the listed v4 work, then rerun the rebuild command.",
            },
        )
    return None


def migration_snapshot(
    harness: Path, legacy_revision: str, profile: str
) -> dict[str, Any]:
    legacy = load_legacy_revision(harness, legacy_revision)
    snapshot = convert_legacy_snapshot(legacy, profile)
    issues = validate_snapshot(snapshot)
    if issues:
        raise AgentFlowError("MIGRATION_FAILED", "Converted architecture is invalid.", {"issues": issues})
    return snapshot


def replace_current_architecture(
    harness: Path,
    snapshot: dict[str, Any],
    proposal_id: str | None,
    applied_at: str,
    applied_by: str,
) -> dict[str, Any]:
    destination = harness / ARCHITECTURE_DIRECTORY
    if not destination.is_dir() or destination.is_symlink():
        raise AgentFlowError("ARCHITECTURE_NOT_FOUND", "Current architecture is missing.")
    temporary = harness / f".{ARCHITECTURE_DIRECTORY}-{uuid.uuid4().hex}"
    backup = harness / f".{ARCHITECTURE_DIRECTORY}-backup-{uuid.uuid4().hex}"
    issues = validate_snapshot(snapshot)
    if issues:
        raise AgentFlowError("VALIDATION_FAILED", "Architecture is invalid.", {"issues": issues})
    try:
        manifest = write_architecture_directory(
            temporary, snapshot, proposal_id, applied_at, applied_by
        )
        os.replace(destination, backup)
        os.replace(temporary, destination)
        shutil.rmtree(backup)
    finally:
        if temporary.exists():
            shutil.rmtree(temporary)
        if backup.exists() and not destination.exists():
            os.replace(backup, destination)
    return manifest


def migration_v2_path(harness: Path, relative: Any, label: str) -> Path:
    normalized, resolved = project_relative_path(harness, relative, label)
    if normalized != relative or not normalized.startswith(
        ("proposals/", "plans/", "implementation/", "comment-inbox/")
    ):
        raise AgentFlowError("CORRUPT_MIGRATION", f"{label} is outside managed migration records.")
    return resolved


def v2_active_proposal_ids(harness: Path, approved_revision: str) -> list[str]:
    stale: list[str] = []
    proposals = require_directory(harness / "proposals", "proposals directory")
    for directory in sorted(proposals.iterdir()):
        if directory.name.startswith("."):
            continue
        if directory.is_symlink() or not directory.is_dir():
            raise AgentFlowError("CORRUPT_PROPOSAL", f"Invalid v2 proposal path: {directory}")
        proposal = read_json(directory / "proposal.json")
        review = read_json(directory / "review.json")
        if not isinstance(proposal, dict) or not isinstance(review, dict):
            raise AgentFlowError("CORRUPT_PROPOSAL", f"v2 proposal is invalid: {directory.name}")
        if review.get("status") in {"draft", "under_review", "changes_requested"}:
            if proposal.get("baseRevision") != approved_revision:
                stale.append(directory.name)
    return stale


def migration_v2_stage_path(harness: Path, value: Any) -> Path:
    if not isinstance(value, str) or re.fullmatch(r"\.migration-v2-[a-f0-9]{32}", value) is None:
        raise AgentFlowError("CORRUPT_MIGRATION", "v2 migration staging directory is invalid.")
    path = harness / value
    if path.is_symlink() or not path.is_dir():
        raise AgentFlowError("CORRUPT_MIGRATION", "v2 migration staging directory is missing.")
    return path


def recover_v2_migration(harness: Path) -> None:
    journal_path = harness / V2_MIGRATION_TRANSACTION
    if not journal_path.exists():
        return
    journal = read_json(journal_path)
    required = {"schemaVersion", "stage", "records", "afterState", "architectureHash"}
    if not isinstance(journal, dict) or set(journal) != required or journal.get("schemaVersion") != 1:
        raise AgentFlowError("CORRUPT_MIGRATION", "v2 migration recovery journal is invalid.")
    stage = migration_v2_stage_path(harness, journal["stage"])
    records = journal.get("records")
    if not isinstance(records, list) or len(records) != len(set(records)):
        raise AgentFlowError("CORRUPT_MIGRATION", "v2 migration recovery records are invalid.")
    record_paths = [migration_v2_path(harness, value, "v2 migration record path") for value in records]
    after_state = journal.get("afterState")
    if not isinstance(after_state, dict) or after_state.get("schemaVersion") != ARCHITECTURE_SCHEMA_VERSION:
        raise AgentFlowError("CORRUPT_MIGRATION", "v2 migration recovery state is invalid.")
    architecture_hash = safe_hash(journal.get("architectureHash"), "v2 migration architecture hash")
    current_state = read_json(harness / "state.json")
    completed = isinstance(current_state, dict) and canonical_json(current_state) == canonical_json(after_state)
    if completed:
        manifest, _ = load_architecture_without_recovery(harness)
        if manifest["hash"] != architecture_hash:
            raise AgentFlowError("CORRUPT_MIGRATION", "Completed v2 migration architecture does not match its journal.")
        shutil.rmtree(stage)
        journal_path.unlink()
        return
    if not isinstance(current_state, dict) or current_state.get("schemaVersion") != 2:
        raise AgentFlowError("CORRUPT_MIGRATION", "v2 migration cannot determine a safe recovery state.")
    backups = require_directory(stage / "backups", "v2 migration backups directory")
    for relative, destination in zip(records, record_paths, strict=True):
        backup = backups / relative
        value = read_json(backup)
        atomic_json(destination, value)
    staged_revisions = stage / "revisions"
    revisions = harness / "revisions"
    if staged_revisions.exists():
        if revisions.exists() or revisions.is_symlink():
            raise AgentFlowError("CORRUPT_MIGRATION", "v2 migration cannot restore revisions safely.")
        os.replace(staged_revisions, revisions)
    architecture = harness / ARCHITECTURE_DIRECTORY
    if architecture.exists() or architecture.is_symlink():
        if architecture.is_symlink() or not architecture.is_dir():
            raise AgentFlowError("CORRUPT_MIGRATION", "v2 migration architecture path is unsafe.")
        shutil.rmtree(architecture)
    shutil.rmtree(stage)
    journal_path.unlink()


def migrate_v2(harness: Path, raw_state: dict[str, Any], apply: bool) -> dict[str, Any]:
    approved_revision = safe_id(raw_state.get("approvedRevision"), "approved v2 revision")
    implemented_revision = safe_id(raw_state.get("implementedRevision"), "implemented v2 revision")
    revisions = load_v2_revisions(harness)
    if approved_revision not in revisions or implemented_revision not in revisions:
        raise AgentFlowError("MIGRATION_UNSUPPORTED", "v2 state references a missing architecture revision.")
    stale_proposals = v2_active_proposal_ids(harness, approved_revision)
    if stale_proposals:
        raise AgentFlowError(
            "MIGRATION_ACTIVE_PROPOSAL_STALE",
            "Cannot migrate active proposals that are based on an older architecture revision.",
            {
                "proposalIds": stale_proposals,
                "resolution": "Resolve, reject, or revise these proposals against the v2 approved revision, then rerun migration.",
            },
        )
    hashes = {
        revision_id: content_hash(snapshot_integrity_payload(snapshot))
        for revision_id, (_, snapshot) in revisions.items()
    }
    updates = v2_migration_updates(harness, revisions)
    current_snapshot = revisions[approved_revision][1]
    result = {
        "required": True,
        "mode": "migrate-v2",
        "fromSchemaVersion": 2,
        "toSchemaVersion": ARCHITECTURE_SCHEMA_VERSION,
        "pageCount": len(current_snapshot["pages"]),
        "revisionCountRemoved": len(revisions),
        "architectureHash": hashes[approved_revision],
    }
    if not apply:
        return result
    if (harness / ARCHITECTURE_DIRECTORY).exists() or (harness / ARCHITECTURE_DIRECTORY).is_symlink():
        raise AgentFlowError(
            "MIGRATION_UNSAFE",
            "Current architecture already exists while the harness still has v2 state.",
        )
    applied_at = utc_now()
    revisions_path = harness / "revisions"
    if revisions_path.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", "v2 revisions directory must not be a symbolic link.")
    after_state = {
        "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
        "implementedArchitectureHash": hashes[implemented_revision],
        "updatedAt": applied_at,
    }
    records: list[str] = []
    update_values: dict[str, dict[str, Any]] = {}
    for path, value in updates:
        relative = path.relative_to(harness).as_posix()
        migration_v2_path(harness, relative, "v2 migration record path")
        if relative in update_values:
            raise AgentFlowError("CORRUPT_MIGRATION", f"Duplicate v2 migration record: {relative}")
        if path.is_symlink() or not path.is_file():
            raise AgentFlowError("UNSAFE_PATH", f"v2 migration record is unsafe: {path}")
        records.append(relative)
        update_values[relative] = value
    stage_name = f".migration-v2-{uuid.uuid4().hex}"
    stage = harness / stage_name
    stage.mkdir()
    journal_path = harness / V2_MIGRATION_TRANSACTION
    try:
        manifest = write_architecture_directory(
            stage / ARCHITECTURE_DIRECTORY,
            current_snapshot,
            None,
            applied_at,
            "migration",
        )
        backups = stage / "backups"
        backups.mkdir()
        for relative in records:
            source = migration_v2_path(harness, relative, "v2 migration record path")
            destination = backups / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, destination, follow_symlinks=False)
        atomic_json(
            journal_path,
            {
                "schemaVersion": 1,
                "stage": stage_name,
                "records": records,
                "afterState": after_state,
                "architectureHash": manifest["hash"],
            },
        )
    except Exception:
        shutil.rmtree(stage, ignore_errors=True)
        raise
    try:
        for relative, value in update_values.items():
            atomic_json(migration_v2_path(harness, relative, "v2 migration record path"), value)
        os.replace(revisions_path, stage / "revisions")
        os.replace(stage / ARCHITECTURE_DIRECTORY, harness / ARCHITECTURE_DIRECTORY)
        atomic_json(harness / "state.json", after_state)
    except Exception:
        recover_v2_migration(harness)
        raise
    shutil.rmtree(stage)
    journal_path.unlink(missing_ok=True)
    return {**result, "migrated": True, "architectureHash": manifest["hash"]}


def migrate_v3(harness: Path, apply: bool, rebuild_from_v3: bool) -> dict[str, Any]:
    recover_v2_migration(harness)
    raw_state = read_json(harness / "state.json")
    if not isinstance(raw_state, dict):
        raise AgentFlowError("CORRUPT_STATE", "AgentFlow state must be an object.")
    profile = installed_harness_profile(harness)
    schema_version = raw_state.get("schemaVersion")
    if schema_version == ARCHITECTURE_SCHEMA_VERSION:
        if not rebuild_from_v3:
            return {
                "required": False,
                "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
                "profile": profile,
                "rebuildFromV3": (harness / "legacy-v3" / "state.json").is_file(),
            }
        legacy_state = archived_legacy_state(harness)
        legacy_revision = safe_id(legacy_state["activeRevision"], "legacy active revision")
        require_rebuild_safe(harness, raw_state, legacy_revision)
        snapshot = migration_snapshot(harness, legacy_revision, profile)
        current_manifest, current_snapshot = load_architecture(harness)
        if snapshot_integrity_payload(snapshot) == snapshot_integrity_payload(current_snapshot):
            return {
                "required": False,
                "migrated": False,
                "mode": "rebuild-from-v3",
                "fromSchemaVersion": 1,
                "toSchemaVersion": ARCHITECTURE_SCHEMA_VERSION,
                "profile": profile,
                "legacyRevision": legacy_revision,
                "pageCount": len(snapshot["pages"]),
                "architectureHash": current_manifest["hash"],
            }
        result = {
            "required": True,
            "mode": "rebuild-from-v3",
            "fromSchemaVersion": 1,
            "toSchemaVersion": ARCHITECTURE_SCHEMA_VERSION,
            "profile": profile,
            "legacyRevision": legacy_revision,
            "pageCount": len(snapshot["pages"]),
        }
        if not apply:
            return result
        applied_at = utc_now()
        manifest = replace_current_architecture(
            harness, snapshot, None, applied_at, "migration"
        )
        state = {
            "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
            "implementedArchitectureHash": manifest["hash"],
            "updatedAt": applied_at,
            "legacyV3Revision": legacy_revision,
        }
        atomic_json(harness / "state.json", state)
        return {**result, "migrated": True, "architectureHash": manifest["hash"]}
    if rebuild_from_v3:
        raise AgentFlowError(
            "MIGRATION_REBUILD_UNAVAILABLE",
            "Rebuild from v3 is available only after a completed legacy-v3 migration.",
        )
    if schema_version == 2:
        return migrate_v2(harness, raw_state, apply)
    if schema_version != 1 or not isinstance(raw_state.get("activeRevision"), str):
        raise AgentFlowError(
            "MIGRATION_UNSUPPORTED", "Only legacy AgentFlow schema 1 or 2 state can migrate."
        )
    legacy_revision = safe_id(raw_state["activeRevision"], "legacy active revision")
    snapshot = migration_snapshot(harness, legacy_revision, profile)
    result = {
        "required": True,
        "mode": "migrate-v3",
        "fromSchemaVersion": 1,
        "toSchemaVersion": ARCHITECTURE_SCHEMA_VERSION,
        "profile": profile,
        "legacyRevision": legacy_revision,
        "pageCount": len(snapshot["pages"]),
    }
    if not apply:
        return result
    applied_at = utc_now()
    manifest = initialize_architecture(harness, snapshot, None, applied_at, "migration")
    archive_legacy_v3(harness, raw_state)
    atomic_json(
        harness / "state.json",
        {
            "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
            "implementedArchitectureHash": manifest["hash"],
            "legacyV3Revision": legacy_revision,
            "updatedAt": applied_at,
        },
    )
    return {
        **result,
        "migrated": True,
        "architectureHash": manifest["hash"],
        "legacyArchive": "legacy-v3",
    }


def choose_proposal(harness: Path, requested: str | None) -> str | None:
    proposals = list_proposals(harness)
    if requested:
        safe_id(requested, "proposal id")
        if not any(proposal["id"] == requested for proposal in proposals):
            raise AgentFlowError("PROPOSAL_NOT_FOUND", f"Proposal does not exist: {requested}")
        return requested
    for status in ("changes_requested", "under_review", "draft"):
        for proposal in proposals:
            if proposal["status"] == status:
                return proposal["id"]
    return proposals[0]["id"] if proposals else None


def comment_bridge_state_path(harness: Path) -> Path:
    return harness / ".runtime" / "comment-bridge.json"


def read_comment_bridge_state(harness: Path) -> dict[str, Any] | None:
    path = comment_bridge_state_path(harness)
    if path.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", "Comment writer state must not be a symbolic link.")
    if not path.exists():
        return None
    try:
        state = read_json(path)
    except AgentFlowError:
        return None
    return state if isinstance(state, dict) else None


def comment_bridge_public_state(state: dict[str, Any]) -> dict[str, str]:
    return {
        "endpoint": f"http://127.0.0.1:{state['port']}",
        "token": state["token"],
    }


def comment_bridge_healthy(state: dict[str, Any], project_id: str) -> bool:
    port = state.get("port")
    token = state.get("token")
    if (
        not isinstance(port, int)
        or isinstance(port, bool)
        or not 1 <= port <= 65535
        or not isinstance(token, str)
        or len(token) < 32
    ):
        return False
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=0.4)
    try:
        connection.request("GET", "/health", headers={"X-AgentFlow-Token": token})
        response = connection.getresponse()
        if response.status != 200:
            return False
        payload = json.loads(response.read(65536))
    except (OSError, ValueError, http.client.HTTPException, json.JSONDecodeError):
        return False
    finally:
        connection.close()
    return (
        isinstance(payload, dict)
        and payload.get("ok") is True
        and payload.get("projectId") == project_id
    )


def ensure_comment_bridge(harness: Path) -> dict[str, str]:
    config = read_json(harness / "config.json")
    project_id = safe_id(config.get("projectId"), "project id")
    state_path = comment_bridge_state_path(harness)
    state = read_comment_bridge_state(harness)
    if state is not None and comment_bridge_healthy(state, project_id):
        return comment_bridge_public_state(state)
    if state_path.exists():
        state_path.unlink()
    runtime = state_path.parent
    if runtime.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", "Comment writer runtime must not be a symbolic link.")
    runtime.mkdir(parents=True, exist_ok=True)
    try:
        os.chmod(runtime, 0o700)
    except OSError:
        pass
    bridge_script = harness / "scripts" / "comment_bridge.py"
    if bridge_script.is_symlink() or not bridge_script.is_file():
        raise AgentFlowError("COMMENT_BRIDGE_MISSING", "AgentFlow comment writer is missing.")
    token = secrets.token_urlsafe(32)
    options: dict[str, Any] = {
        "cwd": str(harness.parent),
        "stdin": subprocess.DEVNULL,
        "stdout": subprocess.DEVNULL,
        "stderr": subprocess.DEVNULL,
    }
    if os.name == "nt":
        options["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP | subprocess.DETACHED_PROCESS
    else:
        options["start_new_session"] = True
    process = subprocess.Popen(
        [
            sys.executable,
            str(bridge_script),
            "--harness",
            str(harness),
            "--state",
            str(state_path),
            "--token",
            token,
            "--idle-seconds",
            str(COMMENT_BRIDGE_IDLE_SECONDS),
        ],
        **options,
    )
    deadline = time.monotonic() + 4
    while time.monotonic() < deadline:
        if process.poll() is not None:
            break
        time.sleep(0.05)
        state = read_comment_bridge_state(harness)
        if state is not None and state.get("token") == token and comment_bridge_healthy(state, project_id):
            return comment_bridge_public_state(state)
    try:
        process.terminate()
    except OSError:
        pass
    raise AgentFlowError("COMMENT_BRIDGE_FAILED", "Could not start the local comment writer.")


def proposal_render_bundle(
    harness: Path,
    proposal: dict[str, Any],
    review: dict[str, Any],
    architecture: dict[str, Any],
) -> dict[str, Any]:
    """Build viewer data without requiring a retained architecture snapshot.

    A proposal can become stale after another approved proposal replaces the
    current architecture. Its original diff cannot be recreated without
    retaining a duplicate architecture tree, so keep the proposal visible as a
    rebase-required record while the lifecycle commands continue to reject it.
    """
    before = architecture
    proposed = architecture
    stale = False
    if review["status"] != "approved":
        try:
            before, proposed = proposal_candidate(harness, proposal)
        except AgentFlowError as error:
            if error.code != "STALE_PROPOSAL":
                raise
            stale = True
    diff = (
        architecture_diff(before, proposed)
        if review["status"] != "approved" and not stale
        else {"navigation": {}, "pages": {}}
    )
    return {
        "proposal": proposal,
        "review": review,
        "before": snapshot_payload(before),
        "proposed": snapshot_payload(proposed),
        "diff": diff,
        "recordOnly": review["status"] == "approved" or stale,
        "stale": stale,
    }


def render_review(
    harness: Path,
    requested_proposal: str | None,
    should_open: bool,
    comments_enabled: bool,
) -> dict[str, Any]:
    import_all_comment_inboxes(harness)
    config = read_json(harness / "config.json")
    state = load_state(harness)
    architecture_manifest, architecture = load_architecture(harness)
    proposal_id = choose_proposal(harness, requested_proposal)
    proposal = None
    review = None
    selected_bundle = None
    if proposal_id is not None:
        proposal, review = load_proposal(harness, proposal_id)
    proposals = list_proposals(harness)
    all_comments = list_comments(harness, None, None)
    proposal_reviews: dict[str, Any] = {}
    for summary in proposals:
        item_proposal, item_review = (
            (proposal, review)
            if summary["id"] == proposal_id and proposal is not None and review is not None
            else load_proposal(harness, summary["id"])
        )
        bundle = proposal_render_bundle(harness, item_proposal, item_review, architecture)
        proposal_reviews[summary["id"]] = bundle
        if summary["id"] == proposal_id:
            selected_bundle = bundle
    before = selected_bundle["before"] if selected_bundle is not None else snapshot_payload(architecture)
    proposed = selected_bundle["proposed"] if selected_bundle is not None else snapshot_payload(architecture)
    diff = selected_bundle["diff"] if selected_bundle is not None else {"navigation": {}, "pages": {}}
    bridge = ensure_comment_bridge(harness) if comments_enabled and proposals else None
    payload = {
        "schemaVersion": ARCHITECTURE_SCHEMA_VERSION,
        "generatedAt": utc_now(),
        "project": config,
        "state": state,
        "nodeTypes": FLOW_NODE_TYPES,
        "implemented": snapshot_payload(architecture),
        "approved": snapshot_payload(architecture),
        "architectureHash": architecture_manifest["hash"],
        "proposal": proposal,
        "review": review,
        "proposals": proposals,
        "proposalReviews": proposal_reviews,
        "comments": all_comments,
        "plans": list_plans(harness),
        "before": before,
        "proposed": proposed,
        "diff": diff,
        "commentBridge": bridge,
    }
    template_path = harness / "viewer" / "template.html"
    if template_path.is_symlink() or not template_path.is_file():
        raise AgentFlowError("INVALID_TEMPLATE", "AgentFlow reviewer template is missing.")
    template = template_path.read_text(encoding="utf-8")
    marker = "__AGENTFLOW_REVIEW_DATA__"
    if template.count(marker) != 1:
        raise AgentFlowError("INVALID_TEMPLATE", "Reviewer data marker is missing or duplicated.")
    serialized = (
        canonical_json(payload)
        .replace("<", "\\u003c")
        .replace("\u2028", "\\u2028")
        .replace("\u2029", "\\u2029")
    )
    output = harness / "index.html"
    if output.is_symlink():
        raise AgentFlowError("UNSAFE_PATH", "Generated AgentFlow index must not be a symbolic link.")
    output.write_text(template.replace(marker, serialized), encoding="utf-8")
    legacy_output = harness / "review.html"
    if legacy_output.exists() and not legacy_output.is_symlink():
        legacy_output.unlink()
    opened = bool(webbrowser.open(output.resolve().as_uri())) if should_open else False
    return {
        "path": str(output.resolve()),
        "proposalId": proposal_id,
        "opened": opened,
        "commentsWritable": bridge is not None,
    }


def validate_command(harness: Path, proposal_id: str | None) -> dict[str, Any]:
    state = load_state(harness)
    if proposal_id:
        proposal, _ = load_proposal(harness, proposal_id)
        _, candidate = proposal_candidate(harness, proposal)
        return {"valid": True, "proposalId": proposal_id, "pageCount": len(candidate["pages"])}
    manifest, snapshot = load_architecture(harness)
    return {
        "valid": True,
        "architectureHash": manifest["hash"],
        "pageCount": len(snapshot["pages"]),
    }


def status_command(harness: Path) -> dict[str, Any]:
    import_all_comment_inboxes(harness)
    state = load_state(harness)
    manifest, _ = load_architecture(harness)
    legacy_archive = harness / "legacy-v3" / "state.json"
    return {
        "project": read_json(harness / "config.json"),
        "harnessProfile": installed_harness_profile(harness),
        "architectureHash": manifest["hash"],
        "implementedArchitectureHash": state["implementedArchitectureHash"],
        "implementationPending": state["implementedArchitectureHash"] != manifest["hash"],
        "legacyV3Archive": legacy_archive.is_file() and not legacy_archive.is_symlink(),
        "proposals": list_proposals(harness),
        "openComments": len(list_comments(harness, None, "open")),
        "plans": list_plans(harness),
    }


def snapshot_command(harness: Path, state_name: str, page_id: str | None) -> dict[str, Any]:
    state = load_state(harness)
    manifest, snapshot = load_architecture(harness)
    if state_name == "implemented" and state["implementedArchitectureHash"] != manifest["hash"]:
        raise AgentFlowError(
            "IMPLEMENTED_SNAPSHOT_UNAVAILABLE",
            "Implementation is pending; inspect the current approved architecture and product code instead.",
        )
    if page_id:
        page_id = safe_id(page_id, "page id")
        if page_id not in snapshot["pages"]:
            raise AgentFlowError("PAGE_NOT_FOUND", f"Page does not exist: {page_id}")
        return {"architectureHash": manifest["hash"], "page": copy.deepcopy(snapshot["pages"][page_id])}
    return {"architectureHash": manifest["hash"], **snapshot_payload(snapshot)}


def build_comment_target(args: argparse.Namespace) -> dict[str, Any]:
    if args.navigation_id:
        return {"kind": "navigation", "navigationId": args.navigation_id}
    target: dict[str, Any] = {"kind": "page", "pageId": args.page_id}
    if args.element_type or args.element_id:
        target.update({"elementType": args.element_type, "elementId": args.element_id})
    return target


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Manage local AgentFlow architecture files.")
    parser.add_argument("--project", default=".", help="Client project root or child path")
    commands = parser.add_subparsers(dest="command", required=True)

    commands.add_parser("status", help="Show architecture, proposal, and implementation state")
    snapshot = commands.add_parser("snapshot", help="Read current approved architecture")
    snapshot.add_argument("--state", choices=("current", "implemented"), default="current")
    snapshot.add_argument("--page", help="Return one page by id")
    validate = commands.add_parser("validate", help="Validate approved or proposed architecture")
    validate.add_argument("--proposal", help="Proposal id")
    render = commands.add_parser("render", help="Generate the self-contained AgentFlow index")
    render.add_argument("--proposal", help="Proposal to select in the generated page")
    render.add_argument("--open", action="store_true", help="Open the generated page")
    render.add_argument("--comments", action="store_true", help="Start the local comment writer")
    migration = commands.add_parser(
        "migrate", help="Check or apply architecture lifecycle migration"
    )
    migration_mode = migration.add_mutually_exclusive_group(required=True)
    migration_mode.add_argument("--check", action="store_true")
    migration_mode.add_argument("--apply", action="store_true")
    migration.add_argument(
        "--rebuild-from-v3",
        action="store_true",
        help="Rebuild the current architecture from preserved .agentflow/legacy-v3 state.",
    )

    proposal = commands.add_parser("proposal", help="Manage architecture proposals")
    proposal_commands = proposal.add_subparsers(dest="proposal_command", required=True)
    create = proposal_commands.add_parser("create")
    create.add_argument("--input", required=True)
    revise = proposal_commands.add_parser("revise")
    revise.add_argument("proposal_id")
    revise.add_argument("--input", required=True)
    submit = proposal_commands.add_parser("submit")
    submit.add_argument("proposal_id")
    approve = proposal_commands.add_parser("approve")
    approve.add_argument("proposal_id")
    approve.add_argument("--approved-by", required=True)
    approve.add_argument(
        "--implemented-baseline",
        action="store_true",
        help="Use only when the proposal documents code that already exists and was inspected",
    )
    reject = proposal_commands.add_parser("reject")
    reject.add_argument("proposal_id")
    reject.add_argument("--reason", required=True)
    proposal_commands.add_parser("list")

    comment = commands.add_parser("comment", help="Manage architecture review comments")
    comment_commands = comment.add_subparsers(dest="comment_command", required=True)
    add = comment_commands.add_parser("add")
    add.add_argument("proposal_id")
    target = add.add_mutually_exclusive_group(required=True)
    target.add_argument("--page-id")
    target.add_argument("--navigation-id")
    add.add_argument("--element-type", choices=sorted(COMMENT_ELEMENT_TYPES - {"page", "navigation"}))
    add.add_argument("--element-id")
    add.add_argument("--message", required=True)
    edit = comment_commands.add_parser("edit")
    edit.add_argument("proposal_id")
    edit.add_argument("comment_id")
    edit.add_argument("--message", required=True)
    delete = comment_commands.add_parser("delete")
    delete.add_argument("proposal_id")
    delete.add_argument("comment_id")
    resolve = comment_commands.add_parser("resolve")
    resolve.add_argument("proposal_id")
    resolve.add_argument("comment_id")
    resolve.add_argument("--resolution", required=True)
    list_command = comment_commands.add_parser("list")
    list_command.add_argument("proposal_id", nargs="?")
    list_command.add_argument("--status", choices=sorted(COMMENT_STATUSES))
    sync = comment_commands.add_parser("sync")
    sync.add_argument("proposal_id")

    plan = commands.add_parser("plan", help="Validate and register architecture-backed plans")
    plan_commands = plan.add_subparsers(dest="plan_command", required=True)
    plan_validate = plan_commands.add_parser("validate")
    plan_validate.add_argument("--input", required=True)
    plan_register = plan_commands.add_parser("register")
    plan_register.add_argument("--input", required=True)
    plan_commands.add_parser("list")

    implementation = commands.add_parser("implementation", help="Audit plan implementation evidence")
    implementation_commands = implementation.add_subparsers(
        dest="implementation_command", required=True
    )
    implementation_audit = implementation_commands.add_parser("audit")
    implementation_audit.add_argument("--input", required=True)
    implementation_complete = implementation_commands.add_parser("complete")
    implementation_complete.add_argument("--input", required=True)
    return parser


def dispatch(args: argparse.Namespace, project: Path, harness: Path) -> tuple[str, Any]:
    if args.command == "status":
        return "status", status_command(harness)
    if args.command == "snapshot":
        return "snapshot", snapshot_command(harness, args.state, args.page)
    if args.command == "validate":
        return "validate", validate_command(harness, args.proposal)
    if args.command == "render":
        return "render", render_review(harness, args.proposal, args.open, args.comments)
    if args.command == "migrate":
        return "migrate", migrate_v3(harness, args.apply, args.rebuild_from_v3)
    if args.command == "proposal":
        if args.proposal_command == "list":
            import_all_comment_inboxes(harness)
            return "proposal.list", list_proposals(harness)
        if args.proposal_command == "create":
            return "proposal.create", create_proposal(harness, args.input)
        import_comment_inbox(harness, args.proposal_id)
        if args.proposal_command == "revise":
            return "proposal.revise", revise_proposal(harness, args.proposal_id, args.input)
        if args.proposal_command == "submit":
            return "proposal.submit", submit_proposal(harness, args.proposal_id)
        if args.proposal_command == "approve":
            return "proposal.approve", approve_proposal(
                harness,
                args.proposal_id,
                args.approved_by,
                args.implemented_baseline,
            )
        if args.proposal_command == "reject":
            return "proposal.reject", reject_proposal(harness, args.proposal_id, args.reason)
    if args.command == "comment":
        if args.comment_command == "list":
            return "comment.list", list_comments(harness, args.proposal_id, args.status)
        imported = import_comment_inbox(harness, args.proposal_id)
        if args.comment_command == "sync":
            return "comment.sync", {
                "proposalId": args.proposal_id,
                "imported": len(imported),
                "operations": imported,
            }
        if args.comment_command == "add":
            return "comment.add", add_comment(
                harness, args.proposal_id, build_comment_target(args), args.message
            )
        if args.comment_command == "edit":
            return "comment.edit", edit_comment(
                harness, args.proposal_id, args.comment_id, args.message
            )
        if args.comment_command == "delete":
            return "comment.delete", delete_comment(harness, args.proposal_id, args.comment_id)
        if args.comment_command == "resolve":
            return "comment.resolve", resolve_comment(
                harness, args.proposal_id, args.comment_id, args.resolution
            )
    if args.command == "plan":
        if args.plan_command == "list":
            return "plan.list", list_plans(harness)
        if args.plan_command == "register":
            return "plan.register", register_plan(project, harness, args.input)
        manifest, issues, coverage = validate_plan_manifest(
            project, harness, read_json(Path(args.input).expanduser().resolve())
        )
        if issues:
            raise AgentFlowError("PLAN_VALIDATION_FAILED", "AgentFlow plan is invalid.", {"issues": issues})
        return "plan.validate", {**coverage, "architectureHash": manifest["architectureHash"]}
    if args.command == "implementation":
        complete = args.implementation_command == "complete"
        return f"implementation.{args.implementation_command}", audit_implementation(
            project, harness, args.input, complete
        )
    raise AgentFlowError("UNKNOWN_COMMAND", "Unsupported AgentFlow command.")


def main() -> int:
    args = build_parser().parse_args()
    try:
        project, harness = resolve_project(args.project)
        with project_lock(harness):
            command, data = dispatch(args, project, harness)
        print(json.dumps({"ok": True, "command": command, "data": data}, separators=(",", ":")))
        return 0
    except AgentFlowError as error:
        payload: dict[str, Any] = {
            "ok": False,
            "error": {"code": error.code, "message": error.message},
        }
        if error.details is not None:
            payload["error"]["details"] = error.details
        print(json.dumps(payload, separators=(",", ":")), file=sys.stderr)
        return 2
    except (OSError, KeyError, TypeError, ValueError) as error:
        print(
            json.dumps(
                {"ok": False, "error": {"code": "INTERNAL_ERROR", "message": str(error)}},
                separators=(",", ":"),
            ),
            file=sys.stderr,
        )
        return 3


if __name__ == "__main__":
    raise SystemExit(main())
