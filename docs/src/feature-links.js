import routes from '@site/.generated/routes.json';

// A linked declaration must be visible even when its reference is collapsed.
export function onRouteDidUpdate({location}) {
  if (!location.hash) return;
  requestAnimationFrame(() => {
    let anchor;
    try { anchor = decodeURIComponent(location.hash.slice(1)); } catch { return; }
    const page = location.pathname.split('/').filter(Boolean).pop();
    const moved = routes[page]?.legacyAnchors?.[anchor];
    if (!document.getElementById(anchor) && moved && moved.page !== page) {
      const base = location.pathname.replace(/\/[^/]+\/?$/, '/');
      window.location.replace(base + moved.page + '/#' + moved.anchor);
      return;
    }
    const anchors = Object.hasOwn(routes, page) ? routes[page].anchors : {};
    const legacy = Object.hasOwn(anchors, anchor) ? anchors[anchor] : undefined;
    if (!document.getElementById(anchor) && legacy) anchor = legacy;
    const target = document.getElementById(anchor);
    if (!target) return;
    let parent = target.parentElement;
    while (parent) {
      if (parent.tagName === 'DETAILS') parent.open = true;
      parent = parent.parentElement;
    }
    target.scrollIntoView();
  });
}
