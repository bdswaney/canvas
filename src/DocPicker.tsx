import { useEffect, useState, type FormEvent } from 'react';
import {
  Alert,
  Button,
  Card,
  Container,
  Group,
  Loader,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { createDoc, listDocs, type Doc } from './docs';

// Documents live in one seeded project until projects get their own screen.
export function DocPicker({ onOpen }: { onOpen: (doc: Doc) => void }) {
  const [docs, setDocs] = useState<Doc[] | null>(null);
  const [name, setName] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listDocs()
      .then(setDocs)
      .catch((failure: unknown) =>
        setError(failure instanceof Error ? failure.message : 'Could not list documents'),
      );
  }, []);

  const create = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      onOpen(await createDoc(name.trim()));
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Could not create the document');
      setBusy(false);
    }
  };

  return (
    <Container size="md" py="xl">
      <Stack>
        <Title order={2}>Documents</Title>
        {error && (
          <Alert color="red" variant="light">
            {error}
          </Alert>
        )}
        <form onSubmit={create}>
          <Group align="flex-end">
            <TextInput
              label="New document"
              placeholder="Release notes"
              value={name}
              onChange={(event) => setName(event.currentTarget.value)}
              style={{ flex: 1 }}
            />
            <Button type="submit" loading={busy} disabled={name.trim() === ''}>
              Create
            </Button>
          </Group>
        </form>

        {docs === null ? (
          <Loader />
        ) : docs.length === 0 ? (
          <Text c="dimmed">No documents yet. Create the first one above.</Text>
        ) : (
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            {docs.map((doc) => (
              <Card key={doc.id} withBorder padding="md" radius="md">
                <Stack gap={4}>
                  <Text fw={600}>{doc.name}</Text>
                  <Text size="sm" c="dimmed">
                    {doc.currentVersion === 0
                      ? 'Never saved'
                      : `Version ${doc.currentVersion} · saved ${new Date(doc.updatedAt).toLocaleString()}`}
                  </Text>
                  <Button variant="light" mt="xs" onClick={() => onOpen(doc)}>
                    Open
                  </Button>
                </Stack>
              </Card>
            ))}
          </SimpleGrid>
        )}
      </Stack>
    </Container>
  );
}
