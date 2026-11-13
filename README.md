<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="brand/logo-dark.svg">
    <img src="brand/logo-light.svg" alt="Countersign" height="56">
  </picture>
</p>

<p align="center">
  <strong>Holds scored candidates and edit proposals until a person decides.</strong><br>
  Part of <a href="https://clearedstack.com">ClearedStack</a>
</p>

---

> **Status: design.** The first milestone is built: a command-line store of pending items and recorded decisions. Most of the specification is not built yet. See [What works today](#what-works-today) and [Not built yet](#not-built-yet).

Countersign is stage 04 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Countersign is meant to be understood and used without the rest.

## What works today

A command-line program that keeps pending items and recorded decisions in one JSON file. It needs Go 1.24 and nothing else. It makes no network calls and reads no clock, so the same input gives the same output everywhere.

```sh
go build -o countersign ./cmd/countersign

# Hold the items in a saved lease. The file stands in for Muster.
./countersign -store queue.json lease -from cmd/countersign/testdata/cases/lease-holds-valid-items/lease.json

./countersign -store queue.json pending
./countersign -store queue.json decide -id cand-0001 -outcome park -reason "Waiting on the sample data set."
./countersign -store queue.json decide -id cand-0002 -outcome override
#   E_PARSE: an override needs a written reason (fingerprint 5be3008405d2)
./countersign -store queue.json decide -id cand-0002 -outcome override -reason "The screen missed that this replaces a paper form."
./countersign -store queue.json decisions
./countersign outcomes
```

The allowed outcomes are these and no others:

| Item | State when leased | Outcome | State afterwards | Written reason |
| --- | --- | --- | --- | --- |
| `candidate` | `scored` | `approve` | `approved` | optional |
| `candidate` | `scored` | `link` | `linked` | optional |
| `candidate` | `scored` | `merge` | `merged` | optional |
| `candidate` | `scored` | `park` | `parked` | optional |
| `candidate` | `scored` | `reject` | `rejected` | optional |
| `candidate` | `screened-out` | `override` | `observed` | **required** |
| `edit_proposal` | `pending_approval` | `approve` | `Approved` | optional |
| `edit_proposal` | `pending_approval` | `decline` | `declined` | optional |

What it does with everything else:

- **An override with no written reason, or a blank one, is refused** with `E_PARSE`. The item stays as it was.
- An outcome that is not in the table for the item's kind and state is refused with `E_PARSE`. So is an unknown outcome, an unknown id, and a second decision on an item already decided. A refusal never changes the store.
- A leased item that is broken (not a JSON object, no usable id, a field of the wrong type, an edit proposal with no type or a body over 4096 bytes) is refused with `E_PARSE` and the rest of the lease carries on.
- A leased item that is valid but that no rule covers (an unknown kind, state, proposal type or field) is held, shown as paused `needs_subtool`, and accepts no decision (`E_NEEDS_SUBTOOL`). This differs from `SPEC.md`, which says Countersign never pauses an item; the question is open in the first pull request.
- Decisions are numbered 1, 2, 3 in the order they were recorded. No time is recorded.
- Errors use fixed text and never include a path, an address or input data. Exit codes: `0` done, `1` refused with a public error code, `2` wrong command line, `3` a file could not be read or written.

One person runs one command at a time. Two commands writing the same store at the same moment are not guarded against.

### Tests

```sh
go test ./...
```

Every rule has a saved example under [`cmd/countersign/testdata/cases`](cmd/countersign/testdata/cases): the command line, the store before, and the exact output, exit code and store expected after. A test fails if a row is added to the table of outcomes without one. The tests use made-up data and no network.

## Not built yet

Everything else in `SPEC.md`:

- Talking to Muster over HTTP. Today Muster is an interface with an in-memory stand-in that only hands out a lease from a file. Nothing is sent back to Muster.
- The spool in `/var/spool/cs`, retrying after `E_TIMEOUT`, and acknowledgments.
- Any screen or page for the owner, and the public `pub.*` views.
- Dispatching Bellbuoy when an edit proposal is approved, and anything involving Waybill, Chartroom or Tidegauge.
- Checking an edit proposal's target against `ownership_map.json`.
- The always-on pod: the `ko` build, the Kubernetes manifests, the hardening. The 30 MiB memory budget has not been measured.

## In this repository

| File | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | The specification: what Countersign does, its interfaces, how it fails and how it is kept safe. |
| [`PROMPT.md`](PROMPT.md) | The prompt to start a build session with. It says what to build and the rules to build it under. |
| [`component.json`](component.json) | This component’s entry in the ClearedStack naming registry. |
| [`brand/`](brand) | The Countersign logo and mark, for light and dark backgrounds. |
| [`cmd/countersign`](cmd/countersign) | The command-line program and its saved examples. |
| [`internal/queue`](internal/queue) | The rules: what is held, which outcomes are allowed, when a decision is refused. |
| [`internal/store`](internal/store) | The JSON file of held items and recorded decisions. |
| [`internal/muster`](internal/muster) | The interface to Muster and its in-memory stand-in. |
| [`internal/fault`](internal/fault) | The three public error codes. |

## At a glance

| | |
| --- | --- |
| **Stage** | 04 of 10 in the pipeline |
| **Formerly** | `cs-04-decide`, then Gavel |
| **Namespace** | `cs-workers` |
| **Container image** | `lazarette.clearedstack.svc.cluster.local:5000/countersign` |
| **Memory budget** | 30 MiB |
| **Depends on** | [Muster](https://github.com/ClearedStack/muster) |
| **Depended on by** | Nothing else in the platform |

### Boundaries

- Leases scored candidates and pending proposals from Muster
- Presents them to the owner and records the decision, with a reason for any override

## Names

`countersign` is this component’s identifier everywhere: the repository, the workload, the container image. The one place names are written down is the [naming registry](https://clearedstack.com/platform/registry.json), kept in [`clearedstack.com`](https://github.com/ClearedStack/clearedstack.com/blob/main/src/data/registry.json). `component.json` here is a copy of this component’s entry. If the two disagree, the registry is right.

## Ground rules

- **No AI when it runs.** The same input gives the same output on any machine.
- **Stop instead of guessing.** A case with no rule is paused and reported, never improvised.
- **Clean room.** Written from scratch. No code, text or design is copied from another project.
- **Public sources and made-up data only.** Nothing classified, controlled or taken from a real contract, in code, tests, issues or commit messages.

## License

[MIT](LICENSE). ClearedStack is an independent personal project. It is not affiliated with, endorsed by, or sponsored by the U.S. Department of Defense or any other government agency.
