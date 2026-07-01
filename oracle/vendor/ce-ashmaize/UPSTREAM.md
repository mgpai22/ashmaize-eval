# Vendored AshMaize reference (eval oracle)

This directory is a **vendored fork** of the AshMaize reference implementation, used only to
build the eval's hidden reference oracle (`oracle/`). The candidate never sees this source — only
the oracle binary's outputs.

- **Upstream:** https://github.com/input-output-hk/ce-ashmaize
- **Pinned commit:** `58d6a1fe3df2582e14d53b67292ce8a36d90e7e6` (2025-09-16)
- **License:** `MIT OR Apache-2.0` (see `LICENSE-APACHE`, `LICENSE-MIT`). Per Apache-2.0 §4, the
  modifications below are stated explicitly.

## What is vendored

Only the crate files the oracle needs to compute AshMaize outputs:

- `src/lib.rs`, `src/rom.rs` — the VM, ROM generation, instruction set, and `hash` entry point.
- `Cargo.toml` — trimmed to the `ashmaize` library + its single dependency (`cryptoxide ~0.5.1`);
  the upstream `[workspace]`, web crates, benches, examples, and dev-dependencies are **not**
  vendored (the oracle does not need them).
- `SPECS.md`, `README.md` — upstream docs, kept for provenance.

## Modifications (Apache-2.0 §4 "state changes")

All changes are **additive, oracle-only public surface** and do **not** alter any computed value.
They exist solely so the eval ABI can expose ROM digests, register digests, and single-op results
without duplicating AshMaize internals in the grader. Each addition is marked with an
`// ASHMAIZE-EVAL:` comment in the source.

1. `src/rom.rs` — added `pub fn Rom::digest_bytes(&self) -> [u8; 64]`, returning the existing
   (private) `RomDigest` bytes.
2. `src/lib.rs` — extracted the existing `Op3` / `Op2` result computation into pure helpers
   `op3_apply` / `op2_apply`, called by both the unchanged `execute_one_instruction` and the new
   `unit_eval`. This is a pure refactor: `execute_one_instruction` now computes the `special1`
   fallback value eagerly (a side-effect-free `prog_digest.clone().finalize()`), which yields a
   byte-identical result to the upstream lazy form.
3. `src/lib.rs` — added `pub fn hash_with_state(...) -> HashState { hash, reg_digest, rom_digest }`.
   `reg_digest = Blake2b::<512>(LE64(reg0) || ... || LE64(reg31))` computed immediately before
   `VM::finalize()`. `hash` itself is unchanged.
4. `src/lib.rs` — added `pub enum UnitOp`, `pub struct UnitRequest`, and
   `pub fn unit_eval(req: UnitRequest) -> u64` for the eval's `unit` ABI, delegating to
   `op3_apply` / `op2_apply` so it cannot diverge from VM execution.

The reference vector (`hash(b"hello", TwoStep{16384,4} ROM of b"123" @ 10 MiB, 8, 256)`) is
reproduced byte-for-byte by the upstream `test_eq` test and by the oracle, confirming the refactor
preserves behavior.
