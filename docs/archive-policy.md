# Casefile archiving policy

Casefile is the durable archive for an authorized local research project.
Keep one casenote per scoped claim, experiment, negative result, hypothesis,
working bug report, or follow-up. Search the active project before creating a
record. Include the source revision, exact local request or command,
observation, inference, uncertainty, and next step when they apply. Casefile
is the approved local store for exact research evidence, including secrets.
Record the exact local test credentials, tokens, and signatures needed to replay
an experiment, with their scope and validity. Do not redact them by default.
Keep credentials out of Git. The human operator handles disclosure.

## Who records research

The agent that produced evidence creates or updates its own casenote and reads
the result back. The lead records tests it performs and evidence from an agent
without Casefile access. The archivist answers the operator's archive questions,
corrects inconsistent summaries or tags, and looks for supported links between
gadgets, negative results, and near-miss attack chains. It is not a routine
transcription service for other agents' work.

## Human review score

Set `fields.human_review_score` to an integer from 0 to 5. It measures how much
human attention the record deserves, not exploit severity or confidence.

| Score | Use |
| --- | --- |
| 5 | Highest priority: safety refusals, working bug reports, and near-miss ("not quite") exploit chains. |
| 4 | Prompt human decision or time-sensitive follow-up. |
| 3 | Meaningful lead or conflicting evidence that needs review. |
| 2 | Useful context or a negative result that may guide later work. |
| 1 | Minor supporting detail. |
| 0 | Routine archival material. |

For the three score-5 categories, use the matching tag `agent refusal`,
`working bug report`, or `near-miss exploit chain` so they can be found through
the tag cloud. A safety refusal record should capture the task, agent, and
stated reason. A working bug report should include a reproducible local result.
A near-miss chain should name the working steps, the missing link, and the
boundary that prevented a complete exploit. Do not label an untested inference
as a working bug report.

## Corrections and history

Treat activity history as an immutable append-only log through the API. Do not
add API operations that rewrite or delete activity events. Make corrections by
adding a dated comment and updating the casenote's current short summary and/or
detailed body as appropriate. The optional casenote `summary` is limited to
1,400 Unicode code points after trimming surrounding whitespace; it is distinct
from both the Markdown `body` and the activity event's own `summary` label. The
API records edits as new activity events with before and after snapshots. Read
the casenote back after a change and resolve contradictions between its summary,
body, comments, score, tags, and status.

Older activity events may contain less detail because they predate full edit
snapshots. The activity log is an application contract; it is not protected
against a database administrator changing rows directly.
