# Running a lab

For the person who runs a Juice Shop CTF with a class. You need: the central
server (the image `macsnoeren/juiceshop-ctf-central`, admin at
`<server>/admin/` with its own login; behind a reverse proxy, ideally with
an IP allow-list in front of admin as well), and the CTF VM
(`juiceshop-ctf-<version>-virtualbox.ova`, or `-vmware.zip` for VMware), from
a release or built with `CENTRAL_URL=<your server> make vm`. How it is made is in
[deploy/vm/README.md](../deploy/vm/README.md); the students'
sheet is [deploy/vm/STUDENT.md](../deploy/vm/STUDENT.md).

```
 each student's laptop (VirtualBox/VMware)                              a server
 Kali ── host-only ──► CTF VM (Juice Shop + ctf-node) ── NAT ──►  central server: games, boards, admin
```

## 0. Admin accounts

Admin has accounts of its own. All of them can do everything, including
managing the others.

- **The very first time** admin is opened on a new central server, it shows
  the **setup page**: make the first account there (a username and a
  password of at least 10 characters). Until that is done, whoever opens
  admin first becomes its owner. An IP allow-list in the reverse proxy keeps
  that to you; without one, open admin yourself right after the server
  starts.
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

## 1. Before the lesson

1. **Make a game.** Admin → *New game*: a name ("Class 2A — 19 Nov"),
   optionally a start and an end (after the end no solve counts and the board
   shows the final result), the points per difficulty (1.0's 100 / 250 / 450 /
   700 / 1000 / 1350 by default), and whether the board is hidden or shows
   initials instead of names.
2. **Write down the join code.** It is shown once, on the page after
   *Create*. It is stored only as a hash: lost means *New join code* on the
   game page (students who joined keep playing; the old code stops admitting).
3. The game is a **draft**: nobody can join yet. Press *Open for joining*
   (or *Start*) when the class may come in. Open and running both admit
   students and count solves.
4. Hand out the VM image and the student sheet.

## 2. In the lesson

- Put the **join code** and the **board** on the beamer: the board of a game
  is `…/juiceshop-ctf/g/<slug>/`, full screen at `…/g/<slug>/beamer` (a
  countdown when the game has an end, the ticker at the bottom, the final
  result when it closes).
- Students import the VM, find its address from Kali (their first task),
  open `http://<address>/ctf/`, and type their name and the code. A student
  in the game appears on the board at once with 0 points.
- Follow along in admin: the game page lists everyone with their last
  report, VM version, score, cheat score and flags; **Events** is the live
  feed (filters by game, participant and kind; CSV export).

### What a student may ask

| They see | It means | Do |
|----------|----------|----|
| "Unknown join code" | typo, or an old code | give the code again (upper case and dashes are fine) |
| "This game is not open yet" / "closed" | the game's state | *Open for joining* |
| "Waiting for the central server" | the VM cannot reach the central server (NAT adapter, network) | nothing: they play on, the join and every solve arrive when it can |
| "Join again" with a message | the central server no longer knows the VM's token (game deleted) | they join again; their solves on the VM are kept |
| The shop is broken | they broke it (there is a DoS challenge) | *Restart Juice Shop* on their CTF page: the progress comes back |
| They want to start over / hand the VM on | — | *Forget* on their CTF page (they type `forget`) |

### Flags

Raised in the feed and shown next to the participant; **never acted on
automatically** (decision D5). The student owns the VM, so a determined one
can fake a report (VERSION-1-1.md §2.1); flags show what an honest VM never
sends.

| Flag | Means |
|------|-------|
| `prerequisite` | a challenge without the one Juice Shop always solves with it (the poison-null-byte files without `nullByteChallenge`) |
| `burst` | more than 5 solves within 10 seconds |
| `before_join` / `after_end` | a solve time before the student joined, or after the game ended |
| `two_boots` | one token reporting from two Juice Shops in turn: a VM copied after it joined, or a shared token |
| `cheat_score` | Juice Shop's own cheat score averages above 0.9 over 5 or more solves |

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
- **No central server at all** works too: the page shows the score by the
  scoring table, nothing is reported.
- **A new version** is a new image to import: there is no path from the central
  server into thirty VirtualBoxes (`make vm` again). The game page shows each VM's version, and a 1.0 VM
  that still reports shows up in the feed as `legacy`.

## 5. Trying it without a class

```sh
make run          # deploy/compose/start.sh: the central server and three VMs, each with its "Kali"
make e2e          # everything above, checked end to end (~5 min)
```

The addresses are printed by `start.sh`; admin is open on 127.0.0.1 there.
