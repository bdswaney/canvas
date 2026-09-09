// Client-side routing is three path shapes, which is fewer than a router is
// worth here. Each is parsed into a variant the app can switch on, so an
// unrecognised path is a single explicit case rather than a null to guess at.
//
//   /              the projects list
//   /project/<id>  one project: its documents and its members
//   /doc/<id>      one document
export type Route =
  | { kind: 'projects' }
  | { kind: 'project'; projectID: string }
  | { kind: 'doc'; docID: string };

// Ids are uuids from the server, but the check only has to be tight enough to
// keep a stray path out of a URL; the server decides what actually exists.
const idPattern = /^[A-Za-z0-9-]{1,64}$/;

function id(value: string | undefined): string | null {
  return value && idPattern.test(value) ? value : null;
}

export function parseRoute(pathname: string = window.location.pathname): Route {
  const [prefix, value] = pathname.split('/').filter(Boolean);
  const parsed = id(value);

  switch (prefix) {
    case undefined:
      return { kind: 'projects' };
    case 'project':
      return parsed ? { kind: 'project', projectID: parsed } : { kind: 'projects' };
    case 'doc':
      return parsed ? { kind: 'doc', docID: parsed } : { kind: 'projects' };
    default:
      return { kind: 'projects' };
  }
}

// Paths are built here rather than interpolated at each call site, so the
// shapes above stay the only definition of what a URL looks like.
export const projectsPath = () => '/';
export const projectPath = (projectID: string) => `/project/${projectID}`;
export const docPath = (docID: string) => `/doc/${docID}`;

// navigate lives here with the paths it takes. A synthetic popstate is what
// tells useRoute to re-read the location, since pushState fires no event.
export function navigate(path: string) {
  window.history.pushState({}, '', path);
  window.dispatchEvent(new PopStateEvent('popstate'));
}
