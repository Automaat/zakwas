package lock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

const holdEnv = "ZAKWAS_LOCK_TEST_HOME"

func TestMain(m *testing.M) {
	if home := os.Getenv(holdEnv); home != "" {
		if _, err := Acquire(context.Background(), home, "zakwas apply", Options{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("locked")
		select {}
	}
	os.Exit(m.Run())
}

func acquire(t *testing.T, home string, opts Options) *Lock {
	t.Helper()
	l, err := Acquire(context.Background(), home, "zakwas apply", opts)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAcquireAgainAfterRelease(t *testing.T) {
	home := t.TempDir()
	if err := acquire(t, home, Options{}).Release(); err != nil {
		t.Fatal(err)
	}
	if err := acquire(t, home, Options{NoWait: true}).Release(); err != nil {
		t.Fatal(err)
	}
}

func TestNoWaitNamesTheHolder(t *testing.T) {
	home := t.TempDir()
	held := acquire(t, home, Options{})
	defer func() { _ = held.Release() }()

	_, err := Acquire(context.Background(), home, "zakwas upgrade", Options{NoWait: true})

	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("got %v, want *BusyError", err)
	}
	if want := (Holder{PID: os.Getpid(), Command: "zakwas apply"}); busy.Holder != want {
		t.Errorf("holder %+v, want %+v", busy.Holder, want)
	}
	want := fmt.Sprintf(`another run is in progress: "zakwas apply" (pid %d); not waiting (--no-wait)`, os.Getpid())
	if err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
}

func TestWaitsUntilReleased(t *testing.T) {
	home := t.TempDir()
	held := acquire(t, home, Options{})
	var waitedFor atomic.Value
	done := make(chan *Lock)
	go func() {
		l, err := Acquire(context.Background(), home, "zakwas upgrade", Options{
			Poll:   10 * time.Millisecond,
			OnWait: func(h Holder) { waitedFor.Store(h) },
		})
		if err != nil {
			t.Error(err)
		}
		done <- l
	}()

	select {
	case <-done:
		t.Fatal("acquired while the lock was held")
	case <-time.After(300 * time.Millisecond):
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	second := <-done
	if second == nil {
		return
	}
	defer func() { _ = second.Release() }()

	h, _ := waitedFor.Load().(Holder)
	if h.PID != os.Getpid() || h.Command != "zakwas apply" {
		t.Errorf("OnWait got %+v", h)
	}
	if got := readHolder(Path(home)); got.Command != "zakwas upgrade" {
		t.Errorf("lock file names %+v, want the second run", got)
	}
}

func TestWaitStopsWithContext(t *testing.T) {
	home := t.TempDir()
	held := acquire(t, home, Options{})
	defer func() { _ = held.Release() }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := Acquire(ctx, home, "zakwas apply", Options{Poll: 10 * time.Millisecond})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
}

func TestKilledHolderReleasesTheLock(t *testing.T) {
	home := t.TempDir()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), holdEnv+"="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if line, _ := bufio.NewReader(stdout).ReadString('\n'); line != "locked\n" {
		t.Fatalf("holder said %q", line)
	}
	_, err = Acquire(context.Background(), home, "zakwas apply", Options{NoWait: true})
	var busy *BusyError
	if !errors.As(err, &busy) || busy.Holder.PID != cmd.Process.Pid {
		t.Fatalf("got %v, want busy with pid %d", err, cmd.Process.Pid)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	l, err := Acquire(context.Background(), home, "zakwas apply", Options{NoWait: true})
	if err != nil {
		t.Fatalf("lock still held after the holder was killed: %v", err)
	}
	_ = l.Release()
}

func TestHolderString(t *testing.T) {
	tests := []struct {
		h    Holder
		want string
	}{
		{Holder{PID: 7, Command: "zakwas apply"}, `"zakwas apply" (pid 7)`},
		{Holder{PID: 7}, "pid " + strconv.Itoa(7)},
		{Holder{}, "another zakwas run"},
	}
	for _, tt := range tests {
		if got := tt.h.String(); got != tt.want {
			t.Errorf("%+v: got %q, want %q", tt.h, got, tt.want)
		}
	}
}
