# Oracle

The grader needs a reference that speaks `spec/ABI.md` and returns the **correct** AshMaize output
for any request. The candidate never sees this code — only its outputs (`oracle` is a black box on
the container's `PATH`).

## What it is

A thin Rust binary (`oracle/src/main.rs`) that:
1. reads one JSON request on stdin (`hash` | `rom_digest` | `unit`),
2. validates it per `spec/ABI.md` (rejecting malformed/invalid input with `{"error":…}` + non-zero
   exit),
3. calls the AshMaize reference with the request's params, and writes the response JSON on stdout.

ROM generation is fixed to the reference configuration `TwoStep { pre_size = 16384,
mixing_numbers = 4 }`; only `rom_size`/`nb_loops`/`nb_instrs`/seeds vary per request.

It depends, by local path, on the **vendored** AshMaize crate:

```
oracle/
  Cargo.toml            # bin `oracle`, deps: ashmaize (path) + serde_json
  src/main.rs           # the ABI wrapper (original, MIT)
  vendor/ce-ashmaize/   # vendored fork of input-output-hk/ce-ashmaize @ 58d6a1f… (MIT OR Apache-2.0)
```

The vendored crate exposes three tiny **oracle-only** additions used by the ABI (see
`vendor/ce-ashmaize/UPSTREAM.md`): `Rom::digest_bytes`, `hash_with_state` (adds `reg_digest` +
`rom_digest`), and `unit_eval` (single-op semantics, delegating to the same op helpers the VM uses).
No AshMaize internals are duplicated in the grader or scenarios.

## Build

```bash
cd oracle && cargo build --release
ln -sf target/release/oracle oracle   # -> oracle/oracle (gitignored), used by the grader
```

Then point the grader at it: `-oracle-bin ./oracle/oracle`. In the container the binary is prebuilt
in a builder stage and copied to `/usr/local/bin/oracle` (see `container/Dockerfile`), so it runs
fully offline.

## Reference vector (must reproduce exactly)

```bash
echo '{"op":"hash","preimage_hex":"68656c6c6f","rom_seed_hex":"313233","rom_size":10485760,"nb_loops":8,"nb_instrs":256}' \
  | ./oracle/oracle
# hash_hex = 389401e43b60d3ad0962443d59ab7cab7cb7c8c41d2b85a8dad9ff47eab6619e
#            e79c38e63d36f8c7960f420095b955b1c0dced4dc36a8cdfaf5deedc399fb4f3
```

This is the upstream `test_eq` vector (`b"hello"` over a 10 MiB `TwoStep` ROM of `b"123"`); see
`spec/ASHMAIZE.md` §11.
