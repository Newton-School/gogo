"use strict";

// English catalog and ISO formats for the unmodified Django UI dependencies.
// These functions format controls only; Go retains cleaning and locale policy.
if (typeof window !== "undefined") {
  window.gettext = window.gettext_noop = message => message;
  window.pgettext = (_context, message) => message;
  window.ngettext = (singular, plural, count) => count === 1 ? singular : plural;
  window.interpolate = (format, values, named = false) => {
    let index = 0;
    return named ? format.replace(/%\(([^)]+)\)s/g, (_match, key) => String(values[key])) : format.replace(/%s/g, () => String(values[index++]));
  };
  window.get_format = name => ({DATE_INPUT_FORMATS: ["%Y-%m-%d"], TIME_INPUT_FORMATS: ["%H:%M:%S"], FIRST_DAY_OF_WEEK: 0}[name]);

  window.addEventListener("load", () => {
    const $ = window.django?.jQuery;
    if (!$?.fn.select2) return;
    // Use Django's bundled Select2 with Gogo's bounded, same-origin lookup.
    // The original controls remain a usable non-JavaScript fallback.
    for (const source of document.querySelectorAll("input[data-relation-url]")) {
      if (!source.form || source.disabled || source.readOnly) continue;
      const endpoint = new URL(source.dataset.relationUrl, window.location.href);
      if (endpoint.origin !== window.location.origin) continue;
      const multiple = Boolean(source.dataset.choiceTarget);
      let select = multiple ? document.getElementById(source.dataset.choiceTarget) : null;
      if (multiple && (!select || select.disabled || select.form !== source.form)) continue;
      if (!multiple) {
        select = document.createElement("select");
        select.id = source.id + "_select";
        select.required = source.required;
        select.append(new Option("", ""));
        if (source.value) select.append(new Option(source.value, source.value, true, true));
        source.after(select);
        for (const label of source.form.querySelectorAll("label[for]")) {
          if (label.htmlFor === source.id) label.htmlFor = select.id;
        }
        $(select).on("change", () => { source.value = select.value; });
        source.type = "hidden";
      } else {
        source.hidden = true;
        for (const label of source.form.querySelectorAll("label[for]")) {
          if (label.htmlFor === source.id) label.hidden = true;
        }
      }
      for (const attribute of ["aria-describedby", "aria-invalid"]) {
        if (source.hasAttribute(attribute)) select.setAttribute(attribute, source.getAttribute(attribute));
      }
      select.classList.add("admin-autocomplete");
      $(select).select2({width: "resolve", placeholder: "", allowClear: !select.required, minimumInputLength: 0,
        ajax: {delay: 250, transport(params, success, failure) {
          const controller = new AbortController();
          const term = String(params.data?.term || "");
          const page = Number(params.data?.page || 1);
          if (term.length > 256 || !Number.isInteger(page) || page < 1 || page > 100) { failure(); return {abort() {}}; }
          const url = new URL(endpoint);
          url.searchParams.set("term", term); url.searchParams.set("page", String(page));
          fetch(url, {signal: controller.signal, credentials: "same-origin", cache: "no-store", headers: {Accept: "application/json"}})
            .then(async response => {
              if (!response.ok) throw new Error("Lookup unavailable");
              const text = await response.text();
              if (text.length > 1048576) throw new Error("Lookup too large");
              const payload = JSON.parse(text);
              if (!Array.isArray(payload.results) || payload.results.length > 20 || payload.results.some(row => typeof row.id !== "string" || typeof row.text !== "string" || row.id.length > 4096 || row.text.length > 4096)) throw new Error("Invalid lookup");
              success({results: payload.results, pagination: {more: payload.pagination?.more === true}});
            }).catch(error => { if (error.name !== "AbortError") failure(); });
          return {abort() { controller.abort(); }};
        }}
      });
      const status = document.getElementById(source.id + "_lookup_status");
      if (status) status.textContent = "Search and select a related object.";
    }
  });

  // Keep Gogo's single datetime POST value. Django's visible Date/Time inputs
  // progressively enhance the native control, which remains the no-JS fallback.
  for (const original of document.querySelectorAll("input.vDateTimeField")) {
    if (!original.form || original.disabled || original.readOnly) continue;
    const box = document.createElement("p");
    box.className = "datetime";
    const components = [];
    const values = original.value.split("T");
    for (const [index, title] of ["Date:", "Time:"].entries()) {
      const label = document.createElement("label");
      const input = document.createElement("input");
      input.type = "text";
      input.className = index === 0 ? "vDateField" : "vTimeField";
      input.id = original.id + "_" + index;
      input.value = values[index] || "";
      input.required = original.required;
      for (const attribute of ["aria-describedby", "aria-invalid"]) {
        if (original.hasAttribute(attribute)) input.setAttribute(attribute, original.getAttribute(attribute));
      }
      label.htmlFor = input.id;
      label.textContent = title;
      label.className = "gogo-datetime-label";
      box.append(label, input);
      if (index === 0) box.append(document.createElement("br"));
      components.push(input);
    }
    const update = () => {
      original.value = components.every(input => input.value.trim() === "") ? "" : components[0].value.trim() + "T" + components[1].value.trim();
    };
    original.type = "hidden";
    original.after(box);
    for (const label of original.form.querySelectorAll("label[for]")) {
      if (label.htmlFor === original.id) label.htmlFor = components[0].id;
    }
    for (const input of components) input.addEventListener("change", update);
    original.form.addEventListener("submit", update);
  }
}

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
