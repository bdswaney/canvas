import { useCallback, useEffect, useState } from 'react';
import { apiRequest } from './api';

export type Doc = {
  id: string;
  projectId: string;
  name: string;
  currentVersion: number;
  updatedAt: string;
  // Go encodes []byte as base64, so this is the base64 sha256 of the artifact
  // stored by the last save. Absent until a doc has been saved once.
  savedSha256?: string;
};

export type Version = {
  version: number;
  sha256: string;
  // authorId is the durable SessionUsers id; author is that id resolved to a
  // username for display, and is empty when it cannot be resolved.
  authorId: string;
  author: string;
  createdAt: string;
};

async function json<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await apiRequest(path, init);
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { message?: string };
    throw new Error(body.message ?? `Request failed (${response.status})`);
  }
  return (await response.json()) as T;
}

export const listDocs = () => json<Doc[]>('/api/docs');

export const getDoc = (id: string) => json<Doc>(`/api/docs/${id}`);

export const createDoc = (name: string) =>
  json<Doc>('/api/docs', { method: 'POST', body: JSON.stringify({ name }) });

export const listVersions = (id: string) => json<Version[]>(`/api/docs/${id}/versions`);

export const restoreVersion = (id: string, version: number) =>
  json<{ version: number; artifact: string }>(`/api/docs/${id}/restore/${version}`, {
    method: 'POST',
  });

/**
 * saveDoc commits one version. Both halves are sent because the server has no
 * Yjs: the artifact is what history stores and what people read, the snapshot
 * is the CRDT state it was taken from.
 */
export const saveDoc = (id: string, artifact: string, snapshot: Uint8Array) =>
  json<{ version: number; sha256: string }>(`/api/docs/${id}/save`, {
    method: 'POST',
    body: JSON.stringify({ artifact, snapshot: toBase64(snapshot) }),
  });

export function toBase64(bytes: Uint8Array): string {
  let binary = '';
  // Chunked so a large snapshot does not blow the argument limit.
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(binary);
}

export async function sha256Base64(text: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return toBase64(new Uint8Array(digest));
}

// useDoc loads a document's metadata and keeps the saved hash current, which
// is what the dirty indicator compares against.
export function useDoc(id: string) {
  const [doc, setDoc] = useState<Doc | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      setDoc(await getDoc(id));
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Could not load the document');
    }
  }, [id]);

  useEffect(() => {
    void refresh();
    // Someone else saving is invisible from here — the save goes over REST,
    // not the document socket — so pick it up when the tab is looked at again.
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

  return { doc, error, refresh, setDoc };
}
