package main

import "testing"

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"pack"}, {"serve", "-unknown"}, {"serve", "unexpected"}, {"serve", "-db", "/does-not-exist/icons.sqlite"}} {
		if err := run(args); err == nil {
			t.Errorf("run(%q) unexpectedly succeeded", args)
		}
	}
}
