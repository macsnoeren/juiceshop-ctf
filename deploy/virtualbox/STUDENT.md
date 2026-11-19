# Juice Shop CTF — getting started

You get two virtual machines: **Kali Linux**, where you attack from, and the
**CTF VM**, which runs your own OWASP Juice Shop. Nobody else can reach your
Juice Shop, and every challenge you solve in it counts for you on the
leaderboard.

**1. Import the CTF VM.** In VirtualBox: *File → Import Appliance…* →
`juiceshop-ctf.ova` → *Import*. You now have it next to your Kali.

**2. Check the network adapters** (*Settings → Network*) of the CTF VM:

| Adapter | Attached to | |
|---------|-------------|-|
| 1 | **NAT** | so the VM can reach the leaderboard |
| 2 | **Host-only Adapter** — the same one Kali uses | so Kali can reach the VM |

Kali needs an adapter on that same **host-only** network. Do **not** use
*Bridged*: that puts your Juice Shop on the classroom network, where anyone can
solve challenges for you, or break your shop. There is nothing to configure
inside either VM.

**3. Start the CTF VM**, then Kali. The CTF VM needs about a minute.

**4. Find the CTF VM — that is your first task.** It is on Kali's host-only
network and got its address from VirtualBox. `ip a` shows Kali's own
address on that network; `nmap` or `netdiscover` finds the other machine on
it. Port 80 is the one that answers.

**5. Join.** In Kali's browser open `http://<address of the CTF VM>/ctf/`.
Type your name (it is shown on the leaderboard) and the **join code** your
teacher shows. Then *Open the Juice Shop* — `http://<address>/juiceshop/`.

**6. Hack.** Browser, Burp, curl, sqlmap: everything works without a cookie
or a password. Your CTF page shows your points, your rank and a link to the
leaderboard.

On your CTF page:

- **Restart Juice Shop** — when you have broken the shop. Your progress comes
  back by itself.
- **Forget** — deletes everything the VM knows about you and starts the shop
  empty. Only to start over or to hand the VM to someone else; what you
  already scored stays on the leaderboard, but you are taken off it.

Something wrong? Your CTF page usually says what. "Waiting for the central
server" is fine: play on, your solves arrive when it can. Otherwise ask your
teacher.
