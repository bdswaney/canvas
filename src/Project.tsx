import { useCallback, useState, type FormEvent } from 'react';
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
import { IconFileText, IconPlus } from '@tabler/icons-react';
import { Archive } from './Archive';
import { Members } from './Members';
import { archiveDoc, useRefetched } from './projects';

// One project: the documents in it and the people who can reach them.
export function Project({
  projectID,
  username,
  onOpenDoc,
}: {
  projectID: string;
  username: string;
  onOpenDoc: (doc: Doc) => void;
}) {
  const docs = useRefetched(useCallback(() => listDocs(projectID), [projectID]));

  return (
    <Container size="md" py="xl">
      <Stack gap="xl">
        <Stack>
          <Title order={2}>
            <Group gap="xs">
              <IconFileText size={22} stroke={1.5} />
              Documents
            </Group>
          </Title>
          {docs.error && (
            <Alert color="red" variant="light">
              {docs.error}
            </Alert>
          )}
          <Creator
            label="New document"
            placeholder="Release notes"
            create={async (name) => onOpenDoc(await createDoc(name, projectID))}
          />
          {docs.value === null ? (
            <Loader />
          ) : docs.value.length === 0 ? (
            <Text c="dimmed">No documents yet. Create the first one above.</Text>
          ) : (
            <SimpleGrid cols={{ base: 1, sm: 2 }}>
              {docs.value.map((doc) => (
                <Card key={doc.id} withBorder padding="md" radius="md">
                  <Stack gap={4}>
                    <Text fw={600}>{doc.name}</Text>
                    <Text size="sm" c="dimmed">
                      {doc.currentVersion === 0
                        ? 'Never saved'
                        : `Version ${doc.currentVersion} · saved ${new Date(doc.updatedAt).toLocaleString()}`}
                    </Text>
                    <Group gap="xs" mt="xs">
                      <Button variant="light" onClick={() => onOpenDoc(doc)}>
                        Open
                      </Button>
                      <Archive
                        what="document"
                        archive={() => archiveDoc(doc.id)}
                        done={docs.refresh}
                      />
                    </Group>
                  </Stack>
                </Card>
              ))}
            </SimpleGrid>
          )}
        </Stack>
        <Members projectID={projectID} me={username} />
      </Stack>
    </Container>
  );
}

// Creator is the name-and-a-button form that both lists above use. It reports
// its own failure rather than clearing the field, so a rejected name is still
// there to correct.
export function Creator({
  label,
  placeholder,
  create,
}: {
  label: string;
  placeholder: string;
  create: (name: string) => Promise<void>;
}) {
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await create(name.trim());
      setName('');
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : 'Could not create that');
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit}>
      <Stack gap="xs">
        <Group align="flex-end">
          <TextInput
            label={label}
            placeholder={placeholder}
            value={name}
            onChange={(event) => setName(event.currentTarget.value)}
            style={{ flex: 1 }}
          />
          <Button
            type="submit"
            loading={busy}
            disabled={name.trim() === ''}
            leftSection={<IconPlus size={16} stroke={1.5} />}
          >
            Create
          </Button>
        </Group>
        {error && (
          <Alert color="red" variant="light">
            {error}
          </Alert>
        )}
      </Stack>
    </form>
  );
}
