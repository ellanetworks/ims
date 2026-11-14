# Pprof

These paths serve the [net/http/pprof](https://pkg.go.dev/net/http/pprof) API. They require Admin, and accept an API token.

## Profiling Endpoints

| Method    | Path                          | Description                                              |
| --------- | ----------------------------- | -------------------------------------------------------- |
| GET       | `/api/v1/pprof/`              | An HTML page listing the available profiles.             |
| GET       | `/api/v1/pprof/allocs`        | A sampling of all past memory allocations.               |
| GET       | `/api/v1/pprof/cmdline`       | The command line invocation of the program.              |
| GET       | `/api/v1/pprof/goroutine`     | Stack traces of all current goroutines.                  |
| GET       | `/api/v1/pprof/goroutineleak` | Stack traces of all leaked goroutines.                   |
| GET       | `/api/v1/pprof/heap`          | A sampling of memory allocations of live objects.        |
| GET       | `/api/v1/pprof/profile`       | A CPU profile, of 30 seconds by default.                 |
| GET       | `/api/v1/pprof/threadcreate`  | Stack traces that led to the creation of new OS threads. |
| GET       | `/api/v1/pprof/trace`         | An execution trace, of 1 second by default.              |
| GET, POST | `/api/v1/pprof/symbol`        | Function names of program counters.                      |

### Query Parameters

| Parameter | Paths                                         | Description                                                                         |
| --------- | --------------------------------------------- | ----------------------------------------------------------------------------------- |
| `seconds` | `profile`, `trace`                            | Duration of the capture.                                                            |
| `seconds` | `allocs`, `goroutine`, `heap`                 | Returns the difference of the profile over this duration.                           |
| `debug`   | `allocs`, `goroutine`, `heap`, `threadcreate` | `0`: protobuf. `1`: text. `2` (`goroutine`): stack traces in the format of a panic. |
| `gc`      | `heap`                                        | `1`: runs a garbage collection first.                                               |
