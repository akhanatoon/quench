package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/akhanatoon/quench/internal/config"
	"github.com/akhanatoon/quench/internal/runner"
	"github.com/akhanatoon/quench/internal/sandbox"
)

const DefaultProtocolVersion = "2024-11-05"

type Server struct {
	defaultDir string
	version    string
	writeMu    sync.Mutex
}

func NewServer(defaultDir string, version string) *Server {
	if version == "" {
		version = "0.1.0"
	}
	return &Server{
		defaultDir: defaultDir,
		version:    version,
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callToolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Serve runs the JSON-RPC 2.0 MCP loop over in/out until EOF or ctx cancellation.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = s.writeResponse(out, rpcResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}

		// JSON-RPC notifications have no id and must not receive a response
		if len(req.ID) == 0 || strings.HasPrefix(req.Method, "notifications/") {
			continue
		}

		res := s.handleRequest(ctx, req)
		if err := s.writeResponse(out, res); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) writeResponse(out io.Writer, resp rpcResponse) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	raw, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n", raw)
	return err
}

func (s *Server) handleRequest(ctx context.Context, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		proto := DefaultProtocolVersion
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 && json.Unmarshal(req.Params, &initParams) == nil && initParams.ProtocolVersion != "" {
			proto = initParams.ProtocolVersion
		}
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": proto,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "quench",
					"version": s.version,
				},
			},
		}

	case "ping":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}

	case "tools/list":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": toolDefinitions(),
			},
		}

	case "tools/call":
		var p callToolParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "invalid tools/call params: " + err.Error()},
			}
		}
		toolRes, rpcErr := s.dispatchTool(ctx, p)
		if rpcErr != nil {
			return rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   rpcErr,
			}
		}
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  toolRes,
		}

	default:
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

func (s *Server) dispatchTool(ctx context.Context, p callToolParams) (*callToolResult, *rpcError) {
	switch p.Name {
	case "verify_changes":
		var args struct {
			ProjectDir       string   `json:"project_dir"`
			BaseRef          string   `json:"base_ref"`
			Checks           []string `json:"checks"`
			Mode             string   `json:"mode"`
			FailFast         bool     `json:"fail_fast"`
			ChangedLinesOnly bool     `json:"changed_lines_only"`
			NoCache          bool     `json:"no_cache"`
		}
		if len(p.Arguments) > 0 {
			if err := json.Unmarshal(p.Arguments, &args); err != nil {
				return errorToolResult("invalid arguments: " + err.Error()), nil
			}
		}

		projDir, err := s.resolveDir(args.ProjectDir)
		if err != nil {
			return errorToolResult(err.Error()), nil
		}
		cfg, _, err := config.Load(projDir)
		if err != nil {
			return errorToolResult(err.Error()), nil
		}

		mode := cfg.Mode
		explicitDocker := false
		if args.Mode != "" {
			if args.Mode != config.ModeDocker && args.Mode != config.ModeLocal {
				return errorToolResult(fmt.Sprintf("invalid mode %q (expected 'docker' or 'local')", args.Mode)), nil
			}
			mode = args.Mode
			explicitDocker = args.Mode == config.ModeDocker
		}

		var sb sandbox.Sandbox
		if mode == config.ModeLocal {
			sb = sandbox.NewLocal(projDir, cfg.Stack)
		} else {
			if !explicitDocker && sandbox.CheckDaemon(ctx) != nil {
				// Graceful auto-fallback to local mode when Docker daemon is offline and docker wasn't explicitly forced
				sb = sandbox.NewLocal(projDir, cfg.Stack)
			} else {
				sb = sandbox.NewDockerPool(projDir, cfg)
				sandbox.BestEffortGC()
			}
		}

		report, err := runner.Run(ctx, cfg, sb, runner.Options{
			ProjectDir:       projDir,
			BaseRef:          args.BaseRef,
			OnlyChecks:       args.Checks,
			FailFast:         args.FailFast,
			ChangedLinesOnly: args.ChangedLinesOnly,
			NoCache:          args.NoCache,
		})
		if err != nil {
			var daemonErr *sandbox.DaemonUnavailableError
			if errors.As(err, &daemonErr) {
				return errorToolResult(daemonErr.Error()), nil
			}
			return errorToolResult(err.Error()), nil
		}
		return jsonToolResult(report)

	case "sandbox_status":
		var args struct {
			ProjectDir string `json:"project_dir"`
		}
		if len(p.Arguments) > 0 {
			_ = json.Unmarshal(p.Arguments, &args)
		}
		projDir, err := s.resolveDir(args.ProjectDir)
		if err != nil {
			return errorToolResult(err.Error()), nil
		}
		cfg, _, err := config.Load(projDir)
		if err != nil {
			return errorToolResult(err.Error()), nil
		}
		var sb sandbox.Sandbox
		if cfg.Mode == config.ModeLocal {
			sb = sandbox.NewLocal(projDir, cfg.Stack)
		} else {
			sb = sandbox.NewDockerPool(projDir, cfg)
		}
		st, stErr := sb.Status(ctx)
		out := map[string]any{
			"status": st,
		}
		if stErr != nil {
			out["warning"] = stErr.Error()
		}
		return jsonToolResult(out)

	case "sandbox_reset":
		var args struct {
			ProjectDir string `json:"project_dir"`
			PurgeCache bool   `json:"purge_cache"`
		}
		if len(p.Arguments) > 0 {
			_ = json.Unmarshal(p.Arguments, &args)
		}
		projDir, err := s.resolveDir(args.ProjectDir)
		if err != nil {
			return errorToolResult(err.Error()), nil
		}
		cfg, _, err := config.Load(projDir)
		if err != nil {
			return errorToolResult(err.Error()), nil
		}
		sb := sandbox.NewDockerPool(projDir, cfg)
		if err := sb.Reset(ctx, args.PurgeCache); err != nil {
			return errorToolResult(err.Error()), nil
		}
		return jsonToolResult(map[string]any{
			"reset":          true,
			"container_name": sb.ContainerName(),
			"purged_cache":   args.PurgeCache,
			"project_dir":    filepath.ToSlash(projDir),
		})

	default:
		return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", p.Name)}
	}
}

func (s *Server) resolveDir(override string) (string, error) {
	target := strings.TrimSpace(override)
	if target == "" {
		target = s.defaultDir
	}
	if target == "" {
		target = "."
	}
	return filepath.Abs(target)
}

func jsonToolResult(v any) (*callToolResult, *rpcError) {
	raw, err := json.Marshal(v)
	if err != nil {
		return errorToolResult("marshal result: " + err.Error()), nil
	}
	return &callToolResult{
		Content: []toolContent{
			{Type: "text", Text: string(raw)},
		},
	}, nil
}

func errorToolResult(msg string) *callToolResult {
	payload, _ := json.Marshal(map[string]string{"error": msg})
	return &callToolResult{
		Content: []toolContent{
			{Type: "text", Text: string(payload)},
		},
		IsError: true,
	}
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "verify_changes",
			"description": "Run fast, diff-aware lint, typecheck, and test verification on changed git files in a warm Docker sandbox or locally. Returns a token-lean JSON report of failures only.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_dir": map[string]any{
						"type":        "string",
						"description": "Optional path to target git repository. Defaults to server working directory.",
					},
					"base_ref": map[string]any{
						"type":        "string",
						"description": "Optional git ref to compare against (e.g. 'main' or 'HEAD~1').",
					},
					"checks": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional subset of check names to run (e.g. ['lint', 'test']).",
					},
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{"docker", "local"},
						"description": "Optional execution mode override ('docker' warm sandbox or 'local' host).",
					},
					"fail_fast": map[string]any{
						"type":        "boolean",
						"description": "Cancel remaining checks immediately on the first failure.",
					},
					"changed_lines_only": map[string]any{
						"type":        "boolean",
						"description": "Filter non-test linter/typecheck diagnostics to lines modified in git diff.",
					},
					"no_cache": map[string]any{
						"type":        "boolean",
						"description": "Bypass memoization cache and force re-execution.",
					},
				},
			},
		},
		{
			"name":        "sandbox_status",
			"description": "Inspect the warm Docker sandbox container status, image, uptime, dependency sync state, and cache volumes for a project.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_dir": map[string]any{
						"type":        "string",
						"description": "Optional path to target git repository.",
					},
				},
			},
		},
		{
			"name":        "sandbox_reset",
			"description": "Reset the warm Docker sandbox container (and optionally purge named dependency cache volumes) after major toolchain or dependency changes.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_dir": map[string]any{
						"type":        "string",
						"description": "Optional path to target git repository.",
					},
					"purge_cache": map[string]any{
						"type":        "boolean",
						"description": "If true, also remove cached dependency volumes (go mod, node_modules, pip).",
					},
				},
			},
		},
	}
}
