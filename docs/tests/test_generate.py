import importlib.util
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


if __name__ == "__main__":
    unittest.main()
