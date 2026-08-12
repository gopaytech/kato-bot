# kato-bot

Lark chat adapter for kato troubleshooting flows

![Version: 0.4.1](https://img.shields.io/badge/Version-0.4.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 0.4.1](https://img.shields.io/badge/AppVersion-0.4.1-informational?style=flat-square) [![made with Go](https://img.shields.io/badge/made%20with-Go-brightgreen)](http://golang.org) [![Github main branch build](https://img.shields.io/github/actions/workflow/status/zufardhiyaulhaq/kato-bot/main.yml?branch=main)](https://github.com/zufardhiyaulhaq/kato-bot/actions/workflows/main.yml) [![GitHub issues](https://img.shields.io/github/issues/zufardhiyaulhaq/kato-bot)](https://github.com/zufardhiyaulhaq/kato-bot/issues) [![GitHub pull requests](https://img.shields.io/github/issues-pr/zufardhiyaulhaq/kato-bot)](https://github.com/zufardhiyaulhaq/kato-bot/pulls)

> A Lark chat adapter for [kato](https://github.com/zufardhiyaulhaq/kato). Invite the bot
> to a Lark group, message it, pick a troubleshooting UseCase, fill in the inputs, and it
> runs kato and posts the summary back — all over a WebSocket long-connection (no ingress).

## How it works

```
Lark group ──ws──> kato-bot ──REST──> kato (in-cluster)
```

1. Message the bot → it shows a card listing the configured **clusters**. In a **direct
   message** any text works; in a **group** @mention the bot (e.g. `@kato start`).
2. Pick a cluster → the card lists that cluster's kato UseCases.
3. Pick a UseCase → the card becomes a form of that UseCase's inputs.
4. Submit → the card shows "running…", then the LLM summary kato produced.

Cards are posted as a **threaded reply** to the triggering message, so each
troubleshooting flow stays in its own thread and keeps the channel tidy.

Access is governed entirely by Lark group membership; kato-bot adds no auth (kato is
read-only). v1 supports Lark; Slack and Telegram are planned on the same core.

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
| `LARK_APP_ID` | (required) | Lark app id |
| `LARK_APP_SECRET` | (required) | Lark app secret |
| `LARK_BASE_URL` | `https://open.larksuite.com` | open-platform base URL (`https://open.larksuite.com` international, `https://open.feishu.cn` China) |
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
helm repo add kato-bot https://zufardhiyaulhaq.com/kato-bot/charts/releases/
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
| image.repository | string | `"ghcr.io/zufardhiyaulhaq/kato-bot"` | Container image repository. |
| image.tag | string | `"v0.4.1"` | Image tag. Defaults to the chart appVersion when empty. |
| katoRunTimeout | string | `"360s"` | Per-run client timeout for kato's synchronous POST /run (Go duration). |
| lark.appId | string | `""` | Lark app id. Required unless lark.existingSecret is set. |
| lark.appSecret | string | `""` | Lark app secret. Required unless lark.existingSecret is set. |
| lark.existingSecret | string | `""` | Name of a pre-existing Secret holding LARK_APP_ID and LARK_APP_SECRET. When set, the chart references it and does NOT create its own Secret (appId/appSecret ignored). |
| larkBaseUrl | string | `"https://open.larksuite.com"` | Lark open-platform base URL. Lark international: https://open.larksuite.com; Feishu (China): https://open.feishu.cn. |
| logLevel | string | `"info"` | Lark SDK log verbosity (debug / info / warn / error). |
| maxConcurrentRuns | int | `4` | Max in-flight kato runs before new submits get a "kato is busy" card. |
| nodeSelector | object | `{}` | Node selector for pod scheduling. |
| resources | object | `{}` | Pod resource requests and limits. |
| tolerations | list | `[]` | Tolerations for pod scheduling. |

see example files [here](https://github.com/zufardhiyaulhaq/kato-bot/blob/main/charts/kato-bot/values.yaml)

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.14.2](https://github.com/norwoodj/helm-docs/releases/v1.14.2)
