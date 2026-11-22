package anon

import (
	"strings"
	"testing"
)

func TestHash(t *testing.T) {
	h := New("secret")
	a := h.Hash("host", "gpu-node-17")
	if a != h.Hash("host", "gpu-node-17") {
		t.Fatal("hash not stable")
	}
	if !strings.HasPrefix(a, "host_") || len(a) != len("host_")+12 {
		t.Fatalf("unexpected format %q", a)
	}
	if a == New("other").Hash("host", "gpu-node-17") {
		t.Fatal("salt ignored")
	}
	if h.Hash("host", "") != "" {
		t.Fatal("empty value should stay empty")
	}
	var none *Hasher
	if none.Hash("host", "x") != "x" {
		t.Fatal("nil hasher should pass through")
	}
}
