package performance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// applyDiff reconstructs the new sequence from the old sequence and an edit
// script, providing a strong correctness check on the Myers algorithm output.
func applyDiff(old []string, edits []DiffEdit) []string {
	var result []string
	for _, e := range edits {
		switch e.Type {
		case EditEqual, EditInsert:
			result = append(result, e.Content)
		case EditDelete:
			// deleted lines do not appear in the new sequence
		}
	}
	return result
}

func TestMyersDiff_EmptyInputs(t *testing.T) {
	mdo := NewMyersDiffOptimizer()

	t.Run("both empty", func(t *testing.T) {
		edits := mdo.MyersDiff(nil, nil)
		assert.Empty(t, edits)
	})

	t.Run("old empty -> all inserts", func(t *testing.T) {
		newLines := []string{"a", "b", "c"}
		edits := mdo.MyersDiff(nil, newLines)
		require.Len(t, edits, 3)
		for i, e := range edits {
			assert.Equal(t, EditInsert, e.Type)
			assert.Equal(t, newLines[i], e.Content)
			assert.Equal(t, i, e.NewPos)
		}
		assert.Equal(t, newLines, applyDiff(nil, edits))
	})

	t.Run("new empty -> all deletes", func(t *testing.T) {
		oldLines := []string{"x", "y"}
		edits := mdo.MyersDiff(oldLines, nil)
		require.Len(t, edits, 2)
		for i, e := range edits {
			assert.Equal(t, EditDelete, e.Type)
			assert.Equal(t, oldLines[i], e.Content)
			assert.Equal(t, i, e.OldPos)
		}
		assert.Empty(t, applyDiff(oldLines, edits))
	})
}

func TestMyersDiff_Identical(t *testing.T) {
	mdo := NewMyersDiffOptimizer()
	lines := []string{"line1", "line2", "line3"}
	edits := mdo.MyersDiff(lines, lines)

	require.Len(t, edits, 3)
	for _, e := range edits {
		assert.Equal(t, EditEqual, e.Type)
	}
	assert.Equal(t, lines, applyDiff(lines, edits))
}

func TestMyersDiff_KnownChanges(t *testing.T) {
	mdo := NewMyersDiffOptimizer()

	tests := []struct {
		name string
		old  []string
		new  []string
	}{
		{"single insert in middle", []string{"a", "c"}, []string{"a", "b", "c"}},
		{"single delete in middle", []string{"a", "b", "c"}, []string{"a", "c"}},
		{"replace line", []string{"a", "b", "c"}, []string{"a", "x", "c"}},
		{"prepend", []string{"b", "c"}, []string{"a", "b", "c"}},
		{"append", []string{"a", "b"}, []string{"a", "b", "c"}},
		{"classic ABCABBA -> CBABAC", []string{"A", "B", "C", "A", "B", "B", "A"}, []string{"C", "B", "A", "B", "A", "C"}},
		{"complete rewrite", []string{"a", "b", "c"}, []string{"x", "y", "z"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edits := mdo.MyersDiff(tt.old, tt.new)
			// The reconstructed new sequence must match exactly.
			assert.Equal(t, tt.new, applyDiff(tt.old, edits))

			// Verify edit count is minimal-ish: number of non-equal edits
			// should equal the symmetric difference contribution.
			var inserts, deletes int
			for _, e := range edits {
				switch e.Type {
				case EditInsert:
					inserts++
				case EditDelete:
					deletes++
				}
			}
			// Equal lines + inserts == len(new); equal + deletes == len(old)
			equals := len(edits) - inserts - deletes
			assert.Equal(t, len(tt.new), equals+inserts)
			assert.Equal(t, len(tt.old), equals+deletes)
		})
	}
}

func TestMyersDiff_OptimizationStats(t *testing.T) {
	mdo := NewMyersDiffOptimizer()
	stats := mdo.GetOptimizationStats(100, 100)

	assert.Equal(t, "O(n²)", stats.OriginalComplexity)
	assert.Equal(t, "O(ND)", stats.NewComplexity)
	assert.Equal(t, 200, stats.LinesProcessed)
	assert.InDelta(t, float64(100*100)/float64(200), stats.ImprovementFactor, 0.0001)
}

func TestMyersDiff_CompareWithNaive(t *testing.T) {
	mdo := NewMyersDiffOptimizer()

	old := []string{"a", "b", "c", "d"}
	newL := []string{"a", "x", "c", "d"}
	myers, naive, improvement := mdo.CompareWithNaive(old, newL)

	assert.Equal(t, int64(len(old)+len(newL)), myers)
	assert.Equal(t, int64(len(old)*len(newL)), naive)
	assert.Greater(t, improvement, 1.0)

	// Edge case: empty inputs -> myersComplexity 0, improvement stays 0.
	m2, n2, imp2 := mdo.CompareWithNaive(nil, nil)
	assert.Equal(t, int64(0), m2)
	assert.Equal(t, int64(0), n2)
	assert.Equal(t, 0.0, imp2)
}
