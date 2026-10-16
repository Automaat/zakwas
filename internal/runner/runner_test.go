package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecRun(t *testing.T) {
	ctx := context.Background()
	e := NewExec()

	res, err := e.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", `printf "$FOO"; cat; echo err >&2; exit 3`}, Env: []string{"FOO=hi-"}, Stdin: "in"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "hi-in" || strings.TrimSpace(res.Stderr) != "err" || res.ExitCode != 3 {
		t.Errorf("res = %+v", res)
	}

	if _, err := e.Run(ctx, Cmd{Name: "definitely-not-a-binary-zakwas"}); err == nil {
		t.Error("expected start error")
	}
}

func TestExecRunDir(t *testing.T) {
	dir := t.TempDir()
	out, err := Output(context.Background(), NewExec(), Cmd{Name: "pwd", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), dir) {
		t.Errorf("pwd = %q, want suffix %q", out, dir)
	}
}

func TestExecStream(t *testing.T) {
	var stdout, stderr bytes.Buffer
	e := &Exec{Stdout: &stdout, Stderr: &stderr}
	res, err := e.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "echo out; echo err >&2"}, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "" || stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Errorf("res = %+v, stdout %q, stderr %q", res, stdout.String(), stderr.String())
	}
}

func TestResultErr(t *testing.T) {
	c := Cmd{Name: "x", Args: []string{"y"}}
	if err := (Result{}).Err(c); err != nil {
		t.Errorf("zero exit: %v", err)
	}
	tests := []struct {
		res  Result
		want string
	}{
		{Result{ExitCode: 2, Stderr: "bad\n"}, "x y: exit 2: bad"},
		{Result{ExitCode: 1, Stdout: "only stdout"}, "x y: exit 1: only stdout"},
	}
	for _, tt := range tests {
		if err := tt.res.Err(c); err == nil || err.Error() != tt.want {
			t.Errorf("Err = %v, want %q", err, tt.want)
		}
	}
}

func TestExecCancelInterruptsChild(t *testing.T) {
	dir := t.TempDir()
	ready, cleaned := filepath.Join(dir, "ready"), filepath.Join(dir, "cleaned")
	script := `trap 'touch "$CLEANED"; exit 130' INT; touch "$READY"; while :; do sleep 0.05; done`

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(ready); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	start := time.Now()
	_, err := NewExec().Run(ctx, Cmd{Name: "sh", Args: []string{"-c", script}, Env: []string{"READY=" + ready, "CLEANED=" + cleaned}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(cleaned); err != nil {
		t.Errorf("child was not interrupted gracefully: %v", err)
	}
	if d := time.Since(start); d >= WaitDelay {
		t.Errorf("took %s; the child should exit on SIGINT, not after the kill delay", d)
	}
}

func TestExecStreamStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if _, err := w.WriteString("from the parent\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	saved := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = saved }()

	for _, tc := range []struct {
		name    string
		noStdin bool
		want    string
	}{{"inherited", false, "got from the parent\n"}, {"closed for a protocol on stdin", true, "none\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			e := &Exec{Stdout: &out, Stderr: &out, NoStdin: tc.noStdin}
			if _, err := e.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", `if read line; then echo "got $line"; else echo none; fi`}, Stream: true}); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.want {
				t.Errorf("output %q, want %q", out.String(), tc.want)
			}
		})
	}
}

func TestExecGroupInterruptsChildren(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewExec().Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "sleep 20; true"}, Group: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s; the forked sleep outlived the timeout", d)
	}
}
