package lock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/testutil"
)

const holdEnv = "ZAKWAS_LOCK_TEST_HOME"

func TestMain(m *testing.M) {
	if home := os.Getenv(holdEnv); home != "" {
		if _, err := Acquire(context.Background(), home, "zakwas apply", Options{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("locked")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
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
	waiting := make(chan Holder, 1)
	done := make(chan *Lock, 1)
	go func() {
		l, err := Acquire(context.Background(), home, "zakwas upgrade", Options{
			Poll:   10 * time.Millisecond,
			OnWait: func(h Holder) { waiting <- h },
		})
		if err != nil {
			t.Error(err)
		}
		done <- l
	}()

	var h Holder
	select {
	case h = <-waiting:
	case <-done:
		t.Fatal("acquired without waiting while the lock was held")
	}
	select {
	case <-done:
		t.Fatal("acquired while the lock was held")
	default:
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	second := <-done
	if second == nil {
		return
	}
	defer func() { _ = second.Release() }()

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
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
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

func TestReadOnlyLockFileStillLocks(t *testing.T) {
	home := t.TempDir()
	if err := acquire(t, home, Options{}).Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Path(home), 0o444); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0444 file")
	}

	first, err := Acquire(context.Background(), home, "zakwas apply", Options{NoWait: true})
	if err != nil {
		t.Fatalf("read-only lock file: %v", err)
	}
	defer func() { _ = first.Release() }()

	_, err = Acquire(context.Background(), home, "zakwas apply", Options{NoWait: true})
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Errorf("a second run got %v, want *BusyError", err)
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
}

func TestBlockedRunDoesNotNameAHolderItCannotTrust(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	if err := acquire(t, home, Options{}).Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(home), []byte("4242\nzakwas apply\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Path(home), 0o444); err != nil {
		t.Fatal(err)
	}
	held := acquire(t, home, Options{})
	defer func() { _ = held.Release() }()

	_, err := Acquire(context.Background(), home, "zakwas apply", Options{NoWait: true})

	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("got %v, want *BusyError", err)
	}
	if busy.Holder != (Holder{}) {
		t.Errorf("holder %+v, want none: the file names an earlier run", busy.Holder)
	}
	if want := "another run is in progress: another zakwas run; not waiting (--no-wait)"; err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
}

func TestUnavailable(t *testing.T) {
	testutil.SkipOnWindows(t, "directory and file modes (chmod 0555, 0000) have no Windows equivalent")
	skipIfRoot(t)
	tests := []struct {
		name  string
		setup func(t *testing.T, home string)
	}{
		{"state dir is read-only", func(t *testing.T, home string) {
			mkdir(t, filepath.Dir(Path(home)), 0o555)
		}},
		{"state dir can't be created", func(t *testing.T, home string) {
			mkdir(t, home, 0o555)
		}},
		{"state dir is read-only and holds an unopenable lock file", func(t *testing.T, home string) {
			mkdir(t, filepath.Dir(Path(home)), 0o755)
			if err := os.WriteFile(Path(home), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(Path(home), 0); err != nil {
				t.Fatal(err)
			}
			mkdir(t, filepath.Dir(Path(home)), 0o555)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			if err := os.Mkdir(home, 0o755); err != nil {
				t.Fatal(err)
			}
			tt.setup(t, home)

			l, err := Acquire(context.Background(), home, "zakwas apply", Options{})

			if l != nil {
				t.Fatal("got a lock, want none")
			}
			var unavailable *UnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("got %v, want *UnavailableError", err)
			}
			if !errors.Is(err, fs.ErrPermission) {
				t.Errorf("%v does not wrap the permission error", err)
			}
			if unavailable.Path != Path(home) || !strings.Contains(err.Error(), Path(home)) {
				t.Errorf("error %q does not name %s", err, Path(home))
			}
			if strings.Contains(err.Error(), "no such file") {
				t.Errorf("error %q hides the permission error", err)
			}
		})
	}
}

func mkdir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func TestReleaseNilLock(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestBlockedRunIgnoresAHolderThatIsGone(t *testing.T) {
	skipIfRoot(t)
	home := t.TempDir()
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	if err := acquire(t, home, Options{}).Release(); err != nil {
		t.Fatal(err)
	}
	stale := fmt.Sprintf("%d\nzakwas apply\n", gone.Process.Pid)
	if err := os.WriteFile(Path(home), []byte(stale), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Path(home), 0o444); err != nil {
		t.Fatal(err)
	}
	readOnly := acquire(t, home, Options{})
	defer func() { _ = readOnly.Release() }()
	if err := os.Chmod(Path(home), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Acquire(context.Background(), home, "zakwas apply", Options{NoWait: true})

	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("got %v, want *BusyError", err)
	}
	if busy.Holder != (Holder{}) {
		t.Errorf("holder %+v, want none: its pid %d is gone", busy.Holder, gone.Process.Pid)
	}
}

func TestUnopenableLockFileFailsInAWritableDir(t *testing.T) {
	testutil.SkipOnWindows(t, "directory and file modes (chmod 0555, 0000) have no Windows equivalent")
	skipIfRoot(t)
	home := t.TempDir()
	if err := acquire(t, home, Options{}).Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Path(home), 0); err != nil {
		t.Fatal(err)
	}

	l, err := Acquire(context.Background(), home, "zakwas apply", Options{})

	if l != nil {
		t.Fatal("got a lock, want none")
	}
	var fileErr *FileError
	if !errors.As(err, &fileErr) {
		t.Fatalf("got %v, want *FileError", err)
	}
	var unavailable *UnavailableError
	if errors.As(err, &unavailable) {
		t.Errorf("%v must not be the warn-and-continue error", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("%v does not wrap the permission error", err)
	}
	for _, want := range []string{Path(home), "permission denied", "another user", "remove it or chmod it"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}
