package config

import (
	"testing"
	"time"
)

func TestDurationUnmarshal(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"10s", 10 * time.Second, false},
		{"2m", 2 * time.Minute, false},
		{"500ms", 500 * time.Millisecond, false},
		{"1h30m", 90 * time.Minute, false},
		{"", 0, false},
		{"nope", 0, true},
	}
	for _, c := range cases {
		var d Duration
		err := d.UnmarshalText([]byte(c.in))
		if (err != nil) != c.err {
			t.Fatalf("UnmarshalText(%q) err=%v wantErr=%v", c.in, err, c.err)
		}
		if !c.err && time.Duration(d) != c.want {
			t.Fatalf("UnmarshalText(%q)=%v want %v", c.in, time.Duration(d), c.want)
		}
	}
}

func TestDurationMarshal(t *testing.T) {
	d := Duration(45 * time.Second)
	b, err := d.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "45s" {
		t.Fatalf("marshal=%q", string(b))
	}
}

func TestDurationStd(t *testing.T) {
	d := Duration(7 * time.Second)
	if d.Std() != 7*time.Second {
		t.Fatalf("std=%v", d.Std())
	}
}
