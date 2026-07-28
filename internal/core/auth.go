package core

import (
	"crypto/subtle"
	"errors"
	"os"
	"path/filepath"
)

type credentialGrant struct {
	Credential   string   `json:"credential"`
	ClientID     string   `json:"clientId"`
	Profile      string   `json:"profile"`
	Capabilities []string `json:"capabilities"`
}
type credentialFile struct {
	Version int               `json:"version"`
	Clients []credentialGrant `json:"clients"`
}
type authenticator struct{ grants []credentialGrant }

func loadAuthenticator(path string) (*authenticator, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("credential file must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("credential file must be a private regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f credentialFile
	if err = strictJSON(b, &f); err != nil || f.Version != 1 || len(f.Clients) == 0 || len(f.Clients) > 64 {
		return nil, errors.New("invalid credential file")
	}
	seenCredential, seenClient := map[string]bool{}, map[string]bool{}
	for _, g := range f.Clients {
		if g.Credential == "" || len(g.Credential) > 512 || len(g.ClientID) > 128 || !validClaimedProfile(g.Profile) || len(g.Capabilities) == 0 || seenCredential[g.Credential] || (g.ClientID != "" && seenClient[g.ClientID]) {
			return nil, errors.New("invalid credential grant")
		}
		seenCredential[g.Credential] = true
		if g.ClientID != "" {
			seenClient[g.ClientID] = true
		}
		allowed := profileCapabilities(g.Profile)
		seenCap := map[string]bool{}
		for _, c := range g.Capabilities {
			if !has(allowed, c) || seenCap[c] {
				return nil, errors.New("invalid credential capability")
			}
			seenCap[c] = true
		}
	}
	return &authenticator{grants: f.Clients}, nil
}

func (a *authenticator) verify(credential, clientID, claimedProfile string) (credentialGrant, bool) {
	if a == nil {
		return credentialGrant{}, false
	}
	for _, g := range a.grants {
		// Compare every configured candidate without using a map keyed by a
		// secret. This is a local boundary, but avoids an avoidable token oracle.
		match := subtle.ConstantTimeCompare([]byte(g.Credential), []byte(credential)) == 1
		if match && (g.ClientID == "" || g.ClientID == clientID) && g.Profile == claimedProfile {
			return g, true
		}
	}
	return credentialGrant{}, false
}

func profileCapabilities(profile string) []string {
	switch profile {
	case "local-operator":
		return []string{"state:read", "operations:read", "events:wait", "capture:sanitized", "metadata:write", "interaction:nudge", "interaction:pause", "interaction:compact", "interaction:resume", "coord:admin", "coord:read", "coord:write"}
	case "mcp-local":
		return []string{"state:read", "operations:read", "events:wait", "capture:sanitized", "metadata:write", "interaction:nudge", "interaction:pause", "interaction:compact", "interaction:resume", "coord:read", "coord:write"}
	// The `orbital` profile is the narrowest grant that lets Orbital's brief-studio reply path work and
	// nothing else: read the grid to locate a pane, take a sanitized capture, and type one bounded notice.
	// It deliberately carries no metadata:write, no operations:read, no events:wait and neither coordination
	// scope — claiming mcp-local instead would grant all five. The name was ALREADY accepted by
	// validClaimedProfile while mapping to an empty capability set here, so a grant claiming it failed
	// credential loading and rejected the WHOLE file, taking controller auth down for every client.
	case "orbital":
		return []string{"state:read", "capture:sanitized", "interaction:nudge"}
	case "read-only":
		return []string{"state:read", "operations:read", "events:wait", "coord:read"}
	default:
		return []string{}
	}
}
