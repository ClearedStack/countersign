package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/clearedstack/countersign/internal/queue"
)

var update = flag.Bool("update", false, "rewrite the expected files of the saved examples")

// TestSavedExamples runs every directory under testdata/cases.
//
// A case holds:
//
//	args              one argument per line; {store} is the store file and
//	                  {case} is the case directory
//	store.json        the store before the command (optional)
//	want.stdout       exact standard output
//	want.stderr       exact standard error
//	want.exit         exit code
//	want.store.json   the store after the command (optional)
//
// When want.store.json is absent the store must be byte-for-byte what it was
// before, or still absent: this is how "refused, nothing changed" is checked.
func TestSavedExamples(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("testdata", "cases", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no saved examples found: %v", err)
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "store.json")
			before, hadStore := readOptional(t, filepath.Join(dir, "store.json"))
			if hadStore {
				if err := os.WriteFile(storePath, before, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			var args []string
			for _, line := range strings.Split(strings.TrimSuffix(mustRead(t, filepath.Join(dir, "args")), "\n"), "\n") {
				line = strings.ReplaceAll(line, "{store}", storePath)
				line = strings.ReplaceAll(line, "{case}", dir)
				args = append(args, line)
			}

			var stdout, stderr bytes.Buffer
			code := run(args, &stdout, &stderr)

			after, hasAfter := readOptional(t, storePath)
			changed := hasAfter != hadStore || !bytes.Equal(before, after)

			if *update {
				write(t, filepath.Join(dir, "want.stdout"), stdout.Bytes())
				write(t, filepath.Join(dir, "want.stderr"), stderr.Bytes())
				write(t, filepath.Join(dir, "want.exit"), []byte(strconv.Itoa(code)+"\n"))
				os.Remove(filepath.Join(dir, "want.store.json"))
				if changed {
					write(t, filepath.Join(dir, "want.store.json"), after)
				}
				return
			}

			compare(t, "stdout", mustRead(t, filepath.Join(dir, "want.stdout")), stdout.String())
			compare(t, "stderr", mustRead(t, filepath.Join(dir, "want.stderr")), stderr.String())
			compare(t, "exit code", mustRead(t, filepath.Join(dir, "want.exit")), strconv.Itoa(code)+"\n")
			wantStore, wantChange := readOptional(t, filepath.Join(dir, "want.store.json"))
			switch {
			case wantChange:
				compare(t, "store", string(wantStore), string(after))
			case changed:
				t.Errorf("the store changed and the example says it must not:\n%s", after)
			}
		})
	}
}

// TestEveryAllowedOutcomeHasASavedExample fails when a row is added to the
// table of allowed decisions without a saved example of it.
func TestEveryAllowedOutcomeHasASavedExample(t *testing.T) {
	for _, r := range queue.Rules() {
		dir := filepath.Join("testdata", "cases", "decide-"+r.Kind+"-"+r.From+"-"+r.Outcome)
		if _, err := os.Stat(filepath.Join(dir, "want.store.json")); err != nil {
			t.Errorf("no saved example at %s", dir)
		}
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readOptional(t *testing.T, path string) ([]byte, bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return data, true
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func compare(t *testing.T, what, want, got string) {
	t.Helper()
	if want != got {
		t.Errorf("%s differs\n--- want\n%s\n--- got\n%s", what, want, got)
	}
}
