package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrUnauthorized        = errors.New("sender not allowed")
	ErrIgnored             = errors.New("ignored non-private message")
	ErrBusy                = errors.New("conversation already processing a turn")
	ErrInvalidContinuation = errors.New("backend continuation no longer exists")
)

type Message struct{ EventID, ChatID, UserID, ChatType, Content string }
type Agent interface {
	Run(ctx context.Context, prompt, continuation, scope string) (answer, nextContinuation string, err error)
}

// RunOptions is a validated per-conversation preference passed to adapters.
type RunOptions struct {
	Model     string `json:"model,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}
type SelectedSettings struct {
	Agent     string
	Model     string
	Reasoning string
	Models    []string
	Efforts   []string
}
type configurableAgent interface {
	Agent
	RunWithOptions(ctx context.Context, prompt, continuation, scope string, opts RunOptions) (answer, nextContinuation string, err error)
	Models() []string
	Efforts() []string
}

type Gateway struct {
	mu      sync.Mutex
	path    string
	allowed map[string]bool
	agents  map[string]Agent
	state   diskState
	busy    map[string]string
}

func New(path string, allowed []string, agents map[string]Agent) (*Gateway, error) {
	if len(allowed) == 0 || len(agents) == 0 || path == "" {
		return nil, errors.New("state path, sender allowlist and agents are required")
	}
	for name, agent := range agents {
		if name == "" || agent == nil {
			return nil, errors.New("registered agents must have a non-empty name and non-nil adapter")
		}
	}
	state, err := loadState(path)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	a := map[string]bool{}
	for _, id := range allowed {
		if id != "" {
			a[id] = true
		}
	}
	if len(a) == 0 {
		return nil, errors.New("sender allowlist is empty")
	}
	return &Gateway{path: path, allowed: a, agents: agents, state: state, busy: map[string]string{}}, nil
}

func (g *Gateway) names() string {
	names := make([]string, 0, len(g.agents))
	for name := range g.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "、")
}

// Handle accepts private text messages. An empty reply means this event was already handled.
func (g *Gateway) Handle(ctx context.Context, m Message) (string, error) {
	if m.ChatType != "p2p" {
		return "", ErrIgnored
	}
	if !g.allowed[m.UserID] {
		return "", ErrUnauthorized
	}
	if m.ChatID == "" || m.UserID == "" || m.EventID == "" {
		return "", errors.New("missing message identity")
	}
	key := m.ChatID + "\x00" + m.UserID
	g.mu.Lock()
	c := g.state.Conversations[key]
	if c == nil {
		c = &conversation{Sessions: map[string]string{}}
		g.state.Conversations[key] = c
	}
	for _, id := range c.Events {
		if id == m.EventID {
			g.mu.Unlock()
			return "", nil
		}
	}
	if running := g.busy[key]; running != "" {
		g.mu.Unlock()
		if running == m.EventID {
			return "", nil
		}
		return "", ErrBusy
	}
	g.busy[key] = m.EventID
	selected := c.Selected
	if _, present := g.agents[selected]; !present {
		selected = ""
	}
	g.mu.Unlock()
	defer func() { g.mu.Lock(); delete(g.busy, key); g.mu.Unlock() }()

	content := strings.TrimSpace(m.Content)
	var reply, agentName, prompt string
	var setSelection, reset bool
	var selectedModel, selectedEffort string
	var updateModel, updateEffort bool
	switch {
	case content == "/help" || content == "":
		reply = "优先使用机器人菜单：选择 Agent、参数设置、更多。文字入口仍可用：/agents 查看列表；/use <agent> 设置默认；@<agent> 问题临时指定；/reset 清空当前 Agent 会话。"
	case content == "/agents":
		reply = "可用 Agent：" + g.names()
	case strings.HasPrefix(content, "/use "):
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(content, "/use ")))
		if _, ok := g.agents[name]; ok {
			agentName = name
			setSelection = true
			reply = "已切换至 " + name + "。"
		} else {
			reply = "未知 Agent：" + name + "。发送 /agents 查看可用列表。"
		}
	case content == "/reset":
		if selected == "" {
			reply = "请先用 /use 选择 Agent。"
		} else {
			agentName = selected
			reset = true
			reply = "已清空 " + selected + " 的会话上下文。"
		}
	case strings.HasPrefix(content, "/model "):
		if selected == "" {
			reply = "请先通过菜单选择 Agent。"
		} else {
			candidate := strings.TrimSpace(strings.TrimPrefix(content, "/model "))
			if !g.supports(selected, "model", candidate) {
				reply = "当前 Agent 不支持该模型。"
			} else {
				agentName = selected
				selectedModel = candidate
				updateModel = true
				reply = "已为 " + selected + " 选择模型 " + candidate + "；将开启新会话。"
			}
		}
	case strings.HasPrefix(content, "/effort "):
		if selected == "" {
			reply = "请先通过菜单选择 Agent。"
		} else {
			candidate := strings.TrimSpace(strings.TrimPrefix(content, "/effort "))
			if !g.supports(selected, "effort", candidate) {
				reply = "当前 Agent 不支持该思考强度。"
			} else {
				agentName = selected
				selectedEffort = candidate
				updateEffort = true
				reply = "已为 " + selected + " 设置思考强度 " + candidate + "。"
			}
		}
	case strings.HasPrefix(content, "@"):
		parts := strings.SplitN(content, " ", 2)
		name := strings.ToLower(strings.TrimPrefix(parts[0], "@"))
		if _, ok := g.agents[name]; !ok {
			reply = "未知 Agent：" + name + "。发送 /agents 查看可用列表。"
		} else if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
			reply = "请在 @" + name + " 后输入问题。"
		} else {
			agentName = name
			prompt = strings.TrimSpace(parts[1])
		}
	case strings.HasPrefix(content, "/"):
		reply = "未知命令。发送 /help 查看用法。"
	default:
		if selected == "" {
			reply = "请先通过菜单选择 Agent；也可用 /agents 查看并用 /use 选择，或发送 @<agent> 问题。"
		} else {
			agentName = selected
			prompt = content
		}
	}
	var next string
	if prompt != "" {
		g.mu.Lock()
		session := g.state.Conversations[key].Sessions[agentName]
		opts := g.state.Conversations[key].Preferences[agentName]
		if opts.Reasoning == "" {
			opts.Reasoning = "high"
		}
		g.mu.Unlock()
		scope := fmt.Sprintf("feishu-%x", sha256.Sum256([]byte(key+"\x00"+agentName)))
		var answer, continuation string
		var err error
		if adapter, ok := g.agents[agentName].(configurableAgent); ok {
			if !contains(adapter.Efforts(), opts.Reasoning) || opts.Model != "" && !contains(adapter.Models(), opts.Model) {
				return "", errors.New("stored Agent settings are not supported by this backend")
			}
			answer, continuation, err = adapter.RunWithOptions(ctx, prompt, session, scope, opts)
		} else {
			answer, continuation, err = g.agents[agentName].Run(ctx, prompt, session, scope)
		}
		if errors.Is(err, ErrInvalidContinuation) {
			reset = true
			prompt = ""
			reply = "【" + agentName + "】会话已失效，已清空上下文；请确认后重新发送请求。"
		} else if err != nil {
			return "", err
		}
		if !reset && strings.TrimSpace(answer) == "" {
			return "", errors.New("agent returned empty response")
		}
		if !reset {
			reply = "【" + agentName + "】" + answer
			next = continuation
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c = g.state.Conversations[key]
	// Mutate a copy and publish only when durable storage succeeds.
	copyC := cloneConversation(c)

	if _, present := g.agents[copyC.Selected]; !present {
		copyC.Selected = ""
	}
	for k, v := range c.Sessions {
		copyC.Sessions[k] = v
	}
	if setSelection {
		if copyC.Selected != agentName {
			copyC.PendingCards = map[string]PendingCard{}
		}
		copyC.Selected = agentName
	}
	if reset {
		copyC.PendingCards = map[string]PendingCard{}
		delete(copyC.Sessions, agentName)
	}
	if updateModel {
		copyC.PendingCards = map[string]PendingCard{}
		opts := copyC.Preferences[agentName]
		if opts.Model != selectedModel {
			delete(copyC.Sessions, agentName)
		}
		opts.Model = selectedModel
		copyC.Preferences[agentName] = opts
	}
	if updateEffort {
		copyC.PendingCards = map[string]PendingCard{}
		opts := copyC.Preferences[agentName]
		opts.Reasoning = selectedEffort
		copyC.Preferences[agentName] = opts
	}
	if prompt != "" {
		copyC.Sessions[agentName] = next
	}
	copyC.Events = append(copyC.Events, m.EventID)
	if len(copyC.Events) > 100 {
		copyC.Events = copyC.Events[len(copyC.Events)-100:]
	}
	g.state.Conversations[key] = copyC
	if err := saveState(g.path, g.state); err != nil {
		g.state.Conversations[key] = c
		return "", fmt.Errorf("save state: %w", err)
	}
	return reply, nil
}

// ChatIDForUser reuses a unique existing private conversation without changing
// the persisted chat+user key. Menu events do not contain a chat ID. If none or
// several are known, the transport must resolve it through Feishu instead.
func (g *Gateway) ChatIDForUser(userID string) (string, bool) {
	if !g.allowed[userID] {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var chat string
	for key := range g.state.Conversations {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 || parts[1] != userID {
			continue
		}
		if chat != "" && chat != parts[0] {
			return "", false
		}
		chat = parts[0]
	}
	return chat, chat != ""
}

func contains(options []string, value string) bool {
	for _, x := range options {
		if x == value {
			return true
		}
	}
	return false
}
func (g *Gateway) supports(name, kind, value string) bool {
	agent, ok := g.agents[name].(configurableAgent)
	if !ok {
		return false
	}
	if kind == "model" {
		return contains(agent.Models(), value)
	}
	return contains(agent.Efforts(), value)
}

// SelectedSettings returns the current registered Agent and its selectable
// settings without creating an implicit chat or authorizing any new user.
func (g *Gateway) SelectedSettings(chat, user string) (SelectedSettings, error) {
	if !g.allowed[user] {
		return SelectedSettings{}, ErrUnauthorized
	}
	if chat == "" || user == "" {
		return SelectedSettings{}, errors.New("missing conversation identity")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.state.Conversations[chat+"\x00"+user]
	if c == nil || c.Selected == "" {
		return SelectedSettings{}, errors.New("select an Agent first")
	}
	agent, ok := g.agents[c.Selected].(configurableAgent)
	if !ok {
		return SelectedSettings{}, errors.New("selected Agent has no configurable model")
	}
	pref := c.Preferences[c.Selected]
	if pref.Reasoning == "" {
		pref.Reasoning = "high"
	}
	return SelectedSettings{Agent: c.Selected, Model: pref.Model, Reasoning: pref.Reasoning, Models: append([]string(nil), agent.Models()...), Efforts: append([]string(nil), agent.Efforts()...)}, nil
}

func cloneConversation(c *conversation) *conversation {
	out := &conversation{Selected: c.Selected, Sessions: map[string]string{}, Preferences: map[string]RunOptions{}, PendingCards: map[string]PendingCard{}, Events: append([]string(nil), c.Events...)}
	for k, v := range c.Sessions {
		out.Sessions[k] = v
	}
	for k, v := range c.Preferences {
		out.Preferences[k] = v
	}
	for k, v := range c.PendingCards {
		out.PendingCards[k] = v
	}
	return out
}

// PrepareSettingCard records a short-lived, opaque one-time card ID before the
// card is sent. The identity and Agent are tied to the durable conversation.
func (g *Gateway) PrepareSettingCard(chat, user, kind string) (SelectedSettings, string, error) {
	if kind != "model" && kind != "reasoning" {
		return SelectedSettings{}, "", errors.New("unsupported setting kind")
	}
	if !g.allowed[user] {
		return SelectedSettings{}, "", ErrUnauthorized
	}
	if chat == "" || user == "" {
		return SelectedSettings{}, "", errors.New("missing conversation identity")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := chat + "\x00" + user
	c := g.state.Conversations[key]
	if c == nil || c.Selected == "" {
		return SelectedSettings{}, "", errors.New("select an Agent first")
	}
	adapter, ok := g.agents[c.Selected].(configurableAgent)
	if !ok {
		return SelectedSettings{}, "", errors.New("selected Agent does not support settings")
	}
	opts := c.Preferences[c.Selected]
	if opts.Reasoning == "" {
		opts.Reasoning = "high"
	}
	models, efforts := adapter.Models(), adapter.Efforts()
	if kind == "model" && len(models) == 0 || kind == "reasoning" && len(efforts) == 0 {
		return SelectedSettings{}, "", errors.New("no options for selected setting")
	}
	seed := make([]byte, 16)
	if _, err := rand.Read(seed); err != nil {
		return SelectedSettings{}, "", err
	}
	nonce := hex.EncodeToString(seed)
	next := cloneConversation(c)
	now := time.Now()
	for id, pending := range next.PendingCards {
		if now.After(pending.Expires) {
			delete(next.PendingCards, id)
		}
	}
	if len(next.PendingCards) >= 16 {
		return SelectedSettings{}, "", errors.New("too many open settings cards; please try again later")
	}
	expected := opts.Model
	if kind == "reasoning" {
		expected = opts.Reasoning
	}
	next.PendingCards[nonce] = PendingCard{Agent: c.Selected, Kind: kind, Expected: expected, Expires: now.Add(15 * time.Minute)}
	g.state.Conversations[key] = next
	if err := saveState(g.path, g.state); err != nil {
		g.state.Conversations[key] = c
		return SelectedSettings{}, "", err
	}
	return SelectedSettings{Agent: c.Selected, Model: opts.Model, Reasoning: opts.Reasoning, Models: append([]string(nil), models...), Efforts: append([]string(nil), efforts...)}, nonce, nil
}

// ApplySettingCard validates the issued one-time nonce and performs the
// compare-and-set under the same lock that persists selection and dedup state.
func (g *Gateway) ApplySettingCard(chat, user, eventID, nonce, agentName, kind, value, expected string) (string, error) {
	if !g.allowed[user] {
		return "", ErrUnauthorized
	}
	if chat == "" || user == "" || eventID == "" || nonce == "" {
		return "", errors.New("invalid card identity")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := chat + "\x00" + user
	c := g.state.Conversations[key]
	if c == nil || g.busy[key] != "" || c.Selected != agentName {
		return "", errors.New("stale or busy settings card")
	}
	pending, ok := c.PendingCards[nonce]
	if !ok || pending.Agent != agentName || pending.Kind != kind || pending.Expected != expected || time.Now().After(pending.Expires) {
		return "", errors.New("unknown or expired settings card")
	}
	adapter, ok := g.agents[agentName].(configurableAgent)
	if !ok {
		return "", errors.New("Agent does not support settings")
	}
	opts := c.Preferences[agentName]
	if opts.Reasoning == "" {
		opts.Reasoning = "high"
	}
	current := opts.Model
	choices := adapter.Models()
	if kind == "reasoning" {
		current = opts.Reasoning
		choices = adapter.Efforts()
	}
	if current != expected || !contains(choices, value) {
		return "", errors.New("settings card was superseded or unsupported")
	}
	next := cloneConversation(c)
	next.PendingCards = map[string]PendingCard{}
	reply := ""
	if kind == "model" {
		if opts.Model != value {
			delete(next.Sessions, agentName)
		}
		opts.Model = value
		reply = "已为 " + agentName + " 选择模型 " + value + "；将开启新会话。"
	} else if kind == "reasoning" {
		opts.Reasoning = value
		reply = "已为 " + agentName + " 设置思考强度 " + value + "。"
	} else {
		return "", errors.New("unsupported setting kind")
	}
	next.Preferences[agentName] = opts
	next.Events = append(next.Events, "card:"+eventID)
	if len(next.Events) > 100 {
		next.Events = next.Events[len(next.Events)-100:]
	}
	g.state.Conversations[key] = next
	if err := saveState(g.path, g.state); err != nil {
		g.state.Conversations[key] = c
		return "", err
	}
	return reply, nil
}
