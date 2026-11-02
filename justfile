# Override with `just go=/path/to/go check build` or the GO environment variable.
go := env("GO", "go")
export GOCACHE := env("GOCACHE", justfile_directory() + "/.cache/go-build")

default:
    @{{ just_executable() }} --list

test:
    "{{ go }}" test ./...

check:
    "{{ go }}" vet ./...
    "{{ go }}" test -race ./...

build:
    "{{ go }}" build -o bin/reviewd ./cmd/reviewd

package:
    GO="{{ go }}" scripts/package.sh
