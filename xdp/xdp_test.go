package xdp

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func fakeRunner(t *testing.T, listJSON string, calls *[]string, failOn string) func() {
	t.Helper()
	prev := runner
	runner = func(args ...string) ([]byte, error) {
		call := strings.Join(args, " ")
		*calls = append(*calls, call)
		if failOn != "" && strings.HasPrefix(call, failOn) {
			return nil, errors.New("boom")
		}
		if args[0] == "list" {
			return []byte(listJSON), nil
		}
		return nil, nil
	}
	return func() { runner = prev }
}

func TestNormalizeRejectsBadInput(t *testing.T) {
	if _, err := Normalize(map[string]string{"70000": "source"}); err == nil {
		t.Fatal("expected invalid port error")
	}
	if _, err := Normalize(map[string]string{"27015": "quake"}); err == nil {
		t.Fatal("expected invalid profile error")
	}
	got, err := Normalize(map[string]string{" 27015 ": " CS16 "})
	if err != nil || !reflect.DeepEqual(got, map[int]string{27015: "cs16"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestSyncAppliesOnlyTheDifference(t *testing.T) {
	var calls []string
	defer fakeRunner(t, `{"27015":"cs16","27019":"generic","9999":"source","27021":"source"}`, &calls, "")()

	res, err := Sync(map[int]string{27015: "cs16", 27019: "source", 27021: "source", 27023: "source", 28960: "cod2"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"list --json", "set 28960 cod2", "set 27019,27023 source", "del 9999"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if len(res.Added) != 2 || len(res.Updated) != 1 || len(res.Removed) != 1 || res.Unchanged != 2 {
		t.Fatalf("unexpected result %+v", res)
	}
	if res.Updated[0] != (Change{Port: 27019, From: "generic", To: "source"}) {
		t.Fatalf("unexpected update %+v", res.Updated[0])
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	var calls []string
	defer fakeRunner(t, `{"27015":"cs16"}`, &calls, "")()

	res, err := Sync(map[int]string{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"list --json"}) || !res.DryRun || len(res.Removed) != 1 {
		t.Fatalf("calls %v result %+v", calls, res)
	}
}

func TestSyncStopsOnCommandError(t *testing.T) {
	var calls []string
	defer fakeRunner(t, `{"1":"source"}`, &calls, "set")()

	if _, err := Sync(map[int]string{2: "source"}, false); err == nil {
		t.Fatal("expected error")
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "del") {
			t.Fatal("must not remove ports after a failed set")
		}
	}
}

func TestChunksSplitLongLists(t *testing.T) {
	ports := make([]int, 0, 250)
	for p := 1; p <= 250; p++ {
		ports = append(ports, p)
	}
	got := chunks(ports)
	if len(got) != 3 || !strings.HasPrefix(got[1], "101,") || !strings.HasSuffix(got[2], ",250") {
		t.Fatalf("unexpected chunks %v", got)
	}
}
