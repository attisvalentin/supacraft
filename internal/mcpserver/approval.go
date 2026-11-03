package mcpserver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// approvalKey signs the request state of an approval prompt, so an
// approval is only accepted for the action this server asked about.
var approvalKey = func() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}()

const approvalInput = "approve"

// approval is an action the user must approve before a tool does it:
// changes to a central server and anything destructive.
type approval struct {
	action  []string // the tool and its arguments; the approval is bound to them
	message string   // what the user is asked
	title   string   // the checkbox label
	command string   // what the user can run instead when there's no prompt
}

// shellCommand joins args into a command line the user can paste: each
// argument is single-quoted unless it is plainly safe, so values the agent
// chose can't add shell syntax.
func shellCommand(args ...string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if safeArg.MatchString(a) {
			quoted[i] = a
		} else {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func (a approval) state() string {
	mac := hmac.New(sha256.New, approvalKey)
	mac.Write([]byte(strings.Join(a.action, "\x00")))
	return hex.EncodeToString(mac.Sum(nil))
}

// approved asks the human to approve a in the client (MCP elicitation, as
// an input request; the client calls the tool again with the answer). The
// agent can't answer it. It reports true once approved; otherwise it
// returns the result the tool should return: the prompt itself, a refusal,
// or, for clients without elicitation, the command for the user to run.
func approved(req *mcp.CallToolRequest, a approval) (*mcp.CallToolResult, bool) {
	if ip := req.Session.InitializeParams(); ip == nil || ip.Capabilities == nil || ip.Capabilities.Elicitation == nil {
		return text(fmt.Sprintf("Not done: this client can't ask the user for approval. The user can run:\n  %s", a.command)), false
	}
	answer, _ := req.Params.InputResponses[approvalInput].(*mcp.ElicitResult)
	if answer == nil {
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{approvalInput: &mcp.ElicitParams{
				Mode:    "form",
				Message: a.message,
				RequestedSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{"approve": map[string]any{"type": "boolean", "title": a.title}},
					"required":   []string{"approve"},
				},
			}},
			RequestState: a.state(),
		}, false
	}
	if !hmac.Equal([]byte(req.Params.RequestState), []byte(a.state())) || answer.Action != "accept" || answer.Content["approve"] != true {
		return text("Not done: the user did not approve it. Don't retry unless they ask again."), false
	}
	return nil, true
}
