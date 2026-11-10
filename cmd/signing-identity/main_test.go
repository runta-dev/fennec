package main

import "testing"

func TestExactUniqueIdentity(t *testing.T) {
	fingerprint := "D718CB5FDC83FDD68A81B83398B7E1C426593C72"
	line := " 1) " + fingerprint + " \"Fennec Local Development\"\n"
	for _, query := range []string{"Fennec Local Development", fingerprint} {
		got, err := resolve(line, query)
		if err != nil || got != fingerprint {
			t.Fatal(got, err)
		}
	}
	if got, err := resolve(line+line, "Fennec Local Development"); err != nil || got != fingerprint {
		t.Fatal("duplicate listing of same certificate rejected", got, err)
	}
	other := " 2) AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA \"Fennec Local Development\"\n"
	for _, input := range []string{"", line + other} {
		if _, err := resolve(input, "Fennec Local Development"); err == nil {
			t.Fatal("ambiguous identity accepted")
		}
	}
	if _, err := resolve(line, "Fennec"); err == nil {
		t.Fatal("partial name accepted")
	}
}
