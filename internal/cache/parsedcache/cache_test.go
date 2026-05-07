package parsedcache

import (
	"testing"
)

type sample struct {
	Name string
	Tags []string
}

func TestStoreAndLoad(t *testing.T) {
	c, err := New[sample](t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := sample{Name: "x", Tags: []string{"a", "b"}}
	if err := c.Store([]byte("source-bytes"), in); err != nil {
		t.Fatal(err)
	}

	out, ok, err := c.Load([]byte("source-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Load returned ok=false on warm cache")
	}
	if out.Name != "x" || len(out.Tags) != 2 {
		t.Errorf("Loaded value mismatch: %+v", out)
	}
}

func TestLoadMissReturnsFalse(t *testing.T) {
	c, _ := New[sample](t.TempDir())
	_, ok, err := c.Load([]byte("never-seen"))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Load returned ok=true on cold cache")
	}
}

func TestDifferentSourceProducesDifferentEntry(t *testing.T) {
	c, _ := New[sample](t.TempDir())
	_ = c.Store([]byte("a"), sample{Name: "A"})
	_ = c.Store([]byte("b"), sample{Name: "B"})

	a, _, _ := c.Load([]byte("a"))
	b, _, _ := c.Load([]byte("b"))
	if a.Name != "A" || b.Name != "B" {
		t.Errorf("entries collided: a=%+v b=%+v", a, b)
	}
}
