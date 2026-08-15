package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestDistContainsPlaceholderIndex(t *testing.T) {
	dist, err := Dist()
	if err != nil {
		t.Fatal(err)
	}
	data, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Viewer frontend has not been built") {
		t.Fatalf("unexpected embedded index: %q", data)
	}
}
