// view-import.js — CSV import preview + export.

import { api, apiBlob } from './api.js';
import { $, escHTML, show, toast, toastErr, summaryCard } from './ui.js';
import { t } from './i18n.js';

/** @type {string|null} */
let pendingCSV = null;
let importEpoch = 0;
window.addEventListener('zapcast:session-cleared', () => { pendingCSV = null; importEpoch++; });

let onAfterImport = () => {};

/** Re-run after import completes so the contacts/groups caches refresh. */
export function setOnAfterImport(fn) { onAfterImport = fn; }

// ─── Drop / file pick ──────────────────────────────────────────

function handleFile(file) {
  if (!file) return;
  const epoch = importEpoch;
  const reader = new FileReader();
  reader.onload = (e) => {
    if(epoch !== importEpoch) return;
    const text = /** @type {string} */ (e.target?.result);
    const lines = text.trim().split('\n');
    if (lines.length < 2) { toast('err_csv_empty', 'error'); return; }

    pendingCSV = text;
    $('importPreviewCount').textContent = t('msg_csv_lines_found').replace('%d', String(lines.length - 1));
    $('importPreviewTable').innerHTML = previewTable(lines);
    show($('importPreview'), true);
    show($('importResult'), false);
  };
  reader.readAsText(file);
}

function previewTable(lines) {
  const rows = lines.slice(0, 11); // header + 10
  let table = '<table style="width:100%;font-size:0.82rem;border-collapse:collapse">';
  rows.forEach((row, i) => {
    const cols = splitCSVLine(row);
    const tag = i === 0 ? 'th' : 'td';
    const style = i === 0 ? 'background:var(--surface3);font-weight:600;' : '';
    table += '<tr>' + cols
      .map((c) => `<${tag} style="${style}padding:6px 10px;border-bottom:1px solid var(--border);color:var(--text-dim)">${escHTML(c)}</${tag}>`)
      .join('') + '</tr>';
  });
  if (lines.length > 11) {
    table += `<tr><td colspan="10" style="padding:8px;text-align:center;color:var(--text-muted)">... e mais ${lines.length - 11} linhas</td></tr>`;
  }
  table += '</table>';
  return table;
}

/**
 * Quote-aware single-line CSV splitter (RFC 4180 basics: "..." quoting with
 * "" escape). The old naive split(',') desynced the preview on names like
 * "Silva, João" while the backend (encoding/csv) parsed them correctly —
 * the preview must agree with what the import will actually do.
 *
 * Exported for the Vitest suite.
 *
 * @param {string} line
 * @returns {string[]}
 */
export function splitCSVLine(line) {
  const out = [];
  let cur = '';
  let inQuotes = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (inQuotes) {
      if (ch === '"') {
        if (line[i + 1] === '"') { cur += '"'; i++; }
        else inQuotes = false;
      } else {
        cur += ch;
      }
    } else if (ch === '"') {
      inQuotes = true;
    } else if (ch === ',') {
      out.push(cur.trim());
      cur = '';
    } else {
      cur += ch;
    }
  }
  out.push(cur.trim());
  return out;
}

function cancel() {
  pendingCSV = null;
  show($('importPreview'), false);
  /** @type {HTMLInputElement} */ ($('importFile')).value = '';
}

async function doImport() {
  if (!pendingCSV) return;
  const btn = /** @type {HTMLButtonElement} */ ($('importBtn'));
  btn.disabled = true;
  btn.innerHTML = '<span class="spinner"></span> ' + t('msg_import_loading');

  try {
    const result = await api('/api/import', 'POST', { csv: pendingCSV });
    if (result.error) { toast(result.error, 'error'); return; }

    show($('importPreview'), false);
    show($('importResult'), true);
    const created = result.created || 0;
    const updated = result.updated || 0;
    const skipped = result.skipped || 0;
    const groupsCreated = result.groupsCreated || 0;

    $('importSummary').innerHTML =
      summaryCard(created, t('msg_new'), 'green') +
      summaryCard(updated, t('msg_updated'), 'accent') +
      summaryCard(skipped, t('msg_skipped'), 'yellow');
    $('importGroupsNote').textContent = groupsCreated > 0
      ? t('msg_groups_created').replace('%d', groupsCreated)
      : '';

    toast(t('msg_import_done').replace('%d', created).replace('%d', updated), 'success');
    await onAfterImport();
    pendingCSV = null;
  } catch (e) {
    toastErr(e);
  } finally {
    btn.disabled = false;
    btn.innerHTML = '&#x1F4E5; ' + t('ui_import_csv');
  }
}

// ─── Export ────────────────────────────────────────────────────

async function exportCSV() {
  try {
    const blob = await apiBlob('/api/contacts/export');
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `zapcast-contacts-${new Date().toISOString().slice(0, 10)}.csv`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
    toast('ui_export_csv', 'success');
  } catch (e) {
    toastErr(e);
  }
}

// ─── Wiring ────────────────────────────────────────────────────

export function initImportView() {
  $('importBtn').addEventListener('click', doImport);
  $('importCancelBtn').addEventListener('click', cancel);
  $('exportContactsBtn').addEventListener('click', exportCSV);

  const drop = $('importDrop');
  const file = /** @type {HTMLInputElement} */ ($('importFile'));

  drop.addEventListener('click', () => file.click());
  drop.addEventListener('dragover', (e) => { e.preventDefault(); drop.classList.add('dragover'); });
  drop.addEventListener('dragleave', () => drop.classList.remove('dragover'));
  drop.addEventListener('drop', (e) => {
    e.preventDefault();
    drop.classList.remove('dragover');
    handleFile(e.dataTransfer?.files?.[0]);
  });
  file.addEventListener('change', () => handleFile(file.files?.[0]));
}
