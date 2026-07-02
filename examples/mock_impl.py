#!/usr/bin/env python3
# PLACEHOLDER, NOT AshMaize.
#
# A deterministic stand-in that speaks the ABI (spec/ABI.md) so you can test the
# grader pipeline before/independently of the real oracle. It enforces the SAME input
# validation as the oracle (so it rejects every adversarial case and accepts every valid
# one), but its success outputs are fake digests rather than AshMaize values.
#
#   mock-as-oracle AND mock-as-agent  -> ~100% (it matches itself).
#   real-oracle as oracle, mock as agent -> fails replay/procedural (fake != reference),
#                                           still passes adversarial (correct rejection).
import sys
import json
import hashlib

MAX_ROM_SIZE = 10 * 1024 * 1024  # 10485760

BINARY_OPS = {"Add", "Mul", "MulH", "Xor", "Div", "Mod", "And",
              "Hash0", "Hash1", "Hash2", "Hash3", "Hash4", "Hash5", "Hash6", "Hash7"}
UNARY_OPS = {"ISqrt", "Neg", "BitRev"}
ROTATE_OPS = {"RotL", "RotR"}


def fail(reason):
    print(json.dumps({"error": reason}))
    sys.exit(1)


def fake(nbytes, *parts):
    """Deterministic fake hex of nbytes bytes from the given parts (NOT AshMaize)."""
    m = hashlib.sha512()
    for p in parts:
        m.update(str(p).encode())
    out = b""
    counter = 0
    while len(out) < nbytes:
        h = hashlib.sha512()
        h.update(m.digest())
        h.update(counter.to_bytes(4, "little"))
        out += h.digest()
        counter += 1
    return out[:nbytes].hex()


def decode_hex(s, field):
    if not isinstance(s, str):
        fail(f"field {field} must be a string")
    if len(s) % 2 != 0:
        fail(f"{field}: odd-length hex")
    for c in s:
        if c not in "0123456789abcdef":
            fail(f"{field}: non-lowercase-hex character")
    return bytes.fromhex(s) if s else b""


def require_str(req, field):
    if field not in req:
        fail(f"missing field: {field}")
    v = req[field]
    if not isinstance(v, str):
        fail(f"field {field} must be a string")
    return v


def require_hex(req, field):
    return decode_hex(require_str(req, field), field)


def require_int(req, field):
    if field not in req:
        fail(f"missing field: {field}")
    v = req[field]
    # bool is a subclass of int; reject it. Floats are not integers. Match the oracle's
    # signed-64-bit parse: anything outside i64 range is not a valid integer field.
    if isinstance(v, bool) or not isinstance(v, int):
        fail(f"field {field} must be an integer")
    if not (-(1 << 63) <= v < (1 << 63)):
        fail(f"field {field} must be an integer")
    return v


def require_rom_size(req):
    n = require_int(req, "rom_size")
    if n <= 0:
        fail("rom_size must be positive")
    if n % 64 != 0:
        fail("rom_size must be a multiple of 64")
    if n > MAX_ROM_SIZE:
        fail("rom_size exceeds the maximum (10485760)")
    return n


def require_u64_hex(req, field):
    b = require_hex(req, field)
    if len(b) != 8:
        fail(f"{field} must be exactly 16 hex digits (8 bytes)")
    return int.from_bytes(b, "big")


def handle_hash(req):
    pre = require_str(req, "preimage_hex"); decode_hex(pre, "preimage_hex")
    seed = require_str(req, "rom_seed_hex"); decode_hex(seed, "rom_seed_hex")
    rom_size = require_rom_size(req)
    nb_loops = require_int(req, "nb_loops")
    nb_instrs = require_int(req, "nb_instrs")
    if nb_loops < 2:
        fail("nb_loops must be >= 2")
    if nb_instrs < 256:
        fail("nb_instrs must be >= 256")
    key = ("hash", pre, seed, rom_size, nb_loops, nb_instrs)
    print(json.dumps({
        "hash_hex": fake(64, "h", *key),
        "reg_digest_hex": fake(64, "r", *key),
        "rom_digest_hex": fake(64, "rom", seed, rom_size),
    }))


def handle_rom_digest(req):
    seed = require_str(req, "rom_seed_hex"); decode_hex(seed, "rom_seed_hex")
    rom_size = require_rom_size(req)
    print(json.dumps({"rom_digest_hex": fake(64, "rom", seed, rom_size)}))


def handle_unit(req):
    instr = require_str(req, "instr")
    a = require_u64_hex(req, "a_hex")
    if instr in UNARY_OPS:
        out = fake(8, "unit", instr, a)
    elif instr in ROTATE_OPS:
        if "shift" not in req:
            fail("missing field: shift")
        shift = require_int(req, "shift")
        if not (0 <= shift <= 31):
            fail("shift must be in 0..=31")
        out = fake(8, "unit", instr, a, shift)
    elif instr in BINARY_OPS:
        b = require_u64_hex(req, "b_hex")
        special1 = require_u64_hex(req, "special1_hex") if "special1_hex" in req else None
        if instr in ("Div", "Mod") and b == 0 and special1 is None:
            fail("special1_hex is required when Div/Mod has a zero divisor")
        out = fake(8, "unit", instr, a, b, special1)
    else:
        fail(f"unsupported instr: {instr}")
    print(json.dumps({"out_hex": out}))


def main():
    try:
        req = json.load(sys.stdin)
    except Exception as e:  # noqa: BLE001
        fail(f"malformed JSON: {e}")
    if not isinstance(req, dict):
        fail("request must be a JSON object")

    op = req.get("op")
    if op == "hash":
        handle_hash(req)
    elif op == "rom_digest":
        handle_rom_digest(req)
    elif op == "unit":
        handle_unit(req)
    elif isinstance(op, str):
        fail(f"unknown op: {op}")
    else:
        fail("missing or non-string op")


if __name__ == "__main__":
    main()
