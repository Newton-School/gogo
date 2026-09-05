"use strict";

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
