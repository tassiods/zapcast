// Operator review only: cache a whitelist of safe DTO fields, never a captured payload.
import { api } from './api.js';
import { $, escHTML, confirmDialog } from './ui.js';
import { statusHTML } from './delivery-labels.js';
let items = [];
let epoch = 0;
let loadSequence = 0;
let loadError = '';
let closeModal = null;
const busy = new Set();
const errors = new Map();
const blockedRetry = new Set();
let refresh = loadDeliveryReview;
export async function cancelRecoveredCampaign(id, reload) {
  const current = epoch;
  if (!Number.isSafeInteger(id) || id <= 0 || busy.has('campaign-' + id)) return;
  const accepted = await confirmDialog({title:'Cancelar campanha',message:'Cancelar somente a campanha ' + id + '? Uma mensagem já aceita pelo provedor não pode ser retirada. Envios em andamento podem permanecer incertos.',danger:true});
  if (!accepted || current !== epoch) return;
  busy.add('campaign-' + id);
  try {
    await api('/api/broadcasts/' + id + '/cancel', 'POST', {});
    if (current === epoch) await reload();
  } catch (error) {
    if (current === epoch) {
      const el = $('campaignError-' + id);
      if (el) el.textContent = error instanceof Error ? error.message : String(error);
    }
  } finally { busy.delete('campaign-' + id); }
}
window.addEventListener('zapcast:session-cleared', () => {
  epoch++; loadSequence++; items = []; loadError = ''; errors.clear(); blockedRetry.clear(); busy.clear();
  closeModal?.(null);
  $('modalCancel')?.click();
  $('modalMsg')?.replaceChildren();
  $('deliveryReview')?.replaceChildren();
});
function linked(row) { return Number.isSafeInteger(row.messageRowId) && row.messageRowId > 0 && !row.noMessageRowId; }
function canConfirm(row) { return linked(row) && ['uncertain','dead','cancelled'].includes(row.status); }
function validMessageId(value) { return value.length > 0 && value.length <= 256 && !/[\s\x00-\x1f\x7f]/.test(value); }
function canRetry(row) {
  return ['uncertain','dead'].includes(row.status) &&
    ['active','interrupted','uncertain','completed'].includes(row.campaignStatus) && linked(row) && !row.noPayload && !blockedRetry.has(row.id);
}
function safeDTO(row) {
  return Object.fromEntries(['id','broadcastId','messageRowId','campaignStatus','contactName','phone','messageId','status','attempts','error','noMessageRowId','noPayload'].map(key => [key,row[key]]));
}
export async function loadDeliveryReview() {
  const current = epoch, sequence = ++loadSequence;
  try {
    const rows = await api('/api/delivery/review');
    if (current !== epoch || sequence !== loadSequence) return;
    loadError = '';
    items = (Array.isArray(rows) ? rows : []).slice(0,500).filter(row => Number.isSafeInteger(row.id) && row.id > 0).map(safeDTO);
    render();
  } catch (error) {
    if (current !== epoch || sequence !== loadSequence) return;
    items = [];
    loadError = 'Não foi possível carregar a revisão: ' + (error instanceof Error ? error.message : String(error));
    render();
  }
}
function render() {
  const el = $('deliveryReview');
  if (!el) return;
  if (loadError) { el.textContent = loadError; return; }
  el.innerHTML = '<h3>Revisão de entregas</h3><p>Pendentes inclui envios em andamento. A revisão mostra até 500 registros; estados legados não resolvidos permanecem no histórico. Repetir pode duplicar uma entrega; não há garantia de entrega exatamente uma vez.</p>' + items.map(row =>
    `<div class="delivery-review-row" data-review-id="${row.id}">
      <div>${escHTML(row.contactName || '')} — ${escHTML(row.phone || '')} · campanha ${escHTML(row.broadcastId)} · ${statusHTML(row.status)} · ${statusHTML(row.campaignStatus)} · tentativas: ${escHTML(row.attempts ?? '?')}</div>
      <p>${escHTML(row.error || '')}</p>
      ${!linked(row) || row.noPayload ? '<p>Não resolvido: vínculo ou conteúdo original indisponível.</p>' : ''}
      <button type="button" class="btn btn-secondary btn-sm" data-review-action="retry" ${canRetry(row) && !busy.has(row.id) ? '' : 'disabled'}>Revisar repetição</button>
      <button type="button" class="btn btn-secondary btn-sm" data-review-action="confirm_sent" ${canConfirm(row) && !busy.has(row.id) ? '' : 'disabled'}>Confirmar envio no provedor</button>
      <div class="delivery-review-error" role="alert">${escHTML(errors.get(row.id) || '')}</div>
    </div>`
  ).join('');
}
export function initDeliveryReview(reload = loadDeliveryReview) {
  refresh = reload;
  const root = $('deliveryReview');
  if (!root || root.dataset.wired) return;
  root.dataset.wired = 'true';
  root.addEventListener('click', async event => {
    const button = /** @type {HTMLButtonElement} */ (/** @type {Element} */ (event.target).closest('[data-review-action]'));
    if (!button || button.disabled) return;
    const container = button.closest('[data-review-id]');
    const id = Number(container?.getAttribute('data-review-id'));
    const row = items.find(item => item.id === id);
    const current = epoch;
    const action = button.dataset.reviewAction;
    const eligible = () => action === 'retry' ? canRetry(row) : action === 'confirm_sent' && canConfirm(row);
    if (!row || busy.has(id) || !eligible()) return;
    const body = await reviewDialog(row, action);
    if (!body || current !== epoch || !items.includes(row) || !eligible()) return;
    busy.add(id); button.disabled = true;
    try {
      await api('/api/delivery/' + id + '/resolve', 'POST', body);
      if (current !== epoch) return;
      errors.delete(id);
      await refresh(); // Authoritative readback; pending is never labelled sent.
    } catch (error) {
      if (current === epoch) {
        const message = (error.status ? 'HTTP ' + error.status + ': ' : '') + (error instanceof Error ? error.message : String(error));
        errors.set(id, message);
        if (error.status === 409) blockedRetry.add(id);
        render();
      }
    } finally {
      if (current === epoch) { busy.delete(id); render(); }
    }
  });
}
function reviewDialog(row, action) {
  const confirming = action === 'confirm_sent';
  closeModal?.(null);
  return new Promise(resolve => {
    const previous = document.activeElement;
    const backdrop = document.createElement('div');
    backdrop.id = 'deliveryReviewModal'; backdrop.className = 'modal-backdrop open';
    backdrop.innerHTML = `<div class="modal" role="dialog" aria-modal="true" aria-labelledby="deliveryReviewTitle" aria-describedby="deliveryReviewRisk">
      <h3 id="deliveryReviewTitle">${confirming ? 'Confirmar envio no provedor' : 'Revisar repetição'}</h3>
      <p>${escHTML(row.contactName || '')} — ${escHTML(row.phone || '')}</p>
      <p id="deliveryReviewRisk">O provedor pode já ter aceitado esta mensagem. Repetir pode causar entrega duplicada; não há garantia de entrega exatamente uma vez.</p>
      ${confirming ? '<label for="deliveryProviderMessageId">ID da mensagem confirmado pelo provedor (1–256 caracteres, sem espaços)</label><input id="deliveryProviderMessageId" type="text" maxlength="256" autocomplete="off" spellcheck="false"><label><input type="checkbox" id="deliveryProviderConfirmed"> Verifiquei no provedor que esta mensagem foi enviada.</label>' : '<label><input type="checkbox" id="deliveryDuplicateAck"> Reconheço o risco de entrega duplicada e autorizo esta repetição.</label>'}
      <div class="modal-actions"><button type="button" id="deliveryReviewDismiss" class="btn btn-secondary btn-sm">Voltar</button><button type="button" id="deliveryReviewSubmit" class="btn btn-danger btn-sm" disabled>${confirming ? 'Registrar confirmação' : 'Autorizar repetição'}</button></div>
    </div>`;
    document.body.append(backdrop);
    const ack = /** @type {HTMLInputElement} */ (backdrop.querySelector(confirming ? '#deliveryProviderConfirmed' : '#deliveryDuplicateAck'));
    const submit = /** @type {HTMLButtonElement} */ (backdrop.querySelector('#deliveryReviewSubmit'));
    const dismiss = /** @type {HTMLButtonElement} */ (backdrop.querySelector('#deliveryReviewDismiss'));
    const messageId = /** @type {HTMLInputElement|null} */ (backdrop.querySelector('#deliveryProviderMessageId'));
    const valid = () => ack.checked && (!confirming || validMessageId(messageId.value));
    let done = false;
    function finish(result) {
      if (done) return; done = true;
      document.removeEventListener('keydown', onKey, true);
      ack.checked = false; if (messageId) messageId.value = ''; backdrop.replaceChildren(); backdrop.remove(); closeModal = null;
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
      resolve(result);
    }
    function onKey(event) {
      if (event.key === 'Escape') { event.preventDefault(); event.stopImmediatePropagation(); finish(null); }
      if (event.key === 'Tab') {
        const controls = [messageId,ack,dismiss,submit].filter(control => control && !control.disabled);
        const index = controls.indexOf(/** @type {HTMLInputElement|HTMLButtonElement} */ (document.activeElement));
        controls[(index + (event.shiftKey ? -1 : 1) + controls.length) % controls.length].focus();
        event.preventDefault(); event.stopImmediatePropagation();
      }
    }
    const validate = () => { submit.disabled = !valid(); };
    ack.addEventListener('change', validate);
    messageId?.addEventListener('input', validate);
    submit.addEventListener('click', () => {
      if (!valid()) return;
      finish(confirming ? {action:'confirm_sent',providerConfirmed:true,messageId:messageId.value} : {action:'retry',acknowledgeDuplicateRisk:true});
    });
    dismiss.addEventListener('click', () => finish(null));
    backdrop.addEventListener('mousedown', event => { if (event.target === backdrop) finish(null); });
    document.addEventListener('keydown', onKey, true); closeModal = finish; dismiss.focus();
  });
}
