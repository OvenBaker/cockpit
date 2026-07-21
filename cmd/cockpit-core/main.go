package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/gareth/cockpit-core/internal/core"
)

const maxFrame = 1 << 20

func main() {
	if len(os.Args) < 2 {
		fatal("usage: cockpit-core daemon|ctl|mcp-stdio")
	}
	switch os.Args[1] {
	case "daemon":
		daemon(os.Args[2:])
	case "ctl":
		ctl(os.Args[2:])
	case "mcp-stdio":
		mcpStdio(os.Args[2:])
	default:
		fatal("usage: cockpit-core daemon|ctl|mcp-stdio")
	}
}

// mcp-stdio deliberately contains no tmux logic or controller policy. It is
// one-client stdio JSON-RPC translation onto the resident controller's framed
// socket; disconnecting it cannot abandon controller waits or operations.
func mcpStdio(args []string) {
	fs := flag.NewFlagSet("mcp-stdio", flag.ExitOnError)
	socket := fs.String("socket", "", "control socket")
	credential := fs.String("credential", "test-local", "local controller credential")
	fs.Parse(args)
	if *socket == "" {
		fatal("mcp-stdio requires --socket")
	}
	c, err := net.Dial("unix", *socket)
	if err != nil {
		fatal(err.Error())
	}
	defer c.Close()
	if err = writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": map[string]any{"protocol": "1.0", "clientId": "cockpit-mcp", "claimedProfile": "mcp-local", "credential": *credential}}); err != nil {
		fatal(err.Error())
	}
	if _, err = readFrame(c); err != nil {
		fatal(err.Error())
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	write := func(v any) { b, _ := json.Marshal(v); _, _ = out.Write(append(b, '\n')); _ = out.Flush() }
	resolve := func(args map[string]any) (map[string]any, error) {
		if ref, ok := args["paneRef"].(string); ok && ref != "" {
			return map[string]any{"paneRef": ref}, nil
		}
		loc, ok := args["locator"].(string)
		if !ok || loc == "" {
			return nil, errors.New("paneRef or canonical locator is required")
		}
		if err := writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "resolve", "method": "pane.resolve", "params": map[string]any{"canonical": loc}}); err != nil {
			return nil, err
		}
		b, err := readFrame(c)
		if err != nil {
			return nil, err
		}
		var r map[string]any
		if json.Unmarshal(b, &r) != nil {
			return nil, errors.New("invalid controller response")
		}
		if r["error"] != nil {
			return nil, fmt.Errorf("%v", r["error"])
		}
		result, _ := r["result"].(map[string]any)
		ref, _ := result["paneRef"].(string)
		if ref == "" {
			return nil, errors.New("resolution failed")
		}
		return map[string]any{"paneRef": ref}, nil
	}
	s := bufio.NewScanner(os.Stdin)
	s.Buffer(make([]byte, 4096), maxFrame)
	for s.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal(s.Bytes(), &req) != nil || req.JSONRPC != "2.0" {
			write(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
			continue
		}
		if req.Method == "notifications/initialized" {
			continue
		}
		if req.Method == "initialize" {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"protocolVersion": "2025-03-26", "serverInfo": map[string]any{"name": "cockpit-core", "version": "1.0"}, "capabilities": map[string]any{"tools": map[string]any{}}}})
			continue
		}
		if req.Method == "tools/list" {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"tools": core.MCPTools()}})
			continue
		}
		if req.Method != "tools/call" {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &call) != nil || call.Arguments == nil {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": -32602, "message": "invalid params"}})
			continue
		}
		method, ok := core.MCPMethod(call.Name)
		if !ok {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"content": []map[string]string{{"type": "text", "text": "unknown tool"}}, "isError": true}})
			continue
		}
		if method != "state.snapshot" && method != "capabilities.get" && method != "wait.for_change" {
			if target, e := resolve(call.Arguments); e != nil {
				write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"content": []map[string]string{{"type": "text", "text": e.Error()}}, "isError": true}})
				continue
			} else {
				call.Arguments["paneRef"] = target["paneRef"]
			}
		}
		params, _ := json.Marshal(call.Arguments)
		if err := writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "tool", "method": method, "params": json.RawMessage(params)}); err != nil {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": -32001, "message": "controller unavailable"}})
			continue
		}
		b, err := readFrame(c)
		if err != nil {
			write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": -32001, "message": "controller unavailable"}})
			continue
		}
		var response map[string]any
		_ = json.Unmarshal(b, &response)
		payload, _ := json.Marshal(response["result"])
		isError := response["error"] != nil
		if isError {
			payload, _ = json.Marshal(response["error"])
		}
		write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"content": []map[string]string{{"type": "text", "text": string(payload)}}, "structuredContent": response["result"], "isError": isError}})
	}
}

func daemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	root := fs.String("test-root", "", "isolated test root")
	socket := fs.String("socket", "", "control socket")
	tmuxSocket := fs.String("tmux-socket", "", "throwaway tmux socket")
	fs.Parse(args)
	if *root == "" || *socket == "" || *tmuxSocket == "" {
		fatal("daemon requires --test-root --socket --tmux-socket")
	}
	d, err := core.NewDaemon(*root, *socket, *tmuxSocket)
	if err != nil {
		fatal(err.Error())
	}
	defer d.Close()
	if err := d.Serve(); err != nil {
		fatal(err.Error())
	}
}

func ctl(args []string) {
	fs := flag.NewFlagSet("ctl", flag.ExitOnError)
	socket := fs.String("socket", "", "control socket")
	credential := fs.String("credential", "test-local", "test credential")
	fs.Parse(args)
	if *socket == "" || fs.NArg() < 1 {
		fatal("ctl requires --socket METHOD [JSON params]")
	}
	params := json.RawMessage(`{}`)
	if fs.NArg() > 1 {
		params = json.RawMessage(fs.Arg(1))
	}
	c, err := net.Dial("unix", *socket)
	if err != nil {
		fatal(err.Error())
	}
	defer c.Close()
	if err := writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "open", "method": "session.open", "params": map[string]any{"protocol": "1.0", "clientId": "cockpitctl", "claimedProfile": "local-operator", "credential": *credential}}); err != nil {
		fatal(err.Error())
	}
	if _, err := readFrame(c); err != nil {
		fatal(err.Error())
	}
	if err := writeFrame(c, map[string]any{"jsonrpc": "2.0", "id": "ctl", "method": fs.Arg(0), "params": params}); err != nil {
		fatal(err.Error())
	}
	r, err := readFrame(c)
	if err != nil {
		fatal(err.Error())
	}
	os.Stdout.Write(append(r, '\n'))
}

func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return errors.New("frame too large")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, err = w.Write(h[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
func readFrame(r io.Reader) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n > maxFrame {
		return nil, errors.New("frame too large")
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}
func fatal(s string) { fmt.Fprintln(os.Stderr, s); os.Exit(1) }
