// api.test.js — exercises the fetch wrapper, the structured-error parser
// and the safeApi fallback. Network is stubbed via global fetch mocks.

import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import {
  api,
  safeApi,
  getAuthToken,
  setAuthToken,
  clearAuthToken,
  withAuthQuery,
  setUnauthorizedHandler,
} from './api.js';

const originalFetch = globalThis.fetch;

beforeEach(() => {
  // Clean state per test: token + 401 handler.
  clearAuthToken();
  setUnauthorizedHandler(() => {});
});

afterEach(() => {
  globalThis.fetch = originalFetch;
});

function mockFetch(response) {
  globalThis.fetch = vi.fn(() => Promise.resolve(response));
}

function jsonResponse(body, { status = 200, statusText = 'OK' } = {}) {
  return new Response(JSON.stringify(body), {
    status,
    statusText,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('auth-token storage', () => {
  it('reads and writes through sessionStorage', () => {
    setAuthToken('alpha');
    expect(getAuthToken()).toBe('alpha');
    clearAuthToken();
    expect(getAuthToken()).toBe('');
  });

  it('returns "" when no token is stored', () => {
    expect(getAuthToken()).toBe('');
  });
});

describe('withAuthQuery', () => {
  it('passes the URL through unchanged when no token is set', () => {
    expect(withAuthQuery('/api/x')).toBe('/api/x');
  });

  it('appends ?token= when no query string is present', () => {
    setAuthToken('abc');
    expect(withAuthQuery('/api/x')).toBe('/api/x?token=abc');
  });

  it('appends &token= when the URL already carries a query string', () => {
    setAuthToken('abc');
    expect(withAuthQuery('/api/x?y=1')).toBe('/api/x?y=1&token=abc');
  });

  it('URL-encodes tokens so reserved characters survive the trip', () => {
    setAuthToken('a b/c');
    expect(withAuthQuery('/api/x')).toBe('/api/x?token=a%20b%2Fc');
  });
});

describe('api()', () => {
  it('returns the decoded JSON payload on success', async () => {
    mockFetch(jsonResponse({ hello: 'world' }));
    await expect(api('/api/x')).resolves.toEqual({ hello: 'world' });
  });

  it('attaches Authorization when a token is set', async () => {
    setAuthToken('bearer-x');
    mockFetch(jsonResponse({ ok: true }));
    await api('/api/x');
    const fetchMock = /** @type {import('vitest').Mock} */ (/** @type {unknown} */ (globalThis.fetch));
    const [, opts] = fetchMock.mock.calls[0];
    expect(opts.headers.Authorization).toBe('Bearer bearer-x');
  });

  it('throws an HttpError with .code from a structured backend error', async () => {
    mockFetch(jsonResponse(
      { error: { code: 'err_phone_too_short', message: 'phone too short (4)' } },
      { status: 400 },
    ));
    await expect(api('/api/contacts', 'POST', { phone: '123' }))
      .rejects.toMatchObject({
        status: 400,
        code: 'err_phone_too_short',
        message: 'phone too short (4)',
      });
  });

  it('falls back to the legacy string-error shape', async () => {
    mockFetch(jsonResponse({ error: 'legacy message' }, { status: 422 }));
    await expect(api('/api/x')).rejects.toMatchObject({
      status: 422,
      message: 'legacy message',
      code: '',
    });
  });

  it('invokes the 401 handler and throws a 401 HttpError', async () => {
    const handler = vi.fn();
    setUnauthorizedHandler(handler);
    mockFetch(new Response('', { status: 401 }));
    await expect(api('/api/x')).rejects.toMatchObject({ status: 401 });
    expect(handler).toHaveBeenCalledOnce();
  });

  it('serialises the body when one is provided', async () => {
    mockFetch(jsonResponse({}));
    await api('/api/x', 'POST', { a: 1 });
    const fetchMock = /** @type {import('vitest').Mock} */ (/** @type {unknown} */ (globalThis.fetch));
    const [, opts] = fetchMock.mock.calls[0];
    expect(opts.body).toBe('{"a":1}');
  });
});

describe('safeApi()', () => {
  it('returns the fallback when api() throws', async () => {
    mockFetch(jsonResponse({ error: 'boom' }, { status: 500 }));
    await expect(safeApi('/api/x', [])).resolves.toEqual([]);
  });

  it('returns the fallback when api() resolves to null', async () => {
    mockFetch(jsonResponse(null));
    await expect(safeApi('/api/x', { ok: true })).resolves.toEqual({ ok: true });
  });

  it('returns the payload on success', async () => {
    mockFetch(jsonResponse({ items: [1, 2] }));
    await expect(safeApi('/api/x', { items: [] })).resolves.toEqual({ items: [1, 2] });
  });
});
