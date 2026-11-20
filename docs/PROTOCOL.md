# Protocol v2: ctf-node ↔ ctf-central

The three requests between a student's VM and the central server, as built
in 1.1. The Go types are in [`internal/protocol`](../internal/protocol),
the one place the format is defined; the examples below are the ones its
tests round-trip.

All three go **from the VM** to the central server and are answered at once,
so a VM needs nothing but outbound HTTPS. Bodies are JSON, at most 256 KiB.
Times are RFC 3339, UTC. Every error is `{"error": "…"}` with a status from
the tables below; the message is safe to show and never repeats a token, a
code or a name.

## POST /api/v2/join

No token: the join code admits the VM.

```json
{"protocol":2,"name":"Zoë","code":"4ffpq7tq9f","joined_at":"2026-11-19T09:00:00Z",
 "node":{"uid":"0123456789abcdef0123456789abcdef","version":"v1.1.0","juiceshop":"20.3.0"}}
```

| Field | |
|-------|-|
| `name` | the display name, 2–40 letters, digits, spaces and `. - ' _`; normalised by the server |
| `code` | 10 characters from `abcdefghjkmnpqrstuvwxyz23456789`; upper case, spaces and dashes are accepted (`4FFPQ-7TQ9F`) |
| `joined_at` | optional: when the student pressed join. A join that waited in the VM's outbox is older than its arrival |
| `node.uid` | 32 hex, random, made at the VM's first join — never at boot |

| Answer | When |
|--------|------|
| **201** | joined: see below. The token is in this answer **once**; the server keeps only its SHA-256 |
| 400 | invalid name or code |
| 403 | unknown join code (it also counts against the address: see limits) |
| 409 | the game is a draft, closed, or past its end |
| 429 | 20 wrong codes from this address in the last minute, or 300 joins of this game in a minute |

```json
{"participant":41,"token":"ctfp_00112233…eeff","name":"Zoë",
 "game":{"id":7,"name":"Class 2A","slug":"class-2a","state":"running","ends_at":"2026-11-19T11:00:00Z"},
 "board":"https://ctf.example.org/g/class-2a/"}
```

The token is `ctfp_` and 64 hex characters. The VM stores it in `node.db`
and sends it with everything after; the join code is not kept.

## POST /api/v2/report

`Authorization: Bearer ctfp_…`. A **complete snapshot**, not a delta: a lost
or repeated report is harmless.

```json
{"protocol":2,"sent_at":"2026-11-19T09:30:00Z","boot":"2026-11-19T08:59:12.431Z",
 "node":{"uid":"0123456789abcdef0123456789abcdef","version":"v1.1.0","juiceshop":"20.3.0"},
 "juiceshop_reachable":true,"restore_failed":false,
 "solves":[{"key":"scoreBoardChallenge","solved_at":"2026-11-19T09:05:00Z","cheat":0.1},
           {"key":"directoryListingChallenge","solved_at":"2026-11-19T09:20:00Z"}],
 "commands_applied":["c-1a2b3c"]}
```

| Field | |
|-------|-|
| `boot` | Juice Shop's start time: a new value after every restart, never an old one again |
| `solves` | every challenge solved since the join, with the first time the VM saw it; at most 500 |
| `cheat` | Juice Shop's own cheat score from its webhook, 0–1, when it sent one |
| `commands_applied` | ids of commands carried out (none of today's commands needs it: `forget` is confirmed by the leave) |

The server, per solve it has not seen for this participant:

- ignores a key the catalogue does not know (the catalogue is the server's,
  embedded from the pinned Juice Shop; a report says nothing about points);
- clamps a `solved_at` more than 5 minutes past `sent_at` to the time of receipt;
- stores the **time of receipt**, which decides ties and first blood;
- stores it as *not counted* when the game no longer accepts solves.

A solve is never removed, and the first time is kept.

| Answer | When |
|--------|------|
| **200** | see below |
| 400 | invalid report (the message says what) |
| 401 | unknown token: the VM shows the join form again and keeps its solves |
| 413 | body too large |

```json
{"standing":{"rank":2,"of":18,"score":1350},
 "game":{"state":"running","ends_at":"2026-11-19T11:00:00Z"},
 "commands":[{"id":"c-1a2b3c","type":"forget"}]}
```

`standing` is `null` when the participant is not on the board. `game.state`
is `closed` once the game is past its end, even before the operator closes
it; the VM then stops recording new solves. A participant the operator removed
— or whose game was cleared or deleted — gets `"state":"left"` and a `forget`
command, until the VM confirms it.

### Commands

| Type | The VM does |
|------|-------------|
| `forget` | exactly what the student's *Forget* button does: leave (reason `command`), wipe `node.db`, restart Juice Shop empty |

Commands are per **participant** (keyed by the token's hash, so a VM whose
participant row was deleted can still be told). Unknown types are ignored, so
a newer server can add commands without breaking older VMs.

## POST /api/v2/leave

`Authorization: Bearer ctfp_…`

```json
{"protocol":2,"reason":"forget"}
```

`reason` is `forget` (the student pressed *Forget*) or `command` (carrying
out a `forget`). The answer is **204**; the participant is marked *left*, off
the board, its solves kept for the operator, and the token stops working. A
leave for a removed participant's token confirms the waiting `forget`. 401
for a token the server does not know.

## The 1.0 protocol

`POST /api/v1/report` answers **410 Gone** with a message, and writes an
event naming the address and the 1.0 instance id, so the operator sees which
VM still runs 1.0.

## Limits

Never per address alone: a school puts a whole room behind one.

| | |
|-|-|
| wrong join codes | 20 per address per minute; the 21st is 429 **before** the code is checked |
| joins of one game | 300 per minute |
| solves per report | 500 |
| body | 256 KiB |
