#!/usr/bin/env python3
"""Build Gogo's offline documentation using only Python and Go standard libraries.

The deliberately small Markdown dialect supports headings, fenced code, tables,
paragraphs, flat lists, links, inline code, emphasis and block quotes. Raw HTML
is escaped. Unsupported source constructs are not silently executable markup.
"""

import argparse
import html
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
DOCS = ROOT / "docs"
OUTPUT = DOCS / "_site"
REPOSITORY = "https://github.com/Newton-School/gogo/blob/"
VERSION = "v1.0.0-alpha.1"


def slug(text):
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-") or "section"


def safe_file(path):
    target = (ROOT / path).resolve()
    if not target.is_relative_to(ROOT) or not target.is_file():
        raise ValueError(f"Missing or out-of-repository source: {path}")
    return target


def inline(text, link=lambda value: value):
    tokens = []

    def stash(value):
        tokens.append(value)
        return f"\x00{len(tokens)-1}\x00"

    text = re.sub(r"`([^`]+)`", lambda m: stash("<code>" + html.escape(m[1]) + "</code>"), text)

    def anchor(match):
        destination = link(match[2])
        parts = urlsplit(destination)
        if parts.scheme not in ("", "https", "http", "mailto") or destination.startswith("//"):
            raise ValueError(f"Unsafe documentation link: {destination}")
        # Inline code in the label was already stashed by this same invocation.
        # Keep its token in this scope rather than recursing with an empty stash.
        return stash(f'<a href="{html.escape(destination, quote=True)}">{html.escape(match[1])}</a>')

    text = re.sub(r"\[([^\]]+)\]\(([^)\s]+)\)", anchor, text)
    text = html.escape(text)
    text = re.sub(r"\*\*([^*]+)\*\*", r"<strong>\1</strong>", text)
    text = re.sub(r"(?<!\*)\*([^*]+)\*(?!\*)", r"<em>\1</em>", text)
    # Stashed links can contain an inline-code stash from the first pass.
    for _ in range(3):
        text = re.sub(r"\x00(\d+)\x00", lambda m: tokens[int(m[1])], text)
    return text


def markdown(source, link=lambda value: value):
    lines = source.splitlines()
    out, toc = [], []
    # Template control IDs must not collide with an API symbol such as Content.
    used = dict.fromkeys(("content", "navigation", "search-dialog", "search-title", "search-input", "search-results", "search-status", "search-open", "search-close", "theme", "menu", "announcement"), 1)
    i = 0
    while i < len(lines):
        line = lines[i]
        if not line.strip():
            i += 1
            continue
        if line.startswith("```"):
            language = line[3:].strip() or "text"
            block = []
            i += 1
            while i < len(lines) and not lines[i].startswith("```"):
                block.append(lines[i])
                i += 1
            if i == len(lines):
                raise ValueError("Unclosed code fence")
            out.append(f'<div class="code-block"><div class="code-label">{html.escape(language)}<button class="copy" type="button">Copy</button></div><pre><code>{html.escape(chr(10).join(block))}</code></pre></div>')
            i += 1
            continue
        heading = re.match(r"^(#{1,6}) (.+)$", line)
        if heading:
            level, title = len(heading[1]), heading[2]
            base = slug(title.replace("`", ""))
            count = used.get(base, 0)
            used[base] = count + 1
            identifier = base + (f"-{count}" if count else "")
            out.append(f'<h{level} id="{identifier}">{inline(title, link)}<a class="heading-anchor" href="#{identifier}" aria-label="Link to this section">#</a></h{level}>')
            if level in (2, 3):
                toc.append((level, title.replace("`", ""), identifier))
            i += 1
            continue
        if line.startswith("|") and i + 1 < len(lines) and re.match(r"^\|[\s:|\-]+\|$", lines[i+1]):
            headers = table_cells(line)
            out.append('<div class="table-wrap"><table><thead><tr>' + "".join(f"<th scope=\"col\">{inline(c, link)}</th>" for c in headers) + "</tr></thead><tbody>")
            i += 2
            while i < len(lines) and lines[i].startswith("|"):
                cells = table_cells(lines[i])
                if len(cells) != len(headers):
                    raise ValueError(f"Table has {len(cells)} cells, expected {len(headers)}: {lines[i]}")
                out.append("<tr>" + "".join(f"<td>{inline(c, link)}</td>" for c in cells) + "</tr>")
                i += 1
            out.append("</tbody></table></div>")
            continue
        if line.startswith(">"):
            block = []
            while i < len(lines) and lines[i].startswith(">"):
                block.append(lines[i].lstrip("> "))
                i += 1
            out.append('<aside class="callout">' + inline(" ".join(block), link) + "</aside>")
            continue
        match = re.match(r"^(?:[-*] |\d+\. )(.+)$", line)
        if match:
            tag = "ol" if line[0].isdigit() else "ul"
            out.append(f"<{tag}>")
            while i < len(lines):
                match = re.match(r"^(?:[-*] |\d+\. )(.+)$", lines[i])
                if not match:
                    break
                item = match[1]
                i += 1
                while i < len(lines) and lines[i].startswith("  ") and lines[i].strip():
                    item += " " + lines[i].strip()
                    i += 1
                out.append("<li>" + inline(item, link) + "</li>")
                if i + 1 < len(lines) and not lines[i].strip() and re.match(r"^(?:[-*] |\d+\. )", lines[i+1]):
                    i += 1
            out.append(f"</{tag}>")
            continue
        if line == "---":
            out.append("<hr>")
            i += 1
            continue
        paragraph = [line]
        i += 1
        while i < len(lines) and lines[i].strip() and not re.match(r"^(#|```|\||>|[-*] |\d+\. )", lines[i]):
            paragraph.append(lines[i])
            i += 1
        out.append("<p>" + inline(" ".join(paragraph), link) + "</p>")
    return "\n".join(out), toc


def table_cells(line):
    """Keep pipes inside inline code and escaped GFM pipes within their cell."""
    cells, current, code, escaped = [], [], False, False
    for character in line.strip().strip("|"):
        if escaped:
            current.append(character)
            escaped = False
        elif character == "\\":
            escaped = True
        elif character == "`":
            code = not code
            current.append(character)
        elif character == "|" and not code:
            cells.append("".join(current).strip())
            current = []
        else:
            current.append(character)
    cells.append("".join(current).strip())
    return cells


def source_link(path, revision):
    return REPOSITORY + revision + "/" + path


def expand(source, revision):
    def include(match):
        path = match[2]
        content = safe_file(path).read_text()
        if match[1] == "code":
            language = "go" if path.endswith(".go") else "text"
            return f"[Example source]({source_link(path, revision)})\n\n```{language}\n{content.rstrip()}\n```"
        # Existing package guides remain the single source of truth. Convert
        # their relative links to repository links before moving their content.
        def rewrite(m):
            if urlsplit(m[2]).scheme or m[2].startswith("#"):
                return m[0]
            target = (Path(path).parent / m[2]).as_posix()
            normalized = os.path.normpath(target)
            return f"[{m[1]}]({source_link(normalized, revision)})"
        content = re.sub(r"\[([^\]]+)\]\(([^)\s]+)\)", rewrite, content)
        # A source document is a section within the owning feature guide.
        content = re.sub(r"^(#{1,5}) ", r"#\1 ", content, flags=re.M)
        return f"[Detailed guide source]({source_link(path, revision)})\n\n{content}"
    return re.sub(r"\{\{(code|include) ([A-Za-z0-9_./-]+)\}\}", include, source)


def api_page(package, guide_id, revision):
    path = package["Path"]
    title = package["Directory"] if package["Directory"] != "." else "gogo"
    lines = [f"# {title}", "", f"`import \"{path}\"`", "", f"[Read the feature guide]({guide_id}.md)", "", package["Doc"].strip(), "", "> This reference is extracted from public Go declarations, including exported options and methods. A symbol's presence is not a guarantee that every backend or feature combination supports it. Read the feature guide and alpha boundaries.", ""]
    for declaration in package["Declarations"]:
        lines.extend(["## " + declaration["Name"], "", declaration["Kind"].capitalize(), "", declaration["Doc"].strip(), "", "```go", declaration["Signature"], "```", "", f"[Source]({source_link(declaration['File'], revision)}#L{declaration['Line']})", ""])
    return "\n".join(lines)


def settings_page(settings):
    lines = ["# Settings reference", "", "Generated from `conf.CoreSchema()`. Empty defaults are shown as **unset**. Required means required when the corresponding resource is selected, not for every application. Setting a variable does not register its service. See [Configuration](configuration.md).", ""]
    groups = dict.fromkeys(item["Group"] for item in settings)
    kinds = ["string", "boolean", "integer", "duration", "list", "URL"]
    for group in groups:
        lines += ["## " + group, "", "| Variable | Type | Default | Required for | Constraints |", "| --- | --- | --- | --- | --- |"]
        for item in settings:
            if item["Group"] != group:
                continue
            default = "**unset**" if not item["Default"] else "`" + item["Default"] + "`"
            requirements = ", ".join(item["RequiredFor"] or []) or "Optional"
            constraints = []
            if item["Sensitive"]:
                constraints.append("Secret; redacted")
            if item["Min"]:
                constraints.append("Minimum " + str(item["Min"]))
            if item["Choices"]:
                constraints.append(", ".join(item["Choices"]))
            lines.append(f"| `{item['Name']}` | {kinds[item['Kind']]} | {default} | {requirements} | {'; '.join(constraints) or '—'} |")
        lines.append("")
    return "\n".join(lines)


def navigation(pages, active):
    primary = [p for p in pages if not p.get("parent")]
    groups = dict.fromkeys(p["section"] for p in primary)
    out = []
    for group in groups:
        out.append(f'<section class="nav-group"><h2>{html.escape(group)}</h2>')
        for page in primary:
            if page["section"] == group:
                current = ' aria-current="page"' if page["id"] == active else ""
                out.append(f'<a href="{page["id"]}.html"{current}>{html.escape(page["title"])}</a>')
                children = [p for p in pages if p.get("parent") == page["id"]]
                if children:
                    opened = ' open' if active == page["id"] or any(p["id"] == active for p in children) else ''
                    out.append(f'<details{opened}><summary>Technical guides</summary>')
                    for child in children:
                        selected = ' aria-current="page"' if child["id"] == active else ''
                        out.append(f'<a href="{child["id"]}.html"{selected}>{html.escape(child["title"])}</a>')
                    out.append('</details>')
        out.append("</section>")
    return "".join(out)


def render_page(page, pages, body, toc, revision):
    on_page = "".join(f'<a class="level-{level}" href="#{identifier}">{html.escape(title)}</a>' for level, title, identifier in toc)
    position = pages.index(page)
    adjacent = []
    for offset, label in [(-1, "Previous"), (1, "Next")]:
        if 0 <= position + offset < len(pages):
            target = pages[position + offset]
            adjacent.append(f'<a href="{target["id"]}.html"><span>{label}</span>{html.escape(target["title"])}</a>')
    return f'''<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{html.escape(page['title'])} · Gogo documentation</title><meta name="description" content="Gogo {VERSION}: developer guides and public Go API reference.">
<link rel="stylesheet" href="assets/style.css"><script src="assets/search-index.js" defer></script><script src="assets/site.js" defer></script></head>
<body><a class="skip" href="#content">Skip to content</a><header class="topbar"><a class="brand" href="index.html"><span class="mark" aria-hidden="true">g</span>gogo<span class="brand-docs">/ docs</span></a><span class="version">{VERSION}</span><div class="header-actions"><button id="search-open" type="button">Search docs <kbd>/</kbd></button><button id="theme" type="button" aria-label="Toggle color theme">Theme</button><button id="menu" type="button" aria-controls="navigation" aria-expanded="false">Menu</button><a href="https://github.com/Newton-School/gogo">GitHub ↗</a></div></header>
<div class="layout"><nav id="navigation" aria-label="Documentation">{navigation(pages, page['id'])}</nav><main id="content"><div class="eyebrow">{html.escape(page['section'])}</div><article>{body}</article><nav class="pager" aria-label="Adjacent pages">{''.join(adjacent)}</nav><footer>Gogo · MIT licensed · Alpha documentation<br>Public API extracted from source revision {revision[:12]}. Framework behavior is unchanged by these docs.</footer></main><aside class="contents" aria-label="On this page"><h2>On this page</h2>{on_page}</aside></div>
<dialog id="search-dialog" aria-labelledby="search-title"><div class="search-header"><h2 id="search-title">Search documentation</h2><button id="search-close" type="button" aria-label="Close search">Close</button></div><label for="search-input">Feature, package, or API symbol</label><input id="search-input" type="search" placeholder="Try: ForeignKeyField, chord, CSRF…" autocomplete="off"><p id="search-status" role="status"></p><ul id="search-results"></ul></dialog><div id="announcement" class="sr-only" role="status"></div></body></html>'''


class Links(HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids, self.links = set(), []

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if "id" in values:
            if values["id"] in self.ids:
                raise ValueError("Duplicate HTML id: " + values["id"])
            self.ids.add(values["id"])
        if tag in ("a", "link", "script"):
            destination = values.get("href", values.get("src", ""))
            if destination:
                self.links.append(destination)


def check_links(directory):
    documents = {}
    for path in sorted(directory.glob("*.html")):
        parsed = Links()
        parsed.feed(path.read_text())
        documents[path] = parsed
    count = 0
    for path, document in documents.items():
        for destination in document.links:
            parts = urlsplit(destination)
            if parts.scheme or parts.netloc:
                continue
            target = (path.parent / unquote(parts.path)).resolve() if parts.path else path
            if not target.is_relative_to(directory) or not target.is_file():
                raise ValueError(f"Broken local link in {path.name}: {destination}")
            if parts.fragment and target in documents and unquote(parts.fragment) not in documents[target].ids:
                raise ValueError(f"Missing anchor in {path.name}: {destination}")
            count += 1
    return count


def build():
    manifest = json.loads((DOCS / "navigation.json").read_text())
    catalog = json.loads(subprocess.check_output(["go", "run", "./docs/tools/catalog"], cwd=ROOT, text=True))
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    pages = []
    owners = {}
    for entry in manifest:
        page = dict(entry)
        page["source"] = expand(safe_file(entry["file"]).read_text(), revision)
        pages.append(page)
        for package in entry.get("packages", []):
            if package in owners:
                raise ValueError("Duplicate guide owner: " + package)
            owners[package] = entry["id"]
    details = []
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
            owner_id = owners[str(owner)]
            text = path.read_text()
            title_match = re.search(r"^# (.+)$", text, re.M)
            title = title_match[1].replace("`", "") if title_match else relative.stem
            identifier = "detail-" + slug(str(relative.with_suffix("")))
            owner_page = next(p for p in pages if p["id"] == owner_id)
            owner_page["source"] += f"\n\n- [{title}]({identifier}.md)\n"
            # Reuse exact package documents with safe relative source links.
            expanded = expand("{{include " + str(relative) + "}}", revision)
            details.append({"id": identifier, "title": title, "section": owner_page["section"], "parent": owner_id, "source": "# " + title + "\n\n[Feature guide](" + owner_id + ".md)\n\n" + expanded})
    for page in pages:
        if any(p["parent"] == page["id"] for p in details):
            # Give the generated further-reading list a visible, stable section.
            first = page["source"].find("\n\n- [" + next(p["title"] for p in details if p["parent"] == page["id"]) + "](" + next(p["id"] for p in details if p["parent"] == page["id"]) + ".md)")
            page["source"] = page["source"][:first] + "\n\n## Technical guides" + page["source"][first:]
    pages.extend(details)
    vocabulary = ["# Template vocabulary", "", "Generated from a default `templates.Engine.Describe(4096)` without loading or rendering templates. These are registered names, not a claim that every Django extension is available. See [Templates](templates.md) for usage.", "", "## Built-in tags", ""]
    vocabulary.extend("- `" + name + "`" for name in catalog["Templates"]["Tags"])
    vocabulary += ["", "## Built-in filters", ""]
    vocabulary.extend("- `" + name + "`" for name in catalog["Templates"]["Filters"])
    pages.append({"id": "template-vocabulary", "title": "Template tags and filters", "section": "Reference", "source": "\n".join(vocabulary)})
    pages.append({"id": "settings", "title": "All settings", "section": "Reference", "source": settings_page(catalog["Settings"])})
    inventory = ["# Package reference", "", "Every public library package in this checkout is listed below. These references are generated from Go syntax and comments, not copied by hand. They include types, exported struct fields, interfaces, constants, functions, and methods. CLI commands are documented in [Management commands](commands.md).", "", "| Package | Learn it | API declarations |", "| --- | --- | --- |"]
    references = []
    actual = set()
    for package in catalog["Packages"]:
        directory = package["Directory"]
        actual.add(directory)
        if directory not in owners:
            raise ValueError("Public package needs a feature guide: " + directory)
        identifier = "api-" + slug(directory if directory != "." else "gogo")
        title = directory if directory != "." else "gogo"
        inventory.append(f"| [{title}]({identifier}.md) | [Guide]({owners[directory]}.md) | {len(package['Declarations'])} |")
        references.append({"id": identifier, "title": title, "section": "Go API", "source": api_page(package, owners[directory], revision)})
    if set(owners) != actual:
        raise ValueError("Guide maps nonexistent public packages: " + str(sorted(set(owners) - actual)))
    pages.append({"id": "packages", "title": "Package index", "section": "Reference", "source": "\n".join(inventory)})
    pages.extend(references)
    identifiers = [p["id"] for p in pages]
    if len(set(identifiers)) != len(identifiers):
        raise ValueError("Duplicate page identifiers")
    if any(not re.fullmatch(r"[a-z0-9-]+", identifier) for identifier in identifiers):
        raise ValueError("Unsafe page identifier")
    # Only generate within the fixed, ignored documentation output directory.
    # Do not delete arbitrary output paths or user-owned files.
    OUTPUT.mkdir(exist_ok=True)
    assets = OUTPUT / "assets"
    assets.mkdir(exist_ok=True)
    for name in ("style.css", "site.js"):
        shutil.copyfile(DOCS / "assets" / name, assets / name)
    search = []
    for page in pages:
        def link(destination):
            if re.fullmatch(r"[a-z0-9-]+\.md(?:#[a-z0-9-]+)?", destination):
                return destination.replace(".md", ".html", 1)
            return destination
        body, toc = markdown(page["source"], link)
        (OUTPUT / (page["id"] + ".html")).write_text(render_page(page, pages, body, toc, revision))
        search.append({"title": page["title"], "url": page["id"] + ".html", "group": page["section"], "text": page["source"]})
        for _, title, identifier in toc:
            search.append({"title": title, "url": page["id"] + ".html#" + identifier, "group": page["title"], "text": title})
    (assets / "search-index.js").write_text("window.GOGO_SEARCH = " + json.dumps(search, ensure_ascii=True).replace("<", "\\u003c") + ";\n")
    links = check_links(OUTPUT)
    summary = {"version": VERSION, "sourceRevision": revision, "pages": len(pages), "featureGuides": len(manifest), "technicalGuides": len(details), "publicPackages": len(catalog["Packages"]), "declarations": sum(len(p["Declarations"]) for p in catalog["Packages"]), "settings": len(catalog["Settings"]), "localLinksChecked": links}
    (OUTPUT / "coverage.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args()
    build()
