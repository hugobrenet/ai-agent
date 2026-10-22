// Runs the official AG-UI client against the runs route the Go test serves:
// a run that ends on the interrupt of a confirmation, then its resume. The
// client verifies the order and the shape of the events, and rebuilds the
// messages from them.
import { HttpAgent } from "@ag-ui/client";

const fail = (message) => {
  console.error("conformance: " + message);
  process.exit(1);
};
const agent = new HttpAgent({
  url: process.argv[2] + "/v1/om3/conversations/conversation-id/runs",
  headers: { Authorization: "Bearer token", "X-Test-Backend": "cluster-id" },
  threadId: "conversation-id",
  initialMessages: [{ id: "m1", role: "user", content: "stop web" }],
});
const events = [];
const subscriber = { onEvent: ({ event }) => { events.push(event); } };

await agent.runAgent({ runId: "run-1" }, subscriber);
const started = events.find((event) => event.type === "RUN_STARTED");
const finished = events.at(-1);
if (started?.protocolVersion !== "1.0") fail("RUN_STARTED declares no protocolVersion 1.0");
const interrupt = finished?.outcome?.interrupts?.[0];
if (finished?.type !== "RUN_FINISHED" || finished.outcome.type !== "interrupt" || interrupt?.reason !== "tool_call" || interrupt.toolCallId !== "call-7") {
  fail("the first run does not end on the tool_call interrupt: " + JSON.stringify(finished));
}
if (finished.usage?.[0]?.totalTokens !== 15) fail("the interrupted run reports no usage: " + JSON.stringify(finished.usage));

events.length = 0;
const resumed = await agent.runAgent({ runId: "run-2", resume: [{ interruptId: interrupt.id, status: "resolved", payload: { approved: true } }] }, subscriber);
if (resumed.result?.turnId !== "turn-id") fail("the resumed run gives no turnId: " + JSON.stringify(resumed.result));
if (events.some((event) => event.type === "TOOL_CALL_START" && event.toolCallId === "call-7")) fail("the resumed run starts the confirmed call again");

const shape = agent.messages.map((message) => message.role + (message.toolCalls ? "[" + message.toolCalls.map((call) => call.function.name).join("+") + "]" : "") + (message.toolCallId ? "(" + message.toolCallId + ")" : ""));
const want = "user,assistant[get_object],tool(call-1),assistant[stop_object],tool(call-7),assistant";
if (shape.join(",") !== want) fail("messages " + shape.join(",") + ", want " + want);
console.log("conformance: ok");
