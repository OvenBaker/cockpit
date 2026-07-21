package core_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

type corpusCase struct {
	Name    string `json:"name"`
	Valid   bool   `json:"valid"`
	Request any    `json:"request"`
	Error   string `json:"error"`
}

func TestProtocolSchemaGoldenCorpus(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	schemaBytes, err := os.ReadFile(filepath.Join(root, "protocol", "v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("schema JSON: %v", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("schema resolution: %v", err)
	}
	errorSchema, err := schema.Defs["errorResponse"].Resolve(nil)
	if err != nil {
		t.Fatalf("error response schema resolution: %v", err)
	}
	corpusBytes, err := os.ReadFile(filepath.Join(root, "protocol", "golden", "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus []corpusCase
	if err := json.Unmarshal(corpusBytes, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus) < 15 {
		t.Fatal("golden corpus is incomplete")
	}
	for _, tc := range corpus {
		err := resolved.Validate(tc.Request)
		if tc.Valid && err != nil {
			t.Errorf("%s rejected: %v", tc.Name, err)
		}
		if !tc.Valid && err == nil {
			t.Errorf("%s accepted unexpectedly", tc.Name)
		}
		if !tc.Valid && tc.Error == "" {
			t.Errorf("%s lacks expected protocol error", tc.Name)
		}
		if !tc.Valid {
			response := map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32001, "message": tc.Error, "data": map[string]any{"code": tc.Error}}}
			if err := errorSchema.Validate(response); err != nil {
				t.Errorf("%s error response rejected: %v", tc.Name, err)
			}
		}
	}
}
