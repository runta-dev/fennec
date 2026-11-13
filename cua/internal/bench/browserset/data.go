package browserset

import (
	"fmt"
	"strings"
)

// The data behind the generated pages. Everything is derived from fixed
// seeds, so every run serves the same bytes.

// Shipment is one row of the Larkspur Freight table.
type Shipment struct {
	ID, Date, From, To, Weight, Status string
}

// ShipmentsPerPage is the Larkspur table's page size.
const ShipmentsPerPage = 10

// The extraction task's target and its near-duplicates: the same digits
// in another order, on other pages.
const (
	TargetShipment = "LF-40731"
	TargetWeight   = "37.4"
)

var shipmentDecoys = map[int]Shipment{
	14:  {ID: "LF-40713", Weight: "73.4"},
	47:  {ID: "LF-40371", Weight: "34.7"},
	84:  {ID: TargetShipment, Weight: TargetWeight},
	106: {ID: "LF-47031", Weight: "47.3"},
}

// lcg is a small deterministic generator (Numerical Recipes constants).
type lcg uint32

func (l *lcg) next(n int) int {
	*l = *l*1664525 + 1013904223
	return int(uint32(*l)>>8) % n
}

// Shipments is the full table, newest first: 120 rows, the target on
// page 9.
func Shipments() []Shipment {
	towns := []string{"Ashby Wharf", "Brookmere", "Calder Bridge", "Dunmore Quay", "Eastholt", "Fallowfield", "Greyhaven", "Hollins Cross", "Ivybridge Lane", "Juniper Flats"}
	statuses := []string{"Delivered", "Delivered", "Delivered", "In transit", "Delivered", "Held at depot"}
	r := lcg(20261113)
	used := map[string]bool{}
	for _, d := range shipmentDecoys {
		used[d.ID] = true
	}
	out := make([]Shipment, 0, 120)
	for i := 0; i < 120; i++ {
		// Newest first: 2026-11-12 going back one day every row or two.
		day := 312 - i*3/2 // day of year 2026
		date := dateOfYear(2026, day)
		s := Shipment{Date: date, From: towns[r.next(len(towns))], To: towns[r.next(len(towns))], Status: statuses[r.next(len(statuses))]}
		for s.To == s.From {
			s.To = towns[r.next(len(towns))]
		}
		if d, ok := shipmentDecoys[i]; ok {
			s.ID, s.Weight = d.ID, d.Weight
		} else {
			for {
				id := fmt.Sprintf("LF-%05d", 10000+r.next(89999))
				if !used[id] && !sameDigits(id, TargetShipment) {
					s.ID = id
					used[id] = true
					break
				}
			}
			s.Weight = fmt.Sprintf("%d.%d", 1+r.next(95), r.next(10))
		}
		out = append(out, s)
	}
	return out
}

// sameDigits reports whether two ids use the same digits in any order, so
// that only the planted decoys look like the target.
func sameDigits(a, b string) bool {
	count := func(s string) [10]int {
		var c [10]int
		for _, ch := range s {
			if ch >= '0' && ch <= '9' {
				c[ch-'0']++
			}
		}
		return c
	}
	return count(a) == count(b)
}

func dateOfYear(year, day int) string {
	mdays := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	m := 0
	for day > mdays[m] {
		day -= mdays[m]
		m++
	}
	return fmt.Sprintf("%d-%02d-%02d", year, m+1, day)
}

// Invoice is one Copperleaf Utilities invoice.
type Invoice struct {
	No, Period, Status, Amount, Note string
}

// OverdueTotal is the sum of the overdue invoices' amounts.
const OverdueTotal = "1408.47"

// Invoices lists the account's invoices, newest first. Four are overdue:
// 212.40 + 87.15 + 1,045.00 + 63.92 = 1,408.47.
func Invoices() []Invoice {
	return []Invoice{
		{"INV-26-1108", "October 2026", "Due", "143.06", "Due by 30 November 2026."},
		{"INV-26-1007", "September 2026", "Overdue", "212.40", "Was due by 31 October 2026."},
		{"INV-26-0906", "August 2026", "Paid", "198.75", "Paid on 28 September 2026."},
		{"INV-26-0805", "July 2026", "Overdue", "87.15", "Was due by 31 August 2026."},
		{"INV-26-0704", "June 2026", "Paid", "176.30", "Paid on 25 July 2026."},
		{"INV-26-0603", "May 2026", "Paid", "160.12", "Paid on 24 June 2026."},
		{"INV-26-0502", "April 2026", "Overdue", "1,045.00", "Includes the meter replacement. Was due by 31 May 2026."},
		{"INV-26-0401", "March 2026", "Paid", "189.44", "Paid on 22 April 2026."},
		{"INV-26-0312", "February 2026", "Paid", "201.98", "Paid on 20 March 2026."},
		{"INV-26-0211", "January 2026", "Overdue", "63.92", "Balance left after a partial payment. Was due by 28 February 2026."},
		{"INV-26-0110", "December 2025", "Paid", "230.10", "Paid on 19 January 2026."},
		{"INV-25-1209", "November 2025", "Paid", "214.65", "Paid on 18 December 2025."},
		{"INV-25-1108", "October 2025", "Paid", "152.33", "Paid on 20 November 2025."},
		{"INV-25-1007", "September 2025", "Paid", "147.81", "Paid on 21 October 2025."},
		{"INV-25-0906", "August 2025", "Paid", "139.02", "Paid on 19 September 2025."},
		{"INV-25-0805", "July 2025", "Paid", "141.47", "Paid on 20 August 2025."},
		{"INV-25-0704", "June 2025", "Paid", "155.90", "Paid on 18 July 2025."},
		{"INV-25-0603", "May 2025", "Paid", "149.36", "Paid on 19 June 2025."},
	}
}

// Reports are the Marrowby circulation CSVs by file name.
var reportNames = []string{
	"circulation-2026-q3-draft.csv", "circulation-2026-q2-final.csv", "circulation-2026-q3-final.csv",
	"circulation-2025-q3-final.csv", "circulation-2026-q1-final.csv",
}

// Report returns a circulation CSV's content, or false for an unknown name.
func Report(name string) ([]byte, bool) {
	found := false
	for _, n := range reportNames {
		found = found || n == name
	}
	if !found {
		return nil, false
	}
	seed := uint32(0)
	for _, c := range name {
		seed = seed*31 + uint32(c)
	}
	r := lcg(seed)
	var b strings.Builder
	b.WriteString("branch,adult_loans,children_loans,ebook_loans\n")
	for _, br := range []string{"Central", "Eastgate", "Mill Road", "Riverside", "Upper Marrowby"} {
		fmt.Fprintf(&b, "%s,%d,%d,%d\n", br, 1200+r.next(4000), 600+r.next(2500), 300+r.next(1800))
	}
	fmt.Fprintf(&b, "# %s\n", strings.TrimSuffix(name, ".csv"))
	return []byte(b.String()), true
}
