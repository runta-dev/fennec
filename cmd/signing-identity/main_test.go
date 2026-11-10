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
	for _, input := range []string{"", line + line} {
		if _, err := resolve(input, "Fennec Local Development"); err == nil {
			t.Fatal("ambiguous identity accepted")
		}
	}
	if _, err := resolve(line, "Fennec"); err == nil {
		t.Fatal("partial name accepted")
	}
}
