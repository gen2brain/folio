package font

// Upright reports a character drawn as it is written in vertical text rather
// than turned a quarter with the line, UAX #50.
func Upright(r rune) bool {
	lo, hi := 0, len(uprightTable)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if uprightTable[mid].hi < r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return uprightTable[lo].up
}
