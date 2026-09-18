#!/usr/bin/env python3
"""Prepare source-backed Markdown and curated sidebars for Docusaurus.

This is a content adapter, not a Markdown/HTML renderer. Docusaurus owns parsing,
navigation, accessibility, search integration, and strict route/anchor checks.
"""

import json
import html
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
            if path.endswith((".go", ".go.txt")):
                language = "go"
            elif path.endswith((".yaml", ".yml")):
                language = "yaml"
            elif Path(path).name == "Dockerfile":
                language = "dockerfile"
            else:
                language = "text"
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


def settings_page(settings, descriptions=None):
    if descriptions is None:
        descriptions = json.loads((DOCS / "settings-descriptions.json").read_text())
    names = {item["Name"] for item in settings}
    missing, unknown = names - descriptions.keys(), descriptions.keys() - names
    if missing or unknown:
        raise ValueError(f"Settings description coverage: missing {sorted(missing)}; unknown {sorted(unknown)}")
    for name, description in descriptions.items():
        if not isinstance(description, str) or not description.strip():
            raise ValueError("Empty or invalid settings description: " + name)
    lines = ["# Settings reference", "", "Defaults, types and requirements are generated from `conf.CoreSchema()`; each setting's purpose is described below. Empty defaults are **unset**. Required means required when that resource is selected, not for every application.", "",
             "**Wiring matters:** declaring a variable does not register a service or automatically apply it to every constructor. Pass the resolved value to the corresponding service or middleware in your project. Reserved settings and built-in limitations are identified below. See [Configuration](configuration.md).", ""]
    kinds = ["string", "boolean", "integer", "duration", "list", "URL"]
    for group in dict.fromkeys(item["Group"] for item in settings):
        lines += ["## " + group, "", "| Variable | Description | Default | Required for | Type and constraints |", "| --- | --- | --- | --- | --- |"]
        for item in [s for s in settings if s["Group"] == group]:
            default = "**unset**" if not item["Default"] else "`" + item["Default"] + "`"
            constraints = ["Secret; redacted"] if item["Sensitive"] else []
            if item["Min"]:
                constraints.append("Minimum " + str(item["Min"]) + (" ns" if kinds[item["Kind"]] == "duration" else ""))
            constraints.extend(item["Choices"] or [])
            description = " ".join(descriptions[item["Name"]].split()).replace("|", "\\|")
            details = "; ".join([kinds[item["Kind"]]] + constraints)
            lines.append(f"| `{item['Name']}` | {description} | {default} | {', '.join(item['RequiredFor'] or []) or 'Optional'} | {details} |")
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
        result = {"type": "category", "label": item["label"], "collapsed": False,
                  "items": [convert(child) for child in item["items"]]}
        if item.get("description"):
            result.update(description=item["description"], link={"type": "generated-index", "title": item["label"], "description": item["description"], "slug": "/category/" + category})
        return result

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


def consolidate_pages(pages, sections, revision):
    """Compose feature pages without discarding source contracts or declarations."""
    destinations, groups = {}, {}
    for section in sections:
        identifier = section["id"]
        if identifier in groups or not re.fullmatch(r"[a-z0-9-]+", identifier):
            raise ValueError("Duplicate or unsafe feature page: " + identifier)
        groups[identifier] = dict(section, parts=[], details=[], references=[])
        for source, title in section["sources"]:
            if source not in pages or source in destinations:
                raise ValueError("Unknown or duplicate consolidated source: " + source)
            destinations[source] = identifier
            groups[identifier]["parts"].append((source, title))
        for kind in ("details", "references"):
            for source in section.get(kind, []):
                if source not in pages or source in destinations:
                    raise ValueError("Unknown or duplicate consolidated source: " + source)
                if kind == "references" and not pages[source].get("api") or kind == "details" and not pages[source].get("parent"):
                    raise ValueError("Invalid consolidated source kind: " + source)
                destinations[source] = identifier
                groups[identifier][kind].append((source, pages[source]["title"]))
    # Explicit placement (for example Admin Accounts) wins over package ownership.
    for source, page in pages.items():
        parent = page.get("parent")
        if parent and source not in destinations:
            if parent not in destinations:
                raise ValueError("Missing consolidated parent: " + parent)
            target = destinations[parent]
            destinations[source] = target
            groups[target]["details"].append((source, page["title"]))
        for reference in page.get("references", []):
            if reference in destinations:
                continue
            if source not in destinations:
                raise ValueError("Invalid consolidated API owner: " + source)
            target = destinations[source]
            destinations[reference] = target
            groups[target]["references"].append((reference, pages[reference]["title"]))
    if set(pages) != set(destinations):
        raise ValueError("Unmapped consolidated sources: " + str(sorted(set(pages) - set(destinations))))
    page_sources = {p["file"]: p["id"] for p in pages.values() if "file" in p}
    prepared, anchors = {}, {}
    heading = re.compile(r"^(#{1,6}) (.+)$", re.M)
    for source, page in pages.items():
        body = page["source"]
        source_path = page.get("file", "docs/" + source + ".md")
        body = rewrite_links(body, source_path, page_sources, revision, pages)
        body = expand(body, revision, page_sources, pages)
        seen, mapping = {}, {}

        def stamp(match):
            level, title = len(match[1]), match[2]
            explicit = re.search(r"\s+\{#([^}]+)\}$", title)
            plain = re.sub(r"\[([^\]]+)\]\([^)]*\)", r"\1", title)
            natural = slug(re.sub(r"[`*_]", "", plain))
            original = explicit[1] if explicit else natural
            count = seen.get(original, 0)
            seen[original] = count + 1
            if count:
                original += "-" + str(count)
            anchor = source if level == 1 else source + "-" + original
            mapping[original] = anchor
            label = title[:explicit.start()] if explicit else title
            return match[1] + " " + label + " {#" + anchor + "}"

        prepared[source] = outside_fences(body, lambda text: heading.sub(stamp, text))
        mapping[""] = source
        mapping["in-this-feature"] = "details" if groups[destinations[source]]["details"] else source
        mapping["go-api-reference"] = "api-reference" if groups[destinations[source]]["references"] else source
        anchors[source] = mapping

    def destination(source, fragment=""):
        return destinations[source] + ".md#" + anchors[source].get(fragment, source + "-" + fragment)

    result = {}
    for identifier, group in groups.items():
        single_source = group["parts"][0][0] if len(group["parts"]) == 1 else None

        def render(source, title, folded=False):
            body = prepared[source]

            def relink(match):
                parts = urlsplit(match[2])
                if parts.scheme or parts.netloc:
                    return match[0]
                target = parts.path.removesuffix(".md") if parts.path else source
                if target not in destinations:
                    return match[0]
                return "[" + match[1] + "](" + destination(target, parts.fragment) + ")"

            body = outside_fences(body, lambda text: LINK.sub(relink, text))

            def nest(match):
                level = len(match[1])
                if level == 1:
                    return "" if folded or source == single_source else "## " + title + " {#" + source + "}"
                if source == single_source and not folded:
                    return match[0]
                return "#" * min(6, level + (2 if folded else 1)) + " " + match[2]

            body = outside_fences(body, lambda text: heading.sub(nest, text)).strip()
            if folded:
                return '<details>\n<summary>' + html.escape(title) + '</summary>\n\n#### ' + title + ' {#' + source + '}\n\n' + body + '\n\n</details>'
            return body

        body = ["# " + group["title"] + " {#" + (single_source or "page-" + identifier) + "}"]
        body.extend(render(source, title) for source, title in group["parts"])
        if group["details"]:
            body += ["## Details {#details}", "Expand a feature for its full behavior, constraints and failure cases."]
            body.extend(render(source, title, True) for source, title in group["details"])
        if group["references"]:
            body += ["## API reference {#api-reference}", "Public Go declarations for this feature. Expand a package or use search to find a symbol."]
            body.extend(render(source, title, True) for source, title in group["references"])
        result[identifier] = {"id": identifier, "title": group["title"], "source": "\n\n".join(body),
                              "description": group["title"] + " features, examples and API reference."}
    routes = {source: {"page": destinations[source], "anchor": source, "anchors": anchors[source]} for source in pages}
    return result, routes


def generate():
    manifest = json.loads((DOCS / "navigation.json").read_text())
    catalog = json.loads(subprocess.check_output(["go", "run", "./docs/tools/catalog"], cwd=ROOT, text=True))
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    source_pages, _ = collect_pages(manifest, catalog, revision)
    sections = json.loads((DOCS / "sections.json").read_text())
    pages, routes = consolidate_pages(source_pages, sections, revision)
    tree = json.loads((DOCS / "tree.json").read_text())
    sidebars = compile_tree(tree, pages)
    content = OUTPUT / "content"
    content.mkdir(parents=True, exist_ok=True)
    # Remove only obsolete generated Markdown, never caller-selected directories.
    for path in content.glob("*.md"):
        if path.stem not in pages:
            path.unlink()
    for page in pages.values():
        body = page["source"]
        frontmatter = {"title": page["title"], "slug": "/" + page["id"], "pagination_label": page["title"]}
        if page.get("description"):
            frontmatter["description"] = page["description"]
        # JSON string values are valid YAML and safely quote metadata.
        header = "\n".join(key + ": " + json.dumps(value) for key, value in frontmatter.items())
        (content / (page["id"] + ".md")).write_text("---\n" + header + "\n---\n\n" + body.rstrip() + "\n")
    (OUTPUT / "sidebars.json").write_text(json.dumps(sidebars, indent=2) + "\n")
    (OUTPUT / "routes.json").write_text(json.dumps(routes, indent=2) + "\n")
    summary = {"version": VERSION, "sourceRevision": revision, "pages": len(pages), "sourceDocuments": len(source_pages), "featureGuides": len(manifest),
               "technicalGuides": sum("parent" in p for p in source_pages.values()), "publicPackages": len(catalog["Packages"]),
               "declarations": sum(len(p["Declarations"]) for p in catalog["Packages"]), "settings": len(catalog["Settings"])}
    (OUTPUT / "coverage.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    generate()
