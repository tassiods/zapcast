// i18n.js — translations loader + DOM application.
//
// The backend ships JSON dictionaries keyed by Accept-Language. We fetch once
// at boot and on every language switch; the cookie persists the user choice.

/** @type {Record<string, string>} */
let table = {};
let current = 'pt-BR';

/** @returns {string} The active language tag. */
export function getLang() { return current; }

/**
 * Lookup helper. Falls back to the key when no translation matches — keeps
 * the UI legible during dictionary churn.
 *
 * @param {string} key
 * @returns {string}
 */
export function t(key) {
  return table[key] || key;
}

/**
 * Fetch the dictionary for `lang` and apply it to the document.
 *
 * @param {string} lang
 */
export async function loadTranslations(lang) {
  try {
    const res = await fetch('/api/translations', { headers: { 'Accept-Language': lang || 'en-US' } });
    table = await res.json();
    current = lang || 'en-US';
    document.documentElement.lang = current;
    applyTranslations();
    syncLanguageSelector();
  } catch (e) {
    console.error('[i18n] failed to load translations', e);
  }
}

/**
 * Persist the user's choice (cookie + state) and reload the dictionary.
 *
 * @param {string} lang
 */
export async function switchLanguage(lang) {
  document.cookie = 'lang=' + lang + '; path=/; max-age=31536000';
  await loadTranslations(lang);
}

/**
 * Replace text / placeholders / meta content for every `data-i18n*` node.
 * Cheap enough to re-run on every dictionary swap.
 */
export function applyTranslations() {
  // Skip replacement when no entry exists for the key — keeps the inline
  // Portuguese default in the markup as a graceful fallback instead of
  // surfacing the raw key.
  for (const el of document.querySelectorAll('[data-i18n]')) {
    const key = el.getAttribute('data-i18n');
    if (!key) continue;
    const v = t(key);
    if (v !== key) el.textContent = v;
  }
  for (const el of document.querySelectorAll('[data-i18n-placeholder]')) {
    const key = el.getAttribute('data-i18n-placeholder');
    if (!key) continue;
    const v = t(key);
    if (v !== key) /** @type {HTMLInputElement} */ (el).placeholder = v;
  }
  for (const el of document.querySelectorAll('[data-i18n-content]')) {
    const key = el.getAttribute('data-i18n-content');
    if (!key) continue;
    const v = t(key);
    if (v !== key) el.setAttribute('content', v);
  }
  const title = t('ui_title');
  if (title !== 'ui_title') document.title = title;
}

function syncLanguageSelector() {
  const selector = /** @type {HTMLSelectElement|null} */ (document.getElementById('langSelector'));
  if (selector) selector.value = current;
}

/**
 * Pick the boot language from cookie → navigator → fallback.
 */
export function detectBootLang() {
  const match = document.cookie.match(/lang=([^;]+)/);
  return match ? match[1] : (navigator.language || 'en-US');
}
