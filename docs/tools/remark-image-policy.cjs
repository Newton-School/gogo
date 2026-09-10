// Docusaurus 3.10.2 depends on image-size 2.0.2, whose ICNS/JXL/HEIF
// parsers have unpatched infinite-loop advisories. No current guide needs
// Markdown image processing. Reject it before the built-in image transformer;
// this also catches reference images and files disguised with safe extensions.
// Static theme assets (such as our SVG logo) do not use that parser.
module.exports = function imagePolicy() {
  return function transform(tree, file) {
    function visit(node) {
      if (node.type === 'image' || node.type === 'imageReference') {
        file.fail('Markdown images are disabled until the documentation image parser is patched. Use a reviewed static theme asset instead.', node);
      }
      for (const child of node.children || []) visit(child);
    }
    visit(tree);
  };
};
