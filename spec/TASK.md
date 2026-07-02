# Task: implement the AshMaize proof-of-work VM

You are implementing **AshMaize**, a memory-hard proof-of-work virtual machine, from the
specification in [`ASHMAIZE.md`](./ASHMAIZE.md). Your program must expose the interface defined in
[`ABI.md`](./ABI.md): read one JSON request on stdin, write one JSON object on stdout.

## Rules

- **Offline.** There is no internet. Do not attempt network access. AshMaize's reference
  implementation exists online, but you cannot reach it, and the upstream source is **intentionally
  absent from this environment** — implementing the spec correctly is the task, not retrieving it.
- **No oracle during implementation.** The reference oracle is used only by the grader after your
  candidate is frozen. Do not try to call, inspect, decompile, search for, or otherwise use an
  oracle, hidden scenarios, grader files, upstream AshMaize source, or any answer key while
  implementing.
- **Determinism is mandatory.** For identical inputs your implementation must produce byte-identical
  outputs across runs and machines. No wall-clock, RNG, thread scheduling, or map-iteration order
  may leak into results.
- **Language is your choice**, as long as it builds and runs in the container and speaks the ABI
  (JSON over stdin/stdout). The container has Python 3, Go, Rust, and a C toolchain.

## How you are graded

A fixed corpus of inputs is run through both your implementation and the reference oracle. Your
score is a weighted average:

| Section | Weight | What it checks |
|---|---|---|
| `replay` | 65% | Full `hash` outputs (`hash_hex`, `reg_digest_hex`, `rom_digest_hex`) match the reference exactly. |
| `procedural` | 25% | Isolated correctness: ROM generation (`rom_digest`) and single-op semantics (`unit`). |
| `adversarial` | 10% | You reject malformed/invalid input (`{"error":…}` + non-zero exit) instead of producing garbage. |

`procedural` and `adversarial` localize *where* you diverge before `replay` will pass. The score is
won on the divergence hot-spots listed at the end of `ASHMAIZE.md`: byte-exact Argon2 `hprime`, ROM
generation and the `rom.at` byte-offset quirk, instruction decode, `MulH`/`ISqrt`/rotate semantics,
the `Mod`-is-division and zero-divisor-Special1 quirks, memory-counter chunk selection, and the
finalizer order. There is no local oracle available during implementation, so use the spec text and
published reference vector in `ASHMAIZE.md` for self-checks.

## Definition of done

`grader` reports your `overall` at (or very near) 100%, meaning your VM is byte-identical to the
reference across the whole corpus, including adversarial rejection. There is always more to harden;
do not stop at the first input that passes.

Your submitted `/workspace/agent.sh` must be relocatable after the workspace is frozen. Do not
hardcode `/workspace/...` paths inside the wrapper; resolve helper files relative to the wrapper's
own directory.
