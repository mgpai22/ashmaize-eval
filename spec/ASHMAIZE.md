# AshMaize — specification (for the eval)

> **Provenance.** AshMaize is Input Output's (IOHK) algorithm —
> [`input-output-hk/ce-ashmaize`](https://github.com/input-output-hk/ce-ashmaize)
> (`MIT OR Apache-2.0`). This file is a self-contained spec write-up **for the eval**, pinned to
> upstream commit `58d6a1fe3df2582e14d53b67292ce8a36d90e7e6`. Every constant and rule below has
> been confirmed against that source and is reproduced byte-for-byte by the reference oracle. The
> reference vector in §11 is the acceptance check.

AshMaize is a CPU-friendly, ASIC-resistant, **memory-hard** proof-of-work function, in the spirit
of RandomX. It is a small **virtual machine** whose program and a large read-only **ROM** are
derived deterministically from seeds, executed over many loops, and folded into a 64-byte digest.

Everything here is **deterministic**: identical inputs must yield byte-identical outputs. There is
no entropy, no wall-clock, no parallel nondeterminism. All multi-byte integers are **little-endian**
unless explicitly stated otherwise.

---

## 0. Primitives

AshMaize is built from two primitives only: **Blake2b** and **Argon2 `hprime`** (the Argon2
variable-length hash `H'`). Get these byte-exact and most of the battle is won.

### 0.1 Blake2b

- `Blake2b-512(data...)` → 64-byte digest. Written below as `B512(...)`. Used everywhere as a
  streaming context (`new → update(a) → update(b) → … → finalize`); concatenation of updates is
  identical to hashing the concatenation.
- `Blake2b-256(data...)` → 32-byte digest. Written `B256(...)`. Used once, for the ROM seed.
- `Blake2bN(data...)` → an `N`-byte digest for arbitrary `1 ≤ N ≤ 64`. **This is real Blake2b with
  its output-length parameter set to `N`, not a truncation of Blake2b-512.** Blake2b mixes the
  digest length into its parameter block, so `Blake2b32(x) ≠ Blake2b512(x)[0..32]`. This matters
  for the tail block of `hprime` (§0.2).

### 0.2 Argon2 `hprime(output_len, input) → bytes`

`hprime` is the Argon2 `H'` variable-length hash. It produces `output_len` bytes from `input`:

```
hprime(L, input):
    if L <= 64:
        return Blake2bL( LE32(L) || input )            # single Blake2b, digest length = L
    V = B512( LE32(L) || input )                        # 64-byte block
    out = V[0..32]                                      # emit first 32 bytes
    remaining = L - 32
    while remaining > 64:
        V = B512(V)                                     # rehash the full 64-byte block
        out ||= V[0..32]                                # emit first 32 bytes
        remaining -= 32
    out ||= Blake2b_remaining( V )                      # tail: digest length = remaining (33..64)
    return out                                          # len(out) == L
```

Notes that trip people up:
- The input is **prefixed with `LE32(output_len)`** before the first hash (and only the first).
- The chain rehashes the **whole 64-byte** `V` each step but **emits only its first 32 bytes**.
- The final block hashes the last full `V` with a Blake2b whose **output length equals the leftover
  byte count** (which is always in `33..=64`), using §0.1's length-parameterized Blake2b.

`hprime` is the only source of "random" bytes in AshMaize. It is used to (a) fill the ROM pre-area,
(b) fill ROM offset bytes, (c) initialize the VM, (d) regenerate the program each loop, and (e) mix
the registers between loops.

---

## 1. Parameters

| Name | Value | Notes |
|---|---|---|
| `NB_REGS` | `32` | registers, indices are 5-bit (`REGS_BITS = 5`, `1 << 5 = 32`). |
| register | `u64` | `REGISTER_SIZE = 8` bytes, little-endian. |
| `INSTR_SIZE` | `20` | bytes per encoded instruction. |
| `DATASET_ACCESS_SIZE` | `64` | ROM "cache line": memory is touched in 64-byte chunks. |
| ROM generation | `TwoStep { pre_size = 16384, mixing_numbers = 4 }` | **fixed** for the eval (`pre_size` is a power of two). |
| KDF | Argon2 `hprime` (§0.2) | |
| Finalizer / digests | Blake2b-512 (§0.1) | |
| `rom_size` | per-request | bytes; must be `> 0` and a **multiple of 64**; the eval caps it at `10485760` (10 MiB). |
| `nb_loops` | per-request | outer execution loops; must be `≥ 2`. |
| `nb_instrs` | per-request | instructions per loop; must be `≥ 256`. |

`rom_size`, `nb_loops`, `nb_instrs` come from the request (see `ABI.md`). A correct implementation
reads them per call and never hardcodes production values. `pre_size` and `mixing_numbers` are
fixed at the values above for every request in this eval.

## 2. Preimage

The hash input ("preimage", a.k.a. *salt*) is supplied to you already assembled as `preimage_hex`.
Preimage construction (nonce, address, challenge id, …) is **out of scope** — you grade only the
ROM/VM/finalize pipeline over the given preimage bytes.

## 3. ROM generation (`TwoStep`)

Given `key` (the `rom_seed` bytes) and `rom_size`:

1. **Seed.** `seed = B256( LE32(rom_size) || key )` — a 32-byte value. (Note `rom_size` is cast to
   `u32` for this length prefix.)
2. **Pre-area.** `pre = hprime(pre_size, seed)` — `pre_size` bytes (here 16384). View it as
   `nb_source_chunks = pre_size / 64` chunks of 64 bytes.
3. **Offset diffs.** For `i` in `0..4`:
   `diff_i = B512( seed || "generation offset" || LE32(i) )`, split into 32 little-endian `u16`s.
   Concatenate to get `offsets_diff`, a list of `4 * 32 = 128` `u16`s.
   (`"generation offset"` is the 17 literal ASCII bytes, no quotes, no separators.)
4. **Offset bases.** `offsets = hprime( rom_size / 64, B512( seed || "generation offset base" ) )` —
   one byte per output chunk (`rom_size / 64` bytes), used as `u8` values.
5. **Fill.** Iterate output chunks `i = 0, 1, … (rom_size/64 - 1)`, each 64 bytes. Let
   `nb_source_chunks = pre_size / 64`:
   - base index `idx0 = i mod nb_source_chunks`; copy `pre[idx0*64 .. idx0*64+64]` into the chunk.
   - `start_idx = offsets[i mod len(offsets)] mod nb_source_chunks`.
   - for `d` in `1 .. mixing_numbers` (i.e. 3 passes when `mixing_numbers = 4`):
     `idx = (start_idx + offsets_diff[(d-1) mod 128]) mod nb_source_chunks` (the add wraps as
     `u32`); XOR `pre[idx*64 .. idx*64+64]` into the chunk (64-byte XOR).
   - feed the finished 64-byte chunk into the running ROM digest.
6. **ROM digest.** `rom_digest = B512(chunk_0 || chunk_1 || …)`, i.e. a single Blake2b-512 context
   updated with every generated 64-byte chunk, in order. Exposed by the ABI as `rom_digest_hex`.

The ROM is the byte string of all the chunks concatenated, length `rom_size`.

### 3.1 ROM access — `rom.at(addr)`

Memory reads index the ROM in 64-byte lines, but **the index is a byte offset, not a line number**,
and the 64-bit address is **truncated to `u32` before the modulo**:

```
rom.at(addr):                                   # addr: the operand's u64 literal (§7)
    i     = addr mod 2^32                       # truncate to u32 FIRST (take the low 32 bits)
    start = i mod (rom_size / 64)               # a BYTE offset in 0 .. rom_size/64
    return ROM[start .. start + 64]             # 64 bytes
```

Two deliberate, easy-to-miss quirks — match both exactly:

1. `start` is a **byte offset**, not a chunk index: consecutive `start` values overlap heavily
   (they differ by 1 byte, not 64). Do **not** multiply by 64.
2. The address is reduced **`u32`-first**: `(addr mod 2^32) mod (rom_size/64)`, *not*
   `addr mod (rom_size/64)`. When `rom_size/64` is a power of two the two orders happen to agree
   (`2^32` is a multiple of `rom_size/64`, so the truncation is invisible), but for any other size
   they diverge for almost every address — e.g. `addr = 2^32` gives `start = 0` here, while a
   direct 64-bit modulo by `65` would give `4`. Doing the modulo on the full 64-bit literal is the
   single most common way otherwise-correct implementations fail non-power-of-two `rom_size`.

## 4. VM state and initialization

VM state: 32 `u64` registers `reg[0..32]`; an instruction pointer `ip` (`u32`); two Blake2b-512
**streaming contexts** `PROG_DIGEST` and `MEM_DIGEST`; a 64-byte `PROG_SEED`; a `memory_counter`
(`u32`); a `loop_counter` (`u32`). `ip`, `memory_counter`, `loop_counter` all start at `0`.

Initialize from the ROM digest and the preimage:

```
init = hprime( 32*8 + 3*64 , rom_digest || preimage )     # 256 + 192 = 448 bytes
```

Map `init` left to right:
- bytes `0 .. 256`: the 32 registers, each `reg[k] = LE_u64( init[k*8 .. k*8+8] )`.
- bytes `256 .. 320` (64 bytes): seed `PROG_DIGEST` — i.e. `PROG_DIGEST = B512_context.update(these 64 bytes)`.
- bytes `320 .. 384` (64 bytes): seed `MEM_DIGEST` likewise.
- bytes `384 .. 448` (64 bytes): `PROG_SEED`.

`PROG_DIGEST`/`MEM_DIGEST` are **not** finalized here; they are live contexts that keep absorbing
data throughout execution.

## 5. Program generation, encoding, decoding

### 5.1 Generation (every loop)

At the start of each loop the entire program is **regenerated** (not permuted) from the current
`PROG_SEED`:

```
program_bytes = hprime( nb_instrs * 20 , PROG_SEED )       # nb_instrs * INSTR_SIZE bytes
```

The instruction executed at step `ip` is the 20-byte slice at
`program_bytes[ (ip * 20) mod (nb_instrs*20) .. +20 ]`. `ip` is a single counter that increments by
1 after every instruction and is **not reset** between loops (it wraps cleanly, so step `ip` always
selects instruction `ip mod nb_instrs`).

### 5.2 Encoding (20 bytes)

| Bytes | Field | Meaning |
|---|---|---|
| `[0]` | opcode | selects the instruction (table §6). |
| `[1]` high nibble | `src1` source | operand-1 source type (§5.3). `byte1 >> 4`. |
| `[1]` low nibble | `src2` source | operand-2 source type. `byte1 & 0x0f`. |
| `[2..4]` | register bitfield | big-endian `u16` `rs = (byte2 << 8) | byte3`. |
| `[4..12]` | `lit1` | little-endian `u64` literal / address for operand 1. |
| `[12..20]` | `lit2` | little-endian `u64` literal / address for operand 2. |

From `rs` decode three 5-bit register indices (mask `0x1f`):
`r1 = (rs >> 10) & 0x1f`, `r2 = (rs >> 5) & 0x1f`, `r3 = rs & 0x1f`. `r3` is the destination.

### 5.3 Operand sources

Each 4-bit source nibble maps to:

| Nibble | Source | Value |
|---|---|---|
| `0..=4` | Register | `reg[r1]` for operand 1, `reg[r2]` for operand 2. |
| `5..=8` | Memory | a ROM read at address `lit1` (op1) or `lit2` (op2); see §7. |
| `9..=12` | Literal | `lit1` (op1) or `lit2` (op2). |
| `13` | Special1 | `PROG_DIGEST` snapshot value; see §8. |
| `14..=15` | Special2 | `MEM_DIGEST` snapshot value; see §8. |

## 6. Instruction set

The opcode byte selects an instruction by **range** (non-uniform probabilities). All arithmetic is
on `u64` with **wrapping** semantics unless stated. Two-operand ops write `reg[r3] = f(src1, src2)`;
one-operand ops write `reg[r3] = f(src1)`.

| Opcode range | Instruction | Operands | Semantics |
|---|---|---|---|
| `0..40` | `Add` | 2 | `src1 + src2` (wrapping) |
| `40..80` | `Mul` | 2 | low 64 bits of `src1 * src2` (wrapping) |
| `80..96` | `MulH` | 2 | high 64 bits of unsigned `src1 * src2` (`(u128(src1)*u128(src2)) >> 64`) |
| `96..112` | `Div` | 2 | if `src2 == 0` → Special1 value (§8); else `src1 / src2` |
| `112..128` | `Mod` | 2 | if `src2 == 0` → Special1 value (§8); else **`src1 / src2`** (see below) |
| `128..138` | `ISqrt` | 1 | integer floor square root of `src1` |
| `138..148` | `BitRev` | 1 | reverse the 64 bits of `src1` |
| `148..188` | `Xor` | 2 | `src1 ^ src2` |
| `188..204` | `RotL` | 1 | `src1` rotate-left by `r1` (the decoded operand-1 register index, `0..=31`) |
| `204..220` | `RotR` | 1 | `src1` rotate-right by `r1` |
| `220..240` | `Neg` | 1 | bitwise NOT `~src1` (**not** two's-complement negation) |
| `240..248` | `And` | 2 | `src1 & src2` |
| `248..=255` | `Hash[N]`, `N = opcode - 248` | 2 | `B512( LE64(src1) || LE64(src2) )`, then take the `N`-th 8-byte chunk as a little-endian `u64` (`0 ≤ N ≤ 7`) |

**Deliberate reference quirks — match them exactly, do not "fix" them:**
- `Mod` returns `src1 / src2` (integer **division**), *not* `src1 % src2`. The opcode is named
  "Mod" but computes the quotient.
- Zero-divisor `Div`/`Mod` returns the **Special1 value** (§8) directly — not `0`, not a panic, and
  not "divide by 1".
- `Neg` is bitwise complement `~src1`, *not* `(~src1)+1`.
- `RotL`/`RotR` rotate by `r1` — the **decoded register index of operand 1** (always `0..=31`) —
  *not* by `src2` or by a literal. One-operand instructions ignore `src2`/operand-2 entirely.
- Rotates are 64-bit rotates (`u64::rotate_left/right`).

## 7. Memory access

When an operand's source is **Memory**, read the ROM at the operand's literal as the address:

```
addr = lit1 (operand 1) or lit2 (operand 2)        # the 64-bit literal, used as the address
line = rom.at(addr)                                 # 64 bytes; u32-truncates addr first (§3.1)
MEM_DIGEST.update(line)                              # absorb the full 64-byte line
memory_counter += 1                                  # wrapping u32
idx = (memory_counter mod 8) * 8                     # AFTER the increment
value = LE_u64( line[idx .. idx + 8] )               # the operand value
```

So: the **whole 64-byte line** feeds the memory digest, but the **operand value** is only the
8-byte sub-chunk selected by the post-increment `memory_counter mod 8`. The counter is bumped
**before** computing `idx`, and it persists across instructions and loops. The address reduction
(`u32`-truncate, then modulo — §3.1) happens inside `rom.at`.

## 8. Special values

Computed on demand from live digest contexts, by **cloning** the context and finalizing the clone
(the live context is never disturbed):

- **Special1** = `LE_u64( clone(PROG_DIGEST).finalize()[0..8] )`.
- **Special2** = `LE_u64( clone(MEM_DIGEST).finalize()[0..8] )`.

The same Special1 value is the zero-divisor fallback for `Div`/`Mod`.

## 9. Per-instruction and per-loop bookkeeping

**Each instruction**, after computing the result and writing `reg[r3]`: absorb the 20 program
bytes of that instruction into `PROG_DIGEST` (`PROG_DIGEST.update(instr_bytes)`), then `ip += 1`.
(Memory operands separately absorb their 64-byte line into `MEM_DIGEST`, in §7.)

**Each loop** runs: regenerate program (§5.1) → execute `nb_instrs` instructions → `PostInstructions`:

```
PostInstructions():
    s = wrapping_sum(reg[0..32])                                 # u64
    prog_value = clone(PROG_DIGEST).update(LE64(s)).finalize()   # 64 bytes; clone, don't disturb
    mem_value  = clone(MEM_DIGEST ).update(LE64(s)).finalize()   # 64 bytes
    mixing = B512( prog_value || mem_value || LE32(loop_counter) )
    bytes = hprime( 32 * 32 * 8 , mixing )                       # 8192 bytes
    # 32 rounds; each round XORs one 8-byte LE word into each of the 32 registers
    for round in 0..32:
        for k in 0..32:
            reg[k] ^= LE_u64( bytes[(round*32 + k)*8 .. +8] )
    PROG_SEED = prog_value
    loop_counter += 1                                            # wrapping u32
```

`prog_value`/`mem_value` are computed from **clones**, so the live `PROG_DIGEST`/`MEM_DIGEST`
continue to accumulate across loops. `PROG_SEED` becomes `prog_value`, feeding the next loop's
program regeneration.

## 10. Finalization

After all `nb_loops` loops:

```
hash = B512( finalize(PROG_DIGEST) || finalize(MEM_DIGEST) || LE32(memory_counter)
             || LE64(reg[0]) || LE64(reg[1]) || … || LE64(reg[31]) )           # 64 bytes
```

This 64-byte value is `hash_hex`.

**`reg_digest_hex` (eval-only).** For partial credit the ABI also exposes a digest of the final
register file, taken **immediately before** the finalization above:
`reg_digest = B512( LE64(reg[0]) || LE64(reg[1]) || … || LE64(reg[31]) )`. It is an eval convenience
(not part of upstream); compute it from the post-loop registers, before the `hash` finalize.

## 11. Reference vector (acceptance check)

```
rom_seed = "123"        # rom_seed_hex = "313233"
preimage = "hello"      # preimage_hex = "68656c6c6f"
rom_size = 10485760     # 10 MiB, TwoStep{ pre_size = 16384, mixing_numbers = 4 }
nb_loops = 8 , nb_instrs = 256

hash_hex = 389401e43b60d3ad0962443d59ab7cab7cb7c8c41d2b85a8dad9ff47eab6619e
           e79c38e63d36f8c7960f420095b955b1c0dced4dc36a8cdfaf5deedc399fb4f3
```

(Concatenate the two lines; the hash is 128 hex chars.) If your implementation reproduces this
exactly, your ROM generation, VM, and finalizer are correct end to end. The oracle exposes the same
configuration via the `hash` request in `ABI.md`.

## 12. Difficulty check (informational)

In production a solution is accepted when the output hash clears a difficulty mask. The eval does
**not** ask you to find a solution — it grades whether your pipeline computes the **same digests**
the reference does for fixed inputs.

---

### Divergence hot-spots (where plausible code silently diverges)
- [ ] Argon2 `hprime` byte-exact: `LE32(len)` prefix, 64-byte chain emitting 32 bytes/step, and the
      **length-parameterized** Blake2b tail block (§0.2). Wrong tail = everything downstream wrong.
- [ ] Blake2b-256 vs Blake2b-512 vs Blake2bN are distinct (output length is in the parameter block).
- [ ] ROM seed is `B256(LE32(rom_size) || key)`; ROM digest is `B512` over generated chunks in order.
- [ ] `rom.at` reduces the address **`u32`-first**: `(addr mod 2^32) mod (rom_size/64)`, used as a
      **byte offset** — not `addr mod (rom_size/64)`, and not `(… ) * 64`. Test a non-power-of-two
      `rom_size` (e.g. 4160): power-of-two sizes mask a wrong reduction order.
- [ ] VM init buffer is `448` bytes split `256 | 64 | 64 | 64`; digests are **seeded** then kept live.
- [ ] Instruction decode: big-endian `rs` u16, 5-bit `r1/r2/r3`; `lit1/lit2` little-endian u64.
- [ ] `MulH` = **high** 64 bits, unsigned. `Mod` returns the **quotient** `src1/src2`.
- [ ] Zero-divisor `Div`/`Mod` → **Special1** value; `Neg` = `~src1`; rotates use `r1`, not `src2`.
- [ ] Memory: digest the full 64-byte line, increment `memory_counter` **then** pick chunk `mc mod 8`.
- [ ] Special1/Special2 are clones of the live contexts (finalize the clone, keep the original).
- [ ] PostInstructions: clone digests + `LE64(sum_regs)`; `mixing` adds `LE32(loop_counter)`;
      `hprime` 8192 bytes; 32 rounds × 32 registers XOR; then `PROG_SEED = prog_value`.
- [ ] Finalizer order: `prog || mem || LE32(memory_counter) || LE64(reg0..reg31)`.
