# Component boundaries and conservative execution

Each component owns package-local request/response types. Composition maps facts
explicitly instead of introducing a mutable shared global domain model. Test
contracts precede implementation and form the executable acceptance boundary.

Local Git and fake-model commands form the runnable development path. Provider
reads use bounded HTTP adapters; unsupported publication/protection semantics
must refuse rather than claim eligibility. Shadow mode remains the default.

Go standard library handles JSON, HTTP, Git subprocesses, parsing, rendering and
CLI. SQLite and Bedrock are the only intended runtime dependencies.
