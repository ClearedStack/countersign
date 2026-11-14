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

> **Status: built, not yet deployed.** The code is here and passes its own tests, which include every saved example in Muster's contract. It has not been run against a live Muster and it is not deployed. [What is not yet done](#not-yet-done) is listed below.

Countersign is stage 04 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Countersign is meant to be understood and used without the rest.

Countersign never decides anything. It holds each item until the owner decides, checks that the decision is one the rules allow and is complete, writes it down, and hands it to Muster.

## What works today

- **A lease loop against Muster.** Every 20 seconds the worker sends what is in its spool, sends a heartbeat for every lease it holds, and leases until Muster answers that there is nothing more. It speaks Muster's [contract v1](https://github.com/ClearedStack/muster/blob/claude/repo-build-prompt-1csgx6/contract/v1/CONTRACT.md): the five worker calls and the release call, JSON over HTTP, the pod's ServiceAccount token as a bearer token.
- **An inbox on disk.** Scored items, `screened_out` items and pending edit proposals are checked on arrival and kept for the owner, urgent lane first, then normal, then backfill, in order of arrival.
- **The allowed outcomes, and no others.**

  | Step | Outcome | Moves it to | Must come with |
  | --- | --- | --- | --- |
  | Scored item | `approve` | `approved` | |
  | Scored item | `link` | `linked` | the existing tool (`-tool`) |
  | Scored item | `merge` | `merged` | the parent tool (`-parent`) and a reason |
  | Scored item | `park` | `parked` | the blocker (`-blocker`) |
  | Scored item | `reject` | `rejected` | a reason |
  | `screened_out` item | `override` | `observed` | a reason |
  | Edit proposal | `approve` | `approved`, on the `urgent` lane | |
  | Edit proposal | `decline` | `declined` | |

- **An override is refused without a written reason.** An override is reopening a `screened_out` item, or choosing an outcome other than the one the score recommends. A blank reason counts as none. A refused decision changes nothing.
- **Releasing a stopped item.** When a worker stops an item with `E_PARSE`, Muster holds it until the owner releases it. `countersign release ID` sends that decision with Muster's release call, and `countersign held` lists the items Countersign itself stopped.
- **A spool.** Each decision is written to disk before the item leaves the inbox. It leaves the spool as delivered only when Muster answers `200`. Results are sent oldest first.
  - **Try again** (`429`, any `5xx`, no answer): the result at the front waits, and everything behind it waits too.
  - **Lease lost** (`409 lease_not_held`): the decision is kept and goes out again under the new lease when Muster offers the item again. The owner is not asked twice.
  - **Refused** (any other status from `400` to `422`): the result is set aside in the log, marked refused, and the item stays as it was.
- **Three public error codes and nothing else.** `E_TIMEOUT`, `E_PARSE` and `E_NEEDS_SUBTOOL`. An error is a code, a rule, an item identifier, a fixed sentence and a fingerprint. It never carries a path, a host name, an address, a token or the text of an underlying failure.
- **Stop instead of guessing.** A leased step that is well-formed but unfamiliar (a kind, state, recommendation, kind of edit, lane or field with no rule) is not shown to the owner. Countersign asks Muster to pause it, and the rest carry on. A broken one is reported with `fail` and `E_PARSE`.
- **A careful client.** The token is read afresh for every call and no call is made without one. No redirect is followed, so the token cannot be carried to another host. No proxy from the environment is used. Answers are read up to a fixed size.
- **An in-memory stand-in for Muster**, and a fake Muster server on top of it, for running and testing alone.
- **A `ko` build** with no Dockerfile. The image runs as an ordinary user.
- **Inside the memory budget.** With the inbox full (256 of the largest proposals allowed), the worker and the largest command together peak at about 24 MiB of the 30 MiB budget. A test measures this on the built program, talking to a fake Muster over HTTP.
- **Writes only to the spool.** A test runs the built program and checks nothing is written to the working directory, the temporary directory or the home directory.

## How to run it

You need Go 1.24 or later. There are no dependencies outside the standard library.

```sh
go test ./...                         # every saved example and every other test
go build -o countersign ./cmd/countersign
```

The spool directory is `/var/spool/cs` unless `-spool` says otherwise.

### Against Muster

```sh
./countersign run -muster http://MUSTER-ADDRESS
```

`-token-file` names the ServiceAccount token and defaults to the path a pod is given. The loop runs until it is stopped. The owner decides with the other commands, run against the same spool directory, for example with `kubectl exec` in the pod.

### Alone, against the stand-in

In one terminal, run the loop over the made-up items in [`examples/standin.json`](examples/standin.json). Its first tick times out on purpose.

```sh
./countersign -spool ./spool run -standin examples/standin.json -interval 2s
```

In another terminal, be the owner:

```sh
./countersign -spool ./spool list              # what is waiting
./countersign -spool ./spool show PA-0002      # one item, and the outcomes allowed for it

# Refused: an override needs a written reason.
./countersign -spool ./spool decide PA-0002 override
# E_PARSE decision.override_reason_missing PA-0002: an override needs a written reason [fp 67665956733e504d]

./countersign -spool ./spool decide PA-0002 override -reason "Reshaped to do one job."
./countersign -spool ./spool decide PA-0001 approve
./countersign -spool ./spool decide EP-0001 approve
./countersign -spool ./spool log               # each result: waiting, acknowledged or refused
./countersign -spool ./spool held              # items Countersign stopped with E_PARSE
./countersign -spool ./spool release PA-0009   # let a stopped item run again
```

Within two seconds the first terminal logs `spool.acknowledged` for each decision. The stand-in lives in the memory of the `run` command, so stopping it forgets the leases. The spool is on disk and survives.

Other commands: `add [FILE]` checks in one item by hand for trying the rules (it has no lease, so a decision on it cannot be sent), `decide -f FILE` takes a decision as JSON, `rules` lists every rule with its error code, and `version` prints the version. A command exits with 0 on success, 1 with a public error on standard error, and 2 for a mistake in how it was called.

To build the image without publishing it:

```sh
KO_DOCKER_REPO=registry.invalid/countersign ko build ./cmd/countersign --push=false --tarball countersign.tar
```

## Rules and saved examples

Every rule has an identifier and at least one saved example: an input and the exact output expected, under [`testdata/rules/<rule>/<case>/`](testdata/rules). There are 46 rules and 167 examples. `go test` runs them all, fails if any output changes by a byte, and fails if a rule has no example.

| Rules | What they cover |
| --- | --- |
| `intake.*` | A step arriving from Muster: accepted, broken (`E_PARSE`) or unfamiliar (`E_NEEDS_SUBTOOL`) |
| `decision.*` | The owner's decision: accepted, or refused (`E_PARSE`) |
| `spool.*` | Handing a result to Muster: confirmed, not confirmed (`E_TIMEOUT`) or refused (`E_PARSE`) |
| `lease.*` | Holding leases: Muster not answering (`E_TIMEOUT`), and a lease that is no longer held |
| `store.unavailable` | The spool cannot be read or written (`E_TIMEOUT`) |

After a deliberate change, `go test . -update` rewrites the expected outputs. Read the difference before committing it.

[`testdata/contract/`](testdata/contract) is a copy of the 52 saved examples in Muster's contract, taken at Muster's commit `470dddf`. A test replays them against Countersign's client: each worker call and the release call is made and what is sent must match the example exactly, each refusal must be sorted correctly into "refused", "lease lost" or "try again", and each step Muster hands out must meet the rule expected for it.

## Layout

| Path | What it is |
| --- | --- |
| [`cmd/countersign`](cmd/countersign) | The program: the command line and the lease loop's entry point |
| [`internal/rule`](internal/rule) | The list of rules, the three error codes, and the only place a public error is made |
| [`internal/item`](internal/item) | Checking an arriving item, and the table of allowed moves |
| [`internal/decide`](internal/decide) | Checking the owner's decision and writing the record |
| [`internal/record`](internal/record) | The decision record |
| [`internal/store`](internal/store) | The inbox and the spool on disk |
| [`internal/muster`](internal/muster) | The calls to Muster: the HTTP client, the reading of a lease, the in-memory stand-in and the fake server |
| [`internal/worker`](internal/worker) | The lease loop |
| [`testdata/rules`](testdata/rules) | The saved examples |
| [`testdata/contract`](testdata/contract) | Muster's contract examples |
| [`.ko.yaml`](.ko.yaml) | The image build |

## Not yet done

Each of these is a requirement that is not built or not proven, with the reason.

| Requirement | Where it stands |
| --- | --- |
| Running against a live Muster | The client is built and replays Muster's saved examples, but it has only ever talked to the fake server in this repository. The first run against a real Muster is still to do. |
| Pausing an unfamiliar item | Countersign asks with `needs_subtool`. Muster's contract has no such move from any state Countersign holds, so Muster refuses. The item is not shown to the owner, the refusal is logged once, and the item comes back each time its lease runs out. The two specifications disagree and one has to change. |
| Where the score and title come from | Countersign reads Assay's evaluation in the form Muster's contract names: `score`, `recommendation` (`build`, `link`, `park` or `reject`), `split_flag`, and `gates` as a verdict for each gate by name. Assay's own branch gives these under other names today (`total`, `Build`, `split_merge_review`, gates as a list). Until Assay follows the contract, an item it scores is stopped here with `E_PARSE`. The title comes from `found.title`, or else Sextant's `core_job`, which the contract does not define. |
| Listing every stopped item | Muster's contract has no call for it. `held` lists only the items Countersign stopped itself, as far back as its local log goes. |
| Keeping a decision when the credential is rejected | `401` and `403` are sorted as refusals, as instructed for this build. A decision in the spool at that moment is set aside and has to be made again when the item is offered again. Retrying instead would keep it. |
| A web inbox for the owner | The specification does not say how the owner signs in. Decisions are made with the command line, run inside the pod. |
| The public `pub.*` views | They belong to Logbook and are fed by Muster. Countersign supplies the decision record as the result of each step. |
| Deployment: the Deployment and NetworkPolicy, the priority class, the spool volume | Not included yet. The program is ready for them: it needs no privileges, writes only to its spool and stops cleanly on `SIGTERM`. |
| Signing with `cosign`, and building in Keelworks | Done by other components. The `ko` build was run here and produces an image that runs as user 65532. |
| Read-only root filesystem, checked in a real pod | Checked here only by running the built program and confirming it writes nowhere but the spool. |
| The 5-layer anti-spam gate | It sits in front of Countersign, in Conn and Muster. Countersign checks a proposal's size again when it arrives. |

Choices made here that the specification does not state, so that they can be corrected: a held text field is at most 4096 bytes (512 for a title, 256 for a tool, parent, target, repository or file), a lease answer is read up to 256 KiB, the inbox holds at most 256 items, one call to Muster may take 10 seconds, the last 200 delivered or refused results are kept for `log`, and a failure of the local disk is reported as `E_TIMEOUT`.

`SPEC.md` was changed in several places while building. Each change and its reason is in the table at the [end of the specification](SPEC.md#changes-to-this-specification), along with the questions that are still open.

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
