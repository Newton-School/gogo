"use strict";
(() => {
  const byID = (id) => document.getElementById(id);
  const dialog = byID("search-dialog"), input = byID("search-input"), results = byID("search-results");
  let previousFocus;
  const announce = (message) => { byID("announcement").textContent = message; };
  try {
    const saved = localStorage.getItem("gogo-docs-theme");
    document.documentElement.dataset.theme = saved === "dark" || (!saved && matchMedia("(prefers-color-scheme: dark)").matches) ? "dark" : "light";
  } catch (_) { /* file:// storage can be unavailable; light remains readable. */ }
  byID("theme").addEventListener("click", () => {
    const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    try { localStorage.setItem("gogo-docs-theme", next); } catch (_) {}
    announce(`${next} theme selected`);
  });
  byID("menu").addEventListener("click", () => {
    const open = byID("navigation").classList.toggle("open");
    byID("menu").setAttribute("aria-expanded", String(open));
  });
  function search() {
    const query = input.value.trim().toLowerCase().slice(0, 200);
    results.replaceChildren();
    if (!query) { byID("search-status").textContent = "Search tutorials, guides, settings, and every public API declaration."; return; }
    const words = query.split(/\s+/);
    const found = (window.GOGO_SEARCH || []).map((item) => {
      const title = item.title.toLowerCase();
      const text = (item.title + " " + item.group + " " + item.text).toLowerCase();
      const score = words.every((word) => text.includes(word)) ? (title === query ? 100 : title.includes(query) ? 70 : words.every((word) => title.includes(word)) ? 50 : 10) : 0;
      return { item, score };
    }).filter((result) => result.score).sort((a, b) => b.score - a.score).slice(0, 40);
    byID("search-status").textContent = found.length ? `${found.length} results${found.length === 40 ? " (showing the first 40)" : ""}` : "No results. Try a package name or a shorter feature name.";
    for (const { item } of found) {
      const row = document.createElement("li"), link = document.createElement("a"), group = document.createElement("small");
      link.href = item.url;
      link.textContent = item.title;
      group.textContent = item.group;
      link.append(group); row.append(link); results.append(row);
    }
  }
  function openSearch() {
    if (dialog.open) return;
    previousFocus = document.activeElement;
    dialog.showModal(); search(); input.focus();
  }
  byID("search-open").addEventListener("click", openSearch);
  byID("search-close").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => previousFocus?.focus());
  input.addEventListener("input", search);
  document.addEventListener("keydown", (event) => {
    const typing = event.target instanceof HTMLElement && (event.target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(event.target.tagName));
    if (!typing && (event.key === "/" || ((event.metaKey || event.ctrlKey) && event.key === "k"))) { event.preventDefault(); openSearch(); }
  });
  for (const button of document.querySelectorAll(".copy")) {
    button.addEventListener("click", async () => {
      const code = button.closest(".code-block").querySelector("code");
      try {
        if (!navigator.clipboard?.writeText) throw new Error("Clipboard unavailable");
        await navigator.clipboard.writeText(code.textContent);
        button.textContent = "Copied";
        announce("Code copied to clipboard");
      } catch (_) {
        const selection = window.getSelection(), range = document.createRange();
        range.selectNodeContents(code); selection.removeAllRanges(); selection.addRange(range);
        button.textContent = "Selected";
        announce("Code selected. Use your keyboard copy command.");
      }
      setTimeout(() => { button.textContent = "Copy"; }, 1800);
    });
  }
})();
