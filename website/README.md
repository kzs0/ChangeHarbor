# ChangeHarbor documentation site

Source for https://kzs0.github.io/ChangeHarbor/, built with
[MkDocs Material](https://squidfunk.github.io/mkdocs-material/).

```sh
python3 -m venv .venv && . .venv/bin/activate
pip install -r website/requirements.txt
mkdocs serve --config-file website/mkdocs.yml     # http://127.0.0.1:8000
```

Publishing: `.github/workflows/docs.yml` builds with `--strict` on every pull
request that touches `website/`, and deploys to GitHub Pages from `main`.
One-time setup: **Settings → Pages → Build and deployment → Source: GitHub
Actions**.

The Go test suite keeps the site honest:

- Every ` ```yaml ` block starting with `# changeharbor.yaml` must validate,
  and together they must show each rollout feature (GitHub App,
  `review.auto`, partial reviews, several repositories, ...).
- Every json block after `<!-- example: KIND [LABEL] [budget=N] -->` must
  decode with the binary's own decoders. `KIND` is `policy`, `model` or
  `health`. Model examples must use a placeholder model name
  (`review-model` or `your-...`), and `budget=N` must equal the config's
  real prompt budget. `configuration/providers.md` needs one labelled
  recipe per provider (`openai`, `azure-openai`, `anthropic`, `openrouter`,
  `litellm`, `vllm`, `ollama`).
- Backticked config keys such as `` `publish.comments` `` must exist.
- The configuration reference must cover every key, and the CLI page every
  command.
- `getting-started/what-a-review-looks-like.md` must match the comment
  renderers' wording.
- Every page must be in the navigation and linked from `docs/llms.txt`.
- No page may use the filler phrases listed in
  `internal/config/rollout_docs_test.go`, or name a real model.
