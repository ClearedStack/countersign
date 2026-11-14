# Countersign specification

> Technical specification for Countersign, the human-accountability queue worker powering tool determinations and anonymous edit approvals on /guides/#decide.

**Status: design.** This is a specification. It describes how Countersign is meant to work, not something that exists yet.

The same text is published at [clearedstack.com/platform/countersign/](https://clearedstack.com/platform/countersign/). To change the specification, change it here and say why.

## Overview & Role

Countersign (`v0.1.0-draft`) powers the human owner's decision and anonymous edit-approval inbox surfaced on `https://clearedstack.com/platform/how-we-decide/` and `https://clearedstack.com/platform/public-edits/`, replacing the original Stage 6 (Decide). Implemented in the Countersign repository under the MIT License, it records authoritative human determinations for evaluated candidates and gates visitor-submitted `✎` edit proposals before any code or configuration touches Plainforge.

Unlike upstream automated workers, Countersign never makes autonomous judgment calls: final accountability for building, linking, merging, parking, rejecting, overriding, or accepting public edits belongs strictly to the human project owner.

## Runtime & Resource Budget

Countersign runs in the `cs-workers` Kubernetes namespace under the **Standard (Workers)** priority class.

| Attribute | Value |
| --- | --- |
| **Worker ID / Repo** | Countersign (`countersign`) |
| **Namespace** | `cs-workers` |
| **Kind / Version** | `worker` · `v0.1.0-draft` |
| **RAM Budget** | `30 MiB` (always-on Go pod) |
| **Runtime AI Calls** | `0` (human-in-the-loop gate + deterministic state routing) |
| **Local Spool** | `/var/spool/cs` (buffers pending decision acknowledgments across Muster restarts) |
| **Build & Deploy** | Built with `ko` (no Docker) by Keelworks, signed with `cosign` in Lazarette, deployed by Shipkeeper via Server-Side Apply |

## Interfaces & State Transitions

Countersign interacts with Muster in the `clearedstack` namespace over ServiceAccount-authenticated HTTP (`CONTRACT_VERSION=v1`), using Muster's worker calls `lease`, `heartbeat`, `complete`, `fail` and `needs_subtool`, and the `release` call that only Countersign may make. The wire is Muster's [contract v1](https://github.com/ClearedStack/muster/blob/claude/repo-build-prompt-1csgx6/contract/v1/CONTRACT.md), and where this page and that contract disagree about the wire, the contract is right. Countersign runs a lease loop: it leases until Muster has nothing more, sends a heartbeat for every lease every 20 seconds, and ends each lease with `complete`, `fail` or `needs_subtool`. The result it gives with `complete` is the decision record. Its two queues are shown on `clearedstack.com` through the read-only `pub.*` views. Those views belong to Logbook and are fed by Muster from the records Countersign hands it; Countersign has no database access and creates no view itself.

Muster is Countersign's only dependency. Where an outcome below names another component (Waybill, Chartroom, Tidegauge, Bellbuoy), Countersign records the decision with Muster, and Muster routes the work. Countersign calls none of them.

1. **Tool Determinations Queue (`scored` and `screened_out` overrides):**
   - **Approve (Build) → `approved`:** Advances the candidate to Waybill for provenance stamping and clean-room spec compilation.
   - **Link → `linked`:** Records an existing free, private external tool in the catalog (Chartroom) without rebuilding.
   - **Merge → `merged`:** Folds scope into an existing or planned parent tool repository.
   - **Park → `parked`:** Defers a viable idea with a named blocker for scheduled Tidegauge revisit.
   - **Reject → `rejected`:** Permanently archives the candidate with its rubric score and rationale.
   - **Override → `observed`:** Returns a `screened_out` item to `observed` only when accompanied by an explicit written reason logged publicly.
   - **Choosing against the recommendation is an override too.** A `scored` item arrives with the outcome its score recommends. Any of the five outcomes above that differs from that recommendation needs a written reason, logged publicly and marked as an override. Merge is never the recommendation, so it always needs one.
   - **Each outcome carries what it describes.** Link names the existing tool, Merge names the parent tool, Park names the blocker, and Reject gives a written reason.
   - **No other move exists.** Approve, Link, Merge, Park and Reject apply only to a `scored` item, and Override only to a `screened_out` item.
2. **Anonymous Public Edit Proposals (`core.edit_proposal`, `pending_approval`):**
   - Receives `DATA`, `CONFIG`, and `ISSUE` proposals (≤ 4 KB, read as 4096 bytes of proposal text) submitted via any `✎` button after they clear Muster's 5-layer anti-spam shield. Countersign checks the size again on arrival.
   - On owner **Approve → `approved`**, the proposal is recorded on the `urgent` lane, and Muster dispatches Bellbuoy (`Proposed` → `Approved` → `Building` → `Live`; `Proposed` is the reader's label for the state `pending_approval`).
   - On owner **Decline → `declined`**, the proposal is logged without altering Plainforge or `core.*` data. A reason may be given and is not required.
3. **Stopped items (release):**
   - An item that any worker stopped with `E_PARSE` is held by Muster in its state and offered to nobody.
   - On owner **Release**, Countersign calls `POST /v1/release` with the item's identifier. The item keeps the state it was stopped in, and its step goes back to the worker it was for, on the `urgent` lane. Muster refuses the release of an item that is not stopped.
   - The call carries the identifier and nothing else. A reason the owner gives is kept in Countersign's own log and is not sent.
   - Muster's contract has no call that lists stopped items. Countersign can list the ones it stopped itself. The owner learns of the others from the public views.

## Sub-Tools & Failure Modes

Countersign intentionally has **zero sub-tools** (`subTools: []`), because final product determinations and edit approvals are human decisions rather than automated pattern rules.

### Sanitized Failure Modes

| Error Code | Trigger | Platform Behavior |
| --- | --- | --- |
| `E_PARSE` | Malformed determination payload; an outcome not allowed for the item's kind and state; missing written rationale on an override; a Link without its tool, a Merge without its parent, a Park without its blocker or a Reject without its reason; or invalid `ownership_map.json` target on an edit proposal. Also a leased item that is itself broken: not well-formed, missing a required field, or over its size limit. | A decision is refused by Countersign before it is recorded, or rejected cleanly by Muster (which holds `ownership_map.json`); the item remains in its current queue state with a fingerprinted error logged. A broken leased item is reported to Muster with `fail`, is not shown to the owner, and keeps its state. Muster holds it until the owner releases it. |
| `E_TIMEOUT` | Transient timeout reaching Muster during decision commit. Muster answering `429` or any `5xx`, and anything that is not an answer from Muster at all, counts as a timeout. | Buffered in `/var/spool/cs` and retried in order. A result leaves the spool as delivered only when Muster answers `200`. Muster treats a repeated call as the same call, so a result sent twice is applied once. |
| `E_NEEDS_SUBTOOL` | A leased step that is well-formed but that Countersign has no rule for: an unfamiliar kind of step, state, recommendation, kind of edit or priority lane, or a lease with a field Countersign does not know. | Countersign asks Muster to pause that one item with `needs_subtool` and does not show it to the owner. Every other item carries on. Muster's contract v1 has no move to `needs_subtool` from any state Countersign holds, so today Muster refuses the pause: the refusal is logged once, and the item is offered again when its lease runs out and is set aside again, without a new log entry. See the open questions. |

How Countersign sorts Muster's answer to a result it sends:

| Muster answers | What Countersign does |
| --- | --- |
| `200` | The result is delivered and leaves the spool. |
| `409` with the cause `lease_not_held` | The lease was lost, not the decision. The decision is kept, out of the way of what is behind it, and goes out again under the new lease when Muster offers the item again. If the item comes back changed, the decision no longer applies and is set aside. |
| Any other status from `400` to `422` | A refusal. The result is set aside in the log, marked refused, and is not sent again. The item stays in its state, and Muster offers it again when its lease runs out. This includes a credential that is not accepted (`401`, `403`). |
| `429`, any `5xx`, or no usable answer | Try again. The result stays at the front of the spool and everything behind it waits. |

A refused `lease` or `heartbeat` changes nothing: the inbox and the spool are kept. A heartbeat answered with `lease_not_held` takes that one item out of the inbox, and Muster offers it again.

Countersign has no `tools/<case>/` sub-tools, and a decision is never paused: the owner's decision is either recorded or refused. Countersign does pause an arriving item it has no rule for, as the table says, so that it never guesses how to present one.

A fingerprint is the first 16 hexadecimal digits of the SHA-256 of the error code, the rule that failed and the item identifier. It is the same on every machine and names nothing private.

## Security & Clean-Room Boundaries

- **5-layer anti-spam gate before Countersign:** Anonymous `✎` edit proposals must pass browser Proof-of-Work, a hidden honeypot field, daily-salted IP hash rate limiting (zero raw IPs stored), and strict 4 KB schema validation against `ownership_map.json` (with HTML/scripts stripped) before entering `pending_approval`.
- **Zero unreviewed cluster mutations:** No anonymous edit or external submission can touch Plainforge, `core.*` state, or running pods until explicitly approved in Countersign.
- **Public accountability & pod hardening:** All decisions, overrides, and edit outcomes are visible in read-only `pub.*` views without login. The pod runs non-root with dropped capabilities, a read-only root filesystem (except `/var/spool/cs`), zero database credentials, and default-deny `NetworkPolicy`.

## Related pages

- [How we decide what to build](https://clearedstack.com/platform/how-we-decide/)
- [Public edits and what stays private](https://clearedstack.com/platform/public-edits/)
- [An item’s life: fourteen states and three lanes](https://clearedstack.com/platform/item-lifecycle/)
- [The pipeline and its ten workers](https://clearedstack.com/platform/pipeline/)

## Changes to this specification

Each change below was made when Countersign was first built, with the reason.

| Change | Reason |
| --- | --- |
| `E_NEEDS_SUBTOOL` added; "never enters `needs_subtool`" removed. | The build rules say a case no rule covers is paused, never improvised. Countersign can be leased an item of a kind, state, recommendation, proposal type or lane it has no rule for, and showing it to the owner anyway would be a guess. |
| Choosing against the recommendation needs a written reason. | "How we decide what to build" says the owner can override the recommendation and that an override needs a written reason. The specification only covered reopening a screened-out item. |
| Link, Merge, Park and Reject are refused without the tool, parent, blocker or reason they describe. | The outcomes were described as carrying these, but only the override reason was listed as a failure. |
| A disallowed outcome is `E_PARSE`. | No code was given for a well-formed decision that asks for a move the table does not have. |
| A broken leased item is `E_PARSE`, reported with `fail`. | The failure table only covered broken decisions. |
| A declined proposal has the state `declined` (provisional). | The specification said declined proposals are logged and gave the state no name. |
| Countersign records decisions with Muster and calls no other component; the `pub.*` views are Logbook's. | The text read as if Countersign dispatched Bellbuoy and published views itself. The registry gives Muster as its only dependency, and the pod has no database credentials and a default-deny network. |
| 4 KB is 4096 bytes of proposal text. A fingerprint is defined. Any failure that is not a clean refusal is a timeout. Records carry a content-derived identifier. | Each was needed to build the behaviour and was not stated. |
| The overview's first link now points to "How we decide what to build". | The same address was given twice. |
| The state is spelled `screened_out`. | Muster's contract v1 spells it so, and the wire has to match. The lifecycle page still shows `screened-out`. |
| The wire is Muster's contract v1, and the lease loop is described. | The contract now exists. Countersign follows it and replays its saved examples in its own tests. |
| Stopped items and the Release decision were added. | Muster's contract gives Countersign the `release` call, so that the owner decides whether an item stopped with `E_PARSE` runs again. |
| How each answer from Muster is sorted is written down, with the lost lease as its own case. | The contract gives every refusal a cause. A lost lease is not a fault in the decision, so the decision is kept for the next lease. Every other status from 400 to 422 is a refusal, and 429 and 5xx are retried. |
| `declined` is no longer provisional. | Muster's contract names it. |

Still open, and not changed here because the right answer is not in this repository:

- The summary line refers to `/guides/#decide`. The guides page has no such anchor.
- The overview says Countersign replaces "the original Stage 6 (Decide)". The registry says it is stage 4 and was `cs-04-decide`.
- **Pausing.** The build rules say an unfamiliar case is paused as `needs_subtool`. Muster's contract has no such move from `scored`, `screened_out` or a proposal, and says "a scored item waits for a person, not for a rule". One of the two has to change. Until then Countersign asks, Muster refuses, and the item is set aside each time it is offered.
- **Where the score comes from.** Muster's contract says an evaluation has the form Logbook gives: `score`, `recommendation` (`build`, `link`, `park` or `reject`), `split_flag`, and `gates` as `{"gate name": {"passed": false, "reason": "..."}}`. Countersign reads `results.assay` in that form. Assay's own branch gives `total`, `Build`, `split_merge_review` and a list of gates today, and an item scored that way is stopped by Countersign with `E_PARSE` because its score is missing. Assay and the contract have to agree. The title is read from `found.title`, or else from `results.sextant.core_job.value`, which the contract does not define.
- **A reason for a release.** The contract has nowhere to carry one, so it stays in Countersign's local log.
- **Listing stopped items.** The contract has no call for it. Countersign lists only the items it stopped itself.
- **A credential that is not accepted while a decision waits.** `401` and `403` are sorted as refusals, so a decision in the spool at that moment is set aside and the owner has to make it again once the item is offered again. Retrying instead would keep the decision. This follows the instruction given for the build, and is noted here because it costs the owner a decision.
- How the owner signs in to a web inbox is not specified, so there is no web inbox. Decisions are made from the command line.
