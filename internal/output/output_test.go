package output

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
)

type recordedCall struct {
	name  string
	args  []string
	stdin string
}

func recorder() (Runner, *[]recordedCall) {
	var calls []recordedCall
	r := func(_ context.Context, name string, args []string, stdin []byte) error {
		calls = append(calls, recordedCall{name: name, args: args, stdin: string(stdin)})
		return nil
	}
	return r, &calls
}

func TestEmitInvokesWtypeAndWlCopy(t *testing.T) {
	run, calls := recorder()
	w := &Wayland{Run: run}
	if err := w.Emit(t.Context(), "hello"); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	want := []recordedCall{
		{name: "wtype", args: []string{"--", "hello"}, stdin: ""},
		{name: "wl-copy", args: nil, stdin: "hello"},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %#v, want %#v", *calls, want)
	}
}

func TestEmitNormalizesNewlines(t *testing.T) {
	run, calls := recorder()
	w := &Wayland{Run: run}
	if err := w.Emit(t.Context(), "hello\nworld\r\nfrom\tmavor"); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	want := []recordedCall{
		{name: "wtype", args: []string{"--", "hello world from mavor"}, stdin: ""},
		{name: "wl-copy", args: nil, stdin: "hello world from mavor"},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %#v, want %#v", *calls, want)
	}
}

func TestEmitRunsBothEvenIfTypingFails(t *testing.T) {
	var copyCalled bool
	w := &Wayland{Run: func(_ context.Context, name string, _ []string, _ []byte) error {
		if name == "wtype" {
			return errors.New("no Wayland focus")
		}
		copyCalled = true
		return nil
	}}
	err := w.Emit(t.Context(), "x")
	if err == nil {
		t.Fatal("expected typing error to propagate")
	}
	if !copyCalled {
		t.Fatal("wl-copy should still run when wtype fails")
	}
}

func TestEmitJoinsErrors(t *testing.T) {
	wtypeErr := errors.New("wtype boom")
	wlcopyErr := errors.New("wl-copy boom")
	w := &Wayland{Run: func(_ context.Context, name string, _ []string, _ []byte) error {
		if name == "wtype" {
			return wtypeErr
		}
		return wlcopyErr
	}}
	err := w.Emit(t.Context(), "x")
	if !errors.Is(err, wtypeErr) || !errors.Is(err, wlcopyErr) {
		t.Fatalf("joined error %v should wrap both %v and %v", err, wtypeErr, wlcopyErr)
	}
}

func TestMockRecordsCalls(t *testing.T) {
	m := &Mock{}
	if err := m.Emit(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}
	if err := m.Emit(t.Context(), "second"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Calls(), []string{"first", "second"}) {
		t.Fatalf("Calls = %v, want [first second]", m.Calls())
	}
}

func TestCopyOnlyDoesNotType(t *testing.T) {
	var ran []string
	var piped []byte
	w := &Wayland{
		Run: func(_ context.Context, name string, _ []string, stdin []byte) error {
			ran = append(ran, name)
			piped = stdin
			return nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := w.CopyOnly(context.Background(), "recovered text"); err != nil {
		t.Fatalf("CopyOnly: %v", err)
	}
	if len(ran) != 1 || ran[0] != "wl-copy" {
		t.Fatalf("ran %v, want only wl-copy", ran)
	}
	if string(piped) != "recovered text" {
		t.Errorf("piped %q, want the exact text", piped)
	}
}
