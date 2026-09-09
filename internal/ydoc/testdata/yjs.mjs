// The other half of the conformance suite: the real yjs the browsers run,
// driven from Go so the two implementations can be compared directly.
//
// Reads one JSON command on stdin, writes one JSON result on stdout. Updates
// cross as base64 because they are binary and JSON is not.
import * as Y from 'yjs';

const read = async () => {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
};

const load = (state) => {
  const doc = new Y.Doc();
  if (state) Y.applyUpdate(doc, Buffer.from(state, 'base64'));
  return doc;
};

const command = await read();
const name = command.name ?? 'notes';

let result;
switch (command.op) {
  // text reads a document, which is how Go's output is checked.
  case 'text': {
    result = { text: load(command.state).getText(name).toString() };
    break;
  }
  // edit applies primitive operations and returns only the update they
  // produced, which is what Go then has to understand.
  case 'edit': {
    const doc = load(command.state);
    const text = doc.getText(name);
    const before = Y.encodeStateVector(doc);
    doc.transact(() => {
      for (const op of command.ops) {
        if (op.insert !== undefined) text.insert(op.insert.index, op.insert.text);
        else if (op.delete !== undefined) text.delete(op.delete.index, op.delete.length);
      }
    });
    result = {
      update: Buffer.from(Y.encodeStateAsUpdate(doc, before)).toString('base64'),
      state: Buffer.from(Y.encodeStateAsUpdate(doc)).toString('base64'),
      text: text.toString(),
    };
    break;
  }
  // merge collapses updates the way compaction will, for comparison with Go.
  case 'merge': {
    const doc = new Y.Doc();
    doc.transact(() => {
      for (const update of command.updates) Y.applyUpdate(doc, Buffer.from(update, 'base64'));
    });
    result = {
      update: Buffer.from(Y.encodeStateAsUpdate(doc)).toString('base64'),
      text: doc.getText(name).toString(),
    };
    break;
  }
  default:
    result = { error: `unknown op ${command.op}` };
}

process.stdout.write(JSON.stringify(result));
