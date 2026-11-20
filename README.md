[![release](https://github.com/macsnoeren/juiceshop-ctf/actions/workflows/release.yml/badge.svg)](https://github.com/macsnoeren/juiceshop-ctf/actions/workflows/release.yml)
[![Dependabot Updates](https://github.com/macsnoeren/juiceshop-ctf/actions/workflows/dependabot/dependabot-updates/badge.svg)](https://github.com/macsnoeren/juiceshop-ctf/actions/workflows/dependabot/dependabot-updates)

# Juice Shop CTF

[Releases](https://github.com/macsnoeren/juiceshop-ctf/releases): the students' VM for VirtualBox and VMware ·
[Docker Hub](https://hub.docker.com/r/macsnoeren/juiceshop-ctf-central): the central server

The [OWASP Juice Shop](https://owasp.org/www-project-juice-shop/) already has a [Juice Shop CTF](https://juice-shop.github.io/tab_ctf.html) that is based on Kubernetes. Although it works perfectly fine, you need a platform to run Kubernetes on in order to use it. When I wanted to use the Juice Shop during one of my lectures, I did not want to run a Kubernetes cluster. However, I did want an overview of all my students during the challenge. Therefore, I created a different way to run a Juice Shop CTF.

This approach is based on a local cyber security lab environment that runs on the participants' laptops. While I use [VirtualBox](https://www.virtualbox.org/) to build the cyber security lab, it could be based on other virtualisation platforms as well. In my VirtualBox environment I run [Kali Linux](https://www.kali.org/) as the main hacking tool, together with different target VMs for (pen)testing activities. During my ethical hacking lectures, my students already have this available on their laptops.

I have created a VM to be used as a target machine within the cyber security lab environment. This VM runs the Juice Shop and a CTF application (ctf-node) that reports which challenges the participant has solved to the central CTF server (ctf-central). As the CTF leader, you run the central CTF server. There you can manage the users who have access to the CTF, make the required changes and start CTF events.

When you run a CTF event as the operator, the participants download the VM and register for the event with an access code. You can follow the participants' progress on a big screen. The participants use a browser and Kali Linux to research and attack the Juice Shop.

This solution removes the need for a Kubernetes cluster. You only need to host the central CTF web server in a Docker environment. Participants connect to this server and you are good to go. It is also possible to run the central CTF server locally, on a separate network. The diagram below shows how it works:

```
 the participant's cyber security lab on their laptop (VirtualBox)
                                                                
 Kali Linux   ──► host-only ──► CTF VM: juiceshop-ctf ── NAT ─────►  ctf-central
 browser,         network       :8080/ctf/  ctf-node: join,          games and join codes,
 Burp Suite                                 progress, restart,       boards per game, beamer,
                                            forget                   admin and the event feed
                                :80/juiceshop/  Juice Shop v20.3.0
```

The operator makes a **game** on the central CTF server's /admin page and shows its **join code**; a participant types their name and that code on their VM's page, and the VM gets a token for that participant. The VM image is the same for everybody and holds no secret.

| | |
|---|---|
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | running a lab: games, the board, cleaning up, the VM |
| [docs/STUDENT.md](docs/STUDENT.md) | the students' sheet |
| [docs/PROTOCOL.md](docs/PROTOCOL.md) | join, report and leave between VM and central server |
| [docs/THREAT-MODEL.md](docs/THREAT-MODEL.md) | the threat model: what is attacked, how it is defended, what is left to the operator |
| [docs/DEVELOPER.md](docs/DEVELOPER.md) | for developers: how the code fits together, where things are, how to make and test a change |
| [CHANGELOG.md](CHANGELOG.md) | the change log of the application |

## Try it?

Needs Docker with compose.

```sh
make run      # the central server and three students' VMs, each with its "Kali"; prints the addresses
make stop     # deploy/compose/stop.sh --wipe forgets everything
```

Then open `http://127.0.0.1:8090/juiceshop-ctf/admin/`: the first time it
asks you to make an admin account, with the setup code `make run` printed.
Make a game there, join it from `http://127.0.0.1:8191/ctf/`, solve
`http://127.0.0.1:8091/juiceshop/ftp/acquisitions.md` and watch the board.

## On a server

The central server is one container, from Docker Hub
([deploy/docker/DOCKERHUB.md](deploy/docker/DOCKERHUB.md) has the options):

```sh
docker run -d --name ctf-central -p 8080:8080 -v ctf-central-db:/data \
  macsnoeren/juiceshop-ctf-central:latest
```

Open `/admin/` and make the first admin account with the **setup code** in
its log (`docker logs ctf-central | grep setup_code`). Behind a reverse
proxy, an IP allow-list in front of `/admin/` is a good second lock.
[docs/THREAT-MODEL.md](docs/THREAT-MODEL.md) says what the CTF protects
against, and what an operator must do for it.

The students' VM is built here, for VirtualBox and VMware, with nothing but
Docker ([deploy/vm/README.md](deploy/vm/README.md)), and points at your
central server:

```sh
CENTRAL_URL=https://ctf.example.org make vm   # dist/vm/juiceshop-ctf-<version>-virtualbox.ova and -vmware.zip
```

A release takes that address from the repository variable `CENTRAL_URL`
([.github/workflows/release.yml](.github/workflows/release.yml)).

The image runs anywhere Docker does:

```sh
docker run -d -p 80:80 -p 8080:8080 -v juiceshop-ctf:/var/lib/ctf \
  --cap-drop ALL --cap-add SETUID --cap-add SETGID --security-opt no-new-privileges \
  juiceshop-ctf
```

The shop is on port 80, the student's page on port 8080. Without
`CTF_CENTRAL_URL` (or with *No central server* on its page) it runs on its
own and shows the local score.

## Development

Two Go programs, `ctf-node` on the VM and `ctf-central` on the server, in one
module. [docs/DEVELOPER.md](docs/DEVELOPER.md) explains how they fit
together, where everything is, and how to test a change (`make test`,
`make e2e`).

## License

Copyright (C) 2026 Maurice Snoeren.

This program is free software: you can redistribute it and/or modify it
under the terms of the [GNU General Public License](LICENSE) as published by
the Free Software Foundation, either version 3 of the License, or (at your
option) any later version. It is distributed WITHOUT ANY WARRANTY; see the
license for details. Every source file says so in its first lines
(`SPDX-License-Identifier: GPL-3.0-or-later`).

Not covered by this license, and under their own:

- [OWASP Juice Shop](https://github.com/juice-shop/juice-shop) (MIT), the
  base of the `juiceshop-ctf` image. The challenge catalogue
  `internal/juiceshop/catalogue-20.3.0.json` and the fixtures in
  `internal/juiceshop/testdata/` are captured from it.
- The Go dependencies in `go.mod`: `modernc.org/sqlite` and
  `golang.org/x/text` (both BSD-3-Clause), which allow use in a GPL program.
