import { useCallback, useEffect, useState } from 'react';
import { apiRequest, errorMessage } from './client';

export type Session = { authenticated: boolean; username: string };

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

// A session expires after ten minutes without an HTTP request, and it is
// refreshed only by HTTP. Someone typing over a WebSocket makes no requests at
// all, so without this their session dies mid-edit. The server rate-limits the
// refresh write, which makes this close to free.
const keepaliveMs = 2 * 60 * 1000;

export type SessionState = {
  session: Session | null; // null until the first check completes
  refresh: () => Promise<void>;
  signOut: () => Promise<void>;
};

export function useSession(): SessionState {
  const [session, setSession] = useState<Session | null>(null);

  const refresh = useCallback(async () => {
    setSession(await fetchSession());
  }, []);

  // The first call also primes the XSRF cookie, so later writes are not met
  // with a redirect that replays their body.
  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (!session?.authenticated) return;
    const timer = setInterval(() => void refresh(), keepaliveMs);
    // Coming back to a backgrounded tab is exactly when a session is most
    // likely to have lapsed.
    const onVisible = () => {
      if (document.visibilityState === 'visible') void refresh();
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [session?.authenticated, refresh]);

  const signOut = useCallback(async () => {
    try {
      await logout();
    } catch {
      // A refused sign-out usually means the session is already gone; either
      // way the next check settles it, so never leave the button dead.
    }
    await refresh();
  }, [refresh]);

  return { session, refresh, signOut };
}
