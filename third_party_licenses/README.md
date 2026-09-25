# Third-party licenses

`op-sa-secret-driver` is a static binary that links the following open-source
packages. All are permissively licensed and compatible with this project's
MIT license (see [`LICENSE`](../LICENSE)). Full license (and, where present,
`NOTICE`) files are kept in the matching subdirectory here.

| Package | License |
|---|---|
| [github.com/1password/onepassword-sdk-go](https://github.com/1password/onepassword-sdk-go) | MIT |
| [github.com/dylibso/observe-sdk/go](https://github.com/dylibso/observe-sdk) | Apache-2.0 |
| [github.com/extism/go-sdk](https://github.com/extism/go-sdk) | BSD-3-Clause |
| [github.com/gobwas/glob](https://github.com/gobwas/glob) | MIT |
| [github.com/ianlancetaylor/demangle](https://github.com/ianlancetaylor/demangle) | BSD-3-Clause |
| [github.com/tetratelabs/wabin](https://github.com/tetratelabs/wabin) | Apache-2.0 |
| [github.com/tetratelabs/wazero](https://github.com/tetratelabs/wazero) | Apache-2.0 |
| [go.opentelemetry.io/proto/otlp](https://github.com/open-telemetry/opentelemetry-proto-go) | Apache-2.0 |
| [golang.org/x/sys](https://cs.opensource.google/go/x/sys) | BSD-3-Clause |
| [google.golang.org/protobuf](https://github.com/protocolbuffers/protobuf-go) | BSD-3-Clause |

These are the transitive dependencies of the 1Password Go SDK; this project
has no other direct dependencies. Generated with
[`google/go-licenses`](https://github.com/google/go-licenses)
(`go-licenses save . --save_path=third_party_licenses`); regenerate after
changing dependencies.
