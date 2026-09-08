import { useRef } from 'react';
import { AppShell, Badge, Container, Group, Paper, Stack, Text, Textarea, Title } from '@mantine/core';
import { Cursors } from './Cursors';
import { usePresence } from './presence';
import { useSharedText, useSync, type Status } from './sync';

const statusColors: Record<Status, string> = {
  connected: 'green',
  connecting: 'yellow',
  disconnected: 'red',
};

export function App() {
  const { doc, awareness, room, status, synced, peers } = useSync();
  const [notes, setNotes] = useSharedText(doc, 'notes');
  // Cursor positions are relative to this element, so every client agrees on
  // where a pointer is regardless of window size.
  const surface = useRef<HTMLDivElement>(null);
  const present = usePresence(awareness, surface);

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
          <Paper withBorder p="xl" radius="md" pos="relative" ref={surface}>
            <Cursors peers={present} />
            <Stack>
              <Title order={1}>Room: {room}</Title>
              <Text c="dimmed">
                Shared state is live. Open this page in another tab, or over the
                tunnel, and edits below converge through the relay. Pointers are
                shared too: move your mouse over this panel.
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
