# Muster contract v1 examples

These files are a copy of `contract/v1/examples/` from
[ClearedStack/muster](https://github.com/ClearedStack/muster), branch
`claude/repo-build-prompt-1csgx6`, at commit `a362cb1`. They are Muster's saved
examples of every call and every refusal in its contract. They are not edited
here. To update them, copy the directory again and record the new commit.

`go test ./internal/muster -run TestContractExamples` replays them against
Countersign's client:

- For each of the five worker calls and the release call, the client makes the call in the example
  and the test checks that what it sends is the example's request, byte for
  byte once white space is removed. The answer in the example is served back,
  and the test checks what the client makes of it.
- For each refusal, whatever call it was given on, the status and body are
  served back and the test checks that the client sorts it correctly into
  "refused", "lease lost" or "try again", and that nothing from the body reaches the error.
- For each lease that is answered with a step, the test checks which rule
  Countersign applies to it.

Examples of calls that are not a worker's (`/v1/edits`, `/v1/items`,
`/v1/health`, `/v1/feed`, `/healthz`) are counted and otherwise left alone.

All data in them is made up.
