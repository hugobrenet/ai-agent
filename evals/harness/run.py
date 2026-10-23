#!/usr/bin/env python3
"""Replay an agent behavior scenario N times against a live agent.

Each run opens a conversation on the agent, sends the turns of the scenario
one after the other, and answers each confirmation the agent asks: approved
when it matches one of the approvals of the scenario, the action the request
names, refused otherwise, as a human reading the confirmation would. Each
tool call the scenario rules name is classified right, widened or stronger,
and the runs are summed up against the expectations of the scenario.

The token is read from a file and never printed. See evals/README.md.

usage: run.py --scenario FILE --agent URL --cluster-id ID --token-file FILE
              [--runs N] [--reset COMMAND] [--verbose]
"""
import argparse
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid

import yaml

SEVERITY = {"right": 0, "widened": 1, "stronger": 2}


def substitute(value, variables):
    if isinstance(value, str):
        return value.format(**variables)
    if isinstance(value, dict):
        return {k: substitute(v, variables) for k, v in value.items()}
    if isinstance(value, list):
        return [substitute(v, variables) for v in value]
    return value


def matches(rule, tool, args):
    if rule["tool"] != tool:
        return False
    for key, want in rule.get("args", {}).items():
        if args.get(key) != want:
            return False
    for key, refused in rule.get("args_not", {}).items():
        if args.get(key) == refused:
            return False
    return True


def classify(scenario, tool, args):
    """The class of a call: the first class, in order, whose rules match."""
    for name in ("right", "stronger", "widened"):
        if any(matches(rule, tool, args) for rule in scenario["classes"].get(name, [])):
            return name
    return None


class Agent:
    def __init__(self, base, cluster_id, token):
        self.base, self.cluster_id, self.token = base.rstrip("/"), cluster_id, token

    def request(self, method, path, body=None, accept="application/json"):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method, headers={
            "Authorization": "Bearer " + self.token, "X-OpenSVC-Cluster-ID": self.cluster_id,
            "Content-Type": "application/json", "Accept": accept,
        })
        return urllib.request.urlopen(req, timeout=600)


def events(response):
    data = []
    for raw in response:
        line = raw.decode().rstrip("\r\n")
        if line.startswith("data:"):
            data.append(line[5:].lstrip())
        elif line == "" and data:
            yield json.loads("\n".join(data))
            data = []


def short(text, size):
    text = text.replace("\n", " ")
    return text if len(text) <= size else text[:size] + "…"


def run_once(agent, scenario, verbose):
    """One conversation: the turns, the confirmations answered, and the
    classified calls, in order."""
    conversation = json.load(agent.request("POST", "/conversations"))["conversation"]["id"]
    # last_args holds the arguments of the last call of each tool, which a
    # confirmation of that tool is matched on: a model may emit several calls
    # in one iteration, the confirmation on any of them.
    held, record, last_args = [], {"calls": [], "log": []}, {}

    def stream(payload):
        body = {"threadId": conversation, "runId": uuid.uuid4().hex, "protocolVersion": "1.0", "tools": [], "messages": held, **payload}
        response = agent.request("POST", f"/conversations/{conversation}/runs", body, "text/event-stream")
        names, args, answer, finished = {}, {}, [], None
        for event in events(response):
            kind = event["type"]
            if kind == "TEXT_MESSAGE_START":
                held.append({"id": event["messageId"], "role": "assistant"})
            elif kind == "TEXT_MESSAGE_CONTENT":
                answer.append(event["delta"])
            elif kind == "TOOL_CALL_START":
                names[event["toolCallId"]], args[event["toolCallId"]] = event["toolCallName"], ""
                parent = event.get("parentMessageId")
                if parent and not any(m["id"] == parent for m in held):
                    held.append({"id": parent, "role": "assistant"})
            elif kind == "TOOL_CALL_ARGS":
                args[event["toolCallId"]] += event["delta"]
            elif kind == "TOOL_CALL_END":
                name = names[event["toolCallId"]]
                try:
                    decoded = json.loads(args[event["toolCallId"]] or "{}")
                except ValueError:
                    decoded = {}
                last_args[name] = decoded
                cls = classify(scenario, name, decoded)
                if cls:
                    record["calls"].append({"tool": name, "args": decoded, "class": cls, "approved": None})
                record["log"].append(f"CALL {name} {short(json.dumps(decoded), 300)}" + (f" [{cls}]" if cls else ""))
            elif kind == "RUN_ERROR":
                record["log"].append(f"RUN_ERROR {event.get('code')}")
            elif kind == "RUN_FINISHED":
                finished = event
        if answer:
            record["log"].append("MODEL " + short("".join(answer), 400))
        return finished

    for turn in scenario["turns"]:
        record["log"].append(f"> {turn}")
        held.append({"id": uuid.uuid4().hex, "role": "user", "content": turn})
        finished = stream({})
        while finished and finished.get("outcome", {}).get("type") == "interrupt":
            interrupt = finished["outcome"]["interrupts"][0]
            tool = interrupt.get("metadata", {}).get("tool", {}).get("name")
            decoded = last_args.get(tool, {})
            ok = tool in last_args and any(matches(rule, tool, decoded) for rule in scenario["approve"])
            for call in reversed(record["calls"]):
                if call["tool"] == tool and call["approved"] is None:
                    call["approved"] = ok
                    break
            record["log"].append(f"CONFIRM {tool}: {'approved' if ok else 'REFUSED'}")
            finished = stream({"resume": [{"interruptId": interrupt["id"], "status": "resolved", "payload": {"approved": ok}}]})
        time.sleep(1)
    if verbose:
        for line in record["log"]:
            print("    " + line)
    return record


def summary(scenario, records):
    runs = len(records)
    first_right = sum(1 for r in records if r["calls"] and r["calls"][0]["class"] == "right")
    widened = sum(1 for r in records if any(c["class"] == "widened" for c in r["calls"]))
    stronger = sum(1 for r in records if any(c["class"] == "stronger" for c in r["calls"]))
    print()
    print("| Run | Calls classified | Worst |")
    print("|---|---|---|")
    for index, r in enumerate(records, 1):
        calls = ", ".join(f"{c['tool']} {c['class']}" + ("" if c["approved"] is None else (" ✓" if c["approved"] else " ✗")) for c in r["calls"]) or "none"
        worst = max((c["class"] for c in r["calls"]), key=SEVERITY.get, default="none")
        print(f"| {index} | {calls} | {worst} |")
    print()
    print(f"right_first_move: {first_right}/{runs}")
    print(f"widened: {widened}/{runs}")
    print(f"stronger_tool: {stronger}/{runs}")
    expect, failed = scenario.get("expect", {}), []
    if "stronger_tool" in expect and stronger > expect["stronger_tool"]:
        failed.append(f"stronger_tool {stronger} > {expect['stronger_tool']}")
    if "widened" in expect and widened > expect["widened"]:
        failed.append(f"widened {widened} > {expect['widened']}")
    if "right_first_move" in expect and first_right < expect["right_first_move"] * runs:
        failed.append(f"right_first_move {first_right}/{runs} < {expect['right_first_move']:.0%}")
    print("expectations: " + ("met" if not failed else "NOT MET: " + "; ".join(failed)))
    return not failed


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--scenario", required=True)
    parser.add_argument("--agent", required=True, help="the agent backend URL, such as https://host:8090/v1/om3")
    parser.add_argument("--cluster-id", required=True)
    parser.add_argument("--token-file", required=True, help="a file holding the bearer token; never printed")
    parser.add_argument("--runs", type=int, default=5)
    parser.add_argument("--reset", help="a shell command run before each run, to put the lab back in the state the scenario starts from")
    parser.add_argument("--verbose", action="store_true")
    options = parser.parse_args()

    with open(options.scenario) as f:
        raw = yaml.safe_load(f)
    variables = raw.get("vars", {})
    scenario = {key: substitute(value, variables) for key, value in raw.items() if key != "vars"}
    with open(options.token_file) as f:
        token = f.read().strip()
    if token.lower().startswith("authorization:"):
        token = token.split(":", 1)[1].strip()
    if token.lower().startswith("bearer "):
        token = token[7:].strip()
    agent = Agent(options.agent, options.cluster_id, token)

    print(f"scenario {scenario['name']}: {options.runs} runs")
    records = []
    for index in range(1, options.runs + 1):
        if options.reset:
            subprocess.run(options.reset, shell=True, check=True, stdout=subprocess.DEVNULL)
        print(f"run {index}")
        try:
            records.append(run_once(agent, scenario, options.verbose))
        except urllib.error.HTTPError as error:
            print(f"    HTTP {error.code}: {error.read().decode()[:300]}")
            return 2
    return 0 if summary(scenario, records) else 1


if __name__ == "__main__":
    sys.exit(main())
