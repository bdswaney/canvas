import { useCallback, useRef } from 'react';
import {
  ActionIcon,
  AppShell,
  Badge,
  Center,
  Container,
  Divider,
  Group,
  Loader,
  Paper,
  SimpleGrid,
  Stack,
  Text,
  Title,
  Tooltip,
  useComputedColorScheme,
} from '@mantine/core';
import { ColorSchemeToggle } from './ColorSchemeToggle';
import { Cursors } from './Cursors';
import { Editor } from './Editor';
import { Login } from './Login';
import { Preview } from './Preview';
import { usePresence } from './presence';
import { useSession } from './useSession';
import { useSharedText, useSync, useTextSnapshot, type Status } from './sync';

const statusColors: Record<Status, string> = {
  connected: 'green',
  connecting: 'yellow',
  disconnected: 'red',
};

export function App() {
  const { session, refresh, signOut } = useSession();

  if (session === null) {
    return (
      <Center mih="100vh">
        <Loader />
      </Center>
    );
  }

  if (!session.authenticated) {
    return <Login onSignedIn={refresh} />;
  }

  // Keyed on the username so a new sign-in gets a fresh document and socket
  // rather than ones that outlived the session that opened them.
  return (
    <Workspace
      key={session.username}
      username={session.username}
      onSignedOut={refresh}
      signOut={signOut}
    />
  );
}

function Workspace({
  username,
  onSignedOut,
  signOut,
}: {
  username: string;
  onSignedOut: () => Promise<void>;
  signOut: () => Promise<void>;
}) {
  // The socket closes with a permanent code when the session lapses; re-check
  // it so the app returns to the login screen instead of looking stalled.
  const handleSignedOut = useCallback(() => void onSignedOut(), [onSignedOut]);
  const { doc, awareness, room, status, synced, peers } = useSync(undefined, handleSignedOut);
  const notes = useSharedText(doc, 'notes');
  const rendered = useTextSnapshot(notes);
  // Pointer positions are relative to this element, so every client agrees on
  // where a pointer is regardless of window size.
  const surface = useRef<HTMLDivElement>(null);
  const present = usePresence(awareness, surface, username);
  const scheme = useComputedColorScheme('light');

  return (
    <AppShell header={{ height: 64 }} padding="md">
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between">
          <Text fw={700} size="xl">Canvas</Text>
          <Group gap="xs">
            <Badge variant="light" color={statusColors[status]}>{status}</Badge>
            <Badge variant="light">{synced ? 'synced' : 'syncing'}</Badge>
            <Badge variant="light">{peers} connected</Badge>
            <Text size="sm" c="dimmed">{username}</Text>
            <ColorSchemeToggle />
            <Tooltip label="Sign out">
              <ActionIcon
                variant="default"
                size="lg"
                onClick={() => void signOut()}
                aria-label="Sign out"
              >
                <svg
                  width="18"
                  height="18"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                >
                  <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9" />
                </svg>
              </ActionIcon>
            </Tooltip>
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Main>
        <Container size="xl" py="xl">
          <Paper withBorder p="xl" radius="md" pos="relative" ref={surface}>
            <Cursors peers={present} />
            <Stack>
              <Title order={1}>Room: {room}</Title>
              <Text c="dimmed">
                Open this page in another tab, or over the tunnel, and everything
                is shared: the text, each other's carets and selections, and the
                pointers moving over this panel.
              </Text>
              <SimpleGrid cols={{ base: 1, md: 2 }} spacing="xl">
                <Stack gap="xs">
                  <Text size="xs" c="dimmed" tt="uppercase" fw={700}>Markdown</Text>
                  <Divider />
                  <Editor text={notes} awareness={awareness} dark={scheme === 'dark'} />
                </Stack>
                <Stack gap="xs">
                  <Text size="xs" c="dimmed" tt="uppercase" fw={700}>Preview</Text>
                  <Divider />
                  <Preview text={rendered} />
                </Stack>
              </SimpleGrid>
            </Stack>
          </Paper>
        </Container>
      </AppShell.Main>
    </AppShell>
  );
}
