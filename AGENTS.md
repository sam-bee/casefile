# Nightwatch casefile

- This is the archive for authorized security research. The hosted service is durable; Docker Compose is a development environment. Work only against targets the operator has authorized; the operator handles disclosure.
- Keep the application API POST-only. GET and HEAD serve the SPA and its static assets.
- PostgreSQL is the canonical data store. Elasticsearch is a rebuildable search index.
- For Docker Compose development, keep PostgreSQL and Elasticsearch data in the local bind directories named by `CASEFILE_DATA_DIR`, outside Git and synced workspaces. Never commit `.env` or credentials.
- Build and generate JavaScript assets only inside Docker, never with host-side npm or bundler commands.
- In Docker Compose development, publish only the web port on `127.0.0.1`. Run the Go application with the UID/GID configured in Compose.

## Version control

- Work on `master` by default. Do not create temporary branches; use another branch only when the operator explicitly requests one.
- Commit completed work in small, coherent changesets. Prefer concise messages of the form `To/Because/For [reason], [specific change]`.
- Keep history linear with rebase pulls and fast-forward merges. When rewriting a pushed branch, use `--force-with-lease`, never an unconditional force push.
- Push completed source changes to the configured remotes as directed by the operator. Keep credentials and research secrets out of Git.
