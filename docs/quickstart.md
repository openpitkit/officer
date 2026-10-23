# Quickstart

This page starts Pit Officer on your own computer with Docker. You need Docker
and one command. You do not need programming tools, a build, or any settings.

A few words used below:

- **Docker** - a program that runs ready-made software packages.
- **Image** - such a package. Officer is published as the image
  `ghcr.io/openpitkit/officer`.
- **Container** - a running copy of an image. Officer's container is named
  `pit-officer`.
- **Volume** - a storage area Docker keeps for a container. Officer's data
  lives in the volume `pit-officer-data`.
- **Panel** - Officer's web page, where you look at and manage Officer.

## Install and start Docker

You install Docker once.

**Docker Desktop (Windows, macOS or Linux).** Install Docker Desktop by the
official guide at <https://docs.docker.com/get-started/get-docker/>. Then start
it and wait until it reports that it is running. Docker Desktop is free for
personal use and small businesses. Larger organisations need a paid Docker
subscription, so on a work computer check with your IT department first.

**Linux with Docker Engine.** Docker Engine is Docker without the Desktop app.
Install it by the guide at <https://docs.docker.com/engine/install/>. Use Docker
Engine 28.0.0 or newer, and 28.3.3 or newer if your system uses firewalld:
older versions can let other computers on the same local network reach the
panel. `docker version` shows which version you have. There is no app to
start, so make sure the Docker service runs:

```sh
sudo systemctl start docker
```

With Docker Engine, choose one of two ways to run `docker` commands. Either
follow the post-installation steps at
<https://docs.docker.com/engine/install/linux-postinstall/>, so that you can run
`docker` without `sudo`. Or put `sudo` - the Linux way to run a command as an
administrator - in front of every `docker` command on this page.

However you installed Docker, the `docker` commands below are the same.

## Open a terminal

A terminal is a window where you type commands. You type each command on this
page there, press Enter, and read what it prints.

- **Windows** - open PowerShell from the Start menu.
- **macOS** - open Terminal from Applications > Utilities, or search for
  Terminal in Spotlight.
- **Linux** - open your terminal application.

## Start Officer

Copy this command, paste it into the terminal, and press Enter. It is the same
on Windows, macOS and Linux.

```sh
docker run -d --name pit-officer --restart unless-stopped -p 127.0.0.1:8787:8787 -v pit-officer-data:/data ghcr.io/openpitkit/officer
```

The command runs Officer in the background, in a container named
`pit-officer`. The panel can be reached from this computer only. Officer's data
is kept in the volume `pit-officer-data`. From now on, Docker starts Officer
again whenever Docker itself starts, unless you stopped Officer.

The first time, the command first downloads the Officer image and shows the
download. A line `Unable to find image ... locally` and lines of download
progress are normal.

Then the command prints one long line of letters and digits. That line is the
new container's ID. If nothing else follows it, Docker has started Officer, but
Officer is not ready yet. If an error follows the ID, Officer did not start - see
[When something goes wrong](#when-something-goes-wrong).

## Wait until it is ready

Officer checks itself and reports whether it is ready. To see that report, run:

```sh
docker ps --filter name=pit-officer
```

Look at the STATUS column. Right after the start it reads
`Up 1 second (health: starting)`. Run the same command again after a few
seconds. Officer is ready when STATUS shows `(healthy)`, for example
`Up 7 seconds (healthy)`.

This usually takes a few seconds.

`healthy` means the panel answers. It does not mean that anything is set up -
see [What comes next](#what-comes-next).

If STATUS shows something other than `(health: starting)` or `(healthy)`, or
the command shows no line for `pit-officer`, go to
[When something goes wrong](#when-something-goes-wrong).

## Open the panel

Open <http://127.0.0.1:8787> in your browser and bookmark it.

The address 127.0.0.1 means your own computer, so other computers cannot reach
the panel.

The first time, the panel opens the first-run setup at
<http://127.0.0.1:8787/setup>. It has seven steps: welcome, accounts,
encryption, approval signing, access, connecting your AI agent, and done.

The access step decides how software on this computer gets in. With an access
token - a secret that software must show - the REST API and MCP, described
below, let a program in only with the token, and the panel asks for a
password. Software that can run commands as you, or control Docker, still gets
past both. Without a token, the panel has no login. REST and MCP stay closed
until you finish the setup.

The same address also serves the REST API for programs under `/api/v1`, with
its reference at `/docs`, and MCP for AI agents at `/mcp`.

## Open the panel again later

Use your bookmark, or type <http://127.0.0.1:8787> into your browser. You can
also open it from the terminal with the command for your system.

Windows PowerShell:

```powershell
Start-Process http://127.0.0.1:8787
```

macOS Terminal:

```sh
open http://127.0.0.1:8787
```

Linux desktop:

```sh
xdg-open http://127.0.0.1:8787
```

These commands open the browser on your own computer. Officer runs inside the
container and cannot open a browser for you. The link works only while Officer
is running - see [Stop and start](#stop-and-start).

## Stop and start

To stop Officer:

```sh
docker stop pit-officer
```

To start it again:

```sh
docker start pit-officer
```

After you restart your computer, start Docker Desktop, or on Linux with Docker
Engine make sure the Docker service runs. Officer comes back with Docker by
itself. The exception is an Officer you stopped with `docker stop`: it
stays stopped until you run `docker start pit-officer`.

Do not run the long `docker run` command from [Start Officer](#start-officer)
again to start an Officer that is already installed. It fails, because the name
`pit-officer` is taken. Use `docker start pit-officer`.

## Your data

Officer keeps all its data in the volume `pit-officer-data`, not in the
container. The data stays when you stop and start Officer. It also stays when
you remove the container and create it again, with these three commands:

```sh
docker stop pit-officer
```

```sh
docker rm pit-officer
```

```sh
docker run -d --name pit-officer --restart unless-stopped -p 127.0.0.1:8787:8787 -v pit-officer-data:/data ghcr.io/openpitkit/officer
```

`docker rm` removes only the container. It never deletes a named volume such as
`pit-officer-data`.

When you create the container again, pass the same extra settings you gave it
before, for example a master key. The volume keeps the data, not the container's
settings.

> **Warning:** `docker volume rm pit-officer-data` deletes all Officer data. It
> is never the fix for anything on this page.

## When something goes wrong

Find what you see, then do what the entry says.

### The docker command is not found

The terminal says that `docker` is not found, or is not recognized as a command.
Docker is not installed, or the terminal was opened before the installation
finished. Install Docker as in [Install and start
Docker](#install-and-start-docker), then close the terminal, open a new one,
and run the command again.

### Docker is not running

The terminal prints a message like one of these, or a message that starts with
`error during connect`:

```text
failed to connect to the docker API at ...; check if the path is correct and if the daemon is running: ...
```

```text
Cannot connect to the Docker daemon at ... Is the docker daemon running?
```

Docker is installed but not running. Start Docker Desktop and wait until it
reports that it is running. On Linux with Docker Engine, start the Docker
service instead:

```sh
sudo systemctl start docker
```

Then run your command again. Nothing was created, so running it again is safe.

### Permission denied on Linux

On Linux the terminal prints `permission denied` and mentions the Docker socket
(`docker.sock`, the door through which commands reach Docker). Your user is not
allowed to use Docker yet. Follow the post-installation steps at
<https://docs.docker.com/engine/install/linux-postinstall/>, or put `sudo` in
front of every `docker` command on this page.

### The port is busy

The `docker run` command prints the long ID and then an error that contains
`ports are not available`, `address already in use`, or
`port is already allocated`. For example:

```text
docker: Error response from daemon: ports are not available: exposing port TCP 127.0.0.1:8787 -> 127.0.0.1:0: listen tcp4 127.0.0.1:8787: bind: address already in use
```

Another program on this computer already uses port 8787, the number at the end
of the address. Docker created the container but could not start it, so the
container is left in state `Created`.

First remove that container. This removes only the container that never
started. The data volume stays.

```sh
docker rm pit-officer
```

Then do one of these:

- Close the other program, and run the command from
  [Start Officer](#start-officer) again.
- Or run the command with another port: change the first 8787 in it, and keep
  `127.0.0.1:` in front of the new number. For example, write
  `-p 127.0.0.1:8788:8787` instead of `-p 127.0.0.1:8787:8787`. Then use
  <http://127.0.0.1:8788> everywhere this page says 8787.

If you choose port 8788, the command is:

```sh
docker run -d --name pit-officer --restart unless-stopped -p 127.0.0.1:8788:8787 -v pit-officer-data:/data ghcr.io/openpitkit/officer
```

Never remove `127.0.0.1:` from the command. Without that prefix, other
computers can reach the panel.

### The name is already in use

The `docker run` command prints:

```text
docker: Error response from daemon: Conflict. The container name "/pit-officer" is already in use by container "<id>". You have to remove (or rename) that container to be able to reuse that name.
```

A container named `pit-officer` already exists, so Officer is most likely
installed already. Do not remove it before you check its state:

```sh
docker ps -a --filter name=pit-officer
```

Look at the STATUS column:

- `Up ... (healthy)` - Officer is running and ready.
  [Open the panel](#open-the-panel).
- `Up ... (health: starting)` - Officer is starting.
  [Wait until it is ready](#wait-until-it-is-ready).
- `Exited (0) ...` - Officer is stopped. Start it with
  `docker start pit-officer`.
- `Created` - Officer never started. Follow
  [The port is busy](#the-port-is-busy).
- `(unhealthy)`, `Restarting ...`, or `Exited` with another number - follow
  [Officer is not ready or the panel does not
  open](#officer-is-not-ready-or-the-panel-does-not-open).

Never delete the `pit-officer-data` volume to resolve this.

### Officer is not ready or the panel does not open

`docker ps` lists only containers that are running. If
`docker ps --filter name=pit-officer` shows no line for `pit-officer`, check its
state as in [The name is already in use](#the-name-is-already-in-use).

If STATUS shows `(unhealthy)`, `Restarting`, or `Exited` with a number other
than 0 such as `Exited (1)`, or the panel does not open, two commands show what
went wrong.

What Officer printed:

```sh
docker logs pit-officer
```

What Officer's readiness check reported:

```sh
docker inspect --format "{{json .State.Health}}" pit-officer
```

When Officer works, its log has a line with `panel listening` and the panel
address, and the readiness check reports `"Status":"healthy"`. The readiness
check's own messages appear only in the second command's output, not in
`docker logs`.

## What comes next

A fresh Officer is empty. It has no account, no instrument, no market data, no
policy and no trading connection. It invents no prices and sends no orders.

In the panel, set up an account, an instrument, its market data, and the
policies. Starting Officer does not do that for you. Nothing trades until you
add a trading connection.

Approval signing is off on a fresh Officer. The first-run setup offers to turn
it on. To turn it on later:

1. Set a master key first. It seals the secrets Officer stores, signing keys
   included. The [Master key](configuration.md#master-key) section of the
   configuration reference covers it. Setting the key means creating the
   container again, as in [Your data](#your-data), with the key passed in.
2. In the panel, open Signing Keys, and generate or import a key.
3. Switch on "Require signature on approval and attestation envelopes".
