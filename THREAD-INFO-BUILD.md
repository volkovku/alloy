# Recovered Java thread-info build

This branch contains the Alloy changes from PR #6725 at
`100cfccbc03519f6e29b58ca6bb54b4c63e108ef` and the local build and diagnostic
additions used with parser PR #116 (`b18082aa10d3e8812bdfdd886eb515fba4b4e35c`).
The parser submodule pins the published module adapter at
`8d10f996b16d2f937e2f2fe0ab966c3d0292b3b8`.

Recovered changes:

- `internal/component/pyroscope/java/{args.go,loop.go,loop_test.go}` and
  `docs/sources/reference/components/pyroscope/pyroscope.java.md`: upstream thread
  block, parser wiring, thread-name transform, tests and documentation.
- Root and collector `go.mod`: replacements for both parser modules, now relative
  to a pinned submodule instead of machine-specific temporary directories.
- `cmd/profilecapture/main.go`: local Pyroscope Push receiver used to capture pprof.
  Its output directory and listen address are now command-line options.
- `tools/thread-info-e2e/`: the Java workload, smoke config, frame-plus-label and
  label-only configs, and the E2E capture script. Paths now use a fresh temporary
  directory, and runs retain their artifacts instead of deleting an old directory.
- `build-thread-info.sh`: the static Linux amd64 build recipe with the original
  source version and toolchain. UI regeneration and collector code generation are
  skipped; this does not rebuild or validate the web UI.

Build from a checkout with Git, Go, Make and sufficient cache space:

```sh
git submodule update --init
bash build-thread-info.sh
```

Tests:

```sh
(cd third_party/thread-info-jfr-parser && make test GO_FLAGS='-count=1 -mod=readonly')
go test -mod=mod -p=2 -count=1 ./internal/component/pyroscope/java ./internal/component/pyroscope/java/asprof
go test -mod=mod ./cmd/profilecapture
```

The separate `internal/component/pyroscope/java/integration` package requires
Docker access; the `nodocker` tag does not disable that package.

For live capture, install a JDK with `java` and `javac`, ensure local port 14040
is free and no other `AlloyThreadE2E` JVM is running, then run:

```sh
bash tools/thread-info-e2e/run.sh
```

The script requires profiles in both modes. It does not automatically assert
their thread labels or frame contents; inspect the captured pprof files.
The thread-info patch covers CPU execution samples, not allocation/lock threads.

The restored tooling was prepared with AI assistance from the original session
log. Generated binaries, build caches, captured profiles and private deployment
configuration are not source changes and are not committed here.
