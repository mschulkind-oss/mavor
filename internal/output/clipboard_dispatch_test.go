package output

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestClipboardDispatch(t *testing.T) {
	for _, text := range []string{"  hello\n world\t", "--clear $(echo bad)\n café 世界", " \n\t"} {
		t.Run(text, func(t *testing.T) {
			calls := 0
			c := &Clipboard{Run: func(ctx context.Context, name string, args []string, stdin []byte) error {
				calls++
				if name != "wl-copy" || !reflect.DeepEqual(args, []string{"--type", "text/plain;charset=utf-8"}) || string(stdin) != CleanText(text) {
					t.Fatalf("unexpected command: %s %q %q", name, args, stdin)
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > ClipboardLaunchTimeout {
					t.Fatal("missing bounded deadline")
				}
				return nil
			}}
			if err := c.Emit(context.Background(), text); err != nil {
				t.Fatal(err)
			}
			want := 1
			if CleanText(text) == "" {
				want = 0
			}
			if calls != want {
				t.Fatalf("calls = %d", calls)
			}
		})
	}
}

func TestClipboardErrors(t *testing.T) {
	sentinel := errors.New("copy failed")
	c := &Clipboard{Run: func(context.Context, string, []string, []byte) error { return sentinel }}
	if err := c.Emit(context.Background(), "hello"); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		if canceled {
			cancel()
		}
		defer cancel()
		c.Run = func(ctx context.Context, _ string, _ []string, _ []byte) error { <-ctx.Done(); return ctx.Err() }
		want := context.DeadlineExceeded
		if canceled {
			want = context.Canceled
		}
		if err := c.Emit(ctx, "hello"); !errors.Is(err, want) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestClipboardLaunchDeadline(t *testing.T) {
	c := &Clipboard{Run: func(ctx context.Context, _ string, _ []string, _ []byte) error { <-ctx.Done(); return ctx.Err() }}
	if err := c.Emit(context.Background(), "hello"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}
