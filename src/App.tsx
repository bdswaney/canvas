import { useRef } from 'react';
import { AppShell, Badge, Container, Group, Paper, Stack, Text, Title } from '@mantine/core';
import { Cursors } from './Cursors';
import { Editor } from './Editor';
import { usePresence } from './presence';
import { useSharedText, useSync, type Status } from './sync';

const statusColors: Record<Status, string> = {
  connected: 'green',
  connecting: 'yellow',
  disconnected: 'red',
};

export function App() {
  const { doc, awareness, room, status, synced, peers } = useSync();
  const notes = useSharedText(doc, 'notes');
  // Pointer positions are relative to this element, so every client agrees on
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
                Open this page in another tab, or over the tunnel, and everything
                is shared: the text, each other's carets and selections, and the
                pointers moving over this panel.
              </Text>
              <Editor text={notes} awareness={awareness} />
            </Stack>
          </Paper>
        </Container>
      </AppShell.Main>
    </AppShell>
  );
}
