# Juice Shop fixtures

Captured from **`bkimminich/juice-shop:v20.3.0`** on 2026-11-18, the version
pinned in `deploy/docker/Dockerfile.node`. The client tests (`../client_test.go`)
serve these files from an `httptest` server, so the agent is tested against
what that version really says, not against what the design assumed it says.

**Upgrading Juice Shop means capturing these again** with the commands below,
and running `go test ./...`. A failing test then shows what changed.

| File | Produced by |
|------|-------------|
| `challenges-fresh.json` | `GET /api/Challenges` on a fresh start: 116 challenges, 18 with `disabledEnv: "Docker"` |
| `challenges-solved.json` | the same after `GET /metrics` and `GET /ftp/acquisitions.md`, which solve `exposedMetricsChallenge` and `directoryListingChallenge` |
| `continue-code.json` | `GET /rest/continue-code` in that state |
| `continue-code-empty.json` | `GET /rest/continue-code-findIt` with no coding challenge done: `{}`, no `continueCode` key |
| `apply-ok.json` | `PUT /rest/continue-code/apply/<code>` after a restart: 200 |
| `apply-invalid.txt` | `PUT /rest/continue-code/apply/bogus`: **404**, plain text |
| `webhook.json` | the body Juice Shop POSTs to `SOLUTIONS_WEBHOOK` on a solve |
| `application-version.json` | `GET /rest/admin/application-version` |

```sh
docker run -d --name js -p 127.0.0.1:13000:3000 bkimminich/juice-shop:vX.Y.Z
J=http://127.0.0.1:13000
curl -s $J/api/Challenges > challenges-fresh.json
curl -s -o /dev/null $J/metrics; curl -s -o /dev/null $J/ftp/acquisitions.md
curl -s $J/api/Challenges > challenges-solved.json
curl -s $J/rest/continue-code > continue-code.json
curl -s $J/rest/continue-code-findIt > continue-code-empty.json
CODE=$(python3 -c 'import json;print(json.load(open("continue-code.json"))["continueCode"])')
docker restart js            # Juice Shop now has 0 solved
curl -s -X PUT $J/rest/continue-code/apply/$CODE > apply-ok.json
curl -s -X PUT $J/rest/continue-code/apply/bogus > apply-invalid.txt
curl -s $J/rest/admin/application-version > application-version.json
# webhook.json: start with -e SOLUTIONS_WEBHOOK=<a listener> and keep one body
```

What these captures settled, and what the design had to be corrected for:

- `disabledEnv` is a string (`"Docker"`) or `null`, not a list.
- `findIt`/`fixIt` answer `{}` when there is nothing to encode, so an empty
  code is normal and must not be applied.
- Applying a code after a restart restores the solves **without** sending the
  webhook (Juice Shop logs them as "Restored"), so a restore never looks like a
  burst of new solves.
- Every challenge's `createdAt` is the moment Juice Shop started (its database
  is recreated on start). The agent uses it as a **boot id**: a different value
  means Juice Shop restarted, even when nothing had been solved yet.
