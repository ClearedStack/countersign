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

> **Status: ready to deploy.** The code builds with the Go standard library only, passes `go vet ./...` and `go test -race ./...` (including all 52 saved examples in Muster's contract v1), has been verified against Muster and Logbook (PostgreSQL), and includes hardened Kubernetes manifests under [`deploy/`](deploy). [What is not yet done](#not-yet-done) lists what waits on other components or cluster rollout.

Countersign is stage 04 of the ClearedStack pipeline. ClearedStack is an open pipeline that finds ideas for government-contracting tools, screens and scores them, and builds each one from the public rule. Countersign is meant to be understood and used without the rest.

Countersign never decides anything. It holds each item until the owner decides, checks that the decision is one the rules allow and is complete, writes it down, and hands it to Muster.

## What works today

- **A lease loop against Muster.** Every 20 seconds the worker sends what is in its spool, sends a heartbeat for every lease it holds, and leases until Muster answers that there is nothing more. It speaks Muster's [contract v1](https://github.com/ClearedStack/muster/blob/main/contract/v1/CONTRACT.md): the five worker calls and the release call, JSON over HTTP, the pod's ServiceAccount token as a bearer token.
- **An inbox on disk.** Scored items, `screened_out` items and pending edit proposals are checked on arrival (with browser `CRLF` line endings normalized to `LF`) and kept for the owner, urgent lane first, then normal, then backfill, in order of arrival.
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

- **An override is refused without a written reason.** An override is reopening a `screened_out` item, or choosing an outcome other than the one the score recommends. A blank reason counts as none, and every reason is capped at 2000 bytes to match Logbook's `core.decision` rule. A refused decision changes nothing.
- **Releasing a stopped item.** When a worker stops an item with `E_PARSE`, Muster holds it until the owner releases it. `countersign release ID` sends that decision with Muster's release call, and `countersign held` lists the items Countersign itself stopped.
- **A spool.** Each decision is written to disk before the item leaves the inbox. It leaves the spool as delivered only when Muster answers `200`. Results are sent oldest first.
  - **Try again** (`429`, any `5xx`, no answer): the result at the front waits, and everything behind it waits too.
  - **Lease lost** (`409 lease_not_held`): the decision is kept and goes out again under the new lease when Muster offers the item again. The owner is not asked twice.
  - **Refused** (any other status from `400` to `422`): the result is set aside in the log, marked refused, and the item stays as it was.
- **Three public error codes and nothing else.** `E_TIMEOUT`, `E_PARSE` and `E_NEEDS_SUBTOOL`. An error is a code, a rule, an item identifier, a fixed sentence and a fingerprint. It never carries a path, a host name, an address, a token or the text of an underlying failure.
- **Stop instead of guessing.** A leased step that is well-formed but unfamiliar (a kind, state, recommendation, kind of edit, lane or field with no rule) is not shown to the owner. Countersign asks Muster to pause it, and the rest carry on. A broken one is reported with `fail` and `E_PARSE`.
- **A careful client.** The token is read afresh for every call and no call is made without one. No redirect is followed, so the token cannot be carried to another host. No proxy from the environment is used. Answers are read up to a fixed size.
- **An in-memory stand-in for Muster**, and a fake Muster server on top of it, for running and testing alone.
- **A `ko` build and hardened Kubernetes manifests** in [`deploy/`](deploy) and [`kustomization.yaml`](kustomization.yaml) with no Dockerfile: namespace `cs-workers`, digest-pinned image in Lazarette, non-root user `65532`, read-only root filesystem, all capabilities dropped, `30Mi` memory budget, `arm64` node affinity excluding `cm3588-plus`, a persistent spool volume, and a default-deny `NetworkPolicy` allowing egress only to Muster (`:8080`) and cluster DNS (`:53`).
- **Inside the memory budget.** With the inbox full (256 of the largest proposals allowed), the worker and the largest command together peak at about 24 MiB of the 30 MiB budget. A test measures this on the built program, talking to a fake Muster over HTTP.
- **Writes only to the spool.** A test runs the built program and checks nothing is written to the working directory, the temporary directory or the home directory.

## How to run it

You need Go 1.24 or later. There are no dependencies outside the standard library.

```sh
go vet ./...
go test -race ./...                   # every saved example and every other test
go build -o countersign ./cmd/countersign
```

The spool directory is `/var/spool/cs` unless `-spool` or `COUNTERSIGN_SPOOL` says otherwise.

### Against Muster

```sh
./countersign run -muster http://MUSTER-ADDRESS:8080
```

`MUSTER_URL` supplies the address when `-muster` is omitted, and `CONTRACT_VERSION` (when set) must be `v1`. `-token-file` names the ServiceAccount token and defaults to the path a pod is given. The loop runs until it is stopped. Because the `ko` static base image contains no shell, the owner runs `/ko-app/countersign` directly with `kubectl exec` against `/var/spool/cs` (for example `kubectl -n cs-workers exec -it deploy/countersign -- /ko-app/countersign review`).

### Alone, against the stand-in

In one terminal, run the loop over the made-up items in [`examples/standin.json`](examples/standin.json). Its first tick times out on purpose.

```sh
./countersign -spool ./spool run -standin examples/standin.json -interval 2s
```

In another terminal, be the owner:

```sh
./countersign -spool ./spool status            # one-screen summary (-json for scripts)
./countersign -spool ./spool list              # what is waiting
./countersign -spool ./spool review            # step through waiting items interactively (-kind, -lane, or ID...)
./countersign -spool ./spool show PA-0002      # one item, and the outcomes allowed for it

# Refused: an override needs a written reason.
./countersign -spool ./spool decide PA-0002 override
# E_PARSE decision.override_reason_missing PA-0002: an override needs a written reason [fp 67665956733e504d]

./countersign -spool ./spool decide PA-0002 override -reason "Reshaped to do one job."
./countersign -spool ./spool decide PA-0001 approve
./countersign -spool ./spool decide PA-0001 approve -waybill spec.json   # approve and attach the specification (see below)
./countersign -spool ./spool decide EP-0001 approve
./countersign -spool ./spool log               # each result: waiting, acknowledged or refused
./countersign -spool ./spool held              # items Countersign stopped with E_PARSE
./countersign -spool ./spool release PA-0009   # let a stopped item run again
```

Within two seconds the first terminal logs `spool.acknowledged` for each decision. The stand-in lives in the memory of the `run` command, so stopping it forgets the leases. The spool is on disk and survives.

Other commands: `status [-json]` summarizes waiting inbox counts by lane and kind alongside outbox, stopped and settled counts, `review [-kind item|proposal] [-lane urgent|normal|backfill] [ID...]` walks waiting items one at a time and prompts only for the fields each outcome needs (leaving skipped or interrupted items untouched), `add [FILE]` checks in one item by hand for trying the rules (it has no lease, so a decision on it cannot be sent), `decide -f FILE` takes a decision as JSON, `rules` lists every rule with its error code, and `version` prints the version. A command exits with 0 on success, 1 with a public error on standard error, and 2 for a mistake in how it was called.

### Approving with a specification

Waybill (stage 5) builds only from what the owner approved, so an approval has to carry the specification the owner reviewed. `decide ID approve -waybill FILE` attaches it:

- The flag is allowed only with `approve` on a `scored` item, or with `override` on a `screened_out` one. With any other outcome, or on an edit proposal, the decision is refused (`decision.field_unexpected`). It cannot be given with `decide -f`, and a decision file cannot carry one.
- FILE is one JSON object, at most 48 KiB, in the form Waybill's parser accepts: UTF-8, no duplicate key at any depth, nesting at most 32 deep, no field Waybill does not know, a `title`, a `tool_type`, a `tool` object and an `authority` with an `https` address, and no key Waybill reserves for raw observations of a vendor's product (`raw_html`, `screenshot` and so on). `item_id` and `state` may be left out; if given, `item_id` must be the item's and `state` the state the decision moves it to. Countersign reads no further: whether the tool is right is for the owner and Waybill to judge.
- The object is carried unchanged, with white space removed, as `waybill` in the decision record, both in the spool and in the `result` of the `complete` call. Nothing else in the record changes. The record, as sent, must stay within 63 KiB so the whole call is within Muster's 64 KiB limit.
- A missing, unreadable, empty, oversized or malformed file refuses the decision with one fixed message (`decision.malformed`) that names neither the file nor the reason, and nothing is spooled. The item stays in the inbox.
- `review` does not take a specification. Approve with `decide` when one is needed.

To build the image without publishing it:

```sh
KO_DOCKER_REPO=registry.invalid/countersign ko build --base-import-paths --push=false --tarball=countersign.tar ./cmd/countersign
```

## Rules and saved examples

Every rule has an identifier and at least one saved example: an input and the exact output expected, under [`testdata/rules/<rule>/<case>/`](testdata/rules). There are 46 rules and 171 examples. `go test` runs them all, fails if any output changes by a byte, and fails if a rule has no example.

| Rules | What they cover |
| --- | --- |
| `intake.*` | A step arriving from Muster: accepted, broken (`E_PARSE`) or unfamiliar (`E_NEEDS_SUBTOOL`) |
| `decision.*` | The owner's decision: accepted, or refused (`E_PARSE`) |
| `spool.*` | Handing a result to Muster: confirmed, not confirmed (`E_TIMEOUT`) or refused (`E_PARSE`) |
| `lease.*` | Holding leases: Muster not answering (`E_TIMEOUT`), and a lease that is no longer held |
| `store.unavailable` | The spool cannot be read or written (`E_TIMEOUT`) |

After a deliberate change, `go test . -update` rewrites the expected outputs. Read the difference before committing it.

[`testdata/contract/`](testdata/contract) is a copy of the 52 saved examples in Muster's contract, taken at Muster's commit `470dddf`. A test replays them against Countersign's client: every worker call and release call (both main requests and setup calls in `given.calls`) is made and must match the example wire request and headers, each refusal must be sorted into "refused", "lease lost" or "try again", and each step Muster hands out must meet the rule expected for it.

Native Go fuzz targets (`FuzzDecodeLease`, `FuzzItemParse`, `FuzzDecideParseAndApply`, `FuzzRelease`, `FuzzReadPromptLine`) check that untrusted lease payloads, visitor proposals, decision inputs and interactive prompt streams never panic, never leak input text in errors, and always preserve Logbook's byte and control-character limits.

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
| [`deploy/`](deploy) | Kubernetes `ServiceAccount`, `PersistentVolumeClaim`, `Deployment` and default-deny `NetworkPolicy` in `cs-workers` |
| [`kustomization.yaml`](kustomization.yaml) | Kustomize entry point listing the manifests in `deploy/` |
| [`testdata/rules`](testdata/rules) | The saved examples |
| [`testdata/contract`](testdata/contract) | Muster's contract examples |
| [`.ko.yaml`](.ko.yaml) | The image build |

## Not yet done

Each of these waits on another component or on cluster rollout.

| Requirement | Where it stands |
| --- | --- |
| Pausing an unfamiliar item | Countersign asks with `needs_subtool`. Muster's contract has no such move from `scored`, `screened_out` or `pending_approval`, so Muster answers `409 move_not_allowed`. The item is not shown to the owner, the refusal is logged once, and the item comes back each time its lease runs out. |
| Proposal `file` and `reason` across a Muster restart | Assay and Muster on `main` now match `CONTRACT.md` and reconstruct `results.assay` and `results.countersign` from Logbook's `core.evaluation` and `core.decision` tables across restarts. An edit proposal's optional `file` and `reason` still live in Muster's process memory until Logbook's `core.edit_proposal` adds columns for them. |
| Keelworks and Shipkeeper `app.yaml` worker schema | Keelworks builds `./cmd/countersign` with `ko` via [`.ko.yaml`](.ko.yaml), signs the image into Lazarette, and pins `digest:` in a root `app.yaml` in Plainforge. Shipkeeper's current `app.yaml` parser requires a listening TCP `port` (`1..65535`) and has no fields for `args: ["run"]`, `PersistentVolumeClaim` spool mounts, or Muster/DNS egress; until `app.yaml` covers portless `cs-workers` workloads, Countersign is applied from [`deploy/`](deploy) and observed read-only by Shipkeeper. |
| Listing every stopped item, and releasing stopped proposals | Muster's contract has no call to list stopped items across all workers (`held` lists those Countersign stopped itself), and `POST /v1/release` accepts `PA-...` item IDs only, not `EP-...` proposal IDs. |
| Keeping a decision when the credential is rejected | `401` and `403` are sorted as refusals. A decision in the spool at that moment is set aside and has to be made again when the item is offered again. |
| A web inbox for the owner | The specification does not say how the owner signs in. Decisions are made with `review` or `decide` inside the pod. |
| Cluster prerequisites at deploy time | `cs-workers` namespace, a signed `countersign@sha256:...` image in Lazarette written by Keelworks, and the worker `PriorityClass` (omitted until created in-cluster; `clearedstack-high` and `cs-build-low` are defined in Logbook and Keelworks). |

Choices made here that the specification does not state, so that they can be corrected: a decision or release reason is at most 2000 bytes (matching Logbook's `core.decision` rule), a blocker or held screening note is at most 4096 bytes (512 for a title, 256 for a tool, parent, target, repository or file), a lease answer is read up to 256 KiB, the inbox holds at most 256 items, one call to Muster may take 10 seconds, the last 200 delivered or refused results are kept for `log`, and a failure of the local disk is reported as `E_TIMEOUT`.

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
