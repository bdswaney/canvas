import { AppShell, Badge, Container, Group, Paper, Stack, Text, Textarea, Title } from '@mantine/core';
import { useSharedText, useSync } from './sync';

import type { Status } from './sync';

const statusColors: Record<Status, string> = {
  connected: 'green',
  connecting: 'yellow',
  disconnected: 'red',
};

export function App() {
  const { doc, room, status, synced, peers } = useSync();
  const [notes, setNotes] = useSharedText(doc, 'notes');

  return (
    <AppShell header={{ height: 64 }} padding="md">
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between">
          <Text fw={700} size="xl">Canvas</Text>
          <Group gap="xs">
            <Badge variant="light" color={statusColors[status]}>{status}</Badge>
            <Badge variant="light">{synced ? 'synced' : 'syncing'}</Badge>
            <Badge variant="light">{peers} connected</Badge>
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Main>
        <Container size="md" py="xl">
          <Paper withBorder p="xl" radius="md">
            <Stack>
              <Title order={1}>Room: {room}</Title>
              <Text c="dimmed">
                Shared state is live. Open this page in another tab, or over the
                tunnel, and edits below converge through the relay. Editing and
                previews come later.
              </Text>
              <Textarea
                label="Shared notes"
                description="Backed by a Yjs document and replayed from the server log on join."
                minRows={8}
                autosize
                value={notes}
                onChange={(event) => setNotes(event.currentTarget.value)}
              />
            </Stack>
          </Paper>
        </Container>
      </AppShell.Main>
    </AppShell>
  );
}
