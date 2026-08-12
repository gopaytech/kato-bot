{% raw %}
# kato-bot Cluster Picker — `select_static` Dropdown — Design

**Status:** Approved (design)

**Goal:** Replace the one-button-per-cluster Lark cluster picker with a single
`select_static` dropdown so the card renders regardless of how many clusters are
configured.

---

## Problem

The Lark cluster picker (`buildClusterPickerCard`) emits **three body elements per
cluster** — an `hr`, a markdown label, and a `Select ▸` callback button. At ~33
clusters the card carries ~100 elements and Lark rejects it:

```
lark reply FAILED code=230099 msg=Failed to create card content,
ext=ErrCode: 200800; ErrMsg: create universal card fail
handle core.ListClusters: lark reply: ... (code 230099)
```

Removing a few clusters brings the element count back under Lark's ceiling and it
renders again — confirming the failure is card size / interactive-element count,
not any logic bug. The failure is **presentation-only**: the REST and MCP
`ListClusters` paths return JSON/data and are unaffected.

## Approach

Render every cluster as an **option inside one `select_static` component**, plus a
single submit button — instead of one interactive button per cluster. This
collapses ~3×N body elements into **two components**, flat regardless of cluster
count.

Two properties make this the right fix:

- **Search comes for free.** The Lark client provides client-side typeahead
  filtering on `select_static`: the user types into the closed dropdown and the
  option list filters locally (verified against a working "Create a ticket" card —
  typing `cd` filtered a long category list to three matches). No server-side
  search, no extra intent, no filter code.
- **Payload shrinks.** Each option is just `{text, value}` — no per-cluster
  buttons. A single dropdown of 200 options is a far smaller, simpler payload than
  today's 33 clusters × 3 interactive elements, so this scales well past the
  ~100-option figure that community lore cites (the v2 docs state no hard cap).

The single-usecase → group flow is otherwise **unchanged**: `@bot` → cluster
dropdown → pick → the existing use-case / group picker.

## Component behavior (Lark)

The cluster picker becomes a Card JSON 2.0 **form** (the same `schema: "2.0"` +
`form` container already used by `buildFormCard`), containing:

1. A header markdown line: `☸️ **kato** — pick a cluster`.
2. One `select_static`:
   - `name: "cluster"`
   - `placeholder: { tag: "plain_text", content: "Select a cluster…" }`
   - `options`: one entry per cluster —
     `{ text: { tag: "plain_text", content: <label> }, value: <name> }`,
     where `<label>` is `cl.Label` (falling back to `cl.Name` when empty) and
     `<value>` is always `cl.Name`.
3. One submit **`Select ▸`** button:
   - `tag: "button"`, `type: "primary"`
   - `form_action_type: "submit"`, `name: "submit"`
   - `behaviors: [{ type: "callback", value: { action: "pick_cluster" } }]`

The button's `value` carries **only** `{action: "pick_cluster"}` — the chosen
cluster is **not** in `value`; it arrives in the form's `form_value`, keyed by the
select's `name` (`"cluster"`). This matches the v2 docs: a select nested in a form
delivers its selection through `form_value`, identified by the component `name`.

## Decode change

`decodeCardAction` currently reads the cluster from `p.Action.Value["cluster"]`
for every action. For `pick_cluster`, the cluster now lives in
`p.Action.FormValue["cluster"]` instead. The fix is scoped to the `pick_cluster`
branch so the other actions (`pick`, `run`, `pick_group`, `run_group`), which
still carry `cluster` in `value`, are untouched:

- Keep the existing top-level `cluster, _ := p.Action.Value["cluster"].(string)`.
- In the `pick_cluster` case, override with the form value when present:
  `if fv := p.Action.FormValue["cluster"]; fv != "" { reply.Cluster = fv }`.

Empty selection (submit with nothing chosen) yields an empty `reply.Cluster`,
which the core already handles: `PickCluster` with an empty cluster renders the
existing `"no cluster selected — start over"` error. No new core path is needed.

## What does NOT change

- **`core/*`** — intents, the `Renderer` port (`RenderClusterPicker([]Cluster)`
  keeps its signature), `Registry`, and `Core.Handle` are all untouched.
- **`render.go` / `cardaction.go`** — `RenderClusterPicker` still receives
  `[]core.Cluster`; only the card *bytes* produced by `buildClusterPickerCard`
  change.
- **REST (`internal/api`) and MCP (`internal/mcp`)** — return clusters as
  JSON/data; clients filter. No card ceiling, no change.
- **The group / use-case picker (`buildPickerCard`)** — a handful of use-cases +
  groups per cluster, nowhere near the ceiling. Explicitly out of scope (YAGNI).

## Testing

Unit-testable (assert the JSON we emit — the existing `cards_test.go` /
`decode_test.go` style):

- `buildClusterPickerCard` produces a `schema:"2.0"` card whose body contains a
  `form` with exactly one `select_static` (name `"cluster"`) and one submit button
  whose `behaviors[0].value.action == "pick_cluster"`.
- The `select_static` has one option per input cluster, in registry order, each
  `{text.content, value}` = `{label-or-name, name}`.
- An empty cluster list still produces a valid card (select with zero options —
  no panic, no per-cluster buttons).
- `decodeCardAction` on a `pick_cluster` payload whose cluster is in `form_value`
  (and absent from `value`) returns `PickCluster` with `Reply.Cluster` set from
  `form_value["cluster"]`.
- A `pick_cluster` payload with an empty/absent `form_value["cluster"]` yields
  `PickCluster` with an empty `Reply.Cluster` (drives the existing "start over"
  message).

**Not** unit-testable — a manual verification step in the plan, done once against a
real Lark tenant, because we can assert only the JSON we build, never Lark's
renderer:

1. `@bot` renders the dropdown; it lists every configured cluster.
2. Typing filters the option list (client typeahead).
3. Picking a cluster + `Select ▸` advances to the use-case picker for that cluster
   — i.e. the selection actually arrives in `form_value["cluster"]` and the shape
   renders. If it instead arrives in `action.option` (direct-callback form), fall
   back to reading `action.option` in the `pick_cluster` decode branch.

## Files touched

- `internal/platform/lark/cards.go` — rewrite `buildClusterPickerCard`.
- `internal/platform/lark/decode.go` — `pick_cluster` reads `form_value["cluster"]`.
- `internal/platform/lark/cards_test.go`, `decode_test.go` — tests above.
- this spec; the implementation plan.

## Non-goals

- No server-side search / pagination (client typeahead covers it).
- No config knob for page size (all clusters are rendered as options).
- No change to the group / use-case picker, REST, or MCP.
- The selection never changes downstream behavior — the resolved cluster name
  flows into the same `PickCluster` path as before.
{% endraw %}
