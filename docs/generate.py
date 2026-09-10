#!/usr/bin/env python3
"""Prepare source-backed Markdown and curated sidebars for Docusaurus.

This is a content adapter, not a Markdown/HTML renderer. Docusaurus owns parsing,
navigation, accessibility, search integration, and strict route/anchor checks.
"""

import json
import os
from pathlib import Path
import re
import subprocess
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]
DOCS = ROOT / "docs"
OUTPUT = DOCS / ".generated"
REPOSITORY = "https://github.com/Newton-School/gogo/blob/"
VERSION = "v1.0.0-alpha.1"
LINK = re.compile(r"\[([^\]]+)\]\(([^)\s]+)\)")


def slug(text):
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-") or "section"


def safe_file(path):
    target = (ROOT / path).resolve()
    if not target.is_relative_to(ROOT) or not target.is_file():
        raise ValueError(f"Missing or out-of-repository source: {path}")
    return target


def source_link(path, revision):
    return REPOSITORY + revision + "/" + path


def outside_fences(source, transform):
    """Never rewrite links or syntax inside executable examples."""
    result, prose, fence = [], [], None
    for line in source.splitlines(keepends=True):
        marker = re.match(r"^\s*(`{3,}|~{3,})", line)
        if marker and fence is None:
            result.append(transform("".join(prose)))
            prose = []
            fence = marker[1]
            result.append(line)
        elif fence is not None:
            result.append(line)
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= len(fence):
                fence = None
        else:
            prose.append(line)
    if fence:
        raise ValueError("Unclosed Markdown fence")
    result.append(transform("".join(prose)))
    return "".join(result)


def rewrite_links(source, source_path, page_sources, revision, page_ids):
    def rewrite(match):
        target = match[2]
        parts = urlsplit(target)
        if parts.scheme not in ("", "https", "http", "mailto") or target.startswith("//"):
            raise ValueError("Unsafe documentation link: " + target)
        if parts.scheme or target.startswith("#"):
            return match[0]
        # Existing feature guides use stable page IDs; package notes use paths.
        identifier = parts.path.removesuffix(".md")
        if source_path.startswith("docs/") and identifier in page_ids:
            destination = identifier + ".md"
        else:
            normalized = os.path.normpath(str(Path(source_path).parent / parts.path))
            if normalized in page_sources:
                destination = page_sources[normalized] + ".md"
            else:
                safe_file(normalized) if Path(normalized).suffix else None
                destination = source_link(normalized, revision)
        if parts.query:
            destination += "?" + parts.query
        if parts.fragment:
            destination += "#" + parts.fragment
        return f"[{match[1]}]({destination})"
    return outside_fences(source, lambda prose: LINK.sub(rewrite, prose))


def expand(source, revision, page_sources, page_ids):
    def include(match):
        path = match[2]
        content = safe_file(path).read_text()
        if match[1] == "code":
            language = "go" if path.endswith(".go") else "text"
            # Pick a fence longer than any backtick run in the source.
            width = max([2] + [len(m[0]) for m in re.finditer(r"`+", content)]) + 1
            fence = "`" * width
            return f"[Example source]({source_link(path, revision)})\n\n{fence}{language}\n{content.rstrip()}\n{fence}"
        content = rewrite_links(content, path, page_sources, revision, page_ids)
        return re.sub(r"^(#{1,5}) ", r"#\1 ", content, flags=re.M)
    return outside_fences(source, lambda prose: re.sub(r"\{\{(code|include) ([A-Za-z0-9_./-]+)\}\}", include, prose))


def api_page(package, guide_id, revision):
    title = package["Directory"] if package["Directory"] != "." else "gogo"
    lines = [f"# {title}", "", f"```go\nimport \"{package['Path']}\"\n```", "",
             f"[Read the feature guide]({guide_id}.md)", "", package["Doc"].strip(), "",
             "> Generated from public Go declarations. A symbol's presence does not guarantee support for every backend or feature combination. Read the feature guide and [alpha limits](compatibility.md).", ""]
    declarations = package["Declarations"]
    for kind, label in [("type", "Types"), ("function", "Functions"), ("constant", "Constants"), ("variable", "Variables")]:
        selected = [d for d in declarations if d["Kind"] == kind]
        if not selected:
            continue
        lines += ["## " + label, ""]
        if kind in ("type", "function"):
            lines += [" · ".join(f"[{d['Name']}](#{slug(d['Name'])})" for d in selected), ""]
        for declaration in selected:
            name = declaration["Name"]
            # Constants may be one declaration containing dozens of names.
            heading = name.split(", ")[0] + (" and related values" if ", " in name else "")
            lines += [f"### {heading} {{#{slug(name)}}}", "", declaration["Doc"].strip(), "", "```go", declaration["Signature"], "```", "", f"[Source]({source_link(declaration['File'], revision)}#L{declaration['Line']})", ""]
            if kind == "type":
                for method in [d for d in declarations if d["Kind"] == "method" and d["Name"].startswith(name + ".")]:
                    lines += [f"#### {method['Name']} {{#{slug(method['Name'])}}}", "", method["Doc"].strip(), "", "```go", method["Signature"], "```", "", f"[Source]({source_link(method['File'], revision)}#L{method['Line']})", ""]
    return "\n".join(lines)


def settings_page(settings):
    lines = ["# Settings reference", "", "Generated from `conf.CoreSchema()`. Empty defaults are **unset**. Required means required when that resource is selected, not for every application. A variable does not register its service. See [Configuration](configuration.md).", ""]
    kinds = ["string", "boolean", "integer", "duration", "list", "URL"]
    for group in dict.fromkeys(item["Group"] for item in settings):
        lines += ["## " + group, "", "| Variable | Type | Default | Required for | Constraints |", "| --- | --- | --- | --- | --- |"]
        for item in [s for s in settings if s["Group"] == group]:
            default = "**unset**" if not item["Default"] else "`" + item["Default"] + "`"
            constraints = ["Secret; redacted"] if item["Sensitive"] else []
            if item["Min"]:
                constraints.append("Minimum " + str(item["Min"]) + (" ns" if kinds[item["Kind"]] == "duration" else ""))
            constraints.extend(item["Choices"] or [])
            lines.append(f"| `{item['Name']}` | {kinds[item['Kind']]} | {default} | {', '.join(item['RequiredFor'] or []) or 'Optional'} | {'; '.join(constraints) or '—'} |")
        lines.append("")
    return "\n".join(lines)


def compile_tree(tree, pages):
    """Assign each document exactly once; explicit note placement wins over auto nesting."""
    used, categories = set(), set()
    explicit = set()

    def collect(items):
        for item in items:
            if isinstance(item, str):
                explicit.add(item)
            else:
                explicit.update(item.get("notes", []))
                collect(item.get("items", []))
    for items in tree.values():
        collect(items)

    def document(identifier):
        if identifier not in pages:
            raise ValueError("Unknown tree page: " + identifier)
        if identifier in used:
            raise ValueError("Duplicate tree page: " + identifier)
        used.add(identifier)
        return {"type": "doc", "id": identifier, "label": pages[identifier]["title"]}

    def convert(item):
        if isinstance(item, str):
            item = {"guide": item}
        if "guide" in item:
            identifier = item["guide"]
            leaf = document(identifier)
            leaf["label"] = item.get("label", leaf["label"])
            notes = item.get("notes", [p["id"] for p in pages.values() if p.get("parent") == identifier and p["id"] not in explicit])
            for note in notes:
                if note in pages:
                    pages[note]["parent"] = identifier
            children = [convert(child) for child in item.get("items", [])] + [document(note) for note in notes]
            if not children:
                return leaf
            return {"type": "category", "label": leaf["label"], "description": pages[identifier].get("description", "Explore " + leaf["label"].lower() + "."), "link": {"type": "doc", "id": identifier}, "collapsed": True, "items": children}
        category = item["id"]
        if category in categories:
            raise ValueError("Duplicate category: " + category)
        categories.add(category)
        return {"type": "category", "label": item["label"], "description": item["description"], "collapsed": True,
                "link": {"type": "generated-index", "title": item["label"], "description": item["description"], "slug": "/category/" + category},
                "items": [convert(child) for child in item["items"]]}

    sidebars = {key: [convert(item) for item in items] for key, items in tree.items()}
    missing = set(pages) - used
    if missing:
        raise ValueError("Pages missing from navigation tree: " + ", ".join(sorted(missing)))
    return sidebars


def collect_pages(manifest, catalog, revision):
    pages, owners = {}, {}

    def add(page):
        identifier = page["id"]
        if identifier in pages or not re.fullmatch(r"[a-z0-9-]+", identifier):
            raise ValueError("Duplicate or unsafe page identifier: " + identifier)
        pages[identifier] = page

    for entry in manifest:
        add(dict(entry, source=safe_file(entry["file"]).read_text()))
        for package in entry.get("packages", []):
            if package in owners:
                raise ValueError("Duplicate guide owner: " + package)
            owners[package] = entry["id"]
    for parent in ("core", "admin", "async", "connectors"):
        for path in sorted((ROOT / parent).rglob("*.md")):
            relative = path.relative_to(ROOT)
            if "internal" in relative.parts or "testdata" in relative.parts:
                continue
            owner = relative.parent
            while str(owner) not in owners and owner != Path("."):
                owner = owner.parent
            if str(owner) not in owners:
                raise ValueError("Technical guide has no package owner: " + str(relative))
            text = path.read_text()
            title = re.search(r"^# (.+)$", text, re.M)
            add({"id": "detail-" + slug(str(relative.with_suffix(""))), "title": title[1].replace("`", "") if title else relative.stem,
                 "parent": owners[str(owner)], "file": str(relative), "source": text})
    vocabulary = ["# Template tags and filters", "", "Generated from a default `templates.Engine.Describe(4096)`. These are registered names, not a claim of complete Django parity. See [Templates](templates.md) for usage.", "", "## Built-in tags", ""]
    vocabulary.extend("- `" + name + "`" for name in catalog["Templates"]["Tags"])
    vocabulary += ["", "## Built-in filters", ""]
    vocabulary.extend("- `" + name + "`" for name in catalog["Templates"]["Filters"])
    add({"id": "template-vocabulary", "title": "Template tags and filters", "source": "\n".join(vocabulary)})
    add({"id": "settings", "title": "Environment settings", "source": settings_page(catalog["Settings"])})
    inventory = ["# Go API reference", "", "Look up exact types, fields, interfaces, functions, and methods. References are generated from the current public Go source; the sidebar groups packages by feature. Start with the linked guide if you are learning a feature for the first time.", "", "Command usage is in [Management commands](commands.md). Environment variables have a separate [settings reference](settings.md).", ""]
    actual, references = set(), {}
    for package in catalog["Packages"]:
        directory = package["Directory"]
        actual.add(directory)
        if directory not in owners:
            raise ValueError("Public package needs a feature guide: " + directory)
        owner = pages[owners[directory]]
        identifier = "api-" + slug(directory if directory != "." else "gogo")
        title = directory if directory != "." else "gogo"
        add({"id": identifier, "title": title, "source": api_page(package, owner["id"], revision), "api": True})
        owner.setdefault("references", []).append(identifier)
        references.setdefault(owner["section"], []).append(identifier)
    if set(owners) != actual:
        raise ValueError("Guide maps nonexistent public packages: " + str(sorted(set(owners) - actual)))
    # Keep reference families in the same learning order as the human guides,
    # not the incidental order in which alphabetical package paths were scanned.
    references = {section: references[section] for section in dict.fromkeys(entry["section"] for entry in manifest) if section in references}
    for section, identifiers in references.items():
        inventory += ["## " + section, "", "| Package | Learn it |", "| --- | --- |"]
        for identifier in identifiers:
            page = pages[identifier]
            owner = owners[page["title"] if page["title"] != "gogo" else "."]
            inventory.append(f"| [{page['title']}]({identifier}.md) | [{pages[owner]['title']}]({owner}.md) |")
        inventory.append("")
    add({"id": "packages", "title": "Go API overview", "source": "\n".join(inventory)})
    return pages, references


def generate():
    manifest = json.loads((DOCS / "navigation.json").read_text())
    catalog = json.loads(subprocess.check_output(["go", "run", "./docs/tools/catalog"], cwd=ROOT, text=True))
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    pages, references = collect_pages(manifest, catalog, revision)
    tree = json.loads((DOCS / "tree.json").read_text())
    tree["referenceSidebar"] = ["packages", "settings", "template-vocabulary"] + [
        {"label": section, "id": "reference-" + slug(section), "description": "Public Go API declarations for " + section.lower() + ".", "items": ids}
        for section, ids in references.items()]
    sidebars = compile_tree(tree, pages)
    page_sources = {p["file"]: p["id"] for p in pages.values() if "file" in p}
    content = OUTPUT / "content"
    content.mkdir(parents=True, exist_ok=True)
    # Remove only obsolete generated Markdown, never caller-selected directories.
    for path in content.glob("*.md"):
        if path.stem not in pages:
            path.unlink()
    for page in pages.values():
        body = page["source"]
        if "file" in page:
            body = rewrite_links(body, page["file"], page_sources, revision, pages)
        body = expand(body, revision, page_sources, pages)
        related = [p for p in pages.values() if p.get("parent") == page["id"]]
        if related:
            body += "\n\n## In this feature\n\n" + "\n".join(f"- [{p['title']}]({p['id']}.md)" for p in related)
        if page.get("references"):
            body += "\n\n## Go API reference\n\n" + "\n".join(f"- [{pages[identifier]['title']}]({identifier}.md)" for identifier in page["references"])
        frontmatter = {"title": page["title"], "slug": "/" + page["id"], "pagination_label": page["title"]}
        if page.get("description"):
            frontmatter["description"] = page["description"]
        if page.get("api"):
            frontmatter["toc_max_heading_level"] = 2
        if page.get("file"):
            frontmatter["custom_edit_url"] = source_link(page["file"], revision)
        # JSON string values are valid YAML and safely quote metadata.
        header = "\n".join(key + ": " + json.dumps(value) for key, value in frontmatter.items())
        (content / (page["id"] + ".md")).write_text("---\n" + header + "\n---\n\n" + body.rstrip() + "\n")
    (OUTPUT / "sidebars.json").write_text(json.dumps(sidebars, indent=2) + "\n")
    summary = {"version": VERSION, "sourceRevision": revision, "pages": len(pages), "featureGuides": len(manifest),
               "technicalGuides": sum("parent" in p for p in pages.values()), "publicPackages": len(catalog["Packages"]),
               "declarations": sum(len(p["Declarations"]) for p in catalog["Packages"]), "settings": len(catalog["Settings"])}
    (OUTPUT / "coverage.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    generate()
