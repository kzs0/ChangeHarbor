# Model providers

Each recipe below is a complete [model config](models.md) that the test suite
decodes with the binary's own decoder. Copy one to `models/review.json`,
replace `your-model` with the model name your provider documents, and set the
`capabilities` to what that model actually supports. ChangeHarbor doesn't
look any of this up, so the numbers are yours to get right.

The model has to support JSON-schema structured output. `openai_chat` asks
for it with `response_format: {"type": "json_schema", ...}` (and caps the
reply with `max_completion_tokens`); `anthropic_messages` uses
`output_config.format`. The config won't load without
`"structured_output": true`, so a model that can't do this can't be used.

The reply also has to finish normally: `finish_reason: "stop"` for
`openai_chat`, `stop_reason: "end_turn"` for `anthropic_messages`. Anything
else, running out of output tokens included, counts as truncated, and the
review comes back as `invalid_output`.

The endpoint must be `https://`. Plain `http://` is accepted only for
`localhost`, `127.0.0.1` and `::1`. Query strings aren't allowed in the
endpoint.

## OpenAI

<!-- example: model openai budget=191130 -->
```json
{
  "endpoint": "https://api.openai.com/v1/chat/completions",
  "model": "your-model",
  "api_key_env": "OPENAI_API_KEY",
  "capabilities": {
    "context_tokens": 400000,
    "max_output_tokens": 32000,
    "input_tokens_per_byte": 1,
    "structured_output": true
  },
  "limits": {
    "max_request_bytes": 4194304,
    "max_response_bytes": 1048576,
    "max_output_tokens": 16000,
    "max_attempts": 2,
    "timeout": "4m",
    "max_total_tokens": 800000
  }
}
```

The key is sent as `Authorization: Bearer $OPENAI_API_KEY`.

## Azure OpenAI

Use the v1 API path, which needs no `api-version` query parameter. `model` is
your **deployment name**, and the key goes in the `api-key` header with no
prefix:

<!-- example: model azure-openai budget=55128 -->
```json
{
  "endpoint": "https://YOUR-RESOURCE.openai.azure.com/openai/v1/chat/completions",
  "model": "your-deployment",
  "api_key_env": "AZURE_OPENAI_API_KEY",
  "api_key_header": "api-key",
  "capabilities": {
    "context_tokens": 128000,
    "max_output_tokens": 16000,
    "input_tokens_per_byte": 1,
    "structured_output": true
  },
  "limits": {
    "max_request_bytes": 1048576,
    "max_response_bytes": 1048576,
    "max_output_tokens": 16000,
    "max_attempts": 2,
    "timeout": "4m",
    "max_total_tokens": 256000
  }
}
```

The older `.../openai/deployments/NAME/chat/completions?api-version=...`
URLs don't work, because the endpoint can't carry a query string.

## Anthropic

`anthropic_messages` talks to the Messages API directly. The endpoint
defaults to `https://api.anthropic.com/v1/messages` and the key goes in
`x-api-key`. This recipe also turns on cost tracking and a cost cap; the
prices are examples, so put in your provider's current prices:

<!-- example: model anthropic budget=91191 -->
```json
{
  "api": "anthropic_messages",
  "model": "your-model",
  "api_key_env": "ANTHROPIC_API_KEY",
  "capabilities": {
    "context_tokens": 200000,
    "max_output_tokens": 32000,
    "input_tokens_per_byte": 1,
    "structured_output": true,
    "pricing_known": true,
    "input_price_micros_per_million": 3000000,
    "output_price_micros_per_million": 15000000
  },
  "limits": {
    "max_request_bytes": 1048576,
    "max_response_bytes": 1048576,
    "max_output_tokens": 16000,
    "max_attempts": 2,
    "timeout": "4m",
    "max_total_tokens": 400000,
    "max_cost_micros": 2000000
  }
}
```

Schema keywords the Messages API rejects (`minimum`, `maxLength` and the
like) are stripped from the request, and ChangeHarbor still enforces them
when it validates the reply. 429, 503 and 529 responses are retried within
`max_attempts`.

## OpenRouter

OpenRouter is OpenAI-compatible. Its model names look like `vendor/name`;
use the one from the model's OpenRouter page, and pick a model that lists
structured outputs as supported:

<!-- example: model openrouter budget=55130 -->
```json
{
  "endpoint": "https://openrouter.ai/api/v1/chat/completions",
  "model": "your-model",
  "api_key_env": "OPENROUTER_API_KEY",
  "capabilities": {
    "context_tokens": 128000,
    "max_output_tokens": 16000,
    "input_tokens_per_byte": 1,
    "structured_output": true
  },
  "limits": {
    "max_request_bytes": 1048576,
    "max_response_bytes": 1048576,
    "max_output_tokens": 16000,
    "max_attempts": 2,
    "timeout": "4m",
    "max_total_tokens": 256000
  }
}
```

## LiteLLM proxy

A [LiteLLM](https://docs.litellm.ai/) proxy gives you one OpenAI-compatible
endpoint in front of several providers. `model` is the `model_name` from the
proxy's config, and the key is a LiteLLM virtual key:

<!-- example: model litellm budget=55130 -->
```json
{
  "endpoint": "https://litellm.internal.example.com/v1/chat/completions",
  "model": "your-model",
  "api_key_env": "LITELLM_API_KEY",
  "capabilities": {
    "context_tokens": 128000,
    "max_output_tokens": 16000,
    "input_tokens_per_byte": 1,
    "structured_output": true
  },
  "limits": {
    "max_request_bytes": 1048576,
    "max_response_bytes": 1048576,
    "max_output_tokens": 16000,
    "max_attempts": 2,
    "timeout": "4m",
    "max_total_tokens": 256000
  }
}
```

Set `capabilities` for the model the proxy routes to, not for the proxy.

## Local models: vLLM and Ollama

Both serve an OpenAI-compatible endpoint. Run them on the same host (or in
the same network namespace, see [Containers](../guides/container.md)) so
plain HTTP on loopback is allowed; anywhere else, put TLS in front. Neither
needs a key, so `api_key_env` is left out.

vLLM, started with `vllm serve your-model --max-model-len 32768`:

<!-- example: model vllm budget=11419 -->
```json
{
  "endpoint": "http://127.0.0.1:8000/v1/chat/completions",
  "model": "your-model",
  "capabilities": {
    "context_tokens": 32768,
    "max_output_tokens": 8192,
    "input_tokens_per_byte": 1,
    "structured_output": true
  },
  "limits": {
    "max_request_bytes": 262144,
    "max_response_bytes": 262144,
    "max_output_tokens": 8192,
    "max_attempts": 1,
    "timeout": "4m",
    "max_total_tokens": 32768
  }
}
```

Ollama, with the model's context window raised to match `context_tokens`
(its default is much smaller, and it truncates the prompt silently):

<!-- example: model ollama budget=11419 -->
```json
{
  "endpoint": "http://127.0.0.1:11434/v1/chat/completions",
  "model": "your-model",
  "capabilities": {
    "context_tokens": 32768,
    "max_output_tokens": 8192,
    "input_tokens_per_byte": 1,
    "structured_output": true
  },
  "limits": {
    "max_request_bytes": 262144,
    "max_response_bytes": 262144,
    "max_output_tokens": 8192,
    "max_attempts": 1,
    "timeout": "4m",
    "max_total_tokens": 32768
  }
}
```

Small local models often return replies that fail validation. Try one in
[shadow mode](../guides/rollout.md) on a few real changes before you rely on
it.

## How big a change fits

ChangeHarbor packs the diff, your review instructions and related source
into one prompt. The diff is required; related source is dropped first when
space runs out. The largest prompt it will build is derived from the model
config, so every request it sends passes every cap:

```text
tokens = min(context_tokens, max_total_tokens / max_attempts) - limits.max_output_tokens
bytes  = min(max_request_bytes, tokens / input_tokens_per_byte)
         and, with a cost cap, no more than the cap allows per attempt
budget = (bytes - size of an empty request) / 2
```

Halving the result leaves room to JSON-escape the prompt into the request
body. `input_tokens_per_byte` is a whole number, at least 1, so ChangeHarbor
budgets at least one token per byte of prompt. Source code usually comes out
to fewer tokens than bytes, so the estimate errs on the safe side and a
128,000-token window holds a prompt of about 55 KB.

The test suite recomputes the budget for every recipe on this page. These
are the results:

| Recipe | Context | Prompt budget |
| --- | --- | --- |
| OpenAI | 400,000 tokens | 191,130 bytes |
| Anthropic | 200,000 tokens | 91,191 bytes |
| Azure OpenAI, OpenRouter, LiteLLM | 128,000 tokens | about 55,130 bytes |
| vLLM, Ollama | 32,768 tokens | 11,419 bytes |

At the example prices, the Anthropic recipe's cost cap would allow a larger
prompt than its context window does, so the window is the limit there. A
tighter `max_cost_micros` would take over.

To review bigger changes, raise `context_tokens` and `max_total_tokens` for
a model with a bigger window, leave generated files out with
`review.exclude`, or set `review.large_changes: partial` to review the files
that fit. `reviewd review --url URL --max-bytes N` lowers the budget for one
run, which is a quick way to see what a smaller model would do.

## What a review costs

Tokens are reserved for every permitted attempt before the request is sent,
so the worst case per review is:

- tokens: `max_total_tokens`, across all attempts;
- money (with `pricing_known`): `max_cost_micros`, in millionths of your
  billing currency. The prices are per million tokens, also in micros:
  `3000000` means 3.00 per million tokens.

With `max_attempts: 2` a reply is retried only when the provider explicitly
rejects the request (429, 503, and 529 for Anthropic). A request whose
outcome is unknown, such as a timeout, is never replayed. Each completed
request, attempt and usage figure is stored in the state directory, and
re-reviewing an unchanged commit reuses the stored reply instead of paying
again.

The service gives each job five minutes in total, so keep `timeout` under
that.
