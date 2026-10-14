package feishu

import (
	"errors"
	"strings"

	larkapp "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
)

var ErrInvalidMenu = errors.New("unknown menu operation or missing identity")

type MenuAction struct{ EventID, UserID, Command string }

// ParseMenu translates configured event keys, never arbitrary operator input.
// The caller must independently check the sender allowlist before executing it.
func ParseMenu(event *larkapp.P2BotMenuV6, names map[string]bool) (MenuAction, error) {
	if event == nil || event.Event == nil || event.Event.EventKey == nil || event.Event.Operator == nil || event.Event.Operator.OperatorId == nil || event.Event.Operator.OperatorId.OpenId == nil || event.EventV2Base == nil || event.EventV2Base.Header == nil || event.EventV2Base.Header.EventID == "" {
		return MenuAction{}, ErrInvalidMenu
	}
	user := *event.Event.Operator.OperatorId.OpenId
	if user == "" {
		return MenuAction{}, ErrInvalidMenu
	}
	key := *event.Event.EventKey
	var command string
	switch key {
	case "agents_list":
		command = "/agents"
	case "session_reset":
		command = "/reset"
	case "gateway_help":
		command = "/help"
	case "settings_model":
		command = "/settings model"
	case "settings_reasoning":
		command = "/settings reasoning"
	default:
		if !strings.HasPrefix(key, "agent_") {
			return MenuAction{}, ErrInvalidMenu
		}
		name := strings.TrimPrefix(key, "agent_")
		if !names[name] {
			return MenuAction{}, ErrInvalidMenu
		}
		command = "/use " + name
	}
	return MenuAction{EventID: event.EventV2Base.Header.EventID, UserID: user, Command: command}, nil
}
