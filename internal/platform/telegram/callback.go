package telegram

import (
	"fmt"
	"strings"

	"github.com/gopaytech/kato-bot/internal/core"
)

// cbMaxBytes is Telegram's callback_data limit.
const cbMaxBytes = 64

func cbFits(data string) bool { return len(data) <= cbMaxBytes }

func cbCluster(cluster string) string     { return "c|" + cluster }
func cbUseCase(cluster, uc string) string { return "u|" + cluster + "|" + uc }
func cbRun(cluster, uc string) string     { return "r|" + cluster + "|" + uc }
func cbGroup(cluster, g string) string    { return "g|" + cluster + "|" + g }
func cbRunGroup(cluster, g string, sum bool) string {
	flag := "0"
	if sum {
		flag = "1"
	}
	return "rg|" + cluster + "|" + g + "|" + flag
}

// decodeCB parses a callback_data string into an intent with Cluster/Name filled.
// The addressing part of Reply (ChatID, MessageID) is set by the dispatcher.
func decodeCB(data string) (core.Intent, error) {
	parts := strings.Split(data, "|")
	switch parts[0] {
	case "c":
		if len(parts) != 2 {
			return nil, fmt.Errorf("bad c payload %q", data)
		}
		return core.PickCluster{Reply: core.Reply{Cluster: parts[1]}}, nil
	case "u":
		if len(parts) != 3 {
			return nil, fmt.Errorf("bad u payload %q", data)
		}
		return core.PickUseCase{Reply: core.Reply{Cluster: parts[1]}, Name: parts[2]}, nil
	case "r":
		if len(parts) != 3 {
			return nil, fmt.Errorf("bad r payload %q", data)
		}
		return core.SubmitForm{Reply: core.Reply{Cluster: parts[1]}, Name: parts[2], Inputs: map[string]string{}}, nil
	case "g":
		if len(parts) != 3 {
			return nil, fmt.Errorf("bad g payload %q", data)
		}
		return core.PickGroup{Reply: core.Reply{Cluster: parts[1]}, Name: parts[2]}, nil
	case "rg":
		if len(parts) != 4 {
			return nil, fmt.Errorf("bad rg payload %q", data)
		}
		return core.RunGroup{Reply: core.Reply{Cluster: parts[1]}, Name: parts[2], Summary: parts[3] == "1"}, nil
	default:
		return nil, fmt.Errorf("unknown callback action %q", parts[0])
	}
}
