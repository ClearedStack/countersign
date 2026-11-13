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

Countersign interacts with Muster in the `clearedstack` namespace over ServiceAccount-authenticated HTTP (`CONTRACT_VERSION=v1`), using Muster's worker calls `lease`, `heartbeat`, `complete`, `fail` and `needs_subtool`. Its two queues are shown on `clearedstack.com` through the read-only `pub.*` views. Those views belong to Logbook and are fed by Muster from the records Countersign hands it; Countersign has no database access and creates no view itself.

Muster is Countersign's only dependency. Where an outcome below names another component (Waybill, Chartroom, Tidegauge, Bellbuoy), Countersign records the decision with Muster, and Muster routes the work. Countersign calls none of them.

1. **Tool Determinations Queue (`scored` and `screened-out` overrides):**
   - **Approve (Build) → `approved`:** Advances the candidate to Waybill for provenance stamping and clean-room spec compilation.
   - **Link → `linked`:** Records an existing free, private external tool in the catalog (Chartroom) without rebuilding.
   - **Merge → `merged`:** Folds scope into an existing or planned parent tool repository.
   - **Park → `parked`:** Defers a viable idea with a named blocker for scheduled Tidegauge revisit.
   - **Reject → `rejected`:** Permanently archives the candidate with its rubric score and rationale.
   - **Override → `observed`:** Returns a `screened-out` item to `observed` only when accompanied by an explicit written reason logged publicly.
   - **Choosing against the recommendation is an override too.** A `scored` item arrives with the outcome its score recommends. Any of the five outcomes above that differs from that recommendation needs a written reason, logged publicly and marked as an override. Merge is never the recommendation, so it always needs one.
   - **Each outcome carries what it describes.** Link names the existing tool, Merge names the parent tool, Park names the blocker, and Reject gives a written reason.
   - **No other move exists.** Approve, Link, Merge, Park and Reject apply only to a `scored` item, and Override only to a `screened-out` item.
2. **Anonymous Public Edit Proposals (`core.edit_proposal`, `pending_approval`):**
   - Receives `DATA`, `CONFIG`, and `ISSUE` proposals (≤ 4 KB, read as 4096 bytes of proposal text) submitted via any `✎` button after they clear Muster's 5-layer anti-spam shield. Countersign checks the size again on arrival.
   - On owner **Approve → `approved`**, the proposal is recorded on the `urgent` lane, and Muster dispatches Bellbuoy (`Proposed` → `Approved` → `Building` → `Live`; `Proposed` is the reader's label for the state `pending_approval`).
   - On owner **Decline → `declined`**, the proposal is logged without altering Plainforge or `core.*` data. A reason may be given and is not required. The state name `declined` is provisional until the naming registry or the item lifecycle confirms it.

## Sub-Tools & Failure Modes

Countersign intentionally has **zero sub-tools** (`subTools: []`), because final product determinations and edit approvals are human decisions rather than automated pattern rules.

### Sanitized Failure Modes

| Error Code | Trigger | Platform Behavior |
| --- | --- | --- |
| `E_PARSE` | Malformed determination payload; an outcome not allowed for the item's kind and state; missing written rationale on an override; a Link without its tool, a Merge without its parent, a Park without its blocker or a Reject without its reason; or invalid `ownership_map.json` target on an edit proposal. Also a leased item that is itself broken: not well-formed, missing a required field, or over its size limit. | A decision is refused by Countersign before it is recorded, or rejected cleanly by Muster (which holds `ownership_map.json`); the item remains in its current queue state with a fingerprinted error logged. A broken leased item is reported to Muster with `fail`, is not shown to the owner, and keeps its state. |
| `E_TIMEOUT` | Transient timeout reaching Muster during decision commit. Any failure to reach Muster that is not a clean refusal counts as a timeout. | Buffered in `/var/spool/cs` and retried in order until acknowledged. Each record carries an identifier derived from its content, so a record sent twice is applied once. |
| `E_NEEDS_SUBTOOL` | A leased item that is well-formed but that Countersign has no rule for: an unfamiliar kind of item, state, recommendation, type of edit proposal or priority lane. | That one item is paused as `needs_subtool` with Muster and is not shown to the owner. Every other item carries on. |

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

Still open, and not changed here because the right answer is not in this repository:

- The summary line refers to `/guides/#decide`. The guides page has no such anchor.
- The overview says Countersign replaces "the original Stage 6 (Decide)". The registry says it is stage 4 and was `cs-04-decide`.
- Muster's `v1` wire contract (paths, payloads, lease length) is not written down, so the HTTP client is not built.
- How the owner signs in to a web inbox is not specified, so there is no web inbox. Decisions are made from the command line.
- The form of an edit proposal's identifier is not specified. Countersign treats identifiers as opaque.
