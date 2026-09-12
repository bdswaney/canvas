import { Code, Divider, ScrollArea, Stack, Text, VisuallyHidden } from '@mantine/core';
import type { DiffHunk, DiffLine, WordChange } from '../diff/diff';

function wordColor(kind: WordChange['kind']): string | undefined {
  if (kind === 'added') return 'var(--mantine-color-green-light)';
  if (kind === 'removed') return 'var(--mantine-color-red-light)';
  return undefined;
}

function lineColor(kind: DiffLine['kind']): string | undefined {
  if (kind === 'added') return 'var(--mantine-color-green-light)';
  if (kind === 'removed') return 'var(--mantine-color-red-light)';
  return undefined;
}

function LineText({ line }: { line: DiffLine }) {
  if (!line.words) return <>{line.text || ' '}</>;
  return (
    <>
      {line.words.map((word, index) => (
        <span key={`${word.kind}-${index}`} style={{ backgroundColor: wordColor(word.kind) }}>
          {word.text || ' '}
        </span>
      ))}
    </>
  );
}

function DiffLineView({ line }: { line: DiffLine }) {
  const number = line.kind === 'removed' ? line.oldLine : line.newLine;
  const prefix = line.kind === 'added' ? '+' : line.kind === 'removed' ? '−' : ' ';
  const accessibleLabel = `${line.kind} line${line.lineEnding ? ` (${line.lineEnding} line ending)` : ''}: ${line.text || 'blank'}`;
  return (
    <div
      role="group"
      aria-label={accessibleLabel}
      style={{
        display: 'grid',
        gridTemplateColumns: '3.5rem 1.25rem minmax(0, 1fr)',
        padding: '0 0.5rem',
        backgroundColor: lineColor(line.kind),
      }}
    >
      <Text component="span" c="dimmed" ta="right" pr="sm" size="xs">
        {number ?? ''}
      </Text>
      <Text component="span" fw={700} c="dimmed" aria-hidden>
        {prefix}
      </Text>
      <VisuallyHidden>{`${line.kind} line${line.lineEnding ? ` (${line.lineEnding} line ending)` : ''}. `}</VisuallyHidden>
      <Code component="span" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>
        <LineText line={line} />
      </Code>
    </div>
  );
}

export function DocumentDiff({ hunks }: { hunks: DiffHunk[] }) {
  if (hunks.length === 0) {
    return <Text c="dimmed" size="sm">The compared versions are identical.</Text>;
  }
  return (
    <ScrollArea.Autosize mah="55vh" type="auto" offsetScrollbars>
      <Stack gap="sm" aria-label="Markdown document differences">
        {hunks.map((hunk) => (
          <div key={`${hunk.oldStart}-${hunk.newStart}`}>
            <Text size="xs" c="dimmed" px="sm" py={4}>
              @@ −{hunk.oldStart},{hunk.oldCount} +{hunk.newStart},{hunk.newCount} @@
            </Text>
            {hunk.lines.map((line, index) => <DiffLineView key={`${line.kind}-${line.oldLine ?? 'n'}-${line.newLine ?? 'n'}-${index}`} line={line} />)}
            <Divider mt="sm" />
          </div>
        ))}
      </Stack>
    </ScrollArea.Autosize>
  );
}
