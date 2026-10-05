// router.js — tiny hash router.
//
// Pages are static <section> nodes already in the DOM; the router only flips
// `.active` classes and notifies a listener so a view can lazy-load data when
// the user lands on it. Hash routing avoids needing the backend to serve the
// shell on arbitrary deep paths.

const PAGES = ['broadcast', 'contacts', 'groups', 'import', 'history', 'templates', 'auth'];
const DEFAULT_PAGE = 'broadcast';

/** @type {(page: string) => void} */
let onChange = () => {};

/**
 * Wire the router. Listens to hash changes + nav clicks, fires `onPageChange`
 * with the freshly activated page slug.
 *
 * @param {(page: string) => void} onPageChange
 */
export function initRouter(onPageChange) {
  onChange = onPageChange;

  window.addEventListener('hashchange', applyFromHash);
  document.addEventListener('click', (e) => {
    const link = /** @type {HTMLElement} */ (e.target).closest('a.nav-item[data-page]');
    if (!link) return;
    // Let the default hash nav happen; we only want to close the mobile sidebar.
    if (window.matchMedia('(max-width: 768px)').matches) {
      document.querySelector('.sidebar')?.classList.remove('open');
      document.getElementById('sidebarBackdrop')?.classList.remove('open');
    }
  });

  applyFromHash();
}

/** @param {string} page */
export function navigate(page) {
  if (!PAGES.includes(page)) page = DEFAULT_PAGE;
  if (location.hash !== '#/' + page) {
    location.hash = '#/' + page;
    return; // hashchange triggers apply.
  }
  apply(page);
}

function applyFromHash() {
  const m = location.hash.match(/^#\/([a-z]+)/);
  const page = (m && PAGES.includes(m[1])) ? m[1] : DEFAULT_PAGE;
  apply(page);
}

function apply(page) {
  for (const el of document.querySelectorAll('.nav-item[data-page]')) {
    el.classList.toggle('active', el.getAttribute('data-page') === page);
  }
  for (const el of document.querySelectorAll('.page')) {
    el.classList.toggle('active', el.id === 'page-' + page);
  }
  try { onChange(page); } catch (e) { console.error('[router] onChange threw', e); }
}
