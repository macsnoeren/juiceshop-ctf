# Running a lab

For the person who runs a Juice Shop CTF with a class. You need: the central
server (the image `macsnoeren/juiceshop-ctf-central`, admin at
`<server>/admin/` with its own login; behind a reverse proxy, ideally with
an IP allow-list in front of admin as well), and the CTF VM
(`juiceshop-ctf-<version>-virtualbox.ova`, or `-vmware.zip` for VMware), from
a release or built with `CENTRAL_URL=<your server> make vm`. How it is made is in
[deploy/vm/README.md](../deploy/vm/README.md); the students'
sheet is [docs/STUDENT.md](STUDENT.md).

```
 each student's laptop (VirtualBox/VMware)                              a server
 Kali ── host-only ──► CTF VM (Juice Shop + ctf-node) ── NAT ──►  central server: games, boards, admin
```

## 0. Admin accounts

Admin has accounts of its own. All of them can do everything, including
managing the others.

- **The very first time** admin is opened on a new central server, it shows
  the **setup page**: make the first account there (a username and a
  password of at least 10 characters). It asks for the **setup code** the
  server printed to its log when it started:
  `docker logs <container> | grep setup_code`. Without that code nobody can
  make the first account, so a fresh server on the internet is safe until
  you get to it. A restart makes a new code; wrong codes count against the
  login limit below.
- **Logging in**: *Keep me logged in* (on by default) keeps this browser
  logged in for 30 days after its last use. Without it, the login ends with
  the browser, or after 12 hours of not using it.
- **Users** (in the bar): add a colleague with a first password and tell it
  to them in person; they change it under *Your account*. *New password…*
  sets a password for someone who lost theirs, and logs them out
  everywhere. *Delete…* (type their username) removes an account. You
  cannot delete your own.
- **A lost laptop**: *Log out everywhere else* under *Your account*, or
  have a colleague set you a new password.
- After 10 wrong passwords for one username, or 20 from one address, logins
  pause for 15 minutes. Every login, failed login and account change is in
  **Events** as kind `auth`, and every admin action names who did it.
  (Anyone can pause the login of a username this way; the accounts are
  still safe, and it ends 15 minutes after the last try.)

### The central server on the internet

- **HTTPS**, from the reverse proxy in front of it. Set HSTS there too
  (`Strict-Transport-Security: max-age=31536000`). The VMs refuse a central
  server on the internet over plain `http://`: anyone on the way would read
  the join code and every token.
- **The proxy sets the client's address.** It must OVERWRITE `X-Real-IP` and
  `X-Forwarded-Proto` (nginx: `proxy_set_header X-Real-IP $remote_addr;` and
  `proxy_set_header X-Forwarded-Proto $scheme;`). The server believes them
  only from a peer in `CTF_TRUSTED_PROXIES` (default: loopback and the
  private ranges, which covers a proxy on the same host or Docker network).
  The limits per address and the addresses in the feed depend on it.
- **No proxy?** Then set `CTF_TRUSTED_PROXIES=none`, or anyone could choose
  their address with every request.
- **An IP allow-list** in front of `<server>/admin/` is a good second lock.

[docs/THREAT-MODEL.md](THREAT-MODEL.md) has the whole picture: what is
attacked, how each attack is met, and what is left to you.

## 1. Before the lesson

1. **Make a game.** Admin → *New game*: a name ("Class 2A — 19 Nov"),
   optionally a start and an end (after the end no solve counts and the board
   shows the final result), the points per difficulty (1.0's 100 / 250 / 450 /
   700 / 1000 / 1350 by default), and whether the board is hidden or shows
   initials instead of names. The *Find it* and *Fix it* of a coding
   challenge score half the challenge's points each; they are not solves,
   so they count in no "solved" and no first blood.
2. **The join code** is on the game page, in admin only. *New join code*
   replaces it (students who joined keep playing; the old code stops
   admitting). A game made with an older central server shows no code until you
   make a new one: its code was kept only as a hash.
3. The game is a **draft**: nobody can join yet. Press *Open for joining*
   (or *Start*) when the class may come in. Open and running both admit
   students and count solves.
4. Hand out the VM image and the student sheet.

## 2. In the lesson

- Put **Beamer with join code** (game page in admin) on the beamer: the
  board full screen with the join code above it, so one screen is enough. The
  code shows while students can join and disappears, within ten seconds, when
  you close joining or the game ends; it comes back when you open joining
  again. It needs your login, so open it on your own laptop. The public board
  (`…/juiceshop-ctf/g/<slug>/`, full screen at `…/g/<slug>/beamer`) never
  shows the code. Both have a countdown when the game has an end, the ticker
  at the bottom, and the final result when it closes.
- **Close joining** (game page, *State*) once the class is in: nobody new can
  join, while everyone in the game plays on and their solves count. *Open
  joining again* for a latecomer, or a student who pressed *Forget*; then
  close it again. *Open for joining* (after ending the game) opens joining
  too. A join a VM made offline that only arrives after you closed joining is
  refused as well: the student sees why and asks you.
- Students import the VM, find its address from Kali (their first task),
  open `http://<address>:8080/ctf/`, and type their name and the code. A student
  in the game appears on the board at once with 0 points.
- Follow along in admin: the game page lists everyone with their last
  report, VM version, score, cheat score and flags; **Events** is the live
  feed (filters by game, participant and kind; CSV export).

### What a student may ask

| They see | It means | Do |
|----------|----------|----|
| "Unknown join code" | typo, or an old code | give the code again (upper case and dashes are fine) |
| "This game is not open yet" / "closed" | the game's state | *Open for joining* |
| "Joining this game is closed; ask your teacher" | you closed joining | *Open joining again*, and close it after they are in |
| "Waiting for the central server" | the VM cannot reach the central server (NAT adapter, network) | nothing: they play on, the join and every solve arrive when it can |
| "Join again" with a message | the central server no longer knows the VM's token (game deleted) | they join again; their solves on the VM are kept |
| The shop is broken or hangs | they broke it (several challenges do: RCE DoS, XXE DoS, Memory Bomb, file write) | *Restart Juice Shop* on their CTF page: the progress comes back |
| A challenge with a chatbot or a wallet does not work | it needs an LLM or an Alchemy API key the VM does not have | nothing: these six cannot be solved on the VM |
| They want to start over / hand the VM on | — | *Forget* on their CTF page (they type `forget`) |

### Flags

Raised in the feed and shown next to the participant; **never acted on
automatically**. The student owns the VM, so a determined one
can fake a report; flags show what an honest VM never
sends.

**The board is a game, not an exam.** A student owns their VM: with the
VM's disk, or with code execution in Juice Shop (SSTi gives it), they can
send any solve they like, and a careful one raises no flag. A student with
SSTi can, for example, apply a classmate's continue code from inside Juice
Shop, where the VM's proxy does not see it; a code with more than five
solves in it shows as `burst`. Nothing
on a VM the student controls can prevent that. Do not grade on points
alone: ask for a short write-up of a few solves, or have a student show one
in class.

| Flag | Means |
|------|-------|
| `prerequisite` | a challenge without the one Juice Shop always solves with it (the poison-null-byte files without `nullByteChallenge`) |
| `burst` | more than 5 solves within 10 seconds |
| `before_join` / `after_end` | a solve time before the student joined, or after the game ended |
| `two_boots` | one token reporting from two Juice Shops in turn: a VM copied after it joined, or a shared token |
| `cheat_score` | Juice Shop's own cheat score averages above 0.9 over 5 or more solves (the highest score seen per challenge counts, so a 0 sent first cannot hide it) |

What to do is yours: talk to the student, or *Remove* them.

## 3. Cleaning up

| Action (game page) | Effect |
|--------------------|--------|
| *Hide* / *Show* | off the board and back; the data stays |
| *Remove…* | off the board, the token stops working, the VM is told `forget` and wipes itself — as if the student pressed *Forget*. Kept in admin as *removed* |
| *Delete* (a removed or left participant) | gone, with their solves |
| *End game* | closed: no joins, nothing counted after now, the board frozen as the final result. *Open* again if it was a mistake |
| *Clear game* | every participant and solve deleted; the game and its code stay; every VM in it forgets |
| *Delete game* | the above, and the game |

End, clear, delete and remove ask you to type the game's name. Every action
is one line in the feed. Events are kept 30 days (admin → *Settings*).

Before deleting, *Export CSV* on the game page keeps the results.

## 4. The VM

- **One image for the whole class.** It holds no identity and no secret: a
  VM gets one when its student joins.
- **Hand out the built files, never a VM you used.** `make vm` builds a VM
  that has never joined anything. A VM you joined a game with while testing
  carries that membership; every copy of it would report as you (the central
  server flags it as `two_boots`). Test on an imported copy, and throw it away.
- **Another central server.** The image points at the central server it was
  built with (`CENTRAL_URL`, deploy/vm/README.md), or at none: then its page
  asks *Which central server?* before the join, so tell the class the
  address (the one you open admin on, without `/admin/`). A VM can be pointed
  elsewhere on its own CTF page (*Central server*, before joining) — or set
  to none, and then the student plays on the VM alone with a local score.
  An address on the internet must be `https://`; plain `http://` is accepted
  only for a server on a private address or a local name, and the page then
  warns that the traffic is not encrypted.
- **Two ports.** The shop is on 80, the student's page on 8080: two origins,
  so script in the shop (Juice Shop has XSS on purpose) cannot press
  *Forget* on the page. A classmate's link to a student's shop can still run
  script in that shop.
- **Only the host-only adapter reaches the VM.** A firewall rule drops new
  connections from adapter 1 (NAT) to both ports, so a VM whose adapter 1
  is set to *Bridged* by mistake is still closed to the classroom.
- **No central server at all** works too: the page shows the score by the
  scoring table, nothing is reported.
- **A new version** is a new image to import: there is no path from the central
  server into thirty VirtualBoxes (`make vm` again). The game page shows each VM's version, and a 1.0 VM
  that still reports shows up in the feed as `legacy`.

### Every challenge is on: Juice Shop's unsafe mode

Juice Shop knows when it runs in Docker, and then switches off the
challenges that are dangerous for the machine it runs on. The VM is a
throwaway machine for one student, so the VM starts Juice Shop with
`NODE_ENV=unsafe`: Juice Shop then loads its own `config/unsafe.yml`, which
sets `challenges.safetyMode: disabled`, and **all 116 challenges** of Juice
Shop 20.3.0 are on. ctf-node sets this itself, in the environment it starts
Juice Shop with (`cmd/ctf-node/main.go`). A `NODE_ENV` set on the container
does nothing, and there is nothing to configure.

These 18 are playable because of it, and count on the board like any other:

| Category | Challenges |
|----------|------------|
| XSS | Reflected XSS (2★), API-only XSS (3★), Client-side XSS Protection (3★), CSP Bypass (4★), HTTP-Header XSS (4★), Server-side XSS Protection (4★), Video XSS (6★) |
| Injection | NoSQL DoS (4★), NoSQL Exfiltration (5★), SSTi (6★) |
| XXE | XXE Data Access (3★), XXE DoS (5★) |
| Insecure Deserialization | Blocked RCE DoS (5★), Memory Bomb (5★), Successful RCE DoS (6★) |
| Vulnerable Components | Local File Read (5★), Arbitrary File Write (6★) |
| Security Misconfiguration | Deprecated Interface (2★) |

What it means for you:

- **Students get real code execution in Juice Shop** (SSTi), can read and
  write its files, and can make it hang or run out of memory. That is
  contained: Juice Shop runs as its own user, without privileges, and cannot
  read the VM's `node.db`, stop ctf-node or take the CTF page's port. A shop
  that hangs comes back with *Restart Juice Shop*, with its progress.
- **The board counts all 116.** A VM of 1.2.0 or older runs Juice Shop in its
  safe mode and cannot solve these 18: a class with old VMs sees them on the
  board but never solved, and the totals ("x of 116") of games played before
  now count them too. Hand out the new VM.
- **Six challenges still cannot be solved**, unsafe mode or not, because
  they need a service the VM does not have, and a key in the VM would be
  every student's: *Mint the Honey Pot* and *Wallet Depletion* (an Alchemy
  API key) and the four that need an AI chatbot, *Chatbot Prompt Injection*,
  *Greedy Chatbot Manipulation*, *AI Debugging* and *System Prompt
  Extraction* (an LLM at `localhost:11434`). They stay on the board.
- The Juice Shop image has no shell. The usual SSTi payload, which runs
  `wget` with `child_process.exec`, does nothing, but the challenge is solved
  by any code Juice Shop evaluates.

### Starting, stopping, resetting

The VM has no login; all of it goes through the hypervisor and the
student's CTF page.

| To | Do |
|----|----|
| start / stop | start or shut down the VM. The container starts with the VM and after a crash; the progress is kept |
| a shop that hangs or is broken | *Restart Juice Shop* on the CTF page (or restart the VM): the progress comes back |
| start over | *Forget* on the CTF page: everything about the student is wiped, Juice Shop starts empty |
| a VM that is beyond repair | delete it and import the OVA again: a new VM that has never joined; the student joins again |
| a new version | `make vm` (deploy/vm/README.md), and every student imports it |

In the test setup on your own machine: `make run`, `make stop`, and
`deploy/compose/stop.sh --wipe` to throw away every game and VM.

## 5. Trying it without a class

```sh
make run          # deploy/compose/start.sh: the central server and three VMs, each with its "Kali"
make e2e          # everything above, checked end to end (~5 min)
```

The addresses and the setup code are printed by `start.sh`; admin is
published on 127.0.0.1 only there.
