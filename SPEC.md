# Countersign specification

> Technical specification for Countersign, the human-accountability queue worker powering tool determinations and anonymous edit approvals on /guides/#decide.

**Status: design.** This is a specification. It describes how Countersign is meant to work, not something that exists yet.

The same text is published at [clearedstack.com/platform/countersign/](https://clearedstack.com/platform/countersign/). To change the specification, change it here and say why.

## Overview & Role

Countersign (`v0.1.0-draft`) powers the human owner's decision and anonymous edit-approval inbox surfaced on `https://clearedstack.com/platform/public-edits/` and `https://clearedstack.com/platform/public-edits/`, replacing the original Stage 6 (Decide). Implemented in the Countersign repository under the MIT License, it records authoritative human determinations for evaluated candidates and gates visitor-submitted `✎` edit proposals before any code or configuration touches Plainforge.

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

Countersign interacts with Muster in the `clearedstack` namespace over ServiceAccount-authenticated HTTP (`CONTRACT_VERSION=v1`) and surfaces two transparent queues via `pub.*` read-only views on `clearedstack.com`:

1. **Tool Determinations Queue (`scored` and `screened-out` overrides):**
   - **Approve (Build) → `approved`:** Advances the candidate to Waybill for provenance stamping and clean-room spec compilation.
   - **Link → `linked`:** Records an existing free, private external tool in the catalog (Chartroom) without rebuilding.
   - **Merge → `merged`:** Folds scope into an existing or planned parent tool repository.
   - **Park → `parked`:** Defers a viable idea with a named blocker for scheduled Tidegauge revisit.
   - **Reject → `rejected`:** Permanently archives the candidate with its rubric score and rationale.
   - **Override → `observed`:** Returns a `screened-out` item to `observed` only when accompanied by an explicit written reason logged publicly.
2. **Anonymous Public Edit Proposals (`core.edit_proposal`, `pending_approval`):**
   - Receives `DATA`, `CONFIG`, and `ISSUE` proposals (≤ 4 KB) submitted via any `✎` button after they clear Muster's 5-layer anti-spam shield.
   - On owner **Approve**, dispatches Bellbuoy on the `urgent` lane (`Proposed` → `Approved` → `Building` → `Live`). Declined proposals are logged without altering Plainforge or `core.*` data.

## Sub-Tools & Failure Modes

Countersign intentionally has **zero sub-tools** (`subTools: []`), because final product determinations and edit approvals are human decisions rather than automated pattern rules.

### Sanitized Failure Modes

| Error Code | Trigger | Platform Behavior |
| --- | --- | --- |
| `E_PARSE` | Malformed determination payload, missing written rationale on an override, or invalid `ownership_map.json` target on an edit proposal. | Rejected cleanly by Muster; item remains in its current queue state with a fingerprinted error logged. |
| `E_TIMEOUT` | Transient timeout reaching Muster during decision commit. | Buffered in `/var/spool/cs` and retried in order until acknowledged. |

Because Countersign has no `tools/<case>/` sub-tools, it never enters `needs_subtool` (`E_NEEDS_SUBTOOL`).

## Security & Clean-Room Boundaries

- **5-layer anti-spam gate before Countersign:** Anonymous `✎` edit proposals must pass browser Proof-of-Work, a hidden honeypot field, daily-salted IP hash rate limiting (zero raw IPs stored), and strict 4 KB schema validation against `ownership_map.json` (with HTML/scripts stripped) before entering `pending_approval`.
- **Zero unreviewed cluster mutations:** No anonymous edit or external submission can touch Plainforge, `core.*` state, or running pods until explicitly approved in Countersign.
- **Public accountability & pod hardening:** All decisions, overrides, and edit outcomes are visible in read-only `pub.*` views without login. The pod runs non-root with dropped capabilities, a read-only root filesystem (except `/var/spool/cs`), zero database credentials, and default-deny `NetworkPolicy`.

## Related pages

- [How we decide what to build](https://clearedstack.com/platform/how-we-decide/)
- [Public edits and what stays private](https://clearedstack.com/platform/public-edits/)
- [An item’s life: fourteen states and three lanes](https://clearedstack.com/platform/item-lifecycle/)
- [The pipeline and its ten workers](https://clearedstack.com/platform/pipeline/)
