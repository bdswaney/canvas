export type ComparisonRequest = {
  id: number;
  controller: AbortController;
};

export type ComparisonRequestController = {
  nextID: number;
  active: ComparisonRequest | null;
};

export function createComparisonRequestController(): ComparisonRequestController {
  return { nextID: 0, active: null };
}

export function beginComparisonRequest(state: ComparisonRequestController): ComparisonRequest {
  state.active?.controller.abort();
  const request = { id: state.nextID + 1, controller: new AbortController() };
  state.nextID = request.id;
  state.active = request;
  return request;
}

export function isCurrentComparisonRequest(state: ComparisonRequestController, id: number): boolean {
  return state.active?.id === id;
}

export function finishComparisonRequest(state: ComparisonRequestController, id: number): boolean {
  if (!isCurrentComparisonRequest(state, id)) return false;
  state.active = null;
  return true;
}

export function cancelComparisonRequest(state: ComparisonRequestController): void {
  state.active?.controller.abort();
  state.active = null;
}
