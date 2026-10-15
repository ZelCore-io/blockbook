#!/usr/bin/env bash
# Source this file before native build/test commands. All build assets are scratch-local.
export BB_TOOLCHAIN_ROOT=/tmp/blockbook-toolchain-20261015
export GOTOOLCHAIN=go1.23.7
export GOCACHE="$BB_TOOLCHAIN_ROOT/cache/go-build"
export GOMODCACHE="$BB_TOOLCHAIN_ROOT/cache/go-mod"
export GOENV=off
export CGO_ENABLED=1
export CGO_CFLAGS="-I$BB_TOOLCHAIN_ROOT/prefix/include"
export CGO_CXXFLAGS="-I$BB_TOOLCHAIN_ROOT/prefix/include"
export CGO_LDFLAGS="-L$BB_TOOLCHAIN_ROOT/prefix/lib -lrocksdb -lc++ -lm -lz -lbz2 -L/opt/homebrew/opt/lz4/lib -llz4 -L/opt/homebrew/opt/zstd/lib -lzstd"
export PKG_CONFIG_PATH="$BB_TOOLCHAIN_ROOT/prefix/lib/pkgconfig"
