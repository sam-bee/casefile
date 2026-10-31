import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const envFile = fileURLToPath(new URL("../../../harness/.env", import.meta.url));
const casefileSkillFile = fileURLToPath(new URL("../../.agents/skills/casefile/SKILL.md", import.meta.url));
const archivePolicyFile = fileURLToPath(new URL("../../docs/archive-policy.md", import.meta.url));
const archivistReadPaths = new Set([
	"../casefile/.agents/skills/casefile/SKILL.md",
	"../casefile/docs/archive-policy.md",
	"casefile/.agents/skills/casefile/SKILL.md",
	"casefile/docs/archive-policy.md",
	casefileSkillFile,
	archivePolicyFile,
]);
const operations = [
	"casenotes/search", "casenotes/get", "casenotes/create", "casenotes/update",
	"comments/create", "links/create", "tags/list", "tags/cloud", "graph/get",
] as const;
const writes = new Set(["casenotes/create", "casenotes/update", "comments/create", "links/create"]);

function actorFor(ctx: any): string {
	const name = ctx?.agent?.kind === "main" ? "nightwatch-lead" : ctx?.agent?.name;
	const provider = ctx?.model?.provider;
	const model = ctx?.model?.id;
	if (typeof name !== "string" || !name || typeof provider !== "string" || !provider || typeof model !== "string" || !model) {
		throw new Error("Casefile writes require the invoking agent and model identity");
	}
	return `${name} (${provider}/${model})`;
}

function configuration() {
	const values: Record<string, string> = {};
	for (const line of readFileSync(envFile, "utf8").split(/\r?\n/)) {
		const match = line.match(/^\s*(CASEFILE_PROJECT|CASEFILE_API_URL)=(.*)\s*$/);
		if (match) values[match[1]] = match[2].trim().replace(/^["']|["']$/g, "");
	}
	const project = values.CASEFILE_PROJECT;
	if (!project) throw new Error("CASEFILE_PROJECT is missing from the local Nightwatch env file");
	const url = new URL(values.CASEFILE_API_URL || "http://127.0.0.1:8090/api");
	if (url.protocol !== "http:" || !["127.0.0.1", "localhost"].includes(url.hostname) ||
		url.username || url.password || url.search || url.hash || url.pathname.replace(/\/$/, "") !== "/api") {
		throw new Error("Casefile tool requires a loopback HTTP /api URL");
	}
	return { project, base: url.href.replace(/\/$/, "") };
}

export default function (pi: any) {
	pi.registerTool({
		name: "casefile",
		label: "Casefile",
		description: "Before constructing any payload, read `../casefile/.agents/skills/casefile/SKILL.md` for the selected operation's exact fields and constraints. Do not invent JSON keys. Supply one listed operation and a JSON object string. The tool injects the active project and the invoking agent's identity for writes.",
		parameters: pi.zod.object({
			operation: pi.zod.enum(operations),
			payload: pi.zod.string().describe("JSON object for the selected Casefile operation; use {} when no fields are needed"),
		}),
		approval: "write",
		loadMode: "essential",
		strict: true,
		async execute(_id: string, params: { operation: typeof operations[number]; payload: string }, signal: AbortSignal, _onUpdate: unknown, ctx: any) {
			const { project, base } = configuration();
			let payload: Record<string, unknown>;
			try { payload = JSON.parse(params.payload); }
			catch { throw new Error("Casefile payload must be a JSON object"); }
			if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
				throw new Error("Casefile payload must be a JSON object");
			}
			delete payload.project_id;
			payload.project = project;
			if (writes.has(params.operation)) {
				const actor = actorFor(ctx);
				delete payload.author;
				delete payload.actor;
				if (params.operation === "casenotes/update") payload.actor = actor;
				else payload.author = actor;
			}
			const response = await fetch(`${base}/${params.operation}`, {
				method: "POST", headers: { "Content-Type": "application/json" },
				body: JSON.stringify(payload), signal,
			});
			const text = await response.text();
			if (!response.ok) throw new Error(`Casefile ${params.operation}: HTTP ${response.status}: ${text.slice(0, 1000)}`);
			return { content: [{ type: "text", text }] };
		},
	});

	pi.on("tool_call", (event: any, ctx: any) => {
		if (ctx.agent?.kind !== "sub" || ctx.agent.name !== "archivist") return;
		if (event.toolName === "casefile" || event.toolName === "yield") return;
		const target = typeof event.input?.path === "string" ? event.input.path : "";
		if (event.toolName === "read" && (target.startsWith("history://") || archivistReadPaths.has(target))) return;
		if (event.toolName === "write" && /^agent:\/\/[A-Za-z0-9._-]+$/.test(target)) return;
		return { block: true, reason: "The archivist may use Casefile, read agent history, and message peers only" };
	});
}
