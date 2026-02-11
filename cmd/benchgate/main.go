package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type baselineFile struct {
	GeneratedAt time.Time               `json:"generated_at"`
	GoVersion   string                  `json:"go_version"`
	Benchtime   string                  `json:"benchtime"`
	Pkg         string                  `json:"pkg"`
	BenchRegex  string                  `json:"bench_regex"`
	Benches     map[string]benchMetrics `json:"benches"`
}

type benchMetrics struct {
	NsPerOp     int64 `json:"ns_per_op"`
	BytesPerOp  int64 `json:"bytes_per_op"`
	AllocsPerOp int64 `json:"allocs_per_op"`
}

type benchResult struct {
	Name        string
	NsPerOp     int64
	BytesPerOp  int64
	AllocsPerOp int64
}

// go test -json emits benchmark lines like:
// "Benchmark_X-8\t123\t456 ns/op\t789 B/op\t10 allocs/op"
// or with decimals: "Benchmark_Y-8\t123\t351.4 ns/op\t400 B/op\t6 allocs/op"
var benchLineRE = regexp.MustCompile(`^(Benchmark[^\s]+)\s+\d+\s+([\d.]+)\s+ns/op(?:\s+(\d+)\s+B/op)?(?:\s+(\d+)\s+allocs/op)?\s*$`)

func main() {
	var (
		pkg        = flag.String("pkg", "./internal/overview", "Go package to benchmark (go test target)")
		benchRegex = flag.String("bench", `Benchmark_(GetLatestCommitsOnly|BlobSizeFillOnly|GetTreeWithCommit_Full|LatestCommitsCache_.*WarmHit)`, "Benchmark regex")
		benchtime  = flag.String("benchtime", "1s", "benchtime passed to go test -bench")
		baseline   = flag.String("baseline", ".benchmarks/overview_baseline.json", "Path to baseline JSON")
		update     = flag.Bool("update", false, "Update/write baseline instead of checking")
		maxRegress = flag.Float64("max-regress", 0.15, "Max allowed regression (e.g. 0.15 allows +15%) for ns/op, B/op, allocs/op")
	)
	flag.Parse()

	results, err := runBench(*pkg, *benchRegex, *benchtime)
	if err != nil {
		fail("bench run failed: %v", err)
	}

	if *update {
		if err := writeBaseline(*baseline, *pkg, *benchRegex, *benchtime, results); err != nil {
			fail("write baseline failed: %v", err)
		}
		fmt.Printf("benchgate: baseline updated: %s\n", *baseline)
		return
	}

	base, err := readBaseline(*baseline)
	if err != nil {
		fail("read baseline failed: %v", err)
	}

	// Safety: ensure check uses the same bench regex/pkg intent.
	if base.Pkg != *pkg {
		fail("baseline pkg mismatch: baseline=%q current=%q", base.Pkg, *pkg)
	}
	if base.BenchRegex != *benchRegex {
		fail("baseline bench regex mismatch: baseline=%q current=%q", base.BenchRegex, *benchRegex)
	}

	violations := checkRegression(base, results, *maxRegress)
	violations = append(violations, checkWarmHitBudgets(results)...)

	printSummary(base, results)

	if len(violations) > 0 {
		sort.Strings(violations)
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, "benchgate:", v)
		}
		os.Exit(1)
	}
	fmt.Println("benchgate: ok")
}

func runBench(pkg, benchRegex, benchtime string) (map[string]benchResult, error) {
	// go test -json ./pkg -run '^$' -bench '...' -benchmem -count=1 -benchtime=...
	args := []string{
		"test",
		"-json",
		pkg,
		"-run", "^$",
		"-bench", benchRegex,
		"-benchmem",
		"-count=1",
		"-benchtime", benchtime,
	}

	cmd := exec.Command("go", args...)
	cmd.Env = os.Environ()

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	type event struct {
		Action string `json:"Action"`
		Output string `json:"Output"`
	}

	results := make(map[string]benchResult)

	sc := bufio.NewScanner(bytes.NewReader(out.Bytes()))
	for sc.Scan() {
		line := sc.Text()
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Action != "output" {
			continue
		}

		// go test -json puts raw output lines under Output, including newlines.
		for _, ol := range strings.Split(e.Output, "\n") {
			ol = strings.TrimSpace(ol)
			if !strings.HasPrefix(ol, "Benchmark") {
				continue
			}
			m := benchLineRE.FindStringSubmatch(ol)
			if len(m) == 0 {
				continue
			}

			name := stripGOMAXPROCS(m[1])
			nsF, _ := strconv.ParseFloat(m[2], 64)
			nsPerOp := int64(nsF + 0.5)
			var bPerOp, allocsPerOp int64
			if m[3] != "" {
				bPerOp, _ = strconv.ParseInt(m[3], 10, 64)
			}
			if m[4] != "" {
				allocsPerOp, _ = strconv.ParseInt(m[4], 10, 64)
			}

			results[name] = benchResult{
				Name:        name,
				NsPerOp:     nsPerOp,
				BytesPerOp:  bPerOp,
				AllocsPerOp: allocsPerOp,
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no benchmark results parsed (benchRegex=%q pkg=%q)", benchRegex, pkg)
	}
	return results, nil
}

func stripGOMAXPROCS(name string) string {
	// Benchmark_Foo-8 -> Benchmark_Foo
	if i := strings.LastIndex(name, "-"); i > 0 {
		if _, err := strconv.Atoi(name[i+1:]); err == nil {
			return name[:i]
		}
	}
	return name
}

func writeBaseline(path, pkg, benchRegex, benchtime string, results map[string]benchResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	out := baselineFile{
		GeneratedAt: time.Now().UTC(),
		GoVersion:   runtime.Version(),
		Benchtime:   benchtime,
		Pkg:         pkg,
		BenchRegex:  benchRegex,
		Benches:     make(map[string]benchMetrics, len(results)),
	}
	for k, v := range results {
		out.Benches[k] = benchMetrics{
			NsPerOp:     v.NsPerOp,
			BytesPerOp:  v.BytesPerOp,
			AllocsPerOp: v.AllocsPerOp,
		}
	}

	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

func readBaseline(path string) (baselineFile, error) {
	var b baselineFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, err
	}
	if b.Benches == nil {
		b.Benches = map[string]benchMetrics{}
	}
	return b, nil
}

func checkRegression(base baselineFile, cur map[string]benchResult, maxRegress float64) []string {
	var violations []string

	names := make([]string, 0, len(base.Benches))
	for n := range base.Benches {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		bm := base.Benches[name]
		cr, ok := cur[name]
		if !ok {
			violations = append(violations, fmt.Sprintf("missing benchmark result: %s", name))
			continue
		}

		// ns/op
		if regressedRatio(cr.NsPerOp, bm.NsPerOp) > 1.0+maxRegress {
			violations = append(violations, fmt.Sprintf("%s regressed ns/op: base=%d cur=%d", name, bm.NsPerOp, cr.NsPerOp))
		}
		// B/op (0 may occur if bench doesn't report; only compare if baseline had it)
		if bm.BytesPerOp > 0 && regressedRatio(cr.BytesPerOp, bm.BytesPerOp) > 1.0+maxRegress {
			violations = append(violations, fmt.Sprintf("%s regressed B/op: base=%d cur=%d", name, bm.BytesPerOp, cr.BytesPerOp))
		}
		// allocs/op
		if bm.AllocsPerOp > 0 && regressedRatio(cr.AllocsPerOp, bm.AllocsPerOp) > 1.0+maxRegress {
			violations = append(violations, fmt.Sprintf("%s regressed allocs/op: base=%d cur=%d", name, bm.AllocsPerOp, cr.AllocsPerOp))
		}
	}
	return violations
}

func regressedRatio(cur, base int64) float64 {
	if base <= 0 {
		return 1.0
	}
	return float64(cur) / float64(base)
}

func checkWarmHitBudgets(cur map[string]benchResult) []string {
	// B) absolute budget gate for warm-hit cache path:
	// - should be "very fast", but keep thresholds loose enough to avoid CI flakes.
	const (
		maxNsPerOp     = int64(100_000) // 100us/op
		maxBytesPerOp  = int64(8_192)   // 8KB/op
		maxAllocsPerOp = int64(50)      // allocs/op
	)

	var violations []string
	for name, r := range cur {
		if !strings.Contains(name, "WarmHit") {
			continue
		}
		if r.NsPerOp > maxNsPerOp {
			violations = append(violations, fmt.Sprintf("%s warm-hit budget exceeded: ns/op=%d max=%d", name, r.NsPerOp, maxNsPerOp))
		}
		if r.BytesPerOp > maxBytesPerOp {
			violations = append(violations, fmt.Sprintf("%s warm-hit budget exceeded: B/op=%d max=%d", name, r.BytesPerOp, maxBytesPerOp))
		}
		if r.AllocsPerOp > maxAllocsPerOp {
			violations = append(violations, fmt.Sprintf("%s warm-hit budget exceeded: allocs/op=%d max=%d", name, r.AllocsPerOp, maxAllocsPerOp))
		}
	}
	return violations
}

func printSummary(base baselineFile, cur map[string]benchResult) {
	names := make([]string, 0, len(base.Benches))
	for n := range base.Benches {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Printf("benchgate: baseline=%s generated_at=%s go=%s benchtime=%s\n",
		filepath.Clean(".benchmarks/overview_baseline.json"),
		base.GeneratedAt.Format(time.RFC3339),
		base.GoVersion,
		base.Benchtime,
	)
	for _, name := range names {
		bm := base.Benches[name]
		cr := cur[name]
		fmt.Printf("benchgate: %s ns/op %s B/op %s allocs/op %s\n",
			name,
			fmt.Sprintf("%d→%d(%.2fx)", bm.NsPerOp, cr.NsPerOp, regressedRatio(cr.NsPerOp, bm.NsPerOp)),
			fmt.Sprintf("%d→%d(%.2fx)", bm.BytesPerOp, cr.BytesPerOp, regressedRatio(cr.BytesPerOp, bm.BytesPerOp)),
			fmt.Sprintf("%d→%d(%.2fx)", bm.AllocsPerOp, cr.AllocsPerOp, regressedRatio(cr.AllocsPerOp, bm.AllocsPerOp)),
		)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "benchgate: "+format+"\n", args...)
	os.Exit(1)
}
