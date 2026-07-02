# ABI — the interface your implementation must expose

Your implementation (and the reference oracle) is a program that reads **one JSON object on
stdin**, writes **one JSON object on stdout**, and exits. On success, exit code `0`. On
rejected/invalid input, print `{"error":"<reason>"}` and **exit non-zero**.

All byte strings are **lowercase hex**, no `0x`, even length. Numbers are JSON integers. This
JSON-over-stdio contract keeps the task language-agnostic and the grader simple. The grader feeds
each request to both your program and the oracle and compares the named fields **exactly** (string
equality), so your output must be canonical: only the fields below, lowercase hex, no extra keys.

There are three request kinds: `hash`, `rom_digest`, `unit`. ROM generation is fixed to
`TwoStep { pre_size = 16384, mixing_numbers = 4 }` (see `ASHMAIZE.md` §1).

---

## `hash` — full pipeline (graded in `replay`, 65%)

Request:
```json
{
  "op": "hash",
  "preimage_hex": "68656c6c6f",
  "rom_seed_hex": "313233",
  "rom_size": 65536,
  "nb_loops": 8,
  "nb_instrs": 256
}
```
Response — all three are 128-hex-char (64-byte) Blake2b-512 digests:
```json
{
  "hash_hex": "…",          // final output hash (ASHMAIZE.md §10)
  "reg_digest_hex": "…",    // B512 of the final 32 registers, taken before finalize (§10)
  "rom_digest_hex": "…"     // B512 of the generated ROM (§3)
}
```

## `rom_digest` — ROM generation only (graded in `procedural`, 25%)

Request:
```json
{ "op": "rom_digest", "rom_seed_hex": "313233", "rom_size": 65536 }
```
Response:
```json
{ "rom_digest_hex": "…" }   // identical to the hash response's rom_digest_hex for the same seed/size
```

## `unit` — single instruction semantics (graded in `procedural`, 25%)

`a_hex`, `b_hex`, and `special1_hex` are **exactly 16 hex digits** (one `u64`, written most
significant byte first, i.e. `"0000000000000003"` is the number `3`). `out_hex` is the `u64` result
formatted the same way: `"%016x"`, lowercase.

Three request shapes by instruction class:

```json
// binary ops: Add, Mul, MulH, Xor, Div, Mod, And, Hash0 … Hash7
{ "op":"unit", "instr":"MulH", "a_hex":"0000000000000003", "b_hex":"ffffffffffffffff" }

// unary ops: ISqrt, Neg, BitRev
{ "op":"unit", "instr":"ISqrt", "a_hex":"000000000000001b" }

// rotate ops: RotL, RotR  (shift mirrors the VM's decoded r1, an integer 0..=31)
{ "op":"unit", "instr":"RotL", "a_hex":"0123456789abcdef", "shift":1 }
```
Response is always:
```json
{ "out_hex": "0000000000000002" }
```

### `unit` op semantics (see `ASHMAIZE.md` §6 for the VM context)

| `instr` | inputs | result |
|---|---|---|
| `Add` | a, b | wrapping `a + b` |
| `Mul` | a, b | low 64 bits of `a * b` |
| `MulH` | a, b | high 64 bits of unsigned `a * b` |
| `Xor` | a, b | `a ^ b` |
| `And` | a, b | `a & b` |
| `Div` | a, b, [special1] | `b == 0` → `special1`; else `a / b` |
| `Mod` | a, b, [special1] | `b == 0` → `special1`; else **`a / b`** (quotient, matching the reference quirk) |
| `Hash0`…`Hash7` | a, b | `N = 0..7`; `B512(LE64(a) || LE64(b))`, then the `N`-th 8-byte chunk as a little-endian `u64` |
| `ISqrt` | a | integer floor `sqrt(a)` |
| `Neg` | a | bitwise NOT `~a` |
| `BitRev` | a | reverse all 64 bits of `a` |
| `RotL` | a, shift | `a` rotate-left by `shift` (`0..=31`) |
| `RotR` | a, shift | `a` rotate-right by `shift` (`0..=31`) |

`special1_hex` is optional for binary ops and ignored unless `Div`/`Mod` hits a zero divisor — in
which case it is **required** (its absence is an error, see below). It plays the role of the VM's
Special1 value, which a real `Div`/`Mod` would derive from `PROG_DIGEST`; the `unit` ABI passes it
explicitly so single ops are testable in isolation.

---

## Errors / adversarial (graded in `adversarial`, 10%)

A correct implementation prints `{"error":"<reason>"}` and exits **non-zero** for any invalid
input. Crashing with no JSON, hanging, or — worst — producing a normal-looking success response for
invalid input all fail the `adversarial` section. You must reject at least:

- malformed JSON; a JSON value that is not an object; missing `op`; unknown `op`.
- any missing required field; a hex field that is not a string.
- a hex field that is odd-length, contains non-hex characters, or contains **uppercase** letters.
- any integer field (`rom_size`, `nb_loops`, `nb_instrs`, `shift`) that is not a **JSON integer
  representable as a signed 64-bit value**: floats are invalid even when integral (`65536.0` ≠
  `65536`), and so are strings, booleans, and magnitudes ≥ 2^63 (e.g. `18446744073709551616`).
  Reject these **before** doing any size-dependent work — a huge `nb_instrs` must produce a fast
  `{"error":…}`, not an allocation attempt or a near-infinite loop.
- `rom_size` that is not positive, not a multiple of 64, or greater than `10485760`.
- `nb_loops < 2`; `nb_instrs < 256`.
- `unit` with an unknown `instr` (e.g. `"Sub"`).
- `unit` `Div`/`Mod` with `b_hex == "0000000000000000"` and **no** `special1_hex`.
- `unit` `RotL`/`RotR` with `shift` outside `0..=31`.

## Notes

- The grader computes "expected" by running the **oracle** on the same request; nothing is
  hard-coded, so the corpus stays valid as the spec is pinned.
- `rom_digest_hex` for a given `(rom_seed_hex, rom_size)` is identical whether produced by `hash` or
  `rom_digest` — same ROM, same digest.
- `preimage_hex` and `rom_seed_hex` may be the **empty string** `""` (zero bytes) — that is valid
  input, not an error. Only the `unit` value fields (`a_hex`, `b_hex`, `special1_hex`) have a fixed
  length (exactly 16 hex digits).
- Determinism is mandatory: identical inputs must yield byte-identical outputs across runs/machines.
