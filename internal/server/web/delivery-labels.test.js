import { it, expect, vi } from 'vitest';
const language=vi.hoisted(()=>({value:'pt-BR'}));
vi.mock('./i18n.js',()=>({getLang:()=>language.value}));
import { campaignSummary, statusClass, statusHTML } from './delivery-labels.js';
it('missing inflight subtotal is explicitly included in pending, never invented as zero',()=>{
  language.value='pt-BR';
  expect(campaignSummary({status:'legacy',pending:2,uncertain:1,cancelled:0,unresolved:3})).toContain('Em envio: incluído em pendentes');
  expect(campaignSummary({status:'legacy',pending:2,uncertain:1,cancelled:0,unresolved:3})).toContain('Não resolvidos: 3');
  expect(campaignSummary({})).toContain('desconhecido');
});
it('whitelists every CSS status and renders human-readable statuses in each supported locale',()=>{
  expect(statusClass('uncertain" onclick="evil')).toBe('badge-unknown');
  for(const [lang,text] of [['pt-BR','Interrompida'],['en-US','Interrupted'],['es-ES','Interrumpida']]) {
    language.value=lang;expect(statusHTML('interrupted')).toContain(text);
  }
});
