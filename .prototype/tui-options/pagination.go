package main

import (
	"fmt"
)

func listPosition(start, end, total, selected int) string {
	if total == 0 {
		return "0 matches"
	}
	return fmt.Sprintf("%d–%d of %d · selected %d", start+1, end, total, selected+1)
}
