import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import {
  Alert,
  Badge,
  Button,
  Center,
  Container,
  Divider,
  Drawer,
  Group,
  Loader,
  Paper,
  SegmentedControl,
  SimpleGrid,
  Stack,
  Table,
  Text,
  Title,
  useComputedColorScheme,
  useMantineTheme,
  VisuallyHidden,
} from '@mantine/core';
import { useLocalStorage, useMediaQuery } from '@mantine/hooks';
import * as Y from 'yjs';
import { Cursors } from '../collab/Cursors';
import { Editor } from '../editor/Editor';
import { Preview } from '../editor/Preview';
import { listVersions, restoreVersion, saveDoc, sha256Base64, useDoc, type Version } from '../api/docs';
import { usePresence } from '../collab/presence';
import {
  IconArrowBackUp,
  IconColumns1,
  IconColumns2,
  IconDeviceFloppy,
  IconEye,
  IconHistory,
  IconPencil,
} from '@tabler/icons-react';
import { useSharedText, useSync, useTextSnapshot, type Status } from '../collab/sync';

// sha256 of the empty string, base64. A document that has never been saved
// has no stored hash, and an empty one has nothing to save.
const emptyDocumentHash = '47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=';

const statusColors: Record<Status, string> = {
  connected: 'green',
  connecting: 'yellow',
  disconnected: 'red',
};

// How the editor and preview are laid out is a viewing preference, not
// document state: it lives in this browser, so two people reading the same
// document can disagree. Mantine's storage hook tolerates blocked storage.
type Layout = 'split' | 'single';
type Pane = 'edit' | 'preview';

const layoutOptions = [
  { value: 'split', label: <LayoutLabel icon={<IconColumns2 size={16} stroke={1.5} />} name="Split view" /> },
  { value: 'single', label: <LayoutLabel icon={<IconColumns1 size={16} stroke={1.5} />} name="Single column" /> },
];

const paneOptions = [
  { value: 'edit', label: <PaneLabel icon={<IconPencil size={16} stroke={1.5} />} name="Markdown" /> },
  { value: 'preview', label: <PaneLabel icon={<IconEye size={16} stroke={1.5} />} name="Preview" /> },
];

function LayoutLabel({ icon, name }: { icon: ReactNode; name: string }) {
  return (
    <Center title={name}>
      {icon}
      <VisuallyHidden>{name}</VisuallyHidden>
    </Center>
  );
}

function PaneLabel({ icon, name }: { icon: ReactNode; name: string }) {
  return (
    <Group gap={6} wrap="nowrap" justify="center">
      {icon}
      <span>{name}</span>
    </Group>
  );
}

export function Workspace({
  docID,
  username,
  onSignedOut,
}: {
  docID: string;
  username: string;
  onSignedOut: () => void;
}) {
  const { doc, awareness, status, synced, peers, refused } = useSync(docID, onSignedOut);
  const notes = useSharedText(doc, 'notes');
  const rendered = useTextSnapshot(notes);
  const { doc: meta, error, refresh } = useDoc(docID);
  const surface = useRef<HTMLDivElement>(null);
  const present = usePresence(awareness, surface, username);
  const scheme = useComputedColorScheme('light');

  const [hash, setHash] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [versions, setVersions] = useState<Version[] | null>(null);

  const [layout, setLayout] = useLocalStorage<Layout>({
    key: 'canvas-workspace-layout',
    defaultValue: 'split',
    getInitialValueInEffect: false,
  });
  const [pane, setPane] = useLocalStorage<Pane>({
    key: 'canvas-workspace-pane',
    defaultValue: 'edit',
    getInitialValueInEffect: false,
  });

  // Split has never worked below md, so a narrow screen is always single
  // column and the layout control is not offered there. The same query
  // decides both, so they cannot disagree at the boundary.
  const theme = useMantineTheme();
  const wide = useMediaQuery(`(min-width: ${theme.breakpoints.md})`, undefined, {
    getInitialValueInEffect: false,
  });
  const singleColumn = !wide || layout === 'single';
  // Anything unexpected in storage falls back to showing the editor, so no
  // combination leaves both panes hidden.
  const showEditor = !singleColumn || pane !== 'preview';
  const showPreview = !singleColumn || pane === 'preview';

  // Whether the document holds unsaved work is a hash comparison, not a
  // question about the journal: opening a document appends to that, and an
  // edit that is typed and deleted leaves rows behind with identical text.
  //
  // The hash is taken from the text itself, not from the preview's debounced
  // copy, so that it always describes what a save would actually send.
  useEffect(() => {
    let cancelled = false;
    let frame = 0;
    const update = () => {
      frame = 0;
      const current = notes.toString();
      void sha256Base64(current).then((digest) => {
        if (!cancelled) setHash(digest);
      });
    };
    const schedule = () => {
      if (frame === 0) frame = requestAnimationFrame(update);
    };
    notes.observe(schedule);
    update();
    return () => {
      cancelled = true;
      if (frame !== 0) cancelAnimationFrame(frame);
      notes.unobserve(schedule);
    };
  }, [notes]);

  const dirty =
    meta === null || hash === null ? false : hash !== (meta.savedSha256 ?? emptyDocumentHash);

  const save = useCallback(async () => {
    setSaving(true);
    setSaveError(null);
    try {
      // Both halves are read in the same tick. The preview's snapshot is
      // debounced, so using it here would store history that disagrees with
      // the document the snapshot came from.
      const artifact = notes.toString();
      const snapshot = Y.encodeStateAsUpdate(doc);
      await saveDoc(docID, artifact, snapshot);
      await refresh();
      setVersions(null);
    } catch (failure) {
      setSaveError(failure instanceof Error ? failure.message : 'Could not save');
    } finally {
      setSaving(false);
    }
  }, [doc, docID, notes, refresh]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key === 's') {
        event.preventDefault();
        void save();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [save]);

  const openHistory = async () => {
    setVersions([]);
    setVersions(await listVersions(docID));
  };

  // Restoring writes a saved artifact back into the live document; the server
  // cannot rebuild CRDT state from text. This is an edit, not a rollback: it
  // is a replace applied to the shared text, so a peer typing at the same
  // moment has their keystrokes merged into the restored text rather than
  // discarded. Everyone converges, and the old version stays in history.
  const restore = async (version: number) => {
    const { artifact } = await restoreVersion(docID, version);
    doc.transact(() => {
      notes.delete(0, notes.length);
      notes.insert(0, artifact);
    });
    setVersions(null);
  };

  if (error) {
    return (
      <Container size="sm" py="xl">
        <Alert color="red" variant="light" title="This document could not be opened">
          {error}
        </Alert>
      </Container>
    );
  }

  if (meta === null) {
    return (
      <Center mih="60vh">
        <Loader />
      </Center>
    );
  }

  return (
    <Container size="xl" py="xl">
      <Drawer opened={versions !== null} onClose={() => setVersions(null)} title="History" position="right">
        {versions === null || versions.length === 0 ? (
          <Text c="dimmed" size="sm">
            {versions === null ? '' : 'No saved versions yet.'}
          </Text>
        ) : (
          <Table>
            <Table.Tbody>
              {versions.map((version) => (
                <Table.Tr key={version.version}>
                  <Table.Td>
                    <Text fw={600}>Version {version.version}</Text>
                    <Text size="xs" c="dimmed">
                      {version.author || 'unknown'} · {new Date(version.createdAt).toLocaleString()}
                    </Text>
                  </Table.Td>
                  <Table.Td align="right">
                    <Button
                      size="compact-sm"
                      variant="light"
                      leftSection={<IconArrowBackUp size={15} stroke={1.5} />}
                      onClick={() => void restore(version.version)}
                    >
                      Restore
                    </Button>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        )}
      </Drawer>

      <Paper withBorder p="xl" radius="md" pos="relative" ref={surface}>
        <Cursors peers={present} />
        <Stack>
          <Group justify="space-between" align="flex-start">
            <div>
              <Title order={1}>{meta.name}</Title>
              <Text c="dimmed" size="sm">
                {meta.currentVersion === 0
                  ? 'Never saved'
                  : `Version ${meta.currentVersion}, saved ${new Date(meta.updatedAt).toLocaleString()}`}
              </Text>
            </div>
            <Group gap="xs">
              <Badge variant="light" color={statusColors[status]}>{status}</Badge>
              <Badge variant="light">{synced ? 'synced' : 'syncing'}</Badge>
              <Badge variant="light">{peers} connected</Badge>
              <Badge variant="light" color={dirty ? 'orange' : 'gray'}>
                {dirty ? 'unsaved changes' : 'saved'}
              </Badge>
              {wide && (
                <SegmentedControl
                  aria-label="Layout"
                  value={layout === 'single' ? 'single' : 'split'}
                  onChange={(value) => setLayout(value as Layout)}
                  data={layoutOptions}
                />
              )}
              <Button
                variant="default"
                leftSection={<IconHistory size={16} stroke={1.5} />}
                onClick={() => void openHistory()}
              >
                History
              </Button>
              <Button
                onClick={() => void save()}
                loading={saving}
                disabled={!dirty}
                leftSection={<IconDeviceFloppy size={16} stroke={1.5} />}
              >
                Save
              </Button>
            </Group>
          </Group>

          {refused && (
            <Alert color="orange" variant="light" title="This document is no longer live here">
              {refused} Nothing further will arrive, and anything typed here now stays on this
              screen.
            </Alert>
          )}

          {saveError && (
            <Alert color="red" variant="light">
              {saveError}
            </Alert>
          )}

          {singleColumn && (
            <SegmentedControl
              aria-label="Show"
              value={showEditor ? 'edit' : 'preview'}
              onChange={(value) => setPane(value as Pane)}
              data={paneOptions}
              style={{ alignSelf: 'flex-start' }}
            />
          )}

          {/* The tree stays the same shape in every layout. The editor is
              hidden rather than unmounted, which would destroy its view and
              undo history on every toggle; it also keeps this person's last
              caret visible to peers while they read. The preview re-reads
              the text when it mounts, so it is simply left out. */}
          <SimpleGrid cols={singleColumn ? 1 : 2} spacing="xl">
            <Stack gap="xs" display={showEditor ? undefined : 'none'}>
              {!singleColumn && <Text size="xs" c="dimmed" tt="uppercase" fw={700}>Markdown</Text>}
              <Divider />
              <Editor text={notes} awareness={awareness} dark={scheme === 'dark'} hidden={!showEditor} />
            </Stack>
            {showPreview && (
              <Stack gap="xs">
                {!singleColumn && <Text size="xs" c="dimmed" tt="uppercase" fw={700}>Preview</Text>}
                <Divider />
                <Preview text={rendered} />
              </Stack>
            )}
          </SimpleGrid>
        </Stack>
      </Paper>
    </Container>
  );
}
