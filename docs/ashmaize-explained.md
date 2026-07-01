# AshMaize Algo Intuition

--

## Preface

We're going to build **AshMaize** in our heads. It's a proof-of-work algorithm out of the Cardano ecosystem, and it's a deliberate cousin of Monero's **RandomX** with the same big idea, but stripped down and re-tuned so it runs *almost as fast in a web browser (WebAssembly) as it does natively*. That last part is the whole personality of the thing, and it'll explain a bunch of design choices later, so tuck it away.

I'm going to teach it the way I like to learn: **one idea at a time**, each one earning the next.

**Table of contents**
- [The one-sentence version](#the-one-sentence-version)
- [Why not just SHA-256?](#why-not-just-sha-256)
- [The two walls](#the-two-walls)
- [Our toolbox: two primitives](#our-toolbox-two-primitives)
- [The hash chain (the clever bit)](#the-hash-chain-the-clever-bit)
- [Piece 1: the ROM](#piece-1-the-rom)
- [Piece 2: the VM](#piece-2-the-vm)
- [Booting the VM](#booting-the-vm)
- [What an instruction looks like](#what-an-instruction-looks-like)
- [Tracing one instruction](#tracing-one-instruction)
- [Piece 3: the loop](#piece-3-the-loop)
- [The finish line](#the-finish-line)
- [The whole thing, in one breath](#the-whole-thing-in-one-breath)
- [Final](#final)

---

## The one-sentence version

Here's AshMaize with zero ceremony:

> You feed it some bytes, it grinds for a bit, and out comes a fixed **64-byte fingerprint**.

Same bytes in -> same 64 bytes out, every single time. Flip one bit of the input -> a completely different, unrelated-looking 64 bytes. That's a hash function. You already know a few SHA-256, MD5, Blake2.

The actual call, so it's concrete:

```rust
let digest = hash(salt, &rom, nb_loops, nb_instrs);
// digest: [u8; 64]
```

Ignore `rom`, `nb_loops`, `nb_instrs` for now. The shape is **bytes in, 64-byte fingerprint out.**

Quick note on the *two* inputs, because people trip on this:

| Input | Job |
|---|---|
| **`key`** (builds the `rom`) | picks *which giant memory* everyone works against |
| **`salt`** | the actual thing you're hashing (in mining, your nonce) |

Think of the **key** as "which puzzle room we're all standing in" and the **salt** as "my individual guess inside that room." You build the room once (expensive), then throw millions of guesses at it (cheap).

---

## Why not just SHA-256?

Fair question. SHA-256 exists, it's battle-tested, it's fast. Why invent anything?

One word: **ASICs (Application Specfic Integrated Circuit).**

Here's the thing nobody tells you up front: **an ASIC only wins when the task is fixed and known in advance.** SHA-256 is the *exact same recipe* every time with the small, fixed sequence of adds and bit-shuffles. Because it never changes, a company can etch that one recipe directly into silicon. No flexibility, no waste, just wires. The result runs roughly a *thousand times* cheaper than your CPU.

And that's how mining centralizes. Whoever can afford warehouses of custom chips wins!

So the goal of AshMaize is to be a hash that's **deliberately miserable to bake into a chip**.

How do you make a chip miserable? You make the workload look like the *one thing a general-purpose CPU is uniquely good at*: **running an unpredictable program over a big pile of memory.** Let's turn that instinct into two concrete design constraints.

---

## The two walls

AshMaize (like RandomX before it) builds two separate walls. Both have to fall for an attacker to win, and neither falls cheaply.

### Wall 1 — the program is random

The work you have to do is *run a program*, however, a **different, unknown-until-you-generate-it program** every time. Adds, multiplies, divides, square roots, XORs, even a full hash-in-a-hash, in any order.

To run an *arbitrary* instruction stream, your chip has to be able to do *all* the instructions and figure out on the fly which one is next. Congratulations — you've just described a general-purpose CPU. If you try to build an ASIC for AshMaize, you end up rebuilding... a CPU. And Intel, AMD, and ARM have a multi-decade, multi-billion-dollar head start on being good at that. There's nothing to specialize *toward*, because the task is literally "be good at everything."

### Wall 2 — the memory is huge

The program is also forced to **randomly read from a giant buffer**, potentially gigabytes.

Logic can be made tiny and cheap. **Memory can't.** Physics doesn't care whether you're a scrappy chip startup or a gamer with a GPU: holding gigabytes and fetching random bytes fast costs everyone the same. So the fancy custom logic doesn't help, the chip spends most of its life *waiting on RAM*, and RAM latency is the great equalizer.

(And no, you can't dodge it by *not* storing the memory and regenerating bytes on demand, recomputing is *slower*)

Put the walls together and the "best hardware for AshMaize" turns out to be... a regular computer.

Now let's build the pieces that raise those walls.

---

## Two primitives

The entire algorithm is built out of exactly two cryptographic tools. That's it. Learn these two and everything downstream is just plumbing.

1. **Blake2b** is a fast cryptographic hash. AshMaize uses the 512-bit flavor, so it eats any bytes and spits out **64 random-looking bytes**. Sometimes we use it one-shot; sometimes we keep a running "context" that we keep feeding.

2. **Argon2's H′** (pronounce it "H-prime") is the star. It's a **variable-length, strictly sequential** byte generator. Give it a seed and a size, and it hands you *exactly that many* random-looking bytes. Deterministically.

That second one is doing the heavy lifting, so let's actually understand *how* it produces its bytes, its one weird property is the load-bearing beam of the whole design.

---

## The hash chain (the clever bit)

Here's the puzzle H′ solves: your seed is tiny (a handful of bytes), but you need to fill **megabytes — or gigabytes** — with reproducible randomness. How?

You **chain** hashes:

```
block 1 = hash(seed)
block 2 = hash(block 1)
block 3 = hash(block 2)
block 4 = hash(block 3)
...keep going until you've made enough bytes...

output = block 1 ‖ block 2 ‖ block 3 ‖ ...   (all glued together)
```

Each link feeds the previous result back into the hash. Glue them all together and you've stretched a tiny seed into as many bytes as you want. You get two properties for free:

- **Random-looking** — every block is a hash output, so it's noise.
- **Reproducible** — same seed -> same chain -> same exact bytes, on any machine, forever.

Now here's the part that matters. Look closely:

> To compute block 1000, you must *already have computed* blocks 1 through 999.

Each link literally depends on the one before it. You **cannot jump ahead**. This is called being **strictly sequential**, and it's the thing that slams Wall 2 shut:

Remember the "just regenerate the memory on demand instead of storing it" cheat? To regenerate the byte at position nine-million, you'd have to re-run the *entire chain* from the start up to that point, which is *slower* than just having stored it. And at any moment you're only holding one block's worth of state, which isn't enough to leap forward. So you're forced to actually keep the whole thing in RAM.

**Sequentiality is what makes "store the gigabytes" the only sane option.** That's the entire trick. Everywhere you see AshMaize call H′ from here on its simply filling memory, seeding the CPU, generating programs, mixing state. Translate it as *"deterministically conjure this many random bytes from this seed, and you can't shortcut it."*

---

## Piece 1: the ROM

The ROM is where Wall 2 lives. "ROM" = **Read-Only Memory**. We build it once, and from then on nobody writes to it — the program only ever *reads*.

The concept is dead simple:

> The ROM is a big block of **random-looking bytes**, produced deterministically from the `key`.

Dump a freshly built 10 MB ROM to your screen and it looks like pure static however, it's *reproducible* static. Anyone with the same key gets byte-for-byte the same block. You pick the size when you build it (could be a few MB for testing, a couple GB for real ASIC pain).

So how do we fill it? We already know: **the hash chain.** Derive a seed from the key, then let H′ grind out a full ROM's worth of sequential randomness. Job done. This is the honest, strongest way to do it, and it's what you'd use in production. You build the ROM once, reuse it for millions of hashes, and amortize the cost.

### "But building 2 GB sequentially is slow!"

Yep. Sequential means you *can't parallelize* — block 2 waits on block 1, so only one CPU core works while the other eleven twiddle their thumbs. For a big ROM that can take real time.

So AshMaize offers a second, faster construction.

1. Slow-build a **small** buffer (the "pre-ROM") with the sequential chain. Small = fast.
2. Blow it up to full size **cheaply**: for each 64-byte chunk of the final ROM, copy one chunk from the pre-ROM and then **XOR in a few more** pre-ROM chunks at pseudo-random offsets.

Why is step 2 fast? Every output chunk is independent, all your cores can go at once. Picture it:

```
   small pre-ROM (a handful of real chunks)
   P0 P1 P2 P3 ... P15
       │      │   │
       ▼      ▼   ▼
   big[2]  =  P2 ^ P8 ^ P15 ^ P1     <- one output chunk = a few pre-ROM chunks XOR'd
```

Each of the (many) big chunks is just *some XOR combination* of the (few) small chunks. Different offsets per chunk -> tons of distinct-looking output from a small source.

There's an honest trade here, and it's worth naming: this fast mode leans on a smaller amount of "real" memory, so it's less punishing to specialized hardware than the full sequential build. That's exactly why it's the testing/benchmarking path, and the pure chain is the recommended one for anything serious. Design is choices, and this one's made in the open.

### The ROM's ID card

One last touch. After the ROM is built, AshMaize hashes the **entire thing** down to a single 64-byte fingerprint:

```
ROM-digest = hash(all the ROM bytes)
```

Why bother? Because this 64-byte digest is the **handoff** to the next stage. The CPU we're about to build gets seeded from `ROM-digest` (plus your salt) — *not* from the raw gigabytes. Two wins:

- **It binds everything to this exact ROM.** Change one byte of the ROM and the digest is totally different, so the whole computation lands somewhere else. No sneaking in a wrong or partial ROM.
- **It's tiny.** The CPU only needs 64 bytes to "know which ROM it's working with," instead of lugging around 2 GB.

So Piece 1 ends with: *"here are my gigabytes of static, and here's my 64-byte ID card."* Onward.

---

## Piece 2: the VM

Wall 1 lives here. Time to build a tiny pretend CPU — a **Virtual Machine**.

Real CPUs do all their actual work in a handful of ultra-fast little storage slots called **registers** — labeled boxes that each hold one number. "Add these two" really means "grab the number in box A, add the number in box B, drop the result in box C." AshMaize simulates exactly this, with:

> **32 registers, each holding a 64-bit number.**

In code that's literally `regs: [u64; 32]` — 32 boxes. Every instruction we run is some flavor of "do math on a couple of boxes, store the result in a box." Run a few hundred of those and the registers become a churning mess that depends on every step you took.

The registers are the **workspace**. The VM also carries a bit of **bookkeeping** alongside them — don't sweat the details yet, just meet them:

| Field | What it is | Job |
|---|---|---|
| **program** | a buffer of bytes | the current random program |
| **PC** | program counter | which instruction we're on |
| **PROG_DIGEST** | a *running* hash | records every instruction executed |
| **MEM_DIGEST** | a *running* hash | records every memory chunk read |
| **PROG_SEED** | 64 bytes | seeds the *next* program |
| **MC** | a counter | how many memory reads happened |
| **LC** | a counter | how many loops happened |

The two **running digests** are the sneaky-important part. They're not finished hashes — they're Blake2b contexts you keep *feeding* as the program runs. One eats every instruction; the other eats every memory read. By the end they've absorbed the *entire history* of the computation. That's the mechanism that makes shortcuts impossible: the final answer is built from these, so you had to actually *do* every step to arrive at the right ones. Hold that thought; it pays off at the finish line.

---

## Booting the VM

When you call `hash(salt, ...)`, where do the VM's *starting* values come from? This is where the ROM's ID card and your salt finally meet.

We glue them together and — you guessed it — run them through the hash chain:

```
seed        = ROM-digest ‖ salt
init_bytes  = H′(seed, enough bytes for all the starting state)
```

Then we **slice** `init_bytes` up and hand out the pieces:

```
   init_bytes (random, derived from ROM-digest + salt)
   ┌───────────────────────────┬────────┬────────┬────────┐
   │ 32 registers (256 bytes)  │ 64 by. │ 64 by. │ 64 by. │
   └───────────────────────────┴────────┴────────┴────────┘
             │                     │        │        │
             ▼                     ▼        ▼        ▼
      32 starting regs      seed for   seed for   PROG_SEED
                            PROG_      MEM_       (first program's
                            DIGEST     DIGEST      seed)
```

- The **32 registers** start as random numbers carved from the first slice.
- The **two running digests** each get their own 64-byte slice as a starting seed — so even the history-recorders begin in an input-dependent state.
- **PROG_SEED** gets the last slice, ready to make the first program.
- **PC, MC, LC** all start at **0**.

Here's the payoff of doing it this way: **everything in the VM is derived from `(ROM-digest + salt)`.** So change your salt by a single bit and the *whole* seed changes -> every register, both digests, and the program seed all start somewhere different -> the two runs diverge instantly and the final hashes look completely unrelated. The salt isn't sprinkled on at the end like garnish; it's baked into the foundation. That's why a good hash "avalanches."

---

## What an instruction looks like

Now the fun part. Almost every instruction has this shape:

```
destination = source1  OP  source2
```

Take two numbers, mash them together with some operation, store the result. In plain English: *"take reg[5], add reg[12], store it in reg[3]."* Then the PC ticks forward.

Two things make this interesting instead of boring: **which operation**, and **where the sources come from.**

### The operations

There are **13 of them**, and you don't need to memorize the list — just feel the *spread*:

| Category | Operations |
|---|---|
| Arithmetic | Add, Multiply, MultiplyHigh, Divide, Modulo |
| Bitwise | Xor, And, Negate |
| Bit-shuffling | RotateLeft, RotateRight, BitReverse |
| Math | integer Square-root |
| **Cryptographic** | **Hash** (run Blake2b on the two inputs!) |

That last one is the assassin. One of the instructions is *itself a full hash*. A chip trying to be efficient at AshMaize has to embed an entire Blake2b engine *inside its instruction executor*, on the off-chance the random program calls for it. Delightful.

And the mix isn't uniform — cheaper ops show up more often, but *every* op keeps a meaningful minimum probability. Why? So a random program almost always contains **at least one of every type**. An attacker can't pray for an "easy" program made of only cheap ops; every program is a full-body workout that hits the adder, the multiplier, the divider, the square-root unit, *and* the hash engine.

### The five sources

Here's the layer that makes each instruction genuinely unpredictable. When an instruction wants an input, that input can come from **five** places:

| Source | Where the number comes from |
|---|---|
| **Register** | one of the 32 boxes (the normal case) |
| **Literal** | a fixed number baked into the instruction's own bytes |
| **Memory** | **read a value out of the big ROM** |
| **Special1** | live bytes pulled from `PROG_DIGEST` |
| **Special2** | live bytes pulled from `MEM_DIGEST` |

Three of these deserve a second look:

**Memory** is *the reason the VM ever touches the ROM.* When a source is Memory, the instruction carries an address, the VM grabs a 64-byte cacheline from the ROM, and uses part of it as the operand. Sprinkle these through a random program and suddenly your CPU is doing constant random reads across gigabytes — *that's* Wall 2 coming alive. (Every such read also gets fed into MEM_DIGEST, so history remembers what you touched.)

**Special1 / Special2** are the feedback loop, and they're beautiful. They reach into the *live, still-changing* running digests and pull out fresh bytes to use as an operand. Since those digests mutate with every instruction, an operand that reads from one has a value that depends on *the entire history of the run so far.* The machine's past feeds back into its present. You cannot compute instruction #200 without having faithfully done #1 through #199.

**Literal** is the simple one: the number's just sitting in the instruction's bytes.

---

## Tracing one instruction

Let's run one, start to finish, so it stops being abstract. Say the PC points at an instruction that decodes to:

- **OP** = Add
- **source1** = Register `reg[5]`
- **source2** = Memory (read the ROM)
- **destination** = `reg[3]`

Here's the VM doing its thing:

```
1. FETCH    grab the instruction bytes at the PC
2. DECODE   -> Add, src1 = reg[5], src2 = Memory, dst = reg[3]

3. RESOLVE src1:  it's a register -> read reg[5]         -> say 1000

4. RESOLVE src2:  it's Memory ->
              • pull a 64-byte cacheline from the ROM
              • feed the whole line into MEM_DIGEST        <- history!
              • bump the memory counter (MC)
              • take an 8-byte slice as the value          -> say 7777

5. COMPUTE  1000 + 7777 = 8777      (all math wraps at 64 bits, like a real CPU)

6. STORE    reg[3] = 8777

7. RECORD   feed the instruction's bytes into PROG_DIGEST <- history!

8. ADVANCE  PC = PC + 1
```

The two lines to burn into memory are **4** and **7**. The *history-recording side effects* that happen on the relevant instructions. Every memory read pours its cacheline into MEM_DIGEST; every executed instruction pours its bytes into PROG_DIGEST. As the program runs, those two digests silently witness *everything*. The answer is built from what they witnessed.

---

## Piece 3: the loop

AshMaize runs several programs, back to back. One `hash()` call is a handful of **loops** (that's `nb_loops`), each running a few hundred **instructions** (that's `nb_instrs`).

One loop looks like this:

```
REPEAT nb_loops times:
   1. GENERATE   make a fresh random program
   2. EXECUTE    run all its instructions (the trace above, ×N)
   3. MIX        scramble the VM state, and mint the seed for the NEXT program
```

**Generate**: fill the program buffer with H′ output, seeded by the current `PROG_SEED`. Those random bytes *are* the program (remember: instructions are just bytes we decode).

**Execute**: run every instruction. Registers churn; the two digests fill up.

**Mix**: after the batch, collapse the registers, snapshot the digests, fold that back into every register a bunch of times, and critically **derive a new `PROG_SEED` from the digests.** Then bump the loop counter.

Then back to Generate, with the new seed -> a brand-new program. Repeat.

Now here's why this structure is Wall 1 in its final form. Ask yourself: **can an attacker know what the next program will be?**

No. The next program comes from `PROG_SEED`, and `PROG_SEED` is derived in the Mix step from the running digests, which only reach their final values *after every instruction of the current loop has actually run.* So the programs form an unpredictable chain:

```
PROG_SEED₀ -> program₀ -> (run it) -> digests -> PROG_SEED₁
                                                  │
PROG_SEED₁ -> program₁ -> (run it) -> digests -> PROG_SEED₂
                                                  │
                                                 ... and so on
```

You can't peek ahead. You can't pre-build a circuit for a program you haven't earned yet. You can't cherry-pick easy instructions. **Each program is only revealed by doing the full work of the previous one.** An attacker is forced to run a general CPU through several unpredictable, varied programs, strictly in order. Exactly the thing custom silicon is worst at.

---

## The finish line

AshMaize does one final hash over everything the machine accumulated:

```
answer = hash( PROG_DIGEST ‖ MEM_DIGEST ‖ MC ‖ reg[0..32] )
```

It commits to the four things that represent "all the work I did":

- **PROG_DIGEST** — every instruction ever executed, across all loops.
- **MEM_DIGEST** — every memory chunk ever read.
- **MC** — *how many* memory reads happened.
- **reg[0..32]** — the final state of all 32 boxes.

Hash it all with Blake2b -> **64 bytes.** That's your answer.

The output is welded to the *entire computation*. Skip one memory read, fumble one instruction, and the digests and registers come out different, so the final 64 bytes come out different. **There is no path to the correct answer that doesn't go through all the work.**

---

## The whole thing, in one breath

You now know every piece. Here it is assembled:

```
       key ─────────────► build a big random ROM (sequential hash chain)
                                     │
                                     ▼
                            ROM-digest (64-byte ID card)
                                     │
       salt ─────────────►  seed & boot the VM
                            (32 registers, 2 running digests, PROG_SEED)
                                     │
                          ┌──────────┴──────────┐
                          ▼   repeat nb_loops    │
                   1. generate a fresh program   │
                   2. run its instructions       │  (each: crunch numbers,
                      (touch the ROM, feed the    │   randomly read the ROM,
                       running digests)           │   feed the digests)
                   3. mix state, mint next seed ──┘
                                     │
                                     ▼
                    finalize: hash the digests + MC + registers
                                     │
                                     ▼
                             64-byte digest
```

And in one sentence: **a key builds a big memory that forces everyone to pay for RAM (Wall 2); a salt boots a tiny CPU that runs a chain of unpredictable random programs against that memory (Wall 1); the whole journey is hashed into the answer.**