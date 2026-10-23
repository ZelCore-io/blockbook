# Build Blockbook with temporary swap

On the Linux build host, from a clean checkout:

```sh
git pull --ff-only origin master
sudo ./contrib/scripts/build-with-swap.sh groestlcoin
```

The wrapper defaults to a **4 GiB temporary swap file**, with **4 GiB of free disk reserved** for the build. It requires at least 8 GiB free on the filesystem containing `/var/tmp`, root privileges, and an ext2/ext3/ext4 or XFS filesystem. Btrfs, tmpfs and other filesystems are rejected rather than guessing their swap setup requirements. Swap creation can still fail if the kernel/filesystem does not support it; in that case the build never starts.

It runs only this Blockbook package build, preserving the existing release-source guard:

```sh
BB_BUILD_ENV=prod make release-deb-blockbook-groestlcoin PORTABLE=1 \
  'BB_RPC_ENV=-e GOFLAGS="-p=1 -timeout=45m"'
```

`BB_BUILD_ENV` defaults to `prod` if unset. Additional arguments after the coin are forwarded to `make`, allowing explicit overrides. The low-memory flags serialize Go package work and extend the test timeout; no tests are skipped. Existing Docker build images are reused according to the normal Makefile rules. The wrapper neither installs the resulting package nor changes the daemon, index, services, existing swap, or `/etc/fstab`.

An exclusive lock prevents two wrapper runs at once. The file is allocated in a private randomly named directory under `/var/tmp`, with mode 0600 and real allocated blocks. Existing swap files are never overwritten. Normal completion, build failure, and catchable interruptions trigger cleanup. Bash waits for a foreground build command to return before processing a signal sent only to the wrapper, keeping the swap available while that build still runs. The build exit status is preserved; cleanup failure after a successful build returns 1 and explains that the build output may already exist.

Before disabling the temporary swap, the wrapper requires available RAM to exceed that file's used swap by 256 MiB (when any of it is used). This is deliberately conservative even when other swap is available. If RAM is insufficient or `swapoff` fails, the active swap file is retained and the exact manual cleanup command is printed. Do not delete an active swap file. `SIGKILL`, power loss or a host reboot cannot run shell cleanup; use the path printed at startup to inspect/remove leftovers. After a reboot this temporary swap will not reactivate automatically because no fstab entry is written.

Optional sizing, in MiB:

```sh
sudo BLOCKBOOK_SWAP_MIB=2048 BLOCKBOOK_SWAP_RESERVE_MIB=4096 \
  ./contrib/scripts/build-with-swap.sh groestlcoin
```

The disk reserve is checked before allocation, not continuously; reserve enough for any Docker image rebuilds or other concurrent disk consumers. This does not turn a low-memory host into a fast build machine.

## Testing without activating swap

From the repository root, use an unprivileged disposable container:

```sh
docker run --rm --network none --cap-drop ALL -v "$PWD:/repo:ro" \
  ubuntu:24.04 bash /repo/contrib/tests/test-build-with-swap.sh
```

The harness mocks swap and build commands and allocates only 1 MiB per scenario. It checks success, build failures, interruption, partial allocation, root/disk/filesystem/size guards, failed setup, retained swap on failed or unsafe cleanup, and concurrent execution. It never activates host or container swap.
