package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type optionsAgent struct {
	calls    []RunOptions
	sessions []string
}

func (a *optionsAgent) Run(context.Context, string, string, string) (string, string, error) {
	return "default", "id", nil
}
func (a *optionsAgent) RunWithOptions(_ context.Context, _ string, session, _ string, opts RunOptions) (string, string, error) {
	a.calls = append(a.calls, opts)
	a.sessions = append(a.sessions, session)
	return "ok", "new-continuation", nil
}
func (a *optionsAgent) Models() []string {
	return []string{"gpt-6-luna", "gpt-6-sol"}
}
func (a *optionsAgent) Efforts() []string { return []string{"low", "medium", "high", "xhigh"} }

func TestPreferenceDefaultPersistenceAndAgentIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	codex, other := &optionsAgent{}, &optionsAgent{}
	aliases := map[string]Agent{"codex": codex, "second": other}
	g, err := New(path, []string{"u", "v"}, aliases)
	if err != nil {
		t.Fatal(err)
	}
	msg := func(id, u, text string) Message {
		return Message{EventID: id, UserID: u, ChatID: "chat", ChatType: "p2p", Content: text}
	}
	for _, m := range []Message{msg("1", "u", "/use codex"), msg("2", "u", "hello"), msg("3", "u", "/effort medium"), msg("4", "u", "next"), msg("5", "u", "/model gpt-6-sol"), msg("6", "u", "after model"), msg("7", "u", "/use second"), msg("8", "u", "other"), msg("9", "v", "@codex independent")} {
		if _, err := g.Handle(context.Background(), m); err != nil {
			t.Fatalf("%s: %v", m.Content, err)
		}
	}
	if len(codex.calls) != 4 || codex.calls[0].Reasoning != "high" || codex.calls[0].Model != "" || codex.calls[1].Reasoning != "medium" || codex.calls[2].Model != "gpt-6-sol" || codex.calls[2].Reasoning != "medium" || codex.calls[3].Reasoning != "high" {
		t.Fatalf("wrong per-user settings: %+v", codex.calls)
	}
	if codex.sessions[0] != "" || codex.sessions[1] != "new-continuation" || codex.sessions[2] != "" {
		t.Fatalf("model switch did not reset only codex session: %+v", codex.sessions)
	}
	if len(other.calls) != 1 || other.calls[0].Reasoning != "high" || other.calls[0].Model != "" {
		t.Fatalf("second agent inherited settings: %+v", other.calls)
	}
	g, err = New(path, []string{"u"}, aliases)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := g.SelectedSettings("chat", "u")
	if err != nil || settings.Agent != "second" || settings.Reasoning != "high" {
		t.Fatalf("settings after restart %+v %v", settings, err)
	}
	if _, err = g.Handle(context.Background(), msg("10", "u", "/use codex")); err != nil {
		t.Fatal(err)
	}
	settings, err = g.SelectedSettings("chat", "u")
	if err != nil || settings.Model != "gpt-6-sol" || settings.Reasoning != "medium" {
		t.Fatalf("codex preference after restart %+v %v", settings, err)
	}
}

func TestPreferencesRejectUnknownModelsEffortsAndDupes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	g, err := New(path, []string{"u"}, map[string]Agent{"codex": &optionsAgent{}})
	if err != nil {
		t.Fatal(err)
	}
	m := func(id, text string) Message {
		return Message{EventID: id, ChatID: "c", UserID: "u", ChatType: "p2p", Content: text}
	}
	if _, err = g.Handle(context.Background(), m("1", "/use codex")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, text string }{{"2", "/model arbitrary"}, {"3", "/effort extreme"}, {"4", "/model --danger"}} {
		reply, err := g.Handle(context.Background(), m(tc.id, tc.text))
		if err != nil || !strings.Contains(reply, "不支持") {
			t.Fatalf("invalid %s => %q %v", tc.text, reply, err)
		}
	}
	reply, err := g.Handle(context.Background(), m("5", "/model gpt-6-luna"))
	if err != nil || !strings.Contains(reply, "新会话") {
		t.Fatalf("switch %q %v", reply, err)
	}
	reply, err = g.Handle(context.Background(), m("5", "/model gpt-6-luna"))
	if err != nil || reply != "" {
		t.Fatalf("duplicate applied twice: %q %v", reply, err)
	}
	if _, err = g.SelectedSettings("c", "outsider"); err == nil {
		t.Fatal("unauthorized settings lookup")
	}
}

func TestSettingsMigrateExistingStateWithoutPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"conversations":{"oldchat\u0000u":{"selected":"codex","sessions":{"codex":"old-session"},"events":["prior"]}}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	agent := &optionsAgent{}
	g, err := New(path, []string{"u"}, map[string]Agent{"codex": agent})
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := g.SelectedSettings("oldchat", "u")
	if err != nil || prefs.Reasoning != "high" || prefs.Model != "" {
		t.Fatalf("old state prefs: %+v %v", prefs, err)
	}
	if _, err = g.Handle(context.Background(), Message{EventID: "new", ChatID: "oldchat", UserID: "u", ChatType: "p2p", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(agent.sessions) != 1 || agent.sessions[0] != "old-session" {
		t.Fatalf("lost old continuation %v", agent.sessions)
	}
}

func TestCardSettingsAreAtomicAndOneTimeAfterEventWindowExpires(t *testing.T) {
	g, err := New(filepath.Join(t.TempDir(), "state.json"), []string{"u"}, map[string]Agent{"codex": &optionsAgent{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	initial := Message{EventID: "select", ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/use codex"}
	if _, err = g.Handle(ctx, initial); err != nil {
		t.Fatal(err)
	}
	_, nonce, err := g.PrepareSettingCard("chat", "u", "reasoning")
	if err != nil {
		t.Fatal(err)
	}
	var ok int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, choice := range []string{"medium", "xhigh"} {
		wg.Add(1)
		go func(i int, choice string) {
			defer wg.Done()
			_, err := g.ApplySettingCard("chat", "u", fmt.Sprintf("card%d", i), nonce, "codex", "reasoning", choice, "high")
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(i, choice)
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("both concurrent choices applied: %d", ok)
	}
	for i := 0; i < 105; i++ {
		m := Message{EventID: fmt.Sprintf("help-%d", i), ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/help"}
		if _, err := g.Handle(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Handle(ctx, Message{EventID: "restore", ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/effort high"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ApplySettingCard("chat", "u", "card-replay", nonce, "codex", "reasoning", "medium", "high"); err == nil {
		t.Fatal("consumed nonce became replayable after event window eviction")
	}
}

func TestCardIssuedBeforeAgentSwitchCannotApplyAfterSwitchBack(t *testing.T) {
	g, err := New(filepath.Join(t.TempDir(), "state.json"), []string{"u"}, map[string]Agent{"codex": &optionsAgent{}, "other": &optionsAgent{}})
	if err != nil {
		t.Fatal(err)
	}
	m := func(id, text string) Message {
		return Message{EventID: id, ChatID: "c", UserID: "u", ChatType: "p2p", Content: text}
	}
	for _, msg := range []Message{m("1", "/use codex"), m("2", "/use other")} {
		if _, err := g.Handle(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	_, nonce, err := g.PrepareSettingCard("c", "u", "reasoning")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []Message{m("3", "/use codex"), m("4", "/use other")} {
		if _, err := g.Handle(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.ApplySettingCard("c", "u", "card", nonce, "other", "reasoning", "medium", "high"); err == nil {
		t.Fatal("previous selection card still valid after switching away/back")
	}
}
