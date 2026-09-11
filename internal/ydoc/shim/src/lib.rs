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
/// keystrokes folded into the replacement at best. Here the common prefix and
/// suffix are left untouched and only the span between them is rewritten, so
/// an edit to one paragraph leaves concurrent edits to another alone.
///
/// It is a prefix/suffix trim rather than a real diff, so scattered changes
/// collapse into one span covering all of them. That is a smaller conflict
/// surface than replacing everything and a larger one than a Myers diff would
/// give; if it proves too coarse, this is the place to improve.
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
    let current: Vec<u16> = text.get_string(&doc.transact()).encode_utf16().collect();
    let desired: Vec<u16> = next.encode_utf16().collect();

    let (start, keep_tail) = trim(&current, &desired);
    let removed = current.len() - start - keep_tail;
    let inserted = &desired[start..desired.len() - keep_tail];

    {
        let mut txn = doc.transact_mut();
        if removed > 0 {
            text.remove_range(&mut txn, start as u32, removed as u32);
        }
        if !inserted.is_empty() {
            match String::from_utf16(inserted) {
                Ok(chunk) => text.insert(&mut txn, start as u32, &chunk),
                Err(e) => return err(&format!("replacement span: {e}")),
            }
        }
    }

    // Bound to a local so the transaction is dropped before doc is.
    let update = doc.transact().encode_state_as_update_v1(&before);
    ok(&update)
}

/// trim returns how much of the head the two share and how much of the tail,
/// without ever splitting a surrogate pair — a boundary inside one would make
/// the indices meaningless to a client counting UTF-16 code units.
fn trim(current: &[u16], desired: &[u16]) -> (usize, usize) {
    let mut start = 0;
    while start < current.len() && start < desired.len() && current[start] == desired[start] {
        start += 1;
    }
    if start > 0 && is_high_surrogate(current[start - 1]) {
        start -= 1;
    }

    let mut tail = 0;
    while tail < current.len() - start
        && tail < desired.len() - start
        && current[current.len() - 1 - tail] == desired[desired.len() - 1 - tail]
    {
        tail += 1;
    }
    if tail > 0 && is_low_surrogate(desired[desired.len() - tail]) {
        tail -= 1;
    }
    (start, tail)
}

fn is_high_surrogate(unit: u16) -> bool {
    (0xD800..=0xDBFF).contains(&unit)
}

fn is_low_surrogate(unit: u16) -> bool {
    (0xDC00..=0xDFFF).contains(&unit)
}
