package rooted

import (
	"encoding/base64"
	"encoding/json"

	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/transport"
)

// Command constructs the shared one-shot cwd launcher without changing the body.
func Command(root, cwd string, req reach.ExecRequest, rootIdentity string) (string, error) {
	b, e := json.Marshal(map[string]any{"command": req.Command, "env": req.Env, "root_identity": rootIdentity})
	if e != nil {
		return "", e
	}
	return "exec " + transport.ShellQuote(HelperPath) + " rooted-exec " + transport.ShellQuote(root) + " " + transport.ShellQuote(cwd) + " " + transport.ShellQuote(base64.RawURLEncoding.EncodeToString(b)), nil
}
func structRequest(req ExecRequest) reach.ExecRequest {
	return reach.ExecRequest{Command: req.Command, Env: req.Env}
}

// ExecRequest is decoded only by the one-shot Guest launcher.
type ExecRequest struct {
	Command      string            `json:"command"`
	RootIdentity string            `json:"root_identity,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
}
