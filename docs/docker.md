# Docker runtime

By default every node of a lab is a process on your machine. A lab created
with `--runtime docker` runs each validator in its own container instead, on
a network of its own, so you can log in to a validator and work as you would
on a real one.

```sh
forklab lab create xrp --profile xrplevm --version 11.1.1 --validators 4 --chain-id xrplevm_1449999-1 --runtime docker
forklab lab up xrp
docker exec -it forklab-xrp-node0 bash
```

Every other forklab command works the same: `status`, `node`, `account`,
`gov`, `upgrade`, runbooks, and the TUI. The runtime is fixed when the lab is
created, and `lab show`, `lab list`, and `node list` name it. Labs on the
default `host` runtime never call docker.

forklab runs the `docker` command that is on your `PATH`. These were run:

| `docker` command | Engine | Status |
|------------------|--------|--------|
| Docker CLI 29 | Podman 6, rootless, through its API socket (`DOCKER_HOST`) | The e2e suite runs on it |
| Podman's own CLI as `docker`: a script that runs `podman` with the same arguments, which is what the `podman-docker` package installs | Podman 6, rootless | One lab was created, started, taken down, and deleted through such a script; the package itself was not installed |
| Docker CLI | Docker Engine, rootless or rootful | Not run |

`--runtime docker` combines with [`--cosmovisor`](upgrades.md#cosmovisor-mode):
cosmovisor then runs in the container and swaps the binary there.

## Log in to a validator

```sh
docker exec -it forklab-xrp-node2 bash
```

In that shell the chain binary is on `PATH`, and it finds its node and the
lab keys without `--home`, `--node`, `--chain-id`, or keyring flags:

```sh
exrpd status
exrpd keys list
exrpd query bank balances "$(exrpd keys show test0 -a)"
exrpd tx bank send test0 "$(exrpd keys show test1 -a)" 1000axrp --gas-prices 800000000000axrp -y
```

A transaction still needs a fee: pass the profile's `gas_prices`.

forklab sets this up when it creates the lab and starts the container:

- The binary's default home, such as `~/.exrpd`, is a link to the node home.
- The node's `config/client.toml` holds the lab's chain ID and the `test`
  keyring backend, and its `node` is the node in the same container.
- The node home has a `keyring-test` link to the lab keyring, which all
  nodes share.

| Variable | Value |
|----------|-------|
| `FORKLAB_NODE_HOME` | The node home, the same path as on the host |
| `FORKLAB_KEYS` | The lab keyring directory |
| `FORKLAB_NODE_LOG` | The node log |
| `FORKLAB_BIN` | The chain binary the node runs |

The default image also has `bash`, `curl`, `jq`, and `ps`; an image you pass
with `--image` has what you put in it. The other validators
answer by container name, for example `curl forklab-xrp-node1:26657/status`.

## Names, network, and ports

| Thing | Name |
|-------|------|
| Container of node `i` | `forklab-<lab>-node<i>`, with host name `node<i>` |
| Network | `forklab-<lab>` |
| Address of node `i` | The tenth address of the network's subnet plus `i`, for example `10.89.0.10` for node 0 |

The names hold only the lab name, so two labs of the same name under two
forklab homes (`FORKLAB_HOME`) would share them. forklab therefore labels
each container and network with `forklab.lab=<lab directory>`, and removes
or reuses only what carries its own lab's label. When a container or network
of that name belongs to another lab, or was not created by forklab,
`lab create`, `lab up`, and `lab delete` stop with `... is not this lab's`
and leave it alone. What the command did before it got there stays done:
other nodes may already run, or other containers may already be removed.
Remove the foreign one yourself, or use another lab name.

Inside its container every node listens on the default ports (26656, 26657,
1317, 9090, and the profile's extra ports). forklab publishes each node's
ports on `127.0.0.1` of the host at the same numbers a host lab uses: the
defaults for node 0, plus 100 per node. `lab show` lists them. The ports must
be free when the lab is created and when it starts.

### Addresses

`lab create` creates the network. The engine chooses its subnet, forklab
records it, and every node gets a fixed address in it. A node's
`persistent_peers` name the other nodes by these addresses, not by container
name. Container names still resolve, so you can use them in a shell.

The addresses are fixed because of how CometBFT treats a peer's address. A
node looks a peer's name up once when it starts, drops the peer when the name
does not resolve at that moment, and keeps the address it got in memory and
in `config/addrbook.json`. forklab removes a node's container when the node
stops and creates a new one when it starts, and the engine may give the new
container another address. With names, nodes that restarted at about the
same time, as in an upgrade, could each keep dialing an address that no
container had any more, and the chain stopped.

The network stays until `lab delete`, also while the lab is down, so the
subnet stays the lab's. Every docker lab therefore holds one subnet of the
engine's address pool for as long as it exists, and Docker's default pool
has room for about 30 networks. If the network is removed anyway, for example by
`docker network prune`, the next start creates it again on the recorded
subnet. When another network has taken that subnet in the meantime, the
start fails with `create the network ... on the lab's subnet ...`: remove
the other network, or create the lab again.

## Image and binaries

Without `--image`, forklab builds a small image from `debian:trixie-slim`
the first time a docker lab needs it and names it `forklab-node:<hash>`. The
image holds no chain binary. One image serves every lab, chain, and version.

When the engine no longer has the image, for example after `docker system
prune -a`, the next command that starts a node builds it again first and
prints `building the node image ...` while it does. forklab builds with
`docker build --load`, and without `--load` when the `docker` command does
not know that flag, as a Docker CLI without the buildx plugin does not. That
second way was tried with this machine's Docker CLI and its legacy builder
only, not on an installation without buildx.

forklab mounts into each container, at the same path as on the host:

| Mount | Mode |
|-------|------|
| The lab directory | read-write |
| The chain binary, and cosmovisor in a cosmovisor lab | read-only |
| `/nix/store`, when the host has one | read-only |

Node homes, logs, and keys therefore stay in the lab directory, and
`node logs`, `lab reset`, and `lab delete` work on them as usual. An upgrade
does not rebuild anything: the supervisor starts a new container with the new
binary mounted, or cosmovisor switches to the binary in the node home.

## File ownership

Files that a node writes in the lab directory stay yours. forklab asks the
engine how it runs and chooses the container user from the answer:

| Engine | Container user | Why |
|--------|----------------|-----|
| Rootless (Podman, rootless Docker) | `root`, also when the image names another `USER` | Root in the container is your user on the host. Any other user could not write the lab directory, and would own its files with an id you cannot delete. |
| Rootful Docker | Your uid and gid, with `HOME=/tmp` | Root in the container would create root-owned files in the lab. |

## Limits

- **The binary must also run on the host.** forklab runs the chain binary on
  your machine to build genesis, to export a fork, and for every query and
  transaction it sends. Only the nodes run in containers.
- **The binary must run in the image.** `lab create` and `upgrade schedule`
  run `<binary> version` in the image first and stop with the loader's
  message when it fails. In a cosmovisor lab, `lab create` also runs
  `cosmovisor help` in the image, which matters for a cosmovisor you pass
  with `--cosmovisor-bin`.
- **SELinux.** forklab starts every container with `--security-opt
  label=disable`, because an enforcing host denies a container its bind
  mounts otherwise, and relabelling the lab directory, the binaries, and
  `/nix/store` would be wrong. This was not tried on an SELinux host.
- **Libraries.** A release binary that needs only glibc runs in the
  default image. For a binary that loads other libraries, such as
  `libwasmvm`, build an image that has them and pass it with `--image`.
- **Binaries built on NixOS** point at an interpreter in `/nix/store`.
  forklab mounts the host's `/nix/store` read-only for that reason, so they
  run in the default image.
- **Cosmovisor downloads** in a docker lab happen inside the container, so
  the URL must be reachable from there.

## Cleanup

`lab down` removes the lab's containers and keeps its network; see
[Addresses](#addresses). A node that stops
or is killed loses its container at once, and the next start creates a new
one. `lab delete` removes the network, and also containers that a lab left
behind, for example after its supervisor was killed. It refuses while the
containers run; `lab delete <name> --force` stops them first and then
removes containers, network, and directory. The directory goes last: when
the engine does not confirm that the containers and the network are gone,
`lab delete` reports that and keeps the lab. The `forklab-node` image
stays, and `docker rmi` removes it.

forklab asks the engine about a docker lab's containers even when the lab
has no pid files, because a supervisor that is killed while it starts a node
can leave a container that no file records. The next supervisor tries to
remove such a container, and logs it when the engine refuses. A stopped
docker lab therefore
also reads as running in `lab list` while the engine does not answer.

forklab waits 30 seconds for the engine to answer a question or a stop, and
longer for `docker run` and `docker build`, which may pull an image. An
engine that takes longer is treated like one that does not answer.

## Errors

| Message | Meaning |
|---------|---------|
| `the docker CLI is not on PATH` | Install Docker, or Podman with its `docker` command |
| `the docker engine does not answer` | The `docker info` call failed, and the message that follows says how: start the engine, or point `DOCKER_HOST` at it |
| `the container needs ... mounted` | The chain binary or cosmovisor that `nodes.json` names is not on the host any more; fetch or build it again |
| `build the node image ...` | The image build failed and the engine's output follows, or the build succeeded and `the engine does not have` the image: the builder keeps its result to itself, so pass an image with `--image` |
| `... does not run in the image ...` | The binary needs libraries the image lacks; see [Limits](#limits) |
| `port ... is in use`, or `address already in use` from `lab up` | Another process holds a port the lab publishes |
| `cannot tell whether the node runs` | The engine did not answer, so forklab assumes the containers may be running: `lab list` shows the lab as running, and `lab down`, `lab reset`, and `lab delete` refuse until the engine answers |
| `the engine did not answer within ...` | The engine accepted the call and did not finish it in time; forklab treats the container's state as unknown |
| `the container ... is not this lab's`, or `the network ...` | Something else has the name; see [Names, network, and ports](#names-network-and-ports) |
