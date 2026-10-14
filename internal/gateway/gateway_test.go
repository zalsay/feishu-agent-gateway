package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type agentFunc func(context.Context, string, string, string) (string, string, error)

func (f agentFunc) Run(ctx context.Context, prompt, session, scope string) (string, string, error) {
	return f(ctx, prompt, session, scope)
}

func TestRoutingIsolationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var mu sync.Mutex
	calls := []string{}
	agents := map[string]Agent{
		"codex": agentFunc(func(_ context.Context, p, s, scope string) (string, string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, "codex:"+p+":"+s)
			return "done", "codex-session", nil
		}),
		"hermes": agentFunc(func(_ context.Context, p, s, scope string) (string, string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, "hermes:"+p+":"+s)
			return "answer", "resp-1", nil
		}),
	}
	g, err := New(path, []string{"user1", "user2"}, agents)
	if err != nil {
		t.Fatal(err)
	}
	msg := func(id, user, text string) Message {
		return Message{EventID: id, ChatID: "chat1", UserID: user, ChatType: "p2p", Content: text}
	}
	run := func(m Message, want string) {
		t.Helper()
		got, err := g.Handle(context.Background(), m)
		if err != nil || got != want {
			t.Fatalf("Handle(%+v)=%q,%v want %q", m, got, err, want)
		}
	}
	run(msg("1", "user1", "hello"), "请先通过菜单选择 Agent；也可用 /agents 查看并用 /use 选择，或发送 @<agent> 问题。")
	run(msg("2", "user1", "/use codex"), "已切换至 codex。")
	run(msg("3", "user1", "first"), "【codex】done")
	run(msg("3", "user1", "first"), "") // duplicate doesn't rerun
	run(msg("4", "user1", "@hermes test"), "【hermes】answer")
	run(msg("5", "user1", "second"), "【codex】done") // explicit alias doesn't change selection
	run(msg("6", "user2", "@codex other"), "【codex】done")
	if got, want := strings.Join(calls, "|"), "codex:first:|hermes:test:|codex:second:codex-session|codex:other:"; got != want {
		t.Fatalf("calls %q want %q", got, want)
	}
	g, err = New(path, []string{"user1"}, agents)
	if err != nil {
		t.Fatal(err)
	}
	got, err := g.Handle(context.Background(), msg("7", "user1", "after restart"))
	if err != nil || got != "【codex】done" {
		t.Fatalf("restart=%q %v", got, err)
	}
	if calls[len(calls)-1] != "codex:after restart:codex-session" {
		t.Fatalf("lost continuation: %v", calls)
	}
	if _, err = g.Handle(context.Background(), msg("8", "user2", "@codex secret")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized %v", err)
	}
}

func TestCommandsAndFailureDoNotAdvance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var sessions []string
	fail := true
	g, err := New(path, []string{"u"}, map[string]Agent{"hermes": agentFunc(func(_ context.Context, p, s, scope string) (string, string, error) {
		sessions = append(sessions, s)
		if fail {
			fail = false
			return "", "", errors.New("backend")
		}
		return "ok", "response", nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	message := func(id, content string) Message {
		return Message{EventID: id, UserID: "u", ChatID: "c", ChatType: "p2p", Content: content}
	}
	for _, tc := range []struct{ id, text, want string }{{"1", "/agents", "可用 Agent：hermes"}, {"2", "@missing hi", "未知 Agent：missing。发送 /agents 查看可用列表。"}, {"3", "/use hermes", "已切换至 hermes。"}, {"4", "@hermes", "请在 @hermes 后输入问题。"}} {
		got, err := g.Handle(context.Background(), message(tc.id, tc.text))
		if err != nil || got != tc.want {
			t.Fatalf("%s = %q %v", tc.text, got, err)
		}
	}
	if _, err := g.Handle(context.Background(), message("5", "fail")); err == nil {
		t.Fatal("expected backend error")
	}
	got, err := g.Handle(context.Background(), message("5", "retry"))
	if err != nil || got != "【hermes】ok" {
		t.Fatalf("retry %q %v", got, err)
	}
	got, err = g.Handle(context.Background(), message("6", "/reset"))
	if err != nil || got != "已清空 hermes 的会话上下文。" {
		t.Fatalf("reset %q %v", got, err)
	}
	got, err = g.Handle(context.Background(), message("7", "after"))
	if err != nil || got != "【hermes】ok" {
		t.Fatalf("after reset %q %v", got, err)
	}
	if strings.Join(sessions, ",") != ",," {
		t.Fatalf("unexpected sessions: %v", sessions)
	}
}

func TestRejectGroupAndConcurrentTurns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	g, err := New(filepath.Join(t.TempDir(), "s.json"), []string{"u"}, map[string]Agent{"codex": agentFunc(func(context.Context, string, string, string) (string, string, error) {
		close(started)
		<-release
		return "ok", "x", nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	m := Message{EventID: "1", UserID: "u", ChatID: "c", ChatType: "group", Content: "@codex hi"}
	if _, err = g.Handle(context.Background(), m); !errors.Is(err, ErrIgnored) {
		t.Fatal(err)
	}
	m.ChatType = "p2p"
	done := make(chan error, 1)
	go func() { _, err := g.Handle(context.Background(), m); done <- err }()
	<-started
	duplicate, err := g.Handle(context.Background(), m)
	if err != nil || duplicate != "" {
		t.Fatalf("duplicate in flight: %q %v", duplicate, err)
	}
	other := m
	other.EventID = "2"
	if _, err := g.Handle(context.Background(), other); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestInvalidContinuationClearsWithoutReplayingPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var sessions []string
	n := 0
	g, err := New(path, []string{"u"}, map[string]Agent{"hermes": agentFunc(func(_ context.Context, p, s, scope string) (string, string, error) {
		sessions = append(sessions, s)
		n++
		if n == 1 {
			return "first", "old", nil
		}
		if n == 2 {
			return "", "", ErrInvalidContinuation
		}
		return "new", "fresh", nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	msg := func(id string) Message {
		return Message{EventID: id, ChatID: "c", UserID: "u", ChatType: "p2p", Content: "@hermes task"}
	}
	if _, err = g.Handle(context.Background(), msg("1")); err != nil {
		t.Fatal(err)
	}
	text, err := g.Handle(context.Background(), msg("2"))
	if err != nil || text != "【hermes】会话已失效，已清空上下文；请确认后重新发送请求。" {
		t.Fatalf("invalid: %q %v", text, err)
	}
	if n != 2 {
		t.Fatal("invalid continuation was automatically replayed")
	}
	if _, err = g.Handle(context.Background(), msg("3")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sessions, ",") != ",old," {
		t.Fatalf("sessions %v", sessions)
	}
}

func TestChatIDForUserPreservesConversationAndAuthorization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	a := agentFunc(func(context.Context, string, string, string) (string, string, error) { return "unused", "unused", nil })
	g, err := New(path, []string{"u"}, map[string]Agent{"codex": a})
	if err != nil {
		t.Fatal(err)
	}
	if chat, ok := g.ChatIDForUser("u"); ok || chat != "" {
		t.Fatalf("unexpected known chat %q", chat)
	}
	m := Message{EventID: "1", ChatID: "c1", UserID: "u", ChatType: "p2p", Content: "/use codex"}
	if _, err := g.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if chat, ok := g.ChatIDForUser("u"); !ok || chat != "c1" {
		t.Fatalf("chat=%q ok=%v", chat, ok)
	}
	if _, ok := g.ChatIDForUser("outsider"); ok {
		t.Fatal("leaked chat to unauthorized identity")
	}
	g, err = New(path, []string{"u"}, map[string]Agent{"codex": a})
	if err != nil {
		t.Fatal(err)
	}
	if chat, ok := g.ChatIDForUser("u"); !ok || chat != "c1" {
		t.Fatalf("restart chat=%q ok=%v", chat, ok)
	}
	m.EventID = "2"
	m.ChatID = "c2"
	if _, err := g.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if chat, ok := g.ChatIDForUser("u"); ok || chat != "" {
		t.Fatalf("ambiguous chat should be resolved by Feishu, got %q", chat)
	}
}

func TestRemovedAgentSelectionFailsClosedAndRetainsOldContinuation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var resumed string
	codex := agentFunc(func(_ context.Context, p, s, scope string) (string, string, error) {
		resumed = s
		return "ok", "old-session", nil
	})
	hermes := agentFunc(func(context.Context, string, string, string) (string, string, error) {
		return "ok", "hermes-session", nil
	})
	g, err := New(path, []string{"u"}, map[string]Agent{"codex": codex, "hermes": hermes})
	if err != nil {
		t.Fatal(err)
	}
	msg := func(id, text string) Message {
		return Message{EventID: id, UserID: "u", ChatID: "c", ChatType: "p2p", Content: text}
	}
	for _, m := range []Message{msg("1", "/use codex"), msg("2", "first")} {
		if _, err := g.Handle(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	g, err = New(path, []string{"u"}, map[string]Agent{"hermes": hermes})
	if err != nil {
		t.Fatal(err)
	}
	got, err := g.Handle(context.Background(), msg("3", "after config change"))
	if err != nil || !strings.Contains(got, "请先") {
		t.Fatalf("removed selected backend: %q %v", got, err)
	}
	g, err = New(path, []string{"u"}, map[string]Agent{"codex": codex})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Handle(context.Background(), msg("4", "/use codex")); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Handle(context.Background(), msg("5", "restored")); err != nil {
		t.Fatal(err)
	}
	if resumed != "old-session" {
		t.Fatalf("old continuation removed: %q", resumed)
	}
}

func TestNewRejectsNilAdapter(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "state.json"), []string{"u"}, map[string]Agent{"codex": nil}); err == nil {
		t.Fatal("nil adapter should fail startup")
	}
}

func TestHelpAndUnselectedPromptPreferMenuWithoutAssumingCodex(t *testing.T) {
	a := agentFunc(func(context.Context, string, string, string) (string, string, error) { return "ok", "id", nil })
	g, err := New(filepath.Join(t.TempDir(), "state.json"), []string{"u"}, map[string]Agent{"hermes_ops": a})
	if err != nil {
		t.Fatal(err)
	}
	for i, content := range []string{"/help", "question"} {
		reply, err := g.Handle(context.Background(), Message{EventID: string(rune('a' + i)), ChatID: "c", UserID: "u", ChatType: "p2p", Content: content})
		if err != nil || !strings.Contains(reply, "菜单") || !strings.Contains(reply, "/use") || strings.Contains(reply, "@codex") {
			t.Fatalf("menu help=%q err=%v", reply, err)
		}
	}
}
