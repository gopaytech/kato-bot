package lark

import (
	"encoding/json"
	"fmt"

	"github.com/zufardhiyaulhaq/kato-bot/internal/core"
)

// decodeMessage maps any received user message to a ListClusters intent (show the cluster
// picker). chatID is the message's chat; userMsgID is the message to reply to.
func decodeMessage(chatID, userMsgID string) core.Intent {
	return core.ListClusters{Reply: core.Reply{ChatID: chatID, InReplyTo: userMsgID}}
}

// cardActionPayload is the subset of Lark's card.action.trigger event we use. The
// callback context identifies the bot card itself (open_message_id), which is exactly
// what we patch on subsequent renders — so no message-id threading is needed.
type cardActionPayload struct {
	Action struct {
		Value     map[string]any    `json:"value"`
		FormValue map[string]string `json:"form_value"`
	} `json:"action"`
	Context struct {
		OpenChatID    string `json:"open_chat_id"`
		OpenMessageID string `json:"open_message_id"`
	} `json:"context"`
}

// decodeCardAction parses a card.action.trigger payload into a PickCluster, PickUseCase,
// or SubmitForm. The selected cluster (when present) is carried in every action value and
// threaded into Reply.Cluster so the core can resolve the right kato backend.
func decodeCardAction(raw []byte) (core.Intent, error) {
	var p cardActionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode card action: %w", err)
	}
	cluster, _ := p.Action.Value["cluster"].(string)
	reply := core.Reply{ChatID: p.Context.OpenChatID, MessageID: p.Context.OpenMessageID, Cluster: cluster}
	action, _ := p.Action.Value["action"].(string)
	useCase, _ := p.Action.Value["usecase"].(string)
	group, _ := p.Action.Value["group"].(string)
	switch action {
	case "pick_cluster":
		return core.PickCluster{Reply: reply}, nil
	case "pick":
		return core.PickUseCase{Reply: reply, Name: useCase}, nil
	case "run":
		inputs := p.Action.FormValue
		if inputs == nil {
			inputs = map[string]string{}
		}
		return core.SubmitForm{Reply: reply, Name: useCase, Inputs: inputs}, nil
	case "pick_group":
		return core.PickGroup{Reply: reply, Name: group}, nil
	case "run_group":
		return core.RunGroup{Reply: reply, Name: group}, nil
	default:
		return nil, fmt.Errorf("unknown card action %q", action)
	}
}
