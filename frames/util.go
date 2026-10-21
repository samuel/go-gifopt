package frames

// countWriter counts the bytes written to it and discards them.
type countWriter struct {
	count int
}

func (w *countWriter) Write(p []byte) (int, error) {
	w.count += len(p)
	return len(p), nil
}

// smallestIntValueIndex returns the index of the smallest value in vs, the lowest
// such index when several match, or -1 when vs is empty.
func smallestIntValueIndex(vs []int) int {
	if len(vs) == 0 {
		return -1
	}
	minV := vs[0]
	minIx := 0
	for i, v := range vs {
		if v < minV {
			minV = v
			minIx = i
		}
	}
	return minIx
}
