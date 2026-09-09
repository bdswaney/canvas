import { useCallback, useEffect, useState } from 'react';
import {
  ActionIcon,
  AppShell,
  Anchor,
  Center,
  Group,
  Loader,
  Text,
  Tooltip,
} from '@mantine/core';
import { ColorSchemeToggle } from './ColorSchemeToggle';
import { DocPicker } from './DocPicker';
import { Login } from './Login';
import { Workspace } from './Workspace';
import { docIdFromPath } from './sync';
import { useSession } from './useSession';

// Client-side routing is one path shape — /doc/<id> — so it needs no router.
function navigate(path: string) {
  window.history.pushState({}, '', path);
  window.dispatchEvent(new PopStateEvent('popstate'));
}

function usePath(): string {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname);
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);
  return path;
}

export function App() {
  const { session, refresh, signOut } = useSession();
  const path = usePath();

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

  return (
    <Shell username={session.username} signOut={signOut}>
      <Routes path={path} username={session.username} onSignedOut={refresh} />
    </Shell>
  );
}

function Routes({
  path,
  username,
  onSignedOut,
}: {
  path: string;
  username: string;
  onSignedOut: () => Promise<void>;
}) {
  // The socket closes with a permanent code when the session lapses, so
  // re-check it rather than leaving the app looking merely disconnected.
  const handleSignedOut = useCallback(() => void onSignedOut(), [onSignedOut]);
  const docID = docIdFromPath(path);

  if (docID === null) {
    return <DocPicker onOpen={(doc) => navigate(`/doc/${doc.id}`)} />;
  }
  // Keyed on the id so switching documents builds a new socket and Y.Doc
  // rather than mutating the open one.
  return <Workspace key={docID} docID={docID} username={username} onSignedOut={handleSignedOut} />;
}

function Shell({
  username,
  signOut,
  children,
}: {
  username: string;
  signOut: () => Promise<void>;
  children: React.ReactNode;
}) {
  return (
    <AppShell header={{ height: 64 }} padding="md">
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between">
          <Anchor
            fw={700}
            size="xl"
            underline="never"
            c="inherit"
            href="/"
            onClick={(event) => {
              event.preventDefault();
              navigate('/');
            }}
          >
            Canvas
          </Anchor>
          <Group gap="xs">
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
      <AppShell.Main>{children}</AppShell.Main>
    </AppShell>
  );
}
