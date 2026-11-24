// Tab-local navigation state: never write file paths to the account or server.
const positions = new Map();
let departure = null;
let highlighted = null;
let highlightTimer;
const MAX_DIRECTORIES = 100;

function normalizedPath(path) {
  return `/${String(path || '').split('/').filter(Boolean).join('/')}`;
}

export function directoryContext(req, user, shareHash = '') {
  return {
    scope: JSON.stringify([user?.id ?? user?.username ?? '', shareHash, shareHash || req?.source || '']),
    path: normalizedPath(req?.path),
    type: req?.type,
  };
}

function key(context) { return JSON.stringify([context.scope, context.path]); }

export function clearDirectoryHighlight() {
  clearTimeout(highlightTimer);
  highlighted?.classList.remove('navigation-returned');
  highlighted = null;
}

// Called before the route changes, while the outgoing DOM and scroll still exist.
export function captureDirectoryPosition(context, container) {
  clearDirectoryHighlight();
  departure = context;
  if (context.type !== 'directory' || !container) return;
  const id = key(context);
  const position = { top: container.scrollTop, left: container.scrollLeft };
  if (container.querySelectorAll) {
    const bounds = container.getBoundingClientRect();
    const items = container.querySelectorAll('.listing-view-root .listing-item[data-name]');
    const focused = document.activeElement;
    const listing = container.querySelector('.listing-view-root');
    const inset = listing ? parseFloat(getComputedStyle(listing).paddingTop) || 0 : 0;
    const visible = item => {
      const rect = item.getBoundingClientRect();
      return rect.top >= bounds.top + inset + 8 && rect.bottom <= bounds.bottom - 64;
    };
    const anchor = [...items].find(item => item === focused && visible(item)) || [...items].find(visible);
    if (anchor) {
      position.anchor = anchor.getAttribute('data-name');
      position.anchorTop = anchor.getBoundingClientRect().top - bounds.top;
    }
    position.top = container.scrollTop;
  }
  positions.delete(id);
  positions.set(id, position);
  if (positions.size > MAX_DIRECTORIES) positions.delete(positions.keys().next().value);
}

export function directoryReturnPosition(context, hash = '') {
  let name = '';
  if (hash) {
    try { name = decodeURIComponent(hash.slice(1)); } catch { name = hash.slice(1); }
  } else if (departure?.scope === context.scope) {
    const prefix = context.path === '/' ? '/' : `${context.path}/`;
    if (departure.path.startsWith(prefix)) name = departure.path.slice(prefix.length).split('/')[0];
  }
  return { ...positions.get(key(context)), name };
}

// Called after the new directory's DOM is ready, including large listings.
export function restoreDirectoryPosition(context, container, root, hash = '') {
  if (!container || !root) return '';
  const { top, left, name, anchor, anchorTop } = directoryReturnPosition(context, hash);
  container.scrollTop = top ?? 0;
  container.scrollLeft = left ?? 0;
  if (!name && !anchor) return '';
  // Match literal names (Chinese, %, #, quotes...), without CSS escaping or IDs.
  const item = Array.from(root.querySelectorAll('.listing-item[data-name]'))
    .find(element => element.getAttribute('data-name') === (name || anchor));
  if (!item) return '';
  const bounds = container.getBoundingClientRect();
  if (anchorTop !== undefined && (name || anchor) === anchor && !hash) {
    container.scrollTop += item.getBoundingClientRect().top - bounds.top - anchorTop;
  }
  const rect = item.getBoundingClientRect();
  const visibleTop = bounds.top + (parseFloat(getComputedStyle(root).paddingTop) || 0) + 8;
  const visibleBottom = bounds.bottom - 64; // status bar and bottom spacing
  if (rect.top < visibleTop || rect.bottom > visibleBottom) {
    container.scrollTop += rect.top - visibleTop - Math.max(0, (visibleBottom - visibleTop - rect.height) / 2);
  }
  // Focusing also makes content-visibility:auto materialize the saved anchor on
  // Forward navigation; preventScroll keeps control of the viewport here.
  item.focus({ preventScroll: true });
  if (!name) return '';
  clearDirectoryHighlight();
  highlighted = item;
  item.classList.add('navigation-returned');
  highlightTimer = setTimeout(clearDirectoryHighlight, 4000);
  return name;
}
