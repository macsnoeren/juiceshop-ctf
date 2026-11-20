# Test setup

The whole CTF 1.1 on one machine: the central server and three students'
VMs (`juiceshop-ctf`), each with a small container playing the student's
Kali on a network of its own. See the header of
[docker-compose.yml](docker-compose.yml) for the picture.

```sh
./start.sh            # build, start, print the addresses
./stop.sh             # stop, keep the data
./stop.sh --wipe      # stop and forget everything
```

Then:

1. Open the admin (`http://127.0.0.1:8090/juiceshop-ctf/admin/`). The
   first time, it asks you to make an account: that is the setup page. It
   asks for the setup code that `./start.sh` printed (or
   `docker compose logs ctf-central | grep setup_code`). The account stays
   logged in on this browser. Make a game, open it; its join code is on the
   game page.
2. Open a VM's page (`http://127.0.0.1:8191/ctf/`), type a name and the
   code. Its shop is on another port (`http://127.0.0.1:8091/juiceshop/`), as
   on the real VM, where the page is on 8080 and the shop on 80.
3. Solve something in its Juice Shop — `http://127.0.0.1:8091/juiceshop/ftp/acquisitions.md`
   is one — and watch the game's board.

`scripts/e2e.sh` runs this setup under its own project name and ports and
checks every behaviour of 1.1 end to end; `scripts/image.sh` checks the image
on its own.
