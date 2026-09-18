import json
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import code_examples
import field_reference
import generate


class CodeExampleTests(unittest.TestCase):
    def test_excerpt_keeps_code_but_removes_shared_indentation(self):
        source = "func demo() {\n\t// docs:begin example\n\tif ok {\n\t\trun()\n\t}\n\t// docs:end example\n}"
        self.assertEqual(code_examples.extract(source, "example"), "if ok {\n\trun()\n}")
        self.assertNotIn("docs:", code_examples.without_markers(source))
        self.assertIn("\t\trun()", code_examples.without_markers(source))

    def test_invalid_markers_and_oversized_examples_fail(self):
        cases = [
            "// docs:begin example\n// docs:end example",
            "// docs:begin example\nvalue := 1",
            "// docs:begin example\n// docs:begin nested\n",
            "// docs:begin example\nvalue := 1\n// docs:end other",
            "// docs:end example",
            "// docs:begin example\n" + "value++\n" * 21 + "// docs:end example",
            ("// docs:begin example\nvalue++\n// docs:end example\n") * 2,
        ]
        for source in cases:
            with self.subTest(source=source), self.assertRaises(ValueError):
                code_examples.extract(source, "example")
        with self.assertRaisesRegex(ValueError, "Unknown"):
            code_examples.extract("", "absent")

    def test_excerpt_include_is_safe_and_never_rescans_inserted_code(self):
        source = '// docs:begin example\nfmt.Println("{{code missing.go}}")\n// docs:end example\n'
        with patch.object(generate, "safe_file") as file:
            file.return_value.read_text.return_value = source
            result = generate.expand("{{snippet docs/example.go example}}", "rev", {}, {})
            self.assertIn('fmt.Println("{{code missing.go}}")', result)
            file.assert_called_once_with("docs/example.go")
        with self.assertRaises(ValueError):
            generate.expand("{{snippet ../outside.go example}}", "rev", {}, {})
        fenced = "```text\n{{snippet missing.go example}}\n```"
        self.assertEqual(generate.expand(fenced, "rev", {}, {}), fenced)

    def test_every_curated_excerpt_is_small_and_exists_in_tested_code(self):
        entries = json.loads((generate.DOCS / "code-examples.json").read_text())
        self.assertGreaterEqual(len(entries), 40)
        for name, entry in entries.items():
            with self.subTest(name=name):
                self.assertTrue(entry["file"].endswith("_test.go"))
                value = code_examples.extract(generate.safe_file(entry["file"]).read_text(), name)
                self.assertLessEqual(len(value.splitlines()), 20)
                self.assertTrue(entry["intro"].strip())

    def test_stale_symbol_and_member_mappings_fail(self):
        catalog = {"Packages": [{"Directory": "sample", "Declarations": [
            {"Name": "Config", "Members": [{"Name": "Limit"}]}]}]}
        entry = {"file": "docs/example.go", "intro": "Configure it.", "symbols": ["sample.Missing"]}
        with patch.object(generate, "safe_file") as file:
            file.return_value.read_text.return_value = "// docs:begin example\nc.Limit = 2\n// docs:end example"
            with self.assertRaisesRegex(ValueError, "symbol"):
                code_examples.load({"example": entry}, catalog, file)
            entry["symbols"] = ["sample.Config"]
            entry["options"] = {"sample.Config": ["Removed"]}
            with self.assertRaisesRegex(ValueError, "option"):
                code_examples.load({"example": entry}, catalog, file)
            entry["options"] = {"sample.Config": ["Limit"]}
            symbols, members = code_examples.load({"example": entry}, catalog, file)
            self.assertIn("c.Limit = 2", symbols["sample.Config"])
            self.assertIn("c.Limit = 2", members["sample.Config.Limit"])

    def test_long_contracts_remain_available_without_a_wall_of_text(self):
        contract = "Important behavior. " + "Detailed requirement. " * 40
        result = "\n".join(code_examples.brief_contract(contract))
        self.assertTrue(result.startswith("Important behavior."))
        self.assertIn(contract.strip(), result)
        self.assertIn("Behavior, constraints and errors", result)
        self.assertNotIn("<details", "\n".join(code_examples.brief_contract("Short contract.")))

    def test_gofmt_fragment_does_not_expose_its_wrapper(self):
        self.assertEqual(field_reference.format_fragment("if ok { run() }"), "if ok {\n\trun()\n}")

    def test_constructor_examples_use_real_field_declarations(self):
        data = json.loads((generate.DOCS / "fields.json").read_text())
        examples = field_reference.constructor_examples(data)
        self.assertGreaterEqual(len(examples), 50)
        self.assertIn('field := models.CharField(', examples["core/models.CharField"])
        self.assertIn("field-models-char.md", examples["core/models.CharField"])
        self.assertIn("field-serializers-stringfield.md", examples["core/api.StringField"])

    def test_usage_precedes_full_signature_and_preserves_contract(self):
        package = {"Directory": "sample", "Path": "example.test/sample", "Doc": "", "Declarations": [
            {"Name": "New", "Kind": "function", "Signature": "func New() Value", "Doc": "Never saves data."}]}
        output = generate.api_page(package, "models", "rev", examples={"sample.New": "```go\nvalue := New()\n```"})
        self.assertIn("Never saves data.", output)
        self.assertLess(output.index("value := New()"), output.index("Go declaration"))
        self.assertEqual(output.count("func New() Value"), 1)
