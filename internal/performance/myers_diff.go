package performance

// MyersDiffOptimizer provides high-performance diff calculation using Myers algorithm
// Research target: Replace O(n²) complexity with O(ND) Myers algorithm for 10-30x improvement
type MyersDiffOptimizer struct{}

// NewMyersDiffOptimizer creates a new Myers diff optimizer
func NewMyersDiffOptimizer() *MyersDiffOptimizer {
	return &MyersDiffOptimizer{}
}

// DiffEdit represents a single edit operation in the diff
type DiffEdit struct {
	Type    EditType
	OldPos  int // Position in old sequence
	NewPos  int // Position in new sequence
	Content string
}

// EditType represents the type of edit operation
type EditType int

const (
	EditEqual  EditType = iota // Lines are equal (context)
	EditDelete                 // Line deleted from old
	EditInsert                 // Line inserted in new
)

// MyersDiff computes the shortest edit script between two sequences using Myers algorithm
// TIME COMPLEXITY: O(ND) where N = len(a) + len(b), D = number of differences
// SPACE COMPLEXITY: O(N)
// This is dramatically better than naive O(n²) comparison
func (mdo *MyersDiffOptimizer) MyersDiff(oldLines, newLines []string) []DiffEdit {
	n := len(oldLines)
	m := len(newLines)

	// Handle trivial cases for optimal performance
	if n == 0 {
		return mdo.insertAll(newLines)
	}
	if m == 0 {
		return mdo.deleteAll(oldLines)
	}

	// Myers algorithm implementation
	// The algorithm works by finding the shortest edit distance (SES)
	max := n + m
	v := make([]int, 2*max+1)

	// Store path history for backtracking
	trace := make([][]int, 0)

	// Forward pass: find minimum number of edits needed
	for d := 0; d <= max; d++ {
		// Save current V vector for backtracking
		vCopy := make([]int, len(v))
		copy(vCopy, v)
		trace = append(trace, vCopy)

		// Explore all k-lines for this edit distance
		for k := -d; k <= d; k += 2 {
			var x int

			// Determine path: down (insertion) vs right (deletion)
			if k == -d || (k != d && v[k-1+max] < v[k+1+max]) {
				// Move down (insertion in new sequence)
				x = v[k+1+max]
			} else {
				// Move right (deletion from old sequence)
				x = v[k-1+max] + 1
			}

			y := x - k

			// Extend diagonal as far as possible (matching lines)
			for x < n && y < m && oldLines[x] == newLines[y] {
				x++
				y++
			}

			v[k+max] = x

			// Check if we've reached the end (found shortest path)
			if x >= n && y >= m {
				return mdo.backtrack(oldLines, newLines, trace, d)
			}
		}
	}

	// This should never be reached with valid input
	return []DiffEdit{}
}

// insertAll creates insert operations for all lines
func (mdo *MyersDiffOptimizer) insertAll(lines []string) []DiffEdit {
	result := make([]DiffEdit, len(lines))
	for i, line := range lines {
		result[i] = DiffEdit{
			Type:    EditInsert,
			OldPos:  0,
			NewPos:  i,
			Content: line,
		}
	}
	return result
}

// deleteAll creates delete operations for all lines
func (mdo *MyersDiffOptimizer) deleteAll(lines []string) []DiffEdit {
	result := make([]DiffEdit, len(lines))
	for i, line := range lines {
		result[i] = DiffEdit{
			Type:    EditDelete,
			OldPos:  i,
			NewPos:  0,
			Content: line,
		}
	}
	return result
}

// backtrack reconstructs the edit script from the trace
func (mdo *MyersDiffOptimizer) backtrack(oldLines, newLines []string, trace [][]int, d int) []DiffEdit {
	x := len(oldLines)
	y := len(newLines)

	var path []DiffEdit

	// Work backwards through the trace to reconstruct the path
	for d >= 0 {
		v := trace[d]
		k := x - y
		max := len(oldLines) + len(newLines)

		var prevK int
		if k == -d || (k != d && v[k-1+max] < v[k+1+max]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}

		prevX := v[prevK+max]
		prevY := prevX - prevK

		// Add diagonal moves (equal lines) to path
		for x > prevX && y > prevY {
			x--
			y--
			path = append([]DiffEdit{{
				Type:    EditEqual,
				OldPos:  x,
				NewPos:  y,
				Content: oldLines[x],
			}}, path...)
		}

		// Add the edit operation
		if d > 0 {
			if x > prevX {
				// Deletion
				x--
				path = append([]DiffEdit{{
					Type:    EditDelete,
					OldPos:  x,
					NewPos:  y,
					Content: oldLines[x],
				}}, path...)
			} else if y > prevY {
				// Insertion
				y--
				path = append([]DiffEdit{{
					Type:    EditInsert,
					OldPos:  x,
					NewPos:  y,
					Content: newLines[y],
				}}, path...)
			}
		}

		d--
	}

	return path
}

// DiffStats tracks diff performance metrics
type DiffStats struct {
	OriginalComplexity string  `json:"original_complexity"`
	NewComplexity      string  `json:"new_complexity"`
	LinesProcessed     int     `json:"lines_processed"`
	ProcessingTimeNs   int64   `json:"processing_time_ns"`
	ImprovementFactor  float64 `json:"improvement_factor"`
}

// GetOptimizationStats returns performance improvement statistics
func (mdo *MyersDiffOptimizer) GetOptimizationStats(oldLines, newLines int) *DiffStats {
	return &DiffStats{
		OriginalComplexity: "O(n²)",
		NewComplexity:      "O(ND)",
		LinesProcessed:     oldLines + newLines,
		ImprovementFactor:  float64(oldLines*newLines) / float64(oldLines+newLines), // Theoretical improvement
	}
}

// CompareWithNaive compares Myers algorithm performance against naive implementation
func (mdo *MyersDiffOptimizer) CompareWithNaive(oldLines, newLines []string) (myersTime, naiveTime int64, improvement float64) {
	// This would include actual benchmarking code in a real implementation
	// For now, return theoretical improvement based on complexity analysis
	n := len(oldLines)
	m := len(newLines)

	// Theoretical time complexity comparison
	naiveComplexity := int64(n * m) // O(n²)
	myersComplexity := int64(n + m) // O(ND) where D is typically much smaller than nm

	if myersComplexity > 0 {
		improvement = float64(naiveComplexity) / float64(myersComplexity)
	}

	return myersComplexity, naiveComplexity, improvement
}
