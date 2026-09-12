import { useState, type FormEvent } from 'react';
import { Alert, Anchor, Badge, Button, Card, Container, Group, Loader, Stack, Text, TextInput, Title } from '@mantine/core';
import { searchDocuments, type SearchResult } from '../api/search';
import { docPath, navigate } from '../app/routes';

// Search is intentionally a saved-artifact surface. It does not merge a live
// journal just to make a result, so its behavior agrees with the HTTP and MCP
// contracts and remains bounded by the database query.
export function Search() {
  const [query, setQuery] = useState('');
  const [searched, setSearched] = useState<string | null>(null);
  const [results, setResults] = useState<SearchResult[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (query === '') return;
    setBusy(true);
    setError(null);
    try {
      setResults(await searchDocuments(query));
      setSearched(query);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Could not search documents');
      setResults(null);
      setSearched(query);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Container size="md" py="xl">
      <Stack gap="xl">
        <Stack gap="xs">
          <Title order={2}>Search documents</Title>
          <Text c="dimmed" size="sm">
            Searches the latest saved document text. Unsaved edits and documents that have never
            been saved are not included.
          </Text>
        </Stack>
        <form onSubmit={submit}>
          <Group align="flex-end">
            <TextInput
              label="Search text"
              placeholder="Find a phrase"
              value={query}
              onChange={(event) => setQuery(event.currentTarget.value)}
              style={{ flex: 1 }}
              autoFocus
            />
            <Button type="submit" loading={busy} disabled={query === ''}>
              Search
            </Button>
          </Group>
        </form>
        {error && <Alert color="red" variant="light">{error}</Alert>}
        {results === null && busy && <Loader />}
        {results !== null && results.length === 0 && (
          <Text c="dimmed">No documents matched{searched === null ? '' : ` “${searched}”`}.</Text>
        )}
        {results !== null && results.length > 0 && (
          <Stack>
            {results.map((result) => (
              <Card key={result.documentId} withBorder padding="md" radius="md">
                <Stack gap={4}>
                  <Anchor
                    fw={600}
                    href={docPath(result.documentId)}
                    onClick={(event) => {
                      if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
                      event.preventDefault();
                      navigate(docPath(result.documentId));
                    }}
                  >
                    {result.name}
                  </Anchor>
                  <Text size="sm" c="dimmed">{result.projectName}</Text>
                  <Badge variant="light" w="fit-content">Saved version {result.version}</Badge>
                </Stack>
              </Card>
            ))}
          </Stack>
        )}
      </Stack>
    </Container>
  );
}
