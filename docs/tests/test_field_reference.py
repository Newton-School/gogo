import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

DOCS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(DOCS))
import field_reference


class FieldReferenceTests(unittest.TestCase):
    def test_every_published_example_compiles_and_validation_examples_execute(self):
        data = json.loads((DOCS / "fields.json").read_text())
        functions, calls, imports = [], [], set()
        for family, rows in data.items():
            for row in rows:
                name = family + row[0]
                body = field_reference.example_body(family, row)
                imports.update(field_reference.imports_for(body))
                functions.append("func " + name + "() {\n" + body + "\n}")
                calls.append(name + "()")
        program = "package main\nimport (\n" + "\n".join(json.dumps(path) for path in sorted(imports)) + ")\n"
        program += "\n".join(functions) + "\nfunc main() {\n" + "\n".join(calls) + "\n}\n"
        with tempfile.TemporaryDirectory(prefix="gogo-doc-fields-") as directory:
            source = Path(directory) / "main.go"
            source.write_text(program)
            result = subprocess.run(["go", "run", str(source)], cwd=DOCS.parent,
                                    capture_output=True, text=True, timeout=120)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_complete_examples_have_only_needed_imports(self):
        example = field_reference.complete_example("forms", ["Char", 'forms.NewField("value", forms.Char)', "Text", "hello"])
        self.assertIn('"net/url"', example)
        self.assertNotIn('"time"', example)
        self.assertIn("func main()", example)
