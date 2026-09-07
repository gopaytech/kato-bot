# kato-bot

Chat adapter (Lark + Telegram) for kato troubleshooting flows

![Version: 0.5.1](https://img.shields.io/badge/Version-0.5.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.5.1](https://img.shields.io/badge/AppVersion-0.5.1-informational?style=flat-square) [![made with Go](https://img.shields.io/badge/made%20with-Go-brightgreen)](http://golang.org) [![Github main branch build](https://img.shields.io/github/actions/workflow/status/gopaytech/kato-bot/main.yml?branch=main)](https://github.com/gopaytech/kato-bot/actions/workflows/main.yml) [![GitHub issues](https://img.shields.io/github/issues/gopaytech/kato-bot)](https://github.com/gopaytech/kato-bot/issues) [![GitHub pull requests](https://img.shields.io/github/issues-pr/gopaytech/kato-bot)](https://github.com/gopaytech/kato-bot/pulls)

> A chat adapter for [kato](https://github.com/gopaytech/kato) on **Lark and Telegram**.
> Invite the bot to a Lark group (or DM/add it on Telegram), pick a cluster, pick a
> troubleshooting UseCase, fill in the inputs, and it runs kato and posts the summary
> back — no ingress required on either platform.

## How it works

```
Lark / Telegram ──▶ kato-bot ──REST──▶ kato (in-cluster)
```

1. Message the bot → it lists the configured **clusters**. On Lark, in a **direct
   message** any text works and in a **group** you @mention the bot (e.g. `@kato start`);
   on Telegram you DM the bot or use `/kato`/@mention in a group.
2. Pick a cluster → it lists that cluster's kato UseCases.
3. Pick a UseCase → you're prompted for that UseCase's inputs.
4. Provide the inputs → it shows "running…", then the LLM summary kato produced.

The platforms differ in how steps 1-4 are presented: **Lark** drives the whole flow
through an interactive card that updates in place, posted as a **threaded reply** to
the triggering message (so each troubleshooting flow stays in its own thread), over a
WebSocket long-connection. **Telegram** uses inline-keyboard buttons for picking the
cluster and UseCase, then a short conversational Q&A (one question at a time, `/cancel`
to abort) for the inputs, editing its own message in place as the flow progresses, over
long-poll `getUpdates` (no card patch).

Access is governed entirely by chat membership — Lark group membership, or Telegram
DM/group membership; kato-bot adds no auth of its own (kato is read-only). Supports
Lark and Telegram on the same core, and both can be enabled in one deployment.

Clusters are configured via the chart's `clusters:` list (rendered into a ConfigMap the
bot reads). The bot must be able to reach each cluster's kato URL over the network —
establishing that reachability (peering, a central management cluster, or per-cluster kato
exposure) is the operator's responsibility.

## MCP server + REST proxy

kato-bot also exposes its multi-cluster aggregation programmatically on port
9090 (value `api.enabled`, default on):

- **MCP** (streamable HTTP) at `/mcp` — tools: `list_clusters`,
  `list_usecases`, `get_usecase`, `run_usecase`, `list_methods`, `run_method`,
  `list_runs`, `get_run`. Every tool takes `cluster` (from `list_clusters`).
- **REST proxy**: kato's API prefixed by cluster —
  `GET /api/v1/clusters`, then `/api/v1/clusters/{cluster}/usecases[...]`,
  `/methods[...]`, `/runs[...]` — requests and responses pass through verbatim.

No auth (same stance as kato): network reach is the boundary. Local use:

```console
kubectl -n kato port-forward svc/kato-bot 9090
claude mcp add --transport http kato http://localhost:9090/mcp
```

`run_method` needs kato ≥ the version that ships `POST /api/v1/methods/{name}/run`;
against older katos the tool surfaces kato's 404 verbatim.

## Configuration (env)

The container is configured entirely through environment variables (the Helm values
below set them on the Deployment):

| var | default | meaning |
|---|---|---|
| `LARK_APP_ID` | (required for Lark) | Lark app id |
| `LARK_APP_SECRET` | (required for Lark) | Lark app secret |
| `LARK_BASE_URL` | `https://open.larksuite.com` | open-platform base URL (`https://open.larksuite.com` international, `https://open.feishu.cn` China) |
| `TELEGRAM_BOT_TOKEN` | (none) | BotFather token; presence enables the Telegram adapter |
| `TELEGRAM_API_BASE_URL` | `https://api.telegram.org` | override for a self-hosted Bot API server |
| `TELEGRAM_POLL_TIMEOUT` | `30s` | `getUpdates` long-poll timeout |
| `TELEGRAM_ALLOWED_CHATS` | (none) | comma-separated allowlist of Telegram chat ids (groups are negative int64); empty means any chat |
| `TELEGRAM_ALLOWED_USERS` | (none) | comma-separated allowlist of Telegram user ids (positive int64); empty means any user |
| `KATO_CLUSTERS_FILE` | `/etc/kato-bot/clusters.yaml` | path to the YAML file listing clusters (name → kato URL); at least one required |
| `KATO_RUN_TIMEOUT` | `360s` | per-run client timeout |
| `LOG_LEVEL` | `info` | log verbosity (`debug`/`info`/`warn`/`error`) |
| `MAX_CONCURRENT_RUNS` | `4` | cap on in-flight kato runs before submits get a "busy" card |
| `HEALTH_ADDR` | `:8080` | address for the `/healthz` + `/readyz` probe server |
| `API_ADDR` | `:9090` | MCP + REST proxy listen address (`/mcp` + `/api/v1/clusters/...`); empty string disables |

To run locally, copy `.env.example` to `.env`, fill it in, and `set -a; source .env; set +a`
before `go run ./cmd/kato-bot` (the binary reads the environment; it does not auto-load `.env`).

## Lark app setup

- Enable bot capability; add the bot to a group (or DM it directly).
- Grant scopes: `im:message`, `im:message:send_as_bot` (send/reply/patch messages). To be
  triggered in groups via `@kato …`, the bot must receive @mentioned group messages
  (default for `im.message.receive_v1`).
- Event subscription: **Use long connection (WebSocket)**; subscribe to
  `im.message.receive_v1` and enable card callbacks (`card.action.trigger`) over the
  long connection.

## Telegram bot setup

- Create a bot with @BotFather and copy its token into `telegram.botToken` (or a
  Secret referenced by `telegram.existingSecret`) with `telegram.enabled=true`.
- DM the bot, or add it to a group. In groups, trigger it with `/kato` or by
  @mentioning it (enable group privacy off, or add it as admin, so it receives
  the trigger message). It fills use-case inputs by asking one question at a time;
  reply with each value, or send `/cancel` to abort.
- Lark and Telegram can run together in one deployment; configure either or both.

By default the Telegram bot is open — anyone who finds it (DMs it, or is in a group it's
added to) can use it. For production, restrict access with `telegram.allowedChats` and/or
`telegram.allowedUsers` (rendered as `TELEGRAM_ALLOWED_CHATS`/`TELEGRAM_ALLOWED_USERS`):
get your own user id from [@userinfobot](https://t.me/userinfobot), and find a group's
chat id the same way (add it to the group briefly) — or just check kato-bot's logs, since
every denied update is logged with its chat and user id (`telegram: denied update from
chat=... user=...`), which is the easiest way to discover the id to allowlist once you've
tried using the bot and been denied. When both lists are set, a chat/user pair must match
**both** (AND) to be allowed; when a list is empty, that dimension is unrestricted. Both
empty (the default) is fully open. Note that setting only `telegram.allowedUsers` (leaving
`allowedChats` empty) still lets an allowed user trigger the bot in any group they're a
member of, where kato's (read-only) output becomes visible to that group's other,
non-allowlisted members — so also set `allowedChats` whenever responses must not appear to
a broader group audience.

## Installing

Install from the chart sources in this repo:

```console
helm install kato-bot charts/kato-bot -n kato \
  --set lark.appId=$LARK_APP_ID \
  --set lark.appSecret=$LARK_APP_SECRET
```

To use a pre-existing Secret instead of letting the chart create one, create a Secret
with `LARK_APP_ID` and `LARK_APP_SECRET` keys and point the chart at it — `appId`/`appSecret`
are then not required:

```console
kubectl -n kato create secret generic my-lark-creds \
  --from-literal=LARK_APP_ID=$LARK_APP_ID \
  --from-literal=LARK_APP_SECRET=$LARK_APP_SECRET

helm install kato-bot charts/kato-bot -n kato --set lark.existingSecret=my-lark-creds
```

Or from the packaged chart repository:

```console
helm repo add kato-bot https://gopaytech.github.io/kato-bot/
helm install my-kato-bot kato-bot/kato-bot --values values.yaml
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for pod scheduling. |
| api.enabled | bool | `true` | Enable the MCP + REST proxy listener on port 9090 (MCP at /mcp, cluster-prefixed kato REST proxy at /api/v1/clusters/...). No auth: anyone who can reach the port can run kato on every configured cluster — keep it ClusterIP / network-restricted. |
| clusters | list | `[{"name":"default","url":"http://kato.kato.svc:8080"}]` | List of kato clusters the bot can target. Each entry needs a unique name and the in-cluster (or reachable) kato REST URL; label is the optional picker button text. Set insecureSkipVerify: true to skip TLS cert verification for an https URL (self-signed certs on a trusted network only; MITM-exposed). |
| groupRunTimeout | string | `"1800s"` | Overall timeout for one group run (Go duration). |
| groupSummary.apiKey | string | `""` | LLM API key, inline. When set (and apiKeySecretRef.name is empty), the chart renders its own dedicated Secret (<name>-groupsummary) holding the key. Ignored when apiKeySecretRef.name is set. |
| groupSummary.apiKeySecretRef | object | `{"key":"","name":""}` | Reference to an existing Secret holding the LLM API key. Preferred — keeps the AI token independent of the Lark secret, and the key can be any name. When name is set, apiKey is ignored. |
| groupSummary.baseUrl | string | `"https://api.openai.com/v1"` | OpenAI-compatible base URL. |
| groupSummary.enabled | bool | `false` | Enable the optional LLM group-summary (kato-bot's only LLM use). |
| groupSummary.maxTokens | int | `1024` | Max completion tokens. |
| groupSummary.model | string | `"gpt-4o-mini"` | Model name. |
| groupSummary.temperature | string | `"0.2"` | Sampling temperature. |
| groups | list | `[]` | Predefined groups: several usecases (each with its own targets) in one cluster. cluster must match a configured cluster. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/gopaytech/kato-bot"` | Container image repository. |
| image.tag | string | `"v0.5.1"` | Image tag. Defaults to the chart appVersion when empty. |
| katoRunTimeout | string | `"360s"` | Per-run client timeout for kato's synchronous POST /run (Go duration). |
| lark.appId | string | `""` | Lark app id. Required unless lark.existingSecret is set or only Telegram is enabled. |
| lark.appSecret | string | `""` | Lark app secret. Required unless lark.existingSecret is set. |
| lark.existingSecret | string | `""` | Name of a pre-existing Secret holding LARK_APP_ID and LARK_APP_SECRET. When set, the chart references it and does NOT create its own Secret (appId/appSecret ignored). |
| larkBaseUrl | string | `"https://open.larksuite.com"` | Lark open-platform base URL. Lark international: https://open.larksuite.com; Feishu (China): https://open.feishu.cn. |
| logLevel | string | `"info"` | Lark SDK log verbosity (debug / info / warn / error). |
| maxConcurrentRuns | int | `4` | Max in-flight kato runs before new submits get a "kato is busy" card. |
| nodeSelector | object | `{}` | Node selector for pod scheduling. |
| resources | object | `{}` | Pod resource requests and limits. |
| telegram.allowedChats | list | `[]` | Restrict which Telegram chats may use the bot (allowlist). Empty (the default) means open — any chat that can reach the bot may use it. Chat ids for groups are NEGATIVE int64 (e.g. -1001234567890); a private chat's id equals the user's id (positive). Combined with allowedUsers via AND: when both are set, a chat/user pair must satisfy both to be let through. Strongly recommended for production — find ids with @userinfobot, or read kato-bot's logs (every denied update logs its chat and user id). |
| telegram.allowedUsers | list | `[]` | Restrict which Telegram users may use the bot (allowlist). Empty (the default) means open — any user may use the bot (subject to allowedChats). User ids are always positive int64. Combined with allowedChats via AND (see above). Strongly recommended for production. |
| telegram.apiBaseUrl | string | `"https://api.telegram.org"` | Telegram Bot API base URL (override only for a self-hosted Bot API server). |
| telegram.botToken | string | `""` | Telegram bot token from BotFather. Required when telegram.enabled and no existingSecret. |
| telegram.enabled | bool | `false` | Enable the Telegram adapter. When true, a bot token is required (inline or via existingSecret). |
| telegram.existingSecret | string | `""` | Name of a pre-existing Secret holding TELEGRAM_BOT_TOKEN. When set, the chart references it and does NOT create its own Telegram Secret (botToken ignored). |
| telegram.pollTimeout | string | `"30s"` | getUpdates long-poll timeout (Go duration). |
| tolerations | list | `[]` | Tolerations for pod scheduling. |

see example files [here](https://github.com/gopaytech/kato-bot/blob/main/charts/kato-bot/values.yaml)

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.14.2](https://github.com/norwoodj/helm-docs/releases/v1.14.2)
