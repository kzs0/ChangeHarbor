# Models

A model config is a strict JSON file, a YAML file (the name ends in
`.yaml` or `.yml`), or the same fields inline. Name it under `models` in
`changeharbor.yaml` and repositories use `default` automatically:

```yaml
# changeharbor.yaml
version: 1
models:
  default: models/review.json
  large: models/review-large.yaml
repositories:
  app:
    url: https://gitlab.com/group/app
    auth:
      token_env: GITLAB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
    model: large           # overrides default for this repository
```

## Model config file

ChangeHarbor speaks two model APIs, chosen with `api`:

- `openai_chat` (default): an OpenAI-compatible chat completions endpoint
  with a JSON schema response format.
- `anthropic_messages`: the Anthropic Messages API with structured output.

It makes one request per review, with no tools.

<!-- example: model -->
```json
{
  "endpoint": "https://models.example.com/v1/chat/completions",
  "model": "review-model",
  "api_key_env": "MODEL_API_KEY",
  "capabilities": {
    "context_tokens": 272000,
    "max_output_tokens": 16000,
    "input_tokens_per_byte": 1,
    "structured_output": true
  },
  "limits": {
    "max_request_bytes": 1048576,
    "max_response_bytes": 262144,
    "max_output_tokens": 16000,
    "max_attempts": 2,
    "timeout": 180000000000,
    "max_total_tokens": 300000
  }
}
```

| Field | Meaning |
| --- | --- |
| `api` | `openai_chat` (default) or `anthropic_messages`. |
| `endpoint` | Full HTTPS URL. Required for `openai_chat`; `anthropic_messages` defaults to `https://api.anthropic.com/v1/messages`. |
| `model` | Model name sent with each request. |
| `api_key_env` | Environment variable holding the API key. |
| `api_key_header` | Header that carries the key, e.g. `api-key` for Azure OpenAI. Default `Authorization` (`openai_chat`) or `x-api-key` (`anthropic_messages`). |
| `api_key_prefix` | Word sent before the key. Default `Bearer` when the header is `Authorization`, none otherwise. `""` sends the bare key. |
| `capabilities` | What the model supports: `context_tokens`, `max_output_tokens`, `structured_output`, and `input_tokens_per_byte`, a whole-number upper bound (1 to 16) on tokens per prompt byte used for budgeting. Optional `pricing_known` with `input_price_micros_per_million` and `output_price_micros_per_million` enables cost tracking. |
| `limits` | Hard caps per run. `timeout` is a duration such as `"3m"` or `"90s"`, or integer nanoseconds (180000000000 = 3 minutes). `max_attempts` counts transport retries of the same request. Optional `max_cost_micros` caps spend when pricing is known. |

## YAML and inline configs

The same fields work in YAML, where `timeout` reads best as a duration:

```yaml
# models/review-large.yaml
api: anthropic_messages
model: your-model
api_key_env: ANTHROPIC_API_KEY
capabilities:
  context_tokens: 200000
  max_output_tokens: 8000
  input_tokens_per_byte: 1
  structured_output: true
limits:
  max_request_bytes: 400000
  max_response_bytes: 200000
  max_output_tokens: 4000
  max_attempts: 2
  timeout: 3m
  max_total_tokens: 1000000
```

Or write them inline under the model's name:

```yaml
# changeharbor.yaml
version: 1
models:
  default:
    api: anthropic_messages
    model: your-model
    api_key_env: ANTHROPIC_API_KEY
    capabilities:
      context_tokens: 200000
      max_output_tokens: 8000
      input_tokens_per_byte: 1
      structured_output: true
    limits:
      max_request_bytes: 400000
      max_response_bytes: 200000
      max_output_tokens: 4000
      max_attempts: 2
      timeout: 3m
      max_total_tokens: 1000000
repositories:
  app:
    url: https://gitlab.com/group/app
    auth:
      token_env: GITLAB_TOKEN
    mirror:
      path: /srv/mirrors/app.git
    policy: policies/app.json
```

YAML files and inline configs follow the same strict YAML rules as
`changeharbor.yaml`. Inline configs are checked when the file is loaded, and
errors name the config path, e.g. `models.default.limits.timout: unknown key;
did you mean "timeout"?`. Model files are read when a review uses them. The
model is not part of the review identity, so switching forms, or models,
leaves existing reviews valid.

Ready-made configs for OpenAI, Azure OpenAI, Anthropic, OpenRouter,
LiteLLM, vLLM and Ollama are on [Model providers](providers.md), along with
how the prompt budget is derived and what a review can cost.

A few rules the decoder enforces: unknown and duplicate keys are errors,
`structured_output` must be `true`, `limits.max_output_tokens` can't exceed
`capabilities.max_output_tokens`, `max_attempts` is 1 or 2, and the endpoint
must use `https://` unless it's on loopback. `max_cost_micros` needs
`pricing_known`.

The largest diff a review can send is derived from these limits. A change
too large for it isn't reviewed unless `review.large_changes` is `partial`
(see the [reference](reference.md#review)). `reviewd review --url URL
--max-bytes N` lowers the budget for one run; with a saved `response` the
budget is `--max-bytes`, or 1 MiB.

Every request, attempt and response is recorded in the state directory. If a
response is lost, ChangeHarbor never silently replays the request.

## Trying without a model

Set `response: path/to/response.json` on a repository instead of `model`, or
pass `--response`. ChangeHarbor then uses that saved response, which is useful
for trying the flow and in tests. It isn't a real review.
