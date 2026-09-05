---
name: apply-agentflow-comments
description: Read and apply every open comment on an AgentFlow proposal, preserve review history, and resolve comments only after validated architecture visibly addresses them.
---

# Apply AgentFlow Comments

Use this skill when the user asks to fix, address, resolve, or review AgentFlow comments. Change the
architecture proposal, not implementation code.

## Select The Proposal

Read repository rules, relevant memories, and `.agentflow/AGENTFLOW.md`. Use the proposal, page,
flow, table, or comment named by the user. Otherwise inspect active proposals:

```bash
python3 .agentflow/scripts/agentflow.py proposal list
```

Do not combine feedback from unrelated proposals. Ask which proposal only when more than one active
proposal genuinely matches. Approved proposal records are immutable; feedback that changes current
architecture requires a new proposal.

## Import And Read All Open Comments

Always run this first:

```bash
python3 .agentflow/scripts/agentflow.py comment list <proposal-id> --status open
```

The command imports pending add, edit, and delete requests from the generated page before returning
the canonical list. Read every Open comment on the proposal, not only comments on the page the user
last viewed.

Read all comments when prior resolved feedback affects the elements being changed:

```bash
python3 .agentflow/scripts/agentflow.py comment list <proposal-id>
```

Resolved comments are history. Do not reopen, edit, delete, or repeat them.

When feedback arrives in chat, record it before revising. Anchor it to the narrowest stable target:

```bash
python3 .agentflow/scripts/agentflow.py comment add <proposal-id> \
  --page-id <page-id> \
  --element-type <column|row|cell|node|edge|group|section|block> \
  --element-id <element-id> \
  --message "<feedback>"
```

Omit the element arguments to target the whole page, or use `--navigation-id <id>` for hierarchy
feedback.

## Revise And Resolve

1. Map every Open comment to a visible page or navigation change.
2. Read the current candidate and any relevant resolved history before editing.
3. Leave conflicting, ambiguous, blocked, or intentionally deferred feedback Open and report why.
4. Revise the complete proposal and validate it:

```bash
python3 .agentflow/scripts/agentflow.py proposal revise <proposal-id> \
  --input /tmp/revised-proposal.json
python3 .agentflow/scripts/agentflow.py validate --proposal <proposal-id>
```

5. Resolve only comments whose requested result is visible in that validated candidate:

```bash
python3 .agentflow/scripts/agentflow.py comment resolve \
  <proposal-id> <comment-id> \
  --resolution "<specific architecture change that addressed the comment>"
```

6. Re-list Open comments. Submit only when none remain:

```bash
python3 .agentflow/scripts/agentflow.py comment list <proposal-id> --status open
python3 .agentflow/scripts/agentflow.py proposal submit <proposal-id>
make agentflow PROPOSAL=<proposal-id>
```

Do not open the generated page. Tell the user to refresh it.

## Status And Memory Rules

- `open` means architecture work or a user decision is still required.
- `resolved` means the validated proposal visibly includes the requested result.
- Only Open comments can be edited or deleted. Resolved comments remain immutable history.
- Never edit proposal review files or `.agentflow/comment-inbox/` by hand.
- Store a concise note in `.agents/memory/architecture-feedback.md` only when the feedback is a
  durable preference that should shape future proposals.

Report resolved comment IDs, remaining Open comments, and blockers. Stop before planning or
implementation until the revised proposal is explicitly approved.
