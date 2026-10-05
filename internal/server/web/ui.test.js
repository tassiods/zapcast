// ui.test.js — escape helpers and the phone validator. Pure functions, so
// no DOM scaffolding needed beyond Vitest's default jsdom env.

import { describe, it, expect } from 'vitest';
import { escHTML, escAttr, humanSize, validatePhoneInline, fold, formatDateTime } from './ui.js';

describe('escHTML / escAttr', () => {
  it('escapes the five XSS-relevant characters', () => {
    expect(escHTML('<script>"&\'</script>')).toBe(
      '&lt;script&gt;&quot;&amp;&#39;&lt;/script&gt;',
    );
  });

  it('escAttr matches escHTML (same escape table)', () => {
    expect(escAttr('a"b<c>d')).toBe(escHTML('a"b<c>d'));
  });

  it('coerces non-strings to string before escaping', () => {
    expect(escHTML(42)).toBe('42');
    expect(escHTML(null)).toBe('null');
  });
});

describe('humanSize', () => {
  it('reports raw bytes under 1 KiB', () => {
    expect(humanSize(512)).toBe('512 B');
  });
  it('reports KB with one decimal place', () => {
    expect(humanSize(2048)).toBe('2.0 KB');
  });
  it('reports MB with one decimal place', () => {
    expect(humanSize(2 * 1024 * 1024)).toBe('2.0 MB');
  });
});

describe('validatePhoneInline', () => {
  it('accepts a full E.164 mobile and returns the digits', () => {
    expect(validatePhoneInline('+55 11 99999-8888')).toEqual({
      ok: true,
      digits: '5511999998888',
    });
  });

  it('rejects empty input', () => {
    expect(validatePhoneInline('')).toMatchObject({ ok: false });
  });

  it('rejects numbers shorter than 7 digits', () => {
    expect(validatePhoneInline('12345')).toMatchObject({ ok: false });
  });

  it('rejects numbers longer than 15 digits', () => {
    expect(validatePhoneInline('1234567890123456')).toMatchObject({ ok: false });
  });

  it('rejects numbers starting with 0', () => {
    expect(validatePhoneInline('05511999998888')).toMatchObject({ ok: false });
  });
});

describe('fold', () => {
  it('strips accents so Jose matches José', () => {
    expect(fold('José')).toBe('jose');
    expect(fold('Jose')).toBe('jose');
  });

  it('folds case and cedilla', () => {
    expect(fold('AVENIDA CENTRAL Ç')).toBe('avenida central c');
  });

  it('is null-safe', () => {
    expect(fold(null)).toBe('');
    expect(fold(undefined)).toBe('');
    expect(fold('')).toBe('');
  });
});

describe('formatDateTime', () => {
  it('renders UTC ISO in local time, not raw', () => {
    const out = formatDateTime('2026-10-05T13:38:01Z');
    expect(out).not.toContain('T');
    expect(out).toMatch(/05\/10\/2026|10\/05\/2026|2026/);
  });

  it('passes garbage through untouched', () => {
    expect(formatDateTime('not-a-date')).toBe('not-a-date');
    expect(formatDateTime(null)).toBe('');
  });
});
