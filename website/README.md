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

- Every ` ```yaml ` block starting with `# changeharbor.yaml` must validate.
- Every block after `<!-- example: policy -->` or `<!-- example: model -->`
  must decode with the binary's own decoders.
- The configuration reference must cover every key.
- The CLI page must cover every command.
- Every page must be in the navigation.
