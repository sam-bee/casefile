# Nightwatch casefile

A small, local research casefile for the Nightwatch agents. PostgreSQL is the
source of truth for casenotes, comments, tags, and links. Elasticsearch holds a
rebuildable search index. The Go service exposes a POST-only application API and
serves a single-page browser interface.

The casefile stores research for authorized local targets. Running it does not
authorize testing a target or publishing findings; the human operator handles
disclosure.

## Run

From this directory:

```sh
./scripts/init-env.sh
docker compose up --build -d
```

Open <http://127.0.0.1:8090/>. The only published port is the web interface,
bound to loopback. To use another loopback port, edit `CASEFILE_PORT` in the
ignored `.env` file before starting the stack. PostgreSQL and Elasticsearch are
reachable only inside this Compose network.

The browser opens in dark mode. The sun/moon button in the header switches
between dark and light modes and remembers the choice in that browser.
Search runs when the Search button is pressed (or Enter is used in a filter).
The left panel can filter human review scores from 1 to 5 by exact match (`=`)
or minimum score (`≥`); “Any score” leaves that filter off.
The project tag cloud shows the most common tags with casenote counts in
brackets and filters on click. Casenote bodies render Markdown in the detail
view, as do comments; the editor retains the Markdown source.

The app container runs as UID 1000/GID 1984 for local bind-directory access.
The JavaScript production bundle is built in the Dockerfile's Node stage; no
host-side npm installation or build is needed.

PostgreSQL and Elasticsearch data live in the local directory named by
`CASEFILE_DATA_DIR` in `.env`, outside this Git repository. The init script
chooses `${XDG_STATE_HOME:-$HOME/.local/state}/nightwatch-casefile` by default;
set `CASEFILE_DATA_DIR` in `.env` to use another local directory. Make sure it
has room for Elasticsearch. `docker compose down` keeps that data; remove it
only when you intend to delete the canonical PostgreSQL records. The local
`.env` contains a generated database password, is mode 0600, and is ignored
by Git.

## Record model

- **Project:** a named research namespace with a stable slug. Casenotes, search,
  tag suggestions, and graph views are scoped to one project. A new database
  starts with `local-research` (`Local research`); existing installations retain
  their project slugs and records. Casenote IDs remain globally unique, so `#6`
  identifies the same record in every context. Projects organize records; they
  are not access controls in this loopback-only app.
- **Casenote:** a project, title, optional short summary, body, kind, status, author, source revision,
  timestamps, free-text tags, and an optional `fields` JSON object. Use fields
  for extra values such as `{"cvss_score":7.5}` without changing the schema.
  The short summary is limited to 1,400 Unicode code points after trimming
  surrounding whitespace; this counts code points rather than UTF-8 bytes or
  user-perceived grapheme clusters. Keep detailed evidence and Markdown in the
  body. The activity record's own `summary` is a separate event label.
  Keep one specific claim, test, question,
  or task per casenote. Include exact local evidence and separate observations from
  inference.
- **Comment:** a dated addition or correction to a casenote. Add a comment when
  new evidence changes the interpretation; update the short summary or detailed
  body as needed.
- **Link:** a directed relationship between two casenotes in the same project,
  with a relation name, required weight, and a short reason. Typical relations include `supports`,
  `contradicts`, `enables`, `depends-on`, `duplicates`, and `related`.
- **Activity:** an append-only record of creation, edits, comments, and links
  on each casenote, with a timestamp and the actor who performed the action.
  Authors and actors use `name (provider/model)` for traceability.

Use `fields.human_review_score` (integer 0–5) for human review importance.
The UI shows it in the editor, result cards, and record detail. The tags
`agent refusal`, `working bug report`, and `near-miss exploit chain` force
score 5 on create or update. See [the archive policy](docs/archive-policy.md)
for scoring, corrections, and evidence guidance. The API offers no operation
to edit or delete activity events; new edits append before/after snapshots.

Tags have no predefined vocabulary. Agents can add terms such as `negative
finding`, `gadget`, or `oracle`; the interface also offers kind and status
filters so these different concepts remain searchable. Tags are normalized to
lowercase for matching. Every link requires a weight from 0 (weak) to 1 (strong).
Two older unweighted demo links were assigned 0.5 during migration; their
activity histories state that this neutral value was not assessed. The browser
has an interactive graph of a project's casenotes and links; node colors show casenote
kind, and link details show their relation and weight. Select a
node to open its record. The graph reports when its node limit truncates a
large project.

## API

All `/api/` routes accept **POST** with a JSON object and
`Content-Type: application/json`. The browser uses GET only for
the page and static assets. Useful endpoints are:

| Endpoint | Purpose |
| --- | --- |
| `/api/projects/list` | List research projects. |
| `/api/projects/create` | Create a project with a name and optional slug. |
| `/api/casenotes/search` | Search text and filter by project, tags, kind, status, or human review score. A blank query lists recent casenotes. |
| `/api/casenotes/get` | Read one casenote with its comments, links, and activity. |
| `/api/casenotes/create` | Create a casenote in a project. |
| `/api/casenotes/update` | Edit a casenote using its version and an actor. |
| `/api/comments/create` | Add a comment. |
| `/api/links/create` | Connect two casenotes in the same project. |
| `/api/tags/list` | Suggest tags within a project; new tags remain allowed. |
| `/api/tags/cloud` | Return up to 100 project tags with casenote counts. |
| `/api/graph/get` | Read project-scoped graph nodes and typed, weighted edges. |
| `/api/health` | Check service readiness. |

For example:

```sh
curl -fsS -X POST http://127.0.0.1:8090/api/casenotes/search \
  -H 'Content-Type: application/json' \
  -d '{"project":"local-research","query":"access control","tags":[],"limit":20}'
```

Use `POST /api/projects/list` to discover slugs. Requests that omit a project
use `local-research`. Supply a `project` slug or `project_id` for project-scoped
searches, casenote creation, tag suggestions, and graph reads. The graph reads
its nodes and links from PostgreSQL, so it reflects committed records even
while search indexing catches up.

Casenote create and update accept an optional `summary` string of up to 1,400
Unicode code points. Omit `summary` on update to preserve its current value, or
send `"summary":""` to clear it. The summary is returned by get and search and
is included in full-text search independently of the body and activity event
labels. Casenote create and update also accept `"fields":{"cvss_score":7.5}`. The value
must be a JSON object with at most 30 keys and 16 KiB of content. Omit `fields`
on update to keep the existing object, or send `{}` to clear it. Optional
fields are included in text search and shown in the detail view. Link creation
requires a numeric `weight` between 0 and 1.

The search index is derived from PostgreSQL and can be rebuilt. The service
keeps track of pending index updates and retries them if Elasticsearch is
temporarily unavailable. Recent writes may have a short search delay; casenote
reads come from PostgreSQL.

For score filtering, send `review_score` as an integer from 1 to 5 and
`review_operator` as `eq` or `gte` to `/api/casenotes/search`. Omit both to
leave scores unfiltered. Records without a score are indexed as 0. When the
numeric search mapping is first added to an existing index, the service
reindexes existing Casenotes from PostgreSQL.

## Go client and agent skill

The Go SDK and CLI live in `client/`. No third-party Go dependencies are
required. From `client/`:

```sh
go run ./cmd/casefile --project local-research project ensure
printf '%s\n' '{"query":"ownership","tags":[],"limit":20}' |
  go run ./cmd/casefile --project local-research post casenotes/search -
printf '%s\n' '{"title":"Local ownership check","summary":"No cross-owner update reproduced.","body":"Detailed evidence...","tags":["negative finding"],"source_revision":"abc123"}' |
  go run ./cmd/casefile --project local-research \
    --actor 'research-agent (deepseek/deepseek-flash)' post casenotes/create -
```

The CLI accepts `--project` and `--url`, or reads `CASEFILE_PROJECT` and
`CASEFILE_API_URL` from the process environment or a file supplied with
`--env`. Change the project slug and run `project ensure` to start a fresh run
without deleting older records. Its `post` command accepts a JSON file or `-`
for stdin, fills in the active
project, and fills in `author` or `actor` from `--actor` or `CASEFILE_ACTOR`
when the JSON omits it. It returns the response as JSON. The SDK exposes the
same POST client through `casefile.New`, `Post`, and `EnsureProject`.

The bundled agent instructions are in
[.agents/skills/casefile/SKILL.md](.agents/skills/casefile/SKILL.md).
Agents started in this repository can discover the skill from the standard
project-local skills directory. Agents started elsewhere can read the file
explicitly or install it into their own skill path.

The OMP archivist source is in `client/omp/archivist.md` and its Casefile-only
tool is `client/omp/casefile.ts`. The local Nightwatch OMP installation keeps
the agent at `../agents/archivist.md` and loads the tool
from its `.omp/config.yml`. Address `archivist` from an OMP session started in
`harness/` to ask questions about the archive, tidy records, or find
supported links among gadgets and near-miss chains. Research agents use the
same Casefile tool to record their own evidence; writes are attributed to the
invoking agent and model.
