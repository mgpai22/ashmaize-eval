# AshMaize Codex attempt constraints

You are in /workspace with only spec/TASK.md, spec/ABI.md, spec/ASHMAIZE.md, and these instructions.

There is no oracle during implementation. Do not inspect /usr/local/bin/oracle. Do not search for hidden scenarios, grader files, upstream AshMaize source, or any answer key.

No internet or dependency fetching is allowed from spawned commands. Use installed toolchains and standard libraries only.

Implement the JSON-over-stdio ABI from spec/ABI.md using the algorithm in spec/ASHMAIZE.md. Leave
final runnable output at /workspace/agent.sh. Any source, compiled binary, or runtime data that
agent.sh needs must live under /workspace.

agent.sh must be relocatable after the workspace is frozen. Do not hardcode /workspace paths inside
the wrapper; resolve helper files relative to agent.sh's own directory, for example with
`DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)`.

agent.sh must read one JSON object on stdin, write one JSON object on stdout, exit 0 on success, and
exit non-zero with {"error":"<reason>"} for invalid input.
