#!/usr/bin/env bash
set -e

if [ $# -lt 2 ]; then
    echo "Missing arugments" 1>&2
    echo "Usage: $(basename $0) <backend|blockbook|all> <coin> [build opts]" 1>&2
    exit 1
fi

package=$1
coin=$2
shift 2

mkdir -p build
cp -r /src/build/templates build
cp -r /src/build/scripts build
cp -r /src/configs .
mkdir -p /go/src/github.com/trezor/blockbook/build && cp -r /src/build/tools /go/src/github.com/trezor/blockbook/build/tools
go env -w GO111MODULE=off
go run build/templates/generate.go $coin
go env -w GO111MODULE=auto

# backend
if ([ $package = "backend" ] || [ $package = "all" ]) && [ -d build/pkg-defs/backend ]; then
    (cd build/pkg-defs/backend && dpkg-buildpackage -b -us -uc $@)
fi

# blockbook
if ([ $package = "blockbook" ] || [ $package = "all" ]) && [ -d build/pkg-defs/blockbook ]; then
    if [ -z "${GITCOMMIT:-}" ]; then
        GITCOMMIT=$(git -c safe.directory=/src -C /src rev-parse HEAD)
        if [ -n "$(git -c safe.directory=/src -C /src status --porcelain --untracked-files=normal)" ]; then
            GITCOMMIT="${GITCOMMIT}-dirty"
        fi
    fi
    export GITCOMMIT
    export VERSION=$(cd build/pkg-defs/blockbook && dpkg-parsechangelog | sed -rne 's/^Version: ([0-9.]+)([-+~].+)?$/\1/p')

    cp Makefile ldb sst_dump build/pkg-defs/blockbook
    cp /src/openapi.yaml build/pkg-defs/blockbook
    cp -r /src/static build/pkg-defs/blockbook
    mkdir build/pkg-defs/blockbook/cert && cp /src/server/testcert.* build/pkg-defs/blockbook/cert
    (cd build/pkg-defs/blockbook && dpkg-buildpackage -b -us -uc $@)
fi

# copy packages
mv build/pkg-defs/*.deb /out
chown $PACKAGER /out/*.deb
