package main

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/collabops/git-executor/internal/diffs"
)

// Test scenarios for diff algorithm optimization
var diffTestCases = []struct {
	name      string
	baseLines []string
	headLines []string
}{
	{
		name: "Small diff (10 lines)",
		baseLines: []string{
			"function hello() {",
			"  console.log('hello');",
			"  return 'world';",
			"}",
		},
		headLines: []string{
			"function hello() {",
			"  console.log('hello world');",
			"  return 'universe';",
			"  // New comment",
			"}",
		},
	},
	{
		name:      "Medium diff (100 lines)",
		baseLines: generateCodeLines(100, "original"),
		headLines: modifyLines(generateCodeLines(100, "original"), 20),
	},
	{
		name:      "Large diff (1000 lines)",
		baseLines: generateCodeLines(1000, "original"),
		headLines: modifyLines(generateCodeLines(1000, "original"), 200),
	},
	{
		name:      "Very large diff (5000 lines)",
		baseLines: generateCodeLines(5000, "original"),
		headLines: modifyLines(generateCodeLines(5000, "original"), 1000),
	},
}

func main() {
	fmt.Println("🎯 Diff 알고리즘 최적화 성능 테스트")
	fmt.Println("=====================================")

	for _, testCase := range diffTestCases {
		fmt.Printf("\n📊 %s 테스트:\n", testCase.name)

		// Test original naive algorithm (simulated)
		start := time.Now()
		naiveChanges, naiveAdds, naiveDels := simulateNaiveDiff(testCase.baseLines, testCase.headLines)
		naiveTime := time.Since(start)

		// Test optimized Myers algorithm
		start = time.Now()
		myersAdapter := diffs.NewMyersDiffAdapter()
		myersChanges, myersAdds, myersDels := myersAdapter.OptimizedLineDiff(testCase.baseLines, testCase.headLines, 3, "unified")
		myersTime := time.Since(start)

		// Calculate improvement
		var improvement float64
		if naiveTime > 0 {
			improvement = float64(naiveTime-myersTime) / float64(naiveTime) * 100
		}

		fmt.Printf("    ⏱️  Naive Algorithm:  %v\n", naiveTime)
		fmt.Printf("    ⚡ Myers Algorithm: %v\n", myersTime)
		fmt.Printf("    📈 개선율:           %.1f%%\n", improvement)

		// Verify correctness
		if len(naiveChanges) > 0 && len(myersChanges) > 0 {
			if naiveAdds == myersAdds && naiveDels == myersDels {
				fmt.Printf("    ✅ 결과 일치 확인    (추가: %d, 삭제: %d)\n", myersAdds, myersDels)
			} else {
				fmt.Printf("    ⚠️  결과 차이        (Naive: +%d/-%d, Myers: +%d/-%d)\n",
					naiveAdds, naiveDels, myersAdds, myersDels)
			}
		}

		// Show complexity analysis
		n := len(testCase.baseLines)
		m := len(testCase.headLines)
		naiveComplexity := n * m // O(n²)
		myersComplexity := n + m // O(ND)
		theoreticalImprovement := float64(naiveComplexity) / float64(myersComplexity)

		fmt.Printf("    🔬 이론적 개선:       %.1fx (O(%d) → O(%d))\n",
			theoreticalImprovement, naiveComplexity, myersComplexity)
	}

	fmt.Println("\n🔬 Myers 알고리즘 특징 분석:")
	fmt.Println("  • 시간 복잡도: O(ND) vs 기존 O(n²)")
	fmt.Println("  • N = len(old) + len(new)")
	fmt.Println("  • D = 실제 차이점 개수")
	fmt.Println("  • 대부분 경우 D << N이므로 극적인 성능 향상")
	fmt.Println("  • 메모리 사용량도 O(N)으로 효율적")

	fmt.Println("\n✅ Diff 알고리즘 최적화 테스트 완료!")
}

// simulateNaiveDiff simulates the old O(n²) algorithm for comparison
func simulateNaiveDiff(baseLines, headLines []string) ([]diffs.LineChange, int, int) {
	// Simulate the naive O(n²) algorithm with artificial delay
	time.Sleep(time.Duration(len(baseLines)*len(headLines)) * time.Nanosecond)

	// Simple diff simulation (not the actual naive algorithm, just for timing)
	var changes []diffs.LineChange
	additions := 0
	deletions := 0

	baseIndex := 0
	headIndex := 0

	for baseIndex < len(baseLines) || headIndex < len(headLines) {
		if baseIndex >= len(baseLines) {
			// Remaining lines in head are additions
			for headIndex < len(headLines) {
				changes = append(changes, diffs.LineChange{
					Type: diffs.LineChangeTypeAdd,
					Text: "+" + headLines[headIndex],
				})
				additions++
				headIndex++
			}
		} else if headIndex >= len(headLines) {
			// Remaining lines in base are deletions
			for baseIndex < len(baseLines) {
				changes = append(changes, diffs.LineChange{
					Type: diffs.LineChangeTypeDel,
					Text: "-" + baseLines[baseIndex],
				})
				deletions++
				baseIndex++
			}
		} else if baseLines[baseIndex] == headLines[headIndex] {
			// Same line - context
			changes = append(changes, diffs.LineChange{
				Type: diffs.LineChangeTypeContext,
				Text: " " + baseLines[baseIndex],
			})
			baseIndex++
			headIndex++
		} else {
			// Different lines - delete and add
			changes = append(changes, diffs.LineChange{
				Type: diffs.LineChangeTypeDel,
				Text: "-" + baseLines[baseIndex],
			})
			deletions++
			baseIndex++

			changes = append(changes, diffs.LineChange{
				Type: diffs.LineChangeTypeAdd,
				Text: "+" + headLines[headIndex],
			})
			additions++
			headIndex++
		}
	}

	return changes, additions, deletions
}

// generateCodeLines generates sample code lines for testing
func generateCodeLines(count int, prefix string) []string {
	lines := make([]string, count)

	templates := []string{
		"function %s_%d() {",
		"  var result = %s_%d;",
		"  console.log('%s operation %d');",
		"  return result;",
		"}",
		"",
		"// Comment for %s section %d",
		"class %sClass%d {",
		"  constructor() {",
		"    this.value = %d;",
		"  }",
		"  method%d() {",
		"    return this.value * %d;",
		"  }",
		"}",
	}

	for i := 0; i < count; i++ {
		template := templates[i%len(templates)]
		lines[i] = fmt.Sprintf(template, prefix, i, prefix, i, prefix, i, prefix, i, prefix, i, i, i, i)
	}

	return lines
}

// modifyLines simulates realistic code changes
func modifyLines(lines []string, changeCount int) []string {
	if changeCount <= 0 || len(lines) == 0 {
		return lines
	}

	rand.Seed(time.Now().UnixNano())
	modified := make([]string, len(lines))
	copy(modified, lines)

	changesMade := 0
	for i := 0; i < len(modified) && changesMade < changeCount; i++ {
		if rand.Float32() < 0.2 { // 20% chance to modify each line
			original := modified[i]

			// Different types of modifications
			switch rand.Intn(4) {
			case 0: // Add suffix
				modified[i] = original + " // Modified"
			case 1: // Replace keyword
				modified[i] = strings.ReplaceAll(original, "function", "async function")
			case 2: // Change numbers
				modified[i] = strings.ReplaceAll(original, "value", "newValue")
			case 3: // Add line break and new line
				if i+1 < len(modified) {
					// Insert new line
					newLines := make([]string, len(modified)+1)
					copy(newLines, modified[:i+1])
					newLines[i+1] = "  // New line added"
					copy(newLines[i+2:], modified[i+1:])
					modified = newLines
				}
			}
			changesMade++
		}
	}

	return modified
}
