import { useCallback, useEffect, useState } from 'react';
import { apiRequest } from './api';

export type Project = {
  id: string;
  name: string;
  createdAt: string;
};

export type Member = {
  // userId is the durable SessionUsers id; username is that id resolved for
  // display, and is empty when it cannot be resolved.
  userId: string;
  username: string;
  addedAt?: string;
};

async function json<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await apiRequest(path, init);
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { message?: string };
    throw new Error(body.message ?? `Request failed (${response.status})`);
  }
  return (await response.json()) as T;
}

async function send(path: string, init: RequestInit): Promise<void> {
  const response = await apiRequest(path, init);
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { message?: string };
    throw new Error(body.message ?? `Request failed (${response.status})`);
  }
}

export const listProjects = () => json<Project[]>('/api/projects');

export const createProject = (name: string) =>
  json<Project>('/api/projects', { method: 'POST', body: JSON.stringify({ name }) });

/** listUsers backs the member picker: every account that can be added. */
export const listUsers = () => json<Member[]>('/api/users');

export const listMembers = (projectID: string) =>
  json<Member[]>(`/api/projects/${projectID}/members`);

export const addMember = (projectID: string, userID: string) =>
  send(`/api/projects/${projectID}/members`, {
    method: 'POST',
    body: JSON.stringify({ userId: userID }),
  });

/**
 * Archiving hides something and deletes nothing. A document's saved versions
 * survive it, because history is the one layer of Canvas that cannot be
 * rebuilt. There is no un-archive yet.
 */
export const archiveProject = (projectID: string) =>
  send(`/api/projects/${projectID}`, { method: 'DELETE' });

export const archiveDoc = (docID: string) => send(`/api/docs/${docID}`, { method: 'DELETE' });

export const removeMember = (projectID: string, userID: string) =>
  send(`/api/projects/${projectID}/members/${userID}`, { method: 'DELETE' });

/**
 * useRefetched loads a value and reloads it when the tab is looked at again.
 * Someone else creating, archiving, or sharing a document happens over REST
 * with no socket to announce it — the same reason useDoc watches focus for
 * saves made elsewhere.
 */
export function useRefetched<T>(load: () => Promise<T>) {
  const [value, setValue] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setValue(await load());
      setError(null);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Could not load');
    }
  }, [load]);

  useEffect(() => {
    void refresh();
    const onVisible = () => {
      if (document.visibilityState === 'visible') void refresh();
    };
    window.addEventListener('focus', onVisible);
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      window.removeEventListener('focus', onVisible);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [refresh]);

  return { value, error, refresh };
}
