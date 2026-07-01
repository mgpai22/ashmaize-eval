//! AshMaize Eval reference oracle.
//!
//! Reads ONE JSON request on stdin, writes ONE JSON object on stdout, exits 0 on success.
//! On rejected/invalid input it prints `{"error":"<reason>"}` and exits non-zero.
//!
//! This is the hidden reference (`spec/ABI.md`): the grader derives every expected output by
//! running this binary. Candidates may call it to check behavior but never see its source.
//!
//! ROM generation is fixed to the reference configuration `TwoStep { pre_size = 16384,
//! mixing_numbers = 4 }`; only `rom_size` (and the seed/preimage/loops/instrs) vary per request.

use std::io::Read;
use std::process::exit;

use ashmaize::{hash_with_state, unit_eval, Rom, RomGenerationType, UnitOp, UnitRequest};
use serde_json::{json, Value};

/// Fixed ROM pre-area size (power of two), matching the upstream reference vector.
const PRE_SIZE: usize = 16 * 1024; // 16384
/// Fixed number of mixing XOR passes, matching the upstream reference vector.
const MIXING_NUMBERS: usize = 4;
/// Largest ROM the eval permits (the upstream reference vector's 10 MiB).
const MAX_ROM_SIZE: i64 = 10 * 1024 * 1024; // 10485760

fn fail(reason: &str) -> ! {
    println!("{}", json!({ "error": reason }));
    exit(1);
}

fn rom_gen() -> RomGenerationType {
    RomGenerationType::TwoStep {
        pre_size: PRE_SIZE,
        mixing_numbers: MIXING_NUMBERS,
    }
}

fn to_hex(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push_str(&format!("{:02x}", b));
    }
    s
}

fn hex_nibble(c: u8, field: &str) -> u8 {
    match c {
        b'0'..=b'9' => c - b'0',
        b'a'..=b'f' => c - b'a' + 10,
        _ => fail(&format!("{}: non-lowercase-hex character", field)),
    }
}

/// Strict hex: lowercase only, even length, `[0-9a-f]` only. Uppercase is rejected.
fn decode_hex(s: &str, field: &str) -> Vec<u8> {
    let b = s.as_bytes();
    if b.len() % 2 != 0 {
        fail(&format!("{}: odd-length hex", field));
    }
    let mut out = Vec::with_capacity(b.len() / 2);
    let mut i = 0;
    while i < b.len() {
        out.push((hex_nibble(b[i], field) << 4) | hex_nibble(b[i + 1], field));
        i += 1 + 1;
    }
    out
}

fn require_str<'a>(req: &'a Value, field: &str) -> &'a str {
    match req.get(field) {
        Some(Value::String(s)) => s,
        Some(_) => fail(&format!("field {} must be a string", field)),
        None => fail(&format!("missing field: {}", field)),
    }
}

fn require_hex(req: &Value, field: &str) -> Vec<u8> {
    decode_hex(require_str(req, field), field)
}

/// Parse a JSON integer field; rejects floats, missing fields, and non-numbers.
fn require_int(req: &Value, field: &str) -> i64 {
    match req.get(field) {
        Some(v) => match v.as_i64() {
            Some(n) => n,
            None => fail(&format!("field {} must be an integer", field)),
        },
        None => fail(&format!("missing field: {}", field)),
    }
}

/// Validate `rom_size`: positive, a multiple of 64, and within the eval cap.
fn require_rom_size(req: &Value) -> usize {
    let n = require_int(req, "rom_size");
    if n <= 0 {
        fail("rom_size must be positive");
    }
    if n % 64 != 0 {
        fail("rom_size must be a multiple of 64");
    }
    if n > MAX_ROM_SIZE {
        fail("rom_size exceeds the maximum (10485760)");
    }
    n as usize
}

/// Parse an exactly-8-byte hex field as a big-endian u64 (the ABI's `unit` value encoding).
fn require_u64_hex(req: &Value, field: &str) -> u64 {
    let bytes = require_hex(req, field);
    if bytes.len() != 8 {
        fail(&format!("{} must be exactly 16 hex digits (8 bytes)", field));
    }
    u64::from_be_bytes(<[u8; 8]>::try_from(bytes.as_slice()).unwrap())
}

fn opt_u64_hex(req: &Value, field: &str) -> Option<u64> {
    if req.get(field).is_some() {
        Some(require_u64_hex(req, field))
    } else {
        None
    }
}

fn handle_hash(req: &Value) {
    let preimage = require_hex(req, "preimage_hex");
    let rom_seed = require_hex(req, "rom_seed_hex");
    let rom_size = require_rom_size(req);
    let nb_loops = require_int(req, "nb_loops");
    let nb_instrs = require_int(req, "nb_instrs");
    if nb_loops < 2 {
        fail("nb_loops must be >= 2");
    }
    if nb_instrs < 256 {
        fail("nb_instrs must be >= 256");
    }

    let rom = Rom::new(&rom_seed, rom_gen(), rom_size);
    let st = hash_with_state(&preimage, &rom, nb_loops as u32, nb_instrs as u32);
    println!(
        "{}",
        json!({
            "hash_hex": to_hex(&st.hash),
            "reg_digest_hex": to_hex(&st.reg_digest),
            "rom_digest_hex": to_hex(&st.rom_digest),
        })
    );
}

fn handle_rom_digest(req: &Value) {
    let rom_seed = require_hex(req, "rom_seed_hex");
    let rom_size = require_rom_size(req);
    let rom = Rom::new(&rom_seed, rom_gen(), rom_size);
    println!("{}", json!({ "rom_digest_hex": to_hex(&rom.digest_bytes()) }));
}

fn handle_unit(req: &Value) {
    let instr = require_str(req, "instr").to_string();
    let a = require_u64_hex(req, "a_hex");

    // Unary (no second operand).
    if let Some(op) = match instr.as_str() {
        "ISqrt" => Some(UnitOp::ISqrt),
        "Neg" => Some(UnitOp::Neg),
        "BitRev" => Some(UnitOp::BitRev),
        _ => None,
    } {
        let out = unit_eval(UnitRequest { op, a, b: 0, special1: 0, shift: 0 });
        emit_unit(out);
    }

    // Rotates: shift comes from `shift` (0..=31), mirroring the VM's decoded r1.
    if let Some(op) = match instr.as_str() {
        "RotL" => Some(UnitOp::RotL),
        "RotR" => Some(UnitOp::RotR),
        _ => None,
    } {
        let shift = require_int(req, "shift");
        if !(0..=31).contains(&shift) {
            fail("shift must be in 0..=31");
        }
        let out = unit_eval(UnitRequest { op, a, b: 0, special1: 0, shift: shift as u32 });
        emit_unit(out);
    }

    // Binary ops.
    let op = match instr.as_str() {
        "Add" => UnitOp::Add,
        "Mul" => UnitOp::Mul,
        "MulH" => UnitOp::MulH,
        "Xor" => UnitOp::Xor,
        "Div" => UnitOp::Div,
        "Mod" => UnitOp::Mod,
        "And" => UnitOp::And,
        "Hash0" => UnitOp::Hash(0),
        "Hash1" => UnitOp::Hash(1),
        "Hash2" => UnitOp::Hash(2),
        "Hash3" => UnitOp::Hash(3),
        "Hash4" => UnitOp::Hash(4),
        "Hash5" => UnitOp::Hash(5),
        "Hash6" => UnitOp::Hash(6),
        "Hash7" => UnitOp::Hash(7),
        other => fail(&format!("unsupported instr: {}", other)),
    };
    let b = require_u64_hex(req, "b_hex");
    let special1 = opt_u64_hex(req, "special1_hex");

    // Div/Mod by zero requires the special1 fallback to be supplied.
    let special1 = match (instr.as_str(), b) {
        ("Div", 0) | ("Mod", 0) => match special1 {
            Some(v) => v,
            None => fail("special1_hex is required when Div/Mod has a zero divisor"),
        },
        _ => special1.unwrap_or(0),
    };

    let out = unit_eval(UnitRequest { op, a, b, special1, shift: 0 });
    emit_unit(out);
}

fn emit_unit(out: u64) -> ! {
    println!("{}", json!({ "out_hex": format!("{:016x}", out) }));
    exit(0);
}

fn main() {
    let mut buf = Vec::new();
    if std::io::stdin().read_to_end(&mut buf).is_err() {
        fail("failed to read stdin");
    }
    let req: Value = match serde_json::from_slice(&buf) {
        Ok(v) => v,
        Err(e) => fail(&format!("malformed JSON: {}", e)),
    };

    match req.get("op").and_then(Value::as_str) {
        Some("hash") => handle_hash(&req),
        Some("rom_digest") => handle_rom_digest(&req),
        Some("unit") => handle_unit(&req),
        Some(other) => fail(&format!("unknown op: {}", other)),
        None => fail("missing or non-string op"),
    }
}
