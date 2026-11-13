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

> **Status: built, not yet connected.** The code is here and passes its own tests against an in-memory stand-in for Muster. It cannot reach a real Muster yet, and it is not deployed. [What is not yet done](#not-yet-done) is listed below.

Countersign is stage 04 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Countersign is meant to be understood and used without the rest.

Countersign never decides anything. It holds each item until the owner decides, checks that the decision is one the rules allow and is complete, writes it down, and hands it to Muster.

## What works today

- **An inbox on disk.** Scored candidates, screened-out candidates and pending edit proposals are checked on arrival and held, urgent lane first, then normal, then backfill, in order of arrival.
- **The allowed outcomes, and no others.**

  | Item | Outcome | Moves it to | Must come with |
  | --- | --- | --- | --- |
  | Scored candidate | `approve` | `approved` | |
  | Scored candidate | `link` | `linked` | the existing tool (`-tool`) |
  | Scored candidate | `merge` | `merged` | the parent tool (`-parent`) and a reason |
  | Scored candidate | `park` | `parked` | the blocker (`-blocker`) |
  | Scored candidate | `reject` | `rejected` | a reason |
  | Screened-out candidate | `override` | `observed` | a reason |
  | Edit proposal | `approve` | `approved`, on the `urgent` lane | |
  | Edit proposal | `decline` | `declined` | |

- **An override is refused without a written reason.** An override is reopening a screened-out candidate, or choosing an outcome other than the one the score recommends. A blank reason counts as none. A refused decision changes nothing.
- **A spool.** Each decision is written to disk before the item leaves the inbox, and stays there until Muster confirms it. Records are sent oldest first. If Muster does not answer, the record at the front waits and everything behind it waits too. A record sent twice is applied once.
- **A worker loop** that sends the spool, keeps its leases alive, lets go of items whose lease ended, and leases more.
- **Three public error codes and nothing else.** `E_TIMEOUT`, `E_PARSE` and `E_NEEDS_SUBTOOL`. An error is a code, a rule, an item identifier, a fixed sentence and a fingerprint. It never carries a path, a host name, an address or the text of an underlying failure.
- **Stop instead of guessing.** An arriving item that is well-formed but unfamiliar (a kind, state, recommendation, proposal type or lane with no rule) is paused as `needs_subtool`. It is not shown to the owner, and the rest carry on.
- **An in-memory stand-in for Muster** that checks each record the way the specification says Muster does, including the ownership map for edit proposals.
- **A `ko` build** with no Dockerfile. The image runs as an ordinary user.
- **Inside the memory budget.** With the inbox full (256 of the largest proposals allowed), the worker and the largest command together peak at about 22 MiB of the 30 MiB budget. A test measures this.
- **Writes only to the spool.** A test runs the built program and checks nothing is written to the working directory, the temporary directory or the home directory.

## How to run it

You need Go 1.24 or later. There are no other dependencies.

```sh
go test ./...                         # every saved example and every other test
go build -o countersign ./cmd/countersign
```

The spool directory is `/var/spool/cs` unless `-spool` says otherwise. Try it with the made-up items in [`examples/standin.json`](examples/standin.json):

```sh
# Run the worker for three ticks against the stand-in. The first tick times out on purpose.
./countersign -spool ./spool run -standin examples/standin.json -interval 1s -ticks 3

./countersign -spool ./spool list              # what is waiting
./countersign -spool ./spool show PA-0002      # one item, and the outcomes allowed for it

# Refused: an override needs a written reason.
./countersign -spool ./spool decide PA-0002 override
# E_PARSE decision.override_reason_missing PA-0002: an override needs a written reason [fp 67665956733e504d]

./countersign -spool ./spool decide PA-0002 override -reason "Reshaped to do one job."
./countersign -spool ./spool decide PA-0001 approve
./countersign -spool ./spool decide EP-0001 approve
./countersign -spool ./spool log               # the records, each waiting until Muster confirms

# Run the worker again: the spooled decisions are delivered, in order.
./countersign -spool ./spool run -standin examples/standin.json -interval 1s -ticks 3
```

Other commands: `add [FILE]` checks in one item by hand, `decide -f FILE` takes a decision as JSON, `rules` lists every rule with its error code, and `version` prints the version. Without `-ticks`, `run` keeps going until it is stopped. A command exits with 0 on success, 1 with a public error on standard error, and 2 for a mistake in how it was called.

The stand-in lives in memory, so each `run` starts it afresh from the seed file. The spool is on disk and survives.

To build the image without publishing it:

```sh
KO_DOCKER_REPO=registry.invalid/countersign ko build ./cmd/countersign --push=false --tarball countersign.tar
```

## Rules and saved examples

Every rule has an identifier and at least one saved example: an input and the exact output expected, under [`testdata/rules/<rule>/<case>/`](testdata/rules). There are 40 rules and 119 examples. `go test` runs them all, fails if any output changes by a byte, and fails if a rule has no example.

| Rules | What they cover |
| --- | --- |
| `intake.*` | An item arriving: accepted, broken (`E_PARSE`) or unfamiliar (`E_NEEDS_SUBTOOL`) |
| `decision.*` | The owner's decision: accepted, or refused (`E_PARSE`) |
| `spool.*` | Handing a record to Muster: confirmed, not confirmed (`E_TIMEOUT`) or refused (`E_PARSE`) |
| `lease.*` | Holding leases: Muster not answering (`E_TIMEOUT`), and a lease that ended |
| `store.unavailable` | The spool cannot be read or written (`E_TIMEOUT`) |

After a deliberate change, `go test . -update` rewrites the expected outputs. Read the difference before committing it.

## Layout

| Path | What it is |
| --- | --- |
| [`cmd/countersign`](cmd/countersign) | The program: the command line and the worker loop's entry point |
| [`internal/rule`](internal/rule) | The list of rules, the three error codes, and the only place a public error is made |
| [`internal/item`](internal/item) | Checking an arriving item, and the table of allowed moves |
| [`internal/decide`](internal/decide) | Checking the owner's decision and writing the record |
| [`internal/record`](internal/record) | The record handed to Muster |
| [`internal/store`](internal/store) | The inbox and the spool on disk |
| [`internal/muster`](internal/muster) | The interface to Muster, and the in-memory stand-in |
| [`internal/worker`](internal/worker) | The loop between the spool, the inbox and Muster |
| [`testdata/rules`](testdata/rules) | The saved examples |
| [`.ko.yaml`](.ko.yaml) | The image build |

## Not yet done

Each of these is a requirement in `SPEC.md` that is not built, with the reason.

| Requirement | Why it is not done |
| --- | --- |
| Talking to the real Muster over ServiceAccount-authenticated HTTP | Muster does not exist yet and its `v1` contract (paths, payloads, lease length) is not written down. Countersign reaches Muster through a five-call interface (`lease`, `heartbeat`, `complete`, `fail`, `needs_subtool`), and only the stand-in implements it. `run` refuses to start without `-standin`. |
| A web inbox for the owner | The specification does not say how the owner signs in. Decisions are made with the command line, run inside the pod. |
| The public `pub.*` views | They belong to Logbook and are fed by Muster. Countersign supplies the records. The stand-in's ledger shows what they would contain. |
| Deployment: the Deployment and NetworkPolicy, the priority class, the spool volume | These need names from inside the cluster that are not in the registry entry, and Shipkeeper deploys them. The program is ready for them: it needs no privileges, writes only to its spool and stops cleanly on `SIGTERM`. |
| Signing with `cosign`, and building in Keelworks | Done by other components. The `ko` build was run here and produces an image that runs as user 65532. |
| Read-only root filesystem, checked in a real pod | Checked here only by running the built program and confirming it writes nowhere but the spool. |
| The 5-layer anti-spam gate | It sits in front of Countersign, in Conn and Muster. Countersign checks a proposal's size again when it arrives. |

Choices made here that the specification does not state, so that they can be corrected: a held text field is at most 4096 bytes (256 for a title, tool, parent or target), an item or decision is at most 32 KiB, the inbox holds at most 256 items, the worker ticks every 15 seconds, the last 200 confirmed records are kept for `log`, and a failure of the local disk is reported as `E_TIMEOUT`.

`SPEC.md` was changed in a few places while building. Each change and its reason is in the table at the [end of the specification](SPEC.md#changes-to-this-specification), along with the questions that are still open.

## In this repository

| File | What it is |
| --- | --- |
| [`SPEC.md`](SPEC.md) | The specification: what Countersign does, its interfaces, how it fails and how it is kept safe. |
| [`PROMPT.md`](PROMPT.md) | The prompt the build session was started with. It says what to build and the rules to build it under. |
| [`component.json`](component.json) | This component’s entry in the ClearedStack naming registry. |
| [`brand/`](brand) | The Countersign logo and mark, for light and dark backgrounds. |

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
