"use strict";

// Slug suggestions are add-form convenience only. The ModelForm still cleans
// and authorizes every submitted field, including requests without JavaScript.
for (const target of document.querySelectorAll("input[data-prepopulate-from]")) {
  try {
    if (!target.form || target.disabled || target.readOnly || target.value !== "") continue;
    const ids = JSON.parse(target.dataset.prepopulateFrom);
    const limit = Number(target.dataset.prepopulateMaxlength);
    const unicode = target.dataset.prepopulateUnicode === "true";
    if (!["true", "false"].includes(target.dataset.prepopulateUnicode)) continue;
    if (!Array.isArray(ids) || ids.length < 1 || ids.length > 16 || !Number.isSafeInteger(limit) || limit < 0) continue;
    const sources = ids.map(id => typeof id === "string" ? document.getElementById(id) : null);
    if (sources.some(source => !source || source === target || source.form !== target.form || source.disabled || source.readOnly || !["INPUT", "TEXTAREA"].includes(source.tagName))) continue;
    let edited = false;
    target.addEventListener("input", () => { edited = true; });
    target.addEventListener("change", () => { edited = true; });
    const update = () => {
      if (edited || !target.form || target.disabled || target.readOnly || sources.some(source => source.form !== target.form || source.disabled || source.readOnly)) return;
      // Unicode suggestions retain letters/numbers. ASCII suggestions strip
      // decomposable accents; neither mode claims language transliteration.
      const text = sources.map(source => source.value).join(" ").slice(0, 65536);
      const normalized = unicode ? text.normalize("NFKC") : text.normalize("NFKD").replace(/[^\x00-\x7F]/g, "");
      const slug = normalized.toLowerCase().replace(unicode ? /[^\p{L}\p{N}_\s-]/gu : /[^a-z0-9_\s-]/g, "")
        .trim().replace(/[-\s]+/g, "-").replace(/^[-_]+|[-_]+$/g, "");
      target.value = limit > 0 ? Array.from(slug).slice(0, limit).join("") : slug;
    };
    for (const source of sources) {
      source.addEventListener("input", update);
      source.addEventListener("change", update);
    }
  } catch (_) {
    // Malformed custom markup disables suggestions, never form submission.
  }
}

// Suggestions are a convenience only. Posted IDs are revalidated by the scoped
// server resolver; labels from lookup JSON are never treated as HTML.
for (const input of document.querySelectorAll("input[data-relation-url]")) {
  const multiple = Boolean(input.dataset.choiceTarget);
  const choices = document.getElementById(multiple ? input.dataset.choiceTarget : input.getAttribute("list"));
  const status = document.getElementById(`${input.id}_lookup_status`);
  if (!choices || !status) continue;
  let timer;
  let controller;
  let generation = 0;
  input.addEventListener("input", () => {
    clearTimeout(timer);
    controller?.abort();
    const current = ++generation;
    const selected = multiple ? Array.from(choices.options).filter(option => option.selected) : [];
    choices.replaceChildren(...selected);
    const term = input.value.trim();
    if (!term) { status.textContent = "Type to search, or enter a related object ID."; return; }
    if (term.length > 256) { status.textContent = "Enter a shorter search."; return; }
    timer = setTimeout(async () => {
      controller = new AbortController();
      status.textContent = "Searching…";
      try {
        const endpoint = new URL(input.dataset.relationUrl, window.location.href);
        if (endpoint.origin !== window.location.origin) throw new Error("Invalid lookup URL");
        endpoint.searchParams.set("term", term);
        endpoint.searchParams.set("page", "1");
        const response = await fetch(endpoint, {credentials: "same-origin", cache: "no-store", signal: controller.signal, headers: {Accept: "application/json"}});
        if (!response.ok) throw new Error("Lookup failed");
        const body = await response.text();
        if (body.length > 1048576) throw new Error("Lookup response too large");
        const payload = JSON.parse(body);
        if (!Array.isArray(payload.results) || payload.results.length > 20) throw new Error("Invalid choices");
        const options = [];
        for (const row of payload.results) {
          if (typeof row.id !== "string" || typeof row.text !== "string" || row.id.length > 4096 || row.text.length > 4096) throw new Error("Invalid choice");
          const option = document.createElement("option");
          option.value = row.id;
          option.label = row.text;
          option.textContent = row.text;
          options.push(option);
        }
        if (current !== generation) return;
        const currentSelections = multiple ? Array.from(choices.options).filter(option => option.selected) : [];
        const selectedIDs = new Set(currentSelections.map(option => option.value));
        choices.replaceChildren(...currentSelections, ...options.filter(option => !selectedIDs.has(option.value)));
        status.textContent = options.length ? `${options.length} suggestions available.${payload.pagination?.more ? " Refine your search for more results." : ""}` : "No matching choices. You may enter a known related object ID.";
      } catch (error) {
        if (current !== generation || error.name === "AbortError") return;
        status.textContent = "Suggestions are unavailable. Retry your search or enter a known related object ID.";
      }
    }, 250);
  });
}
