"""Readable API reference layout; all contracts come from source or reviewed options."""

import re
from code_examples import brief_contract


def slug(text):
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-") or "section"


def code_span(value):
    """Keep Go types readable without leaking Markdown table delimiters."""
    value = " ".join(value.split()).replace("|", "\\|")
    fence = "`" * (max([0] + [len(m[0]) for m in re.finditer(r"`+", value)]) + 1)
    return fence + " " + value + " " + fence


def member_contracts(package, declaration, options):
    key = package["Directory"] + "." + declaration["Name"]
    override = options.get(key, {}).get("fields", {})
    return [(member, override.get(member["Name"], member["Doc"])) for member in declaration.get("Members") or []]


def declaration_details(package, declaration, options):
    lines = []
    key = package["Directory"] + "." + declaration["Name"]
    if key in options:
        lines += ['<div className="reference-guide">', "",
                  f"**Configuration:** [Every option, default and example](options-{slug(key)}.md).", "", "</div>", ""]
    else:
        members = member_contracts(package, declaration, options)
        if members:
            lines += ['<div className="reference-members">', "", "**Members**", ""]
            for member, description in members:
                name = "Embedded type" if member["Name"] == "(embedded or positional)" else member["Name"]
                lines += ['<div className="reference-member">', "", f"**{name}** · {code_span(member['Type'])}", ""]
                if description.strip():
                    lines += [description.strip(), ""]
                lines += ["</div>", ""]
            lines += ["</div>", ""]
    sections = []
    for key, label in [("Parameters", "Parameters"), ("Results", "Returns")]:
        items = declaration.get(key) or []
        if not items:
            continue
        sections += ['<div className="reference-io">', "", "**" + label + "**", "",
                     "| " + ("Parameter" if key == "Parameters" else "Return") + " | Type |", "| --- | --- |"]
        for index, item in enumerate(items, 1):
            name = item["Name"]
            if name == "(embedded or positional)":
                name = str(index)
            else:
                name = code_span(name)
            sections.append(f"| {name} | {code_span(item['Type'])} |")
        sections += ["", "</div>", ""]
    if sections:
        lines += ['<div className="reference-call">', "", *sections, "</div>", ""]
    return lines


def symbol_index(declarations, label):
    lines = []
    folded = len(declarations) > 24
    if folded:
        lines += ['<details className="reference-index-disclosure">',
                  f"<summary>Browse {len(declarations)} {label.lower()}</summary>", ""]
    lines += ['<div className="reference-index">', ""]
    lines += [f"- [{item['Name']}](#{slug(item['Name'])})" for item in declarations]
    lines += ["", "</div>", ""]
    if folded:
        lines += ["</details>", ""]
    return lines


def symbol(package, declaration, options, level=3, examples=None):
    name = declaration["Name"]
    heading = name.split(", ")[0] + (" and related values" if ", " in name else "")
    signature = declaration["Signature"]
    lines = ["#" * level + f" {heading} {{#{slug(name)}}}", "",
             '<p className="reference-kind">' + declaration["Kind"].capitalize() + "</p>", ""]
    lines += brief_contract(declaration["Doc"])
    example = (examples or {}).get(package["Directory"] + "." + name)
    if example:
        lines += ["**Example**", "", example, ""]
    # Full declarations remain available, but long structs should not bury usage.
    folded = bool(example) or declaration["Kind"] == "type" and (len(signature.splitlines()) > 8 or bool(declaration.get("Members")))
    if folded:
        lines += ['<details className="reference-declaration">', "<summary>Go declaration</summary>", ""]
    lines += ["```go", signature, "```", ""]
    if folded:
        lines += ["</details>", ""]
    lines += declaration_details(package, declaration, options)
    return lines


def api_page(package, guide_id, revision, options=None, examples=None):
    options = options or {}
    title = package["Directory"] if package["Directory"] != "." else "gogo"
    lines = [f"# {title}", "", package["Doc"].strip(), "",
             f"```go\nimport \"{package['Path']}\"\n```", "",
             f"[Feature guide and examples]({guide_id}.md) · [Compatibility](compatibility.md)", "",
             "This reference lists the public Go API. Use the feature guide for setup and complete examples; support varies by backend and configuration.", ""]
    declarations = package["Declarations"]
    for kind, label in [("type", "Types"), ("function", "Functions"), ("constant", "Constants"), ("variable", "Variables")]:
        selected = [d for d in declarations if d["Kind"] == kind]
        if not selected:
            continue
        lines += ["## " + label, ""]
        if kind in ("type", "function"):
            lines += symbol_index(selected, label)
        for declaration in selected:
            lines += ['<section className="reference-entry">', "", *symbol(package, declaration, options, examples=examples)]
            methods = [d for d in declarations if d["Kind"] == "method" and d["Name"].startswith(declaration["Name"] + ".")] if kind == "type" else []
            if methods:
                lines += ["**Methods**", "", *symbol_index(methods, "methods")]
            for method in methods:
                lines += ['<section className="reference-method">', "", *symbol(package, method, options, 4, examples), "</section>", ""]
            lines += ["</section>", ""]
    return "\n".join(lines)
