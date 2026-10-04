# Maintain the documentation with Memoria

[Memoria](https://github.com/viktordanov/rs-memoria) connects each README to the files it covers and reports when a README needs review.
This procedure uses Memoria 0.8.0 (configuration `version = 3`). The managed agent skill (`memoria integrations skill install --target claude`) contains the full review contract.

## Which README covers a file

A README covers every selected file in its own folder and below it. It stops covering a subfolder only when it links to or imports a README in that subfolder: a handoff.
The root README links every other README, so each module's README alone covers the module's files. A nested README without that link makes both READMEs pending for an edit there, and `memoria status` reports it as `handoff_absent`.
`docs/README.md` covers all of `docs/`. The design records and the documentation rules have no Memoria markers, so they are not tracked documents. A marker in one of them would make it a tracked document of its own.
A section's `files=` must name files that its README covers, never another README or a file in a handed-off folder.

## When CI fails

The [Memoria workflow](../../.github/workflows/memoria.yml) runs `memoria check` on every push and pull request.
CI never reviews or acknowledges. When it fails, review the pending READMEs locally and commit the updated `memoria.lock`.

## Review loop

Run every command from the repository root. Keep review artifacts outside the repository: `--save` refuses a folder inside it.

```sh
mkdir -p /tmp/uah-memoria
memoria review
```

1. If the plan says to render, run `memoria render <README>` and read the plan again.
2. Save the artifact of the next README: `memoria review <README> --save /tmp/uah-memoria`. It prints the saved path and the `ack` command.
3. Read `memoria review <README>`: the Git hunk under each changed input, the review mode, and `memoria guidance <README>`. Add `--details` when a hunk is cut.
4. Read the sources the mode requires, then the whole README. `full_baseline` means every covered source.
5. Edit the prose if it no longer matches the code. After any edit, save a fresh artifact.
6. Acknowledge the saved artifact. The token comes from the artifact:

```sh
memoria ack <README> \
  --packet /tmp/uah-memoria/<artifact>.json \
  --reviewer <your-label> \
  --result updated \
  --note "<what you verified against this revision>"
```

Use `--result no-update` when the prose was already correct. Repeat until the plan is empty, then run `memoria check`. Review providers before consumers: the root README imports the docs summary, so it comes last.
Never edit an import body by hand, never edit `memoria.lock`, and never acknowledge when the plan is empty.

Routine commands print results, warnings, and errors only. Add `--verbose` or run `memoria lint` to see advisory hints.
A write command waits up to 10 seconds for another writer's lock. If it then reports `state_busy`, run the same command again.

## When the documentation rules change

A change to `docs/documentation/writing.md`, `docs/documentation/sections.md`, or the guidance in `memoria.toml` does not make a README pending, and `memoria check` still passes.
Run `memoria guidance --changed` to list the READMEs reviewed under older guidance. For each README that the change affects, run `memoria invalidate doc:<README> --reason "<what changed>"`, then follow the review loop. Never acknowledge a README only to clear this list.

## Adding a README

Add a README only for a reader who needs a contract of its own. Give it a `summary` export, link it from the root README (the handoff), import that summary where the root README mentions the topic, and run `memoria render README.md`.
Then follow the review loop.
