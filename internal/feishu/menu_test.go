package feishu

import (
	"errors"
	"testing"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkapp "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
)

func menuEvent(key string) *larkapp.P2BotMenuV6 {
	user := "ou_test"
	return &larkapp.P2BotMenuV6{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: "menu-1"}},
		Event:       &larkapp.P2BotMenuV6Data{EventKey: &key, Operator: &larkapp.Operator{OperatorId: &larkapp.UserId{OpenId: &user}}},
	}
}

func TestParseMenuMapsOnlyConfiguredOperations(t *testing.T) {
	names := map[string]bool{"codex": true, "hermes": true, "codex_review": true}
	for _, tc := range []struct{ key, command string }{{"agent_codex", "/use codex"}, {"agent_hermes", "/use hermes"}, {"agent_codex_review", "/use codex_review"}, {"agents_list", "/agents"}, {"session_reset", "/reset"}, {"gateway_help", "/help"}} {
		t.Run(tc.key, func(t *testing.T) {
			action, err := ParseMenu(menuEvent(tc.key), names)
			if err != nil || action.Command != tc.command || action.EventID != "menu-1" || action.UserID != "ou_test" {
				t.Fatalf("action=%+v err=%v", action, err)
			}
		})
	}
	for _, key := range []string{"unknown", "agent_missing", "agent_codex /reset", "agent_", "/use codex"} {
		if _, err := ParseMenu(menuEvent(key), names); !errors.Is(err, ErrInvalidMenu) {
			t.Fatalf("accepted %q: %v", key, err)
		}
	}
}

func TestParseMenuRejectsMissingIdentityAndEventID(t *testing.T) {
	cases := []*larkapp.P2BotMenuV6{nil, {}, menuEvent("gateway_help"), menuEvent("gateway_help"), menuEvent("gateway_help")}
	cases[2].Event.Operator = nil
	cases[3].EventV2Base.Header.EventID = ""
	cases[4].Event.Operator.OperatorId.OpenId = nil
	for i, event := range cases {
		if _, err := ParseMenu(event, nil); !errors.Is(err, ErrInvalidMenu) {
			t.Fatalf("case %d accepted: %v", i, err)
		}
	}
}

func TestParseMenuSettingsKeys(t *testing.T) {
	for _, tc := range []struct{ key, command string }{{"settings_model", "/settings model"}, {"settings_reasoning", "/settings reasoning"}} {
		action, err := ParseMenu(menuEvent(tc.key), map[string]bool{"codex": true})
		if err != nil || action.Command != tc.command {
			t.Fatalf("%s => %+v %v", tc.key, action, err)
		}
	}
}
