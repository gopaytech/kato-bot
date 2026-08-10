// Package core holds kato-bot's platform-agnostic types and state machine.
package core

import "context"

// Reply addresses where a card lives. Opaque to core; adapters fill it from the
// platform event. For Lark: ChatID + MessageID identify the bot's card (for patching);
// InReplyTo is the user's message id the first picker card replies to.
type Reply struct {
	ChatID    string
	MessageID string // the bot card's own id, for patching; empty before the first card
	InReplyTo string // user's message id to reply to (set only for the initial picker)
	Cluster   string // selected cluster name; empty for the initial cluster-picker step
}

// UseCase is one kato UseCase summary (from GET /usecases).
type UseCase struct {
	Name        string
	Description string
	Ready       bool
}

// InputDecl is one declared input of a UseCase.
type InputDecl struct {
	Name     string
	Required bool
}

// Contract is a UseCase's input contract (from GET /usecases/{name}).
type Contract struct {
	Name        string
	Description string
	Inputs      []InputDecl
}

// RunResult is the outcome of POST /usecases/{name}/run. Err is set (non-nil) when
// the run could not be obtained (transport, timeout, or an HTTP error status); the
// renderer shows a friendly message in that case.
type RunResult struct {
	Run      string
	Phase    string
	Summary  string
	Warning  string
	Healthy  *bool  // health verdict from kato: true/false; nil = unknown
	Headline string // one-line reason for Healthy; empty when unknown
	Err      error
}

// KatoClient is the kato REST surface the core depends on (implemented by internal/kato).
type KatoClient interface {
	ListUseCases(ctx context.Context) ([]UseCase, error)
	GetUseCase(ctx context.Context, name string) (Contract, error)
	Run(ctx context.Context, name string, inputs map[string]string) (RunResult, error)
}

// Renderer is the outbound port: turn semantic state into platform cards.
type Renderer interface {
	RenderClusterPicker(ctx context.Context, r Reply, clusters []Cluster) error
	RenderPicker(ctx context.Context, r Reply, ucs []UseCase, groups []Group) error
	RenderGroupConfirm(ctx context.Context, r Reply, g Group) error
	RenderForm(ctx context.Context, r Reply, c Contract, prefill map[string]string, formErr string) error
	RenderRunning(ctx context.Context, r Reply, useCase string, inputs map[string]string) error
	RenderResult(ctx context.Context, r Reply, useCase string, inputs map[string]string, res RunResult) error
	RenderError(ctx context.Context, r Reply, msg string) error
}

// Intent is an inbound, decoded user action. Adapters produce these.
type Intent interface{ isIntent() }

type ListClusters struct{ Reply Reply }
type PickCluster struct{ Reply Reply } // selected cluster is in Reply.Cluster
type PickUseCase struct {
	Reply Reply
	Name  string
}
type SubmitForm struct {
	Reply  Reply
	Name   string
	Inputs map[string]string
}
type PickGroup struct {
	Reply Reply
	Name  string
}
type RunGroup struct {
	Reply Reply
	Name  string
}

func (ListClusters) isIntent() {}
func (PickCluster) isIntent()  {}
func (PickUseCase) isIntent()  {}
func (SubmitForm) isIntent()   {}
func (PickGroup) isIntent()    {}
func (RunGroup) isIntent()     {}

// RunError is a human-facing error message for display in a result card.
type RunError struct{ Msg string }

func (e *RunError) Error() string { return e.Msg }

// HTTPStatusError is implemented by errors that carry an HTTP status code and the
// server's detail message (internal/kato's *APIError does). It lets core map kato
// failures to friendly, status-aware text without importing the kato package
// (which would create an import cycle, since kato imports core).
type HTTPStatusError interface {
	error
	HTTPStatus() int // the HTTP status code from kato (e.g. 400, 429)
	Detail() string  // kato's error message, without the "kato NNN:" prefix
}
