import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("gogo_docs", Path(__file__).resolve().parents[1] / "build.py")
docs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(docs)


class MarkdownTests(unittest.TestCase):
    def test_code_and_raw_html_are_escaped(self):
        rendered, _ = docs.markdown('# Test\n\n<script>alert(1)</script>\n\n```go\nx := "<script>"\n```')
        self.assertNotIn("<script>", rendered)
        self.assertIn("&lt;script&gt;", rendered)

    def test_code_in_link_label(self):
        rendered = docs.inline('[`Store.Save`](api.html)')
        self.assertEqual(rendered, '<a href="api.html"><code>Store.Save</code></a>')
        self.assertNotIn("\x00", rendered)

    def test_unsafe_links_rejected(self):
        for target in ["javascript:alert", "data:text/html,bad", "//example.test"]:
            with self.assertRaises(ValueError):
                docs.inline(f"[bad]({target})")

    def test_duplicate_headings_have_unique_anchors(self):
        rendered, toc = docs.markdown("# Title\n\n## Save\n\nOne\n\n## Save\n\nTwo")
        self.assertEqual([entry[2] for entry in toc], ["save", "save-1"])
        parser = docs.Links()
        parser.feed(rendered)

    def test_tables_keep_pipes_in_code(self):
        rendered, _ = docs.markdown("| Example | Meaning |\n| --- | --- |\n| `a|b` | Either |")
        self.assertIn("<code>a|b</code>", rendered)
        self.assertEqual(docs.table_cells(r"| a\|b | c |"), ["a|b", "c"])

    def test_invalid_table_and_fence_fail(self):
        for source in ["```go\nunclosed", "| A | B |\n| --- | --- |\n| One |"]:
            with self.assertRaises(ValueError):
                docs.markdown(source)

    def test_list_and_toc(self):
        rendered, toc = docs.markdown("## Steps\n\n1. First\n\n2. Second\n\n> Caution")
        self.assertIn("<ol>", rendered)
        self.assertEqual(rendered.count("<li>"), 2)
        self.assertEqual(toc, [(2, "Steps", "steps")])

    def test_include_cannot_escape_repository(self):
        with self.assertRaises(ValueError):
            docs.safe_file("../outside-repository.md")

    def test_links_and_missing_anchors(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "a.html").write_text('<h1 id="one">One</h1><a href="b.html#two">B</a>')
            (root / "b.html").write_text('<h1 id="two">Two</h1><a href="a.html#one">A</a>')
            self.assertEqual(docs.check_links(root), 2)
            (root / "b.html").write_text('<h1 id="missing">Two</h1>')
            with self.assertRaises(ValueError):
                docs.check_links(root)

    def test_outside_output_link_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "a.html").write_text('<a href="../outside.html">Outside</a>')
            with self.assertRaises(ValueError):
                docs.check_links(root)


if __name__ == "__main__":
    unittest.main()
