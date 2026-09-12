import type { ComparisonRequestController } from './comparison';

export type WorkspaceComparisonEntries = {
  captureLiveComparison: () => Promise<void>;
  compareSavedVersions: (version: number) => Promise<void>;
};

/**
 * Keep the History drawer entry points idle-aware without coupling their
 * event handlers to React state. The component uses this same factory, so a
 * unit-level regression exercises the exact guards used by both buttons.
 */
export function createWorkspaceComparisonEntries(
  state: ComparisonRequestController,
  entries: WorkspaceComparisonEntries,
): WorkspaceComparisonEntries {
  return {
    captureLiveComparison: () => state.active ? Promise.resolve() : entries.captureLiveComparison(),
    compareSavedVersions: (version) => state.active ? Promise.resolve() : entries.compareSavedVersions(version),
  };
}
