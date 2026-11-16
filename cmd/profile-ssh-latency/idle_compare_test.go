package main

import "testing"

// Every cloud variant must see every probe type equally often in each position.
// Balanced resident permutations alone do not guarantee this: combining round
// and resident position correlated probe order with the chosen cloud variant.
func TestIdleProbeOrderBalancedAcrossVariants(t *testing.T) {
	var counts [3][3][3]int
	for round, order := range idleVariantOrders {
		for _, variant := range order {
			for turn := range 3 {
				counts[variant][idleProbeType(round, turn)][turn]++
			}
		}
	}
	for variant := range 3 {
		for probe := range 3 {
			for position := range 3 {
				if got := counts[variant][probe][position]; got != 2 {
					t.Errorf("variant=%d probe=%d position=%d: count=%d, want 2", variant, probe, position, got)
				}
			}
		}
	}
}

func TestIdleResidentAndProbePositionsAreJointlyBalanced(t *testing.T) {
	var counts [3][3][3][3]int
	for round := 0; round < 3*len(idleVariantOrders); round++ {
		for residentPosition, variant := range idleVariantOrders[round%len(idleVariantOrders)] {
			for probePosition := range 3 {
				counts[variant][residentPosition][idleProbeType(round, probePosition)][probePosition]++
			}
		}
	}
	for variant := range 3 {
		for residentPosition := range 3 {
			for probe := range 3 {
				for probePosition := range 3 {
					if got := counts[variant][residentPosition][probe][probePosition]; got != 2 {
						t.Errorf("variant=%d resident position=%d probe=%d probe position=%d: count=%d, want 2", variant, residentPosition, probe, probePosition, got)
					}
				}
			}
		}
	}
}
