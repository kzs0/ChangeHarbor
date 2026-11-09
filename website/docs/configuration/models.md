# Models

A model config is a strict JSON file. Name it under `models` in
`changeharbor.yaml` and repositories use `default` automatically:

```yaml
# changeharbor.yaml
version: 1
models:
  default: models/review.json
  large: models/review-large.json
repositories:
  app:
    url: https://gitlab.com/group/app
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
| `capabilities` | What the model supports. `input_tokens_per_byte` is a conservative estimate used for budgeting. Optional `pricing_known` and `input_price_micros_per_million`/`output_price_micros_per_million` enable cost tracking. |
| `limits` | Hard caps per run. `timeout` is a duration such as `"3m"` or `"90s"`, or integer nanoseconds (180000000000 = 3 minutes). `max_attempts` counts transport retries of the same request. Optional `max_cost_micros` caps spend when pricing is known. |

For the Anthropic Messages API:

<!-- example: model -->
```json
{
  "api": "anthropic_messages",
  "model": "review-model",
  "api_key_env": "MODEL_API_KEY",
  "capabilities": {
    "context_tokens": 200000,
    "max_output_tokens": 16000,
    "input_tokens_per_byte": 1,
    "structured_output": true
  },
  "limits": {
    "max_request_bytes": 1048576,
    "max_response_bytes": 1048576,
    "max_output_tokens": 8000,
    "max_attempts": 2,
    "timeout": "3m",
    "max_total_tokens": 600000
  }
}
```

Schema keywords the Messages API doesn't accept (`minimum`, `maxLength` and
similar) are left out of the request and still enforced when ChangeHarbor
validates the response. A refusal or a truncated response is invalid output.
Rate-limit and overload responses (429, 503, 529) are retried within
`max_attempts`.

The largest diff a review can send is derived from these limits: the request
byte cap, the context window less `max_output_tokens`, `max_total_tokens`
split across attempts, and the cost cap. A change too large for it is not
reviewed (see `review.large_changes` in the
[reference](reference.md#review)). `reviewd review --url --max-bytes N` lowers
the budget further; with a saved `response` the budget is `--max-bytes`, or
1 MiB.

Every request, attempt and response is recorded in the state directory. If a
response is lost, ChangeHarbor never silently replays the request.

## Trying without a model

Set `response: path/to/response.json` on a repository instead of `model`, or
pass `--response`. ChangeHarbor then uses that saved response, which is useful
for trying the flow and in tests. It isn't a real review.
