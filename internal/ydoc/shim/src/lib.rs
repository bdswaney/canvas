//! A flat WASM interface over yrs, the Yjs organisation's Rust port, so the Go
//! server can interpret documents it previously only relayed.
//!
//! Everything here is stateless: each call takes an encoded document state,
//! does one thing, and returns bytes. No document handles cross the boundary,
//! so there is no lifecycle for the host to manage and nothing to leak if a
//! call fails.
//!
//! ## Calling convention
//!
//! Buffers are passed as (pointer, length) pairs into WASM linear memory that
//! the host allocated with `canvas_alloc`. Every operation returns a packed
//! `u64` — the high 32 bits are a pointer, the low 32 bits a length — naming a
//! result buffer the host must release with `canvas_dealloc`.
//!
//! The first byte of a result is a status: 0 for success, in which case the
//! rest is the payload, or 1 for failure, in which case the rest is a UTF-8
//! message. Returning errors as data rather than trapping keeps a malformed
//! document from taking down the server.

use std::mem;

use similar::{capture_diff_slices, Algorithm, DiffTag};
use yrs::updates::decoder::Decode;
use yrs::{Doc, GetString, Options, OffsetKind, ReadTxn, StateVector, Text, Transact, Update};

const STATUS_OK: u8 = 0;
const STATUS_ERR: u8 = 1;

/// canvas_alloc reserves `len` bytes and returns a pointer the host can write
/// to. Pair every call with canvas_dealloc.
#[no_mangle]
pub extern "C" fn canvas_alloc(len: u32) -> u32 {
    let mut buf = Vec::<u8>::with_capacity(len as usize);
    let ptr = buf.as_mut_ptr();
    mem::forget(buf);
    ptr as u32
}

/// canvas_dealloc releases a buffer from canvas_alloc or from a result.
#[no_mangle]
pub extern "C" fn canvas_dealloc(ptr: u32, len: u32) {
    if ptr == 0 {
        return;
    }
    unsafe {
        drop(Vec::from_raw_parts(ptr as *mut u8, len as usize, len as usize));
    }
}

fn slice<'a>(ptr: u32, len: u32) -> &'a [u8] {
    if ptr == 0 || len == 0 {
        return &[];
    }
    unsafe { std::slice::from_raw_parts(ptr as *const u8, len as usize) }
}

/// pack hands a buffer back to the host as (pointer << 32) | length.
fn pack(mut body: Vec<u8>) -> u64 {
    body.shrink_to_fit();
    let ptr = body.as_mut_ptr() as u64;
    let len = body.len() as u64;
    mem::forget(body);
    (ptr << 32) | len
}

fn ok(payload: &[u8]) -> u64 {
    let mut body = Vec::with_capacity(payload.len() + 1);
    body.push(STATUS_OK);
    body.extend_from_slice(payload);
    pack(body)
}

fn err(message: &str) -> u64 {
    let mut body = Vec::with_capacity(message.len() + 1);
    body.push(STATUS_ERR);
    body.extend_from_slice(message.as_bytes());
    pack(body)
}

/// document builds a Doc from an encoded update.
///
/// OffsetKind::Utf16 is load-bearing: yrs counts in bytes by default, while
/// Yjs in the browser counts UTF-16 code units. Left at the default, every
/// index in a document containing anything outside ASCII would disagree with
/// the clients, and the corruption would be silent.
fn document(state: &[u8]) -> Result<Doc, String> {
    let doc = Doc::with_options(Options {
        offset_kind: OffsetKind::Utf16,
        ..Default::default()
    });
    if !state.is_empty() {
        let update = Update::decode_v1(state).map_err(|e| format!("decode state: {e}"))?;
        let mut txn = doc.transact_mut();
        txn.apply_update(update);
    }
    Ok(doc)
}

fn name_of(ptr: u32, len: u32) -> Result<String, String> {
    String::from_utf8(slice(ptr, len).to_vec()).map_err(|e| format!("text name: {e}"))
}

fn editing_document(state: &[u8], client_id: u32) -> Result<Doc, String> {
    let doc = Doc::with_options(Options {
        client_id: client_id as u64,
        offset_kind: OffsetKind::Utf16,
        ..Default::default()
    });
    if !state.is_empty() {
        let update = Update::decode_v1(state).map_err(|e| format!("decode state: {e}"))?;
        let mut txn = doc.transact_mut();
        txn.apply_update(update);
    }
    Ok(doc)
}

/// canvas_merge_updates collapses a sequence of updates into one.
///
/// The input is each update prefixed by its length as a little-endian u32,
/// which is how the journal is handed over. The result is a single update
/// carrying the whole document — what compaction stores in place of the rows
/// it replaces.
#[no_mangle]
pub extern "C" fn canvas_merge_updates(ptr: u32, len: u32) -> u64 {
    let doc = match document(&[]) {
        Ok(doc) => doc,
        Err(e) => return err(&e),
    };
    let buf = slice(ptr, len);
    let mut at = 0usize;
    {
        let mut txn = doc.transact_mut();
        while at < buf.len() {
            if at + 4 > buf.len() {
                return err("truncated update length");
            }
            let size = u32::from_le_bytes([buf[at], buf[at + 1], buf[at + 2], buf[at + 3]]) as usize;
            at += 4;
            if at + size > buf.len() {
                return err("truncated update body");
            }
            match Update::decode_v1(&buf[at..at + size]) {
                Ok(update) => txn.apply_update(update),
                Err(e) => return err(&format!("decode update: {e}")),
            }
            at += size;
        }
    }
    let merged = doc.transact().encode_state_as_update_v1(&StateVector::default());
    ok(&merged)
}

/// canvas_text returns the contents of one named Y.Text as UTF-8.
#[no_mangle]
pub extern "C" fn canvas_text(state_ptr: u32, state_len: u32, name_ptr: u32, name_len: u32) -> u64 {
    let name = match name_of(name_ptr, name_len) {
        Ok(name) => name,
        Err(e) => return err(&e),
    };
    let doc = match document(slice(state_ptr, state_len)) {
        Ok(doc) => doc,
        Err(e) => return err(&e),
    };
    let text = doc.get_or_insert_text(name.as_str());
    let contents = text.get_string(&doc.transact());
    ok(contents.as_bytes())
}

/// canvas_set_text edits a named Y.Text until it reads as `next`, and returns
/// only the update that change produced.
///
/// It is deliberately not a delete-everything-and-reinsert. That is what
/// restore does, and restore is documented as an edit rather than a rollback
/// precisely because it cannot merge: a peer typing during one has their
/// keystrokes folded into the replacement at best. Here the current text is
/// diffed against `next` and each hunk becomes its own delete or insert, so
/// two small changes far apart are two small operations and every character
/// between them keeps its Yjs item id. A peer typing anywhere that the diff
/// leaves unchanged keeps their work, and their keystrokes stay where they
/// typed them.
///
/// It is still a diff of text against text: it cannot tell a change the caller
/// meant from a change somebody else made after the caller last read. Callers
/// that care detect that before calling; see edit_document in internal/mcp.
#[no_mangle]
pub extern "C" fn canvas_set_text(
    state_ptr: u32,
    state_len: u32,
    name_ptr: u32,
    name_len: u32,
    next_ptr: u32,
    next_len: u32,
    client_id: u32,
) -> u64 {
    let name = match name_of(name_ptr, name_len) {
        Ok(name) => name,
        Err(e) => return err(&e),
    };
    let next = match std::str::from_utf8(slice(next_ptr, next_len)) {
        Ok(next) => next,
        Err(e) => return err(&format!("replacement text: {e}")),
    };
    let doc = match editing_document(slice(state_ptr, state_len), client_id) {
        Ok(doc) => doc,
        Err(e) => return err(&e),
    };
    let text = doc.get_or_insert_text(name.as_str());

    let before = doc.transact().state_vector();
    let current = text.get_string(&doc.transact());

    {
        let mut txn = doc.transact_mut();
        // Applied front to back, so `at` is always an index into the text as
        // it stands after the edits before it.
        let mut at: u32 = 0;
        for edit in edits(&current, next) {
            match edit {
                Edit::Keep(units) => at += units,
                Edit::Delete(units) => text.remove_range(&mut txn, at, units),
                Edit::Insert(chunk) => {
                    text.insert(&mut txn, at, chunk);
                    at += utf16_len(chunk);
                }
            }
        }
    }

    // Bound to a local so the transaction is dropped before doc is.
    let update = doc.transact().encode_state_as_update_v1(&before);
    ok(&update)
}

/// One step of turning the current text into the desired one. Lengths are in
/// UTF-16 code units, because that is what the document's OffsetKind makes
/// yrs count; see document.
#[derive(Debug, PartialEq)]
enum Edit<'a> {
    Keep(u32),
    Delete(u32),
    Insert(&'a str),
}

/// Past this many characters on the two sides together, a changed block is
/// replaced rather than refined. Myers costs (N+M)·D, which is cheap for the
/// scattered small edits this exists for and quadratic for a block rewritten
/// wholesale, where a character diff would find nothing worth keeping anyway.
const REFINE_LIMIT: usize = 20_000;

/// edits diffs `current` against `desired`: first by line, which keeps the
/// cost proportional to what changed, then by character inside each changed
/// block, so fixing one word in a line rewrites that word and not the line.
///
/// The character pass works on `char`s rather than UTF-16 units, so no hunk
/// boundary can fall inside a surrogate pair; lengths are converted to UTF-16
/// only when emitted. It may split a grapheme cluster such as a ZWJ emoji
/// sequence, which is harmless: the concatenated result is still exactly
/// `desired`.
fn edits<'a>(current: &str, desired: &'a str) -> Vec<Edit<'a>> {
    let old_lines: Vec<&str> = current.split_inclusive('\n').collect();
    let new_lines: Vec<&str> = desired.split_inclusive('\n').collect();
    let old_at = offsets(&old_lines);
    let new_at = offsets(&new_lines);

    let mut out = Vec::new();
    for op in capture_diff_slices(Algorithm::Myers, &old_lines, &new_lines) {
        let (tag, old, new) = op.as_tag_tuple();
        let old_block = &current[old_at[old.start]..old_at[old.end]];
        let new_block = &desired[new_at[new.start]..new_at[new.end]];
        match tag {
            DiffTag::Equal => push(&mut out, Edit::Keep(utf16_len(old_block))),
            DiffTag::Delete => push(&mut out, Edit::Delete(utf16_len(old_block))),
            DiffTag::Insert => push(&mut out, Edit::Insert(new_block)),
            DiffTag::Replace => refine(&mut out, old_block, new_block),
        }
    }
    out
}

/// refine diffs one changed block character by character.
fn refine<'a>(out: &mut Vec<Edit<'a>>, old_block: &str, new_block: &'a str) {
    let old_chars: Vec<char> = old_block.chars().collect();
    let new_chars: Vec<char> = new_block.chars().collect();
    if old_chars.len() + new_chars.len() > REFINE_LIMIT {
        push(out, Edit::Delete(utf16_len(old_block)));
        push(out, Edit::Insert(new_block));
        return;
    }
    // Byte offset of every character in the new block, and of its end, so an
    // insert can borrow its text rather than re-encode it.
    let new_bytes: Vec<usize> = new_block
        .char_indices()
        .map(|(i, _)| i)
        .chain(std::iter::once(new_block.len()))
        .collect();
    let units = |chars: &[char]| chars.iter().map(|c| c.len_utf16() as u32).sum::<u32>();

    for op in capture_diff_slices(Algorithm::Myers, &old_chars, &new_chars) {
        let (tag, old, new) = op.as_tag_tuple();
        let inserted = &new_block[new_bytes[new.start]..new_bytes[new.end]];
        match tag {
            DiffTag::Equal => push(out, Edit::Keep(units(&old_chars[old]))),
            DiffTag::Delete => push(out, Edit::Delete(units(&old_chars[old]))),
            DiffTag::Insert => push(out, Edit::Insert(inserted)),
            DiffTag::Replace => {
                push(out, Edit::Delete(units(&old_chars[old])));
                push(out, Edit::Insert(inserted));
            }
        }
    }
}

/// push appends an edit, folding it into the previous one when both keep or
/// both delete, and dropping empty ones.
fn push<'a>(out: &mut Vec<Edit<'a>>, edit: Edit<'a>) {
    match (out.last_mut(), &edit) {
        (_, Edit::Keep(0)) | (_, Edit::Delete(0)) => {}
        (_, Edit::Insert(chunk)) if chunk.is_empty() => {}
        (Some(Edit::Keep(prev)), Edit::Keep(units)) => *prev += units,
        (Some(Edit::Delete(prev)), Edit::Delete(units)) => *prev += units,
        _ => out.push(edit),
    }
}

/// offsets returns the byte offset at which each line starts, plus the end.
fn offsets(lines: &[&str]) -> Vec<usize> {
    let mut at = Vec::with_capacity(lines.len() + 1);
    let mut sum = 0;
    at.push(0);
    for line in lines {
        sum += line.len();
        at.push(sum);
    }
    at
}

fn utf16_len(s: &str) -> u32 {
    s.encode_utf16().count() as u32
}
