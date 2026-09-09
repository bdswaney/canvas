import { useCallback, useState, type FormEvent } from 'react';
import {
  Alert,
  Button,
  Card,
  Container,
  Group,
  Loader,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { Archive } from './Archive';
import { Link } from './Link';
import { projectPath } from './routes';
import { archiveProject, createProject, listProjects, useRefetched, type Project } from './projects';

// The top of the hierarchy: pick a project, then its documents.
export function Projects({ onOpen }: { onOpen: (project: Project) => void }) {
  const { value: projects, error, refresh } = useRefetched(useCallback(() => listProjects(), []));
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  const create = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setFailure(null);
    try {
      const project = await createProject(name.trim());
      setName('');
      await refresh();
      onOpen(project);
    } catch (problem) {
      setFailure(problem instanceof Error ? problem.message : 'Could not create the project');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Container size="md" py="xl">
      <Stack>
        <Title order={2}>Projects</Title>
        {(error ?? failure) && (
          <Alert color="red" variant="light">
            {failure ?? error}
          </Alert>
        )}
        <form onSubmit={create}>
          <Group align="flex-end">
            <TextInput
              label="New project"
              placeholder="Platform"
              value={name}
              onChange={(event) => setName(event.currentTarget.value)}
              style={{ flex: 1 }}
            />
            <Button type="submit" loading={busy} disabled={name.trim() === ''}>
              Create
            </Button>
          </Group>
        </form>

        {projects === null ? (
          <Loader />
        ) : (
          <SimpleGrid cols={{ base: 1, sm: 2 }}>
            {projects.map((project) => (
              <Card key={project.id} withBorder padding="md" radius="md">
                <Stack gap={4}>
                  <Link fw={600} to={projectPath(project.id)}>
                    {project.name}
                  </Link>
                  <Text size="sm" c="dimmed">
                    Created {new Date(project.createdAt).toLocaleDateString()}
                  </Text>
                  {/* Archiving a project hides its documents with it. Their
                      saved history is kept: that cascade is the reason none
                      of this is a real delete. */}
                  <Archive
                    what="project"
                    archive={() => archiveProject(project.id)}
                    done={refresh}
                  />
                </Stack>
              </Card>
            ))}
          </SimpleGrid>
        )}
      </Stack>
    </Container>
  );
}
