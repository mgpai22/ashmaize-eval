# Scenario corpus

Each `.json` file is one graded case. The grader runs every case's request through the **oracle**
(to compute the expected output) and the **candidate**, then compares the `compare` fields exactly
— except `adversarial` cases, which only run the candidate and check that it *rejects* the input.

Nothing is hard-coded: expected outputs come from the oracle at grade time, so the corpus stays
valid as long as the oracle is correct (it reproduces the `spec/ASHMAIZE.md` §11 reference vector).

## Scenario schema

```json
{
  "id": "replay-0002-hello-64k",   // unique, non-empty; also the filename
  "section": "replay",             // replay (65%) | procedural (25%) | adversarial (10%)
  "weight": 1.0,                    // relative weight within section (procedural-rom = 2.5, Hash* = 0.5)
  "request": { "op": "hash", "…": "…" },   // the JSON request (see spec/ABI.md)
  "compare": ["rom_digest_hex", "reg_digest_hex", "hash_hex"]  // ROM->VM->finalize: first mismatch localizes the stage
}
```

For `adversarial` cases set `"expect_error": true` and omit `compare`. Such a case carries **either**
a structured `request` (e.g. a negative `rom_size`) **or** a `raw_request` string sent to the
candidate verbatim (e.g. malformed JSON) — exactly one of the two. The grader validates the whole
corpus (unique ids, known sections, valid `request.op`, well-formed error cases) before running.

## The corpus (73 cases)

- **`replay/` — 17 full `hash` runs.** The headline section; exact-match on all three digests. The
  `compare` order is `rom_digest_hex, reg_digest_hex, hash_hex` (ROM → VM → finalize) so the grader's
  first-mismatch report localizes the failing stage. Cases vary `preimage`, `rom_seed`, `rom_size`,
  `nb_loops` (2/3/8), and `nb_instrs` (256/257) so a hardcoded size, loop count, or off-by-one
  program length is caught. **Five use non-power-of-two `rom_size`** (`rom_size/64` ∈ {65, 257
  (prime), 1025, 3072}) to exercise the `rom.at` byte-offset modulo path that power-of-two sizes
  mask — a real divergence class surfaced by a model run (see `FAILURES.md`). One case is the full
  §11 reference vector at 10 MiB (`nb_loops=8`), the end-to-end acceptance boundary.
- **`procedural/` — 36 localizing cases.**
  - `procedural-rom-*` (10, weight 2.5 each): `rom_digest` only, across seeds and sizes (incl. the
    10 MiB reference vector and two non-power-of-two chunk counts — 65 and prime 257) so ROM
    generation — Argon2 `hprime` incl. the non-64 tail block, the `TwoStep` mix, and the `B512` ROM
    digest — is isolated from VM execution and from the `rom.at` memory path.
  - `procedural-op-*` (26): `unit` single-op semantics covering every instruction and its traps:
    `Add` wrap, `Mul` low bits, `MulH` high bits, `Xor`/`And`, `Div`, `Div`-by-zero→special1,
    `Mod` (the division quirk), `Mod`-by-zero→special1, `Hash0`–`Hash7` chunk selection (the eight
    `Hash*` cases carry weight 0.5 so they don't swamp the other ops), `ISqrt`, `Neg` (bitwise),
    `BitRev`, and `RotL`/`RotR` at shifts 0/1/31.
- **`adversarial/` — 20 rejection cases.** Malformed JSON, unknown/missing `op`, missing fields,
  non-hex / odd-length / uppercase hex, `rom_size` that is non-positive / non-multiple-of-64 /
  oversized / non-integer / wrong-typed, a numeric (non-string) hex field, a string rotate `shift`,
  an empty `unit` operand, `nb_loops < 2`, `nb_instrs < 256`, an unsupported `unit.instr`,
  `Div`/`Mod`-by-zero with no `special1_hex`, and a rotate `shift > 31`. The candidate must emit
  `{"error":…}` and exit non-zero (or at least exit non-zero); a success response, or hanging, fails.

## Why these three sections

`replay` is the real test — byte-identical full hashes. But a single wrong digest there tells you
nothing about *where* you diverged, so `procedural` pins the failure to ROM generation or a specific
op, and `adversarial` confirms the implementation is a disciplined ABI citizen rather than a fragile
happy-path script. Weights (65/25/10) keep the headline on `replay` while rewarding localizable
correctness; `procedural-rom` cases are weighted 2.5× the trivial single-op cases so hard ROM
generation is worth roughly half the procedural section. Adversarial is capped at 10% so a
validate-only stub cannot look one-fifth correct.

`rom_size` stays small (≤ 256 KiB for most `replay`) so the corpus grades in seconds while still
exercising ROM generation and memory-hard indexing; the single 10 MiB full `hash` replay plus the
10 MiB `rom_digest` case anchor the published reference vector.
