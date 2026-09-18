"""Extract small, named examples from code that the documentation tests execute."""

import re
import textwrap


def extract(source, name):
    marker = re.compile(r"^\s*// docs:(begin|end) ([a-z0-9-]+)\s*$")
    snippets, active, lines = {}, None, []
    for line in source.splitlines():
        match = marker.match(line)
        if match:
            action, key = match.groups()
            if action == "begin":
                if active or key in snippets:
                    raise ValueError("Nested or duplicate code example: " + key)
                active, lines = key, []
            else:
                if active != key:
                    raise ValueError("Unmatched code example: " + key)
                snippet = textwrap.dedent("\n".join(lines)).strip()
                if not snippet or len(snippet.splitlines()) > 20:
                    raise ValueError("Code examples must contain 1–20 lines: " + key)
                snippets[key], active = snippet, None
        elif active:
            lines.append(line)
    if active:
        raise ValueError("Unclosed code example: " + active)
    if name not in snippets:
        raise ValueError("Unknown code example: " + name)
    return snippets[name]


def block(source):
    return "```go\n" + source + "\n```"


def without_markers(source):
    return re.sub(r"^[ \t]*// docs:(?:begin|end) [a-z0-9-]+\s*\n", "", source, flags=re.M)


def render(name, entry, safe_file):
    source = extract(safe_file(entry["file"]).read_text(), name)
    return entry["intro"] + "\n\n" + block(source)


def usage(entry, examples, safe_file):
    """Every configuration page must teach usage, not just repeat Go types."""
    if not entry.get("usage"):
        raise ValueError("Missing configuration usage examples: " + entry["title"])
    lines = ["## Usage", ""]
    seen = set()
    for item in entry["usage"]:
        name, title = item["example"], item["title"]
        if name not in examples or not title.strip() or title in seen:
            raise ValueError("Unknown example or duplicate usage heading: " + name)
        seen.add(title)
        lines += ["### " + title, "", render(name, examples[name], safe_file), ""]
    if entry.get("usageNotes"):
        lines += [entry["usageNotes"], ""]
    return lines


def load(entries, catalog, safe_file):
    """Reject stale symbol/member mappings and oversized or missing excerpts."""
    declarations = {p["Directory"] + "." + d["Name"]: d
                    for p in catalog["Packages"] for d in p["Declarations"]}
    symbols, members = {}, {}
    for name, entry in entries.items():
        rendered = render(name, entry, safe_file)
        for key in entry.get("symbols", []):
            if key not in declarations or key in symbols:
                raise ValueError("Unknown or duplicate example symbol: " + key)
            symbols[key] = rendered
        for key, fields in entry.get("options", {}).items():
            actual = {m["Name"] for m in declarations.get(key, {}).get("Members", [])}
            for field in fields:
                target = key + "." + field
                if field not in actual or target in members:
                    raise ValueError("Unknown or duplicate example option: " + target)
                members[target] = rendered
    return symbols, members


def brief_contract(text):
    """Preserve every contract while moving lengthy prose behind a disclosure."""
    text = text.strip()
    if len(text.split()) <= 65:
        return [text, ""] if text else []
    first = re.split(r"(?<=[.!?])\s+", text, maxsplit=1)[0]
    # Do not manufacture a summary when the first sentence is itself long.
    introduction = [first, ""] if len(first.split()) <= 40 else []
    return [*introduction, '<details className="reference-contract">',
            "<summary>Behavior, constraints and errors</summary>", "", text, "", "</details>", ""]
