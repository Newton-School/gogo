# AgentFlow Review Workflow

Run commands from the client repository root. The CLI prints one JSON result and uses meaningful
exit codes. Automation should branch on `error.code`, not message text.

## Architecture State

```bash
python3 .agentflow/scripts/agentflow.py status
python3 .agentflow/scripts/agentflow.py snapshot --state current
python3 .agentflow/scripts/agentflow.py snapshot --state current --page <page-id>
```

AgentFlow keeps one canonical architecture under `.agentflow/architecture/`. Its manifest has the
current `architectureHash`; state records `implementedArchitectureHash`.

- Equal hashes mean the current architecture is verified against code.
- Different hashes mean approved architecture work is pending implementation.
- `snapshot --state implemented` is available only when the hashes match. AgentFlow does not retain
  full historical page snapshots locally; use the proposal record and Git history for past context.

## Create And Submit A Proposal

Write proposal JSON to a temporary file:

```bash
python3 .agentflow/scripts/agentflow.py proposal create --input /tmp/proposal.json
python3 .agentflow/scripts/agentflow.py validate --proposal <proposal-id>
python3 .agentflow/scripts/agentflow.py proposal submit <proposal-id>
make agentflow PROPOSAL=<proposal-id>
```

`create` pins the proposal to the current architecture hash. `submit` captures an immutable numbered
proposal version. Use `proposal revise <proposal-id> --input ...` for an active proposal and submit
again after validation.

`make agentflow` regenerates `.agentflow/index.html`, starts or reuses the project-scoped comment
writer, and does not open a browser. Ask the user to refresh an existing tab or open the file
manually.

## Review Page

The generated local page provides:

- Architecture with aligned, collapsible client-owned navigation and full-content search;
- Review with proposal summaries plus focused Changes, Proposed, and Before modes;
- Comments with searchable Open and Resolved feedback across proposals;
- table pages for structured definitions;
- pan, wheel zoom, touch pinch zoom, and fit controls for flow pages;
- rich, non-interactive documents for exceptional reference material;
- selectable table and flow elements with technical details and anchored comments.

The URL fragment stores the view, page, proposal, comparison mode, and selected technical target so
refreshing restores context. The project name is used as the HTML title.

The page is static. Its copied Python standard-library writer accepts only authenticated loopback
requests and writes only to this project's `.agentflow/comment-inbox/`. There is no folder picker,
remote service, or application server. The CLI imports inbox requests into canonical proposal
review history.

## Comments

Import and list every Open comment on one proposal:

```bash
python3 .agentflow/scripts/agentflow.py comment list <proposal-id> --status open
```

List comments across all proposals:

```bash
python3 .agentflow/scripts/agentflow.py comment list
python3 .agentflow/scripts/agentflow.py comment list --status resolved
```

Record feedback received in chat:

```bash
python3 .agentflow/scripts/agentflow.py comment add <proposal-id> \
  --page-id <page-id> \
  --element-type node \
  --element-id <node-id> \
  --message "<requested change>"
```

Targets are either `--navigation-id`, a complete `--page-id`, or a page plus one element type:
`column`, `row`, `cell`, `node`, `edge`, `group`, `section`, or `block`.

Only Open comments can be edited or deleted:

```bash
python3 .agentflow/scripts/agentflow.py comment edit \
  <proposal-id> <comment-id> --message "<corrected feedback>"

python3 .agentflow/scripts/agentflow.py comment delete <proposal-id> <comment-id>
```

After a validated proposal visibly addresses a comment:

```bash
python3 .agentflow/scripts/agentflow.py comment resolve \
  <proposal-id> <comment-id> --resolution "<specific architecture change>"
```

Resolved comments are immutable history. Never manipulate inbox or review JSON manually.

## Approval

Approval requires the user's explicit decision and a currently submitted proposal with no Open
comments:

```bash
python3 .agentflow/scripts/agentflow.py proposal approve \
  <proposal-id> --approved-by user
```

Approval writes only changed pages and navigation into `.agentflow/architecture/`, then records the
new `architectureHash` on the immutable approved proposal review. It does not create a full
architecture revision tree. Use `--implemented-baseline` only while documenting inspected code that
already exists, normally during initial adoption. Never use it for new work.

Reject only after an explicit decision:

```bash
python3 .agentflow/scripts/agentflow.py proposal reject \
  <proposal-id> --reason "<reason>"
```

Proposal states:

- `draft`: editable, not submitted;
- `under_review`: submitted and waiting for review;
- `changes_requested`: revised or has Open feedback;
- `approved`: immutable and applied to the current architecture;
- `rejected`: immutable and changes no architecture.

A stale proposal cannot be approved over a newer current architecture. Another proposal also cannot
be approved while `implementationPending` is true. Finish the current approved plan first so a
later plan cannot accidentally omit earlier unimplemented architecture.

The reviewer keeps a stale proposal visible as **Needs rebase**. It does not reconstruct a diff
from an obsolete architecture snapshot. Revise the proposal against the current architecture, then
submit it again for a new reviewable diff.

## Plan And Implementation

After approval, create Markdown under `.agentflow/plans/` plus a traceability manifest described by
the repo-local planning skill. The manifest includes the current `architectureHash` and the approved
`proposalId`:

```bash
python3 .agentflow/scripts/agentflow.py plan validate --input /tmp/plan.json
python3 .agentflow/scripts/agentflow.py plan register --input /tmp/plan.json
```

The CLI calculates every changed navigation/page-element reference from the approved proposal
record and rejects missing, unknown, or duplicate coverage.

During implementation, create evidence with the same architecture hash:

```bash
python3 .agentflow/scripts/agentflow.py implementation audit --input /tmp/evidence.json
python3 .agentflow/scripts/agentflow.py implementation complete --input /tmp/evidence.json
```

`audit` is read-only. `complete` rejects missing evidence or blocked tasks, stores immutable
evidence, and updates `implementedArchitectureHash` only when it matches the current architecture.

## Migration And Correction

After an explicitly requested harness update, inspect migration before applying it:

```bash
python3 .agentflow/scripts/agentflow.py migrate --check
python3 .agentflow/scripts/agentflow.py migrate --apply
python3 .agentflow/scripts/agentflow.py validate
python3 .agentflow/scripts/agentflow.py status
make agentflow
```

The generic profile preserves client-controlled navigation. The Django profile places each resolved
artifact under its owning Django app and a non-empty folder such as `Models`, `Mixins`, `Enums`,
`APIs`, `Tasks`, `Admin`, or `Integrations`; `Platform` is reserved for cross-app concerns. A Django
migration never creates a top-level `Data`, feature, or documentation folder when an artifact owner
is resolved.

When migrating lifecycle schema 2, AgentFlow converts proposal, plan, evidence, and pending comment
records to architecture hashes, retains only the latest approved pages as current architecture, and
removes the old full revision tree. It refuses stale active proposals with
`MIGRATION_ACTIVE_PROPOSAL_STALE` rather than silently rebasing them. The conversion is
transactional: an interrupted migration either completes or restores the legacy state on the next
lifecycle command.

For a preserved legacy-v3 archive, use the correction path only when explicitly requested:

```bash
python3 .agentflow/scripts/agentflow.py migrate --rebuild-from-v3 --check
python3 .agentflow/scripts/agentflow.py migrate --rebuild-from-v3 --apply
```

The rebuild reconstructs the one current architecture from preserved v3 state. It refuses with
`MIGRATION_REBUILD_UNSAFE` if current proposals, plans, implementation evidence, comments, or
pending implementation could be superseded. Do not copy, edit, or hand-write architecture JSON to
work around that error.
