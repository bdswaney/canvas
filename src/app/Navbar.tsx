import { useCallback, useEffect, useState } from 'react';
import { IconFileText, IconLayersIntersect, IconLogout } from '@tabler/icons-react';
import { ActionIcon, Loader, Title, Tooltip, UnstyledButton } from '@mantine/core';
import { ColorSchemeToggle } from '../components/ColorSchemeToggle';
import { getDoc, listDocs, type Doc } from '../api/docs';
import { listProjects, useRefetched, type Project } from '../api/projects';
import { docPath, navigate, projectPath, projectsPath, type Route } from './routes';
import classes from './Navbar.module.css';

type Section = 'projects' | 'documents';

// A project holds documents; a document is the thing people work on.
const sections = [
  { id: 'projects', label: 'Projects', icon: IconLayersIntersect },
  { id: 'documents', label: 'Documents', icon: IconFileText },
] as const satisfies readonly { id: Section; label: string; icon: typeof IconFileText }[];

/**
 * useCurrentProject resolves which project the open document belongs to. A
 * document names its project but the URL does not, so opening one by link has
 * to ask the server before the navbar can show its neighbours.
 */
function useCurrentProject(route: Route): string | null {
  const [projectID, setProjectID] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    const resolve = async () => {
      switch (route.kind) {
        case 'project':
          return route.projectID;
        case 'doc':
          return (await getDoc(route.docID)).projectId;
        default:
          return null;
      }
    };
    // A failure here means the thing is gone or was never reachable; the page
    // itself reports that, so the navbar just falls back to the project list.
    resolve()
      .then((id) => !cancelled && setProjectID(id))
      .catch(() => !cancelled && setProjectID(null));
    return () => {
      cancelled = true;
    };
  }, [route]);

  return projectID;
}

// The section a route belongs to, so that following a link moves the rail too.
function sectionFor(route: Route): Section {
  return route.kind === 'doc' ? 'documents' : 'projects';
}

export function Navbar({
  route,
  username,
  signOut,
}: {
  route: Route;
  username: string;
  signOut: () => Promise<void>;
}) {
  const projectID = useCurrentProject(route);
  const [section, setSection] = useState<Section>(() => sectionFor(route));

  // Navigating changes the section; clicking the rail changes it too, so the
  // route leads and the rail follows rather than the two fighting.
  useEffect(() => setSection(sectionFor(route)), [route]);

  const projects = useRefetched(useCallback(() => listProjects(), []));
  const docs = useRefetched(
    useCallback(() => (projectID ? listDocs(projectID) : Promise.resolve([])), [projectID]),
  );

  return (
    <nav className={classes.navbar}>
      <div className={classes.aside}>
        {sections.map(({ id, label, icon: Glyph }) => (
          <Tooltip key={id} label={label} position="right" withArrow transitionProps={{ duration: 0 }}>
            <UnstyledButton
              className={classes.mainLink}
              data-active={id === section || undefined}
              aria-label={label}
              onClick={() => {
                setSection(id);
                // Documents are always a project's; with none open there is
                // nothing to list, so go and pick one.
                if (id === 'projects' || projectID === null) navigate(projectsPath());
              }}
            >
              <Glyph size={22} stroke={1.5} />
            </UnstyledButton>
          </Tooltip>
        ))}

        <div className={classes.asideFooter}>
          <ColorSchemeToggle />
          <Tooltip label={`Sign out (${username})`} position="right" withArrow>
            <ActionIcon
              variant="subtle"
              color="gray"
              size="lg"
              onClick={() => void signOut()}
              aria-label="Sign out"
            >
              <IconLogout size={18} stroke={1.5} />
            </ActionIcon>
          </Tooltip>
        </div>
      </div>

      <div className={classes.main}>
        <Title order={4} className={classes.title}>
          {sections.find((entry) => entry.id === section)?.label}
        </Title>

        {section === 'projects' && (
          <List
            items={projects.value?.map((project: Project) => ({
              key: project.id,
              label: project.name,
              href: projectPath(project.id),
              active: projectID === project.id,
            }))}
            empty="No projects yet."
          />
        )}


        {section === 'documents' && (
          <List
            items={docs.value?.map((doc: Doc) => ({
              key: doc.id,
              label: doc.name,
              href: docPath(doc.id),
              active: route.kind === 'doc' && route.docID === doc.id,
            }))}
            empty={projectID === null ? 'Open a project first.' : 'No documents in this project.'}
          />
        )}
      </div>
    </nav>
  );
}

type Entry = { key: string; label: string; href: string; active: boolean };

function List({ items, empty }: { items: Entry[] | undefined; empty: string }) {
  if (items === undefined) return <Loader size="sm" m="sm" />;
  if (items.length === 0) return <div className={classes.empty}>{empty}</div>;
  return (
    <>
      {items.map((item) => (
        <a
          key={item.key}
          className={classes.link}
          data-active={item.active || undefined}
          href={item.href}
          onClick={(event) => {
            if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return;
            event.preventDefault();
            navigate(item.href);
          }}
        >
          {item.label}
        </a>
      ))}
    </>
  );
}
