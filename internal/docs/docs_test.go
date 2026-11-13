package docs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSpecChangesOnlyTitleAndDescription(t *testing.T) {
	spec, err := Spec()
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(spec, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contract, &want); err != nil {
		t.Fatal(err)
	}
	gi, wi := got["info"].(map[string]any), want["info"].(map[string]any)
	if gi["title"] != "Loan API (Go, PostgreSQL)" {
		t.Fatalf("title %v", gi["title"])
	}
	first, _, _ := strings.Cut(gi["description"].(string), "\n")
	if !strings.Contains(first, "Go") || !strings.Contains(first, "PostgreSQL") {
		t.Fatalf("first line %q", first)
	}
	if !strings.HasSuffix(gi["description"].(string), wi["description"].(string)) {
		t.Fatal("original description lost")
	}
	delete(gi, "title")
	delete(gi, "description")
	delete(wi, "title")
	delete(wi, "description")
	if !reflect.DeepEqual(got, want) {
		t.Fatal("document differs beyond info.title and info.description")
	}
}

func TestAssets(t *testing.T) {
	for _, n := range []string{"swagger-ui.css", "swagger-ui-bundle.js", "swagger-ui-standalone-preset.js", "favicon-32x32.png"} {
		if b, ok := Asset(n); !ok || len(b) == 0 {
			t.Errorf("%s missing", n)
		}
	}
	if _, ok := Asset("index.html"); ok {
		t.Error("only the allow-listed files may be served")
	}
	if !strings.Contains(string(Index()), "swagger-ui") || strings.Contains(string(Index()), "<script>") {
		t.Error("index must mention swagger-ui and have no inline script")
	}
}
