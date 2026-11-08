# Install

ChangeHarbor is a single binary, `reviewd`.

## Requirements

- Go 1.27.3 (the version in `go.mod`)
- `git`
- A C compiler (SQLite is built with CGO)
- [`just`](https://github.com/casey/just) (optional; runs the same commands as CI)

## Build

```sh
git clone https://github.com/kzs0/ChangeHarbor.git
cd ChangeHarbor
just check build        # vet, race tests, then build bin/reviewd
```

Without `just`:

```sh
CGO_ENABLED=1 go build -o bin/reviewd ./cmd/reviewd
```

Put `bin/reviewd` on your `PATH`, or call it by path. The guides write
`reviewd`.

## Check it works

```sh
reviewd evaluate --fixtures testdata/evaluation.json
```

This replays the offline fixtures with no network access and exits `0`.

## Packaging

`just package` builds a release archive with the binary, licenses and
documentation for deployment hosts.
