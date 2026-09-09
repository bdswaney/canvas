import { useCallback, useEffect, useState } from 'react';
import { Anchor, AppShell, Burger, Center, Group, Loader, Text } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { Login } from './Login';
import { Navbar } from './Navbar';
import { Project } from './Project';
import { Projects } from './Projects';
import { Workspace } from './Workspace';
import { docPath, navigate, parseRoute, projectPath, projectsPath, type Route } from './routes';
import { useSession } from './useSession';

function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute());
  useEffect(() => {
    const onPop = () => setRoute(parseRoute());
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);
  return route;
}

export function App() {
  const { session, refresh, signOut } = useSession();
  const route = useRoute();

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
    <Shell route={route} username={session.username} signOut={signOut}>
      <Routes route={route} username={session.username} onSignedOut={refresh} />
    </Shell>
  );
}

function Routes({
  route,
  username,
  onSignedOut,
}: {
  route: Route;
  username: string;
  onSignedOut: () => Promise<void>;
}) {
  // The socket closes with a permanent code when the session lapses, so
  // re-check it rather than leaving the app looking merely disconnected.
  const handleSignedOut = useCallback(() => void onSignedOut(), [onSignedOut]);

  switch (route.kind) {
    case 'projects':
      return <Projects onOpen={(project) => navigate(projectPath(project.id))} />;
    case 'project':
      return (
        <Project
          key={route.projectID}
          projectID={route.projectID}
          username={username}
          onOpenDoc={(doc) => navigate(docPath(doc.id))}
        />
      );
    case 'doc':
      // Keyed on the id so switching documents builds a new socket and Y.Doc
      // rather than mutating the open one.
      return (
        <Workspace
          key={route.docID}
          docID={route.docID}
          username={username}
          onSignedOut={handleSignedOut}
        />
      );
  }
}

function Shell({
  route,
  username,
  signOut,
  children,
}: {
  route: Route;
  username: string;
  signOut: () => Promise<void>;
  children: React.ReactNode;
}) {
  const [opened, { toggle, close }] = useDisclosure();

  // On a narrow screen the navbar is a drawer; following a link inside it
  // should close it rather than leave it covering what was just opened.
  useEffect(close, [route, close]);

  return (
    <AppShell
      header={{ height: 56 }}
      navbar={{ width: 300, breakpoint: 'sm', collapsed: { mobile: !opened } }}
      padding="md"
    >
      <AppShell.Header>
        <Group h="100%" px="md" gap="sm">
          <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" />
          <Anchor
            fw={700}
            size="lg"
            underline="never"
            c="inherit"
            href={projectsPath()}
            onClick={(event) => {
              event.preventDefault();
              navigate(projectsPath());
            }}
          >
            Canvas
          </Anchor>
          <Text size="sm" c="dimmed" ml="auto">
            {username}
          </Text>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar>
        <Navbar route={route} username={username} signOut={signOut} />
      </AppShell.Navbar>
      <AppShell.Main>{children}</AppShell.Main>
    </AppShell>
  );
}
