// The session cookie is SameSite=Strict and the XSRF token round-trips as a
// cookie the client copies into a header. Names are the session package's
// defaults, which match the convention most front ends already use.
const xsrfCookieName = 'XSRF-TOKEN';
const xsrfHeaderName = 'X-XSRF-TOKEN';

export type Session = { authenticated: boolean; username: string };

function readCookie(name: string): string | null {
  const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`));
  return match ? decodeURIComponent(match[1]) : null;
}

/**
 * request wraps fetch with the two things every API call here needs: cookies,
 * and the XSRF header on anything that is not a plain read. A request without
 * that header is answered with a 307 that re-sends the body, so the app calls
 * GET /api/session once at startup to make sure the cookie exists.
 */
export async function apiRequest(path: string, init: RequestInit = {}): Promise<Response> {
  const method = (init.method ?? 'GET').toUpperCase();
  const send = () => {
    const headers = new Headers(init.headers);
    if (method !== 'GET' && method !== 'HEAD') {
      const token = readCookie(xsrfCookieName);
      if (token) headers.set(xsrfHeaderName, token);
      headers.set('Content-Type', 'application/json');
    }
    // Refusing to follow redirects is deliberate: a write with no XSRF cookie
    // is answered with a 307, and fetch would replay the whole body.
    return fetch(path, { ...init, headers, credentials: 'same-origin', redirect: 'error' });
  };

  try {
    return await send();
  } catch (failure) {
    if (method === 'GET' || method === 'HEAD') throw failure;
    // The XSRF cookie lapsed. Reissue it and try the write once more, rather
    // than leaving the caller with an opaque network error.
    await fetch('/api/session', { credentials: 'same-origin' });
    return send();
  }
}

// The server reports failures as {message, traceID}; fall back to the status.
async function errorMessage(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as { message?: string };
    if (body.message) return body.message;
  } catch {
    // A non-JSON error body is not worth surfacing verbatim.
  }
  return `Request failed (${response.status})`;
}

export async function fetchSession(): Promise<Session> {
  const response = await apiRequest('/api/session');
  if (!response.ok) return { authenticated: false, username: '' };
  return (await response.json()) as Session;
}

export async function login(username: string, password: string): Promise<void> {
  const response = await apiRequest('/api/session', {
    method: 'POST',
    body: JSON.stringify({ username, password }),
  });
  if (!response.ok) throw new Error(await errorMessage(response));
}

export async function logout(): Promise<void> {
  const response = await apiRequest('/api/session', { method: 'DELETE' });
  if (!response.ok) throw new Error(await errorMessage(response));
}
