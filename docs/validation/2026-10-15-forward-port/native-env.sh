#!/usr/bin/env bash
# Scratch master toolchain. Native library paths intentionally reuse preserved read-only assets.
export BB_MASTER_TOOLCHAIN_ROOT=/tmp/blockbook-master-toolchain-20261015
export PATH="$BB_MASTER_TOOLCHAIN_ROOT/bash/bin:$BB_MASTER_TOOLCHAIN_ROOT/cache/go-mod/golang.org/toolchain@v0.0.1-go1.25.4.darwin-arm64/bin:$PATH"
export GOTOOLCHAIN=local
export GOCACHE="$BB_MASTER_TOOLCHAIN_ROOT/cache/go-build"
export GOMODCACHE="$BB_MASTER_TOOLCHAIN_ROOT/cache/go-mod"
export GOENV=off
export CGO_ENABLED=1
export CGO_CFLAGS="-I/tmp/blockbook-toolchain-20261015/prefix/include"
export CGO_CXXFLAGS="$CGO_CFLAGS"
export CGO_LDFLAGS="-L/tmp/blockbook-toolchain-20261015/prefix/lib -lrocksdb -lc++ -lm -lz -lbz2 -L/opt/homebrew/opt/lz4/lib -llz4 -L/opt/homebrew/opt/zstd/lib -lzstd"
export PKG_CONFIG_PATH=/tmp/blockbook-toolchain-20261015/prefix/lib/pkgconfig
