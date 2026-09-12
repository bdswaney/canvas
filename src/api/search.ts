import { apiRequest } from './client';

export type SearchResult = {
  documentId: string;
  projectId: string;
  projectName: string;
  name: string;
  version: number;
};

/**
 * Search only the latest saved artifact text. Live unsaved edits and documents
 * that have never been saved are intentionally outside this endpoint's scope.
 */
export async function searchDocuments(query: string): Promise<SearchResult[]> {
  const response = await apiRequest(`/api/search?q=${encodeURIComponent(query)}`);
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as { message?: string };
    throw new Error(body.message ?? `Request failed (${response.status})`);
  }
  return (await response.json()) as SearchResult[];
}
