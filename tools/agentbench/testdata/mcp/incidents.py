#!/usr/bin/env python3
"""A stdio MCP server for the benchmark's MCP tasks: a fixed catalogue of
incidents, seeded, so every run and every check sees the same data.

search_incidents pages its matches, 50 a page, each with a long body, so a
page is about 50 KB: what a real tracker's search returns. Status and the
owner come only from get_incident. `incidents.py --expect <task>` prints a
task's expected answer, which the task's check/ holds.

Standard library only; JSON-RPC 2.0, one message per line.
"""
import json
import random
import sys

SERVICES = [
    ("payments", "maya.ortiz@example.com"), ("checkout", "li.wei@example.com"), ("search", "omar.haddad@example.com"),
    ("auth", "sofia.rossi@example.com"), ("inventory", "kwame.mensah@example.com"), ("shipping", "anna.kowalska@example.com"),
    ("notifications", "diego.santos@example.com"), ("billing", "yuki.tanaka@example.com"), ("catalog", "fatima.zahra@example.com"),
    ("reviews", "lars.nilsen@example.com"), ("gateway", "priya.nair@example.com"), ("reports", "tom.brennan@example.com"),
]
PROBLEMS = ["timeout", "connection pool", "memory leak", "disk full", "certificate expiry", "deadlock", "rate limit",
            "cache stampede", "DNS failure", "schema migration", "queue backlog", "clock skew"]
WORDS = ("the service degraded while upstream retries piled up and the on call engineer rolled back the deploy "
         "after dashboards showed elevated latency errors saturation in the primary region with partial recovery "
         "once traffic shifted to the secondary cluster and the follow up ticket tracks the remaining cleanup").split()
PAGE = 50


def build():
    rnd = random.Random(20261012)
    out = []
    for n in range(1, 1201):
        service = rnd.choice(SERVICES)[0]
        problems = []
        for _ in range(rnd.choice([1, 1, 2])):
            p = rnd.choices(PROBLEMS, [4] + [1] * (len(PROBLEMS) - 1))[0]  # timeouts are common
            if p not in problems:
                problems.append(p)
        month = rnd.choice([7, 8, 9])
        day = rnd.randint(1, 28)
        body = " ".join(rnd.choice(WORDS) for _ in range(150))
        cut = rnd.randint(20, 120)
        words = body.split()
        words.insert(cut, "(" + " and ".join(problems) + ")")
        out.append({
            "id": "INC-%04d" % n,
            "title": "%s: %s" % (service, problems[0]),
            "service": service,
            "severity": rnd.choices(["SEV1", "SEV2", "SEV3", "SEV4"], [1, 2, 4, 4])[0],
            "created": "2026-%02d-%02d" % (month, day),
            "body": " ".join(words),
            "status": rnd.choices(["open", "mitigated", "resolved"], [2, 2, 5])[0],
            "timeline": ["2026-%02d-%02d %02d:%02d %s" % (month, day, h, rnd.randint(0, 59), rnd.choice(WORDS)) for h in range(8, 18)],
        })
    return out


INCIDENTS = build()
OWNERS = dict(SERVICES)


def summary(i):
    return {k: i[k] for k in ("id", "title", "service", "severity", "created", "body")}


def search(query, page):
    q = (query or "").lower()
    hits = [i for i in INCIDENTS if q in i["title"].lower() or q in i["body"].lower()]
    pages = max(1, (len(hits) + PAGE - 1) // PAGE)
    page = max(1, int(page or 1))
    chunk = hits[(page - 1) * PAGE: page * PAGE]
    return {"query": query, "page": page, "pages": pages, "total": len(hits), "results": [summary(i) for i in chunk]}


def get(id_):
    for i in INCIDENTS:
        if i["id"] == id_:
            return dict(i, owner=OWNERS[i["service"]])
    raise KeyError("no incident " + str(id_))


TOOLS = [
    {"name": "search_incidents", "description": "Search incidents by text in the title or body (case-insensitive). Results are paged, 50 a page; each has the id, title, service, severity, created date, and body. Status and owner come from get_incident.",
     "inputSchema": {"type": "object", "properties": {"query": {"type": "string", "description": "Text to find; empty matches every incident."},
                                                     "page": {"type": "integer", "description": "The page, from 1."}}, "required": ["query"]}},
    {"name": "get_incident", "description": "One incident in full: its status, owner, and timeline besides what search returns.",
     "inputSchema": {"type": "object", "properties": {"id": {"type": "string", "description": "Such as INC-0042."}}, "required": ["id"]}},
    {"name": "list_services", "description": "Every service with its owner.",
     "inputSchema": {"type": "object", "properties": {}}},
]


def call(name, args):
    if name == "search_incidents":
        return search(args.get("query", ""), args.get("page", 1))
    if name == "get_incident":
        return get(args.get("id"))
    if name == "list_services":
        return [{"name": s, "owner": o} for s, o in SERVICES]
    raise KeyError("no tool " + name)


def expect(task):
    if task == "mcp-open-sev1":
        ids = [i["id"] for i in INCIDENTS if "connection pool" in (i["title"] + " " + i["body"]).lower() and i["severity"] in ("SEV1", "SEV2") and i["status"] != "resolved"]
        return "\n".join(sorted(ids))
    if task == "mcp-timeout-summary":
        counts = {}
        for i in INCIDENTS:
            if "timeout" in (i["title"] + " " + i["body"]).lower():
                counts[i["service"]] = counts.get(i["service"], 0) + 1
        return json.dumps(counts, sort_keys=True)
    if task == "mcp-escalation-owner":
        counts = {}
        for i in INCIDENTS:
            if "memory leak" in (i["title"] + " " + i["body"]).lower() and i["severity"] in ("SEV1", "SEV2") and i["status"] == "open":
                counts[i["service"]] = counts.get(i["service"], 0) + 1
        top = max(sorted(counts), key=lambda s: counts[s])
        if sorted(counts.values())[-2:] == [counts[top]] * 2:
            raise SystemExit("a tie for the most open incidents")
        return json.dumps({"service": top, "owner": OWNERS[top], "count": counts[top], "counts": counts}, sort_keys=True)
    raise SystemExit("unknown task " + task)


def main():
    if len(sys.argv) == 3 and sys.argv[1] == "--expect":
        print(expect(sys.argv[2]))
        return
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        msg = json.loads(line)
        if "id" not in msg:
            continue  # a notification
        method, mid = msg.get("method"), msg["id"]
        try:
            if method == "initialize":
                result = {"protocolVersion": msg.get("params", {}).get("protocolVersion", "2025-06-18"),
                          "capabilities": {"tools": {}}, "serverInfo": {"name": "incidents", "version": "1"}}
            elif method == "tools/list":
                result = {"tools": TOOLS}
            elif method == "tools/call":
                p = msg.get("params", {})
                try:
                    text = json.dumps(call(p.get("name"), p.get("arguments") or {}))
                    result = {"content": [{"type": "text", "text": text}], "isError": False}
                except (KeyError, ValueError) as e:
                    result = {"content": [{"type": "text", "text": str(e)}], "isError": True}
            elif method == "ping":
                result = {}
            else:
                sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "method not found"}}) + "\n")
                sys.stdout.flush()
                continue
        except Exception as e:  # noqa: BLE001 - the client must always get an answer
            result = {"content": [{"type": "text", "text": str(e)}], "isError": True}
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": mid, "result": result}) + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
