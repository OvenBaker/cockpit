package main

import (
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
		fatal("usage: cockpit-core daemon|ctl")
	}
	switch os.Args[1] {
	case "daemon":
		daemon(os.Args[2:])
	case "ctl":
		ctl(os.Args[2:])
	default:
		fatal("usage: cockpit-core daemon|ctl")
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
