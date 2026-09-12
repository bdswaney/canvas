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
  Switch,
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
import {
  getDoc,
  getVersionArtifact,
  listVersions,
  restoreVersion,
  saveDoc,
  sha256Base64,
  useDoc,
  type Version,
} from '../api/docs';
import { DocumentDiff } from '../components/DocumentDiff';
import { DiffError, diffMarkdown, type MarkdownDiff } from '../diff/diff';
import {
  beginComparisonRequest,
  cancelComparisonRequest,
  createComparisonRequestController,
  finishComparisonRequest,
  isCurrentComparisonRequest,
  type ComparisonRequestController,
} from '../diff/comparison';
import { createWorkspaceComparisonEntries } from '../diff/workspaceComparison';
import { usePresence } from '../collab/presence';
import {
  IconArrowBackUp,
  IconColumns1,
  IconColumns2,
  IconDeviceFloppy,
  IconEye,
  IconGitCompare,
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

type LiveComparison = {
  kind: 'live';
  id: number;
  baselineVersion: number;
  baselineText: string;
  liveText: string;
  diff: MarkdownDiff;
  stale: boolean;
};

type VersionComparison = {
  kind: 'versions';
  fromVersion: number;
  toVersion: number;
  fromText: string;
  toText: string;
  diff: MarkdownDiff;
};

type Comparison = LiveComparison | VersionComparison;

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
  const { doc: meta, error, refresh, setDoc: setMeta } = useDoc(docID);
  const surface = useRef<HTMLDivElement>(null);
  const present = usePresence(awareness, surface, username);
  const scheme = useComputedColorScheme('light');

  const [hash, setHash] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [versions, setVersions] = useState<Version[] | null>(null);
  const [comparison, setComparison] = useState<Comparison | null>(null);
  const [comparisonError, setComparisonError] = useState<string | null>(null);
  const [compareSelection, setCompareSelection] = useState<number | null>(null);
  const [comparing, setComparing] = useState(false);
  const comparisonRequest = useRef<ComparisonRequestController>(createComparisonRequestController());
  const liveCapture = useRef<{ id: number; stale: boolean } | null>(null);

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
  // Editor preferences belong to this browser, not the shared document, so
  // collaborators can choose Vim mode independently.
  const [vimMode, setVimMode] = useLocalStorage<boolean>({
    key: 'nply-workspace-vim-mode',
    defaultValue: false,
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

  const liveArtifact = useCallback(() => notes.toString(), [notes]);

  // Keep the stale marker tied to the live Y.Text rather than the debounced
  // preview. The observer is installed for the component's lifetime so an
  // edit made while the artifact request is in flight is not missed.
  useEffect(() => {
    const onChange = () => {
      const capture = liveCapture.current;
      if (!capture) return;
      capture.stale = true;
      setComparison((current) =>
        current?.kind === 'live' && current.id === capture.id ? { ...current, stale: true } : current,
      );
    };
    notes.observe(onChange);
    return () => notes.unobserve(onChange);
  }, [notes]);

  const save = useCallback(async () => {
    setSaving(true);
    setSaveError(null);
    try {
      // Both halves are read in the same tick. The preview's snapshot is
      // debounced, so using it here would store history that disagrees with
      // the document the snapshot came from.
      const artifact = liveArtifact();
      const snapshot = Y.encodeStateAsUpdate(doc);
      await saveDoc(docID, artifact, snapshot);
      await refresh();
      setVersions(null);
    } catch (failure) {
      setSaveError(failure instanceof Error ? failure.message : 'Could not save');
    } finally {
      setSaving(false);
    }
  }, [doc, docID, liveArtifact, refresh]);

  const beginComparison = () => {
    const request = beginComparisonRequest(comparisonRequest.current);
    setComparing(true);
    return { id: request.id, signal: request.controller.signal };
  };

  const isCurrentComparison = (id: number) => isCurrentComparisonRequest(comparisonRequest.current, id);

  const finishComparison = (id: number) => {
    if (!finishComparisonRequest(comparisonRequest.current, id)) return false;
    setComparing(false);
    return true;
  };

  useEffect(() => () => cancelComparisonRequest(comparisonRequest.current), []);

  const captureLiveComparisonImpl = async () => {
    const { id, signal } = beginComparison();
    setComparisonError(null);
    try {
      // This metadata read is the baseline linearization point. Capture the
      // exact Y.Text only after it returns, then fetch that returned version.
      const currentMeta = await getDoc(docID, { signal });
      if (!isCurrentComparison(id)) return;
      setMeta(currentMeta);
      const liveText = liveArtifact();
      liveCapture.current = { id, stale: false };
      const baselineVersion = currentMeta.currentVersion;
      const baselineText = baselineVersion === 0
        ? ''
        : (await getVersionArtifact(docID, baselineVersion, { signal })).artifact;
      if (!isCurrentComparison(id)) return;
      const diff = diffMarkdown(baselineText, liveText);
      setComparison({
        kind: 'live',
        id,
        baselineVersion,
        baselineText,
        liveText,
        diff,
        stale: liveCapture.current?.stale ?? false,
      });
    } catch (failure) {
      if (!isCurrentComparison(id)) return;
      liveCapture.current = null;
      setComparisonError(failure instanceof DiffError ? failure.message : failure instanceof Error ? failure.message : 'Could not compare the live document');
    } finally {
      finishComparison(id);
    }
  };

  const compareSavedVersionsImpl = async (version: number) => {
    if (compareSelection === null) {
      liveCapture.current = null;
      setComparison(null);
      setCompareSelection(version);
      setComparisonError(null);
      return;
    }
    if (compareSelection === version) {
      setCompareSelection(null);
      setComparison(null);
      setComparisonError(null);
      return;
    }
    const fromVersion = compareSelection;
    const { id, signal } = beginComparison();
    setComparison(null);
    setComparisonError(null);
    try {
      const [from, to] = await Promise.all([
        getVersionArtifact(docID, fromVersion, { signal }),
        getVersionArtifact(docID, version, { signal }),
      ]);
      if (!isCurrentComparison(id)) return;
      setComparison({
        kind: 'versions',
        fromVersion,
        toVersion: version,
        fromText: from.artifact,
        toText: to.artifact,
        diff: diffMarkdown(from.artifact, to.artifact),
      });
      setCompareSelection(null);
    } catch (failure) {
      if (!isCurrentComparison(id)) return;
      setComparisonError(failure instanceof DiffError ? failure.message : failure instanceof Error ? failure.message : 'Could not compare saved versions');
    } finally {
      finishComparison(id);
    }
  };

  const { captureLiveComparison, compareSavedVersions } = createWorkspaceComparisonEntries(comparisonRequest.current, {
    captureLiveComparison: captureLiveComparisonImpl,
    compareSavedVersions: compareSavedVersionsImpl,
  });

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
        {versions === null ? null : (
          <Stack gap="md">
            <Button
              variant="light"
              leftSection={<IconGitCompare size={16} stroke={1.5} />}
              loading={comparing}
              disabled={comparing}
              onClick={() => void captureLiveComparison()}
            >
              Compare live with {meta.currentVersion === 0 ? 'empty baseline' : `version ${meta.currentVersion}`}
            </Button>
            <Text size="xs" c="dimmed">
              Compare any two saved versions by selecting one row and then another. Comparisons are frozen snapshots.
            </Text>
            {compareSelection !== null && (
              <Alert color="blue" variant="light">
                Version {compareSelection} selected. Select another version to compare.
              </Alert>
            )}
            {versions.length === 0 ? (
              <Text c="dimmed" size="sm">No saved versions yet.</Text>
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
                      <Table.Td>
                        <Button
                          size="compact-sm"
                          variant={compareSelection === version.version ? 'filled' : 'subtle'}
                          leftSection={<IconGitCompare size={15} stroke={1.5} />}
                          disabled={comparing}
                          onClick={() => void compareSavedVersions(version.version)}
                        >
                          {compareSelection === version.version ? 'Selected' : 'Compare'}
                        </Button>
                      </Table.Td>
                      <Table.Td align="right">
                        <Button
                          size="compact-sm"
                          variant="light"
                          leftSection={<IconArrowBackUp size={15} stroke={1.5} />}
                          disabled={comparing}
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
            {comparisonError && <Alert color="red" variant="light">{comparisonError}</Alert>}
            {comparison && (
              <Stack gap="xs">
                <Divider />
                <Text fw={600}>
                  {comparison.kind === 'live'
                    ? `Live document vs ${comparison.baselineVersion === 0 ? 'empty baseline' : `version ${comparison.baselineVersion}`}`
                    : `Version ${comparison.fromVersion} → version ${comparison.toVersion}`}
                </Text>
                {comparison.kind === 'live' && comparison.stale && (
                  <Alert color="orange" variant="light">
                    Live text changed after this comparison was captured. The diff remains frozen; capture it again to include the newer text.
                  </Alert>
                )}
                <DocumentDiff hunks={comparison.diff.hunks} />
              </Stack>
            )}
          </Stack>
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
              <Switch
                aria-label="Vim mode"
                label="Vim"
                checked={vimMode}
                onChange={(event) => setVimMode(event.currentTarget.checked)}
              />
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
              <Editor
                text={notes}
                awareness={awareness}
                dark={scheme === 'dark'}
                vimMode={vimMode}
                hidden={!showEditor}
              />
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
