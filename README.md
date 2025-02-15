# websocket

[![Documentation](https://pkg.go.dev/badge/github.com/mccutchen/websocket)](https://pkg.go.dev/github.com/mccutchen/websocket)
[![Build status](https://github.com/mccutchen/websocket/actions/workflows/test.yaml/badge.svg)](https://github.com/mccutchen/websocket/actions/workflows/test.yaml)
[![Code coverage](https://codecov.io/gh/mccutchen/websocket/branch/main/graph/badge.svg)](https://codecov.io/gh/mccutchen/websocket)
[![Go report card](http://goreportcard.com/badge/github.com/mccutchen/websocket)](https://goreportcard.com/report/github.com/mccutchen/websocket)

A zero-dependency Golang implementation of the websocket protocol ([RFC 6455][rfc]),
originally extracted from [mccutchen/go-httpbin][].

> [!WARNING]
> **Not production ready!**

**Do not use this library in a production application.** It is not
particularly optimized and many breaking API changes are likely for the
forseeable future.

Consider one of these libraries instead:
- https://github.com/gobwas/ws
- https://github.com/gorilla/websocket
- https://github.com/lxzan/gws

## Usage

For now, see
- [Go package docs][pkgdocs] on pkg.go.dev
- Example servers in the [examples](/examples) dir

More useful usage and example docs TK.

## Testing

### Unit tests

This project's unit tests are relatively quick to run and provide decent
coverage of the implementation:

```bash
make test
```

Or generate code coverage:

```bash
make testcover
```

### Autobahn integration tests

The [crossbario/autobahn-testsuite][autobahn] project's "fuzzing client" is
also used for integration/conformance/fuzz testing.

Because these tests a) require docker and b) take 40-60s to run, they are
disabled by default.

To run the autobahn fuzzing client in its default configuration, use:

```bash
make testautobahn
```

There are a variety of options that can be enabled individually or together,
which are useful for viewing the generated HTML test report, narrowing the
set of test cases to debug a particular issue, or to enable more detailed
debug logging.

- `AUTOBAHN=1` is required to enable the Autobahn fuzzing client test suite,
  set automatically by the `make testautobahn` target.

- `CASES` narrows the set of test cases to execute (note the wildcards):

  ```bash
  make testautobahn CASES='5.7,6.12.*,9.*'
  ```

- `REPORT=1` automatically opens the resulting HTML test resport:

  ```bash
  make testautobahn REPORT=1
  ```

- `TARGET={url}` runs autobanh against an external server instead of an
  ephemeral [httptest][] server, which can be useful for, e.g.,
  capturing [pprof][] info or running [tcpdump][]:

  ```bash
  make testautobahn TARGET=http://localhost:8080/
  ```

- `DEBUG=1` enables fairly detailed debug logging via the server's built-in
  websocket lifecycle hooks:

  ```bash
  make testautobahn DEBUG=1
  ```

Putting it all together, a command like this might be used to debug a
a particular failing test case:

```bash
make testautobahn DEBUG=1 CASES=9.1.6 REPORT=1
```

## Benchmarks

🚧 _Accurate, useful benchmarks are very much a work in progress._ 🚧

### Go benchmarks

Standard Go benchmarks may be run like so:

```bash
make bench
```

### nstd/webocket-benchmarks

Basic, manual support for running the [ntsd/websocket-benchmarks][ntsd] suite
of benchmarks is documented in the [examples/benchserver][benchserver] dir.

[autobahn]: https://github.com/crossbario/autobahn-testsuite
[benchserver]: /examples/benchserver/README.md
[httptest]: https://pkg.go.dev/net/http/httptest
[mccutchen/go-httpbin]: https://github.com/mccutchen/go-httpbin
[ntsd]: https://github.com/ntsd/websocket-benchmark
[pkgdocs]: https://pkg.go.dev/github.com/mccutchen/websocket
[pprof]: https://pkg.go.dev/runtime/pprof
[rfc]: https://datatracker.ietf.org/doc/html/rfc6455
[tcpdump]: https://www.tcpdump.org/
