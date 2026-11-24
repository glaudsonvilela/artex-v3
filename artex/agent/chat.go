package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// ChatAgent is the generic, task-independent conversational runner behind the chat
// page. It generalizes MainAgent.Chat: any agent (built-in OR a custom one, by
// key) can be chatted with, multi-turn history resumed from the transcript. It is
// a PURE ASSISTANT — base tools are the SDK DefaultTools (Bash/Read/Write/Edit/
// LS/Glob/Grep) plus whatever skills/MCP the key is made visible; NO pentest
// graph/task context is injected (that stays exclusive to MainAgent).
type ChatAgent struct {
	prov           llm.Provider
	model          string
	workDir        string
	tx             *transcript.Store
	window         int
	proxyAddr      string
	proxyCACert    string
	webSearch      WebSearchOpts
	guard          *guard.Guard // optional; nil disables intercept hooks for chat
	nonStreamingFn func() bool  // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn   func() bool  // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn    func() int   // resolver: per-reply output cap (nil/0 = send no cap)
}

func NewChatAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window int) *ChatAgent {
	return &ChatAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window}
}

// SetNonStreaming wires a resolver deciding whether chat runs use the
// non-streaming model path (true = non-streaming). nil/unset = streaming.
func (c *ChatAgent) SetNonStreaming(fn func() bool) { c.nonStreamingFn = fn }

func (c *ChatAgent) nonStreaming() bool { return c.nonStreamingFn != nil && c.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether chat runs use the experimental
// noa context-compression mechanism. nil/unset = off (built-in compaction). Read
// per run so the settings toggle takes effect without rebuilding the agent.
func (c *ChatAgent) SetNoaEnabled(fn func() bool) { c.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (c *ChatAgent) SetMaxTokens(fn func() int) { c.maxTokensFn = fn }

func (c *ChatAgent) maxTokens() int {
	if c.maxTokensFn == nil {
		return 0
	}
	return c.maxTokensFn()
}

// SetProxy points the chat agent's WebFetch/Bash at the recording proxy plus the
// CA cert it trusts (empty addr = direct). Kept for parity with the other agents.
func (c *ChatAgent) SetProxy(addr, caCert string) { c.proxyAddr, c.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the chat agent (off by default).
func (c *ChatAgent) SetWebSearch(o WebSearchOpts) { c.webSearch = o }

// SetGuard attaches a guard (with user-configured intercept rules) to this chat
// agent. Must be called before Chat; safe to call multiple times.
func (c *ChatAgent) SetGuard(g *guard.Guard) { c.guard = g }

// chatWorkDirSpec gives chat agents a default location for generated files while
// preserving operator-selected paths for direct VM work.
func chatWorkDirSpec(workDir string) string {
	return "\n\n**Working directory**: relative file paths use " + workDir + " by default. When the operator specifies a different path for a VM task, use that path."
}

// autoExecutionGuidance is code-owned so a stale or edited database prompt cannot
// reduce Auto to a platform help bot. Auto has the same VM tools as other chat
// agents and should try them before declaring a requested operation impossible.
const autoExecutionGuidance = `

Auto operator execution:
- Treat a clear operator request concerning this Debian VM as work to perform now. Use Bash, Read, Write, Edit, or the relevant ARTEX task tool in this turn; do not answer with a generic inability statement or moral commentary in place of a tool attempt.
- You may work directly in the VM, including an operator-specified path. For a longer assessment, start or steer an ARTEX task when that is the appropriate tool.
- If one tool or command fails, inspect the concrete error and try a reasonable alternative within the operator's stated scope. Report an actual missing credential, denied platform action, or exhausted technical path precisely.
- Keep targets and data within the operator's authorized scope. Never claim an action succeeded without its tool result.`

// chatSystem renders the DB-managed prompt body for agentKey. Custom agents have
// no per-key in-code default, so DefaultAssistantPrompt is the render fallback.
// Every chat agent, built-in or custom, can read target-derived content through
// WebFetch or Bash, so all of them get the code-owned untrusted-content tail
// after the editable body.
func chatSystem(agentKey, dataDir, workDir string) string {
	body := renderSystem(agentKey, DefaultAssistantPrompt, chatVars{DataDir: dataDir, Now: nowStr()})
	body += untrustedContentTail(body)
	if agentKey == "auto" {
		body += autoExecutionGuidance
	}
	return body + chatWorkDirSpec(workDir) + noiseProfileBlock()
}

// chatVars carries the runtime variables a custom agent's prompt may reference.
// DataDir (server data root) + Now (server wall-clock, refreshed each turn) are the
// universal ones; any other {{.X}} fails to render and falls back to
// DefaultAssistantPrompt.
type chatVars struct{ DataDir, Now string }

// Chat runs ONE turn of a conversation with the agent identified by agentKey,
// resuming prior history keyed by sessionID. maxTurns is the per-turn agent step
// budget (0 = unlimited). maxDuration is the wall-clock run budget per turn
// (0 = unlimited); the timer resets each time Chat is called, so a new user
// message always starts a fresh countdown. webSearch gates network search for
// THIS agent (the global backend/key still come from the chat agent's config,
// but each agent decides on/off). emit receives each execution step (thinking /
// tool_use / tool_result / text / result), tagged with the agent key as the
// worker lane.
func (c *ChatAgent) Chat(ctx context.Context, agentKey, sessionID, message string, maxTurns int, maxDuration time.Duration, webSearch bool, emit func(db.Activity)) (string, error) {
	// gate the global web-search opts by this agent's own flag.
	ws := c.webSearch
	if !webSearch {
		ws.Enabled = false
	}

	// Per-session working directory: <workDir>/sessions/<sessionID>/
	// Isolates file writes across conversations, mirroring how workers use i<intentID>/.
	sessionWorkDir := filepath.Join(c.workDir, "sessions", sessionID)
	_ = os.MkdirAll(sessionWorkDir, 0o755)
	ctx = intercept.WithReviewWorkingDirectory(ctx, sessionWorkDir)

	// Pure assistant: DefaultTools as the base; AugmentTools layers in the key's
	// visible skills/MCP and lets the DB tools table filter/override. DefaultTools
	// have no tools-table rows, so they always pass through.
	base := actool.DefaultTools()
	ctx = WithRunInfo(ctx, RunInfo{SessionID: sessionID})
	tools, def, cleanup := AugmentTools(ctx, agentKey, base)
	defer cleanup()

	system, boundary := deferredSystem(chatSystem(agentKey, c.workDir, sessionWorkDir), def)
	opts := agentcore.Options{
		Provider:        c.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           withWebFetch(ctx, tools, c.proxyAddr, c.proxyCACert), // WebFetch: routed through the recording proxy for tracing; loads the proxy CA to verify MITM-resigned HTTPS certificates; every redirect hop is gated
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		// Web search (optional). ddgs needs no key; brave-free needs BraveKey; tavily needs TavilyKey.
		// WebSearchProxy is a dedicated egress proxy (http/https/socks5), unrelated to the traffic-recording MITM proxy; empty = direct.
		EnableWebSearch:       ws.Enabled,
		WebSearchBackend:      ws.Backend,
		BraveSearchAPIKey:     ws.BraveKey,
		TavilySearchAPIKey:    ws.TavilyKey,
		KeenableSearchAPIKey:  ws.KeenableKey,
		KeenableSearchTitle:   KeenableSearchTitle,
		DeepSeekSearchBaseURL: ws.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  ws.DeepSeekAPIKey,
		DeepSeekSearchModel:   ws.DeepSeekModel,
		WebSearchProxy:        ws.Proxy,
		BashEnv:               proxyEnv(c.proxyAddr, c.proxyCACert), // Bash subcommands go through the proxy + trust the CA by default
		WorkingDir:            sessionWorkDir,
		MaxTurns:              maxTurns,
		MaxDuration:           maxDuration,
		Compaction:            compactionConfig(c.window),
		Todos:                 actool.NewTodoStore(),
		// large tool output spills to cmd-output/ under the session dir.
		// the truncation cap uses the SDK default (30000 chars of tool.Capture).
		ToolOutputDir: filepath.Join(sessionWorkDir, "cmd-output"),
		// hitting the budget (steps) → the SDK runs wrap-up: output a one-line summary. The prompt and
		// wrap-up rounds are editable in the backend per agent key
		// (each custom agent gets its own copy; empty/0 uses the generic default: 10 rounds).
		Settlement:   wrapupSettlement(agentKey, nil),
		NonStreaming: c.nonStreaming(), // when this profile selects non-streaming, use Provider.Complete
		MaxTokens:    c.maxTokens(),    // 0 = send no cap, the server's default decides

		// On by default; ARTEX_UNTRUSTED_BOUNDARY=0 opts out: nonce-frame tool output as untrusted data.
		UntrustedOutputBoundary: untrustedBoundaryEnabled(),
	}
	if c.guard != nil {
		opts.Hooks = c.guard.Hooks()
	}
	// The session id is the provider-side identity (session header, usage metering)
	// with or without persistence; Norma only puts it on ctx when it writes a transcript.
	opts.SessionID = sessionID
	ctx = transcript.WithSessionID(ctx, opts.SessionID)
	if c.tx != nil { // persist raw human↔AI conversation; one accumulating file per thread
		opts.Transcript = c.tx
	}
	// experimental feature: when enabled, noa takes over context compression (archive centralized under <workDir>/noa/<SessionID>, persistent).
	enableNoa(&opts, c.noaEnabledFn, c.workDir, "chat-"+sessionID, noaWarn("chat-"+sessionID))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload prior conversation so the agent has context across turns (each Chat is
	// a fresh session). First turn: no file yet → Resume loads nothing and proceeds.
	// A corrupt or unreadable transcript stops the turn before any model or tool call.
	if c.tx != nil {
		if err := resumeSession(s, opts.SessionID); err != nil {
			return "", err
		}
	}
	// re-unlock skill-gated MCPs from prior Skill() calls in the reloaded history so
	// revealed tools stay callable across the fresh session.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	forward := func(r db.Activity) {
		if emit != nil {
			r.Worker = agentKey
			emit(r)
		}
	}
	if agentKey != "auto" {
		text, _, err := captureRunSession(ctx, s, message, forward)
		return text, err
	}
	// Auto's first model reply can be a plan or refusal despite an actionable
	// request. Hold that text until we know whether the model actually used a tool.
	// If it did not, continue the same session once with an explicit execution
	// instruction. Never claim a tool ran if both attempts ended in prose.
	var first []db.Activity
	usedTool := false
	text, reason, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if r.Kind == "tool_use" {
			usedTool = true
			for _, pending := range first {
				forward(pending)
			}
			first = nil
		}
		if usedTool {
			forward(r)
		} else {
			first = append(first, r)
		}
	})
	if usedTool || err != nil || reason != harness.ReasonCompleted || ctx.Err() != nil {
		for _, r := range first {
			forward(r)
		}
		return text, err
	}
	retryCtx, requiredTool := withRequiredToolChoice(ctx)
	retryUsedTool := false
	var retryPending []db.Activity
	returnText, _, retryErr := captureRunSession(retryCtx, s,
		"Sua resposta anterior apenas descreveu ou recusou a tarefa, sem chamar nenhuma ferramenta. "+
			"Se o pedido do operador requer uma ação, execute agora a primeira ação concreta usando uma ferramenta disponível "+
			"(Bash/Read/Write/Edit ou a ferramenta de missão adequada), acompanhe o resultado e continue a tarefa. "+
			"Não entregue outro plano no lugar da execução. Se uma ação não puder ser feita, explique o bloqueio concreto.", func(r db.Activity) {
			if r.Kind == "tool_use" {
				requiredTool.active.Store(false)
				retryUsedTool = true
				for _, pending := range retryPending {
					forward(pending)
				}
				retryPending = nil
			}
			if retryUsedTool {
				forward(r)
			} else {
				retryPending = append(retryPending, r)
			}
		})
	if retryUsedTool {
		return returnText, retryErr
	}
	if retryErr != nil || !requiredTool.attempted.Load() {
		for _, pending := range retryPending {
			forward(pending)
		}
		return returnText, retryErr
	}
	return "", errors.New("o modelo respondeu sem executar uma ferramenta; nenhuma ação foi realizada nesta tentativa")
}
