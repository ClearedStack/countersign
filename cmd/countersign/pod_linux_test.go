//go:build linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ClearedStack/countersign/internal/muster"
)

// The tests here build the real program and run it the way the pod would:
// nowhere to write but the spool, and a 30 MiB memory budget.

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "countersign-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "countersign")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// budget is the pod's memory budget from the specification.
const budget = 30 << 20

// items fills the inbox to its limit (worker.MaxPending).
const items = 256

type sandbox struct {
	spool, cwd, tmp, home, seed string
}

// newSandbox makes a spool and a seed of the largest edit proposals allowed,
// with the first two calls to Muster failing.
func newSandbox(t *testing.T, n int) sandbox {
	t.Helper()
	root := t.TempDir()
	sb := sandbox{
		spool: filepath.Join(root, "spool"),
		cwd:   filepath.Join(root, "cwd"),
		tmp:   filepath.Join(root, "tmp"),
		home:  filepath.Join(root, "home"),
		seed:  filepath.Join(root, "seed.json"),
	}
	for _, d := range []string{sb.cwd, sb.tmp, sb.home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	seed := muster.Seed{Ownership: []string{"catalog/sample-rate-checker/description"}, Timeouts: 2}
	body := strings.Repeat("Made-up text. ", 4096/14)
	for i := 0; i < n; i++ {
		b, _ := json.Marshal(map[string]string{
			"id": fmt.Sprintf("EP-%04d", i+1), "kind": "proposal", "state": "pending_approval", "lane": "normal",
			"type": "DATA", "target": "catalog/sample-rate-checker/description", "body": body,
		})
		seed.Items = append(seed.Items, b)
	}
	b, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sb.seed, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return sb
}

func (sb sandbox) command(args ...string) *exec.Cmd {
	cmd := exec.Command(binary, append([]string{"-spool", sb.spool}, args...)...)
	cmd.Dir = sb.cwd
	cmd.Env = []string{"TMPDIR=" + sb.tmp, "HOME=" + sb.home}
	return cmd
}

// peak reads the most memory a running process has held so far. It is read
// from the process's own accounting while the process is still alive. The
// figure a parent gets after a child exits is no use here: on Linux it also
// counts the parent's memory at the moment the child was started.
func peak(t *testing.T, pid int) int64 {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatalf("the process ended before its memory could be read")
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			kb, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return kb << 10
		}
	}
	t.Fatal("no peak memory figure")
	return 0
}

// worker starts the loop, waits until it has logged want events of the given
// rule and gone quiet, reads its peak memory, and stops it.
func (sb sandbox) worker(t *testing.T, rule string, want int) (int64, string) {
	t.Helper()
	cmd := sb.command("run", "-standin", sb.seed, "-interval", "5ms")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	seen := 0
	sc := bufio.NewScanner(stderr)
	for seen < want && sc.Scan() {
		log.WriteString(sc.Text() + "\n")
		if strings.Contains(sc.Text(), `"rule":"`+rule+`"`) {
			seen++
		}
	}
	if seen != want {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("the worker logged %d %s events, want %d\n%.2000s", seen, rule, want, log.String())
	}
	time.Sleep(50 * time.Millisecond) // a few more idle ticks
	rss := peak(t, cmd.Process.Pid)
	cmd.Process.Signal(syscall.SIGTERM)
	rest, _ := io.ReadAll(stderr)
	log.Write(rest)
	if err := cmd.Wait(); err != nil {
		t.Errorf("the worker did not stop cleanly on SIGTERM: %v", err)
	}
	return rss, log.String()
}

// listing runs a command whose output is larger than a small pipe. The
// command has loaded everything it needs by the time it writes, and cannot
// finish until the pipe is drained, so its peak memory can be read exactly.
func (sb sandbox) listing(t *testing.T, args ...string) (int64, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	const setPipeSize = 1031 // F_SETPIPE_SZ
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, w.Fd(), setPipeSize, 4096); errno != 0 {
		t.Fatalf("cannot shrink the pipe: %v", errno)
	}
	cmd := sb.command(args...)
	cmd.Stdout = w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	first := make([]byte, 1)
	if _, err := io.ReadFull(r, first); err != nil {
		t.Fatalf("%v printed nothing", args)
	}
	time.Sleep(20 * time.Millisecond) // let it fill the pipe and block
	rss := peak(t, cmd.Process.Pid)
	rest, _ := io.ReadAll(r)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	out := string(first) + string(rest)
	if len(out) <= 2*4096 {
		t.Fatalf("%v printed too little to hold the process open", args)
	}
	return rss, out
}

func (sb sandbox) run(t *testing.T, args ...string) result {
	t.Helper()
	cmd := sb.command(args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return result{code, out.String(), errb.String()}
}

func empty(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Errorf("%s was written to: %d entries", filepath.Base(dir), len(ents))
	}
}

func mib(n int64) float64 { return float64(n) / (1 << 20) }

func TestBuiltProgramStaysInItsBudgetAndItsSpool(t *testing.T) {
	sb := newSandbox(t, items)

	// Muster is down for two ticks, then the inbox fills to its limit.
	fill, log := sb.worker(t, "intake.accept", items)
	if strings.Count(log, `"code":"E_TIMEOUT"`) != 2 {
		t.Errorf("want two timeouts before the inbox fills:\n%.1000s", log)
	}
	if strings.Contains(log, muster.LeakMarker) {
		t.Errorf("the worker printed an address:\n%.1000s", log)
	}

	list, out := sb.listing(t, "list")
	if strings.Count(out, "\n") != items+1 {
		t.Errorf("list shows %d lines", strings.Count(out, "\n"))
	}
	for i := 1; i <= items; i++ {
		outcome := "approve"
		if i%2 == 0 {
			outcome = "decline"
		}
		if r := sb.run(t, "decide", fmt.Sprintf("EP-%04d", i), outcome); r.code != exitOK {
			t.Fatalf("decide %d: %+v", i, r)
		}
	}
	logCmd, out := sb.listing(t, "log")
	if strings.Count(out, `"status":"waiting"`) != items {
		t.Errorf("log shows %d waiting", strings.Count(out, `"status":"waiting"`))
	}

	// A fresh stand-in, as after Muster restarts: the whole spool is
	// delivered, in order.
	drain, log := sb.worker(t, "spool.acknowledged", items)
	next := 1
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `"rule":"spool.acknowledged"`) {
			if !strings.Contains(line, fmt.Sprintf(`"seq":%d}`, next)) && !strings.Contains(line, fmt.Sprintf(`"seq":%d,`, next)) {
				t.Fatalf("record %d was confirmed out of order: %s", next, line)
			}
			next++
		}
	}
	if r := sb.run(t, "log"); strings.Count(r.stdout, `"status":"acknowledged"`) != 200 || strings.Contains(r.stdout, `"waiting"`) {
		t.Errorf("after delivery the log should hold the newest 200 confirmed records")
	}

	// In the pod a command runs beside the worker, in the same budget.
	workerPeak := max(fill, drain)
	commandPeak := max(list, logCmd)
	t.Logf("worker filling the inbox %.1f MiB, delivering the spool %.1f MiB", mib(fill), mib(drain))
	t.Logf("list %.1f MiB, log %.1f MiB", mib(list), mib(logCmd))
	t.Logf("worker and largest command together: %.1f MiB of %d MiB", mib(workerPeak+commandPeak), budget>>20)
	if workerPeak+commandPeak > budget {
		t.Errorf("the worker and a command together use %d bytes, over the %d byte budget", workerPeak+commandPeak, budget)
	}

	// Nothing was written anywhere but the spool.
	empty(t, sb.cwd)
	empty(t, sb.tmp)
	empty(t, sb.home)
}
