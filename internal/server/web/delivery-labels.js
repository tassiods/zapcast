// Fixed presentation classes; never interpolate API status into attributes.
import { getLang } from './i18n.js';
import { escHTML } from './ui.js';
const labels = {
  'pt': {pending:'Pendente', inflight:'Em envio', uncertain:'Incerto', cancelled:'Cancelada', interrupted:'Interrompida', unresolved:'Não resolvido', legacy:'Legado — revisão necessária', completed:'Concluída', active:'Ativa', preparing:'Preparando', dead:'Falha definitiva', failed:'Falha definitiva', sent:'Enviada', delivered:'Entregue', read:'Lida', unknown:'Estado desconhecido', pendingCount:'Pendentes', cancelledCount:'Cancelados', unresolvedCount:'Não resolvidos'},
  'en': {pending:'Pending', inflight:'In flight', uncertain:'Uncertain', cancelled:'Cancelled', interrupted:'Interrupted', unresolved:'Unresolved', legacy:'Legacy — review required', completed:'Completed', active:'Active', preparing:'Preparing', dead:'Definitive failure', failed:'Definitive failure', sent:'Sent', delivered:'Delivered', read:'Read', unknown:'Unknown state', pendingCount:'Pending', cancelledCount:'Cancelled', unresolvedCount:'Unresolved'},
  'es': {pending:'Pendiente', inflight:'En envío', uncertain:'Incierto', cancelled:'Cancelada', interrupted:'Interrumpida', unresolved:'Sin resolver', legacy:'Legado — requiere revisión', completed:'Completada', active:'Activa', preparing:'Preparando', dead:'Fallo definitivo', failed:'Fallo definitivo', sent:'Enviada', delivered:'Entregada', read:'Leída', unknown:'Estado desconocido', pendingCount:'Pendientes', cancelledCount:'Cancelados', unresolvedCount:'Sin resolver'},
};
export function deliveryLabel(key) { return (labels[getLang().slice(0,2)] || labels.en)[key] || labels.en.unknown; }
export function statusClass(status) {
  return ['pending','inflight','uncertain','cancelled','interrupted','failed','sent','delivered','read','dead','preparing','completed','active','legacy'].includes(status) ? 'badge-' + status : 'badge-unknown';
}
export function statusHTML(status) { return `<span class="badge-status ${statusClass(status)}">${escHTML(deliveryLabel(status))}</span>`; }
export function campaignSummary(b) {
  const count = value => Number.isSafeInteger(value) && value >= 0 ? String(value) : ({pt:'desconhecido',es:'desconocido'}[getLang().slice(0,2)] || 'unknown');
  const inPending = {pt:'incluído em pendentes',es:'incluido en pendientes'}[getLang().slice(0,2)] || 'included in pending';
  return statusHTML(b.status) + ' · ' + [
    ['pendingCount', b.pending], ['inflight', b.inflight ?? inPending], ['uncertain', b.uncertain],
    ['cancelledCount', b.cancelled], ['unresolvedCount', b.unresolved],
  ].map(([label,value]) => escHTML(deliveryLabel(label) + ': ' + (value === inPending ? value : count(value)))).join(' · ');
}
