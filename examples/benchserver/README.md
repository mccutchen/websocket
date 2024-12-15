# benchserver

A websocket server that implements each of the [test methods][1] exercised by
the [ntsd/websocket-benchmark][2] project.

To run benchmarks:
 1. Install [k6] (e.g. `brew install k6`)
 2. Clone [ntsd/websocket-benchmark][2] somewhere
 3. Build and run this benchserver example: `make && ./bin/benchserver`
 4. Run `make benchmark-scenarios` from websocket-benchmark repo

[1]: https://github.com/ntsd/websocket-benchmark/blob/master/README.md#test-methods
[2]: https://github.com/ntsd/websocket-benchmark
[3]: https://k6.io/
