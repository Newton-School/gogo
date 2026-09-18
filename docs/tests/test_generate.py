import importlib.util
import json
from pathlib import Path
import re
import unittest

spec = importlib.util.spec_from_file_location("gogo_docs", Path(__file__).resolve().parents[1] / "generate.py")
docs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(docs)


class ContentTests(unittest.TestCase):
    def test_include_cannot_escape_repository(self):
        with self.assertRaises(ValueError):
            docs.safe_file("../outside-repository.md")

    def test_examples_include_exact_source(self):
        path = "docs/snippets/catalog/models.go"
        result = docs.expand("{{code " + path + "}}", "revision", {}, {})
        self.assertIn(docs.safe_file(path).read_text().rstrip(), result)
        self.assertIn("```go\n", result)
        self.assertIn("/revision/" + path, result)

    def test_tutorial_file_languages_are_preserved(self):
        for path, language in [
            ("docs/snippets/storefront/config/catalog.go.txt", "go"),
            ("docs/snippets/storefront/compose.yaml", "yaml"),
            ("docs/snippets/storefront/Dockerfile", "dockerfile"),
            ("docs/snippets/storefront/.dockerignore", "text"),
        ]:
            result = docs.expand("{{code " + path + "}}", "revision", {}, {})
            self.assertIn("```" + language + "\n", result)
            self.assertIn(docs.safe_file(path).read_text().rstrip(), result)

    def test_every_package_has_a_feature_guide_with_an_example(self):
        manifest = json.loads(docs.safe_file("docs/navigation.json").read_text())
        feature_map = docs.safe_file("docs/guides/features.md").read_text()
        for guide in manifest:
            if not guide.get("packages"):
                continue
            source = docs.safe_file(guide["file"]).read_text()
            self.assertRegex(source, r"\{\{code |```(?:go|sh|json|yaml)\n", guide["id"])
            self.assertIn("(" + guide["id"] + ".md", feature_map, guide["id"])

    def test_all_tested_tutorial_files_are_shown_to_readers(self):
        manifest = json.loads(docs.safe_file("docs/navigation.json").read_text())
        source = "\n".join(docs.safe_file(guide["file"]).read_text() for guide in manifest)
        root = docs.safe_file("docs/snippets/storefront/config/catalog.go.txt").parents[2]
        for snippet in root.rglob("*.go.txt"):
            relative = snippet.relative_to(docs.ROOT).as_posix()
            self.assertIn("{{code " + relative + "}}", source)

    def test_first_project_has_an_ordered_beginner_path(self):
        tree = json.loads(docs.safe_file("docs/tree.json").read_text())
        self.assertEqual(list(tree), ["docsSidebar", "adminSidebar", "asyncSidebar"])
        self.assertEqual(tree["docsSidebar"][:3], ["index", "quickstart", "showcase"])
        sections = json.loads(docs.safe_file("docs/sections.json").read_text())
        quickstart = next(s for s in sections if s["id"] == "quickstart")
        self.assertEqual([s[0] for s in quickstart["sources"]], ["installation", "quickstart", "running", "tutorial-api", "docker"])

    def test_code_is_not_rewritten(self):
        source = '```go\n// [Example](models.md)\n// {{code missing.go}}\n```\n'
        self.assertEqual(docs.expand(source, "rev", {}, {}), source)
        self.assertEqual(docs.rewrite_links(source, "docs/index.md", {}, "rev", {"models"}), source)

    def test_unsafe_links_rejected(self):
        for target in ["javascript:alert", "data:text/html,bad", "//example.test"]:
            with self.assertRaises(ValueError):
                docs.rewrite_links(f"[bad]({target})", "docs/index.md", {}, "rev", {})

    def test_package_link_to_included_document_stays_local(self):
        result = docs.rewrite_links("[Queries](../orm/routing.md#routing)", "core/api/README.md", {"core/orm/routing.md": "detail-core-orm-routing"}, "rev", {})
        self.assertEqual(result, "[Queries](detail-core-orm-routing.md#routing)")

    def test_guide_anchor_preserved(self):
        source = "[Field](api-core-models.md#field)"
        self.assertEqual(docs.rewrite_links(source, "docs/guides/models.md", {}, "rev", {"api-core-models"}), source)

    def test_missing_source_link_fails(self):
        with self.assertRaises(ValueError):
            docs.rewrite_links("[Code](missing.go)", "core/api/README.md", {}, "rev", {})

    def test_unclosed_fence_fails(self):
        with self.assertRaises(ValueError):
            docs.outside_fences("```go\nunclosed", lambda text: text)

    def test_all_model_and_form_kinds_have_human_documentation(self):
        cases = [("core/models/schema.go", "docs/guides/model-fields.md"), ("core/forms/fields.go", "docs/guides/forms.md")]
        for source, guide in cases:
            names = re.findall(r'^\s*([A-Z][A-Za-z]+)\s+Kind\s*=', docs.safe_file(source).read_text(), re.M)
            text = docs.safe_file(guide).read_text()
            self.assertGreater(len(names), 20)
            for name in names:
                self.assertIn(name, text, f"{guide} does not describe {name}")

    def test_methods_stay_under_their_type(self):
        def declaration(name, kind):
            return {"Name": name, "Kind": kind, "Signature": "// " + name, "Doc": "", "File": "sample.go", "Line": 1}
        package = {"Directory": "sample", "Path": "example.com/sample", "Doc": "", "Declarations": [declaration("Model", "type"), declaration("Model.Save", "method"), declaration("Build", "function")]}
        source = docs.api_page(package, "models", "rev")
        self.assertLess(source.index("### Model"), source.index("#### Model.Save"))
        self.assertLess(source.index("#### Model.Save"), source.index("## Functions"))
        self.assertEqual(source.count("// Model.Save"), 1)


class SettingsTests(unittest.TestCase):
    def setUp(self):
        self.settings = [{"Name": "GOGO_EXAMPLE", "Group": "Example", "Kind": 3,
                          "Default": "5s", "Sensitive": True, "Min": 1,
                          "Choices": [], "RequiredFor": ["example"]}]

    def test_description_is_next_to_generated_setting_metadata(self):
        page = docs.settings_page(self.settings, {"GOGO_EXAMPLE": "Limits operation duration."})
        self.assertIn("| Variable | Description | Default | Required for | Type and constraints |", page)
        self.assertIn("| `GOGO_EXAMPLE` | Limits operation duration. | `5s` | example | duration; Secret; redacted; Minimum 1 ns |", page)
        self.assertIn("declaring a variable does not register a service", page)

    def test_missing_unknown_and_empty_descriptions_fail(self):
        for descriptions in [{}, {"GOGO_EXAMPLE": "Purpose.", "GOGO_REMOVED": "Old."},
                             {"GOGO_EXAMPLE": " "}, {"GOGO_EXAMPLE": None}]:
            with self.subTest(descriptions=descriptions), self.assertRaises(ValueError):
                docs.settings_page(self.settings, descriptions)

    def test_descriptions_cannot_split_table_rows_or_cells(self):
        page = docs.settings_page(self.settings, {"GOGO_EXAMPLE": "One | two\nthree."})
        self.assertIn("| One \\| two three. |", page)


class TreeTests(unittest.TestCase):
    def setUp(self):
        self.pages = {"guide": {"id": "guide", "title": "Guide"}, "note": {"id": "note", "title": "Details", "parent": "guide"}}

    def test_feature_owns_nested_notes(self):
        result = docs.compile_tree({"sidebar": ["guide"]}, self.pages)
        self.assertEqual(result["sidebar"][0]["link"]["id"], "guide")
        self.assertEqual(result["sidebar"][0]["items"][0]["id"], "note")

    def test_explicit_note_placement_wins(self):
        result = docs.compile_tree({"sidebar": ["guide", "note"]}, self.pages)
        self.assertEqual([item["id"] for item in result["sidebar"]], ["guide", "note"])

    def test_orphan_rejected(self):
        with self.assertRaisesRegex(ValueError, "missing"):
            docs.compile_tree({"sidebar": [{"guide": "guide", "notes": []}]}, self.pages)

    def test_duplicate_rejected(self):
        with self.assertRaisesRegex(ValueError, "Duplicate"):
            docs.compile_tree({"sidebar": ["guide", "guide"]}, self.pages)

    def test_unknown_page_rejected(self):
        with self.assertRaisesRegex(ValueError, "Unknown"):
            docs.compile_tree({"sidebar": ["absent"]}, self.pages)

    def test_category_has_a_descriptive_landing_page(self):
        result = docs.compile_tree({"sidebar": [{"id": "learn", "label": "Learn", "description": "A clear starting point", "items": ["guide"]}]}, self.pages)
        link = result["sidebar"][0]["link"]
        self.assertEqual(link["type"], "generated-index")
        self.assertEqual(link["description"], "A clear starting point")


class ConsolidationTests(unittest.TestCase):
    def setUp(self):
        self.pages = {
            "models": {"id": "models", "title": "Models", "source": "# Models\n\n## Example\n\n[Field](api-models.md#field)\n\n```go\n// [Field](api-models.md#field)\n```", "references": ["api-models"]},
            "fields": {"id": "fields", "title": "Fields", "source": "# Fields\n\n## Example\n\n[Example](#example)"},
            "note": {"id": "note", "title": "Details", "parent": "models", "source": "# Details\n\nContract."},
            "api-models": {"id": "api-models", "title": "models", "api": True, "source": "# models\n\n### Field {#field}\n\nDeclaration."},
        }
        self.sections = [{"id": "models", "title": "Models", "sources": [["models", "Definition"], ["fields", "Fields"]]}]

    def test_every_source_is_kept_once_with_namespaced_anchors(self):
        pages, routes = docs.consolidate_pages(self.pages, self.sections, "rev")
        self.assertEqual(list(pages), ["models"])
        body = pages["models"]["source"]
        for anchor in ["models-example", "fields-example", "api-models-field"]:
            self.assertEqual(body.count("{#" + anchor + "}"), 1)
        self.assertIn("[Example](models.md#fields-example)", body)
        self.assertIn("[Field](models.md#api-models-field)", body)
        self.assertIn("// [Field](api-models.md#field)", body)
        self.assertEqual(body.count("Contract."), 1)
        self.assertEqual(body.count("Declaration."), 1)
        self.assertEqual(body.count("<details>"), 2)
        self.assertEqual(routes["api-models"]["anchors"]["field"], "api-models-field")

    def test_orphans_and_duplicate_placement_fail(self):
        with self.assertRaisesRegex(ValueError, "Unmapped"):
            docs.consolidate_pages(self.pages, [{"id": "models", "title": "Models", "sources": [["models", "Definition"]]}], "rev")
        with self.assertRaisesRegex(ValueError, "duplicate"):
            docs.consolidate_pages(self.pages, self.sections + [{"id": "another", "title": "Another", "sources": [["models", "Definition"]]}], "rev")

    def test_explicit_detail_owner_wins(self):
        sections = self.sections + [{"id": "advanced", "title": "Advanced", "sources": [], "details": ["note"]}]
        pages, routes = docs.consolidate_pages(self.pages, sections, "rev")
        self.assertNotIn("Contract.", pages["models"]["source"])
        self.assertIn("Contract.", pages["advanced"]["source"])
        self.assertEqual(routes["note"]["page"], "advanced")

    def test_single_source_uses_one_title_and_keeps_anchors(self):
        pages = {"setup": {"id": "setup", "title": "Setup", "source": "# Setup\n\nIntroduction.\n\n## Install\n\nExample."}}
        result, routes = docs.consolidate_pages(pages, [{"id": "setup", "title": "Setup", "sources": [["setup", "Setup"]]}], "rev")
        self.assertTrue(result["setup"]["source"].startswith("# Setup {#setup}\n\nIntroduction."))
        self.assertIn("## Install {#setup-install}", result["setup"]["source"])
        self.assertNotIn("## Setup", result["setup"]["source"])
        self.assertEqual(routes["setup"]["anchors"][""], "setup")


if __name__ == "__main__":
    unittest.main()
