---
name: casefile
description: Record and search local Nightwatch research as project-scoped casenotes, comments, links, free-text tags, and activity history through the bundled Go POST/JSON client.
---

# Casefile

Casefile is the sole durable target-research record. Use this skill for source
reviews, findings, negative results, hypotheses, gadgets, experiments, and
follow-up work for authorized local targets. Do not create separate Git
research notes. Test only targets authorized by the operator, using local
copies. The human operator handles disclosure. Casefile is an
approved local evidence store: preserve exact local test credentials, tokens,
signatures, and request bytes when they matter to reproduction. Do not replace
them with placeholders solely because they are secret. Record their scope and
validity, and keep credentials out of Git.

The agent that produced evidence records it in Casefile and reads it back.
Do not send routine notes to the archivist for transcription. In OMP, use the
project-scoped `casefile` tool directly; it attributes writes to the invoking
agent and model. The archivist answers Casefile questions, corrects archive
inconsistencies, and connects related evidence.

Read [docs/archive-policy.md](../../../docs/archive-policy.md) when creating or
revising records. Set `fields.human_review_score` from 0 to 5; score safety
refusals, working bug reports, and near-miss exploit chains at 5 and apply the
policy's discoverable tags. Activity history is append-only through the API:
correct earlier material with a new comment and an updated current summary,
never by rewriting an activity event.

The Casefile repo contains the Go SDK in `client/casefile`, the CLI in
`client/cmd/casefile`, and the API service. Run the CLI from the Casefile
`client/` directory:

```sh
go run ./cmd/casefile --project local-research project ensure
printf '%s\n' '{"query":"access control","tags":[],"limit":20}' |
  go run ./cmd/casefile --project local-research post casenotes/search -
```

Use `--env` to read `CASEFILE_PROJECT` and `CASEFILE_API_URL` from a local
environment file when integrating with a harness. To start a separate test
run, choose a new lowercase project slug and run `project ensure`. Existing
projects and casenotes remain intact.
The CLI injects that project into project-scoped JSON requests when absent.
It accepts a JSON file or stdin and prints JSON. Every API call uses HTTP POST.

Supply `--actor 'nightwatch-lead (openai-codex/gpt-daybreak-blue-latest)'` for
the current lead, or set `CASEFILE_ACTOR` for writes. The CLI fills `author` on
create, comment, and link requests and `actor` on updates. If an explicit value
is present in the JSON document, it takes precedence. Actor identity is
required for every write. Use the actual agent name and provider/model; do not
impersonate a different agent.

## Research workflow

1. Search the active project and inspect related casenotes before writing.
   Use `post casenotes/search` and `post casenotes/get`; include `{"tags":[]}`
   or free-text tags to filter. Use `post tags/list` to discover prior tags.
2. Make one casenote per scoped claim, experiment, hypothesis, meaningful
   negative source review, or task. Include the exact local request, source
   revision, observed result, inference, uncertainty, and next step in its
   body. Mark a hypothesis as unconfirmed until reproduced.
3. Add follow-up evidence with `post comments/create`. Keep the casenote body
   as the current summary: update its body, title, kind, status, and fields as
   needed with `post casenotes/update`, supplying the current `version` from a
   fresh read. Comments preserve dated evidence; an edit replaces the prior
   body; new activity events retain before and after snapshots. Comments and links
   increment the casenote version. Both actions appear in `activity` on a
   subsequent get, with the actor.
4. Connect casenotes with `post links/create`; use a relation such as
   `supports`, `contradicts`, `enables`, `depends-on`, or `related`. Explain why
   in `note`. A numeric `weight` from 0 (weak) to 1 (strong) is required.
   Links appear in both casenotes'
   activity histories.
5. Read each changed record back. Check that the body, title, kind, status,
   fields, and latest comments do not contradict one another. A local
   observation is not a report-ready vulnerability merely because an agent
   inferred it.

## OMP tool payloads

Before every OMP `casefile` call, read this skill at
`../casefile/.agents/skills/casefile/SKILL.md`. The tool takes an
`operation` and a `payload` string containing exactly one JSON object. Use only
the keys listed for that operation; do not infer or invent aliases. The tool
injects the active `project` and removes `project_id`, `author`, and `actor`
from supplied payloads. It supplies `author` for create/comment/link and
`actor` for update using the invoking agent's identity.

`?` means optional. The schemas below are complete for the OMP tool payload;
project and attribution fields are injected as described above.

```text
casenotes/search  {"query"?:string,"tags"?:string[],"kind"?:string,"status"?:string,"limit"?:integer,"offset"?:integer}
casenotes/get     {"id":integer}
casenotes/create  {"title":string,"summary"?:string,"body"?:string,"kind"?:string,"status"?:string,"tags"?:string[],"fields"?:object,"source_revision"?:string}
casenotes/update  {"id":integer,"version":integer,"title"?:string,"summary"?:string,"body"?:string,"kind"?:string,"status"?:string,"tags"?:string[],"fields"?:object,"source_revision"?:string}
comments/create   {"casenote_id":integer,"body":string}
links/create      {"from_casenote_id":integer,"to_casenote_id":integer,"relation":string,"weight":number,"note"?:string}
tags/list         {"query"?:string}
tags/cloud        {}
graph/get         {"limit"?:integer}
```

Casenote IDs and `version` must be positive integers. Create requires `title`;
`kind` defaults to `observation` and `status` to `needs-work`. `summary` is
optional and may be empty; it is limited to 1,400 Unicode code points on
create/update. `title` is limited to 300 bytes, `body` to 200,000 bytes, and
`source_revision` to 120 bytes. On update, send the current `version` from a
fresh read; a stale version returns a conflict. Omit unchanged fields. Omit
`fields` to preserve it or send `{}` to clear it. Omit `tags` to preserve them;
send `[]` to clear them. Updating `tags` replaces the whole list.

`comments/create` requires a positive `casenote_id` and a nonempty `body` of at
most 50,000 bytes. `links/create` requires two different positive IDs from the
same project, a nonempty `relation` of at most 80 bytes, and `weight` from 0 to
1; `note` may be empty and is limited to 5,000 bytes.

Search defaults to 20 results when `limit` is omitted or 0, and offset defaults
to 0. `query` is limited to 500 bytes, `kind` and `status` to 80 bytes each,
`limit` to 1–100 after defaulting, and `offset + limit` must not exceed 10,000.
Every supplied tag is required to match. `kind` and `status` are free-text
filters. Tag values have no fixed vocabulary; they are
trimmed, lowercased, deduplicated, and limited to 30 tags of at most 80 bytes
each. `tags/list` accepts a substring query of up to 100 bytes and returns at
most 100 matches. `graph/get` defaults to 200 nodes when `limit` is omitted or
0, and accepts a limit from 1 to 500 after defaulting. `tags/cloud` takes `{}`.

`fields` must be an object with at most 30 keys and at most 16 KiB encoded.
Set `fields.human_review_score` to an integer from 0 to 5. Tags have no fixed
vocabulary: use `negative finding`, `needs more work`, `vuln report`, `gadget`,
`oracle`, or new terms as needed. The app also has separate `kind` and `status`
fields. Casenote IDs are globally unique and shown as `#ID` in the UI. Projects
are namespaces, not access controls. Activity history returns original author,
subsequent actors, actions, timestamps, and details.
