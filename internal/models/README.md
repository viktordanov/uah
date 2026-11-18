<!-- memoria:section id="overview" files="model.go codex.go bundled.json" -->
# Model catalog

<!-- memoria:export id="summary" -->
uah asks the provider which models the login can use, as Codex does: the list comes from the provider at runtime, is cached for five minutes with its ETag, and falls back to Codex's bundled catalog only when the provider cannot be asked. A new model therefore needs no uah release. When the provider's list is at hand, a model it lacks is refused with the nearest names: by `/model`, by `spawn_agent`, and as a warning in `uah doctor`; `-m` at start and the configuration's `model` are not checked: the model goes to the provider as written.
<!-- /memoria:export -->

This package holds the catalog types, one source per provider, the file cache, and the did-you-mean suggestions. The `/model` menu and picker, the `/model` check, the context window, `uah doctor`, `uah models`, and `-m` completion read it. The facts about Codex were checked against Codex `rust-v0.156.1`, the bundled catalog against `rust-v0.159.1`, and the facts about the runner against unreal-agent v0.1.1.

1. [Models and catalogs](#models-and-catalogs)
2. [Sources](#sources)
3. [The cache and the refresh](#the-cache-and-the-refresh)
4. [Checking a model](#checking-a-model)
5. [Extension points](#extension-points)

A `Model` keeps the Codex fields that uah uses: the ID and display name, the context window and its maximum, the reasoning levels with their descriptions and the default effort, the service tiers (`priority` is uah's `/fast`), the priority order, whether it is hidden, the ChatGPT plans, the minimal Codex client version, the apply_patch tool type, whether the model supports verbosity with its default (Codex's `support_verbosity` and `default_verbosity`), and whether it takes effort updates (Codex's `supports_reasoning_effort_updates`). A `Catalog` is one provider's list and its `Origin`:

| Origin | Meaning | Can reject a model |
| --- | --- | --- |
| `live` | The provider returned it in this process | yes |
| `cached` | The provider returned it earlier; read from the cache | yes |
| `bundled` | `bundled.json`, from Codex's `models-manager/models.json` | no |
| `none` | No list is known for the provider | no |

`bundled.json` keeps Codex's shape and only the fields above, so the ChatGPT backend's answer and the bundled file share one parser. It describes OpenAI's models, so only openai-codex and openai fall back to it.
<!-- /memoria:section -->

<!-- memoria:section id="models" files="model.go suggest.go" -->
## Models and catalogs

Lists are ordered by Codex's `priority`, lowest first; a model without a priority comes after the others. Hidden models (Codex's visibility `hide`) are accepted but menus do not offer them.

`Catalog.Metadata` finds the entry whose metadata applies to an ID in Codex's order: the exact ID, then the longest ID that starts it (`gpt-5.5-2026-01-01` → `gpt-5.5`), then the same after one simple namespace (`openai/gpt-5.5` → `gpt-5.5`). `Window` uses it: the session provider's last catalog first, then the bundled one. `compaction.ContextWindow` is the one function every caller uses for a context window: `model_context_window` when it is set, then the catalog the caller passes (a `Manager.Window`), then 272,000 tokens. There is no process-wide catalog: `app.Setup` builds one per session (`Result.Models`) and hands it to the embedded engine, the subagents' model check, and the TUI; `uah config` uses the shipped catalog alone (`BundledWindow`), and the cached list at any age (`Cached`) for the default model.

`Catalog.Verbosity` picks a request's `text.verbosity` as Codex's `ModelClient` does (`core/src/client.rs`): a model whose entry has `support_verbosity` gets `model_verbosity` when it is set, else the entry's `default_verbosity`; any other model, or one no entry describes, gets none, and a `model_verbosity` set for it is reported as ignored (Codex warns). `Manager.Verbosity` answers from the provider's last list, else the bundled one, as `ApplyPatch` does. Codex's catalog at rust-v0.159.1 marks every model as supporting verbosity, with `low` as the default except `gpt-daybreak-red-latest` (`high`).

`Manager.EffortUpdates` says whether a request's effort can change with a `configuration_update` item in the history ([effort updates](../engine/README.md#adaptive-effort)), as Codex's `reasoning_effort_override_enabled` (`core/src/client.rs`): on openai or openai-codex, for a model whose entry has `supports_reasoning_effort_updates`, from the provider's last list, else the bundled one. The bundled flags come from later lists: Codex's models.json (gpt-6-astra and gpt-6.1-sol) and the ChatGPT backend's (gpt-6-sol and gpt-6-luna); the gpt-5 models have none.

`Suggest` returns up to three near misses: the same words in another order first (`gpt-luna-6` → `gpt-6-luna`), then an edit distance within a third of the ID's length.
<!-- /memoria:section -->

<!-- memoria:section id="sources" files="source.go providers.go" -->
## Sources

A `Source` lists one provider's models for one login. `NewSource` builds it from the engine's configuration: the base URL (`--base-url`, `UAH_LLM_BASE_URL`), then `UAH_LLM_API_KEY`, then the provider's key variable. A test keeps `Endpoints` equal to the engine's provider table.

| Provider | Request | Notes |
| --- | --- | --- |
| openai-codex | `GET {base}/models?client_version=0.169.0` | The ChatGPT backend, as Codex asks it for a ChatGPT login. It sends the engine's headers: `Authorization: Bearer`, `ChatGPT-Account-ID`, `originator` and `User-Agent` set to `uah-core`. The credentials come from `internal/engine/codexauth`, as for the engine: its transport sets them on each request and renews them once after a 401. The base URL must be the backend or a loopback address. |
| openai | `GET {base}/models` (`/v1/models`) | Bearer key. The list has no metadata, so the bundled catalog fills it for known IDs, as Codex merges a list over its bundled models. |
| openrouter | `GET {base}/models` (`/api/v1/models`) | Reads `context_length`. The key is optional because the list is public. |
| fireworks | `GET {base}/models` (`/inference/v1/models`) | Bearer key. Reads `context_length` when the list has it. |
| ollama | `GET {host}/api/tags` | The models pulled locally. The base URL loses its `/v1`. |

The Codex catalog is also the authority for `/speed ultrafast`: the exact model must advertise the tier for this login. For a workspace or unknown plan, uah reads the authenticated `/wham/config/bundle` requirements before offering it; a failed policy check removes Ultrafast but leaves the other speeds available. The cache identity includes the reported plan and client version.

`client_version` is pinned independently of the bundled catalog, because the backend can gate new models on it; Codex sends its own version (`client_version_to_whole`). The bundled catalog remains from rust-v0.159.1. No request follows a redirect, so credentials go only to the configured host. The ETag comes from the response header.
<!-- /memoria:section -->

<!-- memoria:section id="cache" files="cache.go manager.go" -->
## The cache and the refresh

`Manager.Catalog` follows Codex's `RefreshStrategy`:

- `OnlineIfUncached` uses a fresh cache, else it asks the provider. The TUI, `spawn_agent` validation, and session setup when no model is named (to pick the default, as Codex does) use it.
- `Online` always asks the provider. `uah doctor` and `uah models --refresh` use it.
- `Offline` never asks. It reads the cache at any age, else the bundled list. Session setup with a named model uses it, so that session does not wait on the network.

The cache is one file per provider, `<state-dir>/models/<provider>.json`, written through a temporary file and a rename. A file of another version is a miss; version 2 added the models' verbosity, and version 3 `supports_reasoning_effort_updates`. An entry is fresh for 300 seconds (Codex's `DEFAULT_MODEL_CACHE_TTL`). Each entry keeps the ETag, and a stale entry is revalidated with `If-None-Match`; a `304` renews it. An entry holds an identity: a SHA-256 of the provider, the base URL, and the account ID or API key. Another login therefore misses the cache, and the file holds no secret, as Codex scopes its cache by provider and auth. A refresh is bounded to five seconds (Codex's refresh deadline). When it fails, the manager returns the cache at any age, else the bundled list, with `Err` set. One refresh runs at a time for each provider.

Shell completion calls `Cached`. It reads the provider's file without reading credentials or calling the network, else it uses the bundled list.
<!-- /memoria:section -->

<!-- memoria:section id="calls" files="manager.go" -->
## Checking a model

Only a list from the provider (`live` or `cached`, not empty) can refuse a model. With the bundled list or no list, any model passes as before, so a model newer than uah still works.

- `Catalog.Check` gives the `/model` wording: "gpt-luna-6 is not available on openai-codex; did you mean gpt-6-luna?"
- `Manager.Validate(ctx, provider, model)` is for `spawn_agent`. It uses `OnlineIfUncached` and gives Codex's wording plus the near misses: "Unknown model \`gpt-luna-6\` for spawn_agent. Available models: gpt-6-astra, gpt-6-sol, gpt-6-luna, gpt-5.6-sol, gpt-5.6-terra. Did you mean \`gpt-6-luna\`?"

Both return an `*UnavailableError` that matches `ErrUnavailable`, with the suggestions and the first five visible models as fields.
<!-- /memoria:section -->

<!-- memoria:section id="extending" files="source.go modelstest/modelstest.go" -->
## Extension points

- A new provider needs an `Endpoints` entry, a case in `NewSource`, and a parser for its list; the parity test fails until the engine's provider table agrees.
- Tests use `modelstest`: a fixed `Source` and a manager with a temporary cache, so they never call the network.
<!-- /memoria:section -->
